package core

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
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
		return args, unwrapStringifiedArgs(&args, call, err)
	}
	return args, nil
}

// unwrapStringifiedArgs retries a failed decode against the two shapes a server
// produces: the whole argument object carried as a JSON string, and one field
// carried as a JSON string holding the array, object, bool, number or null that
// field declares. Only the field the decode error names is unwrapped, so a
// genuine string field keeps its text; each pass fixes one field, bounded by
// the field count. Anything still failing reports the original error.
func unwrapStringifiedArgs[T any](args *T, call *ToolCall, err error) error {
	if inner := unwrapString(call.Arguments, "{"); inner != nil && json.Unmarshal(inner, args) == nil {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(call.Arguments, &fields) != nil {
		return err
	}
	latest := err
	for range fields {
		typeErr, typed := latest.(*json.UnmarshalTypeError)
		if !typed || typeErr.Value != "string" {
			return err
		}
		value := unwrapString(fields[typeErr.Field], "[{tfn-0123456789")
		if value == nil {
			return err
		}
		fields[typeErr.Field] = value
		loose, _ := json.Marshal(fields) // every value is valid JSON, so this cannot fail
		if latest = json.Unmarshal(loose, args); latest == nil {
			return nil
		}
	}
	return err
}

// unwrapString returns the JSON value a string carries when its text starts
// with one of starts and is valid JSON, or nil. Text that is itself a JSON
// string is left alone: those quotes are part of the value the model meant.
func unwrapString(raw json.RawMessage, starts string) json.RawMessage {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return nil
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || !strings.ContainsRune(starts, rune(trimmed[0])) || !json.Valid([]byte(trimmed)) {
		return nil
	}
	return json.RawMessage(trimmed)
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
