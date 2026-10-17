package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/platform/events"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

// Fixed workloads track compaction cost across read sizes and path counts.
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
	}
	messages := []inference.Message{inference.SystemMessage("system"), inference.UserMessage("task")}
	state := contextWindowState{}
	for turn := range pinRunTurns {
		current = turn
		messages, state = budget.Prepare(context.Background(), "s", pinRunTurn(messages, turn, readBytes, paths), state)
	}
	return compactions, at
}

// TestReadPinCompactionRate reports the compaction count on two workloads.
func TestReadPinCompactionRate(t *testing.T) {
	const summaryBytes = 600
	// paths=20 revisits each path every 20 turns. paths=200 reads each path
	// once, modeling broad exploration with older reads in the summarizable middle.
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
