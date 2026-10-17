package shell

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/invokr-ai/invokr/platform/turn"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/support/fsutil"
	"github.com/invokr-ai/invokr/support/mathutil"
	"github.com/invokr-ai/invokr/support/strutil"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/workspace"
)

type shellArgs struct {
	Command        string   `json:"command" desc:"A bash command line. Pipes, redirects, heredocs, $VAR, $(...), loops and ; all work. Every command it runs must be on the allowlist."`
	Args           []string `json:"args,omitempty" desc:"Optional extra arguments appended to the command line, one per string."`
	Cwd            string   `json:"cwd,omitempty" desc:"Optional working directory, relative to workspace root."`
	TimeoutMS      int      `json:"timeout_ms,omitempty" desc:"Optional timeout override in milliseconds, capped at 120000."`
	MaxOutputBytes int      `json:"max_output_bytes,omitempty" desc:"Optional max stdout+stderr bytes to retain, capped at 200000."`
	Background     bool     `json:"background,omitempty" desc:"Start command in background and return a process_id."`
	Network        bool     `json:"network,omitempty" desc:"Request network access for this command; Plan requires approval."`
	Intent         string   `json:"intent,omitempty" desc:"One sentence: what this command is for, in the user's terms. Shown to the user when approval is asked."`
}

