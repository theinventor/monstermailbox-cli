package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// hermesGateScript renders the pre-run gate. It lists undispositioned trusted
// mail and prints {"wakeAgent": false} when there is none, so an empty inbox
// costs zero LLM tokens. mmbBin/mmbHome are pinned absolutely so the gate does
// not depend on the gateway's PATH/HOME.
func hermesGateScript(mmbBin, mmbHome string) string {
	tmpl := `#!/usr/bin/env bash
# MonsterMailbox inbox backstop gate (managed by ` + "`mmb hermes install`" + `).
#
# A message is "unhandled" iff it has no FINAL disposition. Read vs unread means
# nothing — the agent may have read it and crashed before acting. The gate wakes
# the agent for:
#   1. every work_state=inbox message (regardless of read state)
#   2. every work_state=in_progress older than STALE_MINUTES (claimed, then the
#      turn died); fresher in_progress is left alone (may be actively running).
# Emits {"wakeAgent": false} when nothing is undispositioned (zero-token pass).
set -euo pipefail

export HOME=__MMB_HOME__
export XDG_CONFIG_HOME=__MMB_HOME__/.config
export PATH=__MMB_DIR__:${PATH:-}
MMB=__MMB_BIN__
STALE_MINUTES=__STALE__

INBOX=$(mktemp); INPROG=$(mktemp)
trap 'rm -f "$INBOX" "$INPROG"' EXIT

# --all so read-but-undispositioned mail is included (read state is irrelevant).
"$MMB" inbox list --state trusted --work-state inbox       --all --limit 25 --peek > "$INBOX"
"$MMB" inbox list --state trusted --work-state in_progress --all --limit 25 --peek > "$INPROG"

STALE_MINUTES="$STALE_MINUTES" python3 - "$INBOX" "$INPROG" <<'PY'
import json, os, sys, datetime
from pathlib import Path

def load(path):
    raw = Path(path).read_text().lstrip()
    if not raw:
        return []
    try:
        data, _ = json.JSONDecoder().raw_decode(raw)
    except Exception as e:
        raise SystemExit(f'Could not parse mmb inbox JSON: {e}\n{raw[:500]}')
    return data.get('messages') or []

def parse_ts(s):
    if not s:
        return None
    try:
        return datetime.datetime.fromisoformat(s.replace('Z', '+00:00'))
    except Exception:
        return None

stale_min = int(os.environ.get('STALE_MINUTES', '60'))
now = datetime.datetime.now(datetime.timezone.utc)

def brief(m, reason):
    return {
        'id': m.get('id') or m.get('message_id'),
        'from': m.get('from') or m.get('sender'),
        'subject': m.get('subject'),
        'received_at': m.get('received_at'),
        'work_state': m.get('work_state'),
        'backstop_reason': reason,
    }

out = [brief(m, 'undispositioned') for m in load(sys.argv[1])]

for m in load(sys.argv[2]):
    changed = parse_ts(m.get('work_state_changed_at')) or parse_ts(m.get('claimed_at'))
    if changed is None or (now - changed) > datetime.timedelta(minutes=stale_min):
        out.append(brief(m, 'stuck_in_progress'))

if not out:
    print('{"wakeAgent": false}')
else:
    print(json.dumps({'inbox_messages': out}, indent=2))
PY
`
	r := strings.NewReplacer(
		"__MMB_HOME__", mmbHome,
		"__MMB_DIR__", filepath.Dir(mmbBin),
		"__MMB_BIN__", mmbBin,
		"__STALE__", fmt.Sprintf("%d", backstopStaleMinutes),
	)
	return r.Replace(tmpl)
}

