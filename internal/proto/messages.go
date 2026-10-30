// Package proto defines the messages exchanged between the panel and an agent
// over the WebSocket connection, plus the host description an agent reports.
package proto

import "encoding/json"

// Envelope kinds.
const (
	KindRequest  = "request"
	KindResponse = "response"
	KindEvent    = "event"
)

// Error codes carried in Error.Code.
const (
	CodeUnknownCommand = "unknown_command"
	CodeBadParams      = "bad_params"
	CodeInternal       = "internal"
	CodeOffline        = "offline"
	CodeTimeout        = "timeout"
)

// Request asks the peer to run a command.
type Request struct {
	ID     string          `json:"id"`
	Cmd    string          `json:"cmd"`
	Params json.RawMessage `json:"params,omitempty"`
}

// Error describes why a request failed.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Response answers the Request with the same ID.
type Response struct {
	ID     string          `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Event is an unsolicited notification, optionally tied to a job.
type Event struct {
	Type    string          `json:"type"`
	JobID   string          `json:"job_id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

// Envelope is the single frame type on the wire. Exactly the field matching
// Kind is expected to be set.
type Envelope struct {
	Kind     string    `json:"kind"`
	Request  *Request  `json:"request,omitempty"`
	Response *Response `json:"response,omitempty"`
	Event    *Event    `json:"event,omitempty"`
}
