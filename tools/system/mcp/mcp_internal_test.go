package mcp

import "testing"

func TestMCPArgsSubjectFormatsSortedKeyValuePairs(t *testing.T) {
	got := mcpArgsSubject(map[string]any{"text": "hello", "count": 1})
	want := "count=1 text=hello"
	if got != want {
		t.Fatalf("expected subject %q, got %q", want, got)
	}
}

func TestMCPArgsSubjectEmptyForNoArgs(t *testing.T) {
	if got := mcpArgsSubject(nil); got != "" {
		t.Fatalf("expected empty subject for no args, got %q", got)
	}
}
