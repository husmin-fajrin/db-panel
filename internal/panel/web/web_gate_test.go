package web_test

import (
	"encoding/json"
	"html"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/panel/web"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// G14 with the labels of the red rows; G15.
const (
	wantG14Disk = "Tindakan ini ditolak karena ada prasyarat yang belum terpenuhi: Ruang disk. " +
		"Cara memperbaikinya ada di daftar Prasyarat di halaman ini."
	wantG15 = "Centang &#34;Saya sudah memastikan ada cadangan (backup) terbaru&#34; untuk melanjutkan."
)

// redDisk is a gate whose disk row is red, the text of the agent's report
// in its detail (which no refusal may carry further than the label).
func redDisk(orchestrator.GateInput) (orchestrator.GateResult, error) {
	return orchestrator.GateResult{Rows: []orchestrator.GateRow{
		{Key: "agent_version", Label: "Versi agent"},
		{Key: "disk", Label: "Ruang disk", Status: orchestrator.GateRed, Detail: "Disk <agent-kata> terpakai 99% (ambang kritis 95%).",
			Fix: "Kosongkan ruang"},
	}}, nil
}

// backupUnset is a green gate whose backup row asks for the tick.
func backupUnset(orchestrator.GateInput) (orchestrator.GateResult, error) {
	return orchestrator.GateResult{Rows: []orchestrator.GateRow{
		{Key: "backup", Label: "Cadangan (backup)", Status: orchestrator.GateYellow, NeedsAck: true},
	}}, nil
}

func (f *fakeOrch) setGate(g func(orchestrator.GateInput) (orchestrator.GateResult, error)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.gate = g
}

func (f *fakeOrch) gateInputs() []orchestrator.GateInput {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]orchestrator.GateInput(nil), f.gateIns...)
}

// TestGateRefusesBeforeTokenIsConsumed: a resync posted with a valid token
// while a row is red is refused with 409 and G14 before the token is used;
// once the row is green the same token is accepted.
func TestGateRefusesBeforeTokenIsConsumed(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	f.setGate(redDisk)
	form := withToken(t, e, url.Values{"confirm_name": {"db-replika"}}, web.ActResync, "replica", 2)
	rec := e.do("POST", "/replicas/2/resync", form, c)
	wantCode(t, rec, http.StatusConflict)
	wantBody(t, rec, wantG14Disk)
	// The agent's words are in the page's precondition list only, never
	// in the refusal.
	if strings.Contains(outsideGateLists(rec.Body.String()), "agent-kata") {
		t.Fatalf("the agent's text outside the precondition list:\n%s", rec.Body.String())
	}
	wantCalls(t, f)
	// The first input is the handler's; the refused page reads the gate
	// again for its list.
	if got := f.gateInputs(); len(got) == 0 || got[0] != (orchestrator.GateInput{Action: orchestrator.GateResync,
		MasterID: 1, TargetID: 2}) {
		t.Fatalf("gate inputs = %+v", got)
	}

	f.setGate(nil)
	rec = e.do("POST", "/replicas/2/resync", form, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantCalls(t, f, "resync 2  force=false")
}

// TestGateRefusalIsAudited: a refusal by the gate is one audit entry with
// G0 (the red rows' labels, nothing of the agent's report) as its outcome.
func TestGateRefusalIsAudited(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	f.setGate(redDisk)
	form := withToken(t, e, url.Values{"confirm_name": {"db-replika"}}, web.ActResync, "replica", 2)
	wantCode(t, e.do("POST", "/replicas/2/resync", form, c), http.StatusConflict)
	entries := withoutConfirms(auditEntries(t, e.st))
	if len(entries) == 0 {
		t.Fatal("no audit entry")
	}
	last := entries[len(entries)-1]
	if last.Action != "replica_resync" || last.TargetID != 2 || last.Outcome != "prasyarat belum terpenuhi: Ruang disk" {
		t.Fatalf("audit = %+v", last)
	}
	if strings.Contains(last.DetailJSON, "agent-kata") || strings.Contains(last.Outcome, "agent-kata") {
		t.Fatalf("audit carries the agent's text: %+v", last)
	}
	wantNoSecretInDetail(t, last)
}

// Every gated handler asks the gate before its token, and a refusal leaves
// the orchestrator's start uncalled.
func TestGatedHandlersRefuseOnRedRow(t *testing.T) {
	cases := []struct {
		name   string
		env    func(t *testing.T) (*env, *fakeOrch)
		path   string
		form   url.Values
		token  [3]string // action, kind, id
		input  orchestrator.GateInput
		action string // audit
	}{
		{name: "prepare master again", env: ctlEnv, path: "/servers/1/prepare-master", form: url.Values{},
			token: [3]string{web.ActPrepareMasterAgain, "server", "1"},
			input: orchestrator.GateInput{Action: orchestrator.GatePrepareMaster, MasterID: 1}, action: "prepare_master"},
		{name: "add databases", env: ctlEnv, path: "/replicas/2/databases/add", form: url.Values{"databases": {"toko"}},
			input:  orchestrator.GateInput{Action: orchestrator.GateAddDatabases, MasterID: 1, TargetID: 2},
			action: "add_databases"},
		{name: "remove databases with drop", env: ctlEnv, path: "/replicas/2/databases/remove",
			form:  url.Values{"databases": {"gudang"}, "drop_data": {"1"}, "confirm_name": {"db-replika"}},
			token: [3]string{web.ActRemoveDatabases, "replica", "2"},
			input: orchestrator.GateInput{Action: orchestrator.GateRemoveWithDrop, TargetID: 2}, action: "remove_databases"},
		{name: "planned promote", env: promoteEnvF, path: "/masters/1/promote",
			form:  promoteForm("2", "planned", "accept_unreachable", "4"),
			token: [3]string{web.ActPromote, "master", "1"},
			input: orchestrator.GateInput{Action: orchestrator.GatePromotePlanned, MasterID: 1, TargetID: 2}, action: "promote"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, f := tc.env(t)
			c := e.cookie()
			f.setGate(redDisk)
			form := tc.form
			if tc.token[0] != "" {
				form = withToken(t, e, form, tc.token[0], tc.token[1], atoi(tc.token[2]))
			}
			rec := e.do("POST", tc.path, form, c)
			wantCode(t, rec, http.StatusConflict)
			wantBody(t, rec, wantG14Disk)
			// The first input is the handler's; the refused page reads the
			// gate again for its lists.
			if got := f.gateInputs(); len(got) == 0 || got[0] != tc.input {
				t.Fatalf("gate inputs = %+v, want %+v", got, tc.input)
			}
			for _, call := range f.callList() {
				if !strings.HasPrefix(call, "promote-view") {
					t.Fatalf("orchestrator called after a refusal: %v", f.callList())
				}
			}
			entries := withoutConfirms(auditEntries(t, e.st))
			if last := entries[len(entries)-1]; last.Action != tc.action || last.Outcome != "prasyarat belum terpenuhi: Ruang disk" {
				t.Fatalf("audit = %+v", last)
			}
			if tc.token[0] == "" {
				return
			}
			// The token was not used.
			f.setGate(nil)
			rec = e.do("POST", tc.path, form, c)
			if rec.Code == http.StatusForbidden {
				t.Fatalf("the token was consumed by the refusal: %s", rec.Body.String())
			}
		})
	}
}

