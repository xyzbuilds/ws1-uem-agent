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

## Async vs sync

`ws1 ops describe <op>` shows `sync: true | false`. Sync ops complete inline and return their result in `data`. Async ops queue a job and return `data.job_id`.

For async, two paths:
- **Watch**: `ws1 ... --watch` polls until the job reaches a terminal state and emits the final envelope.
- **Hand off**: emit the envelope with `job_id` to the user and let them poll later via `ws1 jobs get --id job_x1y2z3`.

Don't assume an async op succeeded because the call returned 200; the call only confirms the job was *queued*.

## Bulk + partial-success

When a bulk op returns `ok: true && failure_count > 0`:
1. Don't celebrate. Some targets failed.
2. Inspect each entry in `data.failures`. Each has its own `error.code`.
3. Group failures by code. `STALE_RESOURCE` may warrant a freshness re-check; `RATE_LIMITED` warrants backoff + retry; `IDENTIFIER_NOT_FOUND` is a data quality issue, surface to user.
4. Propose a remediation per group rather than a blanket retry.

## When something goes wrong

**Don't paper over errors.** The error taxonomy is finite and meaningful. If you hit `INTERNAL_ERROR`, the CLI is buggy — capture the envelope and surface it. If you hit `STALE_RESOURCE` repeatedly, something is racing; surface the pattern, don't loop.

The audit log (`ws1 audit tail`) is an ally. After a sequence of write/destructive ops, glancing at the audit log reassures the user that what you said happened, happened.
