#!/usr/bin/env bash

set -euo pipefail

APT_ROOT="${1:-apt}"
DEB_SOURCE_DIR="${2:-dist}"
PKG_NAME="${PKG_NAME:-dbterm}"
DIST_CODENAME="${DIST_CODENAME:-stable}"

if ! command -v dpkg-scanpackages >/dev/null 2>&1; then
  echo "dpkg-scanpackages not found. Install dpkg-dev to generate APT metadata." >&2
  exit 1
fi

POOL_REL="pool/main/d/$PKG_NAME"
POOL_DIR="$APT_ROOT/$POOL_REL"
DISTS_DIR="$APT_ROOT/dists/$DIST_CODENAME"
mkdir -p "$POOL_DIR" "$DISTS_DIR/main/binary-amd64" "$DISTS_DIR/main/binary-arm64"

cp "$DEB_SOURCE_DIR"/${PKG_NAME}_*.deb "$POOL_DIR"/

for ARCH in amd64 arm64; do
  BIN_REL="dists/$DIST_CODENAME/main/binary-$ARCH"
  BIN_DIR="$APT_ROOT/$BIN_REL"
  pushd "$APT_ROOT" >/dev/null
  dpkg-scanpackages --arch "$ARCH" "$POOL_REL" > "$BIN_REL/Packages"
  popd >/dev/null
  gzip -9fk "$BIN_DIR/Packages"
done

pushd "$DISTS_DIR" >/dev/null

FILES=$(find main -type f \( -name Packages -o -name Packages.gz \) | sort)

{
  echo "Origin: $PKG_NAME"
  echo "Label: $PKG_NAME"
  echo "Suite: $DIST_CODENAME"
  echo "Codename: $DIST_CODENAME"
  echo "Architectures: amd64 arm64"
  echo "Components: main"
  echo "Description: APT repository for $PKG_NAME"
  echo "Date: $(date -Ru)"
  echo "MD5Sum:"
  while IFS= read -r FILE; do
    HASH=$(md5sum "$FILE" | awk '{print $1}')
    SIZE=$(stat -c%s "$FILE")
    printf " %s %16d %s\n" "$HASH" "$SIZE" "$FILE"
  done <<< "$FILES"
  echo "SHA256:"
  while IFS= read -r FILE; do
    HASH=$(sha256sum "$FILE" | awk '{print $1}')
    SIZE=$(stat -c%s "$FILE")
    printf " %s %16d %s\n" "$HASH" "$SIZE" "$FILE"
  done <<< "$FILES"
} > Release

popd >/dev/null

echo "APT repo metadata generated under $APT_ROOT"