func ctlEnv(t *testing.T) (*env, *fakeOrch) {
	e, f, _ := controlEnv(t)
	return e, f
}

func promoteEnvF(t *testing.T) (*env, *fakeOrch) {
	e, f, _ := promoteEnv(t)
	f.setView(plannedView())
	return e, f
}

func atoi(s string) int64 {
	n, _ := strconv.ParseInt(s, 10, 64)
	return n
}

// Lepas database without dropping the data, and an emergency promote, are
// never gated: the emergency reads only its own gate (for its audit, never
// a planned one) and starts with a red row.
func TestUngatedHandlersSkipGate(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	f.setGate(redDisk)
	form := withToken(t, e, url.Values{"databases": {"gudang"}}, web.ActRemoveDatabases, "replica", 2)
	wantCode(t, e.do("POST", "/replicas/2/databases/remove", form, c), http.StatusSeeOther)
	if got := f.gateInputs(); len(got) != 0 {
		t.Fatalf("gate asked for remove without drop: %+v", got)
	}

	e, f, _ = promoteEnv(t)
	c = e.cookie()
	f.setGate(redDisk)
	f.setView(emergencyView())
	form = withToken(t, e, promoteForm("3", "emergency", "old_master_dead", "1", "accept_unreachable", "4", "accept_partial", "3",
		"confirm_name", "db-master", "confirm_replica_name", "db-sebagian"), web.ActPromote, "master", 1)
	wantCode(t, e.do("POST", "/masters/1/promote", form, c), http.StatusSeeOther)
	for _, in := range f.gateInputs() {
		if in != (orchestrator.GateInput{Action: orchestrator.GatePromoteEmergency, MasterID: 1, TargetID: 3}) {
			t.Fatalf("gate asked for an emergency promote: %+v", in)
		}
	}
}

// A planned promote whose backup row asks for the tick is refused with 400
// and G15 without it, before its token; with it the orchestrator gets
// BackupAck, and the audit records it. An ErrBackupAck of the orchestrator
// reads the same.
func TestPromoteBackupAck(t *testing.T) {
	e, f, _ := promoteEnv(t)
	c := e.cookie()
	f.setView(plannedView())
	f.setGate(backupUnset)
	form := withToken(t, e, promoteForm("2", "planned", "accept_unreachable", "4"), web.ActPromote, "master", 1)
	rec := e.do("POST", "/masters/1/promote", form, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, wantG15)
	if len(f.promoteList()) != 0 {
		t.Fatal("promote started without the tick")
	}
	form.Set("backup_ack", "1")
	rec = e.do("POST", "/masters/1/promote", form, c) // the same token
	wantCode(t, rec, http.StatusSeeOther)
	if p := f.promoteList(); len(p) != 1 || !p[0].BackupAck {
		t.Fatalf("promotes = %+v", p)
	}
	entries := withoutConfirms(auditEntries(t, e.st))
	last := entries[len(entries)-1]
	var detail map[string]any
	if err := json.Unmarshal([]byte(last.DetailJSON), &detail); err != nil || detail["backup_ack"] != true {
		t.Fatalf("audit = %+v (%v)", last, err)
	}

	// The orchestrator's own refusals read as the handler's.
	f.setGate(nil)
	f.mu.Lock()
	f.promoteErr = orchestrator.ErrBackupAck
	f.mu.Unlock()
	form = withToken(t, e, promoteForm("2", "planned", "accept_unreachable", "4"), web.ActPromote, "master", 1)
	rec = e.do("POST", "/masters/1/promote", form, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, wantG15)
	f.mu.Lock()
	f.promoteErr = &orchestrator.GateError{Rows: []orchestrator.GateRow{{Key: "disk", Label: "Ruang disk", Status: orchestrator.GateRed}}}
	f.mu.Unlock()
	form = withToken(t, e, promoteForm("2", "planned", "accept_unreachable", "4"), web.ActPromote, "master", 1)
	rec = e.do("POST", "/masters/1/promote", form, c)
	wantCode(t, rec, http.StatusConflict)
	wantBody(t, rec, wantG14Disk)
}

// Ulangi of a job that drops data asks the gate before its token.
func TestJobRetryGatedBeforeToken(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	failedResyncJob(t, e, f, resyncJob, "gudang", "toko")
	f.setGate(redDisk)
	form := withToken(t, e, url.Values{}, "job_retry:"+resyncJob, "job", 2)
	rec := e.do("POST", "/jobs/"+resyncJob+"/retry", form, c)
	wantCode(t, rec, http.StatusConflict)
	wantBody(t, rec, wantG14Disk)
	// The refused page lists the red row G14 points at.
	wantBody(t, rec, wantD2, "Kosongkan ruang")
	wantCalls(t, f)
	// The first input is the handler's; the refused page reads the gate
	// again for its list.
	if got := f.gateInputs(); len(got) == 0 || got[0] != (orchestrator.GateInput{Action: orchestrator.GateResync,
		MasterID: 1, TargetID: 2}) {
		t.Fatalf("gate inputs = %+v", got)
	}
	if s := jobStatus(t, e.st, resyncJob); s != store.JobFailed {
		t.Fatalf("job status after a refused retry = %s", s)
	}
	f.setGate(nil)
	rec = e.do("POST", "/jobs/"+resyncJob+"/retry", form, c)
	wantCode(t, rec, http.StatusSeeOther)
	wantCalls(t, f, "retry "+resyncJob)
}

