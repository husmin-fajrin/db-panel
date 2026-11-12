# Rencana 5b: Mode Ketat, Gerbang Prasyarat, dan Panduan Langkah

Status: draf untuk persetujuan pemilik produk (2026-11-12)
Sumber: `docs/superpowers/security/2026-11-10-inventaris-risiko.md`
(R-001, R-019, R-046, R-109, R-110, R-113, R-114, R-116, R-117, R-121, dan
tabel "Prasyarat yang sebaiknya dipaksa panel"), audit
`docs/superpowers/security/2026-11-12-audit-plan-5a.md` (SEC-85, SEC-86),
pembacaan kode di `main` 2609607, dan percakapan dengan pemilik produk
(bagian 7).

## 1. Maksud dan alasan

Panel ini dipakai tim IT yang kemampuan basis datanya terbatas, dan salah
langkah di sini bisa menghilangkan data rumah sakit. Setelah Rencana 5a,
tiga kelompok risiko masih terbuka:

1. **Panel yang dibobol** bisa membuka kembali master lama yang dikarantina
   (dua master, R-109), atau membuang data sebuah server yang dikarantina
   dan menggantinya dengan salinan dari server penyerang (R-110, R-019).
   Hari ini hanya keamanan host panel yang menahannya.
2. **Salah langkah operator.** Panel tidak memeriksa hal-hal yang
   seharusnya benar sebelum tindakan berbahaya: versi agent, pin master,
   jam, sertifikat, ruang disk, cadangan. Prosedurnya hanya ada di README.
3. **Empat celah agent** yang ditemukan audit 5a: kunci start dibaca salah
   di sebagian instalasi (R-113, R-114), laporan start bisa hilang (R-116),
   pengiriman ke panel menahan penjaga (R-117), dan jalan keluar darurat
   `set-role master` bergantung pada berkas yang bisa rusak (R-121).

Pemilik produk meminta risiko dihilangkan, dengan satu batas tegas: panel
tidak boleh menjadi membingungkan atau merepotkan, karena itu berlawanan
dengan tujuan panel dibangun.

## 2. Tujuan, bukan-tujuan, anti-tujuan

Tujuan:
- Server yang dikarantina hanya bisa keluar dari karantina lewat bangun
  ulang yang selesai, atau lewat perintah root di server itu sendiri.
- Data server yang dikarantina tidak bisa dibuang atau diganti tanpa
  persetujuan root di server itu, dan orang yang menyetujui melihat dari
  mana data pengganti akan diambil.
- Tindakan terencana ditolak bila sebuah prasyarat merah, dengan cara
  memperbaikinya disebut di layar.
- Promote darurat tidak pernah ditolak oleh prasyarat.
- Hasil start master lama yang sempat bisa ditulis selalu sampai ke panel,
  juga bila agent restart.
- Penjaga agent tidak pernah menunggu pengiriman ke panel.
- Staf IT menemukan langkah untuk keadaan darurat di layar tindakan itu
  sendiri, di halaman Panduan, dan di README.

Bukan-tujuan (masuk rencana lain atau sengaja dibiarkan):
- Persetujuan di server untuk promote terencana atau pengarahan ulang
  replika (R-017, R-018). Menutupnya berarti setiap promote terencana butuh
  perintah di server. Data tidak hilang pada kedua jalur itu; penahannya
  tetap keamanan host panel dan "Perketat". Dicatat di bagian 8.
- Akun replikasi per replika dan "Perketat" wajib untuk semua tindakan
  (5c), checksum data (5d), sisa butir Sedang/Rendah (5e).
- Memeriksa isi cadangan atau menjalankan cadangan. Panel hanya melihat
  umur dan ukuran berkas.
- Opsi mysqld yang diberikan lewat baris perintah layanan (`ExecStart`,
  `MYSQLD_OPTS`); itu tetap bagian R-097 (kelas A).

Anti-tujuan (gagal walau "jadi"):
- Promote darurat tertahan karena sebuah prasyarat merah.
- Prasyarat merah tanpa cara memperbaiki yang disebut di layar yang sama.
- Operator harus menjalankan lebih dari satu perintah di server untuk satu
  bangun ulang, atau perintah di server untuk pekerjaan sehari-hari (buat
  replika, Resync, pemantauan, promote terencana).
