package mysql

import (
	"fmt"
	"strings"
	"testing"
)

// The texts of the A6 refusal (spec 5.8 T8a, T8b), as the brief gives them.
const (
	t8a = "server ini masih punya database yang tidak disalin dari sumber %s; karantina tidak diakhiri. Yang tidak ada di master baru (%s): jalankan di server ini sudo dbpanel agent drop-database <nama> untuk tiap nama, lalu tekan \"Ulangi dari tahap ini\" pada pekerjaan di panel. Yang ada di master baru (%s): mulai lagi Bangun ulang sebagai replika dari panel; panel menghapus dan menyalinnya ulang."
	t8b = "server ini masih punya database yang tidak disalin dari sumber %s; karantina tidak diakhiri. Bila database itu tidak ada di master baru: jalankan di server ini sudo dbpanel agent drop-database <nama> untuk tiap nama, lalu tekan \"Ulangi dari tahap ini\" pada pekerjaan di panel. Bila ada di master baru: mulai lagi Bangun ulang sebagai replika dari panel; panel menghapus dan menyalinnya ulang. Database itu: %s."
	// The two sentences of T8a.
	t8aAbsent  = " Yang tidak ada di master baru ("
	t8aPresent = " Yang ada di master baru ("
)

// TestNotReplacedNamesAtMostTen: A6 names at most ten databases in a
// list, then ", …".
func TestNotReplacedNamesAtMostTen(t *testing.T) {
	var names []string
	for _, c := range "abcdefghijkl" {
		names = append(names, "db_"+string(c))
	}
	msg := (&errNotReplaced{source: "10.0.0.1:3306", absent: names}).Error()
	want := "(db_a, db_b, db_c, db_d, db_e, db_f, db_g, db_h, db_i, db_j, …):"
	if !strings.Contains(msg, want) {
		t.Errorf("message = %q, want it to hold %q", msg, want)
	}
	if one := (&errNotReplaced{source: "s:1", absent: []string{"x"}}).Error(); !strings.Contains(one, "(x):") {
		t.Errorf("one name: %q", one)
	}
}

// TestNotReplacedTextSplitsByNewMaster: after a full dump, the names the
// new master listed go to the "Yang ada" sentence, the others to "Yang
// tidak ada"; a group with no name has no sentence.
func TestNotReplacedTextSplitsByNewMaster(t *testing.T) {
	c := loadCopy{full: true, source: []string{"Simrs", "lab"}}
	got := notReplaced("10.0.0.1:3306", c, []string{"arsip", "simrs", "uji"}).Error()
	if want := fmt.Sprintf(t8a, "10.0.0.1:3306", "arsip, uji", "simrs"); got != want {
		t.Errorf("both groups:\n got %q\nwant %q", got, want)
	}
	head, absent, present := splitT8a(t)
	if got := notReplaced("s:1", c, []string{"arsip"}).Error(); got != fmt.Sprintf(head, "s:1")+fmt.Sprintf(absent, "arsip") {
		t.Errorf("absent only: %q", got)
	}
	if got := notReplaced("s:1", c, []string{"lab"}).Error(); got != fmt.Sprintf(head, "s:1")+fmt.Sprintf(present, "lab") {
		t.Errorf("present only: %q", got)
	}
}

// splitT8a cuts T8a into its head and its two sentences.
func splitT8a(t *testing.T) (head, absent, present string) {
	t.Helper()
	i, j := strings.Index(t8a, t8aAbsent), strings.Index(t8a, t8aPresent)
	if i < 0 || j < i {
		t.Fatal("T8a does not hold its two sentences")
	}
	return t8a[:i], t8a[i:j], t8a[j:]
}

// TestNotReplacedTextNamedDump: after a dump of named databases, T8b with
// the names at the end, after the advice.
func TestNotReplacedTextNamedDump(t *testing.T) {
	c := loadCopy{named: []string{"toko"}, source: []string{"lama"}}
	got := notReplaced("10.0.0.1:3306", c, []string{"kecil", "lama"}).Error()
	if want := fmt.Sprintf(t8b, "10.0.0.1:3306", "kecil, lama"); got != want {
		t.Errorf("text:\n got %q\nwant %q", got, want)
	}
}

