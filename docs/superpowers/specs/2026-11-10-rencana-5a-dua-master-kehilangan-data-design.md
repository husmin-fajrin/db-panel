# Rencana 5a: Menutup Jalur Dua Master dan Kehilangan Data

Status: draf untuk persetujuan pemilik produk (2026-11-10)
Sumber: `docs/superpowers/security/2026-11-10-inventaris-risiko.md`
(R-029 sampai R-036, R-043, R-058), pembacaan kode di `main` 0912c5f, dan
eksperimen MySQL 8.0.46 (hasil diringkas di bagian 3).

## 1. Maksud dan alasan

Pemilik produk meminta risiko keamanan dan kehilangan data dihilangkan,
bukan dicatat. Inventaris menemukan 65 risiko yang bisa ditutup dengan
kode. Rencana 5 dipecah menjadi 5a sampai 5e; 5a mengerjakan yang paling
berbahaya lebih dulu: jalur yang bisa berakhir dengan dua master yang
sama-sama bisa ditulis, dan jalur yang membuang data tanpa orang sadar.
Semua jalur ini sudah ada di `main` hari ini.

Pembacaan kode menemukan sembilan jalur tambahan di area yang sama; semuanya
masuk lingkup (bagian 5.9).

## 2. Tujuan, bukan-tujuan, anti-tujuan

Tujuan:
- Master lama yang dikarantina tidak pernah menerima tulisan saat MySQL-nya
  menyala lagi, termasuk pada server yang pernah dipromote.
- Penjaga keselamatan agent berjalan dengan irama tetap yang tidak bisa
  dilambatkan dari panel.
- Layar pengalihan tidak pernah menyatakan "tidak ada koneksi" bila ia tidak
  bisa memastikannya, dan bangun ulang master lama menolak selama masih ada
  koneksi kecuali operator menyatakan paham.
- Tidak ada database yang dibuang tanpa konfirmasi segar dari orang yang
  melihat nama database itu, termasuk lewat "Ulangi" dan "Setujui".
- Sinyal dua-master dan master tumbang tidak bisa dibungkam.

Bukan-tujuan (masuk rencana lain):
- Gerbang prasyarat dan mode ketat (5b), akun replikasi per replika dan
  "Perketat" wajib (5c), checksum data (5d), sisa butir Sedang/Rendah (5e).
- Perubahan tampilan di luar yang dibutuhkan butir di sini.

Anti-tujuan (gagal walau "jadi"):
- Perbaikan yang membuka jalur baru bagi panel yang dibobol, misalnya
  agent memercayai daftar replika dari panel.
- Mekanisme yang bisa membuat MySQL master sehat gagal menyala.
- Konfirmasi tambahan pada tindakan darurat yang harus cepat ("Ulangi" pada
  job promote tetap tanpa token).

## 3. Fakta yang menentukan desain

Dari kode:
- Satu loop di agent menjalankan pemeriksaan kesehatan dan semua penjaga,
  dengan jeda `CheckEvery` 10 sampai 3600 detik yang dikirim panel
  (`internal/agent/monitor/monitor.go`). Penjaga tidak berbagi kunci dengan
  pemeriksaan, jadi bisa dipisah.
- Karantina saat MySQL mati hanya menulis pin. Baca-saja baru ditegakkan
  pada detak penjaga pertama setelah MySQL menjawab.
- Server yang pernah dipromote menyimpan `read_only=OFF` dan
  `super_read_only=OFF` di `mysqld-auto.cnf` (`promote.go`).
- `replicas-seen` hanya ditulis dari loop yang sama, hanya memuat host.
- Daftar koneksi hanya menghitung `thread/sql/one_connection`,
  menyembunyikan semua akun `dbpanel_%` dari host mana pun, dan
  menyembunyikan semua sesi akun agent dari server itu sendiri. Bangun ulang
  hanya memperingatkan.
- "Ulangi" dan "Setujui" tidak meminta token. Resync yang gagal di langkah
  salin, saat diulang, langsung membuang daftar tersimpan tanpa preflight.
