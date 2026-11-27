# Rencana 5b-3: Sisa Audit Rencana 5b-2

- Tanggal: 2026-11-27
- Status: menunggu persetujuan pemilik; diubah sesudah builder check, tinjauan
  rancangan dan pemeriksaan ulangnya (bagian 9)
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
   tidak pernah tertinggal baca-saja tanpa tombol di panel atau, di satu
   keadaan, perintah root yang disebut panel (R-184).
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
- Kata sandi untuk senyap (Jangan ganggu), **Batalkan**, **Sudah saya
  alihkan** dan Pengaturan selain pemberitahuan Telegram (5.1).
- Cara menutup dari panel promosi "selesai sebagian" yang kandidatnya tidak
  pernah kembali (bagian 8).
- Halaman ganti kata sandi, retensi log audit, akun replikasi per replika.

**Anti-tujuan** (kalau ini terjadi, rencana gagal walau test lulus)

- Master tertinggal baca-saja tanpa tombol di panel yang bisa membukanya atau
  menyebut perintah root yang membukanya. Hanya satu keadaan memakai perintah
  root sebagai jalan keluar: promote yang perintahnya sudah terkirim dan
  kandidatnya tidak pernah menjawab lagi, selagi master masih terkunci (5.2,
  baris 5 dan bentuk majemuknya di baris 7).
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
| 1 | Kata sandi panel, sekali per 10 menit | Buat token pasang ulang agent (juga saat memperbarui agent); ubah alamat master untuk replika; setujui restart MySQL master; Siapkan sebagai master pertama; Hubungkan MySQL yang mengganti akun tersimpan; Stop replika; ubah pemberitahuan Telegram | Sesi yang dicuri atau ditinggal tidak boleh bisa melakukannya (R-182, R-183, R-191) |
| 2 | "Ulangi" promote terencana sebelum master diganti: kata sandi setiap kali, daftar Prasyarat dibaca lagi, centang cadangan bila diminta; dan baru sesudah kunci master terbukti dibuka | Sesudah promote terencana gagal | Sama dengan memulainya (R-184) |
| 3 | Promote darurat baru (centang, nama, kata sandi), bukan satu klik "Ulangi" | Promote darurat yang gagal sebelum master baru dibuka, termasuk yang "selesai sebagian" dan terbukti tidak terjadi | Satu klik berhari-hari kemudian bisa membuat dua master (SEC-288). Sudah dilihat pemilik |
| 4 | Promote terencana baru, bukan "Ulangi" | "Ulangi" lebih dari 24 jam sesudah konfirmasi terakhir | Aturan yang sama dengan pekerjaan yang membuang data |
| 5 | Tombol **Buka kunci promote** (tanpa kata sandi) lebih dulu; baru sesudah itu "Ulangi", promote terencana baru, Resync, Buat replika, Tambah atau Lepas database dengan data | Promote terencana gagal dan master masih terkunci, juga sesudah tenggat kunci lewat | Membuka kunci tidak lagi menjalankan promote; memuat data selagi kunci berdiri membuat penjaga mengkarantina master yang sehat |
| 6 | Perintah root `unlock-master` di master, sesudah memastikan kandidat | Promote terencana yang perintahnya sudah terkirim dan kandidatnya tidak pernah menjawab lagi; atau promote darurat seperti itu selagi master lama kembali masih terkunci | Tombol di keadaan itu bisa membuat dua master. Satu-satunya keadaan yang jalan keluarnya perintah root |
| 7 | Setiap replika lain harus terbaca, atau dilepas | Mulai dan "Ulangi" promote terencana | Replika yang tidak terbaca mungkin master yang tidak dikenal; pekerjaan terencana bukan jalur gangguan |
| 8 | Mengetik `ya` pada `set-role` | Meninggalkan peran master; masuk karantina; replika dijadikan master; server tanpa peran dijadikan master selagi MySQL-nya masih punya kanal replikasi | Terminal yang salah menghentikan master produksi (R-187). Sudah dilihat pemilik |
| 9 | Mengetik `ya` pada `unlock-master` | Server berdiri di bawah kunci promote sebuah kandidat | Perintah itu melewati semua pemeriksaan agent |
| 10 | Root menjalankan `approve` lagi | Sesudah bangun ulang master lama gagal atau dibatalkan | Persetujuan tidak boleh hidup lebih lama dari percobaannya (R-203). Sudah dilihat pemilik |
| 11 | Satu centang cadangan | Promote terencana atau bangun ulang selagi keadaan folder cadangan belum diketahui | Mengganti folder tidak boleh menghilangkan centang (R-191) |
| 12 | Kata sandi panel untuk **Perbaiki pengaturan tersimpan** | Sekali, pada master yang sudah terkena R-188 | Tindakan yang sama dengan **Buka kunci master** |
| 13 | Perintah pasang ulang agent tampil sekali; yang hilang dibuat lagi | Pasang ulang dan pembaruan agent | Perintah yang tetap tampil bisa dibaca sesi mana pun (R-182) |
| 14 | Satu tautan lagi ke panduan untuk melihat SQL pembuka | Sesudah **Lepas replika** | SQL itu tidak lagi tampil tanpa syarat (R-190) |
| 15 | Memilih folder cadangan lain | Folder di bawah direktori sistem (bukan `/usr/local`) | Agent menolaknya (R-200) |
| 16 | Login sekali dari tiap browser yang akan dipakai saat gangguan | Sesudah pembaruan | Cookie perangkat (R-181) |

**Yang bisa menahan operator saat gangguan, dan mengapa tidak**

| Perubahan | Cara ia bisa menahan | Mengapa tidak, atau bagaimana dicegah |
|---|---|---|
| Permintaan kata sandi baru | Diblokir login salah dari luar | 5.1 dikerjakan paling dulu: konfirmasi punya anggaran per sesi; login ber-cookie perangkat tidak memakai batas bersama |
| Kata sandi untuk alamat master (dipakai sesudah promote darurat ke host NAT) | Satu permintaan lagi di tengah gangguan | Konfirmasi promote darurat membuka jendela 10 menit; di dalamnya tidak diminta lagi. Sesudahnya satu permintaan, yang tidak bisa diblokir dari luar (5.1) |
| "Ulangi" promote bergerbang | Promote setengah jalan tertahan; master lama tidak dikarantina | Gerbang dan kata sandi hanya sebelum `mysql.promote` dikirim; sesudah titik tak kembali tidak pernah (5.2), dengan test |
| "Ulangi" tidak lagi membuka kunci | Master baca-saja tanpa tombol (N1) | **Buka kunci promote**: tanpa kata sandi, tanpa gerbang, tanpa batas waktu, selama `mysql.promote` belum dikirim; bila agent menolak, teksnya menyebut sebab dan, bila aman, perintah root |
| Promote terencana, perintah terkirim, kandidat tidak pernah menjawab | Master baca-saja dan panel tidak punya tombol | Jalan keluarnya perintah root `unlock-master` dengan syarat tertulis (T14t). Disengaja: tombol di sini bisa membuat dua master. Satu-satunya keadaan begitu |
| `mysql.promote` terkirim tanpa jawaban: "Ulangi" menunggu bukti | Promote darurat ke replika lain tertahan selama panel belum tahu pekerjaan di kandidat berakhir | Agent versi ini melaporkan ada-tidaknya pekerjaan itu begitu ia menjawab; batas B hanya untuk agent yang lebih lama. Tanpa aturan ini dua replika bisa dipromosikan |
| `ya` pada `unlock-master` | Satu kata lagi selagi master tidak bisa ditulis | Disengaja; hanya bila kunci sebuah kandidat berdiri; `--ya` ada untuk skrip |
| Muat data dan promote terencana baru ditolak selama kunci promote belum terbukti dibuka, juga selagi master belum punya laporan langsung | Resync atau promote tertahan, beberapa detik sesudah panel restart | Bukan jalur gangguan; catatan kunci hilang sendiri begitu master melapor bisa ditulis sesudah tenggat; promote darurat tetap memakai aturan jam, tidak lebih lama dari sekarang |
| Promote terencana menolak replika yang tidak terbaca | Promote tertahan oleh replika offline | Hanya promote terencana; promote darurat tidak ditahan replika offline yang laporan terakhirnya baca-saja |
| Promote darurat "selesai sebagian" bertanya kandidat dulu | Agent kandidat tidak menjawab | Ditolak tanpa mengubah apa pun; sama dengan hari ini, saat `mysql.promote` ulang juga gagal. Kandidat yang sudah bisa ditulis: satu klik seperti sekarang |
| Batas permintaan agent | Agent menjawab "sibuk" pada perintah promote | Batas 64, pemakaian panel yang jujur segelintir; penjaga tidak lewat batas itu; "sibuk" adalah penolakan pasti, bukan hasil tak diketahui |
| Batas sambungan UI | Operator ditolak selagi klien lambat memenuhi panel | Batas per alamat (64), bukan hanya batas total (5.8) |
| Gerbang menolak laporan basi | Baris merah sesudah restart panel | Promote darurat tidak pernah ditahan gerbang; yang lain kuning, hanya pin master merah, paling lama satu selang laporan |
| Alamat replika tercatat di kunci (R-202) | Alamat replika berubah selama kunci (DHCP): kunci dipertahankan lewat tenggat | Arah aman. Jalan keluar: **Buka kunci promote** menampilkan sebabnya; root memakai `forget-replica` atau `unlock-master` |
| `ya` pada `set-role quarantined` | Beberapa detik lebih lama saat mengkarantina master lama dengan tangan (panduan "Replika yang tersambung di luar alur panel") | Disengaja: langkah inilah yang salah terminal di R-187; `--ya` ada untuk skrip |
| Persetujuan berakhir saat gagal | Satu perjalanan lagi ke terminal | Master lama tidak melayani aplikasi; bukan jalur gangguan |

## 3. Fakta yang menentukan desain

Dari penyelidikan kode 2026-11-27 pada `8caee75`; "(penulis)" berarti dibaca
penulis sendiri dari kode yang sama, "(tinjauan)" berarti ditambahkan tinjauan
rancangan dan dicocokkan penulis dengan kode. Nomor baris di audit sudah
bergeser; di sini ditulis nama fungsi.

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
  pun, juga darurat, ditolak selama pengalihan belum dikonfirmasi; senyap
  (Jangan ganggu) hanya menerima 1, 2, 8 atau 24 jam dan diaudit.
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
  membuat dua master, selama replika yang dipromosikan ada di daftar itu.
  (tinjauan) Daftar itu hanya memuat replika yang terbaca saat kunci dipasang;
  dan selama kunci dipertahankan, penjaga membaca kandidat tiap menit dan
  mengkarantina master bila kandidat terbaca bisa ditulis dengan transaksi
  master.
- F11. "Terkirim, hasil belum diketahui" (`PromoteSent && !PastPoint`):
  `promoteFailed` menanyai kandidat saat gagal; `resumePromote` (sesudah
  restart panel) tidak; `Retry` mengirim `mysql.promote` lagi tanpa bertanya.
  (penulis) Promosi "selesai sebagian" menolak setiap promote baru di
  topologinya. (tinjauan) Tanda "panggilan mulai tidak berjawab" hanya hidup
  di memori satu percobaan; percobaan berikutnya, atau panel sesudah restart,
  tidak memegang id pekerjaan agent dan menganggap pekerjaan itu sudah
  berakhir, padahal agent kandidat mungkin masih di dalamnya.
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
  apakah server itu masih mereplikasi. (tinjauan) `unlock-master` di bawah
  kunci sebuah kandidat mencetak peringatan lalu langsung membuka, tanpa
  bertanya.
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
merepotkan?") dan memilih pilihan 2 dari tiga yang diperlihatkan kepadanya;
bunyinya dikutip di bawah.

- A. Pilihan 1: "Minta setiap kali (usulan awal). Paling aman, paling banyak
  mengetik." Sesi yang dicuri tidak bisa apa pun tanpa kata sandi. Harganya
  permintaan baru di setiap tempat itu, beberapa berturutan dalam satu
  pekerjaan (pasang ulang agent, lalu Hubungkan MySQL). Operator yang terbiasa
  mengetik kata sandi di setiap dialog berhenti membaca dialognya.
- **B (dipilih pemilik). Pilihan 2: "Minta sekali, berlaku 10 menit untuk
  tindakan ringan. Tindakan yang menghapus data atau mengganti master
  (promote, bangun ulang, hapus) tetap meminta setiap kali, seperti sekarang.
  Kelemahannya: bila admin meninggalkan browser dalam 10 menit itu, orang lain
  bisa melakukan tindakan ringan tadi."**
