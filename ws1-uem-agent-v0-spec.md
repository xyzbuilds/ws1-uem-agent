# WS1 UEM Agent — v0 Design Spec

**Status:** Draft for redline
**Last updated:** 2026-04-30
**Predecessor:** `ws1-uem-agent.md` (idea doc)

This doc locks the architectural decisions reached over the design conversation and specifies the contracts (CLI shape, envelope schema, approval flow, policy file format, skill layout) needed before scaffolding starts. Open questions parked for v0.5+ are listed at the end.

---

## 0. Operating principles (added v0.1)

Four principles governing CLI behavior + skill content. They surface throughout this spec where each becomes concrete.

1. **UUID/GUID over integer IDs.** WS1 is migrating identifier conventions from integer (`DeviceID`, `UserID`) to UUID (`Uuid`, `deviceUuid`, `applicationUuid`). The CLI surfaces both — every section ships UUID-flavored ops alongside legacy integer-flavored ops — and the skill teaches agents to prefer UUID when responses offer both. See concepts/02-api-surface.md § Identifiers.

2. **Bulk over loop.** When N>1 targets are required, find a bulk endpoint (`<section>.commandsv*.bulkexecute`, `bulk*`, `batch*`) instead of looping per-target. Loops burn rate-limit and produce non-atomic intent. For state-changing bulks above the per-op `blast_radius_threshold` (default 50), the approval flow gates the call.

3. **Honor rate limits, do not poll.** On HTTP 429 the API client retries once, honoring `Retry-After` (capped at 30 s); persistent 429 surfaces as `RATE_LIMITED` to the caller. Agents do **not** auto-poll job status to confirm completion — that's the wrong mental model for UEM (see principle 4).

4. **Dispatched ≠ executed.** WS1 enqueues commands for devices to pick up on next check-in. API success means the command is in the device queue, not that the device has run it. Polling the API in a loop trying to confirm completion tells you nothing useful and burns rate limit. If verification of effect is needed, do it minutes-to-hours later via a fresh read, only when the user cares.

These four are reinforced in the skill's principle stack (skills/ws1-uem/SKILL.md), the `concepts/04-safety.md` "Dispatched ≠ executed" section, and the runtime behavior in `internal/api/client.go` (rate-limit retry) + `cmd/ws1/sections.go` (no auto-watch on async ops).

---

## 1. Executive summary

We're building a CLI + Skill pair that lets any skill-capable agent (Claude Code, Claude Desktop in Cowork mode, claude.ai, Cursor) operate Omnissa Workspace ONE UEM safely from natural-language goals.

**Locked decisions:**

- **Primary surface:** `ws1` CLI (single static Go binary) + companion `ws1-uem` skill. The CLI is the substrate; the skill makes Claude-family agents fluent with it.
- **Target deployment:** skill-capable agents. The CLI is self-describing (`ws1 ops list`, `ws1 ops describe`, structured envelopes) so non-skill agents can still operate it; just less efficiently.
- **Single operator persona** for v1. Not a fleet, not multi-actor.
- **Auth tiers gate capability:** read-only default, with operator/admin escalation that must be user-initiated, never agent-initiated.
- **Destructive ops always require explicit user approval.** Even with admin auth.
- **Approval surface:** browser-based, served by the CLI on `127.0.0.1:<random>` for the duration of a single CLI invocation. The agent makes one blocking call; the user clicks Approve in their browser. The agent never handles approval tokens.
- **CLI enforces, skill teaches.** Defense in depth — both layers know the rules; the CLI is the last word.
- **Fail-closed on unknown operations.** Any op not in `operations.policy.yaml` is treated as destructive + approval-required, with a warning.
- **JSON envelope is versioned** with a finite error taxonomy. Partial-success modeled explicitly.
- **Build from scratch** (not fork `ws1cli`). Reasons are agent-shaped (envelope, classification, approval, audit, schema dump), not licensing.

**Threat model — what v1 defends against:** agent mistakes, agent hallucinated commands, accidental scale (running a destructive op against the wrong target or too many targets), spec drift introducing un-classified ops.

**Threat model — what v1 does NOT defend against:** a compromised agent process on the same machine, audit-log tampering, multi-agent or multi-user concurrency. These are documented limitations with v2 mitigations sketched.

