package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// TestPromoteRefusalsOrderUnchanged: the refusals StartPromote had before
// they moved into promoteRefusals keep their texts and their order; each
// pair of conditions answers the refusal that came first.
func TestPromoteRefusalsOrderUnchanged(t *testing.T) {
	const (
		notReplica = "kandidat bukan replika master ini"
		branched   = "replika bercabang: masing-masing punya transaksi yang tidak dimiliki yang lain, sehingga tidak ada " +
			"replika yang memuat semuanya. Pilih satu dan setujui bahwa replika lain yang bercabang dibangun ulang dari master baru"
		notEligible = "cand tidak bisa dipromosikan: Dihentikan"
		partial     = `cand hanya mereplikasi sebagian database; b tidak akan ada di master baru. Setujui itu dulu untuk melanjutkan`
		unreachable = "replika berikut tidak terjangkau: rep-b. Setujui bahwa replika itu diarahkan ulang ke master baru saat " +
			"tersambung lagi untuk melanjutkan"
		busy = "topologi ini masih punya pekerjaan yang belum selesai"
	)
	type cond func(g *promoteRig, p *PromoteParams)
	other := func(g *promoteRig, p *PromoteParams) {
		p.CandidateServerID = g.e.agent("lain", "10.0.0.9").id
	}
	branch := func(g *promoteRig, p *PromoteParams) {
		// The master gone with no report kept; c and b each hold
		// transactions the other lacks (an emergency, as only one can
		// branch them without errant transactions).
		g.offline(g.m)
		g.h.set(g.c.id, replicaView(masterUUID+":1-120,"+otherUUID+":1-2", masterUUID+":1-120"))
		g.h.set(g.b.id, replicaView(masterUUID+":1-121", masterUUID+":1-121"))
		p.Mode, p.OldMasterDeadAck = proto.PromoteEmergency, true
	}
	paused := func(g *promoteRig, p *PromoteParams) {
		v := replicaView(masterGTID, masterGTID)
		v.Status.Code = monitor.StatusPaused
		g.h.set(g.c.id, v)
	}
	part := func(g *promoteRig, p *PromoteParams) {
		if err := g.e.st.SetReplicaDatabases(g.c.id, []string{"a"}); err != nil {
			g.e.t.Fatal(err)
		}
	}
	unreach := func(g *promoteRig, p *PromoteParams) {
		g.b.fail("mysql.probeSource", "tidak terjangkau")
	}
	jobOpen := func(g *promoteRig, p *PromoteParams) {
		if err := g.e.st.CreateJob(store.Job{ID: "7777777777777777777777777777777a", Kind: KindMySQLTLS, ServerID: g.m.id,
			TopologyID: g.topo.ID, ParamsJSON: "{}", CreatedAt: time.Now()}, mysqlTLSStepNames()); err != nil {
			g.e.t.Fatal(err)
		}
	}
	cases := []struct {
		name  string
		conds []cond
		want  string
	}{
		{"not a replica of this master", []cond{other}, notReplica},
		{"branched without acceptance", []cond{branch}, branched},
		{"candidate not eligible", []cond{paused}, notEligible},
		{"partial without acceptance", []cond{part}, partial},
		{"unreachable without acceptance", []cond{unreach}, unreachable},
		{"topology busy", []cond{jobOpen}, busy},
		{"branched before partial", []cond{branch, part}, branched},
		{"not eligible before partial", []cond{paused, part}, notEligible},
		{"partial before unreachable", []cond{part, unreach}, partial},
		{"unreachable before busy", []cond{unreach, jobOpen}, unreachable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newPromoteRig(t)
			p := PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id}
			for _, c := range tc.conds {
				c(g, &p)
			}
			_, err := g.e.r.StartPromote(context.Background(), p)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("StartPromote = %v, want %q", err, tc.want)
			}
			if g.c.count("mysql.lockForPromote")+g.m.count("mysql.lockForPromote") != 0 {
				t.Fatal("a refused promote locked the master")
			}
		})
	}
}

