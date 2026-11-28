# Rencana Implementasi 5b-3a: Sisa Audit 5b-2, Bagian A

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Menutup bagian yang berbahaya dari 25 baris risiko yang
ditinggalkan audit Rencana 5b-2, sehingga bisa di-merge sendiri: login salah
dari luar tidak memblokir admin; "Ulangi" promote tidak melewati pemeriksaan
saat mulai dan master tidak pernah tertinggal baca-saja tanpa jalan keluar
yang disebut panel; tindakan ringan meminta kata sandi sekali per 10 menit;
karantina tertunda mendarat sendiri; master yang dibuka tetap bisa ditulis
sesudah restart; perintah root yang berbahaya bertanya dulu; persetujuan
bangun ulang berakhir bersama percobaannya; teks Lepas replika tidak lagi
memberi SQL pembuka tanpa syarat.

**Architecture:** Panel memisahkan tiga hal yang selama ini satu tombol
"Ulangi": membuka kunci promote (tindakan sendiri, tanpa kata sandi, dijaga
pemeriksaan agent), menjalankan lagi promote yang belum dikirim (semua syarat
saat mulai), dan menuntaskan promote yang sudah mengganti master (tidak
pernah ditahan). Keadaan di antaranya (terkirim, hasil belum diketahui)
diputuskan dari bukti: tanda "mulai tidak berjawab" tersimpan di keadaan
pekerjaan, field baru opsional di `mysql.status`, atau batas waktu. Kata
sandi tindakan ringan memakai jendela 10 menit pada catatan sesi di memori
panel; satu-satunya pintu tetap token sekali pakai (`requireConfirm`). Di
agent, satu pasang fungsi menyimpan kedua bendera baca-saja di setiap jalur
kunci dan buka, sesudah keadaan restart diukur lebih dulu dengan mysqld
sungguhan.

**Tech Stack:** Go 1.26, `CGO_ENABLED=0`, tanpa dependensi baru; SQLite
(modernc); html/template + Tailwind v4/daisyUI v5 (`bash
scripts/tailwind.sh`); Alpine build CSP; harness
`internal/agent/drivers/mysql/testmysql` (hanya MySQL 8.0.46); e2e
`internal/panel/e2e_*_test.go`; harness browser Chromium di balik tag
`e2e_browser` (`internal/panel/e2e_browser_test.go`,
`scripts/e2e_browser_confirm.js`).

**Spec:** `docs/superpowers/specs/2026-11-27-rencana-5b-3-sisa-audit-5b2-design.md`
(disetujui pemilik 2026-11-27). Rencana ini melaksanakan **bagian A** menurut
bagian 6 spek ("Urutan dan dua bagian", butir 1 sampai 10); bagian B
direncanakan sesudah bagian A dibangun. Yang mengikat: bagian 2 (anti-tujuan),
5.1 sampai 5.5, 5.7 (R-198 dan fungsi "laporan segar"), 5.9 (R-190), 5.11
(teks), 5.12, 5.13, 6 dan 9. Kode diperiksa pada `main` `8caee75` (versi
0.2.0); nomor baris di rencana ini berasal dari pembacaan pada commit itu,
dan pelaksana memeriksa ulang letaknya sebelum mengubah.

## Global Constraints

- Go 1.26, `CGO_ENABLED=0`, tanpa dependensi baru.
- Bahasa: teks untuk operator (UI, keluaran CLI, pesan galat yang tampil ke
  operator, README, Panduan) berbahasa Indonesia; kode, komentar, nama test
  dan pesan commit berbahasa Inggris; log agent dan panel (slog) berbahasa
  Inggris.
- **Model kepercayaan, dua baris.** Panel dan sesi admin bisa dibobol atau
  dicuri: agent root di tiap server adalah penjaga terakhir datanya (pin
  peran dan sumber, kunci promote, mode ketat, persetujuan root), dan teks
  dari agent tidak dipercaya panel (dibatasi, diberi label, di-escape). Root
  di server basis data, dan admin yang mengetik kata sandi panelnya,
  dipercaya.
- **Anti-tujuan (spek bagian 2); bila salah satunya terjadi, rencana gagal
  walau test lulus:**
  - master tertinggal baca-saja tanpa tombol di panel yang bisa membukanya
    atau menyebut perintah root yang membukanya. Hanya satu keadaan memakai
    perintah root sebagai jalan keluar: promote yang perintahnya sudah
    terkirim dan kandidatnya tidak pernah menjawab lagi, selagi master masih
    terkunci (T14t), dan bentuk majemuknya (T14u);
  - perbaikan yang membuat dua master lebih mungkin: gerbang atau kata sandi
    yang menahan promote setengah jalan, atau buka kunci yang lebih longgar
    dari pemeriksaan agent sekarang;
  - permintaan kata sandi yang bisa diblokir lalu lintas dari luar;
  - langkah baru di jalur gangguan yang tidak ada di tabel "Yang dikerjakan
    operator lebih dari sekarang" spek;
  - agent atau panel baru yang membuat pasangan 0.2.0-nya atau keadaan
    tersimpan berhenti bekerja tanpa langkah pembaruan yang disebut (5.12).
- **Promote darurat tidak pernah ditolak oleh sebuah baris Prasyarat**, juga
  tidak oleh baris yang diubah rencana ini (disk, cadangan). Sesudah titik
  tak kembali (`PastPoint`) "Ulangi" promote tidak pernah diberi gerbang
  atau kata sandi, kedua jenis.
- **Protokol.** Field baru semuanya opsional (`omitempty`); agent 0.2.0
  tetap bekerja dengan panel baru dan panel 0.2.0 dengan agent baru (5.12).
  `proto.HealthSnapshot` harus tetap bisa dibandingkan dengan `==` (skalar,
  string, atau pointer; tidak ada slice atau map langsung di struct itu).
  Aturan "Versi agent" yang ada tidak diubah.
- **Teks ber-id** (T…, A…, C…, B… dari spek 5.11) dipakai persis, huruf demi
  huruf, disalin dari spek, bukan dari rencana ini. `%s`, `%d`, `%v` dan
  `<…>` adalah tempat nilai. Teks yang ditambahkan rencana ini diberi id
  P… dan tanda "tidak ada di spek"; pelaksana yang menambah teks lain
  menandainya begitu juga di daftar teks untuk pemilik.
- Teks agent yang ditampilkan panel selalu berlabel dan dibatasi
  (`maxAgentWords`, `agentText`, `agentErrText`) seperti di tempat lain;
  panel tidak pernah memilih cabang dari isi teks agent, kecuali kalimat
  "tidak dikunci untuk promosi" yang sudah dibaca `promoteUndo` hari ini.
- Nilai tetap dari spek:
  - konfirmasi: paling banyak **5** kata sandi salah per **1 menit** dan
    **20** per **15 menit**, per sesi;
  - login ber-cookie perangkat: **5** salah per **15 menit** per pasangan
    cookie dan alamat; **20** salah per **15 menit** per cookie untuk semua
    alamat; **2** slot pemeriksaan sendiri; cookie berlaku **1 tahun**;
    batas per alamat memakai awalan **/64** untuk IPv6;
  - jendela kata sandi **10 menit**, tidak diperpanjang dengan dipakai;
    token konfirmasi tetap **2 menit**;
  - token pasang ulang agent **15 menit** (server belum ter-pairing tetap 24
    jam); `hostname` agent dipotong **255 rune**, satu baris;
  - pemeriksaan port master: paling banyak sekali per master per **10
    menit**, apa pun alamatnya;
  - baris "Ruang disk" merah pada ≥ ambang kritis atau ≥ **95%**, mana yang
    lebih rendah;
  - "Ulangi" promote terencana sebelum kirim: paling lama **24 jam** sejak
    konfirmasi terakhir (`confirmMaxAge`);
  - pembacaan langsung replika lain: satu `mysql.status`, paling lama **10
    detik**, sejajar;
  - putaran karantina tertunda: tiap **60 detik**, melambat sampai **5
    menit**; penolakan tidak pernah menghentikannya;
  - kunci promote: `promoteLockSec` 1800 detik, `lockCallSlack` 5 menit,
    `promoteClockSkew` 5 menit, `proto.PromoteLockGrace` 60 detik (nilai
    yang ada; tidak diubah).
- **Perintah di README dan Panduan.** Setiap perintah yang ditulis ke README
  atau Panduan dijalankan dulu oleh penulisnya pada mysqld coba di direktori
  sementara, dan setiap teks yang dikutip disalin dari kode. Yang tidak bisa
  dijalankan di mesin pelaksana (`systemctl`, host kedua, MySQL 8.4/9.x)
  disebut satu per satu di laporan task sebagai "tidak dijalankan".
- Tidak pernah `pkill`/`killall` mysqld. Satu penulis dan satu proses test
  pada satu waktu.
- **Gerbang tiap task**, satu per satu, semua exit 0, dengan `-timeout 120m`:
  - task yang menyentuh `internal/agent` atau `internal/proto` (Task 7, 8,
    15, 16, 17, 19, 20, 21, 22):
    `go test ./internal/agent/... ./internal/proto/... -race -short -timeout 120m`
    (sekitar 60 menit), lalu
    `go test ./internal/panel/... -race -short -timeout 120m`, lalu
    `make test`;
  - task panel saja: dua yang terakhir;
  - Task 3, 6, 8, 9, 10, 12, 14, 16, 20, 22 dan 24 menambah `make test-e2e`.
- Bila port pembantu test MySQL ("Address already in use") atau
  `TestBackupScanOneAtATime` menggagalkan sebuah gerbang, test itu dijalankan
  ulang sekali dan kejadiannya dicatat di "Perubahan selama pelaksanaan".
  Bila terjadi lagi, pengendali memajukan "Task bersyarat P" (di akhir daftar
  task) ke depan task berikutnya (spek bagian 6).
- Eksplorasi kode: `graphify query "<pertanyaan>"` sebelum grep atau membaca
  berkas sumber; `graphify update .` sesudah mengubah kode.
- Commit kecil per langkah; tidak ada amend dan tidak ada force-push.
  Trailer commit diberikan pengendali saat task dikirim, tidak ditulis di
  rencana ini. Nama model, id agent, tautan sesi dan path sementara tidak
  pernah ditulis ke berkas repo.

## Review Focus

1. **Panel restart di tengah tiap keadaan promote** (terkunci belum dikirim;
   terkirim tanpa jawaban; sesudah titik tak kembali). Yang diharapkan:
   tidak ada keadaan yang sesudah restart kehilangan tombolnya atau
   disimpulkan "tidak terjadi": catatan kunci dan tanda "tidak berjawab" ada
   di keadaan pekerjaan, bukan di memori. Test: Task 3
   (`TestUnlockPromoteLockAfterPanelRestart`), Task 6
   (`TestPlannedRetryAfterPanelRestart`), Task 8
   (`TestRetryAfterPanelRestartKeepsUnanswered`).
2. **Pasangan dan catatan dari 0.2.0**: agent 0.2.0 yang menjawab panel baru
   (tanpa field baru, tidak mengenal perintah baru), dan pekerjaan promote
   tersimpan 0.2.0 yang dibaca kode baru (tenggat tidak terpotong, tanpa
   `ConfirmedAt`, terkirim tanpa id pekerjaan agent). Yang diharapkan: arah
   aman dan tidak ada yang macet. Test: Task 2
   (`TestStoredLockDeadlineClampedAtRead`,
   `TestLockRecordFromOldJobWithoutTimes`), Task 6
   (`TestPlannedRetryStaleAfter24h`, kasus tanpa `ConfirmedAt`), Task 8
   (`TestOldJobSentWithoutAgentJobCountsUnanswered`,
   `TestRetrySentUnansweredReadOnlyWithinBoundRefused`), Task 19
   (`TestNoRestartWarningFromOldAgent`), Task 22 (`TestEndApprovalTexts`,
   kasus `unknown_command`).
3. **Agent yang menjawab lambat atau tidak pernah** selagi panel menunggu di
   jalur baru (buka kunci promote, pembacaan langsung replika, karantina
   tertunda). Yang diharapkan: setiap panggilan berbatas, tidak ada kunci
   panel yang ditahan untuk server lain, dan operator mendapat teks yang
   menyebut langkah berikutnya. Test: Task 3
   (`TestUnlockPromoteLockAgentSilent`), Task 5
   (`TestPromoteDirectReadsRunInParallelAndBounded`), Task 14
   (`TestPendingQuarantineNotHeldBySilentAgent`).
4. **Dua sesi browser dari admin yang sama** (kantor dan laptop; atau admin
   dan pencuri sesi sesudah admin login lagi). Yang diharapkan: anggaran
   konfirmasi dan jendela kata sandi milik tiap sesi sendiri. Test: Task 1
   (`TestConfirmBudgetPerSession`, kasus sesi kedua), Task 9
   (`TestWindowIsPerSession`).
5. **Jam host panel berubah** (NTP melompat mundur atau maju). Yang
   diharapkan: jendela kata sandi tidak terbuka lebih lama, pembatas tidak
   mengunci lebih lama dari jendelanya, laporan tidak dianggap "langsung"
   dari masa depan, batas B tidak dianggap lewat. Test: Task 1
   (`TestRateLimiterClockSetBack`), Task 2 (`TestLiveReport`, kasus umur
   negatif), Task 8 (`TestUnansweredBoundClockSetBack`), Task 9
   (`TestWindowClockSetBack`).

---

## Keputusan penulis rencana

Spek bagian 6 menyerahkan bentuk (nama, kunci JSON, kolom, migrasi) kepada
rencana ini; arti dan angka dari spek. Yang berikut diputuskan di sini,
masing-masing dengan satu baris alasan.

- **K1. Dua puluh empat task dalam urutan bagian 6 spek.** Butir 2 dipecah
  tiga (catatan kunci; tombol; penolakan) dan butir 3 dipecah empat
  (pembacaan langsung; "Ulangi" sebelum kirim; field agent; keadaan
  "terkirim"), supaya tiap potong punya gerbang peninjau sendiri.
- **K2. README, Panduan dan spek lama ditulis ulang sekali, di Task 24**
  (spek 5.9), kecuali panduan baru "Memakai bekas replika sebagai server
  mandiri" yang ikut Task 23 karena flash T27 menunjuknya. Di dalam cabang,
  di antara task, beberapa kalimat README masih memuat perilaku lama.
- **K3. Kunci anggaran per sesi adalah SHA-256 heksadesimal dari token
  sesi**, bukan tokennya: token tidak pernah menjadi kunci peta pembatas.
- **K4. Cookie perangkat** bernama `__Host-dbpanel_device`, berisi
  `v1.<base64url nama pengguna>.<32 heks acak>.<detik unix terbit>.<base64url HMAC-SHA256>`;
  kuncinya `HMAC-SHA256(panel.key, "dbpanel device cookie v1")` lewat fungsi
  baru `secrets.Box.DeriveKey`. Alasan: tidak ada berkas kunci baru dan
  tidak ada migrasi.
- **K5. Cookie perangkat yang masih sah tidak diganti saat login berhasil**;
  yang baru terbit hanya bila tidak ada yang sah untuk nama itu. Alasan:
  catatan "pasangan yang pernah berhasil" memakai nomor cookie.
- **K6. Kata sandi salah pada login ber-cookie tidak dihitung ke batas nama
  pengguna maupun batas alamat** (hanya ke dua anggaran cookie). Alasan:
  spek berkata "dihitung pada anggarannya sendiri".
- **K7. Login ber-cookie menunggu slotnya paling lama 5 detik**, lalu
  dijawab "Server sibuk" yang ada. Spek hanya berkata "menunggu sebentar".
- **K8. Waktu yang berjalan mundur dihitung sebagai jendela yang berakhir**
  di pembatas, jendela kata sandi dan "laporan langsung" (umur negatif
  berarti tidak langsung). Cookie perangkat yang terbit "di masa depan"
  tetap sah bila tanda tangannya sah. Alasan: jam yang dimundurkan tidak
  boleh membuka jendela lebih lama; cookie bukan kredensial.
- **K9. Catatan kunci tetap memakai field yang ada** (`LockTried`,
  `LockTriedAt`, `LockDeadline`, `LockUnlocked`); catatan "berdiri" berarti
  `(LockTried || LockDeadline != nil) && !LockUnlocked`. Tidak ada migrasi.
- **K10. Catatan kunci yang terbukti hilang lewat laporan langsung master
  dihitung saat dibaca** (`LockRecords`), dan baru disimpan
  (`LockUnlocked`) oleh pemanggil yang memegang `startMu`. Alasan: halaman
  (GET) tidak pernah menulis.
- **K11. Rute tombol: `POST /jobs/{id}/unlock-lock`**, juga dari halaman
  master (form menunjuk pekerjaannya). Tanpa `requireConfirm`.
- **K12. Batas waktu atau sambungan putus saat `mysql.unlockPromote`
  dijawab dengan T11b**, seperti agent yang tidak tersambung. Alasan:
  nasihatnya sama, dan panel tidak tahu apakah kunci terbuka; catatan tetap
  berdiri.
- **K13. Tautan pekerjaan di T11t, T12c dan T15b**: galat orchestrator
  membawa id pekerjaan (`JobID`); di teks polos (audit, log) `%s` adalah id
  itu, dan di halaman id itu ditampilkan sebagai tautan `/jobs/{id}` oleh
  templat (`Page.FlashJob`). Panel tidak punya flash HTML dan tidak
  mendapatkannya di sini.
- **K14. Pembacaan langsung replika lain dikerjakan `PromoteView`** (di luar
  `startMu`, seperti probe yang ada), dan hasilnya dipakai layar,
  `checkPromote`, `StartPromote` dan "Ulangi". Layar promote karena itu
  bisa menunggu sampai 10 detik bila sebuah replika diam.
- **K15. "Ulangi" promote terencana sebelum kirim mulai dari langkah yang
  lebih awal di antara langkah gagalnya dan "Kunci master"**; pekerjaan yang
  gagal di "Buat akun replikasi di kandidat" mulai dari sana.
- **K16. Field `mysql.status`: `promote_request` (`*bool`)**; `nil` dari
  agent lama, `true` berarti "ada". Tanda dipasang lewat kait baru di
  `commands.Registry` yang dipanggil putaran baca sesi.
- **K17. Tanda di keadaan pekerjaan: `promote_sent_at` dan
  `promote_unanswered`.** Untuk pekerjaan 0.2.0, "waktu pembaruan terakhir"
  spek dibaca sebagai `FinishedAt`, lalu `StartedAt`, lalu `CreatedAt`:
  `store.Job` tidak punya waktu pembaruan. `FinishedAt` tidak lebih awal
  dari waktu kirim, jadi batas B hanya bisa lebih panjang (arah aman).
- **K18. Batas B = `max(30 menit, 2 × jumlah batas pekerjaan promote di
  agent)`, ditetapkan Task 8** dan dijaga test di kedua sisi. Pernyataan
  SQL yang hanya dibatasi `ReadTimeout` sambungan (10 menit) dihitung
  berbatas. Bila sebuah langkah tidak punya batas sama sekali, task
  berhenti dan melapor.
- **K19. Ekor T14t dipasang bila catatan kunci pekerjaan terencana itu
  masih berdiri**; ekor T14u bila master lama pekerjaan darurat itu
  tersambung dan statusnya "Terkunci". Alasan: keduanya bisa dihitung tanpa
  panggilan agent.
- **K20. Migrasi `0014_confirm_window.sql`** (`confirmations.via_window`)
  dan **`0015_topology_source_change.sql`** (`topologies.source_changed_by`,
  `source_changed_at`, keduanya boleh `NULL`).
- **K21. Tindakan berat tetap memanggil `requireConfirm`, yang kini menolak
  token jendela; tindakan ringan memanggil `requireConfirmLight`.** Dengan
  begitu setiap pemanggil yang ada otomatis berat.
- **K22. Dialog tindakan ringan tidak menampilkan isian kata sandi sampai
  panel menjawab 401 "butuh kata sandi"**; browser tidak menyimpan apa pun.
- **K23. Setujui pekerjaan yang tidak membuang data selain "restart MySQL
  master" tetap tanpa token**, seperti sekarang. Tabel 5.1 spek hanya
  menyebut restart itu.
- **K24. Dialog T31 hanya dibuka browser bila isian Telegram berubah**
  (`data-confirm-when-changed`), dan T4 diisi alamat yang diketik lewat
  `textContent`; panel menegakkan keduanya sendiri.
- **K25. Asal alamat master disimpan sebagai nama pengguna, atau
  `job:<id pekerjaan>` bila dipasang promote.**
- **K26. Putaran karantina: galat tanpa jawaban agent (batas waktu, putus)
  dicoba lagi sesudah 60 detik, berlipat sampai 5 menit; jawaban agent yang
  menolak langsung memakai selang 5 menit.**
- **K27. Test restart R-188 tetap di paket driver; pemecahan paket itu
  (spek 5.10) tinggal di bagian B.** Gerbang agent bertambah beberapa menit.
- **K28. Task 15 meng-commit test restart yang menegaskan nilai terukur di
  head** (hijau), sesudah lebih dulu dijalankan dengan nilai yang wajib
  (merah, keluarannya dicatat); Task 16 mengganti penegasannya ke nilai
  wajib. Alasan: setiap task harus lolos gerbangnya.
