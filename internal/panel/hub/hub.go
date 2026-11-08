// Package hub keeps the panel's registry of live agent connections and routes
// commands to them. A connection is either a WebSocket from a remote agent
// (WSConn) or an in-process command registry (NewLocalConn, used by tests).
package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// ErrOffline is returned by Hub.Call when the server has no live connection;
// a connection that drops mid-call fails with an error errors.Is matches to
// it as well.
var ErrOffline = proto.ErrOffline

// Conn is one live connection to an agent.
type Conn interface {
	// Call runs cmd on the agent. params is marshalled as JSON (nil sends
	// none); the result is unmarshalled into result unless result is nil.
	// When the agent answers with a failure, or the connection times out or
	// drops, the error is a *proto.Error.
	Call(ctx context.Context, cmd string, params any, result any) error
	Close() error
}

// Listener is notified about connection changes and agent events (the
// latter through Hub.OnEvent).
// OnOnline and OnOffline are delivered in the order the hub applied the
// changes, under the hub's lifecycle lock: a listener may call Call, IsOnline
// and OnlineIDs, but must not call Attach or Detach.
type Listener interface {
	OnOnline(serverID int64)
	OnOffline(serverID int64)
	OnEvent(serverID int64, ev proto.Event)
}

// The health reports (proto.EventHealth) of one server pass through a token
// bucket of reportBurst tokens refilled with one every reportRefill (spec
// 5.12); a report finding it empty is dropped and counted. The connection
// is never closed for it. A sound agent reports once per check interval
// (10 s or more); only the backlog it flushes after a reconnect can come
// faster, and of that, entries past the tenth are dropped (the fresh
// report monitor.configure answers with is a reply, not an event, and is
// not limited).
const (
	reportBurst  = 10
	reportRefill = 2 * time.Second
	// droppedWindow is how far back Dropped counts.
	droppedWindow = time.Hour
	// dropLogEvery is the minimum time between two log lines about one
	// server's dropped reports.
	dropLogEvery = time.Minute
)

// Hub maps server IDs to their active connection.
type Hub struct {
	// lifeMu serialises Attach and Detach together with their listener
	// callbacks, so the listener sees lifecycle changes in order.
	lifeMu sync.Mutex

	mu    sync.Mutex // guards conns
	conns map[int64]Conn
	l     Listener

	now func() time.Time
	log *slog.Logger

	rateMu  sync.Mutex // guards buckets
	buckets map[int64]*reportBucket
}

// reportBucket is one server's report rate limit and its dropped reports
// per minute over the last droppedWindow.
type reportBucket struct {
	tokens   float64
	last     time.Time // when tokens was last refilled
	drops    []dropMinute
	loggedAt time.Time // last log line about drops
}

type dropMinute struct {
	minute time.Time
	n      int
}

// Option configures a Hub.
type Option func(*Hub)

// WithNow sets the clock of the report rate limit; the default is
// time.Now.
func WithNow(now func() time.Time) Option { return func(h *Hub) { h.now = now } }

// WithLogger sets where dropped reports are logged; the default discards.
func WithLogger(l *slog.Logger) Option { return func(h *Hub) { h.log = l } }

// New returns an empty hub that reports to l.
func New(l Listener, opts ...Option) *Hub {
	h := &Hub{conns: make(map[int64]Conn), l: l, now: time.Now, log: slog.New(slog.DiscardHandler),
		buckets: make(map[int64]*reportBucket)}
	for _, o := range opts {
		o(h)
	}
	return h
}

// Attach makes c the active connection for serverID. An existing connection
// is replaced without an OnOffline and closed in the background, so a slow
// close handshake does not delay c; OnOnline is reported for c. It reports
// whether an existing connection was replaced.
func (h *Hub) Attach(serverID int64, c Conn) (replaced bool) {
	h.lifeMu.Lock()
	defer h.lifeMu.Unlock()

	h.mu.Lock()
	old := h.conns[serverID]
	h.conns[serverID] = c
	h.mu.Unlock()

	replaced = old != nil && old != c
	if replaced {
		go old.Close()
	}
	h.l.OnOnline(serverID)
	return replaced
}

// Disconnect removes the active connection of serverID, if any, closes it in
// the background and reports OnOffline. It is used when the server is deleted.
func (h *Hub) Disconnect(serverID int64) {
	h.lifeMu.Lock()
	defer h.lifeMu.Unlock()

	h.mu.Lock()
	c, ok := h.conns[serverID]
	delete(h.conns, serverID)
	h.mu.Unlock()
	if !ok {
		return
	}
	go c.Close()
	h.rateMu.Lock()
	delete(h.buckets, serverID)
	h.rateMu.Unlock()
	h.l.OnOffline(serverID)
}

// Detach removes c if it is still the active connection for serverID and
// reports OnOffline. Detaching a connection that was already replaced is a
// no-op.
func (h *Hub) Detach(serverID int64, c Conn) {
	h.lifeMu.Lock()
	defer h.lifeMu.Unlock()

	h.mu.Lock()
	cur, ok := h.conns[serverID]
	if !ok || cur != c {
		h.mu.Unlock()
		return
	}
	delete(h.conns, serverID)
	h.mu.Unlock()

	h.l.OnOffline(serverID)
}

