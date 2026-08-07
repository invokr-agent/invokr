// Package create_tool lets the agent author a new toolbox tool at runtime:
// it writes tool.json/main.py/requirements.txt under the toolbox directory
// and triggers a reload, so the tool becomes callable in the same session.
// Safe to add only once the toolbox sandbox (M2) and Python supply-chain
// gates (M3) exist, since whatever the agent writes here runs through them.
package create_tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/system/toolbox"
)

var toolNameRe = regexp.MustCompile(`^[a-z][a-z0-9_]{1,63}$`)

var createToolCommitMu sync.Mutex

const createToolExample = `{"name":"create_tool","arguments":{"name":"word_count","description":"Count words in text","example":"{\"name\":\"word_count\",\"arguments\":{\"text\":\"hi there\"}}","parameters":{"type":"object","properties":{"text":{"type":"string"}},"required":["text"]},"python_source":"import json,sys\ncall=json.loads(sys.stdin.read())\nargs=call.get(\"arguments\") or {}\nprint(json.dumps({\"result\":str(len(args[\"text\"].split()))}))","policy":{"tiers":["executes_code"],"approval":"ask"}}}`

type createToolArgs struct {
	Name         string                `json:"name" desc:"Lowercase slug, e.g. word_count."`
	Description  string                `json:"description"`
	Example      string                `json:"example"`
	Parameters   map[string]any        `json:"parameters" desc:"JSON Schema for the new tool's arguments."`
	PythonSource string                `json:"python_source" desc:"Contents of main.py. Stdin is a JSON call envelope; read user values from call['arguments']."`
	Requirements []string              `json:"requirements,omitempty" desc:"Pinned requirements (name==version), no VCS/URL/path specs."`
	Capabilities core.ToolCapabilities `json:"capabilities" schema:"object"`
	Policy       core.ToolPolicy       `json:"policy" schema:"object"`
	// AlwaysVisible defaults to true when omitted: a tool the agent was just
	// asked to author is one it is about to call, and a deferred tool drops out
	// of the prompt schema immediately after creation — which sends the model
	// back to code_interpreter instead of the tool it just built. Pass false
	// explicitly for a tool meant to sit behind tool_search.
	AlwaysVisible *bool `json:"always_visible,omitempty" desc:"Include the full schema in every prompt. Default true; pass false to hide the tool behind tool_search."`
	Overwrite     bool  `json:"overwrite,omitempty" desc:"Replace an existing tool with the same name. Default false."`
}

func (a createToolArgs) alwaysVisible() bool {
	return a.AlwaysVisible == nil || *a.AlwaysVisible
}

// toolSpecFile mirrors the JSON shape toolbox's loader expects from
// tool.json; kept in sync with toolbox's (unexported) spec type by field tag.
type toolSpecFile struct {
	Name         string                `json:"name"`
	Description  string                `json:"description"`
	Example      string                `json:"example"`
	Parameters   map[string]any        `json:"parameters"`
	Policy       core.ToolPolicy       `json:"policy"`
	Capabilities core.ToolCapabilities `json:"capabilities"`
	// Written unconditionally (no omitempty) so the manifest states the
	// visibility it was created with rather than leaning on a loader default.
	AlwaysVisible bool `json:"always_visible"`
}

func RegisterCreateToolTool(registrar core.NativeRegistrar, deps core.CallerDeps) {
	core.Register(registrar, core.Tool[createToolArgs]{
		Spec: core.ToolSpec{
			Name: "create_tool", Description: "Author a new Python toolbox tool: writes tool.json/main.py (and requirements.txt) under the toolbox directory and makes it callable immediately.",
			Example: createToolExample,
			// capabilities is derivable but deliberately optional.
			Parameters: core.SchemaFor[createToolArgs](core.WithRequired("name", "description", "example", "parameters", "python_source")),
			Metadata:   core.NativeMetadata("create_tool", []string{"write_workspace", "executes_code"}, constants.ApprovalPolicyAsk, core.ToolCapabilities{WritesWorkspace: true, ExecutesCode: true}),
		},
		Prepare: func(_ context.Context, args createToolArgs) (createToolArgs, error) {
			args.Policy = defaultPolicy(args.Policy)
			return args, validateCreateToolArgs(registrar, deps, args)
		},
		Approve: func(_ context.Context, args createToolArgs) core.ApprovalDecision {
			return core.Ask(approval.Request{
				Tool: "create_tool", Reason: "create_tool writes and will execute agent-authored code",
				Tier:    strings.Join(args.Policy.Tiers, ", "),
				Summary: fmt.Sprintf("Create toolbox tool %q", args.Name),
				Details: fmt.Sprintf("Requirements: %s\n---\n%s", strings.Join(args.Requirements, ", "), args.PythonSource),
			})
		},
		Approver: deps.Approver,
		Execute: func(ctx context.Context, args createToolArgs) (string, error) {
			return executeCreateTool(ctx, deps, args)
		},
	})
}

// defaultPolicy fills the policy fields a model routinely omits. The bench
// showed create_tool calls rejected only for a missing policy block; the safe
// default (read-only tier, ask before running) is what the model would have
// been told to write anyway.
func defaultPolicy(p core.ToolPolicy) core.ToolPolicy {
	if len(p.Tiers) == 0 {
		p.Tiers = []string{constants.CodeInterpreterTierReadWorkspace}
	}
	if strings.TrimSpace(p.Approval) == "" {
		p.Approval = constants.ApprovalPolicyAsk
	}
	return p
}

