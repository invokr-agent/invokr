#!/usr/bin/env bash
# Builds invokr from source with the git commit and build date baked in, and
# installs it as `invokr-dev` so it sits next to a released `invokr-agent`.
set -euo pipefail

cd "$(dirname "$0")/.."

INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
NAME="${NAME:-invokr-dev}"
PKG=github.com/antonikliment/invokr/platform/version

# Go builds for the host by default; read back what that host is so the build
# is explicit about it and an unsupported platform fails here, not later.
read -r goos goarch <<EOF2
$(go env GOOS GOARCH | tr '\n' ' ')
EOF2
case "$goos" in
  linux | darwin) ;;
  *) echo "build-dev.sh supports linux and darwin, not $goos" >&2; exit 1 ;;
esac

version="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
commit="$(git rev-parse --short HEAD 2>/dev/null || echo none)"
date="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

go build -ldflags "-X $PKG.Version=$version -X $PKG.Commit=$commit -X $PKG.Date=$date" -o "$NAME" .
mkdir -p "$INSTALL_DIR"
cp "$NAME" "$INSTALL_DIR/$NAME"
chmod 0755 "$INSTALL_DIR/$NAME"
echo "installed $INSTALL_DIR/$NAME for $goos/$goarch ($version · $commit · $date)"
