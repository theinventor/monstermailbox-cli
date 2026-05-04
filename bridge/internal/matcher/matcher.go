// Package matcher decides whether an inbound Gmail message matches
// the agent's whitelist and should therefore be forwarded.
//
// Match precedence mirrors the server's `Whitelist.match_for`:
//
//  1. Exact `sender` (case-insensitive on email).
//  2. Compiled `sender_regex` (skipped if pattern fails to compile).
//
// When the matched entry has a `subject_regex`, the subject must ALSO
// match — broken regex = no match (fail-closed). System-managed
// entries are filtered server-side at the /bridge/policy boundary;
// the bridge sees only user-authored entries.
package matcher

import (
	"regexp"
	"strings"
	"sync"

	"github.com/theinventor/monstermailbox-cli/bridge/internal/api"
)

// Matcher caches compiled regexes across Match() calls so a chatty
// inbox doesn't re-compile the same patterns on every message.
type Matcher struct {
	mu       sync.Mutex
	compiled map[string]*regexp.Regexp // key: pattern; value: compiled or nil if uncompilable
}

// New returns an empty matcher.
func New() *Matcher {
	return &Matcher{compiled: make(map[string]*regexp.Regexp)}
}

// Match returns the whitelist entry that admits the given message,
// or nil if no entry matches.
func (m *Matcher) Match(entries []api.WhitelistEntry, fromEmail, subject string) *api.WhitelistEntry {
	from := strings.ToLower(strings.TrimSpace(fromEmail))
	if from == "" {
		return nil
	}

	// 1. Exact sender.
	for i := range entries {
		e := &entries[i]
		if e.Sender != "" && strings.EqualFold(e.Sender, from) {
			if m.subjectMatches(e, subject) {
				return e
			}
		}
	}

	// 2. Sender regex.
	for i := range entries {
		e := &entries[i]
		if e.SenderRegex == "" {
			continue
		}
		re := m.compile(e.SenderRegex)
		if re == nil {
			continue
		}
		if re.MatchString(from) && m.subjectMatches(e, subject) {
			return e
		}
	}
	return nil
}

func (m *Matcher) subjectMatches(e *api.WhitelistEntry, subject string) bool {
	if e.SubjectRegex == "" {
		return true
	}
	re := m.compile(e.SubjectRegex)
	if re == nil {
		return false
	}
	return re.MatchString(subject)
}

func (m *Matcher) compile(pattern string) *regexp.Regexp {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cached, ok := m.compiled[pattern]; ok {
		return cached
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		m.compiled[pattern] = nil
		return nil
	}
	m.compiled[pattern] = re
	return re
}
