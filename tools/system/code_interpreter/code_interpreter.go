package code_interpreter

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/config"
	"github.com/invokr-ai/invokr/constants"
	"github.com/invokr-ai/invokr/platform/events"
	"github.com/invokr-ai/invokr/platform/turn"
	"github.com/invokr-ai/invokr/support/fsutil"
	"github.com/invokr-ai/invokr/support/mathutil"
	"github.com/invokr-ai/invokr/support/strutil"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/core/sandbox"
	"github.com/invokr-ai/invokr/tools/system/workspace"
)

type Args struct {
	Code        string   `json:"code" desc:"Python code to run."`
	InputFiles  []string `json:"input_files,omitempty" desc:"Workspace-relative or approved-root files to stage as read-only inputs."`
	OutputFiles []string `json:"output_files,omitempty" desc:"Workspace-relative destinations to copy back after the run. Write each one into os.environ['INVOKR_CI_ARTIFACT_DIR'] using its base name, e.g. output_files [\"organization.json\"] means writing <INVOKR_CI_ARTIFACT_DIR>/organization.json."`
	TimeoutMS   int      `json:"timeout_ms,omitempty" desc:"Optional timeout override capped by config."`
}

type Artifact struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	SizeBytes int64  `json:"size_bytes"`
	MimeType  string `json:"mime_type"`
	Preview   string `json:"preview,omitempty"`
}

type Result struct {
	Stdout    string     `json:"stdout"`
	Stderr    string     `json:"stderr"`
	ExitCode  int        `json:"exit_code"`
	TimedOut  bool       `json:"timed_out"`
	Artifacts []Artifact `json:"artifacts"`
	// Written names the workspace files copied back out of artifacts/, and
	// Missing the requested ones the run never produced. Missing is reported
	// rather than raised: the run itself succeeded, and the model needs to see
	// which name it failed to write.
	Written []string `json:"written_files,omitempty"`
	Missing []string `json:"missing_files,omitempty"`
}

// resolvedOutput is one requested write-back: the artifact base name the code
// is expected to produce, and the workspace path it lands at.
type resolvedOutput struct {
	Requested, Artifact, AbsPath string
}

type prepared struct {
	args                                        Args
	cfg                                         config.CodeInterpreterConfig
	codeHash, sessionDir, inputDir, artifactDir string
	inputs                                      []resolvedInput
	outputs                                     []resolvedOutput
	// roots are the workspace roots, mounted read-only inside both backends;
	// the first one is the interpreter's working directory.
	roots, tiers, advisories, protected []string
	timeoutMS                           int
}

type resolvedInput struct {
	Requested, AbsPath, RelPath string
}

// Backend runs one prepared call; the two implementations are runInProcess
// and runInContainer.
type Backend func(context.Context, prepared) (Result, error)

type Service struct {
	cfg        config.CodeInterpreterConfig
	workspace  workspace.Policy
	dispatcher events.Dispatcher
	backend    Backend
	now        func() time.Time
}

