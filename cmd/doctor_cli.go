package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"

	"github.com/invokr-agent/invokr/capability/memory/zerfoo"
	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/constants"
	toolcore "github.com/invokr-agent/invokr/tools/core"
	lspsystem "github.com/invokr-agent/invokr/tools/system/lsp"
	"github.com/invokr-agent/invokr/tools/system/shell"
	"github.com/invokr-agent/invokr/tools/system/toolbox"

	"github.com/dustin/go-humanize"
	"github.com/spf13/cobra"
)

// checkResult is one diagnostic line. Status is "ok", "warn", or "fail"; only
// a "fail" makes the command exit nonzero, and every non-ok result carries a
// remedy telling the user what to actually do about it.
type checkResult struct{ name, status, detail, remedy string }

// MarshalJSON backs `doctor --json`; the fields stay unexported.
func (c checkResult) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]string{"name": c.name, "status": c.status, "detail": c.detail, "remedy": c.remedy})
}

func ok(name, detail string) checkResult { return checkResult{name, "ok", detail, ""} }

// NewDoctorCmd builds `invokr doctor`. It deliberately never constructs the
// runtime composition root (see skipsRuntimeBootstrap in root.go): doctor
// exists to explain *why* bootstrap would fail, so it may only read config and
// probe the environment directly.
func NewDoctorCmd(app *config.AppConfig) *cobra.Command {
	cmd := &cobra.Command{
		Use:          "doctor",
		Short:        "Check why the agent will not start, without starting it",
		Long:         "Probes the provider endpoint, the configured model, state directories,\nthe sandbox and container engine, crontab, toolbox interpreters, memory\nmodels and MCP servers. Each non-ok line carries a remedy. Exits\nnonzero only when a check FAILs; WARN is informational.\n\nIt never builds the runtime, so it still works in exactly the broken\nenvironments that make every other command fail.",
		Example:      "  invokr doctor",
		GroupID:      groupConfigure,
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			results := runChecks(cmd.Context(), app.WithDefaults())
			err := emit(cmd, results, func() error { return renderChecks(cmd.OutOrStdout(), results) })
			if err == nil && slices.ContainsFunc(results, func(r checkResult) bool { return r.status == "fail" }) {
				return errors.New("doctor: a check failed") // --json keeps the exit code
			}
			return err
		},
	}
	addJSONFlag(cmd)
	return cmd
}

func runChecks(ctx context.Context, cfg config.AppConfig) []checkResult {
	models, provider := checkProvider(ctx, cfg.Provider)
	return []checkResult{
		provider, checkModel(cfg.Provider.Model, models), checkStateDirs(config.InvokrHomeDir(), cfg.Memory.Dir), checkGrantsFile(cfg.Approvals), checkNamespaceSandbox(), checkContainerEngine(cfg),
		checkOnPath("crontab", "crontab", "install cron (e.g. cronie or vixie-cron); `invokr jobs add` cannot schedule anything without it"),
		checkOnPath("git", "git", "install git; `invokr skills install` cannot fetch skill hubs without it"),
		checkInterpreters(cfg.Toolbox), checkShellAllowlist(cfg.Shell, "."), checkMemoryModels(cfg.Memory.Zerfoo), checkMCPServers(cfg.MCP), checkLanguageServers(cfg.LSP)}
}

func renderChecks(out io.Writer, results []checkResult) error {
	style := newRenderer(out)
	failed := 0
	for _, r := range results {
		_, _ = fmt.Fprintf(out, "%s  %-13s  %s\n", doctorStatus(style, r.status), r.name, r.detail)
		if r.status != "ok" && r.remedy != "" {
			_, _ = fmt.Fprintf(out, "        %s %s\n", style.muted("remedy:"), r.remedy)
		}
		if r.status == "fail" {
			failed++
		}
	}
	if failed > 0 {
		return fmt.Errorf("doctor: %d check(s) failed", failed)
	}
	return nil
}

// doctorStatus pads before painting. Escape sequences have no display width but
// plenty of bytes, so a %-4s around a painted word aligns the escapes and not
// the column.
func doctorStatus(style renderer, status string) string {
	word := fmt.Sprintf("%-4s", strings.ToUpper(status))
	switch status {
	case "ok":
		return style.success(word)
	case "fail":
		return style.danger(word)
	default:
		return style.warn(word)
	}
}

