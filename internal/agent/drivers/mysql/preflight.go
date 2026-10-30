package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

const (
	roleMaster  = "master"
	roleReplica = "replica"

	// reachTimeout bounds the replica's TCP connect to the master.
	reachTimeout = 5 * time.Second
	// maxListed bounds the tables named in the non_innodb detail.
	maxListed = 20
)

var (
	// dbNameRe and userNameRe are the database and user names the panel
	// handles; anything else is refused before it can reach SQL.
	dbNameRe   = regexp.MustCompile(`^[A-Za-z0-9_$]{1,64}$`)
	userNameRe = regexp.MustCompile(`^[A-Za-z0-9_$]{1,32}$`)
)

func (d *Driver) registerPreflight(reg *commands.Registry) {
	reg.Register("mysql.preflight", func(ctx context.Context, params json.RawMessage) (any, error) {
		var p proto.PreflightParams
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, &commands.ParamError{Msg: "parameter tidak valid: " + err.Error()}
		}
		if err := validatePreflight(&p); err != nil {
			return nil, err
		}
		h, err := d.host()
		if err != nil {
			return nil, err
		}
		return Preflight(ctx, h, p)
	})
}

// validatePreflight checks p and trims its free-text fields.
func validatePreflight(p *proto.PreflightParams) error {
	p.Role, p.PeerHost, p.PeerVersion = strings.TrimSpace(p.Role), strings.TrimSpace(p.PeerHost), strings.TrimSpace(p.PeerVersion)
	p.PeerServerUUID = strings.TrimSpace(p.PeerServerUUID)
	switch p.Role {
	case roleMaster:
	case roleReplica:
		switch {
		case p.PeerHost == "":
			return &commands.ParamError{Msg: "alamat master wajib diisi"}
		case p.PeerPort < 1 || p.PeerPort > 65535:
			return &commands.ParamError{Msg: "port master harus antara 1 dan 65535"}
		case p.PeerVersion == "":
			return &commands.ParamError{Msg: "versi MySQL master wajib diisi"}
		}
	default:
		return &commands.ParamError{Msg: "peran harus master atau replica"}
	}
	for _, db := range p.Databases {
		if !dbNameRe.MatchString(db) {
			return &commands.ParamError{Msg: fmt.Sprintf("nama database tidak valid: %q", db)}
		}
	}
	if p.ReplUser != "" && !userNameRe.MatchString(p.ReplUser) {
		return &commands.ParamError{Msg: fmt.Sprintf("nama user replikasi tidak valid: %q", p.ReplUser)}
	}
	return nil
}

// Preflight checks whether this server can take p.Role. It logs in with the
// stored admin account (Host.Open) and reads the server's state itself.
func Preflight(ctx context.Context, h Host, p proto.PreflightParams) (proto.PreflightResult, error) {
	db, err := openAdmin(ctx, h)
	if err != nil {
		return proto.PreflightResult{}, err
	}
	defer db.Close()
	f, err := gatherPreflight(ctx, h, db, p)
	if err != nil {
		return proto.PreflightResult{}, err
	}
	return evaluatePreflight(p, f), nil
}

// preflightFacts is what preflight reads from the server and the host.
// Gathering stops after an unsupported version (only Status.Version is set)
// or missing grants (Status, Account and MissingGrants are set).
type preflightFacts struct {
	Status         proto.MySQLStatus
	VersionComment string   // @@version_comment, which names forks such as Percona Server
	Account        string   // CURRENT_USER()
	MissingGrants  []string // GRANT statements the account lacks
	Databases      []proto.DatabaseInfo

	// Master.
	SkipNetworking bool
	ClonePlugin    string // PLUGIN_STATUS of the clone plugin; "" when not installed
	ReplUserExists bool
	NonInnoDB      []string // "schema.table (ENGINE)" in the selected databases, at most maxListed
	NonInnoDBCount int

	// Replica.
	Unreachable error // TCPReachable's error for the master
	DiskPath    string
	DiskFree    uint64
	DiskErr     error
}

