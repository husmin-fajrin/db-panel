package mysql_test

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// quarantinedConnectRig is a quarantined server (read-only, persisted)
// whose agent holds root as its stored account.
type quarantinedConnectRig struct {
	*pinRig
	inst  *testmysql.Instance
	creds *memCreds
	dir   string
}

func newQuarantinedConnectRig(t *testing.T) *quarantinedConnectRig {
	t.Helper()
	inst := testmysql.Start(t, testmysql.Options{})
	execAll(t, rootDB(t, inst), "CREATE USER lemah@'127.0.0.1' IDENTIFIED BY 'Lemah-123'",
		"GRANT REPLICATION SLAVE ON *.* TO lemah@'127.0.0.1'",
		"SET PERSIST read_only = ON", "SET PERSIST super_read_only = ON")
	dir := pinDir(t, "quarantined", "10.0.0.1:3306")
	creds := &memCreds{p: proto.ConnectParams{User: "root"}}
	r := newPinRig(t, inst.HostWith(creds), dir)
	r.d.Creds = creds
	r.d.StartResultPath = filepath.Join(t.TempDir(), "start-result")
	return &quarantinedConnectRig{pinRig: r, inst: inst, creds: creds, dir: dir}
}

// TestConnectWeakAccountOnQuarantinedServer settles F-10: on a quarantined
// server mysql.connect with an account that lacks the privileges the
// agent's grant check lists (here only REPLICATION SLAVE) is refused and
// the stored account stays, so the guard still puts read-only back.
func TestConnectWeakAccountOnQuarantinedServer(t *testing.T) {
	r := newQuarantinedConnectRig(t)
	weak := proto.ConnectParams{User: "lemah", Password: "Lemah-123", Host: "127.0.0.1", Port: r.inst.Port}
	wantRefused(t, "connect with a weak account", r.dispatch("mysql.connect", weak),
		"akun lemah@127.0.0.1 belum punya semua hak yang dibutuhkan")
	if r.creds.p.User != "root" {
		t.Errorf("stored account = %+v, want root kept", r.creds.p)
	}
	// Someone opens MySQL; the guard puts read-only back through the
	// stored account.
	execAll(t, rootDB(t, r.inst), "SET GLOBAL super_read_only = OFF", "SET GLOBAL read_only = OFF")
	if err := r.d.GuardTick(context.Background(), time.Now()); err != nil {
		t.Logf("GuardTick: %v", err)
	}
	if sro, ro := readOnly(t, rootDB(t, r.inst)); !sro || !ro {
		t.Errorf("after the guard round: super_read_only=%v read_only=%v, want both ON", sro, ro)
	}
}

// TestConnectFullAccountOnQuarantinedServer: an account with every
// privilege the grant check lists is accepted on a quarantined server.
func TestConnectFullAccountOnQuarantinedServer(t *testing.T) {
	r := newQuarantinedConnectRig(t)
	p := proto.ConnectParams{User: "root", Host: "127.0.0.1", Port: r.inst.Port}
	res := decode[proto.ConnectResult](t, r.dispatch("mysql.connect", p))
	if len(res.MissingGrants) != 0 {
		t.Fatalf("root missing grants: %v", res.MissingGrants)
	}
	if r.creds.p.Port != r.inst.Port {
		t.Errorf("stored account = %+v, want the new one", r.creds.p)
	}
}

