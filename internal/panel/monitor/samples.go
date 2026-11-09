package monitor

import (
	"encoding/json"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Lag samples (spec 5.7, 8.1) feed the dashboard's lag chart only: they
// never change a status, an alert or a Telegram message.
const (
	// maxSamplePayload: an agent's sample is at most 200 bytes; a larger
	// one is dropped unread.
	maxSamplePayload = 1 << 10
	// sampleEvery: a server's samples arriving faster than this are
	// dropped (and counted, View.ReportsDropped). monitor.sample is not
	// rate-limited by the hub's report bucket, so this keeps a flooding
	// agent from writing a row per event.
	sampleEvery = 5 * time.Second
	// sampleDropWindow is how far back the dropped samples are counted,
	// as the hub counts dropped reports.
	sampleDropWindow = time.Hour
)

// sampleDrops counts one server's dropped samples per minute over the
// last sampleDropWindow.
type sampleDrops []dropMinute

type dropMinute struct {
	minute time.Time
	n      int
}

// add counts one drop at now and forgets the minutes outside the window.
func (d sampleDrops) add(now time.Time) sampleDrops {
	d = d.prune(now)
	minute := now.Truncate(time.Minute)
	if n := len(d); n > 0 && d[n-1].minute.Equal(minute) {
		d[n-1].n++
		return d
	}
	return append(d, dropMinute{minute: minute, n: 1})
}

func (d sampleDrops) prune(now time.Time) sampleDrops {
	i := 0
	for i < len(d) && !d[i].minute.After(now.Add(-sampleDropWindow)) {
		i++
	}
	return d[i:]
}

func (d sampleDrops) count(now time.Time) int {
	n := 0
	for _, m := range d.prune(now) {
		n += m.n
	}
	return n
}

// handleSample stores one monitor.sample of serverID at the panel's now
// minus its age, unless it is unreadable, too large or arrives within
// sampleEvery of the server's previous one (dropped and counted).
func (m *Monitor) handleSample(serverID int64, payload json.RawMessage) {
	now := m.now()
	if len(payload) > maxSamplePayload {
		m.dropSample(serverID, now)
		m.log.Debug("lag sample too large; dropped", "server_id", serverID, "bytes", len(payload))
		return
	}
	var se proto.SampleEvent
	if err := json.Unmarshal(payload, &se); err != nil {
		m.log.Debug("lag sample unreadable; ignored", "server_id", serverID, "err", err)
		return
	}
	m.sampleMu.Lock()
	if last, ok := m.lastSample[serverID]; ok && now.Sub(last) < sampleEvery {
		m.sampleDrops[serverID] = m.sampleDrops[serverID].add(now)
		m.sampleMu.Unlock()
		return
	}
	m.lastSample[serverID] = now
	m.sampleMu.Unlock()
	m.storeSample(store.HealthSample{ServerID: serverID, TS: sampleTime(now, se.TakenAgoMS), LagSec: se.LagSec,
		IORunning: se.IORunning, SQLRunning: se.SQLRunning, MySQLOK: se.MySQLOK})
}

// dropSample counts one dropped sample of serverID at now.
func (m *Monitor) dropSample(serverID int64, now time.Time) {
	m.sampleMu.Lock()
	m.sampleDrops[serverID] = m.sampleDrops[serverID].add(now)
	m.sampleMu.Unlock()
}

// samplesDropped is how many samples of serverID were dropped in the last
// sampleDropWindow.
func (m *Monitor) samplesDropped(serverID int64) int {
	m.sampleMu.Lock()
	defer m.sampleMu.Unlock()
	return m.sampleDrops[serverID].count(m.now())
}

// forgetSamples drops what the Monitor keeps of the samples of servers
// that no longer exist.
func (m *Monitor) forgetSamples(exists func(id int64) bool) {
	m.sampleMu.Lock()
	defer m.sampleMu.Unlock()
	for id := range m.lastSample {
		if !exists(id) {
			delete(m.lastSample, id)
		}
	}
	for id := range m.sampleDrops {
		if !exists(id) {
			delete(m.sampleDrops, id)
		}
	}
}

// reportSample stores the sample a health report carries, taken at at, so
// an agent without monitor.sample still gives the chart (sparse) data and
// a backlog report fills the gap while the panel was away. A sample at
// the same second is kept once.
func (m *Monitor) reportSample(serverID int64, snap *proto.HealthSnapshot, at time.Time) {
	h := store.HealthSample{ServerID: serverID, TS: at.Truncate(time.Second), MySQLOK: snap.MySQLOK}
	if rs := snap.Replica; rs != nil {
		h.IORunning, h.SQLRunning, h.LagSec = rs.IORunning, rs.SQLRunning, rs.SecondsBehind
	}
	m.storeSample(h)
}

func (m *Monitor) storeSample(h store.HealthSample) {
	if err := m.st.AddHealthSample(h); err != nil {
		// A server deleted while its agent still talks lands here too.
		m.log.Debug("lag sample not stored", "server_id", h.ServerID, "err", err)
	}
}

// sampleTime is the panel's now minus a reading's age, to the second.
func sampleTime(now time.Time, takenAgoMS int64) time.Time {
	return now.Add(-time.Duration(max(takenAgoMS, 0)) * time.Millisecond).Truncate(time.Second)
}
