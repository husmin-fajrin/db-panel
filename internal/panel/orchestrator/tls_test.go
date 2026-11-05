package orchestrator

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

func stepNames(steps []store.JobStep) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.Name
	}
	return out
}

// installedCert is the certificate the panel sent the agent in its last
// mysql.tlsInstall, checked against the panel's MySQL CA.
func (e *env) installedCert(fa *fakeAgent) (*x509.Certificate, proto.TLSInstallParams) {
	e.t.Helper()
	var in proto.TLSInstallParams
	fa.params("mysql.tlsInstall", &in)
	if in.CAPEM != string(e.mysqlCA.CAPEM()) {
		e.t.Fatalf("%s: tlsInstall CA is not the panel's MySQL CA", fa.name)
	}
	cert := parseCertPEM(e.t, in.CertPEM)
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(e.mysqlCA.CAPEM())
	if _, err := cert.Verify(x509.VerifyOptions{Roots: pool, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		e.t.Fatalf("%s: certificate does not chain to the MySQL CA: %v", fa.name, err)
	}
	return cert, in
}

// wantRecorded checks that server_tls holds cert for fa's server.
func (e *env) wantRecorded(fa *fakeAgent, cert *x509.Certificate) {
	e.t.Helper()
	rec, err := e.st.GetServerTLS(fa.id)
	if err != nil {
		e.t.Fatalf("%s: server_tls: %v", fa.name, err)
	}
	if rec.CertSerial != fmt.Sprintf("%032x", cert.SerialNumber) || !rec.NotAfter.Equal(cert.NotAfter.UTC().Truncate(time.Second)) {
		e.t.Fatalf("%s: server_tls = %+v, want serial %032x not after %s", fa.name, rec, cert.SerialNumber, cert.NotAfter)
	}
}

func ipStrings(ips []net.IP) []string {
	var out []string
	for _, ip := range ips {
		out = append(out, ip.String())
	}
	return out
}

func TestMySQLTLSJobInstallsAndRecords(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	// Replicas reach the master at a name: it goes into the certificate
	// next to the server's address.
	if err := e.st.SetTopologySource(topo.ID, "db.example.test", 3306); err != nil {
		t.Fatal(err)
	}

	id, err := e.r.StartMySQLTLS(context.Background(), m.id)
	if err != nil {
		t.Fatalf("StartMySQLTLS: %v", err)
	}
	j, steps := e.waitJob(id, store.JobSucceeded)
	if j.Kind != KindMySQLTLS || j.ServerID != m.id {
		t.Fatalf("job = %+v", j)
	}
	if got, want := stepNames(steps), []string{"Minta kunci dan permintaan sertifikat", "Terbitkan sertifikat",
		"Pasang sertifikat di MySQL"}; !slices.Equal(got, want) {
		t.Fatalf("steps = %q, want %q", got, want)
	}
	wantCmds(t, "master", m.cmds(0), []string{"mysql.tlsRequest", "mysql.tlsInstall"})
	cert, in := e.installedCert(m)
	if in.VerifyHost != "10.0.0.1" {
		t.Fatalf("VerifyHost = %q, want the server's address", in.VerifyHost)
	}
	if got := ipStrings(cert.IPAddresses); !slices.Equal(got, []string{"10.0.0.1"}) ||
		!slices.Equal(cert.DNSNames, []string{"db.example.test"}) {
		t.Fatalf("certificate names: IP %v DNS %v", got, cert.DNSNames)
	}
	if cert.Subject.CommonName != fmt.Sprintf("dbpanel-mysql-%d", m.id) {
		t.Fatalf("CN = %q", cert.Subject.CommonName)
	}
	e.wantRecorded(m, cert)

	// A server whose MySQL is not connected is refused.
	bare := e.agent("bare", "10.0.0.9")
	if _, err := e.r.StartMySQLTLS(context.Background(), bare.id); err == nil || !strings.Contains(err.Error(), "belum dihubungkan") {
		t.Fatalf("StartMySQLTLS on an unconnected server: %v", err)
	}
	// A busy server is refused.
	if err := e.st.CreateJob(store.Job{ID: "busy", Kind: KindPrepareMaster, ServerID: m.id, ParamsJSON: "{}",
		CreatedAt: time.Now()}, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.r.StartMySQLTLS(context.Background(), m.id); !errors.Is(err, ErrBusy) {
		t.Fatalf("StartMySQLTLS on a busy server: %v", err)
	}
}

func TestPrepareMasterIncludesTLSStep(t *testing.T) {
	e := newEnv(t)
	fa := e.connectedMaster("db-master")
	scriptPrepare(fa)

	id, err := e.r.StartPrepareMaster(context.Background(), fa.id)
	if err != nil {
		t.Fatalf("StartPrepareMaster: %v", err)
	}
	_, steps := e.waitJob(id, store.JobSucceeded)
	if got, want := stepNames(steps), []string{"Periksa master", "Terapkan pengaturan", "Pasang sertifikat MySQL",
		"Mulai ulang MySQL", "Verifikasi"}; !slices.Equal(got, want) {
		t.Fatalf("steps = %q, want %q", got, want)
	}
	wantCmds(t, "master", fa.cmds(0), []string{"mysql.preflight", "mysql.configure", "mysql.createReplUser",
		"mysql.tlsRequest", "mysql.tlsInstall", "mysql.status"})
	cert, in := e.installedCert(fa)
	// The address and the topology's source host (the same here) once.
	if got := ipStrings(cert.IPAddresses); in.VerifyHost != "10.0.0.1" || !slices.Equal(got, []string{"10.0.0.1"}) ||
		len(cert.DNSNames) != 0 {
		t.Fatalf("VerifyHost %q, certificate IP %v DNS %v", in.VerifyHost, got, cert.DNSNames)
	}
	e.wantRecorded(fa, cert)
}

func TestCreateReplicaVerifiesWhenMasterHasCert(t *testing.T) {
	for _, tc := range []struct {
		name     string
		method   string
		dbs      []string
		verified bool
	}{
		{"dump with the master's certificate", "dump", []string{"a"}, true},
		{"clone with the master's certificate", "clone", nil, true},
		{"dump without the master's certificate", "dump", []string{"a"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			m, _ := e.readyMaster("db-master")
			r := e.replicaAgent("replica1", tc.method)
			if tc.verified {
				if err := e.st.SetServerTLS(store.ServerTLS{ServerID: m.id, CertSerial: "01", NotAfter: time.Now().AddDate(2, 0, 0),
					InstalledAt: time.Now()}); err != nil {
					t.Fatal(err)
				}
			}
			id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
				MasterServerID: m.id, TargetServerID: r.id, Databases: tc.dbs, ConfirmWipe: true})
			if err != nil {
				t.Fatalf("StartCreateReplica: %v", err)
			}
			_, steps := e.waitJob(id, store.JobSucceeded)
			if got, want := stepNames(steps), []string{"Periksa master", "Periksa server tujuan", "Pasang MySQL",
				"Siapkan replika", "Salin data", "Pasang sertifikat MySQL", "Sambungkan replikasi", "Verifikasi"}; !slices.Equal(got, want) {
				t.Fatalf("steps = %q, want %q", got, want)
			}
			snapshot := "mysql.dumpFrom"
			if tc.method == "clone" {
				snapshot = "mysql.cloneFrom"
			}
			want := []string{"probe", "mysql.preflight", "mysql.configure", "mysql.restart", "mysql.setReadOnly", "mysql.status"}
			if tc.verified {
				want = append(want, "mysql.tlsTrust")
			}
			want = append(want, snapshot)
			if tc.method == "clone" {
				want = append(want, "mysql.setReadOnly")
			}
			want = append(want, "mysql.tlsRequest", "mysql.tlsInstall", "mysql.status")
			if tc.verified {
				want = append(want, "mysql.tlsTrust")
			}
			want = append(want, "mysql.configureReplication", "mysql.startReplica", "mysql.startReplica", "mysql.waitSynced")
			wantCmds(t, "replica", r.cmds(0), want)

			// The replica gets its own certificate either way.
			cert, in := e.installedCert(r)
			if in.VerifyHost != "10.0.0.2" || !slices.Equal(ipStrings(cert.IPAddresses), []string{"10.0.0.2"}) {
				t.Fatalf("replica certificate: VerifyHost %q, IPs %v", in.VerifyHost, cert.IPAddresses)
			}
			e.wantRecorded(r, cert)

			var snap proto.SnapshotParams
			r.params(snapshot, &snap)
			var src proto.ReplicationSourceParams
			r.params("mysql.configureReplication", &src)
			ca := ""
			if tc.verified {
				ca = fakeCAPath
			}
			if snap.VerifyIdentity != tc.verified || snap.SSLCA != ca || !snap.SSL {
				t.Fatalf("%s params: verify %v, ssl_ca %q, ssl %v", snapshot, snap.VerifyIdentity, snap.SSLCA, snap.SSL)
			}
			if src.VerifyServerCert != tc.verified || src.SSLCA != ca || !src.SSL || src.Host != "10.0.0.1" {
				t.Fatalf("configureReplication params = %+v", src)
			}
			if rep := e.replica(r.id); rep.TLSVerified != tc.verified {
				t.Fatalf("tls_verified = %v, want %v", rep.TLSVerified, tc.verified)
			}
			const unverified = "Sambungan replikasi belum diverifikasi: master belum punya sertifikat panel"
			if log := e.jobLog(id); strings.Contains(log, unverified) == tc.verified {
				t.Fatalf("job log (verified %v):\n%s", tc.verified, log)
			}
		})
	}
}