- C. Pilihan 3: "Tidak menambah permintaan kata sandi. Risiko sesi curian
  tetap terbuka." Tidak menutup R-182 dan R-183.

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
  tanpa gerbang, tanpa batas waktu, selama `mysql.promote` belum dikirim dan
  pekerjaannya belum digantikan; pemeriksaan agent (F10) yang menjaga. (2)
  "Ulangi" sebelum `mysql.promote` dikirim diperlakukan seperti memulai
  (penolakan yang sama, Prasyarat, centang cadangan, kata sandi setiap kali,
  batas 24 jam) dan ditolak selama kunci belum terbukti dibuka. (3) Sesudah
  titik tak kembali "Ulangi" tidak pernah digerbang. Untuk keadaan di
  antaranya (terkirim, hasil tak diketahui) panel mencari bukti di kandidat
  dulu, dan tidak ada tombol buka kunci. **Lolos N1**: selama perintah belum
  dikirim tombol buka kunci selalu ada, dan jalan keluarnya sekuat "Ulangi"
  hari ini (perintah agent yang sama, pemeriksaan agent yang sama) tanpa
  menjalankan promote. Harganya: satu tombol dan beberapa teks baru, tugas
  terbesar rencana ini, dan satu keadaan (terkirim, kandidat diam) yang jalan
  keluarnya perintah root, karena di sana tombol apa pun bisa membuat dua
  master.

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
  konfirmasi. Jawaban 429 menyebut sisa waktu yang sebenarnya dan jalan
  keluarnya (T1).
- **Login dengan cookie perangkat.** Login yang berhasil memasang cookie
  `__Host-` (HttpOnly, Secure, SameSite=Lax, satu tahun) berisi nama pengguna,
  nomor acak dan waktu terbit, bertanda tangan HMAC dengan kunci yang
  diturunkan dari `panel.key`. Login yang membawa cookie sah untuk nama
  pengguna yang diketik **tidak** ditolak batas nama pengguna, batas alamat,
  pembatas yang penuh, maupun slot pemeriksaan kata sandi bersama (ia punya
  dua slot sendiri). Ia dihitung pada anggarannya sendiri: 5 salah per 15
  menit, dengan kunci nomor cookie **dan** alamat klien, dan tempatnya dipesan
  sebelum pemeriksaan kata sandi. Anggaran yang habis menolak login untuk
  pasangan cookie dan alamat itu saja sampai jendelanya lewat; ia tidak jatuh
  ke aturan tanpa cookie. Di atasnya ada jumlah per cookie untuk semua alamat:
  20 salah per 15 menit. Bila jumlah itu habis, yang ditolak hanya pasangan
  cookie dan alamat yang **belum pernah** berhasil login sejak panel menyala;
  pasangan yang sudah pernah berhasil tetap memakai anggaran 5-nya sendiri.
  Login tanpa cookie memakai aturan sekarang. Cookie itu bukan kredensial:
  kata sandi tetap diminta. Ia ditolak bila tanda tangannya salah, namanya
  lain, terbit sebelum `reset-password` terakhir, atau lebih tua dari setahun.
  Logout tidak menghapusnya.
- Batas per alamat memakai awalan /64 untuk IPv6. README "Keamanan" ditulis
  ulang: restart bukan obat; panel hanya dijangkau dari jaringan admin (B2).
- Tepi. Browser yang belum pernah login tetap terkunci selama serangan,
  seperti sekarang; obatnya firewall. Panel restart: pembatas kosong (seperti
  sekarang), cookie perangkat tetap sah. `panel.key` diganti: cookie tidak sah
  sampai login berikutnya. Sesi curian adalah sesi **yang sama** dengan sesi
  admin selama admin belum login lagi: 20 tebakan salah si pencuri menolak
  konfirmasi admin juga. Jalan keluarnya keluar lalu login lagi: logout
  mengakhiri sesi yang dicuri, dan sesi baru punya anggaran baru (T1
  mengatakannya). Pencuri cookie perangkat mendapat paling banyak 20 tebakan
  per 15 menit, berapa pun alamat yang ia pakai, dan tidak mengunci browser
  admin yang sudah pernah berhasil login dari alamatnya sejak panel menyala.
  Yang masih bisa ia lakukan: sesudah panel restart, sebelum admin berhasil
  login sekali, ia bisa menghabiskan jumlah itu dan menahan login ber-cookie
  admin sampai 15 menit, berulang; dan selama dua slot pemeriksaan cookie ia
  penuhi, login ber-cookie lain menunggu sebentar.
- Verifikasi (unit, dengan jam): 20 login salah dari empat alamat, lalu
  konfirmasi benar dari sesi yang ada tetap 200 dan login benar ber-cookie
  tetap 303; cookie palsu, milik nama lain, atau dari sebelum `reset-password`
  ditolak; anggaran cookie yang habis di satu alamat tidak menolak alamat
  lain; jumlah per cookie yang habis menolak alamat baru tetapi tidak alamat
  yang sudah pernah berhasil; teks 429 menyebut menit yang benar.

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
  seperti itu saat dipakai. Untuk token pekerjaan (`job_retry`, `job_approve`)
  ringan atau berat ditentukan dari pekerjaannya saat token dipakai.
- Tindakan berat: semua yang hari ini meminta kata sandi per perintah
  (promote, Resync, bangun ulang, hapus, Lepas replika, Buka kunci master,
  sertifikat, salvage, Setujui dan Ulangi pekerjaan yang membuang data),
  **ditambah "Ulangi" promote terencana sebelum master diganti**: ia
  menuntaskan pergantian master, dan aturan yang dipilih pemilik berkata
  tindakan seperti itu meminta setiap kali.
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
| Siapkan sebagai master pertama | jendela | Mengubah pengaturan MySQL yang mungkin sudah dipakai aplikasi. Langkah penyiapan, bukan jalur gangguan |
| Hubungkan MySQL pada server yang **sudah** punya akun tersimpan | jendela | Mengganti akun yang dipakai penjaga dan promote; sesi curian dengan akun aplikasi berhak rendah bisa melumpuhkan agent server itu. Hubungan pertama tidak diminta |
| Stop replika | jendela | Bersama membisukan pemberitahuan, replika yang dihentikan diam-diam berarti data hilang saat promote darurat berikutnya. Jarang dipakai |
| Pengaturan yang mengubah tujuan Telegram (token bot, chat id) atau mematikan jenis peringatan yang dikirim | jendela | Membisukan peringatan. Jarang dipakai |
| "Ulangi" promote terencana sebelum master diganti | kata sandi **setiap kali** (berat), sesudah Prasyarat | Menuntaskan pergantian master (5.2) |
| Jangan ganggu (senyap) | tidak | Paling sering dipakai dari ketiga cara membisukan; sudah dibatasi paling lama 24 jam per kali dan diaudit |
| Sudah saya alihkan | tidak | Pernyataan, bukan perintah ke server; yang dibukanya (bangun ulang, promote berikutnya) meminta kata sandi sendiri; dan ia ada di jalur gangguan kedua (F6) |
| Batalkan | tidak | Jalan keluar operator sebelum promote darurat (K2 Rencana 5b-2b) |
| Pengaturan lain | tidak | Sering disimpan; nilai lama dan baru kini diaudit |
| Token untuk server yang belum ter-pairing | tidak | Tidak ada agent yang digantikan |

**Pasang ulang agent (R-182).** Tombol **Pasang ulang agent** dan **Buat token
baru** pada server yang sudah ter-pairing menjadi tombol berkonfirmasi (pola
**Lepas replika**), teks T3. Perintah pemasangannya ditampilkan **sekali**, di
jawaban atas POST yang membuatnya; halaman server dan daftar server tidak
menyusunnya lagi pada GET berikutnya, dan hanya berkata token aktif sampai
pukul berapa (T3b). Tanpa itu perintah yang dibuat admin bisa dibaca sesi mana
pun tanpa kata sandi (F4). Token pasang ulang berlaku 15 menit, bukan 24 jam.
Perintah yang hilang dibuat lagi; di dalam jendela itu tanpa permintaan.
Server yang belum ter-pairing tidak berubah. Pairing yang berhasil menulis
baris audit `agent_pair`: alamat, menggantikan agent lama atau tidak, nomor
seri sertifikat, versi dan arsitektur agent, `hostname` dipotong 255 rune satu
baris. Potongan yang sama berlaku untuk baris penolakan dan journal. Agent
0.2.0 berpasangan tanpa perubahan. Verifikasi (unit): token untuk server
ter-pairing tanpa konfirmasi → 403, tidak ada token; GET sesudah POST tidak
memuat token; server belum ter-pairing tetap tanpa kata sandi; `hostname` 60
KiB terpotong; satu baris `agent_pair` per pairing.

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

**Stop replika dan Telegram.** **Stop replika** menjadi tombol berkonfirmasi
(T30). Simpan Pengaturan meminta konfirmasi (T31) hanya bila token bot atau
chat id berubah, atau sebuah jenis peringatan dimatikan; simpanan lain lewat
seperti sekarang. Senyap tidak berubah: pilihannya tetap 1, 2, 8 atau 24 jam,
dan setiap kali diaudit. Verifikasi (unit): masing-masing tanpa token → 403;
Pengaturan yang hanya mengubah ambang lewat tanpa token.

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

**Aturannya: selama `mysql.promote` belum dikirim, membuka kunci hanya butuh
satu klik; menjalankan promote lagi butuh semua yang dibutuhkan saat
memulainya; menyelesaikan promote yang sudah mengganti master tidak pernah
ditahan; dan panel tidak pernah menyimpulkan "tidak ada yang terjadi" tanpa
bukti.**

| Keadaan pekerjaan promote | "Ulangi dari tahap ini" | **Buka kunci promote** |
|---|---|---|
| 1. Terencana, gagal, belum dikirim, kunci tidak pernah diminta atau **sudah terbukti dibuka** | Seperti memulai: penolakan yang sama dengan mulai, daftar Prasyarat, centang cadangan bila diminta, kata sandi **setiap kali**; berjalan dari langkah "Kunci master". Lebih dari 24 jam sejak konfirmasi terakhir: ditolak (T12) | Tidak perlu |
| 2. Terencana, gagal, belum dikirim, kunci **belum terbukti dibuka** | Ditolak (T12b): buka kuncinya dulu | Tampil |
| 3. Terencana, dibatalkan (pembatalan hanya mungkin sebelum dikirim) | Tidak ada | Tampil selama kunci belum terbukti dibuka |
| 4. Darurat, gagal sebelum dikirim | Tidak ada, seperti sekarang: promote darurat baru | Tidak ada kunci |
| 5. `mysql.promote` terkirim, hasil belum diketahui (kedua jenis) | Panel mencari bukti di kandidat dulu (di bawah) | **Tidak ada, dan ditolak bila diminta.** Jalan keluar bila kandidat tidak pernah menjawab: perintah root (T14t) |
| 6. Sesudah titik tak kembali (kedua jenis) | Seperti sekarang: tanpa gerbang, tanpa kata sandi | Tidak ada |
| 7. Majemuk: promote terencana gagal dengan kunci dipertahankan; lalu promote darurat terkirim dan kandidatnya diam; lalu master lama kembali, masih terkunci dan masih master topologi | Pekerjaan darurat: baris 5, dengan ekor T14u selama master lama tersambung dan terbaca "Terkunci" | Pada pekerjaan terencana lama: ditolak dengan T11t (selesaikan dulu promote yang lebih baru). Jalan keluar: perintah root (T14u) |

- **Buka kunci promote** mengirim `mysql.unlockPromote` dengan kredensial
  kandidat, seperti jalur gagal sekarang. Berhasil, atau agent menjawab "tidak
  dikunci untuk promosi": panel mencatat kunci terbuka; promote darurat, muat
  data dan "Ulangi" boleh jalan saat itu juga (T10, T10b). Tanpa kata sandi
  dan tanpa gerbang: tindakan ini hanya mengembalikan tulisan ke master yang
  sehat, agent yang memeriksa (F10), dan kata sandi di sini memperpanjang
  henti tulis yang dibuat panel sendiri. Diaudit.
- **Kapan tombol itu ada, dan kapan ditolak.** Aturan ini ditegakkan di
  handler dan di orchestrator, bukan hanya di templat:
  - hanya untuk pekerjaan promote terencana yang `mysql.promote`-nya **belum
    dikirim** (baris 2 dan 3);
  - ditolak **tanpa** menyebut `unlock-master` bila pekerjaan itu sudah
    digantikan. Promosi yang lebih baru di topologi itu sudah selesai, atau
    master pekerjaan itu bukan lagi master topologinya: T11s. Promosi yang
    lebih baru masih berjalan atau "selesai sebagian": T11t, yang menunjuk
    pekerjaan itu. Tanpa aturan ini satu klik sesudah promote darurat ke
    replika lain bisa membuka master lama di samping master baru;
  - ditolak selagi pekerjaan promote topologi itu berjalan.
- **Di mana tombol itu.** Di halaman pekerjaannya (dengan kalimat T9), dan di
  halaman master selama statusnya "Terkunci". Halaman master memakai pekerjaan
  promote terencana terbaru master itu yang kuncinya belum terbukti dibuka;
  bila catatan panel tidak menunjuk satu pun, ia memakai promote terencana
  terakhir master itu yang gagal atau batal, belum dikirim dan belum
  digantikan. Catatan panel bisa salah; agent yang tahu. Bila pekerjaan yang
  ditunjuk catatan itu sudah dikirim (baris 5), halaman master menampilkan T14
  dengan ekornya dan tidak menampilkan tombol. **Buka kunci master** tetap
  tampil di sampingnya.
