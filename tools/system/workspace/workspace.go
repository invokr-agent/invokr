package workspace

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/support/fsutil"
)

type Policy struct {
	Root             string
	AllowHiddenPaths bool
	roots, readonly  []string
	protected        []string
	unrestricted     bool
}

func (p Policy) Unrestricted() Policy {
	p.unrestricted = true
	p.AllowHiddenPaths = true
	p.readonly = nil
	return p
}
func (p Policy) Protect(paths ...string) Policy {
	p.protected = append(p.protected, paths...)
	return p
}

func (p Policy) ProtectedPaths() []string {
	paths := append([]string{config.InvokrHomeDir(), config.DefaultConfigPath()}, p.protected...)
	for _, root := range p.roots {
		paths = append(paths, filepath.Join(root, ".invokr"))
		for _, dir := range GitDirs(root) {
			paths = append(paths, filepath.Join(dir, "config"), filepath.Join(dir, "hooks"))
		}
		git := filepath.Join(root, ".git")
		if info, err := os.Stat(git); err == nil && info.IsDir() {
			paths = append(paths, filepath.Join(git, "hooks"), filepath.Join(git, "config"))
		} else {
			paths = append(paths, git)
		}
	}
	for i, path := range paths {
		if resolved, err := ResolveSymlinkPrefix(path); err == nil {
			paths[i] = resolved
		}
	}
	return paths
}

func (p Policy) IsProtected(path string) bool {
	return slices.ContainsFunc(p.ProtectedPaths(), func(root string) bool {
		if root == "" {
			return false
		}
		_, inside := fsutil.RelInside(cleanAbsPath(root), path)
		return inside
	})
}

// Overlaps compares canonical trees; an unresolved path cannot prove isolation.
func Overlaps(a, b string) bool {
	left, leftErr := ResolveSymlinkPrefix(a)
	right, rightErr := ResolveSymlinkPrefix(b)
	_, below := fsutil.RelInside(left, right)
	_, above := fsutil.RelInside(right, left)
	return leftErr != nil || rightErr != nil || below || above
}

func NewWorkspacePolicy(cfg config.WorkspaceConfig) Policy {
	roots := make([]string, 0, 1+len(cfg.ExtraRoots))
	if root := cleanAbsPath(cfg.Root); root != "" {
		roots = append(roots, root)
	}
	for _, extra := range cfg.ExtraRoots {
		if root := cleanAbsPath(extra); root != "" && !slices.Contains(roots, root) {
			roots = append(roots, root)
		}
	}
	var readonly []string
	for _, extra := range cfg.ReadonlyRoots {
		root := cleanAbsPath(extra)
		if root == "" || slices.Contains(readonly, root) {
			continue
		}
		readonly = append(readonly, root)
		if !slices.Contains(roots, root) {
			roots = append(roots, root)
		}
	}

	root := ""
	if len(roots) > 0 {
		root = roots[0]
	}

	return Policy{Root: root, AllowHiddenPaths: cfg.AllowHiddenPaths, roots: roots, readonly: readonly}
}

// ResolveDir resolves input and requires it to name an existing directory.
// label names the input in the error messages, since callers substitute a
// default (the workspace root) for an empty one.
func (p Policy) ResolveDir(input, label string) (string, error) {
	resolved, err := p.Resolve(input)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", fmt.Errorf("stat cwd %q: %w", label, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("cwd %q is not a directory", label)
	}
	return resolved, nil
}

func (p Policy) Resolve(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path is required")
	}

	var candidate string
	if filepath.IsAbs(path) {
		candidate = filepath.Clean(path)
	} else {
		if p.Root == "" {
			return "", fmt.Errorf("workspace root is not configured")
		}
		candidate = filepath.Join(p.Root, path)
	}
	candidate = cleanAbsPath(candidate)
	candidate, err := ResolveSymlinkPrefix(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve path %q: %w", path, err)
	}
	if p.unrestricted {
		return candidate, nil
	}

	if !p.allows(candidate) {
		if rebased, ok := p.rebaseOnRoot(path); ok {
			return rebased, nil
		}
		// The root is named: a model probing absolute paths was never told
		// where it actually was, and spent turns guessing.
		return "", fmt.Errorf("path %q is outside allowed workspace roots (workspace root is %q); use a path relative to it", path, p.Root)
	}
	return candidate, nil
}

