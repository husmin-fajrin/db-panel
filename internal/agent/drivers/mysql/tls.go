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
	"encoding/hex"
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

func ownerOf(fi os.FileInfo, name string) (tlsOwner, error) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return tlsOwner{}, fmt.Errorf("pemilik %s tidak terbaca", name)
	}
	return tlsOwner{uid: int(st.Uid), gid: int(st.Gid)}, nil
}

// mine reports whether files this process creates already have o.
func (o tlsOwner) mine() bool { return o.uid == os.Geteuid() && o.gid == os.Getegid() }

// tlsFiles is TLSDir, opened once. The data directory belongs to the MySQL
// service user, who could swap dbpanel-tls for a symlink at any moment
// (SEC-58): every operation therefore goes through the directory's own
// descriptor (an *os.Root, which never leaves it, and fchmod/fchown),
// never through a path. path is only for messages and for MySQL.
type tlsFiles struct {
	path  string
	root  *os.Root
	owner tlsOwner
}

// openTLSDir creates TLSDir(l) or makes the existing one a 0700 directory
// owned by the data directory's owner, and opens it. A symlink is refused.
// The caller closes it.
func openTLSDir(l Layout) (*tlsFiles, error) {
	if l.DataDir == "" {
		return nil, errors.New("direktori data MySQL tidak diketahui")
	}
	data, err := os.OpenRoot(l.DataDir)
	if err != nil {
		return nil, fmt.Errorf("buka direktori data MySQL: %w", err)
	}
	defer data.Close()
	fi, err := data.Stat(".")
	if err != nil {
		return nil, fmt.Errorf("baca direktori data MySQL: %w", err)
	}
	owner, err := ownerOf(fi, l.DataDir)
	if err != nil {
		return nil, err
	}
	path := TLSDir(l)
	if err := data.Mkdir(tlsDirName, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("buat %s: %w", path, err)
	}
	if fi, err := data.Lstat(tlsDirName); err != nil {
		return nil, fmt.Errorf("baca %s: %w", path, err)
	} else if !fi.IsDir() {
		return nil, fmt.Errorf("%s bukan direktori", path)
	}
	root, err := data.OpenRoot(tlsDirName)
	if err != nil {
		return nil, fmt.Errorf("buka %s: %w", path, err)
	}
	d := &tlsFiles{path: path, root: root, owner: owner}
	if err := d.fixDir(); err != nil {
		root.Close()
		return nil, err
	}
	return d, nil
}

// fixDir makes the opened directory 0700 and owned by d.owner, through its
// descriptor.
func (d *tlsFiles) fixDir() error {
	f, err := d.root.Open(".")
	if err != nil {
		return fmt.Errorf("buka %s: %w", d.path, err)
	}
	defer f.Close()
	if fi, err := f.Stat(); err != nil || !fi.IsDir() {
		return fmt.Errorf("%s bukan direktori", d.path)
	}
	if err := f.Chmod(0o700); err != nil {
		return fmt.Errorf("atur izin %s: %w", d.path, err)
	}
	if !d.owner.mine() {
		if err := f.Chown(d.owner.uid, d.owner.gid); err != nil {
			return fmt.Errorf("atur pemilik %s: %w", d.path, err)
		}
	}
	return nil
}

func (d *tlsFiles) Close() error { return d.root.Close() }

// join is the path of name in the directory, for messages and MySQL.
func (d *tlsFiles) join(name string) string { return filepath.Join(d.path, name) }

