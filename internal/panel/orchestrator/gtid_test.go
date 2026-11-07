package orchestrator

import "testing"

func TestGTIDSetCounts(t *testing.T) {
	for _, c := range []struct {
		a, b, skip string
		want       int
	}{
		{masterUUID + ":1-120", masterUUID + ":1-118", "", 2},
		{masterUUID + ":1-118", masterUUID + ":1-120", "", 0},
		{masterUUID + ":1-10:20-25,\n" + otherUUID + ":1-3", masterUUID + ":1-22", "", 3 + 3},
		{masterUUID + ":1-10," + otherUUID + ":1-3", "", masterUUID, 3},
		{masterUUID + ":1-5:blue:1-2", masterUUID + ":1-5", "", 2},
		{"", masterUUID + ":1-5", "", 0},
	} {
		a, ok1 := parseGTIDSet(c.a)
		b, ok2 := parseGTIDSet(c.b)
		if !ok1 || !ok2 {
			t.Fatalf("parse %q / %q", c.a, c.b)
		}
		if got := a.minusCount(b, c.skip); got != c.want {
			t.Errorf("%q - %q (skip %q) = %d, want %d", c.a, c.b, c.skip, got, c.want)
		}
	}
	if _, ok := parseGTIDSet("nope"); ok {
		t.Error("parsed a set without intervals")
	}
	u, _ := parseGTIDSet(masterUUID + ":1-5")
	v, _ := parseGTIDSet(masterUUID + ":6-9")
	if got := u.union(v)[masterUUID]; len(got) != 1 || got[0] != (gtidRange{1, 9}) {
		t.Errorf("union = %v", got)
	}
}