// The texts of spec 5.11, copied letter for letter.
const (
	specT24 = "Replika %s melaporkan bisa ditulis (super_read_only mati), jadi mungkin sudah menjadi master; promote ditolak, " +
		"juga promote darurat. Bila %s memang bukan master (misalnya dibuka dengan tangan untuk perbaikan): kunci lagi di " +
		"halaman replikanya, atau lepas dengan Lepas replika, lalu buka halaman ini lagi. Bila aplikasi sudah menulis ke %s: " +
		"jangan promote; periksa server itu dulu."
	specT24b = "Replika %s terakhir melaporkan bisa ditulis dan sekarang tidak menjawab, jadi panel tidak bisa memastikannya; " +
		"promote ditolak, juga promote darurat. Bila Anda yakin %s bukan master: lepas dengan Lepas replika, lalu buka " +
		"halaman ini lagi."
	specT24c = "Replika %s tidak bisa dibaca sekarang, jadi panel tidak bisa memastikan ia masih baca-saja; promote terencana " +
		"ditolak. Sambungkan agent replika itu, atau lepas dengan Lepas replika, lalu buka halaman ini lagi."
	specT24d = "Replika berikut tidak bisa dibaca sekarang; panel memakai laporan terakhirnya (baca-saja), atau tidak punya " +
		"laporan sama sekali: %s."
)

// named is spec text with every value set to name.
func named(spec, name string) string { return strings.ReplaceAll(spec, "%s", name) }

// staleWritable is a replica report an hour old saying super_read_only off.
func staleWritable() monitor.View {
	v := replicaView(masterGTID, masterGTID)
	v.Snap.SuperReadOnly = false
	v.ReceivedAt = time.Now().Add(-time.Hour)
	return v
}

// promoteModes runs fn on a fresh rig for each mode: planned on the rig as
// it is, emergency with the master's agent away (and the replicas' probes
// of it failing), d away too in both. p is the start for candidate c.
func promoteModes(t *testing.T, fn func(t *testing.T, g *promoteRig, p PromoteParams)) {
	for _, mode := range []string{proto.PromotePlanned, proto.PromoteEmergency} {
		t.Run(mode, func(t *testing.T) {
			g := newPromoteRig(t)
			p := PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id, Mode: mode}
			if mode == proto.PromoteEmergency {
				g = emergencyRigFrom(g)
				p.OldMasterDeadAck = true
				p.AcceptUnreachable = []int64{g.c.id, g.b.id, g.d.id}
			}
			fn(t, g, p)
		})
	}
}

// emergencyRigFrom is emergencyRig on g, d kept online.
func emergencyRigFrom(g *promoteRig) *promoteRig {
	g.offline(g.m)
	for _, fa := range []*fakeAgent{g.c, g.b, g.d} {
		fa.reply("mysql.probeSource", proto.ProbeSourceResult{Reachable: false, Err: "connection refused", Host: "10.0.0.1", Port: 3306})
	}
	return g
}

// TestPromoteStaleWritableReportReadLive (R-198): a stale report saying
// writable is read again; mysql.status answering super_read_only ON lets
// the promote start, in both modes.
func TestPromoteStaleWritableReportReadLive(t *testing.T) {
	promoteModes(t, func(t *testing.T, g *promoteRig, p PromoteParams) {
		g.h.set(g.b.id, staleWritable())
		mark := g.b.mark()
		id, err := g.e.r.StartPromote(context.Background(), p)
		if err != nil {
			t.Fatalf("StartPromote with a stale writable report answered read-only: %v", err)
		}
		if !slices.Contains(g.b.cmds(mark), "mysql.status") {
			t.Fatal("the stale replica was not read")
		}
		g.e.waitJob(id, store.JobSucceeded)
	})
}