// A gate that cannot judge (here a store failure) refuses; nothing starts.
func TestGateErrorRefuses(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	f.setGate(func(orchestrator.GateInput) (orchestrator.GateResult, error) {
		return orchestrator.GateResult{}, errGateStore
	})
	form := withToken(t, e, url.Values{"confirm_name": {"db-replika"}}, web.ActResync, "replica", 2)
	rec := e.do("POST", "/replicas/2/resync", form, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, errGateStore.Error())
	wantCalls(t, f)
}

var errGateStore = &gateStoreError{}

type gateStoreError struct{}

func (*gateStoreError) Error() string { return "prasyarat: server #2: database terkunci" }

// Bangun ulang asks the gate before its token, without root's approval
// (its row is judged by the start); a missing backup tick is G15; with it
// the orchestrator gets BackupAck and the audit records it.
func TestRebuildGatedBeforeToken(t *testing.T) {
	e, f, _ := failedRebuildEnv(t)
	c := e.cookie()
	f.approval = proto.RebuildApprovalResult{Required: true, Approved: true, RemainingSec: 86000, AgeSec: 60,
		Source: "10.0.0.2:3306", Addr: "10.0.0.2"}
	form := withToken(t, e, rebuildForm("accept_connections", "1"), web.ActRebuildOldMaster, "server", 1)

	f.setGate(redDisk)
	rec := e.do("POST", "/masters/2/replicas", form, c)
	wantCode(t, rec, http.StatusConflict)
	wantBody(t, rec, wantG14Disk)
	wantCalls(t, f)
	// The first input is the handler's (no approval read); the refused
	// page reads the gate again for its list.
	if got := f.gateInputs(); len(got) == 0 || got[0] != (orchestrator.GateInput{Action: orchestrator.GateRebuildOldMaster,
		MasterID: 2, TargetID: 1}) {
		t.Fatalf("gate inputs = %+v", got)
	}

	f.setGate(backupUnset)
	rec = e.do("POST", "/masters/2/replicas", form, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, wantG15)
	wantCalls(t, f)

	form.Set("backup_ack", "1")
	rec = e.do("POST", "/masters/2/replicas", form, c) // the same token
	wantCode(t, rec, http.StatusSeeOther)
	f.mu.Lock()
	last := f.replicas[len(f.replicas)-1]
	f.mu.Unlock()
	if !last.BackupAck || !last.RebuildOldMaster {
		t.Fatalf("create replica params = %+v", last)
	}
	entries := withoutConfirms(auditEntries(t, e.st))
	var detail map[string]any
	if err := json.Unmarshal([]byte(entries[len(entries)-1].DetailJSON), &detail); err != nil || detail["backup_ack"] != true {
		t.Fatalf("audit = %+v (%v)", entries[len(entries)-1], err)
	}
}

// Texts of the precondition list (plan 5b-2b, D1-D4), as the page shows
// them.
const (
	wantD1 = "Prasyarat"
	wantD2 = "Tombol ini mati selama ada prasyarat yang belum terpenuhi. Perbaiki baris bertanda \"Belum terpenuhi\", lalu muat ulang halaman ini."
	wantD3 = "Saya sudah memastikan ada cadangan (backup) terbaru"
	wantD4 = "Panel hanya melihat umur dan ukuran berkas, bukan apakah cadangan itu bisa dipulihkan."
)

// everyKind is a gate with a green row, a yellow row with a fix (the
// backup folder not set), a yellow row without one (not reported yet) and
// a red row.
func everyKind(orchestrator.GateInput) (orchestrator.GateResult, error) {
	return orchestrator.GateResult{Rows: []orchestrator.GateRow{
		{Key: "agent_version", Label: "Versi agent"},
		{Key: "backup", Label: "Cadangan (backup)", Status: orchestrator.GateYellow,
			Detail: "Folder cadangan master db-master belum diisi.", Fix: "Isi folder cadangan di halaman server master."},
		{Key: "account", Label: "Akun panel", Status: orchestrator.GateYellow,
			Detail: "Akun panel belum diketahui: agent db-replika belum melaporkannya."},
		{Key: "disk", Label: "Ruang disk", Status: orchestrator.GateRed, Detail: "Disk db-replika terpakai 99%.",
			Fix: "Kosongkan ruang disk db-replika."},
	}}, nil
}

// yellowOnly is a gate without a red row.
func yellowOnly(orchestrator.GateInput) (orchestrator.GateResult, error) {
	return orchestrator.GateResult{Rows: []orchestrator.GateRow{
		{Key: "agent_version", Label: "Versi agent"},
		{Key: "account", Label: "Akun panel", Status: orchestrator.GateYellow,
			Detail: "Akun panel belum diketahui: agent db-replika belum melaporkannya."},
	}}, nil
}

// outsideGateLists is body without its precondition lists.
func outsideGateLists(body string) string {
	for {
		i := strings.Index(body, "<section class=\"flex min-w-0 flex-col gap-2\" data-gate-list>")
		if i < 0 {
			return body
		}
		j := strings.Index(body[i:], "</section>")
		if j < 0 {
			return body[:i]
		}
		body = body[:i] + body[i+j+len("</section>"):]
	}
}

// tagsWith is every HTML start tag of body that contains attr.
func tagsWith(body, attr string) []string {
	var out []string
	for rest := body; ; {
		i := strings.Index(rest, attr)
		if i < 0 {
			return out
		}
		start := strings.LastIndex(rest[:i], "<")
		end := strings.Index(rest[i:], ">")
		out = append(out, rest[start:i+end+1])
		rest = rest[i+end+1:]
	}
}

