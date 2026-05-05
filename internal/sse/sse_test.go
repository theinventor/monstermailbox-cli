package sse

import (
	"io"
	"strings"
	"testing"
)

func collect(t *testing.T, raw string) []Event {
	t.Helper()
	r := New(strings.NewReader(raw))
	var got []Event
	for {
		ev, err := r.Next()
		if err == io.EOF {
			return got
		}
		if err != nil {
			t.Fatalf("unexpected err=%v", err)
		}
		got = append(got, ev)
	}
}

func TestReader_DispatchesNamedEventOnBlankLine(t *testing.T) {
	in := "event: connected\ndata: {\"ts\":\"now\"}\n\n"
	got := collect(t, in)
	if len(got) != 1 {
		t.Fatalf("want 1 event; got %d (%+v)", len(got), got)
	}
	if got[0].Name != "connected" || got[0].Data != `{"ts":"now"}` {
		t.Errorf("unexpected event: %+v", got[0])
	}
}

func TestReader_HeartbeatCommentDispatchesImmediately(t *testing.T) {
	in := ": heartbeat\n\nevent: x\ndata: y\n\n"
	got := collect(t, in)
	if len(got) != 2 {
		t.Fatalf("want 2 events (heartbeat + named); got %d", len(got))
	}
	if !got[0].IsComment {
		t.Errorf("first event MUST be a comment-heartbeat; got %+v", got[0])
	}
	if got[1].Name != "x" {
		t.Errorf("second event MUST be the named event; got %+v", got[1])
	}
}

func TestReader_MultipleDataLinesJoinWithNewline(t *testing.T) {
	in := "event: msg\ndata: line1\ndata: line2\n\n"
	got := collect(t, in)
	if len(got) != 1 || got[0].Data != "line1\nline2" {
		t.Errorf("data join failed; got %+v", got)
	}
}

func TestReader_IgnoresIDAndRetryFields(t *testing.T) {
	in := "id: 42\nretry: 3000\nevent: x\ndata: y\n\n"
	got := collect(t, in)
	if len(got) != 1 || got[0].Name != "x" {
		t.Errorf("id/retry MUST be ignored without breaking dispatch; got %+v", got)
	}
}

func TestReader_EOFEndsCleanly(t *testing.T) {
	got := collect(t, "")
	if len(got) != 0 {
		t.Errorf("empty stream MUST produce 0 events; got %+v", got)
	}
}
