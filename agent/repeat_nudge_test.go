package agent

import (
	"strings"
	"testing"
)

// The repeat nudge must steer the model to a different call, not to stopping:
// small models generalise a bare "stop" into abandoning the tool entirely.
func TestRepeatedToolCallNudgeSteersInsteadOfStopping(t *testing.T) {
	for _, want := range []string{
		"That exact call was already made",
		"Vary it instead",
		"use a different tool for the same goal",
	} {
		if !strings.Contains(repeatedToolCallNudge, want) {
			t.Fatalf("repeat nudge missing %q:\n%s", want, repeatedToolCallNudge)
		}
	}
	for _, unwanted := range []string{"Stop repeating it", "do not retry"} {
		if strings.Contains(repeatedToolCallNudge, unwanted) {
			t.Fatalf("repeat nudge still tells the model to stop (%q):\n%s", unwanted, repeatedToolCallNudge)
		}
	}
}
