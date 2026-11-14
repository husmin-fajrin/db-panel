package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"regexp"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// dumpGrantAdvice is the agent's warning when the dump account got RELOAD
// (dumpGrantsHere): fixed text but for the MySQL version, ending with the
// one GRANT to run for the admin account. Only this shape is logged
// uncensored.
var dumpGrantAdvice = regexp.MustCompile(`^akun dump sementara mendapat RELOAD [^'"\\]*` +
	`Jalankan di master: GRANT FLUSH_TABLES ON \*\.\* TO '[^'"\\\n]*'@'[^'"\\\n]*' WITH GRANT OPTION$`)

// dumpUserPrefix names the temporary account a replica dumps the master
// with: dbpanel_dump_<8 hex>, one per dump (SEC-27). The replication
// account itself cannot read tables, reset the binary log or lock the
// master; this one can, for the few minutes of a dump, from the replica's
// address only.
const dumpUserPrefix = "dbpanel_dump_"

// earlierAgentJob reports whether an agent job of cmd that an earlier
// attempt of this step started on serverID is still running or already
// succeeded: runAgentJob then waits for it, or takes its result, instead of
// starting another. Whatever the step does before its copy (dropping
// leftover databases) must not run under it.
func (jr *jobRun) earlierAgentJob(ctx context.Context, serverID int64, cmd string) bool {
	if jr.st.AgentJobID == "" || jr.st.AgentServerID != serverID || jr.st.AgentCmd != cmd {
		return false
	}
	var s proto.JobStatus
	if err := jr.call(ctx, serverID, "job.status", proto.JobIDParams{JobID: jr.st.AgentJobID}, &s); err != nil {
		return false
	}
	return s.State == "running" || s.State == "succeeded"
}

// copySnapshot runs cmd (mysql.cloneFrom or mysql.dumpFrom) on the target
// with p, which names the master. A dump reads the master with a temporary
// account created for it and dropped when the copy ends, on every path;
// reattach is earlierAgentJob's answer, and the earlier attempt's account
// then stays until its dump ends. A clone reads with the replication
// account (BACKUP_ADMIN).
func (jr *jobRun) copySnapshot(ctx context.Context, cmd string, p proto.SnapshotParams, reattach bool, res *proto.SnapshotResult) error {
	if cmd != "mysql.dumpFrom" {
		jr.markCopySent()
		return jr.runAgentJob(ctx, jr.target(), cmd, p, res)
	}
	defer jr.dropDumpAccount(ctx)
	if reattach {
		p.User = jr.st.DumpUser
		jr.markCopySent()
		return jr.runAgentJob(ctx, jr.target(), cmd, p, res)
	}
	// One a cut-off attempt left behind.
	if err := jr.dropDumpAccount(ctx); errors.Is(err, proto.ErrOffline) {
		return fmt.Errorf("master %s tidak terhubung; hubungkan lagi agent-nya lalu Ulangi", jr.serverName(jr.master()))
	} else if err != nil {
		return errors.New("akun dump sementara dari percobaan sebelumnya belum bisa dihapus dari master")
	}
	srv, err := jr.r.d.Store.GetServer(jr.target())
	if err != nil {
		return err
	}
	host, err := dumpAccountHost(ctx, jr.r.sourceHost(srv))
	if err != nil {
		return err
	}
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return err
	}
	user := dumpUserPrefix + hex.EncodeToString(b[:])
	password, err := newReplPassword()
	if err != nil {
		return err
	}
	// Recorded before it is created: a call cut off (timeout, Cancel, panel
	// stop) or failing halfway may still leave it on the master, and the
	// defer, a retry or Cancel then drop it (DROP USER IF EXISTS).
	jr.st.DumpUser, jr.st.DumpHost = user, host
	jr.save()
	var created proto.DumpUserResult
	if err := jr.call(ctx, jr.master(), "mysql.createDumpUser",
		proto.DumpUserParams{User: user, Password: password, FromHost: host, RequireSSL: p.SSL}, &created); err != nil {
		return fmt.Errorf("buat akun dump sementara di master: %w", err)
	}
	jr.logf("info", "Akun dump sementara %s@%s dibuat di master; dihapus lagi setelah salinan selesai.", user, host)
	switch {
	case dumpGrantAdvice.MatchString(created.Warning):
		// The agent's own advice, naming the admin account (from
		// CURRENT_USER(), never row data) in a GRANT to run: kept whole.
		jr.logLine("warn", created.Warning)
	case created.Warning != "":
		jr.logf("warn", "%s", created.Warning) // any other shape: censored
	}
	p.User, p.Password = user, password
	jr.markCopySent()
	return jr.runAgentJob(ctx, jr.target(), cmd, p, res)
}