func validateCreateToolArgs(registrar core.NativeRegistrar, deps core.CallerDeps, args createToolArgs) error {
	if !toolNameRe.MatchString(args.Name) {
		return fmt.Errorf("name %q must match %s", args.Name, toolNameRe.String())
	}
	// Only a genuine native (Go-implemented) tool is a hard collision. A name
	// already loaded from the toolbox itself (a bundled default, or an
	// earlier create_tool call) is exactly what overwrite:true exists to
	// replace, so it is left to the on-disk overwrite check below.
	if registrar != nil {
		if src := registrar.NativeSource(args.Name); src != "" && src != "toolbox" {
			return fmt.Errorf("name %q collides with an existing native tool", args.Name)
		}
	}
	if err := toolbox.ValidateSpecFields(args.Name, args.Description, args.Example, args.Policy); err != nil {
		return fmt.Errorf("invalid tool spec: %w", err)
	}
	if strings.TrimSpace(args.PythonSource) == "" {
		return fmt.Errorf("python_source is required")
	}
	if len(args.Requirements) > 0 {
		reqData := []byte(strings.Join(args.Requirements, "\n"))
		if err := toolbox.ValidateRequirements(reqData, deps.App.Toolbox.PackageAllow, deps.App.Toolbox.PackageDeny); err != nil {
			return fmt.Errorf("invalid requirements: %w", err)
		}
	}
	return nil
}

func executeCreateTool(ctx context.Context, deps core.CallerDeps, args createToolArgs) (string, error) {
	cfg := deps.App.Toolbox.WithDefaults(config.InvokrHomeDir())
	toolDir := filepath.Join(cfg.Dir, args.Name)
	if err := os.MkdirAll(cfg.Dir, 0700); err != nil {
		return "", fmt.Errorf("create toolbox dir: %w", err)
	}
	stagingDir, err := os.MkdirTemp(cfg.Dir, "."+args.Name+"-staging-")
	if err != nil {
		return "", fmt.Errorf("create tool staging dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(stagingDir) }()

	specJSON, err := json.MarshalIndent(toolSpecFile{
		Name: args.Name, Description: args.Description, Example: args.Example,
		Parameters: args.Parameters, Policy: args.Policy, Capabilities: args.Capabilities, AlwaysVisible: args.alwaysVisible(),
	}, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal tool spec: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stagingDir, "tool.json"), specJSON, 0600); err != nil {
		return "", fmt.Errorf("write tool.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(stagingDir, "main.py"), []byte(args.PythonSource), 0600); err != nil {
		return "", fmt.Errorf("write main.py: %w", err)
	}
	if len(args.Requirements) > 0 {
		reqText := strings.Join(args.Requirements, "\n") + "\n"
		if err := os.WriteFile(filepath.Join(stagingDir, "requirements.txt"), []byte(reqText), 0600); err != nil {
			return "", fmt.Errorf("write requirements.txt: %w", err)
		}
	}
	if err := toolbox.ValidateToolDir(stagingDir, cfg); err != nil {
		return "", fmt.Errorf("validate staged tool: %w", err)
	}
	if err := installStagedTool(stagingDir, toolDir, args.Overwrite); err != nil {
		return "", err
	}

	if deps.Dispatcher != nil {
		deps.Dispatcher.Dispatch(ctx, events.Event{Type: events.EventToolboxRefreshRequested})
	}
	return core.MarshalResult("create_tool", struct {
		Created bool   `json:"created"`
		Name    string `json:"name"`
		Dir     string `json:"dir"`
	}{Created: true, Name: args.Name, Dir: toolDir})
}

func installStagedTool(stagingDir, toolDir string, overwrite bool) error {
	createToolCommitMu.Lock()
	defer createToolCommitMu.Unlock()
	_, statErr := os.Stat(toolDir)
	if statErr != nil && !os.IsNotExist(statErr) {
		return fmt.Errorf("stat existing tool: %w", statErr)
	}
	if statErr == nil && !overwrite {
		return fmt.Errorf("tool %q already exists; pass overwrite:true to replace it", filepath.Base(toolDir))
	}
	if os.IsNotExist(statErr) {
		return renameStagedTool(stagingDir, toolDir)
	}
	backupDir, err := uniqueSiblingDir(toolDir, "backup")
	if err != nil {
		return err
	}
	if err := os.Rename(toolDir, backupDir); err != nil {
		return fmt.Errorf("backup existing tool: %w", err)
	}
	if err := renameStagedTool(stagingDir, toolDir); err != nil {
		if rollbackErr := os.Rename(backupDir, toolDir); rollbackErr != nil {
			return fmt.Errorf("%w; restore previous tool: %v", err, rollbackErr)
		}
		return err
	}
	if err := os.RemoveAll(backupDir); err != nil {
		return fmt.Errorf("remove tool backup: %w", err)
	}
	return nil
}

func uniqueSiblingDir(path, suffix string) (string, error) {
	dir, err := os.MkdirTemp(filepath.Dir(path), "."+filepath.Base(path)+"-"+suffix+"-")
	if err != nil {
		return "", fmt.Errorf("reserve tool %s path: %w", suffix, err)
	}
	if err := os.Remove(dir); err != nil {
		return "", fmt.Errorf("prepare tool %s path: %w", suffix, err)
	}
	return dir, nil
}

func renameStagedTool(stagingDir, toolDir string) error {
	if err := os.Rename(stagingDir, toolDir); err != nil {
		return fmt.Errorf("install staged tool: %w", err)
	}
	return nil
}
