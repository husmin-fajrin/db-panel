# Desain: Panel Replikasi Database (db-panel)

Tanggal: 2026-10-30
Status: draft, menunggu persetujuan

## 1. Maksud dan alasan

Tim IT rumah sakit perlu punya salinan siaga (replika) dari database SIMRS
mereka, tapi umumnya tidak punya keahlian server dan database untuk
membangun dan merawat replikasi MySQL lewat terminal. db-panel adalah
aplikasi web yang membuat satu replika dengan satu klik, memantau semua
master dan replika di satu tempat, memberi tahu lewat Telegram kalau ada
yang salah, dan menyediakan kontrol dasar (start/stop, resync, promote)
dengan penjelasan dalam bahasa Indonesia yang bisa dipahami orang awam
database.

Ukuran keberhasilan: seorang staf IT rumah sakit, tanpa pernah membuka
terminal MySQL, bisa (a) mendaftarkan dua server, (b) membuat replika,
(c) melihat setiap saat apakah replika masih sinkron, dan (d) saat master
rusak, tahu apa yang terjadi dan apa yang harus dilakukan.

## 2. Pengguna

- **Staf IT rumah sakit.** Pengguna utama. Paham jaringan dan aplikasi,
  tidak paham internal MySQL. Butuh status yang jelas, tombol yang sedikit,
  dan pesan error yang menjelaskan langkah perbaikan.
- **Tim pengembang db-panel** (programmer dengan latar PHP/CI 3, jQuery,
  Bootstrap, dan belakangan daisyUI/Tailwind/Alpine/ECharts). Perlu bisa
  mengubah tampilan tanpa belajar tooling baru.

## 3. Tujuan, bukan tujuan, anti-tujuan

**Tujuan (versi 1)**

1. Satu panel mengelola banyak master dan banyak replika sekaligus.
2. Pembuatan replika MySQL "sekali klik" dari server yang sudah ada MySQL
   maupun server kosong (panel memasang MySQL-nya).
3. Pemantauan status replikasi yang andal: terhubung/putus, jalan/berhenti
   (dengan error-nya), ketinggalan berapa lama, kesehatan dasar server.
4. Verifikasi isi data (checksum) yang bisa diaktifkan dan dijadwalkan
   user, dengan pelambatan otomatis agar master tidak terganggu.
5. Kontrol: start/stop replikasi, resync ulang, promote terencana dan
   promote darurat. Tidak ada promote otomatis.
6. Notifikasi Telegram.
7. Instalasi gaya aaPanel: satu perintah `curl | sudo bash` untuk panel,
   dan perintah siap salin yang dibuat panel untuk tiap agent.
8. Struktur yang memungkinkan engine database lain ditambahkan belakangan.

**Bukan tujuan (versi 1)**

- Engine selain MySQL 8.x (lihat batasan).
- Promote otomatis / failover otomatis.
- Mengalihkan aplikasi SIMRS ke master baru (virtual IP, proxy, DNS).
  Panel hanya memberi instruksi. Virtual IP dicatat sebagai pengembangan
  berikutnya.
- Replikasi multi-master, replikasi berantai (replika dari replika), Group
  Replication, InnoDB Cluster.
- Backup terjadwal, point-in-time recovery, manajemen user aplikasi.
- Mendukung Windows Server (semua server target Linux).
- Multi-tenant, banyak akun dengan peran berbeda. Versi 1 cukup satu
  akun admin (lihat keputusan 9.2).

**Anti-tujuan** (yang membuat produk ini gagal walau "jalan")

- Panel mengambil keputusan berbahaya sendiri: merestart MySQL produksi,
  mempromosikan replika, atau membuang data tanpa konfirmasi manusia.
- Status yang menipu: menampilkan "sinkron" saat koneksi putus, atau
  "identik" saat isi data belum pernah diverifikasi.
- Agent yang menerima perintah shell bebas dari panel.
- Instalasi yang butuh runtime tambahan (Python, Node, PHP) di server
  rumah sakit.

## 4. Batasan

- Semua server target Linux dengan `systemd`, `curl`, `sudo`. Distro
  yang didukung versi 1: Ubuntu 20.04+/Debian 11+ (apt) dan
  RHEL/Rocky/Alma 8+ (dnf).
