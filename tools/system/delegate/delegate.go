package delegate

import (
	"cmp"
	"context"
	"fmt"
	"github.com/invokr-agent/invokr/capability/memory"
	memcore "github.com/invokr-agent/invokr/capability/memory/core"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/support/mathutil"
	"github.com/invokr-agent/invokr/tools/core"
	"strings"
	"sync"
	"time"

	"github.com/invokr-agent/invokr/capability/approval"
)

const ToolName = "delegate_task"

const (
	defaultMaxParallel = 3
	defaultMaxTurns    = 12
	hardMaxTasks       = 6
	// maxDelegateDepth bounds how many levels of delegate_task nesting are
	// allowed. A depth of 1 means only the top-level agent may delegate;
	// subagents cannot themselves call delegate_task.
	maxDelegateDepth = 1
)

type Args struct {
	Tasks       []Task `json:"tasks,omitempty" desc:"The sub-tasks to delegate. For a single sub-task use prompt/label instead."`
	MaxParallel int    `json:"max_parallel,omitempty" desc:"How many tasks may run at once. Defaults to 3."`
	MaxTurns    int    `json:"max_turns,omitempty" desc:"Turn budget per child agent. Defaults to 12."`
	// Prompt and Label are the single-task shorthand: a nested array of
	// objects is a hard shape for a small model to emit, and one sub-task is
	// the common case, so {"prompt": "..."} means the same as a one-element
	// tasks array.
	Prompt string `json:"prompt,omitempty" desc:"A single sub-task to delegate, instead of tasks."`
	Label  string `json:"label,omitempty" desc:"Short name for the single sub-task given in prompt."`
}

type Task struct {
	Label          string `json:"label" desc:"Short name for this sub-task, unique within the call."`
	Prompt         string `json:"prompt" desc:"What the child agent must do, stated in full: it cannot see this conversation."`
	Context        string `json:"context,omitempty" desc:"Key context from the parent agent's conversation that the subagent needs, since it has no access to the parent's memory."`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" desc:"Optional wall-clock timeout for this task."`
	Agent          string `json:"agent,omitempty" desc:"Optional agent definition name (from ~/.invokr/agents/<name>.md). Unknown name → tool error listing available agents."`
}

type Response struct {
	Results    []core.SubagentResult `json:"results"`
	TotalUsage UsageTotals           `json:"total_usage"`
}

type UsageTotals struct {
	PromptTokens     int64 `json:"prompt_tokens,omitempty"`
	CompletionTokens int64 `json:"completion_tokens,omitempty"`
	TotalTokens      int64 `json:"total_tokens,omitempty"`
}

func RegisterDelegateTool(registrar core.NativeRegistrar, deps core.CallerDeps) {
	core.Register(registrar, core.Tool[Args]{
		Spec: core.ToolSpec{
			Name: ToolName, Description: "Hand a self-contained sub-task to a child agent that works in its own session and returns only its final answer. Use it when a sub-task takes several steps whose intermediate output you do not need — searching a large tree, running and interpreting a check, drafting a file from a spec — so that work stays out of this conversation. Prefer it over a direct call for a search, log read, or command whose output is likely to run past ~100 lines: ask the child for a summary so the raw output never enters this conversation. The child cannot see this conversation, so state everything it needs in `prompt`. For one sub-task pass `prompt`; for several pass `tasks`.",
			Example:    `{"name":"delegate_task","arguments":{"label":"tests","prompt":"List every exported function in ./parser that has no test, and report just the names."}}`,
			Parameters: core.SchemaFor[Args](),
			Metadata:   core.NativeMetadata("agent", []string{"delegate_task"}, constants.ApprovalPolicyAsk, core.ToolCapabilities{ReadsWorkspace: true, WritesWorkspace: true, Network: true, ExecutesCode: true}),
		},
		Prepare: func(ctx context.Context, args Args) (Args, error) { return prepareArgs(ctx, args, deps) },
		Approve: func(_ context.Context, args Args) core.ApprovalDecision {
			return core.Ask(approval.Request{
				ReadOnly: true, // The orchestration delegates effects to the same guarded caller.
				Tool:     ToolName, Tier: "delegate_task", Reason: "delegate_task runs child agents that may use tools",
				Summary: fmt.Sprintf("Run %d delegated task(s)", len(args.Tasks)),
			})
		},
		Approver: deps.Approver,
		Execute: func(ctx context.Context, args Args) (string, error) {
			return runDelegatesWithUnit(ctx, deps.Subagents, deps.Memory, core.SessionIDFromContext(ctx), args, time.Second)
		},
	})
}

// foldSingleTask turns the flat prompt/label shorthand into the one-element
// tasks array the rest of the tool works with.
func foldSingleTask(args Args) Args {
	if len(args.Tasks) > 0 || strings.TrimSpace(args.Prompt) == "" {
		return args
	}
	// The shorthand is advertised as prompt-only, so supply a label rather than
	// failing validation on a field the model was never asked for.
	args.Tasks = []Task{{Label: cmp.Or(strings.TrimSpace(args.Label), "task"), Prompt: args.Prompt}}
	args.Prompt, args.Label = "", ""
	return args
}

