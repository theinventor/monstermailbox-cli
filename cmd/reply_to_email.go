package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/theinventor/monstermailbox-cli/internal/client"
	"github.com/spf13/cobra"
)

// `mmb reply-to-email --to-message-id <id> --body <s> [--cc] [--bcc] [--subject-override <s>]` → POST /send.
//
// Threading is automatic and not opt-out: the CLI fetches the original
// inbound message, derives the recipient + Re:-prefixed subject, then
// POSTs to /send with `in_reply_to_message_id` set so the backend can
// stitch RFC In-Reply-To / References headers on the outbound MIME.
func newReplyToEmailCmd() *cobra.Command {
	var toMessageID, body, subjectOverride string
	var cc, bcc []string
	var noQuote bool
	c := &cobra.Command{
		Use:   "reply-to-email",
		Short: "Reply to an inbound message (threading is automatic)",
		Long: `Reply to an inbound message. Threading is automatic — the CLI fetches
the original, derives the recipient + Re:-prefixed subject, sets
in_reply_to_message_id so the backend stitches RFC In-Reply-To /
References headers on the outbound MIME, AND quotes the original
message body Gmail-style ("On <date>, <sender> wrote:\n> ...") so
the recipient sees context the way every email client renders it.

Pass --no-quote to send a clean body with no quoted history.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if toMessageID == "" || body == "" {
				return fmt.Errorf("--to-message-id and --body are both required")
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

			outBody := body
			if !noQuote {
				outBody = body + "\n\n" + quoteOriginal(orig)
			}

			payload := map[string]any{
				"to":                     orig.From.Email,
				"subject":                subject,
				"body_text":              outBody,
				"in_reply_to_message_id": toMessageID,
			}
			if len(cc) > 0 {
				payload["cc"] = cc
			}
			if len(bcc) > 0 {
				payload["bcc"] = bcc
			}

			resp, err := cli.Do(http.MethodPost, "/send", payload, nil)
			if err != nil {
				return fmt.Errorf("POST /send: %w", err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	c.Flags().StringVar(&toMessageID, "to-message-id", "", "id of the inbound Message to reply to — required")
	c.Flags().StringVar(&body, "body", "", "plain-text body — required")
	c.Flags().StringVar(&subjectOverride, "subject-override", "", "replace the derived 'Re: <orig>' subject (rare)")
	c.Flags().StringSliceVar(&cc, "cc", nil, "cc recipients (comma-separated or repeat the flag)")
	c.Flags().StringSliceVar(&bcc, "bcc", nil, "bcc recipients (comma-separated or repeat the flag)")
	c.Flags().BoolVar(&noQuote, "no-quote", false, "send body alone without quoting the original message")
	return c
}

// originalMessage is the projection of GET /msg/:id that the reply
// command needs: enough fields to build the To, the subject, AND the
// quoted-original block.
type originalMessage struct {
	From struct {
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
	} `json:"from"`
	Subject    string `json:"subject"`
	BodyText   string `json:"body_text"`
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
		return nil, fmt.Errorf("no such message in your mailbox: %s", id)
	}
	if resp.StatusCode >= 400 {
		raw, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("GET /msg/%s: %s: %s", id, resp.Status, strings.TrimSpace(string(raw)))
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
