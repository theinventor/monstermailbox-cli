# CLI design principles

This CLI follows **Trevin Chow's "10 Principles for Agent-Native CLIs"**.

> **Source**: [10 Principles for Agent-Native CLIs](https://trevinsays.com/p/10-principles-for-agent-native-clis) by Trevin Chow.
> Read it before designing any new command, flag, or response shape.

The principles are not optional. **Every new command, flag, and response
shape in this CLI must conform.** Reviews will reject changes that drift
from them. When the principles seem to conflict with a feature request,
write it up — usually the request is the wrong shape, but occasionally
the principle has an exception worth documenting here.

## The 10 principles, with how this CLI implements each

### Tier 1 — Don't break the agent

#### 1. Non-interactive by default
No command may hang on a TTY prompt. Destructive operations require an
explicit `--force`. Where TTY detection matters, treat non-TTY as headless.

> **Where**: `cmd/auth.go` (`auth logout --force`), every mutation command.

#### 2. Structured, parseable output
Data goes to stdout as JSON. Diagnostics go to stderr. Exit codes follow
a documented taxonomy. ANSI is suppressed when output isn't a terminal.

> **Where**: `internal/exitcode/` (codes 0–8), `cmd/inbox.go:buildInboxHint`
> (stderr-only), `cmd/auth.go` (JSON-by-default with `--human` opt-out).
> Exit codes are surfaced in `mmb agent-context`.

#### 3. Errors that teach, and enumerate
When rejecting an enum value, name the valid set. When rejecting input,
include a working example.

> **Where**: `internal/enums/Validate` produces
> `--state must be one of: trusted, quarantined, rejected (got: "secret")`.
> Add a new enum here, not inline.

#### 4. Safe retries and explicit mutation boundaries
Mutation commands accept `--idempotency-key` (sent as the
`Idempotency-Key` HTTP header; server replays the cached response on
retry). They also accept `--dry-run`, which short-circuits before the
HTTP call and emits the would-be request envelope.

> **Where**: `cmd/mutation_flags.go` is the shared helper. Every
> `POST` command must call `bindMutationFlags`.

#### 5. Bounded responses
Every list-style command needs `--limit` + a truncation hint that teaches
how to narrow. Default page sizes stay narrow.

> **Where**: `cmd/inbox.go` (`buildInboxHint` to stderr), `cmd/quarantine.go`.

### Tier 2 — Make it compound

#### 6. Cross-CLI vocabulary consistency
Use `get` (not `info`/`show`), `list` (not `ls`), `create` (not `add`),
`delete` (not `rm`/`remove`), `--force` (not `--skip-confirmations`),
`--json` (not `--format=json`), `--limit` (not `--max`), `--profile`
(not `--account`/`--context`).

> **Where**: enforced by review. v0.3 deprecated `msg show` → `msg get`,
> `whitelist add` → `whitelist create` (hidden aliases preserved one
> release).

#### 7. Three-layer introspection
- Layer 1: cobra `--help` for humans
- Layer 2: `mmb agent-context` — versioned machine-readable JSON of
  every command, flag, enum, exit code, available profile, and
  endpoint
- Layer 3: long-form workflow prose (this directory)

The schema version on `mmb agent-context` is bumped on
backwards-incompatible shape changes; agents pin against it.

> **Where**: `cmd/agent_context.go`. The walker is automatic — every
> new flag shows up for free.

#### 8. Async-aware execution
Async commands need a `--wait` shape. Long-lived streams need
reconnect-with-backoff and a one-shot sibling for agents that want
"block for one event" semantics.

> **Where**: `cmd/inbox.go`: `inbox watch` reconnects with backoff +
> jitter; `inbox wait [--timeout]` is the one-shot block-for-next-event
> shape.

#### 9. Persistent identity through profiles
Save/use/list/show/delete subcommands; `--profile` as a persistent
flag; precedence: explicit flag > env var > config default.

> **Where**: `cmd/auth.go`, `internal/config/`. Available profiles
> surface via `mmb agent-context.available_profiles`; the root
> `--profile` flag surfaces via `mmb agent-context.global_flags`.

#### 10. Two-way I/O
- Output sinks: `--deliver=stdout|file:|webhook:` (TODO; see TODO.md).
- Feedback channel: `mmb feedback "<text>"` writes locally and
  optionally POSTs to `MONSTERMAILBOX_FEEDBACK_ENDPOINT`. Agents
  discover whether the upstream channel exists via
  `mmb agent-context.endpoints.feedback_upstream`.

> **Where**: `cmd/feedback.go`. `--deliver` lands when an artifact-
> producing command exists (no current commands return blob-shaped
> output).

## Adding a new command

Read the principles above first. Then:

1. **Verb**: `get` / `list` / `create` / `update` / `delete`. If you
   need a new verb, justify it.
2. **Flags**: `--json` is implicit (everything emits JSON). Any
   enum-shaped flag goes in `internal/enums` so the rejection error
   names the valid set AND `agent-context` enumerates it. Mutation
   commands MUST use `bindMutationFlags` so they get
   `--idempotency-key` and `--dry-run` for free.
3. **Errors**: wrap with `exitcode.Wrap(<code>, err)`. Don't return
   bare `fmt.Errorf` — that always exits 1, killing the taxonomy.
4. **Response**: structured JSON to stdout. Hints to stderr only.
   Bounded by default — add `--limit` and a truncation hint if the
   response is list-shaped.
5. **Tests**: at least one `cmd/*_test.go` test that runs the command
   end-to-end against an `httptest.NewServer` and asserts the request
   shape (method, path, headers, body) matches the OpenAPI contract.

## When the article gets revised

The author has explicitly noted the principles will keep evolving.
When the canonical post is revised, update this doc to reflect the
new state — and update the implementations to match.

## Credits

The 10 principles in this document are by Trevin Chow. Read the full
article (with examples and the full rationale for each principle) at:

  https://trevinsays.com/p/10-principles-for-agent-native-clis

This file is a project-internal summary plus implementation references.
The principles themselves are Trevin's.
