package web_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

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
	wantNoBody(t, rec, "agent-kata")
	wantCalls(t, f)
	if got := f.gateInputs(); len(got) != 1 || got[0] != (orchestrator.GateInput{Action: orchestrator.GateResync,
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
			if got := f.gateInputs(); len(got) != 1 || got[0] != tc.input {
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
// never gated.
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
	form = withToken(t, e, promoteForm("3", "emergency", "accept_unreachable", "4", "accept_partial", "3",
		"confirm_name", "db-master", "confirm_replica_name", "db-sebagian"), web.ActPromote, "master", 1)
	wantCode(t, e.do("POST", "/masters/1/promote", form, c), http.StatusSeeOther)
	if got := f.gateInputs(); len(got) != 0 {
		t.Fatalf("gate asked for an emergency promote: %+v", got)
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
	wantCalls(t, f)
	if got := f.gateInputs(); len(got) != 1 || got[0] != (orchestrator.GateInput{Action: orchestrator.GateResync,
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
	if got := f.gateInputs(); len(got) != 1 || got[0] != (orchestrator.GateInput{Action: orchestrator.GateRebuildOldMaster,
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
