// Package forward orchestrates: Gmail history → whitelist match →
// fetch raw → POST /bridge/inbound. One Forwarder is shared by the
// daemon goroutine that consumes Pub/Sub events.
//
// The flow:
//
//  1. Receive a `pubsub.PushPayload{emailAddress, historyId}`.
//  2. Call `gog gmail history --since=<lastHistoryID>` to enumerate
//     new message IDs (gmail history is monotonic + cumulative).
//  3. For each new ID:
//       a. Skip if seen in dedup ring.
//       b. Fetch metadata (From + Subject).
//       c. Match against whitelist; skip on no-match.
//       d. Fetch raw MIME.
//       e. POST to /bridge/inbound.
//       f. On success: mark ID seen + advance state.
//  4. Save state after the batch.
package forward

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/theinventor/monstermailbox-cli/bridge/internal/api"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/gogcli"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/log"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/matcher"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/policy"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/pubsub"
	"github.com/theinventor/monstermailbox-cli/bridge/internal/state"
)

// Forwarder is constructed once at daemon start and reused per push.
type Forwarder struct {
	API      *api.Client
	Gog      *gogcli.Client
	Policy   *policy.Store
	Matcher  *matcher.Matcher
	State    *state.State
	Logger   *log.Logger

	// Counters for status output. Mutated under the State mutex.
	Forwarded uint64
	Matched   uint64
	Dropped   uint64
}

// Handle runs the full pipeline for one Pub/Sub push.
func (f *Forwarder) Handle(ctx context.Context, push pubsub.PushPayload) error {
	since := f.State.HistoryID()
	if since == "" {
		// First-ever run: use the historyId from the push as the
		// baseline. We do NOT process the push's own message because
		// its content isn't carried in the envelope and history
		// queries with since==current return empty. Subsequent
		// pushes will move forward from here.
		f.State.SetHistoryID(push.HistoryID)
		_ = f.State.Save()
		f.Logger.Infof("first-run: bookmarked historyId=%s without processing", push.HistoryID)
		return nil
	}

	ids, nextHID, err := f.Gog.History(ctx, since)
	if err != nil {
		return fmt.Errorf("gog history since=%s: %w", since, err)
	}
	if len(ids) == 0 {
		f.Logger.Debugf("history since=%s returned 0 new ids", since)
	}
	for _, id := range ids {
		if err := f.handleOne(ctx, id); err != nil {
			f.Logger.Warnf("forward msg=%s: %v", id, err)
			// Continue with next message — a single failure should
			// NOT stall the whole batch. Pub/Sub at-least-once
			// redelivery would re-attempt this message anyway.
		}
	}
	if nextHID != "" {
		f.State.SetHistoryID(nextHID)
	} else if push.HistoryID != "" {
		// Fallback: gog returned no historyId — push's id is the next
		// best bookmark.
		f.State.SetHistoryID(push.HistoryID)
	}
	if err := f.State.Save(); err != nil {
		f.Logger.Warnf("save state: %v", err)
	}
	return nil
}

func (f *Forwarder) handleOne(ctx context.Context, msgID string) error {
	if f.State.SeenMessage(msgID) {
		f.Logger.Debugf("skip dup msg=%s", msgID)
		return nil
	}

	md, err := f.Gog.GetMetadata(ctx, msgID)
	if err != nil {
		return fmt.Errorf("metadata: %w", err)
	}
	from := extractAddress(md.From)
	snap, _, _ := f.Policy.Snapshot()
	entry := f.Matcher.Match(snap.Whitelist, from, md.Subject)
	if entry == nil {
		f.Dropped++
		f.Logger.Infof("drop msg=%s from=%q subject=%q (no whitelist match)", msgID, from, md.Subject)
		f.State.MarkMessage(msgID) // dedupe drops too — re-eval would yield same result
		return nil
	}
	f.Matched++

	raw, err := f.Gog.GetRaw(ctx, msgID)
	if err != nil {
		return fmt.Errorf("get raw: %w", err)
	}
	if len(raw) == 0 {
		return errors.New("empty raw MIME from gog")
	}

	inboundID, err := f.API.PostInbound(ctx, raw)
	if err != nil {
		return fmt.Errorf("post inbound: %w", err)
	}
	f.Forwarded++
	f.State.MarkMessage(msgID)
	f.Logger.Infof("forward msg=%s from=%q subject=%q → inbound=%s (matched %s)", msgID, from, md.Subject, inboundID, describeMatch(entry))
	return nil
}

// extractAddress pulls the bare address out of a `Display Name <a@b>`
// header. Falls back to the trimmed input on a missing angle pair.
func extractAddress(rfc5322 string) string {
	s := strings.TrimSpace(rfc5322)
	if i := strings.LastIndex(s, "<"); i >= 0 {
		if j := strings.LastIndex(s, ">"); j > i {
			return strings.ToLower(strings.TrimSpace(s[i+1 : j]))
		}
	}
	return strings.ToLower(s)
}

func describeMatch(e *api.WhitelistEntry) string {
	switch {
	case e.Sender != "":
		return "exact sender=" + e.Sender
	case e.SenderRegex != "":
		return "sender_regex=" + e.SenderRegex
	default:
		return "id=" + e.ID
	}
}
