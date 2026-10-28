package shell

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/invokr-agent/invokr/config"
	"github.com/invokr-agent/invokr/tools/core"
	"github.com/invokr-agent/invokr/tools/core/sandbox"
	"github.com/invokr-agent/invokr/tools/system/workspace"
)

func allowSet(names ...string) map[string]struct{} {
	set := map[string]struct{}{}
	for _, name := range names {
		set[name] = struct{}{}
	}
	return set
}

var testAllowed = []string{"cat", "chmod", "echo", "find", "git", "go", "grep", "head", "ls", "sort", "tail", "wc"}

func classify(t *testing.T, line string) (policy, error) {
	t.Helper()
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: t.TempDir()})
	return classifyLine(line, allowSet(testAllowed...), testAllowed, wsp, wsp.Root, false)
}

// The allowlist is the outer gate: every command root anywhere in the line —
// in a pipeline, a loop body, a subshell, a substitution — must be on it.
func TestPolicyAllowlistGatesEveryRoot(t *testing.T) {
	refused := []string{
		"rm -rf x",
		"ls && rm x",
		"cat f | paste -sd+",
		"for f in *.log; do curl $f; done",
		"(ls; rm x)",
		"ls $(rm x)",
		"if ls; then rm x; fi",
		"ls | while read f; do rm \"$f\"; done",
		"X=rm; $X -rf x",
		"eval ls",
		"exec ls",
		"source x.sh",
		"command ls",
		"echo hi | xargs ls",
	}
	for _, line := range refused {
		if _, err := classify(t, line); err == nil {
			t.Fatalf("%q must be refused", line)
		} else if !strings.Contains(err.Error(), "is not allowed") {
			t.Fatalf("%q: refusal must name the allowlist, got %v", line, err)
		}
	}
	allowed := []string{
		"ls",
		"ls | wc -l",
		"cat f; echo ---; wc -l < f",
		"for f in *.log; do echo \"== $f\"; cat \"$f\"; done",
		"if [ -f x ]; then cat x; else echo none; fi",
		"ls && echo ok || echo fail",
		"grep -c ERROR *.log | sort -n | head -1",
		"cd sub && ls",
		"X=1 ls",
		"true; ls; test -d x",
	}
	for _, line := range allowed {
		if _, err := classify(t, line); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
	}
}

// Tiers follow the strongest root, and constructs the walk cannot classify
// statically fail closed to the write tier with a reason the card can show.
func TestPolicyTiers(t *testing.T) {
	cases := []struct {
		line   string
		tier   shellTier
		reason string
	}{
		{"ls -la", shellTierReadonly, ""},
		{"git status && git log -3", shellTierReadonly, ""},
		{"cat f 2>/dev/null | sort", shellTierReadonly, ""},
		{"ls 2>&1 >/dev/null", shellTierReadonly, ""},
		{"wc -l < f", shellTierReadonly, ""},
		{"cat <<EOF\nhi\nEOF", shellTierReadonly, ""},
		{"echo hi > out.txt", shellTierWrite, "redirects output to a file"},
		{"echo hi >> out.txt", shellTierWrite, "redirects output to a file"},
		{"cat > f <<EOF\nx\nEOF", shellTierWrite, "redirects output to a file"},
		{"ls $(echo x)", shellTierWrite, "command substitution"},
		{"ls `echo x`", shellTierWrite, "command substitution"},
		{"cat <(ls)", shellTierWrite, "process substitution"},
		{"ls & echo", shellTierWrite, "background job"},
		{"chmod +x x.sh", shellTierWrite, "chmod is a write command"},
		{"ls && git commit -m x", shellTierWrite, "git is a write command"},
		{"git fetch", shellTierNetwork, "git is a network command"},
		{"git fetch && chmod +x x", shellTierNetwork, "git is a network command"},
		{"sort -o out f", shellTierWrite, "sort is a write command"},
		{"find . -delete", shellTierWrite, "find is a write command"},
		{"cat /etc/passwd", shellTierWrite, "cat is a write command"},
		{"cat ../x", shellTierWrite, "cat is a write command"},
		{"cd /tmp && ls", shellTierWrite, "changes directory outside the workspace"},
		{"wc -l < /etc/passwd", shellTierWrite, "outside the workspace"},
		// A flag hiding in a variable cannot be inspected: rules that read
		// flags fail closed, rules without flags stay read-only.
		{"sort $flags f", shellTierWrite, "sort is a write command"},
		{"cat \"$f\"", shellTierReadonly, ""},
	}
	for _, c := range cases {
		pol, err := classify(t, c.line)
		if err != nil {
			t.Fatalf("%q: %v", c.line, err)
		}
		if pol.tier != c.tier {
			t.Fatalf("%q: tier %q want %q (reasons %v)", c.line, pol.tier, c.tier, pol.reasons)
		}
		if c.reason != "" && !strings.Contains(strings.Join(pol.reasons, "; "), c.reason) {
			t.Fatalf("%q: reasons %v lack %q", c.line, pol.reasons, c.reason)
		}
	}
}

