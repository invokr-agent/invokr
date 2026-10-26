#!/usr/bin/env bash
# Re-record the README demo GIF end to end: venv, binary, recording, optimize.
#
#   make record                       # default prompt, into docs/assets/
#   PROMPT="…" make record            # a different turn
#   OUT=/tmp/try.gif make record      # somewhere else, to preview first
#
# Drives a real turn against the configured model, so it needs one reachable
# and takes about a minute. See README.md in this directory for the knobs.
set -euo pipefail
DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$DIR/../../.." && pwd)"

OUT=${OUT:-$ROOT/docs/assets/demo.gif}
STILL=${STILL:-${OUT%.gif}.png}
VENV=${VENV:-/tmp/invokr-record-venv}
BIN=${BIN:-/tmp/invokr-record-bin}

if [ ! -x "$VENV/bin/python" ]; then
  echo "==> creating $VENV"
  uv venv "$VENV" >/dev/null
  uv pip install --quiet --python "$VENV/bin/python" pyte pillow
fi

echo "==> building the agent"
(cd "$ROOT" && go build -o "$BIN" .)

echo "==> recording (about a minute; a real turn against the configured model)"
"$VENV/bin/python" "$DIR/record.py" "$OUT" "$STILL" -- "$BIN" ${INVOKR_FLAGS:-} agent

if command -v magick >/dev/null; then
  echo "==> optimizing"
  magick "$OUT" -layers optimize +dither -colors "${COLORS:-256}" "$OUT"
fi
ls -lh "$OUT" "$STILL"