func RegisterCodeInterpreterTool(registrar core.NativeRegistrar, deps core.CallerDeps, wsp workspace.Policy) {
	if deps.Policy != nil && deps.Policy.Mode() == approval.YOLO {
		deps.App.CodeInterpreter.Backend = constants.CodeInterpreterBackendProcess
	}
	service := NewService(deps.App.CodeInterpreter, wsp, deps.Dispatcher)
	cfg := service.cfg
	core.Register(registrar, core.Tool[prepared]{
		Spec: core.ToolSpec{
			Name:        constants.CodeInterpreterToolName,
			Description: "Run Python analysis code with artifacts and time limits. Invokr prefers an available container engine by default. Source-token checks are advisory only; the process backend runs in an unprivileged user-namespace sandbox. The working directory is the workspace root, mounted read-only: open workspace files by their usual relative path. The only writable place is the artifacts directory, whose absolute path is in the INVOKR_CI_ARTIFACT_DIR environment variable; /tmp is private to the run and discarded afterwards. A /tmp path in input_files or output_files names the scratch directory the shell tool also sees as /tmp, so a file the two tools exchange can live there. Code that prints nothing answers with stdout \"(no output)\". To produce a workspace file, write it under INVOKR_CI_ARTIFACT_DIR and name it in output_files: it is copied back after the run.",
			Example:     `{"name":"code_interpreter","arguments":{"code":"open('artifacts/out.json','w').write('{}')","input_files":["data/input.csv"],"output_files":["out.json"],"timeout_ms":30000}}`,
			Parameters:  core.SchemaFor[Args](),
			Metadata:    core.NativeMetadata("system", metadataTiers(cfg), constants.ApprovalPolicyAsk, core.ToolCapabilities{ReadsWorkspace: true, WritesWorkspace: true, Network: cfg.ContainerNetwork, ExecutesCode: true}),
		},
		Decode: func(call *core.ToolCall) (prepared, error) {
			args, err := core.DecodeOptionalToolArgs[Args](call)
			if err != nil {
				return prepared{}, fmt.Errorf("parse code_interpreter arguments: %w", err)
			}
			return service.Prepare(args)
		},
		Prepare: func(ctx context.Context, p prepared) (prepared, error) {
			if approval.ModeFromContext(ctx) == approval.Auto {
				p.cfg.ContainerNetwork = true
				p.protected = wsp.ProtectedPaths()
			}
			return p, nil
		},
		Approve: func(ctx context.Context, p prepared) core.ApprovalDecision {
			decision := approvePrepared(p)
			if req := decision.Request; req != nil {
				req.Subject = "sha256:" + p.codeHash
				// The hash is what a grant matches; the person approving reads the code.
				req.GrantPattern = req.Subject
				first, _, _ := strings.Cut(strings.TrimSpace(p.args.Code), "\n")
				req.Summary = "Run Python: " + first
				req.Details = "Code:\n" + p.args.Code + "\n\n" + req.Details
				req.Paths = core.MapSlice(p.outputs, func(out resolvedOutput) string { return wsp.DisplayPath(out.AbsPath) })
				req.Confined = p.cfg.Backend == constants.CodeInterpreterBackendContainer || sandbox.Available()
				req.Network = p.cfg.ContainerNetwork
				req.Writes = slices.ContainsFunc(p.outputs, func(out resolvedOutput) bool {
					_, scratch := fsutil.RelInside(fsutil.SharedTmpDir(wsp.Root), out.AbsPath)
					return !scratch || slices.ContainsFunc(p.roots, func(root string) bool { return workspace.Overlaps(root, out.AbsPath) })
				})
				req.Protected = slices.ContainsFunc(p.outputs, func(out resolvedOutput) bool { return wsp.IsProtected(out.AbsPath) })
				if approval.ModeFromContext(ctx) == approval.Plan && slices.ContainsFunc(p.roots, func(root string) bool { return workspace.Overlaps(root, p.sessionDir) }) {
					return core.Deny("Plan artifacts must be outside source roots")
				}
			}
			return decision
		},
		Approver: deps.Approver,
		Execute: func(ctx context.Context, p prepared) (string, error) {
			return service.Run(ctx, p)
		},
		AuditFailure: func(ctx context.Context, p prepared, err error) {
			service.audit(ctx, p, Result{}, "denied", err, 0)
		},
	})
}

func NewService(cfg config.CodeInterpreterConfig, wsp workspace.Policy, dispatcher events.Dispatcher) *Service {
	cfg = preferContainerBackend(cfg)
	cfg = config.AppConfig{CodeInterpreter: cfg}.WithDefaults().CodeInterpreter
	s := &Service{cfg: cfg, workspace: wsp, dispatcher: dispatcher, now: func() time.Time { return time.Now().UTC() }, backend: runInProcess}
	if cfg.Backend == constants.CodeInterpreterBackendContainer {
		s.backend = runInContainer
	}
	return s
}

