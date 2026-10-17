package registry

import (
	"slices"
	"sync"
	"testing"
)

func TestMapSnapshotsInput(t *testing.T) {
	defs := map[string]int{"b": 2, "a": 1}
	m := NewMap(defs)
	defs["a"] = 99
	delete(defs, "b")
	defs["c"] = 3
	if got, ok := m.Get("a"); !ok || got != 1 {
		t.Fatalf("Get(a) = %d, %v; want the construction-time value", got, ok)
	}
	if got := m.Names(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("Names() = %v; caller mutation changed the snapshot", got)
	}
}

func TestMapNamesDoNotAliasRegistry(t *testing.T) {
	m := NewMap(map[string]int{"b": 2, "a": 1})
	names := m.Names()
	names[0] = "changed"
	if got := m.Names(); !slices.Equal(got, []string{"a", "b"}) {
		t.Fatalf("Names() = %v; returned names must be independent", got)
	}
}

func TestMapZeroValue(t *testing.T) {
	var m Map[int]
	if value, ok := m.Get("missing"); ok || value != 0 {
		t.Fatalf("zero Map.Get = %d, %v", value, ok)
	}
	if got := m.Names(); got != nil {
		t.Fatalf("zero Map.Names = %v; want nil", got)
	}
}

func TestMapSnapshotIsShallow(t *testing.T) {
	value := 1
	m := NewMap(map[string]*int{"a": &value})
	got, ok := m.Get("a")
	if !ok || got != &value {
		t.Fatal("snapshot changed referenced value identity")
	}
}

func TestMapConcurrentReadsWhileCallerReusesInput(t *testing.T) {
	defs := map[string]int{"b": 2, "a": 1}
	m := NewMap(defs)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				if got, ok := m.Get("a"); !ok || got != 1 {
					t.Errorf("Get(a) = %d, %v", got, ok)
					return
				}
				if got := m.Names(); !slices.Equal(got, []string{"a", "b"}) {
					t.Errorf("Names() = %v", got)
					return
				}
			}
		}()
	}
	for i := range 100 {
		defs["a"] = i
		delete(defs, "b")
	}
	wg.Wait()
}
