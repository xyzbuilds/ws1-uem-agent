# Pattern: safe-destructive

The full belt-and-braces ritual for destructive ops. Use this whenever the op class is `destructive` (wipe, unenroll, delete) or whenever you're acting on more than 50 reversible targets.

## When

- Any op with `class: destructive` per `ws1 ops describe`.
- Bulk ops above the blast-radius threshold (default 50).
- Ops the user phrases nervously ("are you sure...", "first see if...").

## Steps

1. **Surface what's about to happen.** One sentence each:
   - *Why*: link the action to the user's stated goal.
   - *What*: target list (count + sample), op name, irreversibility note.
   - *How*: "the CLI will open your browser for approval; click Approve or Deny."

2. **`--dry-run` first** for sets >5. The CLI returns what it would have called without making any state-changing requests.

3. **Execute.** The CLI captures snapshots, opens the browser, waits for click.

4. **Handle the outcome explicitly.** Read `meta.approval_request_id` (if present, the flow ran), `meta.audit_log_entry`, and on bulk: `meta.failure_count`.

5. **Verify.** For destructive ops, follow up with a `devices.get` on at least one target to confirm the new state.

6. **Audit.** `ws1 audit tail --last 5` to confirm the chain captured the action.

## Worked example: "Wipe Bob's lost laptop"

```bash
# 0. Confirm the situation. (Conversation with user; not a CLI step.)

# 1. Lookup.
ws1 systemv2 users search --email bob@example.com
#  -> UserID=10003 (unique)
ws1 mdmv4 devices search --user bob@example.com --platform Apple
#  -> 1 device, DeviceID=12347, FriendlyName="Bob's MacBook"

# 2. Surface.
#    "I found one match — Bob's MacBook (DeviceID 12347, EnrollmentStatus
#     Enrolled, OG EMEA-Pilot). I'm proposing an enterprise-wipe. This is
#     irreversible. The CLI will open your browser for approval."

# 3. Dry-run preflight.
ws1 --dry-run mdmv4 devices wipe --id 12347

# 4. Execute.
ws1 mdmv4 devices wipe --id 12347
#  -> approval flow opens; user clicks Approve;
#  -> envelope returns with approval_request_id, audit_log_entry,
#     async job_id

# 5. Verify.
ws1 mdmv4 devices get --id 12347
#  -> EnrollmentStatus: EnterpriseWipePending

# 6. Audit.
ws1 audit tail --last 1
#  -> entry shows operation=mdmv4.devices.wipe, result=ok,
#     approval_request_id matching the envelope.
```

## Anti-patterns

- **Don't auto-retry on `APPROVAL_DENIED`.** The user said no.
- **Don't auto-retry on `STALE_RESOURCE`.** The state changed; the user's intent may have stale assumptions. Surface the drift, ask, then proceed.
- **Don't combine multiple destructive ops in one user turn.** Confirm each separately. The user clicking Approve on one wipe is not consent for a sequence.
