package apply_patch_test

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/tools"
	"github.com/invokr-ai/invokr/tools/core"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/events"
)

func hasSpec(specs []core.ToolSpec, name string) bool {
	return slices.ContainsFunc(specs, func(spec core.ToolSpec) bool {
		return spec.Name == name
	})
}

func TestCallerRegistersApplyPatchTool(t *testing.T) {
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
		},
	})

	if !hasSpec(caller.Specs(), "apply_patch") {
		t.Fatal("expected apply_patch tool to be registered")
	}
}

func TestApplyPatchSpecShowsCreateSyntax(t *testing.T) {
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
		},
	})

	var applyPatchSpec core.ToolSpec
	for _, spec := range caller.Specs() {
		if spec.Name == "apply_patch" {
			applyPatchSpec = spec
			break
		}
	}
	if applyPatchSpec.Name == "" {
		t.Fatal("expected apply_patch spec")
	}
	if !strings.Contains(applyPatchSpec.Description, "Add File") {
		t.Fatalf("expected apply_patch description to mention Add File, got %q", applyPatchSpec.Description)
	}
	if !strings.Contains(applyPatchSpec.Example, "*** Add File:") {
		t.Fatalf("expected apply_patch example to show Add File, got %q", applyPatchSpec.Example)
	}
}

func TestApplyPatchCreatesFile(t *testing.T) {
	root := t.TempDir()
	caller := newApplyPatchTestCaller(t, root)

	output, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name: "apply_patch",
		Arguments: json.RawMessage(`{
			"patch":"*** Begin Patch\n*** Add File: hello.txt\n+hello\n+world\n*** End Patch"
		}`),
	})
	if err != nil {
		t.Fatalf("apply_patch failed: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, "hello.txt"))
	if err != nil {
		t.Fatalf("read created file: %v", err)
	}
	if string(got) != "hello\nworld" {
		t.Fatalf("expected created content, got %q", string(got))
	}
	assertApplyPatchOutput(t, output, "hello.txt", "create")
}
func mustWriteFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestApplyPatchUpdatesFile(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "note.txt"), "alpha\nbeta\ngamma\n")
	caller := newApplyPatchTestCaller(t, root)

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name: "apply_patch",
		Arguments: json.RawMessage(`{
			"patch":"*** Begin Patch\n*** Update File: note.txt\n@@\n alpha\n-beta\n+beta-2\n gamma\n*** End Patch"
		}`),
	})
	if err != nil {
		t.Fatalf("apply_patch failed: %v", err)
	}

	got, err := os.ReadFile(filepath.Join(root, "note.txt"))
	if err != nil {
		t.Fatalf("read updated file: %v", err)
	}
	if string(got) != "alpha\nbeta-2\ngamma\n" {
		t.Fatalf("expected updated content, got %q", string(got))
	}
}

func TestApplyPatchDeletesFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "dead.txt")
	mustWriteFile(t, path, "bye\n")
	caller := newApplyPatchTestCaller(t, root)

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "apply_patch",
		Arguments: json.RawMessage(`{"patch":"*** Begin Patch\n*** Delete File: dead.txt\n*** End Patch"}`),
	})
	if err != nil {
		t.Fatalf("apply_patch failed: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected file to be deleted, stat err=%v", err)
	}
}

func TestApplyPatchDryRunDoesNotMutate(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "dry.txt")
	mustWriteFile(t, path, "before\n")
	caller := newApplyPatchTestCaller(t, root)

	output, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name: "apply_patch",
		Arguments: json.RawMessage(`{
			"patch":"*** Begin Patch\n*** Update File: dry.txt\n@@\n-before\n+after\n*** End Patch",
			"dry_run":true
		}`),
	})
	if err != nil {
		t.Fatalf("apply_patch failed: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read dry-run file: %v", err)
	}
	if string(got) != "before\n" {
		t.Fatalf("expected dry-run to keep original content, got %q", string(got))
	}
	if !strings.Contains(output, `"dry_run":true`) {
		t.Fatalf("expected dry_run response, got %q", output)
	}
}

// A dry run writes nothing, so an ordinary-path dry run must not require an
// approver — the caller here has none and would fail closed if it asked.
func TestApplyPatchDryRunOrdinaryPathNeedsNoApprover(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "dry.txt"), "before\n")
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: root},
		},
	})

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name: "apply_patch",
		Arguments: json.RawMessage(`{
			"patch":"*** Begin Patch\n*** Update File: dry.txt\n@@\n-before\n+after\n*** End Patch",
			"dry_run":true
		}`),
	})
	if err != nil {
		t.Fatalf("expected ordinary dry run to auto-approve, got %v", err)
	}
}

