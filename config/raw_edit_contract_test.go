package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRawConfigEditsPreserveCreationContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	if err := SetActiveProfile(path, "local"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("router edit must not create a file: %v", err)
	}
	if err := SetSetupFields(path, "http://saved", "saved-model", true); err != nil {
		t.Fatal(err)
	}
	for _, content := range []string{"", "{broken", "{}", `{"provider":{}}`, `{"provider":{"router":{"profiles":{}}}}`} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := SetActiveProfile(path, "local"); err == nil {
			t.Fatalf("router edit accepted %q", content)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != content {
			t.Fatalf("failed edit changed file: %q, %v", got, err)
		}
	}
}

func TestRawSetupDoesNotPersistEnvironmentOrDropUnknownValues(t *testing.T) {
	t.Setenv("INVOKR_MODEL", "temporary-env-model")
	path := filepath.Join(t.TempDir(), "config.json")
	const original = `{"future":{"nested":[1,"keep",false]},"provider":{"api_key_env":"CUSTOM_KEY"}}`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := SetSetupFields(path, "http://saved", "saved-model", true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var before, after map[string]any
	if err := json.Unmarshal([]byte(original), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before["future"], after["future"]) || strings.Contains(string(data), "temporary-env-model") || !strings.Contains(string(data), "CUSTOM_KEY") {
		t.Fatalf("raw values lost or environment persisted: %s", data)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("config permissions changed: %v, %v", info, err)
	}
}
