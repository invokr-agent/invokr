package shell

import (
	"cmp"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/invokr-ai/invokr/support/fsutil"
	"github.com/invokr-ai/invokr/tools/system/workspace"
	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/syntax"
)

// The model writes a bash line and bash runs it (exec.go). Nothing here
// executes anything: the line is parsed into an AST once and walked to answer
// two questions before the sandbox ever sees it — is every command root on the
// allowlist, and which approval tier does the whole line need. The allowlist
// is the outer gate: a root that is not allowed (and not a routed interpreter
// or workspace script) refuses the line, exactly as the argv-only tool did.
// The walk is an allowlist over AST node types: inertNodes and the cases in
// visit are the only constructs the policy understands. Any other node type —
// an arithmetic command, a coprocess, a brace expansion, a [[ ]] test — refuses
// the line rather than passing through unrecognised, so supporting a new
// construct is an explicit edit here. Constructs the walk understands but
// cannot classify statically (a substitution, a background job) fail closed to
// the write tier, so the human sees them before they run.

// policy is what the walk decides about one line.
type policy struct {
	publishes bool
	tier      shellTier
	// reasons name what forced the tier, for the approval card and the audit.
	reasons []string
	// roots are the command names the line runs, in order, deduplicated.
	roots []string
	// sandboxExec marks a root that is not allowlisted but routed into the
	// sandbox as project code (an interpreter or a workspace script).
	sandboxExec bool
}

// harmlessBuiltins run inside bash without reaching the allowlist: they touch
// no file and start no program.
var harmlessBuiltins = []string{"cd", "true", "false", "test", "[", "[[", "printf", "exit", "return", "break", "continue", ":", "local", "export", "unset", "shift", "read", "set", "wait", "type", "declare", "let", "readonly", "getopts", "times", "help"}

// opaqueBuiltins hide the command they run from the walk, so the line is
// refused rather than classified.
var opaqueBuiltins = []string{"eval", "exec", "source", ".", "command", "builtin", "enable", "trap", "hash"}

// classifyLine parses command and walks it. baseDir is the directory the call
// runs in, used to resolve a workspace script; wsp resolves script paths. With
// allowAny set, a root that is not allowlisted is demoted to the write tier
// instead of refused, so the approver (not the allowlist) decides.
func classifyLine(command string, allowed map[string]struct{}, allowedList []string, wsp workspace.Policy, baseDir string, allowAny bool) (policy, error) {
	file, err := syntax.NewParser(syntax.KeepComments(false), syntax.Variant(syntax.LangBash)).
		Parse(strings.NewReader(command), "")
	if err != nil {
		return policy{}, fmt.Errorf("command %q is not valid bash (%v); fix the syntax and resend", shellCommandLabel(command), err)
	}
	refused, brace := braceExpansion(file, wsp)
	if refused != "" {
		return policy{}, fmt.Errorf("command %q is not allowed: %q is a brace expansion, which the policy cannot resolve to the paths it names; write them out", shellCommandLabel(command), refused)
	}
	w := policyWalker{line: command, allowed: allowed, allowedList: allowedList, allowAny: allowAny, wsp: wsp, baseDir: baseDir, pol: policy{tier: shellTierReadonly}}
	if err := w.walk(file); err != nil {
		return policy{}, err
	}
	if brace {
		w.demote(braceTier(w.pol.roots), "expands a brace list")
	}
	if len(w.pol.roots) == 0 && !w.sawBuiltin {
		return policy{}, fmt.Errorf("command %q runs nothing", shellCommandLabel(command))
	}
	return w.pol, nil
}

type policyWalker struct {
	line        string
	allowed     map[string]struct{}
	allowedList []string
	// allowAny lifts the allowlist gate: see classifyLine.
	allowAny bool
	wsp      workspace.Policy
	baseDir  string
	pol      policy
	// prefixAssigns names the build-tool variables set as a prefix on an
	// allowed command in this line (see allowBuildPrefixes).
	prefixAssigns []string
	// sawBuiltin records a harmless builtin as the line's only command
	// (`printf`, `test -f x`, `true`): the line runs something, so it must
	// not be refused as empty.
	sawBuiltin bool
}

