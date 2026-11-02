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
