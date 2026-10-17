package zerfoo

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/invokr-ai/invokr/config"
)

func TestCacheStatusLocalPaths(t *testing.T) {
	gguf := filepath.Join(t.TempDir(), "embed.gguf")
	if err := os.WriteFile(gguf, make([]byte, 64), 0o600); err != nil {
		t.Fatalf("write gguf: %v", err)
	}
	missing := filepath.Join(t.TempDir(), "gone.gguf")

	got := CacheStatus(config.ZerfooMemoryConfig{EmbeddingPath: gguf, SummarizationPath: missing})
	if len(got) != 2 {
		t.Fatalf("expected one status per model, got %d", len(got))
	}
	if got[0].Role != "embedding" || !got[0].Cached || got[0].Bytes != 64 || got[0].Err != nil {
		t.Fatalf("existing local GGUF should report cached with its size: %+v", got[0])
	}
	if got[1].Role != "summarization" || got[1].Cached || got[1].Err == nil {
		t.Fatalf("missing local GGUF should report an error and no cache: %+v", got[1])
	}
}

// A HuggingFace ID with nothing in the manifest must resolve to "not cached"
// rather than an error: zerfoo downloads it on first load.
func TestCacheStatusUncachedModelID(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	for _, s := range CacheStatus(config.ZerfooMemoryConfig{}) {
		if s.Cached || s.Err != nil || s.Bytes != 0 {
			t.Fatalf("uncached model id should be a clean miss: %+v", s)
		}
		if s.Ref == "" {
			t.Fatalf("status should carry the configured ref: %+v", s)
		}
	}
}

func TestRepoIDStripsQuant(t *testing.T) {
	for in, want := range map[string]string{
		"Qwen/Qwen2.5-0.5B-Instruct-GGUF/Q8_0": "Qwen/Qwen2.5-0.5B-Instruct-GGUF",
		"Qwen/Qwen2.5-0.5B-Instruct-GGUF":      "Qwen/Qwen2.5-0.5B-Instruct-GGUF",
	} {
		if got := repoID(in); got != want {
			t.Fatalf("repoID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsLocalPathMatchesLoaderRule(t *testing.T) {
	for in, want := range map[string]bool{
		"/models/a.gguf":  true,
		"./a.gguf":        true,
		"../a.gguf":       true,
		"Qwen/Qwen2.5":    false,
		"models/a.gguf":   false,
		"Qwen/x/Q8_0":     false,
		"~/models/a.gguf": false,
	} {
		if got := isLocalPath(in); got != want {
			t.Fatalf("isLocalPath(%q) = %v, want %v", in, got, want)
		}
	}
}
