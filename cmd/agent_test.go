package cmd

import (
	"bytes"
	"context"
	"fmt"
	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/capability/memory"
	"github.com/invokr-ai/invokr/capability/sessionstore"
	"github.com/invokr-ai/invokr/capability/skills"
	"github.com/invokr-ai/invokr/capability/syntax"
	"github.com/invokr-ai/invokr/clients"
	"github.com/invokr-ai/invokr/cmd/agentui"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/events"
	appruntime "github.com/invokr-ai/invokr/platform/runtime"
	"github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/tools"
	"github.com/invokr-ai/invokr/tools/core"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/agent"

	inference "github.com/rivt-ai/go-inference-router"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type mockLLMProvider struct {
	lastCompletionReq inference.Request
	response          *inference.Response
	responses         []*inference.Response
	err               error

	completionCallCount int
}

type stubSession struct{}

func (stubSession) Send(context.Context, string) (turn.AgentResponse, error) {
	return turn.AgentResponse{}, nil
}

func (stubSession) SessionID() string {
	return "stub-session"
}

type resumableStubSession struct {
	id         string
	transcript []turn.TranscriptMessage
}

func (*resumableStubSession) Send(context.Context, string) (turn.AgentResponse, error) {
	return turn.AgentResponse{}, nil
}
func (s *resumableStubSession) SessionID() string { return s.id }
func (s *resumableStubSession) ResumeTranscript(transcript []turn.TranscriptMessage) error {
	s.transcript = append([]turn.TranscriptMessage(nil), transcript...)
	return nil
}

type stubPromptHistory struct {
	appends []string
}

func (h *stubPromptHistory) Append(_ context.Context, text string) error {
	h.appends = append(h.appends, text)
	return nil
}

func (h *stubPromptHistory) Recent(context.Context, int) ([]string, error) {
	return nil, nil
}

func (m *mockLLMProvider) Completion(_ context.Context, req inference.Request) (*inference.Response, error) {
	m.completionCallCount++
	m.lastCompletionReq = req

	if m.response == nil {
		if len(m.responses) > 0 {
			index := m.completionCallCount - 1
			if index < len(m.responses) {
				return m.responses[index], m.err
			}
		}
		return &inference.Response{}, m.err
	}

	return m.response, m.err
}

func (m *mockLLMProvider) Metadata(context.Context, string) (clients.ProviderMetadata, error) {
	return clients.ProviderMetadata{}, nil
}

func testAppConfig() config.AppConfig {
	return config.AppConfig{
		Provider: config.ProviderConfig{
			BaseURL: "http://localhost:8080",
		},
		Agent: config.AgentConfig{
			SystemPrompt: "You are an agent.",
			Temperature:  0.7,
			NPredict:     256,
		},
	}
}

func newTestAgentSession(t *testing.T, provider *mockLLMProvider) *agent.Session {
	t.Helper()

	root := t.TempDir()
	appConfig := testAppConfig()
	eventBus := events.NewOrderedDispatcher(zap.NewNop())
	memoryManager, err := memory.NewManagerFromConfigWithProvider(config.AppConfig{
		Memory: config.MemoryConfig{
			Dir: filepath.Join(root),
		},
	}, eventBus, nil, nil, nil)
	require.NoError(t, err)
	skillManager := skills.NewManagerFromConfig(appConfig, zap.NewNop())
	toolCaller := tools.NewCaller(core.CallerDeps{App: appConfig, Memory: memoryManager, SkillManager: skillManager, Dispatcher: eventBus}, zap.NewNop())
	session, err := agent.NewSession(
		agent.SessionDeps{
			Logger:   zap.NewNop(),
			Provider: provider,
			Events:   eventBus,
			Memory:   memoryManager,
			Syntax:   syntax.NewTaggedJSONSyntax(),
			Skills:   skillManager,
			Tools:    toolCaller,
		},
		appConfig,
	)
	require.NoError(t, err)
	return session
}

func executeAgentCmd(t *testing.T, services appruntime.Services, input string) (string, error) {
	t.Helper()

	cmd := NewAgentCmd(func() (appruntime.Services, error) {
		if services.Session == nil {
			return appruntime.Services{}, fmt.Errorf("runtime services are required")
		}
		return services, nil
	})
	require.NotNil(t, cmd)

	var out bytes.Buffer
	cmd.SetIn(strings.NewReader(input))
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{})

	err := cmd.Execute()
	return out.String(), err
}

func testRuntimeServices(session turn.IdentifiedSession) appruntime.Services {
	return appruntime.Services{
		Session:       session,
		Events:        events.NewOrderedDispatcher(zap.NewNop()),
		Approvals:     approval.NewBroker(),
		PromptHistory: &stubPromptHistory{},
	}
}