// checkProvider probes the OpenAI-compatible /models endpoint and returns the
// served model ids so checkModel can reuse them.
func checkProvider(ctx context.Context, p config.ProviderConfig) ([]string, checkResult) {
	url := strings.TrimSuffix(p.BaseURL, "/") + "/models"
	fixURL := "start the provider or fix provider.base_url (--base-url / INVOKR_BASE_URL)"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, checkResult{"provider", "fail", fmt.Sprintf("invalid base_url %q: %v", p.BaseURL, err), fixURL}
	}
	if p.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.APIKey)
	}
	resp, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
	if err != nil {
		return nil, checkResult{"provider", "fail", fmt.Sprintf("%s unreachable: %v", url, err), fixURL}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, checkResult{"provider", "fail", fmt.Sprintf("%s returned %s", url, resp.Status), "check provider.api_key and that base_url points at an OpenAI-compatible /v1 endpoint"}
	}
	var payload struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, checkResult{"provider", "warn", fmt.Sprintf("%s reachable but not OpenAI-shaped: %v", url, err),
			"confirm base_url ends at the API root (e.g. http://host:port/v1)"}
	}
	ids := make([]string, 0, len(payload.Data))
	for _, m := range payload.Data {
		ids = append(ids, m.ID)
	}
	return ids, ok("provider", fmt.Sprintf("%s lists %d model(s)", url, len(ids)))
}

func checkModel(model string, served []string) checkResult {
	switch {
	case model == "" && len(served) > 0:
		return ok("model", "no model set; will use "+served[0])
	case len(served) == 0:
		return checkResult{"model", "warn", "cannot verify " + model + ": no model list from the provider", "fix the provider check above, then re-run invokr doctor"}
	case !slices.Contains(served, model):
		return checkResult{"model", "fail", fmt.Sprintf("%q not served; provider offers: %s", model, strings.Join(served, ", ")), "set provider.model (or --model) to one of the served models"}
	}
	return ok("model", model+" is served")
}

// checkStateDirs probes every directory Invokr writes to before it can do
// anything useful: the home dir, the memory dir (which INVOKR_MEMORY_DIR can
// point elsewhere), and the session store nested inside it. Duplicates collapse
// so the common case — all three under ~/.invokr — probes once.
func checkStateDirs(home, memoryDir string) checkResult {
	dirs := []string{home}
	if memoryDir != "" {
		dirs = toolcore.DedupPreserveOrder(append(dirs, memoryDir, filepath.Join(memoryDir, "sessions")))
	}
	for _, dir := range dirs {
		if err := probeWritable(dir); err != nil {
			return checkResult{"storage", "fail", err.Error(), "make " + dir + " writable (chown/chmod), or point INVOKR_HOME / INVOKR_MEMORY_DIR at a writable directory"}
		}
	}
	return ok("storage", strings.Join(dirs, ", ")+" writable")
}

// probeWritable is doctor's one deliberate side effect: it creates the
// directory if missing and writes a temp file into it. Creating it is exactly
// what startup would do, so probing without creating would answer a different
// question than the one the operator is asking.
func probeWritable(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("cannot create %s: %w", dir, err)
	}
	probe, err := os.CreateTemp(dir, ".doctor-*")
	if err != nil {
		return fmt.Errorf("%s is not writable: %w", dir, err)
	}
	_ = probe.Close()
	_ = os.Remove(probe.Name())
	return nil
}

// checkGrantsFile validates the standing-grants store: it holds auto-approval
// decisions, so loose permissions or a corrupt file are security-relevant.
func checkGrantsFile(c config.ApprovalsConfig) checkResult {
	if c.StandingGrantsDisabled {
		return ok("grants", "standing grants disabled")
	}
	info, err := os.Stat(c.GrantsFile)
	switch {
	case os.IsNotExist(err):
		return ok("grants", "no grants file yet (written on the first standing grant)")
	case err != nil:
		return checkResult{"grants", "fail", fmt.Sprintf("cannot stat %s: %v", c.GrantsFile, err), "fix permissions on the file, or delete it to start from no standing grants"}
	case info.Mode().Perm() != 0o600:
		return checkResult{"grants", "warn", fmt.Sprintf("%s has mode %04o, expected 0600", c.GrantsFile, info.Mode().Perm()), "chmod 600 " + c.GrantsFile}
	}
	if data, err := os.ReadFile(c.GrantsFile); err != nil || !json.Valid(data) {
		return checkResult{"grants", "fail", c.GrantsFile + " is unreadable or not valid JSON", "delete " + c.GrantsFile + "; standing grants will simply be asked again"}
	}
	return ok("grants", c.GrantsFile+" looks sane")
}

