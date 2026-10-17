#!/usr/bin/env bash
#
# Render the mermaid diagrams in docs/onboarding/16-diagrams.md as a single
# self-contained HTML page (mermaid loaded from CDN, no build step) and open
# it in a browser. Falls back to printing the file path if no opener/browser
# is available.
#
# Usage:
#   scripts/diagram-view.sh            # generate + try to open
#   scripts/diagram-view.sh --serve    # also serve over http on :8000
#
# Env:
#   OUT  output HTML path (default: $TMPDIR/invokr-diagram-view.html)
set -euo pipefail

REPO=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
SRC="$REPO/docs/onboarding/16-diagrams.md"
OUT="${OUT:-${TMPDIR:-/tmp}/invokr-diagram-view.html}"

[[ -f "$SRC" ]] || { echo "missing $SRC" >&2; exit 1; }

{
  cat <<'HEAD'
<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>Invokr architecture diagrams</title>
<style>
  body { font-family: system-ui, sans-serif; max-width: 1100px; margin: 2rem auto; padding: 0 1rem; }
  h2 { border-top: 1px solid #ccc; padding-top: 1.5rem; }
  pre.mermaid { background: #fafafa; padding: 1rem; overflow-x: auto; }
</style>
</head>
<body>
<h1>Invokr architecture diagrams</h1>
<p>Generated from <code>docs/onboarding/16-diagrams.md</code> by <code>scripts/diagram-view.sh</code>.</p>
HEAD

  awk '
    /^## / { print "<h2>" substr($0, 4) "</h2>"; next }
    /^```mermaid$/ { print "<pre class=\"mermaid\">"; inblock=1; next }
    /^```$/ && inblock { print "</pre>"; inblock=0; next }
    inblock { print }
  ' "$SRC"

  cat <<'TAIL'
<script type="module">
  import mermaid from 'https://cdn.jsdelivr.net/npm/mermaid@11/dist/mermaid.esm.min.mjs';
  mermaid.initialize({ startOnLoad: true });
</script>
</body>
</html>
TAIL
} > "$OUT"

echo "wrote $OUT"

if [[ "${1:-}" == "--serve" ]]; then
  cd "$(dirname "$OUT")"
  echo "serving at http://localhost:8000/$(basename "$OUT")"
  python3 -m http.server 8000
else
  (xdg-open "$OUT" 2>/dev/null || open "$OUT" 2>/dev/null) &
  disown 2>/dev/null || true
  echo "opening in browser (or open $OUT manually)"
fi
