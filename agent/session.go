package agent

import (
	"cmp"
	"context"
	"fmt"
	"github.com/invokr-ai/invokr/capability/agentprompt"
	"github.com/invokr-ai/invokr/clients"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/todo"
	"github.com/invokr-ai/invokr/tools/system/tool_search"
	"github.com/invokr-ai/invokr/tools/system/workspace"
	"slices"
	"strings"

	"github.com/google/uuid"
	inference "github.com/rivt-ai/go-inference-router"
)

func NewSession(agentDeps SessionDeps, settings config.AppConfig) (*Session, error) {
	if err := validateSessionDeps(agentDeps); err != nil {
		return nil, err
	}
	logger, toolCaller := agentDeps.Logger, agentDeps.Tools
	// Snapshot of declared capabilities so compaction can pin the newest
	// mutating tool result (shares core.CapabilityIndex with runtime.ReadOnlyTools).
	toolCapabilities := core.CapabilityIndex(toolCaller.Specs())
	eventDispatcher, sessionID := agentDeps.Events, strings.TrimSpace(agentDeps.SessionID)
	if sessionID == "" {
		sessionID = uuid.NewString()
	}
	if manager, ok := toolCaller.(interface{ StartToolSession(string) error }); ok {
		if err := manager.StartToolSession(sessionID); err != nil {
			return nil, err
		}
	}
	settings = settings.WithDefaults()
	if err := settings.Agent.ValidateModelOptions(); err != nil {
		return nil, err
	}
	providerContext, backgroundContext := sessionContexts(agentDeps, settings)

	// The builder is constructed before the gateway but needs the gateway's
	// native-schema size to attribute the recorded prompt; the closure is only
	// called from Build, by which point gateway is assigned.
	var gateway *OpenAIModelGateway
	promptBuilder, err := newPromptBuilder(agentDeps, settings, func(id string) int {
		_, chars := gateway.PromptBreakdown(id)
		return int(chars)
	})
	if err != nil {
		return nil, err
	}
	if _, err := clients.NormalizeReasoningEffort(settings.Agent.ReasoningEffort); err != nil {
		return nil, fmt.Errorf("agent config: %w", err)
	}

	spend := newSpendCap(settings.Agent, settings.Provider.Pricing, logger)
	gateway = &OpenAIModelGateway{
		provider: agentDeps.Provider, model: settings.Provider.Model, settings: settings.Agent,
		tools: toolCaller, prompts: promptBuilder, syntax: agentDeps.Syntax,
		logger: logger, codec: OpenAINativeToolCodec{}, events: eventDispatcher,
		parentSessionID: agentDeps.ParentSessionID,
		// Explicit cache markers are opt-in per provider; automatic-caching
		// providers leave this nil and see an unchanged request body.
		promptCacheExtra: settings.Provider.PromptCache.RequestExtra(),
		reasoningEffort:  settings.Agent.ReasoningEffort,
		providerContext:  providerContext,
		// Meter compaction spend into the session caps (issue #295);
		// the meter is shared by pointer, so the runner's copy sees it.
		recordSummarizerSpend: spend.observeSummarizer,
	}
	processor := &TurnProcessor{
		toolCaps: toolCapabilities,
		events:   eventDispatcher, sessionID: sessionID, parentSessionID: agentDeps.ParentSessionID,
		syntax: agentDeps.Syntax, tools: toolCaller, logger: logger,
		codec: OpenAINativeToolCodec{}, gate: newResultGate(settings.Agent, sessionID, logger),
		quotas: settings.Agent.ToolQuotas,
	}

	runner := &Runner{
		modelGateway: gateway, turnProcessor: processor, events: eventDispatcher,
		settings: settings.Agent, logger: logger, parentSessionID: agentDeps.ParentSessionID,
		usage: turn.NewSessionTokenUsage(providerContext.ContextWindowTokens()),
		budget: &contextBudget{
			windowSize: int64(settings.Provider.ContextWindowTokens), providerContext: providerContext, backgroundContext: backgroundContext,
			warningPercent: settings.Agent.ContextWarningPercent, compactPercent: settings.Agent.ContextCompactPercent,
			gateway: gateway, events: eventDispatcher, logger: logger, parentSessionID: agentDeps.ParentSessionID,
			writesWorkspace: func(name string) bool { return name == "tools" || toolCapabilities[name].WritesWorkspace },
			toolState:       func() string { return toolStateReminder(toolCaller, sessionID) },
			spill:           processor.gate.spill, workspaceRoot: workspace.NewWorkspacePolicy(settings.Workspace).Root,
			todo: func() string {
				if slices.ContainsFunc(toolCaller.PromptSpecs(sessionID), func(spec core.ToolSpec) bool { return spec.Name == "todo" }) {
					return todo.Render(sessionID)
				}
				return ""
			},
		},
		providerContext: providerContext, provider: agentDeps.Provider,
		backgroundContext: backgroundContext, spend: spend, toolCaps: toolCapabilities,
	}
	providerContext.SetModel(settings.Provider.Model)
	runner.refreshProviderMetadata(settings.Provider.Model)
	return &Session{sessionID: sessionID, runner: runner, tools: toolCaller, prompts: promptBuilder}, nil
}

