package shell

import (
	"os"
	"path/filepath"
	"testing"
)

// $GOMODCACHE wins over the GOPATH default, as it does for go itself.
func TestGoModCacheHonoursTheEnvironment(t *testing.T) {
	t.Setenv("GOMODCACHE", "/cache/mod")
	if got := goModCache(); got != "/cache/mod" {
		t.Fatalf("goModCache() = %q, want /cache/mod", got)
	}

	t.Setenv("GOMODCACHE", "")
	t.Setenv("GOPATH", "/first"+string(os.PathListSeparator)+"/second")
	if got, want := goModCache(), filepath.Join("/first", "pkg", "mod"); got != want {
		t.Fatalf("goModCache() = %q, want %q", got, want)
	}
}
