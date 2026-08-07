package agentui

import (
	"strings"
	"sync"
	"testing"
)

// countingWriter is deliberately not concurrency-safe: under -race, an
// unguarded writePlain would report a data race on buf.
type countingWriter struct{ buf strings.Builder }

func (w *countingWriter) Write(p []byte) (int, error) { return w.buf.Write(p) }

func TestWritePlainSerializesConcurrentWrites(t *testing.T) {
	w := &countingWriter{}
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			writePlain(w, "ab")
		}()
	}
	wg.Wait()

	if got := w.buf.String(); got != strings.Repeat("ab", 50) {
		t.Fatalf("interleaved plain-mode output: %q", got)
	}
}