- **K29. Field laporan kesehatan: `restart_read_only` (`bool`,
  `omitempty`)**, hanya dikirim bila benar. Pembacaan blok konfigurasi
  (`--print-defaults`) disimpan paling lama 60 detik.
- **K30. "Perbaiki pengaturan tersimpan" memakai rute, token dan nama yang
  diketik milik "Buka kunci master"** dengan tanda `repair=1`.
- **K31. Permintaan "akhiri persetujuan" adalah perintah sendiri,
  `mysql.endRebuildApproval`**, bukan parameter `mysql.rebuildApproval`.
  Alasan: agent 0.2.0 mengabaikan parameter yang tidak dikenalnya, dan panel
  tidak bisa membedakan "berakhir" dari "tidak dikenal" (T21 atau T21b).
- **K32. Berkas kunci promote: field `readable_at`**, daftar
  `{"key":"host:port","addr":"alamat"}`.
- **K33. Panduan baru menjadi panduan ke-10**, id `bekas-replika-mandiri`.
- **K34. Yang tidak dikerjakan bagian A dan dibawa ke bagian B:** penanda
  generasi folder cadangan (centang T8 masih bisa dihindari dengan waktu);
  baris gerbang dari laporan basi (R-197); R-199; 5.6; 5.8; sisa 5.9 (R-192,
  R-195 di luar B1, R-196 termasuk kalimat `promote.go:560` SEC-293); 5.10.

## Struktur berkas

Baru:

- `internal/panel/auth/device.go` — cookie perangkat.
- `internal/panel/web/limits.go` — anggaran konfirmasi per sesi, anggaran
  login ber-cookie, kunci alamat /64.
- `internal/panel/orchestrator/promote_lockrecord.go` — catatan kunci,
  tenggat terpotong, `LockRecords`.
- `internal/panel/orchestrator/promote_unlock.go` — tindakan **Buka kunci
  promote**.
- `internal/panel/orchestrator/promote_refuse.go` — penolakan bersama mulai
  dan "Ulangi"; pembacaan langsung replika lain.
- `internal/panel/orchestrator/promote_sent.go` — bukti untuk keadaan
  "terkirim, hasil belum diketahui"; batas B.
- `internal/panel/orchestrator/quarantine_retry.go` — kunci per server
  sasaran dan putaran coba-ulang.
- `internal/panel/store/migrations/0014_confirm_window.sql`,
  `0015_topology_source_change.sql`.
- `internal/agent/drivers/mysql/readonly.go` — `OpenWrites`, `LockWrites`;
  `readonly_restart_test.go` — tabel restart.
- `internal/agent/drivers/mysql/promote_pending.go` — tanda "permintaan
  promote sudah dibaca".
- `internal/agent/drivers/mysql/restart_readonly.go` — field "akan baca-saja
  sesudah restart".
- `internal/panel/e2e_5b3a_test.go` — test ujung ke ujung bagian A.

Diubah (yang utama): `internal/secrets/box.go`;
`internal/panel/auth/{session,ratelimit,middleware}.go`;
`internal/panel/{panel,servers}.go`; `internal/panel/agentapi/pair.go`;
`internal/panel/store/{confirmations,topologies,promotions}.go`;
`internal/panel/monitor/{monitor,state,status,promote}.go`;
`internal/panel/orchestrator/{runner,promote,promote_view,agentjob,gate,gate_port,create_replica,resync,change_databases,controls,approval}.go`;
`internal/panel/web/{router,handlers_auth,handlers_confirm,handlers_jobs,handlers_mysql,handlers_promote,handlers_servers,handlers_controls,handlers_settings,handlers_oldmaster,handlers_guide,backup_folder,audit,render}.go`;
`web/static/app.js`; templat `_confirm.html`, `job_detail.html`,
`master_detail.html`, `promote.html`, `server_detail.html`, `servers.html`,
`replica_detail.html`, `settings.html`, `panduan.html`;
`internal/proto/{mysql,health}.go`;
`internal/agent/{session,cli}.go`; `internal/agent/commands/registry.go`;
`internal/agent/drivers/mysql/{promote,promote_lock,promote_files,configure,status,health,strict,pins,rebuild_approval,snapshot,databases}.go`;
`README.md`; `docs/superpowers/specs/2026-11-05-rencana-4-promote-design.md`
dan `…/2026-11-12-rencana-5b-mode-ketat-gerbang-prasyarat-design.md` (satu
kalimat masing-masing).

Daftar task (baris inventaris yang ditutup; gerbang):

| # | Task | Menutup | Gerbang |
|---|---|---|---|
| 1 | Batas percobaan: anggaran per sesi, cookie perangkat | R-181 | panel |
| 2 | Laporan langsung, catatan kunci, tenggat terpotong | R-184 (SEC-289, SEC-290) | panel |
| 3 | Tindakan **Buka kunci promote** | R-184 (N1, SEC-291) | panel, e2e |
| 4 | Penolakan selagi catatan kunci berdiri | R-184 | panel |
| 5 | Penolakan bersama dan pembacaan langsung replika lain | R-198 | panel |
| 6 | "Ulangi" promote terencana sebelum kirim | R-184 (SEC-240) | panel, e2e |
| 7 | Agent melaporkan "permintaan promote" | R-184 | agent, panel, `make test` |
| 8 | Terkirim, hasil belum diketahui | R-184 (SEC-288) | agent, panel, `make test`, e2e |
| 9 | Jendela kata sandi | dasar R-182, R-183, R-191 | panel, e2e |
| 10 | Pasang ulang agent | R-182 | panel, e2e |
| 11 | Alamat master untuk replika | R-183 | panel |
| 12 | Tindakan ringan lain | R-191 (bagian permintaan) | panel, e2e |
| 13 | Masukan gerbang dan audit | R-191 (bagian gerbang dan audit) | panel |
| 14 | Kunci per server dan karantina tertunda | R-185 | panel, e2e |
| 15 | R-188: ukur dulu | R-188 (SEC-247) | agent, panel, `make test` |
| 16 | R-188: satu pasang fungsi kunci dan buka | R-188 (SEC-246) | agent, panel, `make test`, e2e |
| 17 | `unlock-master` bertanya | R-188, R-187 (S-10) | agent, panel, `make test` |
| 18 | Status master membaca kedua bendera | R-188 | panel |
| 19 | Field "akan baca-saja sesudah restart" dan tombolnya | R-188 | agent, panel, `make test` |
| 20 | Penjaga kunci membaca replika di alamat tercatat | R-202 | agent, panel, `make test`, e2e |
| 21 | `set-role` bertanya | R-187 | agent, panel, `make test` |
| 22 | Persetujuan bangun ulang berakhir bersama percobaannya | R-203 | agent, panel, `make test`, e2e |
| 23 | Teks Lepas replika | R-190 | panel |
| 24 | README, Panduan, pembaruan, e2e, catatan ukur, daftar teks | R-195 (B1), teks semua butir | keempatnya |

---

### Task 1: Batas percobaan: anggaran konfirmasi per sesi dan cookie perangkat

Menutup R-181 (A5b2 SEC-236). Semua permintaan kata sandi baru bergantung
pada task ini.

**Files:**
- Create: `internal/panel/auth/device.go`, `internal/panel/web/limits.go`
- Modify: `internal/secrets/box.go`; `internal/panel/auth/ratelimit.go:49-131`, `middleware.go:27-51`; `internal/panel/panel.go:53-58,98-103,169-186` dan tempat `web.Deps` disusun; `internal/panel/web/router.go:41-88,140-181`, `handlers_auth.go:17-110`, `handlers_confirm.go:106-116,141-208`
- Test: `internal/panel/auth/auth_test.go`, `internal/secrets/box_test.go`, `internal/panel/web/web_limits_test.go` (baru), `web_confirm_test.go`, `web_test.go:192-343`

**Interfaces:**
- Consumes: `auth.RateLimiter`, `auth.Sessions`, `sessionEpoch.valid` (`panel.go:494`), `usernameKey`, `clientIP`.
- Produces:
  ```go
  // secrets
  func (b *Box) DeriveKey(label string) [32]byte // HMAC-SHA256(key, label)

  // auth
  func (r *RateLimiter) RetryAfter(key string) time.Duration // 0 when key is not blocked now
  func TokenFrom(ctx context.Context) string                 // the session token RequireLogin accepted; "" when absent
  const DeviceCookieName = "__Host-dbpanel_device"
  const DeviceCookieMaxAge = 365 * 24 * time.Hour
  type Device struct{ Username, ID string; Issued time.Time } // ID: 32 hex characters
  func NewDevice(key []byte, username string, now time.Time) (value string, d Device)
  func ParseDevice(key []byte, value string, now time.Time) (Device, bool)
  func SetDeviceCookie(w http.ResponseWriter, value string)
  func DeviceCookie(r *http.Request) (string, bool)

  // web.Deps (all optional; nil takes the value named)
  ConfirmLimiter     *auth.RateLimiter // per session key: 5 per minute
  ConfirmLongLimiter *auth.RateLimiter // per session key: 20 per 15 minutes
  DevicePairLimiter  *auth.RateLimiter // per device ID + "|" + ipKey: 5 per 15 minutes
  DeviceTotalLimiter *auth.RateLimiter // per device ID: 20 per 15 minutes
  DeviceKey          []byte            // nil: no device cookie is set or read
  IssuedValid        func(issued time.Time) bool // nil: always valid; the panel passes its session epoch check

  // web/limits.go
  func sessionKey(token string) string // hex SHA-256 of the session token (K3)
  func ipKey(ip string) string         // IPv4 unchanged; IPv6: its /64 prefix followed by "/64"
  ```
- Bentuk cookie: K4. `ParseDevice` menolak bentuk atau tanda tangan yang salah dan cookie yang lebih tua dari `DeviceCookieMaxAge`; nama pengguna dan `IssuedValid` diperiksa pemanggil.

Aturan (spek 5.1, "Batas percobaan"):
- **Konfirmasi.** `issueConfirm` memesan satu tempat di `ConfirmLimiter` dan `ConfirmLongLimiter` dengan kunci `sessionKey(auth.TokenFrom(ctx))` sebelum pemeriksaan kata sandi; kata sandi benar mengosongkan keduanya; permintaan yang tidak sampai ke pemeriksaan mengembalikan tempatnya. `UserLimiter` tidak lagi dibaca untuk memutuskan; kata sandi salah tetap memanggil `UserLimiter.Fail(userKey)`. Jawaban 429 memakai **T1** dengan `%d` = `RetryAfter` pembatas yang menolak, dibulatkan ke atas ke menit, paling sedikit 1.
- **Login ber-cookie.** Cookie sah untuk nama yang diketik (`strings.EqualFold`, `IssuedValid(d.Issued)`): tidak membaca `Limiter`, `UserLimiter` maupun `loginSem`. Urutannya: jumlah per cookie penuh (`DeviceTotalLimiter.Blocked(d.ID)`) dan pasangan ini belum pernah berhasil sejak panel menyala → 429 `msgLoginBlocked`; `DevicePairLimiter.Allow(pasangan)` gagal → 429 `msgLoginBlocked` (tidak jatuh ke aturan tanpa cookie); satu dari dua slot cookie, menunggu paling lama 5 detik (K7), bila tidak dapat tempatnya dikembalikan dan dijawab `msgLoginBusy`; kata sandi salah → `DeviceTotalLimiter.Fail(d.ID)`; benar → `DevicePairLimiter.Reset(pasangan)` dan pasangan dicatat berhasil (peta di memori, paling banyak 10.000 pasangan).
- **Login tanpa cookie**: aturan sekarang, dengan kunci alamat `ipKey(ip)`.
- **Login berhasil** memasang cookie perangkat baru bila permintaan tidak membawa yang sah untuk nama itu (K5). Logout tidak menghapusnya.
- `RateLimiter.current` membuang ember yang `start`-nya sesudah `now` (K8).

- [ ] **Step 1: Test gagal.**
  - `TestBoxDeriveKey`: label sama → kunci sama; label lain atau kunci lain → berbeda; tidak sama dengan kunci segel.
  - `TestRateLimiterRetryAfter`: 5/1 menit, 5 kali `Allow` pada t0 → `RetryAfter` pada t0+20 s = 40 s; kunci tidak diblokir → 0. `TestRateLimiterClockSetBack`: ember penuh pada t0, jam ke t0−1 jam → `Allow` benar.
  - `TestDeviceCookieRoundTrip`; `TestDeviceCookieRejected`: tanda tangan diubah, kunci lain, bagian kurang, terbit lebih dari setahun lalu → `false`.
  - `TestConfirmNotBlockedByOutsideLoginFailures` (penjaga 5.13): 20 login salah untuk `admin` dari empat alamat (5 tiap alamat); lalu `POST /confirm` dengan kata sandi benar dari sesi yang sudah ada → 200; lalu login benar dari alamat kelima dengan cookie perangkat sah → 303.
  - `TestConfirmBudgetPerSession`: 5 salah dalam satu menit → yang keenam 429 dengan T1 `%d` = 1; sesudah 1 menit diterima lagi; 20 salah tersebar dalam 15 menit → 429 dengan T1 yang menyebut sisa menit jendela 15 menit itu; sesi kedua admin yang sama tetap 200 selama sesi pertama diblokir; kata sandi benar mengosongkan keduanya.
  - `TestConfirmWrongStillCountsForLogin`: 20 konfirmasi salah → login tanpa cookie dari alamat baru 429; konfirmasi benar sesi itu, sesudah jendela per sesinya, tetap 200.
  - `TestDeviceCookieSetOnLogin`: nama `__Host-dbpanel_device`, `HttpOnly`, `Secure`, `SameSite=Lax`, `Path=/`, `Max-Age=31536000`; login kedua dengan cookie sah tidak mengirim `Set-Cookie` perangkat baru.
  - `TestDeviceLoginIgnoresCookieOfOtherUserOrBeforeReset`: cookie nama lain, tanda tangan palsu, atau `IssuedValid` salah → diperlakukan tanpa cookie (429 selagi batas nama pengguna penuh).
  - `TestDeviceBudgetPerPair`: 5 salah dari alamat A → keenam dari A 429; cookie yang sama dari alamat B masih dihitung sendiri (200 "kata sandi salah").
  - `TestDeviceTotalBlocksOnlyNewPairs`: pasangan (cookie, A) pernah berhasil; 20 salah dari empat alamat lain; alamat baru E → 429; A dengan kata sandi benar → 303.
  - `TestDeviceLoginReservesBeforeCheck`: 10 login salah bersamaan dari satu pasangan dengan `CheckPassword` lambat → paling banyak 5 mencapai pemeriksaan.
  - `TestLogoutKeepsDeviceCookie`; `TestLoginIPv6SharesSlash64`: lima login dari lima alamat dalam satu /64 menghabiskan batas alamat itu.
- [ ] **Step 2:** `go test ./internal/panel/auth ./internal/secrets ./internal/panel/web -run 'TestBoxDeriveKey|TestRateLimiter|TestDevice|TestConfirm|TestLogoutKeeps|TestLoginIPv6' -short`; FAIL (simbol belum ada).
- [ ] **Step 3:** Implementasi menurut aturan di atas. `panel.go` menyusun pembatas baru dengan jam panel dan mengisi `DeviceKey` dari `box.DeriveKey("dbpanel device cookie v1")` dan `IssuedValid` dari `epoch.valid`. Test login yang ada (`TestLoginRateLimited`, `TestLoginRateLimitedPerUsername`, `TestLoginConcurrencyCap`) tetap lulus tanpa diubah maknanya.
- [ ] **Step 4:** Dua gerbang panel, exit 0.
- [ ] **Step 5: Commit:** `feat(panel): a confirmation budget per session and a device cookie for logins`.

### Task 2: Laporan langsung, catatan kunci, tenggat terpotong

Menutup dari R-184: SEC-289 (jendela bergeser), SEC-290 (tenggat tanpa
batas), dan temuan tinjauan S-1, N-3. Tidak ada tombol atau teks operator
baru di task ini; penolakan yang ada memakai catatan yang benar.

**Files:**
- Create: `internal/panel/orchestrator/promote_lockrecord.go`, `promote_lockrecord_test.go`
- Modify: `internal/panel/monitor/monitor.go:59-77`, `state.go:146-166`; `internal/panel/store/promotions.go`; `internal/panel/orchestrator/promote.go:330-447,644-688,756-769`, `promote_view.go:15-25`
- Test: `internal/panel/monitor/monitor_test.go`, `internal/panel/orchestrator/promote_test.go:784-960,1152-1240`, `promote_retry_test.go`

**Interfaces:**
- Consumes: `jobState.{LockTried,LockTriedAt,LockDeadline,LockUnlocked,PromoteSent,PastPoint}`, `promoteLockSec`, `lockCallSlack`, `promoteClockSkew`, `proto.PromoteLockGrace`, `hub.SentByAgent`, `monitor.LoadSettings`.
- Produces:
  ```go
  // monitor
  // View.Restored: Snap was loaded from the store at startup and no report
  // has been taken since (in memory only; no migration).
  func Live(v View, online bool, now time.Time, staleAfter time.Duration) bool

  // store
  func (s *Store) ListPromotions(topologyID int64) ([]Promotion, error) // newest first, at most 200

  // orchestrator
  func (r *Runner) liveView(serverID int64) (monitor.View, bool) // the view, and whether its report is live
  type LockRecord struct {
      JobID                 string
      PromotionID           int64
      MasterID, CandidateID int64
      Until                 time.Time // clamped deadline + proto.PromoteLockGrace
      Sent                  bool      // PromoteSent || PastPoint
  }
  func lockDeadlineCap(triedAt time.Time) time.Time // triedAt + promoteLockSec s + lockCallSlack + promoteClockSkew
  func lockUntil(st jobState, prom store.Promotion) time.Time
  func (r *Runner) LockRecords(masterID int64) ([]LockRecord, error)       // standing records, newest first; writes nothing
  func (r *Runner) settleLockRecords(masterID int64) ([]LockRecord, error) // callers hold startMu; stores LockUnlocked on proven-open records
  ```

Aturan (spek 5.2 "Catatan kunci", 5.7 "laporan langsung"):
- `Live` benar hanya bila: `online`, `v.Snap != nil`, bukan `Backlog`, bukan `Restored`, `ReceivedAt` tidak nol, dan `0 <= now − ReceivedAt <= staleAfter` (`Settings.StaleAfter`).
- Catatan sebuah pekerjaan `promote_planned` **berdiri** bila semua ini benar: `(LockTried || LockDeadline != nil) && !LockUnlocked` (K9); `MasterServerID` pekerjaan itu masih master topologinya; tidak ada promosi topologi itu berstatus `done` dengan id lebih besar dari `PromotionID`-nya (promosi yang lebih baru yang `running` atau `partial` tidak melepas catatan); dan catatan itu belum terbukti hilang.
- **Terbukti hilang** hanya dengan salah satu: `mysql.unlockPromote` menjawab berhasil atau "tidak dikunci untuk promosi" (kode yang ada di `promoteUndo`); atau `now >= Until` **dan** laporan langsung master (`liveView`) dengan `MySQLOK`, `ReadOnly == false` dan `SuperReadOnly == false`. `settleLockRecords` menyimpan `LockUnlocked = true` untuk yang kedua dan menulis baris log pekerjaan **P9** (tidak ada di spek): `Kunci promote terbukti sudah tidak ada: master melapor bisa ditulis sesudah tenggat kunci.`
- `lockUntil`: tenggat tersimpan dipotong ke `lockDeadlineCap(LockTriedAt)` lalu ditambah `proto.PromoteLockGrace`; tanpa `LockDeadline` atau tanpa `LockTriedAt`, urutan cadangan yang ada di `promote.go:418-428` dipertahankan. Potongan yang sama berlaku untuk pekerjaan tersimpan 0.2.0.
- `promoteLock`: sebelum memanggil, nilai `LockTriedAt` dan `LockDeadline` lama diingat. Jawaban galat **dari agent** (`hub.SentByAgent`) tidak menghapus catatan: `LockTried` tetap benar, `LockUnlocked` tetap salah, dan bila nilai lama ada, kedua cap waktu dikembalikan ke nilai lama. Jawaban berhasil: `LockDeadline = min(res.Deadline, lockDeadlineCap(tried))`, dengan satu baris `Warn` bila terpotong; nilai terpotong itulah yang dikirim ke kandidat (`promote.go:764-768` sudah menghitung dari `st.LockDeadline`).
- `refuseWhileLockMayStand` membaca `LockRecords(masterID)` (setiap promote terencana master itu, bukan hanya yang terakhir) dan menolak selama `now < Until` salah satu catatan. Teksnya belum berubah di task ini.

