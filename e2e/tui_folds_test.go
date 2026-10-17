//go:build e2e

package e2e

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

// A settled shell block collapses to its output tail plus a fold indicator;
// alt+x + enter expands it to the full recorded result.
func TestTUITranscriptFolds(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("BLOCKED: tmux not installed")
	}
	provider := providertest.NewScripted(t,
		providertest.NativeToolCallStep("fold-shell", "shell",
			map[string]any{"command": "sh", "args": []string{"-c", "seq 1 12"}}),
		providertest.CompletionStep{Content: "fold turn done"},
	)
	defer provider.Close()
	home := newHome(t, provider.URL())
	rewriteConfig(t, &home, func(app *config.AppConfig) { app.Shell.AllowedCommands = []string{"sh"} })
	s := newTUITmux(t, home, provider.URL())

	s.waitFor(regexp.MustCompile(`agent started`), 10*time.Second)
	s.submit("run seq")
	s.waitFor(regexp.MustCompile(`APPROVAL\s+step \d+ of \d+`), 10*time.Second)
	s.send("a")
	// Collapsed: 12 output lines, 5-line tail => 7 hidden behind the fold.
	s.waitFor(regexp.MustCompile(`▸ 7 more lines`), 10*time.Second)
	s.waitFor(regexp.MustCompile(`fold turn done`), 10*time.Second)

	s.send("M-r")
	s.waitFor(regexp.MustCompile(`Execution history`), 5*time.Second)
	s.send("q")
	s.send("M-x")   // transcript focus starts on the settled shell block
	s.send("Enter") // toggle -> expanded
	s.waitFor(regexp.MustCompile(`(?s)\b1\b.*\b2\b.*\b12\b`), 5*time.Second)
	if pane := s.capture(); strings.Contains(pane, "▸ 7 more lines") {
		t.Fatalf("fold indicator still visible after expanding:\n%s", pane)
	}
	s.send("Escape")
	s.quit(5 * time.Second)
	provider.AssertClean(t)
}
