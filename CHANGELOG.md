# Changelog

## Unreleased

- Turned OFF Hermes' per-sender pairing for the MonsterMailbox platform: the plugin now defaults `MMB_ALLOW_ALL_SENDERS=true` (the MMB server already gates inbound by trust-state), so unknown senders are processed instead of emailed pairing codes. Override with `MMB_ALLOW_ALL_SENDERS=false` / an `MMB_ALLOWED_SENDERS` allowlist.
- Fixed Hermes adapter mmb 401 under a supervised gateway: mmb subprocesses now run with the recorded HOME (where the mmb profile lives), via `MMB_HOME` / an installer-recorded `mmb_home`.
- Fixed both packaged adapters calling `mmb msg get <id> --peek --json`; `msg get` has no `--json` flag (it emits JSON with `--peek`), so the adapter failed to fetch the message even after receiving an event. Removed the invalid flag (Hermes + OpenClaw).
- Fixed the packaged Hermes plugin adapter for real gateways (found running it
  under Hermes): `connect()`/`disconnect()` now accept lifecycle kwargs (e.g.
  `is_reconnect`); mmb is resolved via MMB_BIN > installer-recorded absolute
  path (`mmb_path`) > PATH > `mmb` so it no longer depends on the gateway's
  PATH; the watcher now loops `mmb inbox wait` + inbox reconcile instead of a
  long-lived `mmb inbox watch` (self-heals after `mmb update` and survives hosts
  where a long-lived watch goes silent); and a duplicate-watcher guard on
  reconnect. `mmb hermes install` records the absolute mmb path next to the plugin.
- Reframed the README and bundled skill to lead with `mmb hermes install` /
  `mmb openclaw install` (SSE) for receiving mail; webhooks demoted to
  advanced/optional.
- Added `mmb hermes install` — one-command install of the MonsterMailbox platform
  plugin into a Hermes agent. Writes the plugin to
  `<hermes-home>/plugins/monstermailbox/` and patches `config.yaml`
  (comment-preserving) with `plugins.enabled += monstermailbox`,
  `platform_toolsets.monstermailbox: [hermes-cli]` (so the agent's turn has a
  terminal tool — the step `hermes plugins install` omits), and
  `command_allowlist += "mmb *"`. Inbound arrives over the SSE stream; no webhook.
  Supports `--home`, `--dry-run`, `--force`; idempotent.
- Added `mmb openclaw install` — one-command install of the MonsterMailbox plugin
  into an OpenClaw agent. Writes the plugin to
  `<openclaw-home>/extensions/monstermailbox/`, links the openclaw SDK so its
  import resolves, and patches `openclaw.json` (adds the plugin to
  `plugins.load.paths` and enables `plugins.entries.monstermailbox`) while
  preserving other keys and backing up the original. The agent then receives
  inbound email over the SSE stream and replies via the `mmb` CLI — no webhook.
  Supports `--home`, `--session-key`, `--state`, `--allowed-senders`, `--mmb-bin`,
  `--dry-run`, and `--force`; idempotent on re-run.
- Fixed `mmb inbox watch` / `mmb inbox wait` silently dropping events. The
  shared HTTP client's 30s total timeout was killing the long-lived `/events`
  SSE stream every 30 seconds; combined with the server's live-only stream this
  lost any event that landed during the reconnect gap (messages stayed visible
  via `mmb inbox list` but never woke a watcher). The stream now uses a
  dedicated no-timeout client with a 45s idle watchdog, parses the SSE `id:`
  field, and sends `Last-Event-ID` on reconnect so the server replays anything
  missed. Named heartbeat events are also swallowed so they can't pollute output
  or falsely satisfy a one-shot `wait`.
- Fixed `mmb expect` for expected verification mail by accepting either a
  sender address or bare domain, deriving the canonical sender eTLD+1, and
  posting the current expectations API fields (`domain`, `expires_in`,
  `purpose`, and optional `subject_regex`). `--window` is now the documented
  duration flag, capped to the server-supported one-hour window, while `--ttl`
  remains a deprecated compatibility alias.
  Quarantine escalation now gives agents the dashboard owner-review path
  instead of a dead-end command, without exposing held body text, links, or
  attachments.
- Added staff-only webhook recovery commands under `mmb staff
  webhook-deliveries`. Operators with staff API keys can list failed/gave-up
  deliveries, inspect redacted metadata, and redrive one real delivery with
  explicit confirmation plus an idempotency key, without exposing payload bodies
  receiver response bodies, or webhook secrets in CLI output.
- Added `mmb msg attachment download <message-id> <attachment-id> --output
  <path>` for safe inbound attachment retrieval. The API still enforces
  message readability and tenant ownership; the CLI requires an explicit output
  path, refuses traversal and accidental overwrite, verifies size/SHA-256 before
  writing, and documents that email attachments are untrusted files.
- Added CLI-side owner email preflight for `mmb auth login` and `mmb register`.
  The CLI now rejects obvious placeholder/reserved domains and no-reply-style
  owner mailboxes before any registration request, so agents do not create
  inboxes or API keys with a mistaken non-human owner address.
- Added `mmb contact support` for first-class technical support questions from
  the CLI. It posts `{text, subject}` to the authenticated `/contact_support`
  API so support email routing stays server-side, emits JSON, supports stdin or
  explicit text, and honors shared mutation safety flags.
- Added `mmb contact product-feedback` as the clearer product-feedback command,
  while keeping `mmb agent-product-feedback` as a deprecated compatibility
  alias. This separates product feedback from local `mmb feedback` CLI notes
  without logging or documenting secrets.
