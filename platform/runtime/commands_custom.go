package runtime

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/invokr-agent/invokr/support/strutil"
	toolcore "github.com/invokr-agent/invokr/tools/core"

	"go.uber.org/zap"
)

// CustomCommand describes a dynamically registered slash command: a tool that
// declared a command in its metadata, or a skill exposed as /skill:<name>.
type CustomCommand struct {
	Name, Usage, Summary string
	// Kind is "tool" or "skill"; the UI picks the handler from it.
	Kind string
}

// SkillCommandPrefix namespaces skill commands so a skill can never shadow a
// built-in command or a tool command.
const SkillCommandPrefix = "skill:"

// CustomCommand.Kind values.
const (
	KindTool  = "tool"
	KindSkill = "skill"
)

// commandNamePattern bounds what a tool may claim as a command name: built-in
// names are plain lowercase words, so requiring the same shape (plus _ and -)
// keeps declared names typeable and prevents spoofing of the skill: namespace.
var commandNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// CustomCommands lists tool- and skill-declared commands, sorted by name.
// Collisions — with built-ins or within the list — are the UI registry's to
// skip: it already refuses any name it knows, including earlier entries.
func (c *CommandService) CustomCommands(ctx context.Context) []CustomCommand {
	// Re-scan the skills directory so a skill installed mid-session shows up;
	// only the filesystem manager caches, hence the narrow assertion.
	if invalidator, ok := c.skills.(interface{ Invalidate() }); ok {
		invalidator.Invalidate()
	}
	out := c.toolCommands()
	out = append(out, c.skillCommands(ctx)...)
	slices.SortFunc(out, func(a, b CustomCommand) int { return cmp.Compare(a.Name, b.Name) })
	return out
}

// toolCommands lists tool-declared commands in Specs order, first declaration
// of a name winning — the same rule executeCustom dispatches by, so the UI
// entry and the dispatched tool always agree.
func (c *CommandService) toolCommands() []CustomCommand {
	var out []CustomCommand
	seen := map[string]string{}
	for _, spec := range c.tools.Specs() {
		command := spec.Metadata.Command
		if command == nil || !commandNamePattern.MatchString(command.Name) {
			continue
		}
		if winner, ok := seen[command.Name]; ok {
			c.logger.Warn("duplicate tool command, keeping first",
				zap.String("command", command.Name), zap.String("winner", winner), zap.String("skipped", spec.Name))
			continue
		}
		seen[command.Name] = spec.Name
		usage := strutil.FirstNonEmptyTrimmed(command.Usage, "/"+command.Name+" [key=value…]")
		summary := strutil.FirstNonEmptyTrimmed(command.Summary, "run the "+spec.Name+" tool")
		out = append(out, CustomCommand{Name: command.Name, Usage: usage, Summary: summary, Kind: KindTool})
	}
	return out
}

func (c *CommandService) skillCommands(ctx context.Context) []CustomCommand {
	specs, err := c.skills.Specs(ctx)
	if err != nil {
		c.logger.Warn("skill commands unavailable", zap.Error(err))
		return nil
	}
	var out []CustomCommand
	for _, spec := range specs {
		if !commandNamePattern.MatchString(spec.Name) {
			c.logger.Warn("skill name is not a valid command, skipping", zap.String("skill", spec.Name))
			continue
		}
		name := SkillCommandPrefix + spec.Name
		out = append(out, CustomCommand{
			Name: name, Usage: "/" + name + " [task…]", Kind: KindSkill,
			Summary: strutil.FirstNonEmptyTrimmed(spec.Description, "run the "+spec.Name+" skill"),
		})
	}
	return out
}

// executeCustom dispatches a command that is not a built-in: a skill command
// returns the skill's instructions (the UI decides how to submit them); a tool
// command becomes a normal tool call, so approval gating applies unchanged.
func (c *CommandService) executeCustom(ctx context.Context, command string, args []string) (string, error) {
	if name, ok := strings.CutPrefix(command, SkillCommandPrefix); ok {
		skill, err := c.skills.Load(ctx, name)
		if err != nil {
			return "", fmt.Errorf("load skill %q: %w", name, err)
		}
		return skill.Instructions, nil
	}
	for _, spec := range c.tools.Specs() {
		if spec.Metadata.Command != nil && spec.Metadata.Command.Name == command {
			return c.callToolCommand(ctx, spec.Name, args)
		}
	}
	return "", fmt.Errorf("runtime command %q is not supported", command)
}

func (c *CommandService) callToolCommand(ctx context.Context, tool string, args []string) (string, error) {
	arguments, err := parseKVArgs(args)
	if err != nil {
		return "", err
	}
	return c.tools.HandleCall(ctx, &toolcore.ToolCall{Name: tool, Arguments: arguments})
}

// parseKVArgs turns key=value tokens into a JSON arguments object. A value
// that parses as a JSON scalar or composite keeps its type ("true", "3",
// "[1,2]"); anything else is a string. The tool's own schema validation stays
// the authority on which keys and types are acceptable.
func parseKVArgs(args []string) (json.RawMessage, error) {
	object := make(map[string]any, len(args))
	for _, arg := range args {
		key, value, ok := strings.Cut(arg, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("argument %q is not key=value", arg)
		}
		var typed any
		if err := json.Unmarshal([]byte(value), &typed); err != nil {
			typed = value
		}
		object[key] = typed
	}
	return json.Marshal(object)
}
