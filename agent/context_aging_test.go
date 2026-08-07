package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/platform/events"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

// bulky pads a result past agedMinBytes so the size floor does not skip it.
func bulky(text string) string { return text + "\n" + strings.Repeat("padding ", 40) }

// exchange is one assistant tool call plus its result.
func exchange(id, tool, args, result string) []inference.Message {
	return []inference.Message{
		inference.AssistantMessage("", inference.ToolCall{ID: id, Name: tool, Arguments: args}),
		inference.ToolMessage(id, result),
	}
}

func TestAgeToolResultsCollapsesOldResultsOnly(t *testing.T) {
	var messages []inference.Message
	messages = append(messages, inference.SystemMessage("sys"), inference.UserMessage("go"))
	// c1 is the newest read of a.go and is pinned; c0 is superseded and ages.
	messages = append(messages, exchange("c0", "read", `{"path":"a.go"}`, bulky("first"))...)
	messages = append(messages, exchange("c1", "read", `{"path":"a.go"}`, bulky("line1\nline2\nline3"))...)
	messages = append(messages, exchange("c2", "shell", `{"command":"go test"}`, bulky(`{"error":"exit 1"}`))...)
	messages = append(messages, exchange("c3", "read", `{"path":"b.go"}`, bulky("x"))...)
	messages = append(messages, exchange("c4", "read", `{"path":"b.go"}`, bulky("y"))...)
	messages = append(messages, exchange("c5", "read", `{"path":"b.go"}`, bulky("z"))...)

	aged, count := ageToolResults(messages, 3, 0, pinnedReadBudgetBytes, nil, nil)
	if count != 1 {
		t.Fatalf("aged %d results, want 1", count)
	}
	if got := aged[3].Content; got != "[elided read {\"path\":\"a.go\"}: 2 lines; call again if needed]" {
		t.Fatalf("stub = %q", got)
	}
	if aged[5].Content != bulky("line1\nline2\nline3") {
		t.Fatal("the newest read of a path must survive aging")
	}
	if aged[7].Content != bulky(`{"error":"exit 1"}`) {
		t.Fatal("newest error result of a tool must survive aging")
	}
	if aged[9].Content != bulky("x") || messages[3].Content != bulky("first") {
		t.Fatal("recent results and the input slice must be untouched")
	}
	if again, count := ageToolResults(aged, 3, 0, pinnedReadBudgetBytes, nil, nil); count != 0 || again[3].Content != aged[3].Content {
		t.Fatal("a stub must not be aged twice")
	}
}

func TestAgeToolResultsKeepsReferencedResultOneMoreTurn(t *testing.T) {
	var messages []inference.Message
	messages = append(messages, exchange("c0", "shell", `{"command":"pytest -k worker"}`, bulky("passed"))...)
	for _, id := range []string{"c1", "c2", "c3"} {
		messages = append(messages, exchange(id, "shell", `{"command":"ls"}`, bulky("ok"))...)
	}
	messages = append(messages, inference.AssistantMessage("Still investigating pytest -k worker"))

	if _, count := ageToolResults(messages, 3, 0, pinnedReadBudgetBytes, nil, nil); count != 0 {
		t.Fatalf("aged %d, want the referenced result kept one extra turn", count)
	}
	// One more turn spends the bonus; the text message above counted as a
	// turn too, so the referenced result goes with it.
	messages = append(messages, exchange("c4", "shell", `{"command":"ls"}`, bulky("ok"))...)
	if aged, count := ageToolResults(messages, 3, 0, pinnedReadBudgetBytes, nil, nil); count == 0 || !strings.HasPrefix(aged[1].Content, agedResultPrefix) {
		t.Fatalf("aged %d after the bonus turn, want the pytest result stubbed", count)
	}
}

func TestPrepareAgesResultsAndNotices(t *testing.T) {
	bus := events.NewOrderedDispatcher(zap.NewNop())
	var notices []string
	bus.Register(events.HandlerFunc(func(_ context.Context, e events.Event) error {
		if e.Type == events.EventNotice {
			notices = append(notices, e.Notice.Text)
		}
		return nil
	}))
	budget := &contextBudget{resultKeepTurns: 1, resultAgeTurns: 1, events: bus, logger: zap.NewNop()}
	var messages []inference.Message
	messages = append(messages, exchange("c1", "shell", `{}`, bulky("old"))...)
	messages = append(messages, exchange("c2", "shell", `{}`, bulky("mid"))...)
	messages = append(messages, exchange("c3", "shell", `{}`, bulky("new"))...)

	out, _ := budget.Prepare(context.Background(), "s1", messages, contextWindowState{}, nil)
	if !strings.HasPrefix(out[1].Content, agedResultPrefix) || out[3].Content != bulky("mid") {
		t.Fatalf("prepare did not age the old result: %#v", out)
	}
	if len(notices) != 1 || !strings.HasPrefix(notices[0], "context.elided: 1 tool result") {
		t.Fatalf("notices = %v", notices)
	}
}

