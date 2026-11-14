# Rencana 5b-2a: Master Lama dengan Satu Aturan

- Tanggal: 2026-11-14
- Status: rancangan, menunggu persetujuan pemilik produk
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
   dikarantina lagi, tanpa klik apa pun. Hapus, Periksa dan Amankan data
   tertinggal, Bangun ulang, dan "Ulangi dari tahap ini" semuanya bekerja
   dari keadaan itu. Monitor menilainya sebagai master lama.
2. Master lama yang dikarantina tidak bisa tinggal bisa-ditulis lebih dari
   satu putaran penjaga (5 detik) tanpa dikunci lagi, dan setiap kejadian
   itu sampai ke panel sebagai peringatan yang tidak bisa hilang.
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

Semua dibaca dari kode pada `8ce7fc5`.

**Panel**

- F1. `fail()` dan `Resume` tidak mengubah peran: sesudah bangun ulang
  gagal, peran tetap `replica` dengan baris replika gagal dan tanda master
  lama di topologi. Orchestrator sendiri berkomentar "A failed rebuild of
  an old master leaves it quarantined" dan `cancelJob` memang
  mengembalikan peran ke `quarantined`; hanya jalur gagal yang tidak.
- F2. Bendera `QuarantineEnded` pada job sudah ada dan disimpan tepat
  sesudah `mysql.configureReplication` berhasil; tanda master lama dihapus
  saat itu. Tiga keadaan salinan (`CopyNotStarted`, `CopyUncertain`,
  `CopyStarted`) sudah ada.
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
  (`RolePin`) dengan peran di panel kecuali untuk master.
- F6. `SwapMaster` menimpa tanda master lama topologi. Sesudah promote
  kedua, master lama yang pertama kehilangan catatannya: Periksa data
  tertinggal menolak ("bukan lagi master lama sebuah topologi") dan kotak
  tampil tanpa tombol bangun ulang.
- F7. Teks galat agent masuk ke pekerjaan sebagai
  `judul langkah + ": " + teks`, lalu disensor. Pelabel `AgentQuote` sudah
  ada tetapi hanya dipakai di jalur persetujuan, dan memotong di 300
  karakter (teks saran agent lebih panjang dari itu).

**Agent**

- F8. Penjaga berputar tiap 5 detik tetapi membaca bendera baca-saja
  **hanya pada peralihan** (MySQL baru menyala, agent baru mulai, muat
  selesai). Selama MySQL hidup terus, bendera tidak pernah dibaca.
- F9. Agent sendiri mematikan baca-saja di lima tempat (`liftReadOnly`):
  muat dump, CLONE, pasang plugin clone, buang database, buat akun. Hanya
  dua yang pertama meninggalkan tanda; tandanya dipasang **sesudah**
  bendera mati.
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
  sudah ada, baru dipakai untuk `--print-defaults`.
- F13. Berkas `leftover-gtid` tidak mencatat sumbernya dan tidak pernah
  dihapus.
- F14. CLI agent (`approve`, `set-role`, `set-source`) tidak pernah masuk
  ke MySQL. `lock-master`/`unlock-master` masuk dengan akun admin yang
  tersimpan. Jalur masuk sebagai root MySQL lewat socket sudah ada
  (`openWith(socket, root)`, dipakai pemasangan).
- F15. Bukti daftar lengkap dan daftar database dibaca pada satu
  sambungan, berurutan. `GRANT`/`REVOKE` ditolak MySQL selama
  `super_read_only` menyala, juga untuk pemegang SUPER.

**Yang harus diukur dulu di mysqld sungguhan** (tugas pertama rencana;
hasilnya dicatat di rencana dan bisa mengubah butir yang disebut):

- M1. Dengan `read_only=ON`, `super_read_only=OFF`: apakah muat dump,
  `DROP DATABASE`, `CREATE USER`/`GRANT`, dan `INSTALL PLUGIN` berhasil
  untuk akun ber-`CONNECTION_ADMIN` tanpa SUPER? Apakah CLONE butuh
  `read_only` mati? (5.3)
- M2. Apakah akun dump yang dibuat panel melihat **semua** database di
  master baru lewat `information_schema.schemata`, dan bentuk keluaran
  klien `mysql` untuk nama yang di-`HEX`. (5.3)
