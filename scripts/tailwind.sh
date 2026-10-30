#!/usr/bin/env bash
# Kompilasi web/src/app.css -> web/static/app.css dengan Tailwind CSS
# standalone CLI dan plugin daisyUI (tanpa Node.js). Jalankan dari root repo.
# Unduhan disimpan di .tools/ (tidak dikomit) dan dilewati bila sudah ada.
set -euo pipefail

TAILWIND_VERSION="${TAILWIND_VERSION:-v4.1.14}"
DAISYUI_VERSION="${DAISYUI_VERSION:-v5.1.29}"
TOOLS=.tools

case "$(uname -m)" in
	x86_64 | amd64) TW_ARCH=x64 ;;
	aarch64 | arm64) TW_ARCH=arm64 ;;
	*) echo "arsitektur tidak didukung: $(uname -m)" >&2; exit 1 ;;
esac

download() { # url target
	if [ ! -f "$2" ]; then
		curl -fsSL -o "$2.tmp" "$1"
		mv "$2.tmp" "$2"
	fi
}

mkdir -p "$TOOLS"
TW="$TOOLS/tailwindcss-${TAILWIND_VERSION}-linux-${TW_ARCH}"
download "https://github.com/tailwindlabs/tailwindcss/releases/download/${TAILWIND_VERSION}/tailwindcss-linux-${TW_ARCH}" "$TW"
chmod +x "$TW"

# Plugin daisyUI dikunci ke versinya lewat penanda; ganti versi = unduh ulang.
if [ "$(cat "$TOOLS/daisyui.version" 2>/dev/null)" != "$DAISYUI_VERSION" ]; then
	rm -f "$TOOLS/daisyui.js" "$TOOLS/daisyui-theme.js"
fi
for f in daisyui.js daisyui-theme.js; do
	download "https://github.com/saadeghi/daisyui/releases/download/${DAISYUI_VERSION}/$f" "$TOOLS/$f"
done
echo "$DAISYUI_VERSION" > "$TOOLS/daisyui.version"

"$TW" -i web/src/app.css -o web/static/app.css --minify
echo "Tailwind ${TAILWIND_VERSION} + daisyUI ${DAISYUI_VERSION} -> web/static/app.css"
