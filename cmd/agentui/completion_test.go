package agentui

import (
	"context"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestCompletionRefreshTriggers(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		wantActive  bool
		wantFirst   string
	}{
		{name: "slash at start", value: "/mod", wantActive: true, wantFirst: "/mode"},
		{name: "slash mid-prompt is not a command", value: "read a/b"},
		{name: "trailing space closes popup", value: "/model "},
		{name: "empty input", value: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var c completion
			// Preload so no filesystem walk runs in the test.
			c.filesLoaded = true
			c.refresh(tc.value)
			if c.active() != tc.wantActive {
				t.Fatalf("active = %v, want %v (items %v)", c.active(), tc.wantActive, c.items)
			}
			if tc.wantFirst != "" && c.items[0] != tc.wantFirst {
				t.Fatalf("first = %q, want %q", c.items[0], tc.wantFirst)
			}
		})
	}
}

func TestCompletionFileTriggerAndSelection(t *testing.T) {
	c := completion{filesLoaded: true, files: []string{"cmd/agentui/model.go", "agent/runner.go", "README.md"}}
	c.refresh("look at @runner")
	if !c.active() || c.items[0] != "@agent/runner.go" {
		t.Fatalf("items = %v", c.items)
	}
	if got := c.selected("look at @runner"); got != "look at @agent/runner.go " {
		t.Fatalf("selected = %q", got)
	}
}

// The popup must not swallow a token the user is still typing mid-word.
func TestTrailingToken(t *testing.T) {
	for value, want := range map[string]string{
		"":         "",
		"abc":      "abc",
		"a b @c":   "@c",
		"a ":       "",
		"one\n@tw": "@tw",
	} {
		if got := trailingToken(value); got != want {
			t.Fatalf("trailingToken(%q) = %q, want %q", value, got, want)
		}
	}
}

// Enter accepts a highlighted slash suggestion, but never a file one.
func TestSubmitAcceptsSlashCompletionOnly(t *testing.T) {
	m := newModel(context.Background(), &fakeSession{}, nil, nil, nil, false, nil, nil)
	m.completion.filesLoaded = true
	m.completion.files = []string{"agent/runner.go"}

	m.input.SetValue("/h")
	m.completion.refresh(m.input.Value())
	if !m.completion.active() || m.completion.items[0] != "/help" {
		t.Fatalf("items = %v", m.completion.items)
	}
	m.submit()
	// /help renders into the popup overlay, so the transcript stays empty.
	if !m.overlay.IsOpen() || m.overlay.Title() != "help" {
		t.Fatalf("overlay = open:%v title:%q, want the help popup", m.overlay.IsOpen(), m.overlay.Title())
	}

	m.overlay.Close()
	m.input.SetValue("look at @runner")
	m.completion.refresh(m.input.Value())
	if !m.completion.active() {
		t.Fatal("file completion not active")
	}
	m.submit()
	if last := m.chatLog.messages[len(m.chatLog.messages)-1]; last.text != "look at @runner" {
		t.Fatalf("submitted %q, want the text as typed", last.text)
	}
}

func TestCompletionViewHighlightsCursor(t *testing.T) {
	c := completion{items: []string{"/a", "/b"}, cursor: 1}
	view := ansi.Strip(c.view())
	if !strings.Contains(view, "› /b") || !strings.Contains(view, "tab complete") {
		t.Fatalf("view = %q", view)
	}
	if !strings.Contains(view, "esc dismiss · 2") {
		t.Fatalf("view should count the suggestions: %q", view)
	}
}
