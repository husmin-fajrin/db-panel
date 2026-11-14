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
