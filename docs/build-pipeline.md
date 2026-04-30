# Build pipeline

How `ws1` stays current with the WS1 UEM API surface as Omnissa ships new tenant versions.

This is a **maintainer-side workflow** — users never run it. Output (refreshed specs, regenerated code, updated skill reference, classification reports) is committed to the repo. Users consume the resulting binary; they don't rebuild.

For background on the spec format and discovery surface, see `docs/spec-acquisition.md`. For locked design decisions, see `CLAUDE.md`.

## Goals

- **One command** to regenerate everything when WS1 ships a new tenant version (~quarterly).
- **Fail-closed on unclassified ops**: build fails until the maintainer classifies new ops in `operations.policy.yaml`.
- **Reproducible**: same tenant + same token = same output. Determinism matters because the output is committed.
- **Dev-loop friendly**: each stage is a separate tool, runnable independently for debugging.
- **Source-of-truth-aware**: the spec is data, not code. The pipeline is the only legitimate way to update `spec/`, `internal/generated/`, and `skills/ws1-uem/reference/`.

## Pipeline overview

```
make sync-specs TENANT=as1831.awmdm.com TOKEN=$WS1_TOKEN
       │
       ▼
┌────────────────────────────────────────┐
│ 1. ws1-build discover                  │  Hit /api/help, parse HTML, list sections
└────────────────┬───────────────────────┘
                 │ .build/sections.json
                 ▼
┌────────────────────────────────────────┐
│ 2. ws1-build fetch                     │  Pull each section spec; record version
└────────────────┬───────────────────────┘
                 │ spec/<slug>.json + spec/VERSION
                 ▼
┌────────────────────────────────────────┐
│ 3. ws1-build diff                      │  Compare to previous spec/; report deltas
└────────────────┬───────────────────────┘
                 │ .build/diff-report.json (informational)
                 ▼
┌────────────────────────────────────────┐
│ 4. ws1-build codegen-cli               │  Generate typed Go bindings + ops_index.go
└────────────────┬───────────────────────┘
                 │ internal/generated/*.go
                 ▼
┌────────────────────────────────────────┐
│ 5. ws1-build codegen-skill             │  Regenerate skill reference index
└────────────────┬───────────────────────┘
                 │ skills/ws1-uem/reference/operation-index.md
                 ▼
┌────────────────────────────────────────┐
│ 6. go build && go test ./...           │  Standard Go build + tests
└────────────────┬───────────────────────┘
                 │
                 ▼
┌────────────────────────────────────────┐
│ 7. ws1-build classify-check            │  Compare generated ops to policy.yaml
└────────────────┬───────────────────────┘
                 │ exit 0 if clean, 1 if unclassified ops exist
                 ▼
              success / fail
```

Any stage failing fails the whole pipeline. The classify-check at the end is the gate: new ops must be classified before the build is considered green.

## Stage 1 — `ws1-build discover`

**Purpose:** find all available API sections on a tenant without hardcoding.

**Inputs:** `--tenant <hostname>`

**Outputs:** `.build/sections.json` — JSON array of `{display_name, slug, version_suffix, spec_url, api_explorer_version}`.

**Steps:**

1. GET `https://<tenant>/api/help` (the index page; auth typically not required).
2. Parse HTML using `golang.org/x/net/html`.
3. Find every `<a href="/api/help/Docs/Explore?urls.primaryName=NAME">NAME</a>`.
4. Slugify each `NAME` per the rules in `docs/spec-acquisition.md` § Slugification.
5. Compose spec URL: `https://<tenant>/api/help/Docs/<slug>`.
6. Extract footer text matching `Workspace ONE UEM API Explorer <version>`. Capture `<version>` (e.g. `2025.11.6.68`).
7. Emit JSON to stdout (or to `--out` if specified).

**Failure modes:**

- Index page returns auth challenge → fail with `DISCOVERY_AUTH_REQUIRED`. (v0 assumes the index is unauthenticated; document if a tenant is configured otherwise.)
- HTML structure changes → fail with `DISCOVERY_STRUCTURE_UNRECOGNIZED`; emit raw HTML to stderr for debugging.
- Footer not found → warn but continue; record `api_explorer_version: "unknown"`.

**Example output:**

```json
{
  "tenant": "as1831.awmdm.com",
  "api_explorer_version": "2025.11.6.68",
  "discovered_at": "2026-04-30T14:00:00Z",
  "sections": [
    {"display_name": "MAM API V1", "slug": "mamv1", "spec_url": "https://as1831.awmdm.com/api/help/Docs/mamv1"},
    {"display_name": "MDM API V4", "slug": "mdmv4", "spec_url": "https://as1831.awmdm.com/api/help/Docs/mdmv4"}
  ]
}
```

## Stage 2 — `ws1-build fetch`

**Purpose:** pull each section's OpenAPI spec and update `spec/`.

**Inputs:** `--sections <path>` (output from stage 1), `--token <bearer>`, `--out <dir>` (default: `spec/`)