func TestTightenTLSSkipsOfflineReplica(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	r1 := e.followingReplica(topo, "a")
	r2 := e.agent("replica2", "10.0.0.3")
	if _, err := e.st.AddReplica(topo.ID, r2.id, nil); err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetReplicaState(r2.id, store.ReplicaReady, ""); err != nil {
		t.Fatal(err)
	}
	r2.detach()

	id, err := e.r.StartTightenTLS(context.Background(), m.id)
	if err != nil {
		t.Fatalf("StartTightenTLS: %v", err)
	}
	j, steps := e.waitJob(id, store.JobSucceeded)
	if j.Kind != KindTightenTLS || j.ServerID != m.id || j.TopologyID != topo.ID {
		t.Fatalf("job = %+v", j)
	}
	if got, want := stepNames(steps), []string{"Pasang sertifikat di master", "Pasang sertifikat di replica1",
		"Arahkan ulang replica1 dengan verifikasi", "Pasang sertifikat di replica2",
		"Arahkan ulang replica2 dengan verifikasi"}; !slices.Equal(got, want) {
		t.Fatalf("steps = %q, want %q", got, want)
	}
	if got, want := stepStatuses(steps), []string{store.StepDone, store.StepDone, store.StepDone, store.StepSkipped,
		store.StepSkipped}; !slices.Equal(got, want) {
		t.Fatalf("step statuses = %v, want %v", got, want)
	}
	if steps[3].Message != "replika tidak terjangkau; ulangi nanti" || steps[4].Message != "replika tidak terjangkau; ulangi nanti" {
		t.Fatalf("skipped steps = %+v, %+v", steps[3], steps[4])
	}
	wantCmds(t, "master", m.cmds(0), []string{"mysql.tlsRequest", "mysql.tlsInstall"})
	wantCmds(t, "replica1", r1.cmds(0), []string{"mysql.tlsRequest", "mysql.tlsInstall", "mysql.stopReplica",
		"mysql.tlsTrust", "mysql.configureReplication", "mysql.startReplica"})
	mcert, _ := e.installedCert(m)
	e.wantRecorded(m, mcert)
	rcert, _ := e.installedCert(r1)
	e.wantRecorded(r1, rcert)

	var src proto.ReplicationSourceParams
	r1.params("mysql.configureReplication", &src)
	if src.Host != "10.0.0.1" || src.Port != 3306 || src.User != "dbpanel_repl" || src.Password != replPW || !src.SSL ||
		!src.VerifyServerCert || src.SSLCA != fakeCAPath || !slices.Equal(src.Databases, []string{"a"}) {
		t.Fatalf("configureReplication params = %+v", src)
	}
	if !e.replica(r1.id).TLSVerified || e.replica(r2.id).TLSVerified {
		t.Fatalf("tls_verified: replica1 %v, replica2 %v", e.replica(r1.id).TLSVerified, e.replica(r2.id).TLSVerified)
	}
	if _, err := e.st.GetServerTLS(r2.id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("server_tls of the offline replica: %v", err)
	}
}

