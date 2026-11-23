package monitor_test

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/notify"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// TestUnpinnedTelegramTextBounded: the Telegram text of master_unpinned
// for a 5,000-character pin with line breaks is short and carries the
// message on one line (the second line is the panel's own tail).
func TestUnpinnedTelegramTextBounded(t *testing.T) {
	pin := strings.Repeat("baris\nbaru\r\n", 500)[:5000]
	now := time.Date(2026, 11, 20, 9, 0, 0, 0, time.Local)
	a := store.Alert{Kind: monitor.KindMasterUnpinned, ServerID: 1, OpenedAt: now,
		Message: monitor.UnpinnedMessage(&proto.HealthSnapshot{RolePin: pin})}
	text := notify.Format(a, "db-master", "", "https://panel.local", now)
	if n := strings.Count(text, "\n"); n != 1 {
		t.Errorf("Telegram text has %d line breaks, want 1 (message, tail):\n%s", n, text)
	}
	if n := utf8.RuneCountInString(text); n > 500 {
		t.Errorf("Telegram text is %d characters, want the pin bounded:\n%s", n, text)
	}
	if !strings.Contains(text, "tidak dikenal (baris baru baris") {
		t.Errorf("Telegram text lacks the bounded pin:\n%s", text)
	}
}
