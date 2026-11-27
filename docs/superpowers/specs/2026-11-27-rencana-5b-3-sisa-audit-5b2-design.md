# Rencana 5b-3: Sisa Audit Rencana 5b-2

- Tanggal: 2026-11-27
- Status: menunggu persetujuan pemilik
- Asal: tabel temuan terbuka audit Rencana 5b-2
  (`docs/superpowers/security/2026-11-24-audit-plan-5b2.md`, disebut "A5b2" di
  bawah) dan baris R-181 sampai R-205 inventaris risiko
  (`docs/superpowers/security/2026-11-10-inventaris-risiko.md`). Keputusan
  pemilik: 2026-11-25 (semua temuan terbuka masuk rencana berikutnya, kelompok
  paling awal), 2026-11-26 ("Kerjakan"), 2026-11-27 (jendela kata sandi,
  pilihan 2).
- Dibangun di atas: `main` pada `8caee75` (versi 0.2.0), yaitu hasil Rencana
  4, 5a, 5b-1, 5b-2a dan 5b-2b. Dua kalimat spek lama ikut berubah: Rencana 4
  §5.1 (pernyataan buka kunci master, lihat 5.4) dan Rencana 5b §5.7 (batas
  percobaan bersama dan daftar tindakan yang dikonfirmasi, lihat 5.1).
- Yang **tidak** ada di sini: akun replikasi per replika dan pemeriksaan
  `set-source` terhadap identitas master (Rencana 5c); halaman ganti kata
  sandi (R-042); retensi log audit (K6 Rencana 5b-2b).

## 1. Maksud dan alasan

Audit Rencana 5b-2 meninggalkan 25 baris risiko terbuka. Tidak ada yang
menghalangi merge 0.2.0, tetapi delapan di antaranya bernilai Tinggi di
inventaris (dua master, data hilang, atau kredensial bocor), dan beberapa lagi
bisa memperpanjang gangguan: login salah dari luar memblokir konfirmasi kata
sandi admin; sesi tanpa kata sandi bisa mengganti agent sebuah server;
"Ulangi" promote berjalan tanpa satu pun pemeriksaan saat mulai; master yang
dibuka dengan `unlock-master` bisa menjadi baca-saja lagi sesudah restart,
berminggu kemudian, sementara panel menampilkannya sehat.

Rencana ini menutup ke-25 baris itu. Tiga hal ditimbang di setiap keputusan,
menurut pesan pemilik: keamanan dan data lebih dulu; panel tidak boleh menjadi
merepotkan untuk operator yang bukan ahli basis data; dan perbaikan yang
memperpanjang gangguan adalah risiko juga. Karena itu setiap langkah baru
untuk operator punya alasan tertulis (tabel di bagian 2), dan tiga hal sengaja
tidak dibangun (bagian 8).

Pelajaran yang mengikat rencana ini adalah regresi N1 (A5b2 SEC-280):
perbaikan "Ulangi" yang dibuat terburu-buru meninggalkan master baca-saja
tanpa tombol di panel. Setiap bagian 5.2 diperiksa terhadap kasus itu.

## 2. Tujuan, bukan-tujuan, anti-tujuan

**Tujuan**

1. Lalu lintas tanpa login tidak bisa memblokir konfirmasi kata sandi sesi
   yang sudah login, maupun login dari browser yang pernah login (R-181).
2. Tindakan yang mengganti agent, mengalihkan sumber salinan, atau mengganggu
   layanan meminta kata sandi panel; untuk tindakan ringan kata sandi diminta
   sekali dan berlaku 10 menit (R-182, R-183, R-191).
3. "Ulangi" promote tidak lagi melewati pemeriksaan saat mulai, dan master
   tidak pernah tertinggal baca-saja tanpa tombol di panel (R-184).
4. Karantina master lama yang tertunda mendarat sendiri, dan satu agent yang
   diam tidak menahan server lain (R-185).
5. Master yang dibuka tetap bisa ditulis sesudah restart MySQL, terukur pada
   tiga jenis server; master yang sudah terkena terlihat di panel dan bisa
   diperbaiki dari sana (R-188).
6. Perintah root yang berbahaya bertanya dulu; persetujuan bangun ulang
   berakhir bersama percobaannya; penjaga kunci promote membaca replika di
   alamat yang tercatat (R-187, R-203, R-202).
7. Panel yang dibobol tidak bisa membuat agent menampung permintaan tanpa
   batas atau menghentikan putaran pemeriksaannya (R-186, R-200, R-201).
8. Gerbang tidak menilai dari laporan basi, dan penolakan promote menyebut
   jalan keluarnya (R-197, R-198, R-199).
9. Header lebih ketat, sesi yang berakhir benar-benar berakhir, dan journal,
   tabel serta memori panel tidak bisa ditumbuhkan dari luar (R-189, R-193,
   R-194).
10. Teks operator berkata benar tentang keadaan baru (R-190, R-192, R-195,
    R-196); test yang kadang gagal diperbaiki dan sisa kecil ditutup (R-204,
    R-205).

**Bukan-tujuan**

- Promote darurat yang melewati replika lain yang melapor bisa ditulis
  (R-198): tidak dibangun, lihat bagian 8.
- TLS 1.3 saja di port UI, HSTS `includeSubDomains`,
  `require-trusted-types-for` dan `report-to` (R-189): tidak dibangun.
- `MemoryMax` pada unit agent (R-186): tidak dibangun.
- Kata sandi untuk Pengaturan, **Batalkan**, **Stop replika** dan **Sudah saya
  alihkan** (5.1).
- Halaman ganti kata sandi, retensi log audit, akun replikasi per replika.

**Anti-tujuan** (kalau ini terjadi, rencana gagal walau test lulus)

- Master tertinggal baca-saja tanpa tombol di panel yang bisa membukanya atau
  menyebut perintah root yang membukanya.
- Perbaikan yang membuat dua master lebih mungkin: gerbang atau kata sandi
  yang menahan promote setengah jalan sehingga master lama lebih lama tidak
  dikarantina; buka kunci yang lebih longgar dari pemeriksaan agent sekarang.
- Permintaan kata sandi yang bisa diblokir lalu lintas dari luar.
- Langkah baru di jalur gangguan yang tidak tercatat di tabel di bawah.
- Agent atau panel baru yang membuat pasangan 0.2.0-nya atau keadaan tersimpan
  berhenti bekerja tanpa langkah pembaruan yang disebut (5.12).

**Yang dikerjakan operator lebih dari sekarang**

| # | Apa | Kapan | Mengapa |
|---|---|---|---|
| 1 | Kata sandi panel, sekali per 10 menit | Buat token pasang ulang agent; ubah alamat master untuk replika; setujui restart MySQL master; Siapkan sebagai master pertama; Hubungkan MySQL yang mengganti akun tersimpan; "Ulangi" promote terencana sebelum master diganti | Sesi yang dicuri atau ditinggal tidak boleh bisa melakukannya (R-182, R-183, R-184, R-191) |
| 2 | "Ulangi" promote terencana sebelum master diganti: daftar Prasyarat dibaca lagi, centang cadangan bila diminta | Sesudah promote terencana gagal | Sama dengan memulainya (R-184) |
| 3 | Promote darurat baru (centang, nama, kata sandi), bukan satu klik "Ulangi" | Promote darurat yang gagal sebelum master baru dibuka, termasuk yang "selesai sebagian" dengan kandidat masih baca-saja | Satu klik berhari-hari kemudian bisa membuat dua master (SEC-288). Sudah dilihat pemilik |
| 4 | Promote terencana baru, bukan "Ulangi" | "Ulangi" lebih dari 24 jam sesudah konfirmasi terakhir | Aturan yang sama dengan pekerjaan yang membuang data |
| 5 | Tombol **Buka kunci promote** (tanpa kata sandi), bukan "Ulangi" | Promote terencana gagal dan master masih terkunci | Membuka kunci tidak lagi menjalankan promote. Jumlah klik sama atau kurang |
| 6 | Mengetik `ya` pada `set-role` | Meninggalkan peran master; masuk karantina; replika dijadikan master | Terminal yang salah menghentikan master produksi (R-187). Sudah dilihat pemilik |
| 7 | Root menjalankan `approve` lagi | Sesudah bangun ulang master lama gagal atau dibatalkan | Persetujuan tidak boleh hidup lebih lama dari percobaannya (R-203). Sudah dilihat pemilik |
| 8 | Satu centang cadangan | Promote terencana atau bangun ulang selagi keadaan folder cadangan belum diketahui | Mengganti folder tidak boleh menghilangkan centang (R-191) |
| 9 | Kata sandi panel untuk **Perbaiki pengaturan tersimpan** | Sekali, pada master yang sudah terkena R-188 | Tindakan yang sama dengan **Buka kunci master** |
| 10 | Satu tautan lagi ke panduan untuk melihat SQL pembuka | Sesudah **Lepas replika** | SQL itu tidak lagi tampil tanpa syarat (R-190) |
| 11 | Memilih folder cadangan lain | Folder di bawah direktori sistem | Agent menolaknya (R-200) |
| 12 | Login sekali dari tiap browser yang akan dipakai saat gangguan | Sesudah pembaruan | Cookie perangkat (R-181) |

**Yang bisa menahan operator saat gangguan, dan mengapa tidak**

| Perubahan | Cara ia bisa menahan | Mengapa tidak, atau bagaimana dicegah |
|---|---|---|
| Permintaan kata sandi baru | Diblokir login salah dari luar | 5.1 dikerjakan paling dulu: konfirmasi punya anggaran per sesi; login ber-cookie perangkat tidak memakai batas bersama |
| Kata sandi untuk alamat master (dipakai sesudah promote darurat ke host NAT) | Satu permintaan lagi di tengah gangguan | Konfirmasi promote darurat membuka jendela 10 menit; di dalamnya tidak diminta lagi. Sesudahnya satu permintaan, yang tidak bisa diblokir dari luar (5.1) |
| "Ulangi" promote bergerbang | Promote setengah jalan tertahan; master lama tidak dikarantina | Gerbang dan kata sandi hanya sebelum `mysql.promote` dikirim; sesudah titik tak kembali tidak pernah (5.2), dengan test |
| "Ulangi" tidak lagi membuka kunci | Master baca-saja tanpa tombol (N1) | **Buka kunci promote**: tanpa kata sandi, tanpa gerbang, tanpa batas waktu; bila agent menolak, teksnya menyebut sebab dan perintah root |
| Promote darurat "selesai sebagian" bertanya kandidat dulu | Agent kandidat tidak menjawab | Ditolak tanpa mengubah apa pun; sama dengan hari ini, saat `mysql.promote` ulang juga gagal. Kandidat yang sudah bisa ditulis: satu klik seperti sekarang |
| Batas permintaan agent | Agent menjawab "sibuk" pada perintah promote | Batas 64, pemakaian panel yang jujur segelintir; penjaga tidak lewat batas itu; "sibuk" adalah penolakan pasti, bukan hasil tak diketahui |
| Batas sambungan UI | Operator ditolak selagi klien lambat memenuhi panel | Batas per alamat, bukan hanya batas total (5.8) |
| Gerbang menolak laporan basi | Baris merah sesudah restart panel | Promote darurat tidak pernah ditahan gerbang; yang lain kuning, hanya pin master merah, paling lama satu selang laporan |
| Alamat replika tercatat di kunci (R-202) | Alamat replika berubah selama kunci (DHCP): kunci dipertahankan lewat tenggat | Arah aman. Jalan keluar: **Buka kunci promote** menampilkan sebabnya; root memakai `forget-replica` atau `unlock-master` |
| `ya` pada `set-role quarantined` | Beberapa detik lebih lama saat mengkarantina master lama dengan tangan (panduan "Replika yang tersambung di luar alur panel") | Disengaja: langkah inilah yang salah terminal di R-187; `--ya` ada untuk skrip |
| Persetujuan berakhir saat gagal | Satu perjalanan lagi ke terminal | Master lama tidak melayani aplikasi; bukan jalur gangguan |

## 3. Fakta yang menentukan desain

Dari penyelidikan kode 2026-11-27 pada `8caee75`; "(penulis)" berarti dibaca
penulis sendiri dari kode yang sama. Nomor baris di audit sudah bergeser; di
sini ditulis nama fungsi.

**Panel: kata sandi dan sesi**

- F1. Login: 5 percobaan per alamat, 20 kata sandi salah per nama pengguna, 15
  menit. Konfirmasi (`issueConfirm`) memakai batas nama pengguna yang
  **sama**: 20 login salah dari empat alamat memblokir konfirmasi dan login
  semua orang, dengan pesan "1 menit". Restart panel tidak menolong.
- F2. Sesi ada di memori panel (12 jam sejak permintaan terakhir, 24 jam sejak
  login; hilang saat restart dan sesudah `reset-password`). Pada gangguan
  tengah malam operator biasanya **belum login**: sisi login sama pentingnya
  dengan sisi konfirmasi.
- F3. (penulis) Konfirmasi sekarang: dialog mengirim kata sandi ke `POST
  /confirm` dan menerima token sekali pakai (2 menit; terikat pengguna,
  tindakan, sasaran); handler memanggil `requireConfirm` lebih dulu. Login
  dibatasi slot pemeriksaan kata sandi bersama (`loginSem`).