func TestPolicyRefusesInvalidBashAndEmptyLines(t *testing.T) {
	for _, line := range []string{"ls |", "if ls; then", "echo 'unterminated", "# only a comment"} {
		if _, err := classify(t, line); err == nil {
			t.Fatalf("%q must be refused", line)
		}
	}
}

// A line whose only command is a harmless builtin still runs something; the
// bench model reaches for printf and test constantly, and `find -exec cat {} +`
// carries a literal `{}` that bash never brace-expands.
func TestPolicyAcceptsBuiltinOnlyLinesAndBareBraces(t *testing.T) {
	for _, line := range []string{"true", "printf 'hi\\n'", "test -f x", "cd sub", "find . -name '*.py' -exec cat {} +"} {
		if _, err := classify(t, line); err != nil {
			t.Fatalf("%q: %v", line, err)
		}
	}
}

// An interpreter or a workspace script off the allowlist is routed as
// sandboxed project code (always asked for); a missing script is refused.
func TestPolicyRoutesProjectCode(t *testing.T) {
	t.Setenv(sandbox.AllowUnsandboxedEnv, "1")
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "run.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root})
	for _, line := range []string{"python3 - <<EOF\nprint(1)\nEOF", "./run.sh", "ls && bash -c 'echo hi'"} {
		pol, err := classifyLine(line, allowSet("ls"), []string{"ls"}, wsp, root, false)
		if err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		if !pol.sandboxExec || pol.tier != shellTierWrite {
			t.Fatalf("%q: sandboxExec=%v tier=%q", line, pol.sandboxExec, pol.tier)
		}
	}
	if _, err := classifyLine("./missing.sh", allowSet("ls"), []string{"ls"}, wsp, root, false); err == nil {
		t.Fatal("missing script must be refused")
	}
}

