package orchestrator

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// wantOneRecordNoParams checks fa got exactly one mysql.recordReplicas and
// that it carried no data: the agent never receives a replica's host or
// port from the panel.
func wantOneRecordNoParams(t *testing.T, what string, fa *fakeAgent) {
	t.Helper()
	got := fa.allParams("mysql.recordReplicas")
	if len(got) != 1 {
		t.Fatalf("%s: %s got %d mysql.recordReplicas calls, want 1", what, fa.name, len(got))
	}
	switch strings.TrimSpace(got[0]) {
	case "", "null", "{}":
	default:
		t.Fatalf("%s: mysql.recordReplicas params = %q, want none", what, got[0])
	}
}

func TestRecordReplicasCalledAfterVerify(t *testing.T) {
	t.Run("create replica", func(t *testing.T) {
		e := newEnv(t)
		m, _ := e.readyMaster("db-master")
		r := e.replicaAgent("replica1", "clone")
		id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
			MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
		if err != nil {
			t.Fatal(err)
		}
		e.waitJob(id, store.JobWaiting)
		if err := e.r.Approve(id, JobAck{Confirmed: true}); err != nil {
			t.Fatal(err)
		}
		e.waitJob(id, store.JobSucceeded)
		wantOneRecordNoParams(t, "create replica", m)
		if n := r.count("mysql.recordReplicas"); n != 0 {
			t.Fatalf("the replica got %d recordReplicas calls; only the master is asked", n)
		}
	})
	t.Run("resync", func(t *testing.T) {
		e := newEnv(t)
		m, topo := e.readyMaster("db-master")
		r := e.resyncReplica(topo, []proto.DatabaseInfo{{Name: "a", Bytes: 100}}, "a")
		id, err := e.r.StartResync(context.Background(), ResyncParams{ServerID: r.id, WipeList: []string{"a"}})
		if err != nil {
			t.Fatal(err)
		}
		e.waitJob(id, store.JobSucceeded)
		wantOneRecordNoParams(t, "resync", m)
	})
	t.Run("add databases", func(t *testing.T) {
		e := newEnv(t)
		m, topo := e.readyMaster("db-master")
		r := e.followingReplica(topo, "a")
		id, err := e.r.StartAddDatabases(context.Background(), r.id, []string{"b"})
		if err != nil {
			t.Fatal(err)
		}
		e.waitJob(id, store.JobSucceeded)
		wantOneRecordNoParams(t, "add databases", m)
	})
	t.Run("remove databases records nothing", func(t *testing.T) {
		e := newEnv(t)
		m, topo := e.readyMaster("db-master")
		r := e.followingReplica(topo, "a", "b")
		r.reply("mysql.dropDatabases", nil)
		id, err := e.r.StartRemoveDatabases(context.Background(), r.id, []string{"b"}, false)
		if err != nil {
			t.Fatal(err)
		}
		e.waitJob(id, store.JobSucceeded)
		if n := m.count("mysql.recordReplicas"); n != 0 {
			t.Fatalf("remove databases: %d recordReplicas calls, want 0", n)
		}
	})
	t.Run("promote repoints to the new master", func(t *testing.T) {
		g := newPromoteRig(t)
		id, err := g.e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
		if err != nil {
			t.Fatal(err)
		}
		g.e.waitJob(id, store.JobSucceeded)
		// b and d were both repointed; each connection is recorded on the NEW
		// master (the candidate), never on the old one.
		if n := g.c.count("mysql.recordReplicas"); n != 2 {
			t.Fatalf("new master got %d recordReplicas, want 2 (one per repointed replica)", n)
		}
		if n := g.m.count("mysql.recordReplicas"); n != 0 {
			t.Fatalf("old master got %d recordReplicas", n)
		}
		for _, p := range g.c.allParams("mysql.recordReplicas") {
			if s := strings.TrimSpace(p); s != "" && s != "null" && s != "{}" {
				t.Fatalf("recordReplicas params = %q", p)
			}
		}
	})
	t.Run("RepointNow", func(t *testing.T) {
		g := newPromoteRig(t)
		if err := g.e.st.SwapMaster(g.topo.ID, g.m.id, g.c.id, "10.0.0.2", 3306); err != nil {
			t.Fatal(err)
		}
		if err := g.e.st.SetReplicaState(g.b.id, store.ReplicaNeedsRepoint, ""); err != nil {
			t.Fatal(err)
		}
		if err := g.e.r.RepointNow(context.Background(), g.b.id); err != nil {
			t.Fatal(err)
		}
		wantOneRecordNoParams(t, "RepointNow", g.c)
	})
	t.Run("OnOnline", func(t *testing.T) {
		g := newPromoteRig(t)
		g.offline(g.d)
		id, err := g.e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id,
			AcceptUnreachable: []int64{g.d.id}})
		if err != nil {
			t.Fatal(err)
		}
		g.e.waitJob(id, store.JobSucceeded)
		before := g.c.count("mysql.recordReplicas") // b, repointed by the job
		g.d.attach()
		g.e.r.OnOnline(g.d.id)
		// The state is stored before the master is asked to record.
		g.e.waitFor("d recorded", func() bool { return g.c.count("mysql.recordReplicas") > before })
		if n := g.c.count("mysql.recordReplicas") - before; n != 1 {
			t.Fatalf("OnOnline: new master got %d recordReplicas, want 1", n)
		}
	})
	t.Run("repoint with the receiver not connected records nothing", func(t *testing.T) {
		g := newPromoteRig(t)
		if err := g.e.st.SwapMaster(g.topo.ID, g.m.id, g.c.id, "10.0.0.2", 3306); err != nil {
			t.Fatal(err)
		}
		if err := g.e.st.SetReplicaState(g.b.id, store.ReplicaNeedsRepoint, ""); err != nil {
			t.Fatal(err)
		}
		g.b.reply("mysql.repoint", proto.RepointResult{IORunning: false})
		if err := g.e.r.RepointNow(context.Background(), g.b.id); err != nil {
			t.Fatal(err)
		}
		if n := g.c.count("mysql.recordReplicas"); n != 0 {
			t.Fatalf("%d recordReplicas for a receiver that is not connected", n)
		}
	})
}

