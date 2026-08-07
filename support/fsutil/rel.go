package fsutil

import (
	"path/filepath"
	"strings"
)

// RelInside returns path expressed relative to root when path is inside root
// (root itself yields "."), and ok=false otherwise. Unlike a bare
// strings.HasPrefix(rel, "..") test, a sibling entry whose name merely starts
// with ".." (e.g. root/../..foo's sibling "..foo") is not rejected.
func RelInside(root, path string) (string, bool) {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", false
	}
	return rel, true
}