- Engine versi 1: **MySQL 8.0 dan 8.4** (Oracle MySQL). Jalur snapshot
  utama adalah plugin CLONE (8.0.17+). MySQL 5.7 dan MariaDB **ditolak
  saat preflight** dengan pesan yang jelas. Lihat pertanyaan terbuka
  di bagian 11.
- Master dan replika harus satu seri versi (8.0 dengan 8.0, 8.4 dengan
  8.4); CLONE mensyaratkan ini. Saat memasang MySQL di server kosong,
  agent memasang seri yang sama dengan master.
- Master harus bisa dijangkau dari replika di port MySQL (3306 atau yang
  dikonfigurasi). Panel tidak membuat tunnel di versi 1.
- Server database boleh tanpa internet; cukup bisa menjangkau panel di
  jaringan lokal. Pemasangan MySQL di server kosong tetap butuh akses ke
  repositori paket (internet atau mirror lokal).
- Aplikasi SIMRS menunjuk database dengan IP tetap di konfigurasinya.
- Panel dipasang di salah satu server replika, bukan di master, agar
  panel tetap hidup saat master tumbang.
- Tidak ada akses SSH antar server. Semua pekerjaan jarak jauh lewat
  agent.

## 5. Pendekatan yang dipertimbangkan

1. **Satu program Go, satu binary, dua peran (panel dan agent).** Tanpa
   dependensi runtime, instalasi satu file, cocok untuk kerja dekat sistem
   (service, config, clone, pasang paket). UI dirender server.
   Kekurangan: tim belum familiar dengan Go.
2. **Python (FastAPI) untuk panel dan agent.** Cepat ditulis, pustaka
   matang. Kekurangan: butuh Python dan paket yang cocok di setiap server
   rumah sakit; pembungkusan satu file besar dan rapuh di distro lama.
3. **Membungkus tool yang ada (Orchestrator, Percona Toolkit) dengan UI
   tipis.** Kuat tapi berorientasi DBA, berbahasa Inggris, banyak
   dependensi, tidak menangani server kosong. Digugurkan.
4. **Panel PHP (CI 4) + agent Go.** Tim nyaman di kode panel, tapi butuh
   PHP dan web server di replika, dua bahasa, dan pekerjaan inti panel
   (proses berjam-jam, koneksi tetap ke banyak agent, push real-time)
   canggung di PHP. Digugurkan.

**Dipilih: pendekatan 1.** Alasan: 70% pekerjaan adalah orkestrasi di
sisi server dan instalasi yang harus andal di mesin yang bukan milik kita.
Ketidakakraban tim dengan Go diredam dengan memisahkan tampilan (template
HTML, CSS, JS, yang tim kuasai) dari mesin (Go).

## 6. Arsitektur

### 6.1 Satu binary, dua peran

`dbpanel` adalah satu binary Go. Dijalankan dengan `dbpanel panel` ia
menjadi panel (web UI + pusat kendali + agent lokal untuk server tempat ia
tinggal). Dijalankan dengan `dbpanel agent` ia menjadi agent. Server tempat
panel berada tidak perlu agent terpisah: panel memakai agent tertanam
(in-process) yang diperlakukan sama seperti agent jarak jauh.

### 6.2 Komunikasi panel <-> agent

- Agent membuka koneksi **keluar** ke panel (WebSocket di atas TLS) dan
  menjaganya tetap hidup dengan reconnect otomatis. Server database tidak
  perlu membuka port masuk untuk panel.
- Panel membuat CA internal saat instalasi. Saat pairing, agent menerima
  sertifikat klien dari panel (ditukar dengan token sekali pakai), lalu
  semua koneksi berikutnya mTLS. Agent menyimpan sertifikat dan alamat
  panel di `/etc/dbpanel/agent.yaml`.
- Protokol: pesan JSON tipe `request`/`response`/`event` dengan id
  korelasi. Perintah yang bisa diterima agent adalah **daftar tetap**
  (lihat 6.4), bukan shell bebas.
- Perintah panjang (clone, dump, install MySQL, checksum) berjalan sebagai
  **job** di agent dengan id; agent mengirim event progres dan log, dan
  panel bisa menanyakan status job kalau koneksi sempat putus. Job tidak
  mati kalau koneksi putus.

### 6.3 Panel

- HTTP server Go, UI template HTML dirender server, aset statis tertanam
  (`embed`): `app.css` hasil Tailwind, `alpine.js`, `echarts.js`.
- Data di SQLite (satu file di `/var/lib/dbpanel/panel.db`), akses lewat
  `database/sql` dengan driver CGO-free (modernc.org/sqlite) agar binary
  tetap statis.
