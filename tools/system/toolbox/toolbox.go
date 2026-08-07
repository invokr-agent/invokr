package toolbox

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/core/sandbox"
	"github.com/invokr-ai/invokr/tools/system/workspace"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"
)

const source = constants.ToolboxSource

const refreshDebounce = time.Second

type executableTool struct {
	core.ToolSpec
	command, dir, workingDir, Runtime string
	args, allowHosts, protected       []string
	allowHiddenPaths, daemon          bool
	config                            config.ToolboxConfig
	logger                            *zap.Logger
	approver                          approval.Approver
	dispatcher                        events.Dispatcher
}

type toolboxResult struct {
	Result string `json:"result"`
	Error  string `json:"error"`
}

type refreshHandler struct {
	registrar core.NativeRegistrar
	deps      core.CallerDeps
	logger    *zap.Logger
	mu        sync.Mutex
	lastDir   dirSignature
	lastScan  time.Time
}

// dirSignature detects toolbox directory changes without trusting mtime
// alone: some filesystems (notably container overlay filesystems used in
// CI) round directory mtimes to whole seconds, so two writes within the
// same second can report an unchanged mtime. Entry count catches the
// common case (a tool added/removed) that mtime alone can miss.
type dirSignature struct {
	modTime time.Time
	entries int
}

func (s dirSignature) Equal(o dirSignature) bool {
	return s.entries == o.entries && s.modTime.Equal(o.modTime)
}

// RegisterToolboxTools scans the toolbox dir into registrar and subscribes a
// refresh handler on the shared event bus. It returns the unregister func for
// that subscription: the caller must invoke it when the registrar's session
// ends, or handlers accumulate on the process-wide bus for every chat session
// and each refresh event re-scans the toolbox once per dead caller (#324).
func RegisterToolboxTools(registrar core.NativeRegistrar, deps core.CallerDeps, logger *zap.Logger) func() {
	if registrar == nil {
		return func() {}
	}
	h := &refreshHandler{registrar: registrar, deps: deps, logger: logger}
	h.refresh(true)
	if bus, ok := deps.Dispatcher.(events.Bus); ok {
		return bus.Register(h)
	}
	return func() {}
}

func (h *refreshHandler) HandleEvent(_ context.Context, event events.Event) error {
	if event.Type == events.EventToolboxRefreshRequested {
		h.refresh(false)
	}
	return nil
}

func (h *refreshHandler) refresh(force bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now()
	dir := toolboxDirSignature(h.deps.App.Toolbox.WithDefaults(config.InvokrHomeDir()).Dir)
	if !force && dir.Equal(h.lastDir) && now.Sub(h.lastScan) < refreshDebounce {
		return
	}

	tools := LoadToolboxTools(h.logger, h.deps)
	skipped := h.registrar.ReplaceNativeToolsBySource(source, tools)
	for _, name := range skipped {
		h.logger.Warn("toolbox tool ignored in favor of native tool", zap.String("name", name))
	}
	h.lastDir = dir
	h.lastScan = now
}

func toolboxDirSignature(dir string) dirSignature {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return dirSignature{}
	}
	info, err := os.Stat(dir)
	if err != nil {
		return dirSignature{}
	}
	return dirSignature{modTime: info.ModTime(), entries: len(entries)}
}

// sandboxAvailableFn is indirected so tests can exercise both platform branches
// of the registration skip below.
var sandboxAvailableFn = sandbox.Available

func LoadToolboxTools(logger *zap.Logger, deps core.CallerDeps) []core.DynamicNativeTool {
	cfg := deps.App.Toolbox.WithDefaults(config.InvokrHomeDir())
	var tools []core.DynamicNativeTool

	// Without a namespace sandbox every invocation is refused at call time, so
	// registering the tools only teaches the model to keep retrying them.
	if !sandboxAvailableFn() && !cfg.AllowUnsandboxed {
		logger.Warn("skipping toolbox tool registration: namespace sandboxing is unavailable on this platform; set INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1 to run unsandboxed (not recommended)")
		return tools
	}

	entries, err := os.ReadDir(cfg.Dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logger.Warn("failed reading toolbox directory", zap.String("dir", cfg.Dir), zap.Error(err))
		}
		return tools
	}

	for _, entry := range entries {
		tool, ok := loadDynamicToolboxTool(entry, cfg, deps, logger)
		if ok {
			tools = append(tools, tool)
		}
	}

	return tools
}

