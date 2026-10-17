package toolbox

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/support/testutil"
	"github.com/invokr-ai/invokr/tools/core"

	"go.uber.org/zap"
)

// recordingDispatcher captures the events a tool emits so the progress channel
// can be asserted on without a UI.
type recordingDispatcher struct {
	mu     sync.Mutex
	events []events.Event
}

func (d *recordingDispatcher) Dispatch(_ context.Context, event events.Event) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.events = append(d.events, event)
}

func (d *recordingDispatcher) progress() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []string
	for _, event := range d.events {
		if event.Type == events.EventToolProgress && event.Tool != nil {
			out = append(out, event.Tool.Progress)
		}
	}
	return out
}

// writePlugin writes a python plugin and merges extra keys into its manifest.
// Python over Node because the CI test image has no node; a fixture with no
// requirements.txt takes the bare-interpreter path, so there is no venv either.
func writePlugin(t *testing.T, root, name, source string, extra map[string]any) string {
	t.Helper()
	dir := testutil.MustWriteToolboxPlugin(t, root, name, name+" fixture", map[string]string{"main.py": source})
	specPath := filepath.Join(dir, "tool.json")
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatal(err)
	}
	spec := map[string]any{}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	for key, value := range extra {
		spec[key] = value
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(specPath, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func loadPluginHandler(t *testing.T, root, name string, dispatcher events.Dispatcher) core.NativeHandler {
	t.Helper()
	deps := core.CallerDeps{
		App: config.AppConfig{
			Toolbox:   config.ToolboxConfig{Dir: root, AllowUnsandboxed: true},
			Workspace: config.WorkspaceConfig{Root: sentinelRoot(t, root)},
		},
		Dispatcher: dispatcher,
	}
	for _, tool := range LoadToolboxTools(zap.NewNop(), deps) {
		if tool.Spec.Name == name {
			return tool.Handler
		}
	}
	t.Fatalf("plugin %q did not load", name)
	return nil
}

func loadPluginSpec(t *testing.T, root string, cfg config.ToolboxConfig) *executableTool {
	t.Helper()
	tool, err := loadToolboxTool(root, cfg.WithDefaults(t.TempDir()), zap.NewNop())
	if err != nil {
		t.Fatalf("load plugin: %v", err)
	}
	return tool
}

// sentinelPlugin marks that the process actually started. It writes into the
// declared workspace, the one path the sandbox bind-mounts read-write, so the
// host test can see the file afterwards.
const sentinelPlugin = `import json, os
open(os.environ["INVOKR_WORKING_DIR"] + "/sentinel", "w").write("spawned")
print(json.dumps({"result": "ran"}))`

// sentinelRoot is the workspace the sentinel plugins write into, one per
// toolbox root so parallel cases never share a sentinel.
var sentinelRoots sync.Map

func sentinelRoot(t *testing.T, toolboxRoot string) string {
	t.Helper()
	dir, _ := sentinelRoots.LoadOrStore(toolboxRoot, t.TempDir())
	return dir.(string)
}

// A schema violation must be caught in-core: the model gets a flagged error and
// the plugin process is never started, which the absent sentinel file proves.
func TestManifestParametersRejectInvalidArgsWithoutSpawning(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "schema_tool", sentinelPlugin, map[string]any{
		"capabilities": map[string]any{"writes_workspace": true, "executes_code": true},
		"parameters": map[string]any{
			"type":                 "object",
			"properties":           map[string]any{"path": map[string]any{"type": "string"}},
			"required":             []any{"path"},
			"additionalProperties": false,
		},
	})
	handler := loadPluginHandler(t, root, "schema_tool", nil)
	sentinel := filepath.Join(sentinelRoot(t, root), "sentinel")

	_, err := handler(context.Background(), &core.ToolCall{Name: "schema_tool", Arguments: []byte(`{"wrong":1}`)})
	var toolErr *core.ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("expected a core.ToolError, got %v", err)
	}
	if toolErr.Payload == "" {
		t.Fatal("validation error must carry a Payload so the model sees the message")
	}
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Fatalf("plugin was spawned despite invalid arguments: %v", statErr)
	}

	out, err := handler(context.Background(), &core.ToolCall{Name: "schema_tool", Arguments: []byte(`{"path":"a.txt"}`)})
	if err != nil {
		t.Fatalf("valid arguments failed: %v", err)
	}
	if out != "ran" {
		t.Fatalf("result = %q, want %q", out, "ran")
	}
	if _, statErr := os.Stat(sentinel); statErr != nil {
		t.Fatalf("valid call did not spawn the plugin: %v", statErr)
	}
}

