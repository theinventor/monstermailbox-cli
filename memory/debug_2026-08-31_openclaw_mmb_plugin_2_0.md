# OpenClaw 2.0 MonsterMailbox Plugin Recovery - 2026-08-31

## Symptom

On the Mac Mini OpenClaw 2.0 install, the MonsterMailbox plugin loaded and had a live `mmb inbox wait` child, but pending trusted inbox messages were not being claimed or dispositioned by Claudito.

## Root Cause

Two issues combined:

- OpenClaw 2.0 package discovery expects a native package directory with `openclaw` package metadata and a strict manifest. The old install path could leave a legacy `.../monstermailbox/index.js` load path and did not declare runtime entries.
- The OpenClaw launchd gateway environment contained `MONSTERMAILBOX_API_KEY`, so bare `mmb` subprocesses used that env credential instead of the intended saved `life` profile. That identity had no pending inbox messages, while `mmb --profile life inbox list --work-state inbox --state trusted --peek` saw the pending mail.

The earlier long-lived `mmb inbox watch` design was also fragile because a stalled child could leave mail sitting indefinitely.

## Fix

- Added OpenClaw 2.0 package metadata (`openclaw.extensions`, `openclaw.runtimeExtensions`, plugin identity, compat).
- Normalized installer `plugins.load.paths` to the package directory and removed legacy `index.js` entries.
- Switched the OpenClaw plugin from long-lived `mmb inbox watch` to bounded `mmb inbox wait --timeout 120s` plus immediate/hourly reconcile.
- Made plugin subprocesses use an absolute `mmbBin` and optional `mmbProfile`; when `mmbProfile` is set, the plugin deletes inherited `MONSTERMAILBOX_API_KEY` before spawning `mmb`.
- Changed the default dispatch session key to `agent:main:subagent:monstermailbox`.
- Strengthened the handoff prompt so Claudito explicitly claims, reads, replies if needed, and dispositions each message.

## Verification

- Local: `node --check cmd/embedded/plugins/openclaw/index.js`
- Local: `go test ./cmd -run 'OpenClaw|Backstop|Inbox|DetectDefaultMMBProfile'`
- Local: `go test ./...`
- Local: `git diff --check`
- Live Mac Mini:
  - Gateway ready on OpenClaw 2026.8.1.
  - Plugin config pinned `mmbBin` to `/Users/troyfam/.local/bin/mmb` and `mmbProfile` to `life`.
  - Previously stuck synthetic messages `1695` and `1696` moved from `inbox` to `done`.
  - Fresh synthetic message `1697` was reconciled, dispatched to `agent:main:subagent:monstermailbox`, claimed, peek-read, and marked `done`.
  - OpenAI model runs still used OpenClaw Codex runtime; gateway env did not contain `OPENAI_API_KEY` or `CODEX_API_KEY`.

## Status

DONE.