- [ ] **Step 1: Test gagal.**
  - `TestLiveReport` (tabel): agent offline; `Backlog`; `Restored`; umur > `StaleAfter`; umur negatif (jam mundur) → salah; laporan segar → benar. `TestRestoredSnapshotMarked`: Monitor yang memuat laporan dari store → `View.Restored` benar; sesudah satu laporan diterima → salah.
  - `TestLockDeadlineClamped`: master menjawab `Deadline` = sekarang + 10 jam → `LockDeadline` tersimpan = `LockTriedAt` + 40 menit; `LockDeadline` di parameter `mysql.promote` sama; `LockRemainingSec` ≤ 2400.
  - `TestStoredLockDeadlineClampedAtRead`: params JSON seperti tulisan 0.2.0 dengan `lock_deadline` setahun lagi → `LockRecords` memberi `Until` = `lock_tried_at` + 40 menit + 60 detik.
  - `TestLockRecordFromOldJobWithoutTimes`: `lock_tried` benar tanpa cap waktu → `Until` dari `FinishedAt` promosi + 1800 detik + 60 detik, seperti sekarang.
  - `TestRelockRefusalKeepsRecordAndTimes`: kunci dipertahankan; "Ulangi" pada tenggat + 1 jam; master menolak kunci ulang → `LockTried` benar, `LockUnlocked` salah, `LockTriedAt` dan `LockDeadline` sama dengan sebelum "Ulangi"; promote darurat sesudah itu tidak ditolak jendela kunci.
  - `TestLockAnsweredErrorDoesNotClearRecord`: `mysql.lockForPromote` menjawab galat "master sudah dikunci (kunci promosi tetap), tetapi …" dan `mysql.unlockPromote` gagal → catatan berdiri.
  - `TestLockRecordsReadEveryPlannedPromotion`: dua promote terencana gagal dari master yang sama, yang lama berdiri dan yang baru terbukti dibuka → `LockRecords` memuat yang lama; promote darurat di dalam jendelanya ditolak.
  - `TestLockRecordNotCountedAfterMasterChanged`; `TestLockRecordNotCountedAfterLaterPromotionDone`; `TestLockRecordCountedWhileLaterPromotionPartial`.
  - `TestLockRecordClearedByLiveReport`: sebelum `Until`, laporan langsung kedua bendera OFF → berdiri; sesudah `Until`, laporan yang sama → tidak berdiri, dan `settleLockRecords` menyimpan `LockUnlocked` serta menulis P9; laporan pulihan, tidak ada laporan, agent offline, atau hanya `read_only` ON → berdiri.
  - `TestLockRecordsWritesNothing`: `LockRecords` tidak mengubah `ParamsJSON`.
- [ ] **Step 2:** `go test ./internal/panel/monitor ./internal/panel/orchestrator -run 'TestLiveReport|TestRestoredSnapshot|TestLock|TestStoredLock|TestRelock' -short`; FAIL.
- [ ] **Step 3:** Implementasi. `fakeHealth` di `promote_test.go` dipakai apa adanya; pembantu `greenGate` dan `newPromoteRig` memberi laporan yang `Live` (tidak `Restored`, `ReceivedAt` = sekarang).
- [ ] **Step 4:** Dua gerbang panel, exit 0.
- [ ] **Step 5: Commit:** `fix(panel): keep the promote lock record through a refused relock and bound its deadline`.

### Task 3: Tindakan Buka kunci promote

Menutup dari R-184: pelajaran N1 (A5b2 SEC-280), SEC-291, dan temuan
tinjauan T-2, T-3, N-5, R-3, R-4, R-8, P2. Sesudah task ini master punya
tombolnya sebelum "Ulangi" diubah.

**Files:**
- Create: `internal/panel/orchestrator/promote_unlock.go`, `promote_unlock_test.go`
- Modify: `internal/panel/orchestrator/promote.go:377-447,1112-1159,1710-1745`, `runner.go:967-1031`; `internal/panel/monitor/promote.go:387-400`; `internal/panel/web/router.go:239-245`, `handlers_jobs.go:119-172` dan pengisi halamannya, `handlers_mysql.go:21-90` (`Orchestrator`), model `masterPage`, `render.go:21-25,167` (`Page.FlashJob`), `audit_summary.go`; `web/templates/job_detail.html:19,81-91`, `master_detail.html:118`, `layout.html:93` (banner)
- Test: `orchestrator/promote_retry_test.go:121-183` (ditulis ulang), `web/web_jobs_test.go`, `web/web_mysql_jobs_test.go` (`fakeOrch`), `monitor/alerts_test.go:2008`

**Interfaces:**
- Consumes: `LockRecords`, `settleLockRecords`, `liveView` (Task 2); `promoteSupersededBy`; `peerCred`, `replAccount`; `agentErrText`.
- Produces:
  ```go
  type UnlockOutcome int
  const ( UnlockOpened UnlockOutcome = iota + 1; UnlockNotLocked )

  // UnlockLockError is a refusal of "Buka kunci promote"; Code picks the text.
  type UnlockLockError struct {
      Code       string // sent | running | superseded | newer_open | agent_offline | candidate_writable | quarantined | refused | no_credential
      Master     string // server names
      Candidate  string
      AgentText  string // labelled and bounded; "" when no agent answered
      NewerJobID string // newer_open: the promote job to finish first
      Cause      string // no_credential: why
  }
  func (e *UnlockLockError) Error() string
  func (r *Runner) UnlockPromoteLock(ctx context.Context, jobID string) (UnlockOutcome, error)
  func (r *Runner) UnlockLockOffered(jobID string) bool
  // UnlockLockJob is the planned promote job the master page's button acts on.
  func (r *Runner) UnlockLockJob(masterID int64) (jobID string, sent bool, err error)
  var ErrPromoteLockStands error // *promoteLockStands{master}: text T12b

  // LockStandsError refuses a load while a planned promote's lock record
  // may stand (T15b); Task 4 adds the codes planned and master_locked.
  type LockStandsError struct {
      Code   string // load
      Master string
      What   string // "Resync ulang", "Buat replika", "Tambah database", "Lepas database"
      JobID  string // the promote job whose lock record stands
  }
  func (e *LockStandsError) Error() string
  ```
  `web.Orchestrator` bertambah ketiga metode itu. Rute `POST /jobs/{id}/unlock-lock` (K11), tindakan audit `promote_unlock_lock` (sasaran `auditJob`, server pekerjaan; detail `job_id`, `result` = `opened`, `not_locked` atau `Code`).

Aturan `UnlockPromoteLock`, dalam urutan ini; pemeriksaan 1 sampai 4 di bawah `startMu`, panggilan agent di luarnya, dan catatan ditulis sesudah pemeriksaan 1 sampai 4 diulang di bawah `startMu`:
1. Pekerjaan bukan `promote_planned` → galat biasa.
2. `PromoteSent || PastPoint` → `sent`, teks **P1** (tidak ada di spek): `Perintah promote pekerjaan ini sudah dikirim ke kandidat, jadi kunci promote tidak dibuka dari panel. Tekan "Ulangi dari tahap ini" pada pekerjaan ini; panel memeriksa kandidat dulu.` Tidak ada yang dikirim.
3. Pekerjaan itu sendiri masih berjalan atau menunggu → `running`, teks yang sudah ada `promote masih berjalan; tunggu sampai selesai`.
4. Digantikan: promosi topologi itu yang lebih baru berstatus `done`, atau master pekerjaan itu bukan lagi master topologinya → `superseded`, **T11s**; promosi yang lebih baru `running` atau `partial` → `newer_open`, **T11t** dengan `NewerJobID`. Kedua teks tidak menyebut `unlock-master`.
5. Agent master tidak tersambung → `agent_offline`, **T11b**.
6. Kredensial kandidat disusun seperti `promoteUndo` (`promote.go:1127-1141`). Tidak bisa disusun → `no_credential`: kalimat **P2** (tidak ada di spek) `Panel tidak bisa menyusun kredensial kandidat %s untuk membuka kunci (%s).` diikuti T11 mulai dari `Jangan Resync kandidat` sampai akhir.
7. `mysql.unlockPromote` dikirim (batas `callTimeout`). Berhasil → `UnlockOpened`: catatan dibuka (`LockDeadline = nil`, `LockTried = false`, `LockUnlocked = true`), baris log pekerjaan dan flash **T10**. Jawaban memuat "tidak dikunci untuk promosi" → `UnlockNotLocked`, catatan dibuka, **T10b**.
8. Batas waktu atau sambungan putus (bukan jawaban agent) → `agent_offline`, T11b (K12); catatan tetap.
9. Agent menolak: panel membaca kandidat (`mysql.status`, paling lama 10 detik) dan peran master, lalu memilih ekor dari keadaan, bukan dari teks agent: master berperan `quarantined` → `quarantined`, **T11d**; kandidat terbaca dengan `SuperReadOnly == false` → `candidate_writable`, **T11c**; selain itu (baca-saja, tidak terbaca) → `refused`, **T11**. `<pesan agent>` adalah `AgentText`.

Aturan lain:
- `UnlockLockOffered`: pekerjaan `promote_planned`, status gagal atau dibatalkan, belum dikirim, catatannya berdiri (`LockRecords`), dan aturan 4 tidak menolak.
- `UnlockLockJob` (halaman master, tampil selama status "Terkunci"): pekerjaan catatan berdiri yang terbaru; bila tidak ada, promote terencana terakhir master itu yang gagal atau batal, belum dikirim dan belum digantikan; bila pekerjaan catatan itu sudah dikirim, `sent` benar dan halaman tidak menampilkan tombol (teksnya datang di Task 8). **Buka kunci master** tetap tampil di sampingnya.
- Halaman pekerjaan menampilkan **T9** di atas tombol **Buka kunci promote**.
- `Retry`: pekerjaan `promote_planned` yang belum dikirim dan catatannya berdiri ditolak dengan **T12b** (`ErrPromoteLockStands`) sebelum apa pun dikirim; `RetryRefused` mengembalikan teks yang sama supaya halaman menyembunyikan "Ulangi". Pekerjaan yang sudah dikirim atau lewat titik tak kembali tidak pernah ditolak karena catatan kunci.
- Teks yang diganti: akhir `lockExpiredMessage` untuk kunci yang dipertahankan menjadi `… aplikasi belum bisa menulis. Periksa job promote di halaman Pekerjaan. ` + **T16**; baris log `promote.go:1153` menjadi **T17** (juga untuk promote yang dibatalkan); `howToUnlock` (`promote.go:435-437`) pada penolakan promote darurat menjadi **T15**; pada penolakan muat data teksnya menjadi **T15b** (aturannya diperluas Task 4).
- Tautan pekerjaan di T11t dan T15b: K13.

- [ ] **Step 1: Test gagal.**
  - `TestUnlockPromoteLockAfterKeptLock` (pengganti `TestPromotePlannedRetryLiftsKeptLock`; penjaga 5.13): kunci dipertahankan, jam di tenggat + 1 jam → satu `mysql.unlockPromote` ke master dengan kredensial kandidat (host `10.0.0.2`, pengguna `dbpanel_repl`); hasil `UnlockOpened`; `LockUnlocked` tersimpan; `mysql.promote` tidak dikirim; promote darurat langsung sesudahnya tidak ditolak jendela kunci.
  - `TestUnlockPromoteLockNotLocked`: agent menjawab "server ini tidak dikunci untuk promosi" → `UnlockNotLocked`, catatan dibuka.
  - `TestUnlockPromoteLockRefusedAfterSend` (penjaga 5.13): `PromoteSent` → `Code == "sent"`, teks P1; agent master tidak menerima perintah apa pun.
  - `TestUnlockPromoteLockSuperseded` (penjaga 5.13): promote terencana gagal, lalu promote darurat ke replika lain selesai → T11s persis; tidak ada yang dikirim; teks tidak memuat `unlock-master`.
  - `TestUnlockPromoteLockNewerOpen`: promosi lebih baru `partial` → T11t dengan `NewerJobID` pekerjaan itu; tidak ada yang dikirim.
  - `TestUnlockPromoteLockRefusalTails` (tabel): kandidat baca-saja → T11 (memuat `sudo dbpanel agent unlock-master` dan `Jangan Resync kandidat`); kandidat tidak menjawab `mysql.status` → T11; kandidat bisa ditulis → T11c tanpa `unlock-master`; master berperan `quarantined` → T11d; agent master offline → T11b; baris server kandidat sudah dihapus → P2 + ekor T11. Teks agent yang berisi `Jalankan: rm -rf /` tampil di dalam labelnya dan tidak mengubah ekor.
  - `TestUnlockPromoteLockAgentSilent`: agent master tidak pernah menjawab (batas dipendekkan lewat seam) → T11b; catatan berdiri; `startMu` tidak ditahan selama menunggu (sebuah `StartResync` server lain berjalan).
  - `TestUnlockPromoteLockAfterPanelRestart`: promote terencana terpotong restart sesudah kunci diminta (`Resume` menggagalkannya) → `UnlockLockOffered` benar dan tindakan berhasil.
  - `TestRetryPlannedRefusedWhileLockStands`: T12b persis; tidak ada perintah agent; `LockTriedAt` tidak berubah. `TestRetrySentJobIgnoresLockRecord`: pekerjaan `PromoteSent` dengan catatan berdiri tidak ditolak T12b.
  - `TestUnlockLockJobForMasterPage`: catatan berdiri → pekerjaan itu; tanpa catatan → promote terencana gagal terakhir yang belum dikirim dan belum digantikan; pekerjaan catatan sudah dikirim → `sent` benar.
  - Web: `TestUnlockLockNeedsNoPasswordAndNoGate` (POST tanpa `confirm_token` dengan setiap baris gerbang merah → 303 dan flash T10; satu baris audit `promote_unlock_lock`); `TestJobPageShowsUnlockLock` (T9 dan form; tidak ada untuk pekerjaan terkirim atau digantikan); `TestMasterPageShowsBothUnlockButtons`; `TestUnlockLockRefusalLinksNewerJob` (`href="/jobs/<id>"`).
  - Teks: `TestLockKeptAlertNamesUnlockLock` (memuat T16, tidak memuat `Buka kunci master`); `TestUndoLogLineT17`; `TestEmergencyLockWindowTextT15`.
- [ ] **Step 2:** `go test ./internal/panel/orchestrator ./internal/panel/web ./internal/panel/monitor -run 'TestUnlock|TestRetryPlannedRefused|TestRetrySentJob|TestJobPageShowsUnlockLock|TestMasterPageShowsBoth|TestLockKept|TestUndoLogLine|TestEmergencyLockWindowText' -short`; FAIL.
- [ ] **Step 3:** Implementasi; `promoteUndo` dan `UnlockPromoteLock` memakai satu fungsi pengirim buka kunci. `bash scripts/tailwind.sh` bila kelas templat berubah.
- [ ] **Step 4:** Dua gerbang panel dan `make test-e2e`, exit 0.
- [ ] **Step 5: Commit:** `feat(panel): unlock a kept promote lock with its own action, without a password`.

### Task 4: Penolakan selagi catatan kunci berdiri

Menutup dari R-184: temuan tinjauan T-4, N-2, S-2 (muat data mengkarantina
master yang sehat; promote terencana kedua di atas kunci yang berdiri).

**Files:**
- Modify: `internal/panel/orchestrator/promote.go:158-306,339-447`, `create_replica.go:159`, `resync.go:78`, `change_databases.go:89,135`, `runner.go:983-985`; `internal/panel/web/handlers_promote.go:400-420,600-625` (layar promote menampilkan penolakan sebelum apa pun diketik)
- Test: `orchestrator/promote_lockrecord_test.go`, `resync_test.go`, `web/web_promote_test.go`

**Interfaces:**
- Consumes: `LockRecords`, `settleLockRecords`, `liveView` (Task 2); `LockStandsError` dan tombol Task 3.
- Produces: `LockStandsError.Code` bertambah `planned` (T12c) dan `master_locked` (T12d; `JobID` kosong). Tidak ada simbol baru lain.

Aturan (spek 5.2 "Selagi catatan itu berdiri"):
- **Muat data** (mulai dan "Ulangi" dari Resync ulang, Buat replika, Tambah database, Lepas database dengan data): ditolak dengan **T15b** selama `settleLockRecords(master)` tidak kosong. Karena catatan baru bisa hilang lewat laporan sesudah `Until`, di dalam jendela jam muat data ditolak apa pun kata laporan; sesudahnya ia ditolak selama catatan berdiri, juga bila master tidak punya laporan langsung.
- **Promote terencana baru**: `settleLockRecords` tidak kosong → **T12c** (pekerjaan catatan terbaru); kosong tetapi status master di Monitor `monitor.StatusLocked` → **T12d**.
- **Promote darurat**: hanya aturan jam Task 2 (`now < Until`), tidak lebih lama dari sekarang.
- Catatan milik pekerjaan yang sudah dikirim (`LockRecord.Sent`) ikut menahan muat data dan promote terencana baru dengan teks yang sama (spek tidak punya teks lain); halaman pekerjaan itu yang menunjuk "Ulangi dari tahap ini" (P1 di Task 3, T14 di Task 8).

- [ ] **Step 1: Test gagal.**
  - `TestLoadRefusedWhileLockRecordStands` (tabel jenis × mulai dan "Ulangi"; penjaga 5.13): kunci dipertahankan, tenggat + 2 jam, master "Terkunci" → T15b persis dengan nama tindakan dan `JobID`.
  - `TestLoadRefusedWithoutLiveMasterReport`: catatan berdiri, laporan master pulihan, tidak ada, atau agent master terputus → Resync dan "Ulangi" sebuah resync gagal ditolak.
  - `TestLoadRefusedInsideWindowDespiteOpenReport`: di dalam jendela jam, laporan langsung kedua bendera OFF → tetap ditolak.
  - `TestLoadAllowedAfterLiveOpenReport`: sesudah jendela, laporan langsung kedua bendera OFF → catatan hilang; Resync dan promote terencana baru diizinkan.
  - `TestLoadIgnoresRecordOfFormerMaster`: catatan milik pekerjaan yang masternya sudah berganti tidak menahan apa pun.
  - `TestPlannedStartRefusedWhileLockStands` (T12c dengan `JobID`); `TestPlannedStartRefusedWhileMasterLocked` (T12d).
  - `TestEmergencyKeepsClockRuleOnly`: catatan berdiri, sesudah `Until`, master tanpa laporan → promote darurat dimulai.
  - Web: `TestPromoteScreenShowsLockRefusal` (T12c tampil di GET layar promote, tombol mati); `TestLockRefusalLinksJob`.
- [ ] **Step 2:** Jalankan test itu; FAIL.
- [ ] **Step 3:** Implementasi.
- [ ] **Step 4:** Dua gerbang panel, exit 0.
- [ ] **Step 5: Commit:** `fix(panel): refuse loads and a new planned promote while a promote lock may stand`.

### Task 5: Penolakan bersama dan pembacaan langsung replika lain

Menutup R-198 (A5b2 SEC-262) sejauh rancangan (tidak ada jalan melewati
replika yang bisa ditulis; yang berubah dari mana panel tahu), dan
menyiapkan fungsi penolakan yang dipakai "Ulangi" di Task 6 (temuan S-3, P3,
NR-7).

**Files:**
- Create: `internal/panel/orchestrator/promote_refuse.go`, `promote_refuse_test.go`
- Modify: `internal/panel/orchestrator/promote.go:158-328` (isi dipindah, bukan diubah maknanya), `promote_view.go:27-60,194-300`, `runner.go:1620-1656` (`jobState`); `internal/panel/web/handlers_promote.go:43,54,184,408-414,552,621`; `web/templates/promote.html`
- Test: `orchestrator/promote_test.go`, `web/web_promote_test.go`

**Interfaces:**
- Consumes: `liveView` (Task 2), `LockStandsError` (Task 3, 4), `PromoteView`, `Candidate`.
- Produces:
  ```go
  const otherReplicaReadTimeout = 10 * time.Second // Runner.otherReadTimeout in tests

  // Candidate, per replica row of the consideration data:
  //   Writable       bool // read now, or reported live: super_read_only off (T24)
  //   WritableSilent bool // not readable now and its last report says writable (T24b)
  //   Unread         bool // not readable now; its last report says read-only, or it has none
  // PromoteView.Unread []string // names of the Unread rows, in row order (T24d)

  // PromoteWritableError is a promote refused over another replica.
  type PromoteWritableError struct {
      Code string // writable (T24) | writable_silent (T24b) | unreadable (T24c)
      Name string
  }
  func (e *PromoteWritableError) Error() string

  // promoteRefusals is every refusal of StartPromote between the read of
  // the topology and the gate, for p against view; callers hold startMu.
  func (r *Runner) promoteRefusals(p PromoteParams, view PromoteView, topo store.Topology) (cand Candidate, mode string, err error)

  // jobState, recorded by StartPromote for a later "Ulangi":
  //   AcceptUnreachable []int64 `json:"accept_unreachable,omitempty"`
  //   AcceptBranched    bool    `json:"accept_branched,omitempty"`
  ```