// Denials for roots the model retries instead of switching tools carry a hint
// pointing at the right tool: file tools for create/modify, git/file tools for
// delete, and `go run` for a built binary the shell will not execute directly.
func TestPolicyDenialHints(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "mybin"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "data.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write data: %v", err)
	}
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root})

	cases := []struct {
		name    string
		line    string
		want    string
		mustNot []string
	}{
		{
			name: "mkdir points at file tools",
			line: "mkdir out",
			want: "create or change files with edit/apply_patch",
		},
		{
			name: "touch points at file tools",
			line: "touch out.txt",
			want: "create or change files with edit/apply_patch",
		},
		{
			name:    "rm points at edit tools and git",
			line:    "rm data.txt",
			want:    "deleting is not available from the shell",
			mustNot: []string{"create or change files with edit/apply_patch"},
		},
		{
			name:    "rmdir points at edit tools and git",
			line:    "rmdir out",
			want:    "deleting is not available from the shell",
			mustNot: []string{"create or change files with edit/apply_patch"},
		},
		{
			name: "relative built-binary path points at go run",
			line: "./bin/cli",
			want: "use `go run .` / `go run ./cmd/...`",
		},
		{
			name: "absolute built-binary path points at go run",
			line: "/tmp/cli",
			want: "use `go run .` / `go run ./cmd/...`",
		},
		{
			name: "bare name resolving to an executable file points at go run",
			line: "mybin",
			want: "use `go run .` / `go run ./cmd/...`",
		},
		{
			name:    "bare name with no matching file gets no built-binary hint",
			line:    "nosuchcmd",
			mustNot: []string{"go run"},
		},
		{
			name:    "bare name resolving to a non-executable file gets no built-binary hint",
			line:    "data.txt",
			mustNot: []string{"go run"},
		},
		{
			name: "xxd points at read_file",
			line: "xxd data.txt",
			want: "to inspect a file use read_file, or cat/head/tail/grep from the shell",
		},
		{
			name: "od points at read_file",
			line: "od data.txt",
			want: "to inspect a file use read_file, or cat/head/tail/grep from the shell",
		},
		{
			name: "hexdump points at read_file",
			line: "hexdump data.txt",
			want: "to inspect a file use read_file, or cat/head/tail/grep from the shell",
		},
		{
			name: "strings points at read_file",
			line: "strings data.txt",
			want: "to inspect a file use read_file, or cat/head/tail/grep from the shell",
		},
		{
			name: "less points at read_file",
			line: "less data.txt",
			want: "to inspect a file use read_file, or cat/head/tail/grep from the shell",
		},
		{
			name: "more points at read_file",
			line: "more data.txt",
			want: "to inspect a file use read_file, or cat/head/tail/grep from the shell",
		},
		{
			name: "nl points at read_file",
			line: "nl data.txt",
			want: "to inspect a file use read_file, or cat/head/tail/grep from the shell",
		},
		{
			name: "column points at read_file",
			line: "column data.txt",
			want: "to inspect a file use read_file, or cat/head/tail/grep from the shell",
		},
		{
			name: "awk points at read_file",
			line: "awk '{print}' data.txt",
			want: "to inspect a file use read_file, or cat/head/tail/grep from the shell",
		},
		{
			name: "sed points at read_file",
			line: "sed -n '1p' data.txt",
			want: "to inspect a file use read_file, or cat/head/tail/grep from the shell",
		},
		{
			name: "cut points at read_file",
			line: "cut -d: -f1 data.txt",
			want: "to inspect a file use read_file, or cat/head/tail/grep from the shell",
		},
		{
			name: "tr points at read_file",
			line: "tr a-z A-Z < data.txt",
			want: "to inspect a file use read_file, or cat/head/tail/grep from the shell",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := classifyLine(c.line, allowSet("ls"), []string{"ls"}, wsp, root, false)
			if err == nil {
				t.Fatalf("%q must be refused", c.line)
			}
			if c.want != "" && !strings.Contains(err.Error(), c.want) {
				t.Fatalf("%q: want %q in %v", c.line, c.want, err)
			}
			for _, absent := range c.mustNot {
				if strings.Contains(err.Error(), absent) {
					t.Fatalf("%q: unexpected %q in %v", c.line, absent, err)
				}
			}
		})
	}
}

// The model states an invocation in "command", in "args", or in both at
// once; all three must produce the same line.
func TestCommandLineMergesArgs(t *testing.T) {
	cases := []struct{ raw, want string }{
		{`{"command":"ls -la","args":["ls","-la"]}`, "ls -la"},
		{`{"command":"ls","args":["-la"]}`, "ls -la"},
		{`{"command":"ls -la","args":["-la"]}`, "ls -la"},
		{`{"command":"cat -A main.py","args":[]}`, "cat -A main.py"},
		{`{"command":"ls","args":["ls","docs/"]}`, "ls docs/"},
		{`{"command":"python3 gen.py","args":["--out","o.json"]}`, "python3 gen.py --out o.json"},
		{`{"command":"grep","args":["-n","two words","f"]}`, "grep -n 'two words' f"},
		{`{"command":"echo","args":["it's"]}`, `echo 'it'\''s'`},
	}
	for _, c := range cases {
		var args shellArgs
		if err := json.Unmarshal([]byte(c.raw), &args); err != nil {
			t.Fatal(err)
		}
		if got := commandLine(args); got != c.want {
			t.Fatalf("%s: got %q want %q", c.raw, got, c.want)
		}
	}
}

