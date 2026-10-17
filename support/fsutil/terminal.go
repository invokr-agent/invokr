package fsutil

import "github.com/charmbracelet/x/term"

// IsTerminal reports whether v (a reader or writer) is an interactive terminal.
func IsTerminal(v any) bool {
	f, ok := v.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(f.Fd())
}
