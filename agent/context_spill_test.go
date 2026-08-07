package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/invokr-ai/invokr/platform/events"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

func spillBudget(t *testing.T) *contextBudget {
	t.Helper()
	return &contextBudget{spill: &fileSpillStore{dir: t.TempDir(), pruneOnce: &sync.Once{}}, logger: zap.NewNop()}
}

func TestContextArchivesRecoverExactPayloads(t *testing.T) {
	b := spillBudget(t)
	args, _ := json.Marshal(map[string]string{"path": "report.py", "cwd": "/work/expense-report-2025", "content": strings.Repeat("print('✓')\n", 900)})
	result, _ := json.Marshal(map[string]any{"stdout": strings.Repeat("row\n", 5000), "stderr": strings.Repeat("warning\n", 800), "exit_code": 7, "timed_out": false, "truncated": true, "cwd": "/work/expense-report-2025"})
	for _, input := range []struct {
		name, text string
		arguments  bool
	}{{"write_file", string(args), true}, {"shell", string(result), false}} {
		t.Run(input.name, func(t *testing.T) {
			got := b.archivePayload(input.name, input.text, input.arguments)
			if len(got) > 2048 || !json.Valid([]byte(got)) {
				t.Fatalf("archive preview is oversized/invalid: %d bytes", len(got))
			}
			var fields map[string]any
			_ = json.Unmarshal([]byte(got), &fields)
			if input.arguments {
				if fields["path"] != "report.py" || fields["cwd"] != "/work/expense-report-2025" {
					t.Fatalf("lost exact path metadata: %v", fields)
				}
			} else if fields["exit_code"] != float64(7) || fields["timed_out"] != false || fields["truncated"] != true {
				t.Fatalf("lost shell status: %v", fields)
			}
			found := false
			files, _ := filepath.Glob(filepath.Join(b.spill.(*fileSpillStore).dir, "*.txt"))
			for _, file := range files {
				body, err := os.ReadFile(file)
				if err == nil && string(body) == input.text && strings.Contains(got, file) {
					found = true
				}
			}
			if !found {
				t.Fatal("preview does not point to an exact, readable archive")
			}
			if again := b.archivePayload(input.name, got, input.arguments); again != got {
				t.Fatal("archive pointer re-archived")
			}
		})
	}
}

func TestContextSpillPreservesNewestAndOriginalTranscript(t *testing.T) {
	b := spillBudget(t)
	messages := replayPrefix()
	for _, id := range []string{"old", "new"} {
		messages = append(messages, exchange(id, "shell", `{"command":"`+strings.Repeat("x", 9000)+`"}`, strings.Repeat("out", 7000))...)
	}
	original, _ := json.Marshal(messages)
	got := b.spillHistory(messages, true)
	after, _ := json.Marshal(messages)
	if string(after) != string(original) {
		t.Fatal("rewrote original/prefetch backing storage")
	}
	if !reflect.DeepEqual(got[4:], messages[4:]) {
		t.Fatal("newest exchange changed without pressure")
	}
	if len(got[2].ToolCalls[0].Arguments) >= 2048 || len(got[3].Content) >= 2048 {
		t.Fatal("old exchange not archived")
	}
	for _, message := range got[2:4] {
		blocks, _ := json.Marshal(message.ContentBlocks())
		if strings.Contains(string(blocks), strings.Repeat("x", 9000)) || strings.Contains(string(blocks), strings.Repeat("out", 7000)) {
			t.Fatal("typed content blocks still contain the archived payload")
		}
	}
	if ids := orphanedToolCallIDs(got); len(ids) > 0 {
		t.Fatalf("orphaned: %v", ids)
	}
	b.spill = failingSpillStore{}
	if failed := b.spillHistory(messages, false); !reflect.DeepEqual(failed, messages) {
		t.Fatal("spill failure destroyed original evidence")
	}
}