func TestNewAgentCmd_Execute_Send(t *testing.T) {
	t.Setenv("INVOKR_TOOLBOX_DIR", "../toolbox")

	mockProvider := &mockLLMProvider{
		response: mockCompletion(`{"type":"reply","message":"hello back"}`),
	}

	agentSession := newTestAgentSession(t, mockProvider)
	got, err := executeAgentCmd(t, testRuntimeServices(agentSession), "hello\nexit\n")
	require.NoError(t, err)

	require.Equal(t, 1, mockProvider.completionCallCount)
	require.Len(t, mockProvider.lastCompletionReq.Messages, 2)
	require.Equal(t, inference.RoleSystem, mockProvider.lastCompletionReq.Messages[0].Role)
	require.Contains(t, mockProvider.lastCompletionReq.Messages[0].Content, "You are an agent.")
	require.Equal(t, inference.RoleUser, mockProvider.lastCompletionReq.Messages[1].Role)
	require.Equal(t, "hello", mockProvider.lastCompletionReq.Messages[1].Content)
	require.NotEmpty(t, mockProvider.lastCompletionReq.Tools)
	require.Equal(t, inference.ToolChoiceAuto, mockProvider.lastCompletionReq.ToolChoice)
	require.NotNil(t, mockProvider.lastCompletionReq.ParallelToolCalls)
	require.True(t, *mockProvider.lastCompletionReq.ParallelToolCalls)
	require.Contains(t, got, "agent started")
	require.Contains(t, got, `hello back`)
	require.Contains(t, got, "usage unavailable")
}

func TestNewAgentCmd_Execute_CallsAgentRunner(t *testing.T) {
	original := runAgentUI
	defer func() { runAgentUI = original }()

	called := false
	var gotLogView bool
	runAgentUI = func(ctx context.Context, p agentui.Params) error {
		called = true
		gotLogView = p.LogView
		require.NotNil(t, ctx)
		require.NotNil(t, p.Session)
		require.NotNil(t, p.EventBus)
		require.NotNil(t, p.Approvals)
		require.NoError(t, p.PromptHistory.Append(ctx, "probe"))
		_, err := io.WriteString(p.Out, "stubbed")
		return err
	}

	history := &stubPromptHistory{}
	services := testRuntimeServices(stubSession{})
	services.PromptHistory = history
	services.LogView = true
	got, err := executeAgentCmd(t, services, "hello\n")
	require.NoError(t, err)
	require.True(t, called)
	require.True(t, gotLogView)
	require.Equal(t, []string{"probe"}, history.appends)
	require.Contains(t, got, "stubbed")
}

func TestNewAgentCmd_ResumeLoadsTranscriptAndSessionID(t *testing.T) {
	original := runAgentUI
	defer func() { runAgentUI = original }()
	dir := t.TempDir()
	store := sessionstore.New(dir)
	for _, item := range []turn.TranscriptMessage{{Role: "user", Text: "old question"}, {Role: "assistant", Text: "old answer"}} {
		if _, err := store.Append(context.Background(), "saved-session", item.Role, item.Text); err != nil {
			t.Fatal(err)
		}
	}
	evidence := []turn.ExecutionEvidence{{Tool: "shell", Outcome: "flagged", ExitCode: new(1), Freshness: "current"}}
	if _, err := store.AppendTurn(context.Background(), "saved-session", "assistant", "Tests pass", sessionstore.ToolRecord{Evidence: evidence}); err != nil {
		t.Fatal(err)
	}
	resumed := &resumableStubSession{id: "saved-session"}
	var params agentui.Params
	runAgentUI = func(_ context.Context, p agentui.Params) error { params = p; return nil }
	services := testRuntimeServices(stubSession{})
	services.App.Memory.Dir = dir
	services.NewChatSession = func(id string) (turn.IdentifiedSession, *approval.Broker, error) {
		if id != "saved-session" {
			t.Fatalf("session id = %q", id)
		}
		return resumed, approval.NewBroker(), nil
	}
	cmd := NewAgentCmd(func() (appruntime.Services, error) { return services, nil })
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--resume", "saved-session"})
	if err := cmd.Execute(); err != nil {
		t.Fatal(err)
	}
	if params.Session != resumed || len(params.Transcript) != 3 || len(resumed.transcript) != 3 {
		t.Fatalf("resume params=%#v session transcript=%#v", params.Transcript, resumed.transcript)
	}
	if got := resumed.transcript[2].ExecutionEvidence; len(got) != 1 || got[0].ExitCode == nil || *got[0].ExitCode != 1 {
		t.Fatalf("resume lost stored evidence: %v", got)
	}
	if !strings.Contains(out.String(), "resumed session saved-session") {
		t.Fatalf("resume notice missing: %q", out.String())
	}
}

