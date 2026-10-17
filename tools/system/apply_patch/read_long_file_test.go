package apply_patch_test

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/tools/core"
)

// read_file's window defaults, mirrored so the tests exercise the real
// boundary rather than a guess at it.
const (
	readDefaultLineLimit = 2000
	readMaxBytes         = 256 << 10
)

func readPage(t *testing.T, root, path string, offset int) string {
	t.Helper()
	args := map[string]any{"paths": []string{path}}
	if offset > 0 {
		args["offset"] = offset
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := newApplyPatchTestCaller(t, root).HandleCall(
		context.Background(), &core.ToolCall{Name: "read", Arguments: encoded})
	if err != nil {
		t.Fatalf("read_file offset=%d: %v", offset, err)
	}
	return out
}

// splitMarker separates the file content from the trailing continuation
// marker, which is the boundary a model has to get right to quote context.
func splitMarker(text string) (content, marker string) {
	idx := strings.Index(text, "[read_file: lines ")
	if idx < 0 {
		return text, ""
	}
	return text[:idx], text[idx:]
}

func numberedFile(lines int) string {
	var b strings.Builder
	for i := 1; i <= lines; i++ {
		fmt.Fprintf(&b, "line %d content\n", i)
	}
	return b.String()
}

// A file past the line limit must return exactly the first window, byte for
// byte, with the marker clearly separated from content.
func TestReadFileLongFileWindowIsExact(t *testing.T) {
	root := t.TempDir()
	whole := numberedFile(readDefaultLineLimit + 500)
	mustWriteFile(t, filepath.Join(root, "big.txt"), whole)

	content, marker := splitMarker(readPage(t, root, "big.txt", 0))
	want := numberedFile(readDefaultLineLimit)
	if content != want {
		t.Fatalf("window is not the first %d lines verbatim (got %d bytes, want %d)",
			readDefaultLineLimit, len(content), len(want))
	}
	if marker == "" {
		t.Fatal("truncated read returned no continuation marker")
	}
	// The marker must start its own line, or a model quoting the tail of the
	// window silently pulls harness text into its patch context.
	if !strings.HasSuffix(content, "\n") {
		t.Fatalf("content does not end with a terminator before the marker: %q", tailOf(content))
	}
	if !strings.Contains(marker, fmt.Sprintf(`{"offset":%d}`, readDefaultLineLimit+1)) {
		t.Fatalf("marker does not name the next offset: %q", marker)
	}
}

// Paging with the offset the marker gives must cover the file exactly: no
// gap, no repeat. A model that follows the instruction gets the whole file.
func TestReadFilePagingReconstructsFileExactly(t *testing.T) {
	root := t.TempDir()
	whole := numberedFile(readDefaultLineLimit*2 + 137)
	mustWriteFile(t, filepath.Join(root, "big.txt"), whole)

	var rebuilt strings.Builder
	for offset, pages := 1, 0; ; pages++ {
		if pages > 10 {
			t.Fatal("paging did not terminate")
		}
		content, marker := splitMarker(readPage(t, root, "big.txt", offset))
		rebuilt.WriteString(content)
		if marker == "" {
			break
		}
		offset += strings.Count(content, "\n")
	}
	if rebuilt.String() != whole {
		t.Fatalf("paged reads do not reconstruct the file: got %d bytes, want %d",
			rebuilt.Len(), len(whole))
	}
}

// Context quoted from the far end of a truncated window still has to apply —
// that is where a model works when it edits a long file.
func TestReadFileLongFileTailContextApplies(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "big.txt"), numberedFile(readDefaultLineLimit+500))

	content, _ := splitMarker(readPage(t, root, "big.txt", 0))
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	tail := lines[len(lines)-4:]

	var patch strings.Builder
	patch.WriteString("*** Begin Patch\n*** Update File: big.txt\n@@\n")
	for _, line := range tail[:len(tail)-1] {
		patch.WriteString(" " + line + "\n")
	}
	patch.WriteString("-" + tail[len(tail)-1] + "\n+REPLACED\n*** End Patch")

	if err := applyPatchText(t, root, patch.String()); err != nil {
		t.Fatalf("context from the end of a truncated window did not apply: %v", err)
	}
}

// A file under the line limit but over the byte cap is truncated by bytes;
// the window must still be whole lines, byte-identical, and marked.
func TestReadFileByteCapTruncatesOnLineBoundary(t *testing.T) {
	root := t.TempDir()
	line := strings.Repeat("x", 1024) + "\n"
	whole := strings.Repeat(line, (readMaxBytes/len(line))+50)
	mustWriteFile(t, filepath.Join(root, "wide.txt"), whole)

	content, marker := splitMarker(readPage(t, root, "wide.txt", 0))
	if marker == "" {
		t.Fatal("byte-capped read returned no continuation marker")
	}
	if !strings.HasSuffix(content, "\n") {
		t.Fatalf("byte cap split a line: %q", tailOf(content))
	}
	if !strings.HasPrefix(whole, content) {
		t.Fatal("byte-capped window is not a verbatim prefix of the file")
	}
}

func tailOf(s string) string {
	if len(s) <= 60 {
		return s
	}
	return "…" + s[len(s)-60:]
}

// A line past the scanner cap fails the whole read rather than returning a
// split line — which keeps Marker()'s "always ends with a terminator" claim
// true, at the cost of minified files being unreadable. Characterized here so
// the trade-off is visible if anyone revisits the cap.
func TestReadFileRejectsLineOverScannerCap(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "min.js"), strings.Repeat("x", (5<<20)+16)+"\n")

	encoded, err := json.Marshal(map[string]any{"paths": []string{"min.js"}})
	if err != nil {
		t.Fatal(err)
	}
	_, callErr := newApplyPatchTestCaller(t, root).HandleCall(
		context.Background(), &core.ToolCall{Name: "read", Arguments: encoded})
	if callErr == nil {
		t.Fatal("expected an over-long line to be rejected")
	}
	if !strings.Contains(callErr.Error(), "line longer than") {
		t.Fatalf("error does not say why the read failed: %v", callErr)
	}
}

// Quoting the continuation marker as if it were file content cannot work: it
// is harness text, not file text. Characterized so the failure is understood
// as a model mistake the marker's wording invites, not a matching bug.
func TestReadFileMarkerQuotedAsContextFails(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "big.txt"), numberedFile(readDefaultLineLimit+10))

	content, marker := splitMarker(readPage(t, root, "big.txt", 0))
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	patch := "*** Begin Patch\n*** Update File: big.txt\n@@\n" +
		" " + lines[len(lines)-1] + "\n" +
		" " + marker + "\n+REPLACED\n*** End Patch"

	if err := applyPatchText(t, root, patch); err == nil {
		t.Fatal("marker quoted as context was accepted as file content")
	}
}
