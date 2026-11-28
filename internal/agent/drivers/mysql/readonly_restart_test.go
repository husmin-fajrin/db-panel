package mysql_test

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent"
	"github.com/husmin-fajrin/db-panel/internal/agent/config"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// flagState is what a server says about read-only: the running read_only
// and super_read_only, and what performance_schema.persisted_variables
// holds for each ("" when nothing is persisted).
type flagState struct {
	RO, SRO                   bool
	PersistedRO, PersistedSRO string
}

func onOff(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func (s flagState) running() runFlags { return runFlags{RO: s.RO, SRO: s.SRO} }

func (s flagState) String() string {
	return "running " + s.running().String() + ", persisted " + orDash(s.PersistedRO) + "/" + orDash(s.PersistedSRO)
}

// runFlags is a running read_only/super_read_only pair.
type runFlags struct{ RO, SRO bool }

func (f runFlags) String() string { return onOff(f.RO) + "/" + onOff(f.SRO) }

var (
	bothOn  = runFlags{RO: true, SRO: true}
	bothOff = runFlags{}
	roOnly  = runFlags{RO: true}
)

// persistedFlag spells a persisted read_only or super_read_only value as
// ON or OFF, accepting "1" and "0" and any letter case as the agent's
// persistedReadOnlyOn does; "" (nothing persisted) and anything else are
// kept as they are.
func persistedFlag(v string) string {
	switch {
	case strings.EqualFold(v, "ON") || v == "1":
		return "ON"
	case strings.EqualFold(v, "OFF") || v == "0":
		return "OFF"
	}
	return v
}

// readFlags reads inst's flags on a connection of its own (one made
// before a restart is gone); the persisted values are spelled by
// persistedFlag.
func readFlags(t *testing.T, inst *testmysql.Instance) flagState {
	t.Helper()
	db, err := sql.Open("mysql", inst.DSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var s flagState
	if err := db.QueryRow("SELECT @@global.read_only, @@global.super_read_only").Scan(&s.RO, &s.SRO); err != nil {
		t.Fatal(err)
	}
	for name, v := range map[string]*string{"read_only": &s.PersistedRO, "super_read_only": &s.PersistedSRO} {
		err := db.QueryRow("SELECT VARIABLE_VALUE FROM performance_schema.persisted_variables WHERE VARIABLE_NAME = ?", name).Scan(v)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			t.Fatal(err)
		}
		*v = persistedFlag(*v)
	}
	return s
}

// withDB runs f on a pool of inst that is closed before it returns, so no
// pool outlives a restart of inst.
func withDB(t *testing.T, inst *testmysql.Instance, f func(db *sql.DB)) {
	t.Helper()
	db, err := sql.Open("mysql", inst.DSN())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	f(db)
}

// execOn runs qs on inst, in order.
func execOn(t *testing.T, inst *testmysql.Instance, qs ...string) {
	t.Helper()
	withDB(t, inst, func(db *sql.DB) { execAll(t, db, qs...) })
}

// waitNoBinlogDump waits until no replica reads inst's binary log.
func waitNoBinlogDump(t *testing.T, inst *testmysql.Instance) {
	t.Helper()
	withDB(t, inst, func(db *sql.DB) {
		deadline := time.Now().Add(time.Minute)
		for count(t, db, "SELECT COUNT(*) FROM information_schema.processlist WHERE COMMAND IN ('Binlog Dump GTID', 'Binlog Dump')") > 0 {
			if time.Now().After(deadline) {
				t.Fatal("a replica still reads the binary log after a minute")
			}
			time.Sleep(200 * time.Millisecond)
		}
	})
}

// restartAndRead restarts inst and reads its flags before anything else
// runs on it (no guard round).
func restartAndRead(t *testing.T, inst *testmysql.Instance) flagState {
	t.Helper()
	inst.Restart(t)
	return readFlags(t, inst)
}

// logged reads inst's flags after a step and logs them under what.
func logged(t *testing.T, inst *testmysql.Instance, what string) flagState {
	t.Helper()
	s := readFlags(t, inst)
	t.Logf("after %s: %s", what, s)
	return s
}

// restarted restarts inst and logs its flags.
func restarted(t *testing.T, inst *testmysql.Instance) flagState {
	t.Helper()
	s := restartAndRead(t, inst)
	t.Logf("after restart: %s", s)
	return s
}

// logVersion logs the version of inst's mysqld.
func logVersion(t *testing.T, inst *testmysql.Instance) {
	t.Helper()
	withDB(t, inst, func(db *sql.DB) {
		var v string
		if err := db.QueryRow("SELECT VERSION()").Scan(&v); err != nil {
			t.Fatal(err)
		}
		t.Logf("mysqld %s", v)
	})
}

// writeOF writes read_only=ON and super_read_only=ON to inst's option
// file, as a replica's managed block holds them; the next start reads
// them.
func writeOF(t *testing.T, inst *testmysql.Instance) {
	t.Helper()
	f, err := os.OpenFile(inst.ConfigFile, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("read_only=ON\nsuper_read_only=ON\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("option file: read_only=ON, super_read_only=ON written")
}

// cliAgentDir makes dir an agent directory whose CLI reaches inst (root
// over its socket): agent.yaml names the socket.
func cliAgentDir(t *testing.T, dir string, inst *testmysql.Instance) {
	t.Helper()
	if err := config.Save(dir, config.Config{PanelAddr: "127.0.0.1:1", ServerID: 1, MySQLSocket: inst.Socket}); err != nil {
		t.Fatal(err)
	}
}

// runAgentCLI runs "dbpanel agent args..." on inst's layout and fails the
// test unless it exits 0.
func runAgentCLI(t *testing.T, inst *testmysql.Instance, args ...string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	seams := agent.CLISeams{
		LayoutHost: func(config.Config) (mysql.Host, error) { return inst.Host(), nil },
		Syslog:     func(string) error { return nil },
	}
	if code := agent.RunCLIWith(args, strings.NewReader(""), &stdout, &stderr, seams); code != 0 {
		t.Fatalf("agent %s = %d (%q, %q)", strings.Join(args, " "), code, stdout.String(), stderr.String())
	}
	t.Logf("agent %s: %s", args[0], strings.TrimSpace(stdout.String()+" "+stderr.String()))
}

// lockedMaster is a plain master pinned master, and a replica following
// it that can be its promote candidate.
type lockedMaster struct {
	master, cand *testmysql.Instance
	r            *pinRig
	dir          string
	cred         proto.PeerCred
}

func newLockedMaster(t *testing.T) *lockedMaster {
	t.Helper()
	master := testmysql.Start(t, testmysql.Options{})
	cand := testmysql.Start(t, testmysql.Options{})
	logVersion(t, master)
	replUser(t, master)
	replUser(t, cand)
	dir := pinDir(t, "master", "")
	m := &lockedMaster{master: master, cand: cand, r: newPinRig(t, master.Host(), dir), dir: dir,
		cred: proto.PeerCred{Host: "127.0.0.1", Port: cand.Port, User: "dbpanel_repl", Password: replPassword}}
	follow(t, master, cand)
	withDB(t, master, func(db *sql.DB) { waitBinlogDump(t, db) })
	logged(t, master, "start (plain master)")
	return m
}

// lock is the promote lock (mysql.lockForPromote) for the candidate.
func (m *lockedMaster) lock(t *testing.T) {
	t.Helper()
	decode[proto.LockForPromoteResult](t, m.r.dispatch("mysql.lockForPromote", proto.LockForPromoteParams{Candidate: m.cred, TimeoutSec: 600}))
	logged(t, m.master, "mysql.lockForPromote")
}

// unlock is mysql.unlockPromote, the candidate still a read-only replica.
func (m *lockedMaster) unlock(t *testing.T) {
	t.Helper()
	execOn(t, m.cand, "SET GLOBAL super_read_only = ON")
	if resp := m.r.dispatch("mysql.unlockPromote", proto.UnlockPromoteParams{Candidate: m.cred}); !resp.OK {
		t.Fatalf("mysql.unlockPromote = %+v", resp.Error)
	}
	logged(t, m.master, "mysql.unlockPromote")
}

// cliMaster is a plain master pinned master whose agent CLI reaches it.
func cliMaster(t *testing.T) (*testmysql.Instance, string) {
	t.Helper()
	inst := testmysql.Start(t, testmysql.Options{})
	logVersion(t, inst)
	dir := pinDir(t, "master", "")
	cliAgentDir(t, dir, inst)
	logged(t, inst, "start (plain master)")
	return inst, dir
}

// promotedMaster is a replica (option file and SetReadOnly(true) as a
// replica has them) promoted by a planned mysql.promote under a locked
// master; afterwards the old master follows it, a candidate for its own
// promote lock. The promoted server is r.replica, pinned master in r.dir.
func promotedMaster(t *testing.T, followBack bool) *promoteRig {
	t.Helper()
	r := newPromoteRig(t)
	logVersion(t, r.replica)
	r.d.SeenPath = filepath.Join(t.TempDir(), "replicas-seen")
	writeOF(t, r.replica)
	if err := mysql.SetReadOnly(context.Background(), r.replica.Host(), true); err != nil {
		t.Fatal(err)
	}
	logged(t, r.replica, "replica: SetReadOnly(true)")
	waitBinlogDump(t, r.mdb)
	mr := newPinRig(t, r.master.Host(), pinDir(t, "master", ""))
	lock := decode[proto.LockForPromoteResult](t, mr.dispatch("mysql.lockForPromote", proto.LockForPromoteParams{
		Candidate: proto.PeerCred{Host: "127.0.0.1", Port: r.replica.Port, User: "dbpanel_repl", Password: replPassword}, TimeoutSec: 600}))
	p := r.params(proto.PromotePlanned)
	p.LockDeadline = &lock.Deadline
	promoteResult(t, r.run(p))
	logged(t, r.replica, "mysql.promote (planned: the two write-opening statements)")
	if followBack {
		// The promoted replica's own Binlog Dump thread on the old master
		// ends first: configureReplication refuses a server replicas read.
		waitNoBinlogDump(t, r.master)
		follow(t, r.replica, r.master)
		waitBinlogDump(t, r.rdb)
	}
	// The rig's pool of the promoted server does not outlive its restarts.
	r.rdb.Close()
	return r
}

// oldMasterCred is the old master of r as the promoted master's
// candidate.
func oldMasterCred(r *promoteRig) proto.PeerCred {
	return proto.PeerCred{Host: "127.0.0.1", Port: r.master.Port, User: "dbpanel_repl", Password: replPassword}
}

// lockPromoted locks r's promoted master for its old master, restarts it
// while locked and reads it before any guard round, then runs one guard
// round (ensureLocked) and logs it. It returns the read before the guard.
func lockPromoted(t *testing.T, r *promoteRig) flagState {
	t.Helper()
	decode[proto.LockForPromoteResult](t, r.dispatch("mysql.lockForPromote", proto.LockForPromoteParams{Candidate: oldMasterCred(r), TimeoutSec: 600}))
	logged(t, r.replica, "mysql.lockForPromote")
	s := restarted(t, r.replica)
	if err := r.d.GuardTick(context.Background(), time.Now()); err != nil {
		t.Fatalf("GuardTick: %v", err)
	}
	logged(t, r.replica, "the guard's first round (ensureLocked)")
	return s
}

// quarantineThenMaster quarantines inst (pinned master in dir; d is the
// driver dispatch reaches) under a fake promoted new master holding its
// transactions
// (mysql.quarantine: persistReadOnly), makes it master again with
// "agent set-role master --ya" (which releases the start lock) and opens
// it with "agent unlock-master".
func quarantineThenMaster(t *testing.T, inst *testmysql.Instance, dir string, d *mysql.Driver,
	dispatch func(string, any) proto.Response) {
	t.Helper()
	peer := &fakePeer{}
	t.Cleanup(mysql.SetReadPeer(peer.read))
	var uuid string
	withDB(t, inst, func(db *sql.DB) {
		if err := db.QueryRow("SELECT @@global.server_uuid").Scan(&uuid); err != nil {
			t.Fatal(err)
		}
	})
	peer.set(mysql.PeerState{Reachable: true, GTIDExecuted: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee:1-4,\n" + uuid + ":1-9",
		ServerUUID: "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"})
	if err := mysql.RecordReplicasSeen(d.SeenPath, []string{guardCandidate.Host}, time.Now()); err != nil {
		t.Fatal(err)
	}
	res := decode[proto.QuarantineResult](t, dispatch("mysql.quarantine", quarantineParams))
	if !res.Applied {
		t.Fatalf("mysql.quarantine = %+v, want applied", res)
	}
	logged(t, inst, "mysql.quarantine (persistReadOnly; start locked "+strconv.FormatBool(res.StartLocked)+")")
	cliAgentDir(t, dir, inst)
	runAgentCLI(t, inst, "set-role", "master", "--ya", "--dir", dir)
	logged(t, inst, "agent set-role master")
	runAgentCLI(t, inst, "unlock-master", "--dir", dir)
	logged(t, inst, "agent unlock-master")
}

// premise is a premise row: OF in the option file or not, both flags
// persisted to v (in the order the chosen fix sends them), then a
// restart.
func premise(withOF bool, v string) func(t *testing.T) flagState {
	return func(t *testing.T) flagState {
		inst := testmysql.Start(t, testmysql.Options{})
		logVersion(t, inst)
		logged(t, inst, "start")
		if withOF {
			writeOF(t, inst)
		}
		execOn(t, inst, "SET PERSIST super_read_only = "+v, "SET PERSIST read_only = "+v)
		logged(t, inst, "SET PERSIST super_read_only = "+v+", SET PERSIST read_only = "+v)
		return restarted(t, inst)
	}
}

// TestReadOnlyAcrossRestart measures, on real mysqld instances, the
// running and persisted read_only/super_read_only after every step and
// after a MySQL restart, for each kind of master and each path that
// locks or opens one (R-188). want is the state each row must have after
// its last restart, running and persisted: every lock stores both flags
// ON, every unlock both OFF (Task 16). It also asserts the answers to the
// two questions of spec section 6.
func TestReadOnlyAcrossRestart(t *testing.T) {
	rows := []struct {
		name string
		run  func(t *testing.T) flagState // the row's steps; the state after its last restart
		want runFlags
	}{
		{name: "premis-1", run: premise(true, "OFF"), want: bothOff},
		{name: "premis-2", run: premise(false, "ON"), want: bothOn},
		{name: "premis-3", run: premise(true, "ON"), want: bothOn},
		{name: "premis-4", run: premise(false, "OFF"), want: bothOff},
		{name: "jenis1-kunci/lockForPromote", want: bothOn, run: func(t *testing.T) flagState {
			m := newLockedMaster(t)
			m.lock(t)
			return restarted(t, m.master)
		}},
		{name: "jenis1-kunci/lock-master", want: bothOn, run: func(t *testing.T) flagState {
			inst, dir := cliMaster(t)
			runAgentCLI(t, inst, "lock-master", "--dir", dir)
			logged(t, inst, "agent lock-master")
			return restarted(t, inst)
		}},
		{name: "jenis1-buka/unlockPromote", want: bothOff, run: func(t *testing.T) flagState {
			m := newLockedMaster(t)
			m.lock(t)
			restarted(t, m.master)
			m.unlock(t)
			return restarted(t, m.master)
		}},
		{name: "jenis1-buka/unlock-master", want: bothOff, run: func(t *testing.T) flagState {
			inst, dir := cliMaster(t)
			runAgentCLI(t, inst, "lock-master", "--dir", dir)
			logged(t, inst, "agent lock-master")
			restarted(t, inst)
			runAgentCLI(t, inst, "unlock-master", "--dir", dir)
			logged(t, inst, "agent unlock-master")
			return restarted(t, inst)
		}},
		{name: "jenis2-kunci", want: bothOn, run: func(t *testing.T) flagState {
			return lockPromoted(t, promotedMaster(t, true))
		}},
		{name: "jenis2-buka", want: bothOff,
			run: func(t *testing.T) flagState {
				r := promotedMaster(t, true)
				lockPromoted(t, r)
				if resp := r.dispatch("mysql.unlockPromote", proto.UnlockPromoteParams{Candidate: oldMasterCred(r)}); !resp.OK {
					t.Fatalf("mysql.unlockPromote = %+v", resp.Error)
				}
				logged(t, r.replica, "mysql.unlockPromote")
				return restarted(t, r.replica)
			}},
		{name: "jenis3-dari-1", want: bothOff,
			run: func(t *testing.T) flagState {
				inst := testmysql.Start(t, testmysql.Options{})
				logVersion(t, inst)
				dir := pinDir(t, "master", "")
				r := newPinRig(t, inst.Host(), dir)
				execOn(t, inst, "CREATE DATABASE toko")
				logged(t, inst, "start (plain master)")
				quarantineThenMaster(t, inst, dir, r.d, r.dispatch)
				return restarted(t, inst)
			}},
		{name: "jenis3-dari-2", want: bothOff,
			run: func(t *testing.T) flagState {
				r := promotedMaster(t, false)
				quarantineThenMaster(t, r.replica, r.dir, r.d, r.dispatch)
				return restarted(t, r.replica)
			}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			s := row.run(t)
			t.Logf("after the last restart: %s; required %s, persisted the same", s, row.want)
			if got := s.running(); got != row.want {
				t.Errorf("after the last restart: running %s, want %s", got, row.want)
			}
			if s.PersistedRO != onOff(row.want.RO) || s.PersistedSRO != onOff(row.want.SRO) {
				t.Errorf("after the last restart: persisted %s/%s, want %s", orDash(s.PersistedRO), orDash(s.PersistedSRO), row.want)
			}
		})
	}
	// The two questions of spec section 6 (R-188), as Task 15 measured
	// them: SET PERSIST super_read_only = ON turns read_only on but does
	// not persist it (why LockWrites sends both statements), and it stays
	// so across a restart.
	t.Run("question-persist-super_read_only-on", func(t *testing.T) {
		inst := testmysql.Start(t, testmysql.Options{})
		logVersion(t, inst)
		logged(t, inst, "start")
		execOn(t, inst, "SET PERSIST super_read_only = ON")
		s := logged(t, inst, "SET PERSIST super_read_only = ON")
		t.Logf("answer: persisted read_only after SET PERSIST super_read_only = ON is %s", orDash(s.PersistedRO))
		if s.running() != bothOn || s.PersistedRO != "" || s.PersistedSRO != "ON" {
			t.Errorf("after SET PERSIST super_read_only = ON: %s; want running ON/ON, persisted -/ON", s)
		}
		if s := restarted(t, inst); s.running() != bothOn || s.PersistedRO != "" || s.PersistedSRO != "ON" {
			t.Errorf("after a restart: %s; want running ON/ON, persisted -/ON", s)
		}
	})
	t.Run("question-setReadOnly-off-with-only-read_only-on", func(t *testing.T) {
		inst := testmysql.Start(t, testmysql.Options{})
		logVersion(t, inst)
		r := newPinRig(t, inst.Host(), pinDir(t, "master", ""))
		execOn(t, inst, "SET PERSIST read_only = ON")
		if s := logged(t, inst, "SET PERSIST read_only = ON"); s.running() != roOnly || s.PersistedRO != "ON" || s.PersistedSRO != "" {
			t.Fatalf("before mysql.setReadOnly{on:false}: %s; want running ON/OFF, persisted ON/-", s)
		}
		resp := r.dispatch("mysql.setReadOnly", proto.ReadOnlyParams{On: false})
		if !resp.OK {
			t.Fatalf("answer: mysql.setReadOnly{on:false} refused: %+v", resp.Error)
		}
		t.Logf("answer: mysql.setReadOnly{on:false} accepted")
		for what, s := range map[string]flagState{"after mysql.setReadOnly{on:false}": logged(t, inst, "mysql.setReadOnly{on:false}"),
			"after a restart": restarted(t, inst)} {
			if s.running() != bothOff || s.PersistedRO != "OFF" || s.PersistedSRO != "OFF" {
				t.Errorf("%s: %s; want running OFF/OFF, persisted OFF/OFF", what, s)
			}
		}
	})
}
