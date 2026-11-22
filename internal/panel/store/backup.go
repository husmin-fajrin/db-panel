package store

import (
	"errors"
	"fmt"
)

// SetServerBackup stores the server's backup folder (dir, "" for none)
// and the age in hours past which its newest file is stale, as given: the
// form that takes them checks them. ErrNotFound for an unknown server.
func (s *Store) SetServerBackup(id int64, dir string, maxAgeHours int) error {
	err := s.execOne(`UPDATE servers SET backup_dir = ?, backup_max_age_hours = ? WHERE id = ?`, dir, maxAgeHours, id)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return fmt.Errorf("set server backup: %w", err)
	}
	return err
}
