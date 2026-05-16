package cmd

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/spf13/cobra"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
)

const supportIntakeAddress = "support@monstermailbox.com"

func newContactCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "contact",
		Short: "Contact MonsterMailbox support or product maintainers",
		Long: `Submit first-party MonsterMailbox contact requests from the CLI.

Use 'mmb contact support' for technical support questions about your
account, delivery, API behavior, or operational issues.

Use 'mmb contact product-feedback' for product ideas, rough edges, and
feature requests about MonsterMailbox itself.

Use 'mmb feedback' only for local notes about the CLI tool; it writes a
local JSONL ledger and may not reach the MonsterMailbox product team.`,
	}
	c.AddCommand(newContactSupportCmd())
	c.AddCommand(newContactProductFeedbackCmd())
	return c
}

func newContactSupportCmd() *cobra.Command {
	var subject, text string
	var mf mutationFlags
	c := &cobra.Command{
		Use:   "support [text]",
		Short: "Send a technical support question to MonsterMailbox support",
		Long: `Send a technical support question through the authenticated support
intake path.

Use this for account, delivery, API behavior, operational, or technical
questions. Use 'mmb contact product-feedback' for product ideas and
feature requests. Use 'mmb feedback' for local CLI-maintainer notes.

Three message input forms (pick exactly one):
  1. Positional:  mmb contact support --subject "webhook retries" "what happened to delivery X?"
  2. Flag:        mmb contact support --subject "webhook retries" --text "what ..."
  3. Stdin:       echo "what ..." | mmb contact support --subject "webhook retries" -

The command sends a new support thread. The authenticated agent identity
is attached by the API request; do not include secrets in the message.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			subject = strings.TrimSpace(subject)
			if subject == "" {
				return exitcode.Wrap(exitcode.Usage,
					fmt.Errorf("--subject is required; example: mmb contact support --subject \"webhook retries\" \"what happened to delivery X?\""))
			}
			body, err := resolveSingleTextInput("support message text", args, text, cmd.InOrStdin())
			if err != nil {
				return exitcode.Wrap(exitcode.Usage, err)
			}

			payload := supportPayload(subject, body)
			if mf.DryRun {
				return printJSON(cmd.OutOrStdout(),
					newDryRunEnvelope(http.MethodPost, "/send", payload, mf))
			}

			cli := newAPIClient()
			resp, err := cli.DoWithHeaders(http.MethodPost, "/send", payload, nil, mf.Headers())
			if err != nil {
				return fmt.Errorf("POST /send: %w", err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	c.Flags().StringVar(&subject, "subject", "", "support request subject — required")
	c.Flags().StringVar(&text, "text", "", "support message body (alternative to positional arg or stdin)")
	bindMutationFlags(c, &mf)
	return c
}

func supportPayload(subject, body string) map[string]any {
	return map[string]any{
		"to":        supportIntakeAddress,
		"subject":   subject,
		"body_text": body,
	}
}
