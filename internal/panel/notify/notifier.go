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

	// Flap control (SEC-46): an alert reopened less than flapWindow after
	// the previous one of its kind and server closed is flapping. Its open
	// message is replaced by one "berkedip N kali" message per flapWindow,
	// and a recovery goes out only once the alert stayed closed
	// recoveryHold.
	flapWindow   = 5 * time.Minute
	recoveryHold = time.Minute
	// minWait is the shortest pause Run takes before a held message is due.
	minWait = time.Second
)

// kindRepeat overrides Config.Repeat for one-time notices (spec 5.14):
// master_quarantined is sent once, master_lock_expired at most every 24 h
// (the agent repeats a lock kept). Both close after 24 h without a
// message: closing them is no recovery (quietKinds).
var (
	kindRepeat = map[string]time.Duration{
		monitor.KindMasterQuarantined: 0, // never repeated, not even after a snooze
		monitor.KindMasterLockExpired: 24 * time.Hour,
	}
	quietKinds = map[string]bool{monitor.KindMasterQuarantined: true, monitor.KindMasterLockExpired: true}
)

// unsnoozableKinds are the alerts a snooze never silences (spec 5.7): each
// still sends its first message and its recovery while the server is
// snoozed; only its repeats wait. They are the ones that can mean data
// loss or two writable masters.
var unsnoozableKinds = map[string]bool{
	monitor.KindMasterDown:           true,
	monitor.KindDiskCrit:             true,
	monitor.KindReplicaUnlocked:      true,
	monitor.KindPromotePartial:       true,
	monitor.KindMasterLockExpired:    true,
	monitor.KindMasterQuarantined:    true,
	monitor.KindMasterUnpinned:       true,
	monitor.KindMasterLockKept:       true,
	monitor.KindMasterStartUnguarded: true,
	monitor.KindMasterStartLate:      true,
	monitor.KindMasterStartBypassed:  true,
	monitor.KindMasterStartBlocked:   true,
}

// UnsnoozableKinds lists the kinds a snooze never silences, in AllKinds
// order; the snooze dialog names them.
func UnsnoozableKinds() []string {
	var out []string
	for _, k := range monitor.AllKinds() {
		if unsnoozableKinds[k] {
			out = append(out, k)
		}
	}
	return out
}

// flapKey is one server's alert kind.
type flapKey struct {
	serverID int64
	kind     string
}

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
	// flapSent is when the last "berkedip" message of a server's alert
	// kind went out; guarded by passMu, in memory only.
	flapSent map[flapKey]time.Time
	// nextDue is when the earliest message the last pass held back is
	// due (zero when none); guarded by passMu.
	nextDue time.Time

	testMu   sync.Mutex
	lastTest time.Time
}