// A plugin with no `parameters` block keeps today's behavior: any arguments go
// straight through to the process.
func TestNoParametersBlockAcceptsAnyArguments(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "legacy_tool", sentinelPlugin, map[string]any{"parameters": nil, "capabilities": map[string]any{"writes_workspace": true, "executes_code": true}})
	handler := loadPluginHandler(t, root, "legacy_tool", nil)
	sentinel := filepath.Join(sentinelRoot(t, root), "sentinel")
	if _, err := handler(context.Background(), &core.ToolCall{Name: "legacy_tool", Arguments: []byte(`{"anything":true}`)}); err != nil {
		t.Fatalf("schema-less plugin rejected arguments: %v", err)
	}
	if _, err := os.Stat(sentinel); err != nil {
		t.Fatalf("schema-less plugin did not run: %v", err)
	}
}

func TestManifestLimitsOverrideDefaults(t *testing.T) {
	root := t.TempDir()
	dir := writePlugin(t, root, "limited_tool", sentinelPlugin, map[string]any{
		"limits": map[string]any{"timeout_seconds": 2, "memory_mb": 512, "output_kb": 8},
	})
	tool := loadPluginSpec(t, dir, config.ToolboxConfig{})
	if tool.config.ExecTimeoutMS != 2000 {
		t.Fatalf("ExecTimeoutMS = %d, want 2000", tool.config.ExecTimeoutMS)
	}
	if tool.config.MemoryLimitBytes != 512*1024*1024 {
		t.Fatalf("MemoryLimitBytes = %d, want %d", tool.config.MemoryLimitBytes, 512*1024*1024)
	}
	if tool.config.MaxOutputBytes != 8*1024 {
		t.Fatalf("MaxOutputBytes = %d, want %d", tool.config.MaxOutputBytes, 8*1024)
	}

	plain := loadPluginSpec(t, writePlugin(t, t.TempDir(), "plain_tool", sentinelPlugin, nil), config.ToolboxConfig{})
	if plain.config.ExecTimeoutMS != constants.ToolboxDefaultExecTimeoutMS {
		t.Fatalf("plugin without limits changed the default timeout: %d", plain.config.ExecTimeoutMS)
	}
}

func TestManifestTimeoutLimitKillsLongRun(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "slow_tool", "import time\ntime.sleep(5)\nprint('{}')", map[string]any{
		"limits": map[string]any{"timeout_seconds": 2},
	})
	handler := loadPluginHandler(t, root, "slow_tool", nil)
	started := time.Now()
	if _, err := handler(context.Background(), &core.ToolCall{Name: "slow_tool", Arguments: []byte(`{}`)}); err == nil {
		t.Fatal("expected the 5s plugin to be killed at the declared 2s limit")
	}
	if elapsed := time.Since(started); elapsed > 4*time.Second {
		t.Fatalf("plugin ran for %v; the declared 2s limit was not enforced", elapsed)
	}
}

func TestManifestLimitAboveCapIsRejectedAtLoad(t *testing.T) {
	dir := writePlugin(t, t.TempDir(), "greedy_tool", sentinelPlugin, map[string]any{
		"limits": map[string]any{"timeout_seconds": 100},
	})
	cfg := config.ToolboxConfig{MaxLimitTimeoutMS: 10_000}.WithDefaults(t.TempDir())
	_, err := loadToolboxTool(dir, cfg, zap.NewNop())
	if err == nil || !strings.Contains(err.Error(), "exceeds cap") {
		t.Fatalf("error = %v, want a limits-exceed-cap diagnostic", err)
	}
}

