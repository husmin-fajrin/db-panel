package orchestrator

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

const (
	replPasswordLen = 24 // MySQL takes at most 32 characters in SOURCE_PASSWORD
	replAlphabet    = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"
)

func prepareMasterStepNames() []string {
	return []string{"Periksa master", "Terapkan pengaturan", "Mulai ulang MySQL", "Verifikasi"}
}

// StartPrepareMaster starts the prepare_master job: check the server, make
// it a master (binary log, GTID, clone plugin, replication account) and
// verify. It refuses a server whose MySQL is not connected or whose admin
// account lacks grants.
func (r *Runner) StartPrepareMaster(ctx context.Context, serverID int64) (string, error) {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	srv, err := r.d.Store.GetServer(serverID)
	if err != nil {
		return "", err
	}
	if srv.Role == store.RoleReplica {
		return "", errors.New("server ini sudah menjadi replika; tidak bisa disiapkan sebagai master")
	}
	inv, err := r.d.Store.GetInventory(serverID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && !inv.Connected) {
		return "", errors.New("MySQL di server ini belum dihubungkan; jalankan Hubungkan MySQL dulu")
	}
	if err != nil {
		return "", err
	}
	if len(inv.MissingGrants) > 0 {
		return "", fmt.Errorf("akun MySQL admin kurang hak; jalankan GRANT berikut di server ini lalu Segarkan: %s",
			strings.Join(inv.MissingGrants, "; "))
	}
	if err := r.refuseBusy(serverID, "server ini"); err != nil {
		return "", err
	}
	return r.createJob(KindPrepareMaster, serverID, 0, jobState{}, prepareMasterStepNames())
}

func (jr *jobRun) prepareMasterSteps() []step {
	names := prepareMasterStepNames()
	return []step{
		{names[0], jr.masterPreflight},
		{names[1], jr.masterConfigure},
		{names[2], jr.masterRestart},
		{names[3], jr.masterVerify},
	}
}

func (jr *jobRun) masterPreflight(ctx context.Context, _ bool) (outcome, error) {
	var res proto.PreflightResult
	if err := jr.call(ctx, jr.job.ServerID, "mysql.preflight",
		proto.PreflightParams{Role: "master", ReplUser: replUser}, &res); err != nil {
		return outcome{}, err
	}
	if err := jr.r.storePreflight(jr.job.ServerID, res); err != nil {
		jr.r.log.Warn("store preflight", "job", jr.job.ID, "err", err)
	}
	jr.logChecks(res.Checks)
	if res.Worst == proto.CheckFail {
		return outcome{}, errors.New(failedChecks(res.Checks, proto.CheckFail))
	}
	jr.st.MasterPort = res.Status.Port
	jr.st.MasterTLS = res.Status.TLSAvailable
	jr.save()
	return done("MySQL %s; pemeriksaan terburuk: %s", res.Status.Version, res.Worst), nil
}

func (jr *jobRun) masterConfigure(ctx context.Context, _ bool) (outcome, error) {
	var res proto.ConfigureResult
	if err := jr.call(ctx, jr.job.ServerID, "mysql.configure",
		proto.ConfigureParams{Role: "master", EnableClone: true, GTID: true}, &res); err != nil {
		return outcome{}, err
	}
	for _, a := range res.Applied {
		jr.logf("info", "Diterapkan: %s", a)
		if a == "binlog_format=ROW" {
			jr.logf("warn", "Format binlog sekarang ROW untuk koneksi baru. Koneksi aplikasi yang sudah terbuka tetap memakai "+
				"format lama sampai tersambung ulang; restart aplikasi (atau MySQL) di jam sepi sebelum membuat replika.")
		}
	}
	if res.ConfigPath != "" {
		jr.logf("info", "Pengaturan ditulis ke %s (server_id %d).", res.ConfigPath, res.ServerID)
	}
	password, err := jr.ensureTopology()
	if err != nil {
		return outcome{}, err
	}
	// "%": the replicas' addresses are not known yet when the master is
	// prepared. The account is guarded by its 24-character random password
	// and holds only REPLICATION SLAVE and BACKUP_ADMIN (dumps read with a
	// temporary account per dump); it requires TLS when the master has it.
	if err := jr.call(ctx, jr.job.ServerID, "mysql.createReplUser",
		proto.ReplUserParams{User: replUser, Password: password, FromHost: "%", RequireSSL: jr.st.MasterTLS}, nil); err != nil {
		return outcome{}, err
	}
	if jr.st.MasterTLS {
		jr.logf("info", "User replikasi %s siap (hanya lewat koneksi TLS).", replUser)
	} else {
		jr.logf("warn", "User replikasi %s siap; master tidak menawarkan TLS, jadi replikasi berjalan tanpa enkripsi.", replUser)
	}
	jr.st.NeedsRestart, jr.st.RestartReasons = res.NeedsRestart, res.RestartReasons
	jr.save()
	if res.NeedsRestart {
		return done("perlu restart: %s", strings.Join(res.RestartReasons, "; ")), nil
	}
	return done("diterapkan tanpa restart"), nil
}

