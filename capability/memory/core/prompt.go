package core

import (
	"cmp"
	"github.com/invokr-ai/invokr/support/strutil"
	"slices"
	"strings"
)

const (
	// RecalledMemoryOpenFence and RecalledMemoryCloseFence delimit recalled
	// memory as data in the system prompt so its content cannot be read as
	// instructions.
	RecalledMemoryOpenFence  = "<recalled-memory>"
	RecalledMemoryCloseFence = "</recalled-memory>"
	// recalledMemoryBrokenFence replaces any literal close fence found in
	// stored content so the fence cannot be closed early.
	recalledMemoryBrokenFence = "<\\/recalled-memory>"
	// MemoryTruncationMarker is appended to the block that crosses the prompt
	// budget boundary.
	MemoryTruncationMarker = "[…memory truncated]"
)

// FenceRecalledMemory escapes any literal close fence in content and wraps it
// in the recalled-memory fences.
func FenceRecalledMemory(content string) string {
	content = strings.ReplaceAll(content, RecalledMemoryCloseFence, recalledMemoryBrokenFence)
	return RecalledMemoryOpenFence + "\n" + content + "\n" + RecalledMemoryCloseFence
}

// ApplyBudget stable-sorts blocks by Priority descending, then keeps blocks
// (in that order) whose combined Content length fits within budget bytes.
// The block that crosses the boundary is truncated with
// MemoryTruncationMarker; later blocks are dropped. A non-positive budget
// disables the guard.
func (blocks PromptBlocks) ApplyBudget(budget int) PromptBlocks {
	if budget <= 0 {
		return blocks
	}
	sorted := slices.Clone(blocks)
	slices.SortStableFunc(sorted, func(a, b PromptBlock) int { return cmp.Compare(b.Priority, a.Priority) })
	out := make(PromptBlocks, 0, len(sorted))
	used := 0
	for _, block := range sorted {
		if used+len(block.Content) <= budget {
			out = append(out, block)
			used += len(block.Content)
			continue
		}
		remaining := budget - used - len(MemoryTruncationMarker)
		if remaining > 0 {
			block.Content = strutil.TruncateUTF8(block.Content, remaining) + MemoryTruncationMarker
			out = append(out, block)
		}
		break
	}
	return out
}

type PromptRequest struct {
	SessionID   string `json:"session_id,omitempty"`
	UserMessage string `json:"user_message,omitempty"`
	BudgetChars int    `json:"budget_chars,omitempty"`
}

type PromptBlock struct {
	Title    string `json:"title,omitempty"`
	Content  string `json:"content,omitempty"`
	Priority int    `json:"priority,omitempty"`
	Source   Source `json:"source,omitempty"`
}

type PromptBlocks []PromptBlock

func (blocks PromptBlocks) RenderMemoryBlocks() string {
	if len(blocks) == 0 {
		return ""
	}

	var b strings.Builder

	for _, block := range blocks {
		title := strings.TrimSpace(block.Title)
		content := strings.TrimSpace(block.Content)

		if content == "" {
			continue
		}

		if title != "" {
			b.WriteString("## ")
			b.WriteString(title)
			b.WriteString("\n\n")
		}

		b.WriteString(content)
		b.WriteString("\n\n")
	}

	return strings.TrimSpace(b.String())
}
