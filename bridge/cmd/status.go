package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/theinventor/monstermailbox-cli/bridge/internal/api"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/config"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/daemon"
)

func newStatusCmd() *cobra.Command {
	var asJSON bool
	c := &cobra.Command{
		Use:   "status",
		Short: "Show daemon state, last server policy version, gog scope health",
		RunE: func(cmd *cobra.Command, _ []string) error {
			report := buildStatus(cmd.Context())
			if asJSON {
				out, _ := json.MarshalIndent(report, "", "  ")
				cmd.Println(string(out))
				return nil
			}
			cmd.Printf("daemon:    %s\n", report.Daemon)
			if report.PID != 0 {
				cmd.Printf("pid:       %d\n", report.PID)
			}
			cmd.Printf("config:    %s\n", report.ConfigPath)
			if report.AgentEmail != "" {
				cmd.Printf("agent:     %s\n", report.AgentEmail)
			}
			if report.GoogleAccount != "" {
				cmd.Printf("gmail:     %s\n", report.GoogleAccount)
			}
			if report.PubSubSub != "" {
				cmd.Printf("pubsub:    projects/%s/subscriptions/%s\n", report.GCPProject, report.PubSubSub)
			}
			if report.PolicyVersion != nil {
				cmd.Printf("policy:    version=%d  %d entries  fetched %s\n",
					*report.PolicyVersion, report.PolicyEntries, time.Since(report.PolicyFetchedAt).Round(time.Second))
			}
			if report.PolicyError != "" {
				cmd.Printf("policy:    ERROR — %s\n", report.PolicyError)
			}
			cmd.Printf("local-only: %v\n", report.LocalOnly)
			return nil
		},
	}
	c.Flags().BoolVar(&asJSON, "json", false, "emit JSON")
	return c
}

type StatusReport struct {
	Daemon          string     `json:"daemon"` // running | stopped | stale
	PID             int        `json:"pid,omitempty"`
	ConfigPath      string     `json:"config_path"`
	AgentEmail      string     `json:"agent_email,omitempty"`
	GoogleAccount   string     `json:"google_account,omitempty"`
	GCPProject      string     `json:"gcp_project,omitempty"`
	PubSubSub       string     `json:"pubsub_sub,omitempty"`
	LocalOnly       bool       `json:"local_only"`
	PolicyVersion   *int       `json:"policy_version,omitempty"`
	PolicyEntries   int        `json:"policy_entries,omitempty"`
	PolicyFetchedAt time.Time  `json:"policy_fetched_at,omitempty"`
	PolicyError     string     `json:"policy_error,omitempty"`
}

func buildStatus(ctx context.Context) StatusReport {
	r := StatusReport{}
	if pid, err := daemon.ReadPid(); err == nil {
		r.PID = pid
		if daemon.IsAlive(pid) {
			r.Daemon = "running"
		} else {
			r.Daemon = "stale"
		}
	} else {
		r.Daemon = "stopped"
	}
	r.ConfigPath, _ = config.Path()
	cfg, err := config.Load()
	if err != nil {
		if errors.Is(err, config.ErrNotInitialized) {
			r.PolicyError = "not initialized — run `mmb-bridge init`"
		} else {
			r.PolicyError = err.Error()
		}
		return r
	}
	r.AgentEmail = cfg.AgentEmail
	r.GoogleAccount = cfg.GoogleAccount
	r.GCPProject = cfg.GCPProject
	r.PubSubSub = cfg.PubSubSub
	r.LocalOnly = cfg.LocalOnly

	if !cfg.LocalOnly {
		// Snapshot the server's reported policy version so the user
		// can confirm sync is working without reading the daemon log.
		client := api.New(cfg.APIBaseURL, cfg.APIKey)
		ctxT, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		if pol, err := client.GetPolicy(ctxT); err != nil {
			r.PolicyError = err.Error()
		} else {
			v := pol.Version
			r.PolicyVersion = &v
			r.PolicyEntries = len(pol.Whitelist)
			r.PolicyFetchedAt = time.Now()
		}
	}
	return r
}

// silence unused fmt import for linters that don't see the
// status build path above.
var _ = fmt.Sprintf
