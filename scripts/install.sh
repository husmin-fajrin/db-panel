#!/usr/bin/env bash
# Pemasang panel dbpanel.
#
#   curl -fsSL https://raw.githubusercontent.com/husmin-fajrin/db-panel/main/scripts/install.sh | sudo bash
#
# Variabel lingkungan (opsional):
#   DBPANEL_BIN      pasang dari berkas binary lokal ini, tanpa mengunduh
#   DBPANEL_VERSION  tag rilis yang diunduh (default: rilis terbaru)
#   DBPANEL_BIN_DIR  direktori berisi dbpanel_linux_* untuk dibagikan panel
#                    ke server agent (disalin ke /var/lib/dbpanel/panel/bin).
#                    Tanpa DBPANEL_BIN dan DBPANEL_BIN_DIR, binary amd64 dan
#                    arm64 diunduh dari rilis yang sama dan diperiksa dengan
#                    SHA256SUMS.
#
# Binary panel yang diunduh diperiksa dengan SHA256SUMS dari rilis yang sama
# sebelum dipasang. Ini menangkap unduhan rusak atau tertukar, bukan rilis
# palsu: rilis belum ditandatangani (lihat README, "Keamanan rilis").
#
# Panel berjalan sebagai pengguna sistem dbpanel (berkasnya di
# /etc/dbpanel/panel dan /var/lib/dbpanel/panel); agent host ini adalah
# service dbpanel-agent terpisah (root, berkasnya di /etc/dbpanel) yang
# berpasangan sendiri dengan token yang ditulis panel.
#
# Pembaruan dari versi yang menjalankan panel sebagai root: panel dihentikan,
# berkasnya dipindah ke subfolder panel/ dan diberikan ke dbpanel, lalu panel
# dan agent lokal dinyalakan. Aman dijalankan ulang, juga setelah terputus:
# tiap langkah memeriksa keadaannya dulu.
set -euo pipefail

REPO=husmin-fajrin/db-panel
# ROOT prefixes every path (tests run the script against a temporary root).
ROOT=${ROOT:-}
BIN=$ROOT/usr/local/bin/dbpanel
CONFIG_DIR=$ROOT/etc/dbpanel        # the agent's; root, 0711
DATA_DIR=$ROOT/var/lib/dbpanel      # root, 0711
PANEL_CONFIG_DIR=$CONFIG_DIR/panel  # the panel's (user dbpanel, 0700)
PANEL_DATA_DIR=$DATA_DIR/panel      # the panel's (user dbpanel, 0700)
UNIT_DIR=$ROOT/etc/systemd/system
PANEL_USER=dbpanel # the units name it too; tests reassign it
PASSWORD_FILE=$PANEL_CONFIG_DIR/admin-initial-password
PASSWORD_WAIT=15 # seconds
# The panel writes the token for this host's agent here at its start; the
# agent pairs with it from its own directory.
PANEL_TOKEN_FILE=$PANEL_CONFIG_DIR/local-pair-token
AGENT_TOKEN_FILE=$CONFIG_DIR/local-pair-token
TOKEN_WAIT=60 # seconds
# Files of a panel that ran as root in /etc/dbpanel and /var/lib/dbpanel,
# moved to the panel's subfolders. ca.key and panel.db go last: while they
# are still in the old place, the move is not finished.
OLD_CONFIG_FILES=(web.custom.crt web.custom.key web.crt web.key web-ca.crt.old web-ca.key.old web-ca.crt web-ca.key
	mysql-ca.crt mysql-ca.key panel.key admin-initial-password ca.crt ca.key)
OLD_DATA_FILES=(bin panel.db-wal panel.db-shm panel.db)

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
	local src=$1 dir=$PANEL_DATA_DIR/bin f name copied=()
	[ -d "$src" ] || die "DBPANEL_BIN_DIR bukan direktori: $src"
	mkdir -p "$dir"
	chmod 0750 "$dir"
	chown "$PANEL_USER:" "$dir"
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