- Data hidup (status, progres job, log) didorong ke browser lewat
  **server-sent events** per halaman. Alpine.js menerima dan memperbarui
  DOM.
- Pekerja latar di panel: poller status (tiap 5 detik per server),
  penjadwal checksum, pengirim Telegram, pembersih log.

### 6.4 Agent

Agent berjalan sebagai root (service systemd `dbpanel-agent`). Ia punya
driver engine (versi 1: `mysql`) di balik antarmuka umum. Perintah yang
diterima, semuanya dengan parameter terstruktur:

- `probe`: info OS, CPU/RAM/disk, apakah MySQL terpasang, versi, status
  service, port.
- `mysql.preflight(role)`: kumpulan pemeriksaan untuk peran master atau
  replika (lihat 7.3).
- `mysql.install(series)`: pasang MySQL seri yang diminta dari repositori
  resmi MySQL (apt/dnf), set root lokal via auth_socket, aktifkan service.
- `mysql.configure(role, params)`: tulis `/etc/mysql/conf.d/dbpanel.cnf`
  (atau `/etc/my.cnf.d/dbpanel.cnf`) dan/atau `SET PERSIST`.
- `mysql.restart` (hanya dijalankan setelah konfirmasi manusia di panel).
- `mysql.createReplUser`, `mysql.cloneFrom`, `mysql.dumpFrom`,
  `mysql.configureReplication`, `mysql.start/stopReplica`,
  `mysql.resetReplica`, `mysql.setReadOnly`, `mysql.promote`.
- `mysql.status`: hasil `SHOW REPLICA STATUS`, `@@gtid_executed`, nilai
  heartbeat, `read_only`, koneksi aktif, ukuran data.
- `mysql.checksum.run/pause/resume/cancel`.
- `job.status(id)`, `job.cancel(id)`, `job.log(id, since)`.

Akses agent ke MySQL lokal: `root@localhost` lewat unix socket
(auth_socket) saat berjalan sebagai root; kalau instalasi lama memakai
password root, user memasukkannya sekali saat pendaftaran server dan
agent menyimpannya di `/etc/dbpanel/agent.yaml` (mode 0600).

### 6.5 Model data (panel, SQLite)

- `servers`: id, nama, host/IP, OS, status agent (online/offline, terakhir
  terlihat), versi agent, versi MySQL, peran saat ini (master / replika /
  belum dipakai), catatan.
- `topologies`: satu master dan daftar replikanya. Master = server;
  replika = server + status replikasi + opsi.
- `repl_users`: kredensial user replikasi per master (password
  terenkripsi; kunci di `/etc/dbpanel/panel.key`).
- `jobs`: id, jenis (create_replica, resync, promote, checksum, install),
  target, tahap saat ini, status, mulai/selesai, pesan error, log.
- `status_samples`: ringkasan status per replika per 5 detik, disimpan
  padat (dipangkas setelah 7 hari) untuk grafik lag.
- `checksum_runs` dan `checksum_results` (per tabel: baris dicek, cocok /
  beda, waktu).
- `alerts`: peringatan aktif dan riwayat, dengan status terkirim ke
  Telegram.
- `settings`: Telegram (bot token, chat id), ambang (lag, disk, waktu
  master dianggap tumbang), jadwal checksum default, bahasa.
- `users`: satu admin (username, hash password), sesi.

## 7. Alur utama

### 7.1 Instalasi panel

```
curl -sSL https://<unduh>/dbpanel/install.sh | sudo bash
```

Skrip: deteksi distro/arsitektur, unduh binary ke `/usr/local/bin/dbpanel`,
buat `/etc/dbpanel/` dan `/var/lib/dbpanel/`, buat CA, buat admin awal
dengan password acak, pasang service `dbpanel-panel`, cetak URL
(`http://<ip>:8080`) dan password. Mendukung mode offline
(`DBPANEL_BIN=/path/ke/binary`). Panel memakai HTTP di jaringan lokal
secara default; HTTPS dengan sertifikat sendiri bisa dinyalakan di
pengaturan.

### 7.2 Pendaftaran server (pasang agent)

Halaman "Server > Tambah" membuat token sekali pakai (berlaku 24 jam) dan
menampilkan:

```
curl -sSL http://<ip-panel>:8080/agent/install?token=XXXX-XXXX | sudo bash
```

