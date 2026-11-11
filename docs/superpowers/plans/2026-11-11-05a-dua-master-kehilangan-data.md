# Rencana Implementasi 5a: Menutup Jalur Dua Master dan Kehilangan Data

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Master lama yang dikarantina tidak pernah menerima tulisan saat
MySQL-nya menyala lagi; penjaga agent berjalan tiap 5 detik apa pun
pengaturan panel; layar pengalihan tidak pernah mengaku "tidak ada
koneksi" tanpa bukti; tidak ada database yang dibuang tanpa konfirmasi
segar; sinyal dua-master dan master tumbang tidak bisa dibungkam.

**Architecture:** Agent mendapat loop penjaga kedua (jeda tetap 5 detik
setelah putaran selesai) yang menjalankan pemutus FTWRL, `GuardTick`
(kunci promote, karantina, kunci start) dan pencatatan replika. Karantina
menulis "kunci start": berkas SQL milik root yang dipasang lewat satu
baris `init_file=` di blok konfigurasi terkelola, diperiksa dengan
`mysqld --print-defaults`, dijaga dan dilepas oleh loop penjaga. Catatan
replika memperoleh berkas port; kunci promote menyimpan daftar host yang
terbaca saat dipasang dan bertahan selama ada yang tidak terbaca. Daftar
koneksi membaca semua thread klien (atau `information_schema` saat
`performance_schema` mati) dan menandai jenis serta kelengkapannya. Di
panel: peringatan Kritis baru, "Jangan ganggu" yang tidak membungkam jenis
kritis plus ringkasan (migrasi 0011), modal resync berkelompok dengan
penegakan di orkestrator, "Ulangi"/"Setujui" bertoken untuk job pembuang,
dan "Tambah database" yang berhenti bila database sudah ada di replika.

**Tech Stack:** Go 1.26, `CGO_ENABLED=0`, SQLite (modernc, satu koneksi),
`github.com/go-sql-driver/mysql` v1.9.3 (`Config.ConnectionAttributes`),
html/template + Tailwind v4/daisyUI v5 (`bash scripts/tailwind.sh`),
Alpine CSP build, harness `internal/agent/drivers/mysql/testmysql`
(mysqld sungguhan; test di-skip bila mysqld tidak terpasang).

**Spec:** `docs/superpowers/specs/2026-11-10-rencana-5a-dua-master-kehilangan-data-design.md`
(semua nomor bagian di bawah merujuk ke sana; bagian 8 mengikat). Bahan:
`docs/superpowers/security/2026-11-10-inventaris-risiko.md` (R-029–R-036,
R-043, R-058). Kode diperiksa pada HEAD `67c1388` (kode sama dengan `main`
`0912c5f`).

## Global Constraints

- Go 1.26, `CGO_ENABLED=0`, tanpa dependensi baru. Gerbang: `make test`
  dan `make test-e2e` lulus setelah setiap task. Test yang butuh mysqld
  memakai `testmysql.Start(t, testmysql.Options{...})` (skip otomatis
  tanpa mysqld; saat root, mysqld berjalan sebagai `nobody`;
  `testmysql` menulis `mysqlx=OFF`).
- Semua teks pengguna berbahasa Indonesia; CSP ketat (tanpa script/style
  inline); komponen Alpine baru di `web/static/app.js`; setelah mengubah
  template jalankan `bash scripts/tailwind.sh`.
- Migrasi baru satu saja: `internal/panel/store/migrations/0011_snooze_from.sql`.
- Nilai tetap: loop penjaga `GuardEvery = 5 * time.Second`, jeda setelah
  putaran selesai (bukan ticker), tidak bisa diatur panel; peringatan log
  penjaga yang sama paling sering 1 per menit; interval pemeriksaan
  10–300 detik di panel dan agent (nilai tersimpan > 300 dibaca 300);
  interval ringkasan dan ambang lag tetap 10–3600; pembacaan peer 5 detik
  per bacaan; recheck kunci promote 1 menit; percobaan ulang tulis kunci
  start paling sering 1 per menit; konfirmasi job pembuang kedaluwarsa
  setelah 24 jam.
- Isi berkas kunci start persis tiga baris, urutan ini:
  `SET GLOBAL super_read_only=ON;` / `SET PERSIST read_only=ON;` /
  `SET PERSIST super_read_only=ON;`. Nama berkas `dbpanel-start-lock.sql`,
  milik root, mode 0644, di `Layout.IncludeDir` bila ada, selain itu di
  direktori `Layout.ConfigFile` (setelah symlink diikuti); tidak pernah di
  dalam `Layout.DataDir`. Baris: `init_file=<path absolut>`.
- Urutan kunci start: tulis = berkas dulu, baru baris; lepas = baris
  dulu, baru berkas. Berkas tidak pernah dihapus selama konfigurasi
  terkelola masih menyebutnya.
- Agent tidak pernah menerima host atau port replika dari panel;
  `mysql.recordReplicas` tidak menerima parameter.
- Akun layanan yang disembunyikan di daftar koneksi hanya `dbpanel_repl`
  (`replUserName`) dan pola `^dbpanel_dump_[0-9a-f]{8}$` (`dumpAccountRe`).
  Atribut koneksi agent: `program_name=dbpanel-agent`.
- Teks UI dari spek, dipakai persis:
  - 5.2: "master lama belum terjamin terkunci saat MySQL menyala; jangan nyalakan MySQL-nya sebelum agent tersambung";
    "master lama sempat bisa ditulis saat menyala; jalankan Periksa data tertinggal";
    "kunci start tidak dipakai MySQL; periksa opsi layanan";
    "MySQL master lama gagal menyala karena berkas kunci tidak terbaca".
  - 5.5: "Tidak ada koneksi aplikasi tersisa" (hanya bila lengkap dan kosong); "jenis koneksi tidak dikenal";
    "tidak bisa dibedakan dari agent"; "akun admin panel"; "sesi akun admin dari server ini"; "tidak bisa dipastikan";
    centang `accept_connections`: "Saya paham N koneksi masih ada dan tulisannya hilang".
  - 5.6: kelompok "ada di master, disalin ulang" dan "hanya di replika, isinya hilang permanen";
    tambah database: "Database X sudah ada di replika ini. Panel tidak bisa memastikan isinya sama dengan master. Isinya di replika akan dibuang permanen, lalu diganti salinan dari master.";
    catatan halaman: "bila ditambahkan lagi, isi di replika dibuang lebih dulu".
- Teks yang ditulis perencana (spek tidak memberinya) ditandai "(teks
  perencana)" di task-nya; jangan diubah tanpa alasan.
- Setelah mengubah kode: `graphify update .`. `graphify-out/` ada di
  `.gitignore` repo ini dan tidak ikut commit.

## Review Focus

1. Server yang kuncinya sudah dilepas (master lama yang dibangun ulang,
   lalu dipromote) harus menyala normal dan bisa ditulis setelah restart:
   tidak ada `init_file` tersisa dan berkasnya hilang. Test
   `TestReleasedServerRestartsWithoutInitFile` di Task 3.
2. Berkas kunci dihapus orang saat agent mati: MySQL menolak menyala,
   penjaga menulis ulang berkas dalam 5 detik setelah agent hidup, dan
   panel mendapat kejadian gagal-menyala dari log error. Test
   `TestGuardRewritesMissingFileAndLine` dan
   `TestBootstrapFailureEventFromErrorLog` di Task 3.
3. Panel lama ↔ agent baru dan panel baru ↔ agent lama:
   `check_every_sec` 3600 dari panel lama diterapkan 300 (bukan ditolak);
   karantina dan daftar koneksi dari agent lama dianggap tidak terjamin /
   `Incomplete`. Test di Task 1, 7, 9.
4. "Jangan ganggu" yang berakhir saat panel mati, dan senyap yang
   dibatalkan manual: ringkasan dikirim tepat sekali. Test
   `TestSnoozeDigestAfterPanelRestart` dan `TestSnoozeDigestSentOnce` di
   Task 10.
5. Token "Ulangi" dipakai dua kali, untuk job lain, atau untuk server lain:
   ditolak 403 dan job tidak berjalan. Test `TestJobTokenBoundToJob` di
   Task 12.

## Bukti profil keamanan mysqld (spek 5.2)

Diambil 2026-11-11 ke direktori kerja perencana
(`/tmp/claude-0/-home-user-db-panel/26bbb5e6-6a34-5a2d-a807-f83dee84386a/scratchpad/apparmor/`).

**Ubuntu `mysql-server-8.0` 8.0.46-0ubuntu0.24.04.4 — terverifikasi dari
paket.** `apt-get download mysql-server-8.0` (sha256
`c538bc4c53e5cbdfadaf6507c56a2fe80633c9cd6505f12df48c2d17714402ac`),
`dpkg-deb -x`. `etc/apparmor.d/usr.sbin.mysqld` memuat:

```
/usr/sbin/mysqld {
  #include <abstractions/user-tmp>
  #include <abstractions/mysql>
# Allow config access
  /etc/mysql/** r,
# Allow data dir access
  /var/lib/mysql/ r,
  /var/lib/mysql/** rwk,
  #include <local/usr.sbin.mysqld>
}
```

`postinst` memuat profil ini (`apparmor_parser -r -T -W
/etc/apparmor.d/usr.sbin.mysqld`). Abstraksi dari paket `apparmor`
4.0.1really4.0.1-0ubuntu0.24.04.9: `abstractions/mysql` hanya soket dan
charset; `abstractions/user-tmp` hanya `owner /tmp/**`, `owner
/var/tmp/**`. Konfigurasi paket: `etc/mysql/mysql.cnf` berisi
`!includedir /etc/mysql/conf.d/` lalu `!includedir
/etc/mysql/mysql.conf.d/`; `mysql.conf.d/mysqld.cnf` memuat `user =
mysql` dan `log_error = /var/log/mysql/error.log`; unit
`mysql.service`: `ExecStart=/usr/sbin/mysqld` tanpa `--defaults-file`.
Jadi di Ubuntu `IncludeDir` = `/etc/mysql/mysql.conf.d` dan berkas
`/etc/mysql/mysql.conf.d/dbpanel-start-lock.sql` termasuk `/etc/mysql/**
r`.

**Oracle MySQL APT (paket `mysql-community-server`) — paket tidak
terjangkau.** `repo.mysql.com`, `dev.mysql.com`, `downloads.mysql.com`,
`cdn.mysql.com` ditolak proxy (CONNECT 403). Sebagai gantinya dibaca
sumber kemasan Oracle di GitHub
`mysql/mysql-server` cabang `8.0`, `8.4` dan `trunk`,
`packaging/deb-in/extra/apparmor-profile` (ketiganya identik, sha256
`006e040d…cfa0d5ac`): memuat `/etc/mysql/** r,`, `/var/lib/mysql/** rwk,`,
`/var/lib/mysql-files/** rwk,`. **Tidak diverifikasi:** bahwa paket deb
Oracle yang terbit memakai berkas ini tanpa perubahan, dan bahwa
konfigurasi bawaannya punya `!includedir`.

**RHEL-family SELinux — sumber kebijakan, bukan paket.** Kebijakan
`selinux-policy` Fedora (`policy/modules/contrib/mysql.if`) menyatakan
modul mysql pindah ke `https://github.com/devexp-db/mysql-selinux`;
dibaca cabang `master` di sana. `mysql.fc` (sha256 `1f2daa57…31670`):

```
/etc/my\.cnf		--	gen_context(system_u:object_r:mysqld_etc_t,s0)
/etc/mysql(/.*)?		gen_context(system_u:object_r:mysqld_etc_t,s0)
/etc/my\.cnf\.d(/.*)?		gen_context(system_u:object_r:mysqld_etc_t,s0)
/var/lib/mysql(-files|-keyring)?(/.*)?		gen_context(system_u:object_r:mysqld_db_t,s0)
```

`mysql.te` (sha256 `54c69520…ebb`): `allow mysqld_t
mysqld_etc_t:file read_file_perms;`; tidak ada `files_read_etc_files`
untuk `mysqld_t`. **Tidak diverifikasi:** isi paket RPM RHEL 8/9/10 dan
Oracle YUM (mirror CentOS Stream, Rocky, Fedora dan repo Oracle ditolak
proxy); apakah `/etc/my.cnf` bawaan RPM Oracle punya `!includedir
/etc/my.cnf.d`; apakah berkas di `/etc` (tipe `etc_t`) terbaca
`mysqld_t` lewat antarmuka lain. Karena itu aturan di bawah
memperlakukan semua selain `mysqld_etc_t` sebagai tidak terbukti.

**Eksperimen lokal perencana (mysqld 8.0.46, tanpa server berjalan):**
- Berkas `.sql` di direktori `!includedir` tidak dibaca sebagai berkas
  opsi (berkas berisi `this is not=an option` tidak membuat
  `--print-defaults` gagal); hanya `*.cnf` yang dibaca.
- `mysqld --defaults-file=F --print-defaults` mencetak semua opsi sesuai
  urutan baca, termasuk setiap `--init_file=`; nilai terakhir menang.
