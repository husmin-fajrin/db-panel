#!/usr/bin/env bash
# Periksa skrip instalasi: sintaks bash (bash -n) untuk scripts/install.sh dan
# hasil render web/templates/agent-install.sh.tmpl, kecocokan unit systemd
# yang ditulis skrip dengan scripts/systemd/*.service, tidak ada berkas pin
# peran/sumber yang dibuat pemasang, pemeriksaan SHA256SUMS binary panel,
# pengguna dbpanel, dan shellcheck bila terpasang. Dengan go dan setpriv,
# install.sh juga dijalankan sungguhan pada root sementara (ROOT=...) dengan
# systemctl tiruan yang menjalankan prosesnya: pasang baru (panel bukan
# root, agent lokal berpasangan), dijalankan ulang, pembaruan dari tata
# letak root, dan pembaruan yang terputus sebelum agent dinyalakan.
# Jalankan dari root repo (sebagai root untuk kasus sungguhan).
set -euo pipefail

PANEL_SCRIPT=scripts/install.sh
AGENT_TEMPLATE=web/templates/agent-install.sh.tmpl

tmp=$(mktemp -d)
# kill_root ROOT stops the processes the fake systemctl started there.
kill_root() {
	local f pid
	for f in "$1"/run/*.pid; do
		[ -f "$f" ] || continue
		pid=$(cat "$f")
		kill "$pid" 2>/dev/null || true
		for _ in {1..100}; do
			kill -0 "$pid" 2>/dev/null || break
			sleep 0.1
		done
		rm -f "$f"
	done
}
cleanup() {
	local r
	for r in "$tmp"/live-*; do
		[ -d "$r" ] && kill_root "$r"
	done
	rm -rf "$tmp"
}
trap cleanup EXIT
failed=0

fail() {
	echo "GAGAL: $*" >&2
	failed=1
}

# unit_from FILE UNIT prints the body of the heredoc that writes UNIT in FILE
# (the lines between "cat >...UNIT <<'UNIT'" and the closing "UNIT").
unit_from() {
	awk -v unit="$2" '
		index($0, unit " <<'\''UNIT'\''") { inside = 1; next }
		inside && /^UNIT$/ { exit }
		inside { print }
	' "$1"
}

# Render the agent template with sample values in place of its actions
# (each value goes through shq, which single-quotes it).
rendered="$tmp/agent-install.sh"
sha_a=$(printf 'a%.0s' {1..64})
sha_b=$(printf 'b%.0s' {1..64})
sha_c=$(printf 'c%.0s' {1..64})
sed -e "s|{{shq \.PanelURL}}|'https://192.0.2.10:8080'|g" \
	-e "s|{{shq \.WebPin}}|'sha256//q83vEjRWeJCrze8SNFZ4kKvN7xI0VniQq83vEjRWeJA='|g" \
	-e "s|{{shq \.Token}}|'ABCDE-FGHJK-MNPQR'|g" \
	-e "s|{{shq \.AgentAddr}}|'192.0.2.10:8443'|g" \
	-e "s|{{shq \.CASHA256}}|'$sha_a'|g" \
	-e "s|{{shq (index \.BinSHA256 \"amd64\")}}|'$sha_b'|g" \
	-e "s|{{shq (index \.BinSHA256 \"arm64\")}}|'$sha_c'|g" \
	"$AGENT_TEMPLATE" >"$rendered"
if grep -q '{{' "$rendered"; then
	fail "$AGENT_TEMPLATE memakai aksi template selain shq PanelURL, WebPin, Token, AgentAddr, CASHA256, BinSHA256 amd64/arm64"
fi
# SEC-23: the agent pins the panel CA and the binary is checked.
# shellcheck disable=SC2016 # the literal text of the script
if ! grep -q -- '--ca-sha256 "$CA_SHA256"' "$rendered"; then
	fail "$AGENT_TEMPLATE tidak memanggil agent pair dengan --ca-sha256"
fi
# The panel is reached over HTTPS, trusted by the pin of its key.
# shellcheck disable=SC2016 # the literal text of the script
if ! grep -q -- '--pinnedpubkey "$WEB_PIN"' "$rendered" || ! grep -q -- 'DBPANEL_WEB_PIN="$WEB_PIN"' "$rendered"; then
	fail "$AGENT_TEMPLATE tidak memakai pin sertifikat panel (WEB_PIN)"
fi
if ! grep -q 'sha256sum -c' "$rendered"; then
	fail "$AGENT_TEMPLATE tidak memeriksa sha256 binary agent"
fi
# The token must never be on a command line (argv is visible in ps).
if grep -n -- '--token' "$rendered"; then
	fail "$AGENT_TEMPLATE memberikan token lewat --token"
fi
if [ "$(grep -c 'ABCDE-FGHJK-MNPQR' "$rendered")" -ne 1 ]; then
	fail "$AGENT_TEMPLATE memakai token di luar TOKEN=..."
fi

for f in "$PANEL_SCRIPT" "$rendered"; do
	if ! bash -n "$f"; then
		fail "bash -n $f"
	fi
	if grep -qE '^[[:space:]]*set -[a-wyz]*x' "$f"; then
		fail "$f memakai set -x (token/sandi bisa bocor ke log)"
	fi
	# set -e ignores a failure anywhere but last in an && list, so a
	# systemctl step chained with && could fail silently.
	if grep -nE '^[[:space:]]*systemctl[^#]*&&' "$f"; then
		fail "$f merangkai systemctl dengan && (kegagalan bisa terlewat oleh set -e)"
	fi
done

check_unit() { # script unit-name
	if ! diff -u "scripts/systemd/$2" <(unit_from "$1" "$2"); then
		fail "unit $2 di $1 berbeda dari scripts/systemd/$2"
	fi
}
check_unit "$PANEL_SCRIPT" dbpanel-panel.service
check_unit "$PANEL_SCRIPT" dbpanel-agent.service
check_unit "$PANEL_SCRIPT" dbpanel-agent.service.d/panel.conf
check_unit "$rendered" dbpanel-agent.service

# The panel runs as user dbpanel and writes only its own directories; this
# host's agent starts after it. No port below 1024 is configurable yet, so
# the unit grants no capability.
panel_unit=scripts/systemd/dbpanel-panel.service
for want in User=dbpanel ProtectSystem=strict "ReadWritePaths=/var/lib/dbpanel/panel /etc/dbpanel/panel" \
	NoNewPrivileges=yes; do
	if ! grep -qx "$want" "$panel_unit"; then
		fail "$panel_unit tidak memuat $want"
	fi
done
if grep -qE '^(User=root|AmbientCapabilities=|CapabilityBoundingSet=CAP_)' "$panel_unit"; then
	fail "$panel_unit memberi panel hak root atau kapabilitas"
fi
if ! grep -qx 'After=dbpanel-panel.service' scripts/systemd/dbpanel-agent.service.d/panel.conf; then
	fail "agent host panel tidak dinyalakan setelah panel"
fi

# SEC-30: the agent writes /etc/dbpanel, MySQL's config and /usr (install),
# so its unit has no ProtectSystem; a mysqld it started through an init
# script must survive an agent restart (KillMode=process). The rest of the
# hardening stays.
agent_unit=scripts/systemd/dbpanel-agent.service
if grep -qE '^[[:space:]]*ProtectSystem=' "$agent_unit"; then
	fail "$agent_unit memakai ProtectSystem (agent harus bisa menulis /etc/dbpanel dan konfigurasi MySQL)"
fi
for want in KillMode=process NoNewPrivileges=yes PrivateTmp=yes ProtectHome=yes ProtectKernelTunables=yes \
	ProtectKernelModules=yes ProtectControlGroups=yes RestrictSUIDSGID=yes; do
	if ! grep -qx "$want" "$agent_unit"; then
		fail "$agent_unit tidak memuat $want"
	fi
done

# A fresh install pins no role or source: /etc/dbpanel/role and
# /etc/dbpanel/source are written only by the agent itself (on the first
# successful "Siapkan master"/"Buat replika", or from MySQL's state on an
# upgraded server) and by "dbpanel agent set-role/set-source". Neither
# installer may create, copy or touch them.
pin_re='(/etc/dbpanel|\$\{?CONFIG_DIR\}?"?)/(role|source)([^A-Za-z0-9_.-]|$)'
for f in "$PANEL_SCRIPT" "$rendered"; do
	if grep -nE "$pin_re" "$f"; then
		fail "$f menulis berkas pin peran/sumber (/etc/dbpanel/role atau /etc/dbpanel/source)"
	fi
done

# SEC-07: the online install checks the panel binary against SHA256SUMS.
# Run install_binary from install.sh (without main) with curl replaced by
# a function serving fixture files.
release="$tmp/release"
mkdir -p "$release"
printf 'binary panel amd64' >"$release/dbpanel_linux_amd64"
printf 'binary panel arm64' >"$release/dbpanel_linux_arm64"
(cd "$release" && sha256sum dbpanel_linux_amd64 dbpanel_linux_arm64 >SHA256SUMS)
# install_case NAME prepares the release with the given change, runs
# install_binary amd64 and prints its output; the exit status is its own.
install_case() {
	(
		# shellcheck source=/dev/null
		source <(sed '/^main "\$@"$/d' "$PANEL_SCRIPT")
		# shellcheck disable=SC2034 # read by install_binary
		BIN="$tmp/case-$1/dbpanel"
		AGENT_TMP="$tmp/case-$1/dl"
		mkdir -p "$AGENT_TMP"
		served="$tmp/case-$1/served"
		cp -r "$release" "$served"
		case $1 in
		tampered) printf 'binary jahat' >"$served/dbpanel_linux_amd64" ;;
		unlisted) sed -i '/dbpanel_linux_amd64/d' "$served/SHA256SUMS" ;;
		esac
		# shellcheck disable=SC2329 # called by install_binary
		curl() { # curl -fsSL URL -o FILE
			cp "$served/${2##*/}" "$4"
		}
		install_binary amd64
	) 2>&1
}
if ! out=$(install_case good) || [ "$(cat "$tmp/case-good/dbpanel")" != 'binary panel amd64' ]; then
	fail "install_binary menolak binary yang cocok dengan SHA256SUMS: $out"