// fixedResolver answers every name from names; any other is not found.
func fixedResolver(names map[string][]string) (restore func()) {
	return mysql.SetResolver(func(_ context.Context, host string) ([]net.IP, error) {
		var out []net.IP
		for _, a := range names[host] {
			out = append(out, net.ParseIP(a))
		}
		if len(out) == 0 {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		return out, nil
	}, func(context.Context, string) ([]string, error) { return nil, nil })
}

// TestLocalMySQL: the socket, localhost, loopback, this machine's own
// addresses and names that resolve only to those are local; another
// address, a name with any other address and a name that does not resolve
// are not.
func TestLocalMySQL(t *testing.T) {
	t.Cleanup(mysql.SetInterfaceAddrs(func() ([]net.Addr, error) {
		return []net.Addr{&net.IPNet{IP: net.ParseIP("127.0.0.1"), Mask: net.CIDRMask(8, 32)},
			&net.IPNet{IP: net.ParseIP("192.168.5.10"), Mask: net.CIDRMask(24, 32)}}, nil
	}))
	t.Cleanup(fixedResolver(map[string][]string{
		"db.lokal":  {"192.168.5.10", "127.0.1.1"},
		"db.campur": {"192.168.5.10", "192.168.5.99"},
		"db.lain":   {"10.20.0.5"},
		"db.lokal6": {"::1"},
	}))
	for host, local := range map[string]bool{
		"": true, "localhost": true, "LOCALHOST": true, "127.0.0.1": true, "127.0.0.53": true, "::1": true,
		"192.168.5.10": true, "db.lokal": true, "db.lokal6": true,
		"192.168.5.99": false, "10.20.0.5": false, "db.campur": false, "db.lain": false,
	} {
		err := mysql.LocalMySQL(context.Background(), host)
		if local && err != nil {
			t.Errorf("%q: %v, want local", host, err)
		}
		if !local {
			want := "MySQL yang disambungkan harus berada di server ini (soket, 127.0.0.1, atau alamat server ini sendiri); " +
				host + " bukan alamat server ini."
			if err == nil || err.Error() != want {
				t.Errorf("%q: %v, want %q", host, err, want)
			}
		}
	}
}

// dnsRefusal is the C-3 text for host and the lookup error text.
func dnsRefusal(host, lookupErr string) string {
	return "alamat MySQL " + host + " tidak bisa dicari (DNS): " + lookupErr + ". MySQL yang disambungkan harus berada " +
		"di server ini (soket, 127.0.0.1, atau alamat server ini sendiri)."
}

// TestLocalMySQLLookupFailure: a name that does not resolve, or resolves
// to no address at all, is still refused, with a text that names DNS as
// the cause rather than the address.
func TestLocalMySQLLookupFailure(t *testing.T) {
	t.Cleanup(mysql.SetResolver(func(_ context.Context, host string) ([]net.IP, error) {
		if host == "db.kosong" {
			return nil, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}, nil))
	for host, want := range map[string]string{
		"db.tidakada": dnsRefusal("db.tidakada", "lookup db.tidakada: no such host"),
		"db.kosong":   dnsRefusal("db.kosong", "tidak ada alamat"),
	} {
		if err := mysql.LocalMySQL(context.Background(), host); err == nil || err.Error() != want {
			t.Errorf("%q: %v\nwant %q", host, err, want)
		}
	}
}

// TestConnectRefusesUnresolvedMySQL: mysql.connect to a name that does not
// resolve is refused with the DNS text before any login, and a stored
// account with such a name fails closed with the same text.
func TestConnectRefusesUnresolvedMySQL(t *testing.T) {
	t.Cleanup(fixedResolver(nil))
	inst := testmysql.Start(t, testmysql.Options{})
	creds := &memCreds{p: proto.ConnectParams{User: "root"}}
	r := newPinRig(t, inst.HostWith(creds), pinDir(t, "", ""))
	r.d.Creds = creds
	want := dnsRefusal("db.tidakada", "lookup db.tidakada: no such host")
	resp := r.dispatch("mysql.connect", proto.ConnectParams{User: "root", Host: "db.tidakada", Port: inst.Port})
	if resp.OK || resp.Error == nil || resp.Error.Message != want {
		t.Fatalf("connect db.tidakada = %+v, want %q", resp, want)
	}
	if creds.p.Host != "" {
		t.Errorf("stored account = %+v, want kept", creds.p)
	}
	stored := &memCreds{p: proto.ConnectParams{User: "root", Host: "db.tidakada", Port: inst.Port}}
	if _, err := inst.HostWith(stored).Open(context.Background()); err == nil || err.Error() != want {
		t.Errorf("Open with a stored unresolved name = %v, want %q", err, want)
	}
}

// TestConnectRefusesRemoteMySQL: on any role mysql.connect refuses a MySQL
// that is not on this machine, before any login, and keeps the stored
// account.
func TestConnectRefusesRemoteMySQL(t *testing.T) {
	t.Cleanup(fixedResolver(map[string][]string{"db.campur": {"127.0.0.1", "10.20.0.5"}}))
	inst := testmysql.Start(t, testmysql.Options{})
	for _, role := range []string{"", "master", "replica", "quarantined"} {
		dir := pinDir(t, role, "")
		creds := &memCreds{p: proto.ConnectParams{User: "root"}}
		r := newPinRig(t, inst.HostWith(creds), dir)
		r.d.Creds = creds
		for _, host := range []string{"10.20.0.5", "db.campur"} {
			wantRefused(t, role+" connect "+host, r.dispatch("mysql.connect",
				proto.ConnectParams{User: "admin", Password: "x", Host: host, Port: 3306}),
				"MySQL yang disambungkan harus berada di server ini", host+" bukan alamat server ini.")
		}
		if creds.p.User != "root" || creds.p.Host != "" {
			t.Errorf("%s: stored account = %+v, want kept", role, creds.p)
		}
	}
}

// TestStoredRemoteAccountFailsClosed: an account an older agent stored
// for another machine's MySQL is never used: opening MySQL fails with the
// locality text, so the health snapshot (and with it the guard's report)
// carries it.
func TestStoredRemoteAccountFailsClosed(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	creds := &memCreds{p: proto.ConnectParams{User: "root", Host: "10.20.0.5", Port: inst.Port}}
	h := inst.HostWith(creds)
	_, err := h.Open(context.Background())
	want := "10.20.0.5 bukan alamat server ini."
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("Open with a stored remote account = %v, want %q", err, want)
	}
	d := &mysql.Driver{Host: h, Creds: creds, PinDir: pinDir(t, "", ""), SeenPath: filepath.Join(t.TempDir(), "seen")}
	if s := d.Snapshot(context.Background()); s.MySQLOK || !strings.Contains(s.MySQLError, want) {
		t.Errorf("snapshot = ok %v, error %q; want the locality text", s.MySQLOK, s.MySQLError)
	}
}

// changingResolver answers name with first on the first lookup and with
// then on every later one; other names are not found. The system resolver
// does not know name, so a client that dials it by name fails.
func changingResolver(name, first, then string) (restore func()) {
	calls := 0
	return mysql.SetResolver(func(_ context.Context, host string) ([]net.IP, error) {
		if host != name {
			return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
		}
		calls++
		if calls == 1 {
			return []net.IP{net.ParseIP(first)}, nil
		}
		return []net.IP{net.ParseIP(then)}, nil
	}, nil)
}

// TestDialGoesToCheckedAddress: the name is resolved once, and the
// connection goes to the address the locality check accepted, not to
// whatever the name resolves to when it is dialled.
func TestDialGoesToCheckedAddress(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	const name = "db.berubah.test"

	t.Run("mysql.connect", func(t *testing.T) {
		defer changingResolver(name, "127.0.0.1", "10.20.0.5")()
		creds := &memCreds{p: proto.ConnectParams{User: "root"}}
		r := newPinRig(t, inst.HostWith(creds), pinDir(t, "", ""))
		r.d.Creds = creds
		decode[proto.ConnectResult](t, r.dispatch("mysql.connect", proto.ConnectParams{User: "root", Host: name, Port: inst.Port}))
		if creds.p.Host != name {
			t.Errorf("stored host = %q, want the name as given", creds.p.Host)
		}
	})
	t.Run("stored account", func(t *testing.T) {
		defer changingResolver(name, "127.0.0.1", "10.20.0.5")()
		creds := &memCreds{p: proto.ConnectParams{User: "root", Host: name, Port: inst.Port}}
		db, err := inst.HostWith(creds).Open(context.Background())
		if err != nil {
			t.Fatalf("Open with a stored name = %v", err)
		}
		defer db.Close()
		// The pool dials again for a second connection: still the checked
		// address.
		c1, err := db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer c1.Close()
		c2, err := db.Conn(context.Background())
		if err != nil {
			t.Fatalf("second connection of the pool: %v", err)
		}
		c2.Close()
	})
}

// TestDumpLocalClientUsesCheckedAddress: the dump's local mysql client is
// given the address the check accepted for the stored account's host
// name, not the name (which only the agent's resolver knows here).
func TestDumpLocalClientUsesCheckedAddress(t *testing.T) {
	const name = "db.lokal.test"
	t.Cleanup(mysql.SetResolver(func(_ context.Context, host string) ([]net.IP, error) {
		if host == name {
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}, nil))
	master := testmysql.Start(t, testmysql.Options{})
	replica := testmysql.Start(t, testmysql.Options{})
	mdb := rootDB(t, master)
	execAll(t, mdb, "CREATE DATABASE a", "CREATE TABLE a.t (id INT PRIMARY KEY)", "INSERT INTO a.t VALUES (1)")
	dumpUser(t, mdb)
	creds := &memCreds{p: proto.ConnectParams{User: "root", Host: name, Port: replica.Port}}
	rig := newSnapshotRig(replica.HostWith(creds), creds)
	snapshotResult(t, rig.run(t, "mysql.dumpFrom", dumpParams(master, "a")))
	if n := count(t, rootDB(t, replica), "SELECT COUNT(*) FROM a.t"); n != 1 {
		t.Errorf("a.t rows on the replica = %d, want 1", n)
	}
}

// TestDialPrefersIPv4: a name with a local IPv6 and a local IPv4 address
// is dialled at the IPv4 one, as MySQL often listens on IPv4 only (the
// test instance binds 127.0.0.1).
func TestDialPrefersIPv4(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	const name = "db.duatumpuk.test"
	t.Cleanup(mysql.SetResolver(func(_ context.Context, host string) ([]net.IP, error) {
		if host == name {
			return []net.IP{net.ParseIP("::1"), net.ParseIP("127.0.0.1")}, nil
		}
		return nil, &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
	}, nil))
	creds := &memCreds{p: proto.ConnectParams{User: "root"}}
	r := newPinRig(t, inst.HostWith(creds), pinDir(t, "", ""))
	r.d.Creds = creds
	decode[proto.ConnectResult](t, r.dispatch("mysql.connect", proto.ConnectParams{User: "root", Host: name, Port: inst.Port}))
	stored := &memCreds{p: proto.ConnectParams{User: "root", Host: name, Port: inst.Port}}
	db, err := inst.HostWith(stored).Open(context.Background())
	if err != nil {
		t.Fatalf("Open with a stored dual-stack name = %v", err)
	}
	db.Close()
}

// TestLocalhostAnyCaseIsLocalhost: localhost in any letter case is
// dialled as localhost (the socket, or 127.0.0.1 with a port), never by
// name.
func TestLocalhostAnyCaseIsLocalhost(t *testing.T) {
	t.Cleanup(mysql.SetResolver(func(_ context.Context, host string) ([]net.IP, error) {
		t.Errorf("lookup of %q", host)
		return nil, errors.New("no lookup expected")
	}, nil))
	for _, h := range []string{"localhost", "LOCALHOST", "LocalHost"} {
		if got, err := mysql.LocalMySQLDial(context.Background(), h); err != nil || got != "localhost" {
			t.Errorf("%q: dial %q, %v; want localhost", h, got, err)
		}
	}
	if got, err := mysql.LocalMySQLDial(context.Background(), ""); err != nil || got != "" {
		t.Errorf(`"": dial %q, %v; want ""`, got, err)
	}
}
