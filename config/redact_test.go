package config

import "testing"

// TestRedacted_APIKeyEnv verifies APIKeyEnv on router providers is masked only
// when it does not look like a real environment variable name.
func TestRedacted_APIKeyEnv(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"env var name stays readable", "ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY"},
		{"pasted key is masked", "sk-ant-api03-abcdefghijklmnop", "****mnop"},
		{"empty stays empty", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := AppConfig{}
			cfg.Provider.Router.Providers = map[string]RouterProviderConfig{
				"p": {APIKeyEnv: tt.in},
			}
			got := cfg.Redacted()
			if got := got.Provider.Router.Providers["p"].APIKeyEnv; got != tt.want {
				t.Fatalf("APIKeyEnv = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestRedacted_APIKeyStillMasks confirms the existing top-level and router
// provider APIKey masking still works.
func TestRedacted_APIKeyStillMasks(t *testing.T) {
	cfg := AppConfig{}
	cfg.Provider.APIKey = "sk-top-level-secret"
	cfg.Provider.Router.Providers = map[string]RouterProviderConfig{
		"p": {APIKey: "sk-router-secret"},
	}
	got := cfg.Redacted()

	if got.Provider.APIKey == "sk-top-level-secret" {
		t.Fatal("top-level APIKey was not masked")
	}
	if got.Provider.Router.Providers["p"].APIKey == "sk-router-secret" {
		t.Fatal("router provider APIKey was not masked")
	}
}
