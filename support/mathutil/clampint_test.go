package mathutil

import "testing"

func TestClampInt(t *testing.T) {
	cases := []struct {
		name        string
		v, def, max int
		want        int
	}{
		{"zero returns default", 0, 8, 50, 8},
		{"negative returns default", -3, 8, 50, 8},
		{"above max clamps", 99, 8, 50, 50},
		{"in range unchanged", 20, 8, 50, 20},
		{"equal max unchanged", 50, 8, 50, 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClampInt(tc.v, tc.def, tc.max); got != tc.want {
				t.Fatalf("ClampInt(%d,%d,%d)=%d want %d", tc.v, tc.def, tc.max, got, tc.want)
			}
		})
	}
}
