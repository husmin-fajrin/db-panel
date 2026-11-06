package mysql_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

const (
	uuidA = "3e11fa47-71ca-11e1-9e33-c80aa9429562"
	uuidB = "4f22ab58-82db-22f2-af44-d91bb0530673"
)

func TestGTIDSetNormalize(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"", ""},
		{uuidA + ":1-5", uuidA + ":1-5"},
		{uuidA + ":1-3:4-5:7", uuidA + ":1-5:7"},
		{uuidA + ":5:1-2:3", uuidA + ":1-3:5"},
		{uuidB + ":1,\n" + strings.ToUpper(uuidA) + ":2-4:1", uuidA + ":1-4," + uuidB + ":1"},
		{uuidA + ":1-3,\n" + uuidA + ":2-9", uuidA + ":1-9"},
		{uuidA + ":1-2:blue:1-3:4", uuidA + ":1-2:blue:1-4"},
		{uuidA + ":red:1,\n" + uuidA + ":blue:2:3", uuidA + ":blue:2-3:red:1"},
	} {
		got, err := mysql.NormalizeGTIDSet(c.in)
		if err != nil || got != c.want {
			t.Errorf("normalize(%q) = %q, %v; want %q", c.in, got, err, c.want)
		}
	}
	for _, bad := range []string{"x:1", uuidA + ":0", uuidA + ":5-3", uuidA + ":1-x", "zzzzzzzz-71ca-11e1-9e33-c80aa9429562:1"} {
		if got, err := mysql.NormalizeGTIDSet(bad); err == nil {
			t.Errorf("normalize(%q) = %q, want an error", bad, got)
		}
	}
	if n, err := mysql.GTIDCount(uuidA + ":1-5:7," + uuidB + ":tag:10-11"); err != nil || n != 8 {
		t.Errorf("count = %d, %v; want 8", n, err)
	}
}

func TestParseBinlogText(t *testing.T) {
	long := "BINLOG '\n" + strings.Repeat("A", 200<<10) + "\n'/*!*/;\n"
	text := `# The proper term is pseudo_replica_mode, but we use this compatibility alias
/*!50530 SET @@SESSION.PSEUDO_SLAVE_MODE=1*/;
# at 4
#251106 10:00:00 server id 1001  end_log_pos 126 CRC32 0x1a2b3c4d 	Start: binlog v 4, server v 8.0.46 created 251106 10:00:00
# at 126
#251106 10:00:00 server id 1001  end_log_pos 197 CRC32 0x5e6f7a8b 	Previous-GTIDs
# ` + uuidA + `:1-5,
` + uuidB + `:1-2
# at 197
#251106 10:00:01 server id 1001  end_log_pos 276 CRC32 0x00000000 	GTID	last_committed=0	sequence_number=1	rbr_only=yes
SET @@SESSION.GTID_NEXT= '` + uuidA + `:6'/*!*/;
` + long + `# at 400
SET @@SESSION.GTID_NEXT= '` + uuidA + `:7'/*!*/;
SET @@SESSION.GTID_NEXT= '` + uuidB + `:blue:1'/*!*/;
SET @@SESSION.GTID_NEXT= 'AUTOMATIC' /* added by mysqlbinlog */ /*!*/;
DELIMITER ;
# End of log file
`
	got, err := mysql.ParseBinlogText(text)
	if want := uuidA + ":1-7," + uuidB + ":1-2:blue:1"; err != nil || got != want {
		t.Errorf("parse = %q, %v; want %q", got, err, want)
	}
	empty := "#251106 10:00:00 server id 1  end_log_pos 155 CRC32 0x1 	Previous-GTIDs\n# [empty]\n# at 155\n" +
		"SET @@SESSION.GTID_NEXT= '" + uuidA + ":1'/*!*/;\n"
	if got, err := mysql.ParseBinlogText(empty); err != nil || got != uuidA+":1" {
		t.Errorf("parse with empty Previous-GTIDs = %q, %v", got, err)
	}
	if got, err := mysql.ParseBinlogText("#x 	Previous-GTIDs\n# nonsense:1\n"); err == nil {
		t.Errorf("parse of a bad Previous-GTIDs = %q, want an error", got)
	}
}

