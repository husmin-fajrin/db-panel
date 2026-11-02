package notify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/secrets"
)

const (
	defaultRetryEvery = 60 * time.Second // spec 5.3: failed sends retried, fixed backoff
	testEvery         = 10 * time.Second // at most one test message this often
	testText          = "Tes dari dbpanel: notifikasi berfungsi."
)

var (
	// ErrNotConfigured: no bot token or no chat id is set.
	ErrNotConfigured = errors.New("Telegram belum diatur")
	// ErrTestTooSoon answers a SendTest within 10 s of the previous one.
	ErrTestTooSoon = errors.New("tunggu 10 detik sebelum mengirim tes lagi")
)

// Sender delivers one message to a chat; *Telegram is the real one.
type Sender interface {
	Send(ctx context.Context, token, chatID, text string) error
}

// Config is what sending needs, read from the settings on every pass.
type Config struct {
	Token, ChatID string
	// Kinds switches alert kinds on and off; a kind not listed is on.
	Kinds   map[string]bool
	BaseURL string        // the panel's address for links; "" leaves links out
	Repeat  time.Duration // re-send an alert still open this often
}

func (c Config) configured() bool { return c.Token != "" && c.ChatID != "" }

func (c Config) enabled(kind string) bool {
	on, ok := c.Kinds[kind]
	return !ok || on
}

// LoadConfig reads the Telegram settings; the token is opened from its
// sealed form (SettingTelegramTokenEnc). Errors never hold the token.
func LoadConfig(s *store.Store, box *secrets.Box) (Config, error) {
	kv, err := s.GetSettings(store.SettingTelegramTokenEnc, store.SettingTelegramChatID, store.SettingTelegramKinds)
	if err != nil {
		return Config{}, err
	}
	set, err := monitor.LoadSettings(s)
	if err != nil {
		return Config{}, err
	}
	cfg := Config{
		ChatID:  strings.TrimSpace(kv[store.SettingTelegramChatID]),
		BaseURL: strings.TrimRight(strings.TrimSpace(set.PanelBaseURL), "/"),
		Repeat:  set.AlertRepeat,
	}
	if enc := kv[store.SettingTelegramTokenEnc]; enc != "" {
		sealed, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			return Config{}, errors.New("token Telegram tersimpan rusak; simpan ulang token bot")
		}
		plain, err := box.Open(sealed)
		if err != nil {
			return Config{}, errors.New("token Telegram tidak bisa dibuka dengan panel.key; simpan ulang token bot")
		}
		cfg.Token = string(plain)
	}
	if k := kv[store.SettingTelegramKinds]; k != "" {
		if err := json.Unmarshal([]byte(k), &cfg.Kinds); err != nil {
			return Config{}, fmt.Errorf("pengaturan jenis peringatan Telegram rusak: %w", err)
		}
	}
	return cfg, nil
}

// SaveToken seals the bot token with the panel's key and stores it; ""
// removes it. A token not of the form "123456:ABC-def..." is refused.
func SaveToken(s *store.Store, box *secrets.Box, token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return s.SetSetting(store.SettingTelegramTokenEnc, "")
	}
	if !tokenRe.MatchString(token) {
		return errors.New("token bot tidak valid: bentuknya angka, titik dua, lalu kode dari BotFather")
	}
	sealed, err := box.Seal([]byte(token))
	if err != nil {
		return fmt.Errorf("segel token Telegram: %w", err)
	}
	return s.SetSetting(store.SettingTelegramTokenEnc, base64.StdEncoding.EncodeToString(sealed))
}

// Deps are what a Notifier works with.
type Deps struct {
	Store  *store.Store
	Box    *secrets.Box
	Sender Sender
	Logger *slog.Logger     // nil means slog.Default()
	Now    func() time.Time // nil means time.Now
	// RetryEvery paces the passes, and so the retries of failed sends; 0
	// means 60 s.
	RetryEvery time.Duration
	// Repeat overrides the alert_repeat_hours setting (6 h by default)
	// when > 0.
	Repeat time.Duration
	// Snoozed reports whether the server's notifications are muted now;
	// nil means never.
	Snoozed func(serverID int64) bool
	// ServerName names a server in messages; nil means "server <id>".
	ServerName func(serverID int64) string
}

