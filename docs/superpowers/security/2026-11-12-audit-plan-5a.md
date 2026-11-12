# Audit Keamanan dbpanel — Rencana 5a (Menutup Jalur Dua Master dan Kehilangan Data)

- Tanggal: 2026-11-12
- Kode yang dijelaskan dokumen ini: cabang `claude/install-superpowers-plugin-ev2k9i` pada commit `e138d86`. Rentang Rencana 5a Task 1–15 adalah `0912c5f..765427b`; gelombang perbaikan akhir adalah `765427b..e138d86` (26 commit, dari `a9545a2` sampai `e138d86`). Untuk R-117 dan R-119 saja (sisa SEC-89 dan SEC-88), keadaan kode yang dijelaskan adalah `ef453b6` (tindak lanjut sebelum merge `bb54853` dan putaran perbaikannya `ef453b6`), yang ditemukan pemeriksaan ulang dan dikerjakan setelah audit.
- Modul: `github.com/husmin-fajrin/db-panel`
- Pembanding: audit Rencana 4a (`docs/superpowers/security/2026-11-05-audit-plan-4a.md`, SEC-54..SEC-65), review akhir Rencana 4b, 4c, dan 6 (`docs/superpowers/ledgers/`, SEC-66..SEC-84), spesifikasi Rencana 5a (`docs/superpowers/specs/2026-11-10-rencana-5a-dua-master-kehilangan-data-design.md`, terutama §5.2–§5.9 dan §7), dan inventaris risiko lintas rencana (`docs/superpowers/security/2026-11-10-inventaris-risiko.md`).
- Sumber temuan: empat pemeriksaan yang semuanya dilakukan dengan **membaca kode**, tanpa menjalankan test, panel, agent, atau mysqld:
  1. audit keamanan sisi agent (`internal/agent`, `internal/proto`, `scripts`) atas `0912c5f..343898c`, temuan SA-1..SA-12;
  2. audit keamanan sisi panel (web, orchestrator, monitor, notifier, store) atas `0912c5f..343898c`, temuan SP-1..SP-15;
  3. review seluruh cabang atas `0912c5f..343898c`, temuan FR-1..FR-17 dan butir yang tidak dinilai ("declined to judge");
  4. pemeriksaan ulang gelombang perbaikan atas `765427b..e138d86`, dua sisi (agent dan panel), dengan putusan per temuan.
  Commit `343898c..765427b` hanya mengubah inventaris risiko, jadi temuan dari `343898c` berlaku juga untuk `765427b`. Nomor baris di dokumen ini sudah dicek ulang ke `e138d86`; lokasi yang tidak bisa dicek tidak dicantumkan.
- Sifat: audit internal atas kode milik sendiri. Satu-satunya hal yang dijalankan adalah gerbang test gelombang perbaikan (Lampiran 8.1). Penulis dokumen ini tidak menjalankan test atau binary apa pun.

---

## 1. Ringkasan eksekutif

Rencana 5a menutup jalur dua master dan kehilangan data yang dicatat inventaris risiko (R-029..R-036, R-043, R-058) dan, menurut tabel spesifikasi §5.9, delapan dari sembilan jalur tambahannya (yang kesembilan, replika sebagian yang tidak terbaca penjaga, tetap terbuka sebagai R-108). Yang ditambahkan: kunci start lewat `init_file` sehingga master lama yang dikarantina menyala baca-saja walau agent mati; loop penjaga tetap 5 detik; catatan replika (host × port) yang dibaca sebelum kunci promosi dibuka; kunci promosi yang dipertahankan bila replika tercatat tidak terbaca; daftar koneksi yang jujur; token baru untuk Ulangi dan Setujui job pembuang; dan "Jangan ganggu" yang tidak membungkam jenis berbahaya.

Hasil keseluruhan: tidak ada temuan Kritis. Tidak ada jalur ke `mysql.dropDatabases` tanpa konfirmasi yang sah. Kunci start dibangun sesuai urutan yang dituntut spesifikasi (berkas sebelum baris, baris sebelum berkas). Tiga celah pada pengiriman peringatan, yang justru menjadi andalan operator saat failover, ditemukan review dan sudah diperbaiki.

Jumlah temuan (40 nomor, SEC-85..SEC-124, setelah SA-5 = FR-1 dan SP-7 = FR-15 digabung):

| Tingkat (pemeriksa; SEC-87 dinaikkan, lihat catatan) | Sebelum gelombang perbaikan | Terbuka atau sebagian sesudahnya |
|---|---|---|
| Tinggi | 3 | 3 |
| Sedang | 6 | 0 |
| Penting (review) | 2 | 0 |
| Rendah | 14 | 8 (dua di antaranya sebagian) |
| Minor (review) | 9 | 1 (sebagian) |
| Tidak dinilai (review, "declined to judge") | 2 | 1 |
| Info | 4 | 3 |
| **Jumlah** | **40** | **16** |

Aturan tingkat di dokumen ini: setiap temuan memakai tingkat yang diberikan pemeriksanya sendiri. Satu-satunya pengecualian adalah SEC-87 (SA-3): pemeriksa memberi Sedang, dan dokumen ini mencatatnya sebagai Tinggi atas keputusan pengendali karena kredensial bocor. Menurut tingkat pemeriksa saja, hitungan "Sebelum" menjadi Tinggi 2 dan Sedang 7. Temuan lain tidak dinilai ulang di sini, walau prioritasnya di inventaris bisa berbeda.

Status: **Diperbaiki 24, Diperbaiki sebagian 3, Terbuka 13, Diterima 0.** Tingkat di tabel ini adalah tingkat pemeriksa (dampak dan kemungkinan). Prioritas di inventaris risiko hanya menilai dampak, jadi beberapa butir yang di sini Rendah menjadi Tinggi di sana (misalnya SEC-103: kemungkinannya kecil, tetapi akibatnya data hilang). Peta nomor ada di §4.0.

Gelombang perbaikan diuji dengan `make test` (exit 0, 36m27s), `make test-e2e` (exit 0, 8m44s), dan `go test -race -short` untuk panel dan agent (exit 0, 38m18s, tanpa race). Proses lengkapnya di Lampiran 8.1, termasuk satu putaran sebelumnya yang gagal.

**Tiga butir yang harus diputuskan pemilik.** Ketiganya bukan bawaan Rencana 5a dan tidak diperbaiki di gelombang ini:

1. **SEC-85 (SA-1), Tinggi, kelas B.** Panel yang dibobol bisa membuka kembali master lama yang dikarantina. Caranya: `mysql.releaseRole` (diizinkan pada peran `quarantined`) membuat peran `unassigned`; penjaga lalu melepas kunci start, dan `mysql.setReadOnly{on:false}` membuka tulis secara PERSIST. **Kasus terburuk:** dua master yang sama-sama bisa ditulis, bertahan melewati restart, dan semua aplikasi yang masih menunjuk alamat lama menulis ke master lama. Penutupnya ("mode ketat": agent menolak pelepasan server yang dikarantina tanpa persetujuan root lokal) mengubah cara "Hapus server" bekerja, jadi dijadwalkan Rencana 5b. Sementara itu tidak ada yang menutupnya selain menjaga host panel.
2. **SEC-86 (SA-2), Tinggi, kelas B.** Panel yang memegang sandi replikasi (panel memang menyimpannya) bisa membuat master **sehat** mengarantina dirinya sendiri di bawah server milik penyerang, lalu membuang datanya lewat `mysql.dropDatabases` atau menimpanya dengan `mysql.cloneFrom` dari server itu. **Kasus terburuk:** produksi berhenti menulis sampai root turun tangan, lalu data master produksi hilang atau diganti salinan penyerang. Bila tidak ada replika yang sinkron, tidak ada salinan yang tersisa. Dijadwalkan Rencana 5b (persetujuan root lokal sebelum bangun ulang yang menyusul karantina). Sementara itu, sama dengan SEC-85, satu-satunya pertahanan adalah menjaga host panel.
3. **SEC-87 (SA-3), Tinggi (dinaikkan dari Sedang, lihat §4).** Pada setiap promote terencana, agent master mengirim sandi akun replikasi, tanpa verifikasi sertifikat, ke setiap host yang tercatat sebagai replika dalam setahun terakhir di setiap port yang tercatat. Rencana 5a melebarkan ini dari satu port ke semua port tercatat. **Kasus terburuk:** perangkat yang mengambil alamat bekas replika menerima sandi `dbpanel_repl` (REPLICATION SLAVE, BACKUP_ADMIN), lalu menyalin atau meng-CLONE seluruh basis data pasien dari master. Penyerang tidak perlu posisi on-path. Penutup tuntasnya adalah akun baca per replika di Rencana 5c (R-038, R-108). Sementara itu: jalankan "Perketat" (verifikasi sertifikat) di topologi, dan jalankan `sudo dbpanel agent forget-replica <host>` di master setiap kali replika dipensiunkan.

Butir terbuka lain, semuanya dengan baris inventaris dan rencana penyerapnya, ada di §7. Ringkasnya:
- Rencana 5b: SA-9 (kunci terlihat terpasang padahal berkas opsi milik pengguna `mysql` mengalahkannya), laporan start yang hilang bila agent restart sebelum terkirim, dan `set-role master` yang butuh kunci rahasia agent. Juga R-117: urutan kirim-laporan sebelum penegakan baca-saja ditemukan pemeriksaan ulang dan sudah diperbaiki di dalam satu putaran setelah audit (`bb54853`, `ef453b6`); yang tersisa, pengiriman yang memblok memperpanjang jeda sebelum putaran berikutnya, tetap di Rencana 5b.
- Rencana 5c: tanda "port penuh" yang bisa diisi siapa pun yang memegang sandi replikasi.
- Rencana 5d: `leftoverDatabases` tanpa sinyal potong.
- Rencana 5e: sisanya.

## 2. Lingkup dan metode

**Rentang kode.**
- Audit agent, audit panel, dan review seluruh cabang membaca `0912c5f..343898c`.
- Pemeriksaan ulang membaca diff gelombang perbaikan `765427b..e138d86` beserta testnya, ditambah pembacaan terarah di `e138d86`.
- Dokumen ini dan inventarisnya dicek ke `e138d86`.

**Yang dibaca.**
- *Agent:* diff kode agent seluruhnya dalam lima putaran. Di luar diff: `pins.go`, `realhost.go`, `layout.go`, `peer.go`, `promote_lock.go`, `promote_files.go`, `quarantine.go`, `status.go`, `replication.go`, `configure.go`, `databases.go`, `command.go`, `agent/cli.go`, `agent/session.go`, `agent/runtime.go`, `agent/monitor/monitor.go`, unit `dbpanel-agent.service`, dan `go-sql-driver/mysql v1.9.3` (`auth.go`, `packets.go`) untuk perilaku autentikasi dan ukuran paket yang menjadi dasar dua temuan.
- *Panel:* diff web, orchestrator, monitor, dan store seluruhnya. Di luar diff: `web/router.go`, `handlers_auth.go`, `auth/*`, `handlers_confirm.go`, `store/confirmations.go`, `handlers_jobs.go`, `handlers_controls.go`, `handlers_mysql.go`, `handlers_oldmaster.go`, `audit.go`, template yang diubah, `web/static/app.js`, `orchestrator/{runner,create_replica,change_databases,resync,snapshot,oldmaster,promote}.go`, `monitor/{alerts,promote,state}.go`, `notify/*`, `store/{servers,alerts,migrate}.go`, `hub/hub.go`, dan `panel/listener_impl.go`. Test diff dicari per sifat (bukan dibaca dari awal sampai akhir) untuk mengetahui sifat mana yang dipin test.
- *Review seluruh cabang:* diff agent/proto, monitor/notify/store, orchestrator, dan web seluruhnya, tambahan README sebagian, triase ledger, dan sumber di HEAD untuk mengikuti risiko.
- *Pemeriksaan ulang:* diff gelombang perbaikan (kode dan test) dua sisi, dengan pembacaan terarah di `e138d86`.

**Cara.**
- Semua pemeriksaan membaca kode. Navigasi memakai `graphify query` lebih dulu, lalu berkas mentah dibaca hanya pada baris yang ditemukan graf atau diff.
- Tidak ada pemeriksa yang menjalankan `go test`, panel, agent, atau mysqld. Setiap temuan menyebut test yang akan membuktikan dan mempinnya.
- Yang dijalankan hanya gerbang test gelombang perbaikan (`make test`, `make test-e2e`, `go test -race -short`). Hasilnya di Lampiran 8.1.
- Penyerang yang dipertimbangkan:
  - pengguna jaringan tanpa autentikasi;
  - sesi curian tanpa sandi;
  - admin yang dijebak tautan atau halaman lintas situs;
  - panel yang dibobol (dengan sandi replikasi yang disimpannya);
  - agent atau MySQL yang dibobol dan mengirim teks atau kejadian palsu;
  - pemegang sandi replikasi bersama;
  - pengguna OS `mysql` di master lama;
  - perangkat yang mengambil alamat bekas replika.
- Panel tidak punya peran berhak rendah (satu admin, R-091), jadi otorisasi antar pengguna tidak dinilai sebagai kontrol.

**Yang tidak dicakup** (dari bagian "tidak dicakup" setiap pemeriksaan, tidak disingkat):
- Perilaku saat berjalan secara umum. Klaim tentang MySQL (bentuk keluaran `--print-defaults`, `init_file`, `~/.my.cnf`) bersandar pada eksperimen yang dicatat spesifikasi dan perilaku MySQL yang terdokumentasi, bukan pada run pemeriksa.
- `testmysql`, helper e2e, dan berkas ekspor khusus test, kecuali untuk mengecek cakupan.
- Logika penundaan `rebuild_load.go` dan kode salvage (tidak berubah di 5a, tidak diaudit ulang).
- Perilaku browser: perilaku `<dialog>` bertumpuk hanya dibaca, tidak dicoba.
- Perilaku Telegram sungguhan.
- Pengukuran waktu balapan (race).
- Daftar yang belum dibuktikan ada di Lampiran 8.3.

## 3. Model ancaman delta

Model ancaman Rencana 1–4 tetap berlaku (audit Rencana 4a §3 butir 18–24). Rencana 5a menambah aset dan mengubah batas berikut.

- **Aset baru:**
  - berkas kunci start (`init_file`) dan barisnya di konfigurasi MySQL terkelola, ditulis root di direktori include atau direktori berkas konfigurasi utama;
  - catatan replika `/var/lib/dbpanel/replicas-seen` (host, setahun) dan `/var/lib/dbpanel/replica-ports-seen` (port, kini paling banyak 8 dengan tanda `penuh`);
  - daftar `Readable` di berkas kunci promosi;
  - antrean laporan start di memori agent (paling banyak 16);
  - kolom `snooze_from` (migrasi 0011) dan field job-state baru (`WipeOnly`, `AddDrop`, `ConfirmedAt`, `WaitList`).
