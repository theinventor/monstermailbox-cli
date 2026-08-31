package cmd

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/theinventor/monstermailbox-cli/internal/exitcode"
)

// Embedded OpenClaw plugin assets. `mmb openclaw install` writes these into
// <openclaw-home>/extensions/monstermailbox/ and patches openclaw.json.
//
//go:embed embedded/plugins/openclaw
var openclawPluginFS embed.FS

const openclawPluginEmbedRoot = "embedded/plugins/openclaw"
const defaultOpenClawSessionKey = "agent:main:subagent:monstermailbox"

func newOpenClawCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "openclaw",
		Short: "Integrate MonsterMailbox with an OpenClaw agent",
		Long: `Install the MonsterMailbox plugin into an OpenClaw install so the agent
receives inbound email via mmb inbox wait/reconcile and replies
via the mmb CLI — no webhook, no public endpoint, no signing secret.`,
	}
	c.AddCommand(newOpenClawInstallCmd())
	return c
}

func newOpenClawInstallCmd() *cobra.Command {
	defaultMMBBin := detectDefaultMMBBin()
	var home, sessionKey, state, allowed, mmbBin, mmbProfile string
	var dryRun, force, noBackstop bool
	var backstopInterval string

	c := &cobra.Command{
		Use:   "install",
		Short: "Install + enable the MonsterMailbox plugin in OpenClaw",
		Long: `Writes the plugin to <openclaw-home>/extensions/monstermailbox/, links the
openclaw SDK so its import resolves, and patches openclaw.json to add the plugin
to plugins.load.paths and enable plugins.entries.monstermailbox.

After install, restart the OpenClaw gateway so the startup service picks up the
watcher, then verify with:  openclaw plugins inspect monstermailbox`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			ocHome, err := resolveOpenClawHome(home)
			if err != nil {
				return exitcode.Wrap(exitcode.Usage, err)
			}
			cfgPath := filepath.Join(ocHome, "openclaw.json")
			if _, err := os.Stat(cfgPath); err != nil {
				return exitcode.Wrap(exitcode.Usage,
					fmt.Errorf("no openclaw.json at %s (is OpenClaw set up here? pass --home)", cfgPath))
			}

			destDir := filepath.Join(ocHome, "extensions", "monstermailbox")
			if mmbProfile == "" && !dryRun {
				mmbProfile = detectDefaultMMBProfile(mmbBin)
			}
			pluginCfg := map[string]any{
				"sessionKey": sessionKey,
				"state":      state,
				"mmbBin":     mmbBin,
			}
			if mmbProfile != "" {
				pluginCfg["mmbProfile"] = mmbProfile
			}
			if s := splitCSV(allowed); len(s) > 0 {
				pluginCfg["allowedSenders"] = s
			}

			if _, err := parseBackstopInterval(backstopInterval); err != nil && !noBackstop {
				return exitcode.Wrap(exitcode.Usage, err)
			}

			if dryRun {
				fmt.Fprintf(out, "DRY RUN — would:\n")
				fmt.Fprintf(out, "  • write plugin files to %s\n", destDir)
				fmt.Fprintf(out, "  • link openclaw SDK into %s/node_modules/openclaw\n", destDir)
				fmt.Fprintf(out, "  • add %s to plugins.load.paths in %s\n", destDir, cfgPath)
				fmt.Fprintf(out, "  • enable plugins.entries.monstermailbox with config %v\n", pluginCfg)
				if noBackstop {
					fmt.Fprintf(out, "  • (skipping backstop cron: --no-backstop)\n")
				} else {
					fmt.Fprintf(out, "  • add/update the %q cron in %s (every %s, isolated session)\n",
						openClawBackstopJobName, filepath.Join(ocHome, "cron", "jobs.json"), backstopInterval)
				}
				fmt.Fprintf(out, "  • disable any active inbox.new webhook (the plugin delivers inbound; both at once = duplicate replies)\n")
				return nil
			}

			if _, err := os.Stat(destDir); err == nil && !force {
				return exitcode.Wrap(exitcode.Usage,
					fmt.Errorf("%s already exists; pass --force to overwrite", destDir))
			}
			if err := writeEmbeddedPlugin(openclawPluginFS, openclawPluginEmbedRoot, destDir); err != nil {
				return fmt.Errorf("write plugin files: %w", err)
			}
			fmt.Fprintf(out, "✓ wrote plugin to %s\n", destDir)

			if msg := linkOpenClawSDK(destDir); msg != "" {
				fmt.Fprintf(out, "%s\n", msg)
			}

			if err := patchOpenClawConfig(cfgPath, destDir, pluginCfg); err != nil {
				return fmt.Errorf("patch openclaw.json: %w", err)
			}
			fmt.Fprintf(out, "✓ patched %s (load.paths + entries.monstermailbox enabled; backup at %s.bak)\n", cfgPath, cfgPath)

			// Install the backstop cron (catches mail the realtime watcher misses).
			if !noBackstop {
				iv, _ := parseBackstopInterval(backstopInterval) // validated above
				if err := installOpenClawBackstop(out, ocHome, mmbBin, iv); err != nil {
					fmt.Fprintf(out, "⚠ backstop cron not installed: %v\n", err)
				}
			}

			// The plugin now owns inbound delivery; a leftover inbox.new webhook
			// would double-deliver every email (duplicate replies). Pause it.
			disableConflictingInboxWebhooks(out, newAPIClient())

			fmt.Fprintf(out, "\nNext steps:\n")
			fmt.Fprintf(out, "  1. Ensure the mmb CLI is authenticated: mmb whoami\n")
			fmt.Fprintf(out, "  2. Make sure the agent isn't on tools.profile messaging/minimal (needs exec).\n")
			fmt.Fprintf(out, "  3. Restart the OpenClaw gateway so the watcher service starts.\n")
			fmt.Fprintf(out, "  4. Verify: openclaw plugins inspect monstermailbox\n")
			return nil
		},
	}
	c.Flags().StringVar(&home, "home", "", "OpenClaw home dir (default: $OPENCLAW_HOME or ~/.openclaw)")
	c.Flags().StringVar(&sessionKey, "session-key", defaultOpenClawSessionKey, "OpenClaw session key for dispatched turns")
	c.Flags().StringVar(&state, "state", "trusted", "trust state to watch (trusted|quarantined|rejected)")
	c.Flags().StringVar(&allowed, "allowed-senders", "", "comma-separated sender allow-list (default: rely on server trust state)")
	c.Flags().StringVar(&mmbBin, "mmb-bin", defaultMMBBin, "path to the mmb binary the plugin shells out to")
	c.Flags().StringVar(&mmbProfile, "mmb-profile", "", "saved mmb auth profile to use inside OpenClaw (default: current mmb whoami profile; empty if unavailable)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "print what would happen, make no changes")
	c.Flags().BoolVar(&force, "force", false, "overwrite an existing plugin directory")
	c.Flags().StringVar(&backstopInterval, "backstop-interval", defaultBackstopInterval, "backstop cron interval (e.g. 15m, 30m, 1h)")
	c.Flags().BoolVar(&noBackstop, "no-backstop", false, "do not install the backstop cron")
	return c
}