Skrip yang disajikan panel mengunduh binary dari panel itu sendiri,
memasang service `dbpanel-agent`, melakukan pairing (tukar token dengan
sertifikat), lalu agent muncul "online" di halaman Server dalam beberapa
detik dengan hasil `probe`. Perintah ini bekerja di server tanpa internet.

### 7.3 Buat replika sekali klik

Dari halaman master, tombol **Buat replika** meminta: server tujuan
(dari daftar server yang online dan belum berperan), dan kalau master
belum punya user replikasi, kredensial admin master untuk membuatnya
(dipakai sekali, tidak disimpan). Lalu job `create_replica` berjalan
dalam tahap-tahap, masing-masing tampil dengan status, durasi, dan log:

1. **Preflight master** (lewat agent master): versi MySQL 8.0/8.4,
   `server_id` != 0, `log_bin=ON`, `binlog_format=ROW`,
   `gtid_mode=ON` + `enforce_gtid_consistency=ON`, `binlog_row_image`,
   plugin CLONE aktif atau bisa diaktifkan, `bind-address` menerima
   koneksi dari IP replika, port bisa dijangkau dari replika (agent
   replika mencoba TCP connect), user replikasi ada dengan hak
   `REPLICATION SLAVE` + `BACKUP_ADMIN`, ruang disk replika >= ukuran data
   master x 1.2.
2. **Siapkan master**. Yang bisa online dilakukan otomatis: `SET PERSIST
   server_id`, `enforce_gtid_consistency=ON` lalu `gtid_mode` dinaikkan
   bertahap (OFF_PERMISSIVE -> ON_PERMISSIVE -> ON) sambil menunggu
   transaksi anonim habis, `INSTALL PLUGIN clone`, buat user replikasi.
   Yang butuh restart (`log_bin` mati, `bind-address` hanya localhost)
   **menghentikan job** dengan penjelasan dan tombol "Restart MySQL di
   master sekarang" yang harus ditekan manusia, atau instruksi manual.
3. **Preflight replika**: MySQL ada atau belum (kalau belum, jalankan 7.4
   lalu lanjut), seri versi sama dengan master, data dir kosong atau user
   mengonfirmasi akan dibuang, disk cukup.
4. **Siapkan replika**: tulis config (`server_id` unik,
   `gtid_mode=ON`, `enforce_gtid_consistency=ON`, `log_bin`,
   `read_only=ON`, `super_read_only=ON`, `relay_log` dsb.), restart
   MySQL replika (boleh otomatis: belum melayani siapa pun), `INSTALL
   PLUGIN clone`, `SET GLOBAL clone_valid_donor_list`.
5. **Snapshot**: `CLONE INSTANCE FROM 'repl'@'master':3306 IDENTIFIED BY
   '...'`. Progres dibaca dari
   `performance_schema.clone_progress`. MySQL replika restart sendiri di
   akhir clone; agent menunggu sampai siap. Jalur fallback `mysqldump
   --single-transaction --set-gtid-purged=ON` hanya untuk kasus CLONE tidak
   mungkin (misalnya plugin tidak bisa diaktifkan) dan selalu diberitahukan.
6. **Sambungkan**: `CHANGE REPLICATION SOURCE TO SOURCE_HOST=...,
   SOURCE_USER=..., SOURCE_PASSWORD=..., SOURCE_AUTO_POSITION=1,
   SOURCE_SSL=1 (jika master mendukung), GET_SOURCE_PUBLIC_KEY=1` lalu
   `START REPLICA`.
7. **Verifikasi**: tunggu IO dan SQL thread `Yes`, `gtid_executed` replika
   mencakup milik master, heartbeat (7.5) tiba, lag turun ke 0. Baru
   replika ditandai **Sinkron**.

Semua tahap idempoten: job yang gagal bisa "Ulangi dari tahap ini".
Password user replikasi dibuat panel (acak, 24 karakter) dan disimpan
terenkripsi.

### 7.4 Pasang MySQL di server kosong

Agent menambahkan repositori resmi MySQL (paket `mysql-apt-config` /
`mysql84-community-release`) untuk seri yang sama dengan master, memasang
`mysql-server`, mengaktifkan service, dan memastikan `root@localhost`
memakai auth_socket. Progres dari output package manager diteruskan ke
log job. Kalau repositori tidak bisa dijangkau, job berhenti dengan
pesan "server ini tidak bisa mengunduh paket MySQL" dan petunjuk mirror.

