package agentprompt

import (
	"github.com/invokr-ai/invokr/support/strutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"unicode"

	"github.com/invokr-ai/invokr/constants"
)

// Environment is the harness's own view of where it is running. It is a
// snapshot taken once per session: cwd, OS and date do not move, and a branch
// change mid-session is not worth a file read on every turn.
type Environment struct {
	Cwd, OS, Arch string
	Date          string // YYYY-MM-DD
	Branch        string // empty when the workspace is not a git checkout
}

// DetectEnvironment snapshots root. Every failure is silent: a missing or
// unreadable .git leaves Branch empty, and an empty root yields a zero
// Environment, which renders nothing.
func DetectEnvironment(root string) Environment {
	root = strings.TrimSpace(root)
	if root == "" {
		return Environment{}
	}
	return Environment{Cwd: sanitizeEnvValue(root), OS: runtime.GOOS, Arch: runtime.GOARCH,
		Date: time.Now().Format("2006-01-02"), Branch: sanitizeEnvValue(gitBranch(root))}
}

// gitBranch reads <root>/.git/HEAD directly — no exec, no dependency. When
// .git is a file (worktree or submodule) it holds "gitdir: <path>" and one
// level of indirection is followed. A "ref: refs/heads/x" line yields "x"; a
// detached HEAD yields the short SHA.
func gitBranch(root string) string {
	gitDir := filepath.Join(root, ".git")
	// A .git *file* (worktree or submodule) holds "gitdir: <path>"; reading a
	// .git directory fails and leaves gitDir as-is. All errors stay silent:
	// a missing HEAD below yields the empty branch.
	if data, err := os.ReadFile(gitDir); err == nil {
		target := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir:"))
		if !filepath.IsAbs(target) {
			target = filepath.Join(root, target)
		}
		gitDir = target
	}
	head, _ := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	line := strings.TrimSpace(string(head))
	if ref, ok := strings.CutPrefix(line, "ref: "); ok {
		return strings.TrimPrefix(strings.TrimSpace(ref), "refs/heads/")
	}
	// Detached HEAD: the short SHA.
	return line[:min(len(line), 12)]
}

// sanitizeEnvValue guards the unfenced environment section: branch names and
// path components come from a cloned repository, so nothing reaches the prompt
// verbatim. Non-printable runes and angle brackets are dropped (no fence or
// tag can be forged), whitespace runs collapse, and the value is capped.
func sanitizeEnvValue(value string) string {
	var b strings.Builder
	for _, r := range value {
		switch {
		case r == '<' || r == '>':
		case unicode.IsSpace(r):
			b.WriteRune(' ')
		case unicode.IsPrint(r):
			b.WriteRune(r)
		}
	}
	cleaned := strings.Join(strings.Fields(b.String()), " ")
	return strutil.TruncateUTF8(cleaned, constants.EnvironmentValueCharLimit)
}