func (s *Service) Prepare(args Args) (prepared, error) {
	args.Code = strings.TrimSpace(args.Code)
	if args.Code == "" {
		return prepared{}, fmt.Errorf("code is required")
	}
	if err := validateBackend(s.cfg); err != nil {
		return prepared{}, err
	}
	advisories := preflightAdvisories(args.Code, s.cfg)
	inputs, err := resolveInputs(args.InputFiles, s.workspace)
	if err != nil {
		return prepared{}, err
	}
	outputs, err := resolveOutputs(args.OutputFiles, s.workspace)
	if err != nil {
		return prepared{}, err
	}
	timeoutMS := mathutil.ClampInt(args.TimeoutMS, min(s.cfg.DefaultTimeoutMS, s.cfg.MaxTimeoutMS), s.cfg.MaxTimeoutMS)
	codeHash := strutil.HashHex(args.Code)
	sessionDir := filepath.Join(s.cfg.SessionDir, codeHash[:12])
	return prepared{args: args, cfg: s.cfg, codeHash: codeHash, sessionDir: sessionDir, inputDir: filepath.Join(sessionDir, "inputs"), artifactDir: filepath.Join(sessionDir, "artifacts"), inputs: inputs, outputs: outputs, roots: s.workspace.Roots(), timeoutMS: timeoutMS, tiers: metadataTiers(s.cfg), advisories: advisories}, nil
}

func (s *Service) Run(ctx context.Context, p prepared) (payload string, err error) {
	started := s.now()
	var result Result
	defer func() {
		s.audit(ctx, p, result, core.Outcome(err), err, s.now().Sub(started))
	}()

	result, err = s.backend(ctx, p)
	if err != nil {
		return "", err
	}
	// The sandbox boundary is unchanged: the interpreter still never sees the
	// workspace. Copying named artifacts back out afterwards is the host's
	// decision, taken under the same workspace policy every other writer uses,
	// and only for paths the approval card already named.
	if result.ExitCode == 0 {
		if result.Written, result.Missing, err = copyOutputs(p); err != nil {
			return "", err
		}
	}
	return core.MarshalResult("code_interpreter", result)
}

func (s *Service) audit(ctx context.Context, p prepared, result Result, outcome string, err error, duration time.Duration) {
	if s.dispatcher == nil {
		return
	}
	paths := core.MapSlice(p.inputs, func(in resolvedInput) string { return filepath.ToSlash(in.RelPath) })
	details := map[string]any{"backend": p.cfg.Backend, "policy_tiers": p.tiers, "code_hash": p.codeHash, "input_file_count": len(p.inputs), "artifact_count": len(result.Artifacts), "timed_out": result.TimedOut, "exit_code": result.ExitCode, "duration_ms": duration.Milliseconds(), "success": err == nil, "network_enabled": p.cfg.ContainerNetwork, "preflight_advisories": p.advisories}
	action := "code_interpreter"
	if p.cfg.Backend == constants.CodeInterpreterBackendContainer {
		details["image"] = p.cfg.ContainerImage
		action = "code_interpreter_container"
	}
	core.DispatchAudit(ctx, s.dispatcher, &events.AuditPayload{Action: action, Tool: constants.CodeInterpreterToolName, Paths: paths, Outcome: outcome, Details: details}, err)
}

// metadataTiers lists read_workspace because both backends mount the
// workspace roots read-only and start in the primary root, so code reads any
// workspace file by relative path; writes still land only in the session
// artifacts directory.
func metadataTiers(cfg config.CodeInterpreterConfig) []string {
	tiers := []string{constants.CodeInterpreterTierCodeExecution, constants.CodeInterpreterTierReadWorkspace, constants.CodeInterpreterTierArtifactWrite}
	if cfg.ContainerNetwork {
		tiers = append(tiers, constants.CodeInterpreterTierNetworkCommand)
	}
	return tiers
}

