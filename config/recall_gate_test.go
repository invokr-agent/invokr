package config

import (
	"testing"

	"github.com/invokr-ai/invokr/constants"
)

func TestRecallGateConfig_Defaults(t *testing.T) {
	c := RecallGateConfig{}.withDefaults()
	if c.Enabled {
		t.Fatal("expected Enabled to default to false")
	}
	if c.NothinkSuffix != constants.DefaultRecallGateNothinkSuffix {
		t.Fatalf("NothinkSuffix = %q, want %q", c.NothinkSuffix, constants.DefaultRecallGateNothinkSuffix)
	}
	if c.MaxQueries != constants.DefaultRecallGateMaxQueries {
		t.Fatalf("MaxQueries = %d, want %d", c.MaxQueries, constants.DefaultRecallGateMaxQueries)
	}
	if c.PerQueryLimit != constants.DefaultRecallGatePerQueryLimit {
		t.Fatalf("PerQueryLimit = %d, want %d", c.PerQueryLimit, constants.DefaultRecallGatePerQueryLimit)
	}
	if c.MaxHits != constants.DefaultRecallGateMaxHits {
		t.Fatalf("MaxHits = %d, want %d", c.MaxHits, constants.DefaultRecallGateMaxHits)
	}
	if c.BudgetChars != constants.DefaultRecallGateBudgetChars {
		t.Fatalf("BudgetChars = %d, want %d", c.BudgetChars, constants.DefaultRecallGateBudgetChars)
	}
	if c.MinMessageChars != constants.DefaultRecallGateMinMessageChars {
		t.Fatalf("MinMessageChars = %d, want %d", c.MinMessageChars, constants.DefaultRecallGateMinMessageChars)
	}
	if c.TimeoutMS != constants.DefaultRecallGateTimeoutMS {
		t.Fatalf("TimeoutMS = %d, want %d", c.TimeoutMS, constants.DefaultRecallGateTimeoutMS)
	}
}

func TestRecallGateConfig_EnvOverrides(t *testing.T) {
	t.Setenv("INVOKR_MEMORY_RECALL_GATE_ENABLED", "true")
	t.Setenv("INVOKR_MEMORY_RECALL_GATE_MODEL", "gate-model")
	t.Setenv("INVOKR_MEMORY_RECALL_GATE_NOTHINK_SUFFIX", "/nt")
	t.Setenv("INVOKR_MEMORY_RECALL_GATE_MAX_QUERIES", "7")
	t.Setenv("INVOKR_MEMORY_RECALL_GATE_PER_QUERY_LIMIT", "9")
	t.Setenv("INVOKR_MEMORY_RECALL_GATE_MAX_HITS", "11")
	t.Setenv("INVOKR_MEMORY_RECALL_GATE_BUDGET_CHARS", "1234")
	t.Setenv("INVOKR_MEMORY_RECALL_GATE_MIN_MESSAGE_CHARS", "3")
	t.Setenv("INVOKR_MEMORY_RECALL_GATE_TIMEOUT_MS", "4000")

	c := RecallGateConfig{}.withDefaults()
	applyEnv(&c)
	if !c.Enabled {
		t.Fatal("expected Enabled true from env")
	}
	if c.Model != "gate-model" {
		t.Fatalf("Model = %q, want gate-model", c.Model)
	}
	if c.NothinkSuffix != "/nt" {
		t.Fatalf("NothinkSuffix = %q, want /nt", c.NothinkSuffix)
	}
	if c.MaxQueries != 7 || c.PerQueryLimit != 9 || c.MaxHits != 11 {
		t.Fatalf("query/hit overrides not applied: %+v", c)
	}
	if c.BudgetChars != 1234 || c.MinMessageChars != 3 || c.TimeoutMS != 4000 {
		t.Fatalf("numeric overrides not applied: %+v", c)
	}
}

func TestRecallGateConfig_RoundTripThroughAppConfig(t *testing.T) {
	app := AppConfig{}.WithDefaults()
	if app.Memory.RecallGate.MaxQueries != constants.DefaultRecallGateMaxQueries {
		t.Fatalf("AppConfig did not apply recall gate defaults: %+v", app.Memory.RecallGate)
	}
}
