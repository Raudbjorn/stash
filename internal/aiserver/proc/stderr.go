package proc

import (
	"bufio"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/stashapp/stash/pkg/logger"
)

// RingBuffer keeps a bounded, oldest-first tail of process output.
type RingBuffer struct {
	mu    sync.Mutex
	lines []string
	next  int
	full  bool
}

// NewRingBuffer constructs a line buffer, defaulting to 200 lines.
func NewRingBuffer(size int) *RingBuffer {
	if size <= 0 {
		size = 200
	}
	return &RingBuffer{lines: make([]string, size)}
}

// Add appends a line, discarding the oldest line when full.
func (r *RingBuffer) Add(line string) {
	if r == nil || len(r.lines) == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines[r.next] = line
	r.next = (r.next + 1) % len(r.lines)
	if r.next == 0 {
		r.full = true
	}
}

// Snapshot returns buffered lines oldest-first.
func (r *RingBuffer) Snapshot() []string {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.full {
		return append([]string(nil), r.lines[:r.next]...)
	}
	out := make([]string, 0, len(r.lines))
	out = append(out, r.lines[r.next:]...)
	out = append(out, r.lines[:r.next]...)
	return out
}

// Reset clears buffered output for a new process generation.
func (r *RingBuffer) Reset() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.next = 0
	r.full = false
	r.mu.Unlock()
}

// PumpStderr logs non-empty lines and retains their bounded tail.
func PumpStderr(name string, reader io.ReadCloser, buffer *RingBuffer, done func()) {
	if done != nil {
		defer done()
	}
	defer reader.Close()
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == "" {
			continue
		}
		buffer.Add(line)
		logger.Debugf("[%s] %s", name, line)
	}
}

// AppendPathEnv prepends dir to a path-style environment variable.
func AppendPathEnv(env []string, key, dir string) []string {
	prefix := key + "="
	for i, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			existing := strings.TrimPrefix(entry, prefix)
			if existing != "" {
				dir += string(os.PathListSeparator) + existing
			}
			env[i] = prefix + dir
			return env
		}
	}
	return append(env, prefix+dir)
}
