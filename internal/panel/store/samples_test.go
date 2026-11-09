package store

import (
	"path/filepath"
	"testing"
	"time"
)

func lagPtr(v int64) *int64 { return &v }

func sampleCount(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM health_samples`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestAddHealthSampleIgnoresDuplicate: a second sample of the same server
// at the same second is ignored (INSERT OR IGNORE on (server_id, ts)).
func TestAddHealthSampleIgnoresDuplicate(t *testing.T) {
	s := openTemp(t)
	srv := mustServer(t, s, "db1")
	other := mustServer(t, s, "db2")
	ts := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	for _, h := range []HealthSample{
		{ServerID: srv.ID, TS: ts, LagSec: lagPtr(3), IORunning: "Yes", SQLRunning: "Yes", MySQLOK: true},
		{ServerID: srv.ID, TS: ts.Add(400 * time.Millisecond), LagSec: lagPtr(9), IORunning: "No", SQLRunning: "Yes", MySQLOK: true},
		{ServerID: other.ID, TS: ts, MySQLOK: false},
	} {
		if err := s.AddHealthSample(h); err != nil {
			t.Fatalf("AddHealthSample(%+v): %v", h, err)
		}
	}
	if n := sampleCount(t, s); n != 2 {
		t.Fatalf("rows = %d, want 2 (the second sample of db1 at the same second ignored)", n)
	}
	got, err := s.LagSeries([]int64{srv.ID}, ts.Add(-time.Minute), ts.Add(time.Minute), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if pts := got[srv.ID]; len(pts) != 1 || pts[0].Lag == nil || *pts[0].Lag != 3 {
		t.Fatalf("series = %+v, want the first sample's lag 3", pts)
	}
	if err := s.AddHealthSample(HealthSample{ServerID: 999, TS: ts}); err == nil {
		t.Fatal("a sample for a missing server was stored")
	}
}

// TestLagSeriesBuckets: samples are grouped in buckets (MAX lag each),
// only buckets with rows come back, in time order, and a bucket whose
// lags are all NULL has a nil lag.
func TestLagSeriesBuckets(t *testing.T) {
	s := openTemp(t)
	a := mustServer(t, s, "a")
	b := mustServer(t, s, "b")
	c := mustServer(t, s, "c")
	base := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC) // a 5-minute boundary
	add := func(id int64, at time.Duration, lag *int64) {
		t.Helper()
		if err := s.AddHealthSample(HealthSample{ServerID: id, TS: base.Add(at), LagSec: lag,
			IORunning: "Yes", SQLRunning: "Yes", MySQLOK: true}); err != nil {
			t.Fatal(err)
		}
	}
	// a: 10, 12 and 25 s lag in one bucket; nothing in 10:05; 4 s in 10:10.
	add(a.ID, 0, lagPtr(10))
	add(a.ID, 90*time.Second, lagPtr(25))
	add(a.ID, 4*time.Minute+59*time.Second, lagPtr(12))
	add(a.ID, 11*time.Minute, lagPtr(4))
	// b: only NULL lags in one bucket, a NULL beside a value in the next.
	add(b.ID, 30*time.Second, nil)
	add(b.ID, 60*time.Second, nil)
	add(b.ID, 6*time.Minute, nil)
	add(b.ID, 7*time.Minute, lagPtr(2))
	// c: not asked for; outside the window for a.
	add(c.ID, 0, lagPtr(99))
	add(a.ID, -time.Hour, lagPtr(500))

	got, err := s.LagSeries([]int64{a.ID, b.ID}, base, base.Add(15*time.Minute), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[c.ID]; ok || len(got) != 2 {
		t.Fatalf("series for %v, want a and b only", got)
	}
	pa := got[a.ID]
	if len(pa) != 2 {
		t.Fatalf("a = %+v, want 2 points (empty 10:05 bucket absent)", pa)
	}
	if !pa[0].T.Equal(base) || pa[0].Lag == nil || *pa[0].Lag != 25 {
		t.Errorf("a[0] = %v %v, want %v 25", pa[0].T, pa[0].Lag, base)
	}
	if !pa[1].T.Equal(base.Add(10*time.Minute)) || pa[1].Lag == nil || *pa[1].Lag != 4 {
		t.Errorf("a[1] = %v %v, want %v 4", pa[1].T, pa[1].Lag, base.Add(10*time.Minute))
	}
	pb := got[b.ID]
	if len(pb) != 2 || pb[0].Lag != nil || !pb[0].T.Equal(base) || pb[1].Lag == nil || *pb[1].Lag != 2 {
		t.Fatalf("b = %+v, want a nil bucket then 2", pb)
	}
	if pb[0].T.Location() != time.UTC {
		t.Errorf("bucket time zone = %v, want UTC", pb[0].T.Location())
	}

	// 24 hours of a sample every 15 s: at most 288 points.
	d := mustServer(t, s, "d")
	for i := range 24 * 3600 / 15 {
		if err := s.AddHealthSample(HealthSample{ServerID: d.ID, TS: base.Add(time.Duration(i) * 15 * time.Second),
			LagSec: lagPtr(int64(i % 7)), MySQLOK: true}); err != nil {
			t.Fatal(err)
		}
	}
	got, err = s.LagSeries([]int64{d.ID}, base, base.Add(24*time.Hour), 5*time.Minute)
	if err != nil || len(got[d.ID]) != 288 {
		t.Fatalf("24 h series = %d points, %v; want 288", len(got[d.ID]), err)
	}
	if got, err := s.LagSeries(nil, base, base.Add(time.Hour), 5*time.Minute); err != nil || len(got) != 0 {
		t.Fatalf("LagSeries(nil) = %v, %v", got, err)
	}
}

// TestPruneHealthSamplesKeepsRecent: prune deletes the samples older than
// the cutoff only; HasSamples sees what is left.
func TestPruneHealthSamplesKeepsRecent(t *testing.T) {
	s := openTemp(t)
	srv := mustServer(t, s, "db1")
	now := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	day := 24 * time.Hour
	for _, ago := range []time.Duration{8 * day, 7*day + time.Second, 7*day - time.Second, time.Hour} {
		if err := s.AddHealthSample(HealthSample{ServerID: srv.ID, TS: now.Add(-ago), MySQLOK: true}); err != nil {
			t.Fatal(err)
		}
	}
	if ok, err := s.HasSamples(srv.ID, now.Add(-30*time.Minute)); err != nil || ok {
		t.Fatalf("HasSamples(last 30 min) = %v, %v; want false", ok, err)
	}
	if ok, err := s.HasSamples(srv.ID, now.Add(-2*time.Hour)); err != nil || !ok {
		t.Fatalf("HasSamples(last 2 h) = %v, %v; want true", ok, err)
	}
	n, err := s.PruneHealthSamples(now.Add(-7 * day))
	if err != nil || n != 2 {
		t.Fatalf("PruneHealthSamples = %d, %v; want 2", n, err)
	}
	if left := sampleCount(t, s); left != 2 {
		t.Fatalf("rows left = %d, want 2", left)
	}
	if ok, err := s.HasSamples(srv.ID, now.Add(-7*day)); err != nil || !ok {
		t.Fatalf("HasSamples after prune = %v, %v", ok, err)
	}
	// A deleted server takes its samples with it.
	if err := s.DeleteServer(srv.ID); err != nil {
		t.Fatal(err)
	}
	if left := sampleCount(t, s); left != 0 {
		t.Fatalf("rows after the server was deleted = %d, want 0", left)
	}
}

// TestMigrate0010AddsHealthSamples: a database at version 9 gains the
// empty health_samples table with its unique (server_id, ts) index.
func TestMigrate0010AddsHealthSamples(t *testing.T) {
	path := filepath.Join(t.TempDir(), "panel.db")
	db := openAtVersion(t, path, 9)
	db.Close()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer s.Close()
	var unique int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM pragma_index_list('health_samples') WHERE name = 'health_samples_server_ts' AND "unique" = 1`).Scan(&unique); err != nil || unique != 1 {
		t.Fatalf("unique index on (server_id, ts) = %d, %v", unique, err)
	}
	if n := sampleCount(t, s); n != 0 {
		t.Fatalf("rows = %d, want 0", n)
	}
}
