package toolbox

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/support/strutil"
	"github.com/invokr-ai/invokr/tools/core"

	"go.uber.org/zap"
)

var toolboxSpecFilenames = []string{"tool.json", "plugin.json", "hamr.json"}

type toolboxSpec struct {
	Name          string                `json:"name"`
	Description   string                `json:"description"`
	Example       string                `json:"example"`
	Parameters    map[string]any        `json:"parameters"`
	Namespace     string                `json:"namespace"`
	AlwaysVisible *bool                 `json:"always_visible,omitempty"`
	Policy        core.ToolPolicy       `json:"policy"`
	Capabilities  core.ToolCapabilities `json:"capabilities"`
	// Daemon, when true, keeps the plugin subprocess alive across calls.
	// The process is started with --daemon and communicates via newline-delimited JSON.
	Daemon bool `json:"daemon"`
	// Limits optionally raises or lowers this tool's execution budget from the
	// process-wide defaults, bounded by the config-level caps.
	Limits *toolboxLimits `json:"limits,omitempty"`
	// Network optionally scopes network-at-exec to named hosts. Declaring
	// allow_hosts turns on the sandbox's network namespace and restricts name
	// resolution to those hosts; see applyNetwork for the approval tier both
	// this and a bare capabilities.network declaration take.
	Network toolboxNetwork `json:"network,omitempty"`
	// Command optionally exposes the tool as a user-facing slash command
	// (e.g. {"name": "browse", "usage": "/browse url=…"}). Approval gating
	// is unchanged: the command path raises the same tiers as a model call.
	Command *core.ToolCommand `json:"command,omitempty"`
}

type toolboxLimits struct {
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	MemoryMB       int `json:"memory_mb,omitempty"`
	OutputKB       int `json:"output_kb,omitempty"`
}

type toolboxNetwork struct {
	AllowHosts []string `json:"allow_hosts,omitempty"`
}

// applyLimits folds a manifest's limits block into the effective config for
// this one tool. A value above its configured cap is an error, not a clamp: a
// tool that asked for more than it may have should fail to load with a
// diagnostic rather than run under limits it never declared.
func applyLimits(cfg config.ToolboxConfig, limits *toolboxLimits) (config.ToolboxConfig, error) {
	if limits == nil {
		return cfg, nil
	}
	return cfg, cmp.Or(
		applyCap(&cfg.ExecTimeoutMS, limits.TimeoutSeconds, 1000, cfg.MaxLimitTimeoutMS, "timeout_seconds"),
		applyCap(&cfg.MemoryLimitBytes, limits.MemoryMB, 1024*1024, cfg.MaxLimitMemoryBytes, "memory_mb"),
		applyCap(&cfg.MaxOutputBytes, limits.OutputKB, 1024, cfg.MaxLimitOutputBytes, "output_kb"))
}

// applyCap sets *dst to asked*unit when positive, refusing values over limit.
func applyCap[T int | int64](dst *T, asked int, unit, limit T, field string) error {
	if v := T(asked) * unit; v > 0 {
		if v > limit {
			return fmt.Errorf("limits.%s %d exceeds cap of %d", field, asked, limit/unit)
		}
		*dst = v
	}
	return nil
}

// applyNetwork maps network-at-exec onto the capability the sandbox reads and
// the approval tier the broker reads, so it never bypasses the tier system it
// shares with shell and web_search. The lift keys off the *effective*
// capability, not off the allow_hosts block alone: a manifest declaring
// capabilities.network with no allow_hosts asks for the strictly wider,
// unscoped namespace, so it must not be the one route that escapes the tier
// (and, through an "auto" approval, the prompt). An explicit approval of
// "never" is left alone — that is a deliberate operator decision, not a
// manifest widening its own reach.
func applyNetwork(caps core.ToolCapabilities, policy core.ToolPolicy, allowHosts []string) (core.ToolCapabilities, core.ToolPolicy, []string) {
	caps.Network = caps.Network || len(allowHosts) > 0
	if !caps.Network {
		return caps, policy, nil
	}
	if !slices.Contains(policy.Tiers, constants.ShellTierNetwork) {
		policy.Tiers = append(policy.Tiers, constants.ShellTierNetwork)
	}
	if policy.Approval == "" || policy.Approval == constants.ApprovalPolicyAuto {
		policy.Approval = constants.ApprovalPolicyAsk
	}
	return caps, policy, allowHosts
}

func isRetiredTool(name string) bool {
	return name == "write_file" || name == "replace_in_file" || name == "read_file" || name == "read"
}

// ValidateSpecFields enforces the spec shape every toolbox tool must satisfy,
// whether it was hand-written or authored by create_tool: a tool created via
// create_tool must never be a tool the loader then refuses to load.
func ValidateSpecFields(name, description, example string, policy core.ToolPolicy) error {
	for _, field := range [][2]string{{"name", name}, {"description", description}, {"example", example}} {
		if strings.TrimSpace(field[1]) == "" {
			return fmt.Errorf("missing %s", field[0])
		}
	}
	if isRetiredTool(name) {
		return fmt.Errorf("retired tool %q is disabled; a built-in tool covers it", name)
	}
	if len(policy.Tiers) == 0 {
		return fmt.Errorf("missing policy.tiers")
	}
	if strings.TrimSpace(policy.Approval) == "" {
		return fmt.Errorf("missing policy.approval")
	}
	return nil
}

