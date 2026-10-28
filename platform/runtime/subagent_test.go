package runtime

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/invokr-agent/invokr/capability/agentdef"
	"github.com/invokr-agent/invokr/capability/skills"
	"github.com/invokr-agent/invokr/capability/syntax"
	"github.com/invokr-agent/invokr/clients"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/support/testutil/providertest"
	"github.com/invokr-agent/invokr/tools/core"

	"go.uber.org/zap"
)

// TestResolveToolCallerBlocksParentMemoryByDefault verifies that a subagent
// without an explicit tool allowlist cannot see or call the parent's memory
// tools (issue #323).
func TestResolveToolCallerBlocksParentMemoryByDefault(t *testing.T) {
	inner := newStub("bash", "memory", "recall", "recall_archive", "session_search")
	r := &subagentRunner{tools: inner}

	caller, err := r.resolveToolCaller("", agentdef.Definition{}, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	specs := caller.Specs()
	if len(specs) != 1 || specs[0].Name != "bash" {
		t.Errorf("expected only [bash] visible, got %v", specs)
	}

	for _, name := range parentMemoryTools {
		_, callErr := caller.HandleCall(context.Background(), &core.ToolCall{Name: name})
		var te *core.ToolError
		if !errors.As(callErr, &te) {
			t.Errorf("tool %q: expected ToolError, got %v", name, callErr)
		}
	}

	if out, callErr := caller.HandleCall(context.Background(), &core.ToolCall{Name: "bash"}); callErr != nil || out != "result:bash" {
		t.Errorf("bash should pass through, got %q, %v", out, callErr)
	}
}

// TestResolveToolCallerAllowlistCanIncludeMemory verifies that an explicit
// agent-definition allowlist may still grant memory tools.
func TestResolveToolCallerAllowlistCanIncludeMemory(t *testing.T) {
	inner := newStub("bash", "memory", "recall")
	r := &subagentRunner{tools: inner}
	def := agentdef.Definition{Tools: []string{"recall"}, ToolsExplicit: true}

	caller, err := r.resolveToolCaller("helper", def, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out, callErr := caller.HandleCall(context.Background(), &core.ToolCall{Name: "recall"}); callErr != nil || out != "result:recall" {
		t.Errorf("recall should be allowed via allowlist, got %q, %v", out, callErr)
	}
}

// schemaTrackingCaller is a shared parent caller that records per-session
// schema-loaded state the way *tools.Caller does. HandleCall marks the calling
// session's schema loaded (what tool_search does), so a completed subagent run
// leaves residue behind unless something calls ForgetSession.
type schemaTrackingCaller struct {
	stubCaller
	mu     sync.Mutex
	loaded map[string]map[string]struct{}
}

func newSchemaTrackingCaller(names ...string) *schemaTrackingCaller {
	return &schemaTrackingCaller{
		stubCaller: *newStub(names...),
		loaded:     map[string]map[string]struct{}{},
	}
}

func (s *schemaTrackingCaller) MarkSchemasLoaded(sessionID string, names []string) {
	if sessionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loaded[sessionID] == nil {
		s.loaded[sessionID] = map[string]struct{}{}
	}
	for _, name := range names {
		s.loaded[sessionID][name] = struct{}{}
	}
}

func (s *schemaTrackingCaller) ForgetSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.loaded, sessionID)
}

func (s *schemaTrackingCaller) HandleCall(ctx context.Context, call *core.ToolCall) (string, error) {
	s.MarkSchemasLoaded(core.SessionIDFromContext(ctx), []string{call.Name})
	return s.stubCaller.HandleCall(ctx, call)
}

func (s *schemaTrackingCaller) sessions() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.loaded))
	for id := range s.loaded {
		ids = append(ids, id)
	}
	return ids
}