func TestManifestNetworkAllowHostsGatesOnApprovalTier(t *testing.T) {
	dir := writePlugin(t, t.TempDir(), "net_tool", sentinelPlugin, map[string]any{
		"network": map[string]any{"allow_hosts": []string{"example.test"}},
	})
	tool := loadPluginSpec(t, dir, config.ToolboxConfig{})
	if !tool.Metadata.Capabilities.Network {
		t.Fatal("allow_hosts did not enable the network capability")
	}
	if !strings.Contains(strings.Join(tool.Metadata.Policy.Tiers, ","), constants.ShellTierNetwork) {
		t.Fatalf("policy tiers = %v, want the network tier", tool.Metadata.Policy.Tiers)
	}
	if tool.Metadata.Policy.Approval != constants.ApprovalPolicyAsk {
		t.Fatalf("approval = %q, want %q", tool.Metadata.Policy.Approval, constants.ApprovalPolicyAsk)
	}
	if len(tool.allowHosts) != 1 || tool.allowHosts[0] != "example.test" {
		t.Fatalf("allowHosts = %v", tool.allowHosts)
	}

	plain := loadPluginSpec(t, writePlugin(t, t.TempDir(), "plain_net", sentinelPlugin, nil), config.ToolboxConfig{})
	if plain.Metadata.Policy.Approval != constants.ApprovalPolicyAuto || plain.allowHosts != nil {
		t.Fatalf("plugin without a network block changed policy: %+v", plain.Metadata.Policy)
	}
}

// The unscoped route must not be the cheap one: a manifest asking for
// capabilities.network with no allow_hosts gets the *wider* posture (host
// resolver, nothing filtered), so it must take at least the tier and the
// approval prompt that the narrower allow_hosts route takes.
func TestManifestNetworkCapabilityAloneStillGatesOnApprovalTier(t *testing.T) {
	dir := writePlugin(t, t.TempDir(), "bare_net_tool", sentinelPlugin, map[string]any{
		"capabilities": map[string]any{"network": true},
	})
	tool := loadPluginSpec(t, dir, config.ToolboxConfig{})
	if !tool.Metadata.Capabilities.Network {
		t.Fatal("declared network capability was dropped")
	}
	if !slices.Contains(tool.Metadata.Policy.Tiers, constants.ShellTierNetwork) {
		t.Fatalf("policy tiers = %v, want %q — an unscoped namespace must not escape the tier", tool.Metadata.Policy.Tiers, constants.ShellTierNetwork)
	}
	if tool.Metadata.Policy.Approval == constants.ApprovalPolicyAuto {
		t.Fatal("approval stayed \"auto\": an unscoped network namespace would be handed out with no prompt")
	}
	if tool.allowHosts != nil {
		t.Fatalf("allowHosts = %v, want none — no allow_hosts block was declared", tool.allowHosts)
	}
}

// An explicit "never" is an operator decision about this tool, not a manifest
// widening its own reach, so the lift leaves it alone.
func TestManifestNetworkKeepsExplicitNeverApproval(t *testing.T) {
	dir := writePlugin(t, t.TempDir(), "never_net_tool", sentinelPlugin, map[string]any{
		"capabilities": map[string]any{"network": true},
		"policy":       map[string]any{"tiers": []string{"read_workspace"}, "approval": "never"},
	})
	if got := loadPluginSpec(t, dir, config.ToolboxConfig{}).Metadata.Policy.Approval; got != "never" {
		t.Fatalf("approval = %q, want %q", got, "never")
	}
}

func TestProgressLinesReachTheEventBusWithoutBreakingTheResult(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "progress_tool", `import json, sys
print("`+constants.ToolboxProgressPrefix+` step 1", file=sys.stderr)
print("noise on stderr", file=sys.stderr)
print("`+constants.ToolboxProgressPrefix+` step 2", file=sys.stderr)
print(json.dumps({"result": "done"}))`, nil)
	dispatcher := &recordingDispatcher{}
	handler := loadPluginHandler(t, root, "progress_tool", dispatcher)

	out, err := handler(context.Background(), &core.ToolCall{Name: "progress_tool", Arguments: []byte(`{}`)})
	if err != nil {
		t.Fatalf("progress plugin failed: %v", err)
	}
	if out != "done" {
		t.Fatalf("final payload = %q, want %q — the result parse must be unaffected", out, "done")
	}
	got := dispatcher.progress()
	if len(got) < 2 {
		t.Fatalf("progress events = %v, want at least 2", got)
	}
	if got[0] != "step 1" || got[1] != "step 2" {
		t.Fatalf("progress events = %v, want the two prefixed lines only", got)
	}
}

