package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// HealthSample is one lag sample of a server (spec 5.7): from the agent's
// monitor.sample or from a health report. TS is kept to the second; a
// second sample of the same server at the same second is ignored.
type HealthSample struct {
	ServerID              int64
	TS                    time.Time
	LagSec                *int64 // nil when the lag is unknown
	IORunning, SQLRunning string // as reported; "" when not a replica
	MySQLOK               bool
}

// LagPoint is one bucket of a lag series: T is the bucket's start and Lag
// the highest lag of its samples, nil when every sample's lag is unknown.
type LagPoint struct {
	T   time.Time
	Lag *int64
}

// AddHealthSample stores h, unless the server already has a sample at the
// same second (INSERT OR IGNORE).
func (s *Store) AddHealthSample(h HealthSample) error {
	var lag sql.NullInt64
	if h.LagSec != nil {
		lag = sql.NullInt64{Int64: *h.LagSec, Valid: true}
	}
	_, err := s.db.Exec(`INSERT OR IGNORE INTO health_samples (server_id, ts, lag_sec, io_running, sql_running, mysql_ok)
		VALUES (?, ?, ?, ?, ?, ?)`, h.ServerID, h.TS.Unix(), lag, h.IORunning, h.SQLRunning, h.MySQLOK)
	if err != nil {
		return fmt.Errorf("add health sample: %w", err)
	}
	return nil
}

// MaxLagBuckets is the most buckets LagSeries spans: 24 hours of 5
// minutes (288), plus one for a window that does not start on a bucket
// boundary.
const MaxLagBuckets = 289

// LagSeries returns, per server of serverIDs, the samples from from to to
// (both included) in buckets of bucket (aligned to the unix epoch; from is
// moved down to its bucket's start, so the first bucket is whole): one
// LagPoint per bucket that has samples, in time order, with the bucket's
// MAX(lag_sec). A server without samples in the window has no entry. A
// window over MaxLagBuckets buckets is refused.
func (s *Store) LagSeries(serverIDs []int64, from, to time.Time, bucket time.Duration) (map[int64][]LagPoint, error) {
	out := make(map[int64][]LagPoint, len(serverIDs))
	if len(serverIDs) == 0 {
		return out, nil
	}
	secs := int64(bucket / time.Second)
	if secs < 1 {
		return nil, fmt.Errorf("lag series: bucket %v is shorter than a second", bucket)
	}
	start, end := from.Unix(), to.Unix()
	start -= ((start % secs) + secs) % secs // down to the bucket's start
	if end < start {
		return out, nil
	}
	if n := (end-start)/secs + 1; n > MaxLagBuckets {
		return nil, fmt.Errorf("lag series: %d buckets of %v, at most %d", n, bucket, MaxLagBuckets)
	}
	args := []any{secs, secs}
	for _, id := range serverIDs {
		args = append(args, id)
	}
	args = append(args, start, end)
	rows, err := s.db.Query(`SELECT server_id, (ts / ?) * ? AS bucket, MAX(lag_sec)
		FROM health_samples
		WHERE server_id IN (?`+strings.Repeat(", ?", len(serverIDs)-1)+`) AND ts >= ? AND ts <= ?
		GROUP BY server_id, bucket
		ORDER BY server_id, bucket`, args...)
	if err != nil {
		return nil, fmt.Errorf("lag series: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id, start int64
			lag       sql.NullInt64
		)
		if err := rows.Scan(&id, &start, &lag); err != nil {
			return nil, fmt.Errorf("lag series: %w", err)
		}
		p := LagPoint{T: time.Unix(start, 0).UTC()}
		if lag.Valid {
			v := lag.Int64
			p.Lag = &v
		}
		out[id] = append(out[id], p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("lag series: %w", err)
	}
	return out, nil
}

// HasSamples reports whether serverID has a sample at or after since.
func (s *Store) HasSamples(serverID int64, since time.Time) (bool, error) {
	var one int
	err := s.db.QueryRow(`SELECT 1 FROM health_samples WHERE server_id = ? AND ts >= ? LIMIT 1`,
		serverID, since.Unix()).Scan(&one)
	switch {
	case err == sql.ErrNoRows:
		return false, nil
	case err != nil:
		return false, fmt.Errorf("has samples: %w", err)
	}
	return true, nil
}

// PruneHealthSamples deletes the samples older than olderThan and returns
// how many it deleted.
func (s *Store) PruneHealthSamples(olderThan time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM health_samples WHERE ts < ?`, olderThan.Unix())
	if err != nil {
		return 0, fmt.Errorf("prune health samples: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune health samples: %w", err)
	}
	return n, nil
}