// Call runs cmd on the agent of serverID, or returns ErrOffline when it has
// no live connection.
func (h *Hub) Call(ctx context.Context, serverID int64, cmd string, params any, result any) error {
	h.mu.Lock()
	c, ok := h.conns[serverID]
	h.mu.Unlock()
	if !ok {
		return ErrOffline
	}
	return c.Call(ctx, cmd, params, result)
}

// OnEvent is the single entry for events sent by agents, remote or embedded:
// it passes ev to the listener, except a health report over the server's
// rate limit, which is dropped and counted (Dropped). It runs on the
// sending connection's goroutine, so the listener's OnEvent must not block.
func (h *Hub) OnEvent(serverID int64, ev proto.Event) {
	if ev.Type == proto.EventHealth && !h.allowReport(serverID) {
		return
	}
	h.l.OnEvent(serverID, ev)
}

// allowReport takes a token from serverID's bucket; without one it records
// the drop and logs it, at most once per dropLogEvery.
func (h *Hub) allowReport(serverID int64) bool {
	now := h.now()
	h.rateMu.Lock()
	b, ok := h.buckets[serverID]
	if !ok {
		b = &reportBucket{tokens: reportBurst, last: now}
		h.buckets[serverID] = b
	}
	if elapsed := now.Sub(b.last); elapsed > 0 {
		b.tokens = min(reportBurst, b.tokens+float64(elapsed)/float64(reportRefill))
		b.last = now
	}
	if b.tokens >= 1 {
		b.tokens--
		h.rateMu.Unlock()
		return true
	}
	minute := now.Truncate(time.Minute)
	if n := len(b.drops); n > 0 && b.drops[n-1].minute.Equal(minute) {
		b.drops[n-1].n++
	} else {
		b.drops = append(b.drops, dropMinute{minute: minute, n: 1})
	}
	b.prune(now)
	logIt := now.Sub(b.loggedAt) >= dropLogEvery
	dropped := 0
	if logIt {
		b.loggedAt = now
		dropped = b.count(now)
	}
	h.rateMu.Unlock()
	if logIt {
		h.log.Warn("agent health reports dropped: over the rate limit", "server_id", serverID,
			"dropped_last_hour", dropped)
	}
	return false
}

// prune forgets the minutes outside droppedWindow.
func (b *reportBucket) prune(now time.Time) {
	i := 0
	for i < len(b.drops) && !b.drops[i].minute.After(now.Add(-droppedWindow)) {
		i++
	}
	b.drops = b.drops[i:]
}

func (b *reportBucket) count(now time.Time) int {
	n := 0
	for _, d := range b.drops {
		if d.minute.After(now.Add(-droppedWindow)) {
			n += d.n
		}
	}
	return n
}

// Dropped is how many health reports of serverID were dropped over the
// rate limit in the last hour.
func (h *Hub) Dropped(serverID int64) int {
	now := h.now()
	h.rateMu.Lock()
	defer h.rateMu.Unlock()
	b, ok := h.buckets[serverID]
	if !ok {
		return 0
	}
	return b.count(now)
}

// IsOnline reports whether serverID has a live connection.
func (h *Hub) IsOnline(serverID int64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	_, ok := h.conns[serverID]
	return ok
}

// OnlineIDs returns the IDs of connected servers in ascending order.
func (h *Hub) OnlineIDs() []int64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	return slices.Sorted(maps.Keys(h.conns))
}

// newRequest builds a request with a fresh random ID and the marshalled params.
func newRequest(cmd string, params any) (proto.Request, error) {
	var id [16]byte
	rand.Read(id[:])
	req := proto.Request{ID: hex.EncodeToString(id[:]), Cmd: cmd}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return proto.Request{}, fmt.Errorf("hub: marshal params for %s: %w", cmd, err)
		}
		req.Params = b
	}
	return req, nil
}

// readResponse turns an agent response into Call's result: a *proto.Error
// when the agent reported failure, otherwise Result unmarshalled into result.
func readResponse(resp proto.Response, result any) error {
	if !resp.OK {
		if resp.Error == nil {
			return &proto.Error{Code: proto.CodeInternal, Message: "agent menolak perintah tanpa keterangan"}
		}
		return resp.Error
	}
	if result == nil || len(resp.Result) == 0 {
		return nil
	}
	if err := json.Unmarshal(resp.Result, result); err != nil {
		return fmt.Errorf("hub: decode result: %w", err)
	}
	return nil
}

func timeoutError() *proto.Error {
	return &proto.Error{Code: proto.CodeTimeout, Message: "batas waktu perintah habis"}
}

// offlineError is a copy of proto.ErrOffline, so a caller that edits the
// error it got cannot change the shared value.
func offlineError() *proto.Error {
	e := *proto.ErrOffline
	return &e
}
