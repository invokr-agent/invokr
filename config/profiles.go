package config

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/support/frontmatter"
)

// Profile is a run configuration loaded from <home>/profiles/<name>.md: YAML
// front matter carries partial config overrides, the markdown body (when
// non-empty) replaces the base system prompt. Pointer fields distinguish
// "absent" from "zero" the same way agentdef front matter does.
type Profile struct {
	Name string
	// Provider names a router provider id; requires a configured router table.
	Provider *string
	// Model overrides the model id.
	Model *string
	// Temperature overrides the sampling temperature.
	Temperature *float64
	// SystemPrompt is the markdown body. Empty keeps the base prompt.
	SystemPrompt string
}

// profileFrontMatter is the YAML structure parsed from the --- block.
type profileFrontMatter struct {
	Provider    *string  `yaml:"provider"`
	Model       *string  `yaml:"model"`
	Temperature *float64 `yaml:"temperature"`
}

// ProfilesDir is where markdown profiles live inside an Invokr home.
func ProfilesDir(homeDir string) string { return filepath.Join(homeDir, constants.ProfilesDirName) }

// LoadProfile reads and parses <home>/profiles/<name>.md.
func LoadProfile(homeDir, name string) (Profile, error) {
	path := filepath.Join(ProfilesDir(homeDir), name+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		return Profile{}, fmt.Errorf("load profile %q: %w", name, err)
	}
	fm, body, err := frontmatter.Parse[profileFrontMatter](data)
	if err != nil {
		return Profile{}, fmt.Errorf("profile %s: %w", path, err)
	}
	return Profile{
		Name: name, Provider: fm.Provider, Model: fm.Model, Temperature: fm.Temperature, SystemPrompt: strings.TrimSpace(body),
	}, nil
}

// ListProfiles names the markdown profiles in <home>/profiles, sorted. An
// absent directory is an empty list, not an error.
func ListProfiles(homeDir string) ([]string, error) {
	entries, err := os.ReadDir(ProfilesDir(homeDir))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list profiles: %w", err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		names = append(names, strings.TrimSuffix(entry.Name(), ".md"))
	}
	slices.Sort(names)
	return names, nil
}

// ApplyNamedProfile resolves name against <home>/profiles/<name>.md first and
// the router profile table second, and overlays it onto cfg. It runs between
// the config-file defaults and withEnv, so environment variables and flags
// still win over profile values.
func ApplyNamedProfile(cfg AppConfig, homeDir, name string) (AppConfig, error) {
	if _, err := os.Stat(filepath.Join(ProfilesDir(homeDir), name+".md")); err == nil {
		profile, err := LoadProfile(homeDir, name)
		if err != nil {
			return AppConfig{}, err
		}
		return cfg.withProfile(profile)
	}
	if _, ok := cfg.Provider.Router.Profiles[name]; ok {
		cfg.Provider.Router.Active = name
		return cfg, nil
	}
	return AppConfig{}, fmt.Errorf("profile %q not found in %s or the router table", name, ProfilesDir(homeDir))
}

// withProfile overlays one markdown profile. Provider/model land in the
// router table when one is configured — a synthetic router profile keyed by
// the profile's name — because bootstrap pins the model from the active
// router profile and would otherwise undo a bare Provider.Model override.
func (c AppConfig) withProfile(p Profile) (AppConfig, error) {
	if p.Provider != nil && !c.Provider.Router.Enabled() {
		return AppConfig{}, fmt.Errorf("profile %q sets a provider but no router table is configured", p.Name)
	}
	if err := c.routeProfile(p); err != nil {
		return AppConfig{}, err
	}
	if p.Model != nil {
		c.Provider.Model = *p.Model
	}
	if p.Temperature != nil {
		c.Agent.Temperature = *p.Temperature
	}
	if p.SystemPrompt != "" {
		c.Agent.SystemPrompt, c.Agent.PromptSource = p.SystemPrompt, "profile"
	}
	return c, nil
}

// routeProfile maps the profile's provider/model onto the router table by
// installing a synthetic router profile under the same name and activating it.
// Absent provider and model leave the routing untouched.
func (c *AppConfig) routeProfile(p Profile) error {
	router := &c.Provider.Router
	if !router.Enabled() || (p.Provider == nil && p.Model == nil) {
		return nil
	}
	base, ok := router.Profiles[router.Active]
	if !ok && p.Provider == nil {
		return fmt.Errorf("profile %q sets no provider and no active router profile supplies one", p.Name)
	}
	entry := RouterProfileConfig{Provider: base.Provider, Model: base.Model}
	if p.Provider != nil {
		if _, ok := router.Providers[*p.Provider]; !ok {
			return fmt.Errorf("profile %q names unknown provider %q", p.Name, *p.Provider)
		}
		entry.Provider = *p.Provider
	}
	if p.Model != nil {
		entry.Model = *p.Model
	}
	router.Profiles[p.Name] = entry
	router.Active = p.Name
	return nil
}
