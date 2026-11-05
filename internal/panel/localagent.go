package panel

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// The panel host's own server is served by a separate agent process (the
// dbpanel-agent unit, as root), which pairs like any agent with a token the
// panel leaves for it in ConfigDir.
const (
	localPairTokenFile = "local-pair-token"
	localPairTokenTTL  = 15 * time.Minute
	// legacyLocalSerial marks a local server paired by the agent that
	// earlier versions embedded in the panel process.
	legacyLocalSerial = "local"
)

// ensureLocalServer returns the id of the panel host's own server. When the
// local_server_id setting is missing or names a deleted server, it adopts a
// server named after the hostname, or <hostname>-panel, that is unpaired or
// paired as "local" (left behind if a previous start stopped before saving
// the setting); a name held by a really paired remote server is skipped.
// Without such a row it creates one under the first free name. A local
// server not paired by a real agent (new, adopted, or paired as "local" by
// the embedded agent of earlier versions) is reset to unpaired at the
// public host, and the setting saved, so every step is safe to repeat.
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
		srv, err := p.Store.GetServer(id)
		if err == nil {
			if srv.CertSerial == legacyLocalSerial {
				if err := p.Store.MarkUnpaired(id, p.opts.PublicHost); err != nil {
					return 0, err
				}
				p.log.Info("local server needs its own agent paired now", "id", id)
			}
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
	if err := p.Store.MarkUnpaired(id, p.opts.PublicHost); err != nil {
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
		if !srv.Paired || srv.CertSerial == legacyLocalSerial {
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

// syncLocalPairToken leaves ConfigDir/local-pair-token for the panel host's
// agent while the local server is not paired: a new 15-minute token and the
// SPKI pin of the panel's HTTPS certificate, "<token>\n<pin>\n", owner
// only (install.sh hands it to the agent). Once the server is paired the
// file is deleted.
func (p *Panel) syncLocalPairToken() error {
	srv, err := p.Store.GetServer(p.localID)
	if err != nil {
		return err
	}
	if srv.Paired && srv.CertSerial != "" && srv.CertSerial != legacyLocalSerial {
		return p.removeLocalPairToken()
	}
	tok, err := p.Store.CreatePairToken(srv.ID, p.now(), localPairTokenTTL)
	if err != nil {
		return fmt.Errorf("token server lokal: %w", err)
	}
	if err := writeFileAtomic(p.localPairTokenPath(), []byte(tok.Token+"\n"+p.certs.pin()+"\n")); err != nil {
		return fmt.Errorf("tulis %s: %w", localPairTokenFile, err)
	}
	p.log.Info("pairing token for the local agent written", "path", p.localPairTokenPath(), "expires", tok.ExpiresAt)
	return nil
}

// removeLocalPairToken deletes ConfigDir/local-pair-token, if any.
func (p *Panel) removeLocalPairToken() error {
	if err := os.Remove(p.localPairTokenPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("hapus %s: %w", localPairTokenFile, err)
	}
	return nil
}

func (p *Panel) localPairTokenPath() string {
	return filepath.Join(p.opts.ConfigDir, localPairTokenFile)
}

// onPaired runs after an agent paired: the local server's token file is
// used up.
func (p *Panel) onPaired(serverID int64) {
	if serverID != p.localID {
		return
	}
	if err := p.removeLocalPairToken(); err != nil {
		p.log.Warn("local agent paired, but its token file stays", "err", err)
	}
}
