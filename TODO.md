# TODO: HTML email support in the CLI

The server is gaining HTML-format support (outbound + inbound). This is the
plan for the CLI side. Pin to principles 2 (bounded responses), 3 (errors
that teach), 5 (bounded defaults), 6 (vocabulary consistency), 10 (two-way I/O).

## Outbound: `new-email` and `reply-to-email`

### Flag shape (matches current `--body` convention)

```
--body <text>            # plain-text body (current behavior, default)
--body-html <html>       # HTML body — opt-in
--body-file <path>       # read --body from a file (for long bodies)
--body-html-file <path>  # read --body-html from a file
```

Either `--body` or `--body-html` is required (not both unless we want to
mirror RFC multipart/alternative — see "Open Q" below). Files exist because
HTML doesn't shell-escape comfortably.

### Wire format

Server is adding (presumably) a `body_html` field alongside `body_text`. The
CLI maps:

| Flags                               | JSON sent                          |
|-------------------------------------|------------------------------------|
| `--body "X"`                        | `{body_text: "X"}`                 |
| `--body-html "<p>X</p>"`            | `{body_html: "<p>X</p>"}`          |
| `--body "X" --body-html "<p>X</p>"` | `{body_text: "X", body_html: "X"}` |

Open Q: when both are present, is that one multipart/alternative message, or
should we reject it? Recommend **accept both** — most production senders ship
both for accessibility / spam-score reasons. If the server only takes one,
the CLI rejects with a principle-3 error: `--body and --body-html are mutually
exclusive — pick one or pass --body-file + --body-html-file for both formats`.

### `reply-to-email` auto-quoting

`quoteOriginal()` currently emits Gmail-style `> ` prefixed plain text. Add a
sibling `quoteOriginalHTML()` that emits a `<blockquote>` block:

```html
<div>On Mon, Jan 2, 2006 at 3:04 PM, Sender &lt;a@b.com&gt; wrote:</div>
<blockquote style="margin:0 0 0 0.8ex; border-left:1px solid #ccc; padding-left:1ex;">
  ...original sanitized HTML body...
</blockquote>
```

Quoting picks format from the outgoing body:
- `--body` only → quote as plain text (current behavior)
- `--body-html` only → quote as HTML
- both → quote in both

Re-uses the original message's sanitized `body_html` when the server provides
it; falls back to escaping `body_text` into `<pre>` if no HTML version exists.

### `--no-quote` keeps working unchanged.

## Inbound: `inbox list`, `msg get`, `inbox watch` / `wait`

**Default stays narrow** — body_text only, like today. HTML is opt-in because
it's bigger (often 5-10× text), most agents don't need it, and the bounded-
default principle costs tokens otherwise.

### Flag shape

```
--with-html         # include body_html in the response (opt-in)
--format text|html  # legacy alternative — only emit one of the two formats
```

`--with-html` is the recommended verb (it's additive and unambiguous).
`--format` is a fallback for agents that genuinely only want one. Validate
via `internal/enums.Validate` so a typo names the valid set.

Applies to:
- `mmb msg get <id> [--with-html]`
- `mmb inbox list [--with-html]`  (only if the server's index returns bodies
  inline; if it only returns metadata + ids today, this is a no-op)
- `mmb inbox watch [--with-html]` and `mmb inbox wait [--with-html]` —
  passes through to whatever the server emits on the SSE stream

### Wire format

The server presumably sends `body_html` as a sibling field on the Message
JSON. The CLI passes through verbatim — no HTML parsing or stripping
client-side. Server-side sanitization is the security boundary; the CLI
never re-sanitizes (would just risk corrupting the server's escapes).

Agents that pipe HTML to a downstream parser (an LLM summarizer, a layout
extractor) get the raw sanitized HTML. Agents that don't `--with-html`
keep their token budget intact.

## `mmb agent-context` surface

These additions are auto-discovered by the existing introspection walker —
no changes needed to `agent_context.go`. Verify after the flags land:

```sh
mmb agent-context | jq '.commands."new-email".flags'
# expects: --body, --body-html, --body-file, --body-html-file, --idempotency-key, --dry-run
```

Add `body_format` to `internal/enums.InContext` if `--format` lands:

```go
BodyFormats = []string{"text", "html"}
InContext["body_format"] = BodyFormats
```

## Tests to add

CLI:
- `cmd/new_email_test.go`: `--body-html` produces `body_html` JSON field
- `cmd/new_email_test.go`: `--body-file` reads from disk and propagates
- `cmd/new_email_test.go`: rejection error when both `--body` and `--body-html-file` collide (if we land mutual-exclusion)
- `cmd/reply_to_email_test.go`: HTML reply quotes with `<blockquote>`, plain-text reply quotes with `> `
- `cmd/cmd_test.go` (table): `--with-html` query param propagates on `msg get`, `inbox list`, `inbox watch/wait`
- `cmd/agent_context_test.go`: pin that `--body-html` shows up in the introspection output

Server:
- HTML body round-trips through `/send` and lands in the outbound MIME
- HTML body round-trips through `/inbox`, `/msg/:id`, `/events` when `with_html=true`
- `with_html` default is `false` — bounded response by default
- Sanitization is tested at the model layer (not the CLI's job)

## Naming review

Per principle 6 (cross-CLI vocabulary), check the broader convention:
- `--with-html` is the SDK-style "include this related field" form (matches
  Stripe `expand[]=`, etc.). ✓
- `--body-html` reads symmetrically with `--body`. ✓
- `--format text|html` is also acceptable but creates a vocabulary split
  with `--with-html` — pick one and document.

Recommendation: **ship `--with-html` only, skip `--format`**. Simpler, additive,
no enum to maintain.

## Migration / back-compat

- Existing `--body` callers keep working unchanged.
- Existing `inbox list` / `msg get` callers keep getting body_text only.
- Hidden alias for `--text` if anyone scripted around `--body` already? No —
  `--body` is the verb, no rename needed.

## Out of scope (for now)

- MIME multipart with attachments (separate principle, separate flag set).
- Markdown → HTML rendering client-side. Keep the CLI dumb; let the agent
  choose the format and ship the bytes.
- Inline image (`cid:`) handling. Server-side concern.
