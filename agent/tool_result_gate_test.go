package agent

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/capability/syntax"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/tools"
	"github.com/invokr-ai/invokr/tools/core"

	"go.uber.org/zap"
)

func testGate(t *testing.T, budget int) *resultGate {
	t.Helper()
	t.Setenv("INVOKR_HOME", t.TempDir())
	return newResultGate(config.AgentConfig{ToolResultMaxTokens: budget, ToolResultStrategy: "spill"}, "gate-test-session", zap.NewNop())
}

func TestGateUnderBudgetPassthrough(t *testing.T) {
	gate := testGate(t, 100)
	small := strings.Repeat("a", 100)
	got, ref := gate.cap("shell", nil, small)
	if got != small || ref != "" {
		t.Fatalf("under-budget result modified: len %d -> %d, ref %q", len(small), len(got), ref)
	}
}

func TestGatePlainTextSpills(t *testing.T) {
	gate := testGate(t, 100) // 400-byte budget
	var lines []string
	for i := 1; i <= 200; i++ {
		lines = append(lines, "line-"+strings.Repeat("x", 20))
	}
	full := strings.Join(lines, "\n")
	got, ref := gate.cap("some_tool", nil, full)
	if ref == "" {
		t.Fatalf("no spill ref returned")
	}
	data, err := os.ReadFile(ref)
	if err != nil {
		t.Fatalf("reading spill file: %v", err)
	}
	if string(data) != full {
		t.Fatalf("spill file does not hold the full output: %d vs %d bytes", len(data), len(full))
	}
	for _, want := range []string{"TOOL RESULT TRUNCATED", "NOT the complete output", ref, "read", "lines omitted"} {
		if !strings.Contains(got, want) {
			t.Fatalf("stub missing %q:\n%s", want, got)
		}
	}
	if len(got) >= len(full) {
		t.Fatalf("stub not smaller than the original: %d >= %d", len(got), len(full))
	}
}

func TestGateTailBiasedPreview(t *testing.T) {
	head, tail, omitted := headTailPreview(strings.Repeat("aaaa aaaa\n", 1000), 400)
	if len(tail) < 2*len(head) {
		t.Fatalf("tail not larger than head: head %d, tail %d", len(head), len(tail))
	}
	if omitted <= 0 {
		t.Fatalf("expected omitted lines, got %d", omitted)
	}
}

func TestGateJSONEnvelopeKeepsSmallFields(t *testing.T) {
	gate := testGate(t, 100)
	stdout := strings.Repeat("build error line\n", 200)
	raw, err := json.Marshal(map[string]any{"stdout": stdout, "exit_code": 1, "timed_out": false})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, ref := gate.cap("shell", nil, string(raw))
	if ref == "" {
		t.Fatalf("no spill ref for envelope result")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &fields); err != nil {
		t.Fatalf("gated envelope is not valid JSON: %v\n%s", err, got)
	}
	if string(fields["exit_code"]) != "1" {
		t.Fatalf("exit_code not preserved: %s", fields["exit_code"])
	}
	var stub string
	if err := json.Unmarshal(fields["stdout"], &stub); err != nil || !strings.Contains(stub, "TOOL RESULT TRUNCATED") {
		t.Fatalf("stdout not replaced by stub: %v %q", err, stub)
	}
	data, err := os.ReadFile(ref)
	if err != nil || string(data) != stdout {
		t.Fatalf("spill file does not hold the raw stdout: err %v, %d vs %d bytes", err, len(data), len(stdout))
	}
	if got == "" {
		t.Fatalf("gate emptied a non-empty payload")
	}
}

