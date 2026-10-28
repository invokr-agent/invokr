package todo_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/tools"
	"github.com/invokr-agent/invokr/tools/core"
	"github.com/invokr-agent/invokr/tools/system/todo"
)

type todoItem struct {
	Task   string `json:"task"`
	Status string `json:"status"`
}

type todoResponse struct {
	Todos []todoItem `json:"todos"`
	Hint  string     `json:"hint"`
}

func newTestCaller(t *testing.T) *tools.Caller {
	t.Helper()
	return tools.NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
			Memory:  config.MemoryConfig{Dir: t.TempDir()},
		},
	})
}

func callTodo(t *testing.T, caller *tools.Caller, ctx context.Context, args string) todoResponse {
	t.Helper()
	if err := caller.StartToolSession(core.SessionIDFromContext(ctx)); err != nil {
		t.Fatal(err)
	}
	out, err := caller.HandleCall(ctx, &core.ToolCall{
		Name:      "todo",
		Arguments: json.RawMessage(args),
	})
	if err != nil {
		t.Fatalf("todo call error = %v", err)
	}
	var resp todoResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	return resp
}

func TestCallerRegistersTodoTool(t *testing.T) {
	caller := newTestCaller(t)
	var spec core.ToolSpec
	for _, s := range caller.Specs() {
		if s.Name == "todo" {
			spec = s
			break
		}
	}
	if spec.Name == "" {
		t.Fatal("expected todo tool to be registered")
	}
	if spec.Metadata.Capabilities != (core.ToolCapabilities{}) {
		t.Fatalf("capabilities = %#v, want zero value (keeps todo out of every approval tier)", spec.Metadata.Capabilities)
	}

	props, ok := spec.Parameters["properties"].(map[string]any)
	if !ok {
		t.Fatalf("parameters = %#v, want object schema", spec.Parameters)
	}
	items, ok := props["items"].(map[string]any)
	if !ok || items["type"] != "array" {
		t.Fatalf("items schema = %#v, want array", props["items"])
	}
	if _, required := spec.Parameters["required"]; required {
		t.Fatalf("required = %#v, want items optional", spec.Parameters["required"])
	}
	itemSchema, ok := items["items"].(map[string]any)
	if !ok {
		t.Fatalf("item element schema = %#v, want object", items["items"])
	}
	itemProps := itemSchema["properties"].(map[string]any)
	status := itemProps["status"].(map[string]any)
	enum, ok := status["enum"].([]string)
	if !ok || len(enum) != 3 {
		t.Fatalf("status enum = %#v, want pending,in_progress,done", status["enum"])
	}
}

func TestTodoWriteThenReadReturnsList(t *testing.T) {
	caller := newTestCaller(t)
	ctx := core.WithSessionID(context.Background(), "s1")

	callTodo(t, caller, ctx, `{"items":[{"task":"read config","status":"in_progress"},{"task":"summarize","status":"pending"}]}`)
	resp := callTodo(t, caller, ctx, `{}`)
	if len(resp.Todos) != 2 || resp.Todos[0].Task != "read config" || resp.Todos[0].Status != "in_progress" {
		t.Fatalf("todos = %#v", resp.Todos)
	}
}

func TestTodoWriteReplacesPreviousList(t *testing.T) {
	caller := newTestCaller(t)
	ctx := core.WithSessionID(context.Background(), "s1")

	callTodo(t, caller, ctx, `{"items":[{"task":"first","status":"pending"},{"task":"second","status":"pending"}]}`)
	resp := callTodo(t, caller, ctx, `{"items":[{"task":"only","status":"done"}]}`)
	if len(resp.Todos) != 1 || resp.Todos[0].Task != "only" || resp.Todos[0].Status != "done" {
		t.Fatalf("todos = %#v, want replacement not append", resp.Todos)
	}
}

func TestTodoNormalizesStatusAndDropsBlankTasks(t *testing.T) {
	caller := newTestCaller(t)
	ctx := core.WithSessionID(context.Background(), "s1")

	long := strings.Repeat("x", 300)
	resp := callTodo(t, caller, ctx,
		`{"items":[{"task":"  ","status":"pending"},{"task":"ok","status":"bogus"},{"task":"`+long+`","status":"done"}]}`)
	if len(resp.Todos) != 2 {
		t.Fatalf("todos = %#v, want blank task dropped", resp.Todos)
	}
	if resp.Todos[0].Status != "pending" {
		t.Fatalf("status = %q, want unknown status normalized to pending", resp.Todos[0].Status)
	}
	if len([]rune(resp.Todos[1].Task)) != 200 {
		t.Fatalf("task length = %d, want capped at 200", len([]rune(resp.Todos[1].Task)))
	}
}

func TestTodoCapsItemCount(t *testing.T) {
	caller := newTestCaller(t)
	ctx := core.WithSessionID(context.Background(), "s1")

	items := make([]string, 0, 25)
	for range 25 {
		items = append(items, `{"task":"step","status":"pending"}`)
	}
	resp := callTodo(t, caller, ctx, `{"items":[`+strings.Join(items, ",")+`]}`)
	if len(resp.Todos) != 20 {
		t.Fatalf("todos = %d items, want capped at 20", len(resp.Todos))
	}
}

