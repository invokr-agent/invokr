package clients

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	inference "github.com/rivt-ai/go-inference-router"
)

const largeReasoningToolResultBytes = 8192

var failedToolStatus = regexp.MustCompile(`\[(exit code -?[1-9][0-9]*|timed out)\]|^FAIL\n`)

// LlamaCppToolResultBudget leaves user and compaction requests at the server default.
// A trailing batch uses the highest budget needed by any tool result.
func LlamaCppToolResultBudget(messages []inference.Message) int {
	if len(messages) == 0 || messages[len(messages)-1].Role != inference.RoleTool {
		return 0
	}
	budget := 1024
	for i := len(messages) - 1; i >= 0 && messages[i].Role == inference.RoleTool; i-- {
		content := messages[i].Content
		if len(content) >= largeReasoningToolResultBytes || failedToolResult(content) {
			budget = 2048
		}
	}
	return budget
}

func failedToolResult(content string) bool {
	if failedToolStatus.MatchString(content) {
		return true
	}
	if !strings.HasPrefix(strings.TrimSpace(content), "{") {
		return false
	}
	var value map[string]any
	if json.Unmarshal([]byte(content), &value) != nil {
		return false
	}
	exitCode, _ := value["exit_code"].(float64)
	return (value["error"] != nil && value["error"] != "") || value["success"] == false || value["ok"] == false ||
		value["status"] == "error" || value["status"] == "failed" || value["timed_out"] == true || exitCode != 0
}

// ReasoningKind names a provider whose effort scale invokr knows. Empty means
// the active provider has no known scale and the level is never sent.
type ReasoningKind string

const (
	ReasoningLlamaCpp   ReasoningKind = "llama.cpp"
	ReasoningOllama     ReasoningKind = "ollama"
	ReasoningOpenRouter ReasoningKind = "openrouter"
	ReasoningAnthropic  ReasoningKind = "anthropic"
)

// reasoningScale is one provider's accepted levels and the request field
// they ride in: key alone for a string field, key+wrap for {key: {wrap: level}}.
type reasoningScale struct {
	levels    []string
	key, wrap string
}

var reasoningScales = map[ReasoningKind]reasoningScale{
	ReasoningLlamaCpp:   {levels: []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}, key: "reasoning_effort"},
	ReasoningOllama:     {levels: []string{"none", "low", "medium", "high", "max"}, key: "reasoning_effort"},
	ReasoningOpenRouter: {levels: []string{"none", "minimal", "low", "medium", "high", "xhigh"}, key: "reasoning", wrap: "effort"},
	ReasoningAnthropic:  {levels: []string{"low", "medium", "high", "xhigh", "max"}, key: "output_config", wrap: "effort"},
}

// ReasoningEfforts is the union of every scale, the order levels are listed in.
var ReasoningEfforts = reasoningScales[ReasoningLlamaCpp].levels

// Levels lists the levels the kind accepts; nil for an unknown kind.
func (k ReasoningKind) Levels() []string { return reasoningScales[k].levels }

// Accepts reports whether level is on the kind's scale.
func (k ReasoningKind) Accepts(level string) bool { return slices.Contains(k.Levels(), level) }

// Extra returns the request field carrying level for this kind.
func (k ReasoningKind) Extra(level string) (key string, value any) {
	scale := reasoningScales[k]
	if scale.wrap != "" {
		return scale.key, map[string]any{scale.wrap: level}
	}
	return scale.key, level
}

// DisablesThinking reports whether level should also carry the llama.cpp
// chat_template_kwargs switch. Templates that key on enable_thinking (Qwen3.x,
// GLM-4.7) ignore reasoning_effort, so "none" alone leaves thinking on.
func (k ReasoningKind) DisablesThinking(level string) bool {
	return k == ReasoningLlamaCpp && level == "none"
}

// NormalizeReasoningEffort validates a user-supplied level against the union
// of all scales. Empty and "default" clear the setting so the server default
// applies.
func NormalizeReasoningEffort(level string) (string, error) {
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" || level == "default" {
		return "", nil
	}
	if !slices.Contains(ReasoningEfforts, level) {
		return "", fmt.Errorf("unknown reasoning effort %q (want %s|default)", level, strings.Join(ReasoningEfforts, "|"))
	}
	return level, nil
}

// ReasoningState is what a UI shows for the reasoning-effort setting.
type ReasoningState struct {
	// Level is the configured effort, even when the provider will not see it.
	Level string
	// Kind is the active provider's scale; empty means the level is never sent.
	Kind ReasoningKind
	// Supported is false when the level will not take effect: unknown kind,
	// level off the kind's scale, or a llama.cpp template that ignores it.
	Supported bool
}
