//go:build linux

package sandbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/tools/core"
)

type sealApprover struct {
	requests []approval.Request
	allow    bool
}

func (a *sealApprover) RequestApproval(_ context.Context, req approval.Request) (approval.Decision, error) {
	a.requests = append(a.requests, req)
	return approval.Decision{Approved: a.allow, Reason: "test decision"}, nil
}

func TestMissingSealsRequireScopedApproval(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, ".invokr")
	plan := Plan{RW: []string{root}, Sealed: []string{missing}, CommandPath: "/bin/sh"}
	for _, allow := range []bool{false, true} {
		a := &sealApprover{allow: allow}
		ctx, release := approval.NewPolicy(approval.Auto, false).Begin(context.Background(), approval.Request{Tool: "shell", Subject: "touch written"}, a)
		for range 2 {
			_, cleanup, err := Command(ctx, plan, root, nil)
			if cleanup != nil {
				cleanup()
			}
			if (err == nil) != allow {
				t.Fatalf("allow=%v error=%v", allow, err)
			}
		}
		release()
		if len(a.requests) != 2 || !a.requests[0].Protected || !a.requests[0].PolicyChecked || !strings.Contains(a.requests[0].Details, missing) {
			t.Fatalf("missing per-call protected-path prompt: %+v", a.requests)
		}
	}
	if _, _, err := Command(context.Background(), plan, root, nil); err == nil {
		t.Fatal("missing policy bypassed protected-path approval")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatal("approval check created host path")
	}
}

func TestForegroundAutoChecksNewProtectedNames(t *testing.T) {
	if !Available() {
		t.Skip("namespace sandbox unavailable")
	}
	for _, name := range []string{".invokr", ".git"} {
		for _, kind := range []string{"directory", "symlink", "preexisting"} {
			t.Run(name+"/"+kind, func(t *testing.T) {
				root, outside := t.TempDir(), t.TempDir()
				protected := filepath.Join(root, name)
				if err := os.WriteFile(filepath.Join(outside, "keep"), []byte("safe"), 0600); err != nil {
					t.Fatal(err)
				}
				line := "mkdir " + name
				if kind == "symlink" {
					line = "ln -s " + shellEscape(outside) + " " + name
				}
				if kind == "preexisting" {
					if err := os.Mkdir(protected, 0700); err != nil {
						t.Fatal(err)
					}
					line = "touch written"
				}
				plan := BuildPlan("/bin/sh", "", "", root, core.ToolCapabilities{WritesWorkspace: true}, 0, nil)
				plan.Sealed = []string{protected}
				a := &sealApprover{}
				ctx, release := approval.NewPolicy(approval.Auto, false).Begin(context.Background(), approval.Request{Tool: "shell"}, a)
				defer release()
				result, err := core.RunProcess(ctx, ProcessSpec(plan, root, []string{"-c", line}, true))
				if kind == "preexisting" {
					if err != nil {
						t.Fatal(err)
					}
					if _, err := os.Stat(protected); err != nil {
						t.Fatal(err)
					}
				} else {
					if err == nil || !strings.Contains(err.Error(), "removed newly created protected path "+protected) || result.ExitCode == 0 {
						t.Fatalf("result=%+v err=%v", result, err)
					}
					if _, err := os.Lstat(protected); !os.IsNotExist(err) {
						t.Fatalf("created name remains: %v", err)
					}
				}
				if len(a.requests) != 0 {
					t.Fatal("foreground Auto prompted")
				}
				if _, err := os.Stat(filepath.Join(outside, "keep")); err != nil {
					t.Fatal("followed symlink target:", err)
				}
			})
		}
	}
}

