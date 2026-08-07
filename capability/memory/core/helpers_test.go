package core

import "testing"

func TestSortHitsByScore(t *testing.T) {
	hits := []Hit{{Score: 0.2}, {Score: 0.9}, {Score: 0.5}}
	SortHitsByScore(hits)
	want := []float64{0.9, 0.5, 0.2}
	for i, w := range want {
		if hits[i].Score != w {
			t.Fatalf("hits[%d].Score = %v, want %v", i, hits[i].Score, w)
		}
	}
}
