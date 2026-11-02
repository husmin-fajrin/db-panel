# Rencana Implementasi 3: Pemantauan, Notifikasi Telegram, Kontrol Dasar

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Agent memeriksa MySQL-nya sendiri tiap 15 detik dan melapor
hanya saat berubah; panel menurunkan status awam untuk tiap master dan
replika, mencatat garis waktu dan audit, membuka/menutup peringatan,
mengirim Telegram ke satu grup paling lama 1 menit setelah masalah, dan
menyediakan tombol Stop/Start, Kunci kembali, Buka kunci master, Resync
ulang, Lepas replika, Hapus server, Jangan ganggu, serta ringkasan
mingguan. Agent master menolak perintah yang merusak datanya (peran yang
dipin), agent replika hanya menerima sumber yang tercatat. Hutang
Rencana 2 yang murah dibayar.

**Architecture:** Paket agent baru `internal/agent/monitor` menjalankan
loop pemeriksaan di atas `mysql.Driver.Snapshot` dan memancarkan event
`monitor.snapshot` ke panel (ring buffer saat panel tidak tersambung).
Paket panel baru `internal/panel/monitor` menerima event, menyimpan
laporan terakhir, menurunkan status (fungsi murni, teruji dengan jam
palsu), menulis `status_events`, menjalankan mesin peringatan, dan
menerbitkan SSE. Paket `internal/panel/notify` mengirim Telegram dari
antrean peringatan. Kontrol adalah perintah agent langsung lewat
orkestrator (Stop/Start/kunci) atau job baru `resync_replica` yang
memakai ulang tahap `create_replica`. Pin peran/sumber hidup di agent
sebagai dua berkas di `/etc/dbpanel`, ditegakkan di handler driver.

**Tech Stack:** Go 1.26.10, `CGO_ENABLED=0`, SQLite (`modernc.org/sqlite`),
`net/http` untuk Bot API Telegram; daisyUI 5 / Tailwind 4 / Alpine CSP
yang sudah ada; harness `testmysql` (mysqld 8.0.46 di `/usr/sbin`).

**Spec:** `docs/superpowers/specs/2026-10-31-rencana-3-pemantauan-notifikasi-kontrol-design.md`
(semua bagian; angka di bawah merujuk ke sana). Spec utama:
`docs/superpowers/specs/2026-10-30-db-replica-panel-design.md` (bagian 4,
6.5, 8, 10b). Ledger Rencana 2:
`docs/superpowers/ledgers/2026-10-30-02-buat-replika-mysql-ledger.md`
(residual 4, 5, 6; M2, M3; celah akun dump). Audit:
`docs/superpowers/security/2026-10-30-audit-plan-2.md` (SEC-17, SEC-28
butir 1 dan 3, SEC-35).

## Global Constraints

- Go 1.26.10, `CGO_ENABLED=0`, pure Go; modul `github.com/husmin-fajrin/db-panel`.
- Pemeriksaan agent hanya memakai `SHOW GLOBAL STATUS`, `SHOW GLOBAL
  VARIABLES`, `SHOW REPLICA STATUS`, `SHOW BINARY LOGS`,
  `performance_schema`/`information_schema` thread list, `SELECT 1`.
  Tidak ada `SELECT` ke tabel pengguna, tidak ada `COUNT(*)`, tidak ada
  tulisan. Timeout tiap perintah 5 dtk, satu koneksi dipakai ulang.
- Tidak ada perbaikan otomatis: satu-satunya perintah tanpa klik manusia
  adalah perintah baca status (`monitor.*`).
- Nilai bawaan: `check_every=15s`, `summary_every=120s`,
  `lag_threshold=60s`, `disconnect_alert=3m`, `lagging_alert=5m`,
  `down_after=30s`, agent offline 30 dtk, laporan basi 3 × `summary_every`,
  `alert_repeat=6h`, disk 85%/95% (tutup 80%/90%), koneksi 80% selama 5
  menit (tutup 70%), batas binlog 80% `binlog_expire_sec`, ring buffer 200,
  payload laporan maks 64 KiB, `status_events` 90 hari, rate limit 1/10
  dtk untuk "Kirim tes" dan "Periksa sekarang", Telegram timeout 10 dtk
  dan coba ulang tiap 60 dtk, ringkasan mingguan Senin 07:00 waktu panel,
  penyapuan akun `dbpanel_dump_%` lebih tua dari 24 jam.
  `monitor.configure` hanya menerima 10–3600 dtk.
- Status replika (urutan prioritas, label persis): Belum siap, Agent
  offline, Agent perlu dimutakhirkan, Tidak terjangkau, Berhenti,
  Dihentikan, Berhenti (di luar panel), Terputus, Ketinggalan, Sinkron.
  Penanda terpisah: Kunci terlepas. Status master: Agent offline, Diduga
  tumbang, Tidak terjangkau, Terkunci, Sehat.
- Telegram: teks polos, tanpa Markdown; hanya nama server, status, nomor
  error, kalimat awam, waktu, tautan panel. Pesan error mentah MySQL tidak
  pernah dikirim. Satu bot, satu chat id. Token disegel dengan
  `panel.key`, tidak pernah ke log, SSE, atau halaman setelah disimpan.
- Pin peran: `master` tidak pernah bisa diubah oleh perintah panel;
  `replica` → `unassigned` hanya lewat `mysql.releaseRole` saat tidak ada
  konfigurasi replikasi dan `binlog_dump_threads=0`. Pin sumber hanya
  dihapus lewat `releaseRole`. Perubahan lain hanya lewat CLI lokal
  `dbpanel agent set-role` / `set-source`.
- `audit_log` tahan ubah (trigger `BEFORE UPDATE`/`BEFORE DELETE`
  `RAISE(ABORT)`), tidak pernah dipangkas, `detail_json` tanpa sandi/token.
- Teks UI dan pesan error untuk user: Indonesia; log: Inggris. Semua
  halaman baru di balik login dan CSRF yang ada (`authed`/`authedPost`).
- Setiap task diakhiri `make test` hijau dan commit. Tes yang butuh
  `mysqld` dilewati bila tidak ada dan WAJIB benar-benar jalan di
  lingkungan pengembangan ini. Paket `internal/panel/monitor`,
  `internal/panel/notify`, `internal/agent/monitor` diuji dengan `-race`.

## Review Focus

1. Jam server agent yang melenceng jauh dari jam panel: garis waktu
   memakai `taken_ago_ms`, bukan jam absolut agent, sehingga laporan
   susulan tetap urut (Task 5, `TestBacklogUsesPanelClock`).
2. Agent Rencana 2 yang belum dimutakhirkan: panel menampilkan "Agent
   perlu dimutakhirkan", tidak menampilkan "Sinkron" palsu, dan tidak
   membuka peringatan tiap 2 menit (Task 5, `TestOldAgentNeedsUpgrade`).
3. Panel mati 2 jam lalu hidup: agent mengirim 200 laporan susulan
   sekaligus; panel tidak mengirim 200 pesan Telegram, hanya keadaan
   akhir tiap peringatan (Task 6, `TestBacklogCollapsesAlerts`).
4. Replika berhenti karena `STOP REPLICA` yang dijalankan orang di
   terminal (bukan lewat panel): status "Berhenti (di luar panel)" dan
   peringatan terbuka; Stop lewat panel: "Dihentikan" tanpa peringatan
   (Task 4, `TestStoppedOutsidePanel`; Task 16 E2E).
5. Token Telegram salah atau panel tanpa internet: peringatan tetap
   tampil dengan "belum terkirim" dan alasannya, coba ulang tiap 60 dtk,
   tidak ada kebocoran token di log atau halaman (Task 7,
   `TestSendFailureIsVisibleAndRetried`, `TestTokenNeverLogged`).