// chain builds n back-to-back tool exchanges; the result of exchange i is
// n-1-i assistant turns old.
func chain(n int) []inference.Message {
	var messages []inference.Message
	for i := range n {
		messages = append(messages, exchange(fmt.Sprintf("c%d", i), "read", `{"path":"f.go"}`, strings.Repeat("body line\n", 40))...)
	}
	return messages
}

func TestAgeToolResultsBatchesUntilTheAgeThreshold(t *testing.T) {
	// keep=3, age=8: results at ages 4..7 are eligible but nothing has crossed
	// the trigger yet, so history is left alone and the prefix stays cacheable.
	if _, count := ageToolResults(chain(8), 3, 8, pinnedReadBudgetBytes, nil, nil); count != 0 {
		t.Fatalf("aged %d results below the age threshold, want 0", count)
	}
	// One result at age 9 crosses it and every eligible result (ages 4..9)
	// collapses in that single pass.
	messages := chain(11)
	aged, count := ageToolResults(messages, 3, 8, pinnedReadBudgetBytes, nil, nil)
	// Ages 4..10 are eligible; the newest (age 0) is the only one pinned.
	if count != 7 {
		t.Fatalf("aged %d results on the trigger pass, want 7 (ages 4..10)", count)
	}
	for index := 1; index <= 13; index += 2 {
		if !strings.HasPrefix(aged[index].Content, agedResultPrefix) {
			t.Fatalf("result at index %d not collapsed: %q", index, aged[index].Content)
		}
	}
	// Next pass: everything shifts a turn, but the stubs are skipped, so only
	// the single newly eligible result counts — below the trigger, so no edit.
	next := append(append([]inference.Message(nil), aged...), exchange("c11", "read", `{"path":"f.go"}`, strings.Repeat("body line\n", 40))...)
	if _, count := ageToolResults(next, 3, 8, pinnedReadBudgetBytes, nil, nil); count != 0 {
		t.Fatalf("aged %d on the follow-up pass, want 0", count)
	}
}

func TestAgeToolResultsSkipsResultsUnderTheSizeFloor(t *testing.T) {
	var messages []inference.Message
	messages = append(messages, exchange("c1", "shell", `{"command":"ls"}`, "ok")...)
	messages = append(messages, exchange("c2", "shell", `{"command":"pwd"}`, strings.Repeat("x", agedMinBytes))...)
	// A newer shell result, so the protected "newest per tool" is not c2.
	messages = append(messages, exchange("c3", "shell", `{"command":"id"}`, bulky("uid"))...)
	for _, id := range []string{"c4", "c5", "c6", "c7"} {
		messages = append(messages, exchange(id, "read", `{"path":"a.go"}`, bulky("body"))...)
	}
	aged, count := ageToolResults(messages, 3, 0, pinnedReadBudgetBytes, nil, nil)
	if count != 1 {
		t.Fatalf("aged %d results, want only the one over the size floor", count)
	}
	if aged[1].Content != "ok" {
		t.Fatalf("a %d-byte result must not be stubbed: %q", len("ok"), aged[1].Content)
	}
	if !strings.HasPrefix(aged[3].Content, agedResultPrefix) {
		t.Fatalf("result at the size floor not stubbed: %q", aged[3].Content)
	}
}

