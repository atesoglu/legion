//go:build e2e

package e2e

import (
	"bytes"
	"sync"
)

// logBuffer collects a child process's output. The process writes from its own
// goroutine while the test reads on failure, so the buffer must be guarded.
type logBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *logBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *logBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}