// TestPromoteLiveWritableRefused: a replica whose agent answers
// super_read_only off refuses both modes with T24.
func TestPromoteLiveWritableRefused(t *testing.T) {
	promoteModes(t, func(t *testing.T, g *promoteRig, p PromoteParams) {
		v := replicaView(masterGTID, masterGTID)
		v.Snap.SuperReadOnly = false
		g.h.set(g.b.id, v) // live, and writable: read again
		g.b.reply("mysql.status", proto.MySQLStatus{Version: "8.4.3", Port: 3306, GTIDExecuted: masterGTID})
		_, err := g.e.r.StartPromote(context.Background(), p)
		if want := named(specT24, "rep-b"); err == nil || err.Error() != want {
			t.Fatalf("StartPromote = %v, want %q", err, want)
		}
		var we *PromoteWritableError
		if !errors.As(err, &we) || we.Code != "writable" || we.Name != "rep-b" {
			t.Fatalf("err = %#v, want *PromoteWritableError writable rep-b", err)
		}
		noPromotionStarted(t, g)
	})
}

// TestPlannedRefusesUnreadableReplica: a planned promote is refused with
// T24c while a replica cannot be read: its agent offline, or not answering
// mysql.status within the bound.
func TestPlannedRefusesUnreadableReplica(t *testing.T) {
	for _, how := range []string{"offline", "silent"} {
		t.Run(how, func(t *testing.T) {
			g := newPromoteRig(t)
			switch how {
			case "offline":
				g.b.detach() // its last report (read-only) is no longer live
			case "silent":
				g.e.r.otherReadTimeout = 200 * time.Millisecond
				v := replicaView(masterGTID, masterGTID)
				v.ReceivedAt = time.Now().Add(-time.Hour)
				g.h.set(g.b.id, v)
				g.b.mu.Lock()
				g.b.silent = map[string]bool{"mysql.status": true}
				g.b.mu.Unlock()
			}
			_, err := g.e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id,
				Mode: proto.PromotePlanned, AcceptUnreachable: []int64{g.b.id}})
			if want := named(specT24c, "rep-b"); err == nil || err.Error() != want {
				t.Fatalf("StartPromote = %v, want %q", err, want)
			}
			noPromotionStarted(t, g)
		})
	}
}

// TestEmergencyNotHeldByOfflineReadOnlyReplica: an emergency promote is
// not held by a replica whose agent is away and whose last report says
// read-only; the view names it (T24d).
func TestEmergencyNotHeldByOfflineReadOnlyReplica(t *testing.T) {
	g := emergencyRigFrom(newPromoteRig(t))
	g.b.detach() // its read-only report stays
	v, err := g.e.r.PromoteView(context.Background(), g.m.id)
	if err != nil {
		t.Fatal(err)
	}
	if v.Mode != proto.PromoteEmergency || !slices.Equal(v.Unread, []string{"rep-b"}) {
		t.Fatalf("view mode %q, Unread %v; want emergency, [rep-b]", v.Mode, v.Unread)
	}
	if got, want := v.UnreadNote(), named(specT24d, "rep-b"); got != want {
		t.Fatalf("UnreadNote = %q, want %q", got, want)
	}
	id, err := g.e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id,
		Mode: proto.PromoteEmergency, OldMasterDeadAck: true, AcceptUnreachable: []int64{g.b.id, g.d.id}})
	if err != nil {
		t.Fatalf("emergency promote with an offline read-only replica: %v", err)
	}
	g.e.waitJob(id, store.JobSucceeded)
}

// TestEmergencyOfflineWritableReplicaRefused: an emergency promote is held
// by a replica whose agent is away and whose last report says writable
// (T24b).
func TestEmergencyOfflineWritableReplicaRefused(t *testing.T) {
	for _, how := range []string{"offline", "silent"} {
		t.Run(how, func(t *testing.T) {
			g := emergencyRigFrom(newPromoteRig(t))
			g.h.set(g.b.id, staleWritable())
			switch how {
			case "offline":
				g.b.detach()
			case "silent":
				g.e.r.otherReadTimeout = 200 * time.Millisecond
				g.b.mu.Lock()
				g.b.silent = map[string]bool{"mysql.status": true}
				g.b.mu.Unlock()
			}
			_, err := g.e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id,
				Mode: proto.PromoteEmergency, OldMasterDeadAck: true, AcceptUnreachable: []int64{g.b.id, g.c.id, g.d.id}})
			if want := named(specT24b, "rep-b"); err == nil || err.Error() != want {
				t.Fatalf("StartPromote = %v, want %q", err, want)
			}
			noPromotionStarted(t, g)
		})
	}
}

