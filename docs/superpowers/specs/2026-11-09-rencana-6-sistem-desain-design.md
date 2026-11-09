# Rencana 6: Sistem Desain dbpanel dan Rapi-Tampilan

Status: disetujui pemilik produk 2026-11-09 (keputusan 8.1 b, 8.2 b)
Mockup acuan: kanvas "Mockup tampilan dbpanel", varian D (gelap) dan D-terang.
Dasar: tur tangkapan layar 15 halaman dari panel yang berjalan (commit 5e0d213).

## 1. Maksud dan alasan

Panel sudah lengkap secara fungsi (Rencana 1–4c) tetapi tampilannya
"kaku, seperti web jadul" (penilaian pemilik produk setelah melihat tur
tangkapan layar), dan tur yang sama menemukan enam cacat kosmetik yang
mengurangi kepercayaan pada panel yang kerjanya berisiko. Pemilik produk
terbiasa dengan aaPanel dan menginginkan panel yang informatif (chart,
gauge), mudah dioperasikan, dan modern.

Rencana ini mengganti kulit panel, bukan isinya: setiap aksi, teks
keputusan, token konfirmasi, dan alur promote tetap sama. Yang berubah
adalah sistem desain (warna, huruf, spasi, komponen), tata letak (sidebar
kiri), widget dashboard (gauge dan grafik), dan perbaikan cacat kosmetik.

## 2. Tujuan, bukan-tujuan, anti-tujuan

Tujuan:
- Satu sistem desain yang dipakai semua 15+ halaman, dua mode (gelap dan
  terang), diterapkan lewat tema daisyUI kustom dan komponen template.
- Dashboard bergaya aaPanel: gauge ringkasan, grafik lag 24 jam, kartu
  topologi, tabel peringatan dan pekerjaan terakhir.
- Semua cacat kosmetik dari tur tangkapan layar diperbaiki (bagian 5.8).
- Tur tangkapan layar menjadi bukti sebelum/sesudah dan alat regresi.

Bukan-tujuan:
- Tidak ada fitur baru selain yang dibutuhkan widget dashboard (riwayat
  lag). Tidak ada perubahan alur, teks keputusan, atau aturan keamanan.
- Tidak ada framework front-end baru; tetap Tailwind v4 + daisyUI v5 +
  Alpine (build CSP) + html/template.

Anti-tujuan (gagal walau "jadi"):
- Pink dipakai untuk hal yang bukan bahaya (sinyal bahaya menjadi tumpul).
- Teks penting memakai warna neon yang kontrasnya di bawah 4.5:1.
- Animasi berdenyut atau glow yang mengganggu pemantauan berjam-jam.
- Aset dimuat dari internet (panel berjalan di jaringan lokal; CSP
  `'self'`).
- Perubahan kulit yang mengubah nama tombol atau kalimat keputusan yang
  sudah ditetapkan Rencana 4 (spek 2026-11-05 §5.15).

## 3. Batasan

- CSP sekarang `default-src 'self'; script-src 'self'; style-src 'self'`
  tanpa inline script: huruf dan skrip harus dibundel di binary
  (`web/static/`), pengalih tema harus skrip statis terpisah.
- Build CSS tanpa Node (`scripts/tailwind.sh`, Tailwind standalone +
  daisyUI); tema kustom didefinisikan di `web/src/app.css` lewat
  `@plugin "daisyui/theme"`.
- Huruf: Bungee, Work Sans, Space Mono (lisensi OFL), subset Latin,
  WOFF2, total ≤ 400 KB; dibundel, bukan Google Fonts.
- Semua teks Indonesia; nama tombol dan kalimat keputusan dari Rencana 4
  tidak berubah.
- Lebar telepon tetap bekerja (sidebar menjadi menu lipat di atas).
- Tes web yang mengecek teks halaman harus tetap lulus; tes yang
  mengecek kelas CSS tertentu boleh disesuaikan.

## 4. Pendekatan yang dipertimbangkan