- **Jawaban agent yang menolak** dipilih dari keadaan, bukan dari teks agent:
  panel membaca kandidat (`mysql.status`) dan peran master, lalu menampilkan
  teks agent berlabel dengan salah satu ekor ini. Kandidat baca-saja, tidak
  terbaca, atau kredensialnya tidak bisa disusun panel: T11, dengan
  `unlock-master` dan syaratnya. Kandidat bisa ditulis: T11c, tanpa perintah
  itu. Master sudah dikarantina: T11d. Agent master tidak tersambung: T11b.
- **Catatan kunci (bendera "kunci diminta, belum terbukti dibuka").** Jawaban
  galat dari master tidak membuktikan tidak ada kunci: agent bisa menjawab
  galat sesudah kunci terpasang. Karena itu sesudah kunci diminta, catatan itu
  tetap berdiri sampai salah satu dari dua hal: `mysql.unlockPromote` menjawab
  "dibuka" atau "tidak dikunci"; atau, sesudah tenggat terpotong + kelonggaran
  lewat, sebuah laporan langsung master (5.7) menunjukkan kedua bendera OFF.
  Kunci promote yang berdiri menjaga `super_read_only` tetap ON (penjaga
  memasangnya lagi tiap putaran), jadi laporan itu bukti kunci sudah tidak
  ada; ini juga menghapus klik tambahan sesudah penjaga membuka master
  sendiri. Kunci ulang yang dijawab penolakan hanya mengembalikan cap waktu ke
  nilai sebelumnya (bila ada), supaya jendela tidak tergeser. Tenggat dari
  jawaban agent master dipotong ke waktu kunci diminta + lama kunci yang
  diminta + kelonggaran panggilan + kelonggaran jam; potongan yang sama
  berlaku saat membaca pekerjaan tersimpan dari 0.2.0, dan nilai terpotong
  itulah yang dikirim ke kandidat (sisa waktu lebih pendek: kandidat menolak,
  bukan membuka terlambat). Pemeriksaan "kunci mungkin berdiri" membaca
  **setiap** promote terencana master itu yang catatannya masih berdiri, bukan
  hanya yang terakhir. Sebuah catatan hanya dihitung selama server itu masih
  master topologinya dan belum ada promosi topologi itu yang **selesai**
  sesudah pekerjaannya; promosi yang lebih baru yang masih berjalan atau
  "selesai sebagian" tidak melepas catatan (master itu masih bisa terkunci,
  baris 7). Tanpa dua aturan ini catatan yang tidak bisa lagi dihapus tombol
  (pekerjaan digantikan, kandidat mati sudah dihapus dari panel) akan menolak
  setiap promote terencana berikutnya dari master itu.
- **Selagi catatan itu berdiri:**
  - "Ulangi" pekerjaan itu ditolak selama `mysql.promote`-nya belum dikirim
    (baris 2). Pekerjaan di baris 5 dan 6 tidak pernah ditolak karena catatan
    kunci, walau catatannya masih berdiri. Jadi "Ulangi" tidak lagi membuka
    kunci sebagai efek samping, tidak bisa menggeser jendela, dan master sudah
    bisa ditulis sebelum operator memutuskan mencoba lagi.
  - Promote terencana **baru** dari master itu ditolak saat mulai (T12c), juga
    bila master terbaca "Terkunci" tanpa catatan (T12d).
  - Promote darurat memakai aturan jam seperti sekarang: ditolak sampai
    tenggat + kelonggaran, tidak lebih lama (T15).
  - Muat data di topologi itu (Resync ulang, Buat replika, Tambah atau Lepas
    database dengan data, dan "Ulangi"-nya) ditolak (T15b). Aturan ini
    **menambah** aturan jam, tidak menggantikannya: di dalam jendela jam muat
    data ditolak apa pun kata laporan, seperti sekarang; sesudahnya ia ditolak
    selama catatan berdiri, dan catatan hanya hilang oleh jawaban buka kunci
    atau oleh laporan langsung master dengan kedua bendera OFF (butir di
    atas). Tidak ada laporan langsung (panel baru menyala, laporan pertama
    belum datang, agent master terputus dari panel) berarti ditolak: "Ulangi"
    sebuah muat data bisa berjalan tanpa agent master, sementara penjaga
    master tetap membaca kandidat tiap menit dan akan mengkarantina master
    yang sehat bila kandidat yang sedang dimuat terbaca bisa ditulis.
- **"Ulangi" sebelum dikirim** (baris 1) memanggil fungsi penolakan yang sama
  dengan `StartPromote` (replika lain bisa ditulis dengan pembacaan langsung
  5.7, kandidat layak, replika tak terjangkau, cabang, versi agent, master
  lama yang belum dibereskan), lalu gerbang, lalu token kata sandi.
- **Terkirim, hasil belum diketahui** (baris 5). Keadaan pekerjaan kini
  menyimpan kapan `mysql.promote` dikirim dan apakah panggilan mulainya
  **tidak berjawab**. Sebelum setiap pengiriman panel menghapus id pekerjaan
  agent yang tersimpan dari percobaan sebelumnya dan memasang tanda "tidak
  berjawab"; tanda itu hanya dihapus oleh jawaban yang membawa id pekerjaan.
  Hari ini tanda itu hanya ada di memori satu percobaan (F11). Pekerjaan
  tersimpan dari 0.2.0 yang terkirim tanpa id pekerjaan agent dihitung tidak
  berjawab, dengan waktu pembaruan terakhirnya sebagai waktu kirim. "Ulangi"
  membaca kandidat (berbatas waktu, di luar `startMu`, lalu diperiksa ulang di
  bawahnya):
  - kandidat bisa ditulis → dicatat lewat titik tak kembali; promote
    dilanjutkan tanpa kata sandi dan tanpa gerbang (menuntaskan karantina
    master lama adalah arah aman);
  - kandidat baca-saja **dan ada bukti pekerjaan agentnya sudah berakhir** →
    tidak ada yang terjadi di kandidat: pekerjaan digagalkan; pada promote
    terencana kunci dibuka; jawabannya T13 atau T13b. Yang dihitung bukti: (a)
    panel memegang id pekerjaan agent dan `job.status` berkata berakhir atau
    tidak dikenal agent; (b) `mysql.status` kandidat membawa field opsional
    baru "permintaan promote: tidak ada"; (c) panggilan tidak berjawab, agent
    tidak mengirim field itu, dan sudah lewat batas B sejak dikirim.
    `mysql.status` yang berkata baca-saja **bukan** bukti, dan field yang
    berkata "ada" mengalahkan (a) dan (c);
  - selain itu → ditolak tanpa mengubah apa pun: T14 (kandidat tidak terbaca)
    atau T14b (masih berjalan, atau belum terbukti berakhir). Promosi tetap
    "selesai sebagian", jadi promote lain tetap ditolak.
- **Arti field itu.** "Ada" berarti: agent ini sudah **membaca** sebuah
  permintaan `mysql.promote` dan pekerjaannya belum berakhir. Tanda itu
  dipasang di putaran baca agent saat permintaan tiba, sebelum permintaan
  berikutnya dibaca, bukan saat handler mendaftarkan pekerjaannya: handler
  bekerja beberapa detik sebelum itu (pemeriksaan pin, pelepasan kunci start),
  dan `mysql.status` yang dijawab di celah itu dari daftar pekerjaan akan
  berkata "tidak ada". Agent menyambung ulang baru sesudah sesi lamanya
  berhenti membaca, jadi permintaan yang tiba lewat sambungan lama sudah
  tercatat sebelum `mysql.status` di sambungan baru dijawab; task memeriksa
  urutan itu.
- **Batas B** bergantung pada arti itu dan harus lebih panjang dari seluruh
  batas pekerjaan promote di agent, dihitung sejak permintaan dibaca. Task ini
  memeriksa bahwa setiap langkah pekerjaan itu memang berbatas dan menetapkan
  angkanya. Yang terlihat di kode sekarang: tiga probe berjarak 10 detik dan
  tunggu relay log 600 detik; usulan awal rancangan 30 menit, dan tidak kurang
  dari dua kali jumlah batas yang ditemukan. Agent versi ini selalu mengirim
  field (b), jadi B hanya dipakai untuk pekerjaan yang dikirim ke agent
  sebelum pembaruan.
- **Keadaan majemuk** (baris 7). Tombol pekerjaan terencana lama ditolak
  dengan T11t, karena promote darurat yang lebih baru belum tuntas; pekerjaan
  darurat itu menjawab T14 atau T14b, dan selama master lamanya tersambung dan
  terbaca "Terkunci" kedua teks itu membawa ekor T14u: perintah root yang
  sama, dengan syarat untuk kedua kandidat.
- **Kandidat tidak pernah menjawab lagi** (baris 5, promote terencana): master
  tetap baca-saja, karena penjaganya mempertahankan kunci selama kandidat
  tidak terbaca. Panel tidak punya tombol untuk keadaan ini: membuka master
  selagi perintah promote mungkin masih berjalan di kandidat bisa membuat dua
  master. Jalan keluarnya perintah root di master, `sudo dbpanel agent
  unlock-master`, dengan syarat yang ditulis di T14t. **Keadaan ini, dan
  bentuk majemuknya di baris 7, satu-satunya di rancangan ini yang jalan
  keluarnya perintah root.** Promosinya tetap "selesai sebagian" (bagian 8).
- **Audit.** "Ulangi" promote terencana dicatat dengan field yang sama dengan
  mulainya; "Ulangi" sesudah titik tak kembali dicatat sebagai lanjutan;
  **Buka kunci promote** dicatat dengan hasilnya.
- **Teks.** Peringatan kunci yang dipertahankan, baris log gagal membuka, dan
  kalimat promote yang dibatalkan menunjuk tombol baru dan melarang Resync
  kandidat selagi master terkunci (T16, T17). Kalimat README dan Panduan
  tentang "Ulangi" ditulis ulang, dan "Setujui" disebut (B1, R-195).
- **Tepi.** Pekerjaan promote gagal atau "selesai sebagian" dari 0.2.0
  mengikuti aturan baru pada "Ulangi" berikutnya; tidak ada migrasi. Satu
  tambahan protokol: field opsional di `mysql.status`. Sesi curian bisa
  menekan **Buka kunci promote**: akibat terburuknya master yang sehat menulis
  lagi, sesudah agent memastikan kandidat masih replika baca-saja dan sesudah
  panel memastikan pekerjaannya belum digantikan. Panel yang dibobol tidak
  mendapat apa pun yang baru (ia sudah bisa mengirim perintah itu).
- **Diperiksa terhadap N1 dan dua tetangganya.**
  - *N1.* Promote terencana gagal saat mengejar, agent kandidat pergi, penjaga
    mempertahankan kunci, operator datang sehari kemudian. "Ulangi" ditolak
    (kunci belum terbukti dibuka; juga lebih dari 24 jam). **Buka kunci
    promote** ada di pekerjaan dan di halaman master. Kandidat sudah kembali:
    agent memeriksa lalu membuka. Kandidat mati: agent menolak, T11 memberi
    `unlock-master` dengan syaratnya dan larangan Resync. Selama itu Resync
    ditolak, jadi master tidak bisa dikarantina penjaganya sendiri.
  - *Sesudah promote darurat ke replika lain.* Master lama kembali, masih
    terkunci. Tombol pada pekerjaan lama ditolak (T11s); server itu ditangani
    sebagai master lama (karantina tertunda, 5.3).
    - *Terkirim, kandidat tidak pernah menjawab.* Tidak ada tombol; "Ulangi"
      menjawab T14 dengan ekor T14t; root membuka master sesudah memastikan
      syaratnya.
  - *Majemuk (baris 7).* Tombol pekerjaan terencana lama menjawab T11t dan
    menunjuk pekerjaan darurat; pekerjaan darurat menjawab T14 dengan ekor
    T14u; root membuka master lama sesudah memastikan kedua kandidat. Muat
    data dan promote terencana baru ditolak selama itu.

  Tidak ada cabang yang meninggalkan master tanpa langkah berikutnya; di satu
  cabang langkah itu perintah root.
