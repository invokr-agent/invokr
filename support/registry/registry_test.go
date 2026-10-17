package registry

import (
	"slices"
	"testing"
)

func TestMapGetAndNames(t *testing.T) {
	m := NewMap(map[string]int{"b": 2, "a": 1})
	if v, ok := m.Get("a"); !ok || v != 1 {
		t.Fatalf("Get(a) = %d, %v; want 1, true", v, ok)
	}
	if _, ok := m.Get("missing"); ok {
		t.Fatalf("Get(missing) should report not found")
	}
	if got := m.Names(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("Names() = %v, want [a b]", got)
	}
}

func TestNewMapNil(t *testing.T) {
	m := NewMap[string](nil)
	if _, ok := m.Get("x"); ok {
		t.Fatalf("empty registry should hold nothing")
	}
	if got := m.Names(); len(got) != 0 {
		t.Fatalf("Names() = %v, want empty", got)
	}
}