- Persetujuan yang bisa diberikan lewat panel, lewat akun MySQL, atau lewat
  Telegram. Panel memegang akun MySQL penuh dan token bot, jadi ketiganya
  bisa dipalsukan panel yang dibobol.
- Perintah persetujuan yang tidak menampilkan apa yang disetujui.
- Prasyarat "merah" karena datanya tidak diketahui. Tidak diketahui adalah
  kuning dengan penjelasan, kecuali pin master dan versi agent.

## 3. Fakta yang menentukan desain

Semua dari pembacaan kode di 2609607.

Agent:
- Peran dipin di `/etc/dbpanel/role`: `master`, `replica`, `quarantined`,
  `unassigned` (`pins.go:37-54`). Berkas pin harus milik root dan tidak
  bisa ditulis grup/lain (`pins.go:295-326`); agent membacanya ulang setiap
  perintah.
- `mysql.releaseRole` ditolak pada `master`, tetapi **diizinkan pada
  `quarantined`** (`pins.go:108`, `pins.go:523-535`). Sesudahnya peran
  `unassigned`, penjaga melepas kunci start, dan `mysql.setReadOnly{on:false}`
  atau `mysql.configure{role:master}` diterima. Kode mencatat ini sebagai
  celah yang diketahui (`pins.go:28-35`).
- Pada server `quarantined` dengan pin sumber, `mysql.dropDatabases`,
  `mysql.dumpFrom`, dan `mysql.cloneFrom` dari sumber yang dipin diizinkan
  tanpa tindakan lokal (`pins.go:716-736`).
- Tidak ada catatan yang tersisa di master lama tentang jalur karantinanya.
  Berkas `promote-lock` dihapus oleh setiap jalur karantina
  (`promote_lock.go:767`, `quarantine.go:163`).
- Satu-satunya jalan panel keluar dari `quarantined` selain `releaseRole`
  adalah `settleRebuilt`: peran menjadi `replica` setelah data dimuat dari
  sumber yang dipin dan replikasi diarahkan ke sana
  (`quarantine.go:439-459`). Bukti "sudah dimuat" hanya ada di memori.
- CLI agent tidak berbicara dengan agent yang berjalan. Keduanya berbagi
  berkas di `/etc/dbpanel` dan kunci `flock` (`cli.go`). Sudah ada beberapa
  penolakan "sampai root bertindak di server": `releaseRole` pada master,
  `lock-master`, pin sumber yang hilang, daftar port replika yang penuh.
- `set-role master` memuat `agent.yaml`, memanggil `secrets.LoadOrCreate`
  (yang **membuat** `secrets.key` bila tidak ada), dan membangun penyimpan
  kredensial, hanya untuk melepas kunci start, yang sendiri tidak butuh
  sambungan MySQL (`cli.go:305-334`, `startlock_guard.go:56-104`). Pin
  `source` dan `source-addr` dibiarkan.
- `--print-defaults` dijalankan sebagai root dengan lingkungan agent, dan
  unit agent memakai `ProtectHome=yes` (`startlock.go:480-525`). Pengurai
  hanya mengenali `init_file`, `user`, dan `log_error` setelah `-` diganti
  `_`; awalan `loose_`, `skip_`, dan sejenisnya tidak dikenali.
- Laporan start (`late`, `bypassed`, start gagal) disimpan di memori
  (`pendingStart`, paling banyak 16) dan dikirim di akhir putaran penjaga.
  Setelah agent restart, laporan dihitung ulang dari bendera saat itu, jadi
  bukti `late` hilang (`startlock_guard.go:337-405`, `quarantine.go:309`).
- Pengiriman penjaga berjalan sinkron lewat `Agent.Emit`, sebagian masih
  di dalam `promoteMu` (`promote_lock.go:564-569`). Batas tulis sesi 10
  detik (`session.go:18`). Tidak ada antrean kirim.
- `HealthSnapshot` tidak memuat jam agent, status `performance_schema`,
  atau nama akun MySQL. Kedua sisi mengurai JSON biasa: field tak dikenal
  diabaikan, field hilang bernilai nol. Kebiasaan untuk field baru:
  `omitempty`, dan pointer bila "tidak ada" harus dibedakan dari nol.
