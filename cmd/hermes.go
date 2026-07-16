package cmd

import (
	"embed"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

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
	c.AddCommand(newHermesDoctorCmd())
	return c
}

// newHermesDoctorCmd diagnoses and repairs a MonsterMailbox Hermes plugin
// install. Its main job is evicting shadow plugin directories (see
// deShadowMonstermailboxPlugins) that silently override the installed adapter.
func newHermesDoctorCmd() *cobra.Command {
	var home string
	c := &cobra.Command{
		Use:   "doctor",
		Short: "Diagnose and repair the MonsterMailbox Hermes plugin install",
		Long: `Checks the Hermes plugin directory for problems that silently break inbound
email — most importantly leftover backup/duplicate directories that declare the
same "monstermailbox" platform name and override the installed adapter (Hermes
loads every manifest under plugins/, last one wins). Repairs them by moving them
out of the plugin scan path into <hermes-home>/plugin-archive/.

Run this if inbound email behaves like an old plugin version (stale or duplicate
replies) after repeated installs. Restart the Hermes gateway afterward so the
cleaned plugin set loads.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()
			hHome, err := resolveHermesHome(home)
			if err != nil {
				return exitcode.Wrap(exitcode.Usage, err)
			}
			destDir := filepath.Join(hHome, "plugins", "monstermailbox")
			if _, err := os.Stat(destDir); err != nil {
				return exitcode.Wrap(exitcode.Usage,
					fmt.Errorf("no monstermailbox plugin at %s (run: mmb hermes install)", destDir))
			}
			n := deShadowMonstermailboxPlugins(hHome, out)
			if n == 0 {
				fmt.Fprintf(out, "✓ plugin scan path is clean — no shadowing directories found\n")
			} else {
				fmt.Fprintf(out, "\n✓ evicted %d shadowing dir(s). Restart the Hermes gateway now so the clean plugin set loads.\n", n)
			}
			return nil
		},
	}
	c.Flags().StringVar(&home, "home", "", "Hermes home dir (default: $HERMES_HOME or ~/.hermes)")
	return c
}

// deShadowMonstermailboxPlugins moves any directory under <hermes-home>/plugins/
// (other than the canonical "monstermailbox" dir) whose manifest declares
// name: monstermailbox out of the scan path and into <hermes-home>/plugin-archive/.
//
// Hermes discovers plugins by scanning EVERY subdirectory of plugins/ and
// registering each manifest it finds; when several declare the same platform
// name, the last one loaded wins. A leftover backup dir (e.g. the
// "monstermailbox.backup.<timestamp>" a deploy script leaves behind) therefore
// silently overrides the freshly-installed adapter — the exact bug that made
// installs appear to "not take". Returns the number of directories evicted.
func deShadowMonstermailboxPlugins(hHome string, out io.Writer) int {
	pluginsDir := filepath.Join(hHome, "plugins")
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		return 0
	}
	archiveDir := filepath.Join(hHome, "plugin-archive")
	moved := 0
	for _, e := range entries {
		if !e.IsDir() || e.Name() == "monstermailbox" {
			continue
		}
		dir := filepath.Join(pluginsDir, e.Name())
		if !declaresMonstermailboxPlugin(dir) {
			continue
		}
		if err := os.MkdirAll(archiveDir, 0o755); err != nil {
			fmt.Fprintf(out, "⚠ found shadow plugin %s but could not create %s: %v\n", e.Name(), archiveDir, err)
			continue
		}
		dest := filepath.Join(archiveDir, e.Name())
		if _, err := os.Stat(dest); err == nil {
			dest += ".dup"
		}
		if err := os.Rename(dir, dest); err != nil {
			fmt.Fprintf(out, "⚠ found shadow plugin %s but could not move it: %v\n", e.Name(), err)
			continue
		}
		fmt.Fprintf(out, "✓ evicted shadow plugin %s → %s (it declared name: monstermailbox and would override the real adapter)\n", e.Name(), dest)
		moved++
	}
	return moved
}

// declaresMonstermailboxPlugin reports whether dir holds a plugin manifest
// (plugin.yaml/plugin.yml) whose name is monstermailbox — i.e. it registers the
// same platform as the real plugin and would shadow it.
func declaresMonstermailboxPlugin(dir string) bool {
	for _, name := range []string{"plugin.yaml", "plugin.yml"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		var m struct {
			Name string `yaml:"name"`
		}
		if err := yaml.Unmarshal(data, &m); err != nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(m.Name), "monstermailbox") {
			return true
		}
	}
	return false
}

func newHermesInstallCmd() *cobra.Command {
	var home string
	var dryRun, force, noBackstop bool
	var backstopInterval string

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

			if _, err := parseBackstopInterval(backstopInterval); err != nil && !noBackstop {
				return exitcode.Wrap(exitcode.Usage, err)
			}

			if dryRun {
				fmt.Fprintf(out, "DRY RUN — would:\n")
				fmt.Fprintf(out, "  • write plugin files to %s\n", destDir)
				fmt.Fprintf(out, "  • evict any shadowing backup/duplicate plugin dir (declares name: monstermailbox) → %s/plugin-archive\n", hHome)
				fmt.Fprintf(out, "  • record the mmb path in %s/mmb_path\n", destDir)
				fmt.Fprintf(out, "  • patch %s: plugins.enabled += monstermailbox\n", cfgPath)
				fmt.Fprintf(out, "  • patch %s: platform_toolsets.monstermailbox: [hermes-cli]\n", cfgPath)
				fmt.Fprintf(out, "  • patch %s: command_allowlist += \"mmb *\"\n", cfgPath)
				fmt.Fprintf(out, "  • patch %s: display.platforms.monstermailbox → minimal tier (no heartbeats/progress)\n", cfgPath)
				if noBackstop {
					fmt.Fprintf(out, "  • (skipping backstop cron: --no-backstop)\n")
				} else {
					fmt.Fprintf(out, "  • write gate script %s and create/update the %q cron (every %s, deliver local)\n",
						filepath.Join(hHome, "scripts", gateScriptName), hermesBackstopJobName, backstopInterval)
				}
				fmt.Fprintf(out, "  • disable any active inbox.new webhook (the plugin delivers inbound; both at once = duplicate replies)\n")
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

			// Evict any leftover backup/duplicate plugin dir that declares the
			// same platform name — Hermes scans every dir under plugins/ and the
			// last manifest loaded wins, so a stray backup silently overrides
			// this adapter (an install that appears to "not take").
			if n := deShadowMonstermailboxPlugins(hHome, out); n > 0 {
				fmt.Fprintf(out, "✓ cleared %d shadowing plugin dir(s) from the scan path\n", n)
			}

			// Record the absolute mmb path so the adapter doesn't depend on the
			// gateway's PATH (which often omits the mmb install dir).
			mmbBin := recordMmbPath(destDir)
			if mmbBin != "" {
				fmt.Fprintf(out, "✓ recorded mmb path: %s\n", mmbBin)
			} else {
				fmt.Fprintf(out, "⚠ could not record mmb path; ensure mmb is on the gateway PATH or set MMB_BIN\n")
			}

			// Record HOME so gateway subprocesses find the mmb profile even when
			// the supervised gateway runs with a different HOME than this shell.
			mmbHome := recordMmbHome(destDir)
			if mmbHome != "" {
				fmt.Fprintf(out, "✓ recorded mmb HOME: %s\n", mmbHome)
			}

			if err := patchHermesConfig(cfgPath); err != nil {
				return fmt.Errorf("patch config.yaml: %w", err)
			}
			fmt.Fprintf(out, "✓ patched %s (plugins.enabled + platform_toolsets + command_allowlist; backup at %s.bak)\n", cfgPath, cfgPath)

			// Install the backstop cron (catches mail the realtime watcher misses).
			if !noBackstop {
				iv, _ := parseBackstopInterval(backstopInterval) // validated above
				if err := installHermesBackstop(out, hHome, mmbBin, mmbHome, iv); err != nil {
					fmt.Fprintf(out, "⚠ backstop cron not fully installed: %v\n", err)
				}
			}

			// The plugin now owns inbound delivery; a leftover inbox.new webhook
			// would double-deliver every email (duplicate replies). Pause it.
			disableConflictingInboxWebhooks(out, newAPIClient())

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
	c.Flags().StringVar(&backstopInterval, "backstop-interval", defaultBackstopInterval, "backstop cron interval (e.g. 15m, 30m, 1h)")
	c.Flags().BoolVar(&noBackstop, "no-backstop", false, "do not install the backstop cron")
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

// recordMmbHome writes the install-time HOME to <destDir>/mmb_home so the adapter
// can run mmb subprocesses with the HOME where the mmb profile lives (a supervised
// gateway often has a different HOME). Returns the path written, or "" on failure.
func recordMmbHome(destDir string) string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	if werr := os.WriteFile(filepath.Join(destDir, "mmb_home"), []byte(home+"\n"), 0o644); werr != nil {
		return ""
	}
	return home
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

	// display.platforms.monstermailbox: minimal/non-interactive tier.
	//
	// This is THE fix for "⏳ Working…" heartbeats and tool-progress getting
	// emailed instead of only real replies. Our platform is minted dynamically,
	// so Hermes doesn't have a built-in display tier for it and falls back to the
	// verbose global defaults (heartbeats/progress/interim chatter all ON). The
	// built-in email/sms/webhook platforms use the minimal tier; we mirror it so
	// Hermes never produces a non-reply surface for this platform at the source.
	display := yamlGetOrCreateMap(root, "display")
	platforms := yamlGetOrCreateMap(display, "platforms")
	mmbDisplay := yamlGetOrCreateMap(platforms, "monstermailbox")
	yamlSetMapValue(mmbDisplay, "tool_progress", yamlQuotedString("off")) // bare off parses as bool
	yamlSetMapValue(mmbDisplay, "interim_assistant_messages", yamlBool(false))
	yamlSetMapValue(mmbDisplay, "long_running_notifications", yamlBool(false)) // gates the ⏳ heartbeat
	yamlSetMapValue(mmbDisplay, "busy_ack_detail", yamlBool(false))
	yamlSetMapValue(mmbDisplay, "streaming", yamlBool(false))

	// compression.codex_gpt55_autoraise: false — suppresses the one-time
	// "ℹ Codex gpt-5.5 caps context at … auto-compaction was raised" notice that
	// Hermes replays over status_callback (i.e. emails) for gpt-5.5 agents. There
	// is no per-platform gate for that notice, only this feature toggle; disabling
	// it also makes the agent compact a bit earlier, which is fine for these
	// email agents (avoids the late-compaction wedge we hit on gpt-5.5).
	compression := yamlGetOrCreateMap(root, "compression")
	yamlSetMapValue(compression, "codex_gpt55_autoraise", yamlBool(false))

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

// yamlBool returns a scalar boolean node.
func yamlBool(b bool) *yaml.Node {
	v := "false"
	if b {
		v = "true"
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: v}
}

// yamlQuotedString returns a double-quoted scalar string node — needed for
// values like "off" that YAML would otherwise parse as a boolean.
func yamlQuotedString(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s, Style: yaml.DoubleQuotedStyle}
}

func yamlStringSeq(vals ...string) *yaml.Node {
	s := &yaml.Node{Kind: yaml.SequenceNode}
	for _, v := range vals {
		s.Content = append(s.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: v})
	}
	return s
}
