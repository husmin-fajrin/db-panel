# Rencana 5b-2: laporan untuk pemilik

Dokumen ini untuk pemilik dbpanel, yang memutuskan apakah cabang kerja Rencana 5b-2 (`claude/install-superpowers-plugin-ev2k9i`) digabung (merge) ke `main`. Isinya hanya fakta dari catatan pekerjaan. Laporan ini tidak memutuskan apa pun dan tidak menyatakan cabang ini aman untuk digabung: penilaian itu ada pada Anda.

Catatan lengkapnya ada di buku catatan pelaksanaan `docs/superpowers/ledgers/2026-11-20-05b2b-gerbang-prasyarat-panduan-ledger.md` dan di catatan audit `docs/superpowers/ledgers/2026-11-20-05b2b-audit-agent.md`, `…-audit-panel-core.md`, `…-audit-web.md`, `…-audit-fix-recheck-web.md`, `…-audit-fix-recheck-core-agent.md`, `…-corrective-fix-report.md`, `…-corrective-fix-recheck.md`.

**Istilah yang sering muncul.**

- **Panel**: aplikasi web tempat operator bekerja. **Agent**: program yang berjalan sebagai root di tiap server MySQL dan menjalankan perintah panel.
- **Prasyarat (gate)**: daftar pemeriksaan yang tampil di layar tindakan. Baris merah ("Belum terpenuhi") mematikan tombol tindakan terencana; baris kuning ("Perlu perhatian") hanya memberi tahu.
- **Promote**: menjadikan sebuah replika master baru. **Promote terencana**: master lama masih hidup; panel menguncinya dulu. **Promote darurat**: master lama mati atau tidak terjangkau.
- **"Ulangi"**: tombol "Ulangi dari tahap ini" di halaman pekerjaan yang gagal; ia melanjutkan pekerjaan yang sama dari tahap yang gagal.
- **Token konfirmasi / kata sandi**: tindakan berbahaya meminta kata sandi panel lagi sebelum jalan. Sesi yang sudah masuk (login) tanpa kata sandi tidak bisa menjalankannya.
- **`read_only` dan `super_read_only`**: dua pengaturan MySQL yang menolak tulisan. `SET PERSIST` menyimpannya sehingga tetap berlaku sesudah MySQL di-restart.
- **Karantina (quarantined)**: master lama yang dikunci baca-saja sesudah promote, sampai dibangun ulang.
- **Pin**: catatan agent di server itu sendiri tentang perannya dan master yang diikutinya. Agent menolak perintah yang tidak cocok dengan pin.
- **`set-role`, `unlock-master`**: perintah root di server (`sudo dbpanel agent …`) untuk mengubah pin peran dan membuka kunci master.
- **K4**: salah satu keputusan penulis rencana (K1–K11) yang sudah Anda lihat sebelum pekerjaan mulai: "Ulangi" dan "Setujui" pada pekerjaan promote yang sudah berjalan tidak dinilai lagi oleh daftar Prasyarat (alasan di rencana: promote tidak membuang data).
- **Gerbang uji**: pengujian otomatis yang harus lulus. `make test` menjalankan semua tes; `make test-e2e` menjalankan panel dan agent bersama; `-race` mencari tabrakan antar proses.
- **Tingkat temuan audit**: Kritis, Tinggi, Sedang, Rendah, Info (dari paling berat).

## Bagian 1. Ringkasan

**Yang dibangun.**

- Setiap layar tindakan yang bisa membuang data atau mengganti master kini menampilkan daftar **Prasyarat** (versi agent, pin master, jam, sertifikat TLS, ruang disk, akun panel, `performance_schema`, cadangan, persetujuan di server, port MySQL master, log audit, cadangan `panel.key`). Baris merah mematikan tombol, dan panel juga menolak tindakan itu di server bila tombol tetap dikirim. Halaman pekerjaan yang membuang data ikut menampilkan baris merah dan mematikan "Ulangi" dan "Setujui".
- Promote darurat tidak dihalangi baris merah, tetapi wajib dicentang: "Saya sudah memastikan master lama mati atau terputus dari aplikasi".
- Di halaman server ada isian **folder cadangan (backup)**. Agent melihat umur dan ukuran berkas terbaru di folder itu (bukan isinya). Bila cadangan master terlalu tua atau kosong, panel membuka peringatan baru `backup_stale` ("Cadangan master usang"), juga lewat Telegram.
- Ada halaman **Panduan** (menu Panduan) berisi sembilan panduan langkah demi langkah untuk keadaan yang jarang terjadi. Isinya sama dengan bagian "Panduan" di README.

**Cara diperiksa.**

- 14 tugas, masing-masing dibuat lalu ditinjau. Temuan penting diperbaiki sebelum tugas dinyatakan selesai.
- Tinjauan akhir seluruh cabang oleh tiga peninjau: tidak ada temuan Kritis, empat temuan Penting. Satu gelombang perbaikan (15 commit, butir F1–F13), lalu satu pemeriksaan ulang: semua butir beres.
- Audit keamanan oleh tiga auditor (sisi agent, inti panel, web). Jumlah temuan menurut auditor: **Kritis 0, Tinggi 1, Sedang 12, Rendah 23, Info 9**. Satu gelombang perbaikan audit (satu temuan Tinggi dan beberapa temuan kecil), diperiksa ulang oleh dua pemeriksa baru. Pemeriksaan ulang itu menemukan satu kemunduran baru (N1, Sedang) pada perbaikan "Ulangi". Karena itu ada satu perubahan pembetulan, yang juga diperiksa ulang: kemunduran itu beres, dan satu temuan kata-kata (Rendah) tersisa untuk Anda.
- Gerbang uji pada kode terakhir (`bc71feb`): `go test ./internal/panel/... -race -short` lulus; `make test` lulus (termasuk paket MySQL agent, dijalankan penuh); `make test-e2e` lulus. Pada `5fb84df` gerbang agent dengan `-race` juga lulus.

