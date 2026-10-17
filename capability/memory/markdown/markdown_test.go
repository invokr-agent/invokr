package markdown

import (
	"strings"
	"testing"
)

func TestNewMemoryReturnsHomeResolutionError(t *testing.T) {
	t.Setenv("HOME", "")

	memory, err := NewMemory(Config{})
	if err == nil || !strings.Contains(err.Error(), "resolve memory dir") {
		t.Fatalf("NewMemory() = (%v, %v), want resolution error", memory, err)
	}
}

func TestNewMemoryUsesConfiguredDirWithoutHome(t *testing.T) {
	t.Setenv("HOME", "")

	memory, err := NewMemory(Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatalf("NewMemory() error = %v", err)
	}
	if memory == nil {
		t.Fatal("NewMemory() returned nil")
	}
}