func TestRecordReplicasOldAgentIgnored(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	m.oldAgent("mysql.recordReplicas")
	r := e.followingReplica(topo, "a")
	id, err := e.r.StartAddDatabases(context.Background(), r.id, []string{"b"})
	if err != nil {
		t.Fatal(err)
	}
	_, steps := e.waitJob(id, store.JobSucceeded)
	if got := stepStatuses(steps); slices.ContainsFunc(got, func(s string) bool { return s != store.StepDone }) {
		t.Fatalf("steps = %v", got)
	}
	if n := m.count("mysql.recordReplicas"); n != 1 {
		t.Fatalf("recordReplicas calls = %d, want 1 (answered unknown_command)", n)
	}
}

func TestQuarantineStepLogsUnguardedStartLock(t *testing.T) {
	run := func(t *testing.T, res proto.QuarantineResult) string {
		g := newPromoteRig(t)
		g.m.reply("mysql.quarantine", res)
		id, err := g.e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id})
		if err != nil {
			t.Fatal(err)
		}
		g.e.waitJob(id, store.JobSucceeded)
		return g.e.jobLog(id)
	}
	t.Run("not locked", func(t *testing.T) {
		log := run(t, proto.QuarantineResult{Applied: true, StartLocked: false,
			StartLockReason:   "berkas kunci tidak terbaca oleh pengguna mysql",
			OperatorInitFiles: []string{"/etc/mysql/ops-init.sql"}})
		for _, want := range []string{
			"Master lama dipin dikarantina, tetapi belum terjamin terkunci saat MySQL menyala: berkas kunci tidak terbaca oleh pengguna mysql.",
			"init_file milik operator berikut tidak dijalankan selama karantina: /etc/mysql/ops-init.sql.",
		} {
			if !strings.Contains(log, want) {
				t.Fatalf("job log lacks %q:\n%s", want, log)
			}
		}
	})
	t.Run("locked", func(t *testing.T) {
		log := run(t, proto.QuarantineResult{Applied: true, StartLocked: true})
		if strings.Contains(log, "belum terjamin terkunci") {
			t.Fatalf("job log warns although the start lock holds:\n%s", log)
		}
	})
	t.Run("older agent", func(t *testing.T) {
		// No StartLocked in the answer: not guaranteed, whatever the reason.
		log := run(t, proto.QuarantineResult{Applied: true})
		if !strings.Contains(log, "belum terjamin terkunci saat MySQL menyala") {
			t.Fatalf("job log lacks the warning:\n%s", log)
		}
	})
}

func TestPromoteViewRefusesCandidateWithStartLock(t *testing.T) {
	g := newPromoteRig(t)
	set := func(sl *proto.StartLockState) {
		v := replicaView(masterGTID, masterGTID)
		v.Snap.StartLock = sl
		g.h.set(g.c.id, v)
	}
	candidate := func() Candidate {
		v, err := g.e.r.PromoteView(context.Background(), g.m.id)
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range v.Candidates {
			if c.ServerID == g.c.id {
				return c
			}
		}
		t.Fatal("candidate missing")
		return Candidate{}
	}
	// Present, even though the agent says Locked is false or true: either
	// way a leftover start lock keeps the server from being promoted.
	for _, sl := range []*proto.StartLockState{
		{Present: true, Locked: true},
		{Present: true, Locked: false, Reason: "apa pun"},
	} {
		set(sl)
		if c := candidate(); c.Eligible || c.Reason != reasonStartLock {
			t.Fatalf("StartLock %+v: eligible=%v reason=%q", sl, c.Eligible, c.Reason)
		}
	}
	// Not present, or an old agent that reports none: eligible.
	for _, sl := range []*proto.StartLockState{nil, {Present: false}} {
		set(sl)
		if c := candidate(); !c.Eligible || c.Reason != "" {
			t.Fatalf("StartLock %+v: eligible=%v reason=%q", sl, c.Eligible, c.Reason)
		}
	}
}
