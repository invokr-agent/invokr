package jobs

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestTemporaryExecutableWarning(t *testing.T) {
	temporary := filepath.Join(t.TempDir(), "go-build", "invokr")
	t.Setenv("TMPDIR", filepath.Dir(filepath.Dir(temporary)))
	if warning := temporaryExecutableWarning(temporary); !strings.Contains(warning, "install Invokr") {
		t.Fatalf("temporary executable warning = %q", warning)
	}
	installed := filepath.Join(filepath.Dir(t.TempDir()), "installed", "invokr")
	if warning := temporaryExecutableWarning(installed); warning != "" {
		t.Fatalf("installed executable warning = %q, want empty", warning)
	}
}
