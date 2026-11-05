# Desain Rencana 4: Promote, Prasyarat Keamanan, HTTPS Panel, Penguatan Audit

Tanggal: 2026-11-05
Status: menunggu persetujuan pemilik produk
Induk: `docs/superpowers/specs/2026-10-30-db-replica-panel-design.md`
(spec utama) dan `2026-10-31-rencana-3-pemantauan-notifikasi-kontrol-design.md`
(Rencana 3). Dokumen ini merinci dan, bila berbeda, menggantikan bagian
7.7 (promote) spec utama, bagian 5.5 Rencana 3 untuk aturan pin yang
berubah, dan temuan audit `docs/superpowers/security/2026-11-02-audit-plan-3.md`
yang disebut di sini. Checksum (7.6), SEC-49, dan minor M1/M5–M8 Rencana 3
ditunda ke Rencana 5.

## 1. Maksud dan alasan

Setelah Rencana 3, panel tahu kapan master tumbang, tetapi tim masih
harus membuka terminal untuk menukar peran. Pada saat itulah mereka
paling tertekan dan paling mudah salah. Rencana 4 membuat panel
menjawab pertanyaan keempat "pawang server": **master mati atau akan
dirawat, apa yang saya tekan?**

Jawabannya satu tombol, tetapi tombol itu mengubah master produksi.
Panel yang dibobol tidak boleh bisa menekannya sendiri. Maka Rencana 4
juga membayar prasyarat keamanan yang selama ini ditunda: agent
membuktikan sendiri bahwa pergantian peran sah, perintah destruktif
butuh konfirmasi kata sandi, panel berjalan tanpa root, sertifikat
MySQL diverifikasi, dan panel memakai HTTPS. Yang terakhir dimajukan
dari Rencana 5 karena Rencana 4 menambah tombol unduh berkas berisi data
asli, dan itu tidak boleh berjalan di atas HTTP polos.

Prinsip pemilik produk yang mengikat seluruh dokumen: **panel tidak
pernah memutuskan; user yang memutuskan, dan user diberi tahu semua
kondisi sehingga tidak salah mengambil keputusan.** Setiap tombol promote
didahului layar yang menampilkan apa yang panel tahu dan apa yang panel
tidak tahu.

## 2. Tujuan, bukan tujuan, anti-tujuan

**Tujuan**

1. Promote terencana: master hidup, tanpa kehilangan data, dijamin.
2. Promote darurat: master tidak terjangkau, dengan layar pertimbangan
   yang menunjukkan perkiraan atau angka pasti transaksi yang mungkin
   hilang, lalu user memutuskan.
3. Master lama dikarantina baca-saja; saat kembali: "Periksa data
   tertinggal", "Amankan data tertinggal" (ekstrak transaksi dari binlog
   ke satu berkas SQL yang bisa diunduh lewat panel), "Bangun ulang
   sebagai replika", atau "Hapus".
4. Layar pengalihan aplikasi: alamat baru, koneksi aplikasi yang masih
   ke master lama, tombol "Sudah saya alihkan", banner dan pengingat.
5. Replika lain diarahkan ke master baru tanpa dump ulang; yang tidak
   terjangkau ditandai dan diarahkan saat kembali.
6. Prasyarat keamanan: agent memverifikasi sendiri pergantian peran
   (5.1), token konfirmasi per perintah destruktif (5.7), panel non-root
   dengan agent terpisah (5.8), sertifikat MySQL dari CA panel dengan
   verifikasi (5.9), dump tanpa akun ber-RELOAD pada seri MySQL yang
   menyediakan hak `FLUSH_TABLES` (5.10), `releaseRole`
   mempertahankan pin sumber (5.10).
7. HTTPS panel otomatis sejak pasang, tanpa langkah tim (5.11).
8. Penguatan audit murah: status pin dan peringatan "Master tidak
   dipin", batas laju laporan, retensi peringatan, hash chain audit,
   sensor kutipan data, batas pesan 1 MiB, tiga minor terakhir Rencana 3
   (5.12).

**Bukan tujuan**

- Promote otomatis atau failover otomatis. Tidak ada, sesuai keputusan
  Rencana 3 (tanpa perbaikan otomatis).
- VIP, DNS internal, atau proxy untuk pengalihan aplikasi. Pengalihan
  manual di tiap aplikasi; panel hanya memberi tahu.
- Menerapkan data tertinggal ke master baru. Panel hanya mengekstrak dan
  menyerahkan berkasnya.
- Checksum isi data (7.6), tanda "hanya di replika", konfirmasi ulang
  daftar buang, Jangan ganggu pada master (SEC-49), minor M1/M5–M8
  Rencana 3, mode ketat persetujuan di luar panel (4.C): Rencana 5.
- Lebih dari satu peran pengguna panel. Tetap satu peran admin.

**Anti-tujuan**

- Dua master yang bisa ditulis pada saat yang sama. Setiap langkah yang
  membuka tulisan di satu server harus didahului kunci atau karantina di
  server lain, atau bukti bahwa server lain tidak terjangkau.
- Panel yang dibobol bisa menukar peran, mengunci master selamanya,
  atau menarik data. Pagar 5.1 dan 5.7 dibangun untuk ini; sisa risiko
  ditulis jujur di bagian 8.
- Tim melihat peringatan sertifikat dan belajar mengabaikannya; atau
  sebaliknya, tim tidak bisa membuka panel karena sertifikat.
- Promote yang gagal di tengah meninggalkan topologi yang tidak bisa
  dipahami user. Setiap langkah punya keadaan yang ditampilkan dan jalan
  keluar yang ditawarkan.

## 3. Batasan

- Semua batasan spec utama (bagian 4) dan Rencana 3 (bagian 3) berlaku.
- Replikasi memakai GTID dan auto position (sudah demikian). Seluruh
  perbandingan posisi di dokumen ini memakai `GTID_SUBTRACT` dan
  `@@global.gtid_executed`.
- `mysqlbinlog` terpasang bersama MySQL server pada semua tata letak
  yang didukung; agent mencarinya seperti `mysqldump` (`findClientTool`).
- Panel di server master: bila panel berjalan di server yang menjadi
  master dan server itu mati, promote darurat tidak bisa dilakukan dari
  panel. README dan form "Siapkan master" memperingatkan: letakkan panel
  di server replika atau server terpisah.
- Akun replikasi `dbpanel_repl` ada di replika lengkap (skema `mysql`
  ikut didump) tetapi tidak di replika sebagian (`--databases <daftar>`).
  `mysql.promote` membuatnya di master baru dengan sandi topologi bila
  belum ada, sebelum replika lain diarahkan. Semua pemeriksaan lintas
  server di 5.1 hanya membaca `@@global.super_read_only`, `read_only`,
  `gtid_executed`, dan `server_uuid`, yang tidak butuh hak khusus;
  tidak ada `SHOW REPLICA STATUS` lintas server, sehingga hak akun
  replikasi tidak berubah.
- Replika sebagian (hanya beberapa database) adalah kasus yang disengaja
  sejak Rencana 2 dan boleh dipromote; transaksi database yang disaring
  tetap tercatat di `gtid_executed` sebagai transaksi kosong, jadi
  pemeriksaan GTID tidak menangkapnya. Panel menangkapnya dari
  inventaris: 5.2 menyebut database yang tidak akan ada di master baru.
- Sertifikat dan berkas yang dibaca `mysqld` harus berada di tempat yang
  boleh dibaca `mysqld` (AppArmor di Ubuntu/Debian hanya mengizinkan
  `/etc/mysql`, datadir, dan sejenisnya; SELinux di RHEL memberi label
  datadir): dokumen ini memakai `<datadir>/dbpanel-tls/`.
- Perubahan peran panel dari root ke user `dbpanel` harus memperlakukan
  instalasi yang sudah ada: `install.sh` memindahkan berkas dan
  kepemilikan tanpa menghapus data (5.8).
- Pengguna awam: antarmuka tidak memakai kata CA, SSL, TLS, GTID, atau
  binlog di teks utama; istilah teknis hanya di "Detail teknis".

## 4. Pendekatan yang dipertimbangkan

**Bagaimana agent yakin perintah "jadilah master" sah**, padahal pin
peran dirancang menolak panel:

- **A. Agent membuktikan sendiri dari keadaan MySQL.** Terencana: agent
  replika mengecek master dari sisinya: master harus terkunci baca-saja
  dan replika sudah menjalankan semua GTID master. Darurat: master tidak
  menjawab dari replika pada tiga percobaan dalam sekitar 30 detik. Panel yang
  dibobol tidak bisa memalsukan keduanya tanpa juga menguasai master.
  Biaya: jika master masih menjawab dari replika, promote darurat
  ditolak dan user diarahkan ke jalur terencana; itu benar secara data.
