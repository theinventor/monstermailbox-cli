package cmd

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

//go:embed embedded/monstermailbox_skill.md
var monsterMailboxSkillSample string

const sampleSkillCommand = "mmb skill get monstermailbox"

func newSkillCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "skill",
		Short: "Print bundled sample skills for agent setup",
		Long: `Print bundled sample OpenClaw skills that teach agents how to use
MonsterMailbox safely. Install the sample in your agent workspace and replace
placeholders such as HUMAN_OWNER_NAME before relying on it in production.`,
	}
	c.AddCommand(newSkillGetCmd())
	return c
}

func newSkillGetCmd() *cobra.Command {
	var output string
	c := &cobra.Command{
		Use:   "get monstermailbox",
		Short: "Get the bundled MonsterMailbox OpenClaw sample skill",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) != 1 || args[0] != "monstermailbox" {
				return fmt.Errorf("usage: mmb skill get monstermailbox")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			if output == "" || output == "-" {
				_, err := fmt.Fprint(cmd.OutOrStdout(), monsterMailboxSkillSample)
				return err
			}
			if err := os.MkdirAll(filepath.Dir(output), 0o755); err != nil {
				return err
			}
			return os.WriteFile(output, []byte(monsterMailboxSkillSample), 0o644)
		},
	}
	c.Flags().StringVarP(&output, "output", "o", "", "write sample skill to this file instead of stdout")
	return c
}

func printSkillSetupReminder(out interface{ Write([]byte) (int, error) }) {
	fmt.Fprintf(out, "\nAgent setup tip: tell the human owner this agent should have a MonsterMailbox skill installed.\n")
	fmt.Fprintf(out, "Sample skill: `%s` (replace HUMAN_OWNER_NAME before use).\n", sampleSkillCommand)
}
