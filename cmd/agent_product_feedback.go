package cmd

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
)

// `mmb agent-product-feedback "<text>"` →
// POST /agent_product_feedback.
//
// Sends free-text feedback about the monstermailbox product upstream
// to the maintainers. Distinct from `mmb feedback`, which is a local
// CLI feedback log that may or may not POST to a separately-configured
// webhook. This verb hits a server-side endpoint that delivers the
// note directly to the project's maintainers.
//
// Input forms (in priority order):
//  1. Positional arg:    mmb agent-product-feedback "the policy editor is great"
//  2. --text flag:       mmb agent-product-feedback --text="..."
//  3. stdin via "-":     echo "..." | mmb agent-product-feedback -
//
// At least one must be supplied; mixed forms are rejected so the
// agent never wonders which one won.
//
// The endpoint deliberately does NOT expose the upstream destination,
// so a successful response just confirms "feedback received." Use
// --dry-run to inspect the request shape without firing it.
func newAgentProductFeedbackCmd() *cobra.Command {
	var text string
	var mf mutationFlags
	c := &cobra.Command{
		Use:   "agent-product-feedback [text]",
		Short: "Send product feedback to the monstermailbox maintainers",
		Long: `Send product feedback to the monstermailbox maintainers via the
agent-side server endpoint. Distinct from 'mmb feedback' (local CLI
feedback log).

Three input forms (pick exactly one):
  1. Positional:  mmb agent-product-feedback "the policy editor is great"
  2. Flag:        mmb agent-product-feedback --text "the ..."
  3. Stdin:       echo "the ..." | mmb agent-product-feedback -

A successful submission returns:
  {"received": true, "message": "feedback received"}

The server's upstream destination is intentionally not exposed in the
response. Body is capped at 4 KB by the server.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := resolveFeedbackText(args, text, cmd.InOrStdin())
			if err != nil {
				return exitcode.Wrap(exitcode.Usage, err)
			}

			payload := map[string]any{"text": body}

			if mf.DryRun {
				return printJSON(cmd.OutOrStdout(),
					newDryRunEnvelope(http.MethodPost, "/agent_product_feedback", payload, mf))
			}

			cli := newAPIClient()
			resp, err := cli.DoWithHeaders(http.MethodPost, "/agent_product_feedback", payload, nil, mf.Headers())
			if err != nil {
				return fmt.Errorf("POST /agent_product_feedback: %w", err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	c.Flags().StringVar(&text, "text", "", "feedback body (alternative to positional arg or stdin)")
	bindMutationFlags(c, &mf)
	return c
}

// resolveFeedbackText collects the feedback body from exactly ONE of
// (positional arg, --text flag, stdin via "-"). Multiple sources or
// missing input both return an error so the agent never wonders which
// one won. stdin is only consulted when the positional is the literal
// "-" (matches `kubectl apply -f -` convention).
func resolveFeedbackText(args []string, textFlag string, stdin io.Reader) (string, error) {
	positional := ""
	stdinRequested := false
	if len(args) == 1 {
		if args[0] == "-" {
			stdinRequested = true
		} else {
			positional = args[0]
		}
	}

	provided := 0
	if positional != "" {
		provided++
	}
	if textFlag != "" {
		provided++
	}
	if stdinRequested {
		provided++
	}

	if provided == 0 {
		return "", fmt.Errorf("feedback text is required (pass as positional arg, --text, or pipe via '-')")
	}
	if provided > 1 {
		return "", fmt.Errorf("pick exactly one input form: positional, --text, or stdin via '-'")
	}

	switch {
	case stdinRequested:
		raw, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("read stdin: %w", err)
		}
		body := strings.TrimSpace(string(raw))
		if body == "" {
			return "", fmt.Errorf("stdin was empty; nothing to submit")
		}
		return body, nil
	case textFlag != "":
		return textFlag, nil
	default:
		return positional, nil
	}
}

// readStdinIfAttached is exposed for tests that need to short-circuit
// stdin handling. Unused in production paths — production runs go
// through resolveFeedbackText directly.
var _ = bufio.NewReader
var _ = os.Stdin