func approvePrepared(p prepared) core.ApprovalDecision {
	if err := validateBackend(p.cfg); err != nil {
		return core.Deny(err.Error())
	}
	// This Ask is not a dead end in an unattended run: `exec --allow-all`
	// approves every request outright, and a per-tier run opts in with
	// `--allow network_command`. It stays an Ask because container networking
	// is the one posture that lets model-written Python reach the internet
	// (#543).
	tier := strings.Join(p.tiers, ", ")
	if p.cfg.ContainerNetwork {
		tier = constants.CodeInterpreterTierNetworkCommand
	}
	return core.Ask(approval.Request{
		Tool: constants.CodeInterpreterToolName, Tier: tier,
		Reason:  "code_interpreter executes Python and writes session artifacts",
		Summary: "Run Python code interpreter",
		Details: fmt.Sprintf("Backend: %s (%s)\nNetwork: %t\nInputs: %d\nWrites back: %s\nTimeout: %dms\nAdvisories: %s", p.cfg.Backend, confinement(p.cfg), p.cfg.ContainerNetwork, len(p.inputs), outputSummary(p.outputs), p.timeoutMS, strings.Join(p.advisories, ", ")),
	})
}

// outputSummary lists the workspace paths a run may overwrite, for the
// approval card. Write-back is the one way this tool touches the workspace, so
// the card has to name the paths rather than a count.
func outputSummary(outputs []resolvedOutput) string {
	if len(outputs) == 0 {
		return "nothing (artifacts stay in the session directory)"
	}
	return strings.Join(core.MapSlice(outputs, func(o resolvedOutput) string { return o.Requested }), ", ")
}

// confinement describes, for the approval card, where the chosen backend can
// write — the card lists artifact_write/read_workspace, so it must not read as
// a bound the backend does not enforce (issue #298).
func confinement(cfg config.CodeInterpreterConfig) string {
	switch {
	case cfg.Backend == constants.CodeInterpreterBackendContainer:
		return "container-isolated"
	case sandbox.Available():
		return "sandboxed; writes confined to the session directory"
	default:
		return "UNSANDBOXED: runs on the host"
	}
}

func validateBackend(cfg config.CodeInterpreterConfig) error {
	switch cfg.Backend {
	case constants.CodeInterpreterBackendProcess, constants.CodeInterpreterBackendContainer:
		return nil
	default:
		return fmt.Errorf("unsupported code_interpreter backend %q", cfg.Backend)
	}
}

// workspaceTmpPath maps "/tmp" and "/tmp/…" onto the workspace directory the
// shell tool mounts at /tmp (constants.ShellTmpDirName), leaving every other
// path alone. The model writes a scratch file with shell and then names
// "/tmp/solve.py" here, which was refused as outside the workspace. The second
// result marks a mapped path, whose hidden directory component is by
// construction, not the model asking for a dotfile.
func workspaceTmpPath(path, root string) (string, bool) {
	path = filepath.Clean(path)
	if root == "" || (path != "/tmp" && !strings.HasPrefix(path, "/tmp/")) {
		return path, false
	}
	return filepath.Join(fsutil.SharedTmpDir(root), strings.TrimPrefix(path, "/tmp")), true
}

// resolveTmpAware resolves one requested path through the workspace policy,
// except for a /tmp path: that maps to the shared scratch dir outside the
// workspace, which resolve would refuse, and whose hidden component is by
// construction rather than the model asking for a dotfile.
func resolveTmpAware(path string, wsp workspace.Policy, resolve func(string) (string, error)) (string, error) {
	mapped, isTmp := workspaceTmpPath(path, wsp.Root)
	if isTmp {
		resolved, err := workspace.ResolveSymlinkPrefix(mapped)
		if err != nil {
			return "", err
		}
		if _, inside := fsutil.RelInside(fsutil.SharedTmpDir(wsp.Root), resolved); !inside {
			return "", fmt.Errorf("scratch path escapes shared temporary directory")
		}
		return resolved, nil
	}
	abs, err := resolve(mapped)
	if err != nil {
		return "", err
	}
	if !wsp.AllowsHiddenPath(wsp.DisplayPath(abs)) {
		return "", fmt.Errorf("path is not allowed: %s", path)
	}
	return abs, nil
}

