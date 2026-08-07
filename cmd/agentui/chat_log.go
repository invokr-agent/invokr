package agentui

import (
	"slices"
	"strings"
	"time"

	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/surface/ui"
)

type toolStatus int

const (
	toolRunning toolStatus = iota
	toolDone
	toolFailed
)

type message struct {
	role, text string
	streaming  bool

	// meta (role == "assistant") is the provenance line under the answer:
	// model, elapsed, tokens.
	meta responseMeta

	// tool fields (role == "tool"): a single block keyed by toolID that mutates
	// in place from running -> done/failed.
	toolID, toolName, toolArgs string
	toolStatus                 toolStatus
	// toolProgressLine is the latest incremental status a running tool
	// reported (EventToolProgress); shown beside the live elapsed.
	toolProgressLine    string
	toolStartedAt       time.Time
	toolDuration        time.Duration
	toolBytes           int
	toolCode, toolError string
	// toolDiff is the unified diff of an applied edit, rendered under the tool
	// line so ordinary edits are visible without an approval prompt.
	toolDiff string
	// toolOutput is the recorded result as the model saw it (capped upstream).
	// Per-tool renderers show a tail of it collapsed, all of it expanded.
	toolOutput string
	// expanded marks a settled block the user unfolded to its full recorded
	// result. It survives streaming updates because the block mutates in place.
	expanded bool

	// rendered memoizes this message's frame output at renderedWidth (I4): a
	// settled message renders identically every frame, and re-running glamour
	// over the whole transcript made a frame cost ~0.4ms per message. Live
	// messages (streaming answers, running tool blocks) are never memoized,
	// and events that mutate a block replace the struct, clearing the memo.
	rendered      string
	renderedWidth int
}

// live reports whether the message still changes frame to frame — a streaming
// answer or a running tool block (whose spinner and elapsed tick).
func (m message) live() bool {
	return m.streaming || (m.role == "tool" && m.text == "" && m.toolStatus == toolRunning)
}

// chatLog owns the chat transcript: user/assistant/tool/help/error messages,
// including the tool-event state machine that mutates a running tool block in
// place as it completes rather than appending a new line per event. The
// model composes chatLog's rendered content with intro/sending/queue/help
// chrome and owns the viewport auto-follow-bottom scroll logic around it.
type chatLog struct {
	messages     []message
	toolProgress progressMode
}

func newChatLog(toolProgress progressMode) chatLog { return chatLog{toolProgress: toolProgress} }

// Append adds a message to the transcript.
func (c *chatLog) Append(msg message) { c.messages = append(c.messages, msg) }

// Clear empties the transcript (e.g. /clear).
func (c *chatLog) Clear() { c.messages = nil }

// Len returns the number of messages in the transcript.
func (c *chatLog) Len() int { return len(c.messages) }

// Last returns a pointer to the last message, or nil if the log is empty.
func (c *chatLog) Last() *message {
	if len(c.messages) == 0 {
		return nil
	}
	return &c.messages[len(c.messages)-1]
}

// ApplyEvent updates the chat with a tool/notice/assistant-delta event,
// mutating the matching block in place (keyed by tool-call ID) rather than
// appending a new line per event.
func (c *chatLog) ApplyEvent(ev events.Event) {
	if c.applyAssistantDelta(ev) {
		return
	}
	if ev.Type == events.EventNotice && ev.Notice != nil {
		c.Append(message{role: "tool", text: ev.Notice.Text})
		return
	}
	if ev.Tool == nil {
		return
	}
	t := *ev.Tool
	if ev.Type == events.EventToolProgress {
		c.setToolProgress(t)
		return
	}
	if ev.Type == events.EventToolStarted {
		started := ev.Timestamp
		if started.IsZero() {
			started = time.Now()
		}
		c.Append(message{role: "tool", toolID: t.ID, toolName: t.Name,
			toolArgs: t.Arguments, toolStatus: toolRunning, toolStartedAt: started})
		return
	}

	idx := c.finishToolMessage(t, ev.Type == events.EventToolFailed)

	switch c.toolProgress {
	case progressOff:
		// Drop the finished block; only live spinners are shown. copy+zero the
		// freed tail so the removed message's strings can be GC'd.
		copy(c.messages[idx:], c.messages[idx+1:])
		c.messages[len(c.messages)-1] = message{}
		c.messages = c.messages[:len(c.messages)-1]
	case progressNew:
		// ponytail: collapse repeats — drop earlier finished blocks of the same
		// tool, keeping the latest. Not strictly consecutive-only; good enough.
		c.dropFinishedToolBlocks(t.Name, idx)
	}
}

