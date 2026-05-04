// Package log writes the daemon's events to ~/.mmb-bridge/bridge.log
// AND optionally mirrors to stderr (for `mmb-bridge start --foreground`).
//
// Format is line-oriented `2026-05-03T18:42:11Z [INFO] message…` —
// not full JSON because `mmb-bridge logs -f` is intended to be human-
// readable. The cmd/logs.go tail with `-f` reads this file directly.
package log

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/theinventor/monstermailbox-cli/bridge/internal/config"
)

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

type Logger struct {
	mu     sync.Mutex
	file   *os.File
	stderr bool
	level  Level
}

// New opens (or creates) ~/.mmb-bridge/bridge.log for append. Pass
// stderrToo=true to mirror lines to stderr — useful in foreground
// mode and during init where the user wants real-time feedback.
func New(stderrToo bool, level Level) (*Logger, error) {
	dir, err := config.Dir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "bridge.log")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	return &Logger{file: f, stderr: stderrToo, level: level}, nil
}

// Path returns the absolute log-file path so `mmb-bridge logs` can
// tail it without re-deriving the path.
func Path() (string, error) {
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "bridge.log"), nil
}

func (l *Logger) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.Close()
}

func (l *Logger) write(level Level, prefix, format string, args ...any) {
	if l == nil || level < l.level {
		return
	}
	line := fmt.Sprintf("%s [%s] %s\n",
		time.Now().UTC().Format(time.RFC3339), prefix, fmt.Sprintf(format, args...))
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		_, _ = l.file.WriteString(line)
	}
	if l.stderr {
		_, _ = os.Stderr.WriteString(line)
	}
}

func (l *Logger) Debugf(f string, a ...any) { l.write(LevelDebug, "DEBUG", f, a...) }
func (l *Logger) Infof(f string, a ...any)  { l.write(LevelInfo, "INFO", f, a...) }
func (l *Logger) Warnf(f string, a ...any)  { l.write(LevelWarn, "WARN", f, a...) }
func (l *Logger) Errorf(f string, a ...any) { l.write(LevelError, "ERROR", f, a...) }
