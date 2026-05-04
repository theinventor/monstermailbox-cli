package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"

	"github.com/theinventor/monstermailbox-cli/bridge/internal/daemon"
)

// newStartCmd runs the daemon. Default is FOREGROUND (the user can
// wrap with launchd/systemd themselves); pass --detach to fork into
// a background child via the same binary's `--foreground` flag.
//
// We deliberately avoid double-fork daemonization tricks; launchd +
// systemd are the right tool for that, and the README walks through
// either. --detach is provided for quick testing without a launcher.
func newStartCmd() *cobra.Command {
	var detach bool
	var foreground bool

	c := &cobra.Command{
		Use:   "start",
		Short: "Start the bridge daemon",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if existing, err := daemon.ReadPid(); err == nil {
				if daemon.IsAlive(existing) {
					return fmt.Errorf("bridge is already running (pid %d). Run `mmb-bridge stop` first", existing)
				}
				_ = daemon.RemovePidFile()
			}

			if detach && !foreground {
				exe, err := os.Executable()
				if err != nil {
					return err
				}
				child := exec.Command(exe, "start", "--foreground")
				// Detach: don't inherit our stdin/out/err; child opens
				// its own log file via internal/log.
				child.Stdin = nil
				child.Stdout = nil
				child.Stderr = nil
				if err := child.Start(); err != nil {
					return fmt.Errorf("spawn detached child: %w", err)
				}
				cmd.Printf("Bridge started (pid %d, detached). Tail logs with `mmb-bridge logs -f`.\n", child.Process.Pid)
				return nil
			}

			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()
			return daemon.Run(ctx, foreground || !detach)
		},
	}
	c.Flags().BoolVar(&detach,     "detach",     false, "fork into a background process")
	c.Flags().BoolVar(&foreground, "foreground", false, "run in foreground (mirror logs to stderr too)")
	return c
}