func TestPrepareShellCommandClampsModelSuppliedLimits(t *testing.T) {
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: t.TempDir()})
	call := &core.ToolCall{Name: "shell", Arguments: json.RawMessage(`{"command":"ls","timeout_ms":99999999,"max_output_bytes":99999999}`)}
	plan, err := prepareShellCommand(call, config.ShellConfig{}, wsp, allowSet("ls"), []string{"ls"})
	if err != nil {
		t.Fatalf("prepare shell command: %v", err)
	}
	if plan.timeoutMS != 120000 || plan.maxOutputBytes != 200000 {
		t.Fatalf("limits not clamped: %d %d", plan.timeoutMS, plan.maxOutputBytes)
	}
}

func TestResolveShellCWDStripsQuotesAndHidesHostPath(t *testing.T) {
	root := t.TempDir()
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: root})
	got, err := resolveShellCWD(`"`+root+`"`, wsp)
	if err != nil || got != root {
		t.Fatalf("resolve shell cwd: %q %v", got, err)
	}
	if err := os.Mkdir(filepath.Join(root, "logs"), 0o755); err != nil {
		t.Fatal(err)
	}
	_, err = resolveShellCWD("ws", wsp)
	if err == nil || strings.Contains(err.Error(), root) || !strings.Contains(err.Error(), "logs") {
		t.Fatalf("cwd error must hide the host path and list the root: %v", err)
	}
}

func TestShellDescriptionIsShort(t *testing.T) {
	// Measured 925 on an unsandboxed host (CI), which prefixes a sandbox note.
	if desc := shellDescription(testAllowed, t.TempDir(), false); len(desc) > 930 {
		t.Fatalf("description is %d chars; the contract is bash, it should not enumerate shapes: %s", len(desc), desc)
	}
}

func TestAssignmentPrefixRefused(t *testing.T) {
	allowed := map[string]struct{}{"ls": {}}
	if _, err := classifyLine("PATH=./bin ls", allowed, []string{"ls"}, workspace.Policy{Root: t.TempDir()}, "", false); err == nil {
		t.Fatalf("PATH= prefix must be refused")
	}
	if _, err := classifyLine("N=3; ls", allowed, []string{"ls"}, workspace.Policy{Root: t.TempDir()}, "", false); err != nil {
		t.Fatalf("bare assignment on its own must pass: %v", err)
	}
}

// TestPolicyWidenedRootsAndTimeoutWrapper covers the roots added for the
// commands the model actually reached for, and `timeout` — a wrapper whose
// own tier would launder the command it wraps, so it is classified by the
// inner root instead.
func TestPolicyWidenedRootsAndTimeoutWrapper(t *testing.T) {
	allowed := []string{"ls", "rm", "sed", "awk", "uniq", "file", "timeout", "which", "diff"}
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: t.TempDir()})
	cases := []struct {
		line string
		tier shellTier
	}{
		{"rm x", shellTierWrite},
		{"sed -i s/a/b/ f", shellTierWrite},
		{"sed -i.bak s/a/b/ f", shellTierWrite},
		{"sed -n '1,5p' f", shellTierWrite},
		{"sed s/a/b/ f", shellTierWrite},
		{"sed 'w out' f", shellTierWrite},
		{"sed 'e id' f", shellTierWrite},
		{"awk '{print}' f", shellTierWrite},
		{`awk 'BEGIN{system("id")}'`, shellTierWrite},
		{`awk '{print > "out"}' f`, shellTierWrite},
		{"sed --help", shellTierReadonly},
		{"awk --help", shellTierReadonly},
		{"uniq -c f", shellTierReadonly},
		{"uniq f out", shellTierWrite},
		{"file -b f", shellTierReadonly},
		{"file -C -m magic", shellTierWrite},
		{"which ls", shellTierReadonly},
		{"diff a b", shellTierReadonly},
		{"timeout 5 ls", shellTierReadonly},
		{"timeout -k 1 5 ls", shellTierReadonly},
		{"timeout 5 rm x", shellTierWrite},
	}
	for _, c := range cases {
		pol, err := classifyLine(c.line, allowSet(allowed...), allowed, wsp, wsp.Root, false)
		if err != nil {
			t.Fatalf("%q: %v", c.line, err)
		}
		if pol.tier != c.tier {
			t.Fatalf("%q: tier %q want %q (reasons %v)", c.line, pol.tier, c.tier, pol.reasons)
		}
	}
	for _, line := range []string{"timeout 5", "timeout 5 curl x"} {
		if _, err := classifyLine(line, allowSet(allowed...), allowed, wsp, wsp.Root, false); err == nil {
			t.Fatalf("%q must be refused", line)
		}
	}
}