Aturan (spek 5.7):
- Pembacaan langsung = satu `mysql.status` ke agent replika itu, paling lama 10 detik, sejajar untuk semua replika yang dibaca, dikerjakan `PromoteView` (K14).
- **Mode terencana:** setiap replika yang laporannya tidak `Live`, tidak ada, atau berkata bisa ditulis dibaca langsung. Jawaban bisa ditulis → `Writable`. Tidak terbaca (agent offline, galat, batas waktu) → `Unread`, dan promote terencana ditolak dengan **T24c**.
- **Mode darurat:** setiap replika yang agentnya tersambung dibaca langsung. Bisa ditulis → `Writable` (**T24**, ditolak juga saat darurat). Tidak tersambung atau tidak menjawab dalam 10 detik: dinilai dari laporan terakhirnya; bisa ditulis → `WritableSilent` (**T24b**, ditolak); baca-saja atau tanpa laporan sama sekali → `Unread`, tidak menahan, dan namanya masuk **T24d** di layar.
- Replika dengan laporan `Live` yang berkata baca-saja tidak dibaca lagi pada mode terencana.
- `promoteRefusals` menjalankan, dalam urutan yang ada di `promote.go:193-294`: mode, centang master mati (darurat), master lama yang belum dibereskan (terencana), kandidat replika topologi ini, cabang, replika lain (`Writable` → T24; `WritableSilent` → T24b; terencana dan `Unread` → T24c), promosi sebelumnya belum tuntas, jendela kunci (darurat) dan catatan kunci atau master terkunci (terencana, Task 4), kelayakan kandidat, versi agent master, kandidat sebagian, replika tak terjangkau, pekerjaan lain di topologi. `StartPromote` memanggilnya lalu menilai gerbang seperti sekarang.
- Layar promote menampilkan T24, T24b atau T24c di `p.Blocked` sebelum apa pun diketik, dan T24d sebagai catatan pada mode darurat. `msgPromoteWritable` dan kalimat `promote.go:235-236` hilang.

- [ ] **Step 1: Test gagal.**
  - `TestPromoteStaleWritableReportReadLive`: laporan basi berkata bisa ditulis, `mysql.status` menjawab `super_read_only` ON → diizinkan, kedua mode.
  - `TestPromoteLiveWritableRefused`: T24 persis (tiga kali nama replika), kedua mode.
  - `TestPlannedRefusesUnreadableReplica`: agent replika offline; dan agent yang tidak menjawab dalam batas → T24c.
  - `TestEmergencyNotHeldByOfflineReadOnlyReplica`: offline, laporan terakhir baca-saja → dimulai; `view.Unread` memuat namanya. `TestEmergencyOfflineWritableReplicaRefused`: T24b. `TestEmergencyReplicaWithoutAnyReport`: tidak menahan, disebut.
  - `TestPromoteDirectReadsRunInParallelAndBounded`: tiga replika yang agentnya tidak menjawab, batas 200 ms lewat seam → `PromoteView` pulang dalam kurang dari 2 × batas.
  - `TestPromoteRefusalsOrderUnchanged`: tabel penolakan lama (bukan replika master ini, bercabang tanpa persetujuan, kandidat tidak layak, sebagian tanpa persetujuan, tak terjangkau tanpa persetujuan, topologi sibuk) memberi teks yang sama dengan sebelum pemindahan.
  - `TestStartPromoteStoresAcceptFlags`.
  - Web: `TestPromoteScreenShowsWritableTextBeforeTyping` (T24 di GET, tombol mati); `TestEmergencyScreenNamesUnreadReplicas` (T24d).
- [ ] **Step 2:** Jalankan; FAIL.
- [ ] **Step 3:** Implementasi.
- [ ] **Step 4:** Dua gerbang panel, exit 0.
- [ ] **Step 5: Commit:** `feat(panel): read other replicas live before a promote and name the way out`.

### Task 6: "Ulangi" promote terencana sebelum kirim, seperti memulainya

Menutup dari R-184: SEC-240 (PC-1) untuk promote terencana, temuan S-3, S-5,
dan audit "Ulangi" promote dari R-191.

**Files:**
- Modify: `internal/panel/orchestrator/runner.go:615-668,690-765,774-828,959-1031`, `promote.go:306-328,1710-1745`; `internal/panel/web/handlers_jobs.go:119-172,463-540` dan pengisi halaman pekerjaan, `handlers_mysql.go:21-90` (`Orchestrator`); `web/templates/job_detail.html:81-91`
- Test: `orchestrator/promote_retry_test.go:184-256`, `runner_test.go:229` (`TestPromoteRetryStillTokenless` ditulis ulang), `web/web_jobs_test.go`, `web/web_gate_test.go`

**Interfaces:**
- Consumes: `promoteRefusals`, `jobState.AcceptUnreachable/AcceptBranched` (Task 5); `ErrPromoteLockStands` (Task 3); `Gate`, `GateError`, `ErrBackupAck`, `ErrJobNeedsConfirm`, `confirmMaxAge`.
- Produces:
  ```go
  // needsConfirm: a Retry or Approve of the job needs a password token typed
  // for it: it drops data (dropsData), or it is a planned promote whose
  // mysql.promote was not sent yet.
  func needsConfirm(kind string, st jobState) bool
  func (r *Runner) NeedsConfirm(jobID string) (bool, error)
  // JobAck.BackupAck bool: the backup tick of a planned promote's Retry (form backup_ack=1).
  var ErrPromoteRetryStale = errors.New(<T12>)
  ```
  `gateActionForJob` mengembalikan `GatePromotePlanned` untuk `promote_planned` dengan `!PromoteSent && !PastPoint`; `JobGateInput` mengisinya dengan `MasterID: st.MasterServerID, TargetID: st.CandidateServerID`. `StartPromote` terencana menyimpan `ConfirmedAt = now`. `web.Orchestrator` bertambah `NeedsConfirm`.

Aturan (spek 5.2, baris 1 tabel):
- `Retry` pekerjaan `promote_planned` yang belum dikirim: `PromoteView` dibaca sebelum `startMu`; di bawah `startMu`, sesudah pemeriksaan yang ada (pekerjaan lain, digantikan) dan T12b: `promoteRefusals` (mode harus terencana; parameter dari keadaan pekerjaan) → gerbang (`GateError`) → centang cadangan bila `NeedsBackupAck()` (`ErrBackupAck`) → `ack.Confirmed` (`ErrJobNeedsConfirm`) → umur konfirmasi terakhir (`ConfirmedAt`, atau `CreatedAt` untuk pekerjaan 0.2.0) lebih dari 24 jam → **T12** → `ConfirmedAt = now` disimpan → pekerjaan dijalankan dari langkah menurut K15.
- `RetryRefused` juga mengembalikan T12 untuk promote terencana yang konfirmasinya lewat 24 jam, supaya halaman menyembunyikan "Ulangi".
- Handler: gerbang dinilai sebelum token dipakai (seperti pekerjaan yang membuang data); token `job_retry:<id>` wajib, dan selalu token berkata sandi (Task 9 menegakkan bahwa token jendela ditolak di sini).
- Halaman pekerjaan untuk keadaan ini: daftar Prasyarat baris merah, centang D3 bila diminta, dan "Ulangi dari tahap ini" sebagai tombol berkonfirmasi (`data-confirm-action="job_retry:<id>"`).
- Audit `job_retry` promote terencana sebelum kirim memuat field yang sama dengan mulainya (`candidate`, `mode`, `backup_ack`, `job_id`); "Ulangi" promote sesudah titik tak kembali dicatat dengan `continuation: true`.
- **Sesudah titik tak kembali, dan untuk pekerjaan yang sudah dikirim, tidak ada yang ditambahkan:** tanpa `promoteRefusals`, tanpa gerbang, tanpa token.

- [ ] **Step 1: Test gagal.**
  - `TestPlannedRetryBeforeSendNeedsToken`: `Retry` tanpa `ack.Confirmed` → `ErrJobNeedsConfirm`; tidak ada perintah agent.
  - `TestPlannedRetryBeforeSendGated`: baris jam merah (31 detik) → `errors.Is(err, ErrGateRed)`. `TestPlannedRetryBeforeSendNeedsBackupAck`.
  - `TestPlannedRetryRunsStartRefusals` (tabel): replika lain bisa ditulis → T24; replika lain tidak terbaca → T24c; kandidat kini berhenti → teks kelayakan yang ada; agent master beda versi; master lama belum dibereskan; master kini hanya mengizinkan darurat → teks mode yang ada.
  - `TestPlannedRetryStaleAfter24h`: `ConfirmedAt` 25 jam lalu → T12 persis; pekerjaan 0.2.0 tanpa `ConfirmedAt` yang dibuat 25 jam lalu → T12; dibuat 1 jam lalu → berjalan.
  - `TestPlannedRetryStartsAtLockStep`: gagal di "Replika mengejar" → `mysql.lockForPromote` dikirim sebelum `mysql.waitSynced`; gagal di "Buat akun replikasi di kandidat" → mulai dari langkah itu.
  - `TestPlannedRetryAfterPanelRestart`: pekerjaan yang digagalkan `Resume` sebelum kirim mengikuti aturan yang sama (T12b selagi catatan berdiri; sesudah kunci terbukti dibuka: gerbang dan token).
  - `TestPromoteRetryPastPointTokenlessAndUngated` (penjaga 5.13, kedua jenis): `PastPoint`, setiap baris gerbang merah dan `gateFault` menjawab galat, `JobAck{}` kosong → pekerjaan berjalan dan `mysql.quarantine` terkirim.
  - Web: `TestGateRefusesPlannedRetryBeforeTokenIsConsumed` (POST "Ulangi" dengan token sah selagi merah → 409 G14; sesudah hijau token yang sama diterima); `TestRetryPlannedPromoteAudit`; `TestJobPagePlannedRetryIsConfirmButton`.
- [ ] **Step 2:** Jalankan; FAIL.
- [ ] **Step 3:** Implementasi. `confirmedJobAction` memakai `NeedsConfirm` untuk token dan gerbang; `DropsData` tetap hanya untuk dialog daftar database.
- [ ] **Step 4:** Dua gerbang panel dan `make test-e2e`, exit 0.
- [ ] **Step 5: Commit:** `fix(panel): a planned promote's retry before the switch needs what its start needs`.

### Task 7: Agent melaporkan "permintaan promote" di `mysql.status`

Menutup dari R-184: temuan N-1 (bukti (b) untuk keadaan "terkirim").

**Files:**
- Create: `internal/agent/drivers/mysql/promote_pending.go`, `promote_pending_test.go`
- Modify: `internal/proto/mysql.go:31-61`; `internal/agent/commands/registry.go:47-115`; `internal/agent/session.go:82-104`; `internal/agent/drivers/mysql/status.go:18-29`, `promote.go:75-150`
- Test: `internal/agent/session_test.go`, `internal/proto/mysql_test.go` (baru)

**Interfaces:**
- Produces:
  ```go
  // proto.MySQLStatus
  // PromoteRequest: this agent has read a mysql.promote request whose job
  // has not ended (true), or none (false); nil from an agent before this field.
  PromoteRequest *bool `json:"promote_request,omitempty"`

  // commands.Registry
  func (r *Registry) Track(cmd string, read func(), answered func(ok bool))
  func (r *Registry) NoteRead(cmd string)              // the session's read loop, before the next request is read
  func (r *Registry) NoteAnswered(cmd string, ok bool) // the request's goroutine, after Dispatch returned

  // mysql.Driver
  func (d *Driver) PromoteRequestPending() bool
  ```

Aturan (spek 5.2 "Arti field itu"):
- Tanda bertambah di **putaran baca** sesi (`session.go`, sebelum `requests.Go`) untuk setiap permintaan `mysql.promote` yang dibaca, sebelum permintaan berikutnya dibaca.
- Tanda berkurang tepat sekali per permintaan: saat `Dispatch` pulang dengan galat (ditolak gerbang, parameter, pin, handler), atau, bila handler mendaftarkan pekerjaan, saat fungsi pekerjaan itu berakhir (berhasil, gagal, dibatalkan; juga jalur "sudah dipromosikan" di `promote.go:95-118`).
- `mysql.status` mengisi `PromoteRequest` dengan penunjuk ke `PromoteRequestPending()`.
- Tanda hidup di proses agent, bukan di sesi: permintaan yang tiba lewat sambungan lama sudah tercatat sebelum `mysql.status` di sambungan baru dijawab.

- [ ] **Step 1: Test gagal.**
  - `TestPromoteRequestMarkedBeforeHandlerRegistersJob`: handler `mysql.promote` ditahan sebelum `Jobs.Start` (seam pada pelepasan kunci start) → `mysql.status` di sesi yang sama menjawab `promote_request: true`.
  - `TestPromoteRequestMarkSurvivesReconnect`: sesi pertama membaca `mysql.promote` (handler ditahan), sesi ditutup; sesi kedua → `mysql.status` menjawab `true`.
  - `TestPromoteRequestClearedOnRefusal` (pin replika tidak cocok; perintah ditolak mode ketat; parameter rusak) → `false`. `TestPromoteRequestClearedWhenJobEnds` (berhasil, gagal, dibatalkan) → `false` sesudah pekerjaan berakhir, `true` selama berjalan.
  - `TestPromoteRequestCountsEveryRequest`: dua permintaan, satu ditolak, satu berjalan → `true` sampai yang berjalan berakhir.
  - `TestMySQLStatusOmitsPromoteRequestWhenNil` (proto): JSON tanpa kunci itu terbaca `nil`; struct lama tanpa field itu membaca JSON baru tanpa galat.
- [ ] **Step 2:** `go test ./internal/agent/... ./internal/proto/... -run 'TestPromoteRequest|TestMySQLStatusOmits' -short`; FAIL.
- [ ] **Step 3:** Implementasi.
- [ ] **Step 4:** Tiga gerbang (agent dan proto, panel, `make test`), exit 0.
- [ ] **Step 5: Commit:** `feat(agent): mysql.status says whether a promote request has been read and not ended`.

### Task 8: Terkirim, hasil belum diketahui

Menutup dari R-184: SEC-288 dan temuan T-1, N-1, N-5, R-2, NR-3, NR-4.
Anti-tujuan yang dijaga: panel tidak pernah menyimpulkan "tidak ada yang
terjadi" tanpa bukti.

**Files:**
- Create: `internal/panel/orchestrator/promote_sent.go`, `promote_sent_test.go`; `internal/agent/drivers/mysql/promote_bound_test.go`
- Modify: `internal/panel/orchestrator/promote.go:729-816,1034-1110,1161-1179`, `agentjob.go:20-61`, `runner.go:959-1031,1620-1666,1737-1740`; `internal/panel/web/handlers_jobs.go`, `handlers_mysql.go` (`masterPage`), `web/templates/master_detail.html`, `job_detail.html`
- Test: `orchestrator/promote_test.go:359-420`, `web/web_jobs_test.go`

**Interfaces:**
- Consumes: `proto.MySQLStatus.PromoteRequest` (Task 7); `LockRecords`, `UnlockLockJob`, `UnlockPromoteLock` (Task 2, 3); `promoteUndo`; `Health.View`.
- Produces:
  ```go
  // jobState
  PromoteSentAt     *time.Time `json:"promote_sent_at,omitempty"`    // when mysql.promote was last sent
  PromoteUnanswered bool       `json:"promote_unanswered,omitempty"` // that send's start call has not been answered with a job id

  // agentPromoteJobBound is the sum of the bounds of every step of the
  // agent's mysql.promote job, from the request being read to its end; its
  // comment lists each part with file and line.
  const agentPromoteJobBound time.Duration = <ditetapkan task ini>
  // promoteUnansweredBound is bound B: max(30 minutes, 2 × agentPromoteJobBound).
  const promoteUnansweredBound time.Duration = <ditetapkan task ini>

  type sentVerdict int
  const ( sentWritable sentVerdict = iota + 1; sentOver; sentUnreadable; sentRunning )
  func (r *Runner) promoteSentVerdict(ctx context.Context, j store.Job, st jobState) (sentVerdict, time.Time /* sent + B, for T14b */)

  // PromoteSentError is "Ulangi" of a sent promote that could not continue.
  type PromoteSentError struct {
      Code      string // over_emergency (T13) | over_planned (T13b) | unreadable (T14) | running (T14b) | changed (P6)
      Candidate string
      Over      time.Time // running: when the panel takes the agent job as ended
      Tail      string    // "" | planned_locked (T14t, Master) | old_master_locked (T14u, Master)
      Master    string
  }
  func (e *PromoteSentError) Error() string
  ```

Langkah pertama task ini adalah pemeriksaan, bukan kode: **setiap langkah
pekerjaan `mysql.promote` di agent harus berbatas waktu.** Yang terbaca
penulis rencana pada head: sambungan admin berbatas 5 detik untuk
menyambung dan 10 menit per pernyataan (`conn.go:52-54`); tiga probe master
lama berjarak 10 detik dengan batas 5 detik masing-masing, lalu satu
pembacaan 5 detik (`promote.go:51,58,387-423`); `STOP REPLICA IO_THREAD`,
`START REPLICA SQL_THREAD`, tunggu relay log 600 detik
(`promote.go:63,493-589`); pembacaan ulang master lama 5 detik; `STOP
REPLICA` dan dua `SET PERSIST` (`promote.go:355-379`); `finishPromote`
(`promote.go:632-680`). Pekerjaan itu sendiri tidak punya batas
(`jobs/runner.go:179` hanya `WithCancel`). **Aturan berhenti:** bila ada
langkah yang tidak dibatasi apa pun (bukan oleh konteks, bukan oleh batas
baca sambungan), task berhenti dan melapor ke pengendali sebelum menulis
kode lain.

Aturan (spek 5.2, baris 5, 6 dan 7 tabel):
- Sebelum **setiap** pengiriman `mysql.promote`: id pekerjaan agent tersimpan dihapus (`AgentJobID`, `AgentCmd`, `AgentServerID`), `PromoteSent = true`, `PromoteSentAt = now`, `PromoteUnanswered = true`, disimpan; `PromoteUnanswered` dihapus hanya oleh jawaban mulai yang membawa id pekerjaan. Field memori `promoteStartUnknown` hilang; `promoteFailed` memakai tanda tersimpan.
- Pekerjaan 0.2.0 dengan `PromoteSent`, tanpa `PromoteSentAt`: dihitung tidak berjawab bila tidak memegang id pekerjaan `mysql.promote`, dengan waktu kirim menurut K17.
- `promoteSentVerdict` membaca kandidat (`mysql.status`, paling lama 10 detik):
  - tidak terbaca → `sentUnreadable`;
  - `ReadOnly == false && SuperReadOnly == false` → `sentWritable`;
  - baca-saja dan `PromoteRequest` benar → `sentRunning` (mengalahkan semua bukti lain);
  - baca-saja dan `PromoteRequest` salah → `sentOver`;
  - baca-saja, field tidak ada, panel memegang id pekerjaan: `job.status` berkata berakhir atau agent tidak mengenalnya (`CodeBadParams`) → `sentOver`; `running` atau tidak bisa dibaca → `sentRunning`;
  - baca-saja, field tidak ada, tidak berjawab: `0 <= now − PromoteSentAt` dan `now − PromoteSentAt >= promoteUnansweredBound` → `sentOver`; selain itu `sentRunning`.
  `mysql.status` yang berkata baca-saja tidak pernah, sendirian, menjadi bukti.
- `Retry` pekerjaan promote (kedua jenis) dengan `PromoteSent && !PastPoint`: putusan dibaca di luar `startMu`, lalu keadaan pekerjaan diperiksa ulang di bawahnya (berubah → **P6**, tidak ada di spek: `Keadaan pekerjaan ini berubah selagi panel memeriksa kandidat; muat ulang halaman ini.`).
  - `sentWritable` → `PastPoint = true`, lanjut tanpa token dan tanpa gerbang;
  - `sentOver` → `PromoteSent = false`, promosi `failed`, pekerjaan `failed`; promote terencana: buka kunci lewat jalur `promoteUndo`; jawabannya **T13** (darurat) atau **T13b** (terencana);
  - `sentUnreadable` → **T14**; `sentRunning` → **T14b** dengan pukul `PromoteSentAt + B`; tidak ada yang diubah, promosi tetap `partial`.
- Ekor T14 dan T14b (K19): pekerjaan terencana yang catatan kuncinya berdiri → **T14t**; pekerjaan darurat yang master lamanya tersambung dan berstatus "Terkunci" → **T14u**.
- `promoteFailed` (di dalam satu percobaan) memakai `promoteSentVerdict` yang sama sebagai ganti `promoteJobMayRun`.
- Halaman master: bila `UnlockLockJob` menjawab `sent`, halaman menampilkan T14 dengan ekor T14t dan tautan ke pekerjaannya, tanpa tombol **Buka kunci promote**; **Buka kunci master** tetap tampil.

