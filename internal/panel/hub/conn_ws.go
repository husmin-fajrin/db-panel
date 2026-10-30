package hub

import (
	"context"
	"sync"

	"github.com/coder/websocket"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// readLimit caps the size of one incoming message.
const readLimit = 4 << 20

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

// NewWSConn wraps ws and starts its read loop. onEvent may be nil.
func NewWSConn(ws *websocket.Conn, onEvent func(proto.Event)) *WSConn {
	ws.SetReadLimit(readLimit)
	c := &WSConn{
		ws:      ws,
		onEvent: onEvent,
		pending: make(map[string]chan proto.Response),
		done:    make(chan struct{}),
	}
	go c.readLoop()
	return c
}

// Wait returns a channel closed when the connection is gone.
func (c *WSConn) Wait() <-chan struct{} { return c.done }

// Call sends cmd to the agent and waits for its response or ctx.
func (c *WSConn) Call(ctx context.Context, cmd string, params any, result any) error {
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

	c.writeMu.Lock()
	err = c.ws.Write(ctx, websocket.MessageText, data)
	c.writeMu.Unlock()
	if err != nil {
		if ctx.Err() != nil {
			return timeoutError()
		}
		return offlineError()
	}

	select {
	case resp := <-ch:
		return readResponse(resp, result)
	case <-ctx.Done():
		return timeoutError()
	}
}

// Close closes the WebSocket, fails pending calls with CodeOffline and closes
// the Wait channel. Later calls do nothing.
func (c *WSConn) Close() error {
	var err error
	c.wsOnce.Do(func() { err = c.ws.Close(websocket.StatusNormalClosure, "") })
	c.stop()
	return err
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
			ch <- proto.ErrorResponse(id, proto.CodeOffline, "koneksi agent terputus")
		}
		close(c.done)
	})
}