// TestPolicyTrapAndHashAreOpaque covers builtins that hide the command they
// run: trap stores shell code the walk never parses, and `hash -p` installs a
// lookup override so a later root resolves to something else.
func TestPolicyTrapAndHashAreOpaque(t *testing.T) {
	for _, line := range []string{"trap 'touch /tmp/pwned' EXIT; ls", "hash -p ./x ls; ls"} {
		if _, err := classify(t, line); err == nil {
			t.Fatalf("%q must be refused", line)
		} else if !strings.Contains(err.Error(), "is not allowed") {
			t.Fatalf("%q: refusal must name the allowlist, got %v", line, err)
		}
	}
}

// TestPolicyLoaderAssignmentsRefused covers the assignment spellings that
// reach the same loader variables as a `NAME=value cmd` prefix: a standalone
// assignment, and the builtins that declare variables.
func TestPolicyLoaderAssignmentsRefused(t *testing.T) {
	refused := []string{
		"PATH=/tmp/evil; ls",
		"export PATH=/tmp/evil; ls",
		"declare -x PATH=/tmp/evil; ls",
		"readonly IFS=x; ls",
		"local PATH=/tmp/evil; ls",
		"IFS=x; ls",
	}
	for _, line := range refused {
		if _, err := classify(t, line); err == nil {
			t.Fatalf("%q must be refused", line)
		}
	}
	for _, line := range []string{"N=3; ls", "export N=3; ls", "declare -x N=3; ls"} {
		if _, err := classify(t, line); err != nil {
			t.Fatalf("%q must pass: %v", line, err)
		}
	}
}

// TestPolicyDupRedirectToFileWrites covers `>&word`: only a digit run (or -)
// is a descriptor; anything else names a file bash will write.
func TestPolicyDupRedirectToFileWrites(t *testing.T) {
	pol, err := classify(t, "ls >& out.txt")
	if err != nil {
		t.Fatalf("ls >& out.txt: %v", err)
	}
	if pol.tier != shellTierWrite {
		t.Fatalf("ls >& out.txt: tier %q want %q (reasons %v)", pol.tier, shellTierWrite, pol.reasons)
	}
	pol, err = classify(t, "ls 2>&1")
	if err != nil {
		t.Fatalf("ls 2>&1: %v", err)
	}
	if pol.tier != shellTierReadonly {
		t.Fatalf("ls 2>&1: tier %q want %q (reasons %v)", pol.tier, shellTierReadonly, pol.reasons)
	}
}

// TestPolicyDollarSingleQuoteNotLiteral covers $'...': its escapes are still
// raw at walk time, so reading it as a literal would hide the path it means.
func TestPolicyDollarSingleQuoteNotLiteral(t *testing.T) {
	pol, err := classify(t, `cat $'\x2f\x65\x74\x63\x2fpasswd'`)
	if err != nil {
		t.Fatalf("unexpected refusal: %v", err)
	}
	if pol.tier == shellTierReadonly {
		t.Fatalf("$'...' path must not classify read-only (reasons %v)", pol.reasons)
	}
}

