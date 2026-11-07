package mysql_test

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// leftoverRig is an old master, pinned quarantined under its former
// replica (the new master), which no longer replicates from it.
type leftoverRig struct {
	*pinRig
	master, replica *testmysql.Instance
	nm              proto.PeerCred
}

// pembacaPassword is the password of the account the old master reads
// the new master with.
const pembacaPassword = "Pembaca-pw-1"

func newLeftoverRig(t *testing.T) *leftoverRig {
	t.Helper()
	master := testmysql.Start(t, testmysql.Options{})
	replica := testmysql.Start(t, testmysql.Options{})
	mdb := rootDB(t, master)
	execAll(t, mdb,
		"CREATE USER 'pembaca'@'%' IDENTIFIED BY '"+pembacaPassword+"'",
		"CREATE USER 'salin'@'%' IDENTIFIED BY 'Salin-pw-1'", "GRANT REPLICATION SLAVE ON *.* TO 'salin'@'%'",
		"CREATE DATABASE toko", "CREATE TABLE toko.t (id INT PRIMARY KEY, v VARCHAR(20))",
		"INSERT INTO toko.t VALUES (1, 'satu')")
	rdb := rootDB(t, replica)
	execAll(t, rdb,
		"CHANGE REPLICATION SOURCE TO SOURCE_HOST='127.0.0.1', SOURCE_PORT="+strconv.Itoa(master.Port)+", SOURCE_USER='salin', "+
			"SOURCE_PASSWORD='Salin-pw-1', SOURCE_SSL=1, SOURCE_AUTO_POSITION=1",
		"START REPLICA")
	want := gtidExecuted(t, mdb)
	deadline := time.Now().Add(time.Minute)
	for gtidExecuted(t, rdb) != want {
		if time.Now().After(deadline) {
			t.Fatalf("replica never caught up with %s", want)
		}
		time.Sleep(200 * time.Millisecond)
	}
	execAll(t, rdb, "STOP REPLICA")
	src := "127.0.0.1:" + strconv.Itoa(replica.Port)
	r := &leftoverRig{pinRig: newPinRig(t, master.Host(), pinDir(t, "quarantined", src)), master: master, replica: replica,
		nm: proto.PeerCred{Host: "127.0.0.1", Port: replica.Port, User: "pembaca", Password: pembacaPassword}}
	return r
}

func (r *leftoverRig) leftover() proto.LeftoverResult {
	r.t.Helper()
	return decode[proto.LeftoverResult](r.t, r.dispatch("mysql.leftover", proto.LeftoverParams{NewMaster: r.nm}))
}

// TestLeftoverEmptyWhenInSync: an old master whose every transaction the
// new master holds has nothing left over.
func TestLeftoverEmptyWhenInSync(t *testing.T) {
	r := newLeftoverRig(t)
	res := r.leftover()
	if res.Transactions != 0 || res.GTIDSet != "" || len(res.Tables) != 0 || res.From != nil || len(res.Binlogs) != 0 {
		t.Errorf("leftover in sync = %+v, want nothing", res)
	}
}

// TestLeftoverCountsAndTables: three transactions committed on the old
// master after its replica stopped are counted, with the tables they
// touched and their rows, the time span and the binary logs read; a
// rotation in between keeps them in two files.
func TestLeftoverCountsAndTables(t *testing.T) {
	r := newLeftoverRig(t)
	mdb := rootDB(t, r.master)
	before := gtidExecuted(t, mdb)
	execAll(t, mdb, "FLUSH BINARY LOGS")
	start := time.Now().Add(-2 * time.Second)
	execAll(t, mdb,
		"INSERT INTO toko.t VALUES (10, 'rahasia-a'), (11, 'rahasia-b')",
		"FLUSH BINARY LOGS",
		"DELETE FROM toko.t WHERE id = 1",
		"CREATE TABLE toko.baru (id INT PRIMARY KEY)")
	// Another file after them, which holds none of them.
	execAll(t, mdb, "FLUSH BINARY LOGS")
	end := time.Now().Add(2 * time.Second)
	var want string
	if err := mdb.QueryRow("SELECT GTID_SUBTRACT(@@global.gtid_executed, ?)", before).Scan(&want); err != nil {
		t.Fatal(err)
	}

	res := r.leftover()
	if res.Transactions != 3 || res.GTIDSet != want {
		t.Errorf("leftover = %d transactions, set %q; want 3, %q", res.Transactions, res.GTIDSet, want)
	}
	wantTables := []proto.TableTouch{{Schema: "toko", Table: "baru", Rows: 0}, {Schema: "toko", Table: "t", Rows: 3}}
	if !slices.Equal(res.Tables, wantTables) {
		t.Errorf("tables = %+v, want %+v", res.Tables, wantTables)
	}
	if res.From == nil || res.To == nil || res.From.Before(start) || res.To.After(end) || res.To.Before(*res.From) {
		t.Errorf("from %v to %v, want within %v .. %v", res.From, res.To, start, end)
	}
	if b, _ := json.Marshal(res); strings.Contains(string(b), "rahasia") {
		t.Errorf("leftover carries row data: %s", b)
	}
	if len(res.Binlogs) != 2 {
		t.Errorf("binlogs = %v, want the two files holding the transactions", res.Binlogs)
	}
}

