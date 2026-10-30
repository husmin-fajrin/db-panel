package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// runAgentJob starts cmd on serverID as an agent job and waits until it
// ends; result receives the job's result. Its log lines go to the job log
// and its progress to the current step. When a job of the same command
// started by an earlier attempt of this step (before a retry or a panel
// restart) is still running, it is waited for instead of starting another;
// one that already succeeded is taken as is.
func (jr *jobRun) runAgentJob(ctx context.Context, serverID int64, cmd string, params, result any) error {
	r := jr.r
	agentID := ""
	if jr.st.AgentJobID != "" && jr.st.AgentServerID == serverID && jr.st.AgentCmd == cmd {
		var s proto.JobStatus
		err := jr.call(ctx, serverID, "job.status", proto.JobIDParams{JobID: jr.st.AgentJobID}, &s)
		switch {
		case err == nil && s.State == "running":
			agentID = jr.st.AgentJobID
			jr.logf("info", "Melanjutkan pekerjaan agent yang masih berjalan.")
		case err == nil && s.State == "succeeded":
			return jr.agentJobEnded(proto.JobDone{JobID: s.ID, OK: true, Result: s.Result}, result)
		}
	}

	jr.lj.drain() // events of an earlier wait
	r.mu.Lock()
	jr.lj.watching, jr.lj.watchServer, jr.lj.watchJob = true, serverID, agentID
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		jr.lj.watching, jr.lj.watchServer, jr.lj.watchJob = false, 0, ""
		r.mu.Unlock()
	}()

	if agentID == "" {
		var started proto.StartedJob
		if err := jr.call(ctx, serverID, cmd, params, &started); err != nil {
			return err
		}
		agentID = started.JobID
		r.mu.Lock()
		jr.lj.watchJob = agentID
		r.mu.Unlock()
		jr.st.AgentServerID, jr.st.AgentJobID, jr.st.AgentCmd = serverID, agentID, cmd
		jr.save()
	}
	return jr.waitAgentJob(ctx, serverID, agentID, result)
}

// waitAgentJob waits for the agent job's job.done event, polling job.status
// as well (an event sent while the agent was disconnected is lost). An agent
// that stays offline longer than offlineGrace, or comes back without the
// job (it restarted), fails the step.
func (jr *jobRun) waitAgentJob(ctx context.Context, serverID int64, agentID string, result any) error {
	r := jr.r
	gone := stepError(fmt.Sprintf("agent %s terputus saat %s", jr.serverName(serverID), jr.steps[jr.idx].title))
	tick := time.NewTicker(r.pollEvery)
	defer tick.Stop()
	var offlineSince time.Time
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-jr.lj.notify:
			if ended, err := jr.handleEvents(agentID, result); ended {
				return err
			}
		case <-tick.C:
			if !r.d.Hub.IsOnline(serverID) {
				if offlineSince.IsZero() {
					offlineSince = time.Now()
					jr.logf("warn", "Agent %s terputus; menunggu sampai %s agar tersambung lagi.",
						jr.serverName(serverID), r.offlineGrace)
				}
				if time.Since(offlineSince) >= r.offlineGrace {
					return gone
				}
				continue
			}
			if !offlineSince.IsZero() {
				offlineSince = time.Time{}
				jr.logf("info", "Agent %s tersambung lagi.", jr.serverName(serverID))
			}
			var s proto.JobStatus
			pctx, cancel := context.WithTimeout(ctx, callTimeout)
			err := r.d.Hub.Call(pctx, serverID, "job.status", proto.JobIDParams{JobID: agentID}, &s)
			cancel()
			var pe *proto.Error
			switch {
			case ctx.Err() != nil:
				return ctx.Err()
			case errors.Is(err, proto.ErrOffline):
				continue // the next tick starts the grace period
			case errors.As(err, &pe) && pe.Code == proto.CodeBadParams:
				return gone // the agent restarted and no longer knows the job
			case err != nil:
				r.log.Debug("poll agent job", "job", jr.job.ID, "agent_job", agentID, "err", err)
				continue
			}
			if s.State == "running" {
				continue
			}
			// Finished; take its last log lines from the queue first.
			if ended, err := jr.handleEvents(agentID, result); ended {
				return err
			}
			return jr.agentJobEnded(proto.JobDone{JobID: agentID, OK: s.State == "succeeded", Error: s.Error, Result: s.Result}, result)
		}
	}
}

// handleEvents applies the queued events of agentID and reports whether
// one of them ended the job.
func (jr *jobRun) handleEvents(agentID string, result any) (bool, error) {
	for _, ev := range jr.lj.drain() {
		if ev.JobID != agentID {
			continue
		}
		switch ev.Type {
		case proto.EventJobProgress:
			var p proto.JobProgress
			if json.Unmarshal(ev.Payload, &p) == nil {
				jr.setStep(store.StepRunning, fmt.Sprintf("%s (%d%%)", p.Message, p.Percent))
			}
		case proto.EventJobLog:
			var l proto.JobLog
			if json.Unmarshal(ev.Payload, &l) == nil {
				jr.logf(l.Level, "%s", l.Message)
			}
		case proto.EventJobDone:
			var d proto.JobDone
			if err := json.Unmarshal(ev.Payload, &d); err != nil {
				return true, fmt.Errorf("hasil pekerjaan agent tidak terbaca: %w", err)
			}
			return true, jr.agentJobEnded(d, result)
		}
	}
	return false, nil
}

// agentJobEnded forgets the finished agent job and turns its outcome into
// the step's.
func (jr *jobRun) agentJobEnded(d proto.JobDone, result any) error {
	jr.st.AgentServerID, jr.st.AgentJobID, jr.st.AgentCmd = 0, "", ""
	jr.save()
	if !d.OK {
		if d.Error == "" {
			d.Error = "pekerjaan agent gagal tanpa keterangan"
		}
		return errors.New(d.Error)
	}
	if result != nil && len(d.Result) > 0 {
		if err := json.Unmarshal(d.Result, result); err != nil {
			return fmt.Errorf("hasil pekerjaan agent tidak terbaca: %w", err)
		}
	}
	return nil
}
