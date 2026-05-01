# ws1 — Workspace ONE UEM agent CLI

An agent-shaped CLI for [Omnissa Workspace ONE UEM](https://docs.omnissa.com/) and a companion Claude skill. Lets a skill-capable agent (or a human) drive WS1 from natural-language goals, with a finite JSON envelope contract on stdout and a browser-approval flow in front of every destructive operation.

> **Status:** v0.1.0 PoC. Not for production use yet — see [release notes](docs/release-notes/v0.1.0.md) for the "v1 doesn't defend against" list.

---

## Install the CLI

Pick the binary for your machine from the [v0.1.0 release](https://github.com/xyzbuilds/ws1-uem-agent/releases/tag/v0.1.0):

```bash
# macOS Apple Silicon
curl -L https://github.com/xyzbuilds/ws1-uem-agent/releases/download/v0.1.0/ws1-darwin-arm64 \
  -o /usr/local/bin/ws1 && chmod +x /usr/local/bin/ws1

# macOS Intel
curl -L https://github.com/xyzbuilds/ws1-uem-agent/releases/download/v0.1.0/ws1-darwin-amd64 \
  -o /usr/local/bin/ws1 && chmod +x /usr/local/bin/ws1

# Linux x86_64
curl -L https://github.com/xyzbuilds/ws1-uem-agent/releases/download/v0.1.0/ws1-linux-amd64 \
  -o /usr/local/bin/ws1 && chmod +x /usr/local/bin/ws1

# Linux arm64
curl -L https://github.com/xyzbuilds/ws1-uem-agent/releases/download/v0.1.0/ws1-linux-arm64 \
  -o /usr/local/bin/ws1 && chmod +x /usr/local/bin/ws1

# Windows: download ws1-windows-amd64.exe and place it on %PATH%
```

Verify:

```bash
ws1
```

You should see the teal mascot banner and a "no configuration found" prompt.

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
- [`CLAUDE.md`](CLAUDE.md) — project conventions and locked design decisions
- [`docs/spec-acquisition.md`](docs/spec-acquisition.md) — how OpenAPI specs are pulled and hashed into the binary
- [`docs/build-pipeline.md`](docs/build-pipeline.md) — the maintainer's `sync-specs` workflow

---

## License

[MIT](LICENSE) © 2026 xyzbuilds.
