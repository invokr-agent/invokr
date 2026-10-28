// Package hub installs skills from skill hubs — git repositories holding
// skills as <dir>/SKILL.md directories — plus local paths and raw URLs.
package hub

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/invokr-agent/invokr/capability/skills"
	"github.com/invokr-agent/invokr/config"
)

const skillFilename = "SKILL.md"

// scanDepth is how many directory levels below the repo root are searched for
// SKILL.md files, matching the npx-skills convention (flat skills/<name>/ and
// categorized skills/<category>/<name>/ layouts both fit).
const scanDepth = 3

// HubSkill is an installable skill found in a hub clone.
type HubSkill struct {
	skills.Spec
	Dir string `json:"dir"` // absolute path of the skill directory in the clone
	Hub string `json:"hub"` // the configured hub entry it came from
	// Installed is filled by callers that know the installed set (the TUI
	// picker); the scan itself does not consult the skills directory.
	Installed bool `json:"installed,omitempty"`
}

// Service resolves, clones, and scans skill hubs and installs skills into the
// configured skills directory.
type Service struct {
	skillsDir, cacheDir string
	hubs                []string
}

func NewService(app config.AppConfig) *Service {
	app = app.WithDefaults()
	return &Service{skillsDir: app.Skills.Dir, cacheDir: app.Skills.HubCacheDir, hubs: app.Skills.Hubs}
}

func (s *Service) Hubs() []string { return s.hubs }

// cloneURL normalizes a hub entry: "owner/repo" GitHub shorthand becomes a
// full clone URL; anything with a scheme or scp-like form passes through.
var shorthandPattern = regexp.MustCompile(`^[\w.-]+/[\w.-]+$`)

func cloneURL(hub string) string {
	if shorthandPattern.MatchString(hub) {
		return "https://github.com/" + hub + ".git"
	}
	return hub
}

func (s *Service) clonePath(hub string) string {
	sanitized := regexp.MustCompile(`[^\w.-]+`).ReplaceAllString(hub, "__")
	return filepath.Join(s.cacheDir, sanitized)
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}

// EnsureClone makes sure a hub has a local clone, cloning shallowly on first
// use, and returns the clone path.
func (s *Service) EnsureClone(ctx context.Context, hub string) (string, error) {
	path := s.clonePath(hub)
	if _, err := os.Stat(filepath.Join(path, ".git")); err == nil {
		return path, nil
	}
	if _, err := exec.LookPath("git"); err != nil {
		return "", fmt.Errorf("skill hubs require git: install git or install from a local path or URL")
	}
	if err := os.MkdirAll(s.cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("create hub cache dir: %w", err)
	}
	if err := runGit(ctx, "", "clone", "--depth", "1", cloneURL(hub), path); err != nil {
		return "", fmt.Errorf("clone hub %q: %w", hub, err)
	}
	return path, nil
}

// Refresh pulls the latest state of a hub clone (cloning first if needed).
func (s *Service) Refresh(ctx context.Context, hub string) (string, error) {
	path, err := s.EnsureClone(ctx, hub)
	if err != nil {
		return "", err
	}
	if err := runGit(ctx, path, "pull", "--ff-only"); err != nil {
		return "", fmt.Errorf("refresh hub %q: %w", hub, err)
	}
	return path, nil
}

// Skills scans every configured hub for installable skills. With refresh set,
// hubs are pulled first; otherwise cached clones are used as-is (cloning only
// when absent). Hub errors are collected, not fatal, so one broken hub does
// not hide the others.
func (s *Service) Skills(ctx context.Context, refresh bool) ([]HubSkill, []error) {
	var found []HubSkill
	var errs []error
	resolve := s.EnsureClone
	if refresh {
		resolve = s.Refresh
	}
	for _, hub := range s.hubs {
		path, err := resolve(ctx, hub)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		found = append(found, scanClone(path, hub)...)
	}
	slices.SortFunc(found, func(a, b HubSkill) int { return cmp.Compare(a.Name, b.Name) })
	return found, errs
}

// scanClone walks a clone up to scanDepth levels deep collecting valid
// SKILL.md directories. Invalid skill files are skipped silently — a hub may
// hold skills for other agents too.
func scanClone(root string, hub string) []HubSkill {
	var found []HubSkill
	_ = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		depth := strings.Count(rel, string(filepath.Separator))
		if d.IsDir() {
			if d.Name() == ".git" || depth >= scanDepth {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != skillFilename {
			return nil
		}
		dir := filepath.Dir(path)
		skill, parseErr := skills.ParseSkillFile(path, filepath.Base(dir))
		if parseErr != nil {
			return nil
		}
		found = append(found, HubSkill{Spec: skill.Spec, Dir: dir, Hub: hub})
		return nil
	})
	return found
}
