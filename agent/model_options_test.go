package agent

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/invokr-agent/invokr/capability/syntax"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/events"
	inference "github.com/rivt-ai/go-inference-router"
	"go.uber.org/zap"
)

func TestModelOptionsSaveSwitchAndRequest(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	for _, o := range config.ModelOptionRegistry {
		t.Setenv(o.Env, "")
	}
	p := &scriptedProvider{outputs: []*inference.Response{{}, {}, {}, {}}}
	g := &OpenAIModelGateway{model: "a", provider: p, settings: config.AppConfig{}.WithDefaults().Agent,
		syntax: syntax.NewTaggedJSONSyntax(), codec: OpenAINativeToolCodec{}, logger: zap.NewNop(),
		providerContext: NewProviderContext(32768, "a"), events: events.NewOrderedDispatcher(zap.NewNop())}
	s := &Session{runner: &Runner{modelGateway: g}}
	if err := s.SaveModelOptions("a", map[string]string{"max_tokens": "32768", "temperature": "0"}); err != nil {
		t.Fatal(err)
	}
	model, values, err := s.ModelOptions()
	if err != nil || model != "a" || values[0].Effective != "32768" || values[0].Source != "model config" {
		t.Fatalf("effective options: %s, %+v, %v", model, values, err)
	}
	call := func() {
		t.Helper()
		if _, err := g.completeAttempt(context.Background(), []inference.Message{inference.UserMessage("hi")}, "s", false, false); err != nil {
			t.Fatal(err)
		}
	}
	call()
	if p.requests[0].MaxOutputTokens != 32768 || *p.requests[0].Temperature != 0 {
		t.Fatalf("saved request: %+v", p.requests[0])
	}
	g.SetModel("b")
	call()
	if p.requests[1].MaxOutputTokens != 16384 || *p.requests[1].Temperature != 1 {
		t.Fatalf("model leaked: %+v", p.requests[1])
	}
	cfg, err := config.LoadDefaultFileWithProfile("")
	if err != nil || cfg.Agent.ModelOptions["a"]["temperature"] != "0" {
		t.Fatalf("reload: %+v, %v", cfg.Agent.ModelOptions, err)
	}
	if err := os.WriteFile(config.DefaultConfigPath(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveModelOptions("a", map[string]string{"max_tokens": "4096"}); err == nil {
		t.Fatal("bad file overwritten")
	}
	g.SetModel("a")
	call()
	if p.requests[2].MaxOutputTokens != 32768 {
		t.Fatal("failed save changed runtime")
	}
	// Summarization constructs its own request instead of using main-turn options.
	p.outputs[3].Message.Content = "summary"
	if _, err := g.SummarizeConversation(context.Background(), []inference.Message{inference.UserMessage("hi")}); err != nil {
		t.Fatal(err)
	}
	if p.requests[3].MaxOutputTokens == 32768 {
		t.Fatal("main cap leaked into summary")
	}
}

func TestModelOptionsConcurrentSaveAndRequest(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	for _, o := range config.ModelOptionRegistry {
		t.Setenv(o.Env, "")
	}
	p := &scriptedProvider{}
	for range 20 {
		p.outputs = append(p.outputs, &inference.Response{})
	}
	g := &OpenAIModelGateway{model: "a", provider: p, settings: config.AppConfig{}.WithDefaults().Agent,
		syntax: syntax.NewTaggedJSONSyntax(), codec: OpenAINativeToolCodec{}, logger: zap.NewNop(),
		providerContext: NewProviderContext(32768, "a"), events: events.NewOrderedDispatcher(zap.NewNop())}
	s := &Session{runner: &Runner{modelGateway: g}}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 20 {
			if err := s.SaveModelOptions("a", map[string]string{"max_tokens": "8192"}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for range 20 {
		if _, err := g.completeAttempt(context.Background(), []inference.Message{inference.UserMessage("hi")}, "s", false, false); err != nil {
			t.Error(err)
		}
		if _, _, err := s.ModelOptions(); err != nil {
			t.Error(err)
		}
	}
	wg.Wait()
}