---

## 2. System architecture

```
┌─────────────────────────────────────────┐
│   Skill-capable agent runtime           │
│   (Claude Code / Cowork / Cursor)       │
│                                         │
│   Loaded on demand:                     │
│   ├── Skill: ws1-uem  (primer)          │
│   └── Bash tool                         │
└──────────────┬──────────────────────────┘
               │  spawns subprocess
               ▼
┌─────────────────────────────────────────┐
│   ws1 CLI process (single invocation)   │
│                                         │
│   ├── Loads operations.policy.yaml      │
│   ├── Generates op-specific args parser │
│   │   from spec.json (compile-time)     │
│   ├── Auth profile (RO/operator/admin)  │
│   └── If approval needed:               │
│       └── Ephemeral HTTP server on      │
│           127.0.0.1:<random>            │
│             ↑                           │
│             │  user clicks Approve      │
│             │                           │
│       ┌─────┴──────┐                    │
│       │ user's     │                    │
│       │ browser    │                    │
│       └────────────┘                    │
└──────────────┬──────────────────────────┘
               │  WS1 REST API
               ▼
        [ Omnissa WS1 UEM tenant ]
```

The CLI is a one-shot process per invocation. The browser server lives only inside that process. There is no long-running daemon in v1.

---

## 3. Trust model

Three principals; capability flows downward; trust does not flow upward.

| Principal | Holds | Trusted for |
|---|---|---|
| User (human) | Browser session, OS keychain, terminal | Approving destructive ops; switching auth profile; setting OG context |
| CLI (per-invocation process) | OAuth client creds (loaded from keychain), in-memory approval state | Generating approval requests; issuing/verifying approval; making API calls |
| Agent (any skill-capable runtime) | Bash tool only | Calling the CLI with arguments; reading stdout/stderr |

**The agent is not a participant in the approval transaction.** It can call the CLI; it cannot fabricate approval, intercept the browser callback, or read the in-process state. If the agent surfaces a request URL to the user as a UX courtesy, that's fine — the URL is bound to a request_id the agent didn't generate and the click is verified server-side by the CLI process.

**Where each secret lives (v1):**

| Secret | Storage | Why |
|---|---|---|
| OAuth client_id / client_secret | OS keychain (macOS Keychain, Windows DPAPI, Linux secret-service via libsecret) | Same-user processes can't read other apps' keychain entries without explicit grant |
| Active profile selection | `~/.config/ws1/profile` (plaintext, name only) | Not sensitive; no creds in this file |
| In-flight approval state | CLI process memory only | Dies with the process; cannot be replayed by other processes |
| Audit log | `~/.config/ws1/audit.log` (JSONL) | v1 limitation: writable by the agent. Documented; v2 hardens via hash chain + remote sink |

**v1 limitations stated honestly:**

- A compromised agent running as the same OS user can read keychain entries the user has previously granted to `ws1`. Mitigations: keychain prompts on first use; users are warned to grant cautiously. v2 considers per-invocation credential injection from a separate process.
- A compromised agent can edit `~/.config/ws1/audit.log`. v2 ships hash-chained entries to a write-only remote sink.
- Concurrent CLI invocations against the same tenant are not coordinated. v2 considers a daemon for cross-invocation state.

---

## 4. CLI surface conventions

### 4.1 Self-describing commands

These five always work, with or without a skill loaded:

| Command | Purpose |
|---|---|
| `ws1 --version --json` | Version, build, supported spec version |
| `ws1 --help [--json]` | Top-level help, machine-readable available |
| `ws1 ops list [--json]` | All operations with class, identifier, sync/async, bulk-capable |
| `ws1 ops describe <op> [--json]` | Single op: full schema, examples, prerequisites, pitfalls |
| `ws1 doctor` | Validates auth, tenant connectivity, config; structured pass/fail report |

A non-skill agent using only `ws1 --help --json` and `ws1 ops describe X` should be able to plan competently. The skill just makes Claude-family agents faster.

### 4.2 Profile model

Three profiles, switched explicitly by the user. The CLI refuses to switch profile based on its own argv (no `ws1 profile use operator` baked into a command); switching is a separate user-initiated invocation.