- `mysqld --defaults-file=F --init-file=X --print-defaults` (opsi lain
  di antara keduanya) **menjalankan mysqld**, bukan mencetak. Argumen
  harus persis `[--defaults-file=F] --print-defaults`.
- `mysqld --validate-config` tidak memeriksa keberadaan berkas
  `init_file` (rc 0 untuk path yang tidak ada). Validasi tidak melindungi
  dari baris tanpa berkas; urutan tulis yang menjaganya.

**Aturan yang diturunkan:** berkas di `IncludeDir` (Ubuntu:
`/etc/mysql/mysql.conf.d`, RHEL: `/etc/my.cnf.d`), selain itu di
direktori `ConfigFile`; ekstensi `.sql`, tidak pernah `.cnf`. Agent
menentukan `MACConfined` (AppArmor: profil `Layout.MysqldPath` berstatus
`enforce` di `/sys/kernel/security/apparmor/profiles`; SELinux:
`/sys/fs/selinux/enforce` = `1` dan label `security.selinux` biner mysqld
memuat `:mysqld_exec_t:`). Bila dibatasi, `StartLocked` hanya benar bila
berkas terbukti boleh dibaca: AppArmor → path (setelah symlink) diawali
`/etc/mysql/`; SELinux → label berkas memuat `:mysqld_etc_t:`. Selain itu
`StartLocked=false` dengan alasan, dan baris tetap ditulis (arah gagal
yang spek 7 terima: MySQL tidak menyala, data aman, panel memperingatkan).

---

### Task 1: Loop penjaga tetap 5 detik dan interval pemeriksaan maksimal 300 detik (R-030)

**Files:**
- Modify: `internal/agent/monitor/monitor.go` (`Run` L267-279, `guardDumpLocks` L331, `guardPromote` L346, `recordReplicas` L359, `Configure` L213-241, `maxIntervalSec` L105)
- Modify: `internal/panel/monitor/settings.go` (`maxIntervalSec` L66, `LoadSettings` L123), `internal/panel/web/handlers_settings.go` L146, `web/templates/settings.html` L49-51
- Test: `internal/agent/monitor/monitor_test.go`, `internal/panel/monitor/settings_test.go` (buat bila belum ada), `internal/panel/web/web_settings_test.go`

**Interfaces:**
- Produces:

```go
// internal/agent/monitor
const GuardEvery = 5 * time.Second   // jeda loop penjaga setelah satu putaran selesai; tidak bisa diatur panel
const maxCheckEverySec = 300         // check_every_sec di atasnya diterapkan sebagai 300 (bukan ditolak)
type Options struct { /* ... */ GuardAfter func(d time.Duration) <-chan time.Time } // jeda loop penjaga; default time.After; test menggantinya
// Run menjalankan dua goroutine: loop lama (tick, sweep, sweepSalvage, pause CheckEvery) dan
// guardLoop (guardDumpLocks, guardPromote, recordReplicas, lalu GuardAfter(GuardEvery)); Run kembali setelah keduanya selesai.
func (m *Monitor) guardLoop(ctx context.Context)
type warnLimiter struct{ mu sync.Mutex; last map[string]time.Time }
func (l *warnLimiter) allow(key string, now time.Time) bool   // true paling sering sekali per menit per key (pesan + teks error)
// internal/panel/monitor
const maxCheckEverySec = 300 // LoadSettings: nilai tersimpan > 300 → 300; < 10 atau tidak terbaca → bawaan
```

- Kompatibilitas (5.8): panel lama mengirim `check_every_sec` 3600 →
  agent baru menerima dan memakai 300 (log Info sekali). Panel baru tidak
  pernah mengirim > 300. `summary_every_sec` dan `lag_threshold_sec` tetap
  10–3600 di kedua sisi.

- [ ] **Step 1: Tulis test agent.** Rig memakai dua hook: `After` (loop lama) dan `GuardAfter` (loop penjaga), masing-masing ke kanalnya sendiri.
  - `TestGuardLoopRunsEveryFiveSecondsAtCheckEvery300`: `Configure{CheckEverySec:300, SummaryEverySec:300}`; loop lama meminta jeda `300*time.Second`; dalam satu jeda loop lama, 10 langkah `GuardAfter` masing-masing meminta `5*time.Second` dan setiap langkah memanggil `KillDumpLocks` (maxHold `time.Minute`), `GuardTick` dan `RecordReplicas` tepat sekali.
  - `TestGuardLoopSleepsAfterCompletion`: `GuardTick` palsu yang menahan 2 detik jam palsu; `GuardAfter` baru diminta setelah `GuardTick` kembali; tidak ada dua `GuardTick` yang tumpang tindih (penghitung konkuren maks 1).
  - `TestGuardWarningsRateLimited`: `GuardTick` selalu mengembalikan error yang sama; 20 putaran dalam 50 detik → 1 baris Warn; setelah jam maju 61 detik → baris kedua; error lain → baris sendiri.
  - `TestConfigureClampsCheckEveryTo300`: `check_every_sec` 3600 → diterima, jeda loop lama 300 s; 9 → `ParamError` "check_every_sec harus 10 sampai 300 detik"; `summary_every_sec` 3600 tetap diterima.
  - `TestSnapshotCommandRunsNoGuard` (baru): `monitor.snapshot` dan `monitor.configure` tidak memanggil penjaga.
  - Ubah `TestMonitorGuardsDumpLocksEveryCheck`, `TestMonitorRecordsReplicasSeenEveryCheck`, `TestMonitorRunsPromoteGuardEveryCheck` agar melangkahkan `GuardAfter` (bukan `After`); sweep tetap di loop lama (`TestMonitorSweepsDumpUsersHourly`, `TestMonitorSweepsSalvageDaily` tidak berubah).
- [ ] **Step 2: Tulis test panel.** `TestStoredCheckEveryAbove300ReadAs300` (setting `"3600"` → `CheckEvery == 300*time.Second`; `"9"` → bawaan 15 s); `TestSettingsFormRejectsCheckEveryAbove300` (POST 301 → 400 dengan pesan rentang 10–300; form `max="300"`, teks bantu "10–300. Seberapa sering agent memeriksa MySQL."). Interval ringkasan tetap 10–3600.
- [ ] **Step 3: Jalankan, pastikan gagal.** `go test ./internal/agent/monitor/ ./internal/panel/monitor/ ./internal/panel/web/ -run 'Guard|Clamp|CheckEvery|Settings' -race`
- [ ] **Step 4: Implementasi.** Loop lama tetap memegang `lastSweep`/`lastSalvageSweep` (hanya goroutine itu). Loop penjaga tidak menyentuh `checkMu`, `seq`, `prev`, `buf`. Warn dari `guardPromote` dan `recordReplicas` lewat `warnLimiter` (kunci = pesan + `err.Error()`); `guardDumpLocks` tetap Debug untuk error, Warn untuk kill.
- [ ] **Step 5: Jalankan** perintah Step 3 dan `go test ./internal/agent/... ./internal/panel/... -race -short`; lulus.
- [ ] **Step 6: Commit** — `git commit -m "feat(agent): fixed 5 s guard loop; check interval capped at 300 s in agent and panel"`

---

### Task 2: Kunci start: berkas, baris, pemeriksaan efektif, dan karantina yang menulisnya (R-031)

**Files:**
- Create: `internal/agent/drivers/mysql/startlock.go`, `internal/agent/drivers/mysql/startlock_test.go`
- Modify: `internal/agent/drivers/mysql/configure.go` (`configure` L151-160, `renderConfig` L289-308, `writeValidConfig` L312-343, `managedOptions` L486 → pakai `readManagedContent`), `quarantine.go` (`quarantine` L76-163, `requarantine` L168-193), `promote_lock.go` (`selfQuarantine` L501-527), `driver.go` (field cache), `internal/proto/mysql.go` (`QuarantineResult` L466)
- Test: `startlock_test.go`, `configure_test.go`, `quarantine_test.go`

**Interfaces:**
- Consumes: `Host.WriteConfig`, `Host.ValidateConfig`, `Host.Exec`, `managedConfigPath`, `writeFileAtomic` (`realhost.go`).
- Produces:

```go
// internal/proto
type StartLockState struct {
	Present           bool     `json:"present"`            // baris init_file agent ada di konfigurasi terkelola
	Locked            bool     `json:"locked"`             // StartLocked: baris + berkas + init_file terakhir = berkas agent + terbaca user mysqld (+ MAC terbukti)
	Reason            string   `json:"reason,omitempty"`   // mengapa Locked false (bahasa Indonesia)
	MACConfined       bool     `json:"mac_confined,omitempty"`
	File              string   `json:"file,omitempty"`
	OperatorInitFiles []string `json:"operator_init_files,omitempty"` // init_file lain yang terbaca sebelum baris agent (tidak dijalankan selama karantina)
}
// QuarantineResult += 
	StartLocked       bool     `json:"start_locked"`
	StartLockReason   string   `json:"start_lock_reason,omitempty"`
	MACConfined       bool     `json:"mac_confined,omitempty"`
	OperatorInitFiles []string `json:"operator_init_files,omitempty"`

// internal/agent/drivers/mysql/startlock.go
const StartLockFile = "dbpanel-start-lock.sql"
const startLockContent = "SET GLOBAL super_read_only=ON;\nSET PERSIST read_only=ON;\nSET PERSIST super_read_only=ON;\n"
func StartLockPath(l Layout) (string, error)          // IncludeDir, else filepath.Dir(EvalSymlinks(ConfigFile)); error bila kosong atau di dalam DataDir
func readManagedContent(l Layout) (content string, exists bool, err error) // isi blok (tanpa penanda) atau seluruh berkas include
func withStartLockLine(content, path string) string    // tambah "init_file=<path>" di akhir isi, atau ganti satu baris init_file yang ada; baris lain identik byte per byte; isi kosong → "[mysqld]\ninit_file=<path>\n"
func withoutStartLockLine(content, path string) string // hapus hanya baris yang kuncinya init_file/init-file dan nilainya path
func startLockLinePresent(l Layout) (bool, error)
func writeStartLock(ctx context.Context, h Host) error // berkas (writeFileAtomic, 0644, chown 0:0 bila root) lalu baris (writeValidConfig); idempoten
var managedConfigMu sync.Mutex                         // dipegang writeValidConfig: semua penulis konfigurasi terkelola dalam satu proses
type effectiveOpts struct{ InitFiles []string; User string; LogError string }
func printDefaultsArgs(l Layout) []string              // len(l.ConfigFiles)==1 → {"--defaults-file="+l.ConfigFile, "--print-defaults"}; selain itu {"--print-defaults"}. Tidak pernah argumen lain.
func readEffectiveOptions(ctx context.Context, h Host) (effectiveOpts, error) // h.Exec(MysqldPath, printDefaultsArgs...); --init_file/--init-file berurutan; User = --user terakhir, else "mysql" bila euid 0, else user proses
func macConfinement(l Layout) string                   // "apparmor" | "selinux" | ""
var readXattr = func(path, name string) (string, error) { /* syscall.Getxattr */ } // test mengganti
func readableBy(path, user string) (bool, error)       // izin mode biasa + bit x setiap direktori induk, untuk uid/gid user (os/user)
func startLockStatus(ctx context.Context, h Host) proto.StartLockState
// configure.go
func renderConfig(role string, serverID uint32, bindAll bool, bl binlogBlock, startLock string) string // startLock != "" → baris init_file=<startLock> terakhir di blok
```

- Aturan `configure()`: `startLock` = path agent bila `prev["init_file"]`
  sama dengan `StartLockPath` (baris sedang ada), apa pun perannya; jadi
  penulisan blok lain tidak pernah membuang kunci yang belum dilepas.
- Karantina: `quarantine` dengan MySQL mati: pin (`SetQuarantinedAt`,
  `ClearLock`) dulu, lalu `writeStartLock`; kegagalan tulis kunci tidak
  membatalkan karantina, hanya membuat `StartLocked=false` dengan alasan.
  Dengan MySQL hidup: `selfQuarantine` (PERSIST ON, pin) memanggil
  `writeStartLock` di akhirnya, sehingga karantina oleh penjaga kunci
  promote (`guardExpired`) juga menulis kunci.
  `requarantine`: `writeStartLock` juga. Semua jalur mengisi
  `QuarantineResult.StartLocked/StartLockReason/MACConfined/OperatorInitFiles`
  dari `startLockStatus`.
- Alasan `Locked=false` (teks perencana): "baris init_file belum ada di
  konfigurasi MySQL", "berkas kunci <path> tidak ada", "init_file lain
  dibaca sesudah baris agent: <path>", "berkas kunci tidak bisa dibaca
  pengguna <user>", "berkas kunci <path> mungkin ditolak profil
  AppArmor/SELinux mysqld", "konfigurasi efektif MySQL tidak bisa dibaca:
  <err>".
- Kompatibilitas (5.8): panel lama mengabaikan field baru
  `QuarantineResult`. Agent lama tidak mengirimnya; panel baru
  memperlakukan karantina tanpa `StartLock` di laporan sebagai tidak
  terjamin (Task 7).

