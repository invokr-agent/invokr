package agent

import "testing"

// Without a configured percent the trigger is the window less one response's
// reserve, clamped: small windows keep the 80% floor, large ones cap at 95%.
func TestCompactThresholdReserve(t *testing.T) {
	for _, c := range []struct {
		window  int64
		percent int
		want    int
	}{
		{61440, 0, 89},
		{16384, 0, 80},
		{200000, 0, 95},
		{61440, 70, 70},
		{0, 0, 80},
	} {
		b := &contextBudget{windowSize: c.window, compactPercent: c.percent}
		if got := b.compactThreshold(); got != c.want {
			t.Fatalf("window %d percent %d: threshold %d, want %d", c.window, c.percent, got, c.want)
		}
	}
}