- M3. Apakah `mysqlbinlog` yang dijalankan sebagai uid akun layanan bisa
  membaca berkas binlog dan indeksnya pada pemasangan Debian dan RHEL
  bawaan. (5.5)
- M4. Bentuk `SHOW GRANTS` di bawah `sql_quote_show_create=OFF`. (5.7)

## 4. Pendekatan yang dipertimbangkan

**Keadaan sesudah bangun ulang gagal**

- A. Biarkan seperti sekarang (peran `replica` + baris gagal) dan
  pertahankan "Batalkan percobaan bangun ulang". Paling kecil. Tetapi
  keadaan bawaannya tetap yang paling berlubang: monitor diam, sertifikat
  dan saran salah sampai operator menekan tombol; dan tombol itu
  menghapus jalan "Ulangi".
- **B (dipilih). Peran kembali `quarantined` saat gagal, baris gagal
  dipertahankan.** Keadaan aman menjadi bawaan. Ini yang sudah dimaksud
  kode (F1) dan yang sudah dilakukan pembatalan. Harganya: peran berubah
  di `fail`, `Resume`, `Retry`, `Approve`; satu pemeriksaan "sudah
  digantikan" dilonggarkan dengan empat syarat persis; monitor membaca
  peran lebih dulu.
- C. Buang baris replika saat gagal (jadi master lama biasa). Sederhana,
  tetapi "Ulangi dari tahap ini" hilang, padahal itu jalan murah yang
  ditunjuk teks agent sesudah `grant-admin` atau `drop-database`.

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
  memeriksanya, menampilkan apa yang akan terjadi, meminta `ya`, membuka
  kunci sesaat, dan menguncinya lagi. Tidak ada jalur panel, tidak ada
  perintah protokol baru.

**Penjaga terus-menerus**

- A. Baca bendera tiap putaran, kunci lagi tanpa peduli siapa yang
  membuka. Berebut dengan muat data agent sendiri (anti-tujuan).
- **B (dipilih). Baca tiap putaran; siapa pun yang membuka baca-saja
  dengan sah memegang satu kunci berkas (`flock`) selama membukanya**:
  pekerjaan agent sendiri maupun perintah CLI di proses lain. Penjaga
  hanya mengunci lagi bila kunci berkas itu bebas, jadi tidak ada celah di
  antara "membaca bendera" dan "mengunci". Kunci berkas dilepas kernel
  bila prosesnya mati.
- C. Hanya peringatan panel dari laporan berkala, tanpa mengunci lagi.
  Lebih kecil, tetapi server tetap bisa ditulis sampai orang bertindak.

**Jendela penyalinan**

- A. Membaca nama database dari aliran dump itu sendiri. Tidak butuh
  apa-apa lagi, tetapi alirannya bisa dipalsukan (F11); ditolak sesudah
  builder check.
- **B (dipilih). Daftar database master baru dibaca dengan klien `mysql`
  memakai kredensial dan TLS yang sama dengan `mysqldump`, sebelum dan
  sesudah dump; dan `read_only` tetap menyala selama muat (bila M1
  mengizinkan).** Tidak ada jalur kredensial baru. Database yang ada di
  server sesudah muat tetapi tidak ada di daftar master baru berarti
  bukan hasil salinan.
- C. Cek silang dengan folder di direktori data MySQL. Folder asing di
  direktori data (misalnya `lost+found`) akan menolak bangun ulang yang
  sah; ditolak.

## 5. Keputusan desain

### 5.1 Keadaan master lama: satu aturan

Empat keadaan, masing-masing satu arti di panel:

| Keadaan | Peran di panel | Baris replika | Tanda master lama | Pin agent |
|---|---|---|---|---|
| Dikarantina (belum dibangun ulang) | `quarantined` | tidak ada | ada | `quarantined` |
| Bangun ulang berjalan | `replica` | `creating` | ada sampai settle | `quarantined` sampai settle |
| **Bangun ulang gagal sebelum settle** | **`quarantined`** | **gagal** | ada | `quarantined` |
| Bangun ulang gagal sesudah settle | `replica` | gagal | sudah dihapus | `replica` |

"Settle" adalah saat agent mengakhiri karantina (jawaban OK
`mysql.configureReplication`; bendera `QuarantineEnded`).

