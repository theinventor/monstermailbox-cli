package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/theinventor/monstermailbox-cli/internal/updater"
	"github.com/spf13/cobra"
)

// `mmb update` — installs the latest released binary in place.
// `mmb update --check` — print update status, exit 1 if newer
// available so shell scripts can `mmb update --check && echo "up
// to date" || mmb update`.
func newUpdateCmd() *cobra.Command {
	var checkOnly, noCache bool
	var pinTo string
	c := &cobra.Command{
		Use:   "update",
		Short: "Check for and install a newer mmb binary",
		Long: `Updates the mmb binary in place.

Default: check the GitHub Releases API (cached for 24h),
download the asset for this platform, verify its sha256, and
atomically replace the running binary.

  mmb update              # install latest
  mmb update --check      # print status, exit 1 if update available
  mmb update --to v0.4.0  # pin to a specific tag
  mmb update --no-cache   # ignore the 24h cache, force a fresh check

Cache lives at ~/.config/mmb/update-check.json. The destination
path is detected from os.Executable() — if mmb was installed
root-owned (e.g. /usr/local/bin/mmb), you'll get a clear error
asking you to use sudo or move the binary to a user-writable
location like ~/.local/bin/.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if checkOnly {
				return runUpdateCheck(cmd, noCache)
			}
			return runUpdateInstall(cmd, pinTo)
		},
	}
	c.Flags().BoolVar(&checkOnly, "check", false, "only check; print status and exit (1 if update available)")
	c.Flags().BoolVar(&noCache, "no-cache", false, "ignore the 24h cache; force a fresh GitHub check")
	c.Flags().StringVar(&pinTo, "to", "", "install a specific tag instead of latest (e.g. v0.4.0)")
	return c
}

func runUpdateCheck(cmd *cobra.Command, noCache bool) error {
	if noCache {
		// Bypass cache by clearing the file before CheckForUpdate.
		_ = os.Remove(updater.CachePath())
	}
	info := updater.CheckForUpdate(Version)

	enc := json.NewEncoder(cmd.OutOrStdout())
	enc.SetIndent("", "  ")
	if err := enc.Encode(info); err != nil {
		return err
	}
	if info.Available {
		// Non-zero exit so `mmb update --check && ...` works in shells.
		// os.Exit (rather than returning an error) so we don't print
		// any error message to stderr — the caller wanted JSON only.
		os.Exit(1)
	}
	return nil
}

func runUpdateInstall(cmd *cobra.Command, pinTo string) error {
	rel, err := updater.LatestRelease(nil)
	if err != nil {
		return fmt.Errorf("could not fetch release info: %w", err)
	}
	// --to allows pinning to a specific tag. We have to fetch by tag
	// because LatestRelease only returns latest. For now, just refuse
	// if pinTo doesn't match latest — the github releases-by-tag API
	// is a separate call we can add when the use case shows up.
	if pinTo != "" && !strings.EqualFold(pinTo, rel.TagName) {
		return fmt.Errorf("--to %s differs from latest %s; pinning is not yet implemented (file an issue if you need it)",
			pinTo, rel.TagName)
	}

	// Where does the running binary live?
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate own binary: %w", err)
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "current: %s\n", Version)
	fmt.Fprintf(out, "latest:  %s\n", rel.TagName)
	fmt.Fprintf(out, "target:  %s\n", exe)
	fmt.Fprintln(out, "downloading…")

	if err := updater.Install(rel, exe, &http.Client{}); err != nil {
		return fmt.Errorf("install: %w", err)
	}
	// Refresh the cache so subsequent commands don't keep saying
	// "update available" when we just installed it.
	_ = os.Remove(updater.CachePath())

	fmt.Fprintf(out, "✓ installed %s — try `mmb --version` to verify\n", rel.TagName)
	return nil
}

