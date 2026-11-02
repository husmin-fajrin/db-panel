package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrAlertOpen is OpenAlert's error when an alert of the same kind is
// already open for the server.
var ErrAlertOpen = errors.New("peringatan sudah terbuka")

// Telegram delivery states of an alert.
const (
	TelegramPending = "pending"
	TelegramSent    = "sent"
	TelegramFailed  = "failed"
	// TelegramRecovered: the alert is closed and needs nothing more; its
	// recovery message went out, or there was nothing to recover from.
	TelegramRecovered = "recovered_sent"
)

// Alert is a condition that needs attention, open until ClosedAt is set.
type Alert struct {
	ID                           int64
	Kind                         string
	ServerID, TopologyID         int64
	OpenedAt                     time.Time
	ClosedAt, LastNotifiedAt     *time.Time
	NotifyCount                  int
	Message                      string
	TelegramState, TelegramError string
	// CloseReason says, in words, why a closed alert did not recover (its
	// server stopped being judged); "" for a recovery or an open alert.
	CloseReason string
}

const alertColumns = `id, kind, server_id, topology_id, opened_at, closed_at, last_notified_at, notify_count, message, telegram_state, telegram_error, close_reason`

func scanAlert(r rowScanner) (Alert, error) {
	var (
		a        Alert
		opened   string
		closed   sql.NullString
		notified sql.NullString
	)
	if err := r.Scan(&a.ID, &a.Kind, &a.ServerID, &a.TopologyID, &opened, &closed, &notified,
		&a.NotifyCount, &a.Message, &a.TelegramState, &a.TelegramError, &a.CloseReason); err != nil {
		return Alert{}, err
	}
	var err error
	if a.OpenedAt, err = parseTime(opened); err != nil {
		return Alert{}, err
	}
	if a.ClosedAt, err = parseNullTime(closed); err != nil {
		return Alert{}, err
	}
	if a.LastNotifiedAt, err = parseNullTime(notified); err != nil {
		return Alert{}, err
	}
	return a, nil
}

func (s *Store) queryAlerts(query string, args ...any) ([]Alert, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, fmt.Errorf("scan alert: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// OpenAlert opens an alert (ClosedAt and the notification fields of a are
// ignored; it starts pending) and returns its id. It fails with ErrAlertOpen
// when an alert of the same kind is already open for the server.
func (s *Store) OpenAlert(a Alert) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO alerts (kind, server_id, topology_id, opened_at, message) VALUES (?, ?, ?, ?, ?)`,
		a.Kind, a.ServerID, a.TopologyID, formatTime(a.OpenedAt), a.Message)
	if err != nil {
		// A partial unique index reports its columns, not its name.
		if strings.Contains(err.Error(), "UNIQUE constraint failed: alerts.kind, alerts.server_id") {
			return 0, ErrAlertOpen
		}
		return 0, fmt.Errorf("open alert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("open alert: %w", err)
	}
	return id, nil
}

// GetOpenAlert returns the open alert of kind for the server, or
// ErrNotFound.
func (s *Store) GetOpenAlert(kind string, serverID int64) (Alert, error) {
	a, err := scanAlert(s.db.QueryRow(`SELECT `+alertColumns+` FROM alerts
		WHERE kind = ? AND server_id = ? AND closed_at IS NULL`, kind, serverID))
	if errors.Is(err, sql.ErrNoRows) {
		return Alert{}, ErrNotFound
	}
	if err != nil {
		return Alert{}, fmt.Errorf("get open alert: %w", err)
	}
	return a, nil
}

// CloseAlert closes an open alert; ErrNotFound if there is none with this id
// or it is already closed.
func (s *Store) CloseAlert(id int64, now time.Time) error {
	return s.CloseAlertWithReason(id, now, "")
}

// CloseAlertWithReason closes an open alert that did not recover, saying
// why (Alert.CloseReason); "" is a recovery.
func (s *Store) CloseAlertWithReason(id int64, now time.Time, reason string) error {
	return s.execOne(`UPDATE alerts SET closed_at = ?, close_reason = ? WHERE id = ? AND closed_at IS NULL`,
		formatTime(now), reason, id)
}

// PreviousAlertClosedAt is when the alert of kind for the server before
// alert id (the newest older one) closed; nil when there is none or it is
// still open.
func (s *Store) PreviousAlertClosedAt(kind string, serverID, id int64) (*time.Time, error) {
	var closed sql.NullString
	err := s.db.QueryRow(`SELECT closed_at FROM alerts WHERE kind = ? AND server_id = ? AND id < ?
		ORDER BY id DESC LIMIT 1`, kind, serverID, id).Scan(&closed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("previous alert: %w", err)
	}
	return parseNullTime(closed)
}

// LaterAlertExists reports whether an alert of kind for the server was
// opened after alert id and before before.
func (s *Store) LaterAlertExists(kind string, serverID, id int64, before time.Time) (bool, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE kind = ? AND server_id = ? AND id > ? AND opened_at < ?`,
		kind, serverID, id, formatTime(before)).Scan(&n); err != nil {
		return false, fmt.Errorf("later alert: %w", err)
	}
	return n > 0, nil
}

