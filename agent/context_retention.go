package agent

import (
	"maps"
	"slices"

	inference "github.com/rivt-ai/go-inference-router"
)

const (
	// maxRetainedMessages caps how far the budgeted tail may grow; picked, not
	// measured (see the plan's open questions).
	maxRetainedMessages = 32
	maxPinnedResults    = 8
	maxPinnedTokens     = 8192 // includes owners, script arguments and sibling results
)

// retentionPlan is the single source of truth for what compaction may touch.
// messages[:prefixEnd] and messages[tailStart:] stay byte-identical,
// [prefixEnd, tailStart) is the summarizable middle, and pinned indices inside
// it must survive elision verbatim.
type retentionPlan struct {
	prefixEnd, tailStart int
	// pinned is the hard set: newest write results, kept even by the
	// overflow elision fallback (ADR 0013). Reads are not carried through
	// summarization: doing so raised the post-compaction floor and roughly
	// doubled the compaction rate, which is itself what drops reads (#559).
	pinned map[int]bool
}

func (p retentionPlan) summarizable() bool { return p.tailStart > p.prefixEnd }

// trimTo narrows the plan to what a prefetched future already summarized: its
// segment must be a prefix of this plan's middle, and the rest of that middle
// then stays verbatim rather than being summarized again (#513). The future is
// nil when it is not usable, including when a later edit to the transcript
// would leave a tool message at future.end orphaned by the trimmed plan.
func (p retentionPlan) trimTo(messages []inference.Message, future *summaryFuture, writes func(string) bool) (retentionPlan, *summaryFuture) {
	if future != nil && future.end > p.prefixEnd && future.end <= p.tailStart && summaryKey(messages[p.prefixEnd:future.end]) == future.key {
		if trimmed := snappedPlan(messages, p.prefixEnd, future.end, writes); trimmed.tailStart == future.end {
			return trimmed, future
		}
	}
	return p, nil
}

// planRetention computes the compaction boundary: floor, then budgeted growth,
// then the pairing snap.
func planRetention(messages []inference.Message, tailBudget int64, scale float64, writes func(string) bool) retentionPlan {
	prefixEnd := min(2, len(messages))
	start := max(prefixEnd, len(messages)-minRetainedMessages)
	return snappedPlan(messages, prefixEnd, growTailToBudget(messages, start, prefixEnd, tailBudget, scale), writes)
}

// chunkRetention plans the oldest ≈budget tokens as the segment, whatever the
// tail's size: what a prefetch can afford when the default segment does not
// fit the headroom (#513).
func chunkRetention(messages []inference.Message, budget int64, scale float64, writes func(string) bool) retentionPlan {
	prefixEnd := min(2, len(messages))
	end := prefixEnd
	for end < len(messages) && calibratedMessageTokens(messages[prefixEnd:end+1], scale) <= budget {
		end++
	}
	return snappedPlan(messages, prefixEnd, end, writes)
}

// snappedPlan finishes a plan at start. The pairing snap runs last so nothing
// can undo it: the tail can never begin at a tool message whose owning
// assistant tool_calls message would be summarized away.
func snappedPlan(messages []inference.Message, prefixEnd, start int, writes func(string) bool) retentionPlan {
	start = snapTailToPair(messages, start, prefixEnd)
	return retentionPlan{
		prefixEnd: prefixEnd, tailStart: start,
		pinned: pinnedResultIndices(messages, prefixEnd, start, writes),
	}
}

// growTailToBudget widens the tail backwards while its calibrated token
// estimate stays under tailBudget and its length under maxRetainedMessages.
// A zero budget (window size unknown) keeps the flat floor.
func growTailToBudget(messages []inference.Message, start, prefixEnd int, budget int64, scale float64) int {
	for start > prefixEnd && budget > 0 &&
		len(messages)-start < maxRetainedMessages &&
		calibratedMessageTokens(messages[start-1:], scale) <= budget {
		start--
	}
	return start
}

