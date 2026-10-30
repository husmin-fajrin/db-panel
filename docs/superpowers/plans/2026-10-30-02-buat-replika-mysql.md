# Rencana Implementasi 2: Buat Replika MySQL Sekali Klik

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** User menghubungkan MySQL di server master dengan satu akun
admin, menekan "Siapkan sebagai master", lalu "Buat replika" dengan
memilih server tujuan dan database; panel mengorkestrasi agent di kedua
sisi sampai replika tersambung dan sinkron, termasuk memasang MySQL di
server kosong, dan belakangan bisa menambah atau melepas database dari
replika. Dimulai dengan mengamankan bootstrap agent.

**Architecture:** Agent mendapat driver `mysql` di balik antarmuka `Host`
yang menemukan tata letak MySQL apa pun (paket distro, aaPanel, tarball)
dari MySQL dan sistem itu sendiri, sehingga bisa diuji dengan instance
mysqld nyata tanpa root. Operasi panjang (clone, dump, install) berjalan
sebagai job di agent yang memancarkan event progres. Panel punya
orkestrator job (state machine per tahap, persisten di SQLite, bisa
diulang per tahap, berhenti di tahap yang butuh persetujuan manusia) dan
UI job dengan log hidup lewat SSE. Replikasi disaring per database di
sisi replika.

**Tech Stack:** Go 1.26.10, `github.com/go-sql-driver/mysql v1.9.3`
(pure Go), `github.com/google/uuid v1.6.0`; selebihnya seperti Rencana 1.
Tes integrasi memakai `mysqld` lokal (Ubuntu `mysql-server-core-8.0`,
8.0.46) lewat harness `testmysql`.

**Spec:** `docs/superpowers/specs/2026-10-30-db-replica-panel-design.md`
(bagian 4, 6.4, 6.5, 7.3, 7.3a, 7.4, 8, 9.5, 9.8, 9.9, 9.10, 9.12, 10a).
Rencana 1 dan ledger-nya: `docs/superpowers/plans/2026-10-30-01-fondasi-panel-agent.md`,
`docs/superpowers/ledgers/2026-10-30-01-fondasi-panel-agent-ledger.md`
(daftar "deferred to Plan 2").

## Global Constraints

- Go 1.26.10, `CGO_ENABLED=0`, pure Go; modul `github.com/husmin-fajrin/db-panel`.
- Engine: Oracle MySQL **8.0.26+**, 8.4, 9.x. Lebih rendah, MariaDB,
  Percona: ditolak di preflight dengan pesan Indonesia. `RESET MASTER`
  untuk 8.0, `RESET BINARY LOGS AND GTIDS` untuk 8.4+.
- Master dan replika satu seri (8.0/8.0, 8.4/8.4, 9/9). CLONE hanya
  bila semua database dipilih, tujuan kosong, user tidak memaksa dump,
  dan donor/penerima memenuhi syarat (8.0.37+ bebas patch; di bawah itu
  patch sama); selain itu dump database terpilih.
- Versi 1: hanya server replika khusus; replika selalu `read_only=ON`
  dan `super_read_only=ON`; server tujuan yang berisi database yang
  tidak ada di master ditolak.
- Tata letak MySQL tidak diasumsikan: file config, datadir, socket, dan
  cara service dijalankan ditemukan dari `mysqld --verbose --help`,
  `SHOW VARIABLES`, dan sistem (unit systemd, init script, `mysqld_safe`).
  Config ditulis ke `conf.d` bila `!includedir` ada, selain itu blok
  bertanda `# BEGIN dbpanel` / `# END dbpanel` di file config utama.
- Agent hanya menjalankan perintah registry dengan parameter terstruktur;
  SQL dengan placeholder; identifier (nama database/user) divalidasi
  `^[A-Za-z0-9_$]{1,64}$` dan dikutip dengan backtick.
- Akun MySQL admin per server: dikirim ke agent lewat mTLS, disimpan
  terenkripsi di `/etc/dbpanel/mysql.cred` (0600; kunci
  `/etc/dbpanel/secrets.key` 0600; `agent.key` adalah kunci TLS); tidak pernah ke log/argv/file config
  MySQL; mysqldump/mysql memakai `--defaults-extra-file` sementara 0600.
- Password user replikasi dibuat panel (24 karakter acak), disimpan
  terenkripsi di panel (AES-256-GCM, kunci `/etc/dbpanel/panel.key`).
- Panel tidak pernah merestart MySQL master tanpa klik manusia; replika
  boleh direstart otomatis selama belum melayani siapa pun.
- Teks UI dan pesan error untuk user: Indonesia; log: Inggris.
- Setiap task diakhiri `make test` hijau dan commit. Tes yang butuh
  `mysqld` dilewati bila tidak ada, dan WAJIB benar-benar jalan di
  lingkungan pengembangan ini (mysqld 8.0.46 terpasang di `/usr/sbin`).

## Review Focus

1. Master dengan `log_bin=OFF` atau `bind-address=127.0.0.1`: job
   "Siapkan master" berhenti dengan status menunggu, menjelaskan bahwa
   MySQL master perlu restart, dan tidak pernah merestart sendiri
   (Task 12).
2. Server tujuan yang berisi database lain (bukan milik master): job
   berhenti dan menjelaskan bahwa versi ini hanya mendukung server
   replika khusus; tujuan yang berisi database milik master dari
   percobaan sebelumnya minta konfirmasi, tidak menimpa diam-diam
   (Task 7 dan Task 12).
3. Agent replika mati di tengah salinan data: job gagal dengan pesan
   yang menyebut tahap dan bisa diulang dari tahap itu (Task 12).
4. Master tidak bisa dijangkau dari replika (firewall): preflight gagal
   menyebut host dan port yang dicoba (Task 7).
5. Akun admin yang haknya kurang: "Hubungkan MySQL" berhasil login tapi
   menampilkan daftar `GRANT` yang kurang, dan "Siapkan master" menolak
   sampai hak lengkap (Task 6).

---

### Task 1: Bootstrap agent terverifikasi (hash skrip, hash binary, pin CA)

**Files:**
- Modify: `internal/panel/agentapi/install.go`, `web/templates/agent-install.sh.tmpl`,
  `internal/panel/web/handlers_servers.go`, `internal/agent/pair.go`,
  `internal/agent/cli.go`, `internal/panel/servers.go`, `README.md`
- Create: `internal/panel/agentapi/binhash.go`
- Test: `internal/panel/agentapi/install_test.go`, `internal/agent/cli_test.go`,
  `internal/panel/web/web_test.go`, `internal/panel/panel_test.go`

**Interfaces:**
- Consumes: `agentapi.Deps`, `BinaryHandler`, `InstallScriptHandler`, `ca.CA.CAPEM()`, `agent.Pair`.
- Produces:

```go
// package agentapi
type BinHashes struct{ /* cache arch→sha256 hex, invalidated by size+mtime */ }
func NewBinHashes(binDir string) *BinHashes
func (b *BinHashes) For(arch string) (sha256hex string, err error)   // binDir/dbpanel_linux_<arch>, atau os.Executable() bila arch == runtime.GOARCH; error "binary untuk arsitektur ini belum tersedia di panel"
func CAFingerprint(caPEM []byte) string            // sha256 hex DER sertifikat CA
type InstallScriptParams struct{ PanelURL, Token, AgentAddr, CASHA256 string; BinSHA256 map[string]string }
func RenderInstallScript(p InstallScriptParams) ([]byte, error)      // deterministik; dipakai handler dan UI
func InstallScriptHandler(d Deps, publicURL func() string, hashes *BinHashes) http.Handler
// package agent
type PairOptions struct{ CASHA256 string; AgentAddr string }
func Pair(ctx context.Context, panelURL, token, dir string, client *http.Client, opts PairOptions) (config.Config, error)
//   CASHA256 != "" → cocokkan CAFingerprint(resp.CAPEM), error "CA panel tidak cocok dengan yang diharapkan"; AgentAddr != "" menimpa resp.AgentAddr
// CLI: agent pair --panel URL --ca-sha256 HEX --agent-addr HOST:PORT  (token dari DBPANEL_TOKEN/stdin); --ca-sha256 wajib kecuali --insecure-skip-ca-pin (cetak peringatan)
```

Perintah pairing di UI (satu baris):
```
curl -fsSL -K - <<<'url = "<PublicURL>/agent/install?token=<T>"' -o /tmp/dbpanel-agent.sh && echo '<SCRIPT_SHA256>  /tmp/dbpanel-agent.sh' | sha256sum -c - && sudo bash /tmp/dbpanel-agent.sh
```
`SCRIPT_SHA256` = sha256 hasil `RenderInstallScript` dengan parameter yang sama yang dipakai handler. Skrip memverifikasi binary (`sha256sum -c`, die `Checksum binary agent tidak cocok; kemungkinan ada yang menyusup di jaringan`) dan memanggil `agent pair --ca-sha256 … --agent-addr …`.

- [ ] **Step 1: Tulis test**: `TestRenderInstallScriptDeterministic`, `TestBinHashesForOwnArch`, `TestPairRejectsCAMismatch`, `TestPairPinsAgentAddr`, `TestPairingCommandContainsScriptHash` (hash di perintah == sha256 body `GET /agent/install?token=`), `TestCLIPairRequiresCAPin`.
- [ ] **Step 2: Jalankan, pastikan gagal.** **Step 3: Implementasi.** **Step 4: Lulus** + uji manual: ubah satu byte fingerprint di skrip → pairing ditolak.
- [ ] **Step 5: Commit** — `git commit -m "feat(security): verified agent bootstrap (script, binary and CA pinning)"`

---

### Task 2: Rapikan seam dari Rencana 1

**Files:**
- Create: `internal/proto/pair.go`, `internal/pki/agent.go`, `internal/secrets/box.go`
- Modify: `internal/agent/pair.go`, `internal/agent/runtime.go`, `internal/panel/agentapi/pair.go`,
  `internal/panel/agentapi/ws.go`, `internal/panel/ca/ca.go`, `internal/panel/hub/hub.go`,
  `internal/panel/hub/conn_local.go`, `internal/panel/localagent.go`, `internal/panel/servers.go`,
  `internal/panel/listener_impl.go`, `internal/proto/messages.go`, `internal/panel/store/migrate.go`
- Test: suite lama tetap hijau; tambah `internal/panel/store/migrate_test.go`, `internal/panel/hub/hub_test.go`, `internal/secrets/box_test.go`

**Interfaces:**
- Produces:

```go
// proto/pair.go: PairRequest, PairResponse dipindah dari package agent (field dan tag sama).
// pki/agent.go: PanelServerName, NewAgentKeyAndCSR, AgentClientTLS dipindah dari ca (signature sama).
// hub:
func (h *Hub) OnEvent(serverID int64, ev proto.Event)   // meneruskan ke Listener.OnEvent; dipakai agentapi dan localagent; Deps.OnEvent dihapus
// proto:
func (e *Error) Is(target error) bool                   // target *Error dengan Code sama
var ErrOffline = &Error{Code: CodeOffline, Message: "koneksi agent terputus"}   // hub.ErrOffline = proto.ErrOffline
// store/migrate.go:
var ErrNewerSchema = errors.New("database dibuat oleh versi dbpanel yang lebih baru")
// secrets (dipakai panel dan agent):
type Box struct{ /* key [32]byte */ }
func LoadOrCreate(path string) (*Box, error)        // 32 byte acak, 0600, O_EXCL; error bila mode lebih longgar
func (b *Box) Seal(plain []byte) ([]byte, error)    // AES-256-GCM, nonce 12 byte di depan
func (b *Box) Open(ct []byte) ([]byte, error)
```

- [ ] **Step 1: Tulis test**: `TestOpenRefusesNewerSchema`, `TestHubOnEventForwards`, `TestErrorsIsOffline`, `TestBoxSealOpenRoundTrip`, `TestBoxRejectsTamper`, `TestBoxRefusesLoosePerms`.
- [ ] **Step 2: Jalankan, pastikan gagal.** **Step 3: Refactor** tanpa perubahan perilaku; tidak ada import `internal/panel/ca` dari `internal/agent`, tidak ada import `internal/agent` dari `internal/panel/agentapi`.
- [ ] **Step 4: `make test` dan `go test -race ./internal/...`.**
- [ ] **Step 5: Commit** — `git commit -m "refactor: wire types in proto, pki and secrets packages, single event path, offline error, schema guard"`

---

### Task 3: Store: inventaris MySQL, topologi, replika, job

**Files:**
- Create: `internal/panel/store/migrations/0003_mysql_topologies_jobs.sql`,
  `internal/panel/store/inventory.go`, `internal/panel/store/topologies.go`, `internal/panel/store/jobs.go`
- Test: `internal/panel/store/inventory_test.go`, `internal/panel/store/topologies_test.go`, `internal/panel/store/jobs_test.go`

**Interfaces:**
- Produces:

```go
type Inventory struct { ServerID int64; Connected bool; Version, Series string; Databases []proto.DatabaseInfo; MissingGrants []string; MasterReady bool; PreflightJSON string; UpdatedAt time.Time }   // proto.DatabaseInfo didefinisikan di Task 6; untuk Task 3 definisikan dulu di proto/mysql.go: Name string, Bytes uint64, Tables int, Engines []string (tag snake_case)
func (s *Store) SetInventory(inv Inventory) error
func (s *Store) GetInventory(serverID int64) (Inventory, error)   // ErrNotFound

type Topology struct { ID, MasterServerID int64; ReplUser string; ReplPasswordEnc []byte; SourceHost string; SourcePort int; CreatedAt time.Time }
func (s *Store) CreateTopology(masterServerID int64, replUser string, replPasswordEnc []byte, sourceHost string, sourcePort int) (Topology, error)   // SetServerRole(master); error bila server sudah replika
func (s *Store) GetTopology(id int64) (Topology, error)
func (s *Store) GetTopologyByMaster(serverID int64) (Topology, error)
func (s *Store) ListTopologies() ([]Topology, error)
const ( ReplicaCreating = "creating"; ReplicaReady = "ready"; ReplicaFailed = "failed" )
type Replica struct { ID, TopologyID, ServerID int64; Databases []string /* kosong = semua */; State, LastError string; CreatedAt time.Time }
func (s *Store) AddReplica(topologyID, serverID int64, databases []string) (Replica, error)   // SetServerRole(replica); error bila sudah master/replika
func (s *Store) SetReplicaDatabases(serverID int64, databases []string) error
func (s *Store) SetReplicaState(serverID int64, state, lastError string) error
func (s *Store) ListReplicas(topologyID int64) ([]Replica, error)
func (s *Store) GetReplicaByServer(serverID int64) (Replica, error)
func (s *Store) RemoveReplica(serverID int64) error    // role → unassigned
func (s *Store) SetServerRole(id int64, role string) error

const ( JobQueued = "queued"; JobRunning = "running"; JobWaiting = "waiting"; JobSucceeded = "succeeded"; JobFailed = "failed"; JobCanceled = "canceled" )
const ( StepPending = "pending"; StepRunning = "running"; StepWaiting = "waiting"; StepDone = "done"; StepFailed = "failed"; StepSkipped = "skipped" )
type Job struct { ID, Kind string; ServerID, TopologyID int64; Status string; CurrentStep int; Error string; ParamsJSON string; CreatedAt time.Time; StartedAt, FinishedAt *time.Time }
type JobStep struct { JobID string; Idx int; Name, Status, Message string; StartedAt, FinishedAt *time.Time }
type JobLogLine struct { ID int64; JobID string; TS time.Time; Level, Message string }
func (s *Store) CreateJob(j Job, stepNames []string) error
func (s *Store) GetJob(id string) (Job, []JobStep, error)
func (s *Store) ListJobs(limit int) ([]Job, error)
func (s *Store) ListJobsForServer(serverID int64, limit int) ([]Job, error)
func (s *Store) RunningJobs() ([]Job, error)                          // running|waiting
func (s *Store) ActiveJobForServer(serverID int64) (Job, bool, error)
func (s *Store) UpdateJob(id, status string, currentStep int, errMsg string, now time.Time) error
func (s *Store) SetStep(jobID string, idx int, status, message string, now time.Time) error
func (s *Store) AppendJobLog(jobID, level, message string, now time.Time) (int64, error)
func (s *Store) JobLog(jobID string, afterID int64, limit int) ([]JobLogLine, error)
func (s *Store) PruneJobLogs(olderThan time.Time) (int64, error)
```

Skema 0003: `mysql_inventory(server_id PK FK CASCADE, connected, version, series, databases_json, missing_grants_json, master_ready, preflight_json, updated_at)`, `topologies(id, master_server_id UNIQUE FK, repl_user, repl_password_enc BLOB, source_host, source_port, created_at)`, `replicas(id, topology_id FK CASCADE, server_id UNIQUE FK CASCADE, databases_json, state, last_error, created_at)`, `jobs(...)`, `job_steps(...)`, `job_log(...)` seperti draf sebelumnya (`job_id, id` terindeks).

- [ ] **Step 1: Tulis test**: `TestInventorySetGet` (JSON database bolak-balik), `TestTopologyLifecycle`, `TestReplicaDatabasesEmptyMeansAll`, `TestCreateTopologyRejectsReplicaServer`, `TestJobLifecycle`, `TestActiveJobForServer`, `TestPruneJobLogs`, `TestDeleteServerCascadesInventoryReplicaJobs`.
- [ ] **Step 2–4.** `go get github.com/google/uuid@v1.6.0`. **Step 5: Commit** — `git commit -m "feat(store): mysql inventory, topologies, replicas, jobs"`

---

### Task 4: Job runner di agent

**Files:**
- Create: `internal/proto/jobs.go`, `internal/agent/jobs/runner.go`
- Test: `internal/agent/jobs/runner_test.go`

**Interfaces:**
- Produces:

```go
// proto/jobs.go (tag JSON snake_case)
const ( EventJobProgress = "job.progress"; EventJobLog = "job.log"; EventJobDone = "job.done" )
type JobProgress struct { JobID string; Percent int; Message string }
type JobLog      struct { JobID string; TS time.Time; Level string; Message string }
type JobDone     struct { JobID string; OK bool; Error string; Result json.RawMessage }
type JobStatus   struct { ID, Kind, State string /* running|succeeded|failed|canceled */; Percent int; Message, Error string; Result json.RawMessage; StartedAt time.Time; FinishedAt *time.Time }
type StartedJob  struct { JobID string }
type JobIDParams struct { JobID string }
type JobLogParams struct { JobID string; After int }
type JobLogResult struct { Lines []JobLog; Next int }

// package jobs
type Emitter interface { Emit(ev proto.Event) error }
type Job struct{ /* ID, Kind, ctx, cancel, ring log 2000 baris */ }
func (j *Job) ID() string
func (j *Job) Context() context.Context
func (j *Job) Progress(percent int, message string)      // dibatasi 1/detik kecuali perubahan ≥ 5%
func (j *Job) Logf(level, format string, args ...any)     // info|warn|error
type Func func(j *Job) (result any, err error)
var ErrBusy = errors.New("agent sedang menjalankan pekerjaan lain")
type Runner struct{ /* satu job aktif; riwayat 20 */ }
func NewRunner(em Emitter, logger *slog.Logger) *Runner
func (r *Runner) Start(kind string, fn Func) (id string, err error)   // id 16 byte hex; job.done saat selesai; panic → err
func (r *Runner) Status(id string) (proto.JobStatus, bool)
func (r *Runner) Cancel(id string) bool
func (r *Runner) Logs(id string, after int) (proto.JobLogResult, bool)
func (r *Runner) Register(reg *commands.Registry)    // "job.status", "job.cancel", "job.log"; id tak dikenal → ParamError "job tidak dikenal"
```

- [ ] **Step 1: Tulis test**: `TestStartRunsAndEmitsDone`, `TestStartRejectsSecondJob`, `TestCancelStopsJob`, `TestPanicBecomesFailure`, `TestProgressThrottled`, `TestLogsPagination`, `TestRegisterCommands`, `TestEmitOfflineDoesNotStopJob`.
- [ ] **Step 2–4** (`-race`). **Step 5: Commit** — `git commit -m "feat(agent): job runner with progress events"`

---

