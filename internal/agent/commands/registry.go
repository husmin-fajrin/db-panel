// Package commands holds the fixed set of commands an agent accepts from the
// panel. Each command is a Go handler registered under a name; the agent never
// executes command strings sent over the wire.
package commands

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Handler runs one command. params is the raw request params (possibly empty).
// A returned *ParamError becomes a bad_params response; any other error
// becomes an internal error. Handlers must honour ctx: it is cancelled when
// the request times out or the agent's connection to the panel ends, and the
// agent does not wait for a handler that keeps running after that.
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// ParamError reports invalid command parameters. Msg is shown to the user.
type ParamError struct{ Msg string }

func (e *ParamError) Error() string { return e.Msg }

// ErrDenied is the answer to a command the local policy disables.
const ErrDenied = "perintah dinonaktifkan oleh kebijakan lokal server ini"

// Registry maps command names to handlers.
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
	denied   map[string]bool // disabled by the host's local policy
	logger   *slog.Logger    // one line per command; nil: none
}

// Deny disables names: Dispatch answers them with ErrDenied without running
// their handler. The host's local policy (agent-policy.yaml) sets it.
func (r *Registry) Deny(names ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.denied == nil {
		r.denied = make(map[string]bool)
	}
	for _, n := range names {
		r.denied[n] = true
	}
}

// SetLogger makes Dispatch log every command it receives: its name, its
// params with password and token fields replaced by "***", the outcome and
// how long it took.
func (r *Registry) SetLogger(l *slog.Logger) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logger = l
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{handlers: make(map[string]Handler)}
}

// Register adds a handler. It panics if name is already registered.
func (r *Registry) Register(name string, h Handler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, dup := r.handlers[name]; dup {
		panic("commands: duplicate registration of " + name)
	}
	r.handlers[name] = h
}

// Names returns the registered command names in sorted order.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.handlers))
	for n := range r.handlers {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Dispatch runs the handler for req.Cmd synchronously and builds the response.
// Handler panics are recovered and reported as internal errors.
func (r *Registry) Dispatch(ctx context.Context, req proto.Request) (resp proto.Response) {
	r.mu.RLock()
	h, ok := r.handlers[req.Cmd]
	denied := r.denied[req.Cmd]
	logger := r.logger
	r.mu.RUnlock()
	if logger != nil {
		start := time.Now()
		defer func() { logCommand(logger, req, resp, time.Since(start)) }()
	}
	if !ok {
		return proto.ErrorResponse(req.ID, proto.CodeUnknownCommand, "perintah tidak dikenal: "+req.Cmd)
	}
	if denied {
		return proto.ErrorResponse(req.ID, proto.CodeBadParams, ErrDenied)
	}

	defer func() {
		if p := recover(); p != nil {
			resp = proto.ErrorResponse(req.ID, proto.CodeInternal, fmt.Sprintf("panic: %v", p))
		}
	}()

	result, err := h(ctx, req.Params)
	if err != nil {
		var pe *ParamError
		if errors.As(err, &pe) {
			return proto.ErrorResponse(req.ID, proto.CodeBadParams, pe.Msg)
		}
		return proto.ErrorResponse(req.ID, proto.CodeInternal, err.Error())
	}
	out, err := proto.NewResponse(req.ID, result)
	if err != nil {
		return proto.ErrorResponse(req.ID, proto.CodeInternal, err.Error())
	}
	return out
}

// pollCommands are read-only commands the panel polls while it waits;
// they are logged at debug level so the log keeps the commands that act.
var pollCommands = map[string]bool{"job.status": true}

// maxLoggedParams bounds the params text in one log line.
const maxLoggedParams = 1024

// logCommand writes the line SetLogger promises.
func logCommand(l *slog.Logger, req proto.Request, resp proto.Response, took time.Duration) {
	outcome := "ok"
	if !resp.OK && resp.Error != nil {
		outcome = "error " + resp.Error.Code + ": " + resp.Error.Message
	}
	level := slog.LevelInfo
	if pollCommands[req.Cmd] {
		level = slog.LevelDebug
	}
	l.Log(context.Background(), level, "command", "cmd", req.Cmd, "params", RedactParams(req.Params),
		"outcome", outcome, "took_ms", took.Milliseconds())
}

// RedactParams renders raw command params for a log: every value whose key
// names a password, secret or token becomes "***", at any depth. Params
// that are not JSON are not shown.
func RedactParams(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return "(tidak terbaca)"
	}
	b, err := json.Marshal(redact(v))
	if err != nil {
		return "(tidak terbaca)"
	}
	if len(b) > maxLoggedParams {
		return string(b[:maxLoggedParams]) + "..."
	}
	return string(b)
}

func redact(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			lk := strings.ToLower(k)
			if strings.Contains(lk, "password") || strings.Contains(lk, "secret") || strings.Contains(lk, "token") {
				t[k] = "***"
				continue
			}
			t[k] = redact(x)
		}
	case []any:
		for i, x := range t {
			t[i] = redact(x)
		}
	}
	return v
}
