// Package exitcode is the documented exit-code taxonomy the CLI uses.
//
// Principle 2 (structured output) says: stable exit-code taxonomy if you can
// manage it. Agents branch on these — `if mmb msg get $id; then ...; fi` is
// almost useless if every error is exit 1.
//
// Wire-up: commands return Error{Code, Err}. main.go's ExitCodeFor walks the
// error chain and sets os.Exit accordingly. The set is also surfaced in
// `mmb agent-context` so agents can discover the taxonomy programmatically.
//
// Adding a new code: add the const here, add to Description(), use Wrap() at
// the call site. Don't introduce ad-hoc os.Exit calls.
package exitcode

import (
	"errors"
	"net/http"
)

// Stable codes. New codes go at the end; do not renumber.
const (
	Success    = 0 // command succeeded
	Generic    = 1 // unclassified error (legacy default)
	Usage      = 2 // bad flags / args / missing required
	Auth       = 3 // 401 / 403 — credentials missing or rejected
	NotFound   = 4 // 404 — resource doesn't exist
	Validation = 5 // 422 / 400 — input rejected by the server
	Server     = 6 // 5xx — server error, retry may help
	Network    = 7 // transport-level failure (DNS, TCP, TLS, timeout)
	Conflict   = 8 // 409 — resource conflict (e.g. address taken)
)

// Description returns a human-readable label for a code. Used by
// agent-context and `mmb --help` documentation; never appears in
// hot paths.
func Description(code int) string {
	switch code {
	case Success:
		return "success"
	case Generic:
		return "generic error"
	case Usage:
		return "usage error (bad flags or args)"
	case Auth:
		return "authentication or authorization failure"
	case NotFound:
		return "resource not found"
	case Validation:
		return "input validation failed"
	case Server:
		return "server error (5xx)"
	case Network:
		return "network or transport failure"
	case Conflict:
		return "resource conflict"
	default:
		return "unknown"
	}
}

// All returns the documented set of exit codes for `mmb agent-context`
// to enumerate. Order is stable (numeric ascending).
func All() []int {
	return []int{Success, Generic, Usage, Auth, NotFound, Validation, Server, Network, Conflict}
}

// Error wraps an underlying error with an exit code. Commands return one of
// these (via Wrap) and main.go decides the os.Exit value via ExitCodeFor.
type Error struct {
	Code int
	Err  error
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Wrap is the canonical way to produce an Error. Returns the underlying
// error unchanged when err is nil, so callers can use:
//
//	return exitcode.Wrap(exitcode.Validation, fmt.Errorf("..."))
//
// without checking err first.
func Wrap(code int, err error) error {
	if err == nil {
		return nil
	}
	return &Error{Code: code, Err: err}
}

// ExitCodeFor walks the error chain and returns the first code it finds.
// Returns Generic when the chain has no Error — keeps backwards compat
// with code that returns plain fmt.Errorf.
func ExitCodeFor(err error) int {
	if err == nil {
		return Success
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return Generic
}

// FromHTTPStatus maps an HTTP response status to the appropriate exit code.
// Used by passthroughJSON so every API command surfaces the right code
// without each caller needing to remember the table.
func FromHTTPStatus(status int) int {
	switch {
	case status >= 200 && status < 300:
		return Success
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return Auth
	case status == http.StatusNotFound:
		return NotFound
	case status == http.StatusConflict:
		return Conflict
	case status == http.StatusBadRequest, status == http.StatusUnprocessableEntity:
		return Validation
	case status >= 500:
		return Server
	default:
		// 405, 410, 429, etc. — not a clear category; surface as generic.
		return Generic
	}
}