- Penolakan pin kembali sebagai `bad_params` dengan teks Indonesia; panel
  tidak bisa membedakannya dari parameter salah.

Panel:
- `agentOutdated` (versi agent berbeda dari panel) hanya dipanggil untuk
  master pada promote terencana dan untuk kandidat
  (`orchestrator/promote.go:217`, `:407`; `promote_view.go:274`). Job lain
  baru tahu saat agent menjawab `unknown_command` di tengah job.
- Tidak ada `Start*` yang memeriksa pin master, TLS, atau disk sebelum
  mulai. Pemeriksaan disk ada di preflight agent, di dalam job.
- Konfirmasi adalah modal sandi yang digerakkan JS (`_confirm.html`,
  `POST /confirm`), bukan layar per tindakan. Hanya promote yang punya
  layar pertimbangan; bangun ulang memakai form buat replika. Beberapa
  tindakan tidak punya konfirmasi sama sekali (tambah database, buat
  replika biasa).
- `Retry`, `Approve`, dan `Resume` tidak melewati pemeriksaan saat mulai
  (`runner.go:783`, `:837`, `:1010`).
- "Hapus server" memanggil `mysql.releaseRole` sebisanya lalu menghapus
  baris server (`orchestrator/controls.go:160-179`).
- Promote darurat sudah meminta nama master lama diketik dan menampilkan
  "Laporan terakhir master" (`promote.html:44-57`).
- Satu-satunya dorongan konfigurasi panel ke agent adalah
  `monitor.configure`, dikirim setiap agent tersambung dan setelah
  Pengaturan disimpan (`monitor/monitor.go:353`). Tidak ada form pengaturan
  per server.
- `ConnectResult.Account` (hasil `CURRENT_USER()`) dikembalikan agent
  tetapi tidak disimpan panel.
- Halaman statis yang ada hanya `/sertifikat`. Menu samping berisi tujuh
  entri tetap (`layout.html:32-38`).

## 4. Pendekatan yang dipertimbangkan

**A. Panel memeriksa prasyarat; agent meminta persetujuan dengan kode
(dipilih).** Gerbang prasyarat satu fungsi di panel, dipakai layar dan
semua `Start*`. Mode ketat di agent: berkas permintaan dan berkas
persetujuan di `/etc/dbpanel`, ditulis CLI root. Kelemahan: gerbang hanya
menahan salah langkah, bukan panel yang dibobol.

**B. Agent juga memeriksa prasyarat.** Lebih kuat terhadap panel yang
dibobol. Ditolak karena agent tidak tahu mana keadaan darurat, sehingga
setiap pengecualian butuh perintah di server; promote darurat jadi lebih
repot tepat saat paling genting, dan pemeriksaannya ditulis dua kali.

**C. Seperti A, persetujuan berupa jendela waktu tanpa kode.** Lebih
sederhana. Ditolak karena selama jendela itu panel yang dibobol bisa
menjalankan tindakan terlindung mana pun, dan operator tidak melihat apa
yang ia setujui.

Pemilik produk menyerahkan pilihan kepada penulis; penulis memilih A.

Satu gagasan dari pemeriksa audit 5a ditolak setelah pembacaan kode:
membebaskan bangun ulang dari persetujuan bila karantina didahului kunci
promosi agent itu sendiri. Kunci promosi dipasang atas perintah panel, dan
kandidatnya hanya perlu tersambung dengan akun replikasi bersama, yang
dipegang panel. Panel yang dibobol bisa menempuh jalur "terencana" dengan
server penyerang sebagai kandidat. Jadi **setiap** bangun ulang server yang
dikarantina butuh persetujuan (bagian 5.2, dan keputusan 7.2).

## 5. Keputusan desain

Pekerjaan dibagi menjadi dua rencana pelaksanaan, dikerjakan berurutan:

- **5b-1: mode ketat dan perbaikan agent** (5.1 sampai 5.6). Menutup butir
  Tinggi lebih dulu.
- **5b-2: gerbang prasyarat dan panduan** (5.7 sampai 5.10).

### 5.1 Karantina tidak bisa dilepas dari panel (R-109)

- Agent menolak `mysql.releaseRole` pada peran `quarantined`, seperti pada
  `master`. Pesannya menyebut perintah lokal yang melepasnya.
