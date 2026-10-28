package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/invokr-agent/invokr/constants"
)

// PersonaPath is where the markdown base system prompt lives inside an
// Invokr home.
func PersonaPath(homeDir string) string { return filepath.Join(homeDir, constants.PersonaFilename) }

// loadPersonaFile reads <homeDir>/PERSONA.md as the base system prompt.
// A missing, empty or unreadable file — or anything that is not a regular
// file (Lstat runs first, so symlinks and directories are skipped) — yields
// ("", false) and the config value stays in effect.
func loadPersonaFile(homeDir string) (string, bool) {
	path := PersonaPath(homeDir)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", false
	}
	return content, true
}

// withPersonaFile overlays the PERSONA.md prompt over whatever the config
// file (or the default) supplied. It runs before withEnv, so
// INVOKR_SYSTEM_PROMPT and command-line flags still win.
func (c AppConfig) withPersonaFile(homeDir string) AppConfig {
	if prompt, ok := loadPersonaFile(homeDir); ok {
		c.Agent.SystemPrompt, c.Agent.PromptSource = prompt, "persona"
	}
	return c
}