func TestTodoIsolatedPerSession(t *testing.T) {
	caller := newTestCaller(t)
	ctxA := core.WithSessionID(context.Background(), "a")
	ctxB := core.WithSessionID(context.Background(), "b")

	callTodo(t, caller, ctxA, `{"items":[{"task":"a-only","status":"pending"}]}`)
	resp := callTodo(t, caller, ctxB, `{}`)
	if len(resp.Todos) != 0 {
		t.Fatalf("todos = %#v, want session b to start empty", resp.Todos)
	}
}

// stubRegistrar registers todo without a delegate_task tool, which is the
// shape a build with delegation disabled has.
type stubRegistrar struct{ handler core.NativeHandler }

func (s *stubRegistrar) RegisterNative(_ core.ToolSpec, h core.NativeHandler) { s.handler = h }
func (s *stubRegistrar) HasNative(string) bool                                { return false }
func (s *stubRegistrar) NativeSource(string) string                           { return "" }
func (s *stubRegistrar) ReplaceNativeToolsBySource(string, []core.DynamicNativeTool) []string {
	return nil
}

const threePending = `{"items":[{"task":"api","status":"pending"},{"task":"web","status":"pending"},{"task":"docs","status":"pending"}]}`

// delegate_task is advertised on every run and called on almost none. The hint
// fires at the one moment the model has just written a list of independent
// steps.
func TestTodoHintsAtDelegationForThreePendingItems(t *testing.T) {
	caller := newTestCaller(t)
	resp := callTodo(t, caller, core.WithSessionID(context.Background(), "s1"), threePending)
	if !strings.Contains(resp.Hint, "enable delegate_task through tools") {
		t.Fatalf("hint = %q, want the delegation nudge", resp.Hint)
	}
}

func TestTodoDelegationHintStandsDown(t *testing.T) {
	caller := newTestCaller(t)
	tests := []struct {
		name string
		ctx  context.Context
		args string
	}{
		{"two pending items are not worth splitting", core.WithSessionID(context.Background(), "s1"),
			`{"items":[{"task":"api","status":"pending"},{"task":"web","status":"pending"}]}`},
		{"in_progress items are not pending work", core.WithSessionID(context.Background(), "s2"),
			`{"items":[{"task":"api","status":"in_progress"},{"task":"web","status":"pending"},{"task":"docs","status":"done"}]}`},
		{"a subagent cannot delegate further", core.WithDelegateDepth(core.WithSessionID(context.Background(), "s3"), 1), threePending},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if resp := callTodo(t, caller, tc.ctx, tc.args); resp.Hint != "" {
				t.Fatalf("hint = %q, want none", resp.Hint)
			}
		})
	}
}

func TestTodoDelegationHintAbsentWhenDelegateNotRegistered(t *testing.T) {
	registrar := &stubRegistrar{}
	todo.RegisterTodoTool(registrar)
	out, err := registrar.handler(core.WithSessionID(context.Background(), "s1"),
		&core.ToolCall{Name: "todo", Arguments: json.RawMessage(threePending)})
	if err != nil {
		t.Fatalf("todo call error = %v", err)
	}
	var resp todoResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if resp.Hint != "" {
		t.Fatalf("hint = %q, want none when delegate_task is not registered", resp.Hint)
	}
}

// Render is what the system prompt shows every turn: one line per item with
// a status mark, or "" for an empty list.
func TestRenderShowsListPerSession(t *testing.T) {
	caller := newTestCaller(t)
	ctx := core.WithSessionID(context.Background(), "render-s1")
	if got := todo.Render("render-s1"); got != "" {
		t.Fatalf("Render(empty) = %q, want empty", got)
	}
	callTodo(t, caller, ctx, `{"items":[{"task":"Read spec","status":"done"},{"task":"Add export","status":"in_progress"},{"task":"Docs","status":"pending"}]}`)
	want := "- [x] Read spec\n- [>] Add export\n- [ ] Docs\n"
	if got := todo.Render("render-s1"); got != want {
		t.Fatalf("Render() = %q, want %q", got, want)
	}
	if got := todo.Render("other"); got != "" {
		t.Fatalf("Render(other session) = %q, want empty", got)
	}
}

func TestTodoDelegationHintRespectsAllowlist(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		allowlist := []string{"todo"}
		if allowed {
			allowlist = append(allowlist, "delegate_task")
		}
		caller := tools.NewCaller(core.CallerDeps{App: config.AppConfig{
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()},
			Memory:  config.MemoryConfig{Dir: t.TempDir()},
			Tools:   config.ToolsConfig{Allowlist: allowlist},
		}})
		t.Cleanup(caller.Close)
		if !caller.HasNative("delegate_task") {
			t.Fatal("delegate_task must remain registered for this regression")
		}
		ctx := core.WithSessionID(context.Background(), t.Name())
		for _, args := range []string{threePending, `{}`} {
			resp := callTodo(t, caller, ctx, args)
			if (resp.Hint != "") != allowed {
				t.Fatalf("allowed=%v, args=%s: hint=%q", allowed, args, resp.Hint)
			}
		}
	}
}
