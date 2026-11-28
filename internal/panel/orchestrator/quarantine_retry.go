package orchestrator

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// The per-server lock and the pending-quarantine round (spec 5.3, R-185).
//
// Lock order: startMu, then one target lock (lockTarget), then promoMu or
// invMu. A target lock is taken only around one mysql.quarantine or
// mysql.repoint and the store writes that record it, never while startMu
// is taken after it, never around a wait for an operator and never for a
// job's whole life; a holder takes no second target lock. An agent that
// does not answer therefore holds back only the commands to its own
// server.

const (
	onlineSendTimeout  = 5 * time.Minute // a fresh bound for each send, taken after the lock
	quarantineRetryMin = 60 * time.Second
	quarantineRetryMax = 5 * time.Minute
	// quarantineRetryScan is how often the round reads the store for a
	// pending quarantine whose time has come.
	quarantineRetryScan = 5 * time.Second
)

// targetLock is one server's lock; n counts its holder and waiters
// (guarded by Runner.targetMu).
type targetLock struct {
	mu sync.Mutex
	n  int
}

// lockTarget serialises mysql.quarantine and mysql.repoint sent to one
// server; it replaces repointMu. The entry is dropped when nobody holds it.
func (r *Runner) lockTarget(serverID int64) (unlock func()) {
	if r.lockTargetHook != nil {
		r.lockTargetHook(serverID)
	}
	r.targetMu.Lock()
	l := r.targets[serverID]
	if l == nil {
		l = &targetLock{}
		r.targets[serverID] = l
	}
	l.n++
	r.targetMu.Unlock()
	l.mu.Lock()
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Unlock()
			r.targetMu.Lock()
			if l.n--; l.n == 0 {
				delete(r.targets, serverID)
			}
			r.targetMu.Unlock()
		})
	}
}

// pendingQuarantine is the send a quarantined server with a pending
// quarantine is owed, decided from the store alone (onOnline's rule).
//
// The server must be quarantined and its promotion's quarantine pending.
// The command names the promotion's topology's current master, also when
// that topology was promoted again since and its old-master marker moved
// on (ruling D58): the server is quarantined under the master that holds
// its data's line. Without that topology, or without a master other than
// the server itself, nothing is owed now (ok false) and the round tries
// again later.
func (r *Runner) pendingQuarantine(serverID int64) (send func(ctx context.Context) error, ok bool) {
	srv, err := r.d.Store.GetServer(serverID)
	if err != nil || srv.Role != store.RoleQuarantined {
		return nil, false
	}
	prom, err := r.d.Store.PromotionByOldMaster(serverID)
	if err != nil || !QuarantinePending(prom) {
		return nil, false
	}
	topo, err := r.d.Store.GetTopology(prom.TopologyID)
	if err != nil {
		r.log.Warn("pending quarantine not sent: the promotion's topology cannot be read", "server_id", serverID,
			"topology_id", prom.TopologyID, "err", err)
		return nil, false
	}
	if topo.MasterServerID == 0 || topo.MasterServerID == serverID {
		r.log.Warn("pending quarantine not sent: the promotion's topology has no other master", "server_id", serverID,
			"topology_id", topo.ID)
		return nil, false
	}
	password, err := r.replPassword(topo)
	if err != nil {
		r.log.Warn("pending quarantine not sent", "server_id", serverID, "err", err)
		return nil, false
	}
	return func(ctx context.Context) error {
		if topo.MasterServerID != prom.NewMasterServerID {
			r.log.Info("pending quarantine sent under the topology's current master (promoted again since)",
				"server_id", serverID, "master_id", topo.MasterServerID)
		}
		_, err := r.sendQuarantine(ctx, prom.ID, serverID, topo.MasterServerID, topo.SourceHost, topo.SourcePort,
			topo.ReplUser, password)
		return err
	}, true
}

// sendPendingQuarantine sends serverID the quarantine it is owed: under its
// target lock, decided again there (pendingQuarantine: a promote since the
// caller looked is seen), with a bound of its own taken after the lock.
// It reports whether anything was sent.
func (r *Runner) sendPendingQuarantine(serverID int64) bool {
	unlock := r.lockTarget(serverID)
	defer unlock()
	send, ok := r.pendingQuarantine(serverID)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(r.base, r.onlineSend)
	defer cancel()
	if err := send(ctx); err != nil {
		r.log.Warn("pending quarantine failed", "server_id", serverID, "err", logErr(err))
	}
	return true
}

// quarantineDue is the round's schedule of one server: the next send is
// not before at; wait is the interval that set at (0: no send judged
// yet); inflight while the round's send runs.
type quarantineDue struct {
	at       time.Time
	wait     time.Duration
	inflight bool
}