- Jalan keluar dari `quarantined` tinggal dua: bangun ulang yang selesai
  (`settleRebuilt`), atau `sudo dbpanel agent set-role master|unassigned`
  di server itu.
- "Hapus server" pada server yang dikarantina tidak lagi memanggil
  `releaseRole`. Baris server dihapus dari panel; server tetap dikarantina
  dan terkunci. Dialog hapus mengatakannya dan menampilkan perintah untuk
  memakai server itu lagi (`set-role unassigned`, atau `set-role master`
  bila server itu akan menjadi master lagi). Ini berlaku apa pun versi
  agentnya.
- Untuk peran `replica` dan `unassigned`, `releaseRole` tidak berubah.

### 5.2 Persetujuan root untuk bangun ulang server yang dikarantina (R-110, R-019)

Yang dilindungi: pada server berperan `quarantined`, agent menolak
`mysql.dropDatabases`, `mysql.dumpFrom`, dan `mysql.cloneFrom` kecuali ada
persetujuan yang berlaku. Pelaksana memeriksa daftar perintah agent dan
menambahkan perintah lain yang bisa membuang atau mengganti data tabel pada
server yang dikarantina.

Berkas (di `/etc/dbpanel`, aturan kepercayaan sama dengan berkas pin: milik
root, tidak bisa ditulis grup/lain, ditulis atomik):

- **Permintaan** `rebuild-request`: `{kode, sumber, dibuat}`. Kode enam
  angka dari `crypto/rand`. Berlaku 30 menit. Dibuat agent, tidak pernah
  dari isi yang dikirim panel: `sumber` adalah pin sumber server itu saat
  permintaan dibuat.
- **Persetujuan** `rebuild-approval`: `{sumber, disetujui, berlaku_sampai}`.
  Berlaku 24 jam. Hanya ditulis CLI.

Persetujuan berlaku bila ketiganya benar: peran `quarantined`; `sumber`
sama dengan pin sumber saat perintah datang; waktu sekarang belum melewati
`berlaku_sampai` dan `disetujui` tidak lebih dari 5 menit di masa depan.
Persetujuan dihapus saat `settleRebuilt` berhasil dan oleh setiap
`set-role`.

Perintah agent baru `mysql.rebuildApproval` (tanpa parameter): menjawab
`{disetujui, sisa_detik, kode, sumber}`. Bila belum ada persetujuan dan
belum ada permintaan yang berlaku, agent membuat permintaan; selama
permintaan berlaku, kodenya tetap sama. Perintah terlindung yang ditolak
juga membuat permintaan, supaya panel lama pun menampilkan perintahnya.

Penolakan memakai kode galat baru (`needs_local_approval`), bukan
`bad_params`, dan pesannya memuat perintah lengkap.

CLI baru:

- `sudo dbpanel agent approve <kode>`
  - Menolak bila tidak ada permintaan yang berlaku atau kodenya salah.
  - Menampilkan, sebelum bertanya: nama host server ini; sumber
    (`host:port`) yang datanya akan disalin; kalimat bahwa **semua data
    MySQL di server ini dibuang** dan diganti salinan dari sumber itu;
    permintaan untuk memastikan sumber itu memang master baru; dan
    pengingat menjalankan "Periksa data tertinggal" dulu bila promote tadi
    darurat.
  - Meminta ketik `ya`. Jawaban lain membatalkan.
  - Menulis persetujuan, menghapus permintaan.
- `sudo dbpanel agent approve batal`: menghapus persetujuan dan permintaan.

Panel:

- Form bangun ulang master lama menampilkan kotak "Persetujuan di server":
  belum atau sudah disetujui (dengan sisa waktu), perintah untuk disalin,
  satu kalimat alasan, dan tombol "Periksa lagi". Panel mendapatkannya dari
  `mysql.rebuildApproval`.
- `StartCreateReplica` untuk bangun ulang menolak bila belum disetujui.
  `Retry` dan `Approve` pada job bangun ulang memeriksanya lagi.
- Bila persetujuan habis di tengah job, langkahnya gagal dengan pesan yang
  memuat perintah; "Ulangi" berjalan setelah disetujui lagi.
