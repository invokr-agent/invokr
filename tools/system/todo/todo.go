package todo

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/invokr-agent/invokr/tools/core"
)

const toolName = "todo"

// delegateToolName is a literal rather than delegate.ToolName: todo and
// delegate both depend on tools/core only, and importing delegate here would
// be the first edge between two sibling tool packages.
const delegateToolName = "delegate_task"

// delegateHint is the C10 trigger. delegate_task is advertised on every run and
// called on none: the schema says what it is, so the nudge has to arrive at the
// moment the model has just written a list of independent steps.
const delegateHint = "If enabled tools cannot cover these independent tasks, enable delegate_task through tools, then use tools.run with one self-contained prompt per task."

// minPendingForDelegateHint is the list size worth splitting; below it the
// delegation overhead is not worth a child session.
const minPendingForDelegateHint = 3

const (
	maxItems   = 20
	maxTaskLen = 200
)

type todoItem struct {
	Task   string `json:"task" desc:"Short imperative description of the step."`
	Status string `json:"status" enum:"pending,in_progress,done" desc:"Current state of this step."`
}

type todoArgs struct {
	Items []todoItem `json:"items,omitempty" desc:"Complete replacement list. Omit to read the current list without changing it."`
}

// lists is the per-session store. Package-level rather than closed over by
// RegisterTodoTool so Render can restore current task state after compaction.
var (
	mu    sync.Mutex
	lists = map[string][]todoItem{}
)

// Render returns the session's list as one line per item for the system
// post-compaction context, or "" when the list is empty.
func Render(sessionID string) string {
	mu.Lock()
	items := lists[sessionID]
	mu.Unlock()
	var out strings.Builder
	for _, item := range items {
		mark := map[string]string{"pending": "[ ]", "in_progress": "[>]", "done": "[x]"}[item.Status]
		out.WriteString("- " + mark + " " + item.Task + "\n")
	}
	return out.String()
}

type todoResponse struct {
	Todos []todoItem `json:"todos"`
	// Hint carries the delegate_task nudge. It rides a field rather than a
	// line appended after the JSON so the result stays parseable as a whole.
	Hint string `json:"hint,omitempty"`
}

// RegisterTodoTool gives the model a per-session task list it writes in full
// and reads back. State is in-memory and keyed by session id: the *tools.Caller
// is built once per process, so a single slice would leak between chat sessions
// and subagent runs.
func RegisterTodoTool(registrar core.NativeRegistrar) {
	core.Register(registrar, core.Tool[todoArgs]{
		Spec: core.ToolSpec{
			Name: toolName, Description: "Track task steps. items replaces the entire list; omit it to read. Mark exactly one item in_progress.",
			Example:    `{"name":"todo","arguments":{"items":[{"task":"Read the config","status":"in_progress"},{"task":"Summarize gaps","status":"pending"}]}}`,
			Parameters: core.SchemaFor[todoArgs](),
			// Zero capabilities: no workspace read/write, no network, no exec —
			// session policy can approve this in-memory bookkeeping automatically.
			Metadata: core.ReadWorkspaceMetadata("system", core.ToolCapabilities{}),
		},
		Execute: func(ctx context.Context, args todoArgs) (string, error) {
			session := core.SessionIDFromContext(ctx)
			mu.Lock()
			defer mu.Unlock()
			if args.Items != nil {
				lists[session] = normalize(args.Items)
			}
			items := lists[session]
			if items == nil {
				items = []todoItem{}
			}
			return core.MarshalResult(toolName, todoResponse{Todos: items, Hint: delegateHintFor(ctx, registrar, items)})
		},
	})
}

// delegateHintFor returns the delegation nudge when the list holds enough
// pending work to be worth splitting and this agent can actually delegate:
// delegate_task must be registered and advertised; a subagent may not delegate further
// (delegate refuses at depth >= 1), so a child never sees the hint.
func delegateHintFor(ctx context.Context, registrar core.NativeRegistrar, items []todoItem) string {
	if !registrar.HasNative(delegateToolName) || core.DelegateDepthFromContext(ctx) > 0 {
		return ""
	}
	if caller, ok := registrar.(interface{ PromptSpecs(string) []core.ToolSpec }); ok &&
		!slices.ContainsFunc(caller.PromptSpecs(core.SessionIDFromContext(ctx)), func(spec core.ToolSpec) bool {
			return spec.Name == delegateToolName
		}) {
		return ""
	}
	pending := 0
	for _, item := range items {
		if item.Status == "pending" {
			pending++
		}
	}
	if pending < minPendingForDelegateHint {
		return ""
	}
	return delegateHint
}

// normalize bounds model input so a runaway list cannot grow the prompt
// without limit: blank tasks are dropped, tasks are trimmed and capped, and
// unknown statuses fall back to pending.
func normalize(items []todoItem) []todoItem {
	out := make([]todoItem, 0, len(items))
	for _, item := range items {
		task := core.TruncateRunes(strings.TrimSpace(item.Task), maxTaskLen)
		if task == "" {
			continue
		}
		switch item.Status {
		case "pending", "in_progress", "done":
		default:
			item.Status = "pending"
		}
		out = append(out, todoItem{Task: task, Status: item.Status})
		if len(out) >= maxItems {
			break
		}
	}
	return out
}
