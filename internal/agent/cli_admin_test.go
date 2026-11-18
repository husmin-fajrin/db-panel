package agent

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The operator texts of grant-admin and reset-admin, as spec 5.8 words them.
const (
	c16 = "agent belum punya akun MySQL tersimpan; jalankan sudo dbpanel agent reset-admin"
	c17 = "akun MySQL yang disimpan agent tidak bisa masuk; jalankan sudo dbpanel agent reset-admin"
	c18 = "Akun %s sudah punya semua hak yang dibutuhkan."
	c19 = "Akun dbpanel_admin@localhost dibuat ulang dan disimpan sebagai akun agent. Di panel, klik Segarkan di halaman server ini."
)

// newAdminRig is a dropRig whose agent keeps its MySQL account in its
// directory (the real sealed store) and logs in to the rig's MySQL with it.
func newAdminRig(t *testing.T) *dropRig {
	t.Helper()
	r := newDropRig(t)
	setVar(t, &cliStoredHost, func(_ config.Config, creds mysql.CredentialStore) (mysql.Host, error) {
		return r.inst.HostWith(creds), nil
	})
	return r
}

// creds is the agent's credential store in the rig's directory.
func (r *dropRig) creds() mysql.CredentialStore {
	r.t.Helper()
	c, err := cliAgentCreds(r.dir)
	if err != nil {
		r.t.Fatal(err)
	}
	return c
}

// store makes p the agent's stored account.
func (r *dropRig) store(p proto.ConnectParams) {
	r.t.Helper()
	if err := r.creds().Save(p); err != nil {
		r.t.Fatal(err)
	}
}

// stored is the agent's stored account.
func (r *dropRig) stored() proto.ConnectParams {
	r.t.Helper()
	p, _, err := r.creds().Load()
	if err != nil {
		r.t.Fatal(err)
	}
	return p
}

// account creates user@host (quoted) with password pw, every privilege
// with GRANT OPTION, less revoke (a list of privileges, revoked on *.*).
// partial_revokes is turned on, so that an account without SHOW DATABASES
// cannot prove its listing complete through its global SELECT.
func (r *dropRig) account(acct, pw, revoke string) {
	r.t.Helper()
	stmts := []string{"SET GLOBAL partial_revokes = ON", "CREATE USER " + acct + " IDENTIFIED BY '" + pw + "'", "GRANT ALL ON *.* TO " + acct + " WITH GRANT OPTION"}
	if revoke != "" {
		stmts = append(stmts, "REVOKE "+revoke+" ON *.* FROM "+acct)
	}
	r.setup(stmts...)
}

// command runs agent command cmd with input in.
func (r *dropRig) command(cmd, in string, args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = RunCLIWith(append(append([]string{cmd}, args...), "--dir", r.dir), strings.NewReader(in), &out, &errOut, CLISeams{Syslog: r.syslog})
	return code, out.String(), errOut.String()
}

// commandHook is command whose hook gets stdout at the first read of the
// input.
func (r *dropRig) commandHook(cmd, in string, hook func(string), args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	input := &hookReader{r: strings.NewReader(in), f: func() { hook(out.String()) }}
	code = RunCLIWith(append(append([]string{cmd}, args...), "--dir", r.dir), input, &out, &errOut, CLISeams{Syslog: r.syslog})
	return code, out.String(), errOut.String()
}

// missing is what grant-admin would give account (user@host) now.
func (r *dropRig) missing(account string) []string {
	r.t.Helper()
	ctx := context.Background()
	conn, err := r.db().Conn(ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	defer conn.Close()
	m, err := mysql.MissingAdminGrants(ctx, conn, account)
	if err != nil {
		r.t.Fatal(err)
	}
	return m
}

// driver dispatches agent commands to a driver on the rig: its pins, its
// MySQL, and the agent's stored account.
func (r *dropRig) driver(cmd string, params any) proto.Response {
	r.t.Helper()
	creds := r.creds()
	d := &mysql.Driver{PinDir: r.dir, Host: r.inst.HostWith(creds), Creds: creds}
	reg := commands.NewRegistry()
	d.Register(reg)
	raw, err := json.Marshal(params)
	if err != nil {
		r.t.Fatal(err)
	}
	return reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: cmd, Params: raw})
}

