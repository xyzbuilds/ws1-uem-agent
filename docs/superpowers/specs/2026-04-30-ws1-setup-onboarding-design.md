# ws1 setup — first-run onboarding

**Status:** Approved for implementation, 2026-04-30.
**Owner:** xyzbuilds.
**Builds on:** the v0 design in `ws1-uem-agent-v0-spec.md` (locked decisions #5, #13, #3).
**Successor:** an implementation plan written via the `superpowers:writing-plans` skill.

## 1. Problem

The current onboarding requires four commands and four pieces of upfront knowledge:

1. `ws1 profile add operator --tenant=... --client-id=... --client-secret=... --region=...` — needs tenant hostname, OAuth `client_id`, OAuth `client_secret`, region code
2. `ws1 profile use operator` — needs profile name
3. `ws1 og use <id>` — needs OG ID, only obtainable via the WS1 console
4. (Optional) verify with a real call

Reported friction: region codes are opaque without `ws1 profile regions` (chicken-and-egg), OG ID requires console-side lookup, the three-command sequence is easy to leave half-done (forgetting `profile use` leaves you stuck on `ro`), and the lone help text on `profile add` doesn't explain what `ro`/`operator`/`admin` mean or that the OAuth client's WS1 console role must match the chosen profile name.

## 2. Goals / non-goals

**Goals:**

- Single human-facing verb for first-run + reconfigure: `ws1 setup`.
- Eliminate the OG-lookup-in-WS1-console step by listing OGs from the API after auth succeeds.
- Make role-vs-profile alignment a first-class concern (the CLI's class gate is belt-and-braces; the OAuth client's WS1 console role is the API-side enforcer).
- Survive non-interactive callers (CI bootstrap) without prompting silently.

**Non-goals:**

- Replacing the underlying primitives. `profile add` / `profile use` / `og use` stay; `setup` orchestrates them.
- Daemon, REPL, or persistent-session shapes. v0 is one-shot per spec section 2.
- Mascot, personality, or playful copy. Tone is GitHub-CLI-neutral.

## 3. Decisions (locked during brainstorming)

- **Q1 — command shape:** `ws1 setup` (option A from brainstorm). Top-level verb that wraps the existing primitives. Agents continue to call the primitives directly; `setup` is human-only.
- **Q2 — profile scope per invocation:** **Default = QuickStart, operator only.** The multi-profile picker (option B from brainstorm) is preserved verbatim — descriptions of `ro`/`operator`/`admin` and the role-match warning all show — but lives behind `ws1 setup --advanced`. Rationale: the user's "reduce friction" intent in chat overrode the original Q2 answer once we saw the actual picker mocked out.
- **Q3 — validation + OG fetch:** validate via real OAuth round-trip then fetch OG list and let the user pick (option A). Fall back to a freeform OG-ID prompt if the OG-list call fails.
- **Adopted from Claude Code / OpenClaw inspection:**
  - QuickStart-vs-Advanced split (OpenClaw).
  - Single-character shortcuts for binary picks (Claude Code permission UI; uppercase = persistent / lowercase = ephemeral mnemonic).
  - In-place spinner replacement with Braille glyph + ASCII fallback (Claude Code).
  - Existing-config-as-defaults so re-runs are reconfiguration paths.
  - Final smoke test that runs a real read op (OpenClaw step 6).
  - "What's next?" exit block with concrete commands, not just "done".
- **Not adopted:** mascots, personality, slash-command syntax, modes/streaming, daemon registration, multi-model picker.

## 4. Design

### 4.1 Command shape

`ws1 setup` is a new top-level command. Two automatic modes selected by argv:

- **Interactive (TTY)** — zero or partial flags + stdout-is-a-terminal. Wizard prompts for the rest. Default path for humans.
- **Non-interactive** — all required flags supplied or stdout is not a terminal. Behaves equivalently to scripted `profile add` + `og use`. Refuses if anything is missing — no silent prompts on a CI pipe.

Re-running `ws1 setup` after initial config detects existing values and offers them as `[bracketed defaults]`. Setup is therefore the install path AND the reconfigure path AND the recovery-after-rotation path.

`ws1 setup --advanced` activates the multi-profile picker and the more detailed prompts. `ws1 setup --quick` is implicit (the default) but accepted as an explicit flag for clarity in scripts.

Help text examples:

- `ws1 setup --help` shows the QuickStart flow.
- `ws1 setup --advanced --help` shows the picker / multi-profile flow.

### 4.2 Wizard flow

Concrete prompt-by-prompt walkthrough. Bracketed defaults appear when re-running setup with existing config; first-time runs show the prompts unbracketed.

```
$ ws1 setup

ws1 setup — connect ws1 to your Workspace ONE UEM tenant

Tenant hostname (e.g. cn1506.awmdm.com): cn1506.awmdm.com

Region:
  [1] uat     Ohio (UAT environment)
  [2] na      Virginia — US, Canada
  [3] emea    Frankfurt — UK, Germany
  [4] apac    Tokyo — India, Japan, Singapore, Australia, Hong Kong
Pick: 2

Profile: operator    (--advanced to configure ro and admin too)

Provision an OAuth client at:
  WS1 Console > Groups & Settings > Configurations > OAuth Client Management
Assign a role appropriate for 'operator' (device management).
The CLI's class gate is belt-and-braces; the OAuth role is the API-side
enforcer. Match them.

Client ID:     a15c39a70ffb4a219ee770af34379324
Client Secret: ********

  ⠹ Validating against na.uemauth.workspaceone.com...
  ✓ Token issued (3600s)

  ⠙ Fetching organization groups...
  ✓ Found 3 OGs.

    [1] Global       (id 1)
    [2] EMEA         (id 2042)
    [3] EMEA-Pilot   (id 4067)
Pick: 3

  ⠦ Smoke test: ws1 mdmv1 devices search --pagesize 1
  ✓ Received 1 device.

──────────────────────────────────────────────────
Setup complete.

Active profile: operator    Tenant: cn1506.awmdm.com    OG: 4067 (EMEA-Pilot)

Try:
  ws1 doctor
  ws1 ops list | jq '.data.count'
  ws1 mdmv1 devices search --pagesize 5

Switch to a safer default for agent sessions:
  ws1 setup --advanced   # configure 'ro' too
──────────────────────────────────────────────────
```

`--advanced` adds, between the region prompt and the credential prompt, a multi-profile picker:

```
Profiles
  Three capability tiers. The OAuth client you provision in the WS1
  console for each profile MUST have the matching role assigned —
  otherwise the CLI's client-side gate is your only defence.

    ro        Read-only. Search and inspect; never changes state.
              Pair with a "Read-only API" role OAuth client.
    operator  Read + write reversible ops (lock device, install
              profile, push command). Destructive ops still require
              browser approval. Pair with a device-management role.
    admin     Same CLI capabilities as operator. Convention: pair
              with a Console Administrator role OAuth client for
              tenant-level configuration (org groups, admins).

  Which to configure now? (comma-separated)
  [operator]: operator,ro
```

Each chosen profile then runs through the credential + validation block in turn. After all profiles are validated, the OG fetch runs once. Profile preference for the OG fetch: `operator` first (most likely to have org-group read scope), then `admin`, then `ro`. The picked OG becomes the global default; per-profile OG override is not supported in v0.1.

When multiple profiles are configured, the active profile is set to `ro` if `ro` is in the list (safer default; matches `SKILL.md`'s principle stack); otherwise to `operator`; otherwise to whichever profile was configured first.

**Re-running setup when a profile already exists:** the wizard prompts `Profile <name> exists. [O]verwrite / [k]eep existing / [c]ancel` (single-char shortcut). Overwrite re-prompts for credentials. Keep advances to the next step using the existing values. Cancel aborts.

### 4.3 UX primitives

- **Spinner glyph:** `⠋ ⠙ ⠹ ⠸ ⠼ ⠴ ⠦ ⠧ ⠇ ⠏` (Braille). ASCII fallback `| / - \` when terminal capabilities don't include Unicode (detected via `LC_ALL`/`LANG` having `UTF-8` or via `golang.org/x/term`'s capability hints).
- **Replacement semantics:** spinner line is overwritten in place by the result line on completion. ANSI `\r` + clear-to-end-of-line. No append.
- **Imperative tense** during work (`Validating...`, `Fetching organization groups...`); past tense or sigil at completion (`✓ Token issued`, `✓ Found 3 OGs.`).
- **Single-char shortcuts** for binary choices (overwrite-existing-profile, use-this-OG-when-only-one). Not used for region (4 options) or profile picker (3 + "all") — letters are ambiguous when both `e` and `a` could match `emea` and `apac`.
- **Hidden secret input:** `golang.org/x/term`'s `ReadPassword` for `--client-secret`.

### 4.4 Code touch points

**New files:**

- `cmd/ws1/setup.go` — Cobra command and orchestrator. Hosts the step-by-step flow, sequence transitions, error fallbacks, and the exit summary block. Approximately 300 lines.
- `cmd/ws1/prompt.go` — TTY UX primitives behind a `Prompter` interface so tests stub it. Methods: `Ask(label, default) (string, error)`, `AskSecret(label) (string, error)`, `Pick(label string, options []PickItem) (PickItem, error)`, `PickByLetter(label string, options map[byte]string) (byte, error)`, `Spinner(label string) *Spinner` with `(*Spinner).Done(ok bool, result string)`. Approximately 150 lines, no new dependencies (uses `golang.org/x/term`, already in `go.mod`).
- `cmd/ws1/status.go` — `ws1 status` subcommand emitting one envelope summarising `{profile, og, tenant, region, audit_seq, last_op_at}`. Reads existing config + tail of audit log. Approximately 80 lines. Independent of `setup` but ships in the same change.

**Modified files:**

- `cmd/ws1/root.go` — `cmd.AddCommand(newSetupCmd(), newStatusCmd())` at top level, alongside the existing `doctor` / `profile` / `og` / `ops` / `audit` commands.

**Unchanged:**

`profile add`, `profile use`, `og use`, the API client, the policy loader, the approval server, audit log — all stay as primitives.

### 4.5 OG-list operation

The wizard calls a section's organization-group search op to populate the OG picker. Preference order:

1. `systemv2.organizationgroupsv2.search` if present.
2. `systemv2.organizationgroupsv2.searchorganizationgroups` or similar v2 variant.
3. `systemv1.organizationgroups.search` (the legacy fallback).

The exact op identifier is confirmed at implementation time against the current spec (`internal/generated/ops_index.json`); the lookup function picks the first available in priority order. If no matching op exists in the compiled index (catastrophic codegen drift), the wizard falls back to the freeform OG-ID prompt with a stderr warning.

### 4.6 Error handling

| Failure | Behavior |
|---|---|
| OAuth validation 401/403 | `✗ Auth failed: <code> <message>`. Re-prompt **client_id + client_secret** (other fields keep their entered values). 3 attempts; on the third failure, exit with `AUTH_INSUFFICIENT_FOR_OP` envelope. |
| Token URL unreachable (region wrong) | `✗ Couldn't reach <url> (network error)`. Re-prompt **region**. Same 3-attempt rule. |
| OG-list call returns 403 (insufficient scope) | Soft fallback: print `note: your OAuth client lacks org-group read; enter the OG ID manually.`, then a freeform `OG ID:` prompt with a hint pointing at WS1 console > Groups & Settings > Groups & Settings. |
| OG-list call times out / 5xx | Same fallback as 403. |
| Smoke test fails after auth + OG succeed | Setup is **considered successful** (creds are valid, OG is set). Print `note: smoke test failed (<err>); you may still need to verify your tenant role`. Don't roll back. |
| Ctrl-C mid-flow | Trap SIGINT. If creds aren't validated yet, write nothing to disk. If creds are validated but OG isn't picked, write the profile + secret (so the auth round-trip isn't wasted) and skip the OG step. Always print `aborted; partial state on disk: <profile-name>` so the user knows what to expect on rerun. |

### 4.7 Non-interactive mode

Required flags: `--tenant`, `--region` (or `--auth-url`), `--client-id`, `--client-secret`, `--og`.

Optional flags: `--profile` (default `operator`), `--skip-validate`, `--skip-smoke-test`.

Behavior when stdout is not a TTY:

- **Refuses if any required flag is missing** with envelope error `code: IDENTIFIER_AMBIGUOUS`, `details.missing: [flag-names]`. No silent prompting on a pipe.
- **Refuses to set the active profile** even with all flags. Adds the profile but leaves the active profile unchanged. Prints a stderr hint: `profile added but not activated; run \`ws1 profile use <name>\` from a terminal`. This honours CLAUDE.md decision #5 (only interactive callers can switch the active profile).

**v0.1 decision: strict — non-interactive setup never sets the active profile.** Fleet provisioning that wants a pre-activated profile in CI must follow up with a one-time interactive `ws1 profile use <name>`. Tracked for v0.5+ if friction surfaces (likely shape: explicit `--accept-active-profile-change` opt-in flag).

### 4.8 `ws1 status` (companion command)

Independent of `setup`; emits one envelope summarising current configuration:

```json
{
  "envelope_version": 1,
  "ok": true,
  "operation": "ws1.status",
  "data": {
    "active_profile": "operator",
    "configured_profiles": ["operator", "ro"],
    "tenant": "cn1506.awmdm.com",
    "region_inferred": "na",
    "og": {"id": 4067, "name": "EMEA-Pilot"},
    "audit_seq": 17,
    "last_op_at": "2026-04-30T14:38:58Z"
  },
  "meta": {"duration_ms": 4}
}
```

Replaces the three-call sequence (`profile current` + `og current` + `audit tail --last 1`) for agent introspection. Does not hit the API — purely on-disk read. The `region_inferred` field is best-effort: derived by reverse-mapping `auth_url` against the regions table; emits `"unknown"` when no mapping matches (e.g., custom `--auth-url` profiles).

### 4.9 Testing

**Unit tests** (`cmd/ws1/setup_test.go`):

- Stub `Prompter` drives the wizard through table-driven scenarios:
  - happy path (operator only, OG list succeeds, smoke test passes)
  - region typo + retry
  - OAuth 401 + retry → success on second attempt
  - OAuth 401 × 3 → exit with envelope error
  - OG-list 403 → freeform fallback
  - smoke test fails → setup considered successful, warning printed
  - Ctrl-C before validation → no disk side effects
  - Ctrl-C after validation, before OG → partial state on disk, OG skipped
- For each scenario, assert: profiles.yaml entries, og file contents, keychain entries (via `WS1_ALLOW_DISK_SECRETS` test override + `WS1_CONFIG_DIR`), audit log entries.

**Integration test** (`test/integration/setup_test.go`):

- Drives the real `ws1 setup` binary against the mock tenant via stdin pipe.
- Mock returns canned OG list (`Global`, `EMEA`, `EMEA-Pilot`) so the picker can be tested end-to-end. The mock needs a new route for the OG-list op; that route is added to `test/mockws1/server.go` as part of this work.
- Asserts: profiles.yaml has the expected entries, og file has the picked id, audit log has a `ws1.setup` entry recording the configuration timestamp + chosen profile + OG.

**`ws1 status` test** (`cmd/ws1/status_test.go`):

- Snapshot tests over fixture configs (no profile / one profile / two profiles / different OGs / missing audit log).
- Confirms envelope shape stays stable across configs.

**Existing tests** (`profile add` / `og use` / `profile use` / approval / audit / api-client / mock / integration) must stay green.

## 5. Out of scope (parked)

- **Fleet bootstrap with active-profile change in non-interactive mode.** See §4.7 open question. Tracked for v0.5+ if friction surfaces.
- **Tenant URL clipboard auto-detect.** Mentioned during brainstorm but not chosen — too magic, low signal-to-noise.
- **OG hierarchy display in the picker.** v0 lists OGs flat. If tenants have hundreds of OGs the flat list becomes unwieldy; tracked for v0.5+ (filtering, parent-grouping).
- **`ws1 setup --import-from-file`** for declarative provisioning. v0.5+.
- **TUI library upgrade** (huh / bubbletea). v0 hand-rolls primitives to avoid dependency weight; if the wizard grows complex, revisit.

## 6. Implementation handoff

The implementation plan for this design is generated via the `superpowers:writing-plans` skill in a follow-up turn. That plan will sequence the work into reviewable commits and identify any sub-decisions the design didn't pin down.
