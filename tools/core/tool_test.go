package core

import (
	"context"
	"errors"
	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/support/testutil"
	"strings"
	"testing"
)

type fakeRegistrar struct {
	spec    ToolSpec
	handler NativeHandler
}

func (r *fakeRegistrar) RegisterNative(spec ToolSpec, handler NativeHandler) {
	r.spec = spec
	r.handler = handler
}

func (r *fakeRegistrar) HasNative(string) bool {
	return false
}

func (r *fakeRegistrar) NativeSource(string) string {
	return ""
}

func (r *fakeRegistrar) ReplaceNativeToolsBySource(string, []DynamicNativeTool) []string {
	return nil
}

func TestRegisterRunsLifecycle(t *testing.T) {
	registrar := &fakeRegistrar{}
	Register(registrar, Tool[testArgs]{
		Spec: ToolSpec{Name: "demo"},
		Prepare: func(_ context.Context, args testArgs) (testArgs, error) {
			args.Value += "-prepared"
			return args, nil
		},
		Execute: func(_ context.Context, args testArgs) (string, error) {
			return args.Value + "-executed", nil
		},
	})

	out, err := registrar.handler(context.Background(), &ToolCall{
		Name:      "demo",
		Arguments: []byte(`{"value":"input"}`),
	})
	if err != nil {
		t.Fatalf("tool failed: %v", err)
	}
	if out != "input-prepared-executed" {
		t.Fatalf("output = %q", out)
	}
}

func TestRegisterRejectsInvalidTool(t *testing.T) {
	registrar := &fakeRegistrar{}
	assertPanic(t, func() {
		Register(registrar, Tool[testArgs]{
			Execute: func(context.Context, testArgs) (string, error) { return "", nil },
		})
	})
	assertPanic(t, func() {
		Register(registrar, Tool[testArgs]{Spec: ToolSpec{Name: "demo"}})
	})
}

func TestAuditFailureRunsBeforeExecute(t *testing.T) {
	var audited error
	tool := Tool[testArgs]{
		Spec: ToolSpec{Name: "demo"},
		Decode: func(*ToolCall) (testArgs, error) {
			return testArgs{Value: "bad"}, errors.New("decode rejected")
		},
		AuditFailure: func(_ context.Context, args testArgs, err error) {
			if args.Value != "bad" {
				t.Fatalf("audit args = %#v", args)
			}
			audited = err
		},
		Execute: func(context.Context, testArgs) (string, error) {
			t.Fatal("execute should not run")
			return "", nil
		},
	}
	_, err := tool.Handle(context.Background(), &ToolCall{Name: "demo"})
	if err == nil || audited == nil || audited.Error() != err.Error() {
		t.Fatalf("expected audit failure error %v, got audit %v", err, audited)
	}
}

func TestHandleApprovalMissingApproverFailsClosed(t *testing.T) {
	tool := Tool[testArgs]{
		Spec: ToolSpec{Name: "demo"},
		Approve: func(context.Context, testArgs) ApprovalDecision {
			return Ask(approval.Request{Tool: "demo", Tier: "write_workspace"})
		},
		Execute: func(context.Context, testArgs) (string, error) {
			return "ran", nil
		},
	}

	_, err := tool.Handle(context.Background(), &ToolCall{Name: "demo"})
	if err == nil || !strings.Contains(err.Error(), "requires approval") {
		t.Fatalf("expected missing approver rejection, got %v", err)
	}
}

func TestHandleApprovalDeniedSkipsExecution(t *testing.T) {
	executed := false
	tool := Tool[testArgs]{
		Spec:     ToolSpec{Name: "demo"},
		Approver: testutil.StaticApprover{Decision: approval.Decision{Approved: false, Reason: "nope"}},
		Approve: func(context.Context, testArgs) ApprovalDecision {
			return Ask(approval.Request{Tool: "demo", Tier: "write_workspace"})
		},
		Execute: func(context.Context, testArgs) (string, error) {
			executed = true
			return "ran", nil
		},
	}

	_, err := tool.Handle(context.Background(), &ToolCall{Name: "demo"})
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("expected approval denial, got %v", err)
	}
	if executed {
		t.Fatal("execute ran after denied approval")
	}
}

func TestHandleApprovalApprovedExecutes(t *testing.T) {
	tool := Tool[testArgs]{
		Spec:     ToolSpec{Name: "demo"},
		Approver: testutil.StaticApprover{Decision: approval.Decision{Approved: true}},
		Approve: func(context.Context, testArgs) ApprovalDecision {
			return Ask(approval.Request{Tool: "demo", Tier: "write_workspace"})
		},
		Execute: func(context.Context, testArgs) (string, error) {
			return "ran", nil
		},
	}

	out, err := tool.Handle(context.Background(), &ToolCall{Name: "demo"})
	if err != nil {
		t.Fatalf("expected approved execution, got %v", err)
	}
	if out != "ran" {
		t.Fatalf("output = %q", out)
	}
}

func TestHandleDenySkipsExecution(t *testing.T) {
	tool := Tool[testArgs]{
		Spec: ToolSpec{Name: "demo"},
		Approve: func(context.Context, testArgs) ApprovalDecision {
			return Deny("blocked")
		},
		Execute: func(context.Context, testArgs) (string, error) {
			return "ran", nil
		},
	}

	_, err := tool.Handle(context.Background(), &ToolCall{Name: "demo"})
	if err == nil || err.Error() != "approval denied for demo: blocked" {
		t.Fatalf("expected deny error, got %v", err)
	}
}

type testArgs struct {
	Value string `json:"value"`
}

func assertPanic(t *testing.T, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
	}()
	fn()
}
