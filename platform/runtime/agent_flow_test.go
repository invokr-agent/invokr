package runtime

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"
	"github.com/invokr-agent/invokr/support/testutil/providertest"
)

func TestRuntime_AgentToolLoopScenarios(t *testing.T) {
	tests := []struct {
		name          string
		app           func(string, string) config.AppConfig
		steps         []providertest.CompletionStep
		wantMessage   string
		wantErr       string
		wantCalls     int
		wantInRequest string
	}{
		{
			name: "tagged tool call",
			steps: []providertest.CompletionStep{
				providertest.ToolCallStep("tools", `{"query":"tools","action":"search"}`),
				providertest.ReplyStep("done"),
			},
			wantMessage:   "done",
			wantCalls:     2,
			wantInRequest: `"type":"tool_result"`,
		},
		{
			name: "native tool call preserves call id",
			steps: []providertest.CompletionStep{
				providertest.NativeToolCallStep("call_search_1", "tools", `{"query":"tools","action":"search"}`),
				providertest.ReplyStep("native done"),
			},
			wantMessage:   "native done",
			wantCalls:     2,
			wantInRequest: "call_search_1",
		},
		{
			name: "unknown tool feeds error back",
			steps: []providertest.CompletionStep{
				providertest.ToolCallStep("missing_tool", `{}`),
				providertest.ReplyStep("handled missing"),
			},
			wantMessage:   "handled missing",
			wantCalls:     2,
			wantInRequest: "unknown tool",
		},
		{
			name: "malformed tagged call feeds parse error back",
			steps: []providertest.CompletionStep{
				{Content: "<|tool_call>\nnot json\n<|tool_call|>"},
				providertest.ReplyStep("handled malformed"),
			},
			wantMessage:   "handled malformed",
			wantCalls:     2,
			wantInRequest: "invalid character",
		},
		{
			name: "max turns degrades to best-effort answer",
			app: func(baseURL, memoryDir string) config.AppConfig {
				app := runtimeTestApp(baseURL, memoryDir)
				app.Agent.MaxTurns = 2
				// Static weights: this scenario is about the step-limit
				// wrap-up, and tool_search is read-only, so at the default
				// half price two calls would not spend the budget.
				app.Agent.ReadTurnWeight, app.Agent.HardTurnMultiplier = 1, 1
				return app
			},
			steps: []providertest.CompletionStep{
				providertest.ToolCallStep("tools", `{"query":"tools","action":"search"}`),
				providertest.ToolCallStep("tools", `{"query":"tools","action":"search"}`),
				providertest.ReplyStep("best effort answer"),
			},
			// The step limit ends this run, so the answer carries the banner.
			wantMessage: "\u26a0 step limit reached (2 turns): work may be incomplete\n\nbest effort answer",
			wantCalls:   3,
		},
		{
			name: "repeated identical tool call nudges model",
			steps: []providertest.CompletionStep{
				providertest.ToolCallStep("tools", `{"query":"tools","action":"search"}`),
				providertest.ToolCallStep("tools", `{"query":"tools","action":"search"}`),
				providertest.ToolCallStep("tools", `{"query":"tools","action":"search"}`),
				providertest.ReplyStep("changed approach"),
			},
			wantMessage:   "changed approach",
			wantCalls:     4,
			wantInRequest: "Vary it instead",
		},
		{
			name: "empty completion nudges model to answer",
			steps: []providertest.CompletionStep{
				{}, // blank: no content, no tool call
				providertest.ReplyStep("recovered answer"),
			},
			wantMessage:   "recovered answer",
			wantCalls:     2,
			wantInRequest: "Your previous response was empty",
		},
		{
			name: "parallel native tool calls both feed back",
			steps: []providertest.CompletionStep{
				{ToolCalls: []providertest.CompletionToolCall{
					{ID: "call_a", Type: "function", Function: providertest.CompletionToolFunction{Name: "tools", Arguments: `{"query":"alpha","action":"search"}`}},
					{ID: "call_b", Type: "function", Function: providertest.CompletionToolFunction{Name: "tools", Arguments: `{"query":"beta","action":"search"}`}},
				}},
				providertest.ReplyStep("parallel done"),
			},
			wantMessage:   "parallel done",
			wantCalls:     2,
			wantInRequest: "call_b",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			provider := providertest.New(t, tt.steps)
			defer provider.Close()
			appFn := tt.app
			if appFn == nil {
				appFn = runtimeTestApp
			}
			rt := bootstrapRuntimeForTest(t, appFn(provider.URL(), t.TempDir()))
			defer func() { _ = rt.Cleanup() }()

			resp, err := rt.Services().Session.Send(context.Background(), "run scenario")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Send() error = %v, want %q", err, tt.wantErr)
				}
			} else {
				if err != nil {
					t.Fatalf("Send() error = %v", err)
				}
				if resp.Message != tt.wantMessage {
					t.Fatalf("message = %q, want %q", resp.Message, tt.wantMessage)
				}
			}
			provider.AssertClean(t)
			snapshot := provider.Snapshot()
			if got := len(snapshot.CompletionRequests); got != tt.wantCalls {
				t.Fatalf("completion calls = %d, want %d", got, tt.wantCalls)
			}
			if tt.wantInRequest != "" && !requestsContain(snapshot.CompletionRequests, tt.wantInRequest) {
				t.Fatalf("completion requests did not contain %q: %#v", tt.wantInRequest, snapshot.CompletionRequests)
			}
		})
	}
}