### Task 5: Fondasi driver MySQL: penemuan tata letak, Host, dialek, status, harness testmysql

**Files:**
- Create: `internal/proto/mysql.go`, `internal/agent/drivers/mysql/host.go`,
  `internal/agent/drivers/mysql/layout.go`, `internal/agent/drivers/mysql/realhost.go`,
  `internal/agent/drivers/mysql/service.go`, `internal/agent/drivers/mysql/dialect.go`,
  `internal/agent/drivers/mysql/conn.go`, `internal/agent/drivers/mysql/status.go`,
  `internal/agent/drivers/mysql/driver.go`,
  `internal/agent/drivers/mysql/testmysql/instance.go`, `internal/agent/drivers/mysql/testmysql/host.go`
- Modify: `internal/agent/cli.go` (daftarkan driver + job runner)
- Test: `internal/agent/drivers/mysql/layout_test.go`, `dialect_test.go`, `service_test.go`, `status_test.go`

**Interfaces:**
- Produces:

```go
// proto/mysql.go (tag snake_case; di dalam package proto tanpa prefix)
type MySQLStatus struct {
    Version, Series string; ServerID uint32; LogBin bool; GTIDMode string; EnforceGTID bool; BinlogFormat string
    ReadOnly, SuperReadOnly bool; GTIDExecuted string; DataDir, Socket string; Port int; BindAddress string
    Replica *ReplicaStatus; UptimeSec int64; ThreadsRunning int; BinlogDumpThreads int
}
type ReplicaStatus struct { IORunning, SQLRunning string; SourceHost string; SourcePort int; LastIOError, LastSQLError string; LastIOErrno, LastSQLErrno int; SecondsBehind *int64; RetrievedGTID, ExecutedGTID string; Filters []string /* REPLICATE_WILD_DO_TABLE */ }

// package mysql
type Layout struct { MysqldPath, ConfigFile string; ConfigFiles []string /* urutan baca */; IncludeDir string /* "" bila tidak ada !includedir */; DataDir, Socket, PIDFile string; Port int; ServiceKind string /* systemd|initscript|mysqld_safe|unknown */; ServiceName string /* unit atau skrip */ }
func DiscoverLayout(ctx context.Context, sys probe.System) (Layout, error)
//  mysqld: LookPath lalu /usr/sbin, /usr/libexec, /www/server/mysql/bin, /usr/local/mysql/bin; `mysqld --verbose --help` → "Default options are read from the following files" + port/socket/datadir/pid-file; ConfigFile = file pertama yang ada; IncludeDir dari baris `!includedir` di file itu; ServiceKind: `systemctl list-unit-files` mengandung mysql(d).service → systemd (nama unit yang aktif/ada); else /etc/init.d/mysqld|mysql ada → initscript; else proses mysqld_safe berjalan → mysqld_safe; else unknown
type ServiceController interface { Start(ctx) error; Stop(ctx) error; Restart(ctx) error; IsActive(ctx) (bool, error); WaitReady(ctx context.Context, timeout time.Duration) error }
func NewServiceController(l Layout, sys probe.System, ready func(ctx) error) ServiceController   // systemd → systemctl; initscript → /etc/init.d/<n> start|stop|restart; mysqld_safe → kill pid + mysqld_safe --defaults-file=... &; unknown → error "cara menjalankan service MySQL tidak dikenali"
type Host interface {
    Layout() Layout
    Open(ctx context.Context) (*sql.DB, error)              // akun admin tersimpan (Task 6); bila belum ada: root via socket
    Service() ServiceController
    WriteConfig(content string) (path string, err error)    // IncludeDir/zz-dbpanel.cnf atau blok bertanda di ConfigFile (ganti blok lama)
    ValidateConfig(ctx context.Context) error               // mysqld --validate-config (dengan --defaults-file=ConfigFile)
    DiskFree(path string) (free, total uint64, err error)
    Exec(ctx context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error
    TCPReachable(ctx context.Context, host string, port int, timeout time.Duration) error
    OSFamily() string
    TempFile(pattern string, mode os.FileMode) (*os.File, error)
}
func NewRealHost(cfg config.Config, creds CredentialStore, sys probe.System, logger *slog.Logger) (Host, error)   // CredentialStore dari Task 6; di task ini antarmuka nil-safe
type Dialect struct { Version string; Major, Minor, Patch int; Series string }
func ParseVersion(v string) (Dialect, error)          // 8.0.46-0ubuntu… → 8.0; 8.4.3 → 8.4; 9.1.0 → 9; MariaDB/5.7/8.0.<26 → ErrUnsupported
var ErrUnsupported = errors.New("versi MySQL tidak didukung (butuh Oracle MySQL 8.0.26 ke atas)")
func (d Dialect) ResetBinaryLogs() string; func (d Dialect) CloneNeedsExactPatch() bool; func (d Dialect) SameSeries(o Dialect) bool
func Status(ctx context.Context, db *sql.DB) (proto.MySQLStatus, error)
type Driver struct{ Host Host; Jobs *jobs.Runner; Logger *slog.Logger }
func (d *Driver) Register(reg *commands.Registry)     // "mysql.status" di task ini
```

`testmysql`: seperti draf (Start/Restart/DSN/Host; t.Skip bila mysqld tidak ada; dir dasar `$DBPANEL_TEST_MYSQL_DIR` atau `/var/tmp/dbpanel-test-<pid>`; `--user=$DBPANEL_TEST_MYSQL_USER` (default `nobody`) bila uid 0; `--secure-file-priv`; GTID ON; supervisor kecil me-restart proses saat keluar dengan exit code 16 atau setelah CLONE; `Host()` memakai Layout buatan: ConfigFile = `<dir>/my.cnf` tanpa includedir sehingga jalur blok bertanda teruji, ServiceKind "test").

- [ ] **Step 1: Tulis test**: `TestDiscoverLayoutDebianPackage` (fake System: mysqld di /usr/sbin, help menyebut /etc/mysql/my.cnf dengan `!includedir /etc/mysql/conf.d/`, unit mysql.service → IncludeDir terisi, ServiceKind systemd), `TestDiscoverLayoutAAPanel` (mysqld di /www/server/mysql/bin, /etc/my.cnf tanpa includedir, /etc/init.d/mysqld → initscript), `TestDiscoverLayoutUnknownService`, `TestWriteConfigManagedBlockReplaces` (dua kali tulis → satu blok), `TestParseVersion` (tabel), `TestResetBinaryLogsByVersion`, `TestStatusAgainstInstance` (Version, LogBin, GTIDMode ON, BinlogDumpThreads 0, Replica nil).
- [ ] **Step 2–3.** `go get github.com/go-sql-driver/mysql@v1.9.3`. **Step 4: Jalankan** `go test ./internal/agent/drivers/mysql/...` di lingkungan ini: harus menjalankan tes instance, bukan skip.
- [ ] **Step 5: Commit** — `git commit -m "feat(mysql): layout discovery, host, dialect, status, testmysql harness"`