- **Gagal dan lanjut-sesudah-restart.** `fail()` dan `Resume`, untuk job
  bangun ulang master lama yang benderanya belum terpasang, mengembalikan
  peran ke `quarantined` sesudah baris ditandai gagal. Baris
  dipertahankan.
- **Ulangi dan Setujui.** Untuk job semacam itu, `Retry`/`Approve`
  mengembalikan peran ke `replica` bersamaan dengan membuka lagi job; bila
  peluncuran gagal, peran kembali `quarantined`. Pemeriksaan persetujuan
  di server dan pemeriksaan sambungan tetap berjalan lebih dulu, seperti
  sekarang.
- **"Sudah digantikan".** Job bangun ulang **tidak** dianggap sudah
  digantikan hanya karena peran `quarantined`, bila keempat syarat ini
  benar: jenis `create_replica` dengan `RebuildOldMaster`; bendera belum
  terpasang; baris server itu gagal; server itu masih master lama
  topologi job. Dengan empat syarat yang sama, job Periksa dan Amankan
  data tertinggal di server itu **tidak** dihitung sebagai "job yang lebih
  baru": keduanya hanya membaca, dan peringatan panel sendiri menyuruh
  operator menjalankannya. Pemeriksaan lain (master berganti, job jenis
  lain yang lebih baru) tidak berubah.
- **Penolakan.** `refuseAwaitingRebuild` menolak Resync, Lepas, dan Buat
  replika biasa untuk setiap server yang `AwaitingRebuild`, apa pun
  perannya. `AwaitingRebuild` menjadi: peran `quarantined`.
- **Hapus server.** Server berperan `quarantined` boleh dihapus walau
  punya baris gagal; barisnya ikut terhapus. Penjaga lain (pengalihan
  belum dikonfirmasi, promote tertunda) tidak berubah.
- **Kotak "Master lama", form bangun ulang, halaman replika, halaman
  pekerjaan.** Mengikuti peran dan keadaan salinan saja. Dalam keadaan
  gagal: Hapus tampil; Periksa dan Amankan tampil kecuali keadaan salinan
  `CopyStarted`; tautan Bangun ulang tampil; "Ulangi dari tahap ini"
  tampil di pekerjaan selama belum digantikan job lain.
- **Daftar replika** (dasbor, halaman replikasi, halaman master) tidak
  menampilkan server berperan `quarantined` sebagai kartu replika; ia
  tampil lewat kotaknya sendiri.
- **Dihapus:** tindakan "Batalkan percobaan bangun ulang" seluruhnya
  (rute, fungsi store, tombol, tiga kalimat, label audit), lengan kedua
  `AwaitingRebuild`, kalimat "percobaan masih tercatat". Tidak ada lagi
  keadaan yang membutuhkannya.
- **Baris lama.** Saat panel menyala, setiap server berperan `replica`
  dengan baris gagal yang masih master lama topologinya dinormalkan:
  bendera job bangun ulang terbarunya terpasang → tanda dihapus (replika
  gagal biasa); selain itu → peran `quarantined`. Aman dijalankan
  berulang.
- **Panel mengikuti pin agent.** Bila panel mencatat `quarantined` dengan
  baris gagal tetapi agent melaporkan pin `replica` dengan sumber yang
  sama dengan master topologi, artinya jawaban settle hilang di jalan:
  monitor memasang bendera, menghapus tanda, dan mengembalikan peran ke
  `replica` (replika gagal biasa). Syaratnya: tiga laporan berturut-turut
  yang semuanya diterima **sesudah** job bangun ulang itu berakhir, dan
  job itu memang bangun ulang master lama di topologi yang sama. Arah
  sebaliknya tidak pernah dilakukan dari laporan. Untuk server `quarantined` tanpa baris yang agentnya
  melaporkan `replica`, kotak menampilkan satu kalimat (5.8 teks T3).
- **Monitor.** `resolve` membaca peran lebih dulu: peran `quarantined`
  selalu dinilai sebagai master lama, ada baris atau tidak. Hasil start
  dan hasil "dikunci lagi" (5.2) dari agent dinilai juga untuk server
  yang sedang dibangun ulang (peran `replica`, tanda master lama masih
  ada), bukan hanya untuk peran `quarantined`. Lengan khusus untuk "peran
  replika yang belum dinilai" tetap, hanya untuk bangun ulang yang sedang
  berjalan.