func TestRuntime_EmitsAgentAndToolEvents(t *testing.T) {
	provider := providertest.NewScripted(t,
		providertest.ToolCallStep("tools", `{"query":"tools","action":"search"}`),
		providertest.ReplyStep("done"),
	)
	defer provider.Close()
	rt := bootstrapRuntimeForTest(t, runtimeTestApp(provider.URL(), t.TempDir()))
	defer func() { _ = rt.Cleanup() }()

	var got []events.Event
	rt.Services().Events.Register(events.HandlerFunc(func(_ context.Context, ev events.Event) error {
		switch ev.Type {
		case events.EventBeforePrompt, events.EventAfterPrompt, events.EventAfterResponse, events.EventToolStarted, events.EventToolCompleted:
			got = append(got, ev)
		}
		return nil
	}))

	resp, err := rt.Services().Session.Send(context.Background(), "emit events")
	if err != nil {
		t.Fatalf("Send() error = %v", err)
	}
	if resp.Message != "done" {
		t.Fatalf("message = %q, want done", resp.Message)
	}

	want := []events.EventType{
		events.EventBeforePrompt,
		events.EventAfterPrompt,
		events.EventToolStarted,
		events.EventToolCompleted,
		events.EventAfterResponse,
	}
	if len(got) != len(want) {
		t.Fatalf("events = %d, want %d: %#v", len(got), len(want), got)
	}
	var sessionID string
	for i, ev := range got {
		if ev.Type != want[i] {
			t.Fatalf("event %d type = %q, want %q: %#v", i, ev.Type, want[i], got)
		}
		if ev.SessionID == "" {
			t.Fatalf("event %d session id is empty: %#v", i, ev)
		}
		if sessionID == "" {
			sessionID = ev.SessionID
		}
		if ev.SessionID != sessionID {
			t.Fatalf("event %d session id = %q, want %q", i, ev.SessionID, sessionID)
		}
	}
	if got[2].Tool == nil || got[2].Tool.Name != "tools" {
		t.Fatalf("tool started payload = %#v", got[2])
	}
	if got[3].Tool == nil || got[3].Tool.OutputBytes == 0 {
		t.Fatalf("tool completed payload = %#v", got[3])
	}
}