- **Batas yang berubah:**
  - Kunci start membuat master lama menyala baca-saja **tanpa** agent. Pertahanan ini ditujukan pada restart tanpa agent, **bukan** pada root atau pengguna OS `mysql`: keduanya bisa menyalakan mysqld sendiri di direktori data. Jawabannya tetap penjaga 5 detik.
  - Kunci itu hanya sekuat apa yang dibaca `mysqld --print-defaults` di lingkungan agent (SEC-98) dan tata letak direktori yang tidak diperiksa pemiliknya (SEC-96).
  - Loop penjaga 5 detik yang terpisah dari interval pemeriksaan. Ia menjalankan kunci start, penjaga karantina, penjaga kunci promosi, pemutus FTWRL, dan pencatatan replika dalam satu loop, sehingga satu operasi yang macet di dalamnya mematikan semuanya (SEC-90).
  - Catatan replika host × port berasal dari koneksi yang dilihat master sendiri, tidak pernah dari panel. Namun port dipilih replika (`report_port`), dan host bisa diisi siapa pun yang login dengan hak REPLICATION SLAVE (SEC-86, SEC-88).
  - Kunci promosi dipertahankan selama replika tercatat tidak terbaca. Arah gagal ini aman bagi data tetapi bisa menahan tulis (R-098).
  - Pembacaan replika tercatat memakai akun replikasi kandidat, tanpa verifikasi sertifikat kecuali topologi sudah "Perketat" (SEC-87).
  - Daftar koneksi mencakup semua thread klien. Yang disembunyikan hanya akun replikasi dan dump buatan agent serta sesi agent dari server itu sendiri yang bertanda `program_name=dbpanel-agent`. Daftar ini menjadi syarat bangun ulang master lama, juga saat Setujui (SEC-91).
  - Ulangi dan Setujui job pembuang memakai token baru yang terikat pada job, dengan batas 24 jam. Setujui terikat pada hash daftar yang ditampilkan (SEC-102, SEC-103).
  - Jenis peringatan yang tidak pernah dibungkam "Jangan ganggu" ditentukan daftar izin (SEC-122), dengan ringkasan saat senyap berakhir.
- **Butir baru model ancaman:**
  - 25. **Panel yang dibobol vs master lama yang dikarantina:** `mysql.releaseRole`, `claimRole`, pelepasan kunci start pada peran `master` (SEC-85).
  - 26. **Panel yang dibobol, atau pemegang sandi replikasi, vs master sehat:** gerbang "pernah tercatat sebagai replika" yang bisa diisi server milik penyerang (SEC-86); port tercatat tanpa batas (SEC-88).
  - 27. **Perangkat di alamat bekas replika:** menerima sandi replikasi pada promote terencana (SEC-87) dan bisa menjawab sebesar apa pun (SEC-99).
  - 28. **Pengguna OS `mysql` di master lama:** log error (SEC-90), berkas opsi di home akun layanan (SEC-98), dan direktori berkas kunci (SEC-96).
  - 29. **Agent yang dibobol vs monitor panel:** banjir kejadian (SEC-93), kejadian yang tidak sesuai peran pengirimnya (SEC-107), dan teks tanpa batas (SEC-108).
  - 30. **Operator yang terburu-buru saat failover:** persetujuan yang menunggu lama (SEC-91), peringatan yang tertutup oleh perubahan peran (SEC-92), dan perintah lokal yang tidak menyebut apa yang dilewatinya (SEC-100).

## 4. Temuan

### 4.0 Tabel temuan

Kolom "Asal" menyebut nomor asli: SA = audit keamanan sisi agent, SP = audit keamanan sisi panel, FR = review seluruh cabang. Kolom "Tingkat" adalah tingkat yang diberikan pemeriksanya. Review memakai Penting/Minor, dan dua butirnya tidak dinilai. Prioritas di inventaris bisa berbeda karena inventaris hanya menilai dampak.

| ID | Asal | Tingkat | Judul | Status | Commit / baris inventaris |
|---|---|---|---|---|---|
| SEC-85 | SA-1 | **Tinggi** | Panel yang dibobol membuka master lama yang dikarantina lewat `releaseRole`; kunci start ikut lepas | **Terbuka** | R-109 (Rencana 5b) |
| SEC-86 | SA-2 | **Tinggi** | Panel pemegang sandi replikasi mengarantina master sehat di bawah server miliknya, lalu membuang datanya | **Terbuka** | R-110 (Rencana 5b) |
| SEC-87 | SA-3 | **Tinggi** (dinaikkan; pemeriksa: Sedang) | Sandi replikasi ditawarkan tanpa verifikasi ke setiap host × port tercatat setahun | **Terbuka** | R-111 (Rencana 5c); R-039 |
| SEC-88 | SA-4 | Sedang | Port tercatat dari replika tanpa batas; pembacaan saat kunci 10 menit, kunci terlambat, kunci yang tidak terbaca | Diperbaiki | 8d9be83, ee3ab70; sisa R-118, R-119 |
| SEC-89 | SA-5 = FR-1 | Sedang / Penting | Laporan start sekali kirim hilang bila sesi panel belum tersambung | Diperbaiki | a9545a2; sisa R-116, R-117 (R-117: urutan dalam putaran diperbaiki setelah audit di bb54853; jeda antarputaran tetap terbuka) |
| SEC-90 | SA-6 | Sedang | Penjaga membuka log error MySQL sebagai root dengan `open` biasa (FIFO menggantung loop penjaga) | Diperbaiki | b7dd3db; sisa R-122 |
| SEC-91 | SP-1 | Sedang | Setujui bangun ulang master lama tidak membaca ulang koneksinya | Diperbaiki | daa12b9 |
| SEC-92 | SP-2 | Sedang | `master_start_late` tertutup oleh perubahan peran bangun ulang; form mempercayai pemeriksaan yang lebih tua | Diperbaiki | a70a82a, 427ca73 |
| SEC-93 | SP-3 | Sedang | Kejadian agent selain laporan kesehatan tidak dibatasi lajunya | Diperbaiki | 9f87ccc |
| SEC-94 | FR-2 | Penting | Perubahan kunci start bukan "perubahan" bagi pelapor agent; "belum diperiksa" palsu saat agent start | Diperbaiki | 33ad13f |
| SEC-95 | FR-3 | Penting | Host kunci promosi yang dipertahankan bisa tertahan sampai sejam | Diperbaiki | 2d52c10 |
| SEC-96 | SA-7 | Rendah | Berkas kunci start: operasi lewat path yang mengikuti symlink; direktori tidak diperiksa | **Diperbaiki sebagian** | 8e93bae; sisa R-115 |
| SEC-97 | SA-8 | Rendah | Kunci start belum di disk saat perintah menjawab (direktori tidak di-sync) | **Terbuka** | R-112 (Rencana 5e) |
| SEC-98 | SA-9 | Rendah | `StartLocked` dihitung di lingkungan agent, bukan lingkungan layanan MySQL | **Terbuka** | R-113 (Rencana 5b); R-097 |
| SEC-99 | SA-10 | Rendah | Jawaban peer tidak dibatasi ukurannya | Diperbaiki | 7c76beb |
| SEC-100 | SA-11 | Rendah | `unlock-master` dan `forget-replica` tidak menyebut apa yang dilewatinya | Diperbaiki | c2133b3 |
| SEC-101 | SP-4 | Rendah | Job yang dibuat sebelum promote tetap bertindak atas master lama | Diperbaiki | 7df4b48 |
| SEC-102 | SP-5 | Rendah | Daftar yang dibuang persetujuan `WaitWipe` hanya tampil lewat pesan yang dipotong 4 KiB | Diperbaiki | 67fd2bd |
| SEC-103 | SP-6 | Rendah | Persetujuan yang mengantre bisa dipakai oleh penantian berikutnya di langkah yang sama | **Terbuka** | R-124 (Rencana 5e) |
| SEC-104 | SP-7 = FR-15 | Rendah / Minor | Jalur tanpa token ke salinan lebih luas dari R-105 | Diperbaiki | b9ec203 (R-105 ditutup) |
| SEC-105 | SP-8 | Rendah | Log audit tidak mencatat apa yang diakui | **Terbuka** | R-125 (Rencana 5e); R-067 |
| SEC-106 | SP-9 | Rendah | Kejadian kunci/start yang terlalu besar atau tidak terbaca tidak membuka peringatan | Diperbaiki | 7c76beb |
| SEC-107 | SP-10 | Rendah | Peringatan dari kejadian tidak terikat pada peran pengirim | **Terbuka** | R-126 (Rencana 5e) |
| SEC-108 | SP-11 | Rendah | Teks agent di luar batas `agentText` | **Diperbaiki sebagian** | 69b05d8; sisa R-127 |
| SEC-109 | SP-12 | Rendah | `?buka=` membuka dialog destruktif pada GET biasa | **Terbuka** | R-128 (Rencana 5e) |
| SEC-110 | FR-4 | Minor | Start yang gagal karena berkas kunci hilang tidak pernah dilaporkan | Diperbaiki | 67e172f; sisa R-123 |
| SEC-111 | FR-5 | Minor | Tambah database membaca ulang replika di langkah dump hanya bila `AddDrop` tidak kosong | Diperbaiki | df9ffc1 |
| SEC-112 | FR-6 | Minor | Pemotongan inventaris (5.000 database) diam-diam | Diperbaiki | df9ffc1 (R-104 ditutup); sisa R-130 |
| SEC-113 | FR-8 | Minor | Pembukuan ringkasan "Jangan ganggu": ringkasan bisa hilang | Diperbaiki | 66ff084 |
| SEC-114 | FR-9 | Minor | `forget-replica`: menolak `host:port` (a); melupakan replika yang masih tersambung (b) | **Diperbaiki sebagian** | 8d9be83 untuk (a); (b) R-120 (Rencana 5e) |
| SEC-115 | FR-10 | Minor | Peringatan `master_lock_kept` memberi `unlock-master` tanpa kehati-hatian yang ada di README | Diperbaiki | c2133b3 |
| SEC-116 | FR-13 | Minor | Penjaga master lama: penegakan baca-saja setelah urusan kunci start; probe salam tiap 5 detik | Diperbaiki | 67e172f |
| SEC-117 | FR-14 | Minor | `lock-master` membaca lalu menulis kunci di luar satu bagian kritis | Diperbaiki | aff8906 |
| SEC-118 | ledger Task 13 m3 (review) | Minor | Tambah database bisa membuang nama yang sama (beda huruf) dengan database yang diikuti | Diperbaiki | df9ffc1 |
| SEC-119 | review: "declined to judge" baris 1 dan rekomendasi 4 | Tidak dinilai | `mysql.unlockPromote` membuka master tanpa membaca replika di daftar kunci | Diperbaiki | 81ccdad |
| SEC-120 | review: "declined to judge" baris 4 | Tidak dinilai | `set-role master` (jalan keluar lokal) butuh kunci rahasia agent dan tata letak MySQL | **Terbuka** | R-121 (Rencana 5b) |
| SEC-121 | SA-12 | Info | Path kunci start ditulis ke berkas opsi tanpa pemeriksaan karakter khusus | Diperbaiki | c2133b3 |
| SEC-122 | SP-13 | Info | Jenis yang tidak pernah dibungkam berupa daftar izin | **Terbuka** | R-129 (Rencana 5e) |
| SEC-123 | SP-14 | Info | Tidak ada test yang mempin penjaga rute baru `POST /jobs/{id}/retry/prepare` | **Terbuka** | Rencana 5e (tanpa baris inventaris) |
| SEC-124 | SP-15 | Info | Pengerasan header opsional | **Terbuka** | Rencana 5e (tanpa baris inventaris) |

Putusan pemeriksaan ulang gelombang perbaikan: semua butir yang diperbaiki dinilai ADDRESSED, kecuali dua yang dinilai PARTLY (SEC-96/SA-7 dan SEC-108/SP-11). Tidak ada yang NOT ADDRESSED. Tidak ada kerusakan baru tingkat Kritis atau Penting. Kerusakan kecil yang ditemukannya dicatat di bawah temuan induknya.

---

### SEC-85 — Panel yang dibobol membuka master lama yang dikarantina (SA-1)
- **Tingkat:** Tinggi. Pemeriksa menilai Tinggi, bukan Kritis, walau rumusannya cocok dengan Kritis, karena tiga alasan: butuh panel yang sepenuhnya dibobol; kerugian setingkat panel sudah diterima sebagai kelas B (R-019, R-021); dan jalurnya rancangan Rencana 4b, bukan buatan 5a. Pemilik boleh menilai lain.
- **Penyerang:** pengendali proses panel. Ia bisa mengirim perintah agent apa pun lewat kanal yang terautentikasi.
- **Lokasi (dicek di `e138d86`):**
  - `internal/agent/drivers/mysql/pins.go:111-116`: daftar tolak peran `quarantined` hanya `mysql.lockForPromote`, `mysql.promote`, `mysql.repoint`, ditambah warisan peran replika. `mysql.releaseRole` tidak ditolak.
  - `replication.go:172-187`: `releaseRole` hanya meminta tidak ada sumber replikasi dan tidak ada replika yang membaca binlog (`refuseReplicating`), lalu memanggil `Pins.Release` (`pins.go:523`).
  - `pins.go:598` (`claimRole`): `mysql.configure{role:"master"}` pada server `unassigned` mempin master.
  - `startlock_guard.go:160`: kunci start dilepas tanpa bukti baca-saja bila peran `master`. Pada peran lain, kunci dilepas begitu baca-saja terbukti, dan hal itu langsung terpenuhi setelah karantina.
- **Skenario:**
  1. Master lama yang dikarantina, MySQL hidup, replika sudah diarahkan ulang.
  2. Panel mengirim `mysql.releaseRole`, dan peran menjadi `unassigned`.
  3. Dalam satu putaran penjaga (5 detik), baris `init_file` dan berkas kunci dihapus.
  4. Panel mengirim `mysql.setReadOnly{on:false}`, dan kedua bendera di-PERSIST OFF.
  5. Varian: `mysql.configure{role:"master"}` dulu, sehingga kunci dilepas seketika tanpa bukti.
- **Dampak:** dua master yang bisa ditulis, juga setelah restart.
- **Yang dikerjakan gelombang perbaikan:** komentar kepala `pins.go:22-35` kini menyebut jalur ini terang-terangan (c2133b3) dan tidak lagi mengklaim panel yang dibobol "tidak pernah" bisa membuang data. Kodenya tidak diubah.
- **Status: Terbuka.** Masuk Rencana 5b ("mode ketat": tolak pelepasan server yang dikarantina tanpa persetujuan root lokal; ini mengubah cara "Hapus server" bekerja). Baris inventaris R-109 (kelas B, Tinggi). **Saran sementara:** lindungi host panel. Hari ini tidak ada hal lain yang menutup jalur ini.
- Usul pemeriksa yang belum dikerjakan: tolak `mysql.releaseRole` pada peran `quarantined`; tolak `mysql.configure{role:"master"}` dan `mysql.setReadOnly{on:false}` dari panel selama baris `init_file` agent masih ada. Test yang akan mempinnya: `releaseRole` pada rig yang dipin `quarantined` ditolak, dan baris serta berkas kunci tetap ada setelah satu tik penjaga.