func TestForegroundCleanupPreservesOverlappingApprovedCreation(t *testing.T) {
	if !Available() {
		t.Skip("namespace sandbox unavailable")
	}
	for _, finished := range []bool{false, true} {
		t.Run(fmt.Sprint("finished=", finished), func(t *testing.T) {
			root := t.TempDir()
			plan := BuildPlan("/bin/sh", "", "", root, core.ToolCapabilities{WritesWorkspace: true}, 0, nil)
			plan.Sealed = []string{filepath.Join(root, ".invokr")}
			a := &sealApprover{allow: true}
			ctx, release := approval.NewPolicy(approval.Auto, false).Begin(context.Background(), approval.Request{Tool: "shell"}, a)
			defer release()
			foreground, check, err := ProcessSpec(plan, root, []string{"-c", "touch written"}, true).Build(ctx)
			if err != nil {
				t.Fatal(err)
			}
			persistent, cleanup, err := Command(ctx, plan, root, []string{"-c", "mkdir .invokr && echo approved > .invokr/keep"})
			if err != nil {
				_ = check()
				t.Fatal(err)
			}
			if err := persistent.Run(); err != nil {
				cleanup()
				_ = check()
				t.Fatal(err)
			}
			if finished {
				cleanup()
			} else {
				defer cleanup()
			}
			if err := foreground.Run(); err != nil {
				_ = check()
				t.Fatal(err)
			}
			if err := check(); err == nil || !strings.Contains(err.Error(), "retained because approved execution overlapped") {
				t.Fatalf("check = %v", err)
			}
			if data, err := os.ReadFile(filepath.Join(root, ".invokr", "keep")); err != nil || string(data) != "approved\n" {
				t.Fatalf("approved creation removed: %q %v", data, err)
			}
			if len(a.requests) != 1 {
				t.Fatalf("approval count %d", len(a.requests))
			}
		})
	}
}

func TestCleanupDoesNotFollowReplacedProtectedParent(t *testing.T) {
	if !Available() {
		t.Skip("namespace sandbox unavailable")
	}
	root := t.TempDir()
	for _, dir := range []string{".git", "victim/hooks"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	keep := filepath.Join(root, "victim/hooks/keep")
	if err := os.WriteFile(keep, []byte("safe"), 0600); err != nil {
		t.Fatal(err)
	}
	plan := BuildPlan("/bin/sh", "", "", root, core.ToolCapabilities{WritesWorkspace: true}, 0, nil)
	plan.Sealed = []string{filepath.Join(root, ".git/hooks")}
	ctx, release := approval.NewPolicy(approval.Auto, false).Begin(context.Background(), approval.Request{Tool: "shell"})
	defer release()
	_, err := core.RunProcess(ctx, ProcessSpec(plan, root, []string{"-c", "mv .git oldgit && ln -s victim .git"}, true))
	if err == nil || !strings.Contains(err.Error(), "parent changed") {
		t.Fatalf("error = %v", err)
	}
	if data, err := os.ReadFile(keep); err != nil || string(data) != "safe" {
		t.Fatalf("followed replaced parent: %q %v", data, err)
	}
}

func TestExistingSealProtectsOnlyItsPath(t *testing.T) {
	if !Available() {
		t.Skip("namespace sandbox unavailable")
	}
	root := t.TempDir()
	protected := filepath.Join(root, ".invokr")
	if err := os.Mkdir(protected, 0700); err != nil {
		t.Fatal(err)
	}
	plan := BuildPlan("/bin/sh", "", "", root, core.ToolCapabilities{WritesWorkspace: true}, 0, nil)
	plan.Sealed = []string{protected}
	a := &sealApprover{}
	ctx, release := approval.NewPolicy(approval.Auto, false).Begin(context.Background(), approval.Request{Tool: "shell"}, a)
	defer release()
	cmd, cleanup, err := Command(ctx, plan, root, []string{"-c", "touch written && ! touch .invokr/denied"})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	if len(a.requests) != 0 {
		t.Fatal("existing sealed path prompted")
	}
	if _, err := os.Stat(filepath.Join(root, "written")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(protected, "denied")); !os.IsNotExist(err) {
		t.Fatal("protected path writable")
	}
}
