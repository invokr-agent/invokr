package create_tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/toolbox"
)

func TestExampleReadsToolCallArgumentsEnvelope(t *testing.T) {
	if !strings.Contains(createToolExample, `call.get(\"arguments\")`) {
		t.Fatalf("create-tool example must read the arguments envelope: %s", createToolExample)
	}
}

func TestInstallStagedToolRollsBackFailedReplacement(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "demo")
	if err := os.MkdirAll(live, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(live, "main.py"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	missingStage := filepath.Join(root, "missing-stage")
	if err := installStagedTool(missingStage, live, true); err == nil {
		t.Fatal("expected missing staging directory to fail")
	}
	data, err := os.ReadFile(filepath.Join(live, "main.py"))
	if err != nil {
		t.Fatalf("previous tool was not restored: %v", err)
	}
	if string(data) != "old" {
		t.Fatalf("restored tool content = %q, want old", data)
	}
}

func TestDefaultPolicyFillsOmittedFields(t *testing.T) {
	got := defaultPolicy(core.ToolPolicy{})
	if len(got.Tiers) != 1 || got.Tiers[0] != constants.CodeInterpreterTierReadWorkspace || got.Approval != constants.ApprovalPolicyAsk {
		t.Fatalf("defaultPolicy = %+v", got)
	}
	kept := defaultPolicy(core.ToolPolicy{Tiers: []string{"write_workspace"}, Approval: "auto"})
	if kept.Tiers[0] != "write_workspace" || kept.Approval != "auto" {
		t.Fatalf("defaultPolicy overwrote explicit fields: %+v", kept)
	}
	if err := toolbox.ValidateSpecFields("t", "d", "e", got); err != nil {
		t.Fatalf("defaulted policy fails validation: %v", err)
	}
}
