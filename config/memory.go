package config

type MemoryScope string

const (
	ScopeUser    MemoryScope = "user"
	ScopeProject MemoryScope = "project"
	ScopeSession MemoryScope = "session"
)
