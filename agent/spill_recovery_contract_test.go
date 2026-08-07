package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/tools"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/read_file"
	"github.com/invokr-ai/invokr/tools/system/workspace"
)

func TestSpillRecoveryExampleInvokesRegisteredRead(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "résult.txt")
	const full = "first line\nsecond line\n"
	if err := os.WriteFile(path, []byte(full), 0o600); err != nil {
		t.Fatal(err)
	}
	stub := spillStub(full, path, 128)
	_, suffix, ok := strings.Cut(stub, "Page through it with read, e.g. ")
	if !ok {
		t.Fatalf("unexpected recovery contract: %s", stub)
	}
	var args json.RawMessage
	if err := json.NewDecoder(strings.NewReader(suffix)).Decode(&args); err != nil {
		t.Fatalf("invalid recovery JSON: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(args, &fields); err != nil || fields["paths"] == nil || fields["path"] != nil {
		t.Fatalf("example must use the paths schema: %s, %v", args, err)
	}
	caller := &tools.Caller{}
	read_file.RegisterReadFileTool(caller, workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root}))
	got, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "read", Arguments: args})
	if err != nil || !strings.Contains(got, full) {
		t.Fatalf("advertised example cannot recover the file: %q, %v", got, err)
	}
}
