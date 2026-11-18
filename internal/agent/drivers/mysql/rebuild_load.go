package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The rebuild of a quarantined old master loads the new master's data with
// read-only lifted (liftReadOnly), after a restart of MySQL. The quarantine
// guard puts read-only back on every restart it sees (guardQuarantined), so
// while such a load runs it defers instead. The mark lives in memory only:
// an agent that dies mid-load loses it, the next transition locks the
// server again, and the rebuild job fails and is retried.

// rebuildLoad is the driver's mark: active while a load from source runs.
type rebuildLoad struct {
	mu       sync.Mutex
	active   bool
	source   string // host:port the load copies from
	deferred bool   // the guard logged its deferral for this load
	// recheck: a load ended; the guard's next tick reads the flags again
	// whether or not it deferred during the load.
	recheck bool
	// loaded is the source (host:port) a snapshot job in this agent run
	// copied the data from successfully; settleRebuilt requires it.
	loaded string
	// copied is what that job replaced: the whole server (CLONE) or the
	// databases a dump load created (see loadMark).
	copied loadCopy
	// writableSince: since the last load began, the guard found a flag
	// off without a legitimate lift, or could not check the flags
	// (noteWritableAfterLoad); settleRebuilt then refuses (T7).
	writableSince bool
	// expectRefused: a load replaced the accounts (AdminReplaced), so the
	// guard's login is refused until it works again; such refusals (any
	// login error the server sends, serverRefusedLogin) are no round the
	// guard could not check.
	expectRefused bool
}

// loadCopy is what one successful snapshot job replaced on this server.
// A CLONE replaces the whole data directory. A dump only adds what it
// loads: before lists the non-system databases present when its load
// began (each of them keeps whatever tables the dump did not overwrite)
// and named the databases it was asked for (empty: every database). A
// dump of every database (full) also records source: the non-system
// databases the source listed before the load and after it, together
// (sourceSeen).
type loadCopy struct {
	whole  bool
	before []string
	named  []string
	full   bool
	source []string
}

// loadMark is the handle of one snapshot job on the driver's mark; nil
// marks nothing.
type loadMark struct {
	r      *rebuildLoad
	source string
	// listed: a dump recorded the databases present before its load
	// (before); a dump without that record never counts as a copy.
	listed bool
	before []string
	// seen: the source's non-system databases, every listing of a full
	// dump together (sourceSeen).
	seen []string
}

// rebuildMark is the mark for a snapshot from host:port: a handle only when
// the role pin is quarantined and host:port is the source pin (matched like
// Allow matches the rebuild commands), else nil.
func (d *Driver) rebuildMark(host string, port int) *loadMark {
	if d.pins == nil || !d.pins.quarantinedUnder(host, port) {
		return nil
	}
	return &loadMark{r: &d.rebuild, source: net.JoinHostPort(host, strconv.Itoa(port))}
}

// set marks the load in progress. A load that begins changing local data
// ends what is known of the one before it: its record (loaded, copied)
// no longer describes the server, and neither does the guard's
// writable-since-load record or the refused logins it expected.
func (m *loadMark) set() {
	if m == nil {
		return
	}
	m.r.mu.Lock()
	defer m.r.mu.Unlock()
	m.r.active, m.r.source, m.r.deferred = true, m.source, false
	m.r.writableSince, m.r.expectRefused = false, false
	m.r.loaded, m.r.copied = "", loadCopy{}
}

// clear ends the load's mark; clearing again is a no-op.
func (m *loadMark) clear() {
	if m == nil {
		return
	}
	m.r.mu.Lock()
	defer m.r.mu.Unlock()
	if m.r.source == m.source && m.r.active {
		m.r.active, m.r.source, m.r.deferred, m.r.recheck = false, "", false, true
	}
}

// listBefore records the non-system databases db holds now, right before
// a dump's load begins, from a listing proved complete
// (completeUserDatabases); nil m records nothing and reads nothing.
func (m *loadMark) listBefore(ctx context.Context, db *sql.DB) error {
	if m == nil {
		return nil
	}
	names, err := completeUserDatabases(ctx, db)
	if err != nil {
		return err
	}
	m.before, m.listed = names, true
	return nil
}