- [ ] **Step 1: Tulis test unit.**
  - `TestStartLockPathRule`: IncludeDir `/x/conf.d` → `/x/conf.d/dbpanel-start-lock.sql`; tanpa IncludeDir, ConfigFile symlink `a/my.cnf → b/real.cnf` → `b/dbpanel-start-lock.sql`; hasil di dalam DataDir → error; tidak pernah berakhiran `.cnf`.
  - `TestStartLockLineInsertedWithoutTouchingOtherLines`: isi blok dengan komentar, spasi aneh dan `read_only=ON` → `withStartLockLine` hanya menambah satu baris; `withoutStartLockLine(withStartLockLine(c,p),p) == c` byte per byte; `init_file` operator (path lain) di blok tidak disentuh `withoutStartLockLine`.
  - `TestPrintDefaultsArgsExact`: satu ConfigFile → persis `[--defaults-file=F --print-defaults]`; tiga → persis `[--print-defaults]`.
  - `TestEffectiveInitFileOrder`: keluaran `--print-defaults` palsu `--init_file=/op/a.sql --user=nobody --init_file=<agent>` → Locked true (bila berkas ada dan terbaca), `OperatorInitFiles == ["/op/a.sql"]`; urutan terbalik → Locked false, alasan memuat `/op/a.sql`.
  - `TestMACAllowedPath`: `macConfinement` dipalsukan "apparmor": `/etc/mysql/mysql.conf.d/x.sql` lolos, `/etc/x.sql` → Locked false; "selinux": `readXattr` palsu `system_u:object_r:mysqld_etc_t:s0` lolos, `…:etc_t:s0` → Locked false.
  - `TestRenderConfigKeepsStartLockLine`: `renderConfig(roleReplica,…, "/p/dbpanel-start-lock.sql")` memuat `init_file=/p/dbpanel-start-lock.sql` sebagai baris terakhir; `startLock == ""` → tidak ada `init_file`.
- [ ] **Step 2: Tulis test mysqld sungguhan** (`testmysql`, layout blok di `my.cnf`, berkas di direktori instance).
  - `TestStartLockFileBeforeLine`: `ValidateConfig` dibuat gagal (opsi tak dikenal di my.cnf sebelum blok) → `writeStartLock` error, berkas ada, baris tidak ada; perbaiki my.cnf → `writeStartLock` sukses, keduanya ada. Tidak ada keadaan baris-tanpa-berkas di titik mana pun.
  - `TestStartLockedFalseWhenUnreadableByMysqlUser` (skip bila bukan root): chmod berkas 0600 → `startLockStatus.Locked == false`, alasan "tidak bisa dibaca pengguna nobody".
  - `TestQuarantinedWhileDownStartsReadOnlyUnderWrites` (5.10 butir 1): instance dengan akun `app`@`127.0.0.1` (INSERT ke `w.t`); `SET PERSIST read_only=OFF; SET PERSIST super_read_only=OFF` (server pernah dipromote); stop; seen host dicatat; `mysql.quarantine` → `Applied=false`, `StartLocked=true`; goroutine menulis `INSERT INTO w.t` lewat TCP tiap 10 ms dengan connect-timeout 1 s sejak sebelum start sampai 3 detik setelah siap; `svc.Start` → 0 INSERT sukses, `SELECT COUNT(*) FROM w.t` = 0, `@@super_read_only=1`, `@@read_only=1`, `persisted_variables` keduanya ON, tanpa satu pun `GuardTick`.
  - `TestQuarantineUpWritesStartLock`: karantina dengan MySQL hidup → `Applied`, `StartLocked`, baris dan berkas ada; `requarantine` idempoten (isi blok tidak berubah).
  - `TestConfigureKeepsPresentStartLockLine`: baris ada, `mysql.configure` role replica → blok baru memuat `init_file` lagi.
- [ ] **Step 3: Jalankan, pastikan gagal.** `go test ./internal/agent/drivers/mysql/ -run 'StartLock|RenderConfig|PrintDefaults|EffectiveInit|MACAllowed|Quarantine' -timeout 20m`
- [ ] **Step 4: Implementasi.** `writeValidConfig` memegang `managedConfigMu`. `readEffectiveOptions` memakai `h.Exec` dengan buffer; `--print-defaults` tidak membuka datadir. Field `startLockMu sync.Mutex; startLockState proto.StartLockState` di `Driver` diisi setiap `startLockStatus` dihitung (dibaca Task 3).
- [ ] **Step 5: Jalankan** Step 3 dan `go test ./internal/agent/... -race -short`; lulus.
- [ ] **Step 6: Commit** — `git commit -m "feat(agent): start lock via init_file on every quarantine, verified with --print-defaults"`

---

### Task 3: Kunci start: penjaga menjaga, melapor dan melepas; CLI `set-role`; promote menolak

**Files:**
- Modify: `internal/agent/drivers/mysql/promote_lock.go` (`GuardTick` L301-350), `quarantine.go` (`guardQuarantined` L236-294, `settleRebuilt` L403), `startlock.go`, `promote.go` (handler `mysql.promote` L76-140), `health.go` (L48), `driver.go`, `internal/agent/cli.go` (`mainSetRole` L239-254), `internal/proto/mysql.go`, `internal/proto/health.go`
- Test: `startlock_guard_test.go` (baru), `quarantine_test.go`, `promote_test.go`, `health_test.go`, `internal/agent/cli_test.go`

**Interfaces:**
- Consumes: Task 2 `writeStartLock`, `startLockStatus`, `withoutStartLockLine`, `StartLockPath`, `readEffectiveOptions`.
- Produces:

```go
// internal/proto
const EventQuarantineStart = "monitor.quarantineStart"
type QuarantineStartEvent struct {
	Result   string `json:"result"`    // "late" | "bypassed" | "ok" (late didahulukan bila keduanya)
	Bypassed bool   `json:"bypassed"`  // @@init_file bukan berkas agent padahal berkas lebih tua dari mysqld yang berjalan
	InitFile string `json:"init_file"` // @@init_file yang terbaca
}
const (QuarantineStartLate = "late"; QuarantineStartBypassed = "bypassed"; QuarantineStartOK = "ok")
const EventQuarantineStartFailed = "monitor.quarantineStartFailed"
type QuarantineStartFailedEvent struct{ File string `json:"file"`; At time.Time `json:"at"` } // waktu baris log
// HealthSnapshot += StartLock *StartLockState `json:"start_lock,omitempty"` // non-nil bila peran quarantined atau baris masih ada

// internal/agent/drivers/mysql
const startLockRetry = time.Minute
func (d *Driver) guardStartLock(ctx context.Context, h Host, now time.Time) // GuardTick memanggilnya pertama, di bawah promoteMu, untuk peran apa pun
func releaseStartLock(ctx context.Context, h Host) error // baris (writeValidConfig dengan withoutStartLockLine) lalu berkas; berkas tanpa baris → hapus berkas saja
func ReleaseStartLock(ctx context.Context, h Host) error // = releaseStartLock; dipakai CLI
func persistedReadOnlyOn(ctx context.Context, db *sql.DB) (bool, error) // performance_schema.persisted_variables read_only=ON dan super_read_only=ON
func scanBootstrapFailure(tail []byte, file string, since time.Time) (time.Time, bool) // baris "Failed to open the bootstrap file <file>" dengan cap waktu RFC3339 di awal baris > since
func (d *Driver) StartLock() *proto.StartLockState // cache untuk Snapshot
// Driver += startLockProved bool (SET PERSIST ON sukses pada MySQL hidup sejak karantina, run agent ini);
//           startLockNextTry time.Time; bootFailAt time.Time (kejadian gagal-menyala terakhir yang dikirim)
```

- Aturan `guardStartLock` per putaran (spek 5.2, keputusan perencana 4–6):
  - Peran `quarantined`: berkas dan baris harus ada; yang hilang ditulis
    ulang (`writeStartLock`), kegagalan dicoba lagi tidak lebih cepat dari
    `startLockNextTry`. Status di-cache. Bila MySQL tidak menjawab: baca
    ekor 64 KiB log error (`LogError` dari opsi efektif; relatif terhadap
    DataDir; tanpa `log_error` → tidak dipantau) dan, bila
    `scanBootstrapFailure(…, since = mtime berkas kunci)` menemukan baris
    yang lebih baru dari `bootFailAt`, kirim `EventQuarantineStartFailed`.
  - Peran `master` dan baris ada: lepas sekarang (root sudah memutuskan).
  - Peran `replica`/`unassigned` dan baris ada: lepas hanya bila MySQL
    menjawab dan (`startLockProved` atau `persistedReadOnlyOn`). Penjaga
    tidak pernah menjalankan `SET` apa pun di cabang ini.
  - Berkas ada tanpa baris dan peran bukan `quarantined`: hapus berkas.
- `guardQuarantined` pada transisi (MySQL menjawab setelah mati, restart,
  atau agent baru mulai) dan tidak sedang menunda untuk muatan bangun
  ulang: baca flag → selalu `persistReadOnly` → `startLockProved=true` →
  baca `@@global.init_file` → kirim `EventQuarantineStart`: `late` bila
  flag sempat mati; `Bypassed` bila `@@init_file` ≠ path agent **dan**
  mtime berkas kunci < (sekarang − Uptime − 2 s); selain itu `ok`.
  `EventQuarantined` tetap seperti sekarang.
- `mysql.promote` (sebelum job dimulai): bila baris ada → (bukti atau
  `persistReadOnly`, kandidat memang replika) lalu `releaseStartLock`;
  gagal → `ParamError` "server ini masih membawa kunci start dari
  karantina dan kunci itu tidak bisa dilepas: <sebab>; promote ditolak"
  (teks perencana).
- CLI `set-role master`: `cliMySQLHost(dir)` → `ReleaseStartLock` dulu;
  gagal → stderr "kunci start tidak bisa dilepas (<sebab>); peran tidak
  diubah" (teks perencana), keluar 1, pin tidak disentuh. Peran lain:
  pin saja, kunci tidak disentuh.
- Kompatibilitas (5.8): panel lama mengabaikan dua kejadian baru (tipe
  tak dikenal) dan field `start_lock`; panel baru dengan agent lama tidak
  mendapat keduanya (Task 7 menanganinya).

- [ ] **Step 1: Tulis test** (mysqld sungguhan kecuali disebut).
  - `TestGuardWritesStartLockForOldAgentQuarantine` (5.10): pin `quarantined` + sumber ditulis lewat `Pins` tanpa baris/berkas (agent lama); satu `GuardTick` → baris dan berkas ada, `StartLock().Locked`.
  - `TestGuardRewritesMissingFileAndLine`: hapus berkas → putaran berikut menulisnya; hapus baris → ditulis lagi; `ValidateConfig` gagal → tidak ada percobaan baru sebelum `now+1m`, ada sesudahnya.
  - `TestStartLockReleaseInterruptedNeverLeavesLineWithoutFile` (5.10): hook gagal saat menulis blok tanpa baris → baris dan berkas tetap; hook gagal saat hapus berkas → berkas tanpa baris; putaran berikut menghapus berkas; di setiap titik `lineWithoutFile == false`.
  - `TestSetRoleReplicaWhileDownKeepsStartLock` (5.10): MySQL mati, CLI `set-role replica` → keluar 0; tiga `GuardTick` → baris dan berkas tetap.
  - `TestSetRoleMasterReleasesFirst`: CLI `set-role master` → baris lalu berkas hilang, pin master; dengan `writeValidConfig` gagal → keluar 1, pesan "peran tidak diubah", pin tetap `quarantined`.
  - `TestReleaseNeedsPersistedOn`: peran replica, MySQL hidup, `RESET PERSIST` kedua flag, agent baru (tanpa bukti) → kunci tetap; `SET PERSIST` ON → putaran berikut melepas.
  - `TestQuarantineStartEvents`: (a) karantina saat mati lalu start → `ok`; (b) agent berhenti, `SET GLOBAL super_read_only=OFF`, agent baru → `late`; (c) MySQL hidup tanpa baris, tulis kunci, `os.Chtimes` berkas 1 jam lalu, agent baru → `bypassed`; (d) seperti (c) tanpa Chtimes → `ok`.
  - `TestBootstrapFailureEventFromErrorLog`: log palsu berisi baris lama (sebelum mtime berkas) dan baris baru `2026-11-11T03:18:19.026539Z 0 [ERROR] [MY-010455] [Server] Failed to open the bootstrap file <path>` → tepat satu kejadian, `At` = waktu baris; putaran berikut tanpa baris baru → tidak ada kejadian.
  - `TestReleasedServerRestartsWithoutInitFile` (Review Focus 1): karantina → `settleRebuilt` (peran replica) → putaran lepas → `SET PERSIST super_read_only=OFF, read_only=OFF`; restart → `@@init_file` kosong, server bisa ditulis, berkas tidak ada.
  - `TestPromoteRefusedWhileStartLockCannotBeReleased`: replika dengan baris dan `writeValidConfig` gagal → `mysql.promote` `ParamError` memuat "kunci start"; tanpa kegagalan → kunci dilepas lalu job promote dimulai.
  - `TestSnapshotReportsStartLock` (unit): peran quarantined → `StartLock != nil`; master tanpa baris → nil.
