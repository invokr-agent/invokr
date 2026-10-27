package tmux

import (
	"context"
	"errors"
	"fmt"
	"github.com/invokr-ai/invokr/platform/turn"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/support/fsutil"
	"github.com/invokr-ai/invokr/support/mathutil"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/core/sandbox"
	"github.com/invokr-ai/invokr/tools/system/shell"
	"github.com/invokr-ai/invokr/tools/system/workspace"
)

const tmuxToolName = "tmux"

// holderSession is a hidden session created alongside the first visible one.
// A tmux server exits when its last session closes, so a model that kills the
// process in its only pane would take the server — and the sandbox holding it
// — down with it (#528). The holder keeps one session alive for the server's
// lifetime and is filtered out of list.
const holderSession = "_invokr_holder"

// socketName isolates this process's tmux server. The random suffix guards
// against PID reuse: with remain-on-exit servers can outlive invokr, and a
// plain invokr-<pid> socket would let a later process inherit a stale
// server's dead sessions.
var socketName = fmt.Sprintf("invokr-%d-%s", os.Getpid(), strings.ReplaceAll(uuid.NewString(), "-", "")[:4])

// hostServer points every tmux call at the user's own server ($TMUX or the
// default socket) instead of the private one: no sandbox, no holder, and the
// model can read and type into every session the user has (--use-host-tmux).
var hostServer atomic.Bool

// SocketPath is the tmux server socket. It lives in the workspace's shared tmp
// directory, which the sandbox bind-mounts at /tmp, so the sandboxed server and
// the host-side clients address the same file under different paths. Without a
// workspace there is nothing shared to mount, so the socket falls back to the
// host temp dir and the server stays on the host.
func SocketPath(workspaceRoot string) string {
	if dir := fsutil.SharedTmpDir(workspaceRoot); dir != "" && os.MkdirAll(dir, 0o700) == nil {
		return filepath.Join(dir, socketName+".sock")
	}
	return filepath.Join(os.TempDir(), socketName+".sock")
}

func createServerConf(dir string) string {
	f, err := os.CreateTemp(dir, socketName+"-*.conf")
	if err != nil {
		return ""
	}
	path := f.Name()
	if _, err = f.WriteString("set -g remain-on-exit on\n"); err == nil {
		err = f.Close()
	} else {
		_ = f.Close()
	}
	if err != nil {
		_ = os.Remove(path)
		return ""
	}
	return path
}

type tmuxArgs struct {
	Action  string   `json:"action" enum:"open,send,read,list,kill"`
	Session string   `json:"session,omitempty" desc:"Session name (required for send/read/kill)."`
	Command string   `json:"command,omitempty" desc:"Shell command to run in the new session (open only)."`
	Cwd     string   `json:"cwd,omitempty" desc:"Working directory for open, workspace-resolved."`
	Keys    []string `json:"keys,omitempty" desc:"To submit, send text with literal:true and keys:[text], then separately send literal:false and keys:[Enter]. ok only means tmux accepted keys; read pane to verify."`
	Literal bool     `json:"literal,omitempty" desc:"Send keys verbatim (-l); Enter then types text instead of submitting."`
	Lines   int      `json:"lines,omitempty" desc:"Scrollback lines above the visible screen to include when reading; default 200, max 10000."`
	WaitMS  int      `json:"wait_ms,omitempty" desc:"Wait up to 30000 ms before capturing to let the TUI render."`
	Width   int      `json:"width,omitempty" desc:"Pane width for open; defaults to 80, clamped to 20-500."`
	Height  int      `json:"height,omitempty" desc:"Pane height for open; defaults to 24, clamped to 5-200."`
}

type preparedTmux struct {
	mode      approval.Mode
	protected []string
	args      tmuxArgs
	cwd       string
	root      string // workspace root: keys the socket path and the sandbox mounts
}