Empat varian digambar dan dibandingkan di kanvas mockup:

- **A "Hijau aaPanel"**: sidebar navy gelap, aksen hijau, padat, gauge
  besar. Paling mirip aaPanel; terasa generik.
- **B "Teal tenang"**: sidebar terang, aksen teal, kartu lega, angka
  besar + gauge kecil. Modern, kurang "panel".
- **C ArcadeNeon apa adanya**: pondasi B + widget A dengan sistem desain
  ArcadeNeon yang diberikan pemilik produk (latar gelap, pink/cyan/lime,
  Bungee). Masalah: pink dipakai untuk aksi utama dan bahaya sekaligus;
  Bungee untuk semua judul; glow berdenyut.
- **D (dipilih)**: C yang disesuaikan untuk panel operasi: pink hanya
  untuk bahaya, cyan untuk aksi utama, Bungee hanya untuk judul halaman
  dan logo, glow tipis hanya di gauge, tanpa animasi; ditambah mode
  terang dengan padanan warna yang lebih gelap.

Keputusan pemilik produk: pondasi B, widget dashboard A, palet
ArcadeNeon yang disesuaikan (D), mode gelap dan terang, huruf dibundel.

## 5. Keputusan desain

### 5.1 Token warna (dua tema daisyUI: `dbpanel-gelap`, `dbpanel-terang`)

| Peran (token daisyUI) | Gelap | Terang |
|---|---|---|
| `base-100` kartu / sidebar | `#12121F` | `#FFFFFF` |
| `base-200` latar halaman | `#0A0A14` | `#F4F6F9` |
| `base-300` garis, pemisah | `#2A2A3E` | `#DDE2EA` |
| `base-content` teks | `#EEEEF0` | `#141424` |
| teks redup (`base-content/70` dipetakan ke warna tetap) | `#9999AA` | `#5E6577` |
| `primary` aksi utama, tautan, menu aktif | `#00F0FF` (teks di atasnya `#0A0A14`) | `#0A7F93` (teks `#FFFFFF`) |
| `success` sehat, pulih, sinkron | `#39FF14` | `#1E9E2E` |
| `warning` peringatan, tertunda, sebagian | `#FFD600` | `#C98A00` |
| `error` bahaya, destruktif, gagal, kritis | `#FF006E` (teks di atasnya `#FFFFFF`; teks merah di latar gelap memakai `#FF4D94`) | `#E0005F` (teks merah `#B8004E`) |
| `info` | sama dengan `primary` | sama dengan `primary` |
| `neutral` tombol sekunder | `#1A1A2E` | `#E9EDF2` |

Aturan pemakaian:
- **Pink (`error`) hanya untuk bahaya**: tombol destruktif (Hapus,
  Lepas, Promote darurat, Bangun ulang, hapus salvage), lencana
  Kritis/gagal, kotak peringatan "tidak bisa dibatalkan", titik status
  gagal. Tidak pernah untuk aksi biasa, logo, menu aktif, atau grafik.
- Cyan (`primary`) untuk aksi utama non-destruktif, tautan, menu aktif,
  baris terpilih, garis grafik utama.
- Maksimal dua warna neon dalam satu komponen.
- Setiap pasangan teks/latar memenuhi kontras 4.5:1 (3:1 untuk teks
  ≥ 24 px). Pasangan di tabel di atas sudah dihitung; pelaksana
  memverifikasi dengan alat kontras pada CSS akhir.
- Status yang harus dibedakan juga berbeda terang-gelapnya, bukan hanya
  warnanya (sehat hijau/lime, peringatan kuning/amber, bahaya pink).

### 5.2 Huruf

- **Bungee** (regular): hanya logo "dbpanel" di sidebar (18 px) dan judul
  halaman H1 (28 px, line-height 1.2). Tidak untuk judul kartu, tabel,
  tombol, atau lencana.
