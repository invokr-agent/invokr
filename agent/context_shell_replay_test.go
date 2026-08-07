package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/invokr-ai/invokr/config"
	"go.uber.org/zap"
	"sync"
	"testing"
)

// X7071 is a size-only reconstruction of the archived b60 M4 failure.
// Unlike the older M3 replay, shell MUST have its production write capability.
// This measures retention mechanics, not the model's counterfactual score.
// 1.4 is the older M3 calibration; 2.6 reflects X7071's pre-compaction
// engine/byte-estimate ratio on its data-heavy turns (see fixture README).
// The real admission gate runs before Prepare; raw event output is not the
// model-facing payload. Summary content and tool bodies remain size proxies.
func TestM4ShellReplayHeadroom(t *testing.T) {
	for _, name := range []string{"m4-x7071.json", "m4-y8191.json"} {
		for _, scale := range []float64{1.4, 2.6} {
			t.Run(fmt.Sprintf("%s/scale%.1f", name, scale), func(t *testing.T) {
				run := loadReplayRun(t, name)
				var counts replayCounts
				b := replayBudget(&counts, replayWindow)
				gate := newResultGate(config.AppConfig{}.WithDefaults().Agent, "replay", zap.NewNop())
				gate.spill = &fileSpillStore{dir: t.TempDir(), pruneOnce: &sync.Once{}}
				b.spill = gate.spill
				refs := make(map[string]string)
				b.writesWorkspace = func(name string) bool {
					return name == "shell" || name == "write_file" || name == "edit_file" || name == "apply_patch"
				}
				messages, state := replayPrefix(), contextWindowState{}
				streak, longest := 0, 0
				var peak int64
				for _, exchange := range replayMessages(run.Segments[0]) {
					for i := 1; i < len(exchange); i++ {
						call := exchange[0].ToolCalls[i-1]
						exchange[i].Content, refs[call.ID] = gate.cap(call.Name, json.RawMessage(call.Arguments), exchange[i].Content)
					}
					before := counts.complete
					messages, state = b.Prepare(context.Background(), "m4", append(messages, exchange...), state, refs)
					state = replayUsage(state, messages, scale)
					if counts.complete > before {
						streak++
						longest = max(longest, streak)
						peak = max(peak, contextTokens(messages, state))
					} else {
						streak = 0
					}
					if ids := orphanedToolCallIDs(messages); len(ids) != 0 {
						t.Fatalf("orphaned results: %v", ids)
					}
				}
				t.Logf("%s: %s; longest consecutive compactions=%d; peak post-compaction tokens=%d", run.Run, counts, longest, peak)
				if longest > 2 || peak > replayWindow*60/100 {
					t.Fatalf("M4 compaction storm: longest=%d, post-compaction=%d; want <=2 and <=60%% window", longest, peak)
				}
			})
		}
	}
}
