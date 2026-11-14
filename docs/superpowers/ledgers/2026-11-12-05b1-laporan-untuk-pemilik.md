# Rencana 5b-1: laporan untuk pemilik

Dokumen ini ditulis untuk pemilik dbpanel yang harus memutuskan apakah cabang kerja Rencana 5b-1 digabung (merge) atau tidak. Isinya: apa saja yang diputuskan atas nama pemilik selama pekerjaan berjalan, semua teks baru yang bisa dilihat operator, dan bagian README yang baru atau berubah. Semua teks dikutip dari kode dan templat pada titik cabang `8b0389d`.

## Bagian 1. Cara membaca dokumen ini

**Apa yang dikerjakan rencana ini.** Rencana 5b-1 membuat "mode ketat" untuk master lama yang dikarantina: server itu hanya menjalankan perintah panel yang tidak membuang data MySQL. Membangun ulang server itu sebagai replika (yang membuang semua datanya) baru boleh jalan setelah root di server itu sendiri menyetujuinya dengan perintah `dbpanel agent approve`, jadi panel saja tidak bisa memulainya. Rencana ini juga memperbaiki beberapa kelemahan pada agent (program kecil yang berjalan di setiap server MySQL), antara lain cara agent memeriksa "kunci start" dan cara panel membuka peringatan.

**Cara pekerjaannya berjalan.** Pekerjaan berjalan lewat 12 tugas (Tugas 1 sampai 12), masing-masing dibuat lalu diperiksa. Sesudah itu ada tinjauan akhir seluruh cabang dan dua audit keamanan. Temuannya dikerjakan dalam putaran perbaikan yang dibagi menjadi delapan bagian, bagian A sampai H.

**Risiko yang masih terbuka.** Daftar risiko yang masih terbuka ada di dokumen audit `docs/superpowers/security/2026-11-14-audit-plan-5b1.md`; risikonya tidak disalin ke sini. Mulailah dari Bagian 1 dokumen audit itu ("Ringkasan eksekutif"): di sana ada tabel semua risiko terbuka, siapa yang bisa memicunya, akibat terburuknya, dan usul perbaikannya. Keputusan yang menyangkut risiko terbuka ditandai "Perlu perhatian pemilik:" di Bagian 2.

**Istilah yang sering muncul.**

- **Panel**: aplikasi web tempat operator bekerja. **Agent**: program di tiap server MySQL yang menjalankan perintah panel.
- **Master lama / dikarantina (quarantined)**: server yang tadinya master lalu digantikan (promote). Ia dikunci baca-saja (`read_only` dan `super_read_only`, dua pengaturan MySQL yang menolak tulisan) sampai dibangun ulang.
- **Pin**: catatan agent di server itu sendiri tentang perannya (`/etc/dbpanel/role`) dan master yang diikutinya (`/etc/dbpanel/source`). Agent menolak perintah yang tidak cocok dengan pin, walau panelnya disusupi.
- **Kunci start (start lock)**: berkas dan satu baris `init_file` (pengaturan MySQL yang menjalankan berkas SQL saat MySQL menyala) supaya MySQL master lama menyala baca-saja walau agent mati.
- **Persetujuan di server**: root menjalankan `sudo dbpanel agent approve <kode>` di master lama itu sebelum panel boleh membangun ulangnya.
- **Bangun ulang sebagai replika**: seluruh data master lama dibuang dan diganti salinan dari master baru.

## Bagian 2. Keputusan yang diambil saat pelaksanaan

Setiap "keputusan pelaksanaan" di bawah ini diambil oleh pengendali pekerjaan atas nama pemilik. Setiap keputusan ditulis di buku catatan pelaksanaan dalam bentuk "keputusan, alasan, biaya bila keliru". Di sini keputusannya diterjemahkan setia, tanpa dilunakkan. Tanda **Perlu perhatian pemilik:** dipasang bila biayanya menyentuh kehilangan data, dua master yang sama-sama bisa ditulis, atau risiko terbuka yang diserahkan kepada pemilik.

Nomor keputusan dipakai persis seperti di buku catatan: P1 sampai P4 dari pemeriksaan awal, lalu 4 sampai 48. Nomor 1, 2 dan 3 tidak ada di buku catatan (urutannya memang P1 sampai P4, baru 4); semua nomor 4 sampai 48 ada, tanpa lubang. Buku catatan memuat 49 keputusan. Keputusan P4 dalam buku catatan ditulis sesudah keputusan 7, saat Tugas 3 berjalan; di sini ia ditaruh bersama P1 sampai P3. Keputusan 19 dalam buku catatan ditulis di tengah baris, bukan di awal baris.

Istilah teknis tetap seperti aslinya. Dua yang sering muncul: "gerbang" berarti pemeriksaan otomatis yang harus lulus sebelum tugas dianggap selesai (`make test` menjalankan seluruh tes otomatis, `make test-e2e` menjalankan tes menyeluruh yang menjalankan panel dan agent bersama-sama, dan `-race` adalah mode tes yang mencari tabrakan antar proses); "gagal tertutup" berarti bila ragu, perintah ditolak.

### 2.1 Sebelum mulai (pemeriksaan awal)

#### Keputusan P1

- **Diputuskan:** Tugas 4 hanya menambahkan dua baris petunjuk penggunaan untuk perintah `approve` (perintah root di server untuk menyetujui bangun ulang). Baris petunjuk `set-role … [--ya]` yang berubah baru ditambahkan di Tugas 5, yang membuat opsi `--ya` (jawab "ya" otomatis) itu.
- **Alasan:** satu baris petunjuk tidak boleh mengiklankan opsi yang belum ada.
- **Biaya bila keliru:** tidak ada, karena hasil akhirnya sama.

#### Keputusan P2

- **Diputuskan:** urutan pelaksanaan menjadi Tugas 1, 2, 4, 3, 5, 6, 7, 8, 9, 10, 11, 12 (Tugas 4 hanya bergantung pada Tugas 2). Tugas 3 juga memperbarui pembantu tes menyeluruh yang membangun ulang master lama, supaya ia menyetujui dulu sebelum mengirim permintaan. Tugas 11 mengganti pembantu itu supaya membaca kode dari formulir.
- **Alasan:** tanpa itu tes menyeluruh merah dari Tugas 3 sampai Tugas 11, dan tugas-tugas sesudahnya tidak bisa memakainya sebagai gerbang.
- **Biaya bila keliru:** beberapa baris pembantu ditulis dua kali.

#### Keputusan P3

- **Diputuskan:** kedua gerbang (`make test`, lalu `make test-e2e`) dijalankan untuk setiap tugas, kecuali Tugas 2 dan 4 (hanya berkas baru dan CLI, cukup `make test`).
- **Alasan:** perubahan perilaku agent sampai ke alur tes menyeluruh panel.
- **Biaya bila keliru:** sekitar 9 menit tambahan per tugas.

#### Keputusan P4

- **Diputuskan:** tugas yang menyentuh `internal/agent` juga menjalankan `go test ./internal/agent/... -race -short -timeout 60m`. Langkah di rencana menulisnya tanpa batas waktu.
- **Alasan:** paket driver MySQL butuh sekitar 40 menit di bawah `-race`, `make test` tidak memakai `-race`, dan gerbang menurut rencana diam-diam kehabisan waktu di Tugas 3.
- **Biaya bila keliru:** sekitar 40 menit tambahan per tugas agent.

### 2.2 Selama tugas (Tugas 2 sampai 11)

#### Keputusan 4

- **Diputuskan:** empat teks yang tidak ada di rencana tetapi ditulis pelaksana Tugas 2 diterima, dengan satu penyesuaian, dan masuk daftar bacaan pemilik: "berkas tidak ada"; "… (DNS): tidak ada alamat"; dan "server ini tidak punya sumber yang dipin" yang menjadi "server ini tidak punya sumber replikasi yang dipin" (kata yang sudah dipakai `promote.go` dan `repoint.go`). Empat catatan kecil pemeriksa pada pemeriksaan persetujuan diperbaiki sekarang, sebagai komit pertama Tugas 4. Isinya: (a) sumber pin dibaca sekali; (b) persetujuan yang waktu habisnya lebih awal dari waktu disetujui ditolak dengan "berkas persetujuan tidak bisa dipakai: waktu habis lebih awal dari waktu persetujuan"; (c) teks kegagalan DNS menyebut host:port di dua tempat; (d) tes dari ujung ke ujung untuk pemeriksaan persetujuan dan untuk berkas permintaan yang tidak tepercaya.
- **Alasan:** pemeriksaan persetujuan adalah batas keamanan, dan pemilik tidak mau ada risiko yang ditunda.
- **Biaya bila keliru:** perubahan Tugas 4 membawa sekitar 60 baris tambahan di luar tugasnya.

#### Keputusan 5

- **Diputuskan:** (i) kegagalan sesudah persetujuan mulai ditulis (menghapus permintaan, atau menyimpan direktori ke disk) kini membatalkan persetujuan itu sebelum melapor; dua teks baru ditulis untuk ini (di Bagian 3, "Teks yang muncul sesudah persetujuan gagal"). (ii) Catatan kecil #3 bertentangan dengan teks B7 di rencana, maka B7 tetap pada kata-katanya tetapi hanya menerima galat milik pencari nama (DNS) lewat jenis galat khusus; galat lain dijawab dengan teks "Pin server ini tidak bisa dibaca …". (iii) Catatan kecil #2, #6 dan #7 ikut putaran perbaikan yang sama; catatan #4 (teks "(tidak diketahui)") masuk daftar bacaan pemilik.
- **Alasan:** CLI ini adalah satu-satunya tempat root memutuskan tentang mengosongkan sebuah server, dan daftar alur di rencana tidak mencakup kegagalan sesudah penulisan. Sebelumnya, pemeriksa menemukan teks "Gagal menulis persetujuan" tampil padahal persetujuan 24 jam yang sah masih tersimpan.
- **Biaya bila keliru:** dua teks operator lagi untuk dibaca pemilik; permintaan yang gagal dihapus kini membatalkan persetujuan alih-alih membiarkannya berlaku.

#### Keputusan 6

- **Diputuskan:** `mysql.tlsTrust` (perintah yang menyimpan berkas sertifikat CA panel, `mysql-ca.crt`, di server) digolongkan "boleh" di server yang dikarantina. Hasilnya 47 perintah: 25 boleh, 17 dilindungi (butuh persetujuan di server), 5 ditolak karena peran. `tlsRequest` dan `tlsInstall` tetap dilindungi. Tabel dan bagian "Keputusan perencana" di rencana perlu dikoreksi.
- **Alasan:** rencana salah menggolongkannya dilindungi, padahal "Periksa data tertinggal" dan jalur penyelamatan memanggilnya di master lama sebelum `mysql.leftover`; menurut spesifikasi jalur penyelamatan harus jalan tanpa persetujuan dan butuh CA itu untuk memeriksa master baru. Perintah itu hanya menulis berkas CA, tidak bisa menyentuh data, dan panel yang disusupi tidak mendapat apa-apa karena panel sendiri adalah CA itu.
- **Biaya bila keliru:** panel yang disusupi bisa mengganti berkas CA di server yang dikarantina (berkas yang sama yang ditunjuk `ssl_ca` MySQL begitu sertifikat dipasang), sehingga pemeriksaan TLS di sana bisa rusak atau melemah; ia tidak bisa menghapus data. Dicatat di audit sebagai risiko sisa.
- **Perlu perhatian pemilik:** ini risiko sisa yang tercatat di audit.

#### Keputusan 7

- **Diputuskan:** aturan 1 dari arahan Tugas 3 dikoreksi. Bila berkas pin tidak bisa dibaca, gerbang menolak setiap perintah yang bukan kelas "boleh", dengan teks yang sudah ada "pin peran/sumber agent tidak bisa dipakai, perintah %s ditolak: %v". Server tanpa berkas pin dianggap belum bertugas (unassigned), bukan tidak terbaca, jadi pemasangan di server baru tidak terpengaruh. Catatan kecil 1 (persetujuan basi bisa berlaku lagi) dan 3 (tes) ikut putaran perbaikan; catatan 2 (mencabut persetujuan tidak menghentikan pekerjaan yang sudah berjalan) menjadi batas yang didokumentasikan; catatan 4 ditunda ke Tugas 11.
- **Alasan:** premis rencana ("Allow menolak perintah terjaga") salah untuk penangan yang tidak pernah memanggil `d.allow`, dan spesifikasi menuntut gagal tertutup. Persetujuan basi adalah lubang nyata dan tesnya murah.
- **Biaya bila keliru:** dengan berkas pin rusak, `mysql.install` (memasang MySQL) dan `mysql.sweepDumpUsers` (membersihkan akun dump) ditolak pada peran apa pun sampai root memperbaiki berkas itu; sebelumnya keduanya jalan.

