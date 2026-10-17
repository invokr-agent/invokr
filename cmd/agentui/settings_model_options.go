package agentui

import (
	"fmt"

	"charm.land/huh/v2"
	"github.com/invokr-ai/invokr/config"
)

type modelOptionsSession interface {
	ModelOptions() (string, []config.ModelOptionValue, error)
	SaveModelOptions(string, map[string]string) error
}

type modelOptionsDraft struct {
	model  string
	values map[string]*string
	save   bool
}

func (m *model) modelOptionsSettings() []huh.Field {
	m.settings.modelOptions = modelOptionsDraft{}
	session, ok := m.session.(modelOptionsSession)
	if !ok {
		return []huh.Field{huh.NewNote().Title("Model options unavailable in this runtime")}
	}
	name, values, err := session.ModelOptions()
	if err != nil {
		return []huh.Field{huh.NewNote().Title("Model options").Description(err.Error())}
	}
	draft := &m.settings.modelOptions
	*draft = modelOptionsDraft{model: name, values: map[string]*string{}}
	fields := []huh.Field{huh.NewNote().Title("Model: " + name).Description("Blank inherits startup settings; omit excludes the field. Save applies to subsequent main requests and persists for this model. Environment overrides win. Small output caps can truncate thinking or tool calls.")}
	for _, value := range values {
		draft.values[value.Option.Key] = new(value.Override)
		fields = append(fields, huh.NewInput().Title(value.Option.Label).
			Description(fmt.Sprintf("Effective: %s · source: %s", value.Effective, value.Source)).
			Placeholder("inherit").Value(draft.values[value.Option.Key]).Validate(value.Option.Validate))
	}
	return append(fields, huh.NewConfirm().Title("Save model options?").Affirmative("Save").Negative("Cancel").Value(&draft.save))
}

func (m *model) saveModelOptions() {
	draft := m.settings.modelOptions
	values := make(map[string]string, len(draft.values))
	for key, value := range draft.values {
		values[key] = *value
	}
	if err := m.session.(modelOptionsSession).SaveModelOptions(draft.model, values); err != nil {
		m.commandMessage("model options: " + err.Error())
		return
	}
	m.commandMessage("model options saved for " + draft.model + " (next main request; environment overrides still apply)")
}