// sourceSeen adds names, a listing of the source's non-system databases
// taken by a full dump, to what the mark's load records as on the source;
// nil m records nothing.
func (m *loadMark) sourceSeen(names []string) {
	if m == nil {
		return
	}
	for _, name := range names {
		if !hasName(m.seen, name) {
			m.seen = append(m.seen, name)
		}
	}
	slices.Sort(m.seen)
}

// done records that the snapshot job copied the data from the mark's
// source (the job succeeded): whole for a CLONE, else a dump of named
// (empty: every database, with the source's databases sourceSeen
// recorded), which counts only once its load recorded the databases
// present before it (listBefore).
func (m *loadMark) done(whole bool, named []string) {
	if m == nil {
		return
	}
	m.r.mu.Lock()
	defer m.r.mu.Unlock()
	if !whole && !m.listed {
		return
	}
	m.r.loaded = m.source
	full := !whole && len(named) == 0
	m.r.copied = loadCopy{whole: whole, before: m.before, named: slices.Clone(named), full: full}
	if full {
		m.r.copied.source = slices.Clone(m.seen)
	}
}

// adminReplaced records that the load replaced this server's accounts
// (AdminReplaced): until the guard logs in again its refused logins are
// expected.
func (m *loadMark) adminReplaced() {
	if m == nil {
		return
	}
	m.r.mu.Lock()
	defer m.r.mu.Unlock()
	m.r.expectRefused = true
}

// noteWritableAfterLoad records that the server was writable, or could not
// be checked, since the last load began.
func (d *Driver) noteWritableAfterLoad() {
	d.rebuild.mu.Lock()
	defer d.rebuild.mu.Unlock()
	d.rebuild.writableSince = true
}

// writableAfterLoad reports noteWritableAfterLoad since the last load
// began.
func (d *Driver) writableAfterLoad() bool {
	d.rebuild.mu.Lock()
	defer d.rebuild.mu.Unlock()
	return d.rebuild.writableSince
}

// expectingRefusal reports whether a load replaced the accounts and the
// guard has not logged in since.
func (d *Driver) expectingRefusal() bool {
	d.rebuild.mu.Lock()
	defer d.rebuild.mu.Unlock()
	return d.rebuild.expectRefused
}

// loggedIn ends expectingRefusal: the guard logged in.
func (d *Driver) loggedIn() {
	d.rebuild.mu.Lock()
	defer d.rebuild.mu.Unlock()
	d.rebuild.expectRefused = false
}

// takeRecheck reports, once, that a load ended since the guard's last
// tick.
func (d *Driver) takeRecheck() bool {
	d.rebuild.mu.Lock()
	defer d.rebuild.mu.Unlock()
	r := d.rebuild.recheck
	d.rebuild.recheck = false
	return r
}

// loadedFrom reports whether a snapshot job in this agent run copied the
// data from host:port, and what it replaced.
func (d *Driver) loadedFrom(host string, port int) (loadCopy, bool) {
	d.rebuild.mu.Lock()
	defer d.rebuild.mu.Unlock()
	if d.rebuild.loaded == "" || d.rebuild.loaded != net.JoinHostPort(host, strconv.Itoa(port)) {
		return loadCopy{}, false
	}
	return d.rebuild.copied, true
}

// maxNamedLeftovers is how many databases the A6 refusal names in each of
// its lists.
const maxNamedLeftovers = 10

// maxNotReplacedBytes caps the whole A6 refusal: the panel's line limit
// (1024) less its label and the step's title. Lists are cut shorter to fit;
// the advice never is.
const maxNotReplacedBytes = 800

// errNotReplaced is the refusal to end a quarantine while databases that
// were not copied from the source remain. After a dump of every database
// (named false) they are split by the source's listing: absent ones the
// new master does not have (drop-database here), present ones it has (a
// new rebuild copies them again), T8a. After a dump of named databases
// (named true) the listing says nothing about them and every name is in
// absent, T8b.
type errNotReplaced struct {
	source          string
	absent, present []string
	named           bool
}