### SEC-86 — Master sehat dikarantina di bawah server milik penyerang, lalu datanya dibuang (SA-2)
- **Tingkat:** Tinggi (alasan bukan Kritis sama dengan SEC-85).
- **Penyerang:** pengendali proses panel. Panel menyimpan sandi replikasi, jadi penyerang juga memegangnya. Ia butuh satu host yang menjangkau port MySQL master; host panel sendiri sudah cukup.
- **Lokasi:**
  - `quarantine.go:107-121`: pada master yang tidak dikunci untuk kandidat itu, `mysql.quarantine` hanya meminta host itu ada di catatan replika (`seenAddr`). Catatan itu diisi setiap host yang punya sesi `Binlog Dump`, dari akun apa pun yang ber-REPLICATION SLAVE.
  - `quarantine.go:149` (`selfQuarantine`).
  - `pins.go:693` (`Allow`): server `quarantined` dengan pin sumber menerima `mysql.dropDatabases`, serta `mysql.dumpFrom`/`mysql.cloneFrom` dari sumber yang dipin.
- **Skenario:**
  1. Dari host H, penyerang login sebagai `dbpanel_repl` dan memulai dump binlog. H tercatat dalam 5 detik, atau seketika lewat `mysql.recordReplicas`.
  2. H dibuat bisa ditulis, lalu `mysql.quarantine{new_master: H, verify:false}` dikirim. Semua pemeriksaan lolos.
  3. Master produksi mem-PERSIST baca-saja, dipin `quarantined` di bawah H, dan mendapat kunci start.
  4. Penyerang menghentikan replika sungguhan lewat agentnya, menutup sesi dump-nya, lalu mengirim `mysql.dropDatabases` atau `mysql.cloneFrom` dari H.
- **Dampak:** henti tulis produksi sampai root turun tangan (`set-role master`), lalu data master produksi dibuang atau diganti. Rencana 5a membuat jalur ini lebih cepat (loop 5 detik, `recordReplicas` sesuai permintaan) tetapi tidak membuatnya baru. Kemungkinannya sama dengan sebelum 5a.
- **Yang dikerjakan gelombang perbaikan:** hanya komentar `pins.go` (c2133b3).
- **Status: Terbuka.** Masuk Rencana 5b (persetujuan root lokal sebelum bangun ulang yang menyusul karantina, yaitu "mode ketat" untuk satu kelompok perintah). Baris inventaris R-110 (kelas B, Tinggi). **Saran sementara:** lindungi host panel.

### SEC-87 — Sandi replikasi ditawarkan tanpa verifikasi ke setiap host × port tercatat (SA-3)
- **Tingkat:** Tinggi, satu-satunya tingkat yang dinaikkan dalam dokumen ini. Pemeriksa memberi Sedang. Keputusan pengendali menaikkannya ke Tinggi karena kredensial bocor (definisi Tinggi di inventaris), dan pemeriksa sendiri menyebut Tinggi untuk baris inventarisnya.
- **Penyerang:** (a) perangkat yang mengambil IP bekas replika (dalam setahun terakhir), atau (b) pengguna lokal tanpa hak di host yang tercatat, pada port tercatat yang tidak dipakai. Tidak perlu akun MySQL atau posisi on-path.
- **Lokasi:**
  - `promote_lock.go:225`, `:335-353` (`lockReadTargets`: setiap host tercatat × setiap port tercatat ditambah port kandidat, dengan akun kandidat);
  - `promote_files.go:368` (`seenCutoff`: setahun);
  - `peer.go:89-90` (tanpa `Verify`: TLS `skip-verify`).
  - Perilaku driver yang menjadi dasarnya dicek pemeriksa di `go-sql-driver/mysql@v1.9.3/auth.go:400-406`: pada "perform full authentication" `caching_sha2_password` di atas TLS (terverifikasi atau tidak), sandi dikirim polos di dalam sesi TLS itu.
- **Skenario:** penyerang mendengarkan di alamat bekas replika dengan program yang berbicara protokol jabat tangan MySQL, menawarkan TLS dengan sertifikat apa pun, dan meminta autentikasi penuh. Pada promote terencana berikutnya, agent master mengirim sandi `dbpanel_repl`.
- **Dampak:** dengan REPLICATION SLAVE dan BACKUP_ADMIN, penyerang bisa mereplikasi atau meng-CLONE seluruh basis data. Sandi yang sama juga menjadi titik awal SEC-86 dan SEC-88.
- **Yang baru di 5a:** sebelumnya akun ini hanya dipakai penjaga setelah promote terencana gagal, dan hanya di port kandidat. Topologi yang sudah "Perketat" tidak terkena: `ReadPeer` (`peer.go:75-88`) memeriksa rantai dan nama sebelum autentikasi.
- **Yang dikerjakan gelombang perbaikan:** catatan README (2fc63a4): jalankan `forget-replica` saat replika dipensiunkan, dan "Perketat" menutup paparan ini. Pemeriksaan ulang menilai catatan itu sesuai kode.
- **Status: Terbuka.** Masuk Rencana 5c (akun baca per replika berhak rendah; R-038, R-108). Baris inventaris R-111 (kelas C, Tinggi); R-039 ditulis ulang karena SEC-87 memperburuknya (tidak perlu posisi on-path). **Saran sementara:** jalankan "Perketat" di topologi, dan `sudo dbpanel agent forget-replica <host>` di master saat replika dipensiunkan. Usul jangka pendek pemeriksa (sematkan kunci publik TLS per host saat dicatat) belum dikerjakan.

### SEC-88 — Port tercatat dari replika, tanpa batas (SA-4)
- **Tingkat:** Sedang.
- **Penyerang:** siapa pun yang memegang akun ber-REPLICATION SLAVE di master: host replika yang dibobol, atau bekas replika yang masih tahu sandi bersama (R-038). Bukan panel.
- **Masalah di `343898c`:**
  - Port diambil dari `report_port` replika tanpa batas, disimpan setahun.
  - Pembacaan saat kunci mencakup host × port, 16 sekaligus, 5 detik masing-masing, dalam konteks permintaan 10 menit, sementara panel menunggu 2 menit.
  - Daftar `Readable` ditulis tanpa batas tetapi dibaca kembali dengan batas 64 KiB.
  - Empat akibatnya: (1) promote terencana bisa diblokir; (2) kunci terpasang setelah panel menyerah; (3) master terkunci oleh berkas yang tidak bisa dibacanya sendiri; (4) pencatatan berhenti setelah 1 MiB.
- **Perbaikan:** 8d9be83, ee3ab70.
  - `maxSeenPorts = 8` dengan tanda `penuh` (`promote_files.go:76`), dan kunci ditolak selama tanda itu ada (`promote_lock.go:216-223`).
  - Paling banyak `maxLockReads = 64` pembacaan host:port (`:225-232`) dalam `lockReadBudget = 60 s` (`:233-246`). Bila salah satu terlampaui, kunci ditolak dan tidak ada yang ditulis.
  - `writeJSON` menolak rekaman di atas `maxRecordSize` (`promote_files.go:349-350`).
  - `writeSeen` membuang host tertua agar berkas tetap terbaca.
  - CLI baru `forget-replica-port {<port>|semua}`.
- **Test pin:** `TestSeenPortsCapped`, `TestSeenFileNeverOverReadLimit`, `TestLockTooLargeNotWritten`, `TestLockForPromoteFanOutBounded` (count, budget, ports full), `TestForgetReplicaPortCLI`.
- **Putusan pemeriksaan ulang:** ADDRESSED. Keempat jalur bahaya tertutup. RED-nya berupa kegagalan build, tetapi pemeriksa ulang memastikan setiap test akan gagal pada kode lama.
- **Status: Diperbaiki.**
- **Sisa yang dicatat:**
  - Tanda `penuh` yang gagal-tertutup menurunkan ambang pemblokiran: sembilan registrasi replika dengan `report_port` berbeda sudah cukup memblokir setiap promote terencana. Penyerang bisa mengisinya lagi setelah root membersihkannya. Tidak ada field kesehatan yang menunjukkannya sebelum promote dicoba. Di atas 1 MiB, host tertua dibuang dari catatan, dan host itu tidak lagi diterima sebagai sumber karantina. → R-118 (Rencana 5c/5e).
  - `forget-replica-port semua` ikut melupakan port replika yang tidak tersambung saat itu. Replika yang terjangkau tetapi replikasinya berhenti, di port lain, tidak dibaca pada kunci berikutnya. → R-119 (Rencana 5e); satu kalimat README ditambahkan. Ditemukan pemeriksaan ulang; setelah audit, `bb54853` dan `ef453b6` membuat `semua` menyebut path berkas, setiap port yang dihapusnya, dan peringatan bahwa port replika yang tidak tersambung sekarang tidak dicatat lagi dengan sendirinya (bila yang ada hanya tanda penuh, ia mengatakannya tanpa peringatan port); teks bantuan dan pesan hapus satu port menyebut hal yang sama (dipin `TestForgetReplicaPortCLI`). Perilakunya tidak berubah, jadi R-119 tetap terbuka: operator kini diperingatkan.
  - Terbuka (kecil), Rencana 5e: batas ukuran `writeJSON` kini juga berlaku untuk `WritePromoted`. `gtid_executed` di atas ~64 KiB akan menggagalkan `mysql.promote` setelah pin master. Hampir tidak terjangkau dalam praktik.

### SEC-89 — Laporan start sekali kirim hilang bila panel belum tersambung (SA-5 = FR-1)
- **Tingkat:** Sedang (audit agent) / Penting (review).
- **Masalah di `343898c`:** `monitor.quarantineStart` (`late`, `bypassed`) dan `monitor.quarantineStartFailed` dikirim sekali. Bila sesi WebSocket belum ada, misalnya saat boot (tik penjaga pertama berlomba dengan dial) atau saat jaringan terbelah, laporan itu hilang. Panel tidak membuka `master_start_late`, sehingga operator bisa membangun ulang master lama tanpa menyelamatkan tulisan dari jendela itu. README menjanjikan sebaliknya.
- **Perbaikan:** a9545a2. Laporan start masuk antrean memori `pendingStart` (paling banyak 16; lewat dari itu dibuang satu laporan: `ok` tertua, atau bila tidak ada `ok`, laporan tertua, yang bisa saja `late`) lewat `emitStart` (`startlock_guard.go:349-367`). `flushStartEvents` (`:375-392`) mengirimnya di awal setiap `GuardTick` dan berhenti pada penolakan pertama. (Sejak `bb54853` pengirimannya pindah ke akhir putaran; lihat R-117 di bawah.)
- **Test pin:** `TestQuarantineStartReportRetriedUntilDelivered` (late, start failed).
- **Putusan pemeriksaan ulang:** ADDRESSED.
- **Status: Diperbaiki.**
- **Sisa yang dicatat:**
  - Antrean hanya di memori. Agent yang restart sebelum laporan terkirim kehilangan laporan itu, lalu melaporkan `ok` (ia membaca bendera yang sudah di-PERSIST-nya). "Terkirim" berarti tertulis ke WebSocket, belum diterima panel. → R-116 (Rencana 5b: bawa hasil start di snapshot kesehatan; ini perubahan protokol). **Saran sementara:** jalankan "Periksa data tertinggal" sebelum setiap bangun ulang master lama, apa pun yang ditampilkan halaman.
  - `flushStartEvents` berjalan sebelum `guardQuarantined` (`promote_lock.go:557` sebelum `:562`). Sesi panel yang soketnya tidak mengalir bisa menunda penguncian ulang putaran itu sampai 10 detik, sekali per sesi rusak. → R-117 (Rencana 5b). Baca-saja sudah ditegakkan pada putaran yang mengantrekan laporan. Penundaan hanya berarti bila sesuatu membuat server bisa ditulis lagi di antaranya. Perbaikannya memindahkan satu baris dan bisa dikerjakan sebelum atau sesudah merge. **Diperbaiki sebagian setelah audit** (ditemukan pemeriksaan ulang). Di dalam satu putaran, urutannya kini benar:
    - `bb54853`: `GuardTick` mengirim laporan start yang menunggu lalu kejadian karantina dari `guardQuarantined` di akhir putaran lewat `defer` (`sendRoundEvents`), setelah penegakan baca-saja, urusan kunci start, dan penjagaan kunci promosi master, pada setiap jalan keluar putaran yang mendapat `promoteMu`; `emitStart` hanya mengantre. Dipin `TestGuardSendsReportsAfterReadOnlyAndHousekeeping` dan `TestGuardFlushesStartReportsOnEveryPath`.
    - Tiga pengiriman lain tetap langsung, masing-masing tindakan terakhir jalurnya (sesudahnya hanya `sendRoundEvents`): `EventLockExpired` dari `keepLock` dan dari pembukaan kunci, serta `EventQuarantined` dari `selfQuarantine`. Sejak `ef453b6`, `selfQuarantine` menulis kunci start sebelum mengirim kejadiannya (dipin `TestSelfQuarantineWritesStartLockBeforeNotice`).
    - **Yang tersisa (R-117 tetap terbuka, Rencana 5b bersama R-116):** loop penjaga baru menunggu `GuardEvery` setelah putaran selesai, pengiriman termasuk (`agent/monitor/monitor.go` `guardLoop`). Pengiriman yang memblok sampai `writeTimeout` (10 detik, `agent/session.go`) di akhir putaran N tetap menunda putaran N+1, jadi penguncian ulang bisa datang sekitar 15 detik setelah penegakan sebelumnya, sekali per sesi rusak. Yang membaik: putaran yang bertemu sesi rusak kini menegakkan baca-saja lebih dulu. Penutup sebenarnya mengeluarkan pengiriman penjaga dari jalur kritis penjaga.
  - Batas laju panel (SEC-93) membuang kejadian tanpa memberi tahu agent. Agent yang tersambung ulang dengan lebih dari 10 laporan berjenis sama bisa kehilangan sebagian, tetapi yang pertama dari setiap jenis tetap lolos.

### SEC-90 — Log error MySQL dibuka root dengan `open` biasa (SA-6)
- **Tingkat:** Sedang.
- **Penyerang:** pengguna OS `mysql` di master lama yang dikarantina, saat MySQL mati.
- **Masalah di `343898c`:**
  - Mengganti log dengan FIFO membuat `open(2)` menggantung selamanya di dalam `GuardTick` dengan `promoteMu` dipegang.
  - Akibatnya semua penjaga di loop itu berhenti (kunci start, karantina, kunci promosi, pemutus FTWRL, pencatatan replika) tanpa peringatan, sementara laporan kesehatan tetap jalan.
  - Varian lain: symlink ke node perangkat dibuka sebagai root.
- **Perbaikan:** b7dd3db. `readTail` (`startlock_guard.go:401-402`) membuka dengan `O_RDONLY|O_NOFOLLOW|O_NONBLOCK|O_CLOEXEC`, menjalankan `fstat`, dan hanya membaca berkas biasa. Selain itu dicatat sebagai "tidak terbaca" paling banyak sekali semenit.
- **Test pin:** `TestReadTailOnlyRegularFiles` (FIFO dengan watchdog 5 detik, symlink).
- **Putusan pemeriksaan ulang:** ADDRESSED.
- **Status: Diperbaiki.**
- **Sisa:** log error yang sah tetapi berupa symlink tidak dibaca lagi (pertukaran yang memang diminta SA-6). Start yang gagal pada tata letak itu tidak dilaporkan; jejaknya hanya peringatan lokal sekali semenit. → R-122 (Rencana 5e); satu kalimat README ditambahkan.

