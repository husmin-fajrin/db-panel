package redact

import (
	"strings"
	"testing"
)

// The agent's texts that quote the panel's own phrases and an advised
// account (G-6), copied from internal/agent at head character for
// character. A change of those texts that quotes a new phrase needs
// panelPhrases extended (and these copies updated).
const (
	textA6 = "server ini masih punya database yang tidak disalin dari sumber 10.0.0.2:3306 (lama_app, arsip); " +
		"karantina tidak diakhiri. Ulangi Bangun ulang sebagai replika dari panel. Bila panel lalu menolak karena " +
		"database itu tidak ada di master baru, lihat README, bagian \"Database yang tidak ada di master baru\"."
	textNoLoad = "sumber replikasi sudah diatur, tetapi server ini tetap dikarantina: agent tidak punya catatan salinan " +
		"data dari bangun ulang ini (misalnya karena agent dimulai ulang di tengah jalan). Mulai lagi Bangun ulang " +
		"sebagai replika dari panel; tombol \"Ulangi dari tahap ini\" pada pekerjaan ini tidak cukup, karena salinan " +
		"data harus diulang."
	textUnsupported = "daftar database di server ini tidak bisa dipastikan lengkap: nama akun MySQL yang dipakai agent " +
		"memuat karakter yang tidak didukung pemeriksaan ini. Hubungkan MySQL di panel dengan akun lain yang namanya " +
		"hanya huruf, angka, garis bawah, titik, atau tanda hubung (misalnya dbpanel_admin). Sesudah itu tekan " +
		"\"Ulangi dari tahap ini\" pada pekerjaan di panel."
	textClone = "CLONE gagal: 3862 Clone Donor Error: 1045 : Access denied. Bila CLONE terus gagal, ulangi dengan " +
		"pilihan \"Paksa dump\""
)

// listingText is the agent's listing refusal for account (as quoteAccount
// prints it), copied from rebuild_load.go (errListIncomplete).
func listingText(account string) string {
	return "daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL " + account + " yang dipakai agent " +
		"tidak punya hak SHOW DATABASES. Beri hak itu sebagai root MySQL di server ini: GRANT SHOW DATABASES ON *.* TO " +
		account + "; Server ini baca-saja, jadi perintah itu harus dijalankan dengan langkah di README, bagian \"Memberi " +
		"hak SHOW DATABASES di server yang dikarantina\". Sesudah itu tekan \"Ulangi dari tahap ini\" pada pekerjaan di panel."
}

// TestQuotesKeepsPanelPhrases (G-6): the panel's own quoted phrases and the
// account the listing refusal advises come out unchanged.
func TestQuotesKeepsPanelPhrases(t *testing.T) {
	for _, in := range []string{textA6, textNoLoad, textUnsupported, textClone,
		listingText("'dbpanel_admin'@'localhost'"), listingText("'panel_admin'@'10.0.0.5'"),
		listingText("'app.user-1$'@'%'"),
		"Tekan \"Sudah saya alihkan\" dulu", "tekan 'Sudah saya alihkan' dulu"} {
		if got := Quotes(in); got != in {
			t.Errorf("Quotes changed\n %q\nto\n %q", in, got)
		}
	}
}

// phraseCases are near misses: censored as before G-6.
var phraseCases = []struct{ name, in, want string }{
	{"longer literal", `tekan "Ulangi dari tahap ini lagi"`, `tekan '…'`},
	{"one character off", `tekan "Ulangi dari tahap ini."`, `tekan '…'`},
	{"other case", `tekan "ulangi dari tahap ini"`, `tekan '…'`},
	{"phrase inside a value", `"Ulangi dari tahap ini" dan 'Budi'`, `"Ulangi dari tahap ini" dan '…'`},
	{"phrase as a duplicate entry", "Duplicate entry 'Ulangi dari tahap ini' for key 'k'", "Duplicate entry '…' for key '…'"},
	{"phrase as a query", "Query: 'Ulangi dari tahap ini'", "Query: '…'"},
	{"account after other words", "Access denied for user 'panel_admin'@'10.0.0.5'", "Access denied for user '…'@'…'"},
	{"account after other GRANT", "GRANT SELECT ON *.* TO 'panel_admin'@'10.0.0.5'", "GRANT SELECT ON *.* TO '…'@'…'"},
	{"user with a space", "akun MySQL 'panel admin'@'localhost' yang", "akun MySQL '…'@'…' yang"},
	{"user with a quote", "akun MySQL 'pa''nel'@'localhost' yang", "akun MySQL '…'@'…' yang"},
	{"host of digits only", "akun MySQL 'panel_admin'@'3201234567890001' yang", "akun MySQL '…'@'…' yang"},
	{"user with no host", "akun MySQL 'panel_admin' yang", "akun MySQL '…' yang"},
	{"row data next to a kept phrase", `Duplicate entry 'Budi' for key 'k'. Tekan "Ulangi dari tahap ini".`,
		`Duplicate entry '…' for key '…'. Tekan "Ulangi dari tahap ini".`},
}

