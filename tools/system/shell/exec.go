package shell

import (
	"cmp"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/invokr-ai/invokr/capability/approval"
	"github.com/invokr-ai/invokr/config"

	"github.com/invokr-ai/invokr/support/fsutil"
	"github.com/invokr-ai/invokr/support/strutil"
	"github.com/invokr-ai/invokr/tools/core"
	"github.com/invokr-ai/invokr/tools/core/sandbox"
	"github.com/invokr-ai/invokr/tools/system/workspace"
)

// Every line runs as `bash -c <line>`. When the host has a namespace sandbox
// the line runs inside it with writable workspace roots, task scratch, selected
// caches and Git directories. Network is available for network-tier or
// operator-allowed lines. Without a sandbox,
// allowlisted lines run on the host as the argv-only tool did, and routed
// project code (interpreters, workspace scripts) needs the operator opt-out.

func sandboxExecReady() bool { return sandbox.Available() || sandbox.AllowUnsandboxed() }

// sandboxInterpreters names the interpreters the model reaches for to run code
// it wrote into the workspace.
var sandboxInterpreters = []string{"python", "python3", "node", "ruby", "perl", "bash", "sh", "zsh"}

// sandboxExecCandidate reports whether root is something to run sandboxed
// rather than refuse: a known interpreter, or a path (./run.sh,
// scripts/build.sh) that resolves — symlinks included, via the workspace
// policy — inside an allowed root to an existing regular file.
func sandboxExecCandidate(root string, wsp workspace.Policy, dir string) bool {
	if slices.Contains(sandboxInterpreters, root) {
		return true
	}
	if !strings.Contains(root, "/") {
		return false
	}
	_, ok := regularFileAt(root, wsp, dir)
	return ok
}

// looksLikeBuiltBinary reports whether a denied root is plausibly a compiled
// artifact rather than a typo or a missing config entry: any path (it either
// escaped the workspace or does not exist yet, since an existing regular file
// at a path would already have been a sandboxExecCandidate), or a bare name
// that resolves, within the workspace, to an existing regular file with an
// executable bit set — the model dropped the `./` prefix a built binary needs.
func looksLikeBuiltBinary(root string, wsp workspace.Policy, dir string) bool {
	if strings.Contains(root, "/") {
		return true
	}
	mode, ok := regularFileAt(root, wsp, dir)
	return ok && mode&0o111 != 0
}

// regularFileAt resolves root through the workspace policy and reports the
// mode of the regular file it names, or ok=false when it resolves to nothing
// usable.
func regularFileAt(root string, wsp workspace.Policy, dir string) (os.FileMode, bool) {
	resolved, err := resolveScriptPath(root, wsp, dir)
	if err != nil {
		return 0, false
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return 0, false
	}
	return info.Mode(), true
}

func resolveScriptPath(root string, wsp workspace.Policy, dir string) (string, error) {
	if filepath.IsAbs(root) {
		return wsp.Resolve(root)
	}
	base := strutil.FirstNonEmpty(strings.TrimSpace(dir), wsp.Root)
	if !filepath.IsAbs(base) {
		base = filepath.Join(wsp.Root, base)
	}
	return wsp.Resolve(filepath.Join(base, root))
}

func bashPath() (string, error) {
	path, err := exec.LookPath("bash")
	if err != nil {
		return "", fmt.Errorf("the shell tool needs bash on PATH: %w", err)
	}
	return path, nil
}

// Keep failures when a later cleanup command succeeds. Ignore the SIGPIPE a
// consumer such as head can cause, but retain other failures in that pipeline.
const pipelineExitTrap = `__invokr_failure=0; __invokr_record_status() {
  local status=$1 code; shift; if (( $# > 1 )); then status=0; for code in "$@"; do if (( code != 0 && code != 141 )); then status=$code; break; fi; done; fi
  if (( __invokr_failure == 0 && status != 0 )); then __invokr_failure=$status; fi
}
trap '__invokr_status=("$?" "${PIPESTATUS[@]}"); if (( __invokr_status[0] == 0 && ${#__invokr_status[@]} > 2 )); then __invokr_record_status "${__invokr_status[@]}"; fi' DEBUG
trap '__invokr_record_status "$?" "${PIPESTATUS[@]}"' ERR
trap '__invokr_record_status "$?" "${PIPESTATUS[@]}"; exit "$__invokr_failure"' EXIT
`

