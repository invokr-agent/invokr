package shell

import (
	"encoding/json"
	"regexp"
	"slices"
	"strings"

	"mvdan.cc/sh/v3/syntax"

	"github.com/invokr-agent/invokr/tools/core"
)

var pythonExe = regexp.MustCompile(`^python(\d+(\.\d+)?)?$`)

// InlinePython reports a shell call that runs Python written into the call
// itself — `python3 -c …`, `python3 - <<EOF`, `… | python3` — rather than a
// script or module from the project, which needs the real shell.
func InlinePython(call *core.ToolCall) bool {
	var args shellArgs
	if call == nil || call.Name != "shell" || json.Unmarshal(call.Arguments, &args) != nil {
		return false
	}
	if len(args.Args) > 0 { // an argv call: the command names the program alone
		return pythonExe.MatchString(strings.TrimSpace(args.Command)) && inlineArgs(args.Args)
	}
	file, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(args.Command), "")
	if err != nil {
		return false
	}
	inline := false
	syntax.Walk(file, func(node syntax.Node) bool {
		if call, ok := node.(*syntax.CallExpr); ok && len(call.Args) > 0 && pythonExe.MatchString(call.Args[0].Lit()) {
			inline = inline || inlineArgs(core.MapSlice(call.Args[1:], func(w *syntax.Word) string { return w.Lit() }))
		}
		return true
	})
	return inline
}

func inlineArgs(words []string) bool {
	if slices.Contains(words, "-m") || slices.ContainsFunc(words, func(w string) bool { return w == "-V" || w == "--version" }) {
		return false
	}
	script := slices.IndexFunc(words, func(w string) bool { return !strings.HasPrefix(w, "-") })
	return slices.Contains(words, "-c") || slices.Contains(words, "-") || script < 0
}
