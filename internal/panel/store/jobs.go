package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Job statuses.
const (
	JobQueued    = "queued"
	JobRunning   = "running"
	JobWaiting   = "waiting"
	JobSucceeded = "succeeded"
	JobFailed    = "failed"
	JobCanceled  = "canceled"
)

// Job step statuses.
const (
	StepPending = "pending"
	StepRunning = "running"
	StepWaiting = "waiting"
	StepDone    = "done"
	StepFailed  = "failed"
	StepSkipped = "skipped"
)

// Job is a long-running orchestrated operation on a server.
type Job struct {
	ID, Kind    string
	ServerID    int64
	TopologyID  int64
	Status      string
	CurrentStep int
	Error       string
	ParamsJSON  string
	CreatedAt   time.Time
	StartedAt   *time.Time
	FinishedAt  *time.Time
}

// JobStep is one step of a job.
type JobStep struct {
	JobID                 string
	Idx                   int
	Name, Status, Message string
	StartedAt, FinishedAt *time.Time
}

// JobLogLine is one line of a job's log.
type JobLogLine struct {
	ID             int64
	JobID          string
	TS             time.Time
	Level, Message string
}

const jobColumns = `id, kind, server_id, topology_id, status, current_step, error, params_json, created_at, started_at, finished_at`

func scanJob(r rowScanner) (Job, error) {
	var (
		j                 Job
		created           string
		started, finished sql.NullString
	)
	if err := r.Scan(&j.ID, &j.Kind, &j.ServerID, &j.TopologyID, &j.Status, &j.CurrentStep, &j.Error,
		&j.ParamsJSON, &created, &started, &finished); err != nil {
		return Job{}, err
	}
	var err error
	if j.CreatedAt, err = parseTime(created); err != nil {
		return Job{}, err
	}
	if j.StartedAt, err = parseNullTime(started); err != nil {
		return Job{}, err
	}
	if j.FinishedAt, err = parseNullTime(finished); err != nil {
		return Job{}, err
	}
	return j, nil
}

func scanStep(r rowScanner) (JobStep, error) {
	var (
		st                JobStep
		started, finished sql.NullString
	)
	if err := r.Scan(&st.JobID, &st.Idx, &st.Name, &st.Status, &st.Message, &started, &finished); err != nil {
		return JobStep{}, err
	}
	var err error
	if st.StartedAt, err = parseNullTime(started); err != nil {
		return JobStep{}, err
	}
	if st.FinishedAt, err = parseNullTime(finished); err != nil {
		return JobStep{}, err
	}
	return st, nil
}

// CreateJob stores j as a queued job at step 0, with one pending step per
// name, in one transaction. Status and CurrentStep of j are ignored.
func (s *Store) CreateJob(j Job, stepNames []string) error {
	if j.ID == "" {
		return errors.New("id pekerjaan kosong")
	}
	created := j.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM servers WHERE id = ?`, j.ServerID).Scan(&exists); err != nil {
		return fmt.Errorf("check server: %w", err)
	}
	if exists == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`INSERT INTO jobs (id, kind, server_id, topology_id, status, current_step, error, params_json, created_at)
		VALUES (?, ?, ?, ?, ?, 0, '', ?, ?)`,
		j.ID, j.Kind, j.ServerID, j.TopologyID, JobQueued, j.ParamsJSON, formatTime(created)); err != nil {
		return fmt.Errorf("insert job: %w", err)
	}
	for i, name := range stepNames {
		if _, err := tx.Exec(`INSERT INTO job_steps (job_id, idx, name, status) VALUES (?, ?, ?, ?)`,
			j.ID, i, name, StepPending); err != nil {
			return fmt.Errorf("insert job step: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// GetJob returns the job and its steps ordered by index, or ErrNotFound.
func (s *Store) GetJob(id string) (Job, []JobStep, error) {
	j, err := scanJob(s.db.QueryRow(`SELECT `+jobColumns+` FROM jobs WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, nil, ErrNotFound
	}
	if err != nil {
		return Job{}, nil, fmt.Errorf("get job: %w", err)
	}
	rows, err := s.db.Query(`SELECT job_id, idx, name, status, message, started_at, finished_at
		FROM job_steps WHERE job_id = ? ORDER BY idx`, id)
	if err != nil {
		return Job{}, nil, fmt.Errorf("get job steps: %w", err)
	}
	defer rows.Close()
	var steps []JobStep
	for rows.Next() {
		st, err := scanStep(rows)
		if err != nil {
			return Job{}, nil, fmt.Errorf("scan job step: %w", err)
		}
		steps = append(steps, st)
	}
	if err := rows.Err(); err != nil {
		return Job{}, nil, err
	}
	return j, steps, nil
}