func TestQuotesPhraseNearMisses(t *testing.T) {
	for _, c := range phraseCases {
		if got := Quotes(c.in); got != c.want {
			t.Errorf("%s: Quotes(%q) = %q, want %q", c.name, c.in, got, c.want)
		}
	}
}

// TestQuotesPhrasesFixedPoint: the fixed point and no leak, for the new
// inputs cut at every byte too.
func TestQuotesPhrasesFixedPoint(t *testing.T) {
	ins := []string{textA6, textNoLoad, textUnsupported, textClone, listingText("'panel_admin'@'10.0.0.5'"),
		listingText("'dbpanel_admin'@'localhost'")}
	for _, c := range phraseCases {
		ins = append(ins, c.in)
	}
	for _, in := range ins {
		for n := 0; n <= len(in); n++ {
			once := Quotes(in[:n])
			if twice := Quotes(once); twice != once {
				t.Errorf("cut at %d: Quotes(%q) = %q, again %q", n, in[:n], once, twice)
			}
			if strings.Contains(once, "Budi") || strings.Contains(once, "3201234567890001") {
				t.Errorf("cut at %d: Quotes(%q) = %q leaks", n, in[:n], once)
			}
		}
	}
}

// adjacentInputs put a kept literal right before a literal in the other
// quote character (H-3, N-G4).
var adjacentInputs = []string{
	`'Paksa dump'"Budi"`,
	`"Ulangi dari tahap ini"'Budi'`,
	`'Sudah saya alihkan'"Budi" dan "Budi"`,
	`tidak bisa dipastikan lengkap: akun MySQL 'a'@'b.c'"Budi"`,
	`'dbpanel_repl'@'h.x'"Budi"`,
	`'Paksa dump'"Budi`,
	`'Paksa dump''Budi'`,
}

// TestQuotesAdjacentFixedPoint: Quotes(Quotes(s)) == Quotes(s) with
// adjacent literals, every input cut at every byte, and no leak.
func TestQuotesAdjacentFixedPoint(t *testing.T) {
	for _, in := range adjacentInputs {
		for n := 0; n <= len(in); n++ {
			once := Quotes(in[:n])
			if twice := Quotes(once); twice != once {
				t.Errorf("cut at %d: Quotes(%q) = %q, again %q", n, in[:n], once, twice)
			}
			if strings.Contains(once, "Budi") {
				t.Errorf("cut at %d: Quotes(%q) = %q leaks", n, in[:n], once)
			}
		}
	}
}

// TestQuotesAdvisedAccountAnchored (H-3, N-G5): an account-shaped pair is
// kept only after the agent's whole sentence prefixes, at a word boundary.
func TestQuotesAdvisedAccountAnchored(t *testing.T) {
	for _, in := range []string{
		"Unknown column 'x' akun MySQL 'NIK3201234567890001'@'budi.santoso' y' in 'where clause'",
		"Unknown column 'x' GRANT SHOW DATABASES ON *.* TO 'Budi_Santoso'@'1985.03.12' y' in 'field list'",
		"Variable 'sql_mode' can't be set to the value of 'x akun MySQL 'Budi_Santoso'@'budi.santoso' y'",
		"Deadlock found when trying to get lock; 'x' akun MySQL 'Budi_Santoso'@'rs.lokal'",
		"xtidak bisa dipastikan lengkap: akun MySQL 'Budi_Santoso'@'rs.lokal'",
		"édi server ini: GRANT SHOW DATABASES ON *.* TO 'Budi_Santoso'@'rs.lokal'",
	} {
		got := Quotes(in)
		for _, leak := range []string{"Budi_Santoso", "NIK3201234567890001", "budi.santoso", "1985.03.12"} {
			if strings.Contains(got, leak) {
				t.Errorf("Quotes(%q) = %q keeps %q", in, got, leak)
			}
		}
	}
	// The agent's texts still pass whole (TestQuotesKeepsPanelPhrases).
}