// resolveEach resolves each non-blank path through resolve and turns it into
// one entry via build, stopping at the first failure.
func resolveEach[T any](paths []string, wsp workspace.Policy, resolve func(string) (string, error), build func(path, abs string) (T, error)) ([]T, error) {
	out := make([]T, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		abs, err := resolveTmpAware(path, wsp, resolve)
		if err != nil {
			return nil, err
		}
		item, err := build(path, abs)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func resolveInputs(paths []string, wsp workspace.Policy) ([]resolvedInput, error) {
	return resolveEach(paths, wsp, wsp.Resolve, func(path, abs string) (resolvedInput, error) {
		info, err := os.Stat(abs)
		if err != nil {
			return resolvedInput{}, fmt.Errorf("stat input %q: %w", path, err)
		}
		if info.IsDir() {
			return resolvedInput{}, fmt.Errorf("input path is a directory: %s", path)
		}
		return resolvedInput{Requested: path, AbsPath: abs, RelPath: relInputPath(path, abs, wsp)}, nil
	})
}

// resolveOutputs validates the requested write-back destinations against the
// workspace policy up front, so an unwritable path is refused before any code
// runs rather than after.
func resolveOutputs(paths []string, wsp workspace.Policy) ([]resolvedOutput, error) {
	return resolveEach(paths, wsp, wsp.ResolveWrite, func(path, abs string) (resolvedOutput, error) {
		return resolvedOutput{Requested: path, Artifact: filepath.Base(filepath.Clean(path)), AbsPath: abs}, nil
	})
}

// copyOutputs moves the named artifacts into the workspace after a successful
// run, reporting what landed and what the code never wrote.
func copyOutputs(p prepared) (written, missing []string, err error) {
	if len(p.outputs) == 0 {
		return nil, nil, nil
	}
	files, err := os.OpenRoot(p.sessionDir)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = files.Close() }()
	for _, out := range p.outputs {
		src := filepath.Join("artifacts", out.Artifact)
		info, statErr := files.Stat(src)
		if statErr != nil || info.IsDir() {
			missing = append(missing, out.Requested)
			continue
		}
		data, readErr := files.ReadFile(src)
		if readErr != nil {
			return nil, nil, fmt.Errorf("read artifact %q: %w", out.Artifact, readErr)
		}
		if target, resolveErr := workspace.ResolveSymlinkPrefix(out.AbsPath); resolveErr != nil || target != out.AbsPath {
			return nil, nil, fmt.Errorf("output destination changed after approval: %s", out.Requested)
		}
		if mkErr := os.MkdirAll(filepath.Dir(out.AbsPath), 0o755); mkErr != nil {
			return nil, nil, fmt.Errorf("create output directory for %q: %w", out.Requested, mkErr)
		}
		if writeErr := os.WriteFile(out.AbsPath, data, 0o644); writeErr != nil {
			return nil, nil, fmt.Errorf("write output %q: %w", out.Requested, writeErr)
		}
		written = append(written, out.Requested)
	}
	return written, missing, nil
}

func relInputPath(requested string, abs string, wsp workspace.Policy) string {
	if !filepath.IsAbs(requested) {
		return filepath.Clean(requested)
	}
	for _, root := range wsp.Roots() {
		if rel, ok := fsutil.RelInside(root, abs); ok {
			return rel
		}
	}
	return filepath.Base(abs)
}

func preflightAdvisories(code string, cfg config.CodeInterpreterConfig) []string {
	blocked := []string{"import subprocess", "from subprocess", "import socket", "from socket", "import requests", "from requests", "import urllib", "from urllib", "import pip", "from pip", "ensurepip", "import venv", "from venv", "os.system", "os.popen", "os.spawn", "os.exec", "__import__('subprocess'", `__import__("subprocess"`, `importlib.import_module("subprocess"`}
	if !cfg.AllowEnv {
		blocked = append(blocked, "os.environ", "environ[", "getenv(")
	}
	var found []string
	for _, token := range blocked {
		if strings.Contains(code, token) {
			found = append(found, token)
		}
	}
	return found
}

// containerEngines are the supported container CLIs, in auto-detection order.
var containerEngines = []string{"docker", "podman"}

func preferContainerBackend(cfg config.CodeInterpreterConfig) config.CodeInterpreterConfig {
	if strings.TrimSpace(cfg.Backend) != "" {
		return cfg
	}
	// LookPath rejects the empty name, so an unset ContainerEngine falls
	// through to the defaults without a check of its own.
	for _, engine := range append([]string{strings.TrimSpace(cfg.ContainerEngine)}, containerEngines...) {
		if _, err := exec.LookPath(engine); err == nil {
			cfg.Backend = constants.CodeInterpreterBackendContainer
			cfg.ContainerEngine = engine
			return cfg
		}
	}
	return cfg
}

func runInProcess(ctx context.Context, p prepared) (Result, error) {
	scriptPath, err := stageScript(p)
	if err != nil {
		return Result{}, err
	}
	spec := core.ProcessSpec{Name: p.cfg.PythonBin, Args: []string{scriptPath}, Dir: p.workDir(), Env: pythonEnv(p)}
	switch {
	case approval.ModeFromContext(ctx) == approval.YOLO:
	case sandbox.Available():
		spec.Build = func(ctx context.Context) (*exec.Cmd, func() error, error) { return sandboxPythonCommand(ctx, p) }
	case !sandbox.AllowUnsandboxed():
		return Result{}, fmt.Errorf("code_interpreter process backend requires namespace sandboxing, which is unavailable on this host; use backend: container, or set %s=1 to run Python directly on the host (not recommended)", sandbox.AllowUnsandboxedEnv)
	}
	return runSpecAndCollect(ctx, spec, p, "run python")
}

// workDir is the interpreter's working directory: the primary workspace root,
// so code opens workspace files by the same relative paths the shell tool
// uses, or the session dir when no workspace root exists.
func (p prepared) workDir() string {
	if len(p.roots) > 0 {
		if info, err := os.Stat(p.roots[0]); err == nil && info.IsDir() {
			return p.roots[0]
		}
	}
	return p.sessionDir
}

// sandboxPythonCommand confines the process backend to the same unprivileged
// user-namespace sandbox toolbox tools run in: the workspace roots are
// mounted read-only with the primary root as cwd, the session directory
// (script, staged inputs, artifacts) is the only writable path, the rest of
// the host is absent, /tmp is a private tmpfs, and networking is off. Without
// it interpreted code writes anywhere the agent's uid can (issue #298).
func sandboxPythonCommand(ctx context.Context, p prepared) (*exec.Cmd, func() error, error) {
	pythonPath, err := exec.LookPath(p.cfg.PythonBin)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve python interpreter %q: %w", p.cfg.PythonBin, err)
	}
	// The session dir is bind-mounted at its own absolute path and cd'd into
	// after pivot_root, where a relative path would no longer resolve.
	sessionDir, err := filepath.Abs(p.sessionDir)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve session directory %q: %w", p.sessionDir, err)
	}
	plan := sandbox.BuildPlan(pythonPath, "", "", "", core.ToolCapabilities{Network: p.cfg.ContainerNetwork}, p.cfg.MemoryLimitBytes, nil)
	// Roots go on RO before the session dir goes on RW: binds apply in that
	// order, so a session dir living under a root stays writable.
	plan.RO = append(plan.RO, p.roots...)
	plan.RW = append(plan.RW, sessionDir)
	plan.Sealed = p.protected
	plan = sandbox.DedupPlan(plan)
	return sandbox.ProcessSpec(plan, p.workDir(), []string{filepath.Join(sessionDir, "main.py")}, true).Build(ctx)
}

