#!/usr/bin/env bash
# Pemasang panel dbpanel.
#
#   curl -fsSL https://raw.githubusercontent.com/husmin-fajrin/db-panel/main/scripts/install.sh | sudo bash
#
# Variabel lingkungan (opsional):
#   DBPANEL_BIN      pasang dari berkas binary lokal ini, tanpa mengunduh
#   DBPANEL_VERSION  tag rilis yang diunduh (default: rilis terbaru)
#   DBPANEL_BIN_DIR  direktori berisi dbpanel_linux_* untuk dibagikan panel
#                    ke server agent (disalin ke /var/lib/dbpanel/bin)
#
# Aman dijalankan ulang: binary diganti dan service dimulai ulang.
set -euo pipefail

REPO=husmin-fajrin/db-panel
BIN=/usr/local/bin/dbpanel
CONFIG_DIR=/etc/dbpanel
DATA_DIR=/var/lib/dbpanel
PASSWORD_FILE=$CONFIG_DIR/admin-initial-password
PASSWORD_WAIT=15 # seconds

die() {
	echo "$*" >&2
	exit 1
}

# install_binary ARCH puts the dbpanel binary at $BIN atomically, from
# DBPANEL_BIN or from GitHub Releases.
install_binary() {
	local arch=$1 url
	trap 'rm -f "$BIN.new"' EXIT
	if [ -n "${DBPANEL_BIN:-}" ]; then
		[ -f "$DBPANEL_BIN" ] || die "DBPANEL_BIN tidak ditemukan: $DBPANEL_BIN"
		echo "Memasang dbpanel dari $DBPANEL_BIN..."
		cp "$DBPANEL_BIN" "$BIN.new"
	else
		if [ -n "${DBPANEL_VERSION:-}" ]; then
			url="https://github.com/$REPO/releases/download/$DBPANEL_VERSION/dbpanel_linux_$arch"
		else
			url="https://github.com/$REPO/releases/latest/download/dbpanel_linux_$arch"
		fi
		echo "Mengunduh $url..."
		curl -fsSL "$url" -o "$BIN.new" || die "Gagal mengunduh $url"
	fi
	chmod 0755 "$BIN.new"
	mv -f "$BIN.new" "$BIN"
}

# copy_agent_binaries copies DBPANEL_BIN_DIR/dbpanel_linux_* to the directory
# the panel serves agent binaries from.
copy_agent_binaries() {
	local dir=$DATA_DIR/bin files
	[ -d "$DBPANEL_BIN_DIR" ] || die "DBPANEL_BIN_DIR bukan direktori: $DBPANEL_BIN_DIR"
	shopt -s nullglob
	files=("$DBPANEL_BIN_DIR"/dbpanel_linux_*)
	shopt -u nullglob
	[ "${#files[@]}" -gt 0 ] || die "Tidak ada berkas dbpanel_linux_* di $DBPANEL_BIN_DIR"
	mkdir -p "$dir"
	chmod 0750 "$dir"
	install -m 0644 "${files[@]}" "$dir/"
	echo "Binary agent disalin ke $dir: ${files[*]##*/}"
}

main() {
	if [ "$(id -u)" -ne 0 ]; then
		die "Jalankan dengan sudo"
	fi

	local machine arch
	machine=$(uname -m)
	case "$machine" in
	x86_64) arch=amd64 ;;
	aarch64) arch=arm64 ;;
	*) die "arsitektur tidak didukung: $machine" ;;
	esac

	# A panel that has run before already created its admin account.
	local first_install=1
	if [ -e "$DATA_DIR/panel.db" ]; then
		first_install=0
	fi

	install_binary "$arch"
	mkdir -p "$CONFIG_DIR" "$DATA_DIR"
	chmod 0700 "$CONFIG_DIR"
	chmod 0750 "$DATA_DIR"
	if [ -n "${DBPANEL_BIN_DIR:-}" ]; then
		copy_agent_binaries
	fi

	cat >/etc/systemd/system/dbpanel-panel.service <<'UNIT'
[Unit]
Description=dbpanel panel
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/dbpanel panel
Restart=always
RestartSec=3
User=root

[Install]
WantedBy=multi-user.target
UNIT

	# A running panel keeps the old binary until restarted.
	local was_active=0
	if systemctl is-active --quiet dbpanel-panel; then
		was_active=1
	fi
	systemctl daemon-reload && systemctl enable --now dbpanel-panel
	if [ "$was_active" -eq 1 ]; then
		systemctl restart dbpanel-panel
	fi

	if [ "$first_install" -eq 1 ]; then
		local i
		for ((i = 0; i < PASSWORD_WAIT * 2; i++)); do
			[ -s "$PASSWORD_FILE" ] && break
			sleep 0.5
		done
	fi
	if ! systemctl is-active --quiet dbpanel-panel; then
		die "Panel gagal berjalan. Periksa log: journalctl -u dbpanel-panel"
	fi

	local ip
	ip=$(hostname -I 2>/dev/null | awk '{print $1}') || true
	echo
	echo "Panel terpasang."
	echo "Alamat : http://${ip:-<ip-server>}:8080"
	echo "Pengguna: admin"
	if [ -s "$PASSWORD_FILE" ]; then
		echo "Sandi   : $(cat "$PASSWORD_FILE")"
	else
		echo "Sandi   : (sudah diatur sebelumnya; gunakan: dbpanel panel reset-password)"
	fi
}

# Everything runs from main so a download cut short never runs half a script.
main "$@"
