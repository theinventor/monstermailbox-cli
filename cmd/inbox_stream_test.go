package cmd

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/theinventor/monstermailbox-cli/internal/client"
)

// A /events stream that only ever emits heartbeat comments (never a matching
// inbox event) must NOT hang `wait` past its --timeout. This is the exact
// failure that stalled a live agent for 7 days: the server kept the connection
// alive with heartbeats (resetting the idle watchdog every ~15s) but delivered
// no events, so streamOnce's read loop spun forever and the between-reconnect
// deadline check in runEventStream was never reached. streamOnce must now
// enforce the deadline itself.
func TestStreamOnce_HeartbeatOnlyStreamHonorsDeadline(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		tick := time.NewTicker(15 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
				if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
					return
				}
				if fl != nil {
					fl.Flush()
				}
			}
		}
	}))
	defer srv.Close()

	t.Setenv(client.EnvAPIURL, srv.URL)
	t.Setenv(client.EnvAPIKey, "mmb_testkey1234567890")

	cli := newAPIClient()
	opts := eventStreamOptions{
		stopOnFirst:     true,
		stateFilter:     "trusted",
		overallDeadline: time.Now().Add(500 * time.Millisecond),
	}

	done := make(chan error, 1)
	var lastID string
	start := time.Now()
	go func() {
		_, err := streamOnce(io.Discard, cli, opts, &lastID)
		done <- err
	}()

	select {
	case <-done:
		if elapsed := time.Since(start); elapsed > 3*time.Second {
			t.Fatalf("streamOnce took %s on a heartbeat-only stream — deadline not enforced during the read loop", elapsed)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("streamOnce hung on a heartbeat-only stream past its deadline (the 7-day-stall bug)")
	}
}

// runEventStream, driving the same heartbeat-only stream with a wait-shaped
// deadline, must return a timeout error promptly rather than blocking forever.
func TestRunEventStream_WaitTimesOutOnHeartbeatOnlyStream(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/events" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		tick := time.NewTicker(15 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
				if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
					return
				}
				if fl != nil {
					fl.Flush()
				}
			}
		}
	}))
	defer srv.Close()

	t.Setenv(client.EnvAPIURL, srv.URL)
	t.Setenv(client.EnvAPIKey, "mmb_testkey1234567890")

	root := NewRootCmd()
	root.SetArgs([]string{"inbox", "wait", "--timeout", "700ms", "--state", "trusted"})

	done := make(chan error, 1)
	start := time.Now()
	go func() { done <- root.Execute() }()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a timeout error from `inbox wait` on a heartbeat-only stream")
		}
		if elapsed := time.Since(start); elapsed > 4*time.Second {
			t.Fatalf("`inbox wait --timeout 700ms` took %s to time out", elapsed)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("`inbox wait` hung past its --timeout on a heartbeat-only stream")
	}
}
