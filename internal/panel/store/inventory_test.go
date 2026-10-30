package store

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

func TestInventorySetGet(t *testing.T) {
	s := openTemp(t)
	srv, err := s.CreateServer("db1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetInventory(srv.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetInventory without row: err = %v, want ErrNotFound", err)
	}

	at := time.Date(2026, 10, 30, 8, 0, 0, 0, time.UTC)
	inv := Inventory{
		ServerID:  srv.ID,
		Connected: true,
		Version:   "8.0.36",
		Series:    "8.0",
		Databases: []proto.DatabaseInfo{
			{Name: "app", Bytes: 1 << 40, Tables: 12, Engines: []string{"InnoDB"}},
			{Name: "logs", Bytes: 10, Tables: 1},
		},
		MissingGrants: []string{"REPLICATION SLAVE", "RELOAD"},
		MasterReady:   true,
		PreflightJSON: `{"ok":true}`,
		UpdatedAt:     at,
	}
	if err := s.SetInventory(inv); err != nil {
		t.Fatalf("SetInventory: %v", err)
	}
	got, err := s.GetInventory(srv.ID)
	if err != nil {
		t.Fatalf("GetInventory: %v", err)
	}
	if !reflect.DeepEqual(got, inv) {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, inv)
	}

	// Upsert replaces the row; empty preflight and nil lists are fine.
	inv2 := Inventory{ServerID: srv.ID, UpdatedAt: at.Add(time.Hour)}
	if err := s.SetInventory(inv2); err != nil {
		t.Fatalf("SetInventory upsert: %v", err)
	}
	got, err = s.GetInventory(srv.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Connected || got.Version != "" || len(got.Databases) != 0 || len(got.MissingGrants) != 0 || got.PreflightJSON != "" {
		t.Fatalf("upsert did not replace: %+v", got)
	}
	if !got.UpdatedAt.Equal(inv2.UpdatedAt) {
		t.Fatalf("UpdatedAt = %v", got.UpdatedAt)
	}
}
