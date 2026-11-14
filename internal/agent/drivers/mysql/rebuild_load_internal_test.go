package mysql

import (
	"strings"
	"testing"
)

// TestNotReplacedNamesAtMostTen: A6 names at most ten databases, then
// ", …".
func TestNotReplacedNamesAtMostTen(t *testing.T) {
	var names []string
	for _, c := range "abcdefghijkl" {
		names = append(names, "db_"+string(c))
	}
	msg := (&errNotReplaced{source: "10.0.0.1:3306", names: names}).Error()
	want := "(db_a, db_b, db_c, db_d, db_e, db_f, db_g, db_h, db_i, db_j, …); karantina tidak diakhiri."
	if !strings.Contains(msg, want) {
		t.Errorf("message = %q, want it to hold %q", msg, want)
	}
	if one := (&errNotReplaced{source: "s:1", names: []string{"x"}}).Error(); !strings.Contains(one, "(x);") {
		t.Errorf("one name: %q", one)
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

// TestListUnsupportedAccountText: the E-1 refusal, character for
// character.
func TestListUnsupportedAccountText(t *testing.T) {
	got := (&errListIncomplete{unsupported: true}).Error()
	want := "daftar database di server ini tidak bisa dipastikan lengkap: nama akun MySQL yang dipakai agent memuat " +
		"karakter yang tidak didukung pemeriksaan ini. Hubungkan MySQL di panel dengan akun lain yang namanya hanya " +
		"huruf, angka, garis bawah, titik, atau tanda hubung (misalnya dbpanel_admin), lalu ulangi bangun ulang dari panel."
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

// TestListIncompleteText: the C-1 refusal, character for character.
func TestListIncompleteText(t *testing.T) {
	got := (&errListIncomplete{account: quoteAccount("rs_scoped@localhost")}).Error()
	want := "daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL 'rs_scoped'@'localhost' yang dipakai " +
		"agent tidak punya hak SHOW DATABASES. Jalankan sebagai root MySQL di server ini: GRANT SHOW DATABASES ON *.* TO " +
		"'rs_scoped'@'localhost'; lalu ulangi bangun ulang dari panel."
	if got != want {
		t.Errorf("text = %q\nwant   %q", got, want)
	}
}
