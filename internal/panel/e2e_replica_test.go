package panel

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/agent"
	"github.com/husmin-fajrin/db-panel/internal/agent/commands"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/drivers/mysql/testmysql"
	"github.com/husmin-fajrin/db-panel/internal/agent/jobs"
	"github.com/husmin-fajrin/db-panel/internal/agent/probe"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/secrets"
)

// Deadlines of the end-to-end test.
const (
	e2ePrepareTimeout = 3 * time.Minute
	e2eCreateTimeout  = 5 * time.Minute
	e2eAddTimeout     = 3 * time.Minute
	e2eReplicaLag     = 10 * time.Second
)

// TestE2ECreateReplica drives a real panel, three real agents and three
// real mysqld instances through "Siapkan master", "Buat replika" (dump of
// one database with a replication filter), "Tambah database", and a second
// "Buat replika" of every database with CLONE onto a target whose stored
// admin account is not root (the copy replaces it: the job waits for the
// user to reconnect), over the panel's HTTP UI, and checks the result in
// MySQL itself.
func TestE2ECreateReplica(t *testing.T) {
	if testing.Short() {
		t.Skip("end-to-end test skipped in -short mode")
	}
	if _, err := mysql.FindMysqld(probe.RealSystem()); err != nil {
		t.Skip("mysqld not installed: " + err.Error())
	}
	start := time.Now()

	// The instances first, so their cleanup (stop mysqld) runs last, after
	// the agents and the panel have stopped.
	masterInst := testmysql.Start(t, testmysql.Options{})
	replicaInst := testmysql.Start(t, testmysql.Options{})
	cloneInst := testmysql.Start(t, testmysql.Options{})
	masterDB := openInstance(t, masterInst)
	replicaDB := openInstance(t, replicaInst)
	cloneDB := openInstance(t, cloneInst)
	// The CLONE target's admin account is not root, as on a server whose
	// MySQL the panel installed (dbpanel_admin): the master has no such
	// account, so the copy replaces it.
	const cloneAdminPW = "Adm1n-e2e-pw"
	execSQL(t, cloneDB, "CREATE USER adm_e2e@localhost IDENTIFIED BY '"+cloneAdminPW+"'",
		"GRANT ALL PRIVILEGES ON *.* TO adm_e2e@localhost WITH GRANT OPTION")
	execSQL(t, masterDB,
		"CREATE DATABASE a", "CREATE TABLE a.t (id INT PRIMARY KEY, v VARCHAR(64))", "INSERT INTO a.t VALUES (1, 'awal')",
		"CREATE DATABASE b", "CREATE TABLE b.t (id INT PRIMARY KEY, v VARCHAR(64))", "INSERT INTO b.t VALUES (1, 'awal')",
	)

	var logs syncBuffer // panel and agents; checked for the replication password
	o := testOptions(t)
	o.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	p := newPanel(t, o)
	uiAddr, _ := startPanel(t, p)
	base := "http://" + uiAddr

	masterSrv := startE2EAgent(t, p, "master-e2e", masterInst, &logs)
	replicaSrv := startE2EAgent(t, p, "replika-e2e", replicaInst, &logs)

	ui := newUIClient(t, base)
	ui.login(readInitialPassword(t, o))

	// Hubungkan MySQL on both servers as the instances' root over the socket.
	for _, id := range []int64{masterSrv, replicaSrv} {
		loc := ui.postRedirect(serverPath(id)+"/mysql/connect", url.Values{"user": {"root"}, "password": {""}, "host": {""}})
		if want := serverPath(id) + "?pesan=terhubung"; loc != want {
			t.Fatalf("connect MySQL on server %d redirected to %q, want %q", id, loc, want)
		}
	}

	// Siapkan master.
	prepareID := ui.postJob(serverPath(masterSrv)+"/prepare-master", nil)
	prepareWaited := false
	job := waitJob(t, p, prepareID, e2ePrepareTimeout, func(j store.Job) bool {
		if j.Status != store.JobWaiting {
			return false
		}
		// The instance binds 127.0.0.1 only; if the panel asks to restart
		// for that, approve it like the user would.
		prepareWaited = true
		_, steps, err := p.Store.GetJob(j.ID)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("prepare_master waits for approval at %q (%s); approving", steps[j.CurrentStep].Name, steps[j.CurrentStep].Message)
		ui.postRedirect("/jobs/"+j.ID+"/approve", nil)
		return true
	})
	if job.Status != store.JobSucceeded {
		t.Fatalf("prepare_master ended %s: %s\n%s", job.Status, job.Error, jobLogText(t, p, prepareID))
	}
	t.Logf("prepare_master succeeded (waited for approval: %v) after %s", prepareWaited, time.Since(start).Round(time.Second))
	topo, err := p.Store.GetTopologyByMaster(masterSrv)
	if err != nil {
		t.Fatal(err)
	}
	if topo.SourceHost != "127.0.0.1" || topo.SourcePort != masterInst.Port {
		t.Fatalf("topology source = %s:%d, want 127.0.0.1:%d", topo.SourceHost, topo.SourcePort, masterInst.Port)
	}

	// Buat replika: the form offers the replica server and both databases.
	form := ui.get(fmt.Sprintf("/masters/%d/replicas/new", masterSrv))
	for _, want := range []string{
		`<option value="` + strconv.FormatInt(replicaSrv, 10) + `"`,
		`name="databases" value="a"`, `name="databases" value="b"`,
	} {
		if !strings.Contains(form, want) {
			t.Fatalf("replica form lacks %s:\n%s", want, form)
		}
	}
	createStart := time.Now()
	createID := ui.postJob(fmt.Sprintf("/masters/%d/replicas", masterSrv), url.Values{
		"target":       {strconv.FormatInt(replicaSrv, 10)},
		"databases":    {"a"},
		"confirm_wipe": {"1"},
	})
	job = waitJob(t, p, createID, e2eCreateTimeout, nil)
	if job.Status != store.JobSucceeded {
		t.Fatalf("create_replica ended %s: %s\n%s", job.Status, job.Error, jobLogText(t, p, createID))
	}
	t.Logf("create_replica succeeded in %s", time.Since(createStart).Round(time.Second))
	if log := jobLogText(t, p, createID); !strings.Contains(log, "Metode salinan awal: dump.") {
		t.Fatalf("create_replica of a subset did not copy with a dump:\n%s", log)
	}
	rep, err := p.Store.GetReplicaByServer(replicaSrv)
	if err != nil {
		t.Fatal(err)
	}
	if rep.State != store.ReplicaReady || strings.Join(rep.Databases, ",") != "a" {
		t.Fatalf("replica = %+v, want ready following [a]", rep)
	}

	// The snapshot carried a, not b; replication follows a only.
	waitRow(t, replicaDB, "a", 1, "awal")
	if schemaExists(t, replicaDB, "b") {
		t.Fatal("database b exists on the replica after a snapshot of [a]")
	}
	execSQL(t, masterDB, "INSERT INTO b.t VALUES (2, 'tidak-diikuti')", "INSERT INTO a.t VALUES (2, 'diikuti')")
	// a's row came after b's in the binlog, so once it is applied b's has
	// been seen (and filtered).
	waitRow(t, replicaDB, "a", 2, "diikuti")
	if schemaExists(t, replicaDB, "b") {
		t.Fatal("a write to b reached the replica that follows [a] only")
	}
	var name, value string
	if err := replicaDB.QueryRow("SHOW GLOBAL VARIABLES LIKE 'super_read_only'").Scan(&name, &value); err != nil {
		t.Fatal(err)
	}
	if value != "ON" {
		t.Fatalf("super_read_only on the replica = %s, want ON", value)
	}

	// Tambah database b.
	addStart := time.Now()
	addID := ui.postJob(fmt.Sprintf("/replicas/%d/databases/add", replicaSrv), url.Values{"databases": {"b"}})
	job = waitJob(t, p, addID, e2eAddTimeout, nil)
	if job.Status != store.JobSucceeded {
		t.Fatalf("add_databases ended %s: %s\n%s", job.Status, job.Error, jobLogText(t, p, addID))
	}
	t.Logf("add_databases succeeded in %s", time.Since(addStart).Round(time.Second))
	if rep, err = p.Store.GetReplicaByServer(replicaSrv); err != nil {
		t.Fatal(err)
	}
	if rep.State != store.ReplicaReady || strings.Join(rep.Databases, ",") != "a,b" {
		t.Fatalf("replica after add = %+v, want ready following [a b]", rep)
	}
	// The dump of b brought the rows written before; a new row replicates.
	waitRow(t, replicaDB, "b", 2, "tidak-diikuti")
	execSQL(t, masterDB, "INSERT INTO b.t VALUES (3, 'sekarang-diikuti')", "INSERT INTO a.t VALUES (3, 'tetap')")
	waitRow(t, replicaDB, "b", 3, "sekarang-diikuti")
	waitRow(t, replicaDB, "a", 3, "tetap")

	// Buat replika of every database on a bare target: CLONE.
	cloneSrv := startE2EAgent(t, p, "replika-clone", cloneInst, &logs)
	if loc := ui.postRedirect(serverPath(cloneSrv)+"/mysql/connect",
		url.Values{"user": {"adm_e2e"}, "password": {cloneAdminPW}, "host": {""}}); loc != serverPath(cloneSrv)+"?pesan=terhubung" {
		t.Fatalf("connect MySQL on the clone target redirected to %q", loc)
	}
	cloneStart := time.Now()
	cloneID := ui.postJob(fmt.Sprintf("/masters/%d/replicas", masterSrv), url.Values{
		"target":       {strconv.FormatInt(cloneSrv, 10)},
		"databases":    {"a", "b"},
		"confirm_wipe": {"1"},
	})
	reconnected := false
	job = waitJob(t, p, cloneID, e2eCreateTimeout, func(j store.Job) bool {
		if !strings.Contains(j.ParamsJSON, `"wait_reason":"reconnect"`) || j.CurrentStep != 4 {
			t.Fatalf("clone job waits at step %d (%s):\n%s", j.CurrentStep, j.ParamsJSON, jobLogText(t, p, cloneID))
		}
		// The job page links to the server page, where the user connects
		// the master's admin account (root here).
		if body := ui.get("/jobs/" + j.ID); !strings.Contains(body, `href="`+serverPath(cloneSrv)+`"`) ||
			!strings.Contains(body, "Sudah saya hubungkan ulang") {
			t.Fatalf("job page lacks the reconnect link or button:\n%s", body)
		}
		if loc := ui.postRedirect(serverPath(cloneSrv)+"/mysql/connect",
			url.Values{"user": {"root"}, "password": {""}, "host": {""}}); loc != serverPath(cloneSrv)+"?pesan=terhubung" {
			t.Fatalf("reconnect redirected to %q", loc)
		}
		ui.postRedirect("/jobs/"+j.ID+"/approve", nil)
		reconnected = true
		return true
	})
	if job.Status != store.JobSucceeded {
		t.Fatalf("create_replica (clone) ended %s: %s\n%s", job.Status, job.Error, jobLogText(t, p, cloneID))
	}
	if !reconnected {
		t.Fatal("the clone job never waited for the reconnect")
	}
	cloneLog := jobLogText(t, p, cloneID)
	if !strings.Contains(cloneLog, "Metode salinan awal: clone.") || strings.Count(cloneLog, "Menyalin dengan CLONE") != 1 {
		t.Fatalf("clone job did not copy exactly once with CLONE:\n%s", cloneLog)
	}
	t.Logf("create_replica with CLONE succeeded in %s", time.Since(cloneStart).Round(time.Second))
	if rep, err := p.Store.GetReplicaByServer(cloneSrv); err != nil || rep.State != store.ReplicaReady || len(rep.Databases) != 0 {
		t.Fatalf("clone replica = %+v, %v; want ready following all", rep, err)
	}
	waitRow(t, cloneDB, "b", 3, "sekarang-diikuti")
	execSQL(t, masterDB, "INSERT INTO a.t (id, v) VALUES (4, 'setelah-clone')", "CREATE DATABASE c",
		"CREATE TABLE c.t (id INT PRIMARY KEY, v VARCHAR(64))", "INSERT INTO c.t VALUES (1, 'baru')")
	waitRow(t, cloneDB, "a", 4, "setelah-clone")
	waitRow(t, cloneDB, "c", 1, "baru")
	// Replication runs over TLS.
	var sslAllowed string
	if err := cloneDB.QueryRow("SELECT SSL_ALLOWED FROM performance_schema.replication_connection_configuration").Scan(&sslAllowed); err != nil ||
		!strings.EqualFold(sslAllowed, "yes") {
		t.Fatalf("replication SSL_ALLOWED = %q, %v", sslAllowed, err)
	}
	// Every temporary dump account was dropped from the master.
	var dumpAccounts int
	if err := masterDB.QueryRow("SELECT COUNT(*) FROM mysql.user WHERE user LIKE 'dbpanel\\_dump\\_%'").Scan(&dumpAccounts); err != nil ||
		dumpAccounts != 0 {
		t.Fatalf("dump accounts left on the master: %d, %v", dumpAccounts, err)
	}

	// The replication password is sealed in the store and never written to
	// a job (log, steps, params, error) or to the panel's or agents' logs.
	if topo, err = p.Store.GetTopologyByMaster(masterSrv); err != nil {
		t.Fatal(err)
	}
	box, err := secrets.LoadOrCreate(filepath.Join(o.ConfigDir, secretsKeyFile))
	if err != nil {
		t.Fatal(err)
	}
	plain, err := box.Open(topo.ReplPasswordEnc)
	if err != nil {
		t.Fatalf("open the stored replication password: %v", err)
	}
	password := string(plain)
	if len(password) != 24 {
		t.Fatalf("replication password has %d characters, want 24", len(password))
	}
	if bytes.Contains(topo.ReplPasswordEnc, plain) {
		t.Fatal("topologies.repl_password_enc contains the plaintext password")
	}
	for _, id := range []string{prepareID, createID, addID, cloneID} {
		j, steps, err := p.Store.GetJob(id)
		if err != nil {
			t.Fatal(err)
		}
		texts := []string{j.Error, j.ParamsJSON, jobLogText(t, p, id)}
		for _, s := range steps {
			texts = append(texts, s.Message)
		}
		for _, s := range texts {
			if strings.Contains(s, password) {
				t.Fatalf("job %s (%s) shows the replication password: %s", id, j.Kind, s)
			}
		}
	}
	if strings.Contains(logs.String(), password) {
		t.Fatal("the panel or agent log contains the replication password")
	}
	t.Logf("end-to-end flow took %s", time.Since(start).Round(time.Second))
}