**Outputs:** `spec/<slug>.json` for each section; `spec/VERSION`.

**Steps:**

1. For each section in the input:
   - GET `<spec_url>` with `Authorization: Bearer <token>`.
   - Validate: response is JSON, has `openapi: "3.0.1"`, has non-empty `paths`.
   - Pretty-print (`json.MarshalIndent` with 2-space indent) for diff-friendliness.
   - Write to `<out>/<slug>.json`.
   - Compute SHA-256 of the pretty-printed bytes.
2. Write `<out>/VERSION` (YAML) — see schema in `docs/spec-acquisition.md` § Repo layout.

**Failure modes:**

- 401 Unauthorized → `FETCH_AUTH_FAILED`; abort.
- Non-OpenAPI response → `FETCH_INVALID_FORMAT`; abort with the offending body in stderr.
- Network errors → retry up to 3× with exponential backoff; fail with `FETCH_NETWORK_ERROR`.

**Pretty-printing matters.** Specs are committed; canonical formatting keeps diffs reviewable. JSON keys are NOT sorted (preserves OpenAPI document order, which often matters for human reading).

## Stage 3 — `ws1-build diff`

**Purpose:** describe what changed between the previous and the just-fetched specs. Informational only; doesn't fail the build.

**Inputs:** `--baseline <git-ref>` (default: `HEAD`), `--new <dir>` (default: `spec/`)

**Outputs:** `.build/diff-report.json`

**Steps:**

1. Git-checkout the baseline `spec/` to a temp dir.
2. For each section, parse old + new spec.
3. Build set of `(path, method, operationId)` tuples from each.
4. Categorize:
   - **Added**: in new, not in old.
   - **Removed**: in old, not in new.
   - **Changed**: in both, but `parameters` / `requestBody` / `responses` differ.
5. Emit structured JSON.

**Why bother:** when the maintainer is reviewing a sync-specs PR, this report tells them what to expect. Especially useful for `Changed` ops — those are signature drift and may need policy.yaml updates or Go binding regeneration that doesn't pass tests.

## Stage 4 — `ws1-build codegen-cli`

**Purpose:** regenerate Go code from `spec/`.

**Inputs:** `--specs <dir>` (default: `spec/`), `--out <dir>` (default: `internal/generated/`)

**Outputs:** `internal/generated/<slug>.go` per section; `internal/generated/ops_index.go` (shared metadata catalog).

**Steps:**

1. For each spec file in `<specs>/*.json`:
   - Parse as OpenAPI 3.0.1.
   - Derive section-slug from filename.
   - For each operation:
     - Compute op identifier per the rule in `docs/spec-acquisition.md` § Operation naming.
     - Generate typed args struct: `type <Tag><Verb>Args struct { ... }`.
     - Generate request builder: `func Build<Tag><Verb>(args <Tag><Verb>Args) (*http.Request, error)`.
     - Generate response parser: `func Parse<Tag><Verb>(body []byte) (<Tag><Verb>Response, error)`.
     - Add metadata entry to the shared index: `OpsIndex["<op-identifier>"] = OpMeta{...}`.
2. Server URL handling:
   - Read `servers[0].url` from the spec.
   - Strip the tenant hostname; keep only the path component (e.g. `/api/mcm`).
   - Generated client composes the runtime base URL (`https://<configured-tenant>` + section path).
3. Emit one Go file per section: `internal/generated/<slug>.go`.
4. Emit shared metadata: `internal/generated/ops_index.go`.
5. All generated files start with `// Code generated by ws1-build. DO NOT EDIT.`

**Conventions:**

- Generated package: `generated`.
- Generated files MUST be `gofmt`-clean.
- Generated types go through `internal/api/` for actual HTTP execution; the `generated` package only describes shape and metadata.

**Failure modes:**

- Verb collision within a tag → emit warning, append HTTP method as disambiguator.
- Missing `tags` AND missing first-path-segment → fail with `CODEGEN_TAG_AMBIGUOUS`.
- Schema reference unresolvable (`$ref: "#/components/schemas/X"` where X doesn't exist) → fail with `CODEGEN_SCHEMA_BROKEN`.

## Stage 5 — `ws1-build codegen-skill`

**Purpose:** regenerate the auto-generated skill reference.

**Inputs:** `--index <path>` (path to `internal/generated/ops_index.go` or its underlying metadata JSON), `--policy <path>` (path to `operations.policy.yaml`), `--out <dir>` (default: `skills/ws1-uem/reference/`)

**Outputs:** `skills/ws1-uem/reference/operation-index.md`

**Steps:**

1. Load the ops index.
2. Load the classification policy.
3. Generate a markdown table per section:

```markdown
## mdmv4

| Op identifier | Class | Reversible | Sync | Identifier | Summary |
|---|---|---|---|---|---|
| `mdmv4.devices.search` | read | — | sync | none | Search devices |
| `mdmv4.devices.lock` | write | full | sync | device_id | Lock device |
| `mdmv4.devices.wipe` | destructive | none | async | device_id | Wipe device |
```

4. Sort sections alphabetically; sort within section by tag, then verb.
5. Header with `auto-generated by ws1-build at <timestamp>; do not edit by hand`.

This is what `SKILL.md` points the agent at: "see `reference/operation-index.md` for op metadata."

## Stage 6 — Standard Go build + test

`go build ./...` and `go test ./...`. Failure here usually means code-gen produced something that doesn't compile (rare but possible if the spec contains shapes the generator doesn't handle).

