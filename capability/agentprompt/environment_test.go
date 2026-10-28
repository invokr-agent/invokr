package agentprompt

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/constants"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestDetectEnvironmentReadsBranchFromGitHead(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "ref: refs/heads/feat/thing\n")

	env := DetectEnvironment(root)
	if env.Branch != "feat/thing" {
		t.Fatalf("Branch = %q, want %q", env.Branch, "feat/thing")
	}
	if env.Cwd == "" || env.OS == "" || env.Arch == "" || env.Date == "" {
		t.Fatalf("expected populated environment, got %+v", env)
	}
}

func TestDetectEnvironmentFollowsGitdirFileForWorktrees(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "real-git", "HEAD"), "ref: refs/heads/wt\n")
	writeFile(t, filepath.Join(root, "checkout", ".git"), "gitdir: ../real-git\n")

	env := DetectEnvironment(filepath.Join(root, "checkout"))
	if env.Branch != "wt" {
		t.Fatalf("Branch = %q, want %q", env.Branch, "wt")
	}
}

func TestDetectEnvironmentShortensDetachedHead(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".git", "HEAD"), "0123456789abcdef0123456789abcdef01234567\n")

	env := DetectEnvironment(root)
	if env.Branch != "0123456789ab" {
		t.Fatalf("Branch = %q, want short sha", env.Branch)
	}
}

func TestDetectEnvironmentOmitsBranchWithoutGit(t *testing.T) {
	env := DetectEnvironment(t.TempDir())
	if env.Branch != "" {
		t.Fatalf("Branch = %q, want empty", env.Branch)
	}
	if env.Cwd == "" {
		t.Fatal("expected Cwd to be set")
	}
}

func TestDetectEnvironmentReturnsZeroForEmptyRoot(t *testing.T) {
	if env := DetectEnvironment("  "); env != (Environment{}) {
		t.Fatalf("expected zero environment, got %+v", env)
	}
}

func TestSanitizeEnvValueStripsControlAndAngleBrackets(t *testing.T) {
	got := sanitizeEnvValue("a<script>b\x00c\td")
	if strings.ContainsAny(got, "<>\x00\t") {
		t.Fatalf("sanitized value still contains forbidden runes: %q", got)
	}
	if got != "ascriptbc d" {
		t.Fatalf("sanitizeEnvValue = %q", got)
	}
}

func TestSanitizeEnvValueTruncatesOversizeBranchName(t *testing.T) {
	got := sanitizeEnvValue(strings.Repeat("x", 500))
	if len(got) != constants.EnvironmentValueCharLimit {
		t.Fatalf("len = %d, want %d", len(got), constants.EnvironmentValueCharLimit)
	}
}
