#!/usr/bin/env bash
# Periksa skrip instalasi: sintaks bash (bash -n) untuk scripts/install.sh dan
# hasil render web/templates/agent-install.sh.tmpl, kecocokan unit systemd
# yang ditulis skrip dengan scripts/systemd/*.service, pemeriksaan
# SHA256SUMS binary panel, dan shellcheck bila terpasang. Jalankan dari root
# repo.
set -euo pipefail

PANEL_SCRIPT=scripts/install.sh
AGENT_TEMPLATE=web/templates/agent-install.sh.tmpl

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
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
sed -e "s|{{shq \.PanelURL}}|'http://192.0.2.10:8080'|g" \
	-e "s|{{shq \.Token}}|'ABCDE-FGHJK-MNPQR'|g" \
	-e "s|{{shq \.AgentAddr}}|'192.0.2.10:8443'|g" \
	-e "s|{{shq \.CASHA256}}|'$sha_a'|g" \
	-e "s|{{shq (index \.BinSHA256 \"amd64\")}}|'$sha_b'|g" \
	-e "s|{{shq (index \.BinSHA256 \"arm64\")}}|'$sha_c'|g" \
	"$AGENT_TEMPLATE" >"$rendered"
if grep -q '{{' "$rendered"; then
	fail "$AGENT_TEMPLATE memakai aksi template selain shq PanelURL, Token, AgentAddr, CASHA256, BinSHA256 amd64/arm64"
fi
# SEC-23: the agent pins the panel CA and the binary is checked.
# shellcheck disable=SC2016 # the literal text of the script
if ! grep -q -- '--ca-sha256 "$CA_SHA256"' "$rendered"; then
	fail "$AGENT_TEMPLATE tidak memanggil agent pair dengan --ca-sha256"
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
check_unit "$rendered" dbpanel-agent.service

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