// TestPolicyNothingReachesReadonlyByAccident is the standing gate on the
// auto-approved tier: a line that reaches read-only runs with no human in the
// loop, so every construct that can set a loader variable, hide a command, or
// name a path outside the workspace must refuse or land on the write tier.
func TestPolicyNothingReachesReadonlyByAccident(t *testing.T) {
	notReadonly := []string{
		"trap 'touch /tmp/pwned' EXIT; ls",
		"hash -p ./x ls; ls",
		"PATH=/tmp/evil; ls",
		"export PATH=/tmp/evil; ls",
		"declare -x PATH=/tmp/evil; ls",
		"typeset PATH=/tmp/evil; ls",
		"readonly PATH=/tmp/evil; ls",
		"local PATH=/tmp/evil; ls",
		"IFS=x; ls",
		"ls >& out.txt",
		`cat $'\x2f\x65\x74\x63\x2fpasswd'`,
		"declare -n r=PATH; r=/tmp/evil; ls",
		"typeset -n r=PATH; r=/tmp/evil; ls",
		"printf -v PATH /tmp/evil; ls",
		"read -r PATH <<< /tmp/evil; ls",
		"read PATH < f; ls",
		"getopts x PATH; ls",
		"unset PATH; echo ${PATH:=/tmp/evil}; ls",
		`x='$(touch /tmp/pwn)'; echo "${x@P}"`,
		"cat {/etc/,}passwd",
		"cat ~/.ssh/id_rsa",
		"cat < ~/.ssh/id_rsa",
		"coproc ls",
	}
	for _, line := range notReadonly {
		pol, err := classify(t, line)
		if err != nil {
			continue // refused outright, which is stronger than demoting
		}
		if pol.tier == shellTierReadonly {
			t.Fatalf("%q classified read-only, so it would auto-approve (roots %v)", line, pol.roots)
		}
	}
	readonly := []string{
		"ls",
		"ls 2>&1",
		"ls >&2",
		"ls >&-",
		"N=3 ls",
		"X=1; ls",
		"set -a; X=1; ls",
		"echo hi | grep h",
		"cat file.txt",
	}
	for _, line := range readonly {
		pol, err := classify(t, line)
		if err != nil {
			t.Fatalf("%q must be allowed: %v", line, err)
		}
		if pol.tier != shellTierReadonly {
			t.Fatalf("%q: tier %q want read-only (reasons %v)", line, pol.tier, pol.reasons)
		}
	}
}

// A heredoc body is never brace-expanded by bash, so the braces in the
// model's `cat > f.py <<'EOF'` scripts (dict literals, f-strings, `ns={}`)
// must not be refused as path expansions; a real brace expansion outside the
// heredoc on the same line still is.
func TestBraceExpansionSkipsHeredocBody(t *testing.T) {
	wsp := workspace.Policy{Root: t.TempDir()}
	allowed := []string{"cat", "python3"}
	for _, line := range []string{
		"cat > f.py <<'EOF'\nns={}\nout = {k: v for k, v in d.items()}\nprint(f\"{ns}\")\nEOF",
		"cat > f.py <<EOF\nns={}\nEOF",
		"python3 - <<'EOF'\nrows = [{'a': 1}]\nEOF\ncat f.py",
	} {
		pol, err := classifyLine(line, allowSet(allowed...), allowed, wsp, wsp.Root, false)
		if err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		if pol.tier != shellTierWrite {
			t.Fatalf("%q: tier = %q, want write", line, pol.tier)
		}
	}
	line := "cat {/etc/,}passwd <<'EOF'\nns={}\nEOF"
	if _, err := classifyLine(line, allowSet(allowed...), allowed, wsp, wsp.Root, false); err == nil || !strings.Contains(err.Error(), "brace expansion") {
		t.Fatalf("%q: err = %v, want brace-expansion refusal", line, err)
	}
}