fi
for c in tampered unlisted; do
	if out=$(install_case "$c"); then
		fail "install_binary memasang binary $c"
	elif [[ $out != *"Checksum binary panel tidak cocok"* ]]; then
		fail "install_binary $c: pesan salah: $out"
	fi
	if [ -e "$tmp/case-$c/dbpanel" ]; then
		fail "install_binary $c: binary tetap terpasang"
	fi
done

# fetch_agent_binaries: an agent binary missing from SHA256SUMS is dropped
# with a warning (not passed by --ignore-missing), a listed one that does
# not match fails the whole fetch.
fetch_case() {
	(
		# shellcheck source=/dev/null
		source <(sed '/^main "\$@"$/d' "$PANEL_SCRIPT")
		dl="$tmp/fetch-$1/dl"
		served="$tmp/fetch-$1/served"
		mkdir -p "$dl"
		cp -r "$release" "$served"
		case $1 in
		unlisted) sed -i '/dbpanel_linux_arm64/d' "$served/SHA256SUMS" ;;
		tampered) printf 'binary jahat' >"$served/dbpanel_linux_arm64" ;;
		esac
		# shellcheck disable=SC2329 # called by fetch_agent_binaries
		curl() { # curl -fsSL URL -o FILE
			cp "$served/${2##*/}" "$4"
		}
		fetch_agent_binaries "$dl"
	) 2>&1
}
if ! out=$(fetch_case good) || [ ! -f "$tmp/fetch-good/dl/dbpanel_linux_arm64" ]; then
	fail "fetch_agent_binaries menolak rilis yang benar: $out"
