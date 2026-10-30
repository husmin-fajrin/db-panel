#!/usr/bin/env bash
# Pemasang panel dbpanel.
#
#   curl -fsSL https://raw.githubusercontent.com/husmin-fajrin/db-panel/main/scripts/install.sh | sudo bash
#
# Variabel lingkungan (opsional):
#   DBPANEL_BIN      pasang dari berkas binary lokal ini, tanpa mengunduh
#   DBPANEL_VERSION  tag rilis yang diunduh (default: rilis terbaru)
#   DBPANEL_BIN_DIR  direktori berisi dbpanel_linux_* untuk dibagikan panel
#                    ke server agent (disalin ke /var/lib/dbpanel/bin).
#                    Tanpa DBPANEL_BIN dan DBPANEL_BIN_DIR, binary amd64 dan
#                    arm64 diunduh dari rilis yang sama dan diperiksa dengan
#                    SHA256SUMS.
#
# Binary panel yang diunduh diperiksa dengan SHA256SUMS dari rilis yang sama
# sebelum dipasang. Ini menangkap unduhan rusak atau tertukar, bukan rilis
# palsu: rilis belum ditandatangani (lihat README, "Keamanan rilis").
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

# release_url FILE prints the download URL of FILE in the chosen release.
release_url() {
	if [ -n "${DBPANEL_VERSION:-}" ]; then
		echo "https://github.com/$REPO/releases/download/$DBPANEL_VERSION/$1"
	else
		echo "https://github.com/$REPO/releases/latest/download/$1"
	fi
}

# listed DIR FILE reports whether DIR/SHA256SUMS has a line for FILE.
# sha256sum -c --ignore-missing passes a file that SHA256SUMS does not
# mention, so every check asks this first.
listed() {
	grep -qE "^[0-9a-f]{64} [ *]$2\$" "$1/SHA256SUMS"
}

# verify_sha256 DIR FILE checks DIR/FILE against its line in DIR/SHA256SUMS.
verify_sha256() {
	local dir=$1 file=$2
	listed "$dir" "$file" || return 1
	(cd "$dir" && sha256sum -c --ignore-missing --quiet SHA256SUMS)
}

# install_binary ARCH puts the dbpanel binary at $BIN atomically, from
# DBPANEL_BIN or from GitHub Releases. A downloaded binary is checked
# against the release's SHA256SUMS (fetched first, into $AGENT_TMP, where
# fetch_agent_binaries reuses both files).
install_binary() {
	local arch=$1 name=dbpanel_linux_$1 url
	if [ -n "${DBPANEL_BIN:-}" ]; then
		[ -f "$DBPANEL_BIN" ] || die "DBPANEL_BIN tidak ditemukan: $DBPANEL_BIN"
		echo "Memasang dbpanel dari $DBPANEL_BIN..."
		cp "$DBPANEL_BIN" "$BIN.new"
	else
		url=$(release_url SHA256SUMS)
		echo "Mengunduh $url..."
		curl -fsSL "$url" -o "$AGENT_TMP/SHA256SUMS" || die "Gagal mengunduh $url"
		url=$(release_url "$name")
		echo "Mengunduh $url..."
		curl -fsSL "$url" -o "$AGENT_TMP/$name" || die "Gagal mengunduh $url"
		verify_sha256 "$AGENT_TMP" "$name" || die "Checksum binary panel tidak cocok"
		cp "$AGENT_TMP/$name" "$BIN.new"
	fi
	chmod 0755 "$BIN.new"
	mv -f "$BIN.new" "$BIN"
}

