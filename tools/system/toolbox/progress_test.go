package toolbox

import (
	"context"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/tools/core"
)

// newProgressWriter builds the stderr tee in isolation, so the cases below
// exercise the buffering itself rather than a plugin's flush timing.
func newProgressWriter() (*progressWriter, *recordingDispatcher) {
	dispatcher := &recordingDispatcher{}
	tool := &executableTool{ToolSpec: core.ToolSpec{Name: "progress_writer"}, dispatcher: dispatcher}
	return &progressWriter{ctx: context.Background(), tool: tool}, dispatcher
}

func writeAll(t *testing.T, w *progressWriter, chunks ...string) {
	t.Helper()
	for _, chunk := range chunks {
		// A short write would truncate the captured stderr this writer is
		// teed into, so the io.Writer contract is part of what is asserted.
		if n, err := w.Write([]byte(chunk)); err != nil || n != len(chunk) {
			t.Fatalf("Write(%d bytes) = %d, %v", len(chunk), n, err)
		}
	}
}

// A plugin's stderr arrives as a byte stream, not as whole lines. Feeding this
// writer already-split lines proves nothing — a naive per-Write bytes.Split
// passes that. These are the cases that separate the two.
func TestProgressWriterBuffersAcrossWrites(t *testing.T) {
	t.Run("line split across writes", func(t *testing.T) {
		w, dispatcher := newProgressWriter()
		for _, b := range []byte(constants.ToolboxProgressPrefix + " alpha\n") {
			writeAll(t, w, string(b))
		}
		if got := dispatcher.progress(); len(got) != 1 || got[0] != "alpha" {
			t.Fatalf("progress = %v, want one event %q reassembled from single-byte writes", got, "alpha")
		}
	})

	t.Run("several lines in one write", func(t *testing.T) {
		w, dispatcher := newProgressWriter()
		prefix := constants.ToolboxProgressPrefix
		writeAll(t, w, prefix+" one\nnoise\n"+prefix+" two\n")
		if got := dispatcher.progress(); len(got) != 2 || got[0] != "one" || got[1] != "two" {
			t.Fatalf("progress = %v, want both prefixed lines from a single write", got)
		}
	})

	t.Run("unterminated tail at EOF", func(t *testing.T) {
		w, dispatcher := newProgressWriter()
		writeAll(t, w, constants.ToolboxProgressPrefix+" partial")
		if got := dispatcher.progress(); len(got) != 0 {
			t.Fatalf("progress = %v, want none: an unterminated line is not a line yet", got)
		}
		writeAll(t, w, "\n")
		if got := dispatcher.progress(); len(got) != 1 || got[0] != "partial" {
			t.Fatalf("progress = %v, want the tail delivered once its newline arrives", got)
		}
	})

	t.Run("oversized line is dropped without wedging the stream", func(t *testing.T) {
		w, dispatcher := newProgressWriter()
		writeAll(t, w, constants.ToolboxProgressPrefix+" "+strings.Repeat("x", 4*progressLineCap))
		if got := dispatcher.progress(); len(got) != 0 {
			t.Fatalf("progress = %v, want none while the line is still unterminated", got)
		}
		writeAll(t, w, "\n"+constants.ToolboxProgressPrefix+" after\n")
		got := dispatcher.progress()
		if len(got) != 1 || got[0] != "after" {
			t.Fatalf("progress = %v, want only %q: the capped line is dropped and the stream recovers", got, "after")
		}
	})
}

func TestProgressWriterPreservesCallID(t *testing.T) {
	w, d := newProgressWriter()
	w.ctx = context.WithValue(core.WithSessionID(w.ctx, "session"), core.ToolCallIDKey{}, "first")
	writeAll(t, w, constants.ToolboxProgressPrefix+" working\n")
	if len(d.events) != 1 || d.events[0].Tool.ID != "first" || d.events[0].SessionID != "session" {
		t.Fatalf("events = %#v", d.events)
	}
}
