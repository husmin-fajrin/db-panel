package mysql

import (
	"net"
	"strconv"
	"sync"
)

// The rebuild of a quarantined old master loads the new master's data with
// read-only lifted (liftReadOnly), after a restart of MySQL. The quarantine
// guard puts read-only back on every restart it sees (guardQuarantined), so
// while such a load runs it defers instead. The mark lives in memory only:
// an agent that dies mid-load loses it, the next transition locks the
// server again, and the rebuild job fails and is retried.

// rebuildLoad is the driver's mark: active while a load from source runs.
type rebuildLoad struct {
	mu       sync.Mutex
	active   bool
	source   string // host:port the load copies from
	deferred bool   // the guard logged its deferral for this load
	// recheck: a load ended; the guard's next tick reads the flags again
	// whether or not it deferred during the load.
	recheck bool
	// loaded is the source (host:port) a snapshot job in this agent run
	// copied the data from successfully; settleRebuilt requires it.
	loaded string
}

// loadMark is the handle of one snapshot job on the driver's mark; nil
// marks nothing.
type loadMark struct {
	r      *rebuildLoad
	source string
}

// rebuildMark is the mark for a snapshot from host:port: a handle only when
// the role pin is quarantined and host:port is the source pin (matched like
// Allow matches the rebuild commands), else nil.
func (d *Driver) rebuildMark(host string, port int) *loadMark {
	if d.pins == nil || !d.pins.quarantinedUnder(host, port) {
		return nil
	}
	return &loadMark{r: &d.rebuild, source: net.JoinHostPort(host, strconv.Itoa(port))}
}

// set marks the load in progress.
func (m *loadMark) set() {
	if m == nil {
		return
	}
	m.r.mu.Lock()
	defer m.r.mu.Unlock()
	m.r.active, m.r.source, m.r.deferred = true, m.source, false
}

// clear ends the load's mark; clearing again is a no-op.
func (m *loadMark) clear() {
	if m == nil {
		return
	}
	m.r.mu.Lock()
	defer m.r.mu.Unlock()
	if m.r.source == m.source && m.r.active {
		m.r.active, m.r.source, m.r.deferred, m.r.recheck = false, "", false, true
	}
}

// done records that the snapshot job copied the data from the mark's
// source (the job succeeded).
func (m *loadMark) done() {
	if m == nil {
		return
	}
	m.r.mu.Lock()
	defer m.r.mu.Unlock()
	m.r.loaded = m.source
}

// takeRecheck reports, once, that a load ended since the guard's last
// tick.
func (d *Driver) takeRecheck() bool {
	d.rebuild.mu.Lock()
	defer d.rebuild.mu.Unlock()
	r := d.rebuild.recheck
	d.rebuild.recheck = false
	return r
}

// loadedFrom reports whether a snapshot job in this agent run copied the
// data from host:port.
func (d *Driver) loadedFrom(host string, port int) bool {
	d.rebuild.mu.Lock()
	defer d.rebuild.mu.Unlock()
	return d.rebuild.loaded != "" && d.rebuild.loaded == net.JoinHostPort(host, strconv.Itoa(port))
}

// deferringToRebuild reports whether the guard defers: a load is marked, its source
// is still the pinned source of a quarantined server. first is true once
// per load (the guard logs then).
func (d *Driver) deferringToRebuild() (source string, first, ok bool) {
	d.rebuild.mu.Lock()
	defer d.rebuild.mu.Unlock()
	if !d.rebuild.active {
		return "", false, false
	}
	host, ps, err := net.SplitHostPort(d.rebuild.source)
	if err != nil {
		return "", false, false
	}
	port, err := strconv.Atoi(ps)
	if err != nil || d.pins == nil || !d.pins.quarantinedUnder(host, port) {
		return "", false, false
	}
	first = !d.rebuild.deferred
	d.rebuild.deferred = true
	return d.rebuild.source, first, true
}
