package orchestrator

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"

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
