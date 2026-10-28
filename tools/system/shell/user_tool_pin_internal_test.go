package shell

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/tools/core/sandbox"
)

func TestModelWritableAliasCannotSwapUserToolMount(t *testing.T) {
	for _, when := range []string{"before-command", "after-command"} {
		t.Run(when, func(t *testing.T) {
			for _, replacement := range []string{"symlink", "directory"} {
				t.Run(replacement, func(t *testing.T) { testUserToolSwap(t, when, replacement) })
			}
		})
	}
}

func testUserToolSwap(t *testing.T, when, replacement string) {
	if !sandbox.Available() {
		t.Skip("namespace sandbox unavailable")
	}
	home, root, aliasParent := t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("HOME", home)
	canonical := filepath.Join(home, "toolchain")
	target := filepath.Join(canonical, "bin", "fixture-tool")
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("#!/bin/sh\nprintf OK\n"), 0755); err != nil {
		t.Fatal(err)
	}
	userbin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(userbin, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(userbin, "fixture-tool")); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(aliasParent, "extra-root")
	if err := os.Symlink(canonical, alias); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(home, "private-secret")
	if err := os.WriteFile(secret, []byte("SENSITIVE_FIXTURE"), 0600); err != nil {
		t.Fatal(err)
	}

	prepared := preparedShellCommand{
		command: "make", commands: []string{"make"}, allowedCommands: []string{"make", "fixture-tool"},
		workspaceRoot: root, roots: []string{root, alias}, cwd: root,
	}
	plan := sandboxPlan("/bin/bash", prepared)
	if !slices.Contains(plan.RO, target) || !slices.Contains(plan.RW, alias) {
		t.Fatalf("fixture did not build expected mounts: RO=%v RW=%v", plan.RO, plan.RW)
	}
	swapTarget := func() {
		swapPlan := plan
		swapPlan.PinnedRO = nil
		swapPlan.RO = slices.DeleteFunc(slices.Clone(plan.RO), func(p string) bool { return p == target })
		viaAlias := filepath.Join(alias, "bin", "fixture-tool")
		line := `mv "$1" "$1.original" && ln -s "$2" "$1"`
		if replacement == "directory" {
			line = `mv "$1" "$1.original" && mkdir "$1"`
		}
		swap, swapCleanup, err := sandbox.Command(context.Background(), swapPlan, root,
			[]string{"-c", line, "swap", viaAlias, secret})
		if err != nil {
			t.Fatal(err)
		}
		swapOut, swapErr := swap.CombinedOutput()
		swapCleanup()
		if swapErr != nil {
			t.Fatalf("sandboxed alias swap failed: %v: %s", swapErr, swapOut)
		}

	}
	if when == "before-command" {
		swapTarget()
	}
	cmd, cleanup, err := sandbox.Command(context.Background(), plan, root,
		[]string{"-c", "test ! -e /proc/self/fd/3 && test ! -e \"$1\" && cat \"$2\"", "probe", secret, target})
	if err != nil {
		if when == "before-command" && errors.Is(err, os.ErrInvalid) && cmd == nil && cleanup == nil {
			return
		}
		t.Fatal(err)
	}
	defer cleanup()
	if when == "after-command" {
		swapTarget()
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sandbox run failed: %v: %s", err, out)
	}
	if strings.Contains(string(out), "SENSITIVE_FIXTURE") {
		t.Fatalf("RED: model-writable alias swapped RO source to hidden secret; output=%q", out)
	}
}