**Status cabang.** `main` tidak disentuh (tetap `78b1bb9`). Tidak ada yang digabung sampai Anda mengatakannya.

## Bagian 2. Yang perlu Anda putuskan

Butir di bawah ini **tidak diperbaiki**. Untuk tiap butir: apa itu, akibat terburuk menurut catatan auditor, dan saran pengendali pekerjaan. Saran hanyalah saran; keputusannya milik Anda.

### WB-2 (SEC-236) — salah login dari luar menghabiskan jatah percobaan dan memblokir konfirmasi kata sandi

- **Apa itu:** panel membatasi percobaan kata sandi. Batas per nama pengguna (20 per 15 menit) dipakai bersama oleh login dan konfirmasi kata sandi. Orang luar yang salah login 5 kali dari masing-masing empat alamat menghabiskan jatah itu (temuan Sedang, dibuktikan dengan menjalankannya).
- **Akibat terburuk:** pelakunya tidak perlu akun; cukup bisa menjangkau port panel dari empat alamat (misalnya server database yang disusupi, atau orang di LAN). Selama ia mengulang 20 permintaan per 15 menit, tidak ada yang bisa mengonfirmasi dengan kata sandi: tidak ada promote (juga promote darurat), tidak ada buka kunci master, bangun ulang, resync, hapus, atau ganti sertifikat; juga tidak ada yang bisa login. Pesannya menyebut "1 menit" padahal blokir sampai 15 menit. Restart panel tidak menolong. Tidak ada data yang hilang karena ini sendiri; yang terhenti adalah kemampuan bertindak, termasuk saat master mati.
- **Saran:** rencana berikutnya, kelompok pertama: hitung konfirmasi kata sandi dari sesi yang sudah login terpisah dari percobaan login tanpa nama. Sampai itu: panel hanya bisa dijangkau dari jaringan admin (firewall atau VPN).

### WB-4 (SEC-238) — token pemasangan ulang agent dibuat tanpa kata sandi; pemasangan tidak dicatat di audit

- **Apa itu:** sesi yang sudah login, tanpa kata sandi, bisa membuat perintah pemasangan (pairing) baru untuk server yang sudah terpasang. Pemasangan yang berhasil tidak menulis baris audit (temuan Sedang, dibaca dari kode).
- **Akibat terburuk:** dengan sesi yang dicuri atau ditinggal terbuka, pelaku menjalankan perintah itu dari host mana pun yang menjangkau panel. Agent asli terputus; proses pelaku menjadi "agent" server itu. Ia menerima semua perintah dan rahasia yang panel kirim ke sana (akun MySQL, akun replikasi, kunci sertifikat MySQL), bisa mengisi laporan dan baris Prasyarat sesukanya, dan bisa menolak atau memalsukan promote dan karantina. Di audit hanya tampak "Perintah pemasangan dibuat".
- **Saran:** rencana berikutnya: minta kata sandi dan tulis baris audit.

### WB-5 (SEC-239) — alamat master untuk replika bisa diganti tanpa kata sandi

- **Apa itu:** `POST /masters/{id}/source` tidak meminta kata sandi. Sesudahnya halaman master menyuruh root menjalankan `sudo dbpanel agent set-source <alamat itu>` di setiap replika (temuan Sedang, dibaca dari kode).
- **Akibat terburuk:** butuh sesi tanpa kata sandi dan operator yang menjalankan perintah itu sebagai root. Pin replika lalu menunjuk host pelaku. Resync atau Buat replika berikutnya (dimulai operator asli dengan kata sandi) menyalin data dari host itu; tanpa "Wajibkan pemeriksaan sertifikat TLS", kata sandi akun replikasi terkirim ke host itu.
- **Saran:** rencana berikutnya: minta kata sandi dan tulis baris audit.

### PC-1 (SEC-240), bagian promote terencana — "Ulangi" promote terencana yang gagal berjalan tanpa Prasyarat dan tanpa kata sandi

- **Apa itu:** auditor inti panel menemukan bahwa "Ulangi" pada promote yang gagal menjalankan promosi lagi tanpa satu pun pemeriksaan saat mulai (temuan Sedang, "batas atas"; dibuktikan dengan tes). Gelombang perbaikan audit sempat menolak "Ulangi" itu, tetapi untuk promote terencana hal itu menimbulkan kemunduran N1: master bisa tertinggal baca-saja tanpa tombol untuk membukanya. Perubahan pembetulan mengembalikan "Ulangi" promote terencana seperti semula (sesuai K4), dan README kini menulisnya apa adanya.
- **Akibat terburuk:** siapa pun di sesi yang sudah login, satu klik, tanpa kata sandi, kapan saja selama itu promosi terakhir topologi tersebut: promosi dilanjutkan tanpa daftar Prasyarat, tanpa centang cadangan, tanpa kata sandi. Kedua agent (kunci di master, dan pemeriksaan kandidat) tetap menjaga, jadi jalur ini sendiri tidak membuat dua master.
- **Saran:** rencana berikutnya, dengan langkah desain: "Ulangi" membaca Prasyarat dan meminta kata sandi, dan membuka kunci master yang tertahan menjadi tindakan tersendiri, supaya master tidak pernah tertinggal baca-saja tanpa tombol. Menurut pengendali, ini tidak aman diubah terlambat tanpa desain itu (itulah yang ditunjukkan N1).

### PC-1 (SEC-288), sisa promote darurat — "Ulangi" tanpa token pada promote darurat yang hasilnya tidak diketahui