func gatherPreflight(ctx context.Context, h Host, db *sql.DB, p proto.PreflightParams) (preflightFacts, error) {
	var f preflightFacts
	// The version alone first: on MariaDB or an old MySQL the queries in
	// Status may fail, and the answer must be the version check.
	if err := db.QueryRowContext(ctx, "SELECT @@version, @@version_comment").Scan(&f.Status.Version, &f.VersionComment); err != nil {
		return f, fmt.Errorf("baca versi MySQL: %w", err)
	}
	if _, err := supportedVersion(f.Status.Version, f.VersionComment); err != nil {
		return f, nil
	}

	// An account without enough privileges reads a partial status; the
	// grants check reports that before any check could trust it.
	st, statusErr := Status(ctx, db)
	if statusErr != nil && !isPrivilegeError(statusErr) {
		return f, statusErr
	}
	f.Status = st
	required := masterGrants
	if p.Role == roleReplica {
		required = replicaGrants
	}
	if err := db.QueryRowContext(ctx, "SELECT CURRENT_USER()").Scan(&f.Account); err != nil {
		return f, fmt.Errorf("baca akun MySQL: %w", err)
	}
	grants, err := showGrants(ctx, db)
	if err != nil {
		return f, err
	}
	if f.MissingGrants = missingGrants(grants, f.Account, required); len(f.MissingGrants) > 0 {
		return f, nil
	}
	if statusErr != nil {
		return f, statusErr // refused although the grants look complete (roles, partial revokes)
	}
	if f.Databases, err = listDatabases(ctx, db); err != nil {
		return f, err
	}

	switch p.Role {
	case roleMaster:
		vars, err := nameValues(ctx, db, `SHOW GLOBAL VARIABLES WHERE Variable_name = 'skip_networking'`)
		if err != nil {
			return f, fmt.Errorf("baca variabel MySQL: %w", err)
		}
		f.SkipNetworking = on(vars["skip_networking"])
		err = db.QueryRowContext(ctx, `SELECT PLUGIN_STATUS FROM information_schema.plugins WHERE PLUGIN_NAME = 'clone'`).Scan(&f.ClonePlugin)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return f, fmt.Errorf("baca status plugin clone: %w", err)
		}
		if p.ReplUser != "" {
			var n int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM mysql.user WHERE user = ?", p.ReplUser).Scan(&n); err != nil {
				return f, fmt.Errorf("baca user replikasi: %w", err)
			}
			f.ReplUserExists = n > 0
		}
		if f.NonInnoDB, f.NonInnoDBCount, err = nonInnoDBTables(ctx, db, p.Databases); err != nil {
			return f, err
		}
	case roleReplica:
		f.Unreachable = h.TCPReachable(ctx, p.PeerHost, p.PeerPort, reachTimeout)
		if f.DiskPath = f.Status.DataDir; f.DiskPath == "" {
			f.DiskPath = h.Layout().DataDir
		}
		f.DiskFree, _, f.DiskErr = h.DiskFree(f.DiskPath)
	}
	return f, nil
}