func loadDynamicToolboxTool(entry os.DirEntry, cfg config.ToolboxConfig, deps core.CallerDeps, logger *zap.Logger) (core.DynamicNativeTool, bool) {
	if !entry.IsDir() {
		return core.DynamicNativeTool{}, false
	}
	// Hidden dirs (per-tool .venv, .git) are never tool candidates, and
	// retired-tool leftovers are expected on disk — neither deserves the
	// "missing tool spec" warning the scanner used to emit for them.
	if strings.HasPrefix(entry.Name(), ".") {
		return core.DynamicNativeTool{}, false
	}
	if isRetiredTool(entry.Name()) {
		logger.Debug("skipping retired toolbox tool dir", zap.String("dir", entry.Name()))
		return core.DynamicNativeTool{}, false
	}
	pluginDir := filepath.Join(cfg.Dir, entry.Name())
	tool, err := loadToolboxTool(pluginDir, cfg, logger)
	if err == nil && deps.App.Workspace.Root != "" {
		tool.workingDir, err = filepath.Abs(deps.App.Workspace.Root)
	}
	if err != nil {
		logger.Warn("skipping toolbox tool", zap.String("dir", pluginDir), zap.Error(err))
		return core.DynamicNativeTool{}, false
	}
	tool.allowHiddenPaths = deps.App.Workspace.AllowHiddenPaths
	tool.approver = deps.Approver
	tool.protected = workspace.NewWorkspacePolicy(deps.App.Workspace).Protect(deps.App.Approvals.GrantsFile).ProtectedPaths()
	tool.dispatcher = deps.Dispatcher
	logger.Debug("loaded toolbox tool", zap.String("name", tool.Name), zap.String("runtime", tool.Runtime), zap.String("dir", pluginDir))
	return dynamicToolboxTool(tool, deps.Approver), true
}

func dynamicToolboxTool(tool *executableTool, approver approval.Approver) core.DynamicNativeTool {
	spec := tool.ToolSpec
	spec.Metadata.ManagedApproval = true
	return core.DynamicNativeTool{
		Spec: spec,
		Handler: func(ctx context.Context, call *core.ToolCall) (string, error) {
			local := *tool
			if approval.ModeFromContext(ctx) == approval.Auto {
				local.Metadata.Capabilities.Network = true
			}
			// Schema validation precedes both approval and exec: a call the
			// manifest's own schema rejects must never spawn the plugin, and
			// must never spend a human's approval prompt either.
			if err := core.ValidateArgs(tool.Parameters, call.Arguments); err != nil {
				return "", &core.ToolError{Code: "invalid_arguments", Err: err, Payload: fmt.Sprintf("invalid arguments for %s: %v", tool.Name, err)}
			}
			if err := approveToolboxCall(ctx, approver, &local, call); err != nil {
				return "", err
			}
			return local.Invoke(ctx, call)
		},
	}
}

func approveToolboxCall(ctx context.Context, approver approval.Approver, tool *executableTool, call *core.ToolCall) error {
	req := approval.Request{
		Subject: string(call.Arguments), Confined: sandbox.Available(), Writes: tool.Metadata.Capabilities.WritesWorkspace, Network: tool.Metadata.Capabilities.Network,
		Tool: tool.Name, Reason: "toolbox tool policy requires approval",
		Tier:    strings.Join(tool.Metadata.Policy.Tiers, ", "),
		Summary: fmt.Sprintf("Run toolbox tool %s", tool.Name),
		Details: fmt.Sprintf("Tool: %s\nPolicy: %s\nTiers: %s\nRuntime: %s\nDirectory: %s\nArguments: %s", tool.Name, tool.Metadata.Policy.Approval, strings.Join(tool.Metadata.Policy.Tiers, ", "), tool.Runtime, tool.Metadata.PluginDir, string(call.Arguments)),
	}
	decision := core.Ask(req)
	if tool.Metadata.Policy.Approval == "" || tool.Metadata.Policy.Approval == constants.ApprovalPolicyAuto {
		decision = core.Automatic(req)
	}
	return core.EnforceApproval(ctx, approver, tool.ToolSpec, decision)
}

func (p *executableTool) Invoke(ctx context.Context, call *core.ToolCall) (string, error) {
	reqBytes, err := json.Marshal(call)
	if err != nil {
		return "", fmt.Errorf("serialize tool call: %w", err)
	}

	command, env, err := p.prepareCommandAndEnv(ctx)
	if err != nil {
		return "", err
	}

	if p.config.ExecTimeoutMS > 0 {
		timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(p.config.ExecTimeoutMS)*time.Millisecond)
		defer cancel()
		ctx = timeoutCtx
	}
	if p.daemon {
		return p.invokeDaemonTool(ctx, command, env, reqBytes)
	}

	return p.invokeOneShotTool(ctx, command, env, reqBytes)
}

