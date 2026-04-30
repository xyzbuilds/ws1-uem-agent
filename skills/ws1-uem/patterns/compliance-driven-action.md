# Pattern: compliance-driven-action

Acting on a compliance signal: a set of devices in some flagged state.

## When

- "Lock everything in 'compromised' state."
- "Unenroll devices with no check-in for 90 days."
- "Push a profile to devices missing it."

The trigger is a **state**, not an identifier list. State is mutable; the action set must resolve at execute time.

## Steps

1. **Express the criterion as a smart group** if it isn't already. Compliance ops staff usually have these saved.
2. **Read the current membership** to confirm the count is sane (`smart-groups members --id`).
3. **Sample**: pick 3 devices; show the user. "Does this match what you mean?"
4. **Act** via bulk command + smart group ID. The action resolves the set fresh.
5. **Verify** with a follow-up read (`devices.get` on a sample) once the command queue is processed.

## Worked example

User: "Wipe all compromised devices in EMEA-Pilot."

```bash
# 1. Find the SG.
ws1 mdmv4 smart-groups search --name "Compromised" --og 12345
#  -> SmartGroupID=901, member count is dynamic

# 2. Inspect.
ws1 mdmv4 smart-groups members --id 901
#  -> 4 devices

# 3. This is destructive. Tell the user clearly:
#    "Enterprise-wiping 4 compromised devices in EMEA-Pilot. Irreversible.
#     Browser approval will open."

# 4. Act.
ws1 mdmv4 devices bulkcommand --smart-group 901 --command EnterpriseWipe

# 5. Verify on a sample after a few minutes.
ws1 mdmv4 devices get --id <sample-id>
#  -> EnrollmentStatus: EnterpriseWipePending or Unenrolled
```

## What can go wrong

- **Membership shifts between read and act.** That's actually fine for compliance ops — you *want* the action to apply to the current set, not the snapshot. But surface this to the user: "the set may grow/shrink between now and execute."
- **State transitions are eventually consistent.** A device labelled compromised may have already been remediated by the time your command runs. Most ops are idempotent enough that this is harmless; verify with the follow-up read if you're unsure.