### 7.5 Pemantauan lapis 1 (selalu aktif)

- Panel membuat database kecil `dbpanel` di master dengan tabel
  `heartbeat(id, ts)`; agent master menulis `ts = NOW(6)` tiap 2 detik.
  Agent replika membaca baris itu; selisih waktu = **lag nyata** dalam
  detik, tidak tergantung `Seconds_Behind_Source`.
- Tiap 5 detik panel meminta `mysql.status` dari tiap agent dan
  menurunkan satu dari status ini untuk replika:
  - **Sinkron**: IO=Yes, SQL=Yes, lag < ambang (default 10 dtk).
  - **Ketinggalan**: jalan tapi lag >= ambang.
  - **Terputus**: IO=No / Connecting (dengan `Last_IO_Error`).
  - **Berhenti**: SQL=No (dengan `Last_SQL_Error`, nomor error, dan
    penjelasan awam untuk error umum: 1032, 1062, 1236, 1594).
  - **Agent offline**: tidak ada data dari agent > 30 dtk.
  - **Belum diverifikasi** sebagai tanda tambahan kalau checksum belum
    pernah dijalankan.
  Untuk master: **Sehat**, **Tidak terjangkau** (agent online tapi MySQL
  tidak merespons), **Agent offline**, lalu **Diduga tumbang** kalau
  tidak terjangkau > ambang (default 60 dtk).
- Replika selalu dijaga `super_read_only=ON`; kalau berubah (ada yang
  mengubah manual), panel menampilkan peringatan dan menawarkan
  mengembalikan.
- Kesehatan server: disk data dir (peringatan 85%, kritis 95%), load,
  RAM, ukuran data, koneksi aktif.

### 7.6 Verifikasi isi data lapis 2 (checksum, opsional)

Tidak aktif default. Di halaman replika, kartu "Verifikasi isi data"
memperlihatkan "Belum pernah diverifikasi" sampai user menjalankan manual
atau mengaktifkan jadwal (default saat diaktifkan: setiap hari 02:00,
durasi maksimal 3 jam, lanjut malam berikutnya).

Mekanisme (model pt-table-checksum): agent master, dalam satu sesi dengan
`binlog_format=STATEMENT`, untuk tiap tabel dan tiap potongan (default
1000 baris berdasar primary key) menjalankan
`REPLACE INTO dbpanel.checksums (db, tbl, chunk, lo, hi, master_crc,
master_cnt) SELECT ..., BIT_XOR(CRC32(CONCAT_WS(...))), COUNT(*) FROM ...
WHERE pk BETWEEN lo AND hi`. Karena direplikasi sebagai statement, replika
menghitung ulang CRC pada barisnya sendiri. Setelah replika menyerap
potongan itu (ditunggu lewat `WAIT_FOR_EXECUTED_GTID_SET`), agent replika
membandingkan `master_crc` dengan hasil hitungnya dan mencatat per
potongan. Pelambatan: jeda minimal 100 ms antar potongan, jeda membesar
otomatis saat lag > 5 dtk atau `Threads_running` master > ambang; jeda
total dibatasi oleh durasi maksimal. Tabel tanpa primary key dicek
dengan satu CRC keseluruhan dan ditandai "pemeriksaan kasar".

Hasil per tabel: identik / berbeda (potongan mana) / dilewati. Kalau ada
perbedaan: peringatan Telegram, dan tombol **Resync tabel** (dump tabel
itu dari master dan muat ulang di replika dengan `sql_log_bin=0`) atau
**Resync penuh** (7.7).

### 7.7 Kontrol

- **Stop / Start replikasi**: `STOP REPLICA` / `START REPLICA`. Stop
  memberi peringatan bahwa lag akan membesar.
- **Resync ulang**: membangun ulang replika dari snapshot baru (tahap
  4-7 di 7.3). Konfirmasi dengan mengetik nama server karena data replika
  dibuang.
- **Promote terencana** (master hidup): panel menyetel master
  `super_read_only=ON` (menghentikan tulisan), menunggu replika menyerap
  semua GTID, lalu `STOP REPLICA; RESET REPLICA ALL;` di replika,
  `read_only=OFF`, replika lain di topologi diarahkan ke master baru
  (`CHANGE REPLICATION SOURCE TO` dengan auto position). Peran di panel
  bertukar. Master lama ditawarkan untuk dibangun ulang sebagai replika.
