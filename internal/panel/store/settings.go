package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
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

// Setting keys of monitoring and notifications. Defaults for keys that are
// not set live with the code that uses them.
const (
	SettingTelegramTokenEnc    = "telegram_token_enc"
	SettingTelegramChatID      = "telegram_chat_id"
	SettingTelegramKinds       = "telegram_kinds_json"
	SettingPanelBaseURL        = "panel_base_url"
	SettingMonitorCheckEvery   = "monitor_check_every"
	SettingMonitorSummaryEvery = "monitor_summary_every"
	SettingLagThreshold        = "lag_threshold_sec"
	SettingDisconnectAlert     = "disconnect_alert_sec"
	SettingDownAfter           = "down_after_sec"
	SettingDiskWarn            = "disk_warn_pct"
	SettingDiskCrit            = "disk_crit_pct"
	SettingWeeklySummary       = "weekly_summary"
	SettingWeeklySummaryAt     = "weekly_summary_at"
	SettingAlertRepeatHours    = "alert_repeat_hours"
)

// GetSettings returns the values of the given keys that are set; a key that
// is not set is absent from the map.
func (s *Store) GetSettings(keys ...string) (map[string]string, error) {
	out := make(map[string]string, len(keys))
	if len(keys) == 0 {
		return out, nil
	}
	args := make([]any, len(keys))
	for i, k := range keys {
		args[i] = k
	}
	rows, err := s.db.Query(`SELECT key, value FROM settings WHERE key IN (?`+strings.Repeat(`, ?`, len(keys)-1)+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("get settings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("scan setting: %w", err)
		}
		out[k] = v
	}
	return out, rows.Err()
}