- **Promote kedua.** Promote **terencana** ditolak selama topologi itu
  masih punya master lama yang belum dibangun ulang atau dihapus (teks
  T4). Promote **darurat** tidak pernah ditolak; layarnya menampilkan
  peringatan (T5), dan sesudahnya kotak master lama yang pertama
  menampilkan kalimat T6 dengan Hapus sebagai satu-satunya tindakan.

### 5.2 Penjaga membaca kunci baca-saja di setiap putaran

- Setiap putaran (5 detik), untuk peran `quarantined` dengan MySQL
  menjawab, penjaga membaca `super_read_only` dan `read_only`.
- **Satu kunci berkas untuk semua pembukaan yang sah.** Sebuah berkas
  kunci milik root di direktori data agent. Yang membuka baca-saja
  memegang `flock` eksklusif atas berkas itu dari sebelum bendera
  dimatikan sampai sesudah bendera dipasang lagi: pekerjaan agent (muat
  dump, CLONE, pasang plugin, buang database, buat akun) dan perintah CLI
  (5.4).
- **Di dalam agent, pembukaan dihitung.** Pembuka pertama mencatat nilai
  semula dan mematikan bendera; pembuka terakhir memasangnya lagi. Dua
  pekerjaan yang tumpang-tindih tidak lagi saling memasang bendera di
  tengah pekerjaan yang lain.
- **Perintah CLI** mencoba mengambil kunci berkas tanpa menunggu. Bila
  sedang dipegang (misalnya bangun ulang sedang memuat), perintah menolak
  dan menyebut sebabnya. Pekerjaan agent yang mendapati kunci dipegang
  CLI menunggu sebentar lalu gagal dengan teks yang jelas, sebelum
  mengubah apa pun.
- **Penjaga** mencoba mengambil kunci berkas tanpa menunggu.
  - Dapat: tidak ada pembukaan yang sah. Bila ada bendera mati, penjaga
    memasang keduanya lagi (`PERSIST`, seperti sekarang) dan mencatat
    kejadian "dikunci lagi".
  - Tidak dapat: ada pembukaan yang sah. Pembukaan yang sah hanya
    mematikan `super_read_only` (5.3), jadi penjaga tetap memeriksa
    `read_only`: bila mati, ia memasangnya lagi dan mencatat kejadian
    yang sama. Pengecualian: pembukaan di dalam agent yang memang
    mematikan keduanya (CLONE, bila M1 menunjukkan itu perlu); selama itu
    penjaga tidak menyentuh apa pun.
- **Kejadian "dikunci lagi" dicatat sebagai hasil start `late`** dengan
  satu keterangan tambahan ("selagi berjalan"), lewat mekanisme hasil
  start yang ada: ditulis ke berkas sebelum dikirim, dibawa laporan
  berkala, tidak hilang bila agent restart atau panel sedang tidak
  terjangkau.
- **Panel:** tidak ada jenis peringatan baru. Peringatan
  `master_start_late` yang ada dipakai; pesannya mengikuti keterangan itu
  (teks T1 untuk "selagi berjalan"), dan label jenisnya menjadi "Master
  lama sempat bisa ditulis". Ditutup seperti sekarang oleh Periksa data
  tertinggal yang lebih baru atau saat server bukan lagi master lama, dan
  kini juga oleh keadaan salinan `CopyStarted` (alasan T2).
- **`master_start_late` mengikuti keadaan salinan** (SEC-226): pesannya
  tidak lagi menyuruh Periksa bila salinan sudah mulai.
- **Settle.** Agent mengingat (di memori, seumur catatan muat; dihapus
  saat muat berikutnya mulai) bila sesudah muat terakhir selesai penjaga
  mendapati bendera mati tanpa pembukaan yang sah, lewat jalur mana pun:
  putaran biasa, MySQL yang menyala lagi dengan bendera mati, atau upaya
  mengunci yang gagal. Bila ya, `settleRebuilt` menolak (teks T7) sampai
  muat diulang. Pembukaan sah oleh CLI tidak dihitung. Ini, bersama F15,
  membuat serangan pada bukti daftar lengkap (SEC-204) tidak lagi senyap;
  sisanya ada di bagian 8.