func RegisterTmuxTool(c core.NativeRegistrar, deps core.CallerDeps, wsp workspace.Policy) {
	hostServer.Store(deps.App.Tools.TmuxHostServer)
	description := "Open and control tmux sessions for terminal applications. The session runs inside the namespace sandbox (workspace-only filesystem, minimal environment) with the host network, so a server it starts is reachable; where sandboxing is unavailable it runs on the host. Use this tool when a process must persist across calls; otherwise use the shell tool. Sessions persist after their command exits (list shows dead=1) — read the output, then kill the session when done. If a screen looks mid-render, read it again."
	if hostServer.Load() {
		description += " Host mode: this targets the user's own tmux server, unsandboxed; list shows their sessions and send/read accept targets like name:0.0. Never kill a session you did not open."
	}
	core.Register(c, core.Tool[preparedTmux]{
		Spec: core.ToolSpec{
			Name: tmuxToolName, Description: description,
			Example:    `{"name":"tmux","arguments":{"action":"open","command":"htop"}}`,
			Parameters: core.SchemaFor[tmuxArgs](),
			Metadata:   core.NativeMetadata("system", []string{constants.ShellTierReadonly, constants.ShellTierWrite}, constants.ApprovalPolicyAsk, core.ToolCapabilities{ReadsWorkspace: true, WritesWorkspace: true, ExecutesCode: true}),
		},
		Decode: func(call *core.ToolCall) (preparedTmux, error) { return prepareTmux(call, wsp) },
		Prepare: func(ctx context.Context, p preparedTmux) (preparedTmux, error) {
			p.mode, p.protected = approval.ModeFromContext(ctx), wsp.ProtectedPaths()
			return p, nil
		},
		Approve: func(_ context.Context, plan preparedTmux) core.ApprovalDecision {
			decision := tmuxApproval(plan)
			req := approval.Request{Tool: tmuxToolName, ReadOnly: plan.args.Action == "read" || plan.args.Action == "list"}
			if decision.Request != nil {
				req = *decision.Request
			}
			req.Subject = strings.Join([]string{plan.args.Action, plan.args.Session, plan.args.Command, strings.Join(plan.args.Keys, " ")}, " ")
			req.Writes = !req.ReadOnly
			serversMu.Lock()
			posture, known := serverModes[plan.root]
			serversMu.Unlock()
			_, socketErr := os.Stat(SocketPath(plan.root))
			req.Confined = !hostServer.Load() && sandbox.Available() && plan.root != "" && (known && posture == approval.Auto || !known && plan.args.Action == "open" && os.IsNotExist(socketErr))
			req.Network = !req.ReadOnly
			req.Publishes = shell.Publishes(plan.args.Command + " " + strings.Join(plan.args.Keys, " "))
			if decision.Request == nil {
				return core.Automatic(req)
			}
			return core.Ask(req)
		},
		Approver: deps.Approver,
		Execute: func(ctx context.Context, plan preparedTmux) (string, error) {
			out, err := runTmuxAction(ctx, plan)
			dispatchTmuxAudit(deps.Dispatcher, ctx, plan, err)
			return out, err
		},
	})
}

func tmuxApproval(plan preparedTmux) core.ApprovalDecision {
	switch plan.args.Action {
	case "open":
		return core.Ask(approval.Request{
			Tool: tmuxToolName, Tier: constants.ShellTierWrite, Reason: "opening a tmux session executes code in the namespace sandbox with host network, or on the host where sandboxing is unavailable",
			Summary: fmt.Sprintf("Open tmux session running: %s", plan.args.Command),
			Details: fmt.Sprintf("Command: %s\nWorking directory: %s", plan.args.Command, plan.cwd),
		})
	case "send":
		return core.Ask(approval.Request{
			Tool: tmuxToolName, Tier: constants.ShellTierWrite, Reason: "sending keys can execute commands in the session's sandbox, or on the host where sandboxing is unavailable",
			Summary: fmt.Sprintf("Send keys to %s: %s", plan.args.Session, strings.Join(plan.args.Keys, " ")),
			Details: fmt.Sprintf("Session: %s\nKeys: %s", plan.args.Session, strings.Join(plan.args.Keys, " ")),
		})
	case "kill":
		if hostServer.Load() {
			return core.Ask(approval.Request{Tool: tmuxToolName, Tier: constants.ShellTierWrite, Reason: "the host tmux server holds the user's own sessions",
				Summary: "Kill tmux session " + plan.args.Session})
		}
		return core.Auto()
	default:
		return core.Auto()
	}
}