- **Apa itu:** "Ulangi" kini ditolak untuk promote darurat yang gagal sebelum perintah promosi (`mysql.promote`) dikirim. Tetapi promote darurat yang perintahnya sudah terkirim dan hasilnya tidak diketahui (status "selesai sebagian") tetap punya "Ulangi" tanpa token, yang mengirim perintah itu lagi kapan saja.
- **Akibat terburuk:** sama dengan kasus darurat PC-1: bila pemeriksaan kandidat terhadap master lama tertipu oleh jaringan yang terputus sebagian, ada dua master untuk sementara, dan tulisan di master lama yang tidak ada di master baru.
- **Saran:** langkah desain yang sama dengan butir di atas.

### PC-2 (SEC-241) — satu agent yang diam menahan kunci arahkan-ulang seluruh panel sampai 5 menit

- **Apa itu:** saat agent tersambung, panel mengambil satu kunci untuk seluruh panel sebelum tahu apakah ada yang perlu dikirim. Satu agent yang tidak menjawab menahannya sampai 5 menit. Karantina tertunda yang gagal tidak dicoba lagi (temuan Sedang, dibaca dari kode).
- **Akibat terburuk:** pelaku: agent atau server database yang disusupi; atau tanpa pelaku, agent yang lambat atau macet. Karantina master lama yang kembali hidup sesudah promote darurat tertunda sampai 5 menit per tahanan (bisa diulang); selama itu master lama menerima tulisan di samping master baru. Bila karantinanya gagal, tidak ada yang mengirimnya lagi sampai agent itu tersambung ulang, dan operator harus mengikuti panduan 9 dengan tangan.
- **Saran:** rencana berikutnya: kunci per server, dan coba lagi karantina yang tertunda.

### AG-1 (SEC-243), sisanya — tidak ada batas permintaan serentak di agent; tidak ada `MemoryMax`

- **Apa itu:** agent membuat satu proses ringan per permintaan tanpa batas jumlah. Unit systemd agent tidak punya `MemoryMax`. Bagian panjang folder cadangan sudah diperbaiki (S5); sisanya belum (temuan Sedang, dibaca dari kode).
- **Akibat terburuk:** pelaku: panel yang disusupi. Memori agent tumbuh sampai kernel membunuh proses; yang terbunuh bisa `mysqld` di master. Tidak ada data yang hilang (InnoDB pulih), tetapi database rumah sakit berhenti.
- **Saran:** rencana berikutnya: batasi permintaan serentak; pasang `MemoryMax` di unit systemd agent.

### AG-2 (SEC-244) dan AG-3 (SEC-245) — `set-role` tidak bertanya pada dua arah yang berbahaya

- **Apa itu:** `set-role quarantined` tidak bertanya apa pun (AG-2, Sedang). `set-role replica` atau `unassigned` di server yang dipin master juga tidak bertanya (AG-3, Sedang). Keduanya dibaca dari kode.
- **Akibat terburuk AG-2:** operator yang terburu-buru menjalankan langkah 4 panduan 9 di terminal yang salah, di master yang sedang dipakai. Lima detik kemudian master menolak semua tulisan, juga sesudah restart. Database berhenti menerima tulisan sampai ada yang menjalankan `set-role master` dan `unlock-master`, lalu berhenti lagi sesudah restart MySQL berikutnya (lihat AG-4). Tidak ada data yang hilang, tidak ada master kedua.
- **Akibat terburuk AG-3:** butuh panel yang disusupi dan operator yang terburu-buru. Agent menolak perintah perusak dan teks penolakannya menyebut perintah `set-role`; operator menjalankannya di master; sesudah itu panel bisa menghapus atau menimpa data master. **Data master produksi hilang.** Auditor memberi Sedang, bukan Tinggi, karena root di server dipercaya dan perintah itu milik root sendiri.
- **Saran:** rencana berikutnya: konfirmasi (kata yang diketik atau `--ya`), dan perbarui panduan yang menampilkan perintah ini.

### AG-4 (SEC-246) dan AG-5 (SEC-247) — `unlock-master` membiarkan `read_only` yang disimpan karantina

- **Apa itu:** server yang pernah dikarantina lalu dijadikan master lagi dengan `set-role master` dan `unlock-master` tetap menyimpan `read_only=ON`. Sesudah restart MySQL berikutnya, master itu menolak tulisan aplikasi, sementara panel menampilkannya sehat karena panel hanya membaca `super_read_only` (temuan tinjauan akhir sisi agent; auditor agent tidak memeriksa sisi panel). AG-4 Sedang. AG-5 (Sedang, belum dibuktikan): pada master yang pernah dipromosikan, sesudah kunci lalu buka kunci, keadaan sesudah restart belum diukur; bisa jadi master itu juga menolak semua tulisan.
- **Akibat terburuk:** tidak perlu pelaku; cukup operator yang mengikuti baris yang dicetak perintah itu. Database berhenti menerima tulisan pada restart MySQL berikutnya, bisa berminggu-minggu kemudian. Tidak ada data yang hilang, tidak ada master kedua. Auditor agent menegaskan bahwa perbaikan dengan `RESET PERSIST`, yang diusulkan tinjauan akhir, **tidak aman**: master yang pernah dipromosikan tetap bisa ditulis hanya karena nilai OFF yang tersimpan.
- **Saran:** rencana berikutnya, awal: perbaiki dengan `SET PERSIST … = OFF` (bukan `RESET PERSIST`), dibuktikan dengan tes restart pada tiga jenis server; panel membaca kedua pengaturan. Sampai itu README dan panduan 5–7 memberi langkah manualnya.

### WB-9 (SEC-251) — header keamanan yang bisa lebih ketat

