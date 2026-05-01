# ws1 — Workspace ONE UEM agent CLI

An agent-shaped CLI for [Omnissa Workspace ONE UEM](https://docs.omnissa.com/) and a companion Claude skill. Lets a skill-capable agent (or a human) drive WS1 from natural-language goals, with a finite JSON envelope contract on stdout and a browser-approval flow in front of every destructive operation.

> **Status:** v0.1.0 PoC. Not for production use yet — see [release notes](docs/release-notes/v0.1.0.md) for the "v1 doesn't defend against" list.

---

## Safety: human-in-the-loop by design

`ws1` is built for skill-capable agents (Claude, Copilot CLI, etc.) to operate WS1 — but the **human is always in the loop for anything that changes state**. Five guardrails work together:

- **Browser approval for destructive ops.** `wipe`, `unenroll`, `delete`, and any op classified `destructive` in [`operations.policy.yaml`](operations.policy.yaml) opens a localhost approval page in your default browser. The CLI binds to `127.0.0.1:<random-port>`, the agent waits, **the user clicks Approve or Deny**, then the CLI proceeds (or aborts). The agent never sees the request id and cannot fabricate approval.
- **Blast-radius gating.** Even non-destructive write ops (e.g. bulk lock across 200 devices) trigger the same approval flow when target count exceeds a configurable threshold. Scale changes the calculus.
- **Fail-closed policy.** Operations that don't appear in [`operations.policy.yaml`](operations.policy.yaml) are treated as destructive + approval-required (with an `UNKNOWN_OPERATION` warning) until a maintainer classifies them. New API surface never gets a free pass.
- **Stale-resource freshness check.** At execute time the CLI re-fetches each target and compares to the snapshot taken at approval time. Any drift returns `STALE_RESOURCE` and the approval is **not consumed** — the user re-approves with the new state in front of them.
- **Hash-chained audit log.** Every state-changing call appends a JSONL row to `~/.config/ws1/audit.log` with a SHA-256 chain. Tampering is detectable via `ws1 audit verify`. Every envelope's `meta.audit_log_entry` points back to the exact row.

Profile escalation is also user-only: `ws1 profile use operator` refuses to run when the CLI is not attached to a terminal, so an agent piping I/O cannot grant itself write privileges via argv.

See [`SECURITY.md`](SECURITY.md) for the full threat model and the v1-doesn't-defend list.

---

## Install the CLI

Pick the binary for your machine from the [v0.1.0 release](https://github.com/xyzbuilds/ws1-uem-agent/releases/tag/v0.1.0). The default install drops the binary in `~/.local/bin`, which is on `PATH` by default on most modern shells — **no sudo required**.

```bash
# macOS Apple Silicon (M1/M2/M3/M4)
mkdir -p ~/.local/bin
curl -L https://github.com/xyzbuilds/ws1-uem-agent/releases/download/v0.1.0/ws1-darwin-arm64 \
  -o ~/.local/bin/ws1 && chmod +x ~/.local/bin/ws1

# macOS Intel
mkdir -p ~/.local/bin
curl -L https://github.com/xyzbuilds/ws1-uem-agent/releases/download/v0.1.0/ws1-darwin-amd64 \
  -o ~/.local/bin/ws1 && chmod +x ~/.local/bin/ws1

# Linux x86_64
mkdir -p ~/.local/bin
curl -L https://github.com/xyzbuilds/ws1-uem-agent/releases/download/v0.1.0/ws1-linux-amd64 \
  -o ~/.local/bin/ws1 && chmod +x ~/.local/bin/ws1

# Linux arm64
mkdir -p ~/.local/bin
curl -L https://github.com/xyzbuilds/ws1-uem-agent/releases/download/v0.1.0/ws1-linux-arm64 \
  -o ~/.local/bin/ws1 && chmod +x ~/.local/bin/ws1
```

If `~/.local/bin` isn't on your `PATH`, add it once:

```bash
echo 'export PATH="$HOME/.local/bin:$PATH"' >> ~/.zshrc && source ~/.zshrc
```

**Windows:** download `ws1-windows-amd64.exe` from the release page, rename to `ws1.exe`, and drop it in a folder already on `%PATH%` (e.g. `C:\Users\<you>\AppData\Local\Programs\ws1\`).

### Verify

```bash
ws1
```

You should see the teal mascot banner and a "no configuration found" prompt.

### If you want it system-wide

Install to `/usr/local/bin` so every user on the machine can run `ws1`. Needs `sudo` on macOS:

```bash
# macOS Apple Silicon — substitute the binary name for your platform
sudo curl -L https://github.com/xyzbuilds/ws1-uem-agent/releases/download/v0.1.0/ws1-darwin-arm64 \
  -o /usr/local/bin/ws1 && sudo chmod +x /usr/local/bin/ws1
```

## Configure the CLI

Provision an OAuth client in your tenant first:

> WS1 console → **Groups & Settings → Configurations → OAuth Client Management → Add**.
> Match the role to the profile (`ro` → Read-Only, `operator` → Console Administrator, `admin` → elevated).

Then run the wizard:

```bash
ws1 setup                    # Quick — single profile (operator)
ws1 setup --advanced         # configure ro + operator + admin in one run
```

You'll be asked for:

1. Tenant hostname (use the **`as`-prefix** REST API URL, e.g. `as1784.awmdm.com`)
2. Region (`uat` / `na` / `emea` / `apac`)
3. OAuth `client_id` + `client_secret` (stored in your OS keychain)
4. Default Org Group (fetched live from your tenant)

Setup runs an OAuth round-trip and a smoke-test API call, then prints a "Setup complete" cheat-sheet.

Re-running `ws1 setup` is the **reconfigure** path — existing values are offered as defaults; press Enter to keep each.

## Use the CLI

```bash
ws1 status                              # one-envelope config snapshot
ws1 doctor                              # connectivity / auth / OG sanity
ws1 ops search wipe                     # find ops by substring
ws1 ops list --section mdmv1 --tag devices --summary
ws1 mdmv1 devices search --pagesize 5   # first page of devices
ws1 mdmv1 devices lock --id <id>        # write-class — triggers browser approval
```

Every command emits a JSON envelope on stdout. When stdout is a TTY the envelope is pretty-printed; when piped it stays compact for downstream parsers (`jq`, scripts, agents). On a TTY, `ops list` and `doctor` also render colored summary tables to **stderr** alongside the envelope.

---

## Install the Skill (`ws1-uem`)

The companion skill teaches Claude how to use the CLI safely. It lives in [`skills/ws1-uem/`](skills/ws1-uem/) (one `SKILL.md` ~3-4k tokens, plus concept and pattern docs, plus an auto-generated reference index).

### Claude Code

```bash
mkdir -p ~/.claude/skills
cp -r skills/ws1-uem ~/.claude/skills/
```

Then in a Claude Code session: `/skills` to confirm it's loaded, or just say "use the ws1-uem skill" and Claude will invoke it.

### Other Claude-family runtimes

The skill is plain markdown — drop `skills/ws1-uem/` wherever your runtime expects skill bundles. The structure is:

```
skills/ws1-uem/
├── SKILL.md                 entry point (~3-4k tokens)
├── concepts/                domain model, API surface, safety, practices
├── patterns/                lookup-then-act, target-by-smart-group, …
└── reference/operation-index.md   auto-generated catalog
```

The skill assumes the CLI is on PATH and configured (it'll tell the user to run `ws1 setup` if not).

---

## Build from source

```bash
git clone https://github.com/xyzbuilds/ws1-uem-agent.git
cd ws1-uem-agent
make build            # binary at bin/ws1
sudo make install     # /usr/local/bin/ws1 (or INSTALL_DIR=~/.local/bin)
make test             # all tests
make release          # cross-compile darwin/linux/windows into dist/
```

Requires Go 1.25+. Lint with [golangci-lint](https://golangci-lint.run) v2.

---

## Documentation

- [v0.1.0 release notes](docs/release-notes/v0.1.0.md) — feature list, install, demo path, known limitations
- [`SECURITY.md`](SECURITY.md) — threat model, what v1 does and doesn't defend, OS-user-scope notes

---

## License

[MIT](LICENSE) © 2026 xyzbuilds.
