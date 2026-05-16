package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSkillGet_PrintsMonsterMailboxSample(t *testing.T) {
	stdout, _, err := runMmbCmd(t, []string{"skill", "get", "monstermailbox"}, nil)
	if err != nil {
		t.Fatalf("skill get monstermailbox: %v", err)
	}
	if !strings.Contains(stdout, "# MonsterMailbox") {
		t.Fatalf("sample skill missing title: %s", stdout[:min(len(stdout), 200)])
	}
	if !strings.Contains(stdout, "HUMAN_OWNER_NAME") {
		t.Errorf("sample skill should include owner placeholder")
	}
	if !strings.Contains(stdout, "SAMPLE_ACTIONS_SECTION") || !strings.Contains(stdout, "END_SAMPLE_ACTIONS_SECTION") {
		t.Errorf("sample skill should include bounded sample actions section")
	}
}

func TestSkillGet_CanWriteOutputFile(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "SKILL.md")
	stdout, _, err := runMmbCmd(t, []string{"skill", "get", "monstermailbox", "--output", outPath}, nil)
	if err != nil {
		t.Fatalf("skill get --output: %v", err)
	}
	if stdout != "" {
		t.Errorf("--output should not print skill body to stdout; got %q", stdout)
	}
	body, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "# MonsterMailbox") {
		t.Errorf("output file missing sample skill body")
	}
}

func TestEmbeddedSkillMatchesRepoSample(t *testing.T) {
	repoSkill, err := os.ReadFile(filepath.Join("..", "skills", "monstermailbox", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(repoSkill) != monsterMailboxSkillSample {
		t.Fatalf("embedded sample skill must match skills/monstermailbox/SKILL.md; copy the updated file to cmd/embedded/monstermailbox_skill.md")
	}
}

func TestAgentContext_AdvertisesSampleSkillResource(t *testing.T) {
	ctx := runAgentContext(t)
	resources, ok := ctx["resources"].(map[string]any)
	if !ok {
		t.Fatalf("resources MUST be present; got %T", ctx["resources"])
	}
	sample, ok := resources["sample_skill"].(map[string]any)
	if !ok {
		t.Fatalf("resources.sample_skill MUST be present; got %v", resources)
	}
	if sample["command"] != sampleSkillCommand {
		t.Errorf("sample skill command = %v; want %q", sample["command"], sampleSkillCommand)
	}
}

func TestAuthSave_PrintsSampleSkillReminder(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "config.json")
	t.Setenv("MMB_CONFIG", cfg)

	stdout, _, err := runMmbCmd(t, []string{
		"auth", "save",
		"--profile", "tip-bot",
		"--api-key", "mmb_tip_test_key",
		"--storage", "file",
	}, nil)
	if err != nil {
		t.Fatalf("auth save: %v", err)
	}
	if !strings.Contains(stdout, "Agent setup tip:") || !strings.Contains(stdout, sampleSkillCommand) {
		t.Errorf("auth save should advertise sample skill setup; got: %s", stdout)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