// TestNotReplacedTextAdviceSurvivesCap: twenty names of 64 characters
// make a text of at most 800 bytes with both advices whole and the lists
// cut, ending in ", …"; the same for T8b.
func TestNotReplacedTextAdviceSurvivesCap(t *testing.T) {
	var absent, present []string
	for i := range 20 {
		name := fmt.Sprintf("%02d", i) + strings.Repeat("x", 62)
		if i%2 == 0 {
			absent = append(absent, name)
		} else {
			present = append(present, name)
		}
	}
	src := "old-master.rs.example:3306"
	got := (&errNotReplaced{source: src, absent: absent, present: present}).Error()
	if len(got) > 800 {
		t.Errorf("T8a is %d bytes, want at most 800: %q", len(got), got)
	}
	head, a, p := splitT8a(t)
	for _, part := range []string{fmt.Sprintf(head, src), a[strings.Index(a, "):"):], p[strings.Index(p, "):"):]} {
		if !strings.Contains(got, part) {
			t.Errorf("T8a lost %q: %q", part, got)
		}
	}
	if n := strings.Count(got, ", …)"); n != 2 {
		t.Errorf("T8a has %d lists cut with \", …\", want 2: %q", n, got)
	}
	if !strings.Contains(got, "("+absent[0]) || !strings.Contains(got, "("+present[0]) {
		t.Errorf("T8a names no database of a group: %q", got)
	}

	got = (&errNotReplaced{source: src, absent: append(absent, present...), named: true}).Error()
	if len(got) > 800 {
		t.Errorf("T8b is %d bytes, want at most 800: %q", len(got), got)
	}
	advice := t8b[strings.Index(t8b, " Bila database"):strings.Index(t8b, "Database itu: ")]
	if !strings.Contains(got, advice) || !strings.HasSuffix(got, ", ….") {
		t.Errorf("T8b lost its advice or its cut: %q", got)
	}
}

// TestAdviceTexts: every advice text is there, filled, and none is cut.
func TestAdviceTexts(t *testing.T) {
	texts := AdviceTexts()
	if len(texts) != 15 {
		t.Errorf("AdviceTexts has %d texts, want 15", len(texts))
	}
	for _, s := range texts {
		if s == "" || strings.Contains(s, "%") && !strings.Contains(s, "10.0.0.%") || len(s) > 800 {
			t.Errorf("advice text %q", s)
		}
	}
}

