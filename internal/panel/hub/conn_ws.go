package hub

import (
	"context"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// readLimit caps the size of one incoming message.
const readLimit = 4 << 20

// writeTimeout bounds writing one request. The write deliberately does not
// use the caller's ctx: coder/websocket closes the whole connection when a
// write's ctx is done, and one cancelled Call must not drop the agent.
const writeTimeout = 10 * time.Second

// The panel pings every agent itself: an agent whose host or network died
// sends nothing, so without pings its connection would look alive until TCP
// gives up, minutes later. A ping unanswered for pingTimeout drops it.
const (
	pingInterval = 10 * time.Second
	pingTimeout  = 10 * time.Second
)

// closeGrace bounds how long CloseNow waits for the agent to answer the
// close frame.
const closeGrace = time.Second

// reasonShutdown is the close reason sent to agents when the panel stops.
const reasonShutdown = "panel shutting down"

// WSConn is the panel side of an agent's WebSocket. Requests are correlated
// with responses by ID; events go to onEvent from the read goroutine.
type WSConn struct {
	ws      *websocket.Conn
	onEvent func(proto.Event)

	writeMu sync.Mutex

	mu      sync.Mutex
	pending map[string]chan proto.Response
	closed  bool

	done     chan struct{}
	stopOnce sync.Once
	wsOnce   sync.Once
}

// NewWSConn wraps ws and starts its read and ping loops. onEvent may be nil;
// the panel passes a func calling Hub.OnEvent.
func NewWSConn(ws *websocket.Conn, onEvent func(proto.Event)) *WSConn {
	ws.SetReadLimit(readLimit)
	c := &WSConn{
		ws:      ws,
		onEvent: onEvent,
		pending: make(map[string]chan proto.Response),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	go c.pingLoop()
	return c
}

// Wait returns a channel closed when the connection is gone.
func (c *WSConn) Wait() <-chan struct{} { return c.done }

// Call sends cmd to the agent and waits for its response or ctx.
func (c *WSConn) Call(ctx context.Context, cmd string, params any, result any) error {
	if ctx.Err() != nil {
		return timeoutError()
	}
	req, err := newRequest(cmd, params)
	if err != nil {
		return err
	}
	data, err := proto.Encode(proto.Envelope{Kind: proto.KindRequest, Request: &req})
	if err != nil {
		return err
	}

	ch := make(chan proto.Response, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return offlineError()
	}
	c.pending[req.ID] = ch
	c.mu.Unlock()
	defer c.forget(req.ID)

	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	c.writeMu.Lock()
	err = c.ws.Write(wctx, websocket.MessageText, data)
	c.writeMu.Unlock()
	cancel()
	if err != nil {
		return offlineError()
	}

	select {
	case resp := <-ch:
		return readResponse(resp, result)
	case <-ctx.Done():
		return timeoutError()
	}
}

// Close fails pending calls with CodeOffline, closes the Wait channel, then
// closes the WebSocket (the close handshake may take a few seconds against an
// unresponsive peer). Later calls do nothing.
func (c *WSConn) Close() error {
	c.stop()
	var err error
	c.wsOnce.Do(func() { err = c.ws.Close(websocket.StatusNormalClosure, "") })
	return err
}

// CloseNow is Close for a panel that is stopping: it fails pending calls and
// closes the Wait channel at once, sends the agent a "going away" close frame
// with reasonShutdown, and waits at most closeGrace for the agent's answer.
// Against an unresponsive agent the close handshake times out in the
// background, so a dead peer cannot hold up the shutdown.
func (c *WSConn) CloseNow() {
	c.stop()
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.wsOnce.Do(func() { c.ws.Close(websocket.StatusGoingAway, reasonShutdown) })
	}()
	t := time.NewTimer(closeGrace)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
	}
}

// abort drops a connection whose peer stopped answering, without a close
// handshake.
func (c *WSConn) abort() {
	c.stop()
	c.wsOnce.Do(func() { c.ws.CloseNow() })
}

// pingLoop pings the agent every pingInterval until the connection is gone;
// an unanswered ping aborts the connection, which ends the Wait channel.
func (c *WSConn) pingLoop() {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-t.C:
		}
		ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
		err := c.ws.Ping(ctx)
		cancel()
		if err != nil {
			c.abort()
			return
		}
	}
}

func (c *WSConn) readLoop() {
	defer c.stop()
	for {
		_, data, err := c.ws.Read(context.Background())
		if err != nil {
			return
		}
		env, err := proto.Decode(data)
		if err != nil {
			continue
		}
		switch env.Kind {
		case proto.KindResponse:
			c.deliver(*env.Response)
		case proto.KindEvent:
			if c.onEvent != nil {
				c.onEvent(*env.Event)
			}
		}
	}
}

// deliver hands resp to the call waiting for its ID; unknown IDs are dropped.
func (c *WSConn) deliver(resp proto.Response) {
	c.mu.Lock()
	ch, ok := c.pending[resp.ID]
	delete(c.pending, resp.ID)
	c.mu.Unlock()
	if ok {
		ch <- resp
	}
}

func (c *WSConn) forget(id string) {
	c.mu.Lock()
	delete(c.pending, id)
	c.mu.Unlock()
}

// stop fails every pending call and closes the Wait channel, once.
func (c *WSConn) stop() {
	c.stopOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		pending := c.pending
		c.pending = make(map[string]chan proto.Response)
		c.mu.Unlock()
		for id, ch := range pending {
			ch <- proto.Response{ID: id, Error: offlineError()}
		}
		close(c.done)
	})
}
