package shell

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/invokr-agent/invokr/tools/core"
)

// pather turns one path argument into the native tool's path, relative to the
// shell call's cwd, or reports that it is not a plain path.
type pather func(string) (string, bool)

// rerouter maps one command's arguments onto a native tool call.
type rerouter func(args []string, path pather) (name string, target any, ok bool)

var reroutes = map[string]rerouter{
	"cat": readPaths, "nl": readPaths, "less": readPaths, "more": readPaths,
	"head": readHead, "sed": readSedRange, "grep": grepSearch, "git": gitRead, "rm": rmFile,
}

// Reroute turns a plain shell line that a native tool covers into a call to
// that tool; the returned call's Note tells the model to call it directly.
// Anything else — pipes, redirects, expansions, flags the native tool cannot
// honour, or a target that is not available — comes back unchanged.
func Reroute(call *core.ToolCall, available func(string) bool) *core.ToolCall {
	if call == nil || call.Name != "shell" {
		return call
	}
	var args shellArgs
	if json.Unmarshal(call.Arguments, &args) != nil || len(args.Args) > 0 || args.Background || args.Network {
		return call
	}
	words, ok := catChain(plainCommands(args.Command))
	if !ok || reroutes[words[0]] == nil {
		return call
	}
	name, target, ok := reroutes[words[0]](words[1:], cwdPath(args.Cwd))
	if !ok || !available(name) {
		return call
	}
	data, _ := json.Marshal(target) // maps and structs of strings and ints
	note := fmt.Sprintf("note: shell `%s` was run as the %s tool instead; call %s directly next time.", shellCommandLabel(args.Command), name, name)
	return &core.ToolCall{Name: name, Arguments: data, Managed: call.Managed, Note: note}
}

// plainCommands returns the words of each command in a line of commands
// joined by && or ;, each with literal arguments: no pipe, other list,
// redirect, assignment or expansion. Anything else is nil.
func plainCommands(line string) [][]string {
	file, err := syntax.NewParser(syntax.KeepComments(false), syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(line), "")
	if err != nil {
		return nil
	}
	var cmds [][]string
	for _, stmt := range file.Stmts {
		if !chainWords(stmt, &cmds) {
			return nil
		}
	}
	return cmds
}

func chainWords(stmt *syntax.Stmt, cmds *[][]string) bool {
	if stmt.Negated || stmt.Background || stmt.Coprocess || len(stmt.Redirs) > 0 {
		return false
	}
	if chain, ok := stmt.Cmd.(*syntax.BinaryCmd); ok && chain.Op == syntax.AndStmt {
		return chainWords(chain.X, cmds) && chainWords(chain.Y, cmds)
	}
	cmd, ok := stmt.Cmd.(*syntax.CallExpr)
	if !ok || len(cmd.Assigns) > 0 || len(cmd.Args) == 0 {
		return false
	}
	words, literal := argWords(cmd.Args)
	*cmds = append(*cmds, words)
	return literal
}

// catChain folds `echo -- a -- && cat a; cat b` into one `cat a b`: the
// echo separators only label files, which read's per-path headers already do.
// A chain of anything else is not one native call.
func catChain(cmds [][]string) ([]string, bool) {
	cmds = slices.DeleteFunc(cmds, func(words []string) bool { return words[0] == "echo" })
	if len(cmds) == 1 {
		return cmds[0], true
	}
	words := []string{"cat"}
	for _, cmd := range cmds {
		if !slices.Contains([]string{"cat", "nl", "less", "more"}, cmd[0]) {
			return nil, false
		}
		words = append(words, cmd[1:]...)
	}
	return words, len(cmds) > 1
}

// cwdPath accepts a path with no flag, glob or home prefix and joins a
// relative one onto cwd.
func cwdPath(cwd string) pather {
	return func(arg string) (string, bool) {
		if arg == "" || strings.HasPrefix(arg, "-") || strings.ContainsAny(arg, "*?[{~") {
			return "", false
		}
		if cwd == "" || filepath.IsAbs(arg) {
			return arg, true
		}
		return filepath.Join(cwd, arg), true
	}
}

func allPaths(args []string, path pather) ([]string, bool) {
	paths := make([]string, 0, len(args))
	for _, arg := range args {
		p, ok := path(arg)
		if !ok {
			return nil, false
		}
		paths = append(paths, p)
	}
	return paths, len(paths) > 0
}

func readPaths(args []string, path pather) (string, any, bool) {
	paths, ok := allPaths(args, path)
	return "read", map[string]any{"paths": paths}, ok
}

// readHead maps `head [-n N | -N] FILE`.
func readHead(args []string, path pather) (string, any, bool) {
	count := "10"
	switch {
	case len(args) == 3 && args[0] == "-n":
		count, args = args[1], args[2:]
	case len(args) == 2 && strings.HasPrefix(args[0], "-"):
		count, args = args[0][1:], args[1:]
	}
	limit, err := strconv.Atoi(count)
	if err != nil || limit < 1 || len(args) != 1 {
		return "", nil, false
	}
	p, ok := path(args[0])
	return "read", map[string]any{"paths": []string{p}, "limit": limit}, ok
}