- **Apa itu:** CSP `default-src 'none'`, HSTS `includeSubDomains`, TLS 1.3 saja, dan `Clear-Site-Data` saat logout belum dipasang (temuan Rendah). Auditor menilai CSP yang ada "sound" dan hasil tambahannya kecil.
- **Akibat terburuk:** auditor tidak menyebut serangan yang terbuka karena ini; ini pengetatan. Catatan auditor: `includeSubDomains` memaksa HTTPS juga untuk nama di bawah nama panel.
- **Saran:** rencana berikutnya, dengan pemeriksaan di browser sungguhan. `includeSubDomains` hanya bila tidak ada situs lain di bawah nama domain panel yang dilayani lewat http biasa: Anda yang tahu itu, kodenya tidak.

### Teks "Lepas replika" (SEC-286) — cara membuat bekas replika bisa ditulis, tanpa syarat

- **Apa itu:** README (sekitar baris 1387), `handlers_mysql.go` (sekitar 210) dan `replica_detail.html` (218) memberi `SET PERSIST super_read_only=OFF; SET PERSIST read_only=OFF` untuk replika yang baru dilepas, tanpa syarat. Dicatat pelaksana perbaikan audit dan pemeriksa ulang web; tidak ada tingkat temuan dari auditor (auditor web semula mencatatnya "acceptable, noted").
- **Akibat terburuk (menurut pemeriksa ulang web):** ini kini satu-satunya tempat panel sendiri mencetak langkah itu. "Lepas replika" tersedia juga saat master "Diduga tumbang". Operator yang tidak bisa menjalankan promote darurat (misalnya karena WB-2) bisa sampai ke keadaan yang sama dengan kotak lama yang sudah dihapus (WB-1): replika bisa ditulis dan master lama tidak dikarantina, sehingga ada dua master bila master lama kembali.
- **Saran:** rencana berikutnya: tambahkan peringatan dua master dan syarat "tidak ada aplikasi yang bisa menjangkau server ini", atau pindahkan langkahnya ke sebuah panduan dengan syarat.

### Kata "Satu pengecualian" (SEC-283) — teks Prasyarat yang terlalu yakin

- **Apa itu:** README (sekitar baris 1528, 3445, 4291) dan halaman Panduan (`web/templates/panduan.html:91`) menyebut "Ulangi" promote terencana sebagai *satu-satunya* pengecualian dari "tidak ada tombol untuk melewati gerbang". Sebenarnya ada satu golongan: "Ulangi" pada pekerjaan yang tahap sisanya tidak membuang data juga berjalan tanpa menilai Prasyarat lagi (Siapkan master; Tambah database tanpa data yang dibuang; Buat replika sesudah salinannya jadi). Temuan Rendah dari pemeriksaan ulang perubahan pembetulan (keputusan R47).
- **Akibat terburuk:** tidak ada tindakan yang menjadi tidak aman karena ini; teksnya melebih-lebihkan. Operator membaca bahwa hanya ada satu pengecualian.
- **Saran:** perbaiki sekarang bila Anda ingin teksnya tepat sebelum merge (empat tempat, satu kalimat, README dan halaman Panduan bersama; kalimat usulan pemeriksa ada di catatannya), atau di kelompok pertama rencana berikutnya. Tidak diperbaiki tanpa kata Anda karena rencana hanya mengizinkan satu gelombang perbaikan, dan satu perubahan pembetulan sudah ditambahkan.

### Temuan Rendah dan Info lain, butir kecil tinjauan (lihat tabel 4.0 dan bagian 4.2–4.3 dokumen audit)

- **Apa itu:** temuan Rendah dan Info audit yang tidak masuk gelombang perbaikan; butir kecil yang ditinggalkan tinjauan akhir dan pemeriksaan ulangnya (tujuh butir kecil dan enam pengamatan di luar cakupan); butir kecil yang ditunda selama tugas (`docs/superpowers/ledgers/2026-11-20-05b2b-deferred-minors.md`); catatan di luar cakupan dari pemeriksaan ulang audit (antara lain: kotak baru di halaman master bisa tampil di samping kartu yang masih menulis "mode terencana"; jendela kunci yang bisa diperpanjang dengan menekan "Ulangi"; tenggat kunci dari agent master yang tidak dibatasi). Rinciannya dan akibat terburuk masing-masing ada di dokumen audit.
- **Saran:** rencana berikutnya, kelompok tugas pertama, seperti yang dilakukan sesudah Rencana 5b-1.

### Tes yang kadang gagal

- **Apa itu:** pembantu tes MySQL memilih port yang bisa dipakai proses lain sebelum `mysqld` memakainya ("Address already in use"); dan `TestBackupScanOneAtATime` gagal sekitar 2 dari 40 putaran, juga di `main`. Keduanya tidak menyangkut perilaku panel, tetapi bisa membuat gerbang uji gagal palsu.
- **Saran:** rencana berikutnya: betulkan pilihan port pembantu dan tes pemindaian itu.

**Pilihan Anda untuk butir-butir ini**, menurut rencana: **perbaiki sekarang** (sebelum merge), **rencana berikutnya**, atau **terima** risikonya. Pilihan bisa berbeda per butir.

## Bagian 3. Keputusan yang diambil saat pelaksanaan

Setiap keputusan di bawah diambil pengendali pekerjaan atas nama Anda dan tercatat di buku catatan dalam bentuk "keputusan — alasan — biaya bila keliru". Nomornya sama dengan di buku catatan: R1 dan R2 dari pemeriksaan awal, lalu R3 sampai R47, tanpa nomor yang hilang. Ada satu keputusan tanpa nomor (sesudah R30); ditaruh di tempatnya. Keputusan yang kemudian diubah atau dicabut diberi tanda.

### R1
- **Diputuskan:** tes `TestSnapshotStaysComparable` tetap berguna saat kompilasi dan kini juga memeriksa sesuatu saat dijalankan.
- **Alasan:** tes yang tidak memeriksa apa pun adalah cacat.
- **Biaya bila keliru:** tidak ada.