// ValidateRequirements applies the M3 pinning/VCS/URL/path parser gate plus the
// package allow/deny policy to requirements text supplied directly (e.g. by
// create_tool) rather than read from an existing requirements.txt on disk. It
// mirrors the install-time gate so a denied package is refused at authoring
// time instead of leaving a tool on disk that only fails on first run.
func ValidateRequirements(data []byte, allow, deny []string) error {
	names, err := parsePythonRequirementNames(data)
	if err != nil {
		return err
	}
	return enforcePackagePolicy(names, allow, deny)
}

func loadToolboxTool(pluginDir string, cfg config.ToolboxConfig, logger *zap.Logger) (*executableTool, error) {
	specPath, err := findToolboxSpecFile(pluginDir)
	if err != nil {
		return nil, err
	}

	b, err := os.ReadFile(specPath)
	if err != nil {
		return nil, fmt.Errorf("read tool spec: %w", err)
	}

	var spec toolboxSpec
	if err := json.Unmarshal(b, &spec); err != nil {
		return nil, fmt.Errorf("parse tool spec: %w", err)
	}
	if err := ValidateSpecFields(spec.Name, spec.Description, spec.Example, spec.Policy); err != nil {
		return nil, fmt.Errorf("tool spec %s: %w", specPath, err)
	}

	cfg, err = applyLimits(cfg, spec.Limits)
	if err != nil {
		return nil, fmt.Errorf("tool spec %s: %w", specPath, err)
	}

	runtime, entrypoint, err := inferRuntimeEntrypoint(pluginDir)
	if err != nil {
		return nil, err
	}

	command, args, err := runtimeCommand(runtime, entrypoint, cfg)
	if err != nil {
		return nil, err
	}

	alwaysVisible := spec.AlwaysVisible != nil && *spec.AlwaysVisible

	// Every toolbox tool is a subprocess running a script, so it executes code
	// by definition. The manifest's own value is not trusted here: capabilities
	// are self-declared, and for an agent-authored tool that means the model
	// declares them. A false negative widens what a standing grant may cover
	// (see newStandingGrants), so the loader asserts the truth instead.
	capabilities := spec.Capabilities
	capabilities.ExecutesCode = true
	capabilities, policy, allowHosts := applyNetwork(capabilities, spec.Policy, spec.Network.AllowHosts)
	return &executableTool{
		ToolSpec: core.ToolSpec{
			Name: spec.Name, Description: spec.Description, Example: spec.Example, Parameters: spec.Parameters,
			Metadata: core.ToolMetadata{
				Namespace: strutil.FirstNonEmptyTrimmed(spec.Namespace, constants.ToolboxSource), Source: source,
				Runtime: runtime, PluginDir: filepath.ToSlash(pluginDir), Policy: policy,
				Capabilities: capabilities, AlwaysVisible: alwaysVisible, Command: spec.Command,
			},
		},
		command: command, args: args, dir: pluginDir, Runtime: runtime,
		daemon: spec.Daemon, allowHosts: allowHosts, config: cfg, logger: logger,
	}, nil
}

// ValidateToolDir proves a staged tool can be loaded before it replaces a live tool.
func ValidateToolDir(pluginDir string, cfg config.ToolboxConfig) error {
	_, err := loadToolboxTool(pluginDir, cfg, zap.NewNop())
	return err
}

// existingFiles returns the names that exist in dir as regular files, in the
// order given.
func existingFiles(dir string, names []string) ([]string, error) {
	var found []string
	for _, name := range names {
		path := filepath.Join(dir, name)
		info, err := os.Stat(path)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("stat %s: %w", path, err)
		}
		if !info.IsDir() {
			found = append(found, name)
		}
	}
	return found, nil
}

func findToolboxSpecFile(pluginDir string) (string, error) {
	found, err := existingFiles(pluginDir, toolboxSpecFilenames)
	if err != nil {
		return "", err
	}
	if len(found) == 0 {
		return "", fmt.Errorf("missing tool spec in %s (expected one of: %s)", pluginDir, strings.Join(toolboxSpecFilenames, ", "))
	}
	return filepath.Join(pluginDir, found[0]), nil
}

// entrypointFiles are the entrypoints a toolbox tool may carry; exactly one of
// them must be present, and it decides the runtime.
var entrypointFiles = []string{"main.go", "main.js", "main.py"}

var entrypointRuntimes = map[string]string{"main.go": "go", "main.js": "node", "main.py": "python"}

func inferRuntimeEntrypoint(pluginDir string) (string, string, error) {
	found, err := existingFiles(pluginDir, entrypointFiles)
	if err != nil {
		return "", "", err
	}
	switch len(found) {
	case 0:
		return "", "", fmt.Errorf("toolbox tool %s is missing entrypoint (expected main.go, main.js, or main.py)", pluginDir)
	case 1:
		// Absolute: the tool may run from the workspace (see executableTool.runDir),
		// where neither a bare entrypoint name nor a relative plugin dir resolves.
		entrypoint, err := filepath.Abs(filepath.Join(pluginDir, found[0]))
		if err != nil {
			return "", "", fmt.Errorf("resolve toolbox entrypoint %s: %w", found[0], err)
		}
		return entrypointRuntimes[found[0]], entrypoint, nil
	default:
		return "", "", fmt.Errorf("toolbox tool %s has multiple entrypoints", pluginDir)
	}
}

func runtimeCommand(runtime string, entrypoint string, cfg config.ToolboxConfig) (string, []string, error) {
	bin, ok := map[string]string{"go": cfg.GoBin, "node": cfg.NodeBin, "python": cfg.PythonBin}[runtime]
	if !ok {
		return "", nil, fmt.Errorf("unsupported runtime %q", runtime)
	}
	if runtime == "go" {
		return bin, []string{"run", entrypoint}, nil
	}
	return bin, []string{entrypoint}, nil
}