fi
if ! out=$(fetch_case unlisted); then
	fail "fetch_agent_binaries gagal karena arm64 tidak tercantum (harusnya dilewati): $out"
elif [ -e "$tmp/fetch-unlisted/dl/dbpanel_linux_arm64" ] || [[ $out != *"dbpanel_linux_arm64 tidak tercantum di SHA256SUMS; dilewati"* ]]; then
	fail "fetch_agent_binaries tidak membuang arm64 yang tidak tercantum: $out"
elif [ ! -f "$tmp/fetch-unlisted/dl/dbpanel_linux_amd64" ]; then
	fail "fetch_agent_binaries membuang amd64 yang tercantum"
fi
if out=$(fetch_case tampered); then
	fail "fetch_agent_binaries menerima arm64 yang tidak cocok: $out"
fi

# ensure_user: the system user is created once, with no login shell and the
# panel's data directory as home.
user_case() { # existing(0|1)
	(
		local exists=$1
		# shellcheck source=/dev/null
		source <(sed '/^main "\$@"$/d' "$PANEL_SCRIPT")
		# shellcheck disable=SC2329 # called by ensure_user
		id() { return "$exists"; }
		# shellcheck disable=SC2329 # called by ensure_user
		useradd() { echo "useradd $*"; }
		ensure_user
	) 2>&1
}
want_useradd="useradd --system --home /var/lib/dbpanel/panel --shell /usr/sbin/nologin dbpanel"
if out=$(user_case 1) && [[ $out != *"$want_useradd"* ]]; then
	fail "ensure_user tanpa pengguna: $out"