func (w *policyWalker) addRoot(root string) {
	if !slices.Contains(w.pol.roots, root) {
		w.pol.roots = append(w.pol.roots, root)
	}
}

func (w *policyWalker) demote(tier shellTier, reason string) {
	if tierRank(tier) > tierRank(w.pol.tier) {
		w.pol.tier = tier
	}
	if !slices.Contains(w.pol.reasons, reason) {
		w.pol.reasons = append(w.pol.reasons, reason)
	}
}

// braceTier is the tier an admitted brace expansion forces. A root whose tier
// depends on its subcommand reads the unexpanded `{fetch,}` as an unknown
// subcommand, which classifies as write — so `git {fetch,} origin` would run
// unprompted wherever writes are allowed and the network is not. Any such root
// on the line lifts the whole line to the network tier; a brace over plain
// paths (`mkdir -p c/{d,e}`) stays at write.
func braceTier(roots []string) shellTier {
	for _, root := range roots {
		if rule := commandRules[root]; len(rule.Network) > 0 || rule.AlwaysNetwork {
			return shellTierNetwork
		}
	}
	return shellTierWrite
}

// tierRanks orders the tiers; an unlisted tier ranks 0 (read-only).
var tierRanks = map[shellTier]int{shellTierNetwork: 2, shellTierWrite: 1}

func tierRank(t shellTier) int { return tierRanks[t] }

// walk visits every node once and refuses any node type the policy does not
// explicitly understand.
func (w *policyWalker) walk(file *syntax.File) error {
	var walkErr error
	syntax.Walk(file, func(n syntax.Node) bool {
		if walkErr != nil || n == nil {
			return false
		}
		walkErr = w.visit(n)
		return walkErr == nil
	})
	return walkErr
}

// visit classifies one node. Every node type the policy understands has a case
// here or in inertNodes; anything else refuses the line.
func (w *policyWalker) visit(n syntax.Node) error {
	switch x := n.(type) {
	case *syntax.CallExpr:
		w.pol.publishes = w.pol.publishes || publishesCall(x)
		return w.call(x)
	case *syntax.DeclClause:
		return w.declClause(x)
	case *syntax.Assign:
		return w.assign(x)
	case *syntax.ParamExp:
		return w.paramExp(x)
	case *syntax.Redirect:
		w.redirect(x)
	case *syntax.WordIter:
		// `for NAME in ...` and `select NAME in ...` set NAME on every
		// iteration. The name is a bare Lit, never an Assign, so it reaches
		// no other gate.
		return w.assignsVariable(x.Name.Value)
	default:
		if !w.demoteNode(n) && !inertNode(n) {
			return w.refuseNode(n)
		}
	}
	return nil
}

// demoteNode handles the node types the walk understands but cannot classify
// statically: they pass, at the write tier, so a human sees them. It reports
// whether the node was one of them.
func (w *policyWalker) demoteNode(n syntax.Node) bool {
	switch x := n.(type) {
	case *syntax.SglQuoted:
		if x.Dollar {
			w.demote(shellTierWrite, "uses a $'...' escape")
		}
	case *syntax.CmdSubst:
		w.demote(shellTierWrite, "runs a command substitution")
	case *syntax.ProcSubst:
		w.demote(shellTierWrite, "runs a process substitution")
	case *syntax.Stmt:
		if x.Background || x.Coprocess {
			w.demote(shellTierWrite, "starts a background job")
		}
	default:
		return false
	}
	return true
}