var sedPrint = regexp.MustCompile(`^-n\x00(\d+)(?:,(\d+))?p\x00([^\x00]+)$`)

// readSedRange maps `sed -n 'A,Bp' FILE` and `sed -n 'Ap' FILE`.
func readSedRange(args []string, path pather) (string, any, bool) {
	m := sedPrint.FindStringSubmatch(strings.Join(args, "\x00"))
	if m == nil {
		return "", nil, false
	}
	from, err := strconv.Atoi(m[1])
	to := from
	if m[2] != "" && err == nil {
		to, err = strconv.Atoi(m[2])
	}
	if err != nil || from < 1 || to < from {
		return "", nil, false
	}
	p, ok := path(m[3])
	return "read", map[string]any{"paths": []string{p}, "offset": from, "limit": to - from + 1}, ok
}

// grepSearch maps `grep [-rRnFE] PATTERN [PATH]` onto file_search. A basic
// regex is rerouted only where it means the same as RE2.
func grepSearch(args []string, path pather) (string, any, bool) {
	args, mode, recursive, ok := grepFlags(args)
	if mode == "" && len(args) > 0 && !strings.ContainsAny(args[0], `\+?|(){}`) {
		mode = map[bool]string{true: "regex", false: "literal"}[strings.ContainsAny(args[0], ".*[]^$")]
	}
	if !ok || mode == "" || len(args) == 0 || len(args) > 2 || (len(args) == 1 && !recursive) {
		return "", nil, false
	}
	glob, ok := grepGlob(args[1:], path, recursive)
	return "file_search", map[string]any{"query": args[0], "mode": mode, "path_glob": glob, "max_results": 100, "context_lines": 0}, ok
}

// grepFlags consumes the leading -rRnFE flags; any other flag fails.
func grepFlags(args []string) (rest []string, mode string, recursive, ok bool) {
	for len(args) > 0 && strings.HasPrefix(args[0], "-") && len(args[0]) > 1 {
		for _, flag := range args[0][1:] {
			switch flag {
			case 'r', 'R':
				recursive = true
			case 'F':
				mode = "literal"
			case 'E':
				mode = "regex"
			case 'n':
			default:
				return nil, "", false, false
			}
		}
		args = args[1:]
	}
	return args, mode, recursive, true
}

// grepGlob is the workspace glob for grep's optional path argument; a
// recursive search with no path covers the cwd.
func grepGlob(rest []string, path pather, recursive bool) (string, bool) {
	target, ok := path(".")
	if len(rest) == 1 {
		target, ok = path(rest[0])
	}
	target = filepath.ToSlash(filepath.Clean(target))
	if !ok || filepath.IsAbs(target) || target == ".." || strings.HasPrefix(target, "../") {
		return "", false
	}
	if !recursive {
		return target, true
	}
	return strings.TrimPrefix(target+"/**", "./**"), true // "." is the whole workspace
}

type gitReadArgs struct {
	Op     string   `json:"op"`
	Ref    string   `json:"ref,omitempty"`
	Paths  []string `json:"paths,omitempty"`
	Staged bool     `json:"staged,omitempty"`
	Limit  int      `json:"limit,omitempty"`
}

// gitRead maps `git status|branch`, `git diff [--cached] [REF] [-- PATHS]`,
// `git show [REF] [-- PATHS]` and `git log --oneline [-n N] [REF] [-- PATHS]`.
func gitRead(args []string, path pather) (string, any, bool) {
	if len(args) == 0 {
		return "", nil, false
	}
	req := gitReadArgs{Op: args[0]}
	var flags []string
	for i := 1; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--":
			paths, ok := allPaths(args[i+1:], path)
			req.Paths, i = paths, len(args)
			if !ok {
				return "", nil, false
			}
		case arg == "-n" && i+1 < len(args):
			flags, i = append(flags, "-"+args[i+1]), i+1
		case strings.HasPrefix(arg, "-"):
			flags = append(flags, arg)
		case req.Ref == "":
			req.Ref = arg
		default:
			return "", nil, false
		}
	}
	return "git_read", req, gitFlags(&req, flags)
}

func gitFlags(req *gitReadArgs, flags []string) bool {
	oneline := false
	for _, flag := range flags {
		limit, err := strconv.Atoi(strings.TrimPrefix(flag, "-"))
		switch {
		case req.Op == "diff" && (flag == "--cached" || flag == "--staged"):
			req.Staged = true
		case req.Op == "log" && flag == "--oneline":
			oneline = true
		case req.Op == "log" && err == nil && limit > 0:
			req.Limit = limit
		default:
			return false
		}
	}
	// git_read's fixed output matches the shell form only for these: status
	// and branch take no arguments, log only in --oneline form.
	bare := req.Ref == "" && req.Paths == nil
	return map[string]bool{"status": bare, "branch": bare, "log": oneline, "diff": true, "show": true}[req.Op]
}

func rmFile(args []string, path pather) (string, any, bool) {
	if len(args) != 1 {
		return "", nil, false
	}
	p, ok := path(args[0])
	return "edit", map[string]any{"path": p, "delete": true}, ok
}
