package fsutil

import "github.com/charmbracelet/x/term"

func IsTerminal(v any) bool {
	f, ok := v.(interface{ Fd() uintptr })
	return ok && term.IsTerminal(f.Fd())
}