// binlogPos is the server's binary log file and position.
func (r *dropRig) binlogPos() string {
	r.t.Helper()
	rows, err := r.db().Query("SHOW MASTER STATUS")
	if err != nil {
		r.t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		r.t.Fatal(err)
	}
	if !rows.Next() {
		return "none"
	}
	vals := make([]sql.NullString, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		r.t.Fatal(err)
	}
	return vals[0].String + ":" + vals[1].String
}

// lockAccounts holds LOCK TABLES mysql.user READ, which makes every account
// statement wait (measured: GRANT and CREATE USER wait for it; an open
// transaction that read the table does not hold them), until release.
func (r *dropRig) lockAccounts() (release func()) {
	r.t.Helper()
	ctx := context.Background()
	conn, err := r.db().Conn(ctx)
	if err != nil {
		r.t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, "LOCK TABLES mysql.user READ"); err != nil {
		r.t.Fatal(err)
	}
	var once sync.Once
	release = func() {
		once.Do(func() {
			conn.ExecContext(ctx, "UNLOCK TABLES")
			conn.Close()
		})
	}
	r.t.Cleanup(release)
	return release
}

// TestGrantAdminAddsMissing: an account without SHOW DATABASES and without
// one dynamic privilege gets both; afterwards mysql.connect stores it on
// the quarantined server, its listing proof holds (nothing missing), the
// GTID set is unchanged, the flags are 1 1, and a system log line names
// the account and the statements.
func TestGrantAdminAddsMissing(t *testing.T) {
	r := newAdminRig(t)
	r.account("'agen'@'localhost'", "Agen-123", "SHOW DATABASES, BACKUP_ADMIN")
	r.store(proto.ConnectParams{User: "agen", Password: "Agen-123"})
	gtid, pos := r.gtidExecuted(), r.binlogPos()
	weak := proto.ConnectParams{User: "agen", Password: "Agen-123"}
	if resp := r.driver("mysql.connect", weak); resp.OK {
		t.Fatal("mysql.connect stored an account lacking BACKUP_ADMIN")
	}

	code, out, errOut := r.command("grant-admin", "ya\n")
	if code != 0 {
		t.Fatalf("grant-admin = %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	for _, want := range []string{
		"GRANT BACKUP_ADMIN ON *.* TO 'agen'@'localhost' WITH GRANT OPTION\n",
		"GRANT SHOW DATABASES ON *.* TO 'agen'@'localhost'\n",
		fmt.Sprintf(textGrantDone, "agen@localhost") + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout lacks %q:\n%s", want, out)
		}
	}
	if m := r.missing("agen@localhost"); len(m) != 0 {
		t.Errorf("still missing %q", m)
	}
	if resp := r.driver("mysql.connect", weak); !resp.OK {
		t.Errorf("mysql.connect after grant-admin = %+v", resp.Error)
	}
	if f, g, p := r.flags(), r.gtidExecuted(), r.binlogPos(); f != "1 1" || g != gtid || p != pos {
		t.Errorf("flags %s, gtid %q (was %q), binlog %s (was %s)", f, g, gtid, p, pos)
	}
	lines := r.syslogLines()
	if len(lines) != 1 || !strings.Contains(lines[0], `account="agen@localhost"`) || !strings.Contains(lines[0], "SHOW DATABASES") ||
		!strings.Contains(lines[0], "outcome=done") {
		t.Errorf("system log %q", lines)
	}
}