// Close releases session-scoped resources held by the tool layer: the
// per-session schema-loaded state tracked by the caller (#317) and event-bus
// handlers registered per session (issue #324). Safe to call on a session
// whose tool caller has no releasable state; idempotent otherwise.
func (s *Session) Close() {
	s.tools.ForgetSession(s.sessionID)
	if closer, ok := s.tools.(interface{ Close() }); ok {
		closer.Close()
	}
}

func newPromptBuilder(deps SessionDeps, settings config.AppConfig, schemaChars func(string) int) (*agentprompt.Builder, error) {
	return agentprompt.NewBuilder(agentprompt.Config{
		BasePrompt: settings.Agent.SystemPrompt, PromptSource: settings.Agent.PromptSource, NativeToolCalling: settings.Agent.NativeToolCalling,
		Environment: agentprompt.DetectEnvironment(settings.Workspace.Root),
	}, agentprompt.Deps{
		Events: deps.Events, Memory: deps.Memory, Skills: deps.Skills, Tools: deps.Tools, Syntax: deps.Syntax, Gate: deps.RecallGate,
		NativeSchemaChars: schemaChars,
	})
}

func sessionContexts(deps SessionDeps, settings config.AppConfig) (*ProviderContext, context.Context) {
	providerContext := cmp.Or(deps.ProviderContext, NewProviderContext(int64(settings.Provider.ContextWindowTokens), settings.Provider.Model))
	return providerContext, cmp.Or(deps.BackgroundContext, context.Background())
}

func validateSessionDeps(deps SessionDeps) error {
	switch {
	case deps.Logger == nil:
		return fmt.Errorf("agent: logger dependency is nil")
	case deps.Provider == nil:
		return fmt.Errorf("agent: provider dependency is nil")
	case deps.Events == nil:
		return fmt.Errorf("agent: events dependency is nil")
	case deps.Memory == nil:
		return fmt.Errorf("agent: memory dependency is nil")
	case deps.Skills == nil:
		return fmt.Errorf("agent: skills dependency is nil")
	case deps.Tools == nil:
		return fmt.Errorf("agent: tools dependency is nil")
	case deps.Syntax == nil:
		return fmt.Errorf("agent: syntax dependency is nil")
	default:
		return nil
	}
}

func (s *Session) Send(ctx context.Context, message string) (turn.AgentResponse, error) {
	return s.runner.Run(ctx, RunRequest{SessionID: s.sessionID, Message: message})
}

// SendImages is Send with images attached to the user message. It refuses when
// the server reports the model cannot read images.
func (s *Session) SendImages(ctx context.Context, message string, images []inference.ContentBlock) (turn.AgentResponse, error) {
	if !s.runner.providerContext.AcceptsImages() {
		return turn.AgentResponse{}, fmt.Errorf("model %s does not accept images", s.runner.providerContext.Model())
	}
	return s.runner.Run(ctx, RunRequest{SessionID: s.sessionID, Message: message, Images: images})
}

func (s *Session) ToolSpecs() []core.ToolSpec {
	if catalog, ok := s.tools.(interface{ AvailableSpecs() []core.ToolSpec }); ok {
		return catalog.AvailableSpecs()
	}
	return s.tools.PromptSpecs(s.sessionID)
}

// ListTools reads the same session state used by the model's tools manager.
func (s *Session) ListTools(ctx context.Context) ([]tool_search.Listing, error) {
	manager, ok := s.tools.(interface {
		ToolListings(context.Context, string) ([]tool_search.Listing, error)
	})
	if !ok {
		return nil, fmt.Errorf("tool management unavailable")
	}
	return manager.ToolListings(ctx, s.sessionID)
}

func (s *Session) SetToolEnabled(ctx context.Context, name string, enabled bool) error {
	manager, ok := s.tools.(interface {
		SetToolEnabled(context.Context, string, string, bool) error
	})
	if !ok {
		return fmt.Errorf("tool management unavailable")
	}
	return manager.SetToolEnabled(ctx, s.sessionID, name, enabled)
}