### SEC-91 — Setujui bangun ulang master lama tidak membaca ulang koneksinya (SP-1)
- **Tingkat:** Sedang. Celah waktu cek/waktu pakai (TOCTOU) pada pengakuan keselamatan. Tidak butuh penyerang.
- **Masalah di `343898c`:** koneksi master lama dibaca saat job mulai. Langkah yang membuang data ada di belakang penantian `WaitWipe` yang panjangnya tak terbatas. `Approve` memanggil `confirmDrop` dengan jawaban "koneksi bersih" yang dikodekan `true`. Aplikasi yang tersambung lagi selama penantian kehilangan tulisannya tanpa pengakuan apa pun.
- **Perbaikan:** daa12b9.
  - `Approve` memanggil `retryConnectionsClear` sebelum `startMu` (`orchestrator/runner.go:784`). Aturan `ErrConnectionsRemain` di `confirmDrop` kini berlaku untuk persetujuan. Daftar yang tidak terbaca tidak dianggap bersih, dan pengakuan saat job mulai tidak dihitung.
  - Web meneruskan `accept_connections` dan merender partial pengakuan di dialog Setujui.
- **Test pin:** `TestApproveRebuildRechecksConnections` (orchestrator dan web). Helper e2e `approveJobAck` mencentang pengakuan bila diminta, sehingga `TestE2EPromotePlanned` menguji SP-1 dari ujung ke ujung.
- **Putusan pemeriksaan ulang:** ADDRESSED.
- **Status: Diperbaiki.**
- Terbuka (kecil), Rencana 5e: pengakuan koneksi juga diminta pada persetujuan sesudah salinan CLONE (`WaitReconnect` dengan `AdminReplaced`). Di titik itu teks "akan kehilangan semua yang ditulisnya ke sini" tidak lagi tepat. Gagal tertutup; biayanya hanya UX.

### SEC-92 — `master_start_late` tertutup oleh perubahan peran; form mempercayai pemeriksaan lama (SP-2)
- **Tingkat:** Sedang.
- **Masalah di `343898c`:** ada dua jalur.
  - (A) Bangun ulang mendaftarkan master lama sebagai replika, dan tik berikutnya menutup peringatan Kritis "sempat bisa ditulis". Bila bangun ulang dibatalkan, peringatan tidak terbuka lagi.
  - (B) Form bangun ulang dan dialog hapus mempercayai hasil "Periksa data tertinggal" yang lebih tua dari start terlambat, sehingga tulisan sesudahnya hilang tanpa peringatan.
- **Perbaikan:** a70a82a, 427ca73.
  - `closeStartAlerts` tidak menutup `master_start_late` selama server adalah replika dalam keadaan yang belum dinilai (`monitor/promote.go:687`).
  - `startLateOpen` membuat form bangun ulang (`web/handlers_oldmaster.go:762`) dan dialog hapus (`:468`) menampilkan peringatan start terlambat.
- **Test pin:** `TestStartLateSurvivesRebuildUntilCopied`, `TestLateStartDistrustsOlderLeftoverCheck`.
- **Putusan pemeriksaan ulang:** ADDRESSED.
- **Status: Diperbaiki.**
- Sisa:
  - Aturan buka-tetap berlaku untuk replika apa pun dalam keadaan belum dinilai, bukan hanya master lama yang dibangun ulang. Agent yang salah atau dibobol pada replika biasa bisa membiarkan peringatan Kritis terbuka dengan saran yang tidak ada di halaman replika. → digabung ke R-126 (SEC-107).
  - Terbuka (kecil), Rencana 5e, tanpa baris inventaris: bila pemeriksaan lama menemukan N transaksi lalu terjadi start terlambat, form dan dialog hanya menampilkan peringatan start terlambat. Kalimat "N transaksi" hilang. Ini kehilangan informasi, bukan celah keselamatan, karena peringatannya tetap menahan kepercayaan pada hasil lama.

### SEC-93 — Kejadian agent selain laporan kesehatan tidak dibatasi (SP-3)
- **Tingkat:** Sedang.
- **Penyerang:** agent yang dibobol pada server mana pun yang berpasangan, atau agent yang salah dan berulang.
- **Masalah di `343898c`:** `monitor.quarantineStart` `bypassed`/`ok` yang dikirim bergantian membuka dan menutup peringatan dengan laju kiriman agent. Akibatnya baris `alerts` terus bertambah sampai disk host panel penuh, riwayat 500 baris terdesak, dan baris Warn tanpa batas tertulis di bawah satu mutex monitor.
- **Perbaikan:** 9f87ccc. `Hub.OnEvent` membatasi empat kejadian kunci/start per server lewat `allowEvent` (`hub/hub.go:297`): burst 10, satu token per 5 detik. Tanpa token, kejadian tetap lolos bila belum ada kejadian berjenis sama yang lolos dalam 10 menit terakhir. Jenis dibentuk hanya dari kata yang dikenal.
- **Test pin:** `TestHubLimitsAgentEvents`. Test tingkat store yang juga diusulkan pemeriksa tidak ditambahkan; pemeriksa ulang menilai test hub cukup.
- **Putusan pemeriksaan ulang:** ADDRESSED. Kasus terburuk menjadi sekitar 100 + 120 kejadian per 10 menit per server.
- **Status: Diperbaiki.**
- Terbuka (kecil), Rencana 5e: kejadian yang dibuang dihitung di penghitung `Dropped` yang sama dengan laporan kesehatan ("laporan dibuang" di halaman server).

### SEC-94 — Perubahan kunci start tidak segera dilaporkan; "belum diperiksa" palsu (FR-2)
- **Tingkat:** Penting.
- **Masalah di `343898c`:** `changed()` di agent tidak memuat `StartLock`, jadi perubahan kunci baru sampai ke panel pada ringkasan berikutnya (sampai 3600 detik). Snapshot pertama setelah agent start bisa menyatakan "kunci start belum diperiksa agent", dan itu membuka peringatan Kritis palsu.
- **Perbaikan:** 33ad13f. `changed()` membandingkan `StartLock` (nil, `Present`, `Locked`, `Reason`). `Snapshot` menghitung `startLockStatus` sendiri bila penjaga belum memeriksanya pada server yang dikarantina.
- **Test pin:** `TestChangedOnStartLock`, `TestFirstSnapshotOfQuarantinedServerCarriesCheckedStartLock`.
- **Putusan pemeriksaan ulang:** ADDRESSED.
- **Status: Diperbaiki.** Perbaikan ini tidak mengubah R-107: server yang dikarantina tetapi belum pernah mengirim laporan tetap tidak dinilai aturan `master_start_unguarded` (`monitor/alerts.go:355`).

### SEC-95 — Host kunci yang dipertahankan bisa tertahan sejam (FR-3)
- **Tingkat:** Penting.
- **Masalah di `343898c`:** `keepLock` membatasi laporan hanya berdasarkan waktu. Bila kunci pertama kali dipertahankan karena kandidat tidak terjangkau, lalu karena replika R tidak terbaca, peringatan Kritis `master_lock_kept` (berisi perintah `forget-replica`/`unlock-master`) tidak muncul sampai 60 menit, sementara aplikasi tidak bisa menulis.
- **Perbaikan:** 2d52c10. Laporan dikirim seketika bila himpunan host berbeda (termasuk dari kosong ke berisi). Irama sejam hanya untuk himpunan yang sama. `keptAt`/`keptHosts` dicatat hanya setelah terkirim.
- **Test pin:** `TestKeptHostsReportedAtOnceAfterAnotherKeepReason` (dengan subtest `TestKeptHostsChangeReported`).
- **Putusan pemeriksaan ulang:** ADDRESSED.
- **Status: Diperbaiki.**

### SEC-96 — Berkas kunci start: operasi yang mengikuti symlink; direktori tidak diperiksa (SA-7)
- **Tingkat:** Rendah.
- **Penyerang:** pengguna lokal yang bisa menulis ke direktori berkas kunci. Pada paket standar hanya root, jadi jalur ini tidak terjangkau. Ia terjangkau pada tata letak yang direktori berkas opsinya milik pengguna `mysql` (tarball dan beberapa instalasi panel hosting, kelas yang sama dengan R-056).
- **Masalah di `343898c`:** `Lstat` lalu `Chmod(path)`/`Chown(path)` mengikuti link yang ditaruh di antaranya, dan pemilik direktori tidak pernah diperiksa.
- **Yang diperbaiki:** 8e93bae. `writeStartLockFile` (`startlock.go:401-406`) membuka berkas yang ada dengan `O_NOFOLLOW|O_NONBLOCK|O_CLOEXEC`, lalu `fixStartLockFile` (`:433`) memperbaiki pemilik dan mode lewat deskriptor itu. Symlink membuat `open` gagal (ELOOP), lalu berkas ditulis ulang dengan rename.
- **Test pin:** `TestStartLockFileFixNeverFollowsLink`.
- **Yang belum:** pemeriksaan bahwa direktori dan induknya milik root dan tidak bisa ditulis grup/lainnya. Pelaksana berhenti karena harness `testmysql` menaruh `my.cnf` di direktori milik pengguna mysqld. Pemeriksa ulang menilai alasan itu hanya biaya harness, bukan penghalang desain: pemeriksaan bisa menerima pemilik yang diharapkan sebagai hook.
- **Penilaian pemeriksa ulang:** nyata tetapi rendah. Hanya terjangkau pada tata letak yang direktori berkas opsinya bisa ditulis pengguna non-root. Kunci start memang bukan pertahanan terhadap pengguna OS `mysql`, yang bisa menyalakan mysqld sendiri. Di tata letak itu, pengguna tersebut bisa menghapus atau mengganti berkas kunci di antara putaran penjaga sementara agent melaporkan `StartLocked=true`. `f.Chown(0,0)` pada inode yang di-hardlink dibatasi `protected_hardlinks`.
- **Putusan pemeriksaan ulang:** PARTLY.
- **Status: Diperbaiki sebagian.** Sisanya di R-115 (Rencana 5e). Nilai termurah yang tersisa: laporkan `Locked=false` beserta alasannya bila direktori gagal pemeriksaan pemilik/mode. Sisa yang sama berlaku untuk `writeFileAtomic` (berkas sementara lalu rename) di direktori yang bisa ditulis non-root.

### SEC-97 — Kunci start belum di disk saat perintah menjawab (SA-8)
- **Tingkat:** Rendah.
- **Lokasi:** `realhost.go:186-221` (`writeFileAtomic`: sync berkas lalu rename, tanpa sync direktori). Berkas kunci start dan konfigurasi terkelola ditulis lewat jalur ini. `ClearLock` menghapus kunci promosi tanpa `syncDir` (`promote_files.go:317`).
- **Skenario:**
  1. `mysql.quarantine` pada server yang MySQL-nya mati menjawab `StartLocked=true`. Pin sudah tahan crash, tetapi berkas dan baris kunci mungkin belum, selama beberapa detik.
  2. Listrik padam dalam jendela itu (kejadian yang biasa menyertai failover).
  3. Host kembali dalam keadaan dikarantina tetapi tanpa kunci start. MySQL menyala sesuai nilai tersimpannya sampai putaran penjaga pertama.
  Karena kedua rename ada di satu direktori, journaling menjaga urutannya: baris tanpa berkas tidak mungkin terjadi. Untuk `ClearLock`, arahnya aman.
- **Status: Terbuka.** Rencana 5e, baris R-112.

### SEC-98 — `StartLocked` dihitung di lingkungan agent (SA-9)
- **Tingkat:** Rendah.
- **Lokasi:**
  - `startlock.go:463-470` (argumen `--print-defaults`);
  - `startlock.go:480-488` (`readEffectiveOptions` dijalankan lewat `Host.Exec`, yang mewarisi lingkungan agent);
  - unit agent `ProtectHome=yes`.
  Daftar bawaan mysqld berakhir dengan `~/.my.cnf`, yang diselesaikan dari `HOME`.
- **Skenario:** pada paket keluarga RHEL, home akun `mysql` adalah direktori data. `.my.cnf` di sana dengan grup `[mysqld]` dibaca terakhir oleh layanan dan bisa memasang `init_file` lain atau mematikannya. `--print-defaults` agent membaca home root (kosong). Hasilnya `StartLocked=true` padahal start berikutnya tidak memakai kunci. Yang bisa menulis berkas itu: pengguna OS `mysql`, atau akun MySQL ber-FILE bila `secure_file_priv` kosong.
- **Dampak:** kelas R-097 yang dicapai lewat berkas yang bukan milik root. Komentar kode (`startlock.go:473-479`) menyebut pemeriksaan ini konservatif; untuk kasus ini klaim itu tidak benar.
- **Status: Terbuka.** Rencana 5b (prasyarat yang dipaksakan), baris R-113; R-097 ditulis ulang.

### SEC-99 — Jawaban peer tidak dibatasi (SA-10)
- **Tingkat:** Rendah.
- **Masalah di `343898c`:** pembacaan peer hanya dibatasi waktu (5 detik), bukan ukuran. Teks error peer masuk utuh ke log dan ke kejadian `kept`. Pesan multi-megabyte membuat kejadian itu lebih dari 1 MiB, sehingga dibuang dan tidak dicoba lagi selama sejam.
- **Perbaikan:** 7c76beb.
  - `ReadPeer` memakai `DialFunc` yang membungkus koneksi dengan `limitedConn` (256 KiB, TLS termasuk; `peer.go:67-72`).
  - `PeerState.Err` dipotong 300 byte (`cutText`).
  - `Reason` kejadian `kept` tidak lagi mengulang daftar host.
- **Test pin:** `TestReadPeerBoundsAnswer`.
- **Putusan pemeriksaan ulang:** ADDRESSED, dengan catatan: test itu lulus juga tanpa `limitedConn`, karena `cutText` saja sudah membatasi `Err`. Batas baca 256 KiB **tidak dipin test**; ini terbuka (kecil), Rencana 5e.
- **Status: Diperbaiki.**

### SEC-100 — Jalur buka lokal tidak menyebut apa yang dilewatinya (SA-11)
- **Tingkat:** Rendah.
- **Masalah di `343898c`:** `unlock-master` membuka tulis tanpa menyebut kandidat atau replika yang terdaftar di kunci, `forget-replica` hanya mencetak hitungan, dan pesan "kunci tidak bisa dibaca" menyarankan `unlock-master`.
- **Perbaikan:** c2133b3. `unlock-master` pada kunci berkandidat mencetak kandidat, tenggat, replika terdaftar, dan peringatan dua master. `forget-replica` mencetak bahwa penjaga tidak lagi menunggu host itu (`internal/agent/cli.go:491-492`). Pesan berkas yang tidak valid tidak lagi menyodorkan `unlock-master` sebagai perbaikan.
- **Test pin:** `TestUnlockMasterSaysWhatItBypasses`, `TestForgetReplicaCLI`.
- **Putusan pemeriksaan ulang:** ADDRESSED. Gerbang `--yes` yang diusulkan pemeriksa tidak ditambahkan; perlu tidaknya konfirmasi diserahkan ke pemilik.
- **Status: Diperbaiki.**

