package events

import (
	"context"
	"time"

	"github.com/invokr-ai/invokr/capability/memory/core"
	"github.com/invokr-ai/invokr/platform/turn"
)

type EventType string

const (
	EventBeforePrompt            EventType = "before_prompt"
	EventAfterPrompt             EventType = "after_prompt"
	EventAfterResponse           EventType = "after_response"
	EventToolStarted             EventType = "tool_started"
	EventToolCompleted           EventType = "tool_completed"
	EventToolFailed              EventType = "tool_failed"
	EventToolProgress            EventType = "tool_progress"
	EventToolboxRefreshRequested EventType = "toolbox_refresh_requested"
	EventAudit                   EventType = "audit"
	EventBeforeRecall            EventType = "before_recall"
	EventAfterRecall             EventType = "after_recall"
	EventBeforeSave              EventType = "before_save"
	EventAfterSave               EventType = "after_save"
	EventNotice                  EventType = "notice"
	EventAssistantDelta          EventType = "assistant_delta"
	// EventReasoning carries one turn's full reasoning text, when the model
	// returned any. Distinct from EventAssistantDelta (streamed answer chunks):
	// this fires once per turn with the whole thing, so a recorded run keeps
	// the reasoning a debug log's tail(...,200) truncated away (#577 bench).
	EventReasoning EventType = "reasoning"
	// EventSystemPrompt carries the rendered system prompt once per session
	// (and again whenever /profile replaces the base prompt). Observation
	// only: nothing in the request path reads it.
	EventSystemPrompt EventType = "system_prompt"
)

type Event struct {
	Type            EventType `json:"type"`
	Timestamp       time.Time `json:"timestamp"`
	SessionID       string    `json:"session_id,omitempty"`
	ParentSessionID string    `json:"parent_session_id,omitempty"`

	Prompt    *PromptPayload         `json:"prompt,omitempty"`
	Response  *ResponsePayload       `json:"response,omitempty"`
	Tool      *ToolPayload           `json:"tool,omitempty"`
	Audit     *AuditPayload          `json:"audit,omitempty"`
	Recall    *RecallPayload         `json:"recall,omitempty"`
	Save      *SavePayload           `json:"save,omitempty"`
	Notice    *NoticePayload         `json:"notice,omitempty"`
	Delta     *AssistantDeltaPayload `json:"delta,omitempty"`
	System    *SystemPromptPayload   `json:"system,omitempty"`
	Reasoning *ReasoningPayload      `json:"reasoning,omitempty"`
}

// SystemPromptPayload records the system message the model was actually given,
// so a recorded run can answer "was this tool advertised?" without a rerun
// (test-bench issue #20). The user message and recalled memory blocks are
// deliberately absent: they are per-turn and unbounded.
type SystemPromptPayload struct {
	// Text is the system message verbatim.
	Text string `json:"text"`
	// Chars is len(Text), so a reader can size the prompt without measuring.
	Chars int `json:"chars"`
	// Sections attributes Chars by origin, in render order.
	Sections []PromptSectionSize `json:"sections,omitempty"`
	// Tools names the tool specs offered to the model, in prompt order.
	Tools []string `json:"tools,omitempty"`
	// Skills names the skills advertised in the skills block, if any.
	Skills []string `json:"skills,omitempty"`
	// Source is where the base prompt came from: "default", "config",
	// "persona", "env" or "profile".
	Source string `json:"source,omitempty"`
	// NativeSchemaChars is the serialized size of the native tool definitions
	// the request carries alongside this prompt. They are not part of Text —
	// the provider appends them — so without this the recorded prompt cannot
	// account for the largest slice of a turn's context.
	NativeSchemaChars int `json:"native_schema_chars,omitempty"`
}

// PromptSectionSize names one system-prompt section and its rendered size.
type PromptSectionSize struct {
	Name  string `json:"name"`
	Chars int    `json:"chars"`
}

type AssistantDeltaPayload struct {
	Text string `json:"text"`
}

// ReasoningPayload carries one turn's reasoning text verbatim, exactly as
// echoed back to the provider on the next request's reasoning_content.
type ReasoningPayload struct {
	Text string `json:"text"`
	Turn int    `json:"turn,omitempty"`
}
type NoticePayload struct {
	Text string `json:"text"`
}

type PromptPayload struct {
	Request core.PromptRequest `json:"request"`
	Blocks  []core.PromptBlock `json:"blocks,omitempty"`
}

