package agent

import (
	"strings"
	"testing"

	inference "github.com/rivt-ai/go-inference-router"
)

func assistantCalls(ids ...string) inference.Message {
	calls := make([]inference.ToolCall, 0, len(ids))
	for _, id := range ids {
		calls = append(calls, inference.ToolCall{ID: id, Name: "read_file", Arguments: `{}`})
	}
	return inference.AssistantMessage("", calls...)
}

func retentionPrefix() []inference.Message {
	return []inference.Message{inference.SystemMessage("system"), inference.UserMessage("request")}
}

func TestPlanRetentionNeverOrphansToolMessage(t *testing.T) {
	tests := []struct {
		name string
		tail []inference.Message
	}{
		{"parallel fan-out", []inference.Message{
			assistantCalls("a"), inference.ToolMessage("a", "one"),
			assistantCalls("b", "c"), inference.ToolMessage("b", "two"), inference.ToolMessage("c", "three"),
		}},
		{"single pair", []inference.Message{
			inference.AssistantMessage("thinking"), assistantCalls("a"), inference.ToolMessage("a", "one"),
			inference.AssistantMessage("done"),
		}},
		{"interleaved", []inference.Message{
			assistantCalls("a"), inference.ToolMessage("a", "one"), inference.UserMessage("next"),
			assistantCalls("b"), inference.ToolMessage("b", "two"),
			assistantCalls("c"), inference.ToolMessage("c", "three"),
		}},
		{"sequential calls, late results", []inference.Message{
			assistantCalls("y"), assistantCalls("a"),
			inference.ToolMessage("y", "one"), inference.ToolMessage("a", "two"),
			inference.UserMessage("x"), inference.AssistantMessage("z"), inference.UserMessage("w"),
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			messages := append(retentionPrefix(), test.tail...)
			plan := planRetention(messages, 0, 1, nil)
			if orphans := orphanedToolCallIDs(messages[plan.tailStart:]); len(orphans) > 0 {
				t.Fatalf("retained tail orphans %v (tailStart = %d)", orphans, plan.tailStart)
			}
		})
	}
}

func TestPlanRetentionKeepsFlatFloor(t *testing.T) {
	messages := append(retentionPrefix(),
		inference.AssistantMessage("one"), inference.UserMessage("two"),
		inference.AssistantMessage("three"), inference.UserMessage("four"),
		inference.AssistantMessage("five"), inference.UserMessage("six"),
	)
	plan := planRetention(messages, 0, 1, nil)
	if got := len(messages) - plan.tailStart; got != minRetainedMessages {
		t.Fatalf("retained %d messages, want the %d floor", got, minRetainedMessages)
	}
	if !plan.summarizable() {
		t.Fatal("plan should be summarizable")
	}
}

func TestPlanRetentionClampsToPrefix(t *testing.T) {
	all := append(retentionPrefix(), inference.AssistantMessage("one"))
	for count := 0; count <= len(all); count++ {
		plan := planRetention(all[:count], 0, 1, nil)
		if plan.summarizable() {
			t.Fatalf("%d messages: unexpectedly summarizable (%+v)", count, plan)
		}
		if len(summarySegment(all[:count], plan)) != 0 {
			t.Fatalf("%d messages: non-empty summary segment", count)
		}
	}
}

func TestSnapTailToPairNeverNarrows(t *testing.T) {
	messages := append(retentionPrefix(),
		assistantCalls("a"), inference.ToolMessage("a", "one"), inference.UserMessage("next"),
	)
	for start := 2; start <= len(messages); start++ {
		if got := snapTailToPair(messages, start, 2); got > start {
			t.Fatalf("start %d narrowed to %d", start, got)
		}
	}
}