// ensureTopology returns the replication password of the master's topology,
// creating the topology with a new sealed password when it has none.
func (jr *jobRun) ensureTopology() (string, error) {
	r := jr.r
	topo, err := r.d.Store.GetTopologyByMaster(jr.job.ServerID)
	if err == nil {
		plain, err := r.d.Secrets.Open(topo.ReplPasswordEnc)
		if err != nil {
			return "", fmt.Errorf("buka password replikasi tersimpan: %w", err)
		}
		return string(plain), nil
	}
	if !errors.Is(err, store.ErrNotFound) {
		return "", err
	}
	password, err := newReplPassword()
	if err != nil {
		return "", err
	}
	enc, err := r.d.Secrets.Seal([]byte(password))
	if err != nil {
		return "", fmt.Errorf("segel password replikasi: %w", err)
	}
	srv, err := r.d.Store.GetServer(jr.job.ServerID)
	if err != nil {
		return "", err
	}
	port := jr.st.MasterPort
	if port == 0 {
		port = 3306
	}
	if _, err := r.d.Store.CreateTopology(srv.ID, replUser, enc, r.sourceHost(srv), port); err != nil {
		return "", err
	}
	jr.logf("info", "Topologi dibuat; password replikasi dibuat panel dan disimpan tersegel.")
	return password, nil
}

// newReplPassword is replPasswordLen random characters from replAlphabet.
func newReplPassword() (string, error) {
	b := make([]byte, replPasswordLen)
	n := big.NewInt(int64(len(replAlphabet)))
	for i := range b {
		k, err := rand.Int(rand.Reader, n)
		if err != nil {
			return "", err
		}
		b[i] = replAlphabet[k.Int64()]
	}
	return string(b), nil
}

// masterRestart restarts the master when the user approves ("Restart
// MySQL di master sekarang"). Without the approval (the first pass, or
// "Sudah saya restart — periksa lagi") it reads the values in effect: when
// the user restarted MySQL themselves they no longer need a restart and
// the step ends without restarting the production master again.
func (jr *jobRun) masterRestart(ctx context.Context, approved bool) (outcome, error) {
	if !jr.st.NeedsRestart {
		return skipped("tidak perlu restart"), nil
	}
	if !approved {
		var st proto.MySQLStatus
		if err := jr.call(ctx, jr.job.ServerID, "mysql.status", nil, &st); err != nil {
			return outcome{}, err
		}
		if pending := pendingRestart(st); len(pending) == 0 {
			jr.st.NeedsRestart = false
			jr.save()
			jr.logf("info", "MySQL di %s sudah memakai pengaturan baru (binary log aktif, menerima koneksi jaringan); tidak perlu restart dari panel.",
				jr.serverName(jr.job.ServerID))
			return done("sudah direstart di luar panel"), nil
		} else if jr.st.WaitReason == WaitRestart {
			jr.logf("warn", "MySQL belum direstart: %s.", strings.Join(pending, "; "))
		}
		jr.st.WaitReason = WaitRestart
		jr.save()
		return wait(jr.restartMessage()), nil
	}
	var res proto.RestartResult
	if err := jr.callWithin(ctx, restartTimeout, jr.job.ServerID, "mysql.restart", nil, &res); err != nil {
		return outcome{}, err
	}
	jr.logf("info", "MySQL dimulai ulang dalam %d ms.", res.TookMs)
	jr.st.NeedsRestart = false
	jr.save()
	return done("dimulai ulang dalam %d ms", res.TookMs), nil
}

