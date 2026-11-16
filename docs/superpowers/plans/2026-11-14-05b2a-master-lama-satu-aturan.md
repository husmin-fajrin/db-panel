# Rencana Implementasi 5b-2a: Master Lama dengan Satu Aturan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Master lama yang dikarantina punya satu arti di panel dan di
agent pada setiap keadaan; penjaga menguncinya lagi dalam satu putaran
bila terbuka tanpa sebab yang sah; karantina hanya berakhir bila setiap
database memang disalin dari master baru; dan pekerjaan berbahaya di
server dilakukan tiga perintah `dbpanel agent`, bukan SQL yang disalin
operator.

**Architecture:** Di agent, setiap pembukaan baca-saja yang sah (pekerjaan
agent atau perintah CLI) memegang satu `flock`; penjaga membaca bendera
tiap putaran dan mengunci lagi hanya bila kunci berkas itu bebas, lalu
mencatatnya sebagai hasil start `late` dengan keterangan "selagi
berjalan". Muat dump hanya mematikan `super_read_only`, dan catatan muat
menyimpan daftar database master baru yang dibaca klien `mysql`. Tiga
subperintah CLI baru masuk ke MySQL sebagai root. Di panel, bangun ulang
master lama yang gagal mengembalikan peran ke `quarantined` dengan baris
gagal dipertahankan; monitor membaca peran sebelum baris; tindakan
"Batalkan percobaan bangun ulang" dihapus; teks agent di pekerjaan selalu
berlabel.

**Tech Stack:** Go 1.26, `CGO_ENABLED=0`, tanpa dependensi baru; SQLite
(modernc); html/template + Tailwind v4/daisyUI v5 (`bash
scripts/tailwind.sh`); harness `internal/agent/drivers/mysql/testmysql`
(mysqld sungguhan, skip bila tidak terpasang); e2e
`internal/panel/e2e_*_test.go`.

**Spec:** `docs/superpowers/specs/2026-11-14-rencana-5b-2a-master-lama-satu-aturan-design.md`
— seluruhnya; bagian 9 mengikat. Kode diperiksa pada `main` `8ce7fc5`.
Fakta kode yang dipakai rencana ini (tanda tangan fungsi, nomor baris)
berasal dari pembacaan pada commit itu; pelaksana memeriksa ulang letaknya
sebelum mengubah.

## Global Constraints

- Go 1.26, `CGO_ENABLED=0`, tanpa dependensi baru (termasuk tidak menambah
  `golang.org/x/term` atau `golang.org/x/sys` sebagai dependensi langsung).
- Bahasa: teks untuk operator (UI, keluaran CLI, pesan galat yang tampil
  ke operator, README) berbahasa Indonesia; kode, komentar, nama test dan
  pesan commit berbahasa Inggris. Log agent (slog) berbahasa Inggris.
