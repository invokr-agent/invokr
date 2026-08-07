package agent

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	inference "github.com/rivt-ai/go-inference-router"
)

const (
	// agedResultPrefix marks a result this pass already collapsed, so a stub
	// is never re-stubbed and its line count stays honest.
	agedResultPrefix = "[elided "
	agedResultFormat = agedResultPrefix + "%s %s: %d lines; call again if needed]"
	// agedSpillFormat is the restorable stub: the full output is still on disk,
	// so the model can page it back instead of re-running the tool.
	agedSpillFormat  = agedResultPrefix + "%s %s: %d lines; full output in %s, read it]"
	agedArgumentsMax = 80
	// agedMinBytes is the size floor: a result this small collapses to a stub
	// of nearly the same length, so the rewrite buys no context back and only
	// costs the prompt-prefix cache.
	agedMinBytes = 200
	// pinnedReadMaxBytes caps which read_file result the "newest read per
	// path" rule pins: a big read would blow a 32k window on its own, so past
	// this size it ages normally like any other result. #558 raised this to
	// 16 KB; the replay of recorded runs showed the raise costs a compaction
	// per three runs with no measured benefit, and the cost saturates between
	// 4 and 8 KB, so it is back at 4 KB (#559).
	pinnedReadMaxBytes = 4096
	// pinnedReadBudgetBytes bounds all pinned reads together, newest paths
	// first: about 8k tokens, so a run that reads many small files still
	// compacts. Kept from #558 on headroom, not on evidence — it is inert on
	// every recorded run replayed for #559, but one of them pins 27 KB across
	// 17 paths, 87% of this ceiling, and with the cap back at 4 KB nothing
	// else bounds the total.
	pinnedReadBudgetBytes = 32 << 10
)

// agingCandidate is one tool result eligible to be collapsed: its position, the
// call that produced it, and how many assistant turns ago it landed.
type agingCandidate struct {
	index int
	call  inference.ToolCall
	turns int
}

// ageToolResults collapses tool results older than keepTurns assistant turns to
// a one-line stub, ahead of the compaction threshold: on a 30-call task the raw
// results outgrew the schemas long before 80% (#480).
//
// The pass is batched with hysteresis: nothing is edited until some eligible
// result is older than ageTurns, and then every eligible result older than
// keepTurns is collapsed in that one pass. Editing history every turn
// invalidates the provider's prompt-prefix cache every turn; batching keeps the
// prefix stable between triggers (cf. Anthropic context editing's
// clear_at_least, and the observation masking of arxiv 2508.21433). ageTurns
// must be at least keepTurns.
//
// Results under agedMinBytes are left alone, and a result the last assistant
// message referenced (its call id or an argument
// value) lives one extra turn, and the newest error result per tool is never
// aged, since the model needs it to recover; neither is the newest result of
// any tool, nor any result of a tool that writes the workspace ("never drop
// the last edit", ADR 0013). A result whose full output the
// gate spilled to a file gets a stub naming that file, so the content stays
// recoverable without re-running the tool. Returns the count collapsed.
func ageToolResults(messages []inference.Message, keepTurns, ageTurns, pinBudget int, spillRefs map[string]string, writesWorkspace func(string) bool) ([]inference.Message, int) {
	if keepTurns <= 0 {
		return messages, 0
	}
	candidates := agingCandidates(messages, keepTurns, pinBudget, writesWorkspace)
	if !agingTriggered(candidates, ageTurns) {
		return messages, 0
	}
	out := append([]inference.Message(nil), messages...)
	for _, candidate := range candidates {
		message := messages[candidate.index]
		out[candidate.index] = inference.ToolMessage(message.ToolCallID, agedStub(candidate.call, message.Content, spillRefs[candidate.call.ID]))
	}
	return out, len(candidates)
}

// agingCandidates lists the tool results the pass would collapse if it ran,
// newest first: those past keepTurns that no protection rule holds back.
func agingCandidates(messages []inference.Message, keepTurns, pinBudget int, writesWorkspace func(string) bool) []agingCandidate {
	calls := toolCallsByID(messages)
	protected, last := protectedResults(messages, calls, pinBudget, writesWorkspace), lastAssistantContent(messages)
	var candidates []agingCandidate
	turns := 0
	for index := len(messages) - 1; index >= 0; index-- {
		message := messages[index]
		if message.Role == inference.RoleAssistant {
			turns++
			continue
		}
		if message.Role != inference.RoleTool || protected[index] || !agingWorthwhile(message.Content) {
			continue
		}
		call := calls[message.ToolCallID]
		if turns <= keepTurns+referencedBonus(last, call) {
			continue
		}
		candidates = append(candidates, agingCandidate{index: index, call: call, turns: turns})
	}
	return candidates
}

// agingTriggered reports whether this pass should edit history at all: only
// once something is older than ageTurns, so the prompt prefix stays stable (and
// cacheable) across the turns in between. candidates are newest-first, so the
// oldest (and thus largest turns count) is the last element.
func agingTriggered(candidates []agingCandidate, ageTurns int) bool {
	return len(candidates) > 0 && candidates[len(candidates)-1].turns > ageTurns
}

// agingWorthwhile rejects results a stub would not meaningfully shrink, and
// stubs themselves, which must never be re-stubbed.
func agingWorthwhile(content string) bool {
	return len(content) >= agedMinBytes && !strings.HasPrefix(content, agedResultPrefix)
}

