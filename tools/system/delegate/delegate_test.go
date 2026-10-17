package delegate

import (
	"context"
	"encoding/json"
	"github.com/invokr-ai/invokr/capability/agentdef"
	memcore "github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/tools/core"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRunner struct {
	mu    sync.Mutex
	seen  []core.SubagentRequest
	delay time.Duration
	err   error
	usage turn.TokenUsage
}

func (r *fakeRunner) RunSubagent(ctx context.Context, req core.SubagentRequest) (core.SubagentResult, error) {
	r.mu.Lock()
	r.seen = append(r.seen, req)
	r.mu.Unlock()
	if r.delay > 0 {
		select {
		case <-time.After(r.delay):
		case <-ctx.Done():
			return core.SubagentResult{Usage: r.usage}, ctx.Err()
		}
	}
	if r.err != nil {
		return core.SubagentResult{Usage: r.usage}, r.err
	}
	return core.SubagentResult{Label: req.Label, FinalText: "done " + req.Label, StopReason: "completed", Usage: r.usage}, nil
}

type fakeMemory struct {
	mu      sync.Mutex
	records []memcore.Record
}

func (m *fakeMemory) Close() error { return nil }
func (m *fakeMemory) PromptBlocks(context.Context, memcore.PromptRequest) (memcore.PromptBlocks, error) {
	return nil, nil
}
func (m *fakeMemory) Save(_ context.Context, rec memcore.Record) (memcore.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, rec)
	return rec, nil
}
func (m *fakeMemory) Update(_ context.Context, _ string, rec memcore.Record) (memcore.Record, error) {
	return rec, nil
}
func (m *fakeMemory) UpdateMetadata(context.Context, string, map[string]string) error { return nil }
func (m *fakeMemory) Delete(context.Context, string) error                            { return nil }
func (m *fakeMemory) List(context.Context, memcore.Query) ([]memcore.Record, error)   { return nil, nil }
func (m *fakeMemory) Recall(context.Context, memcore.Query) ([]memcore.Hit, error)    { return nil, nil }
func (m *fakeMemory) SearchArchive(context.Context, string, int) ([]memcore.Record, error) {
	return nil, nil
}
func (m *fakeMemory) RestoreArchived(context.Context, []string) ([]string, error) { return nil, nil }

func TestPrepareArgsRejectsInvalidTasks(t *testing.T) {
	if _, err := prepareArgs(context.Background(), Args{}, core.CallerDeps{}); err == nil {
		t.Fatal("expected empty task rejection")
	}
	tooMany := Args{Tasks: make([]Task, hardMaxTasks+1)}
	if _, err := prepareArgs(context.Background(), tooMany, core.CallerDeps{}); err == nil {
		t.Fatal("expected too many tasks rejection")
	}
}

func TestPrepareArgsRejectsDuplicateLabels(t *testing.T) {
	args := Args{Tasks: []Task{{Label: "dup", Prompt: "a"}, {Label: "dup", Prompt: "b"}}}
	if _, err := prepareArgs(context.Background(), args, core.CallerDeps{}); err == nil {
		t.Fatal("expected duplicate label rejection")
	}
}