// A dry run touching a hidden path still asks, exactly as before the fix.
func TestApplyPatchDryRunHiddenPathStillAsks(t *testing.T) {
	root := t.TempDir()
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: root},
		},
	})

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name: "apply_patch",
		Arguments: json.RawMessage(`{
			"patch":"*** Begin Patch\n*** Add File: .env\n+SECRET=1\n*** End Patch",
			"dry_run":true
		}`),
	})
	if err == nil || !strings.Contains(err.Error(), "requires approval") {
		t.Fatalf("expected hidden-path dry run to require approval, got %v", err)
	}
}

type approvalFunc func(context.Context, approval.Request) (approval.Decision, error)

func (f approvalFunc) RequestApproval(ctx context.Context, req approval.Request) (approval.Decision, error) {
	return f(ctx, req)
}

func TestApplyPatchRejectsFileChangedAfterPreparation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, ".env")
	mustWriteFile(t, path, "before\n")
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: root, AllowHiddenPaths: true},
		},
		Approver: approvalFunc(func(context.Context, approval.Request) (approval.Decision, error) {
			mustWriteFile(t, path, "external edit\n")
			return approval.Decision{Approved: true}, nil
		}),
	})

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "apply_patch",
		Arguments: json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: .env\n@@\n-before\n+patched\n*** End Patch"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "file changed since patch was prepared") {
		t.Fatalf("expected changed-file error, got %v", err)
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read externally edited file: %v", readErr)
	}
	if string(got) != "external edit\n" {
		t.Fatalf("external edit overwritten: got %q", got)
	}
}

func TestApplyPatchDoesNotClobberLegacyBackupPath(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	backup := filepath.Join(root, ".note.txt.invokr-backup")
	mustWriteFile(t, path, "before\n")
	mustWriteFile(t, backup, "canary\n")
	caller := newApplyPatchTestCaller(t, root)

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "apply_patch",
		Arguments: json.RawMessage(`{"patch":"*** Begin Patch\n*** Update File: note.txt\n@@\n-before\n+after\n*** End Patch"}`),
	})
	if err != nil {
		t.Fatalf("apply_patch failed: %v", err)
	}
	got, readErr := os.ReadFile(backup)
	if readErr != nil {
		t.Fatalf("read legacy backup canary: %v", readErr)
	}
	if string(got) != "canary\n" {
		t.Fatalf("legacy backup canary = %q", got)
	}
}

func TestApplyPatchRejectsStalePatch(t *testing.T) {
	root := t.TempDir()
	mustWriteFile(t, filepath.Join(root, "stale.txt"), "current\n")
	caller := newApplyPatchTestCaller(t, root)

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name: "apply_patch",
		Arguments: json.RawMessage(`{
			"patch":"*** Begin Patch\n*** Update File: stale.txt\n@@\n-old\n+new\n*** End Patch"
		}`),
	})
	for _, want := range []string{"update stale.txt", "hunk 1", `"old"`} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("expected stale patch error containing %q, got %v", want, err)
		}
	}
}

func TestApplyPatchUpdateMissingFileSuggestsAddFile(t *testing.T) {
	root := t.TempDir()
	caller := newApplyPatchTestCaller(t, root)

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name: "apply_patch",
		Arguments: json.RawMessage(`{
			"patch":"*** Begin Patch\n*** Update File: missing.txt\n@@\n+hello\n*** End Patch"
		}`),
	})
	if err == nil || !strings.Contains(err.Error(), "use *** Add File: missing.txt to create it") {
		t.Fatalf("expected missing-file Add File hint, got %v", err)
	}
}

func TestApplyPatchRollbackOnPartialFailure(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "keep.txt")
	mustWriteFile(t, path, "before\n")
	caller := newApplyPatchTestCaller(t, root)

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name: "apply_patch",
		Arguments: json.RawMessage(`{
			"patch":"*** Begin Patch\n*** Update File: keep.txt\n@@\n-before\n+after\n*** Update File: missing.txt\n@@\n-old\n+new\n*** End Patch"
		}`),
	})
	if err == nil {
		t.Fatal("expected multi-file patch to fail")
	}

	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatalf("read rollback file: %v", readErr)
	}
	if string(got) != "before\n" {
		t.Fatalf("expected rollback to restore original content, got %q", string(got))
	}
}

