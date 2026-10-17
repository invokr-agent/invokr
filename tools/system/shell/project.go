package shell

import (
	"fmt"

	"github.com/invokr-ai/invokr/tools/core"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// projectMarkers maps a workspace marker file to the ecosystem name and the
// shell commands a checkout of that ecosystem needs. Detection only ever
// *advises*: workspace content must not change what the agent may execute
// (ADR 0011), so results are surfaced as text for a human to act on.
type projectMarker struct {
	marker, name string
	commands     []string
}

var projectMarkers = []projectMarker{
	{"go.mod", "go", []string{"go", "gofmt"}}, {"package.json", "node", []string{"npm"}},
	{"Cargo.toml", "rust", []string{"cargo"}}, {"Makefile", "make", []string{"make"}},
	{"pyproject.toml", "python", []string{"python3", "pytest"}}, {"setup.py", "python", []string{"python3", "pytest"}},
}

// MissingProjectCommands returns the commands the detected project type needs
// that are absent from the allowlist, in marker order, deduplicated.
func MissingProjectCommands(root string, allowed []string) []string {
	var missing []string
	for _, m := range detectedMarkers(root) {
		missing = append(missing, m.missing(allowed)...)
	}
	return core.DedupPreserveOrder(missing)
}

// projectGapNote renders the prompt line naming detected ecosystems whose
// commands are not allowlisted, or "" when there is nothing to report.
func projectGapNote(root string, allowed []string) string {
	var notes []string
	seen := map[string]struct{}{}
	for _, m := range detectedMarkers(root) {
		if _, dup := seen[m.name]; dup {
			continue
		}
		seen[m.name] = struct{}{}
		if missing := m.missing(allowed); len(missing) > 0 {
			notes = append(notes, fmt.Sprintf("Detected project: %s (%s). Not allowlisted: %s — ask the user to run `invokr doctor` or add them to shell.allowed_commands.", m.name, m.marker, strings.Join(missing, ", ")))
		}
	}
	return strings.Join(notes, " ")
}

// detectedMarkers returns the markers present in root, in table order.
func detectedMarkers(root string) []projectMarker {
	var found []projectMarker
	for _, m := range projectMarkers {
		if _, err := os.Stat(filepath.Join(root, m.marker)); err == nil {
			found = append(found, m)
		}
	}
	return found
}

// missing returns the marker's commands absent from allowed.
func (m projectMarker) missing(allowed []string) []string {
	var missing []string
	for _, command := range m.commands {
		if !slices.Contains(allowed, command) {
			missing = append(missing, command)
		}
	}
	return missing
}