### R2
- **Diputuskan:** Task 8 tidak menyentuh `handlers_confirm.go`. Simpan folder cadangan dan dua tanda (cadangan `panel.key`, penerusan log audit) tidak meminta kata sandi, seperti tertulis di teks tugas.
- **Alasan:** daftar berkas di tugas itu yang salah tulis.
- **Biaya bila keliru:** sesi yang sudah login tanpa kata sandi bisa mengganti folder cadangan (tercatat di audit; yang terungkap hanya umur dan ukuran berkas).

### R3
- **Diputuskan:** batas waktu tes naik dari 60 ke 120 menit (`make test` dan gerbang 1 dan 2).
- **Alasan:** paket MySQL agent sudah memakan 59 dari 60 menit; satu putaran lambat menggagalkan kode yang benar dan membuang satu jam.
- **Biaya bila keliru:** tes yang macet baru ketahuan sesudah dua jam.

### R4
- **Diputuskan:** temuan kecil tentang jeda tetap 200 ms di tes harness diperbaiki sekarang (menunggu berkas penanda).
- **Alasan:** tes yang gagal karena waktu bisa membuang satu jam per kegagalan palsu.
- **Biaya bila keliru:** satu commit kecil tambahan.

### R5
- **Diputuskan:** P-M4 berubah: master baru tetap tidak bisa dihapus selama master lamanya masih tercatat, tetapi dengan pesan sendiri (teks F8).
- **Alasan:** versi rencana mengubah peran karantina master lama di panel dan bisa menghapus topologi saat bangun ulang masih berjalan; yang salah hanya pesannya.
- **Biaya bila keliru:** operator menghapus dua server berurutan (master lama dulu), bukan satu.

### R6
- **Diputuskan:** jawaban agent `timeout` atau `offline` atas mulai promote tetap dibaca "tidak ada jawaban"; perubahan pelaksana di situ dibatalkan.
- **Alasan:** perintah itu mungkin sudah membuka penulisan; menganggap "tidak ada yang dimulai" adalah sisi yang kurang hati-hati.
- **Biaya bila keliru:** operator melihat "tidak diketahui" dan memeriksa dulu sebelum mengulang.

### R7
- **Diputuskan:** kalimat longgar di tes Task 2 dibaca "langkah karantina gagal"; karantina itu tetap dicatat tertunda dan dikirim lagi saat agent tersambung.
- **Alasan:** mengirim ulang adalah sisi aman.
- **Biaya bila keliru:** tidak ada yang hilang.

### R8
- **Diputuskan:** "paling panjang 300 karakter" memakai batas `monitor.AgentText(s, 300)` yang sudah ada (bisa sampai sekitar 600 karakter, satu baris).
- **Alasan:** satu aturan untuk semua halaman dan Telegram.
- **Biaya bila keliru:** teks agent bisa tampil sampai sekitar 600 karakter, bukan 300.

### R9 (diubah oleh R10)
- **Diputuskan:** "Sumber dipin" di halaman server ikut dibatasi: host:port yang sah tampil apa adanya; nilai lain tampil "tidak dikenal (…)" dengan label agent.
- **Alasan:** halaman dan risiko yang sama dengan yang ditutup tugas ini; audit akan mengangkatnya lagi.
- **Biaya bila keliru:** satu commit kecil tambahan.

### R10 (mengubah R9)
- **Diputuskan:** "host:port yang sah" mengikuti aturan agent sendiri, termasuk tanda `_`.
- **Alasan:** R9 lupa `_`, sehingga pin jujur seperti `db_master:3306` akan tampil "tidak dikenal".
- **Biaya bila keliru:** tidak ada.

### R11
- **Diputuskan:** nama baru "Wajibkan pemeriksaan sertifikat TLS" diberi tanda kutip di dua dokumen keamanan; penyebutan pertama di README diberi `(TLS certificate verification)`.
- **Alasan:** nama baru berupa frasa kerja yang terbaca menyatu dengan kalimat.
- **Biaya bila keliru:** tanda kutip di beberapa sel tabel.

### R12
- **Diputuskan:** Task 5 di-commit dan ditinjau selagi `make test` masih berjalan; hasilnya dibaca sebelum tugas dinyatakan selesai.
- **Alasan:** gerbang lain sudah lulus pada kode yang sama; menunggu menambah satu jam tanpa bukti baru.
- **Biaya bila keliru:** perbaikan datang sesudah tinjauan, bukan sebelumnya.

### R13
- **Diputuskan:** Task 6 membuka folder cadangan dengan cara yang lebih ketat dari tulisan rencana (lewat `/proc/self/fd`, tiap subfolder dibuka tanpa mengikuti symlink, jumlah entri dibatasi).
- **Alasan:** cara di rencana membaca seluruh isi folder tanpa batas dan bisa macet; aturan keamanan rencana yang mengikat.
- **Biaya bila keliru:** host tanpa `/proc` melaporkan folder tidak terbaca (tanpa risiko data); kode tambahan untuk dirawat.

### R14
- **Diputuskan:** folder cadangan yang path-nya berupa atau melewati symlink tetap dipindai di tempat tujuannya, asal tujuan itu lolos aturan folder. Di dalam folder tidak ada symlink yang diikuti.
- **Alasan:** "/backup → disk lain" umum; tanpa ini operator membaca "itu bukan folder" tanpa tahu sebabnya.
- **Biaya bila keliru:** orang yang bisa menaruh symlink di path itu (butuh izin tulis di folder induknya) membuat agent melaporkan umur dan ukuran berkas terbaru di folder lain; dua angka, tanpa nama dan isi.

