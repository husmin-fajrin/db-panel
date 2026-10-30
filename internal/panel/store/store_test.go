package store

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "panel.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestOpenRunsMigrationsIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.db")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if _, err := s.CreateServer("a"); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s.Close()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("schema_migrations rows = %d, want 2", n)
	}
	list, err := s.ListServers()
	if err != nil || len(list) != 1 {
		t.Fatalf("data lost after reopen: %v %v", list, err)
	}
}

func TestServerCRUD(t *testing.T) {
	s := openTemp(t)

	if _, err := s.CreateServer("   "); err == nil || err.Error() != "nama server tidak boleh kosong" {
		t.Fatalf("empty name err = %v", err)
	}

	b, err := s.CreateServer("beta")
	if err != nil {
		t.Fatal(err)
	}
	a, err := s.CreateServer("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID == 0 || a.Role != RoleUnassigned || a.Paired || a.LastSeenAt != nil || a.CreatedAt.Location() != time.UTC {
		t.Fatalf("unexpected new server: %+v", a)
	}

	list, err := s.ListServers()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].Name != "alpha" || list[1].Name != "beta" {
		t.Fatalf("list order: %+v", list)
	}

	if err := s.MarkPaired(a.ID, "ABC123", "10.0.0.5:9443"); err != nil {
		t.Fatal(err)
	}
	seen := time.Date(2026, 10, 30, 12, 0, 0, 0, time.FixedZone("WIB", 7*3600))
	probe := proto.ProbeResult{Hostname: "h1", CPUCores: 4}
	if err := s.UpdateProbe(a.ID, probe, "v1.2.3", seen); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetServer(a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Paired || got.CertSerial != "ABC123" || got.Address != "10.0.0.5:9443" || got.AgentVersion != "v1.2.3" {
		t.Fatalf("got %+v", got)
	}
	if got.LastSeenAt == nil || !got.LastSeenAt.Equal(seen) || got.LastSeenAt.Location() != time.UTC {
		t.Fatalf("LastSeenAt = %v", got.LastSeenAt)
	}
	var back proto.ProbeResult
	if err := json.Unmarshal([]byte(got.ProbeJSON), &back); err != nil || back.Hostname != "h1" || back.CPUCores != 4 {
		t.Fatalf("probe json %q: %v", got.ProbeJSON, err)
	}

	later := seen.Add(time.Minute)
	if err := s.TouchSeen(a.ID, later); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetServer(a.ID)
	if !got.LastSeenAt.Equal(later) || got.ProbeJSON == "" {
		t.Fatalf("TouchSeen: %+v", got)
	}

	if err := s.DeleteServer(b.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetServer(b.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetServer deleted: %v", err)
	}

	for name, err := range map[string]error{
		"delete":    s.DeleteServer(999),
		"markpair":  s.MarkPaired(999, "x", "y"),
		"probe":     s.UpdateProbe(999, proto.ProbeResult{}, "v", seen),
		"touchseen": s.TouchSeen(999, seen),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s on missing id: %v", name, err)
		}
	}
	if _, err := s.GetServer(999); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetServer missing: %v", err)
	}
}

func TestCreateServerRejectsDuplicateName(t *testing.T) {
	s := openTemp(t)
	if _, err := s.CreateServer("db1"); err != nil {
		t.Fatal(err)
	}
	_, err := s.CreateServer("db1")
	if err == nil || err.Error() != "nama server sudah dipakai" {
		t.Fatalf("err = %v", err)
	}
}