- **B. Token konfirmasi bertanda tangan panel.** User mengetik ulang
  kata sandi, panel menerbitkan token berumur 2 menit untuk satu
  perintah dan satu sasaran. Melindungi dari klik salah, CSRF, dan sesi
  yang dicuri; tidak melindungi dari panel yang dikuasai karena kuncinya
  di panel.
- **C. Persetujuan di luar panel** (perintah SSH di server replika atau
  tombol bot Telegram). Paling kuat, bertentangan dengan satu klik dan
  memperlambat saat darurat.

**Dipilih: A untuk pergantian peran, B untuk semua perintah destruktif.**
Pemilik produk menyetujui dengan syarat kendali tetap di user dan user
diberi tahu semua kondisi. C disimpan sebagai "mode ketat" Rencana 5.

**HTTPS di jaringan lokal tanpa domain:**

- CA panel sendiri, terpisah dari CA agent, dengan batasan nama (name
  constraints) agar hanya sah untuk subnet jaringan panel dan nama
  panel, bukan untuk situs di internet. Tim memasang satu
  berkas sekali per komputer bila ingin peringatan hilang; tanpa itu
  lalu lintas tetap terenkripsi. Dipilih sebagai jalur utama.
- Sertifikat sendiri (CA internal kantor, Active Directory, domain
  dengan DNS internal): disediakan di balik "Lanjutan".
- Let's Encrypt lewat DNS: butuh domain publik dan API DNS; dibuang.
- Menerima peringatan browser tanpa memasang berkas: tetap berjalan,
  tidak direkomendasikan di antarmuka.

**Panel non-root:** satu jalan wajar: unit `dbpanel-panel` berjalan
sebagai user sistem `dbpanel`, agent di server panel menjadi unit
`dbpanel-agent` root yang sama dengan server lain, tersambung lewat
jalur mTLS yang sama melalui loopback. Alternatif "panel root dengan
pemisahan proses internal" dibuang: tidak mengurangi hak panel.

**Sertifikat MySQL:** satu jalan wajar: CA MySQL milik panel (terpisah
dari CA agent, 5.9) menerbitkan sertifikat server MySQL per server
lewat agent, dimuat tanpa restart (`ALTER
INSTANCE RELOAD TLS`), replika memverifikasi. Alternatif sertifikat
otomatis MySQL (yang dipakai hari ini) tidak bisa diverifikasi siapa
pun.

## 5. Rancangan

### 5.1 Pergantian peran yang diverifikasi agent

Pin peran bertambah satu nilai: `quarantined` (master lama setelah
promote). Nilai sah: `master`, `replica`, `unassigned`, `quarantined`.
Pin sumber pada `quarantined` berisi alamat master baru.

Perintah baru di agent, semuanya memverifikasi keadaan MySQL sendiri
sebelum mengubah pin atau MySQL. Semua kredensial yang diperlukan untuk
bertanya ke server lain dikirim panel dalam parameter (akun replikasi
topologi; semua pemeriksaan hanya membaca `@@global.super_read_only`,
`read_only`, `gtid_executed`, dan `server_uuid`, yang tidak butuh hak
khusus), tidak pernah disimpan agent.

**`mysql.lockForPromote{candidate host:port, repl cred, timeout}`** di
master (pin `master`). Satu-satunya cara panel mengunci master (pin
`master` menolak `setReadOnly{on}` seperti sekarang). Agent menerima
hanya bila `candidate` **saat ini** punya thread `Binlog Dump` dari host
itu: alamat IP hasil resolusi `candidate` sama dengan host thread itu
(`report_host`/`report_port` tidak dipakai karena hanya berlaku setelah
restart). Replika yang tidak sedang tersambung tidak bisa dijadikan
kandidat, sehingga panel yang dibobol tidak bisa mengunci master atas
nama replika yang sudah mati. Monitor juga mencatat tiap host replika
yang pernah tersambung di `/var/lib/dbpanel/replicas-seen` (tanpa batas
waktu, dipangkas setelah 1 tahun); catatan ini dipakai `quarantine`.
Lalu `SET PERSIST super_read_only=ON` (PERSIST, agar restart MySQL
tidak membuka tulis walau sedetik) dan berkas kunci
`/etc/dbpanel/promote-lock` (kandidat, tenggat); selama berkas itu ada,
pin `master` menolak `setReadOnly{off}` (tombol "Buka kunci master"
yang ada; "Kunci kembali" di 5.7 adalah tombol replika dan tidak
tersentuh). Membuka kunci berarti `RESET PERSIST super_read_only` lalu
`SET GLOBAL super_read_only=OFF`, oleh penjaga, `unlockPromote`, atau
CLI. Agent menjalankan
penjaga: pada tenggat (`timeout`, dikirim panel;
job promote memakai 30 menit, yaitu tenggat kejar 10 menit + tenggat
terapan relay log 10 menit + cadangan; maksimum 60) agent memeriksa
`candidate` dengan akun replikasi:
bila `super_read_only=OFF` di sana dan `gtid_executed`-nya memuat UUID
server ini, agent mengkarantina dirinya sendiri (seperti
`mysql.quarantine`); bila `candidate` terjangkau dan masih replika
(`super_read_only=ON` di sana), agent memberi tenggang 60 detik, memeriksa
sekali lagi, dan bila masih replika membuka kunci sendiri, menghapus
berkas kunci, dan melapor (tenggang ini menutup jendela antara
verifikasi ulang `promote` dan pembukaan tulisnya); bila `candidate`
tidak terjangkau, agent tetap terkunci dan mengulang pemeriksaan tiap
menit (default aman: membuka kunci saat kandidat mungkin sudah menjadi
master berarti dua master). `mysql.unlockPromote` membatalkan sebelum
tenggat, hanya bila `candidate` masih replika. Jalan keluar bila
kandidat hilang untuk waktu lama: perintah lokal root `sudo dbpanel
agent unlock-master` di server master, yang menghapus berkas kunci dan
membuka kunci; panel menampilkan perintah itu pada peringatan
`master_locked`. Pasangannya, `sudo dbpanel agent lock-master`, mengunci
dari luar panel (dipakai pada kasus M3 di 5.2 butir 3).

**`mysql.promote{old source host:port, repl cred, mode}`** di replika
(pin `replica`, sumber = `old source`). Agent memverifikasi:

- `planned`: master terjangkau, `super_read_only=ON` di master, dan
  `GTID_SUBTRACT(master.gtid_executed, own.gtid_executed)` kosong.
- Akun `dbpanel_repl@'%'` di kandidat: replika sebagian tidak
  memilikinya (bagian 3). Pin `replica` kini menerima
  `mysql.createReplUser` dengan aturan ketat yang sama seperti master
  (hanya `dbpanel_repl@'%'`, sekali, tidak ada baris lain dengan nama
  itu). Pada `planned`, job menjalankannya di kandidat sebelum
  `lockForPromote`, karena penjaga master memeriksa kandidat dengan akun
  itu. Pada `emergency` tidak ada penjaga; `promote` sendiri membuatnya
  setelah membuka tulis (langkah di bawah).
- `emergency`: master tidak menjawab dari replika ini: tiga percobaan
  sambung TCP dan membaca salam MySQL, masing-masing tenggat 5 detik,
  berjarak 10 detik (sekitar 30 detik); semuanya gagal; dan IO thread
  replika tidak sedang tersambung. Bila master ternyata menjawab dan
  terkunci, agent melanjutkan sebagai `planned`. Bila master menjawab
  dan bisa ditulis, agent menolak dengan pesan "master masih menjawab
  dari replika ini; gunakan promote terencana, atau kunci master dari
  servernya dengan `sudo dbpanel agent lock-master` bila agent-nya tidak
  bisa dipulihkan".

Lalu, pada kedua mode: `STOP REPLICA IO_THREAD`, tunggu relay log yang
sudah diterima diterapkan (`WAIT_FOR_EXECUTED_GTID_SET(retrieved)`,
tenggat 10 menit, kemajuan dilaporkan ke job log) agar transaksi yang
sudah sampai tidak dibuang; pada `planned`, verifikasi master terkunci
diulang tepat sebelum langkah berikut (bila penjaga master sudah membuka
kunci, promote dibatalkan dan replikasi dijalankan lagi); baru `STOP
REPLICA`, lalu **membuka tulis**: `SET PERSIST read_only=OFF; SET
PERSIST super_read_only=OFF` (sejak detik ini penjaga master melihat
kandidat sudah master dan hanya akan mengkarantina, tidak pernah
membuka kunci), baru `RESET REPLICA ALL`; bila akun `dbpanel_repl`
tidak ada, dibuat dengan sandi topologi dari parameter (pada `planned`
job sudah membuatnya lebih dulu, lihat prasyarat); pin peran `master`,
pin sumber dihapus,
catatan `/etc/dbpanel/promoted` berisi waktu, mode, sumber lama, dan
`gtid_executed` saat promote (dipakai layar dan audit). Hasil memuat
`gtid_executed` akhir dan `server_uuid`. Titik tanpa balik adalah
membuka tulis: kegagalan sebelum itu (verifikasi, menunggu relay log)
membuat agent menjalankan `START REPLICA` lagi dan melapor gagal tanpa
perubahan; kegagalan sesudahnya tidak dibalik, dan `promote` bisa
dipanggil ulang: bila tulis sudah terbuka agent melanjutkan langkah
yang belum (`RESET REPLICA ALL`, akun, pin, catatan) tanpa memeriksa
master lagi, karena penjaga master sudah melihatnya sebagai master; bila
tulis belum terbuka, promote diulang dari verifikasi awal. Dengan itu
"Coba lagi" tidak pernah menghasilkan dua master.

