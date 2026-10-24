package clients

import (
	"testing"
	"time"
)

func TestJitteredStaysWithinHalfToOneAndAHalf(t *testing.T) {
	seen := map[time.Duration]bool{}
	for range 200 {
		d := jittered(time.Second)
		if d < 500*time.Millisecond || d >= 1500*time.Millisecond {
			t.Fatalf("jittered(1s) = %s, outside [500ms, 1.5s)", d)
		}
		seen[d] = true
	}
	if len(seen) < 2 {
		t.Fatal("jittered returned the same delay every time")
	}
}
