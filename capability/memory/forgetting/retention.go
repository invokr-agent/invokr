// Package forgetting implements Invokr's opt-in memory decay: an
// Ebbinghaus-style retention model where runtime records fade over time, are
// strengthened each time they are recalled, and drop into a cold archive when
// their retention falls below a threshold. All logic here is pure; the sweep
// orchestration that writes through the store lives in sweeper.go.
//
// The package is disabled unless Config.Enabled is set; callers gate every
// invocation on that flag so behavior is byte-for-byte identical to the
// pre-forgetting store when off.
package forgetting

import (
	"math"
	"slices"
	"time"
)

// ln2 is the decay constant so that retention halves exactly every half-life.
var ln2 = math.Log(2)

// Config is the runtime forgetting configuration. It mirrors
// config.ForgettingConfig; the manager builds one from the app config so this
// package stays independent of the config package.
type Config struct {
	Enabled                                                      bool
	HalfLifeDays, MinRetention, ReinforcementFactor, MaxStrength float64
	PurgeAfterDays, RawEventRetentionDays                        int
	ExemptKinds                                                  []string
}

// Record is the retention-relevant projection of a memory record: enough to
// score decay and compute reinforcement, nothing more.
type Record struct {
	ID, Kind                  string
	Strength                  float64
	RecallCount               int
	LastRecalledAt, UpdatedAt time.Time
}

// IsExempt reports whether a record's kind is exempt from decay (e.g. user
// identity and stated preferences, which should never silently evaporate).
func (c Config) IsExempt(kind string) bool { return slices.Contains(c.ExemptKinds, kind) }

func referenceTime(rec Record) time.Time {
	if !rec.LastRecalledAt.IsZero() {
		return rec.LastRecalledAt
	}
	return rec.UpdatedAt
}

// Retention returns the current retention score for rec at now. Exempt kinds
// never decay (score 1.0). Otherwise it is the Ebbinghaus curve
// strength * exp(-ln2 * age / half_life). A zero or negative half-life disables
// decay (score = strength) so a misconfigured half-life can never erase memory.
func Retention(rec Record, now time.Time, cfg Config) float64 {
	if cfg.IsExempt(rec.Kind) {
		return 1.0
	}
	strength := rec.Strength
	if strength <= 0 {
		strength = 1.0
	}
	if cfg.HalfLifeDays <= 0 {
		return strength
	}
	ageDays := now.Sub(referenceTime(rec)).Hours() / 24
	if ageDays <= 0 {
		return strength
	}
	return strength * math.Exp(-ln2*ageDays/cfg.HalfLifeDays)
}

func ShouldForget(rec Record, now time.Time, cfg Config) bool {
	if cfg.IsExempt(rec.Kind) {
		return false
	}
	return Retention(rec, now, cfg) < cfg.MinRetention
}