### SEC-101 — Job sebelum promote tetap bertindak atas master lama (SP-4)
- **Tingkat:** Rendah.
- **Masalah di `343898c`:** master job ditetapkan saat job dibuat. Setelah promote, Ulangi resync yang gagal memeriksa `wipe_only` terhadap master lama tetapi menyalin dari master baru.
- **Perbaikan:** 7df4b48. `supersededBy` menolak bila master topologi bukan `MasterServerID` job (`orchestrator/runner.go:968`). Job tanpa field itu dianggap berganti (gagal tertutup).
- **Test pin:** `TestRetryRefusedAfterMasterSwapped`.
- **Putusan pemeriksaan ulang:** ADDRESSED. Semua jenis job menyimpan `MasterServerID` saat dibuat, jadi job yang masih hidup tidak salah ditolak.
- **Status: Diperbaiki.**

### SEC-102 — Daftar `WaitWipe` hanya lewat pesan berbatas 4 KiB (SP-5)
- **Tingkat:** Rendah.
- **Masalah di `343898c`:** satu token menyetujui pembuangan seluruh daftar, sementara yang tampil hanya 4 KiB pertama.
- **Perbaikan:** 67fd2bd. Field job-state baru `WaitList` (aditif) menyimpan daftar penuh, dan dialog Setujui menampilkan semua nama. Penantian lama tanpa field itu yang pesannya terpotong ditolak (`refuseCutWipeList`, `orchestrator/runner.go:797`).
- **Test pin:** `TestWipeWaitKeepsFullList`, `TestWipeApprovalListsEveryName`.
- **Putusan pemeriksaan ulang:** ADDRESSED.
- **Status: Diperbaiki.**

### SEC-103 — Persetujuan yang mengantre dipakai penantian berikutnya (SP-6)
- **Tingkat:** Rendah menurut pemeriksa: butuh dua token yang dikonfirmasi sandi dalam jendela sekitar satu milidetik, dan daftar yang berubah di antaranya. Di inventaris prioritasnya Tinggi, karena inventaris hanya menilai dampak dan dampaknya data hilang.
- **Lokasi:** `orchestrator/runner.go:461-477` (`launch` mengantrekan aksi di belakang goroutine yang masih hidup tanpa aksi tertunda); `:521-524` (`execute` menerima persetujuan yang mengantre bila job menunggu di indeks langkah yang sama). Langkahnya membandingkan daftar segar dengan hash penantian yang **sedang berlaku**.
- **Skenario:**
  1. Setujui #1 memulai goroutine.
  2. Setujui #2 tiba sebelum status `running` ditulis, lolos `confirmDrop`, lalu mengantre.
  3. Run #1 menemukan daftar berubah dan menunggu lagi di langkah yang sama dengan daftar B.
  4. Persetujuan yang mengantre lalu menyetujui B: databasenya dibuang tanpa pernah ditampilkan.
- **Status: Terbuka.** Rencana 5e, baris R-124. Usul pemeriksa: bawa hash penantian di aksi, dan buang persetujuan yang hash-nya tidak cocok.

### SEC-104 — Jalur tanpa token ke salinan lebih luas dari R-105 (SP-7 = FR-15)
- **Tingkat:** Rendah / Minor.
- **Masalah di `343898c`:** ada tiga jalur:
  - (1) Setujui `WaitForeignSource` pada Buat replika biasa;
  - (2) Ulangi Buat replika berbasis dump di "Siapkan replika"/"Salin data" tanpa preflight baru dan tanpa batas 24 jam;
  - (3) Ulangi tambah database dari langkah dump bila `AddDrop` kosong (= SEC-111).
  `dropsData` untuk create_replica berupa enumerasi yang akan lapuk.
- **Perbaikan:** b9ec203. `copyLeft := SnapshotGTID == "" && !AdminReplaced` (`orchestrator/runner.go:611-612`), sehingga setiap create_replica yang salinannya belum dibuat dihitung membuang. Akibatnya Ulangi dan Setujui butuh token, terkena batas 24 jam, dan preflight berjalan lagi. Jalur 3 ditutup SEC-111.
- **Test pin:** `TestCreateReplicaDropsUntilCopied` (tabel Method × SnapshotGTID × AdminReplaced × WipeList × WaitReason × RebuildOldMaster dengan oracle terpisah), `TestRetryDumpCreateFailedAtCopyNeedsToken`.
- **Putusan pemeriksaan ulang:** ADDRESSED.
- **Status: Diperbaiki.** R-105 pindah ke "Sudah ditangani". Biayanya satu sandi tambahan pada Ulangi kegagalan awal. Job format lama tanpa `SnapshotGTID` juga dihitung membuang (gagal aman).

### SEC-105 — Log audit tidak mencatat apa yang diakui (SP-8)
- **Tingkat:** Rendah.
- **Lokasi:**
  - `web/handlers_mysql.go:664`: detail mulai bangun ulang memuat `rebuild_old_master` tetapi tidak memuat `accept_connections` atau jumlah koneksi yang diakui.
  - `web/handlers_jobs.go:585`: detail Ulangi/Setujui yang berhasil hanya `job_id` dan `kind`. `wipe_only`, `accept_connections`, dan daftar yang disetujui untuk dibuang hanya ada di log job, yang tidak berantai hash dan ikut terhapus bersama servernya.
  - R-067 sedikit lebih buruk: detail resync kini membawa `wipe_list` dan `wipe_only`, sehingga batas 2 KiB tercapai dengan sekitar separuh jumlah database.
- **Status: Terbuka.** Rencana 5e, baris R-125. Butir R-067 diberi catatan, tetapi tidak digabung ke baris SP-8, supaya jumlah per prioritas tetap terbaca (alasannya di inventaris).

### SEC-106 — Kejadian kunci/start yang tidak terbaca tidak membuka peringatan (SP-9)
- **Tingkat:** Rendah.
- **Masalah di `343898c`:** payload di atas 4 KiB atau yang tidak terurai diabaikan. Untuk "tutup" itu benar, tetapi untuk "buka" itu gagal terbuka: sekitar 80 host membuat kejadian `kept` lebih dari 4 KiB.
- **Perbaikan:** 7c76beb.
  - Panel: `monitor.lockExpired` yang tidak terbaca membuka pemberitahuan `master_lock_expired` generik (`keptAnyway`, `monitor/promote.go:346`). `monitor.quarantineStart` yang tidak terbaca membuka `master_start_late`, dan `monitor.quarantineStartFailed` membuka `master_start_blocked` (`raiseIfNone`, `:593`, `:596`, `:654`), masing-masing hanya bila belum ada yang terbuka.
  - Agent: `Reason` tidak lagi mengulang host.
- **Test pin:** `TestLockKeptOversizeStillAlerts`, `TestUnreadableStartEventsStillAlert`. Pernyataan `TestMasterLockExpiredFromEvent` diubah sesuai perbaikan.
- **Putusan pemeriksaan ulang:** ADDRESSED.
- **Status: Diperbaiki.**
- Terbuka (kecil), Rencana 5e: pemberitahuan generik menggantikan `master_lock_expired` yang lebih spesifik dan masih terbuka (misalnya yang menyebut kandidat) bila kejadian tak terbaca datang. Ini kehilangan informasi saja.

### SEC-107 — Peringatan dari kejadian tidak terikat peran pengirim (SP-10)
- **Tingkat:** Rendah.
- **Lokasi:** `monitor/promote.go:375-379` (`raise` untuk `master_lock_kept` tanpa memeriksa peran pengirim); `lockKeptOver` (`:494`) tidak pernah menutupnya untuk replika.
- **Skenario:** agent replika yang dibobol mengirim `monitor.lockExpired` `kept` beserta host. Peringatan Kritis yang tidak pernah dibungkam lalu tetap terbuka dengan perintah `sudo dbpanel agent unlock-master` untuk master. Penyerang bisa memakainya untuk mendorong operator membuka kunci promosi master dengan tangan saat promote terencana. Sejak c2133b3 (SEC-115), teks peringatan itu memuat kehati-hatian sebelum `unlock-master`.
- **Status: Terbuka.** Rencana 5e, baris R-126. Butir pemeriksaan ulang panel Minor 1 (aturan buka-tetap SEC-92 berlaku untuk replika mana pun) digabung ke baris ini karena akarnya sama: peringatan start/kunci tidak terikat pada peran yang bisa mengalami kondisi itu.

### SEC-108 — Teks agent di luar batas `agentText` (SP-11)
- **Tingkat:** Rendah.
- **Diperbaiki (69b05d8):**
  - (1) `le.Candidate` dan `qe.NewSource` lewat `agentText` (120);
  - (2) `ConnectionsResult.Unreadable`/`.Incomplete` dibatasi 300 dan dibersihkan dari karakter kendali/format, juga di teks konfirmasi bangun ulang;
  - (3) `StartLock.Reason` (300) dan `OperatorInitFiles` (300 masing-masing, paling banyak 10) di halaman server.
- **Test pin:** `TestStartAndKeptMessagesBoundAgentText`, `TestConnectionsBoundAgentText`. Butir 3 **tidak punya test pin** (celah test, Rencana 5e).
- **Yang belum:** (4) nama database dari inventaris agent tidak divalidasi atau ditandai di `inventoryCall` (`orchestrator/runner.go:252-275`). Nama dengan override bidi atau karakter aneh lain bisa membuat dialog atau log terbaca salah, termasuk daftar pembuangan yang kini ditampilkan utuh (SEC-102).
- **Putusan pemeriksaan ulang:** PARTLY. Paparan yang tersisa hanya tampilan: agent menolak nama di luar `dbNameRe` sebelum membuang, men-dump, atau menyaring (§4A), dan persetujuan terikat pada hash daftar.
- **Status: Diperbaiki sebagian.** Sisa di R-127 (Rencana 5e).

### SEC-109 — `?buka=` membuka dialog destruktif pada GET biasa (SP-12)
- **Tingkat:** Rendah.
- **Lokasi:** `web/handlers_jobs.go:335` (`?buka=ulangi`; untuk job bangun ulang, GET itu juga memanggil agent master lama), `web/handlers_mysql.go:830` (`?buka=resync`), `web/static/app.js:534` (`openDialog`).
- **Masalah:** cookie `SameSite=Lax`, jadi tautan dari situs lain atau `/login?next=/replicas/2?buka=resync` membuka halaman dengan dialog destruktif sudah terbuka, dengan pengelompokan dari inventaris tersimpan yang mungkin basi. POST-nya tetap butuh token CSRF, token sandi, dan nama yang diketik. Bacaan segar orkestrator tetap yang memutuskan, dan pengelompokan basi gagal tertutup.
- **Status: Terbuka.** Rencana 5e, baris R-128. Usul pemeriksa: hormati `buka` hanya bila `Sec-Fetch-Site: same-origin`.

### SEC-110 — Start yang gagal karena berkas kunci hilang tidak dilaporkan (FR-4)
- **Tingkat:** Minor.
- **Masalah di `343898c`:** `keepStartLock` menulis ulang berkas (mtime baru) sebelum `watchBootFailure`, yang hanya mengambil baris log yang lebih baru dari mtime itu. Akibatnya MySQL tetap mati dan tidak ada yang diberi tahu.
- **Perbaikan:** 67e172f. `bootSince` (`startlock_guard.go:251-252`) mengingat mtime konfigurasi terkelola sebelum penulisan, lalu log dipindai dari `min(mtime berkas, bootSince)` (`:325-326`).
- **Test pin:** `TestBootstrapFailureReportedWhenGuardRecreatesFile`.
- **Putusan pemeriksaan ulang:** ADDRESSED.
- **Status: Diperbaiki.**
- **Sisa:** jendela pindai yang lebih lebar bisa melaporkan lagi kegagalan lama setelah agent restart (`bootFailAt` hanya di memori). Hasilnya `master_start_blocked` yang basi, bukan keadaan keselamatan yang salah. → R-123 (Rencana 5e). Baris ini tidak digabung dengan R-122 karena penyebabnya berbeda.

### SEC-111 — Tambah database tidak membaca ulang replika di langkah dump (FR-5)
- **Tingkat:** Minor.
- **Masalah di `343898c`:** job yang langkah 0-nya tidak menemukan apa pun bukan "job pembuang". Ulangi dari langkah 1 atau 2 lalu tanpa token dan memuat dump di atas database yang dibuat sesudahnya.
- **Perbaikan:** df9ffc1. `addDump` memanggil `dropAddExisting` bila `(len(AddDrop) > 0 || !addFresh) && !reattach` (`orchestrator/change_databases.go:314`). Bila ada database yang tidak disetujui, job menunggu dengan ketik-nama dan token.
- **Test pin:** `TestAddRetryAtDumpWaitsForDatabaseCreatedMeanwhile`.
- **Putusan pemeriksaan ulang:** ADDRESSED.
- **Status: Diperbaiki.**

### SEC-112 — Pemotongan inventaris diam-diam (FR-6)
- **Tingkat:** Minor.
- **Masalah di `343898c`:** inventaris dipotong di 5.000 database tanpa sinyal. "Tambah database" bisa tidak melihat database yang sudah ada (R-104).
- **Perbaikan:** df9ffc1. `inventoryCallCut`/`refreshInventory` mengembalikan tanda potong (`orchestrator/runner.go:235-275`). `addExisting` (`change_databases.go:494`) dan `enforceWipeOnly` (`create_replica.go:1089`) menolak dengan `errInventoryCut`.
- **Test pin:** `TestAddExistingRefusesTruncatedInventory`, `TestEnforceWipeOnlyRefusesTruncatedInventory`.
- **Putusan pemeriksaan ulang:** ADDRESSED.
- **Status: Diperbaiki.** R-104 pindah ke "Sudah ditangani".
- **Sisa:** `leftoverDatabases` (preflight create/rebuild, `create_replica.go:560-563`) masih memakai `RefreshInventory` tanpa tanda potong. Daftar yang terpotong hanya memperkecil daftar buang (arah aman), tetapi persetujuan lalu menampilkan lebih sedikit nama daripada yang ada di target. → R-130 (Rencana 5d).

### SEC-113 — Pembukuan ringkasan "Jangan ganggu" (FR-8)
- **Tingkat:** Minor.
- **Masalah di `343898c`:** (a) senyap baru sebelum ringkasan senyap sebelumnya terkirim mengatur ulang `snooze_from`, sehingga ringkasan pertama hilang. (b) `ClearSnoozeFrom` tanpa syarat menghapus awal senyap yang dimulai di antara pengiriman dan penghapusan.
- **Perbaikan:** 66ff084. (a) `snooze_from` dipertahankan bila sudah terisi. (b) Penghapusan hanya untuk rentang yang dikirim.
- **Test pin:** `TestResnoozeBeforeDigestKeepsFrom`, `TestClearSnoozeFromOnlyForTheSpanSent`.
- **Putusan pemeriksaan ulang:** ADDRESSED.
- **Status: Diperbaiki.**
- Terbuka (kecil), Rencana 5e:
  - ringkasan yang mencakup dua senyap juga memuat peringatan dari celah di antaranya (lapor lebih, arah aman);
  - tanpa Telegram terkonfigurasi, `snooze_from` tidak pernah dihapus, jadi ringkasan pertama setelah Telegram dipasang mencakup semua sejak senyap pertama.