// TestEmergencyReplicaWithoutAnyReport (NR-7): a replica that cannot be
// read and has no report at all does not hold an emergency promote; the
// view names it.
func TestEmergencyReplicaWithoutAnyReport(t *testing.T) {
	g := emergencyRigFrom(newPromoteRig(t))
	g.offline(g.d) // away, its report gone with it
	v, err := g.e.r.PromoteView(context.Background(), g.m.id)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(v.Unread, []string{"rep-d"}) {
		t.Fatalf("Unread = %v, want [rep-d]", v.Unread)
	}
	id, err := g.e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id,
		Mode: proto.PromoteEmergency, OldMasterDeadAck: true, AcceptUnreachable: []int64{g.b.id, g.d.id}})
	if err != nil {
		t.Fatalf("emergency promote with a replica without any report: %v", err)
	}
	g.e.waitJob(id, store.JobSucceeded)
}

// TestPromoteDirectReadsRunInParallelAndBounded: three replicas whose
// agents do not answer mysql.status are read at once, each within the
// bound: PromoteView returns in less than twice the bound.
func TestPromoteDirectReadsRunInParallelAndBounded(t *testing.T) {
	g := emergencyRigFrom(newPromoteRig(t))
	const bound = 200 * time.Millisecond
	g.e.r.otherReadTimeout = bound
	for _, fa := range []*fakeAgent{g.c, g.b, g.d} {
		fa.mu.Lock()
		fa.silent = map[string]bool{"mysql.status": true}
		fa.mu.Unlock()
	}
	start := time.Now()
	v, err := g.e.r.PromoteView(context.Background(), g.m.id)
	took := time.Since(start)
	if err != nil {
		t.Fatal(err)
	}
	if took >= 2*bound {
		t.Fatalf("PromoteView took %v, want < %v", took, 2*bound)
	}
	for _, fa := range []*fakeAgent{g.c, g.b, g.d} {
		if fa.count("mysql.status") != 1 {
			t.Fatalf("%s read %d times, want 1", fa.name, fa.count("mysql.status"))
		}
	}
	if !slices.Equal(v.Unread, []string{"cand", "rep-b", "rep-d"}) {
		t.Fatalf("Unread = %v", v.Unread)
	}
}

// TestStartPromoteStoresAcceptFlags: the job's state records the
// acceptances the start was given, for a later Ulangi.
func TestStartPromoteStoresAcceptFlags(t *testing.T) {
	g := emergencyRigFrom(newPromoteRig(t))
	e := g.e
	g.offline(g.d)
	g.h.set(g.c.id, replicaView(masterUUID+":1-120,"+otherUUID+":1-2", masterUUID+":1-120"))
	g.h.set(g.b.id, replicaView(masterUUID+":1-121", masterUUID+":1-121"))
	g.b.reply("mysql.repoint", proto.RepointResult{NeedsRebuild: true, Errant: masterUUID + ":121"})
	id, err := e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id,
		Mode: proto.PromoteEmergency, OldMasterDeadAck: true, AcceptBranched: true, AcceptUnreachable: []int64{g.b.id, g.d.id}})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	j, _ := e.waitJob(id, store.JobSucceeded)
	var st jobState
	if err := json.Unmarshal([]byte(j.ParamsJSON), &st); err != nil {
		t.Fatal(err)
	}
	if !st.AcceptBranched || !slices.Equal(st.AcceptUnreachable, []int64{g.b.id, g.d.id}) {
		t.Fatalf("state accept_branched %v, accept_unreachable %v", st.AcceptBranched, st.AcceptUnreachable)
	}
}
