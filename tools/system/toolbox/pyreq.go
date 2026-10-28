package toolbox

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/invokr-agent/invokr/tools/core"
)

// pinnedRequirementRe matches "name==version" with an optional environment
// marker (";...") and optional inline comment. Extras ("name[extra]==1.0")
// are allowed; any other version specifier (>=, ~=, !=, <, >, or no
// specifier at all) is rejected as unpinned.
var pinnedRequirementRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*(\[[A-Za-z0-9_.,-]+\])?==[A-Za-z0-9.!+*_-]+$`)
var packageSeparators = regexp.MustCompile(`[-_.]+`)

func canonicalPackageName(name string) string {
	return packageSeparators.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
}

// parsePythonRequirementNames validates every requirement line and returns
// the lowercased package name of each (extras/markers/version stripped),
// for policy checks against allow/deny lists.
func parsePythonRequirementNames(data []byte) ([]string, error) {
	var names []string
	for i, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if err := validatePythonRequirementLine(line); err != nil {
			return nil, fmt.Errorf("requirements.txt line %d: %w", i+1, err)
		}
		names = append(names, pythonRequirementName(line))
	}
	return names, nil
}

func pythonRequirementName(line string) string {
	if idx := strings.Index(line, ";"); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	name := line[:strings.Index(line, "==")]
	if idx := strings.Index(name, "["); idx >= 0 {
		name = name[:idx]
	}
	return canonicalPackageName(name)
}

// enforcePackagePolicy applies INVOKR_TOOLBOX_PKG_ALLOW/_DENY: deny always
// blocks; a non-empty allow list makes installs default-deny, only
// permitting the named packages. Names use Python's case/punctuation normalization.
func enforcePackagePolicy(names []string, allow []string, deny []string) error {
	denyList := core.MapSlice(deny, canonicalPackageName)
	// Blank allow entries are kept: a configured-but-empty allowlist stays
	// default-deny rather than reading as unset.
	allowList := core.MapSlice(allow, canonicalPackageName)
	for _, name := range names {
		name = canonicalPackageName(name)
		if slices.Contains(denyList, name) {
			return fmt.Errorf("package %q is denied by policy", name)
		}
		if len(allowList) > 0 && !slices.Contains(allowList, name) {
			return fmt.Errorf("package %q is not in the allowlist", name)
		}
	}
	return nil
}

func validatePythonRequirementLine(line string) error {
	if strings.HasPrefix(line, "-") {
		return fmt.Errorf("pip option lines are not allowed (%q)", line)
	}
	if strings.Contains(line, "://") {
		return fmt.Errorf("direct URL/VCS requirements are not allowed (%q)", line)
	}
	if idx := strings.Index(line, ";"); idx >= 0 {
		line = strings.TrimSpace(line[:idx])
	}
	if strings.Contains(line, "@") {
		return fmt.Errorf("direct references (name @ url/path) are not allowed (%q)", line)
	}
	if !pinnedRequirementRe.MatchString(line) {
		return fmt.Errorf("requirement must be exactly pinned with '==' (%q)", line)
	}
	return nil
}
