package retrieval

import "testing"

func TestDiscoveryClassification(t *testing.T) {
	for _, tc := range []struct {
		path      string
		doc, skip bool
	}{
		{"README.md", true, false}, {"AGENTS.md", true, false}, {"docs/notes.MARKDOWN", true, false},
		{"docs/a.TxT", true, false}, {"docs/source.go", false, false}, {"docs/.git", false, true},
		{".invokr", false, true}, {"docs/.hidden", false, true}, {"docs/visible", false, false},
	} {
		if isRetrievalDoc(tc.path) != tc.doc || shouldSkipDir(tc.path) != tc.skip {
			t.Errorf("classification changed for %q", tc.path)
		}
	}
}

func TestMergeHitsBoundaries(t *testing.T) {
	for _, limit := range []int{0, 1, 2, 9} {
		first := []Hit{{PathOrID: "low", Score: 1}}
		second := []Hit{{PathOrID: "high", Score: 2}}
		got := mergeHits(first, second, limit)
		if len(got) != min(limit, 2) {
			t.Fatalf("limit %d: %d hits", limit, len(got))
		}
		if len(got) > 0 && got[0].PathOrID != "high" {
			t.Fatalf("lost ranking: %+v", got)
		}
		if first[0].PathOrID != "low" || second[0].PathOrID != "high" {
			t.Fatal("input mutated")
		}
	}
	if got := mergeHits(nil, nil, 3); got == nil || len(got) != 0 {
		t.Fatalf("empty output changed: %#v", got)
	}
}