// agedStub is the one-line replacement for a collapsed result. When the gate
// spilled the full output to a file the stub points at it, so the model can
// read it back; otherwise all it can do is call the tool again.
func agedStub(call inference.ToolCall, content, spillRef string) string {
	arguments := strings.Join(strings.Fields(call.Arguments), " ")
	arguments = arguments[:min(len(arguments), agedArgumentsMax)]
	lines := strings.Count(content, "\n") + 1
	if spillRef != "" {
		return fmt.Sprintf(agedSpillFormat, call.Name, arguments, lines, spillRef)
	}
	return fmt.Sprintf(agedResultFormat, call.Name, arguments, lines)
}

// protectedResults indexes the results aging must never collapse: every result
// of a tool that writes the workspace, plus the most recent result and the most
// recent error result of each other tool — the model's live view of what that
// tool last did, and what it has to recover from — and the reads
// pinnedReadIndices keeps. In a mixed-tool run this protection moves as newer
// results land: releasing an older "newest per tool" (or "newest per path")
// result can make it eligible and push a pass over agingTriggered earlier than
// a single-tool run would.
func protectedResults(messages []inference.Message, calls map[string]inference.ToolCall, pinBudget int, writesWorkspace func(string) bool) map[int]bool {
	protected := pinnedReadIndices(messages, calls, 0, len(messages), pinBudget)
	newest, failed := make(map[string]int), make(map[string]int)
	for index, message := range messages {
		if message.Role != inference.RoleTool {
			continue
		}
		call := calls[message.ToolCallID]
		switch {
		case writesWorkspace != nil && writesWorkspace(call.Name):
			protected[index] = true
		case len(readPaths(call)) > 0:
			// Reads are pinned per path, not per tool: the newest read
			// call overall says nothing about the file the model is editing.
		default:
			newest[call.Name] = index
			if strings.HasPrefix(message.Content, `{"error":`) {
				failed[call.Name] = index
			}
		}
	}
	for _, indexes := range []map[string]int{newest, failed} {
		for _, index := range indexes {
			protected[index] = true
		}
	}
	return protected
}

// pinnedReadIndices is the "newest read per path stays in view" rule aging
// applies: within [lo, hi), the newest read result of each path, skipping
// any over pinnedReadMaxBytes, taken newest first until budget bytes
// are spent. Anchor misses on the marathon runs were 5% of edits while the
// file's read was in context and 42% once aging dropped it (#553). A batched
// read (paths) covers several files in one result; every one of its paths
// maps to that same message index, so a batched read isn't invisible to this
// rule the way a single-path read isn't (#577 read-batching).
func pinnedReadIndices(messages []inference.Message, calls map[string]inference.ToolCall, lo, hi, budget int) map[int]bool {
	newest := make(map[string]int)
	for index := lo; index < hi; index++ {
		message := messages[index]
		if message.Role != inference.RoleTool || len(message.Content) >= pinnedReadMaxBytes {
			continue
		}
		for _, path := range readPaths(calls[message.ToolCallID]) {
			newest[path] = index
		}
	}
	// A batched read's paths all point at the same index; dedupe before
	// charging the budget so one multi-file result isn't billed once per path.
	indices := slices.Compact(slices.Sorted(maps.Values(newest)))
	pinned := make(map[int]bool, len(indices))
	for _, index := range slices.Backward(indices) {
		if budget -= len(messages[index].Content); budget < 0 {
			break
		}
		pinned[index] = true
	}
	return pinned
}

// toolCallsByID indexes every tool call in the transcript by id, so a tool
// result can be joined back to the call that produced it.
func toolCallsByID(messages []inference.Message) map[string]inference.ToolCall {
	calls := make(map[string]inference.ToolCall)
	for _, message := range messages {
		for _, call := range message.ToolCalls {
			calls[call.ID] = call
		}
	}
	return calls
}

// readPaths is the path argument(s) of a read call - the single path, or
// every path of a batched paths call - or nil for any other call.
func readPaths(call inference.ToolCall) []string {
	if call.Name != readFileToolName {
		return nil
	}
	var arguments struct {
		Path  string   `json:"path"`
		Paths []string `json:"paths"`
	}
	_ = json.Unmarshal([]byte(call.Arguments), &arguments)
	if len(arguments.Paths) > 0 {
		return arguments.Paths
	}
	if arguments.Path != "" {
		return []string{arguments.Path}
	}
	return nil
}

func lastAssistantContent(messages []inference.Message) string {
	for index := len(messages) - 1; index >= 0; index-- {
		if messages[index].Role == inference.RoleAssistant {
			return messages[index].Content
		}
	}
	return ""
}

// referencedBonus is the extra turn a result earns when the last assistant
// message mentions its call id or one of its string arguments (a path, a
// command) — a cheap proxy for "the model is still working from it".
func referencedBonus(last string, call inference.ToolCall) int {
	if last == "" {
		return 0
	}
	if call.ID != "" && strings.Contains(last, call.ID) {
		return 1
	}
	var arguments map[string]any
	_ = json.Unmarshal([]byte(call.Arguments), &arguments)
	for _, value := range arguments {
		if text, ok := value.(string); ok && len(text) >= 3 && strings.Contains(last, text) {
			return 1
		}
	}
	return 0
}
