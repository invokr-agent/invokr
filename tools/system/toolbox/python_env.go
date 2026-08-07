package toolbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/core/sandbox"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
)

var venvMuMap sync.Map // map[string]*sync.Mutex — one mutex per venv dir

func preparePythonPluginEnvLocked(ctx context.Context, p *executableTool, env []string) (string, []string, error) {
	venvDir := pythonVenvDir(p.dir, p.config)
	mu, _ := venvMuMap.LoadOrStore(venvDir, &sync.Mutex{})
	mu.(*sync.Mutex).Lock() //nolint:forcetypeassert // only *sync.Mutex is ever stored
	defer mu.(*sync.Mutex).Unlock()
	if err := p.approvePreparation(ctx); err != nil {
		return "", nil, err
	}
	return preparePythonPluginEnv(ctx, p, env)
}

func preparePythonPluginEnv(ctx context.Context, p *executableTool, env []string) (string, []string, error) {
	pluginDir, cfg := p.dir, p.config
	reqPath := filepath.Join(pluginDir, "requirements.txt")
	if _, err := os.Stat(reqPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg.PythonBin, env, nil
		}
		return "", nil, fmt.Errorf("stat requirements: %w", err)
	}

	venvDir := pythonVenvDir(pluginDir, cfg)
	pythonBin := filepath.Join(venvDir, "bin", "python")
	if os.PathSeparator == '\\' {
		pythonBin = filepath.Join(venvDir, "Scripts", "python.exe")
	}
	if _, err := os.Stat(pythonBin); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return "", nil, fmt.Errorf("stat venv python: %w", err)
		}
		if err := createPythonVenv(ctx, venvDir, env, cfg); err != nil {
			return "", nil, err
		}
	}

	if err := p.installPythonRequirementsIfNeeded(ctx, pythonBin, reqPath, venvDir, env); err != nil {
		return "", nil, err
	}

	env = core.WithEnvValue(env, "VIRTUAL_ENV", venvDir)
	env = core.WithEnvValue(env, "PATH", filepath.Dir(pythonBin)+string(os.PathListSeparator)+core.GetEnvValue(env, "PATH"))
	return pythonBin, env, nil
}

// pythonVenvDir returns the venv for a toolbox tool. Each tool gets its own
// venv under its own directory: agent-authored tools' requirements are
// uncoordinated, so a venv shared across tools would silently couple
// conflicting pins. cfg.VenvDir is an explicit operator override that forces
// every tool back onto one shared venv.
func pythonVenvDir(pluginDir string, cfg config.ToolboxConfig) string {
	if cfg.VenvDir != "" {
		return cfg.VenvDir
	}
	return filepath.Join(pluginDir, ".venv")
}

func createPythonVenv(ctx context.Context, venvDir string, env []string, cfg config.ToolboxConfig) error {
	// Pre-created (rather than left for "venv" to create) so the sandbox
	// plan's dedupExistingPaths sees it and bind-mounts it read-write; venv
	// accepts an already-existing empty target directory.
	if err := os.MkdirAll(venvDir, 0700); err != nil {
		return fmt.Errorf("create venv dir: %w", err)
	}
	// CPython's venv module re-derives sys.executable from its own argv0 and
	// falls back to searching PATH; with the host PATH isolated from the
	// child env (the IsolateHostEnv default), a bare "python3" can't be
	// resolved, so it must be an absolute path here.
	pythonBin, err := resolveAbsoluteCommandPath(cfg.PythonBin)
	if err != nil {
		return fmt.Errorf("resolve python interpreter %q: %w", cfg.PythonBin, err)
	}
	cmd, cleanup, err := pythonSandboxCommand(ctx, cfg, pythonBin, []string{"-m", "venv", venvDir}, filepath.Dir(venvDir), venvDir)
	if err != nil {
		return fmt.Errorf("sandbox python venv creation: %w", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	cmd.Env = env
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("create venv: %s", joinNonEmpty([]string{out.String(), err.Error()}, "\n"))
	}
	return nil
}

// pythonSandboxCommand confines Python venv creation / pip install to the
// venv directory (read-write) plus the requirements/venv-parent directory
// (read-only), with network allowed — install-time code (setup.py, build
// backends, post-install hooks) thus runs FS-confined and cannot touch the
// rest of the host, even though it can reach the package index. Tool
// *execution* keeps running network-off via buildSandboxedCommand.
func pythonSandboxCommand(ctx context.Context, cfg config.ToolboxConfig, pythonBin string, args []string, roDir string, venvDir string) (*exec.Cmd, func(), error) {
	if approval.ModeFromContext(ctx) == approval.YOLO {
		return exec.CommandContext(ctx, pythonBin, args...), nil, nil
	}
	if !sandbox.Available() {
		if !cfg.AllowUnsandboxed {
			return nil, nil, fmt.Errorf("python install requires namespace sandboxing; set INVOKR_TOOLBOX_ALLOW_UNSANDBOXED=1 to bypass (not recommended)")
		}
		return exec.CommandContext(ctx, pythonBin, args...), nil, nil
	}
	resolvedPython, err := resolveAbsoluteCommandPath(pythonBin)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve python interpreter %q: %w", pythonBin, err)
	}
	plan := sandbox.BuildPlan(resolvedPython, roDir, "", venvDir, core.ToolCapabilities{Network: true, WritesWorkspace: true}, cfg.MemoryLimitBytes, nil)
	return sandbox.Command(ctx, plan, roDir, args)
}