// inertNode reports whether a node type carries no policy meaning of its own:
// grouping and control flow whose parts are walked anyway, and the literal
// pieces of a word. Adding a type here admits every line that can contain it.
func inertNode(n syntax.Node) bool {
	switch n.(type) {
	case *syntax.File, *syntax.Word, *syntax.Lit, *syntax.DblQuoted, *syntax.Comment,
		*syntax.BinaryCmd, *syntax.Subshell, *syntax.Block,
		*syntax.IfClause, *syntax.ForClause, *syntax.WhileClause,
		*syntax.CaseClause, *syntax.CaseItem:
		return true
	}
	return false
}

func (w *policyWalker) refuseNode(n syntax.Node) error {
	return fmt.Errorf("command %q is not allowed: it uses a shell construct the policy does not understand (%T); rewrite it as plain commands", shellCommandLabel(w.line), n)
}

// expandingBrace matches a brace pair bash would actually expand: one holding
// a comma list or a `..` range. A bare `{}` (find -exec cat {} +) is a literal.
var expandingBrace = regexp.MustCompile(`\{[^{}]*(,|\.\.)[^{}]*\}`)

// braceExpansion inspects the brace expansions in a line. It reports the first
// word the policy cannot admit, and whether an admissible one was seen (the
// line is then demoted, so the approver sees it).
//
// A brace expansion naming workspace paths is the ordinary way to make a tree
// (`mkdir -p a b c/{d,e}`), and refusing it cost a turn on greenfield work
// (r10 W1/1). One whose alternatives can leave the workspace
// (`cat {/etc/,}passwd`) is still refused: the policy cannot resolve what
// those name. A `..` anywhere is refused with them, which also refuses ranges
// (`a{1..3}`) — a range is rare enough not to be worth telling apart from a
// path escape. A brace in command position needs no case of its own: the
// unexpanded word is not on the allowlist, so the line is refused there.
//
// The default parser reports `{/etc/,}passwd` as a single Lit, so without this
// the word reads as a literal workspace-relative path. SplitBraces runs on a
// copy of the word: syntax.Walk has no case for the BraceExp nodes it would
// introduce. Heredoc bodies are skipped: bash never brace-expands them, and
// the model's `cat > f.py <<'EOF'` scripts are full of dict literals and
// f-strings that would otherwise be refused as path expansions (round-6 bench,
// 21 of 31 shell failures).
func braceExpansion(file *syntax.File, wsp workspace.Policy) (string, bool) {
	scan := braceScan{wsp: wsp}
	syntax.Walk(file, scan.visit)
	return scan.refused, scan.admitted
}

// braceScan carries the state of one braceExpansion walk: the first word the
// policy refuses, and whether an admissible expansion was seen.
type braceScan struct {
	refused  string
	admitted bool
	wsp      workspace.Policy
}

func (s *braceScan) visit(n syntax.Node) bool {
	switch x := n.(type) {
	case *syntax.Redirect:
		return x.Hdoc == nil
	case *syntax.Word:
		s.word(x)
	}
	return s.refused == ""
}

// maxBraceExpansions caps the enumeration: a word producing more paths than
// this is not something the policy should be reasoning about literally.
const maxBraceExpansions = 64

// word admits one brace expansion when every path it enumerates is one the
// policy would have admitted written out. The model writes both shapes —
// `c/{d,e}` and the workspace's own absolute path with a brace on the end
// (r11 W1/1) — and the second is no more dangerous than the first. Nested
// braces enumerate like any other.
func (s *braceScan) word(word *syntax.Word) {
	src := wordSource(word)
	clone := syntax.Word{Parts: slices.Clone(word.Parts)}
	if !syntax.SplitBraces(&clone) || !expandingBrace.MatchString(src) {
		return
	}
	if strings.Contains(src, "..") {
		s.refused = src
		return
	}
	seen := 0
	for expanded, err := range expand.BracesSeq(nil, &clone) {
		seen++
		if err != nil || seen > maxBraceExpansions || !s.insideWorkspace(wordSource(expanded)) {
			s.refused = src
			return
		}
	}
	s.admitted = seen > 0
}

