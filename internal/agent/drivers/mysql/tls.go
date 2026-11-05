package mysql

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode"

	drv "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/pki"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// MySQL's server certificate from the panel's MySQL CA. mysql.tlsRequest
// makes a key and a CSR, the panel signs it, and mysql.tlsInstall installs
// the certificate and has MySQL load it without a restart. mysql.tlsTrust
// only stores the CA, for a replica that verifies its source.
//
// Everything lives in TLSDir, inside the data directory so that mysqld may
// read it under AppArmor or SELinux: the directory 0700 and the files 0600,
// owned by the data directory's owner (the MySQL service user).

const (
	tlsDirName  = "dbpanel-tls"
	tlsKeyFile  = "server.key"
	tlsCertFile = "server.crt"
	tlsCAFile   = "mysql-ca.crt"
	tlsNew      = ".new"
	tlsPrev     = ".prev"

	// maxPEMLen bounds a certificate taken from the panel.
	maxPEMLen = 64 << 10
	// maxCAPathLen is the longest SOURCE_SSL_CA MySQL accepts.
	maxCAPathLen = 511
	// tlsCheckTimeout bounds the connection that checks a new certificate.
	tlsCheckTimeout = 10 * time.Second
)

// TLSDir is the directory of the MySQL server's panel certificate, key and
// CA: <datadir>/dbpanel-tls.
func TLSDir(l Layout) string { return filepath.Join(l.DataDir, tlsDirName) }

