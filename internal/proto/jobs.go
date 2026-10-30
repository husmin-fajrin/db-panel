package proto

import (
	"encoding/json"
	"time"
)

// Event types emitted by the agent's job runner. Every job event also carries
// the job id in Event.JobID.
const (
	EventJobProgress = "job.progress"
	EventJobLog      = "job.log"
	EventJobDone     = "job.done"
)

// JobProgress is the payload of an EventJobProgress event.
type JobProgress struct {
	JobID   string `json:"job_id"`
	Percent int    `json:"percent"`
	Message string `json:"message,omitempty"`
}

// JobLog is one log line of a job; it is the payload of an EventJobLog event
// and the element type of JobLogResult.Lines.
type JobLog struct {
	JobID   string    `json:"job_id"`
	TS      time.Time `json:"ts"`
	Level   string    `json:"level"` // info|warn|error
	Message string    `json:"message"`
}

// JobDone is the payload of the final EventJobDone event.
type JobDone struct {
	JobID  string          `json:"job_id"`
	OK     bool            `json:"ok"`
	Error  string          `json:"error,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
}

// JobStatus is the snapshot returned by the job.status command.
type JobStatus struct {
	ID         string          `json:"id"`
	Kind       string          `json:"kind"`
	State      string          `json:"state"` // running|succeeded|failed|canceled
	Percent    int             `json:"percent"`
	Message    string          `json:"message,omitempty"`
	Error      string          `json:"error,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
	StartedAt  time.Time       `json:"started_at"`
	FinishedAt *time.Time      `json:"finished_at,omitempty"`
}

// StartedJob is the result of a command that starts a background job.
type StartedJob struct {
	JobID string `json:"job_id"`
}

// JobIDParams are the params of job.status and job.cancel.
type JobIDParams struct {
	JobID string `json:"job_id"`
}

// JobLogParams are the params of job.log. After is the Next value of the
// previous call (0 for the first call).
type JobLogParams struct {
	JobID string `json:"job_id"`
	After int    `json:"after"`
}

// JobLogResult is the result of job.log. Next is the index of the last line
// returned (or After when there were none); pass it as After to continue.
type JobLogResult struct {
	Lines []JobLog `json:"lines"`
	Next  int      `json:"next"`
}
