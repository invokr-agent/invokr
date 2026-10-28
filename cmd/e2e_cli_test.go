package cmd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/charmbracelet/x/ansi"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/support/testutil/providertest"
)

const (
	invokrE2ETimeout      = 30 * time.Second
	invokrE2EBuildTimeout = 2 * time.Minute
)

var e2eBuild struct {
	once sync.Once
	dir  string
	path string
	err  error
	out  string
}

type e2eState struct {
	toolbox   string
	memoryDir string
	tokfHome  string
	workDir   string
}

type chatMessage = providertest.ChatMessage

func TestMain(m *testing.M) {
	code := m.Run()
	if e2eBuild.dir != "" {
		_ = os.RemoveAll(e2eBuild.dir)
	}
	os.Exit(code)
}

func TestCLI_E2E_MockedLLM_ExecutesToolWithoutWritingMarkdownMemory(t *testing.T) {
	state := newE2EState(t)
	writeFixtureProbeToolbox(t, state.toolbox, "alpha")
	provider := providertest.NewScripted(t,
		providertest.ToolCallStep("tools", `{"action":"enable","names":["fixture_probe"]}`),
		providertest.ToolCallStep("tools", `{"action":"run","name":"fixture_probe","arguments":{"path":"input.txt"}}`),
		providertest.ReplyStep("fixture contained: alpha"),
	)
	defer provider.Close()

	output, err := runInvokrAgent(t, provider.URL(), state, "please inspect the fixture\nexit\n", "--yolo")
	if err != nil {
		t.Fatalf("invokr agent failed: %v\noutput:\n%s", err, output)
	}

	assertContains(t, output, "agent started")
	assertContains(t, output, "fixture_probe")
	assertContains(t, output, "running...") // tool started block rendered
	assertContains(t, output, "input.txt")  // primary-arg preview rendered
	assertContains(t, output, "fixture contained: alpha")
	assertNotContains(t, output, "<|tool_call>")
	assertNotContains(t, output, "panic:")

	snapshot := provider.Snapshot()
	provider.AssertClean(t)
	if got := len(snapshot.CompletionRequests); got != 3 {
		t.Fatalf("completion calls = %d, want 3", got)
	}

	firstSystem := messageContent(snapshot.CompletionRequests[0].Messages[0])
	// The seeded PERSONA.md mirrors the constant, so the base prompt is the
	// same text whether or not setup ran.
	assertContains(t, firstSystem, constants.DefaultAgentSystemPrompt)
	// Optional tools are named with a one-line purpose; their schemas stay out.
	assertContains(t, firstSystem, "Optional tools:\n")
	assertContains(t, firstSystem, "- fixture_probe: ")
	assertContains(t, messageContent(snapshot.CompletionRequests[0].Messages[1]), "please inspect the fixture")

	firstCompletion := snapshot.CompletionRequests[0]
	if !firstCompletion.Stream {
		t.Fatal("first completion request stream = false, want true")
	}
	if firstCompletion.ToolChoice != "auto" {
		t.Fatalf("first completion request tool_choice = %#v, want auto", firstCompletion.ToolChoice)
	}
	if !firstCompletion.ParallelToolCalls {
		t.Fatal("first completion request parallel_tool_calls = false, want true")
	}
	if completionToolsContain(firstCompletion.Tools, "fixture_probe") {
		t.Fatalf("first completion tools include deferred fixture_probe before schema load: %#v", firstCompletion.Tools)
	}

	secondApply := snapshot.CompletionRequests[2]
	if !messagesContain(secondApply.Messages, `"type":"tool_result"`) {
		t.Fatalf("second apply-template request did not include a tool_result: %#v", secondApply.Messages)
	}
	if !messagesContain(secondApply.Messages, `"result":"alpha`) {
		t.Fatalf("second apply-template request did not include fixture output alpha: %#v", secondApply.Messages)
	}

	// Markdown project memory only changes through the explicit memory tool;
	// a one-off tool run must never materialise project memory rows.
	if rows := markdownMemoryRows(t, state.memoryDir, "project"); len(rows) != 0 {
		t.Fatalf("expected one-off tool run not to create project memory rows, got %v", rows)
	}
}

