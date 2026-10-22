package core

import (
	"encoding/json"
	"testing"
)

type probeArgs struct {
	Name  string `json:"name"`
	Depth int    `json:"depth"`
}

type editArg struct {
	Path string `json:"path"`
}

// The field-level shape: an object whose declared fields are an array, a bool,
// an int and a string, so a value that arrives as a JSON string only clashes
// with the schema when the field wants something else.
type fieldArgs struct {
	Edits   []editArg `json:"edits"`
	Network bool      `json:"network"`
	Depth   int       `json:"depth"`
	Note    string    `json:"note"`
}

// One table row: the arguments a server sent, and what the decoded call must
// hold (an accepted row whose expectation is left at its default is checked
// against that default, so a wrong decode shows up as a failure).
type fieldCase struct {
	name     string
	args     string
	accepted bool
	edits    int
	network  bool
	depth    int
	note     string
}

// Some servers hand the whole argument object back as a JSON string, so the
// outer value is a string whose text is itself the arguments. The decoder takes
// that one layer off rather than refusing the call, and a payload that is wrong
// in substance (a bare string, a number, text that is not JSON at all) still
// fails with the plain decode error rather than answering with defaults.
func TestDecodeOptionalToolArgsUnwrapsStringifiedArguments(t *testing.T) {
	nested := &ToolCall{Name: "probe", Arguments: json.RawMessage(`"{\"name\":\"leaf\",\"depth\":2}"`)}
	got, err := DecodeOptionalToolArgs[probeArgs](nested)
	if err != nil || got.Name != "leaf" || got.Depth != 2 {
		t.Fatalf("stringified arguments = %v, %v", got, err)
	}

	plain := &ToolCall{Name: "probe", Arguments: json.RawMessage(`{"name":"leaf","depth":3}`)}
	if plainGot, plainErr := DecodeOptionalToolArgs[probeArgs](plain); plainErr != nil || plainGot.Name != "leaf" || plainGot.Depth != 3 {
		t.Fatalf("plain arguments decode = %v, %v", plainGot, plainErr)
	}

	// A bare string, a number and truncated text are not argument objects, so the
	// call stays refused.
	for _, raw := range []string{`"just text"`, `"42"`, `"{\"name\":1`} {
		if _, bad := DecodeOptionalToolArgs[probeArgs](&ToolCall{Name: "probe", Arguments: json.RawMessage(raw)}); bad == nil {
			t.Fatalf("non-object payload %q decoded without error", raw)
		}
	}
}

var fieldLevelCases = []fieldCase{
	// A value carrying the array the field declares.
	{name: "stringified array field", args: `{"edits":"[{\"path\":\"a/b\"},{\"path\":\"c\"}]"}`, accepted: true, edits: 2},
	{name: "stringified bool field", args: `{"network":"true"}`, accepted: true, network: true},
	{name: "stringified number field", args: `{"depth":"7"}`, accepted: true, depth: 7},
	{name: "stringified null field", args: `{"edits":"null"}`, accepted: true},
	{name: "several fields at once", args: `{"network":"true","depth":"4","edits":"[{\"path\":\"c\"}]"}`, accepted: true, edits: 1, network: true, depth: 4},
	// A genuine string field is decoded untouched: nothing clashed with the
	// schema, so its JSON-looking text stays the string the model meant.
	{name: "genuine string field holding JSON-looking text", args: `{"note":"{\"path\":\"a\"} [1,2] null"}`, accepted: true, note: `{"path":"a"} [1,2] null`},
	{name: "genuine string field quoting JSON", args: `{"network":"true","note":"\"[1,2]\""}`, accepted: true, network: true, note: `"[1,2]"`},
	// Text that is not a JSON value, and JSON cut short, stay errors: the
	// decoder does not invent an array or a bool out of prose or a fragment.
	{name: "garbage bool field", args: `{"network":"yes, please"}`, accepted: false},
	{name: "truncated array field", args: `{"edits":"[{\"path\":\"a\""}`, accepted: false},
	{name: "array field holding prose", args: `{"edits":"[\"a\", not json]"}`, accepted: false},
	// A string field carrying a bare JSON object in the same call as a
	// stringified field is not untangled: the rewrite cannot tell it was meant
	// as text, so the retry misses and the call stays refused.
	{name: "string field carrying JSON next to a stringified field", args: `{"network":"true","note":"{\"path\":\"a\"}"}`, accepted: false},
}

func TestDecodeOptionalToolArgsUnwrapsStringifiedFields(t *testing.T) {
	for _, c := range fieldLevelCases {
		got, err := DecodeOptionalToolArgs[fieldArgs](&ToolCall{Name: "edit", Arguments: json.RawMessage(c.args)})
		if accepted := err == nil; accepted != c.accepted {
			t.Fatalf("%s: expected %v, got %v (%v)", c.name, c.accepted, accepted, err)
		}
		if !c.accepted {
			continue
		}
		if len(got.Edits) != c.edits || got.Network != c.network || got.Depth != c.depth || got.Note != c.note {
			t.Fatalf("%s: decoded = %+v, expected edits=%d network=%v depth=%d note=%q",
				c.name, got, c.edits, c.network, c.depth, c.note)
		}
	}
}
