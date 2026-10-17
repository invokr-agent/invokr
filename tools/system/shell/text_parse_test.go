package shell

import (
	"strconv"
	"strings"
)

// parseText inverts ShellResult.text for tests: trailing [fact] lines, then an
// optional [stderr] section, the rest is stdout. Command is not rendered.
func parseText(out string) ShellResult {
	var r ShellResult
	lines := strings.Split(out, "\n")
	for len(lines) > 0 {
		last := lines[len(lines)-1]
		switch {
		case strings.HasPrefix(last, "[exit code "):
			r.ExitCode, _ = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(last, "[exit code "), "]"))
		case last == "[timed out]":
			r.TimedOut = true
		case last == "[output truncated]":
			r.Truncated = true
		case strings.HasPrefix(last, "[cwd "):
			r.Cwd = strings.TrimSuffix(strings.TrimPrefix(last, "[cwd "), "]")
		case strings.HasPrefix(last, "[hint] "):
			r.Hint = strings.TrimPrefix(last, "[hint] ")
		default:
			goto body
		}
		lines = lines[:len(lines)-1]
	}
body:
	text := strings.Join(lines, "\n")
	if i := strings.LastIndex(text, "\n[stderr]\n"); i >= 0 {
		r.Stdout, r.Stderr = text[:i], text[i+len("\n[stderr]\n"):]
	} else {
		r.Stdout = text
	}
	if r.Cwd == "" {
		r.Cwd = "."
	}
	return r
}