fi
if ! out=$(user_case 0) || [[ $out == *useradd* ]]; then
	fail "ensure_user membuat pengguna yang sudah ada: $out"
fi

# Live cases: install.sh against a temporary root, with systemctl replaced
# by a function that runs the panel as $PANEL_USER (nobody here: the test
# creates no user) and the agent as root, as the units do.
live_bin="$tmp/dbpanel"
run_install() { # ROOT UI_PORT AGENT_PORT [svc:SERVICE|fn:FUNCTION: stop right before it]
	(
		ROOT=$1
		# shellcheck source=/dev/null
		source <(sed '/^main "\$@"$/d' "$PANEL_SCRIPT")
		PANEL_USER=nobody
		# shellcheck disable=SC2034 # read by install_binary
		DBPANEL_BIN=$live_bin
		svc_pid() { echo "$ROOT/run/$1.pid"; }
		svc_running() { [ -f "$(svc_pid "$1")" ] && kill -0 "$(cat "$(svc_pid "$1")")" 2>/dev/null; }
		svc_stop() { kill_root_one "$(svc_pid "$1")"; }
		kill_root_one() {
			[ -f "$1" ] || return 0
			local pid
			pid=$(cat "$1")
			kill "$pid" 2>/dev/null || true
			for _ in {1..100}; do
				kill -0 "$pid" 2>/dev/null || break
				sleep 0.1
			done
			rm -f "$1"
		}
		svc_start() {
			mkdir -p "$ROOT/run"
			case $1 in
			dbpanel-panel)
				setpriv --reuid="$PANEL_USER" --regid="$(id -g "$PANEL_USER")" --clear-groups \
					"$BIN" panel --data-dir "$PANEL_DATA_DIR" --config-dir "$PANEL_CONFIG_DIR" \
					--http "127.0.0.1:$2" --agent-listen "127.0.0.1:$3" --public-host 127.0.0.1 \
					</dev/null >>"$ROOT/panel.log" 2>&1 &
				;;
			dbpanel-agent)
				"$BIN" agent --dir "$CONFIG_DIR" --panel "https://127.0.0.1:$2" </dev/null >>"$ROOT/agent.log" 2>&1 &
				;;
			esac
			echo $! >"$(svc_pid "$1")"
		}
		ui=$2 ag=$3
		# shellcheck disable=SC2329 # called by main
		systemctl() {
			case $1 in
			daemon-reload) ;;
			is-active) svc_running "$3" ;;
			stop) svc_stop "$2" ;;
			restart) svc_stop "$2" && svc_start "$2" "$ui" "$ag" ;;
			enable) if [ "$2" = --now ] && ! svc_running "$3"; then svc_start "$3" "$ui" "$ag"; fi ;;
			*) return 1 ;;
			esac
		}
		case ${4:-} in
		svc:*)
			# Cut short right before the given service starts.
			stop_at=${4#svc:}
			eval "$(declare -f start_service | sed '1s/start_service/real_start_service/')"
			# shellcheck disable=SC2329 # called by main
			start_service() {
				[ "$1" = "$stop_at" ] && exit 3
				real_start_service "$@"
			}
			;;
		fn:*) eval "${4#fn:}() { exit 3; }" ;;
		esac
		main
	) >"$1/install.out" 2>&1
}
# live_checks ROOT UI_PORT NAME: the panel answers over HTTPS as $PANEL_USER,
# the local agent is paired and connected, the files have their owners.
live_checks() {
	local root=$1 port=$2 name=$3 code user
	code=$(curl -ks -o /dev/null -w '%{http_code}' "https://127.0.0.1:$port/login") || true
	[ "$code" = 200 ] || fail "$name: GET /login = $code"
	user=$(ps -o user= -p "$(cat "$root/run/dbpanel-panel.pid")" 2>/dev/null | tr -d ' ') || true
	[ "$user" = nobody ] || fail "$name: panel berjalan sebagai '$user', bukan pengguna panel"
	for _ in {1..100}; do
		grep -q 'msg="agent connected" server_id=1 ' "$root/panel.log" && break
		sleep 0.2
	done
	grep -q 'msg="agent connected" server_id=1 ' "$root/panel.log" || fail "$name: agent lokal tidak terhubung"
	[ -f "$root/etc/dbpanel/agent.yaml" ] || fail "$name: agent lokal belum berpasangan"
	for f in etc/dbpanel/local-pair-token etc/dbpanel/panel/local-pair-token etc/dbpanel/ca.key var/lib/dbpanel/panel.db; do
		[ ! -e "$root/$f" ] || fail "$name: $f masih ada"
	done
	for f in etc/dbpanel/panel/ca.key etc/dbpanel/panel/web.key etc/dbpanel/panel/panel.key var/lib/dbpanel/panel/panel.db; do
		[ "$(stat -c %U "$root/$f" 2>/dev/null)" = nobody ] || fail "$name: $f bukan milik pengguna panel"
	done
	for d in etc/dbpanel var/lib/dbpanel; do
		[ "$(stat -c '%U %a' "$root/$d")" = "root 711" ] || fail "$name: $d bukan root 0711"
	done
	[ "$(stat -c '%U %a' "$root/etc/dbpanel/panel")" = "nobody 700" ] || fail "$name: etc/dbpanel/panel bukan 0700 pengguna panel"
}
kill_root_svc() { # ROOT SERVICE
	local f="$1/run/$2.pid" pid
	[ -f "$f" ] || return 0
	pid=$(cat "$f")
	kill "$pid" 2>/dev/null || true
	for _ in {1..100}; do
		kill -0 "$pid" 2>/dev/null || break
		sleep 0.1
	done
	rm -f "$f"
}
new_root() { # NAME: a root with what the host has before install.sh
	local r="$tmp/live-$1"
	mkdir -p "$r/usr/local/bin" "$r/run"
	echo "$r"
}
free_port() { echo $((20000 + RANDOM % 20000)); }

