package skills

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/invokr-agent/invokr/config"

	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

const skillFilename = "SKILL.md"

type FilesystemManager struct {
	dir          string
	logger       *zap.Logger
	loadGroup    singleflight.Group
	mu           sync.Mutex
	cachedSkills []Skill
	cacheLoaded  bool
}

func NewManagerFromConfig(app config.AppConfig, logger *zap.Logger) Manager {
	app = app.WithDefaults()
	logger = cmp.Or(logger, zap.NewNop())
	return &FilesystemManager{dir: app.Skills.Dir, logger: logger}
}

// Invalidate drops the cached skill list so the next Specs/Load re-scans disk.
func (m *FilesystemManager) Invalidate() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cacheLoaded = false
	m.loadGroup.Forget("skills")
}

// Specs refreshes listings, including edits within existing skill directories.
func (m *FilesystemManager) Specs(ctx context.Context) ([]Spec, error) {
	m.Invalidate()
	skills, err := m.list(ctx)
	if err != nil {
		return nil, err
	}
	specs := make([]Spec, 0, len(skills))
	for _, skill := range skills {
		specs = append(specs, skill.Spec)
	}
	return specs, nil
}

func (m *FilesystemManager) Load(ctx context.Context, name string) (Skill, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Skill{}, fmt.Errorf("skill name is required")
	}
	// On a miss the skill may have been installed after the cache was built, so
	// the second pass invalidates and re-scans once before giving up.
	for attempt := range 2 {
		if attempt > 0 {
			m.Invalidate()
		}
		skills, err := m.list(ctx)
		if err != nil {
			return Skill{}, err
		}
		if i := slices.IndexFunc(skills, func(skill Skill) bool { return skill.Name == name }); i >= 0 {
			return skills[i], nil
		}
	}
	return Skill{}, fmt.Errorf("unknown skill %q", name)
}

func (m *FilesystemManager) list(ctx context.Context) ([]Skill, error) {
	if skills, ok := m.cached(); ok {
		return skills, nil
	}

	loadedAny, err, _ := m.loadGroup.Do("skills", func() (any, error) {
		if skills, ok := m.cached(); ok {
			return skills, nil
		}
		loaded, err := m.load(ctx)
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		if !m.cacheLoaded {
			m.cachedSkills = cloneSkills(loaded)
			m.cacheLoaded = true
		}
		return cloneSkills(m.cachedSkills), nil
	})
	if err != nil {
		return nil, err
	}
	loaded, _ := loadedAny.([]Skill)
	return loaded, nil
}

// cached returns a copy of the cached skill list, reporting whether one was
// loaded. Callers must not hold m.mu.
func (m *FilesystemManager) cached() ([]Skill, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.cacheLoaded {
		return nil, false
	}
	return cloneSkills(m.cachedSkills), true
}

func (m *FilesystemManager) load(ctx context.Context) ([]Skill, error) {
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	loaded := make([]Skill, 0, len(entries))
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !entry.IsDir() {
			continue
		}
		path := filepath.Join(m.dir, entry.Name(), skillFilename)
		skill, err := ParseSkillFile(path, entry.Name())
		if err != nil {
			m.logger.Warn("skipping skill", zap.String("path", path), zap.Error(err))
			continue
		}
		// The directory is the skill's identity: install and remove key on it,
		// so list and load must too, whatever the frontmatter says.
		skill.Name = entry.Name()
		loaded = append(loaded, skill)
	}
	return loaded, nil
}

func cloneSkills(skills []Skill) []Skill {
	if len(skills) == 0 {
		return nil
	}
	return slices.Clone(skills)
}