- Verifikasi. Unit orchestrator dengan agent palsu: setiap baris tabel;
  "Ulangi" sebelum kirim ditolak tanpa token, dengan baris merah, dan dengan
  kunci belum terbukti dibuka; `TestPromotePlannedRetryLiftsKeptLock` ditulis
  ulang untuk tindakan baru (tenggat + 1 jam, kredensial kandidat, promote
  darurat langsung diizinkan); tombol pada pekerjaan yang sudah dikirim →
  ditolak, tidak ada yang terkirim; pekerjaan gagal dengan promosi darurat
  yang lebih baru selesai → tombol ditolak, tidak ada yang terkirim; kunci
  dipertahankan, tenggat + 2 jam, master "Terkunci" → Resync ditolak; catatan
  berdiri dengan laporan master pulihan atau tidak ada → Resync dan
  "Ulangi"-nya ditolak; di dalam jendela jam dengan laporan langsung kedua
  bendera OFF → tetap ditolak; sesudah jendela dengan laporan langsung kedua
  bendera OFF → catatan hilang, Resync dan promote terencana baru diizinkan;
  catatan milik pekerjaan yang masternya sudah berganti tidak dihitung;
  `mysql.status` berkata "permintaan promote: ada" padahal `job.status` id
  lama berkata berakhir → ditolak; id pekerjaan agent dari percobaan
  sebelumnya dihapus sebelum kirim; keadaan majemuk → T11t dan T14u; penolakan
  kunci ulang tidak menghapus catatan; tenggat terpotong; mulai tidak
  berjawab, kandidat baca-saja, "Ulangi" di dalam batas B → ditolak, promosi
  tetap "selesai sebagian"; field "tidak ada pekerjaan" → digagalkan; kandidat
  bisa ditulis → lanjut; **"Ulangi" sesudah titik tak kembali tetap tanpa
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
  sampai 5 menit. **Penolakan tidak pernah menghentikan putaran**; ia hanya
  memperlambatnya ke selang 5 menit. Penolakan agent atas karantina kebanyakan
  keadaan yang lewat sendiri (master baru belum terbaca atau belum terbuka,
  berkas replika belum terbaca), dan jawaban "sibuk" (5.6) juga dicoba lagi.
  Tiap putaran memakai keputusan yang sama dengan `onOnline` sekarang: selama
  server itu berperan `quarantined` dan karantina promosinya tertunda,
  perintah dikirim atas nama master topologi **yang sekarang**, juga bila
  topologi itu sudah promote lagi sejak itu (arah aman, dan sudah begitu hari
  ini); bila topologinya tidak terbaca atau tidak punya master lain, putaran
  itu tidak mengirim dan mencoba lagi nanti. Putaran berakhir hanya bila
  karantina mendarat, server itu dihapus, atau perannya di panel bukan lagi
  `quarantined`. Halaman server menampilkan kapan terakhir dicoba dan jawaban
  terakhir agent, berlabel (T18); peringatannya tetap terbuka.
- Tepi. Agent yang diam kini hanya menahan kuncinya sendiri. Master lama yang
  agentnya sendiri dibobol tetap bisa mengabaikan karantina (melekat pada
  model). Tidak ada protokol, migrasi, atau langkah operator baru.
- Verifikasi (unit, dua agent palsu, dengan `-race`): X menahan
  `mysql.repoint` tanpa jawaban, M dengan karantina tertunda tersambung →
  karantina M terkirim dalam satu detik; karantina yang ditolak ("tidak bisa
  dibaca", "sibuk") berhasil pada putaran berikutnya tanpa sambung ulang;
  tidak ada kunci untuk server tanpa pekerjaan.

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
   "tidak diketahui" F24. **Aturan berhenti:** bila sebuah baris tetap merah
   dengan perbaikan langkah 2, task berhenti dan melapor; tidak ada bagian 5.4
   lain yang dibangun di atas dugaan. Bila ukuran menunjukkan server terkunci
   menyala bisa ditulis di bawah 0.2.0, catatan pembaruan menambah: jangan
   me-restart MySQL master selama promote terencana sampai agentnya
   diperbarui.
2. **Perbaiki.** Satu pasang fungsi di driver dipakai semua jalur: buka = `SET
   PERSIST super_read_only = OFF` lalu `SET PERSIST read_only = OFF` (dipakai
   `unlockPromote`, penjaga saat tenggat, `unlock-master`,
   `mysql.setReadOnly`); kunci = `SET PERSIST super_read_only = ON` lalu `SET
   PERSIST read_only = ON` (kunci promote, penjaganya, `lock-master`). `RESET
   PERSIST` tidak dipakai lagi untuk kedua bendera. CLI kehilangan salinan
   pernyataannya sendiri. Bila pernyataan kedua buka gagal, berkas kunci masih
   ada dan penjaga mengunci lagi (gagal tertutup). Bila pernyataan kedua kunci
   gagal, server tetap terkunci seperti hari ini dan `lockForPromote` menjawab
   berhasil; agent tidak pernah melaporkan "tidak terkunci" selagi
   `super_read_only` ON. Urutan promote sendiri tidak diubah. Test yang sama
   lalu harus hijau pada setiap baris.
3. **Status panel membaca kedua bendera.** Master dengan `read_only` ON dan
   `super_read_only` OFF berstatus "Terkunci" dengan keterangan T19,
   peringatan `master_locked` mengikuti, dan **Buka kunci master** (yang sudah
   menyimpan keduanya OFF pada versi agent mana pun) memperbaikinya.
4. **Field "akan baca-saja sesudah restart".** Agent di server ber-pin master,
   tanpa kunci promote dan tanpa kunci start, **dan dengan kedua bendera OFF
   sekarang**, melaporkan field opsional baru di laporan kesehatan: benar bila
   nilai tersimpan atau blok konfigurasi akan membuat salah satu bendera ON
   pada start berikutnya. Aturan tepatnya diturunkan dari hasil langkah 1.
   Agent hanya melapor; ia tidak pernah membuka tulisan sendiri. Panel
   menampilkan peringatan T20 di halaman master dengan tombol **Perbaiki
   pengaturan tersimpan**: tindakan dan kata sandi yang sama dengan **Buka
   kunci master** (agent menolaknya di bawah kunci promote, jadi tombol ini
   tidak bisa melewati promote yang berjalan); sesudahnya T20b. Panel
   menampilkan T20 hanya bila status master bukan "Terkunci". Master yang
   baca-saja **sekarang** memakai jalur **Buka kunci master** dan teksnya yang
   sudah ada; jadi perbaikan ini tidak pernah mengubah nilai yang sedang
   berjalan, dan tidak membuka server yang sengaja dibuat baca-saja. Field
   yang tidak ada (agent 0.2.0) berarti tidak diketahui: tidak ada peringatan.
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
  bantuannya C8). Dari `unassigned` ke `master` perintah bertanya (dengan C5)
  hanya bila MySQL server itu menjawab dan punya kanal replikasi yang
  terpasang atau menyimpan kedua bendera ON; master baru yang polos tidak
  melihat pertanyaan, dan MySQL yang mati tidak menahan perintah. Tanpa itu
  pertanyaan (3) bisa dilewati lewat `unassigned`. Antara `replica` dan
  `unassigned` tidak bertanya.
- Layar: baris C1 (nama host kernel, peran sekarang, peran baru), lalu
  akibatnya (C2 sampai C5), lalu pertanyaan yang sudah ada. Peran dibaca ulang
  sesudah jawaban, seperti sekarang. Tanpa `--ya` dan tanpa masukan (skrip),
  perintah membatalkan diri dengan kode keluar 1. Sesudah `set-role
  quarantined` berhasil tercetak C6.
- `roleRefusal` untuk server ber-pin `master` menjadi A1; untuk peran lain
  tidak berubah. Panel tidak mengurai teks itu.
- **`unlock-master` bertanya** bila server berdiri di bawah kunci promote
  sebuah kandidat: peringatan yang sekarang hanya dicetak menjadi pertanyaan
  C9, dijawab `ya`; `--ya` melewatinya. Tanpa kunci seperti itu (master biasa,
  kunci `lock-master`) perintah tidak bertanya. Perintah ini kini jalan keluar
  yang disebut panel (T11, T14t), jadi ia tidak boleh tetap satu-satunya
  perintah root yang tidak bertanya.
- README bagian `set-role` dan panduan "Replika yang tersambung di luar alur
  panel" (langkah 4) menampilkan pertanyaannya; kalimat "Perintah ini tidak
  bertanya" dibuang. Tidak ada skrip di repo yang memanggil `set-role`.
- Tepi. Mengkarantina master lama dengan tangan menjadi beberapa detik lebih
  lama; disengaja (bagian 2). Panel yang dibobol tetap bisa menampilkan
  petunjuk apa pun di halamannya; pertanyaan ini pemeriksaan terakhir di
  terminal root. Agent 0.2.0 tidak bertanya.
- Verifikasi (unit, `cli_test.go`): tiap pasangan peran × `ya`, jawaban lain,
  `--ya`; `unassigned` ke `master` dengan dan tanpa kanal replikasi (mysqld
  sungguhan); `unlock-master` dengan kunci kandidat, kunci `lock-master`, dan
  tanpa kunci; peran berubah selama pertanyaan; pin tidak terbaca tetap lewat
  cabang lama. Teks penolakan di `pins_test.go`.

**Persetujuan bangun ulang (R-203).** Aturannya: persetujuan root hidup selama
satu percobaan bangun ulang, paling lama 24 jam.

- Agent mengakhiri persetujuan sendiri saat pekerjaan `mysql.dumpFrom` atau
  `mysql.cloneFrom` berakhir gagal atau dibatalkan, dan saat
  `mysql.dropDatabases` gagal. Bagian ini tidak bergantung pada panel. Ia
  tidak boleh menggagalkan pembersihan panel yang sah sesudah sebuah langkah
  gagal (`mysql.resetReplica`, `mysql.dropUser` dan `mysql.sweepDumpUsers`
  juga perintah terlindung). Task memeriksanya (bagian 6); bila pembersihan
  itu memang butuh persetujuan di master lama, agent hanya menutup tiga
  perintah yang membuang data, dan persetujuan penuh berakhir oleh permintaan
  panel atau batas 24 jam.
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
  pernah mulai), jadi penilaian 5.2 tetap benar; putaran karantina 5.3 mencoba
  lagi sesudah jawaban itu. Panel 0.2.0 melihatnya sebagai teks galat biasa.
- Verifikasi (unit, sesi palsu): 74 permintaan ke handler yang menahan → 64
  berjalan, 10 dijawab sibuk; slot kembali sesudah lepas dan sesudah
  putus-sambung. Tidak terbukti di sini: perilaku OOM cgroup.

**`monitor.configure` (R-200).**

- Penantian putaran menghitung tenggatnya sekali; konfigurasi ulang hanya
  boleh **memajukan** tenggat (selang baru yang lebih pendek). Panel yang
  jujur tidak melihat beda; folder baru dipindai pada putaran berikutnya,
  tidak pernah lebih lambat dari sekarang.
- `ValidBackupFolder` juga menolak: `/var` dan `/run` sendiri (induk folder
  yang disembunyikan unit), dan pohon sistem `/etc`, `/usr` (kecuali
  `/usr/local`), `/bin`, `/sbin`, `/lib`, `/lib32`, `/lib64`, `/libx32`,
  `/boot`, `/var/lib/dpkg`, `/var/lib/rpm`. `/usr/local` **tidak** ditolak: ia
  tempat yang wajar untuk folder buatan admin, isinya perangkat lunak dan
  bukan rahasia, dan menolaknya menambah peluang baris merah sesudah
  pembaruan. `/var/lib/mysql` juga tidak ditolak: isinya sudah dilaporkan
  agent lewat inventaris. Formulir panel menolak dengan aturan yang sama
  (T22).
- Tepi. Folder tersimpan yang kini ditolak membuat baris cadangan merah dengan
  teks T22b, yang menyuruh memilih folder lain: promote terencana dan bangun
  ulang meminta centang sampai folder diganti (5.12). Folder di luar daftar
  itu tetap bisa dipakai panel yang dibobol untuk mengintai (bagian 8).
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

- **Laporan segar atau "belum diketahui" (R-197).** Ukuran "laporan segar"
  (atau "laporan langsung") di bawah ini juga dipakai 5.2 dan pembacaan R-198,
  jadi fungsinya dibangun di bagian A; pemakaiannya di baris gerbang tetap di
  bagian B. Sebuah laporan segar bila servernya tersambung, laporannya
  langsung (bukan susulan, bukan pulihan saat panel menyala), dan umurnya
  tidak melewati batas basi monitor. Tidak segar: baris jam, disk dan
  `performance_schema` kuning "belum diketahui" (T23); baris pin master
  **merah** (T23b), karena pin yang tidak diketahui tidak boleh meloloskan
  promote terencana. Laporan pulihan ditandai di memori; tidak ada migrasi.
  Sesudah panel restart baris-baris itu kuning atau merah sampai laporan
  langsung pertama. Verifikasi (unit, dengan jam).
- **Replika lain yang bisa ditulis (R-198).** Tidak ada jalan melewati replika
  yang bisa ditulis, juga pada promote darurat (bagian 8). Yang berubah adalah
  dari mana panel tahu. Pembacaan langsung berarti satu `mysql.status` ke
  agent replika itu, paling lama 10 detik, sejajar untuk semua replika.
  - *Promote terencana* (mulai dan "Ulangi"): setiap replika lain yang
    laporannya tidak segar, tidak ada, atau berkata bisa ditulis dibaca
    langsung. Bisa ditulis: ditolak (T24). Tidak terbaca: ditolak juga (T24c);
    pekerjaan terencana bukan jalur gangguan, dan replika yang tidak terbaca
    mungkin master yang tidak dikenal.
  - *Promote darurat*: setiap replika yang agentnya tersambung dibaca
    langsung. Bisa ditulis: ditolak (T24). Replika yang tidak tersambung, atau
    tidak menjawab dalam 10 detik, dinilai dari laporan terakhirnya: baca-saja
    tidak menahan, dan layar menyebut replika mana yang tidak terbaca (T24d);
    bisa ditulis tetap menahan (T24b). Replika yang tidak terbaca dan tidak
    punya laporan sama sekali diperlakukan sama: tidak menahan, dan disebut di
    layar.
  - Teks tampil di layar promote sebelum apa pun diketik. Verifikasi (unit):
    laporan basi bisa ditulis + jawaban baca-saja → diizinkan; terencana
    dengan replika tidak terbaca → ditolak; darurat dengan replika offline
    yang laporan terakhirnya baca-saja → diizinkan dan disebut di layar.
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
  sambungan 64 per alamat dan 512 seluruhnya; tidak pernah batas total saja.
  Angka per alamat dibuat longgar karena admin di balik satu VPN atau firewall
  tampil sebagai satu alamat (B2). Halaman tanpa login tidak lagi menampilkan
  versi panel. README menyebut `--http` untuk mengikat UI ke satu alamat.
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

