//go:build prototype

package workspace

// Investigation only: this deliberately demonstrates why replacing os.Open
// with a broad os.Root is not yet a production policy boundary. Run with:
// GOTOOLCHAIN=go1.26.5 go test -tags prototype ./tools/system/workspace -run RootedReadPrototype -v

import (
	"fmt"
	"go/version"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/invokr-ai/invokr/config"
)

func prototypeRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	v := runtime.Version()
	if version.Compare(v, "go1.26.5") < 0 || (version.Compare(v, "go1.27") >= 0 && version.Compare(v, "go1.27rc2") < 0) {
		t.Fatal("rooted-read experiment requires Go >=1.26.5 or >=1.27rc2 (GO-2026-4970)")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	return root
}

func prototypeWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
}

func prototypeLink(t *testing.T, target, path string) {
	t.Helper()
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
}

// Separate resolution from use so substitutions are deterministic, not a
// probabilistic race that can pass merely because the attacker lost scheduling.
func prototypeRead(t *testing.T, policy Policy, input string, beforeOpen func()) ([]byte, error) {
	t.Helper()
	abs, err := policy.Resolve(input)
	if err != nil {
		return nil, err
	}
	if !policy.AllowsHiddenPath(policy.DisplayPath(abs)) {
		return nil, fmt.Errorf("hidden path: %s", input)
	}
	for _, base := range policy.Roots() {
		if !underRoot(abs, base) {
			continue
		}
		root := prototypeRoot(t, base)
		rel, err := filepath.Rel(base, abs)
		if err != nil {
			return nil, err
		}
		if beforeOpen != nil {
			beforeOpen()
		}
		return root.ReadFile(rel)
	}
	return nil, fmt.Errorf("no allowed root: %s", input)
}

func TestRootedReadPrototypeStableContracts(t *testing.T) {
	root, extra := t.TempDir(), t.TempDir()
	const body = "first\r\nsecond\r\n"
	prototypeWrite(t, filepath.Join(root, "dir/file.txt"), body)
	prototypeWrite(t, filepath.Join(root, "app/file.txt"), body)
	prototypeWrite(t, filepath.Join(root, ".gitignore"), body)
	prototypeWrite(t, filepath.Join(extra, "file.txt"), body)
	prototypeLink(t, filepath.Join(root, "dir"), filepath.Join(root, "absolute"))
	prototypeLink(t, extra, filepath.Join(root, "cross-root"))
	policy := NewWorkspacePolicy(config.WorkspaceConfig{Root: root, ReadonlyRoots: []string{extra}})
	for _, input := range []string{"dir/file.txt", "absolute/file.txt", "cross-root/file.txt", "/app/file.txt", ".gitignore"} {
		t.Run(input, func(t *testing.T) {
			got, err := prototypeRead(t, policy, input, nil)
			if err != nil || string(got) != body {
				t.Fatalf("read = %q, %v", got, err)
			}
		})
	}
	window, err := ReadLineWindow(filepath.Join(root, "dir/file.txt"), 2, 1, 0)
	if err != nil || window.Text != "second\r\n" || window.Total != 2 {
		t.Fatalf("existing paging contract: %+v, %v", window, err)
	}
}

func TestRootedReadPrototypeSwaps(t *testing.T) {
	for _, hidden := range []bool{false, true} {
		t.Run(fmt.Sprintf("hidden_in_root=%v", hidden), func(t *testing.T) {
			root, target := t.TempDir(), t.TempDir()
			if hidden {
				target = filepath.Join(root, ".private")
			}
			prototypeWrite(t, filepath.Join(root, "dir/file.txt"), "approved")
			prototypeWrite(t, filepath.Join(target, "file.txt"), "not-approved")
			policy := NewWorkspacePolicy(config.WorkspaceConfig{Root: root})
			got, err := prototypeRead(t, policy, "dir/file.txt", func() {
				if err := os.Rename(filepath.Join(root, "dir"), filepath.Join(root, "old")); err != nil {
					t.Fatal(err)
				}
				link := target
				if hidden {
					link = ".private" // Relative link stays inside the rooted tree.
				}
				prototypeLink(t, link, filepath.Join(root, "dir"))
			})
			if !hidden {
				if err == nil {
					t.Fatalf("outside-root substitution admitted: %q", got)
				}
				return
			}
			// Passing this assertion records a failed rollout gate, not a fix.
			if err != nil || string(got) != "not-approved" {
				t.Fatalf("expected broad-root hidden-policy counterexample, got %q, %v", got, err)
			}
			t.Log("ROLLOUT BLOCKED: a rooted open still follows a replacement into a hidden subtree")
		})
	}
}

func TestRootedReadPrototypePinnedRootAndAdvisory(t *testing.T) {
	parent, outside := t.TempDir(), t.TempDir()
	path := filepath.Join(parent, "root")
	prototypeWrite(t, filepath.Join(path, "file.txt"), "original")
	root := prototypeRoot(t, path)
	if err := os.Rename(path, filepath.Join(parent, "moved")); err != nil {
		t.Fatal(err)
	}
	prototypeWrite(t, filepath.Join(path, "file.txt"), "replacement")
	got, err := root.ReadFile("file.txt")
	if err != nil || string(got) != "original" {
		t.Fatalf("pinned root followed replacement: %q, %v", got, err)
	}
	prototypeLink(t, outside, filepath.Join(parent, "moved", "escape"))
	file, err := root.Open("escape/")
	if err == nil {
		_ = file.Close()
		t.Fatal("GO-2026-4970 regression: trailing-slash symlink escaped root")
	}
}