func prepareTmux(call *core.ToolCall, wsp workspace.Policy) (preparedTmux, error) {
	args, err := core.DecodeOptionalToolArgs[tmuxArgs](call)
	if err != nil {
		return preparedTmux{}, fmt.Errorf("parse tmux arguments: %w", err)
	}
	args.Action = strings.TrimSpace(args.Action)
	if err := validateTmuxArgs(args); err != nil {
		return preparedTmux{}, err
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		return preparedTmux{}, fmt.Errorf("tmux is not installed: %w", err)
	}
	// Non-positive values fall back to the defaults before clamping.
	args.Width = min(max(mathutil.ClampInt(args.Width, 80, 500), 20), 500)
	args.Height = min(max(mathutil.ClampInt(args.Height, 24, 200), 5), 200)
	// Default read to include recent scrollback: when a pane dies (remain-on-exit)
	// tmux redraws the visible screen with the dead-pane banner and the command's
	// output survives only in history, so a visible-screen-only capture is blank.
	args.Lines = mathutil.ClampInt(args.Lines, 200, 10000)
	args.WaitMS = min(max(args.WaitMS, 0), 30000)
	cwd, err := resolveTmuxCWD(args, wsp)
	return preparedTmux{args: args, cwd: cwd, root: wsp.Root}, err
}

func validateTmuxArgs(args tmuxArgs) error {
	switch args.Action {
	case "open":
		if strings.TrimSpace(args.Command) == "" {
			return fmt.Errorf("command is required for open")
		}
	case "send", "read", "kill":
		if strings.TrimSpace(args.Session) == "" {
			return fmt.Errorf("session is required for %s", args.Action)
		}
		if args.Action == "send" && strings.Join(args.Keys, "") == "" {
			return fmt.Errorf("keys are required for send")
		}
	case "list":
	default:
		return fmt.Errorf("unsupported tmux action %q", args.Action)
	}
	return nil
}

func resolveTmuxCWD(args tmuxArgs, wsp workspace.Policy) (string, error) {
	if args.Action != "open" {
		return "", nil
	}
	cwd := args.Cwd
	if strings.TrimSpace(cwd) == "" {
		cwd = wsp.Root
	}
	return wsp.ResolveDir(cwd, args.Cwd)
}

func runTmuxAction(ctx context.Context, plan preparedTmux) (string, error) {
	switch plan.args.Action {
	case "open":
		return openSession(ctx, plan)
	case "list":
		return listSessions(ctx, plan.root)
	case "kill":
		return killSession(ctx, plan)
	case "send":
		return sendKeys(ctx, plan)
	case "read":
		return readPane(ctx, plan)
	default:
		return "", fmt.Errorf("tmux action %q is not implemented", plan.args.Action)
	}
}

func sendKeys(ctx context.Context, plan preparedTmux) (string, error) {
	tmuxArgs := []string{"send-keys"}
	if plan.args.Literal {
		tmuxArgs = append(tmuxArgs, "-l")
	}
	// "--" stops option parsing so keys starting with "-" (e.g. sending the
	// literal text "-l" to a REPL) reach the pane instead of tmux's getopt.
	tmuxArgs = append(tmuxArgs, "-t", plan.args.Session, "--")
	tmuxArgs = append(tmuxArgs, plan.args.Keys...)
	if _, err := runTmux(ctx, plan.root, tmuxArgs...); err != nil {
		return "", err
	}
	events.RecordExecution(ctx, turn.ExecutionEvidence{Persistent: true})
	return "ok", nil
}

func readPane(ctx context.Context, plan preparedTmux) (string, error) {
	if plan.args.WaitMS > 0 {
		timer := time.NewTimer(time.Duration(plan.args.WaitMS) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-timer.C:
		}
	}
	tmuxArgs := []string{"capture-pane", "-p", "-t", plan.args.Session}
	if plan.args.Lines > 0 {
		tmuxArgs = append(tmuxArgs, "-S", "-"+strconv.Itoa(plan.args.Lines))
	}
	return runTmux(ctx, plan.root, tmuxArgs...)
}

func openSession(ctx context.Context, plan preparedTmux) (string, error) {
	session := "m-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:6]
	// A live server already holds the namespaces every pane it spawns inherits,
	// so only the open that starts the server needs the sandbox. The server is
	// always started on the holder session, never on the model's command: the
	// visible session is created afterwards through an ordinary client, whose
	// argv dies with the call.
	if _, down := runTmux(ctx, plan.root, "list-sessions"); down != nil && !hostServer.Load() {
		if err := startServer(ctx, plan); err != nil {
			return "", err
		}
	}
	args := []string{"new-session", "-d", "-s", session, "-x", strconv.Itoa(plan.args.Width), "-y", strconv.Itoa(plan.args.Height), "-c", plan.cwd, plan.args.Command}
	if _, err := runTmux(ctx, plan.root, args...); err != nil {
		return "", err
	}
	events.RecordExecution(ctx, turn.ExecutionEvidence{Persistent: true})
	return core.MarshalResult("tmux", map[string]string{"session": session})
}

