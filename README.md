# `mmb` — agent CLI for monstermailbox

> **Who this is for**: AI agents (Claude, Cursor, Aider, GitHub Copilot
> Agent, your custom MCP server, your launchd-driven cron job, your
> n8n workflow). Anything that runs on a machine and has its own
> autonomous email needs.
>
> **Who this is NOT for**: humans. If you're a human reading this,
> you probably want the web dashboard at `https://app.monstermailbox.com`
> for inbox triage, claim flow, MFA setup, agent management, and
> human-in-the-loop quarantine review. The CLI is the agent's
> equivalent — same data, agent-shaped surface.

The CLI lets an agent read its inbox, manage its own policy
(whitelist + expectations), reply to existing threads, and start new
ones — without going through the web UI. Each command emits JSON on
stdout for clean piping.

## Install

### `go install` (any platform with a Go toolchain)

```sh
go install github.com/theinventor/monstermailbox-cli@latest
# binary lands in $(go env GOPATH)/bin/monstermailbox-cli
# rename to `mmb` if you prefer the short name:
mv "$(go env GOPATH)/bin/monstermailbox-cli" "$(go env GOPATH)/bin/mmb"
```

### Pre-built binary (no Go required)

Grab the right tarball/zip for your OS+arch from the
[releases page](https://github.com/theinventor/monstermailbox-cli/releases),
extract `mmb`, and put it on `$PATH`:

```sh
# macOS arm64 example
VERSION=$(curl -s https://api.github.com/repos/theinventor/monstermailbox-cli/releases/latest | grep tag_name | cut -d'"' -f4)
curl -fsSL "https://github.com/theinventor/monstermailbox-cli/releases/download/${VERSION}/mmb_${VERSION#v}_darwin_arm64.tar.gz" \
  | tar -xz -C /tmp
mv /tmp/mmb ~/.local/bin/mmb
chmod +x ~/.local/bin/mmb
```

Once installed, `mmb update` self-updates from GitHub Releases.

### From source

```sh
git clone https://github.com/theinventor/monstermailbox-cli
cd monstermailbox-cli
go build -o ~/.local/bin/mmb .
```

## First-time auth — `mmb auth login`

The agent-friendly flow: register a new agent and persist the
returned API key in one shot.

```sh
mmb auth login --address my-bot --email someone-human@example.com
# → creates my-bot@monstermailbox.com
# → prints the key fingerprint (the full key never echoes back)
# → stores the key in the OS keychain by default (mode-0600 file as fallback)
# → sends a claim invite to the human owner
```

After login, no environment variables are needed — every subsequent
command picks up the saved profile automatically.

If the agent already has an API key (issued from the dashboard, copied
from a teammate, exported from a secrets manager), use:

```sh
mmb auth save \
  --profile my-bot \
  --api-key mmb_… \
  --api-url https://api.monstermailbox.com \
  --agent-address my-bot@monstermailbox.com
```

## Auth resolution order

At every command invocation the CLI resolves credentials in this order:

| # | Source | Notes |
| - | --- | --- |
| 1 | `--profile <name>` flag (where supported) | Explicit override |
| 2 | `MONSTERMAILBOX_API_KEY` env var | Beats persisted default. Set `MONSTERMAILBOX_API_URL` too |
| 3 | Default profile in `~/.config/mmb/config.json` | The "I logged in once and forgot about it" path |
| 4 | None — only public endpoints work | `/health`, `/version`, `/agents/register` |

A CI worker can set `MONSTERMAILBOX_API_KEY` and ignore the config
file. A local dev agent can `mmb auth login` once and forget about
secrets. The two don't fight.

## Storage backends

The persisted API key for a profile lives in one of two places:

| Backend | When | Where |
| --- | --- | --- |
| `keychain` (default) | macOS / Windows / Linux with a libsecret agent | OS keyring, encrypted at rest |
| `file` (fallback) | Headless servers, containers, CI without a keyring | `~/.config/mmb/config.json`, mode 0600 |

Override with `--storage=keychain|file|auto` on `auth login` / `auth save`,
or set `MMB_STORAGE=file` globally. `auto` (the default) tries keychain
first and falls back to file when no keyring is available.

Coming from an older release with file-backed profiles? Move them off
disk:

```sh
mmb auth migrate --all      # every file-backed profile → keychain
mmb auth migrate --profile my-bot   # one at a time
```

Migration is idempotent — re-running on already-keychained profiles
prints `skipped`.

## Multiple identities

Switching between agents (testing as a different identity, running
multiple bots from one shell, etc.) is first-class:

```sh
mmb auth list                     # list saved profiles, default starred
mmb auth use other-bot            # change the default profile
mmb auth status --profile X       # inspect a specific profile
mmb auth logout [--profile X]     # remove a profile
```

## Commands

```text
# Auth
mmb auth login    --address <local> --email <owner> [--storage keychain|file|auto]
mmb auth save     --profile <name> --api-key <key> [--api-url <url>] [--storage keychain|file|auto]
mmb auth status   [--profile <name>]
mmb auth list
mmb auth use      <profile>
mmb auth logout   [--profile <name>]
mmb auth migrate  --profile <name> | --all     # move file-backed keys to keychain

# Identity & connectivity probe
mmb whoami

# Inbox (read)
mmb inbox list     [--state trusted|quarantined|rejected] [--limit N]
mmb inbox watch    --json                          # SSE stream of events
mmb msg show       <id>

# Outbound
mmb new-email      --to <addr> --subject <s> --body <s> [--cc <addr>...] [--bcc <addr>...]
mmb reply-to-email --to-message-id <id>      --body <s> [--cc <addr>...] [--bcc <addr>...] [--subject-override <s>]
                  # threading is automatic; subject + recipient are derived from the original
mmb send           --to <addr> --subject <s> --body <s> [--in-reply-to <id>]
                  # deprecated alias; new agents should use new-email / reply-to-email

# Policy
mmb whitelist list
mmb whitelist add  --sender <addr> | --sender-regex <regex>  [--subject-regex <regex>]
mmb expect         --from <addr> [--subject-regex <regex>] [--purpose <text>] [--window <duration>]

# Quarantine (agent-side; human-in-the-loop release happens in the dashboard)
mmb quarantine list [--limit N]
mmb quarantine escalate <id>     # v1 stub
```

Most commands emit JSON; pipe through `jq` for filtering:

```sh
mmb inbox list --state trusted | jq '.messages[].subject'
```

## What the CLI does NOT do

These are deliberately out of scope. Humans, not agents, do them:

- **Sign up / claim the agent.** The human owner gets the invite email
  and clicks the magic link in their browser. Agents can't claim
  themselves — that's the human-in-the-loop boundary.
- **Approve quarantined messages.** The agent sees that a quarantine
  exists (and a redacted preview); the human releases or rejects via
  the dashboard. Deliberate design choice; see
  `app/services/sanitizer/` and `app/services/risk_engine.rb`.
- **MFA enrollment.** Passkeys / TOTP / backup codes are for the human
  owner's dashboard auth, not the agent.
- **Bridge daemon** (`mmb-bridge`) configuration. Different binary,
  different concerns; see `cli/mmb-bridge/`.

## Config file

`~/.config/mmb/config.json` (XDG-compliant; honors `XDG_CONFIG_HOME`).
Mode 0600 because it stores API keys in plaintext. Override via
`MMB_CONFIG=/path/to/file.json` (useful for tests, agent sandboxing).

```jsonc
{
  "default_profile": "my-bot",
  "profiles": {
    "my-bot": {
      "api_url":       "https://api.monstermailbox.com",
      "api_key":       "mmb_…",
      "agent_address": "my-bot@monstermailbox.com",
      "owner_email":   "human@example.com",
      "created_at":    "2026-05-03T22:00:00Z"
    }
  }
}
```

## Layout

```
cli/mmb/
├── main.go                    ← entry point; thin wrapper around cmd.NewRootCmd()
├── cmd/                       ← cobra command tree (one file per command)
│   ├── root.go
│   ├── auth.go                (login / save / status / list / use / logout)
│   ├── whoami.go
│   ├── register.go            (low-level POST /agents/register; auth login wraps it)
│   ├── inbox.go               (inbox list + inbox watch)
│   ├── msg.go                 (msg show)
│   ├── expect.go
│   ├── whitelist.go
│   ├── send.go                (deprecated)
│   ├── new_email.go
│   ├── reply_to_email.go
│   ├── quarantine.go
│   ├── auth_test.go           ← profile resolution + auth subcommand tests
│   └── cmd_test.go            ← per-command HTTP-shape tests
└── internal/
    ├── client/
    │   └── client.go          ← Bearer auth + base URL resolution
    └── config/
        ├── config.go          ← profile load/save (mode 0600, atomic writes)
        └── config_test.go
```

## OpenClaw skill

The official OpenClaw skill draft lives at
[`skills/monstermailbox-mmb/`](skills/monstermailbox-mmb/). It is kept
public-safe and should not contain API keys, private inboxes, customer
data, local deployment paths, or unpublished plans.

Before publishing to ClawHub, review
[`skills/monstermailbox-mmb/references/publishing.md`](skills/monstermailbox-mmb/references/publishing.md).
The intended public slug is `monstermailbox-mmb`.

## Design principles

This CLI follows the **[10 Principles for Agent-Native CLIs](https://trevinsays.com/p/10-principles-for-agent-native-clis)** by
Trevin Chow. Every new command, flag, and response shape must conform.
See [`docs/CLI_DESIGN_PRINCIPLES.md`](docs/CLI_DESIGN_PRINCIPLES.md) for
the project-internal summary with implementation references.

## Tests

```sh
go test ./...
```

The cmd test suite spins up `httptest.NewServer` per test, points the
CLI at it via `MONSTERMAILBOX_API_URL`, runs the command, and asserts
the outgoing HTTP request (method + path + auth header + body shape)
matches the OpenAPI contract. If you change the wire shape of any
command, the contract test fails loud.

## License

MIT — see the repo root.