func (p *executableTool) invokeDaemonTool(ctx context.Context, command string, env []string, reqBytes []byte) (string, error) {
	sessionID := core.SessionIDFromContext(ctx)
	if sessionID == "" {
		return "", fmt.Errorf("toolbox daemon %q: missing session ID", p.Name)
	}
	env = core.WithEnvValue(env, "INVOKR_SESSION_ID", sessionID)
	modeID := sessionID + "|" + string(approval.ModeFromContext(ctx))
	stateDir, err := p.daemonStateDir(env, modeID)
	if err != nil {
		return "", err
	}
	env = core.WithEnvValue(env, "INVOKR_TOOL_STATE_DIR", stateDir)
	key := daemonKey{pluginDir: p.dir, sessionID: modeID}
	dp, err := getDaemon(key, p.config.MaxOutputBytes, func() (*exec.Cmd, func(), error) {
		cmd, cleanup, err := p.buildCommand(context.WithoutCancel(ctx), command, env, append([]string{"--daemon"}, p.args...))
		if err != nil {
			return cmd, cleanup, err
		}
		release := approval.Retain(ctx)
		return cmd, func() {
			release()
			if cleanup != nil {
				cleanup()
			}
		}, nil
	}, env)
	if err != nil {
		return "", fmt.Errorf("toolbox daemon %q: %w", p.Name, err)
	}
	raw, err := invokeDaemon(ctx, dp, reqBytes)
	if err != nil {
		// The daemon may have crashed; evict it so the next call restarts it.
		// Only evict if dp is still the registered daemon — a concurrent caller
		// may have already restarted a fresh replacement.
		evictDaemon(key, dp)
		return "", fmt.Errorf("toolbox daemon %q: %w", p.Name, err)
	}
	return parseDaemonResponse(p.Name, raw)
}

func (p *executableTool) daemonStateDir(env []string, sessionID string) (string, error) {
	stateRoot := core.FirstNonEmptyTrimmed(core.GetEnvValue(env, "INVOKR_TOOL_STATE_DIR"), core.GetEnvValue(env, "INVOKR_BROWSER_AGENT_STATE"))
	if stateRoot == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("toolbox daemon %q: determine state directory: %w", p.Name, err)
		}
		stateRoot = filepath.Join(home, ".local", "share", "invokr", "tool-state")
	}
	return filepath.Join(stateRoot, safeStateDir("plugin", p.dir), safeStateDir("session", sessionID)), nil
}

func safeStateDir(kind, value string) string { return kind + "-" + core.HashHex(value)[:16] }

func (p *executableTool) invokeOneShotTool(ctx context.Context, command string, env []string, reqBytes []byte) (string, error) {
	cmd, cleanup, err := p.buildCommand(ctx, command, env, p.args)
	if err != nil {
		return "", err
	}
	if cleanup != nil {
		defer cleanup()
	}
	cmd.Env = env
	cmd.Stdin = bytes.NewReader(reqBytes)

	res, err := core.RunCollect(ctx, cmd, p.config.MaxOutputBytes, &progressWriter{ctx: ctx, tool: p})
	outText := strings.TrimSpace(res.Stdout)
	errText := strings.TrimSpace(res.Stderr)
	if err != nil {
		detail := cmp.Or(joinNonEmpty([]string{outText, errText, err.Error()}, "\n"), "unknown execution error")
		return "", fmt.Errorf("toolbox tool %q failed: %s", p.Name, detail)
	}

	if res.Truncated {
		// Completed but flagged: model still sees the truncated output (Payload).
		// session_tools.go surfaces Payload to the model but not Code, so the
		// marker is appended inline or the model can't tell output is incomplete.
		return "", &core.ToolError{Code: "output_truncated",
			Err:     fmt.Errorf("toolbox tool %q output exceeded %d bytes", p.Name, p.config.MaxOutputBytes),
			Payload: outText + fmt.Sprintf("\n\n[output truncated: exceeded %d bytes]", p.config.MaxOutputBytes)}
	}

	if outText == "" {
		return "", nil
	}
	return pluginResult(p.Name, outText)
}