# ensure_user creates the panel's system user when it is missing.
ensure_user() {
	if id -u "$PANEL_USER" >/dev/null 2>&1; then
		return 0
	fi
	echo "Membuat pengguna sistem $PANEL_USER..."
	useradd --system --home /var/lib/dbpanel/panel --shell /usr/sbin/nologin "$PANEL_USER" ||
		die "Gagal membuat pengguna $PANEL_USER"
}

# prepare_dirs creates the directories: the shared parents root's and only
# traversable (0711), the panel's subfolders 0700.
prepare_dirs() {
	mkdir -p "$CONFIG_DIR" "$DATA_DIR" "$PANEL_CONFIG_DIR" "$PANEL_DATA_DIR"
	chown root:root "$CONFIG_DIR" "$DATA_DIR"
	chmod 0711 "$CONFIG_DIR" "$DATA_DIR"
	chmod 0700 "$PANEL_CONFIG_DIR" "$PANEL_DATA_DIR"
}

# move_old FROM TO NAME... moves each FROM/NAME to TO/NAME, unless it is
# gone already or TO/NAME exists (a later step, or the agent, owns it then).
move_old() {
	local from=$1 to=$2 name
	shift 2
	for name in "$@"; do
		[ -e "$from/$name" ] || continue
		if [ -e "$to/$name" ]; then
			# The agent of this host writes its own ca.crt in /etc/dbpanel.
			[ "$name" = ca.crt ] || echo "Peringatan: $from/$name dibiarkan; $to/$name sudah ada" >&2
			continue
		fi
		mv "$from/$name" "$to/$name"
		echo "Dipindah: $from/$name -> $to/$name"
	done
}

# migrate_layout moves the files of a panel that ran as root into its
# subfolders and gives the subfolders to the panel user.
migrate_layout() {
	move_old "$DATA_DIR" "$PANEL_DATA_DIR" "${OLD_DATA_FILES[@]}"
	move_old "$CONFIG_DIR" "$PANEL_CONFIG_DIR" "${OLD_CONFIG_FILES[@]}"
	chown -R "$PANEL_USER:" "$PANEL_CONFIG_DIR" "$PANEL_DATA_DIR"
}

# write_units writes the panel unit, the agent unit and, on this host, the
# drop-in that starts the agent after the panel. The units are the files in
# scripts/systemd (install_test.sh checks them).
write_units() {
	mkdir -p "$UNIT_DIR/dbpanel-agent.service.d"
	cat >"$UNIT_DIR"/dbpanel-panel.service <<'UNIT'
[Unit]
Description=dbpanel panel
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/dbpanel panel
Restart=always
RestartSec=3
# The panel needs no privilege: it writes only its own directories. This
# host's agent is the separate dbpanel-agent service.
User=dbpanel
UMask=0077
NoNewPrivileges=yes
PrivateTmp=yes
ProtectHome=yes
ProtectSystem=strict
ReadWritePaths=/var/lib/dbpanel/panel /etc/dbpanel/panel
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
	cat >"$UNIT_DIR"/dbpanel-agent.service <<'UNIT'
[Unit]
Description=dbpanel agent
After=network-online.target
Wants=network-online.target

[Service]
ExecStart=/usr/local/bin/dbpanel agent
Restart=always
RestartSec=3
# Root: the agent manages MySQL. No ProtectSystem: the agent writes
# /etc/dbpanel (sealed MySQL credentials), the MySQL option files under
# /etc and the data directory, and mysql.install installs packages into
# /usr. KillMode=process: a mysqld the agent started through an init
# script or mysqld_safe stays up when the agent restarts.
User=root
KillMode=process
NoNewPrivileges=yes
PrivateTmp=yes
ProtectHome=yes
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
	cat >"$UNIT_DIR"/dbpanel-agent.service.d/panel.conf <<'UNIT'
[Unit]
After=dbpanel-panel.service
UNIT
}

# start_service NAME enables NAME and starts it, or restarts it when it was
# running (a running service keeps the old binary until restarted).
start_service() {
	local name=$1
	systemctl daemon-reload || die "Gagal memuat ulang systemd (systemctl daemon-reload)."
	if systemctl is-active --quiet "$name"; then
		systemctl enable "$name" || die "Gagal mengaktifkan service $name."
		systemctl restart "$name" || die "Gagal memulai ulang service $name. Periksa: journalctl -u $name"
	else
		systemctl enable --now "$name" || die "Gagal menyalakan service $name. Periksa: journalctl -u $name"
	fi
}

