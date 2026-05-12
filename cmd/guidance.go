package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/spf13/cobra"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
)

// `mmb guidance` — per-agent guidance entries. When a matching email
// arrives, the matched entries ride along on the inbox JSON as
// `matched_guidance` so the agent literally cannot miss them.
//
// Verbs (Trevin principle 6): list / get / create / update / delete.
// Matchers are strict-AND across whatever fields you set. At least one
// matcher must be set; an entry with no matchers would shadow every
// email and the server rejects it.
//
// Typical capture flow: when an agent isn't sure what to do with a
// message, it IMs the human, the human writes a procedure, then the
// agent calls `mmb guidance create` to persist the rule for next
// time. See docs/CLI_DESIGN_PRINCIPLES.md.
func newGuidanceCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "guidance",
		Short: "Per-agent guidance entries (matched ones ride along on inbound messages)",
	}
	c.AddCommand(newGuidanceListCmd())
	c.AddCommand(newGuidanceGetCmd())
	c.AddCommand(newGuidanceCreateCmd())
	c.AddCommand(newGuidanceUpdateCmd())
	c.AddCommand(newGuidanceDeleteCmd())
	return c
}

func newGuidanceListCmd() *cobra.Command {
	var limit int
	c := &cobra.Command{
		Use:   "list",
		Short: "List this agent's guidance entries",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cli := newAPIClient()
			q := url.Values{}
			if limit > 0 {
				q.Set("limit", fmt.Sprintf("%d", limit))
			}
			resp, err := cli.Do(http.MethodGet, "/guidance", nil, q)
			if err != nil {
				return fmt.Errorf("GET /guidance: %w", err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	c.Flags().IntVar(&limit, "limit", 0, "max rows (0 = server default)")
	return c
}

func newGuidanceGetCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "get <id>",
		Short: "Get one guidance entry by id",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cli := newAPIClient()
			resp, err := cli.Do(http.MethodGet, "/guidance/"+args[0], nil, nil)
			if err != nil {
				return fmt.Errorf("GET /guidance/%s: %w", args[0], err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	return c
}

// guidanceMutationFlags holds every matcher + content flag the
// create/update commands share. Pulled out so the two commands can't
// drift on flag names — the schema lives here.
type guidanceMutationFlags struct {
	Name         string
	Instructions string
	Enabled      bool
	enabledSet   bool
	FromEmail    string
	FromDomain   string
	FromRegex    string
	SubjectRegex string
	BodyContains []string
	BodyRegex    string
}

// bindGuidanceFlags wires every guidance content flag onto a cobra
// command. Same shape for create and update; difference is which
// flags are REQUIRED (create requires --name + --instructions + at
// least one matcher; update treats all of them as optional patches).
func bindGuidanceFlags(c *cobra.Command, gf *guidanceMutationFlags) {
	c.Flags().StringVar(&gf.Name, "name", "", "short label for this guidance (e.g. ci-failures)")
	c.Flags().StringVar(&gf.Instructions, "instructions", "", "what the agent should do for matching messages")
	c.Flags().BoolVar(&gf.Enabled, "enabled", true, "whether this guidance is active")
	c.Flags().StringVar(&gf.FromEmail, "from-email", "", "matcher: exact sender email (case-insensitive)")
	c.Flags().StringVar(&gf.FromDomain, "from-domain", "", "matcher: sender domain eTLD+1 (case-insensitive)")
	c.Flags().StringVar(&gf.FromRegex, "from-regex", "", "matcher: regex over the From: header")
	c.Flags().StringVar(&gf.SubjectRegex, "subject-regex", "", "matcher: regex over the subject")
	c.Flags().StringSliceVar(&gf.BodyContains, "body-contains", nil, "matcher: ALL substrings must appear in the body (repeat or comma-separate)")
	c.Flags().StringVar(&gf.BodyRegex, "body-regex", "", "matcher: regex over the body text")
}

func (gf *guidanceMutationFlags) markFlagsBeforeRun(cmd *cobra.Command) {
	gf.enabledSet = cmd.Flags().Changed("enabled")
}

func (gf *guidanceMutationFlags) buildBody(includeAll bool) map[string]any {
	body := map[string]any{}
	if gf.Name != "" || includeAll {
		if gf.Name != "" {
			body["name"] = gf.Name
		}
	}
	if gf.Instructions != "" {
		body["instructions"] = gf.Instructions
	}
	if gf.enabledSet {
		body["enabled"] = gf.Enabled
	}
	if gf.FromEmail != "" {
		body["from_email"] = gf.FromEmail
	}
	if gf.FromDomain != "" {
		body["from_domain"] = gf.FromDomain
	}
	if gf.FromRegex != "" {
		body["from_regex"] = gf.FromRegex
	}
	if gf.SubjectRegex != "" {
		body["subject_regex"] = gf.SubjectRegex
	}
	if len(gf.BodyContains) > 0 {
		body["body_contains"] = gf.BodyContains
	}
	if gf.BodyRegex != "" {
		body["body_regex"] = gf.BodyRegex
	}
	return body
}

// hasAnyMatcher returns true if AT LEAST one matcher field is set.
// Create-time enforcement of "guidance must have a matcher"; update
// doesn't enforce this client-side because the user might be
// PATCHing other fields and leaving matchers as-is.
func (gf *guidanceMutationFlags) hasAnyMatcher() bool {
	return gf.FromEmail != "" || gf.FromDomain != "" || gf.FromRegex != "" ||
		gf.SubjectRegex != "" || len(gf.BodyContains) > 0 || gf.BodyRegex != ""
}

func newGuidanceCreateCmd() *cobra.Command {
	var gf guidanceMutationFlags
	var mf mutationFlags
	c := &cobra.Command{
		Use:   "create",
		Short: "Create a new guidance entry for this agent",
		RunE: func(cmd *cobra.Command, _ []string) error {
			gf.markFlagsBeforeRun(cmd)
			if gf.Name == "" || gf.Instructions == "" {
				return exitcode.Wrap(exitcode.Usage,
					fmt.Errorf("--name and --instructions are both required"))
			}
			if !gf.hasAnyMatcher() {
				return exitcode.Wrap(exitcode.Usage,
					fmt.Errorf("at least one matcher is required: --from-email / --from-domain / --from-regex / --subject-regex / --body-contains / --body-regex"))
			}
			body := gf.buildBody(true)

			if mf.DryRun {
				return printJSON(cmd.OutOrStdout(),
					newDryRunEnvelope(http.MethodPost, "/guidance", body, mf))
			}

			cli := newAPIClient()
			resp, err := cli.DoWithHeaders(http.MethodPost, "/guidance", body, nil, mf.Headers())
			if err != nil {
				return fmt.Errorf("POST /guidance: %w", err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	bindGuidanceFlags(c, &gf)
	bindMutationFlags(c, &mf)
	return c
}

func newGuidanceUpdateCmd() *cobra.Command {
	var gf guidanceMutationFlags
	var mf mutationFlags
	c := &cobra.Command{
		Use:   "update <id>",
		Short: "Update a guidance entry (only changed fields are sent)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			gf.markFlagsBeforeRun(cmd)
			body := gf.buildBody(false)
			if len(body) == 0 {
				return exitcode.Wrap(exitcode.Usage,
					fmt.Errorf("nothing to update; pass at least one field flag"))
			}

			path := "/guidance/" + args[0]
			if mf.DryRun {
				return printJSON(cmd.OutOrStdout(),
					newDryRunEnvelope(http.MethodPatch, path, body, mf))
			}

			cli := newAPIClient()
			resp, err := cli.DoWithHeaders(http.MethodPatch, path, body, nil, mf.Headers())
			if err != nil {
				return fmt.Errorf("PATCH %s: %w", path, err)
			}
			defer resp.Body.Close()
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	bindGuidanceFlags(c, &gf)
	bindMutationFlags(c, &mf)
	return c
}

func newGuidanceDeleteCmd() *cobra.Command {
	var mf mutationFlags
	c := &cobra.Command{
		Use:   "delete <id>",
		Short: "Delete a guidance entry",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/guidance/" + args[0]
			if mf.DryRun {
				return printJSON(cmd.OutOrStdout(),
					newDryRunEnvelope(http.MethodDelete, path, nil, mf))
			}

			cli := newAPIClient()
			resp, err := cli.DoWithHeaders(http.MethodDelete, path, nil, nil, mf.Headers())
			if err != nil {
				return fmt.Errorf("DELETE %s: %w", path, err)
			}
			defer resp.Body.Close()

			// 204 No Content has no body — emit an explicit acknowledgment
			// so agents see something on stdout regardless of HTTP code.
			if resp.StatusCode == http.StatusNoContent {
				return printJSON(cmd.OutOrStdout(), map[string]any{
					"deleted": true,
					"id":      args[0],
				})
			}
			return passthroughJSON(cmd.OutOrStdout(), resp)
		},
	}
	bindMutationFlags(c, &mf)
	return c
}

// guard against a future refactor that drops these imports;
// referenced via the strings + json packages in error/inline paths.
var _ = strings.HasPrefix
var _ = json.Marshal