#### Keputusan 8

- **Diputuskan:** `scripts/install_test.sh` tidak boleh menjalankan `set-role master`; ia menulis berkas pin peran di direktori ujinya langsung (tes hanya butuh berkas peran yang checksum-nya dibandingkan setelah upgrade).
- **Alasan:** menjalankan tes tidak boleh menyentuh konfigurasi MySQL asli mesin itu. Sejak Tugas 5, `set-role master` mencari tata letak MySQL yang asli apa pun nilai `--dir`, jadi `make test` sebagai root bisa melepas kunci start MySQL yang asli.
- **Biaya bila keliru:** tes pemasangan tidak lagi menjalankan CLI `set-role` (tes Go yang melakukannya).

#### Keputusan 9

- **Diputuskan:** (i) pin yang tidak bisa dibaca dianggap "mungkin dikarantina": tanpa `--ya` pertanyaan tetap diajukan, dengan teks baru (di Bagian 3) sebagai ganti kalimat biasa. (ii) Teks baru bila kunci start sudah dilepas tetapi penulisan peran gagal. (iii) Peran dibaca lagi sesudah jawaban "ya"; bila berubah, ada teks baru, kode keluar 1, dan tidak ada yang berubah. (iv) Catatan kecil 3 sampai 5 ikut putaran.
- **Alasan:** arahan tugas ("pertanyaan hanya di server yang dikarantina") menganggap peran selalu terbaca; keadaan yang tidak diketahui harus gagal tertutup.
- **Biaya bila keliru:** tiga teks lagi untuk pemilik; memperbaiki pin yang rusak dengan `set-role` kini butuh jawaban "ya" atau `--ya`.

#### Keputusan 10

- **Diputuskan:** (i) tes dengan mysqld sungguhan dijalankan dengan tata letak yang tidak memakai `--defaults-file`; (ii) bila argumen `--print-defaults` agent memuat `--defaults-file`, HOME tidak relevan (terukur, dan sesuai aturan MySQL: hanya berkas itu yang dibaca), jadi syarat "direktori home akun layanan" dilewati dan pemeriksaan jalan satu kali dengan HOME=/nonexistent; (iii) awalan `maximum` pada `init_file` di mana pun membuat kunci dianggap tidak terjamin, dengan teks baru (di Bagian 3, bagian Peringatan).
- **Alasan:** kode mengikuti bukti, seperti yang dituntut spesifikasi 5.4. Tanpa (ii), server yang menyala dari satu berkas dengan akun layanan di bawah `/home` akan menampilkan kunci start "tidak terjamin" tanpa alasan.
- **Biaya bila keliru:** pada (ii), build MySQL yang membaca `~/.my.cnf` walau ada `--defaults-file` akan lolos dari pemeriksaan (tidak ada yang diketahui; diukur pada versi 8.0.46).
- **Perlu perhatian pemilik:** bila (ii) keliru, kunci start bisa dinilai "terkunci" padahal MySQL bisa menyala dalam keadaan bisa ditulis (risiko dua master).

#### Keputusan 11

- **Diputuskan:** (i) home akun layanan yang berada di atau di bawah awalan terlindungi (`/home`, `/root`, `/run/user`) dianggap tersembunyi kecuali agent bisa mendaftarnya dan isinya tidak kosong (menutup kasus `/root`); home asli yang kosong di bawah awalan itu juga dianggap tersembunyi. (ii) Akun layanan yang kolom home-nya kosong mendapat teks sendiri (di Bagian 3, bagian Peringatan) sebagai ganti "tidak ditemukan: direktori home kosong". (iii) Dua tes murah dan satu komentar kode ikut putaran.
- **Alasan:** akunnya ada, jadi kata lama menyesatkan; dan spesifikasi 5.4 menuntut gagal tertutup.
- **Biaya bila keliru:** MySQL yang akun layanannya punya home yang benar-benar kosong di bawah `/home` atau `/root` akan menampilkan kunci start sebagai tidak terjamin.

#### Keputusan 12

- **Diputuskan:** (i) agent menghapus "hasil start" (catatan bagaimana MySQL menyala selama karantina: `late` = sempat bisa ditulis, `failed`, `bypassed`, `ok`) setiap kali ia memindahkan server ke karantina dari peran lain, sebelum pin ditulis. Kegagalan menghapus dicatat pada tingkat Error dan tidak menghentikan karantina, karena karantina lebih penting daripada laporan. Jalur CLI `set-role quarantined` tidak tercakup (tidak ada Driver di sana; putaran penjaga agent berikutnya menghapusnya bila perannya bukan karantina). Ini dicatat sebagai celah sempit yang diketahui. (ii) Catatan kecil 1 sampai 3 ikut putaran.
- **Alasan:** `late` yang hilang diam-diam adalah tepat yang hendak dicegah tugas ini.
- **Biaya bila keliru:** tidak ada selain beberapa baris kode.
- **Perlu perhatian pemilik:** celah sempit yang diketahui (hasil start lama bisa tidak terhapus bila `set-role quarantined` dijalankan saat agent berhenti) dicatat untuk audit.

#### Keputusan 13

- **Diputuskan:** catatan kecil 2 sampai 5 Tugas 8 diperbaiki sekarang dalam putaran lanjutan oleh pelaksana yang sama; catatan 1 (pemberitahuan "kunci dipertahankan" menumpuk selama panel offline) ditunda.
- **Alasan:** tes pewaktuan yang goyah akan membuat setiap tugas berikutnya menanggung gerbang 40 menit; yang lain hanya beberapa baris di kode yang baru ditulis tugas ini.
- **Biaya bila keliru:** sekitar 20 menit.

#### Keputusan 14

- **Diputuskan:** aturan penghapusan peringatan ganda di rencana (Tugas 9) diganti. Panel menyimpan tanda yang tahan lama per (server, jenis peringatan) tentang start terbaru yang sudah dihitungnya (tabel baru `start_marks`, migrasi 0012). Jalur snapshot (laporan lengkap keadaan agent) membuka peringatan hanya untuk start yang belum dihitung. Kejadian ditekan hanya bila snapshot terbaru menunjukkan start sejenis yang sudah dihitung; selain itu perilakunya seperti sebelum tugas ini, jadi agent lama tidak berubah. Start `ok` menandai `failed` dan `bypassed` selesai. Umur dibatasi ke 0 sampai 10 tahun (dalam detik). Hasil yang diputuskan sebelum promosi karantina dimulai diabaikan. Dua tes yang sempat diubah dikembalikan.
- **Alasan:** aturan rencana salah; peringatan `late` yang terlewat adalah risiko yang hendak dihilangkan tugas ini.
- **Biaya bila keliru:** satu migrasi dan satu tabel kecil lagi. Tiga batas sempit tetap ada: langkah mundur jam agent; kejadian yang datang sebelum snapshot pertama sesudah panel dimulai ulang; dan `bypassed` yang kejadiannya dan kejadian `ok` sesudahnya sama-sama hilang. Ketiganya akan dicantumkan di audit.
- **Diganti/dipersempit oleh keputusan 15** (bagian jalur kejadian), lalu seluruhnya oleh **keputusan 16**.
- **Perlu perhatian pemilik:** tiga batas sempit di atas tetap ada sebagai risiko tercatat.

#### Keputusan 15

- **Diputuskan:** kejadian start tidak membawa identitas start yang dilaporkannya, dan itu akar masalahnya. Setiap start yang dinilai kini diberi `start_id` (waktu keputusan agent, buram bagi panel) yang dibawa kejadian start dan hasil start di snapshot. Tanda di panel menyimpan id start terakhir yang dihitung per (server, jenis) dan hanya dibandingkan untuk kesamaan. Peringatan dibuka dulu, tanda ditulis hanya sesudah berhasil dibuka. Aturan "kejadian start menandai jenis yang ditutupnya" dan salinan snapshot di memori dihapus. `failed` saat MySQL berjalan tidak membuka apa-apa dan tidak menandai. `bypassed` yang hanya diketahui dari snapshot terbuka walau ada start `ok` sesudahnya. Migrasi 0012 diubah di tempat (belum dirilis).
- **Alasan:** kesamaan id tidak bisa menghilangkan peringatan lewat kejadian yang terlambat, langkah mundur jam, atau panel yang dimulai ulang; urutan menurut jam mana pun bisa.
- **Biaya bila keliru:** satu bidang protokol di tiga pesan; dua batas sempit tetap ada (`failed` yang kejadiannya hilang saat MySQL berjalan; peringatan `bypassed` yang dibuka dari snapshot tetap sampai kejadian start berikutnya).
- Keputusan ini **menggantikan bagian jalur kejadian dari keputusan 14**. **Diganti oleh keputusan 16.**

#### Keputusan 16

- **Diputuskan:** perubahan ketiga dan struktural pada aturan Tugas 9. Menyimpulkan keadaan dari aliran kejadian yang bisa tertunda, tertukar urutan dengan snapshot, atau terbuang adalah akar masalahnya, padahal agent tahu keadaannya. Berkas hasil start agent dan snapshot-nya kini memuat start terakhir dari tiap jenis buruk (`late`, `failed`, `bypassed`) dengan id dan bendera `over` (sudah berakhir). Panel menyelaraskan tiga peringatan start ke keadaan itu pada setiap laporan (hasilnya sama berapa kali pun, tidak bergantung urutan) dan tidak lagi membiarkan kejadian start yang membawa id membuka atau menutup peringatan. Kejadian tanpa id (agent lama) berperilaku seperti sebelum Tugas 9. Peringatan `blocked` dan `bypassed` yang bertanda ditutup bila laporan menunjukkan karantina berakhir. Karena ini mengerjakan ulang agent, protokol dan panel, dan konteks pelaksana asli hampir penuh sesudah dua putaran, putaran ini diberikan kepada pelaksana baru pada model yang terkuat (aturan putaran 4 sampai 5 dipakai satu putaran lebih awal).
- **Alasan:** tiga putaran tambalan pada rancangan yang tidak sehat lebih buruk daripada satu pembangunan ulang yang sehat.
- **Biaya bila keliru:** sekitar 3 jam; peringatan jenis ini kini mengikuti selang laporan, bukan saat kejadian.
- **Mengganti keputusan 14 dan 15.** Disempurnakan oleh keputusan 17.

#### Keputusan 17

- **Diputuskan:** perbaikan seperti tertulis di catatan temuan pemeriksa. (1) Laporan yang ditolak `ingest` (misalnya nomor urut lebih lama) tidak lagi diselaraskan. (2) Keadaan start agent di memori yang menjadi acuan, berkas hanya penyimpanan, dan kegagalan menulis dicoba lagi tiap putaran penjaga. (3) Aturan 5 untuk `late`: peringatan yang sudah diperiksa sisanya ditutup dulu sebelum yang baru diadopsi. (4) Penutupan berdasarkan pin berjalan sekali tiap akhir karantina dan menghapus tanda `blocked` dan `bypassed`. (5) Aturan 1 melewati kueri bila tanda sama dengan id. (6) Paling banyak 16 entri `Kinds`.
- **Alasan:** tiap perbaikan menutup satu jalan konkret menuju peringatan yang hilang atau dobel yang didemonstrasikan pemeriksa.
- **Biaya bila keliru:** satu siklus gerbang lagi (sekitar 2 jam). Batas yang diterima bertambah: kegagalan menulis disusul agent dimulai ulang sebelum percobaan ulang berhasil; dan peringatan tanpa id yang terbuka selama agent di-upgrade tetap terbuka.
- **Perlu perhatian pemilik:** dua batas yang diterima di atas tetap menjadi risiko tercatat.

#### Keputusan 18

- **Diputuskan:** perbaikan seperti tertulis di catatan temuan Tugas 11. Kode yang bisa dipakai adalah tepat enam angka ASCII, diperiksa di tempat jawaban agent masuk panel. Jawaban yang tidak bisa dipahami panel dianggap "tidak bisa dibaca" dan tidak pernah dianggap "disetujui". Ada teks baru untuk penolakan tanpa kode, dan petunjuk baru di sebelah tombol yang dimatikan (batasan kegunaan dari pemilik: operator tidak boleh dibiarkan menebak kenapa tombol mati). "Perketat TLS" melewati server yang dikarantina, dan percobaan ulang pekerjaan TLS di sana ditolak dengan teks sertifikat MySQL yang dipasang saat dibangun ulang.
- **Alasan:** agent yang disusupi tidak boleh bisa membuat panel menampilkan perintah root lain.
- **Biaya bila keliru:** dua teks lagi untuk pemilik; satu siklus gerbang lagi.