- **Work Sans** 400/500/600: semua teks lain. Body 14 px / 1.5; judul
  kartu H2 16 px/600; teks bantu 12 px; tombol 14 px/600 (16 px untuk
  tombol utama besar).
- **Space Mono** 400/700: angka di gauge, waktu, lag, ukuran, GTID,
  nomor versi, isi "Detail teknis", kolom waktu tabel. Tidak untuk
  lencana (nama Indonesia lebih terbaca dalam Work Sans).
- Berkas WOFF2 subset Latin di `web/static/fonts/`, `@font-face` di
  `app.css` dengan `font-display: swap`; `<link rel="preload">` untuk
  Work Sans 400/600.

### 5.3 Spasi, radius, bayangan

- Skala 8 px: 4, 8, 16, 24, 32, 48. Padding kartu 24 px; jarak antar
  kartu 16 px; margin konten 32 px (telepon 16 px).
- Radius: kartu, tombol, input 8 px; lencana dan chip 4 px; modal 12 px.
- Bayangan: tidak ada pada kartu (tepi 1 px `base-300` saja). Glow
  hanya `drop-shadow(0 0 6px <warna> 35%)` pada busur gauge, mode gelap
  saja. Tidak ada animasi glow; animasi hanya untuk transisi yang
  dipicu pengguna (buka modal, lipat sidebar) ≤ 150 ms dan dihormati
  `prefers-reduced-motion`.

### 5.4 Tata letak

- **Sidebar kiri** 240 px, latar `base-100`, tepi kanan `base-300`:
  logo + nama instansi (pengaturan baru `nama_instansi`, kosong →
  disembunyikan), menu Dashboard, Server, Replikasi, Pekerjaan,
  Peringatan (dengan hitungan), Audit, Pengaturan; kotak status bawah
  ("Semua agent tersambung · N server · panel vX" atau "M agent offline")
  dengan tombol pengalih tema Gelap/Terang dan tombol Keluar (nama
  pengguna).
- "Replikasi" adalah menu dan rute baru `/replikasi`: daftar topologi
  (master, jumlah replika, status ringkas, tombol ke `/masters/{id}`);
  bila hanya ada satu topologi, rute ini langsung mengalihkan ke
  halamannya. Halaman "Server" tetap daftar semua server.
- **Konten** `flex: 1`, lebar penuh, maksimum 1440 px konten + margin;
  judul halaman H1 Bungee di kiri atas dengan breadcrumb di atasnya
  pada halaman turunan; aksi halaman di kanan judul.
- Telepon (< 768 px): sidebar menjadi bar atas dengan tombol menu yang
  membuka daftar (Alpine, tanpa inline script); konten margin 16 px;
  tabel lebar dalam kotak gulir horizontal; grid 4 gauge menjadi 2×2.
- Pengalih tema: skrip statis kecil `web/static/theme.js` dimuat di
  `<head>` (sinkron, bukan defer) yang membaca `localStorage.tema`
  atau `prefers-color-scheme` dan menyetel `data-theme` sebelum render,
  sehingga tidak berkedip; tombol di sidebar menulis `localStorage` dan
  mengganti atribut. Tanpa cookie, tanpa sisi server.

### 5.5 Komponen

- **Gauge**: SVG inline dari template (`define "gauge"`): lingkaran
  dasar `base-300` 9 px, busur warna status, angka Space Mono 17/700 di
  tengah, label Work Sans 600 dan subteks 12 px di kanan; `role="img"`
  + `aria-label` yang menyebut nilai dan artinya.
- **Grafik garis**: SVG inline dari template dengan data dari server
  (bagian 5.7), sumbu 0–ambang×1.5, label jam, legenda per replika,
  titik dan label pada puncak; tanpa pustaka JS. Warna seri: cyan
  (replika 1), kuning/amber (replika 2), hijau (replika 3), lalu
  berulang dengan garis putus-putus.
- **Kartu**: `base-100`, tepi 1 px `base-300`, radius 8, padding 24;
  judul H2 di kiri, aksi/tautan di kanan baris judul.
