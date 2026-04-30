# Security model

This document is the v1 threat model for the `ws1` CLI + `ws1-uem` skill
pair. It tracks what the system defends against, what it doesn't, and
where v2 closes the v1 gaps.

## Trust boundaries

Three principals:

| Principal | Holds | Trusted for |
|---|---|---|
| **User** (human) | Browser session, OS keychain, terminal | Approving destructive ops; switching auth profile; setting OG context |
| **CLI** (per-invocation process) | OAuth client creds (loaded from keychain), in-memory approval state | Generating approval requests; issuing/verifying approval; making API calls |
| **Agent** (any skill-capable runtime) | Bash tool only | Calling the CLI with arguments; reading stdout/stderr |

Capability flows downward; trust does not flow upward. The agent calls the
CLI but cannot fabricate approval, intercept the browser callback, or read
the CLI's in-process state.

## What v1 defends against

- **Agent mistakes / hallucinated commands.** The CLI rejects unknown ops
  (fail-closed via `operations.policy.yaml`) and refuses to execute
  destructive ops without browser approval. The approval page shows the
  user the exact target identity and operation; an agent can't bypass.
- **Accidental scale.** `blast_radius_threshold` in the policy file forces
  approval on bulk write ops above N targets, even if individually
  reversible.
- **Spec drift.** New ops appearing in the WS1 spec without classification
  in `operations.policy.yaml` are treated as destructive (fail-closed)
  with a `UNKNOWN_OPERATION` warning.
- **Stale-resource race**. After approval, the CLI re-fetches each target
  and compares it to the snapshot taken at approval time. Drift returns
  `STALE_RESOURCE`; approval is **not** consumed (the user re-approves).
- **Profile escalation via argv.** `ws1 profile use` refuses to run when
  the CLI is not attached to a terminal, so an agent piping I/O cannot
  switch the active profile to escalate its own permissions.

## What v1 does NOT defend against

- **A compromised agent process on the same machine.** Such an agent can
  read keychain entries the user has previously granted to `ws1` and can
  modify `~/.config/ws1/audit.log`. v2 mitigations:
  - per-invocation credential injection from a separate signer process
  - hash-chained audit entries shipped to a remote write-only sink
  - a long-running `ws1d` daemon that holds the secret and accepts
    signed RPC calls from the CLI
- **Audit-log tampering.** v1 hash-chains entries so post-hoc tampering
  is *detectable* via `ws1 audit verify`; it is not *prevented*. v2
  ships entries to a remote sink as they're created.
- **Multi-agent / multi-user concurrency on the same tenant.** The
  per-invocation in-memory approval state is not coordinated across
  concurrent CLI processes. v2 considers a daemon for cross-invocation
  state.

## Where each secret lives

| Secret | Storage | Why |
|---|---|---|
| OAuth `client_id` / `client_secret` | OS keychain (macOS Keychain / Windows DPAPI / Linux secret-service via libsecret) | Same-user processes can't read other apps' keychain entries without explicit grant. Linux without secret-service: opt-in disk fallback under `WS1_ALLOW_DISK_SECRETS`. |
| Active profile selection | `~/.config/ws1/profile` (plaintext, name only) | Not sensitive; no creds in this file. |
| Active OG | `~/.config/ws1/og` (plaintext, ID only) | Not sensitive. |
| In-flight approval state | CLI process memory only | Dies with the process; cannot be replayed. |
| Audit log | `~/.config/ws1/audit.log` (JSONL, hash-chained) | v1 limitation: writable by the agent. v2 hardens. |

## Approval flow (concretely)

For any op classified `destructive` or any write-class op above the
blast-radius threshold, before any state-changing API call:

1. CLI snapshots the target state via `mdmv4.devices.get` (or equivalent).
2. CLI binds an HTTP server to `127.0.0.1:<random-port>`.
3. CLI generates `request_id = "req_" + 16 random bytes hex`.
4. CLI opens the user's browser at `http://127.0.0.1:<port>/r/<request_id>`
   (also prints the URL to stderr for headless-like cases).
5. User clicks Approve or Deny in their browser; the click is verified
   server-side by the CLI process.
6. CLI re-fetches the target; if drift since the snapshot, abort with
   `STALE_RESOURCE` and **do not consume** the approval.
7. CLI executes the API call; appends an audit-log entry; emits envelope.

The agent never sees the `request_id`, never has a path that produces a
valid approval, and cannot read the in-process state.

## Out-of-process attack surface

The browser server listens on `127.0.0.1` only (loopback). Routes:

- `GET /r/<request_id>` — the approval page (HTML form)
- `POST /r/<request_id>/approve` — approve
- `POST /r/<request_id>/deny` — deny
- `GET /healthz` — 204 (test convenience)

The `request_id` is required in the path, so a malicious local process
that doesn't observe the URL printed to stderr would have to brute-force
128 bits.

## Reporting

Security issues should be reported privately to the maintainer (see
`go.mod` for the canonical contact). Do **not** open a public issue for
a vulnerability; we'll triage privately and disclose after a fix lands.
