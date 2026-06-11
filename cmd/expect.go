package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"net/mail"
	"regexp"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
	"golang.org/x/net/publicsuffix"
)

// `mmb expect --from <email-or-domain> [--subject-regex <pattern>] [--window <duration>]`
// → POST /expectations.
//
// Expectations are pre-arranged trust grants — the agent declares
// ahead of time "I'm expecting verification mail from mapillary.com";
// matching inbound can be delivered trusted/readable when the server's
// scanner and auth gates allow it.
func newExpectCmd() *cobra.Command {
	var from, subjectRegex, subjectAlias, purpose, window, ttl string
	var mf mutationFlags
	c := &cobra.Command{
		Use:   "expect",
		Short: "Pre-arrange a trust grant for an expected inbound message",
		RunE: func(cmd *cobra.Command, _ []string) error {
			body, err := expectBody(from, subjectRegex, subjectAlias, purpose, window, ttl)
			if err != nil {
				return err
			}

			if mf.DryRun {
				return printJSON(cmd.OutOrStdout(),
					newDryRunEnvelope(http.MethodPost, "/expectations", body, mf))
			}

			cli := newAPIClient()
			resp, err := cli.DoWithHeaders(http.MethodPost, "/expectations", body, nil, mf.Headers())
			if err != nil {
				return fmt.Errorf("POST /expectations: %w", err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	c.Flags().StringVar(&from, "from", "", "expected sender (email or domain) — required")
	c.Flags().StringVar(&subjectRegex, "subject-regex", "", "optional regular expression that narrows the expected subject")
	c.Flags().StringVar(&subjectAlias, "subject", "", "deprecated alias for --subject-regex")
	_ = c.Flags().MarkDeprecated("subject", "use --subject-regex instead")
	c.Flags().StringVar(&purpose, "purpose", "verification", "short reason for the expectation")
	c.Flags().StringVar(&window, "window", "", "expectation lifetime up to 1h (e.g. 30m, 1h) — server-default if omitted")
	c.Flags().StringVar(&ttl, "ttl", "", "deprecated alias for --window")
	_ = c.Flags().MarkDeprecated("ttl", "use --window instead")
	bindMutationFlags(c, &mf)
	return c
}

var expectDurationRe = regexp.MustCompile(`^([1-9][0-9]*)(m|h)$`)

func expectBody(from, subjectRegex, subjectAlias, purpose, window, ttl string) (map[string]any, error) {
	domain, err := canonicalExpectationDomain(from)
	if err != nil {
		return nil, exitcode.Wrap(exitcode.Usage, err)
	}
	if subjectRegex != "" && subjectAlias != "" {
		return nil, exitcode.Wrap(exitcode.Usage,
			errors.New("expect accepts only one subject matcher: --subject-regex or deprecated --subject"))
	}
	if window != "" && ttl != "" {
		return nil, exitcode.Wrap(exitcode.Usage,
			errors.New("expect accepts only one duration: --window or deprecated --ttl"))
	}
	duration := window
	if duration == "" {
		duration = ttl
	}
	if duration != "" && !expectDurationSupported(duration) {
		return nil, exitcode.Wrap(exitcode.Usage,
			fmt.Errorf("unsupported expectation duration %q; use 1m through 60m, or 1h", duration))
	}

	body := map[string]any{"domain": domain}
	if subjectRegex == "" {
		subjectRegex = subjectAlias
	}
	if subjectRegex != "" {
		body["subject_regex"] = subjectRegex
	}
	if strings.TrimSpace(purpose) != "" {
		body["purpose"] = strings.TrimSpace(purpose)
	}
	if duration != "" {
		body["expires_in"] = duration
	}
	return body, nil
}

func expectDurationSupported(duration string) bool {
	matches := expectDurationRe.FindStringSubmatch(duration)
	if matches == nil {
		return false
	}
	n, err := strconv.Atoi(matches[1])
	if err != nil {
		return false
	}
	switch matches[2] {
	case "m":
		return n <= 60
	case "h":
		return n == 1
	default:
		return false
	}
}

func canonicalExpectationDomain(input string) (string, error) {
	raw := strings.TrimSpace(input)
	if raw == "" {
		return "", fmt.Errorf("--from is required (the sender email or domain you expect)")
	}

	host := raw
	if strings.Contains(raw, "@") {
		addr, err := mail.ParseAddress(raw)
		if err != nil {
			return "", fmt.Errorf("--from must be a sender email or bare domain (got %q)", input)
		}
		parts := strings.Split(addr.Address, "@")
		if len(parts) != 2 || parts[1] == "" {
			return "", fmt.Errorf("--from must include a domain (got %q)", input)
		}
		host = parts[1]
	}

	host = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(host)), ".")
	if strings.ContainsAny(host, "/:\\") || strings.Contains(host, " ") || !strings.Contains(host, ".") {
		return "", fmt.Errorf("--from must be a bare domain or sender email domain, not %q", input)
	}

	domain, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return "", fmt.Errorf("--from domain %q is not supported: %w", host, err)
	}
	return domain, nil
}
