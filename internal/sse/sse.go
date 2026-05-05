// Package sse is a minimal Server-Sent Events parser sufficient for
// monstermailbox's /events stream.
//
// What we parse, per the W3C spec subset the server emits:
//
//	event: <name>\n
//	data:  <line>\n
//	data:  <line>\n   (multiple data lines join with \n)
//	\n                (blank line dispatches the event)
//
// Comments (lines starting with ":") are heartbeats — we surface them
// as a special "comment" event so `inbox watch` reconnect logic can
// reset the disconnect timer when the server proves it's alive but
// nothing semantic is happening.
//
// Lines we ignore: id:, retry:, BOM. Adding them would not change
// behavior for monstermailbox's stream and would muddy the test
// surface.
package sse

import (
	"bufio"
	"io"
	"strings"
)

// Event is one dispatched SSE event. Empty Name + empty Data with
// IsComment=true represents a heartbeat-comment line from the server.
type Event struct {
	Name      string
	Data      string
	IsComment bool
}

// Reader parses an SSE stream from any io.Reader. Construct with New
// then call Next repeatedly. Returns io.EOF when the underlying stream
// closes — caller decides whether to reconnect.
type Reader struct {
	scanner *bufio.Scanner
}

// New wraps r in a line-oriented scanner sized for a 1MB max event —
// generous for a JSON payload. Larger payloads truncate the line and
// produce a partial parse, which is the same failure mode you'd get
// from any line-based reader.
func New(r io.Reader) *Reader {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	return &Reader{scanner: sc}
}

// Next reads from the stream until it dispatches an Event or hits EOF.
// Returns:
//
//	(event, nil)  — one parsed event ready for the caller
//	(zero,  err)  — io.EOF on clean close, or scanner error
//
// Blank-line semantics: a complete event is dispatched when a blank
// line follows one or more `event:`/`data:` lines.
func (r *Reader) Next() (Event, error) {
	var (
		name string
		data []string
	)

	for r.scanner.Scan() {
		line := r.scanner.Text()

		// Comment lines start with ':' — dispatch as a comment event
		// IMMEDIATELY (don't wait for blank line). The server's heartbeat
		// is `: heartbeat\n\n`; treating the comment line itself as the
		// dispatch point lets watch's reconnect timer reset on heartbeat.
		if strings.HasPrefix(line, ":") {
			return Event{IsComment: true, Data: strings.TrimPrefix(line, ":")}, nil
		}

		// Blank line: dispatch the accumulated event (if any).
		if line == "" {
			if name == "" && len(data) == 0 {
				continue
			}
			return Event{Name: name, Data: strings.Join(data, "\n")}, nil
		}

		field, value, ok := splitField(line)
		if !ok {
			continue
		}
		switch field {
		case "event":
			name = value
		case "data":
			data = append(data, value)
		default:
			// id, retry, etc. — ignored. See pkg comment.
		}
	}
	if err := r.scanner.Err(); err != nil {
		return Event{}, err
	}
	return Event{}, io.EOF
}

// splitField splits "field: value" into (field, value). Per the SSE
// spec, the leading space after the colon is stripped if present.
// Returns ok=false on lines with no colon.
func splitField(line string) (string, string, bool) {
	i := strings.IndexByte(line, ':')
	if i < 0 {
		return "", "", false
	}
	field := line[:i]
	value := line[i+1:]
	value = strings.TrimPrefix(value, " ")
	return field, value, true
}