// TestLeftoverRefusesUnreachableOrOtherNewMaster: the new master must be
// the pinned source and must answer.
func TestLeftoverRefusesUnreachableOrOtherNewMaster(t *testing.T) {
	r := newLeftoverRig(t)
	other := r.nm
	other.Port = r.master.Port
	wantRefused(t, "leftover under another new master", r.dispatch("mysql.leftover", proto.LeftoverParams{NewMaster: other}),
		"sumber "+peerKeyOf(r.nm))
	execAll(t, rootDB(t, r.replica), "DROP USER 'pembaca'@'%'")
	wantRefused(t, "leftover with the new master refusing", r.dispatch("mysql.leftover", proto.LeftoverParams{NewMaster: r.nm}),
		"tidak bisa dibaca")
}

func peerKeyOf(c proto.PeerCred) string { return c.Host + ":" + strconv.Itoa(c.Port) }

// TestLeftoverScanStatementsAndTimes: the parser counts only the
// transactions of the set, takes statement-based DDL and DML as rows 0
// under the current database, reads space-padded hours and backquoted
// names.
func TestLeftoverScanStatementsAndTimes(t *testing.T) {
	const u = "3e11fa47-71ca-11e1-9e33-c80aa9429562"
	text := strings.Join([]string{
		"# at 4",
		"#261107  9:05:01 server id 1  end_log_pos 125 CRC32 0x1 	Start: binlog v 4",
		"#261107  9:05:02 server id 1  end_log_pos 200 CRC32 0x2 	GTID	last_committed=0",
		"SET @@SESSION.GTID_NEXT= '" + u + ":5'/*!*/;",
		"use `to``ko`/*!*/;",
		"CREATE TABLE IF NOT EXISTS `baru` (id INT PRIMARY KEY)",
		"/*!*/;",
		"#261107 10:15:00 server id 1  end_log_pos 300 CRC32 0x3 	GTID	last_committed=1",
		"SET @@SESSION.GTID_NEXT= '" + u + ":6'/*!*/;",
		"BEGIN",
		"INSERT INTO gudang.stok VALUES (1, 'data')",
		"UPDATE `t` SET v = 2",
		"### DELETE FROM `to``ko`.`t`",
		"###   @1=7",
		"COMMIT/*!*/;",
		"#261107 11:00:00 server id 1  end_log_pos 400 CRC32 0x4 	GTID	last_committed=2",
		"SET @@SESSION.GTID_NEXT= '" + u + ":9'/*!*/;",
		"### INSERT INTO `lain`.`x`",
		"DROP TABLE lain.y",
		"SET @@SESSION.GTID_NEXT= 'AUTOMATIC' /* added by mysqlbinlog */ /*!*/;",
	}, "\n")
	res, err := mysql.ScanLeftover(u+":5-6", text)
	if err != nil {
		t.Fatal(err)
	}
	want := []proto.TableTouch{{Schema: "gudang", Table: "stok"}, {Schema: "to`ko", Table: "baru"}, {Schema: "to`ko", Table: "t", Rows: 1}}
	if res.Transactions != 2 || !slices.Equal(res.Tables, want) {
		t.Errorf("scan = %d transactions, %+v; want 2, %+v", res.Transactions, res.Tables, want)
	}
	from := time.Date(2026, 11, 7, 9, 5, 2, 0, time.Local)
	to := time.Date(2026, 11, 7, 10, 15, 0, 0, time.Local)
	if res.From == nil || !res.From.Equal(from) || !res.To.Equal(to) {
		t.Errorf("from %v to %v, want %v .. %v", res.From, res.To, from, to)
	}
}
