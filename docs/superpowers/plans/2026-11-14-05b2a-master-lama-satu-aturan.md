# Rencana Implementasi 5b-2a: Master Lama dengan Satu Aturan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Master lama yang dikarantina punya satu arti di panel dan di
agent pada setiap keadaan; penjaga menguncinya lagi dalam satu putaran
bila terbuka tanpa sebab yang sah, atau melapor bila tidak bisa; karantina
hanya berakhir bila setiap database memang disalin dari master baru; dan
pekerjaan berbahaya di server dilakukan tiga perintah `dbpanel agent`,
bukan SQL yang disalin operator.

**Architecture:** Di agent, setiap pembukaan baca-saja yang sah (pekerjaan
agent atau perintah CLI) memegang satu `flock` atas berkas di direktori
milik root, dan hanya mematikan `super_read_only`; penjaga membaca bendera
tiap putaran dan menulis atau melapor hanya selagi ia sendiri memegang
kunci berkas itu dan tidak ada tanda muat, lalu mencatat kejadiannya
sebagai hasil start `late` dengan keterangan (`running` atau
`unverified`). Muat dump penuh memasang `super_read_only` lagi di aliran
muatnya sendiri, dan catatan muat menyimpan daftar database master baru
yang dibaca klien `mysql`. Tiga subperintah CLI baru masuk ke MySQL
sebagai root. Di panel, peran master lama tetap `quarantined` dari promote
sampai agent mengakhiri karantina (settle), dan settle memasang peran
`replica`, bendera dan menghapus tanda dalam satu transaksi; monitor
membaca peran sebelum baris; tindakan "Batalkan percobaan bangun ulang"
dihapus; teks agent di pekerjaan selalu berlabel, dengan asal yang
ditentukan dari tipe galat hub.

**Tech Stack:** Go 1.26, `CGO_ENABLED=0`, tanpa dependensi baru; SQLite
(modernc); html/template + Tailwind v4/daisyUI v5 (`bash
scripts/tailwind.sh`); harness `internal/agent/drivers/mysql/testmysql`
(mysqld sungguhan, skip bila tidak terpasang); e2e
`internal/panel/e2e_*_test.go`.

**Spec:** `docs/superpowers/specs/2026-11-14-rencana-5b-2a-master-lama-satu-aturan-design.md`
— seluruhnya; bagian 10 (perubahan sesudah tinjauan rancangan dan
pengukuran putaran kedua) mengikat dan mengalahkan bagian 9 di tempat
yang disebutnya. Kode diperiksa pada `main` `8ce7fc5`. Fakta kode yang
dipakai rencana ini (tanda tangan fungsi, nomor baris) berasal dari
pembacaan pada commit itu; pelaksana memeriksa ulang letaknya sebelum
mengubah.

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
  baru dipakai persis seperti di task; setiap teks ber-id (T…, C…, P…)
  sama huruf demi huruf dengan spek 5.8 (P… : blok bertanda di spek
  bagian 7).
- Tidak ada perintah protokol baru dan tidak ada jenis peringatan baru.
  Field protokol baru semuanya `omitempty`.
- Penyelamatan (Periksa dan Amankan data tertinggal) tidak pernah ditolak
  oleh aturan baru kecuali keadaan salinan `CopyStarted` yang sudah ada.
- Pembukaan baca-saja hanya pernah mengirim `SET GLOBAL super_read_only =
  OFF` dan memulihkan dengan `SET GLOBAL super_read_only = ON`. Tidak ada
  kode yang mengirim `SET GLOBAL read_only = OFF` (bukti M1a: itu
  mematikan keduanya tanpa galat).
- Nilai tetap:
  - putaran penjaga 5 detik (`GuardEvery`, tidak diubah);
  - berkas kunci `/var/lib/dbpanel/agent/lift.lock`, mode `0600`, di
    direktori `/var/lib/dbpanel/agent` mode `0700`, keduanya milik uid
    proses (root);
  - pekerjaan agent menunggu kunci berkas paling lama **10 detik**;
    perintah CLI mengulang percobaan tanpa-menunggu paling lama **3
    detik**;
  - sambungan yang disimpan sebuah pembukaan: `SET SESSION wait_timeout =
    28800`;
  - pernyataan penjaga: `SET SESSION lock_wait_timeout = 10` pada
    sambungannya;
  - laporan "tidak bisa memeriksa": sesudah **3 putaran** berturut-turut,
    diulang paling sering sekali per **10 menit**; kejadian "dikunci lagi"
    diantrekan paling sering sekali per **60 detik** (setiap kejadian
    tetap dicatat);
  - daftar database master baru sesudah dump dicoba **3 kali**;
  - perintah CLI: `SET SESSION lock_wait_timeout = 60`; jendela terbuka
    paling lama **10 menit**;
  - ekor log galat **64 KiB**, batas waktu **5 detik**;
  - folder cadangan bawaan `/var/lib/dbpanel/dibuang`, mode `0700`,
    berkas `0600`.
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
   `drop-database` dan di daftar master baru: nama dibandingkan tanpa beda
   huruf besar-kecil dan tidak pernah lewat shell; nama bertanda kutip
   tidak bisa dicadangkan `mysqldump` dan harus berakhir di teks C8, bukan
   di galat mentah. Test: Task 6 (`TestSourceDatabasesOddNames`), Task 8
   (`TestDropDatabaseOddNames`).
2. **MySQL yang tidak dipasang dbpanel** (root berkata sandi, akun
   administrator bukan `root`, socket di tempat lain, pemilik berkas bukan
   akun `mysql`): perintah CLI meminta kata sandi; uid program adalah
   pemilik direktori yang dibaca. Test: Task 8
   (`TestRootSessionPasswordFallback`, `TestRootSessionOtherUser`), Task 7
   (`TestServiceCredentialIsDirectoryOwner`).
3. **Proses mati di tengah:** agent mati saat memegang kunci berkas; CLI
   dimatikan paksa di antara membuka dan mengunci; panel berhenti di
   antara "job gagal" dan "baris gagal". Test: Task 3
   (`TestLiftLockReleasedWhenHolderDies`), Task 8
   (`TestDropDatabaseKilledMidway`), Task 11
   (`TestResumeFailsRebuildRowBeforeJob`).
4. **Data dari sebelum rencana ini:** baris peran `replica` + gagal + tanda
   master lama; job tanpa bendera; `leftover-gtid` tanpa cap sumber; agent
   lama dengan panel baru dan sebaliknya. Test: Task 11
   (`TestNormalizeLegacyFailedRebuilds`), Task 10
   (`TestLeftoverSetWithoutSourceIsNoRecord`), Task 5
   (`TestRelockReportReadableByOldPanelShape`), Task 14
   (`TestQuotesKeepsOldAgentPhrases`).
5. **Hal yang lama atau besar:** `DROP DATABASE` yang menunggu sesi lain
   sementara penjaga berputar; cadangan lebih besar dari ruang kosong;
   bangun ulang berjam-jam sementara operator menjalankan perintah CLI;
   pernyataan penjaga di belakang kunci tabel. Test: Task 4
   (`TestGuardLeavesLongLiftAlone`, `TestGuardBoundedBehindTableLock`),
   Task 8 (`TestDropDatabaseNoSpace`, `TestCLIRefusesWhileLoadHoldsLock`,
   `TestDropDatabaseBlockedByOpenTransaction`).

---

## File Structure

| Berkas | Tanggung jawab |
|---|---|
| `internal/agent/drivers/mysql/lift.go` (baru) | `liftGate`: berkas kunci dan pemeriksaannya, hitungan pembukaan, `end`/`abandon`, sambungan yang disimpan; `AcquireLiftLock` untuk CLI |
| `internal/agent/drivers/mysql/dump.go` | `liftReadOnly` lewat `liftGate`; pemeriksaan akun muat; penutup aliran muat; `sourceDatabases`; dua pembacaan daftar di `dumpFrom` |
| `internal/agent/drivers/mysql/clone.go`, `configure.go`, `databases.go`, `users.go` | pemakai `liftReadOnly` yang baru; `cloneFrom` memakai `abandon` |
| `internal/agent/drivers/mysql/quarantine.go` | penjaga membaca bendera tiap putaran dengan satu aturan; laporan "tidak bisa memeriksa"; `writableSince`; penolakan settle |
| `internal/agent/drivers/mysql/startlock_guard.go`, `start_result.go` | catatan dan laporan "dikunci lagi"; ekor log lewat pemilik direktori |
| `internal/agent/drivers/mysql/rebuild_load.go` | `loadCopy.source`; aturan `notCopied`; catatan muat dibuang saat muat baru; sesi bukti; teks baru |
| `internal/agent/drivers/mysql/preflight.go`, `connect.go` | teks pemeriksaan dan penolakan di server yang dikarantina |
| `internal/agent/drivers/mysql/svcuid.go` (baru), `realhost.go` | `serviceCredential`: pemilik direktori; `ErrNotStarted` |
| `internal/agent/drivers/mysql/leftover.go`, `salvage.go`, `binlog.go` | `mysqlbinlog` dan indeks binlog lewat pemilik direktori; cap sumber `leftover-gtid` |
| `internal/agent/drivers/mysql/replication.go` | sumber kanal dibandingkan dengan pin sebelum start |
| `internal/agent/cli.go` | pendaftaran tiga subperintah, teks bantuan |
| `internal/agent/cli_rootsql.go` (baru) | sesi root MySQL (socket, lalu kata sandi tanpa gema); jendela terbuka bersama ketiga perintah |
| `internal/agent/cli_dropdb.go` (baru) | `drop-database` |
| `internal/agent/cli_admin.go` (baru) | `grant-admin`, `reset-admin` |
| `internal/proto/mysql.go` | `QuarantineStartEvent.While`, `StartKindState.While`, dua nilai keterangan |
| `internal/panel/store/topologies.go`, `jobs.go` | `AddReplica`/`RemoveReplica` mempertahankan peran `quarantined`; `SettleRebuild`; hapus `ReturnToQuarantine`; `NewerJobExistsExcept` |
| `internal/panel/orchestrator/runner.go`, `create_replica.go`, `approval.go`, `controls.go`, `resync.go` | aturan satu keadaan: settle satu transaksi, `supersededBy`, jalur gagal, penolakan, hapus, penormalan |
| `internal/panel/orchestrator/promote.go`, `promote_view.go` | promote kedua |
| `internal/panel/orchestrator/agenttext.go` (baru) | `agentTextError`, `fromAgent`, `AgentQuoteN` |
| `internal/panel/hub/hub.go`, `conn_ws.go` | `AgentError`: asal galat dari tipe |
| `internal/panel/monitor/state.go`, `start_state.go`, `promote.go`, `alerts.go`, `monitor.go` | peran dibaca lebih dulu; pesan dan penutupan `master_start_late` |
| `internal/panel/web/handlers_oldmaster.go`, `handlers_mysql.go`, `handlers_servers.go`, `handlers_replication.go`, `handlers_controls.go`, `handlers_jobs.go`, `handlers_alerts.go`, `router.go`, `handlers_confirm.go`, `audit_summary.go`, `render.go`; `internal/panel/notify/message.go` | kotak, form, daftar, dialog Ulangi, label jenis peringatan, label galat; penghapusan Batalkan |
| `internal/panel/redact/redact.go` | frasa dan jangkar mengikuti teks baru, yang lama dipertahankan satu rilis |
| `web/templates/server_detail.html`, `replica_detail.html`, `job_detail.html`, `_ui.html`, `promote.html` | tampilan |
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
  keputusan yang mengikutinya: apakah ada pemakai `liftReadOnly` yang
  butuh `read_only` mati (Task 3); apakah Task 7 dibangun untuk
  `mysqlbinlog`; pernyataan `SET SESSION` untuk Task 10. Commit
  `docs(plan): measurements M1-M4 for plan 5b-2a`.

