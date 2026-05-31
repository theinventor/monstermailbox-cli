package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/theinventor/monstermailbox-cli/internal/client"
)

const AgentSetupSchemaVersion = "1"

const defaultAgentSetupWaitDelivery = 15 * time.Second

const (
	setupStatusPass       = "pass"
	setupStatusFail       = "fail"
	setupStatusNeedsInput = "needs_input"
	setupStatusSkipped    = "skipped"
	setupStatusDryRun     = "dry_run"
	setupStatusPending    = "pending"
)

type agentSetupFlags struct {
	Address         string
	Email           string
	SaveProfile     string
	Storage         string
	WebhookID       string
	WebhookURL      string
	WebhookName     string
	EventPreset     string
	Headers         []string
	AuthBearer      string
	WaitDelivery    time.Duration
	SkipWebhookTest bool
	SkipTestEmail   bool
	MarkTestDone    bool
	IdempotencyKey  string
	DryRun          bool
	Strict          bool
}

type setupReport struct {
	SchemaVersion string                `json:"schema_version"`
	Status        string                `json:"status"`
	OK            bool                  `json:"ok"`
	StageOrder    []string              `json:"stage_order"`
	Stages        map[string]setupStage `json:"stages"`
	Artifacts     map[string]any        `json:"artifacts,omitempty"`
	NextSteps     []setupNextStep       `json:"next_steps,omitempty"`
}

type setupStage struct {
	Status    string          `json:"status"`
	Code      string          `json:"code,omitempty"`
	Message   string          `json:"message"`
	Details   map[string]any  `json:"details,omitempty"`
	NextSteps []setupNextStep `json:"next_steps,omitempty"`
}

type setupNextStep struct {
	Command       string `json:"command,omitempty"`
	Description   string `json:"description"`
	HumanRequired bool   `json:"human_required,omitempty"`
}

type setupPlannedRequest struct {
	Method         string            `json:"method"`
	Path           string            `json:"path"`
	Body           any               `json:"body,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
	Reason         string            `json:"reason"`
}

type setupHTTPResult struct {
	StatusCode int
	Body       map[string]any
	RawBody    string
}

func newAgentSetupCmd() *cobra.Command {
	var f agentSetupFlags
	c := &cobra.Command{
		Use:   "agent-setup",
		Short: "Run a deterministic guided setup check for this agent mailbox",
		Long: `Runs the MonsterMailbox setup loop as a single JSON report.

The command is non-interactive. It checks local CLI/profile/auth state, makes
the human owner claim/adoption boundary explicit, validates the agent command
surface and bundled setup skill, configures or verifies an inbox.new webhook,
fires a synthetic webhook test, creates a safe real inbox test message, fetches
that message with mmb msg get <id> --peek semantics, and prints a final
pass/fail/needs_input summary.

It never tries to bypass the human claim/adoption step. If the agent is not
authenticated, unclaimed, or unadopted, the JSON report gives exact next
commands for the human/agent pair instead of prompting.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runAgentSetup(cmd, f)
		},
	}
	c.Flags().StringVar(&f.Address, "address", "", "desired local-part for first setup; used only for actionable auth-login next steps")
	c.Flags().StringVar(&f.Email, "email", "", "human owner email; used only for actionable auth-login next steps")
	c.Flags().StringVar(&f.SaveProfile, "save-profile", "", "profile name to suggest when saving a new or existing key")
	c.Flags().StringVar(&f.Storage, "storage", "", storageFlagDescription)
	c.Flags().StringVar(&f.WebhookID, "webhook-id", "", "existing webhook id to verify and test")
	c.Flags().StringVar(&f.WebhookURL, "webhook-url", "", "receiver URL for creating a recommended setup webhook")
	c.Flags().StringVar(&f.WebhookName, "webhook-name", "agent-setup", "name for a webhook created from --webhook-url")
	c.Flags().StringVar(&f.EventPreset, "event-preset", "trusted-inbox", "named event set for a created webhook: trusted-inbox, quarantine-aware-inbox, full-inbound-lifecycle")
	c.Flags().StringArrayVar(&f.Headers, "header", nil, "delivery header for a created webhook, as 'Name: value' (repeatable)")
	c.Flags().StringVar(&f.AuthBearer, "auth-bearer", "", "set delivery Authorization: Bearer <token> on a created webhook")
	c.Flags().DurationVar(&f.WaitDelivery, "wait-delivery", defaultAgentSetupWaitDelivery, "bounded time to poll webhook deliveries for the synthetic test event (0s = do not poll; final status stays pending)")
	c.Flags().BoolVar(&f.SkipWebhookTest, "skip-webhook-test", false, "skip firing the synthetic webhook test")
	c.Flags().BoolVar(&f.SkipTestEmail, "skip-test-email", false, "skip creating and fetching the real synthetic inbox test message")
	c.Flags().BoolVar(&f.MarkTestDone, "mark-test-done", false, "after fetching the synthetic test message, claim and mark only that message done")
	c.Flags().StringVar(&f.IdempotencyKey, "idempotency-key", "", "base token for setup mutations; the command appends operation-specific suffixes")
	c.Flags().BoolVar(&f.DryRun, "dry-run", false, "print the setup actions that would run, then exit (no HTTP calls)")
	c.Flags().BoolVar(&f.Strict, "strict", false, "return non-zero when the final setup status is not pass")
	return c
}

