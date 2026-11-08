package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// sizedRequest encodes a request envelope of exactly size bytes (its
// params are a JSON string padded to fit).
func sizedRequest(t *testing.T, id string, size int) []byte {
	t.Helper()
	enc := func(pad int) []byte {
		req := proto.Request{ID: id, Cmd: "echo", Params: json.RawMessage(`"` + strings.Repeat("x", pad) + `"`)}
		b, err := proto.Encode(proto.Envelope{Kind: proto.KindRequest, Request: &req})
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	b := enc(size - len(enc(0)))
	if len(b) != size {
		t.Fatalf("request is %d bytes, want %d", len(b), size)
	}
	return b
}

// TestAgentReadLimitOneMiB (SEC-34): the agent answers a request of 1 MiB
// from the panel and drops the session on one byte more.
func TestAgentReadLimitOneMiB(t *testing.T) {
	if readLimit != 1<<20 {
		t.Fatalf("readLimit = %d, want 1 MiB", readLimit)
	}
	answered := make(chan string, 2)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer ws.CloseNow()
		ws.SetReadLimit(4 << 20)
		ctx := r.Context()
		// waitAnswer reads until a response arrives (the hello event first).
		waitAnswer := func() bool {
			for {
				_, data, err := ws.Read(ctx)
				if err != nil {
					return false
				}
				if env, err := proto.Decode(data); err == nil && env.Kind == proto.KindResponse {
					answered <- env.Response.ID
					return true
				}
			}
		}
		if ws.Write(ctx, websocket.MessageText, sizedRequest(t, "fits", 1<<20)) != nil || !waitAnswer() {
			return
		}
		if ws.Write(ctx, websocket.MessageText, sizedRequest(t, "over", 1<<20+1)) != nil {
			return
		}
		waitAnswer()
	}))
	defer srv.Close()

	reg := commands.NewRegistry()
	reg.Register("echo", func(context.Context, json.RawMessage) (any, error) { return "ok", nil })
	a := New(config.Config{}, reg, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	go func() { ended <- a.serve(ctx, ws) }()

	select {
	case err := <-ended:
		if ctx.Err() != nil {
			t.Fatal("session did not end on the message over 1 MiB")
		}
		if err == nil {
			t.Fatal("serve returned nil after a message over 1 MiB")
		}
	case <-ctx.Done():
		t.Fatal("session kept after a message over 1 MiB")
	}
	var ids []string
	for len(answered) > 0 {
		ids = append(ids, <-answered)
	}
	if strings.Join(ids, ",") != "fits" {
		t.Fatalf("answered %v, want only the 1 MiB request", ids)
	}
}
