package create_tool

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFilesystemExampleExecutesWithCallEnvelope(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	var example struct {
		Arguments createToolArgs `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(createToolExample), &example); err != nil {
		t.Fatal(err)
	}
	args := example.Arguments
	if !strings.Contains(args.PythonSource, "def main():") || !strings.Contains(args.PythonSource, "\nprint(json.dumps(main()))") {
		t.Fatal("example must call main and print its result at script entrypoint")
	}
	var call struct {
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal([]byte(args.Example), &call); err != nil || len(call.Arguments) != 0 {
		t.Fatalf("example must invoke the created tool without arguments: %q: %v", args.Example, err)
	}
	if !args.Capabilities.ReadsWorkspace || args.Capabilities.WritesWorkspace || args.Capabilities.Network {
		t.Fatalf("unexpected capabilities: %+v", args.Capabilities)
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("workspace-canary"), 0600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(t.TempDir(), "main.py")
	if err := os.WriteFile(script, []byte(args.PythonSource), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(python, script)
	cmd.Dir = root
	cmd.Stdin = strings.NewReader(args.Example)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("example: %v: %s", err, out)
	}
	var result map[string]string
	if err := json.Unmarshal(out, &result); err != nil {
		t.Fatal(err)
	}
	if result["result"] != "workspace-canary" {
		t.Fatalf("result: %s", out)
	}
}
