package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/invokr-ai/invokr/capability/approval"
)

// OutsideReadTool names approvals for reads beyond the roots and the grants
// they create. read and file_search share it, so one "always" covers both.
const OutsideReadTool = "outside_read"

// ResolveRead resolves a path a tool is about to read. Inside the roots it is
// Resolve. Outside them it reports outside=true when outside reads are on; the
// caller must then get AuthorizeOutsideReads before touching the file. Hidden
// and protected paths stay refused there, approval or not. The switch is the
// session's (approval.Policy), so /reads applies to the next call.
func (p Policy) ResolveRead(ctx context.Context, path string) (abs string, outside bool, err error) {
	abs, err = p.Resolve(path)
	var outsideErr *OutsideError
	if err == nil || !errors.As(err, &outsideErr) {
		return abs, false, err
	}
	if !approval.OutsideReadsAllowed(ctx) {
		// Say why, so a model does not report the file as missing.
		return "", false, fmt.Errorf("%w; reading outside the workspace is off (the user can enable it with /reads ask)", err)
	}
	abs = outsideErr.resolved
	if !p.AllowsHiddenPath(abs) || p.IsProtected(abs) {
		return "", false, fmt.Errorf("path %q is hidden or protected; it cannot be read from outside the workspace", path)
	}
	return abs, true, nil
}

// AuthorizeOutsideReads asks the user once for every path in paths. scope is
// the directory an "always" decision grants; / and the home directory are too
// broad, so those reads can only be approved once.
func AuthorizeOutsideReads(ctx context.Context, scope string, paths ...string) error {
	if len(paths) == 0 {
		return nil
	}
	req := approval.Request{
		Tool: OutsideReadTool, Summary: "Read outside the workspace", Details: strings.Join(paths, "\n"),
		Subject: strings.Join(paths, " "), Paths: paths, SubjectIsPathList: true,
		ReadOnly: true, Outside: true, GrantPattern: grantPattern(scope),
	}
	handled, err := approval.Authorize(ctx, nil, req, true)
	if !handled {
		return fmt.Errorf("reading outside the workspace needs approval and none is available: %s", strings.Join(paths, ", "))
	}
	return err
}

// ScopedTo is a read-only policy over dir alone, for searching an approved
// outside directory with the usual symlink, hidden and protected checks.
func (p Policy) ScopedTo(dir string) Policy {
	p.Root, p.roots, p.readonly = dir, []string{dir}, []string{dir}
	return p
}

// CommonDir is the deepest directory containing every path.
func CommonDir(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	dir := filepath.Dir(paths[0])
	for _, path := range paths[1:] {
		for !underRoot(path, dir) && filepath.Dir(dir) != dir {
			dir = filepath.Dir(dir)
		}
	}
	return dir
}

func grantPattern(scope string) string {
	home, _ := os.UserHomeDir()
	if scope = cleanAbsPath(scope); scope == "" || scope == filepath.Dir(scope) || scope == cleanAbsPath(home) {
		return ""
	}
	return scope + "/*"
}
