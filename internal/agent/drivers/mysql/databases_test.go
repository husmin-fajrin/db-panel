package mysql_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// TestDropDatabases: the named databases go, others stay, nothing reaches
// the binary log, and a read-only replica is read-only again afterwards.
func TestDropDatabases(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	db := rootDB(t, inst)
	execAll(t, db,
		"CREATE DATABASE a", "CREATE TABLE a.t (id INT PRIMARY KEY)",
		"CREATE DATABASE b", "CREATE DATABASE keep", "CREATE TABLE keep.t (id INT PRIMARY KEY)",
		"SET GLOBAL super_read_only = ON",
	)
	before := gtidExecuted(t, db)

	res := decode[proto.DropDatabasesResult](t, dispatch(t, inst.Host(), "mysql.dropDatabases",
		proto.DropDatabasesParams{Databases: []string{"a", "b", "gone"}}))
	if !slices.Equal(res.Dropped, []string{"a", "b"}) {
		t.Fatalf("Dropped = %v, want [a b]", res.Dropped)
	}
	for name, want := range map[string]int{"a": 0, "b": 0, "keep": 1} {
		if n := count(t, db, "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = '"+name+"'"); n != want {
			t.Errorf("database %s: count %d, want %d", name, n, want)
		}
	}
	if got := gtidExecuted(t, db); got != before {
		t.Errorf("gtid_executed %q → %q: the drops were binary-logged", before, got)
	}
	var ro, sro bool
	if err := db.QueryRow("SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only").Scan(&ro, &sro); err != nil {
		t.Fatal(err)
	}
	if !ro || !sro {
		t.Errorf("read_only %v, super_read_only %v after the drop; want both ON", ro, sro)
	}
}

func TestDropDatabasesRefusesBadNames(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	for _, dbs := range [][]string{nil, {"mysql"}, {"sys"}, {"SYS"}, {"MySQL"}, {"Performance_Schema"}, {"a`; DROP"}, {"a", "a"}, {"a", "A"}, {"performance_schema"}} {
		r := dispatch(t, inst.Host(), "mysql.dropDatabases", proto.DropDatabasesParams{Databases: dbs})
		if r.OK || r.Error.Code != proto.CodeBadParams {
			t.Errorf("%q: response = %+v, want bad_params", dbs, r)
		}
	}
}

// TestDropDatabasesRefusesRunningReplica: replication that could write to
// the same databases must be stopped first.
func TestDropDatabasesRefusesRunningReplica(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	db := rootDB(t, inst)
	execAll(t, db,
		"CREATE DATABASE a",
		"CHANGE REPLICATION SOURCE TO SOURCE_HOST = '127.0.0.1', SOURCE_PORT = 1, SOURCE_USER = 'nobody', SOURCE_CONNECT_RETRY = 60",
		"START REPLICA",
	)
	t.Cleanup(func() { db.Exec("STOP REPLICA") })
	r := dispatch(t, inst.Host(), "mysql.dropDatabases", proto.DropDatabasesParams{Databases: []string{"a"}})
	if r.OK || !strings.Contains(r.Error.Message, "replikasi masih berjalan") {
		t.Fatalf("response = %+v", r)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = 'a'"); n != 1 {
		t.Fatal("database dropped while replicating")
	}
}

// TestDropDatabasesRefusesReplicationSource: a drop without binary logging
// on a server replicas read from would never reach them.
func TestDropDatabasesRefusesReplicationSource(t *testing.T) {
	source := testmysql.Start(t, testmysql.Options{})
	replica := testmysql.Start(t, testmysql.Options{})
	sdb := rootDB(t, source)
	execAll(t, sdb, "CREATE DATABASE a")
	replUser(t, source)
	rdb := rootDB(t, replica)
	execAll(t, rdb,
		fmt.Sprintf("CHANGE REPLICATION SOURCE TO SOURCE_HOST = '127.0.0.1', SOURCE_PORT = %d, SOURCE_USER = 'dbpanel_repl', "+
			"SOURCE_PASSWORD = %s, SOURCE_AUTO_POSITION = 1, GET_SOURCE_PUBLIC_KEY = 1", source.Port, sqlLiteral(replPassword)),
		"START REPLICA",
	)
	t.Cleanup(func() { rdb.Exec("STOP REPLICA") })
	deadline := time.Now().Add(time.Minute)
	for count(t, sdb, "SELECT COUNT(*) FROM information_schema.processlist WHERE COMMAND IN ('Binlog Dump GTID', 'Binlog Dump')") == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the replica never connected")
		}
		time.Sleep(100 * time.Millisecond)
	}
	r := dispatch(t, source.Host(), "mysql.dropDatabases", proto.DropDatabasesParams{Databases: []string{"a"}})
	if r.OK || !strings.Contains(r.Error.Message, "replika sedang membaca binlog server ini") {
		t.Fatalf("response = %+v", r)
	}
	if n := count(t, sdb, "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = 'a'"); n != 1 {
		t.Fatal("database dropped on a replication source")
	}
}