func runAgentSetup(cmd *cobra.Command, f agentSetupFlags) error {
	if f.Email != "" {
		if err := validateHumanOwnerEmail(f.Email); err != nil {
			return err
		}
	}
	if f.Storage != "" {
		if _, err := resolveStorage(f.Storage); err != nil {
			return err
		}
	}
	if f.WebhookID != "" && f.WebhookURL != "" {
		return fmt.Errorf("--webhook-id and --webhook-url are mutually exclusive")
	}

	cli := newAPIClient()
	report := newSetupReport()
	report.Artifacts["api_url"] = cli.BaseURL
	report.Artifacts["credential_source"] = nilIfEmpty(cli.Source)
	report.Artifacts["api_key"] = cli.MaskedAPIKey()
	if rootProfile != "" {
		report.Artifacts["requested_profile"] = rootProfile
	}
	if profile, p, ok := resolvedProfile(cli.Source); ok {
		report.Artifacts["profile"] = profile
		if p.AgentAddress != "" {
			report.Artifacts["agent_address"] = p.AgentAddress
		}
		if p.OwnerEmail != "" {
			report.Artifacts["owner_email"] = p.OwnerEmail
		}
	}

	report.setStage("cli_version", setupStage{
		Status:  setupStatusPass,
		Message: "mmb CLI is available and the API target is resolved.",
		Details: map[string]any{
			"cli_version": Version,
			"api_url":     cli.BaseURL,
		},
	})

	report.setStage("agent_context", buildAgentContextSetupStage(cmd.Root()))
	report.setStage("skill", buildSkillSetupStage())

	if f.DryRun {
		runAgentSetupDryRun(report, f)
		report.finish(true)
		if err := printJSON(cmd.OutOrStdout(), report); err != nil {
			return err
		}
		return strictAgentSetupError(f, report)
	}

	if stage := probeServerVersion(cli); stage.Status != "" {
		report.setStage("cli_version", mergeStage(report.Stages["cli_version"], stage))
	}

	authReady, humanReady := probeAgentSetupAuth(report, cli, f)
	if !authReady || !humanReady {
		setDependentSetupStages(report, "profile_auth and human_claim must pass before webhook and inbox workflow setup can run")
		report.finish(false)
		if err := printJSON(cmd.OutOrStdout(), report); err != nil {
			return err
		}
		return strictAgentSetupError(f, report)
	}

	webhookID := runAgentSetupWebhookConfig(report, cli, f)
	webhookReady := report.Stages["webhook_config"].Status == setupStatusPass && webhookID != ""

	if f.SkipWebhookTest {
		report.setStage("webhook_synthetic_test", setupStage{
			Status:  setupStatusSkipped,
			Message: "Synthetic webhook test skipped by --skip-webhook-test.",
		})
	} else if webhookReady {
		runAgentSetupWebhookTest(report, cli, webhookID, f)
	} else {
		report.setStage("webhook_synthetic_test", setupStage{
			Status:  setupStatusSkipped,
			Code:    "webhook_not_ready",
			Message: "Synthetic webhook test needs a verified webhook first.",
		})
	}

	if f.SkipTestEmail {
		report.setStage("test_email_send", setupStage{
			Status:  setupStatusSkipped,
			Message: "Real inbox test email skipped by --skip-test-email.",
		})
		report.setStage("message_fetch", setupStage{
			Status:  setupStatusSkipped,
			Message: "Message fetch skipped because --skip-test-email was set.",
		})
	} else if webhookReady || f.SkipWebhookTest {
		messageID := runAgentSetupTestEmail(report, cli, f, webhookID)
		if messageID != "" {
			messageOK := runAgentSetupMessageFetch(report, cli, messageID)
			if f.MarkTestDone {
				runAgentSetupMarkTestDone(report, cli, messageID, messageOK, f)
			} else {
				report.setStage("test_message_work_state", setupStage{
					Status:  setupStatusSkipped,
					Message: "Synthetic test message work-state mutation skipped; pass --mark-test-done to claim and mark it done.",
				})
			}
		} else {
			report.setStage("message_fetch", setupStage{
				Status:  setupStatusSkipped,
				Code:    "test_email_not_created",
				Message: "Message fetch needs a test_email_send message_id first.",
			})
			report.setStage("test_message_work_state", setupStage{
				Status:  setupStatusSkipped,
				Code:    "test_email_not_created",
				Message: "Work-state handling needs a fetched synthetic test message first.",
			})
		}
	} else {
		report.setStage("test_email_send", setupStage{
			Status:  setupStatusSkipped,
			Code:    "webhook_not_ready",
			Message: "Real inbox test email was not created because webhook setup is not ready.",
		})
		report.setStage("message_fetch", setupStage{
			Status:  setupStatusSkipped,
			Code:    "test_email_not_created",
			Message: "Message fetch needs a test_email_send message_id first.",
		})
		report.setStage("test_message_work_state", setupStage{
			Status:  setupStatusSkipped,
			Code:    "test_email_not_created",
			Message: "Work-state handling needs a fetched synthetic test message first.",
		})
	}

	report.finish(false)
	if err := printJSON(cmd.OutOrStdout(), report); err != nil {
		return err
	}
	return strictAgentSetupError(f, report)
}

func newSetupReport() *setupReport {
	return &setupReport{
		SchemaVersion: AgentSetupSchemaVersion,
		Status:        setupStatusPending,
		StageOrder:    []string{},
		Stages:        map[string]setupStage{},
		Artifacts:     map[string]any{},
	}
}

func (r *setupReport) setStage(name string, stage setupStage) {
	if _, exists := r.Stages[name]; !exists {
		r.StageOrder = append(r.StageOrder, name)
	}
	r.Stages[name] = stage
	for _, step := range stage.NextSteps {
		r.addNextStep(step)
	}
}

func (r *setupReport) addNextStep(step setupNextStep) {
	if step.Description == "" && step.Command == "" {
		return
	}
	key := step.Description + "\x00" + step.Command
	for _, existing := range r.NextSteps {
		if existing.Description+"\x00"+existing.Command == key {
			return
		}
	}
	r.NextSteps = append(r.NextSteps, step)
}

func (r *setupReport) finish(dryRun bool) {
	status := setupStatusPass
	skippedEssentialStages := []string{}
	if dryRun {
		status = setupStatusDryRun
	}
	for _, name := range r.StageOrder {
		if name == "final_result" {
			continue
		}
		switch r.Stages[name].Status {
		case setupStatusFail:
			status = setupStatusFail
		case setupStatusNeedsInput:
			if status != setupStatusFail && !dryRun {
				status = setupStatusNeedsInput
			}
		case setupStatusPending:
			if status != setupStatusFail && status != setupStatusNeedsInput && !dryRun {
				status = setupStatusPending
			}
		case setupStatusSkipped:
			if essentialSetupStage(name) && !dryRun {
				skippedEssentialStages = append(skippedEssentialStages, name)
				if status == setupStatusPass {
					status = setupStatusPending
				}
			}
		}
	}
	r.Status = status
	r.OK = status == setupStatusPass
	message := "MonsterMailbox setup verification passed."
	if status == setupStatusDryRun {
		message = "Dry run complete; no HTTP calls were made."
	} else if status == setupStatusNeedsInput {
		message = "MonsterMailbox setup needs human or configuration input before it can pass."
	} else if status == setupStatusPending {
		message = "MonsterMailbox setup has pending asynchronous verification."
	} else if status == setupStatusFail {
		message = "MonsterMailbox setup verification failed; see failing stages and next_steps."
	}
	details := map[string]any{
		"ok": r.OK,
	}
	if len(skippedEssentialStages) > 0 {
		details["skipped_essential_stages"] = skippedEssentialStages
	}
	r.setStage("final_result", setupStage{
		Status:    status,
		Message:   message,
		Details:   details,
		NextSteps: r.NextSteps,
	})
}

func essentialSetupStage(name string) bool {
	switch name {
	case "webhook_synthetic_test", "test_email_send", "message_fetch":
		return true
	default:
		return false
	}
}

