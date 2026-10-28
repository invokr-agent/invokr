//go:build e2e

package e2e

import (
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/support/testutil/providertest"
)

// bellFlag reports tmux's per-window bell flag, which flips to 1 when the
// application inside rang the terminal bell since the window was last active.
func bellFlag(t *testing.T, s *tuiTmux) string {
	t.Helper()
	out, err := exec.Command("tmux", "-L", s.socket, "display-message", "-p", "-t", s.session,
		"#{window_bell_flag}").CombinedOutput()
	if err != nil {
		t.Fatalf("tmux display-message: %v: %s", err, out)
	}
	return strings.TrimSpace(string(out))
}

func waitBellFlag(t *testing.T, s *tuiTmux, want string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if bellFlag(t, s) == want {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// prepareBellTmux arms tmux bell monitoring and moves focus to a fresh shell
// window: tmux only latches window_bell_flag for windows that are not the
// active one. The tuiTmux helper is retargeted at the TUI's own window so
// send/capture keep going to the app, not the focused shell.
func prepareBellTmux(t *testing.T, s *tuiTmux) {
	t.Helper()
	for _, args := range [][]string{
		{"-L", s.socket, "set-option", "-g", "monitor-bell", "on"},
		{"-L", s.socket, "set-option", "-g", "visual-bell", "off"},
		{"-L", s.socket, "new-window", "-t", s.session},
	} {
		if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %v: %v: %s", args, err, out)
		}
	}
	s.session += ":0"
}

// With ui.notify=bell (INVOKR_NOTIFY=bell), a finished turn must ring the
// terminal bell.
func TestTUINotifyBellOnTurnCompletion(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("BLOCKED: tmux not installed")
	}
	provider := providertest.NewScripted(t,
		providertest.CompletionStep{Content: "notify done"},
	)
	defer provider.Close()
	home := newHome(t, provider.URL())
	s := newTUITmux(t, home, provider.URL(), "INVOKR_NOTIFY=bell")
	prepareBellTmux(t, s)

	s.waitFor(regexp.MustCompile(`agent started`), 10*time.Second)
	s.submit("ring when done")
	s.waitFor(regexp.MustCompile(`notify done`), 10*time.Second)
	if !waitBellFlag(t, s, "1", 3*time.Second) {
		t.Fatalf("bell never rang after turn completion:\n%s", s.capture())
	}
}

// With the default mode (off), the same flow must stay silent.
func TestTUINotifyOffStaysSilent(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("BLOCKED: tmux not installed")
	}
	provider := providertest.NewScripted(t,
		providertest.CompletionStep{Content: "silent done"},
	)
	defer provider.Close()
	home := newHome(t, provider.URL())
	s := newTUITmux(t, home, provider.URL())
	prepareBellTmux(t, s)

	s.waitFor(regexp.MustCompile(`agent started`), 10*time.Second)
	s.submit("stay silent")
	s.waitFor(regexp.MustCompile(`silent done`), 10*time.Second)
	if waitBellFlag(t, s, "1", 1*time.Second) {
		t.Fatalf("bell rang with notifications off:\n%s", s.capture())
	}
}