// wantOff fails unless body has a tag with attr and every such tag is
// disabled.
func wantOff(t *testing.T, body, attr string) {
	t.Helper()
	tags := tagsWith(body, attr)
	if len(tags) == 0 {
		t.Fatalf("no element with %s", attr)
	}
	for _, tag := range tags {
		if !strings.Contains(tag, " disabled") {
			t.Fatalf("element not disabled: %s", tag)
		}
	}
}

// wantOn fails unless body has a tag with attr and none of them is
// disabled.
func wantOn(t *testing.T, body, attr string) {
	t.Helper()
	tags := tagsWith(body, attr)
	if len(tags) == 0 {
		t.Fatalf("no element with %s", attr)
	}
	for _, tag := range tags {
		if strings.Contains(tag, " disabled") {
			t.Fatalf("element disabled: %s", tag)
		}
	}
}

// TestPromoteScreenListsEveryRow: the promote screen lists every row of
// the chosen candidate's gate (green, yellow and red), with the status in
// words; a row that is not green shows its detail, then its fix when it
// has one; the backup row carries D4. One list per selectable candidate.
func TestPromoteScreenListsEveryRow(t *testing.T) {
	e, f, _ := promoteEnv(t)
	c := e.cookie()
	f.setView(plannedView())
	f.setGate(everyKind)
	rec := e.do("GET", "/masters/1/promote", nil, c)
	wantCode(t, rec, http.StatusOK)
	body := rec.Body.String()
	list := between(body, `data-promote-box="2" data-gate-box`, "</section>")
	if list == "" {
		t.Fatalf("no precondition list for the chosen candidate:\n%s", body)
	}
	for _, want := range []string{"<h3", wantD1, "Versi agent", "Cadangan (backup)", "Akun panel", "Ruang disk",
		"Terpenuhi", "Perlu perhatian", "Belum terpenuhi",
		"Folder cadangan master db-master belum diisi.", "Isi folder cadangan di halaman server master.",
		"Akun panel belum diketahui: agent db-replika belum melaporkannya.",
		"Disk db-replika terpakai 99%.", "Kosongkan ruang disk db-replika.", wantD4} {
		if !strings.Contains(list, want) {
			t.Errorf("list lacks %q:\n%s", want, list)
		}
	}
	// Detail before fix; a row without a fix shows no fix element.
	if strings.Index(list, "Disk db-replika terpakai") > strings.Index(list, "Kosongkan ruang disk") {
		t.Error("the fix comes before the detail")
	}
	account := between(list, `data-gate-row="account"`, "</li>")
	if account == "" || strings.Contains(account, "data-gate-fix") {
		t.Errorf("account row (no fix): %q", account)
	}
	green := between(list, `data-gate-row="agent_version"`, "</li>")
	if green == "" || strings.Contains(green, "data-gate-detail") || strings.Contains(green, "data-gate-fix") {
		t.Errorf("green row: %q", green)
	}
	// The other selectable candidate has its own list, hidden until chosen.
	if !strings.Contains(body, `class="hidden" data-promote-box="3" data-gate-box`) {
		t.Errorf("no hidden list for candidate 3")
	}
	ins := f.gateInputs()
	want := []orchestrator.GateInput{{Action: orchestrator.GatePromotePlanned, MasterID: 1, TargetID: 2},
		{Action: orchestrator.GatePromotePlanned, MasterID: 1, TargetID: 3}}
	if len(ins) != len(want) || ins[0] != want[0] || ins[1] != want[1] {
		t.Fatalf("gate inputs = %+v, want %+v", ins, want)
	}

	// An emergency lists the rows too; a red row disables nothing.
	f.setView(emergencyView())
	rec = e.do("GET", "/masters/1/promote", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, wantD1, "Belum terpenuhi", "Disk db-replika terpakai 99%.")
	wantOn(t, rec.Body.String(), `data-confirm-action="promote"`)
	wantNoBody(t, rec, wantD2, `data-gate-off="1"`)
	if ins := f.gateInputs(); ins[len(ins)-1].Action != orchestrator.GatePromoteEmergency {
		t.Fatalf("emergency gate input = %+v", ins[len(ins)-1])
	}
}

