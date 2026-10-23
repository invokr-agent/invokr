package shell

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"
)

// commandRule declares how one executable is tiered. The table can only ever
// *narrow* the fail-closed default in classifyShellCommand: an executable with
// no rule stays write, and within a rule anything not named stays write.
//
// Two shapes, and a rule must be exactly one of them:
//
//   - Subcommand grammar (Readonly/Network non-empty): the first non-flag arg is
//     the subcommand, resolved through classifySubcommand so unknown *leading*
//     flags still fail closed. WriteFlags demote the whole invocation first.
//   - Fetcher (AlwaysNetwork): every invocation reaches the network, so there
//     is no argv shape to inspect — the whole binary is network tier.
//   - Flag-only utility (Readonly and Network both empty): the binary has no
//     write mode reachable from argv at all — with no shell there is no
//     redirection, so "writes a file" requires an explicit flag. ReadFlags, when
//     set, is a fail-closed allowlist of the flags that keep it read-only. Flags
//     are not the whole test: only `cwd` is workspace-resolved, so a non-flag
//     argument that is absolute or escapes via ".." demotes to write, keeping
//     the readonly read scope workspace-relative (ADR 0011).
//
// Adding a row is a security review. The bar for readonly is exactly:
// the invocation (a) cannot write outside a build/module cache and (b) does not
// execute project-supplied code. `go vet` is the precedent for (a); `pytest
// --collect-only` fails (b) because collection imports test modules.
type commandRule struct {
	Readonly   []string // read-only subcommands
	Network    []string // subcommands that reach the network
	ReadFlags  []string // flag-only utilities: flags that stay read-only (nil = binary has no write flag)
	WriteFlags []string // flags that demote a subcommand invocation to write
	MaxInputs  int      // flag-only utilities: operands past this many name an output file (0 = no limit)

	// AlwaysNetwork marks a binary whose every invocation reaches the network,
	// whatever its argv (curl, wget). It is its own shape: no subcommands and
	// no flag allowlist.
	AlwaysNetwork bool
}

// commandRules is the whole classification surface. node, python3, pytest and
// make deliberately get no row: every invocation of them executes
// project-supplied code, so the fail-closed write tier (approval every call) is
// the correct answer.
var commandRules = map[string]commandRule{
	// Flag-only utilities with no write mode reachable from argv.
	"ls": {}, "pwd": {}, "cat": {}, "head": {}, "tail": {}, "wc": {}, "grep": {}, "which": {},
	// file -C compiles a magic file to disk, so its flags are an allowlist.
	"file": {ReadFlags: []string{"-b", "-i", "-L", "-h", "-z", "-k", "-s", "-E", "--brief", "--mime", "--mime-type", "--mime-encoding"}},
	"cut":  {}, "sha256sum": {}, "tee": {Readonly: []string{"--help"}},
	// echo writes only to stdout; od dumps bytes; sort has -o (write to file),
	// so its read-only flags are an allowlist.
	"echo": {}, "od": {},
	// base64/tr/diff/sleep have no argv path to a file they write; uniq's
	// second operand is its output file.
	"base64": {}, "tr": {}, "uniq": {MaxInputs: 1}, "diff": {}, "sleep": {},
	// xargs never reaches this rule in practice: the policy resolves the
	// command it runs and tiers that instead. The row keeps the doctor check
	// from reading an allowlisted xargs as untiered.
	"xargs": {},
	// awk and sed programs write files and run commands (awk system() and
	// print >, sed w/W/e), so no argv shape proves a read: every real call is
	// write tier. `sed -n N,Mp FILE` still reroutes to read_file first.
	"awk": {Readonly: []string{"--help"}}, "sed": {Readonly: []string{"--help"}},
	// Identity and clock: no argv reaches a file at all. date -s needs root and
	// is not a write to the workspace, so the flag-only shape still holds.
	"whoami": {}, "id": {}, "date": {},
	// stat's --printf/-c are format strings, not output paths; the allowlist
	// keeps it to the formatting flags the model uses.
	"stat": {ReadFlags: []string{"-c", "--format", "-t", "-L", "--printf"}},
	// Workspace-shaping writes: the subcommand grammar keeps every real call
	// on the write tier (approval); only --help is read-only.
	"chmod": {Readonly: []string{"--help"}}, "mkdir": {Readonly: []string{"--help"}}, "cp": {Readonly: []string{"--help"}},
	"rm": {Readonly: []string{"--help"}}, "rmdir": {Readonly: []string{"--help"}}, "mv": {Readonly: []string{"--help"}}, "touch": {Readonly: []string{"--help"}},
	// xxd's second positional is an output file, so it stays write tier; the
	// row exists only so `invokr doctor` does not report it as unclassified.
	// timeout is classified by the command it wraps (policy.go), never here.
	"xxd": {Readonly: []string{"--help"}}, "timeout": {Readonly: []string{"--help"}},
	"sort": {ReadFlags: []string{"-r", "-u", "-n", "-f", "-b", "-k", "-t", "-h", "--reverse", "--unique", "--numeric-sort"}},
	// find has write primitives, so its read-only flags are an allowlist.
	"find": {ReadFlags: []string{"-name", "-iname", "-path", "-type", "-maxdepth", "-mindepth", "-not", "-o", "-a", "-prune", "-print", "-print0", "-size", "-newer", "-mtime"}},
	// rg has --pre/--pre-glob, which run an arbitrary preprocessor binary, so
	// its read-only flags are an allowlist like find's.
	"rg": {ReadFlags: []string{"-n", "-N", "-l", "-i", "-s", "-S", "-w", "-x", "-c", "-v", "-F", "-e", "-m", "-A", "-B", "-C", "-g", "-t", "--glob", "--type", "--hidden", "--no-ignore", "--fixed-strings", "--count", "--files", "--files-with-matches", "--max-count"}},
	// gofmt is read-only unless it rewrites in place.
	"gofmt": {ReadFlags: []string{"-l", "-d", "-e", "-s"}},

	"git": {Readonly: []string{"status", "diff", "show", "log", "rev-parse", "help"}, Network: []string{"fetch", "pull", "clone"}, WriteFlags: []string{"--output"}},
	// go's WriteFlags exist because `go env -w/-u` persists config.
	// -vettool and -toolexec run arbitrary binaries, so they demote (#318).
	"go": {Readonly: []string{"version", "list", "vet", "env"}, WriteFlags: []string{"-w", "-u", "-vettool", "-toolexec"}},
	// Opt-in ecosystem drivers: not in the default allowlist, but classified
	// here so adding them to allowed_commands does not hit the write-tier trap.
	// Fetchers. Without a row they fell to the write tier, where
	// allow_write_commands runs them unprompted *and* the sandbox gives them
	// no network at all — a silent DNS failure (#545). Network tier is the
	// honest classification: they prompt unless allow_network_commands, and
	// when permitted they keep the host netns. They can also write a file
	// (curl -o, wget's default save), so there is no read-only spelling.
	"curl": {AlwaysNetwork: true}, "wget": {AlwaysNetwork: true},

	"npm":   {Readonly: []string{"ls", "view", "outdated", "config"}, Network: []string{"install", "ci", "audit", "publish"}},
	"cargo": {Readonly: []string{"check", "tree", "metadata", "clippy"}, Network: []string{"fetch", "update", "publish"}},
}

