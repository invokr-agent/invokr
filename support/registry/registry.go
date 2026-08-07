// Package registry provides immutable name-to-definition snapshots.
package registry

import (
	"maps"
	"slices"
)

// Map is a read-only registry. Its key/value associations never change after
// construction, so concurrent lookups need no lock. The zero value is empty.
type Map[T any] struct {
	defs map[string]T
}

// NewMap snapshots defs, which callers may reuse after this call returns.
// The copy is shallow: referenced data inside T is not cloned or protected.
func NewMap[T any](defs map[string]T) *Map[T] {
	return &Map[T]{defs: maps.Clone(defs)}
}

// Get returns the definition registered under name, if any.
func (m *Map[T]) Get(name string) (T, bool) {
	d, ok := m.defs[name]
	return d, ok
}

// Names returns all registered names, sorted, in an independent slice.
func (m *Map[T]) Names() []string {
	return slices.Sorted(maps.Keys(m.defs))
}
