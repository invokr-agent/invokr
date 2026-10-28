package apply_patch_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/tools/core"
)

// The read → patch round trip, with no model in the loop.
//
// A model edits a file by reading it and quoting lines back as hunk context.
// That only works if what read_file returns is usable verbatim as context, so
// this asserts the property directly: read a file through the real tool, build
// a hunk whose context lines are exactly what came back, and apply it. Any
// case that fails here is a hunk-context failure the harness causes and no
// model can avoid — which is worth separating from the model simply
// mis-transcribing, the dominant failure in the benchmark runs.
func roundTripCases() []struct{ name, content string } {
	return []struct{ name, content string }{
		{"trailing newline", "alpha\nbeta\ngamma\n"},
		{"no trailing newline", "alpha\nbeta\ngamma"},
		{"blank lines", "alpha\n\n\nbeta\n"},
		{"tabs and spaces", "def f():\n\tx = 1\n    y = 2\n"},
		{"unicode", "héllo\nünïcode ✓\nlast\n"},
		{"trailing whitespace", "alpha   \nbeta\t\ngamma\n"},
		{"long line", strings.Repeat("x", 4096) + "\nsecond\n"},
		{"crlf", "alpha\r\nbeta\r\ngamma\r\n"},
		{"patch marker lookalikes", "*** Begin Patch\n@@ not a hunk\n+not an addition\nlast\n"},
	}
}

func readFileThroughCaller(t *testing.T, root, path string) string {
	t.Helper()
	args, err := json.Marshal(map[string]any{"paths": []string{path}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := newApplyPatchTestCaller(t, root).HandleCall(
		context.Background(), &core.ToolCall{Name: "read", Arguments: args})
	if err != nil {
		t.Fatalf("read_file: %v", err)
	}
	return out
}

func TestReadFileReturnsBytesIdenticalToFile(t *testing.T) {
	for _, testCase := range roundTripCases() {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			mustWriteFile(t, filepath.Join(root, "f.txt"), testCase.content)
			if got := readFileThroughCaller(t, root, "f.txt"); got != testCase.content {
				t.Fatalf("read_file is not byte-identical to the file\n got: %q\nwant: %q", got, testCase.content)
			}
		})
	}
}

// Replacing the last line, with every preceding line quoted verbatim from
// read_file as context, is the smallest edit a model can make. It must apply.
func TestReadFileOutputWorksAsPatchContext(t *testing.T) {
	for _, testCase := range roundTripCases() {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			mustWriteFile(t, filepath.Join(root, "f.txt"), testCase.content)

			seen := readFileThroughCaller(t, root, "f.txt")
			lines := strings.Split(strings.TrimSuffix(seen, "\n"), "\n")
			if len(lines) < 2 {
				t.Skipf("case needs at least two lines, got %d", len(lines))
			}

			var patch strings.Builder
			patch.WriteString("*** Begin Patch\n*** Update File: f.txt\n@@\n")
			for _, line := range lines[:len(lines)-1] {
				patch.WriteString(" " + line + "\n")
			}
			patch.WriteString("-" + lines[len(lines)-1] + "\n+REPLACED\n*** End Patch")

			if err := applyPatchText(t, root, patch.String()); err != nil {
				t.Fatalf("context quoted verbatim from read_file did not apply: %v\npatch:\n%s", err, patch.String())
			}
			got, err := os.ReadFile(filepath.Join(root, "f.txt"))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(got), "REPLACED") {
				t.Fatalf("patch applied but content is wrong: %q", string(got))
			}
			// Matching a CRLF file requires normalizing it; the file's own
			// endings must survive that, not be quietly rewritten to LF.
			if strings.Contains(testCase.content, "\r\n") && !strings.Contains(string(got), "\r\n") {
				t.Fatalf("CRLF line endings were lost: %q", string(got))
			}
			if !strings.Contains(testCase.content, "\r") && strings.Contains(string(got), "\r") {
				t.Fatalf("CR was introduced into an LF file: %q", string(got))
			}
		})
	}
}
