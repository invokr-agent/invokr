//go:build e2e

package e2e

import (
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/support/testutil/clitest"
	"github.com/invokr-agent/invokr/support/testutil/providertest"
)

func TestC18_TUIInterruption(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("BLOCKED: tmux not installed")
	}
	provider := providertest.NewScripted(t,
		providertest.CompletionStep{StallAfterChunks: 0, StallDuration: 30 * time.Second},
		providertest.NativeToolCallStep("c18-shell", "shell", map[string]any{"command": "sh", "args": []string{"-c", "echo c18"}}),
	)
	defer provider.Close()
	home := newHome(t, provider.URL())
	rewriteConfig(t, &home, func(app *config.AppConfig) { app.Shell.AllowedCommands = []string{"sh"} })
	s := newTUITmux(t, home, provider.URL())

	s.waitFor(regexp.MustCompile(`agent started`), 10*time.Second)
	s.submit("slow request")
	s.waitFor(regexp.MustCompile(`slow request`), 5*time.Second)
	s.send("C-c")
	s.waitFor(regexp.MustCompile(`stopped`), 5*time.Second)
	if !s.alive() {
		t.Fatal("TUI exited after interrupting active request")
	}

	s.submit("request approval")
	s.waitFor(regexp.MustCompile(`APPROVAL|approval required|approval requested|approval queue \(\d+ pending\)`), 5*time.Second)
	s.send("C-c")
	s.waitFor(regexp.MustCompile(`(?s)stopped.*stopped`), 5*time.Second)
	if !s.alive() {
		t.Fatal("TUI exited after interrupting pending approval")
	}

	s.quit(5 * time.Second)
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}

type tuiTmux struct {
	t       *testing.T
	socket  string
	session string
}

func newTUITmux(t *testing.T, home testHome, providerURL string, extraEnv ...string) *tuiTmux {
	t.Helper()
	s := &tuiTmux{t: t, socket: fmt.Sprintf("invokr-e2e-%d", time.Now().UnixNano()), session: "c18"}
	t.Cleanup(func() { _ = exec.Command("tmux", "-L", s.socket, "kill-server").Run() })
	args := []string{"-L", s.socket, "new-session", "-d", "-x", "100", "-y", "30", "-s", s.session,
		"env", "HOME=" + home.state.HomeDir, "INVOKR_HOME=" + home.state.HomeDir,
		"INVOKR_TOOLBOX_DIR=" + home.state.ToolboxDir, "INVOKR_MEMORY_DIR=" + home.state.MemoryDir,
		"INVOKR_TOKF_DISABLED=true", "NO_COLOR=1", "TERM=xterm-256color"}
	args = append(args, extraEnv...)
	args = append(args, clitest.Binary(t), "--base-url="+providerURL, "agent")
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		t.Fatalf("launch TUI in tmux: %v: %s", err, out)
	}
	return s
}

func (s *tuiTmux) send(keys ...string) {
	s.t.Helper()
	args := append([]string{"-L", s.socket, "send-keys", "-t", s.session}, keys...)
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		s.t.Fatalf("tmux send-keys: %v: %s", err, out)
	}
}

func (s *tuiTmux) submit(text string) {
	s.t.Helper()
	args := []string{"-L", s.socket, "send-keys", "-t", s.session, "-l", text}
	if out, err := exec.Command("tmux", args...).CombinedOutput(); err != nil {
		s.t.Fatalf("tmux send text: %v: %s", err, out)
	}
	s.send("Enter")
}

func (s *tuiTmux) capture() string {
	out, _ := exec.Command("tmux", "-L", s.socket, "capture-pane", "-p", "-t", s.session, "-S", "-100").CombinedOutput()
	return strings.TrimSpace(string(out))
}

func (s *tuiTmux) alive() bool {
	return exec.Command("tmux", "-L", s.socket, "has-session", "-t", s.session).Run() == nil
}

func (s *tuiTmux) waitFor(re *regexp.Regexp, timeout time.Duration) {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pane := s.capture(); re.MatchString(pane) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.t.Fatalf("pane never matched %s:\n%s", re, s.capture())
}

// quit presses C-c until the TUI exits. A single C-c is "stop" while a turn
// is still winding down and "quit" when idle; on slow runners the final reply
// can render before the turn is marked done, so one press may only stop it.
func (s *tuiTmux) quit(timeout time.Duration) {
	s.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !s.alive() {
			return
		}
		s.send("C-c")
		time.Sleep(200 * time.Millisecond)
	}
	s.t.Fatalf("TUI did not exit:\n%s", s.capture())
}