func (s *Session) ResumeTranscript(transcript []turn.TranscriptMessage) error {
	messages := []inference.Message{inference.SystemMessage("resumed transcript")}
	for _, item := range transcript {
		switch item.Role {
		case "user":
			messages = append(messages, inference.UserMessage(item.Text))
		case "assistant":
			messages = append(messages, inference.AssistantMessage(resumedAssistantText(item)))
		default:
			return fmt.Errorf("agent: unsupported transcript role %q", item.Role)
		}
	}
	s.runner.mu.Lock()
	defer s.runner.mu.Unlock()
	if len(s.runner.messages) != 0 {
		return fmt.Errorf("agent: cannot resume a session with active history")
	}
	if reminder := toolStateReminder(s.tools, s.sessionID); reminder != "" {
		messages = append(messages, inference.UserMessage(reminder))
	}
	s.runner.messages = messages
	return nil
}

// resumedAssistantText replays the persisted tool ledger alongside the assistant
// text so a resumed session sees what actually ran, not just the model's prose
// about it (ADR 0001).
func resumedAssistantText(item turn.TranscriptMessage) string {
	text := turn.AppendToolOutcomeGuard(item.Text, item.ToolCalls)
	text = turn.AppendHistoricalExecution(text, item.ExecutionEvidence)
	if item.ToolCallCount > 0 {
		text = fmt.Sprintf("%s\n\n[Tool calls this turn: %d]", text, item.ToolCallCount)
	}
	return text
}

// Usage returns a read-only copy of current session token accounting.
func (s *Session) Usage() turn.TokenUsage {
	s.runner.applyProviderContext()
	s.runner.mu.RLock()
	defer s.runner.mu.RUnlock()
	return s.runner.usage
}

func (s *Session) RequestCompaction() { s.runner.budget.RequestCompaction() }

func (s *Session) CompactionThreshold() int { return s.runner.budget.compactThreshold() }

// CompactionEstimate reports roughly how many prompt tokens /compact would
// reclaim: the span of the transcript compaction replaces with a summary,
// measured with the same calibrated estimator the budget uses to decide when to
// compact. It is an estimate because the summary's own cost is not known until
// the summarizer has written it, and it returns 0 when there is nothing old
// enough to summarize.
func (s *Session) CompactionEstimate() int64 {
	s.runner.mu.RLock()
	defer s.runner.mu.RUnlock()
	segment := summarySegment(s.runner.messages, s.runner.budget.plan(s.runner.messages, s.runner.contextState.tokenEstimateScale, false))
	if len(segment) == 0 {
		return 0
	}
	return calibratedMessageTokens(segment, s.runner.contextState.tokenEstimateScale)
}

func (s *Session) SetModel(name string) { s.runner.pendingModel.Store(&name) }

// SetSystemPrompt replaces the base system prompt from the next turn on
// (/profile). Concurrent-safe; the builder applies it at the next Build.
func (s *Session) SetSystemPrompt(prompt string) { s.prompts.SetBasePrompt(prompt) }

func (s *Session) Model() string {
	if sw, ok := s.runner.modelGateway.(modelSwitcherGateway); ok {
		return sw.Model()
	}
	return ""
}

// ListModels fetches available model IDs from the provider; nil,nil when unsupported.
func (s *Session) ListModels(ctx context.Context) ([]string, error) {
	lister, ok := s.runner.modelGateway.(listModelsProvider)
	if !ok {
		return nil, nil
	}
	return lister.ListModels(ctx)
}

func (s *Session) SessionID() string { return s.sessionID }

// SetReasoningEffort validates and applies a level from the next request on.
func (s *Session) SetReasoningEffort(level string) error {
	normalized, err := clients.NormalizeReasoningEffort(level)
	if err != nil {
		return err
	}
	if g, ok := s.runner.modelGateway.(*OpenAIModelGateway); ok {
		g.mu.Lock()
		g.reasoningEffort = normalized
		g.mu.Unlock()
	}
	return nil
}

// ReasoningEffort reports the configured level and whether it takes effect.
func (s *Session) ReasoningEffort() clients.ReasoningState {
	var state clients.ReasoningState
	kind, templateOK, thinkingOK := s.runner.providerContext.ReasoningSupport()
	state.Kind = kind
	if g, ok := s.runner.modelGateway.(*OpenAIModelGateway); ok {
		g.mu.RLock()
		state.Level = g.reasoningEffort
		g.mu.RUnlock()
	}
	// A template that only knows enable_thinking still honours "none".
	templateOK = templateOK || (thinkingOK && kind.DisablesThinking(state.Level))
	state.Supported = kind.Accepts(state.Level) && (kind != clients.ReasoningLlamaCpp || templateOK)
	return state
}

func toolStateReminder(caller core.ToolCaller, id string) string {
	if manager, ok := caller.(interface{ ToolStateReminder(string) string }); ok {
		return manager.ToolStateReminder(id)
	}
	return ""
}
