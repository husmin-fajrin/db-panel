# Rencana Implementasi 5b-2b: Gerbang Prasyarat, Cadangan, dan Panduan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Tindakan terencana ditolak selagi sebuah prasyarat merah, dengan
cara memperbaikinya di layar yang sama; promote darurat tidak pernah
tertahan prasyarat tetapi mewajibkan satu centang yang diaudit; panel
melihat umur cadangan master; staf IT menemukan langkah darurat di halaman
Panduan; "Perketat" berganti nama; butir kecil bawaan tinjauan akhir 5b-2a
ditutup; dan seluruh mode ketat (5b-1, 5b-2a, 5b-2b) diaudit keamanannya.

**Architecture:** Satu fungsi `Runner.Gate` di orchestrator menilai daftar
baris (hijau, kuning, merah) dari data yang sudah ada di panel: baris
server, inventaris MySQL, sertifikat, dan laporan kesehatan terakhir. Ia
tidak memanggil agent. Fungsi yang sama dipakai layar (daftar dan tombol
yang dimatikan), handler (sebelum token konfirmasi dipakai) dan `Start*`
(keputusan, di bawah `startMu`). Agent menambah tiga hal ke laporan
kesehatan: jamnya, status `performance_schema`, dan keadaan folder
cadangan yang dipindai monitor agent paling sering lima menit sekali
tanpa membuka berkas. Halaman Panduan adalah template; README memuat
bagian yang sama dan sebuah test menjaga keduanya tidak berpisah.

**Tech Stack:** Go 1.26, `CGO_ENABLED=0`, tanpa dependensi baru; SQLite
(modernc); html/template + Tailwind v4/daisyUI v5 (`bash
scripts/tailwind.sh`); Alpine build CSP (tanpa JavaScript baru bila bisa);
harness `internal/agent/drivers/mysql/testmysql`; e2e
`internal/panel/e2e_*_test.go`.

**Spec:** `docs/superpowers/specs/2026-11-12-rencana-5b-mode-ketat-gerbang-prasyarat-design.md`
bagian 5.7 sampai 5.13, 6, 7 dan 9 (5.1–5.6 sudah dibangun Rencana 5b-1;
"Catatan pelaksanaan Rencana 5b-1" di akhir spek mengikat). Butir bawaan:
`docs/superpowers/ledgers/2026-11-14-05b2a-final-review-agent.md`,
`…-final-review-panel.md`, `…-deferred-items.md`, dan catatan 21–22 di
`docs/superpowers/plans/2026-11-14-05b2a-master-lama-satu-aturan.md`.
Kode diperiksa pada `main` `78b1bb9`; nomor baris di rencana ini berasal
dari pembacaan pada commit itu, dan pelaksana memeriksa ulang letaknya
sebelum mengubah.

## Global Constraints

- Go 1.26, `CGO_ENABLED=0`, tanpa dependensi baru.
- Bahasa: teks untuk operator (UI, keluaran CLI, pesan galat yang tampil
  ke operator, README, Panduan) berbahasa Indonesia; kode, komentar, nama
  test dan pesan commit berbahasa Inggris; log agent (slog) berbahasa
  Inggris.