### SEC-114 — `forget-replica` pada saat dibutuhkan (FR-9)
- **Tingkat:** Minor.
- **(a) Diperbaiki (8d9be83):** perintah kini menerima `host:port` dari peringatan dan membuang port-nya (`internal/agent/cli.go` `mainForgetReplica`). Test pin: `TestForgetReplicaCLI`. Putusan pemeriksaan ulang: ADDRESSED.
- **(b) Terbuka:** melupakan replika yang masih tersambung. Dari kode di `e138d86`:
  - `mainForgetReplica` (`internal/agent/cli.go:461-495`) tidak memeriksa apakah host itu sedang punya thread `Binlog Dump`.
  - Agent hanya menulis ulang catatan bila himpunan replika yang tersambung berubah, atau setelah `seenRefresh` = 24 jam (`promote_files.go:70`, `:653`). Review menyebut "sejam"; kode menyebut 24 jam.
  - Akibatnya replika yang dilupakan tetapi masih tersambung tidak ada di catatan sampai salah satu itu terjadi (atau agent restart). Selama itu master lama menolak karantina di bawah host tersebut ("tidak pernah tercatat sebagai replika").
  - Bila master mati dan replika itu dipromote darurat, karantina ditolak dan promote menjadi `partial`, seperti R-094.
- **Status: Diperbaiki sebagian.** (b) di R-120 (Rencana 5e). Usul: CLI menolak atau memperingatkan bila host itu sedang punya thread `Binlog Dump`.

### SEC-115 — `master_lock_kept` memberi `unlock-master` tanpa kehati-hatian (FR-10)
- **Tingkat:** Minor.
- **Perbaikan:** c2133b3. Teks peringatan (`monitor/alerts.go:476-477`) kini memuat "pastikan dulu replika itu tidak sedang menjadi master di sisi jaringan lain: unlock-master membuka master tanpa memeriksanya".
- **Test pin:** `TestLockKeptWithHostsIsCritical` (teks persis diperbarui).
- **Putusan pemeriksaan ulang:** ADDRESSED.
- **Status: Diperbaiki.**

### SEC-116 — Urutan penjaga master lama (FR-13)
- **Tingkat:** Minor.
- **Masalah di `343898c`:** (a) `localGreeting` setiap 5 detik menghasilkan satu jabat tangan yang dibatalkan per putaran dan sesi "unauthenticated user" yang dihitung `mysql.connections`, sehingga sesekali muncul "1 koneksi" palsu. (b) Urusan kunci start (yang bisa menjalankan `--print-defaults`) berjalan sebelum penegakan baca-saja.
- **Perbaikan:** 67e172f. Pada server yang dikarantina, `GuardTick` menjalankan `guardQuarantined` lalu `guardStartLock` (`promote_lock.go:561-564`). `guardStartLock` memakai `d.qUp` dari sesi terautentikasi `guardQuarantined`.
- **Test pin:** `TestQuarantineGuardMakesNoAbortedHandshake`, `TestGuardEnforcesReadOnlyBeforeStartLockHousekeeping`.
- **Putusan pemeriksaan ulang:** ADDRESSED. Catatan: `flushStartEvents` yang baru diletakkan di depan keduanya (lihat R-117 di SEC-89); setelah audit, `bb54853` memindahkan laporan start dan kejadian karantina dari `guardQuarantined` ke akhir putaran (sisa jeda antarputaran: R-117 di SEC-89).
- **Status: Diperbaiki.**

### SEC-117 — `lock-master` di luar satu bagian kritis (FR-14)
- **Tingkat:** Minor (ledger: "safety-relevant").
- **Masalah di `343898c`:** `lock-master` bisa mengganti kunci yang baru ditulis `lockForPromote` dengan kunci `cli`. Hasilnya gagal tertutup.
- **Perbaikan:** aff8906. `Pins.WriteLockIfNone` (`promote_files.go:225`) membaca dan menulis di bawah flock kunci. Kunci yang muncul di antaranya dipertahankan, dan CLI keluar dengan kode 1 sambil menyebutnya.
- **Test pin:** `TestLockMasterKeepsLockWrittenMeanwhile`.
- **Putusan pemeriksaan ulang:** ADDRESSED.
- **Status: Diperbaiki.**

### SEC-118 — Nama yang sama (beda huruf) dengan database yang diikuti ikut dibuang (ledger Task 13 m3)
- **Tingkat:** Minor (review: "fix if cheap"). Hanya terjangkau bila `lower_case_table_names` berbeda antara master dan replika.
- **Perbaikan:** df9ffc1. `addExisting` menggagalkan job bila nama yang akan dibuang sama (`EqualFold`) dengan database yang sudah diikuti (`orchestrator/change_databases.go:500`).
- **Test pin:** `TestAddExistingRefusesFollowedNameIgnoringCase`.
- **Putusan pemeriksaan ulang:** ADDRESSED.
- **Status: Diperbaiki.**

### SEC-119 — `mysql.unlockPromote` membuka master tanpa membaca replika di daftar kunci (review: "declined to judge" dan rekomendasi 4)
- **Tingkat:** tidak dinilai. Review tidak menilainya karena spesifikasi §5.4 hanya menyebut penjaga. Ia merekomendasikan perbaikan karena pembatalan job basi setelah promote darurat bisa sampai ke perintah ini.
- **Perbaikan:** 81ccdad. Sebelum membuka, `unlockPromote` membaca `PromoteLock.Readable` dengan akun kandidat (`readListed`). Entri yang tidak terbaca membuat perintah ditolak dan kunci dipertahankan. Entri yang bisa ditulis dan memuat transaksi server ini membuat server mengarantina diri (`selfQuarantine`).
- **Test pin:** `TestUnlockPromoteReadsListedReplicas`.
- **Putusan pemeriksaan ulang:** ADDRESSED.
- **Status: Diperbaiki.**
- **Sambungan ke panel, dicek di `e138d86`:**
  - `promoteUndo` (`orchestrator/promote.go:988-1027`) tidak menganggap master sudah dibuka bila agent menolak.
  - Terbuka (kecil), Rencana 5e: baris log panel sesudah penolakan ("agent master membukanya sendiri saat tenggat kunci", `promote.go:1023`) tidak tepat bila agent menolak karena mempertahankan kunci atau sudah mengarantina diri. Alasan agent ada di baris yang sama.

### SEC-120 — `set-role master` butuh kunci rahasia agent dan tata letak (review: "declined to judge")
- **Tingkat:** tidak dinilai. Review mencatat spesifikasi hanya menyebut "bila pelepasan gagal, peran tidak diubah".
- **Masalah:** jalan keluar darurat lokal `dbpanel agent set-role master` perlu memuat kunci rahasia agent dan tata letak MySQL hanya untuk menghapus satu berkas dan satu baris. Bila `mysql.cred` atau `secrets.key` rusak, jalan keluar itu terhalang saat paling dibutuhkan.
- **Status: Terbuka.** Rencana 5b (runbook), baris R-121.

### SEC-121 — Path kunci start ditulis ke berkas opsi tanpa pemeriksaan (SA-12)
- **Tingkat:** Info.
- **Perbaikan:** c2133b3. `StartLockPath` (`startlock.go:148`) menolak direktori yang memuat `#`, `'`, `"`, `\`, atau karakter kendali.
- **Test pin:** `TestStartLockPathRule` (lima baris).
- **Putusan pemeriksaan ulang:** ADDRESSED.
- **Status: Diperbaiki.**

### SEC-122 — Jenis yang tidak pernah dibungkam berupa daftar izin (SP-13)
- **Tingkat:** Info.
- **Lokasi:** `notify/notifier.go:51` (`unsnoozableKinds`).
- **Masalah:** `TestUnsnoozableKindsPinned` mempin himpunan hari ini. Jenis kritis yang ditambahkan rencana berikutnya akan terbungkam secara bawaan sampai ada yang menambahkannya ke daftar.
- **Status: Terbuka.** Rencana 5e, baris R-129. Usul: test yang memastikan setiap jenis ada di tepat satu dari dua himpunan eksplisit.

### SEC-123 — Tidak ada test yang mempin penjaga rute baru (SP-14)
- **Tingkat:** Info.
- **Masalah:** `POST /jobs/{id}/retry/prepare` dibungkus `authedPost` (`web/router.go:239`) dan aman menurut konstruksinya. Namun tabel `TestCSRFOnNewRoutes` tidak memuat rute job, dan penolakan lintas situs hanya dites untuk `cancel`.
- **Status: Terbuka.** Rencana 5e, tanpa baris inventaris (bukan risiko tersendiri). Usul: satu test yang menelusuri setiap pola POST terdaftar.

### SEC-124 — Pengerasan header opsional (SP-15)
- **Tingkat:** Info. Tidak ada temuan yang bergantung padanya; lihat §4B.
- **Status: Terbuka.** Rencana 5e, tanpa baris inventaris.

### 4.1 Butir review yang hanya soal mutu kode

Butir review seluruh cabang yang tidak menyangkut data hilang, dua master, peringatan tersembunyi, atau konfirmasi yang terlewati:

| Asal | Butir | Status |
|---|---|---|
| FR-7 | Penolakan kandidat yang masih membawa baris kunci start tidak menyebut jalan keluarnya | Diperbaiki (5832e4f): teks kini menyebut performance_schema dan "pilih kandidat lain"; `TestPromoteViewRefusesCandidateWithStartLock` |
| FR-11 | Peringatan driver di loop penjaga tidak dibatasi lajunya | Diperbaiki (67e172f): `warnLimited` untuk tiga peringatan. `TestGuardFaultWarningsLimited` hanya mempin peringatan `ensureLocked`; dua lainnya tidak dipin (celah test, Rencana 5e) |
| FR-12 | `mysql.recordReplicas` (batas 10 detik) berjalan di dalam `repointMu` | Diperbaiki (c51591a); `TestRecordReplicasOutsideRepointMu` |
| FR-16 | Ketepatan README (`--print-defaults` semenit sekali; waktu buka setelah `forget-replica`) | Diperbaiki (2fc63a4) |
| FR-17 | Tidak ada test yang menangkap butir Penting | Diperbaiki dengan test pin gelombang ini: `TestChangedOnStartLock`, `TestQuarantineStartReportRetriedUntilDelivered`, `TestKeptHostsReportedAtOnceAfterAnotherKeepReason` |
| Ledger Task 11 | Saran penolakan `wipe_only` tidak sesuai jenis job | Diperbaiki (c2133b3); `TestWipeOnlyRefusalAdviceFitsJobKind` |
| Komentar `pins.go` | Komentar kepala mengklaim panel yang dibobol tidak pernah bisa membuang data | Diperbaiki (c2133b3); kini menyebut SEC-85/SEC-86 |

---

### 4A. Yang diperiksa dan dinilai aman

Semua butir di bawah dinilai dengan membaca kode. Butir yang dipin test menyebut test-nya.

- **Injeksi (A03), agent:**
  - Tidak ada SQL baru yang dibangun dari nilai dari luar agent. Kueri koneksi berupa konstanta dengan parameter terikat.
  - Satu-satunya baris berkas opsi yang ditulis kode baru adalah `init_file=<path>` dari tata letak yang ditemukan (kini juga tanpa karakter khusus, SEC-121).
  - `mysql.recordReplicas` menolak setiap parameter (`TestRecordReplicasCommandTakesNoData`).
  - Host dari processlist disimpan hanya bila tanpa spasi atau karakter kendali, dan port berupa bilangan 1–65535. Entri `Readable` divalidasi lagi saat dibaca.
  - Argumen `mysqld --print-defaults` tetap.
- **Nama database (sambungan panel ↔ agent):** agent menolak nama di luar `dbNameRe` sebelum membuang, men-dump, atau menyaring: `internal/agent/drivers/mysql/databases.go:51`, `preflight.go:82`, `replication.go:278`, `snapshot.go:143` (pola di `preflight.go:40`). Sisa SEC-108 karena itu soal tampilan: nama aneh tidak bisa membuat panel membuang database lain, karena persetujuan terikat pada hash daftar.
- **Operasi berkas root (A01/A05):**
  - Penulis konfigurasi terkelola hanya `configure`, `writeStartLock`, dan `releaseStartLock`, semuanya di bawah `lockManagedConfig`.
  - Urutan berkas lalu baris saat menulis, baris lalu berkas saat melepas (`TestStartLockFileBeforeLine`, `TestStartLockReleaseInterruptedNeverLeavesLineWithoutFile`).
  - Berkas flock dibuka `O_NOFOLLOW`, 0600, close-on-exec (`startlock.go:100`).
  - Pin, kunci promosi, dan catatan replika dibaca lewat `readTrustedFile` dan ditulis lewat `writePin` (temp, fsync, rename, sync direktori). `/var/lib/dbpanel` milik root 0711.
- **Gagal terbuka (A04):**
  - Pelepasan kunci start butuh MySQL menjawab. `set-role replica` saat MySQL mati mempertahankan kunci (`TestSetRoleReplicaWhileDownKeepsStartLock`), dan bukti dibuang begitu salah satu bendera mati (`TestReleaseProofLostOnceWritesOpen`).
  - Pin yang tidak tepercaya: penjaga kunci start tidak mengubah apa pun.
  - `lockForPromote` menolak bila pembacaan terpotong konteks, jumlah, atau anggaran (`promote_lock.go:225-246`). Penjaga menghitung "tidak terbaca pada waktunya" sebagai tidak terbaca dan mempertahankan kunci.
  - `ForgetLockHost` membaca ulang di bawah flock (`TestForgetLockHostKeepsLockReplacedMeanwhile`). `mysql.dumpFrom` menolak `drop_existing` (`TestDumpFromRefusesDropExisting`).
- **Kredensial (A02/A07):**
  - Sandi tidak muncul di argv, teks error, atau kejadian.
  - `ReadPeer` (`peer.go:53-91`) membangun konfigurasi TLS dengan CA topologi dan nama server bila verifikasi aktif, sehingga sandi replikasi tidak dikirim ke peer yang gagal pemeriksaan sertifikat. Tanpa verifikasi ia memakai `skip-verify`; itulah SEC-87.
- **Paparan informasi:** `mysql.connections` tidak membaca teks statement. Yang disembunyikan menurut nama hanya `dbpanel_repl` dan `^dbpanel_dump_[0-9a-f]{8}$`, sedangkan `dbpanel_admin` ditampilkan.
- **Sumber daya dan konkurensi:**
  - Putaran penjaga tidak tumpang tindih.
  - Urutan kunci konsisten: `promoteMu` → `managedConfigMu` → flock konfigurasi; flock kunci promosi diambil sendiri; `Pins.mu`, `seenMu`, `startLockMu` adalah daun. Tidak ditemukan inversi.
  - Batas laju kejadian di hub membiarkan kejadian pertama tiap jenis lolos, jadi satu laporan `late` yang mengantre tidak terbuang di belakang semburan laporan `ok` (`hub/hub.go:297`).
- **Kontrol akses dan CSRF (panel):**
  - Setiap rute yang mengubah keadaan adalah `authedPost` = login → `sameOrigin` → `limitForm`, termasuk `POST /jobs/{id}/retry/prepare` (`web/router.go:239`).
  - `sameOrigin` menolak Origin/Referer yang kosong, `null`, atau asing. Body form dibatasi 64 KiB. Id job diperiksa terhadap `^[0-9a-f]{32}$`.
- **Token konfirmasi:**
  - 32 byte acak, disimpan sebagai SHA-256, umur 2 menit.
  - Konsumsi atomik terikat pada pengguna, aksi, jenis target, dan id. Token job berupa `job_retry:<id>`/`job_approve:<id>` dan dikonsumsi terhadap server job dari store (`TestJobTokenBoundToJob`).
  - Pemeriksaan sandi berbagi anggaran dengan login.
- **Setiap jalur ke pembuangan:**
  - `mysql.dropDatabases` punya tiga titik panggil. Masing-masing hanya tercapai bila `dropsData` benar, sehingga `confirmDrop` menuntut `ack.Confirmed` pada setiap Setujui dan Ulangi.
  - Penegakannya di orkestrator, di bawah `startMu` (`TestApproveLeftoverNeedsToken`, `TestRetryResyncFailedAtCopyNeedsToken`, `TestRetryCloneCreateFailedAtCopyNeedsToken`). Batas 24 jam: `TestRetryRefusedAfter24h`.
  - `Resume` setelah restart menggagalkan job yang sedang berjalan atau mengantre.
- **Pengakuan:**
  - `wipe_only[]` diputuskan oleh `enforceWipeOnly` terhadap bacaan segar master, gagal tertutup bila tidak terbaca atau terpotong (`TestPreflightUsesFreshMasterInventory`, `TestPreflightRefusesWhenMasterInventoryUnreadable`, `TestRetryResyncNeedsWipeOnlyAgain`, `TestEnforceWipeOnlyRefusesTruncatedInventory`).
  - `confirm_db[]` dibandingkan dengan daftar tersimpan dan hash daftar segar (`TestApproveAddExistingNeedsTypedNames`, `TestAddExistingWaitsBoundToHash`, `TestAddExistingRetryRechecks`).
- **Kunci promosi yang ditolak (sambungan panel ↔ agent):**
  - `mysql.lockForPromote` yang ditolak (port penuh, terlalu banyak pembacaan, anggaran 60 detik) menggagalkan langkah. `promoteUndo` (`orchestrator/promote.go:988-1027`) lalu meminta `mysql.unlockPromote`, mendapat "tidak dikunci untuk promosi" (`promote_lock.go:445`), dan mencatat bahwa tidak ada yang dikunci.
  - `mysql.unlockPromote` yang ditolak membiarkan keadaan kunci job sebagai "dicoba, belum dibuka". Panel tidak menganggap master sudah dibuka.
- **Kejadian `kept`:** panel tidak membaca nama host dari `Reason` kejadian (hanya dari `Hosts`; tidak ada pembacaan `Reason` di `monitor/promote.go`).
- **Injeksi dan encoding (panel):**
  - Fungsi store baru memakai placeholder.
  - Teks agent di template di-escape `html/template` (`TestCutoverEscapesAgentText`).
  - Pesan Telegram berupa teks polos (tanpa `parse_mode`), dan token di-scrub dari error.
  - Tidak ada `template.HTML`, `template.JS`, atau `x-html` baru.
- **Peringatan dan senyap:**
  - `master_start_unguarded` dinilai apa pun perannya.
  - Kejadian start hanya menutup peringatan dengan hasil yang dikenal panel.
  - Senyap meloloskan pesan pertama dan pesan pulih jenis yang tidak pernah dibungkam (`TestCriticalFailedSendDuringSnoozeIsRetried`).
  - Ringkasan berutang di store dan baru dihapus setelah Telegram menerimanya.
- **Integritas data:** migrasi 0011 dijalankan dalam satu transaksi. Field job-state baru `omitempty` dengan nilai nol yang aman (`WaitList` juga).

### 4B. Security headers

**Cara dicek:** audit panel membaca kode (`web/router.go:272-293` `SecurityHeaders`, wrapper di sekitar mux UI, handler luar, dan handler pengalihan HTTP) serta sumber Go 1.26.10 `net/http/server.go:2321-2339` untuk perilaku `http.Error`. **Tidak ada binary yang dijalankan dan tidak ada `curl`**, berbeda dengan audit Rencana 4a §4B. Nilai di bawah adalah nilai di kode, bukan header yang diamati di respons sungguhan.

| Header | Nilai di kode | Penilaian (dari membaca kode) |
|---|---|---|
| Content-Security-Policy | `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'` | Ketat; tanpa `unsafe-inline`/`unsafe-eval` |
| X-Frame-Options | `DENY` | Ada |
| X-Content-Type-Options | `nosniff` | Ada |
| Referrer-Policy | `same-origin` | Ada (alasan bukan `no-referrer` terdokumentasi) |
| Permissions-Policy | `camera=(), microphone=(), geolocation=()` (`router.go:283`) | Ada, pendek. Tambahan opsional: `payment=(), usb=(), serial=(), bluetooth=(), display-capture=(), browsing-topics=()` (SEC-124) |
| Cross-Origin-Opener-Policy / -Resource-Policy | `same-origin` | Ada |
| Strict-Transport-Security | `max-age=31536000`, hanya bila `r.TLS != nil` | Benar; tidak berlaku untuk host IP (R-008, SEC-57) |
| Cache-Control | `no-store` bawaan; statis `public, max-age=3600`; SSE `no-cache` | Benar untuk daftar koneksi, log job, dan halaman peringatan |

