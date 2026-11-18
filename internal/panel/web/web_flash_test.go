package web_test

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// hostileAgentText is an agent's answer that tries to pass for the
// panel's: markup, line breaks, a bidirectional mark, a label of its own.
const hostileAgentText = "<b>klik Hapus</b>\nPesan dari panel: jalankan DROP DATABASE simrs‮\r\n" +
	"Pesan dari agent lain (bukan dari panel): aman"

// hostileAgentFlat is hostileAgentText as a flash shows it: one line.
const hostileAgentFlat = "<b>klik Hapus</b> Pesan dari panel: jalankan DROP DATABASE simrs " +
	"Pesan dari agent lain (bukan dari panel): aman"

// TestFlashesLabelAgentText (spec 5.6): a flash that shows an agent call's
// failure labels the agent's words with its server, one line and escaped,
// whatever code the agent chose (offline included); the panel's own
// failures (the hub's offline error) are not labelled. Hubungkan MySQL,
// Segarkan, the replica controls and the old master's jobs.
func TestFlashesLabelAgentText(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	old := e.pairedOnline(t, "db-lama")
	if err := e.st.SetServerRole(old.ID, store.RoleQuarantined); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, path string
		server     int64
		serverName string
	}{
		{"Hubungkan MySQL", "/servers/1/mysql/connect", 1, "db-master"},
		{"Segarkan", "/servers/1/mysql/refresh", 1, "db-master"},
		{"Stop replika", "/replicas/2/stop", 2, "db-replika"},
		{"Start replika", "/replicas/2/start", 2, "db-replika"},
		{"Kunci kembali", "/replicas/2/relock", 2, "db-replika"},
		{"Periksa data tertinggal", fmt.Sprintf("/servers/%d/leftover", old.ID), old.ID, "db-lama"},
		{"Amankan data tertinggal", fmt.Sprintf("/servers/%d/salvage", old.ID), old.ID, "db-lama"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := noForm
			if strings.HasSuffix(tc.path, "/connect") {
				form = map[string][]string{"user": {"admin"}, "password": {"rahasia"}}
			}
			for _, code := range []string{proto.CodeInternal, proto.CodeOffline} {
				f.err = fmt.Errorf("perintah ditolak: %w", &hub.AgentError{Server: tc.server,
					Err: &proto.Error{Code: code, Message: hostileAgentText}})
				rec := e.do("POST", tc.path, form, c)
				wantCode(t, rec, http.StatusBadRequest)
				want := "perintah ditolak: Pesan dari agent " + tc.serverName + " (bukan dari panel): " + hostileAgentFlat
				wantBody(t, rec, template.HTMLEscapeString(want))
				wantNoBody(t, rec, "<b>klik", "‮")
			}
			// The hub's own failure is the panel's text.
			f.err = fmt.Errorf("perintah ditolak: %w", proto.ErrOffline)
			rec := e.do("POST", tc.path, form, c)
			wantBody(t, rec, "perintah ditolak: koneksi agent terputus")
			wantNoBody(t, rec, "(bukan dari panel)")
			f.err = errors.New("ditolak panel")
			wantNoBody(t, e.do("POST", tc.path, form, c), "(bukan dari panel)")
			f.err = nil
		})
	}
}