- [ ] **Step 1: Periksa batas pekerjaan agent** menurut paragraf di atas; tulis tabelnya (langkah, batas, berkas:baris) di "Perubahan selama pelaksanaan" dan tetapkan kedua konstanta. Berhenti dan lapor bila ada langkah tanpa batas.
- [ ] **Step 2: Test gagal.**
  - `TestPromoteSendClearsOldAgentJobAndMarksUnanswered`: keadaan sebelum kirim memegang `AgentJobID` percobaan lama; saat handler `mysql.promote` palsu dipanggil, params tersimpan sudah tanpa id itu, `PromoteUnanswered` benar dan `PromoteSentAt` terisi; sesudah jawaban membawa id, `PromoteUnanswered` salah.
  - `TestRetrySentUnansweredReadOnlyWithinBoundRefused` (penjaga 5.13): panggilan mulai tidak berjawab, kandidat baca-saja tanpa field (agent 0.2.0), "Ulangi" pada kirim + B − 1 menit → T14b dengan pukul kirim + B; promosi tetap `partial`; kandidat hanya menerima `mysql.status`; promote darurat ke replika lain tetap ditolak.
  - `TestRetrySentUnansweredAfterBoundFails`: pada kirim + B → pekerjaan `failed`, T13 (darurat); terencana → T13b dan `mysql.unlockPromote` terkirim.
  - `TestRetrySentFieldNoneFails` (field salah, di dalam B → digagalkan); `TestRetrySentFieldPendingBeatsJobStatus` (panel memegang id, `job.status` berkata berakhir, field benar → T14b, tidak ada yang berubah); `TestRetrySentJobStatusEndedFails`.
  - `TestRetrySentCandidateWritableContinues`: tanpa token, gerbang merah → `PastPoint`, karantina terkirim.
  - `TestRetrySentCandidateUnreadable`: T14; terencana dengan catatan berdiri → ekor T14t persis; keadaan majemuk (promote terencana gagal dengan kunci dipertahankan, lalu promote darurat terkirim dan kandidatnya diam, master lama tersambung dan "Terkunci") → pekerjaan darurat T14 + T14u, dan `UnlockPromoteLock` pekerjaan terencana lama → T11t.
  - `TestOldJobSentWithoutAgentJobCountsUnanswered`: params 0.2.0 (`promote_sent` benar, tanpa `agent_job_id`), `FinishedAt` = t0 → batas dihitung dari t0.
  - `TestRetryAfterPanelRestartKeepsUnanswered`: mulai tidak berjawab → `partial`; Runner baru di atas store yang sama (`Resume`) → "Ulangi" di dalam B → T14b.
  - `TestUnansweredBoundClockSetBack`: `PromoteSentAt` di masa depan → `sentRunning`.
  - `TestPromoteUnansweredBoundCoversAgentJob`: `promoteUnansweredBound >= 30*time.Minute` dan `>= 2*agentPromoteJobBound`. Sisi agent, `TestPromoteJobBoundParts` (`promote_bound_test.go`): nilai yang dijumlahkan (`emergencyProbes`, `probeSpacing`, `peerTimeout`, `relayWaitSec`, batas baca sambungan admin) sama dengan yang tertulis di komentar konstanta panel.
  - Web: `TestMasterPageShowsT14ForSentJob` (tanpa tombol **Buka kunci promote**); `TestRetrySentRefusalShownOnJobPage`.
- [ ] **Step 3:** Jalankan; FAIL.
- [ ] **Step 4:** Implementasi.
- [ ] **Step 5:** Tiga gerbang dan `make test-e2e`, exit 0.
- [ ] **Step 6: Commit:** `fix(panel): settle a sent promote from evidence, never from a read-only status alone`.

### Task 9: Jendela kata sandi

Dasar R-182, R-183 dan R-191 (keputusan pemilik 2026-11-27, pilihan 2;
temuan S-5). Belum ada tindakan yang memakainya sampai Task 10.

**Files:**
- Create: `internal/panel/store/migrations/0014_confirm_window.sql`
- Modify: `internal/panel/auth/session.go:10-89`; `internal/panel/store/confirmations.go:13-52`; `internal/panel/web/handlers_confirm.go:21-99,131-246`, `handlers_auth.go:104-110`; `web/static/app.js:366-504`; `web/templates/_confirm.html`
- Test: `internal/panel/auth/auth_test.go`, `internal/panel/store/confirmations_test.go`, `internal/panel/web/web_confirm_test.go`

**Interfaces:**
- Consumes: `auth.TokenFrom`, anggaran per sesi (Task 1); `NeedsConfirm` (Task 6).
- Produces:
  ```go
  // auth; Create records the login as a proved password.
  func (s *Sessions) MarkVerified(token string)
  func (s *Sessions) WindowOpen(token string, window time.Duration) bool // 0 <= now-verified < window; changes nothing

  // store; column confirmations.via_window INTEGER NOT NULL DEFAULT 0 (K20)
  // Confirmation.ViaWindow bool
  func (s *Store) ConsumeConfirmation(tokenHash, username, action, targetKind string, targetID int64, now time.Time, allowWindow bool) error

  // web
  const passwordWindow = 10 * time.Minute
  const (
      ActRepairToken   = "repair_token"      // server (paired)
      ActMasterSource  = "master_source"     // master
      ActPrepareMaster = "prepare_master"    // server: the first "Siapkan sebagai master"
      ActConnectMySQL  = "connect_mysql"     // server with a stored account
      ActStopReplica   = "stop_replica"      // replica
      ActTelegram      = "telegram_settings" // settings, target id 0
  )
  var lightActions []string // the six above
  func (a *app) requireConfirm(w http.ResponseWriter, r *http.Request, action, targetKind string, targetID int64) bool      // heavy: refuses a window token
  func (a *app) requireConfirmLight(w http.ResponseWriter, r *http.Request, action, targetKind string, targetID int64) bool // light: takes either
  ```
  `POST /confirm` tanpa kata sandi menjawab 401 `{"error": <P8>, "need_password": true}`; **P8** (tidak ada di spek): `Kata sandi panel diperlukan.`

Aturan (spek 5.1 "Jendela kata sandi"):
- Waktu "kata sandi terakhir terbukti benar" ada di catatan sesi: diisi saat login (`Create`) dan setiap kali `issueConfirm` menerima kata sandi benar (`MarkVerified`), untuk tindakan berat maupun ringan. Memakai jendela tidak mengubahnya.
- `POST /confirm` dengan isian `password` kosong: tindakan ringan atau tindakan pekerjaan (`job_retry:`, `job_approve:`) dan jendela terbuka → token terbit dengan `ViaWindow`, baris audit `confirm_issued` memuat `window: true`; selain itu 401 `need_password`. Tindakan berat tanpa kata sandi selalu 401. Jawaban 401 ini tidak memakai anggaran percobaan.
- `requireConfirm` (semua pemanggil yang ada) memanggil `ConsumeConfirmation` dengan `allowWindow` salah; token jendela ditolak **saat dipakai** dan tidak hangus oleh penolakan itu. Token pekerjaan: `confirmedJobAction` memakai `requireConfirm` bila `NeedsConfirm` benar.
- Dialog (`app.js`): tombol ber-`data-confirm-light="1"` membuka dialog dengan teksnya, tanpa isian kata sandi; kirim pertama tanpa kata sandi; jawaban 401 `need_password` menampilkan isian kata sandi dengan kalimat **T2**, dan kirim berikutnya membawa kata sandi. Tombol lama (tanpa atribut itu) tidak berubah.
- Jendela hilang bersama catatan sesinya: logout, sesi berakhir, panel restart, `reset-password`.

- [ ] **Step 1: Test gagal.**
  - `TestSessionWindow` (auth, jam palsu): terbuka sesudah `Create`; tertutup tepat pada 10 menit; `WindowOpen` berulang tidak memperpanjang; `MarkVerified` membukanya lagi; token tidak dikenal → salah. `TestWindowClockSetBack`: jam dimundurkan 1 jam sesudah `MarkVerified` → tertutup.
  - `TestConfirmationViaWindow` (store): migrasi memberi 0 pada baris lama; `ConsumeConfirmation(..., false)` atas token jendela → `ErrConfirmInvalid` dan `used_at` tetap kosong; dengan `true` → dipakai.
  - `TestWindowIssuesLightTokenWithoutPassword`: login; `POST /confirm` tindakan `stop_replica` tanpa kata sandi → 200; audit `confirm_issued` memuat `window: true`.
  - `TestWindowClosedAsksPassword`: jam + 10 menit → 401 dengan `need_password`; dengan kata sandi → 200 dan permintaan berikutnya tanpa kata sandi → 200. `TestWindowNotExtendedByUse`: dipakai pada menit ke-9; pada menit ke-10 → 401.
  - `TestHeavyNeverIssuedWithoutPassword`: tindakan `promote`, `resync`, `unlock_master`, `cert_upload` tanpa kata sandi di dalam jendela → 401; tidak ada baris `confirmations`.
  - `TestWindowTokenRefusedByHeavyAction` (penjaga 5.13): token jendela untuk `job_retry:<id>` sebuah resync gagal → `POST /jobs/{id}/retry` 403, pekerjaan tidak berubah; sama untuk `job_approve` pekerjaan yang membuang data dan untuk "Ulangi" promote terencana sebelum kirim; token jendela yang disisipkan untuk `promote` → `POST /masters/{id}/promote` 403.
  - `TestWindowIsPerSession`: dua sesi admin yang sama; sesi A baru mengetik kata sandi, sesi B login 11 menit lalu → B mendapat 401.
  - `TestWindowGoneAfterLogout`; `TestNeedPasswordDoesNotUseBudget` (enam jawaban 401 `need_password` berturut-turut, lalu kata sandi benar → 200).
- [ ] **Step 2:** Jalankan; FAIL.
- [ ] **Step 3:** Implementasi. Skenario dialog di browser diuji di Task 24.
- [ ] **Step 4:** Dua gerbang panel dan `make test-e2e`, exit 0.
- [ ] **Step 5: Commit:** `feat(panel): a ten-minute password window on the session for light actions`.

### Task 10: Pasang ulang agent

Menutup R-182 (A5b2 SEC-238; temuan S-6).

**Files:**
- Modify: `internal/panel/web/handlers_servers.go:24,596-613,761-771,836-853,887-911`; `web/templates/server_detail.html:22,44-58,166-178`, `servers.html:32-34`; `internal/panel/agentapi/pair.go:66-189,205-216`
- Test: `internal/panel/web/web_test.go:702-750` (`TestRepairPairedServer` ditulis ulang), `web_servers_test.go`, `internal/panel/agentapi/agentapi_test.go`

**Interfaces:**
- Consumes: `requireConfirmLight`, `ActRepairToken` (Task 9).
- Produces: `const repairTokenTTL = 15 * time.Minute`; `func boundHostname(s string) string` (255 rune, satu baris, tanpa karakter kendali) di `agentapi`; baris audit `agent_pair` dengan hasil `ok` dan detail `replaced` (bool), `cert_serial`, `agent_version`, `arch`, `hostname`.

Aturan (spek 5.1 "Pasang ulang agent"):
- `POST /servers/{id}/token` pada server yang sudah ter-pairing: `requireConfirmLight(ActRepairToken, auditServer, id)` lebih dulu; token berlaku 15 menit; jawabannya halaman server (200) yang memuat perintah pemasangan **sekali**. GET halaman server dan daftar server tidak pernah menyusun perintah untuk server yang sudah ter-pairing: selagi token aktif hanya **T3b** dengan pukul berakhirnya.
- Tombol **Pasang ulang agent** dan **Buat token baru** pada server ter-pairing menjadi tombol berkonfirmasi ringan dengan teks **T3**.
- Server yang belum ter-pairing: tidak berubah (tanpa kata sandi, 24 jam, perintah tampil di GET).
- Pairing yang berhasil menulis satu baris audit `agent_pair`; `replaced` dibaca dari `Paired` sebelum `MarkPaired`. `hostname` dipotong `boundHostname` di baris itu, di baris penolakan (`auditPairRefused`) dan di ketiga baris journal `pair.go:106,133,170`; `agent_version` dan `arch` dipotong 64 rune.

- [ ] **Step 1: Test gagal.**
  - `TestRepairTokenNeedsConfirm`: tanpa token → 403, `ActivePairToken` tidak menemukan apa pun; dengan token jendela → 200.
  - `TestRepairCommandShownOnce`: badan jawaban POST memuat `agent/install?token=`; GET berikutnya tidak, dan memuat T3b dengan pukul = terbit + 15 menit. `TestServerListShowsNoRepairCommand`.
  - `TestUnpairedTokenStillPasswordless`: POST tanpa token → 303; GET menampilkan perintah; berlaku 24 jam.
  - `TestPairWritesAuditRow`: satu baris per pairing; `replaced` salah untuk pairing pertama dan benar untuk pasang ulang; nomor seri sama dengan sertifikat yang terbit.
  - `TestPairHostnameBounded`: `hostname` 60 KiB dengan baris baru → 255 rune satu baris di audit dan di journal; baris penolakan sama.
  - `TestPairOldAgentRequest`: permintaan tanpa `arch` dan `agent_version` (bentuk 0.2.0) tetap berpasangan.
- [ ] **Step 2:** Jalankan; FAIL.
- [ ] **Step 3:** Implementasi.
- [ ] **Step 4:** Dua gerbang panel dan `make test-e2e`, exit 0.
- [ ] **Step 5: Commit:** `fix(panel): re-pairing asks for the password, shows its command once and audits the pairing`.

### Task 11: Alamat master untuk replika

Menutup R-183 (A5b2 SEC-239, SEC-253, SEC-259).

**Files:**
- Create: `internal/panel/store/migrations/0015_topology_source_change.sql`
- Modify: `internal/panel/store/topologies.go:12-60,213-217,453-`; `internal/panel/orchestrator/promote.go:869-880` (`promoteSwap`), `gate_port.go:40-87`, `gate.go:631-657`; `internal/panel/web/handlers_mysql.go:498-536,576-589`; `web/templates/master_detail.html:40-53` dan form alamat; `web/static/app.js` (`data-confirm-value-from`)
- Test: `internal/panel/store/topologies_test.go`, `orchestrator/gate_port_test.go`, `web/web_mysql_test.go`

**Interfaces:**
- Consumes: `requireConfirmLight`, `ActMasterSource` (Task 9).
- Produces:
  ```go
  // store.Topology: SourceChangedBy string ("" when never recorded); SourceChangedAt *time.Time
  func (s *Store) SetTopologySource(id int64, host string, port int, by string, at time.Time) error
  func (s *Store) SwapMaster(topologyID, oldServerID, newServerID int64, newHost string, newPort int, by string, at time.Time) error

  // orchestrator: open is the last answer kept for the master; waitUntil is
  // set when hostPort differs from the address last probed and the master's
  // ten-minute slot has not passed.
  func (r *Runner) masterPortOpen(masterID int64, hostPort string) (open bool, waitUntil time.Time)
  ```

Aturan (spek 5.1 "Alamat master"):
- `setMasterSource` memanggil `requireConfirmLight(ActMasterSource, auditMaster, id)` sebelum pemeriksaan lain; audit `master_source` memuat `old_source_host`, `old_source_port` dan nilai baru. Tombol simpan menjadi tombol berkonfirmasi ringan dengan teks **T4** (K24).
- `SetTopologySource` mencatat nama pengguna dan waktunya; `promoteSwap` mencatat `job:<id>` (K25).
- Spanduk "Arahkan agent replika" memuat **T5** (diubah orang) atau **T5b** (dipasang promote); kolom kosong (pemasangan lama) → tanpa kalimat itu. Waktu ditulis `02-01-2006 15:04`.
- Pemeriksaan port: paling banyak satu sambungan per master per 10 menit, apa pun alamatnya. Alamat yang berbeda dari yang terakhir diperiksa, di dalam selang itu, tidak disambungi: baris "Port MySQL master" kuning dengan **T6** (pukul = pemeriksaan terakhir + 10 menit), tanpa perbaikan.

- [ ] **Step 1: Test gagal.**
  - `TestMigration0015NullableColumns`: topologi lama terbaca dengan `SourceChangedBy == ""` dan `SourceChangedAt == nil`.
  - `TestMasterSourceNeedsConfirm`: tanpa token → 403 dan alamat tersimpan tidak berubah. `TestMasterSourceAuditOldAndNew`.
  - `TestMasterSourceBanner`: sesudah diubah `admin` → T5 persis dengan nama, waktu dan nama master; sesudah `promoteSwap` → T5b dengan id pekerjaan; kolom kosong → spanduk tanpa kedua kalimat.
  - `TestPortProbeOncePerMasterPerTenMinutes`: dua pembacaan gerbang dengan dua alamat dalam 10 menit jam palsu → `Deps.PortProbe` dipanggil sekali; baris kedua kuning dengan T6; sesudah 10 menit alamat baru diperiksa.
  - `TestPortRowNeverRed`.
- [ ] **Step 2:** Jalankan; FAIL.
- [ ] **Step 3:** Implementasi; pemanggil `SwapMaster` dan `SetTopologySource` di test ikut disesuaikan.
- [ ] **Step 4:** Dua gerbang panel, exit 0.
- [ ] **Step 5: Commit:** `fix(panel): changing the master address asks for the password and says who changed it`.

### Task 12: Tindakan ringan lain

Menutup dari R-191: tindakan yang mengganggu layanan tanpa kata sandi (A5b2
SEC-248, SEC-255; temuan S-8).

**Files:**
- Modify: `internal/panel/web/handlers_jobs.go:463-540`, `handlers_mysql.go:267-320,325-362`, `handlers_controls.go:73-100`, `handlers_settings.go:357-447`; `web/templates/server_detail.html:275,310,349`, `replica_detail.html:134-141`, `settings.html:8-40`, `job_detail.html` (tombol Setujui restart); `web/static/app.js` (`data-confirm-when-changed`)
- Test: `internal/panel/web/web_controls_test.go`, `web_mysql_test.go`, `web_settings_test.go`, `web_jobs_test.go`

**Interfaces:**
- Consumes: `requireConfirmLight`, `ActPrepareMaster`, `ActConnectMySQL`, `ActStopReplica`, `ActTelegram`, `jobConfirmAction(ActJobApprove, id)` (Task 9).
- Produces: tidak ada simbol baru untuk task lain.

Aturan (tabel 5.1 spek; masing-masing memanggil `requireConfirmLight` sebelum pemeriksaan lain):
- **Setujui restart MySQL master**: `approveJob` pekerjaan `prepare_master` yang menunggu dengan `WaitReason == WaitRestart` → token `job_approve:<id>` (ringan).
- **Siapkan sebagai master pertama**: `prepareMaster` pada server tanpa topologi → `ActPrepareMaster`. "Siapkan master lagi" tetap berat (`ActPrepareMasterAgain`).
- **Hubungkan MySQL** pada server yang inventarisnya sudah `Connected` → `ActConnectMySQL`; hubungan pertama tidak diminta.
- **Stop replika** → `ActStopReplica`, dialog **T30**.
- **Simpan Pengaturan** → `ActTelegram` (sasaran pengaturan, id 0), dialog **T31**, hanya bila token bot diisi, chat id berubah, atau sebuah jenis peringatan yang tadinya hidup dimatikan. Simpanan lain lewat tanpa token.
- **Tidak berubah, dan dijaga test:** Jangan ganggu (senyap), **Batalkan**, **Sudah saya alihkan**, Jalankan dan Kunci kembali replika, Pengaturan lain, Setujui pekerjaan lain yang tidak membuang data (K23).

- [ ] **Step 1: Test gagal.**
  - `TestApproveMasterRestartNeedsConfirm`, `TestFirstPrepareMasterNeedsConfirm`, `TestStopReplicaNeedsConfirm`: tanpa token → 403 dan orchestrator palsu tidak dipanggil; dengan token jendela → berjalan.
  - `TestReconnectMySQLNeedsConfirmOnlyWithStoredAccount`: inventaris `Connected` → 403 tanpa token; server yang belum pernah terhubung → berjalan tanpa token.
  - `TestTelegramChangeNeedsConfirm` (tabel): token bot diisi; chat id berubah; satu jenis dimatikan → 403 tanpa token dan tidak ada yang tersimpan. `TestOtherSettingsSaveNeedsNoConfirm`: hanya ambang berubah; hanya sebuah jenis dihidupkan → 303.
  - `TestOutagePathStaysTokenless`: `POST` senyap, Batalkan pekerjaan, Sudah saya alihkan tanpa token → tidak 403.
  - `TestLightButtonsCarryDialogTexts`: T30 dan T31 ada di atribut tombolnya; tombolnya ber-`data-confirm-light="1"`.
- [ ] **Step 2:** Jalankan; FAIL.
- [ ] **Step 3:** Implementasi. Pembantu e2e (`e2e_helpers_test.go`: Hubungkan MySQL, Siapkan master, Setujui restart) mengambil token lewat `uiClient.confirmToken` di dalam jendela.
- [ ] **Step 4:** Dua gerbang panel dan `make test-e2e`, exit 0.
- [ ] **Step 5: Commit:** `fix(panel): light actions that disturb a service ask for the password within the window`.

### Task 13: Masukan gerbang dan audit

Menutup dari R-191: A5b2 SEC-260 dan SEC-274 (ambang disk, centang cadangan,
nilai di audit). Penanda generasi folder cadangan **tidak** ada di sini
(bagian B): centang T8 masih bisa dihindari dengan waktu, dan laporan
berhenti bagian A menyebutnya.

**Files:**
- Modify: `internal/panel/orchestrator/gate.go:493-509,572-601`; `internal/panel/web/handlers_settings.go:419-447,463-495`, `backup_folder.go:162-200`, `audit.go:42,204-215`, `handlers_promote.go:283-358`
- Test: `orchestrator/gate_test.go`, `web/web_settings_test.go`, `web/backup_folder_test.go`, `web/web_promote_test.go`, `web/web_audit_test.go`

**Interfaces:**
- Consumes: `Gate`, `GateRow.NeedsAck`, `monitor.JudgeBackup`.
- Produces: `const gateDiskCeiling = 95`. Detail audit `settings_update`: `changes` = peta kunci → `{"old": …, "new": …}` (token bot: `"diubah"` saja). Detail `server_backup`: `old_backup_dir`, `backup_dir` (utuh sampai 1024 byte), `old_max_age_hours`, `max_age_hours`. Detail `promote`: `master_report_received` (RFC 3339 UTC waktu panel menerima laporan master terakhir; `""` bila tidak ada) di samping `master_last_report`.

