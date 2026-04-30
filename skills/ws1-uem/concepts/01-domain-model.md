# 01 — Domain model

WS1 UEM is a tenant-scoped device management system. Everything is anchored to an **organization group** (OG). Within an OG you have devices, users, applications, profiles, and smart groups (saved queries that resolve to a set of devices). Operations are scoped to an OG; cross-OG operations are rare and explicit.

## Canonical objects

| Object | Identifier shape | Notes |
|---|---|---|
| **Organization Group (OG)** | integer ID | Hierarchical. Most ops require `--og <id>`. |
| **Device** | `DeviceID` (int), `SerialNumber` (string), `Udid` (string) | DeviceID is most stable across resyncs. |
| **User** | `UserID` (int), `Username` (string), `Email` (string) | Email is the most agent-friendly identifier. |
| **Smart Group** | integer ID | A saved query for devices. Use to target large fleets. |
| **Profile** | integer ID + Platform | Configuration profile (Wi-Fi, VPN, restrictions). |
| **Application** | integer ID | Internal or public app catalog entry. |
| **Tag** | string | Free-form labels assigned to devices. |

## Identifier discipline

**Always look up before acting.** Common patterns:

- User goal: "lock alice's phones." Email → users.search → 1 user → devices.search?user=email → list of devices → bulk-lock.
- User goal: "lock device with serial ABC123." Serial → devices.search?serialnumber → 1 device → lock by DeviceID.

If a lookup returns more than one match, the CLI returns `IDENTIFIER_AMBIGUOUS` with the candidates. Surface the choice to the user; never auto-pick.

If a lookup returns zero matches, the CLI returns `IDENTIFIER_NOT_FOUND`. Don't retry with a fuzzier query without asking the user — the search may be wrong (wrong tenant, wrong domain).

## OG context

Set once per session via `ws1 og use 12345`, then every command inherits it. Override per-call with `--og <id>`. The CLI refuses commands that don't have an OG context (`TENANT_REQUIRED`).

OGs nest: a "Global" parent contains regional children (EMEA, APAC, AMER), which contain pilot groups, etc. Most ops respect the hierarchy: targeting `EMEA` includes its child OGs. When in doubt, use the narrowest OG that still covers your targets.

## Device lifecycle

Devices move through these states (visible as `EnrollmentStatus`):

- `Discovered` — known but not enrolled.
- `Enrolled` — fully under management.
- `Unenrolled` — removed from management; a re-enrollment is needed.
- `EnterpriseWipePending` / `DeviceWipePending` — awaiting agent-side execution.
- `Compromised` — flagged by compliance (jailbroken/rooted, etc.).

Don't issue write commands against unenrolled or pending-wipe devices; the API will queue them but they'll never run.
