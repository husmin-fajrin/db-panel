package mysql_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// listIncomplete is the listing refusal for account ('user'@'host'), T16.
func listIncomplete(account string) string {
	return "daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL " + account + " yang dipakai agent " +
		"tidak punya hak SHOW DATABASES. Jalankan di server ini: sudo dbpanel agent grant-admin, lalu tekan \"Ulangi " +
		"dari tahap ini\" pada pekerjaan di panel."
}

// grantAsCommand gives account (CURRENT_USER()'s user@host) what the
// refusal's advice, sudo dbpanel agent grant-admin, gives it, the way that
// command does on the quarantined target: the statements are
// mysql.MissingAdminGrants read as root, and they run on one root
// connection with sql_log_bin off between SET GLOBAL super_read_only = OFF
// and = ON (the CLI's window, runLifted; the CLI itself is tested in
// package agent). They must hold GRANT SHOW DATABASES. The target must be
// read-only before, and is so again after, with gtid_executed unchanged.
func (r *settleRig) grantAsCommand(t *testing.T, account string) {
	t.Helper()
	ctx := context.Background()
	db := rootDB(t, r.target)
	state := func() (ro, sro bool, gtid string) {
		if err := db.QueryRow("SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only, @@GLOBAL.gtid_executed").Scan(&ro, &sro, &gtid); err != nil {
			t.Fatal(err)
		}
		return
	}
	ro, sro, gtid := state()
	if !ro || !sro {
		t.Fatalf("target before the grants: read_only=%v super_read_only=%v, want both on", ro, sro)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	stmts, err := mysql.MissingAdminGrants(ctx, conn, account)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(stmts, "GRANT SHOW DATABASES ON *.* TO "+quoteAcct(account)) {
		t.Fatalf("MissingAdminGrants(%s) = %q, want GRANT SHOW DATABASES among them", account, stmts)
	}
	for _, q := range append(append([]string{"SET SESSION sql_log_bin = 0", "SET GLOBAL super_read_only = OFF"}, stmts...),
		"SET GLOBAL super_read_only = ON") {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	if again, err := mysql.MissingAdminGrants(ctx, conn, account); err != nil || len(again) != 0 {
		t.Errorf("MissingAdminGrants after the grants = %q, %v; want none", again, err)
	}
	ro2, sro2, gtid2 := state()
	if !ro2 || !sro2 || gtid2 != gtid {
		t.Fatalf("target after the grants: read_only=%v super_read_only=%v gtid %q (before %q)", ro2, sro2, gtid2, gtid)
	}
}

// quoteAcct turns user@host into 'user'@'host' (plain names only).
func quoteAcct(account string) string {
	i := strings.LastIndex(account, "@")
	return "'" + account[:i] + "'@'" + account[i+1:] + "'"
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
// grants grant-admin gives (SHOW DATABASES among them) make the listing
// complete: the dump runs, and the settle sees the old database (A6).
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

	r.grantAsCommand(t, "rs_partial@localhost")
	if d := r.snap.run(t, "mysql.dumpFrom", dumpParams(r.master, "kecil")); !d.OK {
		t.Fatalf("dumpFrom after grant-admin's grants = %+v", d)
	}
	resp := r.dispatch("mysql.configureReplication", sourceParams(r.master, "kecil"))
	if resp.OK || resp.Error == nil || !strings.Contains(resp.Error.Message, "Database itu: rs_lama.") {
		t.Fatalf("configureReplication = %+v, want A6 (T8b) naming rs_lama", resp)
	}
	r.assertStillQuarantined(t)
}

// TestRebuildRoleOnlyAccountAtSettle: the copy was made with a complete
// account, but the account stored at the settle holds its privileges only
// through a role: the settle is refused with the C-1 text, the role and the
// approval stay. After grant-admin's grants the same settle ends the
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

	r.grantAsCommand(t, "rs_viarole@localhost")
	decode[proto.ReplicaStatus](t, r.dispatch("mysql.configureReplication", sourceParams(r.master, "kecil")))
	if role := pinFile(t, r.dir, mysql.RoleFile); role != "replica" {
		t.Errorf("role pin after grant-admin's grants = %q, want replica", role)
	}
}

// listUnsupported is the listing refusal for an account name the proof
// does not handle, T17.
const listUnsupported = "daftar database di server ini tidak bisa dipastikan lengkap: nama akun MySQL yang dipakai " +
	"agent memuat karakter yang tidak didukung pemeriksaan ini (yang didukung: huruf, angka, garis bawah, titik, " +
	"tanda hubung, dan tanda dolar). Jalankan di server ini: sudo dbpanel agent reset-admin, lalu tekan \"Ulangi " +
	"dari tahap ini\" pada pekerjaan di panel."

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

// TestListingProofSessionSettings (SEC-211, evidence M4): the proof reads
// SHOW GRANTS under its own session settings (sql_quote_show_create = ON,
// an empty sql_mode), so a server whose global sql_quote_show_create is OFF
// still proves an account holding SHOW DATABASES: the settle ends the
// quarantine, and grant-admin's reading (MissingAdminGrants) finds SHOW
// DATABASES held. The session that read them keeps its own settings.
func TestListingProofSessionSettings(t *testing.T) {
	r := newSettleRig(t)
	r.dump("kecil")
	r.onTarget(t, "CREATE USER rs_show@localhost",
		"GRANT SELECT, SHOW DATABASES, RELOAD, PROCESS, REPLICATION CLIENT, SUPER ON *.* TO rs_show@localhost",
		"GRANT SYSTEM_VARIABLES_ADMIN, REPLICATION_SLAVE_ADMIN ON *.* TO rs_show@localhost",
		"SET GLOBAL sql_quote_show_create = OFF", "SET GLOBAL sql_mode = 'ANSI_QUOTES'")

	// grant-admin's reading, on a root session that inherits the globals.
	ctx := context.Background()
	conn, err := rootDB(t, r.target).Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	session := func() (quote bool, mode string) {
		if err := conn.QueryRowContext(ctx, "SELECT @@SESSION.sql_quote_show_create, @@SESSION.sql_mode").Scan(&quote, &mode); err != nil {
			t.Fatal(err)
		}
		return
	}
	if quote, mode := session(); quote || mode != "ANSI_QUOTES" {
		t.Fatalf("root session before: sql_quote_show_create=%v sql_mode=%q, want OFF and ANSI_QUOTES", quote, mode)
	}
	stmts, err := mysql.MissingAdminGrants(ctx, conn, "rs_show@localhost")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range stmts {
		if strings.Contains(s, "SHOW DATABASES") {
			t.Errorf("MissingAdminGrants = %q, want SHOW DATABASES held", stmts)
		}
	}
	if quote, mode := session(); quote || mode != "ANSI_QUOTES" {
		t.Errorf("root session after: sql_quote_show_create=%v sql_mode=%q, want OFF and ANSI_QUOTES kept", quote, mode)
	}

	// The settle, as the stored account.
	r.useAccount("rs_show")
	decode[proto.ReplicaStatus](t, r.dispatch("mysql.configureReplication", sourceParams(r.master, "kecil")))
	if role := pinFile(t, r.dir, mysql.RoleFile); role != "replica" {
		t.Errorf("role pin = %q, want replica", role)
	}
}

// TestListingProofRestoresAfterEndedContext: the caller's context ends
// right after the proof read the grants; the session settings it changed
// are still put back on that connection, which goes on to the pool or to
// grant-admin's GRANTs.
func TestListingProofRestoresAfterEndedContext(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	conn, err := rootDB(t, inst).Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), "SET SESSION sql_quote_show_create = OFF, sql_mode = 'ANSI_QUOTES'"); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer mysql.SetAccountGrantsReadHook(cancel)()
	mysql.MissingAdminGrants(ctx, conn, "root@localhost")
	if ctx.Err() == nil {
		t.Fatal("the hook did not run")
	}
	var quote bool
	var mode string
	if err := conn.QueryRowContext(context.Background(), "SELECT @@SESSION.sql_quote_show_create, @@SESSION.sql_mode").Scan(&quote, &mode); err != nil {
		t.Fatal(err)
	}
	if quote || mode != "ANSI_QUOTES" {
		t.Errorf("session after an ended context: sql_quote_show_create=%v sql_mode=%q, want OFF and ANSI_QUOTES", quote, mode)
	}
}