// Notifier sends the alerts to Telegram (spec 5.3): each new alert once,
// again every Repeat while it stays open, and one recovery message when it
// closes. A failed send is marked failed, with its reason, and retried on
// the next pass. The message text changing (a disk percentage) never
// causes a send of its own.
type Notifier struct {
	st         *store.Store
	box        *secrets.Box
	sender     Sender
	log        *slog.Logger
	now        func() time.Time
	retryEvery time.Duration
	repeat     time.Duration
	snoozed    func(int64) bool
	serverName func(int64) string

	wake   chan struct{}
	passMu sync.Mutex // one pass at a time, so nothing is sent twice
	// wasSnoozed holds the servers found snoozed by the last pass that
	// looked at them; guarded by passMu. Kept in memory only: after a
	// restart a snooze that ended meanwhile sends no reminder.
	wasSnoozed map[int64]bool

	testMu   sync.Mutex
	lastTest time.Time
}

// New returns a Notifier; Run starts its loop.
func New(d Deps) *Notifier {
	n := &Notifier{
		st: d.Store, box: d.Box, sender: d.Sender, log: d.Logger, now: d.Now,
		retryEvery: d.RetryEvery, repeat: d.Repeat, snoozed: d.Snoozed, serverName: d.ServerName,
		wake: make(chan struct{}, 1), wasSnoozed: make(map[int64]bool),
	}
	if n.log == nil {
		n.log = slog.Default()
	}
	if n.now == nil {
		n.now = time.Now
	}
	if n.retryEvery <= 0 {
		n.retryEvery = defaultRetryEvery
	}
	if n.snoozed == nil {
		n.snoozed = func(int64) bool { return false }
	}
	if n.serverName == nil {
		n.serverName = func(id int64) string { return fmt.Sprintf("server %d", id) }
	}
	return n
}

// Run passes over the alerts at once, then every RetryEvery and after each
// Wake, until ctx ends.
func (n *Notifier) Run(ctx context.Context) {
	t := time.NewTicker(n.retryEvery)
	defer t.Stop()
	for {
		n.Pass(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-n.wake:
		}
	}
}

// Wake asks Run for a pass now (an alert opened or closed). It never
// blocks; wakes while a pass is due fold into it.
func (n *Notifier) Wake() {
	select {
	case n.wake <- struct{}{}:
	default:
	}
}

// Pass sends what is due, oldest alert first:
//   - an open alert never sent, or whose last send failed;
//   - an open alert sent longer than Repeat ago;
//   - one recovery message for a closed alert that had been sent.
//
// A snoozed server's alerts wait until the snooze ends (spec 5.3). The
// first pass after that sends each of its alerts still open once, as a
// reminder, whether or not a repeat is due, and the repeat clock restarts
// from that send; the recovery messages of alerts that closed meanwhile go
// out too. Without a bot token and chat id nothing happens and the alerts
// stay pending.
func (n *Notifier) Pass(ctx context.Context) {
	n.passMu.Lock()
	defer n.passMu.Unlock()
	cfg, err := LoadConfig(n.st, n.box)
	if err != nil {
		n.log.Warn("notify: load telegram settings", "err", err)
		return
	}
	if !cfg.configured() {
		return
	}
	if n.repeat > 0 {
		cfg.Repeat = n.repeat
	}
	alerts, err := n.st.ListAlertsToNotify()
	if err != nil {
		n.log.Warn("notify: list alerts", "err", err)
		return
	}
	snoozed := make(map[int64]bool)
	for _, a := range alerts {
		if ctx.Err() != nil {
			return // the snooze ends not yet handled stay in wasSnoozed
		}
		s, ok := snoozed[a.ServerID]
		if !ok {
			s = n.snoozed(a.ServerID)
			snoozed[a.ServerID] = s
		}
		if a.ClosedAt != nil {
			n.notifyRecovered(ctx, cfg, a, s)
		} else {
			n.notify(ctx, cfg, a, s, !s && n.wasSnoozed[a.ServerID])
		}
	}
	for id, s := range snoozed {
		if s {
			n.wasSnoozed[id] = true
		} else {
			delete(n.wasSnoozed, id)
		}
	}
}

