package core

import (
	"context"
	"github.com/invokr-agent/invokr/capability/agentdef"
	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/capability/memory"
	"github.com/invokr-agent/invokr/capability/skills"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/platform/events"
	"github.com/invokr-agent/invokr/platform/turn"
)

type EmbeddingProvider interface {
	Embed(context.Context, []string) ([][]float32, error)
}

type SubagentRunner interface {
	RunSubagent(context.Context, SubagentRequest) (SubagentResult, error)
}

type SubagentRequest struct {
	Label, Prompt, Context, ParentSessionID string
	MaxTurns, Depth                         int
	// AgentName is the definition name to look up in the registry; "" = generalist.
	AgentName string
	// AgentModel is resolved by RunSubagent from the definition; "" = inherit.
	AgentModel string
	// ToolAllowlist is resolved by RunSubagent from the definition.
	// nil = all tools; non-nil (even empty) = allowlist.
	ToolAllowlist []string
}

type SubagentResult struct {
	Label      string          `json:"label"`
	FinalText  string          `json:"final_text,omitempty"`
	StopReason string          `json:"stop_reason,omitempty"`
	Error      string          `json:"error,omitempty"`
	Usage      turn.TokenUsage `json:"usage"`
}

type CallerDeps struct {
	App          config.AppConfig
	Memory       memory.Manager
	Embeddings   EmbeddingProvider
	Subagents    SubagentRunner
	SkillManager skills.Manager
	Dispatcher   events.Dispatcher
	Approver     approval.Approver
	Policy       *approval.Policy
	// AgentDefs is the registry of custom agent definitions.
	// May be nil if the agents directory is absent or empty.
	AgentDefs agentdef.Registry
}
