package agentui

import (
	"path/filepath"
	"slices"

	"github.com/invokr-ai/invokr/support/fsutil"
)

// prefsFile is the TUI's own view preferences, kept beside the prompt history
// so both follow the configured memory dir. It holds only what a keystroke
// toggles and the user expects to still be true tomorrow — config/ remains the
// source of truth for anything the user writes by hand.
const prefsFile = "ui-prefs.json"

type prefs struct {
	HideHints bool `json:"hide_hints"`
	// SyntaxTheme is the stylesheet picked in the settings View tab. It shadows the
	// config's ui.syntax_theme, on the same principle as HideHints: what the
	// user changed with a keystroke should still be true tomorrow.
	SyntaxTheme string `json:"syntax_theme,omitempty"`
	// MouseMode is the tracking preset picked in the settings View tab. Empty means
	// the default ("on"), which is what every prefs file written before it holds.
	MouseMode string `json:"mouse_mode,omitempty"`
	// NotifyMode is the attention-signal mode picked in the settings View tab. Empty
	// means "no explicit choice": the config's ui.notify (or off) applies.
	NotifyMode string `json:"notify_mode,omitempty"`
}

// parseMode reads one of the view modes this file stores back from its name:
// names are listed in value order, and the empty string is the zero value —
// which is what a prefs file written before the setting existed holds. The
// second result reports whether the name was one of them at all.
func parseMode[T ~int](names []string, name string) (T, bool) {
	index := slices.Index(names, name)
	if name == "" {
		index = 0
	}
	return T(max(index, 0)), index >= 0
}

func prefsPath(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, prefsFile)
}

// loadPrefs is best-effort: a missing or corrupt file means "no preferences
// yet", which is the same as the defaults. There is nothing here worth failing
// a startup over.
func loadPrefs(path string) prefs {
	if path == "" {
		return prefs{}
	}
	p, _, err := fsutil.ReadJSON[prefs](path)
	if err != nil {
		return prefs{}
	}
	return p
}

// savePrefs writes the preferences inline on the UI goroutine — one atomic
// write of a few dozen bytes, on a keystroke. Errors are swallowed for the same
// reason loadPrefs swallows them: a view toggle that reported a disk error
// would be more disruptive than the lost preference.
func savePrefs(path string, p prefs) {
	if path == "" {
		return
	}
	_ = fsutil.SaveJSON(path, p)
}
