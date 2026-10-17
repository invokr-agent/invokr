package runtime

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/invokr-ai/invokr/agent"
	"github.com/invokr-ai/invokr/capability/agentdef"
	"github.com/invokr-ai/invokr/capability/memory"
	"github.com/invokr-ai/invokr/capability/skills"
	"github.com/invokr-ai/invokr/capability/syntax"
	"github.com/invokr-ai/invokr/clients"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/support/strutil"
	"github.com/invokr-ai/invokr/tools/core"

	"go.uber.org/zap"
)

type subagentRunner struct {
	logger    *zap.Logger
	provider  clients.LLMProvider
	events    events.Dispatcher
	skills    skills.Manager
	tools     core.ToolCaller
	syntax    syntax.PromptSyntax
	app       config.AppConfig
	agentDefs agentdef.Registry // may be nil
}

func (r *subagentRunner) RunSubagent(ctx context.Context, req core.SubagentRequest) (core.SubagentResult, error) {
	settings := r.app
	def, ok, defErr := r.resolveDefinition(req.AgentName)
	if defErr != nil {
		return core.SubagentResult{Label: req.Label, StopReason: "error", Error: defErr.Error()}, nil
	}
	if ok {
		settings = applyDefinition(settings, def, req)
	} else if req.MaxTurns > 0 {
		settings.Agent.MaxTurns = req.MaxTurns
	}

	toolCaller, toolErr := r.resolveToolCaller(req.AgentName, def, ok)
	if toolErr != nil {
		return core.SubagentResult{Label: req.Label, StopReason: "error", Error: toolErr.Error()}, nil
	}

	session, err := agent.NewSession(agent.SessionDeps{
		Logger: r.logger.Named("subagent"), Provider: r.provider, Events: r.events,
		Memory: memory.NewNoopManager(), SessionID: "", ParentSessionID: req.ParentSessionID,
		Skills: r.skills, Tools: toolCaller, Syntax: r.syntax,
	}, settings)
	if err != nil {
		return core.SubagentResult{Label: req.Label, StopReason: "error", Error: err.Error()}, err
	}
	// Subagent sessions run on a filtered view of the parent's long-lived
	// caller; forget their per-session tool state when the run ends so it does
	// not accumulate there (#317 B11). filteredCaller exposes no Close(), so
	// this cannot tear down the shared caller itself.
	defer session.Close()

	prompt := req.Prompt
	if taskContext := strings.TrimSpace(req.Context); taskContext != "" {
		prompt = fmt.Sprintf("Context from parent agent:\n%s\n\nTask:\n%s", taskContext, req.Prompt)
	}

	ctx = core.WithDelegateDepth(ctx, req.Depth)
	resp, err := session.Send(ctx, prompt)
	if err != nil {
		return core.SubagentResult{Label: req.Label, StopReason: "error", Error: err.Error(), Usage: resp.Usage}, err
	}
	if len(resp.ToolFailures)+len(resp.ToolFlagged) > 0 {
		events.RecordExecution(ctx, turn.ExecutionEvidence{Code: "child tool failure or flagged result"})
	}
	return core.SubagentResult{Label: req.Label, FinalText: turn.AppendHistoricalExecution(turn.AppendToolOutcomeGuard(resp.Message, resp.ToolFailures), resp.ExecutionEvidence), StopReason: strutil.FirstNonEmpty(resp.StopReason, "completed"), Usage: resp.Usage}, nil
}

func (r *subagentRunner) resolveDefinition(agentName string) (agentdef.Definition, bool, error) {
	if agentName == "" {
		return agentdef.Definition{}, false, nil
	}
	if r.agentDefs == nil {
		return agentdef.Definition{}, false, fmt.Errorf("agent definitions not loaded; cannot resolve %q", agentName)
	}
	d, ok := r.agentDefs.Get(agentName)
	if !ok {
		return agentdef.Definition{}, false, fmt.Errorf("agent definition %q not found", agentName)
	}
	return d, true, nil
}

func applyDefinition(settings config.AppConfig, def agentdef.Definition, req core.SubagentRequest) config.AppConfig {
	if def.MaxTurns > 0 {
		settings.Agent.MaxTurns = def.MaxTurns
	} else if req.MaxTurns > 0 {
		settings.Agent.MaxTurns = req.MaxTurns
	}
	if def.Model != "" {
		settings.Provider.Model = def.Model
	}
	if def.Instructions != "" {
		base := strings.TrimSpace(settings.Agent.SystemPrompt)
		if base == "" {
			settings.Agent.SystemPrompt = def.Instructions
		} else {
			settings.Agent.SystemPrompt = base + "\n\n" + def.Instructions
		}
	}
	return settings
}

// parentMemoryTools are tools that read or write the parent agent's memory
// store. Subagents run with a noop memory manager and must not reach the
// parent's memory through the shared tool caller unless an agent definition
// explicitly allowlists these tools.
var parentMemoryTools = []string{"memory", "recall", "recall_archive", "session_search"}

func (r *subagentRunner) resolveToolCaller(agentName string, def agentdef.Definition, hasDef bool) (core.ToolCaller, error) {
	if !hasDef || !def.ToolsExplicit {
		return newFilteredCaller(r.tools, toolNamesExcluding(r.tools.Specs(), parentMemoryTools)), nil
	}
	if err := validateToolAllowlist(agentName, def.Tools, r.tools); err != nil {
		return nil, err
	}
	return newFilteredCaller(r.tools, def.Tools), nil
}

// validateToolAllowlist checks each name in the allowlist against the live
// tool registry. Returns an error if any name is absent.
func validateToolAllowlist(agentName string, names []string, caller core.ToolCaller) error {
	if len(names) == 0 {
		return nil
	}
	live := toolNames(caller.Specs())
	unknown := slices.DeleteFunc(slices.Clone(names), func(name string) bool { return slices.Contains(live, name) })
	if len(unknown) > 0 {
		return fmt.Errorf("agent %q: unknown tool(s) in allowlist: %s", agentName, strings.Join(unknown, ", "))
	}
	return nil
}

func toolNamesExcluding(specs []core.ToolSpec, excluded []string) []string {
	return slices.DeleteFunc(toolNames(specs), func(name string) bool { return slices.Contains(excluded, name) })
}

func toolNames(specs []core.ToolSpec) []string {
	return core.MapSlice(specs, func(s core.ToolSpec) string { return s.Name })
}
