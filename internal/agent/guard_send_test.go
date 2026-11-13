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
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/monitor"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// timedSource is the driver as the monitor's Source, recording when each
// guard round (GuardTick) started and ended.
type timedSource struct {
	*mysql.Driver
	mu         sync.Mutex
	starts     []time.Time
	ends       []time.Time
	roundsDone chan struct{} // closed once rounds have ended
	rounds     int
}

func (s *timedSource) GuardTick(ctx context.Context, now time.Time) error {
	start := time.Now()
	err := s.Driver.GuardTick(ctx, now)
	end := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts, s.ends = append(s.starts, start), append(s.ends, end)
	if len(s.ends) == s.rounds {
		close(s.roundsDone)
	}
	return err
}

// TestGuardRoundsNotDelayedByNonDrainingPanel (R-117): a panel that stops
// reading its websocket holds the agent's writes; the guard's rounds, which
// keep a quarantined server read-only, run on time all the same: the guard
// only queues its events and one sender beside it sends them.
func TestGuardRoundsNotDelayedByNonDrainingPanel(t *testing.T) {
	// A quarantined server whose MySQL is writable: its first guard round
	// puts read-only back and has a late start and a quarantine notice for
	// the panel. Started first: the socket below stays blocked only until
	// the stuck write times out (10 s).
	inst := testmysql.Start(t, testmysql.Options{})
	dir := t.TempDir()
	for name, v := range map[string]string{mysql.RoleFile: "quarantined", mysql.SourceFile: "10.9.9.9:3306"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(v+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := &mysql.Driver{Host: inst.Host(), PinDir: dir,
		SeenPath:   filepath.Join(t.TempDir(), "replicas-seen"),
		SalvageDir: filepath.Join(t.TempDir(), "salvage"),
		Logger:     slog.New(slog.DiscardHandler)}
	d.Register(commands.NewRegistry())

	// A panel that accepts the session and never reads from it.
	srvDone := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Errorf("accept: %v", err)
			return
		}
		defer ws.CloseNow()
		<-srvDone
	}))
	defer srv.Close()
	defer close(srvDone)

	a := New(config.Config{}, commands.NewRegistry(), slog.New(slog.DiscardHandler))
	d.Events = a
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(srv.URL, "http"), nil)
	if err != nil {
		t.Fatal(err)
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		a.serve(ctx, ws)
	}()
	defer func() {
		cancel()
		<-served
	}()
	for a.Emit(proto.Event{Type: "probe"}) != nil {
		if ctx.Err() != nil {
			t.Fatal("session never came up")
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Fill the socket with 1 MiB events until one Emit blocks: it holds
	// the session's write lock until its write times out.
	big, _ := json.Marshal(strings.Repeat("x", 1<<20))
	progress := make(chan struct{}, 1)
	go func() {
		for a.Emit(proto.Event{Type: "filler", Payload: big}) == nil {
			select {
			case progress <- struct{}{}:
			default:
			}
		}
	}()
	for blocked := false; !blocked; {
		select {
		case <-progress:
		case <-time.After(time.Second):
			blocked = true
		case <-ctx.Done():
			t.Fatal("the socket never filled")
		}
	}

	const rounds = 4
	src := &timedSource{Driver: d, rounds: rounds, roundsDone: make(chan struct{})}
	mctx, mcancel := context.WithCancel(ctx)
	ran := make(chan struct{})
	go func() {
		defer close(ran)
		monitor.New(src, a, slog.New(slog.DiscardHandler), monitor.Options{Pins: d.Pins()}).Run(mctx)
	}()
	select {
	case <-src.roundsDone:
	case <-ctx.Done():
		t.Fatal("guard rounds did not run")
	}
	mcancel()
	<-ran

	src.mu.Lock()
	defer src.mu.Unlock()
	for i := range rounds {
		if took := src.ends[i].Sub(src.starts[i]); took > 2*time.Second {
			t.Errorf("guard round %d took %v, want under 2 s", i, took)
		}
		if i+1 < rounds {
			if gap := src.starts[i+1].Sub(src.ends[i]); gap > monitor.GuardEvery+time.Second {
				t.Errorf("guard round %d started %v after round %d ended, want at most %v", i+1, gap, i, monitor.GuardEvery+time.Second)
			}
		}
	}
	var ro bool
	db, err := inst.Host().Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.QueryRow("SELECT @@global.super_read_only").Scan(&ro); err != nil || !ro {
		t.Errorf("super_read_only after the rounds = %v (%v), want on", ro, err)
	}
}
