package core

import "testing"

func TestDedupPreserveOrder(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"empty", nil, []string{}},
		{"no dupes", []string{"a", "b", "c"}, []string{"a", "b", "c"}},
		{"keeps first occurrence order", []string{"b", "a", "b", "c", "a"}, []string{"b", "a", "c"}},
		{"all dupes", []string{"x", "x", "x"}, []string{"x"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := DedupPreserveOrder(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("DedupPreserveOrder(%v) = %v, want %v", tc.in, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("DedupPreserveOrder(%v) = %v, want %v", tc.in, got, tc.want)
				}
			}
		})
	}
}

func TestTruncateRunes(t *testing.T) {
	cases := []struct {
		name  string
		s     string
		limit int
		want  string
	}{
		{"under limit", "hello", 10, "hello"},
		{"exact limit", "hello", 5, "hello"},
		{"over limit", "hello world", 5, "hello"},
		{"zero limit", "hello", 0, ""},
		{"negative limit", "hello", -1, ""},
		{"multibyte runes", "héllo", 2, "hé"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := TruncateRunes(tc.s, tc.limit)
			if got != tc.want {
				t.Fatalf("TruncateRunes(%q, %d) = %q, want %q", tc.s, tc.limit, got, tc.want)
			}
		})
	}
}

func TestFirstNonEmpty(t *testing.T) {
	if got := FirstNonEmpty("", "  ", "value", "other"); got != "value" {
		t.Fatalf("FirstNonEmpty = %q, want %q", got, "value")
	}
	if got := FirstNonEmpty("", ""); got != "" {
		t.Fatalf("FirstNonEmpty = %q, want empty", got)
	}
}
