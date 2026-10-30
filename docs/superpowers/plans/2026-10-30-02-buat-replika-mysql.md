# Rencana Implementasi 2: Buat Replika MySQL Sekali Klik

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Dari panel, user memilih master dan server tujuan, menekan
"Buat replika", dan panel mengorkestrasi agent di kedua sisi sampai
replika MySQL tersambung dan sinkron, termasuk memasang MySQL di server
kosong; dimulai dengan mengamankan bootstrap agent.

**Architecture:** Agent mendapat driver `mysql` di balik antarmuka `Host`
(SQL, service systemd, file config, exec) sehingga bisa diuji dengan
instance mysqld nyata tanpa root. Operasi panjang (clone, dump, install)
berjalan sebagai job di agent yang memancarkan event progres. Panel punya
orkestrator job (state machine per tahap, persisten di SQLite, bisa
diulang per tahap, berhenti di tahap yang butuh persetujuan manusia) dan
UI job dengan log hidup lewat SSE.

**Tech Stack:** Go 1.26.10, `github.com/go-sql-driver/mysql v1.9.3`
(pure Go), `github.com/google/uuid v1.6.0`; selebihnya seperti Rencana 1.
Tes integrasi memakai `mysqld` lokal (Ubuntu `mysql-server-core-8.0`,
8.0.46) lewat harness `testmysql`.

**Spec:** `docs/superpowers/specs/2026-10-30-db-replica-panel-design.md`
(bagian 4, 6.4, 6.5, 7.3, 7.4, 8 (Master, Job), 9.5, 9.8, 9.9, 9.10, 9.12).
Rencana 1 dan ledger-nya: `docs/superpowers/plans/2026-10-30-01-fondasi-panel-agent.md`,
`docs/superpowers/ledgers/2026-10-30-01-fondasi-panel-agent-ledger.md`
(daftar "deferred to Plan 2").

## Global Constraints

- Go 1.26.10, `CGO_ENABLED=0`, pure Go; modul `github.com/husmin-fajrin/db-panel`.
- Engine: Oracle MySQL **8.0.26+**, 8.4, 9.x. Lebih rendah, MariaDB,
  Percona: ditolak di preflight dengan pesan Indonesia. `RESET MASTER`
  untuk 8.0, `RESET BINARY LOGS AND GTIDS` untuk 8.4+.
- Master dan replika satu seri (8.0/8.0, 8.4/8.4, 9/9). CLONE jalur
  utama bila donor dan penerima memenuhi syarat (8.0.37+ bebas patch;
  di bawah itu patch harus sama); dump jalur kedua yang setara.
- Agent hanya menjalankan perintah registry dengan parameter terstruktur;
  tidak ada string shell dari panel. Semua SQL dengan placeholder atau
  identifier yang divalidasi (`[A-Za-z0-9_]{1,32}`).
- Panel tidak pernah merestart MySQL master tanpa klik manusia; replika
  boleh direstart otomatis selama belum melayani siapa pun.
- Replika selalu `read_only=ON` dan `super_read_only=ON`.
- Password user replikasi dibuat panel (24 karakter acak), disimpan
  terenkripsi (AES-256-GCM, kunci `/etc/dbpanel/panel.key` 0600), dikirim
  ke agent hanya lewat kanal mTLS, tidak pernah ditulis ke log, ke file
  config MySQL, atau ke argv (mysqldump/mysql menerima password lewat
  `--defaults-extra-file` sementara 0600 yang dihapus setelahnya).
- Kredensial admin master yang diketik user dipakai sekali dalam job dan
  tidak disimpan.
- Teks UI dan pesan error untuk user: Indonesia; log: Inggris.
- Setiap task diakhiri `make test` hijau dan commit. Tes yang butuh
  `mysqld` dilewati (`t.Skip`) bila tidak ada, dan WAJIB jalan di
  lingkungan pengembangan ini (mysqld 8.0.46 terpasang).

## Review Focus

1. Master dengan `log_bin=OFF` atau `bind-address=127.0.0.1`: job harus
   berhenti di tahap "Siapkan master" dengan status menunggu, menjelaskan
   bahwa MySQL master perlu restart, dan tidak pernah merestart sendiri
   (Task 11 menguji).
2. Server tujuan yang MySQL-nya sudah berisi data: job berhenti dan minta
   konfirmasi bahwa data akan dibuang, bukan menimpa diam-diam (Task 6 dan
   Task 11 menguji).
3. Agent replika mati di tengah clone: job gagal dengan pesan Indonesia
   yang menyebut tahap dan bisa diulang dari tahap snapshot (Task 11).
4. Master tidak bisa dijangkau dari replika (firewall): preflight gagal
   dengan menyebut host dan port yang dicoba (Task 6).
5. MariaDB, MySQL 5.7, atau 8.0 di bawah 8.0.26: ditolak sebelum ada
   perubahan apa pun di server (Task 5 dan Task 6).

---

### Task 1: Bootstrap agent terverifikasi (hash skrip, hash binary, pin CA)

**Files:**
- Modify: `internal/panel/agentapi/install.go`, `web/templates/agent-install.sh.tmpl`,
  `internal/panel/web/handlers_servers.go` (perintah pairing),
  `internal/agent/pair.go`, `internal/agent/cli.go`, `internal/panel/servers.go`,
  `README.md`
- Create: `internal/panel/agentapi/binhash.go`
- Test: `internal/panel/agentapi/install_test.go`, `internal/agent/cli_test.go`,
  `internal/panel/web/web_test.go`, `internal/panel/panel_test.go`

**Interfaces:**
- Consumes: `agentapi.Deps`, `BinaryHandler`, `InstallScriptHandler`,
  `ca.CA.CAPEM()`, `agent.Pair`.
- Produces:

```go
// package agentapi
type BinHashes struct{ /* cache arch→sha256 hex, invalidated by mtime */ }
func NewBinHashes(binDir string) *BinHashes
func (b *BinHashes) For(arch string) (sha256hex string, err error)   // file di binDir, atau os.Executable() bila arch == runtime.GOARCH; error "binary untuk arsitektur ini belum tersedia di panel"
func CAFingerprint(caPEM []byte) string            // sha256 hex DER sertifikat CA
type InstallScriptParams struct{ PanelURL, Token, AgentAddr, CASHA256 string; BinSHA256 map[string]string /* arch→hex */ }
func RenderInstallScript(p InstallScriptParams) ([]byte, error)      // deterministik; dipakai handler dan UI
func InstallScriptHandler(d Deps, publicURL func() string, hashes *BinHashes) http.Handler   // signature baru
// package agent
func Pair(ctx context.Context, panelURL, token, dir string, client *http.Client, opts PairOptions) (config.Config, error)
type PairOptions struct{ CASHA256 string /* wajib bila bukan "" : cocokkan CAFingerprint(resp.CAPEM) */; AgentAddr string /* bila != "" menimpa resp.AgentAddr */ }
// CLI: `agent pair --panel URL --ca-sha256 HEX --agent-addr HOST:PORT` (token dari DBPANEL_TOKEN/stdin seperti sekarang); --ca-sha256 wajib kecuali --insecure-skip-ca-pin (hanya untuk pengembangan, cetak peringatan)
```

