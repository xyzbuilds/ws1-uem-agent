# Pattern: target-by-smart-group

When the target set is too large to enumerate by ID, lean on smart groups.

## When

- More than 50 targets (default `blast_radius_threshold`).
- A recurring criterion ("all iOS devices in EMEA-Pilot") that ops staff already track.
- Any criterion that may shift between your read and your write (compliance status, enrollment state) — smart groups resolve at execution time.

## Steps

1. **Look for an existing SG.** `ws1 mdmv4 smart-groups search --name <substring>`. Existing SGs are visible in the WS1 console; ad-hoc ID lists are not.
2. **If none fits, ask the user before creating one.** Smart groups affect WS1 console UX; an ad-hoc one is a long-tail mess.
3. **Reference the SG by ID** in the bulk-command call, not its name.
4. **Cleanup**: if you created an ad-hoc SG, surface its ID to the user with a "delete when done" note.

## Worked example

User: "Lock all unenrolled-pending devices in EMEA-Pilot."

```bash
# 1. Find the SG.
ws1 mdmv4 smart-groups search --name "Unenrolled Pending"
#  -> SmartGroupID=842

# 2. Inspect membership before action.
ws1 mdmv4 smart-groups members --id 842
#  -> 73 devices

# 3. Above blast threshold; the lock will go through approval.
#    Tell the user: "Locking 73 devices via SmartGroup 842. Browser
#    approval will open."

ws1 mdmv4 devices bulkcommand --smart-group 842 --command Lock
```

## Anti-patterns

- **Don't create one-off SGs without consent**: they pollute the console.
- **Don't use SG name in the runtime call**: names are mutable; IDs aren't.
- **Don't combine SG targeting with ad-hoc ID overrides**: the resolved set isn't reproducible from the audit log alone.