// cwdPlugin reports the directory the tool process actually started in, plus
// what a workspace-relative path resolves to from there.
const cwdPlugin = `import json, os
print(json.dumps({"result": os.getcwd() + "|" + open("data/note.txt").read()}))`

// A toolbox tool runs from the workspace root, not from its own plugin
// directory, so a relative path argument means what it means everywhere else.
func TestToolboxToolRunsFromWorkspaceRoot(t *testing.T) {
	root := t.TempDir()
	writePlugin(t, root, "cwd_tool", cwdPlugin, map[string]any{
		"capabilities": map[string]any{"reads_workspace": true, "executes_code": true},
	})
	workspace := sentinelRoot(t, root)
	if err := os.MkdirAll(filepath.Join(workspace, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "data", "note.txt"), []byte("relative-ok"), 0o600); err != nil {
		t.Fatal(err)
	}

	handler := loadPluginHandler(t, root, "cwd_tool", nil)
	out, err := handler(context.Background(), &core.ToolCall{Name: "cwd_tool", Arguments: []byte(`{}`)})
	if err != nil {
		t.Fatalf("cwd_tool failed: %v", err)
	}
	cwd, contents, _ := strings.Cut(out, "|")
	if resolved, evalErr := filepath.EvalSymlinks(workspace); evalErr == nil {
		workspace = resolved
	}
	if got, _ := filepath.EvalSymlinks(cwd); got != workspace && cwd != workspace {
		t.Fatalf("tool cwd = %q, want workspace %q", cwd, workspace)
	}
	if contents != "relative-ok" {
		t.Fatalf("relative path read = %q, want %q", contents, "relative-ok")
	}
}

// Without a workspace capability the sandbox never mounts the workspace, so the
// tool must keep starting in its own directory.
func TestToolboxToolWithoutWorkspaceCapabilityKeepsToolDir(t *testing.T) {
	tool := &executableTool{ToolSpec: core.ToolSpec{Name: "t"}, dir: "/plugins/t", workingDir: "/ws", Runtime: "python"}
	if got := tool.runDir(); got != "/plugins/t" {
		t.Fatalf("runDir = %q, want the tool dir", got)
	}
	tool.Metadata.Capabilities.ReadsWorkspace = true
	if got := tool.runDir(); got != "/ws" {
		t.Fatalf("runDir = %q, want the workspace", got)
	}
	// `go run` resolves modules against its working directory, so a Go plugin
	// keeps its own dir even with a workspace capability.
	tool.Runtime = "go"
	if got := tool.runDir(); got != "/plugins/t" {
		t.Fatalf("go runDir = %q, want the tool dir", got)
	}
}

// A Go plugin resolves its own module (its go.mod and sibling packages) only
// from its own directory, so the workspace cwd must not apply to `go run`.
func TestGoToolboxToolKeepsItsModuleContext(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain unavailable")
	}
	root := t.TempDir()
	writePlugin(t, root, "go_tool", "", map[string]any{
		"capabilities": map[string]any{"reads_workspace": true, "executes_code": true},
	})
	dir := filepath.Join(root, "go_tool")
	if err := os.Remove(filepath.Join(dir, "main.py")); err != nil {
		t.Fatal(err)
	}
	testutil.MustWriteFile(t, filepath.Join(dir, "go.mod"), "module plugintool\n\ngo 1.24\n")
	testutil.MustWriteFile(t, filepath.Join(dir, "lib", "lib.go"), "package lib\n\nconst Result = \"module-ok\"\n")
	testutil.MustWriteFile(t, filepath.Join(dir, "main.go"),
		"package main\n\nimport (\n\t\"fmt\"\n\n\t\"plugintool/lib\"\n)\n\nfunc main() { fmt.Printf(`{\"result\":%q}`, lib.Result) }\n")

	handler := loadPluginHandler(t, root, "go_tool", nil)
	out, err := handler(context.Background(), &core.ToolCall{Name: "go_tool", Arguments: []byte(`{}`)})
	if err != nil {
		t.Fatalf("go plugin failed: %v", err)
	}
	if out != "module-ok" {
		t.Fatalf("result = %q, want %q", out, "module-ok")
	}
}