type ShellResult struct {
	Command   string `json:"command"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	TimedOut  bool   `json:"timed_out"`
	Truncated bool   `json:"truncated"`
	// Cwd is the directory the line ran in, relative to the workspace root.
	Cwd string `json:"cwd,omitempty"`
	// Hint tells the model what to do about an envelope it cannot fix by
	// rewriting the command — today only a timeout.
	Hint string `json:"hint,omitempty"`
	// Sandboxed marks a line that ran inside the namespace sandbox with the
	// workspace as its only writable path.
	Sandboxed bool `json:"sandboxed,omitempty"`
}

type BackgroundResult struct {
	ProcessID string `json:"process_id"`
	LogPath   string `json:"log_path"`
}

type preparedShellCommand struct {
	mode       approval.Mode
	publishes  bool
	foreground bool
	protected  []string
	args       shellArgs
	// line is the bash command line that runs, after any extra args were
	// appended.
	line string
	// command is the first command root, kept for audit and ledger labels;
	// commands are all of them, which the sandbox resolves on the host so their
	// directories are mounted.
	command, cwd      string
	commands, reasons []string
	allowedCommands   []string
	tier              shellTier
	// workspaceRoot and roots are the sandbox's writable mounts;
	// readonlyRoots are mounted read-only, so a write-tier line cannot touch
	// a reference checkout it may still read.
	workspaceRoot        string
	roots, readonlyRoots []string
	// sandboxExec marks a line running project code (an interpreter or a
	// workspace script), which is always asked for.
	sandboxExec, isolateEnv bool
	// allowNetwork mirrors config's allow_network_commands: the operator has
	// said unattended network access is fine, so the sandbox keeps the host
	// network namespace instead of unsharing it (#543).
	allowNetwork              bool
	timeoutMS, maxOutputBytes int
	env, passthrough          []string
}

// isolatedEnvPassthrough names host env vars always forwarded to shell
// commands when the host env is isolated. EnvPassthrough adds to this set.
// GOMODCACHE/GOPATH/GOCACHE are included so `go` inside the sandbox resolves
// the same module cache the sandbox mounted from the host (tools/system/shell/exec.go's
// goModCache/goBuildCache) instead of falling back to $HOME/go/pkg/mod;
// JAVA_HOME/GRADLE_USER_HOME do the same for the JDK and gradle home the
// sandbox mounts (exec.go's jvmSupportDirs/jvmCacheDirs, #546).
var isolatedEnvPassthrough = []string{"PATH", "HOME", "LANG", "TERM", "TMPDIR", "GOMODCACHE", "GOPATH", "GOCACHE", "JAVA_HOME", "GRADLE_USER_HOME"}

// buildShellEnv returns the minimal environment for an isolated shell command:
// the always-forwarded vars plus any configured passthrough names that are set
// in the host process.
// IsolatedEnv is the environment model-supplied code runs under: the
// always-forwarded vars and none of the agent's secrets.
func IsolatedEnv() []string { return buildShellEnv(nil) }

func buildShellEnv(passthrough []string) []string {
	names := slices.Concat(isolatedEnvPassthrough, passthrough)
	for i, name := range names {
		names[i] = strings.TrimSpace(name)
	}
	env := []string{}
	for _, name := range core.DedupPreserveOrder(names) {
		if value, ok := os.LookupEnv(name); ok && name != "" {
			env = append(env, name+"="+value)
		}
	}
	return env
}

type shellTier string

const (
	shellTierReadonly shellTier = constants.ShellTierReadonly
	shellTierWrite    shellTier = constants.ShellTierWrite
	shellTierNetwork  shellTier = constants.ShellTierNetwork
)

// globalValueFlags names pre-subcommand flags that consume the next argument.
// git's `-c` is deliberately absent: config keys like core.fsmonitor or
// diff.external make git execute arbitrary binaries, so `-c` fails closed to
// the write tier (#318).
var globalValueFlags = map[string]map[string]struct{}{
	"git": {"-C": {}},
	"go":  {"-C": {}},
}

func RegisterShellTool(c core.NativeRegistrar, deps core.CallerDeps, workspace workspace.Policy) {
	cfg := deps.App.Shell
	yolo := deps.Policy != nil && deps.Policy.Mode() == approval.YOLO
	if yolo {
		cfg.AllowAnyCommand = true
	}
	cfg.AllowedCommands = constants.ShellAllowedCommands(cfg.AllowedCommands)
	allowedList := core.NormalizeStrings(cfg.AllowedCommands, false)
	allowed := core.StringSet(allowedList)
	core.Register(c, core.Tool[preparedShellCommand]{
		Spec: core.ToolSpec{
			Name:        constants.ShellToolName,
			Description: shellDescription(allowedList, workspace.Root, yolo),
			Example:     `{"name":"shell","arguments":{"command":"git status","cwd":".","timeout_ms":30000,"max_output_bytes":20000}}`,
			Parameters:  core.SchemaFor[shellArgs](),
			Metadata:    core.NativeMetadata("system", []string{string(shellTierReadonly), string(shellTierWrite), string(shellTierNetwork)}, constants.ApprovalPolicyAsk, core.ToolCapabilities{ReadsWorkspace: true, WritesWorkspace: true, Network: true, ExecutesCode: true}),
		},
		Decode: func(call *core.ToolCall) (preparedShellCommand, error) {
			return prepareShellCommand(call, cfg, workspace, allowed, allowedList, deps.Policy.Mode() == approval.YOLO)
		},
		Prepare: func(ctx context.Context, p preparedShellCommand) (preparedShellCommand, error) {
			p.mode = approval.ModeFromContext(ctx)
			p.protected = workspace.ProtectedPaths()
			if p.mode != approval.Manual {
				p.allowNetwork = p.mode != approval.Plan || p.args.Network
			}
			return p, nil
		},
		Approve: func(ctx context.Context, plan preparedShellCommand) core.ApprovalDecision {
			return modeShellApproval(cfg, plan)
		},
		Approver: deps.Approver,
		Execute: func(ctx context.Context, plan preparedShellCommand) (string, error) {
			if plan.args.Background {
				return startBackgroundShellCommand(deps.Dispatcher, ctx, plan)
			}
			return runPreparedShellCommand(deps.Dispatcher, ctx, plan)
		},
	})
	RegisterProcessTool(c, deps.Dispatcher)
}

// shellDescription is language-neutral by design (#306): the allowlist is a
// plain list and tiers are described generically — Go-specific tier detail
// stays in the internal rule table (rules.go), out of the prompt.
func shellDescription(allowedList []string, root string, yolo bool) string {
	confinement, approvalText, routing := sandboxConfinement(false, false), "Read-only lines need no approval; writes, $(...), and network commands require approval.", scriptRoutingNote()
	allowedText := strings.Join(allowedList, ", ")
	if yolo {
		confinement, approvalText, routing = "runs UNSANDBOXED on the host", "YOLO mode skips shell approvals.", " Interpreters and workspace scripts also run on the host."
		allowedText = "any command"
	}
	desc := fmt.Sprintf("Run bash in the workspace (%s). Allowed commands: %s.%s %s Use cwd within the workspace. /tmp persists between calls; access it with `cd /tmp/x && ...`. Output truncates at max_output_bytes; no output becomes \"(no output)\". Use tmux for processes that must survive this call.",
		confinement, allowedText, routing, approvalText)
	if note := projectGapNote(root, allowedList); note != "" && !yolo {
		desc += " " + note
	}
	return desc
}

func prepareShellCommand(call *core.ToolCall, cfg config.ShellConfig, workspace workspace.Policy, allowed map[string]struct{}, allowedList []string, unrestricted ...bool) (preparedShellCommand, error) {
	args, err := core.DecodeOptionalToolArgs[shellArgs](call)
	if err != nil {
		return preparedShellCommand{}, fmt.Errorf("parse shell arguments: %w", err)
	}
	line := commandLine(args)
	if line == "" {
		return preparedShellCommand{}, fmt.Errorf("command is required")
	}
	cwd, err := resolveShellCWD(args.Cwd, workspace)
	if err != nil {
		return preparedShellCommand{}, err
	}
	pol := policy{tier: shellTierWrite}
	if len(unrestricted) == 0 || !unrestricted[0] {
		pol, err = classifyLine(line, allowed, allowedList, workspace, cwd, cfg.AllowAnyCommand)
	}
	if err != nil {
		return preparedShellCommand{}, err
	}
	if pol.sandboxExec && args.Background {
		return preparedShellCommand{}, fmt.Errorf("%q runs project code and cannot run in the background: drop \"background\"", pol.roots[0])
	}
	timeoutMS, maxOutputBytes := shellLimits(args)
	isolate := cfg.IsolateHostEnv()
	var env []string
	if isolate {
		env = buildShellEnv(cfg.EnvPassthrough)
	}
	command := "bash"
	if len(pol.roots) > 0 {
		command = pol.roots[0]
	}
	return preparedShellCommand{args: args, line: line, command: command, commands: pol.roots, allowedCommands: allowedList, tier: pol.tier, publishes: pol.publishes, reasons: pol.reasons, cwd: cwd, workspaceRoot: workspace.Root, roots: workspace.WritableRoots(), readonlyRoots: workspace.ReadOnlyRoots(), sandboxExec: pol.sandboxExec, allowNetwork: cfg.AllowNetworkCommands, timeoutMS: timeoutMS, maxOutputBytes: maxOutputBytes, isolateEnv: isolate, env: env, passthrough: cfg.EnvPassthrough}, nil
}

// networkAllowed reports whether the sandbox shares the host network
// namespace. An unshared netns has no route and no DNS, so a network-tier
// line (or any line at all once the operator has allowed network commands
// unattended) would fail on "Could not resolve host" (#543).
func (p preparedShellCommand) networkAllowed() bool {
	return p.tier == shellTierNetwork || p.allowNetwork
}

// commandLine joins "command" with any extra "args". The recurring miscall is
// stating the same invocation twice ({"command":"ls -la","args":["ls","-la"]}),
// which naive appending turns into `ls -la ls -la`; so args that merely repeat
// the line's words — with or without the executable in front — are dropped.
func commandLine(args shellArgs) string {
	line := strings.TrimSpace(args.Command)
	extra := args.Args
	words := strings.Fields(line)
	switch {
	case len(extra) == 0 || len(words) == 0:
	case slices.Equal(extra, words), len(words) > 1 && slices.Equal(extra, words[1:]):
		extra = nil
	case extra[0] == words[0]:
		extra = extra[1:]
	}
	for _, arg := range extra {
		line += " " + quoteArg(arg)
	}
	return line
}

// quoteArg single-quotes an argument for bash unless it is plainly safe.
func quoteArg(arg string) string {
	if arg != "" && !strings.ContainsAny(arg, " \t\n'\"\\$`|&;<>()*?[]{}!#~") {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// shellLimits clamps the call's timeout and output budget to the tool's caps,
// falling back to the defaults when the model named neither.
func shellLimits(args shellArgs) (int, int) {
	return mathutil.ClampInt(args.TimeoutMS, constants.ShellDefaultTimeoutMS, constants.ShellMaxTimeoutMS),
		mathutil.ClampInt(args.MaxOutputBytes, constants.ShellDefaultMaxOutputBytes, constants.ShellMaxOutputBytes)
}

func runPreparedShellCommand(dispatcher events.Dispatcher, ctx context.Context, plan preparedShellCommand) (string, error) {
	result, err := runLine(dispatcher, ctx, plan)
	if err != nil {
		return "", err
	}
	out := result.text()
	if result.ExitCode != 0 && !benignExit(plan.command, result.ExitCode) {
		// Flag a non-zero exit, but still return the structured output (incl.
		// exit_code/stderr) to the model via ToolError.Payload.
		code := fmt.Sprintf("exit %d", result.ExitCode)
		if result.TimedOut {
			code = "timeout"
		}
		return out, &core.ToolError{Code: code, Payload: out, Err: fmt.Errorf("%s exited %d", plan.command, result.ExitCode)}
	}
	return out, nil
}

// text renders the result for the model as plain text: stdout as it came, then
// only the facts that matter, one per line. The JSON envelope it replaces
// echoed the command and escaped every newline and ampersand, which pi's raw
// bash output does not (#577).
func (r ShellResult) text() string {
	var b strings.Builder
	b.WriteString(r.Stdout)
	if r.Stderr != "" && r.Stderr != r.Stdout {
		b.WriteString("\n[stderr]\n" + r.Stderr)
	}
	for _, fact := range []struct {
		on   bool
		line string
	}{
		{r.ExitCode != 0, fmt.Sprintf("[exit code %d]", r.ExitCode)},
		{r.TimedOut, "[timed out]"},
		{r.Truncated, "[output truncated]"},
		{r.Cwd != "" && r.Cwd != ".", "[cwd " + r.Cwd + "]"},
		{r.Hint != "", "[hint] " + r.Hint},
	} {
		if fact.on {
			b.WriteString("\n" + fact.line)
		}
	}
	return b.String()
}

// runLine runs the line and returns its captured result. A process that could
// not be started at all is a hard error; a non-zero exit is not.
func runLine(dispatcher events.Dispatcher, ctx context.Context, plan preparedShellCommand) (ShellResult, error) {
	plan.foreground = true
	spec, err := processSpec(plan)
	if err != nil {
		dispatchShellAudit(dispatcher, ctx, plan, "failed", err)
		return ShellResult{}, err
	}
	spec.Timeout = time.Duration(plan.timeoutMS) * time.Millisecond
	spec.OutputLimit = plan.maxOutputBytes
	res, err := core.RunProcess(ctx, spec)
	pipeDelay := errors.Is(err, exec.ErrWaitDelay)
	if err != nil && !pipeDelay {
		if _, ok := errors.AsType[*exec.ExitError](err); !ok && !res.TimedOut {
			dispatchShellAudit(dispatcher, ctx, plan, "failed", err)
			return ShellResult{}, fmt.Errorf("run %s: %w", plan.command, err)
		}
	}
	events.RecordExecution(ctx, turn.ExecutionEvidence{Command: plan.line, Cwd: displayCwd(plan.workspaceRoot, plan.cwd), ExitCode: new(res.ExitCode), ReadOnly: plan.tier == shellTierReadonly})
	dispatchShellAudit(dispatcher, ctx, plan, "completed", nil)
	hint := backgroundHint(plan.line)
	if pipeDelay {
		res.Truncated = true
		hint = `shell exited but a background process kept output pipes open; capture stopped and output may be incomplete. Use the tmux tool for long-running commands: {"action":"open","command":"<your command>"}`
	}
	if res.TimedOut {
		hint = timeoutHint(plan.timeoutMS)
	}
	return ShellResult{Command: plan.line, Stdout: noOutputMarker(strings.TrimSpace(res.Stdout), strings.TrimSpace(res.Stderr)), Stderr: strings.TrimSpace(res.Stderr), ExitCode: res.ExitCode, TimedOut: res.TimedOut, Truncated: res.Truncated, Hint: hint, Cwd: displayCwd(plan.workspaceRoot, plan.cwd), Sandboxed: spec.Build != nil}, nil
}

// timeoutHint must not nudge a blind retry with a longer timeout: a runaway
// launcher re-run at 70 s instead of 30 s exhausted the host (2026-09-28).
func timeoutHint(timeoutMS int) string {
	return fmt.Sprintf("killed after %d ms together with all its child processes. A timeout can mean the command spawned runaway children or never terminates: inspect the program (a launcher that re-executes itself, missing exit conditions, unbounded concurrency) before retrying. Raise timeout_ms only for a command known to be legitimately slow.", timeoutMS)
}

// backgroundHint points a line that tries to outlive the call at the tool that
// can: the model otherwise burns turns re-inventing tmux through the shell
// (#527). It is a hint on a normal result, not an error — the command itself
// may well have succeeded.
func backgroundHint(line string) string {
	trimmed := strings.TrimSpace(line)
	backgrounded := strings.HasSuffix(trimmed, "&") && !strings.HasSuffix(trimmed, "&&")
	if !backgrounded && !containsAnyWord(trimmed, "tmux", "nohup", "setsid") {
		return ""
	}
	return `background processes do not survive this call; use the tmux tool: {"action":"open","command":"<your command>"}`
}

// containsAnyWord reports whether line runs any of the named commands, matched
// on word boundaries so "tmuxinator" or a path fragment does not trigger.
func containsAnyWord(line string, words ...string) bool {
	fields := strings.FieldsFunc(line, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ';' || r == '|' || r == '&' || r == '(' || r == ')'
	})
	for _, field := range fields {
		if slices.Contains(words, filepath.Base(field)) {
			return true
		}
	}
	return false
}

