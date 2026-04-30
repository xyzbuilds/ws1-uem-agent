# 04 — Safety: profiles, approval, async, freshness

## Profiles (capability tiers)

| Profile | Can do | Default? |
|---|---|---|
| `ro` | Read-only ops (search, get) | yes |
| `operator` | Read + write + destructive (with approval) | no |
| `admin` | Same as operator with elevated tenant scope | no |

The active profile is at `ws1 profile current`. To switch: the **user** runs `ws1 profile use operator` themselves. The CLI refuses to switch profile via argv when called non-interactively (i.e. by an agent). If you hit `AUTH_INSUFFICIENT_FOR_OP`, surface the envelope and ask the user to switch.

You can preflight: `ws1 ops describe mdmv4.devices.lock | jq '.data.class'`. If the class is `write` or `destructive` and you're on `ro`, plan for the user to switch before you execute.

## Approval flow (destructive ops)

Every destructive op (and write-class above blast threshold) goes through this:

1. CLI snapshots target state (owner, OG, enrollment status).
2. CLI binds an HTTP server to `127.0.0.1:<random-port>`.
3. CLI opens the user's browser at the approval URL; also prints the URL to stderr.
4. User clicks Approve or Deny.
5. CLI re-fetches each target; compares to the snapshot. Drift → `STALE_RESOURCE`; approval is **not** consumed (user can re-approve after re-evaluating).
6. CLI executes the API call.
7. CLI appends an audit-log entry, prints the envelope, exits.

Default timeout: **5 minutes**. If the user doesn't click in time, you get `APPROVAL_TIMEOUT` and exit code 2. Don't auto-retry — surface to the user.

When you anticipate this flow, say so up front to the user: "I'm about to issue an enterprise-wipe on Alice's iPhone (DeviceID 12345). The CLI will open your browser; please review the approval page and click Approve or Deny."

## Freshness check (drift between approval and execute)

Between the user's click and the API call, the device may have changed: the user might have unenrolled, the device might have moved OGs, etc. The CLI re-fetches before executing. If the snapshot fields have drifted, the op aborts with `STALE_RESOURCE` and the approval is not consumed.

When you see `STALE_RESOURCE`:
1. Re-fetch the target via `ws1 mdmv4 devices get`.
2. Show the user what changed (`details.drift`).
3. Ask whether to proceed; if yes, the user re-runs and re-approves.

Do **not** auto-retry. Drift means the world changed, and the user's intent may no longer apply.

## Dispatched ≠ executed: the UEM async-nature contract

This is the most-violated mental model in agent-driven UEM operations. Internalize it.

WS1 does not run commands directly on devices. Every state-changing command flows through a **device-side queue**:

1. Your `ws1` call lands at the WS1 API.
2. The API validates the command and **enqueues** it for the target device.
3. The API returns. Status is typically `Queued` / `Pending` / `Dispatched` — never `Executed`.
4. **Sometime later**, the device checks in (over MDM heartbeat, push notification, or scheduled poll). It picks up the queued command.
5. The device runs the command. This may take seconds (a `Lock`) or hours (a `DeviceWipe`, an app install on a slow connection).
6. The device reports completion back to WS1 on its *next* check-in after execution.

**What this means for an agent:**

- A successful API response means **the command is in the queue**. It does not mean the device has done anything yet.
- The right success signal is **"the API accepted my dispatch without a validation error"** — typically HTTP 200/202 with `status: Queued` and a `command_uuid` or `job_id`.
- **Do not poll** to "confirm completion." The device's check-in cadence is unrelated to your CLI invocation. A `Lock` on a phone in someone's pocket might land in 2 seconds; an `InstallApplication` on a docked laptop overnight could be 8 hours. Polling the API every few seconds tells you nothing useful and burns rate limit.
- If a follow-up read is genuinely needed (e.g., "did the wipe actually run?"), do it minutes-to-hours later via a fresh `devices get` — and only when the user actually needs to know.
- For long-running operations the user cares about, hand back the `command_uuid`/`job_id` and tell them how to check later: `ws1 mdmv1 commandsv1 search --command_uuid <uuid>` or similar. Don't tie up the CLI in a polling loop.

The CLI does **not** ship a `--watch` flag for this reason. If you find yourself wanting one, you're modeling UEM as if it were a synchronous API.

## Async by name

Some ops have `Async` in their `operationId` (e.g. `Devices_LockAsync`). That's a .NET implementation detail — every WS1 command is async in the queue-then-execute sense above. The CLI strips `Async` from the verb when generating op identifiers; treat all device-targeting ops as async-by-nature even when the name doesn't shout it.

## Bulk + partial-success

When a bulk op returns `ok: true && failure_count > 0`:
1. Don't celebrate. Some targets failed.
2. Inspect each entry in `data.failures`. Each has its own `error.code`.
3. Group failures by code. `STALE_RESOURCE` may warrant a freshness re-check; `RATE_LIMITED` warrants backoff + retry; `IDENTIFIER_NOT_FOUND` is a data quality issue, surface to user.
4. Propose a remediation per group rather than a blanket retry.

## When something goes wrong

**Don't paper over errors.** The error taxonomy is finite and meaningful. If you hit `INTERNAL_ERROR`, the CLI is buggy — capture the envelope and surface it. If you hit `STALE_RESOURCE` repeatedly, something is racing; surface the pattern, don't loop.

The audit log (`ws1 audit tail`) is an ally. After a sequence of write/destructive ops, glancing at the audit log reassures the user that what you said happened, happened.