Perintah pairing di UI (satu baris, token tidak di argv):
```
curl -fsSL -K - <<<'url = "<PublicURL>/agent/install?token=<T>"' -o /tmp/dbpanel-agent.sh && echo '<SCRIPT_SHA256>  /tmp/dbpanel-agent.sh' | sha256sum -c - && sudo bash /tmp/dbpanel-agent.sh
```
`<SCRIPT_SHA256>` = sha256 dari `RenderInstallScript` dengan parameter
yang sama yang akan dipakai handler (PublicURL, token, AgentAddr,
CA fingerprint, hash binary semua arch yang tersedia). Skrip: setelah
unduh binary, `echo "<BinSHA256[arch]>  $BIN.new" | sha256sum -c -` (die
`Checksum binary agent tidak cocok; kemungkinan ada yang menyusup di
jaringan`), lalu `agent pair --panel … --ca-sha256 <CASHA256> --agent-addr <AgentAddr>`.

- [ ] **Step 1: Tulis test**: `TestRenderInstallScriptDeterministic` (dua render sama byte; mengandung hash binary dan `--ca-sha256`), `TestBinHashesForOwnArch` (sama dengan sha256 `os.Executable()`), `TestPairRejectsCAMismatch` (server pair palsu mengirim CA lain → error `CA panel tidak cocok dengan yang diharapkan`), `TestPairPinsAgentAddr`, `TestPairingCommandContainsScriptHash` (web: hash di perintah == sha256 body dari `GET /agent/install?token=`), `TestCLIPairRequiresCAPin` (tanpa flag → exit 2 dengan pesan).
- [ ] **Step 2: Jalankan, pastikan gagal.**
- [ ] **Step 3: Implementasi.** Hash dihitung sekali per file (cache by size+mtime). Template: tambahkan variabel `{{.CASHA256}}`, `{{index .BinSHA256 "amd64"}}`/`arm64` (skip verifikasi bila kosong untuk arch itu: die `binary untuk arsitektur ini belum tersedia di panel`).
- [ ] **Step 4: Jalankan, pastikan lulus**; uji manual end-to-end seperti Rencana 1 Task 11 (pair ke panel nyata, lalu ubah satu byte CA fingerprint di skrip → pairing ditolak).
- [ ] **Step 5: Commit** — `git commit -m "feat(security): verified agent bootstrap (script, binary and CA pinning)"`

---

### Task 2: Rapikan seam dari Rencana 1 (proto wire types, pki, satu jalur event, error offline, guard migrasi)

**Files:**
- Create: `internal/proto/pair.go`, `internal/pki/agent.go`
- Modify: `internal/agent/pair.go`, `internal/agent/runtime.go`,
  `internal/panel/agentapi/pair.go`, `internal/panel/agentapi/ws.go`,
  `internal/panel/ca/ca.go` (hapus helper agent), `internal/panel/hub/hub.go`,
  `internal/panel/hub/conn_local.go`, `internal/panel/localagent.go`,
  `internal/panel/servers.go`, `internal/panel/listener_impl.go`,
  `internal/proto/messages.go`, `internal/panel/store/migrate.go`
- Test: tes yang ada harus tetap hijau; tambah `internal/panel/store/migrate_test.go`,
  `internal/panel/hub/hub_test.go`

**Interfaces:**
- Produces:

```go
// proto/pair.go: PairRequest, PairResponse dipindah dari package agent (field dan tag sama).
// pki/agent.go: PanelServerName, NewAgentKeyAndCSR, AgentClientTLS dipindah dari ca (signature sama); ca mengimpor pki untuk PanelServerName.
// hub: Listener tetap; tambah
func (h *Hub) OnEvent(serverID int64, ev proto.Event)   // meneruskan ke Listener.OnEvent; dipakai agentapi dan localagent sebagai callback → Deps.OnEvent dihapus
// proto: 
func (e *Error) Is(target error) bool   // true bila target *Error dengan Code sama
var ErrOffline = &Error{Code: CodeOffline, Message: "koneksi agent terputus"}   // hub.ErrOffline = proto.ErrOffline (alias var)
// store/migrate.go: guard downgrade
var ErrNewerSchema = errors.New("database dibuat oleh versi dbpanel yang lebih baru")   // bila max(schema_migrations.version) > versi tertinggi yang dikenal
```

- [ ] **Step 1: Tulis test**: `TestOpenRefusesNewerSchema` (sisipkan baris version 999 → `ErrNewerSchema`), `TestHubOnEventForwards`, `TestErrorsIsOffline` (`errors.Is(err, proto.ErrOffline)` untuk error dari Call saat tidak terhubung dan saat putus di tengah).
- [ ] **Step 2: Jalankan, pastikan gagal.**
- [ ] **Step 3: Refactor** tanpa mengubah perilaku; `go build ./...` dan seluruh suite hijau; tidak ada import `internal/panel/ca` dari `internal/agent`, tidak ada import `internal/agent` dari `internal/panel/agentapi`.
- [ ] **Step 4: Jalankan `make test` dan `go test -race ./internal/...`.**
- [ ] **Step 5: Commit** — `git commit -m "refactor: wire types in proto, agent pki package, single event path, offline error, schema guard"`

---

### Task 3: Store: topologi, replika, job, dan kotak rahasia

**Files:**
- Create: `internal/panel/store/migrations/0003_topologies_jobs.sql`,
  `internal/panel/store/topologies.go`, `internal/panel/store/jobs.go`,
  `internal/panel/secrets/box.go`
- Test: `internal/panel/store/topologies_test.go`, `internal/panel/store/jobs_test.go`,
  `internal/panel/secrets/box_test.go`

**Interfaces:**
- Produces:

```go
// package secrets
type Box struct{ /* key [32]byte */ }
func LoadOrCreate(path string) (*Box, error)        // 32 byte acak, file 0600, O_EXCL; error bila mode file lebih longgar dari 0600
func (b *Box) Seal(plain string) ([]byte, error)    // AES-256-GCM, nonce 12 byte acak di depan
func (b *Box) Open(ct []byte) (string, error)

// package store
const ( RoleUnassigned = "unassigned"; RoleMaster = "master"; RoleReplica = "replica" )   // sudah ada
type Topology struct { ID, MasterServerID int64; ReplUser string; ReplPasswordEnc []byte; SourceHost string; SourcePort int; CreatedAt time.Time }
func (s *Store) CreateTopology(masterServerID int64, replUser string, replPasswordEnc []byte, sourceHost string, sourcePort int) (Topology, error)   // juga SetServerRole(master); error bila server sudah replika
func (s *Store) GetTopology(id int64) (Topology, error)
func (s *Store) GetTopologyByMaster(serverID int64) (Topology, error)   // ErrNotFound
func (s *Store) UpdateTopologySource(id int64, host string, port int) error
func (s *Store) ListTopologies() ([]Topology, error)
const ( ReplicaCreating = "creating"; ReplicaReady = "ready"; ReplicaFailed = "failed" )
type Replica struct { ID, TopologyID, ServerID int64; State, LastError string; CreatedAt time.Time }
func (s *Store) AddReplica(topologyID, serverID int64) (Replica, error)   // SetServerRole(replica); error bila server sudah master/replika
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
func (s *Store) CreateJob(j Job, stepNames []string) error            // ID diisi pemanggil (uuid)
func (s *Store) GetJob(id string) (Job, []JobStep, error)
func (s *Store) ListJobs(limit int) ([]Job, error)                    // terbaru dulu
func (s *Store) ListJobsForServer(serverID int64, limit int) ([]Job, error)
func (s *Store) RunningJobs() ([]Job, error)                          // status running|waiting
func (s *Store) UpdateJob(id, status string, currentStep int, errMsg string, now time.Time) error   // set started_at saat pertama running, finished_at saat terminal
func (s *Store) SetStep(jobID string, idx int, status, message string, now time.Time) error
func (s *Store) AppendJobLog(jobID, level, message string, now time.Time) (int64, error)
func (s *Store) JobLog(jobID string, afterID int64, limit int) ([]JobLogLine, error)
func (s *Store) PruneJobLogs(olderThan time.Time) (int64, error)
```