// insideWorkspace reports whether one enumerated path stays in the workspace:
// a relative path does by construction, an absolute one only under the root.
func (s *braceScan) insideWorkspace(path string) bool {
	if !filepath.IsAbs(path) {
		return true
	}
	_, inside := fsutil.RelInside(s.wsp.Root, filepath.Clean(path))
	return s.wsp.Root != "" && inside
}

// call classifies one command by its root. The root must be a literal word:
// a variable in command position cannot be checked against the allowlist.
func (w *policyWalker) call(c *syntax.CallExpr) error {
	if len(c.Args) == 0 {
		return nil // a bare assignment
	}
	root, ok := literalWord(c.Args[0])
	if !ok {
		return fmt.Errorf("command %q is not allowed: the command name %q is a variable and cannot be checked against the allowlist; call the program by its literal path (e.g. ./bin/tool list, not $BIN list) and keep variables out of the command position", shellCommandLabel(w.line), wordSource(c.Args[0]))
	}
	if handled, err := w.wrapper(root, c); handled {
		return err
	}
	args, argsLiteral := w.argWords(c.Args[1:])
	if builtin, err := w.builtin(root, args, argsLiteral); builtin || err != nil {
		return err
	}
	w.addRoot(root)
	// Prefixes are recorded before the allowlist gate: an interpreter routed
	// into the sandbox is not on the allowlist, and `PYTHONPATH=. python3 x.py`
	// is how the model runs a script that imports the workspace.
	w.allowBuildPrefixes(c)
	if _, allowed := w.allowed[root]; !allowed {
		return w.route(root)
	}
	tier := classifyShellCommand(root, args)
	if !argsLiteral && ruleInspectsFlags(root) {
		tier = maxTier(tier, shellTierWrite)
	}
	if tier != shellTierReadonly {
		w.demote(tier, fmt.Sprintf("%s is %s", root, tierWord(tier)))
	}
	return nil
}

// wrapperRule describes a root that runs no program of its own. valueFlags are
// the wrapper's flags that consume the next word (the attached spellings —
// -n2, -I{}, --max-args=2 — carry their value in the flag word and need no
// entry); positional counts the non-flag words that belong to the wrapper
// itself (timeout's duration); needsCommand refuses a wrapper left with
// nothing to run, which xargs allows because it then runs echo.
type wrapperRule struct {
	valueFlags   []string
	positional   int
	needsCommand bool
}

// wrapperRules classifies `timeout [flags] <duration> <cmd> ...` and
// `xargs [flags] [cmd ...]` by the command they wrap: neither reads nor
// writes, so tiering them read-only would launder `xargs rm` past the
// allowlist. The wrapper's own flags are skipped, then the rest is classified
// as if it had been called directly.
var wrapperRules = map[string]wrapperRule{"timeout": {valueFlags: []string{"-k", "-s", "--signal", "--kill-after"}, positional: 1, needsCommand: true}, "xargs": {valueFlags: []string{"-a", "-d", "-E", "-I", "-i", "-L", "-l", "-n", "-P", "-s", "--arg-file", "--delimiter", "--eof", "--replace", "--max-lines", "--max-args", "--max-procs", "--max-chars"}}}

// wrapper handles the roots that run no program of their own: the line is
// classified by the command they wrap, so an allowlisted wrapper cannot
// launder a refused or write-tier target. It reports whether root was one.
func (w *policyWalker) wrapper(root string, c *syntax.CallExpr) (bool, error) {
	rule, isWrapper := wrapperRules[root]
	if _, allowed := w.allowed[root]; !allowed || !isWrapper {
		return false, nil
	}
	w.addRoot(root)
	rest, err := w.skipWrapperFlags(root, c.Args[1:], rule.valueFlags, rule.positional)
	if err != nil {
		return true, err
	}
	if len(rest) == 0 {
		if rule.needsCommand {
			return true, fmt.Errorf("command %q is not allowed: %s wraps no command", shellCommandLabel(w.line), root)
		}
		return true, nil
	}
	return true, w.call(&syntax.CallExpr{Args: rest})
}