- T1 konfirmasi ditolak karena terlalu banyak salah: `Terlalu banyak kata sandi salah di sesi ini; coba lagi dalam %d menit, atau keluar lalu login lagi.`
- T2 kalimat di dialog kata sandi untuk tindakan ringan: `Kata sandi panel diminta sekali; selama 10 menit sesudahnya tindakan seperti ini tidak memintanya lagi.`
- T3 dialog pasang ulang agent: `Pasang ulang agent di %s? Agent yang sekarang tetap bekerja sampai agent baru terpasang. Perintah pemasangannya ditampilkan sekali dan berlaku 15 menit.`
- T3b halaman server selagi token pasang ulang aktif: `Token pasang ulang aktif sampai pukul %s. Perintahnya hanya ditampilkan sekali; bila hilang, buat token baru.`
- T4 dialog ubah alamat master: `Ubah alamat master untuk replika menjadi %s? Replika baru dan Resync ulang akan menyalin dari alamat itu.`
- T5 spanduk "Arahkan agent replika", alamat diubah orang: `Alamat ini diubah oleh %s pada %s. Pastikan itu memang alamat server %s sebelum menjalankan perintah di bawah.`
- T5b sama, alamat dipasang promote: `Alamat ini dipasang panel saat promote (pekerjaan %s) pada %s.`
- T6 baris "Port MySQL master" sesudah alamat diubah: `Alamat master baru saja diubah; port diperiksa lagi paling cepat pukul %s.`
- T7 baris "Ruang disk" bila ambang di atas 95: `Ambang kritis diatur %d%%; gerbang menilai merah mulai 95%%.`
- T8 centang cadangan saat keadaan folder belum diketahui: `Keadaan folder cadangan belum diketahui (folder baru diubah, atau agent master belum melapor). Centang bila Anda sudah memastikan sendiri cadangan terbaru ada.`
- T9 kalimat di atas tombol **Buka kunci promote**: `Master %s mungkin masih terkunci oleh promote ini. Tombol di bawah meminta agent master membuka kuncinya; agent memeriksa dulu bahwa kandidat masih replika.`
- T10 kunci dibuka: `Kunci promote di master %s dibuka; aplikasi bisa menulis lagi.`
- T10b tidak ada kunci promote: `Master %s tidak sedang dikunci untuk promote; tidak ada yang dibuka. Bila master masih berstatus "Terkunci", sebabnya lain: tekan "Buka kunci master".`
- T11 agent menolak membuka; kandidat baca-saja, tidak terbaca, atau kredensialnya tidak bisa disusun panel (pesan agent tampil berlabel di tengahnya): `Agent master %s tidak membuka kunci. <pesan agent>. Jangan Resync kandidat selama master masih terkunci. Bila Anda yakin kandidat tidak dipromosikan dan tidak ada replika lain yang sudah menjadi master, buka di server master: sudo dbpanel agent unlock-master`
- T11b agent master tidak tersambung: `Agent master %s tidak tersambung, jadi panel tidak bisa membuka kuncinya. Sambungkan agent itu lalu tekan lagi. Jangan Resync kandidat selama master masih terkunci. Bila Anda yakin kandidat tidak dipromosikan dan tidak ada replika lain yang sudah menjadi master, buka di server master: sudo dbpanel agent unlock-master`
- T11c agent menolak membuka; kandidat terbaca bisa ditulis: `Agent master %s tidak membuka kunci. <pesan agent>. Kandidat %s terbaca bisa ditulis dan mungkin sudah menjadi master: jangan buka master ini. Periksa kandidat itu dulu. Bila ia memang bukan master, kunci lagi di halaman replikanya, lalu tekan tombol ini lagi.`
- T11d agent menolak membuka; master sudah dikarantina: `Agent master %s tidak membuka kunci. <pesan agent>. Server ini sudah dikarantina; tindakannya ada di kotak Master lama di halaman server.`
- T11s tombol ditolak; promosi yang lebih baru sudah selesai, atau server itu bukan lagi master topologinya: `Kunci promote ini tidak dibuka dari panel: topologi ini sudah promote lagi sejak pekerjaan ini, atau server itu bukan lagi masternya. Server itu ditangani sebagai master lama di halamannya.`
- T11t tombol ditolak; promosi yang lebih baru masih berjalan atau "selesai sebagian" (`%s`: tautan ke pekerjaannya): `Kunci promote ini tidak dibuka dari panel selagi promote yang lebih baru di topologi ini belum tuntas. Selesaikan dulu pekerjaan promote %s.`
- T12 "Ulangi" promote terencana sesudah 24 jam: `Promote ini terakhir dikonfirmasi lebih dari 24 jam lalu, jadi "Ulangi dari tahap ini" tidak tersedia lagi. Mulai promote baru dari halaman master.`
- T12b "Ulangi" promote terencana selagi kunci belum terbukti dibuka: `Master %s mungkin masih terkunci oleh promote ini. Tekan "Buka kunci promote" dulu; sesudah kuncinya terbuka, "Ulangi dari tahap ini" tersedia lagi.`
- T12c promote terencana baru ditolak saat mulai (`%s` kedua: tautan ke pekerjaannya): `Master %s masih terkunci, atau kunci promote terencana sebelumnya belum terbukti dibuka. Tekan "Buka kunci promote" pada pekerjaan promote %s dulu, lalu mulai promote ini lagi.`
- T12d sama, master terbaca "Terkunci" tanpa catatan kunci promote: `Master %s masih terkunci. Buka kuncinya dulu dengan "Buka kunci master" di halaman master, lalu mulai promote ini lagi.`
- T13 promote darurat yang ternyata belum membuka master baru: `Promote darurat ini belum membuka master baru: kandidat %s masih baca-saja. Pekerjaan ini digagalkan; tidak ada yang berubah. Mulai promote darurat baru dari halaman master.`
- T13b sama, promote terencana: `Promote ini belum mengganti master: kandidat %s masih baca-saja. Pekerjaan ini digagalkan. Bila master masih terkunci, tekan "Buka kunci promote". Sesudah itu: "Ulangi dari tahap ini" bila konfirmasi terakhirnya belum lewat 24 jam, atau promote baru.`
- T14 kandidat tidak terbaca: `Kandidat %s belum bisa dibaca, jadi panel belum tahu apakah ia sudah menerima tulisan. Tidak ada yang diubah. Tekan "Ulangi dari tahap ini" lagi sesudah agent kandidat tersambung.`
- T14b pekerjaan agent kandidat masih berjalan, atau belum terbukti berakhir: `Pekerjaan promote di kandidat %s masih berjalan, atau belum terbukti berakhir. Tidak ada yang diubah. Tunggu sebentar, lalu tekan "Ulangi dari tahap ini" lagi. Bila agent kandidat tidak melaporkannya, panel menganggapnya berakhir pukul %s.`
- T14t ekor T14 dan T14b pada promote terencana selagi master terkunci: `Master %s tetap baca-saja selama itu. Bila kandidat tidak akan kembali, buka master di servernya: sudo dbpanel agent unlock-master. Jalankan itu hanya sesudah Anda memastikan kandidat bukan master dan tidak akan menjadi master: MySQL-nya terbaca baca-saja di server kandidat itu sendiri, atau host-nya mati.`
- T14u ekor T14 dan T14b pada promote darurat selagi master lamanya tersambung dan terbaca "Terkunci": `Master lama %s sudah kembali dan masih baca-saja oleh kunci promote terencana sebelumnya. Bila kandidat promote darurat ini tidak akan kembali, buka master lama itu di servernya: sudo dbpanel agent unlock-master. Jalankan itu hanya sesudah Anda memastikan kandidat promote darurat ini dan kandidat promote terencana sebelumnya bukan master dan tidak akan menjadi master: MySQL-nya terbaca baca-saja di servernya sendiri, atau host-nya mati.`
- T15 kalimat penutup penolakan promote darurat selagi "kunci mungkin berdiri" (menggantikan kalimat yang menyuruh "Ulangi"): `Sebelum itu, bila agent master tersambung, tekan "Buka kunci promote" pada pekerjaan promote terencana itu: begitu agent menjawab, panel mencatat kunci terbuka dan promote darurat bisa langsung dijalankan.`
- T15b muat data ditolak selama kunci promote belum terbukti dibuka (`%s` kedua: nama tindakannya, `%s` ketiga: tautan ke pekerjaannya; menggantikan teks lama yang berkata "saat penjaga kunci sudah memutuskan"): `Master %s mungkin masih terkunci oleh promote terencana sebelumnya. %s tidak dijalankan selama itu: memuat data membuat replika bisa ditulis sebentar, dan penjaga kunci master bisa mengkarantina master. Tekan "Buka kunci promote" pada pekerjaan promote %s dulu.`
- T16 akhir peringatan kunci yang dipertahankan penjaga: `Bila promote tidak dilanjutkan, tekan "Buka kunci promote" pada pekerjaan promote itu atau di halaman master. Jangan Resync kandidat selama master masih terkunci.`
- T17 baris log saat panel gagal membuka kunci, juga untuk promote yang dibatalkan: `Kunci master tidak bisa dibuka (%v). Pada tenggat kunci agent master membukanya sendiri bila kandidat masih replika dan semua replika terbaca; bila tidak, kunci dipertahankan. Tekan "Buka kunci promote" untuk mencoba lagi. Jangan Resync kandidat selama master masih terkunci.`
- T18 halaman master lama yang karantinanya tertunda (kalimat terakhir hanya bila agent pernah menolak): `Perintah karantina untuk server ini belum diterima; terakhir dicoba pukul %s. Panel mencoba lagi sendiri selama agent tersambung. Jawaban terakhir agent (bukan dari panel): <pesan agent>`
- T19 keterangan status master: `Master dikunci (read_only aktif, super_read_only mati): akun aplikasi tanpa hak SUPER tidak bisa menulis`
- T20 peringatan di halaman master, dengan tombol **Perbaiki pengaturan tersimpan**: `MySQL di master ini akan menjadi baca-saja sesudah restart berikutnya, karena pengaturan baca-saja lama masih tersimpan. Sekarang aplikasi tetap bisa menulis. Tekan "Perbaiki pengaturan tersimpan": tidak ada restart dan tidak ada gangguan.`
- T20b sesudah diperbaiki: `Pengaturan tersimpan di master %s sudah dibetulkan; master tetap bisa ditulis sesudah restart.`
- T21 bangun ulang gagal atau dibatalkan, persetujuan berakhir: `Persetujuan root di server %s untuk bangun ulang ini sudah diakhiri. Untuk mengulang: tekan "Ulangi dari tahap ini", lalu jalankan di server itu perintah approve dengan kode yang ditampilkan.`
- T21b sama, agent tidak mengakhirinya: `Persetujuan root di server %s mungkin masih berlaku sampai 24 jam sejak disetujui. Bila bangun ulang tidak dilanjutkan, akhiri di server itu: sudo dbpanel agent approve batal`
- T22 formulir folder cadangan: `Folder di bawah direktori sistem (%s) tidak diterima sebagai folder cadangan. Pakai folder lain, misalnya /backup.`
- T22b baris cadangan untuk folder tersimpan yang kini ditolak: `Folder cadangan %s berada di bawah direktori sistem dan tidak lagi diterima. Pilih folder lain di halaman server.`
- T23 baris gerbang dari laporan lama: `belum diketahui: laporan terakhir server ini sudah lama (%s)`
- T23b baris "Pin master" dari laporan lama: `Pin master belum terbaca dari laporan baru (laporan terakhir %s). Muat ulang halaman ini sebentar lagi.`
- T24 promote ditolak, replika lain bisa ditulis: `Replika %s melaporkan bisa ditulis (super_read_only mati), jadi mungkin sudah menjadi master; promote ditolak, juga promote darurat. Bila %s memang bukan master (misalnya dibuka dengan tangan untuk perbaikan): kunci lagi di halaman replikanya, atau lepas dengan Lepas replika, lalu buka halaman ini lagi. Bila aplikasi sudah menulis ke %s: jangan promote; periksa server itu dulu.`
- T24b sama, replika itu tidak menjawab: `Replika %s terakhir melaporkan bisa ditulis dan sekarang tidak menjawab, jadi panel tidak bisa memastikannya; promote ditolak, juga promote darurat. Bila Anda yakin %s bukan master: lepas dengan Lepas replika, lalu buka halaman ini lagi.`
- T24c promote terencana, replika lain tidak terbaca: `Replika %s tidak bisa dibaca sekarang, jadi panel tidak bisa memastikan ia masih baca-saja; promote terencana ditolak. Sambungkan agent replika itu, atau lepas dengan Lepas replika, lalu buka halaman ini lagi.`
- T24d promote darurat, catatan di layar: `Replika berikut tidak bisa dibaca sekarang; panel memakai laporan terakhirnya (baca-saja), atau tidak punya laporan sama sekali: %s.`
- T25 spanduk dasbor: `Berkas kata sandi awal masih ada di host panel. Sesudah kata sandi itu Anda simpan di tempat aman, hapus berkasnya: sudo rm %s`
- T26 kotak di dialog **Lepas replika** selagi master diduga tumbang: `Master %s sedang diduga tumbang. Bila replika ini harus menggantikannya, jangan lepas: jalankan Promote darurat dari halaman master, yang juga mengkarantina master lama. Replika yang dilepas lalu dibuka dengan tangan menjadi master kedua saat master lama hidup lagi.`
- T27 flash sesudah lepas dan hentikan: `Replika dilepas dan replikasinya dihentikan. MySQL di server ini tetap terkunci hanya-baca. Kapan aman membukanya, dan caranya, ada di Panduan: "Memakai bekas replika sebagai server mandiri".`
- T28 kotak hak yang kurang, butir tidak dikenal: `Agent melaporkan hak yang kurang dalam bentuk yang tidak dikenal panel, jadi perintahnya tidak ditampilkan. Klik Segarkan; bila tetap begini, perbarui agent server ini.`
- T29 kotak master tumbang tanpa replika: `Master ini belum punya replika, jadi tidak ada yang bisa dipromosikan: pulihkan master ini.`
- T30 dialog Stop replika: `Hentikan replikasi di %s? Lag membesar sampai replikasi dijalankan lagi; data yang masuk ke master sesudah ini tidak ada di replika ini.`
- T31 dialog simpan pemberitahuan Telegram: `Simpan perubahan pemberitahuan Telegram? Peringatan akan dikirim ke tujuan baru, atau tidak dikirim lagi untuk jenis yang dimatikan.`