// notify handles an open alert; snoozeEnded sends it even when no repeat
// is due.
func (n *Notifier) notify(ctx context.Context, cfg Config, a store.Alert, snoozed, snoozeEnded bool) {
	if !cfg.enabled(a.Kind) {
		// A switched-off kind is marked sent without sending, so it never
		// shows as pending; notify_count stays 0 (nothing was delivered),
		// and with no last_notified_at it goes out if the kind is
		// switched on while the alert is still open.
		if a.TelegramState != store.TelegramSent {
			n.setState(a, store.TelegramSent)
		}
		return
	}
	if snoozed {
		return
	}
	if !snoozeEnded && a.TelegramState == store.TelegramSent && a.LastNotifiedAt != nil &&
		n.now().Before(a.LastNotifiedAt.Add(cfg.Repeat)) {
		return
	}
	master := ""
	if a.Kind == monitor.KindReplicaStopped && a.TopologyID != 0 {
		if topo, err := n.st.GetTopology(a.TopologyID); err == nil {
			master = n.serverName(topo.MasterServerID)
		}
	}
	text := Format(a, n.serverName(a.ServerID), master, cfg.BaseURL, n.now())
	n.deliver(ctx, cfg, a, text, store.TelegramSent)
}

// notifyRecovered handles a closed alert that is not yet TelegramRecovered.
func (n *Notifier) notifyRecovered(ctx context.Context, cfg Config, a store.Alert, snoozed bool) {
	if a.NotifyCount == 0 || !cfg.enabled(a.Kind) {
		// Never delivered (still pending, failed, or a switched-off kind):
		// there is nothing to recover from.
		n.setState(a, store.TelegramRecovered)
		return
	}
	if snoozed {
		return
	}
	n.deliver(ctx, cfg, a, FormatRecovered(a, n.serverName(a.ServerID), *a.ClosedAt), store.TelegramRecovered)
}

// deliver sends text and records the attempt: okState on success, failed
// with the reason otherwise.
func (n *Notifier) deliver(ctx context.Context, cfg Config, a store.Alert, text, okState string) {
	err := n.sender.Send(ctx, cfg.Token, cfg.ChatID, text)
	if err != nil && ctx.Err() != nil {
		return // shutting down: not a failure of Telegram's; the next start retries
	}
	state, reason := okState, ""
	if err != nil {
		state, reason = store.TelegramFailed, scrub(err.Error(), cfg.Token)
		n.log.Warn("notify: telegram send failed", "alert_id", a.ID, "kind", a.Kind, "server_id", a.ServerID, "err", reason)
	} else {
		n.log.Info("notify: telegram sent", "alert_id", a.ID, "kind", a.Kind, "server_id", a.ServerID, "state", okState)
	}
	if err := n.st.SetAlertNotified(a.ID, n.now(), state, reason); err != nil {
		n.log.Warn("notify: record telegram state", "alert_id", a.ID, "err", err)
	}
}

func (n *Notifier) setState(a store.Alert, state string) {
	if err := n.st.SetAlertTelegramState(a.ID, state, ""); err != nil {
		n.log.Warn("notify: record telegram state", "alert_id", a.ID, "err", err)
	}
}

// SendTest sends a test message with the saved settings, at most once per
// 10 s.
func (n *Notifier) SendTest(ctx context.Context) error {
	cfg, err := LoadConfig(n.st, n.box)
	if err != nil {
		return err
	}
	if !cfg.configured() {
		return ErrNotConfigured
	}
	n.testMu.Lock()
	now := n.now()
	if !n.lastTest.IsZero() && now.Sub(n.lastTest) < testEvery {
		n.testMu.Unlock()
		return ErrTestTooSoon
	}
	n.lastTest = now
	n.testMu.Unlock()
	if err := n.sender.Send(ctx, cfg.Token, cfg.ChatID, testText); err != nil {
		reason := scrub(err.Error(), cfg.Token)
		n.log.Warn("notify: telegram test failed", "err", reason)
		return errors.New(reason)
	}
	return nil
}

// SendWeekly sends the weekly summary text with the saved settings. It is
// no alert: the alert kinds do not filter it, a snooze does not hold it
// back, and nothing is recorded. Without a bot token and chat id it returns
// ErrNotConfigured.
func (n *Notifier) SendWeekly(ctx context.Context, text string) error {
	cfg, err := LoadConfig(n.st, n.box)
	if err != nil {
		return err
	}
	if !cfg.configured() {
		return ErrNotConfigured
	}
	if err := n.sender.Send(ctx, cfg.Token, cfg.ChatID, text); err != nil {
		reason := scrub(err.Error(), cfg.Token)
		n.log.Warn("notify: telegram weekly summary failed", "err", reason)
		return errors.New(reason)
	}
	n.log.Info("notify: telegram weekly summary sent")
	return nil
}
