# TODO

Forward-looking work that didn't fit a current PR.

## Inbound HTML body — when the server adds it

Server PR #52 added HTML support to **outbound only** (POST /send accepts
`body_html`). The Message schema (GET /inbox, /msg/:id, SSE events) is still
text-only as of this writing. When the server adds inbound HTML:

### Default stays narrow (principle 5)

Body responses default to `body_text` only — HTML is bigger (often 5–10×),
most agents don't need it, and the bounded-default principle costs tokens
otherwise. Add an opt-in flag.

### Flag shape

```
--with-html         # opt-in: include body_html in the response
```

Applies to:
- `mmb msg get <id> [--with-html]`
- `mmb inbox list [--with-html]` (only if the index returns bodies inline;
  if it returns metadata + ids, `--with-html` is a no-op there)
- `mmb inbox watch [--with-html]` and `mmb inbox wait [--with-html]` —
  passes through to whatever the server emits on the SSE stream

`--with-html` is the SDK-style "include this related field" verb (matches
Stripe `expand[]=`, etc.). Skip a `--format text|html` alternative — it
creates a vocabulary split and an enum to maintain.

### Wire format

Server presumably adds `body_html` as a sibling field on the Message
JSON. CLI passes through verbatim — no HTML parsing or stripping client-
side. Server-side sanitization is the security boundary; the CLI never
re-sanitizes (would risk corrupting the server's escapes).

### Reply-to-email pickup

`reply_to_email.go:fetchOriginalMessage` already reads `body_html` off the
Message projection — the field exists but is empty pre-rollout. Once the
server starts populating it, HTML replies automatically quote the original's
sanitized HTML instead of falling back to escaped text. No CLI change
needed when the server flips the switch (well-tested fallback path is
already covered).

### Tests to add

- `cmd/cmd_test.go`: `--with-html` adds `with_html=true` to the query
  string on `msg get`, `inbox list`, `inbox watch/wait`
- `cmd/agent_context_test.go`: pin that `--with-html` shows up in the
  introspection output for each command

### Naming review

Per principle 6 (cross-CLI vocabulary), `--with-html` is the right verb.
If the server ever accepts a list of fields to expand (`expand=body_html,attachments`),
revisit and consider migrating to `--expand body_html` style.

## --deliver sinks (principle 10, second half)

The article's principle 10 also covers `--deliver=stdout|file:|webhook:`
for routing artifacts. mmb has no artifact-shaped commands today
(everything is JSON-on-stdout). Land this when the first artifact
command appears (e.g. a future `mmb attachment download` would be a
natural fit).

## Out of scope

- MIME multipart with attachments (separate principle, separate flag set).
- Markdown → HTML rendering client-side. Keep the CLI dumb; let the agent
  choose the format and ship the bytes.
- Inline image (`cid:`) handling. Server-side concern.
