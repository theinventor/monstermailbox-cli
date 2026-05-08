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
)

// originalMessage is the projection of GET /msg/:id that reply commands
// consume: enough to build subject, recipient sets, and the quoted-
// original block. `to`/`cc` were added when the server started exposing
// the full participant list — reply-all needs both to derive the
// downstream cc set on the client side too (we use it for the quoted
// attribution; the server still does the authoritative recipient
// computation when reply_mode=all).
type originalMessage struct {
	From struct {
		Email       string `json:"email"`
		DisplayName string `json:"display_name"`
	} `json:"from"`
	To         []string `json:"to"`
	Cc         []string `json:"cc"`
	Bcc        []string `json:"bcc"`
	Subject    string   `json:"subject"`
	BodyText   string   `json:"body_text"`
	BodyHTML   string   `json:"body_html"`
	ReceivedAt string   `json:"received_at"`
}

// fetchOriginalMessage pulls the inbound message. 404/403 maps to a
// clear "no such message in your mailbox" error so agents don't
// accidentally send to the wrong recipient if they fat-finger the id.
// Shared by every reply command.
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

// derivedReplySubject prepends "Re: " unless the subject already starts
// with "re:" (case-insensitive, optionally followed by whitespace).
func derivedReplySubject(orig string) string {
	trimmed := strings.TrimSpace(orig)
	if len(trimmed) >= 3 && strings.EqualFold(trimmed[:3], "re:") {
		return orig
	}
	return "Re: " + orig
}

// quoteOriginal formats the original message's body as a Gmail-style
// quoted block: "On <human date>, <sender> wrote:\n> <line>\n> ...".
// Empty body produces an empty string (not a useless attribution).
func quoteOriginal(orig *originalMessage) string {
	body := strings.TrimRight(orig.BodyText, "\n")
	if body == "" {
		return ""
	}

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

// quoteOriginalHTML wraps the original body in a <blockquote> with an
// attribution line — the HTML analogue of `quoteOriginal`.
func quoteOriginalHTML(orig *originalMessage) string {
	source := strings.TrimRight(orig.BodyHTML, "\n")
	if source == "" {
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
