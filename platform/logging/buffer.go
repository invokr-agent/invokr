package logging

import (
	"strings"
	"sync"
)

type Buffer struct {
	mu      sync.Mutex
	limit   int
	lines   []string
	partial string
}

func NewBuffer(limit int) *Buffer {
	if limit <= 0 {
		limit = 200
	}
	return &Buffer{limit: limit}
}

func (b *Buffer) Write(p []byte) (int, error) {
	if b == nil {
		return len(p), nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	text := b.partial + string(p)
	parts := strings.Split(text, "\n")
	b.partial = parts[len(parts)-1]
	for _, line := range parts[:len(parts)-1] {
		b.appendLocked(strings.TrimRight(line, "\r"))
	}
	return len(p), nil
}

func (b *Buffer) Lines() []string {
	if b == nil {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	lines := make([]string, 0, len(b.lines)+1)
	lines = append(lines, b.lines...)
	if strings.TrimSpace(b.partial) != "" {
		lines = append(lines, b.partial)
	}
	return lines
}

func (b *Buffer) appendLocked(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	b.lines = append(b.lines, line)
	if len(b.lines) > b.limit {
		copy(b.lines, b.lines[len(b.lines)-b.limit:])
		b.lines = b.lines[:b.limit]
	}
}
