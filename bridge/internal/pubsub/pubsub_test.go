package pubsub

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type fakeTokenSource struct{ token string }

func (f *fakeTokenSource) Token(_ context.Context) (string, error) { return f.token, nil }

func TestPull_DecodesGmailPushPayload(t *testing.T) {
	gmailPush, _ := json.Marshal(map[string]any{
		"emailAddress": "you@gmail.com",
		"historyId":    9999,
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, ":pull") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer fake-token" {
			t.Errorf("missing bearer")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"receivedMessages": []map[string]any{
				{
					"ackId": "ack-1",
					"message": map[string]any{
						"data":      base64.StdEncoding.EncodeToString(gmailPush),
						"messageId": "pubsub-msg-1",
					},
				},
			},
		})
	}))
	defer server.Close()

	sub := NewWithBase("p", "s", &fakeTokenSource{"fake-token"}, server.URL)
	msgs, err := sub.Pull(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].AckID != "ack-1" {
		t.Fatalf("unexpected msgs %#v", msgs)
	}
	if msgs[0].Payload.EmailAddress != "you@gmail.com" {
		t.Fatalf("expected emailAddress decoded, got %q", msgs[0].Payload.EmailAddress)
	}
	if msgs[0].Payload.HistoryID != "9999" {
		t.Fatalf("history id should be 9999 (numeric coerced to string); got %q", msgs[0].Payload.HistoryID)
	}
}

func TestPull_EmptyResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	sub := NewWithBase("p", "s", &fakeTokenSource{"t"}, server.URL)
	msgs, err := sub.Pull(context.Background(), 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 0 {
		t.Fatalf("expected empty, got %d", len(msgs))
	}
}

func TestPull_ServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":{"message":"forbidden"}}`))
	}))
	defer server.Close()
	sub := NewWithBase("p", "s", &fakeTokenSource{"t"}, server.URL)
	if _, err := sub.Pull(context.Background(), 5); err == nil {
		t.Fatal("expected error on 403")
	}
}

func TestAcknowledge(t *testing.T) {
	hits := int64(0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt64(&hits, 1)
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"ack-1"`) || !strings.Contains(string(body), `"ack-2"`) {
			t.Errorf("ackIds missing from body: %s", body)
		}
		w.WriteHeader(200)
	}))
	defer server.Close()
	sub := NewWithBase("p", "s", &fakeTokenSource{"t"}, server.URL)
	if err := sub.Acknowledge(context.Background(), []string{"ack-1", "ack-2"}); err != nil {
		t.Fatal(err)
	}
	if hits != 1 {
		t.Fatalf("expected 1 ack call, got %d", hits)
	}
}

func TestAcknowledge_NoOpOnEmpty(t *testing.T) {
	sub := NewWithBase("p", "s", &fakeTokenSource{"t"}, "http://does-not-matter")
	if err := sub.Acknowledge(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
}
