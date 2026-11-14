package mysql_test

import (
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// listIncomplete is the C-1 refusal for account ('user'@'host').
func listIncomplete(account string) string {
	return "daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL " + account + " yang dipakai agent " +
		"tidak punya hak SHOW DATABASES. Jalankan sebagai root MySQL di server ini: GRANT SHOW DATABASES ON *.* TO " +
		account + "; lalu ulangi bangun ulang dari panel."
}

// onTarget runs stmts as root on the quarantined target, with
// super_read_only lifted around them (account changes are writes).
func (r *settleRig) onTarget(t *testing.T, stmts ...string) {
	t.Helper()
	db := rootDB(t, r.target)
	execAll(t, db, append(append([]string{"SET GLOBAL super_read_only = OFF"}, stmts...), "SET GLOBAL super_read_only = ON")...)
}

// useAccount makes the agent's stored admin account user@localhost (no
// password, over the socket).
func (r *settleRig) useAccount(user string) {
	r.creds.p = proto.ConnectParams{User: user}
}

// assertStillQuarantined: the role pin and root's approval are unchanged.
func (r *settleRig) assertStillQuarantined(t *testing.T) {
	t.Helper()
	if role := pinFile(t, r.dir, mysql.RoleFile); role != "quarantined" {
		t.Errorf("role pin = %q, want quarantined", role)
	}
	if got := pinFile(t, r.dir, mysql.RebuildApprovalFile); got == "" {
		t.Error("the approval was cleared")
	}
}

// targetHas reports whether the target has database name (read as root).
func (r *settleRig) targetHas(t *testing.T, name string) bool {
	t.Helper()
	var n int
	if err := rootDB(t, r.target).QueryRow("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = ?", name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}

// TestRebuildScopedAccountCannotSettle: the re-review's scenario. While the
// server was still a master a compromised panel stored an account with the
// global administrative privileges the guard and the rebuild use, but data
// privileges on one database only: the old master's databases are
// invisible to it. A dump of that one database under root's approval must
// not load anything, and the server stays quarantined.
func TestRebuildScopedAccountCannotSettle(t *testing.T) {
	r := newSettleRig(t, "rs_lama")
	r.onTarget(t, "CREATE USER rs_scoped@localhost",
		"GRANT RELOAD, SUPER, PROCESS, REPLICATION CLIENT ON *.* TO rs_scoped@localhost",
		"GRANT SYSTEM_VARIABLES_ADMIN, REPLICATION_SLAVE_ADMIN ON *.* TO rs_scoped@localhost",
		"GRANT ALL ON kecil.* TO rs_scoped@localhost")
	r.useAccount("rs_scoped")

	d := r.snap.run(t, "mysql.dumpFrom", dumpParams(r.master, "kecil"))
	if d.OK || d.Error != listIncomplete("'rs_scoped'@'localhost'") {
		t.Fatalf("dumpFrom{kecil} with a database-scoped account = %+v\nwant the error %q", d, listIncomplete("'rs_scoped'@'localhost'"))
	}
	if r.targetHas(t, "kecil") {
		t.Error("the refused dump loaded kecil")
	}
	r.dispatch("mysql.configureReplication", sourceParams(r.master, "kecil"))
	r.assertStillQuarantined(t)
}

// TestRebuildPartialRevokeAccount: with partial_revokes on, an account
// holding every global privilege but SHOW DATABASES, with everything on the
// old database revoked, cannot see that database: the dump is refused. The
// advised GRANT SHOW DATABASES makes the listing complete: the dump runs,
// and the settle sees the old database (A6).
func TestRebuildPartialRevokeAccount(t *testing.T) {
	r := newSettleRig(t, "rs_lama")
	r.onTarget(t, "SET GLOBAL partial_revokes = ON", "CREATE USER rs_partial@localhost",
		"GRANT ALL ON *.* TO rs_partial@localhost WITH GRANT OPTION",
		"REVOKE SHOW DATABASES ON *.* FROM rs_partial@localhost",
		"REVOKE ALL ON rs_lama.* FROM rs_partial@localhost")
	r.useAccount("rs_partial")

	want := listIncomplete("'rs_partial'@'localhost'")
	if d := r.snap.run(t, "mysql.dumpFrom", dumpParams(r.master, "kecil")); d.OK || d.Error != want {
		t.Fatalf("dumpFrom with a partial revoke = %+v\nwant the error %q", d, want)
	}
	if r.targetHas(t, "kecil") {
		t.Error("the refused dump loaded kecil")
	}

	r.onTarget(t, "GRANT SHOW DATABASES ON *.* TO rs_partial@localhost")
	if d := r.snap.run(t, "mysql.dumpFrom", dumpParams(r.master, "kecil")); !d.OK {
		t.Fatalf("dumpFrom after the advised GRANT = %+v", d)
	}
	resp := r.dispatch("mysql.configureReplication", sourceParams(r.master, "kecil"))
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "(rs_lama); karantina tidak diakhiri") {
		t.Fatalf("configureReplication = %+v, want A6 naming rs_lama", resp)
	}
	r.assertStillQuarantined(t)
}

// TestRebuildRoleOnlyAccountAtSettle: the copy was made with a complete
// account, but the account stored at the settle holds its privileges only
// through a role: the settle is refused with the C-1 text, the role and the
// approval stay. After the advised GRANT the same settle ends the
// quarantine.
func TestRebuildRoleOnlyAccountAtSettle(t *testing.T) {
	r := newSettleRig(t)
	if d := r.snap.run(t, "mysql.dumpFrom", dumpParams(r.master, "kecil")); !d.OK {
		t.Fatalf("dumpFrom{kecil} as root = %+v", d)
	}
	r.onTarget(t, "CREATE ROLE rs_all", "GRANT ALL ON *.* TO rs_all WITH GRANT OPTION",
		"CREATE USER rs_viarole@localhost", "GRANT rs_all TO rs_viarole@localhost",
		"SET DEFAULT ROLE rs_all TO rs_viarole@localhost")
	r.useAccount("rs_viarole")

	want := listIncomplete("'rs_viarole'@'localhost'")
	resp := r.dispatch("mysql.configureReplication", sourceParams(r.master, "kecil"))
	if resp.OK || resp.Error == nil || resp.Error.Message != want {
		t.Fatalf("configureReplication with a role-only account = %+v\nwant the error %q", resp, want)
	}
	r.assertStillQuarantined(t)

	r.onTarget(t, "GRANT SHOW DATABASES ON *.* TO rs_viarole@localhost")
	decode[proto.ReplicaStatus](t, r.dispatch("mysql.configureReplication", sourceParams(r.master, "kecil")))
	if role := pinFile(t, r.dir, mysql.RoleFile); role != "replica" {
		t.Errorf("role pin after the advised GRANT = %q, want replica", role)
	}
}

// listUnsupported is the E-1 refusal for an account name the proof does not
// handle.
const listUnsupported = "daftar database di server ini tidak bisa dipastikan lengkap: nama akun MySQL yang dipakai " +
	"agent memuat karakter yang tidak didukung pemeriksaan ini. Hubungkan MySQL di panel dengan akun lain yang " +
	"namanya hanya huruf, angka, garis bawah, titik, atau tanda hubung (misalnya dbpanel_admin), lalu ulangi bangun " +
	"ulang dari panel."

// TestRebuildCraftedAccountNameCannotSettle: the re-review's forgery. The
// database-scoped account of TestRebuildScopedAccountCannotSettle is named
// so that its own SHOW GRANTS rows hold " SHOW DATABASES ON *.* TO ": the
// dump is refused with the unsupported-name text, nothing is loaded, and
// the server stays quarantined.
func TestRebuildCraftedAccountNameCannotSettle(t *testing.T) {
	r := newSettleRig(t, "rs_lama")
	const acct = "'p, SHOW DATABASES ON *.* TO x'@localhost"
	r.onTarget(t, "CREATE USER "+acct,
		"GRANT RELOAD, SUPER, PROCESS, REPLICATION CLIENT ON *.* TO "+acct,
		"GRANT SYSTEM_VARIABLES_ADMIN, REPLICATION_SLAVE_ADMIN ON *.* TO "+acct,
		"GRANT ALL ON kecil.* TO "+acct, "GRANT SELECT ON performance_schema.* TO "+acct)
	r.useAccount("p, SHOW DATABASES ON *.* TO x")

	d := r.snap.run(t, "mysql.dumpFrom", dumpParams(r.master, "kecil"))
	if d.OK || d.Error != listUnsupported {
		t.Fatalf("dumpFrom{kecil} with a crafted account name = %+v\nwant the error %q", d, listUnsupported)
	}
	if r.targetHas(t, "kecil") {
		t.Error("the refused dump loaded kecil")
	}
	r.dispatch("mysql.configureReplication", sourceParams(r.master, "kecil"))
	r.assertStillQuarantined(t)
}

// TestRebuildCraftedRoleNameCannotSettle: an ordinary database-scoped
// account that holds a role whose name holds " SHOW DATABASES ON *.* TO ":
// the role row proves nothing, and the dump is refused with the listing
// text.
func TestRebuildCraftedRoleNameCannotSettle(t *testing.T) {
	r := newSettleRig(t, "rs_lama")
	const role = "'a`,SHOW DATABASES ON *.* TO b'"
	r.onTarget(t, "CREATE USER rs_scoped@localhost",
		"GRANT RELOAD, SUPER, PROCESS, REPLICATION CLIENT ON *.* TO rs_scoped@localhost",
		"GRANT SYSTEM_VARIABLES_ADMIN, REPLICATION_SLAVE_ADMIN ON *.* TO rs_scoped@localhost",
		"GRANT ALL ON kecil.* TO rs_scoped@localhost", "GRANT SELECT ON performance_schema.* TO rs_scoped@localhost",
		"CREATE ROLE "+role, "GRANT "+role+" TO rs_scoped@localhost")
	r.useAccount("rs_scoped")

	want := listIncomplete("'rs_scoped'@'localhost'")
	d := r.snap.run(t, "mysql.dumpFrom", dumpParams(r.master, "kecil"))
	if d.OK || d.Error != want {
		t.Fatalf("dumpFrom{kecil} with a crafted role = %+v\nwant the error %q", d, want)
	}
	if r.targetHas(t, "kecil") {
		t.Error("the refused dump loaded kecil")
	}
	r.dispatch("mysql.configureReplication", sourceParams(r.master, "kecil"))
	r.assertStillQuarantined(t)
}