func TestApplyPatchRejectsPathTraversal(t *testing.T) {
	root := t.TempDir()
	caller := newApplyPatchTestCaller(t, root)

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "apply_patch",
		Arguments: json.RawMessage(`{"patch":"*** Begin Patch\n*** Add File: ../escape.txt\n+nope\n*** End Patch"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "outside allowed workspace roots") {
		t.Fatalf("expected path traversal rejection, got %v", err)
	}
}

func TestApplyPatchRejectsAbsolutePathOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "escape.txt")
	caller := newApplyPatchTestCaller(t, root)

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "apply_patch",
		Arguments: json.RawMessage(`{"patch":"*** Begin Patch\n*** Add File: ` + outside + `\n+nope\n*** End Patch"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "outside allowed workspace roots") {
		t.Fatalf("expected outside workspace rejection, got %v", err)
	}
}

func TestApplyPatchBlocksHiddenPathsByDefault(t *testing.T) {
	root := t.TempDir()
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: root},
		},
	})

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "apply_patch",
		Arguments: json.RawMessage(`{"patch":"*** Begin Patch\n*** Add File: .env\n+SECRET=1\n*** End Patch"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "requires approval") {
		t.Fatalf("expected hidden path block, got %v", err)
	}
}

func TestApplyPatchRequestsApprovalForHiddenPath(t *testing.T) {
	root := t.TempDir()
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: root},
		},
		Approver: approval.StaticApprover{Decision: approval.Decision{Approved: false, Reason: "denied by test"}},
	})
	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "apply_patch",
		Arguments: json.RawMessage(`{"patch":"*** Begin Patch\n*** Add File: .env\n+SECRET=1\n*** End Patch"}`),
	})
	var denied *approval.DeniedError
	if !errors.As(err, &denied) {
		t.Fatalf("expected approval denial, got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".env")); !os.IsNotExist(statErr) {
		t.Fatalf("expected no hidden file, stat error %v", statErr)
	}
}

func TestApplyPatchCannotAllowSensitivePathByConfig(t *testing.T) {
	root := t.TempDir()
	dispatcher := events.NewOrderedDispatcher(nil)
	var seen events.Event
	dispatcher.Register(events.HandlerFunc(func(_ context.Context, ev events.Event) error {
		if ev.Type == events.EventAudit {
			seen = ev
		}
		return nil
	}))
	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: root, AllowHiddenPaths: true},
		},
		Dispatcher: dispatcher,
	})

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "apply_patch",
		Arguments: json.RawMessage(`{"patch":"*** Begin Patch\n*** Add File: .env\n+SECRET=1\n*** End Patch"}`),
	})
	if err == nil || !strings.Contains(err.Error(), "sensitive path") {
		t.Fatalf("expected sensitive path block, got %v", err)
	}
	if seen.Audit == nil || seen.Audit.Outcome != "rejected" {
		t.Fatalf("expected rejected audit, got %#v", seen)
	}
}

func TestApplyPatchEmitsAuditEvent(t *testing.T) {
	root := t.TempDir()
	dispatcher := events.NewOrderedDispatcher(nil)
	var seen events.Event
	dispatcher.Register(events.HandlerFunc(func(_ context.Context, ev events.Event) error {
		if ev.Type == events.EventAudit {
			seen = ev
		}
		return nil
	}))

	caller := tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: root},
			// apply_patch is tool_search-deferred by default; the allowlist is
			// the production path that advertises it directly.
			Tools: config.ToolsConfig{Allowlist: []string{"apply_patch"}},
		},
		Dispatcher: dispatcher,
		Approver:   approval.StaticApprover{Decision: approval.Decision{Approved: true}},
	})

	_, err := caller.HandleCall(core.WithSessionID(context.Background(), "session-1"), &core.ToolCall{
		Name:      "apply_patch",
		Arguments: json.RawMessage(`{"patch":"*** Begin Patch\n*** Add File: hello.txt\n+hello\n*** End Patch"}`),
	})
	if err != nil {
		t.Fatalf("apply_patch failed: %v", err)
	}
	if seen.Type != events.EventAudit || seen.Audit == nil {
		t.Fatal("expected audit event")
	}
	if seen.SessionID != "session-1" {
		t.Fatalf("expected session id on audit event, got %q", seen.SessionID)
	}
	if seen.Audit.Outcome != "applied" {
		t.Fatalf("expected applied outcome, got %#v", seen.Audit)
	}
	if len(seen.Audit.Paths) != 1 || seen.Audit.Paths[0] != "hello.txt" {
		t.Fatalf("expected audited path hello.txt, got %#v", seen.Audit.Paths)
	}
}

func newApplyPatchTestCaller(t *testing.T, root string) *tools.Caller {
	t.Helper()
	return tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: root},
		},
		Approver: approval.StaticApprover{Decision: approval.Decision{Approved: true}},
	})
}

func assertApplyPatchOutput(t *testing.T, output string, path string, action string) {
	t.Helper()
	if !strings.Contains(output, `"`+"path"+`":"`+path+`"`) {
		t.Fatalf("expected output to include path %q, got %q", path, output)
	}
	if !strings.Contains(output, `"`+"action"+`":"`+action+`"`) {
		t.Fatalf("expected output to include action %q, got %q", action, output)
	}
}