// TestRunSubagentForgetsSessionOnSharedCaller guards the close hook itself:
// subagents run against the parent's long-lived caller through filteredCaller,
// so a finished run must leave no per-session state behind (#317 B11). Dropping
// the deferred session.Close() in RunSubagent fails this test.
func TestRunSubagentPreservesStatusAndForgetsSession(t *testing.T) {
	for _, tc := range []struct {
		name       string
		maxTurns   int
		stopReason string
	}{
		{"completed", 4, "completed"}, {"exhausted", 1, "step_limit"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := providertest.NewScripted(t,
				providertest.ToolCallStep("probe_tool", `{}`),
				providertest.ReplyStep("subagent done"),
			)
			defer provider.Close()

			shared := newSchemaTrackingCaller("probe_tool")
			// Pre-existing state for an unrelated session must survive the run.
			shared.MarkSchemasLoaded("parent-session", []string{"probe_tool"})

			app := config.AppConfig{
				Provider: config.ProviderConfig{BaseURL: provider.URL(), Model: "subagent-test-model"},
				Agent:    config.AgentConfig{SystemPrompt: "You are a subagent.", NPredict: 64, MaxTurns: tc.maxTurns, HardTurnMultiplier: 1},
				Memory:   config.MemoryConfig{Dir: t.TempDir(), DisableSessionTitles: true},
				Toolbox:  config.ToolboxConfig{TokfDisabled: true},
			}.WithDefaults()

			r := &subagentRunner{
				logger:   zap.NewNop(),
				provider: clients.NewClientWithSettings(provider.URL(), "", app.Provider.HTTPTimeout, app.Provider.MaxRetries),
				events:   events.NewOrderedDispatcher(nil),
				skills:   skills.NewManagerFromConfig(app, zap.NewNop()),
				tools:    shared,
				syntax:   syntax.NewTaggedJSONSyntax(),
				app:      app,
			}

			res, err := r.RunSubagent(context.Background(), core.SubagentRequest{Label: "probe", Prompt: "do the thing"})
			if err != nil {
				t.Fatalf("RunSubagent() error = %v", err)
			}
			if res.StopReason != tc.stopReason {
				t.Fatalf("stop reason = %q (%s), want %q", res.StopReason, res.Error, tc.stopReason)
			}

			if got := shared.sessions(); len(got) != 1 || got[0] != "parent-session" {
				t.Fatalf("shared caller sessions after subagent run = %v, want only [parent-session]", got)
			}

		})
	}
}

type flaggedChildCaller struct{ stubCaller }

func (*flaggedChildCaller) HandleCall(context.Context, *core.ToolCall) (string, error) {
	return "check failed", &core.ToolError{Code: "exit 1", Payload: "check failed"}
}
func TestSubagentPropagatesFlaggedEvidence(t *testing.T) {
	provider := providertest.NewScripted(t, providertest.ToolCallStep("probe_tool", `{}`), providertest.ReplyStep("Tests pass"))
	defer provider.Close()
	app := config.AppConfig{Provider: config.ProviderConfig{BaseURL: provider.URL(), Model: "test"}, Agent: config.AgentConfig{MaxTurns: 4}, Memory: config.MemoryConfig{Dir: t.TempDir(), DisableSessionTitles: true}, Toolbox: config.ToolboxConfig{TokfDisabled: true}}.WithDefaults()
	r := &subagentRunner{logger: zap.NewNop(), provider: clients.NewClientWithSettings(provider.URL(), "", app.Provider.HTTPTimeout, app.Provider.MaxRetries), events: events.NewOrderedDispatcher(nil), skills: skills.NewManagerFromConfig(app, zap.NewNop()), tools: &flaggedChildCaller{*newStub("probe_tool")}, syntax: syntax.NewTaggedJSONSyntax(), app: app}
	var ledger events.ExecutionLedger
	payload := events.ToolPayload{ID: "delegate", Name: "delegate_task"}
	ledger.Observe(events.EventToolStarted, payload, true)
	res, err := r.RunSubagent(ledger.Context(context.Background(), "delegate", 0), core.SubagentRequest{Label: "test", Prompt: "test"})
	if err != nil {
		t.Fatal(err)
	}
	ledger.Observe(events.EventToolCompleted, payload, true)
	if !strings.Contains(res.FinalText, "flagged") || !strings.Contains(res.FinalText, "historical") || ledger.Snapshot()[0].Outcome != "flagged" {
		t.Fatalf("lost child evidence: %s %+v", res.FinalText, ledger.Snapshot())
	}
}
