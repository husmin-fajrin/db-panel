// Package commands holds the fixed set of commands an agent accepts from the
// panel. Each command is a Go handler registered under a name; the agent never
// executes command strings sent over the wire.
package commands

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Handler runs one command. params is the raw request params (possibly empty).
// A returned *ParamError becomes a bad_params response; any other error
// becomes an internal error.
type Handler func(ctx context.Context, params json.RawMessage) (any, error)

// ParamError reports invalid command parameters. Msg is shown to the user.
type ParamError struct{ Msg string }

func (e *ParamError) Error() string { return e.Msg }

// Registry maps command names to handlers.
type Registry struct {
	mu       sync.RWMutex
	handlers map[string]Handler
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
	r.mu.RUnlock()
	if !ok {
		return proto.ErrorResponse(req.ID, proto.CodeUnknownCommand, "perintah tidak dikenal: "+req.Cmd)
	}

	defer func() {
		if p := recover(); p != nil {
			resp = proto.ErrorResponse(req.ID, proto.CodeInternal, fmt.Sprintf("panic: %v", p))
		}
	}()

	result, err := h(ctx, req.Params)
	if err != nil {
		if pe, ok := err.(*ParamError); ok {
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