Skema 0003: `topologies(id, master_server_id UNIQUE FK, repl_user, repl_password_enc BLOB, source_host, source_port, created_at)`, `replicas(id, topology_id FK CASCADE, server_id UNIQUE FK CASCADE, state, last_error, created_at)`, `jobs(id TEXT PK, kind, server_id, topology_id, status, current_step, error, params_json, created_at, started_at, finished_at)`, `job_steps(job_id FK CASCADE, idx, name, status, message, started_at, finished_at, PK(job_id, idx))`, `job_log(id AUTOINCREMENT, job_id FK CASCADE, ts, level, message)` + index `(job_id, id)`.

- [ ] **Step 1: Tulis test**: `TestBoxSealOpenRoundTrip`, `TestBoxRejectsTamper`, `TestBoxRefusesLoosePerms`; `TestTopologyLifecycle` (create → role master; add replica → role replica; add same server twice → error; remove → unassigned), `TestCreateTopologyRejectsReplicaServer`; `TestJobLifecycle` (create with 3 steps → get → update running → set step → append log → JobLog after id → finished_at set on succeeded), `TestRunningJobs`, `TestPruneJobLogs`, `TestDeleteServerCascadesReplicaAndJobs`.
- [ ] **Step 2: Jalankan, pastikan gagal.**
- [ ] **Step 3: Implementasi.** `go get github.com/google/uuid@v1.6.0` (dipakai di Task 10; boleh ditambahkan di sini).
- [ ] **Step 4: Jalankan, pastikan lulus.**
- [ ] **Step 5: Commit** — `git commit -m "feat(store): topologies, replicas, jobs; secrets box"`

---

### Task 4: Job runner di agent

**Files:**
- Create: `internal/proto/jobs.go`, `internal/agent/jobs/runner.go`
- Test: `internal/agent/jobs/runner_test.go`

**Interfaces:**
- Produces:

```go
// proto/jobs.go
const ( EventJobProgress = "job.progress"; EventJobLog = "job.log"; EventJobDone = "job.done" )
type JobProgress struct { JobID string `json:"job_id"`; Percent int `json:"percent"`; Message string `json:"message"` }
type JobLog      struct { JobID string `json:"job_id"`; TS time.Time `json:"ts"`; Level string `json:"level"`; Message string `json:"message"` }
type JobDone     struct { JobID string `json:"job_id"`; OK bool `json:"ok"`; Error string `json:"error,omitempty"`; Result json.RawMessage `json:"result,omitempty"` }
type JobStatus   struct { ID string `json:"id"`; Kind string `json:"kind"`; State string `json:"state"` /* running|succeeded|failed|canceled */; Percent int `json:"percent"`; Message string `json:"message"`; Error string `json:"error,omitempty"`; Result json.RawMessage `json:"result,omitempty"`; StartedAt time.Time `json:"started_at"`; FinishedAt *time.Time `json:"finished_at,omitempty"` }
type StartedJob  struct { JobID string `json:"job_id"` }
type JobIDParams struct { JobID string `json:"job_id"` }
type JobLogParams struct { JobID string `json:"job_id"`; After int `json:"after"` }
type JobLogResult struct { Lines []JobLog `json:"lines"`; Next int `json:"next"` }

// package jobs
type Emitter interface { Emit(ev proto.Event) error }
type Job struct{ /* ID, Kind, ctx, cancel, progress, log ring (maks 2000 baris) */ }
func (j *Job) ID() string
func (j *Job) Context() context.Context
func (j *Job) Progress(percent int, message string)        // event job.progress (dibatasi 1/detik kecuali perubahan ≥ 5%)
func (j *Job) Logf(level, format string, args ...any)       // level "info"|"warn"|"error"; event job.log
type Func func(j *Job) (result any, err error)
var ErrBusy = errors.New("agent sedang menjalankan pekerjaan lain")
type Runner struct{ /* satu job aktif sekaligus; riwayat 20 job terakhir */ }
func NewRunner(em Emitter, logger *slog.Logger) *Runner
func (r *Runner) Start(kind string, fn Func) (id string, err error)   // id 16 byte hex; job.done dipancarkan saat selesai (OK/err; panic → err)
func (r *Runner) Status(id string) (proto.JobStatus, bool)
func (r *Runner) Cancel(id string) bool
func (r *Runner) Logs(id string, after int) (proto.JobLogResult, bool)
func (r *Runner) Register(reg *commands.Registry)    // "job.status", "job.cancel", "job.log" (params JobIDParams / JobLogParams; unknown id → ParamError "job tidak dikenal")
```

Catatan: `Emit` mengembalikan `ErrOffline` saat agent terputus; Runner
mencatat event yang gagal terkirim dan tidak menghentikan job (panel
menyusul lewat `job.status`/`job.log`).

- [ ] **Step 1: Tulis test** dengan fake emitter: `TestStartRunsAndEmitsDone`, `TestStartRejectsSecondJob` (ErrBusy), `TestCancelStopsJob` (fn memantau ctx), `TestPanicBecomesFailure`, `TestProgressThrottled` (10 progress 1% dalam 100 ms → ≤ 2 event), `TestLogsPagination`, `TestRegisterCommands` (Dispatch `job.status` untuk id tak dikenal → CodeBadParams).
- [ ] **Step 2: Jalankan, pastikan gagal.**
- [ ] **Step 3: Implementasi.**
- [ ] **Step 4: `go test -race ./internal/agent/jobs/`.**
- [ ] **Step 5: Commit** — `git commit -m "feat(agent): job runner with progress events"`

---

### Task 5: Fondasi driver MySQL: Host, dialek, koneksi, status, harness testmysql

**Files:**
- Create: `internal/proto/mysql.go`, `internal/agent/drivers/mysql/host.go`,
  `internal/agent/drivers/mysql/realhost.go`, `internal/agent/drivers/mysql/dialect.go`,
  `internal/agent/drivers/mysql/conn.go`, `internal/agent/drivers/mysql/status.go`,
  `internal/agent/drivers/mysql/driver.go`,
  `internal/agent/drivers/mysql/testmysql/instance.go`,
  `internal/agent/drivers/mysql/testmysql/host.go`
