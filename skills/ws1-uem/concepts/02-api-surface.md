# 02 — API surface

The WS1 REST API is split across sections. The `ws1` CLI mirrors this split: each section becomes a top-level subcommand named with its versioned slug.

## Sections (v0)

| Section | Slug | What lives here |
|---|---|---|
| Mobile Device Management | `mdmv4` (and v1/v2/v3 for legacy) | Devices, commands (lock/wipe/restart), enrollment ops |
| Mobile Application Management | `mamv1` / `mamv2` | App catalog, install/uninstall, app group bindings |
| Mobile Content Management | `mcmv1` | Content publishing, AwContents |
| Mobile Email Management | `memv1` | Email policies, blocked devices |
| System | `systemv1` / `systemv2` | Users, OGs, admins, roles |

The version-in-command is verbose (`ws1 mdmv4 devices lock` rather than `ws1 mdm devices lock`). v0 keeps it explicit because some sections ship multiple concurrent versions with different signatures. Run `ws1 ops list` to see what's compiled in.

## Operation identifier

`<section>.<tag>.<verb>` — e.g. `mdmv4.devices.search`. The CLI command is the same with dots replaced by spaces.

`section`: the slug. `tag`: the OpenAPI tag, lowercased (e.g. `Devices` → `devices`). `verb`: the operationId suffix after the underscore, with `Async` stripped (e.g. `Devices_LockAsync` → `lock`).

## Envelope schema

Every command emits a single JSON object on stdout. Top level:

```json
{
  "envelope_version": 1,
  "ok": true,
  "operation": "mdmv4.devices.lock",
  "data": <any>,
  "meta": { "duration_ms": 412, ... }
}
```

On error, `ok: false` and `error: {code, message, details}` replaces `data`.

### Read-flavour meta

```json
"meta": {
  "duration_ms": 312, "count": 2, "page": 1, "page_size": 100, "has_more": false
}
```

`count` is the total matched (not just this page). Pagination loop: increment `page` until `has_more: false`.

### Write-flavour meta

```json
"meta": {
  "duration_ms": 412,
  "approval_request_id": "req_a1b2c3",
  "audit_log_entry": "2026-04-30T14:00:00Z#117"
}
```

`approval_request_id` is non-empty whenever the op went through the browser approval flow.

### Bulk / partial-success

```json
"data": {
  "successes": [{"DeviceID": 12345, "command_uuid": "cmd_a1"}, ...],
  "failures":  [{"DeviceID": 12347, "error": {"code": "STALE_RESOURCE", "message": "..."}}]
},
"meta": {
  "target_count": 3, "success_count": 2, "failure_count": 1
}
```

Branching rule:
- `ok: true && failure_count == 0` → all good.
- `ok: true && failure_count > 0` → partial; check `data.failures`, decide retry/escalate.
- `ok: false` → the call itself failed; nothing was done OR the state is unknown. Investigate before retrying.

### Async

```json
"data": {"job_id": "job_x1y2z3", "status": "Pending", "poll_url": "ws1://jobs/job_x1y2z3"},
"meta": {"async": true, "approval_request_id": "req_c3"}
```

Either hand `job_id` back to the user or poll: `ws1 jobs get --id job_x1y2z3 --watch`.

## Error taxonomy

Every `error.code` value is from this finite set:

| Code | Meaning | Recommended action |
|---|---|---|
| `AUTH_INSUFFICIENT_FOR_OP` | Active profile can't perform op's class | Surface; ask user to switch profile |
| `APPROVAL_REQUIRED` | Approval gated and CLI couldn't prompt | Should not normally surface |
| `APPROVAL_TIMEOUT` | User did not click within 5 minutes | Surface; ask whether to retry |
| `APPROVAL_DENIED` | User clicked Deny | Stop. Do not retry. |
| `IDENTIFIER_AMBIGUOUS` | Lookup returned >1 candidate | Surface candidates; ask user to pick |
| `IDENTIFIER_NOT_FOUND` | Lookup returned 0 | Surface; consider the search may be wrong |
| `TENANT_REQUIRED` | No OG context | Run `ws1 og use <id>` then retry |
| `RATE_LIMITED` | API rate limit | Backoff per `details.retry_after_seconds` |
| `ASYNC_JOB_PENDING` | Job not yet complete | Wait/poll, or return job_id to user |
| `STALE_RESOURCE` | Target's state changed between approval and execute | Re-fetch; re-confirm with user; re-approve |
| `UNKNOWN_OPERATION` | Op is in spec but not policy.yaml | Treated destructive (fail-closed); surface warning |
| `SPEC_VERSION_MISMATCH` | CLI's spec older than tenant | Recommend `ws1 update` |
| `NETWORK_ERROR` | Could not reach API | Surface; check connectivity |
| `INTERNAL_ERROR` | Bug in CLI | Surface; recommend `ws1 send-feedback` |

## Exit codes

For shell scripting and concise agent branching:

| Exit | Meaning |
|---|---|
| 0 | `ok: true`, no failures |
| 1 | `ok: true` with `failure_count > 0` (partial) |
| 2 | Recoverable (rate-limit, approval-timeout, network, stale) |
| 3 | Config / auth (insufficient profile, missing OG, version mismatch) |
| 4 | Validation (ambiguous, not-found, unknown-operation) |
| 5 | Internal error / unmapped |
