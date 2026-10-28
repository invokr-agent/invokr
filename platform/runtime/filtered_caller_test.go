package runtime

import (
	"context"
	"errors"
	"testing"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/tools/core"
)

// stubCaller is a minimal ToolCaller for filtered_caller tests.
type stubCaller struct {
	specs []core.ToolSpec
}

func (s *stubCaller) Specs() []core.ToolSpec                 { return s.specs }
func (s *stubCaller) PromptSpecs(_ string) []core.ToolSpec   { return s.specs }
func (s *stubCaller) StableSpecs() []core.ToolSpec           { return s.specs }
func (s *stubCaller) HasNative(name string) bool             { return name == "a" }
func (s *stubCaller) NativeSource(_ string) string           { return "test" }
func (s *stubCaller) MarkSchemasLoaded(_ string, _ []string) {}

func (s *stubCaller) ForgetSession(_ string)      {}
func (s *stubCaller) Approver() approval.Approver { return nil }
func (s *stubCaller) ReplaceNativeToolsBySource(_ string, _ []core.DynamicNativeTool) []string {
	return nil
}
func (s *stubCaller) RegisterNative(_ core.ToolSpec, _ core.NativeHandler) {}
func (s *stubCaller) HandleCall(_ context.Context, call *core.ToolCall) (string, error) {
	return "result:" + call.Name, nil
}

func newStub(names ...string) *stubCaller {
	specs := make([]core.ToolSpec, len(names))
	for i, n := range names {
		specs[i] = core.ToolSpec{Name: n}
	}
	return &stubCaller{specs: specs}
}

func TestFilteredCallerSpecs(t *testing.T) {
	inner := newStub("a", "b", "c")
	fc := newFilteredCaller(inner, []string{"a"})
	specs := fc.Specs()
	if len(specs) != 1 || specs[0].Name != "a" {
		t.Errorf("expected [a], got %v", specs)
	}
	stable := fc.StableSpecs()
	if len(stable) != 1 || stable[0].Name != "a" {
		t.Errorf("stable specs escaped filter: %v", stable)
	}
}

func TestFilteredCallerHandleCallAllowed(t *testing.T) {
	inner := newStub("a", "b")
	fc := newFilteredCaller(inner, []string{"a"})
	out, err := fc.HandleCall(context.Background(), &core.ToolCall{Name: "a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "result:a" {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestFilteredCallerHandleCallDisallowed(t *testing.T) {
	inner := newStub("a", "b")
	fc := newFilteredCaller(inner, []string{"a"})
	_, err := fc.HandleCall(context.Background(), &core.ToolCall{Name: "b"})
	if err == nil {
		t.Fatal("expected error for disallowed tool")
	}
	var te *core.ToolError
	if !errors.As(err, &te) {
		t.Fatalf("expected ToolError, got %T: %v", err, err)
	}
	if te.Payload == "" {
		t.Error("expected non-empty Payload (completed-but-flagged semantics)")
	}
}

func TestFilteredCallerEmptyAllowlist(t *testing.T) {
	inner := newStub("a", "b")
	fc := newFilteredCaller(inner, []string{})
	specs := fc.Specs()
	if len(specs) != 0 {
		t.Errorf("expected empty specs, got %v", specs)
	}
	_, err := fc.HandleCall(context.Background(), &core.ToolCall{Name: "a"})
	if err == nil {
		t.Fatal("expected error for any tool with empty allowlist")
	}
}

func TestFilteredCallerRegisterNativeIsNoOp(t *testing.T) {
	inner := newStub("a")
	fc := newFilteredCaller(inner, []string{"a"})
	// RegisterNative must not panic and must not add to the inner stub.
	fc.RegisterNative(core.ToolSpec{Name: "new-tool"}, nil)
	if len(fc.Specs()) != 1 {
		t.Errorf("specs changed after RegisterNative: %v", fc.Specs())
	}
}

func TestFilteredCallerReplaceNativeToolsBySourceIsNoOp(t *testing.T) {
	inner := newStub("a")
	fc := newFilteredCaller(inner, []string{"a"})
	removed := fc.ReplaceNativeToolsBySource("src", []core.DynamicNativeTool{})
	if removed != nil {
		t.Errorf("expected nil from ReplaceNativeToolsBySource, got %v", removed)
	}
	if len(fc.Specs()) != 1 {
		t.Errorf("specs changed after ReplaceNativeToolsBySource: %v", fc.Specs())
	}
}