Aturan (spek 5.1 "Masukan gerbang dan audit"):
- Baris "Ruang disk" merah pada `pct >= min(DiskCrit, 95)`; bila `DiskCrit > 95`, `Detail` baris itu ditambah **T7**. Peringatan disk tetap memakai ambang yang diatur.
- Baris cadangan dengan folder terisi dan keadaan `unknown` (termasuk `pending`): tetap kuning, `Detail` **T8**, `NeedsAck` pada `promote_planned` dan `rebuild_old_master`.
- Promote darurat tidak ditahan baris mana pun.

- [ ] **Step 1: Test gagal.**
  - `TestGateDiskCeiling`: ambang 99, disk 96% → merah dengan T7 (`%d` = 99); ambang 99, disk 94% → hijau; ambang 90, disk 92% → merah tanpa T7.
  - `TestBackupAckOnUnknownFolderState`: folder terisi, agent tersambung, laporan `pending` atau tanpa keadaan cadangan → `NeedsBackupAck()` benar pada promote terencana dan bangun ulang, salah pada Buat replika; `Detail` = T8.
  - `TestSettingsAuditOldAndNew`; `TestSettingsAuditSecretSaysOnlyChanged` (token bot tidak pernah muncul).
  - `TestBackupDirAuditedWhole`: path 1000 byte utuh, lama dan baru.
  - `TestPromoteAuditReportReceived`.
  - `TestEmergencyNotRefusedByChangedRows`: disk 96% dengan ambang 99 dan cadangan `pending` → promote darurat dimulai.
- [ ] **Step 2:** Jalankan; FAIL.
- [ ] **Step 3:** Implementasi.
- [ ] **Step 4:** Dua gerbang panel, exit 0.
- [ ] **Step 5: Commit:** `fix(panel): the disk row has a ceiling, an unknown backup folder asks for the tick, audits carry old and new values`.

### Task 14: Kunci per server dan karantina tertunda

Menutup R-185 (A5b2 SEC-241; temuan T-5, P4, NR-1).

**Files:**
- Create: `internal/panel/orchestrator/quarantine_retry.go`, `quarantine_retry_test.go`
- Modify: `internal/panel/orchestrator/runner.go:186-225` (`Runner`, `New`, `Close`), `promote.go:46-47,838-840,933-935,1266-1289,1419-1431,1451-1570,1616-1695`; `internal/panel/web/handlers_oldmaster.go` (kotak Master lama), `web/templates/server_detail.html`
- Test: `orchestrator/promote_test.go`, `web/web_oldmaster_test.go`

**Interfaces:**
- Consumes: `sendQuarantine`, `repointReplica`, `QuarantinePending`, `PromotionByOldMaster`, `hub.SentByAgent`.
- Produces:
  ```go
  // lockTarget serialises mysql.quarantine and mysql.repoint sent to one
  // server; it replaces repointMu. The entry is dropped when nobody holds it.
  func (r *Runner) lockTarget(serverID int64) (unlock func())

  // pendingQuarantine is the send a quarantined server with a pending
  // quarantine is owed, decided from the store alone (onOnline's rule).
  func (r *Runner) pendingQuarantine(serverID int64) (send func(ctx context.Context) error, ok bool)

  const (
      onlineSendTimeout  = 5 * time.Minute // a fresh bound for each send, taken after the lock
      quarantineRetryMin = 60 * time.Second
      quarantineRetryMax = 5 * time.Minute
  )

  // PromotionServer gains, for the old master:
  //   TriedAt *time.Time `json:"tried_at,omitempty"` // last send
  //   Refused bool       `json:"refused,omitempty"`  // the agent answered that send with a refusal (Detail holds its text)
  ```

Aturan (spek 5.3):
- `onOnline` membaca store lebih dulu dan pulang **tanpa kunci** bila tidak ada yang perlu dikirim ke server itu.
- `repointMu` hilang; setiap pengirim `mysql.quarantine` atau `mysql.repoint` (langkah pekerjaan, `RepointNow`, `onOnline`, putaran) memegang `lockTarget(server sasaran)`.
- Setiap pengiriman dari `onOnline` dan dari putaran memakai konteks baru `onlineSendTimeout` sesudah kunci diperoleh.
- Putaran coba-ulang (satu goroutine Runner, berakhir di `Close`): untuk setiap server berperan `quarantined` yang karantina promosinya tertunda dan agentnya tersambung, kirim lagi. Keputusannya `pendingQuarantine` (atas nama master topologi yang sekarang; topologi tidak terbaca atau tanpa master lain → tidak mengirim, coba lagi nanti). Selang: K26. **Penolakan tidak pernah menghentikan putaran**, termasuk jawaban "sibuk". Putaran untuk sebuah server berakhir hanya bila karantina mendarat, server dihapus, atau perannya bukan lagi `quarantined`.
- Kotak Master lama yang karantinanya tertunda menampilkan **T18**; kalimat terakhirnya hanya bila `Refused`, dengan teks agent berlabel dan dibatasi.

- [ ] **Step 1: Test gagal** (semuanya juga dengan `-race`).
  - `TestPendingQuarantineNotHeldBySilentAgent` (penjaga 5.13): agent X (replika `needs_repoint`) menahan `mysql.repoint` tanpa jawaban; M dengan karantina tertunda tersambung → `mysql.quarantine` sampai di M dalam 1 detik.
  - `TestOnOnlineTakesNoLockWithoutWork`: replika sehat tersambung → `lockTarget` tidak dipanggil (seam penghitung).
  - `TestQuarantineRetriedAfterRefusal` (penjaga 5.13): M menolak ("master baru … tidak bisa dibaca"), lalu menerima → mendarat pada putaran berikutnya tanpa sambung ulang; selang sesudah penolakan 5 menit (jam palsu); jawaban "agent sedang sibuk" diperlakukan sama.
  - `TestQuarantineRetryNeverStops`: sepuluh penolakan berturut-turut → putaran kesebelas tetap dijadwalkan.
  - `TestQuarantineRetryBacksOffWithoutAnswer`: batas waktu → 60 detik, 120 detik, 240 detik, 300 detik, 300 detik.
  - `TestQuarantineRetryUsesCurrentMaster` (topologi promote lagi sejak itu); `TestQuarantineRetryEndsWhenRoleChangesOrServerDeleted`.
  - `TestOnlineSendHasFreshTimeout`: kunci diperoleh sesudah 4 menit menunggu → panggilan masih punya batas penuh.
  - Web: `TestOldMasterBoxShowsLastAttempt` (T18 dengan pukul; kalimat terakhir hanya bila `Refused`; teks agent ter-escape).
- [ ] **Step 2:** Jalankan dengan `-race`; FAIL.
- [ ] **Step 3:** Implementasi.
- [ ] **Step 4:** Dua gerbang panel dan `make test-e2e`, exit 0.
- [ ] **Step 5: Commit:** `fix(panel): lock quarantine and repoint per server and retry a pending quarantine`.

### Task 15: R-188: ukur dulu

Menutup dari R-188: sel "tidak diketahui" F24 (A5b2 SEC-247) dan bukti bahwa
test restart menangkap kerusakannya. **Tidak ada kode produk yang berubah di
task ini.**

**Files:**
- Create: `internal/agent/drivers/mysql/readonly_restart_test.go`
- Modify: bagian "Catatan hasil ukur R-188" di rencana ini
- Test: berkas itu sendiri (harness `testmysql`: `Instance.Restart`, `Instance.ConfigFile`)

**Interfaces:**
- Consumes (kode head, tanpa diubah): `unlockMaster` (`promote_lock.go:503-510`), `ensureLocked` (`:603-619`), `persistReadOnly` (`quarantine.go:211-218`), `setReadOnly` (`configure.go:609-630`), dua pernyataan promote (`promote.go:374-379`), `persistedReadOnlyOn` (`startlock_guard.go:235-257`), CLI `lock-master` dan `unlock-master` lewat `RunCLIWith`.
- Produces: tipe test `flagState{RO, SRO bool; PersistedRO, PersistedSRO string}` (`""` berarti tidak tersimpan) dan pembantu `readFlags(t, inst) flagState`, `restartAndRead(t, inst) flagState`, dipakai Task 16 dan 19.

Tabel `TestReadOnlyAcrossRestart` (tiap langkah membaca nilai berjalan dan
`performance_schema.persisted_variables`; "OF" = baris `read_only=ON` dan
`super_read_only=ON` yang ditulis ke `Instance.ConfigFile`):

| Baris | Urutan | Wajib sesudah restart terakhir |
|---|---|---|
| premis-1 | tersimpan OFF/OFF di atas OF ON/ON → restart | OFF/OFF |
| premis-2 | tersimpan ON/ON tanpa OF → restart | ON/ON |
| premis-3 | tersimpan ON/ON di atas OF ON/ON → restart | ON/ON |
| premis-4 | tersimpan OFF/OFF tanpa OF → restart | OFF/OFF |
| jenis1-kunci | master polos → kunci (jalur kunci promote; lalu jalur `lock-master`) → restart | ON/ON |
| jenis1-buka | lanjutan: buka (jalur `unlockPromote`; lalu jalur CLI) → restart | OFF/OFF |
| jenis2-kunci | replika (OF ON/ON, `setReadOnly(true)`) → dua pernyataan promote → kunci → **restart selagi terkunci**, dibaca sebelum penjaga bekerja | ON/ON |
| jenis2-buka | lanjutan: buka → restart | OFF/OFF |
| jenis3-dari-1 | master polos → `persistReadOnly` (karantina) → buka (jalur `unlock-master`) → restart | OFF/OFF |
| jenis3-dari-2 | master jenis 2 → `persistReadOnly` → buka → restart | OFF/OFF |

Dua pertanyaan yang juga dijawab dan dicatat (bagian 6 spek): apakah `SET
PERSIST super_read_only = ON` ikut menyimpan `read_only`; dan apakah
`setReadOnly(false)` diterima saat hanya `read_only` yang ON.

