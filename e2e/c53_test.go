//go:build e2e

package e2e

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

// TestC53_ReadFilePagesLargeFile drives the native read paging contract:
// a file over the 2000-line default window comes back truncated with a marker
// naming the next offset, and the follow-up call continues with no duplicated
// or skipped line.
func TestC53_ReadFilePagesLargeFile(t *testing.T) {
	provider := providertest.NewScripted(t,
		providertest.NativeToolCallStep("c53-read-1", "read", map[string]any{"paths": []string{"big.txt"}}),
		providertest.NativeToolCallStep("c53-read-2", "read", map[string]any{"paths": []string{"big.txt"}, "offset": 2001}),
		providertest.ReplyStep("done"),
	)
	defer provider.Close()
	home := newHome(t, provider.URL())
	var b strings.Builder
	for i := 1; i <= 2500; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	mustWrite(t, filepath.Join(home.workspace, "big.txt"), b.String())

	result := runExec(t, home, provider.URL(), "read the big file")
	if result.Err != nil {
		t.Fatalf("exec failed: %v\nstderr: %s", result.Err, result.Stderr)
	}
	snapshot := provider.Snapshot()

	first := toolResultForCall(snapshot.CompletionRequests, "c53-read-1")
	wantMarker := `[read_file: lines 1-2000 of 2500 (truncated); call again with {"offset":2001}]`
	if !strings.HasPrefix(first, "line 1\n") || !strings.HasSuffix(first, wantMarker) {
		t.Fatalf("first page = %.80q ... %.120q", first, first[max(len(first)-120, 0):])
	}
	if !strings.Contains(first, "\nline 2000\n") {
		t.Fatal("first page does not end at line 2000")
	}

	second := toolResultForCall(snapshot.CompletionRequests, "c53-read-2")
	if !strings.HasPrefix(second, "line 2001\n") || !strings.HasSuffix(second, "line 2500\n") {
		t.Fatalf("second page boundaries wrong: %.80q ... %.80q", second, second[max(len(second)-80, 0):])
	}
	if strings.Contains(second, "[read_file:") {
		t.Fatalf("complete second page carries a marker: %.120q", second[max(len(second)-120, 0):])
	}
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}