// stageScript resets the session directories and writes the code to run.
func stageScript(p prepared) (string, error) {
	if err := prepareDirs(p); err != nil {
		return "", err
	}
	scriptPath := filepath.Join(p.sessionDir, "main.py")
	if err := os.WriteFile(scriptPath, []byte(p.args.Code), 0600); err != nil {
		return "", fmt.Errorf("write code script: %w", err)
	}
	return scriptPath, nil
}

func prepareDirs(p prepared) error {
	if err := os.RemoveAll(p.inputDir); err != nil {
		return fmt.Errorf("reset input dir: %w", err)
	}
	if err := os.MkdirAll(p.inputDir, 0700); err != nil {
		return fmt.Errorf("create input dir: %w", err)
	}
	if err := os.MkdirAll(p.artifactDir, 0700); err != nil {
		return fmt.Errorf("create artifact dir: %w", err)
	}
	for _, input := range p.inputs {
		dst := filepath.Join(p.inputDir, filepath.Clean(input.RelPath))
		if !strings.HasPrefix(dst, p.inputDir+string(os.PathSeparator)) {
			return fmt.Errorf("invalid staged input path: %s", input.RelPath)
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0700); err != nil {
			return fmt.Errorf("create staged input dir: %w", err)
		}
		if err := fsutil.CopyFile(input.AbsPath, dst, 0o600); err != nil {
			return err
		}
		_ = os.Chmod(dst, 0400)
	}
	return nil
}

