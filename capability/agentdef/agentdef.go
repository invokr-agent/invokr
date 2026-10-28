// Package agentdef owns parsing and validation of markdown agent-definition
// files stored in ~/.invokr/agents/<name>.md.
package agentdef

import (
	"errors"
	"fmt"
	"github.com/invokr-agent/invokr/support/registry"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/invokr-agent/invokr/support/frontmatter"
)

var nameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

type Definition struct {
	Name          string   // from frontmatter
	Description   string   // from frontmatter
	Model         string   // from frontmatter; "" = inherit parent model
	MaxTurns      int      // from frontmatter; 0 = use delegate tool's max_turns
	Tools         []string // nil = full tool set; non-nil (even empty) = allowlist
	ToolsExplicit bool     // true when "tools:" key was present in frontmatter
	Instructions  string   // markdown body (system-prompt addition)
	sourcePath    string   // file the definition was loaded from (for error reporting)
}

type Registry interface {
	Get(name string) (Definition, bool)
	// Names returns all registered definition names, sorted.
	Names() []string
}

func EmptyRegistry() Registry { return registry.NewMap[Definition](nil) }

// NewRegistryFromDir scans dir for *.md files, parses each, and returns a
// Registry. Errors in individual files are accumulated and returned together.
// An empty or absent dir returns a non-nil Registry with zero definitions.
func NewRegistryFromDir(dir string) (Registry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return EmptyRegistry(), nil
		}
		return nil, fmt.Errorf("agentdef: read dir %s: %w", dir, err)
	}
	defs, errs := loadDirEntries(dir, entries)
	errs = append(errs, checkCollisions(defs)...)
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	m := make(map[string]Definition, len(defs))
	for _, def := range defs {
		m[def.Name] = def
	}
	return registry.NewMap(m), nil
}

func loadDirEntries(dir string, entries []os.DirEntry) ([]Definition, []error) {
	var errs []error
	defs := make([]Definition, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			errs = append(errs, fmt.Errorf("agentdef: read %s: %w", path, readErr))
			continue
		}
		def, parseErr := ParseFile(path, data)
		if parseErr != nil {
			errs = append(errs, parseErr)
			continue
		}
		def.sourcePath = path
		defs = append(defs, def)
	}
	return defs, errs
}

func checkCollisions(defs []Definition) []error {
	var errs []error
	seen := make(map[string]string, len(defs))
	for _, def := range defs {
		if prev, ok := seen[def.Name]; ok {
			errs = append(errs, fmt.Errorf("agentdef: name collision for %q (files: %s and %s)", def.Name, prev, def.sourcePath))
		} else {
			seen[def.Name] = def.sourcePath
		}
	}
	return errs
}

// frontMatter is the YAML structure parsed from the --- block.
type frontMatter struct {
	Name        string    `yaml:"name"`
	Description string    `yaml:"description"`
	Model       string    `yaml:"model"`
	MaxTurns    int       `yaml:"max_turns"`
	Tools       *[]string `yaml:"tools"` // pointer to distinguish absent vs. empty
}

// ParseFile reads and validates a single agent definition file.
// path is used only for error messages; data is the raw file content.
func ParseFile(path string, data []byte) (Definition, error) {
	fm, body, err := frontmatter.Parse[frontMatter](data)
	if err != nil {
		return Definition{}, fmt.Errorf("agentdef: %s: %w", path, err)
	}

	// Checked in order; the first failure names the file and the rule broken.
	for _, check := range []struct {
		broken bool
		reason string
	}{
		{strings.TrimSpace(fm.Name) == "", "name is required"},
		{!nameRe.MatchString(fm.Name), "name must match [a-z][a-z0-9-]*"},
		{strings.TrimSpace(fm.Description) == "", "description is required"},
		{fm.Model != "" && strings.TrimSpace(fm.Model) == "", "model must not be blank if specified"},
		{fm.MaxTurns < 0, "max_turns must be >= 0"},
		{strings.TrimSpace(body) == "", "instructions body is required"},
	} {
		if check.broken {
			return Definition{}, fmt.Errorf("agentdef: %s: %s", path, check.reason)
		}
	}

	def := Definition{Name: fm.Name, Description: fm.Description, Model: fm.Model, MaxTurns: fm.MaxTurns,
		ToolsExplicit: fm.Tools != nil, Instructions: strings.TrimSpace(body)}
	if fm.Tools != nil {
		def.Tools = *fm.Tools
	}
	return def, nil
}
