package code_interpreter

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/tools/system/workspace"
)

func TestRunAuditsOutputCopyFailure(t *testing.T) {
	root := t.TempDir()
	dispatcher := &captureDispatcher{}
	service := NewService(config.CodeInterpreterConfig{
		Backend:    constants.CodeInterpreterBackendProcess,
		SessionDir: filepath.Join(root, ".ci"),
	}, workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root}), dispatcher)
	plan, err := service.Prepare(Args{
		Code:        "print('done')",
		OutputFiles: []string{"result.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	service.backend = func(context.Context, prepared) (Result, error) {
		return Result{ExitCode: 0}, nil
	}

	if _, err := service.Run(context.Background(), plan); err == nil {
		t.Fatal("expected artifact write-back to fail when the session directory is missing")
	}
	if len(dispatcher.events) != 1 || dispatcher.events[0].Audit == nil {
		t.Fatalf("expected one audit event, got %#v", dispatcher.events)
	}
	audit := dispatcher.events[0].Audit
	success, ok := audit.Details["success"].(bool)
	if audit.Outcome != "failed" || audit.Error == "" || !ok || success {
		t.Fatalf("unexpected write-back failure audit: %#v", audit)
	}
}