// startE2EAgent pairs a new server called name with p and runs an agent for
// it in-process whose MySQL driver manages inst. The agent stops when the
// test ends, before inst does.
func startE2EAgent(t *testing.T, p *Panel, name string, inst *testmysql.Instance, logs io.Writer) int64 {
	t.Helper()
	srv, err := p.Store.CreateServer(name)
	if err != nil {
		t.Fatal(err)
	}
	tok, err := p.Store.CreatePairToken(srv.ID, time.Now(), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cfg, err := agent.Pair(ctx, p.PublicURL(), tok.Token, dir, nil, agent.PairOptions{})
	if err != nil {
		cancel()
		t.Fatalf("Pair %s: %v", name, err)
	}
	box, err := secrets.LoadOrCreate(filepath.Join(dir, "secrets.key"))
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	creds := mysql.NewFileCredentialStore(filepath.Join(dir, "mysql.cred"), box)

	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})).With("agent", name)
	sys := probe.RealSystem()
	reg := commands.NewRegistry()
	probe.Register(reg, sys, "e2e")
	a := agent.New(cfg, reg, logger)
	runner := jobs.NewRunner(a, logger)
	runner.Register(reg)
	d := &mysql.Driver{Host: inst.HostWith(creds), Creds: creds, Jobs: runner, Logger: logger, Sys: sys, Cfg: cfg}
	d.Register(reg)

	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Errorf("agent %s did not stop", name)
		}
	})
	waitForWithin(t, 30*time.Second, "agent "+name+" online", func() bool {
		got, err := p.Store.GetServer(srv.ID)
		if err != nil {
			t.Fatal(err)
		}
		return p.Hub.IsOnline(srv.ID) && got.ProbeJSON != ""
	})
	return srv.ID
}

