package orchestrator

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// TestInventoryRefreshUpdatesAccount: the MySQL account the agent answers
// with is stored with the inventory by Connect, replaced by the next
// RefreshInventory, and kept when a refresh fails.
func TestInventoryRefreshUpdatesAccount(t *testing.T) {
	e := newEnv(t)
	fa, _ := e.readyMaster("db-master")
	ctx := context.Background()

	fa.reply("mysql.connect", proto.ConnectResult{Account: "root@localhost"})
	if _, err := e.r.Connect(ctx, fa.id, proto.ConnectParams{User: "root", Password: "pw"}); err != nil {
		t.Fatal(err)
	}
	if inv, err := e.st.GetInventory(fa.id); err != nil || inv.Account != "root@localhost" {
		t.Fatalf("after Connect: account %q, %v", inv.Account, err)
	}

	fa.reply("mysql.inventory", proto.ConnectResult{Account: "dbpanel_admin@localhost"})
	if _, err := e.r.RefreshInventory(ctx, fa.id); err != nil {
		t.Fatal(err)
	}
	if inv, err := e.st.GetInventory(fa.id); err != nil || inv.Account != "dbpanel_admin@localhost" {
		t.Fatalf("after RefreshInventory: account %q, %v", inv.Account, err)
	}

	fa.fail("mysql.inventory", "login MySQL gagal: 1045 Access denied")
	if _, err := e.r.RefreshInventory(ctx, fa.id); err == nil {
		t.Fatal("failed RefreshInventory returned no error")
	}
	if inv, _ := e.st.GetInventory(fa.id); inv.Account != "dbpanel_admin@localhost" {
		t.Fatalf("a failed refresh changed the account to %q", inv.Account)
	}
}

// TestInventoryAccountBounded: an account from a hostile agent is stored
// as one line of at most 255 bytes, cut on a UTF-8 boundary, without
// control or format characters; an honest one is stored as it is.
func TestInventoryAccountBounded(t *testing.T) {
	e := newEnv(t)
	fa, _ := e.readyMaster("db-master")
	ctx := context.Background()

	hostile := "evil@host\r\nagent: palsu‮\x00 " + strings.Repeat("é", 300)
	fa.reply("mysql.inventory", proto.ConnectResult{Account: hostile})
	if _, err := e.r.RefreshInventory(ctx, fa.id); err != nil {
		t.Fatal(err)
	}
	inv, err := e.st.GetInventory(fa.id)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Account) > 255 || !utf8.ValidString(inv.Account) || !oneLine(inv.Account) ||
		!strings.HasPrefix(inv.Account, "evil@host agent: palsu") {
		t.Fatalf("hostile account stored as %q (%d bytes)", inv.Account, len(inv.Account))
	}
	for _, r := range inv.Account {
		if r < 0x20 || r == 0x7f {
			t.Fatalf("control character %U kept in %q", r, inv.Account)
		}
	}

	fa.reply("mysql.inventory", proto.ConnectResult{Account: "dbpanel_admin@localhost"})
	if _, err := e.r.RefreshInventory(ctx, fa.id); err != nil {
		t.Fatal(err)
	}
	if inv, _ := e.st.GetInventory(fa.id); inv.Account != "dbpanel_admin@localhost" {
		t.Fatalf("honest account stored as %q", inv.Account)
	}
}

// TestOnOnlineRefreshesUnknownAccount (C-1, Ruling R40): when an agent
// connects and its stored inventory is connected without an account (as
// every inventory is after the upgrade), the panel reads the inventory
// once in the background, as Segarkan does, so the Akun panel row judges
// itself. It does not wait for the start lock, does not hold up OnOnline,
// and asks nothing when the account is known or MySQL is not connected.
func TestOnOnlineRefreshesUnknownAccount(t *testing.T) {
	e := newEnv(t)
	fa := e.connectedMaster("db-master")
	release := make(chan struct{})
	fa.on("mysql.inventory", func(json.RawMessage) (any, error) {
		<-release
		return proto.ConnectResult{Status: proto.MySQLStatus{Version: "8.4.3", Series: "8.4"}, Databases: masterDBs,
			Account: "root@localhost"}, nil
	})
	// The start lock held meanwhile (a start judging its gate).
	e.r.startMu.Lock()
	n := fa.count("mysql.inventory")
	done := make(chan struct{})
	go func() {
		e.r.OnOnline(fa.id)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("OnOnline waited for the inventory read")
	}
	e.waitFor("the inventory read", func() bool { return fa.count("mysql.inventory") == n+1 })
	close(release)
	e.waitFor("the account stored", func() bool {
		inv, err := e.st.GetInventory(fa.id)
		return err == nil && inv.Account == "root@localhost"
	})
	e.r.startMu.Unlock()
	res, err := e.r.Gate(GateInput{Action: GatePrepareMaster, MasterID: fa.id})
	if err != nil {
		t.Fatal(err)
	}
	if row := gateRow(t, res, "account"); row.Status != GateRed {
		t.Fatalf("account row after the refresh = %+v, want red", row)
	}

	// Known now: the next connect asks nothing.
	e.r.OnOnline(fa.id)
	// MySQL not connected: nothing either.
	other := e.agent("db-other", "10.0.0.7")
	if err := e.st.SetInventory(store.Inventory{ServerID: other.id, Connected: false, UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	e.r.OnOnline(other.id)
	time.Sleep(50 * time.Millisecond)
	if got := fa.count("mysql.inventory"); got != n+1 {
		t.Fatalf("mysql.inventory sent %d times, want once", got-n)
	}
	if got := other.count("mysql.inventory"); got != 0 {
		t.Fatalf("mysql.inventory sent to a server whose MySQL is not connected")
	}
}