func TestPinnedBudgetCountsOwnersAndSiblings(t *testing.T) {
	messages := replayPrefix()
	messages = append(messages, exchange("small", "shell", `{}`, "ok")...)
	calls := []inference.ToolCall{{ID: "write", Name: "shell", Arguments: strings.Repeat("a", 17000)}, {ID: "read", Name: "read_file", Arguments: `{}`}}
	messages = append(messages, inference.AssistantMessage("", calls...), inference.ToolMessage("write", "ok"), inference.ToolMessage("read", strings.Repeat("b", 17000)))
	plan := snappedPlan(messages, 2, len(messages), func(name string) bool { return name == "shell" })
	kept := pinnedMessages(messages, plan)
	if len(kept) != 2 || estimatedMessageTokens(kept) > maxPinnedTokens {
		t.Fatalf("oversized owner/sibling group pinned: %d messages", len(kept))
	}
	// Multiple candidates from one group spend its bytes only once.
	messages[4] = inference.AssistantMessage("", inference.ToolCall{ID: "write", Name: "shell", Arguments: `{"x":0}`}, inference.ToolCall{ID: "read", Name: "shell", Arguments: `{"x":1}`})
	plan = snappedPlan(messages, 2, len(messages), func(string) bool { return true })
	if len(pinnedMessages(messages, plan)) != 5 {
		t.Fatal("paired group was charged twice")
	}
}

func TestCompactionHeadroomAndRuntimeWorkspace(t *testing.T) {
	b := spillBudget(t)
	b.workspaceRoot = "/work/expense-report-2025"
	messages := append(replayPrefix(), exchange("latest", "shell", `{"command":"`+strings.Repeat("x", 9000)+`"}`, strings.Repeat("row", 10000))...)
	got, state := b.afterCompaction(messages, contextWindowState{}, 16000, true, 6000)
	if estimatedMessageTokens(got) > 6000 {
		t.Fatal("no headroom after latest oversized exchange")
	}
	if !strings.Contains(got[len(got)-1].Content, b.workspaceRoot) || !strings.Contains(got[len(got)-1].Content, "do not prepend") {
		t.Fatal("runtime cwd fact missing")
	}
	if len(orphanedToolCallIDs(got)) != 0 {
		t.Fatal("headroom pass orphaned result")
	}
	b.windowSize = 1000000
	again, _ := b.Prepare(context.Background(), "s", got, state, nil)
	if !reflect.DeepEqual(again, got) {
		t.Fatal("ordinary turn invalidated cached prefix")
	}
}

func TestCompactionArchivesWithoutDiscardingPrefetch(t *testing.T) {
	for _, percent := range []int64{90, 110} {
		t.Run(fmt.Sprint(percent), func(t *testing.T) {
			b := spillBudget(t)
			gateway := &compactionFlowGateway{}
			b.gateway, b.windowSize, b.events = gateway, 10000, events.NewOrderedDispatcher(zap.NewNop())
			b.writesWorkspace = func(string) bool { return true }
			messages := replayPrefix()
			for i := range 6 {
				messages = append(messages, exchange(fmt.Sprint(i), "shell", `{"command":"`+strings.Repeat("x", 3000)+`"}`, strings.Repeat("output", 1000))...)
			}
			b.prefetchSummary(messages, 1, 1<<20)
			if b.summaryFuture == nil {
				t.Fatal("fixture did not prefetch")
			}
			if err := b.summaryFuture.wait(context.Background()); err != nil {
				t.Fatal(err)
			}
			got, _ := b.compact(context.Background(), "s", messages, b.plan(messages, 1, false), contextWindowState{}, 10000, percent)
			if gateway.summaryCalls != 1 {
				t.Fatalf("summaryCalls=%d, want one reused prefetch", gateway.summaryCalls)
			}
			if estimatedMessageTokens(got) >= estimatedMessageTokens(messages) {
				t.Fatal("prefetch reuse prevented payload reduction")
			}
			if !strings.Contains(fmt.Sprint(got), "_archived_arguments") {
				t.Fatal("pinned arguments not archived")
			}
			if len(orphanedToolCallIDs(got)) != 0 {
				t.Fatal("prefetch/spill orphaned result")
			}
		})
	}
}