### 2.3 Tinjauan akhir dan penyusunan putaran perbaikan

#### Keputusan 19

- **Diputuskan:** tinjauan seluruh cabang dan audit keamanan dijalankan dengan model terkuat kedua, tidak menunggu dua hari untuk model terkuat. Batas pemakaian mingguan model terkuat baru direset pada 16 November pukul 09.00 UTC.
- **Alasan:** pemilik minta lanjut, dan model terkuat kedua melakukan setiap pemeriksaan mendalam rencana ini.
- **Biaya bila keliru:** temuan yang akan tertangkap model yang lebih kuat bisa terlewat. Pengulangan opsional dengan model terkuat ditawarkan kepada pemilik sebelum merge.
- **Perlu perhatian pemilik:** pengulangan opsional itu adalah keputusan yang diserahkan kepada pemilik.

#### Keputusan 20

- **Diputuskan:** semua temuan audit sisi panel (P-1 sampai P-8) dan pengetatan header masuk putaran perbaikan. Untuk P-2, alasan sebuah persetujuan tidak berlaku lagi menjadi himpunan kode tertutup yang kata-katanya dibuat panel (bidang protokol baru `invalid_code`, delapan alasan dari panel ditambah satu cadangan). Kalimat agent yang tersisa ditampilkan sebagai kutipan berlabel "Pesan dari agent {{server}} (bukan dari panel):" di tempat yang jauh dari kotak perintah. Ada teks baru untuk GET lintas situs pada formulir: "Klik Periksa lagi untuk melihat persetujuan di server."
- **Alasan:** perintah yang ditampilkan panel untuk dijalankan root adalah aset utama cabang ini; server yang bermusuhan tidak boleh bisa menulis kata di sebelahnya yang terbaca seperti kata panel.
- **Biaya bila keliru:** satu bidang protokol, sekitar sepuluh teks operator lagi, dan putaran perbaikan yang lebih besar.

#### Keputusan 21

- **Diputuskan:** semua temuan tinjauan akhir (F-1 sampai F-8), baris triase "sebaiknya diperbaiki" (F-9), dan kecurigaan terhadap `mysql.connect` (F-10, ditentukan lewat tes mysqld sungguhan, diperbaiki bila terbukti) masuk putaran perbaikan. Teks baru: tiga teks baru dan satu teks (C12) yang kalimatnya diubah.
- **Alasan:** F-1 dan F-2 menentukan apakah operator tahu dalam hitungan detik atau menit bahwa server yang dikarantina pernah ditulisi; sisanya kecil dan masing-masing menghapus satu pesan yang salah atau buntu.
- **Biaya bila keliru:** empat teks lagi untuk pemilik; putaran perbaikan yang lebih besar.
- **Dipersempit oleh keputusan 24** (kalimat C12).

#### Keputusan 22

- **Diputuskan:** (A-1) karantina hanya diakhiri bila setiap database non-sistem di server dimuat dari sumber yang dipin oleh bangun ulang ini (atau sesudah CLONE); ada teks baru (A6). (A-2) `mysql.connect` hanya menerima MySQL di mesin ini untuk setiap peran (menutup risiko R-079) dan menolak akun yang kekurangan hak di server yang dikarantina. (A-3) `--print-defaults` yang bergantung pada HOME dijalankan sebagai uid dan gid akun layanan, di samping satu jalan sebagai root untuk berkas sistem (keduanya harus mengatakan terkunci), dengan batas keluaran 256 KiB, 20 entri, dan 10 detik; bukti dikumpulkan dulu. (A-4) setiap alamat hasil pencarian harus sama dengan yang disetujui, dan `approve` selalu mencari alamat serta menolak nama yang punya beberapa alamat (ada teks baru). Sisa "tersambung berdasarkan nama" dicatat. (A-5) `mysql.install` ditolak di server yang dikarantina walau ada persetujuan, `dropUser` dibatasi seperti di master, dan layar `approve` mendapat satu baris lagi. (A-7) CLI menulis baris ke log sistem, dan tiap perintah yang disetujui dicatat satu baris Info.
- **Alasan:** A-1 adalah hasil yang justru hendak dicegah seluruh rencana: satu persetujuan bisa membuat panel yang disusupi mengakhiri karantina dengan data lama tetap ada. Sisanya menutup jalan yang didemonstrasikan audit.
- **Biaya bila keliru:** aturan A-1 bisa menolak bangun ulang berfilter yang sah (pelaksana harus memeriksa alur sebenarnya dulu dan berhenti bila begitu); A-2 bisa menolak pemasangan yang MySQL-nya dijangkau lewat nama yang bukan lokal; A-4 menolak persetujuan untuk nama dengan beberapa alamat (IPv4 dan IPv6) sampai operator menetapkan satu.
- **Perlu perhatian pemilik:** A-1 menyangkut dua master yang bisa ditulis dan data lama yang tertinggal; sisa "tersambung berdasarkan nama" tercatat sebagai risiko.

#### Keputusan 23

- **Diputuskan:** putaran perbaikan dibagi antara dua pelaksana yang bekerja berurutan (sisi agent dulu, lalu sisi panel dan README), bukan satu.
- **Alasan:** sekitar empat puluh butir di agent, protokol, panel dan README melebihi yang bisa dipegang satu konteks dengan andal.
- **Biaya bila keliru:** satu siklus gerbang tambahan (sekitar 1,5 jam).
- **Dilampaui oleh keputusan 27, 31, 36, 41 dan 48** (jumlah bagian bertambah menjadi delapan).

### 2.4 Putaran perbaikan, bagian A sampai E

#### Keputusan 24

- **Diputuskan:** kalimat terakhir C12 menjadi "Perhatian: server ini tidak lagi dikarantina; agent tidak lagi menjaga MySQL-nya tetap baca-saja (read-only)." Kalimat ini dicetak juga bila pin tidak terbaca dan targetnya `unassigned`. Kalimat itu menggantikan kalimat yang diberikan temuan.
- **Alasan:** kalimat yang diberikan salah (diperiksa pelaksana bagian A terhadap penjaga: pada server `unassigned` sesudah karantina, penjaga tidak lagi memasang baca-saja). Teks yang tampil kepada root tepat sesudah mengakhiri karantina tidak boleh menjanjikan perlindungan yang sudah hilang.
- **Biaya bila keliru:** operator membaca peringatan di tempat yang mungkin cukup kalimat lebih lunak; tidak ada perilaku yang bergantung padanya.

#### Keputusan 25

- **Diputuskan:** bagian B mendapat dua pengecualian sempit dari larangan mengubah `internal/agent`: teks dan syarat cetak C12 di `cli.go`, dan celah (seam) Go yang paling kecil supaya tes menyeluruh panel bisa menjalankan CLI dengan tata letak MySQL instance tes dan penulis log sistemnya sendiri (tanpa variabel lingkungan, flag, atau berkas). Gerbang tambahan `go test ./internal/agent -race -short` hanya untuk paket itu, bukan suite driver yang 40 menit.
- **Alasan:** paket driver tidak disentuh, dan `make test` menjalankannya tanpa `-race`.
- **Biaya bila keliru:** tabrakan antar proses (race) di paket driver yang dimasukkan bagian B tidak akan terlihat; bagian B tidak boleh menyunting paket itu, dan pemeriksaan ulang memeriksa jalur berkas pada perubahannya.

#### Keputusan 26

- **Diputuskan:** aturan A-1 yang lebih ketat dari bagian A tetap berlaku: database dihitung tersalin hanya bila dibuat oleh pemuatan itu.
- **Alasan:** dump yang dimuat ke database yang sudah ada meninggalkan tabel lain di database itu, yaitu data lama yang hendak dihapus aturan ini. Alur panel sendiri menjatuhkan semua sisa lebih dulu sehingga tetap selesai (pelaksana memeriksa; tes bangun ulang penuh dan berfilter lulus).
- **Biaya bila keliru:** bangun ulang lewat jalur yang tidak biasa (database kosong buatan tangan, atau sambung ulang sesudah agent dimulai ulang yang kehilangan catatan di memori) tetap dikarantina dengan teks A6 dan butuh bangun ulang kedua atau `set-role` oleh root.

#### Keputusan 27

- **Diputuskan:** putaran perbaikan mendapat bagian ketiga (C, sisi agent, sesudah B), alih-alih menyerahkan varian A-1 yang masih terbuka kepada pemilik sebagai lubang yang diketahui. Varian itu: daftar database untuk keputusan penyelesaian diambil lewat akun admin tersimpan; akun tanpa hak SELECT global atau SHOW DATABASES hanya melihat databasenya sendiri, jadi dump satu database tetap mengakhiri karantina dengan semua data lama tertinggal.
- **Alasan:** putaran masih berjalan; varian itu adalah hasil dari temuan HIGH itu sendiri (master lama bisa ditulis dengan data lama: dua master); instruksi tetap pemilik adalah tidak ada risiko keamanan atau kehilangan data yang disembunyikan; dan aturan proses "tanpa putaran perbaikan kedua" ada untuk menghentikan poles tanpa ujung, bukan untuk mengirim jalur dua master yang sudah diketahui.
- **Biaya bila keliru:** satu siklus gerbang lagi (sekitar 2 jam) dan satu pemeriksaan ulang. Bila pemeriksaan ulang bagian C menemukan A-1 masih terbuka, ia menjadi penghalang merge bagi pemilik, bukan bagian keempat.
- **Dilampaui sebagian oleh keputusan 36** (bagian tambahan untuk butir-butir tertentu).
- **Perlu perhatian pemilik:** keputusan ini menyangkut kemungkinan dua master yang bisa ditulis.

#### Keputusan 28

- **Diputuskan:** bagian C juga mengambil dua catatan kecil pemeriksaan ulang dan tiga sisa kecil: tersambung ke alamat yang sudah diperiksa; menolak berkas `log_error` yang bukan berkas biasa sebelum root membukanya; dan `configureReplication` gagal secara terlihat bila karantina tidak diakhiri karena agent dimulai ulang di tengah bangun ulang. Ada empat teks baru yang persis.
- **Alasan:** masing-masing hanya beberapa baris di sebelah kode yang toh dibuka bagian C, dan masing-masing menghapus satu jalan yang disebut audit atau satu pesan yang mengarahkan operator ke arah yang salah.
- **Biaya bila keliru:** lebih banyak teks untuk dibaca pemilik; butir ketiga (C-6) bisa menggagalkan pekerjaan yang diharapkan lulus oleh jalur yang sah (pelaksana harus memeriksa dulu dan tidak mengubah apa pun bila begitu).
- **Dipersempit oleh keputusan 35** (kata-kata teks C-6).

#### Keputusan 29

- **Diputuskan:** temuan N-1 sampai N-4 dari pemeriksaan ulang sisi panel diberikan kepada pelaksana bagian C sebagai tambahan (berkas panel boleh disentuh hanya untuk keempatnya), bukan menjadi bagian keempat.
- **Alasan:** pelaksana itu sudah menjalankan gerbang panel dan tes menyeluruh; N-1 menghalangi bangun ulang yang sah tanpa jalan keluar di layar (aturan nama host terlalu ketat, jadi alamat master seperti `db_master` membuat jawaban persetujuan "tidak bisa dipakai" selamanya).
- **Biaya bila keliru:** konteks bagian C memuat empat butir kecil lagi; bila tidak selesai, diserahkan dan diputuskan, dan N-1 muncul kepada pemilik sebagai penghalang merge.
- **Diganti oleh keputusan 31** (cara menyalurkan sisa temuan).

#### Keputusan 30

- **Diputuskan:** teks agent yang tidak berlabel di galat pekerjaan biasa (banyak baris, bisa berisi instruksi yang tampak seperti dari panel, dari agent yang bermusuhan lewat langkah gagal mana pun) TIDAK diperbaiki di putaran ini. Ia dicatat sebagai risiko terbuka (melebarkan baris "teks agent di luar pembantu" yang sudah ada) dan diusulkan sebagai tugas pertama rencana 5b-2.
- **Alasan:** memperbaikinya mengubah teks galat setiap kegagalan pekerjaan dan dengan itu puluhan asersi tes dan tes menyeluruh, jadi itu tugas tersendiri; ia sudah ada sebelum rencana ini; jalur persetujuan yang ditambah rencana ini sudah ditutup oleh P-1 dan P-2.
- **Biaya bila keliru:** sampai 5b-2, agent di server yang disusupi masih bisa menulis kata di galat pekerjaan yang terbaca seperti dari panel. Pemilik diberi tahu dan bisa memintanya sebelum merge.
- **Perlu perhatian pemilik:** risiko terbuka ini bisa diminta pemilik untuk diperbaiki sebelum merge.