func strictAgentSetupError(f agentSetupFlags, report *setupReport) error {
	if f.Strict && !report.OK {
		return fmt.Errorf("agent setup status is %s", report.Status)
	}
	return nil
}

func probeServerVersion(cli *client.Client) setupStage {
	resp, err := setupJSONRequest(cli, http.MethodGet, "/version", nil, nil, nil)
	if err != nil {
		return setupStage{
			Status:  setupStatusFail,
			Code:    "api_unreachable",
			Message: "Could not reach the MonsterMailbox API version endpoint.",
			Details: map[string]any{"error": err.Error()},
			NextSteps: []setupNextStep{{
				Description: "Check MONSTERMAILBOX_API_URL or the saved profile API URL, then rerun mmb agent-setup.",
			}},
		}
	}
	details := map[string]any{
		"server_status": resp.StatusCode,
	}
	if len(resp.Body) > 0 {
		details["server_version"] = resp.Body
	} else if resp.RawBody != "" {
		details["server_body"] = resp.RawBody
	}
	if resp.StatusCode >= 400 {
		return setupStage{
			Status:  setupStatusFail,
			Code:    "api_version_failed",
			Message: "The API version endpoint returned an error.",
			Details: details,
			NextSteps: []setupNextStep{{
				Description: "Confirm the configured API URL points at the MonsterMailbox API, not the marketing site.",
			}},
		}
	}
	return setupStage{
		Status:  setupStatusPass,
		Message: "mmb CLI is available and the API version endpoint is reachable.",
		Details: details,
	}
}

func probeAgentSetupAuth(report *setupReport, cli *client.Client, f agentSetupFlags) (bool, bool) {
	authDetails := map[string]any{
		"api_url":     cli.BaseURL,
		"api_key":     cli.MaskedAPIKey(),
		"source":      nilIfEmpty(cli.Source),
		"backend":     nilIfEmpty(cli.Backend),
		"cli_version": Version,
	}
	if rootProfile != "" {
		authDetails["requested_profile"] = rootProfile
	}

	if cli.APIKey == "" {
		report.setStage("profile_auth", setupStage{
			Status:    setupStatusNeedsInput,
			Code:      "missing_auth",
			Message:   "No MonsterMailbox API key is available from --profile, environment, or the default config profile.",
			Details:   authDetails,
			NextSteps: authSetupNextSteps(f),
		})
		report.setStage("human_claim", setupStage{
			Status:  setupStatusNeedsInput,
			Code:    "human_claim_unknown",
			Message: "The human owner must complete the claim/adoption email before inbox, outbound, webhook, or work-state setup can pass.",
			NextSteps: []setupNextStep{{
				Description:   "Have the human owner open the MonsterMailbox claim/adoption email, finish account setup, and adopt the agent mailbox.",
				HumanRequired: true,
			}},
		})
		return false, false
	}

	q := url.Values{}
	q.Set("limit", "1")
	resp, err := setupJSONRequest(cli, http.MethodGet, "/webhooks", nil, q, nil)
	if err != nil {
		report.setStage("profile_auth", setupStage{
			Status:  setupStatusFail,
			Code:    "auth_probe_failed",
			Message: "Could not complete the authenticated setup probe.",
			Details: map[string]any{"error": err.Error()},
		})
		report.setStage("human_claim", setupStage{
			Status:  setupStatusSkipped,
			Message: "Human claim/adoption check skipped because the authenticated probe failed.",
		})
		return false, false
	}

	if resp.StatusCode == http.StatusOK {
		authDetails["probe"] = "GET /webhooks?limit=1"
		report.setStage("profile_auth", setupStage{
			Status:  setupStatusPass,
			Message: "Authenticated profile/API key is available and accepted.",
			Details: authDetails,
		})
		report.setStage("human_claim", setupStage{
			Status:  setupStatusPass,
			Message: "The authenticated setup probe passed, so the owner claim/adoption gate is satisfied for API email access.",
		})
		return true, true
	}

	apiCode := stringField(resp.Body, "error")
	reason := stringField(resp.Body, "reason")
	if apiCode == "agent_pending_adoption" {
		report.setStage("profile_auth", setupStage{
			Status:  setupStatusPass,
			Message: "An API key is present and recognized, but the agent is not ready for email APIs yet.",
			Details: authDetails,
		})
		report.setStage("human_claim", setupStage{
			Status:  setupStatusNeedsInput,
			Code:    reasonOrDefault(reason, apiCode),
			Message: "Human owner claim/adoption must be completed before setup verification can continue.",
			Details: map[string]any{
				"server_status": resp.StatusCode,
				"server_error":  resp.Body,
			},
			NextSteps: []setupNextStep{{
				Description:   "Have the human owner open the claim/adoption email, finish account setup, adopt this agent mailbox, then rerun mmb agent-setup.",
				HumanRequired: true,
			}},
		})
		return true, false
	}

	stage := apiFailureStage("Authenticated setup probe failed.", resp, authSetupNextSteps(f))
	if apiCode == "scope_missing" {
		stage.Message = "The API key is missing a scope required for setup verification."
	}
	report.setStage("profile_auth", stage)
	report.setStage("human_claim", setupStage{
		Status:  setupStatusSkipped,
		Message: "Human claim/adoption check skipped because the authenticated setup probe did not pass.",
	})
	return false, false
}

func setDependentSetupStages(report *setupReport, message string) {
	for _, name := range []string{"webhook_config", "webhook_synthetic_test", "test_email_send", "message_fetch", "test_message_work_state"} {
		report.setStage(name, setupStage{
			Status:  setupStatusSkipped,
			Code:    "setup_preflight_not_ready",
			Message: message,
		})
	}
}

