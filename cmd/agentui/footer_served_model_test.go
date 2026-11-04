package agentui

import (
	"context"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/platform/turn"
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

// TestResponseFooterNamesServedModel: the provenance line under an answer names
// the model that answered, not the configured pin the router never serves.
func TestResponseFooterNamesServedModel(t *testing.T) {
	served, configured := "ornith-9b-mtp-kl-Q8_0", "cyber-tiel/ornith"
	serving := servingSession{configured: configured, served: served}
	m := &model{session: &serving}
	line := renderResponseFooter(m.responseMeta(turn.TokenUsage{Available: true, LatestCompletionTokens: 412}), 80)
	if !strings.Contains(line, served) || strings.Contains(line, configured) {
		t.Fatalf("provenance line = %q, want the served model and not the configured pin", line)
	}
	silent := servingSession{configured: configured}
	if got := (&model{session: &silent}).responseMeta(turn.TokenUsage{}).model; got != configured {
		t.Fatalf("provenance model = %q, want the configured name until the backend reports one", got)
	}
}
