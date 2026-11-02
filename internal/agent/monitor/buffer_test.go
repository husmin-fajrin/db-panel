package monitor

import (
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

func entryWithSeq(seq uint64) entry {
	return entry{ev: proto.HealthEvent{Seq: seq}, takenAt: time.Unix(int64(seq), 0)}
}

func TestRingDrainOldestFirst(t *testing.T) {
	var r ring
	for i := uint64(1); i <= 3; i++ {
		r.Push(entryWithSeq(i))
	}
	got := r.Drain()
	if len(got) != 3 || got[0].ev.Seq != 1 || got[1].ev.Seq != 2 || got[2].ev.Seq != 3 {
		t.Fatalf("Drain = %+v, want seq 1,2,3", got)
	}
	if r.Len() != 0 || len(r.Drain()) != 0 {
		t.Fatalf("ring not empty after Drain: len %d", r.Len())
	}
}

func TestRingDropsOldestBeyondCapacity(t *testing.T) {
	var r ring
	for i := uint64(1); i <= ringCapacity+5; i++ {
		r.Push(entryWithSeq(i))
	}
	if r.Len() != ringCapacity {
		t.Fatalf("Len = %d, want %d", r.Len(), ringCapacity)
	}
	got := r.Drain()
	if len(got) != ringCapacity {
		t.Fatalf("Drain returned %d entries, want %d", len(got), ringCapacity)
	}
	for i, e := range got {
		if want := uint64(i + 6); e.ev.Seq != want {
			t.Fatalf("entry %d has seq %d, want %d", i, e.ev.Seq, want)
		}
		if !e.takenAt.Equal(time.Unix(int64(e.ev.Seq), 0)) {
			t.Fatalf("entry %d lost its takenAt", i)
		}
	}
}
