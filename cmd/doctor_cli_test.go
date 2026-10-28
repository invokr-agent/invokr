package cmd

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
)

func TestRenderChecksExitsNonzeroWithRemedy(t *testing.T) {
	var out bytes.Buffer
	err := renderChecks(&out, []checkResult{
		{name: "provider", status: "ok", detail: "fine"},
		{name: "model", status: "warn", detail: "unknown", remedy: "set provider.model"},
		{name: "grants", status: "fail", detail: "bad json", remedy: "delete grants.json"},
	})
	if err == nil {
		t.Fatalf("expected an error when a check fails")
	}
	text := out.String()
	for _, want := range []string{"remedy: set provider.model", "remedy: delete grants.json", "FAIL"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q:\n%s", want, text)
		}
	}
	if strings.Count(text, "remedy:") != 2 {
		t.Fatalf("ok checks should not print a remedy:\n%s", text)
	}
}

func TestRenderChecksAllOK(t *testing.T) {
	var out bytes.Buffer
	if err := renderChecks(&out, []checkResult{{name: "provider", status: "ok", detail: "fine"}}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckProviderUnreachable(t *testing.T) {
	ids, res := checkProvider(context.Background(), config.ProviderConfig{BaseURL: "http://127.0.0.1:1/v1"})
	if ids != nil {
		t.Fatalf("expected no model ids, got %v", ids)
	}
	if res.status != "fail" || res.remedy == "" {
		t.Fatalf("expected a fail with a remedy, got %+v", res)
	}
}

func TestCheckProviderAndModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"local-model"}]}`))
	}))
	defer server.Close()

	ids, res := checkProvider(context.Background(), config.ProviderConfig{BaseURL: server.URL + "/v1"})
	if res.status != "ok" {
		t.Fatalf("expected ok, got %+v", res)
	}
	if got := checkModel("local-model", ids); got.status != "ok" {
		t.Fatalf("expected served model to pass, got %+v", got)
	}
	if got := checkModel("", ids); got.status != "ok" || !strings.Contains(got.detail, "local-model") {
		t.Fatalf("expected empty model to fall back to first served, got %+v", got)
	}
	missing := checkModel("other-model", ids)
	if missing.status != "fail" || missing.remedy == "" {
		t.Fatalf("expected missing model to fail with a remedy, got %+v", missing)
	}
}

func TestCheckGrantsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "grants.json")
	cfg := config.ApprovalsConfig{GrantsFile: path}

	if got := checkGrantsFile(cfg); got.status != "ok" {
		t.Fatalf("missing grants file should be ok, got %+v", got)
	}
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatalf("write grants file: %v", err)
	}
	if got := checkGrantsFile(cfg); got.status != "warn" || got.remedy == "" {
		t.Fatalf("loose permissions should warn with a remedy, got %+v", got)
	}
	if err := os.WriteFile(path, []byte("not json"), 0o600); err != nil {
		t.Fatalf("write grants file: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod grants file: %v", err)
	}
	if got := checkGrantsFile(cfg); got.status != "fail" {
		t.Fatalf("corrupt grants file should fail, got %+v", got)
	}
}

func TestCheckMCPServers(t *testing.T) {
	cfg := config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"off":      {Command: "definitely-not-a-real-binary", Enabled: false},
		"optional": {Command: "definitely-not-a-real-binary", Enabled: true},
	}}
	if got := checkMCPServers(cfg); got.status != "warn" {
		t.Fatalf("optional broken server should warn, got %+v", got)
	}
	cfg.Servers["needed"] = config.MCPServerConfig{Command: "definitely-not-a-real-binary", Enabled: true, Required: true}
	if got := checkMCPServers(cfg); got.status != "fail" {
		t.Fatalf("required broken server should fail, got %+v", got)
	}
}

func TestCheckMCPServersIgnoresHTTPServers(t *testing.T) {
	cfg := config.MCPConfig{Servers: map[string]config.MCPServerConfig{
		"remote": {URL: "https://mcp.example.com/rpc", Enabled: true, Required: true},
	}}
	if got := checkMCPServers(cfg); got.status != "ok" {
		t.Fatalf("HTTP server has no local command to resolve, got %+v", got)
	}
}

func TestCheckStateDirs(t *testing.T) {
	home := t.TempDir()
	got := checkStateDirs(home, home)
	if got.status != "ok" {
		t.Fatalf("temp dir should be writable, got %+v", got)
	}
	if strings.Count(got.detail, home) != 2 {
		t.Fatalf("home and its sessions dir should both be probed once: %q", got.detail)
	}
	if _, err := os.Stat(filepath.Join(home, "sessions")); err != nil {
		t.Fatalf("sessions dir should have been probed: %v", err)
	}

	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	if got := checkStateDirs(filepath.Join(blocker, "nested"), home); got.status != "fail" || got.remedy == "" {
		t.Fatalf("uncreatable home should fail with a remedy, got %+v", got)
	}
	// A memory dir elsewhere is probed too, not just the home dir.
	if got := checkStateDirs(home, filepath.Join(blocker, "memory")); got.status != "fail" {
		t.Fatalf("uncreatable memory dir should fail, got %+v", got)
	}
	// An unset memory dir must not turn into a relative "sessions" path in cwd.
	if got := checkStateDirs(t.TempDir(), ""); got.status != "ok" || strings.Contains(got.detail, "sessions") {
		t.Fatalf("an empty memory dir should probe the home dir only, got %+v", got)
	}
}

func TestCheckNamespaceSandbox(t *testing.T) {
	got := checkNamespaceSandbox()
	if got.status == "ok" {
		if got.remedy != "" {
			t.Fatalf("an available sandbox needs no remedy, got %+v", got)
		}
		return
	}
	if got.status != "warn" || got.remedy == "" {
		t.Fatalf("an unavailable sandbox should warn with a remedy, got %+v", got)
	}
	if runtime.GOOS != "linux" && !strings.Contains(got.detail, runtime.GOOS) {
		t.Fatalf("non-Linux detail should name the platform, got %+v", got)
	}
}

func TestCheckInterpretersVenvDir(t *testing.T) {
	cfg := config.ToolboxConfig{VenvDir: t.TempDir()}
	if got := checkInterpreters(cfg); got.status != "ok" {
		t.Fatalf("writable venv_dir should be ok, got %+v", got)
	}
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o600); err != nil {
		t.Fatalf("write blocker: %v", err)
	}
	cfg.VenvDir = filepath.Join(blocker, "venv")
	if got := checkInterpreters(cfg); got.status != "warn" || got.remedy == "" {
		t.Fatalf("uncreatable venv_dir should warn with a remedy, got %+v", got)
	}
	if got := checkInterpreters(config.ToolboxConfig{GoBin: "definitely-not-a-real-binary"}); got.status != "warn" {
		t.Fatalf("missing interpreter should warn, got %+v", got)
	}
}

func TestCheckMemoryModels(t *testing.T) {
	if got := checkMemoryModels(config.ZerfooMemoryConfig{}); got.status != "ok" {
		t.Fatalf("disabled zerfoo memory should be ok, got %+v", got)
	}

	gguf := filepath.Join(t.TempDir(), "model.gguf")
	if err := os.WriteFile(gguf, make([]byte, 2048), 0o600); err != nil {
		t.Fatalf("write gguf: %v", err)
	}
	cached := config.ZerfooMemoryConfig{Enabled: true, EmbeddingPath: gguf, SummarizationPath: gguf}
	got := checkMemoryModels(cached)
	if got.status != "ok" || !strings.Contains(got.detail, "2.0 KiB") {
		t.Fatalf("cached local models should be ok with a size, got %+v", got)
	}

	missing := config.ZerfooMemoryConfig{Enabled: true, EmbeddingPath: filepath.Join(t.TempDir(), "gone.gguf")}
	if got := checkMemoryModels(missing); got.status != "fail" || got.remedy == "" {
		t.Fatalf("a missing local GGUF should fail with a remedy, got %+v", got)
	}
}

func TestHumanBytes(t *testing.T) {
	for _, tc := range []struct {
		in   int64
		want string
	}{{512, "512 B"}, {2048, "2.0 KiB"}, {5 << 20, "5.0 MiB"}, {3 << 30, "3.0 GiB"}, {2 << 40, "2.0 TiB"}} {
		if got := humanBytes(tc.in); got != tc.want {
			t.Fatalf("humanBytes(%d) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func shellCfgWithDefaults(commands ...string) config.ShellConfig {
	cfg := config.AppConfig{}
	if len(commands) > 0 {
		cfg.Shell.AllowedCommands = commands
	}
	return cfg.WithDefaults().Shell
}

func TestCheckShellAllowlistSuggestsForDetectedMarkers(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := checkShellAllowlist(shellCfgWithDefaults(), root)
	if got.status != "warn" || !strings.Contains(got.detail, "npm") {
		t.Fatalf("want a warn naming npm, got %+v", got)
	}
	if !strings.Contains(got.remedy, `"allowed_commands"`) || !strings.Contains(got.remedy, `"npm"`) {
		t.Fatalf("remedy is not paste-ready: %q", got.remedy)
	}
}

func TestCheckShellAllowlistFlagsUnclassifiedCommands(t *testing.T) {
	cfg := shellCfgWithDefaults("jq", "ls")
	before := strings.Join(cfg.AllowedCommands, ",")
	got := checkShellAllowlist(cfg, t.TempDir())
	if got.status != "warn" || !strings.Contains(got.detail, "jq") {
		t.Fatalf("want a warn naming jq, got %+v", got)
	}
	if !strings.Contains(got.detail, "every call") {
		t.Fatalf("warn does not explain the trap: %q", got.detail)
	}
	if strings.Join(cfg.AllowedCommands, ",") != before {
		t.Fatal("check mutated the config it was handed")
	}
}

func TestCheckShellAllowlistOKWhenConfigured(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := checkShellAllowlist(shellCfgWithDefaults(), root); got.status != "ok" {
		t.Fatalf("default allowlist covers a Go repo, got %+v", got)
	}
}

// The gap this check exists for: with no server installed the lsp_* tools are
// never registered, and without this nothing tells the user why.
func TestCheckLanguageServersWarnsWhenNoneAreInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir())

	got := checkLanguageServers(config.LSPConfig{})
	if got.status != "warn" {
		t.Fatalf("no server on PATH should warn, got %+v", got)
	}
	if !strings.Contains(got.detail, "not registered") {
		t.Fatalf("detail should say the tools are absent, got %q", got.detail)
	}
	if !strings.Contains(got.remedy, "go install golang.org/x/tools/gopls@latest") {
		t.Fatalf("remedy should name a real install command, got %q", got.remedy)
	}
}

func TestCheckLanguageServersReportsWhatResolved(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "gopls"), []byte("#!/bin/sh\n"), 0o700); err != nil {
		t.Fatalf("write fake server: %v", err)
	}
	t.Setenv("PATH", dir)

	got := checkLanguageServers(config.LSPConfig{})
	if got.status != "ok" {
		t.Fatalf("one resolvable server should be ok, got %+v", got)
	}
	if !strings.Contains(got.detail, "gopls resolvable") {
		t.Fatalf("detail should name what resolved, got %q", got.detail)
	}
	// Knowing which languages are uncovered is the useful half of an ok result.
	if !strings.Contains(got.detail, "not installed: pyright-langserver") {
		t.Fatalf("detail should name what is missing, got %q", got.detail)
	}
}

func TestCheckLanguageServersRespectsDisabledAndCustomServers(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	off := false
	if got := checkLanguageServers(config.LSPConfig{Enabled: &off}); got.status != "ok" {
		t.Fatalf("disabled lsp should be ok, got %+v", got)
	}

	// A user-configured server invokr has no hint for is still named, so the
	// remedy is never empty.
	custom := config.LSPConfig{Servers: []config.LSPServerConfig{
		{Command: "zls", Extensions: []string{".zig"}, LanguageID: "zig"},
	}}
	got := checkLanguageServers(custom)
	if got.status != "warn" || !strings.Contains(got.remedy, "zls") {
		t.Fatalf("custom server should warn and be named, got %+v", got)
	}
	if strings.Contains(got.remedy, "gopls") {
		t.Fatalf("configured servers replace the built-in table, got %q", got.remedy)
	}
}