// setToolProgress attaches an incremental status line to the newest still-
// running block for this tool. A progress event carries no call ID (the plugin
// never learns one), so the running block is matched by name.
func (c *chatLog) setToolProgress(t events.ToolPayload) {
	for i := len(c.messages) - 1; i >= 0; i-- {
		if c.messages[i].toolStatus == toolRunning && c.messages[i].toolName == t.Name {
			c.messages[i].toolProgressLine = t.Progress
			return
		}
	}
}

// finishToolMessage settles the block for a completed/failed tool call,
// mutating the running block in place when one exists, and returns its index.
func (c *chatLog) finishToolMessage(t events.ToolPayload, failed bool) int {
	msg := message{
		role: "tool", toolID: t.ID, toolName: t.Name, toolArgs: t.Arguments,
		toolStatus: toolDone, toolDuration: t.Duration, toolBytes: t.OutputBytes,
		toolCode: t.Code, toolError: t.Error, toolOutput: t.Output,
	}
	if failed {
		msg.toolStatus = toolFailed
	}
	idx := c.findToolMessage(t.ID)
	if idx >= 0 {
		// Keep the started event's name/args: some emitters omit them on completion.
		prev := c.messages[idx]
		msg.toolStartedAt, msg.toolName, msg.toolArgs, msg.expanded = prev.toolStartedAt, prev.toolName, prev.toolArgs, prev.expanded
	}
	if msg.toolStatus == toolDone && t.Code == "" {
		// A flagged result may not have applied, so it gets no diff.
		msg.toolDiff = appliedPatchDiff(msg.toolName, msg.toolArgs)
	}
	if idx < 0 {
		// No running block to update (e.g. event arrived out of order); append one.
		c.Append(msg)
		return len(c.messages) - 1
	}
	c.messages[idx] = msg
	return idx
}

func (c *chatLog) applyAssistantDelta(ev events.Event) bool {
	if ev.Type != events.EventAssistantDelta || ev.Delta == nil {
		return false
	}
	if last := c.Last(); last != nil && last.role == "assistant" && last.streaming {
		last.text += ev.Delta.Text
	} else {
		c.Append(message{role: "assistant", text: ev.Delta.Text, streaming: true})
	}
	return true
}

func (c *chatLog) findToolMessage(id string) int {
	if id == "" {
		return -1
	}
	return slices.IndexFunc(c.messages, func(m message) bool { return m.role == "tool" && m.toolID == id })
}

func (c *chatLog) dropFinishedToolBlocks(name string, keepIdx int) {
	out := c.messages[:0]
	for i := range c.messages {
		msg := c.messages[i]
		if i != keepIdx && msg.role == "tool" && msg.toolName == name && msg.toolStatus != toolRunning {
			continue
		}
		out = append(out, msg)
	}
	// Zero the freed tail so dropped messages' strings can be GC'd.
	for i := len(out); i < len(c.messages); i++ {
		c.messages[i] = message{}
	}
	c.messages = out
}

// Content renders the message transcript. spinnerFrame is the current
// spinner glyph used by running tool blocks; width is the target render
// width. The caller composes this with intro/sending/queue/help chrome.
func (c *chatLog) Content(width int, spinnerFrame string) string {
	content, _ := c.ContentRanges(width, spinnerFrame, -1)
	return content
}

// blockRange is one foldable tool block's [start,end) line range within the
// rendered content, plus the index of the message it renders. The fold
// hit-testing (mouse.go) and focus navigation consume these.
type blockRange struct {
	msg        int
	start, end int
}

