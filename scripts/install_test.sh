#!/usr/bin/env bash
# Periksa skrip instalasi: sintaks bash (bash -n) untuk scripts/install.sh dan
# hasil render web/templates/agent-install.sh.tmpl, kecocokan unit systemd
# yang ditulis skrip dengan scripts/systemd/*.service, dan shellcheck bila
# terpasang. Jalankan dari root repo.
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

# Render the agent template with sample values in place of its actions.
rendered="$tmp/agent-install.sh"
sed -e 's|{{\.PanelURL}}|http://192.0.2.10:8080|g' \
	-e 's|{{\.Token}}|ABCDEFGHJKMNPQRS|g' \
	-e 's|{{\.AgentAddr}}|192.0.2.10:8443|g' \
	"$AGENT_TEMPLATE" >"$rendered"
if grep -q '{{' "$rendered"; then
	fail "$AGENT_TEMPLATE memakai aksi template selain PanelURL, Token, AgentAddr"
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
	if grep -nE '^[^#]*systemctl[^#]*&&' "$f"; then
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