// The parts of T8a and T8b.
const (
	notReplacedHead    = "server ini masih punya database yang tidak disalin dari sumber %s; karantina tidak diakhiri."
	notReplacedAbsent  = " Yang tidak ada di master baru (%s): jalankan di server ini sudo dbpanel agent drop-database <nama> untuk tiap nama, lalu tekan \"Ulangi dari tahap ini\" pada pekerjaan di panel."
	notReplacedPresent = " Yang ada di master baru (%s): mulai lagi Bangun ulang sebagai replika dari panel; panel menghapus dan menyalinnya ulang."
	notReplacedNamed   = " Bila database itu tidak ada di master baru: jalankan di server ini sudo dbpanel agent drop-database <nama> untuk tiap nama, lalu tekan \"Ulangi dari tahap ini\" pada pekerjaan di panel. Bila ada di master baru: mulai lagi Bangun ulang sebagai replika dari panel; panel menghapus dan menyalinnya ulang. Database itu: %s."
)

func (e *errNotReplaced) Error() string {
	absent, present := e.absent, e.present
	if e.named {
		absent, present = slices.Sorted(slices.Values(slices.Concat(e.absent, e.present))), nil
	}
	ka, kp := min(len(absent), maxNamedLeftovers), min(len(present), maxNamedLeftovers)
	for {
		s := e.text(absent, present, ka, kp)
		if len(s) <= maxNotReplacedBytes || ka+kp == 0 {
			return s
		}
		if ka >= kp {
			ka--
		} else {
			kp--
		}
	}
}

// text is the refusal naming the first ka of absent and kp of present.
func (e *errNotReplaced) text(absent, present []string, ka, kp int) string {
	s := fmt.Sprintf(notReplacedHead, e.source)
	if e.named {
		return s + fmt.Sprintf(notReplacedNamed, shortList(absent, ka))
	}
	if len(absent) > 0 {
		s += fmt.Sprintf(notReplacedAbsent, shortList(absent, ka))
	}
	if len(present) > 0 {
		s += fmt.Sprintf(notReplacedPresent, shortList(present, kp))
	}
	return s
}

// shortList is the first k names, then ", …" when some are left out ("…"
// alone when none is shown).
func shortList(names []string, k int) string {
	list := strings.Join(names[:k], ", ")
	switch {
	case k == len(names):
		return list
	case k == 0:
		return "…"
	}
	return list + ", …"
}

// AdviceTexts are the agent's texts that tell the operator what to run or
// press (T7, T8a, T8b, T10, T11, T11b, T12, T13, T14, T15, T16, T17, and
// the settle's answer for a lift lock file it cannot open), each
// with its values filled with examples, as the panel shows them: the
// panel's tests check that its redaction leaves the advice whole.
func AdviceTexts() []string {
	const source, account = "10.0.0.5:3306", "'panel_admin'@'10.0.0.%'"
	return []string{
		errWritableSinceLoad.Error(),
		(&errNotReplaced{source: source, absent: []string{"arsip_lama", "uji"}, present: []string{"simrs"}}).Error(),
		(&errNotReplaced{source: source, absent: []string{"arsip_lama"}, named: true}).Error(),
		(&loadAccountWeakError{account: account}).Error(),
		errSecondListFailed.Error(),
		errSecondListFailedReplaced.Error(),
		ErrLiftHeldByCommand.Error(),
		errLiftLockUnopenable(DefaultLiftLock, errors.New("buat direktori kunci /var/lib/dbpanel/agent: mkdir /var/lib/dbpanel/agent: read-only file system")).Error(),
		dedicatedQuarantinedText,
		fmt.Sprintf(grantsQuarantinedText, "panel_admin@10.0.0.%"),
		fmt.Sprintf(connectWeakQuarantinedText, "panel_admin@10.0.0.%"),
		(&errListIncomplete{account: account}).Error(),
		(&errListIncomplete{unsupported: true}).Error(),
	}
}

// notCopied lists the server's non-system databases now (present) against
// what a dump load c copied: a database that was there before the load
// began, or, for a dump of named databases, one not among them, or, for a
// dump of every database, one the source did not list (onSource), was not
// copied from the source. Names compare without case, as
// lower_case_table_names may fold them. A CLONE leaves nothing.
func (c loadCopy) notCopied(present []string) []string {
	if c.whole {
		return nil
	}
	var out []string
	for _, name := range present {
		if hasName(c.before, name) || (len(c.named) > 0 && !hasName(c.named, name)) || (c.full && !c.onSource(name)) {
			out = append(out, name)
		}
	}
	slices.Sort(out)
	return out
}

