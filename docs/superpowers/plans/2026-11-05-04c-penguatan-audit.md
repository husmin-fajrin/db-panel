# Rencana Implementasi 4c: Penguatan Audit

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Status pin dilaporkan dan "Master tidak dipin" diperingatkan;
laporan agent dibatasi lajunya dan ukurannya; peringatan dan garis
waktu dipangkas; catatan audit berantai hash dan bisa diverifikasi;
kutipan data dari pesan MySQL disensor sebelum disimpan; tiga minor
terakhir Rencana 3 dibayar.

**Architecture:** `HealthSnapshot` bertambah tiga field pin; mesin
peringatan bertambah satu aturan. Hub memakai ember token per server
sebelum `OnEvent` dan `readLimit` 1 MiB. Pemangkasan harian di panel.
`audit_log` memperoleh `prev_hash`/`hash` yang dihitung di `AddAudit`
dalam transaksi; migrasi 0009 melepas dan memasang kembali trigger
append-only untuk mengisi rantai lama; CLI `panel audit-verify`.
Sensor kutipan adalah fungsi murni yang dipanggil di satu tempat:
`Monitor.ingest` dan `jobRun.logf`.

**Tech Stack:** Go 1.26.10, SQLite, `crypto/sha256`, `log/slog`
(journald lewat stdout layanan).

**Spec:** `docs/superpowers/specs/2026-11-05-rencana-4-promote-design.md`
bagian 5.12, 5.13 (audit_log, settings), 5.14 (`master_unpinned`), 9.
Audit: SEC-34, SEC-45, SEC-46, SEC-48, SEC-50. Ledger Rencana 3 baris
"Release fixes" (tiga minor).

## Global Constraints

- Nilai (5.12): ember laporan per server 10, isi ulang 1 per 2 detik;
  pesan agent maks 1 MiB (`readLimit`); peringatan tertutup > 90 hari
  dan `status_events` > 180 hari dipangkas harian; `audit_log` dan
  `job_log` tidak pernah dipangkas; `master_unpinned` setelah > 2
  laporan berturut dengan `role_pin != "master"`.
- Hash: `hash = hex(sha256(prev_hash || "\n" || ts || "\n" || username || "\n" || ip || "\n" || action || "\n" || target_kind || "\n" || target_id || "\n" || detail_json || "\n" || outcome))`; baris pertama `prev_hash = ""`.
- Sensor kutipan: literal `'…'` dan `"…"` (dengan escape `\'`, `''`)
  diganti `'…'`; token bilangan panjang (≥ 8 digit) dipertahankan;
  diterapkan pada `mysql_error`, `last_io_error`, `last_sql_error`,
  pesan job dari perintah MySQL, dan teks Telegram yang memuatnya.
- Semua teks pengguna Indonesia; "Detail teknis" boleh memuat angka
  laporan dibuang.

## Review Focus

1. Agent yang membanjiri laporan (1000/detik) tidak boleh memutus
   sambungannya atau menghentikan laporan server lain; penghitung
   dibuang tampil di Detail teknis. Test di Task 2.
2. Baris audit yang diubah langsung di SQLite harus terdeteksi
   `audit-verify` dengan nomor baris pertama yang putus; migrasi pada
   basis data dengan 0 baris dan dengan 10.000 baris harus berakhir
   dengan rantai utuh. Test di Task 3.
3. Pesan error MySQL yang memuat kutipan dengan escape (`'O\'Brien'`,
   `'a''b'`) harus tersensor seluruhnya tanpa menyisakan potongan.
   Test di Task 4.
4. Agent yang tersambung lagi lalu `monitor.configure` gagal tidak
   boleh menampilkan "Agent offline" lebih dari satu interval;
   "Menunggu laporan pertama" tampil sampai laporan segar. Test di
   Task 1.
5. Pemangkasan harian tidak boleh menghapus peringatan yang masih
   terbuka atau yang ditutup < 90 hari, dan tidak boleh menyentuh
   `audit_log`. Test di Task 2.

---

