package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/platform/events"
	turnpkg "github.com/invokr-ai/invokr/platform/turn"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

// A deterministic replay of recorded marathon transcripts through
// contextBudget.Prepare. Where TestReadPinCompactionRate invents a workload,
// this one replays a real run: the recorded tool sequence, the recorded result
// and argument sizes, the recorded assistant prose lengths, segment by
// segment, against a stub summarizer. No model, no GPU, ~0.3 s.
//
// It exists because the marathon bench cannot resolve a context-management
// change on its own — M3's score SD is ≈1.7 and compactions/run is confounded
// with run length — while the synthetic harness is not faithful enough to
// predict real behaviour. Built for #559, kept as the general instrument: any
// future change to aging, retention or compaction can be measured here first,
// and TestMarathonReplayCompactionRate is a cheap guard against a change that
// silently raises the post-compaction floor.
//
// What it measures: the mechanism against a fixed transcript. What it cannot
// measure: the feedback loop. The recorded transcript is what the model did
// under one build; a different build would have shown it different context and
// it would have done something else. These are not bench predictions.
//
// Fixtures come from testdata/replay/extract.sh, run over
// ~/.cache/invokr-marathon/work/<run>/<task>-r1. They keep, per tool call, the
// turn, tool name, path, argument byte count and result byte count, plus the
// recorded tokens_evaluated trace; contents are replaced by filler of the
// recorded length, since every mechanism under test keys off sizes and paths,
// never off the text.

type replayCall struct {
	Turn      int    `json:"t"`
	Name      string `json:"n"`
	Path      string `json:"p"`
	ArgBytes  int    `json:"a"`
	ByteCount int    `json:"b"`
}

type replaySegment struct {
	Seg int `json:"seg"`
	// Tokens is the recorded tokens_evaluated trace, one entry per completion:
	// the engine's own prompt count, which the replay is calibrated against.
	Tokens []int64        `json:"tokens"`
	Prose  map[string]int `json:"prose"`
	Calls  []replayCall   `json:"calls"`
}

type replayRun struct {
	Run      string          `json:"run"`
	Segments []replaySegment `json:"segments"`
}

const (
	// replaySummaryBytes is the stub summary's size: the recorded runs'
	// summarizer completions were 350-512 tokens.
	replaySummaryBytes = 1600
	// replayOverheadTokens is the non-transcript part of the prompt — system
	// prompt plus tool schemas — read off the recorded runs' first completion
	// (tokens_evaluated 5422 against an almost empty transcript).
	replayOverheadTokens = 5422
	// replayWindow is the window the recorded runs actually ran under: a 64k
	// pin against a 64k engine, less the 4k summary headroom provider_context
	// reserves ("effective_window_tokens": 61440).
	replayWindow = 61440
	// replayScale converts the local byte estimate into engine tokens. The
	// reconstruction is lossy — reasoning text is not recorded, gate wrappers
	// and chat-template markup are not modelled — so the raw estimate runs
	// under the engine's own count. TestMarathonReplayCalibration measures the
	// ratio turn by turn against the recorded trace and puts the median at
	// 1.29-1.45 on the long segments; 1.40 is the fit. It is the harness's one
	// free parameter, and TestMarathonReplayScaleSensitivity sweeps it.
	replayScale = 1.40
)

// replayFixtures are three recorded M3-product-ts runs. They were originally
// one per #559 arm, which is why the names mention #558; as workloads they are
// simply three real M3 transcripts.
var replayFixtures = []string{"m3-pre558.json", "m3-head558.json", "m3-fixc.json"}

// replayBaseline is what each fixture compacts on current dev at replayScale.
// It is a pinned measurement, not a target: a change that moves it is not
// necessarily wrong, but it is never accidental, and the first question about
// any such change is whether the post-compaction floor moved.
var replayBaseline = map[string]int{"m3-pre558.json": 1, "m3-head558.json": 1, "m3-fixc.json": 1}

func loadReplayRun(t *testing.T, name string) replayRun {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "replay", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	var run replayRun
	if err := json.Unmarshal(raw, &run); err != nil {
		t.Fatalf("parse fixture %s: %v", name, err)
	}
	if len(run.Segments) == 0 {
		t.Fatalf("fixture %s has no segments", name)
	}
	return run
}

