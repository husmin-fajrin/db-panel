package mysql

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
// refusal or a kept lock's reason.
const appsOnlyLocked = "terkunci untuk aplikasi saja (read_only=ON, super_read_only=OFF; akun dengan hak SUPER atau CONNECTION_ADMIN masih bisa menulis)"