if [ "$(id -u)" -ne 0 ] || ! command -v go >/dev/null 2>&1 || ! command -v setpriv >/dev/null 2>&1; then
	echo "bukan root, atau go/setpriv tidak ada; kasus install.sh sungguhan dilewati"
elif ! go build -o "$live_bin" ./cmd/dbpanel; then
	fail "go build ./cmd/dbpanel"
else
	chmod 0711 "$tmp"

	# fresh_nonroot
	r=$(new_root fresh) ui=$(free_port) ag=$(free_port)
	if ! run_install "$r" "$ui" "$ag"; then
		fail "fresh_nonroot: install.sh gagal: $(cat "$r/install.out")"
	else
		grep -q 'Sandi   : ' "$r/install.out" || fail "fresh_nonroot: sandi awal tidak dicetak"
		live_checks "$r" "$ui" fresh_nonroot
	fi

	# rerun_idempotent: the paired agent keeps its identity.
	agent_sum=$(sha256sum "$r/etc/dbpanel/agent.yaml" 2>/dev/null) || true
	if ! run_install "$r" "$ui" "$ag"; then
		fail "rerun_idempotent: install.sh gagal: $(cat "$r/install.out")"
	else
		live_checks "$r" "$ui" rerun_idempotent
		[ "$(sha256sum "$r/etc/dbpanel/agent.yaml")" = "$agent_sum" ] || fail "rerun_idempotent: agent dipasangkan ulang"
	fi
	kill_root "$r"

	# upgrade_from_root: the layout of a panel that ran as root.
	r=$(new_root upgrade) ui=$(free_port) ag=$(free_port)
	mkdir -p "$r/etc/dbpanel" "$r/var/lib/dbpanel/bin"
	chmod 0700 "$r/etc/dbpanel" "$r/var/lib/dbpanel"
	"$live_bin" panel --allow-root --data-dir "$r/var/lib/dbpanel" --config-dir "$r/etc/dbpanel" \
		--http "127.0.0.1:$ui" --agent-listen "127.0.0.1:$ag" --public-host 127.0.0.1 </dev/null >"$r/old.log" 2>&1 &
	echo $! >"$r/run/old.pid"
	for _ in {1..100}; do
		[ -s "$r/etc/dbpanel/local-pair-token" ] && break
		sleep 0.1
	done
	kill_root "$r"
	rm -f "$r/etc/dbpanel/local-pair-token" # earlier versions wrote none
	printf 'binary agent' >"$r/var/lib/dbpanel/bin/dbpanel_linux_amd64"
	"$live_bin" agent set-role master --dir "$r/etc/dbpanel" >/dev/null
	declare -A before
	for f in var/lib/dbpanel/panel.db etc/dbpanel/ca.key etc/dbpanel/web.key etc/dbpanel/mysql-ca.key \
		etc/dbpanel/panel.key etc/dbpanel/admin-initial-password var/lib/dbpanel/bin/dbpanel_linux_amd64; do
		before[$f]=$(sha256sum <"$r/$f") || fail "upgrade_from_root: tata letak lama tanpa $f"
	done
	role_sum=$(sha256sum <"$r/etc/dbpanel/role")

	# upgrade_from_root, stopped after the move and before anything opens
	# panel.db (releasing the local agent, starting the panel): every file
	# is in its subfolder, the panel user's, with the same content.
	if run_install "$r" "$ui" "$ag" fn:release_local_agent; then
		fail "upgrade_from_root: install.sh tidak berhenti sebelum panel"
	fi
	for f in "${!before[@]}"; do
		new=${f/etc\/dbpanel\//etc/dbpanel/panel/}
		new=${new/var\/lib\/dbpanel\//var/lib/dbpanel/panel/}
		[ -e "$r/$new" ] || fail "upgrade_from_root: $new tidak ada setelah pindah"
		[ "$(stat -c %U "$r/$new" 2>/dev/null)" = nobody ] || fail "upgrade_from_root: $new bukan milik pengguna panel"
		[ "$(sha256sum <"$r/$new")" = "${before[$f]}" ] || fail "upgrade_from_root: isi $new berubah"
	done

	# interrupted_after_move: run again, now cut short after the panel ran
	# and the token was handed over, right before the agent starts.
	if run_install "$r" "$ui" "$ag" svc:dbpanel-agent; then
		fail "interrupted_after_move: install.sh tidak terputus"
	fi
	[ -s "$r/etc/dbpanel/local-pair-token" ] || fail "interrupted_after_move: token belum diserahkan ke agent"
	[ "$(stat -c '%U %a' "$r/etc/dbpanel/local-pair-token" 2>/dev/null)" = "root 600" ] ||
		fail "interrupted_after_move: token agent bukan root 0600"
	[ ! -e "$r/etc/dbpanel/agent.yaml" ] || fail "interrupted_after_move: agent sudah berjalan"
	if ! run_install "$r" "$ui" "$ag"; then
		fail "upgrade_from_root: install.sh setelah terputus gagal: $(cat "$r/install.out")"
	else
		live_checks "$r" "$ui" upgrade_from_root
		[ "$(sha256sum <"$r/etc/dbpanel/role")" = "$role_sum" ] || fail "upgrade_from_root: pin /etc/dbpanel/role berubah"
	fi

	# lost_agent_yaml: this host's agent lost its files (or the panel moved
	# here) while the panel's database has the server paired: install.sh
	# releases it and the agent pairs again, as the same server.
	kill_root_svc "$r" dbpanel-agent
	rm -f "$r/etc/dbpanel/agent.yaml" "$r/etc/dbpanel/agent.crt" "$r/etc/dbpanel/agent.key"
	: >"$r/panel.log"
	if ! run_install "$r" "$ui" "$ag"; then
		fail "lost_agent_yaml: install.sh gagal: $(cat "$r/install.out")"
	else
		live_checks "$r" "$ui" lost_agent_yaml
		[ "$(sha256sum <"$r/etc/dbpanel/role")" = "$role_sum" ] || fail "lost_agent_yaml: pin /etc/dbpanel/role berubah"
	fi
	kill_root "$r"

	# Root never follows a link the panel user put in its directories.
	r=$(new_root links)
	victim="$tmp/live-links-victim"
	mkdir -p "$victim" "$r/etc/dbpanel" "$r/var/lib/dbpanel"
	chmod 0700 "$victim"
	printf 'rahasia\n' >"$victim/secret"
	chmod 0640 "$victim/secret"
	victim_state=$(stat -c '%U %G %a' "$victim" "$victim/secret")
	mkdir -p "$tmp/rel1" "$tmp/rel2"
	cp "$release/dbpanel_linux_amd64" "$tmp/rel1/"
	cp "$release/dbpanel_linux_arm64" "$tmp/rel2/"
	links_case() { # STEP
		(
			ROOT=$r
			# shellcheck source=/dev/null
			source <(sed '/^main "\$@"$/d' "$PANEL_SCRIPT")
			PANEL_USER=nobody
			TOKEN_WAIT=1
			prepare_dirs
			case $1 in
			bins) copy_agent_binaries "$tmp/rel1" && copy_agent_binaries "$tmp/rel2" ;;
			bin-link)
				rm -rf "$PANEL_DATA_DIR/bin"
				ln -s "$victim" "$PANEL_DATA_DIR/bin"
				copy_agent_binaries "$tmp/rel1"
				;;
			token-link)
				ln -sfn "$victim/secret" "$PANEL_TOKEN_FILE"
				hand_local_token
				;;
			password-link)
				ln -sfn "$victim/secret" "$PASSWORD_FILE"
				print_summary
				;;
			bin-special)
				# A bin/ the panel user owns (as after the move from the
				# old layout) holding a FIFO and a link to /dev/zero.
				rm -rf "$PANEL_DATA_DIR/bin"
				mkdir "$PANEL_DATA_DIR/bin"
				mkfifo "$PANEL_DATA_DIR/bin/dbpanel_linux_amd64"
				ln -s /dev/zero "$PANEL_DATA_DIR/bin/dbpanel_linux_arm64"
				chown -R -h "$PANEL_USER:" "$PANEL_DATA_DIR/bin"
				copy_agent_binaries "$tmp/rel1"
				;;
			esac
		) >"$r/$1.out" 2>&1
	}
	# Both architectures stay served; bin/ is root's, its files readable.
	if ! links_case bins; then
		fail "copy_agent_binaries: $(cat "$r/bins.out")"
	else
		for a in amd64 arm64; do
			cmp -s "$release/dbpanel_linux_$a" "$r/var/lib/dbpanel/panel/bin/dbpanel_linux_$a" ||
				fail "copy_agent_binaries: dbpanel_linux_$a hilang atau berbeda"
		done
		[ "$(stat -c '%U %a' "$r/var/lib/dbpanel/panel/bin")" = "root 755" ] || fail "copy_agent_binaries: bin/ bukan root 0755"
	fi
	if links_case bin-link || [[ $(cat "$r/bin-link.out") != *"bukan direktori biasa"* ]]; then
		fail "copy_agent_binaries mengikuti bin/ yang berupa tautan: $(cat "$r/bin-link.out")"
	fi
	if links_case token-link; then
		fail "hand_local_token menerima token yang berupa tautan: $(cat "$r/token-link.out")"
	fi
	if grep -rqs rahasia "$r/etc/dbpanel"; then
		fail "hand_local_token menyalin isi berkas lain"
	fi
	# The summary reads the initial password as the panel user: a link to a
	# file only root can read prints nothing of it.
	if ! links_case password-link; then
		fail "print_summary gagal: $(cat "$r/password-link.out")"
	elif grep -q rahasia "$r/password-link.out"; then
		fail "print_summary mencetak isi berkas lain lewat tautan sandi awal"
	fi
	# Only regular files are kept from the old bin/, within bounds: a FIFO
	# or /dev/zero neither hangs the script nor fills the disk.
	links_case bin-special &
	special=$!
	for _ in {1..1200}; do
		kill -0 "$special" 2>/dev/null || break
		sleep 0.1
	done
	if kill -0 "$special" 2>/dev/null; then
		pkill -P "$special" 2>/dev/null || true
		kill "$special" 2>/dev/null || true
		fail "copy_agent_binaries macet pada FIFO di bin/"
	elif ! wait "$special"; then
		fail "copy_agent_binaries dengan FIFO atau /dev/zero di bin/: $(cat "$r/bin-special.out")"
	else
		cmp -s "$release/dbpanel_linux_amd64" "$r/var/lib/dbpanel/panel/bin/dbpanel_linux_amd64" ||
			fail "copy_agent_binaries: dbpanel_linux_amd64 tidak terpasang setelah FIFO"
		[ ! -e "$r/var/lib/dbpanel/panel/bin/dbpanel_linux_arm64" ] ||
			fail "copy_agent_binaries menyimpan /dev/zero sebagai dbpanel_linux_arm64"
	fi
	[ "$(stat -c '%U %G %a' "$victim" "$victim/secret")" = "$victim_state" ] ||
		fail "pemilik atau izin berkas di luar direktori panel berubah"
fi

if command -v shellcheck >/dev/null 2>&1; then
	if ! shellcheck "$PANEL_SCRIPT" "$rendered" scripts/install_test.sh; then
		fail shellcheck
	fi
else
	echo "shellcheck tidak terpasang; dilewati"
fi

if [ "$failed" -ne 0 ]; then
	exit 1
fi
echo "ok  scripts/install_test.sh"