// nonInnoDBTables lists base tables not stored in InnoDB in dbs (every user
// database when dbs is empty): the first maxListed and how many there are.
func nonInnoDBTables(ctx context.Context, db *sql.DB, dbs []string) ([]string, int, error) {
	q := `SELECT table_schema, table_name, engine FROM information_schema.tables
		WHERE table_type = 'BASE TABLE' AND engine <> 'InnoDB'
			AND table_schema NOT IN ('mysql', 'sys', 'information_schema', 'performance_schema')`
	var args []any
	if len(dbs) > 0 {
		q += " AND table_schema IN (?" + strings.Repeat(", ?", len(dbs)-1) + ")"
		for _, d := range dbs {
			args = append(args, d)
		}
	}
	rows, err := db.QueryContext(ctx, q+" ORDER BY table_schema, table_name", args...)
	if err != nil {
		return nil, 0, fmt.Errorf("baca engine tabel: %w", err)
	}
	defer rows.Close()
	var out []string
	n := 0
	for rows.Next() {
		var schema, table, engine string
		if err := rows.Scan(&schema, &table, &engine); err != nil {
			return nil, 0, fmt.Errorf("baca engine tabel: %w", err)
		}
		if n++; n <= maxListed {
			out = append(out, fmt.Sprintf("%s.%s (%s)", schema, table, engine))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("baca engine tabel: %w", err)
	}
	return out, n, nil
}

// checkList collects checks in order.
type checkList []proto.Check

func (l *checkList) add(id, status, title, detail string) *proto.Check {
	*l = append(*l, proto.Check{ID: id, Status: status, Title: title, Detail: detail})
	return &(*l)[len(*l)-1]
}

// evaluatePreflight turns facts into checks. An unsupported version stops
// after the version check and missing grants after the grants check: what
// follows would be read from an unsupported server or a partial status.
func evaluatePreflight(p proto.PreflightParams, f preflightFacts) proto.PreflightResult {
	res := proto.PreflightResult{Status: f.Status}
	var l checkList
	finish := func() proto.PreflightResult {
		res.Checks = []proto.Check(l)
		if res.Checks == nil {
			res.Checks = []proto.Check{}
		}
		res.Worst = worstStatus(res.Checks)
		return res
	}

	local, err := supportedVersion(f.Status.Version, f.VersionComment)
	if err != nil {
		running := f.Status.Version
		if f.VersionComment != "" {
			running += " (" + f.VersionComment + ")"
		}
		l.add("version", proto.CheckFail, "Versi MySQL", fmt.Sprintf("%v; server ini menjalankan %s", ErrUnsupported, running))
		return finish()
	}
	l.add("version", proto.CheckOK, "Versi MySQL", fmt.Sprintf("MySQL %s (seri %s)", f.Status.Version, local.Series))

	var v peerVersions
	if p.Role == roleReplica {
		v = replicaIdentityChecks(&l, p, f, local)
	}

	if len(f.MissingGrants) > 0 {
		c := l.add("grants", proto.CheckFail, "Hak akses akun admin", fmt.Sprintf(
			"akun %s belum punya semua hak yang dibutuhkan; jalankan perintah GRANT berikut sebagai root lalu periksa ulang", f.Account))
		c.FixSQL = f.MissingGrants
		return finish()
	}
	l.add("grants", proto.CheckOK, "Hak akses akun admin", "hak yang diperiksa lengkap untuk peran "+p.Role)

	switch p.Role {
	case roleMaster:
		masterChecks(&l, &res, p, f)
	case roleReplica:
		cloneOK := replicaChecks(&l, p, f, v)
		switch {
		case v.peerErr != nil || !v.sameSeries || v.self:
			// No copy method for a master this server cannot replicate from.
		case cloneOK && len(p.Databases) == 0 && len(f.Databases) == 0:
			res.Method = "clone"
		default:
			res.Method = "dump"
		}
	}
	return finish()
}

// supportedVersion is ParseVersion that also refuses forks recognisable only
// by @@version_comment: Percona Server, and MariaDB builds without
// "MariaDB" in @@version.
func supportedVersion(version, comment string) (Dialect, error) {
	if c := strings.ToLower(comment); strings.Contains(c, "percona") || strings.Contains(c, "mariadb") {
		return Dialect{}, ErrUnsupported
	}
	return ParseVersion(version)
}

// peerVersions is how this server's version and identity compare with the
// master's.
type peerVersions struct {
	local, peer Dialect
	peerErr     error // the master's version does not parse
	sameSeries  bool
	self        bool // the master is this very server
}

// replicaIdentityChecks adds same_series and self_peer. They come before
// grants: @@version and @@server_uuid are readable by any account.
func replicaIdentityChecks(l *checkList, p proto.PreflightParams, f preflightFacts, local Dialect) peerVersions {
	v := peerVersions{local: local}
	v.peer, v.peerErr = ParseVersion(p.PeerVersion)
	v.sameSeries = v.peerErr == nil && local.SameSeries(v.peer)
	const title = "Seri versi sama dengan master"
	switch {
	case p.PeerVersion == "":
		l.add("same_series", proto.CheckFail, title, "versi MySQL master tidak diketahui")
	case v.peerErr != nil:
		l.add("same_series", proto.CheckFail, title, fmt.Sprintf("versi MySQL master %s tidak didukung", p.PeerVersion))
	case !v.sameSeries:
		l.add("same_series", proto.CheckFail, title, fmt.Sprintf(
			"master menjalankan MySQL %s (seri %s), server ini %s (seri %s); keduanya harus satu seri",
			p.PeerVersion, v.peer.Series, f.Status.Version, local.Series))
	default:
		l.add("same_series", proto.CheckOK, title, "seri "+local.Series+" sama dengan master")
	}

	const selfTitle = "Server tujuan bukan master itu sendiri"
	v.self = p.PeerServerUUID != "" && strings.EqualFold(p.PeerServerUUID, f.Status.ServerUUID)
	switch {
	case v.self:
		l.add("self_peer", proto.CheckFail, selfTitle, "server tujuan sama dengan master (server_uuid identik); pilih server lain")
	case p.PeerServerUUID == "":
		l.add("self_peer", proto.CheckWarn, selfTitle, "server_uuid master tidak diketahui; tidak bisa dipastikan server ini bukan master")
	default:
		l.add("self_peer", proto.CheckOK, selfTitle, "server_uuid server ini berbeda dari master")
	}
	return v
}

func masterChecks(l *checkList, res *proto.PreflightResult, p proto.PreflightParams, f preflightFacts) {
	st := f.Status
	if st.ServerID == 0 {
		c := l.add("server_id", proto.CheckFail, "server_id", "server_id masih 0; replikasi butuh server_id unik (akan diatur otomatis)")
		c.FixSQL = []string{fmt.Sprintf("SET PERSIST server_id=%d", newServerID())}
	} else {
		l.add("server_id", proto.CheckOK, "server_id", fmt.Sprintf("server_id = %d", st.ServerID))
	}

	if st.LogBin {
		l.add("log_bin", proto.CheckOK, "Binary log", "binary log aktif")
	} else {
		c := l.add("log_bin", proto.CheckNeedsRestart, "Binary log", "binary log mati; perlu diaktifkan di konfigurasi lalu MySQL direstart")
		c.FixConfig = "log-bin=binlog"
	}

	if strings.EqualFold(st.BinlogFormat, "ROW") {
		l.add("binlog_format", proto.CheckOK, "Format binlog", "binlog_format = ROW")
	} else {
		c := l.add("binlog_format", proto.CheckFail, "Format binlog", fmt.Sprintf("binlog_format = %s; replikasi butuh ROW", st.BinlogFormat))
		c.FixSQL = []string{"SET PERSIST binlog_format='ROW'"}
	}

	gtidOn := strings.EqualFold(st.GTIDMode, "ON")
	if gtidOn {
		l.add("gtid_mode", proto.CheckOK, "Mode GTID", "gtid_mode = ON")
	} else {
		l.add("gtid_mode", proto.CheckWarn, "Mode GTID", fmt.Sprintf("gtid_mode = %s; akan dinaikkan bertahap tanpa restart", st.GTIDMode))
	}
	if st.EnforceGTID {
		l.add("enforce_gtid_consistency", proto.CheckOK, "Konsistensi GTID", "enforce_gtid_consistency = ON")
	} else {
		l.add("enforce_gtid_consistency", proto.CheckWarn, "Konsistensi GTID", "enforce_gtid_consistency belum ON; akan dinaikkan bertahap tanpa restart")
	}

	switch f.ClonePlugin {
	case "ACTIVE":
		l.add("clone_plugin", proto.CheckOK, "Plugin CLONE", "plugin clone aktif")
	case "":
		l.add("clone_plugin", proto.CheckWarn, "Plugin CLONE", "plugin clone belum terpasang; akan diaktifkan")
	default:
		l.add("clone_plugin", proto.CheckWarn, "Plugin CLONE", fmt.Sprintf("plugin clone berstatus %s; akan diaktifkan", f.ClonePlugin))
	}

	const bindTitle = "Alamat yang didengar MySQL"
	local := isLocalBind(st.BindAddress)
	switch {
	case f.SkipNetworking:
		c := l.add("bind_address", proto.CheckNeedsRestart, bindTitle,
			"MySQL tidak menerima koneksi TCP (skip-networking); perlu diubah di konfigurasi lalu MySQL direstart")
		c.FixConfig = "skip-networking=OFF"
		if local {
			c.FixConfig += "\nbind-address=0.0.0.0"
		}
	case local:
		c := l.add("bind_address", proto.CheckNeedsRestart, bindTitle, fmt.Sprintf(
			"MySQL hanya mendengar di %s sehingga replika tidak bisa terhubung; perlu bind-address=0.0.0.0 lalu MySQL direstart", st.BindAddress))
		c.FixConfig = "bind-address=0.0.0.0"
	default:
		where := st.BindAddress
		if where == "" || where == "*" {
			where = "semua alamat"
		}
		l.add("bind_address", proto.CheckOK, bindTitle, "MySQL mendengar di "+where)
	}

	if p.ReplUser != "" {
		if f.ReplUserExists {
			l.add("repl_user", proto.CheckOK, "User replikasi", fmt.Sprintf("user %s sudah ada", p.ReplUser))
		} else {
			l.add("repl_user", proto.CheckWarn, "User replikasi", fmt.Sprintf("user %s belum ada; akan dibuat", p.ReplUser))
		}
	}

	if f.NonInnoDBCount > 0 {
		detail := "tabel non-InnoDB tidak aman untuk CLONE dan GTID, ubah ke InnoDB dulu: " + strings.Join(f.NonInnoDB, ", ")
		if more := f.NonInnoDBCount - len(f.NonInnoDB); more > 0 {
			detail += fmt.Sprintf(" dan %d lainnya", more)
		}
		l.add("non_innodb", proto.CheckFail, "Tabel non-InnoDB", detail)
	} else {
		l.add("non_innodb", proto.CheckOK, "Tabel non-InnoDB", "semua tabel memakai InnoDB")
	}

	var found []string
	if st.BinlogDumpThreads > 0 {
		found = append(found, fmt.Sprintf("%d replika sedang membaca binlog server ini", st.BinlogDumpThreads))
	}
	if st.Replica != nil {
		found = append(found, "server ini adalah replika dari "+net.JoinHostPort(st.Replica.SourceHost, strconv.Itoa(st.Replica.SourcePort)))
	}
	// Raising gtid_mode breaks replication that does not use GTID, in
	// either direction.
	var breaks []string
	if st.BinlogDumpThreads > 0 && !gtidOn {
		breaks = append(breaks, "menaikkan gtid_mode akan memutus replika lama yang belum GTID")
	}
	if st.Replica != nil && !gtidOn {
		breaks = append(breaks, "server ini sudah menjadi replika tanpa GTID; menaikkan gtid_mode akan memutus replikasinya")
	}
	const replTitle = "Replikasi yang sudah ada"
	switch {
	case len(breaks) > 0:
		l.add("existing_replication", proto.CheckFail, replTitle, strings.Join(append(found, breaks...), "; "))
	case len(found) > 0:
		l.add("existing_replication", proto.CheckWarn, replTitle, strings.Join(found, "; ")+"; panel tidak mengelola replikasi ini")
	default:
		l.add("existing_replication", proto.CheckOK, replTitle, "tidak ada replikasi lain")
	}

	sizes := map[string]uint64{}
	for _, d := range f.Databases {
		sizes[d.Name] = d.Bytes
	}
	selected := p.Databases
	if len(selected) == 0 {
		for _, d := range f.Databases {
			selected = append(selected, d.Name)
		}
	}
	var missing []string
	for _, name := range selected {
		size, ok := sizes[name]
		if !ok {
			missing = append(missing, name)
		}
		res.DataBytes += size
	}
	if len(missing) > 0 {
		l.add("data_size", proto.CheckFail, "Ukuran data", "database tidak ditemukan di server ini: "+strings.Join(missing, ", "))
	} else {
		l.add("data_size", proto.CheckOK, "Ukuran data", fmt.Sprintf("%s di %d database", gigabytes(res.DataBytes), len(selected)))
	}
}

// replicaChecks adds the replica role's checks after grants and reports
// whether CLONE is possible as far as versions go.
func replicaChecks(l *checkList, p proto.PreflightParams, f preflightFacts, v peerVersions) (cloneOK bool) {
	addr := net.JoinHostPort(p.PeerHost, strconv.Itoa(p.PeerPort))
	if f.Unreachable != nil {
		l.add("reachable", proto.CheckFail, "Koneksi ke master", fmt.Sprintf(
			"tidak bisa menjangkau %s dari server ini; periksa firewall dan bind-address master", addr))
	} else {
		l.add("reachable", proto.CheckOK, "Koneksi ke master", addr+" bisa dijangkau dari server ini")
	}

	need := p.PeerDataBytes + p.PeerDataBytes/5 // × 1.2 without overflow
	switch {
	case f.DiskErr != nil:
		l.add("disk", proto.CheckFail, "Ruang disk", fmt.Sprintf("tidak bisa membaca ruang disk %s: %v", f.DiskPath, f.DiskErr))
	case f.DiskFree < need:
		l.add("disk", proto.CheckFail, "Ruang disk", fmt.Sprintf("ruang kosong di %s %s, butuh %s (1.2 × data master %s)",
			f.DiskPath, gigabytes(f.DiskFree), gigabytes(need), gigabytes(p.PeerDataBytes)))
	default:
		l.add("disk", proto.CheckOK, "Ruang disk", fmt.Sprintf("ruang kosong di %s %s, butuh %s", f.DiskPath, gigabytes(f.DiskFree), gigabytes(need)))
	}

	onMaster := map[string]bool{}
	for _, d := range p.PeerDatabases {
		onMaster[d] = true
	}
	var foreign, leftover []string
	for _, d := range f.Databases {
		if onMaster[d.Name] {
			leftover = append(leftover, d.Name)
		} else {
			foreign = append(foreign, d.Name)
		}
	}
	if len(foreign) > 0 {
		l.add("dedicated", proto.CheckFail, "Server replika khusus", fmt.Sprintf(
			"server ini masih berisi database lain (%s); versi ini hanya mendukung server replika khusus", strings.Join(foreign, ", ")))
	} else {
		l.add("dedicated", proto.CheckOK, "Server replika khusus", "tidak ada database lain di server ini")
	}
	const leftTitle = "Database bernama sama dengan di master"
	wipe := "database bernama sama dengan di master akan dibuang: " + strings.Join(leftover, ", ")
	switch {
	case v.self:
		// These are the master's own databases: never offer to drop them.
		l.add("leftover", proto.CheckFail, leftTitle, "server tujuan sama dengan master; databasenya tidak boleh dibuang")
	case len(leftover) == 0:
		l.add("leftover", proto.CheckOK, leftTitle, "tidak ada database bernama sama dengan di master")
	case p.ConfirmWipe:
		l.add("leftover", proto.CheckOK, leftTitle, wipe+" (sudah dikonfirmasi)")
	default:
		l.add("leftover", proto.CheckNeedsConfirm, leftTitle, wipe)
	}

	if n := f.Status.BinlogDumpThreads; n > 0 {
		l.add("not_master", proto.CheckFail, "Bukan sumber replikasi", fmt.Sprintf(
			"%d replika sedang membaca binlog server ini; server yang menjadi sumber tidak bisa dijadikan replika", n))
	} else {
		l.add("not_master", proto.CheckOK, "Bukan sumber replikasi", "tidak ada replika yang membaca binlog server ini")
	}

	// Before 8.0.37 (on either side) CLONE needs the exact same release.
	const cloneTitle = "Versi untuk CLONE"
	local, peer := v.local, v.peer
	samePatch := local.Major == peer.Major && local.Minor == peer.Minor && local.Patch == peer.Patch
	switch {
	case v.peerErr != nil:
		l.add("clone_patch", proto.CheckWarn, cloneTitle, "versi master tidak bisa dibaca")
	case !v.sameSeries:
		l.add("clone_patch", proto.CheckWarn, cloneTitle, "seri berbeda")
	case (local.CloneNeedsExactPatch() || peer.CloneNeedsExactPatch()) && !samePatch:
		l.add("clone_patch", proto.CheckWarn, cloneTitle, fmt.Sprintf(
			"CLONE di bawah 8.0.37 butuh versi yang persis sama (master %s, server ini %s); data akan disalin dengan dump",
			p.PeerVersion, f.Status.Version))
	default:
		l.add("clone_patch", proto.CheckOK, cloneTitle, "versi master dan server ini bisa memakai CLONE")
		return true
	}
	return false
}

// isLocalBind reports whether every address in bind_address (comma-separated
// since 8.0.13, each optionally "/namespace") is a loopback address.
func isLocalBind(bind string) bool {
	if strings.TrimSpace(bind) == "" {
		return false
	}
	for _, a := range strings.Split(bind, ",") {
		a, _, _ = strings.Cut(strings.TrimSpace(a), "/")
		if strings.EqualFold(a, "localhost") {
			continue
		}
		if ip := net.ParseIP(a); ip == nil || !ip.IsLoopback() {
			return false
		}
	}
	return true
}

// newServerID picks a server_id in [2, 4294967295].
func newServerID() uint32 { return rand.Uint32N(math.MaxUint32-1) + 2 }

var severity = map[string]int{
	proto.CheckOK: 0, proto.CheckWarn: 1, proto.CheckNeedsConfirm: 2, proto.CheckNeedsRestart: 3, proto.CheckFail: 4,
}

// worstStatus is the most severe status among checks: fail > needs_restart >
// needs_confirm > warn > ok.
func worstStatus(checks []proto.Check) string {
	worst := proto.CheckOK
	for _, c := range checks {
		if severity[c.Status] > severity[worst] {
			worst = c.Status
		}
	}
	return worst
}

func gigabytes(b uint64) string { return fmt.Sprintf("%.2f GB", float64(b)/(1<<30)) }