- **Tabel**: kepala 12 px redup dengan garis bawah `base-300`; baris
  48 px, pemisah `base-300` tipis; kolom waktu Space Mono 12 px; aksi
  baris sebagai tombol kecil 32 px; tabel lebar dalam kotak gulir.
- **Lencana status**: padding 3×10, radius 4, Work Sans 12/600, latar
  warna status 13% dan teks warna status (terang: pasangan gelap).
  Lebar mengikuti isi, tidak pernah menimpa teks lain; `white-space:
  nowrap`.
- **Tombol**: tinggi 40 (32 kecil, 48 besar); utama `primary` isi;
  sekunder `neutral` isi dengan tepi `base-300`; destruktif `error`
  isi; tautan-tombol untuk aksi ringan. Fokus keyboard terlihat (ring
  2 px `primary`).
- **Dialog konfirmasi** (yang ada): radius 12, latar `base-100`, judul
  H2, teks peringatan dalam kotak bertepi `error` bila destruktif,
  kolom nama/kata sandi 40 px, tombol konfirmasi `error` untuk aksi
  destruktif dan `primary` untuk lainnya.
- **Banner** (flash, notice, cert, promote_partial, cutover_pending):
  kotak bertepi kiri 3 px warna status, latar status 8%, ikon stroke
  SVG; tidak lebih dari satu banner kritis sekaligus di atas konten.