func TestAgeToolResultsStubPointsAtTheSpillFile(t *testing.T) {
	var messages []inference.Message
	messages = append(messages, exchange("c0", "read", `{"path":"a.go"}`, bulky("first"))...)
	messages = append(messages, exchange("c1", "shell", `{"command":"go test ./..."}`, bulky("out"))...)
	messages = append(messages, exchange("c2", "read", `{"path":"a.go"}`, bulky("src"))...)
	for _, id := range []string{"c3", "c4", "c5"} {
		messages = append(messages, exchange(id, "read", `{"path":"b.go"}`, bulky("body"))...)
	}
	messages = append(messages, exchange("c6", "shell", `{"command":"pwd"}`, bulky("dir"))...)
	refs := map[string]string{"c1": "/home/u/.invokr/spill/s1/c1.txt"}

	aged, count := ageToolResults(messages, 3, 0, pinnedReadBudgetBytes, refs, nil)
	if count != 2 {
		t.Fatalf("aged %d results, want 2", count)
	}
	want := `[elided shell {"command":"go test ./..."}: 2 lines; full output in /home/u/.invokr/spill/s1/c1.txt, read it]`
	if aged[3].Content != want {
		t.Fatalf("spilled stub = %q, want %q", aged[3].Content, want)
	}
	if got := aged[1].Content; !strings.HasSuffix(got, "call again if needed]") {
		t.Fatalf("unspilled stub = %q, want the call-again form", got)
	}
	if aged[5].Content != bulky("src") {
		t.Fatal("the newest read of a.go must survive aging")
	}
}

func TestAgeToolResultsNeverAgesWriteToolResults(t *testing.T) {
	var messages []inference.Message
	messages = append(messages, exchange("c1", "apply_patch", `{"path":"a.go"}`, bulky("applied"))...)
	messages = append(messages, exchange("c2", "apply_patch", `{"path":"b.go"}`, bulky("applied"))...)
	for _, id := range []string{"c3", "c4", "c5", "c6", "c7", "c8"} {
		messages = append(messages, exchange(id, "read", `{"path":"c.go"}`, bulky("body"))...)
	}
	writes := func(name string) bool { return name == "apply_patch" }

	// c8 is the newest read of c.go and is pinned; c3 and c4 are superseded.
	if _, count := ageToolResults(messages, 3, 0, pinnedReadBudgetBytes, nil, writes); count != 2 {
		t.Fatalf("aged %d results with write protection on, want the two old read_file results", count)
	}
	aged, count := ageToolResults(messages, 3, 0, pinnedReadBudgetBytes, nil, nil)
	if count != 3 {
		t.Fatalf("aged %d results without write protection, want 3", count)
	}
	if !strings.HasPrefix(aged[1].Content, agedResultPrefix) {
		t.Fatal("without a writesWorkspace func nothing is pinned")
	}
}

func TestAgeToolResultsReferencedBonusStillTriggersAtTheAgeThreshold(t *testing.T) {
	// keep=3, age=8: the oldest result (c0) is referenced by the trailing
	// assistant message, so its inclusion threshold rises to keep+1 — but at
	// raw age 9 it clears that easily, remains a candidate, and its raw age
	// (not shrunk by the bonus) still crosses ageTurns and fires the batch.
	messages := chain(10)
	messages = append(messages, inference.AssistantMessage("Still using f.go"))
	aged, count := ageToolResults(messages, 3, 8, pinnedReadBudgetBytes, nil, nil)
	if count == 0 {
		t.Fatal("a referenced result at age 9 must still be a candidate and still trigger aging")
	}
	if !strings.HasPrefix(aged[3].Content, agedResultPrefix) {
		t.Fatalf("referenced-but-eligible oldest result not collapsed: %q", aged[3].Content)
	}
}

func TestSpillRefSnapshotNilProcessor(t *testing.T) {
	var p *TurnProcessor
	if got := p.spillRefSnapshot(); got != nil {
		t.Fatalf("spillRefSnapshot() on nil *TurnProcessor = %v, want nil", got)
	}
}

func TestAgeToolResultsKeepsNewestResultPerTool(t *testing.T) {
	var messages []inference.Message
	messages = append(messages, exchange("c1", "grep", `{"pattern":"foo"}`, bulky("hit"))...)
	messages = append(messages, exchange("c2", "grep", `{"pattern":"bar"}`, bulky("hit"))...)
	for _, id := range []string{"c3", "c4", "c5", "c6", "c7"} {
		messages = append(messages, exchange(id, "read", `{"path":"a.go"}`, bulky("body"))...)
	}
	aged, count := ageToolResults(messages, 3, 0, pinnedReadBudgetBytes, nil, nil)
	if count == 0 {
		t.Fatalf("aged %d results, want at least the superseded grep result", count)
	}
	if !strings.HasPrefix(aged[1].Content, agedResultPrefix) || aged[3].Content != bulky("hit") {
		t.Fatalf("the newest grep result must survive: %q", aged[3].Content)
	}
}