func (d *Driver) registerTLS(reg *commands.Registry) {
	reg.Register("mysql.tlsRequest", func(ctx context.Context, _ json.RawMessage) (any, error) {
		if err := d.allow("mysql.tlsRequest", nil); err != nil {
			return nil, err
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		d.tlsMu.Lock()
		defer d.tlsMu.Unlock()
		res, err := requestTLS(h.Layout())
		if err != nil {
			return nil, err
		}
		d.log("mysql tls key created", "dir", TLSDir(h.Layout()))
		return res, nil
	})

	reg.Register("mysql.tlsInstall", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.TLSInstallParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		in, err := validateTLSInstall(&p)
		if err != nil {
			return nil, err
		}
		if err := d.allow("mysql.tlsInstall", p); err != nil {
			return nil, err
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		d.tlsMu.Lock()
		defer d.tlsMu.Unlock()
		res, err := installTLS(ctx, h, d.Creds, in)
		if err != nil {
			if d.Logger != nil {
				d.Logger.Warn("mysql tls install failed", "verify_host", p.VerifyHost, "err", err)
			}
			return nil, err
		}
		d.log("mysql tls installed", "serial", res.Serial, "not_after", res.NotAfter, "verify_host", p.VerifyHost)
		return res, nil
	})

	reg.Register("mysql.tlsTrust", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.TLSTrustParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		ca, err := parseCA(p.CAPEM)
		if err != nil {
			return nil, err
		}
		if err := d.allow("mysql.tlsTrust", p); err != nil {
			return nil, err
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		d.tlsMu.Lock()
		defer d.tlsMu.Unlock()
		path, err := trustTLS(h.Layout(), ca.pem)
		if err != nil {
			return nil, err
		}
		d.log("mysql tls ca stored", "path", path)
		return proto.TLSTrustResult{CAPath: path}, nil
	})
}

// validateVerify checks the CA file of a verified connection to a master:
// verify needs ssl and an absolute, clean path MySQL takes (no quote,
// backslash or control character, so it is the same literal in SQL and in
// an option file). Without verify the path is dropped.
func validateVerify(verify, ssl bool, ca *string) error {
	if !verify {
		*ca = ""
		return nil
	}
	bad := func(format string, args ...any) error { return &commands.ParamError{Msg: fmt.Sprintf(format, args...)} }
	switch {
	case !ssl:
		return bad("verifikasi sertifikat master butuh SSL")
	case *ca == "":
		return bad("verifikasi sertifikat master butuh berkas CA (ssl_ca)")
	case !filepath.IsAbs(*ca) || filepath.Clean(*ca) != *ca:
		return bad("berkas CA harus path absolut: %q", *ca)
	case len(*ca) > maxCAPathLen:
		return bad("path berkas CA paling panjang %d karakter", maxCAPathLen)
	case strings.ContainsAny(*ca, `'"\`) || strings.ContainsFunc(*ca, unicode.IsControl):
		return bad("path berkas CA tidak boleh berisi tanda kutip, backslash atau karakter kontrol: %q", *ca)
	}
	return nil
}

// parsedCert is a certificate taken from the panel, with its PEM.
type parsedCert struct {
	cert *x509.Certificate
	pem  []byte
}

// parseCertPEM parses s: exactly one CERTIFICATE block and nothing else.
func parseCertPEM(s, what string) (parsedCert, error) {
	bad := func(format string, args ...any) error {
		return &commands.ParamError{Msg: what + " " + fmt.Sprintf(format, args...)}
	}
	if len(s) > maxPEMLen {
		return parsedCert{}, bad("terlalu besar")
	}
	block, rest := pem.Decode([]byte(s))
	if block == nil || block.Type != "CERTIFICATE" {
		return parsedCert{}, bad("bukan PEM CERTIFICATE")
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return parsedCert{}, bad("berisi lebih dari satu blok PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return parsedCert{}, bad("tidak valid: %v", err)
	}
	return parsedCert{cert: cert, pem: pem.EncodeToMemory(block)}, nil
}

// parseCA parses a CA certificate.
func parseCA(s string) (parsedCert, error) {
	ca, err := parseCertPEM(s, "CA MySQL")
	if err != nil {
		return ca, err
	}
	if !ca.cert.IsCA || !ca.cert.BasicConstraintsValid {
		return ca, &commands.ParamError{Msg: "CA MySQL bukan sertifikat CA"}
	}
	return ca, nil
}

// tlsInstall is a validated mysql.tlsInstall.
type tlsInstall struct {
	cert, ca   parsedCert
	verifyHost string
}

// validateTLSInstall checks p: a server certificate the CA signed, the CA,
// and the name to check the served certificate against.
func validateTLSInstall(p *proto.TLSInstallParams) (tlsInstall, error) {
	var in tlsInstall
	var err error
	if in.cert, err = parseCertPEM(p.CertPEM, "sertifikat MySQL"); err != nil {
		return in, err
	}
	if in.ca, err = parseCA(p.CAPEM); err != nil {
		return in, err
	}
	if in.cert.cert.IsCA {
		return in, &commands.ParamError{Msg: "sertifikat MySQL adalah sertifikat CA, bukan sertifikat server"}
	}
	if _, ok := in.cert.cert.PublicKey.(*ecdsa.PublicKey); !ok {
		return in, &commands.ParamError{Msg: "sertifikat MySQL harus berkunci ECDSA"}
	}
	if err := in.cert.cert.CheckSignatureFrom(in.ca.cert); err != nil {
		return in, &commands.ParamError{Msg: "sertifikat MySQL tidak ditandatangani CA yang dikirim: " + err.Error()}
	}
	p.VerifyHost = strings.TrimSpace(p.VerifyHost)
	if !sourceHostRe.MatchString(p.VerifyHost) || (strings.Contains(p.VerifyHost, ":") && net.ParseIP(p.VerifyHost) == nil) {
		return in, &commands.ParamError{Msg: fmt.Sprintf("host verifikasi tidak valid: %q", p.VerifyHost)}
	}
	in.verifyHost = p.VerifyHost
	return in, nil
}

// tlsOwner is the uid and gid of the data directory, which the TLS files
// get: mysqld runs as that user and must read them.
type tlsOwner struct{ uid, gid int }

func dataDirOwner(l Layout) (tlsOwner, error) {
	if l.DataDir == "" {
		return tlsOwner{}, errors.New("direktori data MySQL tidak diketahui")
	}
	fi, err := os.Stat(l.DataDir)
	if err != nil {
		return tlsOwner{}, fmt.Errorf("baca direktori data MySQL: %w", err)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return tlsOwner{}, fmt.Errorf("pemilik %s tidak terbaca", l.DataDir)
	}
	return tlsOwner{uid: int(st.Uid), gid: int(st.Gid)}, nil
}

// mine reports whether files this process creates already have o.
func (o tlsOwner) mine() bool { return o.uid == os.Geteuid() && o.gid == os.Getegid() }

// tlsDir creates TLSDir(l) or makes the existing one a 0700 directory
// owned by the data directory's owner. A symlink is refused.
func tlsDir(l Layout) (string, tlsOwner, error) {
	owner, err := dataDirOwner(l)
	if err != nil {
		return "", owner, err
	}
	dir := TLSDir(l)
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", owner, fmt.Errorf("buat %s: %w", dir, err)
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", owner, fmt.Errorf("baca %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return "", owner, fmt.Errorf("%s bukan direktori", dir)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", owner, fmt.Errorf("atur izin %s: %w", dir, err)
	}
	if !owner.mine() {
		if err := os.Lchown(dir, owner.uid, owner.gid); err != nil {
			return "", owner, fmt.Errorf("atur pemilik %s: %w", dir, err)
		}
	}
	return dir, owner, nil
}

// writeTLSFile replaces dir/name with data: a temporary file (0600, owned
// by owner) renamed over it.
func writeTLSFile(dir, name string, data []byte, owner tlsOwner) error {
	path := filepath.Join(dir, name)
	fail := func(err error) error { return fmt.Errorf("tulis %s: %w", path, err) }
	f, err := os.CreateTemp(dir, "."+name+".tmp-*")
	if err != nil {
		return fail(err)
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op after the rename
	err = f.Chmod(0o600)
	if err == nil && !owner.mine() {
		err = f.Chown(owner.uid, owner.gid)
	}
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		return fail(err)
	}
	return nil
}

// requestTLS makes a new ECDSA P-256 key in TLSDir(l)/server.key.new
// (replacing an earlier request's) and returns a CSR for it. The CSR names
// the host only in its subject, which the panel ignores; the key never
// leaves the directory.
func requestTLS(l Layout) (proto.TLSRequestResult, error) {
	var res proto.TLSRequestResult
	dir, owner, err := tlsDir(l)
	if err != nil {
		return res, err
	}
	res.Hostname, _ = os.Hostname()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return res, fmt.Errorf("buat kunci MySQL: %w", err)
	}
	keyPEM, err := pki.EncodeKey(key)
	if err != nil {
		return res, err
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: res.Hostname},
	}, key)
	if err != nil {
		return res, fmt.Errorf("buat CSR MySQL: %w", err)
	}
	if err := writeTLSFile(dir, tlsKeyFile+tlsNew, keyPEM, owner); err != nil {
		return res, err
	}
	res.CSRPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
	return res, nil
}

// trustTLS stores caPEM as TLSDir(l)/mysql-ca.crt and returns its path.
// The same CA again changes nothing.
func trustTLS(l Layout, caPEM []byte) (string, error) {
	dir, owner, err := tlsDir(l)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, tlsCAFile)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, caPEM) {
		return path, nil
	}
	return path, writeTLSFile(dir, tlsCAFile, caPEM, owner)
}

// keyMatches reports whether keyPEM (an EC PRIVATE KEY) is the private key
// of cert.
func keyMatches(keyPEM []byte, cert *x509.Certificate) bool {
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return false
	}
	key, err := x509.ParseECPrivateKey(block.Bytes)
	if err != nil {
		return false
	}
	pub, ok := cert.PublicKey.(*ecdsa.PublicKey)
	return ok && pub.Equal(key.Public())
}

// tlsVars are MySQL's ssl_ca, ssl_cert and ssl_key (NULL when unset).
type tlsVars struct{ ca, cert, key sql.NullString }

func readTLSVars(ctx context.Context, db *sql.DB) (tlsVars, error) {
	var v tlsVars
	if err := db.QueryRowContext(ctx, "SELECT @@GLOBAL.ssl_ca, @@GLOBAL.ssl_cert, @@GLOBAL.ssl_key").Scan(&v.ca, &v.cert, &v.key); err != nil {
		return v, fmt.Errorf("baca pengaturan TLS MySQL: %w", err)
	}
	return v, nil
}

// setTLSVars sets v with SET scope (GLOBAL or PERSIST).
func setTLSVars(ctx context.Context, db *sql.DB, scope string, v tlsVars) error {
	q := "SET " + scope + " ssl_ca = ?, " + scope + " ssl_cert = ?, " + scope + " ssl_key = ?"
	if _, err := db.ExecContext(ctx, q, v.ca, v.cert, v.key); err != nil {
		return fmt.Errorf("SET %s ssl_ca/ssl_cert/ssl_key: %w", scope, err)
	}
	return nil
}

// rotation is the renames of one install, undone in reverse on failure.
type rotation struct {
	dir  string
	done []rotated
}

type rotated struct {
	name    string
	hadPrev bool // name existed and was moved to name.prev
}

// swap moves dir/name to name.prev (when it exists) and name.new to name.
func (r *rotation) swap(name string) error {
	path := filepath.Join(r.dir, name)
	hadPrev := false
	if _, err := os.Lstat(path); err == nil {
		if err := os.Rename(path, path+tlsPrev); err != nil {
			return fmt.Errorf("simpan %s lama: %w", path, err)
		}
		hadPrev = true
	}
	if err := os.Rename(path+tlsNew, path); err != nil {
		if hadPrev {
			_ = os.Rename(path+tlsPrev, path)
		}
		return fmt.Errorf("pasang %s: %w", path, err)
	}
	r.done = append(r.done, rotated{name: name, hadPrev: hadPrev})
	return nil
}

// undo puts every swapped file back: name returns to name.new, name.prev to
// name. It returns the first error and keeps going.
func (r *rotation) undo() error {
	var first error
	for i := len(r.done) - 1; i >= 0; i-- {
		path := filepath.Join(r.dir, r.done[i].name)
		err := os.Rename(path, path+tlsNew)
		if err == nil && r.done[i].hadPrev {
			err = os.Rename(path+tlsPrev, path)
		}
		if err != nil && first == nil {
			first = err
		}
	}
	r.done = nil
	return first
}

// installTLS installs in's certificate for the key mysql.tlsRequest made
// (server.key.new), or for the installed key when there is no new one (the
// CA signed the same key again). The certificate and the CA go to
// server.crt and mysql-ca.crt, the previous files to *.prev; MySQL's
// ssl_ca, ssl_cert and ssl_key are pointed at them and ALTER INSTANCE
// RELOAD TLS loads them. A fresh connection then has to see this
// certificate, verified against the CA for in.verifyHost, before the
// settings are persisted. Any failure after the files moved puts the
// previous files and settings back and reloads them, so MySQL keeps
// serving what it served before.
//
// The check connection goes over the socket (TLS works there too, and no
// account's host part or bind_address gets in the way) with the stored
// admin account, or root without a password when none is stored. A login
// the server refuses still proves the certificate: the refusal comes after
// the TLS handshake.
func installTLS(ctx context.Context, h Host, creds CredentialStore, in tlsInstall) (proto.TLSInstallResult, error) {
	var res proto.TLSInstallResult
	l := h.Layout()
	dir, owner, err := tlsDir(l)
	if err != nil {
		return res, err
	}
	keyName := tlsKeyFile + tlsNew
	keyPEM, err := os.ReadFile(filepath.Join(dir, keyName))
	if errors.Is(err, os.ErrNotExist) {
		keyName = tlsKeyFile
		keyPEM, err = os.ReadFile(filepath.Join(dir, keyName))
		if errors.Is(err, os.ErrNotExist) {
			return res, &commands.ParamError{Msg: "belum ada kunci MySQL di server ini; jalankan mysql.tlsRequest dulu"}
		}
	}
	if err != nil {
		return res, fmt.Errorf("baca kunci MySQL: %w", err)
	}
	if !keyMatches(keyPEM, in.cert.cert) {
		return res, &commands.ParamError{Msg: "sertifikat MySQL tidak cocok dengan kunci yang dibuat server ini (" + keyName + "); minta sertifikat baru"}
	}

	db, err := openAdmin(ctx, h)
	if err != nil {
		return res, err
	}
	defer db.Close()
	old, err := readTLSVars(ctx, db)
	if err != nil {
		return res, err
	}

	if err := writeTLSFile(dir, tlsCertFile+tlsNew, in.cert.pem, owner); err != nil {
		return res, err
	}
	if err := writeTLSFile(dir, tlsCAFile+tlsNew, in.ca.pem, owner); err != nil {
		os.Remove(filepath.Join(dir, tlsCertFile+tlsNew))
		return res, err
	}
	rot := &rotation{dir: dir}
	names := []string{tlsCertFile, tlsCAFile}
	if keyName != tlsKeyFile {
		names = append(names, tlsKeyFile)
	}
	installed := tlsVars{
		ca:   sql.NullString{String: filepath.Join(dir, tlsCAFile), Valid: true},
		cert: sql.NullString{String: filepath.Join(dir, tlsCertFile), Valid: true},
		key:  sql.NullString{String: filepath.Join(dir, tlsKeyFile), Valid: true},
	}

	err = func() error {
		for _, name := range names {
			if err := rot.swap(name); err != nil {
				return err
			}
		}
		if err := setTLSVars(ctx, db, "GLOBAL", installed); err != nil {
			return err
		}
		if _, err := db.ExecContext(ctx, "ALTER INSTANCE RELOAD TLS"); err != nil {
			return fmt.Errorf("MySQL menolak memuat sertifikat baru (ALTER INSTANCE RELOAD TLS): %w", err)
		}
		if err := checkServedCert(ctx, l, creds, in); err != nil {
			return err
		}
		return setTLSVars(ctx, db, "PERSIST", installed)
	}()
	if err != nil {
		return res, rollbackTLS(context.WithoutCancel(ctx), db, rot, old, err)
	}
	// The certificate and CA are in place; nothing is left to undo.
	res.Serial = fmt.Sprintf("%032x", in.cert.cert.SerialNumber)
	res.NotAfter = in.cert.cert.NotAfter
	res.Reloaded = true
	return res, nil
}

// rollbackTLS undoes a failed install: the files, the settings, and the
// loaded certificate. cause is the install's error; the result says
// whether MySQL is back on its previous certificate.
func rollbackTLS(ctx context.Context, db *sql.DB, rot *rotation, old tlsVars, cause error) error {
	var problems []string
	if err := rot.undo(); err != nil {
		problems = append(problems, "kembalikan berkas: "+err.Error())
	}
	for _, name := range []string{tlsCertFile + tlsNew, tlsCAFile + tlsNew} {
		os.Remove(filepath.Join(rot.dir, name)) // the key stays for another try
	}
	if err := setTLSVars(ctx, db, "GLOBAL", old); err != nil {
		problems = append(problems, err.Error())
	} else if _, err := db.ExecContext(ctx, "ALTER INSTANCE RELOAD TLS"); err != nil {
		problems = append(problems, "ALTER INSTANCE RELOAD TLS: "+err.Error())
	}
	if len(problems) > 0 {
		return fmt.Errorf("pasang sertifikat MySQL gagal: %v; pengembalian ke sertifikat lama juga gagal (%s), periksa TLS MySQL di server ini",
			cause, strings.Join(problems, "; "))
	}
	return fmt.Errorf("pasang sertifikat MySQL gagal, sertifikat lama dipakai lagi: %w", cause)
}

// checkServedCert connects to the server as installTLS describes and
// checks that it serves in's certificate, valid for in.verifyHost under in's
// CA.
func checkServedCert(ctx context.Context, l Layout, creds CredentialStore, in tlsInstall) error {
	pool := x509.NewCertPool()
	pool.AddCert(in.ca.cert)
	c := drv.NewConfig()
	c.User = "root"
	if creds != nil {
		if p, ok, err := creds.Load(); err == nil && ok {
			c.User, c.Passwd = p.User, p.Password
		}
	}
	if l.Socket != "" {
		c.Net, c.Addr = "unix", l.Socket
	} else {
		port := l.Port
		if port == 0 {
			port = 3306
		}
		c.Net, c.Addr = "tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	}
	c.Timeout, c.ReadTimeout, c.WriteTimeout = tlsCheckTimeout, tlsCheckTimeout, tlsCheckTimeout
	served := false
	c.TLS = &tls.Config{
		RootCAs:    pool,
		ServerName: in.verifyHost,
		MinVersion: tls.VersionTLS12,
		// Runs after the chain and the name are verified.
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 || !cs.PeerCertificates[0].Equal(in.cert.cert) {
				return errors.New("MySQL masih menyajikan sertifikat lain")
			}
			served = true
			return nil
		},
	}
	conn, err := drv.NewConnector(c)
	if err != nil {
		return err
	}
	db := sql.OpenDB(conn)
	defer db.Close()
	cctx, cancel := context.WithTimeout(ctx, tlsCheckTimeout)
	defer cancel()
	err = db.PingContext(cctx)
	if served && (err == nil || LoginRefused(err)) {
		return nil
	}
	if err == nil {
		err = errors.New("sambungan tidak memakai TLS")
	}
	return fmt.Errorf("sambungan baru dengan verifikasi sertifikat untuk %s gagal: %w", in.verifyHost, err)
}