### 5.3 Jendela penyalinan

- **`read_only` tetap menyala.** `liftReadOnly` hanya mematikan
  `super_read_only`, di semua pemakainya, kecuali di tempat yang menurut
  M1 memang butuh `read_only` mati (yang diduga: CLONE). Bila M1
  menunjukkan muat dump gagal dengan `read_only` menyala, butir ini batal
  untuk muat dump dan dicatat; butir berikutnya tetap berlaku.
- **Ada di master baru.** Pada dump penuh, agent membaca daftar database
  master baru dua kali dengan klien `mysql` (kredensial dan TLS yang sama
  dengan `mysqldump`, nama di-`HEX` supaya tidak ada yang perlu ditafsir):
  sekali sebelum perubahan lokal apa pun, sekali sesudah dump selesai.
  Catatan muat menyimpan gabungan keduanya. Saat settle, database
  non-sistem yang ada di server tetapi **tidak** ada di gabungan itu
  berarti tidak disalin dari sumber: karantina tidak diakhiri (teks agent
  yang ada, dengan saran baru T8). Aturan lama ("sudah ada sebelum muat"
  dan "bukan yang dinamai") tetap. CLONE tetap dihitung mengganti
  seluruhnya.
- Daftar pertama yang tidak terbaca menggagalkan muat sebelum ada yang
  berubah. Daftar kedua yang tidak terbaca membuat muat tidak dihitung:
  settle menolak dengan teks "salinan harus diulang" yang sudah ada.

### 5.4 Tiga perintah menggantikan SQL dengan tangan

Semua dijalankan root di server, hanya bekerja pada pin `quarantined`
(selain itu menolak dan menyebut sebabnya), masuk ke MySQL sebagai root
lewat socket, dan bila itu ditolak meminta kata sandi root MySQL di
terminal (tidak ditampilkan, tidak disimpan, tidak masuk riwayat shell).
Semua menampilkan apa yang akan dilakukan dan meminta `ya`, seperti
`approve`. Semua menulis satu baris ke syslog. Semua membuka
`super_read_only` hanya selama pernyataannya, dengan `sql_log_bin` mati,
sambil memegang kunci berkas penjaga (5.2), dan selalu memeriksa `1 1` di
akhir; bila bukan, perintah itu sendiri memasangnya lagi dan
mengatakannya. Bila kunci berkas sedang dipegang (bangun ulang sedang
memuat), perintah menolak tanpa mengubah apa pun.

- **`sudo dbpanel agent drop-database <nama>`** untuk "database yang tidak
  ada di master baru".
  1. Menolak skema sistem dan nama yang tidak ada.
  2. Membuat cadangan dengan `mysqldump` ke berkas baru (tidak pernah
     menimpa) di folder cadangan milik root (bawaan
     `/var/lib/dbpanel/dibuang/`, bisa diganti `--ke <folder>`), sesudah
     memeriksa ruang kosong.
  3. Memeriksa cadangan: `mysqldump` keluar 0; berkas diakhiri
     `-- Dump completed on`; memuat `CREATE DATABASE` untuk nama itu;
     jumlah `CREATE TABLE` sama dengan jumlah tabel di database. Gagal
     satu saja: berhenti, tidak ada yang dihapus.
  4. Menampilkan nama database, jumlah tabel, letak, ukuran dan SHA-256
     cadangan, dan contoh menyalin cadangan ke server lain. Lalu meminta
     konfirmasi. Saat agent menolak mengakhiri karantina karena "database
     tidak disalin dari sumber", ia menyimpan daftar nama itu di berkas
     milik root. Bila nama yang diminta ada di daftar itu, perintah
     berkata "agent mencatat database ini tidak disalin dari master baru;
     cadangan ini satu-satunya salinannya" dan meminta `ya`. Bila tidak
     ada di daftar (atau daftarnya tidak ada), perintah berkata bahwa ia
     tidak bisa memastikan database ini tidak ada di master baru, dan
     meminta operator **mengetik nama databasenya** sebagai konfirmasi.
  5. Membuang database itu, memeriksa `1 1`, lalu menyebut langkah
     berikutnya di panel.
