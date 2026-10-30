package agent

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/husmin-fajrin/db-panel/internal/proto"
	"github.com/husmin-fajrin/db-panel/internal/version"
)

const (
	readLimit      = 4 << 20
	writeTimeout   = 10 * time.Second
	pingInterval   = 15 * time.Second
	pingTimeout    = 10 * time.Second
	requestTimeout = 10 * time.Minute
	// requestDrain bounds how long a closing session waits for running
	// commands (whose ctx is already cancelled) before it returns anyway.
	requestDrain = 2 * time.Second
)

// session is one live WebSocket to the panel.
type session struct {
	ws      *websocket.Conn
	writeMu sync.Mutex
}

// send writes one envelope. Writes use their own timeout rather than a
// caller's ctx: coder/websocket closes the connection when a write's ctx ends.
func (s *session) send(env proto.Envelope) error {
	data, err := proto.Encode(env)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return s.ws.Write(ctx, websocket.MessageText, data)
}

// serve runs one connection until it drops or ctx is done, and returns the
// read error that ended it. On return the session's own goroutines have
// exited; command goroutines get their ctx cancelled and up to requestDrain
// to finish, so a command that ignores ctx cannot hold up a reconnect or a
// shutdown (its late response is dropped).
func (a *Agent) serve(ctx context.Context, ws *websocket.Conn) error {
	ws.SetReadLimit(readLimit)
	s := &session{ws: ws}

	sctx, cancel := context.WithCancel(ctx)
	var wg, requests sync.WaitGroup
	defer func() {
		cancel()
		ws.CloseNow() // unblocks a ping or write still in flight
		wg.Wait()
		a.drainRequests(&requests)
	}()

	// Reads use a background ctx (a cancelled read ctx drops the connection);
	// a graceful close on shutdown ends the read loop instead.
	wg.Go(func() {
		<-sctx.Done()
		if ctx.Err() != nil {
			ws.Close(websocket.StatusNormalClosure, "agent berhenti")
		}
	})
	wg.Go(func() { a.pingLoop(sctx, ws) })

	payload, _ := json.Marshal(map[string]string{"agent_version": version.Version})
	if err := s.send(proto.Envelope{Kind: proto.KindEvent, Event: &proto.Event{Type: "hello", Payload: payload}}); err != nil {
		return err
	}
	a.setSession(s)
	defer a.setSession(nil)

	for {
		_, data, err := ws.Read(context.Background())
		if err != nil {
			return err
		}
		env, err := proto.Decode(data)
		if err != nil {
			a.logger.Warn("dropping malformed message from panel", "err", err)
			continue
		}
		if env.Kind != proto.KindRequest {
			continue
		}
		req := *env.Request
		requests.Go(func() {
			rctx, rcancel := context.WithTimeout(sctx, requestTimeout)
			defer rcancel()
			resp := a.reg.Dispatch(rctx, req)
			if err := s.send(proto.Envelope{Kind: proto.KindResponse, Response: &resp}); err != nil && sctx.Err() == nil {
				a.logger.Warn("cannot send response", "cmd", req.Cmd, "id", req.ID, "err", err)
			}
		})
	}
}

// drainRequests waits up to requestDrain for the command goroutines.
func (a *Agent) drainRequests(requests *sync.WaitGroup) {
	done := make(chan struct{})
	go func() {
		requests.Wait()
		close(done)
	}()
	t := time.NewTimer(requestDrain)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		a.logger.Warn("commands still running after disconnect; not waiting for them")
	}
}

// pingLoop pings the panel every pingInterval; a failed ping closes the
// connection, which ends the read loop.
func (a *Agent) pingLoop(ctx context.Context, ws *websocket.Conn) {
	t := time.NewTicker(pingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		pctx, cancel := context.WithTimeout(ctx, pingTimeout)
		err := ws.Ping(pctx)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				a.logger.Warn("ping to panel failed", "err", err)
				ws.CloseNow()
			}
			return
		}
	}
}
