package constants

import "slices"

const (
	DefaultToolboxDir = "toolbox"

	// ToolboxSource is the ToolSpec.Metadata.Source every plugin-loaded tool
	// carries; it distinguishes a manifest-declared tool from a compiled-in
	// native one.
	ToolboxSource     = "toolbox"
	DefaultGoBin      = "go"
	DefaultNodeBin    = "node"
	DefaultPythonBin  = "python3"
	DefaultTokfBin    = "tokf"
	DefaultGoCacheDir = "/tmp/invokr-go-cache"

	// ToolboxDefaultMaxOutputBytes caps the combined stdout+stderr a toolbox
	// subprocess may buffer back to the model. This is a runaway-output memory
	// guard, not a context-size limiter, so it is generous: plugins may
	// legitimately return large outputs. Tighten via
	// INVOKR_TOOLBOX_MAX_OUTPUT_BYTES if desired.
	ToolboxDefaultMaxOutputBytes = 1_000_000

	// ToolboxDefaultMemoryLimitBytes bounds a sandboxed toolbox tool process
	// via RLIMIT_AS. Go's own compiler (invoked by "go run") parallelizes
	// function compilation across GOMAXPROCS goroutines, multiplying virtual
	// memory pressure well past the ~1 GiB floor a single Go process needs
	// just to start under RLIMIT_AS; 2 GiB gives that headroom while still
	// containing a runaway allocation.
	ToolboxDefaultMemoryLimitBytes = 2 * 1024 * 1024 * 1024

	// ToolboxDefaultExecTimeoutMS is the wall-clock backstop for toolbox tool
	// execution: the reliable defense against runaway processes (e.g. fork
	// bombs) that resource limits alone don't contain.
	ToolboxDefaultExecTimeoutMS = 60_000

	// ToolboxMaxLimit* bound what a plugin manifest's `limits` block may raise
	// the per-tool timeout / memory / output caps to. They exist so a
	// self-declared manifest cannot grant itself unbounded host resources.
	ToolboxMaxLimitTimeoutMS   = 600_000
	ToolboxMaxLimitMemoryBytes = 8 * 1024 * 1024 * 1024
	ToolboxMaxLimitOutputBytes = 16 * 1024 * 1024

	// ToolboxProgressPrefix marks a plugin's incremental progress line on
	// stderr. Lines carrying it are forwarded to the tool event bus; every
	// other stderr line, and all of stdout, keeps its existing meaning.
	ToolboxProgressPrefix = "INVOKR_PROGRESS:"

	ToolboxDefaultPackageIndexURL     = "https://pypi.org/simple"
	ToolboxDefaultPackageMetadataURL  = "https://pypi.org/pypi/%s/json"
	ToolboxDefaultReleaseCooldownDays = 14
)

func DefaultToolboxIndexHosts() []string { return []string{"pypi.org"} }

func DefaultToolboxArtifactHosts() []string { return []string{"files.pythonhosted.org"} }

// defaultPythonPipArgs forces wheel-only installs: pip never runs a
// sdist's setup.py/build backend, so install-time RCE via a malicious
// build script is refused rather than executed (see M3 supply-chain
// hardening in docs/private/plans/toolbox-agent-authored-tools.md).
var defaultPythonPipArgs = []string{"install", "--only-binary=:all:", "-r"}

func DefaultPythonPipArgs() []string { return slices.Clone(defaultPythonPipArgs) }