**`mysql.quarantine{new master host:port, repl cred}`** di master lama
(pin `master`, terkunci oleh `lockForPromote` atau tidak). Agent
memverifikasi `new master` terjangkau, `super_read_only=OFF`, dan
`gtid_executed`-nya memuat UUID server ini; bila server ini tidak
terkunci oleh `lockForPromote`, `new master` juga harus host yang pernah
tercatat sebagai replika di `replicas-seen` (catatan dipangkas setelah
1 tahun), supaya master lama yang kembali berbulan-bulan kemudian tetap bisa
dikarantina, sementara server asing tidak bisa dipakai panel yang
dibobol untuk mengunci master sehat. Lalu `SET PERSIST
super_read_only=ON; SET PERSIST read_only=ON`, pin peran `quarantined`,
pin sumber = `new master`. Bila MySQL master lama sedang mati, agent
menulis pin saja (verifikasi master baru tetap dilakukan) dan menerapkan
`super_read_only=ON` begitu MySQL kembali. Saat agent start atau MySQL
kembali dengan pin `quarantined`, agent menyetel `super_read_only=ON`
sebelum hal lain dan melapor. Pada promote darurat panel mengirimnya
segera bila agent master lama masih tersambung, atau saat agent itu
tersambung lagi (OnOnline), sebelum perintah lain.

**`mysql.repoint{new master host:port, repl cred}`** di replika lain
(pin `replica`). Agent memverifikasi: sumber yang dipin tidak menjawab
(seperti `emergency`) atau `super_read_only=ON` di sana; `new master`
terjangkau, `super_read_only=OFF`, `gtid_executed`-nya memuat semua
`gtid_executed` replika ini (tidak ada transaksi hanya di replika ini)
dan memuat UUID sumber lama. Lalu `STOP REPLICA; CHANGE REPLICATION
SOURCE TO` host baru dengan auto position; verifikasi sertifikat (5.9)
dinyalakan bila parameter `verify` dari panel benar, yaitu master baru
sudah punya sertifikat panel (`server_tls`); parameter membawa PEM CA
MySQL dan agent menulis `<datadir>/dbpanel-tls/mysql-ca.crt` bila belum
ada (hanya berkas CA, tanpa sertifikat server); selain itu
`SOURCE_SSL=1` tanpa verifikasi seperti hari ini dan panel menandai
`replicas.tls_verified=0` dengan peringatan "Sambungan replikasi belum
diverifikasi"; `START REPLICA`; pin sumber =
`new master`. Bila replika ini punya transaksi yang tidak ada di master
baru, agent tidak mengubah replikasi, tetapi tetap memindahkan pin
sumber ke `new master` (verifikasi yang sama tanpa syarat superset) dan
menolak dengan kode `needs_rebuild` dan daftar selisih GTID, sehingga
"Resync ulang" dari master baru diterima pin.

**Peran `quarantined`** memakai tabel penolakan `replica` (bukan
`master`), ditambah menolak `setReadOnly{off}` (sudah ada di tabel
replika), `lockForPromote`, `promote`, dan `repoint`; `createReplUser`
mengikuti aturan ketat yang sama seperti di atas. Jalur bangun ulang
(`dropDatabases`, `dumpFrom`, `cloneFrom`, `configure{replica}`,
`configureReplication`, `setReplicationFilter`, `resetReplica`,
`startReplica`) diterima **hanya ke sumber yang dipin** (master baru);
`dumpFrom`/`cloneFrom` membuka `super_read_only` sebentar saat memuat
seperti yang sudah dilakukan pada replika (`liftReadOnly`), bukan lewat
`setReadOnly{off}`. Bangun ulang yang berhasil mengubah pin menjadi
`replica`. `releaseRole` pada `quarantined` → `unassigned`
dengan syarat yang sama seperti replika (tanpa konfigurasi replikasi,
tanpa dump thread), pin sumber dipertahankan.

`Bootstrap` (agent dimutakhirkan di server yang sudah ada) membaca
`/etc/dbpanel/promoted` dan pin yang ada; tidak pernah menurunkan
`quarantined` ke peran lain dari keadaan MySQL.

### 5.2 Layar pertimbangan dan pemilihan replika

Halaman `GET /masters/{id}/promote`. Isinya bergantung pada status
master saat dibuka, dan selalu memuat:

- Mode yang akan dijalankan: "Terencana (master hidup)" bila status
  master Sehat/Terkunci; "Darurat (master tidak terjangkau)" bila
  Diduga tumbang/Tidak terjangkau/Agent offline. User tidak memilih
  mode; panel menjelaskan kenapa mode itu yang tersedia, dan pada
  darurat menyediakan tautan "Periksa sekarang" untuk memastikan.
