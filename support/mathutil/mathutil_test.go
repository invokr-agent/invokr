package mathutil

import (
	"math"
	"testing"
)

func TestClamp01(t *testing.T) {
	cases := []struct {
		in, want float64
	}{
		{-0.5, 0},
		{0, 0},
		{0.25, 0.25},
		{1, 1},
		{1.5, 1},
	}
	for _, c := range cases {
		if got := Clamp01(c.in); got != c.want {
			t.Fatalf("Clamp01(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestCosineWithNorms(t *testing.T) {
	a := []float32{3, 4}
	b := []float32{3, 4}
	got := CosineWithNorms(a, 5, b, 5)
	if math.Abs(got-1) > 1e-9 {
		t.Fatalf("CosineWithNorms = %v, want 1", got)
	}
	if got := CosineWithNorms(a, 0, b, 5); got != 0 {
		t.Fatalf("zero normA: got %v, want 0", got)
	}
}
