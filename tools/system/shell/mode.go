package shell

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/core/sandbox"
	"github.com/invokr-ai/invokr/tools/system/workspace"
	"mvdan.cc/sh/v3/syntax"
)

func modeShellApproval(cfg config.ShellConfig, p preparedShellCommand) core.ApprovalDecision {
	req, err := shellApprovalRequest(cfg, p)
	if err != nil {
		return core.Deny(err.Error())
	}
	ask := req != nil
	if req == nil {
		req = &approval.Request{Tool: "shell", Tier: string(p.tier), Subject: p.line, Summary: "Run " + p.line}
	}
	req.Confined, req.Writes = sandbox.Available() && p.mode != approval.YOLO, p.mode != approval.Plan
	if req.Confined && p.publishes && gitPushes(p.line) {
		// The sandbox mounts no ~/.ssh or ssh-agent, so this would fail late
		// with "Host key verification failed" and read like a broken host.
		return core.Deny("git push cannot authenticate inside the shell sandbox; use git_write with op=push, which pushes the current branch with the user's git credentials after approval")
	}
	if req.Confined && callsGH(p.line) {
		// The sandbox mounts no ~/.config/gh, so gh reports "not logged in".
		return core.Deny(GHSandboxDenial)
	}
	req.Network, req.Publishes = p.networkAllowed(), p.publishes
	req.RequiresScopedApproval = p.sandboxExec
	if !req.Confined {
		req.Details += "\nUNSANDBOXED: this command runs on the host."
	}
	if ask {
		return core.Ask(*req)
	}
	return core.Automatic(*req)
}

// Publishing is a command-shape check, not a guarantee about opaque network code.
func Publishes(line string) bool {
	file, err := syntax.NewParser().Parse(strings.NewReader(line), "")
	if err != nil {
		return false
	}
	result := false
	syntax.Walk(file, func(n syntax.Node) bool {
		if call, ok := n.(*syntax.CallExpr); ok {
			result = result || publishesCall(call)
		}
		return true
	})
	return result
}

// GHToolboxTool is the toolbox tool that does gh's GitHub API work outside the sandbox.
const GHToolboxTool = "github_pr"

// GHSandboxDenial is the shell denial for gh; the TUI keys its install suggestion off it.
const GHSandboxDenial = "gh cannot authenticate inside the shell sandbox; to open a pull request use the " + GHToolboxTool + " toolbox tool (install it from /tools → Install if it is not available)"

var ghWrappers = []string{"env", "timeout", "xargs", "command", "nice", "nohup"}

// callsGH reports gh as a command, including the command a wrapper runs.
func callsGH(line string) bool {
	file, err := syntax.NewParser().Parse(strings.NewReader(line), "")
	if err != nil {
		return false
	}
	found := false
	syntax.Walk(file, func(n syntax.Node) bool {
		if call, ok := n.(*syntax.CallExpr); ok {
			words := callWords(call)
			// gh as the command, or as the command a wrapper runs; `echo gh` is not one.
			// Words keep their path so a wrapper's VAR=val survives; compare by basename.
			found = found || len(words) > 0 && (isGH(words[0]) || slices.Contains(ghWrappers, filepath.Base(words[0])) && wrappedGH(words))
		}
		return true
	})
	return found
}

// wrappedGH is true when a wrapper runs gh as its command: the first word left
// is the command, so a wrapper's flags, a VAR=val assignment and a numeric
// argument (timeout's duration, nice's offset) are stepped over. A flag that
// names its argument in the next word (env -u NAME) leaves that name looking
// like the command, and the line reads as that command rather than gh.
func wrappedGH(words []string) bool {
	for i := 1; i < len(words); i++ {
		w, digit := words[i], words[i] != "" && '0' <= words[i][0] && words[i][0] <= '9'
		if strings.HasPrefix(w, "-") || strings.Contains(w, "=") || digit {
			continue
		}
		return isGH(w)
	}
	return false
}

func isGH(word string) bool { return filepath.Base(word) == "gh" }

// callWords is the literal words of a call, paths left alone.
func callWords(call *syntax.CallExpr) []string {
	return core.MapSlice(call.Args, func(arg *syntax.Word) string { word, _ := literalWord(arg); return word })
}

// gitPushes reports a `git … push` call: the first git subcommand, after
// global flags, is push (so `git stash push` is not one).
func gitPushes(line string) bool {
	file, err := syntax.NewParser().Parse(strings.NewReader(line), "")
	if err != nil {
		return false
	}
	found := false
	syntax.Walk(file, func(n syntax.Node) bool {
		if call, ok := n.(*syntax.CallExpr); ok {
			words := callWords(call)
			// Any git word, so env/timeout/xargs wrappers are caught too.
			i := slices.IndexFunc(words, func(w string) bool { return filepath.Base(w) == "git" })
			found = found || i >= 0 && gitSubcommand(words[i+1:]) == "push"
		}
		return true
	})
	return found
}

func gitSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "-C" || args[i] == "-c":
			i++
		case !strings.HasPrefix(args[i], "-"):
			return args[i]
		}
	}
	return ""
}

func publishesCall(call *syntax.CallExpr) bool {
	return publishesWords(callWords(call))
}

func publishesWords(words []string) bool {
	if len(words) < 2 {
		return false
	}
	root := filepath.Base(words[0])
	if publishesWrapped(root, words) {
		return true
	}
	actions := map[string][]string{"git": {"push"}, "npm": {"publish"}, "pnpm": {"publish"}, "yarn": {"publish"}, "docker": {"push"}, "podman": {"push"}, "cargo": {"publish"}, "twine": {"upload"}, "gem": {"push"}, "mvn": {"deploy"}, "gradle": {"publish"}, "gradlew": {"publish"}}
	if slices.ContainsFunc(words[1:], func(w string) bool { return slices.Contains(actions[root], w) }) {
		return true
	}
	return root == "gh" && slices.ContainsFunc(words[1:], func(w string) bool {
		return slices.Contains([]string{"create", "edit", "merge", "close", "reopen", "comment", "review", "upload", "delete", "POST", "PUT", "PATCH", "DELETE", "-f", "-F", "--field", "--raw-field"}, w) || strings.HasPrefix(w, "--method=") && w != "--method=GET"
	})
}

func publishesWrapped(root string, words []string) bool {
	if slices.Contains([]string{"sh", "bash", "zsh"}, root) {
		for i, word := range words[1 : len(words)-1] {
			if strings.HasPrefix(word, "-") && strings.Contains(word, "c") && Publishes(words[i+2]) {
				return true
			}
		}
	}
	if slices.Contains([]string{"env", "timeout", "xargs", "command", "sudo", "nice", "nohup", "stdbuf", "time"}, root) {
		for i := 1; i < len(words); i++ {
			if slices.Contains([]string{"git", "gh", "npm", "pnpm", "yarn", "docker", "podman", "cargo", "twine", "gem", "mvn", "gradle", "gradlew", "sh", "bash", "zsh"}, filepath.Base(words[i])) {
				return publishesWords(words[i:])
			}
		}
	}
	return false
}

// GitProcessSpec shares the shell's confinement for native git, including hooks.
func GitProcessSpec(ctx context.Context, root string, argv, env []string) (core.ProcessSpec, error) {
	return gitProcessSpec(ctx, root, argv, env, false)
}

// GitReadOnlyProcessSpec is GitProcessSpec with the repository mounted read-only
// and sealed, as in Plan mode. With nothing writable there is no unsealed
// protected name to approve, so read-only probes (e.g. before a push) never prompt.
func GitReadOnlyProcessSpec(ctx context.Context, root string, argv, env []string) (core.ProcessSpec, error) {
	return gitProcessSpec(ctx, root, argv, env, true)
}

func gitProcessSpec(ctx context.Context, root string, argv, env []string, gitReadOnly bool) (core.ProcessSpec, error) {
	mode := approval.ModeFromContext(ctx)
	if mode == approval.Manual || mode == approval.YOLO {
		return core.ProcessSpec{Name: "git", Args: argv, Dir: root, Env: env}, nil
	}
	if mode == approval.Plan && !sandbox.Available() {
		return core.ProcessSpec{}, fmt.Errorf("plan verification requires namespace sandboxing")
	}
	if !sandbox.Available() && approval.CallRequest(ctx).Tool == "git_read" {
		_, err := approval.Authorize(ctx, nil, approval.Request{Tool: "git_read", Subject: strings.Join(argv, " ")}, true)
		if err != nil {
			return core.ProcessSpec{}, err
		}
	}
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root}).Protect(approval.RulesPath(ctx))
	p := preparedShellCommand{mode: mode, line: "git " + strings.Join(core.MapSlice(argv, quoteArg), " "), cwd: root, workspaceRoot: root,
		roots: []string{root}, commands: []string{"git"}, protected: wsp.ProtectedPaths(), allowNetwork: mode == approval.Auto, isolateEnv: true,
		gitReadOnly: gitReadOnly}
	spec, err := processSpec(p)
	// Only fallback Git identity values are forwarded; never inherit host secrets.
	for _, kv := range env {
		if strings.HasPrefix(kv, "GIT_AUTHOR_") || strings.HasPrefix(kv, "GIT_COMMITTER_") {
			spec.Env = append(spec.Env, kv)
		}
	}
	return spec, err
}