- [ ] **Step 2: Jalankan, pastikan gagal.** `go test ./internal/agent/... -run 'StartLock|SetRole|QuarantineStart|Bootstrap|Released|PromoteRefused|SnapshotReports' -timeout 25m`
- [ ] **Step 3: Implementasi.** Hook test sebagai `var` paket (`writeConfigHook`, `removeStartLockFile`) dengan `SetX` di `export_test.go` seperti `SetReadPeer`.
- [ ] **Step 4: Jalankan** Step 2 dan `go test ./internal/agent/... -race -short`; lulus.
- [ ] **Step 5: Commit** — `git commit -m "feat(agent): guard keeps, reports and releases the start lock; set-role master releases it first"`

---

### Task 4: Replika tercatat lebih cepat, dengan port (R-032)

**Files:**
- Modify: `internal/agent/drivers/mysql/promote_files.go` (`RecordReplicas` L312-343, `readSeen`), `status.go` (dekat `binlogDumpHosts` L212), `promote_lock.go` (`lockForPromote` L155-208), `driver.go` (`SeenPortsPath`, register), `internal/proto/mysql.go`
- Test: `promote_files_test.go`, `promote_lock_test.go`, `status_test.go`

**Interfaces:**
- Produces:

```go
const SeenPortsFile = "/var/lib/dbpanel/replica-ports-seen" // Driver.SeenPortsPath menggantinya di test
// Format: satu baris per port "<port> <RFC3339>", diurutkan, dipangkas setelah 1 tahun (seenCutoff), ditulis lewat writePin.
// Migrasi: berkas host lama (replicas-seen) tidak diubah formatnya; berkas port yang belum ada dibaca kosong dan terisi pada putaran penjaga berikutnya.
func RecordReplicaPortsSeen(path string, ports []int, now time.Time) error
func readSeenPorts(path string) (map[int]time.Time, error)   // baris rusak dilewati; port 1..65535
func replicaPorts(ctx context.Context, db *sql.DB) ([]int, error) // SHOW REPLICAS kolom Port; error → host tetap dicatat, error di-log
func (d *Driver) recordSeen(hosts []string, ports []int, now time.Time) error // di bawah seenMu; tulis bila himpunan berubah atau seenRefresh lewat
// "mysql.recordReplicas": tanpa parameter (params bukan kosong/null/{} → ParamError); menjalankan RecordReplicas sekarang
type RecordReplicasResult struct{ Hosts int `json:"hosts"`; Ports int `json:"ports"` }
```

- `lockForPromote`: setelah `candidateConnected` ok, catat semua host
  `binlogDumpHosts` saat itu dan `replicaPorts` lewat `recordSeen`
  sebelum menulis kunci; kegagalan catat tidak menggagalkan kunci (log).
- Kompatibilitas (5.8): panel baru memanggil `mysql.recordReplicas` pada
  agent lama → `unknown_command`; panel mengabaikannya (Task 8). Agent
  versi lama yang dipasang kembali membaca `replicas-seen` seperti biasa
  dan tidak tahu berkas port.

- [ ] **Step 1: Tulis test.** `TestSeenPortsFileFormatAndPrune` (dua port, satu > 1 tahun dibuang; baris `abc x` dilewati), `TestOldSeenFileReadUnchanged` (berkas host format lama dibaca sama; `RecordReplicas` tidak menulis ulang berkas host bila himpunan host sama), `TestRecordReplicasStoresPortsFromShowReplicas` (testmysql dua instance, replika tersambung → berkas port memuat port replika), `TestLockForPromoteRecordsCandidate` (seen kosong; `lockForPromote` sukses → host kandidat ada di berkas tanpa tik monitor), `TestRecordReplicasCommandTakesNoData` (`{"hosts":["10.0.0.9"]}` → ParamError; tanpa param → hasil, berkas tidak memuat host dari panel).
- [ ] **Step 2–4.** Gagal → implementasi → `go test ./internal/agent/drivers/mysql/ -run 'Seen|RecordReplicas|LockForPromoteRecords' -timeout 20m` lulus.
- [ ] **Step 5: Commit** — `git commit -m "feat(agent): record replica hosts and ports from the guard loop, lockForPromote and mysql.recordReplicas"`

---

### Task 5: Penjaga kunci promote membaca semua replika yang terbaca saat kunci dipasang (R-033)

**Files:**
- Modify: `internal/agent/drivers/mysql/promote_lock.go` (`lockForPromote`, `guardExpired` L375-438, `writableSeenReplica` L459-480 → diganti, `keepLock` L485), `promote_files.go` (`PromoteLock`, `lockJSON`), `internal/agent/cli.go` (perintah baru, `usage`), `internal/proto/mysql.go` (`LockExpiredEvent`)
- Test: `promote_lock_test.go`, `promote_files_test.go`, `internal/agent/cli_test.go`

**Interfaces:**
- Consumes: Task 4 `readSeen`, `readSeenPorts`.
- Produces:

```go
// PromoteLock += Readable []string // "host:port" yang terbaca saat kunci dipasang (bukan kandidat); lockJSON "readable,omitempty"
const unlockTimeout = 10 * time.Second
const maxPeerReads = 16 // bacaan paralel bersamaan
func readableAtLock(ctx context.Context, cred proto.PeerCred, candAddr string, candPort int, hosts []string, ports []int) []string
	// setiap host tercatat (kecuali candAddr pada candPort) × ({candPort} ∪ ports), paralel, readPeer 5 s; yang Reachable masuk daftar, terurut
func readListed(ctx context.Context, cred proto.PeerCred, list []string, uuid string) (unreadable []string, writable string)
	// writable: entri pertama yang !SuperReadOnly dan gtid_executed memuat uuid; ctx habis → sisa dianggap unreadable
// LockExpiredEvent += Hosts []string `json:"hosts,omitempty"` // host:port yang tidak terbaca (kunci dipertahankan)
func (p *Pins) ForgetLockHost(host string) (removed int, err error) // hapus entri Readable dengan host itu (EqualFold)
func ForgetReplica(seenPath, host string) (bool, error)             // hapus host dari replicas-seen
// CLI: dbpanel agent forget-replica <host> [--dir DIR]
```

- `lockForPromote` sudah menolak kunci bila kandidat tidak terbaca
  (`readPeer` kandidat, L180); perilaku itu dipertahankan dan diberi test
  `TestLockRefusedWhenCandidateUnreadable` di task ini (spek 5.4).
  `Readable` dihitung setelah bacaan kandidat itu, sebelum kunci ditulis.
- `guardExpired`, cabang "dua bacaan read-only": `readListed(lock.Readable)`
  menggantikan `writableSeenReplica`. Ada `writable` → `selfQuarantine`
  (seperti sekarang). Ada `unreadable` → kunci tetap, `nextRead =
  now+lockRecheck`, `keepLock` dengan `Hosts = unreadable` (laporan
  pertama lalu tiap `keptEvery`). Semua terbaca dan read-only → buka
  kunci dengan `context.WithTimeout(context.WithoutCancel(ctx),
  unlockTimeout)` untuk `unlockMaster` dan `ClearLock`.
- Kunci tanpa `Readable` (dipasang agent lama) → daftar kosong: perilaku
  sekarang tanpa pembacaan replika lain.
- Teks CLI (teks perencana): `forget-replica` mencetak "replika <host>
  dihapus dari catatan (N entri kunci promosi ikut dihapus)" atau "<host>
  tidak ada di catatan".
- Kompatibilitas (5.8): agent lama membaca berkas kunci baru (field
  tambahan diabaikan `json.Unmarshal`). Panel lama menerima `hosts` dan
  mengabaikannya (pesan "kept" lama).

- [ ] **Step 1: Tulis test** (mysqld sungguhan: master A, kandidat B, replika lain C; kunci ditulis langsung dengan `Readable=["127.0.0.1:<portC>"]` dan `ArmGuard` seperti `newLockRig`; `readPeer` asli; akun `dbpanel_repl` dibuat di B dan C).
  - `TestLockKeptWhileListedReplicaDownThenOpens` (5.10): B dan C read-only; deadline + grace; stop C → kunci tetap, `super_read_only` A ON, satu kejadian `kept` dengan `Hosts == ["127.0.0.1:<portC>"]`; tik +30 s → tidak ada bacaan; start C → tik setelah recheck + grace → A terbuka, kejadian `unlocked`.
  - `TestPartialReplicaUnreadableAtLockDoesNotHoldLock` (5.10): C tanpa `dbpanel_repl`; `readableAtLock` → C tidak masuk; deadline + grace → A terbuka.
  - `TestReadableAtLockParallel`: 20 peer palsu masing-masing 5 s → selesai < 15 s; port kandidat dan port tercatat sama-sama dicoba.
  - `TestUnlockUsesOwnContext`: ctx tik yang tinggal 1 ms setelah bacaan → kunci tetap terbuka dengan konteks sendiri (A RW, berkas kunci hilang).
  - `TestForgetReplicaCLI`: seen memuat host C, kunci `Readable` memuat C → keduanya hilang, pesan memuat jumlah.
- [ ] **Step 2–4.** Gagal → implementasi → `go test ./internal/agent/... -run 'Lock|Readable|Forget|Guard' -timeout 25m` lulus.
- [ ] **Step 5: Commit** — `git commit -m "feat(agent): promote lock guard reads every replica readable at lock time and keeps the lock while one is not"`

---

### Task 6: Daftar koneksi yang jujur dan koneksi agent bertanda (R-029, R-034)

**Files:**
- Modify: `internal/agent/drivers/mysql/conn.go` (`DSN` L25-47), `peer.go` (`ReadPeer` L59), `tls.go` (`checkServedCert` L683), `connections.go` (seluruh kueri L39-117), `internal/proto/mysql.go` (`ConnectionsResult`, `ConnectionRow` L549-565)
- Test: `connections_test.go`, `conn_test.go` (buat bila belum ada), `peer_test.go`

**Interfaces:**
- Produces:

```go
const agentProgramName = "dbpanel-agent"
// DSN, ReadPeer, checkServedCert: cfg.ConnectionAttributes = "program_name:dbpanel-agent".
// Semua *sql.DB agent (Host.Open → OpenDB → openWith → DSN; install.go, connect.go lewat openWith; testmysql lewat DSN) ikut bertanda.
// Proses anak (mysql, mysqldump, mysqlbinlog) tidak bertanda (lihat Keputusan perencana 12).
type ConnectionsResult struct {
	Rows    []ConnectionRow `json:"rows"`
	TakenAt time.Time       `json:"taken_at"`
	Typed      bool   `json:"typed"`                // baris membawa Kind (agent 5a); false dari agent lama → panel: Incomplete
	Source     string `json:"source,omitempty"`     // "performance_schema" | "information_schema"
	Unreadable string `json:"unreadable,omitempty"` // tidak ada sumber yang terbaca: alasannya
	Incomplete string `json:"incomplete,omitempty"` // Performance_schema_thread_instances_lost > 0: alasannya
	LocalAdmin int    `json:"local_admin"`          // sesi akun agent dari server ini yang bukan milik agent
	AgentIndistinct bool `json:"agent_indistinct,omitempty"` // P_S mati: semua sesi lokal akun agent dihitung
}
type ConnectionRow struct { Host, User, DB string; Count int; AdminAccount bool
	Kind       string `json:"kind"`                  // "sql" | "x" | "lain"
	LocalAdmin bool   `json:"local_admin,omitempty"` // akun agent dari server ini (sesi admin lokal)
}
func connKind(threadName string) string // thread/sql/one_connection → sql; thread/mysqlx/worker → x; lainnya → lain
func serviceAccount(user string) bool   // user == replUserName || dumpAccountRe.MatchString(user)
```

- Kueri P_S (`@@performance_schema = 1`): `performance_schema.threads`
  `TYPE='FOREGROUND' AND PROCESSLIST_ID IS NOT NULL AND PROCESSLIST_USER IS
  NOT NULL AND PROCESSLIST_USER NOT IN ('system user','event_scheduler')
  AND IFNULL(PROCESSLIST_COMMAND,'') NOT LIKE 'Binlog Dump%' AND
  PROCESSLIST_ID <> CONNECTION_ID()`, membawa `NAME` dan apakah ada
  `session_connect_attrs` `program_name`=`dbpanel-agent` untuk
  `PROCESSLIST_ID` itu. Di Go: `serviceAccount` dibuang; akun agent dari
  host lokal (`''`, `localhost`, `127.0.0.1`, `::1`, `::ffff:127.0.0.1`)
  bertanda → disembunyikan; tanpa tanda → baris `LocalAdmin`; akun agent
  dari host lain → `AdminAccount` (juga `dbpanel_admin`).
  `unauthenticated user` dihitung. Lalu `SHOW GLOBAL STATUS LIKE
  'Performance_schema_thread_instances_lost'` > 0 → `Incomplete`
  "sebagian thread tidak tercatat di performance_schema" (teks perencana).