// New returns a Notifier; Run starts its loop.
func New(d Deps) *Notifier {
	n := &Notifier{
		st: d.Store, box: d.Box, sender: d.Sender, log: d.Logger, now: d.Now,
		retryEvery: d.RetryEvery, repeat: d.Repeat, snoozed: d.Snoozed, serverName: d.ServerName,
		wake: make(chan struct{}, 1), wasSnoozed: make(map[int64]bool), flapSent: make(map[flapKey]time.Time),
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

// Run passes over the alerts at once, then every RetryEvery, after each
// Wake, and when a message the last pass held back is due, until ctx ends.
func (n *Notifier) Run(ctx context.Context) {
	t := time.NewTicker(n.retryEvery)
	defer t.Stop()
	for {
		n.Pass(ctx)
		var due <-chan time.Time
		var timer *time.Timer
		if d, ok := n.untilDue(); ok {
			timer = time.NewTimer(d)
			due = timer.C
		}
		select {
		case <-ctx.Done():
		case <-t.C:
		case <-n.wake:
		case <-due:
		}
		if timer != nil {
			timer.Stop()
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// untilDue is how long until the earliest held message is due (at least
// minWait); false when nothing is held.
func (n *Notifier) untilDue() (time.Duration, bool) {
	n.passMu.Lock()
	defer n.passMu.Unlock()
	if n.nextDue.IsZero() {
		return 0, false
	}
	return max(n.nextDue.Sub(n.now()), minWait), true
}

// holdUntil records that a message is held back until at. The caller holds
// passMu.
func (n *Notifier) holdUntil(at time.Time) {
	if n.nextDue.IsZero() || at.Before(n.nextDue) {
		n.nextDue = at
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
//   - one recovery message for a closed alert that had been sent;
//   - the digest of each snooze that has ended (sendDigests).
//
// A snoozed server's alerts wait until the snooze ends (spec 5.3), except
// those of unsnoozableKinds: they send their first message and their
// recovery during the snooze, and only their repeats wait. The
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
	n.nextDue = time.Time{}
	for k, at := range n.flapSent {
		if n.now().Sub(at) > 24*time.Hour {
			delete(n.flapSent, k) // a recovery that never came (the kind switched off)
		}
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
	if ctx.Err() != nil {
		return
	}
	n.sendDigests(ctx, cfg)
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
	if snoozed && (!unsnoozableKinds[a.Kind] || a.NotifyCount > 0) {
		// A snooze holds back everything but the first message of an
		// unsnoozable kind: it falls through to the normal path (flap
		// control, a failed send retried on the next pass).
		return
	}
	repeat, fixed := kindRepeat[a.Kind]
	if !fixed {
		repeat = cfg.Repeat
	}
	sent := a.TelegramState == store.TelegramSent && a.LastNotifiedAt != nil
	if sent && repeat == 0 {
		return // a one-time notice
	}
	if !snoozeEnded && sent && n.now().Before(a.LastNotifiedAt.Add(repeat)) {
		return
	}
	if a.NotifyCount == 0 && n.flapping(ctx, cfg, a) {
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

// flapping handles the first send of an open alert reopened less than
// flapWindow after the previous one of its kind and server closed: it
// sends "berkedip N kali" at most once per flapWindow and holds the alert
// otherwise. It reports whether it handled the alert; false leaves it to
// the normal message (not flapping, or flapWindow passed without another
// reopen).
func (n *Notifier) flapping(ctx context.Context, cfg Config, a store.Alert) bool {
	prev, err := n.st.PreviousAlertClosedAt(a.Kind, a.ServerID, a.ID)
	if err != nil {
		n.log.Warn("notify: previous alert", "alert_id", a.ID, "err", err)
		return false
	}
	if prev == nil || a.OpenedAt.Sub(*prev) >= flapWindow {
		return false
	}
	now := n.now()
	key := flapKey{a.ServerID, a.Kind}
	if last, ok := n.flapSent[key]; ok && now.Before(last.Add(flapWindow)) {
		n.holdUntil(last.Add(flapWindow))
		return true
	}
	count, err := n.st.CountAlertsOpenedSince(a.Kind, a.ServerID, now.Add(-flapWindow))
	if err != nil {
		n.log.Warn("notify: count alerts", "alert_id", a.ID, "err", err)
		return false
	}
	if count < 2 {
		return false // open for a while now: an ordinary alert
	}
	if n.deliver(ctx, cfg, a, FormatFlapping(a, n.serverName(a.ServerID), count, cfg.BaseURL), store.TelegramSent) {
		n.flapSent[key] = now
	}
	return true
}

// notifyRecovered handles a closed alert that is not yet TelegramRecovered.
// A recovery waits until the alert stayed closed recoveryHold; when the
// kind reopened meanwhile it is dropped (the reopen is flapping). An
// alert closed because its server is no longer judged says why instead.
func (n *Notifier) notifyRecovered(ctx context.Context, cfg Config, a store.Alert, snoozed bool) {
	key := flapKey{a.ServerID, a.Kind}
	flapAt, flapped := n.flapSent[key]
	if !cfg.enabled(a.Kind) || (a.NotifyCount == 0 && !flapped) || quietKinds[a.Kind] {
		// Never delivered (still pending, failed, or a switched-off kind),
		// or a notice that expired: there is nothing to recover from.
		n.setState(a, store.TelegramRecovered)
		return
	}
	if snoozed && !unsnoozableKinds[a.Kind] {
		return
	}
	name := n.serverName(a.ServerID)
	if a.CloseReason != "" {
		if a.NotifyCount == 0 {
			n.setState(a, store.TelegramRecovered)
		} else {
			n.deliver(ctx, cfg, a, FormatClosed(a, name), store.TelegramRecovered)
		}
		delete(n.flapSent, key)
		return
	}
	if held := a.ClosedAt.Add(recoveryHold); n.now().Before(held) {
		n.holdUntil(held)
		return
	}
	later, err := n.st.LaterAlertExists(a.Kind, a.ServerID, a.ID, a.ClosedAt.Add(recoveryHold))
	if err != nil {
		n.log.Warn("notify: later alert", "alert_id", a.ID, "err", err)
		return
	}
	if later {
		n.setState(a, store.TelegramRecovered) // reopened: flapping, not recovered
		return
	}
	if a.NotifyCount == 0 {
		// The last of a flapping series, held back: the flapping is over.
		a.OpenedAt = flapAt
	}
	if n.deliver(ctx, cfg, a, FormatRecovered(a, name, *a.ClosedAt), store.TelegramRecovered) {
		delete(n.flapSent, key)
	}
}

// sendDigests sends one message per snooze that has ended, listing the
// alerts opened during it (FormatSnoozeDigest). What is owed lives in the
// store (snooze_from), so a snooze that ended while the panel was down is
// still answered; a failed send leaves it owed for the next pass, and it
// is cleared only after Telegram accepted the message.
func (n *Notifier) sendDigests(ctx context.Context, cfg Config) {
	spans, err := n.st.EndedSnoozes(n.now())
	if err != nil {
		n.log.Warn("notify: ended snoozes", "err", err)
		return
	}
	for _, sp := range spans {
		alerts, err := n.st.AlertsOpenedBetween(sp.ServerID, sp.From, sp.Until)
		if err != nil {
			n.log.Warn("notify: alerts of a snooze", "server_id", sp.ServerID, "err", err)
			continue
		}
		text := FormatSnoozeDigest(n.serverName(sp.ServerID), sp.From, sp.Until, alerts, cfg.BaseURL)
		if err := n.sender.Send(ctx, cfg.Token, cfg.ChatID, text); err != nil {
			if ctx.Err() != nil {
				return
			}
			n.log.Warn("notify: snooze digest failed", "server_id", sp.ServerID, "err", scrub(err.Error(), cfg.Token))
			continue
		}
		n.log.Info("notify: snooze digest sent", "server_id", sp.ServerID, "alerts", len(alerts))
		if err := n.st.ClearSnoozeFrom(sp.ServerID); err != nil {
			n.log.Warn("notify: clear snooze start", "server_id", sp.ServerID, "err", err)
		}
	}
}

// deliver sends text and records the attempt: okState on success, failed
// with the reason otherwise. It reports whether the message went out.
func (n *Notifier) deliver(ctx context.Context, cfg Config, a store.Alert, text, okState string) bool {
	err := n.sender.Send(ctx, cfg.Token, cfg.ChatID, text)
	if err != nil && ctx.Err() != nil {
		return false // shutting down: not a failure of Telegram's; the next start retries
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
	return err == nil
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

// SendWeekly sends the weekly summary text with the saved settings
// (SendText). It is no alert: the alert kinds do not filter it, a snooze
// does not hold it back, and nothing is recorded. Without a bot token and
// chat id it returns ErrNotConfigured.
func (n *Notifier) SendWeekly(ctx context.Context, text string) error {
	if err := n.SendText(ctx, text); err != nil {
		if !errors.Is(err, ErrNotConfigured) {
			n.log.Warn("notify: telegram weekly summary failed", "err", err)
		}
		return err
	}
	n.log.Info("notify: telegram weekly summary sent")
	return nil
}

// SendText sends text with the saved settings, as it is: no alert kind
// filters it, no snooze holds it back, and nothing is recorded (the
// weekly summary, the result of "Periksa data tertinggal"). Without a bot
// token and chat id it returns ErrNotConfigured; a failure's text never
// carries the bot token.
func (n *Notifier) SendText(ctx context.Context, text string) error {
	cfg, err := LoadConfig(n.st, n.box)
	if err != nil {
		return err
	}
	if !cfg.configured() {
		return ErrNotConfigured
	}
	if err := n.sender.Send(ctx, cfg.Token, cfg.ChatID, text); err != nil {
		return errors.New(scrub(err.Error(), cfg.Token))
	}
	return nil
}
