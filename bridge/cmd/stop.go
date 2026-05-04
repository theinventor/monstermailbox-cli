package cmd

import (
	"fmt"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/theinventor/monstermailbox-cli/bridge/internal/daemon"
)

func newStopCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the running bridge daemon (SIGTERM)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			pid, err := daemon.ReadPid()
			if err != nil {
				return fmt.Errorf("no pid file (is the bridge running?)")
			}
			if !daemon.IsAlive(pid) {
				_ = daemon.RemovePidFile()
				cmd.Printf("Bridge is not running (stale pid %d cleaned up).\n", pid)
				return nil
			}
			if err := syscall.Kill(pid, syscall.SIGTERM); err != nil {
				return fmt.Errorf("SIGTERM pid %d: %w", pid, err)
			}
			// Wait up to 10s for the daemon to exit cleanly.
			deadline := time.Now().Add(10 * time.Second)
			for time.Now().Before(deadline) {
				if !daemon.IsAlive(pid) {
					cmd.Printf("Bridge stopped (pid %d).\n", pid)
					_ = daemon.RemovePidFile()
					return nil
				}
				time.Sleep(200 * time.Millisecond)
			}
			cmd.Printf("Bridge did not exit within 10s of SIGTERM; pid %d still alive.\n", pid)
			return nil
		},
	}
}