- Modify: `internal/agent/config/config.go` (field `MySQLRootPassword` sudah ada; tambah `MySQLSocket string yaml:"mysql_socket,omitempty"`), `internal/agent/cli.go` (daftarkan driver + job runner)
- Test: `internal/agent/drivers/mysql/dialect_test.go`, `internal/agent/drivers/mysql/status_test.go`

**Interfaces:**
- Produces:

```go
// proto/mysql.go (tipe wire dipakai panel dan agent; semua field bertag JSON snake_case;
// di dalam package proto tipe ditulis tanpa prefix `proto.`)
type MySQLStatus struct {
    Version string `json:"version"`; Series string `json:"series"` /* "8.0"|"8.4"|"9" */
    ServerID uint32; LogBin bool; GTIDMode string; EnforceGTID bool; BinlogFormat string
    ReadOnly, SuperReadOnly bool; GTIDExecuted string; DataDir string; Port int
    Replica *ReplicaStatus `json:"replica,omitempty"`   // nil bila bukan replika
    UptimeSec int64; ThreadsRunning int
}
type ReplicaStatus struct { IORunning, SQLRunning string /* Yes|No|Connecting */; SourceHost string; SourcePort int; LastIOError, LastSQLError string; LastIOErrno, LastSQLErrno int; SecondsBehind *int64; RetrievedGTID, ExecutedGTID string }

// package mysql (agent driver)
type ServiceController interface { Start(ctx) error; Stop(ctx) error; Restart(ctx) error; IsActive(ctx) (bool, error); WaitReady(ctx context.Context, timeout time.Duration) error /* polling koneksi root */ }
type Host interface {
    OpenRoot(ctx context.Context) (*sql.DB, error)          // socket + auth_socket (root) atau password dari config
    Service() ServiceController
    WriteConfig(content string) (path string, err error)    // /etc/mysql/conf.d/dbpanel.cnf (debian) atau /etc/my.cnf.d/dbpanel.cnf (rhel); 0644
    ValidateConfig(ctx context.Context) error               // mysqld --validate-config
    DataDir(ctx context.Context) (string, error)
    DiskFree(path string) (free, total uint64, err error)
    Exec(ctx context.Context, stdin io.Reader, stdout io.Writer, name string, args ...string) error
    TCPReachable(ctx context.Context, host string, port int, timeout time.Duration) error
    OSFamily() string                                        // "debian"|"rhel"|"unknown"
    TempFile(pattern string, mode os.FileMode) (*os.File, error)   // untuk --defaults-extra-file
}
func NewRealHost(cfg config.Config, logger *slog.Logger) Host
type Dialect struct { Version string; Major, Minor, Patch int; Series string }
func ParseVersion(v string) (Dialect, error)              // "8.0.46-0ubuntu0.24.04.4" → 8,0,46 series "8.0"; "8.4.3" → "8.4"; "9.1.0" → "9"; MariaDB/"5.7" → error ErrUnsupported dengan teks Indonesia
var ErrUnsupported = errors.New("versi MySQL tidak didukung (butuh Oracle MySQL 8.0.26 ke atas)")
func (d Dialect) ResetBinaryLogs() string                 // "RESET MASTER" (8.0) | "RESET BINARY LOGS AND GTIDS"
func (d Dialect) CloneNeedsExactPatch() bool              // true bila < 8.0.37
func (d Dialect) SameSeries(o Dialect) bool
func Status(ctx context.Context, db *sql.DB) (proto.MySQLStatus, error)
type Driver struct{ Host Host; Jobs *jobs.Runner; Logger *slog.Logger }
func (d *Driver) Register(reg *commands.Registry)         // "mysql.status" di task ini; task berikut menambah perintah lain di file masing-masing lewat helper register
```

`testmysql` (harness; hanya untuk tes, package non-test agar bisa dipakai lintas package):
```go
type Instance struct{ Port int; Socket string; DataDir string; ServerID uint32; /* proses */ }
type Options struct{ ServerID uint32; ExtraArgs []string }
func Start(t testing.TB, o Options) *Instance   // t.Skip bila mysqld tidak ditemukan di PATH, /usr/sbin, /usr/libexec; cari dir dasar yang bisa dilalui user tak-root: $DBPANEL_TEST_MYSQL_DIR atau /var/tmp/dbpanel-test-<pid>; bila uid 0 jalankan --user=$DBPANEL_TEST_MYSQL_USER (default "nobody", chown datadir); --initialize-insecure; flag: --no-defaults --port --socket --mysqlx=OFF --server-id --log-bin=binlog --gtid-mode=ON --enforce-gtid-consistency=ON --secure-file-priv=<dir>/files --log-error --pid-file; tunggu siap ≤ 60 s; t.Cleanup stop + hapus
func (i *Instance) Restart(t testing.TB)
func (i *Instance) DSN() string                 // root tanpa password via socket
func (i *Instance) Host() mysql.Host            // implementasi Host untuk instance ini: Service = supervisor kecil (restart proses saat keluar dengan exit code 16 / setelah CLONE), WriteConfig = tulis ke <dir>/conf.d/dbpanel.cnf DAN tambahkan --defaults-extra-file saat restart, ValidateConfig = mysqld --validate-config dengan file itu, Exec = exec nyata (mysqldump/mysql dari PATH)
```

- [ ] **Step 1: Tulis test**: `TestParseVersion` (tabel: 8.0.46-ubuntu → ok; 8.0.25 → ErrUnsupported; 8.4.3 → series 8.4; 9.1.0 → series 9; "10.11.6-MariaDB" → ErrUnsupported; "5.7.44" → ErrUnsupported), `TestResetBinaryLogsByVersion`, `TestStatusAgainstInstance` (testmysql: Version terisi, LogBin true, GTIDMode "ON", Replica nil, ServerID == opsi).
- [ ] **Step 2: Jalankan, pastikan gagal.**
- [ ] **Step 3: Implementasi** termasuk harness. `go get github.com/go-sql-driver/mysql@v1.9.3`. `OpenRoot` RealHost: DSN `root@unix(<socket>)/?parseTime=true&timeout=5s`, socket dari `mysqld --verbose --help` baris `socket` bila config kosong; password dari config bila ada.
- [ ] **Step 4: Jalankan** `go test ./internal/agent/drivers/mysql/...` di lingkungan ini (mysqld ada) — harus menjalankan, bukan skip; cantumkan output di laporan.
- [ ] **Step 5: Commit** — `git commit -m "feat(mysql): driver foundation, dialect, status, testmysql harness"`

---

### Task 6: Preflight master dan replika

**Files:**
- Create: `internal/agent/drivers/mysql/preflight.go`
- Modify: `internal/proto/mysql.go`
- Test: `internal/agent/drivers/mysql/preflight_test.go`

**Interfaces:**
- Produces:

```go
// proto/mysql.go
type PreflightParams struct { Role string `json:"role"` /* master|replica */; PeerHost string `json:"peer_host,omitempty"`; PeerPort int `json:"peer_port,omitempty"`; PeerVersion string `json:"peer_version,omitempty"`; PeerDataBytes uint64 `json:"peer_data_bytes,omitempty"`; ReplUser string `json:"repl_user,omitempty"`; AllowNonEmpty bool `json:"allow_non_empty,omitempty"` }
const ( CheckOK = "ok"; CheckWarn = "warn"; CheckFail = "fail"; CheckNeedsRestart = "needs_restart"; CheckNeedsConfirm = "needs_confirm" )
type Check struct { ID string `json:"id"`; Status string `json:"status"`; Title string `json:"title"` /* Indonesia */; Detail string `json:"detail"`; FixSQL []string `json:"fix_sql,omitempty"`; FixConfig string `json:"fix_config,omitempty"` }
type PreflightResult struct { Status proto.MySQLStatus `json:"status"`; Checks []Check `json:"checks"`; DataBytes uint64 `json:"data_bytes"`; NonInnoDB []string `json:"non_innodb,omitempty"`; ExistingReplicas int `json:"existing_replicas"`; IsReplica bool `json:"is_replica"`; Worst string `json:"worst"` /* ok|warn|needs_confirm|needs_restart|fail */ }
// driver: perintah "mysql.preflight"
func Preflight(ctx context.Context, h Host, p proto.PreflightParams) (proto.PreflightResult, error)
```

Check ID dan aturan (master): `version` (ParseVersion; fail bila ErrUnsupported), `server_id` (0 → fail dengan FixSQL `SET PERSIST server_id=<acak 2..4e9>`; otomatis diperbaiki di Task 7), `log_bin` (OFF → needs_restart, FixConfig `log_bin=binlog`), `binlog_format` (≠ ROW → fail dengan FixSQL `SET PERSIST binlog_format='ROW'`), `gtid_mode` (≠ ON → warn "akan dinaikkan bertahap"), `enforce_gtid_consistency` (≠ ON → warn), `clone_plugin` (tidak aktif → warn "akan diaktifkan"; gagal INSTALL → dump), `bind_address` (`127.0.0.1`/`localhost`/`::1` → needs_restart FixConfig `bind-address=0.0.0.0`), `repl_user` (bila ReplUser diberikan dan tidak ada / grant kurang → warn "akan dibuat"), `non_innodb` (ada tabel non-InnoDB di luar `mysql`,`sys`,`performance_schema`,`information_schema` → fail, Detail daftar maks 20), `existing_replication` (ada thread `Binlog Dump` di PROCESSLIST atau server ini replika → warn dengan jumlah; bila gtid_mode harus berubah dan ada replika lama → fail "akan memutus replika lama"), `data_size` (jumlah `data_length+index_length` dari information_schema.tables).
Replica: `version` (+ `same_series` fail bila PeerVersion beda seri; `clone_patch` warn bila < 8.0.37 dan patch beda → jalur dump), `reachable` (TCPReachable PeerHost:PeerPort timeout 5 s → fail `tidak bisa menjangkau <host>:<port> dari server ini; periksa firewall dan bind-address master`), `disk` (DiskFree(DataDir) < PeerDataBytes×1.2 → fail dengan angka GB), `data_present` (ada database selain sistem → needs_confirm kecuali AllowNonEmpty; Detail daftar database), `not_master` (server ini punya thread Binlog Dump → fail "server ini sedang menjadi master").
`Worst` = status terburuk; urutan fail > needs_restart > needs_confirm > warn > ok.

- [ ] **Step 1: Tulis test** (testmysql + fake host untuk disk/TCP): `TestPreflightMasterHealthy` (Worst ok/warn, tanpa fail), `TestPreflightMasterNonInnoDB` (tabel MyISAM → fail `non_innodb`), `TestPreflightMasterBindLocalhost` (instance dengan `--bind-address=127.0.0.1` → needs_restart), `TestPreflightReplicaUnreachable` (PeerPort tertutup → fail `reachable`, Detail mengandung host:port), `TestPreflightReplicaDataPresent` (buat database → needs_confirm; AllowNonEmpty → ok), `TestPreflightReplicaDiskTooSmall` (fake DiskFree kecil → fail), `TestPreflightRejectsMariaDBVersion` (fake Status versi MariaDB → fail `version` tanpa sentuhan lain).
- [ ] **Step 2: Jalankan, pastikan gagal.** **Step 3: Implementasi.** **Step 4: Lulus.**
- [ ] **Step 5: Commit** — `git commit -m "feat(mysql): preflight checks for master and replica"`

---

### Task 7: Konfigurasi, user replikasi, restart, read-only

**Files:**
- Create: `internal/agent/drivers/mysql/configure.go`, `internal/agent/drivers/mysql/users.go`
- Modify: `internal/proto/mysql.go`
- Test: `internal/agent/drivers/mysql/configure_test.go`, `internal/agent/drivers/mysql/users_test.go`

**Interfaces:**
- Produces:

```go
// proto/mysql.go
type ConfigureParams struct { Role string `json:"role"`; ServerID uint32 `json:"server_id,omitempty"` /* 0 = pilih acak */; EnableClone bool `json:"enable_clone"`; GTID bool `json:"gtid"` }
type ConfigureResult struct { Applied []string `json:"applied"`; NeedsRestart bool `json:"needs_restart"`; RestartReasons []string `json:"restart_reasons"`; ConfigPath string `json:"config_path"`; ServerID uint32 `json:"server_id"` }
type ReplUserParams struct { User string `json:"user"`; Password string `json:"password"`; FromHost string `json:"from_host"` /* "%" atau IP replika */ }
type ReadOnlyParams struct { On bool `json:"on"` }
type RestartResult struct { TookMs int64 `json:"took_ms"` }
// driver, perintah: "mysql.configure", "mysql.restart", "mysql.createReplUser", "mysql.setReadOnly"
```

Aturan `Configure`: (1) `SET PERSIST server_id` bila 0 (acak 2..4294967295, bukan nilai peer); (2) `enforce_gtid_consistency=ON` lalu tahapan `gtid_mode`: OFF→OFF_PERMISSIVE→ON_PERMISSIVE→(tunggu `Ongoing_anonymous_transaction_count`=0 ≤ 60 s)→ON, masing-masing `SET PERSIST`; (3) `binlog_format=ROW` PERSIST; (4) `INSTALL PLUGIN clone SONAME 'mysql_clone.so'` bila EnableClone dan belum aktif; (5) tulis config `dbpanel.cnf` berisi `[mysqld]` dengan `log_bin=binlog`, `binlog_expire_logs_seconds=604800`, `gtid_mode=ON`, `enforce_gtid_consistency=ON`, `server_id=<n>`, dan untuk replica: `read_only=ON`, `super_read_only=ON`, `relay_log=relay`, `log_replica_updates=ON`; untuk master tidak menulis `bind-address` (hanya melaporkan); (6) `ValidateConfig`; (7) `NeedsRestart` bila `log_bin` saat ini OFF, atau bind-address lokal (master), atau replica (replika selalu direstart oleh orkestrator untuk menerapkan file). Tidak pernah merestart di sini. `Restart`: `Service().Restart` lalu `WaitReady ≤ 120 s`. `CreateReplUser`: `CREATE USER IF NOT EXISTS 'u'@'h' IDENTIFIED BY ?` lalu `ALTER USER ... IDENTIFIED BY ?` (idempoten, password baru), `GRANT REPLICATION SLAVE, BACKUP_ADMIN ON *.* TO 'u'@'h'`; identifier divalidasi regex. `SetReadOnly`: `SET PERSIST read_only=?, super_read_only=?`.