func runAgentSetupWebhookConfig(report *setupReport, cli *client.Client, f agentSetupFlags) string {
	if f.WebhookID == "" && f.WebhookURL == "" {
		stage := setupStage{
			Status:  setupStatusNeedsInput,
			Code:    "missing_webhook",
			Message: "Provide an existing webhook id or a receiver URL so setup can verify webhook delivery.",
			NextSteps: []setupNextStep{{
				Command:     "mmb agent-setup --webhook-url https://your-receiver.example.com/mmb",
				Description: "Create a trusted-inbox setup webhook and run the setup verification loop.",
			}, {
				Command:     "mmb agent-setup --webhook-id <webhook_id>",
				Description: "Use an existing webhook and run the setup verification loop.",
			}},
		}
		report.setStage("webhook_config", stage)
		return ""
	}

	if f.WebhookID != "" {
		path := "/webhooks/" + f.WebhookID
		resp, err := setupJSONRequest(cli, http.MethodGet, path, nil, nil, nil)
		if err != nil {
			report.setStage("webhook_config", setupStage{
				Status:  setupStatusFail,
				Code:    "webhook_probe_failed",
				Message: "Could not fetch the configured webhook.",
				Details: map[string]any{"error": err.Error(), "path": path},
			})
			return ""
		}
		if resp.StatusCode >= 400 {
			report.setStage("webhook_config", apiFailureStage("Could not verify the configured webhook.", resp, []setupNextStep{{
				Command:     "mmb webhook list",
				Description: "List available webhooks and rerun setup with a valid --webhook-id.",
			}}))
			return ""
		}
		if !webhookActive(resp.Body) {
			report.setStage("webhook_config", setupStage{
				Status:  setupStatusFail,
				Code:    "webhook_inactive",
				Message: "The configured webhook is inactive.",
				Details: map[string]any{"webhook": redactedWebhookDetails(resp.Body)},
				NextSteps: []setupNextStep{{
					Command:     "mmb webhook update " + f.WebhookID + " --active=true",
					Description: "Re-enable the webhook, then rerun mmb agent-setup.",
				}},
			})
			return ""
		}
		if !webhookHasInboxNew(resp.Body) {
			report.setStage("webhook_config", setupStage{
				Status:  setupStatusFail,
				Code:    "webhook_missing_inbox_new",
				Message: "The configured webhook is not subscribed to inbox.new.",
				Details: map[string]any{"webhook": redactedWebhookDetails(resp.Body)},
				NextSteps: []setupNextStep{{
					Command:     "mmb webhook update " + f.WebhookID + " --event-preset trusted-inbox",
					Description: "Subscribe the webhook to the trusted inbox event, then rerun mmb agent-setup.",
				}},
			})
			return ""
		}
		report.Artifacts["webhook_id"] = f.WebhookID
		report.setStage("webhook_config", setupStage{
			Status:  setupStatusPass,
			Message: "Existing webhook is active and subscribed to inbox.new.",
			Details: map[string]any{"webhook": redactedWebhookDetails(resp.Body)},
		})
		return f.WebhookID
	}

	wf := webhookMutationFlags{
		Name:        f.WebhookName,
		URL:         f.WebhookURL,
		EventPreset: f.EventPreset,
		Headers:     f.Headers,
		AuthBearer:  f.AuthBearer,
	}
	body, safeBody, err := agentSetupWebhookCreateBody(wf)
	if err != nil {
		report.setStage("webhook_config", setupStage{
			Status:  setupStatusFail,
			Code:    "invalid_webhook_flags",
			Message: err.Error(),
		})
		return ""
	}
	headers := setupIdempotencyHeaders(f, "webhook-create")
	resp, err := setupJSONRequest(cli, http.MethodPost, "/webhooks", body, nil, headers)
	if err != nil {
		report.setStage("webhook_config", setupStage{
			Status:  setupStatusFail,
			Code:    "webhook_create_failed",
			Message: "Could not create the setup webhook.",
			Details: map[string]any{"error": err.Error(), "request": setupPlannedRequest{
				Method:         http.MethodPost,
				Path:           "/webhooks",
				Body:           safeBody,
				Headers:        headers,
				IdempotencyKey: headers["Idempotency-Key"],
				Reason:         "create setup webhook",
			}},
		})
		return ""
	}
	if resp.StatusCode >= 400 {
		stage := apiFailureStage("Could not create the setup webhook.", resp, []setupNextStep{{
			Command:     "mmb webhook create --name " + shellArg(f.WebhookName) + " --url " + shellArg(f.WebhookURL) + " --event-preset " + shellArg(f.EventPreset),
			Description: "Create or debug the webhook directly, then rerun mmb agent-setup with --webhook-id.",
		}})
		stage.Details["request"] = setupPlannedRequest{
			Method:         http.MethodPost,
			Path:           "/webhooks",
			Body:           safeBody,
			Headers:        headers,
			IdempotencyKey: headers["Idempotency-Key"],
			Reason:         "create setup webhook",
		}
		report.setStage("webhook_config", stage)
		return ""
	}
	webhookID := stringField(resp.Body, "id")
	if webhookID == "" {
		report.setStage("webhook_config", setupStage{
			Status:  setupStatusFail,
			Code:    "webhook_create_missing_id",
			Message: "Webhook create succeeded but did not return an id.",
			Details: map[string]any{"response": redactedWebhookDetails(resp.Body)},
		})
		return ""
	}
	report.Artifacts["webhook_id"] = webhookID
	if secret := stringField(resp.Body, "secret"); secret != "" {
		report.Artifacts["webhook_secret"] = map[string]any{
			"value":     secret,
			"sensitive": true,
			"warning":   "copy now; the plaintext webhook signing secret is returned only once",
		}
	}
	report.setStage("webhook_config", setupStage{
		Status:  setupStatusPass,
		Message: "Created setup webhook with the recommended event preset.",
		Details: map[string]any{
			"request": setupPlannedRequest{
				Method:         http.MethodPost,
				Path:           "/webhooks",
				Body:           safeBody,
				Headers:        headers,
				IdempotencyKey: headers["Idempotency-Key"],
				Reason:         "create setup webhook",
			},
			"webhook": redactedWebhookDetails(resp.Body),
		},
	})
	return webhookID
}

