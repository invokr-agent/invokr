package skills

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/invokr-ai/invokr/config"

	"go.uber.org/zap"
	"golang.org/x/sync/singleflight"
)

const skillFilename = "SKILL.md"

// mtimeSafetyMargin guards against coarse filesystem mtime granularity (1s on
// ext4/HFS+, 2s on FAT). A directory mtime is only trustworthy as "unchanged
// since the scan" once it is this far behind the scan-completion timestamp;
// anything closer could be a same-tick write that the filesystem's clock
// can't distinguish from "before the scan" (the classic racy-stat problem).
const mtimeSafetyMargin = 2 * time.Second

type FilesystemManager struct {
	dir          string
	logger       *zap.Logger
	loadGroup    singleflight.Group
	mu           sync.Mutex
	cachedSkills []Skill
	cacheLoaded  bool
	// scanCompletedAt is our own wall-clock reading taken right after the
	// last scan finished. Invalidate trusts the cache only when the skills
	// dir's mtime is unambiguously older than this (see mtimeSafetyMargin) —
	// comparing against our fine-grained clock instead of a coarse mtime
	// observed at scan start, so a change landing in the same filesystem
	// mtime tick as the scan is never invisible.
	scanCompletedAt time.Time
}

func NewManagerFromConfig(app config.AppConfig, logger *zap.Logger) Manager {
	app = app.WithDefaults()
	if logger == nil {
		logger = zap.NewNop()
	}
	return &FilesystemManager{dir: app.Skills.Dir, logger: logger}
}

// Invalidate drops the cached skill list so the next Specs/Load re-scans disk.
// It keeps the cache when the skills dir's mtime is safely older than the
// last scan (installing or removing a skill bumps the parent dir), so
// repeated calls made well after a scan cost one stat, not a rescan; calls
// made within mtimeSafetyMargin of the scan pay for a rescan instead, since
// the mtime evidence isn't precise enough yet to trust either way.
func (m *FilesystemManager) Invalidate() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if info, err := os.Stat(m.dir); err == nil && m.cacheLoaded &&
		info.ModTime().Before(m.scanCompletedAt.Add(-mtimeSafetyMargin)) {
		return
	}
	m.cacheLoaded = false
	m.loadGroup.Forget("skills")
}

// Specs backs `/skills` listings and the hub picker's installed markers, which
// must reflect changes made outside this process — Invalidate's mtime gate
// keeps that a single stat once enough time has passed since the last scan.
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
		// Record completion time after reading, under our own clock: any
		// write racing the scan is then within mtimeSafetyMargin of this
		// timestamp (or later), so the next Invalidate rescans rather than
		// missing it.
		completedAt := time.Now()

		m.mu.Lock()
		defer m.mu.Unlock()
		if !m.cacheLoaded {
			m.cachedSkills = cloneSkills(loaded)
			m.cacheLoaded = true
			m.scanCompletedAt = completedAt
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
