package git

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/support/fsutil"
	"github.com/invokr-agent/invokr/tools/core"
)

// pushPlan is a push resolved before approval: the prompt shows exactly what
// the argv will send. Only the current branch is ever pushed, to its own name.
type pushPlan struct {
	argv                         []string
	remote, url, branch, commits string
	force                        bool
	hookNote                     string
}

// preparePush resolves remote, branch and upstream with read-only git. A
// remote must be a configured name, never a URL, so a push only reaches hosts
// the user already set up.
func preparePush(ctx context.Context, root string, args writeArgs) (*pushPlan, error) {
	branch, err := gitProbe(ctx, root, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return nil, err
	}
	if branch == "HEAD" {
		return nil, errors.New("push needs a checked-out branch (HEAD is detached)")
	}
	upstreamRemote, _ := gitProbe(ctx, root, "config", "--get", "branch."+branch+".remote")
	remote := strings.TrimSpace(args.Remote)
	if remote == "" {
		remote = cmp.Or(upstreamRemote, "origin")
	}
	remotes, _ := gitProbe(ctx, root, "remote")
	if !slices.Contains(strings.Fields(remotes), remote) {
		return nil, fmt.Errorf("push: %q is not a configured remote (have: %s)", remote, cmp.Or(strings.Join(strings.Fields(remotes), ", "), "none"))
	}
	p := &pushPlan{remote: remote, branch: branch, force: args.ForceWithLease}
	p.url, _ = gitProbe(ctx, root, "remote", "get-url", remote)
	tracked := remote + "/" + branch
	if p.commits, err = gitProbe(ctx, root, "log", "--oneline", "-20", tracked+"..HEAD"); err != nil {
		p.commits, _ = gitProbe(ctx, root, "log", "--oneline", "-20", "HEAD")
		p.commits = "(new branch on " + remote + ")\n" + p.commits
	}
	p.argv = []string{"push"}
	if p.force {
		p.argv = append(p.argv, "--force-with-lease")
	}
	if args.NoVerify {
		p.argv = append(p.argv, "--no-verify")
	} else {
		p.hookNote = hookNote(ctx, root)
	}
	if upstreamRemote != remote {
		p.argv = append(p.argv, "-u")
	}
	p.argv = append(p.argv, remote, "HEAD:refs/heads/"+branch)
	return p, nil
}

// hookNote warns when the pre-push hook lives somewhere the agent can edit:
// it runs on the host, outside every sandbox, once the push is approved.
func hookNote(ctx context.Context, root string) string {
	hooks, err := gitProbe(ctx, root, "config", "--get", "core.hooksPath")
	if err != nil || hooks == "" {
		return ""
	}
	if !filepath.IsAbs(hooks) {
		hooks = filepath.Join(root, hooks)
	}
	if _, inside := fsutil.RelInside(root, filepath.Clean(hooks)); !inside {
		return ""
	}
	return "\npre-push hook runs on the host from " + hooks + " (workspace-writable); deny and ask for no_verify if unsure."
}

// request is publishing over the network, never confined: plan refuses it,
// manual and auto ask, YOLO approves. A force push gets no grant pattern, so
// "always" cannot cover it.
func (p *pushPlan) request(root string) approval.Request {
	display := "git " + strings.Join(p.argv, " ")
	subject, pattern := "push "+p.remote+" "+p.branch, "push "+p.remote+" "+p.branch
	details := fmt.Sprintf("Command: %s\nRepository: %s\nRemote: %s (%s)\nCommits:\n%s", display, root, p.remote, p.url, p.commits)
	if p.force {
		subject, pattern = "push --force-with-lease "+p.remote+" "+p.branch, ""
		details = "FORCE PUSH: rewrites " + p.remote + "/" + p.branch + " (with lease).\n" + details
	}
	return approval.Request{
		Tool: writeToolName, Tier: "publish", Reason: "git_write push publishes commits with your git credentials",
		Writes: true, Network: true, Publishes: true,
		Summary: "Run " + display, Subject: subject, GrantPattern: pattern, Details: details + p.hookNote,
	}
}

// hostGit runs push as the user's own git: host environment, so ssh-agent,
// known_hosts and credential helpers work. The sandbox gives the shell none
// of these, which is the point of routing push through here.
func hostGit(ctx context.Context, root string, _ []string, args ...string) (string, error) {
	res, err := core.RunProcess(ctx, core.ProcessSpec{Name: "git", Args: append([]string{"--no-pager"}, args...), Dir: root, Timeout: gitTimeout, OutputLimit: gitOutputLimit})
	if err != nil {
		if _, ok := errors.AsType[*exec.ExitError](err); !ok {
			return "", fmt.Errorf("run git push: %w", err)
		}
	}
	out := strings.TrimSpace(res.Stdout + "\n" + res.Stderr) // git reports push progress on stderr
	if res.ExitCode != 0 {
		return "", fmt.Errorf("git push exited %d: %s", res.ExitCode, out)
	}
	return out, nil
}