---

### Task 1: Store: migrasi 0004 dan metode baru

**Files:**
- Create: `internal/panel/store/migrations/0004_monitoring.sql`,
  `internal/panel/store/health.go`, `internal/panel/store/alerts.go`,
  `internal/panel/store/audit.go`
- Modify: `internal/panel/store/topologies.go` (`Replica.Paused`,
  `ReplicaResyncing`, `SetReplicaPaused`), `internal/panel/store/servers.go`
  (`Server.SnoozeUntil`, `SetSnooze`), `internal/panel/store/settings.go`
- Test: `internal/panel/store/health_test.go`, `alerts_test.go`,
  `audit_test.go`, `migrate_test.go`

**Interfaces:**
- Produces:

```go
// 0004_monitoring.sql (spec 5.7)
// server_health(server_id PK REFERENCES servers ON DELETE CASCADE, received_at TEXT, snapshot_json TEXT, status TEXT, status_since TEXT, lock_ok INTEGER, agent_version TEXT)
// status_events(id PK AUTOINCREMENT, server_id, topology_id, ts, kind, from_status, to_status, detail); INDEX (server_id, ts)
// alerts(id PK AUTOINCREMENT, kind, server_id, topology_id, opened_at, closed_at, last_notified_at, notify_count INTEGER DEFAULT 0, message, telegram_state TEXT DEFAULT 'pending', telegram_error TEXT DEFAULT ''); INDEX (server_id, kind, closed_at)
// audit_log(id PK AUTOINCREMENT, ts, username, ip, action, target_kind, target_id INTEGER, detail_json, outcome); TRIGGER audit_log_no_update BEFORE UPDATE ... RAISE(ABORT, 'audit_log is append-only'); TRIGGER audit_log_no_delete BEFORE DELETE ... RAISE(ABORT, ...)
// ALTER TABLE replicas ADD COLUMN paused INTEGER NOT NULL DEFAULT 0; ALTER TABLE servers ADD COLUMN snooze_until TEXT
const ReplicaResyncing = "resyncing"
type Health struct { ServerID int64; ReceivedAt time.Time; SnapshotJSON string; Status string; StatusSince time.Time; LockOK bool; AgentVersion string }
func (s *Store) SetHealth(h Health) error                         // upsert
func (s *Store) GetHealth(serverID int64) (Health, error)         // ErrNotFound
func (s *Store) ListHealth() ([]Health, error)
type StatusEvent struct { ID, ServerID, TopologyID int64; TS time.Time; Kind, FromStatus, ToStatus, Detail string }
func (s *Store) AddStatusEvent(e StatusEvent) (int64, error)
func (s *Store) ListStatusEvents(serverID int64, since time.Time, limit int) ([]StatusEvent, error)  // terbaru dulu
func (s *Store) ListStatusEventsAll(since time.Time) ([]StatusEvent, error)   // untuk ringkasan mingguan, urut ts naik
func (s *Store) PruneStatusEvents(olderThan time.Time) (int64, error)
type Alert struct { ID int64; Kind string; ServerID, TopologyID int64; OpenedAt time.Time; ClosedAt, LastNotifiedAt *time.Time; NotifyCount int; Message, TelegramState, TelegramError string }
const ( TelegramPending = "pending"; TelegramSent = "sent"; TelegramFailed = "failed" )
func (s *Store) OpenAlert(a Alert) (int64, error)                 // gagal bila sudah ada yang terbuka untuk (kind, server_id): ErrAlertOpen
func (s *Store) GetOpenAlert(kind string, serverID int64) (Alert, error)
func (s *Store) CloseAlert(id int64, now time.Time) error
func (s *Store) ListOpenAlerts() ([]Alert, error)
func (s *Store) ListAlerts(since time.Time, limit int) ([]Alert, error)
func (s *Store) SetAlertNotified(id int64, now time.Time, state, errText string) error   // notify_count++ bila state == sent
func (s *Store) SetAlertMessage(id int64, message string) error
type AuditEntry struct { ID int64; TS time.Time; Username, IP, Action, TargetKind string; TargetID int64; DetailJSON, Outcome string }
func (s *Store) AddAudit(e AuditEntry) (int64, error)
func (s *Store) ListAudit(serverID int64, since time.Time, limit int) ([]AuditEntry, error)   // serverID 0 = semua; terbaru dulu
func (s *Store) SetReplicaPaused(serverID int64, paused bool) error
func (s *Store) SetSnooze(serverID int64, until *time.Time) error
// settings.go: kunci konstanta SettingTelegramTokenEnc = "telegram_token_enc", SettingTelegramChatID = "telegram_chat_id", SettingTelegramKinds = "telegram_kinds_json", SettingPanelBaseURL = "panel_base_url", SettingMonitorCheckEvery = "monitor_check_every", SettingMonitorSummaryEvery = "monitor_summary_every", SettingLagThreshold = "lag_threshold_sec", SettingDisconnectAlert = "disconnect_alert_sec", SettingDownAfter = "down_after_sec", SettingDiskWarn = "disk_warn_pct", SettingDiskCrit = "disk_crit_pct", SettingWeeklySummary = "weekly_summary", SettingWeeklySummaryAt = "weekly_summary_at", SettingAlertRepeatHours = "alert_repeat_hours"
func (s *Store) GetSettings(keys ...string) (map[string]string, error)
```

- [ ] **Step 1: Tulis test**: `TestMigrate0004Applies` (versi 4, tabel ada, kolom `paused`/`snooze_until`), `TestHealthUpsert`, `TestStatusEventsOrderAndPrune`, `TestOpenAlertRefusesDuplicateOpen`, `TestCloseAlertThenReopen`, `TestAuditIsAppendOnly` (UPDATE dan DELETE gagal dengan error berisi "append-only"), `TestReplicaPausedRoundTrip`, `TestSnoozeRoundTrip`, `TestDeleteServerCascadesHealthAndAlerts`.
- [ ] **Step 2: Jalankan, pastikan gagal.** **Step 3: Implementasi.** **Step 4: Lulus.**
- [ ] **Step 5: Commit** — `git commit -m "feat(store): health, status events, alerts and audit log (migration 0004)"`

---

### Task 2: Agent: `HealthSnapshot` dari mysqld dan sistem

**Files:**
- Create: `internal/agent/drivers/mysql/health.go`, `internal/proto/health.go`
- Modify: `internal/agent/drivers/mysql/driver.go` (field `Sys` dipakai; simpan koneksi pemantau), `internal/agent/probe/system.go` (tidak berubah; `ReadFile`/`Statfs` dipakai)
- Test: `internal/agent/drivers/mysql/health_test.go` (testmysql)

**Interfaces:**
- Produces:

```go
// package proto
type HealthSnapshot struct {
    MySQLOK bool `json:"mysql_ok"`; MySQLError string `json:"mysql_error,omitempty"`
    Version, ServerUUID string; UptimeSec int64; ThreadsConnected, MaxConnections, ThreadsRunning int
    ReadOnly, SuperReadOnly, LogBin bool; GTIDExecuted string; BinlogDumpThreads int
    BinlogTotalBytes uint64; BinlogOldestAgeSec *int64 `json:"binlog_oldest_age_sec,omitempty"`; BinlogExpireSec int64
    Replica *ReplicaStatus `json:"replica,omitempty"`
    DiskTotal, DiskFree, MemTotal, MemAvailable uint64; Load1 float64
    AgentVersion string
}
// package mysql
// Snapshot reads one HealthSnapshot with the stored admin account, reusing
// one connection across calls (dropped and reopened after a failure). A
// MySQL that does not answer within 5 s yields MySQLOK=false with the error
// text, not an error. ErrNoHost / no stored credential: MySQLError = "belum terhubung".
func (d *Driver) Snapshot(ctx context.Context) proto.HealthSnapshot
func systemStats(sys probe.System, datadir string) (diskTotal, diskFree, memTotal, memAvail uint64, load1 float64)   // /proc/meminfo, /proc/loadavg, Statfs; kegagalan → 0
func binlogOldestAge(sys probe.System, logBinBasename string, now time.Time) *int64   // mtime berkas <basename>.NNNNNN tertua; nil bila tak terbaca
```

