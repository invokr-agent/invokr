package config

import (
	"cmp"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/invokr-ai/invokr/constants"

	"go.uber.org/zap"
)

// Validate rejects config values WithDefaults cannot repair — negative
// timeouts, retries, and turn budgets — and warns (without failing) about
// configured directories that do not exist, so a typo'd path surfaces at
// startup instead of silently yielding empty skills/agents/toolbox sets.
// Call it after WithDefaults; logger may be nil (warnings dropped).
func (c AppConfig) Validate(logger *zap.Logger) error {
	compact := cmp.Or(c.Agent.ContextCompactPercent, constants.ContextCompactPercent)
	if c.Agent.ContextWarningPercent < 0 || c.Agent.ContextWarningPercent >= compact || compact >= 100 ||
		(c.Agent.ContextCompactPercent != 0 && c.Agent.ContextWarningPercent == 0) {
		return fmt.Errorf("config: context percentages require warning below %d with automatic compaction, or 0 < warning < compact < 100 when compact is set", constants.ContextCompactPercent)
	}
	checks := []struct {
		name     string
		negative bool
	}{
		{"provider.http_timeout", c.Provider.HTTPTimeout < 0},
		{"provider.stall_timeout", c.Provider.StallTimeout < 0},
		{"provider.max_retries", c.Provider.MaxRetries < 0},
		{"agent.max_turns", c.Agent.MaxTurns < 0},
		{"agent.run_timeout", c.Agent.RunTimeout < 0},
		{"agent.max_request_tokens", c.Agent.MaxRequestTokens < 0},
		{"agent.tool_result_max_tokens", c.Agent.ToolResultMaxTokens < 0},
		{"memory.zerfoo.request_timeout_ms", c.Memory.Zerfoo.RequestTimeoutMS < 0},
		{"toolbox.exec_timeout_ms", c.Toolbox.ExecTimeoutMS < 0},
		{"code_interpreter.default_timeout_ms", c.CodeInterpreter.DefaultTimeoutMS < 0},
		{"code_interpreter.max_timeout_ms", c.CodeInterpreter.MaxTimeoutMS < 0},
	}
	for _, check := range checks {
		if check.negative {
			return fmt.Errorf("config: %s must not be negative", check.name)
		}
	}
	if err := validateTurnBudget(c.Agent); err != nil {
		return err
	}
	if err := validateMCP(c.MCP.Servers); err != nil {
		return err
	}
	if err := validateToolResultStrategies(c.Agent); err != nil {
		return err
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	warnMissingDir(logger, "skills.dir", c.Skills.Dir)
	warnMissingDir(logger, "agents.dir", c.Agents.Dir)
	warnMissingDir(logger, "toolbox.dir", c.Toolbox.Dir)
	return nil
}

// validateTurnBudget rejects weights the budget cannot mean: a read turn may
// cost between nothing and a full turn, and the hard ceiling may not sit below
// the budget it is meant to bound. Zero is "unset" for both — WithDefaults
// fills it in with cmp.Or — so only a negative weight is rejected.
func validateTurnBudget(agent AgentConfig) error {
	if agent.ReadTurnWeight < 0 || agent.ReadTurnWeight > 1 {
		return fmt.Errorf("config: agent.read_turn_weight must be between 0 and 1")
	}
	if agent.HardTurnMultiplier != 0 && agent.HardTurnMultiplier < 1 {
		return fmt.Errorf("config: agent.hard_turn_multiplier must be at least 1")
	}
	return nil
}

// validateMCP enforces that each enabled server names exactly one transport:
// a stdio Command or an HTTP URL, never both and never neither.
func validateMCP(servers map[string]MCPServerConfig) error {
	for name, server := range servers {
		if !server.Enabled {
			continue
		}
		hasCommand, hasURL := strings.TrimSpace(server.Command) != "", strings.TrimSpace(server.URL) != ""
		if hasCommand == hasURL {
			return fmt.Errorf("config: mcp.servers.%s must set exactly one of command or url", name)
		}
	}
	return nil
}

// validateToolResultStrategies rejects unknown oversize-strategy names so a
// typo surfaces at startup instead of silently falling back to the default,
// and an aging threshold that would fire before the keep window.
func validateToolResultStrategies(agent AgentConfig) error {
	if age := agent.ToolResultAgeTurns; age < 0 || (age > 0 && age < agent.ToolResultKeepTurns) {
		return errors.New("config: agent.tool_result_age_turns must be at least agent.tool_result_keep_turns (and at least 1)")
	}
	valid := func(name string) bool {
		return name == "" || name == constants.ToolResultStrategySpill || name == constants.ToolResultStrategyCap
	}
	if !valid(agent.ToolResultStrategy) {
		return fmt.Errorf("config: agent.tool_result_strategy %q is not a known strategy", agent.ToolResultStrategy)
	}
	for tool, name := range agent.ToolResultStrategies {
		if !valid(name) {
			return fmt.Errorf("config: agent.tool_result_strategies.%s %q is not a known strategy", tool, name)
		}
	}
	return nil
}

func warnMissingDir(logger *zap.Logger, key, dir string) {
	if dir == "" {
		return
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		logger.Warn("configured directory does not exist", zap.String("key", key), zap.String("dir", dir))
	}
}
