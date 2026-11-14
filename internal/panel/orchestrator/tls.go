package orchestrator

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// MySQL server certificates from the panel's MySQL CA (spec 5.9). The
// agent makes the key and a CSR (mysql.tlsRequest), the panel signs it for
// the names it knows itself (the server's address and, for a master, the
// address replicas use), and the agent installs it and has MySQL load it
// without a restart (mysql.tlsInstall); server_tls records it.
//
// The mysql_tls job ("Pasang sertifikat MySQL" on the server page, and the
// daily renewal) runs those three steps; "Siapkan master", "Buat replika"
// and "Resync ulang" run them as one step. A replica verifies its master's
// certificate (replication, dump, CLONE) when the master has one
// (server_tls). tighten_tls ("Perketat sambungan replikasi") gives a
// topology made before that its certificates and points every replica at
// its master again with verification.

// MySQLCA signs the MySQL server certificates; the panel passes its
// *ca.MySQLCA.
type MySQLCA interface {
	IssueMySQLCert(csrPEM []byte, serverID int64, hosts []string) (certPEM []byte, serial string, notAfter time.Time, err error)
	CAPEM() []byte
}

const (
	// tlsRenewBefore: a certificate expiring sooner is renewed by the
	// daily check.
	tlsRenewBefore = 30 * 24 * time.Hour

	stepInstallCert = "Pasang sertifikat MySQL"

	msgReplicaUnreachable = "replika tidak terjangkau; ulangi nanti"
	msgUnverified         = "Sambungan replikasi belum diverifikasi: master belum punya sertifikat panel"
)

func mysqlTLSStepNames() []string {
	return []string{"Minta kunci dan permintaan sertifikat", "Terbitkan sertifikat", "Pasang sertifikat di MySQL"}
}

