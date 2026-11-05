package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ServerTLS records the MySQL server certificate the panel installed on a
// server.
type ServerTLS struct {
	ServerID    int64
	CertSerial  string
	NotAfter    time.Time
	InstalledAt time.Time
}

const serverTLSColumns = `server_id, cert_serial, not_after, installed_at`

func scanServerTLS(r rowScanner) (ServerTLS, error) {
	var (
		t              ServerTLS
		notAfter, inst string
	)
	if err := r.Scan(&t.ServerID, &t.CertSerial, &notAfter, &inst); err != nil {
		return ServerTLS{}, err
	}
	var err error
	if t.NotAfter, err = parseTime(notAfter); err != nil {
		return ServerTLS{}, err
	}
	if t.InstalledAt, err = parseTime(inst); err != nil {
		return ServerTLS{}, err
	}
	return t, nil
}

// SetServerTLS inserts or replaces the server's certificate record.
func (s *Store) SetServerTLS(t ServerTLS) error {
	_, err := s.db.Exec(`INSERT INTO server_tls (server_id, cert_serial, not_after, installed_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(server_id) DO UPDATE SET cert_serial = excluded.cert_serial,
			not_after = excluded.not_after, installed_at = excluded.installed_at`,
		t.ServerID, t.CertSerial, formatTime(t.NotAfter), formatTime(t.InstalledAt))
	if err != nil {
		return fmt.Errorf("set server tls: %w", err)
	}
	return nil
}

// GetServerTLS returns the server's certificate record, or ErrNotFound.
func (s *Store) GetServerTLS(serverID int64) (ServerTLS, error) {
	t, err := scanServerTLS(s.db.QueryRow(`SELECT `+serverTLSColumns+` FROM server_tls WHERE server_id = ?`, serverID))
	if errors.Is(err, sql.ErrNoRows) {
		return ServerTLS{}, ErrNotFound
	}
	if err != nil {
		return ServerTLS{}, fmt.Errorf("get server tls: %w", err)
	}
	return t, nil
}

// ListServerTLSExpiring returns records whose certificate expires before the
// given time, soonest first. Times are RFC3339 UTC, so text order is time order.
func (s *Store) ListServerTLSExpiring(before time.Time) ([]ServerTLS, error) {
	rows, err := s.db.Query(`SELECT `+serverTLSColumns+` FROM server_tls WHERE not_after < ? ORDER BY not_after, server_id`, formatTime(before))
	if err != nil {
		return nil, fmt.Errorf("list server tls: %w", err)
	}
	defer rows.Close()
	var out []ServerTLS
	for rows.Next() {
		t, err := scanServerTLS(rows)
		if err != nil {
			return nil, fmt.Errorf("scan server tls: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// SetReplicaTLSVerified records whether the replica's link to its master was
// verified over TLS.
func (s *Store) SetReplicaTLSVerified(serverID int64, verified bool) error {
	v := 0
	if verified {
		v = 1
	}
	return s.execOne(`UPDATE replicas SET tls_verified = ? WHERE server_id = ?`, v, serverID)
}
