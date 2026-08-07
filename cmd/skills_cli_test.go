package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/config"
)

func newSkillsTestConfig(t *testing.T, hubs ...string) *config.AppConfig {
	t.Helper()
	home := t.TempDir()
	app := config.AppConfig{}
	app.Skills.Dir = filepath.Join(home, "skills")
	app.Skills.HubCacheDir = filepath.Join(home, "hubs")
	app.Skills.Hubs = hubs
	return &app
}

func newTestHubRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	skill := filepath.Join(dir, "skills", "alpha")
	if err := os.MkdirAll(skill, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	content := "---\nname: alpha\ndescription: first skill\n---\n\nDo the thing.\n"
	if err := os.WriteFile(filepath.Join(skill, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "add", "."},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "seed"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

func runSkills(t *testing.T, app *config.AppConfig, args ...string) (string, error) {
	t.Helper()
	cmd := NewSkillsCmd(app)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestSkillsListEmpty(t *testing.T) {
	app := newSkillsTestConfig(t)
	out, err := runSkills(t, app, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "no skills installed") {
		t.Fatalf("expected empty message, got %q", out)
	}
}

func TestSkillsInstallListRemove(t *testing.T) {
	repo := newTestHubRepo(t)
	app := newSkillsTestConfig(t, repo)

	out, err := runSkills(t, app, "install", "alpha")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if !strings.Contains(out, "skill alpha installed") {
		t.Fatalf("install output: %q", out)
	}
	if _, err := os.Stat(filepath.Join(app.Skills.Dir, "alpha", "SKILL.md")); err != nil {
		t.Fatalf("skill not on disk: %v", err)
	}

	out, err = runSkills(t, app, "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "first skill") {
		t.Fatalf("list output: %q", out)
	}

	out, err = runSkills(t, app, "remove", "alpha")
	if err != nil {
		t.Fatalf("remove: %v", err)
	}
	if !strings.Contains(out, "skill alpha removed") {
		t.Fatalf("remove output: %q", out)
	}
}

func TestSkillsSearch(t *testing.T) {
	repo := newTestHubRepo(t)
	app := newSkillsTestConfig(t, repo)
	out, err := runSkills(t, app, "search", "first")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(out, "alpha") {
		t.Fatalf("search output: %q", out)
	}
	out, err = runSkills(t, app, "search", "zzz-no-match")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if !strings.Contains(out, "no matching skills") {
		t.Fatalf("search no-match output: %q", out)
	}
}

func TestSkillsInstallAll(t *testing.T) {
	repo := newTestHubRepo(t)
	app := newSkillsTestConfig(t, repo)
	out, err := runSkills(t, app, "install", "--all")
	if err != nil {
		t.Fatalf("install --all: %v", err)
	}
	if !strings.Contains(out, "installed 1 skills: alpha") {
		t.Fatalf("install --all output: %q", out)
	}
}
