package agentui

import (
	"testing"

	"github.com/invokr-agent/invokr/platform/events"
)

// TestStreamRetryDropsPartialReply: a reply cut short and re-issued must not
// leave its partial text in the transcript once per attempt.
func TestStreamRetryDropsPartialReply(t *testing.T) {
	var c chatLog
	delta := func(text string) events.Event {
		return events.Event{Type: events.EventAssistantDelta, Delta: &events.AssistantDeltaPayload{Text: text}}
	}
	retry := events.Event{Type: events.EventNotice, Notice: &events.NoticePayload{Text: events.StreamRetryNotice}}
	for range 3 {
		c.ApplyEvent(delta("Hello from the"))
		c.ApplyEvent(retry)
	}
	c.ApplyEvent(delta("Hello from the fake server."))
	if c.Len() != 2 {
		t.Fatalf("got %d messages, want the retry line and one reply: %+v", c.Len(), c.messages)
	}
	if got := c.messages[0].text; got != streamRetryLine+" (3)" {
		t.Fatalf("retry line = %q", got)
	}
	if got := c.messages[1].text; got != "Hello from the fake server." {
		t.Fatalf("reply = %q", got)
	}
}