type ResponsePayload struct {
	ExecutionEvidence []turn.ExecutionEvidence `json:"execution_evidence"`
	Query             string                   `json:"query,omitempty"`
	Text              string                   `json:"text,omitempty"`
	ToolOutcomes      []turn.ToolOutcome       `json:"tool_outcomes,omitempty"`
	// ToolCallCount is the runner ledger's count of tool calls for the turn.
	ToolCallCount int `json:"tool_call_count,omitempty"`
}

type ToolPayload struct {
	ID          string        `json:"id,omitempty"`
	Name        string        `json:"name,omitempty"`
	Arguments   string        `json:"arguments,omitempty"`
	Turn        int           `json:"turn,omitempty"`
	Duration    time.Duration `json:"duration,omitempty"`
	OutputBytes int           `json:"output_bytes,omitempty"`
	Error       string        `json:"error,omitempty"`
	// Progress carries one incremental status line emitted by a still-running
	// tool (EventToolProgress); the final result is unaffected by it.
	Progress string `json:"progress,omitempty"`
	// Code flags a completed-but-failed result (e.g. "exit 1", "denied") without
	// making it a hard tool failure. Drives UI failure styling.
	Code string `json:"code,omitempty"`
	// Output is the recorded tool result as the tool produced it — the full
	// text, even when the model-facing copy was reduced by the oversize gate
	// (per-tool byte caps still bound it upstream). The TUI's per-tool
	// renderers show a tail of it collapsed and the whole of it expanded.
	Output string `json:"output,omitempty"`
}

type AuditPayload struct {
	Action  string         `json:"action,omitempty"`
	Tool    string         `json:"tool,omitempty"`
	Paths   []string       `json:"paths,omitempty"`
	DryRun  bool           `json:"dry_run,omitempty"`
	Hunks   int            `json:"hunks,omitempty"`
	Outcome string         `json:"outcome,omitempty"`
	Error   string         `json:"error,omitempty"`
	Details map[string]any `json:"details,omitempty"`
}

type RecallPayload struct {
	Request core.Query `json:"request"`
	Hits    []core.Hit `json:"hits,omitempty"`
}

type SavePayload struct {
	Record core.Record  `json:"record"`
	Saved  *core.Record `json:"saved,omitempty"`
}

type Handler interface {
	HandleEvent(ctx context.Context, event Event) error
}

type HandlerFunc func(ctx context.Context, event Event) error

func (f HandlerFunc) HandleEvent(ctx context.Context, event Event) error { return f(ctx, event) }

type Dispatcher interface {
	Dispatch(ctx context.Context, event Event)
}

type Bus interface {
	Dispatcher
	Register(handler Handler) func()
}

// StreamRetryNotice is the notice the gateway sends when a reply stream was
// cut short and the turn is re-issued; the TUI drops the partial reply on it.
const StreamRetryNotice = "stream cut short; re-issuing the turn"

// Notice dispatches a notice event carrying text, stamped with the current
// time and the given session identifiers. It is a no-op when d is nil.
func Notice(ctx context.Context, d Dispatcher, sessionID, parentSessionID, text string) {
	emit(ctx, d, Event{Type: EventNotice, SessionID: sessionID, ParentSessionID: parentSessionID, Notice: &NoticePayload{Text: text}})
}

// emit stamps event with the current time and dispatches it; a nil dispatcher
// is a no-op.
func emit(ctx context.Context, d Dispatcher, event Event) {
	if d == nil {
		return
	}
	event.Timestamp = time.Now()
	d.Dispatch(ctx, event)
}

// Delta dispatches an assistant streaming delta carrying text, stamped with
// the current time and the given session identifiers. It is a no-op when d is
// nil or text is empty.
func Delta(ctx context.Context, d Dispatcher, sessionID, parentSessionID, text string) {
	if text == "" {
		return
	}
	emit(ctx, d, Event{Type: EventAssistantDelta, SessionID: sessionID, ParentSessionID: parentSessionID, Delta: &AssistantDeltaPayload{Text: text}})
}

// Audit emits an audit event for sessionID, folding err into the payload's
// Error field. It is a no-op when d or payload is nil.
func Audit(ctx context.Context, d Dispatcher, sessionID string, payload *AuditPayload, err error) {
	if d == nil || payload == nil {
		return
	}
	if err != nil {
		payload.Error = err.Error()
	}
	d.Dispatch(ctx, Event{Type: EventAudit, SessionID: sessionID, Audit: payload})
}
