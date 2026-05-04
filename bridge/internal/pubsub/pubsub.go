// Package pubsub is a stripped-down REST client for Google Cloud
// Pub/Sub — Pull + Acknowledge only. We deliberately do NOT pull in
// `cloud.google.com/go/pubsub` because that brings ~200 transitive
// deps, gRPC, and the Cloud SDK to do two HTTP POSTs. The official
// REST API is stable + fully-documented; that's all we need.
//
// Loop:
//
//   for {
//     msgs := Pull(maxMessages=10, returnImmediately=false)
//     for each msg: handler(decoded gmail push payload)
//     Acknowledge(msg.ackId for handled msgs)
//     on empty: sleep 5s (long-poll occasionally returns empty)
//   }
//
// `returnImmediately=false` long-polls up to ~90s server-side which
// keeps idle traffic to two HTTP requests every 90s.
package pubsub

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// PushPayload is the Gmail-side push body that arrives via Pub/Sub.
// Gmail publishes `{emailAddress, historyId}` as the message data;
// we decode it from the base64 envelope before handing to the
// daemon's handler.
type PushPayload struct {
	EmailAddress string `json:"emailAddress"`
	HistoryID    string `json:"historyId"`
}

// ReceivedMessage pairs a decoded payload with the ackId needed to
// confirm processing back to Pub/Sub.
type ReceivedMessage struct {
	AckID   string
	Payload PushPayload
}

// Subscriber pulls + acks messages from a single subscription.
type Subscriber struct {
	Project      string
	Subscription string
	tokenSrc     TokenSource
	httpClient   *http.Client
	baseURL      string
}

// TokenSource is satisfied by *oauth.Source — taking the interface
// here keeps the package importable from tests with a mock source.
type TokenSource interface {
	Token(ctx context.Context) (string, error)
}

// New returns a Subscriber pointing at the production endpoint. Tests
// can override via NewWithBase.
func New(project, subscription string, src TokenSource) *Subscriber {
	return NewWithBase(project, subscription, src, "https://pubsub.googleapis.com")
}

// NewWithBase is exposed for tests that wrap the API with a httptest
// server.
func NewWithBase(project, subscription string, src TokenSource, baseURL string) *Subscriber {
	return &Subscriber{
		Project:      project,
		Subscription: subscription,
		tokenSrc:     src,
		baseURL:      strings.TrimRight(baseURL, "/"),
		httpClient:   &http.Client{Timeout: 120 * time.Second},
	}
}

// Pull issues one POST :pull. `maxMessages` caps the response. The
// returned slice is empty when the subscription is idle; callers
// should treat that as a normal long-poll return, not an error.
func (s *Subscriber) Pull(ctx context.Context, maxMessages int) ([]ReceivedMessage, error) {
	tok, err := s.tokenSrc.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("oauth: %w", err)
	}

	body, _ := json.Marshal(map[string]any{
		"maxMessages":       maxMessages,
		"returnImmediately": false,
	})

	endpoint := fmt.Sprintf("%s/v1/projects/%s/subscriptions/%s:pull",
		s.baseURL, url.PathEscape(s.Project), url.PathEscape(s.Subscription))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("call pull: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("pull %s %s: %s", endpoint, resp.Status, respBody)
	}

	var parsed struct {
		ReceivedMessages []struct {
			AckID   string `json:"ackId"`
			Message struct {
				Data       string            `json:"data"`
				MessageID  string            `json:"messageId"`
				Attributes map[string]string `json:"attributes"`
			} `json:"message"`
		} `json:"receivedMessages"`
	}
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return nil, fmt.Errorf("parse pull response: %w (body: %s)", err, respBody)
	}

	out := make([]ReceivedMessage, 0, len(parsed.ReceivedMessages))
	for _, rm := range parsed.ReceivedMessages {
		// data is base64-encoded JSON: {emailAddress, historyId}.
		payloadBytes, err := base64.StdEncoding.DecodeString(rm.Message.Data)
		if err != nil {
			payloadBytes, err = base64.URLEncoding.DecodeString(rm.Message.Data)
			if err != nil {
				return nil, fmt.Errorf("decode pubsub data: %w", err)
			}
		}
		var pp PushPayload
		// Tolerate historyId as either string or number.
		var raw map[string]any
		if err := json.Unmarshal(payloadBytes, &raw); err != nil {
			return nil, fmt.Errorf("parse push payload: %w (data: %s)", err, payloadBytes)
		}
		if v, ok := raw["emailAddress"].(string); ok {
			pp.EmailAddress = v
		}
		switch v := raw["historyId"].(type) {
		case string:
			pp.HistoryID = v
		case float64:
			pp.HistoryID = fmt.Sprintf("%d", int64(v))
		}
		out = append(out, ReceivedMessage{AckID: rm.AckID, Payload: pp})
	}
	return out, nil
}

// Acknowledge confirms one or more ackIds. Empty input is a no-op.
func (s *Subscriber) Acknowledge(ctx context.Context, ackIDs []string) error {
	if len(ackIDs) == 0 {
		return nil
	}
	tok, err := s.tokenSrc.Token(ctx)
	if err != nil {
		return fmt.Errorf("oauth: %w", err)
	}
	body, _ := json.Marshal(map[string]any{"ackIds": ackIDs})
	endpoint := fmt.Sprintf("%s/v1/projects/%s/subscriptions/%s:acknowledge",
		s.baseURL, url.PathEscape(s.Project), url.PathEscape(s.Subscription))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("call acknowledge: %w", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("acknowledge %s: %s", resp.Status, respBody)
	}
	return nil
}