// noOutputMarker names a silent command explicitly. An empty stdout field
// read to the model as a truncation or a broken tool, and it retried a command
// that had succeeded without printing anything.
func noOutputMarker(stdout, stderr string) string {
	if stdout == "" && stderr == "" {
		return constants.ShellNoOutputMarker
	}
	return stdout
}

// displayCwd renders a directory the way the model should address it:
// relative to the workspace root. Reporting the absolute host path taught the
// model to issue absolute paths, which the sandbox does not have.
func displayCwd(root, dir string) string {
	if root == "" || dir == "" {
		return dir
	}
	if rel, ok := fsutil.RelInside(root, dir); ok {
		return rel
	}
	return dir
}

// interpreterNote answers an interpreter that could not be routed into the
// sandbox with the tool that does run code, rather than the allowlist.
func interpreterNote(command string) string {
	if slices.Contains(sandboxInterpreters, command) {
		return "the shell tool runs no interpreters. To run code use code_interpreter: pass the code itself (not a script path), name any files it reads in input_files, and to produce a workspace file write it under artifacts/ and name it in output_files."
	}
	return "it is not an allowed executable"
}

// shellCommandLabelMax is where a rejected command line stops being quotable
// in an error message and gets reduced to its head.
const shellCommandLabelMax = 60

