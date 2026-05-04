package cmd

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/theinventor/monstermailbox-cli/bridge/internal/api"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/config"
)

// newRotateKeyCmd: POST /bridge/rotate, persist the new key, print
// the previous-last4 for log correlation.
func newRotateKeyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rotate-key",
		Short: "Rotate the bridge-scoped API key (revokes old, issues new)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), 30*time.Second)
			defer cancel()
			c := api.New(cfg.APIBaseURL, cfg.APIKey)
			resp, err := c.Rotate(ctx)
			if err != nil {
				return fmt.Errorf("rotate failed: %w", err)
			}
			cfg.APIKey = resp.APIKey
			if err := config.Save(cfg); err != nil {
				return fmt.Errorf("CRITICAL: rotation succeeded server-side but local save failed; old key (last4 %s) is REVOKED. New key: %s — write it to %s manually. Underlying error: %w",
					resp.PreviousLast4, resp.APIKey, mustPath(), err)
			}
			cmd.Printf("rotated. previous last4=%s, new last4=%s\n", resp.PreviousLast4, resp.APIKey[len(resp.APIKey)-4:])
			cmd.Println("If the daemon was running, restart it: `mmb-bridge stop && mmb-bridge start --detach`.")
			return nil
		},
	}
}

func mustPath() string {
	p, _ := config.Path()
	return p
}