#### Keputusan 31

- **Diputuskan:** sisa temuan pemeriksaan ulang bagian B (README C-1, I-1, m-1 sampai m-12; panel N-1 sampai N-4) diberikan kepada pelaksana keempat (bagian D, README dan panel, sesudah C), bukan kepada bagian C dan bukan kepada pemilik sebagai temuan terbuka. C-1 adalah langkah README "Melepas kunci start dengan tangan" yang menyuruh menyalakan MySQL yang mati dengan anggapan kunci membuatnya baca-saja. Sesudah pemeriksaan ulang bagian D tidak ada pelaksana lagi: yang masih terbuka menjadi penghalang merge atau risiko tercatat.
- **Alasan:** langkah README yang bisa menyalakan master lama yang dikarantina dalam keadaan bisa ditulis adalah kerugian persis yang hendak dicegah rencana ini, dan N-1 menghalangi bangun ulang yang sah; bagian C menyimpan konteksnya untuk butir HIGH di agent.
- **Biaya bila keliru:** satu siklus gerbang lagi (sekitar 40 menit) dan satu pemeriksaan ulang; putaran kini punya empat bagian, bukan satu seperti yang ditetapkan proses.
- **Menggantikan cara penyaluran pada keputusan 29.** Klausa "tidak ada pelaksana lagi sesudah D" **dilampaui oleh keputusan 36, 41 dan 48.**

#### Keputusan 32

- **Diputuskan:** kalimat terakhir teks A6 menjadi "Ulangi Bangun ulang sebagai replika dari panel. Bila panel lalu menolak karena database itu tidak ada di master baru, lihat README, bagian "Database yang tidak ada di master baru"." README mendapat bagian itu: cadangkan database dengan `mysqldump` di server, lalu jatuhkan sebagai root MySQL dengan satu perintah yang hanya mengangkat `super_read_only` (`SET GLOBAL`; `read_only` tetap ON), lalu bangun ulang lagi.
- **Alasan:** teks lama menyebut tindakan panel yang tidak ada dan membuat operator berputar; membuang database dengan tangan oleh root di server sesuai model kepercayaan (root di server yang memutuskan tentang datanya).
- **Biaya bila keliru:** README mengajari operator menjatuhkan database yang mungkin tidak ada di tempat lain. Langkah cadangan wajib di dalam teks, dan pemilik diminta membaca bagian ini.
- **Perlu perhatian pemilik:** ini prosedur manual yang bisa menghapus data di server produksi (lihat Bagian 4).

#### Keputusan 33

- **Diputuskan:** untuk pelepasan kunci start dengan tangan, README menyuruh operator memutus aplikasi sebelum menyalakan MySQL. README menganjurkan baris opsi `super_read_only=ON` hanya bila pengukuran menunjukkan bahwa ia menang atas nilai OFF yang tersimpan; bila tidak, ia berkata terus terang bahwa MySQL bisa menyala dalam keadaan bisa ditulis. (Catatan dari bagian D: pengukuran menunjukkan nilai OFF yang tersimpan mengalahkan baris opsi itu.)
- **Alasan:** README tidak boleh menjanjikan perlindungan yang tidak diukur siapa pun.
- **Biaya bila keliru:** prosedurnya lebih panjang; operator yang terburu-buru bisa melewati langkah pemutusan aplikasi.
- **Perlu perhatian pemilik:** melewati langkah itu bisa membuat dua master bisa ditulis (lihat Bagian 4).

#### Keputusan 34

- **Diputuskan:** aturan C-1 bagian C tetap seperti dibangun (daftar database dianggap lengkap hanya bila SHOW DATABASES atau ALL diberikan langsung pada `*.*` tanpa baris REVOKE yang menyebut SHOW DATABASES atau ALL, atau SELECT atau ALL global diberikan langsung dengan `partial_revokes=0`; dibaca menurut nama akun), walau berbeda dari arahan.
- **Alasan:** aturan dalam arahan dibantah oleh pengukuran.
- **Biaya bila keliru:** akun dengan hak minimum di server dengan `partial_revokes=ON` ditolak untuk bangun ulang sampai satu perintah GRANT dijalankan (sudah didokumentasikan).

#### Keputusan 35

- **Diputuskan:** kalimat terakhir teks C-6 diubah supaya tidak terbaca sebagai tombol coba-ulang pada pekerjaan yang gagal (yang di sana tidak akan pernah berhasil). Perubahan ini diberikan kepada bagian D sebagai butir D-8, dengan satu pengecualian satu string di `internal/agent`.
- **Alasan:** penolakan yang menunjuk tombol yang berputar adalah kebingungan yang diminta pemilik untuk dihindari.
- **Biaya bila keliru:** `make test` bagian D menjalankan ulang paket agent (sekitar 40 menit lebih).
- **Mempersempit keputusan 28** (kata-kata C-6).

#### Keputusan 36

- **Diputuskan:** bagian kelima (E) memperbaiki lubang bukti yang bisa dipalsukan (nama akun, peran, atau kolom yang memuat " ON *.* TO " bisa memalsukan hak SHOW DATABASES) dengan tiga lapis yang saling bebas: (1) hanya token hak yang dikenali dalam pola bersama; (2) akun pada baris harus sama dengan akun yang dibuktikan; (3) akun dengan karakter di luar himpunan kecil tidak dibuktikan, dengan teksnya sendiri. Ditambah dua catatan kecil C-4 dan satu balapan C-5. Pelaksana bagian C dilanjutkan sesudah bagian D didorong (satu penulis pada satu waktu). Keputusan ini menimpa klausa "tidak ada bagian lanjutan" dari keputusan 27 dan 31, hanya untuk butir-butir ini.
- **Alasan:** niat pemilik adalah tidak ada risiko yang diketahui dibiarkan; lubang itu ada di pemeriksaan yang mengakhiri karantina; dan perbaikannya kecil dan konkret (aturan pengurai, bukan perancangan ulang).
- **Biaya bila keliru:** satu siklus gerbang penuh lagi (sekitar 1,7 jam) dan satu pemeriksaan ulang; putaran menjadi lima bagian. Sesudah pemeriksaan ulang bagian E, yang masih terbuka pada butir ini menjadi penghalang merge, dan tidak ada bagian F untuk butir ini. (Keputusan 41 kemudian tetap menambah bagian F hanya untuk README.)
- **Perlu perhatian pemilik:** lubang ini ada di pemeriksaan yang menentukan kapan karantina berakhir.

#### Keputusan 37

- **Diputuskan:** pilihan (1) dalam bentuk terkecil. Setelah satu langkah bangun ulang gagal, server tercatat sebagai replika dengan baris replika yang gagal, dan web tidak menawarkan bangun ulang lagi. Lapisan web kini menerima "peran replika, baris replika gagal, dan master lama dari sebuah topologi" di mana pun ia menerima peran `quarantined` untuk formulir bangun ulang dan kotak "Master lama" (butir D-9). Tindakan yang tidak bisa jalan pada keadaan itu tidak ditawarkan. Monitor tidak diubah; apa yang dilewatkannya pada keadaan itu dilaporkan dan dicatat sebagai risiko.
- **Alasan:** orkestrator sudah dirancang untuk bangun ulang kedua, hanya web yang tidak menawarkannya; alternatifnya membiarkan master lama yang dikarantina tanpa jalan maju di panel sesudah, misalnya, mulai ulang saat bangun ulang.
- **Biaya bila keliru:** keadaan baru yang bisa dicapai pada formulir bangun ulang (masih di belakang gerbang persetujuan dan pemeriksaan agent sendiri); lingkup bagian D bertambah satu perubahan web.
- **Diperluas oleh keputusan 39; ingin diganti oleh keputusan 42, lalu dipersempit oleh keputusan 43.**

#### Keputusan 38

- **Diputuskan:** dari temuan penyelidikan item-item audit yang belum terkonfirmasi, hanya temuan baru yang masuk bagian E (E-5: tolak `mysql.leftover` dan `mysql.salvage` di server yang dikarantina tanpa pin sumber, di sebelah aturan `dropDatabases`). Dua temuan lain TIDAK diperbaiki di putaran ini: (1b) master lama yang menjadi bisa ditulis selagi MySQL tetap hidup tidak dikunci ulang oleh penjaga dan tidak diumumkan panel; (2) `mysqlbinlog` dijalankan sebagai root pada jalur yang dikuasai akun layanan MySQL. Keduanya dicatat sebagai risiko terbuka, diusulkan sebagai tugas pertama 5b-2 bersama butir keputusan 30, dan diberitahukan kepada pemilik sebelum keputusan merge.
- **Alasan:** (1b) butuh penjaga yang terus-menerus yang tidak boleh melawan pengangkatan baca-saja oleh agent sendiri, atau jenis peringatan panel yang baru; (2) butuh `mysqlbinlog` dijalankan sebagai uid akun layanan; keduanya rancangan lama, tingkat Low, dan pekerjaan perancangan, bukan perbaikan. E-5 adalah dua baris aturan peran di keluarga berkas yang toh diubah bagian E, dan menutup jalan pembocoran data.
- **Biaya bila keliru:** master lama yang dibuat bisa ditulis saat berjalan (oleh root MySQL, sebuah event, atau pemulihan yang gagal) tetap bisa ditulis tanpa pengumuman sampai MySQL atau agent dimulai ulang; akun layanan MySQL yang bermusuhan bisa membuat root membuka sebuah perangkat atau membuat penyelamatan tergantung.
- **Perlu perhatian pemilik:** dua risiko terbuka; yang pertama menyangkut master lama yang bisa ditulis tanpa diumumkan.

#### Keputusan 39

- **Diputuskan:** butir D-10: orkestrator menolak "Resync ulang" dan "Lepas replika" untuk server dalam keadaan master lama yang bangun ulangnya gagal. Perintah README yang memakai `sql_log_bin=OFF` (perintah itu tidak dicatat di binary log) dan pengaturan header `bluetooth=()` tidak diubah.
- **Alasan:** menyembunyikan tombol bukan kontrol; resync di keadaan itu berakhir dengan server yang masih dicatat panel sebagai master lama.
- **Biaya bila keliru:** operator yang mengandalkan Resync atau Lepas untuk bangun ulang master lama yang gagal harus memakai formulir bangun ulang atau menghapus server.
- **Memperluas keputusan 37.**

#### Keputusan 40

- **Diputuskan:** diamnya monitor untuk master lama yang bangun ulangnya gagal (tidak ada peringatan kunci start dan start buruk selama peran di panel `replica` dengan baris gagal) TIDAK diperbaiki di putaran ini. Ia dicatat sebagai risiko terbuka dan diusulkan untuk 5b-2 bersama butir "bisa ditulis saat hidup" (satu rancangan: peringatan master lama mengikuti pin yang dilaporkan agent, bukan peran di panel).
- **Alasan:** hal itu sudah ada sebelum rencana ini; penjaga agent sendiri tetap bertindak pada setiap start; dan logika peran monitor adalah mesin keadaan yang kemungkinan besar rusak oleh perubahan cepat.
- **Biaya bila keliru:** di antara bangun ulang yang gagal dan bangun ulang berikutnya, start yang buruk pada master lama diperbaiki agent tetapi tidak diumumkan.
- **Perlu perhatian pemilik:** risiko terbuka yang tercatat.

### 2.5 Putaran perbaikan, bagian F, G, H dan penutup

#### Keputusan 41