// installHermesBackstop writes the gate script under HERMES_HOME/scripts and
// creates/updates the backstop cron job (idempotent by name). Best-effort on the
// cron step: if the hermes binary can't be located it writes the gate and
// returns the exact command to finish manually, rather than failing the install.
func installHermesBackstop(w io.Writer, hHome, mmbBin, mmbHome string, interval time.Duration) error {
	if mmbBin == "" {
		mmbBin = "mmb"
	}
	if mmbHome == "" {
		mmbHome = os.Getenv("HOME")
	}

	scriptsDir := filepath.Join(hHome, "scripts")
	if err := os.MkdirAll(scriptsDir, 0o755); err != nil {
		return fmt.Errorf("create scripts dir: %w", err)
	}
	gatePath := filepath.Join(scriptsDir, gateScriptName)
	if err := os.WriteFile(gatePath, []byte(hermesGateScript(mmbBin, mmbHome)), 0o755); err != nil {
		return fmt.Errorf("write gate script: %w", err)
	}
	fmt.Fprintf(w, "✓ wrote backstop gate script: %s\n", gatePath)

	schedule := hermesScheduleExpr(interval)
	prompt := "You woke because the backstop gate found undispositioned mail (its JSON is in this run's context).\n\n" + backstopAgentTurn()

	existingID := hermesFindCronJobID(hHome, hermesBackstopJobName)
	hermesBin := resolveHermesBin()
	if hermesBin == "" {
		var verb string
		if existingID != "" {
			verb = fmt.Sprintf("cron edit %s", existingID)
		} else {
			verb = fmt.Sprintf("cron create --name %s", hermesBackstopJobName)
		}
		fmt.Fprintf(w, "⚠ could not locate the `hermes` binary; finish by running:\n")
		fmt.Fprintf(w, "    HERMES_HOME=%s hermes %s --schedule %q --deliver local --script %s --prompt <see docs>\n",
			hHome, verb, schedule, gateScriptName)
		return nil
	}

	var args []string
	if existingID != "" {
		args = []string{"cron", "edit", existingID,
			"--schedule", schedule, "--deliver", "local", "--script", gateScriptName, "--prompt", prompt}
	} else {
		args = []string{"cron", "create", "--name", hermesBackstopJobName,
			"--schedule", schedule, "--deliver", "local", "--script", gateScriptName, "--prompt", prompt}
	}
	cmd := exec.Command(hermesBin, args...)
	cmd.Env = append(os.Environ(), "HERMES_HOME="+hHome)
	if outBytes, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(w, "⚠ gate written but `hermes cron` failed: %v\n%s\n", err, strings.TrimSpace(string(outBytes)))
		return nil
	}
	if existingID != "" {
		fmt.Fprintf(w, "✓ updated backstop cron %q (%s, deliver local)\n", hermesBackstopJobName, schedule)
	} else {
		fmt.Fprintf(w, "✓ created backstop cron %q (%s, deliver local)\n", hermesBackstopJobName, schedule)
	}
	return nil
}

// resolveHermesBin finds the hermes CLI: $MMB_HERMES_BIN, then PATH, then the
// common supervised-gateway venv location. Returns "" if not found.
func resolveHermesBin() string {
	if env := os.Getenv("MMB_HERMES_BIN"); env != "" {
		return env
	}
	if p, err := exec.LookPath("hermes"); err == nil {
		return p
	}
	for _, cand := range []string{"/opt/hermes/.venv/bin/hermes"} {
		if _, err := os.Stat(cand); err == nil {
			return cand
		}
	}
	return ""
}

// hermesFindCronJobID reads HERMES_HOME/cron/jobs.json and returns the id of the
// job named want, or "" if absent/unreadable. Tolerates both the top-level array
// and {"jobs": [...]} shapes.
func hermesFindCronJobID(hHome, want string) string {
	raw, err := os.ReadFile(filepath.Join(hHome, "cron", "jobs.json"))
	if err != nil {
		return ""
	}
	jobs := decodeJobsList(raw)
	for _, j := range jobs {
		if name, _ := j["name"].(string); name == want {
			if id, _ := j["id"].(string); id != "" {
				return id
			}
		}
	}
	return ""
}

// decodeJobsList parses a cron jobs file into a slice of job maps, tolerating
// either a bare array or an object with a "jobs" array.
func decodeJobsList(raw []byte) []map[string]any {
	var asArray []map[string]any
	if err := json.Unmarshal(raw, &asArray); err == nil {
		return asArray
	}
	var asObj struct {
		Jobs []map[string]any `json:"jobs"`
	}
	if err := json.Unmarshal(raw, &asObj); err == nil {
		return asObj.Jobs
	}
	return nil
}
