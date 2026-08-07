package core

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"time"
)

// ProcessSpec describes a subprocess to run to completion with its output
// captured. It owns the plumbing the tools/system call sites used to repeat:
// the timeout context, the exec.Cmd construction, and the Dir/Env wiring.
// Env follows exec.Cmd: nil inherits the host environment, non-nil (even
// empty) replaces it. Zero Timeout means bounded only by ctx.
type ProcessSpec struct {
	Name, Dir   string
	Args, Env   []string
	Timeout     time.Duration
	OutputLimit int
	// Build, when non-nil, constructs the command instead of the plain
	// exec.CommandContext(Name, Args...) — the seam the sandboxed call
	// sites (tools/core/sandbox) hook into. Dir/Env are still applied to
	// the returned command, and the returned cleanup runs after the process
	// exits.
	Build func(context.Context) (*exec.Cmd, func() error, error)
}

// RunProcess runs spec to completion, delegating to RunCollect for capture and
// exit classification. The returned error is the raw cmd.Run() error —
// callers keep their own ToolError and audit shaping.
func RunProcess(ctx context.Context, spec ProcessSpec) (res RunResult, err error) {
	if spec.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, spec.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, spec.Name, spec.Args...)
	if spec.Build != nil {
		built, cleanup, buildErr := spec.Build(ctx)
		if buildErr != nil {
			return RunResult{}, buildErr
		}
		if cleanup != nil {
			defer func() {
				if checkErr := cleanup(); checkErr != nil {
					res.ExitCode = -1
					res.Stderr += "\n" + checkErr.Error()
					err = errors.Join(err, checkErr)
				}
			}()
		}
		cmd = built
	}
	cmd.Dir = spec.Dir
	cmd.Env = spec.Env
	ConfigureProcessGroup(cmd)
	return RunCollect(ctx, cmd, spec.OutputLimit)
}

// RunResult holds the captured output and exit classification of a subprocess
// run via RunCollect. Stdout/Stderr are raw (never trimmed) — callers keep
// doing their own trimming.
type RunResult struct {
	Stdout, Stderr      string
	ExitCode            int
	TimedOut, Truncated bool
}

// RunCollect runs cmd with stdout/stderr captured up to limit bytes via a
// shared TruncatingWriters budget, then classifies the outcome. The returned
// error is the raw cmd.Run() error, unchanged (nil on success). ExitCode is
// taken from an *exec.ExitError, or set to -1 when the run timed out with a
// non-exit error; TimedOut reflects ctx's deadline and Truncated reflects
// whether output was dropped. Callers keep their own error handling and
// ToolError/audit shaping, using RunResult for classification.
//
// Any stderrTee additionally receives stderr as it arrives — the seam a caller
// uses to consume a subprocess's incremental progress lines while the
// captured/truncated stderr still reaches the result unchanged.
func RunCollect(ctx context.Context, cmd *exec.Cmd, limit int, stderrTee ...io.Writer) (RunResult, error) {
	w := NewTruncatingWriters(limit)
	// The truncating writer stays first, so a tee never changes what the
	// captured (and possibly truncated) stderr in the result contains.
	cmd.Stdout, cmd.Stderr = w.Stdout(), io.MultiWriter(append([]io.Writer{w.Stderr()}, stderrTee...)...)

	err := cmd.Run()
	res := RunResult{Stdout: w.StdoutString(), Stderr: w.StderrString(), TimedOut: errors.Is(ctx.Err(), context.DeadlineExceeded), Truncated: w.Truncated()}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		res.ExitCode = exitErr.ExitCode()
	} else if err != nil && res.TimedOut {
		res.ExitCode = -1
	}
	return res, err
}
