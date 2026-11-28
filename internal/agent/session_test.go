package agent

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
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

// fakePanel accepts agent sessions; each one is handed to the test as a
// panelConn and kept open until the test closes it.
type fakePanel struct {
	srv   *httptest.Server
	conns chan *panelConn
}

type panelConn struct {
	t     *testing.T
	ws    *websocket.Conn
	ended chan struct{}
}

func newFakePanel(t *testing.T) *fakePanel {
	p := &fakePanel{conns: make(chan *panelConn, 4)}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		c := &panelConn{t: t, ws: ws, ended: make(chan struct{})}
		p.conns <- c
		<-c.ended
		ws.CloseNow()
	}))
	t.Cleanup(p.srv.Close)
	return p
}

// connect starts one agent session to the panel; the returned channel
// gets serve's error when the session ends.
func (p *fakePanel) connect(t *testing.T, ctx context.Context, a *Agent) (*panelConn, <-chan error) {
	t.Helper()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(p.srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	go func() { ended <- a.serve(ctx, ws) }()
	select {
	case c := <-p.conns:
		return c, ended
	case <-ctx.Done():
		t.Fatal("no session")
	}
	return nil, nil
}

// send writes one request.
func (c *panelConn) send(id, cmd string, params any) {
	c.t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		c.t.Fatal(err)
	}
	b, err := proto.Encode(proto.Envelope{Kind: proto.KindRequest, Request: &proto.Request{ID: id, Cmd: cmd, Params: raw}})
	if err != nil {
		c.t.Fatal(err)
	}
	if err := c.ws.Write(context.Background(), websocket.MessageText, b); err != nil {
		c.t.Fatal(err)
	}
}

// answer reads until the response to id arrives.
func (c *panelConn) answer(id string) proto.Response {
	c.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			c.t.Fatalf("waiting for %s: %v", id, err)
		}
		if env, err := proto.Decode(data); err == nil && env.Kind == proto.KindResponse && env.Response.ID == id {
			return *env.Response
		}
	}
}

// promoteRequest is mysql.status's promote_request asked as id.
func (c *panelConn) promoteRequest(id string) bool {
	c.t.Helper()
	c.send(id, "mysql.status", nil)
	resp := c.answer(id)
	var st proto.MySQLStatus
	if !resp.OK || json.Unmarshal(resp.Result, &st) != nil || st.PromoteRequest == nil {
		c.t.Fatalf("mysql.status = %+v %s; want a promote_request", resp.Error, resp.Result)
	}
	return *st.PromoteRequest
}

// promoteSessionAgent is an agent whose MySQL driver is pinned replica of
// another source than the requests name (so a promote that gets past the
// gate is refused), with a gate that holds mysql.promote until release
// is closed.
func promoteSessionAgent(t *testing.T) (a *Agent, held <-chan struct{}, release chan struct{}) {
	t.Helper()
	inst := testmysql.Start(t, testmysql.Options{})
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, mysql.RoleFile), []byte("replica\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, mysql.SourceFile), []byte("127.0.0.1:3398\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	reg := commands.NewRegistry()
	runner := jobs.NewRunner(nil, slog.New(slog.DiscardHandler))
	runner.Register(reg)
	(&mysql.Driver{Host: inst.Host(), PinDir: dir, Jobs: runner}).Register(reg)
	h, release := make(chan struct{}, 4), make(chan struct{})
	reg.SetGate(func(_ context.Context, cmd string) error {
		if cmd == "mysql.promote" {
			h <- struct{}{}
			<-release
		}
		return nil
	})
	return New(config.Config{}, reg, slog.New(slog.DiscardHandler)), h, release
}

var promoteSessionParams = proto.PromoteParams{Mode: proto.PromoteEmergency,
	OldSource:    proto.PeerCred{Host: "127.0.0.1", Port: 3399, User: "dbpanel_repl", Password: "Repl-pw-1"},
	ReplPassword: "Baru-repl-pw-1"}

// TestPromoteRequestMarkedInReadLoop: a mysql.promote read by the session
// is reported by mysql.status asked right after it on the same session,
// while its handler has not even started; once it is answered it is not.
func TestPromoteRequestMarkedInReadLoop(t *testing.T) {
	a, held, release := promoteSessionAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c, _ := newFakePanel(t).connect(t, ctx, a)
	defer close(c.ended)

	if c.promoteRequest("s0") {
		t.Fatal("promote_request true before any promote")
	}
	c.send("p1", "mysql.promote", promoteSessionParams)
	if !c.promoteRequest("s1") {
		t.Error("promote_request false right after the promote was sent")
	}
	<-held
	if !c.promoteRequest("s2") {
		t.Error("promote_request false while the promote is held before its handler")
	}
	close(release)
	if resp := c.answer("p1"); resp.OK {
		t.Fatal("promote from another source = ok")
	}
	if c.promoteRequest("s3") {
		t.Error("promote_request true after the promote was refused")
	}
}

// TestPromoteRequestMarkSurvivesReconnect: a mysql.promote read on a
// session that then closed, its handler still held, is reported by
// mysql.status on the next session.
func TestPromoteRequestMarkSurvivesReconnect(t *testing.T) {
	a, held, release := promoteSessionAgent(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	p := newFakePanel(t)

	first, ended := p.connect(t, ctx, a)
	first.send("p1", "mysql.promote", promoteSessionParams)
	<-held
	close(first.ended) // the panel drops the first session
	select {
	case <-ended:
	case <-ctx.Done():
		t.Fatal("first session did not end")
	}

	second, _ := p.connect(t, ctx, a)
	defer close(second.ended)
	if !second.promoteRequest("s1") {
		t.Error("promote_request false on the next session while the promote read on the first is held")
	}
	close(release)
	deadline := time.Now().Add(time.Minute)
	for second.promoteRequest("s2") {
		if time.Now().After(deadline) {
			t.Fatal("promote_request still true after the held promote was released")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