# hand_local_token waits for the token the panel writes at its start for
# this host's own server and moves it to the agent's directory (root, 0600).
# An agent paired already needs none.
hand_local_token() {
	if [ -e "$CONFIG_DIR/agent.yaml" ]; then
		rm -f "$AGENT_TOKEN_FILE"
		return 0
	fi
	local i
	for ((i = 0; i < TOKEN_WAIT * 2; i++)); do
		[ -s "$PANEL_TOKEN_FILE" ] && break
		sleep 0.5
	done
	[ -s "$PANEL_TOKEN_FILE" ] ||
		die "Panel tidak menulis token untuk agent host ini dalam ${TOKEN_WAIT} detik. Periksa: journalctl -u dbpanel-panel"
	mv -f "$PANEL_TOKEN_FILE" "$AGENT_TOKEN_FILE.new"
	chown root:root "$AGENT_TOKEN_FILE.new"
	chmod 0600 "$AGENT_TOKEN_FILE.new"
	mv -f "$AGENT_TOKEN_FILE.new" "$AGENT_TOKEN_FILE"
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

	# A panel that has run before already created its admin account (in
	# the old layout, before the move, or in the new one).
	local first_install=1
	if [ -e "$DATA_DIR/panel.db" ] || [ -e "$PANEL_DATA_DIR/panel.db" ]; then
		first_install=0
	fi

	# Global: the EXIT trap runs after main's locals are gone.
	AGENT_TMP=$(mktemp -d)
	trap 'rm -f "$BIN.new"; rm -rf "$AGENT_TMP"' EXIT

	install_binary "$arch"

	# The files move while no panel holds them open.
	if systemctl is-active --quiet dbpanel-panel; then
		echo "Menghentikan panel..."
		systemctl stop dbpanel-panel || die "Gagal menghentikan service dbpanel-panel."
	fi
	ensure_user
	prepare_dirs
	migrate_layout
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
	write_units

	# A token left from an interrupted run is stale: the panel writes a new
	# one at its start, and only that one is waited for.
	rm -f "$PANEL_TOKEN_FILE"
	start_service dbpanel-panel

	if [ "$first_install" -eq 1 ]; then
		local i
		for ((i = 0; i < PASSWORD_WAIT * 2; i++)); do
			[ -s "$PASSWORD_FILE" ] && break
			sleep 0.5
		done
	fi
	hand_local_token
	start_service dbpanel-agent

	# Give a crashing panel time to exit (or sit in auto-restart) first; must
	# exceed RestartSec=3.
	sleep 5
	if ! systemctl is-active --quiet dbpanel-panel; then
		die "Panel gagal berjalan. Periksa log: journalctl -u dbpanel-panel"
	fi
	if [ -e "$AGENT_TOKEN_FILE" ]; then
		echo "Peringatan: agent host ini belum berpasangan dengan panel. Periksa: journalctl -u dbpanel-agent" >&2
	fi

	local ip
	ip=$(hostname -I 2>/dev/null | awk '{print $1}') || true
	echo
	echo "Panel terpasang."
	echo "Alamat : https://${ip:-<ip-server>}:8080"
	echo "Browser memperingatkan? Buka http://${ip:-<ip-server>}:8080/sertifikat dan pasang sertifikat keamanan panel."
	echo "Pengguna: admin"
	if [ -s "$PASSWORD_FILE" ]; then
		echo "Sandi   : $(cat "$PASSWORD_FILE")"
		echo
		echo "Sandi awal juga tersimpan di $PASSWORD_FILE."
		echo "Peringatan: hapus file ini setelah login pertama: sudo rm $PASSWORD_FILE"
	else
		echo "Sandi   : (sudah diatur sebelumnya; gunakan: sudo -u $PANEL_USER dbpanel panel reset-password)"
	fi
}

# Everything runs from main so a download cut short never runs half a script.
main "$@"