// ClassifiedCommands returns the executables with a tier rule, sorted. A
// configured command outside this set prompts on every call (`invokr doctor`
// uses it to warn about that trap).
func ClassifiedCommands() []string { return slices.Sorted(maps.Keys(commandRules)) }

func classifyByRule(command string, args []string, rule commandRule) shellTier {
	if rule.AlwaysNetwork {
		return shellTierNetwork
	}
	if hasWriteFlag(args, rule.WriteFlags) {
		return shellTierWrite
	}
	if len(rule.Readonly) == 0 && len(rule.Network) == 0 {
		return classifyFlagsOnly(args, rule.ReadFlags, rule.MaxInputs)
	}
	sub, tier, done := classifySubcommand(command, args)
	if done {
		return tier
	}
	return classifySubcommandTier(rule, sub)
}

func classifySubcommandTier(rule commandRule, sub string) shellTier {
	switch {
	case slices.Contains(rule.Network, sub):
		return shellTierNetwork
	case slices.Contains(rule.Readonly, sub):
		return shellTierReadonly
	}
	return shellTierWrite
}

// classifyFlagsOnly keeps a utility read-only when its flags are known-safe.
// A nil allowlist means the binary has no write flag at all (ls, cat, grep);
// a non-nil one fails closed on anything unlisted (find, gofmt).
func classifyFlagsOnly(args, readFlags []string, maxInputs int) shellTier {
	inputs := 0
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			if inputs++; argEscapesWorkspace(arg) || (maxInputs > 0 && inputs > maxInputs) {
				return shellTierWrite
			}
			continue
		}
		if readFlags != nil && !slices.Contains(readFlags, flagName(arg)) {
			return shellTierWrite
		}
		// A `--flag=value` value never reaches the non-flag branch above, so
		// check it here or `wc --files0-from=/etc/passwd` stays readonly.
		if _, value, ok := strings.Cut(arg, "="); ok && argEscapesWorkspace(value) {
			return shellTierWrite
		}
	}
	return shellTierReadonly
}

// argEscapesWorkspace reports whether a non-flag argument can name a file
// outside the workspace. Only `cwd` is resolved through workspace.Resolve, so
// an absolute, home-relative (~) or parent-escaping argument would otherwise
// read anything on disk
// at the readonly (no-approval) tier.
func argEscapesWorkspace(arg string) bool {
	return strings.HasPrefix(arg, "~") || filepath.IsAbs(arg) || slices.Contains(strings.Split(filepath.ToSlash(arg), "/"), "..")
}

// hasWriteFlag matches a flag exactly or as `--flag=value`, so `git log
// --output=/tmp/x` demotes but `--output-indicator-new=+` does not.
func hasWriteFlag(args, writeFlags []string) bool {
	return slices.ContainsFunc(args, func(arg string) bool { return slices.Contains(writeFlags, flagName(arg)) })
}

func flagName(arg string) string {
	name, _, _ := strings.Cut(arg, "=")
	return name
}