func TestCLI_E2E_MockedLLM_AcceptsSSECompletionChunks(t *testing.T) {
	state := newE2EState(t)
	writeEmptyToolbox(t, state.toolbox)
	provider := providertest.NewScripted(t, providertest.SSEReplyStep("streamed reply works"))
	defer provider.Close()

	output, err := runInvokrAgent(t, provider.URL(), state, "stream please\nexit\n")
	if err != nil {
		t.Fatalf("invokr agent failed: %v\noutput:\n%s", err, output)
	}
	assertContains(t, output, "streamed reply works")
	if got := strings.Count(output, "streamed reply works"); got != 1 {
		t.Fatalf("streamed reply count = %d, want 1; output:\n%s", got, output)
	}

	snapshot := provider.Snapshot()
	provider.AssertClean(t)
	if got := len(snapshot.CompletionRequests); got != 1 {
		t.Fatalf("completion calls = %d, want 1", got)
	}
	if !snapshot.CompletionRequests[0].Stream {
		t.Fatal("completion request stream = false, want true")
	}
}

func TestCLI_E2E_MockedLLM_RecoversFromRepeatedToolLoop(t *testing.T) {
	state := newE2EState(t)
	writeEmptyToolbox(t, state.toolbox)
	provider := providertest.NewScripted(t,
		providertest.ToolCallStep("tools", `{"action":"search","query":"missing-fixture"}`),
		providertest.ToolCallStep("tools", `{"action":"search","query":"missing-fixture"}`),
		providertest.ToolCallStep("tools", `{"action":"search","query":"missing-fixture"}`),
		providertest.ReplyStep("loop recovered"),
		providertest.ReplyStep("follow-up clean"),
	)
	defer provider.Close()

	output, err := runInvokrAgent(t, provider.URL(), state, "repeat a tool until redirected\nfollow up\nexit\n")
	if err != nil {
		t.Fatalf("invokr agent failed: %v\noutput:\n%s", err, output)
	}
	assertContains(t, output, "repeated tool call detected; asking model to change approach")
	assertContains(t, output, "loop recovered")
	assertContains(t, output, "follow-up clean")
	assertNotContains(t, output, "[Continuing your task above]")

	snapshot := provider.Snapshot()
	provider.AssertClean(t)
	provider.AssertExhausted(t)
	if got := len(snapshot.CompletionRequests); got != 5 {
		t.Fatalf("completion calls = %d, want 5", got)
	}
	if !messagesContain(snapshot.CompletionRequests[3].Messages, "[Continuing your task above]") {
		t.Fatal("recovery request missing internal repeated-tool nudge")
	}
	if messagesContain(snapshot.CompletionRequests[4].Messages, "[Continuing your task above]") {
		t.Fatal("internal repeated-tool nudge leaked into follow-up history")
	}
}