// readValidatedRequirements reads reqPath and runs the M3 parser/policy
// gates (pinning, VCS/URL/path rejection, allow/deny) before any pip
// command is built. A nil reqBytes with a nil error means the file does
// not exist and there is nothing to install.
func readValidatedRequirements(reqPath string, cfg config.ToolboxConfig) ([]byte, []string, string, error) {
	info, err := os.Stat(reqPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, "", nil
		}
		return nil, nil, "", fmt.Errorf("stat requirements: %w", err)
	}
	if info.IsDir() {
		return nil, nil, "", fmt.Errorf("requirements path is a directory: %s", reqPath)
	}
	if cfg.VenvDir != "" && len(cfg.PackageAllow)+len(cfg.PackageDeny) > 0 {
		return nil, nil, "", fmt.Errorf("package policy requires per-tool environments; unset INVOKR_TOOLBOX_VENV_DIR")
	}

	reqBytes, err := os.ReadFile(reqPath)
	if err != nil {
		return nil, nil, "", fmt.Errorf("read requirements: %w", err)
	}
	names, err := parsePythonRequirementNames(reqBytes)
	if err != nil {
		return nil, nil, "", fmt.Errorf("invalid requirements: %w", err)
	}
	if err := enforcePackagePolicy(names, cfg.PackageAllow, cfg.PackageDeny); err != nil {
		return nil, nil, "", fmt.Errorf("requirements rejected: %w", err)
	}
	// v2 locks were resolved with --ignore-installed and contain the complete graph.
	return reqBytes, names, "v2:" + fmt.Sprintf("%x", sha256.Sum256(reqBytes)), nil
}

// requirementsMarkerPath keys the marker per requirements.txt when
// INVOKR_TOOLBOX_VENV_DIR forces a shared venv, so two plugins with
// different requirements don't overwrite each other's marker.
func requirementsMarkerPath(venvDir, reqPath string, cfg config.ToolboxConfig) string {
	if cfg.VenvDir == "" {
		return filepath.Join(venvDir, ".requirements.sha256")
	}
	// Hash the absolute path so the marker name doesn't change if the agent's
	// working directory changes between calls.
	if abs, err := filepath.Abs(reqPath); err == nil {
		reqPath = abs
	}
	return filepath.Join(venvDir, ".requirements."+fmt.Sprintf("%x", sha256.Sum256([]byte(reqPath)))+".sha256")
}

// requirementsCache retains the validated closure independently of the editable lock.
type requirementsCache struct {
	Hash     string
	Packages []string
}

func (c requirementsCache) checkPolicy(required []string, cfg config.ToolboxConfig) error {
	if len(required) > 0 && len(c.Packages) == 0 {
		return fmt.Errorf("cached environment lacks dependency graph; rebuild it")
	}
	return enforcePackagePolicy(c.Packages, cfg.PackageAllow, cfg.PackageDeny)
}

func readRequirementsCache(path string) (requirementsCache, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return requirementsCache{}, false, nil
	}
	var cache requirementsCache
	// Legacy hashes and malformed metadata cannot authorize reuse. Re-resolve.
	if json.Unmarshal(data, &cache) != nil {
		cache = requirementsCache{}
	}
	return cache, true, err
}

// rebuildVenvIfNotShared deletes and recreates venvDir before a requirements
// reinstall: pip install is additive, so a changed requirements.txt (e.g. a
// dependency removed) would otherwise leave the old package importable. A
// shared venv (INVOKR_TOOLBOX_VENV_DIR) is never deleted, since other
// plugins' deps live in it too; it falls through to the additive pip install.
func rebuildVenvIfNotShared(ctx context.Context, venvDir string, env []string, cfg config.ToolboxConfig) error {
	if cfg.VenvDir != "" {
		return nil
	}
	if err := os.RemoveAll(venvDir); err != nil {
		return fmt.Errorf("remove stale venv: %w", err)
	}
	return createPythonVenv(ctx, venvDir, env, cfg)
}

func (p *executableTool) installPythonRequirementsIfNeeded(ctx context.Context, pythonBin, reqPath, venvDir string, env []string) error {
	reqBytes, names, hash, err := readValidatedRequirements(reqPath, p.config)
	if err != nil || reqBytes == nil {
		return err
	}

	markerPath := requirementsMarkerPath(venvDir, reqPath, p.config)
	cache, rebuild, err := readRequirementsCache(markerPath)
	if err != nil {
		return err
	}
	if cache.Hash == hash {
		return cache.checkPolicy(names, p.config)
	}
	cache.Packages = nil
	if rebuild {
		if err := rebuildVenvIfNotShared(ctx, venvDir, env, p.config); err != nil {
			return err
		}
	}
	if len(names) > 0 {
		cache.Packages, err = p.secureInstallPythonRequirements(ctx, pythonBin, reqPath, venvDir, env)
		if err != nil {
			return err
		}
	}
	cache.Hash = hash
	data, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	if err := os.WriteFile(markerPath, data, 0600); err != nil {
		return fmt.Errorf("write requirements marker: %w", err)
	}
	return nil
}