// lookupIP resolves a host name; tests replace it.
var lookupIP = net.DefaultResolver.LookupIP

// dumpAccountHost is the host part of a dump account for a replica at
// host: one IP address, which a pinned master insists on (no name or
// pattern). A name (the panel host's --public-host) is resolved to the
// address the panel sees for it, an IPv4 one first.
func dumpAccountHost(ctx context.Context, host string) (string, error) {
	if host == "" {
		return "", errors.New("alamat server tujuan belum diketahui")
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.String(), nil
	}
	ips, err := lookupIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return "", fmt.Errorf("alamat server tujuan %s tidak bisa diterjemahkan ke alamat IP untuk akun dump: %v", host, err)
	}
	for _, ip := range ips {
		if ip.To4() != nil {
			return ip.String(), nil
		}
	}
	return ips[0].String(), nil
}

// dumpAccountMaxAge is how old a dbpanel_dump_* account's password on the
// master must be before a new dump sweeps the account (spec 5.6): one that
// old was left by an attempt whose drop failed and that was abandoned.
// Dropping an account does not end a session already logged in with it.
const dumpAccountMaxAge = 24 * time.Hour

// sweepDumpAccounts drops leftover dump accounts on the master
// (mysql.sweepDumpUsers). It runs at the start of "Siapkan replika"
// (create_replica, resync_replica, whatever the copy method) and of
// add_databases' first step. Best effort: a failure is only logged.
func (jr *jobRun) sweepDumpAccounts(ctx context.Context) {
	var res proto.SweepDumpUsersResult
	if err := jr.call(ctx, jr.master(), "mysql.sweepDumpUsers",
		proto.SweepDumpUsersParams{OlderThanSec: int64(dumpAccountMaxAge / time.Second)}, &res); err != nil {
		jr.r.log.Warn("sweep leftover dump accounts", "job", jr.job.ID, "err", logErr(err))
		return
	}
	if len(res.Dropped) > 0 {
		jr.r.log.Info("leftover dump accounts dropped", "job", jr.job.ID, "accounts", res.Dropped)
	}
}

// dropDumpAccount drops the temporary dump account the job holds on the
// master, if any (best effort: a failure is logged, kept for the next
// attempt or Cancel, and returned). It runs whatever ctx's state, with its
// own bound.
func (jr *jobRun) dropDumpAccount(ctx context.Context) error {
	if jr.st.DumpUser == "" {
		return nil
	}
	dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelTimeout)
	defer cancel()
	user, host := jr.st.DumpUser, jr.st.DumpHost
	if err := jr.r.d.Hub.Call(dctx, jr.st.MasterServerID, "mysql.dropUser",
		proto.DropUserParams{User: user, FromHost: host}, nil); err != nil {
		jr.r.log.Warn("drop dump account", "job", jr.job.ID, "user", user, "err", logErr(err))
		if errors.Is(err, proto.ErrOffline) {
			// Most likely the account was never created: the master was
			// already unreachable for createDumpUser.
			jr.logf("warn", "Master %s tidak terhubung, jadi akun dump sementara %s@%s (mungkin belum sempat dibuat) "+
				"belum bisa dihapus. Dicoba lagi pada percobaan berikutnya; akun dump tertinggal yang lebih tua dari "+
				"24 jam juga disapu saat replika berikutnya dari master ini disiapkan.", jr.serverName(jr.st.MasterServerID), user, host)
			return err
		}
		jr.logf("warn", "Akun dump sementara %s@%s belum bisa dihapus dari master: %v. Dicoba lagi pada percobaan "+
			"berikutnya; bila pekerjaan ini ditinggalkan, hapus manual: DROP USER '%s'@'%s';", user, host, err, user, host)
		return err
	}
	jr.st.DumpUser, jr.st.DumpHost = "", ""
	jr.save()
	jr.logf("info", "Akun dump sementara %s@%s dihapus dari master.", user, host)
	return nil
}

// markCopySent records, before the copy command of a rebuild of an old
// master is sent, that it is being sent (CopySent, H-1d): a lost answer
// then counts as a copy that may have begun.
func (jr *jobRun) markCopySent() {
	if !jr.st.RebuildOldMaster || jr.st.CopySent {
		return
	}
	jr.st.CopySent = true
	jr.save()
}