// queryJobs runs a job SELECT with the given WHERE/ORDER tail.
func (s *Store) queryJobs(tail string, args ...any) ([]Job, error) {
	rows, err := s.db.Query(`SELECT `+jobColumns+` FROM jobs `+tail, args...)
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer rows.Close()
	var out []Job
	for rows.Next() {
		j, err := scanJob(rows)
		if err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

// ListJobs returns the newest jobs first; limit <= 0 means no limit.
func (s *Store) ListJobs(limit int) ([]Job, error) {
	if limit <= 0 {
		limit = -1
	}
	return s.queryJobs(`ORDER BY created_at DESC, id DESC LIMIT ?`, limit)
}

// ListJobsForServer returns a server's newest jobs first; limit <= 0 means
// no limit.
func (s *Store) ListJobsForServer(serverID int64, limit int) ([]Job, error) {
	if limit <= 0 {
		limit = -1
	}
	return s.queryJobs(`WHERE server_id = ? ORDER BY created_at DESC, id DESC LIMIT ?`, serverID, limit)
}

// RunningJobs returns the jobs that are running or waiting, oldest first.
func (s *Store) RunningJobs() ([]Job, error) {
	return s.queryJobs(`WHERE status IN (?, ?) ORDER BY created_at, id`, JobRunning, JobWaiting)
}

// ActiveJobForServer returns the newest running, waiting or queued job of
// the server, if any.
func (s *Store) ActiveJobForServer(serverID int64) (Job, bool, error) {
	j, err := scanJob(s.db.QueryRow(`SELECT `+jobColumns+` FROM jobs
		WHERE server_id = ? AND status IN (?, ?, ?)
		ORDER BY created_at DESC, id DESC LIMIT 1`, serverID, JobRunning, JobWaiting, JobQueued))
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, fmt.Errorf("active job: %w", err)
	}
	return j, true, nil
}

// NewerJobExists reports whether the server has a job created after the
// job jobID (in insertion order, which created_at's one-second precision
// cannot give).
func (s *Store) NewerJobExists(jobID string, serverID int64) (bool, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE server_id = ?
		AND rowid > (SELECT rowid FROM jobs WHERE id = ?)`, serverID, jobID).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("newer job: %w", err)
	}
	return n > 0, nil
}

// ErrJobFinished is returned by UpdateJob for a job that already succeeded,
// failed or was canceled: a finished job never changes again.
var ErrJobFinished = errors.New("pekerjaan sudah selesai")

// UpdateJob sets a job's status, current step and error. started_at is set
// the first time the status becomes running, finished_at when it becomes
// succeeded, failed or canceled. A job that is already in one of those
// terminal states is left untouched and ErrJobFinished is returned.
func (s *Store) UpdateJob(id, status string, currentStep int, errMsg string, now time.Time) error {
	ts := formatTime(now)
	res, err := s.db.Exec(`UPDATE jobs SET status = ?, current_step = ?, error = ?,
		started_at = CASE WHEN ? = 'running' AND started_at IS NULL THEN ? ELSE started_at END,
		finished_at = CASE WHEN ? IN ('succeeded', 'failed', 'canceled') THEN ? ELSE finished_at END
		WHERE id = ? AND status NOT IN ('succeeded', 'failed', 'canceled')`,
		status, currentStep, errMsg, status, ts, status, ts, id)
	if err != nil {
		return fmt.Errorf("update job: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("update job: %w", err)
	}
	if n > 0 {
		return nil
	}
	var exists int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE id = ?`, id).Scan(&exists); err != nil {
		return fmt.Errorf("update job: %w", err)
	}
	if exists == 0 {
		return ErrNotFound
	}
	return ErrJobFinished
}

