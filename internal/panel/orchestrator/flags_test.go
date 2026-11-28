package orchestrator

import "testing"

// TestFlagHelpers: only both flags ON refuse every account; only read_only
// OFF lets ordinary accounts write, whatever super_read_only says.
func TestFlagHelpers(t *testing.T) {
	for _, c := range []struct {
		readOnly, superReadOnly bool
		noWrites, appsWrite     bool
	}{
		{false, false, false, true},
		{false, true, false, true}, // the mix a restart leaves: writable
		{true, false, false, false},
		{true, true, true, false},
	} {
		if got := noWrites(c.readOnly, c.superReadOnly); got != c.noWrites {
			t.Errorf("noWrites(%v, %v) = %v, want %v", c.readOnly, c.superReadOnly, got, c.noWrites)
		}
		if got := appsWrite(c.readOnly); got != c.appsWrite {
			t.Errorf("appsWrite(%v) = %v, want %v (super_read_only %v)", c.readOnly, got, c.appsWrite, c.superReadOnly)
		}
	}
}
