package shell

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/tools/system/workspace"
)

// FuzzClassifyLine checks the one promise read-only makes: the line runs
// without asking, so it must not change anything. Without INVOKR_FUZZ_EXEC it
// only checks the classifier does not panic. With it, every line classified
// read-only is run by bash in a scratch repo and the tree is compared before
// and after. Run that only in a throwaway container (make fuzz-shell), since
// a classifier bug means the fuzzer runs a writing command for real.
func FuzzClassifyLine(f *testing.F) {
	for _, seed := range []string{
		"ls -la", "cat f | grep a | wc -l", "git status", "git diff HEAD", "git log -p",
		"find . -name '*.go' -print", "sort -u f", "head -n 3 f; tail f",
		"for x in a b; do echo $x; done", "test -f f && cat f", "echo $(ls)",
		"GIT_EXTERNAL_DIFF='touch x #' git diff", "git -c core.pager=x log",
		"x=1; echo $x", "printf -v y %s z; echo $y",
		"cat <<'EOF'\na\nEOF", "ls ${x:=y}", "read a < f; echo $a", "echo hi >&2",
		"find . -newer f", "git rev-parse --show-toplevel", "sort -k1 -t, f",
	} {
		f.Add(seed)
	}
	// go is left out: it writes caches and telemetry under HOME, which is noise here.
	allowed := []string{"cat", "echo", "find", "git", "grep", "head", "ls", "sort", "tail", "wc"}
	run := os.Getenv("INVOKR_FUZZ_EXEC") == "1"
	f.Fuzz(func(t *testing.T, line string) {
		root := t.TempDir()
		wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root})
		pol, err := classifyLine(line, allowSet(allowed...), allowed, wsp, root, false)
		if err != nil || pol.tier != shellTierReadonly || !run {
			return
		}
		seedRepo(t, root)
		before := snapshot(t, root)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "bash", "-c", line)
		cmd.Dir = root
		cmd.Env = []string{"PATH=/usr/local/go/bin:/usr/bin:/bin", "HOME=" + t.TempDir(), "TMPDIR=" + root}
		_ = cmd.Run()
		if after := snapshot(t, root); after != before {
			t.Fatalf("read-only line changed the workspace: %q\nbefore:\n%s\nafter:\n%s", line, before, after)
		}
	})
}

func seedRepo(t *testing.T, root string) {
	t.Helper()
	script := "git init -q && echo a > f && git add f && git -c user.email=x -c user.name=x commit -qm i && echo b > f"
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = root
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("seed repo: %v: %s", err, out)
	}
}

// snapshot lists every path with its size and mtime; git's own index refresh
// is excluded since read-only git commands legitimately rewrite it.
func snapshot(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if rel == ".git/index" || strings.HasSuffix(rel, ".lock") {
			return nil
		}
		if d.IsDir() { // an index refresh renames a lock file, touching the dir mtime
			b.WriteString(rel + "/\n")
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		fmt.Fprintf(&b, "%s %d %s\n", rel, info.Size(), info.ModTime())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