- P_S mati: `information_schema.PROCESSLIST` dengan pengecualian yang
  sama (tanpa `NAME`); `Kind="lain"`; semua sesi lokal akun agent →
  `LocalAdmin` dan `AgentIndistinct=true`. Kedua kueri gagal →
  `Unreadable` (bukan error perintah).
- Kompatibilitas (5.8): panel lama membaca `rows` seperti biasa (field
  baru diabaikan). Panel baru: hasil tanpa `typed` = `Incomplete` (Task 9).

- [ ] **Step 1: Tulis test** (mysqld sungguhan; X Protocol diuji lewat baris thread tiruan karena `testmysql` memakai `mysqlx=OFF` dan repo tidak punya klien X — sebutkan di laporan task).
  - `TestConnectionsIdleGTIDServerCompleteAndEmpty` (5.10): instance tanpa klien selain agent (Host.Open dua kali dibiarkan terbuka) → `Rows` kosong, `Typed`, `Unreadable==""`, `Incomplete==""`, `LocalAdmin==0`.
  - `TestConnectionsPerformanceSchemaOff` (5.10): `ExtraArgs: []string{"--performance-schema=OFF"}`; dua sesi `app` → `Source=="information_schema"`, Count 2, `Kind=="lain"`; sesi root lokal (akun agent) tanpa tanda → `LocalAdmin>0`, `AgentIndistinct`.
  - `TestConnKindFromThreadName`: `thread/mysqlx/worker` → `x`; `thread/sql/one_connection` → `sql`; `thread/sql/compress_gtid_table` → `lain`; baris tiruan `thread/mysqlx/worker` dengan USER `app` masuk hitungan dan tidak dibuang filter.
  - `TestConnectionsShowsDbpanelAdminFromOtherHost`: akun `dbpanel_admin`@`%` dari alamat non-loopback (`nonLoopbackIPv4`) → baris `AdminAccount`; `dbpanel_repl` dan `dbpanel_dump_0123abcd` tidak tampil; `dbpanel_lain` tampil.
  - `TestConnectionsUntaggedLocalAgentAccountCounted`: sesi root lewat soket dengan DSN tanpa atribut → baris `LocalAdmin`, `LocalAdmin==1`; sesi lewat `Host.Open` tidak tampil.
  - `TestAgentConnectionsCarryProgramName`: `DSN(...)` memuat `connectionAttributes=program_name%3Adbpanel-agent`; sesi `Host.Open` dan `ReadPeer` punya `program_name=dbpanel-agent` di `session_connect_attrs`.
  - Ubah `TestConnectionsHidesPanelAndReplicationThreads` dan `TestConnectionsHidesAgentAccount` ke aturan baru.
- [ ] **Step 2–4.** Gagal → implementasi → `go test ./internal/agent/drivers/mysql/ -run 'Connections|ConnKind|ProgramName|Peer' -timeout 20m` lulus.
- [ ] **Step 5: Commit** — `git commit -m "feat(agent): complete connection list with kinds, completeness and tagged agent sessions"`

---

### Task 7: Panel: peringatan kunci start, kunci promote yang dipertahankan, kejadian baru

**Files:**
- Modify: `internal/panel/monitor/alerts.go` (konstanta L18-51, `AllKinds` L54, `alertRules`), `internal/panel/monitor/promote.go` (`handlePromoteEvent` L337-363, `closeNotices` L437-470), `monitor.go` (`HandleEvent` L243), `internal/panel/notify/message.go` (`critKinds`, `kindNames`, `masterKinds`), `internal/panel/web/handlers_alerts.go` (`alertKindLabels` L20)
- Test: `internal/panel/monitor/alerts_test.go`, `internal/panel/monitor/promote_test.go`, `internal/panel/notify/message_test.go`

**Interfaces:**
- Consumes: Task 3 `proto.StartLockState`, `EventQuarantineStart`, `EventQuarantineStartFailed`; Task 5 `LockExpiredEvent.Hosts`.
- Produces:

```go
const (
	KindMasterStartUnguarded = "master_start_unguarded" // Kritis; aturan dari laporan
	KindMasterStartLate      = "master_start_late"      // Kritis; kejadian
	KindMasterStartBypassed  = "master_start_bypassed"  // Kritis; kejadian
	KindMasterStartBlocked   = "master_start_blocked"   // Kritis; kejadian
	KindMasterLockKept       = "master_lock_kept"       // Kritis; lockExpired kept dengan Hosts
)
// AllKinds: lima jenis ditambahkan setelah KindMasterUnpinned, urutan di atas.
```

- Buka/tutup (pesan persis dari Global Constraints; tambahan ditandai):
  - `master_start_unguarded`: server berperan `quarantined` di store dan
    laporan terakhir (MySQL hidup atau tidak) punya `StartLock == nil`
    (agent lama) atau `!StartLock.Locked`. Pesan: teks 5.2 pertama, lalu
    " (" + `Reason` + ")" bila ada (teks perencana). Tutup saat `Locked`
    atau server tidak lagi `quarantined`.
  - `master_start_late`: `QuarantineStartEvent.Result == "late"`. Tutup
    saat server tidak lagi `quarantined`, atau saat hasil "Periksa data
    tertinggal" tersimpan setelah peringatan dibuka (`LeftoverOf` promosi
    terbaru, waktu periksa > `OpenedAt`).
  - `master_start_bypassed`: `Bypassed` true. Tutup oleh
    `QuarantineStartEvent` berikut dengan `Bypassed` false, atau server
    tidak lagi `quarantined`.
  - `master_start_blocked`: `EventQuarantineStartFailed`. Pesan: teks 5.2
    keempat + " (" + File + "). Pastikan berkas itu ada dan boleh dibaca
    pengguna mysql (juga oleh AppArmor/SELinux), lalu pastikan agent
    berjalan: agent menulis ulang berkas itu dalam beberapa detik. Jangan
    menghapus baris init_file dari konfigurasi MySQL." (teks perencana).
    Tutup oleh `EventQuarantineStart` apa pun atau server tidak lagi
    `quarantined`.
  - `master_lock_kept`: `LockExpiredEvent{Action:"kept", Hosts non-kosong}`.
    Pesan (teks perencana): "Batas waktu kunci promote di master habis,
    tetapi master tetap dikunci karena replika berikut tidak terbaca:
    <hosts>. Kunci terbuka sendiri setelah semua replika itu terbaca dan
    masih hanya-baca. Bila replika itu sudah tidak dipakai, jalankan di
    server master: sudo dbpanel agent forget-replica <host>. Untuk membuka
    kunci sekarang: sudo dbpanel agent unlock-master". Tutup seperti
    `lockKeptOver` (unlocked, quarantined, master bisa ditulis). `kept`
    tanpa `Hosts` tetap `master_lock_expired`.
- Kompatibilitas (5.8): agent lama → `StartLock` nil pada server
  `quarantined` → `master_start_unguarded` terbuka (sesuai spek).

- [ ] **Step 1: Tulis test** `TestStartUnguardedFromSnapshot` (quarantined + `StartLock{Locked:false, Reason:"x"}` → buka dengan teks 5.2; `Locked:true` → tutup; master biasa tanpa StartLock → tidak ada), `TestOldAgentQuarantineUnguarded` (laporan tanpa `start_lock` pada server quarantined → buka), `TestQuarantineStartLateAndBypassed` (late → `master_start_late`; bypassed → `master_start_bypassed`; ok berikutnya menutup bypassed, bukan late), `TestStartLateClosesAfterLeftoverCheck`, `TestStartBlockedOpensAndStartCloses`, `TestLockKeptWithHostsIsCritical` (pesan memuat host dan `sudo dbpanel agent unlock-master`; `notify.Critical` true; `unlocked` menutup), `TestNewKindsHaveLabels` (setiap `AllKinds()` punya label di `alertKindLabels` dan `kindNames`).
- [ ] **Step 2–4.** Gagal → implementasi → `go test ./internal/panel/monitor/ ./internal/panel/notify/ ./internal/panel/web/ -race` lulus.
- [ ] **Step 5: Commit** — `git commit -m "feat(monitor): critical alerts for start lock, late or bypassed start, failed start and kept promote lock"`

---

### Task 8: Panel: panggil `mysql.recordReplicas`, kandidat promote dengan kunci start, halaman master lama

**Files:**
- Modify: `internal/panel/orchestrator/create_replica.go` (`replicaVerify` L860), `change_databases.go` (`changeVerify` untuk add), `promote.go` (langkah repoint L1193, `RepointNow` L1233, langkah karantina L1130), `promote_view.go` (L262-280), `internal/panel/web/handlers_oldmaster.go` (`deleteWarnings`, halaman server master lama), `web/templates/server_detail.html`
- Test: `internal/panel/orchestrator/fake_test.go` (L45-55), `create_replica_test.go`, `resync_test.go`, `change_databases_test.go`, `promote_test.go`, `internal/panel/web/web_oldmaster_test.go`

**Interfaces:**
- Consumes: Task 4 `mysql.recordReplicas`, `proto.RecordReplicasResult`; Task 2/3 `QuarantineResult.StartLocked`, `HealthSnapshot.StartLock`.
- Produces:

```go
func (r *Runner) recordReplicas(ctx context.Context, masterServerID int64) // 10 s; error → log Debug (unknown_command dari agent lama juga), tidak pernah menggagalkan job
const reasonStartLock = "Kunci start dari karantina lama masih terpasang di server ini; agent-nya belum bisa melepasnya" // teks perencana
```

- Dipanggil pada master topologi setelah replika terverifikasi tersambung:
  akhir `replicaVerify` (buat replika dan resync, termasuk bangun ulang
  master lama), akhir `changeVerify` job `add_databases`, dan setelah
  `mysql.repoint` sukses (langkah promote, `RepointNow`, `OnOnline`) pada
  master baru.
