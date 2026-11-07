package mysql_test

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// listenOnce serves one connection on 127.0.0.1 with serve and returns
// the port.
func listenOnce(t *testing.T, serve func(net.Conn)) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		c, err := l.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		serve(c)
	}()
	return l.Addr().(*net.TCPAddr).Port
}

// TestProbePeerReadsGreeting: a MySQL server answers with its greeting;
// a closed port, a server that says nothing and one that speaks another
// protocol do not count as MySQL answering.
func TestProbePeerReadsGreeting(t *testing.T) {
	ctx := context.Background()
	inst := testmysql.Start(t, testmysql.Options{})
	if err := mysql.ProbePeer(ctx, "127.0.0.1", inst.Port, 5*time.Second); err != nil {
		t.Errorf("ProbePeer(mysqld) = %v", err)
	}
	if err := mysql.ProbePeer(ctx, "127.0.0.1", closedPort(t), 5*time.Second); err == nil {
		t.Error("ProbePeer(closed port) = nil")
	}
	silent := listenOnce(t, func(c net.Conn) { time.Sleep(3 * time.Second) })
	start := time.Now()
	if err := mysql.ProbePeer(ctx, "127.0.0.1", silent, time.Second); err == nil {
		t.Error("ProbePeer(silent server) = nil")
	} else if el := time.Since(start); el > 2500*time.Millisecond {
		t.Errorf("ProbePeer(silent server) took %v with a 1 s timeout", el)
	}
	httpish := listenOnce(t, func(c net.Conn) { c.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n")) })
	if err := mysql.ProbePeer(ctx, "127.0.0.1", httpish, 5*time.Second); err == nil {
		t.Error("ProbePeer(HTTP server) = nil")
	}
}

// TestReadPeerReadsGlobals: ReadPeer logs in with the given account (one
// without any privilege) and reads the four globals; a refused login is
// not reachable and its error never holds the password.
func TestReadPeerReadsGlobals(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	db := rootDB(t, inst)
	execAll(t, db, "CREATE USER 'peer'@'%' IDENTIFIED BY "+sqlLiteral(replPassword), "SET GLOBAL super_read_only = ON")
	var uuid, executed string
	if err := db.QueryRow("SELECT @@global.server_uuid, @@global.gtid_executed").Scan(&uuid, &executed); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	c := proto.PeerCred{Host: "127.0.0.1", Port: inst.Port, User: "peer", Password: replPassword}
	st := mysql.ReadPeer(ctx, c, 5*time.Second)
	if !st.Reachable || !st.SuperReadOnly || !st.ReadOnly || st.ServerUUID != uuid || st.GTIDExecuted != executed || st.Err != "" {
		t.Errorf("ReadPeer = %+v; want reachable, read-only, uuid %s, executed %q", st, uuid, executed)
	}
	execAll(t, db, "SET GLOBAL super_read_only = OFF", "SET GLOBAL read_only = OFF")
	if st := mysql.ReadPeer(ctx, c, 5*time.Second); !st.Reachable || st.SuperReadOnly || st.ReadOnly {
		t.Errorf("ReadPeer on a writable server = %+v", st)
	}
	bad := c
	bad.Password = "Wrong'pw-secret"
	st = mysql.ReadPeer(ctx, bad, 5*time.Second)
	if st.Reachable || st.Err == "" {
		t.Errorf("ReadPeer with a wrong password = %+v", st)
	}
	if strings.Contains(st.Err, bad.Password) || strings.Contains(st.Err, "secret") {
		t.Errorf("error shows the password: %s", st.Err)
	}
	gone := c
	gone.Port = closedPort(t)
	if st := mysql.ReadPeer(ctx, gone, 5*time.Second); st.Reachable || st.Err == "" {
		t.Errorf("ReadPeer on a closed port = %+v", st)
	}
	silent := listenOnce(t, func(c net.Conn) { time.Sleep(4 * time.Second) })
	start := time.Now()
	slow := c
	slow.Port = silent
	if st := mysql.ReadPeer(ctx, slow, time.Second); st.Reachable {
		t.Errorf("ReadPeer on a silent server = %+v", st)
	} else if el := time.Since(start); el > 3*time.Second {
		t.Errorf("ReadPeer on a silent server took %v with a 1 s timeout", el)
	}
}

func TestGTIDHasUUID(t *testing.T) {
	const own = "3e11fa47-71ca-11e1-9e33-c80aa9429562"
	for set, want := range map[string]bool{
		"": false,
		"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-5": false,
		own + ":1-5":                true,
		strings.ToUpper(own) + ":3": true,
		"aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-5,\n" + own + ":1-2:4": true,
		own + ":tag_x:1-4": true,
		own:                false,
		own + "0:1":        false,
	} {
		if got := mysql.GTIDHasUUID(set, own); got != want {
			t.Errorf("GTIDHasUUID(%q) = %v, want %v", set, got, want)
		}
	}
}

// TestReadPeerRequiresTLS (SEC-68): without certificate verification
// ReadPeer still requires TLS: a server that offers none (as someone in
// between stripping it would look) is not read.
func TestReadPeerRequiresTLS(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{ExtraArgs: []string{"tls-version=", "admin-tls-version="}})
	db := rootDB(t, inst)
	execAll(t, db, "CREATE USER 'peer'@'%' IDENTIFIED WITH mysql_native_password BY "+sqlLiteral(replPassword))
	var ssl string
	if err := db.QueryRow("SELECT @@global.have_ssl").Scan(&ssl); err != nil {
		t.Fatal(err)
	}
	if ssl == "YES" {
		t.Fatalf("server still offers TLS (have_ssl=%s)", ssl)
	}
	c := proto.PeerCred{Host: "127.0.0.1", Port: inst.Port, User: "peer", Password: replPassword}
	st := mysql.ReadPeer(context.Background(), c, 5*time.Second)
	if st.Reachable || !strings.Contains(strings.ToLower(st.Err), "tls") && !strings.Contains(strings.ToLower(st.Err), "ssl") {
		t.Errorf("ReadPeer of a server without TLS = %+v; want unreachable, a TLS error", st)
	}
}