func TestGateShellRecaptureHint(t *testing.T) {
	gate := testGate(t, 100)
	raw, err := json.Marshal(map[string]any{"stdout": strings.Repeat("z", 1000), "truncated": true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got, _ := gate.cap("shell", nil, string(raw))
	if !strings.Contains(got, "max_output_bytes") {
		t.Fatalf("truncated=true envelope missing the recapture hint:\n%s", got)
	}
}

func TestGateNilFallsBackToHardCap(t *testing.T) {
	var gate *resultGate
	big := strings.Repeat("x", 70_000)
	got, ref := gate.cap("shell", nil, big)
	if ref != "" {
		t.Fatalf("nil gate produced a spill ref")
	}
	if !strings.Contains(got, "output truncated at") {
		t.Fatalf("nil gate did not hard-truncate")
	}
}

func TestGateSpillFailureFallsBackToHardCap(t *testing.T) {
	gate := testGate(t, 100)
	gate.spill = failingSpillStore{}
	big := strings.Repeat("y", 1000)
	got, ref := gate.cap("some_tool", nil, big)
	if ref != "" {
		t.Fatalf("failed spill still returned a ref")
	}
	if !strings.Contains(got, "output truncated at") {
		t.Fatalf("spill failure did not fall back to the hard cap:\n%s", got)
	}
}

type failingSpillStore struct{}

func (failingSpillStore) put(string, []byte) (string, error) {
	return "", os.ErrPermission
}

func TestGateUTF8Boundary(t *testing.T) {
	gate := testGate(t, 25) // 100-byte budget
	full := strings.Repeat("héllö wörld — ünïcödé\n", 200)
	got, _ := gate.cap("some_tool", nil, full)
	if !strings.HasPrefix(strings.TrimPrefix(got, "["), "TOOL RESULT TRUNCATED") {
		t.Fatalf("unexpected stub prefix: %q", got[:40])
	}
	for _, r := range got {
		if r == '\uFFFD' {
			t.Fatalf("stub contains a replacement rune — rune split on truncation")
		}
	}
}

func TestPruneSpillFiles(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "session-a")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	stale := filepath.Join(dir, "old.txt")
	fresh := filepath.Join(dir, "new.txt")
	for _, path := range []string{stale, fresh} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	old := time.Now().Add(-2 * spillRetention)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	pruneSpillFiles(root, time.Now())
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale spill file not pruned: %v", err)
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Fatalf("fresh spill file removed: %v", err)
	}
}

// gateTestCaller returns a fixed output (or error) for every call.
type gateTestCaller struct {
	output string
	err    error
}

func (c *gateTestCaller) PromptSpecs(string) []core.ToolSpec { return nil }
func (c *gateTestCaller) StableSpecs() []core.ToolSpec       { return nil }
func (c *gateTestCaller) Specs() []core.ToolSpec             { return nil }
func (c *gateTestCaller) HandleCall(context.Context, *core.ToolCall) (string, error) {
	return c.output, c.err
}
func (c *gateTestCaller) HasNative(string) bool              { return true }
func (c *gateTestCaller) NativeSource(string) string         { return "" }
func (c *gateTestCaller) MarkSchemasLoaded(string, []string) {}
func (c *gateTestCaller) ForgetSession(string)               {}
func (c *gateTestCaller) Approver() approval.Approver        { return nil }
func (c *gateTestCaller) ReplaceNativeToolsBySource(string, []core.DynamicNativeTool) []string {
	return nil
}
func (c *gateTestCaller) RegisterNative(core.ToolSpec, core.NativeHandler) {}

func gateTestProcessor(t *testing.T, caller core.ToolCaller, captured *[]events.Event) *TurnProcessor {
	t.Helper()
	bus := events.NewOrderedDispatcher(zap.NewNop(), events.HandlerFunc(func(_ context.Context, e events.Event) error {
		*captured = append(*captured, e)
		return nil
	}))
	return &TurnProcessor{
		events:    bus,
		sessionID: "gate-test-session",
		syntax:    syntax.NewTaggedJSONSyntax(),
		tools:     caller,
		logger:    zap.NewNop(),
		gate:      testGate(t, 100),
	}
}

