package toolbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/support/testutil"
	"github.com/invokr-ai/invokr/tools/core"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestLoadToolboxTools_InferSupportedRuntimes(t *testing.T) {
	root := t.TempDir()

	testutil.MustWriteToolboxPlugin(t, root, "go_tool", "go plugin", map[string]string{
		"main.go": `package main`,
	})

	testutil.MustWriteToolboxPlugin(t, root, "node_tool", "node plugin", map[string]string{
		"main.js": `console.log('ok')`,
	})

	testutil.MustWriteToolboxPlugin(t, root, "python_tool", "python plugin", map[string]string{
		"main.py": `print("ok")`,
	})

	tools := LoadToolboxTools(zap.NewNop(), core.CallerDeps{App: config.AppConfig{Toolbox: config.ToolboxConfig{Dir: root}}})

	if len(tools) != 3 {
		t.Fatalf("expected 3 tools, got %d", len(tools))
	}

	runtimes := map[string]string{}
	for _, tool := range tools {
		runtimes[tool.Spec.Name] = tool.Spec.Metadata.Runtime
	}
	if runtimes["go_tool"] != "go" {
		t.Fatalf("expected go_tool runtime go")
	}
	if runtimes["node_tool"] != "node" {
		t.Fatalf("expected node_tool runtime node")
	}
	if runtimes["python_tool"] != "python" {
		t.Fatalf("expected python_tool runtime python")
	}
}

func TestPluginEnvUsesConfigOwnedEnvironment(t *testing.T) {
	cacheDir := filepath.Join(t.TempDir(), "go-cache")
	isolate := true
	env := pluginEnv("go", config.ToolboxConfig{
		IsolateEnv:        &isolate,
		Env:               map[string]string{"CUSTOM_ENV": "configured"},
		GoCacheDir:        cacheDir,
		GoBin:             "/usr/local/bin/go",
		BrowserAgentState: filepath.Join(t.TempDir(), "browser-state"),
	}.WithDefaults(t.TempDir()))

	if got := core.GetEnvValue(env, "CUSTOM_ENV"); got != "configured" {
		t.Fatalf("expected configured env value, got %q", got)
	}
	if got := core.GetEnvValue(env, "GOCACHE"); got != cacheDir {
		t.Fatalf("expected configured GOCACHE %q, got %q", cacheDir, got)
	}
	if got, want := core.GetEnvValue(env, "PATH"), os.Getenv("PATH"); got != want {
		t.Fatalf("expected isolated env to seed host PATH %q, got %q", want, got)
	}
	if got := core.GetEnvValue(env, "HOME"); got != "" {
		t.Fatalf("expected isolated env to omit other host vars like HOME, got %q", got)
	}
	if got := core.GetEnvValue(env, "INVOKR_GO_BIN"); got != "/usr/local/bin/go" {
		t.Fatalf("expected configured INVOKR_GO_BIN, got %q", got)
	}
	if got := core.GetEnvValue(env, "INVOKR_TOOL_STATE_DIR"); got == "" {
		t.Fatal("expected configured tool state directory")
	}
	if got := core.GetEnvValue(env, "INVOKR_SHELL_ALLOWED_COMMANDS"); got != "" {
		t.Fatalf("expected plugin env to omit shell command list, got %q", got)
	}
}

func TestDaemonStateDirIsScopedByPluginAndSession(t *testing.T) {
	p := &executableTool{ToolSpec: core.ToolSpec{Name: "test"}, dir: "/tool/one"}
	env := []string{"INVOKR_TOOL_STATE_DIR=/tmp/tool-state"}
	one, err := p.daemonStateDir(env, "session-one")
	if err != nil {
		t.Fatal(err)
	}
	two, err := p.daemonStateDir(env, "session-two")
	if err != nil {
		t.Fatal(err)
	}
	p.dir = "/tool/two"
	three, err := p.daemonStateDir(env, "session-one")
	if err != nil {
		t.Fatal(err)
	}
	if one == two || one == three {
		t.Fatalf("daemon state directories must be isolated: %q, %q, %q", one, two, three)
	}
}