// skipWrapperFlags drops a wrapper's leading flags (and the next word after
// any of valueFlags), then positional further non-flag words, and returns
// what is left: the wrapped command. Every word it inspects must be literal,
// or the wrapped command cannot be checked.
func (w *policyWalker) skipWrapperFlags(wrapper string, rest []*syntax.Word, valueFlags []string, positional int) ([]*syntax.Word, error) {
	for len(rest) > 0 {
		word, ok := literalWord(rest[0])
		if !ok {
			return nil, fmt.Errorf("command %q is not allowed: %s's arguments are not literal, so the command it runs cannot be checked", shellCommandLabel(w.line), wrapper)
		}
		if !strings.HasPrefix(word, "-") {
			return rest[positional:], nil
		}
		rest = rest[1:]
		if slices.Contains(valueFlags, word) && len(rest) > 0 {
			rest = rest[1:]
		}
	}
	return rest, nil
}

// builtin handles a bash builtin: harmless ones pass without touching the
// allowlist, opaque ones refuse the line. It reports whether root was one.
func (w *policyWalker) builtin(root string, args []string, argsLiteral bool) (bool, error) {
	if slices.Contains(opaqueBuiltins, root) {
		return true, fmt.Errorf("command %q is not allowed: %q hides the command it runs; call the command directly", shellCommandLabel(w.line), root)
	}
	if !slices.Contains(harmlessBuiltins, root) {
		return false, nil
	}
	w.sawBuiltin = true
	if root == "cd" && cdLeavesWorkspace(args, argsLiteral) {
		w.demote(shellTierWrite, "changes directory outside the workspace")
	}
	return true, w.builtinAssigns(root, args)
}

// cdLeavesWorkspace reports whether a `cd` may land outside the workspace.
// Only one literal, non-flag, non-escaping argument keeps the read-only tier:
// bare `cd` goes to $HOME, `cd -` to $OLDPWD, a flag hides the path in a later
// argument, and a non-literal path is unknowable until bash expands it.
func cdLeavesWorkspace(args []string, argsLiteral bool) bool {
	return len(args) != 1 || !argsLiteral || strings.HasPrefix(args[0], "-") || argEscapesWorkspace(args[0])
}

// route admits an interpreter or a workspace script that is not on the
// allowlist as sandboxed project code; anything else is refused with the
// allowlist, as before.
func (w *policyWalker) route(root string) error {
	label := shellCommandLabel(w.line)
	if w.allowAny {
		w.demote(shellTierWrite, root+" is not on the allowlist")
		return nil
	}
	if sandboxExecCandidate(root, w.wsp, w.baseDir) {
		if !sandboxExecReady() {
			return fmt.Errorf("command %q is not allowed; %s", label, interpreterNote(root))
		}
		w.pol.sandboxExec = true
		w.demote(shellTierWrite, root+" runs project code")
		return nil
	}
	msg := fmt.Sprintf(`command %q is not allowed: %q is not an allowed executable. Allowed executables: %s`, label, root, strings.Join(w.allowedList, ", "))
	switch root {
	case "mkdir", "touch", "cp", "mv", "tee":
		// Models retry these instead of switching tools; point at the file tools.
		msg += "; create or change files with edit/apply_patch (parent directories are created for you)"
	case "rm", "rmdir":
		// Deleting has no file-tool equivalent; point at editing a tracked
		// file's contents away, or git if the deletion itself is the goal.
		msg += "; deleting is not available from the shell — remove a file's contents with edit/apply_patch, or use git (`git rm`, `git checkout --`) if the file is tracked"
	case "xxd", "od", "hexdump", "strings", "less", "more", "nl", "column", "awk", "sed", "cut", "tr":
		// Models reach for these to inspect a file; point at the native tool
		// and the always-allowed inspection builtins instead.
		msg += "; to inspect a file use read_file, or cat/head/tail/grep from the shell"
	default:
		if looksLikeBuiltBinary(root, w.wsp, w.baseDir) {
			// A path the sandbox route above already rejected, or a bare name
			// that resolves to a regular executable in the workspace: either
			// way it is project code the model built, not something to exec.
			msg += "; built binaries cannot be executed directly — use `go run .` / `go run ./cmd/...` (or `go test`) instead"
		}
	}
	return errors.New(msg)
}