- Aturan kata (spek 5b 5.11): pakai nama yang sudah dipakai panel
  ("baca-saja", "karantina", "kunci start", "Bangun ulang sebagai
  replika", "Periksa data tertinggal", "Amankan data tertinggal", "Ulangi
  dari tahap ini"); istilah Inggris dalam kurung pada penyebutan pertama
  di satu layar. Teks lama tidak diubah kecuali yang disebut task. Teks
  baru dipakai persis seperti di task dan di "Teks baru untuk dibaca
  pemilik".
- Tidak ada perintah protokol baru dan tidak ada jenis peringatan baru.
  Field protokol baru semuanya `omitempty`.
- Penyelamatan (Periksa dan Amankan data tertinggal) tidak pernah ditolak
  oleh aturan baru kecuali keadaan salinan `CopyStarted` yang sudah ada.
- Nilai tetap: putaran penjaga 5 detik (`GuardEvery`, tidak diubah);
  menunggu kunci berkas paling lama **10 detik**; laporan pin untuk
  pemulihan: **3** berturut-turut; ekor log galat **64 KiB**, batas waktu
  **5 detik**; folder cadangan bawaan `/var/lib/dbpanel/dibuang`, mode
  `0700`, berkas `0600`; berkas kunci `/var/lib/dbpanel/lift.lock`;
  berkas daftar "tidak disalin" `/var/lib/dbpanel/not-copied`.
- Satu penulis dan satu proses test pada satu waktu (test panel mengimpor
  `internal/agent`). Gerbang tiap task, satu per satu, semua exit 0:
  task yang menyentuh `internal/agent` atau `internal/proto`:
  `go test ./internal/agent/... ./internal/proto/... -race -short -timeout 60m`,
  lalu `go test ./internal/panel/... -race -short -timeout 60m`, lalu
  `make test`; task panel saja: dua yang terakhir. Task 15 menambah
  `make test-e2e`.
- Eksplorasi kode: `graphify query "<pertanyaan>"` sebelum grep atau
  membaca berkas sumber; `graphify update .` sesudah mengubah kode.
- Setiap perintah yang ditulis ke README dijalankan dulu oleh penulisnya
  (mysqld coba di direktori sementara, port/socket sendiri), dan setiap
  teks yang dikutip README disalin dari kode.

## Review Focus

1. **Nama database yang tidak biasa** (spasi, backtick, tanda kutip, huruf
   besar pada server `lower_case_table_names=1`, non-ASCII) di
   `drop-database`, di daftar master baru, dan di berkas `not-copied`:
   nama dibandingkan tanpa beda huruf besar-kecil dan tidak pernah lewat
   shell. Test: Task 6 (`TestSourceDatabasesOddNames`), Task 8
   (`TestDropDatabaseOddNames`).
2. **MySQL yang tidak dipasang dbpanel** (root berkata sandi, socket di
   tempat lain, akun layanan bukan `mysql`): perintah CLI meminta kata
   sandi; uid dicari dari `--user`. Test: Task 8
   (`TestRootSessionPasswordFallback`), Task 7
   (`TestServiceCredentialOtherUser`).
3. **Proses mati di tengah:** agent mati saat memegang kunci berkas; CLI
   dimatikan di antara membuka dan mengunci; panel berhenti di antara
   "baris gagal" dan "peran dikarantina". Test: Task 3
   (`TestLiftLockReleasedWhenHolderDies`), Task 8
   (`TestDropDatabaseKilledMidway`), Task 11
   (`TestResumeNormalizesHalfFailedRebuild`).
4. **Data dari sebelum rencana ini:** baris peran `replica` + gagal + tanda
   master lama; job tanpa bendera; `leftover-gtid` tanpa cap sumber; agent
   lama dengan panel baru dan sebaliknya. Test: Task 11
   (`TestNormalizeLegacyFailedRebuilds`), Task 10
   (`TestLeftoverSetWithoutSourceIsNoRecord`), Task 5
   (`TestRelockReportReadableByOldPanelShape`).
5. **Hal yang lama atau besar:** `DROP DATABASE` yang berjalan bermenit
   sementara penjaga berputar; cadangan lebih besar dari ruang kosong;
   bangun ulang berjam-jam sementara operator menjalankan perintah CLI.
   Test: Task 4 (`TestGuardLeavesLongLiftAlone`), Task 8
   (`TestDropDatabaseNoSpace`, `TestCLIRefusesWhileLoadHoldsLock`).

---

## File Structure

| Berkas | Tanggung jawab |
|---|---|
| `internal/agent/drivers/mysql/lift.go` (baru) | `liftGate`: kunci berkas, hitungan pembukaan, pasang/lepas bendera; `AcquireLiftLock` untuk CLI |
| `internal/agent/drivers/mysql/dump.go` | `liftReadOnly` lewat `liftGate`; `sourceDatabases`; dua pembacaan daftar di `dumpFrom` |
| `internal/agent/drivers/mysql/clone.go`, `configure.go`, `databases.go`, `users.go` | pemakai `liftReadOnly` yang baru |
| `internal/agent/drivers/mysql/quarantine.go` | penjaga membaca bendera tiap putaran; `writableSinceLoad`; penolakan settle |
| `internal/agent/drivers/mysql/startlock_guard.go`, `start_result.go` | laporan "dikunci lagi"; ekor log lewat uid layanan |
| `internal/agent/drivers/mysql/rebuild_load.go` | `loadCopy.source`; aturan `notCopied`; berkas `not-copied`; sesi bukti; teks baru |
| `internal/agent/drivers/mysql/svcuid.go` (baru) | `serviceCredential`: pencarian sempit uid akun layanan |
| `internal/agent/drivers/mysql/leftover.go`, `salvage.go`, `binlog.go` | `mysqlbinlog` dan indeks binlog lewat uid layanan; cap sumber `leftover-gtid` |
| `internal/agent/drivers/mysql/replication.go` | sumber kanal dibandingkan dengan pin sebelum start |
| `internal/agent/cli.go` | pendaftaran tiga subperintah, teks bantuan |
| `internal/agent/cli_rootsql.go` (baru) | sesi root MySQL (socket, lalu kata sandi tanpa gema) |
| `internal/agent/cli_dropdb.go` (baru) | `drop-database` |
| `internal/agent/cli_admin.go` (baru) | `grant-admin`, `reset-admin` |
| `internal/proto/mysql.go` | `QuarantineStartEvent.While`, `StartKindState.While` |
| `internal/panel/orchestrator/runner.go`, `create_replica.go`, `approval.go`, `controls.go`, `resync.go` | aturan satu keadaan: `fail`, `Resume`, `Retry`, `Approve`, `supersededBy`, penolakan, hapus, penormalan |
| `internal/panel/orchestrator/promote.go`, `promote_view.go` | promote kedua |
| `internal/panel/orchestrator/agenttext.go` (baru) | `agentTextError`, `fromAgent`, `AgentQuoteN` |
| `internal/panel/store/topologies.go`, `jobs.go` | hapus `ReturnToQuarantine`; `NewerJobExistsExcept` |
| `internal/panel/monitor/state.go`, `start_state.go`, `promote.go`, `alerts.go` | peran dibaca lebih dulu; pesan dan penutupan `master_start_late`; hitungan pin |
| `internal/panel/web/handlers_oldmaster.go`, `handlers_mysql.go`, `handlers_servers.go`, `handlers_replication.go`, `handlers_controls.go`, `router.go`, `handlers_confirm.go`, `audit_summary.go`, `render.go` | kotak, form, daftar, label galat; penghapusan Batalkan |
| `internal/panel/redact/redact.go` | frasa dan jangkar mengikuti teks baru |
| `web/templates/server_detail.html`, `replica_detail.html`, `_ui.html`, `promote.html` | tampilan |
| `README.md` | dua bagian ditulis ulang menjadi perintah; butir kecil |

---

### Task 1: Tinjauan rancangan sebelum kode

**Files:**
- Create: `docs/superpowers/ledgers/2026-11-14-05b2a-design-review.md`
- Modify: rencana ini, bagian "Perubahan sesudah tinjauan rancangan"

Task ini tidak mengubah kode. Dikerjakan oleh model terkuat yang tersedia
(bila tingkat teratas belum tersedia, dicatat dan dipakai tingkat
berikutnya; tinjauan diulang pada tingkat teratas sebelum Task 11).

- [ ] **Step 1:** Baca spek, rencana ini, dan kode yang disebut bagian 3
  spek. Cari: keputusan di bagian 5 yang tidak bisa bekerja seperti
  tertulis; keadaan di tabel 5.1 yang tidak punya jalan maju atau jalan
  keluar; urutan kejadian yang menghasilkan salah satu anti-tujuan
  (bagian 2); task yang tanda tangannya tidak cocok dengan task lain.
- [ ] **Step 2:** Tulis laporan (temuan, tingkat, usul) ke berkas di atas.
- [ ] **Step 3:** Untuk setiap temuan yang diterima pengendali, ubah task
  yang terkena dan catat di "Perubahan sesudah tinjauan rancangan": butir
  mana, apa yang berubah, mengapa. Temuan yang mengubah bagian 5 atau 7
  spek dibawa ke pemilik sebelum task yang terkena dikerjakan.
- [ ] **Step 4:** Commit `docs: design review for plan 5b-2a`.

### Task 2: Pengukuran M1–M4

**Files:**
- Modify: rencana ini, bagian "Bukti pengukuran"

Semua pada mysqld coba (lihat `testmysql`), tidak pernah MySQL mesin ini.

- [ ] **Step 1 (M1):** Server `read_only=ON`, `super_read_only=OFF`. Akun
  A = hak wajib `mysql.connect` (termasuk `CONNECTION_ADMIN`, tanpa
  SUPER); akun B = aplikasi biasa tanpa `CONNECTION_ADMIN`. Catat untuk A:
  muat hasil `mysqldump --all-databases` dengan
  `--init-command=SET sql_log_bin=0`; `DROP DATABASE`; `CREATE USER` +
  `GRANT`; `INSTALL PLUGIN clone`; `CLONE INSTANCE` (bila bisa disusun di
  harness). Catat untuk B: `INSERT` dan `CREATE DATABASE` (harus ditolak).
- [ ] **Step 2 (M2):** Dengan akun buatan `mysql.createDumpUser`:
  `mysql -N -B -e "SELECT HEX(schema_name) FROM information_schema.schemata"`
  terhadap server yang punya database bernama biasa, berspasi,
  ber-backtick, non-ASCII, dan satu database yang akun itu tidak diberi
  hak khusus. Catat apakah semuanya terlihat dan bentuk keluarannya.
- [ ] **Step 3 (M3):** Jalankan `mysqlbinlog --no-defaults <berkas>` dan
  `cat <indeks>` sebagai uid pemilik direktori data coba. Catat hasilnya,
  dan baca profil AppArmor/SELinux paket MySQL Debian dan RHEL untuk
  `mysqlbinlog` (ada atau tidak; catat sumbernya).
- [ ] **Step 4 (M4):** `SHOW GRANTS FOR` akun dengan
  `sql_quote_show_create=OFF`, dan dengan `sql_mode=ANSI_QUOTES`. Catat
  barisnya.
- [ ] **Step 5:** Tulis tabel hasil ke "Bukti pengukuran" dan, di bawahnya,
  keputusan yang mengikutinya: nilai `both` untuk setiap pemakai
  `liftReadOnly` (Task 3); apakah Task 7 dibangun untuk `mysqlbinlog`;
  pernyataan `SET SESSION` untuk Task 10. Commit
  `docs(plan): measurements M1-M4 for plan 5b-2a`.

Aturan keputusan: `both = false` untuk pemakai yang berhasil pada M1
dengan `read_only` menyala; `both = true` hanya untuk yang gagal. Bila
muat dump gagal dengan `read_only` menyala, berhenti dan laporkan ke
pengendali (butir 5.3 pertama batal; yang lain jalan).

### Task 3: Kunci berkas dan hitungan pembukaan

**Files:**
- Create: `internal/agent/drivers/mysql/lift.go`, `lift_test.go`
- Modify: `dump.go` (`liftReadOnly`, `dumpFrom`), `clone.go`,
  `configure.go` (`installClonePlugin`), `databases.go`
  (`DropDatabases`), `users.go` (`createAccount`), `driver.go`

**Interfaces:**
- Produces:
  - `const DefaultLiftLock = "/var/lib/dbpanel/lift.lock"`; field
    `Driver.LiftLock string` (kosong = bawaan).
  - `type liftGate struct` dengan
    `func newLiftGate(path string) *liftGate`,
    `func (g *liftGate) begin(ctx context.Context, db *sql.DB, both bool, logf func(level, format string, args ...any)) (end func() error, err error)`,
    `func (g *liftGate) tryGuard() (release func(), free bool, both bool)`
    (`free`: kunci berkas didapat dan tidak ada pembukaan di proses ini;
    `both`: pembukaan di proses ini mematikan keduanya).
  - `func AcquireLiftLock(path string) (release func(), err error)` —
    untuk CLI; `LOCK_EX|LOCK_NB`; gagal dengan `ErrLiftBusy`.
  - `var ErrLiftBusy = errors.New(...)` dengan teks operator
    `kunci baca-saja server ini sedang dibuka pekerjaan lain (misalnya bangun ulang yang sedang menyalin data); coba lagi sesudah pekerjaan itu selesai`.
  - `liftReadOnly(ctx, logf, db, mark, g *liftGate, both bool) (restore func() error, err error)`
    (tanda tangan baru; `g` tidak boleh nil di kode produksi).
- Consumes: hasil M1 (nilai `both` per pemakai).

- [ ] **Step 1: Test dulu** (`lift_test.go`, RED):
  - `TestLiftCountsHolders`: dua `begin` tumpang-tindih pada mysqld
    sungguhan `1 1`; sesudah `end` pertama `super_read_only` masih 0;
    sesudah `end` kedua `1 1`.
  - `TestLiftKeepsReadOnlyOn`: `begin(both=false)` → `read_only=1`,
    `super_read_only=0`; `begin(both=true)` → `0 0`; restore hanya
    memasang yang semula menyala.
  - `TestLiftWaitsForForeignLock`: deskriptor lain memegang `flock`;
    `begin` gagal dengan `ErrLiftBusy` sesudah paling lama 10 detik (test
    memakai jam palsu atau batas yang disuntik) dan bendera tidak
    berubah.
  - `TestLiftLockReleasedWhenHolderDies`: proses anak memegang kunci lalu
    dimatikan; `tryGuard` berikutnya `free == true`.
  - `TestTryGuardSeesLocalLift`: selama `begin(both=false)` aktif,
    `tryGuard` → `free=false, both=false`; selama `both=true` →
    `both=true`.
  - `TestAcquireLiftLockBusy`: `AcquireLiftLock` saat `begin` aktif →
    `ErrLiftBusy`.
- [ ] **Step 2:** Jalankan, pastikan gagal (belum terdefinisi).
- [ ] **Step 3:** Tulis `lift.go`. Pembuka pertama mengambil `flock`
  (deskriptor milik gate), membaca nilai semula, mematikan
  `super_read_only` (dan `read_only` bila `both`); pembuka berikutnya
  hanya menaikkan hitungan (bila ia meminta `both` sedangkan yang berjalan
  tidak, ia juga mematikan `read_only` dan gate mencatat `both`). Pembuka
  terakhir memasang lagi yang semula menyala, urutan `read_only` lalu
  `super_read_only`, dengan batas `restoreTimeout` yang ada, lalu melepas
  `flock`. `tryGuard` memakai deskriptor sendiri.
- [ ] **Step 4:** Ubah `liftReadOnly` menjadi pembungkus `g.begin`; tanda
  `mark.set()` dipanggil sebelum `begin` mematikan bendera dan
  `mark.clear()` sesudah `end` (menutup jendela yang dicatat di fakta F9).
  Teruskan `*liftGate` dan nilai `both` (dari Task 2) ke kelima pemakai;
  `Driver` membuat satu gate di `Register`.
- [ ] **Step 5:** Jalankan test paket; test lama yang memanggil
  `liftReadOnly` langsung disesuaikan tanpa mengubah yang ditegaskannya.
  Tambah `TestDumpKeepsReadOnlyDuringLoad` (mysqld sungguhan: selama muat
  berjalan `read_only=1`; akun tanpa `CONNECTION_ADMIN` ditolak `INSERT`).
- [ ] **Step 6:** Gerbang; commit
  `feat(agent): one lock and a holder count for every read-only lift`.

### Task 4: Penjaga membaca bendera di setiap putaran

**Files:**
- Modify: `internal/agent/drivers/mysql/quarantine.go`
  (`guardQuarantined`), `rebuild_load.go`, `driver.go`
- Test: `quarantine_test.go`, `startlock_guard_test.go`

**Interfaces:**
- Consumes: `liftGate.tryGuard` (Task 3).
- Produces: field `Driver.relocked func(h Host, now time.Time, both bool)`
  dipanggil saat penjaga mengunci lagi di luar peralihan (diisi Task 5);
  `rebuildLoad.writableSince bool` dengan
  `func (d *Driver) noteWritableAfterLoad()` dan
  `func (d *Driver) writableAfterLoad() bool`.

- [ ] **Step 1: Test dulu** (mysqld sungguhan, RED):
  - `TestGuardRelocksWhileUp`: peran `quarantined`, putaran pertama
    selesai; `SET GLOBAL super_read_only=OFF` dari sambungan root;
    `GuardTick` berikutnya → `1 1`, nilai tersimpan ON, `relocked`
    dipanggil sekali.
  - `TestGuardLeavesRegisteredLiftAlone`: `begin(both=false)` aktif →
    `GuardTick` tidak mengubah `super_read_only`, `relocked` tidak
    dipanggil.
  - `TestGuardKeepsReadOnlyDuringLift`: `begin(both=false)` aktif, lalu
    `SET GLOBAL read_only=OFF` dari luar → `GuardTick` memasang
    `read_only=1`, `super_read_only` tetap 0, `relocked` dipanggil.
  - `TestGuardLeavesLongLiftAlone`: `begin(both=true)` aktif selama 12
    putaran → tidak ada perubahan, tidak ada laporan.
  - `TestGuardSkipsWhenCLIHoldsLock`: deskriptor lain memegang `flock`,
    `super_read_only=0`, `read_only=1` → tidak ada perubahan, tidak ada
    laporan.
  - `TestSettleRefusedAfterWritableSinceLoad`: muat selesai (catatan muat
    ada); bendera dimatikan dari luar; `GuardTick`; lalu
    `mysql.configureReplication` → ditolak dengan teks T7 persis, pin
    tetap `quarantined`, persetujuan tetap ada. Muat berikutnya menghapus
    catatan itu.
  - Test peralihan yang ada (`TestQuarantineGuardDefersToRebuildLoad`,
    `TestQuarantineStartEvents`, …) tetap lulus tanpa diubah maknanya.
- [ ] **Step 2:** Pastikan gagal.
- [ ] **Step 3:** Di `guardQuarantined`, ganti pulang-awal
  `if d.guard.up && sameRun { return nil }` dengan cabang putaran biasa:
  `tryGuard()`; `free` → baca kedua bendera, bila ada yang mati
  `persistReadOnly` lalu `d.relocked(h, now, true)` dan
  `noteWritableAfterLoad()`; tidak `free` dan bukan `both` → baca
  `read_only`, bila mati `SET GLOBAL read_only = ON`, `d.relocked(h, now,
  false)`, `noteWritableAfterLoad()`; `both` → tidak menyentuh apa pun.
  Jalur peralihan yang ada tidak berubah, kecuali bahwa jalur itu juga
  memanggil `noteWritableAfterLoad()` bila mendapati bendera mati
  (`put`) atau upaya mengunci gagal. `deferringToRebuild` tidak lagi
  melewati seluruh putaran: selama tanda muat aktif penjaga tetap
  menjalankan cabang "tidak `free`".
- [ ] **Step 4:** `settleRebuilt`: sebelum `databasesNotCopied`, bila
  `writableAfterLoad()` → kembalikan galat bertipe sendiri dengan teks T7:
  `server ini sempat tidak terkunci sesudah salinan data selesai; karantina tidak diakhiri. Mulai lagi Bangun ulang sebagai replika dari panel.`
  `mark.set()` menghapus `writableSince`.
- [ ] **Step 5:** Test lulus; gerbang; commit
  `feat(agent): the quarantine guard reads the read-only flags every round`.

### Task 5: Laporan "dikunci lagi" sebagai hasil start `late`

**Files:**
- Modify: `internal/proto/mysql.go`, `internal/agent/drivers/mysql/startlock_guard.go`,
  `start_result.go`, `driver.go`
- Test: `start_result_test.go`, `start_kinds_test.go`, `startlock_guard_test.go`

**Interfaces:**
- Produces: `QuarantineStartEvent.While string \`json:"while,omitempty"\``
  dan `StartKindState.While string \`json:"while,omitempty"\``; nilai
  `proto.QuarantineWhileRunning = "running"`.
  `func (d *Driver) reportRelocked(h Host, now time.Time, both bool)`
  dipasang ke `Driver.relocked`.
- Consumes: mekanisme `recordStart` / `enqueue(proto.EventQuarantineStart, …)`.

- [ ] **Step 1: Test dulu:**
  - `TestRelockRecordedBeforeQueued`: `reportRelocked` → berkas hasil
    start memuat jenis `late` dengan `While == "running"` sebelum kejadian
    masuk antrean; kejadian `monitor.quarantineStart` dengan
    `Result == "late"`, `While == "running"`, `StartID` baru yang berbeda
    dari `StartID` start MySQL itu.
  - `TestRelockSurvivesAgentRestart`: sesudah driver dibuat ulang,
    `HealthSnapshot.StartResult.Kinds` masih memuat entri itu.
  - `TestRelockReportReadableByOldPanelShape`: JSON kejadian itu, dibaca
    ke struct tanpa field `While`, tetap sah dan `Result == "late"`.
  - `TestRelockRepeatedReportsOncePerIncident`: tiga putaran berturut-turut
    yang mengunci lagi dalam satu menit menghasilkan satu catatan dan satu
    kejadian; yang keempat sesudah satu menit menghasilkan yang kedua.
- [ ] **Step 2:** Pastikan gagal.
- [ ] **Step 3:** Tambah field proto. `reportRelocked` memakai
  `recordStart` dengan jenis `late`, `StartID` baru (awalan `relock-`
  + waktu), `While`, lalu `enqueue`. Tahan pengulangan: paling banyak satu
  laporan per 60 detik.
- [ ] **Step 4:** Gerbang; commit
  `feat(agent): report a relock while running as a late start result`.

### Task 6: Daftar database master baru di catatan muat

**Files:**
- Modify: `internal/agent/drivers/mysql/dump.go`, `rebuild_load.go`
- Test: `rebuild_settle_test.go`, `rebuild_load_internal_test.go`, `snapshot_test.go`

**Interfaces:**
- Produces:
  `func sourceDatabases(ctx context.Context, h Host, loadBin, srcFile string, tls clientTLS) ([]string, error)`;
  `loadCopy.source []string`, `loadCopy.full bool`;
  `func (m *loadMark) sourceSeen(names []string)`.
- Consumes: hasil M2.

- [ ] **Step 1: Test dulu:**
  - `TestNotCopied` (tabel, tambah baris): dump penuh
    (`full=true, source=[a,b]`), yang ada `[a,b,c]` → `[c]`; beda huruf
    besar-kecil dihitung sama; dump bernama dan CLONE tidak berubah.
  - `TestSourceDatabasesOddNames` (mysqld sungguhan sebagai sumber): nama
    berspasi, ber-backtick, non-ASCII kembali utuh; skema sistem
    dibuang; keluaran dibatasi (lebih dari 1 MiB → galat).
  - `TestRebuildFullDumpRefusesDatabaseCreatedDuringLoad` (mysqld
    sungguhan): bangun ulang penuh; di tengah muat, akun ber-SUPER membuat
    database `liar`; `mysql.configureReplication` menolak dengan teks
    "masih punya database yang tidak disalin" yang memuat `liar`; pin tetap
    `quarantined`.
  - `TestRebuildFullDumpSettles` yang ada tetap lulus.
  - `TestDumpFailsBeforeChangingData` (tambah kasus): daftar pertama tidak
    terbaca → muat gagal, tidak ada yang berubah lokal.
  - `TestSecondSourceListFailureLeavesNoLoadRecord`: daftar kedua gagal →
    settle menolak dengan teks "salinan harus diulang" yang ada.
- [ ] **Step 2:** Pastikan gagal.
- [ ] **Step 3:** `sourceDatabases` menjalankan
  `loadBin --defaults-file=<srcFile> <argumen TLS> -N -B -e "SELECT HEX(schema_name) FROM information_schema.schemata"`
  lewat `h.Exec`, keluaran dibatasi 1 MiB, tiap baris di-dekode dari hex,
  skema sistem (`systemDatabases`) dibuang.
- [ ] **Step 4:** Di `dumpFrom`, bila `mark != nil` dan
  `len(p.Databases) == 0`: baca daftar sekali sebelum komentar "From here
  on local data changes", dan sekali sesudah `pipeDump` berhasil;
  `mark.sourceSeen` menyimpan gabungannya; kegagalan daftar kedua membuat
  `mark.done` tidak dipanggil. `notCopied`: untuk `full`, nama yang ada
  tetapi tidak di `source` → tidak disalin (ditambah aturan `before` yang
  ada).
- [ ] **Step 5:** Gerbang; commit
  `feat(agent): end a quarantine only when every database is on the new master's list`.

### Task 7: `mysqlbinlog` dan ekor log sebagai uid akun layanan

Dikerjakan hanya bila Task 2 (M3) mengizinkan; bila tidak, task ini
ditandai tidak dibangun dengan alasannya dan dilaporkan.

**Files:**
- Create: `internal/agent/drivers/mysql/svcuid.go`, `svcuid_test.go`
- Modify: `leftover.go` (`streamBinlog`), `salvage.go`, `binlog.go`
  (`readBinlogGTIDs`, `lastBinlogFile`), `startlock_guard.go`
  (`watchBootFailure`)

**Interfaces:**
- Produces:
  `func serviceCredential(ctx context.Context, h Host) (*syscall.Credential, error)`
  (akun dari run `--print-defaults` sebagai root: `--user` terakhir, bawaan
  `mysql`; `lookupUser` + `accountCredential`; tanpa syarat home);
  `func execAsService(ctx context.Context, h Host, warn func(string), stdin io.Reader, stdout io.Writer, name string, args ...string) error`
  (pakai kredensial itu; bila kredensial tidak ada atau proses tidak bisa
  dijalankan sebagai uid itu, jalankan lewat `h.Exec` dan panggil `warn`
  sekali dengan
  `tidak bisa menjalankan sebagai akun layanan MySQL; dijalankan sebagai root`).

- [ ] **Step 1: Test dulu** (host palsu yang mencatat kredensial, seperti
  `printDefaultsHost`):
  - `TestBinlogToolsRunAsServiceAccount`: `mysql.leftover`,
    `mysql.salvage`, `mysql.binlogTail` (MySQL mati) memanggil
    `ExecEnvAs` dengan kredensial uid layanan untuk `mysqlbinlog`; indeks
    binlog dibaca lewat `cat --` dengan kredensial yang sama.
  - `TestServiceCredentialOtherUser`: `--user=mysqld_app` → uid akun itu.
  - `TestServiceCredentialMissingFallsBackToRoot`: pencarian gagal →
    perintah tetap jalan lewat `Exec`, `warn` dipanggil sekali, hasil
    Periksa/Amankan sama.
  - `TestBootLogTailAsServiceAccount`: ekor log dibaca
    `tail -c 65536 -- <path>` sebagai uid layanan, keluaran dibatasi
    64 KiB, batas waktu 5 detik (proses yang menggantung dihentikan,
    penjaga tidak tertahan); tanpa kredensial → `readTail` yang ada.
  - Test Periksa/Amankan dengan mysqld sungguhan yang ada tetap lulus.
- [ ] **Step 2:** Pastikan gagal.
- [ ] **Step 3:** Tulis `svcuid.go`; ganti tiga pemanggilan `mysqlbinlog`,
  pembacaan indeks, dan pembacaan ekor log. `warn` menulis ke log
  pekerjaan agent (`j.Logf("warn", …)`) atau `slog` di penjaga.
- [ ] **Step 4:** Gerbang; commit
  `fix(agent): run mysqlbinlog and read the error log as the MySQL service account`.

### Task 8: Sesi root dan `drop-database`

**Files:**
- Create: `internal/agent/cli_rootsql.go`, `internal/agent/cli_dropdb.go`,
  test `internal/agent/cli_dropdb_test.go`
- Modify: `internal/agent/cli.go` (dispatch, bantuan),
  `internal/agent/drivers/mysql/rebuild_load.go` (tulis `not-copied`),
  `quarantine.go` (hapus `not-copied` saat settle)

**Interfaces:**
- Produces:
  - `type rootSession struct{ DB *sql.DB; DefaultsFile string; Close func() }`,
    `func openRootSession(ctx context.Context, dir string, in io.Reader, stdout io.Writer) (*rootSession, error)`:
    socket dari tata letak agent, pengguna `root` tanpa kata sandi; bila
    ditolak, cetak `Kata sandi root MySQL: `, baca satu baris tanpa gema
    (termios lewat paket `syscall`; bila `in` bukan terminal, baca baris
    biasa), coba lagi sekali. `DefaultsFile` adalah berkas klien sementara
    `0600` untuk `mysqldump`, dihapus `Close`.
  - `var cliRootSession = openRootSession` (seam test).
  - `func mainDropDatabase(args []string, in io.Reader, stdout, stderr io.Writer) int`.
  - `const NotCopiedFile = "/var/lib/dbpanel/not-copied"` (paket mysql);
    `func WriteNotCopied(path string, names []string) error`,
    `func ReadNotCopied(path string) ([]string, error)` (berkas biasa
    milik root, `O_NOFOLLOW`, satu nama ter-hex per baris, paling banyak
    1 MiB), `func RemoveNotCopied(path string) error`.
- Consumes: `AcquireLiftLock` (Task 3); `mysql.LoadPins`.

Urutan `drop-database <nama> [--ke <folder>] [--dir <dir>]`:
1. Pin harus `quarantined`; bukan → tolak dengan
   `perintah ini hanya untuk server yang dikarantina (quarantined); peran server ini: <peran>`.
2. `openRootSession`. `SELECT @@global.read_only, @@global.super_read_only`
   harus `1 1`; bukan → tolak dengan
   `kunci baca-saja server ini sedang tidak terpasang penuh; tunggu beberapa detik (agent memasangnya lagi) lalu ulangi`.
3. Nama harus ada di `information_schema.schemata` (banding tanpa beda
   huruf besar-kecil, pakai nama persis dari server) dan bukan skema
   sistem.
4. Hitung tabel dasar dan perkiraan ukuran dari
   `information_schema.tables`; ruang kosong folder cadangan (`statfs`)
   harus lebih dari perkiraan itu.
5. Cadangan: berkas `<folder>/<nama aman>-<UTC yyyymmddThhmmssZ>.sql`
   dibuka `O_CREATE|O_EXCL|O_NOFOLLOW`, `0600` ("nama aman": karakter di
   luar `[A-Za-z0-9_.-]` menjadi `_`). `mysqldump --defaults-file=…
   --single-transaction --routines --triggers --events
   --set-gtid-purged=OFF --databases <nama>` lewat `exec` tanpa shell.
6. Periksa: kode keluar 0; baris terakhir berkas berawalan
   `-- Dump completed on`; ada baris `CREATE DATABASE` untuk nama itu;
   jumlah baris berawalan `CREATE TABLE` sama dengan jumlah tabel dasar.
   Gagal satu → hapus berkas yang tidak lengkap, tolak, tidak ada yang
   dihapus dari MySQL.
7. Tampilkan ringkasan (nama, jumlah tabel, berkas, ukuran, SHA-256,
   contoh `scp` dan `sha256sum`), lalu konfirmasi: nama ada di
   `not-copied` → kalimat A dan `ya`; selain itu → kalimat B dan operator
   mengetik nama database persis.
   - A: `Agent mencatat database ini tidak disalin dari master baru; cadangan di atas satu-satunya salinannya.`
   - B: `Agent tidak bisa memastikan database ini tidak ada di master baru. Bila database ini ada di master baru, menghapusnya di sini merusak replikasi server ini. Ketik nama database itu untuk melanjutkan, atau apa saja untuk membatalkan: `
8. `AcquireLiftLock`; pada satu sambungan:
   `SET GLOBAL super_read_only=OFF`, `SET SESSION sql_log_bin=0`,
   `DROP DATABASE`, lalu **selalu** (juga sesudah galat, dengan konteks
   yang tidak ikut batal) `SET GLOBAL super_read_only=ON`; lepas kunci.
9. Periksa `1 1`; bukan → pasang lagi dan katakan. Tulis syslog. Cetak
   langkah berikutnya:
   `Selesai. Tekan "Ulangi dari tahap ini" pada pekerjaan di panel, atau mulai lagi Bangun ulang sebagai replika.`

- [ ] **Step 1: Test dulu** (mysqld sungguhan lewat seam, pola
  `TestCLIApprove`):
  - `TestDropDatabaseHappyPath`: server `1 1`; sesudahnya database
    hilang, `gtid_executed` tidak berubah, `1 1`, nilai tersimpan ON,
    cadangan ada dan lulus keempat pemeriksaan, syslog satu baris.
  - `TestDropDatabaseTruncatedDumpStops`: `mysqldump` palsu yang berhenti
    di tengah (keluar bukan 0, atau tanpa baris penutup, atau jumlah
    `CREATE TABLE` kurang) → database masih ada, berkas tidak lengkap
    dihapus.
  - `TestDropDatabaseNeverOverwrites`: berkas dengan nama yang sama sudah
    ada → berkas baru dengan nama lain atau tolak; berkas lama utuh.
  - `TestDropDatabaseConfirmations`: nama di `not-copied` + `ya` → jalan;
    nama tidak di daftar + `ya` → batal; + nama persis → jalan; jawaban
    lain → batal, tidak ada yang berubah.
  - `TestDropDatabaseOddNames`: nama berspasi, ber-backtick, bertanda
    kutip, huruf besar: dibuang dengan benar, nama berkas aman.
  - `TestDropDatabaseErrorRelocks`: `DROP` gagal (kunci foreign key dari
    database lain) → akhir `1 1`, kode keluar bukan 0.
  - `TestDropDatabaseKilledMidway`: proses dihentikan sesudah
    `super_read_only=OFF` → kunci berkas bebas; `GuardTick` berikutnya
    memasang `1 1` dan melapor.
  - `TestDropDatabaseNoSpace`: perkiraan lebih besar dari ruang kosong →
    tolak sebelum `mysqldump`.
  - `TestCLIRefusesWhileLoadHoldsLock`: gate memegang kunci →
    `ErrLiftBusy`, tidak ada yang berubah.
  - `TestDropDatabaseRefusals`: peran bukan `quarantined`; skema sistem;
    nama tidak ada; bendera bukan `1 1`.
  - `TestRootSessionPasswordFallback`: socket ditolak → meminta kata
    sandi, masuk; kata sandi tidak muncul di stdout, stderr, atau syslog.
  - `TestNotCopiedFileWrittenAndCleared`: `errNotReplaced` menulis
    daftarnya; settle yang berhasil dan muat baru menghapusnya.
- [ ] **Step 2:** Pastikan gagal.
- [ ] **Step 3:** Tulis `cli_rootsql.go`, `cli_dropdb.go`; daftarkan
  `drop-database` di `RunCLIWith` dan teks bantuan.
- [ ] **Step 4:** Agent menulis `not-copied` di tempat `errNotReplaced`
  dibuat, menghapusnya di `settleRebuilt` yang berhasil dan di
  `mark.set()`.
- [ ] **Step 5:** Gerbang; commit
  `feat(agent): drop-database, a checked backup and drop on a quarantined server`.

### Task 9: `grant-admin`, `reset-admin`, dan teks agent yang menunjuk perintah

**Files:**
- Create: `internal/agent/cli_admin.go`, `cli_admin_test.go`
- Modify: `internal/agent/cli.go`,
  `internal/agent/drivers/mysql/rebuild_load.go` (`errNotReplaced`,
  `errListIncomplete`), `connect.go`, `install.go` (ekspor pembuat akun
  admin)

**Interfaces:**
- Produces: `func mainGrantAdmin(args []string, in io.Reader, stdout, stderr io.Writer) int`,
  `func mainResetAdmin(args []string, in io.Reader, stdout, stderr io.Writer) int`;
  `func MissingAdminGrants(ctx context.Context, root *sql.DB, user, host string) ([]string, error)`
  (paket mysql: `missingGrants` terhadap `requiredGrants`, ditambah
  `GRANT SHOW DATABASES ON *.* TO …` bila bukti daftar lengkap belum
  terpenuhi);
  `func ResetAdmin(ctx context.Context, root *sql.DB, creds CredentialStore) error`
  (isi `createAdmin` yang ada, memakai sambungan yang diberikan).
- Consumes: `openRootSession`, `AcquireLiftLock` (Task 8, 3).

Kedua perintah: pin `quarantined` (selain itu tolak dengan teks Task 8
butir 1); sesi root; bendera `1 1`; tampilkan pernyataan yang akan
dijalankan; `ya`; di bawah kunci berkas dan dengan `sql_log_bin=0`
jalankan pernyataannya di antara `super_read_only=OFF` dan `=ON` (selalu
memasang lagi); periksa `1 1`; syslog.
- `grant-admin`: akun = akun tersimpan agent; tanpa akun tersimpan → tolak
  dengan `agent belum punya akun MySQL tersimpan; jalankan sudo dbpanel agent reset-admin`.
  Tidak ada yang kurang → `Akun <akun> sudah punya semua hak yang dibutuhkan.`
  dan keluar 0.
- `reset-admin`: `ResetAdmin`; akhirnya cetak
  `Akun dbpanel_admin@localhost dibuat ulang dan disimpan sebagai akun agent. Di panel, klik Segarkan di halaman server ini.`

Teks agent baru (dipakai persis):
- `errNotReplaced`, dua kalimat terakhir diganti (T8):
  `Jalankan di server ini: sudo dbpanel agent drop-database <nama>, lalu tekan "Ulangi dari tahap ini" pada pekerjaan di panel.`
- `errListIncomplete` (kurang hak):
  `daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL %s yang dipakai agent tidak punya hak SHOW DATABASES. Jalankan di server ini: sudo dbpanel agent grant-admin, lalu tekan "Ulangi dari tahap ini" pada pekerjaan di panel.`
- `errListIncomplete` (nama tidak didukung):
  `daftar database di server ini tidak bisa dipastikan lengkap: nama akun MySQL yang dipakai agent memuat karakter yang tidak didukung pemeriksaan ini (yang didukung: huruf, angka, garis bawah, titik, tanda hubung, dan tanda dolar). Jalankan di server ini: sudo dbpanel agent reset-admin, lalu tekan "Ulangi dari tahap ini" pada pekerjaan di panel.`
- `mysql.connect` di server yang dikarantina, akun kurang hak:
  `server ini dikarantina (quarantined): akun %s belum punya semua hak yang dibutuhkan. Jalankan di server ini: sudo dbpanel agent grant-admin, lalu hubungkan lagi.`

- [ ] **Step 1: Test dulu:** `TestGrantAdminAddsMissing` (akun tanpa
  `SHOW DATABASES` dan tanpa satu hak dinamis → sesudahnya
  `mysql.connect` menerima, bukti daftar lulus, `gtid_executed` tidak
  berubah, `1 1`); `TestGrantAdminNothingMissing`;
  `TestGrantAdminFailingGrantRelocks`; `TestResetAdminStoresAccount`
  (akun tersimpan berganti, login dengan akun itu berhasil, akun lama
  tidak dihapus); `TestAdminCommandsRefusals` (peran, bendera, kunci
  dipegang); test teks: `TestListIncompleteText`,
  `TestListUnsupportedAccountText`, test `errNotReplaced`,
  `TestConnectWeakAccountOnQuarantinedServer` diperbarui ke teks di atas;
  `TestRebuildPartialRevokeAccount` dan
  `TestRebuildRoleOnlyAccountAtSettle` memberi hak lewat `MissingAdminGrants`
  + jalur CLI, bukan heredoc.
- [ ] **Step 2:** Pastikan gagal.
- [ ] **Step 3:** Implementasi; daftarkan kedua subperintah.
- [ ] **Step 4:** Gerbang; commit
  `feat(agent): grant-admin and reset-admin; refusals point to the commands`.

### Task 10: Butir kecil agent

**Files:**
- Modify: `internal/agent/drivers/mysql/salvage.go`, `leftover.go`,
  `rebuild_load.go` (`completeUserDatabases`), `replication.go`,
  `quarantine.go`

- [ ] **Step 1: Test dulu:**
  - `TestLeftoverSetStampedWithSource`: berkas `leftover-gtid` memuat
    baris pertama `source <host:port>`; `mysql.salvage` dengan pin sumber
    lain → ditolak dengan teks "Periksa data tertinggal dulu" yang ada.
  - `TestLeftoverSetWithoutSourceIsNoRecord`: berkas bentuk lama (tanpa
    baris sumber) → dianggap tidak ada.
  - `TestSettleRemovesLeftoverSet`.
  - `TestListingProofSessionSettings`: sesi bukti menyetel pernyataan
    hasil M4 sebelum `SHOW GRANTS`; dengan `sql_quote_show_create=OFF`
    global, akun ber-`SHOW DATABASES` tetap terbukti.
  - `TestStartReplicaRefusesForeignChannelOnQuarantined`: di server
    `quarantined` dengan persetujuan, kanal replikasi yang sumbernya bukan
    pin sumber → `mysql.startReplica` dan `mysql.startUntil` menolak
    dengan
    `sumber kanal replikasi di server ini (%s) bukan sumber yang dipin (%s); perintah %s ditolak`.
- [ ] **Step 2:** Pastikan gagal. **Step 3:** Implementasi (cap sumber di
  `recordLeftover`/`readLeftoverSet`; hapus di `settleRebuilt`; `SET
  SESSION` di `completeUserDatabases`; pembanding sumber dari
  `performance_schema.replication_connection_configuration`).
- [ ] **Step 4:** Gerbang; commit `fix(agent): leftover set source stamp, proof session settings, channel source check`.

### Task 11: Panel — aturan satu keadaan di orchestrator

**Files:**
- Modify: `internal/panel/orchestrator/runner.go`, `create_replica.go`,
  `approval.go`, `controls.go`, `resync.go`;
  `internal/panel/store/topologies.go`, `jobs.go`
- Test: `failed_rebuild_test.go`, `approval_test.go`, `superseded_test.go`,
  `controls_test.go`, `store/*_test.go`

**Interfaces:**
- Produces:
  - `func (r *Runner) endRebuildAttempt(j store.Job, st jobState) error`
    (peran → `quarantined` bila `KindCreateReplica`, `RebuildOldMaster`,
    `!QuarantineEnded`).
  - `func (r *Runner) pendingRebuild(j store.Job, st jobState) (bool, error)`
    (empat syarat spek 5.1).
  - `func (s *Store) NewerJobExistsExcept(serverID int64, jobID string, kinds ...string) (bool, error)`.
  - `func (r *Runner) normalizeFailedRebuilds() error`.
  - `AwaitingRebuild(st *store.Store, srv store.Server) (bool, error)`
    tetap tanda tangannya; isinya `srv.Role == store.RoleQuarantined`.
- Removes: `Store.ReturnToQuarantine`, `store.ErrNotFailedRebuild`,
  `Runner.CancelRebuildAttempt`, `latestRebuildEnded`, `errNotFailedRebuild`.

- [ ] **Step 1: Test dulu** (RED; satu test per baris tabel spek 5.1):
  - `TestFailedRebuildReturnsToQuarantine`: gagal sebelum salinan, di
    salinan, dan di sambung sebelum jawaban settle → peran `quarantined`,
    baris gagal, tanda ada, `AwaitingRebuild` benar.
  - `TestFailedRebuildAfterSettleStaysReplica` (yang ada, tetap).
  - `TestResumeNormalizesHalfFailedRebuild`: job berjalan saat panel
    berhenti → sesudah `Resume` peran `quarantined`, baris gagal.
  - `TestNormalizeLegacyFailedRebuilds`: baris lama (peran `replica` +
    gagal + tanda) → `quarantined`; yang job terbarunya berbendera →
    tanda dihapus, peran tetap `replica`; dijalankan dua kali hasilnya
    sama; baris replika gagal biasa (bukan master lama) tidak disentuh.
  - `TestRetryFromQuarantinedFlipsRole`: `Retry` → peran `replica`, job
    berjalan; peluncuran gagal → `quarantined` lagi. Sama untuk `Approve`.
  - `TestRetryStillRefusedWhenSuperseded`: rebuild baru, job sertifikat,
    master berganti → tetap ditolak, peran tidak berubah.
  - `TestLeftoverJobDoesNotSupersedeFailedRebuild`: sesudah Periksa dan
    Amankan data tertinggal, `Retry` job bangun ulang diterima; untuk job
    non-rebuild atau yang berbendera, job Periksa tetap menggantikan.
  - `TestQuarantinedWithRowRefusesResyncDetachCreate`: Resync, Lepas (dua
    mode), Buat replika biasa → `ErrAwaitingRebuild`, tidak ada perintah
    agent, tidak ada perubahan store.
  - `TestDeleteQuarantinedWithFailedRow`: hapus berhasil, baris ikut
    terhapus, master baru bisa dihapus sesudah replika lain dilepas.
  - `TestNewRebuildFromQuarantinedWithRow`: bangun ulang baru berjalan
    sampai selesai; job lama digantikan.
  - `TestLeftoverFromQuarantinedWithRow`: Periksa/Amankan diterima pada
    `CopyNotStarted` dan `CopyUncertain`, ditolak pada `CopyStarted`.
  - Test yang gugur bersama Batalkan dihapus:
    `TestCancelRebuildAttempt`, `TestCancelRebuildAttemptThenDelete`,
    subtest "cancel attempt after copy", `TestReturnToQuarantine`.
    Test yang harapannya berubah diperbarui dan didaftar di laporan:
    `TestRebuildAgainAfterFailedRebuild`,
    `TestFailedRebuildRefusesResyncAndDetach`,
    `TestRebuildFailureBeforeSettleKeepsOldMaster`,
    `TestRebuildRetryAndApproveRecheckApproval`,
    `TestDeleteServerRefusesMasterAndReplica`.
- [ ] **Step 2:** Pastikan gagal.
- [ ] **Step 3:** `fail` dan `Resume` memanggil `endRebuildAttempt` sesudah
  baris ditandai gagal, lalu `replicaChanged`. `Resume` memanggil
  `normalizeFailedRebuilds` lebih dulu.
- [ ] **Step 4:** `supersededBy`: cabang "peran sudah berubah" tidak
  berlaku bila `pendingRebuild`; pemeriksaan "job lebih baru" memakai
  `NewerJobExistsExcept(…, KindLeftover, KindSalvage)` bila
  `pendingRebuild`. `Retry` dan `Approve`: bila `pendingRebuild`,
  `SetServerRole(replica)` bersama `ReopenJob`, dan kembali `quarantined`
  bila `launch` gagal.
- [ ] **Step 5:** `refuseAwaitingRebuild` tanpa gerbang peran;
  `deletableLocked` membolehkan baris bila peran `quarantined`; hapus
  fungsi-fungsi Batalkan di store dan orchestrator.
- [ ] **Step 6:** Gerbang panel dan `make test` (web belum dikompilasi
  ulang tanpa Task 12 bila antarmuka `orchestrator` di web berubah:
  hapus pemakaian `CancelRebuildAttempt` di web dalam task ini sebatas
  supaya paket terkompilasi; tampilan dirapikan di Task 12). Commit
  `feat(panel): a failed rebuild of an old master leaves it quarantined`.

### Task 12: Panel — monitor, kotak, form, daftar

**Files:**
- Modify: `internal/panel/monitor/state.go`, `start_state.go`,
  `promote.go`, `alerts.go`, `monitor.go`;
  `internal/panel/web/handlers_oldmaster.go`, `handlers_mysql.go`,
  `handlers_servers.go`, `handlers_replication.go`, `router.go`,
  `handlers_confirm.go`, `audit_summary.go`, `render.go`;
  `web/templates/server_detail.html`, `replica_detail.html`, `_ui.html`;
  `internal/panel/orchestrator/runner.go` (pemulihan dari pin)
- Test: `monitor/alerts_test.go`, `monitor/state_test.go`,
  `web/web_failed_rebuild_test.go`, `web/web_oldmaster_test.go`,
  `orchestrator/failed_rebuild_test.go`

**Interfaces:**
- Consumes: Task 11; `proto.StartKindState.While` (Task 5).
- Produces:
  - `monitor.Options.CopyStarted func(serverID int64) (bool, error)`
    (diisi saat panel dirangkai dengan
    `orchestrator.RebuildCopyState(...) == CopyStarted`).
  - `monitor.Options.OldMasterReportsReplica func(serverID int64, since time.Time)`
    dipanggil sesudah 3 laporan berturut-turut dengan
    `RolePin == "replica"` dan `SourcePin` sama dengan sumber topologi,
    untuk server berperan `quarantined` yang punya baris; `since` = waktu
    laporan pertama dari ketiganya.
  - `func (r *Runner) SettleFromAgentPin(serverID int64, since time.Time) error`:
    bila server `quarantined` + baris gagal + `pendingRebuild` job
    terbarunya, dan `since` sesudah job itu berakhir → pasang
    `QuarantineEnded`, `ClearOldMaster`, peran `replica`. Selain itu tidak
    berbuat apa-apa.

Teks (dipakai persis):
- T1 `startRelockedText`:
  `MySQL master lama sempat tidak terkunci selagi berjalan; agent sudah menguncinya lagi. Tulisan yang masuk selama itu tidak ada di master baru: jalankan Periksa data tertinggal`
- `startLateCopiedText` (keadaan `CopyStarted`, untuk kedua sebab):
  `master lama sempat bisa ditulis; bangun ulang sudah mulai mengganti data server ini, jadi tulisan itu tidak bisa diperiksa lagi. Selesaikan Bangun ulang sebagai replika, atau hapus server ini`
- T2 `copyStartedCloseReason`:
  `bangun ulang sudah mulai mengganti data server ini; tulisan itu tidak bisa diperiksa lagi`
- Label jenis `master_start_late` di `alertKindLabels` dan `kindNames`:
  `Master lama sempat bisa ditulis`.
- T3 kotak: `Agent server ini melaporkan karantinanya sudah berakhir (peran replica). Mulai lagi Bangun ulang sebagai replika, atau hapus server ini.`

- [ ] **Step 1: Test dulu:**
  - `TestResolveQuarantinedWithRow`: peran `quarantined` + baris gagal →
    dinilai master lama: `master_start_unguarded` terbuka dari laporan
    tanpa kunci start; status `DeriveQuarantined`; aturan replika tertutup
    dengan alasan "server menjadi master lama".
  - `TestStartResultsJudgedWhileRebuildRunning`: peran `replica`, tanda
    master lama ada, job berjalan; laporan memuat jenis `late` →
    peringatan terbuka.
  - `TestRelockedOpensLateAlertWithItsText`: jenis `late` dengan
    `While == "running"` → pesan T1; tanpa `While` → teks lama.
  - `TestLateAlertFollowsCopyState`: `CopyStarted` → pesan
    `startLateCopiedText`; peringatan terbuka ditutup dengan alasan T2;
    `CopyUncertain` dan `CopyNotStarted` → tidak ditutup.
  - `TestPinReplicaHealsLostSettle`: 2 laporan → tidak ada; laporan ke-3
    → `SettleFromAgentPin` dipanggil; laporan yang lebih tua dari akhir
    job tidak dihitung; sumber pin lain → hitungan nol lagi; sesudahnya
    server replika gagal biasa (Resync dan Lepas diterima).
  - Web `TestQuarantinedWithRowBox`: kotak tampil dengan Hapus, tautan
    Bangun ulang, Periksa/Amankan (`CopyNotStarted`), kalimat tidak pasti
    (`CopyUncertain`), kalimat salinan-mulai tanpa tombol (`CopyStarted`);
    tidak ada tombol atau kalimat Batalkan di mana pun
    (`rebuild-attempt/cancel` → 404).
  - `TestRebuildFormFromQuarantinedWithRow`: form 200; peringatan mengikuti
    keadaan salinan; tidak ada kalimat "percobaan masih tercatat".
  - `TestReplicaListsLeaveOutQuarantined`: dasbor, halaman replikasi dan
    halaman master tidak menampilkan server `quarantined` sebagai kartu
    replika.
  - `TestJobPageOffersRetryFromQuarantined`: "Ulangi dari tahap ini"
    tampil; sesudah bangun ulang baru, tidak.
  - `TestBoxSaysAgentReportsReplica`: server `quarantined` tanpa baris
    dengan laporan `RolePin == "replica"` → kalimat T3.
  - Test yang gugur: `TestCancelRebuildAttemptOffered`,
    `TestCancelRebuildAttemptPost`; yang berubah harapannya diperbarui dan
    didaftar: `TestFailedRebuildOffersRebuildAgain`,
    `TestFailedRebuildReplicaPage`, `TestFailedRebuildJobPage`,
    `TestLeftoverWordingAfterFailedRebuild`,
    `TestStartLateSurvivesRebuildUntilCopied`, `wantStartLateText`.
- [ ] **Step 2:** Pastikan gagal.
- [ ] **Step 3:** Monitor: `resolve` membaca server lebih dulu sesudah
  pemeriksaan master; gerbang `startResultFromSnapshot` menerima juga
  server yang tanda master lamanya masih ada; pesan dan penutupan
  `master_start_late`; hitungan pin di `serverState`.
- [ ] **Step 4:** Orchestrator `SettleFromAgentPin`; rangkai kedua
  callback di tempat monitor dibuat.
- [ ] **Step 5:** Web: `oldMasterBox` tanpa `RebuildFailed`; hapus rute,
  handler, aksi konfirmasi, label audit, flash, partial, tiga teks
  Batalkan dan `msgAttemptPending`; daftar replika; kalimat T3.
  `bash scripts/tailwind.sh`.
- [ ] **Step 6:** Gerbang panel, `make test`; commit
  `feat(panel): monitor and pages follow the one rule for old masters`.

### Task 13: Promote kedua

**Files:**
- Modify: `internal/panel/orchestrator/promote.go`, `promote_view.go`;
  `internal/panel/web/handlers_oldmaster.go`; `web/templates/promote.html`,
  `server_detail.html`
- Test: `orchestrator/promote_test.go`, `web/web_promote_test.go`,
  `web/web_oldmaster_test.go`

**Interfaces:**
- Produces: `PromoteView.PendingOldMaster string` (nama server, kosong
  bila tidak ada); `var errOldMasterPending` dengan teks T4.

Teks:
- T4: `master lama %s dari promote sebelumnya belum dibangun ulang atau dihapus; selesaikan itu dulu`
- T5: `Master lama %s dari promote sebelumnya belum dibangun ulang. Sesudah promote ini, server itu tidak bisa lagi diperiksa atau dibangun ulang dari panel; yang tersisa hanya Hapus server.`
- T6: `Topologi ini sudah promote lagi sejak server ini dikarantina. Data tertinggal dan Bangun ulang tidak tersedia lagi untuk server ini. Hapus server ini dari panel, lalu pasang lagi sebagai replika baru bila masih dipakai.`

- [ ] **Step 1: Test dulu:** `TestPlannedPromoteRefusedWhileOldMasterPending`
  (tanda master lama menunjuk server yang masih ada dan `quarantined`,
  dengan atau tanpa baris → T4, tidak ada perintah agent);
  `TestEmergencyPromoteNotRefused` (jalan; `PendingOldMaster` terisi;
  layar memuat T5); `TestOldMasterAfterSecondPromote` (sesudah promote
  darurat kedua: kotak server pertama memuat T6, hanya Hapus; Periksa
  ditolak dengan teks yang ada; Ulangi ditolak "master topologi ini sudah
  berganti"; hapus berhasil).
- [ ] **Step 2:** Pastikan gagal. **Step 3:** Implementasi.
  `bash scripts/tailwind.sh`.
- [ ] **Step 4:** Gerbang panel, `make test`; commit
  `feat(panel): a planned promote waits for the earlier old master`.

### Task 14: Label teks agent, sensor, dan butir kecil panel

**Files:**
- Create: `internal/panel/orchestrator/agenttext.go`
- Modify: `orchestrator/runner.go` (`fail`, `logf`, `callWithin`),
  `agentjob.go`, `snapshot.go` (`markCopySent`), `oldmaster.go`
  (`RebuildCopyState`), `create_replica.go` (teks T9);
  `internal/panel/hub/hub.go`; `internal/panel/redact/redact.go`;
  `internal/panel/web/handlers_*.go` (flash), `web/templates/server_detail.html`
  (spanduk kurang hak)
- Test: `orchestrator/agenttext_test.go` (baru), `redact/phrases_test.go`,
  `orchestrator/copy_state_test.go`, test web flash

**Interfaces:**
- Produces:
  - `func AgentQuoteN(server, text string, limit int) string`
    (`AgentQuote` memanggilnya dengan `maxAgentQuote`).
  - `type agentTextError struct{ server, text string }` (`Error()` =
    `AgentQuoteN(server, text, maxLogLine)`).
  - `func fromAgent(err error) (text string, ok bool)`: benar untuk
    `*proto.Error` yang datang dari agent; salah untuk galat buatan panel
    (`proto.ErrOffline`, batas waktu hub — hub mengekspor nilai pembanding
    `hub.ErrTimeout` bila belum ada).
- Consumes: teks agent Task 9.

- [ ] **Step 1: Test dulu:**
  - `TestJobErrorLabelsAgentText`: langkah gagal dengan `*proto.Error`
    agent → galat job, pesan langkah dan galat terakhir replika =
    `<judul>: Pesan dari agent <server> (bukan dari panel): <teks>`;
    teks multi-baris dan penanda arah diratakan; teks 600 karakter tidak
    dipotong di 300; galat batas waktu dan agent terputus tidak dilabeli.
  - `TestAgentJobFailureLabelled`: `JobDone.Error` dari job agent dilabeli
    sama.
  - `TestAgentLogLinesPrefixed`: baris log dari agent → `agent: <satu baris>`.
  - `TestFlashesLabelAgentText`: Hubungkan MySQL, Segarkan, kendali
    replika, job master lama: galat agent tampil berlabel.
  - `TestQuotesKeepsNewAdvice`: empat teks agent Task 9 (disalin lewat
    fungsi yang diekspor agent untuk test, bukan disalin tangan) keluar
    dari `redact.Quotes` utuh sesudah dilabeli; frasa README yang sudah
    tidak dipakai (`Database yang tidak ada di master baru`,
    `Memberi hak SHOW DATABASES di server yang dikarantina`) tidak lagi
    dipertahankan; nama akun pada teks kurang-hak tetap utuh; titik tetap.
  - `TestQuotesAdvisedAccountAnchored` (diperbarui, N-H5): jangkar hanya
    pada awal kalimat agent yang utuh di awal pesan atau sesudah label.
  - `TestCopySentSaveFailureStopsStep` (N-H1): simpan gagal → langkah
    gagal sebelum perintah salinan dikirim.
  - `TestCopyStateStoredJobs` (tambah baris, N-H3): `CopySent` dengan
    nomor promosi 0 → `CopyUncertain`.
  - `TestCopyStateCloneNeedsRealProgress` (N-H4): untuk CLONE, progres 0%
    bukan bukti; progres di atas 0 atau hasil adalah bukti.
  - `TestWipeApprovalTextForOldMaster` (T9): pada bangun ulang master
    lama pesan persetujuan =
    `database berikut di master lama ini akan dihapus lalu disalin ulang dari master baru: a, b`;
    pada buat-ulang biasa teks lama.
  - `TestMissingGrantsBannerOnQuarantined`: halaman server `quarantined`
    dengan `MissingGrants` menampilkan
    `Akun admin MySQL ini kurang hak. Jalankan di server ini: sudo dbpanel agent grant-admin, lalu klik Segarkan.`
    tanpa blok `GRANT`; peran lain tetap seperti sekarang.
- [ ] **Step 2:** Pastikan gagal.
- [ ] **Step 3:** `agenttext.go`; `callWithin` membungkus galat agent;
  `agentJobEnded` mengembalikan `agentTextError`; `fail` tidak mengubah
  urutan (label ada di teks galat, lalu `redact.Quotes`); `logf` untuk
  baris dari agent; web memakai `shownError` di setiap flash yang
  menampilkan galat pemanggilan agent.
- [ ] **Step 4:** `redact`: daftar frasa menjadi `Ulangi dari tahap ini`,
  `Paksa dump`, `Sudah saya alihkan`; jangkar akun mengikuti teks Task 9.
  Agent mengekspor teksnya untuk test (`export_test.go` di paket mysql
  atau fungsi `…Text` yang sudah ada) dan test panel memakainya.
- [ ] **Step 5:** N-H1, N-H3, N-H4, T9, spanduk.
  `bash scripts/tailwind.sh`.
- [ ] **Step 6:** Gerbang panel, `make test`; commit per kelompok (label;
  sensor; butir kecil).

### Task 15: README, teks untuk pemilik, e2e

**Files:**
- Modify: `README.md`; rencana ini ("Teks baru untuk dibaca pemilik");
  `internal/panel/e2e_replica_test.go` (atau berkas e2e yang cocok)

- [ ] **Step 1: e2e dulu** (RED bila perilaku belum ada):
  - `TestE2ERebuildFailsBeforeCopyThenLeftoverThenRetry`: bangun ulang
    master lama gagal sebelum salinan → halaman server: dikarantina,
    Periksa data tertinggal berjalan → "Ulangi dari tahap ini" masih ada
    → bangun ulang selesai; server replika; kotak hilang.
  - `TestE2ERebuildFailsAfterSettle`: gagal di Verifikasi → replika gagal
    biasa (Resync tersedia, tanpa kotak).
  - `TestE2ERelockAlert`: `SET GLOBAL super_read_only=OFF` di master lama
    → dalam dua putaran penjaga panel membuka peringatan dengan teks T1;
    Periksa data tertinggal menutupnya.
- [ ] **Step 2:** README (setiap perintah dijalankan dulu; teks disalin
  dari kode):
  - "Database yang tidak ada di master baru": ditulis ulang — kapan
    terjadi; satu perintah `sudo dbpanel agent drop-database <nama>`; arti
    setiap bagian keluarannya; dua bentuk konfirmasi dan kapan
    masing-masing muncul; menyalin cadangan ke server lain; langkah
    berikutnya di panel. Blok SQL dengan tangan dihapus.
  - "Memberi hak SHOW DATABASES di server yang dikarantina": ditulis ulang
    — `grant-admin`, dan `reset-admin` untuk nama akun yang tidak
    didukung; bila diminta kata sandi root MySQL. Blok SQL dihapus. Judul
    bagian boleh berubah; tidak ada lagi teks agent yang mengutipnya.
  - Bagian **Master lama**: "Setelah bangun ulang gagal" mengikuti aturan
    baru (dikarantina lagi, apa yang tersedia, Periksa tidak menghapus
    Ulangi); tidak ada lagi "Batalkan percobaan bangun ulang"; promote
    kedua (T4–T6).
  - Bagian peringatan: `master_start_late` kini juga untuk "selagi
    berjalan"; penjaga mengunci lagi dalam 5 detik; membuka master lama
    dengan benar tetap `set-role`.
  - Bagian keamanan/mode ketat: kalimat tentang kapan karantina berakhir
    (daftar master baru; `read_only` selama muat menurut M1).
  - SEC-233 (m-I1…m-I4) pada "Melepas kunci start dengan tangan" dan
    bagian yang tersisa.
- [ ] **Step 3:** Isi "Teks baru untuk dibaca pemilik": setiap teks baru
  atau berubah (T1–T9, teks Task 8 dan 9, layar ketiga perintah CLI
  disalin dari keluaran sungguhan), dengan tempat tampilnya.
- [ ] **Step 4:** Semua gerbang termasuk `make test-e2e`; commit
  `docs: README and owner texts for plan 5b-2a; e2e for the old-master rule`.

---

## Catatan untuk pelaksana

- Urutan: 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8 → 9 → 10 → 11 → 12 → 13 → 14 →
  15. Task 3–10 hanya agent dan proto; Task 11–14 hanya panel (Task 12
  memakai field proto Task 5). Task 9 mengubah teks agent yang dipakai
  test panel Task 14.
- Tidak ada task yang mengubah `internal/panel/monitor` selain Task 12,
  dan tidak ada yang mengubah aturan mode ketat (`strict.go`, tabel peran)
  selain yang disebut Task 10.
- Bila sebuah task menemukan bahwa keputusan spek tidak bisa bekerja
  seperti tertulis: berhenti, laporkan; jangan menambal.
- Sesudah Task 15: tinjauan akhir seluruh cabang seperti biasa. Audit
  keamanan menyeluruh dengan model terkuat dilakukan di akhir Rencana
  5b-2b, mencakup hasil rencana ini.

## Bukti pengukuran

Diukur 2026-11-16 pada **MySQL 8.0.46-0ubuntu0.24.04.4** (paket Ubuntu
24.04 `mysql-server-core-8.0`, `mysql-client-core-8.0`), dua mysqld coba
yang disusun persis seperti `testmysql.Start`: `--initialize-insecure`,
direktori `/var/tmp/dbpanel-m-<n>` milik `nobody:nogroup`, `user=nobody`,
`bind-address=127.0.0.1`, port sendiri, `log-bin=binlog`, `gtid-mode=ON`,
`enforce-gtid-consistency=ON`, dijalankan `mysqld --defaults-file=<my.cnf>
--daemonize`. Tidak ada supervisor (seperti harness). Server **T** (tujuan,
peran "master lama") dan server **S** (sumber/master baru). Kedua instance
dihentikan dan direktorinya dihapus sesudahnya. Bukan MySQL mesin ini.

Akun di T (semua `'…'@'%'`, `WITH GRANT OPTION`, tanpa SUPER):

| Akun | Hak |
|---|---|
| **A** `dbpanel_adm` | persis `staticGrants` + `dynamicGrants` + `FLUSH_TABLES` (`requiredGrants`, 8.0.46 ≥ 8.0.31): `CREATE USER, RELOAD, PROCESS, SELECT, LOCK TABLES, SHOW VIEW, TRIGGER, EVENT, REPLICATION CLIENT, REPLICATION SLAVE` + `SYSTEM_VARIABLES_ADMIN, REPLICATION_SLAVE_ADMIN, BACKUP_ADMIN, CLONE_ADMIN, PERSIST_RO_VARIABLES_ADMIN, CONNECTION_ADMIN, FLUSH_TABLES` |
| **A-r** `dbpanel_admr` | A + `replicaDataGrants` (`RequiredReplicaGrants`): `DROP, CREATE, INSERT, UPDATE, DELETE, ALTER, INDEX, CREATE VIEW, CREATE ROUTINE, ALTER ROUTINE, REFERENCES, CREATE TEMPORARY TABLES` |
| **A-d** `dbpanel_admd` | A-r + `SET_USER_ID, SYSTEM_USER` (yang disarankan `loadFailure` untuk DEFINER akun lain) |
| **B** `appuser` | `ALL PRIVILEGES ON localold.*` + `CREATE ON *.*`; tanpa `CONNECTION_ADMIN` |

Setiap pernyataan dijalankan lewat klien `mysql --defaults-file=<berkas>`
(TCP 127.0.0.1), satu sesi per baris tabel. "Kontrol" = pernyataan yang sama
dengan bendera lain, untuk memisahkan penolakan karena baca-saja dari
penolakan karena hak.

### M1a. Bendera itu sendiri (akun A)

| Keadaan awal (`read_only`/`super_read_only`) | Pernyataan | Hasil | Sesudahnya |
|---|---|---|---|
| ON/ON | `SET GLOBAL read_only=OFF` | berhasil, tidak ada galat | **OFF/OFF** (ikut mematikan `super_read_only`) |
| OFF/OFF | `SET GLOBAL super_read_only=ON` | berhasil | **ON/ON** (ikut menyalakan `read_only`) |
| ON/ON | `SET GLOBAL super_read_only=OFF` | berhasil | ON/OFF |
| OFF/OFF | `SET GLOBAL read_only=ON` | berhasil | **ON/OFF** (`super_read_only` tidak berubah) |
| ON/ON | `SET GLOBAL read_only=ON` | berhasil | ON/ON (tidak berubah) |
| OFF/OFF | `SET GLOBAL read_only=OFF; SET GLOBAL super_read_only=ON` | berhasil | ON/ON |

### M1b. Akun B dengan `read_only=ON`, `super_read_only=OFF`

| Pernyataan | Hasil |
|---|---|
| `INSERT INTO localold.t VALUES (2)` | ditolak: `ERROR 1290 (HY000): The MySQL server is running with the --read-only option so it cannot execute this statement` |
| `CREATE DATABASE appnew` | ditolak: `ERROR 1290 (HY000)`, teks sama |
| `CREATE TABLE localold.t2 (id INT)` | ditolak: `ERROR 1290 (HY000)`, teks sama |
| `SELECT COUNT(*) FROM localold.t` | berhasil (`1`) |

Pembanding: `INSERT` dan `CREATE DATABASE` yang sama oleh A-r berhasil;
oleh A ditolak karena hak (`ERROR 1142 (42000): INSERT command denied to
user 'dbpanel_adm'@'localhost' for table 't'`, `ERROR 1044 (42000): Access
denied for user 'dbpanel_adm'@'%' to database 'appnew'`), bukan karena
baca-saja.

### M1c. Pernyataan setiap pemakai `liftReadOnly` dengan `read_only=ON`, `super_read_only=OFF`

| Pemakai | Pernyataan (persis seperti kode) | Akun | `read_only=ON`, `super_read_only=OFF` | Kontrol |
|---|---|---|---|---|
| `dumpFrom` | `RESET MASTER` (`resetBinaryLogs`, 8.0) | A | berhasil; `gtid_executed` kosong | `super_read_only=ON`: juga berhasil |
| `dumpFrom` | muat `mysqldump --all-databases` (lihat bawah) dengan `mysql --defaults-file=<f> --binary-mode --init-command=SET sql_log_bin=0`, diakhiri `FLUSH PRIVILEGES;` (`flushTrailer`) | A-d | **berhasil, exit 0**: `app` (tabel, view `vw`, procedure `p`, trigger `tr`, event `ev`), `` `with space` ``, skema `mysql` termuat; `gtid_executed` = `gtid_purged` = `…:1-18` dari `SET @@GLOBAL.GTID_PURGED` dump (tidak ada GTID lokal); bendera tetap ON/OFF; sesudah `FLUSH PRIVILEGES` akun A-d tidak ada lagi (`ERROR 1045`, jalur `AdminReplaced`) | `super_read_only=ON`: `SET @@GLOBAL.GTID_PURGED` (baris 26) lolos, lalu `ERROR 1290 (HY000) at line 32: The MySQL server is running with the --super-read-only option so it cannot execute this statement` (`CREATE DATABASE … mysql`) |
| `dumpFrom` | muat yang sama | A-r | gagal karena hak: `ERROR 1419 (HY000) at line 1084: You do not have the SUPER privilege and binary logging is enabled (you *might* want to use the less safe log_bin_trust_function_creators variable)` (trigger dengan `DEFINER=root@localhost`) | `read_only=OFF`: galat sama persis, baris sama |
| `dumpFrom` | muat yang sama | A-r + `SET_USER_ID` saja | gagal karena hak: `ERROR 1227 (42000) at line 1084: Access denied; you need (at least one of) the SYSTEM_USER privilege(s) for this operation` | — |
| `dumpFrom` | muat yang sama | A | gagal karena hak: `ERROR 1044 (42000) at line 32: Access denied for user 'dbpanel_adm'@'%' to database 'mysql'` | — |
| `cloneFrom` | `CLONE INSTANCE FROM 'dbpanel_repl'@'127.0.0.1':<port S> IDENTIFIED BY '…'` (sesudah `SET GLOBAL clone_valid_donor_list`, `clone_ssl_ca = ''`; donor S dengan akun `REPLICATION SLAVE, BACKUP_ADMIN`, plugin clone terpasang di keduanya) | A | **fase data selesai**: `ERROR 3707 (HY000): Restart server failed (mysqld is not managed by supervisor process).` (hasil yang `isCloneRestart` anggap selesai); log: `Clone removing all user data for provisioning: Finished`; sesudah mysqld dinyalakan lagi: `performance_schema.clone_status` = `Completed 0`, database = milik S, `gtid_executed` = milik S | `super_read_only=ON`: `ERROR 1290 (HY000): The MySQL server is running with the --super-read-only option so it cannot execute this statement`; `clone_status` = `Failed 1290` |
| `installClonePlugin` | `INSTALL PLUGIN clone SONAME 'mysql_clone.so'` | A-r | **berhasil**; `PLUGIN_STATUS` = `ACTIVE`; kedua kali `ERROR 1125 (HY000): Function 'clone' already exists` (`errUDFExists`, yang memang diterima kode) | `super_read_only=ON`: `ERROR 1290 (HY000) … --super-read-only …` |
| `installClonePlugin` | sama | A | gagal karena hak: `ERROR 1142 (42000): INSERT command denied to user 'dbpanel_adm'@'localhost' for table 'plugin'` | `read_only=OFF`: galat sama |
| `DropDatabases` | ``SET SESSION sql_log_bin = 0; DROP DATABASE IF EXISTS `dropme` `` | A-r | **berhasil**; database hilang; `gtid_executed` tidak bertambah | `super_read_only=ON`: `ERROR 1290 (HY000) … --super-read-only …` |
| `DropDatabases` | sama | A | gagal karena hak: `ERROR 1044 (42000): Access denied for user 'dbpanel_adm'@'%' to database 'dropme'` | `read_only=OFF`: galat 1044 sama |
| `createAccount` (mode lokal), akun baru | `SET SESSION sql_log_bin = 0; CREATE USER IF NOT EXISTS 'dbpanel_repl'@'%' IDENTIFIED BY '…'; ALTER USER 'dbpanel_repl'@'%' REQUIRE NONE; GRANT REPLICATION SLAVE, BACKUP_ADMIN ON *.* TO 'dbpanel_repl'@'%'` | A | **berhasil**; `gtid_executed` tidak bertambah | `super_read_only=ON`: `CREATE USER` dan `GRANT` masing-masing `ERROR 1290 (HY000) … --super-read-only …` |
| `createAccount` (mode lokal), akun sudah ada | `SET SESSION sql_log_bin = 0; ALTER USER … IDENTIFIED BY '…'; ALTER USER … REQUIRE NONE; REVOKE ALL PRIVILEGES, GRANT OPTION FROM …; GRANT REPLICATION SLAVE, BACKUP_ADMIN ON *.* TO …` | A | **berhasil** | — |
| (bentuk akun dump) | `CREATE USER IF NOT EXISTS …; ALTER USER … REQUIRE NONE WITH MAX_USER_CONNECTIONS 2 PASSWORD EXPIRE INTERVAL 1 DAY; GRANT SELECT, FLUSH_TABLES, LOCK TABLES, SHOW VIEW, TRIGGER, EVENT ON *.* TO …; DROP USER IF EXISTS …` | A | **berhasil** | — |

Dump yang dimuat: dari S (GTID ON, `gtid_executed …:1-18`) dengan akun
`mysql.createDumpUser` (`SELECT, FLUSH_TABLES, LOCK TABLES, SHOW VIEW,
TRIGGER, EVENT`, `MAX_USER_CONNECTIONS 2`, `PASSWORD EXPIRE INTERVAL 1 DAY`)
dan argumen `dumpArgs`: `mysqldump --defaults-file=<f> --single-transaction
--set-gtid-purged=ON --triggers --routines --events --skip-lock-tables
--column-statistics=0 --no-tablespaces --all-databases` (exit 0, 1 293 243
byte). Kepalanya memuat `SET @@SESSION.SQL_LOG_BIN= 0;` dan `SET
@@GLOBAL.GTID_PURGED=/*!80000 '+'*/ '…:1-18';`. Urutan seperti `dumpFrom`:
`RESET MASTER`, lalu muat.

Catatan di luar baca-saja (bukan keputusan Task 2, untuk pengendali):
akun A persis (hak wajib `mysql.connect`) tidak bisa menjalankan
`DROP DATABASE`, `INSTALL PLUGIN` maupun muat dump di server mana pun —
hak datanya tidak ada. Muat `--all-databases` dari master yang punya
trigger/routine ber-`DEFINER=root@localhost` butuh `SET_USER_ID` **dan**
`SYSTEM_USER`; teks `loadFailure` hanya menyebut `SET_USER_ID`. Keduanya
sama dengan `read_only` mati; tidak diubah oleh rencana ini.

Catatan kontrol CLONE dengan `super_read_only=ON`: sebelum galat 1290,
log galat T mencatat
`2026-11-16T01:01:06.526001Z 14 [Warning] [MY-013460] [InnoDB] Clone removing all user data for provisioning: Started`
(utas 14, sesi kontrol itu), dan tidak ada baris `Finished` untuk utas
14; baris `Started`/`Finished` berikutnya milik utas 17, yaitu CLONE
dengan `super_read_only=OFF`. Apakah data pengguna T masih utuh sesudah
kontrol itu tidak diperiksa (tidak dicatat): CLONE berikutnya dijalankan
tanpa membaca daftar database atau isi tabel lebih dulu, dan menimpa
semuanya. Tidak diselidiki lebih jauh.

### M2. Daftar database dengan akun dump (server S)

Akun dibuat dengan pernyataan `createAccount` untuk `mysql.createDumpUser`
(8.0.46: `dumpGrants`); `SHOW GRANTS`:
``GRANT SELECT, LOCK TABLES, SHOW VIEW, EVENT, TRIGGER ON *.* TO `dbpanel_dump_m2`@`%` ``
dan ``GRANT FLUSH_TABLES ON *.* TO `dbpanel_dump_m2`@`%` ``. Database di S: `app`, `with space`,
`` back`tick ``, `rumah_sakit_é`, `数据库` (dibuat dengan
`--default-character-set=utf8mb4`), `rahasia` (berisi tabel) dan `kosong`;
tidak satu pun diberi hak khusus untuk akun dump.

Perintah: `mysql --defaults-file=<f> -N -B -e "SELECT HEX(schema_name) FROM
information_schema.schemata"` (berkas `[client]` user/password/host/port/
`get-server-public-key`, seperti `clientDefaults`). Exit 0, stderr kosong.
Keluaran sama persis dengan `LANG=C` dan `LANG=C.UTF-8`:

| Baris keluaran (persis) | Nama |
|---|---|
| `6D7973716C` | `mysql` |
| `696E666F726D6174696F6E5F736368656D61` | `information_schema` |
| `706572666F726D616E63655F736368656D61` | `performance_schema` |
| `737973` | `sys` |
| `617070` | `app` |
| `77697468207370616365` | `with space` |
| `6261636B607469636B` | `` back`tick `` |
| `72616861736961` | `rahasia` |
| `6B6F736F6E67` | `kosong` |
| `72756D61685F73616B69745FC3A9` | `rumah_sakit_é` |
| `E695B0E68DAEE5BA93` | `数据库` |

Bentuk: satu nama per baris, heksadesimal **huruf besar**, tiap baris
diakhiri `\n` (termasuk yang terakhir), tanpa judul kolom, tanpa urutan
abjad (urutan server), skema sistem ikut. Hex adalah byte UTF-8 nama
(kolom `schema_name` utf8mb3), tidak bergantung pada charset klien. Akun
dump melihat **semua** 11 database, sama dengan root (`COUNT(*)` = 11),
karena `SELECT ON *.*`.

### M3. `mysqlbinlog` dan indeks sebagai uid pemilik direktori data

Direktori data S: `drwxr-x--- nobody:nogroup`; `binlog.000001` dan
`binlog.index` `-rw-r----- nobody:nogroup`. Dijalankan dengan `setpriv
--reuid=<u> --regid=<g> --clear-groups` (sama dengan
`SysProcAttr.Credential`):

| Uid | Perintah | Hasil |
|---|---|---|
| `nobody` (pemilik) | `mysqlbinlog --no-defaults /var/tmp/…/data/binlog.000001` | exit 0, 629 baris, 43 `GTID_NEXT`; juga dengan `--reset-env` dan `HOME=/nonexistent` |
| `nobody` | `cat /var/tmp/…/data/binlog.index` | exit 0: `./binlog.000001` (jalur **relatif** terhadap direktori data) |
| `daemon` (bukan pemilik) | `mysqlbinlog --no-defaults …/binlog.000001` | exit 1: `mysqlbinlog: File '…/binlog.000001' not found (OS errno 13 - Permission denied)` lalu `ERROR: Could not open log file` |
| `daemon` | `cat …/binlog.index` | exit 1: `Permission denied` |

AppArmor (Debian/Ubuntu): paket `mysql-server-8.0_8.0.46-0ubuntu0.24.04.5_amd64.deb`
(arsip Ubuntu noble, sha256 `2c304181…5669`) hanya membawa
`/etc/apparmor.d/usr.sbin.mysqld`, profil untuk `/usr/sbin/mysqld` saja;
`mysql-client-core-8.0` tidak membawa profil. Tidak ada profil untuk
`/usr/bin/mysqlbinlog`, jadi ia berjalan tanpa kurungan dan hanya izin
berkas biasa yang berlaku. `postinst` paket itu memasang `/var/lib/mysql`
`mysql:mysql` mode `0700` dan `/var/log/mysql` `mysql:adm` `0750`
(`error.log` `0640`): uid `mysql` bisa membaca binlog, indeks dan log
galat bawaan. Paket Debian sendiri (bukan Ubuntu) dan paket Oracle
(`mysql-community-*`) tidak diperiksa.

SELinux (RHEL): mesin ini tidak punya SELinux; yang dibaca hanya sumber
kebijakan. `fedora-selinux/selinux-policy` (commit `5df11316`) kini hanya
membawa `mysql.if`; modul `mysql` ada di `devexp-db/mysql-selinux`
(commit `1014b7fb`), yang `mysql.fc`-nya tidak memberi label pada
`mysqlbinlog` (hanya `mysqld`, `mysqld_safe`, `mysql_upgrade`, dll.), jadi
`mysqlbinlog` (`bin_t`) berjalan di domain pemanggil tanpa transisi;
`/var/lib/mysql` berlabel `mysqld_db_t`, `/var/log/mysql` `mysqld_log_t`.
Domain SELinux tidak berubah karena ganti uid, jadi menjalankan sebagai
uid `mysql` tidak menambah atau mengurangi izin SELinux dibanding root.
**Tidak diverifikasi:** RPM RHEL yang sebenarnya (versi `mysql-selinux`
yang dikirim, mode `/var/lib/mysql` dari RPM `mysql-server`), dan domain
tempat agent berjalan di RHEL.

### M4. `SHOW GRANTS FOR` di bawah pengaturan sesi

Akun `'dbpanel_adm'@'%'` di S dengan `partial_revokes=ON`: hak global,
hak dinamis, hak database, partial revoke, hak kolom, dan peran.

Bawaan (`sql_quote_show_create=1`, `sql_mode` bawaan 8.0), juga sama
persis dengan `sql_mode=''`:

```
GRANT SELECT, RELOAD, SHOW DATABASES ON *.* TO `dbpanel_adm`@`%` WITH GRANT OPTION
GRANT CONNECTION_ADMIN,SYSTEM_VARIABLES_ADMIN ON *.* TO `dbpanel_adm`@`%`
GRANT INSERT ON `app`.* TO `dbpanel_adm`@`%`
REVOKE SELECT ON `rahasia`.* FROM `dbpanel_adm`@`%`
GRANT SELECT (`id`) ON `app`.`t` TO `dbpanel_adm`@`%`
GRANT `dbpanel_role`@`%` TO `dbpanel_adm`@`%`
```

`SET SESSION sql_quote_show_create=OFF`:

```
GRANT SELECT, RELOAD, SHOW DATABASES ON *.* TO dbpanel_adm@`%` WITH GRANT OPTION
GRANT CONNECTION_ADMIN,SYSTEM_VARIABLES_ADMIN ON *.* TO dbpanel_adm@`%`
GRANT INSERT ON app.* TO dbpanel_adm@`%`
REVOKE SELECT ON rahasia.* FROM dbpanel_adm@`%`
GRANT SELECT (id) ON app.t TO dbpanel_adm@`%`
GRANT dbpanel_role@`%` TO dbpanel_adm@`%`
```

`SET SESSION sql_mode='ANSI_QUOTES'` (juga `sql_mode='ANSI'` dan bawaan +
`,ANSI_QUOTES`):

```
GRANT SELECT, RELOAD, SHOW DATABASES ON *.* TO "dbpanel_adm"@"%" WITH GRANT OPTION
GRANT CONNECTION_ADMIN,SYSTEM_VARIABLES_ADMIN ON *.* TO "dbpanel_adm"@"%"
GRANT INSERT ON "app".* TO "dbpanel_adm"@"%"
REVOKE SELECT ON "rahasia".* FROM "dbpanel_adm"@"%"
GRANT SELECT ("id") ON "app"."t" TO "dbpanel_adm"@"%"
GRANT "dbpanel_role"@"%" TO "dbpanel_adm"@"%"
```

Keduanya (`sql_quote_show_create=OFF`, `sql_mode='ANSI_QUOTES'`): seperti
OFF, tetapi yang masih dikutip memakai `"`: `GRANT SELECT, RELOAD, SHOW DATABASES ON *.* TO dbpanel_adm@"%" WITH GRANT OPTION`.

Dengan OFF, hanya nama yang tidak perlu dikutip yang dilepas kutipnya
(diukur per akun):

| Akun | Baris dengan `sql_quote_show_create=OFF` |
|---|---|
| `'dbpanel_x'@'localhost'` | `GRANT SELECT ON *.* TO dbpanel_x@localhost` |
| `'dbpanel_x'@'127.0.0.1'` | ``GRANT SELECT ON *.* TO dbpanel_x@`127.0.0.1` `` |
| `'dbpanel_x'@'10.0.%'` | ``GRANT SELECT ON *.* TO dbpanel_x@`10.0.%` `` |
| `'select'@'localhost'` | ``GRANT SELECT ON *.* TO `select`@localhost `` |
| `'dbpanel-y'@'localhost'` | ``GRANT SELECT ON *.* TO `dbpanel-y`@localhost `` |

Nilai global diwarisi sesi baru: sesudah `SET GLOBAL
sql_quote_show_create=OFF`, sesi baru membaca `@@SESSION.sql_quote_show_create
= 0` dan baris tanpa kutip; sesudah `SET GLOBAL sql_mode='ANSI_QUOTES'`,
sesi baru memakai `ANSI_QUOTES`. Pada sesi baru seperti itu, `SET SESSION
sql_quote_show_create = ON, sql_mode = ''` mengembalikan bentuk backtick
bawaan persis (diukur, juga untuk keenam baris di atas). Pernyataan itu
tidak butuh hak apa pun: akun tanpa hak global (`appuser_src`) menjalankannya
dengan berhasil.

### Keputusan

Menurut aturan keputusan Task 2 (`both = false` untuk yang berhasil pada
M1 dengan `read_only` menyala):

- `dumpFrom` (muat dump): **`both = false`**. Muat `--all-databases`
  lengkap, `RESET MASTER`, `SET @@GLOBAL.GTID_PURGED` dan `FLUSH
  PRIVILEGES` berhasil dengan `read_only=ON`, `super_read_only=OFF`. Butir
  pertama 5.3 tetap berlaku untuk muat dump.
- `cloneFrom` (CLONE): **`both = false`**. Fase data CLONE selesai dengan
  `read_only=ON`, `super_read_only=OFF` (hanya `super_read_only` yang
  menolak, 1290). Pengecualian "penjaga tidak menyentuh apa pun" di 5.2
  tidak dibutuhkan.
- `installClonePlugin`: **`both = false`**.
- `DropDatabases`: **`both = false`**.
- `createAccount` (mode lokal): **`both = false`**.
- Akibat M1a untuk Task 3: mematikan hanya `super_read_only` memakai
  `SET GLOBAL super_read_only = OFF` (meninggalkan `read_only=ON`); jangan
  pernah mengirim `SET GLOBAL read_only = OFF`, karena itu ikut mematikan
  `super_read_only` tanpa galat. Sebaliknya `SET GLOBAL super_read_only =
  ON` ikut menyalakan `read_only`, sedangkan `SET GLOBAL read_only = ON`
  tidak menyentuh `super_read_only`.
- Task 7: **dibangun.** `mysqlbinlog --no-defaults` dan pembacaan indeks
  berhasil sebagai uid pemilik direktori data; uid lain ditolak `errno 13`.
  Pada pemasangan Ubuntu bawaan tidak ada profil AppArmor yang mengurung
  `mysqlbinlog`; pada RHEL, menurut sumber kebijakan, SELinux tidak
  bergantung pada uid (tidak diverifikasi di mesin RHEL). Entri indeks
  bisa relatif (`./binlog.000001`) terhadap direktori data. Jalan balik ke
  root dengan satu baris peringatan (5.5) tetap dibangun.
- Task 10: sesi bukti daftar lengkap menjalankan, pada sambungan yang sama
  dan sebelum `SHOW GRANTS FOR`, tepat satu pernyataan:
  `SET SESSION sql_quote_show_create = ON, sql_mode = ''`. Sesudahnya
  setiap nama di baris `SHOW GRANTS` dikutip backtick, termasuk akun dan
  host (bentuk di atas).

## Perubahan sesudah tinjauan rancangan

(Diisi Task 1.)

## Teks baru untuk dibaca pemilik

(Diisi Task 15; teks T1–T9 ada di spek 5.8 dan di task-nya.)
