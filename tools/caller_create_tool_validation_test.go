package tools

import (
	"context"
	"encoding/json"
	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/support/testutil"
	"github.com/invokr-ai/invokr/tools/core"
	"os"
	"path/filepath"
	"testing"
	"time"

	"go.uber.org/zap"
)

func newTestCallerForCreateTool(t *testing.T, root string) *Caller {
	t.Helper()
	t.Setenv("INVOKR_TOOLBOX_DIR", root)
	return NewCaller(
		core.CallerDeps{
			App:        testAppConfigFromEnv(t),
			Dispatcher: events.NewOrderedDispatcher(zap.NewNop()),
			Approver:   testutil.StaticApprover{Decision: approval.Decision{Approved: true}},
		},
		zap.NewNop(),
	)
}

func createToolArgsJSON(t *testing.T, overrides map[string]any) json.RawMessage {
	t.Helper()
	args := map[string]any{
		"name":          "sample_tool",
		"description":   "Sample tool",
		"example":       `{"name":"sample_tool","arguments":{}}`,
		"parameters":    map[string]any{"type": "object"},
		"python_source": "import sys\nsys.stdout.write('{}')\n",
		"policy":        map[string]any{"tiers": []string{"executes_code"}, "approval": "ask"},
	}
	for k, v := range overrides {
		args[k] = v
	}
	b, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal create_tool args: %v", err)
	}
	return b
}

func TestCreateTool_RejectsInvalidSlugWithoutWritingAnything(t *testing.T) {
	root := t.TempDir()
	caller := newTestCallerForCreateTool(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	for _, name := range []string{"Bad-Name", "../escape", "a/b", "1starts_with_digit", ""} {
		args := createToolArgsJSON(t, map[string]any{"name": name})
		if _, err := caller.HandleCall(ctx, &core.ToolCall{Name: "create_tool", Arguments: args}); err == nil {
			t.Fatalf("expected name %q to be rejected", name)
		}
		if name == "" {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(root, name)); statErr == nil {
			t.Fatalf("expected no directory written for rejected name %q", name)
		}
	}
}

func TestCreateTool_RejectsNativeNameCollision(t *testing.T) {
	root := t.TempDir()
	caller := newTestCallerForCreateTool(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	args := createToolArgsJSON(t, map[string]any{"name": "create_tool"})
	if _, err := caller.HandleCall(ctx, &core.ToolCall{Name: "create_tool", Arguments: args}); err == nil {
		t.Fatal("expected collision with an existing native tool to be rejected")
	}
	if _, statErr := os.Stat(filepath.Join(root, "create_tool")); statErr == nil {
		t.Fatal("expected no directory written for a rejected collision")
	}
}

func TestCreateTool_RejectsUnpinnedRequirement(t *testing.T) {
	root := t.TempDir()
	caller := newTestCallerForCreateTool(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	args := createToolArgsJSON(t, map[string]any{
		"name":         "needs_requests",
		"requirements": []string{"requests"},
	})
	if _, err := caller.HandleCall(ctx, &core.ToolCall{Name: "create_tool", Arguments: args}); err == nil {
		t.Fatal("expected unpinned requirement to be rejected")
	}
	if _, statErr := os.Stat(filepath.Join(root, "needs_requests")); statErr == nil {
		t.Fatal("expected no directory written when requirements are rejected")
	}
}

func TestCreateTool_RefusesOverwriteWithoutFlag(t *testing.T) {
	root := t.TempDir()
	caller := newTestCallerForCreateTool(t, root)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	first := createToolArgsJSON(t, map[string]any{"name": "dup_tool"})
	if _, err := caller.HandleCall(ctx, &core.ToolCall{Name: "create_tool", Arguments: first}); err != nil {
		t.Fatalf("first create failed: %v", err)
	}

	second := createToolArgsJSON(t, map[string]any{"name": "dup_tool", "description": "changed"})
	if _, err := caller.HandleCall(ctx, &core.ToolCall{Name: "create_tool", Arguments: second}); err == nil {
		t.Fatal("expected overwrite without overwrite:true to be refused")
	}

	withOverwrite := createToolArgsJSON(t, map[string]any{"name": "dup_tool", "description": "changed", "overwrite": true})
	if _, err := caller.HandleCall(ctx, &core.ToolCall{Name: "create_tool", Arguments: withOverwrite}); err != nil {
		t.Fatalf("expected overwrite:true to succeed, got: %v", err)
	}
}
