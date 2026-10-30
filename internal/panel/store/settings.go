package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// GetSetting returns the value for key, or "" if it is not set.
func (s *Store) GetSetting(key string) (string, error) {
	var v string
	err := s.db.QueryRow(`SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("get setting: %w", err)
	}
	return v, nil
}

// SetSetting upserts a setting.
func (s *Store) SetSetting(key, value string) error {
	_, err := s.db.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("set setting: %w", err)
	}
	return nil
}

// settingSessionEpoch holds the unix time (seconds) of the last password
// reset or change; sessions created at or before it are no longer valid.
const settingSessionEpoch = "session_epoch"

// SessionEpoch returns the session epoch in unix seconds, 0 if never set.
func (s *Store) SessionEpoch() (int64, error) {
	v, err := s.GetSetting(settingSessionEpoch)
	if err != nil || v == "" {
		return 0, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse %s %q: %w", settingSessionEpoch, v, err)
	}
	return n, nil
}

// BumpSessionEpoch sets the session epoch to now, ending every session that
// already exists (also in a panel process other than the caller).
func (s *Store) BumpSessionEpoch(now time.Time) error {
	return s.SetSetting(settingSessionEpoch, strconv.FormatInt(now.Unix(), 10))
}
