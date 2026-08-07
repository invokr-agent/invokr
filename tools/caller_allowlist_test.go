package tools

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/tools/core"
)

// allowlistCaller registers two always-visible tools under the given allowlist.
func allowlistCaller(t *testing.T, allowlist ...string) *Caller {
	t.Helper()
	caller := NewCaller(core.CallerDeps{
		App: config.AppConfig{Tools: config.ToolsConfig{Allowlist: allowlist}},
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
	caller := allowlistCaller(t, "keep_me")

	for _, tc := range []struct {
		name  string
		specs []core.ToolSpec
	}{
		{"callable", caller.CallableSpecs("session-1")},
		{"prompt", caller.PromptSpecs("session-1")},
	} {
		names := specNames(tc.specs)
		if !slices.Contains(names, "keep_me") {
			t.Fatalf("%s specs = %v, want keep_me advertised", tc.name, names)
		}
		if slices.Contains(names, "distraction") {
			t.Fatalf("%s specs = %v, want distraction filtered out", tc.name, names)
		}
	}
}

// With no allowlist every tool is advertised, but only the core set ships its
// schema: the rest is listed for tool_search and loads on its first call (#480).
func TestCallerAdvertisesEveryToolWhenAllowlistUnset(t *testing.T) {
	caller := allowlistCaller(t)
	names := specNames(caller.PromptSpecs("session-1"))
	for _, want := range []string{"keep_me", "distraction"} {
		if !slices.Contains(names, want) {
			t.Fatalf("prompt specs = %v, want %q advertised", names, want)
		}
	}
	if callable := specNames(caller.CallableSpecs("session-1")); slices.Contains(callable, "keep_me") {
		t.Fatalf("callable specs = %v, want keep_me deferred", callable)
	}
	if _, err := caller.HandleCall(core.WithSessionID(context.Background(), "session-1"), &core.ToolCall{Name: "keep_me"}); err != nil {
		t.Fatalf("first call to a deferred tool: %v", err)
	}
	if callable := specNames(caller.CallableSpecs("session-1")); !slices.Contains(callable, "keep_me") {
		t.Fatalf("callable specs = %v, want keep_me loaded after first call", callable)
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
