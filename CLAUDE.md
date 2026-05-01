# CLAUDE.md — WS1 UEM Agent

This file is auto-loaded by Claude Code on every session start. It anchors design decisions and conventions across sessions. When in doubt, read it. Do not change locked decisions without explicit user confirmation in the current session.

## Project

A CLI + Skill pair (`ws1` + `ws1-uem`) that lets skill-capable agents operate Omnissa Workspace ONE UEM safely from natural-language goals. The CLI is the substrate; the skill teaches Claude-family agents to be fluent with it.

**Authoritative design doc:** `ws1-uem-agent-v0-spec.md`. Read this before starting any non-trivial work. Predecessor (idea doc, less binding): `ws1-uem-agent.md`.

## Locked decisions (do not change without user confirmation)

1. **Language: Go.** Single static binary, fast startup, easier distribution.
2. **CLI is self-describing** (`ws1 ops list`, `ws1 ops describe`, structured envelopes) so non-skill agents can still operate it. Skill is an accelerant for Claude-family runtimes, not a requirement.
3. **JSON envelope schema is versioned.** `envelope_version: 1`. Bump on any breaking shape change. See spec section 5 for the full shape and partial-success modeling.
4. **Finite error taxonomy.** Use only the codes in spec section 6. Add new codes via design discussion, not unilaterally.
5. **Three auth profiles: `ro`, `operator`, `admin`.** Default `ro`. Profile switch is user-initiated via `ws1 profile use <name>`. The CLI must refuse to switch profiles based on its own argv when called non-interactively from an agent.
6. **Strict OG context.** Every command requires `--og <id>` or a default set via `ws1 og use`. Missing OG returns `TENANT_REQUIRED`.
7. **Destructive ops always require browser approval.** Approval surface is an ephemeral HTTP server bound to `127.0.0.1:<random-port>` for the lifetime of a single CLI invocation. The agent makes one blocking call. The agent never handles approval tokens. See spec section 7 for the full sequence.
8. **Stale-resource freshness check.** At execute time, re-fetch the target and compare against the snapshot taken at approval time. Drift triggers `STALE_RESOURCE`; approval is **not** consumed.
9. **`operations.policy.yaml` is fail-closed.** Ops not present in the policy file are treated as destructive + approval-required, with a runtime warning. Format and starter content in spec section 8.
10. **Audit log:** `~/.config/ws1/audit.log`, JSONL, hash-chained. v1 limitation: writable by the agent's OS user. Document this in `SECURITY.md`.
11. **Headless mode is deferred to v2.** v1 requires a browser on the machine running the CLI.
12. **Skill layout:** five concept files (`01-domain-model`, `02-api-surface`, `03-targeting`, `04-safety`, `05-practices`), four pattern files, one auto-generated reference index. SKILL.md ~3-4k tokens. See spec section 10.
13. **OAuth client secrets live in OS keychain** (macOS Keychain, Windows DPAPI, Linux secret-service via libsecret). Never plaintext on disk. Fall-back to encrypted config file with passphrase only if keychain unavailable.

## Conventions

### Code

- Go 1.22+. `go fmt` clean, `go vet` clean, `golangci-lint` configured.
- Cobra for command tree. Viper for config.
- Every command returns the JSON envelope on stdout; logs/errors to stderr.
- Exit codes per spec section 5.7.
- No business logic in `cmd/` — keep that thin; logic lives in `internal/`.
- `internal/api/` wraps the WS1 REST client; `internal/policy/` loads and applies `operations.policy.yaml`; `internal/approval/` runs the browser server; `internal/audit/` writes the hash-chained log; `internal/envelope/` is the JSON serializer.
- Code-generated files (from `spec.json`) live in `internal/generated/` and are regenerated via `go generate ./...`. Do not edit by hand.

### Tests

- Unit tests next to code (`foo_test.go`).
- Integration tests in `test/integration/` — use recorded fixtures, not a real tenant.
- A `--record` mode on the CLI lets the user run against a real tenant and capture fixtures; CI replays only.
- Coverage target: 70% for `internal/`, higher for `internal/policy/` and `internal/approval/` (security-critical).

### Commits

- Small, incremental, focused. One logical change per commit.
- Conventional Commits format: `feat:`, `fix:`, `chore:`, `docs:`, `test:`, `refactor:`.
- Each commit must pass `go build`, `go test ./...`, and `golangci-lint run`.

### What NOT to do

- Do not call a real WS1 tenant from tests. Use recorded fixtures.
- Do not write OAuth secrets to disk in plaintext.
- Do not let the CLI switch profiles based on its argv when invoked non-interactively. Profile switches are user-initiated.
- Do not classify a new operation as anything less restrictive than destructive without user review.
- Do not mint approval tokens that persist across CLI invocations in v1. (v2 daemon will change this.)
- Do not paper over `IDENTIFIER_AMBIGUOUS` with a heuristic pick. Surface the choice to the user.
- Do not silently drop fields from the JSON envelope. Every field documented in the spec must appear (even if null).

## When stuck

