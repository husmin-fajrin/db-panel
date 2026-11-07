package monitor

// Explanation is the plain-language reading of a replication error.
type Explanation struct {
	Text, Advice string
	// Primary names the button to highlight: "resync" | "start" | "master"
	// (check the replication account on the master's page) | "".
	Primary string
}

const unknownCause = "Penyebab belum dikenali panel."

var (
	expDuplicate = Explanation{
		Text:    "Data ganda: ada baris yang sudah ada di replika. Biasanya karena ada yang menulis langsung ke replika.",
		Advice:  "Resync ulang",
		Primary: "resync",
	}
	expDiskFull = Explanation{
		Text:    "Disk replika penuh.",
		Advice:  "Bereskan disk, lalu Start replika",
		Primary: "start",
	}
	expAccount = Explanation{
		Text:    "Akun replikasi ditolak master, atau server_id/UUID bentrok.",
		Advice:  "Periksa akun replikasi di halaman master",
		Primary: "master",
	}
	expNoMaster = Explanation{
		Text:    "Master tidak bisa dihubungi dari replika.",
		Advice:  "Periksa jaringan/master",
		Primary: "master",
	}
)

// explanations is the single table of known errors; add a row to teach the
// panel a new one.
var explanations = map[int]Explanation{
	1062: expDuplicate,
	1032: {
		Text:    "Baris tidak ditemukan: isi replika sudah berbeda dari master.",
		Advice:  "Resync ulang",
		Primary: "resync",
	},
	1236: {
		Text:    "Binlog master yang dibutuhkan sudah tidak ada: replika terlalu lama putus.",
		Advice:  "Resync ulang",
		Primary: "resync",
	},
	1594: {
		Text:    "Relay log di replika rusak.",
		Advice:  "Start replika; bila gagal lagi, Resync ulang",
		Primary: "start",
	},
	1021: expDiskFull, // disk full
	1114: expDiskFull, // table full
	3:    expDiskFull, // error writing file
	1045: expAccount,  // access denied (IO)
	1593: expAccount,  // fatal replica error, e.g. server_id/UUID clash (IO)
	2003: expNoMaster, // cannot connect (IO)
	2013: expNoMaster, // connection lost (IO)
}

// statusExplanations read the statuses a promotion leaves behind (spec
// 5.4, 5.6). Primary "repoint" is "Arahkan sekarang", "rebuild" the old
// master's rebuild.
var statusExplanations = map[string]Explanation{
	StatusQuarantined: {
		Text: "Server ini master lama dari sebuah promote. Panel mengunci MySQL-nya (hanya-baca) agar aplikasi yang " +
			"belum dialihkan tidak menulis ke sana.",
		Advice: `Tekan "Periksa data tertinggal" untuk melihat transaksi yang belum sampai ke master baru, lalu ` +
			`"Bangun ulang sebagai replika" atau hapus server ini dari panel; kedua tombol itu baru aktif setelah ` +
			`"Sudah saya alihkan" di layar pengalihan aplikasi`,
		Primary: "rebuild",
	},
	StatusNeedsRepoint: {
		Text: "Setelah promote, replika ini masih mengikuti master lama. Panel mengarahkannya ke master baru sendiri " +
			"begitu agent-nya tersambung.",
		Advice:  `Tekan "Arahkan sekarang" untuk mengarahkannya ke master baru tanpa menunggu`,
		Primary: "repoint",
	},
	StatusNeedsRebuild: {
		Text: "Replika ini punya transaksi yang tidak ada di master baru, jadi tidak bisa sekadar diarahkan ulang. " +
			"Panel tidak membangunnya ulang sendiri.",
		Advice:  `Tekan "Resync ulang" untuk membangunnya ulang dari master baru`,
		Primary: "resync",
	},
}

// expQuarantinePending reads an old master whose quarantine is not
// delivered yet: nothing protects it, unlike a quarantined one.
var expQuarantinePending = Explanation{
	Text: "Server ini master lama dari sebuah promote, tetapi panel belum bisa mengarantinanya karena agent-nya belum " +
		"terjangkau. MySQL-nya belum dikunci: bila hidup, aplikasi yang belum dialihkan bisa menulis ke sana.",
	Advice: "Jangan hidupkan MySQL server ini sebelum agent-nya tersambung; panel mengirim karantina begitu agent " +
		"tersambung",
}

// ExplainStatus reads a status code that needs explaining by itself (the
// statuses a promotion leaves); false for any other code.
// quarantinePending (View.QuarantinePending) picks the explanation of an
// old master not yet quarantined.
func ExplainStatus(code string, quarantinePending bool) (Explanation, bool) {
	if code == StatusQuarantined && quarantinePending {
		return expQuarantinePending, true
	}
	e, ok := statusExplanations[code]
	return e, ok
}

// Explain translates the replica's last errors into plain language. The
// SQL thread's errno is looked up first, then the IO thread's.
func Explain(sqlErrno, ioErrno int) Explanation {
	for _, no := range []int{sqlErrno, ioErrno} {
		if e, ok := explanations[no]; ok && no != 0 {
			return e
		}
	}
	return Explanation{Text: unknownCause}
}