// quarantineNoAnswer reports whether err came without an answer from the
// agent (the call's bound passed, the link closed): retried sooner than a
// refusal (K26).
func quarantineNoAnswer(err error) bool {
	if hub.SentByAgent(err) {
		return false
	}
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) ||
		errors.Is(err, &proto.Error{Code: proto.CodeTimeout}) || errors.Is(err, &proto.Error{Code: proto.CodeOffline})
}

// quarantineTried records the outcome of a mysql.quarantine send to
// serverID (every sender's, through sendQuarantine) in the round's
// schedule: a landed one ends it; one without an answer is tried again
// after 60 s, doubling to 5 minutes; any other failure, the agent's
// refusals ("sibuk" too) among them, after 5 minutes (K26).
func (r *Runner) quarantineTried(serverID int64, err error) {
	r.retryMu.Lock()
	defer r.retryMu.Unlock()
	if err == nil {
		delete(r.retries, serverID)
		return
	}
	d := r.retries[serverID]
	if d == nil {
		d = &quarantineDue{}
		r.retries[serverID] = d
	}
	wait := quarantineRetryMax
	if quarantineNoAnswer(err) {
		wait = min(max(2*d.wait, quarantineRetryMin), quarantineRetryMax)
	}
	d.at, d.wait = r.now().Add(wait), wait
}

// claimRetry reports whether the round sends to serverID now, marking it
// in flight then. A server the round sees for the first time is due after
// quarantineRetryMin: whoever left it pending has just tried, or tries
// when its agent connects (OnOnline).
func (r *Runner) claimRetry(serverID int64, now time.Time) bool {
	r.retryMu.Lock()
	defer r.retryMu.Unlock()
	d := r.retries[serverID]
	switch {
	case d == nil:
		r.retries[serverID] = &quarantineDue{at: now.Add(quarantineRetryMin)}
		return false
	case d.inflight || now.Before(d.at):
		return false
	}
	d.inflight = true
	return true
}

// releaseRetry ends the round's send to serverID; sent false (nothing was
// owed when it looked under the lock: the topology unreadable or without
// another master) tries again after quarantineRetryMin.
func (r *Runner) releaseRetry(serverID int64, sent bool) {
	r.retryMu.Lock()
	defer r.retryMu.Unlock()
	d := r.retries[serverID]
	if d == nil {
		return
	}
	d.inflight = false
	if !sent {
		d.at = r.now().Add(quarantineRetryMin)
	}
}

// startQuarantineRetry starts the round once; Close ends it.
func (r *Runner) startQuarantineRetry() {
	r.retryOnce.Do(func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.closed {
			return
		}
		r.wg.Add(1)
		go r.quarantineRetryLoop()
	})
}

// quarantineRetryLoop is the round (spec 5.3): every retryScan it reads
// the store and sends again each pending quarantine whose time has come
// and whose agent is connected. It ends when the Runner closes.
func (r *Runner) quarantineRetryLoop() {
	defer r.wg.Done()
	t := time.NewTicker(r.retryScan)
	defer t.Stop()
	for {
		select {
		case <-r.base.Done():
			return
		case <-t.C:
		}
		r.quarantineRetryPass()
	}
}

// quarantineRetryPass is one look of the round. A server is owed while it
// is quarantined and its promotion's quarantine pending; the schedule of
// any other server (landed, deleted, another role) is dropped, which ends
// its round. A refusal never does. Each send runs on a goroutine of its
// own, so an agent that does not answer holds back only itself.
func (r *Runner) quarantineRetryPass() {
	servers, err := r.d.Store.ListServers()
	if err != nil {
		r.log.Warn("pending quarantine round: servers cannot be read", "err", err)
		return
	}
	now := r.now()
	owed := map[int64]bool{}
	for _, srv := range servers {
		if srv.Role != store.RoleQuarantined {
			continue
		}
		prom, err := r.d.Store.PromotionByOldMaster(srv.ID)
		if err != nil && !errors.Is(err, store.ErrNotFound) {
			// Unreadable now: its schedule is kept, nothing is sent.
			owed[srv.ID] = true
			continue
		}
		if err != nil || !QuarantinePending(prom) {
			continue
		}
		owed[srv.ID] = true
		if !r.d.Hub.IsOnline(srv.ID) || !r.claimRetry(srv.ID, now) {
			continue
		}
		r.mu.Lock()
		if r.closed {
			r.mu.Unlock()
			r.releaseRetry(srv.ID, true)
			return
		}
		r.wg.Add(1)
		r.mu.Unlock()
		go func(id int64) {
			defer r.wg.Done()
			r.releaseRetry(id, r.sendPendingQuarantine(id))
		}(srv.ID)
	}
	r.retryMu.Lock()
	defer r.retryMu.Unlock()
	for id, d := range r.retries {
		if !owed[id] && !d.inflight {
			delete(r.retries, id)
		}
	}
}