- [ ] **Step 1: Tulis test** (testmysql): `TestConfigureMasterSetsServerIDAndGTID` (instance dengan gtid OFF dan server_id acak 0? mysqld 8 default server_id=1; jalankan dengan `--gtid-mode=OFF --enforce-gtid-consistency=OFF` → setelah Configure: gtid_mode ON, binlog_format ROW, file config ada dan valid), `TestConfigureReportsRestartWhenLogBinOff` (instance `--skip-log-bin` → NeedsRestart, alasan `log_bin`), `TestRestartApplies` (Configure replica → Restart via harness → super_read_only ON), `TestCreateReplUserIdempotent` (dua kali, password berbeda → login dengan password terakhir berhasil lewat TCP), `TestReplUserRejectsBadIdentifier`.
- [ ] **Step 2–4** seperti biasa. **Step 5: Commit** — `git commit -m "feat(mysql): configure, replication user, restart, read-only"`

---

### Task 8: Snapshot: CLONE dan dump

**Files:**
- Create: `internal/agent/drivers/mysql/clone.go`, `internal/agent/drivers/mysql/dump.go`
- Modify: `internal/proto/mysql.go`
- Test: `internal/agent/drivers/mysql/snapshot_test.go`

**Interfaces:**
- Produces:

```go
// proto/mysql.go
type SnapshotParams struct { Host string `json:"host"`; Port int `json:"port"`; User string `json:"user"`; Password string `json:"password"`; EstimateBytes uint64 `json:"estimate_bytes,omitempty"` }
type SnapshotResult struct { Method string `json:"method"` /* clone|dump */; Bytes uint64 `json:"bytes"`; GTIDExecuted string `json:"gtid_executed"`; TookMs int64 `json:"took_ms"` }
// driver, job: "mysql.cloneFrom" → StartedJob; "mysql.dumpFrom" → StartedJob (hasil job.done.Result = SnapshotResult)
```

`cloneFrom` (dalam `jobs.Runner.Start("mysql.clone", …)`): `INSTALL PLUGIN clone` bila perlu; `SET GLOBAL clone_valid_donor_list=?`; jalankan `CLONE INSTANCE FROM ?@?:? IDENTIFIED BY ?` di goroutine dengan koneksi sendiri; sambil menunggu, tiap 2 s baca `performance_schema.clone_progress` (`stage`, `state`, `data`, `estimate`) → `Progress(percent, "tahap <stage>")`; saat CLONE mengembalikan error 3707 (restart gagal, mysqld shutdown) atau koneksi putus: anggap fase restart → `Service().WaitReady` (≤ 10 menit; bila `IsActive` false setelah 15 s → `Service().Start`); setelah siap, `SELECT STATE FROM performance_schema.clone_status` harus `Completed`, else error dengan `ERROR_MESSAGE`; hasil `GTIDExecuted` dari `@@gtid_executed`. `dumpFrom` (`"mysql.dump"`): tulis defaults-extra-file 0600 `[client] user= password= host= port=`; `Exec mysqldump --defaults-extra-file=F --single-transaction --set-gtid-purged=ON --all-databases --triggers --routines --events --skip-lock-tables --column-statistics=0` → stdout dipipakan ke `Exec mysql --defaults-extra-file=F2(lokal, socket root) --init-command=SET sql_log_bin=0` setelah `Dialect.ResetBinaryLogs()` di lokal; progress = byte yang mengalir / estimasi (`PeerDataBytes` dari params bila ada; tambah field `EstimateBytes uint64` ke SnapshotParams); hapus kedua file di `defer`. Keduanya menolak bila server lokal sedang jadi replika yang berjalan (`STOP REPLICA` dulu adalah tugas orkestrator).

- [ ] **Step 1: Tulis test** (testmysql dua instance, user repl dibuat lewat Task 7): `TestCloneFromReplicatesData` (master punya tabel 1000 baris → clone → replika punya 1000 baris, `clone_status` Completed, job.done OK, ada ≥ 1 event progress), `TestCloneFailsWithWrongPassword` (job.done Error berisi teks MySQL, tanpa restart tak terduga), `TestDumpFromReplicatesData` (sama dengan dump; `@@gtid_purged` replika == `@@gtid_executed` master), `TestSnapshotRefusesWhileReplicaRunning`.
- [ ] **Step 2–4.** Catatan: tes clone butuh harness me-restart instance (Task 5 supervisor). **Step 5: Commit** — `git commit -m "feat(mysql): clone and dump snapshot jobs"`

---

### Task 9: Kendali replikasi

**Files:**
- Create: `internal/agent/drivers/mysql/replication.go`
- Modify: `internal/proto/mysql.go`
- Test: `internal/agent/drivers/mysql/replication_test.go`

**Interfaces:**
- Produces:

```go
// proto/mysql.go
type ReplicationSourceParams struct { Host string `json:"host"`; Port int `json:"port"`; User string `json:"user"`; Password string `json:"password"`; SSL bool `json:"ssl"` }
type WaitSyncedParams struct { GTIDSet string `json:"gtid_set"`; TimeoutSec int `json:"timeout_sec"` }
type WaitSyncedResult struct { Synced bool `json:"synced"`; Executed string `json:"executed"`; Status proto.ReplicaStatus `json:"status"` }
// driver, perintah: "mysql.configureReplication", "mysql.startReplica", "mysql.stopReplica", "mysql.resetReplica" (STOP REPLICA; RESET REPLICA ALL), "mysql.waitSynced"
```

`configureReplication`: `STOP REPLICA` bila ada; `CHANGE REPLICATION SOURCE TO SOURCE_HOST=?, SOURCE_PORT=?, SOURCE_USER=?, SOURCE_PASSWORD=?, SOURCE_AUTO_POSITION=1, GET_SOURCE_PUBLIC_KEY=1, SOURCE_CONNECT_RETRY=10, SOURCE_RETRY_COUNT=86400` (+ `SOURCE_SSL=1` bila SSL). `waitSynced`: `SELECT WAIT_FOR_EXECUTED_GTID_SET(?, ?)` lalu `Status`; Synced bila hasil 0 dan IO/SQL `Yes`.

- [ ] **Step 1: Tulis test** (dua instance; replika hasil clone dari Task 8 atau kosong + dump): `TestConfigureStartWaitSynced` (insert di master → waitSynced true; Status IO/SQL Yes), `TestStopStartReplica`, `TestResetReplicaClears` (Status.Replica nil), `TestWaitSyncedTimesOut` (gtid set palsu, timeout 2 s → Synced false, tanpa error).
- [ ] **Step 2–4.** **Step 5: Commit** — `git commit -m "feat(mysql): replication control commands"`

