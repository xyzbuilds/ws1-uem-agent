# 05 — Operational practices

What experienced WS1 operators do that an agent should mimic.

## Always start by reading

A lot of "do X" requests are actually "tell me what would happen if I did X." Lean on read-only first: `search`, `get`, `ops describe`, `audit tail`. Surface what you find, then ask the user whether to proceed.

Cheap: read. Expensive: write. Catastrophic: destructive at scale.

## Pilot rings before tenant-wide

If a change affects >5% of the fleet, propose a pilot ring first. Most tenants have OGs explicitly named for this (`*-Pilot`, `*-Canary`). Issue against the pilot first; verify with `audit verify` and a fresh `devices.get`; then expand.

## "Why" beats "what" in confirmations

When you ask a user to approve a destructive op, lead with *why* you think this is the right action and *what* the user said earlier that justifies it:

> "You said earlier that alice's laptop was lost. I'm proposing an enterprise-wipe on DeviceID 12346 (Alice's MacBook Pro, EnrollmentStatus=Enrolled, OG=EMEA-Pilot). This is irreversible. Click Approve in the browser if this matches what you intended."

Don't bury the action in a long preamble; one sentence each for *why*, *what*, and *the irreversibility note*.

## Avoid timing-sensitive sequences

Anything of the form "wait N seconds, then check" is fragile. WS1 commands are queued; they execute when the device next checks in. Polling from the CLI is fine for status, but don't write logic that assumes a state transition by wall-clock time.

## Tag your ad-hoc smart groups for cleanup

If you must create a smart group for a one-off bulk op, tag it `ad-hoc:agent:<purpose>:<date>` and surface this to the user so they can clean up later. Or: avoid creating one entirely by using `--ids` directly.

## Bulk ops: count, sample, verify, expand

The full pattern for any bulk op:

1. **Count**: `ws1 mdmv4 devices search --user X | jq '.meta.count'`. Sanity-check the count.
2. **Sample**: pick 3 devices from the result; show them to the user. "Does this look right?"
3. **Verify** (optional but recommended for >10 targets): run with `--dry-run` first.
4. **Expand**: actual call with `--ids`.

## Async jobs: hand off rather than babysit

For wipe / publish / install jobs that take minutes, prefer to return the `job_id` to the user with instructions to poll later, rather than polling inline and tying up the CLI. Tell them the command: `ws1 jobs get --id job_x1y2z3`.

## When a search returns nothing

`IDENTIFIER_NOT_FOUND` does not mean "I should retry with fuzzier terms." It means "the world doesn't match the input." Common causes:
- User typed the wrong tenant or OG.
- Email domain is different from username domain.
- Device was unenrolled and removed.
- Staff is testing with a sample they don't actually have access to.

Surface the result and ask the user. Don't try the same search with a wildcard.

## Don't audit-tamper

The audit log is hash-chained. If you find yourself wanting to "edit" an entry, you're solving the wrong problem. If a user wants to suppress noise from a routine sweep, the right move is to filter at read time (`audit tail | jq 'select(.operation != "...")'`), not mutate history.