- **`sudo dbpanel agent grant-admin`** untuk akun admin yang kurang hak:
  menghitung hak yang kurang pada akun yang tersimpan dengan aturan yang
  sama dengan `mysql.connect` ditambah syarat bukti daftar lengkap,
  menampilkan pernyataan `GRANT`-nya, meminta `ya`, menjalankannya.
- **`sudo dbpanel agent reset-admin`** untuk akun yang namanya tidak
  didukung atau yang rusak: membuat atau menyetel ulang
  `dbpanel_admin@localhost` dengan kata sandi acak dan hak penuh, lalu
  menyimpannya sebagai akun agent (sama seperti pemasangan). Operator
  tidak perlu "Hubungkan MySQL" lagi.
- **Teks yang menunjuk README diganti** menunjuk perintah itu: penolakan
  "masih punya database yang tidak disalin", dua penolakan daftar, dan
  penolakan "kurang hak" `mysql.connect` di server yang dikarantina.
  Spanduk "Akun admin MySQL ini kurang hak" di halaman server yang
  dikarantina menampilkan perintahnya, bukan `GRANT` polos (SEC-229).
- **README:** bagian "Database yang tidak ada di master baru" dan
  "Memberi hak SHOW DATABASES di server yang dikarantina" ditulis ulang
  menjadi satu perintah masing-masing dengan arti keluarannya. Blok SQL
  dengan tangan di kedua bagian itu dihapus.

### 5.5 Program dan berkas milik akun layanan

- `mysqlbinlog` (Periksa data tertinggal, Amankan, dan pembacaan ekor
  binlog saat MySQL mati) dijalankan sebagai uid akun layanan MySQL, juga
  pembacaan berkas indeks binlog. Keluaran tetap ditulis root ke folder
  penyelamatan milik root.
- Ekor log galat dibaca oleh program kecil yang dijalankan sebagai uid
  yang sama, dengan batas ukuran dan batas waktu, bukan oleh root di dalam
  proses.
- Uid itu diambil dengan pencarian sempit: akun pada opsi `--user`
  layanan (bawaan `mysql`), dicari di daftar pengguna sistem. Tidak ada
  syarat tentang direktori home atau `~/.my.cnf`.
- **Penyelamatan tidak pernah ditolak karena ini.** Bila uid tidak bisa
  ditentukan, atau menjalankan sebagai uid itu gagal dijalankan, agent
  berjalan seperti sekarang (sebagai root) dan menulis satu baris
  peringatan di log pekerjaan. Ini berlaku di semua peran.
- Bergantung pada M3; bila M3 menunjukkan pemasangan bawaan tidak
  mengizinkannya, butir ini dicatat sebagai tidak dibangun dan dibawa ke
  pemilik, bukan dipaksa.

### 5.6 Label untuk pesan agent

- Setiap teks yang berasal dari agent dan menjadi galat langkah, galat
  pekerjaan, galat terakhir replika, atau pesan kilat di halaman, tampil
  sebagai `Pesan dari agent <server> (bukan dari panel): <teks>`, satu
  baris, karakter kendali dan penanda arah dibuang, dipotong pada batas
  panjang baris log pekerjaan (bukan 300). Teks buatan panel sendiri
  (batas waktu, agent terputus) tidak dilabeli.
- Baris log pekerjaan yang berasal dari agent diratakan menjadi satu baris
  dan diberi awalan `agent: `.
- Urutan tetap: label, lalu sensor. Aturan sensor untuk frasa panel dan
  akun yang disarankan disesuaikan dengan teks baru (5.4), dan sebuah
  test membaca teks agent langsung dari kodenya sehingga daftar frasa dan
  teks agent tidak bisa berpisah diam-diam (SEC-228).

### 5.7 Butir kecil

- `leftover-gtid` mencatat sumber yang dipakai menghitungnya. Amankan
  menolak bila sumber itu bukan pin sumber sekarang; berkas tanpa catatan
  sumber dihitung tidak ada. Berkas dihapus saat settle. (SEC-201)
- Sesi bukti daftar lengkap menyetel `sql_quote_show_create` dan
  `sql_mode`-nya sendiri, sesuai M4. (SEC-211)
- Teks nama akun yang tidak didukung menyebut karakter yang benar. (R-6)
- `mysql.startReplica` dan `mysql.startUntil` di server yang dikarantina
  menolak bila sumber kanal replikasi yang akan dijalankan bukan sumber
  yang dipin. (SEC-210)
