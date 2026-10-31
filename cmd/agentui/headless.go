package agentui

import (
	"context"
	"strings"

	"github.com/charmbracelet/x/ansi"

	"github.com/invokr-agent/invokr/platform/turn"
	toolcore "github.com/invokr-agent/invokr/tools/core"
)

// HeadlessCommand is a slash command that runs without the TUI, for clients
// such as ACP chats.
type HeadlessCommand struct{ Name, Usage, Summary string }

// headlessHidden have a plain handler that only points back at the TUI.
var headlessHidden = map[string]bool{"recorded": true}

func HeadlessCommands() []HeadlessCommand {
	var out []HeadlessCommand
	for _, spec := range commandRegistry {
		if spec.plain != nil && !headlessHidden[spec.name] {
			out = append(out, HeadlessCommand{spec.name, spec.usage, spec.summary})
		}
	}
	return out
}

// RunHeadless runs a headless slash command. A skill yields the prompt to
// send as a turn instead of a reply; handled is false for text that is not a
// headless command, which then goes to the model unchanged.
func RunHeadless(ctx context.Context, backend CommandBackend, session turn.Session, text string) (reply, prompt string, handled bool) {
	name, args, slash := parseCommand(text)
	spec := findCommand(name)
	if !slash || spec == nil || spec.plain == nil || headlessHidden[name] {
		return "", "", false
	}
	if name == "help" {
		return headlessHelp(), "", true
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

// headlessHelp lists only what a headless client can run, one line each:
// chat apps wrap the TUI's two-column table badly.
func headlessHelp() string {
	lines := toolcore.MapSlice(HeadlessCommands(), func(c HeadlessCommand) string { return c.Usage + " — " + c.Summary })
	return strings.Join(lines, "\n")
}
