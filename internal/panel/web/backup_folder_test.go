package web

import (
	"fmt"
	"strings"
	"testing"

	agentmonitor "github.com/husmin-fajrin/db-panel/internal/agent/monitor"
)

const (
	wantB3 = `Folder cadangan harus path absolut tanpa "..", misalnya /var/backups/mysql.`
	wantB4 = "Folder di bawah %s tidak terlihat oleh layanan agent; pakai folder lain, misalnya /var/backups/mysql."
	wantB5 = "Folder %s tidak boleh dipakai sebagai folder cadangan."
)

// backupFolderCases is the folder rule's table: each path with the text
// the form shows ("" when the folder is accepted).
var backupFolderCases = []struct {
	path, want string
}{
	{"/var/backups/mysql", ""},
	{"/srv/b", ""},
	{"/homework", ""},
	{"/tmpfiles", ""},
	{"/var/tmpx", ""},
	{"/devices/b", ""},
	{"", wantB3}, // the empty folder ("scan off") is handled before the rule
	{"relatif", wantB3},
	{"relatif/b", wantB3},
	{"/a/../b", wantB3},
	{"/a//b", wantB3},
	{"/a/./b", wantB3},
	{"/var/backups/", wantB3},
	{"/" + strings.Repeat("a", 1024), wantB3}, // 1025 bytes
	{"/var/backups/my\nsql", wantB3},
	{"/var/backups/my\x7fsql", wantB3},
	{"/var/backups/my\x00sql", wantB3},
	{"/home/x", fmt.Sprintf(wantB4, "/home")},
	{"/home", fmt.Sprintf(wantB4, "/home")},
	{"/root", fmt.Sprintf(wantB4, "/root")},
	{"/run/user/1000", fmt.Sprintf(wantB4, "/run/user")},
	{"/tmp/x", fmt.Sprintf(wantB4, "/tmp")},
	{"/var/tmp", fmt.Sprintf(wantB4, "/var/tmp")},
	{"/", fmt.Sprintf(wantB5, "/")},
	{"/proc/1", fmt.Sprintf(wantB5, "/proc")},
	{"/sys", fmt.Sprintf(wantB5, "/sys")},
	{"/dev/sda", fmt.Sprintf(wantB5, "/dev")},
}

// panelOnlyFolderCases are refused by the panel and accepted by the
// agent, on purpose (Ruling R23): a folder with a Unicode control
// character (C1) or not valid UTF-8 could not be shown back on a page
// unchanged. They are left out of the table shared with the agent.
var panelOnlyFolderCases = []string{
	"/var/backups/my\u0085sql",
	"/var/backups/my\u009fsql",
	"/var/backups/my\xffsql",
	"/var/backups/\xc3",
}

func TestBackupFolderRules(t *testing.T) {
	for _, p := range panelOnlyFolderCases {
		if text, ok := validBackupFolder(p); ok || text != wantB3 {
			t.Errorf("validBackupFolder(%q) = %q, %v; want B3", p, text, ok)
		}
	}
	if n := len("/" + strings.Repeat("a", 1024)); n != 1025 {
		t.Fatalf("long case is %d bytes", n)
	}
	if n := len("/" + strings.Repeat("a", 1023)); n != 1024 {
		t.Fatalf("limit case is %d bytes", n)
	}
	if text, ok := validBackupFolder("/" + strings.Repeat("a", 1023)); !ok {
		t.Errorf("1024-byte folder refused: %q", text)
	}
	for _, c := range backupFolderCases {
		text, ok := validBackupFolder(c.path)
		if ok != (c.want == "") || text != c.want {
			t.Errorf("validBackupFolder(%q) = %q, %v; want %q", c.path, text, ok, c.want)
		}
		if strings.ContainsFunc(text, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			t.Errorf("validBackupFolder(%q) text carries a control byte: %q", c.path, text)
		}
	}
}

// The panel refuses what the agent refuses, and nothing else, over the
// shared table. The two rules differ by design on panelOnlyFolderCases
// (C1 controls, invalid UTF-8), which the panel alone refuses; those are
// not part of this table.
func TestBackupFolderRulesMatchAgent(t *testing.T) {
	for _, c := range backupFolderCases {
		_, panelOK := validBackupFolder(c.path)
		_, agentOK := agentmonitor.ValidBackupFolder(c.path)
		if panelOK != agentOK {
			t.Errorf("%q: panel ok %v, agent ok %v", c.path, panelOK, agentOK)
		}
	}
}

// The form trims white space and trailing slashes (never "/" itself),
// nothing else (Ruling R20).
func TestTrimBackupFolder(t *testing.T) {
	for in, want := range map[string]string{
		"  /var/backups/mysql/ ": "/var/backups/mysql",
		"/var/backups//":         "/var/backups",
		"\t/srv/b\n":             "/srv/b",
		"/":                      "/",
		" /// ":                  "/",
		"/a//b":                  "/a//b",
		"   ":                    "",
		"":                       "",
	} {
		if got := trimBackupFolder(in); got != want {
			t.Errorf("trimBackupFolder(%q) = %q, want %q", in, got, want)
		}
	}
}

// What the form shows back never carries the control bytes typed.
func TestShownBackupFolder(t *testing.T) {
	for in, want := range map[string]string{
		"/var/backups/mysql": "/var/backups/mysql",
		"/a\nb\x00c\x7fd":    "/a�b�c�d",
		"/a\u0085b":          "/a�b",
		"/a\xffb":            "/a�b",
	} {
		if got := shownBackupFolder(in); got != want {
			t.Errorf("shownBackupFolder(%q) = %q, want %q", in, got, want)
		}
	}
}
