package skills

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/config"

	"go.uber.org/zap"
)

func TestFilesystemManagerListsAndLoadsSkills(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "grill-me", `---
name: grill-me
description: Stress-test a plan.
---

Ask hard questions.
`)

	manager := NewManagerFromConfig(config.AppConfig{
		Skills: config.SkillsConfig{Dir: root},
	}, zap.NewNop())

	specs, err := manager.Specs(context.Background())
	if err != nil {
		t.Fatalf("Specs() error = %v", err)
	}
	if len(specs) != 1 || specs[0].Name != "grill-me" || specs[0].Description != "Stress-test a plan." {
		t.Fatalf("unexpected specs: %#v", specs)
	}

	skill, err := manager.Load(context.Background(), "grill-me")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if skill.Instructions != "Ask hard questions." {
		t.Fatalf("unexpected instructions: %q", skill.Instructions)
	}
}

func TestFilesystemManagerRescansOnCacheMiss(t *testing.T) {
	root := t.TempDir()
	manager := NewManagerFromConfig(config.AppConfig{
		Skills: config.SkillsConfig{Dir: root},
	}, zap.NewNop())

	if _, err := manager.Load(context.Background(), "late-arrival"); err == nil {
		t.Fatal("expected unknown skill error before install")
	}

	writeSkill(t, root, "late-arrival", `---
name: late-arrival
description: Installed mid-session.
---

Loaded.
`)

	skill, err := manager.Load(context.Background(), "late-arrival")
	if err != nil {
		t.Fatalf("Load() after install error = %v", err)
	}
	if skill.Instructions != "Loaded." {
		t.Fatalf("unexpected instructions: %q", skill.Instructions)
	}
}

// TestFilesystemManagerRescansUnderCoarseMtimeGranularity simulates a
// filesystem with second-granularity mtimes (common on CI runners, unlike a
// typical dev ext4 box) by forcing the skills dir's mtime to the exact same
// truncated value both before and after installing a skill. A gate that
// merely compares the dir's mtime to the mtime observed at the previous scan
// (whether by equality or a naive "older than" check with no safety margin)
// cannot tell these two moments apart and keeps the stale cache forever.
func TestFilesystemManagerRescansUnderCoarseMtimeGranularity(t *testing.T) {
	root := t.TempDir()
	coarse := time.Now().Truncate(time.Second)
	if err := os.Chtimes(root, coarse, coarse); err != nil {
		t.Fatal(err)
	}

	manager := NewManagerFromConfig(config.AppConfig{
		Skills: config.SkillsConfig{Dir: root},
	}, zap.NewNop())

	if _, err := manager.Load(context.Background(), "late-arrival"); err == nil {
		t.Fatal("expected unknown skill error before install")
	}

	writeSkill(t, root, "late-arrival", `---
name: late-arrival
description: Installed mid-session.
---

Loaded.
`)

	// Collapse the post-install mtime back onto the exact same truncated
	// instant as the pre-scan stat, as a coarse filesystem clock would.
	if err := os.Chtimes(root, coarse, coarse); err != nil {
		t.Fatal(err)
	}

	skill, err := manager.Load(context.Background(), "late-arrival")
	if err != nil {
		t.Fatalf("Load() after install error = %v", err)
	}
	if skill.Instructions != "Loaded." {
		t.Fatalf("unexpected instructions: %q", skill.Instructions)
	}
}

func TestFilesystemManagerDefaultsNameFromDirectory(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "brief", `---
description: Keep answers short.
---

Be concise.
`)

	manager := NewManagerFromConfig(config.AppConfig{
		Skills: config.SkillsConfig{Dir: root},
	}, zap.NewNop())
	skill, err := manager.Load(context.Background(), "brief")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if skill.Name != "brief" {
		t.Fatalf("expected fallback name, got %q", skill.Name)
	}
}

func TestFilesystemManagerLoadsCRLFFrontMatter(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "brief", "---\r\nname: brief\r\ndescription: Keep answers short.\r\n---\r\n\r\nBe concise.\r\n")

	manager := NewManagerFromConfig(config.AppConfig{
		Skills: config.SkillsConfig{Dir: root},
	}, zap.NewNop())

	skill, err := manager.Load(context.Background(), "brief")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if skill.Description != "Keep answers short." {
		t.Fatalf("unexpected description: %q", skill.Description)
	}
	if skill.Instructions != "Be concise." {
		t.Fatalf("unexpected instructions: %q", skill.Instructions)
	}
}

func TestFilesystemManagerLoadsFrontMatterWithOpeningWhitespace(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "brief", "--- \t\nname: brief\ndescription: Keep answers short.\n---\n\nBe concise.\n")

	manager := NewManagerFromConfig(config.AppConfig{
		Skills: config.SkillsConfig{Dir: root},
	}, zap.NewNop())

	skill, err := manager.Load(context.Background(), "brief")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if skill.Description != "Keep answers short." {
		t.Fatalf("unexpected description: %q", skill.Description)
	}
}