// processSpec builds the spec that runs plan's line, sandboxed when the host
// allows it.
func processSpec(plan preparedShellCommand) (core.ProcessSpec, error) {
	bash, err := bashPath()
	if err != nil {
		return core.ProcessSpec{}, err
	}
	spec := core.ProcessSpec{Name: bash, Args: []string{"-c", pipelineExitTrap + plan.line}, Dir: plan.cwd}
	if plan.mode == approval.YOLO {
		return spec, nil
	}
	if plan.mode == approval.Plan && !sandbox.Available() {
		return core.ProcessSpec{}, fmt.Errorf("plan verification requires namespace sandboxing; host fallback is forbidden")
	}
	if plan.mode == approval.Plan && slices.ContainsFunc(append(plan.roots, plan.readonlyRoots...), func(root string) bool { return workspace.Overlaps(root, fsutil.SharedTmpDir(plan.workspaceRoot)) }) {
		return core.ProcessSpec{}, fmt.Errorf("plan scratch must be outside source roots")
	}
	if plan.isolateEnv {
		// Non-nil even when empty: exec.Cmd inherits the host environment for
		// a nil Env, which is the opposite of isolation.
		spec.Env = append([]string{}, plan.env...)
	}
	if !sandbox.Available() {
		if plan.sandboxExec && !sandbox.AllowUnsandboxed() {
			return core.ProcessSpec{}, fmt.Errorf("running %q requires namespace sandboxing, which is unavailable on this host; the shell tool runs no interpreters or scripts without it — to run code use code_interpreter, or set %s=1 to run it directly on the host (not recommended)", plan.line, sandbox.AllowUnsandboxedEnv)
		}
		if plan.sandboxExec {
			// Model-supplied code never inherits the host env: the agent's own
			// secrets must not be readable by a script the model wrote.
			spec.Env = buildShellEnv(nil)
		}
		spec.Env = unsandboxedJVMEnv(plan, spec.Env)
		return spec, nil
	}
	sb := sandboxPlan(bash, plan)
	spec = sandbox.ProcessSpec(sb, plan.cwd, spec.Args, plan.foreground)
	spec.Env = sandboxEnv(plan, sb)
	return spec, nil
}