func TestPlanRetentionGrowsTailToTokenBudget(t *testing.T) {
	messages := retentionPrefix()
	for i := 0; i < 10; i++ {
		messages = append(messages, inference.AssistantMessage(strings.Repeat("word ", 40)), inference.UserMessage("ok"))
	}
	flat := planRetention(messages, 0, 1, nil)
	grown := planRetention(messages, 400, 1, nil)
	if grown.tailStart >= flat.tailStart {
		t.Fatalf("budget did not widen the tail: grown %d, flat %d", grown.tailStart, flat.tailStart)
	}
	if got := calibratedMessageTokens(messages[grown.tailStart:], 1); got > 400 {
		t.Fatalf("retained tail estimates %d tokens, budget 400", got)
	}
	if retained := len(messages) - grown.tailStart; retained > maxRetainedMessages {
		t.Fatalf("retained %d messages, cap %d", retained, maxRetainedMessages)
	}
}

func TestPlanRetentionPinsNewestMutatingResult(t *testing.T) {
	writes := func(name string) bool { return name == "apply_patch" }
	messages := append(retentionPrefix(),
		assistantCalls("p1"), inference.ToolMessage("p1", "patched v1"),
		assistantCalls("r1"), inference.ToolMessage("r1", "file contents"),
		assistantCalls("p2"), inference.ToolMessage("p2", "patched v2"),
		inference.AssistantMessage("a"), inference.UserMessage("b"),
		inference.AssistantMessage("c"), inference.UserMessage("d"),
	)
	// p1 and p2 share name+arguments (assistantCalls uses `{}` for all), so only
	// the newest write result is pinned; the read_file-shaped r1 is not.
	for i, id := range []string{"p1", "r1", "p2"} {
		call := messages[2+2*i].ToolCalls[0]
		if id != "r1" {
			call.Name = "apply_patch"
		}
		messages[2+2*i].ToolCalls[0] = call
	}
	plan := planRetention(messages, 0, 1, writes)
	want := map[int]bool{7: true}
	if len(plan.pinned) != 1 || !plan.pinned[7] {
		t.Fatalf("pinned = %v, want %v (newest apply_patch result)", plan.pinned, want)
	}
}

func TestPinnedResultIndicesBoundedToNewest(t *testing.T) {
	writes := func(string) bool { return true }
	messages := retentionPrefix()
	for i := 0; i < maxPinnedResults+3; i++ {
		id := string(rune('a' + i))
		call := assistantCalls(id)
		call.ToolCalls[0].Arguments = `{"n":"` + id + `"}`
		messages = append(messages, call, inference.ToolMessage(id, "out"))
	}
	pinned := pinnedResultIndices(messages, 2, len(messages), writes)
	if len(pinned) != maxPinnedResults {
		t.Fatalf("pinned %d results, cap %d", len(pinned), maxPinnedResults)
	}
	for index := range pinned {
		if index < len(messages)-2*maxPinnedResults {
			t.Fatalf("kept an old pin at %d; the cap must drop oldest first", index)
		}
	}
}

// Reads inside the summarized middle are not carried verbatim: only write
// results are pinned, so summarization reclaims read bytes every cycle (#559).
func TestPlanRetentionDoesNotCarryReads(t *testing.T) {
	read := func(id, path string) inference.Message {
		return inference.AssistantMessage("", inference.ToolCall{ID: id, Name: readFileToolName, Arguments: `{"path":"` + path + `"}`})
	}
	messages := append(retentionPrefix(),
		read("r1", "main.go"), inference.ToolMessage("r1", "v1"),
		read("r2", "main.go"), inference.ToolMessage("r2", "v2"),
		read("r3", "big.go"), inference.ToolMessage("r3", strings.Repeat("x", pinnedReadMaxBytes)),
		inference.AssistantMessage("a"), inference.UserMessage("b"),
		inference.AssistantMessage("c"), inference.UserMessage("d"),
	)
	plan := planRetention(messages, 0, 1, nil)
	if len(plan.pinned) != 0 {
		t.Fatalf("pinned = %v; want no pins without a write predicate", plan.pinned)
	}
	if kept := pinnedMessages(messages, plan); len(kept) != 0 {
		t.Fatalf("carried = %+v, want nothing carried through summarization", kept)
	}
}
