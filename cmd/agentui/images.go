package agentui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/invokr-ai/invokr/platform/turn"
	inference "github.com/rivt-ai/go-inference-router"
)

// imageTypes are the formats vision servers take as data: URLs.
var imageTypes = map[string]string{".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg", ".gif": "image/gif", ".webp": "image/webp"}

type imageSender interface {
	SendImages(context.Context, string, []inference.ContentBlock) (turn.AgentResponse, error)
}

// imagePastedMsg carries an attached image, an error, or - when the clipboard
// held no image - the ctrl+v key to hand back to the input as a text paste.
type imagePastedMsg struct {
	image    inference.ContentBlock
	err      error
	fallback *tea.KeyPressMsg
}

// pastedImagePath is the image file a paste names (dropping a file on the
// terminal pastes its path), or "".
func pastedImagePath(text string) string {
	path := strings.TrimPrefix(strings.Trim(strings.TrimSpace(text), `'"`), "file://")
	if _, ok := imageTypes[strings.ToLower(filepath.Ext(path))]; !ok {
		return ""
	}
	if info, err := os.Stat(path); err != nil || info.IsDir() {
		return ""
	}
	return path
}

func readImageCmd(path string) tea.Cmd {
	return func() tea.Msg {
		data, err := os.ReadFile(path)
		return imagePastedMsg{image: inference.ImageData(imageTypes[strings.ToLower(filepath.Ext(path))], data), err: err}
	}
}

// clipboardImageCmd reads a PNG from the system clipboard (wl-paste, xclip,
// pngpaste); with none there, ctrl+v falls back to the input's text paste.
func clipboardImageCmd(key tea.KeyPressMsg) tea.Cmd {
	return func() tea.Msg {
		for _, argv := range [][]string{{"wl-paste", "--no-newline", "--type", "image/png"}, {"xclip", "-selection", "clipboard", "-t", "image/png", "-o"}, {"pngpaste", "-"}} {
			if out, err := exec.Command(argv[0], argv[1:]...).Output(); err == nil && len(out) > 0 {
				return imagePastedMsg{image: inference.ImageData("image/png", out)}
			}
		}
		return imagePastedMsg{fallback: &key}
	}
}

// updateImageMsg takes an image-path paste and the attach result; other
// pastes fall through to the input.
func (m *model) updateImageMsg(msg tea.Msg) (tea.Model, tea.Cmd, bool) {
	switch msg := msg.(type) {
	case tea.PasteMsg:
		if path := pastedImagePath(msg.Content); path != "" {
			return m, readImageCmd(path), true
		}
	case imagePastedMsg:
		updated, cmd := m.updateImagePasted(msg)
		return updated, cmd, true
	}
	return nil, nil, false
}

func (m *model) updateImagePasted(msg imagePastedMsg) (tea.Model, tea.Cmd) {
	if msg.fallback != nil {
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(*msg.fallback)
		return m, cmd
	}
	if msg.err != nil {
		m.commandMessage("image: " + msg.err.Error())
		return m, nil
	}
	m.images = append(m.images, msg.image)
	m.input.InsertString(fmt.Sprintf("[image %d] ", len(m.images)))
	return m, nil
}

// send routes a turn with attached images through SendImages.
func (m *model) send(ctx context.Context, text string, images []inference.ContentBlock) (turn.AgentResponse, error) {
	if len(images) == 0 {
		return m.session.Send(ctx, text)
	}
	sender, ok := m.session.(imageSender)
	if !ok {
		return turn.AgentResponse{}, errors.New("this session cannot send images")
	}
	return sender.SendImages(ctx, text, images)
}