func runAgentSetupWebhookTest(report *setupReport, cli *client.Client, webhookID string, f agentSetupFlags) {
	path := "/webhooks/" + webhookID + "/test_deliveries"
	headers := setupIdempotencyHeaders(f, "webhook-test")
	resp, err := setupJSONRequest(cli, http.MethodPost, path, nil, nil, headers)
	if err != nil {
		report.setStage("webhook_synthetic_test", setupStage{
			Status:  setupStatusFail,
			Code:    "webhook_test_failed",
			Message: "Could not fire the synthetic webhook test.",
			Details: map[string]any{"error": err.Error(), "path": path},
		})
		return
	}
	if resp.StatusCode >= 400 {
		report.setStage("webhook_synthetic_test", apiFailureStage("Synthetic webhook test failed.", resp, nil))
		return
	}
	eventID := stringField(resp.Body, "event_id")
	details := map[string]any{
		"response": resp.Body,
		"request": setupPlannedRequest{
			Method:         http.MethodPost,
			Path:           path,
			Headers:        headers,
			IdempotencyKey: headers["Idempotency-Key"],
			Reason:         "fire synthetic webhook test",
		},
	}
	stage := setupStage{
		Status:  setupStatusPass,
		Message: "Synthetic webhook test was accepted.",
		Details: details,
		NextSteps: []setupNextStep{{
			Command:     "mmb webhook deliveries " + webhookID + " --limit 20",
			Description: "Check recent deliveries if the receiver did not observe the synthetic webhook.",
		}},
	}
	if eventID == "" {
		stage.Status = setupStatusFail
		stage.Code = "webhook_test_missing_event_id"
		stage.Message = "Synthetic webhook test was accepted but did not return an event_id for delivery verification."
		report.setStage("webhook_synthetic_test", stage)
		return
	}
	if f.WaitDelivery <= 0 {
		stage.Status = setupStatusPending
		stage.Code = "webhook_delivery_not_confirmed"
		stage.Message = "Synthetic webhook test was accepted, but delivery confirmation was skipped by --wait-delivery=0s."
		stage.NextSteps = append(stage.NextSteps, setupNextStep{
			Command:     "mmb agent-setup --webhook-id " + webhookID + " --wait-delivery 15s",
			Description: "Rerun setup with bounded delivery polling so final pass proves the receiver accepted the signed webhook.",
		})
	} else {
		status, pollDetails := pollWebhookDelivery(cli, webhookID, eventID, f.WaitDelivery)
		details["delivery_poll"] = pollDetails
		switch status {
		case "succeeded":
			stage.Message = "Synthetic webhook test was accepted and delivery succeeded."
		case "failed", "gave_up":
			stage.Status = setupStatusFail
			stage.Code = "webhook_delivery_" + status
			stage.Message = "Synthetic webhook delivery reached a terminal failure state."
		default:
			stage.Status = setupStatusPending
			stage.Code = "webhook_delivery_pending"
			stage.Message = "Synthetic webhook delivery has not reached a terminal state within --wait-delivery."
		}
	}
	report.setStage("webhook_synthetic_test", stage)
}

func runAgentSetupTestEmail(report *setupReport, cli *client.Client, f agentSetupFlags, webhookID string) string {
	headers := setupIdempotencyHeaders(f, "test-email")
	resp, err := setupJSONRequest(cli, http.MethodPost, "/test_emails", nil, nil, headers)
	if err != nil {
		report.setStage("test_email_send", setupStage{
			Status:  setupStatusFail,
			Code:    "test_email_send_failed",
			Message: "Could not create the safe synthetic inbox test message.",
			Details: map[string]any{"error": err.Error()},
		})
		return ""
	}
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed {
		report.setStage("test_email_send", setupStage{
			Status:  setupStatusFail,
			Code:    "missing_backend_capability",
			Message: "The API does not expose POST /test_emails yet.",
			Details: map[string]any{
				"server_status": resp.StatusCode,
				"server_error":  resp.Body,
			},
			NextSteps: []setupNextStep{{
				Command:     "mmb update",
				Description: "Update the CLI, confirm the backend is deployed with /test_emails, then rerun mmb agent-setup.",
			}},
		})
		return ""
	}
	if resp.StatusCode >= 400 {
		report.setStage("test_email_send", apiFailureStage("Safe synthetic inbox test message creation failed.", resp, nil))
		return ""
	}
	messageID := stringField(resp.Body, "message_id")
	if messageID == "" {
		report.setStage("test_email_send", setupStage{
			Status:  setupStatusFail,
			Code:    "test_email_missing_message_id",
			Message: "POST /test_emails succeeded but did not return message_id.",
			Details: map[string]any{"response": resp.Body},
		})
		return ""
	}
	report.Artifacts["test_message_id"] = messageID
	eventID := stringField(resp.Body, "event_id")
	if eventID != "" {
		report.Artifacts["test_email_event_id"] = eventID
		report.Artifacts["webhook_receipt_hint"] = "wait for webhook event inbox.new with event_id=" + eventID + " and data.test=true"
	}
	nextSteps := []setupNextStep{{
		Command:     "mmb msg get " + messageID + " --peek",
		Description: "Fetch the synthetic test message without marking it read.",
	}}
	if eventID != "" {
		nextSteps = append(nextSteps, setupNextStep{
			Description: "Wait for the receiver to observe webhook event inbox.new with event_id=" + eventID + " and data.test=true.",
		})
		if webhookID != "" {
			nextSteps = append(nextSteps, setupNextStep{
				Command:     "mmb webhook deliveries " + webhookID + " --limit 20",
				Description: "Inspect recent deliveries if the receiver did not observe the real inbox test email webhook.",
			})
		}
	}
	report.setStage("test_email_send", setupStage{
		Status:  setupStatusPass,
		Message: "Created a safe synthetic inbox test message.",
		Details: map[string]any{
			"response": resp.Body,
			"request": setupPlannedRequest{
				Method:         http.MethodPost,
				Path:           "/test_emails",
				Headers:        headers,
				IdempotencyKey: headers["Idempotency-Key"],
				Reason:         "create real synthetic inbox test email",
			},
		},
		NextSteps: nextSteps,
	})
	return messageID
}

func runAgentSetupMessageFetch(report *setupReport, cli *client.Client, messageID string) bool {
	q := url.Values{}
	q.Set("peek", "true")
	path := "/msg/" + messageID
	resp, err := setupJSONRequest(cli, http.MethodGet, path, nil, q, nil)
	if err != nil {
		report.setStage("message_fetch", setupStage{
			Status:  setupStatusFail,
			Code:    "message_fetch_failed",
			Message: "Could not fetch the synthetic test message.",
			Details: map[string]any{"error": err.Error(), "path": path},
		})
		return false
	}
	if resp.StatusCode >= 400 {
		report.setStage("message_fetch", apiFailureStage("Could not fetch the synthetic test message.", resp, []setupNextStep{{
			Command:     "mmb msg get " + messageID + " --peek",
			Description: "Retry fetching the synthetic test message.",
		}}))
		return false
	}

	mismatches := []string{}
	if source := firstStringField(resp.Body, "source", "message_source"); source != "" && source != "test_email" {
		mismatches = append(mismatches, "source="+source)
	}
	if state := stringField(resp.Body, "state"); state != "" && state != "trusted" {
		mismatches = append(mismatches, "state="+state)
	}
	if workState := stringField(resp.Body, "work_state"); workState != "" && workState != "inbox" {
		mismatches = append(mismatches, "work_state="+workState)
	}
	status := setupStatusPass
	code := ""
	message := "Fetched the synthetic test message with peek=true."
	if len(mismatches) > 0 {
		status = setupStatusFail
		code = "test_message_shape_mismatch"
		message = "Fetched message did not match the expected synthetic test message shape."
	}
	report.setStage("message_fetch", setupStage{
		Status:  status,
		Code:    code,
		Message: message,
		Details: map[string]any{
			"message":    resp.Body,
			"mismatches": mismatches,
			"request": setupPlannedRequest{
				Method: http.MethodGet,
				Path:   path + "?peek=true",
				Reason: "fetch synthetic test message without marking read",
			},
		},
	})
	return status == setupStatusPass
}