---

### Task 10: Pasang MySQL di server kosong

**Files:**
- Create: `internal/agent/drivers/mysql/install.go`
- Modify: `internal/proto/mysql.go`
- Test: `internal/agent/drivers/mysql/install_test.go` (fake Host.Exec merekam perintah)

**Interfaces:**
- Produces:

```go
// proto/mysql.go
type InstallParams struct { Series string `json:"series"` /* 8.0|8.4|9 */; ExactVersion string `json:"exact_version,omitempty"` }
type InstallResult struct { Version string `json:"version"`; Source string `json:"source"` /* distro|mysql-repo */ }
// driver, job "mysql.install" → StartedJob
```

Aturan: debian → bila `apt-cache policy mysql-server` menyediakan seri yang diminta (Ubuntu 24.04: 8.0) pakai distro (`DEBIAN_FRONTEND=noninteractive apt-get install -y mysql-server`); bila tidak, unduh `https://repo.mysql.com/mysql-apt-config_0.8.33-1_all.deb` (versi bisa diubah lewat `DBPANEL_MYSQL_APT_CONFIG`), `debconf-set-selections` memilih seri, `dpkg -i`, `apt-get update`, `apt-get install -y mysql-server`; rhel → `dnf install -y https://repo.mysql.com/mysql84-community-release-el9-1.noarch.rpm` (map seri→paket release: 8.0 `mysql80-community-release-el<ver>`, 8.4 `mysql84-…`, 9 `mysql-innovation-community-release-el<ver>`), `dnf install -y mysql-community-server`; lalu `systemctl enable --now mysqld|mysql`; untuk rhel baca temp password dari `/var/log/mysqld.log`, ganti via `mysql --connect-expired-password` menjadi auth_socket (`ALTER USER root@localhost IDENTIFIED WITH auth_socket`); verifikasi `OpenRoot` berhasil dan `ParseVersion` seri cocok. Progress per langkah (0/25/50/75/100). Output package manager masuk `Logf("info", …)` per baris. Gagal unduh → error `server ini tidak bisa mengunduh paket MySQL; sediakan mirror lokal atau pasang manual seri <s>`.

- [ ] **Step 1: Tulis test** dengan fake Host (Exec merekam, mengembalikan output yang diatur): `TestInstallDebianUsesDistroWhenSeriesMatches` (urutan perintah apt), `TestInstallDebianAddsMySQLRepoOtherwise`, `TestInstallRHELSetsAuthSocket` (log palsu dengan temporary password → perintah ALTER USER dijalankan via mysql dengan `--connect-expired-password`, password tidak muncul di argv: harus lewat defaults-extra-file), `TestInstallFailsWhenDownloadFails` (pesan Indonesia).
- [ ] **Step 2–4.** **Step 5: Commit** — `git commit -m "feat(mysql): install MySQL on a bare server"`

---

### Task 11: Orkestrator panel: job "Buat replika"

**Files:**
- Create: `internal/panel/orchestrator/runner.go`, `internal/panel/orchestrator/create_replica.go`,
  `internal/panel/orchestrator/agentjob.go` (menunggu job agent lewat event + polling `job.status`),
  `internal/panel/orchestrator/fake_test.go` (agent palsu)
- Modify: `internal/panel/panel.go` (buat `secrets.Box` dari `ConfigDir/panel.key`, buat Runner, `Resume` saat start), `internal/panel/listener_impl.go` (OnEvent → `Runner.HandleEvent`)
- Test: `internal/panel/orchestrator/create_replica_test.go`

**Interfaces:**
- Produces:

```go
type Deps struct { Store *store.Store; Hub *hub.Hub; Secrets *secrets.Box; Publish func(event string, data any); Logger *slog.Logger; Now func() time.Time }
type Runner struct{ /* job aktif per id, mutex, waiter event */ }
func New(d Deps) *Runner
type CreateReplicaParams struct { MasterServerID, TargetServerID int64; MasterAdminUser, MasterAdminPassword string /* opsional, dipakai sekali, tidak disimpan */; Snapshot string /* auto|clone|dump */; AllowNonEmpty bool }
func (r *Runner) StartCreateReplica(ctx context.Context, p CreateReplicaParams) (jobID string, err error)   // menolak bila target sudah master/replika, master == target, atau job lain berjalan untuk target
func (r *Runner) Approve(jobID string) error   // job waiting → lanjut (restart master dengan mysql.restart, lalu tahap berikutnya)
func (r *Runner) Retry(jobID string) error     // job failed → ulangi dari CurrentStep
func (r *Runner) Cancel(jobID string) error    // running → kirim job.cancel ke agent bila ada job agent aktif; status canceled
func (r *Runner) HandleEvent(serverID int64, ev proto.Event)   // job.progress/log/done → simpan log, publish, bangunkan penunggu
func (r *Runner) Resume(ctx context.Context)   // saat panel start: jobs running → failed "panel dimulai ulang; ulangi dari tahap ini"; waiting tetap waiting
```

Tahap (nama tampil Indonesia, idx tetap): 0 `preflight_master` "Periksa master"; 1 `prepare_master` "Siapkan master"; 2 `preflight_replica` "Periksa server replika"; 3 `install_mysql` "Pasang MySQL" (skipped bila sudah ada); 4 `prepare_replica` "Siapkan replika"; 5 `snapshot` "Salin data"; 6 `connect` "Sambungkan replikasi"; 7 `verify` "Verifikasi sinkron".
Logika: 0: `mysql.preflight{role:master, repl_user}` → fail → job failed dengan Detail; `needs_restart` dicatat untuk tahap 1. 1: buat/ambil topologi (user `dbpanel_repl`, password baru 24 karakter via `secrets`), `mysql.configure{master, EnableClone, GTID}`, `mysql.createReplUser{FromHost: alamat replika}` (bila preflight menandai repl_user kurang dan tidak ada akun admin: tetap pakai root agent); bila `NeedsRestart` → status job `waiting`, step waiting, pesan `MySQL di master perlu dimulai ulang untuk mengaktifkan <alasan>. Aplikasi yang memakai database ini akan terputus beberapa detik.` → berhenti sampai `Approve` (lalu `mysql.restart` + `mysql.status` ulang). 2: `probe` target → bila `MySQL.Installed` false → tahap 3; `mysql.preflight{role:replica, peer_*}`; `needs_confirm` → job waiting dengan pesan daftar database, `Approve` berarti `AllowNonEmpty` (semua data di replika dibuang). 3: `mysql.install{series master}` job agent. 4: `mysql.configure{replica}` → `mysql.restart` (otomatis) → `mysql.setReadOnly{on}`. 5: pilih metode: `clone` bila `Snapshot!=dump` dan preflight tidak menandai `clone_patch`/plugin gagal; job agent `mysql.cloneFrom` atau `mysql.dumpFrom` dengan kredensial repl dari topologi (dibuka dari Box); tunggu `job.done` (event) dengan fallback polling `job.status` tiap 10 s; agent offline > 2 menit → failed `agent replika terputus saat <tahap>`. 6: `mysql.configureReplication` + `mysql.startReplica`. 7: `mysql.status` master → `gtid_executed`; `mysql.waitSynced{gtid_set, 600}` di replika; Synced → `SetReplicaState(ready)`, job succeeded; else failed dengan Last*Error. Setiap panggilan agent gagal → step failed, job failed, pesan Indonesia dengan nama tahap dan teks error MySQL (mentah) di log. Publish `job_update {id,status,step,message}` pada tiap perubahan dan `job_log {id,line}` per baris.