// filler is deterministic padding of exactly n bytes, prefixed with lead when
// it fits so a reconstructed read still names its path.
func filler(lead string, n int) string {
	if n <= 0 {
		return ""
	}
	if len(lead)+1 <= n {
		return lead + "\n" + strings.Repeat("x", n-len(lead)-1)
	}
	return strings.Repeat("x", n)
}

// replayArguments rebuilds a call's arguments at the recorded byte length: the
// real path (pinning and aging key off it) plus padding standing in for the
// edit bodies and shell commands that made up the rest.
func replayArguments(call replayCall, id string) string {
	head := fmt.Sprintf(`{"path":%q,"id":%q,"pad":"`, call.Path, id)
	pad := max(0, call.ArgBytes-len(head)-2)
	return head + strings.Repeat("a", pad) + `"}`
}

// callPath pulls the path argument out of any call. readPath is read_file-only
// by design, and the edit classification needs the same field.
func callPath(call inference.ToolCall) string {
	var args struct {
		Path string `json:"path"`
	}
	if json.Unmarshal([]byte(call.Arguments), &args) != nil {
		return ""
	}
	return args.Path
}

// replayMessages reconstructs one segment's transcript: per turn one assistant
// message carrying that turn's calls and its recorded prose length, followed by
// the results.
func replayMessages(segment replaySegment) [][]inference.Message {
	var turns [][]inference.Message
	for start := 0; start < len(segment.Calls); {
		end := start
		for end < len(segment.Calls) && segment.Calls[end].Turn == segment.Calls[start].Turn {
			end++
		}
		turn := segment.Calls[start].Turn
		calls, results := make([]inference.ToolCall, 0, end-start), make([]inference.Message, 0, end-start)
		for index, call := range segment.Calls[start:end] {
			id := fmt.Sprintf("t%d-%d", turn, index)
			calls = append(calls, inference.ToolCall{ID: id, Name: call.Name, Arguments: replayArguments(call, id)})
			results = append(results, inference.ToolMessage(id, filler(call.Path, call.ByteCount)))
		}
		prose := filler("", segment.Prose[strconv.Itoa(turn)])
		turns = append(turns, append([]inference.Message{inference.AssistantMessage(prose, calls...)}, results...))
		start = end
	}
	return turns
}

// replayCounts is what the harness reports. complete counts the "compaction
// complete" notice, not "compaction in progress": Prepare emits the latter on
// every attempt and the former only on success, so counting the substring
// "context compaction" double-counts.
type replayCounts struct{ complete, failed, attempts int }

func (c replayCounts) String() string {
	return fmt.Sprintf("%d complete (%d attempts, %d failed)", c.complete, c.attempts, c.failed)
}

func countReplayNotices(counts *replayCounts) events.Dispatcher {
	return events.NewOrderedDispatcher(zap.NewNop(), events.HandlerFunc(func(_ context.Context, event events.Event) error {
		if event.Notice == nil {
			return nil
		}
		switch text := event.Notice.Text; {
		case text == "context compaction in progress":
			counts.attempts++
		case strings.HasPrefix(text, "context compaction complete"):
			counts.complete++
		case strings.HasPrefix(text, "context summarization failed"):
			counts.failed++
		}
		return nil
	}))
}

func replayBudget(counts *replayCounts, window int64) *contextBudget {
	return &contextBudget{
		windowSize: window, events: countReplayNotices(counts), logger: zap.NewNop(),
		gateway:         &pinRunGateway{summary: strings.Repeat("s", replaySummaryBytes)},
		writesWorkspace: func(name string) bool { return name == "edit_file" || name == "apply_patch" },
		resultKeepTurns: constants.ToolResultKeepTurns, resultAgeTurns: constants.ToolResultAgeTurns,
	}
}

func replayPrefix() []inference.Message {
	return []inference.Message{inference.SystemMessage("system prompt"), inference.UserMessage("task")}
}

// replayCompactions replays every segment of a recorded run. Each segment was
// its own session on the recorded run, so the counts sum across them.
//
// The provider is modelled the way the runner sees it: after every turn the
// replay feeds updateProviderUsage a prompt count of the system/schema overhead
// plus scale x the transcript estimate, so the trigger runs on
// provider-calibrated tokens exactly as in production.
func replayCompactions(t *testing.T, run replayRun, window int64, scale float64) replayCounts {
	t.Helper()
	var counts replayCounts
	for _, segment := range run.Segments {
		budget := replayBudget(&counts, window)
		messages, state := replayPrefix(), contextWindowState{}
		for _, turn := range replayMessages(segment) {
			messages, state = budget.Prepare(context.Background(), "s", append(messages, turn...), state, nil)
			state = replayUsage(state, messages, scale)
		}
	}
	return counts
}