func TestCLI_E2E_MockedLLM_PersistsMemoryToolAndLoadsPrompt(t *testing.T) {
	state := newE2EState(t)
	writeEmptyToolbox(t, state.toolbox)
	firstProvider := providertest.NewScripted(t,
		providertest.ToolCallStep("tools", `{"action":"enable","names":["memory"]}`),
		providertest.ToolCallStep("tools", `{"action":"run","name":"memory","arguments":{"action":"save","scope":"project","kind":"fact","source":"markdown","content":"The fixture keyword is alpha."}}`),
		providertest.ReplyStep("remembered"),
	)
	defer firstProvider.Close()

	output, err := runInvokrAgent(t, firstProvider.URL(), state, "remember the fixture keyword\nexit\n", "--yolo")
	if err != nil {
		t.Fatalf("first invokr agent run failed: %v\noutput:\n%s", err, output)
	}
	assertContains(t, output, "remembered")
	firstProvider.AssertClean(t)

	projectMemories := markdownMemoryRows(t, state.memoryDir, "project")
	assertAnyContains(t, projectMemories, "The fixture keyword is alpha.")

	secondProvider := providertest.NewScripted(t, providertest.ReplyStep("memory was loaded"))
	defer secondProvider.Close()
	output, err = runInvokrAgent(t, secondProvider.URL(), state, "what is the fixture keyword?\nexit\n")
	if err != nil {
		t.Fatalf("second invokr agent run failed: %v\noutput:\n%s", err, output)
	}
	assertContains(t, output, "memory was loaded")

	snapshot := secondProvider.Snapshot()
	secondProvider.AssertClean(t)
	if got := len(snapshot.CompletionRequests); got != 1 {
		t.Fatalf("second run apply-template calls = %d, want 1", got)
	}
	// Recalled memory rides the user message, not the system prompt, so the
	// cacheable prefix stays byte-stable across turns (issue #359).
	messages := snapshot.CompletionRequests[0].Messages
	userPrompt := messageContent(messages[len(messages)-1])
	assertContains(t, userPrompt, "Memory:")
	assertContains(t, userPrompt, "MEMORY [")
	assertContains(t, userPrompt, "The fixture keyword is alpha.")
}

func TestCLI_E2E_MockedLLM_HelpAndExitDoNotCallProvider(t *testing.T) {
	state := newE2EState(t)
	writeEmptyToolbox(t, state.toolbox)
	provider := providertest.NewScripted(t)
	defer provider.Close()

	output, err := runInvokrAgent(t, provider.URL(), state, "/help\nexit\n")
	if err != nil {
		t.Fatalf("invokr agent failed: %v\noutput:\n%s", err, output)
	}
	assertContains(t, output, "agent started")
	assertContains(t, output, "/help")

	snapshot := provider.Snapshot()
	provider.AssertClean(t)
	if got := len(snapshot.CompletionRequests); got != 0 {
		t.Fatalf("apply-template calls = %d, want 0", got)
	}
}

func TestCLI_E2E_MockedLLM_SlashCommandsStayLocal(t *testing.T) {
	state := newE2EState(t)
	writeEmptyToolbox(t, state.toolbox)
	provider := providertest.NewScripted(t, providertest.ReplyStep("normal prompt reached provider"))
	defer provider.Close()

	output, err := runInvokrAgent(t, provider.URL(), state, "/help\n/tools\n/wat\nnormal request\nexit\n")
	if err != nil {
		t.Fatalf("invokr agent failed: %v\noutput:\n%s", err, output)
	}
	for _, want := range []string{"/jobs [run <name>]", "NATIVE TOOLS", "tools", "unknown command, try /help", "normal prompt reached provider"} {
		assertContains(t, output, want)
	}

	snapshot := provider.Snapshot()
	provider.AssertClean(t)
	provider.AssertExhausted(t)
	if got := len(snapshot.CompletionRequests); got != 1 {
		t.Fatalf("completion calls = %d, want 1", got)
	}
	messages := snapshot.CompletionRequests[0].Messages
	if got := messageContent(messages[len(messages)-1]); got != "normal request" {
		t.Fatalf("provider user message = %q, want normal request", got)
	}
}

func TestCLI_E2E_MockedLLM_ProviderErrorExitsNonZero(t *testing.T) {
	state := newE2EState(t)
	writeEmptyToolbox(t, state.toolbox)
	failure := providertest.ErrorStep(http.StatusInternalServerError, `{"error":{"type":"server_error","message":"boom"}}`)
	provider := providertest.NewScripted(t, failure, failure, failure)
	defer provider.Close()

	output, err := runInvokrAgent(t, provider.URL(), state, "trigger provider failure\nexit\n")
	if err == nil {
		t.Fatalf("invokr agent succeeded, want failure\noutput:\n%s", output)
	}
	assertContains(t, output, "server_error: boom")
	provider.AssertClean(t)
}