// ContentRanges renders the transcript and records the line range of every
// foldable (settled tool) block. focused is the message index to mark with
// the transcript-focus cursor, or -1 for none.
func (c *chatLog) ContentRanges(width int, spinnerFrame string, focused int) (string, []blockRange) {
	var b strings.Builder
	var ranges []blockRange
	line := 0
	for i := range c.messages {
		rendered := c.renderMessage(i, width, spinnerFrame)
		if i == focused {
			rendered = markFocused(rendered)
		}
		height := strings.Count(rendered, "\n") + 1
		if c.messages[i].foldable() {
			ranges = append(ranges, blockRange{msg: i, start: line, end: line + height})
		}
		b.WriteString(rendered + "\n\n")
		line += height + 2
	}
	return b.String(), ranges
}

// renderMessage renders one message, serving settled messages from their
// per-message memo. The focus marker is layered on afterwards so it never
// enters the cache.
func (c *chatLog) renderMessage(i, width int, spinnerFrame string) string {
	msg := &c.messages[i]
	if !msg.live() && msg.rendered != "" && msg.renderedWidth == width {
		return msg.rendered
	}
	var mb strings.Builder
	writeMessage(&mb, *msg, width, spinnerFrame)
	rendered := mb.String()
	if !msg.live() {
		msg.rendered, msg.renderedWidth = rendered, width
	}
	return rendered
}

// InvalidateRender drops every message's render memo — for theme or syntax
// changes, which restyle text the memo would otherwise keep in old colors.
func (c *chatLog) InvalidateRender() {
	for i := range c.messages {
		c.messages[i].rendered = ""
	}
}

// foldable reports whether a message is a settled tool block the user can
// expand and collapse.
func (m message) foldable() bool {
	return m.role == "tool" && m.text == "" && m.toolStatus != toolRunning
}

// markFocused appends the transcript-focus cursor to a block's header line.
func markFocused(rendered string) string {
	head, rest, split := strings.Cut(rendered, "\n")
	head += " " + ui.Invokr.Accent.Render("◀")
	if !split {
		return head
	}
	return head + "\n" + rest
}

// answerIndent aligns an answer under the accent bar of the user turn above it.
const answerIndent = "  "

// indentBlock prefixes every line of text with indent, leaving an empty block
// empty so it does not become a line of trailing spaces.
func indentBlock(text, indent string) string {
	if text == "" {
		return ""
	}
	return indent + strings.ReplaceAll(text, "\n", "\n"+indent)
}

func writeMessage(b *strings.Builder, msg message, width int, spinnerFrame string) {
	width = max(20, width)
	switch msg.role {
	case "user":
		// The design marks a user turn with an accent bar rather than a divider,
		// so consecutive turns read as separate without chrome between them.
		bar := ui.Invokr.Accent.Render("› ")
		indent := strings.Repeat(" ", 4)
		b.WriteString(bar + ui.Invokr.Accent.Render(strings.ReplaceAll(wrapText(msg.text, width-4), "\n", "\n"+indent)))
	case "assistant":
		// The design indents an answer under the accent bar of the turn it
		// answers, which is what makes a user line and its response read as one
		// group rather than as two unrelated blocks.
		body := width - len(answerIndent)
		if msg.streaming {
			b.WriteString(indentBlock(renderPartialMarkdown(msg.text, body), answerIndent))
			return
		}
		b.WriteString(indentBlock(renderResponse(msg.text, msg.meta, body), answerIndent))
	case "tool":
		if msg.text == "" {
			b.WriteString(renderToolBlock(msg, spinnerFrame, width))
			return
		}
		// Non-block tool lines (approval requests/decisions) keep plain text.
		if strings.HasPrefix(msg.text, "approval denied") {
			b.WriteString(ui.Invokr.Danger.Render(wrapText(msg.text, width)))
		} else {
			b.WriteString(ui.Invokr.Muted.Render(wrapText(msg.text, width)))
		}
	case "error":
		b.WriteString(ui.Invokr.Danger.Render("error: ") + wrapText(msg.text, width-7))
	}
}