Per kelas respons yang ditambah atau disentuh 5a (dari membaca jalur kode; semua melewati wrapper yang sama):
- halaman penuh (job dengan dialog Ulangi/Setujui, replika, master lama, peringatan, pengaturan);
- render 400/403/409 baru;
- `http.Error` (teks polos + `nosniff`; header lain dipertahankan menurut sumber Go);
- fragmen koneksi;
- JSON (`/confirm`, `/jobs/{id}/log`, `/alerts/count`);
- pengalihan 303.
Semuanya membawa set penuh dengan `no-store`. 404 statis kehilangan `Cache-Control` di jalur error file server Go; tidak sensitif.

**Cookie:** `__Host-dbpanel_session`: `Secure`, `HttpOnly`, `SameSite=Lax`, `Path=/`, tanpa `Domain` (dibaca di `auth/middleware.go`). `Lax`, bukan `Strict`, adalah alasan SEC-109 bisa dicapai dari situs lain. Sisi POST tertutup oleh `sameOrigin`.

**Kode inline:** template yang diubah tidak memuat badan `<script>`, atribut `style=`, handler `on*=`, atau URL `javascript:` (dicari). Komponen Alpine baru didaftarkan di `app.js`. `innerHTML` hanya untuk dua fragmen yang dirender server dan di-escape `html/template`.

**Putusan:** tidak ada celah yang melemahkan kontrol. Tambahan opsional (Permissions-Policy yang lebih panjang; `Sec-Fetch-Site` sebagai sinyal CSRF kedua dan gerbang SEC-109) dicatat di SEC-124 dan SEC-109. Putusan ini berdasarkan pembacaan kode saja.

## 5. Status temuan terjadwal (yang seharusnya ditutup Rencana 5a)

| ID inventaris | Status di `e138d86` | Bukti / sisa |
|---|---|---|
| **R-029, R-034** (daftar koneksi tidak lengkap; akun admin lokal tersembunyi) | **Dikonfirmasi tertutup, dengan sisa** | Audit agent memeriksa kueri koneksi (konstanta, parameter terikat; hanya `dbpanel_repl` dan akun dump buatan agent disembunyikan menurut nama). Celah Setujui ditutup SEC-91 (daa12b9). **Sisa:** R-095, R-100, R-101; pengakuan juga diminta setelah CLONE (SEC-91, kecil). |
| **R-030** (penjaga mengikuti interval) | **Dikonfirmasi tertutup** | Loop tetap 5 detik, putaran tidak tumpang tindih. Satu FIFO bisa mematikan loop itu (SEC-90); diperbaiki b7dd3db. **Sisa:** R-117. Urutan kirim laporan sebelum baca-saja diperbaiki di dalam satu putaran setelah audit (`bb54853`, `ef453b6`); pengiriman yang memblok masih memperpanjang jeda sebelum putaran berikutnya (Rencana 5b). |
| **R-031** (master lama yang dikarantina saat mati baru dikunci pada tik pertama) | **Dikonfirmasi tertutup untuk restart tanpa agent, dengan sisa** | Urutan berkas/baris dan pelepasan yang butuh bukti dikonfirmasi audit agent dan review. Laporan start yang hilang (SEC-89) dan start gagal yang tidak dilaporkan (SEC-110) diperbaiki. **Sisa:** R-096, R-097, R-112 (SEC-97), R-113 (SEC-98), R-114 (`loose-`/`skip-`, belum dibuktikan), R-115 (SEC-96), R-116; dan SEC-85 (panel yang dibobol bisa melepas kunci itu, R-109). |
| **R-032** (master mati di interval pertama tidak bisa dikarantina) | **Dikonfirmasi tertutup, dengan sisa** | Pencatatan 5 detik dan `mysql.recordReplicas` dikonfirmasi. **Sisa:** R-094; gerbang "pernah tercatat" bisa diisi server milik panel (SEC-86, R-110); `forget-replica` pada replika yang masih tersambung (SEC-114 b, R-120); host tertua dibuang di atas 1 MiB (R-118). |
| **R-033** bagian port (penjaga hanya membaca port kandidat) | **Dikonfirmasi tertutup untuk bagian port** | Semua host × port tercatat dibaca saat kunci dan sebelum dibuka. Fan-out kini dibatasi (SEC-88). `unlockPromote` juga membaca daftar itu (SEC-119). **Sisa:** R-108 (akun lain; ditambah kasus nama host processlist di bawah verifikasi sertifikat), R-098, R-102, R-103. **Diperlebar:** sandi replikasi kini dikirim ke semua port tercatat (SEC-87, R-111, R-039). |
| **R-035** (Resync membuang database "hanya di replika" dengan satu ketik-nama) | **Dikonfirmasi tertutup** | `enforceWipeOnly` atas bacaan segar master, gagal tertutup; kini juga menolak inventaris terpotong (SEC-112). |
| **R-036** (Ulangi resync tanpa konfirmasi) | **Dikonfirmasi tertutup, dengan sisa** | Token per job, batas 24 jam, preflight ulang; diperluas ke semua create_replica sebelum salinan (SEC-104, b9ec203). **Sisa:** SEC-103 (R-124). Review seluruh cabang menyebut (tanpa menilai) bahwa restart panel melanjutkan job pembuang tanpa token; kode di `e138d86` membantahnya: `Runner.Resume` (`orchestrator/runner.go:1010-1048`) menggagalkan job yang berjalan atau mengantre di langkahnya, job yang menunggu tetap menunggu, dan lanjutan apa pun butuh Ulangi atau Setujui bertoken. |
| **R-043** ("Jangan ganggu" membungkam bahaya) | **Dikonfirmasi tertutup, dengan sisa** | Himpunan yang tidak pernah dibungkam = dua belas jenis spesifikasi; ringkasan berutang di store. Pembukuan ringkasan diperbaiki (SEC-113). **Sisa:** R-106, R-107, SEC-122 (R-129). |
| **R-058** (Tambah database meninggalkan tabel lama) | **Dikonfirmasi tertutup** | Penantian ketik-nama bertoken. Pembacaan ulang di langkah dump (SEC-111) dan nama beda huruf (SEC-118) diperbaiki. |
| Jalur tambahan spesifikasi §5.9 | Delapan dari sembilan ditutup menurut spesifikasi; tidak dikonfirmasi satu per satu oleh pemeriksa | Sembilan jalur di tabel spesifikasi: nilai `OFF` tersimpan; `dbpanel_admin` tersembunyi; daftar kosong tanpa `performance_schema`; Ulangi/Setujui tanpa token; senyap membungkam `replica_unlocked`/`promote_partial`; deteksi tumbang mengikuti interval; anggaran baca habis diam-diam; `DropExisting`; replika sebagian tidak terbaca penjaga. Yang terakhir tetap terbuka (R-108). Temuan audit yang menyentuh jalur-jalur itu (SEC-88, SEC-104) sudah diperbaiki. |

## 6. Peta OWASP Top 10 dan ASVS

### 6.1 OWASP Top 10 (2021), delta Rencana 5a

| Kategori | Yang berlaku di Rencana 5a | Temuan | Putusan di `e138d86` | Bukti |
|---|---|---|---|---|
| A01 Broken Access Control | Pin peran, `releaseRole`, gerbang karantina, operasi berkas root, `?buka=` | SEC-85, SEC-86, SEC-90, SEC-96, SEC-109, SEC-123 | **rentan** (SEC-85/86 terbuka) | Dibaca; SEC-90 dipin test |
| A02 Cryptographic Failures | Pembacaan replika tanpa verifikasi | SEC-87 | **rentan** | Dibaca, termasuk driver MySQL |
| A03 Injection | Baris berkas opsi, nama database, teks agent | SEC-108, SEC-121 | **sebagian** (tampilan saja) | `dbNameRe` di agent; `html/template` |
| A04 Insecure Design | Kunci start, kunci promosi, persetujuan, peringatan | SEC-86, SEC-88, SEC-89, SEC-91, SEC-92, SEC-93, SEC-97, SEC-101, SEC-102, SEC-103, SEC-104, SEC-106, SEC-107, SEC-122 | **sebagian** | Yang diperbaiki dipin test; SEC-97, SEC-103, SEC-107, SEC-122 terbuka |
| A05 Security Misconfiguration | Lingkungan `--print-defaults`, batas port, header | SEC-88, SEC-98, SEC-124 | **sebagian** | SEC-98 terbuka |
| A06 Vulnerable and Outdated Components | — | — | **tidak diperiksa di audit ini** | govulncheck tidak dijalankan (Lampiran 8.2) |
| A07 Identification and Authentication Failures | Akun replikasi bersama dikirim ke alamat tercatat | SEC-87 | **rentan** | Dibaca |
| A08 Software and Data Integrity Failures | Tidak ada perubahan distribusi | — | **tidak berubah** | — |
| A09 Security Logging and Monitoring Failures | Laporan start, kejadian tak terbaca, audit pengakuan | SEC-89, SEC-94, SEC-95, SEC-105, SEC-106, SEC-110 | **sebagian** (SEC-105 terbuka) | Yang diperbaiki dipin test |
| A10 SSRF | Pembacaan peer ke alamat tercatat, bukan dari input pengguna | SEC-99 | **aman setelah perbaikan** | `limitedConn` (batas 256 KiB tidak dipin test) |

### 6.2 OWASP ASVS 4.0.3 Level 2 (bab yang relevan dengan delta)

