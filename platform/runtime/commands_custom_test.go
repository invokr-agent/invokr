package runtime

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/skills"
	"github.com/invokr-ai/invokr/tools/core"

	"go.uber.org/zap"
)

type fakeSkills struct {
	specs []skills.Spec
	err   error
}

func (f *fakeSkills) Specs(context.Context) ([]skills.Spec, error) { return f.specs, f.err }
func (f *fakeSkills) Load(_ context.Context, name string) (skills.Skill, error) {
	for _, spec := range f.specs {
		if spec.Name == name {
			return skills.Skill{Spec: spec, Instructions: "instructions for " + name}, nil
		}
	}
	return skills.Skill{}, fmt.Errorf("skill %q not found", name)
}

// recordingCaller captures HandleCall arguments on top of stubCaller.
type recordingCaller struct {
	stubCaller
	lastCall *core.ToolCall
}

func (r *recordingCaller) HandleCall(_ context.Context, call *core.ToolCall) (string, error) {
	r.lastCall = call
	return "ran:" + call.Name, nil
}

func commandSpecFor(name string, command *core.ToolCommand) core.ToolSpec {
	return core.ToolSpec{Name: name, Metadata: core.ToolMetadata{Command: command}}
}

func newCustomCommandService(caller core.ToolCaller, skillSpecs []skills.Spec) *CommandService {
	return &CommandService{
		tools:  caller,
		skills: &fakeSkills{specs: skillSpecs},
		logger: zap.NewNop(),
	}
}

func TestCustomCommandsListsToolsAndSkills(t *testing.T) {
	caller := &recordingCaller{stubCaller: stubCaller{specs: []core.ToolSpec{
		commandSpecFor("browser_agent", &core.ToolCommand{Name: "browse", Summary: "drive the browser"}),
		commandSpecFor("plain", nil),
		commandSpecFor("bad", &core.ToolCommand{Name: "Not Valid"}),
	}}}
	c := newCustomCommandService(caller, []skills.Spec{{Name: "review", Description: "review code"}})
	got := c.CustomCommands(context.Background())
	if len(got) != 2 {
		t.Fatalf("expected 2 commands, got %v", got)
	}
	if got[0].Name != "browse" || got[0].Kind != "tool" {
		t.Fatalf("unexpected first command: %+v", got[0])
	}
	if got[1].Name != "skill:review" || got[1].Kind != "skill" {
		t.Fatalf("unexpected second command: %+v", got[1])
	}
	if got[1].Summary != "review code" {
		t.Fatalf("skill summary not taken from description: %+v", got[1])
	}
}

func TestExecuteToolCommandCallsTool(t *testing.T) {
	caller := &recordingCaller{stubCaller: stubCaller{specs: []core.ToolSpec{
		commandSpecFor("browser_agent", &core.ToolCommand{Name: "browse"}),
	}}}
	c := newCustomCommandService(caller, nil)
	out, err := c.Execute(context.Background(), "browse", []string{"url=https://x.test", "depth=2", "safe=true"})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "ran:browser_agent" {
		t.Fatalf("unexpected output %q", out)
	}
	args := string(caller.lastCall.Arguments)
	for _, want := range []string{`"url":"https://x.test"`, `"depth":2`, `"safe":true`} {
		if !strings.Contains(args, want) {
			t.Fatalf("arguments %s missing %s", args, want)
		}
	}
}

func TestExecuteToolCommandRejectsBadArgs(t *testing.T) {
	caller := &recordingCaller{stubCaller: stubCaller{specs: []core.ToolSpec{
		commandSpecFor("browser_agent", &core.ToolCommand{Name: "browse"}),
	}}}
	c := newCustomCommandService(caller, nil)
	if _, err := c.Execute(context.Background(), "browse", []string{"no-equals"}); err == nil {
		t.Fatal("expected key=value error")
	}
}

func TestExecuteSkillCommandReturnsInstructions(t *testing.T) {
	c := newCustomCommandService(&recordingCaller{}, []skills.Spec{{Name: "review"}})
	out, err := c.Execute(context.Background(), "skill:review", nil)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if out != "instructions for review" {
		t.Fatalf("unexpected instructions %q", out)
	}
	if _, err := c.Execute(context.Background(), "skill:missing", nil); err == nil {
		t.Fatal("expected error for unknown skill")
	}
}

func TestExecuteUnknownCommandStillErrors(t *testing.T) {
	c := newCustomCommandService(&recordingCaller{}, nil)
	if _, err := c.Execute(context.Background(), "nonsense", nil); err == nil {
		t.Fatal("expected unsupported-command error")
	}
}