func TestBinlogIndexPath(t *testing.T) {
	dir := t.TempDir()
	data := filepath.Join(dir, "data")
	if err := os.Mkdir(data, 0o755); err != nil {
		t.Fatal(err)
	}
	cnf := filepath.Join(dir, "my.cnf")
	write := func(path, content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	l := mysql.Layout{ConfigFiles: []string{cnf}, DataDir: data}
	check := func(what, want string) {
		t.Helper()
		got, err := mysql.BinlogIndexPath(l)
		if err != nil || got != want {
			t.Errorf("%s: index = %q, %v; want %q", what, got, err, want)
		}
	}
	write(cnf, "[mysqld]\nlog-bin=mysql-bin\n")
	check("relative log-bin", filepath.Join(data, "mysql-bin.index"))
	write(cnf, "[mysqld]\nlog_bin = /var/log/mysql/mysql-bin.log\n")
	check("absolute log-bin with an extension", "/var/log/mysql/mysql-bin.index")
	write(cnf, "[mysqld]\nlog-bin=x\nlog-bin-index=/srv/idx/x.index\n")
	check("log-bin-index", "/srv/idx/x.index")

	write(cnf, "[mysqld]\nlog-bin\n")
	write(filepath.Join(data, "host-relay-bin.index"), "")
	write(filepath.Join(data, "host-bin.index"), "")
	check("the only non-relay index", filepath.Join(data, "host-bin.index"))
	write(filepath.Join(data, "binlog.index"), "")
	check("the default name", filepath.Join(data, "binlog.index"))

	write(cnf, "[mysqld]\nskip-log-bin\n")
	if got, err := mysql.BinlogIndexPath(l); err == nil {
		t.Errorf("skip-log-bin: index = %q, want an error", got)
	}
}

// TestBinlogTailWithMySQLDown: with MySQL up binlogTail answers
// gtid_executed; once it is stopped, the same set read from the binary
// log files with mysqlbinlog.
func TestBinlogTailWithMySQLDown(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	db := rootDB(t, inst)
	execAll(t, db, "CREATE DATABASE toko", "CREATE TABLE toko.t (id INT PRIMARY KEY, v MEDIUMTEXT)",
		"INSERT INTO toko.t VALUES (1, REPEAT('x', 100000))", "FLUSH BINARY LOGS",
		"INSERT INTO toko.t VALUES (2, 'b')", "INSERT INTO toko.t VALUES (3, 'c')")
	// A transaction of another server, so that Previous-GTIDs holds two
	// UUIDs (printed on two lines).
	conn, err := db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"SET gtid_next = '" + uuidB + ":7'", "BEGIN", "COMMIT", "SET gtid_next = 'AUTOMATIC'", "FLUSH BINARY LOGS"} {
		if _, err := conn.ExecContext(context.Background(), q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	conn.Close()
	execAll(t, db, "INSERT INTO toko.t VALUES (4, 'd')")
	want := gtidExecuted(t, db)
	if !strings.Contains(want, uuidB) {
		t.Fatalf("gtid_executed %q lacks %s", want, uuidB)
	}

	up := decode[proto.BinlogTailResult](t, dispatch(t, inst.Host(), "mysql.binlogTail", nil))
	if !up.MySQLRunning || up.GTIDExecuted != want || !strings.HasPrefix(filepath.Base(up.File), "binlog.") {
		t.Fatalf("binlogTail with MySQL up = %+v; want %q", up, want)
	}

	inst.Stop(t)
	down := decode[proto.BinlogTailResult](t, dispatch(t, inst.Host(), "mysql.binlogTail", nil))
	if down.MySQLRunning || down.GTIDExecuted != want || down.File != up.File {
		t.Errorf("binlogTail with MySQL down = %+v; want %q from %s", down, want, up.File)
	}
}
