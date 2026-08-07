package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLines(t *testing.T, n int) string {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	path := filepath.Join(t.TempDir(), "f.txt")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadLineWindowCountsTotalLines(t *testing.T) {
	path := writeLines(t, 50)
	window, err := ReadLineWindow(path, 10, 5, 0)
	if err != nil {
		t.Fatalf("ReadLineWindow: %v", err)
	}
	if window.Start != 10 || window.End != 14 || window.Total != 50 || !window.Truncated {
		t.Fatalf("window = %+v", window)
	}
	if window.Text != "line 10\nline 11\nline 12\nline 13\nline 14\n" {
		t.Fatalf("text = %q", window.Text)
	}
	if !strings.Contains(window.Marker(), `{"offset":15}`) {
		t.Fatalf("marker = %q", window.Marker())
	}

	full, err := ReadLineWindow(path, 1, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	if full.Truncated || full.Marker() != "" || full.Total != 50 {
		t.Fatalf("full window = %+v marker=%q", full, full.Marker())
	}
}

func TestReadLineWindowStopsAtByteCap(t *testing.T) {
	path := writeLines(t, 100)
	window, err := ReadLineWindow(path, 1, 100, 30)
	if err != nil {
		t.Fatalf("ReadLineWindow: %v", err)
	}
	if !window.Truncated || window.End >= 100 || window.Total != 100 {
		t.Fatalf("window = %+v", window)
	}
	if len(window.Text) > 30+len("line 100") {
		t.Fatalf("text overflows the byte cap: %d bytes", len(window.Text))
	}
}

func TestReadLineWindowRejectsOffsetPastEOF(t *testing.T) {
	path := writeLines(t, 3)
	if _, err := ReadLineWindow(path, 10, 5, 0); err == nil || !strings.Contains(err.Error(), "past the end") {
		t.Fatalf("expected offset-past-EOF error, got %v", err)
	}
}

func TestLooksBinaryDetectsNulAndInvalidUTF8(t *testing.T) {
	if LooksBinary("plain text line") {
		t.Fatal("plain text flagged binary")
	}
	if !LooksBinary("nul\x00byte") {
		t.Fatal("NUL byte not flagged")
	}
	if !LooksBinary(string([]byte{0xff, 0xfe, 0x41})) {
		t.Fatal("invalid UTF-8 not flagged")
	}
}
