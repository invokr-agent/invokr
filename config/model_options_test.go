package config

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/invokr-ai/invokr/support/fsutil"
)

func TestModelOptionValidation(t *testing.T) {
	for _, tc := range []struct {
		key, value string
		valid      bool
	}{
		{"max_tokens", "", true}, {"max_tokens", "omit", true}, {"max_tokens", "32768", true},
		{"max_tokens", "0", false}, {"max_tokens", "1.5", false}, {"max_tokens", "-1", false},
		{"temperature", "0", true}, {"temperature", "2", true}, {"temperature", "2.01", false},
		{"temperature", "NaN", false}, {"temperature", "+Inf", false}, {"temperature", "abc", false},
		{"top_p", "0.9", true}, {"top_p", "1.1", false}, {"unknown", "1", false},
	} {
		err := ValidateModelOptionValues(map[string]string{tc.key: tc.value})
		if (err == nil) != tc.valid {
			t.Fatalf("%s=%q: %v", tc.key, tc.value, err)
		}
	}
	var numeric *strconv.NumError
	if err := ValidateModelOptionValues(map[string]string{"temperature": "abc"}); !errors.As(err, &numeric) {
		t.Fatalf("lost numeric cause: %v", err)
	}
	choice := ModelOption{Label: "Continuation", Choices: []string{"true", "false"}}
	if choice.Validate("true") != nil || choice.Validate("yes") == nil {
		t.Fatal("choice validation failed")
	}
	seen := map[string]bool{}
	for _, o := range ModelOptionRegistry {
		if seen[o.Key] || o.Key == "" || o.Read == nil || o.Apply == nil || o.Validate(o.Default) != nil {
			t.Fatalf("invalid registry entry: %+v", o)
		}
		seen[o.Key] = true
	}
}

func TestModelOptionsPersistencePreservesRawConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	initial := `{"unknown":{"keep":true},"agent":{"n_predict":16384,"model_options":{"other":{"top_p":"0.8"}}}}`
	if err := os.WriteFile(path, []byte(initial), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INVOKR_TEMPERATURE", "0.9")
	if err := SetModelOptions(path, "model/a", map[string]string{"temperature": "0", "max_tokens": "omit"}); err != nil {
		t.Fatal(err)
	}
	cfg, _, err := fsutil.ReadJSON[AppConfig](path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.ModelOptions["model/a"]["temperature"] != "0" || cfg.Agent.ModelOptions["other"]["top_p"] != "0.8" {
		t.Fatalf("wrong models: %+v", cfg.Agent.ModelOptions)
	}
	raw, _, _ := fsutil.ReadJSON[map[string]any](path)
	if raw["unknown"] == nil {
		t.Fatal("unrelated field lost")
	}
	before, _ := os.ReadFile(path)
	if err := SetModelOptions(path, "model/a", map[string]string{"temperature": "NaN"}); err == nil {
		t.Fatal("invalid value saved")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("failed validation modified file")
	}
	if err := SetModelOptions(path, "model/a", map[string]string{"temperature": ""}); err != nil {
		t.Fatal(err)
	}
	cfg, _, _ = fsutil.ReadJSON[AppConfig](path)
	if _, ok := cfg.Agent.ModelOptions["model/a"]; ok {
		t.Fatal("inherit did not remove overrides")
	}
}

func TestModelOptionsStartupValidation(t *testing.T) {
	t.Setenv("INVOKR_TOP_P", "bad")
	if err := (AppConfig{}).WithDefaults().Validate(nil); err == nil {
		t.Fatal("invalid new env accepted")
	}
	t.Setenv("INVOKR_TOP_P", "")
	cfg := AppConfig{}.WithDefaults()
	cfg.Agent.ModelOptions = map[string]map[string]string{"unused": {"max_tokens": "-5"}}
	if err := cfg.Validate(nil); err == nil {
		t.Fatal("invalid inactive model accepted")
	}
}
