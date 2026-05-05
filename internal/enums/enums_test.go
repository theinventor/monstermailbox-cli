package enums

import (
	"strings"
	"testing"
)

func TestValidate_AcceptsEmptyAsUnset(t *testing.T) {
	if err := Validate("state", "", InboxStates); err != nil {
		t.Errorf("empty value MUST pass (Validate is range-check, not presence-check); got %v", err)
	}
}

func TestValidate_AcceptsValidValues(t *testing.T) {
	for _, v := range InboxStates {
		if err := Validate("state", v, InboxStates); err != nil {
			t.Errorf("Validate(%q) MUST pass; got %v", v, err)
		}
	}
}

// The principle-3 invariant: when an enum is rejected, the error MUST
// name the valid set so the agent can self-correct in one retry without
// having to read --help.
func TestValidate_RejectionTeachesValidSet(t *testing.T) {
	err := Validate("state", "secret", InboxStates)
	if err == nil {
		t.Fatalf("invalid value MUST yield error; got nil")
	}
	msg := err.Error()
	for _, v := range InboxStates {
		if !strings.Contains(msg, v) {
			t.Errorf("error MUST enumerate %q so the agent self-corrects; got %q", v, msg)
		}
	}
	if !strings.Contains(msg, "secret") {
		t.Errorf("error MUST echo the bad value; got %q", msg)
	}
	if !strings.Contains(msg, "--state") {
		t.Errorf("error MUST name the flag; got %q", msg)
	}
}

func TestInContext_ExposesEverySharedEnum(t *testing.T) {
	// Pin: every var in this package that's a []string MUST be reachable
	// from InContext, otherwise agent-context introspection drifts.
	want := map[string][]string{
		"inbox_state":    InboxStates,
		"deliver_scheme": DeliverSchemes,
	}
	for k, expected := range want {
		got, ok := InContext[k]
		if !ok {
			t.Errorf("InContext missing %q", k)
			continue
		}
		if len(got) != len(expected) {
			t.Errorf("InContext[%q] len = %d; want %d", k, len(got), len(expected))
		}
	}
}
