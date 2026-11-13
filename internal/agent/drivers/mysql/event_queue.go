package mysql

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The guard's events for the panel (R-117): the guard only queues them
// (enqueue), under promoteMu or not, and never waits on the panel; one
// sender (RunEvents) sends them, oldest first, never under promoteMu. A
// send can block for as long as the agent's write timeout on a panel
// connection that does not drain; it delays only the events behind it,
// never a guard round.

const (
	// eventQueueCap is how many events wait for the panel at most.
	eventQueueCap = 64
	// eventRetry is how long after a failed send the sender tries again.
	eventRetry = 5 * time.Second
)

// eventQueue holds the events not sent yet, oldest first.
type eventQueue struct {
	mu    sync.Mutex
	items []proto.Event
	wake  chan struct{} // cap 1, made on first push (or wait)
	// sending: the front event is with the sender (front until
	// popFront). A push that drops that event clears it, and popFront then
	// removes none.
	sending bool
}

// isStartReport is whether typ is a start report (EventQuarantineStart,
// EventQuarantineStartFailed): what the guard judged of a start of MySQL
// on a quarantined server, kept on disk too and carried by the health
// snapshot (start_result.go).
func isStartReport(typ string) bool {
	return typ == proto.EventQuarantineStart || typ == proto.EventQuarantineStartFailed
}

// push appends ev; when full it first drops the oldest start report
// (EventQuarantineStart, EventQuarantineStartFailed: their value is in
// the snapshot), else the oldest event, and returns what it dropped.
func (q *eventQueue) push(ev proto.Event) (dropped *proto.Event) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if len(q.items) >= eventQueueCap {
		i := slices.IndexFunc(q.items, func(e proto.Event) bool { return isStartReport(e.Type) })
		if i < 0 {
			i = 0
		}
		old := q.items[i]
		dropped = &old
		if i == 0 {
			q.sending = false
		}
		q.items = slices.Delete(q.items, i, i+1)
	}
	q.items = append(q.items, ev)
	select {
	case q.wakeLocked() <- struct{}{}:
	default:
	}
	return dropped
}

// front is the oldest event, now with the sender until popFront.
func (q *eventQueue) front() (proto.Event, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sending = len(q.items) > 0
	if !q.sending {
		return proto.Event{}, false
	}
	return q.items[0], true
}

// popFront removes the event front gave, once it was sent; nothing when
// push dropped it meanwhile.
func (q *eventQueue) popFront() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.sending {
		q.items[0] = proto.Event{}
		q.items = q.items[1:]
	}
	q.sending = false
}

// waiter is the channel a push signals.
func (q *eventQueue) waiter() <-chan struct{} {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.wakeLocked()
}

// wakeLocked is q.wake, made when missing. The caller holds q.mu.
func (q *eventQueue) wakeLocked() chan struct{} {
	if q.wake == nil {
		q.wake = make(chan struct{}, 1)
	}
	return q.wake
}

// enqueue queues an event for the panel: it marshals payload and pushes it;
// it never blocks and never sends. Without Events it drops the event.
// A start report is queued after it is written to disk (recordStart).
func (d *Driver) enqueue(typ string, payload any) {
	if d.Events == nil {
		return
	}
	if emitStartHook != nil && isStartReport(typ) {
		emitStartHook(d)
	}
	b, err := json.Marshal(payload)
	if err != nil {
		d.warn("event not encoded", "type", typ, "err", err)
		return
	}
	if dropped := d.outbox.push(proto.Event{Type: typ, Payload: b}); dropped != nil {
		d.warn("event dropped: too many waiting for the panel", "type", dropped.Type)
	}
}

// sendQueued sends from the front until the queue is empty (true) or a
// send fails (false, warned once until a send succeeds). Never under
// promoteMu. One sender at a time: it owns sendWarned.
func (d *Driver) sendQueued() bool {
	for {
		ev, ok := d.outbox.front()
		if !ok {
			return true
		}
		if err := d.Events.Emit(ev); err != nil {
			if !d.sendWarned {
				d.sendWarned = true
				d.warn("event not sent; kept and sent again", "type", ev.Type, "err", err)
			}
			return false
		}
		d.sendWarned = false
		d.outbox.popFront()
	}
}

// RunEvents is the one sender: sendQueued, then wait for a push (wake),
// eventRetry after a failure, or ctx.
func (d *Driver) RunEvents(ctx context.Context) {
	wake := d.outbox.waiter()
	for {
		var retry *time.Timer
		var retryC <-chan time.Time
		if !d.sendQueued() {
			retry = time.NewTimer(eventRetry)
			retryC = retry.C
		}
		select {
		case <-ctx.Done():
		case <-wake:
		case <-retryC:
		}
		if retry != nil {
			retry.Stop()
		}
		if ctx.Err() != nil {
			return
		}
	}
}
