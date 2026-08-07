//go:build e2e

package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

func TestD01_CreateToolLifecycle(t *testing.T) {
	create := func(name, source string, extra map[string]any) map[string]any {
		args := map[string]any{
			"name": name, "description": "D01 fixture", "example": `{"name":"` + name + `","arguments":{"text":"hi"}}`,
			"parameters":    map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
			"python_source": source, "policy": map[string]any{"tiers": []string{"executes_code"}, "approval": "ask"},
		}
		for key, value := range extra {
			args[key] = value
		}
		return args
	}
	count := "import json,sys\na=json.loads(sys.stdin.read())['arguments']\nprint(json.dumps({'result':str(len(a['text'].split()))}))"
	changed := "import json,sys\na=json.loads(sys.stdin.read())['arguments']\nprint(json.dumps({'result':str(len(a['text'].split())+10)}))"
	homeDir := ""
	steps := []providertest.CompletionStep{
		providertest.NativeToolCallStep("d01-create", "create_tool", create("word_count_test", count, nil)),
		providertest.NativeToolCallStep("d01-search", "tool_search", map[string]any{"query": "word_count_test", "include_schema": true}),
		providertest.NativeToolCallStep("d01-call", "word_count_test", map[string]any{"text": "one two three"}),
		providertest.NativeToolCallStep("d01-duplicate", "create_tool", create("word_count_test", count, nil)),
		providertest.NativeToolCallStep("d01-overwrite", "create_tool", create("word_count_test", changed, map[string]any{"overwrite": true})),
		providertest.NativeToolCallStep("d01-call-changed", "word_count_test", map[string]any{"text": "one two three"}),
		providertest.NativeToolCallStep("d01-no-req", "create_tool", create("word_count_test", changed, map[string]any{"overwrite": true})),
		providertest.NativeToolCallStep("d01-invalid-name", "create_tool", create("9bad", count, nil)),
		providertest.NativeToolCallStep("d01-collision", "create_tool", create("shell", count, nil)),
	}
	for i, requirement := range []string{
		"https://example.invalid/pkg.whl", "./local-package", "git+https://example.invalid/pkg.git", "requests", "requests==2.31.0",
	} {
		steps = append(steps, providertest.NativeToolCallStep("d01-req-"+string(rune('a'+i)), "create_tool",
			create("bad_requirement_"+string(rune('a'+i)), count, map[string]any{"requirements": []string{requirement}})))
	}
	steps = append(steps, providertest.ReplyStep("done"))
	steps[6].BeforeResponse = func() {
		if err := os.WriteFile(filepath.Join(homeDir, "word_count_test", "requirements.txt"), []byte("stale==1\n"), 0o600); err != nil {
			t.Errorf("seed stale requirements: %v", err)
		}
	}

	provider := providertest.NewScripted(t, steps...)
	defer provider.Close()
	home := newHome(t, provider.URL())
	homeDir = home.state.ToolboxDir
	home.app.Toolbox.PackageDeny = []string{"requests"}
	got := runInProc(t, home, provider.URL(), "exercise create_tool lifecycle", grantAll)

	if got.Resp.ToolCallCount != 14 {
		t.Fatalf("tool calls=%d, want 14", got.Resp.ToolCallCount)
	}
	results := toolResults(provider.Snapshot().CompletionRequests)
	if !strings.Contains(results, `"3"`) || !strings.Contains(results, `"13"`) {
		t.Fatalf("created tool did not refresh immediately: %s", results)
	}
	if len(got.Resp.ToolFailures) != 8 {
		t.Errorf("failures=%#v, want duplicate + 2 names + 5 requirements", got.Resp.ToolFailures)
	}
	toolDir := filepath.Join(home.state.ToolboxDir, "word_count_test")
	if _, err := os.Stat(filepath.Join(toolDir, "requirements.txt")); !os.IsNotExist(err) {
		t.Fatalf("stale requirements remained: %v", err)
	}
	for _, name := range []string{"9bad", "shell", "bad_requirement_a", "bad_requirement_b", "bad_requirement_c", "bad_requirement_d", "bad_requirement_e"} {
		if _, err := os.Stat(filepath.Join(home.state.ToolboxDir, name)); !os.IsNotExist(err) {
			t.Fatalf("rejected tool left partial files: %s: %v", name, err)
		}
	}
}
