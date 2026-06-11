# Changelog

## Unreleased

- Fixed `mmb expect` for expected verification mail by accepting either a
  sender address or bare domain, deriving the canonical sender eTLD+1, and
  posting the current expectations API fields (`domain`, `expires_in`,
  `purpose`, and optional `subject_regex`). `--window` is now the documented
  duration flag while `--ttl` remains a deprecated compatibility alias.
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
