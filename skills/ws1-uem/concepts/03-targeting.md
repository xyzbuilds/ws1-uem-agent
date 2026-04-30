# 03 — Targeting

The most common cause of bad WS1 outcomes is hitting the wrong devices, or the right ones at too-large a scale. Targeting is a separate concern from the action. Get the target set right; *then* execute.

## Three targeting modes

| Mode | Use when | Example |
|---|---|---|
| **Single** | Exactly one device, identified by ID/serial | `ws1 mdmv4 devices lock --id 12345` |
| **Bulk by ID list** | 2–50 devices, IDs known | `ws1 mdmv4 devices lock --ids 12345,12346,12350` |
| **Smart Group** | >50 devices, or a recurring criterion | reference an existing SG; create one only with explicit user consent |

The CLI's `blast_radius_threshold` is **50** by default for write-class commands. Above 50 targets, even a write-class op (lock, restart) goes through browser approval.

## Decision rules

1. **Stop and count first.** Run `ws1 mdmv4 devices search --user alice@example.com` and look at `meta.count`. If it's surprising — for example, alice has 17 devices when you expected 2 — surface to the user and ask before proceeding.
2. **Narrow with OG before flags.** A search scoped to `--og <child>` is faster, more correct, and less risky than a global search filtered by user. The OG is the spine.
3. **Prefer existing smart groups over ad-hoc.** Smart groups are visible to ops staff in the WS1 console; an ad-hoc bulk list is invisible. If you find yourself targeting "all iOS devices in EMEA-Pilot," look for a saved SG with that semantics.
4. **Pilot rings before tenant-wide.** If the op affects more than ~5% of the device fleet, propose to the user that you run against a pilot ring first. Pilot rings are usually labelled `*-Pilot` or `*-Canary` in the OG hierarchy.

## "Find all devices for X"

The classic alice-lock pattern:

```bash
# 1. Find the user (catch ambiguity)
ws1 systemv2 users search --email alice@example.com

# 2. Lookup their devices
ws1 mdmv4 devices search --user alice@example.com --og 12345

# 3. Show summary; confirm with user before the next step.

# 4. Lock all
ws1 mdmv4 devices lock --ids 12345,12346
```

Step 3 is non-negotiable for any class>read action. Show the user the device list (count, owners, OGs); ask for explicit confirmation before issuing.

## What about "all devices in compliance state X"?

Use the search filter (`compliancestatus`) where supported. If it's not parameterised, you'll need a smart group. Do not loop over `ws1 mdmv4 devices search` and filter client-side for state-changing ops — that's a race condition by construction (a device may transition between your read and your write).

## Cross-OG operations

Rare. Almost always wrong. If a user goal seems to span multiple OGs, ask whether they really mean "Global" (the parent) or whether they mean a specific child OG.
