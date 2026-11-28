package mysql_test

import (
	"database/sql"
	"errors"
	"strconv"
	"testing"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Accounts of the measurement in TestReadOnlyMixWrites.
const (
	mixApp   = "mix_app"   // INSERT on the table only: no SUPER, CONNECTION_ADMIN or SYSTEM_VARIABLES_ADMIN
	mixAdmin = "mix_admin" // the agent's admin grants (staticGrants, dynamicGrants) plus INSERT on the table
	mixPass  = "Mix-pass-1"
)

// mixInsert runs one INSERT on inst as user over its socket and returns
// "accepted" or the MySQL error number.
func mixInsert(t *testing.T, inst *testmysql.Instance, user string) string {
	t.Helper()
	params := proto.ConnectParams{User: user}
	if user != "root" {
		params.Password = mixPass
	}
	db, err := sql.Open("mysql", mysql.DSN(params, inst.Socket))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.Exec("INSERT INTO mixdb.t (who) VALUES (?)", user)
	var me *gomysql.MySQLError
	switch {
	case err == nil:
		return "accepted"
	case errors.As(err, &me):
		return strconv.Itoa(int(me.Number))
	}
	t.Fatalf("INSERT as %s: %v", user, err)
	return ""
}

// TestReadOnlyMixWrites measures on a real mysqld which accounts can write
// in each of the four read_only/super_read_only combinations, the OFF/ON
// mix reached as Task 15's row jenis2-buka reaches it: by a restart over an
// option file that sets both ON with only read_only=OFF persisted. It also
// records whether a running SET GLOBAL can produce that mix.
//
// Measured on mysqld 8.0.46-0ubuntu0.24.04.4: ON/ON refuses every
// account with 1290 (ER_OPTION_PREVENTS_STATEMENT); ON/OFF refuses the
// application account with 1290 and accepts the admin account and root;
// OFF/OFF and the OFF/ON mix accept every account; SET GLOBAL cannot
// produce the mix (super_read_only=ON forces read_only ON, read_only=OFF
// forces super_read_only OFF).
func TestReadOnlyMixWrites(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	logVersion(t, inst)
	grants := "CREATE USER, RELOAD, PROCESS, SELECT, LOCK TABLES, SHOW VIEW, TRIGGER, EVENT, REPLICATION CLIENT, REPLICATION SLAVE, " +
		"SYSTEM_VARIABLES_ADMIN, REPLICATION_SLAVE_ADMIN, BACKUP_ADMIN, CLONE_ADMIN, PERSIST_RO_VARIABLES_ADMIN, CONNECTION_ADMIN"
	execOn(t, inst,
		"CREATE DATABASE mixdb",
		"CREATE TABLE mixdb.t (id INT AUTO_INCREMENT PRIMARY KEY, who VARCHAR(32))",
		"CREATE USER '"+mixApp+"'@'localhost' IDENTIFIED BY '"+mixPass+"'",
		"GRANT INSERT ON mixdb.t TO '"+mixApp+"'@'localhost'",
		"CREATE USER '"+mixAdmin+"'@'localhost' IDENTIFIED BY '"+mixPass+"'",
		"GRANT "+grants+" ON *.* TO '"+mixAdmin+"'@'localhost' WITH GRANT OPTION",
		"GRANT INSERT ON mixdb.t TO '"+mixAdmin+"'@'localhost'",
	)

	type want struct{ app, admin, root string }
	measure := func(name string, wantFlags runFlags, w want) {
		t.Helper()
		got := readFlags(t, inst)
		if got.running() != wantFlags {
			t.Fatalf("%s: running %s, want %s (%s)", name, got.running(), wantFlags, got)
		}
		app, admin, root := mixInsert(t, inst, mixApp), mixInsert(t, inst, mixAdmin), mixInsert(t, inst, "root")
		t.Logf("| %s | %s | app %s | admin %s | root %s |", name, got, app, admin, root)
		if (want{app, admin, root}) != w {
			t.Errorf("%s: app %s, admin %s, root %s; want app %s, admin %s, root %s", name, app, admin, root, w.app, w.admin, w.root)
		}
	}
	const ok, refused = "accepted", "1290"

	measure("OFF/OFF", bothOff, want{ok, ok, ok})
	execOn(t, inst, "SET GLOBAL read_only = ON")
	measure("ON/OFF", roOnly, want{refused, ok, ok})
	execOn(t, inst, "SET GLOBAL super_read_only = ON")
	measure("ON/ON", bothOn, want{refused, refused, refused})

	// Can a running SET GLOBAL produce read_only OFF with super_read_only ON?
	execOn(t, inst, "SET GLOBAL read_only = OFF")
	if s := logged(t, inst, "SET GLOBAL read_only = OFF from ON/ON"); s.running() != bothOff {
		t.Errorf("SET GLOBAL read_only = OFF from ON/ON gave %s", s.running())
	}
	execOn(t, inst, "SET GLOBAL super_read_only = ON")
	if s := logged(t, inst, "SET GLOBAL super_read_only = ON from OFF/OFF"); s.running() != bothOn {
		t.Errorf("SET GLOBAL super_read_only = ON from OFF/OFF gave %s", s.running())
	}
	execOn(t, inst, "SET GLOBAL read_only = OFF")

	// The mix as jenis2-buka reaches it: an option file setting both ON,
	// only read_only=OFF persisted, then a restart.
	writeOF(t, inst)
	execOn(t, inst, "SET PERSIST read_only = OFF")
	logged(t, inst, "SET PERSIST read_only = OFF")
	restarted(t, inst)
	measure("OFF/ON after restart", runFlags{SRO: true}, want{ok, ok, ok})
}
