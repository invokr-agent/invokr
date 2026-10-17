package tools

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/setup"
	"github.com/invokr-ai/invokr/tools/core"
)

// allowlistCaller registers two always-visible tools under the given allowlist.
func allowlistCaller(t *testing.T, allowlist ...string) *Caller {
	t.Helper()
	caller := NewCaller(core.CallerDeps{
		App: config.AppConfig{Memory: config.MemoryConfig{Dir: t.TempDir()}, Tools: config.ToolsConfig{Allowlist: allowlist}},
	})
	t.Cleanup(caller.Close)
	for _, name := range []string{"keep_me", "distraction"} {
		caller.RegisterNative(
			core.ToolSpec{Name: name, Metadata: core.ToolMetadata{AlwaysVisible: true}},
			func(context.Context, *core.ToolCall) (string, error) { return "ok", nil },
		)
	}
	return caller
}

func specNames(specs []core.ToolSpec) []string {
	names := make([]string, 0, len(specs))
	for _, s := range specs {
		names = append(names, s.Name)
	}
	return names
}

func TestCallerAdvertisesOnlyAllowlistedTools(t *testing.T) {
	caller := allowlistCaller(t, "read", "keep_me")
	if err := caller.StartToolSession("session-1"); err != nil {
		t.Fatal(err)
	}
	if names := specNames(caller.StableSpecs()); !slices.Equal(names, []string{"tools", "read"}) {
		t.Fatalf("core: %v", names)
	}
	names := specNames(caller.PromptSpecs("session-1"))
	if !slices.Contains(names, "keep_me") || slices.Contains(names, "distraction") {
		t.Fatalf("catalog: %v", names)
	}
}
func TestCallerKeepsOptionalSchemasOutOfNativeArray(t *testing.T) {
	caller := allowlistCaller(t)
	if err := caller.StartToolSession("session-1"); err != nil {
		t.Fatal(err)
	}
	names := specNames(caller.PromptSpecs("session-1"))
	if !slices.Contains(names, "keep_me") || !slices.Contains(names, "distraction") {
		t.Fatalf("catalog: %v", names)
	}
	if _, err := managerCall(t, caller, "session-1", `{"action":"enable","names":["keep_me"]}`); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(specNames(caller.StableSpecs()), "keep_me") {
		t.Fatal("enable changed native schema array")
	}
}

func TestCallerDispatchesAllowlistedToolNormally(t *testing.T) {
	caller := allowlistCaller(t, "keep_me")

	got, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "keep_me"})
	if err != nil {
		t.Fatalf("HandleCall(keep_me) error = %v, want nil", err)
	}
	if got != "ok" {
		t.Fatalf("HandleCall(keep_me) = %q, want %q", got, "ok")
	}
}

func TestCallerRejectsDispatchOfUnadvertisedTool(t *testing.T) {
	caller := allowlistCaller(t, "keep_me")

	_, err := caller.HandleCall(context.Background(), &core.ToolCall{Name: "distraction"})
	var toolErr *core.ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("HandleCall(distraction) error = %v, want *core.ToolError", err)
	}
	if toolErr.Code != "not_advertised" {
		t.Fatalf("code = %q, want not_advertised", toolErr.Code)
	}
	// Payload is non-empty so the model sees why and can pick a real tool.
	if want := `tool "distraction" is not available in this session`; toolErr.Payload != want {
		t.Fatalf("payload = %q, want %q", toolErr.Payload, want)
	}
}

func TestShippedRestrictedProfilesDoNotDispatchGitBackedSearch(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	if _, err := setup.EnsureAt(home); err != nil {
		t.Fatal(err)
	}
	runGit := func(args ...string) {
		t.Helper()
		command := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	runGit("init")
	marker := filepath.Join(t.TempDir(), "marker")
	hook := filepath.Join(t.TempDir(), "fsmonitor.sh")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nprintf bypass > '"+marker+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	runGit("config", "core.fsmonitor", hook)
	runGit("status", "--porcelain")
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("hook fixture did not run: %v", err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
	for name, mode := range map[string]approval.Mode{"researcher": approval.Auto, "planner": approval.Plan} {
		t.Run(name, func(t *testing.T) {
			app, err := config.ApplyNamedProfile(config.AppConfig{Workspace: config.WorkspaceConfig{Root: root}}, home, name)
			if err != nil {
				t.Fatal(err)
			}
			app.Toolbox.Dir = t.TempDir()
			caller := NewCaller(core.CallerDeps{App: app, Policy: approval.NewPolicy(mode, false)})
			defer caller.Close()
			_, err = caller.HandleCall(context.Background(), &core.ToolCall{Name: "file_search", Arguments: []byte(`{"query":"needle"}`)})
			var toolErr *core.ToolError
			if !errors.As(err, &toolErr) || toolErr.Code != "not_advertised" {
				t.Fatalf("file_search error = %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("fsmonitor hook executed: %v", err)
			}
		})
	}
}
