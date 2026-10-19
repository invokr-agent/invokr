package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/config"
)

type approverFunc func(context.Context, approval.Request) (approval.Decision, error)

func (f approverFunc) RequestApproval(ctx context.Context, req approval.Request) (approval.Decision, error) {
	return f(ctx, req)
}

func outsideCtx(t *testing.T, on bool, a approval.Approver) context.Context {
	t.Helper()
	p := approval.NewPolicy(approval.Manual, false)
	p.SetOutsideReads(on)
	ctx, done := p.Begin(context.Background(), approval.Request{Tool: "read"}, a)
	t.Cleanup(done)
	return ctx
}

func TestResolveReadOutsideTheRoots(t *testing.T) {
	root, other := t.TempDir(), t.TempDir()
	for _, path := range []string{filepath.Join(root, "in.txt"), filepath.Join(other, "out.txt"), filepath.Join(other, ".env")} {
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wsp := NewWorkspacePolicy(config.WorkspaceConfig{Root: root})
	out := filepath.Join(other, "out.txt")

	if _, outside, err := wsp.ResolveRead(outsideCtx(t, true, nil), "in.txt"); err != nil || outside {
		t.Fatalf("inside: outside=%v err=%v", outside, err)
	}
	_, _, err := wsp.ResolveRead(outsideCtx(t, false, nil), out)
	var outsideErr *OutsideError
	if !errors.As(err, &outsideErr) || !strings.Contains(err.Error(), "/reads ask") {
		t.Fatalf("switched off: err=%v, want an OutsideError naming /reads ask", err)
	}
	if abs, outside, err := wsp.ResolveRead(outsideCtx(t, true, nil), out); err != nil || !outside || abs != out {
		t.Fatalf("switched on: abs=%q outside=%v err=%v", abs, outside, err)
	}
	if _, _, err := wsp.ResolveRead(outsideCtx(t, true, nil), filepath.Join(other, ".env")); err == nil || !strings.Contains(err.Error(), "hidden") {
		t.Fatalf("hidden outside path: err=%v, want refusal", err)
	}
	if _, _, err := wsp.ResolveRead(outsideCtx(t, true, nil), config.DefaultConfigPath()); err == nil {
		t.Fatal("protected invokr config must stay unreadable")
	}
}

func TestAuthorizeOutsideReadsAsksOnceWithGrantScope(t *testing.T) {
	var asked []approval.Request
	a := approverFunc(func(_ context.Context, req approval.Request) (approval.Decision, error) {
		asked = append(asked, req)
		return approval.Decision{Reason: "no"}, nil
	})
	err := AuthorizeOutsideReads(outsideCtx(t, true, a), "/src/other", "/src/other/a.md", "/src/other/docs/b.md")
	var denied *approval.DeniedError
	if !errors.As(err, &denied) || len(asked) != 1 {
		t.Fatalf("err=%v prompts=%d, want one prompt and a DeniedError", err, len(asked))
	}
	if req := asked[0]; req.Tool != OutsideReadTool || !req.Outside || req.GrantPattern != "/src/other/*" || len(req.Paths) != 2 {
		t.Fatalf("request = %+v", req)
	}
}

func TestOutsideGrantScope(t *testing.T) {
	home, _ := os.UserHomeDir()
	for scope, want := range map[string]string{"/src/other": "/src/other/*", "/": "", home: ""} {
		if got := grantPattern(scope); got != want {
			t.Fatalf("grantPattern(%q) = %q, want %q", scope, got, want)
		}
	}
	if got := CommonDir([]string{"/a/b/c.md", "/a/b/d/e.md", "/a/x.md"}); got != "/a" {
		t.Fatalf("CommonDir = %q, want /a", got)
	}
}