| Profile | Capability | OAuth credentials |
|---|---|---|
| `ro` | Read ops only; writes/destructive return `AUTH_INSUFFICIENT_FOR_OP` | Read-only client |
| `operator` | Read + write; destructive still gated by approval flow | Operator client |
| `admin` | Everything; destructive still gated by approval flow | Admin client |

Switch via:

```
$ ws1 profile use operator
$ ws1 profile current   # prints active profile
$ ws1 profile list      # prints configured profiles
```

The agent **can read** the active profile (via `ws1 profile current`), so it knows what's reachable. It **cannot change** it; `ws1 profile use ...` is documented as user-only. (Enforcement: the command refuses if its parent process is non-interactive; v2 hardens via a separate signer process.)

### 4.3 Tenant / OG context

Strict by default. Every command requires `--og <id>` or a default set explicitly via:

```
$ ws1 og use 12345
$ ws1 og current
```

Missing OG context returns `TENANT_REQUIRED`. The skill teaches: "always set OG context at the start of a session."

---

## 5. JSON envelope schema

Every command's stdout is a single JSON object matching this shape.

### 5.1 Skeleton

```json
{
  "envelope_version": 1,
  "ok": true,
  "operation": "<dotted.operation.name>",
  "data": { ... },
  "meta": {
    "duration_ms": <int>,
    "spec_version": "<string>",
    "cli_version": "<string>"
  }
}
```

`envelope_version` is bumped on any breaking shape change. Agents check this on every parse.

### 5.2 Read success

```json
{
  "envelope_version": 1,
  "ok": true,
  "operation": "mdm.devices.search",
  "data": [
    {"DeviceID": 12345, "SerialNumber": "ABC123", "FriendlyName": "Alice's iPhone 15", "EnrollmentStatus": "Enrolled"},
    {"DeviceID": 12346, "SerialNumber": "DEF456", "FriendlyName": "Alice's MacBook Pro", "EnrollmentStatus": "Enrolled"}
  ],
  "meta": {
    "duration_ms": 312,
    "count": 2,
    "page": 1,
    "page_size": 100,
    "has_more": false
  }
}
```

### 5.3 Write success (single target)

```json
{
  "envelope_version": 1,
  "ok": true,
  "operation": "mdm.devices.lock",
  "data": {
    "DeviceID": 12345,
    "command_uuid": "cmd_a1b2c3",
    "status": "Queued"
  },
  "meta": {
    "duration_ms": 412,
    "approval_request_id": "req_a1b2c3",
    "audit_log_entry": "2026-04-30T14:00:00Z#117"
  }
}
```

### 5.4 Partial success (bulk)

`ok: true` because the call ran; per-target outcomes are inside `data`. The agent's branching rule:

- `ok == true && meta.failure_count == 0` → all good
- `ok == true && meta.failure_count > 0` → partial; inspect `data.failures`, decide retry/escalate
- `ok == false` → the call itself failed; no work done or unknown state

```json
{
  "envelope_version": 1,
  "ok": true,
  "operation": "mdm.devices.commands.bulk",
  "data": {
    "successes": [
      {"DeviceID": 12345, "command_uuid": "cmd_a1"},
      {"DeviceID": 12346, "command_uuid": "cmd_a2"}
    ],
    "failures": [
      {"DeviceID": 12347, "error": {"code": "STALE_RESOURCE", "message": "Device unenrolled since lookup."}}
    ]
  },
  "meta": {
    "duration_ms": 2104,
    "target_count": 3,
    "success_count": 2,
    "failure_count": 1,
    "approval_request_id": "req_b2c3d4"
  }
}
```

### 5.5 Async job

```json
{
  "envelope_version": 1,
  "ok": true,
  "operation": "mcm.profiles.publish",
  "data": {
    "job_id": "job_x1y2z3",
    "status": "Pending",
    "poll_url": "ws1://jobs/job_x1y2z3"
  },
  "meta": {
    "duration_ms": 218,
    "async": true,
    "approval_request_id": "req_c3d4e5"
  }
}
```

Polling: `ws1 jobs get --id job_x1y2z3`. With `--watch`, the CLI polls until terminal status, prints progress to stderr, returns final envelope on stdout, exits with code reflecting final state.