// CountAlertsOpenedSince counts the alerts of kind for the server opened
// at or after since.
func (s *Store) CountAlertsOpenedSince(kind string, serverID int64, since time.Time) (int, error) {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM alerts WHERE kind = ? AND server_id = ? AND opened_at >= ?`,
		kind, serverID, formatTime(since)).Scan(&n); err != nil {
		return 0, fmt.Errorf("count alerts: %w", err)
	}
	return n, nil
}

// ListOpenAlerts returns all open alerts, oldest first.
func (s *Store) ListOpenAlerts() ([]Alert, error) {
	return s.queryAlerts(`SELECT ` + alertColumns + ` FROM alerts WHERE closed_at IS NULL ORDER BY opened_at, id`)
}

// ListAlerts returns alerts opened at or after since, newest first, at most
// limit.
func (s *Store) ListAlerts(since time.Time, limit int) ([]Alert, error) {
	return s.queryAlerts(`SELECT `+alertColumns+` FROM alerts WHERE opened_at >= ?
		ORDER BY opened_at DESC, id DESC LIMIT ?`, formatTime(since), limit)
}

// SetAlertNotified records a Telegram delivery attempt: its time, state
// (TelegramSent or TelegramFailed) and error text. A sent notification
// raises notify_count.
func (s *Store) SetAlertNotified(id int64, now time.Time, state, errText string) error {
	inc := 0
	if state == TelegramSent {
		inc = 1
	}
	return s.execOne(`UPDATE alerts SET last_notified_at = ?, telegram_state = ?, telegram_error = ?,
		notify_count = notify_count + ? WHERE id = ?`, formatTime(now), state, errText, inc, id)
}

// ListAlertsToNotify returns the alerts the notifier still has work on:
// open ones, and closed ones not yet TelegramRecovered; oldest first.
func (s *Store) ListAlertsToNotify() ([]Alert, error) {
	return s.queryAlerts(`SELECT `+alertColumns+` FROM alerts
		WHERE closed_at IS NULL OR telegram_state != ? ORDER BY opened_at, id`, TelegramRecovered)
}

// SetAlertTelegramState sets an alert's Telegram state and error text
// without recording a delivery: notify_count and last_notified_at stay.
func (s *Store) SetAlertTelegramState(id int64, state, errText string) error {
	return s.execOne(`UPDATE alerts SET telegram_state = ?, telegram_error = ? WHERE id = ?`, state, errText, id)
}

// SetAlertMessage replaces an alert's message.
func (s *Store) SetAlertMessage(id int64, message string) error {
	return s.execOne(`UPDATE alerts SET message = ? WHERE id = ?`, message, id)
}
