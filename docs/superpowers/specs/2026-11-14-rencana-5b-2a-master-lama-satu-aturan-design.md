# Rencana 5b-2a: Master Lama dengan Satu Aturan

- Tanggal: 2026-11-14
- Status: disetujui pemilik produk 2026-11-16; diubah sesudah tinjauan
  rancangan dan pengukuran putaran kedua (bagian 10). Yang masih menunggu
  pemilik: perubahan pada bagian 5 dan 7 yang disebut di 7.12–7.19, dan
  jalan balik ke root yang dipersempit saat pelaksanaan (5.5). Opsi
  `--tanpa-cadangan` di bagian 7 disetujui pemilik 2026-11-17.
- Asal: daftar yang ditunda dari Rencana 5b-1 (rencana 5b-1, "Perubahan
  selama pelaksanaan" butir 18) dan tabel risiko terbuka audit Rencana
  5b-1 (`docs/superpowers/security/2026-11-14-audit-plan-5b1.md`, bagian
  1; disebut "A5b1" di bawah).
- Yang **tidak** ada di sini: gerbang prasyarat, cadangan, promote darurat,
  panduan langkah, dan ganti nama "Perketat" (spek Rencana 5b bagian
  5.7–5.11). Itu menjadi Rencana 5b-2b, ditulis sesudah 5b-2a selesai.

## 1. Maksud dan alasan

Rencana 5b-1 mengunci master lama yang dikarantina dengan benar, tetapi
dua bagiannya tidak pernah tenang di bawah pemeriksaan: (1) keadaan master
lama yang bangun ulangnya gagal, yang ditambal tiga kali; (2)
langkah-dengan-tangan di README, yang menyuruh operator menyalin blok SQL
di server produksi dan pada setiap putaran pemeriksaan masih menyimpan cara
baru untuk salah. Selain itu ada risiko yang dicatat dan belum ditutup:
master lama yang menjadi bisa ditulis selagi MySQL hidup tidak dikunci lagi
dan tidak diperingatkan; tulisan aplikasi selama penyalinan tidak
terdeteksi; root menjalankan program atas berkas milik akun layanan MySQL;
pesan agent tampil tanpa label di halaman pekerjaan.

Rencana ini mengganti tambalan dengan aturan. Setiap keadaan master lama
punya satu arti di panel dan di agent, dan pekerjaan berbahaya di server
dilakukan oleh satu perintah yang memeriksa dirinya sendiri, bukan oleh
operator yang menyalin SQL.

## 2. Tujuan, bukan-tujuan, anti-tujuan

**Tujuan**

1. Sesudah bangun ulang master lama gagal, panel mencatat server itu
   dikarantina, tanpa klik apa pun. Hapus, Periksa dan Amankan data
   tertinggal, Bangun ulang, dan "Ulangi dari tahap ini" semuanya bekerja
   dari keadaan itu. Monitor menilainya sebagai master lama.
2. Master lama yang dikarantina tidak bisa tinggal bisa-ditulis lebih dari
   satu putaran penjaga (5 detik) tanpa dikunci lagi **atau dilaporkan
   sebagai tidak bisa dikunci**, dan setiap kejadian itu sampai ke panel
   sebagai peringatan yang tidak bisa hilang.
3. Selama penyalinan bangun ulang, akun aplikasi biasa tidak bisa menulis
   ke master lama; dan karantina hanya berakhir bila setiap database di
   server itu memang dibuat oleh penyalinan itu.
4. Tidak ada lagi blok SQL yang harus disalin operator ke server yang
   dikarantina. Tiga perintah `dbpanel agent` menggantikannya.
5. Pesan dari agent di halaman pekerjaan selalu berlabel dan satu baris.
6. Root tidak lagi menjalankan `mysqlbinlog` atau membaca log atas berkas
   yang dikendalikan akun layanan MySQL.
7. Risiko kecil yang tercatat (A5b1 SEC-201, 211, 226, 227, 228, 229, 232,
   233, 234) ditutup.

**Bukan-tujuan**

- Gerbang prasyarat dan isi spek 5.7–5.11 (Rencana 5b-2b).
- Akun replikasi per replika (Rencana 5c), yang menutup akar R-110.
- Panel dua bahasa.
- Mengubah prosedur "Melepas kunci start dengan tangan": prosedur itu untuk
  agent yang rusak, jadi tidak bisa diganti perintah agent. Hanya
  ketidaktepatan kecilnya yang dibetulkan (SEC-233).

**Anti-tujuan** (kalau ini terjadi, rencana gagal walau test lulus)

- Penjaga mengunci server di tengah pekerjaan agent sendiri (muat data,
  buang database, buat akun) sehingga bangun ulang yang sah gagal.
- Peringatan Kritis muncul setiap kali operator menjalankan perintah yang
  sah.
- Aturan baru menolak penyelamatan data yang masih ada di disk, atau
  berkata "data sudah dihapus" tanpa bukti.
- Operator berakhir di keadaan tanpa langkah berikutnya.

## 3. Fakta yang menentukan desain

Semua dibaca dari kode pada `8ce7fc5`. Butir bertanda "(tinjauan)"
ditambahkan atau dibetulkan oleh tinjauan rancangan (bagian 10).

**Panel**

- F1. `fail()` dan `Resume` tidak mengubah peran: sesudah bangun ulang
  gagal, peran tetap `replica` dengan baris replika gagal dan tanda master
  lama di topologi. Orchestrator sendiri berkomentar "A failed rebuild of
  an old master leaves it quarantined" dan `cancelJob` memang
  mengembalikan peran ke `quarantined`; hanya jalur gagal yang tidak.
  (tinjauan) Peran `replica` itu dipasang `AddReplica` saat bangun ulang
  dimulai; `RemoveReplica` memasang `unassigned` dan pemanggilnya
  memasang `quarantined` lagi dengan tulisan kedua. `failUnrunnable`
  menggagalkan job tanpa menyentuh baris; `Resume` menulis status job
  lebih dulu, baris sesudahnya.
- F2. Bendera `QuarantineEnded` pada job sudah ada dan disimpan tepat
  sesudah `mysql.configureReplication` berhasil; tanda master lama dihapus
  saat itu, dengan tulisan store yang terpisah. Tiga keadaan salinan
  (`CopyNotStarted`, `CopyUncertain`, `CopyStarted`) sudah ada.
- F3. Yang ada **hanya** karena aturan ini belum dibangun: lengan kedua
  `AwaitingRebuild` (peran `replica` + baris gagal + tanda), tindakan
  "Batalkan percobaan bangun ulang" (`ReturnToQuarantine`,
  `CancelRebuildAttempt`, rutenya, tombol dan tiga kalimatnya), kalimat
  "percobaan masih tercatat".
- F4. Monitor membaca baris replika sebelum peran. Server berperan
  `quarantined` yang punya baris dinilai sebagai replika gagal; aturan
  master lama tidak pernah sampai kepadanya.
- F5. Panel tidak pernah membaca `read_only`/`super_read_only` dari laporan
  server yang dikarantina, dan tidak membandingkan pin peran agent
  (`RolePin`) dengan peran di panel kecuali untuk master. (tinjauan)
  Status server yang dikarantina dihitung dari store saja, jadi laporan
  `mysql_ok=false` tidak membuka apa pun.
- F6. `SwapMaster` menimpa tanda master lama topologi. Sesudah promote
  kedua, master lama yang pertama kehilangan catatannya: Periksa data
  tertinggal menolak ("bukan lagi master lama sebuah topologi") dan kotak
  tampil tanpa tombol bangun ulang.
- F7. Teks galat agent masuk ke pekerjaan sebagai
  `judul langkah + ": " + teks`, lalu disensor. Pelabel `AgentQuote` sudah
  ada tetapi hanya dipakai di jalur persetujuan, dan memotong di 300
  karakter (teks saran agent lebih panjang dari itu). (tinjauan) Hub
  meneruskan galat agent apa adanya (`readResponse` mengembalikan
  `resp.Error`), dengan kode yang dipilih agent; galat buatan hub sendiri
  (batas waktu, agent terputus) bertipe sama, dan yang terakhir sampai ke
  pemanggil yang sedang menunggu lewat `readResponse` juga
  (`WSConn.stop`). Asal sebuah galat tidak bisa dibedakan dari kodenya.
- F16 (tinjauan). Monitor tidak pernah memanggil orchestrator; arah yang
  ada adalah orchestrator (memegang `startMu`) memanggil monitor (yang
  mengambil kuncinya sendiri). Struct konfigurasi monitor bernama `Deps`.
- F17 (tinjauan). Dialog "Ulangi dari tahap ini" hanya menampilkan
  kelompok database yang dihapus dan koneksi; peringatan data tertinggal
  hanya ada di form Bangun ulang (`rebuildTarget`).

**Agent**

- F8. Penjaga berputar tiap 5 detik tetapi membaca bendera baca-saja
  **hanya pada peralihan** (MySQL baru menyala, agent baru mulai, muat
  selesai). Selama MySQL hidup terus, bendera tidak pernah dibaca.
  (tinjauan) Pada peralihan penjaga menjalankan `persistReadOnly` apa pun
  yang dibacanya, dan melaporkan start `late` bila ada bendera mati; yang
  menahannya selama muat hanya tanda muat. Bila penjaga tidak bisa masuk
  ke MySQL (`h.Open` gagal, termasuk login ditolak) ia pulang tanpa
  mencatat apa pun; galat `persistReadOnly` hanya ditulis ke log.
- F9. Agent sendiri mematikan baca-saja di lima tempat (`liftReadOnly`):
  muat dump, CLONE, pasang plugin clone, buang database, buat akun. Hanya
  dua yang pertama meninggalkan tanda; tandanya dipasang **sesudah**
  bendera mati. (tinjauan) Jalur CLONE tidak pernah memanggil `restore`
  sesudah fase datanya: restart MySQL yang memasang bendera lagi, lalu
  `cloneFrom` menjalankan `persistReadOnly` sendiri.
- F10. `liftReadOnly` mematikan `super_read_only` **dan** `read_only`.
  Alasan tertulisnya ("akun admin tidak butuh CONNECTION_ADMIN") sudah
  usang: `CONNECTION_ADMIN` kini hak wajib akun admin, dan akun dengan hak
  itu boleh menulis saat `read_only` menyala. Tidak ada test yang
  mensyaratkan `read_only` mati.
- F11. Selama bangun ulang agent tidak punya sambungan SQL sendiri ke
  master baru; ia menjangkaunya hanya lewat program klien (`mysqldump`,
  dan `mysql` untuk pemeriksaan awal) dengan berkas kredensial sementara
  dan opsi TLS yang sama. Aliran dump tidak bisa dipakai sebagai daftar
  database yang tepercaya: isi routine, trigger dan event ditulis apa
  adanya, dengan baris baru sungguhan, jadi siapa pun yang boleh membuat
  routine di master baru bisa menaruh baris yang tampak seperti
  `CREATE DATABASE`.
- F12. `mysqlbinlog` dijalankan sebagai root di tiga tempat atas berkas di
  direktori binlog. Ekor log galat dibaca root di dalam proses. Mekanisme
  "jalankan sebagai uid akun layanan" (`ExecEnvAs` dengan kredensial)
  sudah ada, baru dipakai untuk `--print-defaults`. (tinjauan) Galat
  `ExecEnvAs` meratakan "program tidak bisa dimulai" dan "program keluar
  dengan kode bukan nol" menjadi satu teks; keduanya belum bisa dibedakan
  pemanggil.
- F13. Berkas `leftover-gtid` tidak mencatat sumbernya dan tidak pernah
  dihapus.
- F14. CLI agent (`approve`, `set-role`, `set-source`) tidak pernah masuk
  ke MySQL. `lock-master`/`unlock-master` masuk dengan akun admin yang
  tersimpan. Jalur masuk sebagai root MySQL lewat socket sudah ada
  (`openWith(socket, root)`, dipakai pemasangan).
- F15. Bukti daftar lengkap dan daftar database dibaca pada satu
  sambungan, berurutan. `GRANT`/`REVOKE` ditolak MySQL selama
  `super_read_only` menyala, juga untuk pemegang SUPER.
- F18 (tinjauan). `recordStart` menilai setiap hasil lewat `judgeKinds`,
  yang untuk hasil `late` tanpa tanda "dilewati" menandai entri
  `bypassed` yang tersimpan sebagai selesai. Catatan tidak punya field id:
  id tiap entri diturunkan dari waktu keputusannya (`startID`).
- F19 (tinjauan). `/var/lib/dbpanel` milik root dengan mode 0711 di host
  panel dan dibuat 0755 oleh agent sendiri; berkas pin ditulis 0644.
  `flock` cukup dengan deskriptor baca-saja, jadi berkas kunci yang
  mengikuti kebiasaan itu bisa dipegang pengguna lokal mana pun.
- F20 (tinjauan). `notCopied` untuk dump bernama menamai setiap database
  lain di server, ada di master baru atau tidak. Bangun ulang master lama
  yang masih menyimpan database yang tidak ada di master baru gagal lebih
  dulu di "Periksa server tujuan" (pemeriksaan "dedicated"), sebelum ada
  catatan muat apa pun; teks pemeriksaan itu tidak menunjuk langkah
  berikutnya.
- F21 (tinjauan). `mysql.connect` di server yang dikarantina menolak akun
  yang kurang hak **sebelum** menyimpannya; akun yang tersimpan tetap akun
  lama. Pemeriksaan "grants" pada bangun ulang memakai kumpulan hak
  replika (`replicaGrants`: hak `mysql.connect` ditambah hak data), bukan
  kumpulan hak `mysql.connect`, dan hasilnya membawa pernyataan `GRANT`
  (`FixSQL`) yang ditolak server yang dikarantina.
- F22 (tinjauan). Muat dump penuh mengganti akun MySQL server itu dengan
  akun master baru; login akun tersimpan agent lalu ditolak sampai
  operator menjalankan "Hubungkan MySQL" lagi (`AdminReplaced`). Ini jalur
  biasa bangun ulang, bukan kejadian langka.

**Yang diukur di mysqld sungguhan**

Pengukuran pertama (M1–M4) sudah selesai pada MySQL 8.0.46; tabelnya ada
di rencana, bagian "Bukti pengukuran". Hasil yang menentukan desain:

- M1. Dengan `read_only=ON`, `super_read_only=OFF`, muat dump penuh
  (termasuk `SET @@GLOBAL.GTID_PURGED` dan `FLUSH PRIVILEGES`), `DROP
  DATABASE`, `CREATE USER`/`GRANT`, `INSTALL PLUGIN` **dan CLONE**
  berhasil untuk akun ber-`CONNECTION_ADMIN` tanpa SUPER. Tidak ada
  pemakai `liftReadOnly` yang butuh `read_only` mati. `SET GLOBAL
  read_only = OFF` selagi `super_read_only` menyala mematikan **keduanya**
  tanpa galat; `SET GLOBAL super_read_only = ON` ikut menyalakan
  `read_only`. Jadi pembukaan yang sah hanya boleh mengirim `SET GLOBAL
  super_read_only = OFF`. (5.2, 5.3)
- M2. Akun dump buatan panel melihat semua database master baru; keluaran
  klien `mysql -N -B` untuk `HEX(schema_name)` adalah satu nama heksa
  huruf besar per baris. (5.3)
- M3. `mysqlbinlog` dan pembacaan indeks binlog berhasil sebagai uid
  pemilik direktori data dan ditolak untuk uid lain; Ubuntu tidak
  mengurung `mysqlbinlog` dengan AppArmor; RHEL hanya dibaca dari sumber
  kebijakan. (5.5)
- M4. Sesi bukti daftar lengkap menyetel `SET SESSION
  sql_quote_show_create = ON, sql_mode = ''` sebelum `SHOW GRANTS`. (5.7)

Pengukuran putaran kedua (rencana, Task 2b; selesai pada MySQL 8.0.46,
buktinya di `docs/superpowers/ledgers/2026-11-14-05b2a-measurements-2.md`)
mengukur yang diminta tinjauan rancangan. X1 (muat buatan agent sendiri di
bawah `read_only=ON`) sudah diukur putaran pertama (M1). Hasil yang
menentukan desain:

- X2. Akun muat tanpa `CONNECTION_ADMIN` dan tanpa SUPER gagal dengan
  ERROR 1290 pada pernyataan tulis pertama dump, **sesudah** `RESET
  MASTER` dan sesudah `SET @@GLOBAL.GTID_PURGED`: riwayat GTID lokal
  hilang dan `gtid_executed` mengaku memuat set sumber tanpa datanya.
  Tidak ada pemeriksaan yang ada yang menangkapnya lebih awal. SUPER
  sebagai pengganti `CONNECTION_ADMIN` cukup. (5.3)
- X4. `SET PERSIST read_only = ON` (dan `super_read_only`) menunggu selama
  sesi lain memegang `LOCK TABLES … WRITE` atau menjalankan pernyataan
  tulis, juga sesi berhak seperti klien yang sedang memuat dump; semua
  penulis lain antre di belakangnya. Pernyataan itu **tidak** menunggu
  transaksi yang sekadar terbuka; `COMMIT` transaksi itu lalu gagal 1290.
  Batas waktu di sisi klien (konteks 30 detik penjaga) **tidak**
  menghentikan pernyataan di server: ia tetap menunggu dan berlaku
  belakangan, bisa setengah (`read_only` saja). Batas yang bersih adalah
  `SET SESSION lock_wait_timeout = <n>` pada sambungan yang sama: galat
  1205 sesudah n detik, bendera tidak berubah. (5.2)
- X5. `mysqldump` 8.0.46 tidak bisa men-dump database yang namanya memuat
  `'` atau `"` (keluar 2). Dump yang terpotong karena disk penuh (keluar
  5) tetap memuat baris `CREATE TABLE` sebanyak tabel dasarnya, jadi
  bukti kelengkapan **hanya** kode keluar 0 dan baris terakhir `-- Dump
  completed on …`. View, partisi dan kolom turunan tidak mengubah jumlah
  baris `CREATE TABLE`; isi routine dan string literal yang memuat baris
  berawalan `DELIMITER` bisa menambahnya, jadi "paling sedikit" aman dan
  "sama dengan" tidak. (5.4)
- X6. CLONE tidak menyentuh `mysqld-auto.cnf` penerima: sesudah restart,
  penerima menyala dengan nilai tersimpannya **sendiri** (ON/ON di server
  yang dikarantina). (5.2)
- X7. Sesudah muat dump penuh yang `FLUSH PRIVILEGES`-nya mengganti akun,
  sambungan yang disimpan sejak sebelum muat ditolak 1227 pada `SET GLOBAL
  super_read_only = ON` (hak dinamis diperiksa ulang) dan sambungan baru
  ditolak 1045: **tidak ada sambungan agent yang bisa memasang bendera
  lagi.** Yang terbukti berhasil: pemulihan dikirim di aliran muat itu
  sendiri, sebelum `FLUSH PRIVILEGES`; muat keluar 0 dan bendera berakhir
  ON/ON. Sambungan yang menganggur melewati `wait_timeout` mati dengan
  4031; `SET SESSION wait_timeout = 28800` mencegahnya. (5.2, 5.3)

**X3 (MySQL 8.4) tidak diukur**: 8.4 tidak terpasang di lingkungan ini;
lihat bagian 8.

## 4. Pendekatan yang dipertimbangkan

**Keadaan sesudah bangun ulang gagal**

- A. Biarkan seperti sekarang (peran `replica` + baris gagal) dan
  pertahankan "Batalkan percobaan bangun ulang". Paling kecil. Tetapi
  keadaan bawaannya tetap yang paling berlubang: monitor diam, sertifikat
  dan saran salah sampai operator menekan tombol; dan tombol itu
  menghapus jalan "Ulangi".
- B (dipilih semula, ditolak sesudah tinjauan rancangan). Peran kembali
  `quarantined` saat gagal, baris gagal dipertahankan; peran berubah di
  `fail`, `Resume`, `Retry`, `Approve`. Tinjauan menemukan jalur gagal
  yang tidak mengubah peran (`failUnrunnable`, galat store di tengah) dan
  urutan tulisan yang bisa disela `Retry`; setiap saat bangun ulang gagal
  berperan `replica`, master lama itu diperlakukan sebagai replika gagal
  biasa (Resync dan Lepas diterima).
- **B' (dipilih). Peran tetap `quarantined` dari promote sampai settle.**
  Aturannya menjadi satu kalimat: peran `quarantined` berarti karantina
  belum berakhir. `AddReplica` tidak mengubah peran `quarantined`; settle
  memasang peran `replica`, bendera dan menghapus tanda dalam satu
  transaksi store. Tidak ada lagi perubahan peran di `fail`, `Resume`,
  `Retry`, `Approve`, jadi tidak ada keadaan setengah berubah. Harganya:
  bangun ulang yang sedang berjalan tampil di kotak "Master lama" dan di
  halaman pekerjaannya, bukan sebagai kartu replika "sedang dibuat".
- C. Buang baris replika saat gagal (jadi master lama biasa). Sederhana,
  tetapi "Ulangi dari tahap ini" hilang, padahal itu jalan murah yang
  ditunjuk teks agent sesudah `grant-admin` atau `drop-database`.

**Jawaban settle yang hilang di jalan** (panel `quarantined`, agent sudah
`replica`)

- A (dipilih semula, ditolak sesudah tinjauan rancangan). Monitor
  memulihkan sendiri sesudah tiga laporan pin `replica`. Panggilan balik
  dari monitor ke orchestrator membalik urutan kunci yang ada (bisa
  macet), dan pin `replica` juga dihasilkan `set-role replica` yang
  diketik admin, jadi panel akan mencatat "karantina diakhiri sesudah
  salinan" untuk job yang mungkin gagal sebelum menyalin.
- **B (dipilih). Tidak ada pemulihan dari laporan.** Kotak menampilkan
  satu kalimat untuk server `quarantined` yang agentnya melaporkan peran
  lain; "Ulangi dari tahap ini" memulihkan jawaban settle yang hilang
  seperti sekarang. Harganya satu klik.

**Langkah-dengan-tangan di server yang dikarantina**

- A. Pertahankan README dan perbaiki lagi. Sudah tiga putaran; setiap
  putaran menemukan cara baru untuk kehilangan data.
- B. Tombol di panel yang menyuruh agent membuang database atau memberi
  hak. Panel yang dibobol tidak boleh bisa membuang data master lama, jadi
  tombol itu tetap butuh persetujuan root di server. Hasilnya dua langkah
  (panel lalu server) untuk hal yang bisa satu.
- **C (dipilih). Perintah CLI agent yang dijalankan root di server.** Satu
  perintah salin-tempel, sejalan dengan keputusan pemilik 7.1 pada spek 5b
  ("satu perintah di master lama"). Perintah itu yang membuat cadangan,
  memeriksanya, menampilkan apa yang akan terjadi, meminta konfirmasi,
  membuka kunci sesaat, dan menguncinya lagi. Tidak ada jalur panel, tidak
  ada perintah protokol baru. Tinjauan rancangan menambahkan yang dulu
  dijaga langkah README dan hilang di rancangan pertama: pemeriksaan di
  master baru, sesi lain yang menahan `DROP`, dan pemeriksaan ulang
  sesudah konfirmasi (5.4).

**Apa yang diketahui `drop-database` tentang master baru**

- A (dipilih semula, ditolak sesudah tinjauan rancangan). Agent menyimpan
  daftar "tidak disalin" di berkas; nama yang ada di daftar cukup
  dikonfirmasi `ya`. Daftar itu berkata lebih dari yang agent tahu (untuk
  dump bernama: semua database lain), bisa diisi panel yang dibobol lewat
  pilihan nama dump, hidup lebih lama dari karantinanya, dan tidak ada
  sama sekali di jalur utama (penolakan "Periksa server tujuan").
- **B (dipilih). Tidak ada berkas daftar.** Perintah selalu meminta nama
  database diketik dan selalu menampilkan cara melihat di master baru.

**Penjaga terus-menerus**

- A. Baca bendera tiap putaran, kunci lagi tanpa peduli siapa yang
  membuka. Berebut dengan muat data agent sendiri (anti-tujuan).
- **B (dipilih). Baca tiap putaran; siapa pun yang membuka baca-saja
  dengan sah memegang satu kunci berkas (`flock`) selama membukanya**:
  pekerjaan agent sendiri maupun perintah CLI di proses lain. Penjaga
  menulis bendera hanya selagi ia sendiri memegang kunci berkas itu dan
  tidak ada tanda muat (5.2), jadi tidak ada celah di antara "membaca
  bendera" dan "mengunci". Kunci berkas dilepas kernel bila prosesnya
  mati. Sesudah tinjauan rancangan: berkas kunci berada di direktori yang
  hanya bisa dimasuki root, dan jalur yang tidak pernah memasang bendera
  lagi (CLONE) melepas kuncinya dengan sengaja.
- C. Hanya peringatan panel dari laporan berkala, tanpa mengunci lagi.
  Lebih kecil, tetapi server tetap bisa ditulis sampai orang bertindak.

**Jendela penyalinan**

- A. Membaca nama database dari aliran dump itu sendiri. Tidak butuh
  apa-apa lagi, tetapi alirannya bisa dipalsukan (F11); ditolak sesudah
  builder check.
- **B (dipilih). Daftar database master baru dibaca dengan klien `mysql`
  memakai kredensial dan TLS yang sama dengan `mysqldump`, sebelum dan
  sesudah dump; dan `read_only` tetap menyala selama setiap pembukaan.**
  Tidak ada jalur kredensial baru. Database yang ada di server sesudah
  muat tetapi tidak ada di daftar master baru berarti bukan hasil salinan.
  Sesudah pengukuran M1: tidak ada pemakai yang butuh `read_only` mati,
  jadi mode "matikan keduanya" yang semula disiapkan untuk CLONE tidak
  dibangun sama sekali.
- C. Cek silang dengan folder di direktori data MySQL. Folder asing di
  direktori data (misalnya `lost+found`) akan menolak bangun ulang yang
  sah; ditolak.

**Uid untuk `mysqlbinlog` dan ekor log**

- A (dipilih semula, ditolak sesudah tinjauan rancangan). Akun pada opsi
  `--user` layanan, dicari di daftar pengguna sistem. Butuh menjalankan
  `--print-defaults` dan pencarian pengguna, dengan beberapa cara untuk
  "uid tidak ketemu".
- **B (dipilih). Pemilik direktori yang dibaca** (dibaca dengan `lstat`).
  Proses itu lalu tidak bisa membaca apa pun yang tidak bisa dibaca
  pemilik berkasnya. Pemilik root berarti jalan seperti sekarang, dengan
  satu baris peringatan.

## 5. Keputusan desain

### 5.1 Keadaan master lama: satu aturan

**Aturannya: peran `quarantined` di panel berarti karantina belum
berakhir.** Peran itu dipasang promote dan baru berganti saat agent
mengakhiri karantina ("settle": jawaban OK `mysql.configureReplication`;
bendera `QuarantineEnded`). Tidak ada tempat lain yang mengubahnya.

| Keadaan | Peran di panel | Baris replika | Tanda master lama | Pin agent |
|---|---|---|---|---|
| Dikarantina (belum dibangun ulang) | `quarantined` | tidak ada | ada | `quarantined` |
| Bangun ulang berjalan atau menunggu | `quarantined` | `creating` | ada | `quarantined` |
| Bangun ulang gagal sebelum settle | `quarantined` | gagal | ada | `quarantined` |
| Sudah settle, bangun ulang masih berjalan | `replica` | `creating` | sudah dihapus | `replica` |
| Bangun ulang gagal sesudah settle | `replica` | gagal | sudah dihapus | `replica` |
| Baris lama (dari sebelum rencana ini) | `replica` | gagal | ada | `quarantined` |
| Sesudah promote darurat kedua | `quarantined` | tidak ada atau gagal | **hilang** | `quarantined` |

Apa yang ditawarkan halaman, diterima orchestrator, dan dinilai monitor:

| Keadaan | Ditawarkan | Diterima orchestrator | Monitor | Jalan keluar |
|---|---|---|---|---|
| Dikarantina | Hapus, Periksa, Amankan, Bangun ulang | sama | master lama | bangun ulang, hapus |
| Bangun ulang berjalan atau menunggu | kotak: kalimat T22 dan tautan ke pekerjaannya; di pekerjaan: Batalkan, Setujui | Batalkan (kembali "Dikarantina"), Setujui; yang lain ditolak karena server sibuk | master lama; hasil start dan "dikunci lagi" dinilai | selesai, gagal, batal |
| Gagal sebelum settle | Hapus; Periksa dan Amankan kecuali `CopyStarted`; Bangun ulang; "Ulangi dari tahap ini" di pekerjaan | sama; Resync, Lepas, Buat replika biasa ditolak | master lama | Ulangi, bangun ulang baru, hapus, Periksa lalu Ulangi |
| Sudah settle / gagal sesudah settle | seperti replika biasa (Resync, Lepas, Ulangi) | sama | replika | biasa |
| Baris lama | — | dinormalkan saat panel menyala | — | menjadi "gagal sebelum settle" atau "gagal sesudah settle" |
| Sesudah promote darurat kedua | kalimat T6, Hapus | hapus; yang lain ditolak | master lama tanpa topologi | hapus, lalu `set-role` di server (dialog hapus mengatakannya) |

- **`AddReplica` dan `RemoveReplica` tidak mengubah peran `quarantined`.**
  Bangun ulang master lama menambah baris replika tanpa mengganti peran;
  membatalkan atau memulai bangun ulang baru membuang baris tanpa
  melewati peran `unassigned`. Untuk peran lain keduanya bekerja seperti
  sekarang.
- **Settle dalam satu transaksi store:** bendera `QuarantineEnded` pada
  job, penghapusan tanda master lama, dan peran `replica`. Bila transaksi
  itu gagal, langkah gagal dan ketiganya tidak berubah; "Ulangi dari
  tahap ini" mengulangnya (agent menjawab OK lagi).
- **Gagal, lanjut-sesudah-restart, Ulangi, Setujui** tidak menyentuh
  peran. Setiap jalur yang menggagalkan job bangun ulang (galat langkah,
  panel dimulai ulang, job yang tidak bisa dijalankan panel ini,
  peluncuran yang ditolak) meninggalkan baris gagal; `Resume` menandai
  baris lebih dulu, status job sesudahnya, di bawah kunci yang sama
  dengan `Retry`.
- **"Sudah digantikan".** Untuk job `create_replica` dengan
  `RebuildOldMaster` yang benderanya belum terpasang, peran server harus
  `quarantined` (peran lain berarti "peran server ini sudah berubah");
  untuk job lain aturan peran `replica` tidak berubah. Job Periksa dan
  Amankan data tertinggal **tidak** dihitung sebagai "job yang lebih baru"
  bila keempat syarat ini benar: jenis `create_replica` dengan
  `RebuildOldMaster`; bendera belum terpasang; baris server itu gagal;
  server itu masih master lama topologi job. Keduanya hanya membaca, dan
  peringatan panel sendiri menyuruh operator menjalankannya. Pemeriksaan
  lain (master berganti, job jenis lain yang lebih baru) tidak berubah.
- **Dialog Ulangi dan Setujui** pada bangun ulang yang belum settle
  menampilkan peringatan data tertinggal yang sama dengan form Bangun
  ulang, dari fungsi yang sama: data tertinggal yang ditemukan dan belum
  diamankan, belum diperiksa, dan "sempat bisa ditulis sejak pemeriksaan
  terakhir". Tanpa itu "Ulangi" sesudah Periksa menghapus yang baru saja
  ditemukan Periksa tanpa sepatah kata.
- **Penolakan.** `refuseAwaitingRebuild` menolak Resync, Lepas, dan Buat
  replika biasa untuk setiap server yang `AwaitingRebuild`.
  `AwaitingRebuild` menjadi: peran `quarantined`.
- **Hapus server.** Server berperan `quarantined` boleh dihapus walau
  punya baris gagal; barisnya ikut terhapus. Penjaga lain (pengalihan
  belum dikonfirmasi, promote tertunda, server sibuk) tidak berubah.
  Sebelum menghapus, dan sebelum memulai bangun ulang baru, panel
  mengirim `job.cancel` untuk job salinan agent yang tercatat pada
  bangun ulang terakhir (usaha terbaik): sesudah panel dimulai ulang,
  agent mungkin masih menyalin.
- **Kotak "Master lama", form bangun ulang, halaman replika, halaman
  pekerjaan.** Mengikuti peran dan keadaan salinan saja. Selagi bangun
  ulang berjalan atau menunggu: kotak hanya menampilkan kalimat T22 dan
  tautan ke pekerjaannya. Dalam keadaan gagal: Hapus tampil; Periksa dan
  Amankan tampil kecuali keadaan salinan `CopyStarted`; tautan Bangun
  ulang tampil; "Ulangi dari tahap ini" tampil di pekerjaan selama belum
  digantikan job lain. Halaman replika server berperan `quarantined`
  tidak menawarkan Resync maupun Lepas; ia menampilkan kalimat T23 dan
  tautan ke halaman server.
- **Daftar replika** (dasbor, halaman replikasi, halaman master) tidak
  menampilkan server berperan `quarantined` sebagai kartu replika, baik
  barisnya `creating` maupun gagal; ia tampil lewat kotaknya sendiri.
- **Dihapus:** tindakan "Batalkan percobaan bangun ulang" seluruhnya
  (rute, fungsi store, tombol, tiga kalimat, label audit), lengan kedua
  `AwaitingRebuild`, kalimat "percobaan masih tercatat". Tidak ada lagi
  keadaan yang membutuhkannya.
- **Baris lama.** Saat panel menyala, setiap server berperan `replica`
  dengan baris gagal yang masih master lama topologinya dinormalkan:
  bendera job bangun ulang terbarunya terpasang → tanda dihapus (replika
  gagal biasa); selain itu (juga bila job itu tidak ada atau tidak
  terbaca) → peran `quarantined`. Aman dijalankan berulang.
- **Agent melaporkan peran lain.** Panel tidak pernah mengubah peran
  karena sebuah laporan. Untuk server berperan `quarantined` yang
  agentnya melaporkan pin peran lain (`replica`, `master`, `unassigned`),
  kotak menampilkan satu kalimat: T3 bila server itu tidak punya baris,
  T3b bila punya (menambah jalan "Ulangi dari tahap ini"). Jawaban settle
  yang hilang di jalan dipulihkan "Ulangi dari tahap ini" seperti
  sekarang: agent menjawab OK lagi dan settle dicatat.
- **Monitor.** `resolve` membaca peran lebih dulu: peran `quarantined`
  selalu dinilai sebagai master lama, ada baris atau tidak. Karena peran
  tetap `quarantined` selama bangun ulang, hasil start dan hasil "dikunci
  lagi" (5.2) dinilai juga selama bangun ulang berjalan tanpa aturan
  tambahan; lengan khusus monitor untuk "peran replika yang belum
  dinilai" dihapus.
- **Promote kedua.** Promote **terencana** ditolak selama topologi itu
  masih punya master lama yang belum dibangun ulang atau dihapus (teks
  T4). Promote **darurat** tidak pernah ditolak; layarnya menampilkan
  peringatan (T5), dan sesudahnya kotak master lama yang pertama
  menampilkan kalimat T6 dengan Hapus sebagai satu-satunya tindakan.
  Tiga tempat lain ikut berkata benar untuk server itu: halaman
  replikanya (T23, tanpa Resync dan Lepas); peringatan
  `master_start_late` yang terbuka ditutup dengan alasan T26, dan yang
  baru dibuka memakai pesan T25 (Periksa data tertinggal memang ditolak
  untuk server itu); layar promote menamai server `quarantined` yang
  punya baris sebagai T24, bukan "Replika gagal".

### 5.2 Penjaga membaca kunci baca-saja di setiap putaran

- Setiap putaran (5 detik), untuk peran `quarantined` dengan MySQL
  menjawab, penjaga membaca `super_read_only` dan `read_only`.
- **Satu kunci berkas untuk semua pembukaan yang sah.** Berkas
  `lift.lock` (mode 0600) di direktori `/var/lib/dbpanel/agent/` (milik
  root, mode 0700; dibuat agent atau perintah CLI bila belum ada). Setiap
  pembuka (gerbang agent, penjaga, CLI) membuka berkas itu tanpa mengikuti
  symlink dan tanpa mewariskannya ke proses anak, lalu memeriksa
  deskriptornya: berkas biasa, milik root, tanpa bit untuk grup atau
  pengguna lain, satu nama saja. Berkas yang tidak lulus diganti agent
  dengan berkas baru; CLI menolaknya dan menyebut sebabnya (teks C4).
  Yang membuka baca-saja memegang `flock` eksklusif atas berkas itu dari
  sebelum bendera dimatikan sampai sesudah bendera dipasang lagi:
  pekerjaan agent (muat dump, CLONE, pasang plugin, buang database, buat
  akun) dan perintah CLI (5.4).
- **Pembukaan hanya mematikan `super_read_only`.** Satu-satunya
  pernyataan pembuka adalah `SET GLOBAL super_read_only = OFF`, dan
  pemulihnya `SET GLOBAL super_read_only = ON`. `read_only` tidak pernah
  dimatikan oleh pembukaan mana pun (M1).
- **Di dalam agent, pembukaan dihitung.** Pembuka pertama mengambil kunci
  berkas dan mencatat nilai semula; setiap pembuka, juga yang berikutnya,
  mengirim pernyataan pembuka (yang pertama mungkin sudah dipasang lagi
  oleh pihak lain); pembuka terakhir memasang lagi yang semula menyala.
- **Pemulihan.** Setiap pembuka menyimpan satu sambungan sendiri sejak
  awal, dengan `SET SESSION wait_timeout = 28800` supaya tidak mati selagi
  menganggur (X7). Pembuka terakhir lebih dulu **membaca** bendera
  (membaca tidak butuh hak): bila `super_read_only` sudah menyala, tidak
  ada yang dikirim dan pemulihan berhasil; hanya bila masih mati ia
  mengirim `SET GLOBAL super_read_only = ON` pada sambungan yang
  disimpannya (pada pembukaan tunggal: sambungan pembuka pertama). Bila
  itu gagal, kunci berkas tetap dilepas, agent mencatatnya sebagai
  kegagalannya sendiri, dan penjaga yang mengunci lagi.
- **Muat dump penuh memasang benderanya sendiri di aliran muat** (5.3):
  sesudah muat itu mengganti akun, tidak ada sambungan agent yang masih
  boleh memasangnya (X7), jadi pemulihan di atas mendapati bendera sudah
  menyala.
- **Dua cara mengakhiri pembukaan.** `end`: pasang lagi yang semula
  menyala, lalu lepas. `abandon`: lepas tanpa menyentuh bendera, untuk
  jalur CLONE sesudah fase datanya, tempat restart MySQL yang
  mengembalikan nilai tersimpan; bila pembuka terakhir pergi lewat
  `abandon`, nilai semula yang dicatat dilupakan.
- **Perintah CLI** mencoba mengambil kunci berkas tanpa menunggu
  (mengulang sampai 3 detik). Bila sedang dipegang (misalnya bangun ulang
  sedang memuat), perintah menolak dengan teks C3. Pekerjaan agent yang
  mendapati kunci dipegang CLI menunggu paling lama 10 detik lalu gagal
  dengan teks T12, sebelum mengubah apa pun.
- **Satu aturan untuk penjaga: penjaga menulis `super_read_only`,
  melaporkan start, atau melaporkan "dikunci lagi" hanya selagi ia sendiri
  memegang kunci berkas itu dan tidak ada tanda muat yang aktif.** Gerbang
  agent dan CLI butuh kunci yang sama untuk membuka. Urutan tiap putaran:
  1. Masuk ke MySQL; pembukuan peralihan seperti sekarang; baca
     `super_read_only`, `read_only`, `init_file`.
  2. Bila kedua bendera menyala dan putaran ini bukan peralihan: selesai;
     berkas kunci tidak disentuh.
  3. Coba ambil kunci berkas tanpa menunggu; lihat apakah tanda muat
     aktif.
  4. Kunci tidak didapat, atau tanda muat aktif: ada pembukaan yang sah
     (atau muat berada di antara tandanya dan pembukaannya). Penjaga tidak
     menyentuh `super_read_only`. Bila `read_only` mati, ia memasangnya
     lagi (`SET GLOBAL read_only = ON`, yang tidak mengubah
     `super_read_only`), melaporkan "dikunci lagi" dan mencatat "bisa
     ditulis sejak muat". Laporan start yang tertunda dinilai pada putaran
     pertama sesudah pembukaan itu berakhir.
  5. Kunci didapat dan tidak ada tanda muat: `PERSIST` kedua bendera di
     bawah kunci itu; lalu laporan start bila ada start yang tertunda,
     selain itu laporan "dikunci lagi" bila ada bendera yang terbaca mati.
  6. Bila MySQL menjawab tetapi penjaga tidak bisa membaca kedua bendera
     atau tidak bisa memasangnya lagi (login ditolak, pernyataan gagal,
     atau galat 1205 di bawah) selama **tiga putaran berturut-turut**,
     selagi kunci berkas bebas dan tidak ada tanda muat: penjaga
     melaporkan "tidak bisa memeriksa" (di bawah) dan mencatat "bisa
     ditulis sejak muat"; diulang paling sering sekali per 10 menit selama
     keadaannya berlanjut.
- **Pernyataan penjaga dibatasi di server, bukan di klien** (X4). Penjaga
  menjalankan pernyataannya pada satu sambungan yang lebih dulu menyetel
  `SET SESSION lock_wait_timeout = 10`. Pernyataan yang menunggu di
  belakang `LOCK TABLES … WRITE` atau tulisan yang sedang berjalan lalu
  berakhir dengan galat 1205 sesudah 10 detik tanpa mengubah bendera; itu
  dihitung "tidak bisa mengunci" untuk butir 6 dan diulang putaran
  berikutnya. Batas waktu konteks saja tidak dipakai sebagai batas:
  pernyataan yang dibatalkan klien tetap menunggu di server dan bisa
  berlaku setengah.
- **Restart CLONE** bukan start yang dilaporkan, seperti sekarang. MySQL
  menyala lagi dengan nilai tersimpan penerima sendiri (X6: `1 1` di
  server yang dikarantina), jadi selama tanda muat masih aktif penjaga
  tidak mendapati apa pun untuk dipasang; `cloneFrom` tetap menjalankan
  `PERSIST`-nya sendiri sesudah MySQL menyala, lalu melepas pembukaannya
  lewat `abandon`.
- **Sesudah muat mengganti akun** (F22) login penjaga ditolak sampai
  operator menjalankan "Hubungkan MySQL"; itu jalur biasa, jadi selama
  jendela itu (dari akhir muat yang melaporkan `AdminReplaced` sampai
  login penjaga berhasil lagi) butir 6 tidak menghitung. Settle tetap
  membaca bendera sendiri (di bawah).
- **Kejadian "dikunci lagi" dan "tidak bisa memeriksa" dicatat sebagai
  hasil start `late`** dengan satu keterangan tambahan (`running` atau
  `unverified`), lewat mekanisme hasil start yang ada: ditulis ke berkas
  sebelum dikirim, dibawa laporan berkala, tidak hilang bila agent
  restart atau panel sedang tidak terjangkau. Catatan itu hanya mengisi
  entri `late`; entri "kunci start dilewati" dan "gagal menyala" tidak
  disentuh (kejadian ini bukan start MySQL). Id-nya diturunkan dari waktu
  kejadian, seperti entri lain. **Setiap kejadian dicatat**; hanya
  pengiriman kejadiannya yang dibatasi satu per 60 detik.
- **Panel:** tidak ada jenis peringatan baru. Peringatan
  `master_start_late` yang ada dipakai; pesannya mengikuti keterangan itu
  (T1 untuk `running`, T1b untuk `unverified`), dan label jenisnya
  menjadi T20. Ditutup seperti sekarang oleh Periksa data tertinggal yang
  lebih baru atau saat server bukan lagi master lama, dan kini juga oleh
  keadaan salinan `CopyStarted` (alasan T2).
- **`master_start_late` mengikuti keadaan salinan** (SEC-226): pesannya
  tidak lagi menyuruh Periksa bila salinan sudah mulai (T2b). Peringatan
  yang dibuka dalam keadaan `CopyStarted` ditutup pada putaran monitor
  berikutnya dengan alasan T2: satu pemberitahuan Kritis dan satu
  pemberitahuan tutup per kejadian, dan itu disengaja. Pengecualian:
  keterangan `unverified` selalu memakai T1b dan **tidak** ditutup oleh
  `CopyStarted`, karena masalahnya (agent tidak bisa mengunci) masih
  berlangsung; bila peringatan `master_start_late` sudah terbuka dengan
  pesan lain, pesannya diganti T1b.
- **Settle.** Agent mengingat (di memori, seumur catatan muat) bila sejak
  muat terakhir **dimulai** penjaga mendapati bendera mati tanpa pembukaan
  yang sah (butir 4 dan 5) atau tidak bisa memeriksa (butir 6). Yang
  dihitung hanya bendera yang terbaca mati atau yang tidak bisa dibaca;
  `PERSIST` yang gagal sesudah kedua bendera terbaca menyala diulang
  putaran berikutnya dan tidak dihitung. Muat baru yang dimulai menghapus
  ingatan itu **bersama catatan muat sebelumnya**: sejak muat baru mulai,
  catatan lama tidak lagi menggambarkan server itu. `settleRebuilt`
  menolak (teks T7) bila ingatan itu terpasang; dan tepat sebelum
  mengakhiri karantina ia membaca kedua bendera sendiri sambil memegang
  kunci berkas: selain `1 1` berarti T7 juga. Bila kunci berkas sedang
  dipegang sebuah perintah CLI, settle menolak dengan T12 (bisa diulang),
  bukan T7. Pembukaan sah oleh CLI tidak dihitung. Ini, bersama F15,
  membuat serangan pada bukti daftar lengkap (SEC-204) tidak lagi senyap;
  sisanya ada di bagian 8.

### 5.3 Jendela penyalinan

- **`read_only` tetap menyala** selama setiap pembukaan, di semua
  pemakainya termasuk CLONE (M1). Karena itu `dumpFrom`, sebelum `STOP
  REPLICA` dan sebelum perubahan lokal apa pun, menolak akun muat yang
  tidak memegang `CONNECTION_ADMIN` maupun SUPER selagi `read_only`
  menyala di server tujuan (di peran apa pun, bukan hanya di server yang
  dikarantina), dengan teks T10: tanpa pemeriksaan itu akun seperti itu
  baru gagal sesudah log biner di-reset dan sesudah `gtid_executed`
  mengaku memuat set sumber (X2). Salah satu dari kedua hak itu cukup.
- **Penutup aliran muat dump penuh memasang bendera lagi.** Untuk dump
  penuh (`--all-databases`) yang dimuat selagi pembuka pertama mendapati
  `super_read_only` menyala, penutup yang ditambahkan agent ke aliran
  muat menjadi `SET GLOBAL super_read_only = ON;` diikuti `FLUSH
  PRIVILEGES;` yang sudah ada. Urutan itu penting: sesudah `FLUSH
  PRIVILEGES` memuat akun master baru, tidak ada sambungan agent yang
  bisa memasang bendera (X7). Dump bernama tidak mengganti akun dan tidak
  mendapat penutup ini; CLONE memakai restart (X6).
- **Ada di master baru.** Pada dump penuh, agent membaca daftar database
  master baru dua kali dengan klien `mysql` (kredensial dan TLS yang sama
  dengan `mysqldump`, nama di-`HEX` supaya tidak ada yang perlu ditafsir):
  sekali sebelum perubahan lokal apa pun, sekali sesudah dump selesai.
  Catatan muat menyimpan gabungan keduanya. Saat settle, database
  non-sistem yang ada di server tetapi **tidak** ada di gabungan itu
  berarti tidak disalin dari sumber: karantina tidak diakhiri (teks T8a
  atau T8b). Aturan lama ("sudah ada sebelum muat" dan "bukan yang
  dinamai") tetap. CLONE tetap dihitung mengganti seluruhnya.
- Daftar pertama yang tidak terbaca menggagalkan muat sebelum ada yang
  berubah. Daftar kedua dicoba **tiga kali**; bila tetap tidak terbaca,
  langkah salinan gagal dengan teks T11 dan muat itu tidak dihitung
  (operator tahu saat itu juga, bukan di langkah berikutnya dengan sebab
  yang salah).

### 5.4 Tiga perintah menggantikan SQL dengan tangan

Semua dijalankan root di server, hanya bekerja pada pin `quarantined`
(selain itu menolak dengan teks C1), dan masuk ke MySQL sebagai root lewat
socket; bila itu ditolak, perintah meminta kata sandi di terminal (tidak
ditampilkan, tidak disimpan, tidak masuk riwayat shell, tidak ditulis ke
berkas). Opsi `--pengguna <nama>` memakai akun administrator lain untuk
server yang akun administratornya bukan `root@localhost`. Semua
menampilkan apa yang akan dilakukan dan meminta konfirmasi, seperti
`approve`. Semua menulis satu baris ke syslog. Nama yang berasal dari
MySQL dicetak tanpa karakter kendali dan penanda arah, dan dikutip di
baris syslog.

Yang sama pada ketiganya:

- **Urutan pemeriksaan.** Pin; lalu kunci berkas dicoba (ambil dan
  lepas): bila sedang dipegang, teks C3; baru kemudian bendera harus
  `1 1`, selain itu teks C2. Dengan urutan ini operator yang menjalankan
  perintah selagi bangun ulang memuat mendapat sebab yang benar.
- **Diperiksa ulang sesudah konfirmasi.** Sesudah operator menjawab dan
  sesudah kunci berkas dipegang, pada sambungan yang sama: pin dibaca
  lagi dan harus `quarantined`; bendera harus `1 1`; dan yang akan diubah
  masih sama dengan yang ditampilkan. Selain itu: teks C11, tidak ada
  yang diubah. Pernyataan terakhir memasang lagi yang terbaca pada
  pemeriksaan itu, bukan nilai tetap.
- **Jendela terbuka yang pendek dan selalu ditutup.** Pernyataannya
  dijalankan pada satu sambungan dengan `sql_log_bin` mati dan
  `lock_wait_timeout` 60 detik, sambil memegang kunci berkas (5.2). Dari
  sebelum `super_read_only` dimatikan sampai sesudah pemeriksaan akhir,
  SIGINT, SIGTERM dan SIGHUP ditangkap: perintah memasang bendera lagi
  (sambungan sendiri, batas waktu sendiri), mencetak teks C13, lalu
  keluar dengan kode bukan nol. Jendela terbuka dibatasi 10 menit;
  sesudah itu perintah menyerah dengan cara yang sama (teks C14). Di
  akhir, perintah selalu memeriksa `1 1`; bila bukan, ia sendiri
  memasangnya lagi dan mengatakannya.

- **`sudo dbpanel agent drop-database <nama>`** untuk "database yang tidak
  ada di master baru".
  1. Menolak skema sistem dan nama yang tidak ada.
  2. Membuat cadangan dengan `mysqldump` ke berkas baru di folder cadangan
     milik root (bawaan `/var/lib/dbpanel/dibuang/`, bisa diganti `--ke
     <folder>`), sesudah memeriksa ruang kosong. Berkas yang namanya sudah
     ada tidak pernah ditimpa dan tidak diganti nama lain diam-diam:
     perintah menolak (teks C6). Bila folder `--ke` bukan milik root atau
     bisa ditulis grup atau pengguna lain, perintah mencetak peringatan
     C7 sebelum bertanya (tidak menolak: disk yang dipasang adalah alasan
     opsi itu ada).
  3. Memeriksa cadangan lewat deskriptor yang sama dengan yang dipakai
     menulisnya. **Bukti kelengkapan hanya dua:** `mysqldump` keluar 0,
     dan baris terakhir berkas diawali `-- Dump completed` (X5: dump yang
     terpotong karena disk penuh lolos semua hitungan lain). Sebagai
     pemeriksaan kewajaran, bukan bukti: berkas memuat baris `CREATE
     DATABASE` untuk nama itu; jumlah baris berawalan `CREATE TABLE` **di
     luar blok `DELIMITER`** (isi routine, trigger dan event ditulis apa
     adanya di dalam blok itu) **paling sedikit** jumlah tabel dasar di
     database; tidak ada blok `DELIMITER` yang masih terbuka di akhir
     berkas. `mysqldump` dijalankan tanpa membaca berkas opsi lain (opsi
     `skip-comments` di berkas opsi menghapus baris penutup). Gagal satu
     saja: berhenti dengan teks C8, tidak ada yang dihapus. Database yang
     namanya memuat `'` atau `"` tidak bisa di-dump `mysqldump` 8.0 (X5),
     jadi selalu berakhir di sini. Berkas dan foldernya di-`fsync`
     sebelum operator ditanya.
  4. Menampilkan nama database, jumlah tabel, letak, ukuran dan SHA-256
     cadangan, dan contoh menyalin cadangan ke server lain. Lalu
     menampilkan cara melihat di master baru (teks C9; C9b untuk nama
     yang tidak aman ditulis di baris perintah) dan **selalu** meminta
     operator mengetik nama databasenya (teks C10). Agent tidak tahu
     apakah database itu ada di master baru, jadi tidak ada jalan pendek
     `ya`.
  5. Memeriksa ulang (database masih ada dengan jumlah tabel dasar yang
     sama), membuang database itu, memeriksa `1 1`, lalu menyebut langkah
     berikutnya di panel (teks C15). Bila `DROP DATABASE` menunggu sesi
     lain lebih dari 60 detik: bendera dipasang lagi, teks C12, cadangan
     tetap ada.
- **`sudo dbpanel agent grant-admin`** untuk akun admin yang kurang hak:
  menghitung hak yang kurang pada akun tersimpan terhadap **kumpulan hak
  replika** (yang diperiksa bangun ulang; replika adalah satu-satunya
  yang bisa dibuat panel dari server yang dikarantina) ditambah syarat
  bukti daftar lengkap (`SHOW DATABASES`), menampilkan pernyataan
  `GRANT`-nya, meminta `ya`, menjalankannya. Akun dinamai seperti MySQL
  mengenalnya: perintah masuk sekali dengan akun tersimpan dan membaca
  `CURRENT_USER()`; bila login itu ditolak, teks C17.
- **`sudo dbpanel agent reset-admin`** untuk akun yang namanya tidak
  didukung, yang rusak, atau yang belum pernah tersimpan: membuat atau
  menyetel ulang `dbpanel_admin@localhost` dengan kata sandi acak dan hak
  penuh, lalu menyimpannya sebagai akun agent (sama seperti pemasangan).
  Operator tidak perlu "Hubungkan MySQL" lagi.
- **Teks yang menunjuk README atau `GRANT` polos diganti** menunjuk
  perintah itu:
  - penolakan "masih punya database yang tidak disalin" (T8a untuk dump
    penuh: nama dipisah menjadi "tidak ada di master baru" →
    `drop-database` dan "ada di master baru" → bangun ulang baru; T8b
    untuk dump bernama), dengan saran sebelum daftar nama supaya
    pemotongan panjang tidak pernah memotong sarannya;
  - dua penolakan daftar (T16, T17);
  - penolakan "kurang hak" `mysql.connect` di server yang dikarantina
    (T15): akun itu **tidak disimpan**, jadi yang ditunjuk `reset-admin`,
    bukan `grant-admin` (yang bekerja pada akun tersimpan);
  - pemeriksaan "Hak akses akun admin" di "Periksa server tujuan" pada
    server yang dikarantina: tanpa pernyataan `GRANT`, dengan teks T14;
  - pemeriksaan "Server replika khusus" pada server yang dikarantina
    diakhiri kalimat T13 (ini jalur utama menuju `drop-database`);
  - spanduk "Akun admin MySQL ini kurang hak" di halaman server yang
    dikarantina menampilkan perintahnya, bukan `GRANT` polos (T21,
    SEC-229).
- **README:** bagian "Database yang tidak ada di master baru" dan
  "Memberi hak SHOW DATABASES di server yang dikarantina" ditulis ulang
  menjadi satu perintah masing-masing dengan arti keluarannya. Blok SQL
  dengan tangan di kedua bagian itu dihapus. Kedua judul bagian
  dipertahankan satu rilis (5.9), dan masing-masing dibuka dengan kalimat
  T27.

### 5.5 Program dan berkas milik akun layanan

- `mysqlbinlog` (Periksa data tertinggal, Amankan, dan pembacaan ekor
  binlog saat MySQL mati) dijalankan sebagai **pemilik direktori binlog**,
  juga pembacaan berkas indeks binlog. Keluaran tetap ditulis root ke
  folder penyelamatan milik root.
- Ekor log galat dibaca oleh program kecil yang dijalankan sebagai
  **pemilik direktori log galat**, dengan batas ukuran dan batas waktu,
  bukan oleh root di dalam proses.
- Pemilik dibaca dengan `lstat` atas direktori itu (uid dan gid; tanpa
  grup tambahan). Tidak ada lagi pencarian akun dari opsi `--user`.
- **Bila pemiliknya root**, agent berjalan seperti sekarang (sebagai
  root) dan menulis satu baris peringatan (T19) di log agent. Ini berlaku
  di semua peran.
- **Jalan balik ke root hanya bila proses tidak bisa dimulai** di bawah
  uid itu (fork, ganti uid, atau exec gagal). Program yang sudah mulai
  lalu gagal adalah Periksa/Amankan yang gagal dengan galatnya sendiri;
  ia **tidak pernah** diulang sebagai root. Tanpa aturan ini akun layanan
  yang menentukan kapan root menjalankan program itu (cukup dengan
  membuat program gagal sebagai uid-nya).
- **Diubah saat pelaksanaan (2026-11-17, menunggu dibaca pemilik):** dua
  keadaan yang bisa dipancing akun layanan **tidak** lagi berujung di
  root; keduanya gagal dengan teksnya sendiri dan tidak mengubah data.
  (a) Proses tidak bisa dimulai karena akun itu sudah mencapai batas
  jumlah prosesnya (`EAGAIN`): teks T19b. (b) Pemilik direktori tidak
  bisa dibaca (`lstat` gagal, misalnya direktori dipindahkan sesaat):
  teks T19c. Kalimat lama "penyelamatan tidak pernah ditolak karena ini"
  tidak berlaku untuk dua keadaan itu: Periksa/Amankan gagal dulu dan
  bisa diulang.
- Agent menunggu paling lama 2 detik sesudah program selesai atau
  dihentikan sebelum melepas pipanya, supaya proses lain milik akun itu
  yang ikut memegang pipa tidak menahan agent.
- M3 mengizinkannya (pemasangan Ubuntu bawaan diukur; RHEL dibaca dari
  sumber kebijakan saja, lihat bagian 8).

### 5.6 Label untuk pesan agent

- Setiap teks yang berasal dari agent dan menjadi galat langkah, galat
  pekerjaan, galat terakhir replika, atau pesan kilat di halaman, tampil
  sebagai `Pesan dari agent <server> (bukan dari panel): <teks>`, satu
  baris, karakter kendali dan penanda arah dibuang, dipotong pada batas
  panjang baris log pekerjaan (bukan 300). Teks buatan panel sendiri
  (batas waktu, agent terputus) tidak dilabeli.
- **Asal ditentukan dari tipe, bukan dari kode.** Hub membungkus setiap
  kegagalan yang dilaporkan agent dalam tipe galatnya sendiri; galat
  buatan hub (batas waktu, agent terputus, termasuk yang diberikan kepada
  pemanggil yang sedang menunggu saat sambungan putus) tidak dibungkus.
  Jawaban agent yang kodenya kode milik panel (`offline`, `timeout`)
  tetap dibungkus, jadi tetap berlabel: agent yang dibobol tidak bisa
  melepas label dengan memilih kode.
- Baris log pekerjaan yang berasal dari agent diratakan menjadi satu baris
  dan diberi awalan `agent: `.
- Urutan tetap: label, lalu sensor. Aturan sensor untuk frasa panel dan
  akun yang disarankan disesuaikan dengan teks baru (5.4), dan sebuah
  test membaca teks agent langsung dari kodenya sehingga daftar frasa dan
  teks agent tidak bisa berpisah diam-diam (SEC-228). Frasa dan jangkar
  untuk teks agent lama dipertahankan satu rilis (5.9).

### 5.7 Butir kecil

- `leftover-gtid` mencatat sumber yang dipakai menghitungnya. Amankan
  menolak bila sumber itu bukan pin sumber sekarang; berkas tanpa catatan
  sumber dihitung tidak ada. Berkas dihapus saat settle. (SEC-201)
- Sesi bukti daftar lengkap menyetel `sql_quote_show_create` dan
  `sql_mode`-nya sendiri, sesuai M4. (SEC-211)
- Teks nama akun yang tidak didukung menyebut karakter yang benar (T17).
- `mysql.startReplica` dan `mysql.startUntil` di server yang dikarantina
  menolak bila sumber kanal replikasi yang akan dijalankan bukan sumber
  yang dipin (T18). (SEC-210)
- Lima butir kode SEC-232: penyimpanan `CopySent` yang gagal menghentikan
  langkah sebelum perintah dikirim; `CopySent` dihitung "tidak pasti" apa
  pun nomor promosinya; untuk CLONE bukti adalah progres di atas 0 atau
  hasil; jangkar akun pada sensor memakai awal kalimat agent yang utuh;
  butir tentang tombol Batalkan gugur bersama tombolnya.
- Dua teks panel SEC-234: pesan persetujuan hapus pada bangun ulang master
  lama (T9); kalimat Batalkan gugur bersama tombolnya.
- Empat ketidaktepatan README SEC-233.

### 5.8 Teks baru (untuk dibaca pemilik)

Setiap teks ditulis di satu baris dan dipakai persis. `%s` dan `<…>`
adalah tempat nilai. Teks bertanda **(baru sesudah tinjauan rancangan)**
belum ada, atau berbunyi lain, saat pemilik menyetujui rancangan ini.

**Tampil di panel (buatan panel)**

- T1 pesan `master_start_late` untuk "dikunci lagi selagi berjalan": `MySQL master lama sempat tidak terkunci selagi berjalan; agent sudah menguncinya lagi. Tulisan yang masuk selama itu tidak ada di master baru: jalankan Periksa data tertinggal`
- T1b pesan `master_start_late` untuk "tidak bisa memeriksa" (baru sesudah tinjauan rancangan): `Agent tidak bisa memeriksa atau memasang lagi kunci baca-saja master lama ini (login atau hak akun MySQL agent ditolak, atau MySQL tidak menjawab perintahnya); master lama mungkin bisa ditulis. Jalankan di server itu: sudo dbpanel agent reset-admin. Sesudah itu jalankan Periksa data tertinggal`
- T2 alasan tutup saat salinan sudah mulai: `bangun ulang sudah mulai mengganti data server ini; tulisan itu tidak bisa diperiksa lagi`
- T2b pesan `master_start_late` saat salinan sudah mulai (baru sesudah tinjauan rancangan: akhir kalimatnya berubah): `master lama sempat bisa ditulis; bangun ulang sudah mulai mengganti data server ini, jadi tulisan itu tidak bisa diperiksa lagi. Bila agent lalu menolak mengakhiri karantina, mulai lagi Bangun ulang sebagai replika; atau hapus server ini`
- T3 kotak, agent melaporkan peran lain, server tanpa baris replika (baru sesudah tinjauan rancangan: kini untuk setiap peran selain karantina): `Agent server ini melaporkan peran %s, bukan karantina. Mulai lagi Bangun ulang sebagai replika, atau hapus server ini.`
- T3b kotak, agent melaporkan peran lain, server dengan baris replika (baru sesudah tinjauan rancangan): `Agent server ini melaporkan peran %s, bukan karantina. Mulai lagi Bangun ulang sebagai replika, hapus server ini, atau tekan "Ulangi dari tahap ini" pada pekerjaan bangun ulangnya.`
- T4 penolakan promote terencana: `master lama %s dari promote sebelumnya belum dibangun ulang atau dihapus; selesaikan itu dulu`
- T5 peringatan layar promote darurat: `Master lama %s dari promote sebelumnya belum dibangun ulang. Sesudah promote ini, server itu tidak bisa lagi diperiksa atau dibangun ulang dari panel; yang tersisa hanya Hapus server.`
- T6 kotak sesudah promote kedua: `Topologi ini sudah promote lagi sejak server ini dikarantina. Data tertinggal dan Bangun ulang tidak tersedia lagi untuk server ini. Hapus server ini dari panel, lalu pasang lagi sebagai replika baru bila masih dipakai.`
- T9 persetujuan hapus pada bangun ulang master lama: `database berikut di master lama ini akan dihapus lalu disalin ulang dari master baru: %s`
- T20 label jenis peringatan `master_start_late`: `Master lama sempat bisa ditulis`
- T21 spanduk akun kurang hak di halaman server yang dikarantina: `Akun admin MySQL ini kurang hak. Jalankan di server ini: sudo dbpanel agent grant-admin, lalu klik Segarkan.`
- T22 kotak selagi bangun ulang berjalan atau menunggu, diikuti tautan "Lihat pekerjaan" (baru sesudah tinjauan rancangan): `Bangun ulang sebagai replika sedang berjalan.`
- T23 halaman replika server yang dikarantina, diikuti tautan ke halaman server (baru sesudah tinjauan rancangan): `Server ini master lama yang dikarantina. Tindakannya ada di halaman server.`
- T24 layar promote, alasan kandidat untuk server dikarantina yang punya baris replika (baru sesudah tinjauan rancangan): `Master lama yang dikarantina`
- T25 pesan `master_start_late` untuk master lama yang topologinya sudah promote lagi (baru sesudah tinjauan rancangan): `master lama sempat bisa ditulis; topologi ini sudah promote lagi, jadi tulisan itu tidak bisa diperiksa lagi dari panel. Hapus server ini`
- T26 alasan tutup `master_start_late` untuk master lama yang topologinya sudah promote lagi (baru sesudah tinjauan rancangan): `topologi ini sudah promote lagi; tulisan itu tidak bisa diperiksa lagi dari panel`

**Dari agent (tampil di panel dengan label 5.6)**

- T7 settle ditolak karena server sempat tidak terkunci (baru sesudah tinjauan rancangan: "selama atau sesudah", dan tombol Ulangi disebut tidak cukup): `server ini sempat tidak terkunci selama atau sesudah salinan data; karantina tidak diakhiri. Mulai lagi Bangun ulang sebagai replika dari panel; tombol "Ulangi dari tahap ini" tidak cukup, karena salinan data harus diulang.`
- T8a penolakan "tidak disalin dari sumber" sesudah dump penuh, menggantikan T8 lama (baru sesudah tinjauan rancangan); kalimat untuk kelompok yang kosong tidak ditulis: `server ini masih punya database yang tidak disalin dari sumber %s; karantina tidak diakhiri. Yang tidak ada di master baru (%s): jalankan di server ini sudo dbpanel agent drop-database <nama> untuk tiap nama, lalu tekan "Ulangi dari tahap ini" pada pekerjaan di panel. Yang ada di master baru (%s): mulai lagi Bangun ulang sebagai replika dari panel; panel menghapus dan menyalinnya ulang.`
- T8b penolakan yang sama sesudah dump bernama (baru sesudah tinjauan rancangan): `server ini masih punya database yang tidak disalin dari sumber %s; karantina tidak diakhiri. Bila database itu tidak ada di master baru: jalankan di server ini sudo dbpanel agent drop-database <nama> untuk tiap nama, lalu tekan "Ulangi dari tahap ini" pada pekerjaan di panel. Bila ada di master baru: mulai lagi Bangun ulang sebagai replika dari panel; panel menghapus dan menyalinnya ulang. Database itu: %s.`
- T10 `dumpFrom` menolak akun muat yang tidak bisa menulis selagi `read_only` menyala (baru sesudah tinjauan rancangan): `akun MySQL %s yang dipakai agent tidak punya hak CONNECTION_ADMIN maupun SUPER, jadi tidak bisa memuat data selagi server ini baca-saja (read_only); tidak ada yang diubah. Di server yang dikarantina jalankan: sudo dbpanel agent grant-admin; di server lain beri hak itu sebagai root MySQL. Sesudah itu tekan "Ulangi dari tahap ini" pada pekerjaan di panel.`
- T11 daftar database master baru tidak terbaca sesudah muat (baru sesudah tinjauan rancangan): `salinan selesai dimuat, tetapi daftar database master baru tidak bisa dibaca sesudahnya, jadi salinan ini tidak dihitung; tekan "Ulangi dari tahap ini"`
- T12 pekerjaan agent mendapati kunci berkas dipegang perintah CLI (baru sesudah tinjauan rancangan): `kunci baca-saja server ini sedang dibuka sebuah perintah dbpanel agent yang dijalankan di server itu; tunggu sampai perintah itu selesai, lalu tekan "Ulangi dari tahap ini"`
- T13 akhir pemeriksaan "Server replika khusus" pada server yang dikarantina (baru sesudah tinjauan rancangan): `Untuk tiap database itu jalankan di server ini: sudo dbpanel agent drop-database <nama> (perintah itu membuat cadangan dulu), lalu tekan "Ulangi dari tahap ini".`
- T14 pemeriksaan "Hak akses akun admin" pada server yang dikarantina (baru sesudah tinjauan rancangan): `akun %s belum punya semua hak yang dibutuhkan. Jalankan di server ini: sudo dbpanel agent grant-admin, lalu tekan "Ulangi dari tahap ini" pada pekerjaan di panel.`
- T15 `mysql.connect` di server yang dikarantina, akun kurang hak (baru sesudah tinjauan rancangan: menunjuk `reset-admin`, bukan `grant-admin`): `server ini dikarantina (quarantined): akun %s belum punya semua hak yang dibutuhkan, jadi tidak disimpan. Jalankan di server ini: sudo dbpanel agent reset-admin (membuat akun dbpanel_admin dan menyimpannya sebagai akun agent), lalu klik Segarkan di halaman server ini. Hubungkan MySQL tidak perlu diulang.`
- T16 daftar tidak bisa dipastikan lengkap, akun kurang hak: `daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL %s yang dipakai agent tidak punya hak SHOW DATABASES. Jalankan di server ini: sudo dbpanel agent grant-admin, lalu tekan "Ulangi dari tahap ini" pada pekerjaan di panel.`
- T17 daftar tidak bisa dipastikan lengkap, nama akun tidak didukung: `daftar database di server ini tidak bisa dipastikan lengkap: nama akun MySQL yang dipakai agent memuat karakter yang tidak didukung pemeriksaan ini (yang didukung: huruf, angka, garis bawah, titik, tanda hubung, dan tanda dolar). Jalankan di server ini: sudo dbpanel agent reset-admin, lalu tekan "Ulangi dari tahap ini" pada pekerjaan di panel.`
- T18 sumber kanal replikasi bukan sumber yang dipin: `sumber kanal replikasi di server ini (%s) bukan sumber yang dipin (%s); perintah %s ditolak`
- T19 baris peringatan di log agent saat program dijalankan root: `tidak bisa menjalankan sebagai akun layanan MySQL; dijalankan sebagai root`
- T19b galat bila akun layanan sudah mencapai batas jumlah proses: `akun layanan MySQL sudah mencapai batas jumlah proses, jadi program ini tidak bisa dijalankan sebagai akun itu dan tidak dijalankan sebagai root; periksa proses milik akun itu di server ini, lalu ulangi`
- T19c galat bila pemilik direktori tidak bisa dibaca: `pemilik direktori <dir> tidak bisa dibaca (<sebab>), jadi program ini tidak dijalankan; periksa direktori itu di server ini, lalu ulangi`

**Keluaran perintah CLI di server**

- C1 peran bukan karantina: `perintah ini hanya untuk server yang dikarantina (quarantined); peran server ini: <peran>`
- C2 bendera tidak `1 1` dan kunci berkas bebas: `kunci baca-saja server ini sedang tidak terpasang penuh; tunggu beberapa detik (agent memasangnya lagi) lalu ulangi`
- C3 kunci berkas sedang dipegang: `kunci baca-saja server ini sedang dibuka pekerjaan lain (misalnya bangun ulang yang sedang menyalin data); coba lagi sesudah pekerjaan itu selesai`
- C4 berkas kunci tidak lulus pemeriksaan (baru sesudah tinjauan rancangan); sebabnya salah satu dari "bukan berkas biasa", "bukan milik root", "bisa dibaca atau ditulis pengguna lain", "punya lebih dari satu nama": `berkas kunci <berkas> tidak aman dipakai (<sebab>); tidak ada yang diubah. Agent membuat ulang berkas itu saat membutuhkannya; bila pesan ini tetap muncul, mulai ulang agent lalu jalankan perintah ini lagi.`
- C5 permintaan kata sandi: `Kata sandi root MySQL: `
- C5b permintaan kata sandi dengan `--pengguna <nama>` (baru sesudah tinjauan rancangan): `Kata sandi akun MySQL <nama>: `
- C6 berkas cadangan sudah ada (baru sesudah tinjauan rancangan): `berkas cadangan <berkas> sudah ada; tidak ada yang ditimpa dan tidak ada yang dihapus. Jalankan perintah ini lagi.`
- C7 peringatan folder `--ke` (baru sesudah tinjauan rancangan): `Peringatan: folder <folder> bukan milik root atau bisa ditulis pengguna lain; cadangan di sana bisa diubah atau dihapus pengguna lain.`
- C8 cadangan gagal dibuat atau tidak lulus pemeriksaan (baru sesudah tinjauan rancangan): `Cadangan database <nama> tidak bisa dibuat (<sebab>); tidak ada yang dihapus. Bila cadangan tetap tidak bisa dibuat, jalan yang tersisa: Hapus server ini dari panel, lalu pasang lagi sebagai replika baru.`
- C9 cara melihat di master baru (baru sesudah tinjauan rancangan): `Periksa dulu di master baru (hanya membaca): mysql -e 'SHOW DATABASES LIKE "<nama>"'. Jawabannya harus kosong.`
- C9b sama, untuk nama yang memuat karakter di luar huruf, angka, garis bawah, titik, tanda hubung dan tanda dolar (baru sesudah tinjauan rancangan): `Periksa dulu di master baru (hanya membaca): mysql -e 'SELECT schema_name FROM information_schema.schemata WHERE HEX(schema_name) = "<HEX nama>"'. Jawabannya harus kosong.`
- C10 konfirmasi `drop-database`, selalu: `Agent tidak bisa memastikan database ini tidak ada di master baru. Bila database ini ada di master baru, menghapusnya di sini merusak replikasi server ini. Ketik nama database itu untuk melanjutkan, atau apa saja untuk membatalkan: `
- C11 keadaan berubah selama operator membaca (baru sesudah tinjauan rancangan): `Keadaan server ini berubah selama Anda membaca; tidak ada yang diubah. Jalankan perintah ini lagi.`
- C12 `DROP DATABASE` menunggu sesi lain lebih dari 60 detik (baru sesudah tinjauan rancangan): `Database <nama> sedang dipakai sambungan lain, jadi belum dihapus. Kunci baca-saja sudah terpasang lagi. Hentikan aplikasi atau sesi yang masih memakai database itu, lalu jalankan perintah ini lagi. Cadangan tadi tetap ada di <berkas>.`
- C13 perintah dihentikan sinyal selagi kunci terbuka (baru sesudah tinjauan rancangan): `Perintah dihentikan (sinyal); kunci baca-saja server ini sudah terpasang lagi. Jalankan perintah ini lagi bila pekerjaannya belum selesai.`
- C14 batas 10 menit (baru sesudah tinjauan rancangan): `Perintah ini sudah membuka kunci baca-saja selama 10 menit dan dihentikan; kunci baca-saja sudah terpasang lagi. Jalankan perintah ini lagi.`
- C15 `drop-database` selesai: `Selesai. Tekan "Ulangi dari tahap ini" pada pekerjaan di panel, atau mulai lagi Bangun ulang sebagai replika.`
- C16 `grant-admin` tanpa akun tersimpan: `agent belum punya akun MySQL tersimpan; jalankan sudo dbpanel agent reset-admin`
- C17 `grant-admin`, login akun tersimpan ditolak (baru sesudah tinjauan rancangan): `akun MySQL yang disimpan agent tidak bisa masuk; jalankan sudo dbpanel agent reset-admin`
- C18 `grant-admin`, tidak ada yang kurang: `Akun <akun> sudah punya semua hak yang dibutuhkan.`
- C19 `reset-admin` selesai: `Akun dbpanel_admin@localhost dibuat ulang dan disimpan sebagai akun agent. Di panel, klik Segarkan di halaman server ini.`

**README**

- T27 kalimat pembuka kedua bagian yang ditulis ulang (baru sesudah tinjauan rancangan): `Perintah ini ada sejak versi <versi>; bila agent menjawab perintah tidak dikenal, perbarui agent dulu (Pasang ulang agent di halaman server).`

Tata letak layar ketiga perintah CLI (ringkasan sebelum konfirmasi, baris
"kunci dipasang lagi" di akhir) ditulis pelaksana menurut pola `approve`
dan dikumpulkan di laporan untuk dibaca pemilik. Teks opsi
`--tanpa-cadangan` ada di bagian 7 (disetujui pemilik 2026-11-17).

### 5.9 Kompatibilitas

- Agent baru, panel lama: kejadian "dikunci lagi" dan "tidak bisa
  memeriksa" dikirim sebagai hasil start `late`, yang sudah dikenal panel
  lama; panel lama membuka peringatan `master_start_late` dengan teks
  lamanya ("sempat bisa ditulis saat menyala; jalankan Periksa data
  tertinggal"). Tindakannya sama. Keterangan tambahannya adalah field
  `omitempty` yang diabaikan panel lama. Catatan kejadian itu tidak
  menyentuh entri lain, jadi panel lama tidak melihat peringatan "kunci
  start dilewati" tertutup karenanya.
- Panel baru, agent lama: tidak ada kejadian "dikunci lagi"; perilaku
  lama. Penolakan agent lama masih mengutip dua judul bagian README dan
  menyarankan `GRANT`: kedua judul itu tetap ada di README, dan kedua
  frasa serta jangkar akun lama tetap dipertahankan sensor panel, selama
  satu rilis. Bagian README yang ditulis ulang menyuruh menjalankan
  subperintah yang belum dimiliki agent lama; karena itu masing-masing
  dibuka dengan kalimat T27.
- Catatan muat tetap di memori: agent yang restart di antara muat dan
  settle tetap mengharuskan muat diulang, seperti sekarang.
- Tidak ada migrasi basis data panel selain penormalan baris lama (5.1).
- Direktori `/var/lib/dbpanel/agent/` dibuat agent atau perintah CLI saat
  pertama dibutuhkan; pemasang tidak perlu diubah.

### 5.10 Verifikasi

- Tugas pertama: tinjauan rancangan ini oleh model terkuat yang tersedia,
  sebelum ada kode (selesai; hasilnya di bagian 10).
- Tugas kedua: pengukuran M1–M4 (selesai, bukti di rencana) dan
  pengukuran putaran kedua X2, X4, X5, X6, X7 (selesai, bukti di
  berkasnya sendiri, bagian 3).
- Test agent dengan mysqld sungguhan: bendera dimatikan dari luar →
  terkunci lagi dalam satu putaran dan hasilnya terkirim; penjaga yang
  tidak bisa masuk tiga putaran → laporan "tidak bisa memeriksa"; muat
  dump tidak dikunci penjaga; aplikasi tanpa `CONNECTION_ADMIN` ditolak
  menulis selama muat; database yang dibuat di tengah muat membuat settle
  menolak; muat yang gagal membuang catatan muat sebelumnya; CLONE
  melepas kunci berkas; bangun ulang dengan dump penuh yang mengganti
  akun admin berakhir `1 1` tanpa laporan "dikunci lagi"; pernyataan
  penjaga di belakang `LOCK TABLES … WRITE` berhenti dengan 1205 dan
  tidak berlaku belakangan; ketiga perintah CLI pada server
  `super_read_only`, termasuk cadangan terpotong, berkas sudah ada, galat
  di tengah (akhir selalu `1 1`), sinyal di tengah, sesi lain yang
  menahan `DROP`, keadaan yang berubah selama konfirmasi, nama yang harus
  selalu diketik, dan penjaga tidak mengunci di tengahnya.
- Test panel: setiap baris tabel 5.1 (peran, baris, tanda, apa yang
  ditawarkan, apa yang diterima); satu test atas setiap jalur yang
  menggagalkan bangun ulang; `Resume`; penormalan baris lama; kalimat
  kotak untuk agent yang melaporkan peran lain; dialog Ulangi dengan
  peringatan data tertinggal; promote kedua; label di setiap tempat teks
  agent tampil, termasuk jawaban agent yang memakai kode milik panel.
- Test e2e: bangun ulang gagal sebelum salinan → Periksa data tertinggal
  bekerja dan menemukan transaksi → dialog "Ulangi dari tahap ini"
  menyebutnya → Ulangi masih tersedia dan menyelesaikan bangun ulang;
  gagal sesudah settle → replika gagal biasa.
- Test penjaga dan kunci berkas: perintah CLI dan muat data tidak pernah
  saling memasang bendera; CLI menolak selama muat; penjaga tidak
  mencatat "dikunci lagi" untuk pembukaan yang sah, juga pada peralihan;
  `read_only` yang dimatikan dari luar selama muat dipasang lagi dan
  dicatat; berkas kunci yang salah mode, symlink, atau hard link ditolak.
- Audit keamanan menyeluruh dengan model terkuat dilakukan di akhir
  Rencana 5b-2b, mencakup seluruh mode ketat termasuk hasil rencana ini.
  Rencana ini sendiri ditutup dengan tinjauan akhir seluruh cabang
  seperti biasa.

## 6. Yang diserahkan kepada pelaksana

- Nama fungsi, berkas, field dan kunci JSON; bentuk hasil "dikunci lagi"
  di dalam mekanisme hasil start. (Letak dan mode berkas kunci penjaga
  **tidak** lagi diserahkan: lihat 5.2.)
- Cara membaca kata sandi tanpa menampilkannya, tanpa menambah dependensi.
- Program kecil pembaca ekor log dan batasnya.
- Tata letak keluaran ketiga perintah CLI.
- Pembagian menjadi task (rencana pelaksanaan).

## 7. Keputusan penulis yang perlu dilihat pemilik

Butir 12–19 baru sesudah tinjauan rancangan; butir 1–11 sudah dilihat
pemilik, dengan perubahan yang disebut di tiap butir.

1. **"Batalkan percobaan bangun ulang" dihapus.** Tombol itu baru
   dibuat di 5b-1. Dengan aturan 5.1 semua yang ditawarkannya (Hapus,
   Periksa, Bangun ulang) tersedia langsung, dan "Ulangi" tidak hilang.
2. **Promote terencana ditolak selama master lama sebelumnya belum
   dibereskan** (5.1). Alasannya: promote kedua membuat data tertinggal
   master lama pertama tidak bisa lagi diperiksa lewat panel. Promote
   darurat tidak terkena.
3. **Tiga perintah baru di server, bukan tombol di panel** (bagian 4).
   Blok SQL dengan tangan di dua bagian README dihapus.
4. **Perintah itu bisa meminta kata sandi root MySQL** di terminal bila
   masuk lewat socket ditolak. Ini untuk MySQL yang tidak dipasang
   dbpanel (misalnya dari aaPanel), yang root-nya memakai kata sandi.
   Sesudah tinjauan: ada opsi `--pengguna <nama>` untuk server yang akun
   administratornya bukan `root@localhost`, dan kata sandi tidak pernah
   ditulis ke berkas.
5. **Cadangan dari `drop-database` disimpan di server itu**
   (`/var/lib/dbpanel/dibuang/`) dan tidak pernah dihapus otomatis.
   Perintah menampilkan cara menyalinnya ke server lain, tetapi tidak
   memaksanya.
6. **Penjaga mengunci lagi tanpa bertanya.** Admin yang sengaja membuka
   `super_read_only` di master lama dengan tangan akan mendapati server
   terkunci lagi dalam 5 detik dan satu peringatan Kritis di panel. Cara
   yang benar untuk membuka master lama tetap `set-role`.
7. **Serangan balapan pada bukti daftar lengkap (SEC-204) tidak ditutup
   habis** (bagian 8). Penutupan penuhnya (cek silang dengan direktori
   data) bisa menolak bangun ulang yang sah di server dengan folder asing
   di direktori data MySQL, jadi tidak diambil.
8. **Rencana 5b-2 dipecah dua:** 5b-2a (ini) lalu 5b-2b (spek 5.7–5.11
   dan audit menyeluruh).
9. **Periksa dan Amankan data tertinggal tidak menghapus "Ulangi dari
   tahap ini"** pada bangun ulang yang gagal (5.1). Sesudah tinjauan:
   dialog Ulangi dan Setujui menampilkan peringatan data tertinggal yang
   sama dengan form Bangun ulang, supaya yang ditemukan Periksa tidak
   terhapus tanpa disebut.
10. **Tidak ada jenis peringatan baru.** Kejadian "dikunci lagi" memakai
    peringatan `master_start_late` yang ada dengan pesan lain; labelnya
    berubah menjadi "Master lama sempat bisa ditulis".
11. **Bila penjaga mengunci lagi di tengah bangun ulang yang sedang
    berjalan, bangun ulang tidak dihentikan.** Peringatannya terbuka, dan
    form bangun ulang sudah memperingatkan hal yang sama untuk start yang
    terlambat. Menghentikannya otomatis bisa menjebak operator di keadaan
    tanpa jalan (Periksa ditolak sesudah salinan mulai). Sesudah
    tinjauan: bila kejadian itu terjadi selama atau sesudah salinan,
    agent menolak mengakhiri karantina (T7) dan salinan harus diulang.
12. **Peran tetap "dikarantina" sampai agent mengakhiri karantina**
    (5.1; bagian 4, pendekatan B'). Yang terlihat: bangun ulang master
    lama yang sedang berjalan tampil di kotak "Master lama" (kalimat T22
    dengan tautan ke pekerjaannya) dan di halaman pekerjaannya, **bukan**
    sebagai kartu replika "sedang dibuat" di dasbor, halaman replikasi
    dan halaman master. Statusnya selama itu "Master lama; dikarantina".
13. **`drop-database` selalu meminta nama database diketik** dan selalu
    menampilkan cara melihat di master baru (C9, C10). Jalan pendek `ya`
    untuk nama yang "dicatat agent" tidak dibangun: agent tidak tahu
    cukup untuk berkata "cadangan ini satu-satunya salinannya".
14. **Tidak ada pemulihan otomatis dari laporan agent.** Bila jawaban
    settle hilang di jalan (panel "dikarantina", agent sudah "replica"),
    kotak menampilkan kalimat T3 atau T3b dan operator menekan "Ulangi
    dari tahap ini" (satu klik). Panel tidak pernah mengubah peran karena
    sebuah laporan.
15. **Tujuan 2 berbunyi lain, dengan satu teks peringatan baru (T1b).**
    Penjaga yang tidak bisa masuk ke MySQL atau tidak bisa memasang kunci
    lagi selama tiga putaran kini melapor ("tidak bisa memeriksa"),
    dengan peringatan Kritis yang menyuruh `reset-admin`. Sebelumnya
    keadaan itu senyap. Pengecualian: jendela sesudah muat mengganti akun
    sampai operator menjalankan "Hubungkan MySQL" (bagian 8).
16. **Perintah di server tidak menunggu tanpa batas.** `DROP DATABASE`
    menunggu sesi lain paling lama 60 detik lalu berhenti dengan saran
    (C12); Ctrl-C dan putusnya SSH ditangkap dan kunci dipasang lagi
    (C13); jendela terbuka paling lama 10 menit (C14); keadaan diperiksa
    ulang sesudah operator menjawab (C11).
17. **`mysqlbinlog` dan pembaca log dijalankan sebagai pemilik direktori
    yang dibacanya**, bukan sebagai akun pada opsi `--user`. Bila
    pemiliknya root (misalnya log galat di `/var/log` pada RHEL), agent
    berjalan seperti sekarang dengan satu baris peringatan.
18. **`read_only` tidak pernah dimatikan agent**, juga selama CLONE (M1).
    Akibatnya akun muat tanpa `CONNECTION_ADMIN` maupun SUPER ditolak
    sebelum ada yang berubah (T10), di server mana pun yang `read_only`-nya
    menyala.
19. **Teks yang berubah atau baru sejak persetujuan:** T1b, T2b, T3, T3b,
    T7, T8a, T8b, T10–T15, T22–T27, C4, C5b, C6–C9b, C11–C14, C17
    (bagian 5.8). Yang hilang: T8 lama, kalimat "agent mencatat database
    ini tidak disalin dari master baru", dan teks `mysql.connect` yang
    menunjuk `grant-admin`.

**Opsi `--tanpa-cadangan` untuk `drop-database` (disetujui pemilik
2026-11-17).** Tanpa opsi ini, database yang tidak bisa dicadangkan
(tabel yang tidak terbaca `mysqldump`, misalnya di master lama yang mati
mendadak) tidak punya jalan lain selain Hapus server (teks C8), karena
bangun ulang menolak selama database itu ada. Batasnya:

- Opsi tidak ditulis di teks bantuan; ia hanya disebut di teks cadangan
  yang gagal, yang lalu berbunyi (menggantikan C8) — P1: `Cadangan database <nama> tidak bisa dibuat (<sebab>); tidak ada yang dihapus. Bila database itu memang tidak diperlukan lagi, jalankan: sudo dbpanel agent drop-database <nama> --tanpa-cadangan`
- Dengan opsi itu perintah tetap mencoba membuat cadangan. Bila berhasil,
  jalannya biasa. Bila gagal, perintah tidak berhenti tetapi bertanya —
  P2: `Cadangan database <nama> tidak bisa dibuat (<sebab>). Dengan --tanpa-cadangan database itu dihapus TANPA cadangan dan tidak bisa dikembalikan. Ketik nama database itu untuk melanjutkan, atau apa saja untuk membatalkan: `
- Selebihnya sama: cara melihat di master baru ditampilkan, keadaan
  diperiksa ulang sesudah konfirmasi, baris syslog mencatat "tanpa
  cadangan".
- Opsi ini hanya untuk cadangan yang sudah dicoba dan gagal. Yang masih
  punya jalan yang menyimpan cadangan tetap ditolak juga dengan opsi
  ini: ruang kosong folder cadangan tidak cukup (pakai `--ke`), nama
  berkas cadangan sudah ada (jalankan lagi), folder tidak bisa dipakai,
  dan perintah yang dihentikan sinyal selagi mencadangkan.

## 8. Risiko yang tersisa setelah 5b-2a

- **SEC-204, dipersempit.** Pemegang SUPER di master lama yang membuka
  `super_read_only`, mengubah hak akun agent, dan menutupnya lagi di
  antara dua putaran penjaga (di bawah 5 detik), tepat saat settle, masih
  bisa membuat daftar database tidak lengkap. Akibatnya replika menyimpan
  database lama; tidak ada data hilang. Bila penjaga sempat melihatnya,
  atau bendera masih mati saat settle membacanya sendiri, settle ditolak
  dan peringatan terbuka.
- **Tulisan ke database yang disalin, selama muat, oleh akun
  ber-`CONNECTION_ADMIN` atau SUPER** (misalnya aplikasi yang masuk
  sebagai root MySQL) tetap tidak terdeteksi: database-nya memang dibuat
  oleh muat. Yang menahannya hanya konfirmasi "Sudah saya alihkan".
  (SEC-212, sisa SEC-225)
- **Gabungan dua daftar master baru** juga memuat nama yang dibuat di
  master baru sesudah `mysqldump` membaca daftarnya; database bernama
  sama yang dibuat di master lama selama muat lalu dihitung "disalin".
  Dan daftar itu selengkap pandangan akun dump (M2).
- **CLONE** mengganti seluruh data, jadi tulisan selama CLONE hilang,
  bukan tertinggal. Sesudah restart CLONE penerima menyala dengan nilai
  tersimpannya sendiri (X6). **Tidak diukur:** penerima yang benderanya
  hanya dipasang `SET GLOBAL` (tidak tersimpan), dan restart oleh systemd
  sungguhan (pengukuran memakai mysqld tanpa supervisor).
- **MySQL 8.4 tidak diukur** (X3): hanya 8.0.46 yang terpasang di
  lingkungan pengukuran; semua hasil M1–M4 dan X2–X7 berlaku untuk seri
  itu. Bila di seri lain sebuah pemakai ternyata butuh `read_only` mati,
  pemakai itu gagal dengan ERROR 1290 pada pernyataan pertamanya; yang
  tidak tertangkap pemeriksaan T10 adalah muat dump yang gagal sesudah
  log biner di-reset.
- **Pemulihan di aliran muat hanya untuk dump penuh.** Dump bernama tidak
  mengganti akun, jadi tidak terkena X7; benderanya dipasang lagi pada
  sambungan yang disimpan, yang bertahan menganggur sampai 8 jam
  (`wait_timeout` 28800): muat bernama yang lebih lama dari itu berakhir
  dengan pemulihan yang gagal, penjaga yang mengunci lagi, dan T7. Jalur
  CLONE dengan akun yang diganti tidak diukur untuk sambungan yang
  disimpan (CLONE mematikan semua sesi; restart yang memasang bendera).
- **Pekerjaan agent lain yang masih membuka saat muat dump penuh
  berakhir** mendapati `super_read_only` sudah dipasang lagi oleh penutup
  aliran muat dan gagal dengan ERROR 1290 pada pernyataannya; pekerjaan
  itu diulang. (Tumpang-tindih seperti itu tidak terjadi dalam urutan
  langkah bangun ulang panel.)
- **Pernyataan penjaga menunggu di belakang `LOCK TABLES … WRITE` dan
  tulisan yang sedang berjalan**, juga milik sesi berhak; selama itu
  server belum terkunci, dan sesudah tiga putaran penjaga melapor "tidak
  bisa memeriksa" (T1b). Transaksi yang sekadar terbuka tidak menahan
  penjaga; `COMMIT`-nya gagal.
- **Catatan muat di memori:** agent restart di antara muat dan settle
  mengharuskan muat diulang.
- **Perintah yang dimatikan paksa (SIGKILL) atau server yang kehilangan
  daya di antara membuka dan memasang kunci:** penjaga mengunci lagi,
  peringatan Kritis terbuka, dan sesudah muat salinan harus diulang (T7).
  Sama bila pemulihan terakhir sebuah pekerjaan agent gagal.
- **Root yang memegang kunci berkas lama** (perintah yang macet di luar
  batas 10 menitnya sendiri, atau proses lain milik root) membuat
  `super_read_only` mati tanpa laporan selama itu; `read_only` tetap
  dijaga.
- **Akun yang bisa mematikan `read_only`** bisa membuat setiap bangun
  ulang berakhir di T7. Akun itu memang sudah bisa menulis ke server.
- **Laporan start dari restart yang terjadi selama sebuah pembukaan**
  tertunda sampai pembukaan itu berakhir.
- **Sesudah muat mengganti akun, sampai "Hubungkan MySQL":** penjaga
  tidak bisa masuk dan tidak melapor (jendela itu jalur biasa, 5.2).
  Settle tetap membaca bendera sendiri sebelum mengakhiri karantina.
- **Kejadian "dikunci lagi" yang dicatat selama bangun ulang dan belum
  dilihat panel** ikut terhapus bersama berkas hasil start saat settle.
- **Jawaban settle yang hilang:** sampai operator menekan "Ulangi dari
  tahap ini", monitor menilai server itu sebagai master lama yang
  agentnya tidak lagi melaporkan hasil start.
- **Sesudah promote darurat kedua**, master lama pertama hanya bisa
  dihapus dari panel; data tertinggalnya hanya bisa diambil dengan tangan.
- **Dikunci lagi di tengah bangun ulang sebelum salinan mulai:** tulisan
  yang masuk di jendela itu terhapus saat salinan menggantikan data,
  kecuali operator menghentikan bangun ulang dan menjalankan Periksa
  lebih dulu. Peringatannya terbuka; bangun ulang tidak dihentikan
  otomatis (keputusan 7.11).
- **Skema sistem tidak dibandingkan saat karantina diakhiri** (SEC-213)
  dan **hak lewat peran (role) tidak dihitung bukti daftar lengkap**
  (SEC-214, gagal tertutup): keduanya menurut rancangan, tidak diubah.
- **Bila pemilik direktori itu root**, `mysqlbinlog` dan pembaca log
  tetap dijalankan root seperti sekarang (5.5), juga bila direktori
  milik root itu bisa ditulis grup atau pengguna lain. Proses yang
  dijalankan sebagai pemilik direktori tidak membawa grup tambahan akun
  itu, tetapi membawa grup direktori itu walau akun itu bukan anggotanya
  (Debian: `/var/log/mysql` milik `mysql:adm`). RHEL tidak diverifikasi
  di mesin sungguhan (M3).
- **Akun layanan bisa menggagalkan Periksa/Amankan dan pembacaan log
  galat** (memenuhi batas prosesnya, memindahkan direktori, atau
  menghentikan program yang berjalan atas namanya); ia tidak bisa
  membuat root yang menjalankannya. Pembacaan log galat yang ditahan
  begitu memegang kunci penjaga paling lama 7 detik tiap putaran.
- **Kata sandi root MySQL yang diketik** dikirim apa adanya ke apa pun
  yang mendengarkan di jalur socket, dan akun layanan menguasai direktori
  socket itu selagi mysqld mati.
- **Root di server melewati semuanya**, termasuk kunci berkas penjaga.
  Batas kepercayaan yang sama dengan agent sendiri.
- **"Melepas kunci start dengan tangan"** tetap prosedur dengan tangan.

## 9. Keputusan penulis setelah builder check

Builder check menemukan lima pertentangan dan enam pertanyaan. Tidak ada
yang hanya bisa dijawab pemilik produk; semuanya diputuskan penulis dan
sudah dimasukkan ke bagian di atas.

1. **Daftar dari aliran dump bisa dipalsukan** lewat isi routine. Diganti
   daftar yang dibaca klien `mysql` dari master baru (F11, bagian 4, 5.3).
2. **Klaim kompatibilitas panel lama salah** (panel lama mengabaikan hasil
   yang tidak dikenal). Kejadian "dikunci lagi" kini dikirim sebagai hasil
   `late` yang sudah dikenal (5.2, 5.9), dan jenis peringatan baru
   dibatalkan (7.10).
3. **"Periksa lalu Ulangi" tidak mungkin** karena job Periksa menggantikan
   job bangun ulang. Pemeriksaan itu dilonggarkan untuk Periksa dan
   Amankan saja (5.1, 7.9).
4. **Menolak bila uid tidak ketemu berarti menolak penyelamatan.** Diganti
   pencarian sempit dan jalan-seperti-sekarang bila gagal (5.5).
5. **Berkas penanda saja tidak menutup celah** antara CLI dan penjaga, dan
   dua pembukaan bisa saling memasang bendera. Diganti satu kunci berkas
   (`flock`) dan pembukaan yang dihitung (bagian 4, 5.2).
6. **Apa yang dikecualikan sebuah pembukaan:** hanya `super_read_only`;
   penjaga tetap menjaga `read_only` selama muat (5.2).
7. **Dikunci lagi saat bangun ulang berjalan:** peringatan terbuka juga
   untuk server yang sedang dibangun ulang; bangun ulang tidak dihentikan
   (5.1, 7.11, bagian 8).
8. **Apa yang menahan settle:** setiap kali penjaga mendapati bendera mati
   tanpa pembukaan sah sesudah muat selesai, lewat jalur mana pun (5.2).
9. **`drop-database` atas database yang ternyata disalin:** konfirmasi
   lebih berat (mengetik nama) bila nama itu tidak ada di daftar yang
   dicatat agent, dan kalimatnya tidak lagi mengklaim "tidak ada di server
   lain" tanpa dasar (5.4).
10. **Kesegaran laporan pin:** tiga laporan berturut-turut sesudah job
    berakhir (5.1).
11. **SEC-210** masuk (5.7); **SEC-213 dan SEC-214** dicatat sebagai
    menurut rancangan (bagian 8).

## 10. Perubahan sesudah tinjauan rancangan

Tinjauan rancangan (`docs/superpowers/ledgers/2026-11-14-05b2a-design-review.md`)
menghasilkan 30 temuan (R-1 … R-30) dan empat penyederhanaan (F-1 … F-4).
Semuanya diterima; beberapa dalam bentuk yang lebih kecil. Di bawah: apa
yang berubah dan di bagian mana. Bagian 9 dibiarkan sebagai catatan; yang
kini berbunyi lain disebut di akhir bagian ini.

| Temuan | Yang berubah | Bagian |
|---|---|---|
| R-1 | Pembukaan punya dua akhir: `end` dan `abandon`; jalur CLONE melepas kunci berkas sesudah restart tanpa menyentuh bendera | 3 (F9), 5.2 |
| R-2 | Berkas kunci di `/var/lib/dbpanel/agent/` (0700, root), berkas 0600; setiap pembuka memeriksa deskriptornya; CLI menolak berkas yang tidak lulus (C4) | 3 (F19), 5.2, 5.8, 6 |
| R-3 | Satu aturan penjaga (menulis dan melapor hanya selagi memegang kunci berkas dan tanpa tanda muat) dan urutan putaran yang tetap; laporan start selama pembukaan tertunda | 3 (F8), 5.2, 8 |
| R-4 | Pengukuran putaran kedua X2, X4–X7 ditambahkan; X1 sudah diukur; X3 (8.4) tidak diukur. Mode "matikan keduanya" dihapus dari gerbang, penjaga dan laporan; `dumpFrom` menolak akun muat yang lemah (T10) | 3, 4, 5.2, 5.3, 7.18, 8 |
| R-5 | Tujuan 2 berbunyi lain; penjaga yang tidak bisa memeriksa tiga putaran melapor (`unverified`, T1b); jendela sesudah akun diganti muat dikecualikan | 2, 3 (F8, F22), 5.2, 5.8, 7.15, 8 |
| R-6 | Muat baru membuang catatan muat lama; settle membaca bendera sendiri; `PERSIST` gagal sesudah `1 1` tidak dihitung | 5.2 |
| R-7 | Catatan "dikunci lagi" hanya mengisi entri `late`, tidak menilai entri lain; id dari waktu; setiap kejadian dicatat, hanya pengiriman dibatasi | 3 (F18), 5.2, 5.9 |
| R-8 | `lock_wait_timeout` 60 detik; sinyal ditangkap selagi kunci terbuka; batas 10 menit (C12, C13, C14) | 4, 5.4, 5.8, 7.16, 8 |
| R-9 | Pemeriksaan ulang sesudah konfirmasi di bawah kunci; pemulihan memasang yang terbaca (C11) | 5.4, 5.8 |
| R-10, F-3 | Tidak ada berkas "tidak disalin"; nama selalu diketik; cara melihat di master baru selalu ditampilkan (C9, C9b, C10); T8 menjadi T8a/T8b; pemeriksaan "Server replika khusus" menunjuk perintah (T13) | 3 (F20), 4, 5.3, 5.4, 5.8, 7.13 |
| R-11 | Baris `CREATE TABLE` dihitung di luar blok `DELIMITER` dan "paling sedikit"; deskriptor disimpan, `fsync` sebelum bertanya; peringatan `--ke` (C7); teks cadangan gagal (C8, digantikan P1 sejak opsi `--tanpa-cadangan` disetujui pemilik 2026-11-17) | 5.4, 5.8, 7 (blok bertanda) |
| R-12 | Penolakan `mysql.connect` menunjuk `reset-admin` (T15) | 3 (F21), 5.4, 5.8 |
| R-13 | `grant-admin` memakai kumpulan hak replika dan nama akun dari `CURRENT_USER()` (C17); pemeriksaan "Hak akses akun admin" di server yang dikarantina tanpa `GRANT` (T14) | 3 (F21), 5.4, 5.8 |
| R-14, F-2 | Uid = pemilik direktori yang dibaca; root = seperti sekarang dengan peringatan; jalan balik ke root hanya bila proses tidak bisa dimulai | 3 (F12), 4, 5.5, 7.17, 8 |
| R-15, F-1 | Peran tetap `quarantined` sampai settle; `AddReplica`/`RemoveReplica` tidak mengubahnya; settle satu transaksi; tidak ada perubahan peran di gagal/Resume/Ulangi/Setujui; setiap jalur gagal meninggalkan baris gagal | 3 (F1, F2), 4, 5.1, 7.12 |
| R-16 | Dialog Ulangi dan Setujui menampilkan peringatan data tertinggal milik form | 3 (F17), 5.1, 5.10, 7.9 |
| R-17, R-19, F-4 | Tidak ada pemulihan dari laporan, tidak ada panggilan balik monitor; kalimat kotak T3/T3b untuk setiap peran lain | 3 (F16), 4, 5.1, 5.8, 7.14, 8 |
| R-18 | Asal galat dari tipe pembungkus hub, bukan dari kode | 3 (F7), 5.6 |
| R-20 | Sesudah promote darurat kedua: halaman replika (T23), peringatan (T25, T26), nama kandidat (T24). Butir tentang alasan penolakan Ulangi gugur oleh F-1: peran tetap `quarantined`, jadi alasan yang keluar memang "master topologi ini sudah berganti" | 5.1, 5.8 |
| R-21 | Pembuka berikutnya mengirim pernyataan pembuka lagi; pemulihan pada sambungan yang disimpan; kunci dilepas walau pemulihan gagal; berkas kunci tidak diwariskan ke proses anak. Butir tentang hitungan mode "keduanya" gugur bersama mode itu | 5.2 |
| R-22 | Kunci dicoba sebelum bendera diperiksa, diulang sampai 3 detik; dua teks sibuk (C3 untuk CLI, T12 untuk pekerjaan agent) | 5.2, 5.4, 5.8 |
| R-23 | Kata sandi tidak ditulis ke berkas; satu pembaca untuk kata sandi dan konfirmasi; risiko socket dicatat | 5.4, 7.4, 8 |
| R-24 | Nama dari MySQL dicetak tanpa karakter kendali dan dikutip di syslog | 5.4 |
| R-25 | Pernyataan pada satu sambungan (bukan kumpulan sambungan); opsi `--pengguna <nama>` (C5b) | 5.4, 5.8, 7.4 |
| R-26 | Daftar kedua dicoba tiga kali; gagal → langkah salinan gagal dengan T11; risiko gabungan daftar dicatat | 5.3, 5.8, 8 |
| R-27 | T7 dan T2b berbunyi lain; buka-tutup peringatan dalam keadaan `CopyStarted` dicatat sebagai disengaja | 5.2, 5.8 |
| R-28 | Dua judul README dan dua frasa sensor dipertahankan satu rilis; kalimat pembuka T27; kasus "panel baru, agent lama" | 5.4, 5.8, 5.9 |
| R-29 | Hanya pembukuan rencana | — |
| R-30 | `job.cancel` usaha terbaik sebelum hapus dan sebelum bangun ulang baru | 5.1 |

Sesudah tinjauan, pengukuran putaran kedua (bagian 3; bukti di
`docs/superpowers/ledgers/2026-11-14-05b2a-measurements-2.md`) mengubah
atau meneguhkan butir berikut:

| Sumber | Yang berubah | Bagian |
|---|---|---|
| pengukuran putaran kedua, X7 | "Pemulihan pada sambungan yang disimpan" (R-21) tidak cukup sesudah dump penuh mengganti akun: penutup aliran muat dump penuh kini memasang `super_read_only` sebelum `FLUSH PRIVILEGES`; pemulihan membaca bendera dulu dan tidak mengirim apa pun bila sudah menyala; sambungan yang disimpan menyetel `wait_timeout` 28800 | 3, 5.2, 5.3, 5.10, 8 |
| pengukuran putaran kedua, X4 | Penjaga membatasi pernyataannya di server (`lock_wait_timeout` 10 detik); galat 1205 dihitung "tidak bisa mengunci" untuk aturan tiga putaran (R-5); batas waktu klien tidak dipakai sebagai batas | 3, 5.2, 5.10, 8 |
| pengukuran putaran kedua, X2 | Pemeriksaan akun muat (T10) diteguhkan dan berlaku kapan pun `read_only` menyala di server tujuan, bukan hanya di server yang dikarantina; SUPER saja cukup | 3, 5.3 |
| pengukuran putaran kedua, X5 | Bukti kelengkapan cadangan hanya kode keluar 0 dan baris `-- Dump completed`; hitungan `CREATE TABLE` "paling sedikit" dan hanya pemeriksaan kewajaran; nama ber-`'` atau ber-`"` adalah kasus "cadangan tidak bisa dibuat" | 3, 5.4 |
| pengukuran putaran kedua, X6 | `abandon` (R-1) diteguhkan: penerima CLONE menyala dengan nilai tersimpannya sendiri; yang tidak diukur dicatat | 3, 5.2, 8 |
| pengukuran putaran kedua, X3 | MySQL 8.4 tidak diukur | 3, 8 |

Butir bagian 9 yang kini berbunyi lain:

- 9.4 ("pencarian sempit" uid): diganti pemilik direktori (5.5).
- 9.6 ("apa yang dikecualikan sebuah pembukaan"): tetap hanya
  `super_read_only`, kini tanpa pengecualian untuk CLONE (5.2, 5.3).
- 9.8 ("apa yang menahan settle"): ditambah pembacaan bendera oleh settle
  sendiri dan laporan "tidak bisa memeriksa"; dihitung sejak muat dimulai
  (5.2).
- 9.9 (konfirmasi `drop-database`): tidak ada lagi daftar yang dicatat
  agent; nama selalu diketik (5.4).
- 9.10 (kesegaran laporan pin): gugur; tidak ada pemulihan dari laporan
  (5.1).