// redirect classifies a redirection: stderr to /dev/null or into stdout, and
// anything to /dev/null, stay read-only; stdin from a workspace file and
// heredocs feed input; every other redirect writes somewhere.
func (w *policyWalker) redirect(r *syntax.Redirect) {
	target, literal := literalWord(r.Word)
	switch r.Op {
	case syntax.Hdoc, syntax.DashHdoc, syntax.WordHdoc:
		return
	case syntax.RdrIn:
		if !literal || argEscapesWorkspace(target) {
			w.demote(shellTierWrite, "reads from a path outside the workspace")
		}
		return
	case syntax.DplOut, syntax.DplIn:
		if literal && fdTarget(target) {
			return // a real fd dup, e.g. 2>&1
		}
		fallthrough // `ls >& out.txt` names a file, not a descriptor
	case syntax.RdrOut, syntax.AppOut, syntax.RdrClob, syntax.RdrAll, syntax.AppAll:
		if literal && target == "/dev/null" {
			return
		}
		w.demote(shellTierWrite, "redirects output to a file")
	default:
		w.demote(shellTierWrite, "uses a "+r.Op.String()+" redirect")
	}
}

// argWords renders the call's arguments for the tier rules. A word with an
// expansion is rendered as its source text; argsLiteral reports whether every
// word was a plain literal, so a rule that inspects flags can fail closed on
// a flag hiding in a variable.
func (w *policyWalker) argWords(words []*syntax.Word) ([]string, bool) {
	out := make([]string, 0, len(words))
	literal := true
	for _, word := range words {
		text, ok := literalWord(word)
		if !ok {
			literal = false
			text = wordSource(word)
		}
		out = append(out, text)
	}
	return out, literal
}

// literalWord returns the word's text when it is made only of literals and
// quotes — no expansion of any kind.
func literalWord(word *syntax.Word) (string, bool) {
	if word == nil {
		return "", false
	}
	var b strings.Builder
	for _, part := range word.Parts {
		switch p := part.(type) {
		case *syntax.Lit:
			b.WriteString(p.Value)
		case *syntax.SglQuoted:
			if p.Dollar {
				return "", false // $'...' still holds escapes; fail closed
			}
			b.WriteString(p.Value)
		case *syntax.DblQuoted:
			text, ok := literalWord(&syntax.Word{Parts: p.Parts})
			if !ok {
				return "", false
			}
			b.WriteString(text)
		default:
			return "", false
		}
	}
	return b.String(), true
}