// notReplaced is the A6 refusal for the databases left that load c did
// not copy from source: after a dump of every database split by whether
// the source listed them (onSource), after any other dump all as one list.
func notReplaced(source string, c loadCopy, left []string) *errNotReplaced {
	e := &errNotReplaced{source: source, named: !c.full}
	for _, name := range left {
		if c.full && c.onSource(name) {
			e.present = append(e.present, name)
		} else {
			e.absent = append(e.absent, name)
		}
	}
	return e
}

// onSource reports whether the source listed name (without case) before
// or after a full dump's load.
func (c loadCopy) onSource(name string) bool { return hasName(c.source, name) }

// hasName reports whether list holds name, compared without case.
func hasName(list []string, name string) bool {
	return slices.ContainsFunc(list, func(n string) bool { return strings.EqualFold(n, name) })
}

// errListIncomplete refuses to rely on a database listing that cannot be
// proved complete: information_schema shows an account only the databases
// it holds some privilege on. account is 'user'@'host' (quoteAccount);
// unsupported means the account's name itself is outside what the proof
// handles (proofAccount), and the text then names no account; cause, for
// the log, is why the proof failed.
type errListIncomplete struct {
	account     string
	unsupported bool
	cause       error
}

func (e *errListIncomplete) Error() string {
	if e.unsupported {
		return listUnsupportedText
	}
	return fmt.Sprintf(listIncompleteText, e.account)
}

// T16 and T17.
const (
	listIncompleteText = "daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL %s yang dipakai agent " +
		"tidak punya hak SHOW DATABASES. Jalankan di server ini: sudo dbpanel agent grant-admin, lalu tekan \"Ulangi " +
		"dari tahap ini\" pada pekerjaan di panel."
	listUnsupportedText = "daftar database di server ini tidak bisa dipastikan lengkap: nama akun MySQL yang dipakai agent " +
		"memuat karakter yang tidak didukung pemeriksaan ini (yang didukung: huruf, angka, garis bawah, titik, tanda " +
		"hubung, dan tanda dolar). Jalankan di server ini: sudo dbpanel agent reset-admin, lalu tekan \"Ulangi dari " +
		"tahap ini\" pada pekerjaan di panel."
)

func (e *errListIncomplete) Unwrap() error { return e.cause }

// completeUserDatabases is userDatabases on one connection of db, after
// proving on that same connection that the account sees every database
// (listingComplete); otherwise errListIncomplete.
func completeUserDatabases(ctx context.Context, db *sql.DB) ([]string, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("baca daftar database: %w", err)
	}
	defer conn.Close()
	var account string
	if err := conn.QueryRowContext(ctx, "SELECT CURRENT_USER()").Scan(&account); err != nil {
		return nil, fmt.Errorf("baca akun MySQL: %w", err)
	}
	user, host, ok := proofAccount(account)
	if !ok {
		return nil, &errListIncomplete{unsupported: true, cause: fmt.Errorf("account name %q not supported by the proof", account)}
	}
	if err := listingComplete(ctx, conn, user, host); err != nil {
		return nil, &errListIncomplete{account: quoteAccount(account), cause: err}
	}
	return userDatabases(ctx, conn)
}

// Characters an account's user and host may hold for the proof: none
// needs escaping in a quoted string or identifier under any sql_mode
// (NO_BACKSLASH_ESCAPES, ANSI_QUOTES), and none can make a SHOW GRANTS row
// look like another one.
var (
	proofUserRe = regexp.MustCompile(`^[A-Za-z0-9_.$-]+$`)
	proofHostRe = regexp.MustCompile(`^[A-Za-z0-9_.%:/-]+$`)
)