**Dari agent (tampil di panel dengan label)**

- A1 penolakan di server ber-pin master: `server ini dipin sebagai master; perintah %s ditolak karena bisa membuang atau menimpa data master. Jangan ubah peran server ini kecuali server lain sudah menjadi master dan aplikasi tidak lagi memakai server ini.`
- A2 agent penuh: `agent sedang sibuk (terlalu banyak perintah berjalan); coba lagi sebentar`
- A3 ekor dua teks "tidak disalin dari sumber" Rencana 5b-2a, untuk nama yang tidak aman: `… dan %d database lain dengan nama tidak biasa; lihat dengan: sudo mysql -e 'SHOW DATABASES'`

**Keluaran perintah CLI di server**

- C1 baris pertama pertanyaan `set-role`: `Server ini: %s. Peran sekarang: %s. Peran baru: %s.`
- C2 peran baru `quarantined`: `Setelah perintah ini: dalam beberapa detik agent membuat MySQL server ini baca-saja (read-only) dan memasang kunci start; aplikasi tidak bisa menulis ke server ini, juga sesudah MySQL di-restart. Jalankan hanya di master lama, sesudah server lain menjadi master. Untuk membatalkan: sudo dbpanel agent set-role master, lalu sudo dbpanel agent unlock-master.`
- C3 dari `master` ke `replica`: `Setelah perintah ini: server ini tidak lagi dijaga sebagai master. Panel boleh membuang datanya dan menyalin ulang dari server lain tanpa persetujuan di server ini.`
- C4 dari `master` ke `unassigned`: `Setelah perintah ini: server ini tanpa peran. Panel boleh menyiapkannya ulang, termasuk membuang semua datanya, tanpa persetujuan di server ini.`
- C5 dari `replica` ke `master`, dan dari `unassigned` ke `master` bila MySQL-nya punya kanal replikasi atau menyimpan kedua bendera ON: `Setelah perintah ini: server ini dipin sebagai master dan panel boleh membukanya untuk ditulis. Perintah ini tidak menghentikan replikasi: bila server ini masih mengikuti master lain, Anda akan punya dua server yang bisa ditulis.`
- C6 sesudah `set-role quarantined`: `Peran diperbarui. Dalam beberapa detik agent membuat MySQL server ini baca-saja dan memasang kunci start.`
- C7 sesudah `unlock-master`: `MySQL server ini bisa ditulis lagi, dan tetap begitu sesudah MySQL di-restart.`
- C8 teks bantuan `--ya`: `tanpa pertanyaan`
- C9 pertanyaan `unlock-master` di bawah kunci promote sebuah kandidat: `Server ini dikunci untuk promote kandidat %s (tenggat %s). Perintah ini membuka kunci tanpa memeriksa kandidat itu atau replika lain; bila salah satunya sudah menjadi master, Anda akan punya dua master yang bisa ditulis. Ketik ya untuk melanjutkan, atau apa saja untuk membatalkan: `

**README, Panduan dan spek**

- B1 kalimat "Ulangi" (README tiga tempat, Panduan): `**Ulangi dari tahap ini** pada promote terencana yang gagal sebelum master diganti membaca lagi daftar **Prasyarat** dan meminta kata sandi panel setiap kali, seperti saat memulainya; bila master masih terkunci oleh promote itu, tekan **Buka kunci promote** dulu. **Ulangi dari tahap ini** dan **Setujui** pada pekerjaan yang langkah sisanya tidak membuang data (misalnya Buat replika sesudah salinannya jadi, atau promote yang masternya sudah diganti) tidak menilai lagi daftar itu. "Langkah sisanya" berarti langkah yang belum dijalankan pekerjaan itu.`
- B2 README "Keamanan", batas percobaan: `Buka panel hanya dari jaringan admin (firewall atau VPN). Login salah dari luar tidak memblokir konfirmasi kata sandi sesi yang sudah login, dan tidak memblokir login dari browser yang pernah berhasil login. Browser yang belum pernah login bisa terkunci 15 menit selama serangan berlangsung; me-restart panel tidak menolong.`
- B3 Rencana 4 §5.1: `Membuka kunci berarti SET PERSIST super_read_only = OFF lalu SET PERSIST read_only = OFF. Mengunci berarti SET PERSIST untuk keduanya ON.`
- B4 syarat di panduan "Memakai bekas replika sebagai server mandiri": `Buka hanya bila server ini akan dipakai aplikasi sebagai server mandiri dan master lamanya tidak lagi dipakai aplikasi mana pun. Bila master sedang tumbang dan replika ini harus menggantikannya, jangan lewat jalan ini: pakai Promote darurat, yang juga mengkarantina master lama.`
- B5 perintah pemeriksaan event: `mysql -e "SELECT EVENT_SCHEMA, EVENT_NAME FROM information_schema.EVENTS WHERE STATUS='ENABLED';"`
- B6 README "Keamanan", jendela kata sandi: `Sesudah kata sandi panel Anda terbukti benar (login atau konfirmasi), selama 10 menit tindakan ringan tidak memintanya lagi: token pasang ulang agent, alamat master untuk replika, persetujuan restart MySQL master, Siapkan sebagai master pertama, Hubungkan MySQL yang mengganti akun, Stop replika, dan perubahan pemberitahuan Telegram. Tindakan yang menghapus data atau mengganti master, termasuk Ulangi promote terencana, tetap meminta setiap kali. Jangan tinggalkan browser yang sedang login: dalam 10 menit itu orang lain bisa melakukan tindakan ringan tadi.`
- B7 README, memori agent: `Unit agent tidak memasang MemoryMax. Di server yang MySQL-nya dinyalakan agent sendiri (mysqld_safe atau skrip init), mysqld berada di cgroup unit agent, dan batas itu bisa membuat kernel membunuh MySQL. Agent membatasi jumlah perintah panel yang berjalan sekaligus (64).`

### 5.12 Kompatibilitas

- **Panel baru, agent 0.2.0.** Semua bagian panel bekerja. Yang belum ada
  sampai agent diperbarui: field "akan baca-saja sesudah restart" (tidak ada
  peringatan T20), buka dan kunci dengan kedua bendera (agent lama masih
  memakai `RESET PERSIST`), pengakhiran persetujuan (T21b), alamat replika di
  berkas kunci, pertanyaan `set-role` dan `unlock-master`, batas permintaan,
  penanda folder cadangan, dan laporan "permintaan promote" di `mysql.status`
  (tanpa itu "Ulangi" promote yang mulainya tidak berjawab menunggu batas B,
  5.2). Aturan "Versi agent" yang ada tidak diubah: tindakan bergerbang dan
  promote darurat ke kandidat ber-agent lain versi tetap ditolak sampai agent
  diperbarui.
- **Agent baru, panel 0.2.0.** Field laporan baru bersifat opsional dan
  diabaikan panel lama. Jawaban "sibuk" tampil sebagai galat biasa. Sesudah
  bangun ulang gagal, "Ulangi" di panel lama bertemu permintaan persetujuan
  lewat alur yang sudah ada (diperiksa di task R-203).
- **Keadaan tersimpan.** Migrasi: dua kolom yang boleh kosong di `topologies`
  (5.1) dan satu tanda pada token konfirmasi. Pekerjaan promote dari 0.2.0
  mengikuti aturan 5.2 pada "Ulangi" berikutnya; tenggat kunci tersimpan
  dipotong saat dibaca, dan pekerjaan yang terkirim tanpa id pekerjaan agent
  dihitung "mulai tidak berjawab". Promote terencana dari 0.2.0 yang kuncinya
  dibuka penjaga sendiri masih membawa catatan "kunci diminta, belum terbukti
  dibuka": sesudah pembaruan catatan itu hilang begitu laporan langsung
  pertama master menunjukkan kedua bendera OFF, biasanya dalam beberapa detik
  sesudah agentnya tersambung; sampai saat itu muat data dan promote terencana
  baru dari master itu ditolak (T15b, T12c). Catatan milik pekerjaan yang
  masternya sudah berganti tidak dihitung dan tidak menahan apa pun. Ambang
  disk tersimpan di atas 95 tetap tersimpan; gerbang memakai 95. Sesi ada di
  memori: restart saat pembaruan mengakhirinya, seperti setiap restart. Berkas
  kunci promote dari agent lama dibaca lewat nama.
- **Langkah pembaruan untuk operator** (masuk README "Pembaruan" dan catatan
  rilis):
  1. Perbarui panel, lalu **segera** setiap agent (perintah pasang ulang tiap
     server kini tampil sekali dan berlaku 15 menit). Seperti sekarang,
     promote ke kandidat yang versi agentnya lain dari panel ditolak, juga
     promote darurat.
  2. Sesudah itu buka halaman tiap master. Bila tampil peringatan T20, tekan
     **Perbaiki pengaturan tersimpan**. Jangan menunggu: master itu baca-saja
     pada restart MySQL berikutnya.
  3. Login sekali dari tiap browser yang akan dipakai saat gangguan.
  4. Skrip yang memanggil `set-role` menambah `--ya`.
  5. Bila baris cadangan sebuah server menjadi merah dengan teks "berada di
     bawah direktori sistem" (T22b), pilih folder lain di halaman server itu.
     `/usr/local` tetap diterima.
  6. Promote terencana kini menolak bila ada replika lain yang tidak bisa
     dibaca: sambungkan agentnya atau lepas replikanya dulu.
  7. Skrip yang memanggil `unlock-master` di bawah kunci promote menambah
     `--ya`.
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
  tenggat + 1 jam; tombol itu ditolak sesudah `mysql.promote` dikirim dan
  sesudah pekerjaannya digantikan; mulai tidak berjawab tidak pernah
  disimpulkan "tidak terjadi" dari `mysql.status` saja; muat data ditolak
  selama catatan kunci berdiri dan master tidak punya laporan langsung;
  putaran karantina tidak berhenti karena penolakan; konfirmasi dan login
  ber-cookie selagi batas nama pengguna penuh; token jendela ditolak tindakan
  berat; test kunci-per-server dengan `-race`.
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
- Bentuk tanda "mulai tidak berjawab" di keadaan pekerjaan dan bentuk field
  "permintaan promote" di `mysql.status`; **artinya** tidak diserahkan (5.2).
- Bentuk permintaan "akhiri persetujuan" (5.5): parameter opsional pada
  `mysql.rebuildApproval` atau perintah sendiri. Syaratnya tetap: selalu
  diizinkan di server yang dikarantina, hanya bisa mengakhiri, dan agent 0.2.0
  tidak terganggu olehnya.
- Angka yang tidak ditetapkan: laju baris log pekerjaan, batas baris karena
  sambungan berkedip.
- Pembagian menjadi task (rencana pelaksanaan).

**Yang belum diketahui dan harus diperiksa, di task yang disebut**

- Hasil tabel restart, dan dari situ aturan field "akan baca-saja sesudah
  restart" (task pertama R-188).
- Apakah `SET PERSIST super_read_only = ON` ikut menyimpan `read_only`, dan
  apakah **Buka kunci master** diterima agent saat hanya `read_only` yang ON
  (task R-188).
- Apakah setiap langkah pekerjaan promote di agent berbatas waktu, dan berapa
  jumlah batasnya: dari situ batas B (task "Ulangi" R-184). Bila ada langkah
  tanpa batas, task berhenti dan melapor.
- Setiap perintah terlindung yang dikirim panel ke master lama sesudah sebuah
  langkah bangun ulang gagal; pengakhiran persetujuan, oleh panel **dan oleh
  agent sendiri**, tidak boleh menggagalkannya (task R-203; jalan mundurnya di
  5.5). Juga: "Ulangi" dari panel 0.2.0 terhadap agent baru.
