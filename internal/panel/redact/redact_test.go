package redact

import (
	"strings"
	"testing"
)

// quoteCases are TestRedactQuotes' table; TestRedactQuotesFixedPoint
// runs Quotes twice over every input.
var quoteCases = []struct {
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
	{"tak tertutup", "Duplicate entry 'Budi Santoso for key", "Duplicate entry '…'"},
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
	{"query update", "Error 'Duplicate entry 'x' for key 'k'' on query. Default database: 'rs'. Query: 'UPDATE pasien SET alamat='Jl Mawar' WHERE nik=3201234567890001'",
		"Error '…' on query. Default database: '…'. Query: '…'"},
	{"query insert", "Error 'Duplicate entry '7' for key 'PRIMARY'' on query. Default database: 'rs'. Query: 'INSERT INTO pasien VALUES (7, 'Budi', 81234567890, NULL)'",
		"Error '…' on query. Default database: '…'. Query: '…'"},
	{"query sampai kutip terakhir", "Query: 'INSERT INTO pasien VALUES ('Budi'", "Query: '…'"},
	{"query tanpa kutip penutup", "Query: 'INSERT INTO pasien VALUES (7, 3201234567890001", "Query: '…'"},
	{"query lintas baris", "Query: 'INSERT INTO pasien\nVALUES (7, 'Budi', 3201234567890001)'", "Query: '…'"},
	{"query terpotong 512 byte", "Error 'Duplicate entry '7' for key 'PRIMARY'' on query. Default database: 'rs'. " +
		"Query: 'INSERT INTO pasien VALUES (7, 'Budi', 3201234567890001, 'Jl Mawar No",
		"Error '…' on query. Default database: '…'. Query: '…'"},
	{"query lalu baris lain", "Query: 'UPDATE p SET a='x'\nbaris berikut 'Budi' 3201234567890001", "Query: '…'"},
	{"duplikat dengan kutip di nilai", "Duplicate entry 'Rumah 'Melati' Blok 3' for key 'alamat'", "Duplicate entry '…' for key '…'"},
	{"error SQL bersarang kutip di nilai",
		"Error 'Duplicate entry 'Rumah 'Melati' Blok 3' for key 'alamat'' on query. Default database: 'rs'. Query: 'INSERT INTO p VALUES ('Rumah 'Melati' Blok 3')'",
		"Error '…' on query. Default database: '…'. Query: '…'"},
	{"write_rows kutip di nilai", "Could not execute Write_rows event on table rs.p; Duplicate entry 'Rumah 'Melati' Blok 3' for key 'alamat', Error_code: 1062; end_log_pos 123456789",
		"Could not execute Write_rows event on table rs.p; Duplicate entry '…' for key '…', Error_code: 1062; end_log_pos 123456789"},
	{"incorrect value kutip di nilai", "Incorrect string value: 'Jl 'Mawar' 5' for column 'alamat' at row 1",
		"Incorrect string value: '…' for column '…' at row 1"},
	{"literal heksa", "entry x'ab' ditolak", "entry x'…' ditolak"},
	{"literal bit", "nilai b'1010' dan X'4275' ditolak", "nilai b'…' dan X'…' ditolak"},
	{"host angka saja", "'dbpanel_a'@'3201234567890001'", "'dbpanel_a'@'…'"},
	{"host ipv6 tetap", "'dbpanel_repl'@'fe80::1'", "'dbpanel_repl'@'fe80::1'"},
	{"backtick tetap", "Table `rs`.`pasien` doesn't exist", "Table `rs`.`pasien` doesn't exist"},
	// SEC-74: values holding a quote then a non-word character or a
	// newline, parse errors, and cut messages leave no fragment.
	{"parse error lintas baris", "Error 'You have an error in your SQL syntax; check the manual that corresponds to your MySQL server version for the right syntax to use near 'nama = 'Budi'\n AND nik = 3201234567890123' at line 1' on query. Default database: 'rs'. Query: 'SELECT 1'",
		"Error '…' on query. Default database: '…'. Query: '…'"},
	{"parse error tanpa error luar", "You have an error in your SQL syntax; check the manual for the right syntax to use near 'nama = 'Budi'\n AND nik = 3201234567890123' at line 1",
		"You have an error in your SQL syntax; check the manual for the right syntax to use near '…'"},
	{"truncated incorrect", "Truncated incorrect DOUBLE value: 'Jl. Merdeka No' 5 RT 3'", "Truncated incorrect DOUBLE value: '…'"},
	{"duplikat newline dan kutip", "Duplicate entry 'Budi\nO' Santoso' for key 'nama'", "Duplicate entry '…' for key '…'"},
	{"error terpotong kutip di nilai", "Error 'Duplicate entry 'O' Brien", "Error '…'"},
	{"utf8", "Duplicate entry 'Siti Nurhalizah ✓' for key 'k'", "Duplicate entry '…' for key '…'"},
}

// leakFragments are pieces of the SEC-74 inputs' values; none may
// survive Quotes, whole or cut at any byte.
var leakFragments = []string{"Budi", "3201234567890123", " 5 RT 3", "Santoso", "Brien", "AND nik"}

func TestRedactQuotes(t *testing.T) {
	for _, c := range quoteCases {
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

// TestRedactQuotesFixedPoint: censoring censored text changes nothing,
// for every table input and for each input cut at every byte (as MySQL
// cuts Last_SQL_Error at 512 bytes).
func TestRedactQuotesFixedPoint(t *testing.T) {
	for _, c := range quoteCases {
		for n := 0; n <= len(c.in); n++ {
			in := c.in[:n]
			once := Quotes(in)
			if twice := Quotes(once); twice != once {
				t.Errorf("%s, cut at %d: Quotes(%q) = %q, Quotes again = %q", c.name, n, in, once, twice)
			}
			for _, leak := range leakFragments {
				if strings.Contains(once, leak) {
					t.Errorf("%s, cut at %d: Quotes(%q) = %q leaks %q", c.name, n, in, once, leak)
				}
			}
		}
	}
}