Hasil (selesai; lihat "Bukti pengukuran"): tidak satu pun pemakai butuh
`read_only` mati, jadi pembukaan hanya mematikan `super_read_only` dan
mode yang mematikan keduanya tidak dibangun (lihat "Perubahan sesudah
tinjauan rancangan", R-4); Task 7 dibangun; Task 10 memakai `SET SESSION
sql_quote_show_create = ON, sql_mode = ''`.

### Task 2b: Pengukuran putaran kedua (X2, X4–X7)

**Files:**
- Create: `docs/superpowers/ledgers/2026-11-14-05b2a-measurements-2.md`

Selesai (MySQL 8.0.46, mysqld coba seperti Task 2); berkas di atas adalah
buktinya dan dibaca utuh oleh task yang disebut di tabel. Diminta
tinjauan rancangan (R-4). X1 sudah diukur Task 2 (M1c). **X3 (MySQL 8.4)
tidak diukur**: 8.4 tidak terpasang di lingkungan ini.

| Baris | Yang diukur | Hasil yang mengikat | Dibaca oleh |
|---|---|---|---|
| X2 | muat penuh oleh akun tanpa `CONNECTION_ADMIN` | tanpa `CONNECTION_ADMIN` dan tanpa SUPER: ERROR 1290 pada baris tulis pertama dump, **sesudah** `RESET MASTER` dan `SET @@GLOBAL.GTID_PURGED`; SUPER saja cukup | Task 3: pemeriksaan akun muat di `dumpFrom`, kapan pun `read_only` menyala |
| X4 | pernyataan penjaga terhadap server yang sibuk | menunggu di belakang `LOCK TABLES … WRITE` dan tulisan berjalan (juga sesi berhak), tidak menunggu transaksi terbuka; pembatalan konteks klien tidak menghentikannya di server (berlaku belakangan, bisa setengah); `SET SESSION lock_wait_timeout = <n>` → galat 1205, bendera tidak berubah | Task 4: penjaga menyetel `lock_wait_timeout = 10`; 1205 = "tidak bisa mengunci" |
| X5 | bentuk keluaran `mysqldump --databases <nama>` | bukti kelengkapan hanya exit 0 dan baris terakhir berawalan `-- Dump completed`; baris berawalan `CREATE TABLE ` di luar blok `DELIMITER ;;` … `DELIMITER ;` paling sedikit sebanyak tabel dasar (bisa lebih); nama ber-`'` atau ber-`"` tidak bisa di-dump (exit 2); baris `CREATE DATABASE` berawalan ``CREATE DATABASE /*!32312 IF NOT EXISTS*/ `<nama, backtick digandakan>` `` | Task 8: pemeriksaan cadangan |
| X6 | restart sesudah CLONE | CLONE tidak menyentuh `mysqld-auto.cnf` penerima; penerima menyala dengan nilai tersimpannya sendiri (ON/ON di server yang dikarantina). Tidak diukur: bendera yang hanya `SET GLOBAL`; restart oleh systemd sungguhan | Task 3: `abandon`; Task 4: restart CLONE |
| X7 | sambungan yang dipegang melewati muat penuh (`AdminReplaced`) | sesudah `FLUSH PRIVILEGES` mengganti akun: sambungan yang disimpan → 1227 pada `SET GLOBAL super_read_only = ON`, sambungan baru → 1045; pemulihan di aliran muat sebelum `FLUSH PRIVILEGES` berhasil (exit 0, ON/ON); sambungan menganggur mati dengan 4031 kecuali `SET SESSION wait_timeout = 28800` | Task 3: penutup aliran muat, `end` yang membaca dulu, sambungan yang disimpan |

- [ ] **Step 1:** Ukur X2, X4, X5, X6, X7 dan tulis berkas bukti; catat
  yang tidak diukur. Commit
  `docs: second measurement round for plan 5b-2a (X2, X4-X7)`.

### Task 3: Kunci berkas, hitungan pembukaan, dan pemulihan

**Files:**
- Create: `internal/agent/drivers/mysql/lift.go`, `lift_test.go`
- Modify: `dump.go` (`liftReadOnly`, `dumpFrom`, penutup aliran muat),
  `clone.go`, `configure.go` (`installClonePlugin`), `databases.go`
  (`DropDatabases`), `users.go` (`createAccount`), `driver.go`
- Test: `lift_test.go`, `snapshot_test.go`

**Interfaces:**
- Produces:
  - `var DefaultLiftLock = "/var/lib/dbpanel/agent/lift.lock"`; field
    `Driver.LiftLock string` (kosong = bawaan).
  - `func openLiftLock(path string, replace bool) (*os.File, error)`:
    pembuka bersama gerbang, penjaga dan CLI. Membuat direktori (`0700`)
    dan berkas (`O_CREAT|O_EXCL`, `0600`) bila belum ada; membuka dengan
    `O_NOFOLLOW|O_CLOEXEC`; memeriksa direktori (direktori, milik uid
    proses, tanpa bit grup/lain) dan deskriptor berkas (berkas biasa,
    milik uid proses, tanpa bit grup/lain, jumlah nama 1). Berkas yang
    tidak lulus: `replace == true` (agent) → berkas baru ditulis di
    sampingnya dan di-`rename` menggantikannya; `replace == false` (CLI)
    → `*LiftLockError`.
  - `type LiftLockError struct{ Path, Reason string }`; `Error()` = teks
    C4 dengan `Reason` salah satu dari `bukan berkas biasa`, `bukan milik
    root`, `bisa dibaca atau ditulis pengguna lain`, `punya lebih dari
    satu nama`.
  - `type liftGate struct` dengan `func newLiftGate(path string) *liftGate`,
    `func (g *liftGate) begin(ctx context.Context, db *sql.DB, logf func(level, format string, args ...any)) (*liftHold, error)`,
    `func (g *liftGate) tryGuard() (release func(), free bool)` (`free`:
    kunci berkas didapat lewat deskriptor sendiri dan tidak ada pembukaan
    di proses ini).
  - `type liftHold struct` dengan `func (h *liftHold) end() error`
    (pasang lagi yang semula menyala, lalu lepas),
    `func (h *liftHold) abandon()` (lepas tanpa menyentuh bendera),
    `func (h *liftHold) superWasOn() bool` (`super_read_only` menyala saat
    pembuka pertama membacanya). `end` dan `abandon` aman dipanggil lebih
    dari sekali; yang pertama yang berlaku.
  - `func AcquireLiftLock(path string) (release func(), err error)` —
    untuk CLI; `LOCK_EX|LOCK_NB`; dipegang → `ErrLiftBusy`; berkas tidak
    lulus → `*LiftLockError`.
  - `var ErrLiftBusy` (teks C3, untuk CLI) dan
    `var ErrLiftHeldByCommand` (teks T12, untuk pekerjaan agent yang
    mendapati kunci dipegang proses lain).
  - `liftReadOnly(ctx context.Context, logf func(level, format string, args ...any), db *sql.DB, mark *loadMark, g *liftGate) (*liftHold, error)`
    (tanda tangan baru; `g` tidak boleh nil di kode produksi).
  - `func refuseWeakLoadAccount(ctx context.Context, db *sql.DB) error`
    dan `var errLoadAccountWeak` pembawa teks T10 (`%s` = `CURRENT_USER()`).
  - `func loadTrailer(full, superWasOn bool) string`: `""` untuk dump
    bernama; `"\nFLUSH PRIVILEGES;\n"` (yang ada, `flushTrailer`) untuk
    dump penuh bila `superWasOn` salah; `"\nSET GLOBAL super_read_only =
    ON;\nFLUSH PRIVILEGES;\n"` untuk dump penuh bila `superWasOn` benar.
- Consumes: bukti M1 (tidak ada pemakai yang butuh `read_only` mati),
  baris X2, X6, X7 dari Task 2b.

Teks (dipakai persis):
- C3 `ErrLiftBusy`: `kunci baca-saja server ini sedang dibuka pekerjaan lain (misalnya bangun ulang yang sedang menyalin data); coba lagi sesudah pekerjaan itu selesai`
- C4 `LiftLockError`: `berkas kunci <berkas> tidak aman dipakai (<sebab>); tidak ada yang diubah. Agent membuat ulang berkas itu saat membutuhkannya; bila pesan ini tetap muncul, mulai ulang agent lalu jalankan perintah ini lagi.`
- T12 `ErrLiftHeldByCommand`: `kunci baca-saja server ini sedang dibuka sebuah perintah dbpanel agent yang dijalankan di server itu; tunggu sampai perintah itu selesai, lalu tekan "Ulangi dari tahap ini"`
- T10 `errLoadAccountWeak`: `akun MySQL %s yang dipakai agent tidak punya hak CONNECTION_ADMIN maupun SUPER, jadi tidak bisa memuat data selagi server ini baca-saja (read_only); tidak ada yang diubah. Di server yang dikarantina jalankan: sudo dbpanel agent grant-admin; di server lain beri hak itu sebagai root MySQL. Sesudah itu tekan "Ulangi dari tahap ini" pada pekerjaan di panel.`

Aturan gerbang:
- Pembuka pertama mengambil `flock` (deskriptor milik gerbang, dari
  `openLiftLock(path, true)`); bila dipegang proses lain ia mengulang
  paling lama 10 detik lalu gagal dengan `ErrLiftHeldByCommand`, sebelum
  mengubah apa pun. Ia membaca dan mencatat nilai semula.
- **Setiap** pembuka, juga yang berikutnya, mengirim `SET GLOBAL
  super_read_only = OFF` (yang pertama mungkin sudah dipasang lagi pihak
  lain). Tidak ada pernyataan lain yang mematikan bendera.
- Setiap pembuka mengambil satu `*sql.Conn` dari `db`-nya saat `begin`,
  menyetel `SET SESSION wait_timeout = 28800` padanya, dan menyimpannya
  sampai `end`/`abandon`.
- `end` pembuka terakhir, dengan batas `restoreTimeout` yang ada pada
  konteks yang tidak ikut batal: **membaca** `@@global.super_read_only`
  pada sambungan yang disimpan; bila sudah menyala (atau semula memang
  mati) tidak mengirim apa pun; bila masih mati dan semula menyala,
  mengirim `SET GLOBAL super_read_only = ON`. Bila pembacaan atau
  pernyataan itu gagal: dicatat dengan `logf("error", …)` sebagai
  kegagalan agent sendiri, galatnya dikembalikan, dan `flock` **tetap
  dilepas** (penjaga yang mengunci lagi). `end` yang bukan terakhir hanya
  menurunkan hitungan.
- `abandon` menurunkan hitungan tanpa pernyataan apa pun; bila pembuka
  terakhir pergi lewat `abandon`, nilai semula yang dicatat dilupakan dan
  `flock` dilepas.
- `tryGuard` memakai deskriptor sendiri (`openLiftLock(path, true)`).

- [ ] **Step 1: Test dulu** (`lift_test.go`, RED; mysqld sungguhan kecuali
  disebut lain):
  - `TestLiftCountsHolders`: dua `begin` tumpang-tindih pada server
    `1 1`; sesudah `end` pertama `super_read_only` masih 0; sesudah `end`
    kedua `1 1`.
  - `TestLiftKeepsReadOnlyOn`: `begin` → `read_only=1`,
    `super_read_only=0`; satu-satunya pernyataan pembuka yang terkirim
    `SET GLOBAL super_read_only = OFF`; `end` → `1 1`. Pada server yang
    semula `1 0`, `end` meninggalkan `1 0`.
  - `TestLiftLaterHolderReassertsOff`: selagi pembuka pertama aktif,
    `SET GLOBAL super_read_only = ON` dari luar; `begin` kedua →
    `super_read_only=0` lagi.
  - `TestLiftEndSendsNothingWhenAlreadyOn`: selagi pembukaan aktif,
    `super_read_only` dipasang lagi dari luar; `end` → tidak ada
    pernyataan `SET` yang terkirim (sambungan pencatat), hasil nil,
    `1 1`.
  - `TestLiftWaitsForForeignLock`: deskriptor lain memegang `flock`;
    `begin` gagal dengan `ErrLiftHeldByCommand` sesudah paling lama 10
    detik (batas disuntik) dan bendera tidak berubah.
  - `TestLiftLockReleasedWhenHolderDies`: proses anak memegang kunci lalu
    dimatikan; `tryGuard` berikutnya `free == true`.
  - `TestLiftLockNotInherited`: proses anak yang dimulai selagi
    pembukaan aktif dan hidup lebih lama darinya tidak ikut memegang
    kunci: sesudah `end`, `AcquireLiftLock` berhasil selagi anak itu
    masih berjalan.
  - `TestTryGuardSeesLocalLift`: selama `begin` aktif `tryGuard` →
    `free == false`; sesudah `end` → `free == true`.
  - `TestAcquireLiftLockBusy`: `AcquireLiftLock` saat `begin` aktif →
    `ErrLiftBusy`.
  - `TestLiftLockRefusesForeignFile` (tanpa mysqld): berkas mode `0644`,
    symlink, berkas dengan dua nama (hard link), direktori mode `0755` →
    `AcquireLiftLock` mengembalikan `*LiftLockError` dengan `Reason` yang
    sesuai dan tidak membuat atau mengubah apa pun; gerbang agent
    (`begin`, `tryGuard`) mengganti berkas itu dengan berkas `0600` baru
    dan berjalan; sasaran symlink tidak tersentuh.
  - `TestAbandonAfterRestartKeepsFlags`: `begin`; bendera dipasang dari
    luar (meniru restart); `abandon` → tidak ada pernyataan terkirim,
    kunci bebas, dan `begin` berikutnya membaca nilai semula yang baru.
  - `TestCloneReleasesLift` (mysqld sungguhan bila harness bisa CLONE,
    selain itu host palsu yang gagal sesudah fase data): sesudah job,
    `tryGuard` `free == true` dan `begin` berikutnya mematikan
    `super_read_only`.
  - `TestLiftRestoreOnKeptConnection`: selagi pembukaan aktif (dump
    bernama), semua sambungan lain di pool ditutup dan sambungan yang
    disimpan dibiarkan menganggur melewati `wait_timeout` global yang
    dipendekkan → `end` tetap berhasil, `1 1`.
  - `TestLiftReleasesLockWhenRestoreFails`: sambungan yang disimpan
    diputus dari luar → `end` mengembalikan galat, satu baris `error`
    lewat `logf`, `tryGuard` `free == true`.
  - `TestLoadTrailer`: ketiga bentuk `loadTrailer` persis seperti di
    atas.
- [ ] **Step 2: Test muat** (`snapshot_test.go`, RED):
  - `TestDumpKeepsReadOnlyDuringLoad`: selama muat berjalan
    `read_only=1`; akun tanpa `CONNECTION_ADMIN` ditolak `INSERT`.
  - `TestFullDumpRebuildRestoresLockInStream` (modelnya
    `TestDumpAllDatabasesReplacesAdminAccount`): bangun ulang dengan dump
    penuh yang mengganti akun admin, server semula `1 1` → sesudah job
    bendera `1 1`, hasil `AdminReplaced`, kunci berkas bebas, `end`
    mengembalikan nil. (Bahwa penjaga lalu tidak melapor dan tidak
    mencatat "bisa ditulis sejak muat" ditegaskan Task 4,
    `TestFullDumpRebuildLeavesNoRelock`.)
  - `TestDumpRefusesLoadAccountWithoutBypass`: akun muat tanpa
    `CONNECTION_ADMIN` dan tanpa SUPER, `read_only` menyala (server
    dikarantina, dan server replika biasa) → `mysql.dumpFrom` gagal
    dengan teks T10 persis; replikasi tidak dihentikan (`STOP REPLICA`
    tidak terkirim), log biner tidak di-reset (`gtid_executed` tidak
    berubah), bendera `1 1`. Akun dengan SUPER saja diterima.
- [ ] **Step 3:** Pastikan gagal (belum terdefinisi).
- [ ] **Step 4:** Tulis `lift.go` menurut aturan gerbang di atas.
- [ ] **Step 5:** Ubah `liftReadOnly` menjadi pembungkus `g.begin`: tanda
  `mark.set()` dipanggil sebelum `begin` dan `mark.clear()` sesudah
  `end`/`abandon` (menutup jendela fakta F9). Teruskan `*liftGate` ke
  kelima pemakai; `Driver` membuat satu gerbang di `Register`.
  - `dumpFrom`: `refuseWeakLoadAccount` dijalankan bersama pemeriksaan
    sebelum perubahan lokal (sebelum `STOP REPLICA`), bila `read_only`
    menyala, di peran apa pun; penutup aliran muat dari
    `loadTrailer(len(p.Databases) == 0, hold.superWasOn())`. Penutup ini
    milik task ini saja; Task 6 tidak mengubahnya.
  - `cloneFrom`: satu `defer` — sebelum fase data selesai `hold.end()`,
    sesudahnya (`dataReplaced`) `hold.abandon()`; pada jalan berhasil
    `abandon` dipanggil sesudah `restartAfterClone` kembali dan, untuk
    bangun ulang, sesudah `persistReadOnly(vctx, after)`-nya.
  - `installClonePlugin`, `DropDatabases`, `createAccount`: `end` di
    tempat `restore` sekarang, dengan penanganan galat yang sama.
- [ ] **Step 6:** Jalankan test paket; test lama yang memanggil
  `liftReadOnly` langsung disesuaikan tanpa mengubah yang ditegaskannya
  (yang menegaskan `read_only` mati selama pembukaan, bila ada, berbalik
  dan didaftar di laporan).
- [ ] **Step 7:** Gerbang; commit
  `feat(agent): one lock and a holder count for every read-only lift`.

Akibat yang dicatat: pekerjaan agent lain yang masih membuka saat muat
dump penuh berakhir mendapati `super_read_only` sudah menyala lagi dan
gagal 1290 pada pernyataannya (spek bagian 8); direktori
`/var/lib/dbpanel/agent` dibuat agent atau CLI sendiri, pemasang tidak
diubah.

### Task 4: Penjaga membaca bendera di setiap putaran

**Files:**
- Modify: `internal/agent/drivers/mysql/quarantine.go`
  (`guardQuarantined`, `persistReadOnly`, `settleRebuilt`),
  `rebuild_load.go`, `snapshot.go` (`registerSnapshotJob`), `driver.go`;
  `internal/proto/mysql.go` (dua nilai keterangan)
- Test: `quarantine_test.go`, `startlock_guard_test.go`,
  `rebuild_settle_test.go`

**Interfaces:**
- Consumes: `liftGate.tryGuard() (release func(), free bool)`,
  `ErrLiftHeldByCommand` (Task 3); baris X4 dan X6 Task 2b.
- Produces:
  - `proto.QuarantineWhileRunning = "running"`,
    `proto.QuarantineWhileUnverified = "unverified"`.
  - field `Driver.relocked func(h Host, now time.Time, while string)`,
    dipanggil dengan salah satu nilai di atas; nil ditoleransi (tidak
    ada laporan) sampai Task 5 mengisinya.
  - `rebuildLoad.writableSince bool` dengan
    `func (d *Driver) noteWritableAfterLoad()` dan
    `func (d *Driver) writableAfterLoad() bool`.
  - `rebuildLoad.expectRefused bool` dengan
    `func (m *loadMark) adminReplaced()` (dipanggil pembungkus job
    snapshot bila hasilnya `AdminReplaced`); dihapus saat login penjaga
    berhasil lagi dan saat `mark.set()`.
  - `var errWritableSinceLoad` pembawa teks T7.
  - `persistReadOnly(ctx context.Context, db execer) error`, dengan
    antarmuka `execer` yang sudah ada di `users.go` (dipenuhi `*sql.DB`
    dan `*sql.Conn`; pemanggil lain tidak berubah).

Teks (dipakai persis):
- T7 `errWritableSinceLoad`: `server ini sempat tidak terkunci selama atau sesudah salinan data; karantina tidak diakhiri. Mulai lagi Bangun ulang sebagai replika dari panel; tombol "Ulangi dari tahap ini" tidak cukup, karena salinan data harus diulang.`
- T12 (dari Task 3, `ErrLiftHeldByCommand`) dipakai settle bila kunci
  berkas dipegang sebuah perintah CLI: `kunci baca-saja server ini sedang dibuka sebuah perintah dbpanel agent yang dijalankan di server itu; tunggu sampai perintah itu selesai, lalu tekan "Ulangi dari tahap ini"`

**Aturan (spek 5.2): penjaga menulis `super_read_only`, melaporkan start,
atau melaporkan "dikunci lagi" hanya selagi ia sendiri memegang kunci
berkas dan tidak ada tanda muat yang aktif.** Urutan `guardQuarantined`,
menggantikan pulang-awal `if d.guard.up && sameRun { return nil }` dan
`persistReadOnly` tanpa syarat:

1. `h.Open`; pembukuan `qUp`, `Uptime`, `qStartPending`, `takeRecheck`
   seperti sekarang. Ambil satu `*sql.Conn`, setel `SET SESSION
   lock_wait_timeout = 10` padanya, dan pakai sambungan itu untuk semua
   pernyataan putaran ini. Baca `super_read_only`, `read_only`,
   `init_file` **setiap putaran**.
2. `needWrite` = ada bendera mati, atau putaran ini peralihan (seperti
   sekarang: `!d.guard.up || !sameRun`). Bila `needWrite` salah: selesai;
   berkas kunci tidak dibuka.
3. `release, free := tryGuard()`; `loading := deferringToRebuild()`
   (pencatatan lognya seperti sekarang).
4. `!free || loading`: penjaga tidak menyentuh `super_read_only`. Bila
   `read_only` mati: `SET GLOBAL read_only = ON`; berhasil →
   `d.relocked(h, now, proto.QuarantineWhileRunning)`; berhasil atau
   tidak → `noteWritableAfterLoad()`. Lalu `d.guard.up = false` dan
   selesai; `qStartPending` dipertahankan, jadi start dinilai pada
   putaran pertama sesudah pembukaan berakhir.
5. `free && !loading`: `persistReadOnly` di bawah kunci itu, lalu lepas.
   Sesudahnya, seperti sekarang: laporan start bila `qStartPending`
   (dengan `put` yang dibaca di langkah 1), selain itu
   `d.relocked(h, now, proto.QuarantineWhileRunning)` bila ada bendera
   yang terbaca mati. `noteWritableAfterLoad()` bila ada bendera yang
   terbaca mati, juga bila `persistReadOnly` gagal. `persistReadOnly`
   yang gagal sesudah kedua bendera terbaca menyala tidak mencatat apa
   pun dan diulang putaran berikutnya.
6. Putaran "tidak bisa memeriksa": (a) `h.Open` gagal dengan galat
   apa pun yang dikirim server MySQL sendiri saat login
   (`*mysql.MySQLError`: 1045, 1698, akun dikunci 3118, dan lainnya;
   `LoginRefused` di conn.go tidak diperlebar); (b) pembacaan bendera
   gagal; (c) ada bendera terbaca
   mati, kunci didapat, dan pernyataan pemasangnya gagal, termasuk galat
   1205 (`lock_wait_timeout` habis). Hitungan `d.qUnverified` naik pada
   putaran seperti itu dan kembali nol pada putaran lain (MySQL yang
   tidak menjawab sama sekali — tidak ada yang mendengarkan, batas
   waktu, soket tidak ada — bukan putaran seperti itu). Saat hitungan
   mencapai 3, bila `tryGuard` bebas, tidak ada tanda muat, dan
   `expectRefused` salah: `d.relocked(h, now,
   proto.QuarantineWhileUnverified)` dan `noteWritableAfterLoad()`;
   selama keadaannya berlanjut diulang paling sering sekali per 10 menit.
   Selama `expectRefused` (jendela sesudah muat mengganti akun sampai
   login penjaga berhasil lagi) sebab (a) tidak menaikkan hitungan.

Batas waktu konteks tidak dipakai sebagai batas pernyataan (X4:
pernyataan yang dibatalkan klien tetap menunggu di server dan bisa
berlaku setengah); batasnya `lock_wait_timeout` di sambungan itu.

Restart CLONE: MySQL menyala dengan nilai tersimpan penerima sendiri (X6:
`1 1` di server yang dikarantina), tanda muat masih aktif → langkah 4
tanpa apa pun untuk dipasang; `afterLoad` menghapus `qStartPending`
seperti sekarang, jadi restart itu tidak dilaporkan sebagai start.

- [ ] **Step 1: Test dulu** (mysqld sungguhan, RED):
  - `TestGuardRelocksWhileUp`: peran `quarantined`, putaran pertama
    selesai; `SET GLOBAL super_read_only=OFF` dari sambungan root;
    `GuardTick` berikutnya → `1 1`, nilai tersimpan ON, `relocked`
    dipanggil sekali dengan `"running"`.
  - `TestGuardNormalRoundDoesNotTouchLockFile`: bendera `1 1`, bukan
    peralihan → pembuka berkas kunci (disuntik) tidak dipanggil.
  - `TestGuardLeavesRegisteredLiftAlone`: `begin` aktif → `GuardTick`
    tidak mengubah `super_read_only`, `relocked` tidak dipanggil.
  - `TestGuardKeepsReadOnlyDuringLift`: `begin` aktif, lalu `SET GLOBAL
    read_only=OFF` dari luar →
    `GuardTick` memasang `read_only=1`, `super_read_only` tetap 0,
    `relocked` dipanggil dengan `"running"`, `writableAfterLoad()` benar.
  - `TestGuardLeavesLongLiftAlone`: `begin` aktif selama 12 putaran →
    tidak ada perubahan, tidak ada laporan.
  - `TestGuardSkipsWhenCLIHoldsLock`: deskriptor lain memegang `flock`,
    `super_read_only=0`, `read_only=1` → tidak ada perubahan, tidak ada
    laporan.
  - `TestGuardTransitionDuringCLILift`: deskriptor lain memegang kunci,
    bendera `1 0`, putaran pertama sesudah driver dibuat → tidak ada
    pernyataan tulis, tidak ada laporan; sesudah kunci dilepas dan
    bendera dipasang lagi: satu laporan start `ok`, tidak ada `late`.
  - `TestGuardTransitionDuringUnmarkedLift`: `DropDatabases` sedang
    berjalan (pembukaan tanpa tanda muat) saat putaran peralihan → tidak
    ada `SET PERSIST`, tidak ada laporan.
  - `TestCloneRestartIsNoStart`: penegasan "The load's restart is not a
    start of the quarantined server" yang kini ada di
    `TestQuarantineGuardDefersToRebuildLoad`, dijalankan dengan pembukaan
    lewat gerbang: MySQL menyala lagi selagi tanda muat dan pembukaan
    masih aktif → tidak ada laporan start, tidak ada `relocked`,
    `writableAfterLoad()` salah.
  - `TestGuardBoundedBehindTableLock`: sesi berhak memegang `LOCK TABLES
    … WRITE` selagi `super_read_only` mati dan kunci berkas bebas →
    pernyataan penjaga berakhir dengan galat 1205 sesudah batasnya (batas
    disuntik lebih pendek), bendera tidak berubah, tidak ada pernyataan
    yang tertinggal menunggu di server (`processlist`), `GuardTick`
    kembali; sesudah `UNLOCK TABLES` putaran berikutnya memasang `1 1`
    dan memanggil `relocked` dengan `"running"`. Tiga putaran berturut
    di belakang kunci itu → `relocked` dengan `"unverified"`.
  - `TestGuardReportsWhenItCannotLogIn`: dua kasus, akun tersimpan
    dikunci (`ACCOUNT LOCK`, galat 3118) dan kata sandinya diganti
    (1045); login ditolak tiga putaran, tanpa tanda muat, kunci bebas → `relocked`
    dipanggil sekali dengan `"unverified"`, `writableAfterLoad()` benar;
    putaran berikutnya dalam 10 menit tidak memanggil lagi; sesudah 10
    menit (jam disuntik) sekali lagi. Dua putaran ditolak lalu berhasil →
    tidak ada panggilan.
  - `TestGuardQuietWhileMySQLDown`: mysqld berhenti lima putaran → tidak
    ada laporan, `writableAfterLoad()` tetap salah.
  - `TestGuardReportsWhenRelockFails`: bendera mati, akun agent tanpa
    `SYSTEM_VARIABLES_ADMIN` → sesudah tiga putaran `"unverified"`.
  - `TestGuardUnverifiedNotDuringLift`: login ditolak tiga putaran
    selagi deskriptor lain memegang kunci → tidak ada panggilan.
  - `TestGuardQuietWhileAdminReplaced`: sesudah muat yang berakhir
    `AdminReplaced`, login ditolak 12 putaran → tidak ada panggilan,
    `writableAfterLoad()` salah; login pertama yang berhasil mengakhiri
    pengecualian.
  - `TestFullDumpRebuildLeavesNoRelock`: bangun ulang dengan dump penuh
    yang mengganti akun admin (lihat Task 3), lalu 12 putaran penjaga →
    bendera `1 1`, `relocked` tidak pernah dipanggil,
    `writableAfterLoad()` salah.
  - `TestFailedPersistAfterFlagsOnCostsNothing`: putaran peralihan,
    bendera terbaca `1 1`, `SET PERSIST` gagal → `writableAfterLoad()`
    salah, tidak ada laporan, putaran berikutnya mencoba lagi.
  - `TestSettleRefusedAfterWritableSinceLoad`: muat selesai (catatan muat
    ada); bendera dimatikan dari luar; `GuardTick`; lalu
    `mysql.configureReplication` → ditolak dengan teks T7 persis, pin
    tetap `quarantined`, persetujuan tetap ada. Muat berikutnya menghapus
    catatan itu.
  - `TestSettleRefusedWhenFlagsOffNow`: muat selesai; bendera dimatikan
    dari luar dan penjaga belum berputar → `mysql.configureReplication`
    ditolak dengan T7.
  - `TestSettleWaitsForCLILift`: deskriptor lain memegang kunci →
    `mysql.configureReplication` ditolak dengan teks T12, pin tetap
    `quarantined`, `writableAfterLoad()` salah; sesudah dilepas, settle
    berhasil.
  - `TestFailedLoadDropsEarlierLoadRecord`: muat penuh berhasil; muat
    kedua gagal sesudah pembukaannya dimulai (di dalam aliran muat) →
    `loadedFrom` salah, `mysql.configureReplication` menjawab
    `errNoLoadRecord`.
  - `TestEarlyFailedLoadKeepsRecordAndWritableFlag`: muat kedua gagal
    sebelum ada perubahan lokal (kata sandi akun dump salah, gagal di
    uji klien) → catatan muat lama dan tanda "sempat bisa ditulis"
    tetap; settle tetap menjawab T7.
  - Test peralihan yang ada (`TestQuarantineGuardDefersToRebuildLoad`,
    `TestQuarantineStartEvents`, …) tetap lulus tanpa diubah maknanya:
    tanda muat saja tetap menahan penjaga (langkah 4).
- [ ] **Step 2:** Pastikan gagal.
- [ ] **Step 3:** Tulis `guardQuarantined` menurut enam langkah di atas.
- [ ] **Step 4:** `rebuild_load.go`: `mark.set()` menghapus
  `writableSince` dan `expectRefused` **dan membuang catatan muat**
  (`loaded = ""`, `copied = loadCopy{}`); pembungkus job snapshot
  memanggil `mark.adminReplaced()` bila hasilnya `AdminReplaced`.
- [ ] **Step 5:** `settleRebuilt`, sebelum daftar database server itu
  dibandingkan dengan catatan muat: `writableAfterLoad()` →
  `errWritableSinceLoad`. Tepat sebelum
  `d.pins.settleRebuilt`: `tryGuard()`; tidak bebas →
  `ErrLiftHeldByCommand`; bebas → baca kedua bendera di bawah kunci itu;
  selain `1 1`, atau tidak terbaca → `errWritableSinceLoad`.
- [ ] **Step 6:** Test lulus; gerbang; commit
  `feat(agent): the quarantine guard reads the read-only flags every round`.

### Task 5: Catatan dan laporan "dikunci lagi" sebagai hasil start `late`

**Files:**
- Modify: `internal/proto/mysql.go`, `internal/agent/drivers/mysql/startlock_guard.go`,
  `start_result.go`, `driver.go`
- Test: `start_result_test.go`, `start_kinds_test.go`, `startlock_guard_test.go`

**Interfaces:**
- Consumes: `proto.QuarantineWhileRunning`,
  `proto.QuarantineWhileUnverified`, `Driver.relocked` (Task 4);
  `startID`, `saveStartResult`, `enqueue(proto.EventQuarantineStart, …)`.
- Produces:
  - `QuarantineStartEvent.While string \`json:"while,omitempty"\`` dan
    `StartKindState.While string \`json:"while,omitempty"\``.
  - `startKindRecord.While string \`json:"while,omitempty"\`` (catatan
    yang disimpan); `StartResult(now)` menyalinnya ke
    `StartKindState.While`.
  - `func (d *Driver) recordRelock(t time.Time, while string) error`:
    hanya mengisi `Kinds[late] = {Decided: t, While: while}` dan hasil
    terburuk; **tidak pernah** memanggil `judgeKinds`, jadi entri
    `bypassed` dan `failed` tidak disentuh.
  - `func (d *Driver) reportRelocked(h Host, now time.Time, while string)`,
    dipasang ke `Driver.relocked`: `recordRelock` lebih dulu, lalu
    `enqueue` kejadian `monitor.quarantineStart` dengan `Result ==
    "late"`, `While`, `StartID == startID(now)`. Setiap panggilan
    dicatat; kejadian diantrekan paling sering sekali per 60 detik.

- [ ] **Step 1: Test dulu:**
  - `TestRelockRecordedBeforeQueued`: `reportRelocked(…, "running")` →
    berkas hasil start memuat jenis `late` dengan `While == "running"`
    sebelum kejadian masuk antrean; kejadian `monitor.quarantineStart`
    dengan `Result == "late"`, `While == "running"`, dan `StartID` sama
    dengan `startID(now)` dan dengan `StartID` entri `late` di
    `HealthSnapshot.StartResult.Kinds`.
  - `TestRelockSurvivesAgentRestart`: sesudah driver dibuat ulang,
    `HealthSnapshot.StartResult.Kinds` masih memuat entri itu dengan
    `While`-nya.
  - `TestRelockReportReadableByOldPanelShape`: JSON kejadian itu, dibaca
    ke struct tanpa field `While`, tetap sah dan `Result == "late"`.
  - `TestRelockRecordsEveryIncidentQueuesOncePerMinute`: tiga kejadian
    dalam satu menit → berkas memuat `Decided` kejadian ketiga (tiap
    kejadian menulis `Decided` baru), satu kejadian di antrean; kejadian
    keempat sesudah satu menit → kejadian kedua di antrean.
  - `TestRelockLeavesBypassedAndFailedEntriesAlone`: entri `bypassed` dan
    `failed` yang tersimpan tetap `Over == false` dengan `Decided`
    semula sesudah `recordRelock`.
  - `TestUnverifiedRecordedWithItsWhile`: `reportRelocked(…,
    "unverified")` → `While == "unverified"` di berkas, snapshot dan
    kejadian.
- [ ] **Step 2:** Pastikan gagal.
- [ ] **Step 3:** Tambah field proto dan field catatan; `recordRelock`;
  `reportRelocked`; pasang ke `Driver.relocked` di `Register`.
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
  `func (m *loadMark) sourceSeen(names []string)`;
  `func (c loadCopy) onSource(name string) bool` (tanpa beda huruf
  besar-kecil; dipakai Task 9 untuk memisah nama);
  `var errSecondListFailed` pembawa teks T11.
- Consumes: bukti M2; `dumpFrom` sesudah Task 3 (penutup aliran muat
  tidak diubah di sini).

Teks (dipakai persis):
- T11 `errSecondListFailed`: `salinan selesai dimuat, tetapi daftar database master baru tidak bisa dibaca sesudahnya, jadi salinan ini tidak dihitung; tekan "Ulangi dari tahap ini"`

- [ ] **Step 1: Test dulu:**
  - test tabel `notCopied` yang ada di `rebuild_load_internal_test.go`
    (tambah baris): dump penuh
    (`full=true, source=[a,b]`), yang ada `[a,b,c]` → `[c]`; beda huruf
    besar-kecil dihitung sama; dump bernama dan CLONE tidak berubah.
  - `TestSourceDatabasesOddNames` (mysqld sungguhan sebagai sumber): nama
    berspasi, ber-backtick, bertanda kutip, non-ASCII kembali utuh; skema
    sistem dibuang; keluaran dibatasi (lebih dari 1 MiB → galat).
  - `TestRebuildFullDumpRefusesDatabaseCreatedDuringLoad` (mysqld
    sungguhan): bangun ulang penuh; di tengah muat, akun ber-SUPER membuat
    database `liar`; `mysql.configureReplication` menolak dengan teks
    "masih punya database yang tidak disalin" yang memuat `liar`; pin tetap
    `quarantined`.
  - `TestRebuildFullDumpSettles` yang ada tetap lulus.
  - `TestDumpFailsBeforeChangingData` (tambah kasus): daftar pertama tidak
    terbaca → muat gagal, tidak ada yang berubah lokal.
  - `TestSecondSourceListRetriedThreeTimes`: daftar kedua gagal dua kali
    lalu berhasil → muat dihitung, gabungan kedua daftar tersimpan.
  - `TestSecondSourceListFailureFailsCopyStep`: daftar kedua gagal tiga
    kali → job `mysql.dumpFrom` gagal dengan teks T11 persis; tidak ada
    catatan muat; bendera `1 1`.
- [ ] **Step 2:** Pastikan gagal.
- [ ] **Step 3:** `sourceDatabases` menjalankan
  `loadBin --defaults-file=<srcFile> <argumen TLS> -N -B -e "SELECT HEX(schema_name) FROM information_schema.schemata"`
  lewat `h.Exec`, keluaran dibatasi 1 MiB, tiap baris di-dekode dari hex,
  skema sistem (`systemDatabases`) dibuang.
- [ ] **Step 4:** Di `dumpFrom`, bila `mark != nil` dan
  `len(p.Databases) == 0`: baca daftar sekali sebelum komentar "From here
  on local data changes", dan sesudah `pipeDump` berhasil sampai tiga
  kali (jeda pendek pilihan pelaksana); `mark.sourceSeen` menyimpan
  gabungannya; tiga kegagalan → `errSecondListFailed` dan `mark.done`
  tidak dipanggil. `notCopied`: untuk `full`, nama yang ada tetapi tidak
  di `source` → tidak disalin (ditambah aturan `before` yang ada).
- [ ] **Step 5:** Gerbang; commit
  `feat(agent): end a quarantine only when every database is on the new master's list`.

### Task 7: `mysqlbinlog` dan ekor log sebagai pemilik direktori

Dibangun (bukti M3).

**Files:**
- Create: `internal/agent/drivers/mysql/svcuid.go`, `svcuid_test.go`
- Modify: `realhost.go` (`ExecEnvAs`), `leftover.go` (`streamBinlog`),
  `salvage.go`, `binlog.go` (`readBinlogGTIDs`, `lastBinlogFile`),
  `startlock_guard.go` (`watchBootFailure`)

**Interfaces:**
- Produces:
  - `var ErrNotStarted = errors.New(…)`: `host.ExecEnvAs` membungkusnya
    (`%w`) bila `cmd.Run` gagal tanpa prosesnya pernah berjalan (galatnya
    bukan `*exec.ExitError` dan konteksnya belum berakhir: fork, ganti
    uid/grup, atau exec gagal). Proses yang berjalan lalu keluar dengan
    kode bukan nol, dan proses yang dihentikan konteks, **bukan**
    `ErrNotStarted`. Host palsu di test mengembalikan galat yang
    membungkusnya untuk meniru.
  - `func serviceCredential(dir string) (*syscall.Credential, error)`:
    `lstat(dir)` lewat seam
    `var lstatOwner = func(path string) (uid, gid uint32, err error)`
    (symlink tidak diikuti: yang dibaca pemilik nama itu sendiri); tidak
    terbaca → galat; pemilik uid 0 → `errServiceRoot`; selain itu
    `&syscall.Credential{Uid: uid, Gid: gid}` (tanpa grup tambahan).
  - `func execAsService(ctx context.Context, h Host, dir string, warn func(string), stdin io.Reader, stdout io.Writer, name string, args ...string) error`:
    kredensial dari `serviceCredential(dir)`. Galat (termasuk
    `errServiceRoot`) → `h.Exec` seperti sekarang dan `warn` sekali
    dengan teks T19. Ada kredensial → `h.ExecEnvAs`; hanya bila galatnya
    `ErrNotStarted` → `h.Exec` dan `warn` sekali; galat lain dikembalikan
    apa adanya dan program **tidak** dijalankan lagi sebagai root.

Teks (dipakai persis):
- T19: `tidak bisa menjalankan sebagai akun layanan MySQL; dijalankan sebagai root`

`dir`: direktori binlog (`filepath.Dir` jalur berkas binlog pertama, atau
direktori indeks binlog saat MySQL mati) untuk `mysqlbinlog` dan `cat --`
indeks; direktori berkas log galat untuk ekor log.

- [ ] **Step 1: Test dulu** (host palsu yang mencatat kredensial, seperti
  `printDefaultsHost`):
  - `TestBinlogToolsRunAsDirectoryOwner`: `mysql.leftover`,
    `mysql.salvage`, `mysql.binlogTail` (MySQL mati) memanggil
    `ExecEnvAs` dengan uid dan gid pemilik direktori binlog untuk
    `mysqlbinlog`; indeks binlog dibaca lewat `cat --` dengan kredensial
    yang sama.
  - `TestServiceCredentialIsDirectoryOwner`: direktori milik uid 1234
    (bukan akun `mysql`) → `Uid == 1234`, `Gid` milik direktori, `Groups`
    kosong; symlink milik uid 1234 yang menunjuk ke direktori milik root
    → `Uid == 1234` (symlink tidak diikuti, jadi akun yang membuat
    symlink tidak pernah mendapat proses root).
  - `TestServiceCredentialRootCountsAsMissing`: pemilik uid 0 → perintah
    jalan lewat `Exec`, `warn` dipanggil sekali dengan T19.
  - `TestServiceCredentialMissingFallsBackToRoot`: `lstat` gagal →
    perintah tetap jalan lewat `Exec`, `warn` sekali, hasil
    Periksa/Amankan sama.
  - `TestServiceToolFailureIsNotRetriedAsRoot`: program berjalan sebagai
    uid itu lalu keluar bukan nol (berkas binlog diganti symlink ke
    berkas yang hanya terbaca root) → galatnya dikembalikan, `Exec`
    (root) tidak pernah dipanggil; untuk Amankan keluaran tidak ditulis
    dua kali.
  - `TestServiceToolNotStartedFallsBackToRoot`: `ExecEnvAs` mengembalikan
    `ErrNotStarted` → `Exec` dipanggil sekali, `warn` sekali.
  - `TestExecEnvAsReportsNotStarted` (host sungguhan): program yang tidak
    ada → galat membungkus `ErrNotStarted`; program yang keluar 1 →
    tidak.
  - `TestBootLogTailAsDirectoryOwner`: ekor log dibaca
    `tail -c 65536 -- <path>` sebagai pemilik direktori log, keluaran
    dibatasi 64 KiB, batas waktu 5 detik (proses yang menggantung
    dihentikan, penjaga tidak tertahan); pemilik root atau tidak terbaca
    → `readTail` yang ada.
  - Test Periksa/Amankan dengan mysqld sungguhan yang ada tetap lulus.
- [ ] **Step 2:** Pastikan gagal.
- [ ] **Step 3:** `ErrNotStarted` di `realhost.go`; tulis `svcuid.go`;
  ganti tiga pemanggilan `mysqlbinlog`, pembacaan indeks, dan pembacaan
  ekor log. `warn` menulis ke log pekerjaan agent (`j.Logf("warn", …)`)
  atau `slog` berbatas-laju di penjaga.
- [ ] **Step 4:** Gerbang; commit
  `fix(agent): run mysqlbinlog and read the error log as the owner of the directory read`.

### Task 8: Sesi root, jendela terbuka bersama, dan `drop-database`

**Files:**
- Create: `internal/agent/cli_rootsql.go`, `internal/agent/cli_dropdb.go`,
  test `internal/agent/cli_rootsql_test.go`, `internal/agent/cli_dropdb_test.go`
- Modify: `internal/agent/cli.go` (dispatch, bantuan)

Task ini tidak lagi menulis berkas daftar "tidak disalin" dan tidak
mengubah `rebuild_load.go` atau `quarantine.go` (lihat "Perubahan sesudah
tinjauan rancangan", R-10).

**Interfaces:**
- Produces:
  - `type promptReader struct` dengan
    `func newPromptReader(in io.Reader) *promptReader`,
    `func (p *promptReader) line() (string, error)`,
    `func (p *promptReader) secret() (string, error)` (tanpa gema lewat
    termios paket `syscall` bila `in` terminal; selain itu baris biasa).
    **Satu** pembaca untuk baris kata sandi dan baris konfirmasi: fungsi
    utama membuatnya sekali dan meneruskannya.
  - `type rootSession struct{ DB *sql.DB; Conn *sql.Conn; User string; Close func() }`:
    `Conn` satu sambungan untuk semua pernyataan perintah (`sql_log_bin`
    per sesi); `DB` hanya untuk sambungan kedua yang memasang bendera
    lagi saat sinyal.
    `func (s *rootSession) dumpCommand(ctx context.Context, bin string, args ...string) (*exec.Cmd, error)`:
    perintah klien dengan kredensial sesi itu tanpa berkas — login tanpa
    kata sandi: `--no-defaults --socket=<s> --user=<u>`; dengan kata
    sandi: opsi lewat pipa yang diwariskan,
    `--defaults-file=/dev/fd/3` (satu-satunya berkas opsi yang dibaca).
  - `func openRootSession(ctx context.Context, dir, user string, in *promptReader, stdout io.Writer) (*rootSession, error)`:
    socket dari tata letak agent; `user` kosong = `root`; mula-mula tanpa
    kata sandi; bila ditolak, cetak C5 (atau C5b bila `user` diberi),
    baca satu baris lewat `in.secret()`, coba lagi sekali.
  - `var cliRootSession = openRootSession` (seam test);
    `var cliLiftLock = mysql.DefaultLiftLock`.
  - `func probeLiftLock(path string, wait time.Duration) error`: ambil dan
    lepas kunci berkas, mengulang percobaan tanpa-menunggu sampai `wait`
    (3 detik); dipegang → `mysql.ErrLiftBusy`; berkas tidak lulus →
    `*mysql.LiftLockError`.
  - `func runLifted(ctx context.Context, s *rootSession, dir, lockPath string, recheck, do func(ctx context.Context, c *sql.Conn) error) error`:
    jendela terbuka bersama ketiga perintah (Task 9 memakainya). Galat
    bertipe: `errStateChanged` (teks C11), `errLockWait` (galat MySQL
    1205 dari `do`), `errInterrupted` (teks C13), `errWindowCap` (teks
    C14).
  - `func printable(s string) string`: membuang karakter kendali dan
    penanda arah (aturan yang sama dengan `AgentText` di panel) dari
    setiap nama asal MySQL sebelum dicetak; baris syslog memakai
    `strconv.Quote`, seperti `approvalSyslog` untuk nama pengguna.
  - `func mainDropDatabase(args []string, in io.Reader, stdout, stderr io.Writer, writeSyslog func(string) error) int`.
- Consumes: `AcquireLiftLock`, `ErrLiftBusy`, `LiftLockError` (Task 3);
  `mysql.LoadPins`; baris X5 Task 2b.

Teks (dipakai persis):
- C1: `perintah ini hanya untuk server yang dikarantina (quarantined); peran server ini: <peran>`
- C2: `kunci baca-saja server ini sedang tidak terpasang penuh; tunggu beberapa detik (agent memasangnya lagi) lalu ulangi`
- C5: `Kata sandi root MySQL: `
- C5b: `Kata sandi akun MySQL <nama>: `
- C6: `berkas cadangan <berkas> sudah ada; tidak ada yang ditimpa dan tidak ada yang dihapus. Jalankan perintah ini lagi.`
- C7: `Peringatan: folder <folder> bukan milik root atau bisa ditulis pengguna lain; cadangan di sana bisa diubah atau dihapus pengguna lain.`
- C8: `Cadangan database <nama> tidak bisa dibuat (<sebab>); tidak ada yang dihapus. Bila cadangan tetap tidak bisa dibuat, jalan yang tersisa: Hapus server ini dari panel, lalu pasang lagi sebagai replika baru.`
- C9: `Periksa dulu di master baru (hanya membaca): mysql -e 'SHOW DATABASES LIKE "<nama>"'. Jawabannya harus kosong.`
- C9b: `Periksa dulu di master baru (hanya membaca): mysql -e 'SELECT schema_name FROM information_schema.schemata WHERE HEX(schema_name) = "<HEX nama>"'. Jawabannya harus kosong.`
- C10: `Agent tidak bisa memastikan database ini tidak ada di master baru. Bila database ini ada di master baru, menghapusnya di sini merusak replikasi server ini. Ketik nama database itu untuk melanjutkan, atau apa saja untuk membatalkan: `
- C11: `Keadaan server ini berubah selama Anda membaca; tidak ada yang diubah. Jalankan perintah ini lagi.`
- C12: `Database <nama> sedang dipakai sambungan lain, jadi belum dihapus. Kunci baca-saja sudah terpasang lagi. Hentikan aplikasi atau sesi yang masih memakai database itu, lalu jalankan perintah ini lagi. Cadangan tadi tetap ada di <berkas>.`
- C13: `Perintah dihentikan (sinyal); kunci baca-saja server ini sudah terpasang lagi. Jalankan perintah ini lagi bila pekerjaannya belum selesai.`
- C14: `Perintah ini sudah membuka kunci baca-saja selama 10 menit dan dihentikan; kunci baca-saja sudah terpasang lagi. Jalankan perintah ini lagi.`
- C15: `Selesai. Tekan "Ulangi dari tahap ini" pada pekerjaan di panel, atau mulai lagi Bangun ulang sebagai replika.`
- C3 dan C4 (dari Task 3) dicetak apa adanya dari `ErrLiftBusy` dan
  `LiftLockError`.

**`runLifted`** (sama untuk ketiga perintah):
1. `AcquireLiftLock(lockPath)`; gagal → galatnya (C3 atau C4).
2. Pemeriksaan ulang sesudah konfirmasi, pada `s.Conn`: pin peran dibaca
   lagi dari `dir` dan harus `quarantined`; `@@global.read_only`,
   `@@global.super_read_only` harus `1 1`; lalu `recheck` milik perintah.
   Salah satu gagal → `errStateChanged`, tidak ada pernyataan lain yang
   terkirim.
3. SIGINT, SIGTERM dan SIGHUP ditangkap sejak sebelum langkah 4 sampai
   sesudah langkah 6. `SET SESSION sql_log_bin = 0`, `SET SESSION
   lock_wait_timeout = 60` pada `s.Conn`.
4. `SET GLOBAL super_read_only = OFF`; `do`.
5. Selalu, juga sesudah galat: pasang lagi yang terbaca di langkah 2
   (`SET GLOBAL super_read_only = ON`), pada `s.Conn`, dengan konteks
   yang tidak ikut batal.
6. Baca kedua bendera; bukan `1 1` → pasang lagi dan katakan (baris
   keluaran susunan pelaksana, masuk daftar teks Task 15). Lepas kunci.
- Sinyal, atau jendela terbuka melewati 10 menit: sambungan kedua dari
  `s.DB` menyetel `lock_wait_timeout`-nya sendiri, mengirim `KILL QUERY`
  untuk sambungan `s.Conn`, lalu `SET GLOBAL super_read_only = ON`;
  hasilnya `errInterrupted` atau `errWindowCap`; kode keluar bukan nol.
  (X4: membatalkan konteks saja tidak menghentikan pernyataan di server.)

**Urutan `drop-database <nama> [--ke <folder>] [--pengguna <nama>] [--dir <dir>]`:**
1. Pin harus `quarantined`; bukan → C1.
2. `probeLiftLock` (3 detik); dipegang → C3; berkas tidak lulus → C4.
   Tidak ada cadangan yang dibuat sebelum langkah ini lulus.
3. `openRootSession`. Bendera harus `1 1`; bukan → C2.
4. Nama harus ada di `information_schema.schemata` (banding tanpa beda
   huruf besar-kecil, pakai nama persis dari server) dan bukan skema
   sistem.
5. Hitung tabel dasar dan perkiraan ukuran dari
   `information_schema.tables`; ruang kosong folder cadangan (`statfs`)
   harus lebih dari perkiraan itu.
6. Cadangan: berkas `<folder>/<nama aman>-<UTC yyyymmddThhmmssZ>.sql`
   dibuka `O_CREATE|O_EXCL|O_NOFOLLOW`, `0600` ("nama aman": karakter di
   luar `[A-Za-z0-9_.-]` menjadi `_`); sudah ada → C6 (tidak pernah
   menimpa, tidak pernah memilih nama lain). Folder `--ke` yang bukan
   milik root atau bisa ditulis grup/pengguna lain → baris C7 dicetak
   sebelum konfirmasi (tidak menolak). `mysqldump` lewat
   `s.dumpCommand` tanpa shell, dengan `--single-transaction --routines
   --triggers --events --set-gtid-purged=OFF --databases <nama>`; tanpa
   `--skip-comments`/`--compact`. Deskriptor berkas itu disimpan sampai
   perintah selesai.
7. Periksa lewat deskriptor yang sama (X5). **Bukti kelengkapan:** kode
   keluar 0 **dan** baris terakhir berawalan `-- Dump completed`.
   Pemeriksaan kewajaran: ada baris berawalan ``CREATE DATABASE /*!32312
   IF NOT EXISTS*/ `<nama, backtick digandakan>` ``; jumlah baris
   berawalan `CREATE TABLE ` di luar blok yang dibuka baris persis
   `DELIMITER ;;` dan ditutup baris persis `DELIMITER ;` **paling
   sedikit** jumlah tabel dasar; tidak ada blok yang masih terbuka di
   akhir berkas. Gagal satu → hapus berkas yang tidak lengkap, C8 dengan
   sebabnya, tidak ada yang dihapus dari MySQL. Database yang namanya
   memuat `'` atau `"` selalu berakhir di sini (`mysqldump` keluar 2).
   Lalu SHA-256 lewat deskriptor itu, `fsync` berkas dan `fsync`
   foldernya.
8. Tampilkan ringkasan (nama, jumlah tabel, berkas, ukuran, SHA-256,
   contoh `scp` dan `sha256sum`), semua nama lewat `printable`; lalu C9
   (C9b bila nama memuat karakter di luar `[A-Za-z0-9_.$-]`); lalu C10.
   Operator harus mengetik nama database persis; jawaban lain, juga
   `ya`, membatalkan.
9. `runLifted` dengan `recheck`: database masih ada dengan jumlah tabel
   dasar yang sama; `do`: `DROP DATABASE`. `errLockWait` → C12 dengan
   nama berkas cadangan, kode keluar bukan nol.
10. Tulis syslog. Cetak C15.

- [ ] **Step 1: Test dulu** (mysqld sungguhan lewat seam, pola
  `TestCLIApprove`):
  - `TestDropDatabaseHappyPath`: server `1 1`; sesudahnya database
    hilang, `gtid_executed` tidak berubah, `1 1`, nilai tersimpan ON,
    cadangan ada dan lulus pemeriksaan langkah 7, syslog satu baris.
  - `TestDropDatabaseTruncatedDumpStops`: `mysqldump` palsu yang berhenti
    di tengah — (a) keluar bukan 0; (b) keluar 0 tanpa baris penutup; (c)
    keluar 5 dengan baris `CREATE TABLE` lengkap tetapi tanpa baris
    penutup (bentuk disk penuh X5); (d) jumlah `CREATE TABLE` kurang →
    C8 persis, database masih ada, berkas tidak lengkap dihapus.
  - `TestDropDatabaseRoutineWithCreateTableLine`: procedure yang isinya
    punya baris berawalan `CREATE TABLE ` → pemeriksaan lulus (dihitung
    di luar blok, "paling sedikit").
  - `TestDropDatabaseNeverOverwrites`: berkas dengan nama yang sama sudah
    ada → C6 persis, berkas lama utuh, tidak ada berkas lain yang dibuat,
    tidak ada yang dihapus.
  - `TestDropDatabaseConfirmation`: C9 dan C10 selalu dicetak sebelum
    pertanyaan; jawaban `ya` → batal; nama persis → jalan; jawaban lain →
    batal, tidak ada yang berubah.
  - `TestDropDatabaseOddNames`: nama berspasi, ber-backtick, huruf besar,
    non-ASCII: dibuang dengan benar, nama berkas aman, petunjuk memakai
    C9b untuk nama di luar `[A-Za-z0-9_.$-]` dan C9 selain itu; nama
    bertanda kutip (`o'brien`, `dq"uote`) → C8, database masih ada.
  - `TestDropDatabaseErrorRelocks`: `DROP` gagal (kunci foreign key dari
    database lain) → akhir `1 1`, kode keluar bukan 0.
  - `TestDropDatabaseKilledMidway`: proses dimatikan paksa (SIGKILL)
    sesudah `super_read_only=OFF` → kunci berkas bebas; `GuardTick`
    berikutnya memasang `1 1` dan melapor (risiko tersisa, spek bagian
    8).
  - `TestDropDatabaseInterruptedRelocksItself`: SIGINT, SIGTERM dan
    SIGHUP selagi `DROP` menunggu → `1 1` dipasang perintah itu sendiri,
    C13, kode keluar bukan 0, kunci berkas bebas, `GuardTick` berikutnya
    tidak melapor apa pun.
  - `TestDropDatabaseBlockedByOpenTransaction`: sesi lain memegang
    transaksi terbuka yang membaca tabel database itu → sesudah batas
    `lock_wait_timeout` (disuntik lebih pendek): database masih ada,
    `1 1`, C12 persis dengan nama berkas, cadangan tetap ada, kode keluar
    bukan 0.
  - `TestDropDatabaseGivesUpAfterCap` (batas disuntik): C14, `1 1`.
  - `TestDropDatabaseRecheckAfterConfirmation`: selagi pertanyaan
    menunggu, (a) peran diganti `master`, (b) bendera dimatikan, (c)
    sebuah tabel ditambahkan → C11 persis, tidak ada yang dihapus, `SET
    GLOBAL super_read_only = OFF` tidak pernah terkirim.
  - `TestDropDatabaseSyncsBackupBeforeDrop` (lapisan berkas palsu):
    `fsync` berkas dan `fsync` folder tercatat sebelum pertanyaan
    dicetak; pemeriksaan dan SHA-256 memakai deskriptor yang sama (jalur
    tidak dibuka lagi).
  - `TestDropDatabaseWarnsOnSharedFolder`: folder `--ke` bisa ditulis
    grup → C7 dicetak sebelum pertanyaan; perintah tidak menolak.
  - `TestDropDatabaseNoSpace`: perkiraan lebih besar dari ruang kosong →
    tolak sebelum `mysqldump`.
  - `TestCLIRefusesWhileLoadHoldsLock`: gerbang agent memegang kunci
    (bendera `1 0`) → teks C3 (bukan C2), tidak ada cadangan yang dibuat,
    tidak ada yang berubah.
  - `TestCLIProbeRetriesShortly`: kunci dilepas dalam 3 detik → perintah
    jalan.
  - `TestCLIRefusesUnsafeLockFile`: berkas kunci mode `0644` → C4 dengan
    sebabnya, tidak ada yang berubah.
  - `TestDropDatabaseRefusals`: peran bukan `quarantined` (C1); skema
    sistem; nama tidak ada; bendera bukan `1 1` dengan kunci bebas (C2).
  - `TestRootSessionPasswordFallback`: socket ditolak → C5, masuk; kata
    sandi tidak muncul di stdout, stderr, syslog, argumen proses, atau
    berkas apa pun (direktori sementara diperiksa, juga sesudah
    pertanyaan dijawab dengan Ctrl-C); baris kata sandi dan baris
    konfirmasi yang diumpankan lewat satu aliran keduanya sampai.
  - `TestRootSessionOtherUser`: `--pengguna admin2` → login sebagai akun
    itu, pertanyaan C5b menyebut namanya.
  - `TestCLIOutputEscapesNames`: nama database yang memuat ESC, baris
    baru dan penanda arah → keluaran terminal tanpa karakter itu; baris
    syslog tetap satu baris dengan nama dalam bentuk `strconv.Quote`.
- [ ] **Step 2:** Pastikan gagal.
- [ ] **Step 3:** Tulis `cli_rootsql.go` (`promptReader`, `rootSession`,
  `probeLiftLock`, `runLifted`, `printable`), `cli_dropdb.go`; daftarkan
  `drop-database` di `RunCLIWith` dan teks bantuan (termasuk
  `--pengguna`). `dumpCommand` diperiksa dengan `mysqldump` sungguhan;
  bila klien menolak membaca opsi dari pipa, jalan mundurnya berkas
  sementara `0600` yang dihapus juga oleh penangan sinyal, dan itu
  dilaporkan.
- [ ] **Step 4:** Gerbang; commit
  `feat(agent): drop-database, a checked backup and drop on a quarantined server`.

<!-- MENUNGGU KEPUTUSAN PEMILIK: opsi --tanpa-cadangan. Bila opsi ini tidak
     diambil, hapus blok ini seluruhnya (sampai penanda AKHIR); tidak ada
     bagian lain Task 8 yang perlu diubah. -->

**MENUNGGU KEPUTUSAN PEMILIK — opsi `--tanpa-cadangan`** (spek bagian 7,
blok bertanda). Dikerjakan hanya bila pemilik menyetujuinya; sampai itu
Task 8 dibangun tanpa blok ini.

- Opsi `--tanpa-cadangan` diterima `mainDropDatabase` tetapi tidak
  ditulis di teks bantuan. Dengan blok ini, teks cadangan gagal (langkah
  7) memakai P1 menggantikan C8.
- Dengan opsi itu perintah tetap menjalankan langkah 5–7. Cadangan
  berhasil → jalan biasa. Cadangan gagal → tidak berhenti: C9/C9b
  dicetak, lalu pertanyaan P2 (nama harus diketik persis), lalu langkah
  9–10; baris syslog memuat `backup=none`.
- Teks (dipakai persis):
  - P1: `Cadangan database <nama> tidak bisa dibuat (<sebab>); tidak ada yang dihapus. Bila database itu memang tidak diperlukan lagi, jalankan: sudo dbpanel agent drop-database <nama> --tanpa-cadangan`
  - P2: `Cadangan database <nama> tidak bisa dibuat (<sebab>). Dengan --tanpa-cadangan database itu dihapus TANPA cadangan dan tidak bisa dikembalikan. Ketik nama database itu untuk melanjutkan, atau apa saja untuk membatalkan: `
- Test: `TestDropDatabaseWithoutBackup` (nama bertanda kutip + opsi +
  nama diketik → database hilang, `1 1`, syslog `backup=none`; jawaban
  lain → batal); `TestDropDatabaseWithoutBackupStillBacksUpWhenItCan`
  (cadangan bisa dibuat → P2 tidak muncul, cadangan ada);
  `TestFailedBackupTextNamesOption` (tanpa opsi, cadangan gagal → P1
  persis).

<!-- AKHIR blok MENUNGGU KEPUTUSAN PEMILIK: opsi --tanpa-cadangan -->

### Task 9: `grant-admin`, `reset-admin`, dan teks agent yang menunjuk perintah

**Files:**
- Create: `internal/agent/cli_admin.go`, `cli_admin_test.go`
- Modify: `internal/agent/cli.go`,
  `internal/agent/drivers/mysql/rebuild_load.go` (`errNotReplaced`,
  `errListIncomplete`), `quarantine.go` (pembuatan `errNotReplaced`),
  `connect.go`, `preflight.go`, `install.go` (ekspor pembuat akun admin)
- Test: `cli_admin_test.go`, `rebuild_load_internal_test.go`,
  `connect_local_test.go`, `preflight_test.go`, `rebuild_listing_test.go`

Task ini menjalankan juga gerbang panel; bila gerbang itu gagal hanya
karena sebuah test panel menyimpan salinan teks agent lama, harapan test
itu diperbarui di sini (perubahan test saja) dan didaftar di laporan.

**Interfaces:**
- Produces:
  - `func mainGrantAdmin(args []string, in io.Reader, stdout, stderr io.Writer, writeSyslog func(string) error) int`,
    `func mainResetAdmin(args []string, in io.Reader, stdout, stderr io.Writer, writeSyslog func(string) error) int`.
  - `func MissingAdminGrants(ctx context.Context, c *sql.Conn, account string) ([]string, error)`
    (paket mysql): `missingGrants` terhadap
    `requiredGrants(replicaGrants, <versi server>)`, ditambah
    `GRANT SHOW DATABASES ON *.* TO …` bila bukti daftar lengkap belum
    terpenuhi. `account` adalah `CURRENT_USER()` akun tersimpan.
  - `func StoredAccountName(ctx context.Context, h Host) (string, error)`:
    masuk sekali dengan akun tersimpan dan membaca `CURRENT_USER()`;
    tanpa akun tersimpan → `ErrNotConnected`; login ditolak → galat yang
    dikenali `LoginRefused`.
  - `func ResetAdmin(ctx context.Context, c *sql.Conn, creds CredentialStore) error`
    (isi `createAdmin` yang ada, memakai sambungan yang diberikan).
  - `errNotReplaced{source string; absent, present []string; named bool}`
    dengan teks T8a/T8b; pembuatnya di `settleRebuilt` memisah nama
    dengan `loadCopy.onSource`.
  - `preflightFacts.Quarantined bool`; tanda tangan menjadi
    `Preflight(ctx context.Context, h Host, p proto.PreflightParams, quarantined bool) (proto.PreflightResult, error)`,
    dan penangan `mysql.preflight` mengisinya dari `d.pins.roleErr()`
    (pin tidak terbaca dihitung dikarantina, seperti `mysql.connect`).
  - `func AdviceTexts() []string` (paket mysql, kode produksi): setiap
    teks saran agent task ini dan T7, T10, T11, T12 dengan tempat nilai
    diisi contoh, untuk test sensor panel (Task 14).
- Consumes: `openRootSession`, `probeLiftLock`, `runLifted`,
  `promptReader`, `printable` (Task 8); `loadCopy.onSource`,
  `loadCopy.full` (Task 6).

Kedua perintah (`[--pengguna <nama>] [--dir <dir>]`): pin `quarantined`
(selain itu C1); `probeLiftLock` (C3/C4); sesi root; bendera `1 1` (C2);
tampilkan pernyataan yang akan dijalankan (nama lewat `printable`); `ya`;
`runLifted` dengan `recheck` = pernyataan yang dihitung ulang masih sama
dengan yang ditampilkan (selain itu C11), `do` = pernyataannya pada satu
sambungan; syslog.
- `grant-admin`: akun = `StoredAccountName` (akun seperti MySQL
  mengenalnya, bukan host yang tersimpan); tanpa akun tersimpan → C16;
  login akun tersimpan ditolak → C17; tidak ada yang kurang → C18 dan
  keluar 0.
- `reset-admin`: `ResetAdmin`; akhirnya C19.

Teks CLI (dipakai persis):
- C16: `agent belum punya akun MySQL tersimpan; jalankan sudo dbpanel agent reset-admin`
- C17: `akun MySQL yang disimpan agent tidak bisa masuk; jalankan sudo dbpanel agent reset-admin`
- C18: `Akun <akun> sudah punya semua hak yang dibutuhkan.`
- C19: `Akun dbpanel_admin@localhost dibuat ulang dan disimpan sebagai akun agent. Di panel, klik Segarkan di halaman server ini.`

Teks agent (dipakai persis):
- T8a `errNotReplaced` sesudah dump penuh; kalimat untuk kelompok yang
  kosong tidak ditulis; tiap daftar paling banyak `maxNamedLeftovers`
  nama lalu `, …`, dan daftar dipotong lebih pendek lagi bila perlu
  supaya seluruh teks tidak lebih dari 800 byte (label panel dan judul
  langkah memakai sisanya dari batas baris 1024): `server ini masih punya database yang tidak disalin dari sumber %s; karantina tidak diakhiri. Yang tidak ada di master baru (%s): jalankan di server ini sudo dbpanel agent drop-database <nama> untuk tiap nama, lalu tekan "Ulangi dari tahap ini" pada pekerjaan di panel. Yang ada di master baru (%s): mulai lagi Bangun ulang sebagai replika dari panel; panel menghapus dan menyalinnya ulang.`
- T8b `errNotReplaced` sesudah dump bernama (daftar di akhir, sesudah
  saran): `server ini masih punya database yang tidak disalin dari sumber %s; karantina tidak diakhiri. Bila database itu tidak ada di master baru: jalankan di server ini sudo dbpanel agent drop-database <nama> untuk tiap nama, lalu tekan "Ulangi dari tahap ini" pada pekerjaan di panel. Bila ada di master baru: mulai lagi Bangun ulang sebagai replika dari panel; panel menghapus dan menyalinnya ulang. Database itu: %s.`
- T13, ditambahkan di akhir rincian pemeriksaan "dedicated" bila
  `Quarantined`: `Untuk tiap database itu jalankan di server ini: sudo dbpanel agent drop-database <nama> (perintah itu membuat cadangan dulu), lalu tekan "Ulangi dari tahap ini".`
- T14, rincian pemeriksaan "grants" bila `Quarantined` (tanpa `FixSQL`):
  `akun %s belum punya semua hak yang dibutuhkan. Jalankan di server ini: sudo dbpanel agent grant-admin, lalu tekan "Ulangi dari tahap ini" pada pekerjaan di panel.`
- T15 `mysql.connect` di server yang dikarantina, akun kurang hak (tanpa
  daftar `GRANT`): `server ini dikarantina (quarantined): akun %s belum punya semua hak yang dibutuhkan, jadi tidak disimpan. Jalankan di server ini: sudo dbpanel agent reset-admin (membuat akun dbpanel_admin dan menyimpannya sebagai akun agent), lalu klik Segarkan di halaman server ini. Hubungkan MySQL tidak perlu diulang.`
- T16 `errListIncomplete` (kurang hak): `daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL %s yang dipakai agent tidak punya hak SHOW DATABASES. Jalankan di server ini: sudo dbpanel agent grant-admin, lalu tekan "Ulangi dari tahap ini" pada pekerjaan di panel.`
- T17 `errListIncomplete` (nama tidak didukung): `daftar database di server ini tidak bisa dipastikan lengkap: nama akun MySQL yang dipakai agent memuat karakter yang tidak didukung pemeriksaan ini (yang didukung: huruf, angka, garis bawah, titik, tanda hubung, dan tanda dolar). Jalankan di server ini: sudo dbpanel agent reset-admin, lalu tekan "Ulangi dari tahap ini" pada pekerjaan di panel.`

- [ ] **Step 1: Test dulu:**
  - `TestGrantAdminAddsMissing`: akun tanpa `SHOW DATABASES` dan tanpa
    satu hak dinamis → sesudahnya `mysql.connect` menerima, bukti daftar
    lulus, `gtid_executed` tidak berubah, `1 1`.
  - `TestGrantAdminCoversReplicaPreflight`: akun tersimpan memegang
    kumpulan hak `mysql.connect` tetapi tidak hak data → sesudah perintah,
    pemeriksaan "grants" `mysql.preflight` peran replika berstatus OK.
  - `TestGrantAdminNamesAccountAsMySQLKnowsIt`: akun `'x'@'%'` yang
    tersimpan dengan host `127.0.0.1` → pernyataan yang ditampilkan dan
    dijalankan berbunyi `… TO 'x'@'%'`.
  - `TestGrantAdminStoredLoginRefused`: C17 persis, tidak ada yang
    berubah. `TestGrantAdminNothingMissing`: C18, keluar 0.
  - `TestGrantAdminFailingGrantRelocks`: `GRANT` gagal → akhir `1 1`.
  - `TestResetAdminStoresAccount`: akun tersimpan berganti, login dengan
    akun itu berhasil, akun lama tidak dihapus, C19.
  - `TestAdminCommandsRefusals`: peran (C1), kunci dipegang (C3), bendera
    (C2).
  - `TestAdminCommandsRecheckAfterConfirmation`: peran atau bendera
    berubah selagi pertanyaan menunggu → C11, tidak ada pernyataan.
  - `TestAdminCommandsKeepSqlLogBinOff`: dengan kumpulan sambungan lebih
    dari satu, semua pernyataan perintah berjalan pada satu sambungan;
    `gtid_executed` dan log biner tidak berubah.
  - `TestAdminCommandInterruptedRelocksItself`: SIGINT di tengah → `1 1`,
    C13.
  - Teks: `TestListIncompleteText` (T16), `TestListUnsupportedAccountText`
    (T17), `TestNotReplacedTextSplitsByNewMaster` (dump penuh: nama di
    daftar master baru masuk kalimat "Yang ada", yang tidak masuk "Yang
    tidak ada"; satu kelompok kosong → kalimatnya tidak ada),
    `TestNotReplacedTextNamedDump` (T8b),
    `TestNotReplacedTextAdviceSurvivesCap` (20 nama 64 karakter: teks
    paling banyak 800 byte, kedua saran utuh, daftar yang dipotong
    dengan `, …`), `TestDedicatedCheckPointsToCommandOnQuarantined` (T13 hanya
    bila dikarantina), `TestGrantsCheckOnQuarantinedHasNoFixSQL` (T14,
    `FixSQL` kosong; peran lain seperti sekarang),
    `TestConnectWeakAccountOnQuarantinedServer` diperbarui ke T15,
    `TestConnectRefusalAdviceWorks` (connect ditolak → `reset-admin` →
    `mysql.inventory` tanpa hak yang kurang).
  - `TestRebuildPartialRevokeAccount` dan
    `TestRebuildRoleOnlyAccountAtSettle` memberi hak lewat
    `MissingAdminGrants` + jalur CLI, bukan heredoc.
- [ ] **Step 2:** Pastikan gagal.
- [ ] **Step 3:** Implementasi; daftarkan kedua subperintah; `AdviceTexts`.
- [ ] **Step 4:** Gerbang; commit
  `feat(agent): grant-admin and reset-admin; refusals point to the commands`.

### Task 10: Butir kecil agent

**Files:**
- Modify: `internal/agent/drivers/mysql/salvage.go`, `leftover.go`,
  `rebuild_load.go` (`completeUserDatabases`), `replication.go`,
  `quarantine.go`

Teks (dipakai persis):
- T18: `sumber kanal replikasi di server ini (%s) bukan sumber yang dipin (%s); perintah %s ditolak`

- [ ] **Step 1: Test dulu:**
  - `TestLeftoverSetStampedWithSource`: berkas `leftover-gtid` memuat
    baris pertama `source <host:port>`; `mysql.salvage` dengan pin sumber
    lain → ditolak dengan teks "Periksa data tertinggal dulu" yang ada.
  - `TestLeftoverSetWithoutSourceIsNoRecord`: berkas bentuk lama (tanpa
    baris sumber) → dianggap tidak ada.
  - `TestSettleRemovesLeftoverSet`.
  - `TestListingProofSessionSettings`: sesi bukti menjalankan `SET
    SESSION sql_quote_show_create = ON, sql_mode = ''` (bukti M4) pada
    sambungan yang sama sebelum `SHOW GRANTS`; dengan
    `sql_quote_show_create=OFF` global, akun ber-`SHOW DATABASES` tetap
    terbukti.
  - `TestStartReplicaRefusesForeignChannelOnQuarantined`: di server
    `quarantined` dengan persetujuan, kanal replikasi yang sumbernya bukan
    pin sumber → `mysql.startReplica` dan `mysql.startUntil` menolak
    dengan teks T18.
- [ ] **Step 2:** Pastikan gagal. **Step 3:** Implementasi (cap sumber di
  `recordLeftover`/`readLeftoverSet`; hapus di `settleRebuilt`; `SET
  SESSION` di `completeUserDatabases`; pembanding sumber dari
  `performance_schema.replication_connection_configuration`).
- [ ] **Step 4:** Gerbang; commit `fix(agent): leftover set source stamp, proof session settings, channel source check`.

### Task 11: Panel — aturan satu keadaan di orchestrator

Aturan (spek 5.1): **peran `quarantined` berarti karantina belum
berakhir.** Peran itu tidak diubah oleh gagal, `Resume`, `Retry` maupun
`Approve`; hanya settle yang menggantinya.

**Files:**
- Modify: `internal/panel/orchestrator/runner.go`, `create_replica.go`,
  `approval.go`, `controls.go`, `resync.go`;
  `internal/panel/store/topologies.go`, `jobs.go`
- Test: `failed_rebuild_test.go`, `approval_test.go`, `superseded_test.go`,
  `controls_test.go`, `store/*_test.go`

**Interfaces:**
- Produces:
  - `func (s *Store) AddReplica(topologyID, serverID int64, databases []string) (Replica, error)`
    (tanda tangan tetap): peran `quarantined` tidak diubah (baris
    ditambahkan saja); peran lain seperti sekarang.
  - `func (s *Store) RemoveReplica(serverID int64) error` (tanda tangan
    tetap): peran `quarantined` tidak diubah; peran lain menjadi
    `unassigned` seperti sekarang.
  - `func (s *Store) SettleRebuild(jobID, paramsJSON string, serverID int64) error`:
    satu transaksi — parameter job (yang memuat `QuarantineEnded`),
    penghapusan tanda master lama yang menunjuk `serverID`, dan peran
    `replica`. Aman diulang.
  - Seam test `Runner.settleStore func(jobID, paramsJSON string, serverID int64) error`,
    menggantikan `settleSave` dan `settleClear`.
  - `func (r *Runner) pendingRebuild(j store.Job, st jobState) (bool, error)`
    (empat syarat spek 5.1: `KindCreateReplica` dengan `RebuildOldMaster`;
    `!QuarantineEnded`; baris server itu gagal; server itu masih master
    lama topologi job).
  - `func (s *Store) NewerJobExistsExcept(jobID string, serverID int64, kinds ...string) (bool, error)`
    (urutan parameter seperti `NewerJobExists`).
  - `func (r *Runner) normalizeFailedRebuilds() error`.
  - `func (r *Runner) cancelAgentCopy(ctx context.Context, serverID int64)`:
    `job.cancel` usaha terbaik untuk `AgentJobID` yang tersimpan pada job
    `create_replica` terbaru server itu (seperti `cancelJob`); galatnya
    hanya dicatat di log.
  - `AwaitingRebuild(st *store.Store, srv store.Server) (bool, error)`
    tetap tanda tangannya; isinya `srv.Role == store.RoleQuarantined`.
- Removes: `Store.ReturnToQuarantine`, `store.ErrNotFailedRebuild`,
  `Runner.CancelRebuildAttempt`, `latestRebuildEnded`,
  `errNotFailedRebuild`, seam `settleSave`/`settleClear`,
  `Runner.keepQuarantined` dan penulisan `SetServerRole(…,
  RoleQuarantined)` di `StartCreateReplica` (tidak ada lagi yang perlu
  dikembalikan).

- [ ] **Step 1: Test dulu** (RED; satu test per baris tabel spek 5.1):
  - `TestRebuildKeepsRoleQuarantinedUntilSettle`: `StartCreateReplica`
    (bangun ulang) → peran `quarantined`, baris `creating`, tanda ada;
    sampai dan sesudah langkah Salin data peran tetap `quarantined`;
    sesudah `mysql.configureReplication` menjawab OK → peran `replica`,
    `QuarantineEnded` tersimpan, tanda hilang.
  - `TestSettleStoreFails` (menggantikan `TestSettleSaveFails` dan
    `TestSettleClearFails`): seam mengembalikan galat → langkah gagal
    dengan `simpan keadaan pekerjaan: …`; di store peran `quarantined`,
    tanda ada, bendera belum terpasang (ketiganya tidak berubah); `Retry`
    → ketiganya terpasang.
  - `TestStoreSettleRebuildIsOneTransaction` (paket store): galat yang
    disuntik di tulisan ketiga → tidak ada dari ketiganya yang berubah;
    dipanggil dua kali → hasil sama.
  - `TestAddAndRemoveReplicaKeepQuarantinedRole` (paket store): server
    `quarantined` → sesudah `AddReplica` dan sesudah `RemoveReplica` peran
    tetap `quarantined`; server `unassigned` → `replica` lalu
    `unassigned` seperti sekarang.
  - `TestFailedRebuildStaysQuarantined`: gagal sebelum salinan, di
    salinan, dan di sambung sebelum jawaban settle → peran `quarantined`,
    baris gagal, tanda ada, `AwaitingRebuild` benar.
  - `TestEveryFailurePathLeavesQuarantinedWithFailedRow`: bangun ulang
    digagalkan lewat setiap jalur yang menulis `JobFailed` untuk job
    `create_replica` — galat langkah (`fail`), panel dimulai ulang
    (`Resume`), job yang tidak bisa dijalankan (`failUnrunnable`),
    peluncuran yang ditolak di `Retry` — dan sesudah masing-masing: peran
    `quarantined`, baris gagal, tanda ada.
  - `TestResumeFailsRebuildRowBeforeJob`: job bangun ulang berjalan saat
    panel berhenti → sesudah `Resume` baris gagal dan job gagal; `Retry`
    yang datang bersamaan tidak pernah melihat job gagal dengan baris
    `creating` (`Resume` memegang `startMu` dan menulis baris lebih
    dulu).
  - `TestFailedRebuildAfterSettleStaysReplica` (yang ada, tetap).
  - `TestNormalizeLegacyFailedRebuilds`: baris lama (peran `replica` +
    gagal + tanda) → `quarantined`; yang job terbarunya berbendera →
    tanda dihapus, peran tetap `replica`; job tidak ada atau tidak
    terbaca → `quarantined`; dijalankan dua kali hasilnya sama; baris
    replika gagal biasa (bukan master lama) tidak disentuh.
  - `TestRetryFromQuarantinedKeepsRole`: `Retry` job bangun ulang yang
    gagal → job berjalan, peran tetap `quarantined`; peluncuran gagal →
    job gagal lagi, baris gagal, peran `quarantined`.
  - `TestApproveWaitingRebuildWithQuarantinedRole`: job bangun ulang yang
    menunggu (baris `creating`, peran `quarantined`) → `Approve`
    diterima.
  - `TestSupersededRequiresQuarantinedWhileUnsettled`: job bangun ulang
    tanpa bendera dengan peran `replica` (dibuat tangan) → "peran server
    ini sudah berubah"; job berbendera dengan peran `replica` →
    diterima; job `create_replica` biasa dengan peran `quarantined` →
    ditolak.
  - `TestRetryStillRefusedWhenSuperseded`: rebuild baru, job sertifikat,
    master berganti → tetap ditolak, peran tidak berubah.
  - `TestLeftoverJobDoesNotSupersedeFailedRebuild`: sesudah Periksa dan
    Amankan data tertinggal, `Retry` job bangun ulang diterima; untuk job
    non-rebuild atau yang berbendera, job Periksa tetap menggantikan.
  - `TestQuarantinedWithRowRefusesResyncDetachCreate`: Resync, Lepas (dua
    mode), Buat replika biasa → `ErrAwaitingRebuild`, tidak ada perintah
    agent, tidak ada perubahan store.
  - `TestLeftoverRefusedWhileRebuildRuns`: selagi job bangun ulang
    berjalan atau menunggu, Periksa dan Amankan ditolak karena server
    sibuk.
  - `TestCancelRunningRebuildStaysQuarantined`: Batalkan job yang
    berjalan → baris hilang, peran `quarantined` (tidak pernah
    `unassigned`), `mysql.releaseRole` tidak dikirim.
  - `TestDeleteQuarantinedWithFailedRow`: `job.cancel` dikirim untuk job
    agent yang tersimpan (kegagalannya tidak menghentikan hapus); hapus
    berhasil, baris ikut terhapus, master baru bisa dihapus sesudah
    replika lain dilepas.
  - `TestNewRebuildFromQuarantinedWithRow`: `job.cancel` dikirim lebih
    dulu; bangun ulang baru berjalan sampai selesai; job lama
    digantikan; peran tidak pernah meninggalkan `quarantined` sebelum
    settle.
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
    `TestDeleteServerRefusesMasterAndReplica`,
    `TestRetryConnectHealsQuarantineEnded` (tetap: Ulangi memulihkan
    jawaban settle yang hilang; kini juga menegaskan peran `replica`
    sesudahnya).
- [ ] **Step 2:** Pastikan gagal.
- [ ] **Step 3:** Store: `AddReplica`, `RemoveReplica`, `SettleRebuild`,
  `NewerJobExistsExcept`; hapus `ReturnToQuarantine`.
- [ ] **Step 4:** `replicaConnect`: blok settle memanggil satu
  `SettleRebuild` (lewat seam) pada setiap lewatan sesudah agent menjawab
  OK; galat → bendera di memori dikembalikan dan langkah gagal dengan
  `simpan keadaan pekerjaan: …`.
- [ ] **Step 5:** Jalur gagal: `failUnrunnable` menandai baris gagal untuk
  job `create_replica` sebelum menulis status job; `Resume` memegang
  `startMu` dan, untuk job yang menyalin replika, menulis baris sebelum
  status job, sesudah memanggil `normalizeFailedRebuilds`. `fail` tidak
  berubah (sudah menulis baris lebih dulu). Tidak ada penulisan peran di
  jalur mana pun.
- [ ] **Step 6:** `supersededBy`: untuk job dengan `RebuildOldMaster` dan
  `!QuarantineEnded` peran harus `quarantined`, selain itu aturan peran
  `replica` yang ada; pemeriksaan "job lebih baru" memakai
  `NewerJobExistsExcept(…, KindLeftover, KindSalvage)` bila
  `pendingRebuild`. `Retry` dan `Approve` tidak menyentuh peran.
- [ ] **Step 7:** `refuseAwaitingRebuild` tanpa gerbang peran;
  `deletableLocked` membolehkan baris bila peran `quarantined`;
  `deleteServer` dan cabang bangun ulang `StartCreateReplica` memanggil
  `cancelAgentCopy` lebih dulu; hapus fungsi-fungsi Batalkan di store dan
  orchestrator, `keepQuarantined`, dan kedua seam lama.
- [ ] **Step 8:** Gerbang panel dan `make test` (hapus pemakaian
  `CancelRebuildAttempt` di web dalam task ini sebatas supaya paket
  terkompilasi; tampilan dirapikan di Task 12). Commit
  `feat(panel): an old master stays quarantined until its agent ends the quarantine`.

### Task 12: Panel — monitor, kotak, form, daftar, dialog

**Files:**
- Modify: `internal/panel/monitor/state.go`, `start_state.go`,
  `promote.go`, `alerts.go`, `monitor.go`;
  `internal/panel/notify/message.go` (`kindNames`);
  `internal/panel/web/handlers_oldmaster.go`, `handlers_mysql.go`,
  `handlers_servers.go`, `handlers_replication.go`, `handlers_jobs.go`,
  `handlers_alerts.go` (`alertKindLabels`), `router.go`,
  `handlers_confirm.go`, `audit_summary.go`, `render.go`;
  `web/templates/server_detail.html`, `replica_detail.html`,
  `job_detail.html`, `_ui.html`
- Test: `monitor/alerts_test.go`, `monitor/state_test.go`,
  `web/web_failed_rebuild_test.go`, `web/web_oldmaster_test.go`

Tidak ada pemulihan dari laporan agent: task ini tidak menambah panggilan
balik dari monitor ke orchestrator dan tidak mengubah peran karena sebuah
laporan (lihat "Perubahan sesudah tinjauan rancangan", R-17/R-19).

**Interfaces:**
- Consumes: Task 11; `proto.StartKindState.While`,
  `proto.QuarantineWhileRunning`, `proto.QuarantineWhileUnverified`
  (Task 4, 5).
- Produces:
  - `monitor.Deps.CopyStarted func(serverID int64) (bool, error)` (diisi
    saat panel dirangkai dengan
    `orchestrator.RebuildCopyState(...) == CopyStarted`; nil = tidak
    pernah).
  - `oldMasterBox.RebuildJobID string` (job bangun ulang yang berjalan
    atau menunggu; kotak lalu hanya menampilkan T22 dan tautannya),
    `oldMasterBox.AgentRole string` (pin peran yang dilaporkan agent bila
    bukan `quarantined` dan tidak kosong), `oldMasterBox.HasRow bool`;
    `oldMasterBox.RebuildFailed` dihapus.
  - `func (a *app) leftoverWarnings(srv store.Server) ([]string, error)`:
    peringatan data tertinggal yang kini disusun di dalam `rebuildTarget`
    (ditemukan dan belum diamankan; belum diperiksa; sempat bisa ditulis
    sejak pemeriksaan terakhir; keadaan salinan), dipakai `rebuildTarget`
    dan dialog; `retryView.Warnings []string`.

Teks (dipakai persis):
- T1 `startRelockedText`: `MySQL master lama sempat tidak terkunci selagi berjalan; agent sudah menguncinya lagi. Tulisan yang masuk selama itu tidak ada di master baru: jalankan Periksa data tertinggal`
- T1b `startUnverifiedText`: `Agent tidak bisa memeriksa atau memasang lagi kunci baca-saja master lama ini (login atau hak akun MySQL agent ditolak, atau MySQL tidak menjawab perintahnya); master lama mungkin bisa ditulis. Jalankan di server itu: sudo dbpanel agent reset-admin. Sesudah itu jalankan Periksa data tertinggal`
- T2 `copyStartedCloseReason`: `bangun ulang sudah mulai mengganti data server ini; tulisan itu tidak bisa diperiksa lagi`
- T2b `startLateCopiedText` (keadaan `CopyStarted`, untuk keterangan kosong dan `running`): `master lama sempat bisa ditulis; bangun ulang sudah mulai mengganti data server ini, jadi tulisan itu tidak bisa diperiksa lagi. Bila agent lalu menolak mengakhiri karantina, mulai lagi Bangun ulang sebagai replika; atau hapus server ini`
- T3 kotak, server tanpa baris replika: `Agent server ini melaporkan peran %s, bukan karantina. Mulai lagi Bangun ulang sebagai replika, atau hapus server ini.`
- T3b kotak, server dengan baris replika: `Agent server ini melaporkan peran %s, bukan karantina. Mulai lagi Bangun ulang sebagai replika, hapus server ini, atau tekan "Ulangi dari tahap ini" pada pekerjaan bangun ulangnya.`
- T20 label jenis `master_start_late` di `alertKindLabels`, dan di `kindNames` dengan huruf awal kecil seperti entri lain di sana: `Master lama sempat bisa ditulis`
- T22 kotak selagi bangun ulang berjalan atau menunggu, diikuti tautan "Lihat pekerjaan": `Bangun ulang sebagai replika sedang berjalan.`
- T23 halaman replika server berperan `quarantined`, diikuti tautan ke halaman server: `Server ini master lama yang dikarantina. Tindakannya ada di halaman server.`
- T25 `startLateNoTopologyText`: `master lama sempat bisa ditulis; topologi ini sudah promote lagi, jadi tulisan itu tidak bisa diperiksa lagi dari panel. Hapus server ini`
- T26 `noTopologyCloseReason`: `topologi ini sudah promote lagi; tulisan itu tidak bisa diperiksa lagi dari panel`

Aturan pesan dan penutupan `master_start_late` untuk server berperan
`quarantined`:
- Pesan saat dibuka: keterangan `unverified` → T1b, selalu; selain itu,
  bila tidak ada topologi yang menamai server itu sebagai master lamanya
  → T25; bila `CopyStarted` → T2b; keterangan `running` → T1; tanpa
  keterangan → teks lama (`startLateText`).
- Penutupan (di samping yang ada): tidak ada topologi yang menamainya →
  alasan T26; `CopyStarted` → alasan T2. Keduanya **tidak** berlaku
  selagi entri `late` terbaru dalam laporan server itu berketerangan
  `unverified`.
- Peringatan yang dibuka dalam keadaan `CopyStarted` ditutup pada putaran
  monitor berikutnya: satu pemberitahuan Kritis dan satu pemberitahuan
  tutup per kejadian, disengaja.
- Peringatan yang sudah terbuka dengan pesan lain berganti pesan menjadi
  T1b saat entri `late` yang lebih baru berketerangan `unverified`.

- [ ] **Step 1: Test dulu:**
  - `TestResolveQuarantinedWithRow`: peran `quarantined` + baris
    (`creating`, dan gagal) → dinilai master lama:
    `master_start_unguarded` terbuka dari laporan tanpa kunci start;
    status `DeriveQuarantined`; aturan replika tertutup dengan alasan
    "server menjadi master lama".
  - `TestStartResultsJudgedWhileRebuildRunning`: peran `quarantined`,
    baris `creating`, job berjalan; laporan memuat jenis `late` →
    peringatan terbuka.
  - `TestRelockedOpensLateAlertWithItsText`: jenis `late` dengan
    `While == "running"` → pesan T1; tanpa `While` → teks lama.
  - `TestUnverifiedOpensLateAlert`: `While == "unverified"` → pesan T1b
    persis.
  - `TestUnverifiedReplacesOpenLateMessage`: peringatan terbuka dengan
    pesan T1; laporan berikutnya memuat entri `late` ber-`unverified`
    dengan id baru → peringatan yang sama kini berpesan T1b (tidak
    dibuka dua kali).
  - `TestUnverifiedNotClosedByCopyStarted`: `CopyStarted` dan keterangan
    `unverified` → pesan T1b, peringatan tidak ditutup.
  - `TestLateAlertFollowsCopyState`: `CopyStarted` → pesan T2b;
    peringatan terbuka ditutup dengan alasan T2; `CopyUncertain` dan
    `CopyNotStarted` → tidak ditutup.
  - `TestRelockAfterLeftoverCheckOpensAgain`: kejadian pada t0 →
    peringatan; Periksa data tertinggal pada t0+20 detik menutupnya;
    kejadian kedua pada t0+40 detik (id baru di laporan) → terbuka lagi.
  - `TestLateAlertAfterSecondPromote`: server `quarantined` yang tidak
    lagi dinamai topologi mana pun: peringatan terbuka ditutup dengan
    alasan T26; entri `late` baru dibuka dengan pesan T25.
  - `TestStartLateSurvivesRebuildUntilCopied` diperbarui: peran kini
    `quarantined` selama bangun ulang; lengan "peran replika yang belum
    dinilai" tidak ada lagi.
  - Web `TestQuarantinedWithRowBox`: kotak tampil dengan Hapus, tautan
    Bangun ulang, Periksa/Amankan (`CopyNotStarted`), kalimat tidak pasti
    (`CopyUncertain`), kalimat salinan-mulai tanpa tombol (`CopyStarted`);
    tidak ada tombol atau kalimat Batalkan di mana pun
    (`rebuild-attempt/cancel` → 404).
  - `TestBoxShowsRunningRebuild`: job bangun ulang berjalan, dan menunggu
    → kotak memuat T22 dan tautan ke halaman job itu; tidak ada tombol
    Hapus, Periksa, Amankan atau Bangun ulang.
  - `TestRebuildFormFromQuarantinedWithRow`: form 200; peringatan mengikuti
    keadaan salinan; tidak ada kalimat "percobaan masih tercatat".
  - `TestReplicaListsLeaveOutQuarantined`: dasbor, halaman replikasi dan
    halaman master tidak menampilkan server `quarantined` sebagai kartu
    replika, baik barisnya `creating` maupun gagal.
  - `TestReplicaPageOfQuarantinedServer`: halaman replika server
    `quarantined` (dengan dan tanpa topologi yang menamainya) tidak
    menawarkan Resync maupun Lepas; memuat T23 dan tautan ke halaman
    server.
  - `TestJobPageOffersRetryFromQuarantined`: "Ulangi dari tahap ini"
    tampil; sesudah bangun ulang baru, tidak.
  - `TestRetryDialogShowsLeftoverWarnings`: bangun ulang gagal sebelum
    salinan, Periksa menemukan 214 transaksi → dialog Ulangi dan dialog
    Setujui memuat baris peringatan yang sama dengan form (jumlah
    transaksi; "belum diperiksa" bila belum; "sempat bisa ditulis" bila
    `master_start_late` terbuka).
  - `TestBoxSaysAgentReportsOtherRole`: server `quarantined` tanpa baris
    dengan laporan `RolePin == "replica"` → T3 dengan `replica`; dengan
    baris gagal → T3b; `RolePin == "master"` → T3 dengan `master`;
    `RolePin` `quarantined` atau kosong → tidak ada kalimat.
  - `TestStartLateLabel`: label T20 di halaman peringatan dan di
    pemberitahuan.
  - Test yang gugur: `TestCancelRebuildAttemptOffered`,
    `TestCancelRebuildAttemptPost`; yang berubah harapannya diperbarui dan
    didaftar: `TestFailedRebuildOffersRebuildAgain`,
    `TestFailedRebuildReplicaPage`, `TestFailedRebuildJobPage`,
    `TestLeftoverWordingAfterFailedRebuild`, `wantStartLateText`.
- [ ] **Step 2:** Pastikan gagal.
- [ ] **Step 3:** Monitor: `resolve` membaca server sesudah pemeriksaan
  master dan sebelum baris replika; `closeStartAlerts` kehilangan lengan
  "peran replika yang belum dinilai" dan mendapat dua penutupan di atas;
  `startKindAlert` memilih pesan menurut aturan di atas; `Deps.CopyStarted`
  dirangkai di tempat monitor dibuat. Tidak ada hitungan pin dan tidak
  ada panggilan ke orchestrator.
- [ ] **Step 4:** Web: `oldMasterBox` tanpa `RebuildFailed`, dengan
  `RebuildJobID`, `AgentRole`, `HasRow`; `leftoverWarnings` dipakai
  `rebuildTarget`, `retryView` dan dialog Setujui; halaman replika menurut
  peran; daftar replika; label T20; hapus rute, handler, aksi konfirmasi,
  label audit, flash, partial, tiga teks Batalkan dan `msgAttemptPending`.
  `bash scripts/tailwind.sh`.
- [ ] **Step 5:** Gerbang panel, `make test`; commit
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
- Consumes: Task 11 (peran `quarantined` dengan baris), Task 12 (halaman
  replika dan peringatan untuk server tanpa topologi).

Teks (dipakai persis):
- T4: `master lama %s dari promote sebelumnya belum dibangun ulang atau dihapus; selesaikan itu dulu`
- T5: `Master lama %s dari promote sebelumnya belum dibangun ulang. Sesudah promote ini, server itu tidak bisa lagi diperiksa atau dibangun ulang dari panel; yang tersisa hanya Hapus server.`
- T6: `Topologi ini sudah promote lagi sejak server ini dikarantina. Data tertinggal dan Bangun ulang tidak tersedia lagi untuk server ini. Hapus server ini dari panel, lalu pasang lagi sebagai replika baru bila masih dipakai.`
- T24 alasan kandidat di layar promote untuk server berperan `quarantined` yang punya baris replika (menggantikan "Replika gagal" / "Replika sedang dibuat"): `Master lama yang dikarantina`

- [ ] **Step 1: Test dulu:** `TestPlannedPromoteRefusedWhileOldMasterPending`
  (tanda master lama menunjuk server yang masih ada dan `quarantined`,
  dengan atau tanpa baris → T4, tidak ada perintah agent);
  `TestEmergencyPromoteNotRefused` (jalan; `PendingOldMaster` terisi;
  layar memuat T5); `TestPromoteScreenNamesQuarantinedCandidate` (server
  `quarantined` dengan baris gagal tampil dengan alasan T24 dan tidak
  bisa dipilih); `TestOldMasterAfterSecondPromote` (sesudah promote
  darurat kedua: kotak server pertama memuat T6, hanya Hapus; Periksa
  ditolak dengan teks yang ada; Ulangi ditolak dengan alasan "master
  topologi ini sudah berganti sejak pekerjaan ini dibuat" — peran server
  itu tetap `quarantined`, jadi pemeriksaan peran lulus dan pemeriksaan
  master yang menolak; hapus berhasil).
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
  `internal/panel/hub/hub.go`, `conn_ws.go`;
  `internal/panel/redact/redact.go`;
  `internal/panel/web/handlers_*.go` (flash, `shownError`),
  `web/templates/server_detail.html` (spanduk kurang hak)
- Test: `orchestrator/agenttext_test.go` (baru), `hub/hub_test.go`,
  `redact/phrases_test.go`, `orchestrator/copy_state_test.go`, test web
  flash

**Interfaces:**
- Produces:
  - `type AgentError struct{ Server int64; Err *proto.Error }` (paket
    hub) dengan `Error() string` (= `Err.Message`) dan `Unwrap() error`
    (= `Err`, jadi pemeriksaan kode yang ada lewat `errors.As`/`errors.Is`
    tetap bekerja). Setiap kegagalan **yang dilaporkan agent** dibungkus
    tipe ini (di `readResponse`; `Server` diisi `Hub.Call`). Galat buatan
    hub tidak dibungkus: `timeoutError()`, `offlineError()`, termasuk
    `offlineError()` yang `WSConn.stop` berikan kepada pemanggil yang
    sedang menunggu (yang sekarang sampai lewat `readResponse` dan karena
    itu harus dibedakan dari jawaban agent sebelum dibungkus).
  - `func fromAgent(err error) (text string, ok bool)`:
    `errors.As(err, *hub.AgentError)`; asal ditentukan dari tipe, tidak
    pernah dari kode.
  - `func AgentQuoteN(server, text string, limit int) string`
    (`AgentQuote` memanggilnya dengan `maxAgentQuote`).
  - `type agentTextError struct{ server, text string }` (`Error()` =
    `AgentQuoteN(server, text, maxLogLine)`).
- Consumes: `mysql.AdviceTexts()` (Task 9).

Teks (dipakai persis):
- T9: `database berikut di master lama ini akan dihapus lalu disalin ulang dari master baru: %s`
- T21 spanduk di halaman server berperan `quarantined`: `Akun admin MySQL ini kurang hak. Jalankan di server ini: sudo dbpanel agent grant-admin, lalu klik Segarkan.`

- [ ] **Step 1: Test dulu:**
  - `TestJobErrorLabelsAgentText`: langkah gagal dengan galat agent →
    galat job, pesan langkah dan galat terakhir replika =
    `<judul>: Pesan dari agent <server> (bukan dari panel): <teks>`;
    teks multi-baris dan penanda arah diratakan; teks 600 karakter tidak
    dipotong di 300; galat batas waktu dan agent terputus tidak dilabeli.
  - `TestAgentErrorWithPanelCodeIsLabelled`: agent menjawab dengan kode
    `timeout` (dan `offline`) dan pesan buatannya → tetap berlabel.
  - `TestHubTimeoutIsNotLabelled`: batas waktu hub → tidak berlabel.
  - `TestHubDisconnectDuringCallIsNotLabelled` (paket hub dan
    orchestrator): sambungan putus selagi panggilan menunggu → galatnya
    bukan `*hub.AgentError`, `errors.Is(err, proto.ErrOffline)` tetap
    benar.
  - `TestAgentJobFailureLabelled`: `JobDone.Error` dari job agent dilabeli
    sama.
  - `TestAgentLogLinesPrefixed`: baris log dari agent → `agent: <satu baris>`.
  - `TestFlashesLabelAgentText`: Hubungkan MySQL, Segarkan, kendali
    replika, job master lama: galat agent tampil berlabel.
  - `TestQuotesKeepsNewAdvice`: setiap teks dari `mysql.AdviceTexts()`
    (T7, T8a, T8b, T10–T17) keluar dari `redact.Quotes` utuh sesudah
    dilabeli dan tidak terpotong pada `maxLogLine`; nama akun pada teks
    kurang-hak tetap utuh; titik tetap.
  - `TestQuotesKeepsOldAgentPhrases`: kedua frasa judul README
    (`Database yang tidak ada di master baru`, `Memberi hak SHOW DATABASES
    di server yang dikarantina`) dan jangkar akun `GRANT SHOW DATABASES`
    lama tetap dipertahankan sensor, untuk teks agent yang belum
    diperbarui (satu rilis).
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
    dengan `MissingGrants` menampilkan T21 tanpa blok `GRANT`; peran lain
    tetap seperti sekarang.
- [ ] **Step 2:** Pastikan gagal.
- [ ] **Step 3:** Hub: `AgentError`, pembungkusan di `readResponse`, jalur
  `WSConn.stop` tanpa pembungkus. `agenttext.go`; `callWithin` memakai
  `fromAgent`; `agentJobEnded` mengembalikan `agentTextError`; `fail`
  tidak mengubah urutan (label ada di teks galat, lalu `redact.Quotes`);
  `logf` untuk baris dari agent; web `shownError` memakai
  `*hub.AgentError` di setiap flash yang menampilkan galat pemanggilan
  agent.
- [ ] **Step 4:** `redact`: daftar frasa tidak berkurang (`Ulangi dari
  tahap ini`, `Database yang tidak ada di master baru`, `Memberi hak SHOW
  DATABASES di server yang dikarantina`, `Paksa dump`, `Sudah saya
  alihkan`); jangkar akun pertama memakai awal kalimat agent yang utuh
  (N-H5) dan mengikuti T16; jangkar `GRANT SHOW DATABASES` lama tetap
  satu rilis. Test panel membaca teks agent dari `mysql.AdviceTexts()`,
  bukan salinan tangan.
- [ ] **Step 5:** N-H1, N-H3, N-H4, T9, spanduk T21.
  `bash scripts/tailwind.sh`.
- [ ] **Step 6:** Gerbang panel, `make test`; commit per kelompok (hub dan
  label; sensor; butir kecil).

### Task 15: README, teks untuk pemilik, e2e

**Files:**
- Modify: `README.md`; rencana ini ("Teks baru untuk dibaca pemilik");
  `internal/panel/e2e_replica_test.go` (atau berkas e2e yang cocok)

Teks (dipakai persis):
- T27 kalimat pembuka kedua bagian README yang ditulis ulang, dengan
  versi rilis yang memuat perintahnya: `Perintah ini ada sejak versi <versi>; bila agent menjawab perintah tidak dikenal, perbarui agent dulu (Pasang ulang agent di halaman server).`

- [ ] **Step 1: e2e dulu** (RED bila perilaku belum ada):
  - `TestE2ERebuildFailsBeforeCopyThenLeftoverThenRetry`: bangun ulang
    master lama gagal sebelum salinan → halaman server: dikarantina,
    Periksa data tertinggal berjalan dan menemukan transaksi → dialog
    "Ulangi dari tahap ini" menyebut transaksi itu → Ulangi → bangun
    ulang selesai; server replika; kotak hilang. Selagi bangun ulang
    berjalan kotak menampilkan T22 dan server itu tidak tampil sebagai
    kartu replika.
  - `TestE2ERebuildFailsAfterSettle`: gagal di Verifikasi → replika gagal
    biasa (Resync tersedia, tanpa kotak).
  - `TestE2ERelockAlert`: `SET GLOBAL super_read_only=OFF` di master lama
    → dalam dua putaran penjaga panel membuka peringatan dengan teks T1;
    Periksa data tertinggal menutupnya.
- [ ] **Step 2:** README (setiap perintah dijalankan dulu; teks disalin
  dari kode):
  - "Database yang tidak ada di master baru": judul dipertahankan;
    dibuka dengan T27; ditulis ulang — kapan terjadi (penolakan "Periksa
    server tujuan" dan penolakan saat mengakhiri karantina); satu
    perintah `sudo dbpanel agent drop-database <nama>`; arti setiap
    bagian keluarannya; cara melihat di master baru dan mengapa nama
    selalu harus diketik; apa yang terjadi bila database masih dipakai
    (C12) atau perintah dihentikan (C13); cadangan yang tidak bisa dibuat
    (C8); menyalin cadangan ke server lain; langkah berikutnya di panel.
    Blok SQL dengan tangan dihapus.
  - "Memberi hak SHOW DATABASES di server yang dikarantina": judul
    dipertahankan; dibuka dengan T27; ditulis ulang — `grant-admin`, dan
    `reset-admin` untuk nama akun yang tidak didukung, akun yang tidak
    bisa masuk, atau "Hubungkan MySQL" yang ditolak; opsi `--pengguna`;
    bila diminta kata sandi. Blok SQL dihapus.
  - Bagian **Master lama**: "Setelah bangun ulang gagal" mengikuti aturan
    baru (tetap dikarantina, apa yang tersedia, Periksa tidak menghapus
    Ulangi, dialog Ulangi menyebut data tertinggal); bangun ulang yang
    berjalan tampil di kotak "Master lama"; kalimat kotak bila agent
    melaporkan peran lain (T3, T3b) dan bahwa "Ulangi dari tahap ini"
    memulihkannya; tidak ada lagi "Batalkan percobaan bangun ulang";
    promote kedua (T4–T6).
  - Bagian peringatan: `master_start_late` kini juga untuk "selagi
    berjalan" (T1) dan "tidak bisa memeriksa" (T1b); penjaga mengunci
    lagi dalam 5 detik; membuka master lama dengan benar tetap
    `set-role`.
  - Bagian keamanan/mode ketat: kalimat tentang kapan karantina berakhir
    (daftar master baru; `read_only` tetap menyala selama muat; server
    yang sempat tidak terkunci selama atau sesudah salinan harus disalin
    ulang).
  - SEC-233 (m-I1…m-I4) pada "Melepas kunci start dengan tangan" dan
    bagian yang tersisa.
- [ ] **Step 3:** Isi "Teks baru untuk dibaca pemilik": setiap teks baru
  atau berubah (semua id T… dan C… di spek 5.8, layar ketiga perintah CLI
  disalin dari keluaran sungguhan), dengan tempat tampilnya.
- [ ] **Step 4:** Semua gerbang termasuk `make test-e2e`; commit
  `docs: README and owner texts for plan 5b-2a; e2e for the old-master rule`.

---

## Catatan untuk pelaksana

- Urutan: 1 → 2 → 2b → 3 → 4 → 5 → 6 → 7 → 8 → 9 → 10 → 11 → 12 → 13 →
  14 → 15. Task 3–10 mengubah agent dan proto saja, kecuali Task 9 yang
  boleh memperbarui harapan test panel yang menyimpan salinan teks agent
  lama. Task 11–14 hanya panel (Task 12 memakai field proto Task 5; Task
  14 memakai `mysql.AdviceTexts()` Task 9).
- Task 3, 4 dan 8 membaca berkas bukti Task 2b sebelum mulai; bila yang
  terukur bertentangan dengan yang ditulis task, berhenti dan laporkan.
- Tidak ada task yang mengubah `internal/panel/monitor` selain Task 12,
  dan tidak ada yang mengubah aturan mode ketat (`strict.go`, tabel peran)
  selain yang disebut Task 10.
- Butir yang menunggu keputusan pemilik: perubahan spek 7.12–7.19 (teks
  T1b untuk Task 4/5/12; F-1 untuk Task 11; F-3 untuk Task 8; F-4 untuk
  Task 12) dan blok bertanda `--tanpa-cadangan` di Task 8. Task 3–7 tidak
  bergantung pada jawaban itu kecuali teks T1b.
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

Tinjauan rancangan (Task 1,
`docs/superpowers/ledgers/2026-11-14-05b2a-design-review.md`) menghasilkan
30 temuan (R-1 … R-30) dan empat penyederhanaan (F-1 … F-4); semuanya
diterima, beberapa dalam bentuk yang lebih kecil. Sesudahnya pengukuran
putaran kedua (Task 2b,
`docs/superpowers/ledgers/2026-11-14-05b2a-measurements-2.md`) mengubah
atau meneguhkan beberapa butir. Spek bagian 10 memuat daftar yang sama
menurut bagian spek.

Catatan untuk membaca "Bukti pengukuran": baris keputusan di sana yang
berbunyi `both = false` berarti "pembukaan hanya mematikan
`super_read_only`". Karena itu berlaku untuk kelima pemakai, parameter
itu tidak dibangun sama sekali (R-4).

| Temuan | Task | Yang berubah |
|---|---|---|
| R-1 | 3 | Pembukaan punya `end` dan `abandon`; `cloneFrom` melepas lewat `abandon` sesudah restart; test `TestCloneReleasesLift`, `TestAbandonAfterRestartKeepsFlags` |
| R-2 | Global Constraints, 3, 8 | Berkas kunci `/var/lib/dbpanel/agent/lift.lock` (direktori `0700`, berkas `0600`); `openLiftLock` memeriksa direktori dan deskriptor (`O_NOFOLLOW`, `O_CLOEXEC`, berkas biasa, milik uid proses, tanpa bit grup/lain, satu nama); agent mengganti berkas yang tidak lulus, CLI menolak (C4); `TestLiftLockRefusesForeignFile`, `TestCLIRefusesUnsafeLockFile` |
| R-3 | 4 | Satu aturan penjaga dan urutan enam langkah menggantikan langkah 3 lama dan keputusan pra-pelaksanaan P1 (tanda muat tetap menahan penjaga; kunci berkas menahannya untuk pembukaan tanpa tanda); `TestGuardTransitionDuringCLILift`, `TestGuardTransitionDuringUnmarkedLift`, `TestCloneRestartIsNoStart`, `TestGuardNormalRoundDoesNotTouchLockFile` |
| R-4 | 2, 2b, 3, 4, 5 | Task 2b ditambahkan; mode yang mematikan `read_only` dihapus dari gerbang (`begin`, `tryGuard`), penjaga dan laporan (`relocked`); `dumpFrom` menolak akun muat yang lemah (T10, `TestDumpRefusesLoadAccountWithoutBypass`) |
| R-5 | 4, 5, 12 | Laporan "tidak bisa memeriksa" sesudah tiga putaran (`unverified`, teks T1b), paling sering sekali per 10 menit; jendela `AdminReplaced` dikecualikan; `TestGuardReportsWhenItCannotLogIn`, `TestGuardReportsWhenRelockFails`, `TestGuardQuietWhileAdminReplaced`, `TestUnverifiedOpensLateAlert`, `TestUnverifiedNotClosedByCopyStarted` |
| R-6 | 4 | `mark.set()` membuang catatan muat lama; settle membaca bendera sendiri di bawah kunci berkas; `PERSIST` gagal sesudah `1 1` tidak dihitung; `TestFailedLoadDropsEarlierLoadRecord`, `TestSettleRefusedWhenFlagsOffNow`, `TestSettleWaitsForCLILift`, `TestFailedPersistAfterFlagsOnCostsNothing` |
| R-7 | 5 | `recordRelock` (tidak memanggil `judgeKinds`), `While` di catatan tersimpan, id = `startID(t)`, setiap kejadian dicatat dan hanya antrean dibatasi; `TestRelockRecordsEveryIncidentQueuesOncePerMinute` menggantikan `TestRelockRepeatedReportsOncePerIncident`; `TestRelockLeavesBypassedAndFailedEntriesAlone`; panel `TestRelockAfterLeftoverCheckOpensAgain` (Task 12) |
| R-8 | Global Constraints, 8, 9 | `runLifted`: `lock_wait_timeout` 60 detik, sinyal ditangkap, batas 10 menit, pemulihan lewat sambungan kedua dengan `KILL QUERY`; teks C12, C13, C14; `TestDropDatabaseInterruptedRelocksItself`, `TestDropDatabaseBlockedByOpenTransaction`, `TestDropDatabaseGivesUpAfterCap` |
| R-9 | 8, 9 | Pemeriksaan ulang sesudah konfirmasi di bawah kunci (`runLifted` langkah 2, teks C11); pemulihan memasang yang terbaca; `TestDropDatabaseRecheckAfterConfirmation`, `TestAdminCommandsRecheckAfterConfirmation` |
| R-10, F-3 | Global Constraints, 8, 9 | Berkas daftar "tidak disalin" dan ketiga fungsinya tidak dibangun (Task 8 tidak lagi mengubah `rebuild_load.go`/`quarantine.go`); nama selalu diketik (C10) sesudah petunjuk C9/C9b; `errNotReplaced` menjadi T8a/T8b; pemeriksaan "dedicated" menunjuk perintah (T13); `TestNotReplacedTextSplitsByNewMaster`, `TestDedicatedCheckPointsToCommandOnQuarantined` |
| R-11 | 8 | Pemeriksaan cadangan lewat deskriptor yang disimpan, `fsync` berkas dan folder sebelum bertanya, peringatan `--ke` (C7), teks cadangan gagal (C8), `TestDropDatabaseRoutineWithCreateTableLine`, `TestDropDatabaseSyncsBackupBeforeDrop`; opsi `--tanpa-cadangan` sebagai satu blok bertanda yang menunggu pemilik |
| R-12 | 9 | Teks `mysql.connect` menjadi T15 (menunjuk `reset-admin`); `TestConnectRefusalAdviceWorks` |
| R-13 | 9 | `MissingAdminGrants` memakai `replicaGrants` + `SHOW DATABASES`; `StoredAccountName` (`CURRENT_USER()`); teks C17; pemeriksaan "grants" di server yang dikarantina tanpa `FixSQL` (T14); `TestGrantAdminCoversReplicaPreflight` |
| R-14, F-2 | Review Focus, 7 | `serviceCredential(dir)` = pemilik direktori lewat `lstat`; uid 0 = seperti sekarang dengan T19; `ErrNotStarted` membedakan "tidak bisa dimulai" dari "gagal", dan hanya yang pertama jatuh ke root; `TestServiceToolFailureIsNotRetriedAsRoot`, `TestServiceCredentialRootCountsAsMissing`; `TestServiceCredentialOtherUser` menjadi `TestServiceCredentialIsDirectoryOwner` |
| R-15, F-1 | Architecture, 11 | Peran tetap `quarantined` sampai settle; `AddReplica`/`RemoveReplica` mempertahankannya; `SettleRebuild` satu transaksi; tidak ada penulisan peran di `fail`/`Resume`/`Retry`/`Approve`; `failUnrunnable` menandai baris, `Resume` menulis baris sebelum job di bawah `startMu`; `TestEveryFailurePathLeavesQuarantinedWithFailedRow`; `TestRetryFromQuarantinedFlipsRole` dan `TestResumeNormalizesHalfFailedRebuild` diganti `TestRetryFromQuarantinedKeepsRole`, `TestResumeFailsRebuildRowBeforeJob` |
| R-16 | 12, 15 | `leftoverWarnings` dipakai form, dialog Ulangi dan dialog Setujui; `TestRetryDialogShowsLeftoverWarnings`; e2e menegaskan dialog menyebut transaksi yang ditemukan |
| R-17, R-19, F-4 | Global Constraints, 12 | Tidak ada hitungan laporan pin, tidak ada panggilan balik monitor, tidak ada pemulihan dari laporan; kalimat kotak T3/T3b untuk setiap peran lain; Ulangi memulihkan jawaban settle yang hilang (`TestRetryConnectHealsQuarantineEnded`); `TestPinReplicaHealsLostSettle` tidak dibangun, `TestBoxSaysAgentReportsReplica` menjadi `TestBoxSaysAgentReportsOtherRole` |
| R-18 | 14 | `*hub.AgentError`; `fromAgent` menurut tipe; `TestAgentErrorWithPanelCodeIsLabelled`, `TestHubTimeoutIsNotLabelled`, `TestHubDisconnectDuringCallIsNotLabelled` |
| R-20 | 12, 13 | Halaman replika server `quarantined` tanpa Resync/Lepas (T23); peringatan sesudah promote kedua (T25, T26); alasan kandidat T24; harapan `TestOldMasterAfterSecondPromote` tetap "master topologi ini sudah berganti" karena di bawah F-1 peran tetap `quarantined` |
| R-21 | 3 | Pembuka berikutnya mengirim pernyataan pembuka lagi; sambungan disimpan per pembuka; kunci dilepas walau pemulihan gagal; `O_CLOEXEC`; `TestLiftLaterHolderReassertsOff`, `TestLiftReleasesLockWhenRestoreFails`, `TestLiftLockNotInherited`. Butir hitungan mode "keduanya" gugur bersama mode itu |
| R-22 | 3, 8 | Urutan CLI: kunci dicoba (3 detik) sebelum bendera; dua teks sibuk: C3 (`ErrLiftBusy`, CLI) dan T12 (`ErrLiftHeldByCommand`, pekerjaan agent); `TestCLIProbeRetriesShortly` |
| R-23 | 8 | Kata sandi tidak ditulis ke berkas (`dumpCommand`); `promptReader` tunggal; `TestRootSessionPasswordFallback` diperluas |
| R-24 | 8, 9 | `printable` untuk setiap nama asal MySQL; `strconv.Quote` di syslog; `TestCLIOutputEscapesNames` |
| R-25 | 8, 9 | `rootSession.Conn`, `MissingAdminGrants`, `ResetAdmin` memakai `*sql.Conn`; opsi `--pengguna <nama>` (C5b); `TestAdminCommandsKeepSqlLogBinOff`, `TestRootSessionOtherUser` |
| R-26 | Global Constraints, 6 | Daftar kedua dicoba tiga kali; gagal → `errSecondListFailed` (T11) dan langkah salinan gagal; `TestSecondSourceListRetriedThreeTimes`, `TestSecondSourceListFailureFailsCopyStep` menggantikan `TestSecondSourceListFailureLeavesNoLoadRecord` |
| R-27 | 4, 12 | T7 dan T2b berbunyi lain; buka-tutup peringatan dalam keadaan `CopyStarted` dicatat sebagai disengaja |
| R-28 | Review Focus, 14, 15 | Dua judul README dan dua frasa sensor (dan jangkar `GRANT` lama) dipertahankan satu rilis; kalimat pembuka T27; `TestQuotesKeepsOldAgentPhrases` |
| R-29 | 5, 9, 12, Catatan untuk pelaksana | Task 12: `handlers_alerts.go`, `notify/message.go`, `monitor.Deps`; Task 5: `While` di catatan tersimpan; catatan urutan menyebut Task 9 boleh menyentuh test panel |
| R-30 | 11 | `cancelAgentCopy` sebelum hapus dan sebelum bangun ulang baru; `TestDeleteQuarantinedWithFailedRow`, `TestNewRebuildFromQuarantinedWithRow` |
| pengukuran putaran kedua, X7 | Global Constraints, 3, 4 | Penutup aliran muat dump penuh memasang `super_read_only` sebelum `FLUSH PRIVILEGES` (`loadTrailer`); `end` membaca dulu dan tidak mengirim apa pun bila sudah menyala; sambungan yang disimpan menyetel `wait_timeout` 28800; `TestFullDumpRebuildRestoresLockInStream`, `TestLiftEndSendsNothingWhenAlreadyOn`, `TestFullDumpRebuildLeavesNoRelock` |
| pengukuran putaran kedua, X4 | Global Constraints, 4, 8 | Penjaga menyetel `lock_wait_timeout` 10 pada sambungannya; galat 1205 = "tidak bisa mengunci" (aturan tiga putaran); batas konteks tidak dipakai sebagai batas; `TestGuardBoundedBehindTableLock`; pemulihan CLI saat sinyal memakai `KILL QUERY` |
| pengukuran putaran kedua, X2 | 3 | Pemeriksaan akun muat berlaku kapan pun `read_only` menyala di server tujuan (bukan hanya di server yang dikarantina); SUPER saja cukup |
| pengukuran putaran kedua, X5 | Review Focus, 8 | Bukti kelengkapan hanya exit 0 dan baris `-- Dump completed`; hitungan `CREATE TABLE ` "paling sedikit", di luar blok `DELIMITER`, sebagai pemeriksaan kewajaran; nama ber-`'`/`"` berakhir di C8 (atau di blok `--tanpa-cadangan`); `mysqldump` tidak membaca berkas opsi lain |
| pengukuran putaran kedua, X6 | 3, 4 | `abandon` diteguhkan: penerima CLONE menyala dengan nilai tersimpannya sendiri, jadi restart CLONE tidak butuh aturan penjaga tambahan; yang tidak diukur dicatat di spek bagian 8 |
| pengukuran putaran kedua, X3 | 2b | MySQL 8.4 tidak diukur (tidak terpasang); dicatat di spek bagian 8 |

Keputusan pra-pelaksanaan yang tetap berlaku: penjaga menoleransi
`Driver.relocked` nil di antara Task 4 dan Task 5; Task 9 boleh
memperbarui harapan test panel yang menyimpan salinan teks agent lama;
`drop-database` menolak bila nama berkas cadangan sudah ada (C6).

## Perubahan selama pelaksanaan

1. Task 4, uji catatan muat (2026-11-16). Muat yang gagal karena kata
   sandi akun dump salah berhenti di uji klien, sebelum pembukaan
   baca-saja, jadi `mark.set()` tidak berjalan dan catatan muat lama
   tetap ada. Itu benar: server masih seperti ditinggalkan muat
   sebelumnya. `TestFailedLoadDropsEarlierLoadRecord` kini membuat muat
   kedua gagal sesudah pembukaannya dimulai, dan
   `TestEarlyFailedLoadKeepsRecordAndWritableFlag` menjaga kasus gagal
   lebih awal.
2. Task 4, login penjaga yang ditolak (2026-11-17). Tinjauan tugas
   menemukan bahwa hanya galat 1045 dan 1698 yang dihitung; akun agent
   yang dikunci (3118) membuat penjaga diam. Kini setiap galat yang
   dikirim server MySQL sendiri saat login dihitung untuk aturan tiga
   putaran; hanya MySQL yang tidak menjawab sama sekali yang tidak.
   Akibatnya: server yang menolak login tiga putaran berturut-turut
   karena sebab sementara (misalnya sambungan penuh, galat 1040) juga
   membuka peringatan "tidak bisa memeriksa".
3. Task 4, uji settle yang ada. Empat uji settle
   (`TestConfigureReplicationEndsQuarantine`,
   `TestStartKindsGoWithTheFile`, `TestSettleRebuiltClearsStartResult`,
   `TestSettleRebuiltClearsApproval`) kini memakai mysqld sungguhan
   yang terkunci baca-saja, karena settle membaca kedua bendera sendiri.
   Dua uji penjaga yang ada kini membuka kunci seperti pembukaan
   sungguhan (hanya `super_read_only`).
4. Task 5, tempat kode (2026-11-17). Catatan dan laporan "dikunci lagi"
   seluruhnya ada di `start_result.go` (di samping `recordStartResult`),
   ujinya di `start_result_test.go`; `startlock_guard.go` dan dua berkas
   uji lain di daftar Files tidak diubah. Uji yang ada
   `TestQuarantineGuardDefersToRebuildLoad` kini hanya menghitung
   kejadian start yang `While`-nya kosong, karena penjaganya sekarang
   juga mengantre laporan "dikunci lagi".

## Teks baru untuk dibaca pemilik

(Diisi Task 15. Teks ber-id T1–T27 dan C1–C19 ada di spek 5.8 dan di
task-nya; P1 dan P2 di blok bertanda spek bagian 7 dan Task 8.)