- "Jangan ganggu" membungkam semua jenis peringatan pada server itu.
  Peringatan yang terbuka lalu tertutup selama senyap ditandai pulih tanpa
  pernah dikirim.
- Tambah database memuat dump di atas isi yang ada.

Dari eksperimen (MySQL 8.0.46):
- Nilai di `mysqld-auto.cnf` mengalahkan berkas konfigurasi dan opsi baris
  perintah. Baris `super_read_only=ON` di berkas konfigurasi tidak berguna
  pada server yang pernah dipromote.
- `init_file` dijalankan setelah nilai tersimpan dimuat dan sebelum klien
  mana pun dilayani: selama `init_file` berjalan, klien tidak mendapat
  jabat tangan; tidak ada satu pun INSERT yang lolos.
- `SET PERSIST` di dalam `init_file` berhasil dan memperbaiki
  `mysqld-auto.cnf`.
- Berkas `init_file` yang hilang atau tidak terbaca membuat mysqld menolak
  menyala. Pernyataan yang gagal menghentikan pernyataan sesudahnya, tetapi
  mysqld tetap menyala.
- Dengan `performance_schema=OFF`, kueri agent mengembalikan kosong tanpa
  error; `information_schema.PROCESSLIST` tetap memuat sesi.
- Sesi X Protocol tampil sebagai `thread/mysqlx/worker` dengan
  `PROCESSLIST_ID` terisi.
- Atribut koneksi buatan klien tampil di
  `performance_schema.session_connect_attrs`.
- Dump agent memuat `DROP TABLE/VIEW/EVENT/FUNCTION/PROCEDURE IF EXISTS`
  per objek di master, tanpa `DROP DATABASE`. Objek yang hanya ada di
  replika bertahan.
- `SHOW REPLICAS` di master mengisi `Port` dengan port replika walau
  `report_port` tidak diatur; `Host` kosong tanpa `report_host`.

Hanya 8.0.46 yang diuji. Lantai dukungan adalah 8.0.26, dan seri 8.4 serta
9.x diterima; bagian 5.2 dan 5.10 memuat pengaman untuk versi yang berbeda
perilaku.

## 4. Pendekatan yang dipertimbangkan

**Karantina yang berlaku saat MySQL menyala (R-031).**
- Baris baca-saja di berkas konfigurasi terkelola. Ditolak: kalah oleh
  `mysqld-auto.cnf` pada server yang pernah dipromote.
- Menyunting `mysqld-auto.cnf` saat MySQL mati. Ditolak: format berversi
  milik MySQL, dan berkas yang rusak membuat MySQL menolak menyala.
- `persisted_globals_load=OFF`. Ditolak: ikut membuang pengaturan TLS dan
  GTID yang disimpan agent.
- Drop-in systemd. Ditolak: tidak mencakup server init-script dan
  `mysqld_safe` (aaPanel).
- **`init_file` selama karantina (dipilih).** Menang atas nilai tersimpan,
  tanpa jendela tulis, memperbaiki nilai tersimpan dengan sendirinya, dan
  bekerja di semua jenis layanan.

**Irama penjaga (R-030).**
- Membatasi `CheckEvery` saja. Ditolak sebagai satu-satunya perlakuan: di
  sisi panel bukan kendali terhadap panel yang dibobol; di sisi agent ia
  mengikat laju laporan ke laju penjaga.
- **Loop penjaga terpisah dengan jeda tetap (dipilih)**, ditambah batas
  atas interval pemeriksaan yang lebih rendah.

**Replika yang belum tercatat (R-032, R-033).**
- Agent menerima daftar replika dari panel. Ditolak: bukti "master baru
  memuat UUID saya" bisa dipalsukan, sehingga panel yang dibobol bisa
  mengarantina master sehat. Itu serangan yang desain karantina larang.
- **Pencatatan lokal yang lebih cepat dan lebih sering (dipilih)**, dengan
  port dari `SHOW REPLICAS`, dan penjaga yang mempertahankan kunci bila ada
  replika yang tidak terbaca.

