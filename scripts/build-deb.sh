#!/usr/bin/env bash

set -euo pipefail

BIN_NAME="${BIN_NAME:-dbterm}"
VERSION="${1:-}"
DIST_DIR="${2:-dist}"

if [[ -z "$VERSION" ]]; then
  echo "usage: $0 <version> [dist_dir]" >&2
  exit 1
fi

if ! command -v dpkg-deb >/dev/null 2>&1; then
  echo "dpkg-deb not found. Install dpkg to build Debian packages." >&2
  exit 1
fi

mkdir -p "$DIST_DIR"
PKG_TMP_DIR="$DIST_DIR/.pkg"
rm -rf "$PKG_TMP_DIR"
mkdir -p "$PKG_TMP_DIR"

for ARCH in amd64 arm64; do
  BIN_PATH="$DIST_DIR/${BIN_NAME}-linux-${ARCH}"
  if [[ ! -f "$BIN_PATH" ]]; then
    echo "missing binary: $BIN_PATH" >&2
    exit 1
  fi

  ROOT_DIR="$PKG_TMP_DIR/${BIN_NAME}_${VERSION}_${ARCH}"
  mkdir -p "$ROOT_DIR/DEBIAN" "$ROOT_DIR/usr/bin" "$ROOT_DIR/usr/share/doc/$BIN_NAME"

  install -m 0755 "$BIN_PATH" "$ROOT_DIR/usr/bin/$BIN_NAME"
  cat > "$ROOT_DIR/usr/share/doc/$BIN_NAME/copyright" <<'EOF'
Copyright (c) dbterm contributors
License: MIT
EOF

  cat > "$ROOT_DIR/DEBIAN/control" <<EOF
Package: $BIN_NAME
Version: $VERSION
Section: database
Priority: optional
Architecture: $ARCH
Maintainer: dbterm maintainers <shreyam1008@users.noreply.github.com>
Description: Multi-database terminal client (PostgreSQL, MySQL, SQLite)
 Keyboard-driven TUI for running queries and browsing tables.
EOF

  OUT_DEB="$DIST_DIR/${BIN_NAME}_${VERSION}_${ARCH}.deb"
  dpkg-deb --build --root-owner-group "$ROOT_DIR" "$OUT_DEB" >/dev/null
  echo "built $OUT_DEB"
done

rm -rf "$PKG_TMP_DIR"