**Aturan berhenti.** Bila salah satu baris **premis** tidak memberi nilai
wajibnya di head, perbaikan yang dipilih (menyimpan kedua bendera secara
eksplisit) tidak menentukan keadaan start: task berhenti, melapor ke
pengendali, dan Task 16 sampai 19 tidak dikerjakan. Bila baris
`jenis2-kunci` menunjukkan server menyala bisa ditulis di head, itu dicatat:
Task 24 menambah kalimat ke catatan pembaruan ("jangan me-restart MySQL
master selama promote terencana sampai agentnya diperbarui").

- [ ] **Step 1: Tulis test** dengan penegasan nilai **wajib** di setiap baris.
- [ ] **Step 2:** `go test ./internal/agent/drivers/mysql -run TestReadOnlyAcrossRestart -v -timeout 30m`. Yang diharapkan: baris premis LULUS; setidaknya `jenis3-dari-1` dan `jenis3-dari-2` GAGAL (F24). Salin keluaran tiap baris (nilai berjalan dan tersimpan sesudah tiap langkah) ke "Catatan hasil ukur R-188". Terapkan aturan berhenti.
- [ ] **Step 3:** Untuk baris yang gagal di head, ganti penegasannya dengan nilai yang **terukur** (kolom `head` di tabel test, dengan komentar `// measured at 8caee75; Task 16 asserts the required state`), supaya gerbang hijau (K28).
- [ ] **Step 4:** `go test ./internal/agent/drivers/mysql -run TestReadOnlyAcrossRestart -timeout 30m`; PASS. Lalu tiga gerbang, exit 0.
- [ ] **Step 5: Commit:** `test(agent): measure read-only flags across a MySQL restart for each kind of master`.

### Task 16: R-188: satu pasang fungsi kunci dan buka

Menutup dari R-188: A5b2 SEC-246 (`unlock-master` meninggalkan `read_only`
tersimpan) dan keadaan terkunci yang bergantung pada urutan muat MySQL.

**Files:**
- Create: `internal/agent/drivers/mysql/readonly.go`
- Modify: `internal/agent/drivers/mysql/promote_lock.go:251-269,500-510,601-619`, `configure.go:609-630`; `internal/agent/cli.go:728-749,758-839`
- Test: `readonly_restart_test.go` (penegasan kembali ke nilai wajib), `promote_lock_test.go`, `internal/agent/cli_test.go`

**Interfaces:**
- Consumes: hasil Task 15 (aturan berhenti sudah lewat).
- Produces:
  ```go
  // OpenWrites: SET PERSIST super_read_only = OFF, then SET PERSIST read_only = OFF.
  func OpenWrites(ctx context.Context, db execer) error
  // LockWrites: SET PERSIST super_read_only = ON, then SET PERSIST read_only = ON.
  // locked is true once the first statement succeeded.
  func LockWrites(ctx context.Context, db execer) (locked bool, err error)
  ```

Aturan (spek 5.4 butir 2):
- `OpenWrites` dipakai `unlockPromote`, penjaga saat tenggat (`guardExpired`), `mysql.setReadOnly{on:false}` dan CLI `unlock-master`. `LockWrites` dipakai `lockForPromote`, `ensureLocked` dan CLI `lock-master`. CLI kehilangan salinan pernyataannya. `RESET PERSIST` tidak dipakai lagi untuk kedua bendera.
- **Tidak disentuh:** urutan promote (`promote.go:374-379`), `persistReadOnly` (karantina), `setReadOnly(true)`, kunci start.
- Pernyataan kedua buka gagal → galat; berkas kunci tidak dihapus, penjaga mengunci lagi. Pernyataan kedua kunci gagal → satu baris `Warn`, dan `lockForPromote` menjawab berhasil (`locked` benar); agent tidak pernah melaporkan "tidak terkunci" selagi `super_read_only` ON.
- `unlock-master` mencetak **C7** sebagai ganti baris `master dibuka: …` yang ada.

- [ ] **Step 1: Test gagal.**
  - `TestReadOnlyAcrossRestart`: setiap baris kembali menegaskan nilai wajib (kolom `head` dan komentarnya dihapus).
  - `TestUnlockPathsPersistBothOff` (tabel jalur: `unlockPromote`, penjaga saat tenggat, `setReadOnly(false)`, CLI): nilai tersimpan `read_only=OFF` dan `super_read_only=OFF`. `TestLockPathsPersistBothOn` (kunci promote, `ensureLocked`, CLI `lock-master`).
  - `TestLockSecondStatementFailureStillLocks`: `execer` palsu yang menggagalkan pernyataan kedua → `lockForPromote` berhasil, berkas kunci ada. `TestOpenSecondStatementFailureKeepsLockFile`: berkas kunci tetap; putaran penjaga berikutnya memasang `super_read_only` lagi.
  - `TestUnlockMasterPrintsC7`.
  - `TestNoResetPersistOfReadOnlyFlags`: tidak ada berkas `.go` bukan test di `internal/agent` yang memuat `RESET PERSIST` bersama `read_only`.
- [ ] **Step 2:** Jalankan; FAIL.
- [ ] **Step 3:** Implementasi.
- [ ] **Step 4:** `TestReadOnlyAcrossRestart` hijau di **setiap** baris. **Aturan berhenti:** bila sebuah baris tetap merah dengan perbaikan ini, task berhenti dan melapor; Task 18 dan 19 tidak dikerjakan di atas dugaan. Lalu tiga gerbang dan `make test-e2e`, exit 0.
- [ ] **Step 5: Commit:** `fix(agent): every unlock persists both read-only flags off and every lock persists both on`.

### Task 17: `unlock-master` bertanya

Menutup temuan S-10 (bagian R-188 dan R-187 di berkas CLI yang sama):
perintah yang kini disebut panel sebagai jalan keluar (T11, T14t) tidak
boleh menjadi satu-satunya perintah root yang tidak bertanya.

**Files:**
- Modify: `internal/agent/cli.go:143-200` (pengirim subperintah), `:798-839`
- Test: `internal/agent/cli_test.go`

**Interfaces:**
- Consumes: `OpenWrites` (Task 16), `setRoleAsk`.
- Produces: `func mainUnlockMaster(args []string, in io.Reader, stdout, stderr io.Writer) int`; bendera `--ya` dengan teks bantuan **C8**.

Aturan (spek 5.5):
- Bertanya hanya bila berkas kunci promote ada dan kandidatnya bukan `mysql.CLILockCandidate`: **C9** (kandidat; tenggat dalam bentuk yang sudah dipakai, `time.RFC3339` lokal), dijawab `ya`. Jawaban lain atau tanpa masukan → **P3** (tidak ada di spek) `Dibatalkan; kunci tidak dibuka.`, kode keluar 1, tidak ada yang berubah.
- Sesudah jawaban, berkas kunci dibaca ulang; kandidat atau tenggatnya berubah, atau kuncinya hilang → **P4** (tidak ada di spek) `Kunci promosi di server ini berubah selama Anda membaca; jalankan perintah ini lagi.`, kode keluar 1.
- `--ya` melewati pertanyaan; baris `Perhatian: …` yang ada (`cli.go:820-827`) tetap dicetak pada jalur itu.
- Tanpa kunci, atau kunci `lock-master`: tidak bertanya.

- [ ] **Step 1: Test gagal.** `TestUnlockMasterAsksUnderCandidateLock` (jawaban `ya` → terbuka dan kunci dihapus; `y`, kosong, akhir masukan → kode 1, kedua bendera dan berkas kunci tidak berubah; C9 persis dengan kandidat dan tenggat); `TestUnlockMasterYaFlagSkipsQuestion`; `TestUnlockMasterNoQuestionWithoutCandidateLock` (tanpa kunci; kunci `lock-master`); `TestUnlockMasterLockChangedDuringQuestion`.
- [ ] **Step 2:** `go test ./internal/agent -run TestUnlockMaster -short`; FAIL.
- [ ] **Step 3:** Implementasi.
- [ ] **Step 4:** Tiga gerbang, exit 0.
- [ ] **Step 5: Commit:** `fix(agent): unlock-master asks before it bypasses a candidate's promote lock`.

### Task 18: Status master membaca kedua bendera

Menutup dari R-188: panel menampilkan master sehat padahal aplikasi ditolak
(ERROR 1290).

**Files:**
- Modify: `internal/panel/monitor/status.go:265-268`
- Test: `internal/panel/monitor/status_test.go`, `alerts_test.go`

**Interfaces:**
- Consumes: `proto.HealthSnapshot.ReadOnly` (sudah dikirim setiap agent).
- Produces: tidak ada simbol baru; `StatusLocked` kini juga untuk `ReadOnly && !SuperReadOnly`, dengan keterangan **T19**.

- [ ] **Step 1: Test gagal.** `TestMasterStatusFlagCombinations`: OFF/OFF → sehat; `super_read_only` ON (dengan atau tanpa `read_only`) → "Terkunci" dengan keterangan yang ada; hanya `read_only` ON → "Terkunci" dengan T19 persis. `TestMasterLockedAlertOnReadOnlyOnly`: peringatan `master_locked` terbuka, dan tertutup saat kedua bendera OFF.
- [ ] **Step 2:** Jalankan; FAIL.
- [ ] **Step 3:** Implementasi. **Buka kunci master** (`mysql.setReadOnly{on:false}`, yang sudah menyimpan keduanya OFF pada versi agent mana pun) adalah perbaikannya; halaman master sudah menampilkannya selama status "Terkunci".
- [ ] **Step 4:** Dua gerbang panel, exit 0.
- [ ] **Step 5: Commit:** `fix(panel): a master with read_only on reads as locked`.

### Task 19: Field "akan baca-saja sesudah restart" dan Perbaiki pengaturan tersimpan

Menutup dari R-188: master yang **sudah** terkena (nilai tersimpan salah,
belum direstart) terlihat dan bisa diperbaiki dari panel (temuan S-4).

**Files:**
- Create: `internal/agent/drivers/mysql/restart_readonly.go`, `restart_readonly_test.go`
- Modify: `internal/proto/health.go:17-66`; `internal/agent/drivers/mysql/health.go:36-62`; `internal/panel/web/handlers_controls.go:297-316`, `handlers_mysql.go` (`masterPage`), `web/templates/master_detail.html:118,219-226`
- Test: `internal/proto/health_test.go`, `internal/panel/web/web_controls_test.go`, `web_mysql_test.go`

**Interfaces:**
- Consumes: tabel Task 15 (aturan field), `OpenWrites` (Task 16), status Task 18, `persisted_variables`, pembacaan `--print-defaults` kunci start (`startlock.go:703-720`).
- Produces:
  ```go
  // proto.HealthSnapshot (K29)
  // RestartReadOnly: on a server pinned master, with no promote lock and no
  // start lock, and both flags off now, MySQL would start read-only at its
  // next restart. Sent only when true; an agent before this field never sends it.
  RestartReadOnly bool `json:"restart_read_only,omitempty"`

  // mysql driver: the rule, from Task 15's table.
  type flagValues struct{ ReadOnly, SuperReadOnly string } // "ON", "OFF", or "" when not set there
  func restartReadOnly(persisted, optionFile flagValues) bool
  ```
  Form **Perbaiki pengaturan tersimpan**: `POST /masters/{id}/unlock` dengan `repair=1` (K30); detail audit `master_unlock` memuat `repair: true`; flash `?pesan=tersimpan-dibetulkan` = **T20b**.

Aturan (spek 5.4 butir 4):
- Agent melaporkan field itu hanya bila: pin peran `master`; tidak ada berkas kunci promote; tidak ada kunci start; `read_only` dan `super_read_only` keduanya OFF sekarang; dan nilai tersimpan atau blok konfigurasi akan membuat salah satu bendera ON pada start berikutnya. Aturan awal: sebuah bendera ON pada start bila nilai tersimpannya ON, atau bila ia tidak tersimpan dan berkas opsi berkata ON. **Bila tabel Task 15 menunjukkan lain untuk sebuah kombinasi, aturan mengikuti tabel**, dan bedanya dicatat di "Perubahan selama pelaksanaan".
- Agent hanya melapor; jalur ini tidak pernah menjalankan pernyataan yang mengubah bendera. Pembacaan berkas opsi disimpan paling lama 60 detik.
- Panel menampilkan **T20** dan tombol **Perbaiki pengaturan tersimpan** di halaman master hanya bila `RestartReadOnly` benar **dan** status master bukan "Terkunci". Tombol itu tindakan, token (`unlock_master`, berat) dan nama yang diketik milik **Buka kunci master**; agent menolaknya di bawah kunci promote seperti sekarang.

- [ ] **Step 1: Test gagal.**
  - `TestRestartReadOnlyRule` (tabel murni dari hasil Task 15, termasuk: tersimpan `read_only=ON` saja → benar; tersimpan `read_only=OFF` saja di atas OF ON/ON → menurut ukuran; tersimpan OFF/OFF di atas OF ON/ON → salah; tidak ada yang tersimpan, tanpa OF → salah).
  - `TestRestartReadOnlyReported` (mysqld sungguhan): master yang dibuka dengan tiga pernyataan lama (`RESET PERSIST IF EXISTS super_read_only; SET GLOBAL …`) sesudah karantina → laporan memuat `restart_read_only: true`; sesudah `OpenWrites` → field hilang; restart sesudah itu → OFF/OFF.
  - `TestRestartReadOnlyOnlyOnOpenPinnedMaster`: pin replika; pin `quarantined`; berkas kunci promote ada; kunci start ada; `read_only` ON sekarang → field tidak dikirim.
  - `TestRestartReadOnlyNeverWrites`: nilai tersimpan dan berjalan sama sebelum dan sesudah sepuluh laporan.
  - `TestHealthSnapshotStillComparable` (proto): dua snapshot bernilai sama `==`.
  - Web: `TestMasterPageShowsRestartWarning` (T20 dan tombol; tidak ada saat status "Terkunci"; tidak ada saat field salah); `TestNoRestartWarningFromOldAgent` (snapshot tanpa field → tidak ada peringatan); `TestRepairStoredSettings` (tanpa token → 403; dengan token dan nama → `UnlockMaster` dipanggil, flash T20b, audit `repair: true`); `TestRepairRefusedUnderPromoteLock` (penolakan agent tampil berlabel, tidak ada flash T20b).
- [ ] **Step 2:** Jalankan; FAIL.
- [ ] **Step 3:** Implementasi.
- [ ] **Step 4:** Tiga gerbang, exit 0.
- [ ] **Step 5: Commit:** `feat: warn about a master that would restart read-only and repair it from the panel`.

### Task 20: Penjaga kunci membaca replika di alamat tercatat

Menutup R-202 (A5b2 SEC-269).

**Files:**
- Modify: `internal/agent/drivers/mysql/promote_files.go:95-137,211-310`, `promote_lock.go:225-253,310-390,438-498,624-700`; `internal/agent/cli.go:845-874` (teks tidak berubah)
- Test: `internal/agent/drivers/mysql/promote_lock_test.go`, `promote_files_test.go` (seam `lookupIP` dan `readPeer` di `export_test.go:176,257`)

**Interfaces:**
- Consumes: `lookupIP`, `dialAt` (`quarantine.go:571`), `readPeers`.
- Produces:
  ```go
  // PromoteLock.ReadableAt maps a Readable entry (host:port) to the address
  // that answered when the lock was set; an older agent's lock has none.
  ReadableAt map[string]string
  // lockJSON (K32): ReadableAt []lockPeerJSON `json:"readable_at,omitempty"`
  type lockPeerJSON struct{ Key, Addr string } // json: "key", "addr"
  ```

Aturan (spek 5.5 "Penjaga kunci promote"):
- Saat kunci dipasang, tiap nama dalam daftar diselesaikan sekali dan dibaca di alamatnya (`dialAt`; nama tetap dipakai untuk memeriksa sertifikat); yang dicatat adalah alamat yang menjawab. Nama yang berupa alamat IP dicatat sebagai dirinya.
- `readListed` (dipakai `unlockPromote` dan `guardExpired`) menyambung ke alamat tercatat; entri tanpa alamat (berkas kunci dari agent lama) dibaca lewat nama seperti sekarang.
- `ForgetLockHost` menghapus pasangannya juga.
- Alamat tercatat yang tidak menjawab terbaca "tidak bisa dibaca": kunci dipertahankan dan kejadian Kritis yang ada (`keepLock`) menyebut `host:port`-nya.
- **Langkah pemeriksaan (bagian 6 spek):** baca baris R-033 (bagian port, sudah ditangani) dan R-108 (replika tidak terbaca saat kunci dipasang) di inventaris risiko; laporan task menyebut apakah perubahan ini menyentuh salah satunya. Tidak ada kode untuk R-108 di sini.

- [ ] **Step 1: Test gagal.** `TestListedReplicaReadAtRecordedAddress`: saat kunci dipasang `lookupIP` menjawab alamat A; saat buka dan saat tenggat ia menjawab alamat umpan → `readPeer` hanya dipanggil untuk A; umpan tidak pernah disambungi. `TestLockRecordsAnsweringAddress` (nama dengan dua alamat, hanya yang kedua menjawab). `TestOldLockFileReadByName` (berkas tanpa `readable_at`). `TestForgetReplicaRemovesAddressPair`. `TestGuardKeepsLockWhenRecordedAddressSilent`. `TestLockFileRoundTripKeepsReadableAt`. Dengan mysqld sungguhan: `TestPromoteLockReadsListedReplicaByAddress` (satu test kunci dua instance yang ada, dengan nama yang dipetakan ke alamat instance kedua).
- [ ] **Step 2:** Jalankan; FAIL.
- [ ] **Step 3:** Implementasi.
- [ ] **Step 4:** Tiga gerbang dan `make test-e2e`, exit 0.
- [ ] **Step 5: Commit:** `fix(agent): the promote lock guard reads listed replicas at the address recorded at lock time`.

### Task 21: `set-role` bertanya

Menutup R-187 (A5b2 SEC-244, SEC-245; temuan S-9).

**Files:**
- Modify: `internal/agent/cli.go:327-450`; `internal/agent/drivers/mysql/pins.go:179-183`
- Test: `internal/agent/cli_test.go`, `internal/agent/drivers/mysql/pins_test.go`

**Interfaces:**
- Consumes: `setRoleAsk`, `setRoleCancelled`, `setRoleChanged`, `cliMySQLHost`, `persistedReadOnlyOn`.
- Produces: `func setRoleQuestion(was, role string, mysqlFollows bool) (consequence string, ask bool)`; teks bantuan `--ya` = **C8**.

Aturan (spek 5.5 "`set-role` bertanya"):
- Bertanya bila: peran sekarang `master` dan peran baru bukan `master` (**C3** ke `replica`, **C4** ke `unassigned`, **C2** ke `quarantined`); peran baru `quarantined` dan peran sekarang bukan itu (**C2**); peran sekarang `replica` dan peran baru `master` (**C5**); peran sekarang `unassigned` dan peran baru `master`, **hanya bila** MySQL server itu menjawab dan punya kanal replikasi terpasang atau menyimpan kedua bendera ON (**C5**). MySQL yang mati tidak menahan perintah dan tidak memunculkan pertanyaan. Antara `replica` dan `unassigned`: tidak bertanya.
- Layar: baris **C1** (nama host kernel dari `os.Hostname`, peran sekarang, peran baru), lalu akibatnya, lalu `setRoleAsk` yang ada. Pertanyaan yang sudah ada (meninggalkan `quarantined`, pin tidak terbaca) tidak berubah.
- Jawaban selain `ya`, atau tanpa masukan → `setRoleCancelled`, kode keluar 1. Peran dibaca ulang sesudah jawaban (`setRoleChanged`). `--ya` melewati semuanya.
- Sesudah `set-role quarantined` berhasil tercetak **C6** sebagai ganti baris `peran diperbarui; …`.
- `roleRefusal` untuk peran `master` menjadi **A1**; untuk peran lain tidak berubah. Panel tidak mengurai teks itu.

- [ ] **Step 1: Test gagal.**
  - `TestSetRoleAsks` (tabel pasangan peran × jawaban `ya`, `tidak`, akhir masukan, `--ya`): `master`→`replica`, `master`→`unassigned`, `master`→`quarantined`, `replica`→`quarantined`, `unassigned`→`quarantined`, `replica`→`master`; tanpa pertanyaan: `replica`↔`unassigned`, `master`→`master`. Peran hanya berubah pada `ya` atau `--ya`.
  - `TestSetRoleScreen`: C1 dengan nama host, lalu C2, C3, C4 atau C5 persis, lalu `setRoleAsk`.
  - `TestSetRoleUnassignedToMaster` (mysqld sungguhan): kanal replikasi terpasang → bertanya dengan C5; kedua bendera tersimpan ON → bertanya; master polos → tidak bertanya; MySQL dihentikan → tidak bertanya dan peran berubah.
  - `TestSetRoleQuarantinedPrintsC6`; `TestSetRoleChangedDuringQuestion`; `TestSetRoleUnreadablePinsKeepOldBranch`.
  - `TestRoleRefusalOnMasterIsA1` (`pins_test.go`): teks A1 dengan nama perintah; tidak memuat `set-role <peran>`; peran `replica` dan `quarantined` memberi teks lama.
- [ ] **Step 2:** Jalankan; FAIL.
- [ ] **Step 3:** Implementasi.
- [ ] **Step 4:** Tiga gerbang, exit 0.
- [ ] **Step 5: Commit:** `fix(agent): set-role asks before it can stop a master or open a replicating server`.

### Task 22: Persetujuan bangun ulang berakhir bersama percobaannya

Menutup R-203 (A5b2 SEC-270; temuan R-5).

**Files:**
- Modify: `internal/agent/drivers/mysql/strict.go:54-99,186-193`, `snapshot.go:36-60` (`registerSnapshotJob`), `databases.go:15-`, `rebuild_approval.go:240-268`; `internal/proto/mysql.go:802-815`; `internal/panel/orchestrator/runner.go:1434-1504,1863-1903`, `approval.go`
- Test: `internal/agent/drivers/mysql/strict_test.go`, `rebuild_approval_test.go`, `internal/agent/strict_commands_test.go`; `internal/panel/orchestrator/oldmaster_test.go`, `approval_test.go`

**Interfaces:**
- Consumes: `Pins.ClearRebuild`, `strictGate`, `rebuildApprovalForJob`, `ErrRebuildNeedsApproval`.
- Produces:
  ```go
  // agent: mysql.endRebuildApproval (K31), strictAllowed, no parameters; it
  // can only end: the approval and the request are removed.
  // proto
  type EndRebuildApprovalResult struct {
      Ended bool `json:"ended"` // true also when there was none
  }
  // panel
  func (r *Runner) endRebuildApproval(ctx context.Context, serverID int64) (ended bool) // false: unknown command, no answer, any error
  ```

Aturan (spek 5.5 "Persetujuan bangun ulang"):
- **Langkah pemeriksaan lebih dulu (bagian 6 spek):** daftar setiap perintah yang dikirim panel ke master lama sesudah sebuah langkah bangun ulang gagal atau dibatalkan (`jobRun.fail` `runner.go:1863-1903`, `cancelJob` `:1434-1504`, `stopReplicaAfter`, `dropDumpAccount`), dengan kelasnya di `strictCommands`. Yang terbaca penulis rencana: `mysql.stopReplica` dan `job.cancel` diizinkan; `mysql.resetReplica` (`runner.go:1471`) terlindung dan dikirim saat batal sesudah langkah sambung. Hasilnya dicatat di "Perubahan selama pelaksanaan". **Jalan mundur:** bila ada pembersihan yang memang butuh persetujuan di master lama yang masih dikarantina, agent hanya menutup tiga perintah yang membuang data (`mysql.dumpFrom`, `mysql.cloneFrom`, `mysql.dropDatabases`) sampai `approve` berikutnya, dan persetujuan penuh berakhir oleh permintaan panel atau batas 24 jam; keputusan itu dicatat dan dilaporkan.
- Agent mengakhiri persetujuan sendiri (`ClearRebuild`) saat pekerjaan `mysql.dumpFrom` atau `mysql.cloneFrom` berakhir gagal atau dibatalkan, dan saat `mysql.dropDatabases` gagal, hanya di server berperan `quarantined`. Jalur berhasil dan pengakhiran saat bangun ulang tuntas (`quarantine.go:704`) tidak berubah.
- Panel, saat pekerjaan bangun ulang master lama (`RebuildOldMaster && !QuarantineEnded`) gagal atau dibatalkan, **sesudah** perintah pembersihannya sendiri, mengirim `mysql.endRebuildApproval` (batas `cancelTimeout`). `Ended` benar → baris log pekerjaan **T21**; tidak dikenal (`unknown_command`), tidak menjawab atau galat lain → **T21b**. Jawaban `unknown_command` di sini tidak menandai agent "perlu dimutakhirkan".
- "Ulangi dari tahap ini" sesudah itu berhenti sekali di "menunggu persetujuan di server" dengan kode baru, lewat alur yang ada. Batas 24 jam dan `approve batal` tidak berubah.

- [ ] **Step 1: Catat pemeriksaan** di atas; tetapkan apakah jalan mundur dipakai.
- [ ] **Step 2: Test gagal.**
  - Agent: `TestApprovalEndsWhenCopyFailsOrIsCancelled` (dump gagal; clone gagal; dibatalkan → berkas persetujuan dan permintaan hilang); `TestApprovalEndsWhenDropDatabasesFails`; `TestApprovalKeptWhenCopySucceeds`; `TestSettleStillClearsApproval`; `TestEndRebuildApprovalOnlyEnds` (di server dikarantina tanpa persetujuan → `ended: true`, tidak ada berkas dibuat; dengan persetujuan → hilang; perintah itu `strictAllowed`); `TestEndRebuildApprovalOutsideQuarantine` (tidak mengubah apa pun).
  - Agent: `TestCleanupAfterFailedCopyNotRefused`: tiap perintah dari tabel Step 1 yang dikirim panel sesudah salinan gagal dijawab tanpa `needs_local_approval`, sesudah agent mengakhiri persetujuan.
  - Panel: `TestRebuildFailureEndsApproval` (urutan: pembersihan dulu, lalu `mysql.endRebuildApproval`); `TestRebuildCancelEndsApproval`; `TestEndApprovalTexts` (`ended` → T21 persis; `unknown_command` → T21b dan `AgentNeedsUpgrade` tidak dipanggil; batas waktu → T21b); `TestOrdinaryReplicaFailureSendsNoEndApproval`.
  - Panel: `TestRetryAfterFailedRebuildWaitsForApproval`: "Ulangi" → `ErrRebuildNeedsApproval` dengan kode baru; sesudah persetujuan, berjalan. `TestOldPanelRetryMeetsApprovalRequest`: alur "Ulangi" tanpa perintah baru (bentuk panel 0.2.0) terhadap agent yang sudah mengakhiri persetujuan → `needs_local_approval` lewat alur yang ada.
- [ ] **Step 3:** Jalankan; FAIL.
- [ ] **Step 4:** Implementasi.
- [ ] **Step 5:** Tiga gerbang dan `make test-e2e`, exit 0.
- [ ] **Step 6: Commit:** `fix: a rebuild approval ends with the attempt it was given for`.

### Task 23: Teks Lepas replika

Menutup R-190 (A5b2 SEC-286).

**Files:**
- Modify: `internal/panel/web/handlers_mysql.go:207-214` (flash `dilepas-henti`), halaman replika (model dialog), `handlers_guide.go`; `web/templates/replica_detail.html:205-225`, `panduan.html`; `README.md:1380-1394` dan bagian `## Panduan` (sesudah `### Replika yang tersambung di luar alur panel`)
- Test: `internal/panel/web/web_monitor_test.go:680` (`TestNoTemplatePromotesByHand`), `guide_readme_test.go:167,206`, `web_controls_test.go`, `web_flash_test.go`

**Interfaces:**
- Consumes: status master dari Monitor (`monitor.StatusDown`).
- Produces: panduan ke-10 **`Memakai bekas replika sebagai server mandiri`**, id `bekas-replika-mandiri` (K33).

Aturan (spek 5.9 "Lepas replika"):
- Flash sesudah lepas dan hentikan = **T27**. Pilihan "Lepas dan hentikan replikasi" di dialog tidak lagi memuat SQL; kalimatnya **P7** (tidak ada di spek, disusun dari kalimat T27): `Replikasi dihentikan dan pengaturannya dihapus. MySQL di server ini tetap terkunci hanya-baca. Kapan aman membukanya, dan caranya, ada di Panduan: "Memakai bekas replika sebagai server mandiri".`
- Dialog **Lepas replika** menampilkan kotak peringatan **T26** selagi status master "Diduga tumbang"; tidak memblokir dan tidak menambah centang atau isian wajib.
- Panduan baru: kalimat syarat **B4**, lalu langkah bernomor: periksa `SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only;`, lalu `SET PERSIST super_read_only=OFF;` dan `SET PERSIST read_only=OFF;`, lalu periksa lagi. README memuat bagian yang sama (`###` berjudul sama, blok perintah yang sama); README "Kontrol replika" kehilangan SQL-nya dan menunjuk panduan itu.
- Setiap perintah panduan itu dijalankan penulisnya pada mysqld coba lebih dulu.

- [ ] **Step 1: Test gagal.** `TestNoUnlockSQLOutsideGuide` (perluasan `TestNoTemplatePromotesByHand`: tidak ada templat selain `panduan.html`, dan tidak ada flash, yang memuat `super_read_only=OFF`); `TestDetachFlashT27`; `TestDetachDialogWarnsWhenMasterDown` (T26 dengan nama master saat "Diduga tumbang"; tidak ada saat sehat; form tetap bisa dikirim tanpa isian baru); `TestGuidePageHasTenGuides` (pengganti `TestGuidePageHasNineGuides`); `TestGuideMatchesREADME` lulus dengan sepuluh panduan; `TestScreensLinkToGuides` memuat `#bekas-replika-mandiri`.
- [ ] **Step 2:** Jalankan; FAIL.
- [ ] **Step 3:** Implementasi; `bash scripts/tailwind.sh`.
- [ ] **Step 4:** Dua gerbang panel, exit 0.
- [ ] **Step 5: Commit:** `fix(panel): the detach texts no longer give the unlock SQL without its condition`.

### Task 24: README, Panduan, pembaruan, test ujung ke ujung, catatan ukur, daftar teks

Menutup R-195 sejauh kalimat B1, dan membuat bagian A bisa di-merge sendiri
(bagian 6 spek): tidak ada kalimat README, Panduan atau spek yang berbohong
pada hari merge.

**Files:**
- Create: `internal/panel/e2e_5b3a_test.go`; `scripts/e2e_browser_light_confirm.js`
- Modify: `README.md`; `web/templates/panduan.html`; `internal/panel/e2e_browser_test.go`, `e2e_helpers_test.go`; `docs/superpowers/specs/2026-11-05-rencana-4-promote-design.md:208-210`, `docs/superpowers/specs/2026-11-12-rencana-5b-mode-ketat-gerbang-prasyarat-design.md` (§5.7, satu baris rujukan ke spek 5b-3 §5.1); bagian "Catatan hasil ukur R-188" dan "Teks baru untuk dibaca pemilik" rencana ini
- Test: `internal/panel/web/guide_readme_test.go`

**Interfaces:**
- Consumes: semua task sebelumnya; pembantu e2e (`startE2EPromoteTopology`, `uiClient`, `skipWithoutE2E`, `globalVar`, `waitVar`, `e2eAgent.stop/start`, `restartMySQL`).

README dan Panduan (halaman Panduan dan README saling mencerminkan;
`TestGuideMatchesREADME` menjaganya):
- **B1** menggantikan kalimat "Ulangi … tidak menilai lagi" di `README.md:1528-1530`, `:3445-3446`, `:4291-4293` dan `panduan.html:91`.
- **B2** dan **B6** di "Keamanan"; paragraf `README.md:121-127` ("Login pertama") ditulis ulang: restart panel bukan obat. Langkah 5.12 nomor 3 (login sekali dari tiap browser).
- "Promote": tombol **Buka kunci promote**; tiga keadaan "Ulangi" (sebelum kirim; terkirim; sesudah master diganti); jalan keluar perintah root T14t dengan syaratnya; larangan Resync selagi master terkunci; promote terencana menolak replika yang tidak terbaca (5.12 nomor 6).
- "Tambah server database" dan "Pembaruan": perintah pasang ulang tampil sekali dan berlaku 15 menit; langkah 5.12 nomor 1, 2, 4 dan 7; kalimat bersyarat dari Task 15 bila ukurannya menuntut.
- "Peran dan sumber yang dipin": layar pertanyaan `set-role` (C1 sampai C5), `--ya`, pertanyaan `unlock-master` (C9), dan C7.
- "Master lama": sesudah bangun ulang gagal atau dibatalkan root menjalankan `approve` lagi.
- "Kontrol replika": Stop replika meminta kata sandi panel (jendela).
- "Keamanan": satu kalimat masing-masing untuk ambang disk, folder cadangan dan pelepasan replika sebagai cara mengubah warna baris; risiko tersisa bagian A dari spek bagian 8 ("Dari perubahan rencana ini" dan "Bagian A tanpa bagian B").
- Panduan "Melepas kunci master lama dengan tangan", "Master terkunci padahal tidak ada promote" dan "Memakai lagi server yang dihapus saat masih dikarantina": langkah `SET PERSIST … OFF` dengan tangan diganti `sudo dbpanel agent unlock-master` untuk agent versi ini, dengan **satu baris** untuk agent yang lebih lama; syarat yang tertulis di atas langkah itu tidak diubah.
- Panduan "Replika yang tersambung di luar alur panel", langkah 3 dan 4: pertanyaan `set-role quarantined` ditampilkan; dua kalimat "tidak bertanya" dibuang.
- **B3** menggantikan kalimat Rencana 4 §5.1 (`RESET PERSIST super_read_only; …`).
- Setiap perintah yang ditulis atau diubah dijalankan dulu pada mysqld coba; yang tidak bisa dijalankan disebut di laporan.

- [ ] **Step 1: Test e2e gagal** (`skipWithoutE2E`):
  - `TestE2EKeptPromoteLockThenUnlockLock`: promote terencana dengan agent kandidat dihentikan saat "Replika mengejar" → pekerjaan gagal, kunci dipertahankan; master `super_read_only` ON. Resync replika lain ditolak dengan T15b; "Ulangi" promote ditolak dengan T12b. Agent kandidat dinyalakan lagi; `POST /jobs/{id}/unlock-lock` tanpa token → flash T10; di master `read_only` dan `super_read_only` OFF, nilai tersimpan keduanya OFF; Resync kini diterima.
  - `TestE2EMasterWithPersistedReadOnly`: `SET PERSIST read_only=ON` di master → status "Terkunci" dengan T19 dan peringatan `master_locked`; **Buka kunci master** → sehat, nilai tersimpan keduanya OFF. Diulang dengan kedua bendera ON.
  - `TestE2ERebuildFailedThenRetryWaitsForApproval`: bangun ulang master lama digagalkan di langkah salin → log pekerjaan memuat T21; berkas persetujuan di agent hilang; "Ulangi dari tahap ini" berhenti menunggu persetujuan dengan kode baru; sesudah `approve`, selesai.
  - Browser (`go test -tags e2e_browser -run TestE2EBrowserLightConfirm ./internal/panel/`): di dalam jendela, dialog **Stop replika** tampil dengan T30 tanpa isian kata sandi dan mengirim; sesudah jam panel dimajukan 10 menit, dialog yang sama menampilkan isian kata sandi dengan T2. `TestE2EBrowserConfirm` yang ada tetap lulus.
- [ ] **Step 2:** `make test-e2e` dan perintah browser di atas; test baru FAIL, atau bila sudah lulus karena task sebelumnya, catat itu di laporan.
- [ ] **Step 3:** Betulkan apa pun yang ditemukan test itu di kode task sebelumnya dengan commit `fix(...)` tersendiri; tulis bagian README dan Panduan; ganti kalimat kedua spek lama.
- [ ] **Step 4: Periksa penjaga 5.13.** Setiap test berikut ada dan hijau: `TestPromoteRetryPastPointTokenlessAndUngated`; `TestUnlockPromoteLockAfterKeptLock`; `TestUnlockPromoteLockRefusedAfterSend`; `TestUnlockPromoteLockSuperseded`; `TestRetrySentUnansweredReadOnlyWithinBoundRefused`; `TestLoadRefusedWithoutLiveMasterReport`; `TestQuarantineRetryNeverStops`; `TestConfirmNotBlockedByOutsideLoginFailures`; `TestWindowTokenRefusedByHeavyAction`; `TestPendingQuarantineNotHeldBySilentAgent` dengan `-race`; `TestReadOnlyAcrossRestart`.
- [ ] **Step 5: Lengkapi "Catatan hasil ukur R-188"** (tabel akhir sesudah Task 16, dengan versi MySQL) dan isi "Teks baru untuk dibaca pemilik": setiap teks operator yang baru, berubah atau hilang antara `8caee75` dan akhir bagian A, disalin dari kode, dengan id spek; teks P… dan setiap teks lain yang tidak ada di spek ditandai "tidak ada di spek".
- [ ] **Step 6:** Empat gerbang (agent dan proto, panel, `make test`, `make test-e2e`) dan test browser, exit 0.
- [ ] **Step 7: Commit:** `docs: README, guides and upgrade steps for Plan 5b-3 part A; end-to-end tests of the new flows`.

### Task bersyarat P: port pembantu test MySQL

Hanya dikerjakan bila aturan di Global Constraints memajukannya (spek bagian
6 dan 5.10); selain itu ia milik bagian B.

**Files:**
- Modify: `internal/agent/drivers/mysql/testmysql/instance.go:105-175,279-286`
- Test: `internal/agent/drivers/mysql/testmysql/instance_test.go`

Aturan (spek 5.10): port dipilih sesudah `--initialize`, di luar rentang port sementara kernel (`/proc/sys/net/ipv4/ip_local_port_range`), dicadangkan dengan berkas ber-`flock` selama instance hidup, dengan satu percobaan ulang di port lain bila log galat berkata alamat sedang dipakai.

- [ ] **Step 1: Test gagal.** `TestPortOutsideEphemeralRange`; `TestPortReservedAcrossInstances` (dua instance bersamaan tidak pernah mendapat port yang sama; berkas cadangan dilepas saat instance berhenti).
- [ ] **Step 2:** Jalankan; FAIL.
- [ ] **Step 3:** Implementasi.
- [ ] **Step 4:** Tiga gerbang, exit 0.
- [ ] **Step 5: Commit:** `test: reserve the MySQL test helper's port for the life of the instance`.

---

## Sesudah Task 24

1. **Tinjauan seluruh cabang bagian A**, seperti biasa (dua peninjau: sisi
   agent dan sisi panel), satu gelombang perbaikan, lalu satu pemeriksaan
   ulang terarah yang terbatas pada diff perbaikan itu. Tidak ada tinjauan
   penuh kedua.
2. **Pengendali berhenti dan melapor kepada pemilik** di antara bagian A dan
   bagian B (bagian 6 spek). Laporan berhenti itu singkat, berbahasa
   Indonesia, dan memuat:
   - apa yang kini tertutup (baris R dan nomor SEC dari tabel task di atas),
     hasil tinjauan cabang dan gelombang perbaikannya;
   - catatan hasil ukur R-188, dan bahwa semuanya hanya terukur pada MySQL
     8.0.46;
   - **yang belum ada sampai bagian B:** penanda generasi folder cadangan,
     jadi centang cadangan T8 masih bisa dihindari dengan waktu (laporan lama
     mengembalikan keadaan folder sebelumnya); baris gerbang masih menilai
     laporan basi (R-197); R-199; batas beban agent dan monitor (R-186,
     R-200, R-201); header, sesi dan pertumbuhan (R-189, R-193, R-194); sisa
     teks (R-192, sisa R-195 dan R-196); test yang kadang gagal dan sisa
     kecil (R-204, R-205); langkah pembaruan 5.12 nomor 5;
   - setiap keputusan K rencana ini yang menyimpang dari bunyi spek, setiap
     teks "tidak ada di spek", setiap butir "Perubahan selama pelaksanaan",
     dan yang "tidak dijalankan" di mesin pelaksana;
   - permintaan keputusan: lanjut ke rencana bagian B, atau merge bagian A
     dulu. Merge ke `main` hanya atas persetujuan pemilik.
3. **Audit keamanan dijalankan sekali, sesudah bagian B, atas kedua bagian.**
   Bagian A tidak diaudit sendiri. Aturannya (spek 5.13 "Penutup"; susunan
   seperti audit Rencana 5b-2):
   - **Tiga pemeriksa, berjalan bersamaan, hanya membaca** (tidak menjalankan
     test yang menyalakan MySQL; `graphify query` lebih dulu): agent; panel
     (orchestrator, monitor, store); web.
   - **Sisi web menurut OWASP Top 10**, tiap kategori dinilai satu per satu
     dengan bukti berkas dan baris (tabel rute: butuh login? asal sama?
     konfirmasi berat, ringan, atau tidak? diaudit?). **Header keamanan
     diperiksa dengan test, bukan hanya dibaca**; header yang kurang menjadi
     temuan.
   - Yang wajib dijawab untuk bagian A: apakah ada tindakan berat yang
     menerima token jendela; apakah ada jalan dari sesi tanpa kata sandi ke
     agent pengganti, alamat master lain, atau replika yang berhenti; apakah
     **Buka kunci promote** bisa membuka master di samping master lain;
     apakah ada keadaan promote yang meninggalkan master baca-saja tanpa
     langkah berikutnya; apakah cookie perangkat bisa dipakai sebagai
     kredensial atau untuk mengunci admin; setiap keputusan K dan setiap
     butir spek bagian 8.
   - **Keluaran:** dokumen audit di `docs/superpowers/security/`, temuan
     bernomor mulai **SEC-295**; inventaris risiko
     (`docs/superpowers/security/2026-11-10-inventaris-risiko.md`)
     diperbarui: baris R-181 sampai R-205 yang tertutup dipindah ke "Sudah
     ditangani" dengan commit-nya, risiko baru mulai **R-206**; laporan
     singkat untuk pemilik.
   - **Sesudah audit:** temuan Kritis dan Tinggi diperbaiki dalam satu
     gelombang, lalu diperiksa ulang oleh pemeriksa yang menemukannya,
     terbatas pada diff perbaikan itu. Temuan Sedang ke bawah dibawa ke
     pemilik dengan saran. Temuan yang hanya bisa ditutup dengan mengubah
     keputusan spek yang sudah disetujui: berhenti dan tanyakan pemilik.

## Catatan hasil ukur R-188

Diisi Task 15 (pada kode head) dan dilengkapi Task 16, 19 dan 24. Semua
nilai berlaku untuk **MySQL 8.0.46** saja; 8.4, 9.x dan batas bawah 8.0.26
tidak bisa dijalankan di mesin pelaksana. Nilai ditulis `read_only` /
`super_read_only`; "tersimpan" adalah isi
`performance_schema.persisted_variables` (`-` berarti tidak tersimpan).

Terukur oleh Task 15 dengan `TestReadOnlyAcrossRestart` pada head `b3a1a9d`,
mysqld `SELECT VERSION()` = `8.0.46-0ubuntu0.24.04.4`. "OF" = baris
`read_only=ON` dan `super_read_only=ON` di berkas opsi. Sel "berjalan" adalah
nilai berjalan sesudah restart terakhir; tersimpan ditulis sesudah tanda `;`.

| Baris | Tersimpan sebelum restart | Sesudah restart, di head `b3a1a9d` | Wajib | Sesudah Task 16 |
|---|---|---|---|---|
| premis-1 | OFF/OFF (OF ada) | OFF/OFF; OFF/OFF | OFF/OFF | OFF/OFF; OFF/OFF |
| premis-2 | ON/ON (tanpa OF) | ON/ON; ON/ON | ON/ON | ON/ON; ON/ON |
| premis-3 | ON/ON (OF ada) | ON/ON; ON/ON | ON/ON | ON/ON; ON/ON |
| premis-4 | OFF/OFF (tanpa OF) | OFF/OFF; OFF/OFF | OFF/OFF | OFF/OFF; OFF/OFF |
| jenis1-kunci | `lockForPromote`: -/ON; `lock-master`: -/ON | keduanya ON/ON; -/ON | ON/ON | keduanya ON/ON; ON/ON |
| jenis1-buka | `unlockPromote`: -/-; `unlock-master`: -/- | keduanya OFF/OFF; -/- | OFF/OFF | keduanya OFF/OFF; OFF/OFF |
| jenis2-kunci | OFF/ON (OF ada) | ON/ON; OFF/ON | ON/ON | ON/ON; ON/ON |
| jenis2-buka | OFF/- (OF ada) | **OFF/ON**; OFF/- | OFF/OFF | OFF/OFF; OFF/OFF |
| jenis3-dari-1 | ON/- (tanpa OF) | **ON/OFF**; ON/- | OFF/OFF | OFF/OFF; OFF/OFF |
| jenis3-dari-2 | ON/- (OF ada) | **ON/ON**; ON/- | OFF/OFF | OFF/OFF; OFF/OFF |

Baris premis semua lulus di head: aturan berhenti tidak berlaku. Tiga baris
yang tebal gagal di head; test menegaskan nilai terukurnya (K28) sampai Task 16.

Kolom "Sesudah Task 16" diukur oleh Task 16 dengan `TestReadOnlyAcrossRestart`
yang sama (mysqld `8.0.46-0ubuntu0.24.04.4`): nilai berjalan sesudah restart
terakhir; tersimpan sesudah tanda `;`. Setiap baris lulus, dan test kini juga
menegaskan nilai tersimpan: setiap kunci menyimpan ON/ON, setiap buka OFF/OFF.
Langkah yang berubah: setiap `mysql.lockForPromote`, `agent lock-master` dan
putaran penjaga menyimpan ON/ON (bukan -/ON atau OFF/ON); setiap
`mysql.unlockPromote` dan `agent unlock-master` menyimpan OFF/OFF (bukan -/-,
OFF/- atau ON/-). Kedua pertanyaan tetap dijawab sama dan kini ditegaskan.

Langkah per baris (berjalan; tersimpan, sesudah tiap langkah):

- premis-1: mulai OFF/OFF; -/- → OF ditulis → `SET PERSIST super_read_only = OFF`, `SET PERSIST read_only = OFF`: OFF/OFF; OFF/OFF → restart: OFF/OFF; OFF/OFF.
- premis-2: mulai OFF/OFF; -/- → `SET PERSIST super_read_only = ON`, `SET PERSIST read_only = ON`: ON/ON; ON/ON → restart: ON/ON; ON/ON.
- premis-3: mulai OFF/OFF; -/- → OF ditulis → kedua `SET PERSIST … = ON`: ON/ON; ON/ON → restart: ON/ON; ON/ON.
- premis-4: mulai OFF/OFF; -/- → kedua `SET PERSIST … = OFF`: OFF/OFF; OFF/OFF → restart: OFF/OFF; OFF/OFF.
- jenis1-kunci/lockForPromote: master polos OFF/OFF; -/- → `mysql.lockForPromote` (kandidat replika sungguhan): ON/ON; -/ON → restart: ON/ON; -/ON.
- jenis1-kunci/lock-master: master polos OFF/OFF; -/- → `agent lock-master`: ON/ON; -/ON → restart: ON/ON; -/ON.
- jenis1-buka/unlockPromote: master polos OFF/OFF; -/- → `mysql.lockForPromote`: ON/ON; -/ON → restart: ON/ON; -/ON → `mysql.unlockPromote`: OFF/OFF; -/- → restart: OFF/OFF; -/-.
- jenis1-buka/unlock-master: master polos OFF/OFF; -/- → `agent lock-master`: ON/ON; -/ON → restart: ON/ON; -/ON → `agent unlock-master`: OFF/OFF; -/- → restart: OFF/OFF; -/-.
- jenis2-kunci: replika: OF ditulis, `SetReadOnly(true)`: ON/ON; ON/ON → `mysql.promote` terencana (kedua pernyataan pembuka tulis): OFF/OFF; OFF/OFF → `mysql.lockForPromote` (kandidat: master lama yang kini mengikutinya): ON/ON; OFF/ON → restart selagi terkunci, dibaca sebelum penjaga: ON/ON; OFF/ON → putaran penjaga pertama (`ensureLocked`): ON/ON; OFF/ON.
- jenis2-buka: sama sampai putaran penjaga pertama (ON/ON; OFF/ON) → `mysql.unlockPromote`: OFF/OFF; OFF/- → restart: OFF/ON; OFF/-.
- jenis3-dari-1: master polos OFF/OFF; -/- → `mysql.quarantine` (`persistReadOnly`; kunci start ditulis): ON/ON; ON/ON → `agent set-role master --ya` (melepas kunci start): ON/ON; ON/ON → `agent unlock-master`: OFF/OFF; ON/- → restart: ON/OFF; ON/-.
- jenis3-dari-2: replika → promote seperti jenis2 (OFF/OFF; OFF/OFF) → `mysql.quarantine`: ON/ON; ON/ON → `agent set-role master --ya`: ON/ON; ON/ON → `agent unlock-master`: OFF/OFF; ON/- → restart: ON/ON; ON/-.

Catatan: jenis2-buka menyala dengan `read_only=OFF` dan `super_read_only=ON`
sekaligus, keadaan yang tidak bisa dibuat dengan `SET` saat berjalan (OF
memasang `super_read_only`, nilai tersimpan `read_only=OFF` dimuat sesudahnya
tanpa menurunkan `super_read_only`). Apakah aplikasi bisa menulis pada
keadaan ini tidak diukur; bagaimanapun barisnya tidak memberi OFF/OFF.

- Apakah `SET PERSIST super_read_only = ON` ikut menyimpan `read_only`:
  **tidak**. Sesudahnya berjalan ON/ON (MySQL ikut menyalakan `read_only`),
  tersimpan `-`/ON; sesudah restart tetap ON/ON; -/ON (tanpa OF, karena
  `super_read_only=ON` yang dimuat menyalakan `read_only`).
- Apakah `setReadOnly(false)` diterima saat hanya `read_only` yang ON:
  **ya**. Pada master yang dipin, sesudah `SET PERSIST read_only = ON`
  (berjalan ON/OFF; tersimpan ON/-), `mysql.setReadOnly{on:false}` berhasil:
  OFF/OFF; OFF/OFF, dan sesudah restart OFF/OFF; OFF/OFF.
- Apakah server terkunci menyala bisa ditulis di head (`jenis2-kunci`), dan
  karena itu catatan pembaruan mendapat kalimat tambahan: **tidak**. Master
  jenis 2 yang di-restart selagi terkunci menyala ON/ON (tersimpan OFF/ON, OF
  ada) sebelum penjaga bekerja; catatan pembaruan tidak mendapat kalimat
  "jangan me-restart MySQL master selama promote terencana sampai agentnya
  diperbarui" (Task 24).
- Aturan field `restart_read_only` yang diturunkan dari tabel ini: (diisi
  Task 19)

## Perubahan selama pelaksanaan

(Diisi selama pelaksanaan, bernomor, dengan tanggal dan alasan.)

## Teks baru untuk dibaca pemilik

Diisi Task 24: setiap teks operator yang baru, berubah atau hilang antara
`main` (`8caee75`) dan akhir bagian A, dikelompokkan menurut tempat
tampilnya, ditulis seperti di kode. Id (T…, A…, C…, B…) mengikuti spek
5.11; id P… adalah teks rencana ini. Teks yang tidak ada di spek, dari
rencana ini atau dari pelaksana, diberi tanda "tidak ada di spek".

`%s`, `%d` dan `%v` adalah tempat nilai; `<…>` adalah nilai dari halaman
atau teks agent berlabel. "diubah" menulis teks lama lalu teks baru.

Bagian README yang ditulis ulang bagian A juga teks untuk operator dan perlu
dibaca utuh: paragraf batas percobaan di "Login pertama", "Promote", "Peran
dan sumber yang dipin", "Pembaruan", "Keamanan", dan panduan ke-10.

### Login dan konfirmasi kata sandi

(T1, T2, P8)

### Halaman pekerjaan dan halaman master: kunci promote dan "Ulangi"

(T9, T10, T10b, T11, T11b, T11c, T11d, T11s, T11t, T12, T12b, T12c, T12d,
T13, T13b, T14, T14b, T14t, T14u, T15, T15b, T16, T17, P1, P2, P6, P9)

### Layar Promote…

(T24, T24b, T24c, T24d)

### Halaman server, daftar server dan kotak Master lama

(T3, T3b, T18, T21, T21b)

### Halaman master: alamat untuk replika, status dan pengaturan tersimpan

(T4, T5, T5b, T6, T19, T20, T20b)

### Halaman replika

(T26, T27, T30, P7)

### Daftar Prasyarat

(T7, T8)

### Pengaturan

(T31)

### Dari agent

(A1)

### Keluaran CLI agent

(C1 sampai C9, P3, P4)

### README, Panduan dan spek lama

(B1, B2, B3, B4, B6; panduan ke-10; langkah pembaruan 5.12 nomor 1 sampai 4,
6 dan 7)

### Audit

(tindakan dan field baru: `promote_unlock_lock`, `agent_pair` berhasil,
`window`, `changes`, `repair`, `continuation`, `master_report_received`)
