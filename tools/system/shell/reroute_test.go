package shell

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/tools/core"
)

func TestRerouteMapsPlainLinesToNativeTools(t *testing.T) {
	tests := []struct {
		args, tool, want string
	}{
		{`{"command":"cat a.go b.go"}`, "read", `{"paths":["a.go","b.go"]}`},
		{`{"command":"cat a.go","cwd":"pkg"}`, "read", `{"paths":["pkg/a.go"]}`},
		{`{"command":"nl /etc/hosts"}`, "read", `{"paths":["/etc/hosts"]}`},
		{`{"command":"head a.go"}`, "read", `{"limit":10,"paths":["a.go"]}`},
		{`{"command":"head -n 40 a.go"}`, "read", `{"limit":40,"paths":["a.go"]}`},
		{`{"command":"head -5 a.go"}`, "read", `{"limit":5,"paths":["a.go"]}`},
		{`{"command":"sed -n '20,30p' a.go"}`, "read", `{"limit":11,"offset":20,"paths":["a.go"]}`},
		{`{"command":"sed -n 7p a.go"}`, "read", `{"limit":1,"offset":7,"paths":["a.go"]}`},
		{`{"command":"grep -rn TODO ."}`, "file_search", `{"context_lines":0,"max_results":100,"mode":"literal","path_glob":"","query":"TODO"}`},
		{`{"command":"grep -rn 'func .*Call' agent"}`, "file_search", `{"context_lines":0,"max_results":100,"mode":"regex","path_glob":"agent/**","query":"func .*Call"}`},
		{`{"command":"grep -F 'a(b' x.go"}`, "file_search", `{"context_lines":0,"max_results":100,"mode":"literal","path_glob":"x.go","query":"a(b"}`},
		{`{"command":"git status"}`, "git_read", `{"op":"status"}`},
		{`{"command":"git diff --cached -- a.go"}`, "git_read", `{"op":"diff","paths":["a.go"],"staged":true}`},
		{`{"command":"git log --oneline -n 5 main"}`, "git_read", `{"op":"log","ref":"main","limit":5}`},
		{`{"command":"git show HEAD~1"}`, "git_read", `{"op":"show","ref":"HEAD~1"}`},
		{`{"command":"rm old.txt"}`, "edit", `{"delete":true,"path":"old.txt"}`},
		{`{"command":"echo '--- a ---' && cat a.go && echo '--- b ---' && cat b.go"}`, "read", `{"paths":["a.go","b.go"]}`},
		{`{"command":"cat a.go && nl b.go","cwd":"pkg"}`, "read", `{"paths":["pkg/a.go","pkg/b.go"]}`},
		{`{"command":"cat a.go; echo '=== b ==='; cat b.go"}`, "read", `{"paths":["a.go","b.go"]}`},
		{`{"command":"echo STATUS && git status"}`, "git_read", `{"op":"status"}`},
	}
	for _, tc := range tests {
		got := Reroute(&core.ToolCall{Name: "shell", Arguments: json.RawMessage(tc.args)}, func(string) bool { return true })
		if got.Name != tc.tool || string(got.Arguments) != tc.want {
			t.Errorf("%s: got %s %s, want %s %s", tc.args, got.Name, got.Arguments, tc.tool, tc.want)
		}
		if !strings.Contains(got.Note, "call "+tc.tool+" directly") {
			t.Errorf("%s: note %q", tc.args, got.Note)
		}
	}
}

func TestRerouteLeavesOtherLinesOnShell(t *testing.T) {
	for _, args := range []string{
		`{"command":"cat a.go | wc -l"}`,
		`{"command":"cat a.go > b.go"}`,
		`{"command":"cat $F"}`,
		`{"command":"cat *.go"}`,
		`{"command":"cat -n a.go"}`,
		`{"command":"cat"}`,
		`{"command":"cat a.go; ls"}`,
		`{"command":"cat a.go 2>/dev/null"}`,
		`{"command":"cat","args":["a.go"]}`,
		`{"command":"cat a.go","background":true}`,
		`{"command":"head -c 10 a.go"}`,
		`{"command":"head -n 5 a.go b.go"}`,
		`{"command":"tail -n 5 a.go"}`,
		`{"command":"sed -n '/x/p' a.go"}`,
		`{"command":"sed -n '9,3p' a.go"}`,
		`{"command":"grep -v x a.go"}`,
		`{"command":"grep -i x a.go"}`,
		`{"command":"grep x"}`,
		`{"command":"grep 'a\\|b' a.go"}`,
		`{"command":"grep -r x ../other"}`,
		`{"command":"git log"}`,
		`{"command":"git status -s"}`,
		`{"command":"git -C x status"}`,
		`{"command":"git commit -m x"}`,
		`{"command":"rm -rf build"}`,
		`{"command":"rm a b"}`,
		`{"command":"go test ./..."}`,
		`{"command":"cat a.go && ls"}`,
		`{"command":"git status; git log --oneline"}`,
		`{"command":"cat a.go || cat b.go"}`,
		`{"command":"cat a.go && head -n 5 b.go"}`,
		`{"command":"cat a.go && git status"}`,
		`{"command":"echo a && echo b"}`,
		`{"command":"echo $X && cat a.go"}`,
		`{"command":"cat a.go && cat b.go > c.go"}`,
	} {
		call := &core.ToolCall{Name: "shell", Arguments: json.RawMessage(args)}
		if got := Reroute(call, func(string) bool { return true }); got != call {
			t.Errorf("%s: rerouted to %s %s", args, got.Name, got.Arguments)
		}
	}
}

func TestRerouteNeedsAvailableTarget(t *testing.T) {
	call := &core.ToolCall{Name: "shell", Arguments: json.RawMessage(`{"command":"cat a.go"}`)}
	if got := Reroute(call, func(name string) bool { return name != "read" }); got != call {
		t.Fatalf("rerouted to unavailable %s", got.Name)
	}
}
