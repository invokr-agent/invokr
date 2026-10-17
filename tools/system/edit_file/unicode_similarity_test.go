package edit_file

import (
	"errors"
	"math"
	"strings"
	"testing"
)

func TestLineSimilarityVisitsEveryUTF8Byte(t *testing.T) {
	for _, tc := range []struct {
		left, right string
		want        float64
	}{
		{"é", "ø", 0.5},
		{"é\né", "ø\nø", 0.6},
		{"界", "🙂", 0},
		{"same", "same", 1},
		{"abc", "xyz", 0},
		{"abc", "abx", 2.0 / 3},
		{"", "x", 0},
	} {
		t.Run(tc.left+"/"+tc.right, func(t *testing.T) {
			got := lineSimilarity([]string{tc.left}, []string{tc.right})
			if math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("similarity(%q, %q)=%v, want %v", tc.left, tc.right, got, tc.want)
			}
			if reverse := lineSimilarity([]string{tc.right}, []string{tc.left}); reverse != got {
				t.Fatalf("asymmetric similarity: %v versus %v", got, reverse)
			}
		})
	}
}

func TestBlockAnchorRejectsDifferentUnicodeInterior(t *testing.T) {
	const old = "anchor\né\né"
	const content = "anchor\nø\nø"
	if spans := locateBlockAnchor(content, old); len(spans) != 0 {
		t.Fatalf("different Unicode interior accepted: %v", spans)
	}
	if _, _, err := replaceIn(content, old, "replacement", false, "\n"); !errors.Is(err, errOldNotFound) {
		t.Fatalf("unsafe fuzzy replacement was accepted: %v", err)
	}
	if got, n, err := replaceIn(content, content, old, false, "\n"); err != nil || got != old || n != 1 {
		t.Fatalf("exact Unicode replacement changed: %q, %d, %v", got, n, err)
	}
}

func TestLineSimilarityRetainsWorkBound(t *testing.T) {
	if got := lineSimilarity([]string{strings.Repeat("a", 2049)}, []string{strings.Repeat("b", 2049)}); got != 0 {
		t.Fatalf("oversized comparison scored %v", got)
	}
}