- Aturan kata (spek 5.11): pakai nama yang sudah dipakai panel
  ("baca-saja", "karantina", "kunci start", "Bangun ulang sebagai
  replika", "Periksa data tertinggal"); pada penyebutan pertama di satu
  layar atau panduan, istilah Inggrisnya di dalam kurung, misalnya
  "cadangan (backup)", "jam (clock)". Hal baru memakai istilah IT Inggris
  yang lazim. Teks lama tidak diubah kecuali yang disebut task.
- Teks ber-id di rencana ini (F…, B…, G…, D…) dipakai persis, huruf
  demi huruf. `%s`, `%d` adalah tempat nilai.
- **Promote darurat tidak pernah ditolak oleh sebuah baris prasyarat.**
  Periksa/Amankan data tertinggal, "Wajibkan pemeriksaan sertifikat TLS",
  pemasangan sertifikat, hentikan/jalankan/kunci ulang replika, arahkan
  ulang, lepas replika, buka kunci master, batalkan pekerjaan, hapus
  server, dan `RenewMySQLTLS` tidak digerbang.
- Tidak diketahui berarti kuning dengan penjelasan, kecuali "Versi agent"
  dan "Pin master" yang merah bila tidak diketahui.
- `Gate` tidak memanggil agent dan tidak menulis apa pun. Tidak ada
  tombol untuk melewati gerbang.
- Field protokol baru semuanya `omitempty`; `proto.HealthSnapshot` harus
  tetap bisa dibandingkan dengan `==` (skalar, string, atau pointer; tidak
  ada slice atau map langsung di struct itu).
- Agent tidak pernah membuka berkas di folder cadangan, tidak mengirim
  nama berkas, dan tidak mengikuti symlink di dalamnya. Dari agent ke
  panel hanya kode tertutup dan angka; tidak ada teks bebas baru.
- `monitor.configure` tidak boleh gagal karena folder cadangan yang
  buruk: pengaturan pemantauan lain tetap diterapkan, dan folder itu
  dilaporkan tidak terbaca.
- Nilai tetap:
  - selisih jam merah di atas **30 detik**;
  - umur cadangan paling lama bawaan **48 jam**, boleh 1 sampai 8760;
  - pemindaian folder: paling dalam **3 tingkat** (berkas di folder itu,
    di subfolder, dan di sub-subfolder), paling banyak **10.000 entri**,
    paling lama **10 detik**, paling sering sekali per **5 menit**, satu
    pemindaian pada satu waktu;
  - folder ditolak: bukan path absolut atau tidak bersih
    (`filepath.Clean(p) != p`), lebih dari 1024 byte, memuat byte kendali,
    sama dengan `/`, atau sama dengan/di bawah `/home`, `/root`,
    `/run/user`, `/tmp`, `/var/tmp`, `/proc`, `/sys`, `/dev`;
  - pemeriksaan port master dari host panel: batas **1 detik**, hasilnya
    disimpan **10 menit** per master;
  - teks agent yang ditampilkan dibatasi `maxAgentWords` (300) seperti di
    tempat lain.
- Satu penulis dan satu proses test pada satu waktu. Gerbang tiap task,
  satu per satu, semua exit 0: task yang menyentuh `internal/agent` atau
  `internal/proto`:
  `go test ./internal/agent/... ./internal/proto/... -race -short -timeout 60m`,
  lalu `go test ./internal/panel/... -race -short -timeout 60m`, lalu
  `make test`; task panel saja: dua yang terakhir. Task 10, 12 dan 14
  menambah `make test-e2e`. Tidak pernah `pkill`/`killall` mysqld.
- Eksplorasi kode: `graphify query "<pertanyaan>"` sebelum grep atau
  membaca berkas sumber; `graphify update .` sesudah mengubah kode.
- Setiap perintah yang ditulis ke README atau Panduan dijalankan dulu
  oleh penulisnya (mysqld coba di direktori sementara), dan setiap teks
  yang dikutip disalin dari kode. Yang tidak bisa dijalankan di mesin
  pelaksana (`systemctl`, `timedatectl`, host kedua) disebut satu per
  satu di laporan task sebagai "tidak dijalankan".
- Commit kecil per langkah; tidak ada amend dan tidak ada force-push.

## Review Focus

1. **Laporan kesehatan yang basi atau belum ada** (agent baru tersambung,
   panel baru dinyalakan, laporan tertunda/backlog): baris tidak boleh
   merah karena data lama. Yang diharapkan: kuning "belum diketahui",
   kecuali versi agent dan pin master. Test: Task 9
   (`TestGateUnknownIsYellow`, `TestGateClockIgnoresBacklogReport`).
2. **Folder cadangan yang tidak biasa** (symlink ke luar, berkas khusus,
   jutaan entri, mount yang macet, nama dengan baris baru, folder di
   `/tmp`): agent tidak boleh macet, mengikuti symlink, atau menggagalkan
   `monitor.configure`. Test: Task 6 (`TestBackupScanSkipsSymlinksAndSpecials`,
   `TestBackupScanLimits`, `TestBackupScanOneAtATime`,
   `TestConfigureKeepsSettingsWithBadFolder`).
3. **Halaman yang sudah terbuka lama**: operator menekan tombol di
   halaman yang dimuat sebelum baris menjadi merah. Yang diharapkan:
   ditolak sebelum kata sandi dipakai, token tidak hangus. Test: Task 10
   (`TestGateRefusesBeforeTokenIsConsumed`).
4. **Promote darurat dengan semua baris merah**: tetap mulai; auditnya
   memuat kunci setiap baris merah. Test: Task 12
   (`TestEmergencyPromoteStartsWithEveryRowRed`).
5. **Akun MySQL dengan `@` di nama pengguna, atau akun yang sudah
   diganti tanpa sepengetahuan panel** (`reset-admin`): nama dipisah di
   `@` terakhir; baris yang salah merah harus bisa dihijaukan dari layar
   itu (tombol penyegar inventaris). Test: Task 9
   (`TestGateAccountSplitsAtLastAt`), Task 7
   (`TestInventoryRefreshUpdatesAccount`).

---

## Keputusan penulis yang perlu dilihat pemilik

Spek 5.7–5.10 menyerahkan beberapa hal kepada pelaksana, dan pembacaan
kode menemukan sembilan tempat spek tidak cocok dengan kode. Yang berikut
diputuskan di rencana ini.

- **K1. Promote darurat ke kandidat yang agent-nya beda versi tetap
  ditolak** (aturan kelayakan kandidat sejak Rencana 4b,
  `promote_view.go:280-284`), dan tidak diubah. Spek 5.12 berkata "promote
  darurat tetap jalan" dengan agent lama. Alasan: agent kandidat yang
  tidak mengenal sebuah perintah gagal di tengah promote, pada saat
  paling genting. Ruginya: bila satu-satunya replika ber-agent lama,
  agent itu harus diperbarui dulu (beberapa menit). Pilihan lain:
  kandidat seperti itu boleh dipilih pada promote darurat dengan centang
  tambahan.
- **K2. Promote darurat tetap ditolak selagi topologi punya pekerjaan
  yang belum selesai**; layar sudah menunjuk pekerjaannya (5b-2a). Tidak
  ada pembatalan otomatis.
- **K3. Daftar prasyarat ditampilkan di halaman, bukan di dalam modal
  kata sandi.** Selagi ada baris merah, tombol tindakannya mati dan
  daftarnya tampil di sampingnya, sehingga modal tidak pernah terbuka.
  Ini memenuhi "modal tidak meminta sandi selama ada yang merah" tanpa
  JavaScript baru. "Siapkan master" dan "Tambah database", yang tidak
  punya modal, mendapat daftar yang sama.
- **K4. `Retry`/`Approve` pekerjaan promote tidak pernah digerbang**
  (promote tidak membuang data). Yang dinilai ulang hanya pekerjaan yang
  `dropsData`-nya benar.
- **K5. Folder cadangan di `/tmp` dan `/var/tmp` juga ditolak**, karena
  layanan agent berjalan dengan `PrivateTmp=yes` dan melihat folder
  kosong di sana. Spek hanya menyebut `/home`, `/root`, `/run/user`.
  `/proc`, `/sys`, `/dev` dan `/` ditolak supaya agent tidak menyentuh
  berkas khusus.
- **K6. Penerusan log audit hanya ditandai dengan tangan.** Spek berkata
  kuning sampai `systemd-journal-upload` aktif *atau* ditandai. Pendeteksi
  otomatis tidak dibangun: mesin pelaksana tidak punya systemd, jadi ia
  tidak bisa diukur, dan tanda hijau yang salah lebih buruk daripada satu
  klik. Barisnya tidak pernah menolak.
- **K7. Baris "Akun panel" membaca akun dari inventaris MySQL panel**
  (`CURRENT_USER()` saat Hubungkan/Segarkan). Sesudah `reset-admin` di
  server, panel belum tahu sampai inventaris disegarkan; arahnya aman
  (merah yang salah, bukan hijau yang salah), dan cara menghijaukannya
  ada di baris itu.
- **K8. Judul kotak master lama yang karantinanya tertunda** menjadi
  teks F1 (Task 2). **Teks `reset-admin` saat login berhasil tetapi
  simpan gagal** menjadi teks F4 (Task 1).
- **K9. Yang tidak dikerjakan dan diserahkan ke audit atau Rencana 5e:**
  `cancelAgentCopy` di luar `startMu` (terburuk: salinan basi dibatalkan,
  Ulangi menyalin lagi); nama database bertanda kutip di baris yang
  disensor (dialog persetujuan tetap menampilkan semua nama); lingkungan
  `MYSQL_*`/`LIBMYSQL_*` program klien agent; `tail` atas FIFO yang
  dinamai `log_error`; `putBack`/`finalCheck` tanpa `defer`; `grant-admin`
  yang tidak menghitung ulang.
- **K10. Baris lama di basis data tetap memakai nama "Perketat"**
  (pesan peringatan `job_failed` dan baris log pekerjaan yang sudah
  tersimpan). Label pekerjaan dan label audit dihitung saat halaman
  digambar, jadi langsung berganti.
- **K11. Layar tindakan menautkan ke panduannya, tidak menyalin
  langkahnya.** Spek 5.10 meminta isi yang sama di layar, di halaman
  Panduan dan di README. Yang ditaruh di layar adalah tautan ke bagian
  Panduan yang tepat (dan satu kalimat di layar pengalihan), supaya
  langkah yang sudah diaudit tidak punya tiga salinan yang bisa berpisah.

## Struktur berkas

Baru:

- `internal/agent/monitor/backup.go` — pemindai folder cadangan, validasi
  folder, hasil yang disimpan untuk laporan.
- `internal/panel/orchestrator/gate.go` — tipe baris, `Runner.Gate`,
  aturan tiap baris.
- `internal/panel/orchestrator/gate_port.go` — pemeriksaan port master
  dari host panel dengan simpanan 10 menit.
- `internal/panel/store/migrations/0013_gate.sql`,
  `internal/panel/store/backup.go` — kolom dan aksesornya.
- `internal/panel/web/handlers_gate.go` — model tampilan daftar
  prasyarat; `web/templates/_gate.html` — potongan daftar.
- `internal/panel/web/handlers_guide.go`, `web/templates/panduan.html`,
  `internal/panel/web/guide_readme_test.go`.

Diubah (yang utama): `internal/proto/health.go`;
`internal/agent/monitor/{monitor,changes}.go`;
`internal/agent/drivers/mysql/{health,quarantine,lift,dump,salvage,leftover}.go`;
`internal/agent/{cli_admin,cli_dropdb,cli_rootsql}.go`;
`internal/panel/monitor/{monitor,alerts,state,start_state}.go`;
`internal/panel/orchestrator/{runner,promote,promote_view,create_replica,resync,change_databases,prepare_master,controls,tls}.go`;
`internal/panel/web/{router,render,audit,handlers_promote,handlers_mysql,handlers_controls,handlers_servers,handlers_oldmaster,handlers_settings,handlers_alerts,handlers_jobs,handlers_audit}.go`;
`internal/panel/notify/message.go`; template `promote.html`,
`replica_new.html`, `server_detail.html`, `replica_detail.html`,
`replica_databases.html`, `master_detail.html`, `settings.html`,
`cutover.html`, `layout.html`; `README.md`; dua dokumen keamanan yang
hidup.

---

### Task 1: Butir bawaan sisi agent

**Files:**
- Modify: `internal/agent/drivers/mysql/quarantine.go:676-728`, `lift.go:318`, `dump.go:182,655-663`, `salvage.go:360-373,518-522`, `leftover.go:67-106`, `rebuild_load.go` (`AdviceTexts`)
- Modify: `internal/agent/cli_admin.go:112-123,453-456,514-561`, `cli_dropdb.go:145,685`, `cli_rootsql.go:127-136,170,205-209`; `internal/agent/drivers/mysql/dump.go:399-411`
- Modify: `internal/agent/drivers/mysql/testmysql/{instance,host}.go`
- Test: `quarantine_test.go`, `lift_test.go`, `salvage_test.go`, `cli_admin_test.go`, `cli_dropdb_test.go`, `testmysql/instance_test.go`

**Interfaces:**
- Produces: `func mysql.FindClientTool(mysqldPath, name string) string` (pembungkus `findClientTool`); teks F2–F5 di bawah; `AdviceTexts()` bertambah satu (F2), jadi hitungan di `TestAdviceTexts` menjadi 15.

Teks:
- **F2** (settle, bendera tidak bisa dibaca): `kunci baca-saja server ini tidak bisa diperiksa sebelum karantina diakhiri (%v); karantina tidak diakhiri dan tidak ada yang diubah. Tekan "Ulangi dari tahap ini"`
- **F3** (`reset-admin`, akun tersimpan tidak terbaca; dicetak sebelum pertanyaan, lalu perintah berlanjut): `Akun MySQL yang disimpan agent tidak bisa dibaca (%v); akun itu akan diganti dengan yang baru.`
- **F4** (`reset-admin`, login akun baru berhasil tetapi simpan gagal), dua baris: `Akun dbpanel_admin@localhost sudah dibuat ulang dan bisa masuk, tetapi tidak bisa disimpan agent (%s). Kunci baca-saja server ini sudah terpasang lagi.` lalu `Agent masih memakai akun lama dan mungkin tidak bisa masuk ke MySQL. Perbaiki sebabnya, lalu jalankan perintah ini lagi.`
- **F5** (galat berhenti harness, bukan teks operator): `mysqld %d (dir %s, port %d, server id %d) killed after shutdown timeout`.

- [ ] **Step 1: Test gagal untuk settle (A-M3).** `TestSettleTransientReadIsNotT7`: dengan `openAdmin` yang gagal sekali, `settleRebuilt` mengembalikan galat yang teksnya berawalan F2 sampai `(`, bukan `errWritableSinceLoad`; catatan `writableSince` kosong; panggilan kedua (baca berhasil, kedua bendera ON) mengakhiri karantina. `TestSettleFlagsOffIsStillT7`: `read_only=OFF` tetap T7.
- [ ] **Step 2:** Jalankan `go test ./internal/agent/drivers/mysql -run 'TestSettle(TransientReadIsNotT7|FlagsOffIsStillT7)' -short`; harus FAIL pada yang pertama.
- [ ] **Step 3:** `flagsOnNow` mengembalikan `errFlagsUnread{cause error}` untuk host/`openAdmin`/query yang gagal dan galat lamanya hanya untuk bendera yang mati; `settleRebuilt` menjawab F2 untuk yang pertama. Tambahkan F2 ke `AdviceTexts()`.
- [ ] **Step 4: A-M8.** Test `TestSecondHoldDumpDoesNotWriteSuperOn`: dua `begin` bertumpuk, `loadTrailer` milik pemegang kedua tidak memuat `SET GLOBAL super_read_only = ON`; pemegang pertama memuatnya. Implementasi: `liftHold.first bool`, dan `dump.go:182` memakai `hold.superWasOn() && hold.first`.
- [ ] **Step 5: `recordLeftover` gagal dua kali.** Test `TestLeftoverEmptyResultFailsWhenStaleRecordRemains`: hasil kosong, tulis gagal, hapus gagal dengan berkas lama masih ada → `mysql.leftover` gagal dengan `leftoverUnsaved`; hapus gagal sesudah berkas hilang → OK. Implementasi: `recordLeftover` dan `forgetLeftover` mengembalikan `(staleMayRemain bool, err error)`.
- [ ] **Step 6: A-M6.** Test `TestDropDatabaseFindsMysqldumpBesideMysqld`: `MysqldPath` menunjuk direktori sementara berisi `mysqld` dan `mysqldump` palsu, `PATH` kosong → program yang dijalankan adalah yang di samping `mysqld`. Implementasi: `rootSession.mysqldPath`; `cliMysqldump` tetap sebagai nama yang bisa diganti test, diselesaikan lewat `mysql.FindClientTool`.
- [ ] **Step 7: A-M7 dan F4.** Test `TestResetAdminReplacesUnreadableStoredAccount`: `creds.Load` gagal → F3 tercetak, perintah berlanjut, akun baru tersimpan, kode keluar 0. `cliAgentCreds` yang gagal tetap `textCredsUnusable`. Test `TestResetAdminSaveFailedAfterGoodLogin`: `verifyingStore` login berhasil, `Save` gagal → dua baris F4, bukan `textResetFailed`; syslog `not-stored`. Implementasi: tipe `storeWriteError` yang dibungkus `verifyingStore.Save`, dan `case` sebelum `default:` di `cli_admin.go:557`.
- [ ] **Step 8: Harness.** `supervisor.stop` memakai F5; sebelum SIGTERM `Instance` mencoba (batas 2 detik, galat diabaikan) membaca `information_schema.processlist` dan `performance_schema.clone_status`, dan pada kegagalan berhenti cleanup mencetak keduanya serta `logTail()` lewat `t.Logf` sebelum `os.RemoveAll`. Test `TestStopFailureNamesInstanceAndKeepsLogTail` dengan `supervisor` palsu yang tidak mau berhenti.
- [ ] **Step 9:** Tiga gerbang (agent, panel, `make test`), semua exit 0.
- [ ] **Step 10: Commit** per langkah 3–8, misalnya `fix(agent): settle says retry when the read-only flags cannot be read`.

### Task 2: Butir bawaan sisi panel

**Files:**
- Modify: `internal/panel/web/handlers_oldmaster.go:644-657`, `handlers_mysql.go:564-570`; `web/templates/server_detail.html:343`
- Modify: `internal/panel/orchestrator/controls.go:288-295`, `promote.go:261-269,747-752,777,1283`
- Modify: `internal/panel/monitor/alerts.go:401-404`, `start_state.go:203-210,364-388`, `state.go:514-531`
- Test: berkas `_test.go` di samping masing-masing

**Interfaces:**
- Produces: `func hub.SentByAgent(err error) bool` (benar bila galat itu `*hub.AgentError`).

Teks:
- **F1** (judul kotak, karantina tertunda): `Master lama (belum dikarantina; promote {{if .Planned}}terencana{{else}}darurat{{end}} ke {{or .NewMaster.Name "master baru"}} pada <waktu>)`. Judul lama tetap untuk yang sudah dikarantina.
- **F6** (dialog hapus, agent melaporkan peran lain): `Agent server ini melaporkan peran %s, bukan karantina. Menghapusnya dari panel tidak mengubah apa pun di server itu; periksa dulu peran itu di halaman server.`
- **F7** (peringatan, topologi sudah dihapus): `master lama sempat bisa ditulis; topologinya sudah dihapus dari panel, jadi tulisan itu tidak bisa diperiksa lagi dari panel. Hapus server ini` dan alasan tutup `topologinya sudah dihapus dari panel; tulisan itu tidak bisa diperiksa lagi dari panel`.

- [ ] **Step 1: Test gagal.**
  - `TestOldMasterHeadingPendingQuarantine`: halaman server dengan `QuarantinePending` memuat `belum dikarantina` dan tidak memuat `dikarantina sejak`.
  - `TestDeleteDialogNamesReportedRole` (P-M3): `box.AgentRole` terisi → F6, tanpa `tetap dikarantina`.
  - `TestDeleteNewMasterWithOnlyOldMasterRow` (P-M4): master yang satu-satunya baris replikanya milik server berperan `quarantined` bisa dihapus.
  - `TestStartLateTextWhenTopologyDeleted` (P-M5): F7; promote kedua tetap teks lama.
  - `TestLateAlertLeavesUnverifiedText` (P-M6): peringatan yang sempat T1b kembali ke `lateMessage` saat laporan berikutnya bisa memeriksa.
  - `TestReplicasDisconnectedSkipsQuarantined` (P-M7).
  - `TestBusyLabelForQuarantinedOldMaster`: penolakan sibuk untuk baris master lama memakai label `master lama`, bukan `replika`.
  - `TestRebuildFormBusyDuringTopologyPromote`: form bangun ulang menandai sibuk bila topologi punya promote berjalan.
  - `TestAgentSentOfflineIsNotDisconnect`: agent yang menjawab `mysql.quarantine` dengan kode `offline` membuat langkah gagal (karantina tidak dicatat tertunda); hub yang benar-benar putus tetap menunda.
- [ ] **Step 2:** Jalankan `go test ./internal/panel/... -run '<nama-nama di atas>' -short`; semuanya FAIL.
- [ ] **Step 3:** Implementasi tiap butir, perubahan terkecil seperti di kolom "Smallest change" tinjauan akhir sisi panel. Untuk `offline`: `promote.go:777` dan `:1283` menganggap "tidak tersambung" hanya bila `errors.Is(err, proto.ErrOffline) && !hub.SentByAgent(err)`.
- [ ] **Step 4:** Dua gerbang panel, exit 0.
- [ ] **Step 5: Commit** per butir.

### Task 3: Batas dan label untuk teks agent di halaman pemantauan dan audit

**Files:**
- Modify: `internal/panel/web/handlers_servers.go:645-660`, `handlers_mysql.go` (model halaman master dan replika), `audit.go:126-135`; template `master_detail.html:145`, `replica_detail.html:105-110`, `server_detail.html:141-143`
- Test: `internal/panel/web/agenttext_sites_test.go`, `audit_test.go`

**Interfaces:**
- Consumes: `monitor.AgentText(s string, max int) string` (`alerts.go:417`), `maxAgentWords` (`handlers_oldmaster.go:1010`), `agentWords` (pemberi label yang dipakai pekerjaan).

- [ ] **Step 1: Test gagal.** `TestHealthFieldsBoundedAndLabelled`: laporan dengan `MySQLError`, `LastSQLError`, `LastIOError`, `PinError` sepanjang 5.000 karakter berisi baris baru dan `RolePin` tak dikenal sepanjang 200 karakter → di ketiga halaman tiap nilai satu baris, paling panjang 300 karakter, didahului label `Pesan dari agent`; `RolePin` tak dikenal tampil sebagai `tidak dikenal` diikuti nilai yang dibatasi 32 karakter. `TestAuditOutcomeLabelsAgentError`: hasil audit yang berasal dari `*hub.AgentError` berawalan `Pesan dari agent %s (bukan dari panel): `; galat panel tidak berlabel.
- [ ] **Step 2:** Jalankan; FAIL.
- [ ] **Step 3:** Batasi dan beri label saat model tampilan dibangun (bukan di template). `outcomeOf` memberi label saat menulis; baris audit lama tidak diubah (rantai hash mencakup `outcome`).
- [ ] **Step 4:** Dua gerbang panel, exit 0. **Commit:** `fix(panel): bound and label agent text on monitoring pages and in audit outcomes`.

### Task 4: "Perketat" menjadi "Wajibkan pemeriksaan sertifikat TLS"

**Files:**
- Modify: `web/templates/master_detail.html:58,62`; `internal/panel/web/handlers_jobs.go:95`, `handlers_audit.go:89`; `internal/panel/monitor/alerts.go:623`; `internal/panel/orchestrator/create_replica.go:988,1006`, `tls.go:679`; komentar yang menyebut nama itu di berkas yang sama
- Modify: `internal/panel/web/web_mysql_jobs_test.go:1275`
- Modify: `README.md` (11 baris: 453, 480, 687, 688, 925, 927, 1261, 2817, 2819, 3064, 3117); `docs/superpowers/security/2026-11-10-inventaris-risiko.md` (15 baris); `docs/superpowers/security/2026-11-12-audit-plan-5a.md` (9 baris)

**Interfaces:**
- Produces: nama operator **`Wajibkan pemeriksaan sertifikat TLS`**. Pengenal kode tidak berubah: `tighten_tls`, `KindTightenTLS`, `ActTightenTLS`, rute `/masters/{id}/tighten-tls`, `StartTightenTLS`, field `tighten_*`.

Penggantian (kiri persis seperti sekarang):

| Sekarang | Menjadi |
|---|---|
| `Perketat sambungan replikasi` (judul, tombol, label pekerjaan, label audit, `jobKindNames`) | `Wajibkan pemeriksaan sertifikat TLS` |
| `Perketat sambungan replikasi master {{.Server.Name}} dan replikanya.` | `Wajibkan pemeriksaan sertifikat TLS untuk replikasi master {{.Server.Name}} dan replikanya.` |
| `Jalankan Perketat sambungan replikasi di halaman master` | `Jalankan Wajibkan pemeriksaan sertifikat TLS di halaman master` |
| `… dan Perketat diulang.` (`tls.go:679`) | `… dan Wajibkan pemeriksaan sertifikat TLS diulang.` |
| README dan dokumen keamanan: `"Perketat"`, `**Perketat**`, `Perketat` sebagai nama tombol atau pekerjaan | nama baru, dengan tanda yang sama |

`TLS diperketat` (kata kerja, inventaris baris 325 dan 419) tidak diganti. Ledger, rencana, spek lain dan audit 5b-1 tidak disentuh.

- [ ] **Step 1: Test gagal.** `TestNoOperatorTextSaysPerketat` (`internal/panel/web`): tidak ada berkas di `web/templates/` dan tidak ada literal string di `internal/panel/**/*.go` (bukan `_test.go`, bukan komentar) yang memuat `Perketat`; `README.md` tidak memuatnya. Ubah `web_mysql_jobs_test.go:1275` ke nama baru.
- [ ] **Step 2:** Jalankan; FAIL.
- [ ] **Step 3:** Ganti menurut tabel. Di halaman master, penyebutan pertama ditulis `Wajibkan pemeriksaan sertifikat TLS (TLS certificate verification)`.
- [ ] **Step 4:** `grep -rn "Perketat" web/templates internal/panel README.md docs/superpowers/security/2026-11-10-inventaris-risiko.md docs/superpowers/security/2026-11-12-audit-plan-5a.md` hanya menyisakan komentar test dan `diperketat`. Dua gerbang panel, exit 0.
- [ ] **Step 5: Commit:** `feat(panel): rename the tighten action to "Wajibkan pemeriksaan sertifikat TLS"`.

### Task 5: Agent melaporkan jam dan `performance_schema`

**Files:**
- Modify: `internal/proto/health.go:7-56`
- Modify: `internal/agent/drivers/mysql/health.go:149-188`; `internal/agent/monitor/monitor.go:489-534`, `changes.go:26-46`
- Test: `internal/proto/health_test.go`, `internal/agent/drivers/mysql/health_test.go`, `internal/agent/monitor/monitor_test.go`

**Interfaces:**
- Produces, di `proto.HealthSnapshot`:
  ```go
  // ClockUnixMS is the agent's wall clock when the snapshot was taken,
  // in Unix milliseconds; 0 from an agent before this field.
  ClockUnixMS int64 `json:"clock_unix_ms,omitempty"`
  // PerfSchema is @@global.performance_schema; nil while MySQL does
  // not answer and from an agent before this field.
  PerfSchema *bool `json:"performance_schema,omitempty"`
  ```

- [ ] **Step 1: Test gagal.** `TestSnapshotStaysComparable` (proto): `var a, b proto.HealthSnapshot; _ = a == b` dikompilasi (menjaga aturan "tanpa slice/map"). `TestHealthReportsPerfSchema` (mysqld sungguhan): `PerfSchema` bukan nil dan `*PerfSchema == true` pada instance bawaan; dengan `ExtraArgs: performance_schema=OFF` nilainya `false`. `TestHealthPerfSchemaNilWhenMySQLDown`: `MySQLOK == false` → `PerfSchema == nil`. `TestReportCarriesAgentClock` (monitor, jam palsu): laporan dari `tick` dan dari `forcedCheck` membawa `ClockUnixMS == takenAt.UnixMilli()`. `TestChangedOnRolePinAndPerfSchema`: `changed()` benar bila `RolePin` berubah, dan bila `PerfSchema` berubah dari nil/false/true ke nilai lain; `ClockUnixMS` yang berubah sendirian tidak membuat `changed()` benar.
- [ ] **Step 2:** Jalankan test-test itu; FAIL (field belum ada).
- [ ] **Step 3:** Tambah dua field. `'performance_schema'` masuk daftar `IN (...)` pernyataan pertama `readHealth` dan dibaca dengan `on(...)`; tidak ada pernyataan SQL baru. Monitor mengisi `ClockUnixMS` di satu fungsi pembantu yang dipanggil `tick` dan `forcedCheck` tepat sesudah `takenAt` diambil (fungsi yang sama dipakai Task 6 untuk keadaan cadangan): `func (m *Monitor) stamp(s *proto.HealthSnapshot, takenAt time.Time)`. `changed()` memuat `RolePin` (tambahkan bila belum ada) dan `PerfSchema`.
- [ ] **Step 4:** Tiga gerbang, exit 0.
- [ ] **Step 5: Commit:** `feat(agent): report the agent clock and performance_schema in health snapshots`.

### Task 6: Agent memindai folder cadangan

**Files:**
- Create: `internal/agent/monitor/backup.go`, `internal/agent/monitor/backup_test.go`
- Modify: `internal/proto/health.go` (`MonitorConfigureParams`, `HealthSnapshot`, tipe baru); `internal/agent/monitor/monitor.go:161-173,210-278,306-329`, `changes.go`

**Interfaces:**
- Consumes: `(*Monitor).stamp` dari Task 5.
- Produces:
  ```go
  // in MonitorConfigureParams
  // BackupFolder is the folder whose newest file the agent reports;
  // "" turns the scan off. An agent before this field ignores it.
  BackupFolder string `json:"backup_folder,omitempty"`

  // in HealthSnapshot
  // Backup is the backup folder's state; nil when no folder is set and
  // from an agent before this field.
  Backup *BackupState `json:"backup,omitempty"`

  type BackupState struct {
      Status       string `json:"status"`                  // BackupOK | BackupNone | BackupUnreadable | BackupPending
      Reason       string `json:"reason,omitempty"`        // set with BackupUnreadable
      NewestAgeSec int64  `json:"newest_age_sec,omitempty"` // age of the newest regular file when the snapshot was taken
      NewestSize   int64  `json:"newest_size,omitempty"`
      Truncated    bool   `json:"truncated,omitempty"`     // the entry limit ended the scan early
      ScanAgoSec   int64  `json:"scan_ago_sec,omitempty"`
  }
  const (
      BackupOK = "ok"; BackupNone = "none"; BackupUnreadable = "unreadable"; BackupPending = "pending"
      BackupReasonMissing = "missing"; BackupReasonNotDir = "not_dir"; BackupReasonDenied = "denied"
      BackupReasonHidden = "hidden"; BackupReasonInvalid = "invalid"; BackupReasonTimeout = "timeout"
      BackupReasonLimit = "limit"; BackupReasonError = "error"
  )
  ```
- Produces (paket `monitor` agent): `func ValidBackupFolder(p string) (reason string, ok bool)` — aturan folder dari Global Constraints; `reason` adalah `BackupReasonHidden` untuk `/home`, `/root`, `/run/user`, `/tmp`, `/var/tmp` dan `BackupReasonInvalid` untuk yang lain. Panel memakai fungsi yang sama lewat salinan aturan di Task 8 (panel tidak mengimpor paket agent di kode produksi); sebuah test di Task 8 membandingkan keduanya atas tabel kasus yang sama.

Aturan pemindaian:
- Folder dibuka dengan `os.OpenRoot(folder)`, lalu `fs.WalkDir(root.FS(), ".", fn)`. Hanya `DirEntry.Type()` dan `DirEntry.Info()` (lstat) yang dipakai; tidak ada `Open` atas entri. Entri yang bukan berkas biasa atau direktori (symlink, FIFO, soket, perangkat) dilewati. Direktori lebih dalam dari tingkat ke-3 dilewati dengan `fs.SkipDir`.
- Yang dicari: berkas biasa dengan `ModTime` terbaru. `NewestAgeSec = now − ModTime`, tidak kurang dari 0.
- Hitungan entri (semua entri yang dilihat) berhenti di 10.000: `Truncated = true`; bila belum ada berkas biasa yang ditemukan, hasilnya `BackupUnreadable`/`BackupReasonLimit`.
- Pemindaian berjalan di goroutine sendiri; putaran `Run` menunggu paling lama 10 detik. Bila belum selesai: `BackupUnreadable`/`BackupReasonTimeout`, dan **tidak ada pemindaian baru** sampai goroutine itu kembali.
- Jadwal: di putaran `Run` sesudah `sweepSalvage`, bila folder terisi dan (folder baru saja berubah, atau pemindaian terakhir ≥ 5 menit lalu). Sesudah `monitor.configure` mengganti folder, hasil lama dibuang dan laporan membawa `BackupPending` sampai pemindaian pertama selesai.
- `Configure` menerima folder apa pun: yang tidak lolos `ValidBackupFolder` disimpan sebagai "tidak dipindai" dan dilaporkan `BackupUnreadable` dengan alasannya. `monitor.configure` tetap berhasil.
- `changed()` benar bila `Backup` berubah antara nil dan bukan nil, atau `Status`/`Reason` berubah. Umur dan ukuran saja tidak.
- Log agent paling sering satu baris per menit per pesan (`m.warn`), berbahasa Inggris, tanpa nama berkas.

- [ ] **Step 1: Test gagal** (direktori sementara, jam palsu):
  - `TestBackupScanFindsNewestWithinThreeLevels`: berkas di tingkat 1, 2, 3 dan 4; yang terbaru di tingkat 4 diabaikan; hasil `BackupOK` dengan umur dan ukuran berkas terbaru di tingkat ≤ 3.
  - `TestBackupScanSkipsSymlinksAndSpecials`: symlink ke berkas lebih baru di luar folder, symlink ke direktori, FIFO, dan symlink absolut ke `/etc/passwd` tidak pernah menjadi "terbaru"; pemindaian tidak macet pada FIFO.
  - `TestBackupScanEmptyFolderIsNone`, `TestBackupScanZeroByteNewest` (`BackupOK`, `NewestSize == 0`), `TestBackupScanFutureMtimeIsAgeZero`.
  - `TestBackupScanLimits`: 10.001 entri tanpa berkas biasa → `unreadable/limit`; 10.001 entri dengan berkas biasa di awal → `BackupOK` dan `Truncated`.
  - `TestBackupScanUnreadableReasons`: folder tidak ada → `missing`; berkas biasa sebagai folder → `not_dir`; `/tmp/x` dan `/home/x` → `hidden`; `relatif/x`, `/a/../b`, `/` , `/proc/1` → `invalid`.
  - `TestBackupScanOneAtATime`: pemindai palsu yang tertahan → hasil `unreadable/timeout` sesudah 10 detik jam palsu, dan putaran berikutnya tidak memulai pemindaian kedua (hitung pemanggilan).
  - `TestBackupScanEveryFiveMinutes` dan `TestFolderChangeReportsPendingThenResult`.
  - `TestConfigureKeepsSettingsWithBadFolder`: `monitor.configure` dengan `check_every_sec` sah dan folder `/home/x` → berhasil, interval diterapkan, laporan membawa `unreadable/hidden`.
  - `TestOldParamsWithoutFolderTurnScanOff`: `monitor.configure` tanpa `backup_folder` sesudah yang berisi → `Backup == nil`.
- [ ] **Step 2:** `go test ./internal/agent/monitor -run 'TestBackup|TestConfigureKeeps|TestFolderChange|TestOldParams' -short`; FAIL.
- [ ] **Step 3:** Tulis `backup.go`: `ValidBackupFolder`, `scanBackup(folder string, now func() time.Time) backupResult`, penjadwalan di `Run`, dan `(*Monitor).backupState(now time.Time) *proto.BackupState` yang dipanggil `stamp`.
- [ ] **Step 4:** Tiga gerbang, exit 0.
- [ ] **Step 5: Commit:** `feat(agent): report the age and size of the newest file in a backup folder`.

### Task 7: Panel menyimpan akun, folder cadangan dan tanda; peringatan `backup_stale`

**Files:**
- Create: `internal/panel/store/migrations/0013_gate.sql`, `internal/panel/store/backup.go`
- Modify: `internal/panel/store/{servers,settings,migrate_test}.go` (hitungan migrasi 12 → 13 di `migrate_test.go:53,62,157`); `internal/panel/orchestrator/runner.go:258-280`; `internal/panel/monitor/{monitor,alerts,settings}.go`; `internal/panel/notify/message.go:58-100`; `internal/panel/web/handlers_alerts.go:20-49`
- Test: `store/backup_test.go`, `orchestrator/inventory_test.go`, `monitor/alerts_test.go`, `monitor/monitor_test.go`, dan test pencacah yang sudah ada (`TestNewKindsHaveLabels`, `TestEveryKindHasIconAndName`, `web_settings_test.go:170,335`)

**Interfaces:**
- Consumes: `proto.BackupState`, `MonitorConfigureParams.BackupFolder`, `HealthSnapshot.ClockUnixMS` (Task 5–6).
- Produces:
  - Migrasi 0013: `ALTER TABLE servers ADD COLUMN backup_dir TEXT NOT NULL DEFAULT ''`; `ALTER TABLE servers ADD COLUMN backup_max_age_hours INTEGER NOT NULL DEFAULT 48`; `ALTER TABLE mysql_inventory ADD COLUMN account TEXT NOT NULL DEFAULT ''`.
  - `store.Server.BackupDir string`, `store.Server.BackupMaxAgeHours int`; `func (s *Store) SetServerBackup(id int64, dir string, maxAgeHours int) error`; `store.Inventory.Account string` (diisi `inventoryCallCut` dari `ConnectResult.Account`).
  - Kunci pengaturan: `SettingPanelKeyBackedUpAt = "panel_key_backed_up_at"`, `SettingPanelKeyBackedUpBy = "panel_key_backed_up_by"`, `SettingAuditForwardedAt = "audit_forwarded_at"`, `SettingAuditForwardedBy = "audit_forwarded_by"` (nilai waktu RFC 3339 UTC; kosong berarti belum ditandai).
  - `func (m *Monitor) ConfigureServer(ctx context.Context, id int64)` — satu `monitor.configure` ke satu server, dengan `BackupFolder` dari baris server; `OnOnline` dan `Reconfigure` juga mengirim folder itu.
  - `func monitor.ClockSkew(v View) (time.Duration, bool)` — `time.UnixMilli(v.Snap.ClockUnixMS).Sub(v.ReceivedAt)`; `false` bila `v.Snap == nil`, `ClockUnixMS == 0`, atau laporan terakhir adalah laporan tertunda (`View.Backlog`, field baru yang diisi `report` dari `HealthEvent.Backlog`).
  - `type monitor.BackupVerdict struct { Status string; AgeSec, Size int64; Reason string }` dan `func monitor.JudgeBackup(v View, srv store.Server, now time.Time) BackupVerdict`, dengan `Status` salah satu `unset`, `unknown`, `ok`, `stale`, `empty_file`, `none`, `unreadable`. Umur yang dinilai: `NewestAgeSec + now.Sub(v.ReceivedAt)`. `unknown` bila folder terisi tetapi `v.Snap == nil`, `Snap.Backup == nil`, atau `Status == pending`.
  - `func monitor.BackupMessage(b BackupVerdict, master string, maxAgeHours int) string` — teks G8a–G8d di bawah; `""` untuk `unset`, `unknown` dan `ok`. Dipakai peringatan ini, kartu cadangan (Task 8) dan baris gerbang (Task 9).
  - Jenis peringatan `KindBackupStale = "backup_stale"` (di `AllKinds()` sesudah `KindMasterLockKept`).

Teks cadangan (`%s` pertama nama master):
- **G8a** (`stale`) `Berkas terbaru di folder cadangan master %s berumur %s (batas %d jam).`
- **G8b** (`empty_file`) `Berkas terbaru di folder cadangan master %s kosong (0 byte).`
- **G8c** (`none`) `Folder cadangan master %s tidak berisi berkas.`
- **G8d** (`unreadable`) `Folder cadangan master %s tidak bisa dibaca agent: %s.` dengan alasan: `missing` → `folder itu tidak ada`; `not_dir` → `itu bukan folder`; `denied` → `izin ditolak`; `hidden` → `folder itu tidak terlihat oleh layanan agent`; `invalid` → `path-nya tidak sah`; `timeout` → `pemindaian tidak selesai dalam 10 detik`; `limit` → `lebih dari 10.000 entri dan belum ada berkas yang ditemukan`; `error` dan kode lain → `galat lain; lihat log agent`.

Peringatan `backup_stale`: aturan status di `alertRules`, hanya untuk server berperan master. Terbuka bila `JudgeBackup` memberi `stale`, `empty_file`, `none` atau `unreadable`; tertutup bila `ok` atau `unset`; `unknown` tidak membuka dan tidak menutup. Pesannya `BackupMessage`. Label di halaman peringatan dan di Telegram: **`Cadangan master usang`**. Tingkat: peringatan biasa (bukan `critKinds`, bukan `infoKinds`), tautan `masterKinds`, bisa ditunda (tidak masuk `unsnoozableKinds`).

- [ ] **Step 1: Test gagal.**
  - `TestMigration0013Columns` dan hitungan migrasi 13.
  - `TestSetServerBackupRoundTrip`.
  - `TestInventoryRefreshUpdatesAccount`: `Connect` lalu `RefreshInventory` dengan `Account` berbeda → `GetInventory(...).Account` mengikuti yang terakhir.
  - `TestConfigureSendsBackupFolder`: `ConfigureServer`, `OnOnline` dan `Reconfigure` mengirim `backup_folder` dari baris server; server tanpa folder mengirim field kosong.
  - `TestClockSkew`: laporan dengan `ClockUnixMS` 45 detik di depan `ReceivedAt` → 45 s; laporan tertunda → `false`; agent lama → `false`.
  - `TestJudgeBackupTable` dan `TestBackupMessageTexts`: tiap status dan tiap alasan, termasuk umur yang melewati batas hanya karena waktu berlalu sejak laporan (laporan 47 jam, diterima 2 jam lalu, batas 48 → `stale`).
  - `TestBackupStaleAlertOpensAndCloses`: terbuka pada `stale`, pesan sama dengan G8a; tetap terbuka pada `unknown`; tertutup pada `ok`; tidak pernah terbuka untuk replika atau server yang dikarantina.
- [ ] **Step 2:** Jalankan; FAIL.
- [ ] **Step 3:** Implementasi. `inventoryCallCut` menyimpan `Account` apa adanya, dibatasi 255 byte dan satu baris.
- [ ] **Step 4:** Dua gerbang panel, exit 0.
- [ ] **Step 5: Commit** per bagian: migrasi dan store; akun; `ConfigureServer`; `ClockSkew`; peringatan.

### Task 8: Isian cadangan di halaman server dan tanda di Pengaturan

**Files:**
- Modify: `internal/panel/web/router.go:198-250`, `handlers_servers.go:123-183,702-`, `handlers_settings.go:341-405`, `handlers_confirm.go:23-64`, `handlers_audit.go:80-95`; template `server_detail.html`, `settings.html`
- Create: `internal/panel/web/backup_folder.go`, `internal/panel/web/backup_folder_test.go`
- Test: `web_servers_test.go`, `web_settings_test.go`

**Interfaces:**
- Consumes: `Store.SetServerBackup`, `Monitor.ConfigureServer`, kunci pengaturan Task 7, `monitor.JudgeBackup`, `monitor.BackupMessage`.
- Produces:
  - `POST /servers/{id}/backup` → `saveServerBackup` (form `backup_dir`, `backup_max_age_hours`); tanpa token konfirmasi; diaudit sebagai `server_backup` dengan `{"backup_dir", "backup_max_age_hours"}`; sesudah menyimpan memanggil `ConfigureServer` (galatnya tidak menggagalkan penyimpanan; folder terkirim lagi saat agent tersambung).
  - `POST /settings/panel-key-backed-up` dan `POST /settings/audit-forwarded` (masing-masing dengan `marked=1` atau `marked=0`), diaudit sebagai `panel_key_marked` dan `audit_forward_marked` dengan `{"marked"}`; siapa dan kapan disimpan di kunci pengaturan Task 7.
  - `func validBackupFolder(p string) (text string, ok bool)` di `backup_folder.go`, dengan aturan yang sama dengan `ValidBackupFolder` agent.

Teks:
- **B1** judul kartu: `Cadangan (backup)`; isian: `Folder cadangan` dan `Umur paling lama (jam)`; tombol `Simpan`.
- **B2** keterangan: `Panel hanya melihat umur dan ukuran berkas terbaru di folder ini (paling dalam tiga tingkat), bukan apakah cadangan itu bisa dipulihkan. Nama dan isi berkas tidak pernah dikirim ke panel. Kosongkan folder bila server ini tidak menyimpan cadangan.`
- **B3** `Folder cadangan harus path absolut tanpa "..", misalnya /var/backups/mysql.`
- **B4** `Folder di bawah %s tidak terlihat oleh layanan agent; pakai folder lain, misalnya /var/backups/mysql.` (`%s`: awalan yang cocok, misalnya `/home`)
- **B5** `Folder %s tidak boleh dipakai sebagai folder cadangan.` (untuk `/`, `/proc`, `/sys`, `/dev`)
- **B6** `Umur paling lama harus 1 sampai 8760 jam.`
- **B7** keadaan sekarang di kartu, satu dari: `Belum ada laporan dari agent.`; `Berkas terbaru: %s lalu, %s.` (umur dan ukuran); atau hasil `monitor.BackupMessage`.
- **B8** Pengaturan, dua baris dengan tombol: `panel.key sudah dicadangkan` (`Tandai sudah` / `Batalkan tanda`) dan `Log audit diteruskan ke server lain` (sama); di bawah masing-masing bila ditandai: `Ditandai oleh %s pada %s.`

- [ ] **Step 1: Test gagal.** `TestBackupFolderRules`: tabel kasus (sah: `/var/backups/mysql`, `/srv/b`; B3: `relatif`, `/a/../b`, `/a//b`, string 1025 byte, path dengan `\n`; B4: `/home/x`, `/root`, `/run/user/1000`, `/tmp/x`, `/var/tmp`; B5: `/`, `/proc/1`, `/sys`, `/dev/sda`; `/homework` dan `/tmpfiles` sah). `TestBackupFolderRulesMatchAgent`: tabel yang sama terhadap `agentmonitor.ValidBackupFolder` (impor di `_test.go` saja) memberi sah/tidak sah yang sama. `TestSaveServerBackup`: menyimpan, mengaudit, memanggil `ConfigureServer` sekali; folder buruk → 400 dengan teksnya dan tidak ada yang tersimpan; umur 0 atau 8761 → B6. `TestServerPageShowsBackupState`. `TestSettingsMarks`: menandai dan membatalkan, audit memuat nama pengguna.
- [ ] **Step 2:** Jalankan; FAIL.
- [ ] **Step 3:** Implementasi. Kartu cadangan tampil di halaman setiap server yang sudah berpasangan, di bawah kartu MySQL.
- [ ] **Step 4:** `bash scripts/tailwind.sh` bila kelas baru dipakai; dua gerbang panel, exit 0.
- [ ] **Step 5: Commit:** `feat(panel): per-server backup folder form and the two marks in settings`.

### Task 9: `Runner.Gate` dan aturan tiap baris

**Files:**
- Create: `internal/panel/orchestrator/gate.go`, `gate_port.go`, `gate_test.go`, `gate_port_test.go`
- Modify: `internal/panel/orchestrator/runner.go:105-135` (`Deps`), `promote_view.go:17-21` (`Health`); `internal/panel/web/handlers_mysql.go:476-482` (memakai `orchestrator.ReplicaUnverified`)

**Interfaces:**
- Consumes: `store.Server.{AgentVersion,BackupDir,BackupMaxAgeHours}`, `store.Inventory.Account`, `GetServerTLS`, `store.Replica.{State,TLSVerified}`, `Health.View`, `monitor.{LoadSettings,DiskPercent,ClockSkew,JudgeBackup,BackupMessage}`, kunci pengaturan Task 7, `Deps.{Version,LocalServerID,Hub}`.
- Produces:
  ```go
  type GateStatus int
  const ( GateGreen GateStatus = iota; GateYellow; GateRed )

  type GateAction string
  const (
      GatePromotePlanned   GateAction = "promote_planned"
      GatePromoteEmergency GateAction = "promote_emergency" // rows shown, never refused
      GateCreateReplica    GateAction = "create_replica"
      GateRebuildOldMaster GateAction = "rebuild_old_master"
      GateResync           GateAction = "resync"
      GateAddDatabases     GateAction = "add_databases"
      GateRemoveWithDrop   GateAction = "remove_databases_drop"
      GatePrepareMaster    GateAction = "prepare_master"
  )

  type GateRow struct {
      Key    string     // agent_version, master_pin, clock, tls, disk, account, perf_schema, backup, approval, master_port, audit_forward, panel_key
      Label  string
      Status GateStatus
      Detail string     // "" when green
      Fix    string     // "" when green
      NeedsAck bool     // backup row, folder not set, on planned promote and rebuild
  }

  type GateInput struct {
      Action   GateAction
      MasterID int64  // topology master; for prepare_master the server itself
      TargetID int64  // candidate, target or replica; 0 for prepare_master
      Approval *bool  // rebuild only: nil = not asked yet, else the agent's answer
  }

  type GateResult struct{ Rows []GateRow }
  func (g GateResult) Red() []GateRow
  func (g GateResult) RedKeys() []string
  func (g GateResult) NeedsBackupAck() bool

  func (r *Runner) Gate(in GateInput) (GateResult, error)

  // GateError is returned by Start*, Retry and Approve; Unwrap gives ErrGateRed.
  type GateError struct{ Rows []GateRow }
  var ErrGateRed = errors.New("prasyarat belum terpenuhi")
  var ErrBackupAck = errors.New("centang cadangan belum diisi")

  func ReplicaUnverified(rep store.Replica) bool // !TLSVerified && (Ready || Attention)
  ```
  `GateError.Error()`: **G0** `prasyarat belum terpenuhi: %s` dengan label baris merah dipisah `, `.
- `Deps.PortProbe func(ctx context.Context, hostPort string) bool` (nil berarti `net.Dialer{Timeout: time.Second}`); `Deps.Now` yang sudah ada dipakai untuk simpanan 10 menit.

Baris per tindakan (urutan tampil = urutan di sini):

| Tindakan | Server yang dinilai | Baris |
|---|---|---|
| `promote_planned`, `promote_emergency` | master, kandidat | agent_version, master_pin, clock, tls (sertifikat master dan replika belum terverifikasi), account, backup; lalu kuning: master_port, audit_forward, panel_key |
| `create_replica` | master, tujuan | agent_version, master_pin, disk, account, perf_schema |
| `rebuild_old_master` | master baru, master lama | agent_version, tls (hanya sertifikat master), disk, account, perf_schema, backup, approval; lalu kuning: master_port, audit_forward, panel_key |
| `resync`, `add_databases` | master, replika itu | agent_version, master_pin, disk, account, perf_schema |
| `remove_databases_drop` | replika itu | agent_version, account |
| `prepare_master` | server itu | agent_version, account |

Aturan dan teks (label; merah; cara memperbaiki). `%s` pertama selalu nama server. Baris `agent_version`, `clock`, `disk` dan `account` dinilai untuk setiap server yang dinilai; hasilnya satu baris, dengan `Detail` berisi kalimat tiap server yang merah, dipisah spasi.

- **agent_version** — label `Versi agent`. Dilewati (hijau) bila `Deps.Version == ""`. Merah **G1a** `Agent di %s versi %s, panel versi %s.` bila berbeda; **G1b** `Versi agent di %s belum diketahui.` bila kosong. Perbaikan **G1f** `Perbarui agent di server itu (Pasang ulang agent di halaman server), lalu muat ulang halaman ini.`
- **master_pin** — label `Pin master`. Merah bila laporan master tidak ada atau `Snap.RolePin != "master"`: **G2a** `Agent master %s tidak melaporkan pin master (yang dilaporkan: %s).` dengan nilai yang dibatasi 32 karakter atau `tidak ada`. Perbaikan **G2f** `Jalankan Siapkan master di halaman server itu. Bila baru saja dijalankan, klik Periksa sekarang di halaman server, lalu muat ulang halaman ini.`
- **clock** — label `Jam (clock)`. Merah bila `|ClockSkew| > 30s` pada salah satu server: **G3a** `Jam server %s berbeda %s dari jam host panel (batas 30 detik).` Perbaikan **G3f** `Selaraskan jam server itu dan host panel dengan NTP (di server: timedatectl set-ntp true), klik Periksa sekarang di halaman server, lalu muat ulang halaman ini.`
- **tls** — label `Pemeriksaan sertifikat TLS`. Merah **G4a** `Master %s belum punya sertifikat MySQL dari panel.` (perbaikan **G4f** `Pasang sertifikat MySQL di halaman server master, lalu jalankan Wajibkan pemeriksaan sertifikat TLS di halaman master.`) atau, hanya untuk promote, **G4b** `Replika %s belum memeriksa sertifikat master.` dengan nama-nama dipisah `, ` (perbaikan **G4g** `Jalankan Wajibkan pemeriksaan sertifikat TLS di halaman master.`). Yang dihitung: `ReplicaUnverified`.
- **disk** — label `Ruang disk`. Merah bila `DiskPercent >= Thresholds.DiskCrit`: **G5a** `Disk %s terpakai %d%% (ambang kritis %d%%).` Perbaikan **G5f** `Kosongkan ruang di server itu, klik Periksa sekarang di halaman server, lalu muat ulang halaman ini.`
- **account** — label `Akun panel`. Nama pengguna adalah bagian `Inventory.Account` sebelum `@` terakhir. Merah bila sama persis dengan `root`: **G6a** `Agent di %s masuk ke MySQL sebagai root.` Perbaikan **G6f** `Buat akun MySQL khusus panel lalu hubungkan lagi (Panduan, "Memperbaiki baris merah"). Bila akunnya sudah diganti di server, klik %s di kartu MySQL halaman server itu.` (`%s`: label tombol `/servers/{id}/mysql/refresh` seperti di template). Server tanpa inventaris tersambung (tujuan Buat replika yang belum punya MySQL): baris itu tidak dinilai untuk server tersebut.
- **perf_schema** — label `performance_schema`. Merah bila `*PerfSchema == false` di master: **G7a** `performance_schema mati di master %s.` Perbaikan **G7f** `Hapus performance_schema=OFF dari konfigurasi MySQL master, lalu restart MySQL master di jam sepi.`
- **backup** — label `Cadangan (backup)`, dari `monitor.JudgeBackup` atas master topologi (pada bangun ulang: master baru). Merah untuk `stale`, `empty_file`, `none`, `unreadable`, dengan `Detail` dari `monitor.BackupMessage` (teks G8a–G8d, Task 7). Perbaikan **G8f** `Jalankan cadangan master, atau betulkan folder cadangan di halaman server master.` Kuning `unset` **G8u** `Folder cadangan master %s belum diisi, jadi panel tidak bisa melihat umur cadangan.` dengan `NeedsAck` pada `promote_planned` dan `rebuild_old_master`.
- **approval** — label `Persetujuan di server`. `Approval == nil`: kuning **G9u** `Persetujuan di server %s belum diperiksa.`; `false`: merah **G9a** `Belum disetujui di server %s.` Perbaikan **G9f** `Jalankan perintah di kotak Persetujuan di server pada halaman ini, lalu klik Periksa lagi.`
- **Tidak diketahui** (semua baris selain agent_version dan master_pin): kuning **G10a** `%s belum diketahui: agent %s sedang tidak tersambung.` atau **G10b** `%s belum diketahui: agent %s belum melaporkannya.` (`%s` pertama: label baris).
- **master_port** (kuning saja) — label `Port MySQL master`. Dinilai hanya bila `LocalServerID` bukan master dan bukan replika topologi itu. Alamat: host dan port yang dipakai replika untuk menyambung ke master (nilai yang dikirim sebagai sumber replikasi; pelaksana menyebut fungsinya di laporan). **G11a** `Port MySQL master %s bisa dijangkau dari host panel; sebaiknya hanya terbuka untuk replika.` Perbaikan **G11f** `Batasi port itu di firewall master hanya untuk alamat replika.`
- **audit_forward** (kuning saja) — label `Log audit`. **G12a** `Log audit belum ditandai diteruskan ke server lain.` Perbaikan **G12f** `Teruskan journal host panel ke server lain (systemd-journal-upload), lalu tandai di Pengaturan.`
- **panel_key** (kuning saja) — label `Cadangan panel.key`. **G13a** `panel.key belum ditandai sudah dicadangkan.` Perbaikan **G13f** `Cadangkan panel.key (README, "Cadangkan kunci panel"), lalu tandai di Pengaturan.`

- [ ] **Step 1: Test gagal** (`gate_test.go`, store sungguhan dan `Health` palsu dari `fake_test.go`):
  - `TestGateRowsPerAction`: untuk tiap tindakan, urutan `Key` sama dengan tabel.
  - Satu test per baris merah: `TestGateAgentVersion` (beda, kosong, `Deps.Version == ""`), `TestGateMasterPin` (tanpa laporan, `replica`, `master`), `TestGateClock` (31 s merah, 30 s hijau, negatif 31 s merah), `TestGateTLS` (tanpa sertifikat; replika siap belum terverifikasi merah hanya pada promote; replika `needs_repoint` tidak dihitung), `TestGateDisk`, `TestGateAccountSplitsAtLastAt` (`root@localhost` merah; `a@b@localhost` → pengguna `a@b`; `rooter@localhost` hijau; `Root@localhost` hijau), `TestGatePerfSchema`, `TestGateBackup` (tiap status; pada bangun ulang yang dinilai master baru), `TestGateApproval`.
  - `TestGateUnknownIsYellow`: agent tujuan offline → disk dan clock kuning G10a; laporan tanpa `PerfSchema` → G10b; tidak ada yang merah selain versi dan pin.
  - `TestGateClockIgnoresBacklogReport`.
  - `TestGateAccountSkippedForTargetWithoutMySQL`.
  - `TestGatePortProbe` (`gate_port_test.go`): dipanggil sekali per master dalam 10 menit jam palsu; tidak dipanggil bila host panel adalah master atau replika topologi; hasil `true` → baris kuning G11a; tidak pernah merah.
  - `TestGateNeedsBackupAck`: `unset` pada promote terencana dan bangun ulang saja.
  - `TestGateMakesNoAgentCall`: hub palsu yang menggagalkan test pada panggilan apa pun.
- [ ] **Step 2:** `go test ./internal/panel/orchestrator -run TestGate -short`; FAIL.
- [ ] **Step 3:** Tulis `gate.go` dan `gate_port.go`. Halaman master memakai `ReplicaUnverified` sebagai ganti aturan sebarisnya.
- [ ] **Step 4:** Dua gerbang panel, exit 0.
- [ ] **Step 5: Commit:** `feat(panel): one precondition gate that judges rows from stored data`.

### Task 10: Gerbang ditegakkan di `Start*`, `Retry`/`Approve`, dan handler

**Files:**
- Modify: `internal/panel/orchestrator/promote.go:142-283`, `create_replica.go:89-216`, `resync.go:52-97`, `change_databases.go:81-153`, `prepare_master.go:36-64`, `runner.go:624-639,803-918`
- Modify: `internal/panel/web/handlers_mysql.go:21` (`Orchestrator`), `:231-251,653-746,1030-1083`, `handlers_promote.go:244-280`, `handlers_controls.go:206-228`, `handlers_jobs.go:473-486`
- Modify (test fixtures): `internal/panel/orchestrator/fake_test.go`, `internal/panel/web/web_mysql_jobs_test.go` (`fakeOrch`), `internal/panel/e2e_helpers_test.go`
- Test: `orchestrator/gate_enforce_test.go`, `web/web_gate_test.go`

**Interfaces:**
- Consumes: `Runner.Gate`, `GateError`, `ErrGateRed`, `ErrBackupAck` (Task 9).
- Produces:
  - `PromoteParams.BackupAck bool`, `CreateReplicaParams.BackupAck bool` (form `backup_ack=1`).
  - `func gateActionForJob(kind string, st jobState) (GateAction, bool)` — `create_replica` → `GateRebuildOldMaster` bila `st.RebuildOldMaster`, selain itu `GateCreateReplica`; `resync_replica` → `GateResync`; `add_databases` → `GateAddDatabases`; `remove_databases` → `GateRemoveWithDrop`; jenis lain → `false`.
  - `web.Orchestrator` bertambah `Gate(orchestrator.GateInput) (orchestrator.GateResult, error)`.
  - Pembantu test `greenGate(t, e, serverIDs ...int64)` di `fake_test.go` (versi agent sama dengan panel, laporan segar berpin `master` untuk master, sertifikat master, replika terverifikasi, akun `dbpanel_admin@localhost`, `PerfSchema` benar, jam selaras) dan padanannya di test web dan e2e, supaya test yang sudah ada tetap berangkat dari gerbang hijau.

Aturan:
- **`Start*`**: di bawah `startMu`, sesudah semua pemeriksaan yang ada dan sebelum tulisan pertama ke store, panggil `Gate`; baris merah → `*GateError`. `StartPromote` hanya untuk mode terencana. `StartRemoveDatabases` hanya bila `dropData`. `StartCreateReplica` bangun ulang mengisi `Approval` dengan hasil `requireRebuildApproval` yang sudah dijalankannya.
- **Centang cadangan**: `StartPromote` terencana dan `StartCreateReplica` bangun ulang mengembalikan `ErrBackupAck` bila `NeedsBackupAck()` dan `!BackupAck`.
- **`Retry`/`Approve`**: bila `dropsData(kind, st)` benar dan `gateActionForJob` mengenal jenisnya, nilai gerbang sebelum `confirmDrop`; merah → `*GateError`. Pekerjaan promote tidak pernah dinilai (K4).
- **Handler**: setiap handler tindakan yang digerbang memanggil `Gate` **sebelum** `requireConfirm`. Merah → halaman yang sama dengan status 409 dan teks **G14** `Tindakan ini ditolak karena ada prasyarat yang belum terpenuhi: %s. Cara memperbaikinya ada di daftar Prasyarat di halaman ini.` (label dipisah `, `); token tidak dipakai; diaudit dengan hasil G0. `confirmedJobAction` melakukannya bila `DropsData`.
- Centang yang hilang: 400 dengan **G15** `Centang "Saya sudah memastikan ada cadangan (backup) terbaru" untuk melanjutkan.`
- Audit promote terencana dan bangun ulang memuat `backup_ack` bila centang dipakai.

- [ ] **Step 1: Test gagal.**
  - `TestStartRefusesOnRedRow`: tabel tindakan × satu baris merah yang berlaku baginya → `errors.Is(err, ErrGateRed)`, tidak ada pekerjaan dan tidak ada baris replika yang dibuat, tidak ada panggilan agent sesudah pemeriksaan yang lama.
  - `TestStartPassesWithYellowRows`.
  - `TestPlannedPromoteNeedsBackupAck`, `TestRebuildNeedsBackupAck`; dengan folder terisi dan `ok`, centang tidak diminta.
  - `TestRetryGatedOnlyWhenDataIsDropped`: `Retry` resync dengan disk merah ditolak; `Retry` pekerjaan promote yang gagal dengan baris yang sama merah berjalan; `Approve` bangun ulang dengan persetujuan hilang ditolak seperti sekarang.
  - `TestRemoveDatabasesWithoutDropNotGated`.
  - `TestGateRefusesBeforeTokenIsConsumed` (web): POST resync dengan token sah selagi merah → 409 G14; sesudah baris hijau, token yang sama diterima.
  - `TestGateRefusalIsAudited`.
- [ ] **Step 2:** Jalankan; FAIL.
- [ ] **Step 3:** Implementasi menurut aturan di atas; `greenGate` dipasang di pembantu lingkungan test sehingga test lama tidak diubah satu per satu.
- [ ] **Step 4:** Dua gerbang panel dan `make test-e2e`, exit 0.
- [ ] **Step 5: Commit:** `feat(panel): refuse planned actions while a precondition row is red`.

### Task 11: Daftar prasyarat di layar

**Files:**
- Create: `internal/panel/web/handlers_gate.go`, `web/templates/_gate.html`
- Modify: `internal/panel/web/render.go` (daftar template), `handlers_promote.go:180-217,400-512`, `handlers_mysql.go:533-630` dan model halaman replika/database, `handlers_servers.go:123-183`; template `promote.html`, `replica_new.html`, `server_detail.html:271,304`, `replica_detail.html:197`, `replica_databases.html:110`
- Test: `internal/panel/web/web_gate_test.go`

**Interfaces:**
- Consumes: `Orchestrator.Gate`, `GateResult`.
- Produces: `type gateView struct { Rows []gateRowView; Red, NeedsAck, Emergency, OnlyRed bool }`; `func newGateView(g orchestrator.GateResult, emergency, onlyRed bool) gateView`; template `{{template "gate-list" .Gate}}`.

Teks:
- **D1** judul: `Prasyarat`. Lencana status (kata, bukan hanya warna): `Terpenuhi`, `Perlu perhatian`, `Belum terpenuhi`.
- **D2** di bawah tombol yang mati: `Tombol ini mati selama ada prasyarat yang belum terpenuhi. Perbaiki baris bertanda "Belum terpenuhi", lalu muat ulang halaman ini.`
- **D3** centang: `Saya sudah memastikan ada cadangan (backup) terbaru`.
- **D4** di bawah baris cadangan: `Panel hanya melihat umur dan ukuran berkas, bukan apakah cadangan itu bisa dipulihkan.`

Aturan tampil:
- Layar promote dan form buat replika/bangun ulang: daftar lengkap (hijau, kuning, merah). Tiap baris bukan hijau menampilkan `Detail` lalu `Fix`. Baris merah → tombol kirim `disabled` dan D2.
- Resync (halaman replika), Siapkan master dan Siapkan master lagi (halaman server), Tambah database dan Lepas database dengan hapus data (halaman database replika): daftar hanya tampil bila ada baris merah (`OnlyRed`), berisi baris merah saja; tombolnya `disabled` dan D2. Pada "Lepas database", yang mati adalah kotak centang hapus data, bukan tombol lepas.
- Pada bangun ulang, baris `approval` memakai jawaban yang sudah dibaca kotak "Persetujuan di server"; GET tidak menambah panggilan agent.
- Centang D3 tampil (wajib, `required`) pada promote terencana dan bangun ulang bila `NeedsAck`.

- [ ] **Step 1: Test gagal.** `TestPromoteScreenListsEveryRow`; `TestCreateReplicaFormListsRows`; `TestRedRowDisablesButton` (lima tombol; tiap halaman memuat `disabled` pada tombolnya, D2, `Detail` dan `Fix` baris merah, dan tidak memuat `data-confirm-action` yang aktif untuk tombol itu); `TestGreenGateShowsNothingOnModalOnlyButtons`; `TestBackupAckCheckboxShownOnlyWhenNeeded`; `TestGateListEscapesServerNames` (nama server dengan `<` tampil ter-escape).
- [ ] **Step 2:** Jalankan; FAIL.
- [ ] **Step 3:** Implementasi; `bash scripts/tailwind.sh`.
- [ ] **Step 4:** Dua gerbang panel, exit 0.
- [ ] **Step 5: Commit:** `feat(panel): show the precondition list and disable gated buttons while a row is red`.

### Task 12: Promote darurat: centang wajib, baris ditampilkan, audit

**Files:**
- Modify: `internal/panel/orchestrator/promote.go:142-283` (`PromoteParams`), `internal/panel/web/handlers_promote.go:58-80,244-340`, `web/templates/promote.html:46-61,176-186`, `internal/panel/e2e_helpers_test.go` (`startPromote`)
- Test: `orchestrator/promote_test.go`, `web/web_promote_test.go`

**Interfaces:**
- Consumes: `Gate` dengan `GatePromoteEmergency`.
- Produces: `PromoteParams.OldMasterDeadAck bool` (form `old_master_dead=1`); `var ErrOldMasterAck = errors.New("centang master lama mati belum diisi")`; field audit `old_master_dead` (bool), `master_last_report` (RFC 3339 UTC, `""` bila tidak ada), `skipped_red` (daftar `Key`).

Teks:
- **D5** centang (spek 5.9, persis): `Saya sudah memastikan master lama mati atau terputus dari aplikasi`
- **D6** di atas daftar pada mode darurat: `Baris yang belum terpenuhi tidak menghalangi promote darurat.`
- **D7** penolakan: `Centang "Saya sudah memastikan master lama mati atau terputus dari aplikasi" untuk melanjutkan promote darurat.`

- [ ] **Step 1: Test gagal.**
  - `TestEmergencyPromoteNeedsDeadMasterTick`: tanpa centang → `checkPromote` menolak dengan D7 sebelum token dipakai; `StartPromote` darurat tanpa `OldMasterDeadAck` → `ErrOldMasterAck`. Mode terencana tidak meminta centang itu.
  - `TestEmergencyPromoteStartsWithEveryRowRed`: versi agent master beda, pin hilang, jam 5 menit, tanpa sertifikat, akun `root`, cadangan usang → pekerjaan promote dibuat; tombol di layar tidak `disabled`; D6 tampil.
  - `TestEmergencyPromoteAuditFields`: entri audit memuat `old_master_dead: true`, `master_last_report` sama dengan `View.LastReportAt`, dan `skipped_red` berisi tepat kunci baris merah saat itu, terurut seperti daftar.
  - `TestEmergencyPromoteCandidateRulesUnchanged` (K1, K2): kandidat ber-agent beda versi tetap tidak bisa dipilih; pekerjaan topologi yang belum selesai tetap menolak dengan `msgPromoteJobOpen`.
- [ ] **Step 2:** Jalankan; FAIL.
- [ ] **Step 3:** Implementasi. Nama master lama tetap diketik seperti sekarang.
- [ ] **Step 4:** Dua gerbang panel dan `make test-e2e`, exit 0.
- [ ] **Step 5: Commit:** `feat(panel): emergency promote asks for the dead-master tick and audits skipped rows`.

### Task 13: Halaman Panduan, bagian README yang sama, dan tautan dari layar

**Files:**
- Create: `internal/panel/web/handlers_guide.go`, `web/templates/panduan.html`, `internal/panel/web/guide_readme_test.go`
- Modify: `internal/panel/web/router.go`, `render.go:55-137`, `web/templates/layout.html:32-38`, `internal/panel/web/web_layout_test.go:39-80`; `promote.html`, `replica_new.html`, `cutover.html`, `_gate.html`; `README.md` (bagian baru `## Panduan` sebelum `## Catatan audit`)

**Interfaces:**
- Produces: rute `GET /panduan` (butuh login, `authed`); entri menu kedelapan **`Panduan`** (`data-menu="panduan"`, sesudah Pengaturan); jangkar per panduan (kolom "id").

| # | Judul (`<h2>` di halaman, `###` di README) | id | Sumber isi |
|---|---|---|---|
| 1 | `Promote darurat` | `promote-darurat` | README "Promote terencana dan darurat", "Layar pertimbangan"; Task 12 |
| 2 | `Bangun ulang master lama` | `bangun-ulang-master-lama` | README "Master lama": Periksa data tertinggal, Amankan, alihkan aplikasi, `approve`, bangun ulang |
| 3 | `Memindahkan jadwal cadangan sesudah promote` | `jadwal-cadangan` | spek 5.8; Task 8 |
| 4 | `Memperpanjang sertifikat` | `perpanjang-sertifikat` | README "Sertifikat MySQL" |
| 5 | `Melepas kunci master lama dengan tangan` | `lepas-kunci-dengan-tangan` | README "Melepas kunci start dengan tangan" (teksnya sudah diaudit; langkah dan perintah disalin, tidak ditulis ulang) |
| 6 | `Master terkunci padahal tidak ada promote` | `master-terkunci-tanpa-promote` | README "Peran dan sumber yang dipin" (`set-role master`) |
| 7 | `Memakai lagi server yang dihapus saat masih dikarantina` | `pakai-lagi-server-dikarantina` | README bagian berjudul sama |
| 8 | `Memperbaiki baris merah` | `baris-merah` | Task 9: satu sub-bagian per baris, dengan `Detail` dan `Fix`-nya; akun MySQL khusus panel dengan perintah SQL lengkap |
| 9 | `Replika yang tersambung di luar alur panel` | `replika-di-luar-panel` | spek 5a bagian 5.3 (catatan tentang replika yang tersambung sesaat sebelum master mati) |

Aturan isi:
- Tiap panduan: satu kalimat kapan dipakai, lalu langkah bernomor; setiap perintah di blok sendiri (`<pre class="perintah">` di halaman, blok berpagar di README).
- Panduan 8, akun khusus: pernyataan SQL diambil dari yang dikirim `mysql.ResetAdmin` (`install.go:567-601`) untuk membuat `dbpanel_admin@localhost`, dijalankan dulu pada mysqld coba, lalu langkah "Hubungkan MySQL" dengan akun itu dan pemeriksaan bahwa baris "Akun panel" hijau.
- Panduan 5, 6 dan 7 tidak menambah langkah yang tidak ada di bagian README asalnya.
- Tautan: layar promote darurat → `#promote-darurat`; form bangun ulang → `#bangun-ulang-master-lama`; G6f → `#baris-merah`; layar pengalihan menambah **D8** `Pindahkan jadwal cadangan (backup) ke master baru, lalu isi folder cadangan di halaman servernya.` dengan tautan `#jadwal-cadangan`.

- [ ] **Step 1: Test gagal.**
  - `TestGuidePageNeedsLogin`; `TestGuidePageHasNineGuides` (sembilan `<h2>` dengan judul dan id di tabel, berurutan).
  - `TestLayoutSidebarAndActiveMenu` diperbarui ke delapan entri; `/panduan` menandai menu Panduan aktif.
  - `TestGuideMatchesREADME` (`guide_readme_test.go`, membaca `../../../README.md` dan template): (a) bagian `## Panduan` README memuat sembilan `###` dengan judul yang sama dan urutan yang sama; (b) untuk tiap panduan, daftar perintah di halaman (isi tiap `<pre class="perintah">`, sesudah `html.UnescapeString`) sama persis dan berurutan dengan daftar isi blok berpagar di bagian README-nya.
  - `TestScreensLinkToGuides`: empat tautan di atas ada dan jangkarnya ada di halaman Panduan.
- [ ] **Step 2:** Jalankan; FAIL.
- [ ] **Step 3:** Tulis halaman dan bagian README. Jalankan setiap perintah yang bisa dijalankan; catat yang tidak.
- [ ] **Step 4:** `bash scripts/tailwind.sh`; dua gerbang panel, exit 0.
- [ ] **Step 5: Commit:** `feat(panel): guide page with nine step-by-step guides, mirrored in the README`.

### Task 14: README, test ujung-ke-ujung, dan daftar teks untuk pemilik

**Files:**
- Modify: `README.md` ("Pemantauan", "Promote", "Master lama", "Pembaruan", "Keamanan"); `internal/panel/e2e_gate_test.go` (baru); bagian "Teks baru untuk dibaca pemilik" di rencana ini

**Interfaces:**
- Consumes: semua task sebelumnya; pembantu e2e (`startE2EPromoteTopology`, `uiClient`, `openAlert`).

README menambah:
- "Prasyarat": tiap baris, arti tiga status, bahwa tindakan terencana ditolak selagi ada baris "Belum terpenuhi", bahwa promote darurat tidak, dan bahwa tidak ada tombol untuk melewati gerbang.
- "Cadangan (backup)": dua isian, apa yang dilihat agent dan apa yang tidak, folder yang ditolak dan sebabnya, peringatan "Cadangan master usang".
- "Pembaruan": sesudah memperbarui ke versi ini, sebelum promote terencana berikutnya: jalankan "Wajibkan pemeriksaan sertifikat TLS", ganti akun panel bila masih `root`, dan perbarui semua agent.
- "Keamanan", risiko yang tersisa: panel yang dibobol bisa menunjuk agent ke folder lain dan mengetahui umur serta ukuran berkas terbaru di sana (paling dalam tiga tingkat); gerbang menahan salah langkah, bukan panel yang dibobol; baris yang salah merah menahan pekerjaan terencana sampai diperbaiki; penerusan log audit hanya tanda dengan tangan (K6); keputusan K1 dan K2.

- [ ] **Step 1: Test e2e gagal** (`skipWithoutE2E`):
  - `TestE2EGateRefusesPlannedPromoteThenAllows`: folder cadangan master diisi lewat store dengan `/nonexistent-dbpanel-e2e` → agent melaporkan `unreadable/missing`; layar promote menampilkan G8d dan tombolnya mati; POST promote terencana dengan token sah → 409 G14 dan token masih berlaku; peringatan `backup_stale` terbuka. Folder dikosongkan → baris kuning G8u, promote tanpa centang ditolak G15, dengan centang berjalan sampai selesai; audit memuat `backup_ack`.
  - `TestE2EEmergencyPromoteSkipsRedRows`: baris cadangan merah seperti di atas, master dimatikan; promote darurat tanpa centang D5 ditolak D7; dengan centang selesai; audit memuat `skipped_red` yang berisi `backup`.
  - `TestE2EBackupFolderReported`: folder sungguhan (dibuat test di luar awalan yang ditolak, dihapus saat selesai) berisi satu berkas → halaman server menampilkan B7 `Berkas terbaru:`; berkas dijadikan 0 byte → G8b sesudah pemindaian berikutnya.
- [ ] **Step 2:** `make test-e2e`; tiga test baru FAIL atau, bila sudah lulus karena task sebelumnya, catat itu di laporan.
- [ ] **Step 3:** Betulkan apa pun yang ditemukan test itu di kode task sebelumnya dengan commit `fix(...)` tersendiri; tulis bagian README; isi daftar teks.
- [ ] **Step 4:** Empat gerbang (agent, panel, `make test`, `make test-e2e`), exit 0.
- [ ] **Step 5: Commit:** `test(e2e): the gate refuses planned work and lets an emergency promote through; docs for Plan 5b-2b`.

---

## Sesudah Task 14

1. **Tinjauan akhir seluruh cabang** seperti biasa (dua peninjau: sisi agent dan sisi panel), satu gelombang perbaikan, satu pemeriksaan ulang terarah.
2. **Audit keamanan menyeluruh**, sesudah gelombang perbaikan itu, dengan model terkuat yang tersedia. Bukan audit atas diff saja: yang dibaca adalah mode ketat seperti adanya sekarang (hasil 5b-1, 5b-2a dan rencana ini).

### Audit keamanan

**Tiga pemeriksa, berjalan bersamaan, hanya membaca** (tidak menjalankan test yang menyalakan MySQL; `graphify query` lebih dulu):

- **Agent.** `strict.go`, `pins.go`, `rebuild_approval.go`, semua `cli*.go`, `startlock*.go`, `quarantine.go`, `lift.go`, `rebuild_load.go`, `dump.go`, `snapshot.go`, `clone.go`, `leftover.go`, `salvage.go`, `svcuid.go`, `connect.go`, `internal/agent/monitor` (termasuk `backup.go`), dan unit systemd di `scripts/install.sh`.
- **Panel: orchestrator, monitor, store.** `approval.go`, `create_replica.go`, `runner.go`, `promote*.go`, `oldmaster.go`, `controls.go`, `tls.go`, `gate*.go`; `monitor/{promote,start_state,alerts,state,monitor}.go`; migrasi 0012–0013; `redact`.
- **Web, menurut OWASP.** Tiap kategori OWASP Top 10 (2021) dinilai satu per satu dengan bukti berkas dan baris: kontrol akses di setiap rute (tabel rute: butuh login? CSRF? konfirmasi kata sandi? diaudit?), kegagalan kriptografi, injeksi (SQL, perintah, template, header, log), desain tidak aman, salah konfigurasi keamanan, komponen rentan (`govulncheck ./...` bila bisa dijalankan; bila tidak, dicatat tidak dijalankan), identifikasi dan autentikasi (sesi, kuki, pembatas laju, token konfirmasi), integritas perangkat lunak dan data (rantai audit, rilis, `SHA256SUMS`), pencatatan dan pemantauan, dan SSRF (pemeriksaan port master, alamat agent). **Header keamanan diperiksa dengan test, bukan hanya dibaca:** `Content-Security-Policy` (tanpa `unsafe-inline`/`unsafe-eval`), `Strict-Transport-Security`, `X-Content-Type-Options`, `frame-ancestors`/`X-Frame-Options`, `Referrer-Policy`, `Permissions-Policy`, `Cross-Origin-Opener-Policy`, `Cross-Origin-Resource-Policy`, `Cache-Control` pada halaman yang butuh login, dan atribut kuki (`Secure`, `HttpOnly`, `SameSite`); header yang kurang menjadi temuan. Juga: setiap tempat teks agent tampil, dan langkah README/Panduan yang dijalankan dengan tangan, dibaca sebagai operator yang mengikutinya apa adanya.

**Penyerang yang dipertimbangkan:** yang sudah ada di audit 5b-1 bagian 2 (panel yang dibobol; agent atau server basis data yang dibobol; pengguna OS `mysql`; pemegang SUPER di master lama; pengendali DNS; admin yang dijebak tautan lintas situs; operator yang terburu-buru; test suite sebagai root), ditambah: panel yang dibobol memilih folder cadangan; orang yang bisa menulis ke folder cadangan; host panel yang memeriksa port master.

**Pertanyaan yang harus dijawab satu per satu:** butir K9 rencana ini; daftar "Declined to judge" kedua tinjauan akhir 5b-2a; risiko di spek 5b-2a bagian 8 dan 11; SEC-204, SEC-212, SEC-213, SEC-214; keputusan K1, K2, K6; apakah `Gate` punya jalan samping (tindakan yang membuang data tetapi tidak melewatinya); apakah sebuah baris bisa dibuat hijau oleh agent yang dibobol sehingga tindakan yang seharusnya tertahan lolos, dan apa akibat terburuknya.

**Keluaran:**
- `docs/superpowers/security/<tanggal>-audit-plan-5b2.md`, dengan susunan audit 5b-1: ringkasan untuk keputusan merge, lingkup dan metode, model ancaman delta, tabel temuan (nomor mulai **SEC-235**), risiko sisa, dan "yang tidak dikerjakan". Temuan ditulis apa adanya dari pemeriksa; tingkatnya Kritis, Tinggi, Sedang, Rendah, Info.
- `docs/superpowers/security/2026-11-10-inventaris-risiko.md` diperbarui: R-046 dan baris tabel prasyarat dipindah ke "Sudah ditangani" dengan commit-nya; risiko baru mulai **R-181**.
- Laporan singkat untuk pemilik dalam bahasa Indonesia: apa yang ditemukan, apa yang diperbaiki, apa yang tersisa beserta akibat terburuknya.

**Aturan sesudah audit:**
- Temuan Kritis dan Tinggi diperbaiki sebelum merge dalam **satu** gelombang perbaikan, lalu diperiksa ulang oleh pemeriksa yang menemukannya, terbatas pada diff perbaikan itu. Tidak ada audit penuh kedua; itu dicatat di "yang tidak dikerjakan".
- Temuan Sedang ke bawah dibawa ke pemilik dengan saran (perbaiki sekarang, rencana berikutnya, atau terima), tidak ada yang dibuang diam-diam.
- Temuan yang hanya bisa ditutup dengan mengubah keputusan spek yang sudah disetujui: berhenti dan tanyakan pemilik.
- Merge ke `main` hanya atas persetujuan pemilik sesudah laporan itu.

## Perubahan selama pelaksanaan

(Diisi selama pelaksanaan, bernomor, dengan tanggal dan alasan.)

1. **2026-11-22 — batas waktu `make test` naik dari 60 ke 120 menit (Task 1).**
   Paket driver MySQL sisi agent sudah memakan 59 dari 60 menit di mesin
   pengembangan, sehingga satu putaran lambat akan menggagalkan pohon yang
   benar. `Makefile` dan baris README-nya diubah; gerbang 1 dan 2 dijalankan
   dengan `-timeout 120m`.
2. **2026-11-22 — pengujian penghenti paksa di harness menunggu berkas penanda,
   bukan jeda tetap (Task 1).** Temuan kecil peninjau; diperbaiki langsung
   karena kegagalan akibat waktu di rangkaian ini memakan hingga satu jam.
3. **2026-11-22 — P-M4 berubah: master baru tetap tidak bisa dihapus selama
   baris master lamanya masih ada, tetapi dengan pesannya sendiri (Task 2).**
   Rencana semula membolehkan penghapusan itu. Pelaksana membuktikan dua
   akibatnya: peran master lama di panel berubah dari `quarantined` menjadi
   `unassigned` (kotak master lama dan peringatan start-nya hilang, server itu
   ditawarkan sebagai target replika), dan topologi bisa terhapus saat bangun
   ulang master lama masih berjalan. Gantinya: galat baru
   `ErrMasterHasOldMaster` dengan teks **F8** `server ini masih menjadi master
   dan master lamanya masih tercatat di topologi ini; selesaikan bangun ulang
   master lama itu atau hapus master lama itu dari panel dulu`. Pengujiannya
   bernama `TestDeleteNewMasterRefusedNamesOldMasterRow`.
4. **2026-11-22 — `promoteStartRefused` tidak diubah (Task 2).** Jawaban agent
   berkode `timeout` atau `offline` atas perintah mulai promote tetap dibaca
   "tidak ada jawaban" (sisi hati-hati: perintah itu mungkin sudah membuka
   penulisan). Hanya langkah karantina dan arahkan-ulang yang membedakan
   `offline` kiriman agent dari sambungan yang benar-benar putus.
5. **2026-11-22 — arti "karantina tidak dicatat tertunda" di pengujian terakhir
   Task 2.** Langkah karantina gagal (bukan dilewati sebagai tertunda);
   karantina promote itu tetap tercatat tertunda supaya dikirim lagi saat agent
   tersambung.
6. **2026-11-22 — arti "paling panjang 300 karakter" (Task 3).** Batasnya
   batas `monitor.AgentText(s, 300)` yang sudah dipakai di tempat lain: 300
   karakter bukan-spasi, tiap deret spasi menjadi satu spasi, jadi paling
   banyak sekitar 600 karakter dan satu baris. Satu aturan untuk semua halaman
   dan Telegram.
7. **2026-11-22 — "Sumber dipin" di halaman server ikut dibatasi (Task 3).**
   Tidak ada di rencana; temuan peninjau. Nilai yang berbentuk host:port
   menurut aturan agent sendiri (`^[A-Za-z0-9_.:-]{1,255}$`, `:` hanya di
   alamat IP, port 1-65535) tampil apa adanya; nilai lain tampil sebagai
   `tidak dikenal (` + label agent + nilai yang dipotong 32 karakter + `)`.
8. **2026-11-22 — tanda kutip untuk nama baru di dokumen keamanan (Task 4).**
   Di inventaris risiko dan audit 5a, nama "Wajibkan pemeriksaan sertifikat
   TLS" diberi tanda kutip di tempat ia berdiri tanpa tanda di tengah kalimat
   (nama lama satu kata, nama baru sebuah frasa kerja). Penyebutan pertama di
   README diberi `(TLS certificate verification)`.
9. **2026-11-22 — cara membuka folder cadangan diperketat (Task 6).**
   Rencana menulis `os.OpenRoot(folder)` lalu `fs.WalkDir(root.FS(), …)`.
   Yang dibangun: folder dibuka dengan `O_DIRECTORY`, akar dibuka lewat
   `/proc/self/fd/N`, dan tiap subdirektori dibuka satu komponen demi satu
   komponen dari deskriptor folder itu dengan `O_NOFOLLOW`. Alasannya:
   `root.FS()` membaca seluruh isi direktori ke memori tanpa batas, bisa
   macet pada FIFO yang ditukar masuk, dan mengikuti symlink di dalam folder
   bila sebuah direktori ditukar dengan symlink sesudah didaftar (temuan
   peninjau). Aturan rencana tidak berubah: tidak ada symlink di dalam folder
   yang diikuti, tidak ada berkas yang dibuka, semua batas tetap.
10. **2026-11-22 — folder cadangan yang berupa symlink tetap dipindai
    (Task 6).** Tidak diatur rencana. Bila path folder adalah, atau melewati,
    sebuah symlink (misalnya `/backup` yang menunjuk ke disk lain), agent
    memindai tempat yang dituju, asalkan tempat itu lolos aturan folder yang
    sama: tujuan di `/proc`, `/sys`, `/dev` ditolak sebagai path tidak sah,
    tujuan di `/home`, `/root`, `/run/user`, `/tmp`, `/var/tmp` sebagai tidak
    terlihat. Tanpa ini operator membaca "itu bukan folder" untuk folder yang
    sah. Biayanya: orang yang bisa menaruh symlink di path itu (perlu izin
    tulis di folder induknya) membuat agent melaporkan umur dan ukuran berkas
    terbaru di direktori lain; hanya dua angka, tanpa nama dan tanpa isi.
    Untuk ditinjau di audit keamanan.

## Teks baru untuk dibaca pemilik

Diisi Task 14: setiap teks operator yang baru, berubah atau hilang antara
`main` (`78b1bb9`) dan akhir rencana ini, dikelompokkan menurut tempat
tampilnya, ditulis seperti di kode. Id (F…, B…, G…, D…) mengikuti rencana
ini; teks yang ditambahkan pelaksana di luar daftar itu diberi tanda
"tidak ada di rencana".