func TestToolboxConfigEnvPolicyOverrides(t *testing.T) {
	t.Setenv("INVOKR_TOOLBOX_ISOLATE_ENV", "true")
	t.Setenv("INVOKR_TOOLBOX_ENV", "A=one,B=two")
	t.Setenv("INVOKR_GO_CACHE_DIR", "/tmp/custom-go-cache")
	t.Setenv("INVOKR_PYTHON_PIP_ARGS", "install --no-index -r")
	t.Setenv("INVOKR_BROWSER_AGENT_STATE", "/tmp/browser-state")
	t.Setenv("INVOKR_SHELL_ALLOWED_COMMANDS", "git,go")

	t.Setenv("INVOKR_HOME", t.TempDir())
	cfg, err := config.LoadDefaultFileWithProfile("")
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}

	if !cfg.Toolbox.IsolateHostEnv() {
		t.Fatal("expected toolbox env isolation override")
	}
	if cfg.Toolbox.Env["A"] != "one" || cfg.Toolbox.Env["B"] != "two" {
		t.Fatalf("expected configured toolbox env map, got %#v", cfg.Toolbox.Env)
	}
	if cfg.Toolbox.GoCacheDir != "/tmp/custom-go-cache" {
		t.Fatalf("expected configured go cache dir, got %q", cfg.Toolbox.GoCacheDir)
	}
	if got := strings.Join(cfg.Toolbox.PythonPipArgs, " "); got != "install --no-index -r" {
		t.Fatalf("expected configured pip args, got %q", got)
	}
	if cfg.Toolbox.BrowserAgentState != "/tmp/browser-state" {
		t.Fatalf("expected configured browser state, got %q", cfg.Toolbox.BrowserAgentState)
	}
	if got := strings.Join(cfg.Shell.AllowedCommands, ","); got != "git,go" {
		t.Fatalf("expected configured shell commands, got %q", got)
	}
}

func TestRetiredEditToolsAreAlwaysSkipped(t *testing.T) {
	for _, name := range []string{"write_file", "replace_in_file", "read_file"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			testutil.MustWriteToolboxPlugin(t, root, name, "legacy writer", map[string]string{
				"main.py": `print("{\"result\":\"ok\"}")`,
			})

			tools := LoadToolboxTools(zap.NewNop(), core.CallerDeps{App: config.AppConfig{Toolbox: config.ToolboxConfig{Dir: root}}})
			if len(tools) != 0 {
				t.Fatalf("expected retired %s tool to be skipped, got %d tools", name, len(tools))
			}
		})
	}
}

// Load-bearing for create_tool too: a retired name must be refused at
// authoring time so a plugin can never squat the native read_file.
func TestReadFileSpecIsRetired(t *testing.T) {
	err := ValidateSpecFields("read_file", "desc", "example", core.ToolPolicy{Tiers: []string{"read_workspace"}, Approval: "auto"})
	if err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("expected read_file spec to be retired, got %v", err)
	}
}

func TestLoadToolboxToolsRejectsMissingPolicyMetadata(t *testing.T) {
	root := t.TempDir()
	pluginDir := filepath.Join(root, "missing_policy")
	testutil.MustWriteFile(t, filepath.Join(pluginDir, "tool.json"), `{
  "name":"missing_policy",
  "description":"Missing policy metadata.",
  "example":"{\"name\":\"missing_policy\",\"arguments\":{}}"
}`)
	testutil.MustWriteFile(t, filepath.Join(pluginDir, "main.py"), `print('{"result":"ok"}')`)

	tools := LoadToolboxTools(zap.NewNop(), core.CallerDeps{App: config.AppConfig{Toolbox: config.ToolboxConfig{Dir: root}}})
	if len(tools) != 0 {
		t.Fatalf("expected tool without policy metadata to be rejected, got %d tools", len(tools))
	}
}

