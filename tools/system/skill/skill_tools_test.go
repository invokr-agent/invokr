package skill_test

import (
	"context"
	"encoding/json"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/tools"
	"github.com/invokr-agent/invokr/tools/core"
	"github.com/invokr-agent/invokr/tools/system/skill"
	"testing"

	"github.com/invokr-agent/invokr/capability/skills"
)

type fakeSkillManager struct {
	skill skills.Skill
}

func (m fakeSkillManager) Specs(context.Context) ([]skills.Spec, error) {
	return []skills.Spec{m.skill.Spec}, nil
}

func (m fakeSkillManager) Load(_ context.Context, name string) (skills.Skill, error) {
	if name == m.skill.Name {
		return m.skill, nil
	}
	return skills.Skill{}, errUnknownFakeSkill
}

var errUnknownFakeSkill = &fakeSkillError{}

type fakeSkillError struct{}

func (*fakeSkillError) Error() string { return "unknown fake skill" }

func TestRegisterSkillToolsLoadsSkill(t *testing.T) {
	deps := core.CallerDeps{
		App: config.AppConfig{},
		SkillManager: fakeSkillManager{skill: skills.Skill{
			Spec: skills.Spec{
				Name:        "grill-me",
				Description: "Stress-test a plan.",
			},
			Instructions: "Ask hard questions.",
			Path:         "/tmp/secret/SKILL.md",
		},
		},
	}
	caller := tools.NewCaller(deps)
	skill.RegisterSkillTools(caller, deps)

	got, err := caller.HandleCall(context.Background(), &core.ToolCall{
		Name:      "load_skill",
		Arguments: json.RawMessage(`{"name":"grill-me"}`),
	})
	if err != nil {
		t.Fatalf("HandleCall() error = %v", err)
	}

	var payload skills.Skill
	if err := json.Unmarshal([]byte(got), &payload); err != nil {
		t.Fatalf("result was not JSON skill: %v", err)
	}
	if payload.Name != "grill-me" || payload.Instructions != "Ask hard questions." {
		t.Fatalf("unexpected skill payload: %#v", payload)
	}
	if payload.Path != "" {
		t.Fatalf("expected path to be omitted, got %q", payload.Path)
	}
}