func TestFilesystemManagerSkipsInvalidSkills(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "bad", `---
name: bad
---

Missing description.
`)

	manager := NewManagerFromConfig(config.AppConfig{
		Skills: config.SkillsConfig{Dir: root},
	}, zap.NewNop())
	specs, err := manager.Specs(context.Background())
	if err != nil {
		t.Fatalf("Specs() error = %v", err)
	}
	if len(specs) != 0 {
		t.Fatalf("expected invalid skill skipped, got %#v", specs)
	}
}

func TestFilesystemManagerSpecsRescansChanges(t *testing.T) {
	root := t.TempDir()
	path := writeSkill(t, root, "brief", `---
description: Keep answers short.
---

Be concise.
`)

	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(root, old, old); err != nil {
		t.Fatal(err)
	}

	manager := NewManagerFromConfig(config.AppConfig{
		Skills: config.SkillsConfig{Dir: root},
	}, zap.NewNop())

	specs, err := manager.Specs(context.Background())
	if err != nil {
		t.Fatalf("Specs() error = %v", err)
	}
	if len(specs) != 1 || specs[0].Name != "brief" {
		t.Fatalf("unexpected specs: %#v", specs)
	}

	if err := os.WriteFile(path, []byte(`---
name: changed
description: Changed description.
---

Changed instructions.
`), 0o600); err != nil {
		t.Fatal(err)
	}

	specs, err = manager.Specs(context.Background())
	if err != nil {
		t.Fatalf("Specs() after file change error = %v", err)
	}
	if len(specs) != 1 || specs[0].Name != "brief" || specs[0].Description != "Changed description." {
		t.Fatalf("Specs() did not rescan: %#v", specs)
	}

	skill, err := manager.Load(context.Background(), "brief")
	if err != nil {
		t.Fatalf("Load() after file change error = %v", err)
	}
	if skill.Instructions != "Changed instructions." {
		t.Fatalf("Load() = %q, want rescanned instructions", skill.Instructions)
	}
}

func TestFilesystemManagerSpecsSeesRemovals(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "keep", "---\ndescription: Stays.\n---\n\nBody.\n")
	gone := writeSkill(t, root, "gone", "---\ndescription: Goes.\n---\n\nBody.\n")

	manager := NewManagerFromConfig(config.AppConfig{
		Skills: config.SkillsConfig{Dir: root},
	}, zap.NewNop())

	specs, err := manager.Specs(context.Background())
	if err != nil {
		t.Fatalf("Specs() error = %v", err)
	}
	if len(specs) != 2 {
		t.Fatalf("unexpected specs: %#v", specs)
	}

	if err := os.RemoveAll(filepath.Dir(gone)); err != nil {
		t.Fatal(err)
	}
	specs, err = manager.Specs(context.Background())
	if err != nil {
		t.Fatalf("Specs() after removal error = %v", err)
	}
	if len(specs) != 1 || specs[0].Name != "keep" {
		t.Fatalf("Specs() kept a removed skill: %#v", specs)
	}
}

func TestFilesystemManagerUnknownSkill(t *testing.T) {
	manager := NewManagerFromConfig(config.AppConfig{
		Skills: config.SkillsConfig{Dir: t.TempDir()},
	}, zap.NewNop())
	if _, err := manager.Load(context.Background(), "missing"); err == nil {
		t.Fatal("expected unknown skill error")
	}
}

func writeSkill(t *testing.T, root string, name string, content string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFilesystemManagerDirectoryOrdering(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"zeta", "alpha", "middle"} {
		writeSkill(t, root, name, "---\nname: conflicting\ndescription: "+name+"\n---\nBody")
	}
	writeSkill(t, root, "beta", "---\ninvalid: [\n---\nBody")
	if err := os.Mkdir(filepath.Join(root, "empty"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "file"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	manager := NewManagerFromConfig(config.AppConfig{Skills: config.SkillsConfig{Dir: root}}, zap.NewNop())
	specs, err := manager.Specs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(specs) != 3 {
		t.Fatalf("unexpected specs: %+v", specs)
	}
	for i, want := range []string{"alpha", "middle", "zeta"} {
		if specs[i].Name != want || specs[i].Description != want {
			t.Fatalf("spec[%d]=%+v, want %s", i, specs[i], want)
		}
	}
}

func TestFilesystemManagerExplicitInvalidateRefreshesBody(t *testing.T) {
	root := t.TempDir()
	path := writeSkill(t, root, "brief", "---\ndescription: Brief.\n---\nOld body.")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(root, old, old); err != nil {
		t.Fatal(err)
	}
	manager := NewManagerFromConfig(config.AppConfig{Skills: config.SkillsConfig{Dir: root}}, zap.NewNop()).(*FilesystemManager)
	if _, err := manager.Load(context.Background(), "brief"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("---\ndescription: Brief.\n---\nNew body."), 0600); err != nil {
		t.Fatal(err)
	}
	manager.Invalidate()
	skill, err := manager.Load(context.Background(), "brief")
	if err != nil || skill.Instructions != "New body." {
		t.Fatalf("stale skill after invalidation: %+v %v", skill, err)
	}
}
