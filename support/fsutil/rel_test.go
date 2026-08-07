package fsutil

import (
	"path/filepath"
	"testing"
)

func TestRelInside(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "work", "repo")
	cases := []struct {
		name    string
		path    string
		wantRel string
		wantOK  bool
	}{
		{"inside", filepath.Join(root, "pkg", "file.go"), filepath.Join("pkg", "file.go"), true},
		{"root itself", root, ".", true},
		{"parent", filepath.Dir(root), "", false},
		{"outside sibling", filepath.Join(filepath.Dir(root), "other"), "", false},
		{"dot-dot-named sibling inside", filepath.Join(root, "..foo"), "..foo", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rel, ok := RelInside(root, tc.path)
			if ok != tc.wantOK || rel != tc.wantRel {
				t.Fatalf("RelInside(%q, %q) = %q, %v; want %q, %v", root, tc.path, rel, ok, tc.wantRel, tc.wantOK)
			}
		})
	}
}