- Lima butir kode SEC-232: penyimpanan `CopySent` yang gagal menghentikan
  langkah sebelum perintah dikirim; `CopySent` dihitung "tidak pasti" apa
  pun nomor promosinya; untuk CLONE bukti adalah progres di atas 0 atau
  hasil; jangkar akun pada sensor memakai awal kalimat agent yang utuh;
  butir tentang tombol Batalkan gugur bersama tombolnya.
- Dua teks panel SEC-234: pesan persetujuan hapus pada bangun ulang master
  lama (T9); kalimat Batalkan gugur bersama tombolnya.
- Empat ketidaktepatan README SEC-233.

### 5.8 Teks baru (untuk dibaca pemilik)

- T1 pesan `master_start_late` untuk "selagi berjalan": `MySQL master lama sempat tidak
  terkunci selagi berjalan; agent sudah menguncinya lagi. Tulisan yang
  masuk selama itu tidak ada di master baru: jalankan Periksa data
  tertinggal`
- T2 alasan tutup: `bangun ulang sudah mulai mengganti data server ini;
  tulisan itu tidak bisa diperiksa lagi`
- T3 kotak, agent melaporkan `replica`: `Agent server ini melaporkan
  karantinanya sudah berakhir (peran replica). Mulai lagi Bangun ulang
  sebagai replika, atau hapus server ini.`
- T4 penolakan promote terencana: `master lama %s dari promote sebelumnya
  belum dibangun ulang atau dihapus; selesaikan itu dulu`
- T5 peringatan layar promote darurat: `Master lama %s dari promote
  sebelumnya belum dibangun ulang. Sesudah promote ini, server itu tidak
  bisa lagi diperiksa atau dibangun ulang dari panel; yang tersisa hanya
  Hapus server.`
- T6 kotak sesudah promote kedua: `Topologi ini sudah promote lagi sejak
  server ini dikarantina. Data tertinggal dan Bangun ulang tidak tersedia
  lagi untuk server ini. Hapus server ini dari panel, lalu pasang lagi
  sebagai replika baru bila masih dipakai.`
- T7 agent, settle sesudah dikunci lagi: `server ini sempat tidak
  terkunci sesudah salinan data selesai; karantina tidak diakhiri. Mulai
  lagi Bangun ulang sebagai replika dari panel.`
- T8 saran pada penolakan "tidak disalin dari sumber": `Jalankan di
  server ini: sudo dbpanel agent drop-database <nama>, lalu tekan "Ulangi
  dari tahap ini" pada pekerjaan di panel.`
- T9 persetujuan hapus pada bangun ulang master lama: `database berikut
  di master lama ini akan dihapus lalu disalin ulang dari master baru: %s`
- Teks layar ketiga perintah CLI ditulis pelaksana menurut pola `approve`
  dan dikumpulkan di laporan untuk dibaca pemilik.

### 5.9 Kompatibilitas

