package testutil

import (
	"encoding/json"
	"path/filepath"
	"testing"
)

func MustWriteToolboxPlugin(
	t testing.TB,
	root string,
	name string,
	description string,
	files map[string]string,
) string {
	t.Helper()

	pluginDir := filepath.Join(root, name)

	exampleBytes, err := json.Marshal(map[string]any{
		"name":      name,
		"arguments": map[string]any{},
	})
	if err != nil {
		t.Fatalf("marshal toolbox example for %s: %v", name, err)
	}

	specBytes, err := json.Marshal(map[string]any{
		"name":        name,
		"description": description,
		"example":     string(exampleBytes),
		"namespace":   "toolbox",
		"policy": map[string]any{
			"tiers":    []string{"read_workspace"},
			"approval": "auto",
		},
		"capabilities": map[string]any{
			"reads_workspace":  false,
			"writes_workspace": false,
			"network":          false,
			"executes_code":    true,
		},
		"parameters": map[string]any{
			"type":                 "object",
			"additionalProperties": true,
		},
	})
	if err != nil {
		t.Fatalf("marshal toolbox spec for %s: %v", name, err)
	}

	MustWriteFile(t, filepath.Join(pluginDir, "tool.json"), string(specBytes))

	for relPath, content := range files {
		MustWriteFile(t, filepath.Join(pluginDir, relPath), content)
	}

	return pluginDir
}