If the task touches a locked decision and the right answer isn't obvious from the spec, **ask the user**. Don't guess on safety- or schema-relevant decisions. It's much cheaper to clarify than to refactor.

If you're stuck on a non-locked decision (e.g. how to structure a helper, which Go library to use), make the call and document the rationale in a `# Note:` comment so the user can review.

## Spec acquisition

WS1 UEM splits its API across multiple sections, each shipped as its own **OpenAPI 3.0.1** spec file. Specs are pulled directly from the user's tenant at:

```
https://<tenant>.awmdm.com/api/help/Docs/<section><version>
e.g. https://as1831.awmdm.com/api/help/Docs/mcmv1
```

Sections in scope: `mdmv1`, `mdmv2`, `mamv1`, `mcmv1`, `memv1`, `systemv1`. Each spec carries its own `servers[].url`, so section-to-base-URL mapping is data, not code.

**Layout:** save each spec as `spec/<section>v<n>.json`. Pin the tenant version that produced them in `spec/VERSION`. Surface drift as `SPEC_VERSION_MISMATCH`.

**Operation naming** (used by `operations.policy.yaml` and as our canonical op identifier):

```
<section-slug>.<tag-lowercase>.<verb-lowercase>
```

`<section-slug>` is the full versioned slug (e.g. `mcmv1`, `mdmv4`, `systemv2`) — preserves version because some sections (notably MDM) ship multiple concurrent versions with different op signatures. Derived from the OpenAPI document: `<section-slug>` from the file name (`mcmv1.json` → `mcmv1`), `<tag>` from `tags[0]` (`AwContents` → `awcontents`), `<verb>` from the `operationId` suffix after the underscore (`AwContents_SearchAsync` → `search`). Example: `mcmv1.awcontents.search`.

For the full procedure, discovery details, and code-gen rules, see `docs/spec-acquisition.md`. For the maintainer pipeline that automates spec sync and code-gen, see `docs/build-pipeline.md`.

## Demo target (v0 acceptance criteria)

End-to-end execution of: *"Find all devices for `alice@example.com`, show me a summary, then on my approval, lock them all."*

This must exercise: auth + OG context, user lookup with ambiguity handling, device list (read-class + pagination), summary rendering, single-vs-bulk decision, browser approval flow, bulk lock with partial-success envelope, and audit log entries with valid hash chain.

A `--dry-run` variant must complete the same flow without any state-changing API calls.

## Roadmap pointer

For the v0 task list, see spec section 12 ("Next concrete steps"). For parked questions, see spec section 13.

## Current release state

**Shipped:** [v0.1.0](https://github.com/xyzbuilds/ws1-uem-agent/releases/tag/v0.1.0) — initial PoC release, 2026-04-30. Five cross-compiled binaries on the release page; install one-liners in `README.md`.

**Release notes:** `docs/release-notes/v0.1.0.md` — feature list, install per platform, demo path for stakeholder calls, known limitations.

**License:** MIT (see `LICENSE`). Sole-authored through v0.1.0.

**Next cut:** see `docs/release-notes/v0.1.0.md` "What's next" for the v0.2 / v1 roadmap. Top items deferred from v0.1.0:
- Destructive op pre-flight summary (target name + owner + OG before approval round-trip).
- TTL on elevated profiles (auto-revert to `ro` after N minutes idle) — see `SECURITY.md`.
- `--profile <name>` per-invocation override that doesn't mutate the persisted active-profile file.

## Release workflow

```bash
make release VERSION=X.Y.Z       # cross-compile to dist/
$EDITOR docs/release-notes/vX.Y.Z.md
git commit -m "docs(release-notes): vX.Y.Z" && git push origin main
make release VERSION=X.Y.Z       # rebuild on the docs commit so version is clean
git tag -a vX.Y.Z -m "..."  &&  git push origin vX.Y.Z
gh release create vX.Y.Z --notes-file docs/release-notes/vX.Y.Z.md dist/ws1-* dist/SHA256SUMS
```

`make release` cross-compiles five targets (darwin arm64/amd64, linux amd64/arm64, windows amd64), CGO=0, statically linked, version-stamped, with SHA256SUMS. Don't skip the second `make release` — without it, the version stamp will say `-dirty`.

## Brand surface (locked at v0.1.0)

The teal mascot banner, "ws1 CLI" title, and "Workspace ONE UEM — agent-first CLI" tagline are locked. All banner surfaces (`ws1 setup`, bare `ws1`, future commands) go through `titleLine()` + `brandTagline` + `printBanner()` in `cmd/ws1/banner.go`.

Color/symbol vocabulary is consistent across every TTY surface:
- `read` → green, `write` → blue, `destructive` → red — wrapped via `colorByClass()`.
- Profile names colored same as their class (`ro` green, `operator` warn-yellow, `admin` red).
- Symbols: `✓` pass, `✗` fail, `·` skip, `⚠` always-requires-approval, `!` approval-at-scale, `→` async dispatch, `›` input prompt marker.
- Every visual decoration is gated on `stderrIsTTY()`; agents (no TTY) get plain text and identical JSON envelopes on stdout.

Don't invent new colors or symbols without checking this list — consistency is the point.