---

### Task 6: Hubungkan MySQL: kredensial admin, inventaris, pemeriksaan hak

**Files:**
- Create: `internal/agent/drivers/mysql/credentials.go`, `internal/agent/drivers/mysql/connect.go`
- Modify: `internal/proto/mysql.go`, `internal/agent/cli.go` (buat `secrets.Box` dari `/etc/dbpanel/secrets.key`)
- Test: `internal/agent/drivers/mysql/credentials_test.go`, `connect_test.go`

**Interfaces:**
- Produces:

```go
// proto/mysql.go
type ConnectParams struct { User string; Password string; Host string /* default "localhost" */; Port int /* 0 = socket */ }
type ConnectResult struct { Status MySQLStatus; Databases []DatabaseInfo /* tanpa mysql, sys, information_schema, performance_schema */; MissingGrants []string /* pernyataan GRANT yang kurang, kosong = cukup */; Account string /* 'user'@'host' efektif */ }
type DatabaseInfo struct { Name string; Bytes uint64; Tables int; Engines []string }   // sudah ada sejak Task 3
// package mysql
type CredentialStore interface { Load() (proto.ConnectParams, bool, error); Save(proto.ConnectParams) error; Clear() error }
func NewFileCredentialStore(path string, box *secrets.Box) CredentialStore   // /etc/dbpanel/mysql.cred, JSON tersegel, 0600
// perintah "mysql.connect": uji login (Open dengan kredensial baru), ambil Status, daftar database, cek hak; bila berhasil Save; gagal login → error "login MySQL gagal: <errno> <pesan>" tanpa menyimpan
// perintah "mysql.inventory": pakai kredensial tersimpan, hasil sama dengan connect; tanpa kredensial → error "MySQL belum dihubungkan"
func RequiredGrants() []string   // daftar hak yang diperiksa lewat SHOW GRANTS: ALL PRIVILEGES atau gabungan {SUPER | (SYSTEM_VARIABLES_ADMIN, REPLICATION_SLAVE_ADMIN, BACKUP_ADMIN, CLONE_ADMIN, PERSIST_RO_VARIABLES_ADMIN)}, CREATE USER, RELOAD, PROCESS, SELECT/LOCK TABLES/SHOW VIEW/TRIGGER/EVENT ON *.*, REPLICATION CLIENT, dan WITH GRANT OPTION untuk membuat user replikasi; MissingGrants berisi kalimat `GRANT <hak> ON *.* TO '<user>'@'<host>'`
```

- [ ] **Step 1: Tulis test** (testmysql): `TestConnectSavesAndLists` (buat database `demo` → ada di Databases dengan Bytes > 0; file cred 0600 dan tersegel), `TestConnectWrongPasswordDoesNotSave`, `TestConnectReportsMissingGrants` (user dengan hak terbatas → MissingGrants berisi `GRANT ... REPLICATION_SLAVE_ADMIN`), `TestInventoryWithoutCredentialsFails`.
- [ ] **Step 2–4.** **Step 5: Commit** — `git commit -m "feat(mysql): connect with admin credentials, inventory, grant check"`

---

### Task 7: Preflight master dan replika

**Files:**
- Create: `internal/agent/drivers/mysql/preflight.go`
- Modify: `internal/proto/mysql.go`
- Test: `internal/agent/drivers/mysql/preflight_test.go`

**Interfaces:**
- Produces:

```go
type PreflightParams struct { Role string /* master|replica */; PeerHost string; PeerPort int; PeerVersion string; PeerDataBytes uint64; PeerDatabases []string; ReplUser string; Databases []string /* yang akan direplikasi; kosong = semua */; ConfirmWipe bool }
const ( CheckOK = "ok"; CheckWarn = "warn"; CheckFail = "fail"; CheckNeedsRestart = "needs_restart"; CheckNeedsConfirm = "needs_confirm" )
type Check struct { ID, Status, Title, Detail string; FixSQL []string; FixConfig string }
type PreflightResult struct { Status MySQLStatus; Checks []Check; DataBytes uint64; Worst string; Method string /* clone|dump|"" (hanya replica) */ }
// perintah "mysql.preflight"
```

Check master: `grants` (MissingGrants dari Task 6 → fail), `version`, `server_id` (0 → fail+FixSQL; diperbaiki Task 8), `log_bin` (OFF → needs_restart), `binlog_format` (≠ROW → fail+FixSQL PERSIST), `gtid_mode`/`enforce_gtid_consistency` (≠ON → warn "akan dinaikkan bertahap"), `clone_plugin` (warn bila tidak aktif), `bind_address` (lokal → needs_restart, FixConfig `bind-address=0.0.0.0`), `repl_user` (warn "akan dibuat"), `non_innodb` (tabel non-InnoDB di database terpilih → fail, Detail maks 20), `existing_replication` (BinlogDumpThreads>0 atau IsReplica → warn; dengan perubahan gtid_mode → fail), `data_size` (jumlah database terpilih).
Check replica: `version` + `same_series` (fail), `reachable` (TCP ke PeerHost:PeerPort 5 s → fail `tidak bisa menjangkau <host>:<port> dari server ini; periksa firewall dan bind-address master`), `disk` (free < PeerDataBytes×1.2 → fail dengan GB), `dedicated` (database lokal yang TIDAK ada di PeerDatabases → fail `server ini masih berisi database lain (<daftar>); versi ini hanya mendukung server replika khusus`), `leftover` (database lokal yang ada di PeerDatabases → needs_confirm `database dari percobaan sebelumnya akan dibuang: <daftar>`; ConfirmWipe → ok), `not_master` (BinlogDumpThreads>0 → fail), `clone_patch` (warn → Method dump). `Method` = clone bila Databases kosong, tidak ada database lokal, tidak ada clone_patch warn; selain itu dump.

- [ ] **Step 1: Tulis test**: `TestPreflightMasterHealthy`, `TestPreflightMasterNonInnoDBOnlyInSelectedDBs` (tabel MyISAM di db yang tidak dipilih → tidak fail), `TestPreflightMasterBindLocalhost`, `TestPreflightMasterMissingGrantsFails`, `TestPreflightReplicaUnreachable`, `TestPreflightReplicaNotDedicated` (db `lain` tidak ada di master → fail `dedicated`), `TestPreflightReplicaLeftoverNeedsConfirm`, `TestPreflightReplicaMethodDumpWhenSubset`, `TestPreflightRejectsMariaDBVersion`.
- [ ] **Step 2–4.** **Step 5: Commit** — `git commit -m "feat(mysql): preflight checks for master and replica"`

---

### Task 8: Konfigurasi, user replikasi, restart, read-only

**Files:**
- Create: `internal/agent/drivers/mysql/configure.go`, `internal/agent/drivers/mysql/users.go`
- Modify: `internal/proto/mysql.go`
- Test: `internal/agent/drivers/mysql/configure_test.go`, `users_test.go`