- [ ] **Step 1: Tulis test** (testmysql): `TestSnapshotMaster` (mysqld dengan log_bin: `LogBin`, `BinlogExpireSec`, `BinlogTotalBytes>0`, `BinlogOldestAgeSec` tidak nil, `MaxConnections>0`, `Replica==nil`, disk/mem > 0), `TestSnapshotReplica` (instance kedua dengan `CHANGE REPLICATION SOURCE TO` ke yang pertama: `Replica.IORunning`, `SQLRunning` terisi), `TestSnapshotMySQLDownIsNotError` (instance dihentikan → `MySQLOK=false`, `MySQLError` berisi teks, tidak panic; koneksi dibuka lagi setelah instance hidup), `TestSnapshotNoCredential` (`MySQLError=="belum terhubung"`), `TestBinlogOldestAgeUnreadable` (basename palsu → nil), `TestSystemStatsFromFakeProc` (probe.System palsu).
- [ ] **Step 2: Jalankan, pastikan gagal.** **Step 3: Implementasi.** **Step 4: Lulus.**
- [ ] **Step 5: Commit** — `git commit -m "feat(agent): health snapshot of MySQL and host"`

---

### Task 3: Agent: loop pemantau, deteksi perubahan, ring buffer, perintah `monitor.*`

**Files:**
- Create: `internal/agent/monitor/monitor.go`, `internal/agent/monitor/changes.go`,
  `internal/agent/monitor/buffer.go`
- Modify: `internal/agent/cli.go` (`mainRun`: buat dan jalankan Monitor),
  `internal/panel/localagent.go` (agent tertanam: sama), `internal/proto/health.go`
- Test: `internal/agent/monitor/monitor_test.go`, `changes_test.go`, `buffer_test.go`

**Interfaces:**
- Consumes: `(*mysql.Driver).Snapshot`, `(*agent.Agent).Emit` / `localEmitter`.
- Produces:

```go
// package proto
const EventHealth = "monitor.snapshot"
type HealthEvent struct { Seq uint64 `json:"seq"`; TakenAgoMS int64 `json:"taken_ago_ms"`; Backlog bool `json:"backlog"`; Snapshot HealthSnapshot `json:"snapshot"` }
type MonitorConfigureParams struct { CheckEverySec, SummaryEverySec, LagThresholdSec int; RoleHint string `json:"role_hint,omitempty"` }   // 10..3600; RoleHint "master"|"replica"|"" (dipakai Task 9)
// package monitor (agent)
type Source interface{ Snapshot(ctx context.Context) proto.HealthSnapshot }
type Emitter interface{ Emit(ev proto.Event) error }       // ErrOffline → simpan ke buffer
type Options struct { CheckEvery, SummaryEvery, LagThreshold time.Duration; Now func() time.Time }   // bawaan 15s/120s/60s; TakenAgoMS = Now()-takenAt saat dikirim
func New(src Source, em Emitter, logger *slog.Logger, o Options) *Monitor
func (m *Monitor) Register(reg *commands.Registry)          // "monitor.configure" → (flush buffer, kembalikan HealthEvent terkini); "monitor.snapshot" → HealthEvent terkini (memaksa pemeriksaan baru)
func (m *Monitor) Run(ctx context.Context)                 // loop; tick pertama segera
func (m *Monitor) Configure(p proto.MonitorConfigureParams) error   // validasi 10..3600
// changes.go
func changed(prev, cur proto.HealthSnapshot, lagThreshold time.Duration, diskWarn, diskCrit, connPct int) bool   // aturan spec 5.1 "Kapan melapor"; prev kosong → true
// buffer.go
type ring struct{}  // kapasitas 200; Push(HealthEvent) membuang yang terlama; Drain() []HealthEvent urut lama→baru
```

Laporan yang dikirim membawa `Seq` naik monoton per proses agent;
`TakenAgoMS` = `Now()-takenAt` saat dikirim; `Backlog=true` untuk entri
dari buffer. Flush buffer dicoba pada tiap tick dan di handler
`monitor.configure`; entri yang gagal dikirim tetap di buffer.

- [ ] **Step 1: Tulis test** dengan Source dan Emitter palsu serta jam palsu: `TestFirstTickReportsImmediately`, `TestNoReportWhenUnchanged` (3 tick tanpa perubahan → 1 laporan, lalu ringkasan tepat pada `SummaryEvery`), `TestReportsOnIOStateChange`, `TestReportsOnLagCrossingThreshold` (59→61 dtk ya; 61→70 tidak), `TestReportsOnDiskCrossing85`, `TestReportsOnUptimeDecrease`, `TestReportsOnDumpThreadsChange`, `TestOfflineBuffersChangesOnly` (Emit ErrOffline: perubahan masuk buffer, ringkasan tidak; buffer maks 200), `TestConfigureFlushesBacklogInOrder` (`Backlog=true`, `TakenAgoMS` naik untuk entri lebih lama), `TestConfigureRejectsOutOfRange`, `TestSnapshotCommandForcesCheck`.
- [ ] **Step 2: Jalankan, pastikan gagal.** **Step 3: Implementasi**, pasang di `mainRun` dan `attachLocal` (goroutine dengan ctx agent). **Step 4: Lulus** (`-race`).
- [ ] **Step 5: Commit** — `git commit -m "feat(agent): monitor loop reporting health changes to the panel"`

---

### Task 4: Panel: penurunan status dan penjelasan awam (fungsi murni)

**Files:**
- Create: `internal/panel/monitor/status.go`, `internal/panel/monitor/explain.go`
- Test: `internal/panel/monitor/status_test.go`, `explain_test.go`

**Interfaces:**
- Produces:

```go
// package monitor (panel)
const (
    StatusNotReady = "Belum siap"; StatusAgentOffline = "Agent offline"; StatusNeedsUpgrade = "Agent perlu dimutakhirkan"
    StatusUnreachable = "Tidak terjangkau"; StatusStopped = "Berhenti"; StatusPaused = "Dihentikan"; StatusStoppedOutside = "Berhenti (di luar panel)"
    StatusDisconnected = "Terputus"; StatusLagging = "Ketinggalan"; StatusSynced = "Sinkron"
    StatusDown = "Diduga tumbang"; StatusLocked = "Terkunci"; StatusHealthy = "Sehat"
)
type Severity int // SevOK, SevWarn, SevCrit, SevNeutral
type Status struct { Code string; Severity Severity; Detail string; LockLost bool /* replika: super_read_only OFF */; LagSec *int64; GTIDGap int /* transaksi master yang belum diterima */ }
type Thresholds struct { Lag, DisconnectAlert, DownAfter, StaleAfter time.Duration; DiskWarn, DiskCrit, ConnPct int }
type ReplicaInput struct { State string /* store.Replica.State */; Paused bool; AgentOnline bool; OfflineSince, ReceivedAt time.Time; NeedsUpgrade bool; Snap *proto.HealthSnapshot; MasterGTIDExecuted string; GapSince time.Time; Now time.Time }
type MasterInput struct { AgentOnline bool; OfflineSince, ReceivedAt time.Time; NeedsUpgrade bool; Snap *proto.HealthSnapshot; ReplicasDisconnected int; Now time.Time }
func DeriveReplica(in ReplicaInput, th Thresholds) Status
func DeriveMaster(in MasterInput, th Thresholds) Status
func GTIDGap(masterExecuted, replicaRetrieved string) int   // jumlah transaksi di master yang tidak ada di replika; parsing rentang "uuid:1-5,7"; 0 bila salah satu kosong
type Explanation struct { Text, Advice string; Primary string /* "resync" | "start" | "master" | "" */ }
func Explain(sqlErrno, ioErrno int) Explanation              // tabel spec 5.2; tak dikenal → Text "Penyebab belum dikenali panel.", Advice "", Primary ""
func SeverityOf(code string) Severity
```