### 5.6 Error

```json
{
  "envelope_version": 1,
  "ok": false,
  "operation": "mdm.devices.wipe",
  "error": {
    "code": "AUTH_INSUFFICIENT_FOR_OP",
    "message": "Current profile 'ro' cannot perform destructive ops. Switch to 'operator' or 'admin'.",
    "details": {
      "active_profile": "ro",
      "required_profile_minimum": "operator",
      "operation_class": "destructive"
    }
  },
  "meta": {
    "duration_ms": 12,
    "cli_version": "0.1.0"
  }
}
```

### 5.7 Exit codes

| Code | Meaning |
|---|---|
| 0 | `ok: true`, no failures |
| 1 | `ok: true` with `meta.failure_count > 0` (partial) |
| 2 | `ok: false`, recoverable (e.g. RATE_LIMITED, APPROVAL_TIMEOUT) |
| 3 | `ok: false`, configuration / auth error (AUTH_INSUFFICIENT_FOR_OP, TENANT_REQUIRED) |
| 4 | `ok: false`, validation error (IDENTIFIER_AMBIGUOUS, IDENTIFIER_NOT_FOUND, UNKNOWN_OPERATION) |
| 5 | `ok: false`, internal error |

---

## 6. Error taxonomy

Every `error.code` value, with semantics and recommended agent action. Skill teaches the agent what each one means.

| Code | When | Recommended agent action |
|---|---|---|
| `AUTH_INSUFFICIENT_FOR_OP` | Active profile can't perform op's class | Surface to user; ask whether to switch profile |
| `APPROVAL_REQUIRED` | Destructive or scale-gated op with no in-process approval | Should not normally surface; CLI handles approval inline. If returned, agent re-runs with `--approve-via=browser` (default) |
| `APPROVAL_TIMEOUT` | User did not approve within window | Surface to user; ask whether to retry |
| `APPROVAL_DENIED` | User clicked Deny in browser | Stop; do not retry |
| `IDENTIFIER_AMBIGUOUS` | Lookup returned multiple candidates | Surface candidates to user; ask which one |
| `IDENTIFIER_NOT_FOUND` | Lookup returned zero | Surface to user; consider the search may be wrong |
| `TENANT_REQUIRED` | No OG context set | Surface to user; ask which OG |
| `RATE_LIMITED` | API rate limit hit | Backoff per `details.retry_after_seconds`, then retry |
| `ASYNC_JOB_PENDING` | Job started, not yet complete (only when re-checking status) | Wait and poll, or return `meta.job_id` to user |
| `STALE_RESOURCE` | Approved target's state changed before execute | Re-fetch target; re-confirm with user; re-approve |
| `UNKNOWN_OPERATION` | Op exists in spec.json but not in policy.yaml | Treated as destructive (fail-closed). Surface warning to user; recommend op be classified |
| `SPEC_VERSION_MISMATCH` | CLI's compiled spec is older than tenant's | Surface to user; recommend `ws1 update` |
| `NETWORK_ERROR` | Could not reach API | Surface to user; check connectivity |
| `INTERNAL_ERROR` | Bug in CLI | Surface to user; recommend `ws1 send-feedback` |

---

## 7. Approval flow

Concrete sequence for a destructive op (`mdm.devices.wipe`).

### 7.1 Sequence

