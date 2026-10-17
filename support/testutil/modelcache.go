package testutil

import (
	"path/filepath"
	"runtime"
	"testing"
)

// UseRepoModelCache points model downloads at a gitignored cache inside the
// repo for the duration of the test.
//
// zerfoo caches HuggingFace models under os.UserHomeDir()/.cache/zerfoo/models
// and offers no override, so redirecting HOME is the only seam. The directory
// deliberately outlives the test: the point is that a multi-gigabyte GGUF is
// downloaded once and reused by every later run.
func UseRepoModelCache(t *testing.T) {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the repo root for the model cache")
	}
	t.Setenv("HOME", filepath.Join(filepath.Dir(file), "..", "..", ".modelcache"))
}