func runAgentSetupMarkTestDone(report *setupReport, cli *client.Client, messageID string, messageOK bool, f agentSetupFlags) {
	if !messageOK {
		report.setStage("test_message_work_state", setupStage{
			Status:  setupStatusSkipped,
			Code:    "message_fetch_not_verified",
			Message: "Work-state mutation skipped because the fetched message was not verified as the synthetic test message.",
		})
		return
	}
	claimBody := map[string]any{
		"state":                  "in_progress",
		"expected_current_state": "inbox",
		"note":                   "agent-setup synthetic test message verification",
		"claimed_by":             "mmb agent-setup",
	}
	claimHeaders := setupIdempotencyHeaders(f, "test-message-claim")
	claimPath := "/msg/" + messageID + "/work_state"
	claimResp, err := setupJSONRequest(cli, http.MethodPatch, claimPath, claimBody, nil, claimHeaders)
	if err != nil {
		report.setStage("test_message_work_state", setupStage{
			Status:  setupStatusFail,
			Code:    "test_message_claim_failed",
			Message: "Could not claim the synthetic test message.",
			Details: map[string]any{"error": err.Error()},
		})
		return
	}
	if claimResp.StatusCode >= 400 {
		report.setStage("test_message_work_state", apiFailureStage("Could not claim the synthetic test message.", claimResp, nil))
		return
	}

	doneBody := map[string]any{
		"state": "done",
		"note":  "agent-setup synthetic test message handled successfully",
	}
	doneHeaders := setupIdempotencyHeaders(f, "test-message-done")
	doneResp, err := setupJSONRequest(cli, http.MethodPatch, claimPath, doneBody, nil, doneHeaders)
	if err != nil {
		report.setStage("test_message_work_state", setupStage{
			Status:  setupStatusFail,
			Code:    "test_message_done_failed",
			Message: "Could not mark the synthetic test message done.",
			Details: map[string]any{"error": err.Error()},
		})
		return
	}
	if doneResp.StatusCode >= 400 {
		report.setStage("test_message_work_state", apiFailureStage("Could not mark the synthetic test message done.", doneResp, nil))
		return
	}
	report.setStage("test_message_work_state", setupStage{
		Status:  setupStatusPass,
		Message: "Claimed and marked the verified synthetic test message done.",
		Details: map[string]any{
			"claim_response": claimResp.Body,
			"done_response":  doneResp.Body,
			"requests": []setupPlannedRequest{{
				Method:         http.MethodPatch,
				Path:           claimPath,
				Body:           claimBody,
				Headers:        claimHeaders,
				IdempotencyKey: claimHeaders["Idempotency-Key"],
				Reason:         "claim synthetic test message",
			}, {
				Method:         http.MethodPatch,
				Path:           claimPath,
				Body:           doneBody,
				Headers:        doneHeaders,
				IdempotencyKey: doneHeaders["Idempotency-Key"],
				Reason:         "mark synthetic test message done",
			}},
		},
	})
}

func runAgentSetupDryRun(report *setupReport, f agentSetupFlags) {
	authMessage := "Dry run resolved local auth metadata but did not call authenticated endpoints."
	if f.Address != "" || f.Email != "" || f.SaveProfile != "" {
		authMessage = "Dry run captured the auth setup inputs but did not register or save credentials."
	}
	report.setStage("profile_auth", setupStage{
		Status:    setupStatusDryRun,
		Code:      "dry_run_auth_not_verified",
		Message:   authMessage,
		NextSteps: authSetupNextSteps(f),
	})
	report.setStage("human_claim", setupStage{
		Status:  setupStatusNeedsInput,
		Code:    "human_claim_not_verified",
		Message: "Dry run cannot verify the human claim/adoption email. The human must complete that step before live setup can pass.",
		NextSteps: []setupNextStep{{
			Description:   "Have the human owner complete the claim/adoption email, then rerun without --dry-run.",
			HumanRequired: true,
		}},
	})

	planned := []setupPlannedRequest{}
	if f.WebhookURL != "" {
		wf := webhookMutationFlags{
			Name:        f.WebhookName,
			URL:         f.WebhookURL,
			EventPreset: f.EventPreset,
			Headers:     f.Headers,
			AuthBearer:  f.AuthBearer,
		}
		_, safeBody, err := agentSetupWebhookCreateBody(wf)
		if err != nil {
			report.setStage("webhook_config", setupStage{
				Status:  setupStatusFail,
				Code:    "invalid_webhook_flags",
				Message: err.Error(),
			})
		} else {
			headers := setupIdempotencyHeaders(f, "webhook-create")
			planned = append(planned, setupPlannedRequest{
				Method:         http.MethodPost,
				Path:           "/webhooks",
				Body:           safeBody,
				Headers:        headers,
				IdempotencyKey: headers["Idempotency-Key"],
				Reason:         "create setup webhook",
			})
			report.setStage("webhook_config", setupStage{
				Status:  setupStatusDryRun,
				Message: "Would create a setup webhook with the recommended event preset.",
				Details: map[string]any{"request": planned[len(planned)-1]},
			})
		}
	} else if f.WebhookID != "" {
		report.setStage("webhook_config", setupStage{
			Status:  setupStatusDryRun,
			Message: "Would verify the existing webhook before testing it.",
			Details: map[string]any{"request": setupPlannedRequest{
				Method: http.MethodGet,
				Path:   "/webhooks/" + f.WebhookID,
				Reason: "verify existing webhook",
			}},
		})
	} else {
		report.setStage("webhook_config", setupStage{
			Status:  setupStatusNeedsInput,
			Code:    "missing_webhook",
			Message: "Provide --webhook-url or --webhook-id for live setup verification.",
			NextSteps: []setupNextStep{{
				Command:     "mmb agent-setup --webhook-url https://your-receiver.example.com/mmb",
				Description: "Create a trusted-inbox setup webhook and run live verification.",
			}},
		})
	}

	if f.SkipWebhookTest {
		report.setStage("webhook_synthetic_test", setupStage{
			Status:  setupStatusSkipped,
			Message: "Would skip synthetic webhook test because --skip-webhook-test is set.",
		})
	} else if f.WebhookID != "" {
		headers := setupIdempotencyHeaders(f, "webhook-test")
		req := setupPlannedRequest{
			Method:         http.MethodPost,
			Path:           "/webhooks/" + f.WebhookID + "/test_deliveries",
			Headers:        headers,
			IdempotencyKey: headers["Idempotency-Key"],
			Reason:         "fire synthetic webhook test",
		}
		planned = append(planned, req)
		report.setStage("webhook_synthetic_test", setupStage{
			Status:  setupStatusDryRun,
			Message: "Would fire the synthetic webhook test.",
			Details: map[string]any{"request": req},
		})
	} else if f.WebhookURL != "" {
		report.setStage("webhook_synthetic_test", setupStage{
			Status:  setupStatusDryRun,
			Message: "Would fire the synthetic webhook test after the webhook create response returns an id.",
		})
	} else {
		report.setStage("webhook_synthetic_test", setupStage{
			Status:  setupStatusSkipped,
			Code:    "missing_webhook",
			Message: "Synthetic webhook test needs a webhook id.",
		})
	}

	if f.SkipTestEmail {
		report.setStage("test_email_send", setupStage{
			Status:  setupStatusSkipped,
			Message: "Would skip real inbox test email because --skip-test-email is set.",
		})
		report.setStage("message_fetch", setupStage{
			Status:  setupStatusSkipped,
			Message: "Message fetch skipped because --skip-test-email is set.",
		})
	} else {
		headers := setupIdempotencyHeaders(f, "test-email")
		req := setupPlannedRequest{
			Method:         http.MethodPost,
			Path:           "/test_emails",
			Headers:        headers,
			IdempotencyKey: headers["Idempotency-Key"],
			Reason:         "create real synthetic inbox test email",
		}
		planned = append(planned, req)
		report.setStage("test_email_send", setupStage{
			Status:  setupStatusDryRun,
			Message: "Would create a safe synthetic inbox test message.",
			Details: map[string]any{"request": req},
		})
		report.setStage("message_fetch", setupStage{
			Status:  setupStatusDryRun,
			Message: "Would fetch the returned message_id with peek=true.",
			Details: map[string]any{"request_template": setupPlannedRequest{
				Method: http.MethodGet,
				Path:   "/msg/{message_id_from_test_email}?peek=true",
				Reason: "fetch synthetic test message without marking read",
			}},
		})
	}

	if f.MarkTestDone {
		claimHeaders := setupIdempotencyHeaders(f, "test-message-claim")
		doneHeaders := setupIdempotencyHeaders(f, "test-message-done")
		reqs := []setupPlannedRequest{{
			Method:         http.MethodPatch,
			Path:           "/msg/{message_id_from_test_email}/work_state",
			Body:           map[string]any{"state": "in_progress", "expected_current_state": "inbox", "note": "agent-setup synthetic test message verification", "claimed_by": "mmb agent-setup"},
			Headers:        claimHeaders,
			IdempotencyKey: claimHeaders["Idempotency-Key"],
			Reason:         "claim synthetic test message",
		}, {
			Method:         http.MethodPatch,
			Path:           "/msg/{message_id_from_test_email}/work_state",
			Body:           map[string]any{"state": "done", "note": "agent-setup synthetic test message handled successfully"},
			Headers:        doneHeaders,
			IdempotencyKey: doneHeaders["Idempotency-Key"],
			Reason:         "mark synthetic test message done",
		}}
		planned = append(planned, reqs...)
		report.setStage("test_message_work_state", setupStage{
			Status:  setupStatusDryRun,
			Message: "Would claim and mark only the verified synthetic test message done.",
			Details: map[string]any{"requests": reqs},
		})
	} else {
		report.setStage("test_message_work_state", setupStage{
			Status:  setupStatusSkipped,
			Message: "Would leave the synthetic test message in the inbox work-state unless --mark-test-done is set.",
		})
	}
	report.Artifacts["planned_requests"] = planned
}

