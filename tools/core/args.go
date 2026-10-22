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
// field declares. Only a type error naming a string as the offending value gets
// that field-level rewrite, so a call that decoded cleanly is never re-read and
// a string field holding JSON-looking text keeps its contents. A retry that
// still cannot satisfy the struct reports the original error, the one the
// caller can act on, rather than one the retry invented.
func unwrapStringifiedArgs[T any](args *T, call *ToolCall, err error) error {
	inner := stringifiedToolArgs(call.Arguments)
	if len(inner) > 0 && json.Unmarshal(inner, args) == nil {
		return nil
	}
	typeErr, typed := err.(*json.UnmarshalTypeError)
	if !typed || typeErr.Value != "string" {
		return err
	}
	loose, marshalErr := stringifiedFields(call.Arguments)
	if marshalErr != nil {
		return marshalErr
	}
	if len(loose) > 0 && json.Unmarshal(loose, args) == nil {
		return nil
	}
	return err
}

// stringifiedToolArgs returns the argument object carried inside a JSON string
// (a double-encoded call), or nothing when the bytes are not that shape. Only
// an object is unwrapped: a bare string or a number is a value the schema
// would reject anyway, and guessing at it would invent an argument.
func stringifiedToolArgs(raw json.RawMessage) []byte {
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return nil
	}
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") || !json.Valid([]byte(trimmed)) {
		return nil
	}
	return []byte(trimmed)
}

// stringifiedFields rewrites the top-level argument values a server wrapped in
// a JSON string, and leaves every other value alone: prose and text that quotes
// JSON keep their contents, so a genuine string field still holds the string the
// model meant. Nothing is returned when no value needed rewriting, which keeps
// the caller from retrying a payload it already rejected.
func stringifiedFields(raw json.RawMessage) ([]byte, error) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, nil
	}
	changed := false
	for key, value := range fields {
		inner := unwrappedValue(value)
		if inner == "" {
			continue
		}
		fields[key] = json.RawMessage(inner)
		changed = true
	}
	if !changed {
		return nil, nil
	}
	loose, err := json.Marshal(fields)
	if err != nil {
		return nil, err
	}
	return loose, nil
}

// unwrappedValue returns the JSON value a string argument carries, or nothing
// when the argument is not that shape. Only an array, object, bool, number or
// null is taken: text that is itself a JSON string is left alone, because the
// quotes are part of the value the model meant, not an encoding layer.
func unwrappedValue(value json.RawMessage) string {
	var text string
	if json.Unmarshal(value, &text) != nil {
		return ""
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return ""
	}
	switch trimmed[0] {
	case '[', '{', 't', 'f', 'n', '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		if json.Valid([]byte(trimmed)) {
			return trimmed
		}
	}
	return ""
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