func pythonEnv(p prepared) []string {
	env := []string{"PYTHONNOUSERSITE=1", "PYTHONPATH=", "PATH=" + os.Getenv("PATH"), "INVOKR_CI_INPUT_DIR=" + p.inputDir, "INVOKR_CI_ARTIFACT_DIR=" + p.artifactDir, "INVOKR_CI_SESSION_DIR=" + p.sessionDir}
	if p.cfg.AllowEnv {
		env = append(os.Environ(), env...)
	}
	return env
}

func collectArtifacts(dir string, maxBytes int) ([]Artifact, error) {
	var artifacts []Artifact
	err := filepath.WalkDir(dir, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.Size() > int64(maxBytes) {
			return fmt.Errorf("artifact %s exceeds max size", entry.Name())
		}
		rel, _ := filepath.Rel(dir, path)
		artifact := Artifact{
			Path: "session://artifacts/" + filepath.ToSlash(rel), Name: entry.Name(),
			SizeBytes: info.Size(), MimeType: detectMime(path),
		}
		if strings.HasPrefix(artifact.MimeType, "text/") || artifact.MimeType == "application/json" {
			artifact.Preview = artifactPreview(path, 4096)
		}
		artifacts = append(artifacts, artifact)
		return nil
	})
	slices.SortFunc(artifacts, func(a, b Artifact) int { return cmp.Compare(a.Path, b.Path) })
	return artifacts, err
}

func detectMime(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 512)
	n, _ := f.Read(buf)
	return http.DetectContentType(buf[:n])
}

func artifactPreview(path string, limit int) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	if len(data) > limit {
		data = data[:limit]
	}
	return string(data)
}

func runInContainer(ctx context.Context, p prepared) (Result, error) {
	if _, err := stageScript(p); err != nil {
		return Result{}, err
	}
	argv, err := BuildContainerArgv(p)
	if err != nil {
		return Result{}, err
	}
	return runSpecAndCollect(ctx, core.ProcessSpec{Name: argv[0], Args: argv[1:]}, p, "run container")
}