// SetStep sets a step's status and message. started_at is set the first
// time the step leaves pending, finished_at when it is done, failed or
// skipped.
func (s *Store) SetStep(jobID string, idx int, status, message string, now time.Time) error {
	ts := formatTime(now)
	return s.execOne(`UPDATE job_steps SET status = ?, message = ?,
		started_at = CASE WHEN ? != 'pending' AND started_at IS NULL THEN ? ELSE started_at END,
		finished_at = CASE WHEN ? IN ('done', 'failed', 'skipped') THEN ? ELSE finished_at END
		WHERE job_id = ? AND idx = ?`, status, message, status, ts, status, ts, jobID, idx)
}

// AppendJobLog adds a line to a job's log and returns its id.
func (s *Store) AppendJobLog(jobID, level, message string, now time.Time) (int64, error) {
	res, err := s.db.Exec(`INSERT INTO job_log (job_id, ts, level, message) VALUES (?, ?, ?, ?)`,
		jobID, formatTime(now), level, message)
	if err != nil {
		return 0, fmt.Errorf("append job log: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("append job log: %w", err)
	}
	return id, nil
}

// defaultJobLogLimit is how many lines JobLog returns when limit <= 0.
const defaultJobLogLimit = 500

// JobLog returns the log lines of a job with id > afterID, oldest first, at
// most limit (defaultJobLogLimit when limit <= 0).
func (s *Store) JobLog(jobID string, afterID int64, limit int) ([]JobLogLine, error) {
	if limit <= 0 {
		limit = defaultJobLogLimit
	}
	rows, err := s.db.Query(`SELECT id, job_id, ts, level, message FROM job_log
		WHERE job_id = ? AND id > ? ORDER BY id LIMIT ?`, jobID, afterID, limit)
	if err != nil {
		return nil, fmt.Errorf("job log: %w", err)
	}
	defer rows.Close()
	var out []JobLogLine
	for rows.Next() {
		var (
			l  JobLogLine
			ts string
		)
		if err := rows.Scan(&l.ID, &l.JobID, &ts, &l.Level, &l.Message); err != nil {
			return nil, fmt.Errorf("scan job log: %w", err)
		}
		if l.TS, err = parseTime(ts); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// PruneJobLogs deletes log lines older than olderThan and returns how many.
func (s *Store) PruneJobLogs(olderThan time.Time) (int64, error) {
	res, err := s.db.Exec(`DELETE FROM job_log WHERE ts < ?`, formatTime(olderThan))
	if err != nil {
		return 0, fmt.Errorf("prune job log: %w", err)
	}
	return res.RowsAffected()
}

// SetJobParams replaces a job's params JSON. The orchestrator keeps what a
// job learned along the way there (choices, the agent job it waits for), so
// a retry or an approval after a panel restart continues with it.
func (s *Store) SetJobParams(id, paramsJSON string) error {
	return s.execOne(`UPDATE jobs SET params_json = ? WHERE id = ?`, paramsJSON, id)
}

// ReopenJob moves a failed job back to running at its current step, for
// "retry from this step": its error and finished_at are cleared. Any other
// status is left alone and ErrJobFinished (succeeded, canceled) or an error
// naming the status is returned; ErrNotFound for an unknown id.
func (s *Store) ReopenJob(id string, now time.Time) error {
	res, err := s.db.Exec(`UPDATE jobs SET status = ?, error = '', finished_at = NULL,
		started_at = COALESCE(started_at, ?) WHERE id = ? AND status = ?`,
		JobRunning, formatTime(now), id, JobFailed)
	if err != nil {
		return fmt.Errorf("reopen job: %w", err)
	}
	if n, err := res.RowsAffected(); err != nil {
		return fmt.Errorf("reopen job: %w", err)
	} else if n > 0 {
		return nil
	}
	var status string
	err = s.db.QueryRow(`SELECT status FROM jobs WHERE id = ?`, id).Scan(&status)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("reopen job: %w", err)
	case status == JobSucceeded || status == JobCanceled:
		return ErrJobFinished
	}
	return fmt.Errorf("pekerjaan berstatus %s, bukan gagal", status)
}
