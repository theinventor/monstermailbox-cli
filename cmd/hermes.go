package cmd

import (
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
	"gopkg.in/yaml.v3"
)

// Embedded Hermes plugin assets. `mmb hermes install` writes these into
// <hermes-home>/plugins/monstermailbox/ and patches config.yaml.
//
// The `all:` prefix is required so files starting with `_` (e.g. __init__.py)
// are included — plain //go:embed skips them.
//
//go:embed all:embedded/plugins/hermes
var hermesPluginFS embed.FS

const hermesPluginEmbedRoot = "embedded/plugins/hermes"

func newHermesCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "hermes",
		Short: "Integrate MonsterMailbox with a Hermes agent",
		Long: `Install the MonsterMailbox platform plugin into a Hermes install so the agent
receives inbound email over the SSE event stream (mmb inbox watch) and replies
via the mmb CLI — no webhook, no public endpoint, no signing secret.`,
	}
	c.AddCommand(newHermesInstallCmd())
	return c
}

func newHermesInstallCmd() *cobra.Command {
	var home string
	var dryRun, force bool

	c := &cobra.Command{
		Use:   "install",
		Short: "Install + enable the MonsterMailbox platform plugin in Hermes",
		Long: `Writes the plugin to <hermes-home>/plugins/monstermailbox/ and patches
config.yaml so the platform runs with a usable toolset:

  plugins.enabled            += monstermailbox      (user plugins are opt-in)
  platform_toolsets.monstermailbox: [hermes-cli]    (so the turn has ` + "`terminal`" + `)
  command_allowlist          += "mmb *"             (unattended mmb execution)

The toolset line is the make-or-break step: a plugin platform otherwise defaults
to a non-existent ` + "`hermes-<name>`" + ` toolset and the agent gets no shell — the same
failure the plain webhook channel has.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			hHome, err := resolveHermesHome(home)
			if err != nil {
				return exitcode.Wrap(exitcode.Usage, err)
			}
			cfgPath := filepath.Join(hHome, "config.yaml")
			if _, err := os.Stat(cfgPath); err != nil {
				return exitcode.Wrap(exitcode.Usage,
					fmt.Errorf("no config.yaml at %s (is Hermes set up here? pass --home)", cfgPath))
			}
			destDir := filepath.Join(hHome, "plugins", "monstermailbox")

			if dryRun {
				fmt.Fprintf(out, "DRY RUN — would:\n")
				fmt.Fprintf(out, "  • write plugin files to %s\n", destDir)
				fmt.Fprintf(out, "  • record the mmb path in %s/mmb_path\n", destDir)
				fmt.Fprintf(out, "  • patch %s: plugins.enabled += monstermailbox\n", cfgPath)
				fmt.Fprintf(out, "  • patch %s: platform_toolsets.monstermailbox: [hermes-cli]\n", cfgPath)
				fmt.Fprintf(out, "  • patch %s: command_allowlist += \"mmb *\"\n", cfgPath)
				return nil
			}

			if _, err := os.Stat(destDir); err == nil && !force {
				return exitcode.Wrap(exitcode.Usage,
					fmt.Errorf("%s already exists; pass --force to overwrite", destDir))
			}
			if err := writeEmbeddedPlugin(hermesPluginFS, hermesPluginEmbedRoot, destDir); err != nil {
				return fmt.Errorf("write plugin files: %w", err)
			}
			fmt.Fprintf(out, "✓ wrote plugin to %s\n", destDir)

			// Record the absolute mmb path so the adapter doesn't depend on the
			// gateway's PATH (which often omits the mmb install dir).
			if mp := recordMmbPath(destDir); mp != "" {
				fmt.Fprintf(out, "✓ recorded mmb path: %s\n", mp)
			} else {
				fmt.Fprintf(out, "⚠ could not record mmb path; ensure mmb is on the gateway PATH or set MMB_BIN\n")
			}

			if err := patchHermesConfig(cfgPath); err != nil {
				return fmt.Errorf("patch config.yaml: %w", err)
			}
			fmt.Fprintf(out, "✓ patched %s (plugins.enabled + platform_toolsets + command_allowlist; backup at %s.bak)\n", cfgPath, cfgPath)

			fmt.Fprintf(out, "\nNext steps:\n")
			fmt.Fprintf(out, "  1. Ensure the mmb CLI is authenticated: mmb whoami\n")
			fmt.Fprintf(out, "  2. Restart the Hermes gateway so the plugin loads.\n")
			fmt.Fprintf(out, "  3. Verify: hermes plugins list  (look for monstermailbox → enabled)\n")
			return nil
		},
	}
	c.Flags().StringVar(&home, "home", "", "Hermes home dir (default: $HERMES_HOME or ~/.hermes)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "print what would happen, make no changes")
	c.Flags().BoolVar(&force, "force", false, "overwrite an existing plugin directory")
	return c
}

// recordMmbPath writes the absolute path of the running mmb binary to
// <destDir>/mmb_path so the plugin adapter can find mmb regardless of the
// gateway process's PATH. Returns the path written, or "" on failure.
func recordMmbPath(destDir string) string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, rerr := filepath.EvalSymlinks(exe); rerr == nil {
		exe = resolved
	}
	if werr := os.WriteFile(filepath.Join(destDir, "mmb_path"), []byte(exe+"\n"), 0o644); werr != nil {
		return ""
	}
	return exe
}

func resolveHermesHome(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if env := os.Getenv("HERMES_HOME"); env != "" {
		return env, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot resolve home dir: %w", err)
	}
	return filepath.Join(h, ".hermes"), nil
}

// patchHermesConfig adds the three managed keys to config.yaml, preserving the
// rest of the document (comments + key order) via the yaml.Node API. Writes a
// .bak of the original first.
func patchHermesConfig(cfgPath string) error {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(cfgPath+".bak", raw, 0o644); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}

	var doc yaml.Node
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return fmt.Errorf("config.yaml is not valid YAML: %w", err)
	}
	root := documentRoot(&doc)

	// plugins.enabled += monstermailbox
	plugins := yamlGetOrCreateMap(root, "plugins")
	enabled := yamlGetOrCreateSeq(plugins, "enabled")
	yamlSeqAddString(enabled, "monstermailbox")

	// platform_toolsets.monstermailbox: [hermes-cli]
	toolsets := yamlGetOrCreateMap(root, "platform_toolsets")
	yamlSetMapValue(toolsets, "monstermailbox", yamlStringSeq("hermes-cli"))

	// command_allowlist += "mmb *"
	allow := yamlGetOrCreateTopSeq(root, "command_allowlist")
	yamlSeqAddString(allow, "mmb *")

	pretty, err := yaml.Marshal(&doc)
	if err != nil {
		return err
	}
	return os.WriteFile(cfgPath, pretty, 0o644)
}

// documentRoot returns the mapping node at the root of a parsed YAML document,
// creating an empty mapping if the document was empty.
func documentRoot(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		return doc.Content[0]
	}
	m := &yaml.Node{Kind: yaml.MappingNode}
	doc.Kind = yaml.DocumentNode
	doc.Content = []*yaml.Node{m}
	return m
}

// yamlMapGet returns the value node for key in a mapping node, or nil.
func yamlMapGet(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func yamlSetMapValue(m *yaml.Node, key string, val *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = val
			return
		}
	}
	m.Content = append(m.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Value: key},
		val,
	)
}

func yamlGetOrCreateMap(parent *yaml.Node, key string) *yaml.Node {
	if v := yamlMapGet(parent, key); v != nil && v.Kind == yaml.MappingNode {
		return v
	}
	m := &yaml.Node{Kind: yaml.MappingNode}
	yamlSetMapValue(parent, key, m)
	return m
}

func yamlGetOrCreateSeq(parent *yaml.Node, key string) *yaml.Node {
	if v := yamlMapGet(parent, key); v != nil && v.Kind == yaml.SequenceNode {
		return v
	}
	s := &yaml.Node{Kind: yaml.SequenceNode}
	yamlSetMapValue(parent, key, s)
	return s
}

// yamlGetOrCreateTopSeq is yamlGetOrCreateSeq for a top-level key.
func yamlGetOrCreateTopSeq(root *yaml.Node, key string) *yaml.Node {
	return yamlGetOrCreateSeq(root, key)
}

func yamlSeqAddString(seq *yaml.Node, val string) {
	for _, n := range seq.Content {
		if n.Value == val {
			return
		}
	}
	seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: val})
}

func yamlStringSeq(vals ...string) *yaml.Node {
	s := &yaml.Node{Kind: yaml.SequenceNode}
	for _, v := range vals {
		s.Content = append(s.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: v})
	}
	return s
}
