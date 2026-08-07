package lsp

import (
	"context"
	"strings"
	"testing"
)

// A call on a file no installed server handles must name the extension and the
// server that is missing, so the model reads "tool not installed" rather than
// "language not supported" (#304).
func TestForNamesTheMissingServerForTheExtension(t *testing.T) {
	manager := NewManager(t.TempDir(), []ServerConfig{
		{Command: "definitely-not-installed-server", Extensions: []string{".py"}, LanguageID: "python"},
	})

	_, err := manager.For(context.Background(), "src/app.py")
	if err == nil {
		t.Fatal("expected an error for an uninstalled server")
	}
	for _, want := range []string{".py", "definitely-not-installed-server", "not installed on PATH"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not mention %q", err, want)
		}
	}
}

func TestForUnconfiguredExtensionListsActiveExtensions(t *testing.T) {
	manager := NewManager(t.TempDir(), []ServerConfig{
		{Command: "definitely-not-installed-server", Extensions: []string{".py"}, LanguageID: "python"},
	})

	_, err := manager.For(context.Background(), "notes.rb")
	if err == nil {
		t.Fatal("expected an error for an unconfigured extension")
	}
	if !strings.Contains(err.Error(), ".rb") || !strings.Contains(err.Error(), "active extensions: none") {
		t.Fatalf("error %q should name the extension and the active extensions", err)
	}
}