### R15
- **Diputuskan:** berkas yang sekaligus terlalu tua dan 0 byte dinilai "usang" (umur dinilai dulu).
- **Alasan:** umur tetap benar sampai ada berkas baru.
- **Biaya bila keliru:** operator melihat pesan benar yang lain lebih dulu.

### R16
- **Diputuskan:** nama peringatan di Telegram "cadangan master usang" (huruf kecil); di halaman "Cadangan master usang".
- **Alasan:** semua nama jenis lain di Telegram huruf kecil.
- **Biaya bila keliru:** satu huruf besar.

### R17 (diubah oleh R18)
- **Diputuskan:** umur di teks G8a ditulis menit, jam, lalu hari dan jam, selalu dibulatkan ke bawah.
- **Alasan:** teks peringatan yang terbuka tidak berubah lebih dari sekali sejam.
- **Biaya bila keliru:** hanya kata-kata.

### R18 (mengubah R17)
- **Diputuskan:** selama jam pertama sesudah batas terlewati, umur ditulis "lebih dari <batas> jam".
- **Alasan:** tanpa itu teksnya berbunyi "berumur 48 jam (batas 48 jam)".
- **Biaya bila keliru:** hanya kata-kata.

### R19
- **Diputuskan:** di kartu cadangan server yang bukan master, teks masalahnya memakai kata "server", bukan "master".
- **Alasan:** kartu tampil di semua server; replika tidak boleh disebut master.
- **Biaya bila keliru:** empat variasi teks di luar rencana (ada di daftar teks Anda).

### R20
- **Diputuskan:** isian folder dirapikan (spasi dan garis miring di akhir dibuang) sebelum diperiksa dan disimpan.
- **Alasan:** "/var/backups/mysql/" yang ditempel tidak boleh ditolak dengan pesan yang tidak menjelaskan salahnya.
- **Biaya bila keliru:** tidak ada yang diketahui.

### R21
- **Diputuskan:** label audit untuk tiga aksi baru: "Atur folder cadangan", "Tanda cadangan panel.key", "Tanda penerusan log audit".
- **Alasan:** tanpa label, halaman audit menampilkan nama mentah.
- **Biaya bila keliru:** hanya kata-kata.

### R22
- **Diputuskan:** begitu pengaturan cadangan disimpan, panel melupakan laporan cadangan terakhir server itu sampai agent melapor lagi.
- **Alasan:** laporan lama menggambarkan folder lain, dan pada agent yang offline akan tetap begitu.
- **Biaya bila keliru:** sesudah mengubah batas umur saja, kartu menulis "Belum ada laporan dari agent." sampai agent menjawab.

### R23
- **Diputuskan:** panel menolak folder yang memuat karakter kendali Unicode atau bukan UTF-8 yang sah (lebih ketat dari agent).
- **Alasan:** path seperti itu tidak bisa ditampilkan kembali apa adanya; menyimpan lagi akan menyimpan path lain.
- **Biaya bila keliru:** folder dengan nama seperti itu tidak bisa diisi dari panel.

### R24
- **Diputuskan:** selisih jam di teks G3a ditulis detik (dibulatkan ke atas) di bawah 2 menit, lalu menit, lalu jam (ke bawah).
- **Alasan:** baris merah tidak boleh berbunyi "berbeda 30 detik (batas 30 detik)".
- **Biaya bila keliru:** hanya kata-kata.

### R25
- **Diputuskan:** baris Cadangan di Prasyarat kuning ("belum diketahui") selama agent master tidak tersambung.
- **Alasan:** tanpa agent, penilaian "usang" hanya dari perkiraan waktu; data yang tidak diketahui berwarna kuning.
- **Biaya bila keliru:** cadangan yang benar-benar usang di master yang offline tampil kuning, bukan merah; promote terencana tetap butuh master online.

### R26
- **Diputuskan:** "folder belum diisi" dinilai lebih dulu dari data panel sendiri: kuning, dan centang cadangan tetap diminta, walau agent master offline.
- **Alasan:** spesifikasi meminta centang bila cadangan belum diatur; itu tidak bergantung pada agent.
- **Biaya bila keliru:** satu centang diminta saat master offline.

### R27
- **Diputuskan:** baris Port MySQL master juga dilewati bila host panel adalah tujuan tindakan.
- **Alasan:** host itu akan menjadi replika yang memang harus menjangkau port itu.
- **Biaya bila keliru:** satu petunjuk kuning tidak tampil.

### R28 (diubah oleh R40 untuk baris Akun panel)
- **Diputuskan:** dua baris kuning diberi cara memperbaiki yang tidak ada di rencana (G8u dan G10a); G10b tanpa cara memperbaiki.
- **Alasan:** baris yang menyebut masalah tanpa menyebut langkahnya membingungkan operator.
- **Biaya bila keliru:** dua kalimat untuk diubah (ada di daftar teks Anda).

### R29
- **Diputuskan:** pemeriksaan Prasyarat dijalankan di bawah kunci mulai (`startMu`) seperti di rencana; pemeriksaan port pertama boleh menahan kunci paling lama 1 detik.
- **Alasan:** lebih sederhana; tidak ada tindakan yang terganggu oleh 1 detik.
- **Biaya bila keliru:** tindakan antre 1 detik, sekali per 10 menit per master.

### R30
- **Diputuskan:** baris Akun panel hanya melewati inventaris yang belum tersambung untuk tujuan Buat replika; untuk server lain, kuning.
- **Alasan:** data yang tidak diketahui berwarna kuning menurut spesifikasi.
- **Biaya bila keliru:** satu baris kuning lagi di server yang sambungan MySQL-nya putus.

### Keputusan tanpa nomor (Task 9, sesudah R30)
- **Diputuskan:** temuan kecil "pemeriksaan port yang tidak pernah kembali membuat yang menunggu terblokir selamanya" diperbaiki di Task 10, dengan tes, sebelum Prasyarat dipindah ke bawah kunci.
- **Alasan:** dengan R29 temuan itu menjadi penting.
- **Biaya bila keliru:** tidak dicatat di buku catatan.