```
1. Agent:   ws1 mdm devices wipe --id 12345

2. CLI:     - Loads operations.policy.yaml
            - Looks up mdm.devices.wipe → {class: destructive, approval: always_required}
            - Looks up target: GET /devices/12345
              → {SerialNumber: "ABC123", FriendlyName: "Alice's iPhone 15",
                 EnrollmentUser: "alice@example.com", OG: "EMEA-Pilot"}
            - Computes args_hash = sha256(normalize(args))
            - Generates request_id = "req_" + 16 random bytes hex
            - Generates random port P in [49152, 65535]
            - Starts HTTP server bound to 127.0.0.1:P
            - Tries: open / xdg-open / start http://127.0.0.1:P/r/req_xxx
            - Prints to stderr:
              "Approval required. Opening browser. If it doesn't open:
               http://127.0.0.1:54321/r/req_a1b2c3"
            - Blocks on server channel; 5-minute timeout

3. Browser: GET /r/req_a1b2c3
            CLI server renders approval page:
              ┌─────────────────────────────────────────────┐
              │  Wipe device                                │
              │                                             │
              │  Target:        Alice's iPhone 15           │
              │  Serial:        ABC123                      │
              │  Owner:         alice@example.com           │
              │  Org Group:     EMEA-Pilot                  │
              │  Reversibility: Irreversible                │
              │  Profile:       operator                    │
              │                                             │
              │  Initiated by:  agent process (PID 4421)    │
              │  Request ID:    req_a1b2c3                  │
              │  Expires:       4m 32s                      │
              │                                             │
              │  [ Approve ]   [ Deny ]                     │
              └─────────────────────────────────────────────┘

4. User:    Clicks [ Approve ]
            Browser POSTs to /r/req_a1b2c3/approve

5. CLI:     - Verifies request_id matches
            - Re-fetches target: GET /devices/12345
            - Compares against snapshot taken in step 2
              - If owner/OG/enrollment_status changed → STALE_RESOURCE error,
                approval not consumed
            - Marks request_id as used (in-process)
            - Issues API call: POST /devices/12345/commands {CommandXml: "EnterpriseWipe"}
            - Appends audit log entry
            - Shuts down HTTP server
            - Prints final envelope on stdout
            - Exits with code 0
```

### 7.2 What's bound

The in-process approval record holds:

```go
type ApprovalRequest struct {
    RequestID      string
    Operation      string
    ArgsHash       []byte    // sha256 of normalized args
    TargetID       string
    TargetSnapshot map[string]any  // owner, OG, enrollment status
    CreatedAt      time.Time
    ExpiresAt      time.Time
    Used           bool
}
```

The execute-time freshness check compares the current target state against `TargetSnapshot`. Drift triggers `STALE_RESOURCE` and approval is **not** consumed (user can re-approve after re-evaluating).

### 7.3 Edge cases

| Case | Behavior |
|---|---|
| User closes browser without clicking | 5-min timeout → `APPROVAL_TIMEOUT` error |
| User clicks Deny | Server records denial; CLI exits with `APPROVAL_DENIED` |
| Browser doesn't open (no display) | URL printed to stderr; user can copy-paste from another machine if 127.0.0.1 is reachable. If not, `--no-prompt` mode returns the request envelope and recommends side-channel approval (deferred to v2) |
| Multiple approval requests in same run | Each generates a distinct request_id, distinct port, distinct URL |
| Port conflict | CLI retries with another random port |
| User reloads approval page after click | Server returns "already approved" or "already denied" |

### 7.4 Headless mode

**Deferred to v2.** v1 requires a browser on the machine running the CLI. Documented constraint. Failures in headless contexts return:

```json
{
  "envelope_version": 1,
  "ok": false,
  "operation": "mdm.devices.wipe",
  "error": {
    "code": "APPROVAL_REQUIRED",
    "message": "Headless approval not supported in v1. Run ws1 from a machine with a browser.",
    "details": {"v2_alternative": "ws1 approve <request_id> via daemon"}
  }
}
```

---

## 8. Operations policy file

Source-of-truth file for op classification. Hand-curated, checked into the repo, shipped inside the CLI binary at build time.

### 8.1 Format

`operations.policy.yaml`:

```yaml
version: 1

# Default applied to any op in spec.json not listed below.
# Fail-closed: unknown = destructive + approval-required.
__default__:
  class: destructive
  reversible: unknown
  approval: always_required
  warn: "Operation not classified in policy.yaml; treated as destructive."

# Read ops
mdm.devices.search:
  class: read
  identifier: none
  warn_if_results_over: 1000

mdm.devices.get:
  class: read
  identifier: device_id

# Write ops
mdm.devices.lock:
  class: write
  reversible: full
  identifier: device_id
  blast_radius_threshold: 50  # approval needed if targeting more than this
  sync: true

mdm.devices.commands.bulk:
  class: write
  reversible: depends_on_command
  identifier: device_id_array
  blast_radius_threshold: 50
  approval: required_if_count_over_threshold

# Destructive ops
mdm.devices.wipe:
  class: destructive
  reversible: none
  approval: always_required
  identifier: device_id
  sync: false  # async job

mdm.devices.unenroll:
  class: destructive
  reversible: none
  approval: always_required
  identifier: device_id
  sync: true

mdm.devices.enterprise_wipe:
  class: destructive
  reversible: partial
  approval: always_required
  identifier: device_id
  sync: false
```

