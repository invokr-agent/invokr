package agent

import (
	"fmt"
	"maps"

	"github.com/invokr-ai/invokr/clients"
	"github.com/invokr-ai/invokr/config"
)

func (g *OpenAIModelGateway) modelOptionSettings() config.AgentConfig {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.settings // ModelOptions maps are immutable snapshots, replaced on save.
}

func (s *Session) ModelOptions() (string, []config.ModelOptionValue, error) {
	g, ok := s.runner.modelGateway.(*OpenAIModelGateway)
	if !ok {
		return "", nil, fmt.Errorf("model options unavailable")
	}
	model, settings := g.Model(), g.modelOptionSettings()
	req, err := clients.ApplyModelOptions(g.buildRequest(model, nil, nil), settings)
	if err != nil {
		return model, nil, err
	}
	values := make([]config.ModelOptionValue, 0, len(config.ModelOptionRegistry))
	for _, o := range config.ModelOptionRegistry {
		_, source := o.Override(settings, model)
		values = append(values, config.ModelOptionValue{Option: o, Effective: o.Read(req), Source: source, Override: settings.ModelOptions[model][o.Key]})
	}
	return model, values, nil
}

// SaveModelOptions persists first, then publishes a new snapshot for the next
// request. A failed write leaves the running session unchanged.
func (s *Session) SaveModelOptions(model string, values map[string]string) error {
	g, ok := s.runner.modelGateway.(*OpenAIModelGateway)
	if !ok {
		return fmt.Errorf("model options unavailable")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := config.SetModelOptions(config.DefaultConfigPath(), model, values); err != nil {
		return fmt.Errorf("save model options: %w", err)
	}
	next := maps.Clone(g.settings.ModelOptions)
	if next == nil {
		next = map[string]map[string]string{}
	}
	next[model] = maps.Clone(values)
	g.settings.ModelOptions = next
	return nil
}
