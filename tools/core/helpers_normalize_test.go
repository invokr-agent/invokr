package core

import (
	"reflect"
	"slices"
	"testing"
)

func TestNormalizeStringsContract(t *testing.T) {
	for _, tc := range []struct {
		input []string
		lower bool
		want  []string
	}{
		{nil, false, []string{}}, {[]string{}, true, []string{}}, {[]string{" ", "\t"}, false, []string{}},
		{[]string{" B ", "a", "B", "b", "Ä", "ä"}, false, []string{"B", "a", "b", "Ä", "ä"}},
		{[]string{" B ", "a", "B", "b", "Ä", "ä"}, true, []string{"a", "b", "ä"}},
	} {
		before := slices.Clone(tc.input)
		got := NormalizeStrings(tc.input, tc.lower)
		if !reflect.DeepEqual(got, tc.want) || !reflect.DeepEqual(tc.input, before) {
			t.Fatalf("NormalizeStrings(%q,%v)=%#v, want %#v; input now %#v", before, tc.lower, got, tc.want, tc.input)
		}
	}
}
