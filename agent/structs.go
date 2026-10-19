package agent

import (
	"context"
	"github.com/invokr-ai/invokr/capability/agentprompt"
	"github.com/invokr-ai/invokr/capability/memory"
	"github.com/invokr-ai/invokr/capability/skills"
	"github.com/invokr-ai/invokr/capability/syntax"
	"github.com/invokr-ai/invokr/clients"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/platform/turn"
	toolscore "github.com/invokr-ai/invokr/tools/core"
	"sync"
	"sync/atomic"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

type SessionDeps struct {
	Logger                     *zap.Logger
	Provider                   clients.LLMProvider
	Events                     events.Dispatcher
	Memory                     memory.Manager
	SessionID, ParentSessionID string
	Skills                     skills.Manager
	Tools                      toolscore.ToolCaller
	Syntax                     syntax.PromptSyntax
	ProviderContext            *ProviderContext
	BackgroundContext          context.Context
	RecallGate                 agentprompt.RecallGate // optional; nil disables the recall gate
}

type Session struct {
	sessionID string
	runner    *Runner
	tools     toolscore.ToolCaller
	prompts   *agentprompt.Builder
}

type Runner struct {
	mu                sync.RWMutex
	modelGateway      ModelGateway
	turnProcessor     *TurnProcessor
	events            events.Dispatcher
	settings          config.AgentConfig
	logger            *zap.Logger
	usage             turn.TokenUsage
	parentSessionID   string
	pendingModel      atomic.Pointer[string]
	messages          []inference.Message
	contextState      contextWindowState
	budget            *contextBudget
	providerContext   *ProviderContext
	provider          clients.LLMProvider
	backgroundContext context.Context
	spend             spendCap
	// toolCaps is the declared-capability index (core.CapabilityIndex), which
	// the turn budget uses to price a read-only turn. Nil in tests: an unknown
	// tool is never read-only, so every turn is full price.
	toolCaps map[string]toolscore.ToolCapabilities
}

type RunRequest struct {
	SessionID, Message string
	// Images ride on the user message as content blocks.
	Images []inference.ContentBlock
}

type ModelGateway interface {
	Begin(ctx context.Context, req BeginModelRequest) (ModelTurn, error)
	Continue(ctx context.Context, req ContinueModelRequest) (ModelTurn, error)
}

// Narrow capability interfaces onto the model gateway. They keep call sites
// depending only on the methods they use rather than the concrete gateway, and
// deliberately do not widen ModelGateway itself.
type modelSwitcherGateway interface {
	SetModel(string)
	Model() string
}

// servedModelGateway reports the model name the provider echoed on its last
// response, which can differ from the requested one (aliases, router pins).
type servedModelGateway interface {
	ServedModel() string
}

// retryCountingGateway reports per-run recovery retries by reason.
type retryCountingGateway interface {
	RetryCounts() map[string]int
}

type conversationSummarizer interface {
	SummarizeConversation(context.Context, []inference.Message) (string, error)
}

type listModelsProvider interface {
	ListModels(context.Context) ([]string, error)
}

var (
	_ modelSwitcherGateway   = (*OpenAIModelGateway)(nil)
	_ conversationSummarizer = (*OpenAIModelGateway)(nil)
	_ listModelsProvider     = (*OpenAIModelGateway)(nil)
)

type BeginModelRequest struct {
	SessionID, Message string
	Images             []inference.ContentBlock
	// History carries the prior conversation thread (system message at index 0),
	// if any, so a new user turn continues the same conversation rather than
	// starting fresh each call.
	History []inference.Message
}

type ContinueModelRequest struct {
	SessionID string
	Messages  []inference.Message
	// DisableTools forces a tool-free completion for this call regardless of the
	// gateway's static NativeToolRequestsDisabled setting (used for the graceful
	// best-effort answer when MaxTurns is exhausted).
	DisableTools bool
	// DisableThinking forces the completion to run without extended thinking.
	// The give-up salvage calls set it: a model that is already looping or
	// cut mid-output answers a "stop and summarize" ask far more reliably
	// with its reasoning budget out of the way.
	DisableThinking bool
	// CompletedCalls is the runner's outcome-ledger tool-call count so far
	// this run (ADR 0001's source of truth), not a transcript scan. The
	// tool-call parse give-up path reports it instead of counting surviving
	// RoleTool messages, which undercounts after compaction (#538).
	CompletedCalls int
}

type ModelTurn struct {
	Messages   []inference.Message
	Completion completionOutput
}

type OpenAIModelGateway struct {
	mu       sync.RWMutex
	provider clients.LLMProvider
	model    string
	// servedModel is the model name the provider echoed on its last response.
	servedModel     string
	settings        config.AgentConfig
	tools           toolscore.ToolCaller
	prompts         *agentprompt.Builder
	syntax          syntax.PromptSyntax
	logger          *zap.Logger
	codec           OpenAINativeToolCodec
	events          events.Dispatcher
	parentSessionID string
	toolDefsJSON    []byte
	// promptCacheExtra carries provider-specific explicit cache markers into
	// every completion body; nil (the default) sends nothing extra.
	promptCacheExtra map[string]any
	// reasoningEffort is sent in the active provider's effort field while
	// providerContext knows its scale; empty sends nothing.
	reasoningEffort string
	providerContext *ProviderContext
	// recordSummarizerSpend meters compaction/summarizer completions into the
	// session spend cap (issue #295); nil disables metering (tests).
	recordSummarizerSpend func(prompt, completion int64)
	// lastPromptSections is the previous turn's system-prompt attribution,
	// kept for the /context breakdown; nil before the first turn.
	lastPromptSections []agentprompt.PromptSection
	// lengthRetriesUsed counts the no-thinking length retries spent on the
	// current turn (reset by Begin and Continue). One per turn: a turn cut
	// twice in a row is written off rather than retried forever (#443).
	lengthRetriesUsed int
	// parseRecoveries counts the truncated-tool-call recoveries spent on the
	// current run (reset by Begin). Beyond the cap the run gives up as before.
	parseRecoveries int
	// lastParseGiveUp is the previous parse rejection the run gave up on, so a
	// rejection repeating byte for byte is recognised as the server refusing
	// the request rather than a fresh mistake by the model (#539).
	lastParseGiveUp string
	// reasoningOverflowRetries counts the no-thinking length retries spent on
	// the current run (reset by Begin), for the ledger.
	reasoningOverflowRetries int
}

type TurnProcessor struct {
	evidence                   events.ExecutionLedger
	toolCaps                   map[string]toolscore.ToolCapabilities
	events                     events.Dispatcher
	sessionID, parentSessionID string
	syntax                     syntax.PromptSyntax
	tools                      toolscore.ToolCaller
	logger                     *zap.Logger
	codec                      OpenAINativeToolCodec
	// gate bounds what one tool result may cost the context window; nil (in
	// tests) degrades to the legacy hard cap.
	gate *resultGate

	// Request-local denial history prevents identical retries from re-prompting.
	callMu      sync.Mutex
	deniedCalls map[string]struct{}
	// quotas caps how many times one run may call a tool (config.ToolQuotas);
	// quotaUsed counts the calls made against it (guarded by callMu).
	quotas, quotaUsed map[string]int
}

type OpenAINativeToolCodec struct{}

type turnResult struct {
	done          bool
	answer        string
	usage         turn.TokenUsage
	outcomes      []turn.ToolOutcome
	toolCallCount int
	// stopReason names the reason a turn ended the run other than on the
	// model's own answer, and is empty otherwise (ADR 0001).
	stopReason string
	// calls carries each tool call's identity so the runner can detect a model
	// stuck repeating the same call. Empty for parse-failed calls.
	calls []toolCallSignature
}

// toolCallSignature identifies a tool call by name and a hash of its arguments,
// for the runner's repetition guard.
type toolCallSignature struct {
	Name, Key string
	// Args is the raw argument text, kept for the near-duplicate guard: two
	// calls whose hashes differ can still be the same script with one
	// constant changed.
	Args string
	// Result hashes the tool result this call produced, so the repeat guard
	// only counts a call as repeated when it also returned the same output —
	// a re-run that reports something new is progress, not a loop.
	Result string
}

type completionOutput struct {
	content string
	// reasoning is the model's thinking for this turn, carried back on the
	// history message so later turns see what it already worked out, as pi
	// does (#577).
	reasoning                  string
	toolCalls                  []inference.ToolCall
	usage                      turn.TokenUsage
	finishReason               inference.FinishReason
	syntheticFailure, streamed bool
	// lengthExhausted marks a completion whose generation hit the output limit
	// without a tool call, even after the no-thinking retry: the turn is not
	// finished, it was cut (#443).
	lengthExhausted bool
	// lengthRetried marks a completion produced by (or after) this run's one
	// no-thinking length retry, so the runner can end the run when that retry
	// does not pay off.
	lengthRetried bool
	// parseRecovered marks a completion whose tool call was rejected as
	// unparseable and is being fed back to the model as a tool error instead
	// of ending the thread. parseDetail carries the server's reason.
	parseRecovered bool
	parseDetail    string
}