- **Diputuskan:** (a) teks penolakan daftar database dan teks "nama akun tidak didukung" milik agent diubah supaya menunjuk bagian README dan tombol "Ulangi dari tahap ini" (butir E-6, untuk pelaksana bagian E; ia mengukur GRANT pada server `super_read_only` dan tidak lagi menyunting README). (b) Bagian F khusus README: F-1 bagian baru "Memberi hak SHOW DATABASES di server yang dikarantina"; F-2 langkah memutus aplikasi; F-3 pemeriksaan bahwa operator di server yang benar dan aturan berhenti; F-4 catatan kecil m-A sampai m-I. Bagian F diberikan kepada pelaksana bagian D sesudah bagian E didorong, dan diikuti satu pemeriksaan ulang README yang hanya melihat bagian yang berubah. Latarnya (dari pemeriksaan ulang README bagian D): GRANT yang disarankan sesudah penolakan daftar tidak bisa dijalankan di server yang dikarantina (`super_read_only` menjawab ERROR 1290); langkah 2 pelepasan kunci start menawarkan aturan firewall sebagai ganti menghentikan aplikasi; dan bagian "jatuhkan database" tidak pernah membuktikan bahwa operator ada di master lama.
- **Alasan:** tiap butir yang masih terbuka adalah langkah yang akan diikuti operator pada server produksi; putaran khusus README tidak butuh gerbang tes.
- **Biaya bila keliru:** sekitar satu jam lagi; putaran menjadi enam bagian. Batas keras: sesudah pemeriksaan ulang bagian E dan F tidak ada lagi yang dikirim; yang masih terbuka diserahkan kepada pemilik.
- **Batas kerasnya dilampaui oleh keputusan 48.**

#### Keputusan 42

- **Diputuskan:** tambalan untuk keadaan "peran replika, baris gagal, master lama" (D-9 dan D-10) diganti satu aturan: bangun ulang master lama yang gagal membuat panel mencatatnya sebagai `quarantined` lagi (desain B; alternatif A: pintu keluar yang dirancang yang mengembalikan peran). Pelaksana bagian D diminta menulis catatan rancangan baca-saja selagi bagian E memegang pohon kode. Implementasinya (bagian G, panel) dan putaran README (bagian F) menyusul sesudah bagian E didorong, lalu satu pemeriksaan ulang kode dan satu pemeriksaan ulang README, dengan batas keras sesudahnya.
- **Alasan:** buku catatan tidak menulis kalimat alasan terpisah; latarnya ada pada temuan pemeriksaan ulang sebelumnya: master lama yang bangun ulangnya gagal tidak bisa lagi dihapus dari panel, dan formulir menyuruh "Periksa data tertinggal" yang justru disembunyikan dan ditolak pada keadaan itu.
- **Biaya bila keliru:** satu perubahan panel lagi di area dengan keadaan paling banyak; bila desain B dipilih, ia juga menutup celah monitor pada keputusan 40.
- **Dipersempit oleh keputusan 43** (desain B tidak dibangun).

#### Keputusan 43

- **Diputuskan:** putaran ini mengambil alternatif A ditambah bendera selesai dari desain B (bagian G): G-1 bendera `QuarantineEnded` dan penanda master lama dihapus saat penyelesaian; G-2 tindakan "Batalkan percobaan bangun ulang" (satu transaksi penyimpanan, tanpa panggilan ke agent); G-3 teks "data tertinggal" yang jujur sesudah penyalinan dimulai; G-4 daftar target; G-5 pekerjaan sertifikat melewati server seperti itu. Desain B TIDAK dibangun di putaran ini; ia menjadi kelompok tugas pertama 5b-2, dengan catatan rancangan sebagai masukan, bersama peringatan "bisa ditulis saat hidup" dan butir `mysqlbinlog`.
- **Alasan:** desain B sebesar sebuah fitur, melonggarkan penjaga yang tercatat sebagai SEC-31, mengubah rekonsiliasi monitor, dan mengonversi baris yang sudah ada; itu butuh spesifikasi, tugas dan pemeriksaan per tugas sendiri, bukan satu putaran perbaikan dengan satu pemeriksaan ulang.
- **Biaya bila keliru:** sampai itu dibangun, keadaan bawaan sesudah bangun ulang yang gagal tetap "replika, gagal" di panel, dan peringatan master lama dari monitor diam sampai operator menekan "Batalkan percobaan bangun ulang" atau memulai bangun ulang berikutnya.
- **Mempersempit keputusan 42.**
- **Perlu perhatian pemilik:** monitor diam pada keadaan itu sampai ada tindakan operator (risiko terbuka).

#### Keputusan 44

- **Diputuskan:** teks saran yang disensor diperbaiki di pembersih teks panel, di dalam bagian G (butir G-6). Panel menyensor isi kutipan ('…') pada pesan galat agent supaya data baris tidak bocor, dan itu membuat saran agent tidak terbaca. Kini (a) daftar frasa panel sendiri yang dikutip (cocok persis) dipertahankan; (b) pasangan akun `'user'@'host'` dipertahankan hanya bila nama pengguna hanya memuat karakter bukti, dan pasangan itu langsung mengikuti persis "akun MySQL " atau "GRANT SHOW DATABASES ON *.* TO ". Teks agent tidak diubah (tidak ada siklus gerbang agent baru). MySQL tidak pernah mencetak kata-kata Indonesia itu, dan pesannya yang mencetak nilai dicocokkan lebih dulu dengan templat sendiri.
- **Alasan:** saran yang tidak bisa dibaca operator adalah kebingungan yang diminta pemilik untuk dihindari, dan ini menyangkut tiga teks yang sudah diperiksa.
- **Biaya bila keliru:** nilai baris yang dicetak MySQL tanpa escape, tepat sesudah salah satu dari dua awalan itu dan berbentuk akun, akan tersimpan tanpa sensor. Daftar frasa dan teks agent adalah dua tempat yang harus berubah bersama.
- **Perlu perhatian pemilik:** ada batas sempit di mana data baris bisa tidak tersensor.

#### Keputusan 45

- **Diputuskan:** bagian F juga mengukur opsi klien mysql `--force` untuk dua urutan perintah manual (bagian GRANT dan bagian "jatuhkan database"), dan memakainya bila ia membuat pernyataan yang memasang kunci ulang berjalan sesudah pernyataan yang gagal (butir F-7). Pemeriksaan `1 1` (nilai `read_only` dan `super_read_only`) menjadi langkah bernomor terakhir di kedua bagian itu apa pun hasilnya.
- **Alasan:** bagian E mengukur bahwa GRANT yang gagal meninggalkan `super_read_only` OFF tanpa apa pun yang memasangnya lagi sampai MySQL dimulai ulang.
- **Biaya bila keliru:** perintah di README berbeda satu opsi klien dari urutan yang dijalankan tes agent.
- **Hasil (catatan status bagian G dan F):** `--force` bekerja untuk bentuk heredoc tetapi tidak untuk `mysql -e`, jadi tak satu pun perintah README memakainya; kedua bagian berakhir dengan pemeriksaan `1 1`.

#### Keputusan 46

- **Diputuskan:** batas keras diterapkan di sisi agent. Temuan N-2, N-4, R-1, R-2 dan R-3 TIDAK diperbaiki di putaran ini dan tidak ada pelaksana agent lagi. Kelimanya diserahkan kepada pemilik sebagai risiko terbuka yang tercatat, masing-masing dengan tawaran memperbaiki sebelum merge, dan masuk ke kelompok tugas pertama 5b-2. Penjelasan singkat: R-1 adalah perlombaan antara pembuktian hak (dibaca dari tabel hak) dan daftar database (memakai hak saat login), dan N-2 adalah catatan GTID sisa (`leftover-gtid`) yang tidak mencatat sumbernya dan tidak pernah dihapus. Penutupan R-1 dan butir "bisa ditulis saat hidup" dirancang bersama (penjaga membaca bendera baca-saja tiap tick dan penyelesaian memeriksa silang daftar dengan direktori data); N-2: hapus `leftover-gtid` atau beri cap sumber.
- **Alasan:** tiap butir butuh perubahan agent dengan siklus gerbang 45 menit dan pemeriksaan sendiri; putaran sudah punya tujuh bagian; R-1 butuh penyerang yang sudah memegang SUPER di master lama (yang bisa membuatnya bisa ditulis secara langsung, risiko 1b yang tercatat); N-2 butuh penyusupan sebelum upgrade.
- **Biaya bila keliru:** R-1: penyerang itu bisa mengakhiri karantina dengan database lama masih di server (data basi pada replika, tidak ada data yang hilang). N-2: pada server yang di-upgrade dari agent lama yang panelnya sudah disusupi, binary log yang dipertahankan bisa diekstrak sesudah root menyematkan sumber.
- **Perlu perhatian pemilik:** lima risiko terbuka yang diserahkan kepada pemilik. Keputusan 48 tidak mencabut ini: butir-butir ini tetap terbuka.

#### Keputusan 47

- **Diputuskan:** temuan bahwa pada bangun ulang penuh sebuah database yang dibuat aplikasi selama pemuatan tidak ditandai TIDAK diperbaiki di putaran ini (batas keras, sisi agent). Ia dicatat sebagai risiko terbuka bagi pemilik dan bagi kelompok tugas pertama 5b-2, dengan dua penutupan untuk dirancang di sana: membandingkan daftar database server sesudah pemuatan dengan daftar milik sumber, dan menjaga `read_only` tetap ON selama pemuatan supaya hanya akun pemuat yang bisa menulis.
- **Alasan:** batas keras sisi agent. Kasusnya membutuhkan aplikasi yang masih menulis ke master lama sesudah operator mengonfirmasi pengalihan dan menyetujui bangun ulang di server; jendela yang sama membiarkan aplikasi menulis ke tabel yang dimuat, yang tidak bisa dideteksi oleh daftar apa pun.
- **Biaya bila keliru:** replika hasil bangun ulang menyimpan sebuah database (atau baris) yang tidak ada di master baru, tanpa terlihat; tidak ada data di master baru yang hilang.
- **Perlu perhatian pemilik:** risiko terbuka yang menyangkut data yang diam-diam berbeda.

#### Keputusan 48

- **Diputuskan:** satu putaran terakhir (bagian H, hanya panel dan README) menimpa batas keras keputusan 41, 43 dan 46. Isinya: perbaikan pemeriksaan cadangan di README, perbaikan eksekusi kedua yang bisa mengosongkan cadangan, aturan tiga keadaan untuk "data tertinggal" di panel (belum mulai, ada bukti perubahan, tidak pasti; pada keadaan tidak pasti "Periksa" dan "Amankan" tetap ditawarkan dengan peringatan), dan hasil `1 0` yang tidak lagi dianggap "server salah". Bagian H diberikan kepada pelaksana yang sama, lalu dua pemeriksaan ulang (kode dan README). Sesudah itu putaran BERAKHIR apa pun temuannya: semua butir yang masih terbuka dilaporkan kepada pemilik tanpa diperbaiki, dengan tingkat keparahan dan usul perbaikan, sebelum pertanyaan merge.
- **Alasan:** dua pemeriksaan ulang terakhir menemukan tiga cara kehilangan data yang diperkenalkan atau disetujui putaran ini sendiri: pemeriksaan cadangan di README yang meloloskan dump yang terpotong sebelum langkah membuang satu-satunya salinan (tingkat Kritis), eksekusi kedua yang mengosongkan cadangan, dan aturan panel yang menolak menyelamatkan transaksi yang masih ada di disk. Aturan pemilik adalah tidak ada risiko kehilangan data yang dibiarkan; menggabung cabang dengan langkah README yang diketahui menghancurkan satu-satunya salinan bukan hasil yang akan dipilih pemilik; dan tiap butir punya koreksi kecil yang konkret (pemeriksa memberi teks siap tempel).
- **Biaya bila keliru:** satu siklus lagi (sekitar 1,5 jam). Pemilik dua kali diberi tahu tidak ada putaran perbaikan lagi, dan keputusan ini melanggar pernyataan itu. Prosedur manual README dan keadaan "bangun ulang gagal" belum menyatu dalam siklus periksa-perbaiki, jadi pemeriksaan ulang berikutnya bisa menemukan masalah lagi; jawaban yang bersifat struktural (tindakan panel sebagai ganti SQL manual, desain B, penjaga yang membaca bendera tiap tick) adalah pekerjaan 5b-2.
- **Menimpa batas keras keputusan 41, 43 dan 46** untuk satu putaran ini; butir-butir terbuka keputusan 46 tidak berubah.
- **Perlu perhatian pemilik:** keputusan ini menyangkut kehilangan data pada prosedur manual README. Sesudah putaran H, dua pemeriksaan ulang terakhir tidak menemukan penghalang merge; yang masih terbuka dan dilaporkan kepada pemilik tanpa diperbaiki ada di bawah.

#### Yang masih terbuka sesudah putaran perbaikan berakhir (dari catatan terakhir buku catatan)

