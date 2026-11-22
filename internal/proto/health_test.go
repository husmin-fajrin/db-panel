package proto_test

import (
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// TestSnapshotStaysComparable: HealthSnapshot holds no slice or map
// directly, so == compiles (the agent's monitor compares a snapshot with
// the zero value), and two snapshots differing only in ClockUnixMS are
// not equal.
func TestSnapshotStaysComparable(t *testing.T) {
	var a, b proto.HealthSnapshot
	_ = a == b // compiles only while the struct stays comparable
	if a != b {
		t.Fatal("two zero snapshots are not equal")
	}
	b.ClockUnixMS = 1
	if a == b {
		t.Fatal("snapshots differing in ClockUnixMS compare equal")
	}
}