// rebaseOnRoot answers an absolute path that is outside every root but names
// an existing file once joined onto the workspace root. A model that believes
// the workspace is mounted at /app asks for "/app/config.py" when it means
// "<root>/app/config.py"; refusing that cost a turn while the file was sitting
// right there (r10 L1/1). The file must already exist, so this can only ever
// reinterpret a path the workspace itself provides — anything else is still
// refused. A write to "/app/x" therefore lands on "<root>/app/x".
func (p Policy) rebaseOnRoot(path string) (string, bool) {
	candidate := cleanAbsPath(filepath.Join(p.Root, path))
	if candidate == p.Root {
		// "/" joins onto the root itself; it names the filesystem, not a file
		// the workspace happens to hold.
		return "", false
	}
	candidate, err := ResolveSymlinkPrefix(candidate)
	if err != nil || !p.allows(candidate) {
		return "", false
	}
	if _, err := os.Lstat(candidate); err != nil {
		return "", false
	}
	return candidate, true
}

// ResolveSymlinkPrefix canonicalizes paths, including not-yet-created leaves.
func ResolveSymlinkPrefix(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}
	current := path
	var suffix []string
	hops := 0
	for {
		target, symlink, err := readSymlink(current)
		if err != nil {
			return "", err
		}
		if symlink {
			hops++
			if hops > 40 {
				return "", fmt.Errorf("too many levels of symbolic links")
			}
			current = target
			continue
		}
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			return appendSuffix(resolved, suffix), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return cleanAbsPath(path), nil
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}

func readSymlink(path string) (string, bool, error) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return "", false, nil
	}
	target, err := os.Readlink(path)
	if err != nil {
		return "", false, err
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(path), target)
	}
	return cleanAbsPath(target), true, nil
}

func appendSuffix(path string, suffix []string) string {
	for i := len(suffix) - 1; i >= 0; i-- {
		path = filepath.Join(path, suffix[i])
	}
	return cleanAbsPath(path)
}

// ResolveWrite resolves a path a tool is about to modify: the same rules as
// Resolve, plus a refusal for anything under a read-only root.
func (p Policy) ResolveWrite(path string) (string, error) {
	resolved, err := p.Resolve(path)
	if err != nil {
		return "", err
	}
	if root, ok := p.ReadOnlyRoot(resolved); ok {
		// Name the writable root too: told only that "a" root is read-only,
		// a model concludes its whole workspace is and stops writing.
		return "", fmt.Errorf("path %q is under the read-only root %s; it can be read but not modified. The workspace root %s is writable — write there instead", path, root, p.Root)
	}
	return resolved, nil
}

// underRoot reports whether an already-cleaned absolute path is root itself or
// sits beneath it.
func underRoot(path, root string) bool {
	return path == root || strings.HasPrefix(path, root+string(os.PathSeparator))
}

// RelDisplayPath renders abs relative to the workspace root, in slash form, or
// fallback when there is no root or abs sits outside it. It is the path shape
// the write tools report and ask approval for.
func (p Policy) RelDisplayPath(abs, fallback string) string {
	if p.Root == "" {
		return fallback
	}
	return relInsideAny([]string{p.Root}, abs, fallback)
}

// relInsideAny renders abs relative to the first root containing it, in slash
// form, or fallback when none does. It is the one place the display shape is
// decided, for both the single-root and the all-roots view.
func relInsideAny(roots []string, abs, fallback string) string {
	for _, root := range roots {
		if rel, ok := fsutil.RelInside(root, abs); ok {
			return filepath.ToSlash(rel)
		}
	}
	return fallback
}

// ReadOnlyRoot names the read-only root containing path, if any.
func (p Policy) ReadOnlyRoot(path string) (string, bool) {
	path = cleanAbsPath(path)
	if i := slices.IndexFunc(p.readonly, func(root string) bool { return underRoot(path, root) }); i >= 0 {
		return p.readonly[i], true
	}
	return "", false
}

// ReadOnlyRoots returns the configured read-only roots.
func (p Policy) ReadOnlyRoots() []string { return slices.Clone(p.readonly) }

// WritableRoots returns the roots a tool may write to: Roots() minus the
// read-only ones.
func (p Policy) WritableRoots() []string {
	return slices.DeleteFunc(slices.Clone(p.roots), func(root string) bool { _, ok := p.ReadOnlyRoot(root); return ok })
}

func (p Policy) Roots() []string { return slices.Clone(p.roots) }

func (p Policy) Allows(path string) bool { return p.allows(cleanAbsPath(path)) }

// AllowsHiddenPath reports whether a dotfile path may be touched. A top-level
// or nested .gitignore is always allowed: models read it on turn one to learn
// the layout, and refusing it cost a turn in 10 of 125 recent bench segments.
func (p Policy) AllowsHiddenPath(path string) bool {
	if p.AllowHiddenPaths {
		return true
	}
	if filepath.Base(path) == ".gitignore" {
		return !PathHasHiddenComponent(filepath.Dir(path))
	}
	return !PathHasHiddenComponent(path)
}