- Agent yang tidak mengenal `mysql.rebuildApproval`: bangun ulang ditolak
  dengan "Agent perlu dimutakhirkan".
- Entri audit bangun ulang memuat bahwa persetujuan lokal ada dan umurnya.
  Agent mencatat ke log saat pertama memakai sebuah persetujuan.

Yang sengaja tidak dilindungi persetujuan: `mysql.quarantine` itu sendiri.
Panel yang dibobol masih bisa membuat master sehat baca-saja; itu gangguan
layanan, bukan kehilangan data, dan `set-role master` memulihkannya
(Panduan, 5.10).

### 5.3 `set-role` yang bisa diandalkan saat berkas rusak (R-121)

- `set-role master` melepas kunci start tanpa memuat `secrets.key` atau
  `mysql.cred`, dan tidak pernah membuat `secrets.key`.
- Bila `agent.yaml` tidak terbaca, ia tetap mencoba dengan tata letak MySQL
  bawaan, dan menyebut dengan jelas apakah kunci start dilepas, tidak
  ditemukan, atau tidak bisa diperiksa. Peran tidak diubah bila pelepasan
  gagal (seperti sekarang).
- `set-role master` juga menghapus pin `source` dan `source-addr`. Setiap
  `set-role` menghapus berkas permintaan dan persetujuan.
- Setelah berhasil, CLI mencetak langkah berikutnya: MySQL masih baca-saja,
  dan cara membukanya.
- Panduan memuat langkah manual melepas kunci start (hapus baris
  `init_file` dan berkasnya) untuk keadaan ketika binary agent sendiri
  tidak bisa dijalankan.

### 5.4 Kunci start dibaca sesuai lingkungan layanan MySQL (R-113, R-114)

- `--print-defaults` dijalankan dengan `HOME` akun layanan (akun dari opsi
  `user`, bawaan `mysql`). Bila direktori home itu ada tetapi tidak
  terlihat oleh agent (`ProtectHome`), kunci dilaporkan tidak terpasang
  dengan alasan yang menyebut sebabnya.
- Pengurai mengenali awalan opsi yang diterima mysqld (`loose`, `skip`,
  `disable`, `enable`, `maximum`). Setiap argumen yang, setelah awalan
  dibuang, bernama `init_file` dan bukan baris agent sebagai yang terakhir
  membuat kunci dilaporkan tidak terpasang.
- Sebelum menulis kode, pelaksana membuktikan dengan mysqld sungguhan apa
  yang dicetak `--print-defaults` dan apa yang dilakukan mysqld untuk
  `loose-init-file`, `skip-init-file`, dan `~/.my.cnf` akun layanan.
  Hasilnya dicatat di rencana pelaksanaan, seperti "Bukti profil keamanan
  mysqld" di Rencana 5a. Bila sebuah bentuk terbukti tidak berpengaruh,
  ia tetap diperlakukan sebagai tidak terkunci (gagal-tertutup); alasannya
  ditulis.

### 5.5 Hasil start yang tidak bisa hilang (R-116)

- Agent menyimpan **hasil start terburuk sejak karantina** di berkas
  (`/var/lib/dbpanel`), ditulis saat hasil diputuskan, sebelum dikirim.
  Urutan terburuk: `late`, start gagal, `bypassed`, `ok`.
- `HealthSnapshot` membawa hasil itu di setiap laporan selama server
  berperan `quarantined`: hasil, umur relatif (bukan jam dinding), dan
  `init_file` yang dipakai. Field baru, `omitempty`, pointer.
- Panel membuka `master_start_late` (dan peringatan bangun ulang) dari
  isi snapshot, bukan hanya dari kejadian. Kejadian yang ada tetap dikirim
  untuk kecepatan dan untuk panel lama; panel tidak membuka peringatan dua
  kali.
- Berkas dihapus saat server keluar dari `quarantined`.

### 5.6 Pengiriman di luar jalur penjaga (R-117)

- Penjaga tidak pernah memanggil pengiriman secara langsung. Ia menaruh
  kejadian ke antrean terbatas; satu goroutine pengirim mengurasnya.
- Tidak ada pengiriman yang berjalan sambil memegang `promoteMu`.
- Antrean menyimpan kejadian sampai terkirim, berurutan. Bila penuh,
  kejadian tertua yang nilainya sudah dibawa snapshot (hasil start) dibuang
  lebih dulu.
