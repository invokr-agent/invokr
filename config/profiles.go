package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/support/frontmatter"
)

// Profile is a partial run configuration loaded from JSON or Markdown.
type Profile struct {
	Name string `json:"-" yaml:"-"`
	// Provider names a router provider id; requires a configured router table.
	Provider    *string  `json:"provider" yaml:"provider"`
	Model       *string  `json:"model" yaml:"model"`
	Temperature *float64 `json:"temperature" yaml:"temperature"`
	// SystemPrompt is the markdown body. Empty keeps the base prompt.
	SystemPrompt string `json:"system_prompt" yaml:"-"`
	Tools        *struct {
		Allowlist *[]string `json:"allowlist" yaml:"allowlist"`
	} `json:"tools" yaml:"tools"`
	Mode *string `json:"mode" yaml:"mode"`
}

func ProfilesDir(homeDir string) string { return filepath.Join(homeDir, constants.ProfilesDirName) }

// LoadProfile reads a Markdown or JSON profile. Markdown wins on a name collision.
func LoadProfile(homeDir, name string) (Profile, error) {
	path := filepath.Join(ProfilesDir(homeDir), name+".md")
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		path = filepath.Join(ProfilesDir(homeDir), name+".json")
		data, err = os.ReadFile(path)
		if err != nil {
			return Profile{}, fmt.Errorf("load profile %q: %w", name, err)
		}
		p := Profile{Name: name}
		if err := json.Unmarshal(data, &p); err != nil {
			return Profile{}, fmt.Errorf("profile %s: %w", path, err)
		}
		return p, nil
	}
	if err != nil {
		return Profile{}, fmt.Errorf("load profile %q: %w", name, err)
	}
	p, body, err := frontmatter.Parse[Profile](data)
	if err != nil {
		return Profile{}, fmt.Errorf("profile %s: %w", path, err)
	}
	p.Name, p.SystemPrompt = name, strings.TrimSpace(body)
	return p, nil
}

// ListProfiles names the file profiles in <home>/profiles, sorted. An
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
	seen := map[string]bool{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") && !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		name := strings.TrimSuffix(strings.TrimSuffix(entry.Name(), ".md"), ".json")
		if !seen[name] {
			names = append(names, name)
			seen[name] = true
		}
	}
	slices.Sort(names)
	return names, nil
}

// IsFileProfile checks both supported file formats before router fallback.
func IsFileProfile(homeDir, name string) (bool, error) {
	for _, ext := range []string{".md", ".json"} {
		path := filepath.Join(ProfilesDir(homeDir), name+ext)
		_, err := os.Stat(path)
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return false, fmt.Errorf("check profile %s: %w", name, err)
		}
	}
	return false, nil
}

// ApplyNamedProfile resolves name against <home>/profiles files first and
// the router profile table second, and overlays it onto cfg. It runs between
// the config-file defaults and withEnv, so environment variables and flags
// still win over profile values.
func ApplyNamedProfile(cfg AppConfig, homeDir, name string) (AppConfig, error) {
	if file, err := IsFileProfile(homeDir, name); err != nil {
		return AppConfig{}, err
	} else if file {
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

// withProfile overlays one file profile. Provider/model land in the
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
	if p.Tools != nil && p.Tools.Allowlist != nil {
		c.Tools.Allowlist = slices.Clone(*p.Tools.Allowlist)
	}
	if p.Mode != nil {
		if !slices.Contains([]string{"manual", "auto", "plan"}, *p.Mode) {
			return AppConfig{}, fmt.Errorf("profile %q has invalid mode %q", p.Name, *p.Mode)
		}
		c.SessionMode = *p.Mode
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