func TestTightenTLSKeepsPausedReplicaStopped(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	r := e.followingReplica(topo)
	if err := e.st.SetReplicaPaused(r.id, true); err != nil {
		t.Fatal(err)
	}
	id, err := e.r.StartTightenTLS(context.Background(), m.id)
	if err != nil {
		t.Fatalf("StartTightenTLS: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	wantCmds(t, "replica", r.cmds(0), []string{"mysql.tlsRequest", "mysql.tlsInstall", "mysql.stopReplica",
		"mysql.tlsTrust", "mysql.configureReplication"})
	if !e.replica(r.id).TLSVerified {
		t.Fatal("tls_verified not set")
	}
}

func TestTightenTLSRefusals(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	r := e.followingReplica(topo, "a")
	plain := e.connectedMaster("not-a-master")

	if _, err := e.r.StartTightenTLS(context.Background(), plain.id); err == nil || !strings.Contains(err.Error(), "bukan master") {
		t.Fatalf("StartTightenTLS on a server without topology: %v", err)
	}
	// A replica of the topology with an unfinished job: refused.
	if err := e.st.CreateJob(store.Job{ID: "busy", Kind: KindAddDatabases, ServerID: r.id, TopologyID: topo.ID,
		ParamsJSON: "{}", CreatedAt: time.Now()}, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.r.StartTightenTLS(context.Background(), m.id); !errors.Is(err, ErrBusy) || !strings.Contains(err.Error(), "replica1") {
		t.Fatalf("StartTightenTLS with a busy replica: %v", err)
	}
}

func TestDailyRenewalStartsJob(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	later := e.connectedMaster("db-later")
	now := time.Now()
	for _, rec := range []store.ServerTLS{
		{ServerID: m.id, CertSerial: "01", NotAfter: now.Add(29 * 24 * time.Hour), InstalledAt: now.AddDate(-2, 0, 0)},
		{ServerID: later.id, CertSerial: "02", NotAfter: now.Add(31 * 24 * time.Hour), InstalledAt: now.AddDate(-2, 0, 0)},
	} {
		if err := e.st.SetServerTLS(rec); err != nil {
			t.Fatal(err)
		}
	}

	ids := e.r.RenewMySQLTLS(context.Background())
	if len(ids) != 1 {
		t.Fatalf("renewal started %d jobs, want 1", len(ids))
	}
	j, _ := e.waitJob(ids[0], store.JobSucceeded)
	if j.Kind != KindMySQLTLS || j.ServerID != m.id {
		t.Fatalf("renewal job = %+v", j)
	}
	cert, _ := e.installedCert(m)
	e.wantRecorded(m, cert)

	// Renewed: the next daily check starts nothing.
	if ids := e.r.RenewMySQLTLS(context.Background()); len(ids) != 0 {
		t.Fatalf("second check started %v", ids)
	}
	jobs, err := e.st.ListJobs(0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, j := range jobs {
		if j.Kind == KindMySQLTLS {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d mysql_tls jobs, want 1", n)
	}
	if later.count("mysql.tlsRequest") != 0 {
		t.Fatal("a certificate valid for 31 more days was renewed")
	}
}

func TestDailyRenewalSkipsOfflineAndBusyServers(t *testing.T) {
	e := newEnv(t)
	off := e.connectedMaster("db-off")
	busy := e.connectedMaster("db-busy")
	soon := time.Now().Add(10 * 24 * time.Hour)
	for _, fa := range []*fakeAgent{off, busy} {
		if err := e.st.SetServerTLS(store.ServerTLS{ServerID: fa.id, CertSerial: "01", NotAfter: soon, InstalledAt: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	off.detach()
	if err := e.st.CreateJob(store.Job{ID: "busy", Kind: KindPrepareMaster, ServerID: busy.id, ParamsJSON: "{}",
		CreatedAt: time.Now()}, []string{"x"}); err != nil {
		t.Fatal(err)
	}
	if ids := e.r.RenewMySQLTLS(context.Background()); len(ids) != 0 {
		t.Fatalf("renewal started %v", ids)
	}
}

func TestAddDatabasesDumpVerifiesMasterCert(t *testing.T) {
	e := newEnv(t)
	m, topo := e.readyMaster("db-master")
	r := e.followingReplica(topo, "a")
	if err := e.st.SetServerTLS(store.ServerTLS{ServerID: m.id, CertSerial: "01", NotAfter: time.Now().AddDate(2, 0, 0),
		InstalledAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	id, err := e.r.StartAddDatabases(context.Background(), r.id, []string{"b"})
	if err != nil {
		t.Fatalf("StartAddDatabases: %v", err)
	}
	e.waitJob(id, store.JobSucceeded)
	cmds := r.cmds(0)
	if i := slices.Index(cmds, "mysql.tlsTrust"); i < 0 || cmds[i+1] != "mysql.dumpFrom" {
		t.Fatalf("replica commands = %v, want mysql.tlsTrust right before mysql.dumpFrom", cmds)
	}
	var snap proto.SnapshotParams
	r.params("mysql.dumpFrom", &snap)
	if !snap.SSL || !snap.VerifyIdentity || snap.SSLCA != fakeCAPath {
		t.Fatalf("dumpFrom params = %+v", snap)
	}
}