// StartMySQLTLS starts the mysql_tls job: a new key and certificate for the
// server's MySQL. It refuses a server whose MySQL is not connected, whose
// admin account lacks grants (ALTER INSTANCE RELOAD TLS needs
// CONNECTION_ADMIN), or that has an unfinished job.
func (r *Runner) StartMySQLTLS(ctx context.Context, serverID int64) (string, error) {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	srv, err := r.d.Store.GetServer(serverID)
	if err != nil {
		return "", err
	}
	if srv.Role == store.RoleQuarantined {
		return "", errMySQLTLSQuarantined
	}
	if srv.Address == "" {
		return "", errors.New("server ini belum terhubung ke panel")
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
	return r.createJob(KindMySQLTLS, serverID, 0, jobState{}, mysqlTLSStepNames())
}

// errMySQLTLSQuarantined refuses a new certificate for a quarantined old
// master: its agent needs root's approval for it, and its rebuild installs
// one.
var errMySQLTLSQuarantined = errors.New("Server ini dikarantina (quarantined); sertifikat MySQL-nya dipasang lagi saat dibangun ulang sebagai replika.")

// RenewMySQLTLS is the daily check: it starts mysql_tls for every server
// whose certificate expires within tlsRenewBefore, whose agent is online,
// that has no unfinished job and is not quarantined, and returns the jobs
// it started. A server it skips is tried again the next day.
func (r *Runner) RenewMySQLTLS(ctx context.Context) []string {
	recs, err := r.d.Store.ListServerTLSExpiring(r.now().Add(tlsRenewBefore))
	if err != nil {
		r.log.Warn("list expiring mysql certificates", "err", err)
		return nil
	}
	var ids []string
	for _, rec := range recs {
		if ctx.Err() != nil {
			break
		}
		if srv, err := r.d.Store.GetServer(rec.ServerID); err == nil && srv.Role == store.RoleQuarantined {
			r.log.Info("mysql certificate renewal skipped: quarantined", "server_id", rec.ServerID, "not_after", rec.NotAfter)
			continue
		}
		if !r.d.Hub.IsOnline(rec.ServerID) {
			r.log.Info("mysql certificate renewal postponed: agent offline", "server_id", rec.ServerID, "not_after", rec.NotAfter)
			continue
		}
		if _, busy, err := r.d.Store.ActiveJobForServer(rec.ServerID); err != nil || busy {
			r.log.Info("mysql certificate renewal postponed: server busy", "server_id", rec.ServerID, "err", err)
			continue
		}
		id, err := r.StartMySQLTLS(ctx, rec.ServerID)
		if err != nil {
			r.log.Warn("mysql certificate renewal refused", "server_id", rec.ServerID, "err", logErr(err))
			continue
		}
		r.log.Info("mysql certificate renewal started", "server_id", rec.ServerID, "job", id, "not_after", rec.NotAfter)
		ids = append(ids, id)
	}
	return ids
}

// mysqlTLSSteps are the steps of mysql_tls for the job's server.
func (jr *jobRun) mysqlTLSSteps() []step {
	return jr.tlsSteps(jr.job.ServerID)
}

// tlsSteps request, issue and install serverID's certificate.
func (jr *jobRun) tlsSteps(serverID int64) []step {
	n := mysqlTLSStepNames()
	return []step{
		{n[0], func(ctx context.Context, _ bool) (outcome, error) { return jr.tlsRequest(ctx, serverID) }},
		{n[1], func(context.Context, bool) (outcome, error) { return jr.tlsIssue(serverID) }},
		{n[2], func(ctx context.Context, _ bool) (outcome, error) { return jr.tlsInstall(ctx, serverID) }},
	}
}

// installCert is one step that runs tlsSteps for serverID: "Pasang
// sertifikat MySQL" in Siapkan master and Buat replika, and the
// certificate steps of tighten_tls.
func (jr *jobRun) installCert(serverID int64) func(ctx context.Context, approved bool) (outcome, error) {
	return func(ctx context.Context, approved bool) (outcome, error) {
		var out outcome
		for _, s := range jr.tlsSteps(serverID) {
			o, err := s.fn(ctx, approved)
			if err != nil {
				return outcome{}, err
			}
			out = o
		}
		return out, nil
	}
}

// oldAgentError is a TLS command the server's agent does not know: it is
// from before the panel's MySQL certificates (Plan 4a). Its text tells the
// user what to do; it unwraps to a stepError, so it is shown as it is.
type oldAgentError struct{ stepError }

func (e oldAgentError) Unwrap() error { return e.stepError }

// tlsCall is jr.call for a TLS command: an agent that does not know cmd
// is marked as needing an update and the error says how to update it.
func (jr *jobRun) tlsCall(ctx context.Context, serverID int64, cmd string, params, result any) error {
	err := jr.call(ctx, serverID, cmd, params, result)
	if !errors.Is(err, &proto.Error{Code: proto.CodeUnknownCommand}) {
		return err
	}
	if jr.r.d.Alerts != nil {
		jr.r.d.Alerts.AgentNeedsUpgrade(serverID)
	}
	return oldAgentError{stepError(fmt.Sprintf("Agent di %s versi lama dan belum mengenal sertifikat MySQL. "+
		"Klik Pasang ulang agent di halaman server itu, jalankan perintahnya, lalu Ulangi.", jr.serverName(serverID)))}
}

// isOldAgent reports whether err is an oldAgentError.
func isOldAgent(err error) bool {
	var oe oldAgentError
	return errors.As(err, &oe)
}

// installCertOrSkip is installCert for a step that may go on without a
// certificate (Siapkan master, and a replica's own certificate when its
// master has none): an agent too old for it skips the step with a warning
// in the job log; the topology then has no certificate on that server,
// which the panel already handles ("Sambungan replikasi belum
// diverifikasi", Perketat).
func (jr *jobRun) installCertOrSkip(serverID int64, install func(ctx context.Context, approved bool) (outcome, error)) func(ctx context.Context, approved bool) (outcome, error) {
	return func(ctx context.Context, approved bool) (outcome, error) {
		out, err := install(ctx, approved)
		if err != nil && isOldAgent(err) {
			jr.logf("warn", "%s Sertifikat MySQL dilewati; replikasi ke server ini belum diverifikasi.", err.Error())
			return skipped("agent versi lama; pasang ulang agent lalu jalankan Pasang sertifikat MySQL"), nil
		}
		return out, err
	}
}

func (jr *jobRun) tlsRequest(ctx context.Context, serverID int64) (outcome, error) {
	if jr.r.d.MySQLCA == nil {
		return outcome{}, errors.New("penerbit sertifikat MySQL panel tidak tersedia")
	}
	var res proto.TLSRequestResult
	if err := jr.tlsCall(ctx, serverID, "mysql.tlsRequest", nil, &res); err != nil {
		return outcome{}, err
	}
	jr.st.TLSCSR, jr.st.TLSCert = res.CSRPEM, ""
	jr.save()
	return done("kunci baru dibuat di %s", jr.serverName(serverID)), nil
}

func (jr *jobRun) tlsIssue(serverID int64) (outcome, error) {
	if jr.r.d.MySQLCA == nil {
		return outcome{}, errors.New("penerbit sertifikat MySQL panel tidak tersedia")
	}
	if jr.st.TLSCSR == "" {
		return outcome{}, errors.New("permintaan sertifikat belum ada; mulai lagi dari tahap pertama")
	}
	hosts, err := jr.r.certHosts(serverID)
	if err != nil {
		return outcome{}, err
	}
	certPEM, serial, notAfter, err := jr.r.d.MySQLCA.IssueMySQLCert([]byte(jr.st.TLSCSR), serverID, hosts)
	if err != nil {
		return outcome{}, err
	}
	jr.st.TLSCert = string(certPEM)
	jr.save()
	jr.logf("info", "Sertifikat MySQL %s untuk %s diterbitkan (nomor seri %s, berlaku sampai %s).",
		jr.serverName(serverID), strings.Join(hosts, ", "), serial, notAfter.Format(time.DateOnly))
	return done("berlaku sampai %s", notAfter.Format(time.DateOnly)), nil
}

func (jr *jobRun) tlsInstall(ctx context.Context, serverID int64) (outcome, error) {
	if jr.st.TLSCert == "" {
		return outcome{}, errors.New("sertifikat belum diterbitkan; mulai lagi dari tahap pertama")
	}
	srv, err := jr.r.d.Store.GetServer(serverID)
	if err != nil {
		return outcome{}, err
	}
	var res proto.TLSInstallResult
	if err := jr.tlsCall(ctx, serverID, "mysql.tlsInstall", proto.TLSInstallParams{
		CertPEM: jr.st.TLSCert, CAPEM: string(jr.r.d.MySQLCA.CAPEM()), VerifyHost: srv.Address,
	}, &res); err != nil {
		return outcome{}, err
	}
	// SEC-63c: the serial the agent saw MySQL serve must be the one the
	// panel issued; server_tls records nothing else.
	if err := matchIssuedSerial(jr.st.TLSCert, res.Serial); err != nil {
		return outcome{}, err
	}
	if err := jr.r.d.Store.SetServerTLS(store.ServerTLS{ServerID: serverID, CertSerial: res.Serial, NotAfter: res.NotAfter,
		InstalledAt: jr.r.now(), Hosts: certNames(jr.st.TLSCert)}); err != nil {
		return outcome{}, err
	}
	jr.st.TLSCSR, jr.st.TLSCert = "", ""
	jr.save()
	jr.logf("info", "Sertifikat MySQL terpasang di %s dan langsung dipakai, tanpa restart MySQL.", srv.Name)
	return done("terpasang; berlaku sampai %s", res.NotAfter.Format(time.DateOnly)), nil
}

// certSerial is the serial of the PEM certificate certPEM as agents
// report it (32 hex digits); "" when it does not parse.
func certSerial(certPEM string) string {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return ""
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%032x", cert.SerialNumber)
}

// matchIssuedSerial refuses got, the serial an agent reports installed,
// unless it is the serial of certPEM, the certificate the panel issued
// (SEC-63c); an issued certificate that does not parse matches nothing.
func matchIssuedSerial(certPEM, got string) error {
	want := certSerial(certPEM)
	if want == "" {
		return stepError("sertifikat yang diterbitkan panel tidak terbaca; sertifikat tidak dicatat. Mulai lagi dari tahap pertama")
	}
	if got != want {
		return stepError(fmt.Sprintf("agent melaporkan sertifikat terpasang dengan nomor seri %s, bukan %s yang "+
			"diterbitkan panel; sertifikat tidak dicatat. Periksa server itu lalu Ulangi", got, want))
	}
	return nil
}

// verifiedLink reports whether rs shows the receiver connected (IO Yes,
// no error) with SOURCE_SSL_VERIFY_SERVER_CERT=1.
func verifiedLink(rs *proto.ReplicaStatus) bool {
	return rs != nil && rs.IORunning == "Yes" && rs.LastIOErrno == 0 && rs.SSLVerifyServerCert
}

// awaitVerifiedLink waits, up to verifyIOWait, until the replica on
// serverID reports its receiver connected with verification
// (verifiedLink), starting from rs, the status START REPLICA answered.
// tls_verified is set only on true (SEC-63c). A receiver connected
// without verification, or an agent that does not report it, is false at
// once: waiting does not change it. last is the status last seen.
func (jr *jobRun) awaitVerifiedLink(ctx context.Context, serverID int64, rs *proto.ReplicaStatus) (last *proto.ReplicaStatus, ok bool, err error) {
	// The deadline is on the runner's clock; the pause between two
	// polls is pollEvery (at most a second).
	deadline := jr.r.now().Add(jr.r.verifyIOWait)
	every := min(jr.r.pollEvery, time.Second)
	for {
		switch {
		case verifiedLink(rs):
			return rs, true, nil
		case rs != nil && rs.IORunning == "Yes" && rs.LastIOErrno == 0, ioRefused(rs):
			return rs, false, nil
		case !jr.r.now().Before(deadline):
			return rs, false, nil
		}
		pause := time.NewTimer(every)
		select {
		case <-ctx.Done():
			pause.Stop()
			return rs, false, ctx.Err()
		case <-pause.C:
		}
		var st proto.MySQLStatus
		if err := jr.call(ctx, serverID, "mysql.status", nil, &st); err != nil {
			return rs, false, err
		}
		rs = st.Replica
	}
}

// retryableIOErrnos are receiver errors MySQL keeps retrying (the agent's
// list in repoint.go): the master not reachable yet, the connection lost,
// a login refused.
var retryableIOErrnos = map[int]bool{1045: true, 2003: true, 2005: true, 2013: true}

// ioRefused reports whether rs shows a receiver error MySQL does not
// retry, such as 2026 (the master's certificate refused): waiting does
// not help (fix round 1).
func ioRefused(rs *proto.ReplicaStatus) bool {
	return rs != nil && rs.LastIOErrno != 0 && !retryableIOErrnos[rs.LastIOErrno]
}

// unverifiedText says why awaitVerifiedLink's last status is not a
// verified link.
func unverifiedText(rs *proto.ReplicaStatus) string {
	switch {
	case rs == nil:
		return "status replika tidak terbaca"
	case rs.IORunning == "Yes" && rs.LastIOErrno == 0:
		return "thread IO tersambung, tetapi agent tidak melaporkan SOURCE_SSL_VERIFY_SERVER_CERT=1 (agent versi lama?)"
	case rs.LastIOErrno != 0:
		return fmt.Sprintf("thread IO %s, error %d: %s", rs.IORunning, rs.LastIOErrno, truncate(rs.LastIOError, 200))
	default:
		return "thread IO " + rs.IORunning
	}
}

// certHosts are the names a server's certificate holds: its address and,
// for a master, the address replicas reach it at. Nothing an agent reports
// goes in.
func (r *Runner) certHosts(serverID int64) ([]string, error) {
	srv, err := r.d.Store.GetServer(serverID)
	if err != nil {
		return nil, err
	}
	if srv.Address == "" {
		return nil, errors.New("alamat server belum diketahui")
	}
	hosts := []string{srv.Address}
	topo, err := r.d.Store.GetTopologyByMaster(serverID)
	switch {
	case err == nil:
		if h := topo.SourceHost; h != "" && !slices.ContainsFunc(hosts, func(x string) bool { return strings.EqualFold(x, h) }) {
			hosts = append(hosts, h)
		}
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	}
	return hosts, nil
}

// certNames are the DNS and IP names of the certificate in certPEM, nil
// when it does not parse.
func certNames(certPEM string) []string {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil
	}
	out := slices.Clone(cert.DNSNames)
	for _, ip := range cert.IPAddresses {
		out = append(out, ip.String())
	}
	return out
}

