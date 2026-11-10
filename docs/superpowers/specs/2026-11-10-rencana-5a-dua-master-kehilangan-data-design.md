# Rencana 5a: Menutup Jalur Dua Master dan Kehilangan Data

Status: draf untuk persetujuan pemilik produk (2026-11-10), sudah melewati
builder check; keputusan penulis sesudahnya ada di bagian 8
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
  port dari `SHOW REPLICAS`, dan penjaga yang mempertahankan kunci bila
  replika yang terbaca saat kunci dipasang tidak lagi terbaca.

**Pembuangan database (R-035, R-036, R-058).**
- Hanya memperbaiki teks modal. Ditolak: "Ulangi" tetap membuang tanpa
  konfirmasi.
- **Klasifikasi di modal, pengakuan per database, penegakan di orchestrator, dan
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

Kunci start terdiri dari dua bagian: satu baris `init_file=<berkas>` di
blok konfigurasi terkelola, dan berkas itu sendiri.

Isi berkas, tiga baris dalam urutan ini:
1. `SET GLOBAL super_read_only=ON;`
2. `SET PERSIST read_only=ON;`
3. `SET PERSIST super_read_only=ON;`

Baris pertama mengunci walau penyimpanan gagal (eksperimen: pernyataan yang
gagal menghentikan pernyataan sesudahnya).

Letak dan izin:
- Di luar datadir (CLONE mengganti isi datadir, dan berkas yang hilang
  membuat mysqld menolak menyala). Di direktori include bila ada, selain
  itu di direktori berkas konfigurasi utama. Milik root, mode 0644.
- Nama berkas harus termasuk yang boleh dibaca mysqld menurut profil
  AppArmor/SELinux bawaan paket MySQL (Ubuntu, Oracle APT/YUM). Perencana
  wajib membaca profil itu dari paketnya dan mencatat buktinya di rencana;
  ini belum diuji di eksperimen.

Menulis kunci:
- Kunci ditulis pada setiap karantina, baik MySQL hidup maupun mati.
  Urutannya: berkas dulu, baru baris. Penulisan baris memakai jalur
  tulis-validasi-kembalikan yang ada dan hanya menambah satu baris.
- `renderConfig` ikut memuat baris itu selama peran `quarantined`, sehingga
  penulisan konfigurasi lain (misalnya saat bangun ulang) tidak
  membuangnya.
- Bila MySQL hidup saat karantina, agent juga menjalankan `SET PERSIST`
  untuk kedua bendera saat itu juga.
- Agent memeriksa nilai efektif dengan `mysqld --print-defaults` memakai
  berkas konfigurasi yang sama dengan layanan. `StartLocked` benar hanya
  bila `init_file` terakhir adalah berkas agent dan berkas itu terbaca oleh
  pengguna `mysql` (pemeriksaan izin biasa).
- Bila operator punya `init_file` sendiri di berkas yang dibaca lebih
  dulu, baris agent menang karena urutan. Agent tidak menyunting berkas
  operator. Hasil karantina menyebut `init_file` operator yang tidak
  dijalankan selama karantina, dan halaman master lama menampilkannya.
- Hasil `mysql.quarantine` memuat `StartLocked bool`, alasannya bila
  salah, dan `MACConfined bool` (mysqld dibatasi AppArmor atau SELinux).
  Bila `StartLocked` salah, karantina tetap dipin dan panel membuka
  peringatan Kritis "master lama belum terjamin terkunci saat MySQL
  menyala; jangan nyalakan MySQL-nya sebelum agent tersambung".

Menjaga kunci (loop penjaga, 5 detik, di bawah kunci promote):
- Selama peran `quarantined`: berkas dan baris harus ada. Yang hilang
  ditulis ulang; kegagalan dicoba lagi paling sering sekali per menit.
  Server yang dikarantina oleh agent versi lama mendapat kuncinya pada
  putaran pertama setelah agent diperbarui.