- [ ] **Step 1: Tulis test** dengan agent palsu (`hub.NewLocalConn` + registry yang handler-nya skrip tabel per nama perintah, bisa diprogram per tes untuk gagal/menunda/memancarkan event lewat `hub.OnEvent`): `TestCreateReplicaHappyPath` (8 tahap done, replica ready, topology dibuat, password tersegel bukan plaintext di DB), `TestStopsWaitingWhenMasterNeedsRestart` (status waiting, tidak ada `mysql.restart` sampai Approve; setelah Approve restart dipanggil dan lanjut), `TestWaitsConfirmWhenReplicaHasData`, `TestInstallsMySQLWhenMissing` (tahap 3 dijalankan; sebaliknya skipped), `TestFailsWhenAgentDropsDuringSnapshot` (fake tidak pernah kirim job.done dan Hub offline → failed dengan pesan tahap "Salin data"; `Retry` mulai lagi dari tahap 5), `TestFallsBackToDumpWhenClonePatchMismatch`, `TestResumeMarksRunningFailed`, `TestCancelSendsJobCancel`, `TestRejectsTargetAlreadyReplica`.
- [ ] **Step 2–4** (`-race`). **Step 5: Commit** — `git commit -m "feat(panel): create-replica orchestrator"`

---

### Task 12: UI: master, buat replika, job

**Files:**
- Create: `web/templates/master_detail.html`, `web/templates/replica_new.html`,
  `web/templates/job_detail.html`, `web/templates/jobs.html`,
  `internal/panel/web/handlers_master.go`, `internal/panel/web/handlers_jobs.go`
- Modify: `internal/panel/web/router.go`, `web/templates/server_detail.html`
  (tombol "Jadikan master", tautan ke master/replika), `web/templates/dashboard.html`
  (kartu jumlah master/replika/job berjalan), `web/templates/layout.html` (menu Pekerjaan),
  `web/static/app.js` (SSE `job_update`/`job_log` → DOM), `internal/panel/web/router.go` (Deps + `Orchestrator`)
- Test: `internal/panel/web/web_master_jobs_test.go`

**Interfaces:**
- Consumes: `orchestrator.Runner` lewat antarmuka kecil di web:

```go
type Orchestrator interface {
    StartCreateReplica(ctx context.Context, p orchestrator.CreateReplicaParams) (string, error)
    Approve(jobID string) error; Retry(jobID string) error; Cancel(jobID string) error
}
// web.Deps += Orchestrator Orchestrator
```
Rute: `POST /servers/{id}/make-master` (CreateTopology tanpa kredensial: repl user dibuat saat job pertama; menolak bila belum paired/online atau probe menunjukkan MySQL tidak terpasang: `MySQL belum terpasang di server ini`) → redirect `/masters/{id}`; `GET /masters/{id}` (kesehatan ringkas dari probe, daftar replika + state, tombol Buat replika, riwayat job); `GET /masters/{id}/replicas/new` (pilih server tujuan: paired, online, unassigned; opsi snapshot auto/clone/dump; field opsional user+password admin master dengan keterangan "dipakai sekali, tidak disimpan"); `POST /masters/{id}/replicas` → `StartCreateReplica` → redirect `/jobs/{jobID}`; `GET /jobs` (50 terbaru); `GET /jobs/{id}` (tahap dengan status/pesan, log hidup, tombol sesuai status: Setujui & lanjutkan (waiting), Ulangi dari tahap ini (failed), Batalkan (running)); `POST /jobs/{id}/approve|retry|cancel`. SSE: `job_update`, `job_log`. Status job dalam bahasa Indonesia: Antre, Berjalan, Menunggu persetujuan, Selesai, Gagal, Dibatalkan.

- [ ] **Step 1: Tulis test** (fake Orchestrator): `TestMakeMasterCreatesTopology`, `TestMakeMasterRefusesWithoutMySQL`, `TestNewReplicaFormListsEligibleServers` (hanya paired+unassigned; yang offline ditandai nonaktif), `TestCreateReplicaStartsJobAndRedirects`, `TestJobDetailShowsStepsAndButtons` (waiting → tombol Setujui), `TestJobApproveRetryCancelPost` (CSRF + panggilan ke fake), `TestJobsListRequiresLogin`.
- [ ] **Step 2–4.** Jalankan `scripts/tailwind.sh` bila kelas baru dipakai; commit `app.css`. **Step 5: Commit** — `git commit -m "feat(web): master page, create replica flow, job pages"`

---

### Task 13: Integrasi end-to-end dengan mysqld nyata

**Files:**
- Create: `internal/panel/e2e_replica_test.go`
- Modify: `Makefile` (target `test-e2e`: `go test -run E2E -count=1 ./internal/panel/`), `README.md` (cara menjalankan tes integrasi; butuh mysqld lokal)

- [ ] **Step 1: Tulis test** `TestE2ECreateReplica` (skip bila `testmysql` tidak menemukan mysqld atau `-short`): dua instance testmysql (master: gtid OFF, server_id 1; replika: kosong) ← masing-masing dilayani oleh agent nyata (`agent.New` dengan registry berisi `probe` palsu yang melaporkan MySQL terpasang + `mysql.Driver{Host: inst.Host()}` + `jobs.Runner`) yang dipasangkan ke panel nyata (pola Rencana 1 `TestRemoteAgentPairsAndIsProbed`); lewat HTTP: login, make-master, buat replika (snapshot auto); tunggu job succeeded ≤ 5 menit; assert: baris yang disisipkan di master sesudah job muncul di replika ≤ 10 s; `super_read_only` ON di replika; password repl di DB tersegel; log job tidak memuat password.
- [ ] **Step 2: Jalankan `make test-e2e`** di lingkungan ini; cantumkan output (durasi) di laporan.
- [ ] **Step 3: Commit** — `git commit -m "test: end-to-end create replica against real mysqld"`

---

## Catatan untuk pelaksana

- Spec 9.10 meminta antarmuka `Driver` lintas engine. Di rencana ini
  driver MySQL dibangun langsung sebagai kumpulan perintah `mysql.*`
  dengan tipe wire di `proto/mysql.go`; antarmuka lintas engine ditarik
  saat engine kedua benar-benar masuk (YAGNI), dan orkestrator hanya
  memanggil nama perintah, sehingga penarikan itu tidak mengubah panel.

- Rencana 3 (pemantauan dan Telegram) akan memakai `mysql.status` tiap 5 detik dan menambah heartbeat; jangan bangun itu di sini.
- Pesan error MySQL mentah selalu masuk log job; teks untuk user di step message memakai bahasa awam plus nomor error.
- Jangan menulis password apa pun ke `job_log`; uji ini ada di Task 13.