// sandboxPlan mounts the workspace roots read-write, the user cache
// read-write (go's build cache lives there), the host's alternatives and
// account files read-only so `whoami` still has a name to print, and the
// directory of every command the line runs, resolved on the host, so bash
// finds the same binary a direct exec would.
func sandboxPlan(bash string, plan preparedShellCommand) sandbox.Plan {
	caps := core.ToolCapabilities{WritesWorkspace: true, Network: plan.networkAllowed()}
	sb := sandbox.BuildPlan(bash, "", "", plan.workspaceRoot, caps, 0, nil)
	sb.RO = append(sb.RO, "/etc/alternatives", "/etc/passwd", "/etc/group")
	sb.TmpDir = sharedTmpDir(plan.workspaceRoot)
	// The user bin dirs stay mounted whatever the line runs: they are on the
	// sandbox PATH, and a PATH entry the mount filter drops is a "command not
	// found" for a binary that is there.
	sb.RO = append(sb.RO, UserBinDirs()...)
	var toolDirs []string
	for _, command := range plan.commands {
		toolDirs = append(toolDirs, commandDirs(command)...)
	}
	isJVM := slices.ContainsFunc(plan.commands, isJVMCommand)
	toolDirs = append(toolDirs, jvmSupportDirs(isJVM)...)
	// A symlink inside a read-only mount dangles unless its target is mounted
	// too; the JDK keeps its whole conf directory behind one (#546).
	sb.RO = append(sb.RO, toolDirs...)
	sb.RO = append(sb.RO, escapedLinkTargets(toolDirs)...)
	if slices.ContainsFunc(plan.commands, func(command string) bool { return filepath.Base(command) == "make" }) {
		sb.PinnedRO = userToolFiles(plan.allowedCommands)
		sb.RO = append(sb.RO, sb.PinnedRO...)
	}
	// The gradle home read-only first, its cache directories read-write on
	// top: sandbox.Command writes the RO binds before the RW ones, so the
	// narrower mounts layer over the home rather than being hidden by it.
	sb.RO = append(sb.RO, gradleReadOnlyDirs(isJVM)...)
	sb.RW = append(sb.RW, jvmCacheDirs(isJVM)...)
	for _, root := range plan.roots {
		if root != plan.workspaceRoot {
			sb.RW = append(sb.RW, root)
		}
	}
	sb.RO = append(sb.RO, plan.readonlyRoots...)
	// With no workspace configured the call runs from the process's own
	// directory, which has to exist inside the sandbox too.
	if !slices.ContainsFunc(sb.RW, func(root string) bool { _, ok := fsutil.RelInside(root, plan.cwd); return ok }) {
		sb.RW = append(sb.RW, plan.cwd)
	}
	// Only go needs a writable cache, and only its build cache: mounting all
	// of os.UserCacheDir() exposed every other workspace living under it.
	if slices.Contains(plan.commands, "go") {
		sb.RW = append(sb.RW, goBuildCache())
		// The module cache go reads its dependencies from lives outside the
		// workspace too. Unmounted, every build inside the sandbox fails on a
		// cache miss it cannot fill: the network tier it would need to refetch
		// is not the tier a build runs at. Read-only — populating the cache is
		// the host's job.
		sb.RO = append(sb.RO, goModCache())
	}
	// A linked worktree's .git is a pointer file, not a directory, and what it
	// points at is outside the workspace. Without it git cannot read the
	// repository at all: every command answers "not a git repository".
	gitDirs := workspace.GitDirs(plan.workspaceRoot)
	readOnlyGit := plan.readOnlyGit()
	if slices.Contains(plan.commands, "git") {
		mountDirs := gitDirs
		if len(mountDirs) == 2 { // A linked worktree lives inside its shared repository.
			mountDirs = mountDirs[:1]
		}
		if !readOnlyGit {
			sb.PinnedRW = mountDirs
			sb.RW = append(sb.RW, mountDirs...)
		}
		// HOME is forwarded but the home directory itself is not mounted, so
		// git inside the sandbox finds no global config and therefore no
		// author. It then refuses to commit, and the model's way out is a bare
		// `git config user.email`, which writes the *shared* repository config
		// and rewrites the identity for every linked worktree (#498). Mounting
		// the global config read-only gives it the user's real identity and
		// removes the reason to reach for `git config` at all.
		sb.RO = append(sb.RO, gitConfigFiles()...)
	}
	if readOnlyGit {
		// Concat, not append: appending onto plan.roots could write into its backing array.
		sb = sealReadOnly(sb, slices.Concat(plan.roots, gitDirs))
	}
	if plan.mode == approval.Auto {
		sb.Sealed = append(sb.Sealed, plan.protected...)
	}
	sb.Sealed = append(sb.Sealed, plan.readonlyRoots...)
	return sandbox.DedupPlan(sb)
}

// readOnlyGit reports whether the repository is mounted read-only: always in
// Plan mode, and for read-only git probes.
func (p preparedShellCommand) readOnlyGit() bool { return p.mode == approval.Plan || p.gitReadOnly }

func sealReadOnly(sb sandbox.Plan, sources []string) sandbox.Plan {
	sb.Sealed, sb.RO = append(sb.Sealed, sources...), append(sb.RO, sources...)
	sb.RW = slices.DeleteFunc(sb.RW, func(path string) bool {
		return slices.ContainsFunc(sources, func(root string) bool { return workspace.Overlaps(root, path) })
	})
	return sb
}

