package agentui

import (
	"fmt"
	"strconv"
	"testing"
)

// benchChatModel builds a model whose transcript holds n messages in the mix
// a real session produces: user turns, settled markdown answers, and settled
// shell blocks with a few lines of output.
func benchChatModel(n int) *model {
	m := newTestModelWithHiddenLogs()
	for i := range n {
		switch i % 3 {
		case 0:
			m.chatLog.Append(message{role: "user", text: "prompt " + strconv.Itoa(i)})
		case 1:
			m.chatLog.Append(message{role: "assistant", text: "Answer **" + strconv.Itoa(i) + "** with `code` and\n\n- a list\n- of items"})
		default:
			m.chatLog.Append(message{
				role: "tool", toolID: "b" + strconv.Itoa(i), toolName: "shell",
				toolArgs: `{"command":"ls"}`, toolStatus: toolDone,
				toolOutput: "line one\nline two\nline three",
			})
		}
	}
	return m
}

// BenchmarkChatLogView measures one frame of the chat-log render path (the
// full transcript re-render syncChatViewport performs), idle and with a
// streaming tail mutating between frames. I4's numbers; see the plan doc.
func BenchmarkChatLogView(b *testing.B) {
	for _, n := range []int{200, 1000, 5000} {
		b.Run(fmt.Sprintf("idle_%d", n), func(b *testing.B) {
			m := benchChatModel(n)
			b.ResetTimer()
			for b.Loop() {
				m.syncChatViewport(false)
			}
		})
		b.Run(fmt.Sprintf("stream_%d", n), func(b *testing.B) {
			m := benchChatModel(n)
			m.chatLog.Append(message{role: "assistant", text: "partial", streaming: true})
			b.ResetTimer()
			for b.Loop() {
				m.chatLog.Last().text += " word"
				m.syncChatViewport(false)
			}
		})
	}
}
