package redact

import (
	"strings"
	"testing"
)

func TestRedactQuotes(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"tanpa kutipan", "Error 2003: Can't connect", "Error 2003: Can't connect"},
		{"kosong", "", ""},
		{"angka panjang tetap", "12345678", "12345678"},
		{"posisi binlog tetap", "the master log mysql-bin.000003, end_log_pos 123456789",
			"the master log mysql-bin.000003, end_log_pos 123456789"},
		{"duplikat", "Duplicate entry 'Budi' for key 'PRIMARY'", "Duplicate entry '…' for key '…'"},
		{"kutip ganda", `Incorrect string value: "Budi" for column`, "Incorrect string value: '…' for column"},
		{"escape garis miring", `'O\'Brien'`, "'…'"},
		{"escape ganda", "'a''b'", "'…'"},
		{"escape dalam kalimat", `Duplicate entry 'O\'Brien-12' for key 'nama'`, "Duplicate entry '…' for key '…'"},
		{"escape kutip ganda", `"a""b" dan "c\"d"`, "'…' dan '…'"},
		{"angka dalam kutipan disensor", "Duplicate entry '3201234567890001' for key 'nik'", "Duplicate entry '…' for key '…'"},
		{"pengguna dan host", "Access denied for user 'Budi'@'10.0.0.9' (using password: YES)",
			"Access denied for user '…'@'…' (using password: YES)"},
		{"kutipan kosong", "nilai '' kosong", "nilai '…' kosong"},
		{"tak tertutup", "Duplicate entry 'Budi Santoso for key", "Duplicate entry '…"},
		{"tak tertutup hanya sebaris", "baris 'Budi\nbaris kedua tanpa kutipan", "baris '…\nbaris kedua tanpa kutipan"},
		{"literal lintas baris", "nilai 'Jl. Mawar\nNo. 5' ditolak", "nilai '…' ditolak"},
		{"apostrof dalam kata", "the event's master log 'x'", "the event's master log '…'"},
		{"kutip di dalam nilai", "Duplicate entry 'Budi's' for key 'k'", "Duplicate entry '…' for key '…'"},
		{"error SQL replika bersarang",
			"Error 'Duplicate entry 'Budi' for key 'PRIMARY'' on query. Default database: 'rs'. Query: 'INSERT INTO pasien VALUES ('Budi')'",
			""}, // dicek di bawah: tanpa Budi, rs, INSERT
		{"nilai menempel kata", "entry x'Budi' ditolak", "entry x'…' ditolak"},
		{"apostrof di akhir", "Can't connect to MySQL server on 'db1' (111)", "Can't connect to MySQL server on '…' (111)"},
		{"akun panel tetap", "DROP USER 'dbpanel_repl'@'%', 'dbpanel_repl'@'10.0.0.%';",
			"DROP USER 'dbpanel_repl'@'%', 'dbpanel_repl'@'10.0.0.%';"},
		{"akun dump panel tetap", "GRANT FLUSH_TABLES ON *.* TO 'dbpanel_dump_9ec8cc7d'@'10.0.0.2' WITH GRANT OPTION",
			"GRANT FLUSH_TABLES ON *.* TO 'dbpanel_dump_9ec8cc7d'@'10.0.0.2' WITH GRANT OPTION"},
		{"akun lain disensor", "Access denied for user 'dbpanel'@'h', 'Budi'@'10.0.0.9'", "Access denied for user '…'@'…', '…'@'…'"},
		{"nilai mirip akun panel", "Duplicate entry 'dbpanel_repl Budi' for key 'k'", "Duplicate entry '…' for key '…'"},
		{"host tanpa akun panel", "'x'@'10.0.0.1'", "'…'@'…'"},
		{"bukan host", "'dbpanel_repl'@'Budi Santoso'", "'dbpanel_repl'@'…'"},
		{"backtick tetap", "Table `rs`.`pasien` doesn't exist", "Table `rs`.`pasien` doesn't exist"},
		{"utf8", "Duplicate entry 'Siti Nurhalizah ✓' for key 'k'", "Duplicate entry '…' for key '…'"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Quotes(c.in)
			if c.want == "" && c.in != "" {
				for _, leak := range []string{"Budi", "rs'", "INSERT", "pasien"} {
					if strings.Contains(got, leak) {
						t.Fatalf("Quotes(%q) = %q, masih memuat %q", c.in, got, leak)
					}
				}
				if !strings.HasPrefix(got, "Error '…'") {
					t.Fatalf("Quotes(%q) = %q, want prefix Error '…'", c.in, got)
				}
			} else if got != c.want {
				t.Fatalf("Quotes(%q) = %q, want %q", c.in, got, c.want)
			}
			if again := Quotes(got); again != got {
				t.Fatalf("Quotes tidak stabil: %q → %q", got, again)
			}
		})
	}
}