// xargs runs no program of its own, so the policy resolves the command it
// would run and checks that against the same allowlist and tier rules.
func TestPolicyXargsFollowsTargetCommand(t *testing.T) {
	allowed := append([]string{"xargs"}, testAllowed...)
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: t.TempDir()})
	classifyX := func(line string) (policy, error) {
		return classifyLine(line, allowSet(allowed...), allowed, wsp, wsp.Root, false)
	}
	ok := []struct {
		line string
		tier shellTier
	}{
		{"find . -name '*.go' | xargs grep -l TODO", shellTierReadonly},
		{"find . | xargs -0 -n 10 grep -l x", shellTierReadonly},
		{"find . | xargs -I {} cat {}", shellTierReadonly},
		{"ls | xargs", shellTierReadonly},
		// The target keeps its own tier: xargs must not launder it.
		{"ls | xargs chmod +x", shellTierWrite},
	}
	for _, c := range ok {
		pol, err := classifyX(c.line)
		if err != nil {
			t.Fatalf("%q: %v", c.line, err)
		}
		if pol.tier != c.tier {
			t.Fatalf("%q: tier %q want %q", c.line, pol.tier, c.tier)
		}
	}
	refused := []string{
		"find . | xargs rm -f",
		"find . | xargs -n 1 curl",
		"find . | xargs $cmd",
	}
	for _, line := range refused {
		if _, err := classifyX(line); err == nil {
			t.Fatalf("%q must be refused", line)
		}
	}
	// An interpreter is routed into the sandbox rather than refused, but it
	// must never come through xargs as read-only.
	if pol, err := classifyX("find . | xargs sh -c 'cat $1'"); err == nil && pol.tier == shellTierReadonly {
		t.Fatalf("xargs sh must not be read-only")
	}
}

// --allow-all (config: shell.allow_any_command) lifts the executable
// allowlist: an off-list root is classified as write tier for the approver
// instead of being refused, while the default gate still refuses it.
func TestPolicyAllowAnyCommandLiftsTheAllowlist(t *testing.T) {
	wsp := workspace.NewWorkspacePolicy(config.WorkspaceConfig{Root: t.TempDir()})
	const line = "which npm && npm --version"

	if _, err := classifyLine(line, allowSet(testAllowed...), testAllowed, wsp, wsp.Root, false); err == nil {
		t.Fatal("npm allowed without allow-any")
	}
	pol, err := classifyLine(line, allowSet(testAllowed...), testAllowed, wsp, wsp.Root, true)
	if err != nil {
		t.Fatalf("allow-any refused %q: %v", line, err)
	}
	if pol.tier != shellTierWrite {
		t.Fatalf("tier = %q, want %q so the approver decides", pol.tier, shellTierWrite)
	}
}

// A build-tool variable in front of an allowed command picks a build mode, not
// a program: `GOFLAGS=-mod=mod go test ./...` was refused outright, and the
// model had to route around it (round-8 bench, U1/3).
func TestPolicyAllowsBuildToolPrefixOnAnAllowedCommand(t *testing.T) {
	for _, line := range []string{
		"GOFLAGS=-mod=mod go test ./...",
		"CGO_ENABLED=0 GOOS=linux go build ./...",
		"cd app && GOFLAGS=-mod=mod go test ./...",
	} {
		if _, err := classify(t, line); err != nil {
			t.Fatalf("%q must pass: %v", line, err)
		}
	}
}

// The prefix is admitted only where it cannot outlive the command or hide its
// value: a bare assignment, an export, a non-literal value, and every other
// loader variable stay refused.
func TestPolicyBuildToolPrefixStaysNarrow(t *testing.T) {
	for _, line := range []string{
		"GOFLAGS=-mod=mod; go test ./...",
		"export GOFLAGS=-mod=mod; go test ./...",
		"GOFLAGS=$EVIL go test ./...",
		"PATH=/tmp/evil go test ./...",
		"GOFLAGS=-mod=mod curl example.com",
	} {
		if _, err := classify(t, line); err == nil {
			t.Fatalf("%q must be refused", line)
		}
	}
}

// The escape hatch for a line the allowlist parser refuses is only useful if
// the model is told about it (#428).
func TestShellDescriptionNamesScriptEscapeHatch(t *testing.T) {
	desc := shellDescription(testAllowed, t.TempDir(), false)
	if !strings.Contains(desc, "bash /tmp/") {
		t.Fatalf("description does not surface the script escape hatch: %s", desc)
	}
}

