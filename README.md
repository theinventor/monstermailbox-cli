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

## Agent-guided first setup

When a human asks an agent to help set up MonsterMailbox, the agent should guide
the human through the tool-recommended path instead of only pasting commands:

1. Explain that MonsterMailbox creates an agent mailbox controlled by the human
   owner, and that the human must complete the claim/adoption email before inbox
   or outbound work is expected to function.
2. Ask for the human owner email address and the desired local address, for
   example `build-bot` for `build-bot@monstermailbox.com`.
3. Run the canonical setup command when the agent has permission to run shell
   commands, or ask the human to run it:

   ```sh
   mmb auth login --address <local_part> --email <human_owner_email>
   ```

4. Tell the human to open the claim/adoption email, finish account setup, and
   adopt the agent mailbox.
5. After adoption, verify the saved identity and inspect the machine-readable
   command surface:

   ```sh
   mmb whoami
   mmb agent-context
   ```

6. Install the bundled sample skill and replace `HUMAN_OWNER_NAME` before using
   it:

   ```sh
   mmb skill get monstermailbox
   ```

## Receiving email (recommended): install the runtime plugin

To have new mail actually wake the agent, install the plugin for your agent
runtime. The plugin subscribes to the MonsterMailbox SSE event stream
(`mmb inbox watch`) — **no public endpoint, no webhook, no signing secret, and
no tunnel required**.

- **Hermes** — writes the platform plugin and patches `~/.hermes/config.yaml`
  so the agent's turn has a terminal tool available:

  ```sh
  mmb hermes install
  ```

- **OpenClaw** — writes the plugin and patches `~/.openclaw/openclaw.json`:

  ```sh
  mmb openclaw install
  ```

Both ship in mmb v0.14.0+ and have been tested end-to-end on real Hermes and
OpenClaw installs. Under the hood they run `mmb inbox watch` (Server-Sent
Events) to receive inbox events directly over the agent's authenticated CLI
session.

