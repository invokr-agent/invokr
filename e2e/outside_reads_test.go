//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/support/testutil/providertest"
)

// With workspace.reads=ask a read beyond the workspace queues an approval;
// "a" grants its directory, so a second read there runs without asking.
func TestOutsideReadAsksThenDirectoryGrantCovers(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("BLOCKED: tmux not installed")
	}
	other := t.TempDir()
	first, second := filepath.Join(other, "a.md"), filepath.Join(other, "b.md")
	for path, body := range map[string]string{first: "alpha-outside-739", second: "beta-outside-739"} {
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	provider := providertest.NewScripted(t,
		providertest.NativeToolCallStep("o-one", "read", map[string]any{"paths": []string{first}}),
		providertest.ReplyStep("first read done"),
		providertest.NativeToolCallStep("o-two", "read", map[string]any{"paths": []string{second}}),
		providertest.ReplyStep("second read done"),
	)
	defer provider.Close()
	home := newHome(t, provider.URL())
	rewriteConfig(t, &home, func(app *config.AppConfig) { app.Workspace.Reads = config.ReadsAsk })
	s := newTUITmux(t, home, provider.URL())

	s.waitFor(regexp.MustCompile(`agent started`), 10*time.Second)
	s.submit("read the outside file")
	s.waitFor(regexp.MustCompile(`outside_read`), 10*time.Second)
	s.send("a")
	s.waitFor(regexp.MustCompile(`first read done`), 10*time.Second)
	s.waitFor(regexp.MustCompile(`e2e-scripted · \d+ms`), 10*time.Second)
	s.submit("read the other one")
	s.waitFor(regexp.MustCompile(`second read done`), 10*time.Second)

	data, err := json.Marshal(provider.Snapshot().CompletionRequests)
	if err != nil || !strings.Contains(string(data), "alpha-outside-739") || !strings.Contains(string(data), "beta-outside-739") {
		t.Fatalf("outside contents never reached the model (marshal error: %v)", err)
	}
	s.quit(5 * time.Second)
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}
