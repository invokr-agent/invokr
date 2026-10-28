package agent

import (
	"context"
	"testing"

	"github.com/invokr-agent/invokr/capability/syntax"
	"github.com/invokr-agent/invokr/clients"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"
	"github.com/invokr-agent/invokr/tools"

	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

// switchableGateway is a mock ModelGateway that also implements modelSwitcherGateway.
// It records the model name at each Begin/Continue call for assertion.
type switchableGateway struct {
	replies      []string
	next         int
	currentModel string
	modelsAtTurn []string
	onSet        func(string)
}

func (g *switchableGateway) SetModel(name string) {
	g.currentModel = name
	if g.onSet != nil {
		g.onSet(name)
	}
}
func (g *switchableGateway) Model() string { return g.currentModel }

func (g *switchableGateway) Begin(_ context.Context, req BeginModelRequest) (ModelTurn, error) {
	return g.turn([]inference.Message{
		inference.SystemMessage("sys"),
		inference.UserMessage(req.Message),
	})
}

func (g *switchableGateway) Continue(_ context.Context, req ContinueModelRequest) (ModelTurn, error) {
	return g.turn(req.Messages)
}

func (g *switchableGateway) turn(messages []inference.Message) (ModelTurn, error) {
	g.modelsAtTurn = append(g.modelsAtTurn, g.currentModel)
	reply := g.replies[g.next]
	g.next++
	return ModelTurn{
		Messages:   messages,
		Completion: completionOutput{content: `{"type":"reply","message":"` + reply + `"}`},
	}, nil
}

func makeRunnerWithSwitchableGateway(gw *switchableGateway) *Runner {
	bus := events.NewOrderedDispatcher(zap.NewNop())
	providerContext := NewProviderContext(0)
	providerContext.SetModel(gw.currentModel)
	return &Runner{
		modelGateway:    gw,
		turnProcessor:   &TurnProcessor{events: bus, sessionID: "s1", syntax: syntax.NewTaggedJSONSyntax(), tools: &tools.Caller{}, logger: zap.NewNop()},
		events:          bus,
		settings:        config.AgentConfig{MaxTurns: 4},
		logger:          zap.NewNop(),
		usage:           turn.NewSessionTokenUsage(0),
		providerContext: providerContext,
	}
}

func TestGatewaySetModelRoundtrip(t *testing.T) {
	provider := &scriptedProvider{}
	gw := &OpenAIModelGateway{
		model:    "model-a",
		provider: provider,
		settings: config.AgentConfig{},
		tools:    &tools.Caller{},
		syntax:   syntax.NewTaggedJSONSyntax(),
		logger:   zap.NewNop(),
	}

	if got := gw.Model(); got != "model-a" {
		t.Fatalf("Model() = %q, want %q", got, "model-a")
	}
	gw.SetModel("model-b")
	if got := gw.Model(); got != "model-b" {
		t.Fatalf("after SetModel, Model() = %q, want %q", got, "model-b")
	}
}

func TestSessionModelReturnsInitialModel(t *testing.T) {
	gw := &switchableGateway{
		currentModel: "initial-model",
		replies:      []string{},
	}
	runner := makeRunnerWithSwitchableGateway(gw)
	sess := &Session{sessionID: "s1", runner: runner}

	if got := sess.Model(); got != "initial-model" {
		t.Fatalf("Model() = %q, want %q", got, "initial-model")
	}
}

// TestSetModelTakesEffectOnNextSend verifies that SetModel while idle causes the
// next Send to use the new model name, while the previous Send used the old one.
func TestSetModelTakesEffectOnNextSend(t *testing.T) {
	gw := &switchableGateway{
		currentModel: "old-model",
		replies:      []string{"first", "second"},
	}
	runner := makeRunnerWithSwitchableGateway(gw)
	sess := &Session{sessionID: "s1", runner: runner}
	runner.providerContext.ApplyMetadata("old-model", clients.ProviderMetadata{ContextWindowTokens: 1000})

	if _, err := sess.Send(context.Background(), "first message"); err != nil {
		t.Fatal(err)
	}
	sess.SetModel("new-model")
	if _, err := sess.Send(context.Background(), "second message"); err != nil {
		t.Fatal(err)
	}

	if len(gw.modelsAtTurn) != 2 {
		t.Fatalf("modelsAtTurn = %v, want 2 entries", gw.modelsAtTurn)
	}
	if gw.modelsAtTurn[0] != "old-model" {
		t.Fatalf("first turn model = %q, want %q", gw.modelsAtTurn[0], "old-model")
	}
	if gw.modelsAtTurn[1] != "new-model" {
		t.Fatalf("second turn model = %q, want %q", gw.modelsAtTurn[1], "new-model")
	}
	if got := runner.providerContext.Model(); got != "new-model" {
		t.Fatalf("shared model = %q, want %q", got, "new-model")
	}
	if got := runner.usage.ContextWindowTokens; got != 0 {
		t.Fatalf("usage context after model switch = %d, want unknown fallback", got)
	}
}

func TestModelQueuedDuringApplyRemainsPending(t *testing.T) {
	gw := &switchableGateway{currentModel: "configured"}
	runner := makeRunnerWithSwitchableGateway(gw)
	fallback := "startup-fallback"
	runner.pendingModel.Store(&fallback)
	gw.onSet = func(string) {
		userChoice := "user-choice"
		runner.pendingModel.Store(&userChoice)
		gw.onSet = nil
	}

	runner.applyPendingModel()
	if pending := runner.pendingModel.Load(); pending == nil || *pending != "user-choice" {
		t.Fatalf("pending model = %v, want user choice", pending)
	}
	runner.applyPendingModel()
	if gw.currentModel != "user-choice" || runner.pendingModel.Load() != nil {
		t.Fatalf("model = %q pending = %v, want applied user choice", gw.currentModel, runner.pendingModel.Load())
	}
}