func TestExecuteToolEventCarriesOriginalOutputModelGetsStub(t *testing.T) {
	full := strings.Repeat("diff line\n", 500)
	var captured []events.Event
	p := gateTestProcessor(t, &gateTestCaller{output: full}, &captured)
	got, _, outcome := p.executeTool(context.Background(), 1, "call-1", "native tool call", &core.ToolCall{Name: "shell"})
	if outcome != nil {
		t.Fatalf("unexpected outcome: %+v", outcome)
	}
	if !strings.Contains(got, "TOOL RESULT TRUNCATED") {
		t.Fatalf("model-facing output is not the stub:\n%.120s", got)
	}
	var completed *events.ToolPayload
	for _, e := range captured {
		if e.Type == events.EventToolCompleted {
			completed = e.Tool
		}
	}
	if completed == nil {
		t.Fatalf("no completed event dispatched")
	}
	if completed.Output != full || completed.OutputBytes != len(full) {
		t.Fatalf("completed event does not carry the original output: %d bytes vs %d", completed.OutputBytes, len(full))
	}
	ref := filepath.Join(p.gate.spill.(*fileSpillStore).dir, "shell-1.txt")
	data, err := os.ReadFile(ref)
	if err != nil || string(data) != full || !strings.Contains(got, ref) {
		t.Fatalf("model preview must name the complete spilled output: %v", err)
	}
}

func TestExecuteToolFlaggedPayloadGated(t *testing.T) {
	payload := `{"stdout":"` + strings.Repeat("e", 1000) + `","exit_code":1}`
	var captured []events.Event
	p := gateTestProcessor(t, &gateTestCaller{err: &core.ToolError{Code: "exit 1", Payload: payload}}, &captured)
	got, _, outcome := p.executeTool(context.Background(), 1, "call-2", "native tool call", &core.ToolCall{Name: "shell"})
	if outcome != nil {
		t.Fatalf("flagged payload became a hard failure: %+v", outcome)
	}
	if got == "" {
		t.Fatalf("gate emptied a flagged payload")
	}
	if !strings.Contains(got, "TOOL RESULT TRUNCATED") {
		t.Fatalf("flagged payload not gated:\n%.120s", got)
	}
	for _, e := range captured {
		if e.Type == events.EventToolCompleted && e.Tool.Output != payload {
			t.Fatalf("completed event does not carry the original payload")
		}
	}
}

func TestGateCapStrategyNoSpill(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	gate := newResultGate(config.AgentConfig{
		ToolResultMaxTokens:  100,
		ToolResultStrategy:   "spill",
		ToolResultStrategies: map[string]string{"read": "cap"},
	}, "gate-test-session", zap.NewNop())
	// Over the gate budget but under the cap (2x the budget = 800 bytes):
	// passes through untouched, with no spill file.
	page := strings.Repeat("line\n", 150)
	got, ref := gate.cap("read", nil, page)
	if ref != "" {
		t.Fatalf("cap strategy produced a spill ref")
	}
	if got != page {
		t.Fatalf("cap strategy modified an under-cap page: %d -> %d bytes", len(page), len(got))
	}
}

// The repeat guard hashes the RAW tool output, before the gate spills it: a
// spill stub embeds a fresh sequence number in its file name, so an oversized
// but otherwise identical result must still trip the guard after
// maxIdenticalToolCallRepeats identical calls.
func TestRepeatGuardFiresOnIdenticalOversizedResult(t *testing.T) {
	same := strings.Repeat("build failed\n", 200)
	caller := &tools.Caller{}
	caller.RegisterNative(core.ToolSpec{Name: "demo", Metadata: core.ToolMetadata{AlwaysVisible: true}},
		func(context.Context, *core.ToolCall) (string, error) { return same, nil })
	runner := testRunner(t, nil, []string{
		`<|tool_call>{"name":"demo","arguments":{}}<|tool_call|>`,
		`<|tool_call>{"name":"demo","arguments":{}}<|tool_call|>`,
		`<|tool_call>{"name":"demo","arguments":{}}<|tool_call|>`,
		`{"type":"reply","message":"best effort"}`,
	})
	runner.turnProcessor.tools = caller
	runner.turnProcessor.gate = testGate(t, 10) // tiny budget: every call spills, each with a distinct file name

	var notices []string
	runner.events.(*events.OrderedDispatcher).Register(events.HandlerFunc(func(_ context.Context, e events.Event) error {
		if e.Type == events.EventNotice {
			notices = append(notices, e.Notice.Text)
		}
		return nil
	}))

	if _, err := runner.Run(context.Background(), RunRequest{SessionID: "s1", Message: "run"}); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, n := range notices {
		if strings.Contains(n, "repeated tool call detected") {
			found = true
		}
	}
	if !found {
		t.Fatalf("notices = %v, want the repeat guard to have fired despite the spill stub's changing file name", notices)
	}
}

