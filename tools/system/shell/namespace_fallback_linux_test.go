package shell_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/capability/approval"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/support/testutil"
	"github.com/invokr-agent/invokr/tools"
	"github.com/invokr-agent/invokr/tools/core"
	"github.com/invokr-agent/invokr/tools/core/sandbox"
)

type fallbackApprover struct {
	testutil.StaticApprover
	requests []approval.Request
}

func (a *fallbackApprover) RequestApproval(ctx context.Context, req approval.Request) (approval.Decision, error) {
	a.requests = append(a.requests, req)
	return a.StaticApprover.RequestApproval(ctx, req)
}

// The containment gate starts a compiled test binary with a failing unshare
// shim on PATH, before sandbox's one-time support probe runs.
func TestShellNamespaceUnavailableFallback(t *testing.T) {
	if os.Getenv("INVOKR_TEST_NAMESPACE_UNAVAILABLE") != "1" {
		t.Skip("run by the simulated namespace-unavailable containment stage")
	}
	if sandbox.Available() {
		t.Fatal("namespace probe unexpectedly succeeded; shim was not active before test startup")
	}
	root, outside := t.TempDir(), t.TempDir()
	marker := filepath.Join(root, "marker.txt")
	if err := os.WriteFile(filepath.Join(root, "marker.sh"), []byte("#!/bin/sh\nprintf ran > marker.txt\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	call := func(mode approval.Mode, approved bool, cwd, command string) (string, *fallbackApprover, error) {
		policy := approval.NewPolicy(mode, false)
		approver := &fallbackApprover{StaticApprover: testutil.StaticApprover{Decision: approval.Decision{Approved: approved, Reason: "declined fixture"}}}
		caller := tools.NewCaller(core.CallerDeps{Approver: approver, Policy: policy, App: config.AppConfig{
			Toolbox: config.ToolboxConfig{Dir: t.TempDir()}, Workspace: config.WorkspaceConfig{Root: root},
			Shell: config.ShellConfig{AllowedCommands: []string{"echo", "pwd"}, AllowWriteCommands: true},
		}})
		ctx, release := policy.Begin(context.Background(), approval.Request{Tool: "shell", Subject: "./marker.sh"}, approver)
		defer release()
		args, _ := json.Marshal(map[string]string{"command": command, "cwd": cwd})
		out, err := caller.HandleCall(ctx, &core.ToolCall{Name: "shell", Arguments: args})
		return out, approver, err
	}
	assertAbsent := func() {
		t.Helper()
		if _, err := os.Stat(marker); !os.IsNotExist(err) {
			t.Fatalf("script wrote marker before approval: %v", err)
		}
	}
	t.Setenv(sandbox.AllowUnsandboxedEnv, "0")
	t.Run("DefaultRefusesScript", func(t *testing.T) {
		if _, approver, err := call(approval.Manual, true, "", "./marker.sh"); err == nil || len(approver.requests) != 0 {
			t.Fatalf("default fallback accepted script: %v", err)
		}
		assertAbsent()
	})
	t.Run("AllowlistedCommandRuns", func(t *testing.T) {
		if out, _, err := call(approval.Manual, true, "", "pwd"); err != nil || !strings.Contains(out, root) {
			t.Fatalf("allowlisted command failed on unsupported host: %q %v", out, err)
		}
		assertAbsent()
	})
	t.Setenv(sandbox.AllowUnsandboxedEnv, "1")
	t.Run("DeniedApprovalDisclosesHost", func(t *testing.T) {
		var denied *approval.DeniedError
		if _, approver, err := call(approval.Manual, false, "", "./marker.sh"); !errors.As(err, &denied) {
			t.Fatalf("denied approval accepted script: %v", err)
		} else if len(approver.requests) != 1 || approver.requests[0].Confined || !approver.requests[0].RequiresScopedApproval || !strings.Contains(approver.requests[0].Details, "UNSANDBOXED") {
			t.Fatalf("approval concealed host fallback: %+v", approver.requests)
		}
		assertAbsent()
	})
	t.Run("OutsideCwdRejected", func(t *testing.T) {
		if _, approver, err := call(approval.Manual, true, outside, "./marker.sh"); err == nil || len(approver.requests) != 0 {
			t.Fatalf("outside cwd accepted: %v", err)
		}
		assertAbsent()
	})
	t.Run("PlanRefusesHostFallback", func(t *testing.T) {
		var denied *approval.DeniedError
		if _, _, err := call(approval.Plan, true, "", "./marker.sh"); !errors.As(err, &denied) {
			t.Fatalf("Plan accepted host fallback: %v", err)
		}
		assertAbsent()
	})
	t.Run("ApprovedOptInRuns", func(t *testing.T) {
		if _, approver, err := call(approval.Manual, true, "", "./marker.sh"); err != nil || len(approver.requests) != 1 || approver.requests[0].Confined {
			t.Fatalf("approved opt-in failed or claimed confinement: %v %+v", err, approver.requests)
		}
		if data, err := os.ReadFile(marker); err != nil || string(data) != "ran" {
			t.Fatalf("approved script did not run: %q %v", data, err)
		}
	})
}