var tokenRe = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{5}-[0-9A-HJKMNP-TV-Z]{5}-[0-9A-HJKMNP-TV-Z]{5}$`)

func TestPairTokenLifecycle(t *testing.T) {
	s := openTemp(t)
	srv, _ := s.CreateServer("db1")
	now := time.Date(2026, 10, 30, 10, 0, 0, 500_000_000, time.UTC)

	tok, err := s.CreatePairToken(srv.ID, now, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if !tokenRe.MatchString(tok.Token) {
		t.Fatalf("token format: %q", tok.Token)
	}
	if want := now.Add(15 * time.Minute).Truncate(time.Second); tok.ServerID != srv.ID || !tok.ExpiresAt.Equal(want) || tok.UsedAt != nil {
		t.Fatalf("token: %+v", tok)
	}

	act, ok, err := s.ActivePairToken(srv.ID, now)
	if err != nil || !ok || act.Token != tok.Token {
		t.Fatalf("active: %+v %v %v", act, ok, err)
	}
	if !act.ExpiresAt.Equal(tok.ExpiresAt) {
		t.Fatalf("ExpiresAt read back %v, created %v", act.ExpiresAt, tok.ExpiresAt)
	}
	if _, err := s.PeekPairToken(tok.Token, now); err != nil {
		t.Fatalf("peek: %v", err)
	}
	if _, err := s.PeekPairToken(tok.Token, now); err != nil {
		t.Fatalf("peek must not consume: %v", err)
	}

	got, err := s.ConsumePairToken(tok.Token, now)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	if got.ServerID != srv.ID || got.UsedAt == nil || !got.UsedAt.Equal(now.Truncate(time.Second)) {
		t.Fatalf("consumed: %+v", got)
	}
	if _, err := s.ConsumePairToken(tok.Token, now); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("second consume: %v", err)
	}
	if _, err := s.PeekPairToken(tok.Token, now); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("peek after consume: %v", err)
	}
	if _, ok, _ := s.ActivePairToken(srv.ID, now); ok {
		t.Fatal("no active token expected after consume")
	}
	if _, err := s.ConsumePairToken("NOPE0-NOPE0-NOPE0", now); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("unknown token: %v", err)
	}
}

func TestPairTokenExpires(t *testing.T) {
	s := openTemp(t)
	srv, _ := s.CreateServer("db1")
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	tok, _ := s.CreatePairToken(srv.ID, now, time.Minute)

	after := now.Add(2 * time.Minute)
	if _, err := s.PeekPairToken(tok.Token, after); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("peek expired: %v", err)
	}
	if _, err := s.ConsumePairToken(tok.Token, after); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("consume expired: %v", err)
	}
	if _, ok, _ := s.ActivePairToken(srv.ID, after); ok {
		t.Fatal("expired token must not be active")
	}
}

func TestCreatePairTokenReplacesOld(t *testing.T) {
	s := openTemp(t)
	srv, _ := s.CreateServer("db1")
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	old, _ := s.CreatePairToken(srv.ID, now, time.Hour)
	fresh, err := s.CreatePairToken(srv.ID, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if old.Token == fresh.Token {
		t.Fatal("tokens must differ")
	}
	if _, err := s.ConsumePairToken(old.Token, now); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("old token still valid: %v", err)
	}
	if _, err := s.ConsumePairToken(fresh.Token, now); err != nil {
		t.Fatalf("fresh token: %v", err)
	}
}

func TestMarkPairedDeletesUnusedTokens(t *testing.T) {
	s := openTemp(t)
	srv, _ := s.CreateServer("db1")
	other, _ := s.CreateServer("db2")
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	used, _ := s.CreatePairToken(srv.ID, now, time.Hour)
	if _, err := s.ConsumePairToken(used.Token, now); err != nil {
		t.Fatal(err)
	}
	pending, _ := s.CreatePairToken(srv.ID, now, time.Hour)
	otherTok, _ := s.CreatePairToken(other.ID, now, time.Hour)

	if err := s.MarkPaired(srv.ID, "local", "127.0.0.1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PeekPairToken(pending.Token, now); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("unused token survived MarkPaired: %v", err)
	}
	if _, ok, _ := s.ActivePairToken(srv.ID, now); ok {
		t.Fatal("server still has an active token after MarkPaired")
	}
	if _, err := s.PeekPairToken(otherTok.Token, now); err != nil {
		t.Fatalf("another server's token was deleted: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pair_tokens WHERE token_hash = ?`, hashToken(used.Token)).Scan(&n); err != nil || n != 1 {
		t.Fatalf("used token rows = %d err=%v, want it kept", n, err)
	}
}

