package hub

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/config"
)

func skillMD(name, description string) string {
	return "---\nname: " + name + "\ndescription: " + description + "\n---\n\nDo the thing.\n"
}

// newHubRepo builds a real git repository holding skills, so clone/pull run
// against the filesystem instead of the network.
func newHubRepo(t *testing.T, skills map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range skills {
		path := filepath.Join(dir, "skills", name)
		if err := os.MkdirAll(path, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte(content), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "add", "."},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "seed", "--allow-empty"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

func newTestService(t *testing.T, hubs ...string) *Service {
	t.Helper()
	home := t.TempDir()
	app := config.AppConfig{}
	app.Skills.Dir = filepath.Join(home, "skills")
	app.Skills.HubCacheDir = filepath.Join(home, "hubs")
	app.Skills.Hubs = hubs
	return NewService(app)
}

func TestCloneURLShorthand(t *testing.T) {
	if got := cloneURL("invokr-ai/skills-hub"); got != "https://github.com/invokr-ai/skills-hub.git" {
		t.Fatalf("shorthand: got %q", got)
	}
	full := "https://example.com/x/y.git"
	if got := cloneURL(full); got != full {
		t.Fatalf("full URL changed: got %q", got)
	}
}

func TestSkillsScansHub(t *testing.T) {
	repo := newHubRepo(t, map[string]string{
		"alpha": skillMD("alpha", "first skill"),
		"bad":   "not a skill file",
	})
	svc := newTestService(t, repo)
	found, errs := svc.Skills(context.Background(), false)
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(found) != 1 || found[0].Name != "alpha" {
		t.Fatalf("expected only alpha, got %+v", found)
	}
}

func TestInstallFromHubAndRemove(t *testing.T) {
	repo := newHubRepo(t, map[string]string{"alpha": skillMD("alpha", "first skill")})
	svc := newTestService(t, repo)
	name, err := svc.Install(context.Background(), "alpha", "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if name != "alpha" {
		t.Fatalf("installed name: got %q", name)
	}
	if _, err := os.Stat(filepath.Join(svc.skillsDir, "alpha", "SKILL.md")); err != nil {
		t.Fatalf("installed file missing: %v", err)
	}
	if err := svc.Remove("alpha"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(filepath.Join(svc.skillsDir, "alpha")); !os.IsNotExist(err) {
		t.Fatalf("skill dir still present after remove")
	}
}

func TestInstallUnknownSkill(t *testing.T) {
	repo := newHubRepo(t, map[string]string{"alpha": skillMD("alpha", "first skill")})
	svc := newTestService(t, repo)
	if _, err := svc.Install(context.Background(), "nope", ""); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func TestInstallFromLocalDir(t *testing.T) {
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "SKILL.md"), []byte(skillMD("local-one", "local skill")), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	svc := newTestService(t)
	name, err := svc.Install(context.Background(), src, "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if name != "local-one" {
		t.Fatalf("installed name: got %q", name)
	}
}

func TestInstallAll(t *testing.T) {
	repo := newHubRepo(t, map[string]string{
		"alpha": skillMD("alpha", "first skill"),
		"beta":  skillMD("beta", "second skill"),
	})
	svc := newTestService(t, repo)
	names, errs := svc.InstallAll(context.Background())
	if len(errs) != 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(names) != 2 {
		t.Fatalf("expected 2 installed, got %v", names)
	}
}

func TestRemoveRejectsPathEscapes(t *testing.T) {
	svc := newTestService(t)
	for _, name := range []string{"", "..", "a/b", ".hidden"} {
		if err := svc.Remove(name); err == nil {
			t.Fatalf("expected error for %q", name)
		}
	}
}

func TestMissingGitError(t *testing.T) {
	repo := newHubRepo(t, map[string]string{"alpha": skillMD("alpha", "first skill")})
	svc := newTestService(t, repo)
	t.Setenv("PATH", t.TempDir())
	_, errs := svc.Skills(context.Background(), false)
	if len(errs) != 1 || !strings.Contains(errs[0].Error(), "require git") {
		t.Fatalf("expected require-git error, got %v", errs)
	}
}

func TestInstallURLRequiresName(t *testing.T) {
	svc := newTestService(t)
	if _, err := svc.Install(context.Background(), "https://example.com/SKILL.md", ""); err == nil ||
		!strings.Contains(err.Error(), "--name") {
		t.Fatalf("expected --name error, got %v", err)
	}
}