Detail status: `Detail` berisi kalimat awam (misal "Terputus 4 menit;
MySQL akan menyambung sendiri", "agent tidak terhubung 3 menit dan 2
replika kehilangan koneksi"). Terputus menjadi `SevCrit` setelah
`DisconnectAlert`. Ketinggalan juga bila `GTIDGap>0` lebih lama dari
`th.Lag` (`GapSince`).

- [ ] **Step 1: Tulis test** tabel: `TestDeriveReplicaPriority` (setiap baris spec 5.2 dalam urutan, termasuk `TestStoppedOutsidePanel` sebagai kasus bernama), `TestDisconnectedTurnsCritAfter3Min`, `TestLaggingByGTIDGap` (SecondsBehind 0 tetapi gap selama > ambang), `TestStaleReportIsAgentOffline`, `TestDeriveMasterDownAfterThreshold` dengan `ReplicasDisconnected` masuk Detail, `TestMasterLocked`, `TestGTIDGapParsing` (rentang, banyak uuid, kosong), `TestExplainTable` (1062, 1032, 1236, 1594, 1021, 1114, 3, 1045, 1593, 2003, 2013, 9999).
- [ ] **Step 2–4.** **Step 5: Commit** — `git commit -m "feat(panel): replica and master status derivation with plain-language explanations"`

---

### Task 5: Panel: penerimaan laporan, garis waktu, SSE, `monitor.configure` saat online

**Files:**
- Create: `internal/panel/monitor/monitor.go`, `internal/panel/monitor/settings.go`
- Modify: `internal/panel/panel.go` (buat Monitor, `Run` menjalankan `Monitor.Run`),
  `internal/panel/listener_impl.go` (`onEvent` → `Monitor.HandleEvent`; `setOnline` → `Monitor.OnOnline/OnOffline`),
  `internal/panel/servers.go`
- Test: `internal/panel/monitor/monitor_test.go`, `settings_test.go`,
  `internal/panel/panel_test.go`

**Interfaces:**
- Consumes: Task 1 store, Task 3 event, Task 4 fungsi.
- Produces:

```go
type Settings struct { CheckEvery, SummaryEvery time.Duration; Thresholds; AlertRepeat time.Duration; LaggingAlert time.Duration; WeeklySummary bool; WeeklySummaryAt string; PanelBaseURL string }
func LoadSettings(s *store.Store) (Settings, error)         // bawaan dari Global Constraints bila kosong
func SaveSettings(s *store.Store, st Settings) error
type Deps struct { Store *store.Store; Hub *hub.Hub; Publish func(event string, data any); Logger *slog.Logger; Now func() time.Time; TickEvery time.Duration /* 5s */ }
type Monitor struct{}
func New(d Deps) *Monitor
func (m *Monitor) Run(ctx context.Context)                 // tick: hitung ulang status berbasis waktu, pangkas status_events 90 hari sekali sehari
func (m *Monitor) HandleEvent(serverID int64, ev proto.Event) bool   // true bila EventHealth; menolak payload > 64 KiB (log Warn, abaikan)
func (m *Monitor) OnOnline(serverID int64)                 // goroutine: hub.Call monitor.configure{settings, RoleHint: servers.role}; jawaban = laporan pertama; error "unknown command" → NeedsUpgrade
func (m *Monitor) OnOffline(serverID int64)
func (m *Monitor) Reconfigure()                            // setelah SaveSettings: kirim monitor.configure ke semua agent online
func (m *Monitor) CheckNow(ctx context.Context, serverID int64) error   // hub.Call monitor.snapshot; rate limit 1/10 dtk per server
type View struct { ServerID int64; Role string; Status Status; Since, ReceivedAt time.Time; Snap *proto.HealthSnapshot; NeedsUpgrade bool; SnoozeUntil *time.Time }
func (m *Monitor) View(serverID int64) (View, bool)
func (m *Monitor) Views() map[int64]View
```

Per laporan: `received = Now() - TakenAgoMS`; simpan `server_health`;
turunkan status (replika: ambil `MasterGTIDExecuted` dari View master
topologinya); bila status atau `LockLost` berubah → `status_events`
(`kind` = `status`|`lock`|`restart`|`disk`|`connections`|`agent`) dan
publish `health_update{server_id, status, severity}`. Laporan `Backlog`
diproses urut tanpa publish per entri (satu publish di akhir).

- [ ] **Step 1: Tulis test** dengan store sementara, hub palsu (seperti `fake_test.go` orkestrator) dan jam palsu: `TestSnapshotStoredAndStatusDerived`, `TestStatusChangeWritesEventAndPublishes`, `TestBacklogUsesPanelClock` (dua entri backlog `TakenAgoMS` 7200000 dan 60000 → `ts` event berbeda 119 menit, tidak memakai jam agent), `TestOldAgentNeedsUpgrade` (hub menjawab error unknown command → View `NeedsUpgrade`, status "Agent perlu dimutakhirkan", tidak ada event tiap tick), `TestOfflineThenDownAfterThreshold` (tick memajukan jam), `TestReplicaUsesMasterGTIDFromMasterView`, `TestOversizedPayloadIgnored`, `TestSettingsDefaultsAndRoundTrip`, `TestReconfigureSendsToOnlineAgents`, `TestCheckNowRateLimited`.
- [ ] **Step 2–4** (`-race`). **Step 5: Commit** — `git commit -m "feat(panel): ingest agent health reports into status, timeline and SSE"`

---

### Task 6: Mesin peringatan

**Files:**
- Create: `internal/panel/monitor/alerts.go`
- Modify: `internal/panel/monitor/monitor.go` (panggil `Evaluate` setelah tiap laporan dan tiap tick)
- Test: `internal/panel/monitor/alerts_test.go`

**Interfaces:**
- Produces:

```go
const ( KindReplicaStopped = "replica_stopped"; KindReplicaDisconnected = "replica_disconnected"; KindReplicaLagging = "replica_lagging"; KindReplicaUnlocked = "replica_unlocked"; KindMasterLocked = "master_locked"; KindMasterDown = "master_down"; KindMySQLUnreachable = "mysql_unreachable"; KindAgentOffline = "agent_offline"; KindDiskWarn = "disk_warn"; KindDiskCrit = "disk_crit"; KindConnectionsHigh = "connections_high"; KindBinlogDeadline = "binlog_deadline"; KindJobFailed = "job_failed" )
func AllKinds() []string
type alertRule struct { kind string; open func(v View, ctx ruleCtx) (bool, string /* pesan */); close func(v View, ctx ruleCtx) bool }
func (m *Monitor) Evaluate(now time.Time)                  // untuk tiap server: buka (OpenAlert) atau tutup (CloseAlert) menurut spec 5.3; publish alert_update{id, kind, server_id, open}
func (m *Monitor) JobFailed(serverID int64, jobID, kind, errText string)   // dipanggil orkestrator (Task 11) → buka job_failed (pesan: jenis job + nama tahap, tanpa error mentah)
func (m *Monitor) JobEnded(serverID int64, jobID string)                      // job diulang dan berhasil, atau dibatalkan → tutup job_failed
func (m *Monitor) Snooze(serverID int64, until *time.Time) error
```

`binlog_deadline`: replika dalam Terputus/Berhenti/Dihentikan sejak
`Since` lebih lama dari 80% `BinlogExpireSec` master (0 → tidak pernah).
Pesan peringatan dibentuk dari `Explain` (replica_stopped) dan Detail
status; tidak pernah memuat `LastSQLError`/`LastIOError` mentah. Snooze
tidak mencegah buka/tutup, hanya pengiriman (Task 7).

- [ ] **Step 1: Tulis test** dengan jam palsu: `TestOpenOnceWhileCondition` (status tetap Berhenti 10 tick → 1 alert), `TestCloseOnRecovery`, `TestDisconnectedOpensAfter3Min`, `TestLaggingOpensAfter5MinClosesAfter1MinBelow`, `TestDiskHysteresis` (86→84 tetap terbuka, 79 tutup), `TestConnectionsHigh5Min`, `TestBinlogDeadline80Percent` dan `TestBinlogDeadlineNeverWhenExpireZero`, `TestBacklogCollapsesAlerts` (200 laporan susulan bolak-balik → paling banyak satu buka dan satu tutup per kind), `TestMessageNeverContainsRawError` (LastSQLError berisi "Budi" → pesan tidak memuat "Budi"), `TestJobFailedAlert`.
- [ ] **Step 2–4** (`-race`). **Step 5: Commit** — `git commit -m "feat(panel): alert engine with open/close rules and hysteresis"`

---

### Task 7: Telegram: klien Bot API, pengirim, pengaturan tersegel

**Files:**
- Create: `internal/panel/notify/telegram.go`, `internal/panel/notify/notifier.go`,
  `internal/panel/notify/message.go`
- Modify: `internal/panel/panel.go` (Notifier dijalankan di `Run`)
- Test: `internal/panel/notify/telegram_test.go`, `notifier_test.go`, `message_test.go`

**Interfaces:**
- Consumes: `store.Alert`, `monitor.Settings`, `secrets.Box`.
- Produces:

```go
// package notify
type Telegram struct { BaseURL string /* "https://api.telegram.org" */; HTTP *http.Client /* timeout 10s, proxy dari env */ }
func (t *Telegram) Send(ctx context.Context, token, chatID, text string) error   // POST /bot<token>/sendMessage {chat_id, text, disable_web_page_preview:true}; error tanpa token di dalamnya
type Config struct { Token, ChatID string; Kinds map[string]bool; BaseURL string /* panel */ }
func LoadConfig(s *store.Store, box *secrets.Box) (Config, error)   // token dibuka dari SettingTelegramTokenEnc
func SaveToken(s *store.Store, box *secrets.Box, token string) error
type Deps struct { Store *store.Store; Box *secrets.Box; Sender interface{ Send(ctx, token, chatID, text string) error }; Logger *slog.Logger; Now func() time.Time; RetryEvery time.Duration /* 60s */; Repeat time.Duration /* 6h */; Snoozed func(serverID int64) bool; ServerName func(int64) string }
func New(d Deps) *Notifier
func (n *Notifier) Run(ctx context.Context)                // loop: tiap RetryEvery → Pass()
func (n *Notifier) Pass(ctx context.Context)               // alert terbuka: pending/failed → kirim; sent dan LastNotifiedAt+Repeat lewat → kirim ulang; alert yang baru ditutup (closed_at ada, belum ada pesan pulih) → kirim "Pulih"; snoozed → lewati (kirim saat snooze berakhir)
func (n *Notifier) Wake()                                  // dipanggil Monitor setelah Evaluate: kirim segera, tanpa menunggu tick
func (n *Notifier) SendTest(ctx context.Context) error     // "Tes dari dbpanel: notifikasi berfungsi."; rate limit 1/10 dtk
// message.go
func Format(a store.Alert, serverName, masterName, baseURL string, now time.Time) string   // bentuk spec 5.3; prefix ⛔ (crit), ⚠️ (warn), ✅ Pulih
func FormatRecovered(a store.Alert, serverName string, closedAt time.Time) string          // "✅ <server> pulih: <kind awam> selesai setelah <durasi>"
```

Status pulih dicatat lewat `telegram_state` `recovered_sent` setelah
pesan Pulih terkirim. Sandi/token tidak pernah masuk `slog`; error HTTP
di-log dengan URL yang token-nya diganti `***`.

- [ ] **Step 1: Tulis test** dengan `httptest.Server`: `TestSendPostsToBotAPI` (path `/bot<token>/sendMessage`, body chat_id dan text), `TestSendErrorHidesToken`, `TestFormatMatchesSpec` (teks persis contoh spec 5.3 untuk 1062), `TestPassSendsPendingThenMarksSent`, `TestSendFailureIsVisibleAndRetried` (server 500 → `telegram_state=failed`, `telegram_error` terisi, dikirim lagi pada Pass berikutnya), `TestRepeatEvery6Hours`, `TestRecoveredMessageOnce`, `TestSnoozedSkipsUntilEnd`, `TestKindsFilter`, `TestTokenNeverLogged` (handler slog ke buffer; cari token → tidak ada), `TestSendTestRateLimited`.
- [ ] **Step 2–4** (`-race`). **Step 5: Commit** — `git commit -m "feat(panel): Telegram notifier for alerts with retries and recovery messages"`

---

### Task 8: Ringkasan mingguan

**Files:**
- Create: `internal/panel/monitor/weekly.go`
- Modify: `internal/panel/notify/notifier.go` (`SendWeekly`), `internal/panel/monitor/monitor.go` (jadwal)
- Test: `internal/panel/monitor/weekly_test.go`

**Interfaces:**
- Produces:

```go
type WeeklyRow struct { ServerName, Role string; SyncedPct float64; Incidents int; IncidentTotal time.Duration; DiskPct int; FailedJobs int }
func Weekly(s *store.Store, views map[int64]View, from, to time.Time) ([]WeeklyRow, error)   // SyncedPct dari status_events: waktu dalam Sinkron/Ketinggalan dibagi waktu replika terdaftar dalam rentang
func FormatWeekly(rows []WeeklyRow, from, to time.Time) string
func (m *Monitor) scheduleWeekly(ctx context.Context)      // bila Settings.WeeklySummary: pada "Mon 07:00" waktu panel, sekali per minggu (settings key weekly_last_sent menjaga idempoten setelah restart)
```

- [ ] **Step 1: Tulis test**: `TestWeeklySyncedPctFromEvents` (replika Sinkron 6 hari, Berhenti 1 hari → 85.7%), `TestWeeklyCountsIncidents`, `TestWeeklyOnceEvenAfterRestart`, `TestFormatWeekly`.
- [ ] **Step 2–4.** **Step 5: Commit** — `git commit -m "feat(panel): weekly Telegram summary"`

---

### Task 9: Agent: pin peran dan sumber, `mysql.releaseRole`, CLI `set-role`/`set-source`

**Files:**
- Create: `internal/agent/drivers/mysql/pins.go`
- Modify: `internal/agent/drivers/mysql/driver.go` (field `PinDir string`, `pins *Pins`),
  `configure.go`, `replication.go`, `snapshot.go`, `clone.go`, `databases.go`, `users.go`
  (pemeriksaan pin di handler), `internal/agent/cli.go` (`set-role`, `set-source`, `RegisterMySQL` meneruskan dir),
  `internal/panel/localagent.go`, `internal/agent/monitor/monitor.go` (`RoleHint` → `Pins.Bootstrap`), `README.md`
- Test: `internal/agent/drivers/mysql/pins_test.go`, `internal/agent/cli_test.go`

**Interfaces:**
- Produces:

```go
const ( RoleFile = "role"; SourceFile = "source" )   // di PinDir (/etc/dbpanel), 0644 milik root
type Pins struct{}
func LoadPins(dir string) (*Pins, error)
func (p *Pins) Role() string                               // "master"|"replica"|"unassigned"
func (p *Pins) Source() (host string, port int, ok bool)
func (p *Pins) SetRole(role string) error                  // dipakai configure{role} saat Role()=="unassigned", dan CLI
func (p *Pins) SetSource(host string, port int) error      // dipakai configureReplication pertama (tanpa pin) dan CLI
func (p *Pins) Release() error                             // replica→unassigned + hapus source; ditolak bila Role()=="master"
func (p *Pins) Bootstrap(ctx context.Context, db *sql.DB, hint string) error   // hanya bila berkas role tidak ada: hint replica + SHOW REPLICA STATUS ada baris → replica; hint master + log_bin ON → master; selain itu unassigned
var ErrRolePinned = errors.New(...)                        // pesan: "server ini dipin sebagai <peran>; perintah <nama> ditolak. Untuk mengubah peran jalankan di server ini: sudo dbpanel agent set-role <peran>"
func (p *Pins) Allow(cmd string, params any) error         // tabel spec 5.5; mysql.setReadOnly{on:false} pada master tidak lagi memanggil refuseOnSource
// perintah baru: "mysql.releaseRole" (tanpa parameter) → Release() setelah memeriksa SHOW REPLICA STATUS kosong dan binlogDumpThreads==0
// CLI: dbpanel agent set-role {master|replica|unassigned} [--dir]; dbpanel agent set-source HOST:PORT [--dir]  — menulis berkas, mencetak "peran/sumber diperbarui; agent membacanya ulang dalam 15 detik" (Pins memuat ulang berkas tiap pemanggilan Allow: baca mtime)
```

`mysql.configureReplication` tanpa pin sumber menulis pin; dengan pin
yang berbeda → ParamError menyebut `set-source`. `dumpFrom` dan
`cloneFrom` memeriksa pin sumber yang sama. Pesan penolakan seluruhnya
Indonesia dan menyebut perintah CLI.

- [ ] **Step 1: Tulis test** (registry dengan Pins di dir sementara, testmysql untuk `releaseRole`/`Bootstrap`): `TestMasterRefusesDestructiveCommands` (tiap perintah tabel master → ErrRolePinned, tanpa menyentuh MySQL), `TestReplicaRefusesUnlockAndCreateReplUser`, `TestUnassignedKeepsRefuseOnSource`, `TestMasterUnlockAllowedDespiteDumpThreads`, `TestConfigureWritesRoleOnce` (configure{replica} lalu configure{master} → ditolak), `TestSourcePinWrittenThenEnforced` (configureReplication host A ok; host B → pesan berisi "set-source"; dumpFrom host B ditolak), `TestReleaseRoleRequiresNoReplication`, `TestReleaseRoleRefusedOnMaster`, `TestBootstrapFromHintAndState`, `TestPinsReloadAfterCLIWrite`, `TestCLISetRoleAndSetSource`.
- [ ] **Step 2–4.** README: bagian "Peran dan sumber yang dipin" dengan kedua perintah CLI. **Step 5: Commit** — `git commit -m "feat(agent): pinned role and source with local CLI override"`

---

### Task 10: Agent: preflight mode resync dan penyapuan akun dump

**Files:**
- Modify: `internal/agent/drivers/mysql/preflight.go` (`Resync`, `WipeList`, `ReclaimBytes`),
  `internal/agent/drivers/mysql/users.go` (`mysql.sweepDumpUsers`), `internal/proto/mysql.go`
- Test: `internal/agent/drivers/mysql/preflight_test.go`, `users_test.go`

**Interfaces:**
- Produces:

```go
// proto.PreflightParams tambahan:
//   Resync bool            — target adalah replika terdaftar master ini
//   WipeList []string      — database yang user setujui dibuang (resync)
//   ReclaimBytes uint64    — ukuran data replika saat ini, ditambahkan ke ruang bebas pada cek disk
// Perilaku resync: database target yang ada di WipeList bukan "leftover"; yang tidak ada → leftover seperti biasa. CLONE dipilih bila semua database dipilih dan setiap database target ada di WipeList (preflight.go:290 diperluas). Cek disk: free + ReclaimBytes >= 1.2 × PeerDataBytes.
// mysql.sweepDumpUsers {OlderThanSec int64} → {Dropped []string}: DROP USER untuk akun mysql.user dengan User LIKE 'dbpanel_dump_%' dan password_last_changed < NOW() - OlderThanSec; tidak pernah menyentuh dbpanel_repl*; log per akun.
```

- [ ] **Step 1: Tulis test** (testmysql): `TestPreflightResyncTreatsWipeListAsNotLeftover`, `TestPreflightResyncAllowsCloneOnNonEmptyTarget`, `TestPreflightResyncUnknownDatabaseStillLeftover`, `TestPreflightDiskCountsReclaim`, `TestSweepDumpUsersDropsOldOnly` (akun baru dibiarkan, `dbpanel_repl` dibiarkan).
- [ ] **Step 2–4.** **Step 5: Commit** — `git commit -m "feat(agent): resync-aware preflight and leftover dump account sweep"`

---

### Task 11: Orkestrator: kontrol langsung, job `resync_replica`, Lepas replika, aturan Hapus server

**Files:**
- Create: `internal/panel/orchestrator/controls.go`, `internal/panel/orchestrator/resync.go`
- Modify: `internal/panel/orchestrator/runner.go` (`KindResyncReplica`; `JobFailed`/`JobSucceeded` ke Monitor lewat `Deps.Alerts`; Cancel/gagal `create_replica` → `releaseRole`),
  `create_replica.go` (langkah "Siapkan replika" memanggil `mysql.sweepDumpUsers` dulu; `replicaCheckTarget` menerima mode resync),
  `change_databases.go` (sweep juga), `internal/panel/web/handlers_mysql.go` (interface `Orchestrator` diperluas)
- Test: `internal/panel/orchestrator/controls_test.go`, `resync_test.go`, `create_replica_test.go`

**Interfaces:**
- Consumes: Task 9 `mysql.releaseRole`, Task 10 preflight/sweep, Task 6 `JobFailed`.
- Produces:

```go
const KindResyncReplica = "resync_replica"
type Alerts interface{ JobFailed(serverID int64, jobID, kind, errText string); JobEnded(serverID int64, jobID string) }   // Deps.Alerts; nil = abaikan
func (r *Runner) StopReplica(ctx context.Context, serverID int64) error     // refuseBusy; mysql.stopReplica; SetReplicaPaused(true)
func (r *Runner) StartReplica(ctx context.Context, serverID int64) error    // mysql.startReplica; SetReplicaPaused(false)
func (r *Runner) Relock(ctx context.Context, serverID int64) error          // replika: mysql.setReadOnly{on:true}
func (r *Runner) UnlockMaster(ctx context.Context, serverID int64) error    // master: mysql.setReadOnly{on:false}
type ResyncParams struct { ServerID int64; ForceDump bool; WipeList []string /* dari modal */ }
func (r *Runner) StartResync(ctx context.Context, p ResyncParams) (string, error)   // replika ready/attention/failed; state→resyncing; tahap: Periksa master, Periksa server tujuan, Siapkan replika, Salin data, Sambungkan replikasi, Verifikasi
func (r *Runner) DetachReplica(ctx context.Context, serverID int64, stop bool) error   // stop=false: hapus baris replika, role unassigned; stop=true: stopReplica, resetReplica, releaseRole lalu sama; master offline tidak menghalangi
func (r *Runner) DeleteServer(ctx context.Context, serverID int64) error    // tolak: master topologi ("lepas atau hapus semua replikanya dulu") atau replika terdaftar ("lepas replika dulu"); else releaseRole best effort, Store.DeleteServer, Hub.Disconnect
```

`resync_replica` memakai `jobState` yang ada: `MasterServerID` dari
topologi, `Databases` dari baris replika, `ConfirmWipe=true`, `WipeList`
dari modal, dan `PreflightParams{Resync:true, WipeList, ReclaimBytes}`
(ReclaimBytes = jumlah byte database replika dari inventarisnya). Tahap
"Periksa server tujuan" berhenti menunggu persetujuan hanya bila
preflight melaporkan leftover di luar WipeList. Sukses → `ready`,
`paused=0`; gagal → `failed`. `create_replica` yang dibatalkan atau
gagal lalu replikanya dihapus (`runner.go:645`) memanggil
`mysql.releaseRole` best effort (log bila gagal).

- [ ] **Step 1: Tulis test** dengan agent palsu: `TestStopStartSetsPaused`, `TestRelockAndUnlockCallCorrectCommands`, `TestResyncHappyPathOneConfirmation` (tidak ada tahap waiting; `preflight` menerima `Resync:true` dan `WipeList`; sweepDumpUsers dipanggil sebelum createDumpUser), `TestResyncStopsForUnknownLeftover`, `TestResyncUsesCloneForAllDatabases`, `TestResyncRefusedWhileBusy`, `TestDetachKeepsReplicationRunning` (tidak ada stopReplica; baris hilang; role unassigned), `TestDetachAndStopCallsReleaseRole`, `TestDeleteServerRefusesMasterAndReplica`, `TestCancelCreateReplicaReleasesRole`, `TestJobFailedReachesAlerts`.
- [ ] **Step 2–4** (`-race`). **Step 5: Commit** — `git commit -m "feat(panel): replica controls, resync job, detach and delete rules"`

---

### Task 12: Web: catatan audit, tombol kontrol, Resync ulang, Lepas, Hapus, Jangan ganggu

**Files:**
- Create: `internal/panel/web/audit.go`, `internal/panel/web/handlers_controls.go`
- Modify: `internal/panel/web/router.go` (`Deps.Monitor MonitorView`: `interface{ View(int64) (monitor.View, bool); Views() map[int64]monitor.View; CheckNow(context.Context, int64) error; Snooze(int64, *time.Time) error }`), `handlers_servers.go` (`deleteServer` → `Orchestrator.DeleteServer`),
  `handlers_mysql.go`, `handlers_jobs.go`, `handlers_auth.go` (audit login/logout),
  `web/templates/replica_detail.html`, `master_detail.html`, `server_detail.html`, `web/static/app.js`
- Test: `internal/panel/web/web_controls_test.go`, `web_audit_test.go`

**Interfaces:**
- Consumes: Task 11 `Runner` API, Task 1 `AddAudit`, Task 6 `Snooze`.
- Produces:

```go
func (a *app) audit(r *http.Request, action, targetKind string, targetID int64, detail any, outcome string)   // username dari auth.UsernameFrom, IP dari RemoteAddr (X-Forwarded-For diabaikan), detail JSON tanpa field password/token
// route baru (semua authedPost kecuali disebut):
//   POST /replicas/{id}/stop, /start, /relock, /resync (form: confirm_name, force_dump, wipe_list[]), /detach (form: confirm_name, mode=keep|stop)
//   POST /masters/{id}/unlock (form: confirm_name)
//   POST /servers/{id}/check (Periksa sekarang), POST /servers/{id}/snooze (hours=1|2|8|24|0)
//   POST /servers/{id}/delete kini form confirm_name; menolak dengan pesan Orchestrator
// Modal Stop: "Lag akan membesar selama replika berhenti. Master menyimpan binlog N hari (dari BinlogExpireSec master); lebih dari itu replika harus disync ulang." Modal Kunci kembali: "Bila ada yang sempat menulis ke replika, isinya bisa berbeda dari master; pertimbangkan Resync ulang." Modal Resync: ukuran data master + saran di luar jam sibuk + daftar database yang dibuang.
// Modal konfirmasi: data-confirm-name (Alpine) mengaktifkan tombol hanya bila ketikan == nama server. Resync: modal memuat daftar database dari inventaris replika (disegarkan lewat RefreshInventory saat modal dibuka) ke wipe_list[].
// Setiap handler POST yang ada (connect, refresh, prepare-master, replicas, databases add/remove, jobs approve/retry/cancel, token, login, logout) memanggil a.audit.
```

Aksi audit (string tetap): `login`, `login_failed`, `logout`,
`mysql_connect`, `mysql_refresh`, `prepare_master`, `create_replica`,
`add_databases`, `remove_databases`, `job_approve`, `job_retry`,
`job_cancel`, `replica_stop`, `replica_start`, `replica_relock`,
`replica_resync`, `replica_detach`, `master_unlock`, `server_check`,
`server_snooze`, `server_delete`, `server_token`, `settings_update`,
`telegram_test`.

- [ ] **Step 1: Tulis test** (httptest dengan Orchestrator palsu): `TestStopStartButtonsCallOrchestrator`, `TestResyncRequiresTypedName` (nama salah → 400 dan tidak ada panggilan), `TestResyncPassesWipeListAndForceDump`, `TestDetachModes`, `TestDeleteRefusalMessageShown`, `TestSnoozeHours`, `TestEveryPostWritesAudit` (tabel route → aksi; detail tanpa "password"), `TestAuditLoginFailed`, `TestCSRFOnNewRoutes` (tanpa Origin → 403).
- [ ] **Step 2–4.** **Step 5: Commit** — `git commit -m "feat(web): replica controls with typed confirmations and audit log"`

---

### Task 13: Web: dashboard, halaman master dan replika dengan status, garis waktu, SSE

**Files:**
- Modify: `internal/panel/web/handlers_servers.go` (dashboard), `handlers_mysql.go` (master/replica pages),
  `router.go`, `render.go` (fungsi template `severityClass`, `fmtDuration`, `fmtPct`),
  `web/templates/dashboard.html`, `master_detail.html`, `replica_detail.html`, `servers.html`, `web/static/app.js`
- Create: `web/templates/_status.html` (fragmen status), `_timeline.html`; route `GET /servers/{id}/status-fragment`
- Test: `internal/panel/web/web_monitor_test.go`

**Interfaces:**
- Consumes: `monitor.View`, `monitor.Explain`, `store.ListStatusEvents`, `ListAudit`.
- Produces:

```go
// Deps.Monitor (Task 12) dipakai untuk View/Views.
// Dashboard: satu kartu per topologi: master (status, severity) + baris replika (nama, status, penanda kunci, "tertinggal N dtk" / "N transaksi", "dicek N dtk lalu"); kartu angka: master, replika, peringatan aktif; server tanpa MySQL terhubung: "Belum dipantau".
// Halaman master: status besar + Detail; versi, uptime, replika tersambung vs terdaftar (BinlogDumpThreads vs len(replicas)), umur binlog tertua dan batas simpan, koneksi/max, disk/RAM/load, tombol Buka kunci (hanya saat Terkunci), Periksa sekarang, Jangan ganggu; garis waktu 7 hari (?hari=90); kotak "Yang bisa dilakukan sekarang" saat Diduga tumbang (teks statis spec 5.8).
// Halaman replika: status besar + Explain (Text, Advice; tombol utama = Primary diberi btn-primary), penanda kunci + tombol Kunci kembali, tombol Task 12, lag, GTID diterima vs dieksekusi, "Detail teknis" (collapse: LastSQLError/LastIOError mentah, errno, source host/port, filter), garis waktu, riwayat job.
// app.js: event SSE health_update{server_id} → fetch /servers/{id}/status-fragment dan ganti elemen [data-status-for=id]; alert_update → perbarui angka peringatan di navbar.
// Daftar server: kolom status pemantauan, versi agent, badge "perlu dimutakhirkan" + perintah pembaruan (curl install ulang) bila NeedsUpgrade.
```

- [ ] **Step 1: Tulis test**: `TestDashboardShowsStatusPerReplica`, `TestReplicaPageShowsExplanationAndPrimaryButton` (1062 → teks awam, tombol Resync ulang `btn-primary`), `TestReplicaPageTechnicalDetailsHoldRawError` (error mentah ada di dalam `<details>`, tidak di judul), `TestMasterPageDownShowsManualGuidance`, `TestStatusFragmentEndpoint`, `TestServersListNeedsUpgradeBadge`, `TestTemplatesParse`.
- [ ] **Step 2–4.** Uji manual di browser (`make build`, panel lokal): dashboard diperbarui tanpa muat ulang saat `STOP REPLICA` di mysqld tes. **Step 5: Commit** — `git commit -m "feat(web): monitoring dashboard, master and replica status pages with live updates"`

---

### Task 14: Web: halaman Peringatan, Pengaturan, Audit

**Files:**
- Create: `internal/panel/web/handlers_alerts.go`, `handlers_settings.go`, `handlers_audit.go`,
  `web/templates/alerts.html`, `settings.html`, `audit.html`
- Modify: `router.go`, `render.go` (`pageNames`), `web/templates/layout.html` (menu: Peringatan dengan angka, Pengaturan, Audit)
- Test: `internal/panel/web/web_alerts_test.go`, `web_settings_test.go`, `web_audit_test.go`

**Interfaces:**
- Consumes: `store.ListOpenAlerts/ListAlerts/ListAudit`, `monitor.LoadSettings/SaveSettings`, `Monitor.Reconfigure`, `notify.SaveToken/LoadConfig`, `Notifier.SendTest`.
- Produces:

```go
// GET /alerts: aktif (kind awam, server, sejak, status Telegram + telegram_error, tombol Jangan ganggu server itu) dan riwayat 90 hari.
// GET/POST /settings: Telegram (token: kolom kosong = tidak diubah; tampil "tersimpan"; chat id; sakelar per kind; tombol POST /settings/telegram-test), ambang dan interval (validasi rentang: check 10–3600, dst.), ringkasan mingguan (on/off, hari+jam), alamat dasar panel (URL http/https). Simpan → SaveSettings, Monitor.Reconfigure, audit settings_update (detail tanpa token).
// GET /audit?server=&sejak=: tabel ts, pengguna, IP, aksi, target, hasil, detail ringkas.
```

- [ ] **Step 1: Tulis test**: `TestAlertsPageShowsOpenWithTelegramState`, `TestSettingsSaveSealsTokenAndNeverEchoes` (respons GET tidak memuat token), `TestSettingsValidatesRanges`, `TestSettingsSaveReconfiguresMonitor`, `TestTelegramTestButton` (Sender palsu dipanggil; rate limit 10 dtk → 429 kedua), `TestAuditPageFilters`, `TestSettingsUpdateAuditHasNoToken`.
- [ ] **Step 2–4.** **Step 5: Commit** — `git commit -m "feat(web): alerts, settings and audit pages"`

---

### Task 15: Hutang Rencana 2

**Files:**
- Modify: `internal/agent/drivers/mysql/realhost.go` (`Exec`: `SysProcAttr{Pdeathsig: syscall.SIGTERM}`),
  `internal/agent/drivers/mysql/install.go` (`Run`: sama), `internal/panel/orchestrator/runner.go`
  (cap 1 KiB di semua jalur `logf`/`AppendJobLog`; `Cancel` antara reopen dan launch → `canceled` (M2)),
  `internal/panel/orchestrator/prepare_master.go` (`masterVerify` memakai alamat sumber tersimpan; kalimat log master offline (M3)),
  `internal/panel/web/handlers_jobs.go` (`GET /jobs/{id}/log?after=<id>` JSON `[]JobLogLine`), `web/static/app.js` (pakai saat SSE putus-sambung)
- Test: `internal/agent/drivers/mysql/realhost_test.go`, `internal/panel/orchestrator/superseded_test.go`, `runner_test.go`, `prepare_master_test.go`, `internal/panel/web/web_mysql_jobs_test.go`

- [ ] **Step 1: Tulis test**: `TestExecSetsPdeathsig` (SysProcAttr diperiksa lewat hook pembuat cmd), `TestInstallRunSetsPdeathsig`, `TestJobLogCappedOnEveryPath` (logf, step message dari agent, error job > 1 KiB → terpotong dengan "…"), `TestCancelBetweenReopenAndLaunchIsCanceled` (M2; memakai gerbang channel seperti `TestRetryReopensBeforeNewJobs`), `TestMasterVerifyUsesStoredSource`, `TestOfflineMasterLogWording` (M3: kalimat menyebut "master" bukan "server tujuan"), `TestJobLogAfterEndpoint`.
- [ ] **Step 2–4** (`-race` pada orkestrator). **Step 5: Commit** — `git commit -m "fix: Plan 2 residuals — Pdeathsig, log caps, cancel race, verify source, log replay"`

---

### Task 16: Integrasi end-to-end, README, graph

**Files:**
- Modify: `internal/panel/e2e_replica_test.go` (lanjutan `TestE2ECreateReplica`, `TestE2EPinnedRole`),
  `README.md` (bagian Pemantauan, Telegram, Kontrol, Peran dan sumber yang dipin, Audit, Pembaruan agent),
  `scripts/install_test.sh` (unit agent tidak berubah; cek berkas pin tidak dibuat saat install)

- [ ] **Step 1: Tulis test E2E** (panel nyata, dua agent, dua mysqld, Telegram `httptest`): setelah replika `ready`: (a) `STOP REPLICA` langsung di mysqld replika → dalam 30 dtk View replika "Berhenti (di luar panel)", alert `replica_stopped` terbuka, Telegram palsu menerima satu pesan yang memuat nama server dan tidak memuat teks error mentah; (b) `POST /replicas/{id}/start` → "Sinkron" dalam 30 dtk, pesan "pulih" diterima sekali; (c) `POST /replicas/{id}/stop` → "Dihentikan", tidak ada alert baru; (d) `SET GLOBAL super_read_only=OFF` di replika → `LockLost`, `replica_unlocked`; `POST /replicas/{id}/relock` → tertutup; (e) `POST /replicas/{id}/resync` dengan nama server dan `wipe_list` → job `resync_replica` sukses tanpa tahap waiting, replika `ready`; (f) `TestE2EPinnedRole`: `mysql.dropDatabases` ke agent master lewat hub → error berisi "dipin sebagai master"; `POST /servers/{master}/delete` → 409 dengan pesan "lepas atau hapus semua replikanya dulu".
- [ ] **Step 2: Jalankan `make test` dan `make test-e2e`**; semua hijau tanpa `(cached)` untuk paket yang berubah.
- [ ] **Step 3: README dan `graphify update .`** (graphify-out gitignored; jalankan saja).
- [ ] **Step 4: Commit** — `git commit -m "test: end-to-end monitoring, controls and pinned role; docs"`

---

## Catatan untuk pelaksana

- Jam palsu: semua paket panel baru menerima `Now func() time.Time` lewat
  Deps dan tidak memanggil `time.Now` langsung; loop memakai ticker yang
  bisa diganti (`TickEvery`).
- `hub.Call` untuk `monitor.configure` dijalankan di goroutine dari
  `OnOnline` (yang berjalan di bawah kunci hub); jangan memanggil hub
  secara sinkron dari listener.
- Agent tertanam di panel (`localagent.go`) harus mendapat Monitor dan
  Pins yang sama dengan agent jarak jauh; PinDir = `ConfigDir` panel.
- Tidak ada perubahan unit systemd; berkas pin ditulis agent sendiri.