func TestRuntime_ProviderMetadataBehavior(t *testing.T) {
	t.Run("metadata fills an unset window", func(t *testing.T) {
		provider := providertest.New(t, nil, providertest.WithContextWindow(8192))
		defer provider.Close()
		app := runtimeTestApp(provider.URL(), t.TempDir())

		rt := bootstrapRuntimeForTest(t, app)
		defer func() { _ = rt.Cleanup() }()
		usage := rt.Services().Session.(interface{ Usage() turn.TokenUsage })
		want := int64(8192)
		deadline := time.Now().Add(time.Second)
		for usage.Usage().ContextWindowTokens != want && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if got := usage.Usage().ContextWindowTokens; got != want {
			t.Fatalf("session context window = %d, want discovered %d", got, want)
		}
	})

	// A configured window is a pin: discovery must not raise it, or the
	// compaction threshold silently moves to the provider's limit.
	t.Run("configured window pins over metadata", func(t *testing.T) {
		provider := providertest.New(t, nil, providertest.WithContextWindow(8192))
		defer provider.Close()
		app := runtimeTestApp(provider.URL(), t.TempDir())
		app.Provider.ContextWindowTokens = 1024

		rt := bootstrapRuntimeForTest(t, app)
		defer func() { _ = rt.Cleanup() }()
		usage := rt.Services().Session.(interface{ Usage() turn.TokenUsage })
		// No poll loop here (unlike the discovery case above): the pin holds
		// regardless of when discovery finishes, so a fixed sleep is enough —
		// there is no race to poll for.
		time.Sleep(50 * time.Millisecond)
		// The pin (1024) is smaller than the reserved headroom: the
		// reservation never eats into the window itself (0 means "unknown"
		// elsewhere and disables budgets), so the unreserved pin holds.
		if got := usage.Usage().ContextWindowTokens; got != 1024 {
			t.Fatalf("session context window = %d, want the configured pin 1024 unreserved", got)
		}
	})

	t.Run("missing metadata keeps fallback", func(t *testing.T) {
		provider := providertest.New(t, nil)
		defer provider.Close()
		app := runtimeTestApp(provider.URL(), t.TempDir())
		app.Provider.ContextWindowTokens = 1024

		rt := bootstrapRuntimeForTest(t, app)
		defer func() { _ = rt.Cleanup() }()
		if got := rt.Services().App.Provider.ContextWindowTokens; got != 1024 {
			t.Fatalf("context window = %d, want 1024", got)
		}
		usage := rt.Services().Session.(interface{ Usage() turn.TokenUsage })
		if got := usage.Usage().ContextWindowTokens; got != 1024 {
			t.Fatalf("session context window = %d, want fallback 1024", got)
		}
	})
}

func bootstrapRuntimeForTest(t *testing.T, app config.AppConfig) *Runtime {
	t.Helper()
	rt, err := Bootstrap(app, Options{})
	if err != nil {
		t.Fatalf("Bootstrap() error = %v", err)
	}
	return rt
}

func runtimeTestApp(baseURL, memoryDir string) config.AppConfig {
	return config.AppConfig{
		Provider: config.ProviderConfig{
			BaseURL: baseURL,
			Model:   "runtime-test-model",
		},
		Agent: config.AgentConfig{
			SystemPrompt:      "You are an agent.",
			NPredict:          64,
			MaxTurns:          4,
			NativeToolCalling: true,
		},
		// Titling makes an extra background model call; these tests assert exact
		// scripted completion counts, so keep it off.
		Memory: config.MemoryConfig{Dir: memoryDir, DisableSessionTitles: true},
		Toolbox: config.ToolboxConfig{
			TokfDisabled: true,
			TokfHome:     memoryDir + "/tokf",
		},
	}
}

func requestsContain(requests []providertest.CompletionRequest, needle string) bool {
	for _, req := range requests {
		for _, msg := range req.Messages {
			if messageContains(msg, needle) {
				return true
			}
		}
	}
	return false
}

func messageContains(msg providertest.ChatMessage, needle string) bool {
	return strings.Contains(messageString(msg.Content), needle) ||
		strings.Contains(messageString(msg.ToolCalls), needle) ||
		strings.Contains(msg.ToolCallID, needle)
}

func messageString(value any) string {
	switch v := value.(type) {
	case string:
		return v
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}