func TestNewAgentCmd_Execute_RendersTokenUsage(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: mockCompletionWithUsage(`{"type":"reply","message":"hello back"}`, 1234, 56, 1290),
	}

	agentSession := newTestAgentSession(t, mockProvider)
	got, err := executeAgentCmd(t, testRuntimeServices(agentSession), "hello\nexit\n")
	require.NoError(t, err)
	require.Contains(t, got, "context: 1,290 tokens (window size unknown)")
}

func TestAgentSession_Send_TracksSessionTotals(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: mockCompletionWithUsage(`{"type":"reply","message":"hello back"}`, 10, 2, 12),
	}

	agentSession := newTestAgentSession(t, mockProvider)
	first, err := agentSession.Send(context.Background(), "hello")
	require.NoError(t, err)
	require.True(t, first.Usage.Available)
	require.Equal(t, int64(10), first.Usage.LatestPromptTokens)
	require.Equal(t, int64(2), first.Usage.LatestCompletionTokens)
	require.Equal(t, int64(12), first.Usage.TurnTotalTokens)
	require.Equal(t, int64(12), first.Usage.SessionTotalTokens)

	second, err := agentSession.Send(context.Background(), "hello again")
	require.NoError(t, err)
	require.Equal(t, int64(12), second.Usage.TurnTotalTokens)
	require.Equal(t, int64(24), second.Usage.SessionTotalTokens)
}

func TestAgentSession_Send_SumsMultiCompletionTurn(t *testing.T) {
	mockProvider := &mockLLMProvider{
		responses: []*inference.Response{
			mockCompletionWithUsage("<|tool_call>\n{", 10, 2, 12),
			mockCompletionWithUsage(`{"type":"reply","message":"hello back"}`, 20, 3, 23),
		},
	}

	agentSession := newTestAgentSession(t, mockProvider)
	resp, err := agentSession.Send(context.Background(), "hello")
	require.NoError(t, err)
	require.True(t, resp.Usage.Available)
	require.Equal(t, int64(20), resp.Usage.LatestPromptTokens)
	require.Equal(t, int64(3), resp.Usage.LatestCompletionTokens)
	require.Equal(t, int64(30), resp.Usage.TurnPromptTokens)
	require.Equal(t, int64(5), resp.Usage.TurnCompletionTokens)
	require.Equal(t, int64(35), resp.Usage.TurnTotalTokens)
	require.Equal(t, int64(35), resp.Usage.SessionTotalTokens)
	require.Equal(t, 2, mockProvider.completionCallCount)
}

func TestAgentSession_Send_MissingUsageUnavailable(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: mockCompletion(`{"type":"reply","message":"hello back"}`),
	}

	agentSession := newTestAgentSession(t, mockProvider)
	resp, err := agentSession.Send(context.Background(), "hello")
	require.NoError(t, err)
	require.False(t, resp.Usage.Available)
}

func TestNewAgentCmd_Execute_Exit(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: mockCompletion(`{"type":"reply","message":"should not be called"}`),
	}

	agentSession := newTestAgentSession(t, mockProvider)
	got, err := executeAgentCmd(t, testRuntimeServices(agentSession), "exit\n")
	require.NoError(t, err)

	require.Equal(t, 0, mockProvider.completionCallCount)
	require.Contains(t, got, "agent started")
}

func TestNewAgentCmd_Execute_MissingAgentSession(t *testing.T) {
	_, err := executeAgentCmd(t, appruntime.Services{}, "hello\n")
	require.Error(t, err)
	require.Contains(t, err.Error(), "runtime services are required")
}

func TestNewAgentCmd_Execute_UsesAgentSessionDirectly(t *testing.T) {
	mockProvider := &mockLLMProvider{
		response: mockCompletion(`{"type":"reply","message":"hello back"}`),
	}

	agentSession := newTestAgentSession(t, mockProvider)
	got, err := executeAgentCmd(t, testRuntimeServices(agentSession), "hello\nexit\n")
	require.NoError(t, err)
	require.Contains(t, got, "hello back")
}

func mockCompletion(content string) *inference.Response {
	return &inference.Response{
		FinishReason: inference.FinishStop,
		Message:      inference.AssistantMessage(content),
	}
}

func mockCompletionWithUsage(content string, promptTokens, completionTokens, totalTokens int64) *inference.Response {
	resp := mockCompletion(content)
	resp.ID = "chatcmpl-test"
	resp.Model = "test-model"
	resp.Usage = inference.Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
	}
	return resp
}
