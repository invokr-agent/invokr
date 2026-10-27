package core

import (
	"cmp"
	"slices"
	"time"
)

func SortHitsByScore(hits []Hit) {
	slices.SortStableFunc(hits, func(a, b Hit) int { return cmp.Compare(b.Score, a.Score) })
}

// UTCNow returns the current UTC time formatted as RFC3339 with nanoseconds,
// the canonical timestamp format used by the memory stores.
func UTCNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }
