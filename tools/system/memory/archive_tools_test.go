package memory

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/approval"
	memorycore "github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/support/testutil"
	"github.com/invokr-ai/invokr/tools/core"
)

type captureRegistrar struct {
	specs    map[string]core.ToolSpec
	handlers map[string]core.NativeHandler
}

func newCaptureRegistrar() *captureRegistrar {
	return &captureRegistrar{specs: map[string]core.ToolSpec{}, handlers: map[string]core.NativeHandler{}}
}

func (r *captureRegistrar) RegisterNative(spec core.ToolSpec, handler core.NativeHandler) {
	r.specs[spec.Name] = spec
	r.handlers[spec.Name] = handler
}
func (r *captureRegistrar) HasNative(string) bool      { return false }
func (r *captureRegistrar) NativeSource(string) string { return "" }
func (r *captureRegistrar) ReplaceNativeToolsBySource(string, []core.DynamicNativeTool) []string {
	return nil
}

func TestHandleArchiveToolSearchAndRestore(t *testing.T) {
	ctx := context.Background()
	manager := &fakeMemoryManager{archived: []memorycore.Record{{ID: "a1", Content: "old fact"}}}

	out, err := handleArchiveTool(ctx, manager, archiveToolArgs{Query: "old"})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if manager.archiveQuery != "old" {
		t.Fatalf("archive query = %q, want old", manager.archiveQuery)
	}
	if !strings.Contains(out, `"content":"old fact"`) {
		t.Fatalf("search output missing archived record: %s", out)
	}

	if _, err := handleArchiveTool(ctx, manager, archiveToolArgs{Restore: []string{"a1"}}); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(manager.restored) != 1 || manager.restored[0] != "a1" {
		t.Fatalf("restore ids = %#v, want [a1]", manager.restored)
	}
}

func TestHandleArchiveToolRequiresQueryOrRestore(t *testing.T) {
	if _, err := handleArchiveTool(context.Background(), &fakeMemoryManager{}, archiveToolArgs{}); err == nil {
		t.Fatal("expected error when neither query nor restore is set")
	}
}

// recall_archive must be approval-gated: with an approver that denies, the
// handler fails closed and never reaches SearchArchive. This proves the tool is
// never auto-approved (unlike memory/recall).
func TestRecallArchiveIsApprovalGated(t *testing.T) {
	reg := newCaptureRegistrar()
	manager := &fakeMemoryManager{archived: []memorycore.Record{{ID: "a1", Content: "old"}}}

	registerArchiveTool(reg, manager, testutil.StaticApprover{Decision: approval.Decision{Approved: false}})

	spec, ok := reg.specs["recall_archive"]
	if !ok {
		t.Fatal("recall_archive not registered")
	}
	if spec.Metadata.Policy.Approval != constants.ApprovalPolicyAsk {
		t.Fatalf("recall_archive approval policy = %q, want ask", spec.Metadata.Policy.Approval)
	}

	call := &core.ToolCall{Name: "recall_archive", Arguments: json.RawMessage(`{"query":"old"}`)}
	if _, err := reg.handlers["recall_archive"](context.Background(), call); err == nil {
		t.Fatal("expected denial to block recall_archive")
	}
	if manager.archiveQuery != "" {
		t.Fatalf("denied call still searched the archive: %q", manager.archiveQuery)
	}

	// With approval granted the call proceeds.
	reg2 := newCaptureRegistrar()
	registerArchiveTool(reg2, manager, testutil.StaticApprover{Decision: approval.Decision{Approved: true}})
	if _, err := reg2.handlers["recall_archive"](context.Background(), call); err != nil {
		t.Fatalf("approved recall_archive failed: %v", err)
	}
	if manager.archiveQuery != "old" {
		t.Fatalf("approved call did not search: %q", manager.archiveQuery)
	}
}