### 8.2 Build-time check

A CI job runs on every spec.json update:

```
$ ws1-build classify-check
Comparing spec.json (v2506) to operations.policy.yaml ...
Unclassified operations:
  - mdm.devices.refresh_compliance  (NEW in v2506)
  - system.org_groups.delete         (NEW in v2506)

Action: classify these in operations.policy.yaml before merging.
Until classified, they will be treated as destructive at runtime.
```

### 8.3 Runtime warning

When a user invokes an unclassified op, the envelope includes a warning:

```json
{
  "envelope_version": 1,
  "ok": false,
  "operation": "mdm.devices.refresh_compliance",
  "error": {
    "code": "APPROVAL_REQUIRED",
    "message": "Approval required.",
    "details": {
      "warn": "This operation is not classified in policy.yaml. Treated as destructive (fail-closed). File a PR to classify it.",
      "approval_url": "http://127.0.0.1:54321/r/req_xxx"
    }
  }
}
```

---

## 9. Audit log

`~/.config/ws1/audit.log`, JSONL, append-only by convention.

```json
{"ts":"2026-04-30T14:00:00Z","seq":117,"caller":"agent:claude-code:session-X","operation":"mdm.devices.lock","args_hash":"sha256:a1b2...","class":"write","approval_request_id":null,"profile":"operator","tenant":"12345","result":"ok","duration_ms":412,"prev_hash":"sha256:f0e1..."}
```

`prev_hash` chains entries. Tampering with any prior entry breaks the chain at every subsequent entry. v1 detects tampering after the fact; v2 ships entries to a remote write-only sink for prevention.

`ws1 audit tail [--last N]` and `ws1 audit verify` are user-invokable.

---

## 10. Skill layout (trimmed)

```
skills/ws1-uem/
├── SKILL.md                      # entrypoint; ~3-4k tokens; always loaded
├── concepts/
│   ├── 01-domain-model.md        # OG architecture + canonical objects + identifiers
│   ├── 02-api-surface.md         # MDM/MAM/MCM/System mental map
│   ├── 03-targeting.md           # single vs bulk vs smart-group + decision rules
│   ├── 04-safety.md              # async/sync, destructive flow, approval, pitfalls
│   └── 05-practices.md           # WS1 operational wisdom (pilot rings, etc.)
├── patterns/
│   ├── lookup-then-act.md
│   ├── target-by-smart-group.md
│   ├── compliance-driven-action.md
│   └── safe-destructive.md
└── reference/
    └── operation-index.md        # auto-generated from spec.json + policy.yaml
```

### 10.1 SKILL.md (always loaded; budget 3-4k tokens)

Contents:

- **Identity:** "you are a WS1 UEM operator; your tools are the `ws1` CLI and Bash"
- **Principle stack** (5–7 ordered bullets):
  1. Always set OG context first (`ws1 og use ...`)
  2. Lookup before act — never guess identifiers
  3. Single → bulk → smart-group as targeting scales (thresholds in `03-targeting.md`)
  4. Read-only profile is default; escalate per task with user consent
  5. Destructive ops require user approval via browser; don't try to bypass
  6. Always check `meta.failure_count` on bulk results
  7. Async ops return `job_id`; poll or `--watch`
- **Decision tree:** "I have a goal → which concept file should I read next?"
- **Kill-list:** ops where the agent must pause and surface to the user even before calling the CLI (any `class: destructive`)
- **Pointer to reference/operation-index.md** for op lookup

### 10.2 What changed from the original 8-file layout

Collapsed:

- `00-architecture.md` + `01-objects.md` + `02-identifiers.md` → `01-domain-model.md`
- `04-single-vs-bulk.md` → `03-targeting.md` (also covers smart-group sizing)
- `05-async-jobs.md` + `06-pitfalls.md` → `04-safety.md` (with destructive flow + approval)
- `07-best-practices.md` → `05-practices.md`