- F4. Token pasang ulang agent dibuat tanpa kata sandi; pairing yang berhasil
  memutus agent lama **tanpa** baris audit; `hostname` agent (sampai 64 KiB)
  masuk journal. (penulis) Token berlaku 24 jam, dan halaman server
  menampilkan perintahnya selama itu kepada siapa pun di sesi itu.
- F5. Alamat master untuk replika diubah tanpa kata sandi; audit mencatat
  nilai baru saja; spanduk lalu menyuruh `set-source` di tiap replika tanpa
  berkata siapa mengubahnya. Jawaban pemeriksaan port disimpan **per alamat**,
  jadi setiap alamat baru disambungi.
- F6. Tanpa kata sandi (diaudit): Batalkan, Stop replika, Hubungkan MySQL,
  Siapkan sebagai master pertama, Setujui restart MySQL master, Sudah saya
  alihkan. (penulis) Restart pada Siapkan master **selalu** menunggu
  persetujuan; Hubungkan MySQL butuh kata sandi sebuah akun MySQL; promote apa
  pun, juga darurat, ditolak selama pengalihan belum dikonfirmasi.
- F7. Audit Pengaturan mencatat nama kunci, bukan nilai. Ambang disk kritis
  diterima sampai 99. Baris cadangan meminta centang hanya bila folder tidak
  diatur; `unknown` atau `pending` sesudah folder diganti kuning tanpa
  centang.

**Panel: promote**

- F8. `Retry` hanya menolak promote darurat yang `mysql.promote`-nya belum
  dikirim. Gerbang dan token hanya untuk pekerjaan yang membuang data.
- F9. "Ulangi" promote terencana adalah satu-satunya cara panel membuka kunci
  promote yang dipertahankan penjaga: kunci ulang ditolak, langkah gagal,
  `promoteUndo` mengirim `mysql.unlockPromote` dengan kredensial kandidat.
- F10. (penulis, dicek di kode agent) `unlockPromote` menolak kecuali kandidat
  terbaca di alamat tercatat dengan `super_read_only` ON dan setiap replika
  dalam daftar kunci terbaca; replika dalam daftar yang bisa ditulis membuat
  agent mengkarantina dirinya. Buka kunci dari panel tidak bisa, sendirian,
  membuat dua master.
- F11. "Terkirim, hasil belum diketahui" (`PromoteSent && !PastPoint`):
  `promoteFailed` menanyai kandidat saat gagal; `resumePromote` (sesudah
  restart panel) tidak; `Retry` mengirim `mysql.promote` lagi tanpa bertanya.
  (penulis) Promosi "selesai sebagian" menolak setiap promote baru di
  topologinya.
- F12. Kunci ulang yang **dijawab** master dengan penolakan tetap memasang
  `LockTriedAt` = sekarang, jadi `refuseWhileLockMayStand` menolak promote
  darurat dan muat data sekitar 36 menit lagi; teksnya menyuruh "Ulangi", yang
  menggesernya lagi. `LockDeadline` dari agent master tidak dibatasi.
- F13. Gerbang promote terencana mencari topologi lewat master **lama**;
  sesudah pengalihan pencarian itu galat, dan gerbang yang tidak bisa menilai
  menolak. Gerbang pada "Ulangi" sesudah titik tak kembali akan menahan
  promote yang masih harus mengkarantina master lama.
- F14. `onOnline` mengambil kunci seluruh panel `repointMu` untuk **setiap**
  server yang tersambung, sebelum tahu ada yang dikirim, dan menahannya selama
  panggilan agent (sampai 5 menit). Karantina yang gagal tidak dikirim lagi
  sampai agent itu tersambung ulang.
- F15. Promote ditolak di kedua mode bila replika lain `Writable`, dinilai
  dari laporan tersimpan berapa pun umurnya. Gerbang menilai jam, disk,
  `performance_schema` dan pin master dengan cara yang sama, juga dari laporan
  yang dipulihkan saat panel menyala.
- F16. Bangun ulang pada server yang tidak dikarantina menghapus baris replika
  gagal lebih dulu, baru menolak.

**Panel: status, header, teks**

- F17. Status master "Terkunci" hanya bila `super_read_only`; `read_only` ada
  di laporan tetapi tidak dibaca.
- F18. Port UI juga melayani pemasangan agent (`/agent/install`, `/agent/bin`,
  `/agent/pair`) kepada `curl` di server basis data; TLS minimum 1.2.
  `/events` memeriksa sesi sekali; `Sessions.Get` **memperpanjang** sesi.
  (penulis) Halaman tanpa login menampilkan versi panel; alamat dengar UI
  sudah bisa diatur (`--http`).
- F19. Teks yang salah atau berbahaya: SQL pembuka bekas replika tanpa syarat
  di tiga tempat; `lockExpiredMessage` menunjuk **Buka kunci master**, yang
  ditolak agent di bawah kunci promote; `STATUS="ENABLED"` gagal di bawah
  `ANSI_QUOTES`; kotak GRANT menampilkan teks agent mentah di samping
  "jalankan sebagai root".

**Agent**

- F20. Satu goroutine per permintaan, tanpa batas jumlah, pesan sampai 1 MiB.
  Handler hidup lebih lama dari sesinya, jadi batas per sambungan bisa
  dilewati. `monitor.configure` menunggu kuncinya tanpa menghormati
  pembatalan.
- F21. Unit agent memakai `KillMode=process` karena agent bisa menyalakan
  mysqld sendiri (`mysqld_safe`, skrip init); mysqld itu tinggal di cgroup
  unit agent.
- F22. `set-role` bertanya hanya saat **meninggalkan** `quarantined`; `--ya`
  sudah ada. Dari `master` ke `quarantined`, dalam 5 detik kedua bendera
  disimpan ON dan kunci start ditulis; yang tercetak hanya "peran diperbarui".
  Penolakan agent di master berakhir "Untuk mengubah peran jalankan … set-role
  <peran>". `set-role master` dari `replica` tidak bertanya, dan
  `mysql.setReadOnly{on:false}` di server ber-pin master tidak memeriksa
  apakah server itu masih mereplikasi.
- F23. Siapa menulis bendera baca-saja (P = nilai tersimpan `SET PERSIST`; OF
  = blok konfigurasi yang dikelola agent):

  | Jalur | Pernyataan |
  |---|---|
  | Kunci promote, penjaganya, `lock-master` | `SET PERSIST super_read_only = ON` saja |
  | `unlockPromote`, penjaga saat tenggat, `unlock-master` | `RESET PERSIST IF EXISTS super_read_only`, lalu `SET GLOBAL` keduanya OFF |
  | **Buka kunci master** di panel (`mysql.setReadOnly`); promote | `SET PERSIST` keduanya OFF |
  | Karantina, kunci start, sesudah CLONE, replika buatan panel | `SET PERSIST` keduanya ON |
  | Konfigurasi replika (OF) | `read_only=ON`, `super_read_only=ON`; promote **tidak** menulis ulang OF |

- F24. Akibatnya, menurut kode. Master yang pernah dikarantina lalu dibuka
  dengan `unlock-master` menyimpan P `read_only=ON`: sesudah restart aplikasi
  tanpa SUPER ditolak (ERROR 1290), dan bila server itu dulu replika (OF
  ON/ON) **semua** tulisan ditolak. Master yang pernah dipromote, sesudah
  kunci lalu buka kunci, hanya menyimpan P `read_only=OFF` di atas OF
  `super_read_only=ON`: keadaan sesudah restart **tidak diketahui**. Sama
  tidak diketahuinya: master yang pernah dipromote dan direstart **selagi
  kunci promote berdiri** (mungkin bisa ditulis beberapa detik). Yang terukur
  (Rencana 5a, 8.0.46) hanya: P menang atas OF untuk bendera yang sama.
- F25. Setiap `monitor.configure` memulai ulang penantian putaran: dikirim
  tiap 9 detik, pemeriksaan, pemindaian cadangan dan penghapusan berkas
  salvage tidak pernah jalan (penjaga tetap jalan). `/var` dan `/run` sendiri
  lolos sebagai folder cadangan.
- F26. Program klien MySQL yang dijalankan agent mewarisi seluruh lingkungan
  root. Jendela perintah operator (`runLifted`) memasang lagi baca-saja tanpa
  `defer`.
- F27. Berkas kunci promote mencatat replika yang terbaca sebagai `host:port`;
  penjaga dan `unlockPromote` menyambung lagi lewat **nama** itu, sedangkan
  kandidat dibaca di alamat yang dicatat.
- F28. Persetujuan bangun ulang berlaku 24 jam untuk semua perintah terlindung
  dan **tidak** berakhir saat pekerjaan gagal atau dibatalkan.
  `mysql.rebuildApproval` (selalu diizinkan) membaca keadaannya.
- F29. Mesin pembangun hanya punya MySQL 8.0.46 (tidak ada 8.4, 9.x, maupun
  batas bawah 8.0.26), dan menjalankan satu proses test pada satu waktu; paket
  test driver sekitar 60 menit. Ada harness browser Chromium (Playwright);
  Firefox dan Safari tidak ada.

## 4. Pendekatan yang dipertimbangkan

Tiga tempat punya pilihan sungguhan. Selebihnya penyelidikan kode hanya
menemukan satu jalan yang layak; alasannya ada di bagian 5.

**Kata sandi untuk tindakan ringan**