// TestNotCopied: a dump's copy accounts only for databases created by its
// load, among those it named; a CLONE accounts for everything.
func TestNotCopied(t *testing.T) {
	present := []string{"baru", "Lama", "lain"}
	cases := []struct {
		name string
		c    loadCopy
		want string
	}{
		{"clone", loadCopy{whole: true, before: []string{"lama"}}, ""},
		{"all databases", loadCopy{before: []string{"lama"}}, "Lama"},
		{"named", loadCopy{before: nil, named: []string{"BARU"}}, "Lama,lain"},
		{"named and before", loadCopy{before: []string{"baru"}, named: []string{"baru", "lama", "lain"}}, "baru"},
	}
	for _, c := range cases {
		if got := strings.Join(c.c.notCopied(present), ","); got != c.want {
			t.Errorf("%s: notCopied = %q, want %q", c.name, got, c.want)
		}
	}

	// A dump of every database also accounts only for what the source
	// listed (before or after its load); a named dump and a CLONE do not
	// look at the source's listing.
	present = []string{"a", "B", "c"}
	for _, c := range []struct {
		name string
		c    loadCopy
		want string
	}{
		{"full dump", loadCopy{full: true, source: []string{"a", "b"}}, "c"},
		{"full dump, case folded", loadCopy{full: true, source: []string{"A", "b", "C"}}, ""},
		{"full dump and before", loadCopy{full: true, source: []string{"a", "b", "c"}, before: []string{"a"}}, "a"},
		{"full dump, empty source", loadCopy{full: true}, "B,a,c"},
		{"named", loadCopy{named: []string{"a", "b", "c"}, source: []string{"a"}}, ""},
		{"clone", loadCopy{whole: true, source: []string{"a"}}, ""},
	} {
		if got := strings.Join(c.c.notCopied(present), ","); got != c.want {
			t.Errorf("%s: notCopied = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestOnSource: a name is on the source's listing without regard to case.
func TestOnSource(t *testing.T) {
	c := loadCopy{full: true, source: []string{"Toko", "rumah_sakit_é"}}
	for name, want := range map[string]bool{"toko": true, "TOKO": true, "rumah_sakit_é": true, "kecil": false, "": false} {
		if got := c.onSource(name); got != want {
			t.Errorf("onSource(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestSourceSeenUnion: the listings before and after a full dump's load
// are kept together, a name in both (in any case) once; done stores them
// for a full dump only.
func TestSourceSeenUnion(t *testing.T) {
	r := &rebuildLoad{}
	m := &loadMark{r: r, source: "10.0.0.1:3306", listed: true}
	m.sourceSeen([]string{"toko", "kecil"})
	m.sourceSeen([]string{"Toko", "sesudah"})
	m.done(false, nil)
	if !r.copied.full || strings.Join(r.copied.source, ",") != "kecil,sesudah,toko" {
		t.Errorf("full dump record = %+v, want full with kecil,sesudah,toko", r.copied)
	}
	m.done(false, []string{"toko"})
	if r.copied.full || r.copied.source != nil {
		t.Errorf("named dump record = %+v, want no source listing", r.copied)
	}
	m.done(true, nil)
	if r.copied.full || r.copied.source != nil {
		t.Errorf("clone record = %+v, want no source listing", r.copied)
	}
}

// TestListingProof: which SHOW GRANTS rows (read for the account by name,
// so a role's privileges never show as the account's) prove that the
// account's database listing is complete (plan evidence "Bukti daftar
// database yang lengkap").
func TestListingProof(t *testing.T) {
	const to = " TO `u`@`localhost`"
	cases := []struct {
		name          string
		grants        []string
		show, select_ bool
	}{
		{"global SELECT", []string{"GRANT SELECT ON *.*" + to}, false, true},
		{"SHOW DATABASES", []string{"GRANT SHOW DATABASES ON *.*" + to}, true, false},
		{"ALL", []string{"GRANT ALL PRIVILEGES ON *.*" + to + " WITH GRANT OPTION"}, true, true},
		{"install account (8.0 lists static privileges)", []string{
			"GRANT SELECT, INSERT, RELOAD, SHOW DATABASES, SUPER ON *.*" + to + " WITH GRANT OPTION",
			"GRANT BACKUP_ADMIN,SYSTEM_VARIABLES_ADMIN ON *.*" + to + " WITH GRANT OPTION"}, true, true},
		{"database-scoped admin", []string{
			"GRANT RELOAD, SUPER ON *.*" + to,
			"GRANT REPLICATION_SLAVE_ADMIN,SYSTEM_VARIABLES_ADMIN ON *.*" + to,
			"GRANT ALL PRIVILEGES ON `x`.*" + to}, false, false},
		{"SHOW DATABASES on one database is no global grant", []string{"GRANT SELECT ON `old`.*" + to}, false, false},
		{"role only", []string{"GRANT USAGE ON *.*" + to, "GRANT `r_all`@`%`" + to}, false, false},
		{"partial revoke of SELECT keeps SHOW DATABASES", []string{
			"GRANT SELECT, SHOW DATABASES ON *.*" + to, "REVOKE SELECT ON `old1`.* FROM `u`@`localhost`"}, true, true},
		{"partial revoke of SHOW DATABASES", []string{
			"GRANT SHOW DATABASES ON *.*" + to, "REVOKE SHOW DATABASES ON `old1`.* FROM `u`@`localhost`"}, false, false},
		{"partial revoke of everything on one database", []string{
			"GRANT ALL PRIVILEGES ON *.*" + to, "REVOKE SELECT, INSERT, DROP ON `old2`.* FROM `u`@`localhost`"}, true, true},
		{"nothing", nil, false, false},
		// Crafted names (plan evidence, part E): the privilege list is
		// privilege tokens only, so a name holding " ON *.* TO " proves
		// nothing.
		{"crafted user name", []string{
			"GRANT RELOAD ON *.* TO `p, SHOW DATABASES ON *.* TO x`@`localhost`",
			"GRANT ALL PRIVILEGES ON `x`.* TO `p, SHOW DATABASES ON *.* TO x`@`localhost`"}, false, false},
		{"crafted column name", []string{"GRANT USAGE ON *.*" + to,
			"GRANT SELECT (`q, SHOW DATABASES ON *.* TO z`) ON `x`.`t`" + to}, false, false},
		{"crafted role name", []string{"GRANT USAGE ON *.*" + to,
			"GRANT `a``,SHOW DATABASES ON *.* TO b`@`%`" + to}, false, false},
		{"crafted role name, ANSI_QUOTES", []string{"GRANT USAGE ON *.* TO \"u\"@\"localhost\"",
			"GRANT \"a`,SHOW DATABASES ON *.* TO b\"@\"%\" TO \"u\"@\"localhost\""}, false, false},
		{"REVOKE row with a crafted identifier", []string{"GRANT SHOW DATABASES ON *.*" + to,
			"REVOKE SELECT (`c`) ON `x`.`t` FROM `u`@`localhost`"}, false, false},
		{"REVOKE row of another account", []string{"GRANT SHOW DATABASES ON *.*" + to,
			"REVOKE SELECT ON `old1`.* FROM `v`@`localhost`"}, false, false},
		{"GRANT row of another account is ignored", []string{"GRANT SHOW DATABASES, SELECT ON *.* TO `v`@`localhost`"},
			false, false},
		{"another account, then the account's own row", []string{"GRANT SHOW DATABASES ON *.* TO `v`@`localhost`",
			"GRANT SELECT ON *.*" + to}, false, true},
		{"ANSI_QUOTES rows", []string{"GRANT SELECT, SHOW DATABASES ON *.* TO \"u\"@\"localhost\""}, true, true},
	}
	for _, c := range cases {
		show, sel := listingProof(c.grants, "u", "localhost")
		if show != c.show || sel != c.select_ {
			t.Errorf("%s: listingProof = (show %v, select %v), want (%v, %v)", c.name, show, sel, c.show, c.select_)
		}
	}
}

// TestProofAccount: CURRENT_USER() is used for the proof only when its
// user and host need no quoting care.
func TestProofAccount(t *testing.T) {
	for _, a := range []string{"dbpanel_admin@localhost", "root@localhost", "admin@127.0.0.1", "app-user@%",
		"u@192.168.1.%", "u@::1", "panel_admin@localhost", "a$b.c@db.lokal"} {
		if _, _, ok := proofAccount(a); !ok {
			t.Errorf("%q refused, want accepted", a)
		}
	}
	for _, a := range []string{"@localhost", `a\b@localhost`, "a'b@localhost", "a b@localhost", "a,b@localhost",
		"a`b@localhost", "p, SHOW DATABASES ON *.* TO x@localhost", "ü@localhost", "u@", "u", "u@local host", `u@"h"`} {
		if _, _, ok := proofAccount(a); ok {
			t.Errorf("%q accepted, want refused", a)
		}
	}
	if u, h, _ := proofAccount("app-user@192.168.1.%"); u != "app-user" || h != "192.168.1.%" {
		t.Errorf("split = %q, %q", u, h)
	}
}

// TestListUnsupportedAccountText: T17, character for character.
func TestListUnsupportedAccountText(t *testing.T) {
	got := (&errListIncomplete{unsupported: true}).Error()
	want := "daftar database di server ini tidak bisa dipastikan lengkap: nama akun MySQL yang dipakai agent memuat karakter yang tidak didukung pemeriksaan ini (yang didukung: huruf, angka, garis bawah, titik, tanda hubung, dan tanda dolar). Jalankan di server ini: sudo dbpanel agent reset-admin, lalu tekan \"Ulangi dari tahap ini\" pada pekerjaan di panel."
	if got != want {
		t.Errorf("text = %q\nwant   %q", got, want)
	}
}

// TestMissingGrantsCraftedRow: a name holding " ON *.* TO " no longer
// fakes a global privilege for the grant check.
func TestMissingGrantsCraftedRow(t *testing.T) {
	req := grantSet{static: []string{"SELECT", "RELOAD"}}
	for name, rows := range map[string][]string{
		"user name": {"GRANT USAGE ON *.* TO `p, SELECT, RELOAD ON *.* TO x`@`localhost` WITH GRANT OPTION",
			"GRANT ALL PRIVILEGES ON `x`.* TO `p, SELECT, RELOAD ON *.* TO x`@`localhost`"},
		"role name": {"GRANT USAGE ON *.* TO `u`@`localhost` WITH GRANT OPTION",
			"GRANT `a``,SELECT, RELOAD ON *.* TO b`@`%` TO `u`@`localhost`"},
		"column name": {"GRANT USAGE ON *.* TO `u`@`localhost` WITH GRANT OPTION",
			"GRANT SELECT (`q, SELECT, RELOAD ON *.* TO z`) ON `x`.`t` TO `u`@`localhost`"},
	} {
		got := missingGrants(rows, "u@localhost", req)
		if len(got) == 0 || !strings.Contains(got[0], "SELECT, RELOAD") {
			t.Errorf("%s: missingGrants = %q, want SELECT and RELOAD missing", name, got)
		}
	}
}

// TestListIncompleteText: T16, character for character.
func TestListIncompleteText(t *testing.T) {
	got := (&errListIncomplete{account: quoteAccount("rs_scoped@localhost")}).Error()
	want := fmt.Sprintf("daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL %s yang dipakai agent tidak punya hak SHOW DATABASES. Jalankan di server ini: sudo dbpanel agent grant-admin, lalu tekan \"Ulangi dari tahap ini\" pada pekerjaan di panel.",
		"'rs_scoped'@'localhost'")
	if got != want {
		t.Errorf("text = %q\nwant   %q", got, want)
	}
}
