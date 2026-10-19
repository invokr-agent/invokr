package agentui

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/invokr-ai/invokr/platform/turn"
	inference "github.com/rivt-ai/go-inference-router"
)

type imageSession struct {
	fakeSession
	images []inference.ContentBlock
}

func (s *imageSession) SendImages(_ context.Context, text string, images []inference.ContentBlock) (turn.AgentResponse, error) {
	s.calls, s.images = append(s.calls, text), images
	return turn.AgentResponse{Message: "a cat"}, nil
}

func TestPastedImagePath(t *testing.T) {
	dir := t.TempDir()
	png := filepath.Join(dir, "shot.png")
	if err := os.WriteFile(png, []byte{1}, 0o600); err != nil {
		t.Fatal(err)
	}
	for paste, want := range map[string]string{
		png:                          png,
		"'" + png + "'\n":            png,
		"file://" + png:              png,
		filepath.Join(dir, "no.png"): "",
		filepath.Join(dir, "a.txt"):  "",
		"look at this " + png:        "",
	} {
		if got := pastedImagePath(paste); got != want {
			t.Errorf("pastedImagePath(%q) = %q, want %q", paste, got, want)
		}
	}
}

// A dropped image path becomes an [image N] marker, and the image rides on the
// next submit through SendImages rather than as pasted text.
func TestPastedImageIsSentWithPrompt(t *testing.T) {
	png := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(png, []byte{1, 2}, 0o600); err != nil {
		t.Fatal(err)
	}
	session := &imageSession{}
	m := newModel(context.Background(), session, nil, nil, fakeLogSource{}, false, nil, nil)
	m.width, m.height = 120, 24

	_, cmd := m.Update(tea.PasteMsg{Content: png})
	if cmd == nil {
		t.Fatal("image path paste produced no command")
	}
	m.Update(cmd())
	m.input.InsertString("what is this?")
	if got := m.input.Value(); got != "[image 1] what is this?" {
		t.Fatalf("input = %q, want the image marker", got)
	}
	_, cmd = m.submit()
	cmd().(tea.BatchMsg)[0]() // the send
	if len(session.images) != 1 || string(session.images[0].Media.Data) != "\x01\x02" || len(m.images) != 0 {
		t.Fatalf("sent images = %#v, pending = %d; want the pasted image sent once", session.images, len(m.images))
	}
}
