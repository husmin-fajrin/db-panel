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
	}
	for _, c := range cases {
		show, sel := listingProof(c.grants)
		if show != c.show || sel != c.select_ {
			t.Errorf("%s: listingProof = (show %v, select %v), want (%v, %v)", c.name, show, sel, c.show, c.select_)
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
