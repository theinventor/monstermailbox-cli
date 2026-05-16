package cmd

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/theinventor/monstermailbox-cli/internal/config"
	"github.com/theinventor/monstermailbox-cli/internal/enums"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
)

// AgentContextSchemaVersion is bumped when the shape of the JSON
// emitted by `mmb agent-context` changes in a backwards-incompatible
// way (renamed/removed fields). Additive changes do NOT bump it. Agents
// pin against this version so they can detect breakage in CI.
const AgentContextSchemaVersion = "1"

// `mmb agent-context` — versioned, machine-readable description of the
// entire CLI surface. Principle 7 (three-layer introspection): --help
// for humans, this for agents, SKILL.md for prose workflow guidance.
//
// Output shape is:
//
//	{
//	  "schema_version":  "1",
//	  "cli_version":     "v0.3.0",
//	  "commands":        { name: { summary, subcommands?, flags?, args, hidden, deprecated? } },
//	  "global_flags":    { "--profile": { type, usage } },
//	  "enums":           { name: [...] },
//	  "exit_codes":      { "0": "success", "4": "resource not found", ... },
//	  "available_profiles": [...],
//	  "endpoints":       { "feedback_upstream": null, "product_feedback": "/agent_product_feedback", "support_intake": "/contact_support" }
//	}
//
// Walks the cobra command tree at runtime — no codegen, no schema file
// to keep in sync. The tradeoff is that every flag's metadata comes from
// what we set on the cobra.Flag (Name, Shorthand, Usage, DefValue, Type).
// That's enough for agents to construct valid invocations without
// scraping --help, which is the whole point.
func newAgentContextCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "agent-context",
		Short: "Emit a versioned machine-readable description of the CLI surface",
		Long: `Prints a JSON document describing every command, subcommand, flag,
enum, and exit code the CLI knows about, plus the names of saved
profiles and which integration endpoints are wired up. Agents read
this once at startup to construct valid invocations without parsing
--help text.

Schema versioning: the top-level "schema_version" field is bumped on
breakage; agents pin against it.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return printJSON(cmd.OutOrStdout(), buildAgentContext(cmd.Root()))
		},
	}
}

// buildAgentContext walks the rootCmd tree and produces the JSON shape.
// Pulled out for testability — tests can construct a command tree and
// assert on the exact emitted shape.
func buildAgentContext(root *cobra.Command) map[string]any {
	commands := map[string]any{}
	for _, c := range root.Commands() {
		if shouldSkipForAgents(c) {
			continue
		}
		commands[c.Name()] = describeCommand(c)
	}

	exitCodes := map[string]string{}
	for _, code := range exitcode.All() {
		exitCodes[itoa(code)] = exitcode.Description(code)
	}

	return map[string]any{
		"schema_version":     AgentContextSchemaVersion,
		"cli_version":        Version,
		"commands":           commands,
		"global_flags":       describeFlags(root.PersistentFlags()),
		"enums":              enums.InContext,
		"exit_codes":         exitCodes,
		"available_profiles": availableProfiles(),
		"endpoints": map[string]any{
			"feedback_upstream": feedbackUpstreamForContext(),
			"product_feedback":  "/agent_product_feedback",
			"support_intake":    supportIntakePath,
		},
	}
}

// describeCommand recursively walks one cobra.Command. Returns nil for
// commands that should be hidden from the agent surface (the
// agent-context command itself, deprecated aliases, etc.) — the caller
// filters them out via shouldSkipForAgents.
func describeCommand(c *cobra.Command) map[string]any {
	desc := map[string]any{
		"summary": c.Short,
	}

	// Long description if it adds detail beyond Short.
	if c.Long != "" && c.Long != c.Short {
		desc["description"] = c.Long
	}

	// Use string carries argument shape ("get <id>", "create [<sender>]").
	// Strip the command name prefix so agents see only the args portion.
	if useArgs := strings.TrimSpace(strings.TrimPrefix(c.Use, c.Name())); useArgs != "" {
		desc["args"] = useArgs
	}

	// Local flags (excluding inherited cobra flags like --help).
	if flags := describeFlags(c.LocalFlags()); len(flags) > 0 {
		desc["flags"] = flags
	}

	// Subcommands.
	subs := map[string]any{}
	for _, sub := range c.Commands() {
		if shouldSkipForAgents(sub) {
			continue
		}
		subs[sub.Name()] = describeCommand(sub)
	}
	if len(subs) > 0 {
		desc["subcommands"] = subs
	}

	if c.Deprecated != "" {
		desc["deprecated"] = c.Deprecated
	}
	return desc
}

// describeFlags emits a sorted, deterministic flag map. We skip cobra's
// inherited --help/--version flags (they're universal) so agents see
// only the command-specific surface.
func describeFlags(set *pflag.FlagSet) map[string]any {
	flags := map[string]any{}
	set.VisitAll(func(f *pflag.Flag) {
		if f.Hidden {
			return
		}
		// Skip cobra's auto-injected --help (universal, not interesting).
		if f.Name == "help" {
			return
		}
		entry := map[string]any{
			"type":  f.Value.Type(),
			"usage": f.Usage,
		}
		if f.DefValue != "" && f.DefValue != "false" && f.DefValue != "0" && f.DefValue != "[]" {
			entry["default"] = f.DefValue
		}
		if f.Shorthand != "" {
			entry["shorthand"] = f.Shorthand
		}
		flags["--"+f.Name] = entry
	})
	return flags
}

// shouldSkipForAgents filters cobra.Command instances that don't belong
// in the agent-context surface. Hidden commands are deprecated aliases
// (per Phase 1's `msg show` and `whitelist add`); the help command is
// cobra-builtin noise.
func shouldSkipForAgents(c *cobra.Command) bool {
	if c.Hidden {
		return true
	}
	if c.Name() == "help" {
		return true
	}
	return false
}

// availableProfiles surfaces saved profile names so agents know which
// --profile values they can pass without reading the config file
// directly. Returns empty slice if no profiles are configured.
func availableProfiles() []string {
	f, err := config.Load()
	if err != nil {
		return []string{}
	}
	if f.Profiles == nil {
		return []string{}
	}
	return f.Names()
}

// feedbackUpstreamForContext returns the configured upstream endpoint
// for `mmb feedback` (principle 10) or nil if no endpoint is wired up.
// Surfaced in agent-context so agents can decide whether their feedback
// reaches the maintainers or only sits in the local JSONL log.
func feedbackUpstreamForContext() any {
	if v := os.Getenv(EnvFeedbackEndpoint); v != "" {
		return v
	}
	return nil
}

// itoa is a tiny int-to-string helper that avoids strconv import bloat
// at package-init time. Used only to key the exit-codes map.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	buf := [12]byte{}
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
