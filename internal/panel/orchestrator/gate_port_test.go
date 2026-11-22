package orchestrator

import (
	"context"
	"net"
	"slices"
	"sync"
	"testing"
	"time"
)

func TestGatePortProbe(t *testing.T) {
	g := newGateRig(t)
	g.probeOpen = true
	open := "Port MySQL master db-master bisa dijangkau dari host panel; sebaiknya hanya terbuka untuk replika."

	// The panel host is neither the master nor a replica: probed once per
	// master in 10 minutes, at the address replicas use.
	wantRow(t, g.row(GatePromotePlanned, "master_port"), GateYellow, open, wantG11f)
	wantRow(t, g.row(GateRebuildOldMaster, "master_port"), GateYellow, open, wantG11f)
	g.advance(9*time.Minute + 59*time.Second)
	wantRow(t, g.row(GatePromoteEmergency, "master_port"), GateYellow, open, wantG11f)
	if got := g.probed(); !slices.Equal(got, []string{"10.0.0.1:3306"}) {
		t.Fatalf("probes = %v", got)
	}
	g.advance(time.Second)
	g.probeMu.Lock()
	g.probeOpen = false
	g.probeMu.Unlock()
	wantGreen(t, g.row(GatePromotePlanned, "master_port"))
	if got := g.probed(); len(got) != 2 {
		t.Fatalf("probes after 10 minutes = %v", got)
	}

	// Actions without the row never probe.
	g.advance(time.Hour)
	for _, a := range []GateAction{GateCreateReplica, GateResync, GateAddDatabases, GateRemoveWithDrop, GatePrepareMaster} {
		g.gate(g.input(a))
	}
	if got := g.probed(); len(got) != 2 {
		t.Fatalf("probes by actions without the row = %v", got)
	}

	// The panel host is the master, or a replica of its topology: no probe.
	g.probeOpen = true
	for _, local := range []int64{g.m.id, g.rep.id} {
		g.e.r.d.LocalServerID = local
		wantGreen(t, g.row(GatePromotePlanned, "master_port"))
	}
	if got := g.probed(); len(got) != 2 {
		t.Fatalf("probes from a server of the topology = %v", got)
	}
	// Never red, whatever the probe answers.
	g.e.r.d.LocalServerID = g.tgt.id
	if got := g.row(GatePromotePlanned, "master_port"); got.Status != GateYellow {
		t.Fatalf("master_port = %+v", got)
	}
}

// Concurrent gates for one master share one probe.
func TestGatePortProbeOnceWhileInFlight(t *testing.T) {
	g := newGateRig(t)
	g.probeOpen = true
	g.probeWait = make(chan struct{})
	var wg sync.WaitGroup
	rows := make([]GateRow, 5)
	for i := range rows {
		wg.Go(func() {
			res, err := g.e.r.Gate(g.input(GatePromotePlanned))
			if err != nil {
				t.Error(err)
				return
			}
			rows[i] = gateRow(t, res, "master_port")
		})
	}
	g.e.waitFor("the first probe", func() bool { return len(g.probed()) == 1 })
	time.Sleep(50 * time.Millisecond) // the others reach the probe's wait
	close(g.probeWait)
	wg.Wait()
	if got := g.probed(); len(got) != 1 {
		t.Fatalf("probes = %v", got)
	}
	for _, r := range rows {
		if r.Status != GateYellow {
			t.Fatalf("row = %+v", r)
		}
	}
}

// The default probe dials with a one-second bound and sends nothing.
func TestGateDefaultPortProbe(t *testing.T) {
	e := newEnv(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan int, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			got <- -1
			return
		}
		defer c.Close()
		c.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, _ := c.Read(make([]byte, 16))
		got <- n
	}()
	if !e.r.dialPort(ln.Addr().String()) {
		t.Fatal("open port reads as closed")
	}
	if n := <-got; n != 0 {
		t.Fatalf("the probe sent %d bytes", n)
	}
	addr := ln.Addr().String()
	ln.Close()
	if e.r.dialPort(addr) {
		t.Fatal("closed port reads as open")
	}
	var deadline time.Duration
	e.r.d.PortProbe = func(ctx context.Context, _ string) bool {
		d, _ := ctx.Deadline()
		deadline = time.Until(d)
		return false
	}
	e.r.dialPort("10.0.0.1:3306")
	if deadline <= 0 || deadline > time.Second {
		t.Fatalf("probe deadline in %v", deadline)
	}
}

// Ruling R27: the panel host that is the action's target (the old master
// of a rebuild, about to become a replica) does not probe.
func TestGatePortProbeSkipsPanelHostAsTarget(t *testing.T) {
	g := newGateRig(t)
	g.probeOpen = true
	g.e.r.d.LocalServerID = g.old.id
	wantGreen(t, g.row(GateRebuildOldMaster, "master_port"))
	g.e.r.d.LocalServerID = g.rep.id
	in := g.input(GatePromotePlanned)
	in.TargetID = g.rep.id
	wantGreen(t, gateRow(t, g.gate(in), "master_port"))
	if got := g.probed(); len(got) != 0 {
		t.Fatalf("probes = %v", got)
	}
}