**Pembuangan database (R-035, R-036, R-058).**
- Hanya memperbaiki teks modal. Ditolak: "Ulangi" tetap membuang tanpa
  konfirmasi.
- **Klasifikasi di modal, pengakuan per database, penegakan di agent, dan
  konfirmasi ulang pada "Ulangi"/"Setujui" (dipilih).**

**"Jangan ganggu" (R-043).**
- Menghapus fitur pada master. Ditolak: pemeliharaan terencana butuh
  ketenangan dari pengingat berulang.
- **Jenis kritis tetap dikirim sekali, pengingatnya yang diredam, dan
  ringkasan saat senyap berakhir (dipilih).**

## 5. Keputusan desain

### 5.1 Loop penjaga tetap (R-030)

- Agent menjalankan loop kedua yang memanggil pemutus FTWRL akun dump,
  penjaga kunci promote dan karantina, serta pencatatan replika. Jedanya
  5 detik setelah satu putaran selesai (bukan ticker), tidak bisa diatur
  dari panel.
- Pemeriksaan kesehatan, laporan, sampel lag, dan sapuan harian tetap di
  loop lama dengan `CheckEvery`.
- Peringatan log penjaga yang sama dibatasi satu per menit.
- Interval pemeriksaan: batas atas turun dari 3600 menjadi 300 detik di
  panel dan agent. Nilai tersimpan yang lebih besar dibaca sebagai 300.
  Alasannya: panel baru tahu MySQL master mati pada pemeriksaan berikutnya,
  dan keputusan promote darurat bergantung pada itu.

### 5.2 Karantina yang terkunci sejak MySQL menyala (R-031)

- Selama peran server `quarantined`, blok konfigurasi terkelola memuat
  `init_file=<berkas>`. Isi berkas, dua baris saja dan dalam urutan ini:
  `SET PERSIST read_only=ON;` lalu `SET PERSIST super_read_only=ON;`.
- Berkas ditaruh di samping konfigurasi terkelola, di luar datadir (CLONE
  mengganti isi datadir): di direktori include bila ada, selain itu di
  direktori berkas konfigurasi utama. Milik root, mode 0644.
- Baris dan berkas ditulis pada setiap karantina, baik MySQL hidup maupun
  mati. Penulisan memakai jalur tulis-validasi-kembalikan yang ada
  (`mysqld --validate-config` bekerja saat MySQL mati) dan hanya menambah
  satu baris; baris lain di blok tidak diubah.
- Baris dan berkas dihapus saat karantina berakhir: bangun ulang selesai,
  `releaseRole`, hapus server, dan `set-role` lewat CLI.
- Hasil `mysql.quarantine` memuat `StartLocked bool` dan alasan bila
  gagal. Bila penulisan gagal (validasi ditolak, direktori tidak bisa
  ditulis, berkas tidak terbaca oleh pengguna `mysql`), karantina tetap
  dipin, dan panel membuka peringatan Kritis "master lama belum terjamin
  terkunci saat MySQL menyala; jangan nyalakan MySQL-nya sebelum agent
  tersambung".
- Bila operator sudah punya `init_file` sendiri, baris agent
  menggantikannya selama karantina. Hasil karantina menyebut ini, dan
  halaman master lama menampilkannya.
- Penjaga karantina yang ada tetap berjalan sebagai lapis kedua. Bila pada
  detak pertama setelah MySQL menyala ia mendapati bendera baca-saja mati
  padahal `StartLocked` benar, ia menguncinya dan mengirim kejadian
  `monitor.quarantineLate`; panel membuka peringatan Kritis "master lama
  sempat bisa ditulis saat menyala; jalankan Periksa data tertinggal".
  Ini pengaman untuk versi MySQL yang berperilaku lain dari 8.0.46.
- Akibat yang diterima: bila berkas itu dihapus orang, MySQL master lama
  menolak menyala sampai agent menulisnya lagi. Ini hanya mengenai server
  yang dikarantina. Agent memeriksa keberadaan berkas di setiap putaran
  penjaga dan menulis ulang bila hilang.

