package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/invokr-ai/invokr/constants"
)

// PromptSource is recorded with every system prompt a run sends, and it is the
// only thing that says which of four overlapping sources produced the text. It
// is set in three different places (withDefaults, withPersonaFile, withEnv), so
// the layering is pinned here rather than in any one of them.
func TestPromptSourceAttribution(t *testing.T) {
	custom := "You are a custom agent."
	tests := []struct {
		name    string
		cfg     AppConfig
		persona string
		env     string
		want    string
		wantTxt string
	}{
		{name: "default", want: "default", wantTxt: constants.DefaultAgentSystemPrompt},
		{name: "config", cfg: AppConfig{Agent: AgentConfig{SystemPrompt: custom}}, want: "config", wantTxt: custom},
		{name: "persona", persona: "Persona prompt.", want: "persona", wantTxt: "Persona prompt."},
		{name: "env", env: "Env prompt.", want: "env", wantTxt: "Env prompt."},
		{name: "empty env is not an override", env: "", want: "default", wantTxt: constants.DefaultAgentSystemPrompt},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("INVOKR_HOME", home)
			t.Setenv("INVOKR_SYSTEM_PROMPT", tc.env)
			if tc.persona != "" {
				if err := os.WriteFile(filepath.Join(home, constants.PersonaFilename), []byte(tc.persona), 0o600); err != nil {
					t.Fatalf("write persona: %v", err)
				}
			}
			got, err := resolveLoadedConfig(tc.cfg, "")
			if err != nil {
				t.Fatalf("resolveLoadedConfig() error = %v", err)
			}
			if got.Agent.PromptSource != tc.want {
				t.Fatalf("PromptSource = %q, want %q", got.Agent.PromptSource, tc.want)
			}
			if got.Agent.SystemPrompt != tc.wantTxt {
				t.Fatalf("SystemPrompt = %q, want %q", got.Agent.SystemPrompt, tc.wantTxt)
			}
		})
	}
}
