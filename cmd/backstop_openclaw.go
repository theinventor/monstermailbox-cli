package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// openClawBackstopTurn renders the agent-turn message for the OpenClaw cron.
// OpenClaw cron jobs are always agent turns (no pre-run gate), so the turn runs
// in a fresh isolated session and exits fast on NO_REPLY when nothing is due.
func openClawBackstopTurn(mmbBin string) string {
	turn := backstopAgentTurn()
	if mmbBin != "" && mmbBin != "mmb" {
		turn = strings.ReplaceAll(turn, "mmb ", mmbBin+" ")
	}
	return turn
}

// openClawBackstopJob builds the cron job object for jobs.json. id/createdAtMs
// are passed in so an update preserves them.
func openClawBackstopJob(id string, createdAtMs, nowMs, everyMs int64, mmbBin string) map[string]any {
	return map[string]any{
		"id":            id,
		"agentId":       "main",
		"name":          openClawBackstopJobName,
		"description":   "MonsterMailbox inbox backstop: catches trusted mail the realtime watcher missed; re-queues undispositioned inbox + stale in_progress.",
		"enabled":       true,
		"createdAtMs":   createdAtMs,
		"schedule":      map[string]any{"kind": "every", "everyMs": everyMs, "anchorMs": nowMs},
		"sessionTarget": "isolated",
		"wakeMode":      "now",
		"payload": map[string]any{
			"kind":           "agentTurn",
			"message":        openClawBackstopTurn(mmbBin),
			"timeoutSeconds": 900,
			"lightContext":   true,
		},
		"delivery": map[string]any{"mode": "none"},
		"state":    map[string]any{},
	}
}

// installOpenClawBackstop merges the backstop job into <ocHome>/cron/jobs.json,
// idempotent by job name (an existing job keeps its id + createdAtMs). Writes a
// .bak of any existing file first.
func installOpenClawBackstop(w io.Writer, ocHome, mmbBin string, interval time.Duration) error {
	if mmbBin == "" {
		mmbBin = "mmb"
	}
	cronDir := filepath.Join(ocHome, "cron")
	if err := os.MkdirAll(cronDir, 0o755); err != nil {
		return fmt.Errorf("create cron dir: %w", err)
	}
	cronPath := filepath.Join(cronDir, "jobs.json")

	root := map[string]any{"version": float64(1), "jobs": []any{}}
	if raw, err := os.ReadFile(cronPath); err == nil {
		if err := os.WriteFile(cronPath+".bak", raw, 0o644); err != nil {
			return fmt.Errorf("write backup: %w", err)
		}
		if err := json.Unmarshal(raw, &root); err != nil {
			return fmt.Errorf("cron jobs.json is not valid JSON: %w", err)
		}
		if _, ok := root["jobs"].([]any); !ok {
			root["jobs"] = []any{}
		}
		if _, ok := root["version"]; !ok {
			root["version"] = float64(1)
		}
	}

	nowMs := time.Now().UnixMilli()
	everyMs := interval.Milliseconds()

	jobs, _ := root["jobs"].([]any)
	updated := false
	for i, raw := range jobs {
		job, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if name, _ := job["name"].(string); name == openClawBackstopJobName {
			id, _ := job["id"].(string)
			if id == "" {
				newID, err := newUUIDv4()
				if err != nil {
					return err
				}
				id = newID
			}
			createdAtMs := nowMs
			if c, ok := toInt64(job["createdAtMs"]); ok {
				createdAtMs = c
			}
			jobs[i] = openClawBackstopJob(id, createdAtMs, nowMs, everyMs, mmbBin)
			updated = true
			break
		}
	}
	if !updated {
		id, err := newUUIDv4()
		if err != nil {
			return err
		}
		jobs = append(jobs, openClawBackstopJob(id, nowMs, nowMs, everyMs, mmbBin))
	}
	root["jobs"] = jobs

	pretty, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	pretty = append(pretty, '\n')
	if err := os.WriteFile(cronPath, pretty, 0o644); err != nil {
		return err
	}
	if updated {
		fmt.Fprintf(w, "✓ updated backstop cron in %s (%s, isolated session)\n", cronPath, hermesScheduleExpr(interval))
	} else {
		fmt.Fprintf(w, "✓ added backstop cron to %s (%s, isolated session)\n", cronPath, hermesScheduleExpr(interval))
	}
	return nil
}

// toInt64 coerces a JSON number (float64) or integer to int64.
func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	}
	return 0, false
}
