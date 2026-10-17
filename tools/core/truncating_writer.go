package core

import (
	"bytes"
	"io"
	"sync"
)

// TruncatingWriters caps the combined stdout+stderr a subprocess may buffer.
// Once the shared byte budget is exhausted it silently drops further output
// (the process keeps running) and records that truncation happened. This is a
// real memory guard: bytes past the limit are never buffered.
type TruncatingWriters struct {
	mu             sync.Mutex
	remaining      int
	cut            bool
	stdout, stderr *truncatingBuffer
}

type truncatingBuffer struct {
	parent *TruncatingWriters
	buf    bytes.Buffer
}

// NewTruncatingWriters returns writers sharing a combined byte budget of limit.
func NewTruncatingWriters(limit int) *TruncatingWriters {
	parent := &TruncatingWriters{remaining: limit}
	parent.stdout = &truncatingBuffer{parent: parent}
	parent.stderr = &truncatingBuffer{parent: parent}
	return parent
}

func (w *TruncatingWriters) Stdout() io.Writer { return w.stdout }

func (w *TruncatingWriters) Stderr() io.Writer { return w.stderr }

func (w *TruncatingWriters) StdoutString() string { return w.stdout.String() }

func (w *TruncatingWriters) StderrString() string { return w.stderr.String() }

// Truncated reports whether any output was dropped because the budget ran out.
func (w *TruncatingWriters) Truncated() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cut
}

func (b *truncatingBuffer) Write(p []byte) (int, error) {
	b.parent.mu.Lock()
	defer b.parent.mu.Unlock()

	written := len(p)
	if b.parent.remaining <= 0 {
		b.parent.cut = true
		return written, nil
	}
	if len(p) > b.parent.remaining {
		p = p[:b.parent.remaining]
		b.parent.cut = true
	}
	b.parent.remaining -= len(p)
	_, _ = b.buf.Write(p)
	return written, nil
}

func (b *truncatingBuffer) String() string {
	b.parent.mu.Lock()
	defer b.parent.mu.Unlock()
	return b.buf.String()
}
