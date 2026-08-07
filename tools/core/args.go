package core

import (
	"encoding/json"
	"fmt"
	"regexp"
)

func DecodeOptionalToolArgs[T any](call *ToolCall) (T, error) {
	var args T
	if call == nil {
		return args, fmt.Errorf("tool call is required")
	}
	if len(call.Arguments) == 0 {
		return args, nil
	}
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		return args, err
	}
	return args, nil
}

// templateMarkup matches the chat-template and tool-call delimiters a model
// must never put inside an argument value. When a template is mis-rendered the
// delimiter that should have closed the call is decoded as part of the last
// argument instead, so the tool receives a value with the rest of the
// (intended) call glued onto it.
var templateMarkup = regexp.MustCompile(`</?(tool_call|function|parameter)[>=]|<\|im_(start|end)\|>`)

// pathArgNames are the argument names whose value is a filesystem path. The
// check is limited to these: a path can never legitimately contain a template
// delimiter, whereas `command` or an edit's `new` routinely can (grepping for
// "</tool_call>", writing a test fixture), and refusing those would break
// working calls.
var pathArgNames = []string{"cwd", "dir", "directory", "path", "file", "file_path", "filename", "target_file", "old_path", "new_path", "source", "destination"}

// RejectTemplateMarkupArgs reports a path argument that carries chat-template
// markup, so the call is refused with an explanation instead of being run
// against a nonsense path. The markup is never stripped: what survived the
// leak is a truncated fragment of what the model meant, and silently guessing
// at the intended path would run the wrong call.
func RejectTemplateMarkupArgs(call *ToolCall) error {
	args := map[string]any{}
	if call == nil || len(call.Arguments) == 0 || json.Unmarshal(call.Arguments, &args) != nil {
		return nil
	}
	for _, name := range pathArgNames {
		value, _ := args[name].(string)
		if marker := templateMarkup.FindString(value); marker != "" {
			return &ToolError{Code: "malformed_call", Err: fmt.Errorf("argument %q contains tool-call markup (%s), so this call was cut short by a formatting error and was not run; resend it as one well-formed call with %q holding only the path", name, marker, name)}
		}
	}
	return nil
}
