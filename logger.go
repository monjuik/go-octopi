package gooctopi

import (
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

// Logger abstracts logging behaviour used across the project.
type Logger interface {
	Printf(format string, args ...any)
}

// NewLogger returns a logger that writes formatted entries with a timestamp and tag.
func NewLogger(tag string) Logger {
	return &structuredLogger{
		tag:    tag,
		writer: os.Stdout,
	}
}

// NewDiscardLogger returns a logger that drops all log entries (useful in tests).
func NewDiscardLogger() Logger {
	return discardLogger{}
}

type structuredLogger struct {
	tag    string
	writer io.Writer
	mu     sync.Mutex
}

func (l *structuredLogger) Printf(format string, args ...any) {
	if l == nil {
		return
	}
	timestamp := time.Now().UTC().Format("2006/01/02 15:04:05.000")
	message := fmt.Sprintf(format, args...)
	l.mu.Lock()
	defer l.mu.Unlock()
	fmt.Fprintf(l.writer, "%s [%s] %s\n", timestamp, l.tag, message)
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}