func TestCreatePairTokenMissingServer(t *testing.T) {
	s := openTemp(t)
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	if _, err := s.CreatePairToken(999, now, time.Hour); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

func TestDeleteServerCascadesTokens(t *testing.T) {
	s := openTemp(t)
	srv, _ := s.CreateServer("db1")
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	tok, _ := s.CreatePairToken(srv.ID, now, time.Hour)
	if err := s.DeleteServer(srv.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PeekPairToken(tok.Token, now); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("peek after delete: %v", err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pair_tokens`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("pair_tokens rows = %d err=%v", n, err)
	}
}

func TestUsersAndSettings(t *testing.T) {
	s := openTemp(t)

	if n, err := s.CountUsers(); err != nil || n != 0 {
		t.Fatalf("CountUsers = %d %v", n, err)
	}
	if err := s.CreateUser("admin", "hash1"); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateUser("admin", "hash2"); err == nil || err.Error() != "nama pengguna sudah ada" {
		t.Fatalf("dup user err = %v", err)
	}
	u, err := s.GetUser("admin")
	if err != nil || u.ID == 0 || u.Username != "admin" || u.PasswordHash != "hash1" {
		t.Fatalf("GetUser: %+v %v", u, err)
	}
	if _, err := s.GetUser("ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetUser missing: %v", err)
	}
	if err := s.SetPasswordHash("admin", "hash3"); err != nil {
		t.Fatal(err)
	}
	u, _ = s.GetUser("admin")
	if u.PasswordHash != "hash3" {
		t.Fatalf("hash = %q", u.PasswordHash)
	}
	if err := s.SetPasswordHash("ghost", "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetPasswordHash missing: %v", err)
	}
	if n, _ := s.CountUsers(); n != 1 {
		t.Fatalf("CountUsers = %d", n)
	}

	if v, err := s.GetSetting("k"); err != nil || v != "" {
		t.Fatalf("missing setting = %q %v", v, err)
	}
	if err := s.SetSetting("k", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSetting("k", "v2"); err != nil {
		t.Fatal(err)
	}
	if v, _ := s.GetSetting("k"); v != "v2" {
		t.Fatalf("setting = %q", v)
	}
}

func TestSessionEpoch(t *testing.T) {
	s := openTemp(t)
	if e, err := s.SessionEpoch(); err != nil || e != 0 {
		t.Fatalf("initial epoch = %d %v, want 0", e, err)
	}
	now := time.Unix(1_800_000_000, 500_000_000)
	if err := s.BumpSessionEpoch(now); err != nil {
		t.Fatal(err)
	}
	if e, err := s.SessionEpoch(); err != nil || e != 1_800_000_000 {
		t.Fatalf("epoch = %d %v, want 1800000000", e, err)
	}
}

// TestCreateServerNamePolicy (SEC-16).
func TestCreateServerNamePolicy(t *testing.T) {
	s := openTemp(t)
	for _, ok := range []string{"db-utama", "DB Replika 2", "srv_01.rs", strings.Repeat("a", 64), "  spasi-tepi  "} {
		if _, err := s.CreateServer(ok); err != nil {
			t.Errorf("CreateServer(%q): %v", ok, err)
		}
	}
	for _, bad := range []string{
		strings.Repeat("a", 65), "db\nbaru", "db\x1b[31m", "evil‮exe", "db/../x", "db;rm", "dbé",
	} {
		_, err := s.CreateServer(bad)
		if !errors.Is(err, ErrServerName) || err.Error() != "nama server hanya boleh huruf, angka, spasi, titik, garis bawah dan strip (maks. 64)" {
			t.Errorf("CreateServer(%q) err = %v", bad, err)
		}
	}
	if got, _ := s.GetServer(5); got.Name != "spasi-tepi" {
		t.Errorf("name not trimmed: %q", got.Name)
	}
}

// TestPairTokenStoredAsHash (SEC-13): the database holds only SHA-256 of
// each token; the plaintext lives in the creating process only.
func TestPairTokenStoredAsHash(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	srv, _ := s.CreateServer("db1")
	now := time.Now()
	tok, err := s.CreatePairToken(srv.ID, now, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := s.db.QueryRow(`SELECT token_hash FROM pair_tokens WHERE server_id = ?`, srv.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(tok.Token))
	if stored != hex.EncodeToString(sum[:]) {
		t.Fatalf("stored %q, want sha256 of the token", stored)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if wal, err := os.ReadFile(path + "-wal"); err == nil {
		raw = append(raw, wal...)
	}
	if bytes.Contains(raw, []byte(tok.Token)) {
		t.Fatal("plaintext token found in the database files")
	}

	// A new process (panel restart) still accepts the token but cannot show it.
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	act, ok, err := s.ActivePairToken(srv.ID, now)
	if err != nil || !ok || act.Token != "" {
		t.Fatalf("active after reopen = %+v %v %v, want found with Token \"\"", act, ok, err)
	}
	if pt, err := s.PeekPairToken(tok.Token, now); err != nil || pt.Token != tok.Token || pt.ServerID != srv.ID {
		t.Fatalf("peek after reopen = %+v %v", pt, err)
	}
	if _, err := s.ConsumePairToken(tok.Token, now); err != nil {
		t.Fatalf("consume after reopen: %v", err)
	}
}

// TestMigrationDropsPlaintextTokens: upgrading a database from schema 1
// removes the plaintext tokens it holds.
func TestMigrationDropsPlaintextTokens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	v1, err := migrationsFS.ReadFile("migrations/0001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		string(v1),
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY)`,
		`INSERT INTO schema_migrations (version) VALUES (1)`,
		`INSERT INTO servers (name, created_at) VALUES ('db1', '2026-10-30T10:00:00Z')`,
		`INSERT INTO pair_tokens (token, server_id, expires_at) VALUES ('PLAIN-TOKEN-AAAAA', 1, '2099-01-01T00:00:00Z')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pair_tokens`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("pair_tokens after upgrade = %d %v, want 0", n, err)
	}
	if _, err := s.PeekPairToken("PLAIN-TOKEN-AAAAA", time.Now()); !errors.Is(err, ErrTokenInvalid) {
		t.Fatalf("old plaintext token after upgrade: %v", err)
	}
	if _, err := s.CreatePairToken(1, time.Now(), time.Hour); err != nil {
		t.Fatalf("create token after upgrade: %v", err)
	}
}

// TestOpenFileModes (SEC-13): panel.db and its -wal/-shm are 0600 whatever
// the umask, and Open tightens files left with a looser mode.
func TestOpenFileModes(t *testing.T) {
	old := syscall.Umask(0o022)
	defer syscall.Umask(old)
	dir := t.TempDir()
	path := filepath.Join(dir, "panel.db")
	check := func(when string) {
		t.Helper()
		for _, p := range []string{path, path + "-wal", path + "-shm"} {
			fi, err := os.Stat(p)
			if err != nil {
				t.Fatalf("%s: %v", when, err)
			}
			if m := fi.Mode().Perm(); m != 0o600 {
				t.Errorf("%s: %s mode %o, want 600", when, filepath.Base(p), m)
			}
		}
	}
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateServer("db1"); err != nil {
		t.Fatal(err)
	}
	check("new database")
	s.Close()

	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		os.Chmod(p, 0o644)
	}
	s, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.ListServers(); err != nil {
		t.Fatal(err)
	}
	check("reopened 0644 database")
}

// TestUpdateProbeTooLarge (SEC-11): a probe result over 64 KiB is refused
// and the previous one kept.
func TestUpdateProbeTooLarge(t *testing.T) {
	s := openTemp(t)
	srv, _ := s.CreateServer("db1")
	small := proto.ProbeResult{Hostname: "kecil"}
	if err := s.UpdateProbe(srv.ID, small, "v1", time.Now()); err != nil {
		t.Fatal(err)
	}
	big := proto.ProbeResult{Hostname: strings.Repeat("x", 64<<10)}
	if err := s.UpdateProbe(srv.ID, big, "v2", time.Now()); !errors.Is(err, ErrProbeTooLarge) {
		t.Fatalf("UpdateProbe(big) err = %v, want ErrProbeTooLarge", err)
	}
	got, _ := s.GetServer(srv.ID)
	if !strings.Contains(got.ProbeJSON, `"kecil"`) || got.AgentVersion != "v1" {
		t.Fatalf("server after refused probe = %+v, want the previous probe", got)
	}
}