// pendingRestart lists what the master still needs a restart for, read
// from the values in effect: the same reasons mysql.configure gives.
func pendingRestart(st proto.MySQLStatus) []string {
	var out []string
	if !st.LogBin {
		out = append(out, "binary log masih OFF")
	}
	if loopbackOnly(st.BindAddress) {
		out = append(out, "MySQL masih hanya mendengar di "+st.BindAddress)
	}
	return out
}

// loopbackOnly reports whether every address in bind_address is loopback.
func loopbackOnly(bind string) bool {
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

// restartMessage explains why the master must restart and what it costs.
func (jr *jobRun) restartMessage() string {
	var b strings.Builder
	fmt.Fprintf(&b, "MySQL di %s perlu dimulai ulang: %s. ", jr.serverName(jr.job.ServerID),
		strings.Join(jr.st.RestartReasons, "; "))
	b.WriteString("Aplikasi yang memakai database ini akan terputus beberapa detik; lakukan di jam sepi. " +
		"Bila Anda merestart MySQL sendiri nanti, klik Sudah saya restart — periksa lagi.")
	for _, reason := range jr.st.RestartReasons {
		if strings.Contains(strings.ToLower(reason), "bind-address") {
			port := jr.st.MasterPort
			if port == 0 {
				port = 3306
			}
			fmt.Fprintf(&b, " MySQL akan menerima koneksi dari semua alamat jaringan; pastikan firewall membatasi port %d.", port)
			break
		}
	}
	return b.String()
}

func (jr *jobRun) masterVerify(ctx context.Context, _ bool) (outcome, error) {
	r := jr.r
	var st proto.MySQLStatus
	if err := jr.call(ctx, jr.job.ServerID, "mysql.status", nil, &st); err != nil {
		return outcome{}, err
	}
	if p := masterProblems(st); p != "" {
		return outcome{}, errors.New("master belum siap: " + p)
	}
	srv, err := r.d.Store.GetServer(jr.job.ServerID)
	if err != nil {
		return outcome{}, err
	}
	topo, err := r.d.Store.GetTopologyByMaster(srv.ID)
	if err != nil {
		return outcome{}, err
	}
	// An address the user set on the master page is kept; the port is the
	// one MySQL listens on now.
	host := topo.SourceHost
	if host == "" {
		host = r.sourceHost(srv)
	}
	if err := r.d.Store.SetTopologySource(topo.ID, host, st.Port); err != nil {
		return outcome{}, err
	}
	if err := r.updateInventory(srv.ID, func(inv *store.Inventory) { inv.MasterReady = true }); err != nil {
		return outcome{}, err
	}
	if err := r.d.Store.SetServerRole(srv.ID, store.RoleMaster); err != nil {
		return outcome{}, err
	}
	jr.logf("info", "Master siap: binary log ON, GTID ON, format ROW; replika menghubungi %s:%d.", host, st.Port)
	return done("master siap"), nil
}

// masterProblems lists what keeps st from serving replicas, "" when nothing.
func masterProblems(st proto.MySQLStatus) string {
	var p []string
	if st.ServerID == 0 {
		p = append(p, "server_id 0")
	}
	if !st.LogBin {
		p = append(p, "log_bin OFF")
	}
	if st.GTIDMode != "ON" {
		p = append(p, "gtid_mode "+st.GTIDMode)
	}
	if !st.EnforceGTID {
		p = append(p, "enforce_gtid_consistency OFF")
	}
	if st.BinlogFormat != "ROW" {
		p = append(p, "binlog_format "+st.BinlogFormat)
	}
	return strings.Join(p, ", ")
}

// storePreflight keeps the latest preflight answer in the inventory.
func (r *Runner) storePreflight(serverID int64, res proto.PreflightResult) error {
	b, err := json.Marshal(res)
	if err != nil {
		return err
	}
	return r.updateInventory(serverID, func(inv *store.Inventory) { inv.PreflightJSON = string(b) })
}