// write replaces name with data: a new temporary file (0600, owned by
// d.owner) renamed over it.
func (d *tlsFiles) write(name string, data []byte) error {
	fail := func(err error) error { return fmt.Errorf("tulis %s: %w", d.join(name), err) }
	var rnd [8]byte
	rand.Read(rnd[:]) // never fails (crypto/rand)
	tmp := "." + name + ".tmp-" + hex.EncodeToString(rnd[:])
	f, err := d.root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fail(err)
	}
	defer d.root.Remove(tmp) // no-op after the rename
	err = f.Chmod(0o600)
	if err == nil && !d.owner.mine() {
		err = f.Chown(d.owner.uid, d.owner.gid)
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
		err = d.root.Rename(tmp, name)
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
	d, err := openTLSDir(l)
	if err != nil {
		return res, err
	}
	defer d.Close()
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
	if err := d.write(tlsKeyFile+tlsNew, keyPEM); err != nil {
		return res, err
	}
	res.CSRPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
	return res, nil
}

// trustTLS stores caPEM as TLSDir(l)/mysql-ca.crt and returns its path.
// The same CA again changes nothing.
func trustTLS(l Layout, caPEM []byte) (string, error) {
	d, err := openTLSDir(l)
	if err != nil {
		return "", err
	}
	defer d.Close()
	path := d.join(tlsCAFile)
	if old, err := d.root.ReadFile(tlsCAFile); err == nil && bytes.Equal(old, caPEM) {
		return path, nil
	}
	return path, d.write(tlsCAFile, caPEM)
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
	d    *tlsFiles
	done []rotated
}

type rotated struct {
	name    string
	hadPrev bool // name existed and was moved to name.prev
}

