#!/usr/bin/env bash
# Unduh Alpine.js ke web/static/vendor/alpine.min.js. Jalankan dari root repo.
#
# CDN npm (jsdelivr/unpkg) tidak selalu bisa dijangkau, jadi berkas diambil
# dari tarball npm resmi: package/dist/cdn.min.js sama dengan
# https://cdn.jsdelivr.net/npm/alpinejs@<versi>/dist/cdn.min.js.
set -euo pipefail

ALPINE_VERSION="${ALPINE_VERSION:-3.14.9}"
TOOLS=.tools
TARBALL="$TOOLS/alpinejs-${ALPINE_VERSION}.tgz"
TARGET=web/static/vendor/alpine.min.js

mkdir -p "$TOOLS" "$(dirname "$TARGET")"
if [ ! -f "$TARBALL" ]; then
	curl -fsSL -o "$TARBALL.tmp" "https://registry.npmjs.org/alpinejs/-/alpinejs-${ALPINE_VERSION}.tgz"
	mv "$TARBALL.tmp" "$TARBALL"
fi
tar -xzOf "$TARBALL" package/dist/cdn.min.js > "$TARGET.tmp"
mv "$TARGET.tmp" "$TARGET"
echo "Alpine.js ${ALPINE_VERSION} -> $TARGET"
