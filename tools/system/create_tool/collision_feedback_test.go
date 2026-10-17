package create_tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCollisionFeedbackPreservesExistingTool(t *testing.T) {
	root := t.TempDir()
	live := filepath.Join(root, "normalise_expenses")
	stage := filepath.Join(root, "stage")
	for path, text := range map[string]string{live: "old", stage: "new"} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(path, "main.py"), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	err := installStagedTool(stage, live, false)
	if err == nil {
		t.Fatal("collision accepted")
	}
	for _, want := range []string{`"action":"create"`, `"definition":{"name":"normalise_expenses","overwrite":true`, "full definition"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("missing %q: %v", want, err)
		}
	}
	old, err := os.ReadFile(filepath.Join(live, "main.py"))
	if err != nil || string(old) != "old" {
		t.Fatalf("live changed: %s %v", old, err)
	}
	if err := installStagedTool(stage, live, true); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(filepath.Join(live, "main.py"))
	if err != nil || string(updated) != "new" {
		t.Fatalf("overwrite: %s %v", updated, err)
	}
}
