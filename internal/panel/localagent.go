package panel

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/husmin-fajrin/db-panel/internal/agent"
	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// attachLocal attaches the agent embedded in the panel process as the
// connection of the panel host's own server, and returns the function that
// detaches it. The embedded agent offers the same commands as a remote one
// (probe, mysql.*, job.*); its MySQL credential and sealing key live in the
// panel's config directory.
func (p *Panel) attachLocal() (detach func()) {
	reg := commands.NewRegistry()
	sys := probe.RealSystem()
	probe.Register(reg, sys, p.opts.Version)
	id := p.localID
	emit := func(ev proto.Event) { p.Hub.OnEvent(id, ev) }
	runner := jobs.NewRunner(localEmitter(emit), p.log)
	runner.Register(reg)
	agent.RegisterMySQL(reg, config.Config{ServerID: id}, p.opts.ConfigDir, sys, runner, p.log)
	conn := hub.NewLocalConn(reg, emit)
	p.Hub.Attach(id, conn)
	return func() { p.Hub.Detach(id, conn) }
}

// localEmitter delivers the embedded agent's job events to the hub; the
// embedded agent is never offline.
type localEmitter func(ev proto.Event)

func (e localEmitter) Emit(ev proto.Event) error {
	e(ev)
	return nil
}

// ensureLocalServer returns the id of the panel host's own server. When the
// local_server_id setting is missing or names a deleted server, it adopts a
// server named after the hostname, or <hostname>-panel, that is unpaired or
// already paired as "local" (left behind if a previous start stopped before
// saving the setting); a name held by a really paired remote server is
// skipped. Without such a row it creates one under the first free name. It
// then marks the server paired as "local" and saves the setting, so every
// step is safe to repeat.
func (p *Panel) ensureLocalServer() (int64, error) {
	v, err := p.Store.GetSetting(settingLocalServerID)
	if err != nil {
		return 0, err
	}
	var previous int64 // id of a local server that was deleted, for the log
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
		previous = id
	}

	id, name, adopted, err := p.adoptOrCreateLocal()
	if err != nil {
		return 0, err
	}
	if err := p.Store.MarkPaired(id, "local", "127.0.0.1"); err != nil {
		return 0, err
	}
	if err := p.Store.SetSetting(settingLocalServerID, strconv.FormatInt(id, 10)); err != nil {
		return 0, err
	}
	msg := "local server created"
	if adopted {
		msg = "adopting existing server as the local server"
	}
	attrs := []any{"id", id, "name", name}
	if previous != 0 {
		attrs = append(attrs, "deleted_id", previous)
	}
	p.log.Info(msg, attrs...)
	return id, nil
}

// adoptOrCreateLocal picks the row for the local server among the names
// <hostname> and <hostname>-panel: the first one that is free is created, the
// first one that holds an unpaired or "local" server is adopted.
func (p *Panel) adoptOrCreateLocal() (id int64, name string, adopted bool, err error) {
	servers, err := p.Store.ListServers()
	if err != nil {
		return 0, "", false, err
	}
	byName := make(map[string]store.Server, len(servers))
	for _, s := range servers {
		byName[s.Name] = s
	}
	host := localServerName()
	for _, name := range []string{host, host + "-panel"} {
		srv, exists := byName[name]
		if !exists {
			created, err := p.Store.CreateServer(name)
			if err != nil {
				return 0, "", false, err
			}
			return created.ID, name, false, nil
		}
		if !srv.Paired || srv.CertSerial == "local" {
			return srv.ID, name, true, nil
		}
	}
	return 0, "", false, fmt.Errorf("nama %q dan %q sudah dipakai server lain; ganti nama salah satunya", host, host+"-panel")
}

// localServerName is the hostname, or "panel" when it is unknown.
func localServerName() string {
	name, err := os.Hostname()
	if err != nil || strings.TrimSpace(name) == "" {
		return "panel"
	}
	return strings.TrimSpace(name)
}