// TestGrantAdminCoversReplicaPreflight: an account holding what
// mysql.connect checks but no data privileges: afterwards the replica
// preflight's grants check on the quarantined server is OK.
func TestGrantAdminCoversReplicaPreflight(t *testing.T) {
	r := newAdminRig(t)
	r.account("'agen'@'localhost'", "Agen-123", "DROP, CREATE, INSERT, UPDATE, DELETE, ALTER, INDEX, CREATE VIEW, "+
		"CREATE ROUTINE, ALTER ROUTINE, REFERENCES, CREATE TEMPORARY TABLES")
	r.store(proto.ConnectParams{User: "agen", Password: "Agen-123"})
	if resp := r.driver("mysql.connect", proto.ConnectParams{User: "agen", Password: "Agen-123"}); !resp.OK {
		t.Fatalf("mysql.connect with the master privileges = %+v", resp.Error)
	}
	var version string
	if err := r.db().QueryRow("SELECT @@version").Scan(&version); err != nil {
		t.Fatal(err)
	}
	p := proto.PreflightParams{Role: "replica", PeerHost: "127.0.0.1", PeerPort: r.inst.Port, PeerVersion: version,
		PeerServerUUID: "3e11fa47-71ca-11e1-9e33-c80aa9429562"}
	grants := func() proto.Check {
		t.Helper()
		resp := r.driver("mysql.preflight", p)
		var res proto.PreflightResult
		if !resp.OK || json.Unmarshal(resp.Result, &res) != nil {
			t.Fatalf("mysql.preflight = %+v", resp.Error)
		}
		for _, c := range res.Checks {
			if c.ID == "grants" {
				return c
			}
		}
		t.Fatal("no grants check")
		return proto.Check{}
	}
	if c := grants(); c.Status != proto.CheckFail {
		t.Fatalf("grants check before = %+v, want fail", c)
	}
	if code, out, errOut := r.command("grant-admin", "ya\n"); code != 0 {
		t.Fatalf("grant-admin = %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	if c := grants(); c.Status != proto.CheckOK {
		t.Errorf("grants check after grant-admin = %+v, want ok", c)
	}
}

// TestGrantAdminNamesAccountAsMySQLKnowsIt: the stored account logs in as
// 127.0.0.1 but MySQL knows it as 'x'@'%': the statements shown and run
// name 'x'@'%'.
func TestGrantAdminNamesAccountAsMySQLKnowsIt(t *testing.T) {
	r := newAdminRig(t)
	r.account("'x'@'%'", "Iks-1234", "SHOW DATABASES")
	r.store(proto.ConnectParams{User: "x", Password: "Iks-1234", Host: "127.0.0.1", Port: r.inst.Port})
	r.exec("SET GLOBAL log_output = 'TABLE'")
	r.setup("TRUNCATE mysql.general_log")
	r.exec("SET GLOBAL general_log = ON")
	code, out, errOut := r.command("grant-admin", "ya\n")
	r.exec("SET GLOBAL general_log = OFF")
	if code != 0 || !strings.Contains(out, "  GRANT SHOW DATABASES ON *.* TO 'x'@'%'\n") {
		t.Fatalf("grant-admin = %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	var n int
	if err := r.db().QueryRow("SELECT COUNT(*) FROM mysql.general_log WHERE CONVERT(argument USING utf8mb4) = " +
		`"GRANT SHOW DATABASES ON *.* TO 'x'@'%'"`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("the GRANT to 'x'@'%%' ran %d times, want once", n)
	}
	if m := r.missing("x@%"); len(m) != 0 {
		t.Errorf("still missing %q", m)
	}
}

// TestGrantAdminStoredLoginRefused: a stored account that cannot log in is
// C17, without a stored account C16; nothing changes.
func TestGrantAdminStoredLoginRefused(t *testing.T) {
	r := newAdminRig(t)
	if code, _, errOut := r.command("grant-admin", "ya\n"); code != 1 || errOut != c16+"\n" {
		t.Errorf("no stored account: %d %q, want C16", code, errOut)
	}
	r.account("'agen'@'localhost'", "Agen-123", "SHOW DATABASES")
	r.store(proto.ConnectParams{User: "agen", Password: "salah"})
	if code, out, errOut := r.command("grant-admin", "ya\n"); code != 1 || errOut != c17+"\n" || out != "" {
		t.Errorf("refused login: %d, stdout %q, stderr %q; want C17", code, out, errOut)
	}
	if m := r.missing("agen@localhost"); len(m) != 1 || r.flags() != "1 1" || len(r.syslogLines()) != 0 {
		t.Errorf("missing %q, flags %s, system log %q", m, r.flags(), r.syslogLines())
	}
}

// TestGrantAdminNothingMissing: C18 and exit 0, nothing sent or logged.
func TestGrantAdminNothingMissing(t *testing.T) {
	r := newAdminRig(t)
	r.store(proto.ConnectParams{User: "root"})
	code, out, errOut := r.command("grant-admin", "")
	if code != 0 || out != fmt.Sprintf(c18, "root@localhost")+"\n" || errOut != "" {
		t.Errorf("grant-admin = %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	if len(r.syslogLines()) != 0 {
		t.Errorf("system log %q", r.syslogLines())
	}
}

// TestGrantAdminFailingGrantRelocks: the GRANT fails (the administrator
// account given with --pengguna cannot pass the privilege on): the flags
// are 1 1 again, the lock is free, and the command says the grant failed.
func TestGrantAdminFailingGrantRelocks(t *testing.T) {
	r := newAdminRig(t)
	r.account("'agen'@'localhost'", "Agen-123", "SHOW DATABASES")
	r.store(proto.ConnectParams{User: "agen", Password: "Agen-123"})
	r.setup("CREATE USER 'op'@'localhost' IDENTIFIED BY 'Op-12345'",
		"GRANT SELECT, PROCESS ON *.* TO 'op'@'localhost'", "GRANT SYSTEM_VARIABLES_ADMIN, CONNECTION_ADMIN ON *.* TO 'op'@'localhost'")
	code, out, errOut := r.command("grant-admin", "Op-12345\nya\n", "--pengguna", "op")
	if code != 1 || !strings.HasPrefix(errOut, "Hak akun agen@localhost tidak bisa diberikan (") {
		t.Errorf("grant-admin = %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	if f := r.flags(); f != "1 1" || !r.lockFree() {
		t.Errorf("flags %s, lock free %v", f, r.lockFree())
	}
	if m := r.missing("agen@localhost"); len(m) != 1 || len(r.syslogLines()) != 0 {
		t.Errorf("missing %q, system log %q (nothing changed: no line)", m, r.syslogLines())
	}
}

// TestResetAdminStoresAccount: dbpanel_admin@localhost is made and stored
// as the agent's account and logs in; the account stored before is not
// dropped; C19; the password is in no output, log line or plain file; the
// GTID set and the binary log are unchanged and the flags 1 1.
func TestResetAdminStoresAccount(t *testing.T) {
	r := newAdminRig(t)
	r.account("'agen'@'localhost'", "Agen-123", "SHOW DATABASES")
	r.store(proto.ConnectParams{User: "agen", Password: "Agen-123"})
	gtid, pos := r.gtidExecuted(), r.binlogPos()
	code, out, errOut := r.command("reset-admin", "ya\n")
	if code != 0 || !strings.HasSuffix(out, textConfirmYa+c19+"\n") || errOut != "" {
		t.Fatalf("reset-admin = %d\nstdout %q\nstderr %q", code, out, errOut)
	}
	p := r.stored()
	if p.User != "dbpanel_admin" || p.Host != "localhost" || len(p.Password) < 16 {
		t.Fatalf("stored account %q@%q (password %d characters)", p.User, p.Host, len(p.Password))
	}
	name, err := mysql.StoredAccountName(context.Background(), r.inst.HostWith(r.creds()))
	if err != nil || name != "dbpanel_admin@localhost" {
		t.Errorf("StoredAccountName = %q, %v", name, err)
	}
	if m := r.missing("dbpanel_admin@localhost"); len(m) != 0 {
		t.Errorf("dbpanel_admin lacks %q", m)
	}
	var n int
	if err := r.db().QueryRow("SELECT COUNT(*) FROM mysql.user WHERE user = 'agen'").Scan(&n); err != nil || n != 1 {
		t.Errorf("the earlier account: %d rows, %v; want it kept", n, err)
	}
	if f, g, bp := r.flags(), r.gtidExecuted(), r.binlogPos(); f != "1 1" || g != gtid || bp != pos {
		t.Errorf("flags %s, gtid %q (was %q), binlog %s (was %s)", f, g, gtid, bp, pos)
	}
	lines := r.syslogLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "admin account reset") || !strings.Contains(lines[0], "outcome=done") {
		t.Errorf("system log %q", lines)
	}
	for what, s := range map[string]string{"stdout": out, "system log": strings.Join(lines, "\n")} {
		if strings.Contains(s, p.Password) {
			t.Errorf("the password is in the %s", what)
		}
	}
	scanForSecret(t, p.Password, r.dir, r.inst.Dir)

	// Again: a new password, stored, and it logs in.
	if code, _, errOut := r.command("reset-admin", "ya\n"); code != 0 || r.stored().Password == p.Password {
		t.Errorf("second reset-admin = %d, %q; password changed %v", code, errOut, r.stored().Password != p.Password)
	}
	if name, err := mysql.StoredAccountName(context.Background(), r.inst.HostWith(r.creds())); err != nil || name != "dbpanel_admin@localhost" {
		t.Errorf("after the second run: %q, %v", name, err)
	}
}

// TestAdminCommandsRefusals: not quarantined (C1), the lift lock held (C3),
// the flags not 1 1 with the lock free (C2), for both commands; nothing is
// sent.
func TestAdminCommandsRefusals(t *testing.T) {
	r := newAdminRig(t)
	r.account("'agen'@'localhost'", "Agen-123", "SHOW DATABASES")
	r.store(proto.ConnectParams{User: "agen", Password: "Agen-123"})
	holdLock := func() func() {
		release, err := mysql.AcquireLiftLock(r.lock)
		if err != nil {
			t.Fatal(err)
		}
		return release
	}
	var held func()
	for _, cmd := range []string{"grant-admin", "reset-admin"} {
		for _, c := range []struct {
			name, want string
			prep, undo func()
		}{
			{"not quarantined", fmt.Sprintf(c1, "master"),
				func() { mysqlLoad(t, r.dir).SetRole("master") }, func() { mysqlLoad(t, r.dir).SetRole("quarantined") }},
			{"lift lock held", mysql.ErrLiftBusy.Error(), func() { held = holdLock() }, func() { held() }},
			{"flags off", c2,
				func() { r.exec("SET GLOBAL super_read_only = OFF") }, func() { r.exec("SET GLOBAL super_read_only = ON") }},
		} {
			t.Run(cmd+" "+c.name, func(t *testing.T) {
				c.prep()
				code, out, errOut := r.command(cmd, "ya\n")
				c.undo()
				if code != 1 || errOut != c.want+"\n" {
					t.Errorf("%s = %d\nstderr %q\nwant   %q\nstdout %q", cmd, code, errOut, c.want+"\n", out)
				}
			})
		}
	}
	if m := r.missing("agen@localhost"); len(m) != 1 || r.stored().User != "agen" || len(r.syslogLines()) != 0 {
		t.Errorf("missing %q, stored %q, system log %q", m, r.stored().User, r.syslogLines())
	}
	if code, _, _ := r.command("grant-admin", "", "lebih"); code != 2 {
		t.Errorf("an extra argument: code %d, want 2", code)
	}
}

// TestAdminCommandsRecheckAfterConfirmation: the role or the flags change
// while the question waits: C11, and SET GLOBAL super_read_only = OFF is
// never sent.
func TestAdminCommandsRecheckAfterConfirmation(t *testing.T) {
	r := newAdminRig(t)
	r.account("'agen'@'localhost'", "Agen-123", "SHOW DATABASES")
	r.store(proto.ConnectParams{User: "agen", Password: "Agen-123"})
	r.exec("SET GLOBAL log_output = 'TABLE'")
	for _, cmd := range []string{"grant-admin", "reset-admin"} {
		for name, c := range map[string]struct{ change, undo func() }{
			"role": {
				func() { mysqlLoad(t, r.dir).SetRole("master") },
				func() { mysqlLoad(t, r.dir).SetRole("quarantined") },
			},
			"flags": {
				func() { r.exec("SET GLOBAL read_only = OFF") },
				func() { r.exec("SET GLOBAL super_read_only = ON") },
			},
		} {
			t.Run(cmd+" "+name, func(t *testing.T) {
				r.setup("TRUNCATE mysql.general_log")
				r.exec("SET GLOBAL general_log = ON")
				code, out, errOut := r.commandHook(cmd, "ya\n", func(string) { c.change() })
				r.exec("SET GLOBAL general_log = OFF")
				c.undo()
				if code != 1 || errOut != c11+"\n" {
					t.Errorf("%s = %d\nstderr %q\nstdout %q", cmd, code, errOut, out)
				}
				var lifted int
				if err := r.db().QueryRow("SELECT COUNT(*) FROM mysql.general_log WHERE CONVERT(argument USING utf8mb4) LIKE '%super_read_only = OFF%'").Scan(&lifted); err != nil {
					t.Fatal(err)
				}
				if lifted != 0 || r.flags() != "1 1" || !r.lockFree() {
					t.Errorf("lifted %d times, flags %s, lock free %v", lifted, r.flags(), r.lockFree())
				}
			})
		}
	}
	if m := r.missing("agen@localhost"); len(m) != 1 || r.stored().User != "agen" || len(r.syslogLines()) != 0 {
		t.Errorf("missing %q, stored %q, system log %q", m, r.stored().User, r.syslogLines())
	}
}

// TestAdminCommandsKeepSqlLogBinOff: the session's pool has more than one
// connection, yet every statement that changes an account runs on the
// connection that turned sql_log_bin off; the GTID set and the binary log
// do not move.
func TestAdminCommandsKeepSqlLogBinOff(t *testing.T) {
	r := newAdminRig(t)
	r.account("'agen'@'localhost'", "Agen-123", "SHOW DATABASES, BACKUP_ADMIN")
	r.store(proto.ConnectParams{User: "agen", Password: "Agen-123"})
	if s := r.session(); s.DB.Stats().MaxOpenConnections < 2 {
		t.Fatalf("the session's pool holds %d connections; the test needs more than one", s.DB.Stats().MaxOpenConnections)
	} else {
		s.Close()
	}
	r.exec("SET GLOBAL log_output = 'TABLE'")
	for _, cmd := range []string{"grant-admin", "reset-admin"} {
		t.Run(cmd, func(t *testing.T) {
			r.setup("TRUNCATE mysql.general_log")
			r.exec("SET GLOBAL general_log = ON")
			gtid, pos := r.gtidExecuted(), r.binlogPos()
			code, out, errOut := r.command(cmd, "ya\n")
			if g, p := r.gtidExecuted(), r.binlogPos(); g != gtid || p != pos {
				t.Errorf("gtid %q (was %q), binlog %s (was %s)", g, gtid, p, pos)
			}
			r.exec("SET GLOBAL general_log = OFF")
			if code != 0 {
				t.Fatalf("%s = %d\nstdout %q\nstderr %q", cmd, code, out, errOut)
			}
			rows, err := r.db().Query("SELECT thread_id, CONVERT(argument USING utf8mb4) FROM mysql.general_log WHERE command_type = 'Query'")
			if err != nil {
				t.Fatal(err)
			}
			defer rows.Close()
			var off int64 = -1
			var changes []int64
			for rows.Next() {
				var id int64
				var q string
				if err := rows.Scan(&id, &q); err != nil {
					t.Fatal(err)
				}
				switch {
				case q == "SET SESSION sql_log_bin = 0":
					off = id
				case strings.HasPrefix(q, "GRANT ") || strings.HasPrefix(q, "CREATE USER") || strings.HasPrefix(q, "ALTER USER"):
					changes = append(changes, id)
				}
			}
			if off < 0 || len(changes) == 0 || slices.ContainsFunc(changes, func(id int64) bool { return id != off }) {
				t.Errorf("sql_log_bin off on thread %d; account statements on %v", off, changes)
			}
		})
	}
}

// TestAdminCommandInterruptedRelocksItself: SIGINT while the command's
// statement waits for a lock: the command itself locks again (C13), exits
// non-zero, frees the lift lock, and says what the server shows; for
// reset-admin the stored account stays.
func TestAdminCommandInterruptedRelocksItself(t *testing.T) {
	r := newAdminRig(t)
	r.account("'agen'@'localhost'", "Agen-123", "SHOW DATABASES")
	r.store(proto.ConnectParams{User: "agen", Password: "Agen-123"})
	for _, c := range []struct{ cmd, statement, after string }{
		{"grant-admin", "GRANT SHOW DATABASES", ""},
		{"reset-admin", "CREATE USER", textResetNotStored},
	} {
		t.Run(c.cmd, func(t *testing.T) {
			release := r.lockAccounts()
			p := r.startCommand(helperSpec{}, c.cmd)
			p.waitOut(textConfirmYa)
			p.send("ya")
			waitFor(t, func() bool {
				var n int
				if err := r.db().QueryRow("SELECT COUNT(*) FROM information_schema.processlist WHERE info LIKE ? AND state LIKE 'Waiting for%'",
					c.statement+"%").Scan(&n); err != nil {
					t.Fatal(err)
				}
				return n > 0 && r.flags() == "1 0"
			}, func() string { return c.statement + " never waited with super_read_only off; flags " + r.flags() })
			start := time.Now()
			if err := p.cmd.Process.Signal(syscall.SIGINT); err != nil {
				t.Fatal(err)
			}
			code, signaled := p.wait()
			release()
			if d := time.Since(start); d > 15*time.Second {
				t.Errorf("the command took %v to end after SIGINT", d)
			}
			if code == 0 || signaled != 0 || !strings.Contains(p.err.String(), c13+"\n"+c.after) {
				t.Errorf("code %d, signal %v, stderr %q", code, signaled, p.err.String())
			}
			if f := r.flags(); f != "1 1" || !r.lockFree() {
				t.Errorf("flags %s, lock free %v", f, r.lockFree())
			}
			if m := r.missing("agen@localhost"); len(m) != 1 || r.stored().User != "agen" {
				t.Errorf("missing %q, stored %q", m, r.stored().User)
			}
		})
	}
}
