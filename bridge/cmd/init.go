package cmd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/theinventor/monstermailbox-cli/bridge/internal/api"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/config"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/gogcli"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/oauth"
)

// newInitCmd: end-to-end first-run flow:
//
//  1. Verify gog is installed + authenticated for the chosen account.
//  2. Verify gog's OAuth token carries the pubsub scope (re-auth if not).
//  3. Redeem the enrollment token at /bridges/enroll → get bridge API key.
//  4. Register the Gmail Pub/Sub watch via gog (idempotent).
//  5. Write ~/.mmb-bridge/config.json (mode 0600).
//
// Every check prints what it's about to do AND what to run if it
// fails, so a copy-paste user (or AI agent) can fix things forward.
func newInitCmd() *cobra.Command {
	var (
		enrollmentToken string
		apiBaseURL      string
		account         string
		gcpProject      string
		pubsubTopic     string
		pubsubSub       string
		localOnly       bool
		logLevel        string
		skipWatchStart  bool
	)

	c := &cobra.Command{
		Use:   "init",
		Short: "Enroll this machine with monstermailbox + register the Gmail Pub/Sub watch",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if enrollmentToken == "" {
				return fmt.Errorf("--enrollment-token is required (mint one at Bridge → Generate setup token in your dashboard)")
			}
			if apiBaseURL == "" {
				return fmt.Errorf("--api-base-url is required (e.g. https://app.monstermailbox.com)")
			}
			if account == "" {
				return fmt.Errorf("--account is required (the gmail address gog watches; use the account you ran `gog login <email>` for)")
			}
			if !localOnly && (gcpProject == "" || pubsubTopic == "" || pubsubSub == "") {
				return fmt.Errorf("--gcp-project, --pubsub-topic, and --pubsub-subscription are all required (or pass --local-only). " +
					"See README — it has the exact gcloud commands to create these.")
			}

			// 1. gog presence + auth.
			if _, err := exec.LookPath("gog"); err != nil {
				return fmt.Errorf("`gog` is not on $PATH — install it: brew install steipete/tap/gogcli (or see https://github.com/steipete/gogcli)")
			}
			cmd.Printf("✓ gog is installed\n")

			ctx, cancel := context.WithTimeout(cmd.Context(), 90*time.Second)
			defer cancel()

			if err := exec.CommandContext(ctx, "gog", "auth", "status").Run(); err != nil {
				return fmt.Errorf("gog reports unauthenticated. Run:\n  gog login %s --extra-scopes=%s --force-consent",
					account, oauth.PubSubScope)
			}
			cmd.Printf("✓ gog is authenticated\n")

			// 2. PubSub scope check (lazy: try a token export and bail
			//    with the precise re-auth command if scope is missing).
			if !localOnly {
				src, err := oauth.LoadSourceFromGog(ctx, account)
				if err != nil {
					return err
				}
				if _, err := src.Token(ctx); err != nil {
					return err
				}
				cmd.Printf("✓ gog OAuth has pubsub scope\n")
			}

			// 3. Redeem the enrollment token.
			cmd.Printf("→ redeeming enrollment token at %s …\n", apiBaseURL)
			resp, err := api.Enroll(ctx, apiBaseURL, enrollmentToken)
			if err != nil {
				return fmt.Errorf("enrollment failed: %w", err)
			}
			cmd.Printf("✓ enrolled as %s (api_base_url=%s)\n", resp.AgentEmail, resp.APIBaseURL)

			// 4. Register Pub/Sub watch (idempotent on gog's side).
			if !localOnly && !skipWatchStart {
				topicFQDN := pubsubTopic
				if !strings.HasPrefix(topicFQDN, "projects/") {
					topicFQDN = fmt.Sprintf("projects/%s/topics/%s", gcpProject, pubsubTopic)
				}
				cmd.Printf("→ registering gmail Pub/Sub watch on %s …\n", topicFQDN)
				gog := gogcli.New(account)
				if err := gog.WatchStart(ctx, topicFQDN); err != nil {
					return fmt.Errorf("`gog gmail watch start --topic %s` failed.\n"+
						"Common causes:\n"+
						"  - the topic doesn't exist (create it: gcloud pubsub topics create %s)\n"+
						"  - Gmail's service account isn't a publisher on the topic. Run:\n"+
						"      gcloud pubsub topics add-iam-policy-binding %s \\\n"+
						"        --member=serviceAccount:gmail-api-push@system.gserviceaccount.com \\\n"+
						"        --role=roles/pubsub.publisher\n"+
						"\n"+
						"underlying error: %w", topicFQDN, pubsubTopic, pubsubTopic, err)
				}
				cmd.Printf("✓ Gmail will publish to %s\n", topicFQDN)
			}

			// 5. Write config.
			cfg := &config.Config{
				APIBaseURL:    resp.APIBaseURL,
				APIKey:        resp.APIKey,
				AgentEmail:    resp.AgentEmail,
				GoogleAccount: account,
				GCPProject:    gcpProject,
				PubSubTopic:   pubsubTopic,
				PubSubSub:     pubsubSub,
				LocalOnly:     localOnly,
				LogLevel:      logLevel,
			}
			if err := config.Save(cfg); err != nil {
				return err
			}
			path, _ := config.Path()
			cmd.Printf("✓ wrote %s (mode 0600)\n", path)
			cmd.Println()
			cmd.Println("Next: run `mmb-bridge start` to begin forwarding mail.")
			return nil
		},
	}
	c.Flags().StringVar(&enrollmentToken, "enrollment-token", "", "one-time bre_… token from the dashboard (Bridge → Generate setup token)")
	c.Flags().StringVar(&apiBaseURL,      "api-base-url",     "", "monstermailbox base URL (e.g. https://app.monstermailbox.com)")
	c.Flags().StringVar(&account,         "account",          "", "gmail address gog will watch (must match `gog login <email>`)")
	c.Flags().StringVar(&gcpProject,      "gcp-project",      "", "GCP project hosting the Pub/Sub topic + subscription")
	c.Flags().StringVar(&pubsubTopic,     "pubsub-topic",     "", "Pub/Sub topic name (e.g. gmail-events) — gmail-api-push@system.gserviceaccount.com must be a publisher on it")
	c.Flags().StringVar(&pubsubSub,       "pubsub-subscription", "", "Pub/Sub PULL subscription name (e.g. mmb-bridge-pull) on the topic above")
	c.Flags().BoolVar(&localOnly,         "local-only",       false, "ignore /bridge/policy; use ~/.mmb-bridge/whitelist.json")
	c.Flags().StringVar(&logLevel,        "log-level",        "info", "log level: debug|info|warn|error")
	c.Flags().BoolVar(&skipWatchStart,    "skip-watch-start", false, "skip `gog gmail watch start` (advanced; you've already registered the watch)")
	return c
}