- **Kotak kosong**: ikon stroke kecil + kalimat ajakan ("Belum ada
  replika. Tambah replika pertama.") dengan tombol.
- Ikon: stroke SVG inline 18/20 px, tanpa emoji, satu set gaya (garis
  2 px, ujung bulat), `aria-hidden` bila dekoratif.

### 5.6 Halaman

Setiap halaman yang ada dipindahkan ke tata letak dan komponen baru
tanpa mengubah rute, form, nama tombol, dan kalimat keputusan. Yang
berubah per halaman:

- **Dashboard**: baris sapaan singkat + status global; grid 4 gauge
  (replika sinkron x/y; lag tertinggi vs ambang; disk master % dengan
  GB bebas; peringatan terbuka dengan ringkasan satu baris); grafik lag
  24 jam; kartu topologi (master → replika, status satu baris, tombol
  Tambah replika dan Ganti master…); tabel Peringatan terbaru (3) dan
  Pekerjaan terakhir (3). Banner promote_partial dan cutover_pending
  tetap di atas. Bila belum ada topologi: kotak kosong dengan ajakan.
- **Server (daftar)**: tabel dengan nama tidak patah baris, lencana
  status di kolom sendiri, aksi di kanan; baris server lokal "vm"
  diberi label "server panel" bukan "Belum dipasangkan".
- **Detail server**: kepala dengan nama, peran, alamat, status besar
  (gauge kecil atau lencana), aksi; kartu MySQL: baris "Layanan"
  menyebut apa yang diperiksa ("layanan systemd mysql: tidak ditemukan;
  proses mysqld: berjalan") alih-alih "berhenti" merah ketika MySQL
  jelas menjawab. Tanpa perubahan agent: baris itu menggabungkan
  `ServiceActive` (unit systemd) dengan `MySQLOK` dari laporan
  kesehatan: "MySQL menjawab · layanan systemd `mysql` tidak aktif"
  berwarna netral, bukan merah; merah hanya bila MySQL tidak menjawab.
  Tombol "Siapkan sebagai master" pada server yang sudah master
  (`PrepareAgain`, aksi `prepare_master_again`) tetap ada karena itu
  satu-satunya cara menyiapkan ulang, tetapi dipindah ke kelompok
  "Tindakan lanjutan" yang dilipat dengan keterangan "Siapkan ulang
  (akun replikasi, pengaturan binlog)". "Detail teknis" dalam kotak
  Space Mono yang bisa dilipat.
- **Master (topologi)**: diagram topologi sebagai kartu (seperti
  dashboard) + tabel replika + aksi; tombol "Promote…" dengan teks mode.
- **Replika**: status, lag, cakupan database, aksi; kondisi `needs_*`
  sebagai banner warna peringatan/bahaya dengan tombol yang sudah ada.
- **Ganti master (promote)**: seperti mockup D: kartu "Apa yang akan
  terjadi" dengan tiga angka (laporan master, jeda tulis, kandidat
  paling mutakhir) dan daftar urutan langkah; tabel kandidat dengan
  kolom lengkap termasuk "Bisa dipilih?" yang terbaca penuh, lencana di
  bawah nama (tidak menimpa), baris terpilih berlatar `primary` 5%;
  kotak peringatan `error` + tombol "Promote" (nama tombol tidak
  berubah; ia membuka dialog konfirmasi yang sudah ada, yang tombolnya
  tetap "Konfirmasi"). Mockup D memakai teks "Lanjutkan ke konfirmasi";
  teks itu tidak dipakai. Replika tidak
  terjangkau, bercabang, dan sebagian ditampilkan dengan lencana
  warning/error dan centang persetujuan yang sudah ada.
- **Job**: langkah sebagai garis waktu vertikal dengan ikon status,
  log dalam kotak Space Mono, tombol Ulangi/Batal/Setujui sesuai status.
- **Pengalihan (cutover)**: dua kolom koneksi (master lama | master
  baru) sebagai tabel ringkas yang menyegarkan tiap 15 detik, lencana
  "akun admin panel", tombol "Sudah saya alihkan" `primary`.
- **Master lama**: kotak "Master lama" sebagai kartu bertepi `warning`
  dengan hasil "Periksa data tertinggal" sebagai tabel, daftar berkas
  salvage dengan tombol unduh/hapus, tombol Bangun ulang (sekunder) dan
  Hapus (`error`).
- **Peringatan**: filter sebagai chip; dropdown "Jangan ganggu" dengan
  panah yang tidak menimpa teks; tabel dengan lencana tingkat.
- **Audit**: tabel dengan aksi diterjemahkan lewat peta label (semua
  aksi yang ada: login, logout, promote, cutover_confirmed,
  salvage_download, dll.); kolom Detail menampilkan ringkasan
  terbaca ("kandidat: replika-a; terima sebagian: ya") dengan JSON
  mentah di `<details>`; filter tanggal memakai `<input type="date">`
  dengan format tampilan lokal `dd-mm-yyyy`; status rantai sebagai
  lencana + kalimat caveat yang ada.
- **Pengaturan**: bagian yang ada (Telegram, Ambang dan interval,
  Ringkasan mingguan, Alamat dasar panel, Retensi, Sertifikat) masing-
  masing menjadi kartu dengan grid 2 kolom yang tidak melampaui lebar;
  label dan teks bantu tidak bertumpuk; bagian baru "Tampilan":
  `nama_instansi` (ditampilkan di sidebar, ≤ 60 karakter) dan
  `tema_bawaan` (ikuti sistem / gelap / terang) sebagai nilai awal
  sebelum pengguna memilih sendiri di peramban; Retensi bertambah
  `samples_retention_days`.
- **Login**: kartu di tengah latar `base-200`, logo Bungee, form; tanpa
  sidebar.
- **Sertifikat (/sertifikat)**: halaman publik tanpa sidebar, kartu
  langkah bernomor (ini urutan sungguhan).

### 5.7 Data untuk widget dashboard

- Grafik lag membutuhkan riwayat. Tabel baru `health_samples`
  (migrasi 0010): `server_id, ts, lag_sec (NULL bila
  `SecondsBehind` nil), io_running TEXT (`Yes`/`No`/`Connecting`, apa
  adanya dari laporan), sql_running TEXT, mysql_ok INTEGER`, satu baris
  per laporan kesehatan yang diterima monitor, termasuk laporan backlog
  (waktunya dari `TakenAgoMS`) sehingga celah saat panel offline terisi;
  `INSERT OR IGNORE` pada kunci `(server_id, ts)`; dipangkas oleh
  goroutine retensi harian yang ada dengan `samples_retention_days`
  (bawaan 7, rentang 1–90, validasi sendiri karena pengaturan retensi
  lain memakai 1–3650).
- Kepadatan data (keputusan pemilik produk, bagian 8): agent hari ini
  melapor hanya saat status berubah atau ringkasan jatuh tempo
  (bawaan 120 detik, 10–3600), dan pergerakan lag di bawah ambang tidak
  dilaporkan; jadi tanpa perubahan agent grafik bersifat jarang.
- Kueri grafik: 24 jam terakhir, dibucket 5 menit (`MAX(lag_sec)` per
  bucket), per replika topologi; ≤ 288 titik per seri; dihitung saat
  halaman dibuka (SQLite, < 10 ms untuk 3 replika × 7 hari).
- Kelas CSS yang dipancarkan dari Go (`severityClass`, `statusBadges`,
  `toneOf`, dan daftar `@source inline` di `app.css`) dipetakan ulang ke
  token tema baru bersama template; tema `corporate` dan `data-theme`
  yang ditulis tetap di `layout.html` dihapus.
- Gauge disk memakai field disk yang sudah ada di `HealthSnapshot`;
  gauge lag memakai lag tertinggi di antara replika topologi dan ambang
  dari pengaturan; gauge peringatan memakai hitungan terbuka dan judul
  peringatan terbaru.
- Tidak ada polling baru: halaman dashboard tetap statis dengan tombol
  "Periksa sekarang"; status baris tetap lewat SSE yang ada.

### 5.8 Perbaikan cacat dari tur tangkapan layar (wajib)

1. Layar promote: lencana tidak menimpa nama; semua kolom terbaca pada
   1280 px tanpa terpotong; tabel lebar bergulir pada layar lebih kecil.
2. Pengaturan: tidak ada gulir horizontal pada 1280 px; input tidak
   keluar dari kartu; teks bantu tidak bertumpuk.
3. Detail server: baris "Layanan" menjelaskan pemeriksaan dan tidak
   menandai merah ketika MySQL menjawab; tombol "Siapkan sebagai master"
   tidak ditawarkan pada master.
4. Dashboard: status replika dan "tertinggal N transaksi" tidak
   bertentangan. Perubahan hanya pada tampilan (`views_monitor.go`,
   `statusView`), bukan pada kode status monitor `StatusSynced` yang
   dipakai peringatan, Telegram, dan ringkasan mingguan: bila status
   Sinkron tetapi detail menyebut transaksi tertinggal, label yang
   ditampilkan menjadi "Mengejar" dengan detail yang sama.
5. Audit: aksi diterjemahkan, Detail terbaca, tanggal lokal.
6. Panah dropdown tidak menimpa teks; nama server dan lencana tidak
   patah baris bila ruang cukup.

### 5.9 Aksesibilitas dan kualitas

- Kontras 4.5:1 untuk semua teks (3:1 ≥ 24 px) di kedua tema; diuji
  dengan skrip yang menghitung rasio dari CSS akhir untuk setiap
  pasangan token.
- Fokus keyboard terlihat; semua tombol `<button>`/`<a href>`; ikon
  dekoratif `aria-hidden`; gauge dan grafik `role="img"` + `aria-label`.
- `prefers-reduced-motion` menonaktifkan transisi.
- Lebar 390 px (telepon) dan 1280 px (laptop kecil) tanpa gulir
  horizontal halaman; tabel lebar bergulir dalam kotaknya.

### 5.10 Verifikasi

- Tur tangkapan layar (`TestE2EScreenshotTour`) diperluas: kedua tema
  dan dua lebar (1440 dan 390) untuk 15 halaman yang ada + Replikasi;
  hasil sebelum/sesudah disimpan sebagai bukti review (tidak dikomit).
- Tes kontras token (unit, pada `app.css` yang dibangun atau pada
  tabel token Go yang menjadi sumbernya).
- Tes template: semua halaman merender dalam tata letak baru; tes web
  yang ada tetap lulus; e2e Rencana 3–4c tetap lulus (nama tombol dan
  teks keputusan tidak berubah).
- Audit keamanan ringan: CSP tidak dilonggarkan; tidak ada inline
  script/style; `theme.js` hanya membaca/menulis `localStorage.tema`
  dengan nilai terbatas; tidak ada aset eksternal.

## 6. Yang diserahkan kepada pelaksana

- Pembagian komponen template (partial) dan penamaan kelas utilitas.
- Cara membuat subset WOFF2 (pyftsubset atau unduhan subset dari
  sumber resmi) dan penempatan berkas; catatan lisensi OFL di
  `web/static/fonts/LICENSE`.
- Bentuk persis kueri bucket SQLite dan tipe Go untuk seri grafik.
- Urutan migrasi halaman (disarankan: layout + tema + komponen →
  dashboard → promote/cutover/master lama → sisa halaman → pengaturan
  dan audit → tur tangkapan layar dua tema).

## 7. Risiko dan mitigasi

- Perubahan kulit menyentuh semua template sekaligus: risiko regresi
  teks/alur. Mitigasi: e2e dan tes web yang ada harus lulus tanpa
  mengubah asersi teks; tur tangkapan layar diperiksa reviewer.
- Warna neon di latar gelap bisa melelahkan bila dipakai luas.
  Mitigasi: neon hanya pada aksen (busur gauge, tombol utama, titik
  status); teks tetap putih pudar/abu.
- Tabel `health_samples` menambah tulisan SQLite tiap laporan (≤ 1 baris
  per detik per server). Mitigasi: `INSERT OR IGNORE`, indeks
  `(server_id, ts)`, pemangkasan harian.
- Bungee tidak punya huruf Indonesia khusus yang bermasalah (Latin
  dasar), tetapi tidak cocok untuk teks panjang: dibatasi H1 dan logo.

## 8. Keputusan pemilik produk (2026-11-09)

- **8.1 Kepadatan grafik lag: (b) sampel dari agent.** Agent mengirim
  event ringan `monitor.sample` pada setiap pemeriksaan kesehatan
  (interval pemeriksaan yang ada, 10–60 detik): `{ts, lag_sec|null,
  io_running, sql_running, mysql_ok}`; ukurannya ≤ 200 byte. Panel
  menyimpannya ke `health_samples` (5.7) dan tidak memakainya untuk
  status, peringatan, atau Telegram (laporan kesehatan tetap sumber
  kebenaran untuk itu). Event ini melewati ember token laporan yang
  ada (10 token, isi ulang 1 per 2 detik) karena 1 event per 10 detik
  per server berada jauh di bawah laju isi ulang; event yang dibuang
  hanya mengurangi titik grafik. Agent lama (tanpa event ini) tetap
  bekerja; grafiknya memakai laporan kesehatan saja dan halaman
  menyebut "grafik jarang: agent perlu dimutakhirkan". Alasan: grafik
  berlubang lebih menyesatkan daripada tidak ada grafik; pembaruan
  agent terjadi bersama rilis ini.
- **8.2 Dashboard beberapa topologi: (b) gauge agregat + tab per
  topologi.** Empat gauge menjumlahkan semua topologi (replika sinkron
  total/semua, lag tertinggi di antara semua replika dengan nama
  replikanya, disk master terburuk dengan nama masternya, peringatan
  terbuka global). Grafik lag dan kartu topologi berada dalam tab per
  topologi (tab tersembunyi bila hanya satu topologi, sehingga tampilan
  sama persis dengan mockup D). Pilihan tab disimpan di `localStorage`.
  Alasan: bentuk tidak berubah untuk pemakai satu topologi, dan
  informasi terburuk tetap terlihat tanpa berpindah tab.
