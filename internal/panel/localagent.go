package panel

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

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

// ensureLocalServer returns the id of the panel host's own server. When the
// local_server_id setting is missing or names a deleted server, it adopts the
// server named after the hostname (left behind if a previous start stopped
// before saving the setting) or creates it, marks it paired as "local" and
// saves the setting. Every step is safe to repeat.
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

	name := localServerName()
	id, found, err := p.serverIDByName(name)
	if err != nil {
		return 0, err
	}
	if found {
		p.log.Info("adopting existing server as the local server", "id", id, "name", name)
	} else {
		srv, err := p.Store.CreateServer(name)
		if err != nil {
			return 0, err
		}
		id = srv.ID
		p.log.Info("local server created", "id", id, "name", name)
	}
	if err := p.Store.MarkPaired(id, "local", "127.0.0.1"); err != nil {
		return 0, err
	}
	if err := p.Store.SetSetting(settingLocalServerID, strconv.FormatInt(id, 10)); err != nil {
		return 0, err
	}
	return id, nil
}

// localServerName is the hostname, or "panel" when it is unknown.
func localServerName() string {
	name, err := os.Hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		return "panel"
	}
	return strings.TrimSpace(name)
}

// serverIDByName returns the id of the server called name, if any.
func (p *Panel) serverIDByName(name string) (int64, bool, error) {
	servers, err := p.Store.ListServers()
	if err != nil {
		return 0, false, err
	}
	for _, s := range servers {
		if s.Name == name {
			return s.ID, true, nil
		}
	}
	return 0, false, nil
}
