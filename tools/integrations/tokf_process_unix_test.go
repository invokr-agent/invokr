//go:build unix

package integrations

import (
	"context"
	"github.com/invokr-ai/invokr/tools/core"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTokfResultFilterCancelsTokfProcessGroup(t *testing.T) {
	root := t.TempDir()
	fakeTokf := filepath.Join(root, "tokf")
	childMarker := filepath.Join(root, "child-survived")

	script := `#!/bin/sh
(sh -c 'sleep 1; printf child > "$TOKF_CHILD_MARKER"') &
wait
`
	if err := os.WriteFile(fakeTokf, []byte(script), 0755); err != nil {
		t.Fatalf("write fake tokf: %v", err)
	}

	t.Setenv("TOKF_CHILD_MARKER", childMarker)

	filter := &TokfResultFilter{
		bin:       fakeTokf,
		helperBin: "/tmp/github.com/invokr-ai/invokr",
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	got, err := filter.FilterToolResult(ctx, core.ToolResultMeta{ToolName: "shell", Source: "toolbox"}, "raw result")
	if err != nil {
		t.Fatalf("FilterToolResult returned error: %v", err)
	}
	if got != "raw result" {
		t.Fatalf("expected unfiltered fallback output after cancellation, got %q", got)
	}

	time.Sleep(1200 * time.Millisecond)
	if _, err := os.Stat(childMarker); !os.IsNotExist(err) {
		t.Fatalf("tokf child process survived cancellation; marker stat error = %v", err)
	}
}
