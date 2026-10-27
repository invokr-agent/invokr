package config

import (
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/support/fsutil"
)

func DefaultConfigDir() string { return InvokrHomeDir() }

func DefaultConfigPath() string {
	return filepath.Join(DefaultConfigDir(), constants.DefaultConfigFile)
}

// LoadDefaultFileWithProfile loads the default config file and overlays the
// named file or router profile. Empty name loads plain.
func LoadDefaultFileWithProfile(profile string) (AppConfig, error) {
	return loadFile(DefaultConfigPath(), profile, false)
}

// LoadDefaultFileWithoutProfile lets recovery commands run when the selected
// profile is malformed, so setup can reset the shipped JSON files.
func LoadDefaultFileWithoutProfile() (AppConfig, error) {
	return loadFile(DefaultConfigPath(), "", true)
}

func loadFile(path, profile string, ignoreStoredProfile bool) (AppConfig, error) {
	cfg, existed, err := fsutil.ReadJSON[AppConfig](path)
	if err != nil {
		if existed {
			return AppConfig{}, fmt.Errorf("parse config %s: %w", path, err)
		}
		return AppConfig{}, err
	}
	if ignoreStoredProfile {
		cfg.Profile = ""
	}
	return resolveLoadedConfig(cfg, profile)
}

// resolveLoadedConfig layers the loaded file: defaults fill blanks, PERSONA.md
// replaces the prompt, the profile (when named) overlays both, and env wins
// last. Flags are bound onto the result afterwards, so they win over all of it.
func resolveLoadedConfig(cfg AppConfig, profile string) (AppConfig, error) {
	home := InvokrHomeDir()
	cfg = cfg.withDefaults(home).withPersonaFile(home)
	if profile = cmp.Or(profile, cfg.Profile); profile != "" {
		var err error
		if cfg, err = ApplyNamedProfile(cfg, home, profile); err != nil {
			return AppConfig{}, err
		}
	}
	return cfg.withEnv(), nil
}

// EnsureRouterProvider adds a provider definition and its model profile to the
// config file, creating the router table itself when the file predates it.
//
// Creating the table seeds the existing single-provider settings as a "local"
// profile and leaves it active, so adding a provider never silently reroutes a
// working setup. Switching is a separate, explicit "config use".
func EnsureRouterProvider(path, providerID, providerType, model string) error {
	return editRawConfig(path, true, func(raw map[string]any) error {
		provider := childMap(raw, "provider")
		router, _ := provider["router"].(map[string]any)
		if router == nil {
			router = seedRouterTable(provider)
			provider["router"] = router
		}
		providers := childMap(router, "providers")
		if _, ok := providers[providerID]; !ok {
			providers[providerID] = map[string]any{"type": providerType}
		}
		profiles := childMap(router, "profiles")
		if _, ok := profiles[providerID]; !ok && model != "" {
			profiles[providerID] = map[string]any{"provider": providerID, "model": model}
		}
		return nil
	})
}

func childMap(parent map[string]any, key string) map[string]any {
	child, _ := parent[key].(map[string]any)
	if child == nil {
		child = map[string]any{}
		parent[key] = child
	}
	return child
}

// seedRouterTable builds the initial router table from the single-provider
// settings already in the file, so the setup that was working keeps working and
// stays selected.
func seedRouterTable(provider map[string]any) map[string]any {
	baseURL, _ := provider["base_url"].(string)
	baseURL = cmp.Or(baseURL, constants.DefaultProviderBaseURL)
	model, _ := provider["model"].(string)
	return map[string]any{
		"active": "local",
		"providers": map[string]any{
			"local": map[string]any{
				"type": "openai-compatible", "base_url": baseURL, "options": map[string]any{"metadata_path": "/props"},
			},
		},
		"profiles": map[string]any{
			"local": map[string]any{"provider": "local", "model": model},
		},
	}
}

// SetProviderAPIKey stores a credential on one provider definition, replacing
// any api_key_env reference so the stored key is the one that takes effect.
func SetProviderAPIKey(path, providerID, apiKey string) error {
	return editRouterTable(path, func(_, router map[string]any) error {
		providers, _ := router["providers"].(map[string]any)
		definition, _ := providers[providerID].(map[string]any)
		if definition == nil {
			return fmt.Errorf("provider %q is not defined in %s", providerID, path)
		}
		definition["api_key"] = apiKey
		delete(definition, "api_key_env")
		return nil
	})
}

// editRouterTable applies edit to provider.router in the config file, leaving
// every other key's value unchanged. It edits the raw JSON
// rather than re-marshalling an AppConfig because LoadFile resolves defaults
// and environment overrides, which would otherwise be baked into the file.
func editRouterTable(path string, edit func(raw, router map[string]any) error) error {
	return editRawConfig(path, false, func(raw map[string]any) error {
		provider, _ := raw["provider"].(map[string]any)
		if provider == nil {
			return fmt.Errorf("config %s defines no provider section", path)
		}
		router, _ := provider["router"].(map[string]any)
		if router == nil {
			return fmt.Errorf("config %s defines no provider.router table", path)
		}
		return edit(raw, router)
	})
}

// SetActiveProfile persists the active profile, resolving names the way
// --profile does: a file profile under <home>/profiles first (stored as
// the top-level "profile" key, since it may set only the prompt and so needs
// no router table), then provider.router.active.
func SetActiveProfile(path, profile string) error {
	if file, err := IsFileProfile(InvokrHomeDir(), profile); err != nil {
		return err
	} else if file {
		return editRawConfig(path, false, func(raw map[string]any) error {
			raw["profile"] = profile
			return nil
		})
	}
	return editRouterTable(path, func(raw, router map[string]any) error {
		profiles, _ := router["profiles"].(map[string]any)
		if _, ok := profiles[profile]; !ok {
			return fmt.Errorf("profile %q is not defined in %s or %s", profile, path, ProfilesDir(InvokrHomeDir()))
		}
		router["active"] = profile
		delete(raw, "profile") // a router pick replaces an earlier markdown one
		return nil
	})
}

// SetSetupFields patches the three settings the TUI's Setup tab edits —
// provider.base_url, provider.model and memory.zerfoo.enabled — into the raw
// config file, leaving every other key as the user wrote it.
func SetSetupFields(path, baseURL, model string, memory bool) error {
	return editRawConfig(path, true, func(raw map[string]any) error {
		provider := childMap(raw, "provider")
		provider["base_url"], provider["model"] = baseURL, model
		childMap(childMap(raw, "memory"), "zerfoo")["enabled"] = memory
		return nil
	})
}

func SetWorkspaceReads(path, reads string) error {
	return editRawConfig(path, true, func(raw map[string]any) error {
		childMap(raw, "workspace")["reads"] = reads
		return nil
	})
}

// editRawConfig patches raw values, never resolved defaults or environment.
// Setup/seeding permit missing or empty files; router edits require valid JSON.
func editRawConfig(path string, create bool, edit func(map[string]any) error) error {
	data, err := os.ReadFile(path)
	if err != nil && (!create || !errors.Is(err, os.ErrNotExist)) {
		return err
	}
	raw := map[string]any{}
	if len(data) > 0 || !create {
		if err := json.Unmarshal(data, &raw); err != nil {
			return fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	if err := edit(raw); err != nil {
		return err
	}
	return fsutil.WriteJSON(path, raw, 0o600)
}
