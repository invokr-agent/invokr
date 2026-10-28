package shell

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/support/testutil"
	"github.com/invokr-agent/invokr/tools/core"
	"github.com/invokr-agent/invokr/tools/core/sandbox"
	"github.com/invokr-agent/invokr/tools/system/workspace"
)

func TestPublishingCommandShapes(t *testing.T) {
	for _, line := range []string{"git push", "git -C . push origin main", "bash -lc 'git push'", "env FOO=bar git push", "timeout 60 git push", "xargs git push", "nice -n 10 git push", "nohup git push", "stdbuf -oL git push", "/usr/bin/time git push", "pnpm publish", "podman push image", "gh api repos/x -X POST", "gh release upload v1 artifact", "echo hello; npm publish"} {
		if !Publishes(line) {
			t.Errorf("publishing missed: %s", line)
		}
	}
	for _, line := range []string{"git status", "git add .", "git commit -m update", "gh pr view 1", "go test ./...", "echo 'git push'"} {
		if Publishes(line) {
			t.Errorf("not publishing: %s", line)
		}
	}
}

func TestAutoBashCommandCannotBypassDeclinedApproval(t *testing.T) {
	t.Setenv(sandbox.AllowUnsandboxedEnv, "1")
	root := t.TempDir()
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root})
	for _, allowed := range [][]string{{"touch"}, {"touch", "bash"}} {
		cfg := config.ShellConfig{AllowedCommands: allowed}
		call := &core.ToolCall{Arguments: []byte(`{"command":"bash -c 'touch denied'"}`)}
		plan, err := prepareShellCommand(call, cfg, wsp, core.StringSet(allowed), allowed)
		if err != nil {
			t.Fatal(err)
		}
		plan.mode = approval.Auto
		if !plan.sandboxExec {
			t.Fatalf("bash -c must be opaque with allowlist %v", allowed)
		}
		ctx, done := approval.NewPolicy(approval.Auto, false).Begin(context.Background(), approval.Request{Tool: "shell", Subject: plan.line})
		approver := testutil.StaticApprover{Decision: approval.Decision{Reason: "declined by user"}}
		decision := modeShellApproval(cfg, plan)
		if decision.Request == nil {
			t.Fatal("opaque shell command must request approval")
		}
		decision.Request.Confined = true
		if err := core.EnforceApproval(ctx, approver, core.ToolSpec{Name: "shell"}, decision); err == nil || !strings.Contains(err.Error(), "declined by user") {
			t.Fatalf("declined bash -c approval = %v", err)
		}
		done()
	}
}

func TestAutoForegroundRunsWithoutMissingSealPromptAndBackgroundStillAsks(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("namespace sandbox unavailable")
	}
	t.Setenv("INVOKR_HOME", t.TempDir())
	root := t.TempDir()
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root})
	p := preparedShellCommand{mode: approval.Auto, workspaceRoot: root, cwd: root, roots: []string{root}, protected: wsp.ProtectedPaths(), command: "bash", line: "touch written"}
	ctx, release := approval.NewPolicy(approval.Auto, false).Begin(context.Background(), approval.Request{Tool: "shell", Subject: p.line}, testutil.StaticApprover{Decision: approval.Decision{Reason: "unexpected prompt"}})
	defer release()
	if _, err := runLine(nil, ctx, p); err != nil {
		t.Fatal(err)
	}
	p.line = "mkdir .invokr"
	if _, err := runLine(nil, ctx, p); err == nil || !strings.Contains(err.Error(), "removed newly created protected path") {
		t.Fatalf("foreground error = %v", err)
	}
	p.args.Background = true
	if _, err := startBackgroundShellCommand(nil, ctx, p); err == nil || !strings.Contains(err.Error(), "unexpected prompt") {
		t.Fatalf("background error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, ".invokr")); !os.IsNotExist(err) {
		t.Fatal("protected name remains")
	}
}

func TestManualNetworkArgumentCannotWidenConfiguration(t *testing.T) {
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: t.TempDir()})
	p, err := prepareShellCommand(&core.ToolCall{Arguments: []byte(`{"command":"pwd","network":true}`)}, config.ShellConfig{}, wsp, core.StringSet([]string{"pwd"}), []string{"pwd"})
	if err != nil {
		t.Fatal(err)
	}
	if p.networkAllowed() {
		t.Fatal("model enabled network in Manual")
	}
}