- Uji: dengan soket panel yang tidak mengalir, jarak antarputaran penjaga
  tetap `GuardEvery` ditambah waktu kerja putaran itu sendiri.

### 5.7 Gerbang prasyarat (R-046 dan tabel prasyarat)

Satu fungsi di orchestrator menilai daftar baris untuk sebuah tindakan dan
topologinya. Tiap baris punya: kunci, label, status (hijau, kuning, merah),
penjelasan, dan cara memperbaiki. Fungsi yang sama dipakai untuk tampilan
dan untuk keputusan.

Di mana ditegakkan:
- Semua `Start*` di bawah menolak bila ada baris merah, sebelum ada yang
  diubah dan sebelum token konfirmasi dipakai.
- `Retry` dan `Approve` menilai ulang hanya bila langkah yang tersisa masih
  membuang data. Promote yang sudah melewati titik tidak-bisa-batal tidak
  pernah ditahan.
- Job otomatis (`RenewMySQLTLS`) dan pemasangan sertifikat tidak digerbang.

Di mana ditampilkan:
- Layar promote dan form buat replika/bangun ulang menampilkan daftarnya.
- Untuk tindakan yang hanya punya modal sandi, baris merah ditampilkan
  sebelum sandi diminta, dan modal tidak meminta sandi selama ada yang
  merah.

Baris merah:

| Kunci | Merah bila | Berlaku untuk | Sumber data |
|---|---|---|---|
| Versi agent | versi agent server yang terlibat berbeda dari panel | semua `Start*` | sudah ada |
| Pin master | master topologi tidak melaporkan pin `master` | promote terencana, buat replika, Resync, tambah database | sudah ada |
| Jam | selisih jam server dengan panel lebih dari 30 detik | promote terencana | field baru: jam agent saat laporan diambil |
| Perketat | ada replika topologi yang belum terverifikasi, atau master tanpa sertifikat | promote terencana, bangun ulang | sudah ada |
| Ruang disk | disk server tujuan atau master di atas ambang kritis Pengaturan | buat replika, bangun ulang, Resync, tambah database | sudah ada |
| Akun panel | akun MySQL agent bernama `root` | semua `Start*` | panel menyimpan `ConnectResult.Account` |
| `performance_schema` | mati di master | buat replika, bangun ulang, Resync, tambah database | field baru |
| Cadangan | folder diisi, dan berkas terbaru lebih tua dari batas, kosong, atau folder tidak terbaca | promote terencana, bangun ulang | 5.8 |
| Persetujuan di server | belum disetujui | bangun ulang | 5.2 |

Baris kuning (tidak menolak):
- Cadangan belum diatur: meminta centang "saya sudah memastikan ada
  cadangan terbaru" pada promote terencana dan bangun ulang; centangnya
  diaudit.
- Port MySQL master bisa dijangkau dari host panel, bila host panel bukan
  master atau replika topologi itu.
- Log audit belum diteruskan ke server lain: kuning sampai
  `systemd-journal-upload` aktif di host panel atau operator menandai
  sudah.
- `panel.key` belum ditandai sudah dicadangkan (tombol di Pengaturan;
  siapa dan kapan diaudit).
- Data sebuah baris merah tidak diketahui (agent offline, field kosong dari
  agent yang versinya sama): kuning dengan penjelasan. Pengecualian: pin
  master dan versi agent, yang merah bila tidak diketahui.

Kompatibilitas: field baru kosong dari agent lama berarti baris itu tidak
dinilai; baris "Versi agent" sudah merah untuk agent itu.

### 5.8 Cadangan

- Tiap server punya dua isian di halaman servernya: "folder cadangan" dan
  "umur paling lama" (bawaan 48 jam). Disimpan di panel, dikirim ke agent
  lewat `monitor.configure`.
- Agent mencari berkas biasa terbaru di folder itu, paling dalam tiga
  tingkat, dengan batas jumlah entri dan batas waktu, paling sering sekali
  tiap lima menit. Ia melaporkan umur dan ukuran berkas itu di snapshot.
  Nama dan isi berkas tidak pernah dikirim.
