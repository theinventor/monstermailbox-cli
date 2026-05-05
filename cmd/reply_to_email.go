package cmd

import (
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/theinventor/monstermailbox-cli/internal/client"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
	"github.com/spf13/cobra"
)

// `mmb reply-to-email --to-message-id <id> --body <s>` → POST /send.
//
// Threading is automatic and not opt-out: the CLI fetches the original
// inbound message, derives the recipient + Re:-prefixed subject, then
// POSTs to /send with `in_reply_to_message_id` set so the backend can
// stitch RFC In-Reply-To / References headers on the outbound MIME.
//
// Body forms (at least one required) match `mmb new-email`:
// --body / --body-html / --body-file / --body-html-file. When HTML is
// present the auto-quoted block uses <blockquote>; when only plain
// text is present the quote is Gmail-style "> " prefixed. With both,
// both are quoted so the multipart alternatives stay in sync.
func newReplyToEmailCmd() *cobra.Command {
	var toMessageID, subjectOverride string
	var cc, bcc []string
	var noQuote bool
	var mf mutationFlags
	var bf bodyFlags
	c := &cobra.Command{
		Use:   "reply-to-email",
		Short: "Reply to an inbound message (threading is automatic)",
		Long: `Reply to an inbound message. Threading is automatic — the CLI fetches
the original, derives the recipient + Re:-prefixed subject, sets
in_reply_to_message_id so the backend stitches RFC In-Reply-To /
References headers on the outbound MIME, AND quotes the original
message body Gmail-style so the recipient sees context the way every
email client renders it.

Plain-text replies quote with "> " prefixed lines.
HTML replies quote with a <blockquote> block.
With both bodies, both are quoted (multipart alternatives stay in sync).

Pass --no-quote to send a clean body with no quoted history.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if toMessageID == "" {
				return exitcode.Wrap(exitcode.Usage,
					fmt.Errorf("--to-message-id is required"))
			}
			text, htmlBody, err := bf.resolve()
			if err != nil {
				return err
			}

			cli := client.New()
			orig, err := fetchOriginalMessage(cli, toMessageID)
			if err != nil {
				return err
			}

			subject := subjectOverride
			if subject == "" {
				subject = derivedReplySubject(orig.Subject)
			}

			outText := text
			outHTML := htmlBody
			if !noQuote {
				if outText != "" {
					outText = outText + "\n\n" + quoteOriginal(orig)
				}
				if outHTML != "" {
					outHTML = outHTML + "\n" + quoteOriginalHTML(orig)
				}
			}

			payload := map[string]any{
				"to":                     orig.From.Email,
				"subject":                subject,
				"in_reply_to_message_id": toMessageID,
			}
			if outText != "" {
				payload["body_text"] = outText
			}
			if outHTML != "" {
				payload["body_html"] = outHTML
			}
			if len(cc) > 0 {
				payload["cc"] = cc
			}
			if len(bcc) > 0 {
				payload["bcc"] = bcc
			}

			if mf.DryRun {
				return printJSON(cmd.OutOrStdout(),
					newDryRunEnvelope(http.MethodPost, "/send", payload, mf))
			}

			resp, err := cli.DoWithHeaders(http.MethodPost, "/send", payload, nil, mf.Headers())
			if err != nil {
				return fmt.Errorf("POST /send: %w", err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	c.Flags().StringVar(&toMessageID, "to-message-id", "", "id of the inbound Message to reply to — required")
	c.Flags().StringVar(&subjectOverride, "subject-override", "", "replace the derived 'Re: <orig>' subject (rare)")
	c.Flags().StringSliceVar(&cc, "cc", nil, "cc recipients (comma-separated or repeat the flag)")
	c.Flags().StringSliceVar(&bcc, "bcc", nil, "bcc recipients (comma-separated or repeat the flag)")
	c.Flags().BoolVar(&noQuote, "no-quote", false, "send body alone without quoting the original message")
	bindBodyFlags(c, &bf)
	bindMutationFlags(c, &mf)
	return c
}

// originalMessage is the projection of GET /msg/:id that the reply
// command needs: enough fields to build the To, the subject, AND the
// quoted-original block.
//
// `body_html` is read for the HTML-quote path. When the inbound
// schema doesn't (yet) carry it, the field stays empty and the HTML
// quote falls back to escaping `body_text` into a <blockquote>. See
// TODO.md for the inbound HTML rollout.
type originalMessage struct {
	From struct {
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
	} `json:"from"`
	Subject    string `json:"subject"`
	BodyText   string `json:"body_text"`
	BodyHTML   string `json:"body_html"`
	ReceivedAt string `json:"received_at"`
}

// fetchOriginalMessage pulls the inbound message. 404/403 maps to a
// clear "no such message in your mailbox" error so agents don't
// accidentally send to the wrong recipient if they fat-finger the id.
func fetchOriginalMessage(cli *client.Client, id string) (*originalMessage, error) {
	resp, err := cli.Do(http.MethodGet, "/msg/"+id, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("GET /msg/%s: %w", id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusForbidden {
		return nil, exitcode.Wrap(exitcode.NotFound,
			fmt.Errorf("no such message in your mailbox: %s", id))
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		hint := strings.TrimSpace(string(raw))
		if hint == "" {
			// Empty 4xx/5xx → almost always wrong-host. Surface the URL
			// so the user/agent can spot a misconfigured profile or env
			// var. Same invariant `passthroughJSON` enforces; restated
			// here because reply-to-email handles the response itself.
			hint = fmt.Sprintf("(empty body — check %s is the right API URL)", resp.Request.URL.String())
		}
		return nil, exitcode.Wrap(exitcode.FromHTTPStatus(resp.StatusCode),
			fmt.Errorf("HTTP %d %s: %s", resp.StatusCode, http.StatusText(resp.StatusCode), hint))
	}

	var parsed originalMessage
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode /msg/%s: %w", id, err)
	}
	if parsed.From.Email == "" {
		return nil, fmt.Errorf("message %s has no from.email; cannot derive recipient", id)
	}
	return &parsed, nil
}

// quoteOriginal formats the original message's body as a Gmail-style
// quoted block: "On <human date>, <sender> wrote:\n> <line>\n> ...".
// Empty body produces an empty string (not a useless attribution).
func quoteOriginal(orig *originalMessage) string {
	body := strings.TrimRight(orig.BodyText, "\n")
	if body == "" {
		return ""
	}

	// Gmail-style date: "Sun, May 3, 2026 at 4:01 PM" — local time of
	// the running CLI. We parse the server's RFC3339 received_at and
	// reformat; if parsing fails, omit the timestamp rather than
	// printing a misleading raw string.
	var datePart string
	if t, err := time.Parse(time.RFC3339, orig.ReceivedAt); err == nil {
		datePart = t.Local().Format("Mon, Jan 2, 2006 at 3:04 PM")
	}

	sender := orig.From.DisplayName
	if sender == "" {
		sender = orig.From.Email
	} else {
		sender = fmt.Sprintf("%s <%s>", sender, orig.From.Email)
	}

	var attribution string
	if datePart != "" {
		attribution = fmt.Sprintf("On %s, %s wrote:", datePart, sender)
	} else {
		attribution = fmt.Sprintf("%s wrote:", sender)
	}

	// Prefix every line of the original with "> ", preserving paragraph
	// breaks. Gmail's convention: "> " for replied content, "> > " for
	// content that was already quoted by them, etc. We don't try to
	// re-collapse — just one level of "> ".
	lines := strings.Split(body, "\n")
	for i, l := range lines {
		if l == "" {
			lines[i] = ">"
		} else {
			lines[i] = "> " + l
		}
	}

	return attribution + "\n" + strings.Join(lines, "\n")
}

// derivedReplySubject prepends "Re: " unless the subject already starts
// with "re:" (case-insensitive, optionally followed by whitespace).
func derivedReplySubject(orig string) string {
	trimmed := strings.TrimSpace(orig)
	if len(trimmed) >= 3 && strings.EqualFold(trimmed[:3], "re:") {
		return orig
	}
	return "Re: " + orig
}

// quoteOriginalHTML wraps the original body in a <blockquote> with an
// attribution line — the HTML analogue of `quoteOriginal`. The server
// sanitizes both inbound HTML (eventually) and outbound HTML before
// shipping to Postmark, so the CLI does NOT re-sanitize: it passes
// through the server's sanitized body_html, or HTML-escapes body_text
// as a safe fallback when no body_html is available.
//
// Output shape (Gmail-compatible):
//
//	<div>On Mon, May 5, 2026 at 1:34 PM, Sender &lt;a@b.com&gt; wrote:</div>
//	<blockquote style="margin:0 0 0 0.8ex; border-left:1px solid #ccc; padding-left:1ex;">
//	  ...original body...
//	</blockquote>
//
// Empty input → empty string (no useless attribution). Same contract
// as quoteOriginal so callers can compose `body + "\n" + quote(orig)`
// without nil-checking.
func quoteOriginalHTML(orig *originalMessage) string {
	source := strings.TrimRight(orig.BodyHTML, "\n")
	if source == "" {
		// No HTML version on the inbound — escape the plain text and
		// preserve newlines. This keeps the HTML reply visually correct
		// even when the inbound was text-only or pre-rollout.
		text := strings.TrimRight(orig.BodyText, "\n")
		if text == "" {
			return ""
		}
		escaped := html.EscapeString(text)
		escaped = strings.ReplaceAll(escaped, "\n", "<br>\n")
		source = escaped
	}

	var datePart string
	if t, err := time.Parse(time.RFC3339, orig.ReceivedAt); err == nil {
		datePart = t.Local().Format("Mon, Jan 2, 2006 at 3:04 PM")
	}

	senderText := orig.From.DisplayName
	if senderText == "" {
		senderText = orig.From.Email
	} else {
		senderText = fmt.Sprintf("%s <%s>", senderText, orig.From.Email)
	}
	senderHTML := html.EscapeString(senderText)

	var attribution string
	if datePart != "" {
		attribution = fmt.Sprintf("<div>On %s, %s wrote:</div>",
			html.EscapeString(datePart), senderHTML)
	} else {
		attribution = fmt.Sprintf("<div>%s wrote:</div>", senderHTML)
	}

	return attribution +
		`<blockquote style="margin:0 0 0 0.8ex; border-left:1px solid #ccc; padding-left:1ex;">` +
		"\n" + source + "\n</blockquote>"
}