// checkNamespaceSandbox reports whether toolbox tools and the code_interpreter
// process backend can run sandboxed. The probe is Linux-only and also fails on
// hardened hosts that ship `unshare` with unprivileged user namespaces disabled
// — without it both refuse to run, so this is worth knowing before the first
// tool call.
func checkNamespaceSandbox() checkResult {
	if toolbox.NamespaceSandboxAvailable() {
		return ok("sandbox", "unprivileged user namespaces work; toolbox tools and the code_interpreter process backend run sandboxed")
	}
	detail := "unprivileged user namespaces unavailable"
	if runtime.GOOS != "linux" {
		detail = "namespace sandboxing is Linux-only (this host is " + runtime.GOOS + ")"
	}
	return checkResult{"sandbox", "warn", detail,
		"toolbox tools and the code_interpreter process backend refuse to run; install util-linux and enable user.max_user_namespaces, or set INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1 to run them on the host (not recommended)"}
}

func checkContainerEngine(cfg config.AppConfig) checkResult {
	containerCI := cfg.CodeInterpreter.Backend == constants.CodeInterpreterBackendContainer
	if !cfg.Sandbox.Enabled && !containerCI {
		return ok("container", "not required (sandbox off, code_interpreter backend="+cfg.CodeInterpreter.Backend+")")
	}
	engine := cfg.Sandbox.Engine
	if containerCI && cfg.CodeInterpreter.ContainerEngine != "" {
		engine = cfg.CodeInterpreter.ContainerEngine
	}
	path, err := exec.LookPath(engine)
	if err != nil {
		return checkResult{"container", "fail", fmt.Sprintf("%s not found in PATH: %v", engine, err), "install " + engine + ", or set sandbox.enabled=false and code_interpreter.backend=process"}
	}
	return ok("container", engine+" found at "+path)
}

// checkOnPath is the shape every "is this binary installed" check shares: a
// warning naming the remedy, because none of them stops a run outright.
func checkOnPath(name, bin, remedy string) checkResult {
	if _, err := exec.LookPath(bin); err != nil {
		return checkResult{name, "warn", fmt.Sprintf("%s not found in PATH: %v", bin, err), remedy}
	}
	return ok(name, bin+" found in PATH")
}

func checkInterpreters(c config.ToolboxConfig) checkResult {
	var missing []string
	for _, bin := range []string{c.GoBin, c.NodeBin, c.PythonBin} {
		if _, err := exec.LookPath(bin); bin != "" && err != nil {
			missing = append(missing, bin)
		}
	}
	if len(missing) > 0 {
		return checkResult{"interpreters", "warn", "not on PATH: " + strings.Join(missing, ", "),
			"install them, or point toolbox.go_bin/node_bin/python_bin at real paths; toolbox tools in those languages fail until then"}
	}
	// An explicit venv_dir override replaces the per-tool venvs, so Invokr has
	// to be able to create it; the default (empty) needs no probe.
	if c.VenvDir != "" {
		if err := probeWritable(c.VenvDir); err != nil {
			return checkResult{"interpreters", "warn", "go/node/python resolvable, but venv_dir " + err.Error(),
				"make toolbox.venv_dir writable, or unset it (INVOKR_TOOLBOX_VENV_DIR) to use per-tool venvs"}
		}
	}
	return ok("interpreters", "go/node/python resolvable")
}

// checkMemoryModels reports the Zerfoo GGUF cache state. It never loads or
// downloads anything: an uncached model is a warning about a slow, network-bound
// first memory write, while a configured local path that is not there is a hard
// failure because zerfoo will never fall back to a download for it.
func checkMemoryModels(c config.ZerfooMemoryConfig) checkResult {
	if !c.Enabled {
		return ok("memory models", "zerfoo memory disabled")
	}
	var details, pending []string
	for _, m := range zerfoo.CacheStatus(c) {
		switch {
		case m.Err != nil:
			return checkResult{"memory models", "fail", fmt.Sprintf("%s path %s is unusable: %v", m.Role, m.Ref, m.Err),
				"fix memory.zerfoo." + m.Role + "_path, or clear it to download " + m.Ref + " from HuggingFace"}
		case m.Cached:
			details = append(details, fmt.Sprintf("%s %s cached (%s)", m.Role, m.Ref, humanBytes(m.Bytes)))
		default:
			details = append(details, m.Role+" "+m.Ref+" not cached")
			pending = append(pending, m.Ref)
		}
	}
	if len(pending) > 0 {
		return checkResult{"memory models", "warn", strings.Join(details, "; "), "the first memory write downloads " + strings.Join(pending, " and ") +
			" from HuggingFace (GB-scale); pre-fetch it, or set memory.zerfoo.*_path to local GGUFs"}
	}
	return ok("memory models", strings.Join(details, "; "))
}