### 5.3 Replika tercatat lebih cepat (R-032)

- Pencatatan `replicas-seen` pindah ke loop penjaga (5 detik).
- `lockForPromote` mencatat kandidat begitu melihat thread Binlog Dump-nya.
- Perintah agent baru `mysql.recordReplicas` hanya memicu pengamatan lokal
  dan tidak menerima data. Panel memanggilnya pada master setelah replika
  terverifikasi tersambung di akhir buat replika, resync, tambah database,
  dan pengarahan ulang.
- Agent tidak pernah menerima host replika dari panel.
- Sisa yang tidak bisa ditutup: master yang mati kurang dari 5 detik
  setelah replika pertama kali tersambung di luar alur panel. Jalur CLI
  untuk kasus ini masuk runbook 5b.

### 5.4 Penjaga kunci membaca semua replika (R-033)

- Bersama host, agent mencatat himpunan port dari `SHOW REPLICAS`.
- Saat tenggat kunci lewat, penjaga membaca setiap host yang tercatat pada
  port kandidat dan setiap port tercatat, paralel, 5 detik per bacaan.
- Bila ada host yang tercatat dalam 48 jam terakhir dan tidak terbaca di
  port mana pun, kunci dipertahankan dan kejadian `monitor.lockExpired`
  dengan tindakan `kept` dikirim dengan daftar host itu. Host yang lebih
  lama dari 48 jam dan tidak terbaca dilewati seperti sekarang.
- Pembukaan kunci memakai konteks waktu sendiri, sehingga hasilnya tidak
  bergantung pada sisa waktu pembacaan.
- Akibat yang diterima: pada topologi dengan replika sebagian (yang tidak
  punya akun replikasi), penjaga tidak pernah membuka kunci sendiri.
  Pembukaan oleh panel saat job gagal tetap bekerja; bila panel tidak bisa
  menjangkau agent, master tetap terkunci sampai `unlock-master` dijalankan
  di server. Ini memilih henti layanan daripada dua master. Akun yang bisa
  membaca replika sebagian masuk 5c.
- CLI baru `dbpanel agent forget-replica <host>` menghapus satu host dari
  catatan, untuk replika yang sudah dipensiunkan. Panel tidak punya
  padanannya.

### 5.5 Daftar koneksi yang jujur (R-029, R-034)

- Kueri membaca semua thread FOREGROUND yang punya `PROCESSLIST_ID`, tanpa
  saringan nama thread. Tetap dikecualikan: `system user`,
  `event_scheduler`, thread Binlog Dump, dan koneksi kueri itu sendiri.
- Setiap baris membawa jenis: `sql`, `x` (X Protocol), atau `lain`. Jenis
  `lain` ditampilkan sebagai "jenis koneksi tidak dikenal".
- Bila `performance_schema` mati, agent membaca
  `information_schema.PROCESSLIST`. Bila itu pun tidak bisa dibaca, atau
  `Performance_schema_thread_instances_lost` lebih dari nol, hasil ditandai
  `Unreadable` atau `Incomplete` dengan alasan.
- Yang disembunyikan hanya akun layanan buatan agent: akun replikasi dan
  akun dump (pola persis dari kode). `dbpanel_admin` tidak lagi
  disembunyikan.
- Semua koneksi agent diberi atribut `program_name=dbpanel-agent`. Yang
  disembunyikan sebagai "milik agent" hanya sesi akun agent dari server itu
  sendiri yang membawa atribut ini. Sesi akun agent lain dari server itu
  dihitung sebagai "sesi akun admin dari server ini". Tanpa
  `performance_schema`, atribut tidak terbaca, jadi semua sesi lokal akun
  agent dihitung dan diberi catatan "tidak bisa dibedakan dari agent".
- Sesi akun admin dari host lain diberi lencana "akun admin panel",
  termasuk `dbpanel_admin`.
