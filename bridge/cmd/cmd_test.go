package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// Smoke test: the cobra tree wires up every subcommand we ship and
// each prints a usable help screen.
func TestRoot_AllSubcommandsExposed(t *testing.T) {
	r := Root()
	want := []string{"init", "start", "stop", "status", "logs", "whitelist", "rotate-key"}
	got := map[string]bool{}
	for _, c := range r.Commands() {
		got[c.Name()] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing subcommand: %s", w)
		}
	}
}

func TestRoot_HelpRunsWithoutError(t *testing.T) {
	r := Root()
	var buf bytes.Buffer
	r.SetOut(&buf)
	r.SetArgs([]string{"--help"})
	if err := r.Execute(); err != nil {
		t.Fatalf("help: %v", err)
	}
	if !strings.Contains(buf.String(), "mmb-bridge") {
		t.Fatalf("help output missing program name; got: %s", buf.String())
	}
}

func TestInit_RequiresEnrollmentToken(t *testing.T) {
	r := Root()
	r.SetArgs([]string{"init"})
	r.SilenceErrors = true
	err := r.Execute()
	if err == nil {
		t.Fatal("expected init to fail without --enrollment-token")
	}
	if !strings.Contains(err.Error(), "enrollment-token") {
		t.Fatalf("error must mention enrollment-token: %v", err)
	}
}

func TestStop_NoPidFile_FriendlyError(t *testing.T) {
	t.Setenv("MMB_BRIDGE_DIR", t.TempDir())
	r := Root()
	r.SetArgs([]string{"stop"})
	r.SilenceErrors = true
	err := r.Execute()
	if err == nil {
		t.Fatal("expected error when no daemon is running")
	}
	if !strings.Contains(err.Error(), "pid file") {
		t.Fatalf("error should mention missing pid file: %v", err)
	}
}
