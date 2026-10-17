package memory

import (
	"context"
	"encoding/json"
	memorymgr "github.com/invokr-ai/invokr/capability/memory"
	memorycore "github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/tools/core"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/config"
)

type fakeMemoryManager struct {
	saved    memorycore.Record
	updated  memorycore.Record
	deleted  string
	listed   []memorycore.Record
	recalled memorycore.Query

	archived     []memorycore.Record
	archiveQuery string
	restored     []string
}

func (m *fakeMemoryManager) Close() error {
	return nil
}

func (m *fakeMemoryManager) PromptBlocks(context.Context, memorycore.PromptRequest) (memorycore.PromptBlocks, error) {
	return nil, nil
}

func (m *fakeMemoryManager) Save(_ context.Context, rec memorycore.Record) (memorycore.Record, error) {
	rec.ID = "project:alpha"
	m.saved = rec
	return rec, nil
}

func (m *fakeMemoryManager) Update(_ context.Context, id string, rec memorycore.Record) (memorycore.Record, error) {
	rec.ID = id
	m.updated = rec
	return rec, nil
}

func (m *fakeMemoryManager) UpdateMetadata(context.Context, string, map[string]string) error {
	return nil
}

func (m *fakeMemoryManager) Delete(_ context.Context, id string) error {
	m.deleted = id
	return nil
}

func (m *fakeMemoryManager) List(context.Context, memorycore.Query) ([]memorycore.Record, error) {
	return m.listed, nil
}

func (m *fakeMemoryManager) Recall(_ context.Context, q memorycore.Query) ([]memorycore.Hit, error) {
	m.recalled = q
	return nil, nil
}

func (m *fakeMemoryManager) SearchArchive(_ context.Context, query string, _ int) ([]memorycore.Record, error) {
	m.archiveQuery = query
	return m.archived, nil
}

func (m *fakeMemoryManager) RestoreArchived(_ context.Context, ids []string) ([]string, error) {
	m.restored = ids
	return ids, nil
}

func TestHandleRecallToolDefaultsSessionIDFromContext(t *testing.T) {
	manager := &fakeMemoryManager{}
	ctx := core.WithSessionID(context.Background(), "sess-123")

	// No session_id in args → falls back to the context session.
	if _, err := handleRecallTool(ctx, manager, memorycore.Query{Query: "what did we discuss"}); err != nil {
		t.Fatalf("handleRecallTool: %v", err)
	}
	if manager.recalled.SessionID != "sess-123" {
		t.Fatalf("session id = %q, want context session", manager.recalled.SessionID)
	}

	// Explicit session_id wins over the context default.
	if _, err := handleRecallTool(ctx, manager, memorycore.Query{Query: "q", SessionID: "explicit"}); err != nil {
		t.Fatalf("handleRecallTool: %v", err)
	}
	if manager.recalled.SessionID != "explicit" {
		t.Fatalf("session id = %q, want explicit override", manager.recalled.SessionID)
	}

	if _, err := handleRecallTool(ctx, manager, memorycore.Query{
		Query:  "project fact",
		Scopes: []config.MemoryScope{config.ScopeProject},
	}); err != nil {
		t.Fatalf("handle project recall: %v", err)
	}
	if manager.recalled.SessionID != "" {
		t.Fatalf("project recall session id = %q, want empty", manager.recalled.SessionID)
	}
}

func TestHandleMemoryToolUsesManagerRecordModel(t *testing.T) {
	ctx := context.Background()
	manager := &fakeMemoryManager{
		listed: []memorycore.Record{{
			ID:      "project:alpha",
			Scope:   config.ScopeProject,
			Kind:    memorycore.KindFact,
			Content: "alpha",
		}},
	}

	out, err := handleMemoryToolCall(ctx, manager, &core.ToolCall{
		Name:      "memory",
		Arguments: json.RawMessage(`{"action":"save","scope":"project","kind":"fact","content":"alpha"}`),
	})
	if err != nil {
		t.Fatalf("save memory tool: %v", err)
	}
	if manager.saved.Content != "alpha" || manager.saved.Scope != config.ScopeProject || manager.saved.Kind != memorycore.KindFact {
		t.Fatalf("save used wrong record: %#v", manager.saved)
	}
	if !strings.Contains(out, `"id":"project:alpha"`) {
		t.Fatalf("save output missing saved record: %s", out)
	}

	_, err = handleMemoryToolCall(ctx, manager, &core.ToolCall{
		Name:      "memory",
		Arguments: json.RawMessage(`{"action":"update","id":"project:alpha","scope":"project","kind":"fact","content":"beta"}`),
	})
	if err != nil {
		t.Fatalf("update memory tool: %v", err)
	}
	if manager.updated.ID != "project:alpha" || manager.updated.Content != "beta" {
		t.Fatalf("update used wrong record: %#v", manager.updated)
	}

	_, err = handleMemoryToolCall(ctx, manager, &core.ToolCall{
		Name:      "memory",
		Arguments: json.RawMessage(`{"action":"delete","id":"project:alpha"}`),
	})
	if err != nil {
		t.Fatalf("delete memory tool: %v", err)
	}
	if manager.deleted != "project:alpha" {
		t.Fatalf("delete used id %q", manager.deleted)
	}

	out, err = handleMemoryToolCall(ctx, manager, &core.ToolCall{
		Name:      "memory",
		Arguments: json.RawMessage(`{"action":"list","scopes":["project"],"kinds":["fact"]}`),
	})
	if err != nil {
		t.Fatalf("list memory tool: %v", err)
	}
	if !strings.Contains(out, `"records":[`) || !strings.Contains(out, `"content":"alpha"`) {
		t.Fatalf("list output missing records: %s", out)
	}
}

func TestHandleMemoryToolForwardsRecordSource(t *testing.T) {
	ctx := context.Background()
	manager := &fakeMemoryManager{}

	_, err := handleMemoryToolCall(ctx, manager, &core.ToolCall{
		Name:      "memory",
		Arguments: json.RawMessage(`{"action":"save","scope":"project","kind":"fact","source":"markdown","content":"pinned"}`),
	})
	if err != nil {
		t.Fatalf("save markdown memory tool: %v", err)
	}
	if manager.saved.Source != memorycore.SourceMarkdown {
		t.Fatalf("source = %q, want %q", manager.saved.Source, memorycore.SourceMarkdown)
	}
}

func TestDecodeMemoryToolArgsRejectsMissingActionAndUnknownFields(t *testing.T) {
	for _, raw := range []string{"{}", "{\"action\":null}"} {
		_, err := decodeMemoryToolArgs(&core.ToolCall{Name: "memory", Arguments: json.RawMessage(raw)})
		if err == nil {
			t.Fatalf("decodeMemoryToolArgs(%s) expected error", raw)
		}
	}
}

func TestMemoryToolParametersRequireAction(t *testing.T) {
	params := core.SchemaFor[memoryToolArgs]()
	required, ok := params["required"].([]string)
	if !ok {
		t.Fatalf("memory parameters missing required actions: %#v", params)
	}
	if len(required) != 1 || required[0] != "action" {
		t.Fatalf("memory required fields = %#v, want action", required)
	}
}

func handleMemoryToolCall(ctx context.Context, manager memorymgr.Manager, call *core.ToolCall) (string, error) {
	args, err := decodeMemoryToolArgs(call)
	if err != nil {
		return "", err
	}
	return handleMemoryTool(ctx, manager, args)
}