- Layar pengalihan menampilkan "Tidak ada koneksi aplikasi tersisa" hanya
  bila hasil lengkap dan kosong. Selain itu ia menampilkan jumlah, atau
  "tidak bisa dipastikan" beserta alasannya.
- Bangun ulang master lama ditolak bila masih ada koneksi atau hasil tidak
  bisa dipastikan, kecuali form membawa centang `accept_connections`
  ("Saya paham N koneksi masih ada dan tulisannya hilang") bersama nama
  server yang diketik dan token yang sudah ada. Orchestrator memeriksa
  ulang daftar koneksi saat job dimulai dan menolak tanpa pengakuan itu.

### 5.6 Pembuangan database dengan konfirmasi segar (R-035, R-036, R-058)

Resync:
- Sebelum modal dibuka, inventaris replika dan master disegarkan.
- Modal mengelompokkan database replika: "ada di master, disalin ulang"
  dan "hanya di replika, isinya hilang permanen". Setiap database kelompok
  kedua punya centang sendiri (`wipe_only`); semuanya wajib dicentang.
  Nama server yang diketik dan token tetap.
- Agent menegakkan hal yang sama: parameter preflight mendapat
  `WipeOnlyReplica`. Preflight gagal bila ada database di daftar buang yang
  tidak ada di master dan tidak ada di `WipeOnlyReplica`. Agent memakai
  daftar database master yang hidup, bukan inventaris panel.

"Ulangi" dan "Setujui":
- Untuk job yang membuang data, keduanya meminta token konfirmasi baru
  (aksi `job_retry` dan `job_approve`, terikat pada id job). Job yang
  membuang data: resync, buat replika dengan daftar buang atau bangun ulang
  master lama, lepas database dengan hapus data, dan tambah database yang
  menimpa sisa (di bawah).
- Dialog "Ulangi" menampilkan lagi daftar database yang akan dibuang dengan
  pengelompokan terkini, dan meminta centang `wipe_only` lagi.
- "Ulangi" selalu menjalankan preflight lagi sebelum langkah buang, dari
  langkah mana pun job itu gagal.
- Job yang konfirmasi terakhirnya lebih dari 24 jam lalu tidak bisa diulang;
  pesannya meminta memulai tindakan baru.
- "Ulangi" pada job promote, perketat TLS, dan sertifikat tetap tanpa token.

Lepas database dengan hapus data: selain token, form meminta nama replika
diketik.

Tambah database (R-058):
- Saat job dimulai, inventaris replika disegarkan. Bila database yang
  ditambahkan sudah ada di replika, job berhenti di langkah tunggu "database
  ini sudah ada di replika (sisa lama) dan akan dibuang sebelum disalin",
  terikat pada hash daftar itu, dan menunggu "Setujui" bertoken.
- Setelah disetujui, agent membuang database itu dengan `sql_log_bin=0`
  lalu memuat dump. Riwayat GTID replika tidak disentuh.
- Halaman database replika mendapat catatan pada Tambah dan pada Lepas
  tanpa hapus data: "bila ditambahkan lagi, isi lama dibuang lebih dulu".

Kemampuan `DropExisting` pada `mysql.dumpFrom` tidak dipakai panel; agent
menolak parameter itu.

### 5.7 "Jangan ganggu" yang tidak membungkam bahaya (R-043)

- Jenis yang tidak pernah dibungkam: `master_down`, `disk_crit`,
  `replica_unlocked`, `promote_partial`, `master_lock_expired`,
  `master_quarantined`, `master_unpinned`, dan dua peringatan baru di 5.2.
- Selama senyap, jenis itu tetap mengirim pesan pertama dan pesan pulih.
  Pengingat berulangnya diredam.
- Jenis lain dibungkam seperti sekarang.
- Saat senyap berakhir, panel mengirim satu ringkasan: rentang waktu,
  jumlah peringatan yang terbuka selama senyap, dan daftarnya, termasuk
  yang sudah pulih. Awal senyap disimpan di basis data (`snooze_from`,
  migrasi 0011), sehingga ringkasan tidak hilang bila panel restart.
