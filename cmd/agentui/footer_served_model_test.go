package agentui

import (
	"context"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/platform/turn"
)

// servingSession is a session whose backend serves a model the configured name
// never mentions — the router pin the footer used to hide.
type servingSession struct {
	configured, served string
}

func (s *servingSession) Send(ctx context.Context, text string) (turn.AgentResponse, error) {
	return turn.AgentResponse{}, nil
}

func (s *servingSession) SetModel(name string) { s.configured = name }

func (s *servingSession) Model() string { return s.configured }

func (s *servingSession) ServedModel() string { return s.served }

// TestFooterShowsServedModel: the model cell names what answered, and falls back
// to the configured name until the backend reports one.
func TestFooterShowsServedModel(t *testing.T) {
	served := "qwen3.8-flash-next-coder-iq1_m"
	configured := "cyber-tiel/ornith"
	serving := servingSession{configured: configured, served: served}
	m := &model{session: &serving}
	if got := m.modelName(); got != served {
		t.Fatalf("model cell = %q, want the served model %q", got, served)
	}
	silent := servingSession{configured: configured}
	unreported := &model{session: &silent}
	if got := unreported.modelName(); got != configured {
		t.Fatalf("model cell = %q, want the configured name until the backend reports one", got)
	}
	row := statusline{folder: "invokr", branch: "main"}.view(m.modelName(), "", "", 120)
	if !strings.Contains(row, served) {
		t.Fatalf("statusline = %q, want the served model", row)
	}
}
