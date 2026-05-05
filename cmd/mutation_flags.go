package cmd

import (
	"github.com/spf13/cobra"
)

// mutationFlags is the shared (--idempotency-key, --dry-run) pair every
// create-style command surfaces. Principle 4 (safe retries / explicit
// mutation boundaries):
//
//   • --idempotency-key:  caller-supplied opaque token; the server
//     replays a cached response on retry instead of duplicating work.
//     Sent as the `Idempotency-Key` HTTP header.
//
//   • --dry-run:          short-circuit BEFORE the HTTP call. The
//     command prints the JSON envelope it would have POSTed and exits
//     0 — useful for agents that want to validate a payload without
//     side effects.
//
// Wire-up: every mutation command declares a `mutationFlags` value
// and calls `bindMutationFlags(cmd, &mf)` in its constructor. RunE
// then checks `mf.DryRun` before calling cli.Do, and passes
// `mf.Headers()` into cli.DoWithHeaders.
type mutationFlags struct {
	IdempotencyKey string
	DryRun         bool
}

// bindMutationFlags wires the two flags onto a cobra command. Always
// safe to call — the flags are optional, and omitting both restores
// the legacy behavior.
func bindMutationFlags(c *cobra.Command, mf *mutationFlags) {
	c.Flags().StringVar(&mf.IdempotencyKey, "idempotency-key", "",
		"caller-supplied token; retries with the same key replay the original response")
	c.Flags().BoolVar(&mf.DryRun, "dry-run", false,
		"print the request that would be sent, then exit (no HTTP call)")
}

// Headers returns the extra HTTP headers this mutation invocation
// should carry. Returns nil if no headers are needed.
func (mf mutationFlags) Headers() map[string]string {
	if mf.IdempotencyKey == "" {
		return nil
	}
	return map[string]string{"Idempotency-Key": mf.IdempotencyKey}
}

// dryRunEnvelope is the shape every --dry-run response uses, so agents
// see consistent fields no matter which mutation they invoked. The
// `dry_run: true` flag at the top level is the unambiguous signal —
// no HTTP call happened.
type dryRunEnvelope struct {
	DryRun         bool              `json:"dry_run"`
	Method         string            `json:"method"`
	Path           string            `json:"path"`
	Body           any               `json:"body,omitempty"`
	Headers        map[string]string `json:"headers,omitempty"`
	IdempotencyKey string            `json:"idempotency_key,omitempty"`
}

// newDryRunEnvelope packages the would-be-request for stdout emission.
func newDryRunEnvelope(method, path string, body any, mf mutationFlags) dryRunEnvelope {
	return dryRunEnvelope{
		DryRun:         true,
		Method:         method,
		Path:           path,
		Body:           body,
		Headers:        mf.Headers(),
		IdempotencyKey: mf.IdempotencyKey,
	}
}
