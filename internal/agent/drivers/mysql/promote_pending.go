package mysql

import (
	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
)

// The mysql.promote requests this agent process has read and whose job has
// not ended (spec 5.2, "Arti field itu"), for mysql.status. promoteReqs
// counts two kinds of unit, each added and removed exactly once:
//
//   - one per request, from the session's read loop (Registry.NoteRead)
//     until Dispatch returned its answer (Registry.NoteAnswered), whatever
//     the answer: refusals at the gate, in the handler, or a panic;
//   - one per job the handler registers, from just before Jobs.Start until
//     the job's function returns (or at once when Jobs.Start fails).
//
// The handler adds its job's unit before it returns, so a request whose
// job runs is counted without a gap until both its answer and its job
// have ended. The answer's OK is not used to decide who removes the
// request's unit: a job registered under an answer that still became an
// error would then be removed twice.

// PromoteRequestPending reports whether this agent has read a
// mysql.promote request whose job has not ended.
func (d *Driver) PromoteRequestPending() bool {
	return d.promoteReqs.Load() > 0
}

// trackPromoteRequests counts every mysql.promote request the session
// reads until it is answered.
func (d *Driver) trackPromoteRequests(reg *commands.Registry) {
	reg.Track("mysql.promote", func() { d.promoteReqs.Add(1) }, func(bool) { d.promoteDone() })
}

// promoteDone removes one unit.
func (d *Driver) promoteDone() {
	if d.promoteReqs.Add(-1) < 0 && d.Logger != nil {
		d.Logger.Error("promote request count below zero; mysql.status may say no promote request while one runs")
	}
}

// startPromoteJob starts fn as the mysql.promote job, counted from before
// Jobs.Start until fn returns.
func (d *Driver) startPromoteJob(fn jobs.Func) (string, error) {
	d.promoteReqs.Add(1)
	id, err := d.Jobs.Start("mysql.promote", func(j *jobs.Job) (any, error) {
		defer d.promoteDone()
		return fn(j)
	})
	if err != nil {
		d.promoteDone()
	}
	return id, err
}
