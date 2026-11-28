package web_test

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
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
			base := noForm
			if strings.HasSuffix(tc.path, "/connect") {
				base = map[string][]string{"user": {"admin"}, "password": {"rahasia"}}
			}
			// Reconnecting server 1 (its account is stored) and Stop
			// replika take a light token, one per post.
			light := map[string]struct {
				action, kind string
			}{"/servers/1/mysql/connect": {web.ActConnectMySQL, "server"}, "/replicas/2/stop": {web.ActStopReplica, "replica"}}
			post := func() *httptest.ResponseRecorder {
				t.Helper()
				form := base
				if l, ok := light[tc.path]; ok {
					form = withToken(t, e, base, l.action, l.kind, tc.server)
				}
				return e.do("POST", tc.path, form, c)
			}
			for _, code := range []string{proto.CodeInternal, proto.CodeOffline} {
				f.err = fmt.Errorf("perintah ditolak: %w", &hub.AgentError{Server: tc.server,
					Err: &proto.Error{Code: code, Message: hostileAgentText}})
				rec := post()
				wantCode(t, rec, http.StatusBadRequest)
				want := "perintah ditolak: Pesan dari agent " + tc.serverName + " (bukan dari panel): " + hostileAgentFlat
				wantBody(t, rec, template.HTMLEscapeString(want))
				wantNoBody(t, rec, "<b>klik", "‮")
			}
			// The hub's own failure is the panel's text.
			f.err = fmt.Errorf("perintah ditolak: %w", proto.ErrOffline)
			rec := post()
			wantBody(t, rec, "perintah ditolak: koneksi agent terputus")
			wantNoBody(t, rec, "(bukan dari panel)")
			f.err = errors.New("ditolak panel")
			wantNoBody(t, post(), "(bukan dari panel)")
			f.err = nil
		})
	}
}

// msgDetachedStopped is T27 (spec 5.11), the flash after Lepas dan
// hentikan replikasi.
const msgDetachedStopped = "Replika dilepas dan replikasinya dihentikan. MySQL di server ini tetap terkunci hanya-baca. " +
	"Kapan aman membukanya, dan caranya, ada di Panduan: \"Memakai bekas replika sebagai server mandiri\"."

// TestDetachFlashT27 (spec 5.9, R-190): the flash after Lepas dan
// hentikan replikasi is T27; it names the guide and gives no SQL.
func TestDetachFlashT27(t *testing.T) {
	e, _, _ := controlEnv(t)
	rec := e.do("GET", "/servers/2?pesan=dilepas-henti", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, template.HTMLEscapeString(msgDetachedStopped))
	wantNoBody(t, rec, "super_read_only=OFF", "SET PERSIST", "mysql -e")
}
