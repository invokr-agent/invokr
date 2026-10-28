package tool_search

import (
	"github.com/invokr-agent/invokr/tools/core"
	"strings"
	"testing"
)

func TestRoutingFeedbackDoesNotExecuteOrGrantAccess(t *testing.T) {
	for _, test := range []struct{ args, want string }{
		{`{"action":"run","name":"shell","arguments":{"command":"pwd"}}`, `"name":"shell","arguments":{...}`},
		{`{"action":"run","name":"create_tool","arguments":{"name":"demo"}}`, `"action":"create","definition":{...}`},
	} {
		target, err := Resolve(&core.ToolCall{Name: "tools", Arguments: []byte(test.args)}, func(string) bool { return true }, defaultProtected)
		if target != nil || err == nil || !strings.Contains(err.Error(), test.want) {
			t.Fatalf("target=%v error=%v", target, err)
		}
	}
	target, err := Resolve(&core.ToolCall{Name: "tools", Arguments: []byte(`{"action":"create","definition":{"name":"blocked"}}`)}, func(string) bool { return false }, defaultProtected)
	if target != nil || err == nil || !strings.Contains(err.Error(), "operator allowlist") {
		t.Fatalf("allowlist bypass: %v %v", target, err)
	}
}