// fdTarget reports whether a dup target names a file descriptor rather than
// a file: bash accepts a digit run or "-" (close).
func fdTarget(target string) bool {
	if target == "-" {
		return true
	}
	if target == "" {
		return false
	}
	return strings.IndexFunc(target, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

func wordSource(word *syntax.Word) string {
	var b strings.Builder
	_ = syntax.NewPrinter().Print(&b, word)
	return b.String()
}

// ruleInspectsFlags reports whether a root's tier depends on its flags, so a
// non-literal argument could smuggle one past classification.
func ruleInspectsFlags(root string) bool {
	rule, ok := commandRules[root]
	return ok && (rule.ReadFlags != nil || len(rule.WriteFlags) > 0 || len(rule.Readonly) > 0)
}

func maxTier(a, b shellTier) shellTier {
	if tierRank(b) > tierRank(a) {
		return b
	}
	return a
}

// tierWords name a tier in an approval reason.
var tierWords = map[shellTier]string{shellTierNetwork: "a network command", shellTierWrite: "a write command"}

func tierWord(t shellTier) string { return cmp.Or(tierWords[t], "read-only") }

// loaderVariables are the environment names that change which program a
// command resolves to or what it loads before running — the names that would
// let a read-only line run model-written code without an approval.
var loaderVariables = []string{"PATH", "BASH_ENV", "ENV", "IFS", "CDPATH", "SHELLOPTS", "BASHOPTS", "NODE_OPTIONS", "PYTHONSTARTUP", "PYTHONPATH", "PERL5OPT", "PERL5LIB", "RUBYOPT", "GOFLAGS", "GIT_EXEC_PATH", "GIT_CONFIG_PARAMETERS"}

// buildToolVariables are read-only build-tool settings the model routinely
// puts in front of a command (`GOFLAGS=-mod=mod go test ./...`). They pick a
// build mode, not a program to run, so they are admitted — but only as a
// prefix on a command the allowlist already accepts, never as a bare
// assignment that would outlive the line.
var buildToolVariables = []string{"GOFLAGS", "GOOS", "GOARCH", "CGO_ENABLED", "GOWORK", "GOPROXY", "NODE_ENV", "CI"}

// libraryPathVariables tell an interpreter where to find *libraries*, not
// which interpreter to run. `PYTHONPATH=. python3 tests.py` is the ordinary
// way to run a script against the workspace package, and refusing it outright
// left the model with no way to do that at all (r9 L1/1). They are still
// loader variables, so a literal prefix on one command is admitted at the
// write tier — the approver sees the line — and never as a bare assignment
// that would outlive it.
var libraryPathVariables = []string{"PYTHONPATH", "PERL5LIB"}

// allowBuildPrefixes records the `NAME=value` prefixes of one call as
// pre-approved, so the Assign nodes the walk reaches next pass the loader-name
// gate. Only literal values qualify: a non-literal value cannot be read, and
// GOFLAGS is a loader variable precisely because its value can name a program.
func (w *policyWalker) allowBuildPrefixes(c *syntax.CallExpr) {
	for _, a := range c.Assigns {
		if a.Name == nil || a.Value == nil {
			continue
		}
		if _, ok := literalWord(a.Value); !ok {
			continue
		}
		if slices.Contains(libraryPathVariables, a.Name.Value) {
			w.demote(shellTierWrite, "sets "+a.Name.Value+" for one command")
		} else if !slices.Contains(buildToolVariables, a.Name.Value) {
			continue
		}
		w.prefixAssigns = append(w.prefixAssigns, a.Name.Value)
	}
}

// assign is the one gate every variable assignment passes through: a bare
// `NAME=value`, a `NAME=value cmd` prefix, and each assignment inside a
// declare/export/local/readonly clause all reach the walk as an Assign.
func (w *policyWalker) assign(a *syntax.Assign) error {
	if a.Name == nil {
		return nil
	}
	return w.assignsVariable(a.Name.Value)
}

// assignsVariable refuses setting a name that would change which program runs
// or what it loads. Every construct that can set a variable — an Assign, a
// declare clause, read/printf -v/getopts, an assigning ${x:=y} — funnels here.
func (w *policyWalker) assignsVariable(name string) error {
	// bash accepts NAME[idx] wherever it accepts NAME, and $NAME expands to
	// element 0 -- so a subscript reaches the same variable. Normalising here
	// covers every caller rather than each spelling separately.
	name, _, _ = strings.Cut(name, "[")
	if slices.Contains(w.prefixAssigns, name) {
		return nil
	}
	if !loaderVariable(name) {
		return nil
	}
	return fmt.Errorf("command %q is not allowed: setting %s would change which program runs or how it loads", shellCommandLabel(w.line), name)
}

// assigningBuiltins name a variable in their arguments rather than with `=`.
// Every non-flag argument is treated as a name: over-broad on purpose, since a
// flag value read as a name only ever refuses more.
var assigningBuiltins = []string{"read", "getopts", "mapfile", "readarray"}

// builtinAssigns gates the builtins that assign to a variable named in argv.
func (w *policyWalker) builtinAssigns(root string, args []string) error {
	if root == "printf" {
		return w.printfAssigns(args)
	}
	if !slices.Contains(assigningBuiltins, root) {
		return nil
	}
	for _, arg := range args {
		if err := w.assignsVariable(flagRemainder(arg)); err != nil {
			return err
		}
	}
	return nil
}

// flagRemainder strips a flag word's leading dashes and option letters, so an
// attached name (`read -aPATH`) is gated like a detached one. A flag value read
// as a name only ever refuses more, which is the trade this file already makes.
func flagRemainder(arg string) string {
	if !strings.HasPrefix(arg, "-") {
		return arg
	}
	return strings.TrimLeft(arg, "-adeinprstuN0123456789")
}

// printfAssigns gates `printf -v NAME`, which is a general assignment.
func (w *policyWalker) printfAssigns(args []string) error {
	for i, arg := range args {
		if !strings.HasPrefix(arg, "-v") {
			continue
		}
		name := strings.TrimPrefix(arg, "-v")
		if name == "" && i+1 < len(args) {
			name = args[i+1]
		}
		if err := w.assignsVariable(name); err != nil {
			return err
		}
	}
	return nil
}

// declClause checks `export`/`declare`/`readonly`/`local` clauses. The names
// they assign are gated by assign; what is left is the nameref flag, which
// makes a later plain `r=...` write whatever name r holds, and a value that
// names a loader variable, which is how a nameref is pointed at one.
func (w *policyWalker) declClause(d *syntax.DeclClause) error {
	for _, a := range d.Args {
		flag, isFlag := literalWord(a.Value)
		if a.Name == nil && isFlag && namerefFlag(flag) {
			return fmt.Errorf("command %q is not allowed: %s %s declares a nameref, so a later plain assignment can set any variable", shellCommandLabel(w.line), d.Variant.Value, flag)
		}
		if a.Name != nil && isFlag {
			if err := w.assignsVariable(flag); err != nil {
				return err
			}
		}
	}
	return nil
}

// namerefFlag reports whether a declare-style flag word carries -n.
func namerefFlag(flag string) bool {
	if flag == "--nameref" {
		return true
	}
	return strings.HasPrefix(flag, "-") && !strings.HasPrefix(flag, "--") && strings.Contains(flag, "n")
}

// paramExpTransforms are the ${x@OP} operators the policy allows: Q quotes and
// E unescapes into data. P re-parses the value as shell and expands it — a
// command substitution with no CmdSubst node in the AST — and A/a round-trip
// through declare, so both are refused.
var paramExpTransforms = []string{"Q", "E"}

// paramExp gates a parameter expansion: the assigning forms set a variable, and
// the @-transforms can execute the value.
func (w *policyWalker) paramExp(p *syntax.ParamExp) error {
	if p.Exp == nil {
		return nil
	}
	switch p.Exp.Op {
	case syntax.AssignUnset, syntax.AssignUnsetOrNull:
		if p.Param != nil {
			return w.assignsVariable(p.Param.Value)
		}
	case syntax.OtherParamOps:
		op, ok := literalWord(p.Exp.Word)
		if !ok || !slices.Contains(paramExpTransforms, op) {
			return fmt.Errorf("command %q is not allowed: ${...@%s} can re-parse and run the value it expands", shellCommandLabel(w.line), op)
		}
	}
	return nil
}

func loaderVariable(name string) bool {
	return slices.Contains(loaderVariables, name) || strings.HasPrefix(name, "LD_") || strings.HasPrefix(name, "DYLD_")
}
