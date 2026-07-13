package cmd

import "testing"

// The resume cursor must only ever move FORWARD. Broadcasts can arrive
// out of id order (concurrent classification jobs finish in any order),
// and a cursor that moves backwards makes the server's Last-Event-ID
// replay re-deliver already-emitted events on every reconnect — which
// newer servers force roughly every 60 seconds (stream rotation).
func TestAdvanceCursor(t *testing.T) {
	cases := []struct {
		name      string
		current   string
		candidate string
		want      string
	}{
		{"empty current takes candidate", "", "42", "42"},
		{"forward advances", "42", "43", "43"},
		{"backward is ignored", "43", "42", "43"},
		{"equal keeps current", "42", "42", "42"},
		{"zero seed from hello", "", "0", "0"},
		{"first real id beats zero seed", "0", "7", "7"},
		{"non-numeric candidate ignored", "42", "garbage", "42"},
		{"non-numeric current replaced", "garbage", "42", "42"},
		{"both non-numeric keeps current", "garbage", "junk", "garbage"},
		{"large ids compare numerically not lexically", "99", "100", "100"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := advanceCursor(tc.current, tc.candidate); got != tc.want {
				t.Errorf("advanceCursor(%q, %q) = %q, want %q", tc.current, tc.candidate, got, tc.want)
			}
		})
	}
}
