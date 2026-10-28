package tmux

import (
	"encoding/json"
	"os/exec"
	"testing"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/tools/core"
	"github.com/invokr-agent/invokr/tools/system/workspace"
)

// Negative geometry and scrollback values fall back to the defaults, exactly
// like zero: a model passing -1 must not get a 20x5 pane or lose history.
func TestPrepareTmuxNegativeLimitsUseDefaults(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	call := &core.ToolCall{Name: "tmux", Arguments: json.RawMessage(`{"action":"open","command":"cat","width":-5,"height":-5,"lines":-5}`)}
	plan, err := prepareTmux(call, workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: t.TempDir()}))
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if plan.args.Width != 80 || plan.args.Height != 24 || plan.args.Lines != 200 {
		t.Fatalf("got width=%d height=%d lines=%d, want 80/24/200", plan.args.Width, plan.args.Height, plan.args.Lines)
	}
}