func PathHasHiddenComponent(path string) bool {
	for part := range strings.SplitSeq(filepath.ToSlash(path), "/") {
		if part = strings.TrimSpace(part); part != "." && part != ".." && strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

func (p Policy) allows(path string) bool {
	return slices.ContainsFunc(p.roots, func(root string) bool { return underRoot(path, root) })
}

// DominantEOL reports the line ending content mostly uses, so a line-based
// rewrite can be spliced back in the file's own ending rather than the model's.
// A file is treated as CRLF when at least half its newlines are.
func DominantEOL(content string) string {
	crlf := strings.Count(content, "\r\n")
	if crlf > 0 && crlf*2 >= strings.Count(content, "\n") {
		return "\r\n"
	}
	return "\n"
}

func cleanAbsPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if abs, err := filepath.Abs(path); err == nil {
		return filepath.Clean(abs)
	}
	return filepath.Clean(path)
}

// MatchGlob reports whether path matches a slash-separated glob pattern in
// which "**" matches any number of path segments and each other segment is
// matched with filepath.Match. An empty pattern matches nothing.
func MatchGlob(pattern string, path string) bool {
	if pattern == "" {
		return false
	}
	return globPartsMatch(strings.Split(filepath.ToSlash(pattern), "/"), strings.Split(filepath.ToSlash(path), "/"))
}

func globPartsMatch(patternParts []string, pathParts []string) bool {
	if len(patternParts) == 0 {
		return len(pathParts) == 0
	}
	if patternParts[0] == "**" {
		if globPartsMatch(patternParts[1:], pathParts) {
			return true
		}
		return len(pathParts) > 0 && globPartsMatch(patternParts, pathParts[1:])
	}
	if len(pathParts) == 0 {
		return false
	}
	ok, err := filepath.Match(patternParts[0], pathParts[0])
	if err != nil || !ok {
		return false
	}
	return globPartsMatch(patternParts[1:], pathParts[1:])
}

// GitDirs names the git directories a linked worktree needs mounted. In a plain
// clone .git is a directory inside the workspace and there is nothing to add. In
// a worktree it is a file holding `gitdir: <path>` naming
// <main>/.git/worktrees/<name>, which sits outside the workspace along with the
// main .git it shares objects and refs with. A commit writes to both, so both
// are read-write.
func GitDirs(workspaceRoot string) []string {
	if workspaceRoot == "" {
		return nil
	}
	pointer := filepath.Join(workspaceRoot, ".git")
	info, err := os.Lstat(pointer)
	if err != nil || !info.Mode().IsRegular() {
		return nil
	}
	data, err := os.ReadFile(pointer)
	if err != nil {
		return nil
	}
	target, ok := strings.CutPrefix(strings.TrimSpace(string(data)), "gitdir:")
	if !ok || strings.TrimSpace(target) == "" {
		return nil
	}
	target = strings.TrimSpace(target)
	if !filepath.IsAbs(target) {
		target = filepath.Join(workspaceRoot, target)
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil || !gitDirPointsBack(workspaceRoot, target) {
		return nil
	}
	dirs := []string{target}
	// The shared repository is the .git two levels up from
	// <main>/.git/worktrees/<name>; anything else is a layout we don't know.
	if shared := filepath.Dir(filepath.Dir(target)); filepath.Base(shared) == ".git" && filepath.Base(filepath.Dir(target)) == "worktrees" {
		dirs = []string{shared, target} // Mount parent before child so the child stays visible.
	}
	return dirs
}

// A real linked worktree points back via <gitdir>/gitdir; an absorbed
// submodule via core.worktree in <gitdir>/config. Workspace-controlled .git
// text alone cannot authorize an external mount.
func gitDirPointsBack(workspaceRoot, target string) bool {
	root, err := filepath.EvalSymlinks(workspaceRoot)
	if err != nil {
		return false
	}
	if back, err := os.ReadFile(filepath.Join(target, "gitdir")); err == nil {
		return resolvesTo(target, strings.TrimSpace(string(back)), filepath.Join(root, ".git"))
	}
	// Git parses quoting and escapes in core.worktree.
	value, err := exec.Command("git", "config", "--null", "--file", filepath.Join(target, "config"), "--get", "core.worktree").Output()
	return err == nil && resolvesTo(target, strings.TrimSuffix(string(value), "\x00"), root)
}

func resolvesTo(base, path, want string) bool {
	if path == "" {
		return false
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(base, path)
	}
	resolved, err := filepath.EvalSymlinks(path)
	return err == nil && resolved == want
}