// pinnedResultIndices keeps, inside the summarizable middle, the newest tool
// result per distinct name+arguments key for tools that write the workspace —
// "never drop the last edit" — bounded to the newest maxPinnedResults.
func pinnedResultIndices(messages []inference.Message, prefixEnd, tailStart int, writes func(string) bool) map[int]bool {
	if writes == nil {
		return nil
	}
	calls, newest := toolCallsByID(messages), make(map[string]int)
	for index := prefixEnd; index < tailStart; index++ {
		if call, ok := calls[messages[index].ToolCallID]; ok && writes(call.Name) {
			newest[call.Name+call.Arguments] = index
		}
	}
	pinned, indexes := make(map[int]bool, len(newest)), slices.Sorted(maps.Values(newest))
	// At most eight candidates: use the same pairing closure as compaction,
	// so sibling results and shared owners are counted exactly once.
	for _, index := range slices.Backward(indexes[max(0, len(indexes)-maxPinnedResults):]) {
		pinned[index] = true
		plan := retentionPlan{prefixEnd: prefixEnd, tailStart: tailStart, pinned: pinned}
		if estimatedMessageTokens(pinnedMessages(messages, plan)) > maxPinnedTokens {
			delete(pinned, index)
		}
	}
	return pinned
}

// callOwners maps every tool call id to the index of the assistant message that
// issued it.
func callOwners(messages []inference.Message) map[string]int {
	owners := make(map[string]int)
	for index, message := range messages {
		for _, call := range message.ToolCalls {
			if call.ID != "" {
				owners[call.ID] = index
			}
		}
	}
	return owners
}

// snapTailToPair widens the tail backwards until no retained tool message is
// orphaned from the assistant message that issued its call. It never narrows.
func snapTailToPair(messages []inference.Message, start, floor int) int {
	owners := callOwners(messages)
	for index := start; index < len(messages); index++ {
		id := messages[index].ToolCallID
		if id == "" {
			continue
		}
		if owner, ok := owners[id]; ok && owner < start {
			start = max(floor, owner)
			// Rescan the span the widened tail just took in: it can hold
			// tool messages whose owners sit even further back. The loop's
			// index++ resumes at start+1, which is safe because start is
			// either inside the sys/user prefix floor or an assistant
			// message — neither carries a ToolCallID.
			index = start
		}
	}
	return start
}

// orphanedToolCallIDs returns the ids referenced by tool messages that have no
// owning assistant message inside the same slice. OpenAI-shaped providers reject
// such a request outright, so this is a checked post-condition of compaction
// rather than an assumption.
func orphanedToolCallIDs(messages []inference.Message) []string {
	owners := callOwners(messages)
	var orphans []string
	for _, message := range messages {
		if _, ok := owners[message.ToolCallID]; message.ToolCallID != "" && !ok {
			orphans = append(orphans, message.ToolCallID)
		}
	}
	return orphans
}

// pinnedMessages returns the carried tool results from the summarizable middle
// with their owning assistant messages and sibling results — paired both ways.
// pinnedWithOwners is a set of result indices plus the assistant messages
// whose tool calls own them: a pinned edit is its arguments as much as its
// result, so neither may be elided or blanked.
func pinnedWithOwners(messages []inference.Message, pinned map[int]bool) map[int]bool {
	owners, keep := callOwners(messages), make(map[int]bool, 2*len(pinned))
	for index := range pinned {
		keep[index] = true
		keep[owners[messages[index].ToolCallID]] = true
	}
	// A missing owner reads as index 0, which the prefix never exposes.
	delete(keep, 0)
	return keep
}

func pinnedMessages(messages []inference.Message, plan retentionPlan) []inference.Message {
	owners, keep := callOwners(messages), pinnedWithOwners(messages, plan.pinned)
	var out []inference.Message
	for index := plan.prefixEnd; index < plan.tailStart; index++ {
		if keep[index] || keep[owners[messages[index].ToolCallID]] {
			out = append(out, messages[index])
		}
	}
	return out
}
