package core

// ToolError lets a tool flag a result with a short, structured code (e.g.
// "exit 1", "not_found") while optionally still returning output to the model
// via Payload. Extracted with errors.As, mirroring approval.DeniedError.
//
// If Payload is non-empty the call is treated as completed-but-flagged: the
// model still receives Payload, and the UI renders the code as a failure
// marker. If Payload is empty it is a hard failure.
type ToolError struct {
	Code, Payload string
	Err           error
}

func (e *ToolError) Error() string {
	if e == nil {
		return "tool error"
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return e.Code
}

func (e *ToolError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