- **Promote darurat** (master diduga tumbang): sama tanpa tahap menunggu
  master. Konfirmasi menampilkan posisi GTID terakhir yang diterima dan
  kalimat tegas bahwa transaksi yang belum sampai akan hilang. Master lama
  ditandai **dikarantina**: saat agent-nya online lagi, agent langsung
  menyetel `super_read_only=ON` sebelum hal lain, dan panel menolak
  menjadikannya master sampai dibangun ulang.
- Kedua promote diakhiri layar instruksi: "Ubah alamat database di
  konfigurasi SIMRS ke <IP baru>, lalu restart aplikasi", dengan tombol
  "Sudah saya alihkan" yang menutup job. Sampai ditekan, dashboard
  menampilkan banner "Promote menunggu pengalihan aplikasi".
- **Lepas replika** (berhenti dikelola, MySQL dibiarkan) dan **Hapus
  server** dari panel.

### 7.8 Notifikasi Telegram

Pengaturan: bot token dan chat id, dengan tombol "Kirim tes". Peringatan
dikirim saat status berubah (bukan berulang tiap detik), dengan
pengulangan tiap 6 jam selama masih aktif, dan pesan "pulih" saat
selesai. Jenis: master diduga tumbang (dengan saran promote dan replika
mana yang paling mutakhir), replika terputus/berhenti, lag melewati
ambang, disk hampir penuh, agent offline, checksum menemukan perbedaan,
job gagal, promote selesai menunggu pengalihan aplikasi.

## 8. Antarmuka

Bahasa Indonesia di seluruh UI. Halaman:

- **Dashboard**: kartu ringkasan (jumlah master, replika, peringatan
  aktif), daftar topologi dengan status warna tiap replika, grafik lag 24
  jam, peringatan terbaru.
- **Server**: daftar server dengan status agent, peran, versi MySQL,
  disk; tombol Tambah server (perintah pairing).
- **Master (detail)**: kesehatan master, replikanya, tombol Buat replika,
  pengaturan user replikasi, riwayat job.
- **Replika (detail)**: status besar (Sinkron / Ketinggalan / Terputus /
  Berhenti) dengan penjelasan dan tindakan yang disarankan, grafik lag,
  kartu verifikasi isi data, tombol Stop/Start/Resync/Promote, riwayat
  job dan checksum.
- **Job (detail)**: tahap-tahap dengan status, log hidup, tombol Ulangi
  dari tahap ini / Batalkan.
- **Peringatan**: aktif dan riwayat.
- **Pengaturan**: Telegram, ambang, jadwal checksum default, HTTPS,
  ganti password, pembaruan panel.

Semua konfirmasi berbahaya memakai modal daisyUI dengan ringkasan dampak
dalam bahasa awam. Error MySQL selalu ditampilkan mentah di bagian
"detail teknis" yang bisa dibuka, dengan penjelasan awam di atasnya.

## 9. Keputusan yang sudah diambil

9.1 **Stack**: Go (1.23+), `net/http` + `html/template`, SQLite
(modernc), WebSocket (nhooyr/websocket atau gorilla), `go-sql-driver/mysql`.
Tampilan: daisyUI 5 + Tailwind CSS 4 dikompilasi dengan Tailwind
standalone CLI (tanpa Node; `app.css` hasil kompilasi dikomit), Alpine.js
untuk interaksi, ECharts 6 untuk grafik. Semua aset tertanam di binary.
Build: `go build`; rilis lintas arsitektur (amd64, arm64) dengan
goreleaser; `install.sh` mengunduh dari halaman rilis.

9.2 **Autentikasi**: satu akun admin, sesi cookie, rate limit login.
Multi-user ditunda.

9.3 **Panel di replika**, bukan di master; agent tertanam untuk host
panel.

9.4 **Tidak ada SSH, tidak ada tunnel** di versi 1. Master harus bisa
dijangkau di port MySQL dari replika.

9.5 **CLONE sebagai jalur utama, mysqldump sebagai jalur kedua yang
digarap setara.** Sebelum MySQL 8.0.37, CLONE mensyaratkan versi patch
donor dan penerima persis sama, sementara repositori resmi hanya
menyediakan patch terbaru; jadi untuk master 8.0.x lama jalur dump akan
sering terpakai. Preflight memilih jalur secara otomatis (CLONE bila
versi memenuhi syarat dan plugin bisa aktif, dump bila tidak) dan
memberi tahu user jalur mana yang dipakai beserta perkiraan durasinya.
Saat memasang MySQL di server kosong, agent mencoba memasang patch yang
persis sama dengan master bila tersedia di repositori; bila tidak,
memasang patch terbaru di seri yang sama dan memakai jalur dump.