// uiClient is a browser for the panel UI: a cookie jar, the Origin header
// form posts carry, and no redirect following.
type uiClient struct {
	t    *testing.T
	base string
	c    *http.Client
}

func newUIClient(t *testing.T, base string) *uiClient {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &uiClient{t: t, base: base, c: &http.Client{
		Jar:           jar,
		Timeout:       3 * time.Minute, // mysql.connect answers within the agent's call timeout
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (u *uiClient) login(password string) {
	u.t.Helper()
	if loc := u.postRedirect("/login", url.Values{"username": {"admin"}, "password": {password}}); loc != "/" {
		u.t.Fatalf("login redirected to %q, want /", loc)
	}
}

// get returns the body of a 200 page.
func (u *uiClient) get(path string) string {
	u.t.Helper()
	resp, err := u.c.Get(u.base + path)
	if err != nil {
		u.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		u.t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		u.t.Fatalf("GET %s = %d:\n%s", path, resp.StatusCode, b)
	}
	return string(b)
}

// postRedirect posts form to path and returns where the 303 answer points.
func (u *uiClient) postRedirect(path string, form url.Values) string {
	u.t.Helper()
	req, err := http.NewRequest("POST", u.base+path, strings.NewReader(form.Encode()))
	if err != nil {
		u.t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", u.base)
	resp, err := u.c.Do(req)
	if err != nil {
		u.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusSeeOther {
		u.t.Fatalf("POST %s = %d, want 303:\n%s", path, resp.StatusCode, b)
	}
	return resp.Header.Get("Location")
}

// postJob posts form to path, which starts a job, and returns the job's id.
func (u *uiClient) postJob(path string, form url.Values) string {
	u.t.Helper()
	loc := u.postRedirect(path, form)
	id, ok := strings.CutPrefix(loc, "/jobs/")
	if !ok || id == "" || strings.Contains(id, "/") {
		u.t.Fatalf("POST %s redirected to %q, want a job page", path, loc)
	}
	// The job page renders for the user who started it.
	if body := u.get(loc); !strings.Contains(body, id) {
		u.t.Fatalf("job page %s does not show the job", loc)
	}
	return id
}

func serverPath(id int64) string { return "/servers/" + strconv.FormatInt(id, 10) }

// waitJob polls the job until it succeeded, failed or was canceled, or
// timeout passes. onWait, when set, sees the job while it is waiting and
// returns whether it acted (approved) on it.
func waitJob(t *testing.T, p *Panel, id string, timeout time.Duration, onWait func(store.Job) bool) store.Job {
	t.Helper()
	deadline := time.Now().Add(timeout)
	approvedAt := -1
	for {
		j, _, err := p.Store.GetJob(id)
		if err != nil {
			t.Fatal(err)
		}
		switch j.Status {
		case store.JobSucceeded, store.JobFailed, store.JobCanceled:
			return j
		case store.JobWaiting:
			// Act once per waiting step; the job leaves waiting on its own.
			if onWait == nil {
				t.Fatalf("job %s (%s) waits at step %d:\n%s", id, j.Kind, j.CurrentStep, jobLogText(t, p, id))
			}
			if approvedAt != j.CurrentStep && onWait(j) {
				approvedAt = j.CurrentStep
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s (%s) still %s after %s:\n%s", id, j.Kind, j.Status, timeout, jobLogText(t, p, id))
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// jobLogText is the job's whole log, one line per entry.
func jobLogText(t *testing.T, p *Panel, id string) string {
	t.Helper()
	lines, err := p.Store.JobLog(id, 0, 100000)
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&b, "%s %s\n", l.Level, l.Message)
	}
	return b.String()
}

// waitForWithin is waitFor with its own deadline.
func waitForWithin(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func openInstance(t *testing.T, inst *testmysql.Instance) *sql.DB {
	t.Helper()
	db, err := sql.Open("mysql", inst.DSN())
	if err != nil {
		t.Fatal(err)
	}
	// The pool must not outlive mysqld: close it before the instance stops.
	t.Cleanup(func() { db.Close() })
	db.SetMaxIdleConns(0) // the replica restarts during the job
	return db
}

func execSQL(t *testing.T, db *sql.DB, stmts ...string) {
	t.Helper()
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// waitRow waits up to e2eReplicaLag for row id of <database>.t to have
// value v on db.
func waitRow(t *testing.T, db *sql.DB, database string, id int, v string) {
	t.Helper()
	deadline := time.Now().Add(e2eReplicaLag)
	var last string
	for {
		var got string
		err := db.QueryRow("SELECT v FROM `"+database+"`.t WHERE id = ?", id).Scan(&got)
		if err == nil && got == v {
			return
		}
		last = fmt.Sprintf("v=%q err=%v", got, err)
		if time.Now().After(deadline) {
			t.Fatalf("row %d of %s.t did not reach %q on the replica within %s (last: %s)", id, database, v, e2eReplicaLag, last)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func schemaExists(t *testing.T, db *sql.DB, name string) bool {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = ?", name).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n > 0
}