### R31
- **Diputuskan:** teks `Prasyarat tidak bisa dibaca: %s` (tidak ada di rencana) dipakai.
- **Alasan:** halaman harus mengatakan sesuatu bila Prasyarat tidak bisa dinilai.
- **Biaya bila keliru:** satu kalimat untuk diubah; `%s` bisa berisi pesan galat basis data berbahasa Inggris.

### R32
- **Diputuskan:** panduan 5 dan 7 menyalin langkah README yang sudah diaudit kata demi kata, termasuk `SET PERSIST super_read_only=OFF; SET PERSIST read_only=OFF;`, tepat sesudah syaratnya. Tidak ada panduan yang menambah langkah semacam itu.
- **Alasan:** rencana menyuruh menyalin, bukan menulis ulang; README menjelaskan mengapa langkah manual itu ada.
- **Biaya bila keliru:** panduan mengulang langkah root yang berisiko, yang sudah ada di README.

### R33
- **Diputuskan:** panduan 6 tidak menambah perintah; hanya satu kalimat rujukan ke langkah panduan 7.
- **Alasan:** mengikuti sumbernya.
- **Biaya bila keliru:** satu kalimat rujukan.

### R34
- **Diputuskan:** panduan 9 dan 3 ditulis baru. Panduan 9 hanya memakai perintah CLI agent yang ada, hanya ke arah aman (menuju baca-saja atau karantina).
- **Alasan:** spesifikasi memintanya dan belum ada teks yang diaudit.
- **Biaya bila keliru:** panduan dengan langkah yang salah; keduanya ditandai untuk tinjauan, audit dan bacaan Anda.

### R35
- **Diputuskan:** akun khusus di panduan 8 bernama `panel_admin@localhost`, bukan `dbpanel_admin`; hak aksesnya sama.
- **Alasan:** README "Hubungkan MySQL" melarang nama berawalan `dbpanel_`.
- **Biaya bila keliru:** nama berbeda dari yang dibuat `reset-admin`; satu nama untuk diubah.

### R36
- **Diputuskan:** langkah akun di panduan 8 tidak pernah dijalankan di replika, dan baca-saja tidak pernah dimatikan di replika; dijalankan di master. Panduan memeriksa dulu apakah akun sudah ada.
- **Alasan:** panduan yang gagal di replika kandidat justru memblokir promote yang mau dibukanya.
- **Biaya bila keliru:** beberapa kalimat panduan 8.

### R37
- **Diputuskan:** tinjauan akhir memakai tiga peninjau, bukan dua. (Hanya soal cara kerja.)
- **Biaya bila keliru:** satu peninjau lagi.

### R38
- **Diputuskan:** perilaku `unlock-master` yang membiarkan `read_only` dari karantina tidak diubah di gelombang perbaikan akhir; dibawa ke audit lalu kepada Anda (lihat AG-4 di Bagian 2).
- **Alasan:** perilaku lama di perintah root, tertulis di README yang sudah diaudit; mengubahnya terlambat mengubah perilaku keamanan yang sudah diaudit.
- **Biaya bila keliru:** sampai diperbaiki, master bisa menjadi baca-saja lagi sesudah restart MySQL (layanan terhenti, tanpa kehilangan data), sementara panel menampilkannya sehat.

### R39
- **Diputuskan:** laporan cadangan yang terpotong (folder lebih dari 10.000 entri) tetap dinilai, dan mendapat satu kalimat penjelasan.
- **Alasan:** baris merah harus bisa dijelaskan dan diperbaiki operator.
- **Biaya bila keliru:** satu kalimat; folder besar tetap memblokir sampai diarahkan ke subfolder.

### R40 (mengubah R28 untuk satu baris)
- **Diputuskan:** baris Akun panel yang belum dilaporkan mendapat teks perbaikan yang menyebut "Segarkan"; README dan panduan 8 ditambah langkahnya; panel membaca inventaris sekali saat agent tersambung bila akunnya belum diketahui.
- **Alasan:** tanpa ini, sesudah pembaruan, setiap server yang sudah ada membiarkan agent yang masuk sebagai root lolos di tindakan terencana sampai ada yang mengklik.
- **Biaya bila keliru:** satu pembacaan inventaris per sambungan agent untuk server tanpa akun tersimpan.

### R41
- **Diputuskan:** peringatan `backup_stale` tidak dibuka dan tidak ditutup selama agent master tidak tersambung atau laporannya sudah lama.
- **Alasan:** alasan R25 berlaku juga untuk peringatan.
- **Biaya bila keliru:** cadangan yang benar-benar usang di master yang agent-nya pergi tidak membuka peringatan baru (peringatan agent offline menutupi keadaan itu).

### R42
- **Diputuskan:** gelombang perbaikan audit mengerjakan temuan Tinggi (WB-1) dan temuan kecil yang tidak mengubah keputusan yang disetujui: PC-3, WB-3, PC-1, pemeriksaan panjang AG-1, WB-8, dan bagian aman WB-9.
- **Alasan:** Anda meminta risiko tidak ditunda dan header yang kuat.
- **Biaya bila keliru:** gelombang lebih besar dari minimum rencana, diperiksa ulang hanya pada diff-nya.

### R43 (dicabut untuk promote terencana oleh R46)
- **Diputuskan:** "Ulangi" promote yang gagal sebelum perintah promosi terkirim ditolak, untuk kedua jenis; operator memulai promote baru. Bila ternyata master bisa tertinggal terkunci dengan "Ulangi" sebagai satu-satunya jalan keluar, butir ini dihentikan dan dilaporkan.
- **Alasan:** mempersempit K4 ke arah aman dan membuat kalimat README "tidak ada tombol untuk melewatinya" benar.
- **Biaya bila keliru:** satu kali mulai lagi (dengan konfirmasinya) sesudah promote gagal.

