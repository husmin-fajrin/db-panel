package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// MySQL's two read-only flags, measured on mysqld 8.0
// (readonly_mix_test.go): only both ON refuses every account; read_only ON
// with super_read_only OFF refuses ordinary accounts and lets SUPER or
// CONNECTION_ADMIN ones write; read_only OFF lets every account write,
// whatever super_read_only says (that mix only a restart produces). Every
// decision of the agent about whether a server takes writes, its own or
// another's, uses these two; read_only ON with super_read_only OFF is
// neither, and each place says what it does there.

// NoWrites: MySQL refuses writes from every account (both flags ON).
func NoWrites(readOnly, superReadOnly bool) bool { return readOnly && superReadOnly }

// AppsWrite: ordinary accounts can write (read_only OFF, whatever
// super_read_only says).
func AppsWrite(readOnly bool) bool { return !readOnly }

// writableFlags names, in a refusal, the flags of a server that is not
// NoWrites: "super_read_only=OFF" as before, or both for the mix, whose
// super_read_only is ON.
func writableFlags(st PeerState) string {
	if AppsWrite(st.ReadOnly) && st.SuperReadOnly {
		return "read_only=OFF, super_read_only=ON"
	}
	return "super_read_only=OFF"
}

// appsOnlyLocked describes read_only ON with super_read_only OFF in a
// refusal or a kept lock's reason: what was read and who can write.
const appsOnlyLocked = "terkunci untuk aplikasi saja (read_only=ON, super_read_only=OFF): aplikasi biasa tidak bisa menulis ke sana, " +
	"tetapi akun dengan hak SUPER atau CONNECTION_ADMIN bisa"

// relockThere is the way out named for a server read appsOnlyLocked: lock
// it fully with the panel's "Kunci kembali" on its replica page
// (mysql.setReadOnly on: both flags ON and stored), after a load running
// there has ended.
const relockThere = `kunci penuh server itu dulu dengan tombol "Kunci kembali" di halaman replikanya ` +
	`(bila server itu sedang memuat data, tunggu sampai selesai)`

// listHosts joins hosts for a text, at most n of them.
func listHosts(hosts []string, n int) string {
	if len(hosts) <= n {
		return strings.Join(hosts, ", ")
	}
	return strings.Join(hosts[:n], ", ") + fmt.Sprintf(", dan %d lainnya", len(hosts)-n)
}

// ownWrites is what this server (db) executed that set, another server's
// gtid_executed, lacks: GTID_SUBTRACT on this server's MySQL, as caughtUp
// and repoint compare sets. Non-empty means this server took writes of its
// own (or holds transactions of a third server) the other has not.
func ownWrites(ctx context.Context, db *sql.DB, set string) (string, error) {
	var own string
	if err := db.QueryRowContext(ctx, "SELECT GTID_SUBTRACT(@@global.gtid_executed, ?)", set).Scan(&own); err != nil {
		return "", fmt.Errorf("bandingkan set GTID: %w", err)
	}
	return strings.ReplaceAll(own, "\n", ""), nil
}

// peerOwnWrites is what set, another server's gtid_executed, holds that
// this server (db) lacks, computed on this server's MySQL.
func peerOwnWrites(ctx context.Context, db *sql.DB, set string) (string, error) {
	var own string
	if err := db.QueryRowContext(ctx, "SELECT GTID_SUBTRACT(?, @@global.gtid_executed)", set).Scan(&own); err != nil {
		return "", fmt.Errorf("bandingkan set GTID: %w", err)
	}
	return strings.ReplaceAll(own, "\n", ""), nil
}

// maxGTIDText bounds a GTID set quoted in a text.
const maxGTIDText = 200

// textNothingByHand ends every text about two servers that may hold
// different writes.
const textNothingByHand = "Jangan kunci, buka kunci, atau jalankan replikasi di kedua server dengan tangan; " +
	"hubungi administrator MySQL untuk mencocokkan data kedua server"

// divergedText says that this server and the old master key may hold
// different writes. own is what this server holds that the old master
// lacks ("" when the old master could not be read: unread says why);
// oldMayWrite adds that the old master may still take writes; done says
// what the agent did (or did not do).
func divergedText(key, own, unread string, oldMayWrite bool, done string) string {
	var head string
	if unread != "" {
		head = fmt.Sprintf("master lama %s tidak bisa dibaca (%s), jadi tidak bisa dipastikan apakah server ini memuat tulisan "+
			"yang tidak ada di sana", key, cutText(unread, maxPeerErr))
	} else {
		head = fmt.Sprintf("server ini memuat transaksi yang tidak ada di master lama %s (%s)", key, cutText(own, maxGTIDText))
		if oldMayWrite {
			head += ", dan master lama itu mungkin masih menerima tulis"
		}
	}
	return head + ": kedua server bisa berisi tulisan yang berbeda. " + done + " " + textNothingByHand
}

// candidateOwnText says that the candidate key, read at readAt, holds own,
// transactions this master lacks: it took writes of its own and holds the
// newer data, so this master must stay locked; the way out is the promote
// run again.
func candidateOwnText(key, readAt, own string) string {
	return fmt.Sprintf("kandidat %s (dibaca di %s) memuat transaksi yang tidak ada di master ini (%s): kandidat sudah "+
		"menerima tulis sendiri dan datanya lebih baru. Jangan buka kunci master ini; ulangi promote dari panel "+
		`(tombol "Ulangi" pada pekerjaan promote) supaya kandidat itu menjadi master; bila promote itu ditolak, `+
		"hubungi administrator MySQL", key, readAt, cutText(own, maxGTIDText))
}
