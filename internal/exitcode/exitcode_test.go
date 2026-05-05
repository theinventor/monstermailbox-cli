package exitcode

import (
	"errors"
	"fmt"
	"testing"
)

func TestExitCodeFor_NilIsSuccess(t *testing.T) {
	if got := ExitCodeFor(nil); got != Success {
		t.Errorf("ExitCodeFor(nil) = %d; want Success(%d)", got, Success)
	}
}

func TestExitCodeFor_PlainErrorIsGeneric(t *testing.T) {
	if got := ExitCodeFor(errors.New("plain")); got != Generic {
		t.Errorf("plain error = %d; want Generic(%d)", got, Generic)
	}
}

// The wire-up that main.go depends on: a wrapped Error must be discoverable
// even when it's behind a fmt.Errorf("%w", ...) wrap.
func TestExitCodeFor_FindsWrappedError(t *testing.T) {
	err := fmt.Errorf("outer: %w", Wrap(Validation, errors.New("inner")))
	if got := ExitCodeFor(err); got != Validation {
		t.Errorf("wrapped Error = %d; want Validation(%d)", got, Validation)
	}
}

func TestWrap_PreservesNil(t *testing.T) {
	if Wrap(Auth, nil) != nil {
		t.Errorf("Wrap(_, nil) MUST return nil; got non-nil")
	}
}

func TestFromHTTPStatus_Mapping(t *testing.T) {
	cases := map[int]int{
		200: Success,
		201: Success,
		400: Validation,
		401: Auth,
		403: Auth,
		404: NotFound,
		409: Conflict,
		422: Validation,
		429: Generic, // explicitly: 429 has no dedicated code
		500: Server,
		502: Server,
		503: Server,
	}
	for status, want := range cases {
		if got := FromHTTPStatus(status); got != want {
			t.Errorf("FromHTTPStatus(%d) = %d; want %d", status, got, want)
		}
	}
}

func TestAll_ContainsEveryDefinedCode(t *testing.T) {
	all := All()
	if len(all) < 9 {
		t.Errorf("All() should enumerate every defined code; got len=%d", len(all))
	}
	// Each must have a Description (not "unknown").
	for _, c := range all {
		if Description(c) == "unknown" {
			t.Errorf("code %d has no Description", c)
		}
	}
}
