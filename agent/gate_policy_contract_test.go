package agent

import (
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/config"
	"go.uber.org/zap"
)

func TestGatePolicyPrecedenceAndFallback(t *testing.T) {
	for _, tc := range []struct {
		name, fallback string
		overrides      map[string]string
		spill          bool
	}{
		{"default cap", "cap", nil, false},
		{"default spill", "spill", nil, true},
		{"unknown default", "future", nil, true},
		{"empty default", "", nil, true},
		{"override cap", "spill", map[string]string{"demo": "cap"}, false},
		{"override spill", "cap", map[string]string{"demo": "spill"}, true},
		{"empty override", "cap", map[string]string{"demo": ""}, true},
		{"unknown override", "cap", map[string]string{"demo": "future"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("INVOKR_HOME", t.TempDir())
			gate := newResultGate(config.AgentConfig{ToolResultMaxTokens: 100, ToolResultStrategy: tc.fallback, ToolResultStrategies: tc.overrides}, t.Name(), zap.NewNop())
			full := strings.Repeat("x", 2000)
			got, ref := gate.cap("demo", nil, full)
			if (ref != "") != tc.spill {
				t.Fatalf("wrong policy: ref=%q, result=%.100q", ref, got)
			}
			if !tc.spill && got != capToolResult(full, 200) {
				t.Fatal("cap must use twice the normal gate budget")
			}
		})
	}
}

func TestGateCopiesOverridesAndKeepsFailureBudget(t *testing.T) {
	t.Setenv("INVOKR_HOME", t.TempDir())
	overrides := map[string]string{"demo": "cap"}
	gate := newResultGate(config.AgentConfig{ToolResultMaxTokens: 100, ToolResultStrategy: "spill", ToolResultStrategies: overrides}, t.Name(), zap.NewNop())
	overrides["demo"] = "spill"
	full := strings.Repeat("x", 2000)
	if got, ref := gate.cap("demo", nil, full); ref != "" || got != capToolResult(full, 200) {
		t.Fatal("caller mutation changed configured policy")
	}
	gate.spill = failingSpillStore{}
	if got, ref := gate.cap("other", nil, full); ref != "" || got != capToolResult(full, 100) {
		t.Fatal("spill failure must use the normal, not doubled, cap")
	}
}
