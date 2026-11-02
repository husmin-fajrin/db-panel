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
}

const alertColumns = `id, kind, server_id, topology_id, opened_at, closed_at, last_notified_at, notify_count, message, telegram_state, telegram_error`

func scanAlert(r rowScanner) (Alert, error) {
	var (
		a        Alert
		opened   string
		closed   sql.NullString
		notified sql.NullString
	)
	if err := r.Scan(&a.ID, &a.Kind, &a.ServerID, &a.TopologyID, &opened, &closed, &notified,
		&a.NotifyCount, &a.Message, &a.TelegramState, &a.TelegramError); err != nil {
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
	return s.execOne(`UPDATE alerts SET closed_at = ? WHERE id = ? AND closed_at IS NULL`, formatTime(now), id)
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
