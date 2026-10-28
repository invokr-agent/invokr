//go:build linux

package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/tools/core"
)

func TestPinPathsRejectsSymlinkedAncestor(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real", "git")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if file, err := openPinnedPath(filepath.Join(root, "link", "git"), true); err == nil {
		_ = file.Close()
		t.Fatal("symlinked ancestor accepted as writable bind source")
	}
}

func TestMixedPinsCloseHighDescriptorsBeforePayload(t *testing.T) {
	if !Available() {
		t.Skip("namespace sandbox unavailable")
	}
	root, files, writable := t.TempDir(), t.TempDir(), t.TempDir()
	plan := BuildPlan("/bin/sh", root, "", "", core.ToolCapabilities{}, 0, nil)
	plan.RW, plan.PinnedRW = []string{writable}, []string{writable}
	for index := range 8 {
		path := filepath.Join(files, fmt.Sprint(index))
		if err := os.WriteFile(path, []byte("PINNED_FILE"), 0o755); err != nil {
			t.Fatal(err)
		}
		plan.RO, plan.PinnedRO = append(plan.RO, path), append(plan.PinnedRO, path)
	}
	cmd, cleanup, err := Command(context.Background(), plan, root, []string{"-c",
		`for fd in 3 4 5 6 7 8 9 10 11; do test ! -e /proc/self/fd/$fd || exit 90; done; test -d "$2" && cat "$1"`,
		"probe", plan.PinnedRO[0], writable})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	out, err := cmd.CombinedOutput()
	if err != nil || strings.TrimSpace(string(out)) != "PINNED_FILE" {
		t.Fatalf("mixed-pin payload: %v: %s", err, out)
	}
}

func TestPinFileRejectsSymlinksAndNonRegularLeaves(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(real, "tool")
	if err := os.WriteFile(file, []byte("executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	for link, target := range map[string]string{"ancestor": real, "leaf": file} {
		if err := os.Symlink(target, filepath.Join(root, link)); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join(root, "ancestor", "tool"), filepath.Join(root, "leaf"), real} {
		if opened, err := openPinnedPath(path, false); err == nil {
			_ = opened.Close()
			t.Fatalf("accepted non-regular or symlinked file source %s", path)
		}
	}
	opened, err := openPinnedPath(file, false)
	if err != nil {
		t.Fatal(err)
	}
	_ = opened.Close()
}

func TestReopenPinnedRequiresExecutableFile(t *testing.T) {
	root := t.TempDir()
	plain := filepath.Join(root, "plain")
	tool := filepath.Join(root, "tool")
	if err := os.WriteFile(plain, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tool, []byte("executable"), 0o755); err != nil {
		t.Fatal(err)
	}
	if files, err := reopenPinned(Plan{PinnedRO: []string{plain}}); err == nil || !errors.Is(err, os.ErrInvalid) {
		closePinned(files)
		t.Fatalf("non-executable pin: err = %v, want os.ErrInvalid", err)
	}
	files, err := reopenPinned(Plan{PinnedRO: []string{tool}})
	if err != nil {
		t.Fatalf("executable pin rejected: %v", err)
	}
	closePinned(files)
}
