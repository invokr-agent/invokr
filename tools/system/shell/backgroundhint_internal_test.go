package shell

import (
	"strings"
	"testing"
)

func TestBackgroundHintPointsAtTmux(t *testing.T) {
	for _, line := range []string{"nohup python3 -m api &", "setsid ./server", "tmux new-session -d -s api", "sleep 5 &", "cd /tmp && nohup ./s"} {
		if backgroundHint(line) == "" {
			t.Fatalf("expected tmux hint for %q", line)
		}
	}
	for _, line := range []string{"echo hi && echo bye", "echo tmuxinator", "go test ./...", ""} {
		if hint := backgroundHint(line); hint != "" {
			t.Fatalf("unexpected hint for %q: %q", line, hint)
		}
	}
}

func TestBackgroundHintNamesTheConcreteCall(t *testing.T) {
	hint := backgroundHint("nohup ./server &")
	if !strings.Contains(hint, `"action":"open"`) {
		t.Fatalf("hint %q does not name the tmux open action", hint)
	}
}