- Kecocokan R-202 dengan sisa audit 5a yang berdekatan (R-033, R-108), yang
  belum dicocokkan pemeriksa (task R-202).
- Unduhan salvage dan `WriteTimeout`; `--print-defaults` dan `MYSQL_HOME`;
  laporan di atas 1 MiB (task masing-masing, 5.6 dan 5.8).
- Dua butir R-196 yang belum dibaca ulang (5.9).

**Urutan dan dua bagian**

Bagian A, yang berbahaya, dalam urutan ini:

1. R-181 (batas percobaan). Semua permintaan baru bergantung padanya.
2. R-184, bagian kunci: catatan kunci dan tenggat; **Buka kunci promote**
   dengan aturan "belum dikirim" dan "digantikan"; penolakan muat data,
   promote terencana baru dan "Ulangi" selagi kunci belum terbukti dibuka;
   teks T9 sampai T12c dan T15 sampai T17.
3. R-184, bagian "Ulangi": fungsi penolakan bersama dengan mulai, termasuk
   pembacaan langsung R-198 dan fungsi "laporan segar" (5.7), yang karena itu
   masuk bagian A (fungsi itu sudah dibutuhkan butir 2); kata sandi setiap
   kali; keadaan "terkirim" dengan tanda tidak berjawab, field `mysql.status`
   di agent dan batas B; teks T13 sampai T14t, T24 sampai T24d, B1.
4. Kelompok kata sandi: jendela, R-182, R-183, R-191, Stop replika dan
   Telegram.
5. R-185.
6. R-188: ukur, perbaiki, status panel, field dan tombol, teks;
   `unlock-master` bertanya (berkas CLI yang sama).
7. R-202 (berkas dan jalur buka yang sama dengan R-188).
8. R-187.
9. R-203.
10. R-190 (ditambahkan penulis ke bagian A, lihat bagian 7).

Butir 2 dan 3 dipisah karena pekerjaan kunci dan "Ulangi" kini lebih besar;
butir 2 bisa ditinjau sendiri, dan sesudahnya master sudah punya tombolnya
sebelum "Ulangi" diubah. "Ulangi" tidak lagi bergantung pada jendela, jadi
butir 3 tidak menunggu butir 4.

Supaya bagian A bisa di-merge sendiri, ia harus memuat: setiap teks panel,
CLI, README dan Panduan yang menjelaskan perilaku yang diubahnya (B1 sampai
B4, B6, dan teks T, A, C milik butir di atas), sehingga tidak ada kalimat yang
berbohong pada hari merge; kedua migrasi; langkah pembaruan 5.12 nomor 1
sampai 4, 6 dan 7; catatan hasil ukur R-188; dan test penjaga 5.13 dalam
keadaan hijau. Yang belum ada sampai bagian B dan harus disebut di laporan
bagian A: penanda folder cadangan (centang T8 masih bisa dihindari dengan
waktu).

Pengendali berhenti dan melapor singkat di antara bagian A dan bagian B. Bila
dua test yang kadang gagal (5.10) mengganggu gerbang test bagian A, perbaikan
port pembantu test boleh dimajukan ke awal bagian A.

Bagian B: 5.6 (dengan perbaikan port dan test pemindaian dari 5.10, port lebih
dulu bila rendaman butuh gerbang yang stabil), sisa 5.7 (R-197, R-199), 5.8,
sisa 5.9, 5.10. Lalu penutup di 5.13.

## 7. Keputusan penulis yang perlu dilihat pemilik

**Tiga hal yang perlu diketahui sebelum menyetujui**

- **Jendela kata sandi, seperti yang Anda pilih, dengan satu pembetulan.**
  Kata sandi yang terbukti benar (login, konfirmasi berat, atau permintaan
  ringan) membuka 10 menit pada sesi itu. Di dalamnya tujuh tindakan tidak
  meminta kata sandi: token pasang ulang agent, alamat master untuk replika,
  persetujuan restart MySQL master, Siapkan sebagai master pertama, Hubungkan
  MySQL yang mengganti akun, Stop replika, dan perubahan pemberitahuan
  Telegram. Yang paling berat adalah pasang ulang agent: orang yang memakai
  browser yang ditinggal dalam 10 menit itu bisa mengganti agent sebuah
  server. **Pembetulannya: "Ulangi" promote terencana, yang Anda lihat di
  daftar tindakan ringan, kini meminta kata sandi setiap kali** (butir 1).
- **Tiga perubahan untuk operator yang sudah Anda lihat:** promote darurat
  yang gagal sebelum master baru dibuka tidak bisa di-"Ulangi" dan dimulai
  baru; `set-role` meminta `ya` diketik; sesudah bangun ulang master lama
  gagal atau dibatalkan, root menjalankan `approve` lagi.
- **Tiga hal yang tidak dibangun** (alasannya di bagian 8): promote darurat
  yang melewati replika lain yang melapor bisa ditulis; TLS 1.3 saja dan HSTS
  `includeSubDomains`; `MemoryMax` pada unit agent.

**Keputusan yang belum Anda lihat.** Butir 1 sampai 8 baru sesudah builder
check dan tinjauan rancangan (bagian 9); butir 9 sampai 17 dari rancangan
pertama, dengan perubahan yang disebut.

1. **"Ulangi" promote terencana meminta kata sandi setiap kali, bukan lewat
   jendela.** Ia menuntaskan pergantian master, dan aturan yang Anda pilih
   berkata tindakan seperti itu meminta setiap kali. Ini menyimpang dari tabel
   yang Anda lihat, ke arah yang lebih aman. Harganya satu kata sandi pada
   jalur yang tidak pernah jalur gangguan.
2. **Perintah pasang ulang agent ditampilkan sekali, dan tokennya berlaku 15
   menit, bukan 24 jam** (5.1). Perintah yang tetap tampil bisa dibaca sesi
   mana pun tanpa kata sandi. Bila salah: operator yang kehilangan perintahnya
   membuat token baru (tanpa permintaan di dalam jendela).
3. **Stop replika dan perubahan pemberitahuan Telegram masuk jendela; senyap
   (Jangan ganggu) tidak** (5.1). Membisukan peringatan lalu menghentikan
   replika menyiapkan kehilangan data yang tidak terlihat. Senyap paling
   sering dipakai, sudah dibatasi 24 jam per kali dan diaudit. Sisa risikonya
   di bagian 8.
4. **`unlock-master` meminta `ya` diketik bila server berdiri di bawah kunci
   promote sebuah kandidat** (`--ya` untuk skrip). Perintah itu kini jalan
   keluar yang disebut panel, dan ia melewati semua pemeriksaan. Harganya satu
   kata selagi master tidak bisa ditulis.
5. **Satu keadaan punya jalan keluar perintah root, bukan tombol:** promote
   terencana yang perintahnya sudah terkirim dan kandidatnya tidak pernah
   menjawab lagi. Master tetap baca-saja sampai root menjalankan
   `unlock-master` sesudah memastikan kandidat bukan dan tidak akan menjadi
   master (T14t). Tombol di keadaan itu bisa membuat dua master. Hal yang sama
   berlaku untuk bentuk majemuknya: promote darurat yang kandidatnya diam
   sementara master lama kembali masih terkunci (T14u).
6. **Selagi master masih terkunci oleh promote terencana yang gagal, panel
   menolak Resync, Buat replika, Tambah dan Lepas database dengan data,
   promote terencana baru, dan "Ulangi" promote itu, sampai kuncinya dibuka**
   (5.2), juga sesudah tenggat kunci lewat. Memuat data selagi kunci berdiri
   membuat penjaga mengkarantina master yang sehat. Harganya: satu klik **Buka
   kunci promote** lebih dulu; dan sesudah panel restart, tindakan-tindakan
   itu ditolak beberapa detik sampai laporan pertama master datang.
7. **Promote terencana ditolak bila ada replika lain yang tidak bisa dibaca;
   promote darurat tidak** (5.7). Replika yang tidak terbaca mungkin master
   yang tidak dikenal. Bila salah: promote terencana menunggu agent replika
   itu tersambung, atau replikanya dilepas.
8. **Folder cadangan di bawah `/usr/local` tetap diterima; pohon sistem lain
   ditolak** (5.6). Bila salah: sebuah rumah sakit dengan cadangan di pohon
   yang ditolak mendapat baris merah dan memilih folder lain.
9. **Tiga tindakan yang belum diputuskan.** Siapkan sebagai master pertama dan
   Hubungkan MySQL yang mengganti akun tersimpan masuk jendela; Sudah saya
   alihkan tetap tanpa kata sandi (alasan di tabel 5.1).
10. **Anggaran konfirmasi dihitung per sesi, dan login ber-cookie perangkat
    tidak memakai batas per alamat maupun slot pemeriksaan bersama** (5.1).
    Bila salah: pencuri cookie perangkat mendapat 5 tebakan kata sandi per 15
    menit dari alamatnya.
11. **R-190 (teks Lepas replika) dan pembacaan langsung R-198 masuk bagian
    A.** Yang pertama baris Tinggi yang kecil; yang kedua kini dipakai
    "Ulangi" promote.
12. **Buka kunci promote di halaman master tidak bergantung pada catatan panel
    saja:** bila catatan tidak menunjuk pekerjaan mana pun dan master
    "Terkunci", tombol memakai promote terencana terakhir yang gagal dan belum
    dikirim. Bila salah: tombol tampil saat tidak perlu, dan agent menjawab
    "tidak dikunci".
13. **Promote terencana yang perintahnya terkirim tetapi terbukti tidak
    terjadi digagalkan, bukan dilanjutkan diam-diam** (T13b). Melanjutkannya
    berarti promote tanpa Prasyarat.
14. **Peringatan R-188 menunjuk tombol panel, bukan perintah root, dan hanya
    tampil pada master yang sekarang bisa ditulis** (T20). Tombol itu tidak
    bisa melewati kunci promote, dan tidak pernah mengubah nilai yang sedang
    berjalan.
15. **Persetujuan berakhir pada setiap kegagalan bangun ulang, dan panel ikut
    meminta agent mengakhirinya** (5.5): satu tambahan kecil pada protokol,
    dan satu `approve` lagi juga pada alur `drop-database` lalu "Ulangi".
16. **`set-role master` dari `unassigned` bertanya hanya bila MySQL server itu
    masih punya kanal replikasi atau menyimpan kedua bendera ON.** Agent tetap
    tidak menolak **Buka kunci master** pada server yang masih mereplikasi:
    penolakan yang keliru meninggalkan master baca-saja tanpa tombol.
17. **Butir kecil dan teks.** Sambungan agent yang berkedip tidak ditunda;
    versi panel dibuang dari halaman tanpa login; penanda folder cadangan
    dibangun sekarang. Teks baru: semua butir 5.11; yang paling perlu dibaca
    T2, T9 sampai T17, T20, T21, T24, T26, C1 sampai C5, C9, B1, B6.

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
  yang memegang sesi itu bisa melakukan tujuh tindakan ringan, termasuk
  membuat token pasang ulang agent (lalu menerima rahasia server itu) dan
  mengubah alamat master (ke mana karantina tertunda dan repoint dikirim; pin
  agent menolak host yang tidak dikenal). Baris audit `agent_pair` dan
  `window: true` mencatatnya.
- **Senyap, Batalkan, Sudah saya alihkan dan Pengaturan lain tetap tanpa kata
  sandi.** Sesi tanpa kata sandi masih bisa membisukan pemberitahuan sebuah
  server sampai 24 jam, dan mengulanginya; yang tidak bisa lagi tanpa kata
  sandi adalah menghentikan replika dan mengalihkan Telegram. Semuanya
  diaudit.
- **Sesi curian adalah sesi yang sama.** Selama admin belum login lagi, 20
  tebakan salah si pencuri menolak konfirmasi admin; jalan keluarnya logout
  lalu login (T1).
- **Browser yang belum pernah login** tetap bisa dikunci dari luar, dan
  serangan dari banyak alamat masih bisa memenuhi slot pemeriksaan login
  anonim. Cookie perangkat yang dicuri memberi paling banyak 20 tebakan per 15
  menit; sesudah panel restart, sebelum admin berhasil login sekali,
  pencurinya bisa menahan login ber-cookie admin 15 menit berulang.
- **Promote yang perintahnya terkirim dan kandidatnya tidak pernah kembali**
  tetap "selesai sebagian" dan menahan promote lain di topologi itu, juga
  sesudah root membuka master. Batas ini sudah ada. Panel tidak menawarkan
  cara menutup promosi seperti itu: menutupnya tanpa bukti dari kandidat
  adalah jalan ke dua master (T-1). Jalan yang tersisa: kandidat kembali dan
  "Ulangi" menilainya.
- **Promote terencana, terkirim, kandidat diam:** jalan keluar master adalah
  perintah root dengan syarat yang dinilai orang (T14t). Root yang salah
  menilai mendapat dua master; karena itu `unlock-master` kini bertanya.