- Folder harus path absolut. Folder di bawah `/home`, `/root`, atau
  `/run/user` tidak terlihat oleh agent; panel menolaknya saat disimpan
  dan menyebut sebabnya.
- Baris "Cadangan" menilai master topologi saat itu. Setelah promote,
  master baru belum punya folder, sehingga barisnya kuning sampai diisi;
  layar pengalihan dan Panduan menyebut "pindahkan jadwal cadangan".
- Peringatan baru `backup_stale` untuk master yang foldernya diisi dan
  berkas terbarunya melewati batas, lewat mesin peringatan yang ada
  (termasuk Telegram).
- Layar menulis: panel hanya melihat umur dan ukuran berkas, bukan apakah
  cadangan itu bisa dipulihkan.

### 5.9 Promote darurat (R-001)

- Baris prasyarat ditampilkan dengan kalimat bahwa baris merah tidak
  menghalangi promote darurat.
- Centang baru yang wajib: "Saya sudah memastikan master lama mati atau
  terputus dari aplikasi". Tanpa centang, promote darurat ditolak.
- Nama master lama tetap diketik, seperti sekarang.
- Audit promote darurat memuat: centang itu, waktu laporan terakhir master,
  dan kunci setiap baris merah yang dilewati.

### 5.10 Panduan langkah

Isi yang sama di tiga tempat: layar tindakan (langkah sebelum dan sesudah,
tepat di tempatnya), halaman "Panduan" di panel (menu baru, butuh login),
dan README. Pelaksana memilih satu sumber untuk halaman dan README, atau
menambah uji yang gagal bila judul dan perintahnya berbeda.

Panduan yang ditulis:
1. Promote darurat: memastikan master lama mati, apa arti tiap centang,
   apa yang dilakukan sesudahnya.
2. Bangun ulang master lama: Periksa data tertinggal, amankan, alihkan
   aplikasi, `approve`, lalu bangun ulang.
3. Memindahkan jadwal cadangan setelah promote, dan mengisi folder
   cadangan.
4. Memperpanjang sertifikat.
5. Melepas kunci master lama dengan tangan bila agent rusak (5.3).
6. Master terkunci (karantina) padahal tidak ada promote: `set-role master`
   dan apa yang harus diperiksa.
7. Memakai lagi server yang dihapus saat masih dikarantina (5.1).
8. Memperbaiki tiap baris merah, termasuk membuat akun MySQL khusus panel
   dengan perintah SQL lengkap, dan menjalankan "Perketat".
9. Replika yang tersambung di luar alur panel sesaat sebelum master mati
   (catatan spek 5a 5.3).

### 5.11 Perubahan protokol dan kompatibilitas

- `HealthSnapshot`: jam agent saat laporan diambil; status
  `performance_schema`; keadaan cadangan; hasil start terburuk. Semua
  `omitempty`.
- `MonitorConfigureParams`: folder cadangan.
- Perintah baru `mysql.rebuildApproval`; kode galat baru
  `needs_local_approval`.
- Panel baru dengan agent lama: gerbang "Versi agent" menolak tindakan
  terencana; promote darurat tetap jalan. Agent baru dengan panel lama:
  penolakan mode ketat tampil sebagai pesan teks yang memuat perintahnya.
- Migrasi panel: kolom folder dan batas umur cadangan per server, akun
  MySQL di inventaris, penanda `panel.key`, dan penanda penerusan log.

### 5.12 Verifikasi

- Uji agent dengan mysqld sungguhan untuk 5.4, dan untuk 5.2: perintah
  terlindung ditolak tanpa persetujuan, diterima dengan persetujuan,
  ditolak lagi setelah sumber berubah, kedaluwarsa, atau `set-role`.
- Uji bahwa panel tidak punya jalan dari `quarantined` ke bisa-ditulis:
  urutan R-109 (`releaseRole`, lalu `setReadOnly` atau `configure`) ditolak
  di langkah pertama.
- Uji e2e: bangun ulang master lama berhenti di persetujuan, lanjut
  setelah `approve`; hapus server yang dikarantina meninggalkan server itu
  terkunci.
- Uji tiap baris prasyarat: merah menolak `Start*` terencana; promote
  darurat tetap mulai dengan baris yang sama merah, dan auditnya memuat
  baris itu.
- Uji 5.5: agent restart di antara hasil `late` dan pengirimannya; panel
  tetap membuka peringatan.