- README: satu kalimat yang salah (I-H1, tingkat Important) dan delapan catatan kecil (m-H1 sampai m-H8) dan satu catatan lama yang baru diperbaiki sebagian (m-2), semuanya dari pemeriksaan ulang README bagian H.
- Kode: lima catatan kecil (N-H1 sampai N-H5) dan teks peringatan monitor `master_start_late` yang masih menyuruh "Periksa data tertinggal" sesudah penyalinan dimulai (N-G3).
- Risiko tercatat dari keputusan 30, 38, 40, 46 dan 47.

## Bagian 3. Teks baru yang bisa dilihat operator

Cara membaca bagian ini:

- Setiap teks dikutip karakter demi karakter dari kode atau templat pada titik cabang `8b0389d`, dan berkasnya disebut. Teks yang tercantum di laporan atau di rencana tetapi sudah tidak ada atau sudah diubah kata-katanya hanya dikutip dalam bentuk yang ada sekarang; yang tidak ada lagi disebut di baris penutup bagian ini.
- `%s`, `%v`, `%d` adalah isian dari Go: `%s` diisi teks (nama server, `host:port`, nama perintah), `%v` diisi sebab galat, `%d` diisi angka. `{{…}}` adalah isian templat halaman panel; artinya dijelaskan di bawah tiap teks.
- Dalam templat, perintah kadang dibungkus tag HTML `<code>`. Tag itu tidak ditulis di kutipan; kata-katanya persis sama.
- Spasi sejajar di petunjuk penggunaan hanya perataan.

### 3.1 Di server, lewat perintah `dbpanel agent`

Semua teks di bagian ini ada di `internal/agent/cli.go`, kecuali bila disebut lain. Operator melihatnya di terminal saat root menjalankan perintahnya di server.

#### Petunjuk penggunaan (`dbpanel agent` tanpa argumen atau dengan argumen salah)

Tampil sebagai bantuan (`dbpanel agent help`, `-h`, `--help`) dan saat argumen salah. Tiga baris ini baru atau berubah.

> ubah peran yang dipin; --ya: tanpa pertanyaan pada server yang dikarantina (quarantined)

> setujui bangun ulang server yang dikarantina; kode dari form "Bangun ulang sebagai replika"

> hapus persetujuan dan permintaan bangun ulang

Berkas: `internal/agent/cli.go` (konstanta `usage`).

#### `dbpanel agent set-role`: ringkasan keadaan sebelum bertanya

Tampil bila peran sekarang `quarantined`, atau pin tidak bisa dibaca, dan perannya diubah ke yang lain tanpa `--ya`. Salah satu dari tiga kalimat ini tampil dulu.

> Server ini dikarantina (quarantined) mengikuti sumber %s.

`%s` diisi `host:port` master baru.

> Server ini dikarantina (quarantined) tanpa sumber yang dipin.

> Pin server ini tidak bisa dibaca (%v); server ini mungkin dikarantina (quarantined).

`%v` diisi sebab pin tidak bisa dibaca.

Berkas: `internal/agent/cli.go`.

#### `dbpanel agent set-role`: apa yang dilepas operator

Tampil sesudah kalimat di atas, sesuai peran yang dituju (`master`, `unassigned` atau `replica`).

Ke `master`:

> Setelah perintah ini:
> - kunci start (start lock) dilepas sekarang, jadi MySQL tidak lagi dipaksa baca-saja (read-only) saat menyala;
> - server ini dipin sebagai master dan bisa dibuka untuk ditulis. Bila master baru masih dipakai aplikasi, Anda akan punya dua master yang bisa ditulis.

Ke `unassigned`:

> Setelah perintah ini:
> - agent melepas kunci start (start lock) setelah MySQL menyala dengan baca-saja (read-only) tersimpan;
> - server ini tanpa peran: panel boleh menyiapkannya ulang, termasuk membuang semua datanya, tanpa persetujuan di server ini.

Ke `replica`:

> Setelah perintah ini:
> - agent melepas kunci start (start lock) setelah MySQL menyala dengan baca-saja (read-only) tersimpan;
> - server ini dipin sebagai replika: panel boleh membuang datanya dan menyalin ulang dari sumber yang dipin tanpa persetujuan di server ini.

Berkas: `internal/agent/cli.go`.

#### `dbpanel agent set-role`: pertanyaan dan jawabannya

> Ketik ya untuk melanjutkan, atau apa saja untuk membatalkan: 

Tampil sebagai pertanyaan. Jawaban selain `ya` menghasilkan:

> Dibatalkan; peran tidak diubah.

Bila peran berubah selama operator membaca layar (kode keluar 1, tidak ada yang berubah):

> Peran server ini berubah selama Anda membaca; jalankan perintah ini lagi.

Berkas: `internal/agent/cli.go`.

#### `dbpanel agent set-role master`: hasil pelepasan kunci start

Tampil saat perannya diubah ke `master`; satu dari kalimat ini sesuai hasilnya.

> Kunci start dilepas: baris init_file dan berkas kuncinya dihapus dari konfigurasi MySQL.

> Kunci start tidak ditemukan; tidak ada yang dilepas.

> MySQL tidak ditemukan di server ini; tidak ada kunci start yang perlu dilepas.

> kunci start tidak bisa diperiksa (%v); peran tidak diubah. Bila agent tidak bisa dijalankan, lepas dengan tangan: lihat README, bagian "Melepas kunci start dengan tangan".

`%v` diisi sebab pemeriksaan gagal.

Bila baris `init_file` sudah terhapus tetapi berkas kuncinya tidak bisa dihapus:

> Baris init_file sudah dihapus dari konfigurasi MySQL, tetapi berkas kunci start tidak bisa dihapus (%v); peran tidak diubah. Jalankan perintah ini lagi.

Bila kunci sudah dilepas tetapi penulisan peran gagal:

> Kunci start sudah dilepas, tetapi peran belum diubah. Jalankan perintah ini lagi; sampai itu MySQL tidak dipaksa baca-saja (read-only) saat menyala.

Berkas: `internal/agent/cli.go`.

#### `dbpanel agent set-role`: langkah berikutnya

Tampil setelah perannya berhasil diubah: ke `master` (kalimat pertama), atau ke `unassigned` dari server yang dikarantina atau pin yang tidak terbaca (kalimat kedua).

> Langkah berikutnya: bila MySQL di server ini masih baca-saja (read-only) dan server ini memang akan dipakai aplikasi sebagai master, pastikan master lain tidak lagi dipakai, lalu buka dengan: sudo dbpanel agent unlock-master

> Langkah berikutnya: bila server ini sudah dihapus dari panel, tambahkan lagi lewat Tambah server, lalu jalankan Siapkan master atau Buat replika. Perhatian: server ini tidak lagi dikarantina; agent tidak lagi menjaga MySQL-nya tetap baca-saja (read-only).

Berkas: `internal/agent/cli.go`.

#### `dbpanel agent approve <kode>`: layar persetujuan

Tampil saat root menjalankan perintah dengan kode yang benar, sebelum bertanya.

> Persetujuan bangun ulang (rebuild) server ini
>
> Server ini  : %s
> Peran       : dikarantina (quarantined)
> Sumber data : %s
> Alamat IP   : %s

Isian berurutan: nama komputer server ini ("(tidak diketahui)" bila tidak terbaca), `host:port` sumber, dan alamat IP sumber itu.

> Bila disetujui, SEMUA data MySQL di server ini dibuang dan diganti salinan dari sumber di atas. Data yang dibuang tidak bisa dikembalikan.
> Selama persetujuan berlaku (24 jam), panel juga boleh mengubah pengaturan replikasi, akun replikasi, pengaturan dan sertifikat MySQL di server ini, dan memulai ulang MySQL.
> Pastikan nama dan alamat IP di atas memang master baru Anda, yaitu server yang sekarang dipakai aplikasi.
> Bila promote tadi promote darurat, jalankan dulu "Periksa data tertinggal" (dan "Amankan data tertinggal" bila ada) di halaman server ini di panel.
> Persetujuan berlaku 24 jam, hanya untuk sumber di atas.
>
> Ketik ya untuk menyetujui, atau apa saja untuk membatalkan: 

Berkas: `internal/agent/cli.go`.

#### `dbpanel agent approve <kode>`: hasil

Jawaban selain `ya`:

> Dibatalkan; tidak ada yang disetujui.

Bila permintaan berubah selama root membaca layar:

> Permintaan berubah selama Anda membaca; jalankan perintah ini lagi.

Bila berhasil:

> Disetujui sampai %s. Kembali ke panel dan klik Periksa lagi. Untuk membatalkan: sudo dbpanel agent approve batal

`%s` diisi waktu berakhir, dalam jam server, bentuk `2006-01-02 15:04 MST`.

Bila catatan ke log sistem gagal ditulis (persetujuan tetap berlaku):

> Peringatan: catatan ke log sistem gagal ditulis (%v).

Berkas: `internal/agent/cli.go`.

#### `dbpanel agent approve <kode>`: penolakan sebelum bertanya

Semuanya tampil di terminal dan tidak menulis persetujuan.

> Pin server ini tidak bisa dibaca (%v); persetujuan tidak ditulis.

> Server ini tidak dikarantina (peran: %s); persetujuan bangun ulang tidak diperlukan.

`%s` diisi peran server ini sekarang.

> Server ini dikarantina tanpa sumber yang dipin; pin master barunya dulu: sudo dbpanel agent set-source <host:port>

> Tidak ada permintaan persetujuan yang berlaku di server ini. Buka form "Bangun ulang sebagai replika" di panel, klik Periksa lagi, lalu jalankan perintah yang ditampilkan.

> Kode salah. Salin perintah dari form "Bangun ulang sebagai replika" di panel.

> Alamat untuk nama sumber %s tidak ditemukan (DNS: %v); persetujuan tidak ditulis.

`%s` diisi nama sumber, `%v` diisi sebab pencarian DNS gagal. Berkas: `internal/agent/cli.go`.

Dua penolakan berikut dibuat di `internal/agent/drivers/mysql/rebuild_approval.go` dan dicetak oleh `cli.go`.

Nama sumber mengarah ke lebih dari satu alamat:

> Nama sumber %s mengarah ke lebih dari satu alamat (%s). Persetujuan hanya bisa diberikan bila nama itu mengarah ke satu alamat. Tetapkan satu alamat untuk nama itu di server ini (misalnya di /etc/hosts), lalu jalankan perintah approve ini lagi. Persetujuan tidak ditulis.

Nama sumber sekarang mengarah ke alamat lain daripada yang dicatat saat karantina:

> Nama sumber %s sekarang mengarah ke %s, bukan ke alamat yang dicatat saat karantina (%s). Bila alamat baru itu memang master baru, jalankan: sudo dbpanel agent set-source %s, lalu jalankan perintah approve ini lagi. Persetujuan tidak ditulis.

Isian berurutan: nama sumber, alamat baru, alamat yang tercatat, nama sumber lagi.

#### Teks yang muncul sesudah persetujuan gagal ditulis

Tampil bila ada langkah gagal sesudah persetujuan mulai ditulis (keputusan 5). Persetujuan dibatalkan sebelum teks ini tampil, kecuali teks kedua.

> Gagal menulis persetujuan: %v

> Gagal menghapus permintaan: %v; persetujuan dibatalkan. Jalankan perintah ini lagi.

> Berkas persetujuan tidak bisa dihapus (%v), jadi persetujuan mungkin tetap berlaku. Batalkan dengan: sudo dbpanel agent approve batal

Berkas: `internal/agent/cli.go`.

#### `dbpanel agent approve batal`

> Persetujuan dan permintaan bangun ulang di server ini dihapus.

> Gagal menghapus: %v

Berkas: `internal/agent/cli.go`.

### 3.2 Penolakan dari agent yang tampil di panel (halaman pekerjaan dan formulir)

Teks di bagian ini dibuat oleh agent dan dikirim ke panel sebagai jawaban atas sebuah perintah. Operator melihatnya sebagai galat pada langkah pekerjaan di halaman pekerjaan atau pada formulir bangun ulang, biasanya di bawah label "Pesan dari agent … (bukan dari panel):" (lihat 3.4). Pada halaman pekerjaan, panel mengganti isi kutipan bertanda petik dengan `'…'` kecuali beberapa frasa dan nama akun yang memang dikenal (keputusan 44).

#### Pelepasan peran ditolak

Tampil bila panel mencoba melepas peran server yang dikarantina (misalnya lewat "Hapus server").

