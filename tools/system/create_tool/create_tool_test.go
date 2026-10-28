package create_tool

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/tools/core"
	"github.com/invokr-agent/invokr/tools/system/toolbox"
)

func TestCreateToolSchemaRequiresCapabilities(t *testing.T) {
	r := &captureCreateToolRegistrar{}
	RegisterCreateToolTool(r, core.CallerDeps{})
	required, ok := r.spec.Parameters["required"].([]string)
	if !ok || !slices.Contains(required, "capabilities") {
		t.Fatalf("create_tool required fields = %v, want capabilities", r.spec.Parameters["required"])
	}
}

type captureCreateToolRegistrar struct{ spec core.ToolSpec }

func (r *captureCreateToolRegistrar) RegisterNative(spec core.ToolSpec, _ core.NativeHandler) {
	r.spec = spec
}
func (*captureCreateToolRegistrar) HasNative(string) bool      { return false }
func (*captureCreateToolRegistrar) NativeSource(string) string { return "" }
func (*captureCreateToolRegistrar) ReplaceNativeToolsBySource(string, []core.DynamicNativeTool) []string {
	return nil
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