// CertNamesHost reports whether host (a name or IP) is one of names, a
// MySQL certificate's names: IPs compare as addresses, names without case.
func CertNamesHost(names []string, host string) bool {
	ip := net.ParseIP(host)
	return slices.ContainsFunc(names, func(n string) bool {
		if nip := net.ParseIP(n); ip != nil || nip != nil {
			return ip != nil && nip != nil && ip.Equal(nip)
		}
		return strings.EqualFold(n, host)
	})
}

// checkMasterCertHost refuses a replica job on a master whose certificate
// (known names) does not hold the address replicas reach it at: the copy
// and replication verify that name and would fail mid-job (I8).
func (jr *jobRun) checkMasterCertHost(sourceHost string) error {
	rec, err := jr.r.d.Store.GetServerTLS(jr.master())
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if rec.Hosts == nil || CertNamesHost(rec.Hosts, sourceHost) {
		return nil
	}
	return stepError(fmt.Sprintf("Alamat master berubah ke %s, tetapi sertifikat MySQL master hanya memuat %s; "+
		"klik Perbarui sertifikat MySQL di halaman server master, lalu Ulangi.", sourceHost, strings.Join(rec.Hosts, ", ")))
}

// masterHasCert reports whether the master has a certificate from the
// panel (server_tls): replicas then verify it.
func (r *Runner) masterHasCert(masterID int64) (bool, error) {
	_, err := r.d.Store.GetServerTLS(masterID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// trustCA stores the panel's MySQL CA on serverID (mysql.tlsTrust, which
// may run any number of times) and returns the file's path there, for
// SSLCA.
func (jr *jobRun) trustCA(ctx context.Context, serverID int64) (string, error) {
	if jr.r.d.MySQLCA == nil {
		return "", errors.New("penerbit sertifikat MySQL panel tidak tersedia")
	}
	var res proto.TLSTrustResult
	if err := jr.tlsCall(ctx, serverID, "mysql.tlsTrust", proto.TLSTrustParams{CAPEM: string(jr.r.d.MySQLCA.CAPEM())}, &res); err != nil {
		return "", err
	}
	if res.CAPath == "" {
		return "", errors.New("agent tidak menyebut letak berkas CA MySQL")
	}
	return res.CAPath, nil
}

// verifySnapshot makes p, a copy from the master to the job's target,
// verify the master's certificate when the job does (TLSVerify).
func (jr *jobRun) verifySnapshot(ctx context.Context, p *proto.SnapshotParams) error {
	if !jr.st.TLSVerify {
		return nil
	}
	path, err := jr.trustCA(ctx, jr.target())
	if err != nil {
		return err
	}
	p.SSL, p.SSLCA, p.VerifyIdentity = true, path, true
	return nil
}

func tightenStepNames(replicaNames []string) []string {
	out := []string{"Pasang sertifikat di master"}
	for _, n := range replicaNames {
		out = append(out, "Pasang sertifikat di "+n, "Arahkan ulang "+n+" dengan verifikasi")
	}
	return out
}

// StartTightenTLS starts the tighten_tls job on the master's topology: the
// master's certificate, then each replica's certificate and its
// replication pointed at the master again with verification. It refuses a
// server that is not a master and a topology whose master or a replica has
// an unfinished job. A quarantined server with a replica row is left out.
func (r *Runner) StartTightenTLS(ctx context.Context, masterServerID int64) (string, error) {
	r.startMu.Lock()
	defer r.startMu.Unlock()
	srv, err := r.d.Store.GetServer(masterServerID)
	if err != nil {
		return "", err
	}
	topo, err := r.d.Store.GetTopologyByMaster(srv.ID)
	if errors.Is(err, store.ErrNotFound) {
		return "", errors.New("server ini bukan master; jalankan Siapkan master dulu")
	}
	if err != nil {
		return "", err
	}
	reps, err := r.d.Store.ListReplicas(topo.ID)
	if err != nil {
		return "", err
	}
	if err := r.refuseBusy(srv.ID, "master ini"); err != nil {
		return "", err
	}
	var ids []int64
	var names []string
	for _, rep := range reps {
		rs, err := r.d.Store.GetServer(rep.ServerID)
		if err != nil {
			return "", err
		}
		// A quarantined old master (its rebuild failed) gets no
		// certificate: its agent needs root's approval for it.
		if rs.Role == store.RoleQuarantined {
			r.log.Info("tighten tls: quarantined server left out", "server_id", rs.ID)
			continue
		}
		if err := r.refuseBusy(rep.ServerID, "replika "+rs.Name); err != nil {
			return "", err
		}
		ids = append(ids, rep.ServerID)
		names = append(names, rs.Name)
	}
	return r.createJob(KindTightenTLS, srv.ID, topo.ID, jobState{TightenReplicas: ids}, tightenStepNames(names))
}

// tightenTLSSteps are the steps of tighten_tls; the titles come from the
// stored steps (they name the replicas as they were named then).
func (jr *jobRun) tightenTLSSteps(rows []store.JobStep) []step {
	title := func(i int) string {
		if i < len(rows) {
			return rows[i].Name
		}
		return ""
	}
	steps := []step{{title(0), jr.installCert(jr.job.ServerID)}}
	for i, id := range jr.st.TightenReplicas {
		steps = append(steps,
			step{title(1 + 2*i), jr.tightenReplicaCert(id)},
			step{title(2 + 2*i), jr.tightenRepoint(id)})
	}
	return steps
}

// tightenSkip is why the replica's tighten steps are skipped, "" when they
// run: its agent is offline, it is no longer this topology's replica, it
// is not ready, or it has a job of its own.
func (jr *jobRun) tightenSkip(serverID int64) (string, error) {
	if reason, ok := jr.st.TightenSkipped[serverID]; ok {
		return reason, nil
	}
	if !jr.r.d.Hub.IsOnline(serverID) {
		return msgReplicaUnreachable, nil
	}
	rep, err := jr.r.d.Store.GetReplicaByServer(serverID)
	if errors.Is(err, store.ErrNotFound) {
		return "server ini tidak lagi menjadi replika", nil
	}
	if err != nil {
		return "", err
	}
	if rep.TopologyID != jr.job.TopologyID {
		return "server ini sekarang replika dari master lain", nil
	}
	if rep.State != store.ReplicaReady && rep.State != store.ReplicaAttention {
		return "replika " + replicaStateText(rep.State) + "; ulangi nanti", nil
	}
	if _, busy, err := jr.r.d.Store.ActiveJobForServer(serverID); err != nil {
		return "", err
	} else if busy {
		return "replika sedang menjalankan pekerjaan lain; ulangi nanti", nil
	}
	return "", nil
}

// skipReplica records that the replica's steps are skipped, so its
// redirect step is skipped for the same reason.
func (jr *jobRun) skipReplica(serverID int64, reason string) outcome {
	if jr.st.TightenSkipped == nil {
		jr.st.TightenSkipped = map[int64]string{}
	}
	jr.st.TightenSkipped[serverID] = reason
	jr.save()
	jr.logf("warn", "%s dilewati: %s.", jr.serverName(serverID), reason)
	return skipped(reason)
}

func (jr *jobRun) tightenReplicaCert(serverID int64) func(ctx context.Context, approved bool) (outcome, error) {
	install := jr.installCert(serverID)
	return func(ctx context.Context, approved bool) (outcome, error) {
		// A retry looks again: the replica may be back.
		delete(jr.st.TightenSkipped, serverID)
		reason, err := jr.tightenSkip(serverID)
		if err != nil {
			return outcome{}, err
		}
		if reason != "" {
			return jr.skipReplica(serverID, reason), nil
		}
		return install(ctx, approved)
	}
}

// tightenRepoint points the replica at its master again, now verifying
// the master's certificate: STOP REPLICA, the CA file, CHANGE REPLICATION
// SOURCE with SOURCE_SSL_VERIFY_SERVER_CERT, START REPLICA, and
// tls_verified. Around the stop the replica is marked paused, as Stop
// replika does, so a health report in between reads "Dihentikan" (stopped
// by the panel) and opens no replica_stopped alert; a replica an operator
// stopped stays stopped and paused.
func (jr *jobRun) tightenRepoint(serverID int64) func(ctx context.Context, approved bool) (outcome, error) {
	return func(ctx context.Context, _ bool) (outcome, error) {
		reason, err := jr.tightenSkip(serverID)
		if err != nil {
			return outcome{}, err
		}
		if reason != "" {
			if _, ok := jr.st.TightenSkipped[serverID]; ok {
				return skipped(reason), nil
			}
			return jr.skipReplica(serverID, reason), nil
		}
		rep, err := jr.r.d.Store.GetReplicaByServer(serverID)
		if err != nil {
			return outcome{}, err
		}
		// Paused by this job's earlier attempt is not the operator's stop.
		operatorPaused := rep.Paused && jr.st.TightenPaused != serverID
		topo, password, err := jr.replAccount()
		if err != nil {
			return outcome{}, err
		}
		if !operatorPaused {
			jr.st.TightenPaused = serverID
			jr.save()
			if err := jr.r.d.Store.SetReplicaPaused(serverID, true); err != nil {
				return outcome{}, err
			}
			jr.r.replicaChanged(serverID)
		}
		if err := jr.call(ctx, serverID, "mysql.stopReplica", nil, nil); err != nil {
			jr.restartReplica(ctx, serverID)
			return outcome{}, err
		}
		path, err := jr.trustCA(ctx, serverID)
		if err == nil {
			err = jr.call(ctx, serverID, "mysql.configureReplication", proto.ReplicationSourceParams{
				Host: topo.SourceHost, Port: topo.SourcePort, User: topo.ReplUser, Password: password, SSL: true,
				SSLCA: path, VerifyServerCert: true, Databases: rep.Databases,
			}, nil)
		}
		if err != nil {
			// Replication goes on as it was configured before.
			jr.restartReplica(ctx, serverID)
			return outcome{}, err
		}
		// The verified source replaced whatever tls_verified described.
		if err := jr.r.d.Store.SetReplicaTLSVerified(serverID, false); err != nil {
			return outcome{}, err
		}
		if operatorPaused {
			// Nothing connects while the operator's stop holds, so nothing
			// proves the verification yet (SEC-63c).
			jr.logf("warn", "Replikasi %s diarahkan dengan verifikasi sertifikat master %s, tetapi tetap dihentikan seperti "+
				"sebelumnya; tanda terverifikasi diberikan setelah replikasi dijalankan lagi dan Perketat diulang.",
				jr.serverName(serverID), topo.SourceHost)
			return done("verifikasi diatur; replikasi tetap dihentikan, tanda terverifikasi menunggu replikasi berjalan"), nil
		}
		var rs proto.ReplicaStatus
		if err := jr.call(ctx, serverID, "mysql.startReplica", nil, &rs); err != nil {
			// The replica cannot connect with verification (a name or
			// chain mismatch, or an applier error it had before): the
			// unverified source is gone, so it goes back.
			return outcome{}, jr.revertUnverified(ctx, serverID, topo, password, rep.Databases, err)
		}
		last, ok, err := jr.awaitVerifiedLink(ctx, serverID, &rs)
		if err != nil {
			jr.restartReplica(ctx, serverID)
			return outcome{}, err
		}
		if ioRefused(last) {
			// The receiver refused the verified source (a name or chain
			// mismatch): it goes back, as when START REPLICA fails. The
			// paused mark stays until restartReplica clears it.
			return outcome{}, jr.revertUnverified(ctx, serverID, topo, password, rep.Databases,
				errors.New(unverifiedText(last)))
		}
		jr.unpause(serverID)
		if !ok {
			return outcome{}, stepError(fmt.Sprintf("replikasi %s belum tersambung ke master dengan verifikasi sertifikat (%s); "+
				"tanda terverifikasi belum diberikan. Periksa replika lalu Ulangi", jr.serverName(serverID), unverifiedText(last)))
		}
		if err := jr.r.d.Store.SetReplicaTLSVerified(serverID, true); err != nil {
			return outcome{}, err
		}
		jr.logf("info", "Replikasi %s sekarang memverifikasi sertifikat master %s (thread IO tersambung).",
			jr.serverName(serverID), topo.SourceHost)
		return done("terverifikasi"), nil
	}
}

// revertUnverified points the replica at its master again as before the
// redirect (TLS without verification) after START REPLICA failed on the
// verified source, starts it and clears the paused mark (best effort, as
// restartReplica). The step fails either way, with cause.
func (jr *jobRun) revertUnverified(ctx context.Context, serverID int64, topo store.Topology, password string,
	databases []string, cause error) error {
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelTimeout)
	defer cancel()
	// A failed start may leave a thread running; CHANGE needs both stopped.
	if err := jr.r.d.Hub.Call(rctx, serverID, "mysql.stopReplica", nil, nil); err != nil {
		jr.r.log.Warn("stop replica before reverting the redirect", "job", jr.job.ID, "server_id", serverID, "err", logErr(err))
	}
	err := jr.r.d.Hub.Call(rctx, serverID, "mysql.configureReplication", proto.ReplicationSourceParams{
		Host: topo.SourceHost, Port: topo.SourcePort, User: topo.ReplUser, Password: password, SSL: true,
		Databases: databases,
	}, nil)
	if err != nil {
		jr.logf("error", "Replikasi %s tidak bisa dikembalikan tanpa verifikasi: %v", jr.serverName(serverID), err)
		return stepError(fmt.Sprintf("verifikasi gagal: %v; mengembalikan replikasi tanpa verifikasi juga gagal (%v); "+
			"periksa replika lalu Ulangi", cause, err))
	}
	jr.restartReplica(rctx, serverID)
	if jr.st.TightenPaused == serverID {
		return stepError(fmt.Sprintf("verifikasi gagal: %v; replikasi dikembalikan tanpa verifikasi tetapi belum berjalan; "+
			"klik Start replika atau Ulangi", cause))
	}
	jr.logf("warn", "Replikasi %s dikembalikan tanpa verifikasi dan berjalan lagi.", jr.serverName(serverID))
	return stepError(fmt.Sprintf("verifikasi gagal: %v; replikasi dikembalikan tanpa verifikasi", cause))
}

// unpause clears the paused mark tightenRepoint set on the replica, now
// that its replication runs again.
func (jr *jobRun) unpause(serverID int64) {
	if err := jr.r.d.Store.SetReplicaPaused(serverID, false); err != nil {
		jr.r.log.Warn("clear paused after redirect", "job", jr.job.ID, "server_id", serverID, "err", err)
		return
	}
	jr.st.TightenPaused = 0
	jr.save()
	jr.r.replicaChanged(serverID)
}

// restartReplica starts the replica's replication again after a redirect
// that stopped it did not finish, and clears the paused mark (best
// effort). A replica this job did not mark paused (the operator stopped
// it, or the redirect had not begun) is left alone. When the start fails
// it stays paused: Dihentikan, until Ulangi or Start replika.
func (jr *jobRun) restartReplica(ctx context.Context, serverID int64) {
	if jr.st.TightenPaused != serverID {
		return
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelTimeout)
	defer cancel()
	if err := jr.r.d.Hub.Call(sctx, serverID, "mysql.startReplica", nil, nil); err != nil {
		jr.r.log.Warn("start replica after unfinished redirect", "job", jr.job.ID, "server_id", serverID, "err", logErr(err))
		return
	}
	jr.unpause(serverID)
}

// tightenReplicaAt is the replica whose steps tighten_tls step idx belongs
// to, and whether idx is its redirect step.
func (st jobState) tightenReplicaAt(idx int) (serverID int64, repoint, ok bool) {
	if idx < 1 {
		return 0, false, false
	}
	i := (idx - 1) / 2
	if i >= len(st.TightenReplicas) {
		return 0, false, false
	}
	return st.TightenReplicas[i], idx%2 == 0, true
}

// refuseQuarantinedTLS refuses running a mysql_tls job again on a server
// that is quarantined now (P15).
func (r *Runner) refuseQuarantinedTLS(j store.Job) error {
	if j.Kind != KindMySQLTLS {
		return nil
	}
	srv, err := r.d.Store.GetServer(j.ServerID)
	if err != nil {
		return err
	}
	if srv.Role == store.RoleQuarantined {
		return errMySQLTLSQuarantined
	}
	return nil
}