> server ini dikarantina (quarantined); perintah mysql.releaseRole ditolak. Karantina hanya lepas lewat bangun ulang yang selesai, atau dengan perintah di server ini: sudo dbpanel agent set-role unassigned (atau sudo dbpanel agent set-role master bila server ini akan menjadi master lagi)

Berkas: `internal/agent/drivers/mysql/pins.go`.

#### Perintah butuh persetujuan di server

Tampil bila panel menjalankan perintah yang membuang data pada server yang dikarantina tanpa persetujuan yang berlaku.

> server ini dikarantina (quarantined) mengikuti sumber %s; perintah %s butuh persetujuan root di server ini. Jalankan di server ini: sudo dbpanel agent approve %s (kode berlaku %d menit lagi)

Isian berurutan: `host:port` sumber, nama perintah, kode enam angka, sisa menit.

> server ini dikarantina (quarantined); perintah %s butuh persetujuan root di server ini, tetapi permintaan persetujuan tidak bisa dibuat: %v

`%v` diisi sebab, misalnya "server ini tidak punya sumber replikasi yang dipin" atau "server ini tidak dikarantina (peran: %s)". Berkas: `internal/agent/drivers/mysql/strict.go`; sebab-sebabnya di `rebuild_approval.go`.

#### Karantina tanpa sumber yang dipin

Tampil bila perintah bangun ulang, `mysql.leftover` atau `mysql.salvage` dikirim ke server yang dikarantina tanpa pin sumber.

> server ini dipin sebagai quarantined tanpa sumber yang dipin; perintah %s ditolak. Pin master barunya dulu di server ini: sudo dbpanel agent set-source <host:port>

`%s` diisi nama perintah. Berkas: `internal/agent/drivers/mysql/pins.go`.

#### Berkas pin tidak bisa dibaca

Tampil bila berkas pin rusak: semua perintah kecuali yang tergolong "boleh" ditolak, termasuk pertanyaan formulir bangun ulang.

> pin peran/sumber agent tidak bisa dipakai, perintah %s ditolak: %v

> pin peran/sumber agent tidak bisa dipakai, perintah mysql.rebuildApproval ditolak: %v

Berkas: `internal/agent/drivers/mysql/pins.go` dan `strict.go`.

#### Menyambungkan MySQL ditolak (akun dan alamat)

Tampil di panel saat "Hubungkan MySQL" (atau pemeriksaan agent) menolak. Tiga teks pertama baru; yang keempat berubah kata-katanya untuk server yang dikarantina.

> MySQL yang disambungkan harus berada di server ini (soket, 127.0.0.1, atau alamat server ini sendiri); %s bukan alamat server ini.

`%s` diisi host yang diberikan.

> alamat MySQL %s tidak bisa dicari (DNS): %v. MySQL yang disambungkan harus berada di server ini (soket, 127.0.0.1, atau alamat server ini sendiri).

`%v` diisi sebab; "tidak ada alamat" bila nama itu tidak punya alamat. Berkas: `internal/agent/drivers/mysql/connect.go`.

> server ini dikarantina (quarantined): akun %s belum punya semua hak yang dibutuhkan; jalankan perintah GRANT berikut sebagai root lalu periksa ulang: %s

Isian: nama akun, dan perintah GRANT yang kurang dipisah titik koma. Berkas: `internal/agent/drivers/mysql/connect.go`.

#### Langkah "Atur replikasi" menolak mengakhiri karantina

Tampil di halaman pekerjaan bila bangun ulang sampai ke langkah pengaturan replikasi tetapi server masih punya database yang tidak disalin dari sumber (kode A6):

> server ini masih punya database yang tidak disalin dari sumber %s (%s); karantina tidak diakhiri. Ulangi Bangun ulang sebagai replika dari panel. Bila panel lalu menolak karena database itu tidak ada di master baru, lihat README, bagian "Database yang tidak ada di master baru".

Isian: `host:port` sumber dan daftar nama database (dipotong dengan "…" bila panjang). Berkas: `internal/agent/drivers/mysql/rebuild_load.go`.

Bila daftar database tidak bisa dibaca:

> sumber replikasi sudah diatur, tetapi daftar database di server ini tidak bisa dibaca; karantina tidak diakhiri: %w

`%w` diisi sebab galat. Berkas: `internal/agent/drivers/mysql/quarantine.go`.

Bila agent tidak punya catatan salinan data (misalnya agent dimulai ulang di tengah jalan):

> sumber replikasi sudah diatur, tetapi server ini tetap dikarantina: agent tidak punya catatan salinan data dari bangun ulang ini (misalnya karena agent dimulai ulang di tengah jalan). Mulai lagi Bangun ulang sebagai replika dari panel; tombol "Ulangi dari tahap ini" pada pekerjaan ini tidak cukup, karena salinan data harus diulang.

Berkas: `internal/agent/drivers/mysql/quarantine.go`.

#### Daftar database tidak bisa dipastikan lengkap

Tampil bila akun admin yang dipakai agent tidak punya hak `SHOW DATABASES` pada server yang dikarantina:

> daftar database di server ini tidak bisa dipastikan lengkap: akun MySQL %s yang dipakai agent tidak punya hak SHOW DATABASES. Beri hak itu sebagai root MySQL di server ini: GRANT SHOW DATABASES ON *.* TO %s; Server ini baca-saja, jadi perintah itu harus dijalankan dengan langkah di README, bagian "Memberi hak SHOW DATABASES di server yang dikarantina". Sesudah itu tekan "Ulangi dari tahap ini" pada pekerjaan di panel.

`%s` (dua kali) diisi akun dalam bentuk `'pengguna'@'host'`.

Bila nama akunnya memuat karakter yang tidak didukung:

> daftar database di server ini tidak bisa dipastikan lengkap: nama akun MySQL yang dipakai agent memuat karakter yang tidak didukung pemeriksaan ini. Hubungkan MySQL di panel dengan akun lain yang namanya hanya huruf, angka, garis bawah, titik, atau tanda hubung (misalnya dbpanel_admin). Sesudah itu tekan "Ulangi dari tahap ini" pada pekerjaan di panel.

Berkas: `internal/agent/drivers/mysql/rebuild_load.go`.

### 3.3 Halaman server

#### Dialog "Hapus server" pada master lama yang dikarantina

Tampil di jendela "Hapus server", di bawah "Perhatikan sebelum menghapus:". Bila laporan terakhir menyebut kunci start "Terkunci":

> Server ini tetap dikarantina (quarantined) setelah dihapus dari panel: MySQL-nya tetap baca-saja (read-only) dan kunci start (start lock) tetap terpasang. Untuk memakai server ini lagi, jalankan di server itu: sudo dbpanel agent set-role unassigned, atau sudo dbpanel agent set-role master bila server ini akan menjadi master lagi.

Selain itu (belum ada laporan, atau kunci "belum terjamin"):

> Server ini tetap dikarantina (quarantined) setelah dihapus dari panel, tetapi kunci start (start lock)-nya tidak terjamin, jadi MySQL-nya belum tentu baca-saja (read-only) setelah menyala ulang. Untuk memakai server ini lagi, jalankan di server itu: sudo dbpanel agent set-role unassigned, atau sudo dbpanel agent set-role master bila server ini akan menjadi master lagi.

Berkas: `internal/panel/web/handlers_oldmaster.go` (fungsi `deleteWarnings`).

#### Kotak "Master lama": data tertinggal sesudah penyalinan dimulai

Tampil di bagian "data tertinggal" pada halaman server master lama bila bangun ulang sebelumnya sudah mulai menyalin data. Teks yang sama juga menggantikan saran pada penjelasan status server.

> Bangun ulang sebelumnya sudah mulai menyalin data ke server ini, jadi data lamanya sudah dihapus atau diganti; data tertinggal tidak bisa diperiksa atau diamankan lagi lewat panel.

Berkas: `internal/panel/orchestrator/oldmaster.go` (`CopyStartedText`).

#### Kotak "Master lama": percobaan bangun ulang yang gagal masih tercatat

Tampil di tempat tombol "Periksa data tertinggal" bila ada percobaan bangun ulang yang gagal.

> Data tertinggal belum bisa diperiksa selama percobaan bangun ulang yang gagal masih tercatat. Untuk memeriksa atau mengamankannya, tekan dulu "Batalkan percobaan bangun ulang" di halaman server ini.

Berkas: `internal/panel/web/handlers_oldmaster.go` (`msgAttemptPending`).

#### Kotak "Master lama": penyalinan mungkin sudah mulai

Tampil sebagai banner peringatan di atas tombol "Periksa data tertinggal" bila bangun ulang berhenti di tahap "Salin data" dan tidak ada bukti penyalinan sempat mulai.

> Bangun ulang sebelumnya berhenti di tahap Salin data. Bila penyalinan sempat mulai, catatan transaksi lama di server ini sudah dihapus, dan hasil "tidak ada data tertinggal" dari pemeriksaan ini belum tentu benar.

Berkas: `internal/panel/web/handlers_oldmaster.go` (`msgCopyUncertain`).

#### Tombol "Batalkan percobaan bangun ulang"

Tampil di halaman server (dan halaman replika, 3.5) bila master lama tertinggal dalam keadaan bangun ulang yang gagal. Tombolnya:

> Batalkan percobaan bangun ulang

Kalimat di bawah tombol, dan bagian dari kalimat konfirmasi:

> Server ini kembali tercatat sebagai master lama yang dikarantina; data di server ini tidak diubah. Sesudah itu Hapus server dan Bangun ulang sebagai replika tersedia lagi, dan tombol "Ulangi dari tahap ini" pada pekerjaan yang gagal tidak bisa dipakai lagi.

Kalimat konfirmasi lengkap (isian: nama server, lalu kalimat di atas):

> Batalkan percobaan bangun ulang di {{.Name}}. {{cancelAttemptText}}

Setelah tombol ditekan dan berhasil, halaman server menampilkan:

> Percobaan bangun ulang dibatalkan; server ini kembali tercatat sebagai master lama yang dikarantina.

Bila server bukan master lama dengan percobaan yang gagal, tampil sebagai pesan galat:

> server ini bukan master lama dengan percobaan bangun ulang yang gagal

Berkas: `web/templates/_ui.html`; kalimatnya di `internal/panel/web/handlers_oldmaster.go`; teks galat di `internal/panel/orchestrator/controls.go`.

#### Memasang sertifikat MySQL pada server yang dikarantina

Tampil bila operator menekan "Pasang sertifikat MySQL" pada server yang dikarantina, atau mengulang pekerjaan sertifikatnya.

> Server ini dikarantina (quarantined); sertifikat MySQL-nya dipasang lagi saat dibangun ulang sebagai replika.

Berkas: `internal/panel/orchestrator/tls.go`.

### 3.4 Halaman pekerjaan

#### Label untuk kata-kata agent

Tampil di depan setiap kalimat agent yang ditampilkan panel pada halaman pekerjaan atau formulir bangun ulang, supaya tidak terbaca sebagai kata panel (keputusan 20). Kalimatnya dipotong dan dijadikan satu baris.

> Pesan dari agent %s (bukan dari panel): %s

Isian: nama server, lalu kalimat agent. Berkas: `internal/panel/orchestrator/approval.go`.

#### Langkah gagal karena butuh persetujuan di server (dengan kode)

Tampil sebagai galat pada langkah pekerjaan bila agent menjawab butuh persetujuan di tengah pekerjaan dan kodenya terbaca:

> Server %s dikarantina (quarantined): langkah ini butuh persetujuan di server itu. Jalankan di server itu sebagai root: sudo dbpanel agent approve %s, lalu klik Ulangi.

Isian: nama server, kode enam angka. Bila kodenya tidak bisa dibaca:

> Server %s dikarantina (quarantined): langkah ini butuh persetujuan di server itu. Buka form Bangun ulang sebagai replika, klik Periksa lagi, jalankan perintah yang ditampilkan di server itu, lalu klik Ulangi.

Berkas: `internal/panel/orchestrator/approval.go`.

#### Bangun ulang ditolak sebelum dimulai (atau saat diulang atau disetujui)

Tampil bila operator memulai, mengulang atau menyetujui pekerjaan bangun ulang yang belum disetujui di server.

> Bangun ulang %s butuh persetujuan di server itu. Jalankan di server itu sebagai root: sudo dbpanel agent approve %s, lalu coba lagi.

Isian: nama server, kode enam angka.

> Bangun ulang %s butuh persetujuan di server itu, tetapi kodenya tidak bisa dibaca dari agent. Buka form Bangun ulang sebagai replika dan klik Periksa lagi.