### Task 1: Agent dan monitor: status pin di laporan, peringatan "Master tidak dipin", tiga minor Rencana 3

**Files:**
- Modify: `internal/proto/health.go` (`HealthSnapshot.RolePin`, `SourcePin`, `PinError string`), `internal/agent/drivers/mysql/health.go`
  (mengisi dari `Pins`), `internal/panel/monitor/alerts.go` (`KindMasterUnpinned = "master_unpinned"`, Peringatan, saran
  "jalankan ulang Siapkan master"), `state.go` (status `Agent offline` → "Menunggu laporan pertama" pada OnOnline sampai laporan segar),
  `internal/panel/web/templates/server_detail.html` ("Peran dipin: …" di Detail teknis), `internal/agent/drivers/mysql/users.go`
  (petunjuk `DROP USER` menyebut host baris yang ada; `createReplUser` diserialkan dengan `sync.Mutex` per Driver),
  `internal/panel/notify/message.go`, `README.md`
- Test: `health_test.go`, `alerts_test.go`, `state_test.go`, `users_test.go`

- [ ] **Step 1: Tulis test** `TestSnapshotReportsPins`, `TestMasterUnpinnedAlertAfterThreeReports` (2 laporan → tidak; 3 → buka; laporan dengan pin master → tutup), `TestReconnectShowsWaitingFirstReport`, `TestDropUserHintNamesActualHost`, `TestCreateReplUserSerialized` (dua goroutine → satu `CREATE USER`, tanpa `ALTER`).
- [ ] **Step 2–4.** Implementasi; `go test ./internal/agent/... ./internal/panel/monitor/ -race`.
- [ ] **Step 5: Commit** — `git commit -m "feat(monitor): pin status in reports, master_unpinned alert, Plan 3 minors"`

---

### Task 2: Hub dan panel: batas laju laporan, batas pesan 1 MiB, retensi harian dan pengaturan

**Files:**
- Modify: `internal/panel/hub/conn_ws.go` (`readLimit = 1 << 20`), `internal/agent/session.go` (sama), `internal/panel/hub/hub.go`
  (`OnEvent`: ember per server untuk `monitor.snapshot`; `Dropped(serverID) int`), `internal/panel/monitor/monitor.go`
  (`View.ReportsDropped`), `internal/panel/panel.go` (goroutine harian: `PruneAlertsClosedBefore`, `PruneStatusEvents`,
  `PruneConfirmations`), `internal/panel/store/alerts.go` (`PruneAlertsClosedBefore(t) (int64, error)`), `settings.go`
  (`alerts_retention_days` 90, `events_retention_days` 180), `internal/panel/web/handlers_settings.go` + `settings.html`
  (bagian "Retensi"), `views_monitor.go` (Detail teknis: laporan dibuang), `README.md`
- Test: `hub_test.go`, `panel_test.go`, `alerts_test.go` (store), `web_settings_test.go`

- [ ] **Step 1: Tulis test** `TestHubRateLimitsSnapshotsPerServer` (jam palsu: 10 lolos, ke-11 dibuang, 2 detik kemudian 1 lolos; server lain tidak terpengaruh; koneksi tetap), `TestReadLimitOneMiB` (pesan 1 MiB+1 ditolak), `TestDailyPruneKeepsOpenAndRecent`, `TestPruneNeverTouchesAudit`, `TestRetentionSettingsValidation` (1–3650 hari).
- [ ] **Step 2–4.** Implementasi; `go test ./internal/panel/... -race`.
- [ ] **Step 5: Commit** — `git commit -m "feat(panel): report rate limit, 1 MiB message cap, daily retention"`

---

### Task 3: Audit: hash chain, migrasi 0009, `panel audit-verify`, status di `/audit`, salinan journald

