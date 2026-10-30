package hub

import (
	"context"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// localConn runs commands on the agent embedded in the panel process.
type localConn struct {
	reg *commands.Registry
	// onEvent is kept for symmetry with WSConn (the panel passes a func
	// calling Hub.OnEvent); the embedded agent does not emit events yet.
	onEvent func(proto.Event)
}

// NewLocalConn returns a Conn that dispatches straight to reg. Close is a
// no-op. onEvent should forward to Hub.OnEvent, the hub's single entry for
// agent events.
func NewLocalConn(reg *commands.Registry, onEvent func(proto.Event)) Conn {
	return &localConn{reg: reg, onEvent: onEvent}
}

func (c *localConn) Call(ctx context.Context, cmd string, params any, result any) error {
	req, err := newRequest(cmd, params)
	if err != nil {
		return err
	}
	return readResponse(c.reg.Dispatch(ctx, req), result)
}

func (c *localConn) Close() error { return nil }