func buildAgentContextSetupStage(root *cobra.Command) setupStage {
	ctx := buildAgentContext(root)
	commands, _ := ctx["commands"].(map[string]any)
	_, hasAgentSetup := commands["agent-setup"]
	status := setupStatusPass
	code := ""
	message := "agent-context is available and includes the guided setup command."
	if !hasAgentSetup {
		status = setupStatusFail
		code = "agent_context_missing_agent_setup"
		message = "agent-context does not expose mmb agent-setup."
	}
	return setupStage{
		Status:  status,
		Code:    code,
		Message: message,
		Details: map[string]any{
			"schema_version":  ctx["schema_version"],
			"has_agent_setup": hasAgentSetup,
			"command_count":   len(commands),
		},
	}
}

func buildSkillSetupStage() setupStage {
	hasSetupLoop := strings.Contains(monsterMailboxSkillSample, "mmb agent-setup")
	hasHumanBoundary := strings.Contains(monsterMailboxSkillSample, "claim/adoption")
	status := setupStatusPass
	code := ""
	message := "Bundled MonsterMailbox setup skill is available and documents the guided setup loop."
	if !hasSetupLoop || !hasHumanBoundary {
		status = setupStatusFail
		code = "setup_skill_missing_guidance"
		message = "Bundled MonsterMailbox skill is missing setup-loop or human claim/adoption guidance."
	}
	return setupStage{
		Status:  status,
		Code:    code,
		Message: message,
		Details: map[string]any{
			"command":            sampleSkillCommand,
			"has_agent_setup":    hasSetupLoop,
			"has_human_boundary": hasHumanBoundary,
		},
		NextSteps: []setupNextStep{{
			Command:     sampleSkillCommand,
			Description: "Install or display the official MonsterMailbox setup skill, then replace HUMAN_OWNER_NAME before relying on routing rules.",
		}},
	}
}

func agentSetupWebhookCreateBody(wf webhookMutationFlags) (map[string]any, map[string]any, error) {
	if wf.Name == "" {
		return nil, nil, fmt.Errorf("--webhook-name is required when --webhook-url is set")
	}
	if wf.URL == "" {
		return nil, nil, fmt.Errorf("--webhook-url is required")
	}
	events, err := normalizeWebhookEvents(wf)
	if err != nil {
		return nil, nil, err
	}
	if len(events) == 0 {
		return nil, nil, fmt.Errorf("specify --event-preset or webhook events")
	}
	if err := validateWebhookEvents(events); err != nil {
		return nil, nil, err
	}
	headers, err := webhookHeadersFromFlags(wf)
	if err != nil {
		return nil, nil, err
	}
	body := map[string]any{
		"name":   wf.Name,
		"url":    wf.URL,
		"events": events,
	}
	if len(headers) > 0 {
		body["headers"] = headers
	}
	safeBody := map[string]any{
		"name":   wf.Name,
		"url":    wf.URL,
		"events": events,
	}
	if len(headers) > 0 {
		safeBody["headers"] = redactHeaderMap(headers)
	}
	return body, safeBody, nil
}