func newE2EState(t *testing.T) e2eState {
	t.Helper()

	root := t.TempDir()
	state := e2eState{
		toolbox:   filepath.Join(root, "toolbox"),
		memoryDir: filepath.Join(root, "memory", "markdown"),
		tokfHome:  filepath.Join(root, "tokf"),
		workDir:   filepath.Join(root, "work"),
	}
	for _, dir := range []string{state.toolbox, state.memoryDir, state.tokfHome, state.workDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create e2e dir %s: %v", dir, err)
		}
	}
	return state
}

func runInvokrAgent(t *testing.T, baseURL string, state e2eState, stdin string, flags ...string) (string, error) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), invokrE2ETimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, invokrBinary(t), append([]string{"--base-url=" + baseURL, "--model=test-model", "agent"}, flags...)...)
	cmd.Dir = state.workDir
	cmd.Env = mergedEnv(os.Environ(), map[string]string{
		// Isolate from the developer's real ~/.invokr (config.json there can
		// override the persona/system prompt these tests assert on).
		"INVOKR_HOME":        filepath.Join(state.workDir, "..", "home"),
		"INVOKR_TOOLBOX_DIR": state.toolbox,
		"INVOKR_MEMORY_DIR":  state.memoryDir,
		// Pin the flat legacy layout so markdownMemoryRows can read
		// MEMORY.md without recomputing the per-workspace digest path.
		"INVOKR_MEMORY_PROJECT_DIR": state.memoryDir,
		"INVOKR_TOKF_DISABLED":      "true",
		// Session titling makes one extra model call at shutdown; these tests
		// assert exact scripted completion counts, so keep it off.
		"INVOKR_MEMORY_DISABLE_SESSION_TITLES": "true",
		"INVOKR_TOKF_HOME":                     state.tokfHome,
		"NO_COLOR":                             "1",
		"TERM":                                 "dumb",
	})
	cmd.Stdin = strings.NewReader(stdin)

	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output

	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("invokr agent timed out after %s\noutput:\n%s", invokrE2ETimeout, output.String())
	}
	return output.String(), err
}

func invokrBinary(t *testing.T) string {
	t.Helper()

	e2eBuild.once.Do(func() {
		dir, err := os.MkdirTemp("", "invokr-e2e-bin-*")
		if err != nil {
			e2eBuild.err = err
			return
		}
		e2eBuild.dir = dir
		e2eBuild.path = filepath.Join(dir, "github.com/invokr-agent/invokr")

		ctx, cancel := context.WithTimeout(context.Background(), invokrE2EBuildTimeout)
		defer cancel()

		cmd := exec.CommandContext(ctx, "go", "build", "-o", e2eBuild.path, ".")
		cmd.Dir = repoRoot()
		cmd.Env = os.Environ()

		var output bytes.Buffer
		cmd.Stdout = &output
		cmd.Stderr = &output
		if err := cmd.Run(); err != nil {
			e2eBuild.err = err
			e2eBuild.out = output.String()
			return
		}
		if ctx.Err() == context.DeadlineExceeded {
			e2eBuild.err = ctx.Err()
			e2eBuild.out = output.String()
		}
	})

	if e2eBuild.err != nil {
		t.Fatalf("build invokr binary: %v\noutput:\n%s", e2eBuild.err, e2eBuild.out)
	}
	return e2eBuild.path
}

