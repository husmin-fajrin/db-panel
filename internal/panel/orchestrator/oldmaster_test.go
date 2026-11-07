package orchestrator

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// promoted runs a planned promotion of the rig's candidate: the master is
// then the quarantined old master.
func (g *promoteRig) promoted(t *testing.T) {
	t.Helper()
	id, err := g.e.r.StartPromote(context.Background(), PromoteParams{MasterServerID: g.m.id, CandidateServerID: g.c.id,
		AcceptUnreachable: []int64{}})
	if err != nil {
		t.Fatalf("StartPromote: %v", err)
	}
	g.e.waitJob(id, store.JobSucceeded)
}

func TestLeftoverJobStoresResultAndSendsText(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	var mu sync.Mutex
	var sent []string
	e.r.d.SendText = func(_ context.Context, text string) error {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, text)
		return nil
	}
	ctx := context.Background()
	// Not an old master yet.
	if _, err := e.r.StartLeftover(ctx, g.m.id); err == nil {
		t.Fatal("leftover on a master accepted")
	}
	g.promoted(t)
	from := time.Date(2026, 11, 5, 9, 41, 20, 0, time.Local)
	to := from.Add(11 * time.Second)
	g.m.reply("mysql.leftover", proto.LeftoverResult{GTIDSet: masterUUID + ":121-123", Transactions: 3,
		Tables: []proto.TableTouch{{Schema: "simrs", Table: "pasien", Rows: 2}, {Schema: "simrs", Table: "kunjungan", Rows: 1}},
		From:   &from, To: &to, Binlogs: []string{"binlog.000007"}})
	// Salvage before any check is refused.
	if _, err := e.r.StartSalvage(ctx, g.m.id); err == nil || !strings.Contains(err.Error(), "Periksa data tertinggal dulu") {
		t.Fatalf("salvage before a check: %v", err)
	}
	id, err := e.r.StartLeftover(ctx, g.m.id)
	if err != nil {
		t.Fatalf("StartLeftover: %v", err)
	}
	j, steps := e.waitJob(id, store.JobSucceeded)
	if j.Kind != KindLeftover || len(steps) != 1 || steps[0].Name != "Periksa data tertinggal" {
		t.Fatalf("job %+v steps %+v", j, steps)
	}
	want := "Ada 3 transaksi yang tidak sampai ke master baru, menyentuh simrs.pasien (2 baris), simrs.kunjungan (1 baris), antara 09:41:20 dan 09:41:31"
	if !strings.Contains(steps[0].Message, want) {
		t.Fatalf("step message = %q", steps[0].Message)
	}
	var lp proto.LeftoverParams
	g.m.params("mysql.leftover", &lp)
	if lp.NewMaster.Host != "10.0.0.2" || lp.NewMaster.Port != 3306 || lp.NewMaster.User != replUser || lp.NewMaster.Password != replPW {
		t.Fatalf("leftover params = %+v", lp)
	}
	check, ok := LeftoverOf(g.promotion())
	if !ok || check.Transactions != 3 || check.GTIDSet != masterUUID+":121-123" || check.JobID != id || len(check.Tables) != 2 {
		t.Fatalf("stored check = %+v, %v", check, ok)
	}
	mu.Lock()
	if len(sent) != 1 || !strings.Contains(sent[0], "db-master") || !strings.Contains(sent[0], want) {
		t.Fatalf("telegram = %q", sent)
	}
	mu.Unlock()

	// Amankan extracts the checked set.
	g.m.reply("mysql.salvage", proto.SalvageFile{Name: "20261105T094500Z-1.sql", Size: 2048,
		CreatedAt: time.Now(), ExpiresAt: time.Now().Add(7 * 24 * time.Hour)})
	id, err = e.r.StartSalvage(ctx, g.m.id)
	if err != nil {
		t.Fatalf("StartSalvage: %v", err)
	}
	j, steps = e.waitJob(id, store.JobSucceeded)
	if j.Kind != KindSalvage || !strings.Contains(steps[0].Message, "20261105T094500Z-1.sql") {
		t.Fatalf("salvage job %+v steps %+v", j, steps)
	}
	var sp proto.SalvageParams
	g.m.params("mysql.salvage", &sp)
	if sp.GTIDSet != masterUUID+":121-123" {
		t.Fatalf("salvage params = %+v", sp)
	}
}

func TestSalvageRefusedWithoutLeftover(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	g.promoted(t)
	g.m.reply("mysql.leftover", proto.LeftoverResult{})
	id, err := e.r.StartLeftover(context.Background(), g.m.id)
	if err != nil {
		t.Fatal(err)
	}
	_, steps := e.waitJob(id, store.JobSucceeded)
	if steps[0].Message != "Tidak ada data tertinggal" {
		t.Fatalf("step message = %q", steps[0].Message)
	}
	if _, err := e.r.StartSalvage(context.Background(), g.m.id); err == nil || !strings.Contains(err.Error(), "tidak menemukan data tertinggal") {
		t.Fatalf("salvage without leftover: %v", err)
	}
}

func TestSalvageDirectCallsValidateName(t *testing.T) {
	g := newPromoteRig(t)
	e := g.e
	ctx := context.Background()
	for _, name := range []string{"../etc/passwd", "20261105T094500Z-1.sql/..", "x.sql", ""} {
		if _, err := e.r.SalvageRead(ctx, g.m.id, name, 0); err == nil {
			t.Errorf("SalvageRead(%q) accepted", name)
		}
		if err := e.r.SalvageDelete(ctx, g.m.id, name); err == nil {
			t.Errorf("SalvageDelete(%q) accepted", name)
		}
	}
	if n := g.m.count("mysql.salvageRead") + g.m.count("mysql.salvageDelete"); n != 0 {
		t.Fatalf("%d calls reached the agent", n)
	}
	g.m.reply("mysql.salvageRead", proto.SalvageReadResult{Data: []byte("-- sql"), EOF: true, Size: 6})
	res, err := e.r.SalvageRead(ctx, g.m.id, "20261105T094500Z-1.sql", 0)
	if err != nil || string(res.Data) != "-- sql" || !res.EOF {
		t.Fatalf("SalvageRead = %+v, %v", res, err)
	}
	g.m.reply("mysql.connections", proto.ConnectionsResult{Rows: []proto.ConnectionRow{{Host: "10.0.0.9", User: "app", DB: "simrs", Count: 4}}})
	c, err := e.r.Connections(ctx, g.m.id)
	if err != nil || len(c.Rows) != 1 || c.Rows[0].Count != 4 {
		t.Fatalf("Connections = %+v, %v", c, err)
	}
}
