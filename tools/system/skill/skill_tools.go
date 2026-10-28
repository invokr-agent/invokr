package skill

import (
	"context"
	"fmt"
	"github.com/invokr-agent/invokr/tools/core"
	"strings"

	"github.com/invokr-agent/invokr/capability/skills"
)

type loadSkillArgs struct {
	Name string `json:"name" desc:"Skill name from the Available skills list."`
}

func RegisterSkillTools(c core.NativeRegistrar, deps core.CallerDeps) {
	manager := deps.SkillManager
	if manager == nil {
		return
	}

	core.Register(c, core.Tool[loadSkillArgs]{
		Spec: core.ToolSpec{
			Name: "load_skill", Description: "Load full instructions for an available skill by name. Use this before applying a listed skill.",
			Example:    `{"name":"load_skill","arguments":{"name":"grill-me"}}`,
			Parameters: core.SchemaFor[loadSkillArgs](), Metadata: core.ReadWorkspaceMetadata("skill", core.ToolCapabilities{ReadsWorkspace: true}),
		},
		Execute: func(ctx context.Context, args loadSkillArgs) (string, error) {
			return handleLoadSkill(ctx, manager, args)
		},
	})
}

func handleLoadSkill(ctx context.Context, manager skills.Manager, args loadSkillArgs) (string, error) {
	args.Name = strings.TrimSpace(args.Name)
	if args.Name == "" {
		return "", fmt.Errorf("name is required")
	}
	skill, err := manager.Load(ctx, args.Name)
	if err != nil {
		return "", err
	}
	return core.MarshalResult("load_skill", struct {
		Name         string `json:"name"`
		Description  string `json:"description"`
		Instructions string `json:"instructions"`
	}{
		Name: skill.Name, Description: skill.Description, Instructions: skill.Instructions,
	})
}