func TestRunDelegatesReturnsFinalSummariesOnly(t *testing.T) {
	runner := &fakeRunner{}
	args, err := prepareArgs(context.Background(), Args{Tasks: []Task{{Label: "tests", Prompt: "check tests"}}, MaxParallel: 1}, core.CallerDeps{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := runDelegatesWithUnit(context.Background(), runner, nil, "parent", args, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].FinalText != "done tests" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if len(runner.seen) != 1 || runner.seen[0].ParentSessionID != "parent" || !strings.Contains(runner.seen[0].Prompt, "check") {
		t.Fatalf("unexpected runner request: %+v", runner.seen)
	}
	if runner.seen[0].Depth != 1 {
		t.Fatalf("expected depth 1, got %d", runner.seen[0].Depth)
	}
}

func TestRunDelegatesRejectsAtDepthLimit(t *testing.T) {
	runner := &fakeRunner{}
	args, err := prepareArgs(context.Background(), Args{Tasks: []Task{{Label: "tests", Prompt: "check tests"}}}, core.CallerDeps{})
	if err != nil {
		t.Fatal(err)
	}
	ctx := core.WithDelegateDepth(context.Background(), maxDelegateDepth)
	if _, err := runDelegatesWithUnit(ctx, runner, nil, "parent", args, time.Second); err == nil {
		t.Fatal("expected depth limit rejection")
	}
	if len(runner.seen) != 0 {
		t.Fatalf("runner should not have been invoked, saw %d calls", len(runner.seen))
	}
}

func TestRunDelegatesAppliesPerTaskTimeout(t *testing.T) {
	// Runner blocks far longer than the per-task timeout; the parent context
	// has no deadline, so a timeout can only come from the per-task budget.
	runner := &fakeRunner{delay: time.Hour, usage: turn.TokenUsage{TurnTotalTokens: 7}}
	args := Args{Tasks: []Task{{Label: "slow", Prompt: "do work", TimeoutSeconds: 1}}}
	prepared, err := prepareArgs(context.Background(), args, core.CallerDeps{})
	if err != nil {
		t.Fatal(err)
	}
	// Shrink the task timeout to keep the test fast while still being driven by
	// the per-task budget rather than the parent context.
	prepared.Tasks[0].TimeoutSeconds = 1
	out, err := runDelegatesWithUnit(context.Background(), runner, nil, "parent", prepared, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Results) != 1 || resp.Results[0].StopReason != "timeout" {
		t.Fatalf("expected timeout stop reason, got %+v", resp.Results)
	}
	if resp.Results[0].Usage.TurnTotalTokens != 7 || resp.TotalUsage.TotalTokens != 7 {
		t.Fatalf("expected usage preserved on timeout, got %+v / %+v", resp.Results[0].Usage, resp.TotalUsage)
	}
}

func TestRunDelegatesPreservesUsageOnError(t *testing.T) {
	runner := &fakeRunner{err: context.Canceled, usage: turn.TokenUsage{TurnPromptTokens: 3, TurnCompletionTokens: 2, TurnTotalTokens: 5}}
	args, err := prepareArgs(context.Background(), Args{Tasks: []Task{{Label: "boom", Prompt: "do work"}}}, core.CallerDeps{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := runDelegatesWithUnit(context.Background(), runner, nil, "parent", args, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	var resp Response
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Results[0].StopReason != "error" || resp.Results[0].Usage.TurnTotalTokens != 5 {
		t.Fatalf("expected usage preserved on error, got %+v", resp.Results[0])
	}
	if resp.TotalUsage.TotalTokens != 5 || resp.TotalUsage.PromptTokens != 3 || resp.TotalUsage.CompletionTokens != 2 {
		t.Fatalf("unexpected aggregated usage: %+v", resp.TotalUsage)
	}
}

func TestRunDelegatesRecordsInputOutputInParentMemory(t *testing.T) {
	runner := &fakeRunner{}
	mem := &fakeMemory{}
	args, err := prepareArgs(context.Background(), Args{Tasks: []Task{{Label: "tests", Prompt: "check tests"}}}, core.CallerDeps{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runDelegatesWithUnit(context.Background(), runner, mem, "parent", args, time.Second); err != nil {
		t.Fatal(err)
	}
	if len(mem.records) != 1 {
		t.Fatalf("expected one memory record, got %d", len(mem.records))
	}
	rec := mem.records[0]
	if rec.SessionID != "parent" {
		t.Fatalf("expected record tagged with parent session, got %q", rec.SessionID)
	}
	if !strings.Contains(rec.Content, "check tests") || !strings.Contains(rec.Content, "done tests") {
		t.Fatalf("expected record to contain input and output, got %q", rec.Content)
	}
	if rec.Kind != memcore.KindEpisode || rec.Scope != config.ScopeProject {
		t.Fatalf("unexpected record kind/scope: %+v", rec)
	}
}

// fakeAgentRegistry is a minimal agentdef.Registry for delegate tests.
type fakeAgentRegistry struct {
	names []string
}

func (r *fakeAgentRegistry) Get(name string) (agentdef.Definition, bool) {
	for _, n := range r.names {
		if n == name {
			return agentdef.Definition{Name: n, Description: "test", Instructions: "test"}, true
		}
	}
	return agentdef.Definition{}, false
}

func (r *fakeAgentRegistry) Names() []string { return r.names }

func TestPrepareArgsUnknownAgentName(t *testing.T) {
	deps := core.CallerDeps{AgentDefs: &fakeAgentRegistry{names: []string{"coder"}}}
	args := Args{Tasks: []Task{{Label: "x", Prompt: "p", Agent: "missing"}}}
	_, err := prepareArgs(context.Background(), args, deps)
	if err == nil {
		t.Fatal("expected error for unknown agent")
	}
	if !strings.Contains(err.Error(), "available: coder") {
		t.Errorf("error %q should list available agents", err.Error())
	}
}

func TestPrepareArgsValidAgentNamePasses(t *testing.T) {
	deps := core.CallerDeps{AgentDefs: &fakeAgentRegistry{names: []string{"coder"}}}
	args := Args{Tasks: []Task{{Label: "x", Prompt: "p", Agent: "coder"}}}
	_, err := prepareArgs(context.Background(), args, deps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPrepareArgsNoAgentWithNilRegistry(t *testing.T) {
	deps := core.CallerDeps{AgentDefs: nil}
	args := Args{Tasks: []Task{{Label: "x", Prompt: "p"}}}
	_, err := prepareArgs(context.Background(), args, deps)
	if err != nil {
		t.Fatalf("unexpected error for no agent specified: %v", err)
	}
}

func TestPrepareArgsAgentSetWithNilRegistry(t *testing.T) {
	deps := core.CallerDeps{AgentDefs: nil}
	args := Args{Tasks: []Task{{Label: "x", Prompt: "p", Agent: "x"}}}
	_, err := prepareArgs(context.Background(), args, deps)
	if err == nil {
		t.Fatal("expected error when agent set but registry nil")
	}
	if !strings.Contains(err.Error(), "agent definitions not available") {
		t.Errorf("error %q should mention 'agent definitions not available'", err.Error())
	}
}

// The nested tasks[] array is a hard shape for a small model, so a single
// sub-task can be given flat as prompt/label.
func TestPrepareArgsSingleTaskShorthand(t *testing.T) {
	got, err := prepareArgs(context.Background(), Args{Prompt: " find the tests ", Label: "scan"}, core.CallerDeps{})
	if err != nil {
		t.Fatalf("prepareArgs: %v", err)
	}
	if len(got.Tasks) != 1 || got.Tasks[0].Prompt != "find the tests" || got.Tasks[0].Label != "scan" {
		t.Fatalf("tasks = %+v", got.Tasks)
	}
	if got.Prompt != "" || got.Label != "" {
		t.Fatalf("shorthand fields must be folded into tasks, got %+v", got)
	}
}

func TestPrepareArgsRejectsNoTaskAtAll(t *testing.T) {
	if _, err := prepareArgs(context.Background(), Args{}, core.CallerDeps{}); err == nil {
		t.Fatal("expected an error when neither prompt nor tasks is given")
	}
}

// The shorthand is advertised as prompt-only, so omitting label must work
// rather than failing validation on a field the model was never asked for.
func TestPrepareArgsSingleTaskShorthandWithoutLabel(t *testing.T) {
	got, err := prepareArgs(context.Background(), Args{Prompt: "find the tests"}, core.CallerDeps{})
	if err != nil {
		t.Fatalf("prepareArgs: %v", err)
	}
	if len(got.Tasks) != 1 || got.Tasks[0].Label == "" {
		t.Fatalf("tasks = %+v, want a defaulted label", got.Tasks)
	}
}
