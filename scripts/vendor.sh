#!/usr/bin/env bash
# Unduh Alpine.js (build CSP) ke web/static/vendor/alpine-csp.min.js.
# Jalankan dari root repo.
#
# Build CSP (@alpinejs/csp) tidak memakai eval/new Function, sehingga panel
# bisa memakai Content-Security-Policy tanpa 'unsafe-eval'. Konsekuensinya,
# atribut Alpine di template hanya boleh berisi nama properti atau method
# komponen yang didaftarkan di web/static/app.js (tanpa ekspresi JS).
#
# CDN npm (jsdelivr/unpkg) tidak selalu bisa dijangkau, jadi berkas diambil
# dari tarball npm resmi: package/dist/cdn.min.js sama dengan
# https://cdn.jsdelivr.net/npm/@alpinejs/csp@<versi>/dist/cdn.min.js.
# Integritas tarball dicocokkan dengan nilai "integrity" di registry npm.
set -euo pipefail

ALPINE_VERSION="${ALPINE_VERSION:-3.14.9}"
# sha512 tarball @alpinejs/csp 3.14.9 (dist.integrity di registry npm).
# Ganti bersama ALPINE_VERSION.
ALPINE_INTEGRITY="${ALPINE_INTEGRITY:-sha512-Z/aeDo4Wn4bA4+u77kw9h+nGGW+QzbR2ULTQ1pwOFWk3j2SZtTTI1ZM6FrYOqkFUcwo44e12pY+Y/Va6GTIlFQ==}"
TOOLS=.tools
TARBALL="$TOOLS/alpinejs-csp-${ALPINE_VERSION}.tgz"
TARGET=web/static/vendor/alpine-csp.min.js

mkdir -p "$TOOLS" "$(dirname "$TARGET")"
if [ ! -f "$TARBALL" ]; then
	curl -fsSL -o "$TARBALL.tmp" "https://registry.npmjs.org/@alpinejs/csp/-/csp-${ALPINE_VERSION}.tgz"
	mv "$TARBALL.tmp" "$TARBALL"
fi
got="sha512-$(openssl dgst -sha512 -binary "$TARBALL" | base64 -w0)"
if [ "$got" != "$ALPINE_INTEGRITY" ]; then
	rm -f "$TARBALL"
	echo "Integritas tarball Alpine tidak cocok: $got" >&2
	exit 1
fi
tar -xzOf "$TARBALL" package/dist/cdn.min.js >"$TARGET.tmp"
mv "$TARGET.tmp" "$TARGET"
echo "Alpine.js CSP ${ALPINE_VERSION} -> $TARGET"