// TestCreateReplicaFormListsRows: Buat replika lists every row of each
// offered target's gate, shown for the chosen one; Bangun ulang lists the
// rows of its one target, the approval judged from the answer the page
// already read (no further agent call).
func TestCreateReplicaFormListsRows(t *testing.T) {
	e, f, _ := newMySQLEnv(t)
	c := e.cookie()
	master := e.pairedOnline(t, "db-master")
	e.pairedOnline(t, "db-calon") // 2
	e.makeMaster(t, master, shopDatabases...)
	f.setGate(everyKind)
	rec := e.do("GET", "/masters/1/replicas/new", nil, c)
	wantCode(t, rec, http.StatusOK)
	body := rec.Body.String()
	list := between(body, `data-gate-for="2"`, "</section>")
	for _, want := range []string{wantD1, "Versi agent", "Terpenuhi", "Perlu perhatian", "Belum terpenuhi",
		"Akun panel belum diketahui: agent db-replika belum melaporkannya.", "Disk db-replika terpakai 99%.",
		"Kosongkan ruang disk db-replika."} {
		if !strings.Contains(list, want) {
			t.Errorf("list lacks %q:\n%s", want, list)
		}
	}
	wantBody(t, rec, `x-data="targetGate"`, `<option value="2" data-gate-off="1" data-gate-red="1">db-calon`)
	if ins := f.gateInputs(); len(ins) != 1 || ins[0] != (orchestrator.GateInput{Action: orchestrator.GateCreateReplica,
		MasterID: 1, TargetID: 2}) {
		t.Fatalf("gate inputs = %+v", ins)
	}

	e, f, _ = failedRebuildEnv(t)
	c = e.cookie()
	f.approval = proto.RebuildApprovalResult{Required: true, Approved: true, RemainingSec: 86000, AgeSec: 60,
		Source: "10.0.0.2:3306", Addr: "10.0.0.2"}
	f.setGate(everyKind)
	rec = e.do("GET", "/masters/2/replicas/new?bangun-ulang=1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, wantD1, "Disk db-replika terpakai 99%.", wantD4)
	approved := true
	if ins := f.gateInputs(); len(ins) != 1 || ins[0] != (orchestrator.GateInput{Action: orchestrator.GateRebuildOldMaster,
		MasterID: 2, TargetID: 1, Approval: ins[0].Approval}) || ins[0].Approval == nil || *ins[0].Approval != approved {
		t.Fatalf("gate inputs = %+v", ins)
	}
	if f.approvalCalls != 1 {
		t.Fatalf("approval read %d times, want 1 (the box's)", f.approvalCalls)
	}
	// Not approved: the row reads the agent's "no".
	f.approval = proto.RebuildApprovalResult{Required: true, Code: "123456", Source: "10.0.0.2:3306", RemainingSec: 1700}
	e.do("GET", "/masters/2/replicas/new?bangun-ulang=1", nil, c)
	if ins := f.gateInputs(); ins[len(ins)-1].Approval == nil || *ins[len(ins)-1].Approval {
		t.Fatalf("gate input = %+v", ins[len(ins)-1])
	}
}

// TestRedRowDisablesButton: with a red row each gated button (the drop
// checkbox of Lepas database) is disabled, D2 is shown below it, and the
// red row's detail and fix are on the page.
func TestRedRowDisablesButton(t *testing.T) {
	cases := []struct {
		name   string
		env    func(t *testing.T) (*env, *fakeOrch)
		method string
		path   string
		form   url.Values
		attr   string // the gated element
		note   string // D2, or for the drop checkbox its option form; "" is D2
	}{
		{name: "promote", env: promoteEnvF, method: "GET", path: "/masters/1/promote", attr: `data-confirm-action="promote"`},
		{name: "create replica", env: createEnv, method: "POST", path: "/masters/1/replicas",
			form: url.Values{"target": {"2"}, "databases": {"toko"}, "confirm_wipe": {"1"}}, attr: `data-gate-button`},
		{name: "rebuild", env: rebuildEnvF, method: "GET", path: "/masters/2/replicas/new?bangun-ulang=1",
			attr: `data-confirm-action="rebuild_old_master"`},
		{name: "resync", env: ctlEnv, method: "GET", path: "/replicas/2", attr: `data-confirm-action="resync"`},
		{name: "prepare master again", env: ctlEnv, method: "GET", path: "/servers/1",
			attr: `data-confirm-action="prepare_master_again"`},
		{name: "prepare master", env: prepareEnv, method: "GET", path: "/servers/1", attr: `>Siapkan sebagai master</button>`},
		{name: "add databases", env: addEnv, method: "GET", path: "/replicas/2/databases", attr: `data-gate-button`},
		{name: "remove databases with drop", env: ctlEnv, method: "GET", path: "/replicas/2/databases",
			attr: `name="drop_data"`, note: wantD2Option},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, f := tc.env(t)
			f.setGate(redDisk)
			rec := e.do(tc.method, tc.path, tc.form, e.cookie())
			body := rec.Body.String()
			wantOff(t, body, tc.attr)
			note := tc.note
			if note == "" {
				note = wantD2
			}
			if !strings.Contains(html.UnescapeString(body), note) {
				t.Errorf("page lacks %q", note)
			}
			wantBody(t, rec, wantD1, "Belum terpenuhi", "Ruang disk", "Disk &lt;agent-kata&gt; terpakai 99% (ambang kritis 95%).",
				"Kosongkan ruang")
			for _, tag := range tagsWith(body, "data-gate-off-note") {
				if strings.Contains(tag, "hidden") {
					t.Errorf("D2 hidden: %s", tag)
				}
			}
			// Only the red row on the pages that list red rows only.
			if tc.name != "promote" && tc.name != "create replica" && tc.name != "rebuild" {
				wantNoBody(t, rec, "Terpenuhi<", `data-gate-row="agent_version"`)
			}
		})
	}
	// Lepas database without dropping the data stays offered.
	e, f := ctlEnv(t)
	f.setGate(redDisk)
	rec := e.do("GET", "/replicas/2/databases", nil, e.cookie())
	wantOn(t, rec.Body.String(), `data-confirm-action="remove_databases"`)
}

func createEnv(t *testing.T) (*env, *fakeOrch) {
	e, f, _ := newMySQLEnv(t)
	master := e.pairedOnline(t, "db-master")
	e.pairedOnline(t, "db-calon") // 2
	e.makeMaster(t, master, shopDatabases...)
	return e, f
}

func rebuildEnvF(t *testing.T) (*env, *fakeOrch) {
	e, f, _ := failedRebuildEnv(t)
	f.approval = proto.RebuildApprovalResult{Required: true, Approved: true, RemainingSec: 86000, AgeSec: 60,
		Source: "10.0.0.2:3306", Addr: "10.0.0.2"}
	return e, f
}