- Langkah "Karantina master lama": log job
  `jr.logf("warn", ...)` bila `!res.StartLocked`, memuat `StartLockReason`
  (teks perencana: "Master lama dipin dikarantina, tetapi belum terjamin
  terkunci saat MySQL menyala: %s."); `OperatorInitFiles` disebut.
- `PromoteView`: kandidat dengan `Snap.StartLock != nil &&
  Snap.StartLock.Present` → `Eligible=false`, `Reason=reasonStartLock`.
- Halaman server master lama: baris "Kunci start" di Detail teknis
  (Terkunci / belum terjamin: alasan) dan, bila ada, daftar
  `OperatorInitFiles` dengan kalimat (teks perencana) "init_file milik
  operator berikut tidak dijalankan selama karantina: …".
- Dialog hapus server `quarantined` (`deleteWarnings`): tambah kalimat
  (teks perencana) "Bila MySQL server ini sedang mati, karantina dan kunci
  start-nya tetap ada di server itu setelah dihapus dari panel; MySQL-nya
  akan menyala hanya-baca sampai seseorang mengubah perannya di server
  itu."
- Fake agent: `mysql.recordReplicas` ditambahkan ke `agentCommands`
  dengan balasan bawaan `RecordReplicasResult{}` (panggilan tanpa skrip
  tidak menggagalkan test, hanya untuk perintah ini).

- [ ] **Step 1: Tulis test** `TestRecordReplicasCalledAfterVerify` (buat replika, resync, tambah database, repoint → masing-masing tepat satu `mysql.recordReplicas` ke master dengan params kosong), `TestRecordReplicasOldAgentIgnored` (unknown_command → job tetap sukses), `TestQuarantineStepLogsUnguardedStartLock`, `TestPromoteViewRefusesCandidateWithStartLock`, `TestOldMasterPageShowsStartLockAndOperatorInitFiles`, `TestDeleteQuarantinedDialogSaysLockStays`.
- [ ] **Step 2–4.** Gagal → implementasi → `go test ./internal/panel/orchestrator/ ./internal/panel/web/ -race`; `bash scripts/tailwind.sh`.
- [ ] **Step 5: Commit** — `git commit -m "feat(panel): trigger replica recording after verified connections; start lock on promote candidates and the old master page"`

---

### Task 9: Panel: layar pengalihan jujur dan bangun ulang yang menolak tanpa pengakuan (R-029, R-034)

**Files:**
- Modify: `internal/panel/web/handlers_oldmaster.go` (`connectionsView` L73, `connectionsOf` L93-125, `rebuildTarget` L620-670), `web/templates/_connections.html`, `web/templates/replica_new.html` (L3-30, L81-82), `internal/panel/web/handlers_mysql.go` (`createReplica` L631-718), `internal/panel/orchestrator/create_replica.go` (`CreateReplicaParams`, `StartCreateReplica` L70-168), `runner.go` (`Retry` untuk `RebuildOldMaster`, dipakai Task 12)
- Test: `internal/panel/web/web_cutover_test.go`, `web_oldmaster_test.go`, `internal/panel/orchestrator/create_replica_test.go`

**Interfaces:**
- Consumes: Task 6 `proto.ConnectionsResult` (`Typed`, `Unreadable`, `Incomplete`, `LocalAdmin`, `AgentIndistinct`, `Row.Kind`, `Row.LocalAdmin`).
- Produces:

```go
// connectionsView +=
	LocalAdminTotal int
	Uncertain       string // "" hanya bila hasil lengkap; selain itu alasan "tidak bisa dipastikan"
	Indistinct      bool   // catatan "tidak bisa dibedakan dari agent"
func (v connectionsView) CompleteAndEmpty() bool // Unreadable=="" && Uncertain=="" && Total==0
// orchestrator
type CreateReplicaParams struct { /* ... */ AcceptConnections bool }
// jobState += AcceptConnections bool `json:"accept_connections,omitempty"` // dipakai Retry di Task 12
var ErrConnectionsRemain = errors.New("masih ada koneksi ke master lama atau jumlahnya tidak bisa dipastikan; centang pernyataan di form bila Anda paham tulisannya hilang") // teks perencana
func (r *Runner) oldMasterConnectionsClear(ctx context.Context, serverID int64) (clear bool, total int, err error)
	// mysql.connections langsung; clear hanya bila Typed && Unreadable=="" && Incomplete=="" && len(Rows)==0
```

- Agent lama (`Typed` false) → `Uncertain` "agent perlu dimutakhirkan:
  daftar koneksinya belum lengkap" (teks perencana). `Incomplete` →
  `Uncertain = Incomplete`.
- `_connections.html`: hijau "Tidak ada koneksi aplikasi tersisa" hanya
  bila `CompleteAndEmpty`; selain itu jumlah, atau "tidak bisa dipastikan"
  + alasan. Per baris: `Kind=="x"` lencana "X Protocol" (teks perencana),
  `Kind=="lain"` lencana "jenis koneksi tidak dikenal", `AdminAccount`
  lencana "akun admin panel", `LocalAdmin` lencana "sesi akun admin dari
  server ini"; `Indistinct` → catatan "tidak bisa dibedakan dari agent".
- Form bangun ulang: bila tidak `CompleteAndEmpty`, centang wajib
  `accept_connections` dengan label "Saya paham N koneksi masih ada dan
  tulisannya hilang" (N = `Total`); bila hasil tidak bisa dipastikan,
  label (teks perencana) "Saya paham koneksi ke server ini tidak bisa
  dipastikan dan tulisan yang masih masuk hilang". Handler menolak 400
  tanpa centang itu (setelah token dan nama diketik, urutan yang ada).
- `StartCreateReplica` dengan `RebuildOldMaster`: sebelum `startMu`,
  `oldMasterConnectionsClear`; tidak clear dan `!AcceptConnections` →
  `ErrConnectionsRemain`; agent tidak terbaca → sama. `jobState`
  menyimpan `AcceptConnections` (dipakai Retry di Task 12).

- [ ] **Step 1: Tulis test** `TestCutoverNoConnectionsOnlyWhenCompleteAndEmpty` (kosong+Typed → hijau; kosong tanpa Typed → "tidak bisa dipastikan"; `Incomplete` → "tidak bisa dipastikan" + alasan), `TestCutoverBadges` (x, lain, akun admin panel, sesi akun admin dari server ini, catatan indistinct), `TestRebuildRefusedWithoutAcceptConnections` (3 koneksi → form memuat "Saya paham 3 koneksi masih ada dan tulisannya hilang"; POST dengan token+nama tanpa centang → 400; dengan centang → job), `TestStartRebuildRechecksConnections` (orkestrator: fake `mysql.connections` 1 baris → `ErrConnectionsRemain`; `AcceptConnections` → job dibuat; tidak Typed → ditolak).
- [ ] **Step 2–4.** Gagal → implementasi → `go test ./internal/panel/... -race -short`; `bash scripts/tailwind.sh`.
- [ ] **Step 5: Commit** — `git commit -m "feat(web): honest cutover connections; old-master rebuild refused without acknowledging connections"`

---

### Task 10: "Jangan ganggu" yang tidak membungkam bahaya, dengan ringkasan (R-043)

**Files:**
- Create: `internal/panel/store/migrations/0011_snooze_from.sql`
- Modify: `internal/panel/store/servers.go` (`serverColumns` L40, scan L49-61, `SetSnooze` L216-222), `internal/panel/store/alerts.go`, `internal/panel/monitor/alerts.go` (`Snooze` L694), `internal/panel/notify/notifier.go` (`Pass` L278-326, `notify` L330, `notifyRecovered` L407), `notify/message.go`, `internal/panel/web/handlers_controls.go` (`snoozeServer` L255), `web/templates/layout.html` (L128-147), `web/templates/alerts.html` (L28-45), `web/static/app.js`
- Test: `internal/panel/store/migrate_test.go`, `store_test.go`, `notify/notifier_test.go`, `notify/message_test.go`, `internal/panel/web/web_controls_test.go`, `web_alerts_test.go`

**Interfaces:**
- Consumes: Task 7 jenis baru.
- Produces:

```sql
-- 0011_snooze_from.sql
ALTER TABLE servers ADD COLUMN snooze_from TEXT;
```

```go
// store
// Server += SnoozeFrom *time.Time
func (s *Store) SetSnooze(id int64, until *time.Time, now time.Time) error
	// until > now: snooze_until = until; snooze_from = snooze_from bila senyap masih aktif, selain itu now
	// until == nil: snooze_until = now bila senyap aktif (snooze_from tetap, ringkasan tetap dikirim), selain itu tidak berubah
type SnoozeSpan struct{ ServerID int64; From, Until time.Time }
func (s *Store) EndedSnoozes(now time.Time) ([]SnoozeSpan, error) // snooze_from NOT NULL dan snooze_until <= now
func (s *Store) ClearSnoozeFrom(id int64) error
func (s *Store) AlertsOpenedBetween(serverID int64, from, to time.Time) ([]Alert, error) // opened_at di [from, to), urut opened_at
// monitor
func (m *Monitor) Snooze(serverID int64, until *time.Time) error // memanggil SetSnooze(id, until, m.now())
// notify
var unsnoozableKinds = map[string]bool{ monitor.KindMasterDown, KindDiskCrit, KindReplicaUnlocked, KindPromotePartial,
	KindMasterLockExpired, KindMasterQuarantined, KindMasterUnpinned, KindMasterLockKept,
	KindMasterStartUnguarded, KindMasterStartLate, KindMasterStartBypassed, KindMasterStartBlocked: true }
func UnsnoozableKinds() []string // urutan AllKinds; dipakai dialog
func FormatSnoozeDigest(serverName string, from, until time.Time, alerts []store.Alert, baseURL string) string
```

- Selama senyap: jenis `unsnoozableKinds` mengirim pesan pertama (belum
  pernah terkirim) dan pesan pulih seperti biasa; pengulangan diredam
  (tidak ada kirim ulang sebelum senyap berakhir). Jenis lain seperti
  sekarang.
- Ringkasan: setiap `Pass`, `EndedSnoozes(now)` → untuk tiap span kirim
  satu pesan (teks perencana): "Jangan ganggu untuk <server> berakhir
  (<from>–<until>). Selama senyap terbuka N peringatan:" lalu satu baris
  per peringatan (label jenis, waktu buka, "pulih <waktu>" bila tertutup)
  — termasuk yang sudah pulih. N = 0 → "Selama senyap tidak ada
  peringatan baru." Kirim sukses → `ClearSnoozeFrom`; gagal → dicoba
  `Pass` berikutnya. Tidak bergantung `wasSnoozed` (memori).
- Dialog (konfirmasi biasa, tanpa token; komponen Alpine `confirmSnooze`
  dengan `<dialog>` dan tombol submit di dalamnya): judul "Jangan ganggu
  <server>?" dan (teks perencana) "Pemberitahuan Telegram untuk server ini
  ditunda selama waktu yang dipilih, kecuali peringatan berikut yang tetap
  dikirim sekali saat muncul dan saat pulih:" + daftar label
  `UnsnoozableKinds` + "Saat senyap berakhir, panel mengirim ringkasan."
  Membatalkan senyap tidak membuka dialog.

- [ ] **Step 1: Tulis test** `TestMigration0011AddsSnoozeFrom`, `TestSetSnoozeKeepsFromWhenExtended`, `TestCancelSnoozeEndsNowAndKeepsFrom`, `TestCriticalKindsSentOnceDuringSnooze` (master_down terbuka saat senyap → satu kirim; 7 jam kemudian masih senyap → tidak ada ulang), `TestCriticalRecoverySentDuringSnooze`, `TestOtherKindsSilencedAsBefore` (replica_lagging tidak terkirim), `TestSnoozeDigestAfterPanelRestart` (5.10: senyap berakhir saat Notifier lama berhenti; Notifier baru dengan store yang sama → satu ringkasan memuat peringatan yang terbuka dan yang sudah pulih; `snooze_from` NULL setelahnya), `TestSnoozeDigestSentOnce` (dua `Pass` berturut → satu ringkasan; kirim gagal → ringkasan pada `Pass` berikut), `TestSnoozeDialogListsKinds` (halaman memuat dialog dengan semua label `UnsnoozableKinds`; form POST tetap tanpa token).
- [ ] **Step 2–4.** Gagal → implementasi → `go test ./internal/panel/... -race -short`; `bash scripts/tailwind.sh`.
- [ ] **Step 5: Commit** — `git commit -m "feat(notify): snooze never silences critical kinds; digest when it ends (migration 0011)"`

---

### Task 11: Resync: modal berkelompok, pengakuan per database, penegakan di orkestrator (R-035)

**Files:**
- Modify: `internal/panel/web/handlers_controls.go` (`prepareResync` L109-122, `validWipeList` L127-140, `resyncReplica` L142-176), `internal/panel/web/handlers_mysql.go` (`replicaPage`, `renderReplica` L816-870), `web/templates/replica_detail.html` (L175-200), `internal/panel/orchestrator/resync.go` (`ResyncParams`, `StartResync`), `create_replica.go` (`replicaPreflight` L350-466), `runner.go` (`jobState` L922)
- Test: `internal/panel/web/web_controls_test.go`, `internal/panel/orchestrator/resync_test.go`, `create_replica_test.go`

**Interfaces:**
- Produces:

```go
// web
type wipeGroups struct{ OnMaster []string; ReplicaOnly []string } // dari inventaris replika dan master tersimpan (baru disegarkan)
// replicaPage: WipeList []string tetap (semua DB replika); += Wipe wipeGroups; MasterInventoryRead bool
func groupWipe(replica, master []proto.DatabaseInfo) wipeGroups
// orchestrator
type ResyncParams struct { ServerID int64; ForceDump bool; WipeList []string; WipeOnly []string }
// jobState += WipeOnly []string `json:"wipe_only,omitempty"`; ConfirmedAt time.Time `json:"confirmed_at,omitempty"`
func (jr *jobRun) enforceWipeOnly(ctx context.Context) error
	// RefreshInventory(master) langsung (bukan inventaris tersimpan); gagal → error "inventaris master tidak bisa dibaca; pekerjaan ditolak agar tidak ada database yang dibuang tanpa dicek" (teks perencana)
	// setiap nama WipeList yang tidak ada di master dan tidak di WipeOnly → error "database <d> hanya ada di replika dan belum diakui akan dibuang; mulai Resync ulang lagi dari halaman replika" (teks perencana)
	// sukses: jr.st.PeerDatabases = nama di master hasil baca itu
```

- `prepareResync`: segarkan inventaris replika **dan** master; gagal
  salah satu → pesan yang ada (`msgWipeRefresh`), dialog tidak dibuka.
- Modal (`replica_detail.html`): dua kelompok berjudul persis "ada di
  master, disalin ulang" (daftar + hidden `wipe_list[]`) dan "hanya di
  replika, isinya hilang permanen" (setiap nama: hidden `wipe_list[]` dan
  checkbox wajib `wipe_only[]` dengan `required`, label nama database).
  Nama server diketik dan token tetap (tombol `data-confirm-*` yang ada).
- `resyncReplica`: tolak (`msgWipeList`) bila `wipe_only[]` bukan
  himpunan bagian `wipe_list[]` atau ada DB "hanya di replika" (menurut
  inventaris master tersimpan) yang tidak dicentang.
- `StartResync`: `WipeOnly` disalin terurut unik ke `jobState`;
  `ConfirmedAt = now`.
- `replicaPreflight`: bila `len(WipeList) > 0` (resync, atau sisa yang
  disetujui) panggil `enforceWipeOnly` **sebelum** `mysql.preflight`;
  error → langkah gagal.

- [ ] **Step 1: Tulis test** `TestPrepareResyncRefreshesMasterToo`, `TestResyncModalGroups` (master {a,b}, replika {a,laporan} → "ada di master, disalin ulang": a; "hanya di replika, isinya hilang permanen": laporan dengan checkbox `wipe_only[]` required), `TestResyncRequiresEveryReplicaOnlyTicked` (POST tanpa `wipe_only[]=laporan` → 400 `msgWipeList`), `TestPreflightRefusesUnacknowledgedReplicaOnly` (orkestrator: master segar tanpa `laporan`, WipeOnly kosong → langkah gagal, `mysql.preflight` tidak dipanggil, `mysql.dropDatabases` tidak dipanggil), `TestPreflightRefusesWhenMasterInventoryUnreadable` (`mysql.inventory` master gagal → gagal), `TestPreflightUsesFreshMasterInventory` (inventaris tersimpan basi memuat `laporan`, agent master tidak → ditolak).
- [ ] **Step 2–4.** Gagal → implementasi → `go test ./internal/panel/... -race -short`; `bash scripts/tailwind.sh`.
- [ ] **Step 5: Commit** — `git commit -m "feat(resync): grouped wipe list with per-database acknowledgement enforced against a fresh master inventory"`

