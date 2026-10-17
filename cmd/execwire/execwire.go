// Package execwire defines the NDJSON envelope shared by the `invokr exec
// --json` producer (cmd/exec_cli.go) and its e2e consumers. Keeping the field
// keys and event-type values in one place means a producer rename fails to
// compile instead of silently breaking the tests that decode the stream.
package execwire

// Envelope field keys.
const (
	KeyType  = "type"
	KeyItem  = "item"
	KeyTs    = "ts"
	KeyModel = "model"
	// KeyServedModel is the model the provider reported serving, present when
	// the provider echoes one.
	KeyServedModel = "served_model"
	// KeySystem carries the TypeSystemPrompt payload.
	KeySystem = "system"
	// KeyReasoning carries the TypeReasoning payload.
	KeyReasoning = "reasoning"
)

// Event-type values emitted on the `type` field.
const (
	TypeItemStarted     = "item.started"
	TypeItemCompleted   = "item.completed"
	TypeItemProgress    = "item.progress"
	TypeThreadStarted   = "thread.started"
	TypeThreadCompleted = "thread.completed"
	TypeTurnCompleted   = "turn.completed"
	TypeError           = "error"
	TypeNotice          = "notice"
	// TypeSystemPrompt records the system message the run was given, once per
	// segment. Consumers that do not know it can ignore it: it carries no
	// "item" and no thread outcome.
	TypeSystemPrompt = "system_prompt"
	// TypeReasoning records one turn's full reasoning text. A consumer that
	// does not know it can ignore it, same as TypeSystemPrompt.
	TypeReasoning = "reasoning"
)