func (p *executableTool) prepareCommandAndEnv(ctx context.Context) (string, []string, error) {
	command := p.command
	env := pluginEnv(p.Runtime, p.config)
	if strings.TrimSpace(p.workingDir) != "" {
		env = core.WithEnvValue(env, "INVOKR_WORKING_DIR", p.workingDir)
	}
	env = core.WithEnvValue(env, "INVOKR_TOOL_DIR", p.dir)
	if p.allowHiddenPaths {
		env = core.WithEnvValue(env, "INVOKR_WORKSPACE_ALLOW_HIDDEN_PATHS", "1")
	}
	if p.Runtime != "python" {
		return command, env, nil
	}
	pythonBin, pythonEnv, err := preparePythonPluginEnvLocked(ctx, p, env)
	if err != nil {
		return "", nil, fmt.Errorf("prepare python toolbox tool %q: %w", p.Name, err)
	}
	return pythonBin, pythonEnv, nil
}

// NamespaceSandboxAvailable reports whether toolbox tools can run sandboxed on
// this host: the platform must be Linux and unprivileged user namespaces must
// actually work. Exported for `invokr doctor`, which has to explain an
// unavailable sandbox before any tool is ever invoked.
func NamespaceSandboxAvailable() bool { return sandbox.Available() }

// buildCommand wraps command/p.args in the namespace sandbox when one is
// available, falling back to a direct host process only if the operator has
// explicitly opted in via INVOKR_TOOLBOX_ALLOW_UNSANDBOXED. No silent host
// fallback: if neither is true, toolbox execution is refused.
func (p *executableTool) buildCommand(ctx context.Context, command string, env []string, args []string) (*exec.Cmd, func(), error) {
	mode := approval.ModeFromContext(ctx)
	if mode == approval.Plan {
		return nil, nil, fmt.Errorf("toolbox tools are unavailable in Plan")
	}
	if sandbox.Available() && mode != approval.YOLO {
		return p.buildSandboxedCommand(ctx, command, env, args)
	}
	if !p.config.AllowUnsandboxed && mode != approval.YOLO {
		return nil, nil, fmt.Errorf("toolbox tool %q: namespace sandboxing is unavailable on this platform; set INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1 to run unsandboxed (not recommended)", p.Name)
	}
	p.logger.Warn("running toolbox tool unsandboxed", zap.String("name", p.Name))
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Dir = p.runDir()
	return cmd, nil, nil
}

// runDir is the working directory a toolbox tool runs in: the workspace root,
// so a relative path argument means the same thing it does for native tools
// (#535). The tool's own directory holds its code, not its data, and stays
// reachable through INVOKR_TOOL_DIR. Falls back to the tool directory when
// there is no workspace, when the tool declares no workspace capability (the
// sandbox only mounts the workspace for tools that do), or for Go.
func (p *executableTool) runDir() string {
	caps := p.Metadata.Capabilities
	// Go plugins are excluded: `go run` resolves modules against its working
	// directory, so a plugin importing its own go.mod dependency or a sibling
	// package only builds from the plugin dir. Go tools use INVOKR_WORKING_DIR.
	if wd := strings.TrimSpace(p.workingDir); wd != "" && p.Runtime != "go" && (caps.ReadsWorkspace || caps.WritesWorkspace) {
		return wd
	}
	if abs, err := filepath.Abs(p.dir); err == nil {
		return abs
	}
	return p.dir
}

func (p *executableTool) buildSandboxedCommand(ctx context.Context, command string, env []string, args []string) (*exec.Cmd, func(), error) {
	resolvedCommand, err := resolveAbsoluteCommandPath(command)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve toolbox tool %q command %q: %w", p.Name, command, err)
	}
	// toolDir must be absolute: it is bind-mounted at this exact path and
	// then cd'd into after pivot_root, where a relative path would no
	// longer resolve against the original host working directory.
	toolDir, err := filepath.Abs(p.dir)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve toolbox tool %q directory %q: %w", p.Name, p.dir, err)
	}
	venvDir := ""
	if p.Runtime == "python" {
		venvDir = pythonVenvDir(toolDir, p.config)
	}
	plan := sandbox.BuildPlan(resolvedCommand, toolDir, venvDir, core.GetEnvValue(env, "INVOKR_WORKING_DIR"), p.Metadata.Capabilities, p.config.MemoryLimitBytes, p.allowHosts)
	mode := approval.ModeFromContext(ctx)
	if mode == approval.Auto {
		plan.Sealed = p.protected
	}
	if stateDir := core.GetEnvValue(env, "INVOKR_TOOL_STATE_DIR"); p.daemon && stateDir != "" {
		if err := os.MkdirAll(stateDir, 0o700); err != nil {
			return nil, nil, fmt.Errorf("create toolbox tool %q state directory: %w", p.Name, err)
		}
		plan.RW = append(plan.RW, stateDir)
		plan = sandbox.DedupPlan(plan)
	}
	if goroot := core.GetEnvValue(env, "GOROOT"); goroot != "" {
		plan.RO = append(plan.RO, goroot)
	}
	// GOCACHE must be writable inside the sandbox: /tmp is a fresh tmpfs per
	// invocation, so without a persistent cache bind Go tools recompile the
	// whole dependency graph on every call.
	if gocache := core.GetEnvValue(env, "GOCACHE"); gocache != "" {
		plan.RW = append(plan.RW, gocache)
	}
	cmd, cleanup, err := sandbox.Command(ctx, plan, p.runDir(), args)
	if err != nil {
		return nil, nil, fmt.Errorf("sandbox toolbox tool %q: %w", p.Name, err)
	}
	return cmd, cleanup, nil
}

