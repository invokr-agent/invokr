package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/tools/core"
)

// goLiterals are the Go-specific spellings that must never reach the model
// through a tool description, example or parameter doc: every one of them made
// the agent present itself as a Go-only tool in a Python project (#304).
//
// The table is deliberately checked against *every* registered native tool, so
// a tool added later cannot quietly reintroduce a Go-only example.
// "go version", "go env" and "gofmt (" catch the Go-only tier prose the shell
// description used to carry (#306) — bare allowlist members remain allowed.
var goLiterals = []string{".go", "main.go", "go.dev", "Go and", "Go release", "golang", "go version", "go env", "go list", "go vet", "gofmt ("}

func TestNativeToolSpecsAreLanguageNeutral(t *testing.T) {
	caller := NewCaller(core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: t.TempDir()},
			Workspace: config.WorkspaceConfig{Root: t.TempDir()},
		},
	}, zap.NewNop())

	specs := caller.Specs()
	if len(specs) == 0 {
		t.Fatal("no native tools registered")
	}
	for _, spec := range specs {
		// The lsp tools name the extensions of the language servers actually
		// installed on this machine, so ".go" there is a fact about the host,
		// not a hardcoded example. Everything else must be neutral.
		if spec.Metadata.Namespace == "lsp" {
			continue
		}
		params, err := json.Marshal(spec.Parameters)
		if err != nil {
			t.Fatalf("%s: marshal parameters: %v", spec.Name, err)
		}
		rendered := spec.Description + "\n" + spec.Example + "\n" + string(params)
		for _, literal := range goLiterals {
			if strings.Contains(rendered, literal) {
				t.Errorf("tool %s exposes Go-specific literal %q; use a language-neutral example (README.md, src/app.py, src/**):\n%s",
					spec.Name, literal, rendered)
			}
		}
	}
}
