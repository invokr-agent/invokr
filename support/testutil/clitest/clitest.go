package clitest

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	defaultRunTimeout   = 30 * time.Second
	defaultBuildTimeout = 2 * time.Minute
)

var buildState struct {
	once sync.Once
	path string
	err  error
	out  string
}

type State struct {
	HomeDir    string
	ToolboxDir string
	MemoryDir  string
	TokfHome   string
	WorkDir    string
}

type Options struct {
	ProviderURL string
	State       State
	Input       string
	Args        []string
	CommandArgs []string
	Env         map[string]string
	Timeout     time.Duration
}

type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
	TimedOut bool
	Err      error
}

type Process struct {
	cmd     *exec.Cmd
	stdout  bytes.Buffer
	stderr  bytes.Buffer
	ctx     context.Context
	cancel  context.CancelFunc
	waitErr error
	waited  bool
}

func NewState(t testing.TB) State {
	t.Helper()

	root := t.TempDir()
	state := State{
		HomeDir:    filepath.Join(root, "home"),
		ToolboxDir: filepath.Join(root, "toolbox"),
		MemoryDir:  filepath.Join(root, "memory", "markdown"),
		TokfHome:   filepath.Join(root, "tokf"),
		WorkDir:    filepath.Join(root, "work"),
	}
	for _, dir := range []string{state.HomeDir, state.ToolboxDir, state.MemoryDir, state.TokfHome, state.WorkDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create cli test dir %s: %v", dir, err)
		}
	}
	return state
}

func Run(t testing.TB, opts Options) Result {
	t.Helper()
	return Start(t, opts).Wait()
}

func Start(t testing.TB, opts Options) *Process {
	t.Helper()

	timeout := opts.Timeout
	if timeout == 0 {
		timeout = defaultRunTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)

	args := append([]string(nil), opts.CommandArgs...)
	if len(args) == 0 {
		args = []string{"--base-url=" + opts.ProviderURL}
		args = append(args, opts.Args...)
		args = append(args, "agent")
	}
	cmd := exec.CommandContext(ctx, Binary(t), args...)
	configureCommand(cmd)
	cmd.Dir = opts.State.WorkDir
	cmd.Env = mergedEnv(isolatedEnv(opts.State), map[string]string{
		"INVOKR_HOME":          opts.State.HomeDir,
		"INVOKR_TOOLBOX_DIR":   opts.State.ToolboxDir,
		"INVOKR_MEMORY_DIR":    opts.State.MemoryDir,
		"INVOKR_TOKF_DISABLED": "true",
		"INVOKR_TOKF_HOME":     opts.State.TokfHome,
		"NO_COLOR":             "1",
		"TERM":                 "dumb",
	})
	cmd.Env = mergedEnv(cmd.Env, opts.Env)
	cmd.Stdin = strings.NewReader(opts.Input)

	process := &Process{cmd: cmd, ctx: ctx, cancel: cancel}
	cmd.Stdout = &process.stdout
	cmd.Stderr = &process.stderr
	if err := cmd.Start(); err != nil {
		cancel()
		process.waitErr = fmt.Errorf("start invokr binary: %w", err)
		process.waited = true
	}
	return process
}

func (p *Process) PID() int {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return 0
	}
	return p.cmd.Process.Pid
}

func (p *Process) Kill() error {
	if p == nil || p.cmd == nil || p.cmd.Process == nil {
		return nil
	}
	if p.cancel != nil {
		p.cancel()
	}
	return p.cmd.Process.Kill()
}

func (p *Process) Wait() Result {
	if p == nil {
		return Result{ExitCode: -1, Err: errors.New("nil process")}
	}
	if !p.waited {
		p.waitErr = p.cmd.Wait()
		p.waited = true
		p.cancel()
	}
	result := Result{Stdout: p.stdout.String(), Stderr: p.stderr.String(), ExitCode: 0, Err: p.waitErr}
	if p.ctx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		result.ExitCode = -1
		return result
	}
	var exitErr *exec.ExitError
	if p.waitErr != nil && errors.As(p.waitErr, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	} else if p.waitErr != nil {
		result.ExitCode = -1
	}
	return result
}

func isolatedEnv(state State) []string {
	values := map[string]string{
		"HOME":   state.HomeDir,
		"TMPDIR": filepath.Join(state.HomeDir, "tmp"),
	}
	for _, key := range []string{"PATH", "GOROOT", "GOCACHE", "LANG", "LC_ALL"} {
		if value, ok := os.LookupEnv(key); ok {
			values[key] = value
		}
	}
	_ = os.MkdirAll(values["TMPDIR"], 0o755)
	out := make([]string, 0, len(values))
	for key, value := range values {
		out = append(out, key+"="+value)
	}
	return out
}

func Binary(t testing.TB) string {
	t.Helper()

	buildState.once.Do(func() {
		dir, err := os.MkdirTemp("", "invokr-e2e-bin-*")
		if err != nil {
			buildState.err = err
			return
		}
		buildState.path = filepath.Join(dir, "github.com/invokr-ai/invokr")

		ctx, cancel := context.WithTimeout(context.Background(), defaultBuildTimeout)
		defer cancel()

		// -buildvcs=false: the test binary needs no VCS stamp, and stamping fails
		// outright when git rejects the checkout (e.g. dubious ownership in CI).
		cmd := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", buildState.path, ".")
		cmd.Dir = RepoRoot()
		cmd.Env = os.Environ()

		var output bytes.Buffer
		cmd.Stdout = &output
		cmd.Stderr = &output
		if err := cmd.Run(); err != nil {
			buildState.err = err
			buildState.out = output.String()
			return
		}
		if ctx.Err() == context.DeadlineExceeded {
			buildState.err = ctx.Err()
			buildState.out = output.String()
		}
	})

	if buildState.err != nil {
		t.Fatalf("build invokr binary: %v\noutput:\n%s", buildState.err, buildState.out)
	}
	return buildState.path
}

func RepoRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(wd, "go.mod")); err == nil {
			return wd
		}
		next := filepath.Dir(wd)
		if next == wd {
			return "."
		}
		wd = next
	}
}

func mergedEnv(base []string, overrides map[string]string) []string {
	if len(overrides) == 0 {
		return append([]string(nil), base...)
	}
	values := map[string]string{}
	order := []string{}
	for _, entry := range base {
		key, _, ok := strings.Cut(entry, "=")
		if !ok {
			continue
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = entry
	}
	for key, value := range overrides {
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = key + "=" + value
	}
	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, values[key])
	}
	return out
}
