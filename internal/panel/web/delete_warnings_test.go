package web

import (
	"strings"
	"testing"

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