- Agent baru, panel lama: kejadian "dikunci lagi" dikirim sebagai hasil
  start `late`, yang sudah dikenal panel lama; panel lama membuka
  peringatan `master_start_late` dengan teks lamanya ("sempat bisa ditulis
  saat menyala; jalankan Periksa data tertinggal"). Tindakannya sama.
  Keterangan tambahannya adalah field `omitempty` yang diabaikan panel
  lama.
- Panel baru, agent lama: tidak ada kejadian "dikunci lagi"; perilaku
  lama.
- Catatan muat tetap di memori: agent yang restart di antara muat dan
  settle tetap mengharuskan muat diulang, seperti sekarang.
- Tidak ada migrasi basis data panel selain penormalan baris lama (5.1).

### 5.10 Verifikasi

- Tugas pertama: tinjauan rancangan ini oleh model terkuat yang tersedia,
  sebelum ada kode; hasilnya boleh mengubah tugas berikutnya dan dicatat.
- Tugas kedua: pengukuran M1–M4, dicatat sebagai bukti di rencana.
- Test agent dengan mysqld sungguhan: bendera dimatikan dari luar →
  terkunci lagi dalam satu putaran dan hasilnya terkirim; muat dump tidak
  dikunci penjaga; aplikasi tanpa `CONNECTION_ADMIN` ditolak menulis
  selama muat; database yang dibuat di tengah muat membuat settle
  menolak; ketiga perintah CLI pada server `super_read_only`, termasuk
  cadangan terpotong, berkas sudah ada, galat di tengah (akhir selalu
  `1 1`), nama yang tidak ada di daftar agent (harus mengetik nama), dan
  penjaga tidak mengunci di tengahnya.
- Test panel: setiap baris tabel 5.1 (peran, baris, tanda, apa yang
  ditawarkan, apa yang diterima); `Resume`; penormalan baris lama;
  pemulihan dari pin agent; promote kedua; label di setiap tempat teks
  agent tampil.
- Test e2e: bangun ulang gagal sebelum salinan → Periksa data tertinggal
  bekerja → "Ulangi dari tahap ini" masih tersedia dan menyelesaikan
  bangun ulang; gagal sesudah settle → replika gagal biasa.
- Test penjaga dan kunci berkas: perintah CLI dan muat data tidak pernah
  saling memasang bendera; CLI menolak selama muat; penjaga tidak
  mencatat "dikunci lagi" untuk pembukaan yang sah; `read_only` yang
  dimatikan dari luar selama muat dipasang lagi dan dicatat.
- Audit keamanan menyeluruh dengan model terkuat dilakukan di akhir
  Rencana 5b-2b, mencakup seluruh mode ketat termasuk hasil rencana ini.
  Rencana ini sendiri ditutup dengan tinjauan akhir seluruh cabang
  seperti biasa.

## 6. Yang diserahkan kepada pelaksana

- Nama fungsi, berkas, field dan kunci JSON; nama dan letak berkas kunci penjaga;
  bentuk hasil "dikunci lagi" di dalam mekanisme hasil start.
- Cara membaca kata sandi tanpa menampilkannya, tanpa menambah dependensi.
- Program kecil pembaca ekor log dan batasnya.
- Tata letak keluaran ketiga perintah CLI.
- Pembagian menjadi task (rencana pelaksanaan).

## 7. Keputusan penulis yang perlu dilihat pemilik

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
   tahap ini"** pada bangun ulang yang gagal (5.1). Sekarang job apa pun
   yang lebih baru menghapusnya, sehingga operator yang mengikuti saran
   panel (Periksa dulu) terpaksa menyalin ulang dari awal.
10. **Tidak ada jenis peringatan baru.** Kejadian "dikunci lagi" memakai
    peringatan `master_start_late` yang ada dengan pesan lain; labelnya
    berubah menjadi "Master lama sempat bisa ditulis".
11. **Bila penjaga mengunci lagi di tengah bangun ulang yang sedang
    berjalan, bangun ulang tidak dihentikan.** Peringatannya terbuka, dan
    form bangun ulang sudah memperingatkan hal yang sama untuk start yang
    terlambat. Menghentikannya otomatis bisa menjebak operator di keadaan
    tanpa jalan (Periksa ditolak sesudah salinan mulai).

## 8. Risiko yang tersisa setelah 5b-2a

- **SEC-204, dipersempit.** Pemegang SUPER di master lama yang membuka
  `super_read_only`, mengubah hak akun agent, dan menutupnya lagi di
  antara dua putaran penjaga (di bawah 5 detik), tepat saat settle, masih
  bisa membuat daftar database tidak lengkap. Akibatnya replika menyimpan
  database lama; tidak ada data hilang. Bila penjaga sempat melihatnya,
  settle ditolak dan peringatan terbuka.
- **Tulisan ke database yang disalin, selama muat, oleh akun
  ber-`CONNECTION_ADMIN` atau SUPER** (misalnya aplikasi yang masuk
  sebagai root MySQL) tetap tidak terdeteksi: database-nya memang dibuat
  oleh muat. Yang menahannya hanya konfirmasi "Sudah saya alihkan".
  (SEC-212, sisa SEC-225)
- **CLONE** mungkin tetap membuka `read_only` (M1); ia mengganti seluruh
  data, jadi tulisan selama CLONE hilang, bukan tertinggal.
- **Catatan muat di memori:** agent restart di antara muat dan settle
  mengharuskan muat diulang.
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
- **Bila uid akun layanan tidak bisa ditentukan**, `mysqlbinlog` tetap
  dijalankan root seperti sekarang (5.5).
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
