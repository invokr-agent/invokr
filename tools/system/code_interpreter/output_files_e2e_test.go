package code_interpreter

import (
	"context"
	"github.com/invokr-agent/invokr/tools/core/sandbox"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/constants"
	"github.com/invokr-agent/invokr/tools/system/workspace"
)

// End-to-end proof that output_files reaches the workspace: the unit tests
// exercise copyOutputs directly, which would still pass if the two backends
// disagreed about where "artifacts/" is — as they once did, the container
// mounting it at /artifacts while cwd was /work, so every relative write the
// tool description asks for was silently dropped.
func TestEndToEndOutputFilesRealPython(t *testing.T) {
	if !sandbox.Available() {
		t.Skip("namespace sandbox unavailable")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "in.csv"), []byte("a,b\n1,2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root})
	svc := NewService(config.CodeInterpreterConfig{SessionDir: t.TempDir()}, wsp, nil)
	if err := validateBackend(svc.cfg); err != nil {
		t.Skipf("no usable code_interpreter backend here: %v", err)
	}
	// A docker CLI without a reachable daemon selects the container backend
	// and then fails every run; that is this machine's gap, not the tool's.
	if svc.cfg.Backend == constants.CodeInterpreterBackendContainer {
		if err := exec.Command(svc.cfg.ContainerEngine, "info").Run(); err != nil {
			t.Skipf("container engine %q not usable here: %v", svc.cfg.ContainerEngine, err)
		}
	}
	code := "import json,os\n" +
		"src=open('in.csv').read()\n" +
		"open(os.path.join(os.environ['INVOKR_CI_ARTIFACT_DIR'],'organization.json'),'w').write(json.dumps({'rows':src.count(chr(10))}))\n" +
		"print('wrote')\n"
	p, err := svc.Prepare(Args{Code: code, InputFiles: []string{"in.csv"}, OutputFiles: []string{"organization.json"}})
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	out, err := svc.Run(context.Background(), p)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	got, readErr := os.ReadFile(filepath.Join(root, "organization.json"))
	if readErr != nil {
		t.Fatalf("workspace file missing: %v\nresult: %s", readErr, out)
	}
	if !strings.Contains(string(got), "rows") {
		t.Fatalf("content = %q", got)
	}
	t.Logf("OK result: %s", out)
}