func shellCommandLabel(command string) string {
	if len(command) <= shellCommandLabelMax {
		return command
	}
	return command[:shellCommandLabelMax] + "…"
}

func classifyShellCommand(command string, args []string) shellTier {
	rule, ok := commandRules[command]
	if !ok {
		return shellTierWrite
	}
	if command == "git" {
		return classifyGitCommand(args, rule)
	}
	return classifyByRule(command, args, rule)
}

// classifyGitCommand keeps the sub-argument inspection that git's branch and
// stash subcommands need; every other subcommand is data in commandRules.
func classifyGitCommand(args []string, rule commandRule) shellTier {
	if hasWriteFlag(args, rule.WriteFlags) {
		return shellTierWrite
	}
	subcommand, tier, done := classifySubcommand("git", args)
	if done {
		return tier
	}
	if subcommand != "branch" && subcommand != "stash" {
		return classifySubcommandTier(rule, subcommand)
	}
	subArgs := argsAfterSubcommand(args, subcommand)
	if subcommand == "stash" {
		if len(subArgs) > 0 && subArgs[0] == "list" {
			return shellTierReadonly
		}
		return shellTierWrite
	}
	if len(subArgs) == 0 || slices.Contains([]string{"-a", "--all", "-r", "--remotes", "--list"}, subArgs[0]) {
		return shellTierReadonly
	}
	return shellTierWrite
}