// prepareEnv: server 1 is connected and not a master yet (Siapkan master).
func prepareEnv(t *testing.T) (*env, *fakeOrch) {
	e, f, _ := newMySQLEnv(t)
	e.pairedOnline(t, "db-utama")
	if err := e.st.SetInventory(store.Inventory{ServerID: 1, Connected: true, Version: "8.0.39",
		Databases: shopDatabases, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return e, f
}

// addEnv is controlEnv whose master has a database the replica does not
// follow (Tambah database).
func addEnv(t *testing.T) (*env, *fakeOrch) {
	e, f := ctlEnv(t)
	dbs := append(append([]proto.DatabaseInfo(nil), shopDatabases...), proto.DatabaseInfo{Name: "arsip", Bytes: 1 << 20})
	if err := e.st.SetInventory(store.Inventory{ServerID: 1, Connected: true, Version: "8.0.39", Series: "8.0",
		Databases: dbs, MasterReady: true, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return e, f
}

// TestGreenGateShowsNothingOnModalOnlyButtons: while no row is red, the
// pages whose gated action has only its button (Resync, Siapkan master,
// Siapkan master lagi, Tambah database, Lepas database) show no list and no
// D2, also with yellow rows, and nothing is disabled.
func TestGreenGateShowsNothingOnModalOnlyButtons(t *testing.T) {
	cases := []struct {
		name string
		env  func(t *testing.T) (*env, *fakeOrch)
		path string
		attr string
		in   orchestrator.GateInput // the gate the page reads for attr
	}{
		{"resync", ctlEnv, "/replicas/2", `data-confirm-action="resync"`,
			orchestrator.GateInput{Action: orchestrator.GateResync, MasterID: 1, TargetID: 2}},
		{"prepare master again", ctlEnv, "/servers/1", `data-confirm-action="prepare_master_again"`,
			orchestrator.GateInput{Action: orchestrator.GatePrepareMaster, MasterID: 1}},
		{"prepare master", prepareEnv, "/servers/1", `>Siapkan sebagai master</button>`,
			orchestrator.GateInput{Action: orchestrator.GatePrepareMaster, MasterID: 1}},
		{"add databases", addEnv, "/replicas/2/databases", `data-gate-button`,
			orchestrator.GateInput{Action: orchestrator.GateAddDatabases, MasterID: 1, TargetID: 2}},
		{"remove databases", ctlEnv, "/replicas/2/databases", `name="drop_data"`,
			orchestrator.GateInput{Action: orchestrator.GateRemoveWithDrop, TargetID: 2}},
	}
	for _, tc := range cases {
		for name, g := range map[string]func(orchestrator.GateInput) (orchestrator.GateResult, error){
			"green": nil, "yellow": yellowOnly} {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				e, f := tc.env(t)
				f.setGate(g)
				rec := e.do("GET", tc.path, nil, e.cookie())
				wantCode(t, rec, http.StatusOK)
				wantOn(t, rec.Body.String(), tc.attr)
				wantNoBody(t, rec, "data-gate-list", wantD2, "Akun panel belum diketahui")
				// The page reads the gate of its action, for the servers of
				// that action, and no other server pair for it.
				got := f.gateInputs()
				if !slices.Contains(got, tc.in) {
					t.Fatalf("gate inputs = %+v, want %+v among them", got, tc.in)
				}
				for _, in := range got {
					if in.Action == tc.in.Action && in != tc.in {
						t.Fatalf("gate read for other servers: %+v", in)
					}
				}
			})
		}
	}
}

// TestBackupAckCheckboxShownOnlyWhenNeeded: D3 is a required checkbox
// (backup_ack=1) on a planned promote and a rebuild whose backup row asks
// for it, and nowhere else.
func TestBackupAckCheckboxShownOnlyWhenNeeded(t *testing.T) {
	const box = `name="backup_ack" value="1" required`
	e, f := promoteEnvF(t)
	c := e.cookie()
	rec := e.do("GET", "/masters/1/promote", nil, c)
	wantNoBody(t, rec, box, wantD3)
	f.setGate(backupUnset)
	rec = e.do("GET", "/masters/1/promote", nil, c)
	wantBody(t, rec, box, wantD3)
	wantUnticked(t, rec.Body.String(), `name="backup_ack"`)
	// Nor after a refused post that carried the tick.
	rec = e.do("POST", "/masters/1/promote", withToken(t, e, promoteForm("2", "planned", "backup_ack", "1"),
		web.ActPromote, "master", 1), c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Setujui bahwa replika db-mati diarahkan ulang", box)
	wantUnticked(t, rec.Body.String(), `name="backup_ack"`)
	f.setView(emergencyView())
	rec = e.do("GET", "/masters/1/promote", nil, c)
	wantNoBody(t, rec, box, wantD3)

	e, f = rebuildEnvF(t)
	c = e.cookie()
	rec = e.do("GET", "/masters/2/replicas/new?bangun-ulang=1", nil, c)
	wantNoBody(t, rec, box, wantD3)
	f.setGate(backupUnset)
	rec = e.do("GET", "/masters/2/replicas/new?bangun-ulang=1", nil, c)
	wantBody(t, rec, box, wantD3)
	wantUnticked(t, rec.Body.String(), `name="backup_ack"`)

	// Buat replika never asks for it.
	e, f = createEnv(t)
	f.setGate(backupUnset)
	wantNoBody(t, e.do("GET", "/masters/1/replicas/new", nil, e.cookie()), box, wantD3)
}

// wantUnticked fails unless body has an input with attr and none of them
// is checked.
func wantUnticked(t *testing.T, body, attr string) {
	t.Helper()
	tags := tagsWith(body, attr)
	if len(tags) == 0 {
		t.Fatalf("no element with %s", attr)
	}
	for _, tag := range tags {
		if strings.Contains(tag, "checked") {
			t.Fatalf("box pre-ticked: %s", tag)
		}
	}
}

// TestGateListEscapesServerNames: a row's label, detail and fix carry
// names the operator chose; they are escaped like any other text.
func TestGateListEscapesServerNames(t *testing.T) {
	evil := func(orchestrator.GateInput) (orchestrator.GateResult, error) {
		return orchestrator.GateResult{Rows: []orchestrator.GateRow{
			{Key: "disk", Label: "Ruang <i>disk</i>", Status: orchestrator.GateRed,
				Detail: `Disk <script>alert("db")</script> penuh.`, Fix: `Kosongkan <b onclick="x">db<1</b>.`},
		}}, nil
	}
	for _, tc := range []struct {
		env  func(t *testing.T) (*env, *fakeOrch)
		path string
	}{{promoteEnvF, "/masters/1/promote"}, {ctlEnv, "/replicas/2"}, {createEnv, "/masters/1/replicas/new"}} {
		e, f := tc.env(t)
		f.setGate(evil)
		rec := e.do("GET", tc.path, nil, e.cookie())
		wantBody(t, rec, "Ruang &lt;i&gt;disk&lt;/i&gt;", "Disk &lt;script&gt;alert(&#34;db&#34;)&lt;/script&gt; penuh.",
			"Kosongkan &lt;b onclick=&#34;x&#34;&gt;db&lt;1&lt;/b&gt;.")
		wantNoBody(t, rec, "<i>disk", "<script>alert", `<b onclick`)
	}
}

// TestGateErrorOnPage: a gate that cannot judge leaves the page readable:
// the list is one line saying the preconditions could not be read, and the
// gated button is disabled (its post would be refused).
func TestGateErrorOnPage(t *testing.T) {
	gateErr := func(orchestrator.GateInput) (orchestrator.GateResult, error) {
		return orchestrator.GateResult{}, errGateStore
	}
	for _, tc := range []struct {
		env  func(t *testing.T) (*env, *fakeOrch)
		path string
		attr string
	}{
		{ctlEnv, "/replicas/2", `data-confirm-action="resync"`},
		{promoteEnvF, "/masters/1/promote", `data-confirm-action="promote"`},
		{rebuildEnvF, "/masters/2/replicas/new?bangun-ulang=1", `data-confirm-action="rebuild_old_master"`},
	} {
		e, f := tc.env(t)
		f.setGate(gateErr)
		rec := e.do("GET", tc.path, nil, e.cookie())
		wantCode(t, rec, http.StatusOK)
		wantBody(t, rec, wantD1, "Prasyarat tidak bisa dibaca: prasyarat: server #2: database terkunci")
		wantNoBody(t, rec, wantD2)
		wantOff(t, rec.Body.String(), tc.attr)
	}
}

// TestLateGateRefusalAuditedWithG0: when the orchestrator's start refuses
// with a *GateError after the handler's own gate check passed (a row
// turned red in between), every gated handler audits G0 (the red rows'
// labels), as resync and the job actions do; the page still shows G14.
func TestLateGateRefusalAuditedWithG0(t *testing.T) {
	late := &orchestrator.GateError{Rows: []orchestrator.GateRow{{Key: "disk", Label: "Ruang disk", Status: orchestrator.GateRed,
		Detail: "Disk <agent-kata> penuh."}}}
	cases := []struct {
		name   string
		env    func(t *testing.T) (*env, *fakeOrch)
		path   string
		form   url.Values
		token  [3]string
		action string
	}{
		{name: "planned promote", env: promoteEnvF, path: "/masters/1/promote",
			form:  promoteForm("2", "planned", "accept_unreachable", "4"),
			token: [3]string{web.ActPromote, "master", "1"}, action: "promote"},
		{name: "create replica", env: createEnv, path: "/masters/1/replicas",
			form: url.Values{"target": {"2"}, "databases": {"toko"}, "confirm_wipe": {"1"}}, action: "create_replica"},
		{name: "add databases", env: addEnv, path: "/replicas/2/databases/add", form: url.Values{"databases": {"arsip"}},
			action: "add_databases"},
		{name: "remove databases with drop", env: ctlEnv, path: "/replicas/2/databases/remove",
			form:  url.Values{"databases": {"gudang"}, "drop_data": {"1"}, "confirm_name": {"db-replika"}},
			token: [3]string{web.ActRemoveDatabases, "replica", "2"}, action: "remove_databases"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, f := tc.env(t)
			c := e.cookie()
			form := tc.form
			if tc.token[0] != "" {
				form = withToken(t, e, form, tc.token[0], tc.token[1], atoi(tc.token[2]))
			}
			f.mu.Lock()
			if tc.action == "promote" {
				f.promoteErr = late
			} else {
				f.err = late
			}
			f.mu.Unlock()
			rec := e.do("POST", tc.path, form, c)
			wantCode(t, rec, http.StatusConflict)
			wantBody(t, rec, wantG14Disk)
			entries := withoutConfirms(auditEntries(t, e.st))
			last := entries[len(entries)-1]
			if last.Action != tc.action || last.Outcome != "prasyarat belum terpenuhi: Ruang disk" {
				t.Fatalf("audit = %+v", last)
			}
			if strings.Contains(last.DetailJSON, "agent-kata") {
				t.Fatalf("audit carries the agent's text: %+v", last)
			}
		})
	}
}

// TestPromoteModeMovedBeforeGateAndToken: the mode the form posted is
// checked against the master's mode before the gate and before the
// token: a planned post after the master went offline reads "mode moved"
// (not G14), an emergency post against a planned master too, and neither
// consumes the token.
func TestPromoteModeMovedBeforeGateAndToken(t *testing.T) {
	e, f := promoteEnvF(t)
	c := e.cookie()
	f.setView(emergencyView())
	f.setGate(redDisk)
	planned := withToken(t, e, promoteForm("2", "planned", "accept_unreachable", "4"), web.ActPromote, "master", 1)
	rec := e.do("POST", "/masters/1/promote", planned, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Status master berubah sejak halaman dibuka: sekarang hanya promote darurat yang tersedia.")
	wantNoBody(t, rec, wantG14Disk)
	for _, in := range f.gateInputs() {
		if in.Action == orchestrator.GatePromotePlanned {
			t.Fatalf("planned gate asked for a mode that moved: %+v", f.gateInputs())
		}
	}

	// An emergency post against a planned master: refused, token kept.
	f.setView(plannedView())
	f.setGate(nil)
	emergency := withToken(t, e, promoteForm("2", "emergency", "accept_unreachable", "4", "confirm_name", "db-master"),
		web.ActPromote, "master", 1)
	rec = e.do("POST", "/masters/1/promote", emergency, c)
	wantCode(t, rec, http.StatusBadRequest)
	wantBody(t, rec, "Status master berubah sejak halaman dibuka: sekarang hanya promote terencana yang tersedia.")
	if len(f.promoteList()) != 0 {
		t.Fatal("promote started")
	}
	// The same token, now with the mode the master has, is accepted: the
	// refusals did not consume it.
	emergency.Set("mode", "planned")
	rec = e.do("POST", "/masters/1/promote", emergency, c)
	wantCode(t, rec, http.StatusSeeOther)
	rec = e.do("POST", "/masters/1/promote", planned, c)
	wantCode(t, rec, http.StatusSeeOther)
}

// TestPrepareAgainBoxOpensOnRedRow: the folded "Tindakan lanjutan" box of
// a master's page opens while its "Siapkan ulang" list has a red row, so
// the operator sees why the button is off; folded otherwise.
func TestPrepareAgainBoxOpensOnRedRow(t *testing.T) {
	e, f := ctlEnv(t)
	c := e.cookie()
	box := func() string {
		t.Helper()
		rec := e.do("GET", "/servers/1", nil, c)
		wantCode(t, rec, http.StatusOK)
		tags := tagsWith(rec.Body.String(), "data-advanced")
		if len(tags) != 1 {
			t.Fatalf("advanced box tags = %v", tags)
		}
		return tags[0]
	}
	for name, g := range map[string]func(orchestrator.GateInput) (orchestrator.GateResult, error){
		"green": nil, "yellow": yellowOnly} {
		f.setGate(g)
		if tag := box(); strings.Contains(tag, " open") {
			t.Fatalf("%s: box open: %s", name, tag)
		}
	}
	f.setGate(redDisk)
	if tag := box(); !strings.Contains(tag, " open") {
		t.Fatalf("red: box folded: %s", tag)
	}
}

// TestJobPageRedGate (W-1): the page of a data-dropping job whose gate has
// a red row lists that row (red rows only), disables Ulangi and Setujui
// and shows D2; the page asks the agent nothing. With a green gate the
// buttons are on and no list is drawn.
func TestJobPageRedGate(t *testing.T) {
	const approveJob = "3333333333333333333333333333333d"
	cases := []struct {
		name  string
		setup func(t *testing.T, e *env, f *fakeOrch) string
		attrs []string
	}{
		{name: "retry", setup: func(t *testing.T, e *env, f *fakeOrch) string {
			failedResyncJob(t, e, f, resyncJob, "gudang", "toko")
			return resyncJob
		}, attrs: []string{`data-confirm-action="job_retry:` + resyncJob + `"`, `data-gate-button`}},
		{name: "approve", setup: func(t *testing.T, e *env, f *fakeOrch) string {
			storeJob(t, e.st, approveJob, orchestrator.KindCreateReplica, 1,
				`{"master_server_id":2,"wait_reason":"wipe","wait_list":["toko"]}`, store.JobWaiting,
				"Periksa master", "Periksa server tujuan")
			f.mu.Lock()
			f.drops = map[string]bool{approveJob: true}
			f.mu.Unlock()
			return approveJob
		}, attrs: []string{`data-confirm-action="job_approve:` + approveJob + `"`, `data-gate-button`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, f, _ := controlEnv(t)
			c := e.cookie()
			id := tc.setup(t, e, f)
			f.setGate(redDisk)
			rec := e.do("GET", "/jobs/"+id, nil, c)
			wantCode(t, rec, http.StatusOK)
			body := rec.Body.String()
			for _, attr := range tc.attrs {
				wantOff(t, body, attr)
			}
			wantBody(t, rec, wantD1, wantD2, "Belum terpenuhi", "Ruang disk",
				"Disk &lt;agent-kata&gt; terpakai 99% (ambang kritis 95%).", "Kosongkan ruang")
			wantNoBody(t, rec, "Terpenuhi<", `data-gate-row="agent_version"`)
			if strings.Contains(outsideGateLists(body), "agent-kata") {
				t.Fatalf("the agent's text outside the precondition list:\n%s", body)
			}
			wantCalls(t, f)
			if got := f.gateInputs(); len(got) != 1 || got[0].TargetID == 0 {
				t.Fatalf("gate inputs = %+v", got)
			}

			f.setGate(nil)
			rec = e.do("GET", "/jobs/"+id, nil, c)
			body = rec.Body.String()
			for _, attr := range tc.attrs {
				wantOn(t, body, attr)
			}
			wantNoBody(t, rec, wantD2, "data-gate-list")
		})
	}
}

// TestResyncRefusalOpensDialog (W-3): a resync refused for a red row, by
// the handler or by the start, renders the replica page with the resync
// dialog open, so the list G14 points at is in sight.
func TestResyncRefusalOpensDialog(t *testing.T) {
	const open = `id="modal-resync" class="modal" x-data="openDialog" data-open="true"`
	e, f, _ := controlEnv(t)
	c := e.cookie()
	f.setGate(redDisk)
	form := withToken(t, e, url.Values{"confirm_name": {"db-replika"}}, web.ActResync, "replica", 2)
	rec := e.do("POST", "/replicas/2/resync", form, c)
	wantCode(t, rec, http.StatusConflict)
	wantBody(t, rec, wantG14Disk, open, "Kosongkan ruang", wantD2)

	// The start's own gate refusal (the row turned red after the check).
	f.setGate(nil)
	f.mu.Lock()
	f.err = &orchestrator.GateError{Rows: []orchestrator.GateRow{{Key: "disk", Label: "Ruang disk", Status: orchestrator.GateRed}}}
	f.mu.Unlock()
	rec = e.do("POST", "/replicas/2/resync", form, c)
	wantCode(t, rec, http.StatusConflict)
	wantBody(t, rec, wantG14Disk, open)
	// Another refusal of the start keeps the dialog closed.
	f.mu.Lock()
	f.err = errGateStore
	f.mu.Unlock()
	form = withToken(t, e, url.Values{"confirm_name": {"db-replika"}}, web.ActResync, "replica", 2)
	rec = e.do("POST", "/replicas/2/resync", form, c)
	wantNoBody(t, rec, open)
	f.mu.Lock()
	f.err = nil
	f.mu.Unlock()

	// A plain GET keeps the dialog closed.
	rec = e.do("GET", "/replicas/2", nil, c)
	wantNoBody(t, rec, open)
}

// wantD2Option is D2 for the drop checkbox of Lepas database, whose
// button stays on (W-6): it speaks of the option, not the button.
const wantD2Option = "Pilihan ini mati selama ada prasyarat yang belum terpenuhi. Perbaiki baris bertanda \"Belum terpenuhi\", " +
	"lalu muat ulang halaman ini."

// TestDropOptionOffNote: under the disabled "Hapus datanya juga di
// replika" the note speaks of the option, while the button's D2 stays
// where a button is off (Tambah database).
func TestDropOptionOffNote(t *testing.T) {
	e, f := addEnv(t)
	f.setGate(redDisk)
	rec := e.do("GET", "/replicas/2/databases", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	body := rec.Body.String()
	drop := between(body, `id="lepas"`, "</form>")
	if !strings.Contains(html.UnescapeString(drop), wantD2Option) || strings.Contains(html.UnescapeString(drop), "Tombol ini mati") {
		t.Fatalf("Lepas database's note:\n%s", drop)
	}
	add := body[:strings.Index(body, `id="lepas"`)]
	if !strings.Contains(html.UnescapeString(add), wantD2) {
		t.Fatalf("Tambah database lost D2:\n%s", add)
	}
}