// The newest read of a path is the model's freshest copy of its content; an
// older, superseded read of the same path is stale and ages — a re-read
// recovers it if the model still needs it.
func TestAgeToolResultsKeepsTheNewestReadOfEachPath(t *testing.T) {
	var messages []inference.Message
	messages = append(messages, exchange("c1", "read", `{"path":"spec.md"}`, bulky("spec"))...)
	messages = append(messages, exchange("c2", "read", `{"path":"spec.md"}`, bulky("spec again"))...)
	for _, id := range []string{"c3", "c4"} {
		messages = append(messages, exchange(id, "shell", `{"command":"ls"}`, bulky("ok"))...)
	}
	// Newer reads of another path, so spec.md's re-read (c2) is still the
	// newest read of ITS path, even though it is not the newest read_file call
	// overall.
	for _, id := range []string{"c5", "c6"} {
		messages = append(messages, exchange(id, "read", `{"path":"other.go"}`, bulky("other"))...)
	}

	aged, count := ageToolResults(messages, 3, 0, pinnedReadBudgetBytes, nil, nil)
	if count != 1 {
		t.Fatalf("aged %d results, want only the stale first read of spec.md", count)
	}
	if !strings.HasPrefix(aged[1].Content, agedResultPrefix) {
		t.Fatalf("stale first read of spec.md not collapsed: %q", aged[1].Content)
	}
	if aged[3].Content != bulky("spec again") {
		t.Fatalf("newest read of spec.md was aged: %q", aged[3].Content)
	}
}

// A batched read (paths) covers several files in one result; every one of
// its paths must pin that result just like a single-path read does, or a
// model that batches its reads (the #577 read-batching change) gets no
// anti-aging protection at all for the files it read that way.
func TestAgeToolResultsPinsEveryPathOfABatchedRead(t *testing.T) {
	var messages []inference.Message
	messages = append(messages, exchange("c1", "read", `{"paths":["a.go","b.go"]}`, bulky("=== a.go ===\na\n=== b.go ===\nb"))...)
	for _, id := range []string{"c2", "c3"} {
		messages = append(messages, exchange(id, "shell", `{"command":"ls"}`, bulky("ok"))...)
	}
	// A later single-path re-read of b.go supersedes the batched call for
	// b.go only; a.go's newest read is still the batched result.
	messages = append(messages, exchange("c4", "read", `{"path":"b.go"}`, bulky("b again"))...)

	aged, count := ageToolResults(messages, 3, 0, pinnedReadBudgetBytes, nil, nil)
	if count != 0 {
		t.Fatalf("aged %d results, want the batched read (still newest for a.go) pinned", count)
	}
	if strings.HasPrefix(aged[1].Content, agedResultPrefix) {
		t.Fatalf("batched read of a.go/b.go was aged despite still being the newest read of a.go: %q", aged[1].Content)
	}
}

// Reads are pinned newest path first until pinnedReadBudgetBytes is spent:
// ten reads just under the 4 KB cap total more than the 32 KB budget, so the
// oldest ages even though it is under the per-read cap (#558's budget, kept by
// #559 as the only ceiling on total pinned bytes).
func TestAgeToolResultsPinsReadsWithinTheBudget(t *testing.T) {
	const paths = 10
	var messages []inference.Message
	for index := range paths {
		path := fmt.Sprintf("f%d.go", index)
		messages = append(messages, exchange(path, "read", `{"path":"`+path+`"}`, strings.Repeat("x", pinnedReadMaxBytes-1))...)
	}
	for _, id := range []string{"c1", "c2", "c3", "c4"} {
		messages = append(messages, exchange(id, "shell", `{"command":"ls"}`, bulky("ok"))...)
	}
	aged, count := ageToolResults(messages, 3, 0, pinnedReadBudgetBytes, nil, nil)
	// Ten reads of ~4 KB are 40 KB against a 32 KB budget, so the oldest two
	// fall outside it and age; the rest stay pinned.
	if count != 2 {
		t.Fatalf("aged %d results, want the two oldest reads past the budget", count)
	}
	for _, index := range []int{1, 3} {
		if !strings.HasPrefix(aged[index].Content, agedResultPrefix) {
			t.Fatalf("read at %d survived though it is past the budget: %.60q", index, aged[index].Content)
		}
	}
	for _, index := range []int{5, 19} {
		if strings.HasPrefix(aged[index].Content, agedResultPrefix) {
			t.Fatalf("read at %d aged though inside the budget", index)
		}
	}
}