> Agent perlu dimutakhirkan: agent di %s belum mengenal persetujuan di server, jadi bangun ulang ditolak. Klik Pasang ulang agent di halaman server itu, jalankan perintahnya, lalu coba lagi.

> parameter pekerjaan ini tidak bisa dibaca, jadi persetujuan bangun ulang di server tidak bisa diperiksa; pekerjaan ini tidak bisa diulang atau disetujui

Berkas: `internal/panel/orchestrator/approval.go`.

#### Catatan pada log pekerjaan dan galat penyimpanan

Tampil sebagai baris log pekerjaan bila karantina master lama berakhir pada langkah pengaturan replikasi:

> Karantina master lama berakhir: agent kini menyimpan peran replika.

Tampil sebagai galat pekerjaan bila keadaan pekerjaan tidak bisa disimpan sesudah itu (`%w` diisi sebab):

> simpan keadaan pekerjaan: %w

Berkas: `internal/panel/orchestrator/create_replica.go`.

#### Kalimat di bawah tombol "Ulangi dari tahap ini"

Tampil di halaman pekerjaan yang gagal bila pekerjaan itu bangun ulang master lama. `{{.}}` adalah tautan ke formulir bangun ulang.

> Bila "Ulangi dari tahap ini" tidak menolong, mulai lagi Bangun ulang sebagai replika.

Berkas: `web/templates/job_detail.html`.

### 3.5 Halaman replika

Tampil di halaman replika sebuah master lama yang tertinggal dalam keadaan bangun ulang yang gagal. `{{.}}` adalah tautan ke formulir bangun ulang.

> Server ini master lama yang masih dikarantina. Pakai Bangun ulang sebagai replika.

Berkas: `web/templates/replica_detail.html`. Di halaman yang sama ada tombol "Batalkan percobaan bangun ulang" beserta kalimatnya (lihat 3.3).

Pada keadaan itu, "Resync ulang", "Lepas replika" dan "Buat replika" (non-bangun-ulang) dengan server itu sebagai target ditolak:

> server ini master lama yang masih dikarantina; pakai Bangun ulang sebagai replika

Berkas: `internal/panel/orchestrator/approval.go`.

### 3.6 Formulir "Bangun ulang sebagai replika"

Kotak "Persetujuan di server" muncul di atas formulir. Semua teksnya ada di `web/templates/replica_new.html` kecuali disebut lain.

#### Judul dan tombol

> Persetujuan di server

> Periksa lagi

#### Kotak bila belum bertanya ke agent

Tampil bila formulir dibuka lewat tautan dari situs atau host lain (agent tidak ditanya; keputusan 20).

> Klik Periksa lagi untuk melihat persetujuan di server.

#### Kotak bila belum disetujui

> Belum disetujui. Jalankan perintah ini di {{$rb.Server.Name}} sebagai root, lalu klik Periksa lagi:

> sudo dbpanel agent approve {{.Code}}

> Kode berlaku {{$rb.ApprovalMinutes}} menit lagi.

Isian: nama server, kode enam angka, sisa menit.

> Bangun ulang membuang semua data MySQL di server ini. Persetujuan di server memastikan orang yang memegang server itu melihat dari mana data penggantinya diambil, sehingga panel saja tidak bisa melakukannya.

#### Kotak bila sudah disetujui

> Sudah disetujui di {{$rb.Server.Name}} untuk sumber {{.Source}} ({{.Addr}}); berlaku {{$rb.ApprovalHours}} jam {{$rb.ApprovalHourMinutes}} menit lagi. Untuk membatalkan, jalankan di server itu: sudo dbpanel agent approve batal

Isian: nama server, `host:port` sumber, alamat IP, sisa jam dan menit.

#### Kotak bila persetujuan yang ada tidak berlaku lagi

Tampil di atas kotak "belum disetujui". `{{.}}` diisi salah satu alasan di bawah.

> Persetujuan yang ada di {{$rb.Server.Name}} tidak berlaku lagi: {{.}}.

Alasan, dalam kata-kata panel (`internal/panel/orchestrator/approval.go`):

> masa berlakunya sudah habis

> sumber yang dipin di server itu sudah berubah

> nama sumbernya sekarang mengarah ke alamat lain

> alamat untuk nama sumbernya tidak ditemukan (DNS)

> waktu persetujuannya di masa depan; periksa jam server itu

> masa berlakunya tidak wajar

> server itu tidak lagi dikarantina

> berkas persetujuannya tidak bisa dipakai

Untuk alasan yang tidak dikenal panel:

> alasannya tidak dikenali panel ini

#### Kotak bila tidak diperlukan

> Agent {{$rb.Server.Name}} tidak melaporkan server ini dikarantina, jadi persetujuan di server tidak diperlukan.

#### Kotak bila persetujuan tidak bisa diperiksa

Berkas: `internal/panel/web/handlers_oldmaster.go`.

> Persetujuan di server tidak bisa diperiksa: agent %s offline.

> Agent perlu dimutakhirkan: agent di %s belum mengenal persetujuan di server. Klik Pasang ulang agent di halaman server itu, jalankan perintahnya, lalu klik Periksa lagi.

> Persetujuan di server tidak bisa diperiksa: agent %s menolak permintaannya.

Teks terakhir diikuti kutipan berlabel "Pesan dari agent …" (3.4).

> Persetujuan di server tidak bisa diperiksa sekarang; klik Periksa lagi.

#### Petunjuk di sebelah tombol yang dimatikan

Tampil di atas tombol "Bangun ulang sebagai replika" selama belum disetujui, yang membuat tombol itu tidak bisa diklik.

> Tombol ini aktif setelah bangun ulang disetujui di server; lihat kotak Persetujuan di server di atas.

#### Peringatan data tertinggal pada formulir (dan pada kalimat konfirmasi)

Peringatan formulir juga ditambahkan ke kalimat konfirmasi tombol "Bangun ulang sebagai replika" (kalimat konfirmasinya sendiri sudah ada sebelum rencana ini). Tiga teks baru; kalimat 2 dan 3 sama dengan teks di halaman server (3.3).

> Bangun ulang sebelumnya sudah mulai menyalin data ke server ini; transaksi yang tidak sampai ke master baru tidak bisa lagi diperiksa atau diamankan lewat panel.

Berkas: `internal/panel/web/handlers_oldmaster.go` (`msgCopyStartedForm`); dua teks lainnya adalah `msgAttemptPending` dan `msgCopyUncertain` (3.3).

### 3.7 Peringatan dan keterangan status

#### Alasan kunci start "belum terjamin"

Tampil di halaman server sebagai "Kunci start: belum terjamin: …" dan di dalam kurung pada peringatan `master_start_unguarded`. Teks berikut baru; yang diawali "konfigurasi efektif MySQL tidak bisa dibaca:" memuat sebabnya di belakang titik dua.

> opsi %s membuat MySQL menolak menyala; kunci start tidak terjamin

`%s` diisi opsi persis seperti tertulis di konfigurasi (awalan `maximum-` pada `init_file`).

> konfigurasi efektif MySQL tidak bisa dibaca: akun layanan MySQL %s tidak punya direktori home; ~/.my.cnf tidak bisa diperiksa

> konfigurasi efektif MySQL tidak bisa dibaca: direktori home akun layanan MySQL %s (%s) tidak terlihat oleh agent (ProtectHome); ~/.my.cnf di sana tidak bisa diperiksa

> konfigurasi efektif MySQL tidak bisa dibaca: akun layanan MySQL %s tidak ditemukan: %v

> konfigurasi efektif MySQL tidak bisa dibaca: id akun layanan MySQL %s tidak bisa dibaca: %v

> konfigurasi efektif MySQL tidak bisa dibaca: opsi user di ~/.my.cnf akun layanan MySQL berbeda (%s, bukan %s); kunci start tidak bisa dipastikan

> konfigurasi efektif MySQL tidak bisa dibaca: keluaran --print-defaults terlalu besar

Berkas: `internal/agent/drivers/mysql/startlock.go`; tampil di panel lewat `web/templates/server_detail.html` dan `internal/panel/monitor/alerts.go`.

#### Teks peringatan start yang kata-katanya tidak diubah

Rencana ini tidak menambah kata baru pada peringatan master lama; yang diubah adalah kapan peringatan itu dibuka dan ditutup (Tugas 9). Tiga teks ini dibuka dari laporan agent:

> master lama sempat bisa ditulis saat menyala; jalankan Periksa data tertinggal

> kunci start tidak dipakai MySQL; periksa opsi layanan

> MySQL master lama gagal menyala karena berkas kunci tidak terbaca

Teks pertama masih menyuruh "Periksa data tertinggal" walau, sesudah penyalinan bangun ulang dimulai, panel tidak lagi bisa memeriksanya (catatan N-G3, masih terbuka). Berkas: `internal/panel/monitor/alerts.go`.

#### Tindakan di daftar audit

Tampil di halaman Audit sebagai nama tindakan:

> Batalkan percobaan bangun ulang

Berkas: `internal/panel/web/audit_summary.go`.

#### Teks yang tidak ada lagi

Baris penutup (teks yang disebut di laporan atau rencana tetapi tidak ada atau sudah berubah pada `8b0389d`): (1) baris layar `approve` "(nama sumber juga mengarah ke: …)" sudah dihapus (nama sumber dengan beberapa alamat kini ditolak); (2) fragmen "direktori home kosong" dari Tugas 6 sudah diganti (keputusan 11); (3) kalimat C12 versi rencana ("Sampai itu MySQL-nya tetap baca-saja (read-only).") sudah diganti (keputusan 24); (4) teks D1 versi rencana (satu kalimat) sudah dipecah menjadi dua kalimat pada 3.3; (5) kalimat agent tentang mengapa persetujuan tidak berlaku (misalnya "persetujuan sudah habis pada …", "berkas persetujuan tidak bisa dipakai: …") masih ada di agent tetapi panel tidak lagi menampilkannya, diganti alasan bahasa panel pada 3.6.

## Bagian 4. Bagian README yang baru atau diubah

README bertambah sekitar 1.100 baris dan berkurang 53 baris sejak komit dasar `2609607`. Bagian berikut ditambah atau diubah banyak. Tiga bagian bertanda "yang paling perlu dibaca pemilik" menyangkut prosedur manual yang menyentuh data di server produksi.

1. `### Hubungkan MySQL` — menerangkan bahwa agent hanya mau tersambung ke MySQL di server itu sendiri dan apa yang tampil bila ditolak.
2. `### Peran dan sumber yang dipin` — memuat paragraf baru "Mode ketat (strict mode)": perintah yang tetap jalan, yang butuh persetujuan di server, dan yang selalu ditolak di server yang dikarantina.
3. `### Master lama` — menerangkan kunci start, cara agent menilainya, dan hasil start (`late`, `failed`, `bypassed`, `ok`) yang dilaporkan ke panel.
4. `#### Persetujuan di server untuk bangun ulang` — langkah demi langkah menyetujui bangun ulang di server dengan `sudo dbpanel agent approve`, teks yang muncul, dan arti tiap penolakan.
5. `#### Database yang tidak ada di master baru` — **yang paling perlu dibaca pemilik.** Prosedur tangan untuk database di master lama yang tidak ada di master baru: cadangkan dengan `mysqldump`, jatuhkan sebagai root MySQL, lalu bangun ulang lagi. Salah langkah di sini bisa membuang satu-satunya salinan data.
6. `#### Memberi hak SHOW DATABASES di server yang dikarantina` — **yang paling perlu dibaca pemilik.** Prosedur tangan untuk memberi hak `SHOW DATABASES` kepada akun agent di server yang baca-saja (`super_read_only`); kegagalan di tengahnya bisa meninggalkan `super_read_only` mati sampai diperiksa dan dipasang lagi.
7. `#### Memakai lagi server yang dihapus saat masih dikarantina` — apa yang terjadi di server bila master lama dihapus dari panel dan cara memakainya lagi dengan `set-role`.
8. `#### Melepas kunci start dengan tangan` — **yang paling perlu dibaca pemilik.** Prosedur tangan untuk melepas kunci start tanpa agent; salah langkah atau melewati langkah pemutusan aplikasi bisa membuat MySQL menyala dalam keadaan bisa ditulis sehingga ada dua master.

Perubahan kecil juga ada di `## Keamanan` (satu butir tentang apa yang bisa dan tidak bisa dilakukan panel yang disusupi pada master lama yang dikarantina) dan di `## Menjalankan test` (uraian tes menyeluruh yang kini memakai persetujuan `approve`).