Same content, fewer navigation hops, denser primer per file.

---

## 11. v0 demo target

Concrete scenario the v0 build must execute end-to-end:

> "Find all devices for `alice@example.com`, show me a summary, then on my approval, lock them all."

This exercises:

| Stage | Subsystem |
|---|---|
| Auth + OG context | Profile model, tenant discipline |
| User lookup | Identifier discipline, `IDENTIFIER_NOT_FOUND` / `IDENTIFIER_AMBIGUOUS` paths |
| Device list for user | Read-class op, envelope schema, pagination |
| Summary to user | Agent rendering of structured `data` |
| Lock decision | Single vs bulk threshold (likely under 50 → loop or bulk) |
| Approval prompt | Browser approval flow end-to-end |
| Bulk lock execute | Bulk op, partial-success envelope shape |
| Audit | Log entries written, hash-chained |

A `--dry-run` variant exercises the dry-run path without making any state changes.

---

## 12. Next concrete steps

The build sequence is documented in detail in `claude-code-kickoff.md` (12 sessions). Summary:

1. **Scaffold v0 CLI in Go** — project layout, `go.mod`, Cobra skeleton, envelope package with full tests, `ws1 doctor` stub, CI.
2. **Build the maintainer pipeline** (`ws1-build discover` + `fetch`) per `docs/build-pipeline.md`. First spec pull commits `spec/*.json` and `spec/VERSION`.
3. **Code-gen + classification gate** (`ws1-build codegen-cli` + `codegen-skill` + `classify-check`). Wire `make sync-specs`. Bootstrap `operations.policy.yaml` covering all initial ops.
4. **OAuth + keychain + profile model** (`ro` / `operator` / `admin`).
5. **First three read commands** against the real tenant.
6. **Policy loader runtime enforcement** (the policy file exists from step 3; this wires the runtime check).
7. **Browser approval server** end-to-end (safety-critical; high coverage required).
8. **First write command** (`ws1 mdmv4 devices lock`) exercising the approval flow.
9. **Audit log** with hash chain + `ws1 audit verify`.
10. **Bulk + partial-success envelope** + stale-resource freshness check.
11. **Skill drafting**: `SKILL.md` + the five concept files (auto-generated `reference/operation-index.md` already exists from step 3).
12. **End-to-end alice-lock demo** + iterate on what hurts.

---

## 13. Open questions parked for v0.5+

| # | Question | Default if not answered |
|---|---|---|
| 1 | OAuth client secret in keychain — what's the UX when keychain isn't available (Linux without secret-service)? | Fall back to encrypted config file with passphrase |
| 2 | Idempotency keys on write/destructive ops — generate per-call or per-approval? | Per-approval (agent retry of the same approval doesn't double-execute) |
| 3 | Async jobs across CLI invocations — does v1 track them, or is each invocation independent? | Independent in v1; `~/.config/ws1/jobs.jsonl` recommended in v0.5 |
| 4 | Distribution: Homebrew, scoop, native installer, or just GitHub release tarballs for v0? | GitHub release tarballs for v0; brew tap for v0.5 |
| 5 | Telemetry / `ws1 send-feedback` — opt-in shape? What's collected? | Last envelope + sanitized config; opt-in, never automatic |
| 6 | Batched approval for sequences of ops (single click approves a planned sequence)? | Not in v1; agent must use bulk endpoints to compress consent |
| 7 | Single-user only in v1, or multi-user-on-shared-tenant from day one? | Single-user; multi-user is v2 with shared daemon |
| 8 | Ad-hoc smart group creation for one-off bulk targeting? | Prefer existing SGs; create only with user approval; auto-tag for cleanup |
| 9 | `SECURITY.md` / public threat model doc — when to write? | Before first public release; draft alongside v0 |

---

## 14. References

- Predecessor doc: `ws1-uem-agent.md`
- Existing CLI to learn from: <https://github.com/ancalabrese/ws1cli>
- Omnissa WS1 UEM API docs (spec.json source)
- FSF on subprocess use of GPL software: <https://www.gnu.org/licenses/gpl-faq.html#MereAggregation>