**Files:**
- Create: `internal/panel/store/migrations/0009_audit_chain.sql` (kolom), `internal/panel/store/audit_chain.go`
- Modify: `internal/panel/store/migrate.go` (migrasi 0009 memakai hook Go: `DROP TRIGGER` ×2, isi rantai berurutan `id`, `CREATE TRIGGER` ×2,
  dalam satu transaksi), `audit.go` (`AddAudit` menghitung hash dalam transaksi dengan `SELECT hash FROM audit_log ORDER BY id DESC LIMIT 1`),
  `internal/panel/cli.go` (`audit-verify [--data-dir]` → keluar 0 "Rantai utuh sampai #N" atau 1 "Rantai putus di #M"),
  `internal/panel/web/handlers_audit.go` + `audit.html` (baris status rantai, dihitung saat halaman dibuka, dibatasi 1×/menit cache),
  `internal/panel/web/audit.go` (log `slog.Info("audit", …)` tiap sisipan), `README.md`
- Test: `audit_chain_test.go`, `migrate_test.go`, `cli_test.go`, `web_audit_test.go`

**Interfaces:**

```go
func AuditHash(prev string, e AuditEntry) string
func (s *Store) VerifyAuditChain() (lastOK int64, brokenAt int64, err error)   // brokenAt 0 bila utuh
```

- [ ] **Step 1: Tulis test** `TestAuditHashDeterministic`, `TestAddAuditChains`, `TestVerifyDetectsTamper` (ubah lewat `sql.DB` dengan trigger dilepas sementara di test → `brokenAt` baris itu), `TestMigrate0009BackfillsEmptyAndLarge` (0 dan 10.000 baris; trigger kembali ada: `UPDATE` ditolak), `TestAuditVerifyCLI`, `TestAuditPageShowsChainStatus`, `TestAuditInsertLogged`.
- [ ] **Step 2–4.** Implementasi; `go test ./internal/panel/... -race`.
- [ ] **Step 5: Commit** — `git commit -m "feat(audit): hash chain with verification and journald copy (migration 0009)"`

---

### Task 4: Sensor kutipan data saat ingest

**Files:**
- Create: `internal/panel/redact/redact.go`
- Modify: `internal/panel/monitor/state.go` (`ingest`: sensor pada `MySQLError`, `Replica.LastIOError`, `LastSQLError` sebelum simpan/derive),
  `internal/panel/orchestrator/runner.go` (`jobRun.logf` dan pesan langkah dari error agent), `internal/panel/monitor/alerts.go`
  (pesan peringatan yang memuat error), `README.md`
- Test: `redact_test.go`, `state_test.go`, `runner_test.go`

**Interfaces:**

```go
func Quotes(s string) string   // "Duplicate entry 'Budi' for key" → "Duplicate entry '…' for key"; "'O\\'Brien'" → "'…'"; "'a''b'" → "'…'"; "12345678" tetap
```

- [ ] **Step 1: Tulis test** `TestRedactQuotes` (tabel kasus termasuk escape, kutip ganda, kutipan tak tertutup → sisa baris disensor, string tanpa kutipan tidak berubah), `TestIngestRedactsErrors` (snapshot dengan error berisi `'Budi'` → `server_health.snapshot_json` dan `status_events.detail` tanpa `Budi`), `TestJobLogRedacts`.
- [ ] **Step 2–4.** Implementasi; `go test ./internal/panel/... -race`.
- [ ] **Step 5: Commit** — `git commit -m "feat(panel): redact quoted data from MySQL messages at ingest"`

---

### Task 5: Integrasi, README, graph

- [ ] **Step 1:** `make test` lulus; `make test-e2e` lulus (e2e Rencana 3 dan 4b harus tetap lulus dengan batas laju dan sensor).
- [ ] **Step 2:** README: bagian "Catatan audit" (rantai hash, `audit-verify`), "Pemantauan" (laporan dibuang, retensi), "Keamanan" (sensor kutipan, pin dilaporkan).
- [ ] **Step 3:** `graphify update .`; commit — `git commit -m "docs: Plan 4c audit hardening"`

## Catatan untuk pelaksana

- Urutan bebas kecuali Task 5 terakhir; Task 3 mengubah `AddAudit`,
  jadi test web yang memeriksa audit tetap harus lulus.