func humanBytes(n int64) string { return humanize.IBytes(uint64(n)) }

func checkMCPServers(c config.MCPConfig) checkResult {
	var broken []string
	status := "warn"
	for name, server := range c.Servers {
		if !server.Enabled || strings.TrimSpace(server.URL) != "" {
			continue // HTTP servers have no local command to resolve.
		}
		if _, err := exec.LookPath(server.Command); err != nil {
			broken = append(broken, name+" ("+server.Command+")")
			if server.Required {
				status = "fail"
			}
		}
	}
	if len(broken) == 0 {
		return ok("mcp", fmt.Sprintf("%d configured MCP server(s), all commands resolvable", len(c.Servers)))
	}
	slices.Sort(broken)
	return checkResult{"mcp", status, "command not found for: " + strings.Join(broken, ", "), "install the server command or set mcp.servers.<name>.enabled=false"}
}

func checkShellAllowlist(cfg config.ShellConfig, root string) checkResult {
	if !cfg.IsEnabled() {
		return ok("shell allowlist", "shell tool disabled")
	}
	classified := shell.ClassifiedCommands()
	unclassified := slices.DeleteFunc(slices.Clone(cfg.AllowedCommands), func(command string) bool {
		return slices.Contains(classified, command)
	})
	if len(unclassified) > 0 {
		return checkResult{"shell allowlist", "warn", "no tier rule for: " + strings.Join(unclassified, ", ") + " — these prompt for approval on every call",
			"remove them from shell.allowed_commands, or add a commandRules row (a security review) in tools/system/shell/rules.go"}
	}
	missing := shell.MissingProjectCommands(root, cfg.AllowedCommands)
	if len(missing) > 0 {
		suggested := append(append([]string(nil), cfg.AllowedCommands...), missing...)
		return checkResult{"shell allowlist", "warn", "this project type also uses: " + strings.Join(missing, ", "),
			`paste into config: "shell": {"allowed_commands": ["` + strings.Join(suggested, `", "`) + `"]}`}
	}
	return ok("shell allowlist", "covers the detected project type")
}

// languageServerHints maps a known server command to the way its own ecosystem
// installs it. Invokr installs nothing itself — these are the commands the user
// would run anyway — and a server they configured themselves needs no hint.
var languageServerHints = map[string]string{
	"gopls": "go install golang.org/x/tools/gopls@latest", "rust-analyzer": "rustup component add rust-analyzer",
	"pyright-langserver": "npm i -g pyright", "typescript-language-server": "npm i -g typescript-language-server typescript"}

// checkLanguageServers explains the one way the lsp tools fail silently. They
// are registered only when a server is on PATH, so with none installed the
// model simply has no code-navigation tools and nothing anywhere says why.
func checkLanguageServers(c config.LSPConfig) checkResult {
	if !c.IsEnabled() {
		return ok("lsp", "disabled by config")
	}
	var found, missing []string
	for _, server := range lspsystem.Servers(c) {
		if _, err := exec.LookPath(server.Command); err != nil {
			missing = append(missing, server.Command)
			continue
		}
		found = append(found, server.Command)
	}
	slices.Sort(found)
	slices.Sort(missing)
	if len(found) > 0 {
		return ok("lsp", strings.Join(found, ", ")+" resolvable"+notInstalled(missing))
	}
	return checkResult{"lsp", "warn", "no language server on PATH; the lsp_* tools are not registered", "install one for the language you work in — " + strings.Join(installHints(missing), "; ")}
}

func notInstalled(missing []string) string {
	if len(missing) == 0 {
		return ""
	}
	return " (not installed: " + strings.Join(missing, ", ") + ")"
}

func installHints(missing []string) []string {
	return toolcore.MapSlice(missing, func(command string) string {
		if hint, ok := languageServerHints[command]; ok {
			return command + ": " + hint
		}
		return command
	})
}
