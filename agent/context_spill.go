package agent

import (
	"encoding/json"
	"slices"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

// Spill only at compaction boundaries: ordinary turns keep a stable cache
// prefix. The latest exchange remains verbatim for immediate follow-up work.
// Archived calls are historical records, never instructions to execute again.
func (b *contextBudget) spillHistory(messages []inference.Message, keepNewest bool) []inference.Message {
	if b.spill == nil {
		return messages
	}
	out := slices.Clone(messages)
	stop := len(messages)
	if keepNewest {
		stop = snapTailToPair(messages, max(2, len(messages)-2), 2)
	}
	for i := 2; i < stop; i++ {
		message := &out[i]
		if message.Role == inference.RoleTool {
			message.Content = b.archivePayload("result", message.Content, false)
		}
		message.ToolCalls = slices.Clone(message.ToolCalls)
		for j := range message.ToolCalls {
			call := &message.ToolCalls[j]
			call.Arguments = b.archivePayload(call.Name+"-arguments", call.Arguments, true)
		}
		if message.Content != messages[i].Content || !slices.Equal(message.ToolCalls, messages[i].ToolCalls) {
			message.Blocks = nil // project typed blocks from the rewritten canonical fields
		}
	}
	return out
}

func (b *contextBudget) archivePayload(name, text string, arguments bool) string {
	if len(text) <= 2048 {
		return text
	}
	// Store the complete envelope, not just stdout: both streams, status and
	// original arguments remain recoverable even after another compaction.
	ref, err := b.spill.put(name, []byte(text))
	if err != nil {
		b.logger.Warn("cannot archive context payload", zap.Error(err))
		return text
	}
	if arguments {
		fields := map[string]any{"_archived_arguments": ref, "_notice": "Historical call; use read with paths containing the archive filename for exact arguments. Do not rerun this placeholder."}
		var original map[string]json.RawMessage
		_ = json.Unmarshal([]byte(text), &original)
		for _, key := range []string{"path", "cwd"} {
			if value := original[key]; len(value) > 0 && len(value) <= 512 {
				fields[key] = value
			}
		}
		encoded, _ := json.Marshal(fields)
		return string(encoded)
	}
	fields, key, _, ok := envelopeLongestField(text)
	if ok {
		// Preserve small metadata such as exit_code, timed_out and cwd. Large
		// fields live in the complete archive referenced by the preview.
		for field, value := range fields {
			if len(value) > 256 {
				delete(fields, field)
			}
		}
		fields[key], _ = json.Marshal(spillStub(text, ref, 512))
		encoded, _ := json.Marshal(fields)
		return string(encoded)
	}
	return spillStub(text, ref, 512)
}