func writeFixtureProbeToolbox(t *testing.T, toolbox string, fixtureContent string) {
	t.Helper()

	pluginDir := filepath.Join(toolbox, "fixture_probe")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatalf("create fixture_probe plugin dir: %v", err)
	}
	writeTestFile(t, filepath.Join(pluginDir, "tool.json"), `{
  "name": "fixture_probe",
  "description": "Read a named fixture file from the plugin directory.",
  "example": "{\"name\":\"fixture_probe\",\"arguments\":{\"path\":\"input.txt\"}}",
  "namespace": "toolbox",
  "policy": {"tiers":["read_workspace"],"approval":"auto"},
  "capabilities": {"reads_workspace":false,"writes_workspace":false,"network":false,"executes_code":true}
}`)
	writeTestFile(t, filepath.Join(pluginDir, "main.py"), `import json
from pathlib import Path
import sys

call = json.load(sys.stdin)
args = call.get("arguments") or {}
path = Path(args.get("path", "input.txt"))
text = path.read_text()
print(json.dumps({"result": text.strip()}), end="")
`)
	writeTestFile(t, filepath.Join(pluginDir, "input.txt"), fixtureContent+"\n")
}

func writeEmptyToolbox(t *testing.T, toolbox string) {
	t.Helper()
	if err := os.MkdirAll(toolbox, 0o755); err != nil {
		t.Fatalf("create empty toolbox %s: %v", toolbox, err)
	}
}

func writeTestFile(t *testing.T, path string, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write test file %s: %v", path, err)
	}
}

func markdownMemoryRows(t *testing.T, dir string, kind string) []string {
	t.Helper()

	// This replaced the old SQLite-backed memoryRows helper. Restore DB queries here
	// when SQLite becomes the source of truth for e2e memory assertions again.
	name := map[string]string{
		"project": "MEMORY.md",
		"user":    "USER.md",
	}[kind]
	if name == "" {
		t.Fatalf("unsupported markdown memory kind %q", kind)
	}
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read %s markdown memory: %v", kind, err)
	}

	var contents []string
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "- "))
		if line != "" {
			contents = append(contents, line)
		}
	}
	return contents
}

func repoRoot() string {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		panic("could not determine e2e test file path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), ".."))
}

func messageContent(msg chatMessage) string {
	switch value := msg.Content.(type) {
	case string:
		return value
	case nil:
		return ""
	default:
		b, err := json.Marshal(value)
		if err != nil {
			return fmt.Sprint(value)
		}
		return string(b)
	}
}

func messagesContain(messages []chatMessage, substring string) bool {
	for _, msg := range messages {
		if strings.Contains(messageContent(msg), substring) {
			return true
		}
	}
	return false
}

func completionToolsContain(tools any, name string) bool {
	items, ok := tools.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		tool, ok := item.(map[string]any)
		if !ok {
			continue
		}
		fn, ok := tool["function"].(map[string]any)
		if !ok {
			continue
		}
		if fn["name"] == name {
			return true
		}
	}
	return false
}

func mergedEnv(base []string, overrides map[string]string) []string {
	env := append([]string(nil), base...)
	for key, value := range overrides {
		prefix := key + "="
		replaced := false
		for i, item := range env {
			if strings.HasPrefix(item, prefix) {
				env[i] = prefix + value
				replaced = true
				break
			}
		}
		if !replaced {
			env = append(env, prefix+value)
		}
	}
	return env
}

func assertContains(t *testing.T, got string, want string) {
	t.Helper()
	// The TUI renderers style names and arguments separately, so compare on
	// the stripped text rather than on the escape-laden original.
	if !strings.Contains(ansi.Strip(got), want) {
		t.Fatalf("expected text to contain %q\ntext:\n%s", want, got)
	}
}

func assertNotContains(t *testing.T, got string, want string) {
	t.Helper()
	if strings.Contains(got, want) {
		t.Fatalf("expected text not to contain %q\ntext:\n%s", want, got)
	}
}

func assertAnyContains(t *testing.T, rows []string, want string) {
	t.Helper()
	for _, row := range rows {
		if strings.Contains(row, want) {
			return
		}
	}
	t.Fatalf("expected at least one row to contain %q\nrows:\n%s", want, strings.Join(rows, "\n---\n"))
}