func TestToolboxAlwaysVisibleComesFromSpec(t *testing.T) {
	root := t.TempDir()
	for _, tc := range []struct {
		name, extra string
		want        bool
	}{
		{name: "custom_visible", extra: `,"always_visible":true`, want: true},
		{name: "custom_default", want: false},
		{name: "custom_hidden", extra: `,"always_visible":false`, want: false},
	} {
		t.Run(tc.name+tc.extra, func(t *testing.T) {
			dir := filepath.Join(root, strings.ReplaceAll(t.Name(), "/", "_"))
			testutil.MustWriteFile(t, filepath.Join(dir, "tool.json"), fmt.Sprintf(`{
  "name":%q,"description":"visibility test","example":"example",
  "policy":{"tiers":["read_workspace"],"approval":"auto"}%s
}`, tc.name, tc.extra))
			testutil.MustWriteFile(t, filepath.Join(dir, "main.py"), `print('{"result":"ok"}')`)
			tool, err := loadToolboxTool(dir, config.ToolboxConfig{}.WithDefaults(root), zap.NewNop())
			if err != nil {
				t.Fatal(err)
			}
			if tool.Metadata.AlwaysVisible != tc.want {
				t.Fatalf("AlwaysVisible = %v, want %v", tool.Metadata.AlwaysVisible, tc.want)
			}
		})
	}
}

