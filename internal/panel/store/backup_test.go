package store

import (
	"errors"
	"testing"
)

// TestSetServerBackupRoundTrip: a new server has no backup folder and the
// default limit of 48 hours; SetServerBackup stores both as given, every
// reader of the server row sees them, and an unknown server is
// ErrNotFound.
func TestSetServerBackupRoundTrip(t *testing.T) {
	s := openTemp(t)
	srv, err := s.CreateServer("db1")
	if err != nil {
		t.Fatal(err)
	}
	if srv.BackupDir != "" || srv.BackupMaxAgeHours != 48 {
		t.Fatalf("new server: dir %q, max age %d; want \"\", 48", srv.BackupDir, srv.BackupMaxAgeHours)
	}
	if err := s.SetServerBackup(srv.ID, "/srv/backup/mysql", 24); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetServer(srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.BackupDir != "/srv/backup/mysql" || got.BackupMaxAgeHours != 24 {
		t.Fatalf("after SetServerBackup: dir %q, max age %d", got.BackupDir, got.BackupMaxAgeHours)
	}
	list, err := s.ListServers()
	if err != nil || len(list) != 1 || list[0].BackupDir != "/srv/backup/mysql" || list[0].BackupMaxAgeHours != 24 {
		t.Fatalf("ListServers = %+v, %v", list, err)
	}

	// Clearing the folder keeps the limit given.
	if err := s.SetServerBackup(srv.ID, "", 48); err != nil {
		t.Fatal(err)
	}
	if got, err = s.GetServer(srv.ID); err != nil || got.BackupDir != "" || got.BackupMaxAgeHours != 48 {
		t.Fatalf("after clearing: %+v, %v", got, err)
	}

	if err := s.SetServerBackup(srv.ID+100, "/srv/backup", 48); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetServerBackup on an unknown server = %v, want ErrNotFound", err)
	}
}