func runSpecAndCollect(ctx context.Context, spec core.ProcessSpec, p prepared, label string) (Result, error) {
	spec.OutputLimit = p.cfg.MaxOutputBytes
	if spec.OutputLimit <= 0 {
		spec.OutputLimit = constants.CodeInterpreterMaxOutputBytes
	}
	spec.Timeout = time.Duration(p.timeoutMS) * time.Millisecond
	res, err := core.RunProcess(ctx, spec)
	stdout, stderr := trimOutput(res.Stdout, p.cfg.MaxOutputBytes), trimOutput(res.Stderr, p.cfg.MaxOutputBytes)
	if stdout == "" && stderr == "" {
		// Say it, rather than leaving an empty field the model reads as a
		// truncation or a broken tool and retries.
		stdout = constants.ShellNoOutputMarker
	}
	r := Result{Stdout: stdout, Stderr: stderr, ExitCode: res.ExitCode, TimedOut: res.TimedOut}
	if err != nil {
		if _, ok := errors.AsType[*exec.ExitError](err); !ok && !res.TimedOut {
			return r, fmt.Errorf("%s: %w", label, err)
		}
	}
	events.RecordExecution(ctx, turn.ExecutionEvidence{Command: spec.Name + " " + strings.Join(spec.Args, " "), Cwd: spec.Dir, ExitCode: new(res.ExitCode)})
	r.Artifacts, err = collectArtifacts(p.artifactDir, p.cfg.MaxArtifactBytes)
	return r, err
}

func trimOutput(text string, maxBytes int) string {
	text = strings.TrimSpace(text)
	if maxBytes <= 0 {
		return text
	}
	return strutil.TruncateUTF8(text, maxBytes)
}

func BuildContainerArgv(p prepared) ([]string, error) {
	engine := cmp.Or(strings.TrimSpace(p.cfg.ContainerEngine), "docker")
	if !slices.Contains(containerEngines, engine) {
		return nil, fmt.Errorf("unsupported container engine %q", engine)
	}
	if strings.TrimSpace(p.cfg.ContainerImage) == "" {
		return nil, fmt.Errorf("container image is required")
	}
	argv := []string{
		engine, "run", "--rm",
		// Mounted under /work, the working directory, so that "inputs/x" and
		// "artifacts/y" mean the same thing in both backends. The process
		// backend runs with the session dir as cwd and these as real
		// subdirectories; a container that put them at the filesystem root
		// would silently drop every relative write the tool description asks
		// the model to make.
		"-v", p.inputDir + ":/work/inputs:ro",
		"-v", p.artifactDir + ":/work/artifacts:rw",
		"-v", filepath.Join(p.sessionDir, "main.py") + ":/work/main.py:ro",
	}
	// The workspace roots are visible read-only at their host paths, with
	// the primary root as cwd, matching the process backend.
	for _, root := range p.roots {
		argv = append(argv, "-v", root+":"+root+":ro")
	}
	workDir := "/work"
	if len(p.roots) > 0 {
		workDir = p.roots[0]
	}
	argv = append(argv, "-w", workDir, "-e", "PYTHONNOUSERSITE=1", "-e", "PYTHONPATH=", "-e", "INVOKR_CI_INPUT_DIR=/work/inputs", "-e", "INVOKR_CI_ARTIFACT_DIR=/work/artifacts", "-e", "INVOKR_CI_SESSION_DIR=/work")
	if !p.cfg.ContainerNetwork {
		argv = append(argv, "--network=none")
	}
	if p.cfg.MemoryLimitBytes > 0 {
		argv = append(argv, "--memory", fmt.Sprintf("%d", p.cfg.MemoryLimitBytes))
	}
	if strings.TrimSpace(p.cfg.ContainerCPUs) != "" {
		argv = append(argv, "--cpus", p.cfg.ContainerCPUs)
	}
	argv = append(argv, p.cfg.ContainerImage, "python", "/work/main.py")
	return argv, nil
}