- **Laporan langsung master sebagai bukti kunci sudah tidak ada.** Catatan
  kunci dihapus oleh laporan master dengan kedua bendera OFF sesudah tenggat.
  Agent master yang dibobol bisa melaporkan itu selagi terkunci; ia memang
  sudah bisa membuka dirinya sendiri.
- **Batas B** (pekerjaan yang dikirim ke agent sebelum pembaruan) adalah angka
  yang diturunkan dari batas pekerjaan agent; bila sebuah langkah agent
  ternyata tidak berbatas, task berhenti dan melapor.
- **Jendela "kunci mungkin berdiri" tetap ada untuk promote darurat.** Bila
  master mati sesudah promote terencana gagal dan sebelum kuncinya terbukti
  dibuka, promote darurat menunggu sampai tenggat kunci lewat (paling lama
  sekitar 36 menit sejak kunci diminta). Batas ini sudah ada; yang berubah: ia
  tidak bisa lagi digeser dengan "Ulangi" atau diperpanjang agent master.
- **Buka kunci promote bisa ditolak agent** (kandidat mati, replika tidak
  terbaca). Jalan yang tersisa adalah `unlock-master` oleh root, seperti
  sekarang; T11 memberi syaratnya. Selama itu muat data ditolak.
- **Promote darurat dan replika offline.** Replika yang tidak tersambung dan
  laporan terakhirnya baca-saja tidak menahan promote darurat; bila ia
  sebenarnya sudah menjadi master sejak laporan itu, promote darurat tidak
  melihatnya. Layar menyebut replika mana yang tidak terbaca (T24d).
- **R-188.** Hanya 8.0.46 yang terukur. Server dengan
  `persisted_globals_load=OFF` mengabaikan nilai tersimpan: master yang pernah
  dipromote tetap bisa menyala baca-saja di sana. Nilai OFF tersimpan
  mengalahkan `read_only=ON` yang kelak ditulis orang ke my.cnf. Master yang
  agentnya belum diperbarui tidak menampilkan peringatan. Aturan field bisa
  salah untuk kombinasi yang tidak diukur: peringatan palsu atau tenang palsu.
- **R-187.** Operator yang terbiasa mengetik `ya` tetap bisa salah terminal.
  Server ber-pin master yang masih mereplikasi tetap bisa dibuka panel.
- **R-203.** Panel yang dibobol tidak ditahan selama percobaan berjalan, dan
  persetujuan yang belum dipakai hidup sampai 24 jam bila panel itu tidak
  memintanya berakhir.
- **R-202.** Berkas kunci dari agent lama dibaca lewat nama (paling lama 60
  menit).
- **R-185.** Master lama yang agentnya sendiri dibobol tetap bisa mengabaikan
  karantina. Sesi di dalam jendela yang mengubah alamat master membuat
  karantina tertunda ditolak agent sampai alamatnya dibetulkan; putaran tetap
  mencoba dan T18 menampilkan penolakannya.
- **R-186.** Handler yang macet menahan slot sampai agent direstart.
- **R-200.** Folder di luar daftar yang ditolak, termasuk `/usr/local`, tetap
  bisa dipakai panel yang dibobol untuk mengetahui ada-tidaknya, umur dan
  ukuran berkas.
- **R-201.** Start layanan MySQL oleh agent tetap membawa lingkungan root;
  membersihkannya bisa mengubah cara `mysqld_safe` menemukan konfigurasinya,
  dan itu tidak terukur.
- **R-183.** Panel yang dibobol seluruhnya tetap bisa menampilkan spanduk apa
  pun. Pemeriksaan di sisi agent (`set-source` mencocokkan identitas master)
  dicatat untuk Rencana 5c.
- **R-189.** CSP baru hanya terbukti di Chromium.
- **R-193, R-194.** Belum ada halaman ganti kata sandi (R-042); log audit
  tidak dipangkas (K6). Server pertama yang belum ter-pairing masih
  menampilkan perintah pemasangannya seperti sekarang.
- **Bagian A tanpa bagian B.** Sampai bagian B selesai, centang cadangan T8
  bisa dihindari dengan laporan lama.

**Anggapan tentang lingkungan** (tidak ditanyakan kepada pemilik sekarang;
masing-masing dengan akibatnya bila salah)

- **Tidak ada rumah sakit yang menyimpan cadangan di bawah pohon sistem yang
  ditolak** (`/etc`, `/usr` selain `/usr/local`, `/boot`, dan seterusnya).
  Bila salah: sesudah pembaruan baris cadangan server itu merah dengan teks
  T22b; promote terencana dan bangun ulang meminta centang sampai folder
  diganti. Promote darurat tidak terpengaruh.
- **Saat gangguan operator memakai browser yang pernah login ke panel.** Bila
  salah, dan panel sedang diserang dari luar: browser baru bisa terkunci 15
  menit berulang kali. Jalan keluarnya browser yang pernah login, atau menutup
  sumber serangan di firewall.
- **Alamat replika tidak berubah selama kunci promote berdiri** (paling lama
  60 menit). Bila salah (DHCP): penjaga tidak bisa membaca replika itu di
  alamat tercatat, kunci dipertahankan lewat tenggat, dan master baca-saja
  sampai root menjalankan `forget-replica` atau `unlock-master`. Arah aman,
  tetapi henti tulis lebih lama.
- **Verifikasi sertifikat antarserver menyala.** Bila salah: alamat master
  yang diubah sesi di dalam jendela, ditambah operator yang menjalankan
  `set-source` dari spanduk, bisa mengirim sandi akun replikasi ke host lain
  (sisa R-183); dan pada agent yang belum diperbarui, umpan DNS R-202 masih
  bisa menipu penjaga kunci.

## 9. Perubahan sesudah builder check dan tinjauan rancangan

Rancangan pertama diperiksa dua kali: builder check (empat pertanyaan, P1
sampai P4) dan tinjauan rancangan untuk keselamatan (0 Kritis, 5 Tinggi T-1
sampai T-5, 10 Sedang S-1 sampai S-10, 8 Rendah R-1 sampai R-8). Kelima temuan
Tinggi dicocokkan penulis dengan kode pada `8caee75`; semuanya benar. Semua
temuan diambil. Di bagian ini "R-1" sampai "R-8" adalah nomor temuan Rendah
tinjauan, bukan baris inventaris.

| Temuan | Yang berubah | Di mana |
|---|---|---|
| T-1 | Tanda "mulai tidak berjawab" dan waktu kirim disimpan di keadaan pekerjaan; "tidak ada yang terjadi" butuh bukti (`job.status`, field baru di `mysql.status`, atau batas B); `mysql.status` baca-saja bukan bukti | 5.2 baris 5; T14b; 5.12; 5.13 |
| T-2, P1 | **Buka kunci promote** hanya selama `mysql.promote` belum dikirim, ditegakkan di handler dan orchestrator; keadaan "terkirim, kandidat diam" mendapat jalan keluar perintah root dengan syarat | 5.2 tabel dan butir; T14t; bagian 2, 7, 8 |
| T-3 | Tombol ditolak bila pekerjaannya sudah digantikan, tanpa menyebut `unlock-master` | 5.2; T11s |
| T-4 | Muat data ditolak selama master masih terkunci oleh promote terencana yang gagal, berapa pun jamnya; promote darurat tetap memakai aturan jam; larangan Resync di teks | 5.2; T15b, T11, T16, T17 |
| T-5, P4 | Penolakan tidak menghentikan putaran karantina; putaran melambat ke 5 menit dan berakhir hanya bila karantina mendarat atau promosinya tuntas; "sibuk" dicoba lagi | 5.3; 5.6; T18 |
| P2 | "Ulangi" sebelum kirim ditolak selama kunci belum terbukti dibuka; sesudah itu berjalan dari "Kunci master" | 5.2 baris 1 dan 2; T12b |
| P3 | Promote terencana membaca langsung replika yang laporannya basi dan menolak yang tidak terbaca; promote darurat membaca yang tersambung dan tidak ditahan replika offline yang laporan terakhirnya baca-saja | 5.7; T24c, T24d |
| S-1 | Jawaban galat tidak menghapus catatan kunci; hanya cap waktu yang kembali; `lockForPromote` berhasil walau pernyataan kedua gagal; semua promote terencana master itu dibaca | 5.2; 5.4 |
| S-2 | Promote terencana baru ditolak selagi master terkunci atau kunci lama belum terbukti dibuka; tombol halaman master memakai pekerjaan yang kuncinya belum terbukti dibuka | 5.2; T12c |
| S-3 | "Ulangi" sebelum kirim memakai fungsi penolakan yang sama dengan mulai | 5.2 |
| S-4 | Field dan T20 hanya untuk master yang sekarang bisa ditulis | 5.4 butir 4 |
| S-5 | "Ulangi" promote terencana menjadi tindakan berat | 5.1; B1, B6; bagian 2 dan 7 |
| S-6 | Perintah pasang ulang ditampilkan sekali | 5.1; T3, T3b |
| S-7 | T1 dan "Tepi" menyebut logout lalu login | 5.1; T1 |
| S-8 | Stop replika dan perubahan Telegram masuk jendela; senyap tetap, sudah berbatas 24 jam dan diaudit | 5.1; T30, T31; bagian 8 |
| S-9 | `set-role master` dari `unassigned` bertanya bila MySQL-nya punya kanal replikasi atau kedua bendera tersimpan ON | 5.5 |
| S-10 | `unlock-master` meminta `ya` di bawah kunci kandidat | 5.5; C9 |
| R-1 | Anggaran cookie perangkat: kunci cookie dan alamat, tempat dipesan dulu, habis berarti ditolak | 5.1 |
| R-2 | T13b tidak menawarkan "Ulangi" sesudah 24 jam | T13b |
| R-3 | T10b menunjuk **Buka kunci master**; kedua tombol tampil | 5.2; T10b |
| R-4, R-8 | Ekor teks penolakan dipilih dari keadaan kandidat dan peran master, juga bila kredensial kandidat tidak bisa disusun | 5.2; T11, T11c, T11d |
| R-5 | Pengakhiran persetujuan oleh agent tidak boleh menggagalkan pembersihan panel; pemeriksaannya diperluas, dengan jalan mundur | 5.5; bagian 6 |
| R-6 | Batas sambungan UI 64 per alamat, 512 seluruhnya | 5.8 |
| R-7 | Aturan berhenti untuk "ukur dulu" | 5.4 butir 1 |
| Putusan lain | `/usr/local` diterima; bunyi pilihan 1 dan 3 jendela kata sandi dikutip; empat pertanyaan lingkungan menjadi anggapan tertulis; R-198 pindah ke bagian A | 5.6, 5.12; bagian 4; bagian 8; bagian 6 |

**Sesudah pemeriksaan ulang tinjauan rancangan.** Peninjau memeriksa revisi di
atas: 14 dari 15 temuan tertutup, T-4 sebagian; tidak ada Kritis atau Tinggi
baru; lima Sedang baru (N-1 sampai N-5) dan delapan Rendah (NR-1 sampai NR-8).
Semuanya diambil.

| Temuan | Yang berubah | Di mana |
|---|---|---|
| N-1 | Field `mysql.status` berarti "permintaan `mysql.promote` sudah dibaca agent dan pekerjaannya belum berakhir", dipasang di putaran baca; panel menghapus id pekerjaan agent lama dan memasang tanda "tidak berjawab" sebelum tiap kirim; "ada" mengalahkan bukti lain; batas B bergantung pada arti itu | 5.2 ("Arti field itu", "Batas B"); bagian 6 |
| N-2 (melengkapi T-4) | Penolakan muat data **menambah** aturan jam: di dalam jendela selalu ditolak; sesudahnya ditolak selama catatan berdiri; tanpa laporan langsung master berarti ditolak | 5.2; T15b; 5.13 |
| N-3 | Catatan kunci dihitung hanya selama server itu master topologinya dan belum ada promosi yang selesai sesudahnya; laporan langsung dengan kedua bendera OFF sesudah tenggat + kelonggaran menghapusnya; pekerjaan 0.2.0 dijelaskan | 5.2; 5.12; bagian 8 |
| N-4 | Jumlah per cookie perangkat 20 per 15 menit untuk semua alamat; yang ditolak hanya pasangan yang belum pernah berhasil sejak panel menyala; sisa untuk pencuri disebut | 5.1; bagian 8 |
| N-5 | Keadaan majemuk masuk tabel dan pemeriksaan N1; T11s dipecah (T11s, T11t); ekor T14u pada promote darurat | 5.2 baris 7; T11t, T14u; bagian 7 |
| NR-1 | Putaran karantina memakai keputusan `onOnline`: tetap mengirim sesudah promote kedua, atas nama master yang sekarang | 5.3 |
| NR-2 | T12c dan T15b menautkan pekerjaannya | T12c, T15b |
| NR-3, NR-4 | Baris 5 dan 6 tidak pernah ditolak catatan kunci; halaman master untuk pekerjaan yang sudah dikirim menampilkan T14, bukan tombol | 5.2 |
| NR-5 | T11c memberi langkah berikutnya | T11c |
| NR-6 | Batas total sambungan UI tidak lagi tercatat terbuka | bagian 6 |
| NR-7 | Replika tanpa laporan sama sekali pada promote darurat | 5.7; T24d |
| NR-8 | Fungsi "laporan segar" dibangun di bagian A | 5.7; bagian 6 |
