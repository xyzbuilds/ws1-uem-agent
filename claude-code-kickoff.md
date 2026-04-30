# Claude Code Kickoff Prompt

Paste the prompt below into a fresh Claude Code session, run from this directory:

```
cd "/Users/xyz/Coding/ws1-uem-agent/WS1 UEM Agent"
claude
```

---

## Prompt to paste

> I'm building a CLI + Skill pair called `ws1` / `ws1-uem` that lets skill-capable agents operate Omnissa Workspace ONE UEM safely from natural-language goals. The full design is in `ws1-uem-agent-v0-spec.md` and the binding conventions / locked decisions are in `CLAUDE.md`. There's also a maintainer pipeline spec in `docs/build-pipeline.md` and a spec-acquisition reference in `docs/spec-acquisition.md`. Read all four before touching code.
>
> Today's task is **session 1 from `claude-code-kickoff.md`**: scaffold the v0 Go project skeleton.
>
> Specifically, in this session:
>
> 1. Create the Go module (`module github.com/<my-username>/ws1-uem-agent` — ask me what to use). Initialize `go.mod`. Pick Go 1.22+.
> 2. Lay out the directory tree per `CLAUDE.md` Conventions → Code: `cmd/`, `internal/api/`, `internal/policy/`, `internal/approval/`, `internal/audit/`, `internal/envelope/`, `internal/generated/`, `test/integration/`, `spec/`.
> 3. Add Cobra + Viper dependencies. Skeleton root command in `cmd/ws1/main.go` and `cmd/ws1/root.go`.
> 4. Implement `internal/envelope/` per spec section 5: types for `Envelope`, `Error`, the partial-success shape, exit-code helper. Unit tests for serialization round-trips covering all five envelope flavors (read success, write success, partial success, async, error).
> 5. Implement `ws1 --version --json` and `ws1 doctor` (the latter as a stub that prints a structured pass-fail envelope; real auth/connectivity checks come later).
> 6. Add a `Makefile` or `magefile.go` with: `build`, `test`, `lint`, `generate`. CI config (`.github/workflows/ci.yml`) running build + test + lint on push.
> 7. Add `.golangci.yml` with sane defaults.
> 8. Commit incrementally per the Conventions → Commits rule. One logical change per commit, conventional-commit prefixes.
>
> Do **not** in this session: implement OAuth, call any real API, write the approval server, write the policy loader, or generate from `spec.json` (we don't have one yet). Stay scoped to the skeleton, the envelope package (with full tests), the version/doctor stub commands, and CI. Everything else gets its own session.
>
> Before you start, confirm: (a) what module path I want (ask me), (b) that you've read both `CLAUDE.md` and `ws1-uem-agent-v0-spec.md`, and (c) the commit you plan to land first.
>
> When you finish each commit, summarize what you did in one sentence and pause for me to inspect before starting the next.

---

## Why this scope

The first session is deliberately narrow: skeleton + envelope package + CI. Reasons:

- The envelope is the contract that touches every other piece. Get it right with full test coverage before anything builds on top.
- No real API calls means no need for `spec.json`, no OAuth setup, no tenant. You can iterate fast and offline.
- `ws1 doctor` is a useful smoke test: it exercises the envelope, exit codes, and the command tree without touching anything risky.
- CI from session 1 means every subsequent change gets validated automatically.

## Suggested next sessions (in order)

| # | Task | Prerequisite |
|---|---|---|
| 2 | Build `ws1-build discover` + `ws1-build fetch` per `docs/build-pipeline.md` stages 1–2. Run once against `as1831.awmdm.com` with the user's token; commit resulting `spec/*.json` and `spec/VERSION` | Session 1 |
| 3 | Build `ws1-build codegen-cli` + `codegen-skill` + `classify-check` (stages 4, 5, 7). Wire `make sync-specs` Makefile target. Verify a clean run produces `internal/generated/*.go` and an empty unclassified list (initial `operations.policy.yaml` covers all current ops) | Session 2 |
| 4 | OAuth client-credentials flow + keychain integration; profile model (`ro`/`operator`/`admin`) | Session 1 |
| 5 | First three read commands: `ws1 mdmv4 devices search`, `ws1 mdmv4 devices get`, `ws1 systemv2 users get` (or whichever versions are current) | Sessions 3 + 4 |
| 6 | `operations.policy.yaml` loader + classification middleware; fail-closed default. Note: the policy file is created in session 3 as part of classify-check; session 6 wires the runtime enforcement | Session 3 |
| 7 | Browser approval server (the safety-critical session — high test coverage required) | Session 5 |
| 8 | First write command: `ws1 mdmv4 devices lock` (reversible, low-risk; exercises approval) | Sessions 6 + 7 |
| 9 | Audit log with hash chain + `ws1 audit verify` | Session 8 |
| 10 | Bulk lock + partial-success envelope; freshness check on stale resources | Sessions 8 + 9 |
| 11 | Skill drafting: `SKILL.md` + the five concept files (the auto-generated `reference/operation-index.md` already exists from session 3) | Session 10 |
| 12 | End-to-end demo: alice-lock scenario from spec section 11 | Session 11 |

Each session is ~1-3 hours of Claude Code work. v0 demo done in roughly two weeks of evening sessions.

## Things only you can do (block specific sessions)

- Provision OAuth clients (`ro`, `operator`, `admin`) in your WS1 tenant. Blocks session 4.
- Provide a bearer token from your tenant for the first spec pull. Blocks session 2.
- Decide module path / GitHub org for the repo. Blocks session 1.
- Initial classification of the ~200 ops on first spec pull. Blocks session 3 finishing cleanly. (Pragmatic approach: bulk-classify by HTTP method as a starting point — session 3 prompt should ask Claude Code to apply the heuristic suggestions wholesale and then ask you to review specific destructive ops; this is the only place we relax the "no auto-classification" rule, and only at v0 bootstrap.)
- Decide v0 distribution channel (just GitHub releases is fine for v0). Not blocking.