- Tabel replika: nama, status, lag, posisi terhadap master (lihat
  bawah), antrean terapan ("semua diterima, 3 belum diterapkan; akan
  diterapkan sebelum promote"), cakupan database ("semua database" atau
  "sebagian: tidak ada `simrs_lama`, `gudang`", dari inventaris master
  dibandingkan `databases_json`), kesegaran laporan ("laporan 12 detik
  lalu"), tanda "Paling mutakhir" pada satu replika, dan alasan bila
  replika tidak bisa dipilih (Dihentikan, Berhenti dengan error, Agent
  offline, data tidak segar lebih dari 2× interval cek, atau mempunyai
  transaksi di luar master).
- Replika sebagian boleh dipilih (keputusan pemilik produk: itu kasus
  yang disengaja). Pilihan bawaan selalu replika lengkap paling
  mutakhir bila ada. Memilih replika sebagian menampilkan kotak merah
  "Database berikut tidak akan ada di master baru: …; data itu tetap
  utuh di master lama yang dikarantina dan bisa diamankan dari sana",
  dan konfirmasi 5.7 meminta ketik nama replika.
- Replika yang tidak terjangkau ditulis tegas: "Replika B tidak
  terjangkau. Promote tetap bisa dijalankan; B akan ditandai 'Perlu
  diarahkan ulang' dan diarahkan ke master baru saat tersambung lagi.
  Bila ternyata B punya data yang tidak dimiliki master baru, B perlu
  dibangun ulang."
- Jeda tulis: pada terencana, "Aplikasi tidak bisa menulis sejak kunci
  sampai Anda mengganti alamat di tiap aplikasi" dengan perkiraan dari
  lag; pada darurat, "Aplikasi sudah tidak bisa menulis sejak master
  hilang".
- Pilihan replika (default: paling mutakhir) dan tombol "Promote" yang
  membuka konfirmasi 5.7 (kata sandi; pada darurat juga ketik nama
  master lama).

**Posisi terhadap master.** Terencana: `GTID_SUBTRACT(master.executed,
replica.executed)` dari snapshot segar; panel meminta `monitor.snapshot`
ke master dan semua replika saat halaman dibuka (batas laju yang ada).
Darurat, dengan master tidak menjawab:

1. Laporan terakhir master: waktu dan `gtid_executed` terakhir yang
   disimpan panel.
2. Tiap replika: `GTID_SUBTRACT(last_master.executed,
   replica.retrieved)` → "sudah menerima semua yang master laporkan pukul
   HH:MM:SS" atau "tertinggal N transaksi dari laporan terakhir master".
   Replika juga dibandingkan satu sama lain; yang `executed`-nya
   superset semua replika lain ditandai paling mutakhir; bila dua
   replika saling tidak superset (bercabang), keduanya ditandai dan
   promote menolak sampai user memilih yang satu dan menerima bahwa yang
   lain dibangun ulang.
3. Cek dari sisi replika: panel meminta `mysql.probeSource` ke tiap
   replika hidup (sambung TCP + salam MySQL ke sumber yang dipin, 5
   detik). Hasil "master tidak menjawab dari replika A, B" atau
   peringatan "master menjawab dari replika A; masalahnya mungkin di
   jaringan panel atau agent master, bukan MySQL-nya. Promote darurat
   akan ditolak agent. Pulihkan agent master (`systemctl restart
   dbpanel-agent` di server master) lalu jalankan promote terencana;
   bila agent tidak bisa dipulihkan tetapi server bisa diakses, kunci
   master dengan `sudo dbpanel agent lock-master`, lalu promote darurat
   akan berjalan sebagai terencana".
4. Bila agent master lama masih tersambung tetapi MySQL mati: panel
   meminta `mysql.binlogTail`, agent membaca GTID terakhir dari berkas
   binlog terakhir di disk dengan `mysqlbinlog` (tanpa MySQL hidup),
   mengembalikan `gtid_executed` pasti. Angka pada butir 2 menjadi pasti:
   "ada 3 transaksi yang belum sampai ke replika mana pun".
5. Kalimat penutup: "Transaksi yang belum sampai ke replika terpilih
   akan hilang dari master baru. Bila master lama hidup kembali, panel
   bisa memeriksa dan mengamankannya (5.4)."

### 5.3 Job promote

Dua jenis job baru di orchestrator: `promote_planned` dan
`promote_emergency`, satu per topologi, menolak bila ada job lain
berjalan di topologi (semafor `ErrBusy` yang ada). Keduanya tercatat
di tabel `promotions` (5.13) sejak mulai.

**`promote_planned`**, langkah:

1. Kunci master: `mysql.lockForPromote{candidate, repl cred, timeout
   30 menit}`. Job log
   mencatat waktu kunci; layar menampilkan hitungan jeda tulis.
2. Tunggu replika terpilih menyerap: `mysql.waitSynced` yang ada,
   tenggat 10 menit, log kemajuan tiap 5 detik. Gagal → batalkan dengan
   `mysql.unlockPromote`, job gagal, master tidak berubah.
3. Promote replika terpilih: `mysql.promote{planned}`.
4. Karantina master lama: `mysql.quarantine{new master, repl cred}`.
   Gagal (agent
   master lama lepas di tengah) → job menandai "karantina tertunda",
   panel mengirim ulang pada OnOnline; penjaga agent master (5.1) juga
   menutup sendiri pada tenggat.
5. Tukar data panel (5.13) dalam satu transaksi: topologi menunjuk
   master baru, baris replika terpilih dihapus, master lama menjadi
   server `quarantined` milik topologi.
6. Arahkan replika lain: `mysql.repoint` berurutan; yang gagal karena
   tidak terjangkau → `needs_repoint`; yang menolak `needs_rebuild` →
   `needs_rebuild`. Keduanya tidak menggagalkan job; dicatat di log dan
   peringatan (5.14).
7. Selesai → `cutover_pending` menyala, layar pengalihan (5.5), Telegram.

Kegagalan di langkah 3 sebelum titik tanpa balik (5.1): agent sudah
mengembalikan replika ke keadaan semula; job membatalkan kunci dengan
`mysql.unlockPromote` (kandidat masih replika, jadi diterima) dan gagal;
master tidak berubah. Kegagalan sesudah titik tanpa balik: job tidak
membatalkan (tidak ada jalan mundur yang aman dari dua master) dan
tidak membuka kunci master; job berhenti dengan status "selesai
sebagian" dan daftar langkah yang belum, masing-masing dengan tombol
"Coba lagi" (promote yang bisa dilanjutkan, karantina, arahkan
replika). Dashboard menampilkan banner sampai semua selesai.

**`promote_emergency`**, langkah: promote replika terpilih
(`mysql.promote{emergency}`), tukar data panel, karantina master lama
(`mysql.quarantine` segera bila agent master lama masih tersambung,
misalnya MySQL-nya yang mati; selain itu "karantina tertunda", dikirim
pada OnOnline), arahkan replika lain, layar pengalihan. Master lama yang kembali sebelum karantina terkirim bisa
sempat ditulis aplikasi yang belum dialihkan; jendela ini (dari MySQL
hidup sampai agent tersambung dan `quarantine` masuk, biasanya detik)
ditulis di layar pengalihan dan di README. Transaksi yang masuk di
jendela itu ditemukan oleh "Periksa data tertinggal".

Replika tempat panel berjalan boleh dipilih menjadi master baru; agent
lokal (5.8) memperlakukannya seperti replika lain.

Jaringan terbelah: promote darurat membuktikan master tidak terjangkau
dari replika, bukan bahwa master mati. Master yang hidup di sisi
jaringan lain tetap bisa ditulis aplikasi di sisi itu selama jaringan
terbelah, dan karantina tidak bisa sampai. Keputusan pemilik produk:
tanpa penguncian otomatis di master (kunci sendiri saat tidak melihat
panel dan replika akan memadamkan master sehat pada kebetulan yang
salah). Layar pertimbangan mengatakannya dengan tegas ("Pastikan master
benar-benar mati atau tidak lagi dipakai aplikasi; bila ia hidup di
jaringan lain, tulisan di sana akan tertinggal"), dan "Periksa data
tertinggal" menemukan tulisan yang masuk.

### 5.4 Master lama: periksa, amankan, bangun ulang

Halaman server `quarantined` menampilkan kotak "Master lama (dikarantina
sejak HH:MM, promote darurat/terencana ke <master baru>)" dengan:

- **"Periksa data tertinggal"** → `mysql.leftover{new master, repl
  cred}` di master lama: `GTID_SUBTRACT(own.executed, new.executed)`;
  bila tidak kosong, agent menjalankan `mysqlbinlog --include-gtids=<set>
  --verbose --base64-output=DECODE-ROWS` atas berkas binlog yang memuat
  set itu (ditemukan lewat `SHOW BINARY LOGS` dan `Previous_gtids`) dan
  merangkum: jumlah transaksi, database dan tabel yang disentuh, rentang
  waktu. Hasil disimpan panel di `promotions.leftover_json`. Tampilan:
  "Tidak ada data tertinggal" (hijau) atau "Ada 3 transaksi yang tidak
  sampai ke master baru, menyentuh simrs.pasien (2 baris), simrs.kunjungan
  (1 baris), antara 09:41:20 dan 09:41:31". Pada terencana hasilnya
  selalu kosong; tombol tetap ada.
- **"Amankan data tertinggal"** (hanya bila ada) → `mysql.salvage{gtid
  set}`: `mysqlbinlog --include-gtids=<set> --skip-gtids --verbose`
  ditulis ke `/var/lib/dbpanel/salvage/<ts>-<serverid>.sql`, 0600 root.
  Berkas dapat dijalankan dengan `mysql` dan memuat komentar SQL
  terbaca untuk tiap baris. Agent menghapus berkas lebih tua dari 7
  hari (sapuan harian). `mysql.salvageList` mendaftar berkas (nama,
  ukuran, waktu); `mysql.salvageRead{name, offset}` mengembalikan
  potongan 512 KiB; nama diverifikasi terhadap pola ketat dan folder
  tetap.
- **Unduh** → `GET /servers/{id}/salvage/{name}` dengan token 5.7 yang
  diterbitkan untuk berkas itu; panel mengalirkan potongan ke browser
  tanpa menyimpan; audit `salvage_download` mencatat nama, ukuran, dan
  hasil. Hanya lewat HTTPS (5.11).
- **"Bangun ulang sebagai replika"** → form "Buat replika" yang ada
  dengan server terpilih dan master baru sebagai sumber; job
  `create_replica` berjalan dengan peran `quarantined` (5.1). Tombol
  nonaktif selama `cutover_pending` ("Tekan 'Sudah saya alihkan' dulu:
  membangun ulang membuka kunci tulis sebentar saat memuat data, dan
  aplikasi yang belum dialihkan bisa menulis ke sini"). Sesudahnya,
  konfirmasi menampilkan jumlah koneksi aplikasi yang masih ada (5.5)
  dan, bila "Periksa" belum dijalankan atau menemukan data tertinggal,
  memperingatkan bahwa data itu akan hilang dari server ini; selalu
  meminta ketik nama server. Berkas salvage tidak dihapus oleh bangun
  ulang.
- **"Hapus server"**: yang ada; pada `quarantined` memanggil
  `releaseRole` seperti replika, dan seperti bangun ulang tidak tersedia
  selama `cutover_pending` (melepas pin `quarantined` menghilangkan
  penjagaan baca-saja saat start).
- Daftar berkas salvage dengan ukuran, sisa hari, tombol unduh, dan
  tombol hapus (`mysql.salvageDelete{name}`, `POST
  /servers/{id}/salvage/{name}/delete`, dengan token 5.7 karena berkas
  itu satu-satunya salinan data tertinggal).

Telegram mengirim "Master lama <nama> kembali dan dikarantina" saat
karantina masuk, dan hasil "Periksa" saat dijalankan.

### 5.5 Layar pengalihan aplikasi

Halaman `GET /masters/{id}/cutover` (master baru), tersedia selama
`cutover_pending`:

- Judul: alamat master baru (`source_host:port` topologi baru), tombol
  salin. "Arahkan semua aplikasi yang menulis ke database ke alamat
  ini, lalu restart aplikasinya." Alamat lama di bawahnya.
- Jam promote selesai dan, pada terencana, jam kunci dan lama jeda
  tulis berjalan.
- Tabel "Koneksi yang masih ke master lama": `mysql.connections` di
  master lama (processlist dirangkum per host asal, akun, database,
  jumlah; akun `dbpanel_*`, `event_scheduler`, dan thread sistem
  disembunyikan), diperbarui tiap 15 detik lewat fragmen; "Tidak ada
  koneksi aplikasi tersisa" bila kosong; "Master lama tidak terjangkau,
  tidak bisa membaca koneksinya" bila agent-nya offline.
- Tabel yang sama untuk master baru: "Koneksi yang sudah ke master
  baru".
- Catatan jendela karantina darurat (5.3) bila master lama belum
  dikarantina: "Master lama belum dikarantina karena belum terjangkau.
  Pastikan aplikasi dialihkan sebelum ia dihidupkan kembali."
- Tombol "Sudah saya alihkan" (POST, tanpa token, tercatat audit)
  menutup `cutover_pending`; banner dashboard "Promote menunggu
  pengalihan aplikasi" hilang; Telegram berhenti mengingatkan.

Banner dashboard dan halaman master baru memuat tautan ke layar ini.
Telegram: ringkasan promote saat selesai (mode, master lama → baru,
replika yang ikut, yang perlu diarahkan/dibangun ulang, alamat baru),
pengingat tiap 6 jam selama `cutover_pending`.

### 5.6 Replika lain setelah promote

Baris replika memperoleh keadaan `needs_repoint` dan `needs_rebuild`
(5.13). Panel:

- `needs_repoint`: pada OnOnline agent replika itu, panel mengirim
  `mysql.repoint`; berhasil → `running`; menolak `needs_rebuild` →
  `needs_rebuild`. Status halaman: "Perlu diarahkan ulang ke master
  baru; akan dilakukan saat tersambung" dengan tombol "Arahkan sekarang".
- `needs_rebuild`: status "Perlu dibangun ulang: replika ini punya
  transaksi yang tidak ada di master baru" dengan tombol "Resync ulang"
  yang ada (job `resync_replica` dari master baru; pin sumber sudah
  dipindahkan oleh `repoint`, 5.1). Tidak otomatis.
- Keduanya memicu peringatan (5.14) sampai selesai.

### 5.7 Token konfirmasi per perintah destruktif

Komponen modal "Konfirmasi" (Alpine CSP, tanpa JS inline) meminta kata
sandi panel dan, bila handler memintanya, ketik nama server. `POST
/confirm` menerima `action`, `target_kind`, `target_id`, kata sandi;
memverifikasi kata sandi user sesi (batas laju 5 per menit per user,
gagal tercatat audit `confirm_failed`), lalu menerbitkan token acak 32
byte; panel menyimpan hash-nya di `confirmations` dengan user, action,
target, `expires_at` = +2 menit, `used_at`. Handler destruktif menerima
`confirm_token` di form, mencocokkan hash, user, action, target, umur,
dan belum dipakai; menandai dipakai dalam transaksi yang sama. Satu
token satu permintaan.

Wajib token: promote (keduanya), bangun ulang master lama, unduh
salvage, resync ulang, lepas replika, hapus server, lepas database
(`remove_databases`), buka kunci master, hapus berkas salvage,
"Perketat sambungan replikasi", "Pasang sertifikat MySQL", dan
`prepare-master` bila master sudah punya topologi. "Sudah saya alihkan", stop/start,
kunci kembali, snooze, dan periksa tidak butuh token.

Token tidak dikirim ke agent: agent tidak bisa membedakannya dari token
yang dibuat panel yang dibobol (4.B). Pagar agent adalah 5.1.

### 5.8 Panel non-root, agent terpisah

- User sistem `dbpanel` (tanpa login, home `/var/lib/dbpanel/panel`)
  dibuat `install.sh`. Unit `dbpanel-panel.service`: `User=dbpanel`,
  `ProtectSystem=strict`, `ReadWritePaths=/var/lib/dbpanel/panel
  /etc/dbpanel/panel`, `AmbientCapabilities=CAP_NET_BIND_SERVICE` hanya
  bila port < 1024 dikonfigurasi (default tetap 8080/8443, tidak butuh).
- Tata letak berkas panel: `/etc/dbpanel/` dan `/var/lib/dbpanel/`
  menjadi 0711 root (hanya lintas, tidak bisa didaftar); di dalamnya
  `/etc/dbpanel/panel/` (ca.crt, ca.key, panel.key, mysql-ca.crt,
  mysql-ca.key, web-ca.crt, web-ca.key, web.crt, web.key,
  admin-initial-password) 0700 `dbpanel` dan `/var/lib/dbpanel/panel/`
  (panel.db, bin/ agent) 0700 `dbpanel`. Berkas agent tetap langsung di
  `/etc/dbpanel/` seperti server lain (agent.yaml, agent.crt/key,
  ca.crt, secrets.key, mysql.cred, role, source, promoted,
  agent-policy.yaml), 0600 root; panel.go tidak lagi memaksa 0700 pada
  induknya.
- Agent tertanam (`localagent.go`, `hub.NewLocalConn`) dihapus. Server
  panel menjadi baris `servers` biasa yang dipasangkan: pada start
  pertama atau upgrade, panel menulis token pasangan sekali pakai untuk
  baris server lokalnya ke `/etc/dbpanel/panel/local-pair-token` (0600
  `dbpanel`), dan `install.sh` (root) memindahkannya ke
  `/etc/dbpanel/local-pair-token` lalu memulai `dbpanel-agent`, yang
  memakainya untuk `pair` ke `https://127.0.0.1:<port web>/agent/pair`
  (pasangan dilayani listener web, dengan pin kunci 5.11) dan
  menghapusnya. `servers.address` server lokal tetap `PublicHost`
  (bukan 127.0.0.1): `LocalServerID` dan perlakuan khususnya di
  orchestrator dipertahankan, karena alamat itu menjadi `source_host`
  bila server panel menjadi master dan SAN sertifikat MySQL-nya.
  `ErrLocalServer` (server panel tidak bisa dihapus) tetap.
- Upgrade dari instalasi root: `install.sh` menghentikan panel, membuat
  user, memindahkan berkas ke tata letak baru, mengubah pemilik, menulis
  unit baru, memulai panel, menunggu panel menulis `local-pair-token`
  (panel menulisnya saat start setiap kali baris server lokalnya belum
  punya agent berpasangan), memindahkannya, lalu memulai agent lokal
  yang memakai pin dan kredensial yang sudah ada (`/etc/dbpanel/role`,
  `source`, `mysql.cred`, `secrets.key` tetap di tempatnya). Dijalankan
  ulang aman (idempoten).
  README bagian "Pembaruan" menjelaskan: perbarui panel dulu, lalu
  agent.
- Agent lokal berjalan sebelum panel siap menerima: `Restart=always`
  dan retry pasangan yang ada sudah cukup.

### 5.9 Sertifikat MySQL dari CA panel

- CA MySQL terpisah (`mysql-ca.crt/key`, dibuat panel saat start
  pertama atau upgrade, masa 10 tahun), bukan CA agent: CA agent
  menerbitkan sertifikat yang agent percayai sebagai identitas panel,
  jadi tidak boleh menandatangani sertifikat yang isinya dipengaruhi
  server lain. `ca.IssueMySQLCert(csr, serverID, hosts)` hanya menaruh
  SAN yang panel ketahui sendiri: `servers.address` dan `source_host`
  topologi; nama yang dilaporkan agent tidak pernah masuk sertifikat.
  Masa 2 tahun seperti leaf lain.
- Job `mysql_tls` per server (langkah dalam "Siapkan master" dan
  "Siapkan replika", serta tombol "Pasang sertifikat MySQL" di halaman
  server untuk server yang sudah ada): `mysql.tlsRequest` (agent membuat
  kunci dan CSR di `<datadir>/dbpanel-tls/`, berkas milik user `mysql`
  0600, folder 0700, agar terbaca `mysqld` di bawah AppArmor/SELinux),
  panel menandatangani, `mysql.tlsInstall{cert, ca}` menulis
  `server.crt`, `server.key`, `mysql-ca.crt` di folder itu, `SET
  PERSIST ssl_ca/ssl_cert/ssl_key`, `ALTER INSTANCE RELOAD TLS`,
  memverifikasi dengan sambungan baru `ssl-mode=VERIFY_IDENTITY`. Gagal
  memuat → kembalikan berkas lama, job gagal, tidak ada restart. `ALTER
  INSTANCE RELOAD TLS` butuh `CONNECTION_ADMIN`: masuk daftar hak wajib
  preflight akun admin; server lama yang kekurangan ditampilkan dengan
  perintah `GRANT` seperti hak lain yang kurang.
- Replika: bila master punya sertifikat panel (`server_tls`; selalu
  benar untuk topologi yang disiapkan setelah Rencana 4, karena "Siapkan
  master" memasangnya), `CHANGE REPLICATION SOURCE TO ... SOURCE_SSL=1,
  SOURCE_SSL_CA='<datadir>/dbpanel-tls/mysql-ca.crt',
  SOURCE_SSL_VERIFY_SERVER_CERT=1` (memverifikasi nama host sumber) dan
  `mysqldump`/CLONE memakai `--ssl-mode=VERIFY_IDENTITY` dan
  `clone_ssl_ca`; `replicas.tls_verified=1`. Bila master belum punya
  (topologi lama yang belum "Diperketat"), seperti hari ini:
  `SOURCE_SSL=1` tanpa verifikasi, `tls_verified=0`, dan peringatan
  "Sambungan replikasi belum diverifikasi" di halaman master. Akun
  replikasi tetap `REQUIRE SSL`.
- Replika yang sudah berjalan sebelum Rencana 4: tombol "Perketat
  sambungan replikasi" di halaman master menjalankan `mysql_tls` di
  master dan di tiap replika (tiap server mendapat sertifikatnya sendiri
  dan berkas `mysql-ca.crt`, yang dibutuhkan `SOURCE_SSL_CA` dan berguna
  saat replika itu kelak dipromote), lalu `CHANGE REPLICATION SOURCE`
  ulang di tiap replika (STOP/START singkat) dan menandai
  `tls_verified=1`, dengan token 5.7. Panel menampilkan peringatan
  "Sambungan replikasi belum diverifikasi" (bukan alert Telegram) pada
  topologi lama sampai dijalankan.
- Perpanjangan: agent meminta sertifikat baru 30 hari sebelum
  kedaluwarsa lewat perintah yang sama; panel memeriksa harian.

### 5.10 Dump tanpa RELOAD, `releaseRole`, kedaluwarsa akun dump

- Akun dump per job memperoleh hak dinamis `FLUSH_TABLES` sebagai ganti
  `RELOAD` pada seri MySQL yang mendukungnya (builder memverifikasi versi
  minimum pada 8.0 dan menulisnya di README; seri lebih tua tetap
  `RELOAD` dengan catatan di log job). `RESET BINARY LOGS AND GTIDS`
  tidak lagi bisa dari akun dump.
- Agent menyapu sendiri akun `dbpanel_dump_*` lebih tua dari 24 jam
  sekali per jam (monitor), bukan hanya saat panel meminta (SEC-42 butir
  2).
- `mysql.releaseRole` tidak lagi menghapus pin sumber (SEC-43 butir 1).
  Memakai ulang server untuk master lain butuh `sudo dbpanel agent
  set-source <host:port>` di server itu; pesan penolakan dan halaman
  "Buat replika" menampilkan perintah itu bila sumber yang dipin
  berbeda. Keputusan: keamanan menang atas kemudahan untuk kasus yang
  jarang.

### 5.11 HTTPS panel

- CA web terpisah: `web-ca.crt/key` di `/etc/dbpanel/panel/`, dibuat
  pada start pertama (atau upgrade), masa 10 tahun, dengan name
  constraints: subnet semua antarmuka saat dibuat (prefix antarmuka,
  jadi pergantian IP oleh DHCP di subnet yang sama tetap tercakup),
  `PublicHost`, nama host mesin, dan daftar tambahan di Pengaturan.
  Sertifikat web `web.crt` masa 2 tahun, SAN = nama dan IP yang
  terpakai, diterbitkan ulang otomatis 30 hari sebelum kedaluwarsa dan
  saat daftar nama berubah; proses memuat ulang tanpa restart. Nama
  baru di luar batasan CA → panel membuat CA web baru, menerbitkan ulang,
  dan menampilkan pemberitahuan tetap "Sertifikat keamanan panel
  diperbarui; komputer yang pernah memasangnya perlu memasang ulang"
  dengan tautan `/sertifikat`; tidak pernah diam-diam.
- Satu port (default 8080) melayani HTTPS dan HTTP: listener membaca
  byte pertama; TLS (0x16) → HTTPS; selain itu → HTTP yang hanya
  melayani `GET /sertifikat` (halaman "Pasang sertifikat keamanan
  panel": unduh `dbpanel-panel.crt`, langkah untuk Windows, macOS,
  Firefox, Android, iOS dengan tangkapan layar, kalimat Group Policy
  untuk admin domain) dan `GET /sertifikat/dbpanel-panel.crt`; semua
  jalur lain `301` ke `https://` alamat yang sama. Bookmark lama tetap
  sampai ke panel; perintah pasang agent yang ditampilkan sebelum
  pembaruan tidak lagi berlaku (token pasangannya pun kedaluwarsa) dan
  panel menampilkan perintah baru.
- Cookie sesi `Secure` selalu; HSTS 1 tahun hanya pada HTTPS; CSP yang
  ada tetap. `panel_base_url` bawaan menjadi `https://`; pengaturan lama
  `http://` ditampilkan dengan peringatan dan tautan "ubah ke https".
- "Lanjutan" di Pengaturan: unggah sertifikat dan kunci sendiri (PEM,
  rantai opsional) → disimpan 0600, divalidasi (kunci cocok, belum
  kedaluwarsa, SAN memuat alamat panel), dipakai sejak simpan; tombol
  "Kembali ke sertifikat panel". Kunci tidak pernah ditampilkan lagi.
- Pengujian HTTPS oleh panel sendiri saat start: bila sertifikat atau
  kunci rusak, panel kembali ke sertifikat yang dibuatnya sendiri dan
  menulis peringatan, tidak pernah jatuh ke HTTP polos.
- Antarmuka: istilah "sertifikat keamanan panel" saja. Tautan Telegram
  memakai `panel_base_url` https.
- Jalur agent di port web (`/agent/install`, `/agent/bin/*`, `POST
  /agent/pair`) ikut HTTPS, tidak dikecualikan: token pasangan dan
  sertifikat yang dikirim balik tidak boleh lewat HTTP polos. Perintah
  pasang yang ditampilkan panel menjadi `curl -fsSL -k --pinnedpubkey
  sha256//<hash SPKI web.crt> https://<panel>:<port>/agent/install | sudo
  bash -s -- <token> <hash>` (`-k` mematikan pemeriksaan rantai karena
  CA web tidak dikenal mesin itu; pin kuncilah yang menjadi
  pengamannya); skrip pasang dan `dbpanel agent pair` memakai pin yang
  sama untuk mengunduh binary dan memasang, lalu agent menyimpan CA
  agent dari balasan pasangan seperti sekarang. Pin mengikat ke kunci
  sertifikat yang sedang dilayani: panel mempertahankan kunci `web.crt`
  saat menerbitkan ulang, dan perintah pasang selalu dibuat dari kunci
  yang aktif saat ditampilkan; mengganti ke sertifikat sendiri atau
  kembali ke sertifikat panel membuat perintah yang ditampilkan sebelumnya
  tidak berlaku, dan halaman server menampilkan yang baru. Agent yang
  sudah berpasangan tidak menyentuh port web lagi; jalur mTLS 8443 tetap
  terpisah dan tidak berubah.

### 5.12 Penguatan audit murah

- **Status pin** di `HealthSnapshot`: `role_pin`, `source_pin`,
  `pin_error`. Halaman server menampilkan "Peran dipin: master" di Detail
  teknis. Peringatan baru `master_unpinned` ("Master tidak dipin",
  Peringatan): master topologi yang melapor `role_pin` bukan `master`
  selama > 2 laporan; saran: jalankan ulang "Siapkan master". Pin dibaca
  dengan `O_NOFOLLOW` dan harus berkas biasa milik root (seperti
  `agent-policy.yaml`); pembacaan aman ini sudah ada (`readPinFile`),
  yang baru hanya pelaporannya.
- **Batas laju laporan**: per server, ember 10 laporan, isi ulang 1 per
  2 detik; laporan lebih dibuang dengan penghitung yang masuk log dan
  Detail teknis ("12 laporan dibuang dalam 1 jam terakhir"), tidak
  pernah menggagalkan sambungan. Pesan agent dibatasi 1 MiB (SEC-34).
- **Retensi**: peringatan tertutup lebih tua dari 90 hari dan
  `status_events` lebih tua dari 180 hari dipangkas harian; `audit_log`
  dan `job_log` tidak pernah dipangkas. Nilai di `settings`
  (`alerts_retention_days`, `events_retention_days`), ditampilkan di
  Pengaturan.
- **Hash chain audit**: kolom `prev_hash`, `hash` (SHA-256 atas
  `prev_hash` dan kolom lain dalam bentuk kanonik), dihitung saat sisip
  dalam transaksi yang sama dengan `SELECT ... ORDER BY id DESC LIMIT 1`;
  migrasi menghitung rantai untuk baris lama; CLI `dbpanel panel
  audit-verify` dan baris status di `/audit` ("Rantai utuh sampai #4521"
  atau "Rantai putus di #3100"). Sisipan juga ditulis ke journald lewat
  logger (salinan kedua, SEC-45).
- **Sensor kutipan data saat ingest**: string yang datang dari agent
  (error MySQL, `last_io_error`, `last_sql_error`, pesan job dari
  perintah MySQL) disaring sebelum disimpan atau dikirim: literal
  berkutip tunggal dan ganda diganti `'…'`, angka panjang dipertahankan.
  Berlaku untuk `server_health`, `status_events`, `alerts`, `job_log`,
  dan Telegram.
- **Tiga minor terakhir Rencana 3**: (1) saat agent tersambung lagi,
  status `Agent offline` langsung diganti "Menunggu laporan pertama"
  sampai laporan segar masuk; (2) petunjuk `DROP USER` menyebut host
  baris yang benar-benar ada; (3) `createReplUser` diserialkan per
  driver sehingga dua permintaan serentak tidak mengulang `ALTER`.

### 5.13 Model data (migrasi 0006)

- `servers.role` menerima `quarantined`; kolom baru `pin_role TEXT`,
  `pin_source TEXT`, `pin_error TEXT` (laporan terakhir).
- `replicas.state` menerima `needs_repoint`, `needs_rebuild`; kolom baru
  `tls_verified INTEGER NOT NULL DEFAULT 0` (sambungan replikasi
  memverifikasi sertifikat master).
- `topologies`: `cutover_pending INTEGER NOT NULL DEFAULT 0`,
  `old_master_server_id INTEGER NOT NULL DEFAULT 0`. Apakah master
  punya sertifikat panel dibaca dari `server_tls`, tidak disimpan dua
  kali.
- `promotions(id, topology_id, mode, old_master_server_id,
  new_master_server_id, started_at, finished_at, cutover_confirmed_at,
  lock_at, last_master_gtid, new_master_gtid, replicas_json,
  leftover_json, status, job_id)`. Dipakai layar pengalihan, halaman
  master lama, riwayat di halaman topologi, dan audit.
- `confirmations(token_hash PRIMARY KEY, username, action, target_kind,
  target_id, expires_at, used_at)`; baris kedaluwarsa dipangkas harian.
- `audit_log`: `prev_hash TEXT NOT NULL DEFAULT ''`, `hash TEXT NOT NULL
  DEFAULT ''`. Migrasi, dalam satu transaksi: `ALTER TABLE`, `DROP
  TRIGGER` kedua trigger append-only, isi rantai untuk baris lama
  berurutan `id`, buat ulang kedua trigger. Di luar migrasi ini tidak
  ada kode yang menyentuh trigger.
- `settings`: `https_mode` (`panel`|`custom`), `https_extra_names`,
  `alerts_retention_days`, `events_retention_days`.
- `server_tls(server_id PRIMARY KEY, cert_serial, not_after, installed_at)`.

### 5.14 Peringatan dan Telegram baru

| Jenis | Tingkat | Buka | Tutup |
|---|---|---|---|
| `promote_cutover` | Peringatan | promote selesai, `cutover_pending` | "Sudah saya alihkan" |
| `promote_partial` | Kritis | job promote selesai sebagian | semua langkah tersisa selesai |
| `replica_needs_repoint` | Peringatan | `needs_repoint` | `running` |
| `replica_needs_rebuild` | Kritis | `needs_rebuild` | resync selesai |
| `master_quarantined` | Info (sekali) | karantina masuk | 24 jam |
| `master_unpinned` | Peringatan | 5.12 | pin master dilaporkan |
| `master_lock_expired` | Peringatan | penjaga `lockForPromote` membuka/mengkarantina sendiri | 24 jam |
| `tls_expiring` | Peringatan | sertifikat MySQL/web < 14 hari | diperpanjang |

Semua mengikuti pola Rencana 3 (hysteresis, pengulangan 6 jam, pulih
≥ 1 menit). Pesan promote menyebut alamat baru dan tautan layar
pengalihan.

### 5.15 Antarmuka

- Halaman master: tombol "Promote…" (selalu ada; teks di bawahnya
  menyebut mode yang akan dipakai), kotak "Perketat sambungan
  replikasi" bila master belum punya baris `server_tls` atau ada replika
  dengan `tls_verified=0`.
- `GET /masters/{id}/promote` (5.2), `POST /masters/{id}/promote`
  (replika terpilih, token), `GET /masters/{id}/cutover` dan `POST
  /masters/{id}/cutover/done` (5.5), fragmen koneksi `GET
  /servers/{id}/connections-fragment`.
- Halaman server `quarantined` (5.4): `POST /servers/{id}/leftover`,
  `POST /servers/{id}/salvage`, `GET /servers/{id}/salvage/{name}`,
  `POST /servers/{id}/salvage/{name}/delete`, tombol bangun ulang dan
  hapus.
- `POST /confirm` dan komponen modal (5.7) dipakai semua tombol
  destruktif yang ada; tombol lama yang hanya mengetik nama server
  sekarang juga meminta kata sandi.
- `GET /sertifikat` (HTTP dan HTTPS), Pengaturan: bagian "Sertifikat
  keamanan panel" (status, nama yang tercakup, unduh, Lanjutan: unggah
  sendiri) dan "Retensi".
- `/audit`: baris status rantai; filter aksi baru.
- Dashboard: banner `cutover_pending` dan `promote_partial`; kartu
  server `quarantined` berlabel "Master lama".
- Job promote tampil di `/jobs` seperti job lain, dengan log yang bisa
  dibaca awam ("Mengunci master… Replika A mengejar: tersisa 2
  transaksi… Replika A kini master… Mengarahkan replika B…").

### 5.16 Pengujian

- Unit: pins (peran `quarantined`, tabel penolakan, `promoted`), verifier
  promote/repoint/quarantine dengan MySQL palsu, penjaga
  `lockForPromote` dengan jam palsu, token konfirmasi (umur, sekali
  pakai, pengikatan sasaran, batas laju), hash chain (migrasi,
  verifikasi, deteksi ubah), sensor kutipan, batas laju laporan, name
  constraints CA web, listener satu port (TLS vs HTTP), validasi unggah
  sertifikat, salvage path (nama ketat, folder tetap).
- MySQL nyata (paket `mysql`, ~menit): `lockForPromote` + `promote
  planned` + `quarantine` + `repoint` dengan tiga mysqld; `promote
  emergency` dengan master dimatikan; `binlogTail` dengan mysqld mati;
  `leftover` dan `salvage` dengan transaksi yang sengaja tertinggal;
  `mysql_tls` termasuk `RELOAD TLS` dan replika `VERIFY_SERVER_CERT`;
  dump dengan `FLUSH_TABLES`.
- E2E: `TestE2EPromotePlanned` (kunci, kejar, tukar, arahkan replika
  ketiga, layar pengalihan, "Sudah saya alihkan", bangun ulang master
  lama); `TestE2EPromoteEmergency` (bunuh master, layar pertimbangan
  dengan `binlogTail`, promote, hidupkan master lama → karantina →
  periksa menemukan transaksi tertinggal → amankan → unduh lewat HTTPS
  dengan token → bangun ulang); `TestE2EHTTPS` (sertifikat otomatis,
  halaman `/sertifikat` lewat HTTP, pengalihan, cookie Secure);
  `install_test.sh` dengan user `dbpanel` dan agent lokal berpasangan,
  termasuk upgrade dari tata letak root.

## 6. Diserahkan ke builder

- Urutan kerja yang disarankan: 5.7 token, 5.8 non-root, 5.11 HTTPS,
  5.9 TLS MySQL, 5.10, lalu 5.1–5.6 promote, lalu 5.12 penguatan.
  Prasyarat dulu agar promote lahir di atas pagar yang sudah ada.
- Nama fungsi, paket, dan bentuk parameter perintah; pembagian langkah
  job; teks awam tiap pesan (mengikuti nada Rencana 3).
- Cara membaca binlog tanpa MySQL hidup (`mysqlbinlog` atas berkas yang
  dicatat `binlog.index`), dan batas waktu/ukuran keluarannya.
- Versi minimum `FLUSH_TABLES` dan perilaku bila tidak ada.
- Rincian `install.sh` upgrade idempoten dan pasangan agent lokal.
- Tampilan tangkapan layar halaman `/sertifikat` per sistem operasi.
- Bentuk kanonik baris audit untuk hash.

## 7. Keputusan pemilik produk (brainstorming 2026-11-05)

1. Lingkup: promote terencana + darurat + layar pengalihan; prasyarat
   keamanan yang dipakai promote; penguatan audit murah. Checksum dan
   SEC-49 ke Rencana 5. HTTPS panel ditarik masuk Rencana 4.
2. Pemeriksaan sebelum promote (layar pertimbangan) sebagai bahan
   keputusan user; pembacaan binlog master lama saat MySQL mati masuk
   Rencana 4.
3. "Periksa data tertinggal" dan "Amankan data tertinggal" (ekstrak dari
   binlog, bukan dump penuh), panel tidak pernah menerapkannya.
4. Tim perlu bisa mengunduh berkas data tertinggal lewat panel; karena
   itu HTTPS wajib dulu.
5. HTTPS harus bekerja di jaringan lokal tanpa domain dan tanpa
   pengetahuan CA/SSL dari tim: otomatis sejak pasang, halaman pasang
   sertifikat untuk menghilangkan peringatan, istilah awam.
6. Layar pengalihan menampilkan koneksi aplikasi yang masih ke master
   lama.
7. Jeda tulis selama pengalihan manual diterima; kebanyakan pengguna
   mengubah alamat di aplikasi secara manual.
8. Promote boleh berjalan walau ada replika tidak terjangkau; user
   diberi tahu dan memutuskan.
9. Pendekatan A untuk pergantian peran dan B untuk perintah destruktif
   lain, dengan syarat kendali di user dan user diberi tahu semua
   kondisi.
10. Pemilik produk meminta agar permintaan yang tidak mungkin atau
    berisiko (keamanan, kebocoran, kehilangan data) disampaikan, bukan
    diterima begitu saja.
11. (Builder check) Replika sebagian database boleh dipromote; panel
    menyebut database yang tidak akan ada di master baru dan
    mengutamakan replika lengkap.
12. (Builder check) Master yang terputus jaringan tetapi hidup tidak
    mengunci dirinya sendiri; risiko ditulis dan ditemukan oleh
    "Periksa data tertinggal".

## 8. Risiko residual yang disadari

- Panel yang dibobol tetap bisa, dengan menyebut replika yang sedang
  tersambung sebagai kandidat, mengunci master dan memicu promote
  terencana yang tidak diinginkan: data utuh, aplikasi kehilangan
  tulisan sampai dialihkan; semua tercatat audit dan Telegram. Bila
  kandidat lalu dibuat tidak terjangkau, kunci bertahan sampai operator
  menjalankan `dbpanel agent unlock-master` di server master (pilihan
  sadar: membuka otomatis berisiko dua master). Mode ketat (4.C) di
  Rencana 5 menutupnya.
- Pin `replica` kini menerima `createReplUser` ketat, sehingga panel
  yang dibobol bisa membuat akun replikasi di replika sebagian; replika
  lengkap sudah memilikinya sejak dump, jadi paparan tidak bertambah
  jenisnya (akun itu bisa membaca binlog dan menjadi donor CLONE
  replika, bukan master).
- `repoint` memverifikasi master baru hanya lewat keadaan MySQL-nya;
  server palsu dengan `gtid_purged` yang disetel bisa lolos bila
  penyerang menguasai panel **dan** punya server MySQL di jaringan
  **dan** sumber lama terkunci atau mati. Dampak terbatas pada replika
  yang diarahkan; master dan data aslinya tidak tersentuh.
- Jendela karantina darurat (5.3): detik antara MySQL master lama hidup
  dan agent mengunci; atau, pada jaringan terbelah, selama master lama
  hidup di sisi lain dan aplikasi di sana menulis (keputusan 7.12).
  Keduanya ditemukan oleh "Periksa data tertinggal".
- Promote ke replika sebagian meninggalkan database lain hanya di master
  lama (baca-saja). Panel menyebutkannya sebelum promote; data tidak
  hilang selama master lama tidak dibangun ulang atau dihapus.
- Berkas salvage berisi data asli di disk master lama selama 7 hari,
  0600 root. Dihapus otomatis; bisa dihapus lebih awal dari halaman.
- Tim yang tidak memasang sertifikat tetap melihat peringatan browser;
  lalu lintas tetap terenkripsi, tetapi penyamaran di jaringan lokal
  tidak terdeteksi oleh mereka.

## 9. Keputusan dari builder check (ditetapkan penulis dokumen)

Pertanyaan builder yang bisa diputuskan tanpa mengubah apa yang
dibangun, jawabannya sudah dimasukkan ke bagian terkait:

1. Urutan promote darurat menunggu relay log yang sudah diterima
   diterapkan sebelum `RESET REPLICA ALL` (5.1).
2. Pemeriksaan lintas server hanya membaca variabel global; hak akun
   replikasi tidak berubah (3, 5.1).
3. Pencocokan kandidat `lockForPromote` lewat IP thread `Binlog Dump`
   (5.1).
4. CA MySQL terpisah, SAN hanya dari alamat yang panel ketahui,
   `VERIFY_IDENTITY`, berkas di `<datadir>/dbpanel-tls/`,
   `CONNECTION_ADMIN` di preflight (5.9).
5. Name constraints CA web per subnet antarmuka; nama di luar batasan
   membuat CA baru dengan pemberitahuan (5.11).
6. Jalur pasang agent tetap di port web, lewat HTTPS dengan pin kunci
   di perintah pasang (5.11, 5.8).
7. Folder induk 0711, berkas panel di subfolder milik `dbpanel`;
   alamat server lokal tetap `PublicHost` (5.8).
8. Bangun ulang master lama menunggu "Sudah saya alihkan" dan
   menampilkan koneksi tersisa (5.4).
9. Migrasi hash chain melepas dan memasang kembali trigger append-only
   dalam satu transaksi (5.13).
10. (Pemeriksaan konsistensi) Tenggat `lockForPromote` dari job = 30
    menit, lebih panjang dari jumlah tenggat kejar dan terapan relay
    log, dan `promote{planned}` memverifikasi ulang kunci master tepat
    sebelum menukar peran (5.1).
11. (Pemeriksaan konsistensi) `repoint` yang menolak `needs_rebuild`
    tetap memindahkan pin sumber agar resync dari master baru tidak
    ditolak pin; verifikasi sertifikat pada `repoint` mengikuti ada
    tidaknya sertifikat panel di master baru (5.1).
12. (Pemeriksaan konsistensi) `quarantine` saat MySQL master lama mati
    menulis pin dulu dan menerapkan kunci saat MySQL kembali (5.1).
13. (Pemeriksaan konsistensi) Kandidat `lockForPromote` harus sedang
    tersambung; kunci dicatat di berkas dan dipasang lagi saat MySQL
    restart; selama berkas ada, "Buka kunci master" ditolak; kandidat
    yang hilang membuat master tetap terkunci sampai `dbpanel agent
    unlock-master`; penjaga memberi tenggang 60 detik sebelum membuka;
    kunci memakai PERSIST agar restart tidak membuka tulis (5.1, 8).
14. (Pemeriksaan konsistensi) Titik tanpa balik promote adalah membuka
    tulis, yang dilakukan sebelum `RESET REPLICA ALL`, sehingga penjaga
    master hanya bisa mengkarantina sesudahnya; "Coba lagi" melanjutkan
    tanpa memeriksa master bila tulis sudah terbuka (5.1, 5.3).
15. (Pemeriksaan konsistensi) Pin `replica` menerima `createReplUser`
    ketat agar replika sebagian bisa menjadi kandidat (5.1, 8).
16. (Pemeriksaan konsistensi) `replicas-seen` disimpan 1 tahun untuk
    `quarantine`; `repoint` membawa berkas CA ke replika; verifikasi
    sertifikat pada "Siapkan replika" dan `repoint` mengikuti ada
    tidaknya sertifikat master, dicatat per replika di `tls_verified`;
    karantina darurat dikirim segera bila agent master lama tersambung; "Perketat" memasang sertifikat di semua server
    topologi; perintah pasang agent memakai `-k` + pin dan dibuat dari
    kunci aktif; upgrade memulai panel dulu lalu agent lokal; hapus
    berkas salvage dan hapus master lama diatur (5.1, 5.4, 5.8, 5.9,
    5.11); CLI lokal `lock-master`/`unlock-master` (5.1).
