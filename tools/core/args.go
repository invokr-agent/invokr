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
// genuine string field keeps its text even in a call that needs unwrapping
// somewhere else. The retry reports the next offending field, so several
// stringified fields untangle one per pass, and a field is never re-read: the
// pass count is bounded by the number of fields. A field whose text is not a
// JSON value, and a retry that still misses, report the original error rather
// than one the retry invented.
func unwrapStringifiedArgs[T any](args *T, call *ToolCall, err error) error {
	inner := stringifiedToolArgs(call.Arguments)
	if len(inner) > 0 && json.Unmarshal(inner, args) == nil {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(call.Arguments, &fields) != nil {
		return err
	}
	tried := map[string]bool{}
	first, latest := err, err
	for round := len(fields); round > 0; round = round - 1 {
		typeErr, typed := latest.(*json.UnmarshalTypeError)
		if !typed || typeErr.Value != "string" {
			break
		}
		if typeErr.Field == "" || tried[typeErr.Field] {
			break
		}
		tried[typeErr.Field] = true
		value := unwrappedValue(fields[typeErr.Field])
		if value == "" {
			break
		}
		fields[typeErr.Field] = json.RawMessage(value)
		latest = retryFields(args, fields)
		if latest == nil {
			return nil
		}
	}
	return first
}

// retryFields marshals the argument fields as given and decodes them into the
// tool's struct, returning the error that decode produced, marshalling included
// — a marshal failure is a real fault in the arguments, not something to
// report as the plain decode error the caller already saw.
func retryFields[T any](args *T, fields map[string]json.RawMessage) error {
	loose, marshalErr := json.Marshal(fields)
	if marshalErr != nil {
		return marshalErr
	}
	return json.Unmarshal(loose, args)
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
