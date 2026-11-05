package mysql_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/pem"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	drv "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/panel/ca"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// testMySQLCA is a panel MySQL CA in a temporary directory.
func testMySQLCA(t *testing.T) *ca.MySQLCA {
	t.Helper()
	m, err := ca.LoadOrCreateMySQLCA(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// requestCert runs mysql.tlsRequest on inst and has m sign the CSR for hosts.
func requestCert(t *testing.T, inst *testmysql.Instance, m *ca.MySQLCA, hosts ...string) (certPEM []byte, serial string, notAfter time.Time) {
	t.Helper()
	req := decode[proto.TLSRequestResult](t, dispatch(t, inst.Host(), "mysql.tlsRequest", struct{}{}))
	certPEM, serial, notAfter, err := m.IssueMySQLCert([]byte(req.CSRPEM), 7, hosts)
	if err != nil {
		t.Fatal(err)
	}
	return certPEM, serial, notAfter
}

// installCert gives inst a certificate from m naming 127.0.0.1.
func installCert(t *testing.T, inst *testmysql.Instance, m *ca.MySQLCA) proto.TLSInstallResult {
	t.Helper()
	certPEM, _, _ := requestCert(t, inst, m, "127.0.0.1")
	return decode[proto.TLSInstallResult](t, dispatch(t, inst.Host(), "mysql.tlsInstall",
		proto.TLSInstallParams{CertPEM: string(certPEM), CAPEM: string(m.CAPEM()), VerifyHost: "127.0.0.1"}))
}

func statusValue(t *testing.T, db *sql.DB, name string) string {
	t.Helper()
	var v string
	if err := db.QueryRow("SELECT VARIABLE_VALUE FROM performance_schema.global_status WHERE VARIABLE_NAME = ?", name).Scan(&v); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return v
}

// verifiedConn connects to inst over TCP as a password account, verifying
// the server's certificate against caPEM and the name 127.0.0.1, and returns
// the serial of the certificate the server presented.
func verifiedConn(t *testing.T, inst *testmysql.Instance, caPEM []byte) (string, error) {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("bad CA PEM")
	}
	var serial string
	c := drv.NewConfig()
	c.User, c.Passwd = "tlscheck", "pw-tls"
	c.Net, c.Addr = "tcp", "127.0.0.1:"+strconv.Itoa(inst.Port)
	c.Timeout = 5 * time.Second
	c.TLS = &tls.Config{RootCAs: pool, ServerName: "127.0.0.1", MinVersion: tls.VersionTLS12,
		VerifyConnection: func(cs tls.ConnectionState) error {
			serial = cs.PeerCertificates[0].SerialNumber.Text(16)
			return nil
		}}
	conn, err := drv.NewConnector(c)
	if err != nil {
		t.Fatal(err)
	}
	db := sql.OpenDB(conn)
	defer db.Close()
	var name, cipher string
	if err := db.QueryRow("SHOW SESSION STATUS LIKE 'Ssl_cipher'").Scan(&name, &cipher); err != nil {
		return "", err
	}
	if cipher == "" {
		t.Fatal("verified connection is not encrypted")
	}
	return serial, nil
}

func tlsCheckUser(t *testing.T, db *sql.DB) {
	t.Helper()
	execAll(t, db, "CREATE USER tlscheck@'%' IDENTIFIED BY 'pw-tls'")
}

func wantMode(t *testing.T, path string, mode os.FileMode, owner os.FileInfo) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != mode {
		t.Errorf("%s mode = %v, want %v", path, fi.Mode().Perm(), mode)
	}
	got, want := fi.Sys().(*syscall.Stat_t), owner.Sys().(*syscall.Stat_t)
	if got.Uid != want.Uid || got.Gid != want.Gid {
		t.Errorf("%s owner = %d:%d, want the data directory's %d:%d", path, got.Uid, got.Gid, want.Uid, want.Gid)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

func TestTLSRequestInstallReload(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	db := rootDB(t, inst)
	tlsCheckUser(t, db)
	m := testMySQLCA(t)
	dir := mysql.TLSDir(inst.Layout())
	if dir != filepath.Join(inst.DataDir, "dbpanel-tls") {
		t.Fatalf("TLSDir = %s", dir)
	}
	dataDir, err := os.Stat(inst.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	before := statusValue(t, db, "Ssl_server_not_after")

	req := decode[proto.TLSRequestResult](t, dispatch(t, inst.Host(), "mysql.tlsRequest", struct{}{}))
	block, _ := pem.Decode([]byte(req.CSRPEM))
	if block == nil || block.Type != "CERTIFICATE REQUEST" {
		t.Fatalf("CSR = %q", req.CSRPEM)
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if pub, ok := csr.PublicKey.(*ecdsa.PublicKey); !ok || pub.Curve != elliptic.P256() {
		t.Errorf("CSR key = %T, want P-256", csr.PublicKey)
	}
	if len(csr.DNSNames)+len(csr.IPAddresses) != 0 {
		t.Errorf("CSR names = %v %v, want none", csr.DNSNames, csr.IPAddresses)
	}
	if host, _ := os.Hostname(); req.Hostname != host {
		t.Errorf("Hostname = %q, want %q", req.Hostname, host)
	}
	if strings.Contains(req.CSRPEM, "PRIVATE") {
		t.Error("result carries key material")
	}
	wantMode(t, dir, 0o700, dataDir)
	wantMode(t, filepath.Join(dir, "server.key.new"), 0o600, dataDir)
	if exists(filepath.Join(dir, "server.key")) {
		t.Error("server.key exists before tlsInstall")
	}

	certPEM, serial, notAfter, err := m.IssueMySQLCert([]byte(req.CSRPEM), 7, []string{"127.0.0.1", "db1.example"})
	if err != nil {
		t.Fatal(err)
	}
	res := decode[proto.TLSInstallResult](t, dispatch(t, inst.Host(), "mysql.tlsInstall",
		proto.TLSInstallParams{CertPEM: string(certPEM), CAPEM: string(m.CAPEM()), VerifyHost: "127.0.0.1"}))
	if res.Serial != serial || !res.NotAfter.Equal(notAfter) || !res.Reloaded {
		t.Errorf("result = %+v, want serial %s, not after %v", res, serial, notAfter)
	}
	for name, want := range map[string][]byte{"server.crt": certPEM, "mysql-ca.crt": m.CAPEM()} {
		if got := readFile(t, filepath.Join(dir, name)); !bytes.Equal(got, want) {
			t.Errorf("%s = %q", name, got)
		}
	}
	for _, name := range []string{"server.crt", "server.key", "mysql-ca.crt"} {
		wantMode(t, filepath.Join(dir, name), 0o600, dataDir)
	}
	for _, name := range []string{"server.key.new", "server.crt.new", "mysql-ca.crt.new"} {
		if exists(filepath.Join(dir, name)) {
			t.Errorf("%s left behind", name)
		}
	}

	after := statusValue(t, db, "Ssl_server_not_after")
	if after == before || !strings.Contains(after, strconv.Itoa(notAfter.Year())) {
		t.Errorf("Ssl_server_not_after = %q (before %q), want the new certificate's %v", after, before, notAfter)
	}
	got, err := verifiedConn(t, inst, m.CAPEM())
	if err != nil {
		t.Fatalf("VERIFY_IDENTITY connection to 127.0.0.1: %v", err)
	}
	if strings.TrimLeft(serial, "0") != got {
		t.Errorf("server presented serial %s, want %s", got, serial)
	}
	// The settings survive a restart.
	for name, want := range map[string]string{"ssl_ca": "mysql-ca.crt", "ssl_cert": "server.crt", "ssl_key": "server.key"} {
		var v string
		if err := db.QueryRow("SELECT VARIABLE_VALUE FROM performance_schema.persisted_variables WHERE VARIABLE_NAME = ?", name).Scan(&v); err != nil {
			t.Fatalf("persisted %s: %v", name, err)
		}
		if v != filepath.Join(dir, want) {
			t.Errorf("persisted %s = %q", name, v)
		}
	}
	db.SetMaxIdleConns(0) // close the pool's connections while the server is up
	inst.Restart(t)
	if _, err := verifiedConn(t, inst, m.CAPEM()); err != nil {
		t.Errorf("verified connection after restart: %v", err)
	}

	// A renewal with a new key: the old files become *.prev.
	oldKey := readFile(t, filepath.Join(dir, "server.key"))
	res2 := installCert(t, inst, m)
	if res2.Serial == res.Serial {
		t.Error("renewal kept the serial")
	}
	if got := readFile(t, filepath.Join(dir, "server.key.prev")); !bytes.Equal(got, oldKey) {
		t.Error("server.key.prev is not the previous key")
	}
	if got := readFile(t, filepath.Join(dir, "server.crt.prev")); !bytes.Equal(got, certPEM) {
		t.Error("server.crt.prev is not the previous certificate")
	}
	if got, err := verifiedConn(t, inst, m.CAPEM()); err != nil || strings.TrimLeft(res2.Serial, "0") != got {
		t.Errorf("after renewal: serial %s, %v; want %s", got, err, res2.Serial)
	}
}

// otherKeyCert is a certificate from m for a key nobody requested.
func otherKeyCert(t *testing.T, m *ca.MySQLCA) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "x"}}, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM, _, _, err := m.IssueMySQLCert(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}), 7, []string{"127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	return certPEM
}

func TestTLSInstallRollbackOnBadCert(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	db := rootDB(t, inst)
	tlsCheckUser(t, db)
	m := testMySQLCA(t)
	dir := mysql.TLSDir(inst.Layout())

	// On a server still serving its own automatic certificate, a failed
	// install puts the original settings back.
	autoNotAfter := statusValue(t, db, "Ssl_server_not_after")
	certPEM, _, _ := requestCert(t, inst, m, "127.0.0.1")
	r := dispatch(t, inst.Host(), "mysql.tlsInstall",
		proto.TLSInstallParams{CertPEM: string(certPEM), CAPEM: string(m.CAPEM()), VerifyHost: "db9.example"})
	if r.OK || !strings.Contains(r.Error.Message, "db9.example") {
		t.Fatalf("install with a name the certificate lacks = %+v", r)
	}
	if got := statusValue(t, db, "Ssl_server_not_after"); got != autoNotAfter {
		t.Errorf("Ssl_server_not_after = %q, want the automatic certificate's %q", got, autoNotAfter)
	}
	if v := variable(t, db, "ssl_cert"); v != "server-cert.pem" {
		t.Errorf("ssl_cert = %q after the rollback", v)
	}
	if n := count(t, db, "SELECT COUNT(*) FROM performance_schema.persisted_variables WHERE VARIABLE_NAME LIKE 'ssl\\_%'"); n != 0 {
		t.Errorf("%d ssl_* settings persisted by a failed install", n)
	}
	if exists(filepath.Join(dir, "server.crt")) || exists(filepath.Join(dir, "server.key")) {
		t.Error("files of the failed install left in place")
	}
	// The requested key is kept for another try.
	if !exists(filepath.Join(dir, "server.key.new")) {
		t.Error("server.key.new removed by the rollback")
	}

	good := installCert(t, inst, m)
	goodCert := readFile(t, filepath.Join(dir, "server.crt"))
	goodKey := readFile(t, filepath.Join(dir, "server.key"))
	notAfter := statusValue(t, db, "Ssl_server_not_after")

	// A certificate for another key is refused before anything changes.
	decode[proto.TLSRequestResult](t, dispatch(t, inst.Host(), "mysql.tlsRequest", struct{}{}))
	r = dispatch(t, inst.Host(), "mysql.tlsInstall",
		proto.TLSInstallParams{CertPEM: string(otherKeyCert(t, m)), CAPEM: string(m.CAPEM()), VerifyHost: "127.0.0.1"})
	if r.OK || !strings.Contains(r.Error.Message, "kunci") {
		t.Errorf("install of a certificate for another key = %+v", r)
	}

	// A certificate that fails the check after the reload is rolled back:
	// the old files return and MySQL serves the old certificate again.
	certPEM, _, _ = requestCert(t, inst, m, "127.0.0.1")
	r = dispatch(t, inst.Host(), "mysql.tlsInstall",
		proto.TLSInstallParams{CertPEM: string(certPEM), CAPEM: string(m.CAPEM()), VerifyHost: "db9.example"})
	if r.OK {
		t.Fatal("install with a name the certificate lacks succeeded")
	}
	if strings.Contains(r.Error.Message, "PRIVATE") {
		t.Errorf("error carries key material: %s", r.Error.Message)
	}
	if !bytes.Equal(readFile(t, filepath.Join(dir, "server.crt")), goodCert) ||
		!bytes.Equal(readFile(t, filepath.Join(dir, "server.key")), goodKey) {
		t.Error("previous certificate or key not restored")
	}
	if got := statusValue(t, db, "Ssl_server_not_after"); got != notAfter {
		t.Errorf("Ssl_server_not_after = %q, want the previous %q", got, notAfter)
	}
	if got, err := verifiedConn(t, inst, m.CAPEM()); err != nil || got != strings.TrimLeft(good.Serial, "0") {
		t.Errorf("after the rollback the server presents %s (%v), want %s", got, err, good.Serial)
	}
}

// TestTLSInstallRefusesClientCertAccounts (I7): accounts that log in with
// client certificates are checked against ssl_ca; an install that would
// replace it is refused, naming them, before anything changes. With the
// panel's CA already in place (an earlier install) a renewal goes on.
func TestTLSInstallRefusesClientCertAccounts(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	db := rootDB(t, inst)
	tlsCheckUser(t, db)
	m := testMySQLCA(t)
	if _, err := db.Exec("CREATE USER 'aplikasi'@'%' IDENTIFIED BY 'Rahasia-123' REQUIRE X509"); err != nil {
		t.Fatal(err)
	}
	before := variable(t, db, "ssl_ca")
	certPEM, _, _ := requestCert(t, inst, m, "127.0.0.1")
	r := dispatch(t, inst.Host(), "mysql.tlsInstall",
		proto.TLSInstallParams{CertPEM: string(certPEM), CAPEM: string(m.CAPEM()), VerifyHost: "127.0.0.1"})
	if r.OK || !strings.Contains(r.Error.Message, "akun 'aplikasi'@'%' memakai sertifikat klien") {
		t.Fatalf("install with a REQUIRE X509 account = %+v", r)
	}
	if got := variable(t, db, "ssl_ca"); got != before {
		t.Fatalf("ssl_ca = %q after the refusal, want %q", got, before)
	}
	if exists(filepath.Join(mysql.TLSDir(inst.Layout()), "server.crt")) {
		t.Fatal("certificate installed despite the refusal")
	}
	// Without the account the install goes on; with the panel's CA as
	// ssl_ca an account with REQUIRE SUBJECT no longer stops a renewal.
	if _, err := db.Exec("DROP USER 'aplikasi'@'%'"); err != nil {
		t.Fatal(err)
	}
	r = dispatch(t, inst.Host(), "mysql.tlsInstall",
		proto.TLSInstallParams{CertPEM: string(certPEM), CAPEM: string(m.CAPEM()), VerifyHost: "127.0.0.1"})
	if !r.OK {
		t.Fatalf("install = %+v", r.Error)
	}
	if _, err := db.Exec("CREATE USER 'aplikasi'@'%' IDENTIFIED BY 'Rahasia-123' REQUIRE SUBJECT '/CN=aplikasi'"); err != nil {
		t.Fatal(err)
	}
	installCert(t, inst, m)
}

func TestTLSInstallRejectsBadParams(t *testing.T) {
	m := testMySQLCA(t)
	certPEM := otherKeyCert(t, m)
	ok := proto.TLSInstallParams{CertPEM: string(certPEM), CAPEM: string(m.CAPEM()), VerifyHost: "db1.example"}
	with := func(f func(p *proto.TLSInstallParams)) proto.TLSInstallParams { p := ok; f(&p); return p }
	for name, p := range map[string]proto.TLSInstallParams{
		"no cert":        with(func(p *proto.TLSInstallParams) { p.CertPEM = "" }),
		"cert not PEM":   with(func(p *proto.TLSInstallParams) { p.CertPEM = "x" }),
		"two certs":      with(func(p *proto.TLSInstallParams) { p.CertPEM += string(certPEM) }),
		"CA as cert":     with(func(p *proto.TLSInstallParams) { p.CertPEM = string(m.CAPEM()) }),
		"leaf as CA":     with(func(p *proto.TLSInstallParams) { p.CAPEM = string(certPEM) }),
		"other CA":       with(func(p *proto.TLSInstallParams) { p.CAPEM = string(testMySQLCA(t).CAPEM()) }),
		"no verify host": with(func(p *proto.TLSInstallParams) { p.VerifyHost = "" }),
		"bad host":       with(func(p *proto.TLSInstallParams) { p.VerifyHost = "db1'x" }),
	} {
		r := dispatch(t, nil, "mysql.tlsInstall", p)
		if r.OK || r.Error.Code != proto.CodeBadParams {
			t.Errorf("%s: %+v, want bad_params", name, r)
		}
	}
	for name, p := range map[string]proto.TLSTrustParams{
		"empty":   {},
		"leaf":    {CAPEM: string(certPEM)},
		"garbage": {CAPEM: "-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----\n"},
	} {
		r := dispatch(t, nil, "mysql.tlsTrust", p)
		if r.OK || r.Error.Code != proto.CodeBadParams {
			t.Errorf("tlsTrust %s: %+v, want bad_params", name, r)
		}
	}
}

func TestTLSTrustWritesOnlyCA(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	m := testMySQLCA(t)
	dir := mysql.TLSDir(inst.Layout())
	dataDir, err := os.Stat(inst.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 { // idempotent
		res := decode[proto.TLSTrustResult](t, dispatch(t, inst.Host(), "mysql.tlsTrust", proto.TLSTrustParams{CAPEM: string(m.CAPEM())}))
		if res.CAPath != filepath.Join(dir, "mysql-ca.crt") {
			t.Errorf("CAPath = %s", res.CAPath)
		}
		if got := readFile(t, res.CAPath); !bytes.Equal(got, m.CAPEM()) {
			t.Errorf("mysql-ca.crt = %q", got)
		}
		wantMode(t, dir, 0o700, dataDir)
		wantMode(t, res.CAPath, 0o600, dataDir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("dbpanel-tls holds %d entries, want only mysql-ca.crt", len(entries))
	}
	if v := variable(t, rootDB(t, inst), "ssl_ca"); v == filepath.Join(dir, "mysql-ca.crt") {
		t.Error("tlsTrust changed ssl_ca")
	}
}

// TestTLSCommandsAllowedForEveryRole: the TLS commands pass the pins of
// every role.
func TestTLSCommandsAllowedForEveryRole(t *testing.T) {
	m := testMySQLCA(t)
	install := proto.TLSInstallParams{CertPEM: string(otherKeyCert(t, m)), CAPEM: string(m.CAPEM()), VerifyHost: "127.0.0.1"}
	for _, role := range []string{"master", "replica", "unassigned"} {
		source := ""
		if role == "replica" {
			source = "10.0.0.5:3306"
		}
		rig := newPinRig(t, nil, pinDir(t, role, source))
		wantPastPins(t, role+" tlsRequest", rig.dispatch("mysql.tlsRequest", struct{}{}))
		wantPastPins(t, role+" tlsInstall", rig.dispatch("mysql.tlsInstall", install))
		wantPastPins(t, role+" tlsTrust", rig.dispatch("mysql.tlsTrust", proto.TLSTrustParams{CAPEM: string(m.CAPEM())}))
	}
}