// sharedTmpDir is the per-workspace directory mounted at /tmp: a scratch file
// the model writes to /tmp survives the call and is the same file
// code_interpreter reaches under that path. It lives outside the workspace so
// it never shows up in the task repo's git status. Without a workspace there
// is nothing to share, so the sandbox keeps its private tmpfs.
func sharedTmpDir(workspaceRoot string) string {
	dir := fsutil.SharedTmpDir(workspaceRoot)
	if dir == "" {
		return ""
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	return dir
}

// unsandboxedJVMEnv points a host-run gradle at the per-workspace daemon
// registry (#579). Only a JVM line is touched; env otherwise stays whatever
// processSpec already set it to, nil included, so a non-JVM line keeps
// inheriting the host env.
func unsandboxedJVMEnv(plan preparedShellCommand, env []string) []string {
	if !slices.ContainsFunc(plan.commands, isJVMCommand) {
		return env
	}
	if env == nil {
		// Gradle runs on the host here, so the registry has to be an env
		// build too (env otherwise stays nil to inherit the host env).
		env = os.Environ()
	}
	// A per-workspace daemon registry, so a --no-daemon build in one concurrent
	// session cannot stop another's daemon (#579). Without a workspace gradle
	// keeps its own registry choice.
	registry := ""
	if dir := sharedTmpDir(plan.workspaceRoot); dir != "" {
		registry = filepath.Join(dir, "gradle-daemon")
	}
	return jvmEnv(true, registry, env)
}

// goBuildCache names the directory `go build` writes to. $GOCACHE wins, as it
// does for go itself; otherwise it is the documented default under the user
// cache dir.
func goBuildCache() string {
	if cache := config.Env("GOCACHE"); cache != "" {
		return cache
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return ""
	}
	return filepath.Join(cache, "go-build")
}

// goModCache names the directory `go` reads downloaded modules from. $GOMODCACHE
// wins, as it does for go itself; otherwise it is pkg/mod under the first GOPATH
// entry, defaulting to ~/go.
func goModCache() string {
	if cache := config.Env("GOMODCACHE"); cache != "" {
		return cache
	}
	gopath := cmp.Or(config.Env("GOPATH"), userHomeDir("go"))
	if gopath == "" {
		return ""
	}
	// GOPATH is a list; go builds the module cache under its first entry.
	first, _, _ := strings.Cut(gopath, string(os.PathListSeparator))
	return filepath.Join(first, "pkg", "mod")
}

// UserBinDirs names the per-user binary directories a login shell puts on PATH
// but an agent started from a service or a script often does not inherit
// (node/npm and go-installed tools live there). Only existing directories are
// returned, so a host without them is unaffected.
func UserBinDirs() []string {
	if userHomeDir() == "" {
		return nil
	}
	dirs := []string{userHomeDir(".local", "bin"), userHomeDir("go", "bin")}
	return slices.DeleteFunc(dirs, func(dir string) bool {
		info, err := os.Stat(dir)
		return err != nil || !info.IsDir()
	})
}

// Supply only declared executable files for make's child processes. A toolchain
// prefix may contain secrets; auxiliary runtime trees need explicit read roots.
func userToolFiles(allowed []string) []string {
	var files []string
	for _, name := range allowed {
		if filepath.Base(name) != name {
			continue
		}
		for _, bin := range UserBinDirs() {
			path, err := exec.LookPath(filepath.Join(bin, name))
			if err != nil {
				continue
			}
			if target, err := filepath.EvalSymlinks(path); err == nil && target != path {
				files = append(files, target)
			}
		}
	}
	return files
}

// lookCommand resolves a command on the host PATH, falling back to the user
// bin dirs when the agent's own PATH is missing them.
func lookCommand(command string) (string, error) {
	path, err := exec.LookPath(command)
	if err == nil {
		return path, nil
	}
	for _, dir := range UserBinDirs() {
		candidate := filepath.Join(dir, command)
		if info, statErr := os.Stat(candidate); statErr == nil && info.Mode().IsRegular() && info.Mode()&0o111 != 0 {
			return candidate, nil
		}
	}
	return "", err
}

// commandDirs resolves a command and returns the directories its binary and
// its symlink target live in; nothing for an unknown command.
//
// An absolute path is followed too, not skipped. The per-user bin dirs are
// mounted whole, but a tool installed there is often a symlink into a manager's
// own tree (~/.local/bin/pytest -> ~/.local/share/uv/tools/pytest/bin/pytest).
// Mounting only the link left it dangling inside the sandbox, so running it by
// its full path answered "No such file or directory" for a binary that is
// installed — and the model concluded the tool was missing (r9 B2/1).
func commandDirs(command string) []string {
	if strings.Contains(command, "/") && !filepath.IsAbs(command) {
		// A relative path names a workspace script, already mounted.
		return nil
	}
	// LookPath stats and exec-checks a path that contains a separator, so an
	// absolute path needs no separate check here.
	path, err := lookCommand(command)
	if err != nil {
		return nil
	}
	return symlinkChainDirs(path)
}

// symlinkChainDirs returns the prefix dirs of path and of every hop in its
// symlink chain. Resolving straight to the final target skips the hops in
// between, and those are where the runtime lives: ~/.local/bin/npm links to
// ~/.hermes/node/bin/npm, whose own chain ends in lib/node_modules, so
// mounting only the end leaves both the hop and the node binary beside it
// outside the sandbox and the npm symlink dangling.
func symlinkChainDirs(path string) []string {
	dirs := prefixDirs(path)
	for range maxSymlinkHops {
		target, err := os.Readlink(path)
		if err != nil {
			break
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		path = filepath.Clean(target)
		dirs = append(dirs, prefixDirs(path)...)
	}
	return dirs
}

// maxSymlinkHops bounds the walk so a symlink cycle cannot spin.
const maxSymlinkHops = 8

// prefixDirs returns the binary's directory and, for a prefix layout
// (<prefix>/bin/<tool>), the prefix itself: go needs its GOROOT and node its
// lib/ beside bin/, and neither is under /usr on every host.
func prefixDirs(path string) []string {
	dir := filepath.Dir(path)
	if filepath.Base(dir) == "bin" {
		return []string{filepath.Dir(dir)}
	}
	return []string{dir}
}

// sandboxEnv is the line's environment with PATH reduced to the directories
// the sandbox actually mounts: a host PATH entry under an unmounted prefix
// would only produce "command not found" for a binary that is there. Project
// code never sees the host env, whatever the isolation setting.
func sandboxEnv(plan preparedShellCommand, sb sandbox.Plan) []string {
	env := buildShellEnv(plan.passthrough)
	switch {
	case plan.sandboxExec:
		env = buildShellEnv(nil)
	case !plan.isolateEnv:
		env = os.Environ()
	}
	mounted := slices.Concat(sb.RO, sb.RW)
	for i, kv := range env {
		if !strings.HasPrefix(kv, "PATH=") {
			continue
		}
		var keep []string
		dirs := append(filepath.SplitList(strings.TrimPrefix(kv, "PATH=")), UserBinDirs()...)
		for _, dir := range core.DedupPreserveOrder(dirs) {
			if slices.ContainsFunc(mounted, func(m string) bool { return dir == m || strings.HasPrefix(dir, m+"/") }) {
				keep = append(keep, dir)
			}
		}
		env[i] = "PATH=" + strings.Join(keep, string(os.PathListSeparator))
	}
	registry := ""
	if sb.TmpDir != "" {
		registry = "/tmp/gradle-daemon"
	}
	return jvmEnv(slices.ContainsFunc(plan.commands, isJVMCommand), registry, env)
}

// sandboxConfinement describes, for the approval card and the tool
// description, what boundary a line actually gets — never claiming a sandbox
// the host cannot provide.
// perLine reports a specific line's network posture instead of the default.
func sandboxConfinement(perLine, network bool) string {
	if sandbox.Available() {
		state := "network isolated by default; network-tier or operator-allowed lines use the host network"
		if perLine {
			state = "network isolated"
			if network {
				state = "host network available"
			}
		}
		return "sandboxed: writable workspace roots, task scratch, selected caches and Git directories; " + state
	}
	if sandbox.AllowUnsandboxed() {
		return "runs UNSANDBOXED on the host: namespace sandboxing is unavailable and " + sandbox.AllowUnsandboxedEnv + "=1 is set"
	}
	return "runs on the host: namespace sandboxing is unavailable, so interpreters and scripts are refused"
}

// scriptRoutingNote states the routing in the tool description, only where it
// is actually available.
func scriptRoutingNote() string {
	if !sandboxExecReady() {
		return " No interpreter or script runs here: to run code use code_interpreter."
	}
	return " An interpreter (python3, node, bash, …) or a script in the workspace (./run.sh) also runs, and always asks for approval first. A rejected line can go in a script instead: `bash /tmp/x.sh`."
}

// gitConfigFiles names the user's global git configuration, in the order git
// itself reads it: $GIT_CONFIG_GLOBAL, then XDG, then ~/.gitconfig. Only files
// that exist are returned, so a host without one is unaffected. The system
// config under /etc is already covered by the read-only /usr and /etc mounts.
func gitConfigFiles() []string {
	var paths []string
	if explicit := config.Env("GIT_CONFIG_GLOBAL"); explicit != "" {
		paths = append(paths, explicit)
	}
	if xdg := config.Env("XDG_CONFIG_HOME"); xdg != "" {
		paths = append(paths, filepath.Join(xdg, "git", "config"))
	}
	if userHomeDir() != "" {
		paths = append(paths, userHomeDir(".config", "git", "config"), userHomeDir(".gitconfig"))
	}
	return slices.DeleteFunc(paths, func(path string) bool {
		info, err := os.Stat(path)
		return err != nil || !info.Mode().IsRegular()
	})
}