// swap moves name to name.prev (when it exists) and name.new to name.
func (r *rotation) swap(name string) error {
	root, path := r.d.root, r.d.join(name)
	hadPrev := false
	if _, err := root.Lstat(name); err == nil {
		if err := root.Rename(name, name+tlsPrev); err != nil {
			return fmt.Errorf("simpan %s lama: %w", path, err)
		}
		hadPrev = true
	}
	if err := root.Rename(name+tlsNew, name); err != nil {
		if hadPrev {
			_ = root.Rename(name+tlsPrev, name)
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
		name := r.done[i].name
		err := r.d.root.Rename(name, name+tlsNew)
		if err == nil && r.done[i].hadPrev {
			err = r.d.root.Rename(name+tlsPrev, name)
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
	d, err := openTLSDir(l)
	if err != nil {
		return res, err
	}
	defer d.Close()
	keyName := tlsKeyFile + tlsNew
	keyPEM, err := d.root.ReadFile(keyName)
	if errors.Is(err, os.ErrNotExist) {
		keyName = tlsKeyFile
		keyPEM, err = d.root.ReadFile(keyName)
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
	installed := tlsVars{
		ca:   sql.NullString{String: d.join(tlsCAFile), Valid: true},
		cert: sql.NullString{String: d.join(tlsCertFile), Valid: true},
		key:  sql.NullString{String: d.join(tlsKeyFile), Valid: true},
	}
	if err := refuseClientCertAccounts(ctx, db, old, installed.ca.String); err != nil {
		return res, err
	}

	if err := d.write(tlsCertFile+tlsNew, in.cert.pem); err != nil {
		return res, err
	}
	if err := d.write(tlsCAFile+tlsNew, in.ca.pem); err != nil {
		d.root.Remove(tlsCertFile + tlsNew)
		return res, err
	}
	rot := &rotation{d: d}
	names := []string{tlsCertFile, tlsCAFile}
	if keyName != tlsKeyFile {
		names = append(names, tlsKeyFile)
	}

	var served string
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
		serial, err := checkServedCert(ctx, l, creds, in)
		if err != nil {
			return err
		}
		served = serial
		return setTLSVars(ctx, db, "PERSIST", installed)
	}()
	if err != nil {
		return res, rollbackTLS(context.WithoutCancel(ctx), db, rot, old, err)
	}
	// The certificate and CA are in place; nothing is left to undo. The
	// serial reported is the one MySQL was seen serving (SEC-63c), which
	// matchServedCert found equal to the certificate the panel sent.
	res.Serial = served
	res.NotAfter = in.cert.cert.NotAfter
	res.Reloaded = true
	return res, nil
}

// refuseClientCertAccounts refuses an install that would change MySQL's
// ssl_ca while accounts log in with client certificates (REQUIRE X509,
// ISSUER or SUBJECT): MySQL checks those against ssl_ca, and the panel's CA
// in its place would lock the applications out (I7). An unset ssl_ca, or
// the panel's own from an earlier install, is no change.
func refuseClientCertAccounts(ctx context.Context, db *sql.DB, old tlsVars, newCA string) error {
	if !old.ca.Valid || old.ca.String == "" || old.ca.String == newCA {
		return nil
	}
	rows, err := db.QueryContext(ctx, `SELECT user, host FROM mysql.user WHERE ssl_type IN ('X509', 'SPECIFIED')
		ORDER BY user, host`)
	if err != nil {
		return fmt.Errorf("baca akun bersertifikat klien: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var user, host string
		if err := rows.Scan(&user, &host); err != nil {
			return fmt.Errorf("baca akun bersertifikat klien: %w", err)
		}
		names = append(names, quoteString(user)+"@"+quoteString(host))
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("baca akun bersertifikat klien: %w", err)
	}
	if len(names) == 0 {
		return nil
	}
	return &commands.ParamError{Msg: fmt.Sprintf("akun %s memakai sertifikat klien yang diperiksa MySQL dengan ssl_ca "+
		"saat ini (%s); sertifikat MySQL panel mengganti ssl_ca sehingga aplikasi itu tidak bisa masuk lagi. "+
		"Sertifikat MySQL panel tidak dipasang di server ini (lihat README, Sertifikat klien MySQL)",
		strings.Join(names, ", "), old.ca.String)}
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
		rot.d.root.Remove(name) // the key stays for another try
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

// matchServedCert checks the certificate MySQL served (peer, leaf first)
// against want, the certificate the panel sent (SEC-63c): the same serial
// number, then the same bytes. It returns the served serial as installTLS
// reports it (32 hex digits).
func matchServedCert(peer []*x509.Certificate, want *x509.Certificate) (string, error) {
	if len(peer) == 0 {
		return "", errors.New("MySQL tidak menyajikan sertifikat")
	}
	got := peer[0]
	serial := fmt.Sprintf("%032x", got.SerialNumber)
	if got.SerialNumber.Cmp(want.SerialNumber) != 0 {
		return "", fmt.Errorf("MySQL menyajikan sertifikat bernomor seri %s, bukan %032x yang dikirim panel", serial, want.SerialNumber)
	}
	if !got.Equal(want) {
		return "", fmt.Errorf("MySQL menyajikan sertifikat bernomor seri %s yang isinya berbeda dari yang dikirim panel", serial)
	}
	return serial, nil
}

// checkServedCert connects to the server as installTLS describes and
// checks that it serves in's certificate (matchServedCert), valid for
// in.verifyHost under in's CA. It returns the served serial.
func checkServedCert(ctx context.Context, l Layout, creds CredentialStore, in tlsInstall) (string, error) {
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
	served := ""
	c.TLS = &tls.Config{
		RootCAs:    pool,
		ServerName: in.verifyHost,
		MinVersion: tls.VersionTLS12,
		// Runs after the chain and the name are verified.
		VerifyConnection: func(cs tls.ConnectionState) error {
			serial, err := matchServedCert(cs.PeerCertificates, in.cert.cert)
			if err != nil {
				return err
			}
			served = serial
			return nil
		},
	}
	conn, err := drv.NewConnector(c)
	if err != nil {
		return "", err
	}
	db := sql.OpenDB(conn)
	defer db.Close()
	cctx, cancel := context.WithTimeout(ctx, tlsCheckTimeout)
	defer cancel()
	err = db.PingContext(cctx)
	if served != "" && (err == nil || LoginRefused(err)) {
		return served, nil
	}
	if err == nil {
		err = errors.New("sambungan tidak memakai TLS")
	}
	return "", fmt.Errorf("sambungan baru dengan verifikasi sertifikat untuk %s gagal: %w", in.verifyHost, err)
}