// proofAccount splits CURRENT_USER()'s user@host for the proof; ok is
// false for a name the proof does not handle safely.
func proofAccount(account string) (user, host string, ok bool) {
	i := strings.LastIndex(account, "@")
	if i < 0 {
		return "", "", false
	}
	user, host = account[:i], account[i+1:]
	return user, host, proofUserRe.MatchString(user) && proofHostRe.MatchString(host)
}

// listingComplete proves that user@host's listing of databases on conn is
// complete (plan evidence "Bukti daftar database yang lengkap"): the
// account holds SHOW DATABASES directly on *.*, not partially revoked; or
// it holds SELECT directly on *.* and partial revokes are off, so none can
// hide a database from it. The grants are read for the account by name:
// SHOW GRANTS FOR CURRENT_USER() would show an active role's privileges as
// the account's own. Anything else, a query error included, is no proof.
// user and host passed proofAccount.
func listingComplete(ctx context.Context, conn *sql.Conn, user, host string) error {
	grants, err := accountGrants(ctx, conn, user, host)
	if err != nil {
		return err
	}
	ok, err := listingProvable(ctx, conn, grants, user, host)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("neither SHOW DATABASES on *.* without a revoke, nor SELECT on *.* with partial_revokes off")
	}
	return nil
}

// accountGrants are the SHOW GRANTS rows of user@host, read by name (user
// and host passed proofAccount, so they need no escaping). They are read
// under the proof's own session settings (plan evidence M4): with
// sql_quote_show_create OFF the rows name the account without quotes, and
// sql_mode's ANSI_QUOTES changes the quotes, either of which would make
// listingProof miss a privilege the account holds. conn's previous values
// are put back afterwards, since conn may run more statements (grant-admin
// runs its GRANTs on it), also when ctx has ended by then (under
// proofRestoreTimeout). When they cannot be put back the read is an error and
// conn's connection is discarded, so that neither conn nor the pool it
// returns to runs anything under the proof's settings.
func accountGrants(ctx context.Context, conn *sql.Conn, user, host string) (grants []string, err error) {
	var quote int
	var mode string
	if err := conn.QueryRowContext(ctx, "SELECT @@SESSION.sql_quote_show_create, @@SESSION.sql_mode").Scan(&quote, &mode); err != nil {
		return nil, fmt.Errorf("SHOW GRANTS: read session settings: %w", err)
	}
	if _, err := conn.ExecContext(ctx, "SET SESSION sql_quote_show_create = ON, sql_mode = ''"); err != nil {
		return nil, fmt.Errorf("SHOW GRANTS: session settings: %w", err)
	}
	defer func() {
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), proofRestoreTimeout)
		defer cancel()
		_, rerr := conn.ExecContext(rctx, "SET SESSION sql_quote_show_create = ?, sql_mode = ?", quote, mode)
		if rerr == nil {
			return
		}
		// driver.ErrBadConn from Raw closes the connection instead of
		// returning it to the pool.
		conn.Raw(func(any) error { return driver.ErrBadConn })
		grants, err = nil, fmt.Errorf("SHOW GRANTS: restore session settings: %w", rerr)
	}()
	defer accountGrantsRead()
	rows, err := conn.QueryContext(ctx, "SHOW GRANTS FOR "+quoteString(user)+"@"+quoteString(host))
	if err != nil {
		return nil, fmt.Errorf("SHOW GRANTS: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var g string
		if err := rows.Scan(&g); err != nil {
			return nil, fmt.Errorf("SHOW GRANTS: %w", err)
		}
		grants = append(grants, g)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("SHOW GRANTS: %w", err)
	}
	return grants, nil
}

// proofRestoreTimeout bounds putting a session's settings back after the
// proof's SHOW GRANTS (accountGrants).
const proofRestoreTimeout = 10 * time.Second

// accountGrantsRead runs once the grant rows are read, before the session
// settings are put back; a test hook.
var accountGrantsRead = func() {}

// listingProvable is the proof's decision for user@host from its grants
// (accountGrants): SHOW DATABASES held on *.* and not revoked
// (listingProof), or SELECT on *.* while partial_revokes, read on conn, is
// off. The settle (listingComplete) and grant-admin (MissingAdminGrants)
// both decide here, so they never disagree. The error is a failed read
// only.
func listingProvable(ctx context.Context, conn *sql.Conn, grants []string, user, host string) (bool, error) {
	show, sel := listingProof(grants, user, host)
	if show {
		return true, nil
	}
	if !sel {
		return false, nil
	}
	var partial bool
	if err := conn.QueryRowContext(ctx, "SELECT @@GLOBAL.partial_revokes").Scan(&partial); err != nil {
		return false, fmt.Errorf("read partial_revokes: %w", err)
	}
	return !partial, nil
}