// TestSpillHintPageLinesFollowBudget: the paging hint must state a line count
// derived from the actual result budget and this file's line width, not a
// fixed ~500 (#536).
func TestSpillHintPageLinesFollowBudget(t *testing.T) {
	wide := strings.Repeat(strings.Repeat("x", 199)+"\n", 200) // 200 bytes/line
	stub := spillStub(wide, "/tmp/spill.txt", 1_000)           // 4,000-byte budget -> ~20 lines
	if !strings.Contains(stub, `"limit":20`) {
		t.Fatalf("hint limit not derived from budget: %q", firstLines(stub, 3))
	}
	if !strings.Contains(stub, "~1000 tokens (~4000 bytes)") || !strings.Contains(stub, "~20 lines per page") {
		t.Fatalf("hint does not state the budget: %q", firstLines(stub, 3))
	}
	narrow := spillStub(strings.Repeat("ab\n", 4000), "/tmp/spill.txt", 1_000) // 3 bytes/line
	if !strings.Contains(narrow, `"limit":2000`) {
		t.Fatalf("narrow-line hint not derived from budget: %q", firstLines(narrow, 3))
	}
}

func firstLines(s string, n int) string {
	parts := strings.SplitN(s, "\n", n+1)
	return strings.Join(parts[:min(n, len(parts))], "\n")
}

// TestCapStrategyBoundedByGateBudget: read's cap path must not exceed
// twice the result budget, so one page can never cost 16k (#536).
func TestCapStrategyBoundedByGateBudget(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	gate := newResultGate(config.AgentConfig{
		ToolResultMaxTokens:  1_000, // 4,000-byte budget -> 8,000-byte cap
		ToolResultStrategy:   "spill",
		ToolResultStrategies: map[string]string{"read": "cap"},
	}, "cap-budget-session", zap.NewNop())
	big := strings.Repeat("y", 40_000)
	got, ref := gate.cap("read", nil, big)
	if ref != "" {
		t.Fatalf("cap strategy spilled: ref %q", ref)
	}
	if len(got) > 8_000+len(truncationMarker(8_000)) {
		t.Fatalf("capped read result = %d bytes, want <= ~8000", len(got))
	}
	if !strings.Contains(got, "output truncated at ~2000 tokens") {
		t.Fatalf("missing standard truncation notice: %q", got[max(len(got)-200, 0):])
	}
}

func TestEnvelopeLongestFieldJSONFallback(t *testing.T) {
	for _, input := range []string{
		`{"stdout":"long output","exit_code":7}`,
		"```json\n{\"stdout\":\"long output\",\"exit_code\":7}\n```",
		`prefix {"stdout":"long output","exit_code":7} trailing {"stdout":"other"}`,
	} {
		fields, key, value, ok := envelopeLongestField(input)
		if !ok || key != "stdout" || value != "long output" || string(fields["exit_code"]) != "7" {
			t.Fatalf("envelopeLongestField(%q) = %v, %q, %q, %v", input, fields, key, value, ok)
		}
	}
	for _, input := range []string{
		`plain output`, `null`, `{}`, `prefix {"stdout":"unfinished"`,
		`prefix {bad} {"stdout":"must not skip first brace"}`,
	} {
		fields, key, value, ok := envelopeLongestField(input)
		if ok || fields != nil || key != "" || value != "" {
			t.Fatalf("invalid envelope %q yielded %v, %q, %q, %v", input, fields, key, value, ok)
		}
	}
}