# copy_agent_binaries SRC copies SRC/dbpanel_linux_{amd64,arm64} to the
# directory the panel serves agent binaries from, each one atomically.
copy_agent_binaries() {
	local src=$1 dir=$DATA_DIR/bin f name copied=()
	[ -d "$src" ] || die "DBPANEL_BIN_DIR bukan direktori: $src"
	mkdir -p "$dir"
	chmod 0750 "$dir"
	for f in "$src"/dbpanel_linux_*; do
		name=${f##*/}
		if [[ ! -f $f || ! $name =~ ^dbpanel_linux_(amd64|arm64)$ ]]; then
			continue
		fi
		install -m 0644 "$f" "$dir/$name.new"
		mv -f "$dir/$name.new" "$dir/$name"
		copied+=("$name")
	done
	[ "${#copied[@]}" -gt 0 ] || die "Tidak ada dbpanel_linux_amd64 atau dbpanel_linux_arm64 di $src"
	echo "Binary agent disalin ke $dir: ${copied[*]}"
}

# fetch_agent_binaries DIR downloads dbpanel_linux_{amd64,arm64} and
# SHA256SUMS of the release into DIR (skipping files install_binary already
# put there) and checks them: a binary SHA256SUMS does not list is removed
# with a warning (never served unchecked), and a mismatch fails. It returns
# non-zero on any failure (it is called from an if, where set -e does not
# apply).
fetch_agent_binaries() {
	local dir=$1 f name
	for f in dbpanel_linux_amd64 dbpanel_linux_arm64 SHA256SUMS; do
		[ -f "$dir/$f" ] && continue
		curl -fsSL "$(release_url "$f")" -o "$dir/$f" || return 1
	done
	for f in "$dir"/dbpanel_linux_*; do
		name=${f##*/}
		if [ -f "$f" ] && ! listed "$dir" "$name"; then
			echo "Peringatan: $name tidak tercantum di SHA256SUMS; dilewati" >&2
			rm -f "$f"
		fi
	done
	(cd "$dir" && sha256sum -c --ignore-missing --quiet SHA256SUMS) || return 1
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

	# Global: the EXIT trap runs after main's locals are gone.
	AGENT_TMP=$(mktemp -d)
	trap 'rm -f "$BIN.new"; rm -rf "$AGENT_TMP"' EXIT

	install_binary "$arch"
	mkdir -p "$CONFIG_DIR" "$DATA_DIR"
	# Owner only: panel.db holds password and token hashes, the config
	# directory the CA key and the initial password.
	chmod 0700 "$CONFIG_DIR" "$DATA_DIR"
	if [ -n "${DBPANEL_BIN_DIR:-}" ]; then
		copy_agent_binaries "$DBPANEL_BIN_DIR"
	elif [ -z "${DBPANEL_BIN:-}" ]; then
		# Online install: also fetch both architectures so the panel can serve
		# agents of either kind (its own arch is the binary just installed).
		echo "Mengunduh binary agent amd64 dan arm64..."
		if fetch_agent_binaries "$AGENT_TMP"; then
			copy_agent_binaries "$AGENT_TMP"
		else
			echo "Peringatan: binary arsitektur lain tidak terunduh; agent arsitektur lain perlu DBPANEL_BIN_DIR" >&2
		fi
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
# Root: the panel runs this host's embedded agent. Hardening that still
# allows it; the panel writes only its data and config directories.
User=root
NoNewPrivileges=yes
PrivateTmp=yes
ProtectHome=yes
ProtectSystem=strict
ReadWritePaths=/var/lib/dbpanel /etc/dbpanel
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictSUIDSGID=yes
LockPersonality=yes
RestrictRealtime=yes
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
UNIT

	# A running panel keeps the old binary until restarted.
	local was_active=0
	if systemctl is-active --quiet dbpanel-panel; then
		was_active=1
	fi
	# Separate statements: set -e would ignore a failure early in an && list.
	systemctl daemon-reload || die "Gagal memuat ulang systemd (systemctl daemon-reload)."
	systemctl enable --now dbpanel-panel || die "Gagal menyalakan service dbpanel-panel. Periksa: journalctl -u dbpanel-panel"
	if [ "$was_active" -eq 1 ]; then
		systemctl restart dbpanel-panel || die "Gagal memulai ulang service dbpanel-panel. Periksa: journalctl -u dbpanel-panel"
	fi

	if [ "$first_install" -eq 1 ]; then
		local i
		for ((i = 0; i < PASSWORD_WAIT * 2; i++)); do
			[ -s "$PASSWORD_FILE" ] && break
			sleep 0.5
		done
	fi
	# Give a crashing panel time to exit (or sit in auto-restart) first; must
	# exceed RestartSec=3.
	sleep 5
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
		echo
		echo "Sandi awal juga tersimpan di $PASSWORD_FILE."
		echo "Peringatan: hapus file ini setelah login pertama: sudo rm $PASSWORD_FILE"
	else
		echo "Sandi   : (sudah diatur sebelumnya; gunakan: dbpanel panel reset-password)"
	fi
}

# Everything runs from main so a download cut short never runs half a script.
main "$@"
