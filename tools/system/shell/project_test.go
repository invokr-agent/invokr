package shell

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeMarker(t *testing.T, root, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestShellDescriptionNamesDetectedEcosystemGaps(t *testing.T) {
	root := t.TempDir()
	writeMarker(t, root, "pyproject.toml")
	writeMarker(t, root, "setup.py") // same ecosystem: must not repeat
	got := shellDescription([]string{"git", "ls"}, root)
	want := "Detected project: python (pyproject.toml). Not allowlisted: python3, pytest"
	if !strings.Contains(got, want) {
		t.Fatalf("description missing project line %q:\n%s", want, got)
	}
	if strings.Count(got, "Detected project: python") != 1 {
		t.Fatalf("python ecosystem reported more than once:\n%s", got)
	}
	if !strings.Contains(got, "invokr doctor") || !strings.Contains(got, "shell.allowed_commands") {
		t.Fatalf("description missing remedy:\n%s", got)
	}
}

func TestShellDescriptionOmitsProjectLineWhenCovered(t *testing.T) {
	root := t.TempDir()
	writeMarker(t, root, "go.mod")
	got := shellDescription([]string{"go", "gofmt", "git"}, root)
	if strings.Contains(got, "Detected project") {
		t.Fatalf("covered project must not be flagged:\n%s", got)
	}
}

func TestMissingProjectCommands(t *testing.T) {
	root := t.TempDir()
	writeMarker(t, root, "package.json")
	got := MissingProjectCommands(root, []string{"git"})
	if len(got) != 1 || got[0] != "npm" {
		t.Fatalf("want [npm], got %v", got)
	}
	if got := MissingProjectCommands(root, []string{"npm"}); len(got) != 0 {
		t.Fatalf("want none, got %v", got)
	}
}