// `mkdir -p a b c/{d,e}` is the ordinary way to lay out a tree, and refusing
// it cost a turn on greenfield work (r10 W1/1). A workspace-relative brace
// list in argument position is admitted at the write tier; one that can leave
// the workspace, or one in command position, is still refused.
func TestBraceExpansionAllowedInArgumentPosition(t *testing.T) {
	wsp := workspace.Policy{Root: t.TempDir()}
	allowed := []string{"mkdir", "cat", "touch", "git", "npm"}
	// A brace over plain paths writes; one on a root whose tier depends on its
	// subcommand hides that subcommand from the rules, so it needs the network
	// tier rather than the write tier the unknown `{fetch,}` would earn.
	for line, want := range map[string]shellTier{
		"mkdir -p a b c/{d,e}": shellTierWrite,
		"touch pkg/{a,b}.go":   shellTierWrite,
		"git {fetch,} origin":  shellTierNetwork,
		"npm {install,} x":     shellTierNetwork,
	} {
		pol, err := classifyLine(line, allowSet(allowed...), allowed, wsp, wsp.Root, false)
		if err != nil {
			t.Fatalf("%q: %v", line, err)
		}
		if pol.tier != want {
			t.Fatalf("%q: tier = %q, want %q", line, pol.tier, want)
		}
	}
	for _, line := range []string{"cat {/etc/,}passwd", "cat {../,}outside", "{mkdir,rmdir} a"} {
		if _, err := classifyLine(line, allowSet(allowed...), allowed, wsp, wsp.Root, false); err == nil {
			t.Fatalf("%q: want a refusal", line)
		}
	}
}

// The model writes the workspace's own absolute path with a brace on the end
// (`mkdir -p <root>/frontend/src/{routes,lib}`, r11 W1/1); that names the same
// paths as the relative spelling and is admitted with it. A brace that expands
// outside the root is not.
func TestBraceExpansionAllowsAbsoluteInWorkspacePaths(t *testing.T) {
	root := t.TempDir()
	wsp := workspace.Policy{Root: root}
	allowed := []string{"mkdir", "cat"}
	line := "mkdir -p " + root + "/frontend/src/{routes,lib} " + root + "/frontend/static"
	pol, err := classifyLine(line, allowSet(allowed...), allowed, wsp, root, false)
	if err != nil {
		t.Fatalf("%q: %v", line, err)
	}
	if pol.tier != shellTierWrite {
		t.Fatalf("tier = %q, want write", pol.tier)
	}
	for _, bad := range []string{"cat /etc/{passwd,shadow}", "mkdir -p " + root + "/{a,../../b}"} {
		if _, err := classifyLine(bad, allowSet(allowed...), allowed, wsp, root, false); err == nil {
			t.Fatalf("%q: want a refusal", bad)
		}
	}
}

// A word whose braces multiply out past the cap is not something the policy
// should be reasoning about path by path, so it is refused rather than
// enumerated.
func TestBraceExpansionRefusesPastTheCap(t *testing.T) {
	wsp := workspace.Policy{Root: t.TempDir()}
	allowed := []string{"mkdir"}
	line := "mkdir -p " + strings.Repeat("{a,b}", 7) // 128 paths, cap is 64
	if _, err := classifyLine(line, allowSet(allowed...), allowed, wsp, wsp.Root, false); err == nil {
		t.Fatalf("%q: want a refusal", line)
	}
}

// A variable in command position is rejected with the literal-path form the
// model should write instead; the bare "not a literal" wording repeated within
// runs without teaching it (#571).
func TestVariableCommandNameRejectionTeachesLiteralPath(t *testing.T) {
	_, err := classify(t, "T=./bin/taskctl && $T list")
	if err == nil || !strings.Contains(err.Error(), `"$T" is a variable`) || !strings.Contains(err.Error(), "literal path") || !strings.Contains(err.Error(), "not $BIN list") {
		t.Fatalf("err = %v, want the literal-path re-teach", err)
	}
}
