package orchestrator

import (
	"context"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// The master_port row's probe: can the panel host open a TCP connection
// to the master's MySQL port? It dials from the panel host, sends
// nothing and closes at once. Each master's answer is kept 10 minutes (by
// Deps.Now), and gates that need it meanwhile, or while a probe is on the
// way, share it: concurrent gates for one master make one dial.

const (
	// portProbeTimeout bounds one probe.
	portProbeTimeout = time.Second
	// portProbeKeep is how long a master's answer is kept.
	portProbeKeep = 10 * time.Minute
)

// portProbes holds the last probe of each master. Its mutex guards only
// the map; no dial happens under it.
type portProbes struct {
	mu       sync.Mutex
	byMaster map[int64]*portProbe
}

// portProbe is one master's probe. open and at are written before done is
// closed and only read after.
type portProbe struct {
	hostPort string
	done     chan struct{}
	open     bool
	at       time.Time
}

// masterSourceAddr is the address replicas reach the topology's master
// at: the source host and port every CHANGE REPLICATION SOURCE and copy is
// given (Topology.SourceHost and SourcePort, set by "Siapkan master" from
// Runner.sourceHost and the master's port, and swapped by a promote).
func masterSourceAddr(topo store.Topology) string {
	return net.JoinHostPort(topo.SourceHost, strconv.Itoa(topo.SourcePort))
}

// masterPortOpen reports whether hostPort, master masterID's MySQL port,
// answered the panel host's last probe; it probes when the master has no
// answer under 10 minutes old for that address, or waits for the probe on
// the way.
func (r *Runner) masterPortOpen(masterID int64, hostPort string) bool {
	now := r.now()
	r.ports.mu.Lock()
	if r.ports.byMaster == nil {
		r.ports.byMaster = map[int64]*portProbe{}
	}
	p := r.ports.byMaster[masterID]
	if p != nil && p.hostPort == hostPort {
		select {
		case <-p.done:
			if age := now.Sub(p.at); age >= 0 && age < portProbeKeep {
				r.ports.mu.Unlock()
				return p.open
			}
		default: // on the way: share it
			r.ports.mu.Unlock()
			<-p.done
			return p.open
		}
	}
	p = &portProbe{hostPort: hostPort, done: make(chan struct{})}
	r.ports.byMaster[masterID] = p
	r.ports.mu.Unlock()

	p.open = r.dialPort(hostPort)
	p.at = now
	close(p.done)
	return p.open
}

// dialPort is one probe of hostPort, bounded by portProbeTimeout:
// Deps.PortProbe, or a TCP dial closed at once.
func (r *Runner) dialPort(hostPort string) bool {
	ctx, cancel := context.WithTimeout(r.base, portProbeTimeout)
	defer cancel()
	if r.d.PortProbe != nil {
		return r.d.PortProbe(ctx, hostPort)
	}
	conn, err := (&net.Dialer{Timeout: portProbeTimeout}).DialContext(ctx, "tcp", hostPort)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}
