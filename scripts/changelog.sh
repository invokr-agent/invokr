#!/usr/bin/env bash
# Prints release notes for TAG: the commit subjects since the previous tag,
# grouped by conventional-commit type. docs/test/chore/ci commits are skipped.
# Usage: scripts/changelog.sh v0.5.3 [since]   (defaults to the newest tag;
# since overrides the previous tag, e.g. the last stable one for an edge build)
set -euo pipefail

tag=${1:-$(git describe --tags --abbrev=0)}
prev=${2:-$(git describe --tags --abbrev=0 "$tag^" 2>/dev/null || true)}
range=${prev:+$prev..}$tag

subjects=$(git log --no-merges --format=%s "$range")
group() { # heading, type regex
  local lines
  lines=$(grep -E "^($2)(\([^)]*\))?!?: " <<<"$subjects" | sed -E 's/^[a-z]+(\(([^)]*)\))?!?: /- **\2** /; s/\*\*\*\* //') || true
  [[ -n $lines ]] && printf '### %s\n\n%s\n\n' "$1" "$lines"
  return 0
}
other=$(grep -vE '^(feat|fix|perf|refactor|docs|test|chore|ci|build|style)(\([^)]*\))?!?: ' <<<"$subjects" | sed 's/^/- /') || true

{
  group Features feat
  group Fixes fix
  group Performance perf
  group Refactors refactor
  [[ -n $other ]] && printf '### Other\n\n%s\n\n' "$other"
  true
} | cat -s