- Setiap teks baru ditulis dalam bahasa Indonesia dan dikumpulkan di
  laporan untuk dibaca pemilik produk.

## 6. Yang diserahkan kepada pelaksana

- Nama berkas, field, kolom, dan kunci JSON; bentuk antrean kirim; batas
  jumlah entri dan batas waktu pemindaian folder cadangan.
- Tata letak daftar prasyarat dan cara modal menampilkannya.
- Cara mengukur selisih jam dari jam agent, umur laporan, dan jam panel.
- Cara memeriksa port master dari host panel, dan cara mengenali
  `systemd-journal-upload`.
- Pembagian tiap rencana (5b-1, 5b-2) menjadi task.

## 7. Keputusan pemilik produk (percakapan 2026-11-12)

1. Persetujuan di server bisa diterima bila hanya satu perintah
   salin-tempel di master lama, hanya setelah promote.
2. Tindakan terencana ditolak bila ada prasyarat merah; promote darurat
   tetap bisa dilanjutkan dengan mengetik nama server, dengan centang
   "master lama mati atau terputus" yang wajib.
3. Daftar baris merah dan kuning di 5.7 diterima, termasuk "Perketat"
   merah untuk promote terencana dan bangun ulang.
4. Cadangan: rumah sakit pemilik memakai mysqldump berkala dan aaPanel
   (belakangan ke Google Drive); cara rumah sakit lain tidak diketahui.
   Maka centang adalah bawaan, dan folder cadangan adalah pilihan.
5. Tidak ada keadaan darurat lain yang diminta punya panduan.
6. Pendekatan diserahkan kepada penulis (A dipilih).

Keputusan penulis yang perlu dilihat pemilik:

- **7.1** "Hapus server" pada server yang dikarantina tidak memakai kode
  persetujuan. Server dibiarkan terkunci, dan memakainya lagi butuh
  `set-role` di server itu. Ini satu perintah, sama seperti yang
  disepakati, tetapi bentuknya berbeda dari `approve <kode>`.
- **7.2** Bangun ulang butuh persetujuan **juga setelah promote
  terencana**, berbeda dari yang penulis katakan dalam percakapan.
  Alasannya di bagian 4: agent tidak bisa membedakan promote terencana
  yang sungguhan dari yang dipalsukan panel yang dibobol. Bebannya tetap
  satu perintah di master lama per promote.
- **7.3** Persetujuan berlaku 24 jam dan mencakup pengulangan job yang
  sama; kode permintaan berlaku 30 menit.
- **7.4** Selisih jam diukur langsung; status NTP tidak diperiksa.
- **7.5** Peringatan `backup_stale` ditambahkan walau tidak dibahas.
- **7.6** Penerusan log audit boleh ditandai manual oleh operator.

## 8. Risiko yang tersisa setelah 5b

- **R-017, R-018 tetap terbuka.** Panel yang dibobol masih bisa memicu
  promote terencana yang tidak diinginkan atau mengarahkan replika ke
  server lain.
- **Panel yang dibobol masih bisa mengarantina master sehat** (gangguan
  layanan sampai root menjalankan `set-role master`).
- **Persetujuan bergantung pada orang yang membaca.** Operator yang
  mengetik `ya` tanpa membaca sumbernya menyetujui apa pun yang diminta.
  CLI menampilkan sumber itu dengan jelas; lebih dari itu tidak bisa
  dipaksa.
- **Root di server melewati semuanya.** Itu batas kepercayaan yang sama
  dengan agent sendiri.
- **Centang cadangan hanya pengingat.** Folder cadangan hanya membuktikan
  ada berkas baru yang tidak kosong.
- **Prasyarat yang salah merah menahan pekerjaan terencana** sampai
  diperbaiki atau panel diperbarui. Promote darurat tidak terkena. Tidak
  ada tombol untuk melewati gerbang.
- **Instalasi lama perlu tindakan sebelum promote terencana berikutnya:**
  menjalankan "Perketat", dan mengganti akun panel bila masih `root`.
- **Akar R-110 belum tertutup:** akun replikasi bersama (Rencana 5c).
- **Opsi mysqld di baris perintah layanan** tetap tidak terbaca agent
  (R-097).
