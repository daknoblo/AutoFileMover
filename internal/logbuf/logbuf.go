// Package logbuf provides an slog handler writer that tees log output to a
// destination (stdout) while keeping the most recent lines in memory so they
// can be shown in the web UI. The log level is adjustable at runtime.
package logbuf

import (
	"io"
	"log/slog"
	"strings"
	"sync"
)

// Buffer is an io.Writer that stores recent log lines and forwards them to an
// underlying writer.
type Buffer struct {
	mu    sync.Mutex
	lines []string
	max   int
	out   io.Writer
}

// New creates a buffer keeping up to max lines, mirroring writes to out.
func New(max int, out io.Writer) *Buffer {
	return &Buffer{max: max, out: out}
}

// Write implements io.Writer.
func (b *Buffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	for _, line := range strings.Split(strings.TrimRight(string(p), "\n"), "\n") {
		if line == "" {
			continue
		}
		b.lines = append(b.lines, line)
	}
	if len(b.lines) > b.max {
		b.lines = b.lines[len(b.lines)-b.max:]
	}
	b.mu.Unlock()
	return b.out.Write(p)
}

// Lines returns a copy of the buffered lines, newest last.
func (b *Buffer) Lines() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	return out
}

// ParseLevel maps a string to an slog.Level, defaulting to info.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// LevelName returns the lower-case name of a level.
func LevelName(l slog.Level) string {
	switch l {
	case slog.LevelDebug:
		return "debug"
	case slog.LevelWarn:
		return "warn"
	case slog.LevelError:
		return "error"
	default:
		return "info"
	}
}
