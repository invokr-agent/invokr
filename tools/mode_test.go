package tools

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/tools/core"
)

type modeTestApprover struct{ requests []approval.Request }

func (a *modeTestApprover) RequestApproval(_ context.Context, req approval.Request) (approval.Decision, error) {
	a.requests = append(a.requests, req)
	return approval.Decision{Approved: true}, nil
}

func TestCallerModesGuardAutomaticAndUnmanagedTools(t *testing.T) {
	for _, mode := range []approval.Mode{approval.Manual, approval.Auto, approval.Plan, approval.YOLO} {
		t.Run(string(mode), func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "secret"), []byte("private"), 0600); err != nil {
				t.Fatal(err)
			}
			store := approval.NewStore("")
			_, err := store.Add(approval.Grant{Deny: true, Tool: "read", ArgPattern: "secret", Scope: approval.ScopeGlobal, ExpiresAt: time.Now().Add(time.Hour)})
			if err != nil {
				t.Fatal(err)
			}
			p := approval.NewPolicy(mode, false)
			p.SetStore(store, nil)
			a := &modeTestApprover{}
			c := NewCaller(core.CallerDeps{App: config.AppConfig{Workspace: config.WorkspaceConfig{Root: root}}, Policy: p, Approver: a})
			defer c.Close()
			if _, err := c.HandleCall(context.Background(), &core.ToolCall{Name: "read", Arguments: []byte(`{"paths":["./secret"]}`)}); err == nil {
				t.Fatal("automatic read bypassed denial")
			}
			ran := false
			c.RegisterNative(core.ToolSpec{Name: "external", Metadata: core.ToolMetadata{Source: "plugin", Capabilities: core.ToolCapabilities{ReadsWorkspace: true}}}, func(context.Context, *core.ToolCall) (string, error) { ran = true; return "ok", nil })
			_, err = c.HandleCall(context.Background(), &core.ToolCall{Name: "external"})
			if mode == approval.Plan {
				if err == nil || ran || slices.Contains(specNames(c.PromptSpecs("")), "external") {
					t.Fatal("Plan exposed external execution")
				}
			} else if err != nil || !ran {
				t.Fatalf("call failed: %v", err)
			}
			if (len(a.requests) > 0) != (mode == approval.Auto) {
				t.Fatalf("unexpected approval count %d", len(a.requests))
			}
		})
	}
}

func TestCallerAutoEditsAndPlanRejectsWrites(t *testing.T) {
	root := t.TempDir()
	p := approval.NewPolicy(approval.Auto, false)
	a := &modeTestApprover{}
	c := NewCaller(core.CallerDeps{App: config.AppConfig{Workspace: config.WorkspaceConfig{Root: root, AllowHiddenPaths: true}}, Policy: p, Approver: a})
	defer c.Close()
	call := &core.ToolCall{Name: "edit", Arguments: []byte(`{"path":"new.txt","edits":[{"old":"","new":"hello"}]}`)}
	if _, err := c.HandleCall(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	if len(a.requests) != 0 {
		t.Fatal("workspace edit prompted")
	}
	call.Arguments = []byte(`{"path":".invokr/settings.json","edits":[{"old":"","new":"{}"}]}`)
	if _, err := c.HandleCall(context.Background(), call); err != nil {
		t.Fatal(err)
	}
	if len(a.requests) != 1 || !a.requests[0].Protected {
		t.Fatalf("protected edit not scoped: %v", a.requests)
	}
	if err := p.SetMode(approval.Plan); err != nil {
		t.Fatal(err)
	}
	call.Arguments = []byte(`{"path":"other.txt","edits":[{"old":"","new":"blocked"}]}`)
	if _, err := c.HandleCall(context.Background(), call); err == nil {
		t.Fatal("Plan wrote a source file")
	}
	if _, err := os.Stat(filepath.Join(root, "other.txt")); !os.IsNotExist(err) {
		t.Fatal("Plan source changed")
	}
}

func TestLSPHostExecutionNeedsApprovalAndIsUnavailableInPlan(t *testing.T) {
	p := approval.NewPolicy(approval.Auto, false)
	c := NewCaller(core.CallerDeps{Policy: p, Approver: approval.StaticApprover{}})
	defer c.Close()
	core.Register(c, core.Tool[struct{}]{Spec: core.ToolSpec{Name: "test_lsp", Metadata: core.ReadWorkspaceMetadata("lsp", core.ToolCapabilities{ReadsWorkspace: true})}, Execute: func(context.Context, struct{}) (string, error) { t.Fatal("LSP ran without approval"); return "", nil }})
	for _, mode := range []approval.Mode{approval.Auto, approval.Plan} {
		if err := p.SetMode(mode); err != nil {
			t.Fatal(err)
		}
		if _, err := c.HandleCall(context.Background(), &core.ToolCall{Name: "test_lsp"}); err == nil {
			t.Fatal("LSP host execution bypassed policy")
		}
	}
}

// A deny grant names one file; the model asks for it behind an allowed file in
// the same batched read. Every path has to reach the matcher, not just the
// first (#577).
func TestDenyGrantMatchesAnyPathOfABatchedRead(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"open.txt", "secret"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	store := approval.NewStore("")
	if _, err := store.Add(approval.Grant{Deny: true, Tool: "read", ArgPattern: "secret", Scope: approval.ScopeGlobal, ExpiresAt: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	p := approval.NewPolicy(approval.Auto, false)
	p.SetStore(store, nil)
	c := NewCaller(core.CallerDeps{App: config.AppConfig{Workspace: config.WorkspaceConfig{Root: root}}, Policy: p, Approver: &modeTestApprover{}})
	defer c.Close()

	call := &core.ToolCall{Name: "read", Arguments: []byte(`{"paths":["./open.txt","./secret"]}`)}
	if _, err := c.HandleCall(context.Background(), call); err == nil {
		t.Fatal("denied path read as the second entry of a batched call")
	}
}
