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

// #559: does #558 (read pin 4→16 KB, plus carrying pinned reads verbatim
// through summarization) raise the compaction rate on a workload whose reads
// sit in the newly pinned 4-16 KB band? Marathon M3 suggested roughly 2x on
// n=2 vs n=3, which the bench cannot resolve (score SD ≈ 1.7). This is the
// deterministic, no-model stand-in: a fixed 200-turn transcript replayed under
// three configurations, counting compactions.
//
// The three configurations separate the two mechanisms #558 bundled:
//
// Only the restored 4 KB cap is a shipped configuration on this branch, so
// that is what is reported. #558's own shape — a 16 KB cap under a 32 KB total
// budget, plus the compaction carry — is not reachable here; the numbers
// measured against main are quoted on the PR instead.
const (
	pinRunTurns      = 200
	pinRunPaths      = 20
	pinRunShellBytes = 2 << 10
	pinRunEditBytes  = 1 << 10
	pinRunProseBytes = 400
	pinRunWindow     = 64000
)

type pinRunGateway struct{ summary string }

func (*pinRunGateway) Begin(context.Context, BeginModelRequest) (ModelTurn, error) {
	return ModelTurn{}, nil
}

func (*pinRunGateway) Continue(context.Context, ContinueModelRequest) (ModelTurn, error) {
	return ModelTurn{}, nil
}

func (g *pinRunGateway) SummarizeConversation(context.Context, []inference.Message) (string, error) {
	return g.summary, nil
}

// pinRunTurn appends one assistant turn: a 12 KB read of a rotating path and a
// 2 KB shell result, the shape marathon M3 runs.
func pinRunTurn(messages []inference.Message, turn, readBytes, paths int) []inference.Message {
	readID, shellID, editID := fmt.Sprintf("r%d", turn), fmt.Sprintf("s%d", turn), fmt.Sprintf("e%d", turn)
	path := fmt.Sprintf("src/pkg%02d/file.go", turn%paths)
	calls := []inference.ToolCall{
		{ID: readID, Name: readFileToolName, Arguments: fmt.Sprintf(`{"path":%q}`, path)},
		{ID: shellID, Name: "shell", Arguments: `{"command":"go build ./..."}`},
		{ID: editID, Name: "edit", Arguments: fmt.Sprintf(`{"path":%q,"old":"a%d","new":"b%d"}`, path, turn, turn)},
	}
	return append(messages,
		inference.AssistantMessage(fmt.Sprintf("turn %d: inspecting %s. ", turn, path)+strings.Repeat("p", pinRunProseBytes), calls...),
		inference.ToolMessage(readID, path+"\n"+strings.Repeat("x", readBytes)),
		inference.ToolMessage(shellID, strings.Repeat("y", pinRunShellBytes)),
		inference.ToolMessage(editID, strings.Repeat("z", pinRunEditBytes)),
	)
}

// countCompactions replays the fixed transcript and returns how many
// compactions Prepare ran.
func countCompactions(t *testing.T, summaryBytes, readBytes, paths int) (int, []int) {
	t.Helper()
	var compactions int
	var at []int
	current := 0
	dispatcher := events.NewOrderedDispatcher(zap.NewNop(), events.HandlerFunc(func(_ context.Context, event events.Event) error {
		if event.Notice == nil {
			return nil
		}
		if event.Notice.Text == "context compaction in progress" {
			compactions++
			at = append(at, current)
		}
		return nil
	}))
	budget := &contextBudget{
		windowSize: pinRunWindow, events: dispatcher, logger: zap.NewNop(),
		gateway:         &pinRunGateway{summary: strings.Repeat("s", summaryBytes)},
		writesWorkspace: func(name string) bool { return name == "edit" },
		resultKeepTurns: constants.ToolResultKeepTurns, resultAgeTurns: constants.ToolResultAgeTurns,
	}
	messages := []inference.Message{inference.SystemMessage("system"), inference.UserMessage("task")}
	state := contextWindowState{}
	for turn := range pinRunTurns {
		current = turn
		messages, state = budget.Prepare(context.Background(), "s", pinRunTurn(messages, turn, readBytes, paths), state, nil)
	}
	return compactions, at
}

// TestReadPinCompactionRate reports the compaction count on two workloads. It
// is a report, not a threshold: the numbers that decided this revert came from
// the ndjson replay of recorded runs, where the 4 KB cap was never worse than
// a larger one. On main the 200-path workload compacts 108 times against
// single digits here — the compaction carry was the dominant effect.
func TestReadPinCompactionRate(t *testing.T) {
	const summaryBytes = 600
	// paths=20 re-reads every path every 20 turns, so a pinned read is always
	// fresh and sits in the retained tail. paths=200 reads each path once, the
	// broad-exploration shape, so pinned reads sit in the summarizable middle.
	for _, paths := range []int{pinRunPaths, pinRunTurns} {
		for _, readBytes := range []int{3 << 10, 6 << 10, 9 << 10, 12 << 10} {
			count, at := countCompactions(t, summaryBytes, readBytes, paths)
			t.Logf("paths=%3d read=%2d KB over %d turns: %d compactions at %v",
				paths, readBytes>>10, pinRunTurns, count, at)
		}
	}
	// Sensitivity: the stub summary is part of the post-compaction floor, so
	// vary its size to show the ranking does not depend on it.
	for _, summary := range []int{200, 6000} {
		count, _ := countCompactions(t, summary, 6<<10, pinRunTurns)
		t.Logf("paths=200 read=6 KB, %d-byte summary: %d compactions", summary, count)
	}
}
