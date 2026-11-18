package redact

import (
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
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
	for _, text := range mysql.AdviceTexts() {
		ins = append(ins, label+text)
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
	// Two censored literals side by side (also the account above, no longer
	// anchored since N-H5).
	`'x'"Budi"`,
	`'x'"Budi`,
	`Unknown column 'x'"Budi" y`,
	`"x"'Budi'"Budi"`,
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
		// N-H5: the agent's whole sentence, but not at the start of the
		// message nor right after the panel's label.
		"Unknown column 'x' daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL 'Budi_Santoso'@'rs.lokal' y",
		"Deadlock found; daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL 'Budi_Santoso'@'rs.lokal'",
		label + "Unknown column 'x' daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL 'Budi_Santoso'@'rs.lokal'",
		"xdaftar database di server ini tidak bisa dipastikan lengkap: akun MySQL 'Budi_Santoso'@'rs.lokal'",
		"(bukan dari panel)daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL 'Budi_Santoso'@'rs.lokal'",
		// The sentence without its start is not the agent's whole sentence.
		"tidak bisa dipastikan lengkap: akun MySQL 'Budi_Santoso'@'rs.lokal'",
		// The sentence's start without its own words after the account.
		"daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL 'Budi_Santoso'@'rs.lokal' tidak dikenal",
		"akun MySQL 'Budi_Santoso'@'rs.lokal' yang dipakai agent tidak punya hak SHOW DATABASES. ",
		label + "akun MySQL 'Budi_Santoso'@'rs.lokal' ditolak",
		"daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL 'Budi_Santoso'@'rs.lokal' yang dipakai " +
			"agent tidak punya hak CONNECTION_ADMIN maupun SUPER, ",
	} {
		got := Quotes(in)
		for _, leak := range []string{"Budi_Santoso", "NIK3201234567890001", "budi.santoso", "1985.03.12"} {
			if strings.Contains(got, leak) {
				t.Errorf("Quotes(%q) = %q keeps %q", in, got, leak)
			}
		}
	}
	// The agent's texts still pass whole (TestQuotesKeepsPanelPhrases,
	// TestQuotesKeepsNewAdvice), at the start of the message or after the
	// label.
	for _, prefix := range []string{"", label, "Salin data: " + label} {
		in := prefix + listingT16("'Budi_Santoso'@'rs.lokal'")
		if got := Quotes(in); got != in {
			t.Errorf("Quotes(%q) = %q, want it whole", in, got)
		}
	}
}

// label is the panel's label before an agent's words (orchestrator
// agentLabel), as a job's error carries it after the step title.
const label = "Pesan dari agent db-lama (bukan dari panel): "

// listingT16 is the agent's listing refusal now (T16) for account, the
// shape of mysql.AdviceTexts' text with another account.
func listingT16(account string) string {
	return "daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL " + account + " yang dipakai agent " +
		"tidak punya hak SHOW DATABASES. Jalankan di server ini: sudo dbpanel agent grant-admin, lalu tekan \"Ulangi " +
		"dari tahap ini\" pada pekerjaan di panel."
}

// TestQuotesKeepsNewAdvice (SEC-228): every advice text of the agent,
// read from its code (mysql.AdviceTexts), comes out of Quotes whole, bare
// and after the panel's label: the advised account and the closing dot
// included. (The orchestrator's TestAdviceWholeInFailLine checks the same
// on the job's real error line, cut at maxLogLine.)
func TestQuotesKeepsNewAdvice(t *testing.T) {
	texts := mysql.AdviceTexts()
	if len(texts) != 13 {
		t.Fatalf("AdviceTexts has %d texts, want 13 (T7, T8a, T8b, T10, T11, T11b, T12-T17, the unopenable lift lock)", len(texts))
	}
	for _, text := range texts {
		for _, prefix := range []string{"", label, "Sambungkan replikasi: " + label} {
			in := prefix + text
			if got := Quotes(in); got != in {
				t.Errorf("Quotes changed\n %q\nto\n %q", in, got)
			}
		}
	}
	accounts := 0
	for _, text := range texts {
		if strings.Contains(text, "'panel_admin'@'10.0.0.%'") {
			accounts++
		}
	}
	if accounts == 0 {
		t.Error("no advice text quotes the account: the anchor is not exercised")
	}
}

// TestQuotesKeepsOldAgentPhrases (spec 5.9, one release): an agent from
// before this release still quotes the two README section titles and
// advises GRANT SHOW DATABASES; its texts pass whole, bare and labelled.
func TestQuotesKeepsOldAgentPhrases(t *testing.T) {
	for _, phrase := range []string{"Database yang tidak ada di master baru",
		"Memberi hak SHOW DATABASES di server yang dikarantina"} {
		if !panelPhrase(phrase) {
			t.Errorf("%q is no longer a kept phrase", phrase)
		}
	}
	for _, text := range []string{textA6, listingText("'panel_admin'@'10.0.0.5'"), listingText("'dbpanel_admin'@'localhost'")} {
		for _, prefix := range []string{"", label, "Sambungkan replikasi: " + label} {
			if got := Quotes(prefix + text); got != prefix+text {
				t.Errorf("Quotes changed\n %q\nto\n %q", prefix+text, got)
			}
		}
	}
}

// TestQuotesSecretNextToAnchor: a secret-looking literal right next to a
// kept account, phrase or anchor is still censored.
func TestQuotesSecretNextToAnchor(t *testing.T) {
	const secret = "S3cret-Pw"
	for _, in := range []string{
		listingT16("'panel_admin'@'10.0.0.%'") + " GRANT ALL ON *.* TO 'panel_admin'@'10.0.0.%' IDENTIFIED BY '" + secret + "'",
		label + listingT16("'panel_admin'@'10.0.0.%'") + " IDENTIFIED BY '" + secret + "'",
		"daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL 'panel_admin'@'10.0.0.%' IDENTIFIED BY '" + secret + "'",
		"daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL '" + secret + "'",
		"daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL 'panel_admin'@'" + secret + " x'",
		"di server ini: GRANT SHOW DATABASES ON *.* TO 'panel_admin'@'10.0.0.5' IDENTIFIED BY '" + secret + "'",
		"di server ini: GRANT SHOW DATABASES ON *.* TO 'mysql://root:" + secret + "@10.0.0.5:3306/simrs'",
		label + "akun MySQL 'mysql://root:" + secret + "@10.0.0.5:3306/simrs'@'h.x'",
		"akun MySQL 'panel_admin'@'10.0.0.%' yang dipakai agent tidak punya hak CONNECTION_ADMIN maupun SUPER, " +
			"IDENTIFIED BY '" + secret + "'",

		`tekan "Ulangi dari tahap ini" 'mysql://root:` + secret + `@10.0.0.5:3306/simrs'`,
		`"Memberi hak SHOW DATABASES di server yang dikarantina" IDENTIFIED BY '` + secret + `'`,
		`"Database yang tidak ada di master baru"'` + secret + `'`,
		`jalankan "Hubungkan MySQL" IDENTIFIED BY '` + secret + `'`,
		`jalankan "Hubungkan MySQL"'mysql://root:` + secret + `@10.0.0.5/simrs'`,
		`jalankan "Hubungkan MySQL ` + secret + `"`,
	} {
		if got := Quotes(in); strings.Contains(got, secret) {
			t.Errorf("Quotes(%q) = %q keeps the secret", in, got)
		}
	}
}
