package panel

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// attachLocal attaches the agent embedded in the panel process as the
// connection of the panel host's own server, and returns the function that
// detaches it.
func (p *Panel) attachLocal() (detach func()) {
	reg := commands.NewRegistry()
	probe.Register(reg, probe.RealSystem(), p.opts.Version)
	id := p.localID
	conn := hub.NewLocalConn(reg, func(ev proto.Event) { p.onEvent(id, ev) })
	p.Hub.Attach(id, conn)
	return func() { p.Hub.Detach(id, conn) }
}

// ensureLocalServer returns the id of the panel host's own server, creating
// it (paired, serial "local") when the local_server_id setting is missing or
// names a server that no longer exists.
func (p *Panel) ensureLocalServer() (int64, error) {
	v, err := p.Store.GetSetting(settingLocalServerID)
	if err != nil {
		return 0, err
	}
	if v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("setelan %s tidak valid: %q", settingLocalServerID, v)
		}
		if _, err := p.Store.GetServer(id); err == nil {
			return id, nil
		} else if !errors.Is(err, store.ErrNotFound) {
			return 0, err
		}
		p.log.Warn("local server was deleted, creating it again", "old_id", id)
	}

	name, err := p.localServerName()
	if err != nil {
		return 0, err
	}
	srv, err := p.Store.CreateServer(name)
	if err != nil {
		return 0, err
	}
	if err := p.Store.MarkPaired(srv.ID, "local", "127.0.0.1"); err != nil {
		return 0, err
	}
	if err := p.Store.SetSetting(settingLocalServerID, strconv.FormatInt(srv.ID, 10)); err != nil {
		return 0, err
	}
	p.log.Info("local server created", "id", srv.ID, "name", name)
	return srv.ID, nil
}

// localServerName is the hostname, or hostname-panel when a server already
// uses that name.
func (p *Panel) localServerName() (string, error) {
	name, err := os.Hostname()
	if err != nil || name == "" {
		name = "panel"
	}
	servers, err := p.Store.ListServers()
	if err != nil {
		return "", err
	}
	for _, s := range servers {
		if s.Name == name {
			return name + "-panel", nil
		}
	}
	return name, nil
}