// resolveAbsoluteCommandPath turns a runtime binary name (e.g. "go", resolved
// via host PATH) into an absolute path. The sandboxed process has no PATH of
// its own, and the sandbox only bind-mounts paths it knows about, so the
// command must be resolved and its containing directory bind-mounted before
// the sandboxed exec can find it.
func resolveAbsoluteCommandPath(command string) (string, error) {
	if filepath.IsAbs(command) {
		return command, nil
	}
	resolved, err := exec.LookPath(command)
	if err != nil {
		return "", err
	}
	return filepath.Abs(resolved)
}

func pluginResult(name string, outText string) (string, error) {
	var result toolboxResult
	if err := json.Unmarshal([]byte(outText), &result); err == nil && (result.Result != "" || result.Error != "") {
		if strings.TrimSpace(result.Error) != "" {
			return "", fmt.Errorf("toolbox tool %q returned error: %s", name, strings.TrimSpace(result.Error))
		}
		return result.Result, nil
	}
	return outText, nil
}

func pluginEnv(runtime string, cfg config.ToolboxConfig) []string {
	var env []string
	if !cfg.IsolateHostEnv() {
		env = os.Environ()
	} else if path, ok := os.LookupEnv("PATH"); ok {
		// The unsandboxed INVOKR_TOOLBOX_ALLOW_UNSANDBOXED path runs the tool
		// directly with this env; without PATH it can't spawn subprocesses by
		// bare name. The sandboxed path is unaffected: its init script exports
		// its own PATH.
		env = core.WithEnvValue(env, "PATH", path)
	}
	for key, value := range cfg.Env {
		env = core.WithEnvValue(env, key, value)
	}
	env = core.WithEnvValue(env, "INVOKR_GO_BIN", core.FirstNonEmptyTrimmed(cfg.GoBin, constants.DefaultGoBin))
	if strings.TrimSpace(cfg.BrowserAgentState) != "" {
		env = core.WithEnvValue(env, "INVOKR_TOOL_STATE_DIR", cfg.BrowserAgentState)
	}
	if runtime == "go" {
		if core.GetEnvValue(env, "GOCACHE") == "" {
			cacheDir := core.FirstNonEmptyTrimmed(cfg.GoCacheDir, constants.DefaultGoCacheDir)
			_ = os.MkdirAll(cacheDir, 0700)
			env = core.WithEnvValue(env, "GOCACHE", cacheDir)
		}
		if core.GetEnvValue(env, "GOROOT") == "" {
			// Release go binaries are built trimmed: they can't infer GOROOT
			// from their own executable path inside the sandbox (which only
			// bind-mounts the binary's containing directory, not the whole
			// toolchain tree), so it must be resolved on the host and passed
			// through explicitly.
			if goroot, err := goRootFor(core.FirstNonEmptyTrimmed(cfg.GoBin, constants.DefaultGoBin)); err == nil && goroot != "" {
				env = core.WithEnvValue(env, "GOROOT", goroot)
			}
		}
	}

	return env
}

var (
	goRootMu    sync.Mutex
	goRootCache = make(map[string]string)
)

// goRootFor caches its result per goBin: GOROOT does not change during the
// process lifetime, so a Go toolbox tool invocation should not pay for a
// `go env GOROOT` subprocess every time.
func goRootFor(goBin string) (string, error) {
	goRootMu.Lock()
	defer goRootMu.Unlock()
	if val, ok := goRootCache[goBin]; ok {
		return val, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, goBin, "env", "GOROOT").Output()
	if err != nil {
		return "", err
	}
	val := strings.TrimSpace(string(out))

	goRootCache[goBin] = val

	return val, nil
}

func joinNonEmpty(parts []string, sep string) string {
	cleaned := core.MapSlice(parts, strings.TrimSpace)
	return strings.Join(slices.DeleteFunc(cleaned, func(part string) bool { return part == "" }), sep)
}