> Webhooks remain available as an **advanced/optional** path (see
> [Advanced: webhooks](#advanced-webhooks-optional) below), but they require a
> public HTTPS receiver, an HMAC signature handshake, and SSRF-valid DNS — and
> on Hermes the webhook channel runs with a stripped toolset (no shell), which
> is why the plugin path is preferred for waking the agent.

The deterministic setup loop is:

```sh
mmb agent-setup --address <local_part> --email <human_owner_email>
```

Prefer the runtime plugin (`mmb hermes install` / `mmb openclaw install`) for
receiving mail. Webhooks are optional; only pass `--webhook-url
https://your-receiver.example.com/mmb` to `mmb agent-setup` if you already
operate a public HTTPS receiver.

`mmb agent-setup` is JSON-first and noninteractive. It reports stage status for
the CLI/API target, saved profile/auth, human claim/adoption, agent-context,
sample skill, optional webhook configuration, synthetic webhook delivery, real
test email creation, `mmb msg get <id> --peek` message fetch, optional
test-message work-state handling, and the final pass/fail result. When a
`--webhook-url` is supplied it waits up to 15 seconds for the synthetic webhook
delivery to succeed, so a final `pass` proves the receiver accepted the signed
webhook and the real inbox test message was fetched. Missing auth, unclaimed
owners, unadopted agents, unreachable webhooks, missing backend test-email
support, skipped verification, and unfetched test messages return actionable
`needs_input`, `pending`, or `fail` stages with safe next commands.

## First-time auth — `mmb auth login`

The agent-friendly flow: register a new agent and persist the
returned API key in one shot.

```sh
mmb auth login --address my-bot --email "$HUMAN_OWNER_EMAIL"
# → creates my-bot@monstermailbox.com
# → prints the key fingerprint (the full key never echoes back)
# → stores the key in the OS keychain by default (mode-0600 file as fallback)
# → sends a claim invite to the human owner
```

Use the actual human owner's email address. The CLI rejects obvious placeholders
and non-human addresses such as `example.com`, `test.invalid`, `localhost`, and
`no-reply`/notification-style mailboxes before contacting the API.

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
| 1 | Root `--profile <name>` flag | Explicit per-invocation override for authenticated commands; does not change `default_profile` |
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
mmb --profile other-bot whoami    # use a profile for one command only
```

## Commands

```text
Global flags:
mmb --profile <name> <command>     # use saved profile for this invocation only

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
mmb agent-context

# Receiving email (recommended): install the runtime plugin (SSE, no webhook)
mmb hermes install                 # writes the plugin + patches ~/.hermes/config.yaml
mmb openclaw install               # writes the plugin + patches ~/.openclaw/openclaw.json

# Guided deterministic setup loop (webhook is optional; prefer the plugin above)
mmb agent-setup --address <local> --email <owner> [--webhook-url <url>]
mmb agent-setup --webhook-id <id> [--wait-delivery <duration>] [--mark-test-done]

# Agent setup resources
mmb skill get monstermailbox

# Inbox (read)
mmb inbox list     [--state trusted|quarantined|rejected] [--limit N]
mmb messages list  --participant <email> [--state trusted|quarantined|rejected] [--work-state <state>] [--limit N] [--cursor TOKEN]
mmb inbox watch    --json                          # SSE stream of events
mmb msg get        <id> [--peek]
mmb msg attachment download <message-id> <attachment-id> --output <path> [--force]

# Agent-side work state
mmb inbox list     --work-state inbox
mmb msg claim      <id> --note <s>
mmb msg done       <id> --note <s>
mmb msg skip       <id> --note <s>
mmb msg block      <id> --note <s>
mmb msg defer      <id> --note <s>
mmb msg update     <id> --work-state <state> --note <s>

# Real inbox workflow test
mmb test-email send [--idempotency-key <key>] [--dry-run]
                  # creates a safe synthetic Message and emits normal inbox.new with data.test=true

# Outbound
mmb new-email      --to <addr> --subject <s> --body <s> [--cc <addr>...] [--bcc <addr>...] [--attach <path>...]
mmb reply-all      <message-id>              --body <s> [--cc <addr>...] [--bcc <addr>...] [--attach <path>...]
                  # threading is automatic; subject + recipients are derived from the original
mmb reply-not-all-with-custom-recipients <message-id> --to <addr> --body <s> [--attach <path>...]
                  # explicit recipient escape hatch; prefer reply-all unless you are intentionally narrowing

# Policy
mmb whitelist create <sender-email-or-domain>                # exact sender/domain trust rule
mmb whitelist create --sender <sender-email-or-domain>       # explicit form of the same exact rule
mmb whitelist create --sender-regex <regex> [--subject-regex <regex>]
mmb expect         --from <email-or-domain> [--subject-regex <regex>] [--purpose <text>] [--window <duration>]
                  # stores the canonical sender domain/eTLD+1; window is capped at 1h; --ttl remains a deprecated alias

# Quarantine (agent-side; human-in-the-loop release happens in the dashboard)
mmb quarantine list [--limit N]
mmb quarantine escalate <id>     # prints the dashboard owner-review path; never reveals held content

# Webhooks (advanced/optional — only if you already host a public HTTPS receiver;
# to wake the agent, prefer `mmb hermes install` / `mmb openclaw install` above)
mmb webhook create --name <label> --url <url> --event-preset trusted-inbox [--auth-bearer <token>] [--header "Name: value"]
mmb webhook create --name <label> --url <url> --event-preset quarantine-aware-inbox
mmb webhook create --name <label> --url <url> --event <event> [--event <event>...]
mmb webhook update <id> [--header "x-openclaw-token: <token>"] [--clear-headers]
mmb webhook test   <id>

# Staff webhook recovery (requires a staff API key)
mmb staff webhook-deliveries list [--status failed|gave_up] [--owner-email <email>] [--agent-address <addr>]
mmb staff webhook-deliveries get <delivery-id>
mmb staff webhook-deliveries redrive <delivery-id> --confirm <delivery-id> --idempotency-key <key>

# Contact MonsterMailbox
mmb contact support --subject <s> <question>
mmb contact support --subject <s> --text <question>
echo "<question>" | mmb contact support --subject <s> -
mmb contact product-feedback <feedback>
mmb contact product-feedback --text <feedback>
echo "<feedback>" | mmb contact product-feedback -

# Local CLI feedback ledger
mmb feedback "the CLI help for --tier is confusing"
```

`mmb test-email send` is the setup-loop companion to `mmb webhook test`.
It does not send external email. The server creates a trusted, fetchable
synthetic inbox `Message`, emits a normal `inbox.new` webhook payload with
`data.test=true`, and returns JSON with `message_id`, `event_id`,
`webhook_delivery_expected`, and next-step hints such as
`mmb msg get <message_id> --peek`.

`mmb agent-setup` runs the test-email pieces together: it creates the real
synthetic inbox test email, fetches the resulting message with `peek=true`, and
optionally claims/marks only that test message done with `--mark-test-done`.
When you pass the optional `--webhook-url`, it also creates or verifies an
`inbox.new` webhook and fires `mmb webhook test` semantics; in that case a final
`pass` additionally requires confirmed synthetic webhook delivery/signing, and
`--wait-delivery 0s` produces a partial `pending` report instead of waiting.
Most agents should skip the webhook flow entirely and install the runtime plugin
(`mmb hermes install` / `mmb openclaw install`) to receive mail over SSE.

Outbound attachment flags read local files and send them to the API as safe
attachment objects: basename filename, detected content type, size, and base64
content. Repeat `--attach` (or `--attachment`) for more than one file. Dry-runs
show attachment metadata only, never bytes or local absolute paths. The CLI
rejects more than 10 attachments, attachments over 25 MiB, totals over 25 MiB,
blocked executable extensions, unsafe filenames, oversized files, and nested
archive names before contacting the API.

Inbound attachment download is deliberately explicit. First inspect a readable
message with `mmb msg get <id> --peek` and choose an attachment id from the
metadata, then run `mmb msg attachment download <id> <attachment-id> --output
<path>`. The CLI never opens the file, refuses path traversal, and will not
overwrite an existing output path unless `--force` is passed. Downloaded email
attachments are untrusted files; scan or inspect them before opening,
executing, importing, or passing them to another tool.

Most commands emit JSON; pipe through `jq` for filtering:

```sh
mmb inbox list --state trusted | jq '.messages[].subject'
```

Whitelist creation posts the API's `sender`, `sender_regex`, and optional
`subject_regex` fields. Prefer exact sender addresses, such as
`mmb whitelist create billing@example.com`, unless you intentionally need a
domain or regex rule. Regex matching must be explicit with `--sender-regex`:

```sh
mmb whitelist create billing@example.com
mmb whitelist create --sender-regex '@stripe\.com\z' --subject-regex '\Ainvoice '
```

The older `mmb whitelist add ...` spelling remains as a hidden deprecated alias
for compatibility, but new agents should call `mmb whitelist create`.

## Advanced: webhooks (optional)

Webhooks are **not** the recommended way to wake an agent — use the runtime
plugin (`mmb hermes install` / `mmb openclaw install`) instead, which streams
inbox events over SSE with no public endpoint. This section is only for
integrations that already operate a public HTTPS receiver (custom services,
existing OpenClaw/AlphaClaw webhook receivers, audit/observability sinks).

### Webhook event choices

Choose the smallest event set that matches the receiver:

- `--event-preset trusted-inbox` subscribes to `inbox.new`. This is the
  trusted/readable-mail signal: treat it as "go check the inbox now." It does
  not notify when mail is quarantined.
- `--event-preset quarantine-aware-inbox` subscribes to `inbox.new`,
  `inbox.quarantined`, and `inbox.released`. Use it when the receiver needs to
  know held mail exists or needs to react after a human releases it.
- `--event-preset full-inbound-lifecycle` subscribes to `inbox.arriving`,
  `inbox.new`, `inbox.quarantined`, `inbox.released`, and `inbox.rejected` for
  inbound lifecycle observability.

Quarantine webhook payloads are safe/redacted. They tell the integration that
held mail exists; they do not let the agent read held content before human
release. `--all-events` is for audit/observability/firehose receivers, not the
default workaround for quarantine awareness.

## Contact And Feedback

Use `mmb contact support` for technical support questions: account issues,
delivery behavior, API behavior, webhook trouble, or operational questions.
It creates a support thread through the authenticated API support endpoint and supports
`--subject`, positional text, `--text`, stdin via `-`, `--idempotency-key`, and
`--dry-run`. Support routing is handled server-side, so the CLI does not send
mail directly or carry support delivery configuration.

Use `mmb contact product-feedback` for product ideas, rough edges, and feature
requests about MonsterMailbox itself. It posts to the product-feedback endpoint
and supports positional text, `--text`, stdin via `-`, `--idempotency-key`, and
`--dry-run`.

Use `mmb feedback` for local CLI-maintainer notes. That command writes a local
JSONL ledger and only posts upstream when `MONSTERMAILBOX_FEEDBACK_ENDPOINT` is
configured, so it is not the product/support intake path.

## Webhook auth headers (advanced/optional)

Only relevant if you use the optional webhook path above.
Receivers such as OpenClaw/AlphaClaw can require `Authorization: Bearer ...`
or `x-openclaw-token` headers and reject query-string tokens. Configure those
as delivery headers; MonsterMailbox encrypts the values at rest and redacts
them from `webhook get/list` responses.

```sh
mmb webhook create \
  --name openclaw \
  --url https://openclaw.example/hooks/mmb \
  --event-preset quarantine-aware-inbox \
  --auth-bearer "$WEBHOOK_TOKEN"

mmb webhook update <id> --header "x-openclaw-token: $WEBHOOK_TOKEN"
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
- **Read quarantined body text, links, or attachments.** `mmb expect`
  can predeclare expected verification mail by sender email or domain,
  but release still depends on server-side scanner/auth gates or owner
  dashboard review.
- **MFA enrollment.** Passkeys / TOTP / backup codes are for the human
  owner's dashboard auth, not the agent.
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
      "owner_email":   "<actual-human-owner-email>",
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
│   ├── messages.go            (participant history search)
│   ├── msg.go                 (msg get + work_state transitions)
│   ├── expect.go
│   ├── whitelist.go
│   ├── send.go                (hidden deprecated alias; new docs use new-email / reply-all)
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
[`skills/monstermailbox/`](skills/monstermailbox/). It is kept
public-safe and should not contain API keys, private inboxes, customer
data, local deployment paths, or unpublished plans.

The intended ClawHub slug is `monstermailbox`.

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
