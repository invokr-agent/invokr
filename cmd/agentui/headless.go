package agentui

import (
	"context"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/invokr-agent/invokr/platform/turn"
	toolcore "github.com/invokr-agent/invokr/tools/core"
)

type HeadlessCommand struct{ Name, Usage, Summary string }

// Chat commands skip the model and its approvals, so none may act on the
// host; false allows the bare listing only. Tool commands would bypass the
// chat's approvals and are left out.
var headlessAllowed = map[string]bool{
	"help": true, "usage": true, "compact": true, "model": true, "reasoning": true,
	"profile": true, "tools": true, "agents": true, "jobs": false, "skills": false,
}

func headless(spec *commandSpec) bool {
	_, builtin := headlessAllowed[spec.name]
	return spec.plain != nil && (builtin || spec.skill)
}

func HeadlessCommands() []HeadlessCommand {
	var out []HeadlessCommand
	for _, spec := range commandRegistry {
		if headless(&spec) {
			usage := spec.usage
			if !headlessAllowed[spec.name] && !spec.skill {
				usage = "/" + spec.name
			}
			out = append(out, HeadlessCommand{spec.name, usage, spec.summary})
		}
	}
	return out
}

// RunHeadless returns a skill as prompt, to send as a turn, instead of reply.
func RunHeadless(ctx context.Context, backend CommandBackend, session turn.Session, text string) (reply, prompt string, handled bool) {
	name, args, slash := parseCommand(text)
	spec := findCommand(name)
	if !slash || spec == nil || !headless(spec) {
		return "", "", false
	}
	if withArgs, builtin := headlessAllowed[name]; builtin && !withArgs && len(args) > 0 {
		return "/" + name + " takes no arguments here; use the terminal UI", "", true
	}
	if name == "help" {
		lines := toolcore.MapSlice(HeadlessCommands(), func(c HeadlessCommand) string { return c.Usage + " — " + c.Summary })
		return strings.Join(lines, "\n"), "", true
	}
	if spec.skill {
		instructions, err := backend.Execute(ctx, name, nil)
		if err != nil {
			return "error: " + err.Error(), "", true
		}
		return "", skillPrompt(instructions, args), true
	}
	out, _ := spec.plain(ctx, backend, session, name, args)
	return ansi.Strip(out), "", true
}