- Tombol "Jangan ganggu" membuka dialog konfirmasi biasa (tanpa token) yang
  menyebut jenis mana yang tetap dikirim.

### 5.8 Perubahan protokol dan kompatibilitas

- Baru: `mysql.recordReplicas`; kejadian `monitor.quarantineLate`; field
  `StartLocked` pada hasil karantina; `WipeOnlyReplica` pada parameter
  preflight; jenis baris, `Unreadable`, `Incomplete`, dan hitungan sesi
  admin lokal pada hasil koneksi.
- Aturan yang ada tetap: agent yang versinya berbeda dari panel tidak boleh
  dipakai untuk promote. Agent lama yang tidak mengenal field baru
  diperlakukan sebagai "tidak terjamin": karantina tanpa `StartLocked`
  membuka peringatan Kritis 5.2; daftar koneksi tanpa jenis baris dianggap
  `Incomplete`.

### 5.9 Jalur tambahan dari pembacaan kode

| Jalur | Ditutup oleh |
|---|---|
| Nilai `OFF` tersimpan pada server yang pernah dipromote | 5.2 |
| `dbpanel_admin` tersembunyi dari semua host | 5.5 |
| Daftar kosong saat `performance_schema` mati | 5.5 |
| "Ulangi"/"Setujui" tanpa token pada semua job pembuang | 5.6 |
| Senyap membungkam `replica_unlocked` dan `promote_partial` | 5.7 |
| Deteksi master tumbang mengikuti interval sampai satu jam | 5.1 |
| Anggaran waktu pembacaan replika habis diam-diam | 5.4 |
| Replika sebagian tidak terbaca penjaga | 5.4 (kunci dipertahankan); akun di 5c |
| `DropExisting` yang tidak terpakai | 5.6 |

### 5.10 Verifikasi

- Tes dengan mysqld sungguhan untuk setiap butir: server yang pernah
  dipromote, dikarantina saat mati, lalu dinyalakan sambil klien mencoba
  menulis terus-menerus: tidak ada tulisan yang lolos.
- Tes bahwa penjaga berjalan tiap 5 detik saat `CheckEvery` 300.
- Tes daftar koneksi dengan `performance_schema=OFF`. Sesi X Protocol diuji
  lewat baris thread tiruan bila klien X tidak tersedia di lingkungan tes;
  laporan menyebut mana yang dipakai.
- Tes "Ulangi" pada resync yang gagal di langkah salin: tanpa token
  ditolak; dengan token, preflight berjalan lagi.
- Tes tambah database di atas sisa lama: objek yang hanya ada di replika
  hilang setelah job selesai.
- Tes ringkasan senyap setelah panel restart.
- E2e promote terencana dan darurat yang ada tetap lulus.
- Review per task dan audit keamanan akhir seperti rencana sebelumnya.
  Setiap butir inventaris ditandai "sudah ditangani" dengan commit-nya.

## 6. Yang diserahkan kepada pelaksana

- Nama berkas `init_file` dan cara menyisipkan satu baris ke blok
  terkelola tanpa menulis ulang baris lain.
- Bentuk berkas catatan port replika dan migrasinya dari format lama.
- Cara menandai koneksi agent di semua tempat `OpenDB` dipakai.
- Tata letak modal resync dan dialog "Ulangi".
- Urutan task.

## 7. Risiko yang tersisa setelah 5a

- Master yang mati kurang dari 5 detik setelah replika pertama tersambung di
  luar alur panel tidak bisa dikarantina otomatis (5.3).
- Atribut koneksi bisa dipalsukan klien; pemalsuan hanya menyembunyikan
  sesi yang sudah memakai akun admin dari server itu sendiri (5.5).
- Perilaku `init_file` hanya diuji pada 8.0.46; lapis kedua di 5.2
  melaporkan bila versi lain berperilaku beda.
- Jaringan terbelah dan panel yang dibobol tetap seperti di inventaris
  kelas A dan B; perlakuannya di 5b.