### R44
- **Diputuskan:** tidak masuk gelombang perbaikan audit, dibawa kepada Anda dengan saran: WB-2, WB-4, WB-5, PC-2, sisa AG-1, AG-2, AG-3, AG-4/AG-5, header yang lebih ketat, dan semua temuan Rendah dan Info lain.
- **Alasan:** masing-masing mengubah keputusan yang disetujui, butuh desain, pengukuran, atau pemeriksaan di browser.
- **Biaya bila keliru:** risiko ini tetap terbuka sampai rencana berikutnya.

### R45
- **Diputuskan:** gelombang perbaikan audit diperiksa ulang oleh dua pemeriksa baru pada diff-nya, bukan oleh tiga auditor semula. (Hanya soal cara kerja; untuk menghemat kuota.)
- **Biaya bila keliru:** pemeriksaan ulang tidak tahu apa yang diingat auditor di luar catatannya.

### R46 (mencabut R43 untuk promote terencana)
- **Diputuskan:** penolakan "Ulangi" tetap untuk promote darurat yang gagal sebelum perintah promosi terkirim. Promote terencana mendapat "Ulangi"-nya kembali seperti sebelumnya (K4 tidak berubah), logika jendela kunci dihapus, dan README menulis apa adanya. Bagian promote terencana dari PC-1 dibawa kepada Anda.
- **Alasan:** syarat R43 sendiri tidak terpenuhi (kemunduran N1: master bisa tertinggal baca-saja tanpa tombol); gangguan tulis yang lebih lama di master produksi lebih buruk daripada risiko yang ditutup, yang masih dijaga kedua agent.
- **Biaya bila keliru:** promote terencana yang gagal tetap bisa dilanjutkan dengan "Ulangi" tanpa Prasyarat dan tanpa kata sandi sampai Anda memutuskan.

### R47
- **Diputuskan:** kata "Satu pengecualian" di README dan halaman Panduan tidak diperbaiki sekarang; dibawa kepada Anda sebagai temuan Rendah dengan kalimat usulan pemeriksa (lihat Bagian 2).
- **Alasan:** rencana mengizinkan satu gelombang perbaikan, R46 sudah menambah satu perubahan, dan tiap putaran baru pada teks panduan yang diaudit membawa temuan baru; kalimat itu hanya melebih-lebihkan untuk "Ulangi" yang tidak membuang data.
- **Biaya bila keliru:** sampai Anda berkata "perbaiki", empat tempat menyebut tepat satu pengecualian padahal ada satu golongan; tidak ada tindakan yang menjadi tidak aman.

## Bagian 4. Yang tidak dijalankan atau tidak dibuktikan

- Tidak ada audit penuh kedua sesudah gelombang perbaikan; perbaikan hanya diperiksa ulang pada diff-nya.
- Pemeriksaan ulang gelombang perbaikan audit dilakukan dua pemeriksa baru yang diberi teks temuan auditor, bukan oleh auditor sendiri.
- `govulncheck` (pemeriksa kerentanan pustaka) tidak bisa menjangkau vuln.go.dev dari mesin pembangun: tidak dijalankan. Tidak ada pustaka yang dicocokkan dengan basis data kerentanan.
- Tidak ada percobaan di browser sungguhan. Header, CSP dan halaman diperiksa dengan tes dan dengan membaca. Bagian WB-3 yang bergantung pada perilaku cookie di Firefox dan Safari berasal dari pengetahuan auditor, bukan tes (WB-3 sudah diperbaiki).
- Langkah panduan yang tidak dijalankan dari awal sampai akhir: langkah `systemctl`/`timedatectl`; `unlock-master` dan `reset-admin` dengan agent yang terpasang; langkah 6–7 panduan 9 (panduan 9 diperiksa dengan membaca kode dan menjalankan CLI saja); "Hubungkan MySQL" dengan `panel_admin` di UI.
- Pemindaian ulang folder cadangan tiap 5 menit tidak ditunjukkan dari ujung ke ujung (hanya tes unit agent). Tes e2e nomor 3 (cadangan) tidak diuji dengan mutasi dan butuh root.
- Perilaku MySQL sesudah restart untuk AG-5 tidak diukur (aturan audit melarang menyalakan MySQL): belum dibuktikan.
- Temuan PC-2 sampai PC-9 dan semua temuan agent hanya dibuktikan dengan membaca kode; PC-1 dengan satu tes memakai agent palsu. Tes untuk kemunduran N1 memakai agent palsu: bahwa agent sungguhan membuka kunci di keadaan itu dibaca dari kode agent, tidak dijalankan.
- Tes yang diketahui kadang gagal, tidak diperbaiki: pembantu tes MySQL yang berebut port ("Address already in use"; `make test` gagal sekali karenanya di Task 9 dan lulus saat diulang), dan `TestBackupScanOneAtATime` (sekitar 2 dari 40 putaran, juga di `main`).

## Bagian 5. Teks baru untuk Anda baca

Semua teks operator yang baru, berubah atau hilang dalam rencana ini dikumpulkan di bagian "Teks baru untuk dibaca pemilik" di akhir rencana `docs/superpowers/plans/2026-11-20-05b2b-gerbang-prasyarat-panduan.md`; teks yang ditambahkan pelaksana di luar rencana ditandai "tidak ada di rencana". Bagian itu punya 15 sub-bagian, dari "Menu dan halaman Panduan" sampai "Gelombang perbaikan audit keamanan", dan menyebut bagian README yang perlu dibaca utuh ("Prasyarat", "Cadangan (backup)", "Pembaruan", "Keamanan").
