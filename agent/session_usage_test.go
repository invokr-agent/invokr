package agent

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/capability/memory"
	"github.com/invokr-agent/invokr/capability/skills"
	"github.com/invokr-agent/invokr/capability/syntax"
	"github.com/invokr-agent/invokr/clients"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/tools"
	"github.com/invokr-agent/invokr/tools/core"
	"go.uber.org/zap"
)

// TestSessionReportsWindowBeforeFirstTurn is the session half of issue #82's
// meter startup: a session reports its provider's context window with nothing
// spent before its first model call, and the window comes from the provider
// itself rather than from a caller, so a surface can render 0 / window from the
// start instead of waiting for a usage event.
func TestSessionReportsWindowBeforeFirstTurn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"default_generation_settings":{"n_ctx":128000}}`))
	}))
	defer server.Close()

	app := config.AppConfig{
		Provider:  config.ProviderConfig{Model: "local-llama"},
		Memory:    config.MemoryConfig{Dir: t.TempDir()},
		Workspace: config.WorkspaceConfig{Root: t.TempDir()},
		Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
	}
	caller := tools.NewCaller(core.CallerDeps{App: app}, zap.NewNop())
	session, err := NewSession(SessionDeps{
		Logger:            zap.NewNop(),
		Provider:          clients.NewClient(server.URL),
		Events:            events.NewOrderedDispatcher(zap.NewNop()),
		Memory:            memory.NewNoopManager(),
		Skills:            skills.NewManagerFromConfig(app, zap.NewNop()),
		Tools:             caller,
		Syntax:            syntax.NewTaggedJSONSyntax(),
		BackgroundContext: context.Background(),
	}, app)
	if err != nil {
		t.Fatalf("new session: %v", err)
	}
	defer session.Close()

	want := int64(128_000)
	deadline := time.Now().Add(10 * time.Second)
	for session.Usage().ContextWindowTokens != want && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	usage := session.Usage()
	if usage.ContextWindowTokens != want {
		t.Fatalf("window before the first turn = %d after probing, want the provider's %d", usage.ContextWindowTokens, want)
	}
	if usage.LatestPromptTokens+usage.LatestCompletionTokens != 0 {
		t.Fatalf("usage before the first turn = %+v, want the window at 0", usage)
	}
}