---

### Task 12: "Ulangi" dan "Setujui" bertoken untuk job pembuang, batas 24 jam, preflight ulang (R-036)

**Files:**
- Modify: `internal/panel/orchestrator/runner.go` (`Approve` L550, `Retry` L569-614, `jobState`), `create_replica.go` (`replicaSnapshot` L626-705), `change_databases.go` (`StartRemoveDatabases`, `removeDrop` L353), `internal/panel/web/handlers_jobs.go` (`jobAction` L359-384, `approvalFor` L156, `renderJob` L227), `handlers_confirm.go` (L22-75, `confirmRefused`), `router.go` (L237-239), `handlers_mysql.go` (`Orchestrator` interface L22-60, `removeReplicaDatabases` L987), `web/templates/job_detail.html` (L41-70), `web/templates/replica_databases.html` (L73-112), `web/static/app.js` (`removeDatabases`)
- Test: `internal/panel/orchestrator/runner_test.go`, `resync_test.go`, `superseded_test.go`, `internal/panel/web/web_jobs_test.go`, `web_confirm_test.go`, `web_mysql_jobs_test.go`

**Interfaces:**
- Consumes: Task 9 `jobState.AcceptConnections`, `oldMasterConnectionsClear`; Task 11 `WipeOnly`, `ConfirmedAt`, `enforceWipeOnly`.
- Produces:

```go
// orchestrator
type JobAck struct {
	Confirmed         bool     // web memakai token job_retry/job_approve segar untuk job ini
	WipeOnly          []string // resync: pengakuan ulang "hanya di replika" (menggantikan st.WipeOnly)
	TypedDatabases    []string // add_databases (Task 13)
	AcceptConnections bool     // bangun ulang master lama
}
func (r *Runner) Approve(jobID string, ack JobAck) error
func (r *Runner) Retry(jobID string, ack JobAck) error
func (r *Runner) DropsData(jobID string) (bool, error)
func dropsData(kind string, st jobState) bool
	// resync_replica; create_replica dengan len(WipeList)>0 || RebuildOldMaster || WaitReason ∈ {WaitWipe, WaitClone};
	// remove_databases dengan DropData. (Task 13 menambah klausa add_databases; jangan merujuk field-nya di sini.)
const confirmMaxAge = 24 * time.Hour
var ErrConfirmStale = errors.New("konfirmasi terakhir pekerjaan ini lebih dari 24 jam lalu; mulai tindakan baru dari halaman servernya") // teks perencana
var ErrJobNeedsConfirm = errors.New("pekerjaan ini membuang data; ulangi atau setujui lewat dialog konfirmasi") // teks perencana
// jobRun += preflightFresh bool // replicaPreflight sukses dalam run ini
// web
const ActJobRetry = "job_retry"; const ActJobApprove = "job_approve"
func jobConfirmAction(act, jobID string) string // act + ":" + jobID; confirmAction menerimanya bila jobID cocok jobIDPattern
// confirmKinds += auditJob; target_id = job.ServerID
```

- Aturan `Retry`/`Approve` untuk job `dropsData`: tanpa `ack.Confirmed` →
  `ErrJobNeedsConfirm`; `Retry` dengan `now − ConfirmedAt` (atau
  `job.CreatedAt` bila nol) > 24 jam → `ErrConfirmStale`; sukses →
  `st.ConfirmedAt = now` (disimpan sebelum `launch`). `Retry` resync:
  `ack.WipeOnly` ⊆ `WipeList` → menggantikan `st.WipeOnly`. `Retry`
  bangun ulang master lama: `oldMasterConnectionsClear`; tidak clear dan
  `!ack.AcceptConnections` → `ErrConnectionsRemain`. Job promote, perketat
  TLS dan sertifikat tidak berubah (tanpa token).
- `replicaSnapshot`: sebelum `mysql.dropDatabases` atau `mysql.cloneFrom`
  pada job `dropsData` (dan bukan `reattach`/`AdminReplaced`), bila
  `!jr.preflightFresh` jalankan `jr.replicaPreflight(ctx, approved)`
  dulu; hasilnya bukan done → kembalikan hasil itu. Jadi "Ulangi" dari
  langkah mana pun menjalankan preflight (dan `enforceWipeOnly`) sebelum
  membuang.
- Web: `retryJob`/`approveJob` → `DropsData`; true →
  `requireConfirm(jobConfirmAction(...), auditJob, job.ServerID)` lalu
  `JobAck{Confirmed:true, WipeOnly: r.PostForm["wipe_only[]"],
  AcceptConnections: …}`. Rute baru `POST /jobs/{id}/retry/prepare`
  (segarkan inventaris replika dan master untuk resync, lalu redirect
  `/jobs/{id}?buka=ulangi`). Halaman job, job pembuang: tombol "Ulangi
  dari tahap ini" membuka dialog (`openDialog`) berisi pengelompokan
  terkini (komponen modal resync yang sama: "ada di master, disalin
  ulang" / "hanya di replika, isinya hilang permanen" + `wipe_only[]`
  required) dan tombol konfirmasi `data-confirm-action="job_retry:<id>"
  data-confirm-kind="job" data-confirm-id="<serverID>" data-confirm-danger="1"`;
  bangun ulang: centang `accept_connections` seperti Task 9 bila perlu.
  Tombol "Setujui" job pembuang: tombol di dialog `confirmApproval`
  memakai `data-confirm-action="job_approve:<id>"`. `confirmRefused`
  untuk `auditJob` merender halaman job dengan 403.
- Lepas database dengan hapus data: tombol "Ya, lepas" membawa
  `data-confirm-name` = nama replika hanya bila "Hapus datanya juga"
  dicentang (getter Alpine `confirmName` di `removeDatabases`; nama dari
  `data-server-name` form); handler: `drop_data` && `!typedName` → 400
  `msgNameMismatch`. Job `remove_databases` dengan hapus data:
  `ConfirmedAt = now` saat dibuat.

- [ ] **Step 1: Tulis test** `TestRetryResyncFailedAtCopyNeedsToken` (5.10: resync gagal di "Salin data"; POST retry tanpa token → 403, job tetap gagal; dengan token → fake agent menerima `mysql.inventory` master lalu `mysql.preflight` lagi sebelum `mysql.dropDatabases`), `TestRetryRefusedAfter24h` (ConfirmedAt 25 jam lalu → `ErrConfirmStale`; job lama tanpa ConfirmedAt memakai CreatedAt), `TestApproveLeftoverNeedsToken`, `TestApproveRefreshesConfirmedAt`, `TestPromoteRetryStillTokenless`, `TestTightenTLSRetryTokenless`, `TestRetryDialogRegroupsAndAsksWipeOnly` (`/retry/prepare` menyegarkan dua inventaris; dialog memuat `wipe_only[]` required untuk DB hanya-di-replika), `TestRetryRebuildRechecksConnections`, `TestJobTokenBoundToJob` (Review Focus 5: token `job_retry:A` dipakai untuk job B → 403; dipakai dua kali → 403 kedua; token server lain → 403), `TestRemoveDatabasesDropNeedsTypedName` (drop tanpa nama → 400; tanpa drop tidak butuh nama).
- [ ] **Step 2–4.** Gagal → implementasi (perbarui fake `Orchestrator` di test web dan semua pemanggil `Approve`/`Retry`) → `go test ./internal/panel/... -race -short`; `bash scripts/tailwind.sh`.
- [ ] **Step 5: Commit** — `git commit -m "feat(jobs): fresh token for retry and approve of data-dropping jobs, 24 h limit, preflight before every drop"`

---

### Task 13: Tambah database yang sudah ada di replika (R-058) dan agent menolak `DropExisting`

**Files:**
- Modify: `internal/panel/orchestrator/change_databases.go` (`addCatchUp` L252, `addDump` L278-314), `runner.go` (`jobState`, konstanta wait L1075-1079, `Approve`), `internal/panel/web/handlers_jobs.go` (`approvalFor`), `web/templates/job_detail.html`, `web/templates/replica_databases.html` (bagian Tambah dan Lepas), `internal/agent/drivers/mysql/dump.go` (L99, L132; handler `mysql.dumpFrom`), `internal/proto/mysql.go` (komentar `DropExisting` L226)
- Test: `internal/panel/orchestrator/change_databases_test.go`, `internal/panel/web/web_mysql_jobs_test.go`, `web_mysql_test.go`, `internal/agent/drivers/mysql/snapshot_test.go`, `internal/panel/e2e_replica_test.go`

**Interfaces:**
- Consumes: Task 12 `JobAck.TypedDatabases`, `dropsData`, token `job_approve`.
- Produces:

```go
const WaitAddExisting = "add_existing" // add_databases: database yang ditambahkan sudah ada di replika
// jobState += AddExisting []string `json:"add_existing,omitempty"`; AddExistingHash string `json:"add_existing_hash,omitempty"`; AddDrop []string `json:"add_drop,omitempty"`
func (jr *jobRun) addExisting(ctx context.Context) ([]string, error) // RefreshInventory(replika) segar; irisan dengan st.Change, perbandingan tanpa beda huruf besar-kecil; terurut
func addExistingText(names []string) string // satu paragraf per nama: teks 5.6 persis, X = nama
// jobRun += addFresh bool
// dropsData (Task 12) += add_databases dengan len(AddDrop) > 0 || WaitReason == WaitAddExisting
```

- `addCatchUp` (langkah 0) **dimulai** dengan `addExisting`: kosong →
  lanjut seperti sekarang. Tidak kosong: `h := hashList(list)`; bila
  `!approved || h != st.AddExistingHash` → simpan `AddExisting`, hash,
  `WaitReason = WaitAddExisting`, kembalikan `wait(addExistingText(list))`.
  Disetujui → `AddDrop = AddExisting`, `addFresh = true`, lanjut.
- `Approve` untuk `WaitAddExisting`: `ack.Confirmed` dan himpunan
  `ack.TypedDatabases` (dipangkas spasi) sama persis dengan `AddExisting`;
  selain itu error (teks perencana) "Nama database yang diketik tidak
  cocok dengan daftar; ketik nama setiap database persis".
- `addDump`: bila `len(AddDrop) > 0 && !reattach`: bila `!addFresh`
  hitung ulang `addExisting`; hash berbeda → kembali menunggu (wait yang
  sama); lalu `mysql.dropDatabases{Databases: AddDrop}` (agent:
  `sql_log_bin=0`, replikasi sudah berhenti di langkah 1), lalu dump
  (tetap tanpa `DropExisting`; riwayat GTID replika tidak disentuh).
