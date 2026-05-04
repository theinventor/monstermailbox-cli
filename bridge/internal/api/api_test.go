package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestEnroll_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bridges/enroll" || r.Method != "POST" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		var got map[string]string
		_ = json.Unmarshal(body, &got)
		if got["enrollment_token"] != "bre_test" {
			t.Errorf("unexpected token: %v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"api_base_url":"https://app.example.com","api_key":"mmb_abc","agent_email":"a@b.com"}`))
	}))
	defer server.Close()

	resp, err := Enroll(context.Background(), server.URL, "bre_test")
	if err != nil {
		t.Fatal(err)
	}
	if resp.APIKey != "mmb_abc" || resp.AgentEmail != "a@b.com" {
		t.Fatalf("unexpected response: %#v", resp)
	}
}

func TestEnroll_ServerErrorIsSurfaced(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":"invalid_token","message":"used"}`))
	}))
	defer server.Close()

	_, err := Enroll(context.Background(), server.URL, "bre_used")
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "used") {
		t.Fatalf("error must contain status + body; got: %v", err)
	}
}

func TestPostInbound_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mmb_abc" {
			t.Errorf("missing/wrong bearer header")
		}
		if r.Header.Get("Content-Type") != "message/rfc822" {
			t.Errorf("expected message/rfc822 content-type; got %q", r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.HasPrefix(string(body), "From:") {
			t.Errorf("body should start with MIME headers; got %q", string(body))
		}
		w.WriteHeader(202)
		_, _ = w.Write([]byte(`{"inbound_email_id":"42"}`))
	}))
	defer server.Close()

	c := New(server.URL, "mmb_abc")
	id, err := c.PostInbound(context.Background(), []byte("From: a@b.com\nSubject: hi\n\nbody"))
	if err != nil {
		t.Fatal(err)
	}
	if id != "42" {
		t.Fatalf("expected id=42, got %q", id)
	}
}

func TestGetPolicy_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bridge/policy" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"whitelist":[{"id":"1","sender":"a@b.com"}],"version":7,"last_updated_at":"2026-05-03T00:00:00Z"}`))
	}))
	defer server.Close()

	c := New(server.URL, "mmb_abc")
	pol, err := c.GetPolicy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if pol.Version != 7 || len(pol.Whitelist) != 1 || pol.Whitelist[0].Sender != "a@b.com" {
		t.Fatalf("unexpected policy: %#v", pol)
	}
}

func TestRotate_HappyPath(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/bridge/rotate" || r.Method != "POST" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"api_key":"mmb_new","previous_last4":"abcd"}`))
	}))
	defer server.Close()

	c := New(server.URL, "mmb_old")
	resp, err := c.Rotate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if resp.APIKey != "mmb_new" || resp.PreviousLast4 != "abcd" {
		t.Fatalf("unexpected: %#v", resp)
	}
}
