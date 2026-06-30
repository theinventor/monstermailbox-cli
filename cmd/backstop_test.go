package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestNewUUIDv4_FormatAndUniqueness(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		u, err := newUUIDv4()
		if err != nil {
			t.Fatal(err)
		}
		if !re.MatchString(u) {
			t.Fatalf("not a v4 uuid: %q", u)
		}
		if seen[u] {
			t.Fatalf("duplicate uuid: %q", u)
		}
		seen[u] = true
	}
}

func TestParseBackstopInterval(t *testing.T) {
	for _, ok := range []string{"15m", "30m", "1h", "1m"} {
		if _, err := parseBackstopInterval(ok); err != nil {
			t.Errorf("%q should parse: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "30s", "0m", "abc", "15"} {
		if _, err := parseBackstopInterval(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
}

func TestHermesScheduleExpr(t *testing.T) {
	cases := map[string]string{"15m": "every 15m", "30m": "every 30m", "1h": "every 1h", "90m": "every 90m"}
	for in, want := range cases {
		d, _ := time.ParseDuration(in)
		if got := hermesScheduleExpr(d); got != want {
			t.Errorf("hermesScheduleExpr(%s)=%q want %q", in, got, want)
		}
	}
}

func TestHermesGateScript_PinsPathsAndCatchesUndispositioned(t *testing.T) {
	s := hermesGateScript("/home/agent/.local/bin/mmb", "/home/agent")
	for _, want := range []string{
		"export HOME=/home/agent",
		"MMB=/home/agent/.local/bin/mmb",
		"export PATH=/home/agent/.local/bin:",
		"STALE_MINUTES=60",
		"--work-state inbox",
		"--work-state in_progress",
		"--all",
		`{"wakeAgent": false}`,
		"stuck_in_progress",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("gate script missing %q", want)
		}
	}
}

func TestOpenClawBackstopTurn_RewritesMmbBin(t *testing.T) {
	def := openClawBackstopTurn("mmb")
	if !strings.Contains(def, "mmb inbox list") {
		t.Error("default turn should reference `mmb inbox list`")
	}
	custom := openClawBackstopTurn("/Users/agent/.local/bin/mmb")
	if strings.Contains(custom, "mmb inbox list") && !strings.Contains(custom, "/Users/agent/.local/bin/mmb inbox list") {
		t.Error("custom mmb bin should be substituted into the turn")
	}
	if !strings.Contains(custom, "/Users/agent/.local/bin/mmb inbox list") {
		t.Error("expected absolute mmb path in turn commands")
	}
}

func readJobs(t *testing.T, cronPath string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(cronPath)
	if err != nil {
		t.Fatalf("read jobs.json: %v", err)
	}
	var root struct {
		Version any              `json:"version"`
		Jobs    []map[string]any `json:"jobs"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		t.Fatalf("jobs.json invalid: %v", err)
	}
	return root.Jobs
}

func countBackstop(jobs []map[string]any) (int, map[string]any) {
	n := 0
	var found map[string]any
	for _, j := range jobs {
		if name, _ := j["name"].(string); name == openClawBackstopJobName {
			n++
			found = j
		}
	}
	return n, found
}

func TestInstallOpenClawBackstop_CreatesAndIsIdempotent(t *testing.T) {
	ocHome := t.TempDir()
	cronPath := filepath.Join(ocHome, "cron", "jobs.json")

	var buf bytes.Buffer
	if err := installOpenClawBackstop(&buf, ocHome, "mmb", 15*time.Minute); err != nil {
		t.Fatalf("install: %v", err)
	}
	jobs := readJobs(t, cronPath)
	n, job := countBackstop(jobs)
	if n != 1 {
		t.Fatalf("want exactly 1 backstop job, got %d", n)
	}
	if job["sessionTarget"] != "isolated" {
		t.Errorf("sessionTarget must be isolated; got %v", job["sessionTarget"])
	}
	sched := job["schedule"].(map[string]any)
	if int64(sched["everyMs"].(float64)) != (15 * time.Minute).Milliseconds() {
		t.Errorf("everyMs wrong: %v", sched["everyMs"])
	}
	if sched["kind"] != "every" {
		t.Errorf("schedule.kind must be every; got %v", sched["kind"])
	}
	id1, _ := job["id"].(string)
	if id1 == "" {
		t.Fatal("job must have an id")
	}

	// Re-install at a new interval → still one job, same id, schedule updated.
	if err := installOpenClawBackstop(&buf, ocHome, "mmb", 30*time.Minute); err != nil {
		t.Fatalf("reinstall: %v", err)
	}
	jobs = readJobs(t, cronPath)
	n, job = countBackstop(jobs)
	if n != 1 {
		t.Fatalf("idempotency broken: %d backstop jobs after reinstall", n)
	}
	if id2, _ := job["id"].(string); id2 != id1 {
		t.Errorf("id must be preserved across reinstall: %q -> %q", id1, id2)
	}
	sched = job["schedule"].(map[string]any)
	if int64(sched["everyMs"].(float64)) != (30 * time.Minute).Milliseconds() {
		t.Errorf("schedule not updated on reinstall: %v", sched["everyMs"])
	}
	if _, err := os.Stat(cronPath + ".bak"); err != nil {
		t.Error("expected a .bak backup after reinstall")
	}
}

func TestInstallOpenClawBackstop_PreservesOtherJobs(t *testing.T) {
	ocHome := t.TempDir()
	cronDir := filepath.Join(ocHome, "cron")
	os.MkdirAll(cronDir, 0o755)
	cronPath := filepath.Join(cronDir, "jobs.json")
	seed := `{"version":1,"jobs":[{"id":"keep-me","name":"Mercury weekly review","enabled":true}]}`
	os.WriteFile(cronPath, []byte(seed), 0o644)

	var buf bytes.Buffer
	if err := installOpenClawBackstop(&buf, ocHome, "mmb", 15*time.Minute); err != nil {
		t.Fatalf("install: %v", err)
	}
	jobs := readJobs(t, cronPath)
	var keptMercury, hasBackstop bool
	for _, j := range jobs {
		switch j["name"] {
		case "Mercury weekly review":
			keptMercury = true
		case openClawBackstopJobName:
			hasBackstop = true
		}
	}
	if !keptMercury {
		t.Error("unrelated job must be preserved")
	}
	if !hasBackstop {
		t.Error("backstop job must be added")
	}
}

func TestInstallHermesBackstop_WritesGateScript(t *testing.T) {
	t.Setenv("MMB_HERMES_BIN", "") // force the no-binary path (deterministic on CI)
	t.Setenv("PATH", "")           // ensure LookPath("hermes") fails
	hHome := t.TempDir()

	var buf bytes.Buffer
	if err := installHermesBackstop(&buf, hHome, "/usr/local/bin/mmb", "/home/agent", 15*time.Minute); err != nil {
		t.Fatalf("install: %v", err)
	}
	gate := filepath.Join(hHome, "scripts", gateScriptName)
	info, err := os.Stat(gate)
	if err != nil {
		t.Fatalf("gate script not written: %v", err)
	}
	// Windows has no POSIX exec bit; Go doesn't set it there.
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o100 == 0 {
		t.Error("gate script must be executable")
	}
	body, _ := os.ReadFile(gate)
	if !strings.Contains(string(body), "MMB=/usr/local/bin/mmb") {
		t.Error("gate script must pin the mmb path")
	}
}

func TestHermesFindCronJobID(t *testing.T) {
	dir := t.TempDir()
	cronDir := filepath.Join(dir, "cron")
	os.MkdirAll(cronDir, 0o755)
	p := filepath.Join(cronDir, "jobs.json")

	// object-with-jobs shape
	os.WriteFile(p, []byte(`{"jobs":[{"id":"abc123","name":"mmb-inbox-backstop"},{"id":"x","name":"other"}]}`), 0o644)
	if got := hermesFindCronJobID(dir, hermesBackstopJobName); got != "abc123" {
		t.Errorf("object shape: got %q want abc123", got)
	}
	// bare-array shape
	os.WriteFile(p, []byte(`[{"id":"arr9","name":"mmb-inbox-backstop"}]`), 0o644)
	if got := hermesFindCronJobID(dir, hermesBackstopJobName); got != "arr9" {
		t.Errorf("array shape: got %q want arr9", got)
	}
	// absent
	os.WriteFile(p, []byte(`{"jobs":[]}`), 0o644)
	if got := hermesFindCronJobID(dir, hermesBackstopJobName); got != "" {
		t.Errorf("absent: got %q want empty", got)
	}
}

// Guard: the backstop cron argv adapts to Hermes's cron CLI — `cron edit` always
// uses --schedule/--prompt flags; `cron create` uses flags pre-0.17.0 but
// POSITIONAL schedule+prompt from 0.17.0 on (which broke the old flag form).
func TestHermesCronArgs_BothCreateForms(t *testing.T) {
	// 0.17.0+: positional create, no --schedule/--prompt.
	pos := strings.Join(hermesCronArgs("", "every 15m", "do the thing", false), " ")
	if strings.Contains(pos, "--schedule") || strings.Contains(pos, "--prompt") {
		t.Errorf("0.17.0 create must be positional, got: %s", pos)
	}
	if !strings.Contains(pos, "cron create every 15m do the thing") {
		t.Errorf("0.17.0 create must pass schedule+prompt positionally, got: %s", pos)
	}
	// pre-0.17.0: flag create.
	fl := strings.Join(hermesCronArgs("", "every 15m", "do the thing", true), " ")
	if !strings.Contains(fl, "--schedule every 15m") || !strings.Contains(fl, "--prompt do the thing") {
		t.Errorf("legacy create must use --schedule/--prompt flags, got: %s", fl)
	}
	// edit: flags on every version.
	ed := strings.Join(hermesCronArgs("job123", "every 30m", "p", false), " ")
	if !strings.Contains(ed, "cron edit job123") || !strings.Contains(ed, "--schedule every 30m") {
		t.Errorf("edit must use --schedule flag, got: %s", ed)
	}
}
