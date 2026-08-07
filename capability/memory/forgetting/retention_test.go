package forgetting

import (
	"math"
	"testing"
	"time"
)

func testConfig() Config {
	return Config{
		Enabled:               true,
		HalfLifeDays:          14,
		MinRetention:          0.15,
		ReinforcementFactor:   1.5,
		MaxStrength:           10.0,
		PurgeAfterDays:        0,
		RawEventRetentionDays: 7,
		ExemptKinds:           []string{"preference", "profile"},
	}
}

func TestRetentionHalvesAtHalfLife(t *testing.T) {
	cfg := testConfig()
	now := time.Now().UTC()
	rec := Record{Kind: "fact", Strength: 1.0, UpdatedAt: now.Add(-14 * 24 * time.Hour)}
	got := Retention(rec, now, cfg)
	if math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("retention at one half-life = %v, want 0.5", got)
	}
}

func TestRetentionExemptKindNeverDecays(t *testing.T) {
	cfg := testConfig()
	now := time.Now().UTC()
	rec := Record{Kind: "preference", Strength: 1.0, UpdatedAt: now.Add(-365 * 24 * time.Hour)}
	if got := Retention(rec, now, cfg); got != 1.0 {
		t.Fatalf("exempt retention = %v, want 1.0", got)
	}
	if ShouldForget(rec, now, cfg) {
		t.Fatalf("exempt record should never be forgotten")
	}
}

func TestRetentionUsesLastRecalledOverUpdated(t *testing.T) {
	cfg := testConfig()
	now := time.Now().UTC()
	rec := Record{
		Kind:           "fact",
		Strength:       1.0,
		UpdatedAt:      now.Add(-365 * 24 * time.Hour),
		LastRecalledAt: now.Add(-14 * 24 * time.Hour),
	}
	if got := Retention(rec, now, cfg); math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("retention = %v, want 0.5 (anchored on last_recalled_at)", got)
	}
}

func TestRetentionZeroHalfLifeDisablesDecay(t *testing.T) {
	cfg := testConfig()
	cfg.HalfLifeDays = 0
	now := time.Now().UTC()
	rec := Record{Kind: "fact", Strength: 2.0, UpdatedAt: now.Add(-1000 * 24 * time.Hour)}
	if got := Retention(rec, now, cfg); got != 2.0 {
		t.Fatalf("retention with zero half-life = %v, want strength 2.0", got)
	}
}

func TestReinforceCapsAtMaxStrength(t *testing.T) {
	cfg := testConfig()
	now := time.Now().UTC()
	rec := Record{Kind: "fact", Strength: 8.0, RecallCount: 3}
	got := Reinforce(rec, now, cfg)
	if got.Strength != cfg.MaxStrength {
		t.Fatalf("strength = %v, want capped at %v", got.Strength, cfg.MaxStrength)
	}
	if got.RecallCount != 4 {
		t.Fatalf("recall_count = %d, want 4", got.RecallCount)
	}
	if !got.LastRecalledAt.Equal(now) {
		t.Fatalf("last_recalled_at = %v, want %v", got.LastRecalledAt, now)
	}
}

func TestReinforceMultipliesBelowCap(t *testing.T) {
	cfg := testConfig()
	now := time.Now().UTC()
	rec := Record{Kind: "fact", Strength: 1.0}
	got := Reinforce(rec, now, cfg)
	if math.Abs(got.Strength-1.5) > 1e-9 {
		t.Fatalf("strength = %v, want 1.5", got.Strength)
	}
}

func TestShouldForgetBoundary(t *testing.T) {
	cfg := testConfig()
	now := time.Now().UTC()
	cases := []struct {
		name     string
		ageDays  float64
		strength float64
		want     bool
	}{
		{"fresh", 0, 1.0, false},
		{"one half-life", 14, 1.0, false}, // 0.5 >= 0.15
		{"deep decay", 60, 1.0, true},     // well below 0.15
		{"reinforced survives", 60, 10.0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := Record{Kind: "fact", Strength: tc.strength, UpdatedAt: now.Add(-time.Duration(tc.ageDays*24) * time.Hour)}
			if got := ShouldForget(rec, now, cfg); got != tc.want {
				t.Fatalf("ShouldForget = %v, want %v (retention %v)", got, tc.want, Retention(rec, now, cfg))
			}
		})
	}
}

func TestShouldPurge(t *testing.T) {
	now := time.Now().UTC()
	cfg := testConfig()
	if ShouldPurge(now.Add(-30*24*time.Hour), now, cfg) {
		t.Fatalf("purge disabled when PurgeAfterDays == 0")
	}
	cfg.PurgeAfterDays = 10
	if !ShouldPurge(now.Add(-11*24*time.Hour), now, cfg) {
		t.Fatalf("expected purge for row older than 10 days")
	}
	if ShouldPurge(now.Add(-5*24*time.Hour), now, cfg) {
		t.Fatalf("did not expect purge for row younger than 10 days")
	}
	if ShouldPurge(time.Time{}, now, cfg) {
		t.Fatalf("zero archivedAt must never purge")
	}
}