## Stage 7 — `ws1-build classify-check`

**Purpose:** the gate. New ops must be classified in `operations.policy.yaml` before the build can succeed.

**Inputs:** `--index <path>`, `--policy <path>`

**Outputs:** human-readable report on stdout; exit code.

**Steps:**

1. Build set of all op identifiers from the index.
2. Build set of policy keys from `operations.policy.yaml` (exclude `__default__`).
3. **Unclassified** = in index, not in policy.
4. **Orphaned** = in policy, not in index.
5. For unclassified ops, suggest a class via heuristic (HTTP method GET → read; POST/PUT → write; DELETE → destructive). Suggestion is informational; do NOT auto-apply.
6. Print report:

```
Sync specs report
=================

Tenant: as1831.awmdm.com
Spec version: 2025.11.6.68 (was 2025.08.4.42)

Spec deltas:
  Added:    7 ops
  Removed:  2 ops
  Changed:  3 ops

Unclassified operations (3):
  - mdmv4.devices.refresh_compliance
    Heuristic: class=write (POST)
    Action: add to operations.policy.yaml

  - systemv2.audit.export
    Heuristic: class=read (GET)
    Action: add to operations.policy.yaml

  - mdmv4.devices.bulk_unenroll
    Heuristic: class=destructive (DELETE method)
    Action: add to operations.policy.yaml

Orphaned operations (2):
  - mdmv1.devices.deprecated_thing
  - mdmv1.legacy.removed_op
  Action: clean up operations.policy.yaml

Build aborted: 3 unclassified operations.
See docs/build-pipeline.md for classification policy.
```

**Exit codes:** 0 if no unclassified ops (orphans only warn); 1 otherwise.

This stage is non-negotiable. Auto-classifying new ops would silently weaken the safety gate.

## Orchestration

`Makefile`:

```makefile
.PHONY: sync-specs discover fetch diff codegen-cli codegen-skill build test classify-check tools

TENANT ?= as1831.awmdm.com
TOKEN ?=
BUILDDIR := .build

tools:
	go build -o bin/ws1-build ./cmd/ws1-build

sync-specs: tools $(BUILDDIR) discover fetch diff codegen-cli codegen-skill build test classify-check
	@echo "✓ Sync complete."

$(BUILDDIR):
	mkdir -p $(BUILDDIR)

discover: tools | $(BUILDDIR)
	./bin/ws1-build discover --tenant=$(TENANT) --out=$(BUILDDIR)/sections.json

fetch: discover
	@test -n "$(TOKEN)" || (echo "TOKEN required" && exit 1)
	./bin/ws1-build fetch --sections=$(BUILDDIR)/sections.json --token=$(TOKEN) --out=spec/

diff: fetch
	./bin/ws1-build diff --baseline=HEAD --new=spec/ --out=$(BUILDDIR)/diff-report.json

codegen-cli: fetch
	./bin/ws1-build codegen-cli --specs=spec/ --out=internal/generated/

codegen-skill: codegen-cli
	./bin/ws1-build codegen-skill --index=internal/generated/ops_index.go --policy=operations.policy.yaml --out=skills/ws1-uem/reference/

build:
	go build ./...

test: build
	go test ./...

classify-check: codegen-cli
	./bin/ws1-build classify-check --index=internal/generated/ops_index.go --policy=operations.policy.yaml
```

## When the maintainer runs this

- WS1 console footer reports a new API Explorer version.
- A user reports `SPEC_VERSION_MISMATCH` for an op repeatedly.
- Before tagging a CLI release.
- (v0.5+) Scheduled GitHub Action: weekly run against a reference tenant; opens a PR if changes are detected, with the diff report and the classification gaps inline.

## `ws1-build` distribution

`ws1-build` lives in `cmd/ws1-build/`. It's a developer tool, not user-facing. Built by `make tools`. NOT shipped in user releases. NOT added to `go install` instructions for end users.

## Future work (v0.5+)

- **Scheduled sync.** GitHub Actions cron weekly; opens PR on change.
- **Auto-PR with diff.** PR body includes the diff report and the classify-check report so reviewers can classify inline.
- **Reference tenant.** A sandbox tenant maintained for CI reproducibility; the scheduled sync pulls from there.
- **User-side bootstrap.** Shipped CLI auto-pulls user's tenant specs on first run, overlays at runtime, exposes generic `ws1 raw` for unmapped ops.
- **`ws1-build dry-run` mode.** Run the full pipeline without writing files; useful for "what would change?" inspection.
