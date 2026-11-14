package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"slices"
	"strconv"
	"strings"
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
	// copied is what that job replaced: the whole server (CLONE) or the
	// databases a dump load created (see loadMark).
	copied loadCopy
}

// loadCopy is what one successful snapshot job replaced on this server.
// A CLONE replaces the whole data directory. A dump only adds what it
// loads: before lists the non-system databases present when its load
// began (each of them keeps whatever tables the dump did not overwrite)
// and named the databases it was asked for (empty: every database).
type loadCopy struct {
	whole  bool
	before []string
	named  []string
}

// loadMark is the handle of one snapshot job on the driver's mark; nil
// marks nothing.
type loadMark struct {
	r      *rebuildLoad
	source string
	// listed: a dump recorded the databases present before its load
	// (before); a dump without that record never counts as a copy.
	listed bool
	before []string
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

// listBefore records the non-system databases db holds now, right before
// a dump's load begins; nil m records nothing and reads nothing.
func (m *loadMark) listBefore(ctx context.Context, db *sql.DB) error {
	if m == nil {
		return nil
	}
	names, err := userDatabases(ctx, db)
	if err != nil {
		return err
	}
	m.before, m.listed = names, true
	return nil
}

// done records that the snapshot job copied the data from the mark's
// source (the job succeeded): whole for a CLONE, else a dump of named
// (empty: every database), which counts only once its load recorded the
// databases present before it (listBefore).
func (m *loadMark) done(whole bool, named []string) {
	if m == nil {
		return
	}
	m.r.mu.Lock()
	defer m.r.mu.Unlock()
	if !whole && !m.listed {
		return
	}
	m.r.loaded = m.source
	m.r.copied = loadCopy{whole: whole, before: m.before, named: slices.Clone(named)}
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
// data from host:port, and what it replaced.
func (d *Driver) loadedFrom(host string, port int) (loadCopy, bool) {
	d.rebuild.mu.Lock()
	defer d.rebuild.mu.Unlock()
	if d.rebuild.loaded == "" || d.rebuild.loaded != net.JoinHostPort(host, strconv.Itoa(port)) {
		return loadCopy{}, false
	}
	return d.rebuild.copied, true
}

// maxNamedLeftovers is how many databases the A6 refusal names.
const maxNamedLeftovers = 10

// errNotReplaced is the refusal to end a quarantine while databases that
// were not copied from the source remain.
type errNotReplaced struct {
	source string
	names  []string
}

func (e *errNotReplaced) Error() string {
	list := strings.Join(e.names[:min(len(e.names), maxNamedLeftovers)], ", ")
	if len(e.names) > maxNamedLeftovers {
		list += ", …"
	}
	return fmt.Sprintf("server ini masih punya database yang tidak disalin dari sumber %s (%s); karantina tidak diakhiri. "+
		"Selamatkan atau hapus database itu lewat panel, lalu ulangi Bangun ulang sebagai replika.", e.source, list)
}

// notCopied lists the server's non-system databases now (present) against
// what a dump load c copied: a database that was there before the load
// began, or, for a dump of named databases, one not among them, was not
// copied from the source. Names compare without case, as
// lower_case_table_names may fold them. A CLONE leaves nothing.
func (c loadCopy) notCopied(present []string) []string {
	if c.whole {
		return nil
	}
	has := func(list []string, name string) bool {
		return slices.ContainsFunc(list, func(n string) bool { return strings.EqualFold(n, name) })
	}
	var out []string
	for _, name := range present {
		if has(c.before, name) || (len(c.named) > 0 && !has(c.named, name)) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// userDatabases are db's databases outside systemDatabases, sorted.
func userDatabases(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT schema_name FROM information_schema.schemata")
	if err != nil {
		return nil, fmt.Errorf("baca daftar database: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("baca daftar database: %w", err)
		}
		if !systemDatabases[strings.ToLower(name)] {
			out = append(out, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("baca daftar database: %w", err)
	}
	slices.Sort(out)
	return out, nil
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