func detectDefaultMMBBin() string {
	if p, err := exec.LookPath("mmb"); err == nil && p != "" {
		return p
	}
	return "mmb"
}

func detectDefaultMMBProfile(mmbBin string) string {
	out, err := exec.Command(mmbBin, "whoami").Output()
	if err != nil {
		return ""
	}
	var identity map[string]any
	if err := json.Unmarshal(out, &identity); err != nil {
		return ""
	}
	if profile, ok := identity["profile"].(string); ok {
		return profile
	}
	return ""
}

func resolveOpenClawHome(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	if env := os.Getenv("OPENCLAW_HOME"); env != "" {
		return env, nil
	}
	h, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot resolve home dir: %w", err)
	}
	return filepath.Join(h, ".openclaw"), nil
}

// writeEmbeddedPlugin copies every file under embedRoot in fsys into destDir,
// flattening the embed prefix.
func writeEmbeddedPlugin(fsys embed.FS, embedRoot, destDir string) error {
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	return fs.WalkDir(fsys, embedRoot, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := fsys.ReadFile(p)
		if err != nil {
			return err
		}
		rel := strings.TrimPrefix(p, embedRoot+"/")
		target := filepath.Join(destDir, rel)
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
}

// linkOpenClawSDK symlinks <destDir>/node_modules/openclaw -> the globally
// installed openclaw package so `import "openclaw/plugin-sdk/..."` resolves from
// the extension dir. Best-effort: returns a human message (success or guidance).
func linkOpenClawSDK(destDir string) string {
	globalRoot, err := exec.Command("npm", "root", "-g").Output()
	if err != nil {
		return "⚠ could not run `npm root -g`; if the plugin fails to import the openclaw SDK, link it manually into node_modules/openclaw"
	}
	pkg := filepath.Join(strings.TrimSpace(string(globalRoot)), "openclaw")
	if _, err := os.Stat(pkg); err != nil {
		return "⚠ openclaw package not found in global node_modules; link it into node_modules/openclaw if the SDK import fails"
	}
	nm := filepath.Join(destDir, "node_modules")
	if err := os.MkdirAll(nm, 0o755); err != nil {
		return "⚠ could not create node_modules for the SDK link: " + err.Error()
	}
	link := filepath.Join(nm, "openclaw")
	_ = os.Remove(link)
	if err := os.Symlink(pkg, link); err != nil {
		return "⚠ could not symlink the openclaw SDK: " + err.Error()
	}
	return "✓ linked openclaw SDK (" + pkg + ")"
}

// patchOpenClawConfig adds destDir to plugins.load.paths and enables
// plugins.entries.monstermailbox in openclaw.json, preserving other keys.
// Writes a .bak of the original first.
func patchOpenClawConfig(cfgPath, destDir string, pluginCfg map[string]any) error {
	raw, err := os.ReadFile(cfgPath)
	if err != nil {
		return err
	}
	if err := os.WriteFile(cfgPath+".bak", raw, 0o644); err != nil {
		return fmt.Errorf("write backup: %w", err)
	}

	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return fmt.Errorf("openclaw.json is not valid JSON: %w", err)
	}

	plugins := childMap(root, "plugins")
	load := childMap(plugins, "load")
	load["paths"] = appendUnique(normalizeOpenClawPluginLoadPaths(load["paths"], destDir), destDir)

	entries := childMap(plugins, "entries")
	entries["monstermailbox"] = map[string]any{
		"enabled": true,
		"config":  pluginCfg,
	}

	pretty, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	pretty = append(pretty, '\n')
	return os.WriteFile(cfgPath, pretty, 0o644)
}

// childMap returns parent[key] as a map[string]any, creating it if missing or
// not a map.
func childMap(parent map[string]any, key string) map[string]any {
	if existing, ok := parent[key].(map[string]any); ok {
		return existing
	}
	m := map[string]any{}
	parent[key] = m
	return m
}

// appendUnique appends val to an existing []any (string list), skipping if
// already present. Tolerates a missing/typeless current value.
func appendUnique(current any, val string) []any {
	var list []any
	if existing, ok := current.([]any); ok {
		list = existing
	}
	for _, e := range list {
		if s, ok := e.(string); ok && s == val {
			return list
		}
	}
	return append(list, val)
}

func normalizeOpenClawPluginLoadPaths(current any, destDir string) []any {
	var out []any
	legacyFilePath := filepath.Join(destDir, "index.js")
	if existing, ok := current.([]any); ok {
		for _, e := range existing {
			s, ok := e.(string)
			if ok && (s == destDir || s == legacyFilePath) {
				continue
			}
			out = append(out, e)
		}
	}
	return out
}

func splitCSV(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}
