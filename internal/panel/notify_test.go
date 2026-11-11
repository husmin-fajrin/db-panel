package panel

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/notify"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/secrets"
)

// TestNotifierWiredToMonitor: alerts the Monitor opens and closes reach
// Telegram through the running panel without waiting for the retry tick,
// and a snoozed server's alert waits.
func TestNotifierWiredToMonitor(t *testing.T) {
	t.Parallel()
	o := testOptions(t)
	p := newPanel(t, o)

	var (
		mu    sync.Mutex
		texts []string
	)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var m struct{ Text string }
		_ = json.NewDecoder(r.Body).Decode(&m)
		mu.Lock()
		texts = append(texts, m.Text)
		mu.Unlock()
		w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(api.Close)
	sent := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), texts...)
	}
	p.telegram.BaseURL = api.URL
	box, err := secrets.LoadOrCreate(filepath.Join(o.ConfigDir, "panel.key"))
	if err != nil {
		t.Fatal(err)
	}
	if err := notify.SaveToken(p.Store, box, "123:abcDEF"); err != nil {
		t.Fatal(err)
	}
	if err := p.Store.SetSetting(store.SettingTelegramChatID, "-1"); err != nil {
		t.Fatal(err)
	}

	// The panel's clock can be moved ahead: a recovery waits until the
	// alert stayed closed a minute (flap control).
	var ahead atomic.Int64
	p.now = func() time.Time { return time.Now().Add(time.Duration(ahead.Load())) }

	p.Monitor.JobFailed(p.localID, "job-1", "create_replica", "Salin data", "")
	startPanel(t, p)
	waitFor(t, "the open alert sent by the first pass", func() bool { return len(sent()) == 1 })
	if got := sent()[0]; !strings.Contains(got, "Pekerjaan Buat replika gagal") {
		t.Fatalf("message = %q", got)
	}
	// The retry tick is a minute away: the Monitor's wake-up starts a pass,
	// which holds the recovery and wakes again once it is due.
	p.Monitor.JobEnded(p.localID, "job-1")
	ahead.Store(int64(61 * time.Second))
	waitFor(t, "the recovery message", func() bool { return len(sent()) == 2 })
	if got := sent()[1]; !strings.Contains(got, "pulih: pekerjaan gagal") {
		t.Fatalf("recovery message = %q", got)
	}

	until := time.Now().Add(time.Hour)
	if err := p.Monitor.Snooze(p.localID, &until); err != nil {
		t.Fatal(err)
	}
	p.Monitor.JobFailed(p.localID, "job-2", "create_replica", "", "")
	p.Notifier.Pass(context.Background())
	a, err := p.Store.GetOpenAlert("job_failed", p.localID)
	if err != nil || a.TelegramState != store.TelegramPending || len(sent()) != 2 {
		t.Fatalf("snoozed: alert %+v (%v), messages %q", a, err, sent())
	}
	if err := p.Monitor.Snooze(p.localID, nil); err != nil {
		t.Fatal(err)
	}
	p.Notifier.Pass(context.Background())
	// The held alert goes out, and so does the digest of the snooze that
	// the cancel ended (it opened no new alert besides that one).
	got := sent()
	if len(got) != 4 || !strings.Contains(got[2], "pekerjaan gagal berkedip") ||
		!strings.HasPrefix(got[3], "Jangan ganggu untuk ") {
		t.Fatalf("after the snooze: messages %q", got)
	}
}