func replayUsage(state contextWindowState, messages []inference.Message, scale float64) contextWindowState {
	prompt := replayOverheadTokens + int64(scale*float64(estimatedMessageTokens(messages)))
	return updateProviderUsage(state, turnpkg.TokenUsage{Available: true, LatestPromptTokens: prompt}, messages, messages)
}

func replayCallCount(run replayRun) int {
	calls := 0
	for _, segment := range run.Segments {
		calls += len(segment.Calls)
	}
	return calls
}

// TestMarathonReplayCompactionRate is the regression guard: how often current
// code compacts on three real transcripts. A change to aging, retention or
// compaction that raises the post-compaction floor shows up here as a higher
// count, cheaply and deterministically, which is what neither the bench nor
// the synthetic harness delivers.
func TestMarathonReplayCompactionRate(t *testing.T) {
	for _, fixture := range replayFixtures {
		run := loadReplayRun(t, fixture)
		counts := replayCompactions(t, run, replayWindow, replayScale)
		t.Logf("%-16s %d tool calls, %d segments: %s",
			strings.TrimSuffix(fixture, ".json"), replayCallCount(run), len(run.Segments), counts)
		if counts.complete != replayBaseline[fixture] {
			t.Fatalf("%s: %d compactions, baseline %d. If the change was deliberate, re-pin replayBaseline and say in the commit what moved the post-compaction floor",
				fixture, counts.complete, replayBaseline[fixture])
		}
	}
}

// TestMarathonReplayScaleSensitivity sweeps the one fitted parameter. The
// compaction count is a threshold-crossing count, so it moves in integer steps;
// this shows how close the baseline sits to a boundary.
func TestMarathonReplayScaleSensitivity(t *testing.T) {
	for _, fixture := range replayFixtures {
		run := loadReplayRun(t, fixture)
		var row []string
		for _, scale := range []float64{1.20, 1.30, replayScale, 1.50, 1.60} {
			row = append(row, fmt.Sprintf("%.2f=%d", scale, replayCompactions(t, run, replayWindow, scale).complete))
		}
		t.Logf("%-16s scale sweep %s", strings.TrimSuffix(fixture, ".json"), strings.Join(row, " "))
	}
}

// TestMarathonReplayCalibration reports how the reconstructed transcript's
// token estimate tracks the engine's own tokens_evaluated count, turn by turn,
// up to each segment's first compaction — after which the two histories
// diverge and the comparison is meaningless. It fixes replayScale and makes
// the fit auditable rather than asserted.
func TestMarathonReplayCalibration(t *testing.T) {
	for _, fixture := range replayFixtures {
		run := loadReplayRun(t, fixture)
		for _, segment := range run.Segments {
			ratios := segmentRatios(t, segment)
			if len(ratios) == 0 {
				continue
			}
			slices.Sort(ratios)
			t.Logf("%-16s seg %d: %d comparable turns, engine/estimate ratio min=%.2f median=%.2f max=%.2f",
				strings.TrimSuffix(fixture, ".json"), segment.Seg, len(ratios),
				ratios[0], ratios[len(ratios)/2], ratios[len(ratios)-1])
		}
	}
}

// segmentRatios replays one segment with compaction out of reach and returns,
// per turn, the engine count over the replay's own estimate of the same
// prompt. It stops at the first recorded drop in tokens_evaluated, which is
// where the recorded run compacted.
func segmentRatios(t *testing.T, segment replaySegment) []float64 {
	t.Helper()
	var counts replayCounts
	budget := replayBudget(&counts, 1<<30)
	messages, state := replayPrefix(), contextWindowState{}
	var ratios []float64
	for index, turn := range replayMessages(segment) {
		messages, state = budget.Prepare(context.Background(), "s", append(messages, turn...), state, nil)
		if index >= len(segment.Tokens) || (index > 0 && segment.Tokens[index] < segment.Tokens[index-1]) {
			break
		}
		if estimate := estimatedMessageTokens(messages); estimate > 0 {
			ratios = append(ratios, float64(segment.Tokens[index]-replayOverheadTokens)/float64(estimate))
		}
	}
	return ratios
}