**Interfaces:**
- Produces:

```go
type ConfigureParams struct { Role string; ServerID uint32 /* 0 = acak */; EnableClone bool; GTID bool }
type ConfigureResult struct { Applied []string; NeedsRestart bool; RestartReasons []string; ConfigPath string; ServerID uint32 }
type ReplUserParams struct { User string; Password string; FromHost string }
type ReadOnlyParams struct { On bool }
type RestartResult struct { TookMs int64 }
// perintah: "mysql.configure", "mysql.restart", "mysql.createReplUser", "mysql.setReadOnly"
```

Aturan seperti draf: PERSIST server_id/binlog_format/enforce_gtid; gtid_mode bertahap; INSTALL PLUGIN clone; `WriteConfig` blok `[mysqld]` (log_bin=binlog, binlog_expire_logs_seconds=604800, gtid_mode=ON, enforce_gtid_consistency=ON, server_id, binlog_format=ROW; replica: read_only, super_read_only, relay_log=relay, log_replica_updates=ON); `ValidateConfig`; NeedsRestart bila log_bin OFF / bind lokal (master) / replica. `Restart`: Service().Restart + WaitReady 120 s. `CreateReplUser` idempoten dengan `GRANT REPLICATION SLAVE, BACKUP_ADMIN`. `SetReadOnly` PERSIST read_only+super_read_only.

- [ ] **Step 1: Tulis test** (testmysql): `TestConfigureMasterSetsGTIDAndWritesBlock` (instance gtid OFF → ON; file my.cnf berisi blok bertanda; validate ok), `TestConfigureReportsRestartWhenLogBinOff`, `TestRestartAppliesReplicaConfig` (super_read_only ON setelah restart), `TestCreateReplUserIdempotent`, `TestReplUserRejectsBadIdentifier`.
- [ ] **Step 2–4.** **Step 5: Commit** — `git commit -m "feat(mysql): configure, replication user, restart, read-only"`

---

### Task 9: Snapshot: CLONE dan dump per database

**Files:**
- Create: `internal/agent/drivers/mysql/clone.go`, `internal/agent/drivers/mysql/dump.go`
- Modify: `internal/proto/mysql.go`
- Test: `internal/agent/drivers/mysql/snapshot_test.go`

**Interfaces:**
- Produces:

```go
type SnapshotParams struct { Host string; Port int; User, Password string; Databases []string /* dump saja; kosong = semua database user */; EstimateBytes uint64; DropExisting bool /* dump: DROP DATABASE lokal yang ada di Databases sebelum muat */ }
type SnapshotResult struct { Method string; Bytes uint64; GTIDExecuted string /* clone: @@gtid_executed; dump: gtid set dari --set-gtid-purged */; TookMs int64 }
// job "mysql.cloneFrom", job "mysql.dumpFrom" → StartedJob; job.done.Result = SnapshotResult
```

`cloneFrom` seperti draf (INSTALL PLUGIN, donor list, CLONE di goroutine, progress dari `clone_progress`, tangani 3707/putus → WaitReady, cek `clone_status` Completed). `dumpFrom`: `STOP REPLICA` bila ada; `Dialect.ResetBinaryLogs()` lokal; defaults-extra-file 0600 untuk sumber dan lokal; `mysqldump --single-transaction --set-gtid-purged=ON --triggers --routines --events --skip-lock-tables --column-statistics=0` + (`--all-databases` bila kosong, else `--databases <daftar>`); pipa ke `mysql --init-command="SET sql_log_bin=0"`; bila DropExisting: `DROP DATABASE IF EXISTS` tiap database terpilih dulu; progress byte/EstimateBytes; hasil GTIDExecuted dari `@@gtid_executed` lokal setelah muat (gtid_purged terpasang oleh dump).

- [ ] **Step 1: Tulis test** (dua instance): `TestCloneFromReplicatesData`, `TestCloneFailsWithWrongPassword`, `TestDumpSelectedDatabasesOnly` (master db a,b; Databases [a] → replika punya a, tidak b; `@@gtid_executed` replika == master saat dump), `TestDumpAllDatabases`, `TestDumpDropExistingReplaces`, `TestSnapshotRefusesWhileReplicaRunning`.
- [ ] **Step 2–4.** **Step 5: Commit** — `git commit -m "feat(mysql): clone and per-database dump snapshot jobs"`

---

### Task 10: Kendali replikasi dengan filter

**Files:**
- Create: `internal/agent/drivers/mysql/replication.go`
- Modify: `internal/proto/mysql.go`
- Test: `internal/agent/drivers/mysql/replication_test.go`

**Interfaces:**
- Produces:

```go
type ReplicationSourceParams struct { Host string; Port int; User, Password string; SSL bool; Databases []string /* filter; kosong = tanpa filter */ }
type ReplicationFilterParams struct { Databases []string }
type WaitSyncedParams struct { GTIDSet string; TimeoutSec int }
type WaitSyncedResult struct { Synced bool; Executed string; Status ReplicaStatus }
type StartUntilParams struct { GTIDSet string; TimeoutSec int }
// perintah: "mysql.configureReplication" (STOP REPLICA; CHANGE REPLICATION FILTER REPLICATE_WILD_DO_TABLE=('db.%',…) atau () ; CHANGE REPLICATION SOURCE TO … AUTO_POSITION=1, GET_SOURCE_PUBLIC_KEY=1, SOURCE_CONNECT_RETRY=10, SOURCE_RETRY_COUNT=86400 [, SOURCE_SSL=1]),
//           "mysql.setReplicationFilter" (STOP; CHANGE REPLICATION FILTER; START), "mysql.startReplica", "mysql.stopReplica", "mysql.resetReplica" (STOP; RESET REPLICA ALL; filter kosong),
//           "mysql.waitSynced" (WAIT_FOR_EXECUTED_GTID_SET), "mysql.startUntil" (START REPLICA UNTIL SQL_AFTER_GTIDS=?; tunggu SQL thread berhenti ≤ timeout)
```

- [ ] **Step 1: Tulis test** (dua instance): `TestFilteredReplicationAppliesOnlySelected` (filter [a]; insert ke a dan b di master → replika punya a baru, b tidak ada; GTID executed tetap lengkap), `TestConfigureStartWaitSynced`, `TestSetReplicationFilterExpands`, `TestStartUntilStopsAtGTID`, `TestResetReplicaClears`, `TestWaitSyncedTimesOut`.
- [ ] **Step 2–4.** **Step 5: Commit** — `git commit -m "feat(mysql): replication control with per-database filters"`

---

### Task 11: Pasang MySQL di server kosong