Usulan semula menambah permintaan kata sandi di beberapa tempat. Pemilik
keberatan ("Kata sandi diminta di banyak tempat. Ini bukannya malah
merepotkan?") dan memilih pilihan 2 dari tiga. Bunyi pilihan 1 dan 3 tidak
tercatat kata demi kata; yang ditulis di sini adalah dua ujungnya.

- A. Minta setiap kali. Paling ketat: sesi yang dicuri tidak bisa apa pun
  tanpa kata sandi. Harganya permintaan baru di setiap tempat itu, beberapa
  berturutan dalam satu pekerjaan (pasang ulang agent, lalu Hubungkan MySQL).
  Operator yang terbiasa mengetik kata sandi di setiap dialog berhenti membaca
  dialognya.
- **B (dipilih pemilik). Minta sekali, berlaku 10 menit, untuk tindakan
  ringan.** Tindakan yang menghapus data atau mengganti master tetap meminta
  setiap kali. Kelemahannya, seperti yang disampaikan kepada pemilik: bila
  admin meninggalkan browser dalam 10 menit itu, orang lain bisa melakukan
  tindakan ringan tadi.
- C. Tidak meminta (keadaan sekarang), hanya audit. Tidak menutup R-182 dan
  R-183.

Cara jendela itu dibangun juga punya pilihan:

- Di mana ia hidup. (1) Di browser (kata sandi atau token diingat skrip): bisa
  dibaca skrip lain dan tidak bisa dicabut. (2) Per nama pengguna di panel:
  sesi curian ikut menikmati kata sandi yang diketik admin di browser lain.
  (3) Cookie bertanda tangan berisi waktu: tidak bisa dicabut saat logout.
  **(4, dipilih) Di panel, pada catatan sesi itu sendiri**: hilang saat
  logout, saat sesi berakhir, saat panel restart, dan saat `reset-password`.
- Kapan ia mulai. **Setiap kali kata sandi sesi itu terbukti benar**: login,
  konfirmasi tindakan berat, atau permintaan untuk tindakan ringan. Jendela
  **tidak** diperpanjang dengan dipakai: jendela yang bergeser akan terbuka
  selama penyerang terus memakainya.
- Apa yang tidak pernah dicakupnya. Semua tindakan yang hari ini meminta kata
  sandi per perintah; sesi lain dari pengguna yang sama; apa pun sesudah
  logout. Token yang terbit lewat jendela ditolak oleh tindakan berat di
  tempat token itu dipakai, bukan hanya di tempat terbitnya.
- Bagaimana tindakan ringan lewat. (1) Handler melihat jendela dan melewatkan
  token: dua jalur penegakan. **(2, dipilih) Jendela hanya menggantikan
  *mengetik kata sandi* di `POST /confirm`**: tindakan ringan tetap membawa
  token sekali pakai yang terikat tindakan dan sasaran, dan `requireConfirm`
  tetap satu-satunya pintu.

**"Ulangi" promote yang kuncinya dipertahankan**

Kasus uji setiap pilihan adalah N1: promote terencana gagal saat mengejar;
panel tidak berhasil membuka kunci (agent kandidat pergi); pada tenggat
penjaga master **mempertahankan** kunci; master sehat tetapi baca-saja;
operator datang satu jam kemudian.

- A. Tolak dan mulai baru ("Ulangi" ditolak sebelum `mysql.promote` dikirim,
  untuk kedua jenis). Sederhana, dan promote baru memang membaca semuanya
  lagi. **Gagal pada N1**: kunci tidak punya tombol; promote terencana baru
  harus lolos gerbang dengan kandidat yang sama sementara aplikasi berhenti
  menulis. Inilah yang dibangun `f2514a2`, dengan pengecualian "selama kunci
  mungkin berdiri" yang ternyata bisa lewat (N1) dan bisa digeser (SEC-289).
- B. Gerbang dan kata sandi pada setiap "Ulangi" promote. Satu aturan untuk
  semua keadaan. **Gagal dua kali**: sesudah titik tak kembali gerbang tidak
  bisa menilai (F13) dan menolak, jadi promote yang masih harus mengkarantina
  master lama tertahan (dua master lebih lama); dan pada N1 membuka kunci jadi
  butuh daftar Prasyarat yang hijau dan kata sandi, memperpanjang henti tulis
  yang dibuat panel sendiri.
- **C (dipilih). Pisahkan tiga hal yang selama ini satu tombol.** (1) Membuka
  kunci menjadi tindakan sendiri, **Buka kunci promote**: tanpa kata sandi,
  tanpa gerbang, tanpa batas waktu; pemeriksaan agent (F10) yang menjaga. (2)
  "Ulangi" sebelum `mysql.promote` dikirim diperlakukan seperti memulai:
  Prasyarat, centang cadangan, kata sandi lewat jendela, batas 24 jam. (3)
  Sesudah titik tak kembali "Ulangi" tidak pernah digerbang. Untuk keadaan di
  antaranya (terkirim, hasil tak diketahui) panel bertanya ke kandidat dulu.
  **Lolos N1**: tombol buka kunci selalu ada, dan jalan keluarnya sekuat
  "Ulangi" hari ini (perintah agent yang sama, pemeriksaan agent yang sama)
  tanpa menjalankan promote. Harganya: satu tombol dan beberapa teks baru, dan
  tugas terbesar rencana ini.

**Perbaikan `unlock-master`**

- A. Tambah `RESET PERSIST IF EXISTS read_only` (usul tinjauan akhir 5b-2).
  `RESET` menghapus nilai tersimpan, jadi berkas opsi yang menentukan saat
  start. Replika yang dipromote tetap membawa `read_only=ON` dan
  `super_read_only=ON` di blok konfigurasinya (F23): ia akan menolak **semua**
  tulisan sesudah restart pertamanya setelah promote terencana yang
  dibatalkan. **Tidak aman** (A5b2 SEC-246).
- B. Biarkan `RESET PERSIST super_read_only` saja (keadaan sekarang).
  Meninggalkan `read_only=ON` milik karantina, dan pada master yang pernah
  dipromote meninggalkan keadaan yang tidak terukur (F24).
- C. Tulis ulang blok konfigurasi saat promote, supaya OF tidak lagi berkata
  ON. Pertahanan berlapis, tetapi penulisan blok memvalidasi dengan mysqld dan
  bisa gagal di ujung promote, saat tidak ada lagi yang boleh gagal; master
  lama tetap membawa blok lamanya. Tidak sekarang.
- **D (dipilih). `SET PERSIST super_read_only = OFF` lalu `SET PERSIST
  read_only = OFF` di setiap jalur buka; dan kunci menyimpan keduanya ON.**
  Kedua bendera selalu tertulis eksplisit, jadi start berikutnya tidak
  bergantung pada berkas opsi maupun pada urutan MySQL menerapkan dua nilai
  yang berbeda. Kunci dan buka menjadi cermin tepat. Harganya: nilai OFF
  tersimpan mengalahkan `read_only=ON` yang kelak ditulis orang ke my.cnf di
  server itu (bagian 8).
- **Mengukur dulu (dipilih, sebagai langkah pertama, bukan pengganti D).**
  Perbaikan D tidak bergantung pada hasil ukur, tetapi tiga hal bergantung:
  seberapa parah server yang **sudah** terkena (teks peringatan dan catatan
  pembaruan), aturan field "akan baca-saja sesudah restart" (5.4), dan bukti
  bahwa test restart memang menangkap kerusakannya (merah pada kode sekarang,
  hijau sesudah perbaikan). Tanpa ukuran, sel "tidak diketahui" di F24 akan
  diisi dugaan.

## 5. Keputusan desain

Di setiap sub-bagian: aturan, perilaku, tepi (gangguan; agent 0.2.0 atau
keadaan tersimpan; panel yang dibobol atau sesi curian), lalu verifikasi. Teks
operator bernomor ada di 5.11.

### 5.1 Kata sandi dan batas percobaan (R-181, R-182, R-183, R-191)

**Batas percobaan (R-181); dikerjakan paling dulu.** Aturannya: lalu lintas
tanpa login tidak menyentuh anggaran yang dipakai sesi yang sudah login atau
browser yang pernah login.

- **Konfirmasi punya anggaran per sesi:** paling banyak 5 kata sandi salah per
  menit dan 20 per 15 menit; kata sandi benar mengosongkannya. `issueConfirm`
  tidak lagi membaca batas nama pengguna untuk memutuskan. Kata sandi salah di
  sini tetap dihitung ke batas nama pengguna (menahan login anonim, seperti
  maksud spek 5b), tetapi batas nama pengguna yang penuh tidak lagi menolak
  konfirmasi. Jawaban 429 menyebut sisa waktu yang sebenarnya (T1).
- **Login dengan cookie perangkat.** Login yang berhasil memasang cookie
  `__Host-` (HttpOnly, Secure, SameSite=Lax, satu tahun) berisi nama pengguna,
  nomor acak dan waktu terbit, bertanda tangan HMAC dengan kunci yang
  diturunkan dari `panel.key`. Login yang membawa cookie sah untuk nama
  pengguna yang diketik dihitung pada anggaran cookie itu sendiri (5 salah per
  15 menit) dan **tidak** ditolak batas nama pengguna, batas alamat, pembatas
  yang penuh, maupun slot pemeriksaan kata sandi bersama (ia punya dua slot
  sendiri). Login tanpa cookie memakai aturan sekarang. Cookie itu bukan
  kredensial: kata sandi tetap diminta. Ia ditolak bila tanda tangannya salah,
  namanya lain, terbit sebelum `reset-password` terakhir, atau lebih tua dari
  setahun. Logout tidak menghapusnya.
- Batas per alamat memakai awalan /64 untuk IPv6. README "Keamanan" ditulis
  ulang: restart bukan obat; panel hanya dijangkau dari jaringan admin (B2).
- Tepi. Browser yang belum pernah login tetap terkunci selama serangan,
  seperti sekarang; obatnya firewall. Panel restart: pembatas kosong (seperti
  sekarang), cookie perangkat tetap sah. `panel.key` diganti: cookie tidak sah
  sampai login berikutnya. Sesi curian: 20 tebakan per 15 menit pada
  anggarannya sendiri, dan ia tidak bisa menghabiskan anggaran sesi admin.
  Cookie perangkat curian: 5 tebakan per 15 menit, dan hanya bisa mengunci
  cookie itu.
- Verifikasi (unit, dengan jam): 20 login salah dari empat alamat, lalu
  konfirmasi benar dari sesi yang ada tetap 200 dan login benar ber-cookie
  tetap 303; cookie palsu, milik nama lain, atau dari sebelum `reset-password`
  ditolak; teks 429 menyebut menit yang benar.

**Jendela kata sandi.** Aturannya: kata sandi yang terbukti benar membuka
jendela 10 menit pada sesi itu; di dalamnya tindakan ringan tidak meminta kata
sandi lagi; tindakan berat tidak pernah memakainya.

- Catatan sesi (di memori panel) menyimpan waktu kata sandi terakhir terbukti
  benar: saat login, saat konfirmasi tindakan berat, saat permintaan untuk
  tindakan ringan. Jendela terbuka bila waktu itu kurang dari 10 menit lalu.
  Memakai jendela tidak mengubah waktu itu.
- `POST /confirm` untuk tindakan ringan boleh datang tanpa kata sandi. Jendela
  terbuka: token terbit, dengan `window: true` di baris audit
  `confirm_issued`. Jendela tertutup: jawaban 401 "butuh kata sandi", dan
  dialog kata sandi muncul dengan kalimat T2. Browser tidak perlu tahu keadaan
  jendela, dan tidak menyimpan apa pun.
- `POST /confirm` untuk tindakan berat tanpa kata sandi selalu ditolak. Token
  mencatat bahwa ia terbit lewat jendela, dan tindakan berat menolak token
  seperti itu saat dipakai.
- Tindakan berat tidak berubah: semua yang hari ini meminta kata sandi per
  perintah (promote, Resync, bangun ulang, hapus, Lepas replika, Buka kunci
  master, sertifikat, salvage, Setujui dan Ulangi pekerjaan yang membuang
  data).
- Tepi. Jendela hilang saat logout, saat sesi berakhir, saat panel restart,
  saat `reset-password`. Halaman yang dibuka sebelum pembaruan tetap bekerja
  (tombol lamanya mengirim kata sandi). Sesi curian di dalam jendela bisa
  melakukan tindakan ringan: itulah kelemahan yang diterima pemilik (bagian
  8); README mengatakannya (B6).

**Tindakan mana yang meminta apa**

| Tindakan | Diminta | Alasan |
|---|---|---|
| Token pasang ulang agent (server sudah ter-pairing) | jendela | Mengganti agent: penerima setiap rahasia server itu (R-182) |
| Ubah alamat master untuk replika | jendela | Mengalihkan sumber salinan berikutnya (R-183) |
| Setujui restart MySQL master (Siapkan master) | jendela | Restart master adalah gangguan |
| "Ulangi" promote terencana sebelum master diganti | jendela, sesudah Prasyarat | 5.2 |
| Siapkan sebagai master pertama | jendela | Mengubah pengaturan MySQL yang mungkin sudah dipakai aplikasi. Langkah penyiapan, bukan jalur gangguan |
| Hubungkan MySQL pada server yang **sudah** punya akun tersimpan | jendela | Mengganti akun yang dipakai penjaga dan promote; sesi curian dengan akun aplikasi berhak rendah bisa melumpuhkan agent server itu. Hubungan pertama tidak diminta |
| Sudah saya alihkan | tidak | Pernyataan, bukan perintah ke server; yang dibukanya (bangun ulang, promote berikutnya) meminta kata sandi sendiri; dan ia ada di jalur gangguan kedua (F6) |
| Batalkan | tidak | Jalan keluar operator sebelum promote darurat (K2 Rencana 5b-2b) |
| Stop replika | tidak | Bisa dibalik, dan membuka peringatan |
| Pengaturan | tidak | Sering disimpan; nilai lama dan baru kini diaudit |
| Token untuk server yang belum ter-pairing | tidak | Tidak ada agent yang digantikan |

**Pasang ulang agent (R-182).** Tombol **Pasang ulang agent** dan **Buat token
baru** pada server yang sudah ter-pairing menjadi tombol berkonfirmasi (pola
**Lepas replika**), teks T3. Token pasang ulang berlaku 15 menit, bukan 24
jam, dan halaman server berhenti menampilkan perintahnya saat token habis:
tanpa itu perintah yang dibuat admin bisa dibaca sesi mana pun sehari penuh
(F4). Pairing yang berhasil menulis baris audit `agent_pair`: alamat,
menggantikan agent lama atau tidak, nomor seri sertifikat, versi dan
arsitektur agent, `hostname` dipotong 255 rune satu baris. Potongan yang sama
berlaku untuk baris penolakan dan journal. Agent 0.2.0 berpasangan tanpa
perubahan. Verifikasi (unit): token untuk server ter-pairing tanpa konfirmasi
→ 403, tidak ada token; server belum ter-pairing tetap tanpa kata sandi;
`hostname` 60 KiB terpotong; satu baris `agent_pair` per pairing.

**Alamat master (R-183).** Rute itu meminta konfirmasi (jendela), teks T4;
audit mencatat nilai lama dan baru. Topologi mendapat dua kolom yang boleh
kosong: siapa dan kapan alamat terakhir diubah; promote mengisinya dengan
nomor pekerjaannya. Spanduk "Arahkan agent replika" memuat kalimat T5 (atau
T5b sesudah promote); pemasangan lama dengan kolom kosong menampilkan spanduk
tanpa kalimat itu. Pemeriksaan port master menyambung paling banyak sekali per
master per 10 menit, apa pun alamatnya; alamat baru menunggu gilirannya dan
barisnya berkata T6. Verifikasi (unit): tanpa token → 403 dan tidak ada yang
tersimpan; dua pembacaan gerbang dengan dua alamat dalam 10 menit → satu
sambungan.

**Masukan gerbang dan audit (R-191).**

- Audit Pengaturan mencatat nilai lama dan baru setiap kunci yang berubah;
  untuk rahasia (token bot Telegram) hanya "diubah".
- Baris "Ruang disk" merah pada pemakaian ≥ ambang kritis **atau** ≥ 95%, mana
  yang lebih rendah, jadi menaikkan ambang tidak mematikan baris itu (T7).
  Peringatan disk tetap memakai ambang yang diatur.
- Baris cadangan meminta centang juga saat keadaan folder `unknown` atau
  `pending`, pada promote terencana dan bangun ulang (T8). Supaya laporan lama
  tidak bisa mengembalikan keadaan folder sebelumnya, lihat butir generasi
  folder di 5.10.
- `backup_dir` utuh di audit (batasnya sudah 1024), lama dan baru.
  `master_last_report` dicatat bersama waktu panel menerima laporan itu.
- README "Keamanan": satu kalimat masing-masing untuk ambang, folder cadangan
  dan pelepasan replika sebagai cara mengubah warna baris.
- Tepi. Promote darurat tidak ditahan baris mana pun, jadi tidak ada dari
  butir ini yang menahan gangguan. Verifikasi (unit): ambang 99 dengan disk
  96% tetap merah; centang diminta pada `pending`; nilai lama dan baru ada di
  audit.

### 5.2 "Ulangi" promote dan kunci master (R-184, R-195)

**Aturannya: membuka kunci tidak pernah butuh apa pun dari operator selain
satu klik; menjalankan promote lagi butuh semua yang dibutuhkan saat
memulainya; menyelesaikan promote yang sudah mengganti master tidak pernah
ditahan.**

| Keadaan pekerjaan promote | "Ulangi dari tahap ini" | **Buka kunci promote** |
|---|---|---|
| Terencana, gagal, `mysql.promote` belum dikirim | Seperti memulai: daftar Prasyarat dibaca (kandidat dan master pekerjaan itu), centang cadangan bila diminta, kata sandi lewat jendela. Lebih dari 24 jam sejak konfirmasi terakhir: ditolak (T12) | Tampil selama kunci belum terbukti dibuka |
| Terencana, dibatalkan | Tidak ada (pekerjaan batal tidak bisa diulang) | Tampil selama kunci belum terbukti dibuka |
| Darurat, gagal sebelum dikirim | Tidak ada, seperti sekarang: promote darurat baru | Tidak ada kunci |
| `mysql.promote` terkirim, hasil belum diketahui (kedua jenis) | Panel bertanya ke kandidat dulu (di bawah) | Tidak ditawarkan: kandidat mungkin sudah menulis |
| Sesudah titik tak kembali (kedua jenis) | Seperti sekarang: tanpa gerbang, tanpa kata sandi | Tidak ditawarkan |

- **Buka kunci promote** menjalankan pembukaan yang sekarang ada di jalur
  gagal (`mysql.unlockPromote` dengan kredensial kandidat). Berhasil, atau
  agent menjawab "tidak dikunci untuk promosi": panel mencatat kunci terbuka,
  dan promote darurat serta muat data boleh jalan saat itu juga (T10, T10b).
  Agent menolak: teksnya tampil berlabel bersama perintah root dan syaratnya
  (T11). Agent master tidak tersambung: T11b. Diaudit. Tanpa kata sandi dan
  tanpa gerbang, karena tindakan ini hanya mengembalikan tulisan ke master
  yang sehat, agent yang memeriksa (F10), dan kata sandi di sini memperpanjang
  henti tulis yang dibuat panel sendiri.
- Tombol itu (dengan kalimat T9) ada di halaman pekerjaan promote, dan di
  halaman master selama statusnya "Terkunci" dan promosi terakhir topologi itu
  promote terencana yang tidak sedang berjalan dan belum melewati titik tak
  kembali. Di halaman master tombol itu tampil **apa pun** catatan panel
  tentang kunci: catatan panel bisa salah, agent yang tahu. Selagi pekerjaan
  promote topologi itu berjalan, panel menolak tombol itu.
- **Terkirim, hasil belum diketahui.** Sebelum apa pun, "Ulangi" membaca
  kandidat seperti `promoteFailed` (`mysql.status`, `job.status`, berbatas
  waktu, di luar `startMu`, lalu diperiksa ulang di bawahnya):
  - kandidat bisa ditulis → dicatat lewat titik tak kembali, promote
    dilanjutkan tanpa kata sandi dan tanpa gerbang (menuntaskan karantina
    master lama adalah arah aman);
  - kandidat baca-saja dan pekerjaan agentnya selesai → tidak ada yang terjadi
    di kandidat: pekerjaan digagalkan, kunci dibuka (terencana), dan
    jawabannya T13 (darurat: mulai promote darurat baru) atau T13b (terencana:
    kini berlaku baris pertama tabel);
  - kandidat tidak terbaca, atau pekerjaan agentnya masih berjalan → ditolak
    dengan T14 atau T14b; tidak ada yang diubah.
- **Jendela "kunci mungkin berdiri".** Kunci ulang yang **dijawab** master
  dengan penolakan mengembalikan catatan kunci sebelumnya (tidak ada kunci
  baru, jadi tidak ada jendela baru). Pembukaan yang terkonfirmasi mengakhiri
  jendela seketika. Tenggat dari jawaban agent master dipotong ke
  `LockTriedAt` + lama kunci yang diminta + kelonggaran panggilan +
  kelonggaran jam; potongan yang sama diterapkan saat membaca pekerjaan
  tersimpan dari 0.2.0. Yang dikirim ke kandidat adalah nilai terpotong (sisa
  waktu lebih pendek: kandidat menolak, bukan membuka terlambat). Teks
  penolakan menunjuk **Buka kunci promote**, bukan "Ulangi" (T15).
- **Audit.** "Ulangi" promote terencana dicatat dengan field yang sama dengan
  mulainya (baris merah yang dilewati centang, dan seterusnya); "Ulangi"
  sesudah titik tak kembali dicatat sebagai lanjutan.
- **Teks.** `lockExpiredMessage`, baris log `promoteUndo`, dan kalimat promote
  terencana yang dibatalkan menunjuk tombol baru (T16, T17). Kalimat README
  dan Panduan tentang "Ulangi" ditulis ulang (B1), dan kata "Setujui" disebut
  (R-195).
- **Tepi.** Pekerjaan promote gagal atau "selesai sebagian" dari 0.2.0
  mengikuti aturan baru pada "Ulangi" berikutnya; tidak ada migrasi. Tidak ada
  perubahan protokol: agent 0.2.0 sudah punya ketiga perintah itu. Sesi curian
  bisa menekan **Buka kunci promote**: akibat terburuknya master yang sehat
  menulis lagi, sesudah agent memastikan kandidat masih replika baca-saja.
  Panel yang dibobol tidak mendapat apa pun yang baru (ia sudah bisa mengirim
  perintah itu).
- **Diperiksa terhadap N1.** Promote terencana gagal saat mengejar, agent
  kandidat pergi, penjaga mempertahankan kunci, operator datang sehari
  kemudian. "Ulangi" ditolak (lebih dari 24 jam); **Buka kunci promote** tetap
  ada di pekerjaan dan di halaman master. Kandidat sudah kembali: agent
  memeriksa lalu membuka. Kandidat mati: agent menolak dengan sebabnya, dan
  T11 memberi `unlock-master` dengan syaratnya. Tidak ada cabang yang
  meninggalkan master tanpa langkah berikutnya.
- Verifikasi. Unit orchestrator dengan agent palsu: setiap baris tabel;
  "Ulangi" sebelum kirim ditolak tanpa token dan dengan baris merah;
  `TestPromotePlannedRetryLiftsKeptLock` ditulis ulang untuk tindakan baru
  (tenggat + 1 jam, kredensial kandidat, promote darurat langsung diizinkan);
  kunci ulang yang ditolak tidak menggeser `LockTriedAt`; tenggat terpotong;
  empat jawaban kandidat; **"Ulangi" sesudah titik tak kembali tetap tanpa
  token dan tanpa gerbang, kedua jenis** (penjaga F13). Ujung ke ujung dengan
  mysqld sungguhan: promote terencana gagal saat mengejar dengan agent
  kandidat dihentikan, lalu **Buka kunci promote** sesudah kandidat kembali,
  lewat pemeriksaan agent yang asli.

### 5.3 Kunci panel dan karantina tertunda (R-185)

**Aturannya: panel memutuskan dari catatannya dulu, mengunci hanya server yang
dikirimi perintah, dan mencoba lagi sendiri.**

- `onOnline` membaca store lebih dulu dan pulang tanpa kunci bila tidak ada
  yang perlu dikirim ke server itu.
- `repointMu` seluruh panel diganti kunci per server sasaran (server yang
  menerima `quarantine` atau `repoint`). Yang dijaga kunci lama hanya "satu
  perintah semacam itu per server pada satu waktu"; pengalihan topologi tidak
  berada di bawahnya.
- Setiap pengiriman dari `onOnline` mendapat batas waktunya sendiri sesudah
  kunci diperoleh, bukan sisa waktu sejak agent tersambung.
- Putaran coba-ulang: selama sebuah promosi punya karantina tertunda dan agent
  master lamanya tersambung, panel mengirim lagi tiap 60 detik, melambat
  sampai 5 menit. Penolakan pasti dari agent menghentikan putaran untuk
  promosi itu; peringatannya tetap terbuka. Halaman server menampilkan kapan
  terakhir dicoba (T18).
- Tepi. Agent yang diam kini hanya menahan kuncinya sendiri. Master lama yang
  agentnya sendiri dibobol tetap bisa mengabaikan karantina (melekat pada
  model). Tidak ada protokol, migrasi, atau langkah operator baru.
- Verifikasi (unit, dua agent palsu, dengan `-race`): X menahan
  `mysql.repoint` tanpa jawaban, M dengan karantina tertunda tersambung →
  karantina M terkirim dalam satu detik; karantina yang gagal dikirim lagi
  selagi M tersambung; tidak ada kunci untuk server tanpa pekerjaan.

### 5.4 `unlock-master` dan baca-saja sesudah restart (R-188)

**Aturannya: setiap jalur yang membuka master menyimpan kedua bendera OFF;
setiap jalur yang mengunci menyimpan keduanya ON; dan panel menampilkan master
yang tidak bisa ditulis aplikasi, sekarang atau sesudah restart berikutnya.**

1. **Ukur dulu.** Satu test bertabel dengan mysqld sungguhan membaca nilai
   berjalan dan `performance_schema.persisted_variables` sesudah setiap
   langkah dan sesudah restart, pada kode **sekarang**: master yang tidak
   pernah dipromote (siapkan → kunci → restart → buka → restart); master yang
   pernah dipromote (replika → promote → kunci → **restart selagi terkunci** →
   buka → restart); server yang pernah dikarantina lalu dijadikan master lagi,
   dari kedua asal. Hasilnya dicatat di rencana pelaksanaan dan mengisi sel
   "tidak diketahui" F24.
2. **Perbaiki.** Satu pasang fungsi di driver dipakai semua jalur: buka = `SET
   PERSIST super_read_only = OFF` lalu `SET PERSIST read_only = OFF` (dipakai
   `unlockPromote`, penjaga saat tenggat, `unlock-master`,
   `mysql.setReadOnly`); kunci = `SET PERSIST super_read_only = ON` lalu `SET
   PERSIST read_only = ON` (kunci promote, penjaganya, `lock-master`). `RESET
   PERSIST` tidak dipakai lagi untuk kedua bendera. CLI kehilangan salinan
   pernyataannya sendiri. Bila pernyataan kedua buka gagal, berkas kunci masih
   ada dan penjaga mengunci lagi (gagal tertutup). Bila pernyataan kedua kunci
   gagal, server tetap terkunci seperti hari ini; agent tidak pernah
   melaporkan "tidak terkunci" selagi `super_read_only` ON. Urutan promote
   sendiri tidak diubah. Test yang sama lalu harus hijau pada setiap baris.
3. **Status panel membaca kedua bendera.** Master dengan `read_only` ON dan
   `super_read_only` OFF berstatus "Terkunci" dengan keterangan T19,
   peringatan `master_locked` mengikuti, dan **Buka kunci master** (yang sudah
   menyimpan keduanya OFF pada versi agent mana pun) memperbaikinya.
4. **Field "akan baca-saja sesudah restart".** Agent di server ber-pin master,
   tanpa kunci promote dan tanpa kunci start, melaporkan field opsional baru
   di laporan kesehatan: benar bila nilai tersimpan atau blok konfigurasi akan
   membuat salah satu bendera ON pada start berikutnya. Aturan tepatnya
   diturunkan dari hasil langkah 1. Agent hanya melapor; ia tidak pernah
   membuka tulisan sendiri. Panel menampilkan peringatan T20 di halaman master
   dengan tombol **Perbaiki pengaturan tersimpan**: tindakan dan kata sandi
   yang sama dengan **Buka kunci master** (agent menolaknya di bawah kunci
   promote, jadi tombol ini tidak bisa melewati promote yang berjalan);
   sesudahnya T20b. Field yang tidak ada (agent 0.2.0) berarti tidak
   diketahui: tidak ada peringatan.
5. **Teks.** `unlock-master` berkata C7. Kalimat Rencana 4 §5.1 "Membuka kunci
   berarti `RESET PERSIST super_read_only; …`" menjadi B3. README dan panduan
   "Melepas kunci master lama dengan tangan", "Master terkunci padahal tidak
   ada promote" dan "Memakai lagi server yang dihapus saat masih dikarantina"
   membuang langkah `SET PERSIST` dengan tangan untuk agent versi ini, dan
   menyimpan satu baris untuk agent yang lebih lama.

- Tepi. Master yang **sudah** terkena: lihat 5.12. Buka kunci lewat agent
  0.2.0 tetap memakai `RESET PERSIST` sampai agent itu diperbarui. Server
  dengan `persisted_globals_load=OFF` mengabaikan semua nilai tersimpan (batas
  yang sudah ada, bagian 8). Tidak ada jalur baru yang membuka server yang
  hari ini tetap baca-saja: karantina dan kunci start tidak disentuh, dan
  semua jalur buka tetap ditolak di server yang dikarantina.
- Verifikasi. Test restart di atas (mysqld 8.0.46). Unit monitor: empat
  kombinasi bendera, peringatan buka dan tutup. Ujung ke ujung: `SET PERSIST
  read_only=ON` pada master → "Terkunci" → **Buka kunci master**
  membersihkannya, juga bila hanya `read_only` yang ON. Tidak terbukti di
  sini: 8.4, 9.x, dan restart oleh systemd sungguhan.

### 5.5 Perintah root: `set-role`, persetujuan, penjaga kunci (R-187, R-203, R-202)

**`set-role` bertanya (R-187).** Aturannya: `set-role` meminta `ya` diketik
setiap kali perintah itu bisa menghentikan sebuah master atau membuka server
yang masih mereplikasi; penolakan agent di master tidak lagi berbunyi seperti
petunjuk.

- Bertanya bila: (1) peran sekarang `master` dan peran baru bukan `master`;
  (2) peran baru `quarantined` dan peran sekarang bukan itu; (3) peran
  sekarang `replica` dan peran baru `master`. Pertanyaan yang sudah ada
  (meninggalkan `quarantined`) tetap. `--ya` melewati semuanya (teks
  bantuannya C8). Tidak bertanya: `unassigned` → `master` (jalan yang
  disarankan peringatan `master_unpinned`) dan antara `replica` dan
  `unassigned`.
- Layar: baris C1 (nama host kernel, peran sekarang, peran baru), lalu
  akibatnya (C2 sampai C5), lalu pertanyaan yang sudah ada. Peran dibaca ulang
  sesudah jawaban, seperti sekarang. Tanpa `--ya` dan tanpa masukan (skrip),
  perintah membatalkan diri dengan kode keluar 1. Sesudah `set-role
  quarantined` berhasil tercetak C6.
- `roleRefusal` untuk server ber-pin `master` menjadi A1; untuk peran lain
  tidak berubah. Panel tidak mengurai teks itu.
- README bagian `set-role` dan panduan "Replika yang tersambung di luar alur
  panel" (langkah 4) menampilkan pertanyaannya; kalimat "Perintah ini tidak
  bertanya" dibuang. Tidak ada skrip di repo yang memanggil `set-role`.
- Tepi. Mengkarantina master lama dengan tangan menjadi beberapa detik lebih
  lama; disengaja (bagian 2). Panel yang dibobol tetap bisa menampilkan
  petunjuk apa pun di halamannya; pertanyaan ini pemeriksaan terakhir di
  terminal root. Agent 0.2.0 tidak bertanya.
- Verifikasi (unit, `cli_test.go`): tiap pasangan peran × `ya`, jawaban lain,
  `--ya`; peran berubah selama pertanyaan; pin tidak terbaca tetap lewat
  cabang lama. Teks penolakan di `pins_test.go`.

**Persetujuan bangun ulang (R-203).** Aturannya: persetujuan root hidup selama
satu percobaan bangun ulang, paling lama 24 jam.

- Agent mengakhiri persetujuan sendiri saat pekerjaan `mysql.dumpFrom` atau
  `mysql.cloneFrom` berakhir gagal atau dibatalkan, dan saat
  `mysql.dropDatabases` gagal. Bagian ini tidak bergantung pada panel.
- Panel, saat pekerjaan bangun ulang master lama gagal atau dibatalkan,
  meminta agent mengakhiri persetujuan (sesudah perintah pembersihannya
  sendiri). Permintaan itu selalu diizinkan di server yang dikarantina dan
  hanya bisa mengakhiri. Jawaban agent menentukan teksnya: T21 bila berakhir;
  T21b bila agent tidak mengenalnya atau tidak menjawab.
- "Ulangi dari tahap ini" sesudah itu berhenti sekali di "menunggu persetujuan
  di server" dengan kode baru, lewat alur yang sudah ada. Ini juga berlaku
  sesudah `drop-database` atau `grant-admin`: teks agent Rencana 5b-2a yang
  menyuruh "Ulangi dari tahap ini" tetap benar, dan T21 menambahkan langkah
  `approve`. Batas 24 jam dan `approve batal` tidak berubah.
- Tepi. Agent 0.2.0 tidak mengakhiri apa pun: T21b. Panel yang dibobol tidak
  ditahan selama percobaan berjalan (melekat), dan persetujuan yang belum
  pernah dipakai tetap hidup 24 jam bila panel itu tidak memintanya berakhir
  (bagian 8).
- Verifikasi. Unit driver dengan pekerjaan palsu: salinan gagal atau
  dibatalkan → berkas persetujuan hilang; jalur berhasil dan settle tidak
  berubah. Unit panel: jalur gagal dan batal mengirim permintaan itu; teks per
  jawaban. Ujung ke ujung: bangun ulang gagal → "Ulangi" → menunggu
  persetujuan.

**Penjaga kunci promote (R-202).** Aturannya: penjaga membaca setiap replika
dalam daftar kunci di alamat yang dicatat saat kunci dipasang, seperti ia
membaca kandidat.

- Saat kunci dipasang, tiap nama dalam daftar diselesaikan sekali dan dibaca
  di alamatnya (nama tetap dipakai untuk memeriksa sertifikat). Berkas kunci
  menyimpan pasangan `host:port` dan alamat yang menjawab, sebagai field baru
  yang boleh tidak ada. Penjaga dan `unlockPromote` menyambung ke alamat itu.
  `forget-replica` menghapus pasangannya juga.
- Berkas kunci dari agent lama (tanpa alamat) dibaca lewat nama seperti
  sekarang; kunci hidup paling lama 60 menit, jadi jendela itu menutup
  sendiri. Nama dengan beberapa alamat: yang dicatat adalah yang menjawab.
- Tepi. Alamat replika yang berubah selama kunci berdiri terbaca "tidak bisa
  dibaca": kunci dipertahankan dan kejadian Kritis yang sudah ada menyebutnya
  (bagian 2). Tidak ada perubahan protokol.
- Verifikasi. Unit dengan pengganti `lookupIP` dan `readPeer`: DNS menjawab
  alamat lain saat buka → alamat tercatat yang disambungi, umpan tidak pernah;
  berkas lama; `forget-replica`. Dengan mysqld sungguhan: satu test kunci dua
  instance yang ada, dengan alamat yang dipetakan.

### 5.6 Batas beban agent dan monitor (R-186, R-200, R-201)

**Batas permintaan (R-186).** Aturannya: agent menjalankan paling banyak 64
permintaan panel sekaligus, dihitung untuk seluruh agent, bukan per sambungan.

- Slot diambil di putaran baca sebelum handler dimulai dan dilepas saat
  handler pulang, juga sesudah sesinya berakhir (F20). Bila penuh, permintaan
  langsung dijawab galat A2 (kode galat yang sudah ada) dan parameternya
  dibuang. Putaran penjaga dan pengiriman kejadian tidak memakai slot.
  `monitor.configure` menunggu kuncinya sambil menghormati pembatalan, jadi
  antreannya berakhir saat panel pergi.
- Memori yang bisa ditahan permintaan menjadi sekitar 64 × 2 MiB.
- `MemoryMax` dan `OOMScoreAdjust` **tidak** dipasang (F21, bagian 8); README
  mengatakannya (B7).
- Tepi. Handler yang macet tanpa menghormati pembatalan menahan slotnya; bila
  64 macet, agent menjawab "sibuk" sampai direstart, dan penjaga tetap jalan.
  Jawaban "sibuk" pada `mysql.promote` adalah penolakan pasti (handler tidak
  pernah mulai), jadi penilaian 5.2 tetap benar. Panel 0.2.0 melihatnya
  sebagai teks galat biasa.
- Verifikasi (unit, sesi palsu): 74 permintaan ke handler yang menahan → 64
  berjalan, 10 dijawab sibuk; slot kembali sesudah lepas dan sesudah
  putus-sambung. Tidak terbukti di sini: perilaku OOM cgroup.

**`monitor.configure` (R-200).**

- Penantian putaran menghitung tenggatnya sekali; konfigurasi ulang hanya
  boleh **memajukan** tenggat (selang baru yang lebih pendek). Panel yang
  jujur tidak melihat beda; folder baru dipindai pada putaran berikutnya,
  tidak pernah lebih lambat dari sekarang.
- `ValidBackupFolder` juga menolak: `/var` dan `/run` sendiri (induk folder
  yang disembunyikan unit), dan pohon sistem `/etc`, `/usr`, `/bin`, `/sbin`,
  `/lib`, `/lib32`, `/lib64`, `/libx32`, `/boot`, `/var/lib/dpkg`,
  `/var/lib/rpm`. `/var/lib/mysql` **tidak** ditolak: isinya sudah dilaporkan
  agent lewat inventaris, dan ada pemasangan yang menaruh cadangan di
  dekatnya. Formulir panel menolak dengan aturan yang sama (T22).
- Tepi. Folder tersimpan yang kini ditolak membuat baris cadangan "tidak
  valid" (merah): promote terencana dan bangun ulang meminta centang sampai
  folder diganti (5.12). Folder di luar daftar itu tetap bisa dipakai panel
  yang dibobol untuk mengintai (bagian 8).
- Verifikasi (unit, jam palsu): konfigurasi tiap 9 detik selama 10 menit →
  putaran tetap terjadi tiap selang; tabel folder.

**Lingkungan program klien dan jendela perintah (R-201).** Satu fungsi
membangun lingkungan bersih: tanpa `MYSQL_*` dan `LIBMYSQL_*`,
`MYSQL_TEST_LOGIN_FILE=/dev/null`, lalu kunci milik pemanggil. Dipakai
`ExecEnvAs` (semua pemanggilnya), perintah dump CLI, dan `mysqld
--validate-config` serta `mysqld --verbose --help`. Tidak dipakai pemasang
(apt butuh lingkungannya) dan tidak untuk start layanan (bagian 8). Task ini
memeriksa bahwa kedua pemanggilan `--print-defaults` kunci start memakai
`--defaults-file`; bila lingkungan layanan yang terukur membutuhkan
`MYSQL_HOME`, kunci itu dikecualikan. `runLifted` memasang lagi baca-saja
lewat `defer` dengan `recover`, lalu meneruskan paniknya. Verifikasi: unit
atas lingkungan yang dibangun; dengan mysqld sungguhan, perintah yang panik
meninggalkan kedua bendera ON.

### 5.7 Gerbang dan status (R-197, R-198, R-199)

- **Laporan segar atau "belum diketahui" (R-197).** Sebuah laporan segar bila
  servernya tersambung, laporannya langsung (bukan susulan, bukan pulihan saat
  panel menyala), dan umurnya tidak melewati batas basi monitor. Tidak segar:
  baris jam, disk dan `performance_schema` kuning "belum diketahui" (T23);
  baris pin master **merah** (T23b), karena pin yang tidak diketahui tidak
  boleh meloloskan promote terencana. Laporan pulihan ditandai di memori;
  tidak ada migrasi. Sesudah panel restart baris-baris itu kuning atau merah
  sampai laporan langsung pertama. Verifikasi (unit, dengan jam).
- **Replika yang melapor bisa ditulis (R-198).** Tidak ada jalan melewatinya,
  juga pada promote darurat (bagian 8). Yang berubah: untuk setiap replika
  yang laporan tersimpannya berkata bisa ditulis, panel membaca agentnya
  sekali (`mysql.status`, paling lama 10 detik, sejajar) sebelum menilai.
  Jawaban baca-saja: tidak menahan. Jawaban bisa ditulis: ditolak dengan T24,
  yang menyebut replikanya dan kedua jalan keluar. Tidak menjawab: tetap
  ditolak (T24b). Teks tampil di layar promote sebelum apa pun diketik.
  Verifikasi (unit): laporan basi bisa ditulis + jawaban baca-saja → promote
  diizinkan.
- **Bangun ulang pada server yang tidak dikarantina (R-199).** Pemeriksaan
  peran dipindah ke atas `RemoveReplica`: permintaan ditolak tanpa menyentuh
  baris. Verifikasi (unit).

### 5.8 Header, sesi, pertumbuhan (R-189, R-193, R-194)

- **CSP (R-189).** `default-src 'none'`, direktif yang sudah ada, dan
  `font-src 'self'`. Dibuktikan dua kali: test yang mengurai templat dan CSS
  dan menuntut direktif untuk tiap jenis sumber daya; dan tur setiap halaman
  di Chromium dengan pendengar `securitypolicyviolation` yang harus mencatat
  nol pelanggaran. `Clear-Site-Data: "cache"` hanya pada logout: `"cookies"`
  akan menghapus cookie situs lain di domain yang sama, `"storage"` akan
  menghapus pilihan tema. Yang tidak dibangun: bagian 8.
- **Sesi (R-193).** Aliran `/events` memeriksa sesinya di setiap ping (20
  detik) dengan pemeriksaan baru yang **tidak** memperpanjang sesi; memakai
  `Sessions.Get` di sini akan menahan tab menganggur tetap login sampai 24
  jam. Sesudah logout, kedaluwarsa atau `reset-password` aliran berakhir dalam
  satu ping.
- **Kata sandi awal.** Selama berkas `admin-initial-password` ada, dasbor
  menampilkan spanduk T25. Panel tidak menghapusnya sendiri (belum ada halaman
  ganti kata sandi).
- **Batas server UI.** `WriteTimeout` 2 menit; aliran SSE sudah mengecualikan
  dirinya, dan task ini memeriksa unduhan salvage berbuat sama. Batas
  sambungan 32 per alamat dengan batas total yang tinggi; tidak pernah batas
  total saja. Halaman tanpa login tidak lagi menampilkan versi panel. README
  menyebut `--http` untuk mengikat UI ke satu alamat.
- **Pertumbuhan (R-194).** Pembatas per alamat (30 per menit) pada
  `/agent/pair`, `/agent/install`, `/agent/bin/*`. Pembacaan inventaris
  otomatis untuk akun kosong paling banyak sekali per server per 10 menit,
  diaudit. Peringatan berjenis sama untuk server yang sama yang terbuka lagi
  dalam 10 menit membuka lagi baris yang sama; tidak ada peringatan yang
  dibuang. Baris log per pekerjaan dibatasi lajunya. Sambungan agent **tidak**
  ditolak atau ditunda (5.3 bergantung padanya); yang dibatasi adalah baris
  yang ditulis karena sambungan yang berkedip. Agent membatasi ukuran
  laporannya, sesudah task ini mengukur apakah laporan di atas 1 MiB memang
  membuat server tampak offline (A5b2 SEC-278, belum dikonfirmasi).
  Verifikasi: unit per batas.

### 5.9 Teks (R-190, R-192, R-195, R-196)

- **Lepas replika (R-190).** Flash dan dialog tidak lagi memuat SQL pembuka
  (T27). Panduan mendapat bagian "Memakai bekas replika sebagai server
  mandiri" dengan syaratnya (B4) lalu SQL-nya; README sama. Dialog **Lepas
  replika** menampilkan kotak peringatan T26 selagi master "Diduga tumbang";
  tidak memblokir dan tidak menambah centang. Test: tidak ada templat atau
  flash di luar Panduan yang memuat `super_read_only=OFF`.
- **Teks agent di samping perintah root (R-192).** Panel mengurai tiap butir
  `MissingGrants` dengan tata bahasa ketat (`GRANT` + hak dari daftar tetap +
  `ON *.* TO` akun inventaris) dan menampilkan pernyataan yang ia susun
  sendiri; butir lain menjadi satu baris T28 tanpa perintah. Pin yang tidak
  dikenal tampil sebagai "tidak dikenal" tanpa teks agent. Di agent, dua teks
  penolakan "tidak disalin dari sumber" Rencana 5b-2a hanya menyebut nama yang
  aman untuk shell (huruf, angka, garis bawah, tanda hubung bukan di depan,
  paling panjang 64); sisanya dihitung (A3). Test dengan teks jahat (`\!`,
  baris baru, penanda arah).
- **"Ulangi" dan "Setujui" (R-195).** Kalimat B1 di README (tiga tempat) dan
  halaman Panduan.
- **Butir R-196.** Perintah pemeriksaan event memakai kutip tunggal (B5). Teks
  kunci promote: 5.2. Kotak master tumbang tanpa replika mendapat kalimat T29.
  Dua butir belum diperiksa ulang pada kode sekarang dan diperiksa di task
  teks: kartu **Ganti master** yang masih berkata "mode terencana" saat kotak
  darurat muncul (pilihannya: ikut diperbarui fragmen status, atau kalimat
  mode dibuang dari kartu), dan tab lama yang menerima kotak lewat fragmen.
- Semua teks ini dikerjakan sesudah 5.2 dan 5.4, supaya README dan Panduan
  ditulis ulang sekali.

### 5.10 Test dan sisa kecil (R-204, R-205)

- **Port pembantu test MySQL.** Dipilih sesudah `--initialize`, di luar
  rentang port sementara kernel, dicadangkan dengan berkas ber-`flock` selama
  instance hidup, dengan satu percobaan ulang di port lain.
- **`TestBackupScanOneAtATime`.** Pemindai palsu memberi tanda "mulai" sesudah
  mencatat panggilannya; test menunggu tanda itu. Ditulis ulang sesudah
  perubahan penantian putaran (5.6), lalu direndam (200 kali sendiri dan di
  dalam paketnya). Akar masalahnya belum terbukti dari membaca; rendaman itu
  buktinya.
- **Direktori test** tidak berawalan `dbpanel`, sisa yang lebih tua dari
  sehari dihapus saat test mulai, pembersihan didaftarkan sebelum batas waktu
  bisa melompatinya. `TestPlainHTTPRedirectHeaders` memakai nilai tetap dan
  memeriksa `Server` tidak ada.
- **Paket test driver** (sekitar 60 menit) dipecah saat test restart 5.4
  ditambahkan.
- **Generasi folder cadangan.** Laporan cadangan agent membawa penanda
  konfigurasi folder yang dipindainya (field opsional baru); panel menganggap
  laporan dengan penanda lain "belum diketahui". Ini menutup dua sisa (laporan
  lama mengembalikan keadaan folder sebelumnya; dua simpan cepat) dan membuat
  centang 5.1 tidak bisa dihindari dengan waktu. Agent 0.2.0 tidak
  mengirimnya: perilaku sekarang.
- **Sisa lain, satu task:** `configMu`, `ageClamped` dan `startPin` dipangkas
  atau dibatasi; peringatan `job_failed` promote yang gagal sebelum kirim
  ditutup; teks perbaikan F2a untuk baris dua server; panduan 8 (Segarkan atau
  Hubungkan); pembacaan inventaris otomatis diaudit (5.8); README "Keamanan"
  menyebut baris Akun panel dan Versi agent; README "Pembaruan" langkah 1 dan
  daftar penolakannya; halaman pekerjaan memeriksa "digantikan" sebelum
  gerbang; `/retry/prepare` tidak menyegarkan inventaris pada halaman merah
  yang basi; test panduan mengenali konstanta bernama lain; kartu dan baris
  cadangan tidak mengekstrapolasi umur saat peringatan menganggapnya tidak
  diketahui; log folder di agent dipotong sama di dua tempat; test
  `hidden`/`invalid` lewat `monitor.configure`; komentar salah tempat. Satu
  butir menunggu pemeriksaan: kutip tunggal dalam sebab galat agent yang bisa
  terpotong sensor panel (diperbaiki di sisi yang lebih kecil).

### 5.11 Teks baru (untuk dibaca pemilik)

Setiap teks ditulis di satu baris dan dipakai persis. `%s`, `%d` dan `<…>`
adalah tempat nilai. Tata letak layar `set-role` mengikuti pola teks
`set-role` yang sudah ada; kalimatnya yang ditetapkan di sini.

**Tampil di panel (buatan panel)**

- T1 konfirmasi ditolak karena terlalu banyak salah: `Terlalu banyak kata sandi salah di sesi ini; coba lagi dalam %d menit.`
- T2 kalimat di dialog kata sandi untuk tindakan ringan: `Kata sandi panel diminta sekali; selama 10 menit sesudahnya tindakan seperti ini tidak memintanya lagi.`
- T3 dialog pasang ulang agent: `Pasang ulang agent di %s? Agent yang sekarang tetap bekerja sampai agent baru terpasang. Perintah pemasangannya berlaku 15 menit.`
- T4 dialog ubah alamat master: `Ubah alamat master untuk replika menjadi %s? Replika baru dan Resync ulang akan menyalin dari alamat itu.`
- T5 spanduk "Arahkan agent replika", alamat diubah orang: `Alamat ini diubah oleh %s pada %s. Pastikan itu memang alamat server %s sebelum menjalankan perintah di bawah.`
- T5b sama, alamat dipasang promote: `Alamat ini dipasang panel saat promote (pekerjaan %s) pada %s.`
- T6 baris "Port MySQL master" sesudah alamat diubah: `Alamat master baru saja diubah; port diperiksa lagi paling cepat pukul %s.`
- T7 baris "Ruang disk" bila ambang di atas 95: `Ambang kritis diatur %d%%; gerbang menilai merah mulai 95%%.`
- T8 centang cadangan saat keadaan folder belum diketahui: `Keadaan folder cadangan belum diketahui (folder baru diubah, atau agent master belum melapor). Centang bila Anda sudah memastikan sendiri cadangan terbaru ada.`
- T9 kalimat di atas tombol **Buka kunci promote**: `Master %s mungkin masih terkunci oleh promote ini. Tombol di bawah meminta agent master membuka kuncinya; agent memeriksa dulu bahwa kandidat masih replika.`
- T10 kunci dibuka: `Kunci promote di master %s dibuka; aplikasi bisa menulis
  lagi.`
- T10b tidak ada kunci: `Master %s tidak sedang dikunci untuk promote; tidak
  ada yang dibuka.`
- T11 agent menolak membuka (pesan agent tampil berlabel di tengahnya): `Agent master %s tidak membuka kunci. <pesan agent>. Bila Anda yakin kandidat tidak dipromosikan dan tidak ada replika lain yang sudah menjadi master, buka di server master: sudo dbpanel agent unlock-master`
- T11b agent master tidak tersambung: `Agent master %s tidak tersambung, jadi panel tidak bisa membuka kuncinya. Sambungkan agent itu lalu tekan lagi; atau, bila Anda yakin kandidat tidak dipromosikan dan tidak ada replika lain yang sudah menjadi master, buka di server master: sudo dbpanel agent unlock-master`
- T12 "Ulangi" promote terencana sesudah 24 jam: `Promote ini terakhir dikonfirmasi lebih dari 24 jam lalu, jadi "Ulangi dari tahap ini" tidak tersedia lagi. Mulai promote baru dari halaman master. Bila master masih terkunci, tekan "Buka kunci promote" dulu.`
- T13 promote darurat yang ternyata belum membuka master baru: `Promote darurat ini belum membuka master baru: kandidat %s masih baca-saja. Pekerjaan ini digagalkan; tidak ada yang berubah. Mulai promote darurat baru dari halaman master.`
- T13b sama, promote terencana: `Promote ini belum mengganti master: kandidat %s masih baca-saja. Pekerjaan ini digagalkan. Bila master masih terkunci, tekan "Buka kunci promote". Untuk mencoba lagi: "Ulangi dari tahap ini" (daftar Prasyarat dibaca lagi), atau promote baru.`
- T14 kandidat tidak terbaca: `Kandidat %s belum bisa dibaca, jadi panel belum tahu apakah ia sudah menerima tulisan. Tidak ada yang diubah. Tekan "Ulangi dari tahap ini" lagi sesudah agent kandidat tersambung.`
- T14b pekerjaan agent kandidat masih berjalan: `Pekerjaan promote di kandidat %s masih berjalan. Tidak ada yang diubah. Tunggu sebentar, lalu tekan "Ulangi dari tahap ini" lagi.`
- T15 kalimat penutup penolakan "kunci mungkin berdiri" (menggantikan kalimat yang menyuruh "Ulangi"): `Sebelum itu, bila agent master tersambung, tekan "Buka kunci promote" pada pekerjaan promote terencana itu: begitu agent menjawab, panel mencatat kunci terbuka dan tindakan ini bisa langsung dijalankan.`
- T16 akhir peringatan kunci yang dipertahankan penjaga: `Bila promote tidak dilanjutkan, tekan "Buka kunci promote" pada pekerjaan promote itu atau di halaman master.`
- T17 baris log saat panel gagal membuka kunci, juga untuk promote yang dibatalkan: `Kunci master tidak bisa dibuka (%v). Pada tenggat kunci agent master membukanya sendiri bila kandidat masih replika dan semua replika terbaca; bila tidak, kunci dipertahankan. Tekan "Buka kunci promote" untuk mencoba lagi.`
- T18 halaman master lama yang karantinanya tertunda: `Perintah karantina untuk server ini belum diterima agentnya; terakhir dicoba pukul %s. Panel mencoba lagi sendiri selama agent tersambung.`
- T19 keterangan status master: `Master dikunci (read_only aktif, super_read_only mati): akun aplikasi tanpa hak SUPER tidak bisa menulis`
- T20 peringatan di halaman master, dengan tombol **Perbaiki pengaturan tersimpan**: `MySQL di master ini akan menjadi baca-saja sesudah restart berikutnya, karena pengaturan baca-saja lama masih tersimpan. Sekarang aplikasi tetap bisa menulis. Tekan "Perbaiki pengaturan tersimpan": tidak ada restart dan tidak ada gangguan.`
- T20b sesudah diperbaiki: `Pengaturan tersimpan di master %s sudah dibetulkan; master tetap bisa ditulis sesudah restart.`
- T21 bangun ulang gagal atau dibatalkan, persetujuan berakhir: `Persetujuan root di server %s untuk bangun ulang ini sudah diakhiri. Untuk mengulang: tekan "Ulangi dari tahap ini", lalu jalankan di server itu perintah approve dengan kode yang ditampilkan.`
- T21b sama, agent tidak mengakhirinya: `Persetujuan root di server %s mungkin masih berlaku sampai 24 jam sejak disetujui. Bila bangun ulang tidak dilanjutkan, akhiri di server itu: sudo dbpanel agent approve batal`
- T22 formulir folder cadangan: `Folder di bawah direktori sistem (%s) tidak diterima sebagai folder cadangan. Pakai folder lain, misalnya /backup.`
- T23 baris gerbang dari laporan lama: `belum diketahui: laporan terakhir server ini sudah lama (%s)`
- T23b baris "Pin master" dari laporan lama: `Pin master belum terbaca dari laporan baru (laporan terakhir %s). Muat ulang halaman ini sebentar lagi.`
- T24 promote ditolak, replika lain bisa ditulis: `Replika %s melaporkan bisa ditulis (super_read_only mati), jadi mungkin sudah menjadi master; promote ditolak, juga promote darurat. Bila %s memang bukan master (misalnya dibuka dengan tangan untuk perbaikan): kunci lagi di halaman replikanya, atau lepas dengan Lepas replika, lalu buka halaman ini lagi. Bila aplikasi sudah menulis ke %s: jangan promote; periksa server itu dulu.`
- T24b sama, replika itu tidak menjawab: `Replika %s terakhir melaporkan bisa ditulis dan sekarang tidak menjawab, jadi panel tidak bisa memastikannya; promote ditolak, juga promote darurat. Bila Anda yakin %s bukan master: lepas dengan Lepas replika, lalu buka halaman ini lagi.`
- T25 spanduk dasbor: `Berkas kata sandi awal masih ada di host panel. Sesudah kata sandi itu Anda simpan di tempat aman, hapus berkasnya: sudo rm %s`
- T26 kotak di dialog **Lepas replika** selagi master diduga tumbang: `Master %s sedang diduga tumbang. Bila replika ini harus menggantikannya, jangan lepas: jalankan Promote darurat dari halaman master, yang juga mengkarantina master lama. Replika yang dilepas lalu dibuka dengan tangan menjadi master kedua saat master lama hidup lagi.`
- T27 flash sesudah lepas dan hentikan: `Replika dilepas dan replikasinya dihentikan. MySQL di server ini tetap terkunci hanya-baca. Kapan aman membukanya, dan caranya, ada di Panduan: "Memakai bekas replika sebagai server mandiri".`
- T28 kotak hak yang kurang, butir tidak dikenal: `Agent melaporkan hak yang kurang dalam bentuk yang tidak dikenal panel, jadi perintahnya tidak ditampilkan. Klik Segarkan; bila tetap begini, perbarui agent server ini.`
- T29 kotak master tumbang tanpa replika: `Master ini belum punya replika, jadi tidak ada yang bisa dipromosikan: pulihkan master ini.`

**Dari agent (tampil di panel dengan label)**

- A1 penolakan di server ber-pin master: `server ini dipin sebagai master; perintah %s ditolak karena bisa membuang atau menimpa data master. Jangan ubah peran server ini kecuali server lain sudah menjadi master dan aplikasi tidak lagi memakai server ini.`
- A2 agent penuh: `agent sedang sibuk (terlalu banyak perintah berjalan); coba
  lagi sebentar`
- A3 ekor dua teks "tidak disalin dari sumber" Rencana 5b-2a, untuk nama yang tidak aman: `… dan %d database lain dengan nama tidak biasa; lihat dengan: sudo mysql -e 'SHOW DATABASES'`

**Keluaran perintah CLI di server**

- C1 baris pertama pertanyaan `set-role`: `Server ini: %s. Peran sekarang: %s.
  Peran baru: %s.`
- C2 peran baru `quarantined`: `Setelah perintah ini: dalam beberapa detik agent membuat MySQL server ini baca-saja (read-only) dan memasang kunci start; aplikasi tidak bisa menulis ke server ini, juga sesudah MySQL di-restart. Jalankan hanya di master lama, sesudah server lain menjadi master. Untuk membatalkan: sudo dbpanel agent set-role master, lalu sudo dbpanel agent unlock-master.`
- C3 dari `master` ke `replica`: `Setelah perintah ini: server ini tidak lagi dijaga sebagai master. Panel boleh membuang datanya dan menyalin ulang dari server lain tanpa persetujuan di server ini.`
- C4 dari `master` ke `unassigned`: `Setelah perintah ini: server ini tanpa peran. Panel boleh menyiapkannya ulang, termasuk membuang semua datanya, tanpa persetujuan di server ini.`
- C5 dari `replica` ke `master`: `Setelah perintah ini: server ini dipin sebagai master dan panel boleh membukanya untuk ditulis. Perintah ini tidak menghentikan replikasi: bila server ini masih mengikuti master lain, Anda akan punya dua server yang bisa ditulis.`
- C6 sesudah `set-role quarantined`: `Peran diperbarui. Dalam beberapa detik agent membuat MySQL server ini baca-saja dan memasang kunci start.`
- C7 sesudah `unlock-master`: `MySQL server ini bisa ditulis lagi, dan tetap begitu sesudah MySQL di-restart.`
- C8 teks bantuan `--ya`: `tanpa pertanyaan`

**README, Panduan dan spek**

- B1 kalimat "Ulangi" (README tiga tempat, Panduan): `**Ulangi dari tahap ini** pada promote terencana yang gagal sebelum master diganti membaca lagi daftar **Prasyarat** dan meminta kata sandi panel, seperti saat memulainya. **Ulangi dari tahap ini** dan **Setujui** pada pekerjaan yang langkah sisanya tidak membuang data (misalnya Buat replika sesudah salinannya jadi, atau promote yang masternya sudah diganti) tidak menilai lagi daftar itu. "Langkah sisanya" berarti langkah yang belum dijalankan pekerjaan itu.`
- B2 README "Keamanan", batas percobaan: `Buka panel hanya dari jaringan admin (firewall atau VPN). Login salah dari luar tidak memblokir konfirmasi kata sandi sesi yang sudah login, dan tidak memblokir login dari browser yang pernah berhasil login. Browser yang belum pernah login bisa terkunci 15 menit selama serangan berlangsung; me-restart panel tidak menolong.`
- B3 Rencana 4 §5.1: `Membuka kunci berarti SET PERSIST super_read_only = OFF lalu SET PERSIST read_only = OFF. Mengunci berarti SET PERSIST untuk keduanya ON.`
- B4 syarat di panduan "Memakai bekas replika sebagai server mandiri": `Buka hanya bila server ini akan dipakai aplikasi sebagai server mandiri dan master lamanya tidak lagi dipakai aplikasi mana pun. Bila master sedang tumbang dan replika ini harus menggantikannya, jangan lewat jalan ini: pakai Promote darurat, yang juga mengkarantina master lama.`
- B5 perintah pemeriksaan event: `mysql -e "SELECT EVENT_SCHEMA, EVENT_NAME FROM information_schema.EVENTS WHERE STATUS='ENABLED';"`
- B6 README "Keamanan", jendela kata sandi: `Sesudah kata sandi panel Anda terbukti benar (login atau konfirmasi), selama 10 menit tindakan ringan tidak memintanya lagi: token pasang ulang agent, alamat master untuk replika, persetujuan restart MySQL master, Siapkan sebagai master pertama, Hubungkan MySQL yang mengganti akun, dan Ulangi promote terencana sebelum master diganti. Tindakan yang menghapus data atau mengganti master tetap meminta setiap kali. Jangan tinggalkan browser yang sedang login: dalam 10 menit itu orang lain bisa melakukan tindakan ringan tadi.`
- B7 README, memori agent: `Unit agent tidak memasang MemoryMax. Di server yang MySQL-nya dinyalakan agent sendiri (mysqld_safe atau skrip init), mysqld berada di cgroup unit agent, dan batas itu bisa membuat kernel membunuh MySQL. Agent membatasi jumlah perintah panel yang berjalan sekaligus (64).`

### 5.12 Kompatibilitas

- **Panel baru, agent 0.2.0.** Semua bagian panel bekerja. Yang belum ada
  sampai agent diperbarui: field "akan baca-saja sesudah restart" (tidak ada
  peringatan T20), buka dan kunci dengan kedua bendera (agent lama masih
  memakai `RESET PERSIST`), pengakhiran persetujuan (T21b), alamat replika di
  berkas kunci, pertanyaan `set-role`, batas permintaan, penanda folder
  cadangan. Aturan "Versi agent" yang ada tidak diubah: tindakan bergerbang
  dan promote darurat ke kandidat ber-agent lain versi tetap ditolak sampai
  agent diperbarui.
- **Agent baru, panel 0.2.0.** Field laporan baru bersifat opsional dan
  diabaikan panel lama. Jawaban "sibuk" tampil sebagai galat biasa. Sesudah
  bangun ulang gagal, "Ulangi" di panel lama bertemu permintaan persetujuan
  lewat alur yang sudah ada (diperiksa di task R-203).
- **Keadaan tersimpan.** Migrasi: dua kolom yang boleh kosong di `topologies`
  (5.1) dan satu tanda pada token konfirmasi. Pekerjaan promote dari 0.2.0
  mengikuti aturan 5.2 pada "Ulangi" berikutnya; tenggat kunci tersimpan
  dipotong saat dibaca. Ambang disk tersimpan di atas 95 tetap tersimpan;
  gerbang memakai 95. Sesi ada di memori: restart saat pembaruan
  mengakhirinya, seperti setiap restart. Berkas kunci promote dari agent lama
  dibaca lewat nama.
- **Langkah pembaruan untuk operator** (masuk README "Pembaruan" dan catatan
  rilis):
  1. Perbarui panel, lalu **segera** setiap agent. Seperti sekarang, promote
     ke kandidat yang versi agentnya lain dari panel ditolak, juga promote
     darurat.
  2. Sesudah itu buka halaman tiap master. Bila tampil peringatan T20, tekan
     **Perbaiki pengaturan tersimpan**. Jangan menunggu: master itu baca-saja
     pada restart MySQL berikutnya.
  3. Login sekali dari tiap browser yang akan dipakai saat gangguan.
  4. Skrip yang memanggil `set-role` menambah `--ya`.
  5. Bila baris cadangan menjadi "tidak valid" karena foldernya di bawah
     direktori sistem, pilih folder lain.
- **Master yang sudah terkena R-188.** Yang nilai tersimpannya salah tetapi
  belum direstart: bisa ditulis, dan sesudah agentnya diperbarui menampilkan
  T20 dengan tombolnya. Yang **sudah** direstart dan kini menolak tulisan
  aplikasi: tampil "Terkunci" (T19 bila hanya `read_only` yang ON) dan **Buka
  kunci master** memperbaikinya dengan versi agent mana pun. Selama agentnya
  belum diperbarui, master jenis pertama tidak menampilkan apa pun; karena itu
  langkah 1 dan 2 berurutan.

### 5.13 Verifikasi

- **Unit** (agent palsu, orchestrator palsu, jam palsu): disebut di tiap
  sub-bagian. Yang wajib ada sebagai penjaga regresi: "Ulangi" sesudah titik
  tak kembali tanpa token dan tanpa gerbang; **Buka kunci promote** sesudah
  tenggat + 1 jam; konfirmasi dan login ber-cookie selagi batas nama pengguna
  penuh; token jendela ditolak tindakan berat; test kunci-per-server dengan
  `-race`.
- **mysqld sungguhan (harness yang ada).** Tabel restart 5.4, dijalankan dulu
  pada kode sekarang lalu pada perbaikan; kunci promote dengan alamat
  tercatat; perintah yang panik di jendela operator; buka kunci lewat
  pemeriksaan agent yang asli. **Hanya MySQL 8.0.46** yang ada di mesin ini:
  8.4, 9.x dan batas bawah 8.0.26 tidak bisa dijalankan, dan semua hasil ukur
  berlaku untuk 8.0.46 saja.
- **Ujung ke ujung** (panel, agent, mysqld): promote terencana gagal dengan
  kunci dipertahankan lalu **Buka kunci promote**; master dengan `read_only`
  tersimpan → "Terkunci" → dibuka; bangun ulang gagal → "Ulangi" → menunggu
  persetujuan.
- **Browser (Chromium lewat harness yang ada):** tur setiap halaman tanpa
  pelanggaran CSP; dialog konfirmasi untuk tindakan ringan, di dalam dan di
  luar jendela.
- **Tidak bisa dibuktikan di sini:** Firefox, Safari dan browser lama milik
  operator (CSP, cookie `__Host-` pada alamat IP); klien CentOS 7 dengan
  OpenSSL lama terhadap port UI (karena itu TLS 1.3 saja tidak dibangun);
  perilaku systemd (restart MySQL oleh systemd, cgroup dan OOM); NAT dan
  jaringan terbelah sungguhan; Telegram.
- **Penutup.** Tinjauan seluruh cabang, lalu audit keamanan seperti Rencana
  5b-2: tiga pemeriksa yang hanya membaca, peta OWASP untuk sisi web, satu
  gelombang perbaikan, pemeriksaan ulang atas diff-nya.

## 6. Yang diserahkan kepada pelaksana

**Bentuk**

- Nama fungsi, berkas, field, kunci JSON dan kolom; bentuk cookie perangkat;
  bentuk kunci per server (5.3); bentuk field laporan baru.
- Bentuk permintaan "akhiri persetujuan" (5.5): parameter opsional pada
  `mysql.rebuildApproval` atau perintah sendiri. Syaratnya tetap: selalu
  diizinkan di server yang dikarantina, hanya bisa mengakhiri, dan agent 0.2.0
  tidak terganggu olehnya.
- Angka yang tidak ditetapkan: batas total sambungan UI, laju baris log
  pekerjaan, batas baris karena sambungan berkedip.
- Pembagian menjadi task (rencana pelaksanaan).

**Yang belum diketahui dan harus diperiksa, di task yang disebut**

- Hasil tabel restart, dan dari situ aturan field "akan baca-saja sesudah
  restart" (task pertama R-188).
- Apakah `SET PERSIST super_read_only = ON` ikut menyimpan `read_only`, dan
  apakah **Buka kunci master** diterima agent saat hanya `read_only` yang ON
  (task R-188).
- Setiap perintah terlindung yang dikirim panel ke master lama sesudah sebuah
  langkah bangun ulang gagal; pengakhiran persetujuan oleh panel harus
  sesudahnya (task R-203). Juga: "Ulangi" dari panel 0.2.0 terhadap agent
  baru.
- Kecocokan R-202 dengan sisa audit 5a yang berdekatan (R-033, R-108), yang
  belum dicocokkan pemeriksa (task R-202).
- Unduhan salvage dan `WriteTimeout`; `--print-defaults` dan `MYSQL_HOME`;
  laporan di atas 1 MiB (task masing-masing, 5.6 dan 5.8).
- Dua butir R-196 yang belum dibaca ulang (5.9).

**Urutan dan dua bagian**

Bagian A, yang berbahaya, dalam urutan ini:

1. R-181 (batas percobaan). Semua permintaan baru bergantung padanya.
2. R-184 ("Ulangi", **Buka kunci promote**, jendela kunci, tenggat), bersama
   kalimat B1 dan teks kunci T9 sampai T17.
3. Kelompok kata sandi: jendela, R-182, R-183, R-191.
4. R-185.
5. R-188: ukur, perbaiki, status panel, field dan tombol, teks.
6. R-202 (berkas dan jalur buka yang sama dengan R-188).
7. R-187.
8. R-203.
9. R-190 (ditambahkan penulis ke bagian A, lihat bagian 7).

Supaya bagian A bisa di-merge sendiri, ia harus memuat: setiap teks panel,
CLI, README dan Panduan yang menjelaskan perilaku yang diubahnya (B1 sampai
B4, B6, dan teks T, A, C milik butir di atas), sehingga tidak ada kalimat yang
berbohong pada hari merge; kedua migrasi; langkah pembaruan 5.12 nomor 1
sampai 4; catatan hasil ukur R-188; dan test penjaga 5.13 dalam keadaan hijau.
Yang belum ada sampai bagian B dan harus disebut di laporan bagian A: penanda
folder cadangan (centang T8 masih bisa dihindari dengan waktu) dan pembacaan
langsung R-198.

Pengendali berhenti dan melapor singkat di antara bagian A dan bagian B. Bila
dua test yang kadang gagal (5.10) mengganggu gerbang test bagian A, perbaikan
port pembantu test boleh dimajukan ke awal bagian A.

Bagian B: 5.6 (dengan perbaikan port dan test pemindaian dari 5.10, port lebih
dulu bila rendaman butuh gerbang yang stabil), 5.7, 5.8, sisa 5.9, 5.10. Lalu
penutup di 5.13.

## 7. Keputusan penulis yang perlu dilihat pemilik

**Tiga hal yang perlu diketahui sebelum menyetujui**

- **Jendela kata sandi, seperti yang Anda pilih.** Kata sandi yang terbukti
  benar (login, konfirmasi berat, atau permintaan ringan) membuka 10 menit
  pada sesi itu. Di dalamnya enam tindakan tidak meminta kata sandi: token
  pasang ulang agent, alamat master untuk replika, persetujuan restart MySQL
  master, Siapkan sebagai master pertama, Hubungkan MySQL yang mengganti akun,
  dan "Ulangi" promote terencana sebelum master diganti. Yang paling berat di
  antaranya adalah pasang ulang agent: orang yang memakai browser yang
  ditinggal dalam 10 menit itu bisa mengganti agent sebuah server.
- **Tiga perubahan untuk operator yang sudah Anda lihat:** promote darurat
  yang gagal sebelum master baru dibuka tidak bisa di-"Ulangi" dan dimulai
  baru; `set-role` meminta `ya` diketik; sesudah bangun ulang master lama
  gagal atau dibatalkan, root menjalankan `approve` lagi.
- **Tiga hal yang tidak dibangun** (alasannya di bagian 8): promote darurat
  yang melewati replika lain yang melapor bisa ditulis; TLS 1.3 saja dan HSTS
  `includeSubDomains`; `MemoryMax` pada unit agent.

**Keputusan penulis yang belum Anda lihat**

1. **Tiga tindakan yang belum diputuskan.** Siapkan sebagai master pertama dan
   Hubungkan MySQL yang mengganti akun tersimpan masuk jendela; Sudah saya
   alihkan tetap tanpa kata sandi (alasan di tabel 5.1). Bila salah: dua yang
   pertama menambah satu permintaan pada langkah penyiapan; yang ketiga
   membiarkan sesi curian menyatakan pengalihan selesai, tetapi semua yang
   dibukanya meminta kata sandi.
2. **Token pasang ulang agent berlaku 15 menit, bukan 24 jam** (5.1). Perintah
   yang sudah dibuat tampil di halaman server selama tokennya hidup, jadi kata
   sandi saat membuatnya tidak melindungi sehari penuh. Bila salah: operator
   yang lambat membuat token baru.
3. **Anggaran konfirmasi dihitung per sesi, dan login ber-cookie perangkat
   tidak memakai batas per alamat maupun slot pemeriksaan bersama** (5.1).
   Tanpa itu sesi curian, atau serangan dari alamat yang sama dengan admin,
   masih bisa memblokir. Bila salah: pencuri cookie perangkat mendapat 5
   tebakan kata sandi per 15 menit.
4. **R-190 (teks Lepas replika) dimasukkan ke bagian A.** Barisnya Tinggi (dua
   master), perbaikannya kecil, dan SQL-nya berubah bersama R-188. Bila salah:
   bagian A sedikit lebih besar.
5. **Buka kunci promote tampil di halaman master apa pun catatan panel**,
   selama master "Terkunci" dan promosi terakhirnya promote terencana yang
   tidak berjalan (5.2). Catatan panel bisa salah; tombol yang hilang karena
   catatan itu adalah N1 lagi. Bila salah: tombol tampil saat tidak perlu, dan
   agent menjawab "tidak dikunci".
6. **Promote terencana yang perintahnya terkirim tetapi tidak terjadi
   digagalkan, bukan dilanjutkan diam-diam** (T13b). Melanjutkannya berarti
   menjalankan promote tanpa Prasyarat. Bila salah: satu klik lagi untuk
   operator.
7. **Peringatan R-188 menunjuk tombol panel, bukan perintah root** (T20).
   Tombol itu tidak bisa melewati kunci promote yang berdiri; `unlock-master`
   bisa. Bila salah: satu tombol yang jarang tampil.
8. **Persetujuan berakhir pada setiap kegagalan bangun ulang, dan panel ikut
   meminta agent mengakhirinya** (5.5). Penyelidikan kode menyarankan aturan
   agent saja; itu tidak mencakup pembatalan selagi tidak ada pekerjaan agent
   yang berjalan. Harganya satu tambahan kecil pada protokol, dan satu
   perjalanan lagi ke terminal juga pada alur `drop-database` lalu "Ulangi".
   Bila salah: bangun ulang yang gagal berulang terasa lebih merepotkan.
9. **Daftar folder cadangan yang ditolak** (5.6): `/var/lib/mysql` tidak
   masuk, `/usr` masuk seluruhnya (termasuk `/usr/local`). Bila salah: sebuah
   rumah sakit yang menyimpan cadangan di `/usr/local/…` mendapat baris merah
   dan harus memindahkan foldernya.
10. **Dua hal dari R-187 yang tidak dibangun:** `set-role master` dari
    `unassigned` tidak bertanya (itu jalan yang disarankan peringatan panel
    sendiri), dan agent tidak menolak **Buka kunci master** pada server yang
    masih mereplikasi (penolakan yang keliru meninggalkan master baca-saja
    tanpa tombol). Bila salah: lihat bagian 8.
11. **Butir kecil yang menyimpang dari saran penyelidikan kode:** sambungan
    agent yang berkedip tidak ditunda (penundaan akan menunda karantina master
    lama yang kembali); versi panel dibuang dari halaman tanpa login; penanda
    folder cadangan dibangun sekarang (field opsional), karena tanpa itu
    centang T8 bisa dihindari.
12. **Teks baru:** semua butir 5.11. Yang paling perlu dibaca: T2, T9 sampai
    T17, T20, T21, T24, T26, C1 sampai C5, B1, B6.

## 8. Risiko yang tersisa setelah 5b-3

**Sengaja tidak dibangun**

- **Promote darurat melewati replika yang melapor bisa ditulis (R-198).**
  Replika itu mungkin master dari promosi sebelumnya yang hasilnya hilang;
  melewatinya dengan satu centang bisa berakhir dengan dua atau tiga master.
  Yang tersisa: satu replika yang salah konfigurasi, atau agentnya dibobol,
  menahan promote darurat seluruh topologi sampai operator mengunci atau
  melepas replika itu (kata sandi panel). Layar kini menyebut replikanya dan
  jalan keluarnya (T24).
- **TLS 1.3 saja dan HSTS `includeSubDomains` (R-189).** Port UI juga melayani
  pemasangan agent kepada `curl` di server basis data; OpenSSL lama (misalnya
  CentOS 7) gagal dengan TLS 1.3 saja, dan itu tidak bisa diuji di sini. Kode
  tidak bisa tahu situs apa lagi yang hidup di bawah nama panel. Yang tersisa:
  UI menerima TLS 1.2 (dengan cipher suite bawaan Go); nama di bawah nama
  panel tidak dipaksa HTTPS. `require-trusted-types-for` dan `report-to` juga
  tidak dibangun.
- **`MemoryMax` pada unit agent (R-186).** Di server yang MySQL-nya dinyalakan
  agent sendiri, mysqld tinggal di cgroup agent, dan batas itu bisa membunuh
  MySQL: gangguan yang hendak dicegahnya. Yang tersisa: memori agent dibatasi
  lewat jumlah permintaan saja; panel yang dibobol yang menemukan jalan lain
  untuk menumbuhkan agent masih bisa memancing OOM killer. Tidak ada data
  hilang; basis data berhenti.

**Dari perubahan rencana ini**

- **Jendela kata sandi.** Dalam 10 menit sesudah kata sandi diketik, siapa pun
  yang memegang sesi itu bisa melakukan enam tindakan ringan, termasuk membuat
  token pasang ulang agent (lalu menerima rahasia server itu). Baris audit
  `agent_pair` dan `window: true` mencatatnya.
- **Browser yang belum pernah login** tetap bisa dikunci dari luar, dan
  serangan dari banyak alamat masih bisa memenuhi slot pemeriksaan login
  anonim. Cookie perangkat yang dicuri memberi 5 tebakan per 15 menit.
- **Promote "selesai sebagian" yang kandidatnya tidak pernah kembali** tetap
  menahan promote lain di topologi itu (F11). Batas ini sudah ada dan tidak
  diubah; rancangan ini hanya memastikan ia tidak bertambah.
- **Jendela "kunci mungkin berdiri" tetap ada.** Bila master mati sesudah
  promote terencana gagal dan sebelum kuncinya terbukti dibuka, promote
  darurat menunggu sampai tenggat kunci lewat (paling lama sekitar 36 menit
  sejak kunci diminta). Batas ini sudah ada; yang berubah: ia tidak bisa lagi
  digeser dengan "Ulangi" atau diperpanjang agent master.
- **Buka kunci promote bisa ditolak agent** (kandidat mati, replika tidak
  terbaca). Jalan yang tersisa adalah `unlock-master` oleh root, seperti
  sekarang; T11 memberi syaratnya.
- **R-188.** Hanya 8.0.46 yang terukur. Server dengan
  `persisted_globals_load=OFF` mengabaikan nilai tersimpan: master yang pernah
  dipromote tetap bisa menyala baca-saja di sana. Nilai OFF tersimpan
  mengalahkan `read_only=ON` yang kelak ditulis orang ke my.cnf. Master yang
  agentnya belum diperbarui tidak menampilkan peringatan. Aturan field bisa
  salah untuk kombinasi yang tidak diukur: peringatan palsu atau tenang palsu.
- **R-187.** Operator yang terbiasa mengetik `ya` tetap bisa salah terminal.
  `set-role master` dari `unassigned` tidak bertanya. Server ber-pin master
  yang masih mereplikasi tetap bisa dibuka panel.
- **R-203.** Panel yang dibobol tidak ditahan selama percobaan berjalan, dan
  persetujuan yang belum dipakai hidup sampai 24 jam bila panel itu tidak
  memintanya berakhir.
- **R-202.** Berkas kunci dari agent lama dibaca lewat nama (paling lama 60
  menit). Alamat replika yang berubah selama kunci memperpanjang henti tulis
  master sampai root bertindak.
- **R-185.** Master lama yang agentnya sendiri dibobol tetap bisa mengabaikan
  karantina.
- **R-186.** Handler yang macet menahan slot sampai agent direstart.
- **R-200.** Folder di luar daftar yang ditolak tetap bisa dipakai panel yang
  dibobol untuk mengetahui ada-tidaknya, umur dan ukuran berkas.
- **R-201.** Start layanan MySQL oleh agent tetap membawa lingkungan root;
  membersihkannya bisa mengubah cara `mysqld_safe` menemukan konfigurasinya,
  dan itu tidak terukur.
- **R-183.** Panel yang dibobol seluruhnya tetap bisa menampilkan spanduk apa
  pun. Pemeriksaan di sisi agent (`set-source` mencocokkan identitas master)
  dicatat untuk Rencana 5c.
- **R-191.** Batalkan, Stop replika, Sudah saya alihkan dan Pengaturan
  (termasuk Telegram) tetap tanpa kata sandi; semuanya diaudit.
- **R-189.** CSP baru hanya terbukti di Chromium.
- **R-193, R-194.** Belum ada halaman ganti kata sandi (R-042); log audit
  tidak dipangkas (K6).
- **Bagian A tanpa bagian B.** Sampai bagian B selesai: centang cadangan T8
  bisa dihindari dengan laporan lama, dan penolakan R-198 masih memakai
  laporan tersimpan.