- Halaman job: `approvalFor(WaitAddExisting)` → `approval{Label: "Ya,
  buang dan salin dari master", Danger: true}` (teks perencana); dialog
  memuat satu input teks wajib `confirm_db[]` per nama (label "Ketik
  nama database <nama>") dan tombol `job_approve:<id>`.
- Halaman database replika: di bagian Tambah dan di bawah opsi Lepas tanpa
  hapus data, catatan persis "bila ditambahkan lagi, isi di replika
  dibuang lebih dulu".
- Agent `mysql.dumpFrom`: `p.DropExisting` true → `ParamError` (teks
  perencana) "drop_existing tidak didukung; panel memakai
  mysql.dropDatabases"; kode drop di `dumpFrom` dihapus.
- Kompatibilitas (5.8): tidak ada panel yang mengirim `drop_existing`
  (grep); panel lama tidak terpengaruh.

- [ ] **Step 1: Tulis test** `TestAddExistingWaitsBoundToHash` (replika segar memuat `rekap` → wait dengan teks 5.6 persis untuk `rekap`; daftar berubah → wait lagi), `TestApproveAddExistingNeedsTypedNames` (5.10: tanpa token → `ErrJobNeedsConfirm`; nama salah → ditolak; benar → lanjut), `TestAddExistingDropsBeforeDump` (urutan panggilan fake: `mysql.stopReplica`, `mysql.dropDatabases{rekap}`, `mysql.dumpFrom` tanpa `drop_existing`), `TestAddExistingRetryRechecks` (retry dari langkah dump dengan replika yang kini juga memuat `rekap2` → kembali menunggu), `TestDumpFromRefusesDropExisting` (agent), `TestDatabasesPageNotes`, `TestE2EAddDatabaseExistingOnReplica` (5.10, mysqld sungguhan di harness e2e: replika sebagian mengikuti `a`; buat `rekap` di master; di replika (read-only dilepas sementara lewat root) buat `rekap.t_lama`, `rekap.v_lama`, procedure `rekap.p_lama`; Tambah `rekap` → job menunggu; Setujui dengan token tanpa nama → ditolak; dengan nama → job sukses; di replika `t_lama`, `v_lama`, `p_lama` tidak ada, tabel master ada; `gtid_executed` replika masih memuat UUID replika itu bila sebelumnya memuatnya).
- [ ] **Step 2–4.** Gagal → implementasi → `go test ./internal/panel/... ./internal/agent/drivers/mysql/ -race -short` dan `go test -timeout 30m -run TestE2EAddDatabaseExistingOnReplica ./internal/panel/`; `bash scripts/tailwind.sh`.
- [ ] **Step 5: Commit** — `git commit -m "feat(databases): adding a database that exists on the replica waits for typed names, then drops and copies; agent refuses drop_existing"`

---

### Task 14: E2E, README, graph

**Files:**
- Modify: `internal/panel/e2e_replica_test.go` (`TestE2EPromoteEmergency` L1119-1135, `TestE2EPromotePlanned`), `README.md` (bagian "Promote", "Keamanan", "Pemantauan", CLI agent)

- [ ] **Step 1: Ubah `TestE2EPromoteEmergency`** (keputusan perencana 14): setelah `m.agent.stop` dan `restartMySQL(m.inst)`, harapkan `super_read_only` **ON** (kunci start bekerja tanpa agent); lalu tiru orang yang membuka kunci: `SET GLOBAL super_read_only = OFF` (root), `INSERT` baris rahasia, `m.agent.start`; harapkan karantina dan peringatan `master_start_late` terbuka; sisa alur (Periksa data tertinggal = 1 transaksi, Amankan, unduh, hapus, bangun ulang) tidak berubah. Setelah bangun ulang: managed config tanpa `init_file`, berkas kunci tidak ada (dilepas penjaga).
- [ ] **Step 2: `TestE2EPromotePlanned`**: setelah karantina, halaman master lama menampilkan "Kunci start" terkunci; layar pengalihan hijau hanya bila daftar lengkap dan kosong; bangun ulang memerlukan `accept_connections` bila koneksi uji `app_e2e` masih terbuka.
- [ ] **Step 3:** `make test` lulus; `make test-e2e` lulus (catat durasi).
- [ ] **Step 4: README**: kunci start (berkas, letak, akibat menghapusnya, AppArmor/SELinux), loop penjaga 5 detik dan batas 300 detik, `forget-replica`, kunci promote yang dipertahankan, daftar koneksi dan `program_name`, "Jangan ganggu" dan ringkasan, Ulangi/Setujui bertoken dan batas 24 jam, Tambah database yang sudah ada.
- [ ] **Step 5:** `graphify update .`; commit — `git commit -m "test(e2e): emergency promote with the start lock; docs for Plan 5a"`

---

### Task 15: Inventaris risiko

**Files:**
- Modify: `docs/superpowers/security/2026-11-10-inventaris-risiko.md`

- [ ] **Step 1:** Pindahkan R-029, R-030, R-031, R-032, R-033, R-034, R-035, R-036, R-043, R-058 dari tabel kelas C ke "Sudah ditangani": satu baris per kelompok, kolom "Ditangani di" memuat "Rencana 5a Task N" dan hash commit dari `git log --oneline` (R-030: Task 1; R-031: Task 2–3, 14; R-032: Task 4, 8; R-033: Task 5, 7; R-029/R-034: Task 6, 9; R-035: Task 11; R-036: Task 12; R-043: Task 10; R-058: Task 13). Tambah baris untuk jalur tambahan spek 5.9 (nilai OFF tersimpan, `dbpanel_admin` tersembunyi, daftar kosong saat P_S mati, Ulangi/Setujui tanpa token, senyap membungkam `replica_unlocked`/`promote_partial`, deteksi master tumbang sampai satu jam, anggaran pembacaan replika, `DropExisting`).
- [ ] **Step 2:** Catat risiko sisa spek bagian 7 sebagai baris baru `R-094`… dengan status `terbuka`: master mati < 5 detik setelah replika pertama tersambung di luar panel (kelas A, Sedang); atribut koneksi bisa dipalsukan (kelas B, Rendah); `init_file` hanya diuji 8.0.46 dan tanpa AppArmor/SELinux, profil yang menolak berkas menunda "Periksa data tertinggal" (kelas C, Sedang; sebut bukti dan yang belum diverifikasi dari bagian "Bukti profil keamanan mysqld" rencana ini); `--init-file` di baris perintah layanan mengalahkan baris agent (kelas A, Sedang); master tertahan baca-saja selama replika terdaftar tidak terbaca, termasuk oleh panel yang dibobol (kelas B, Sedang); jaringan terbelah dan panel dibobol tetap kelas A/B (rujuk R-001, kelas B).
- [ ] **Step 3:** Hitung ulang tabel "Ringkasan" dari isi tabel (perkiraan: kelas C Tinggi 10→4, Sedang 21→17; jumlah C 65→55 sebelum baris baru) dan daftar "Lima butir terbuka dengan prioritas tertinggi"; perbarui "Keadaan kode yang dicek".
- [ ] **Step 4: Commit** — `git commit -m "docs(security): Plan 5a items handled; residual risks recorded"`

---

## Keputusan perencana

1. **Nama dan letak berkas kunci start.** `dbpanel-start-lock.sql` di
   `IncludeDir`, selain itu di direktori `ConfigFile` setelah symlink;
   ditolak bila di dalam `DataDir`. Alasan: bukti profil (Ubuntu dan
   Oracle `/etc/mysql/** r`; SELinux `mysqld_etc_t` untuk `/etc/mysql`
   dan `/etc/my.cnf.d`), dan eksperimen bahwa `.sql` di includedir tidak
   dibaca sebagai berkas opsi. Biaya bila salah: di RPM tanpa
   `!includedir` berkas jatuh di `/etc` (tidak terbukti terbaca) → MySQL
   master lama tidak menyala; panel memperingatkan (Kritis).
2. **MAC yang tidak terbukti = tidak terjamin, baris tetap ditulis.**
   Aman bagi data (gagal menyala, bukan bisa ditulis). Biaya: "Periksa
   data tertinggal" tertunda sampai operator memperbaiki.
3. **Argumen `--print-defaults`.** Satu `ConfigFiles` (testmysql) →
   `--defaults-file`; selain itu daftar bawaan seperti unit systemd
   Ubuntu. Opsi baris perintah layanan tidak terlihat (spek 7,
   `bypassed`). Biaya: layanan yang memakai `--defaults-file` lain
   membuat `StartLocked` bisa salah benar; penjaga melaporkan `bypassed`
   saat menyala.
4. **`renderConfig` mempertahankan baris selama baris itu ada**, bukan
   hanya selama peran `quarantined`. Alasan: penulisan blok sebelum
   pelepasan tidak boleh membuang kunci. Biaya: replika yang menunggu
   pelepasan menyala dengan `init_file` (tetap hanya-baca, sesuai peran).
5. **Aturan lepas.** `master` → segera (root/CLI sudah memutuskan);
   `replica`/`unassigned` → MySQL hidup dan (bukti run ini atau
   `persisted_variables` ON/ON); penjaga tidak pernah menjalankan `SET`
   di cabang ini agar tidak mengganggu muatan dump yang sedang berjalan.
   Biaya: dengan `performance_schema` mati dan agent yang di-restart,
   kunci bertahan sampai promote melepasnya.
6. **`mysql.promote` boleh `SET PERSIST` ON lalu melepas** pada kandidat
   (replika) yang masih membawa baris. Biaya: tidak ada; kandidat memang
   hanya-baca sampai titik tanpa balik.
7. **Karantina saat mati: pin dulu, baru kunci start.** Pin yang tertulis
   menjamin penjaga menulis kunci pada putaran berikut. Biaya: jendela
   crash di antara keduanya ≤ 5 detik setelah agent hidup lagi.
8. **`bypassed` hanya bila berkas kunci lebih tua dari mysqld yang
   berjalan.** Tanpa ini, setiap server yang dikarantina saat MySQL
   hidup akan memicu Kritis palsu. Biaya: opsi baris perintah yang
   menimpa baris yang ditulis setelah start baru terlihat pada start
   berikutnya.
9. **Port replika di berkas terpisah `replica-ports-seen`**, berkas host
   lama tidak diubah. Alasan: `SHOW REPLICAS` tanpa `report_host` tidak
   bisa dihubungkan ke host; berkas lama tetap terbaca agent lama. Biaya:
   bacaan host × port bertambah (dibatasi paralel 16).
10. **Agent menerapkan `check_every_sec` > 300 sebagai 300, tidak
    menolak.** Panel lama tetap bisa mengonfigurasi agent baru. Batas
    ringkasan dan ambang lag tetap 3600 karena spek hanya menurunkan
    interval pemeriksaan. Biaya: tidak ada.
11. **`performance_schema` mati: jenis baris "lain", hasil tetap
    lengkap.** `information_schema.PROCESSLIST` memuat semua sesi tetapi
    tidak membedakan SQL dan X. Biaya: semua baris berlencana "jenis
    koneksi tidak dikenal" di server itu.
12. **Proses anak `mysql`/`mysqldump`/`mysqlbinlog` tidak bertanda.**
    Klien CLI 8.0 tidak punya opsi atribut koneksi. Arah aman: sesi muat
    agent terhitung sebagai "sesi akun admin dari server ini".
13. **Ulangi bangun ulang master lama memeriksa koneksi lagi.** Spek
    hanya menyebut saat job dimulai; diperluas karena Ulangi bisa terjadi
    berjam-jam kemudian. Biaya: satu centang lagi bila koneksi masih ada.
14. **E2E darurat diubah.** `TestE2EPromoteEmergency` menegaskan master
    lama menyala bisa ditulis tanpa agent — persis jalur yang 5.2 tutup;
    spek meminta e2e itu "tetap lulus". Diubah: harapkan ON, tiru
    pembukaan manual untuk data tertinggal, dan uji `late`. Biaya bila
    salah: e2e tidak lagi meniru "agent belum sempat mengunci" secara
    harfiah (jalur itu memang tidak ada lagi).
15. **Token job terikat lewat nama aksi** `job_retry:<id>`, target kind
    `job`, target id = server job. Konfirmasi yang ada menyimpan id target
    sebagai bilangan, id job heksadesimal. Biaya: tidak ada.
16. **Semua "Setujui" pada job pembuang butuh token**, termasuk "Sudah
    saya hubungkan ulang". Spek mengikat token pada jenis job. Biaya:
    satu kata sandi lagi pada langkah yang tidak membuang.
17. **Batas 24 jam hanya untuk Ulangi**; Setujui bertoken memperbarui
    `ConfirmedAt`. Job lama tanpa `ConfirmedAt` memakai `CreatedAt`.
18. **Lepas database dengan hapus data tidak punya "preflight ulang".**
    Daftarnya adalah database yang diikuti; token, nama replika dan 24 jam
    yang menjaganya.
19. **Perbandingan nama database tanpa beda huruf besar-kecil** di Tambah
    database. Arah aman: lebih sering bertanya.
20. **Lima jenis peringatan baru**, `master_lock_kept` terpisah dari
    `master_lock_expired` agar Kritis dan bisa dikecualikan dari senyap.
21. **Saklar jenis Telegram global tidak diubah**; jenis baru aktif
    secara bawaan. Spek 5.7 hanya mengatur "Jangan ganggu". Biaya: admin
    tetap bisa mematikan jenis kritis di Pengaturan (dicatat sebagai sisa
    untuk 5e).
22. **Membatalkan senyap = mengakhirinya sekarang**, ringkasan tetap
    dikirim.
23. **Teks yang tidak ada di spek** (dialog hapus, label pengakuan saat
    jumlah tidak diketahui, pesan error baru) ditulis perencana dan
    ditandai di task-nya.
24. **`managedConfigMu`** menyerialkan semua penulis konfigurasi
    terkelola dalam satu agent. CLI `set-role master` adalah proses lain;
    balapan dengan penjaga paling buruk menulis ulang baris yang lalu
    dilepas lagi (peran master).
25. **Pemantauan log error memakai cap waktu baris > mtime berkas
    kunci.** Baris lama tidak memicu peringatan. Tanpa `log_error` di
    konfigurasi (log ke journal) tidak ada pemantauan; hanya peringatan
    "belum terjamin"/blocked dari jalur lain.
26. **`monitor.quarantineStart` dikirim pada setiap transisi**, termasuk
    agent mulai dengan MySQL hidup; `ok` menutup `bypassed`/`blocked`.

## Catatan untuk pelaksana

- Urutan: 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8 → 9 → 10 → 11 → 12 → 13 → 14 →
  15. Task 4–6 hanya bergantung pada Task 1; Task 10 bebas setelah 7;
  Task 12 setelah 9 dan 11; Task 13 setelah 12.
- Test mysqld memakan menit; pakai `-run` dan `-timeout 30m` saat
  iterasi, lalu paket penuh sebelum commit.
- Setiap task diikuti review independen; setelah Task 15, review akhir
  seluruh cabang dan audit keamanan seperti rencana sebelumnya (spek 5.10).
- Task 2 dan 3 menyentuh `writeValidConfig`, yang juga dipakai
  `configure()`; jalankan `go test ./internal/agent/drivers/mysql/ -run
  'Configure|Restart|Rebuild' -timeout 30m` sebelum commit.
