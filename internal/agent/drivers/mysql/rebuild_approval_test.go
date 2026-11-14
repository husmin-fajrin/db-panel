package mysql_test

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
)

func TestRebuildCodeSixDigits(t *testing.T) {
	dir := pinDir(t, "quarantined", "db-b:3306")
	p := mustPins(t, dir)
	re := regexp.MustCompile(`^[0-9]{6}$`)
	seen := map[string]bool{}
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 200; i++ {
		// Each call is past the previous request's TTL, so it makes a new code.
		r, err := p.EnsureRebuildRequest(now.Add(time.Duration(i) * mysql.RebuildRequestTTL))
		if err != nil {
			t.Fatal(err)
		}
		if !re.MatchString(r.Code) {
			t.Fatalf("code %q is not six digits", r.Code)
		}
		seen[r.Code] = true
	}
	if len(seen) < 2 {
		t.Errorf("200 codes were all %v", seen)
	}
}

func jsonKeys(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func wantMode0644(t *testing.T, path string) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("%s mode = %04o, want 0644", path, fi.Mode().Perm())
	}
}

func TestRebuildRequestKeptWhileValid(t *testing.T) {
	dir := pinDir(t, "quarantined", "db-b:3306")
	p := mustPins(t, dir)
	t0 := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	r1, err := p.EnsureRebuildRequest(t0)
	if err != nil {
		t.Fatal(err)
	}
	if r1.Source != "db-b:3306" || !r1.Created.Equal(t0) {
		t.Fatalf("r1 = %+v", r1)
	}
	r2, err := p.EnsureRebuildRequest(t0.Add(29 * time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if r2.Code != r1.Code || !r2.Created.Equal(t0) {
		t.Errorf("r2 = %+v, want the request of %+v", r2, r1)
	}
	r3, err := p.EnsureRebuildRequest(t0.Add(30 * time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !r3.Created.Equal(t0.Add(30 * time.Minute)) {
		t.Errorf("r3.Created = %v, want a new request at t0+30m", r3.Created)
	}
	path := filepath.Join(dir, mysql.RebuildRequestFile)
	wantMode0644(t, path)
	if got := jsonKeys(t, path); !reflect.DeepEqual(got, []string{"code", "created", "source"}) {
		t.Errorf("keys = %v", got)
	}
	if err := p.SetSource("db-c", 3306); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := p.ValidRebuildRequest(t0.Add(31 * time.Minute)); err != nil || ok {
		t.Errorf("ValidRebuildRequest after SetSource = %v, %v; want not valid", ok, err)
	}
	r4, err := p.EnsureRebuildRequest(t0.Add(31 * time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if r4.Source != "db-c:3306" {
		t.Errorf("r4.Source = %q, want db-c:3306", r4.Source)
	}
}

func TestRebuildRequestNeedsQuarantinedSource(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	for name, dir := range map[string]string{
		"replica":                pinDir(t, "replica", "db-b:3306"),
		"quarantined, no source": pinDir(t, "quarantined", ""),
	} {
		p := mustPins(t, dir)
		if _, err := p.EnsureRebuildRequest(now); err == nil {
			t.Errorf("%s: EnsureRebuildRequest = nil error", name)
		}
		if _, err := os.Stat(filepath.Join(dir, mysql.RebuildRequestFile)); !os.IsNotExist(err) {
			t.Errorf("%s: request file written (stat err %v)", name, err)
		}
	}
}

func TestRebuildApprovalValidity(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	base := mysql.RebuildApproval{Source: "db-b:3306", Addr: "10.0.0.5", Approved: now.Add(-time.Hour), Expires: now.Add(23 * time.Hour)}
	ips := []net.IP{net.ParseIP("10.0.0.5")}
	with := func(f func(*mysql.RebuildApproval)) mysql.RebuildApproval {
		a := base
		f(&a)
		return a
	}
	cases := []struct {
		name   string
		a      mysql.RebuildApproval
		role   string
		source string
		ips    []net.IP
		now    time.Time
		want   string // "" means valid
	}{
		{"valid", base, "quarantined", "db-b:3306", ips, now, ""},
		{"source case", base, "quarantined", "DB-B:3306", ips, now, ""},
		{"role", base, "replica", "db-b:3306", ips, now, "tidak dikarantina"},
		{"other source", base, "quarantined", "db-c:3306", ips, now, "db-c:3306"},
		{"other address", base, "quarantined", "db-b:3306", []net.IP{net.ParseIP("10.0.0.6")}, now, "10.0.0.5"},
		{"mixed answer", base, "quarantined", "db-b:3306", []net.IP{net.ParseIP("10.0.0.5"), net.ParseIP("10.0.0.6")}, now,
			"mengarah ke alamat lain"},
		{"no address", base, "quarantined", "db-b:3306", nil, now, "mengarah ke alamat lain"},
		{"same address twice", base, "quarantined", "db-b:3306", []net.IP{net.ParseIP("10.0.0.5"), net.ParseIP("10.0.0.5")}, now, ""},
		{"expired", base, "quarantined", "db-b:3306", ips, base.Expires, "sudah habis"},
		{"future edge", with(func(a *mysql.RebuildApproval) {
			a.Approved = now.Add(5 * time.Minute)
			a.Expires = a.Approved.Add(24 * time.Hour)
		}), "quarantined", "db-b:3306", ips, now, ""},
		{"future", with(func(a *mysql.RebuildApproval) {
			a.Approved = now.Add(5*time.Minute + time.Second)
			a.Expires = a.Approved.Add(24 * time.Hour)
		}), "quarantined", "db-b:3306", ips, now, "di masa depan"},
		{"too long", with(func(a *mysql.RebuildApproval) { a.Expires = a.Approved.Add(24*time.Hour + time.Second) }),
			"quarantined", "db-b:3306", ips, now, "lebih dari 24 jam"},
		{"expires before approved", with(func(a *mysql.RebuildApproval) { a.Expires = a.Approved.Add(-time.Second) }),
			"quarantined", "db-b:3306", ips, now,
			"berkas persetujuan tidak bisa dipakai: waktu habis lebih awal dari waktu persetujuan"},
	}
	codes := map[string]string{
		"role": "role", "other source": "source", "other address": "addr", "mixed answer": "addr", "no address": "addr",
		"expired": "expired", "future": "future", "too long": "lifetime", "expires before approved": "lifetime",
	}
	for _, c := range cases {
		err := mysql.CheckApproval(c.a, c.role, c.source, c.ips, c.now)
		if got := mysql.InvalidCode(err); got != codes[c.name] {
			t.Errorf("%s: code %q, want %q", c.name, got, codes[c.name])
		}
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v, want valid", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: %v, want an error containing %q", c.name, err, c.want)
		}
	}
}

func TestRebuildFilesTrusted(t *testing.T) {
	dir := pinDir(t, "quarantined", "db-b:3306")
	p := mustPins(t, dir)
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	a := mysql.NewRebuildApproval("db-b:3306", "10.0.0.5", now)
	if !a.Expires.Equal(now.Add(mysql.RebuildApprovalTTL)) {
		t.Fatalf("Expires = %v", a.Expires)
	}
	if err := p.WriteRebuildApproval(a); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, mysql.RebuildApprovalFile)
	wantMode0644(t, path)
	if got := jsonKeys(t, path); !reflect.DeepEqual(got, []string{"addr", "approved", "expires", "source"}) {
		t.Errorf("keys = %v", got)
	}
	got, ok, err := p.ReadRebuildApproval()
	if err != nil || !ok || got.Source != a.Source || got.Addr != a.Addr || !got.Expires.Equal(a.Expires) {
		t.Fatalf("ReadRebuildApproval = %+v, %v, %v", got, ok, err)
	}

	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.ReadRebuildApproval(); err == nil || !strings.Contains(err.Error(), "chmod 0644") {
		t.Errorf("group-writable: err = %v, want chmod 0644 hint", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "elsewhere")
	if err := os.WriteFile(target, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.ReadRebuildApproval(); err == nil || !strings.Contains(err.Error(), "harus berkas biasa") {
		t.Errorf("symlink: err = %v, want harus berkas biasa", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}

	if _, err := p.EnsureRebuildRequest(now); err != nil {
		t.Fatal(err)
	}
	if err := p.WriteRebuildApproval(a); err != nil {
		t.Fatal(err)
	}
	if err := p.ClearRebuild(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{mysql.RebuildRequestFile, mysql.RebuildApprovalFile} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s still there after ClearRebuild (%v)", name, err)
		}
	}
	if err := p.ClearRebuild(); err != nil {
		t.Errorf("ClearRebuild with none = %v, want nil", err)
	}
}

func TestRebuildApprovalAddr(t *testing.T) {
	ctx := context.Background()
	p := mustPins(t, pinDir(t, "quarantined", "10.0.0.5:3306"))
	looked := false
	addr, err := p.RebuildApprovalAddr(ctx, func(context.Context, string) ([]net.IP, error) {
		looked = true
		return nil, nil
	})
	if err != nil || addr != "10.0.0.5" || looked {
		t.Errorf("IP source = %q, %v, looked up %v", addr, err, looked)
	}

	dir := pinDir(t, "quarantined", "db-b:3306")
	p = mustPins(t, dir)
	answer := []net.IP{net.ParseIP("10.0.0.6")}
	lookup := func(context.Context, string) ([]net.IP, error) { return answer, nil }
	if addr, err = p.RebuildApprovalAddr(ctx, lookup); err != nil || addr != "10.0.0.6" {
		t.Errorf("one address = %q, %v", addr, err)
	}
	answer = []net.IP{net.ParseIP("10.0.0.6"), net.ParseIP("10.0.0.6")}
	if addr, err = p.RebuildApprovalAddr(ctx, lookup); err != nil || addr != "10.0.0.6" {
		t.Errorf("one address twice = %q, %v", addr, err)
	}
	answer = []net.IP{net.ParseIP("fd00::5"), net.ParseIP("10.0.0.7"), net.ParseIP("10.0.0.6")}
	_, err = p.RebuildApprovalAddr(ctx, lookup)
	b16 := "Nama sumber db-b:3306 mengarah ke lebih dari satu alamat (10.0.0.6, 10.0.0.7, fd00::5). Persetujuan hanya bisa " +
		"diberikan bila nama itu mengarah ke satu alamat. Tetapkan satu alamat untuk nama itu di server ini (misalnya di " +
		"/etc/hosts), lalu jalankan perintah approve ini lagi. Persetujuan tidak ditulis."
	if err == nil || err.Error() != b16 {
		t.Errorf("three addresses: %v, want B16", err)
	}

	// A recorded (verified) address: the name is still looked up.
	if err := os.WriteFile(filepath.Join(dir, mysql.SourceAddrFile), []byte("10.0.0.9\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	answer = []net.IP{net.ParseIP("10.0.0.9")}
	if addr, err = p.RebuildApprovalAddr(ctx, lookup); err != nil || addr != "10.0.0.9" {
		t.Errorf("recorded address resolved = %q, %v", addr, err)
	}
	answer = []net.IP{net.ParseIP("10.0.0.6")}
	_, err = p.RebuildApprovalAddr(ctx, lookup)
	b15 := "Nama sumber db-b:3306 sekarang mengarah ke 10.0.0.6, bukan ke alamat yang dicatat saat karantina (10.0.0.9). " +
		"Bila alamat baru itu memang master baru, jalankan: sudo dbpanel agent set-source db-b:3306, lalu jalankan perintah " +
		"approve ini lagi. Persetujuan tidak ditulis."
	if err == nil || err.Error() != b15 {
		t.Errorf("name moved: %v, want B15", err)
	}
	answer = []net.IP{net.ParseIP("10.0.0.9"), net.ParseIP("10.0.0.6")}
	if _, err = p.RebuildApprovalAddr(ctx, lookup); err == nil || !strings.HasPrefix(err.Error(), "Nama sumber db-b:3306 mengarah ke lebih dari satu alamat (10.0.0.6, 10.0.0.9).") {
		t.Errorf("recorded among two: %v, want B16", err)
	}
}

func TestRebuildApprovalAddrDNSNamesHostPort(t *testing.T) {
	p := mustPins(t, pinDir(t, "quarantined", "db-b:3306"))
	fail := func(context.Context, string) ([]net.IP, error) { return nil, errors.New("no such host") }
	if _, err := p.RebuildApprovalAddr(context.Background(), fail); err == nil || !strings.Contains(err.Error(), "sumber db-b:3306 tidak ditemukan (DNS)") {
		t.Errorf("DNS error: err = %v, want the source named as host:port", err)
	}
	none := func(context.Context, string) ([]net.IP, error) { return nil, nil }
	if _, err := p.RebuildApprovalAddr(context.Background(), none); err == nil || !strings.Contains(err.Error(), "sumber db-b:3306 tidak ditemukan (DNS): tidak ada alamat") {
		t.Errorf("no address: err = %v, want the source named as host:port", err)
	}
}

func TestCheckRebuildApproval(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	approval := mysql.NewRebuildApproval("db-b:3306", "10.0.0.5", now.Add(-time.Hour))
	resolve := func(ips []net.IP, err error) func() {
		return mysql.SetResolver(func(context.Context, string) ([]net.IP, error) { return ips, err }, nil)
	}

	t.Run("no file", func(t *testing.T) {
		p := mustPins(t, pinDir(t, "quarantined", "db-b:3306"))
		defer resolve([]net.IP{net.ParseIP("10.0.0.5")}, nil)()
		if _, err := p.CheckRebuildApproval(ctx, now); err == nil || !strings.Contains(err.Error(), "berkas persetujuan tidak bisa dipakai") ||
			mysql.InvalidCode(err) != "file" {
			t.Errorf("err = %v (code %q)", err, mysql.InvalidCode(err))
		}
	})
	t.Run("untrusted file", func(t *testing.T) {
		dir := pinDir(t, "quarantined", "db-b:3306")
		p := mustPins(t, dir)
		if err := p.WriteRebuildApproval(approval); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(filepath.Join(dir, mysql.RebuildApprovalFile), 0o666); err != nil {
			t.Fatal(err)
		}
		defer resolve([]net.IP{net.ParseIP("10.0.0.5")}, nil)()
		if _, err := p.CheckRebuildApproval(ctx, now); err == nil || !strings.Contains(err.Error(), "berkas persetujuan tidak bisa dipakai") ||
			mysql.InvalidCode(err) != "file" {
			t.Errorf("err = %v (code %q)", err, mysql.InvalidCode(err))
		}
	})
	t.Run("DNS error", func(t *testing.T) {
		p := mustPins(t, pinDir(t, "quarantined", "db-b:3306"))
		if err := p.WriteRebuildApproval(approval); err != nil {
			t.Fatal(err)
		}
		defer resolve(nil, errors.New("servfail"))()
		_, err := p.CheckRebuildApproval(ctx, now)
		if err == nil || !strings.Contains(err.Error(), "tidak ditemukan (DNS)") || !strings.Contains(err.Error(), "db-b:3306") ||
			mysql.InvalidCode(err) != "dns" {
			t.Errorf("err = %v (code %q)", err, mysql.InvalidCode(err))
		}
	})
	t.Run("no address and no error", func(t *testing.T) {
		// The same answer RebuildApprovalAddr words as DNS ("tidak ada
		// alamat"), so the panel words it one way.
		p := mustPins(t, pinDir(t, "quarantined", "db-b:3306"))
		if err := p.WriteRebuildApproval(approval); err != nil {
			t.Fatal(err)
		}
		defer resolve(nil, nil)()
		_, err := p.CheckRebuildApproval(ctx, now)
		if err == nil || !strings.Contains(err.Error(), "tidak ada alamat") || mysql.InvalidCode(err) != "dns" {
			t.Errorf("err = %v (code %q)", err, mysql.InvalidCode(err))
		}
	})
	t.Run("approved address", func(t *testing.T) {
		p := mustPins(t, pinDir(t, "quarantined", "db-b:3306"))
		if err := p.WriteRebuildApproval(approval); err != nil {
			t.Fatal(err)
		}
		defer resolve([]net.IP{net.ParseIP("10.0.0.5")}, nil)()
		got, err := p.CheckRebuildApproval(ctx, now)
		if err != nil {
			t.Fatal(err)
		}
		if got.Source != approval.Source || got.Addr != approval.Addr || !got.Expires.Equal(approval.Expires) {
			t.Errorf("approval = %+v, want %+v", got, approval)
		}
	})
}

func TestEnsureRebuildRequestUntrustedFile(t *testing.T) {
	dir := pinDir(t, "quarantined", "db-b:3306")
	p := mustPins(t, dir)
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	if _, err := p.EnsureRebuildRequest(now); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, mysql.RebuildRequestFile)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := p.EnsureRebuildRequest(now.Add(time.Minute)); err == nil {
		t.Error("EnsureRebuildRequest = nil error with an untrusted request file")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Errorf("request file changed: %q -> %q", before, after)
	}
}