func TestPlanCacheAliasCannotWriteSources(t *testing.T) {
	root, cache := t.TempDir(), filepath.Join(t.TempDir(), "cache")
	if err := os.Symlink(root, cache); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOCACHE", cache)
	p := sandboxPlan("/bin/bash", preparedShellCommand{mode: approval.Plan, workspaceRoot: root, cwd: root, roots: []string{root}, commands: []string{"go"}})
	if slices.Contains(p.RW, cache) || slices.Contains(p.RW, root) {
		t.Fatalf("source writable through alias: %v", p.RW)
	}
}

func TestPlanRefusesScratchInsideSourcesBeforeCreatingIt(t *testing.T) {
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	_, err := processSpec(preparedShellCommand{mode: approval.Plan, workspaceRoot: root, cwd: root, roots: []string{root}, line: "true"})
	if err == nil {
		t.Fatal("Plan accepted writable scratch inside sources")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("Plan prepared scratch in source root: %v", entries)
	}
}

func TestPlanExecProtectsSourceAndAllowsScratch(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("namespace sandbox unavailable")
	}
	root := t.TempDir()
	p := preparedShellCommand{mode: approval.Plan, workspaceRoot: root, cwd: root, roots: []string{root}, commands: []string{"touch"}, line: "touch denied 2>/dev/null && exit 9; touch /tmp/allowed", isolateEnv: true}
	spec, err := processSpec(p)
	if err != nil {
		t.Fatal(err)
	}
	command, cleanup, err := spec.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	}()
	command.Env = spec.Env
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, "denied")); !os.IsNotExist(err) {
		t.Fatal("Plan wrote source")
	}
}

func TestAutoSandboxDoesNotCreateControlDirectories(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("namespace sandbox unavailable")
	}
	root := t.TempDir()
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root})
	p := preparedShellCommand{mode: approval.Auto, workspaceRoot: root, cwd: root, roots: []string{root}, protected: wsp.ProtectedPaths(), line: "touch written"}
	spec, err := processSpec(p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, release := approval.NewPolicy(approval.Auto, false).Begin(context.Background(), approval.Request{Tool: "shell", Subject: p.line}, testutil.StaticApprover{Decision: approval.Decision{Approved: true}})
	defer release()
	command, cleanup, err := spec.Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	}()
	command.Env = spec.Env
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "written" {
		t.Fatalf("Auto polluted workspace: %v", entries)
	}
}

func TestAutoGitCanCommitWithProtectedControlPaths(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("namespace sandbox unavailable")
	}
	root := t.TempDir()
	if out, err := exec.Command("git", "init", root).CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root})
	p := preparedShellCommand{mode: approval.Auto, workspaceRoot: root, cwd: root, roots: []string{root}, protected: wsp.ProtectedPaths(), commands: []string{"git"}, line: "git add file && git -c user.name=Test -c user.email=test@example.invalid commit -m test"}
	spec, err := processSpec(p)
	if err != nil {
		t.Fatal(err)
	}
	ctx, release := approval.NewPolicy(approval.Auto, false).Begin(context.Background(), approval.Request{Tool: "shell", Subject: p.line}, testutil.StaticApprover{Decision: approval.Decision{Approved: true}})
	defer release()
	command, cleanup, err := spec.Build(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := cleanup(); err != nil {
			t.Error(err)
		}
	}()
	command.Env = spec.Env
	if out, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if _, err := os.Stat(filepath.Join(root, ".invokr")); !os.IsNotExist(err) {
		t.Fatal("Auto created .invokr")
	}
}

func TestGitPushShapesAreRedirected(t *testing.T) {
	for line, want := range map[string]bool{
		"git push": true, "git -C . push origin main": true, "env FOO=bar git push": true, "timeout 60 git push -u origin x": true,
		"git stash push": false, "git status": false, "git log --grep push": false, "npm publish": false,
	} {
		if got := gitPushes(line); got != want {
			t.Errorf("gitPushes(%q) = %v, want %v", line, got, want)
		}
	}
}

func TestGHCallsAreRedirected(t *testing.T) {
	for line, want := range map[string]bool{
		"gh pr create": true, "/usr/bin/gh auth status": true, "env GH_DEBUG=1 gh pr view": true, "git push && gh pr create --fill": true, "timeout 60 gh pr create": true, "nice -n 10 gh issue list": true, "xargs -n1 gh pr merge": true, "env PATH=/usr/bin gh pr view": true,
		"git status": false, "echo gh": false, "ghq list": false, "timeout 60 rg gh .": false, "xargs grep gh": false, "env ghq list": false,
	} {
		if got := callsGH(line); got != want {
			t.Errorf("callsGH(%q) = %v, want %v", line, got, want)
		}
	}
}