func prepareArgs(_ context.Context, args Args, deps core.CallerDeps) (Args, error) {
	args = foldSingleTask(args)
	if len(args.Tasks) == 0 {
		return args, fmt.Errorf("give a sub-task in prompt, or several in tasks")
	}
	if len(args.Tasks) > hardMaxTasks {
		return args, fmt.Errorf("too many tasks: max %d", hardMaxTasks)
	}
	args.MaxParallel = mathutil.ClampInt(args.MaxParallel, defaultMaxParallel, hardMaxTasks)
	if args.MaxTurns <= 0 {
		args.MaxTurns = defaultMaxTurns
	}
	seen := make(map[string]struct{}, len(args.Tasks))
	for i := range args.Tasks {
		args.Tasks[i].Label = strings.TrimSpace(args.Tasks[i].Label)
		args.Tasks[i].Prompt = strings.TrimSpace(args.Tasks[i].Prompt)
		args.Tasks[i].Context = strings.TrimSpace(args.Tasks[i].Context)
		if args.Tasks[i].Label == "" || args.Tasks[i].Prompt == "" {
			return args, fmt.Errorf("task label and prompt are required")
		}
		if _, dup := seen[args.Tasks[i].Label]; dup {
			return args, fmt.Errorf("duplicate task label: %q", args.Tasks[i].Label)
		}
		seen[args.Tasks[i].Label] = struct{}{}
		if args.Tasks[i].TimeoutSeconds < 0 {
			return args, fmt.Errorf("timeout_seconds must not be negative")
		}
		if err := validateAgentName(args.Tasks[i].Agent, deps); err != nil {
			return args, err
		}
	}
	return args, nil
}

func validateAgentName(name string, deps core.CallerDeps) error {
	if name == "" {
		return nil
	}
	if deps.AgentDefs == nil {
		return fmt.Errorf("agent definitions not available; no agents directory loaded")
	}
	if _, ok := deps.AgentDefs.Get(name); !ok {
		available := strings.Join(deps.AgentDefs.Names(), ", ")
		return fmt.Errorf("unknown agent %q; available: %s", name, available)
	}
	return nil
}

// runDelegatesWithUnit runs the delegated tasks, with the per-task timeout unit
// factored out so tests can drive timeouts without real-time waits.
func runDelegatesWithUnit(ctx context.Context, runner core.SubagentRunner, mem memory.Manager, parentSessionID string, args Args, timeoutUnit time.Duration) (string, error) {
	if runner == nil {
		return "", fmt.Errorf("subagent runner is unavailable")
	}
	depth := core.DelegateDepthFromContext(ctx)
	if depth >= maxDelegateDepth {
		return "", fmt.Errorf("delegate_task depth limit reached: subagents cannot delegate further")
	}
	results := make([]core.SubagentResult, len(args.Tasks))
	sem := make(chan struct{}, args.MaxParallel)
	var wg sync.WaitGroup
	var memMu sync.Mutex // serialize memory writes; the manager may be SQLite-backed
	for i, task := range args.Tasks {
		wg.Add(1)
		go func(i int, task Task) {
			defer wg.Done()
			select {
			case <-ctx.Done():
				results[i] = core.SubagentResult{Label: task.Label, Error: ctx.Err().Error(), StopReason: "error"}
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()

			result := runOneTask(ctx, runner, parentSessionID, task, depth, args.MaxTurns, timeoutUnit)
			results[i] = result
			memMu.Lock()
			recordDelegateMemory(ctx, mem, parentSessionID, task, result)
			memMu.Unlock()
		}(i, task)
	}
	wg.Wait()

	resp := Response{Results: results}
	for _, result := range results {
		resp.TotalUsage.PromptTokens += result.Usage.TurnPromptTokens
		resp.TotalUsage.CompletionTokens += result.Usage.TurnCompletionTokens
		resp.TotalUsage.TotalTokens += result.Usage.TurnTotalTokens
	}

	return core.MarshalResult("delegate", resp)
}

// runOneTask runs a single delegated task, applying its optional per-task
// timeout and normalizing the result on failure while preserving any reported usage.
func runOneTask(ctx context.Context, runner core.SubagentRunner, parentSessionID string, task Task, depth, maxTurns int, timeoutUnit time.Duration) core.SubagentResult {
	taskCtx := ctx
	if task.TimeoutSeconds > 0 {
		var cancel context.CancelFunc
		taskCtx, cancel = context.WithTimeout(ctx, time.Duration(task.TimeoutSeconds)*timeoutUnit)
		defer cancel()
	}

	result, err := runner.RunSubagent(taskCtx, core.SubagentRequest{
		Label: task.Label, Prompt: task.Prompt, Context: task.Context, ParentSessionID: parentSessionID,
		MaxTurns: maxTurns, Depth: depth + 1, AgentName: task.Agent,
	})
	if err != nil {
		// Preserve any usage the runner reported before failing.
		result.Label = task.Label
		result.FinalText = ""
		if taskCtx.Err() == context.DeadlineExceeded {
			result.Error = "task timed out"
			result.StopReason = "timeout"
		} else {
			result.Error = err.Error()
			result.StopReason = "error"
		}
	}
	return result
}

// recordDelegateMemory persists the delegated task's input/output into the
// parent agent's memory, since the subagent itself never reads or writes
// memory directly.
func recordDelegateMemory(ctx context.Context, mem memory.Manager, parentSessionID string, task Task, result core.SubagentResult) {
	if mem == nil {
		return
	}
	var content string
	if result.Error != "" {
		content = fmt.Sprintf("Delegated task %q failed.\nInput: %s\nError: %s", task.Label, task.Prompt, result.Error)
	} else {
		content = fmt.Sprintf("Delegated task %q completed.\nInput: %s\nOutput: %s", task.Label, task.Prompt, result.FinalText)
	}
	_, _ = mem.Save(ctx, memcore.Record{
		Scope: config.ScopeProject, Kind: memcore.KindEpisode, Source: memcore.SourceRuntime,
		SessionID: parentSessionID, Content: content,
	})
}
