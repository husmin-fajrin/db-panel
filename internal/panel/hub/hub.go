// Package hub keeps the panel's registry of live agent connections and routes
// commands to them. A connection is either a WebSocket from a remote agent
// (WSConn) or the in-process agent of the panel host (NewLocalConn).
package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sync"

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

// Hub maps server IDs to their active connection.
type Hub struct {
	// lifeMu serialises Attach and Detach together with their listener
	// callbacks, so the listener sees lifecycle changes in order.
	lifeMu sync.Mutex

	mu    sync.Mutex // guards conns
	conns map[int64]Conn
	l     Listener
}

// New returns an empty hub that reports to l.
func New(l Listener) *Hub {
	return &Hub{conns: make(map[int64]Conn), l: l}
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
// it passes ev to the listener. It runs on the sending connection's
// goroutine, so the listener's OnEvent must not block.
func (h *Hub) OnEvent(serverID int64, ev proto.Event) {
	h.l.OnEvent(serverID, ev)
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
