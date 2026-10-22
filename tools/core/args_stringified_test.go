package core

import (
	"encoding/json"
	"testing"
)

type probeArgs struct {
	Name  string `json:"name"`
	Depth int    `json:"depth"`
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
