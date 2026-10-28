// Package git provides native git tools: git_read (read-only inspection with
// large-diff drill-down) and git_write (staging/commit/branch/stash and push
// of the current branch, always approval-gated). Everything else — pull,
// rebase, reset — stays on the shell tool.
package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"time"

	"github.com/invokr-agent/invokr/tools/core"
	"github.com/invokr-agent/invokr/tools/system/shell"
	"github.com/invokr-agent/invokr/tools/system/workspace"
)

const (
	readToolName  = "git_read"
	writeToolName = "git_write"

	// gitTimeout bounds any single git invocation.
	gitTimeout = 30 * time.Second
	// gitOutputLimit caps captured bytes; TruncatingWriters keeps memory
	// bounded, and the tool-result gate spills anything the model can't hold.
	gitOutputLimit = 2 << 20
)

func RegisterGitTools(c core.NativeRegistrar, deps core.CallerDeps, wsp workspace.Policy) {
	root := wsp.Root
	if root == "" {
		root, _ = os.Getwd()
	}
	registerReadTool(c, root)
	registerWriteTool(c, deps, root)
}

// gitText runs one git command in root and returns trimmed stdout, converting
// a non-zero exit into an error carrying stderr. A raw non-ExitError (git
// missing, not a repo dir) is wrapped as-is. Arguments are argv-literal — no
// shell, and never `-c` (config keys can execute arbitrary binaries, #318).
func gitText(ctx context.Context, root string, args ...string) (string, error) {
	return gitTextSpec(ctx, shell.GitProcessSpec, root, nil, args...)
}

func gitProbe(ctx context.Context, root string, args ...string) (string, error) {
	return gitTextSpec(ctx, shell.GitReadOnlyProcessSpec, root, nil, args...)
}

// gitTextEnv is gitText with an explicit environment. A nil env inherits the
// host's, as exec.Cmd does; a non-nil one replaces it wholesale, so callers
// append to os.Environ() rather than building one from scratch.
func gitTextEnv(ctx context.Context, root string, env []string, args ...string) (string, error) {
	return gitTextSpec(ctx, shell.GitProcessSpec, root, env, args...)
}

func gitTextSpec(ctx context.Context, specFor func(context.Context, string, []string, []string) (core.ProcessSpec, error), root string, env []string, args ...string) (string, error) {
	spec, err := specFor(ctx, root, append([]string{"--no-pager"}, args...), env)
	if err != nil {
		return "", err
	}
	spec.Timeout, spec.OutputLimit = gitTimeout, gitOutputLimit
	res, err := core.RunProcess(ctx, spec)
	if err != nil {
		if _, ok := errors.AsType[*exec.ExitError](err); !ok {
			return "", fmt.Errorf("run git %s: %w", firstArg(args), err)
		}
	}
	if res.ExitCode != 0 {
		return "", fmt.Errorf("git %s exited %d: %s", firstArg(args), res.ExitCode, strings.TrimSpace(res.Stderr))
	}
	return strings.TrimSpace(res.Stdout), nil
}

func firstArg(args []string) string {
	if i := slices.IndexFunc(args, func(a string) bool { return !strings.HasPrefix(a, "-") }); i >= 0 {
		return args[i]
	}
	return "git"
}

// flagged wraps a git failure so the model still sees the message: non-empty
// Payload means "completed but flagged" (see core.ToolError contract).
func flagged(tool string, err error) (string, error) {
	out, marshalErr := core.MarshalResult(tool, map[string]string{"error": err.Error()})
	if marshalErr != nil {
		return "", marshalErr
	}
	return out, &core.ToolError{Code: "git error", Payload: out, Err: err}
}
