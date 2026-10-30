package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// startFakeAgent serves a WebSocket endpoint that plays the agent: serve runs
// with the accepted connection and the handler returns when serve does. It
// returns a WSConn dialled to that endpoint, closed at test cleanup.
func startFakeAgent(t *testing.T, onEvent func(proto.Event), serve func(ctx context.Context, ws *websocket.Conn)) *WSConn {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer ws.CloseNow()
		serve(r.Context(), ws)
	}))
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	c := NewWSConn(ws, onEvent)
	t.Cleanup(func() { c.Close() })
	return c
}

// readRequest reads one envelope from the panel and returns its request.
func readRequest(ctx context.Context, ws *websocket.Conn) (*proto.Request, error) {
	_, data, err := ws.Read(ctx)
	if err != nil {
		return nil, err
	}
	env, err := proto.Decode(data)
	if err != nil {
		return nil, err
	}
	if env.Kind != proto.KindRequest {
		return nil, errors.New("not a request: " + env.Kind)
	}
	return env.Request, nil
}

func writeEnvelope(ctx context.Context, ws *websocket.Conn, env proto.Envelope) error {
	data, err := proto.Encode(env)
	if err != nil {
		return err
	}
	return ws.Write(ctx, websocket.MessageText, data)
}

// drain reads until the connection fails, never replying.
func drain(ctx context.Context, ws *websocket.Conn, got chan<- string) {
	for {
		req, err := readRequest(ctx, ws)
		if err != nil {
			return
		}
		if got != nil {
			got <- req.ID
		}
	}
}

func TestWSConnCallRoundTrip(t *testing.T) {
	c := startFakeAgent(t, nil, func(ctx context.Context, ws *websocket.Conn) {
		for {
			req, err := readRequest(ctx, ws)
			if err != nil {
				return
			}
			var resp proto.Response
			if req.Cmd == "fail" {
				resp = proto.ErrorResponse(req.ID, proto.CodeBadParams, "parameter salah")
			} else {
				resp, err = proto.NewResponse(req.ID, map[string]any{"cmd": req.Cmd, "id": req.ID, "params": req.Params})
				if err != nil {
					t.Errorf("NewResponse: %v", err)
					return
				}
			}
			if err := writeEnvelope(ctx, ws, proto.Envelope{Kind: proto.KindResponse, Response: &resp}); err != nil {
				return
			}
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var out struct {
		Cmd    string          `json:"cmd"`
		ID     string          `json:"id"`
		Params json.RawMessage `json:"params"`
	}
	if err := c.Call(ctx, "probe", map[string]int{"n": 1}, &out); err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.Cmd != "probe" || string(out.Params) != `{"n":1}` {
		t.Errorf("result = %+v", out)
	}
	if len(out.ID) != 32 {
		t.Errorf("request ID %q: want 32 hex chars", out.ID)
	}

	err := c.Call(ctx, "fail", nil, nil)
	var pe *proto.Error
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v (%T), want *proto.Error", err, err)
	}
	if pe.Code != proto.CodeBadParams || pe.Message != "parameter salah" {
		t.Errorf("error = %+v", pe)
	}
}

func TestWSConnEventDelivered(t *testing.T) {
	events := make(chan proto.Event, 1)
	startFakeAgent(t, func(ev proto.Event) { events <- ev }, func(ctx context.Context, ws *websocket.Conn) {
		ev := proto.Event{Type: "job.progress", JobID: "j1", Payload: json.RawMessage(`{"pct":50}`)}
		if err := writeEnvelope(ctx, ws, proto.Envelope{Kind: proto.KindEvent, Event: &ev}); err != nil {
			t.Errorf("write event: %v", err)
			return
		}
		drain(ctx, ws, nil)
	})

	select {
	case ev := <-events:
		if ev.Type != "job.progress" || ev.JobID != "j1" || string(ev.Payload) != `{"pct":50}` {
			t.Errorf("event = %+v", ev)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("event not delivered")
	}
}

func TestWSConnCallTimeout(t *testing.T) {
	c := startFakeAgent(t, nil, func(ctx context.Context, ws *websocket.Conn) {
		drain(ctx, ws, nil)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := c.Call(ctx, "probe", nil, nil)
	var pe *proto.Error
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v (%T), want *proto.Error", err, err)
	}
	if pe.Code != proto.CodeTimeout || pe.Message != "batas waktu perintah habis" {
		t.Errorf("error = %+v", pe)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("Call took %v", d)
	}

	c.mu.Lock()
	n := len(c.pending)
	c.mu.Unlock()
	if n != 0 {
		t.Errorf("pending calls left after timeout: %d", n)
	}
}

func TestWSConnCloseFailsPending(t *testing.T) {
	got := make(chan string, 1)
	c := startFakeAgent(t, nil, func(ctx context.Context, ws *websocket.Conn) {
		drain(ctx, ws, got)
	})

	errc := make(chan error, 1)
	go func() { errc <- c.Call(context.Background(), "probe", nil, nil) }()

	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("agent never received the request")
	}
	if err := c.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}

	select {
	case err := <-errc:
		var pe *proto.Error
		if !errors.As(err, &pe) {
			t.Fatalf("err = %v (%T), want *proto.Error", err, err)
		}
		if pe.Code != proto.CodeOffline || pe.Message != "koneksi agent terputus" {
			t.Errorf("error = %+v", pe)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("pending Call not failed by Close")
	}

	select {
	case <-c.Wait():
	case <-time.After(5 * time.Second):
		t.Fatal("Wait() not closed after Close")
	}

	err := c.Call(context.Background(), "probe", nil, nil)
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != proto.CodeOffline {
		t.Errorf("Call after Close: err = %v, want CodeOffline", err)
	}
}

func TestWSConnAgentDisconnect(t *testing.T) {
	c := startFakeAgent(t, nil, func(ctx context.Context, ws *websocket.Conn) {
		if _, err := readRequest(ctx, ws); err != nil {
			return
		}
		ws.Close(websocket.StatusGoingAway, "")
	})

	err := c.Call(context.Background(), "probe", nil, nil)
	var pe *proto.Error
	if !errors.As(err, &pe) || pe.Code != proto.CodeOffline {
		t.Errorf("err = %v, want CodeOffline", err)
	}
	select {
	case <-c.Wait():
	case <-time.After(5 * time.Second):
		t.Fatal("Wait() not closed after agent disconnect")
	}
}

func TestWSConnCancelledCallKeepsConn(t *testing.T) {
	c := startFakeAgent(t, nil, func(ctx context.Context, ws *websocket.Conn) {
		for {
			req, err := readRequest(ctx, ws)
			if err != nil {
				return
			}
			resp, err := proto.NewResponse(req.ID, req.Cmd)
			if err != nil {
				t.Errorf("NewResponse: %v", err)
				return
			}
			if err := writeEnvelope(ctx, ws, proto.Envelope{Kind: proto.KindResponse, Response: &resp}); err != nil {
				return
			}
		}
	})

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for range 10 {
		err := c.Call(cancelled, "probe", nil, nil)
		var pe *proto.Error
		if !errors.As(err, &pe) || pe.Code != proto.CodeTimeout {
			t.Fatalf("cancelled Call: err = %v, want CodeTimeout", err)
		}
	}

	ctx, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	var out string
	if err := c.Call(ctx, "probe", nil, &out); err != nil {
		t.Fatalf("Call after cancelled calls: %v", err)
	}
	if out != "probe" {
		t.Errorf("result = %q", out)
	}
	select {
	case <-c.Wait():
		t.Fatal("connection closed by a cancelled Call")
	default:
	}
}