9.6 **Tidak ada promote otomatis.** Promote darurat mengarantina master
lama.

9.7 **Checksum nonaktif default**, mekanisme statement-replicated CRC
per potongan dengan pelambatan.

9.8 **Pasang MySQL dari repositori resmi MySQL**, seri mengikuti master.

9.9 **Agent berjalan sebagai root** (butuh mengelola service, config,
dan paket). Perintahnya daftar tetap; parameter divalidasi; tidak ada
eksekusi shell dengan string dari panel.

9.10 **Antarmuka driver engine** di agent (`Driver` dengan metode
preflight, install, configure, snapshot, configureReplication, status,
checksum, promote) dan di panel (teks penjelasan error per engine).
Versi 1 hanya implementasi `mysql`, tapi tidak ada kode panel yang
mengasumsikan MySQL di luar driver dan teks.

9.12 **Cara server menjadi master.** Server yang didaftarkan berperan
"belum dipakai". Ia menjadi master saat user menekan "Jadikan master"
di halaman server, atau otomatis saat dipilih sebagai sumber di alur
Buat replika. Preflight master mendeteksi replikasi yang sudah ada di
luar panel (thread binlog dump di `SHOW PROCESSLIST`, `SHOW REPLICAS`,
dan apakah server itu sendiri replika dari server lain). Kalau menaikkan
`gtid_mode` akan memutus replika lama non-GTID, job berhenti dan
menjelaskan; panel tidak mengadopsi topologi yang sudah ada di versi 1
(bukan tujuan), hanya memperingatkan.

9.13 **Promote replika yang menjadi host panel diizinkan**, dengan
peringatan di layar konfirmasi bahwa setelah itu panel berada di master
dan sebaiknya dipindahkan. Memindahkan panel di versi 1 adalah prosedur
manual yang didokumentasikan: salin `/etc/dbpanel` dan `/var/lib/dbpanel`
ke server baru, jalankan panel di sana, ubah alamat panel di
`/etc/dbpanel/agent.yaml` tiap agent. Fitur "pindah panel" dari UI
ditunda.

9.14 **Rilis di GitHub Releases pada repo ini.** Paket rilis berisi
binary amd64 dan arm64 sekaligus, dan panel menyimpan keduanya agar bisa
menyajikan binary agent untuk arsitektur apa pun tanpa internet. Mode
offline `install.sh` (binary dibawa manual) adalah jalur resmi, bukan
opsi tambahan. Bila repo dijadikan privat, `install.sh` memakai token
baca yang dimasukkan user.

9.15 **Pengaturan proxy HTTP** di panel untuk Telegram dan pembaruan,
karena banyak jaringan rumah sakit hanya mengizinkan internet lewat
proxy. Kanal notifikasi kedua (SMTP) ditunda sampai ada kebutuhan.

9.11 **Struktur repo**: `cmd/dbpanel` (entry), `internal/panel` (http,
ui, store, orchestrator, notify), `internal/agent` (server, jobs,
drivers/mysql), `internal/proto` (pesan), `web/` (template, css sumber,
js, aset), `scripts/` (install.sh, agent-install.sh.tmpl), `docs/`.

## 10. Diserahkan ke builder

- Detail skema SQLite dan migrasi.
- Format persis pesan protokol dan penanganan reconnect/resume job.
- Pemilihan pustaka WebSocket dan cara embed aset.
- Daftar lengkap error MySQL yang diberi penjelasan awam (minimal 1032,
  1062, 1236, 1594, 2003, 1045, 3159).
- Strategi pengujian: unit untuk orkestrator dengan agent palsu; integrasi
  dengan dua kontainer MySQL 8.4 di Docker untuk alur 7.3 lengkap, promote,
  dan checksum.
- Mekanisme pembaruan panel dan agent (minimal: tombol "Perbarui" yang
  mengunduh binary baru dan restart service; agent diperbarui oleh panel).
- Tata letak detail halaman di dalam kerangka bagian 8.

## 11. Pertanyaan terbuka untuk pemilik produk

Lihat percakapan; diisi setelah builder check.