- Pada putaran pertama setelah MySQL menyala, penjaga menjalankan
  `SET PERSIST` untuk kedua bendera dan memeriksa `@@init_file`. Tiga
  hasil yang dilaporkan lewat kejadian `monitor.quarantineStart`:
  bendera sempat mati (`late`, peringatan Kritis "master lama sempat bisa
  ditulis saat menyala; jalankan Periksa data tertinggal"); `@@init_file`
  bukan berkas agent (`bypassed`, peringatan Kritis "kunci start tidak
  dipakai MySQL; periksa opsi layanan"); selain itu `ok`.
- Selama peran `quarantined` dan MySQL mati, penjaga membaca ekor log
  error MySQL. Bila ada "Failed to open the bootstrap file" untuk berkas
  agent, ia mengirim kejadian dan panel membuka peringatan Kritis "MySQL
  master lama gagal menyala karena berkas kunci tidak terbaca" dengan
  langkah perbaikannya. Arah gagal ini aman bagi data: MySQL tidak
  menyala, jadi tidak bisa ditulis.

Melepas kunci:
- Kunci dilepas hanya bila peran bukan lagi `quarantined` **dan** agent
  sudah berhasil menjalankan `SET PERSIST` ON pada MySQL yang hidup sejak
  karantina. Sebelum itu kunci tetap ada, apa pun perannya.
- Urutannya: baris dulu, baru berkas. Berkas tidak pernah dihapus selama
  konfigurasi terkelola masih menyebutnya.
- Pelepasan dikerjakan loop penjaga, bukan langkah sekali jalan: di setiap
  putaran, bila baris masih ada dan syarat lepas terpenuhi, penjaga
  melepasnya. Jadi kegagalan tulis atau agent yang mati di tengah jalan
  diulang sendiri, tanpa berkas penanda.
- Pengecualian: `set-role master` lewat CLI melepas kunci saat itu juga,
  sebelum pin diubah. Bila pelepasan gagal, peran tidak diubah dan CLI
  menyebut sebabnya.
- `mysql.promote` dan pemeriksaan kandidat menolak server yang barisnya
  masih ada dan tidak bisa dilepas.
- "Hapus server" tidak melepas kunci bila MySQL mati (`releaseRole` butuh
  MySQL hidup). Server itu tetap dikarantina dan terkunci; dialog hapus
  mengatakannya. Cara memakai ulang server itu masuk runbook 5b.

Akibat yang diterima: bila berkas kunci dihapus orang saat agent mati,
MySQL server itu menolak menyala sampai agent hidup dan menulisnya lagi.
Ini hanya mengenai server yang kuncinya belum dilepas.

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
- Saat `lockForPromote`, agent membaca bendera baca-saja kandidat dan
  setiap host tercatat (pada port kandidat dan setiap port tercatat,
  paralel, 5 detik per bacaan), lalu menyimpan daftar host yang terbaca
  bersama kunci. Bila kandidat tidak terbaca, kunci ditolak dan promote
  gagal di awal tanpa akibat.
- Saat tenggat kunci lewat, penjaga membaca lagi host dalam daftar itu.
  Bila ada yang bisa ditulis, perilaku yang ada berlaku. Bila ada yang
  tidak terbaca, kunci dipertahankan dan kejadian `monitor.lockExpired`
  dengan tindakan `kept` dikirim dengan daftar host itu; panel membuka
  peringatan Kritis yang memuat perintah `unlock-master`.
- Host yang sudah tidak terbaca saat kunci dipasang (replika sebagian
  tanpa akun replikasi, replika yang sudah lama mati) dilewati seperti
  sekarang. Jadi topologi dengan replika sebagian tidak terkunci
  selamanya.
- Penjaga memeriksa ulang tiap menit. Kunci terbuka sendiri begitu semua
  host dalam daftar terbaca dan baca-saja.
- Pembukaan kunci memakai konteks waktu sendiri, sehingga hasilnya tidak
  bergantung pada sisa waktu pembacaan.
- CLI baru `dbpanel agent forget-replica <host>` menghapus satu host dari
  catatan dan dari daftar kunci yang sedang berdiri. Panel tidak punya
  padanannya.

Akibat yang diterima, dan satu-satunya keputusan di spek ini yang menukar
ketersediaan dengan keamanan data: bila replika yang terbaca saat kunci
dipasang menjadi tidak terbaca (jaringan terbelah, atau MySQL-nya mati),
master tetap baca-saja sampai replika itu terbaca lagi atau `unlock-master`
dijalankan di server. Hari ini kunci selalu terbuka sendiri paling lama
satu jam. Panel yang dibobol bisa memakai ini untuk menahan master dalam
keadaan baca-saja setelah panel dicabut, dengan mematikan MySQL sebuah
replika; keadaan itu berakhir saat MySQL replika dinyalakan lagi. Akibatnya
henti tulis, bukan kehilangan data. Alternatifnya (membuka kunci setelah
batas waktu) mengembalikan jalur dua master R-033.

### 5.5 Daftar koneksi yang jujur (R-029, R-034)

- Kueri membaca semua thread FOREGROUND yang punya `PROCESSLIST_ID` dan
  `PROCESSLIST_USER` tidak NULL, tanpa saringan nama thread. Tetap
  dikecualikan: `system user`, `event_scheduler`, thread Binlog Dump, dan
  koneksi kueri itu sendiri. Syarat pengguna tidak NULL membuang thread
  sistem seperti `compress_gtid_table`, yang ada di setiap server GTID.
  Sesi yang sedang masuk (`unauthenticated user`) dihitung sebagai koneksi.
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
- Penegakan ada di orchestrator, satu tempat untuk mulai dan "Ulangi":
  tepat sebelum preflight, inventaris master dibaca langsung dari agent
  master. Job ditolak bila ada database di daftar buang yang tidak ada di
  master dan tidak ada di daftar `wipe_only` yang diakui. Bila inventaris
  master tidak bisa dibaca, job ditolak.
- Agent replika tidak punya cara membaca daftar database master (akun
  replikasi tidak bisa), jadi ini bukan pertahanan terhadap panel yang
  dibobol; panel seperti itu memang bisa memerintahkan pembuangan (kelas B
  di inventaris).

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
  ditambahkan sudah ada di replika, job berhenti di langkah tunggu, terikat
  pada hash daftar itu.
- Panel tidak mengaku tahu asal database itu. Teksnya: "Database X sudah
  ada di replika ini. Panel tidak bisa memastikan isinya sama dengan
  master. Isinya di replika akan dibuang permanen, lalu diganti salinan
  dari master."
- "Setujui" untuk langkah ini meminta token dan nama setiap database itu
  diketik. Ini berlaku baik database itu sisa pelepasan lama maupun dibuat
  langsung di replika.
- Setelah disetujui, agent membuang database itu dengan `sql_log_bin=0`
  lalu memuat dump. Riwayat GTID replika tidak disentuh.
- Halaman database replika mendapat catatan pada Tambah dan pada Lepas
  tanpa hapus data: "bila ditambahkan lagi, isi di replika dibuang lebih
  dulu".

Kemampuan `DropExisting` pada `mysql.dumpFrom` tidak dipakai panel; agent
menolak parameter itu.

### 5.7 "Jangan ganggu" yang tidak membungkam bahaya (R-043)

- Jenis yang tidak pernah dibungkam: `master_down`, `disk_crit`,
  `replica_unlocked`, `promote_partial`, `master_lock_expired`,
  `master_quarantined`, `master_unpinned`, kunci yang dipertahankan (5.4),
  dan peringatan baru di 5.2.
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

- Baru: `mysql.recordReplicas`; kejadian `monitor.quarantineStart` dan
  kejadian gagal-menyala di 5.2; field `StartLocked` dan `MACConfined` pada
  hasil karantina; daftar host terbaca pada kunci promote; jenis baris,
  `Unreadable`, `Incomplete`, dan hitungan sesi admin lokal pada hasil
  koneksi.
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
| Replika sebagian tidak terbaca penjaga | 5.4 (dilewati seperti sekarang); akun di 5c |
| `DropExisting` yang tidak terpakai | 5.6 |

### 5.10 Verifikasi

- Tes dengan mysqld sungguhan untuk setiap butir: server yang pernah
  dipromote, dikarantina saat mati, lalu dinyalakan sambil klien mencoba
  menulis terus-menerus: tidak ada tulisan yang lolos.
- Tes bahwa penjaga berjalan tiap 5 detik saat `CheckEvery` 300.
- Tes kunci start: baris tanpa berkas tidak pernah tertinggal setelah
  pelepasan yang digagalkan di tengah; server yang dikarantina agent lama
  mendapat kunci setelah pembaruan; `set-role replica` saat MySQL mati
  tidak melepas kunci.
- Tes penjaga kunci: replika yang terbaca saat kunci dipasang lalu
  dimatikan mempertahankan kunci, dan kunci terbuka setelah replika hidup
  lagi; replika sebagian tidak menahan kunci.
- Tes daftar koneksi pada server GTID yang menganggur: hasilnya lengkap dan
  kosong.
- Tes daftar koneksi dengan `performance_schema=OFF`. Sesi X Protocol diuji
  lewat baris thread tiruan bila klien X tidak tersedia di lingkungan tes;
  laporan menyebut mana yang dipakai.
- Tes "Ulangi" pada resync yang gagal di langkah salin: tanpa token
  ditolak; dengan token, preflight berjalan lagi.
- Tes tambah database yang sudah ada di replika: tanpa nama diketik
  ditolak; setelah disetujui, objek yang hanya ada di replika hilang.
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
- Perilaku `init_file` hanya diuji pada 8.0.46 dan tanpa AppArmor/SELinux.
  Bila versi lain berperilaku beda, penjaga melaporkannya (5.2). Bila
  profil keamanan menolak berkas kunci, MySQL master lama tidak menyala dan
  panel memperingatkan; data aman, tetapi "Periksa data tertinggal"
  tertunda sampai diperbaiki.
- Opsi `--init-file` di baris perintah layanan mengalahkan baris agent.
  Agent tidak bisa melihatnya saat MySQL mati; penjaga melaporkannya begitu
  MySQL menyala (5.2, `bypassed`).
- Master bisa tertahan baca-saja selama replika yang tadinya terbaca tidak
  terbaca (5.4).
- Jaringan terbelah dan panel yang dibobol tetap seperti di inventaris
  kelas A dan B; perlakuannya di 5b.

## 8. Keputusan penulis setelah builder check

Reviewer membaca draf dengan kode dan hasil eksperimen. Keputusan berikut
dibuat penulis, bukan pemilik produk; mohon diperiksa saat membaca.

1. Tambah database: draf membuang database yang sudah ada di replika
   dengan teks "sisa lama". Itu jalur kehilangan data baru bagi database
   yang dibuat langsung di replika. Sekarang teksnya jujur dan nama
   database wajib diketik (5.6).
2. Kunci promote: draf mempertahankan kunci untuk setiap replika yang
   tidak terbaca dalam 48 jam. Sekarang hanya untuk replika yang terbaca
   saat kunci dipasang, dan kunci terbuka sendiri saat replika itu terbaca
   lagi (5.4). Sisa akibatnya ditulis di 5.4.
3. Kunci start dilepas hanya setelah MySQL terbukti menyimpan ON; pelepasan
   diulang penjaga sampai tuntas; baris dilepas sebelum berkas (5.2).
4. `set-role replica` atau `unassigned` saat MySQL mati tidak lagi membuka
   jalan bagi MySQL untuk menyala dalam keadaan bisa ditulis (5.2).
5. Isi berkas kunci diawali `SET GLOBAL super_read_only=ON` (5.2).
6. Penegakan `wipe_only` ada di orchestrator dengan inventaris master yang
   segar, bukan di agent (5.6).
7. Daftar koneksi tetap membuang thread tanpa pengguna (5.5).
8. "Hapus server" tidak menjanjikan pelepasan kunci (5.2).