// startServer brings the server up on the holder session, sandboxed where the
// sandbox is available.
func startServer(ctx context.Context, plan preparedTmux) error {
	var err error
	if sandbox.Available() && plan.root != "" && plan.mode != approval.YOLO {
		err = openSandboxed(ctx, plan)
	} else {
		_, err = runTmux(ctx, plan.root, "new-session", "-d", "-s", holderSession)
	}
	if err == nil {
		serversMu.Lock()
		serverRoots = append(serverRoots, plan.root)
		serverModes[plan.root] = plan.mode
		serverReleases = append(serverReleases, approval.Retain(ctx))
		serversMu.Unlock()
	}
	return err
}

// serverRoots names the workspace roots this process started a server for; the
// holder session means a server now outlives every session the model opened, so
// something has to end it when invokr exits.
var (
	serversMu      sync.Mutex
	serverRoots    []string
	serverModes    = map[string]approval.Mode{}
	serverReleases []func()
)

// ShutdownServers kills the tmux servers this process started. Runtime cleanup
// calls it alongside the other daemon shutdowns.
func ShutdownServers() {
	serversMu.Lock()
	roots := serverRoots
	serverRoots = nil
	releases := serverReleases
	serverReleases, serverModes = nil, map[string]approval.Mode{}
	serversMu.Unlock()
	for _, release := range releases {
		release()
	}
	for _, root := range roots {
		_ = exec.Command("tmux", "-S", SocketPath(root), "kill-server").Run()
	}
}

func hideHolder(out string) string {
	kept := []string{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line != "" && !strings.HasPrefix(line, holderSession+" ") {
			kept = append(kept, line)
		}
	}
	if len(kept) == 0 {
		return "no sessions"
	}
	return strings.Join(kept, "\n")
}

func listSessions(ctx context.Context, root string) (string, error) {
	out, err := runTmux(ctx, root, "list-sessions", "-F", "#{session_name} #{session_created} #{pane_current_command} dead=#{pane_dead}")
	if toolErr, ok := errors.AsType[*core.ToolError](err); ok {
		payload := strings.ToLower(toolErr.Payload)
		if strings.Contains(payload, "no server running") || strings.Contains(payload, "error connecting") || strings.Contains(payload, "server exited unexpectedly") {
			return "no sessions", nil
		}
	}
	if err != nil {
		return out, err
	}
	return hideHolder(out), nil
}

func killSession(ctx context.Context, plan preparedTmux) (string, error) {
	if _, err := runTmux(ctx, plan.root, "kill-session", "-t", plan.args.Session); err != nil {
		return "", err
	}
	return "killed", nil
}

func runTmux(ctx context.Context, root string, args ...string) (string, error) {
	var base []string
	if !hostServer.Load() {
		base = []string{"-S", SocketPath(root)}
		if conf := createServerConf(""); conf != "" {
			base = append(base, "-f", conf)
			defer func() { _ = os.Remove(conf) }()
		}
	}
	res, err := core.RunProcess(ctx, core.ProcessSpec{
		Name: "tmux", Args: append(base, args...), OutputLimit: constants.ShellDefaultMaxOutputBytes,
		Timeout: time.Duration(constants.ShellDefaultTimeoutMS) * time.Millisecond,
	})
	if err == nil {
		return res.Stdout, nil
	}
	if _, ok := errors.AsType[*exec.ExitError](err); ok {
		return "", &core.ToolError{Code: fmt.Sprintf("exit %d", res.ExitCode), Payload: strings.TrimSpace(res.Stderr), Err: err}
	}
	return "", err
}

func dispatchTmuxAudit(dispatcher events.Dispatcher, ctx context.Context, plan preparedTmux, err error) {
	payload := &events.AuditPayload{Action: "tmux:" + plan.args.Action, Tool: tmuxToolName, Outcome: core.Outcome(err)}
	if plan.cwd != "" {
		payload.Paths = []string{filepath.ToSlash(plan.cwd)}
	}
	core.DispatchAudit(ctx, dispatcher, payload, err)
}
