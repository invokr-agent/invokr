package agentui

import (
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/platform/events"
)

func TestRenderToolDenialIncludesReason(t *testing.T) {
	line := renderToolEvent(events.Event{Type: events.EventToolFailed, Tool: &events.ToolPayload{
		Name: "shell", Code: "denied", Error: "approval denied for shell: standing denial test-rule",
	}})
	if !strings.Contains(line, "auto-denied: standing denial test-rule") {
		t.Fatalf("denial reason missing: %s", line)
	}
}

func TestRenderUserDenialIsNotLabeledAutomatic(t *testing.T) {
	line := renderToolEvent(events.Event{Type: events.EventToolFailed, Tool: &events.ToolPayload{
		Name: "shell", Code: "denied", Error: "approval denied for shell: denied by user",
	}})
	if strings.Contains(line, "auto-denied") || !strings.Contains(line, "denied: denied by user") {
		t.Fatalf("wrong denial label: %s", line)
	}
}