func classifySubcommand(command string, args []string) (string, shellTier, bool) {
	for index := 0; index < len(args); index++ {
		arg := args[index]
		if !strings.HasPrefix(arg, "-") {
			return arg, "", false
		}
		if arg == "--version" || arg == "--help" {
			return "", shellTierReadonly, true
		}
		if _, ok := globalValueFlags[command][arg]; !ok || index+1 >= len(args) {
			return "", shellTierWrite, true
		}
		index++
		// `-C <dir>` retargets the whole invocation; an absolute or
		// parent-escaping dir would read outside the workspace at the
		// no-approval tier (#318).
		if argEscapesWorkspace(args[index]) {
			return "", shellTierWrite, true
		}
	}
	return "", shellTierReadonly, true
}

func shellApprovalRequest(cfg config.ShellConfig, plan preparedShellCommand) (*approval.Request, error) {
	// A line running project code executes arbitrary code, so the blanket
	// "writes are fine" setting does not cover it: it is always asked for. The
	// session-level approval bypass still applies, as it does everywhere else.
	auto := map[shellTier]bool{shellTierReadonly: true, shellTierWrite: cfg.AllowWriteCommands, shellTierNetwork: cfg.AllowNetworkCommands}
	if _, known := auto[plan.tier]; !known {
		return nil, fmt.Errorf("command %q classified into unknown tier %q", plan.command, plan.tier)
	}
	if !plan.sandboxExec && auto[plan.tier] {
		return nil, nil
	}
	trigger := strings.Join(plan.reasons, "; ")
	trigger += " (" + sandboxConfinement(true, plan.networkAllowed()) + ")"
	// The model's stated intent is what the user checks the command against;
	// "Run <line>" only repeats the subject, so it is the fallback.
	summary := strings.TrimSpace(plan.args.Intent)
	summary = cmp.Or(summary, "Run "+plan.line)
	return &approval.Request{Tool: constants.ShellToolName, Tier: string(plan.tier), Summary: summary, Subject: plan.line, Reason: fmt.Sprintf("shell classified this as %s: %s", plan.tier, trigger), Details: fmt.Sprintf("Command: %s\nWorking directory: %s\nTier: %s\nTrigger: %s\nTimeout: %d ms\nMax output: %d bytes", plan.line, plan.cwd, plan.tier, trigger, plan.timeoutMS, plan.maxOutputBytes)}, nil
}

