package constants

import "slices"

const (
	ShellToolName = "shell"

	ShellDefaultTimeoutMS      = 30000
	ShellDefaultMaxOutputBytes = 20000

	// Caps on model-supplied overrides, mirroring CodeInterpreterMaxTimeoutMS:
	// without them a single tool call can pin a turn for hours or buffer
	// unbounded output.
	ShellMaxTimeoutMS   = 120000
	ShellMaxOutputBytes = 200000

	// ShellNoOutputMarker stands in for a command that printed nothing at
	// all. An empty stdout field read as a truncation or a tool failure, and
	// the model retried a command that had in fact succeeded silently.
	ShellNoOutputMarker = "(no output)"

	ShellTierReadonly = "execute_readonly_command"
	ShellTierWrite    = "execute_write_command"
	ShellTierNetwork  = "execute_network_command"
)

// The default allowlist admits only binaries that cannot execute
// project-supplied code. Ecosystem drivers (make, npm, cargo, node, python3,
// pytest) are classified in tools/system/shell/rules.go but must be opted into
// shell.allowed_commands: with allow_write_commands: true they would otherwise
// be silent arbitrary execution. `go` predates this rule and stays for
// compatibility.
var defaultShellAllowedCommands = []string{
	"ls", "pwd", "cat", "head", "tail", "wc", "find", "grep", "rg",
	"git", "go", "gofmt",
	// Inspection; awk is write tier, since its programs can write and exec.
	// "echo" earns its place by unblocking chains: the model routinely
	// separates steps with `echo "---"`, and one disallowed segment rejects
	// the whole chain.
	"echo", "file", "od", "sort", "awk", "cut", "tee", "sha256sum",
	// Environment questions the model asks before deciding what to run: who it
	// is, what the file is, what time it is. None of them can write.
	"whoami", "id", "stat", "date",
	// Workspace-shaping writes. Unknown to rules.go, so each call is write
	// tier and asks for approval; without them the model's `chmod +x
	// script.sh` and `mkdir -p out && cp ...` are rejected outright.
	"chmod", "mkdir", "cp", "rm", "rmdir", "mv", "touch",
	// Roots the model reaches for constantly. which/base64/xxd/tr/uniq/diff/
	// sleep cannot write outside an explicit output argument; sed is write
	// tier (w/e commands); timeout is classified by the command it wraps.
	"which", "sed", "base64", "xxd", "sleep", "timeout", "tr", "uniq", "diff",
	// xargs is a wrapper classified by the command it runs, like timeout.
	"xargs",
}

func DefaultShellAllowedCommands() []string { return slices.Clone(defaultShellAllowedCommands) }

// shellReadonlyBaseline is the floor under any configured allowlist. Every
// entry only inspects: none can write, and none can execute project-supplied
// code, so admitting them is not an approval-posture change (ADR 0011 (4)).
// A config written before the default list grew silently narrowed the agent to
// whatever it happened to name — three bench models spent their turn budget
// retrying blocked `find`/`head`/`tail` — because a configured list replaces
// the default outright.
var shellReadonlyBaseline = []string{
	"ls", "pwd", "cat", "head", "tail", "wc", "find", "grep", "rg",
	"echo", "which", "file", "sort", "uniq", "diff", "stat", "date",
	// xargs runs no program of its own: the policy resolves the command it
	// would run and checks that against this same allowlist, so `find | xargs
	// grep` passes and `xargs rm` is refused on rm.
	"xargs",
}

// ShellAllowedCommands resolves the effective allowlist: an empty configured
// list means the full default, and a configured list is the user's list plus
// the read-only baseline.
func ShellAllowedCommands(configured []string) []string {
	if len(configured) == 0 {
		return DefaultShellAllowedCommands()
	}
	out := slices.Clone(configured)
	for _, command := range shellReadonlyBaseline {
		if !slices.Contains(out, command) {
			out = append(out, command)
		}
	}
	return out
}
