# Pattern: lookup-then-act

The fundamental shape of nearly every WS1 task.

## When

Any time the user gives you a human-friendly identifier (email, name, serial, friendly name) and you need a stable internal ID (UserID, DeviceID, OG ID).

## Steps

1. **Search** with the human identifier. Catch ambiguity (`IDENTIFIER_AMBIGUOUS`) and zero-match (`IDENTIFIER_NOT_FOUND`) explicitly.
2. **Surface the candidate** to the user if there's only one match — but the action class is destructive or above-threshold-write. ("I found Alice — UserID 10001, alice@example.com. Is this the right alice?")
3. **Get** the full record so you have the snapshot fields needed for approval flow.
4. **Act** with the stable ID.

## Worked example

User: "Lock alice's iPhone."

```bash
# 1. Search by user → get devices.
ws1 systemv2 users search --email alice@example.com
#  -> 1 user, UserID=10001
ws1 mdmv4 devices search --user alice@example.com
#  -> 2 devices: iPhone 15 (DeviceID=12345), MacBook Pro (12346)

# 2. The user said "iPhone" — DeviceID 12345 is the only iPhone match.
#    Show the device to the user; confirm.

# 3. Get for the freshness snapshot.
ws1 mdmv4 devices get --id 12345

# 4. Lock.
ws1 mdmv4 devices lock --id 12345
```

## What can go wrong

- **Ambiguous search**: never auto-pick. Surface options and ask.
- **Zero match**: don't retry with fuzzier search. Ask the user.
- **Stale state between search and act**: the device may have unenrolled. The CLI's freshness check covers this for destructive ops; for write-class single ops the snapshot is captured pre-call so drift is detected.