func TestLoadToolboxToolsSkipsHiddenAndRetiredDirsSilently(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{".venv", ".git", "write_file"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	observed, logs := observer.New(zap.WarnLevel)
	tools := LoadToolboxTools(zap.New(observed), core.CallerDeps{App: config.AppConfig{Toolbox: config.ToolboxConfig{Dir: root}}})

	if len(tools) != 0 {
		t.Fatalf("expected no tools, got %d", len(tools))
	}
	if logs.Len() != 0 {
		t.Fatalf("expected no warnings for hidden/retired dirs, got %v", logs.All())
	}
}

// TestMain pins sandbox availability on so the loader tests exercise tool
// discovery rather than the registration skip, regardless of the test host.
func TestMain(m *testing.M) {
	sandboxAvailableFn = func() bool { return true }
	os.Exit(m.Run())
}

func TestLoadToolboxToolsSkipsRegistrationWithoutSandbox(t *testing.T) {
	root := t.TempDir()
	testutil.MustWriteToolboxPlugin(t, root, "python_tool", "python plugin", map[string]string{
		"main.py": `print("ok")`,
	})

	sandboxAvailableFn = func() bool { return false }
	t.Cleanup(func() { sandboxAvailableFn = func() bool { return true } })

	observed, logs := observer.New(zap.WarnLevel)
	deps := core.CallerDeps{App: config.AppConfig{Toolbox: config.ToolboxConfig{Dir: root}}}
	if tools := LoadToolboxTools(zap.New(observed), deps); len(tools) != 0 {
		t.Fatalf("expected no tools registered without a sandbox, got %d", len(tools))
	}
	if logs.Len() != 1 {
		t.Fatalf("expected one startup warning, got %v", logs.All())
	}

	deps.App.Toolbox.AllowUnsandboxed = true
	if tools := LoadToolboxTools(zap.NewNop(), deps); len(tools) != 1 {
		t.Fatalf("expected AllowUnsandboxed to register the tool, got %d", len(tools))
	}
}

type stubRegistrar struct {
	replaced  int
	onReplace func()
}

func (r *stubRegistrar) RegisterNative(core.ToolSpec, core.NativeHandler) {}
func (r *stubRegistrar) HasNative(string) bool                            { return false }
func (r *stubRegistrar) NativeSource(string) string                       { return "" }
func (r *stubRegistrar) ReplaceNativeToolsBySource(string, []core.DynamicNativeTool) []string {
	r.replaced++
	if r.onReplace != nil {
		r.onReplace()
	}
	return nil
}

func TestRegisterToolboxToolsUnregisterDuringRefresh(t *testing.T) {
	root := t.TempDir()
	bus := events.NewOrderedDispatcher(nil)
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	registrar := &stubRegistrar{}
	registrar.onReplace = func() {
		if registrar.replaced == 2 {
			close(entered)
			<-release
		}
	}
	deps := core.CallerDeps{
		App: config.AppConfig{Toolbox: config.ToolboxConfig{Dir: root}}, Dispatcher: bus,
	}
	unregister := RegisterToolboxTools(registrar, deps, zap.NewNop())
	defer unregister()
	// An entry-count change forces a scan without relying on debounce timing.
	if err := os.Mkdir(filepath.Join(root, ".refresh-marker"), 0700); err != nil {
		t.Fatal(err)
	}
	event := events.Event{Type: events.EventToolboxRefreshRequested}
	go func() {
		defer close(done)
		bus.Dispatch(context.Background(), event)
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("refresh did not reach the registrar")
	}
	unregister()
	close(release)
	<-done
	bus.Dispatch(context.Background(), event)
	if registrar.replaced != 2 {
		t.Fatalf("replacement calls = %d, want initial scan and in-flight refresh", registrar.replaced)
	}
}

type stubBus struct {
	registered   int
	unregistered int
}

func (b *stubBus) Dispatch(context.Context, events.Event) {}
func (b *stubBus) Register(events.Handler) func() {
	b.registered++
	return func() { b.unregistered++ }
}

func TestRegisterToolboxTools_ReturnsBusUnregisterFunc(t *testing.T) {
	bus := &stubBus{}
	registrar := &stubRegistrar{}
	deps := core.CallerDeps{
		App:        config.AppConfig{Toolbox: config.ToolboxConfig{Dir: t.TempDir()}},
		Dispatcher: bus,
	}

	unregister := RegisterToolboxTools(registrar, deps, zap.NewNop())
	if unregister == nil {
		t.Fatalf("expected non-nil unregister func")
	}
	if bus.registered != 1 {
		t.Fatalf("expected 1 bus registration, got %d", bus.registered)
	}
	if registrar.replaced != 1 {
		t.Fatalf("expected initial toolbox scan, got %d", registrar.replaced)
	}

	unregister()
	if bus.unregistered != 1 {
		t.Fatalf("expected bus unregistration, got %d", bus.unregistered)
	}
}

func TestRegisterToolboxTools_NilRegistrarAndPlainDispatcher(t *testing.T) {
	if unregister := RegisterToolboxTools(nil, core.CallerDeps{}, zap.NewNop()); unregister == nil {
		t.Fatalf("expected non-nil no-op unregister for nil registrar")
	}
	deps := core.CallerDeps{App: config.AppConfig{Toolbox: config.ToolboxConfig{Dir: t.TempDir()}}}
	if unregister := RegisterToolboxTools(&stubRegistrar{}, deps, zap.NewNop()); unregister == nil {
		t.Fatalf("expected non-nil no-op unregister for non-bus dispatcher")
	}
}

func TestToolboxCommandDeclarationReachesMetadata(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "cmd_tool")
	testutil.MustWriteFile(t, filepath.Join(dir, "tool.json"), `{
  "name":"cmd_tool","description":"command test","example":"example",
  "policy":{"tiers":["read_workspace"],"approval":"auto"},
  "command":{"name":"browse","usage":"/browse url=…","summary":"drive the browser"}
}`)
	testutil.MustWriteFile(t, filepath.Join(dir, "main.py"), `print('{"result":"ok"}')`)
	tool, err := loadToolboxTool(dir, config.ToolboxConfig{}.WithDefaults(root), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	command := tool.Metadata.Command
	if command == nil || command.Name != "browse" || command.Summary != "drive the browser" {
		t.Fatalf("command metadata not loaded: %+v", command)
	}
}