| Bab | Gagal / tak terverifikasi | Temuan | Lulus (contoh) |
|---|---|---|---|
| V1 Architecture | 1.4.x kontrol akses ditegakkan di sisi tepercaya: agent mempercayai panel untuk pelepasan dan karantina | SEC-85, SEC-86 | Penegakan `dropsData` di orkestrator, bukan di web |
| V2 Authentication | 2.10.x kredensial layanan: satu akun replikasi bersama dikirim ke alamat tercatat | SEC-87 | Sandi tidak di argv atau log |
| V4 Access Control | 4.3.1 aksi administratif dengan konfirmasi: persetujuan yang mengantre | SEC-103 | Token per job dan per aksi; batas 24 jam |
| V7 Error Handling and Logging | 7.1.x apa yang diakui tidak diaudit; 7.2.x peringatan tidak terikat peran | SEC-105, SEC-107 | Batas laju kejadian; kejadian tak terbaca tetap membuka peringatan |
| V9 Communications | 9.2.1 verifikasi sertifikat ke server lain bersifat opsional | SEC-87 | Dengan "Perketat", rantai dan nama diperiksa sebelum autentikasi |
| V12 Files and Resources | 12.3.x operasi berkas di direktori yang mungkin milik pengguna lain | SEC-96, SEC-97 | `O_NOFOLLOW` + `fstat` untuk log error dan berkas kunci |
| V14 Configuration | 14.4.x header (dibaca dari kode, tidak diamati) | SEC-124 | Set header lengkap (§4B) |

## 7. Rekomendasi berurutan dengan prioritas

1. **Keputusan pemilik sebelum merge:**
   - SEC-85, SEC-86: terima sebagai kelas B sampai Rencana 5b, atau minta perbaikan kecil yang diusulkan pemeriksa (tolak `releaseRole` pada `quarantined`; tolak pembuangan setelah karantina yang tidak didahului kunci promosi agent itu sendiri) lebih dulu.
   - SEC-87: terima sampai Rencana 5c dengan saran operator ("Perketat"; `forget-replica` saat replika dipensiunkan).
   - R-117: perbaikan satu baris (pindahkan `flushStartEvents` ke akhir `GuardTick`), bisa dikerjakan sebelum atau sesudah merge. **Dikerjakan sebelum merge di `bb54853` untuk urutan di dalam satu putaran; sisanya (jeda sebelum putaran berikutnya) tetap terbuka di Rencana 5b.**
2. **Rencana 5b (mode ketat, prasyarat, runbook):**
   - SEC-85 (R-109) dan SEC-86 (R-110): persetujuan root lokal.
   - SEC-98 (R-113): jalankan `--print-defaults` dengan `HOME` akun layanan, atau baca `<home>/.my.cnf`.
   - R-114: buktikan bentuk `loose-`/`skip-` dengan mysqld.
   - R-116: bawa hasil start di snapshot kesehatan.
   - R-117: keluarkan pengiriman penjaga dari jalur kritis penjaga (urutan dalam putaran sudah diperbaiki di `bb54853`).
   - SEC-120 (R-121): `set-role master` tanpa memuat kunci rahasia.
   - **Sementara:** jalankan "Periksa data tertinggal" sebelum setiap bangun ulang master lama; jaga host panel; jangan taruh opsi MySQL di home akun `mysql`.
3. **Rencana 5c (akun per replika, "Perketat" wajib):**
   - SEC-87 (R-111) bersama R-038, R-039, R-108.
   - Tanda "port penuh" (R-118, bagian akun bersama).
   - **Sementara:** "Perketat"; `forget-replica` dan `forget-replica-port` sesuai pesan penolakan.
4. **Rencana 5d:** `leftoverDatabases` dengan tanda potong (R-130).
5. **Rencana 5e (butir Sedang/Rendah):**
   - SEC-97 (R-112), SEC-96 sisa (R-115), SEC-103 (R-124), SEC-105 (R-125), SEC-107 dan sisa SEC-92 (R-126), SEC-108 sisa (R-127), SEC-109 (R-128), SEC-122 (R-129);
   - SEC-114 b (R-120), R-118 (field kesehatan), R-119 (keluaran `semua` sudah memperingatkan sejak `bb54853`; sisanya: hanya membuang port yang tidak dipakai), R-122, R-123;
   - SEC-123, SEC-124;
   - butir "terbuka (kecil)" di §4 (pengakuan setelah CLONE, kalimat "N transaksi", penghitung `Dropped`, ringkasan tanpa Telegram, pemberitahuan generik, baris log `promote.go:1023`, batas `WritePromoted`).
6. **Test regresi yang perlu ditambahkan:**
   - batas baca 256 KiB `limitedConn` (SEC-99);
   - SP-11 butir 3 (SEC-108);
   - dua peringatan FR-11 yang belum dipin;
   - test di tepat 24 jam untuk Ulangi (dilewati karena berbasis jam dinding);
   - satu test yang menelusuri setiap rute POST (SEC-123);
   - test bahwa setiap jenis peringatan diklasifikasikan (SEC-122).

## 8. Lampiran

### 8.1 Hasil test gelombang perbaikan

Dijalankan oleh pelaksana gelombang perbaikan, bukan oleh pemeriksa dan bukan oleh penulis dokumen ini.

| Perintah | Commit | Hasil | Durasi |
|---|---|---|---|
| `make test` | `2fc63a4` | **exit 2**: dua kegagalan, keduanya diperbaiki di `e138d86` (lihat di bawah) | 35m58s |
| `make test-e2e` | `e138d86` | exit 0 (`ok internal/panel 522.673s`) | 8m44s |
| `go test -timeout 60m -race -short ./internal/panel/... ./internal/agent/...` | `e138d86` | exit 0, tanpa laporan race | 38m18s |
| `make test` (vet, semua paket, `install_test.sh` dengan shellcheck) | `e138d86` | exit 0 | 36m27s |

Dua kegagalan pada `2fc63a4`:
- `TestLockForPromoteFanOutBounded/count` (test baru): kandidat belum menerapkan akun `dbpanel_repl` yang direplikasi (`Access denied`). Test kini menunggu replika yang sudah sinkron. Perubahan hanya di test.
- `TestE2EPromotePlanned`: Setujui bangun ulang kini membaca ulang koneksi master lama (SEC-91). E2e yang menjaga satu aplikasi tetap tersambung dan menyetujui tanpa pengakuan mendapat 400. Ini perilaku baru yang memang dimaksud. Helper `approveJob` menjadi `approveJobAck`, yang mencentang `accept_connections` bila bangun ulang dimulai dengan pengakuan, dan menggagalkan test bila persetujuan memintanya tanpa diduga.

Selain itu, setiap perbaikan menjalankan test paket yang tersentuh (RED pada kode lama, GREEN sesudahnya). Menurut laporan gelombang perbaikan:
- RED berupa kegagalan perilaku pada kode lama: FR-1/SA-5, FR-2, FR-3, SP-1, SP-2, SA-6, FR-5, FR-6, ledger Task 13 m3, FR-14, FR-12, FR-8 (a), FR-4, FR-13 (a)(b), FR-11 (dengan pembalikan sementara satu baris), SP-4, SP-9, SA-10, SP-11 (butir 1–2), `unlockPromote`, FR-15/SP-7.
- RED berupa kegagalan build atau kompilasi (API atau tanda tangan baru): SA-4, SP-3, SP-5 (orkestrator; sisi web gagal perilaku), FR-8 (b), SA-7.
- RED tidak dijalankan terpisah (pernyataan baru atas teks yang sebelumnya tidak ada): FR-7, SA-11 (baris `forget-replica`), SA-12, ledger Task 11.
- RED berupa test teks persis yang diperbarui lalu gagal pada kode lama: FR-10.
- Tidak dinyatakan dalam laporan: SA-11 selain baris `forget-replica` (`unlock-master`, pesan berkas tak valid).

### 8.2 Dependensi dan govulncheck

govulncheck dan daftar dependensi **tidak dijalankan untuk audit ini**. `go.mod` dan `go.sum` tidak dibandingkan dalam penulisan dokumen ini. Tidak ada keluaran yang bisa dicantumkan.

### 8.3 Test lama yang ekspektasinya berubah (dari laporan gelombang perbaikan)

- Helper saja: `eventLog` (test driver agent) mendapat sakelar offline; `fakePeer` mendapat jawaban per host.
- `TestDropsData` (`orchestrator/runner_test.go`): baris "create, foreign source wait", "create, reconnect wait", "create, plain", "create, dump onto empty target" berubah dari `false` ke `true`. Ditambah baris "reconnect wait after the copy" (`false`) dan "dump copied" (`false`) (SEC-104).
- Panggilan Ulangi/Setujui job create biasa di test lain kini memakai `JobAck{Confirmed: true}` tanpa pernyataan yang diubah: `TestFailsWhenAgentDropsDuringSnapshot`, `TestJobFailedReachesAlerts`, `TestForeignSourceNamedBeforeReset`, `TestOfflineMasterLogWording`, `TestRetryReopensBeforeNewJobs`, `TestCancelBetweenReopenAndLaunchIsCanceled`, `TestOldCreateReplicaJobResumesAndRetries` (SEC-104).
- `TestRecordReplicasCalledAfterVerify/promote repoints to the new master`: master baru kini menerima 1 `mysql.recordReplicas`, bukan 2 (FR-12).
- `TestSetSnoozeKeepsFromWhenExtended` kini menghapus ringkasan berutang dulu; `TestEndedSnoozesAndClear` memakai tanda tangan `ClearSnoozeFrom(SnoozeSpan)` baru (SEC-113).
- `TestMasterLockExpiredFromEvent`: `monitor.lockExpired` yang tidak terbaca kini membuka pemberitahuan generik (SEC-106).
- `internal/panel/e2e_helpers_test.go`: persetujuan bangun ulang di e2e mencentang `accept_connections` bila diminta (SEC-91).
- `TestLockKeptWithHostsIsCritical`: teks persis mendapat klausa kehati-hatian (SEC-115).
- `TestFailsWhenAgentDropsDuringSnapshot`: perintah replika sesudah Ulangi kini diawali `mysql.preflight` sebelum `mysql.dumpFrom` (lebih ketat).

Pemeriksaan ulang menilai setiap perubahan itu dibenarkan oleh temuannya, dan tidak ada yang dilonggarkan.

### 8.4 Yang tidak bisa dijalankan atau dibuktikan

- **Semua temuan berasal dari membaca kode.** Audit agent, audit panel, review seluruh cabang, dan pemeriksaan ulang tidak menjalankan test, panel, agent, atau mysqld. Satu-satunya yang dijalankan adalah gerbang test di 8.1.
- **Security headers** dinilai dari kode dan sumber Go, bukan dari respons binary yang berjalan (§4B).
- **Kecurigaan audit agent yang belum dikonfirmasi:**
  1. *`loose-init-file` / `skip-init-file`.* `readEffectiveOptions` hanya mengenali `init_file`/`init-file` (`startlock.go:507-510`). MySQL menerima awalan `loose-` pada opsi apa pun, dan (menurut pemeriksa) awalan `skip-` yang mengatur ulang opsi string. Bila berkas operator yang dibaca setelah baris agent memakai bentuk itu, agent bisa melaporkan kunci terpasang padahal mysqld memakai berkas lain atau tidak sama sekali. **Dicek:** parser. **Tidak dicek:** apa yang dicetak `mysqld --print-defaults` untuk bentuk itu dan bagaimana mysqld memperlakukan `skip-init-file` (butuh mysqld yang berjalan). Dicatat sebagai R-114 dengan status "perlu dicek".
  2. *`startLockProved` bertahan melewati perubahan peran lewat CLI.* Bukti hanya diatur ulang oleh karantina yang ditulis saat MySQL mati. Urutan quarantine → rebuilt → promoted → `set-role quarantined` → `set-role replica` dalam satu run agent bisa membuat penjaga menerima bukti lama, bila kedua bendera kebetulan ON tetapi nilai tersimpannya OFF. Pemeriksa tidak bisa menyusun urutan realistis yang membuat bendera ON tanpa tersimpan.
  3. *`set-role master` berlomba dengan penjaga.* CLI melepas kunci start lalu menulis peran. Putaran penjaga di antaranya masih melihat `quarantined` dan menulis kunci lagi. Bila MySQL restart di jendela itu (sekitar 5 detik), berkas init mem-PERSIST baca-saja pada server yang baru dipin root sebagai master. Dibaca dari kode, tidak didemonstrasikan.
  4. *Penjaga yang ditahan panel.* `GuardTick` melewati putarannya selama `promoteMu` dipegang. Pada server yang dikarantina, satu-satunya perintah panel yang mengambilnya adalah `mysql.quarantine`, yang sendiri mem-PERSIST baca-saja lagi. Pada master, `lockForPromote` dulu bisa memegangnya sampai 10 menit; kini dibatasi anggaran 60 detik (SEC-88). Lamanya loop perintah ketat bisa menahan penjaga tidak diukur.
  5. *Tenggat jam dinding.* Tenggat kunci promosi dibandingkan dengan jam dinding. Lompatan jam maju di master selama promote terencana membuat penjaga mulai membaca lebih awal. Pemeriksa menilai ini tidak membuka dua master (kandidat dibaca dua kali berselang semenit, dan agent kandidat sendiri memeriksa master lama sebelum membuka), tetapi sisi kandidat tidak ditelusuri. Bawaan sejak 4b.
- **Kecurigaan audit panel yang belum dikonfirmasi:**
  1. *Journald membuang salinan audit saat banjir log* (bagian SEC-93). Baris Warn yang tidak dibatasi dan salinan audit di logger yang sama sudah dikonfirmasi. Bahwa journald lalu membuang baris layanan itu diambil dari batas laju yang terdokumentasi, tidak diamati.
  2. *`master_lock_kept` ditutup oleh laporan backlog.* Tidak ditelusuri apakah laporan backlog yang diambil sebelum kunci tetapi tiba sesudahnya bisa menetapkan status itu. Agent mengulang kejadian `kept`-nya, dan `raise` akan membuka peringatan lagi.
  3. *Skema yang tidak didaftar inventaris.* `addExisting` mempercayai `mysql.inventory`. `listDatabases` dibaca (hanya empat skema sistem yang dikecualikan), tetapi tidak setiap jalur yang bisa menyembunyikan nama dari `information_schema.schemata`.
  4. *Dialog bertumpuk.* `confirmModal` menutup dialog sumber dan mengirim form-nya dengan input yang terkait `form=` (`confirm_db[]`). Hanya dibaca, tidak dicoba di browser. Ledger sudah meminta pemeriksaan manual.
  5. *Latensi halaman.* `GET /jobs/{id}?buka=ulangi` untuk job bangun ulang menunggu agent master lama sampai batas waktu baca langsung. Tidak diukur.
- **Pemeriksaan ulang** tidak menjalankan test. Ia menilai bahwa run yang dilaporkan pelaksana sudah cukup.
- **Kunci start** hanya diuji pada MySQL 8.0.46 tanpa AppArmor/SELinux yang ditegakkan (R-096). Paket RPM dan Oracle tidak diuji.
- **Perilaku `caching_sha2_password`** yang menjadi dasar SEC-87 dibaca dari sumber driver, tidak didemonstrasikan dengan pendengar palsu.
