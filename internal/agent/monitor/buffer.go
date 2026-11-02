package monitor

import (
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// ringCapacity is how many change reports are kept while the panel is
// unreachable.
const ringCapacity = 200

// entry is a report kept for later, with the time its snapshot was taken so
// TakenAgoMS can be computed when it is finally sent.
type entry struct {
	ev      proto.HealthEvent
	takenAt time.Time
}

// ring keeps the newest ringCapacity entries; pushing onto a full ring
// drops the oldest. The zero value is ready to use. It is not safe for
// concurrent use.
type ring struct {
	items []entry // oldest first
}

// Push appends e, dropping the oldest entry when the ring is full.
func (r *ring) Push(e entry) {
	if len(r.items) == ringCapacity {
		copy(r.items, r.items[1:])
		r.items = r.items[:ringCapacity-1]
	}
	r.items = append(r.items, e)
}

// Drain removes and returns every entry, oldest first.
func (r *ring) Drain() []entry {
	out := r.items
	r.items = nil
	return out
}

// Len is the number of entries kept.
func (r *ring) Len() int { return len(r.items) }
