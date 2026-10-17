//go:build e2e

package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/support/testutil/providertest"
)

func TestTUIClearStartsNewSession(t *testing.T) {
	provider := providertest.NewScripted(t,
		providertest.CompletionStep{Content: "old turn done"},
		providertest.NativeToolCallStep("fresh-read", "read", map[string]any{"paths": []string{"README.md"}}),
		providertest.CompletionStep{Content: "new turn done"},
		providertest.CompletionStep{Content: "third turn done"},
	)
	defer provider.Close()
	home := newHome(t, provider.URL())
	s := newTUITmux(t, home, provider.URL())
	s.waitFor(regexp.MustCompile(`agent started`), 10*time.Second)
	s.submit("old-conversation-marker-739")
	s.waitFor(regexp.MustCompile(`old turn done`), 10*time.Second)
	s.submit("/clear")
	s.waitFor(regexp.MustCompile(`new session started`), 10*time.Second)
	s.submit("new conversation")
	s.waitFor(regexp.MustCompile(`new turn done`), 10*time.Second)
	s.send("M-r")
	s.waitFor(regexp.MustCompile(`read "README.md"`), 5*time.Second)
	s.send("q")
	requests := provider.Snapshot().CompletionRequests
	data, err := json.Marshal(requests[len(requests)-1])
	if err != nil || strings.Contains(string(data), "old-conversation-marker-739") {
		t.Fatalf("new session retained old conversation (marshal error: %v)", err)
	}
	s.submit("/clear")
	for deadline := time.Now().Add(10 * time.Second); strings.Contains(s.capture(), "new turn done"); {
		if time.Now().After(deadline) {
			t.Fatal("second clear left the previous transcript visible")
		}
		time.Sleep(50 * time.Millisecond)
	}
	s.waitFor(regexp.MustCompile(`new session started`), 10*time.Second)
	time.Sleep(250 * time.Millisecond) // let the restarted TUI attach before typing
	s.submit("third conversation")
	s.waitFor(regexp.MustCompile(`third turn done`), 10*time.Second)
	requests = provider.Snapshot().CompletionRequests
	data, err = json.Marshal(requests[len(requests)-1])
	if err != nil || strings.Contains(string(data), "new conversation") || strings.Contains(string(data), "old-conversation-marker-739") {
		t.Fatalf("repeated clear retained previous conversations (marshal error: %v)", err)
	}
	var paths []string
	var transcripts string
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		paths, err = filepath.Glob(filepath.Join(home.app.Memory.Dir, "sessions", "*.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		transcripts = ""
		for _, path := range paths {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			transcripts += string(data)
		}
		if len(paths) == 3 && strings.Contains(transcripts, "old-conversation-marker-739") && strings.Contains(transcripts, "new conversation") && strings.Contains(transcripts, "third conversation") {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(paths) != 3 || !strings.Contains(transcripts, "old-conversation-marker-739") || !strings.Contains(transcripts, "new conversation") || !strings.Contains(transcripts, "third conversation") {
		t.Fatalf("want three preserved conversations in three session files, got %v: %s", paths, transcripts)
	}
	s.quit(5 * time.Second)
	provider.AssertClean(t)
	provider.AssertExhausted(t)
}