// A read over pinnedReadMaxBytes ages like any other result: pinning it would
// risk blowing a 32k context window on a single stale copy.
func TestAgeToolResultsAgesAReadOverTheCap(t *testing.T) {
	var messages []inference.Message
	messages = append(messages, exchange("c1", "read", `{"path":"huge.go"}`, strings.Repeat("x", pinnedReadMaxBytes+1))...)
	for _, id := range []string{"c2", "c3", "c4", "c5"} {
		messages = append(messages, exchange(id, "shell", `{"command":"ls"}`, bulky("ok"))...)
	}
	aged, count := ageToolResults(messages, 3, 0, pinnedReadBudgetBytes, nil, nil)
	if count != 1 {
		t.Fatalf("aged %d results, want the oversized read collapsed", count)
	}
	if !strings.HasPrefix(aged[1].Content, agedResultPrefix) {
		t.Fatalf("read over the cap not collapsed: %.80s", aged[1].Content)
	}
}

// keep=5 is the shipped default: a result is verbatim for five assistant
// turns and eligible on the sixth.
func TestAgeToolResultsKeepTurnsBoundary(t *testing.T) {
	var messages []inference.Message
	for i := range 7 {
		messages = append(messages, exchange(fmt.Sprintf("c%d", i), "shell", fmt.Sprintf(`{"command":"cmd%d"}`, i), bulky("out"))...)
	}
	// Ages run 0..6: only the age-6 result is past keep=5.
	if _, count := ageToolResults(messages, constants.ToolResultKeepTurns, 0, pinnedReadBudgetBytes, nil, nil); count != 1 {
		t.Fatalf("aged %d results at keep=5, want 1", count)
	}
	if _, count := ageToolResults(messages[2:], constants.ToolResultKeepTurns, 0, pinnedReadBudgetBytes, nil, nil); count != 0 {
		t.Fatalf("aged %d results with nothing past keep=5, want 0", count)
	}
}

// Aging defaults scale linearly with the window from their 32k tuning, capped
// at 4x; explicit settings (config or env) win (#577).
func TestAgingLimitsScaleWithWindow(t *testing.T) {
	cases := []struct {
		window, keep, age          int64
		wantKeep, wantAge, wantPin int
	}{
		{0, 0, 0, 5, 8, 32 << 10},
		{32 << 10, 0, 0, 5, 8, 32 << 10},
		{64 << 10, 0, 0, 10, 16, 64 << 10},
		{128 << 10, 0, 0, 20, 32, 128 << 10},
		{1 << 20, 0, 0, 20, 32, 128 << 10},
		{64 << 10, 3, 4, 3, 4, 64 << 10},
		{32 << 10, 12, 0, 12, 12, 32 << 10},
		{128 << 10, 0, 8, 8, 8, 128 << 10}, // explicit age caps the scaled keep
	}
	for _, c := range cases {
		b := &contextBudget{windowSize: c.window, resultKeepTurns: int(c.keep), resultAgeTurns: int(c.age)}
		if keep, age, pin := b.agingLimits(); keep != c.wantKeep || age != c.wantAge || pin != c.wantPin {
			t.Fatalf("window %d keep %d age %d: got %d/%d/%d, want %d/%d/%d", c.window, c.keep, c.age, keep, age, pin, c.wantKeep, c.wantAge, c.wantPin)
		}
	}
}

// At a 64k window Prepare pins 64 KiB of reads: the ten ~4 KB reads that the
// 32 KiB budget would partly age all stay verbatim (#577).
func TestPrepareScalesThePinBudgetWithTheWindow(t *testing.T) {
	var messages []inference.Message
	for index := range 10 {
		path := fmt.Sprintf("f%d.go", index)
		messages = append(messages, exchange(path, "read", `{"path":"`+path+`"}`, strings.Repeat("x", pinnedReadMaxBytes-1))...)
	}
	for _, id := range []string{"c1", "c2", "c3", "c4"} {
		messages = append(messages, exchange(id, "shell", `{"command":"ls"}`, bulky("ok"))...)
	}
	budget := &contextBudget{windowSize: 64 << 10, resultKeepTurns: 3, resultAgeTurns: 3, events: events.NewOrderedDispatcher(zap.NewNop()), logger: zap.NewNop()}
	out, _ := budget.Prepare(context.Background(), "s1", messages, contextWindowState{}, nil)
	for index := 1; index < 20; index += 2 {
		if strings.HasPrefix(out[index].Content, agedResultPrefix) {
			t.Fatalf("read at %d aged despite the 64 KiB pin budget", index)
		}
	}
}