// revokeRow is a partial revoke row of SHOW GRANTS, e.g.
// "REVOKE SELECT, SHOW DATABASES ON `old`.* FROM `u`@`h`": privilege
// tokens only (grantPrivileges), then the account last.
var revokeRow = regexp.MustCompile(`^REVOKE ` + grantPrivileges + ` ON .+ FROM (\S+)$`)

// listingProof reads the SHOW GRANTS rows of user@host: show is SHOW
// DATABASES (or ALL) granted on *.* with no revoke row naming it; sel is
// SELECT (or ALL) granted on *.*. A row counts only when it parses with
// privilege tokens alone (globalGrant, revokeRow) and names user@host,
// quoted with backticks or (ANSI_QUOTES) double quotes. Other GRANT rows
// (role, column and database grants, other accounts) are ignored; a REVOKE
// row that does not parse or names another account proves nothing at all.
func listingProof(grants []string, user, host string) (show, sel bool) {
	self := func(a string) bool {
		return a == "`"+user+"`@`"+host+"`" || a == `"`+user+`"@"`+host+`"`
	}
	held, revoked := map[string]bool{}, false
	for _, g := range grants {
		g = strings.TrimSpace(g)
		if strings.HasPrefix(g, "REVOKE ") {
			m := revokeRow.FindStringSubmatch(g)
			if m == nil || !self(m[2]) {
				return false, false
			}
			for _, p := range strings.Split(m[1], ",") {
				switch strings.TrimSpace(p) {
				case "SHOW DATABASES", "ALL", "ALL PRIVILEGES":
					revoked = true
				}
			}
			continue
		}
		if m := globalGrant.FindStringSubmatch(g); m != nil && self(m[2]) {
			for _, p := range strings.Split(m[1], ",") {
				held[strings.TrimSpace(p)] = true
			}
		}
	}
	all := held["ALL"] || held["ALL PRIVILEGES"]
	return (all || held["SHOW DATABASES"]) && !revoked, all || held["SELECT"]
}

// rowsQuerier is a *sql.DB or one of its connections.
type rowsQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// userDatabases are db's databases outside systemDatabases, sorted.
func userDatabases(ctx context.Context, db rowsQuerier) ([]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT schema_name FROM information_schema.schemata")
	if err != nil {
		return nil, fmt.Errorf("baca daftar database: %w", err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("baca daftar database: %w", err)
		}
		if !systemDatabases[strings.ToLower(name)] {
			out = append(out, name)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("baca daftar database: %w", err)
	}
	slices.Sort(out)
	return out, nil
}

// deferringToRebuild reports whether the guard defers: a load is marked, its source
// is still the pinned source of a quarantined server. first is true once
// per load (the guard logs then).
func (d *Driver) deferringToRebuild() (source string, first, ok bool) {
	d.rebuild.mu.Lock()
	defer d.rebuild.mu.Unlock()
	if !d.markHonoured() {
		return "", false, false
	}
	first = !d.rebuild.deferred
	d.rebuild.deferred = true
	return d.rebuild.source, first, true
}

// rebuildMarked is deferringToRebuild's ok, without recording a deferral.
func (d *Driver) rebuildMarked() bool {
	d.rebuild.mu.Lock()
	defer d.rebuild.mu.Unlock()
	return d.markHonoured()
}

// markHonoured: a load is marked and its source is still the pinned
// source of a quarantined server. The caller holds d.rebuild.mu.
func (d *Driver) markHonoured() bool {
	if !d.rebuild.active {
		return false
	}
	host, ps, err := net.SplitHostPort(d.rebuild.source)
	if err != nil {
		return false
	}
	port, err := strconv.Atoi(ps)
	return err == nil && d.pins != nil && d.pins.quarantinedUnder(host, port)
}
