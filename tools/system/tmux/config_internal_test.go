package tmux

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateServerConfUsesExclusiveTempFile(t *testing.T) {
	path := createServerConf("")
	if path == "" {
		t.Fatal("createServerConf returned empty path")
	}
	t.Cleanup(func() { _ = os.Remove(path) })
	if filepath.Base(path) == socketName+".conf" {
		t.Fatalf("config path is predictable: %q", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "remain-on-exit on") {
		t.Fatalf("unexpected config: %q", data)
	}
}

func TestHideHolderFiltersHolderSession(t *testing.T) {
	out := hideHolder(holderSession + " 1 sh dead=0\nm-abc 1 cat dead=0")
	if out != "m-abc 1 cat dead=0" {
		t.Fatalf("holder leaked into list: %q", out)
	}
	if only := hideHolder(holderSession + " 1 sh dead=0"); only != "no sessions" {
		t.Fatalf("expected no sessions, got %q", only)
	}
}
