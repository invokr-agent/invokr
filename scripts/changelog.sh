#!/usr/bin/env bash
# Adds a CHANGELOG.md section for TAG, built from the commit subjects since the
# previous tag and grouped by conventional-commit type. The newest section goes
# on top. docs/test/chore/ci commits are skipped, as in .goreleaser.yaml.
# Usage: scripts/changelog.sh v0.5.3   (defaults to the newest tag)
set -euo pipefail

tag=${1:-$(git describe --tags --abbrev=0)}
prev=$(git describe --tags --abbrev=0 "$tag^" 2>/dev/null || true)
range=${prev:+$prev..}$tag
file=CHANGELOG.md

if grep -q "^## $tag " "$file" 2>/dev/null; then
  echo "$file already has $tag" >&2
  exit 0
fi

subjects=$(git log --no-merges --format=%s "$range")
group() { # heading, type regex
  local lines
  lines=$(grep -E "^($2)(\([^)]*\))?!?: " <<<"$subjects" | sed -E 's/^[a-z]+(\(([^)]*)\))?!?: /- **\2** /; s/\*\*\*\* //') || true
  [[ -n $lines ]] && printf '### %s\n\n%s\n\n' "$1" "$lines"
  return 0
}
other=$(grep -vE '^(feat|fix|perf|refactor|docs|test|chore|ci|build|style)(\([^)]*\))?!?: ' <<<"$subjects" | sed 's/^/- /') || true

section=$(
  printf '## %s (%s)\n\n' "$tag" "$(git log -1 --format=%cs "$tag")"
  group Features feat
  group Fixes fix
  group Performance perf
  group Refactors refactor
  [[ -n $other ]] && printf '### Other\n\n%s\n\n' "$other"
  true
)

body=$(tail -n +2 "$file" 2>/dev/null || true)
printf '# Changelog\n\n%s\n%s\n' "$section" "$body" | cat -s >"$file"
echo "added $tag to $file"