Seperti draf (debian distro bila seri cocok, selain itu repo resmi; rhel repo resmi; auth_socket root; verifikasi versi), dengan tambahan: setelah pasang, agent membuat akun admin lokal `dbpanel_admin`@localhost dengan password acak, menyimpannya lewat `CredentialStore` (Task 6), sehingga replika hasil pasang otomatis "terhubung" tanpa input user. Tes dengan fake Host seperti draf plus `TestInstallCreatesAdminCredential`.

- [ ] **Step 1–4.** **Step 5: Commit** — `git commit -m "feat(mysql): install MySQL on a bare server"`

---

### Task 12: Orkestrator panel: job "Siapkan master" dan "Buat replika"

**Files:**
- Create: `internal/panel/orchestrator/runner.go`, `internal/panel/orchestrator/prepare_master.go`,
  `internal/panel/orchestrator/create_replica.go`, `internal/panel/orchestrator/agentjob.go`,
  `internal/panel/orchestrator/fake_test.go`
- Modify: `internal/panel/panel.go` (Box dari `ConfigDir/panel.key`, Runner, `Resume`), `internal/panel/listener_impl.go` (OnEvent → `Runner.HandleEvent`)
- Test: `internal/panel/orchestrator/prepare_master_test.go`, `create_replica_test.go`

**Interfaces:**
- Produces:

```go
type Deps struct { Store *store.Store; Hub *hub.Hub; Secrets *secrets.Box; Publish func(event string, data any); Logger *slog.Logger; Now func() time.Time }
type Runner struct{}
func New(d Deps) *Runner
func (r *Runner) Connect(ctx context.Context, serverID int64, p proto.ConnectParams) (proto.ConnectResult, error)   // sinkron: hub.Call mysql.connect, simpan Inventory (Connected, versi, database, MissingGrants)
func (r *Runner) RefreshInventory(ctx context.Context, serverID int64) (proto.ConnectResult, error)
func (r *Runner) StartPrepareMaster(ctx context.Context, serverID int64) (jobID string, err error)   // menolak bila inventory tidak Connected atau MissingGrants tidak kosong (pesan menyebut GRANT)
type CreateReplicaParams struct { MasterServerID, TargetServerID int64; Databases []string; ForceDump bool; ConfirmWipe bool }
func (r *Runner) StartCreateReplica(ctx context.Context, p CreateReplicaParams) (jobID string, err error)   // menolak bila master belum MasterReady, target sudah berperan, master == target, atau ada job aktif untuk target
func (r *Runner) Approve(jobID string) error; Retry(jobID string) error; Cancel(jobID string) error
func (r *Runner) HandleEvent(serverID int64, ev proto.Event)
func (r *Runner) Resume(ctx context.Context)
```

Job `prepare_master` (tahap): 0 `preflight` "Periksa master" (mysql.preflight role master, ReplUser `dbpanel_repl`); 1 `configure` "Terapkan pengaturan" (mysql.configure master; buat topologi + password repl tersegel bila belum ada; mysql.createReplUser FromHost `%` — ruling: `%` karena alamat replika belum diketahui saat master disiapkan; keamanan dijaga oleh password 24 karakter + hak hanya REPLICATION SLAVE/BACKUP_ADMIN); 2 `restart` "Mulai ulang MySQL" (waiting bila NeedsRestart dengan pesan alasan + "Aplikasi yang memakai database ini akan terputus beberapa detik"; Approve → mysql.restart; else skipped); 3 `verify` "Verifikasi" (mysql.status: log_bin ON, gtid ON, ROW → Inventory.MasterReady=true).
Job `create_replica` (tahap): 0 `preflight_master` (cepat: status master masih siap; ambil PeerDatabases dari inventory, DataBytes database terpilih); 1 `preflight_replica` (probe target; bila MySQL tidak terpasang → tandai tahap 2; else mysql.preflight role replica dengan PeerDatabases/Databases/ConfirmWipe; `leftover` → waiting "database dari percobaan sebelumnya akan dibuang: …" sampai Approve (=ConfirmWipe); `dedicated` fail); 2 `install_mysql` (skipped/job agent); 3 `prepare_replica` (configure replica → restart otomatis → setReadOnly on); 4 `snapshot` "Salin data" (Method dari preflight; ForceDump → dump; **bila clone: waiting** dengan pesan "CLONE akan menghapus SEMUA database di <server>. Lanjutkan?" sampai Approve, kecuali ConfirmWipe sudah true dan tidak ada database lokal); 5 `connect` (configureReplication dengan Databases + startReplica); 6 `verify` (waitSynced terhadap gtid master; ready). Kegagalan panggilan agent → step failed dengan nama tahap + errno; agent offline > 2 menit saat menunggu job agent → failed `agent <server> terputus saat <tahap>`. Publish `job_update`, `job_log`.

- [ ] **Step 1: Tulis test** dengan agent palsu (registry skrip per perintah + `hub.OnEvent`): `TestPrepareMasterHappyPath` (MasterReady true, topologi dibuat, password tersegel), `TestPrepareMasterWaitsForRestartApproval` (tidak ada mysql.restart sebelum Approve), `TestPrepareMasterRefusesMissingGrants`, `TestCreateReplicaHappyPathClone` (semua db, tujuan kosong → metode clone, waiting konfirmasi CLONE, Approve → lanjut, 7 tahap done, replica ready dengan Databases kosong), `TestCreateReplicaSubsetUsesDumpWithFilter` (Databases [a] → dumpFrom Databases [a], configureReplication Databases [a]), `TestCreateReplicaRefusesNonDedicatedTarget`, `TestCreateReplicaInstallsMySQLWhenMissing`, `TestFailsWhenAgentDropsDuringSnapshot` + `Retry` dari tahap 4, `TestForceDumpSkipsClone`, `TestResumeMarksRunningFailed`, `TestCancelSendsJobCancel`.
- [ ] **Step 2–4** (`-race`). **Step 5: Commit** — `git commit -m "feat(panel): prepare-master and create-replica orchestration"`

---

### Task 13: UI: Hubungkan MySQL, Siapkan master, Buat replika, Job

**Files:**
- Create: `web/templates/master_detail.html`, `web/templates/replica_new.html`, `web/templates/replica_detail.html`,
  `web/templates/job_detail.html`, `web/templates/jobs.html`, `internal/panel/web/handlers_mysql.go`,
  `internal/panel/web/handlers_jobs.go`
- Modify: `web/templates/server_detail.html` (kartu "MySQL": form Hubungkan MySQL bila belum; versi + tabel database + ukuran + hak kurang; tombol "Siapkan sebagai master" / "Segarkan"), `web/templates/dashboard.html`, `web/templates/layout.html` (menu Pekerjaan), `web/static/app.js` (SSE `job_update`/`job_log`), `internal/panel/web/router.go`
- Test: `internal/panel/web/web_mysql_jobs_test.go`

**Interfaces:**
- Consumes: antarmuka kecil di web:

```go
type Orchestrator interface {
    Connect(ctx, serverID int64, p proto.ConnectParams) (proto.ConnectResult, error)
    RefreshInventory(ctx, serverID int64) (proto.ConnectResult, error)
    StartPrepareMaster(ctx, serverID int64) (string, error)
    StartCreateReplica(ctx, p orchestrator.CreateReplicaParams) (string, error)
    Approve(jobID string) error; Retry(jobID string) error; Cancel(jobID string) error
}
```
Rute: `POST /servers/{id}/mysql/connect` (user, password, host opsional; pesan sukses/gagal; password tidak pernah ditampilkan ulang), `POST /servers/{id}/mysql/refresh`, `POST /servers/{id}/prepare-master` → redirect `/jobs/{jobID}`; `GET /masters/{id}` (kesehatan ringkas, database, replika + state + database yang diikuti, tombol Buat replika, riwayat job); `GET /masters/{id}/replicas/new` (pilih server tujuan: paired+online+unassigned; checkbox database dari inventory master, default semua; pilihan "Paksa dump"; kotak keterangan: server tujuan dikunci hanya-baca seluruhnya dan semua database lamanya dibuang); `POST /masters/{id}/replicas` → `StartCreateReplica` → redirect job; `GET /replicas/{id}` (state, database diikuti, tombol Tambah/Lepas database (Task 14), riwayat job); `GET /jobs`, `GET /jobs/{id}` (tahap, log hidup, tombol: Setujui & lanjutkan / Ulangi dari tahap ini / Batalkan), `POST /jobs/{id}/approve|retry|cancel`. Status job Indonesia: Antre, Berjalan, Menunggu persetujuan, Selesai, Gagal, Dibatalkan.

- [ ] **Step 1: Tulis test** (fake Orchestrator): `TestConnectMySQLFormPostsAndShowsDatabases`, `TestConnectMySQLShowsMissingGrants`, `TestPrepareMasterStartsJob`, `TestNewReplicaFormListsEligibleServersAndDatabases`, `TestCreateReplicaPostsSelectedDatabases`, `TestJobDetailShowsStepsAndButtons`, `TestJobApproveRetryCancelPost`, `TestJobsListRequiresLogin`.
- [ ] **Step 2–4.** `scripts/tailwind.sh` bila kelas baru; commit `app.css`. **Step 5: Commit** — `git commit -m "feat(web): connect MySQL, prepare master, create replica, job pages"`

---

### Task 14: Tambah dan lepas database pada replika yang berjalan

**Files:**
- Create: `internal/panel/orchestrator/change_databases.go`, `web/templates/replica_databases.html`
- Modify: `internal/agent/drivers/mysql/dump.go` (dipakai ulang), `internal/panel/web/handlers_mysql.go`, `internal/panel/web/router.go`, `internal/proto/mysql.go` (`DropDatabasesParams{Databases}` → perintah "mysql.dropDatabases", hanya di server berperan replika dan super_read_only ON; agent mematikan super_read_only sesaat dalam satu sesi `SET SESSION`? Tidak bisa: super_read_only global. Resolusi: DROP dijalankan dengan `SET GLOBAL super_read_only=OFF; DROP …; SET GLOBAL super_read_only=ON` dalam satu perintah agent dengan `sql_log_bin=0`, dicatat di log.)
- Test: `internal/panel/orchestrator/change_databases_test.go`, `internal/panel/web/web_mysql_jobs_test.go`

Job `add_databases` (tahap): 0 `catch_up` (mysql.status master → gtid; waitSynced replika); 1 `stop` (stopReplica); 2 `dump` (dumpFrom Databases baru, DropExisting; Result.GTIDExecuted = P2); 3 `replay_until` (startUntil P2 dengan filter lama; tunggu); 4 `expand_filter` (setReplicationFilter gabungan; startReplica); 5 `verify` (waitSynced; SetReplicaDatabases). Job `remove_databases`: 0 `shrink_filter` (stop; setReplicationFilter dikurangi; start); 1 `drop` (hanya bila user mencentang "hapus datanya juga": mysql.dropDatabases); 2 `verify`. Rute: `GET /replicas/{id}/databases` (daftar database master dengan status diikuti/tidak), `POST /replicas/{id}/databases/add`, `POST /replicas/{id}/databases/remove` (checkbox hapus data; konfirmasi modal). Replika yang mengikuti semua database (Databases kosong) tidak menampilkan fitur ini.

- [ ] **Step 1: Tulis test** (orkestrator dengan agent palsu; web dengan fake): `TestAddDatabasesSequence` (urutan perintah persis: status master → waitSynced → stopReplica → dumpFrom[b] → startUntil(P2) → setReplicationFilter[a,b] → startReplica → waitSynced; SetReplicaDatabases [a,b]), `TestAddDatabasesFailsIfReplicaLagging` (waitSynced false → failed di tahap 0), `TestRemoveDatabasesWithDrop`, `TestRemoveDatabasesKeepsData`, `TestReplicaDatabasesPageHiddenForAll`.
- [ ] **Step 2–4.** **Step 5: Commit** — `git commit -m "feat: add and remove databases on a running replica"`

---

### Task 15: Integrasi end-to-end dengan mysqld nyata

**Files:**
- Create: `internal/panel/e2e_replica_test.go`
- Modify: `Makefile` (`test-e2e`), `README.md` (cara menjalankan; butuh mysqld lokal)

- [ ] **Step 1: Tulis test** `TestE2ECreateReplica` (skip bila mysqld tidak ada atau `-short`): dua instance testmysql (master dengan database a,b; replika kosong) ← dua agent nyata dengan `mysql.Driver{Host: inst.Host()}` + `jobs.Runner` + `CredentialStore` di dir temp, dipasangkan ke panel nyata; lewat HTTP: login, connect MySQL master (root instance), prepare-master (job selesai tanpa waiting karena instance sudah GTID/binlog), buat replika dengan Databases [a] (dump + filter); tunggu job succeeded ≤ 5 menit; assert: insert ke a muncul di replika ≤ 10 s, insert ke b tidak; `super_read_only` ON; password repl tersegel di DB; log job tanpa password; lalu add_databases [b] → insert ke b muncul.
- [ ] **Step 2: `make test-e2e`** di lingkungan ini; cantumkan durasi di laporan.
- [ ] **Step 3: Commit** — `git commit -m "test: end-to-end create replica and add database against real mysqld"`

---

## Catatan untuk pelaksana

- Spec 9.10 (antarmuka `Driver` lintas engine) ditarik saat engine kedua
  masuk; orkestrator hanya memanggil nama perintah.
- Pesan error MySQL mentah masuk log job; teks untuk user memakai bahasa
  awam plus nomor error.
- Jangan menulis password apa pun ke `job_log`; Task 15 menguji.
- Rencana 3 (pemantauan, heartbeat, Telegram, peringatan bila
  `super_read_only` dimatikan) tidak dibangun di sini.