func argsAfterSubcommand(args []string, subcommand string) []string {
	if i := slices.Index(args, subcommand); i >= 0 {
		return args[i+1:]
	}
	return nil
}

func resolveShellCWD(input string, workspace workspace.Policy) (string, error) {
	input = stripSurroundingQuotes(strings.TrimSpace(input))
	if input == "" {
		if workspace.Root != "" {
			input = workspace.Root
		} else {
			var err error
			input, err = os.Getwd()
			if err != nil {
				return "", fmt.Errorf("resolve shell cwd: %w", err)
			}
		}
	}
	resolved, err := workspace.ResolveDir(input, input)
	if err != nil {
		return "", cwdError(err, input, workspace.Root)
	}
	return resolved, nil
}

// cwdError adds the hint the raw stat failure cannot give, and keeps the
// host's absolute path out of the model's view. The recurring miscall is
// passing a cwd the working directory already ends with ({"cwd":"ws"} from a
// workspace rooted at …/ws).
func cwdError(err error, input, root string) error {
	// A cwd outside the workspace is answered by naming the scratch directory
	// that *is* writable and how to reach it. The bare "use a path relative to
	// it" refusal cost three calls to a model that wanted a throwaway module in
	// /tmp: /tmp is writable from inside the line, but only the workspace is a
	// legal cwd, and nothing said so (r9 U1/4).
	if _, inside := fsutil.RelInside(root, filepath.Clean(input)); root != "" && filepath.IsAbs(input) && !inside {
		return fmt.Errorf("cwd %q is outside the workspace (workspace root is %q), and cwd must name a directory inside it. /tmp is a writable scratch directory (shared with code_interpreter, kept between calls): reach it from inside the command line instead, leaving cwd in the workspace — `mkdir -p /tmp/scratch && cd /tmp/scratch && ...`", input, root)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		return fmt.Errorf("cwd %q does not exist", input)
	}
	dirs := slices.DeleteFunc(entries, func(entry os.DirEntry) bool { return !entry.IsDir() })
	names := core.MapSlice(dirs, os.DirEntry.Name)
	return fmt.Errorf("cwd %q does not exist; \".\" contains: %s", input, strings.Join(names, ", "))
}

// stripSurroundingQuotes trims a single pair of surrounding double quotes
// (and any resulting surrounding whitespace), guarding against a model
// passing cwd as a JSON string that still carries literal quotes.
func stripSurroundingQuotes(input string) string {
	if len(input) >= 2 && strings.HasPrefix(input, `"`) && strings.HasSuffix(input, `"`) {
		input = strings.TrimSpace(input[1 : len(input)-1])
	}
	return input
}

func dispatchShellAudit(dispatcher events.Dispatcher, ctx context.Context, plan preparedShellCommand, outcome string, err error) {
	payload := &events.AuditPayload{Action: "shell:" + string(plan.tier), Tool: "shell", Outcome: outcome, Paths: []string{filepath.ToSlash(strutil.FirstNonEmpty(plan.args.Cwd, plan.cwd))}}
	// Recorded so post-hoc review can diff the stated intent against what ran.
	if intent := strings.TrimSpace(plan.args.Intent); intent != "" {
		payload.Details = map[string]any{"intent": intent}
	}
	core.DispatchAudit(ctx, dispatcher, payload, err)
}

// benignExit reports whether a non-zero exit is an answer, not a failure:
// grep and rg exit 1 for "no match", find exits 1 after printing what it
// could. Flagging those as tool failures taught the model a working search
// was broken — and the ledger counted a non-failure as one.
func benignExit(command string, code int) bool {
	return code == 1 && (command == "grep" || command == "rg" || command == "find")
}
