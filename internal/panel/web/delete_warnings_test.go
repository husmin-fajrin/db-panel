package web

import (
	"slices"
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

func TestDeleteWarningsQuarantinedWithoutBox(t *testing.T) {
	got := deleteWarnings(store.Server{Name: "x", Role: store.RoleQuarantined}, nil, nil, true)
	if len(got) != 1 || !strings.HasPrefix(got[0], "Server ini tetap dikarantina (quarantined) setelah dihapus dari panel:") {
		t.Fatalf("deleteWarnings = %q", got)
	}
	got = deleteWarnings(store.Server{Name: "x", Role: store.RoleQuarantined}, nil, nil, false)
	if len(got) != 1 || !strings.HasPrefix(got[0], "Server ini tetap dikarantina (quarantined) setelah dihapus dari panel, tetapi") {
		t.Fatalf("deleteWarnings, lock not held = %q", got)
	}
	if got := deleteWarnings(store.Server{Name: "x", Role: store.RoleReplica}, nil, nil, true); len(got) != 0 {
		t.Fatalf("deleteWarnings of a replica = %q", got)
	}
}

// TestDeleteDialogNamesReportedRole (P-M3, F6): when the agent reports
// another role pin than quarantined, the delete dialog names that role and
// does not say the server stays quarantined.
func TestDeleteDialogNamesReportedRole(t *testing.T) {
	const f6 = "Agent server ini melaporkan peran replica, bukan karantina. Menghapusnya dari panel tidak mengubah apa pun " +
		"di server itu; periksa dulu peran itu di halaman server."
	srv := store.Server{Name: "x", Role: store.RoleQuarantined}
	box := &oldMasterBox{AgentRole: store.RoleReplica, Leftover: &orchestrator.LeftoverCheck{}}
	for _, lockHolds := range []bool{true, false} {
		got := deleteWarnings(srv, box, nil, lockHolds)
		if !slices.Contains(got, f6) {
			t.Errorf("lock holds %v: deleteWarnings = %q, want F6", lockHolds, got)
		}
		for _, w := range got {
			if strings.Contains(w, "tetap dikarantina") {
				t.Errorf("lock holds %v: %q says the server stays quarantined", lockHolds, w)
			}
		}
	}
}