func setupJSONRequest(cli *client.Client, method, path string, body any, q url.Values, headers map[string]string) (setupHTTPResult, error) {
	resp, err := cli.DoWithHeaders(method, path, body, q, headers)
	if err != nil {
		return setupHTTPResult{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return setupHTTPResult{}, err
	}
	result := setupHTTPResult{
		StatusCode: resp.StatusCode,
		RawBody:    strings.TrimSpace(string(raw)),
	}
	if len(raw) > 0 {
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err == nil {
			result.Body = body
		}
	}
	return result, nil
}

func apiFailureStage(message string, resp setupHTTPResult, next []setupNextStep) setupStage {
	code := stringField(resp.Body, "error")
	if code == "" {
		code = fmt.Sprintf("http_%d", resp.StatusCode)
	}
	details := map[string]any{
		"server_status": resp.StatusCode,
	}
	if len(resp.Body) > 0 {
		details["server_error"] = resp.Body
	} else if resp.RawBody != "" {
		details["server_body"] = resp.RawBody
	}
	if code == "unauthorized" && len(next) == 0 {
		next = authSetupNextSteps(agentSetupFlags{})
	}
	if code == "agent_pending_adoption" {
		next = append(next, setupNextStep{
			Description:   "Have the human owner complete the claim/adoption email, adopt this agent mailbox, then rerun mmb agent-setup.",
			HumanRequired: true,
		})
	}
	if code == "scope_missing" {
		next = append(next, setupNextStep{
			Description: "Use an API key with the required setup scopes, or ask the human owner to issue one from the dashboard.",
		})
	}
	return setupStage{
		Status:    setupStatusFail,
		Code:      code,
		Message:   message,
		Details:   details,
		NextSteps: next,
	}
}

func pollWebhookDelivery(cli *client.Client, webhookID, eventID string, wait time.Duration) (string, map[string]any) {
	deadline := time.Now().Add(wait)
	path := "/webhooks/" + webhookID + "/deliveries"
	q := url.Values{}
	q.Set("limit", "20")
	var lastErr string
	polls := 0
	for {
		polls++
		resp, err := setupJSONRequest(cli, http.MethodGet, path, nil, q, nil)
		if err != nil {
			lastErr = err.Error()
		} else if resp.StatusCode >= 400 {
			lastErr = fmt.Sprintf("HTTP %d", resp.StatusCode)
		} else if delivery, ok := findDeliveryByEventID(resp.Body, eventID); ok {
			status := stringField(delivery, "status")
			return status, map[string]any{
				"polls":    polls,
				"event_id": eventID,
				"delivery": delivery,
			}
		}
		if time.Now().After(deadline) {
			return setupStatusPending, map[string]any{
				"polls":      polls,
				"event_id":   eventID,
				"last_error": nilIfEmpty(lastErr),
			}
		}
		time.Sleep(minDuration(500*time.Millisecond, time.Until(deadline)))
	}
}

func findDeliveryByEventID(body map[string]any, eventID string) (map[string]any, bool) {
	rows, _ := body["deliveries"].([]any)
	for _, row := range rows {
		m, ok := row.(map[string]any)
		if !ok {
			continue
		}
		if stringField(m, "event_id") == eventID {
			return m, true
		}
	}
	return nil, false
}

func authSetupNextSteps(f agentSetupFlags) []setupNextStep {
	address := placeholderOr(f.Address, "<local_part>")
	email := placeholderOr(f.Email, "<human_owner_email>")
	profile := placeholderOr(f.SaveProfile, "<profile>")
	login := "mmb auth login --address " + shellArg(address) + " --email " + shellArg(email)
	if f.SaveProfile != "" {
		login += " --profile " + shellArg(f.SaveProfile)
	}
	if f.Storage != "" {
		login += " --storage " + shellArg(f.Storage)
	}
	save := "mmb auth save --profile " + shellArg(profile) + " --api-key <api_key> --api-url https://api.monstermailbox.com --agent-address <agent@monstermailbox.com>"
	if f.Storage != "" {
		save += " --storage " + shellArg(f.Storage)
	}
	return []setupNextStep{{
		Command:       login,
		Description:   "Create a governed agent mailbox, save the returned key locally, and send the human owner claim/adoption email.",
		HumanRequired: true,
	}, {
		Description:   "The human owner must open the MonsterMailbox claim/adoption email, finish account setup, and adopt this agent mailbox.",
		HumanRequired: true,
	}, {
		Command:     save,
		Description: "If an API key already exists, save it as a local profile instead of creating a new mailbox.",
	}}
}

func setupIdempotencyHeaders(f agentSetupFlags, suffix string) map[string]string {
	if f.IdempotencyKey == "" {
		return nil
	}
	return map[string]string{"Idempotency-Key": f.IdempotencyKey + "-" + suffix}
}

func webhookActive(body map[string]any) bool {
	active, ok := body["active"].(bool)
	return !ok || active
}

func webhookHasInboxNew(body map[string]any) bool {
	events, _ := body["events"].([]any)
	for _, raw := range events {
		if raw == "inbox.new" || raw == "*" {
			return true
		}
	}
	return false
}

func redactedWebhookDetails(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		switch strings.ToLower(k) {
		case "secret":
			out[k] = "[ONE_TIME_SECRET_REDACTED]"
		case "headers", "delivery_headers":
			if headers, ok := v.(map[string]any); ok {
				redacted := map[string]any{}
				for hk := range headers {
					redacted[hk] = "[REDACTED]"
				}
				out[k] = redacted
			} else {
				out[k] = "[REDACTED]"
			}
		default:
			out[k] = v
		}
	}
	return out
}

func redactHeaderMap(headers map[string]string) map[string]string {
	out := map[string]string{}
	for k := range headers {
		out[k] = "[REDACTED]"
	}
	return out
}

func stringField(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	switch t := v.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	default:
		return fmt.Sprintf("%v", t)
	}
}

func firstStringField(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v := stringField(m, key); v != "" {
			return v
		}
	}
	return ""
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func reasonOrDefault(reason, fallback string) string {
	if reason != "" {
		return reason
	}
	return fallback
}

func placeholderOr(value, placeholder string) string {
	if strings.TrimSpace(value) == "" {
		return placeholder
	}
	return value
}

func shellArg(s string) string {
	if strings.HasPrefix(s, "<") && strings.HasSuffix(s, ">") {
		return s
	}
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, " \t\n\"'\\$`") {
		b, _ := json.Marshal(s)
		return string(b)
	}
	return s
}

func mergeStage(base, overlay setupStage) setupStage {
	if overlay.Status != "" {
		base.Status = overlay.Status
	}
	if overlay.Code != "" {
		base.Code = overlay.Code
	}
	if overlay.Message != "" {
		base.Message = overlay.Message
	}
	if base.Details == nil {
		base.Details = map[string]any{}
	}
	for k, v := range overlay.Details {
		base.Details[k] = v
	}
	if len(overlay.NextSteps) > 0 {
		base.NextSteps = append(base.NextSteps, overlay.NextSteps...)
	}
	return base
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
