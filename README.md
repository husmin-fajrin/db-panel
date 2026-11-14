# dbpanel

dbpanel adalah panel web untuk mengelola replikasi MySQL antar-server di
jaringan lokal, ditujukan bagi tim IT rumah sakit yang ingin pemasangan
semudah aaPanel: satu perintah untuk panel, satu perintah untuk tiap server
database. Satu binary `dbpanel` menjalankan dua peran: **panel** (UI web
lewat HTTPS, port 8080) dan **agent** (berjalan di tiap server database, terhubung ke
panel lewat mTLS di port 8443). Server database tidak perlu akses internet;
agent diunduh langsung dari panel.

Kebutuhan server: Linux amd64/arm64 dengan `systemd`, `curl` dan `sudo`.

## Pasang panel

Ganti `main` dengan branch/tag rilis yang dipakai.
```sh
curl -fsSL https://raw.githubusercontent.com/husmin-fajrin/db-panel/main/scripts/install.sh | sudo bash
```

Skrip mengunduh `SHA256SUMS` dan binary dari GitHub Releases, memeriksa
binary dengan `SHA256SUMS` (berhenti dengan "Checksum binary panel tidak
cocok" bila berbeda), memasangnya ke `/usr/local/bin/dbpanel`, membuat
pengguna sistem `dbpanel` (tanpa shell login) dan direktori panel
`/etc/dbpanel/panel/` serta `/var/lib/dbpanel/panel/` (0700, milik
`dbpanel`), memasang dan menyalakan service `dbpanel-panel` (berjalan sebagai
`dbpanel`, bukan root) lalu service `dbpanel-agent` untuk host panel sendiri,
dan mencetak alamat panel, pengguna dan sandi awal. Agent host panel adalah
agent biasa yang terpisah dari panel: saat start, panel menulis token
pemasangan untuk servernya sendiri (berlaku 15 menit), skrip menyerahkannya
ke agent, dan agent berpasangan sendiri lewat `https://127.0.0.1:8080`
(lihat "Keamanan" untuk tata letak berkas).
Rilis tertentu: tambahkan `DBPANEL_VERSION=v0.1.0` sebelum `bash`
(`... | sudo DBPANEL_VERSION=v0.1.0 bash`).

**Tanpa internet (offline):** salin binary (dari halaman rilis, atau
`dist/dbpanel` hasil `make build`) dan `scripts/install.sh` ke server, lalu:

```sh
sudo DBPANEL_BIN=/path/ke/dbpanel bash install.sh
```

**Binary agent untuk arsitektur lain:** panel selalu bisa membagikan
binary-nya sendiri ke server dengan arsitektur yang sama. Pemasangan online
juga mengunduh `dbpanel_linux_amd64` dan `dbpanel_linux_arm64` dari rilis
yang sama dan mencocokkannya dengan `SHA256SUMS` rilis itu: binary yang
tidak tercantum di `SHA256SUMS` dibuang dengan peringatan, dan binary yang
tidak cocok membatalkan penyalinan binary agent (lihat "Keamanan rilis"
untuk batas pemeriksaan ini). Dengan begitu server arm64 bisa
dipasangkan ke panel amd64 dan sebaliknya; bila unduhan itu gagal, skrip
hanya memberi peringatan. Untuk pemasangan offline, berikan direktori berisi
`dbpanel_linux_amd64` dan/atau `dbpanel_linux_arm64`. Berkas ini dibuat oleh
`make release` (`make build` hanya membuat `dist/dbpanel`):

```sh
make release   # dist/dbpanel_linux_amd64, dist/dbpanel_linux_arm64, dist/SHA256SUMS
sudo DBPANEL_BIN=dist/dbpanel_linux_amd64 DBPANEL_BIN_DIR=dist bash scripts/install.sh
```

Berkas `dbpanel_linux_amd64`/`dbpanel_linux_arm64` disalin ke
`/var/lib/dbpanel/panel/bin/` (nama lain diabaikan). Skrip aman
dijalankan ulang, juga untuk memperbarui panel (lihat "Pembaruan").

**HTTPS sejak pasang.** Panel langsung memakai HTTPS di port 8080, tanpa
langkah tambahan: ia membuat sertifikat keamanannya sendiri saat start
pertama. Alamat `http://` lama tetap bisa dibuka; browser dialihkan ke
`https://` alamat yang sama.

Saat pertama membuka `https://<ip-panel>:8080`, browser memperingatkan
bahwa sambungan "tidak aman" atau "tidak pribadi", karena sertifikat panel
belum dikenal komputer itu. Terima peringatan itu sekali, buka
`https://<ip-panel>:8080/sertifikat` (tanpa login), unduh sertifikat
keamanan panel, **cocokkan sidik jari SHA-256-nya** dengan yang dicetak
`install.sh` di akhir pemasangan (juga tampil di **Pengaturan >
Sertifikat keamanan panel** setelah login), baru pasang, lalu buka panel
lagi. Halaman itu menjelaskan langkahnya, termasuk cara melihat sidik
jari, untuk Windows, macOS, Firefox, Android, dan iPhone/iPad. Cukup sekali
per komputer atau ponsel; sesudahnya browser membuka panel tanpa
peringatan. Sertifikat itu dipasang sebagai penerbit tepercaya: bila sidik
jarinya berbeda, jangan pasang (ada yang mengganti berkasnya di jaringan).
Halaman `/sertifikat` juga bisa dibuka lewat `http://` untuk browser yang
menolak HTTPS, tetapi isinya (sidik jarinya juga) tidak terlindung di
jaringan; cocokkan selalu dengan keluaran `install.sh` atau Pengaturan.
Untuk banyak komputer lewat Group Policy, salin `web-ca.crt` langsung dari
host panel (SSH atau flashdisk) dan cocokkan sidik jarinya, jangan dari
unduhan browser.

Simpan bookmark panel dengan `https://`. Browser hanya mengingat "selalu
HTTPS" (HSTS) untuk nama DNS, tidak untuk alamat IP: alamat `http://`
dengan IP selalu terbuka tanpa enkripsi dulu sebelum dialihkan, dan orang
lain di jaringan bisa menyela di situ (mis. menampilkan halaman login
palsu). Untuk perlindungan penuh, buka panel lewat nama DNS
(`--public-host panel.rsud.local` atau **Nama dan alamat tambahan**)
dengan `https://`.

## Login pertama

Buka `https://<ip-panel>:8080` dan masuk sebagai `admin` (peringatan
browser? lihat "HTTPS sejak pasang" di atas). Sandi awal
dicetak di akhir instalasi dan tersimpan di
`/etc/dbpanel/panel/admin-initial-password` (hanya bisa dibaca root dan
pengguna `dbpanel`):

```sh
sudo cat /etc/dbpanel/panel/admin-initial-password
```

Hapus berkas itu setelah login pertama (`sudo rm
/etc/dbpanel/panel/admin-initial-password`).

Lupa sandi? Buat sandi baru (dicetak ke layar), sebagai pengguna panel
supaya berkas database tetap miliknya:

```sh
sudo -u dbpanel dbpanel panel reset-password
```

`reset-password` juga mengakhiri semua sesi yang sedang login (dalam
beberapa detik, tanpa restart panel). Sesi berakhir setelah 12 jam tanpa
aktivitas dan paling lama 24 jam setelah login.

Login dibatasi 5 percobaan per alamat IP dan 20 sandi salah per nama
pengguna dalam 15 menit (sandi salah di konfirmasi kata sandi ikut
dihitung), dan hanya beberapa pemeriksaan sandi yang berjalan
bersamaan (selebihnya "Server sibuk, coba lagi sebentar"). Bila akun
terkunci, misalnya karena orang lain sengaja salah sandi berulang kali,
penghitung itu ada di memori panel: `sudo systemctl restart dbpanel-panel`
menghapusnya (semua sesi yang sedang login juga berakhir).

## Tampilan

Panel punya dua tema: **gelap** (tema utama: latar hampir hitam, aksen neon
hanya pada busur gauge, tombol utama dan titik status) dan **terang**.
Tombol **Ganti tema** di menu samping (kiri; pada layar sempit di balik
tombol **Menu** di atas) berpindah tema dan pilihannya disimpan di peramban
itu saja. Peramban yang belum pernah memilih memakai **Pengaturan >
Tampilan > Tema bawaan**: Ikuti sistem (bawaan: mengikuti mode gelap/terang
sistem operasi), Gelap, atau Terang. Setelah peramban memilih tema lewat
**Ganti tema**, belum ada pilihan untuk kembali ke "ikuti sistem" di
peramban itu (tombolnya hanya berganti antara gelap dan terang); hapus data
situs panel di peramban untuk kembali ke tema bawaan. Di bagian yang sama **Nama instansi**
(paling panjang 60 karakter) tampil di bawah logo di menu samping;
kosongkan untuk menyembunyikannya.

Huruf ikut di dalam binary (subset Latin WOFF2, lisensi SIL OFL 1.1, lihat
`web/static/fonts/LICENSE`): Bungee untuk logo dan judul halaman, Work Sans
untuk teks, Space Mono untuk angka, alamat dan waktu. Panel tidak memuat
apa pun dari internet, jadi tampilannya sama di jaringan tertutup.

Dashboard dibuka dengan empat gauge (replika sinkron, lag tertinggi, disk
master terburuk, peringatan terbuka) dan grafik lag 24 jam per replika;
isinya dijelaskan di [Pemantauan](#pemantauan). Titik grafik berasal dari
sampel lag yang dikirim agent pada setiap pemeriksaan kesehatan (paling
banyak satu per 5 detik per server disimpan) dan disimpan 7 hari, bisa
diubah di **Pengaturan > Retensi**. Semua halaman bisa dipakai di layar
selebar 390 px (telepon) tanpa menggeser halaman ke samping; tabel yang
lebar bergulir di dalam kotaknya sendiri.

Isian tanggal (misalnya **Sejak**
di halaman Audit) memakai pemilih tanggal bawaan peramban, yang mengikuti
bahasa dan wilayah peramban atau sistem operasi: di peramban berbahasa
Inggris (AS) 2 November tampil sebagai 11/02/2026. Atur bahasa peramban
atau sistem operasi ke Indonesia agar urutannya hari/bulan/tahun.

## Tambah server database

1. Di panel buka **Server > Tambah server**, isi nama, simpan.
2. Salin perintah pemasangan yang ditampilkan, misalnya

   ```sh
   f=$(mktemp) && curl -fsSL -k --pinnedpubkey sha256//<pin> --max-redirs 0 -K - <<<'url = "https://192.168.1.10:8080/agent/install?token=..."' -o "$f" && echo "<sha256 skrip>  $f" | sha256sum -c - && sudo bash "$f"
   ```

   Perintah ini terdiri dari tiga langkah:
   1. `curl` mengunduh skrip pemasang lewat HTTPS ke berkas sementara baru dari
      `mktemp` (nama acak, hanya bisa dibaca pemiliknya; nama tetap di
      `/tmp` bisa disiapkan lebih dulu oleh pengguna lain di server itu
      lalu diubah tepat sebelum `sudo`). Server database belum mengenal
      sertifikat panel, jadi `-k` mematikan pemeriksaan penerbitnya;
      sebagai gantinya `--pinnedpubkey` hanya menerima kunci sertifikat
      panel itu sendiri (pin `sha256//...`). Mesin lain di jaringan yang
      menjawab atas nama panel ditolak sebelum token terkirim. `curl` tidak
      mengikuti pengalihan (`--max-redirs 0`), agar token tidak ikut
      terkirim ke host lain;
   2. `sha256sum -c` mencocokkan isi skrip dengan sha256 yang ditampilkan
      panel (dihitung dari skrip yang sama persis yang disajikan untuk token
      itu);
   3. hanya bila cocok, skrip dijalankan dengan `sudo`.

   Pin dan hash saling melengkapi: pin memastikan yang menjawab adalah
   panel, hash memastikan skrip yang dijalankan sebagai root adalah skrip
   yang ditampilkan panel. Skrip yang sudah terverifikasi membawa pin yang
   sama (`WEB_PIN`), yang dipakai untuk mengunduh binary agent dan
   diberikan ke `dbpanel agent pair` lewat variabel lingkungan
   `DBPANEL_WEB_PIN`; skrip itu juga membawa
   sha256 binary agent (unduhan yang tidak cocok ditolak dengan "Checksum
   binary agent tidak cocok; kemungkinan ada yang menyusup di jaringan") dan
   sidik jari CA panel, yang diberikan ke `dbpanel agent pair --ca-sha256`:
   agent menolak jawaban pairing dengan CA lain ("CA panel tidak cocok dengan
   yang diharapkan") dan menghubungi alamat agent dari skrip
   (`--agent-addr`), bukan dari jawaban pairing. Unduhan binary dan
   `dbpanel agent pair` juga tidak mengikuti pengalihan (redirect). Hash di perintah berubah
   bila token, alamat panel, CA, sertifikat panel, atau binary agent di panel berubah; bila
   `sha256sum -c` gagal tanpa alasan jelas, muat ulang halaman server lalu
   salin perintah yang baru.

   Perintah ini butuh shell bash (atau zsh): URL bertoken diberikan ke
   `curl` lewat here-string (`<<<`), bukan sebagai argumen, sehingga token
   tidak terlihat di `ps`. Perintah tetap tercatat di riwayat shell; awali
   dengan spasi bila `HISTCONTROL` berisi `ignorespace`/`ignoreboth`
   (bawaan Ubuntu), atau jalankan `export HISTCONTROL=ignorespace` dulu.
3. Jalankan perintah itu di server database. Binary agent diunduh dari
   panel (tidak perlu internet) dan diperiksa sha256-nya, service
   `dbpanel-agent` dipasang, dan agent dipasangkan dengan token sekali pakai
   serta CA panel yang dipin.
4. Server tampil online di panel beserta info sistem dan MySQL.

Server database harus bisa menjangkau panel di port 8080 (unduhan dan
pairing) dan 8443 (koneksi agent). Bila pairing gagal (token kedaluwarsa
atau sudah dipakai), buat token baru dari halaman server lalu ulangi.

**Pasang ulang agent:** bila server database dipasang ulang atau isi
`/etc/dbpanel` di server itu hilang, buka halaman server lalu klik **Pasang
ulang agent**. Panel membuat token baru dan menampilkan perintah pemasangan;
jalankan perintah itu di server tersebut. Agent lama tetap bekerja sampai
agent baru berhasil dipasangkan; saat itu koneksi agent lama langsung
diputus dan sertifikatnya tidak diterima lagi. Ini juga cara mencabut
sertifikat agent yang kuncinya diduga bocor. Server panel sendiri tidak perlu
dipasangi agent dari halaman server: `install.sh` memasang dan
memasangkan agent-nya (service `dbpanel-agent`), dan server itu tidak bisa
dihapus. Bila isi `/etc/dbpanel` di host panel hilang, jalankan ulang
`install.sh`: tanpa `/etc/dbpanel/agent.yaml` skrip melepas agent lama
(`dbpanel panel reset-local-agent`) dan agent host panel berpasangan lagi
sebagai server yang sama (riwayat dan pemantauannya tetap).

**Hapus server:** koneksi agent-nya langsung diputus dan sertifikatnya
ditolak. Service `dbpanel-agent` di server itu tetap terpasang; matikan
dengan `sudo systemctl disable --now dbpanel-agent`.

## Alamat panel untuk agent

Panel menebak IP-nya sendiri (IPv4 non-loopback pertama) untuk perintah
pairing dan alamat yang dihubungi agent. Bila tebakan salah (mis. server
punya beberapa kartu jaringan), jalankan panel dengan `--public-host`:

```sh
sudo systemctl edit dbpanel-panel
# isi:
# [Service]
# ExecStart=
# ExecStart=/usr/local/bin/dbpanel panel --public-host 192.168.1.10
sudo systemctl restart dbpanel-panel
```

Nilai `--public-host` yang pernah diberikan disimpan di database panel dan
tetap dipakai (mengalahkan deteksi otomatis) pada start berikutnya,
walaupun flag tidak diberikan lagi.

Agent menghubungi panel lewat HTTPS. Perintah pemasangan di halaman server
selalu memuat alamat `https://` dan pin kunci sertifikat panel
(`--pinnedpubkey sha256//...`); perintah lama yang masih berisi `http://`
tidak berlaku lagi (tokennya pun sudah kedaluwarsa), jadi salin perintah
baru dari halaman server. Pairing manual:

```sh
sudo DBPANEL_TOKEN=... DBPANEL_WEB_PIN='sha256//...' dbpanel agent pair \
  --panel https://192.168.1.10:8080 --ca-sha256 <sidik jari CA>
```

Pin ada di **Pengaturan > Sertifikat keamanan panel** (juga `WEB_PIN=` di
skrip pemasang). Tanpa pin, `agent pair` menolak panel HTTPS yang
sertifikatnya tidak dikenal mesin itu.

Sertifikat panel memuat alamat panel (`--public-host`), nama host mesin
(bila berupa nama lengkap dengan titik), dan alamat kartu jaringan panel.
Panel dibuka dengan nama atau alamat lain (mis. nama DNS kantor)? Tambahkan
di **Pengaturan > Sertifikat keamanan panel > Lanjutan > Nama dan alamat
tambahan**.

## Replikasi MySQL

Alurnya: **Hubungkan MySQL** di setiap server, **Siapkan sebagai master**
di server sumber, lalu **Buat replika** dari halaman master. Setiap alur
adalah pekerjaan bertahap di **Pekerjaan**; tahap yang butuh persetujuan
menunggu tombol Anda dan menjelaskan akibatnya.

### Hubungkan MySQL

Di halaman server, masukkan akun admin MySQL di server itu. Pakai akun
admin khusus untuk panel (misalnya `panel_admin`; jangan diawali
`dbpanel_`, yang dipakai akun buatan panel), bukan `root` atau akun yang
juga dipakai orang atau aplikasi: layar pengalihan aplikasi
(lihat "Pengalihan aplikasi") tidak menampilkan sesi akun yang dipakai
agent bila sesi itu tersambung dari server itu sendiri (socket,
`localhost`, `127.0.0.1`, `::1`), dan sesinya dari host lain hanya
ditandai "akun admin panel". Kosongkan host agar agent memakai socket
lokal (disarankan). Agent hanya mau tersambung ke MySQL di server itu
sendiri: host kosong atau `localhost`, alamat loopback (`127.0.0.1`,
`::1`), alamat salah satu antarmuka jaringan server itu, atau nama yang
semua alamatnya termasuk itu. Host lain ditolak dengan "MySQL yang
disambungkan harus berada di server ini (soket, 127.0.0.1, atau alamat
server ini sendiri); <host> bukan alamat server ini." Akun tersimpan dari
versi lama yang menyebut host lain tidak dipakai: setiap akses agent ke
MySQL gagal dengan teks yang sama, dan laporan pemantauan server itu
membawanya. Perbaikannya: **Hubungkan MySQL** lagi dari panel dengan
host kosong atau alamat server itu sendiri.

Nama host yang tidak bisa dicari tetap ditolak, dengan "alamat MySQL
<host> tidak bisa dicari (DNS): <sebab>. MySQL yang disambungkan harus
berada di server ini (soket, 127.0.0.1, atau alamat server ini sendiri)."
(sebabnya "tidak ada alamat" bila nama itu tidak punya alamat sama
sekali); periksa DNS server itu. Agent mencari nama itu sekali dan
tersambung ke alamat yang sudah diperiksa. Setelah agent dimutakhirkan,
server yang akun tersimpannya menyebut alamat yang tidak ada di salah satu
antarmuka server itu sendiri (misalnya alamat NAT atau alamat publik)
gagal di setiap akses MySQL dengan teks "… bukan alamat server ini." di
atas, termasuk penjaga karantina, sampai Anda menjalankan **Hubungkan
MySQL** lagi dari panel dengan host lokal; pada server yang dikarantina
akun itu harus punya semua hak yang diperiksa panel (lihat "Peran dan
sumber yang dipin").

Agent menyimpan akun itu tersegel di
`/etc/dbpanel/mysql.cred` di server itu; panel tidak menyimpannya dan
tidak menampilkannya lagi. Bila akun kurang hak, panel menampilkan
perintah `GRANT` yang perlu dijalankan.

Contoh akun khusus (jalankan sebagai `root` di MySQL server itu, sesuaikan
sandinya; hak di bawah adalah daftar yang diperiksa panel):

```sql
CREATE USER 'panel_admin'@'localhost' IDENTIFIED BY '<sandi>';
GRANT ALL ON *.* TO 'panel_admin'@'localhost' WITH GRANT OPTION;
```

Hak global (`ON *.*`) yang diperiksa untuk akun admin (`ALL ... WITH GRANT
OPTION` di atas sudah memegang semuanya):

- `CREATE USER, RELOAD, PROCESS, SELECT, LOCK TABLES, SHOW VIEW, TRIGGER,
  EVENT, REPLICATION CLIENT, REPLICATION SLAVE` dengan `WITH GRANT OPTION`;
  server replika juga `DROP, CREATE, INSERT, UPDATE, DELETE, ALTER, INDEX,
  CREATE VIEW, CREATE ROUTINE, ALTER ROUTINE, REFERENCES, CREATE TEMPORARY
  TABLES` (untuk memuat dump).
- `SYSTEM_VARIABLES_ADMIN, REPLICATION_SLAVE_ADMIN, CLONE_ADMIN,
  PERSIST_RO_VARIABLES_ADMIN` dan `CONNECTION_ADMIN` (dipakai `ALTER
  INSTANCE RELOAD TLS` saat panel memasang sertifikat MySQL).
- `BACKUP_ADMIN` dan, di MySQL 8.0.31 ke atas, `FLUSH_TABLES`, keduanya
  `WITH GRANT OPTION`, karena agent memberikannya ke akun yang dibuatnya:
  `BACKUP_ADMIN` ke akun replikasi (untuk CLONE), `FLUSH_TABLES` ke akun
  dump sementara (lihat "Akun yang dibuat panel di master").

Kata sandi MySQL yang Anda ketik melintas jaringan lewat HTTPS
(terenkripsi).

### Siapkan master

Panel memeriksa server, lalu mengaktifkan binary log, GTID, format binlog
ROW, `server_id` yang unik, dan plugin CLONE, serta membuat akun replikasi
`dbpanel_repl`. Yang bisa berubah tanpa restart langsung diterapkan. Nama
dan masa simpan binary log yang sudah ada dipertahankan.

- **Restart:** bila binary log masih mati atau MySQL hanya mendengar di
  `127.0.0.1`, MySQL harus direstart. Pilih **Restart MySQL di master
  sekarang** (aplikasi terputus beberapa detik; lakukan di jam sepi), atau
  restart sendiri nanti lalu klik **Sudah saya restart — periksa lagi**:
  panel membaca nilai yang berlaku dan tidak merestart lagi.
- **Format binlog:** bila sebelumnya `MIXED`/`STATEMENT`, koneksi aplikasi
  yang sudah terbuka tetap memakai format lama sampai tersambung ulang.
  Restart aplikasi (atau MySQL) di jam sepi sebelum membuat replika.
- **Firewall:** master yang tadinya hanya mendengar di localhost akan
  mendengar di semua alamat (`bind_address=0.0.0.0`). Buka port MySQL
  master hanya untuk IP replika, misalnya:

  ```sh
  sudo ufw allow from 192.168.1.21 to any port 3306 proto tcp
  # atau firewalld:
  sudo firewall-cmd --permanent --add-rich-rule='rule family=ipv4 source address=192.168.1.21 port port=3306 protocol=tcp accept'
  sudo firewall-cmd --reload
  ```
- **Alamat untuk replika:** setiap replika harus bisa menjangkau port
  MySQL master di alamat yang tertera di halaman master. Panel memakai
  alamat tempat agent master terhubung; bila master punya beberapa kartu
  jaringan, di balik NAT, atau dihubungi lewat VPN, ubah lewat **Ubah
  alamat untuk replika**. Alamat itu diperiksa dari server replika saat
  replika berikutnya dibuat.
- **Sertifikat MySQL:** tahap **Pasang sertifikat MySQL** memberi MySQL
  master sertifikat dari panel (lihat "Sertifikat MySQL" di bawah), tanpa
  restart. Sertifikat itu memuat alamat server di panel dan alamat untuk
  replika; bila alamat untuk replika diubah ke nama atau IP lain, klik
  **Perbarui sertifikat MySQL** di halaman server master agar alamat baru
  ikut tercantum.

### Buat replika

- **Server tujuan khusus:** server tujuan dijadikan **hanya-baca penuh**
  (`read_only` + `super_read_only`): tidak ada akun yang bisa menulis,
  termasuk admin; hanya replikasi yang mengubah data. Aplikasi boleh
  membaca dari replika, tidak boleh menulis. Database lama di server
  tujuan dibuang (panel selalu meminta persetujuan dan menyebut namanya).
  Bila server tujuan sudah mereplikasi dari server lain, panel menyebut
  sumber itu dan baru menghapus pengaturannya setelah Anda setujui.
- **Cara menyalin:** semua database ke server kosong memakai CLONE (cepat);
  sebagian database, atau **Paksa dump**, memakai mysqldump. Saat dump
  dimulai, master dikunci sesaat (`FLUSH TABLES WITH READ LOCK`); kueri
  panjang di master membuat kunci itu menunggu dan penulisan aplikasi ikut
  tertahan. Buat replika dan tambah database **di jam sepi**. Selama data
  dimuat, replika sementara bisa ditulis; jangan arahkan aplikasi ke sana.
- **`lower_case_table_names`** master dan replika harus sama. Nilai ini
  hanya bisa diatur saat MySQL pertama kali dipasang; bila berbeda, pasang
  ulang MySQL di server tujuan dengan nilai master, atau pilih server lain.
- **Akun MySQL setelah CLONE:** CLONE (dan dump semua database) menyalin
  juga akun-akun MySQL master ke replika. Akun admin yang tersimpan untuk
  replika (misalnya `dbpanel_admin` buatan panel) tidak berlaku lagi, dan
  pekerjaan menunggu di tahap **Salin data**. Buka halaman server replika,
  klik **Hubungkan MySQL**, masukkan akun admin MySQL milik **master**
  (akun yang sama dengan di Hubungkan MySQL master), lalu kembali ke
  halaman pekerjaan dan klik **Sudah saya hubungkan ulang — lanjutkan**.
  Data tidak disalin ulang. Sejak itu akun-akun di replika sama dengan di
  master.
- **TLS:** bila master menawarkan TLS (MySQL 8 membuat sertifikatnya
  sendiri), replikasi, CLONE dan dump berjalan terenkripsi dan akun
  replikasi hanya bisa masuk lewat TLS.
- **Sertifikat MySQL dan verifikasi:** setelah data tersalin, tahap
  **Pasang sertifikat MySQL** memberi replika sertifikatnya sendiri dan
  berkas CA MySQL panel (`<datadir>/dbpanel-tls/mysql-ca.crt`). Bila master
  punya sertifikat dari panel (selalu, untuk master yang disiapkan dengan
  versi ini), replika memverifikasinya: replikasi memakai
  `SOURCE_SSL_CA` dan `SOURCE_SSL_VERIFY_SERVER_CERT=1` (sertifikat harus
  diterbitkan CA MySQL panel dan memuat alamat master), dan dump memakai
  `--ssl-mode=VERIFY_IDENTITY`. Jadi penyerang di jaringan tidak bisa
  menyamar sebagai master. **CLONE hanya memeriksa rantai sertifikat**
  (`clone_ssl_ca`: sertifikat donor harus diterbitkan CA MySQL panel),
  tidak memeriksa nama host, karena MySQL tidak menyediakan pemeriksaan
  nama untuk CLONE; sertifikat dari CA itu hanya dimiliki server yang
  dikelola panel. Bila master belum punya sertifikat dari panel (topologi
  lama), replikasi tetap terenkripsi tanpa verifikasi, log pekerjaan
  menulis "Sambungan replikasi belum diverifikasi: master belum punya
  sertifikat panel", dan halaman master menampilkan peringatan itu sampai
  **Perketat sambungan replikasi** dijalankan.

### Sertifikat MySQL

Panel punya penerbit sertifikat sendiri untuk MySQL (CA MySQL,
`/etc/dbpanel/panel/mysql-ca.crt` dan `mysql-ca.key`, dibuat saat panel pertama
start, masa 10 tahun), terpisah dari CA agent. Tiap server mendapat
sertifikat sendiri (masa 2 tahun) yang hanya memuat nama yang panel
ketahui sendiri: alamat server di panel dan, untuk master, alamat untuk
replika. Agent membuat kuncinya di server itu
(`<datadir>/dbpanel-tls/server.key`, folder 0700 dan berkas 0600 milik
user `mysql`); kunci tidak pernah meninggalkan server. Agent (root)
membuka folder itu sekali dan menulis, mengganti nama, serta mengatur izin
lewat descriptor-nya, tidak lewat path: tautan yang dipasang pemilik
datadir tidak pernah diikuti. MySQL memuat
sertifikat baru tanpa restart (`ALTER INSTANCE RELOAD TLS`, butuh hak
`CONNECTION_ADMIN`), lalu agent mencoba sambungan baru yang memeriksa
sertifikat itu; bila gagal, berkas lama dikembalikan dan pekerjaan gagal.
Agent melaporkan nomor seri sertifikat yang dilihatnya disajikan MySQL,
dan panel hanya mencatatnya bila sama dengan yang diterbitkannya (bila
tidak, pekerjaan gagal tanpa mencatat apa pun).

- **Siapkan master**, **Buat replika**, dan **Resync ulang** memasang
  sertifikat di server yang disiapkan (tahap **Pasang sertifikat MySQL**).
- **Pasang sertifikat MySQL** di halaman server (dengan konfirmasi kata
  sandi) memasang atau memperbarui sertifikat server yang sudah ada.
  Halaman server menampilkan nomor seri dan masa berlakunya.
- **Perketat sambungan replikasi** di halaman master (dengan konfirmasi
  kata sandi) untuk topologi yang dibuat sebelum versi ini, atau bila ada
  replika yang belum memverifikasi master: panel memasang sertifikat di
  master, lalu untuk tiap replika memasang sertifikatnya dan menyambungkan
  ulang replikasinya dengan verifikasi (replikasi berhenti beberapa detik
  dan selama itu replika tampil **Dihentikan**, tanpa peringatan; bila
  replikasi gagal dijalankan lagi, replika tetap Dihentikan sampai
  **Ulangi** atau **Start replika**; replika yang dihentikan lewat **Stop
  replika** tetap berhenti). Replika
  yang sedang tidak terjangkau atau punya pekerjaan lain dilewati
  ("replika tidak terjangkau; ulangi nanti") tanpa menggagalkan
  pekerjaan; jalankan lagi nanti. Replika baru ditandai terverifikasi
  setelah thread IO-nya terlihat tersambung dengan
  `SOURCE_SSL_VERIFY_SERVER_CERT=1` (panel menunggu paling lama 1 menit;
  bila belum, tahap itu gagal dan bisa di-**Ulangi**; bila thread IO
  menolak sertifikat master, replikasi langsung dikembalikan tanpa
  verifikasi). **Buat replika** dan pengarahan ulang saat promote memakai
  aturan yang sama. Setelah itu tandanya mengikuti laporan kesehatan replika
  siap yang agent-nya sama versinya dengan panel: ditandai saat thread IO
  tersambung dengan verifikasi ke sumber topologi dan master punya
  sertifikat panel (misalnya replika yang dihentikan operator lalu
  dijalankan lagi), dan dicabut bila laporan menunjukkan verifikasi mati. Kotak ini tampil selama master belum
  punya sertifikat atau ada replika dengan peringatan **Sambungan
  replikasi belum diverifikasi**.
- **Perpanjangan otomatis:** panel memeriksa setiap hari (pertama kali 10
  menit setelah start) dan menjalankan **Pasang sertifikat MySQL** sendiri
  untuk sertifikat yang habis dalam 30 hari, bila agent-nya terhubung dan
  server itu tidak sedang menjalankan pekerjaan lain. Bila sertifikat
  tinggal kurang dari 14 hari, peringatan **Sertifikat MySQL hampir
  kedaluwarsa** ("Sertifikat MySQL <nama> kedaluwarsa dalam N hari",
  tingkat peringatan, ikut dikirim ke Telegram) terbuka sampai
  sertifikatnya diperpanjang. Sertifikat keamanan panel (web)
  memperbarui dirinya sendiri; lihat "Keamanan". Sertifikat HTTPS yang
  sedang dipakai panel (biasanya sertifikat sendiri yang diunggah) yang
  tinggal kurang dari 14 hari membuka peringatan **Sertifikat HTTPS panel
  hampir kedaluwarsa** di server host panel.
- **Alamat master berubah.** Sertifikat MySQL master hanya memuat alamat
  saat diterbitkan. Setelah **Ubah alamat untuk replika** ke alamat yang
  belum dimuat, halaman master meminta **Perbarui sertifikat MySQL** dulu,
  dan **Buat replika**/**Resync ulang** menolak di tahap pertama
  ("Alamat master berubah ...") sampai sertifikatnya diperbarui.
- **Sertifikat klien MySQL.** Sertifikat panel mengganti `ssl_ca` MySQL.
  Bila ada akun yang masuk dengan sertifikat klien (`REQUIRE X509`,
  `ISSUER` atau `SUBJECT`) dan `ssl_ca` saat ini berbeda, agent menolak
  memasang sertifikat panel dan menyebut akun-akun itu: aplikasi itu akan
  terkunci bila `ssl_ca` diganti. Server seperti itu tetap tanpa sertifikat
  panel (replikasinya tidak diverifikasi) sampai akun itu tidak lagi
  memakai sertifikat klien.

### Akun yang dibuat panel di master

- `dbpanel_repl@'%'`: hanya `REPLICATION SLAVE` dan `BACKUP_ADMIN` (untuk
  CLONE), sandi acak 24 karakter. Sandinya tersimpan di setiap replika di
  tabel `mysql.slave_master_info`; **jangan beri akun di replika
  `SELECT ON *.*`** (misalnya akun laporan), pakai grant per database.
- `dbpanel_dump_<8 heksa>@'<alamat replika>'`: akun sementara untuk satu
  dump (baca data, kunci singkat), dibuat sesaat sebelum dump dan dihapus
  sesudahnya, juga bila dump gagal atau dibatalkan. Di MySQL 8.0.31 ke atas
  akun ini mendapat `FLUSH_TABLES`, bukan `RELOAD` (lihat "Keamanan"). Bila
  penghapusan gagal, log pekerjaan menyebut perintah `DROP USER` yang perlu
  dijalankan; agent juga menyapu sendiri, tiap jam, akun dump yang berumur
  lebih dari 6 jam dan tidak sedang dipakai. Sandi akun dump kedaluwarsa 1 hari setelah dibuat
  (`PASSWORD EXPIRE INTERVAL 1 DAY`).

Agent hanya mau membuat, mengubah, atau menghapus akun yang namanya
diawali `dbpanel_`, dan menolak perintah yang menghapus data, menimpa
server, atau mengubah replikasi di server yang sedang dibaca replika lain
(master), apa pun yang diminta panel. Di master yang dipin agent tidak
pernah mengubah akun yang sudah ada (sandi dan haknya tetap). Bila "Siapkan
master" dijalankan ulang untuk topologi baru sementara `dbpanel_repl` masih
ada dari persiapan sebelumnya, pekerjaan berhenti dan menyebut perintah
`DROP USER` yang perlu dijalankan di master (hanya bila tidak ada replika
lain yang memakainya), dengan host baris akun yang memang ada (misalnya
`'dbpanel_repl'@'10.0.0.%'`, tidak selalu `'%'`). Dua permintaan pembuatan
akun replikasi yang datang bersamaan dijalankan agent satu per satu, jadi
yang kedua menemukan akun yang baru dibuat dan tidak mengubahnya.

### Kebijakan lokal agent

Operator di server database bisa menonaktifkan perintah tertentu untuk
agent di server itu, misalnya di master produksi. Buat
`/etc/dbpanel/agent-policy.yaml` (milik root, mode 0644 atau 0600, tidak
bisa ditulis grup atau pengguna lain):

```yaml
deny:
  - mysql.dropDatabases
  - mysql.dumpFrom
  - mysql.cloneFrom
  - mysql.resetReplica
  - mysql.configureReplication
```

lalu `sudo systemctl restart dbpanel-agent`. Perintah yang ditolak dijawab
"perintah dinonaktifkan oleh kebijakan lokal server ini". Berkas yang
izinnya longgar membuat agent menolak berjalan (lihat
`journalctl -u dbpanel-agent`). Agent mencatat setiap perintah yang
diterimanya (nama, parameter dengan sandi disamarkan, hasil, durasi) di
journal.

### Peran dan sumber yang dipin

Agent mengingat peran servernya di `/etc/dbpanel/role` (`master`,
`replica`, atau `quarantined`) dan master yang diikuti replika di `/etc/dbpanel/source`
(`host:port`). Berkas peran ditulis saat "Siapkan master" atau "Buat
replika" pertama berhasil; berkas sumber saat replikasi atau salinan data
(dump, CLONE) pertama kali diarahkan ke master. Sesudah itu agent sendiri
menolak perintah panel yang tidak cocok, walau panelnya disusupi: master
menolak hapus database, salin data dari server lain, dijadikan atau
dikonfigurasi sebagai replika, dikunci read-only, membuat akun selain akun
replikasi `dbpanel_repl` dan akun dump sementara `dbpanel_dump_<8 heksa>`
dari satu alamat IP, mengubah akun yang sudah ada, mengganti `server_id`,
dan restart MySQL kecuali sekali sesudah pengaturan yang memang menunggu
restart; replika menolak buka kunci, dijadikan master, akun replikasi
selain `dbpanel_repl@'%'` (dan akun itu pun hanya bila belum ada baris
`dbpanel_repl` di host mana pun), dan master selain sumbernya.

Peran **quarantined** dipakai untuk master lama sesudah promosi replika:
servernya menolak semua yang ditolak replika, ditambah perintah promosi
(`mysql.lockForPromote`, `mysql.promote`, `mysql.repoint`,
`mysql.unlockPromote`) dan pelepasan peran (`mysql.releaseRole`). Ia hanya
bisa dibangun ulang (rebuild: hapus database, dump atau CLONE, dikonfigurasi
sebagai replika, replikasi) dari sumber yang dipin, yaitu master barunya.
Bangun ulang itu juga butuh persetujuan root di server itu (lihat "Mode
ketat" di bawah). Panel tidak bisa melepas server ini seperti replika. Karantina
hanya berakhir lewat bangun ulang yang selesai, atau dengan `set-role` di
server itu sendiri. Bila panel mencoba melepasnya, agent menjawab "server
ini dikarantina (quarantined); perintah mysql.releaseRole ditolak.
Karantina hanya lepas lewat bangun ulang yang selesai, atau dengan perintah
di server ini: sudo dbpanel agent set-role unassigned (atau sudo dbpanel
agent set-role master bila server ini akan menjadi master lagi)". Agent
tidak pernah menurunkan pin `quarantined` (atau pin lain) berdasarkan
keadaan MySQL.

**Mode ketat (strict mode).** Di server yang dikarantina (quarantined),
agent hanya menjalankan perintah panel yang tidak membuang data MySQL.
Tanpa persetujuan, yang tetap jalan:

- membaca keadaan: pemantauan, status MySQL, daftar database, koneksi,
  pemeriksaan awal, binlog, dan status serta log pekerjaan agent;
- penyelamatan: **Periksa data tertinggal** (`mysql.leftover`),
  **Amankan data tertinggal** (`mysql.salvage`), serta daftar, **Unduh**
  dan **Hapus** berkas salvage;
- menyimpan berkas CA panel (`mysql.tlsTrust`). **Periksa data
  tertinggal** dan **Amankan data tertinggal** memakainya untuk memeriksa
  sertifikat master baru, dan penyelamatan harus bisa jalan tanpa
  persetujuan. Perintah ini hanya menulis berkas CA dan tidak menyentuh
  data;
- yang hanya mengencangkan: membuat MySQL baca-saja (read-only)
  (`mysql.setReadOnly`; membukanya tetap ditolak), menghentikan replikasi
  (`mysql.stopReplica`), dan membatalkan pekerjaan agent (`job.cancel`);
- **Hubungkan MySQL** (`mysql.connect`): hanya menyimpan akun yang terbukti
  bisa masuk ke MySQL di server itu dan punya semua hak yang diperiksa
  panel (lihat "Hubungkan MySQL"). **Periksa data tertinggal** butuh akun
  yang berfungsi, dan penjaga karantina butuh hak itu untuk memasang
  baca-saja lagi. Akun yang kurang hak ditolak dan akun yang tersimpan
  tetap dipakai: "server ini dikarantina (quarantined): akun <akun> belum
  punya semua hak yang dibutuhkan; jalankan perintah GRANT berikut sebagai
  root lalu periksa ulang: <perintah GRANT, dipisah titik koma>";
- karantina ulang di bawah sumber yang sama (`mysql.quarantine`),
  pencatatan replika (`mysql.recordReplicas`), pembacaan sumber yang dipin
  (`mysql.probeSource`, `mysql.waitSynced`), dan pertanyaan form bangun
  ulang tentang persetujuan (`mysql.rebuildApproval`).

Yang butuh persetujuan root di server itu: semua langkah bangun ulang (hapus
database, dump, CLONE, konfigurasi dan filter replikasi, start dan reset
replikasi, pengaturan dan restart MySQL), membuat akun MySQL, menghapus
akun dump sementara (`dbpanel_dump_<8 heksa>`), serta kunci dan sertifikat
MySQL (`mysql.tlsRequest`, `mysql.tlsInstall`). Dua hal tetap ditolak
walau ada persetujuan: menghapus akun selain akun dump sementara (seperti
di master; misalnya akun replikasi), dan memasang MySQL (`mysql.install`;
master lama sudah punya MySQL). Penolakannya "server ini dipin sebagai quarantined; perintah
<perintah> ditolak. Untuk mengubah peran jalankan di server ini: sudo
dbpanel agent set-role <peran>". Tanpa persetujuan yang berlaku, agent menolak dengan
kode `needs_local_approval` dan teks "server ini dikarantina (quarantined)
mengikuti sumber <host:port>; perintah <perintah> butuh persetujuan root di
server ini. Jalankan di server ini: sudo dbpanel agent approve <kode> (kode
berlaku <N> menit lagi)". Cara menyetujui ada di "Persetujuan di server
untuk bangun ulang" (bagian "Master lama" di "Promote"). Perintah promosi
dan pelepasan peran tidak pernah diberi kode: tabel peran menolaknya, apa
pun persetujuannya. Server yang dikarantina tanpa pin sumber menolak
perintah yang butuh persetujuan dengan "server ini dipin sebagai
quarantined tanpa sumber yang dipin; perintah <perintah> ditolak. Pin master
barunya dulu di server ini: sudo dbpanel agent set-source <host:port>".

Bila berkas pin tidak bisa dibaca, di server mana pun, hanya perintah dari
daftar "tetap jalan" di atas yang dijalankan. Perintah lain ditolak dengan
"pin peran/sumber agent tidak bisa dipakai, perintah <perintah> ditolak:
<sebab>". Pertanyaan form bangun ulang (`mysql.rebuildApproval`) juga
dijawab dengan teks itu, bukan "persetujuan tidak diperlukan": server itu
mungkin dikarantina.

Karena memasang sertifikat butuh persetujuan, panel tidak memasang
sertifikat MySQL baru di server yang dikarantina. Perpanjangan sertifikat
harian dan **Perketat** melewatinya; selama server itu masih menjalankan
pekerjaan (misalnya bangun ulang), **Perketat** ditolak seperti untuk
replika lain yang sibuk ("replika <server> masih punya pekerjaan yang
belum selesai"). Memasang sertifikat MySQL di sana, atau
mengulang pekerjaan sertifikatnya, ditolak dengan "Server ini dikarantina
(quarantined); sertifikat MySQL-nya dipasang lagi saat dibangun ulang
sebagai replika." Sertifikat baru dipasang saat server itu dibangun ulang.

Hanya di server `quarantined`, panel bisa melihat transaksi master lama
yang tidak dimiliki master barunya (`mysql.leftover`: jumlah transaksi,
tabel dan jumlah baris, rentang waktu, tanpa isi data) dan mengekstraknya
ke berkas SQL (`mysql.salvage`) di `/var/lib/dbpanel/salvage`
(direktori 0700, berkas 0600, paling besar 2 GiB, dihapus otomatis
setelah 7 hari) untuk diunduh dan dijalankan ulang secara manual. Pada
format binlog STATEMENT atau MIXED, literal string yang memuat baris
berakhiran `/*!*/;` bisa mengacaukan ringkasan `mysql.leftover` (jumlah dan
rentang waktu); logging berbasis baris (ROW), yang diwajibkan panel, tidak
terpengaruh.

Pin **master** tidak bisa diubah dari panel. Peran **replika** bisa
dilepas panel sendiri, setelah replikasinya dihentikan dan direset, lewat
**Lepas dan hentikan replikasi**, **Hapus server**, atau saat pekerjaan
**Buat replika** dibatalkan. Server itu kembali tanpa peran (`role` berisi
`unassigned`), tetapi **pin sumbernya tetap**: panel masih hanya bisa
mengarahkan replikasi, dump, atau CLONE ke master yang sama. Perintah ke
master lain ditolak dengan pesan "server ini dipin mengikuti sumber
`<host:port>`; … jalankan di server ini: sudo dbpanel agent set-source
<host:port>". Jadi panel yang disusupi tidak bisa memakai pelepasan peran
untuk mengarahkan replika ke master pilihannya; ia masih bisa menimpa data
server yang dilepas dari master yang sama. Peran dan sumber hanya diubah di
server itu sendiri:

```sh
sudo dbpanel agent set-role master       # atau replica, quarantined, atau unassigned (hapus peran dan sumber)
sudo dbpanel agent set-source 10.0.0.5:3306
```

Baris bantuan untuk `set-role` dan `approve` (`dbpanel agent help`):

```text
  dbpanel agent set-role {master|replica|quarantined|unassigned} [--ya] [--dir DIR]
                                                         ubah peran yang dipin; --ya: tanpa pertanyaan
                                                         pada server yang dikarantina (quarantined)
  dbpanel agent approve <kode> [--dir DIR]               setujui bangun ulang server yang dikarantina;
                                                         kode dari form "Bangun ulang sebagai replika"
  dbpanel agent approve batal [--dir DIR]                hapus persetujuan dan permintaan bangun ulang
```

**`set-role` di server yang dikarantina bertanya dulu.** Ini berlaku untuk
`set-role master`, `replica` dan `unassigned` di server yang dipin
`quarantined`, dan di server yang berkas pinnya tidak bisa dibaca (server
itu mungkin dikarantina). Perintah menampilkan keadaan server itu (sumber
yang diikutinya, atau sebab pinnya tidak bisa dibaca) dan apa yang berubah
setelahnya, lalu menulis "Ketik ya untuk melanjutkan, atau apa saja untuk
membatalkan:". Ketik `ya` lalu Enter untuk melanjutkan. Jawaban lain, atau
input kosong, membatalkan: perintah menulis "Dibatalkan; peran tidak
diubah." dan keluar dengan kode 1. Tambahkan `--ya` untuk melewati
pertanyaan itu, misalnya di skrip:

```sh
sudo dbpanel agent set-role unassigned --ya
```

Bila peran berubah selagi pertanyaan ditampilkan, perintah berhenti dengan
"Peran server ini berubah selama Anda membaca; jalankan perintah ini
lagi.". `set-role quarantined` tidak bertanya.

Setiap `set-role` lebih dulu menghapus permintaan dan persetujuan bangun
ulang (`/etc/dbpanel/rebuild-request` dan `/etc/dbpanel/rebuild-approval`).
`set-role master` juga menghapus pin sumber (`/etc/dbpanel/source` dan
`/etc/dbpanel/source-addr`): master tidak mengikuti server lain.

`set-role master` berjalan dalam dua langkah. Langkah pertama melepas kunci
start (start lock) master lama (lihat "Master lama" di "Promote"). Langkah
kedua baru mengubah peran. Untuk langkah pertama perintah hanya mencari
mysqld dan berkas konfigurasinya; ia tidak memakai kunci rahasia agent
(`secrets.key`) atau akun MySQL yang disimpan agent (`mysql.cred`). Hasil
langkah pertama salah satu dari tiga ini, dan ketiganya lanjut mengubah
peran:

- "Kunci start dilepas: baris init_file dan berkas kuncinya dihapus dari
  konfigurasi MySQL."
- "Kunci start tidak ditemukan; tidak ada yang dilepas."
- "MySQL tidak ditemukan di server ini; tidak ada kunci start yang perlu
  dilepas."

Bila mysqld ada tetapi konfigurasinya tidak bisa dibaca, perintah berhenti
dan peran tidak diubah: "kunci start tidak bisa diperiksa (<sebab>); peran
tidak diubah. Bila agent tidak bisa dijalankan, lepas dengan tangan: lihat
README, bagian "Melepas kunci start dengan tangan"." Bila kunci ada tetapi
gagal dilepas, perintah juga berhenti: "kunci start tidak bisa dilepas
(<sebab>); peran tidak diubah". Bila barisnya sudah terhapus tetapi
berkasnya belum, perintah berhenti dengan "Baris init_file sudah dihapus
dari konfigurasi MySQL, tetapi berkas kunci start tidak bisa dihapus
(<sebab>); peran tidak diubah. Jalankan perintah ini lagi."; jalankan
`set-role master` sekali lagi. Bila kunci sudah dilepas tetapi peran gagal
ditulis, perintah menulis "Kunci start sudah dilepas, tetapi peran belum
diubah. Jalankan perintah ini lagi; sampai itu MySQL tidak dipaksa
baca-saja (read-only) saat menyala." Artinya server itu masih dipin
`quarantined`, tetapi MySQL-nya bisa menyala tanpa kunci start. Agent yang
berjalan menulis kunci itu lagi selama perannya `quarantined`. Jalankan
`set-role master` sekali lagi.

`set-role replica` atau `unassigned` tidak melepas kunci start: agent yang
berjalan melepasnya sendiri setelah MySQL hidup dan `read_only` serta
`super_read_only` tersimpan ON, jadi server itu tetap menyala baca-saja.
Sesudah itu agent tidak lagi memasang baca-saja di server `unassigned`:
siapa pun yang mematikannya (juga panel, lewat perintah yang membuka
baca-saja) membuat MySQL-nya bisa ditulis.
`set-role quarantined` membuat agent yang berjalan menulis kunci start
dalam 5 detik.

Master bisa dikunci dan dibuka langsung di servernya:

```sh
sudo dbpanel agent lock-master     # SET PERSIST super_read_only=ON + kunci promosi
sudo dbpanel agent unlock-master   # RESET PERSIST, read_only OFF, hapus kunci promosi
```

`lock-master` membuat MySQL read-only, juga sesudah restart, dan menulis
`/etc/dbpanel/promote-lock` tanpa kandidat promosi (`"candidate":"cli"`);
selama berkas itu ada, master yang dipin menolak dibuka kuncinya dari panel,
dan tidak ada proses otomatis yang membukanya. `lock-master` menolak bila
kunci sudah ada; `unlock-master` aman diulang dan hanya berjalan di server
yang dipin `master` (atau server `unassigned` yang dikunci dengan
`lock-master`): replika dan server `quarantined` tidak pernah dibuat bisa
ditulis olehnya. Catatan promosi terakhir
disimpan di `/etc/dbpanel/promoted`. Kedua berkas ini ditulis dan dibaca
seperti berkas pin.

Panel mengunci master untuk promosi dengan `mysql.lockForPromote`: hanya
pada server yang dipin `master`, hanya bila kandidat saat ini membaca binlog
master (alamat IP kandidat sama dengan host thread `Binlog Dump`), dan
hanya bila belum ada kunci. Kandidat yang tersambung ke master lewat NAT
atau proxy (alamat thread `Binlog Dump` bukan alamat kandidat) tidak bisa
dipakai untuk mengunci. Kunci itu `/etc/dbpanel/promote-lock` berisi
`host:port` kandidat dan tenggat (60 sampai 3600 detik), ditulis lebih
dulu, lalu `SET PERSIST super_read_only=ON` (bila MySQL menolaknya, berkas
kunci dihapus lagi). Sebelum menulis kunci, agent harus bisa membaca
kandidat dengan akun replikasi yang dikirim panel (bila tidak, master tidak
dikunci dan promote gagal di awal), lalu membaca semua replika yang pernah
tersambung ke master ini (host dari berkas replika di bawah, pada port
kandidat dan setiap port di daftar port replika; paralel, 5 detik per bacaan) dan
menyimpan yang terbaca bersama kunci. Pembacaan itu dibatasi: paling banyak
64 pasangan host × port, selesai dalam 60 detik (jauh di bawah batas tunggu
panel); lebih dari itu, atau daftar port replika yang ditandai penuh (lihat di
bawah), kunci ditolak dengan pesan yang menyebut berkas daftar dan perintah
untuk merapikannya. Replika yang sudah tidak terbaca saat
itu (replika sebagian tanpa akun replikasi, replika yang lama mati)
dilewati dan tidak menahan kunci. Selama berkas kunci ada di master, agent
memasang lagi `super_read_only` tiap 5 detik bila mati (misalnya sesudah
MySQL restart). Pada tenggat agent membaca kandidat
dengan akun replikasi yang dikirim panel (hanya disimpan di memori): bila
kandidat bisa ditulis dan `gtid_executed`-nya memuat UUID server ini,
master mengkarantina dirinya (`quarantined`, sumber = kandidat); bila
kandidat masih read-only, agent menunggu 60 detik dan membaca lagi; bila masih
read-only, agent membaca lagi setiap replika yang tersimpan bersama kunci
(dengan akun yang sama): bila salah satunya bisa ditulis dan
`gtid_executed`-nya memuat UUID server ini (replika lain yang dipromosikan
darurat selama kunci berdiri), master mengkarantina dirinya di bawah
replika itu. Bila salah satunya **tidak bisa dibaca**, kunci
**dipertahankan**: master tetap baca-saja, agent membaca lagi tiap menit,
dan panel membuka peringatan Kritis **Kunci promote master dipertahankan**
yang menyebut replika itu. Kunci terbuka sendiri begitu semua replika itu
terbaca dan baca-saja (atau bisa ditulis tetapi tanpa transaksi server ini). Bila replika itu memang sudah tidak dipakai,
jalankan `forget-replica` (di bawah) di server master; untuk membuka kunci
saat itu juga, `unlock-master` (pastikan dulu replika itu tidak sedang
menjadi master di sisi jaringan lain). Jadi master bisa tertahan baca-saja
selama replika yang terbaca saat kunci dipasang tidak terbaca: aplikasi
tidak bisa menulis sampai itu selesai (pilihan ini menukar ketersediaan
dengan menutup jalan dua master). Selain itu (kandidat tidak terjangkau, atau
agent dimulai ulang sehingga akunnya hilang) kunci tetap dan dicoba lagi
tiap menit, sampai `unlock-master`. Kunci yang dipertahankan dilaporkan ke
panel saat pertama kali, langsung lagi setiap kali daftar replika yang tidak
terbaca berubah, dan selain itu paling sering sejam sekali; laporan yang
tidak sampai ke panel dicoba lagi tiap menit. `mysql.unlockPromote`
membatalkan kunci sebelum tenggat hanya bila kandidat terbaca masih
read-only dan setiap replika yang tersimpan bersama kunci terbaca: replika
yang tidak terbaca menahan kunci, dan replika yang sudah menjadi master
yang bisa ditulis dengan transaksi server ini membuat master mengkarantina
dirinya, seperti penjaga. Kunci `lock-master` tidak
pernah disentuh keduanya. Bila pemeriksaan agent jatuh tepat di antara
langkah `unlock-master` (MySQL dibuka, lalu berkas kunci dihapus), MySQL
bisa terkunci lagi; jalankan `unlock-master` sekali lagi.

Agent juga mencatat replika yang membaca binlog server ini tiap 5 detik
(loop penjaga, lihat "Kecepatan" di "Pemantauan") di
`/var/lib/dbpanel/replicas-seen`: satu baris per
host, `<host> <waktu RFC3339>`, dengan waktu terbaru; host yang tidak
terlihat lebih dari setahun dibuang. Host diambil dari kolom `HOST` daftar
proses MySQL tanpa port, jadi berupa nama bila `skip_name_resolve` mati.
Port replika (dari `SHOW REPLICAS`) dimasukkan dengan cara yang sama ke daftar port replika di
`/var/lib/dbpanel/replica-ports-seen`, paling banyak 8 port (port dipilih
replika sendiri lewat `report_port`): port berikutnya tidak masuk daftar, berkas
ditandai penuh (baris `penuh <waktu>`), agent menulis peringatan di log, dan
promote terencana ditolak sampai root merapikannya dengan `forget-replica-port`
(di bawah). Berkas daftar replika tidak pernah ditulis lebih besar dari yang
dibaca agent (1 MiB): host tertua dibuang lebih dulu dan dicatat di log.
`mysql.lockForPromote` mencatat
kandidat begitu melihat thread `Binlog Dump`-nya, dan panel meminta master
mencatat replikanya (`mysql.recordReplicas`, tanpa data: agent hanya
melihat koneksinya sendiri dan tidak pernah menerima host replika dari
panel) setelah replika terverifikasi tersambung di akhir **Buat replika**,
**Resync ulang**, **Tambah database**, dan pengarahan ulang. Yang belum
tertutup: master yang mati kurang dari 5 detik setelah replika pertama
tersambung di luar alur panel tidak mencatat replika itu, jadi promote
darurat ke replika itu tidak bisa mengkarantina master lama (agent master
lama menolak karantina di bawah host yang tidak pernah tercatat).

Replika yang sudah tidak dipakai dihapus dari daftar replika itu, dan dari kunci
promosi yang sedang berdiri, di server master:

```sh
sudo dbpanel agent forget-replica 10.0.0.7        # semua port host itu; nama dibandingkan tanpa beda huruf besar-kecil
sudo dbpanel agent forget-replica 10.0.0.7:3306   # sama; port dari peringatan diabaikan
sudo dbpanel agent forget-replica-port 3307       # hapus satu port dari daftar port replika
sudo dbpanel agent forget-replica-port semua      # kosongkan daftar port replika dan tanda penuhnya
```

Panel tidak punya padanannya. `forget-replica` tidak membuka kunci sendiri:
penjaga kunci membaca kandidat lagi pada pemeriksaan berikutnya (paling lama
semenit), lalu sekali lagi 60 detik kemudian, dan membuka master setelah
pembacaan kedua bila kandidat masih baca-saja dan replika lain yang tersimpan
bersama kunci terbaca baca-saja (atau bisa ditulis tanpa transaksi server
ini). Perintah ini menyebut yang dilewatinya: penjaga tidak lagi menunggu
host itu, jadi bila replika itu ternyata sudah menjadi master, master ini
bisa terbuka dan ada dua master. `forget-replica-port semua` hanya mengosongkan
daftar port replika; port replika yang sedang tersambung masuk daftar lagi dalam
beberapa detik. Periksa dulu isi `/var/lib/dbpanel/replica-ports-seen`
sebelum menjalankan `forget-replica-port semua`: port replika yang tidak
tersambung saat itu (misalnya replikasinya sedang dihentikan) ikut terlupa,
sehingga replika itu tidak dibaca pada kunci promosi berikutnya dan penjaga
tidak menunggunya.

**Saat replika dipensiunkan, jalankan `forget-replica` untuk host itu di
server master.** Daftar replika disimpan setahun, dan pada setiap promote
terencana agent master membaca setiap host di daftar replika pada setiap port
di daftar port replika dengan akun replikasi. Pada topologi tanpa verifikasi sertifikat
("Perketat" belum dijalankan) sandi akun itu dikirim ke siapa pun yang
menjawab di alamat dan port itu, termasuk perangkat yang kemudian memakai
alamat replika lama. "Perketat" menutup ini: tanpa sertifikat yang cocok
sandi tidak dikirim.

Agent membacanya ulang pada perintah berikutnya, tanpa restart. Bila alamat
master untuk replika diubah di halaman master, halaman itu menampilkan
perintah `set-source` untuk tiap replika. Pemasangan
agent tidak membuat berkas pin; server yang baru dipasang belum berperan
apa pun sampai salah satu alur di atas berhasil.

Agent yang dimutakhirkan dari versi lama di server yang sudah
dikonfigurasi menulis **berkas peran saja**, dan hanya dengan petunjuk
peran dari panel: master dipin bila panel mencatatnya sebagai master (sudah
"Siapkan master"), replika bila panel mencatatnya sebagai replika ("Buat
replika"); server lain tetap tanpa pin. Master yang masih punya baris
`SHOW REPLICA STATUS` tidak dipin otomatis; pin dengan `set-role master`.
Pin sumber replika seperti itu baru ditulis pada konfigurasi replikasi,
dump, atau CLONE berikutnya (misalnya **Resync ulang**). Berkas
pin harus milik root dan tidak bisa ditulis grup atau pengguna lain;
selama tidak demikian, perintah yang dijaga ditolak dengan pesan cara
memperbaikinya.

Setiap laporan pemantauan memuat pin itu. Halaman server menampilkannya di
**Detail teknis** ("Peran dipin: master", "Sumber dipin: 10.0.0.1:3306"),
juga alasan bila berkas pin tidak bisa dibaca (nama berkas tanpa
direktorinya). Master topologi yang melaporkan peran dipin selain `master`
(atau berkas pin yang tidak bisa dibaca) tiga laporan berturut-turut
memunculkan peringatan **Master tidak dipin** (tingkat peringatan); di
master seperti itu agent tidak menolak perintah yang bisa merusak
replikasi. Saran: jalankan ulang **Siapkan master** (perbaiki dulu berkas
pinnya bila tidak terbaca). Peringatan tertutup begitu laporan menyebut
pin `master`. Replika dan master lama yang dikarantina tidak pernah
mendapatkannya, begitu pula agent versi lama yang belum melaporkan pin.

### Cadangkan kunci panel

Sandi replikasi disimpan di `panel.db` tersegel dengan
`/etc/dbpanel/panel/panel.key`. **Cadangkan `/etc/dbpanel/panel/panel.key`
bersama `/var/lib/dbpanel/panel/panel.db`** di tempat aman di luar host panel. Tanpa
kunci itu sandi replikasi tidak bisa dibuka lagi: menyiapkan master ulang
membuat sandi baru, dan setiap replika harus dibuat ulang. Simpan kunci
terpisah dari cadangan database biasa; siapa pun yang memegang keduanya
bisa membuka sandi replikasi.

Cadangkan juga isi `/etc/dbpanel/panel/` lainnya (paling tidak
`mysql-ca.crt`/`mysql-ca.key` dan `web-ca.crt`/`web-ca.key`). Tanpa CA
MySQL yang sama, panel membuat CA baru: sertifikat MySQL yang diterbitkan
sesudahnya (juga perpanjangan otomatis) tidak dikenali replika yang sudah
ada, karena mereka memverifikasi master dengan CA lama. Tanpa CA web yang
sama, setiap komputer harus memasang ulang sertifikat keamanan panel.

### Memindahkan panel ke server lain

1. Hentikan panel: `sudo systemctl stop dbpanel-panel`.
2. Salin `/etc/dbpanel/panel` (termasuk `ca.crt`, `ca.key`, `panel.key`,
   `mysql-ca.*` dan `web-ca.*`) dan `/var/lib/dbpanel/panel` ke tempat yang
   sama di server baru. Panel di server baru menerbitkan ulang sertifikat
   HTTPS-nya untuk alamat baru; bila alamat itu di
   luar batas CA web, panel membuat CA web baru dan komputer pengguna perlu
   memasang ulang sertifikat keamanan panel (lihat "Keamanan").
3. Pasang panel di server baru (`install.sh`). Salin dulu ke tata letak
   lama (`/etc/dbpanel/*`, `/var/lib/dbpanel/*`) agar skrip memindahkan
   dan memberikannya ke pengguna `dbpanel`, atau langsung ke subfolder
   `panel/` lalu `sudo chown -R dbpanel: /etc/dbpanel/panel
   /var/lib/dbpanel/panel` sebelum menjalankan skrip. Server baru belum
   punya `/etc/dbpanel/agent.yaml`, jadi skrip melepas agent host panel
   lama dan agent server baru berpasangan sebagai server host panel yang
   sama. Jalankan panel dengan `--public-host <alamat baru>` bila perlu.
4. Di setiap server database, ubah `panel_addr` di
   `/etc/dbpanel/agent.yaml` ke `<alamat baru>:8443`, lalu
   `sudo systemctl restart dbpanel-agent`.
5. Matikan panel lama (`sudo systemctl disable --now dbpanel-panel`) dan
   hapus salinan kuncinya di sana.

## Pemantauan

Begitu sebuah server menjadi master atau replika, agent di server itu
memeriksa MySQL-nya sendiri dan melapor ke panel. **Dashboard** dibuka
dengan satu baris ringkasan (peringatan terbuka dan tindakan yang
menunggu Anda) dan kapan laporan terbaru masuk, lalu empat gauge yang
menjumlahkan semua topologi:

- **Replika sinkron**: berapa replika yang Sinkron dari semuanya.
- **Lag tertinggi**: lag terbesar di antara semua replika, dengan nama
  replikanya dan ambang lag dari Pengaturan (kuning bila mencapai ambang).
- **Disk master terburuk**: persen terpakai disk data master yang paling
  penuh, dengan nama master dan sisa ruangnya (kuning dan merah mengikuti
  batas disk di Pengaturan).
- **Peringatan terbuka**: jumlahnya dan judul peringatan terbaru (merah
  bila ada yang kritis).

Di bawahnya grafik lag 24 jam (titik per 5 menit, satu garis per replika,
garis putus-putus ambang) dan kartu topologi: master lalu setiap replika
satu baris (status berwarna, penanda kunci, tertinggal berapa, kapan
terakhir dicek), dengan tombol **Tambah replika** dan **Ganti master…**.
Bila ada lebih dari satu topologi, grafik dan kartunya dipisah per tab
(nama master); tab yang terakhir dipilih diingat oleh peramban. Paling
bawah tiga peringatan dan tiga pekerjaan terbaru. Baris status berganti
sendiri saat status berubah; angka gauge dan grafik diperbarui saat
halaman dibuka lagi (**Periksa sekarang** memuat ulang dashboard). Server
yang belum menjadi master atau replika tampil **Belum dipantau**.

Status replika, dalam kata sederhana:

| Status | Artinya |
|---|---|
| Sinkron | replikasi berjalan dan mengikuti master |
| Mengejar | dinilai Sinkron (di bawah ambang), tetapi masih ada transaksi master yang belum dijalankan; tampil bersama "tertinggal N transaksi". Peringatan, Telegram, dan ringkasan mingguan tetap menyebutnya Sinkron |
| Ketinggalan | replikasi berjalan tetapi tertinggal dari master (bawaan: 60 detik atau lebih) |
| Terputus | replika tidak tersambung ke master; MySQL mencoba menyambung sendiri (merah setelah 3 menit) |
| Dihentikan | replikasi dihentikan lewat panel (**Stop replika**) |
| Berhenti (di luar panel) | replikasi berhenti tanpa lewat panel, misalnya ada yang menjalankan `STOP REPLICA` |
| Berhenti | replikasi berhenti karena error; panel menyebut nomor error, artinya, dan sarannya |
| Tidak terjangkau | MySQL di replika tidak menjawab |
| Agent offline | agent tidak terhubung ke panel, atau laporannya terlambat |
| Menunggu laporan pertama | agent baru tersambung lagi setelah offline; laporan lama tidak dinilai sampai laporan baru masuk (paling lama selama batas laporan terlambat, sesudah itu Agent offline lagi); peringatan agent offline baru ditutup oleh laporan itu, dan promote master dalam status ini dianggap darurat |
| Agent perlu dimutakhirkan | agent versi lama yang belum bisa memantau (lihat "Pembaruan") |
| Belum siap | replika sedang dibuat atau disync ulang, atau pembuatannya gagal |

Penanda merah **Kunci terlepas** tampil di samping status apa pun bila
`super_read_only` di replika mati: aplikasi bisa menulis langsung ke
replika dan isinya bisa berbeda dari master.

Status master: **Sehat**, **Terkunci** (`super_read_only` menyala, aplikasi
tidak bisa menulis), **Tidak terjangkau**, **Agent offline**, **Menunggu
laporan pertama** (seperti di replika), dan **Diduga
tumbang** (agent atau MySQL master tidak menjawab lebih dari 30 detik).
Untuk master yang diduga tumbang, halamannya menampilkan langkah manual
"Yang bisa dilakukan sekarang"; panel belum bisa memindahkan peran master
sendiri.

Halaman master dan replika juga menampilkan versi, uptime, koneksi, disk,
RAM, beban, lag, umur binlog, dan garis waktu kejadian 7 hari (bisa diperluas ke
90 hari; riwayat disimpan 180 hari, lihat Retensi di bawah). Pesan error
MySQL selengkapnya (dengan kutipan data disensor, lihat di bawah) hanya ada
di **Detail teknis** halaman replika. **Detail teknis** juga menampilkan
pin peran dan sumber server itu (lihat "Peran dan sumber yang dipin") dan
jumlah laporan yang dibuang (lihat "Batas dari agent").

**Sensor kutipan data.** Pesan error MySQL sering mengutip isi baris
(`Duplicate entry 'Budi' for key 'nama'`). Sebelum disimpan atau
dikirim, panel mengganti setiap nilai berkutip (`'…'` atau `"…"`,
termasuk escape `\'` dan `''`) dengan `'…'`, jadi yang tersimpan adalah
`Duplicate entry '…' for key '…'`. Berlaku untuk error MySQL di laporan
agent (`mysql_error`, `last_io_error`, `last_sql_error`: snapshot,
garis waktu, peringatan, dan Telegram yang dibangun darinya), log dan
pesan pekerjaan (termasuk langkah promote dan arahkan ulang), galat
terakhir replika, rincian server di catatan promosi, serta hasil di
catatan audit bila berasal dari jawaban agent. Teks di luar kutipan tetap
utuh: nomor error, nama tabel, posisi binlog, dan angka panjang lain.
Untuk pesan MySQL yang mencetak nilai tanpa escape (`Duplicate entry`,
`Incorrect … value`, `Error '…' on query`), panel mencari penutup pesan
itu di seluruh pesan, jadi kutip atau baris baru di dalam nilai tidak
memotong sensornya. Pernyataan SQL setelah `Query: '`, teks setelah
`near '` (error sintaks), nilai `Truncated incorrect … value: '`, dan
pesan yang terpotong di tengah nilai disensor sampai akhir pesan. Kutipan
lain yang tidak ditutup menyensor sisa barisnya.

Yang *tidak* disensor: nama akun buatan panel beserta host-nya
(`'dbpanel_repl'@'10.0.0.%'`, `'dbpanel_dump_…'`) dan baris saran panel
yang memuatnya (perintah `DROP USER`/`GRANT` yang perlu dijalankan), karena
itu bukan data aplikasi; data yang ditulis MySQL tanpa tanda kutip; di
pesan yang polanya tidak dikenal panel, potongan nilai yang memuat kutip
diikuti spasi atau tanda baca (sensornya heuristik); dan
teks yang tersimpan sebelum versi ini (tidak diubah di basis data; snapshot
lama disensor saat dibaca ulang, tetapi garis waktu, peringatan, log
pekerjaan, dan catatan audit lama tetap seperti aslinya). Sensor terjadi di
panel: journal agent (`journalctl -u dbpanel-agent`) dan log error MySQL di
server database tetap memuat pesan asli, jadi batasi siapa yang bisa
membacanya di server itu.

**Beban.** Setiap agent hanya membaca MySQL di servernya sendiri: beberapa
kueri ringan (`SHOW GLOBAL STATUS`/`VARIABLES`, `SHOW REPLICA STATUS`) tiap
15 detik lewat satu koneksi yang dipakai ulang, tanpa menyentuh tabel
aplikasi. Panel tidak pernah tersambung ke MySQL; replika tidak menanyai
master. Agent hanya mengirim laporan saat ada yang berubah, dan ringkasan
tiap 2 menit.

Setelah tombol atau pekerjaan panel mengubah replika (Start, Stop, Resync
ulang, Buat replika), panel langsung meminta laporan baru dari agent dan
menilai statusnya lagi hanya dari laporan yang diambil sesudah perubahan
itu, jadi laporan lama tidak memicu peringatan palsu.

**Kecepatan.** Replika yang berhenti terlihat di panel dan dikirim ke
Telegram dalam waktu kurang dari 30 detik; master yang tumbang dalam
sekitar 1 menit. Interval, ambang, dan batas waktu itu bisa diubah di
**Pengaturan > Ambang dan interval** (interval pemeriksaan 10 sampai 300
detik, di panel dan di agent; nilai lebih besar yang tersimpan dari versi
lama dibaca 300, karena panel baru tahu master mati pada pemeriksaan
berikutnya; interval ringkasan dan ambang lag tetap 10 sampai 3600 detik).
Tombol **Periksa sekarang** di halaman server meminta laporan baru
saat itu juga (paling sering sekali per 10 detik).

**Loop penjaga.** Di samping pemeriksaan itu, agent menjalankan loop
penjaga dengan jeda tetap 5 detik setelah tiap putaran selesai (bukan
jadwal tetap, jadi putaran tidak bertumpuk), apa pun interval
pemeriksaannya; panel tidak bisa mengubahnya. Tiap putaran: memutus sesi
akun dump yang memegang kunci baca global terlalu lama (lihat "Keamanan"),
menjaga kunci promosi dan karantina beserta kunci start master lama (lihat
"Promote"), dan mencatat replika yang membaca binlog (lihat "Peran dan
sumber yang dipin"). Galat yang sama dari penjaga kunci promosi atau dari
pencatatan replika ditulis ke log paling sering sekali per menit.

**Batas dari agent.** Panel menerima paling banyak 10 laporan beruntun
dari satu server, lalu satu tiap 2 detik; laporan yang lebih sering dari
itu dibuang tanpa memutus sambungan, dicatat di log panel, dan jumlahnya
tampil di **Detail teknis** halaman server ("12 laporan dibuang dalam 1 jam
terakhir"). Laporan tertunda yang dikirim agent setelah tersambung lagi
(paling banyak 200) mendapat jatah tersendiri, sekali per 10 menit per
server, jadi agent yang sehat praktis tidak pernah mencapai batas ini;
laporan yang dibuang biasanya berarti agent rusak atau diubah. Satu
pesan dari agent (dan dari panel ke agent) paling besar 1 MiB; pesan yang
lebih besar memutus sambungan, dan agent tersambung lagi sendiri. Pesan
biasa jauh di bawah batas itu; hanya daftar yang sangat besar (sekitar
10.000 database atau tabel di satu server) yang bisa melewatinya.

**Retensi.** Sekali sehari panel menghapus peringatan yang sudah ditutup
lebih dari 90 hari dan riwayat status (linimasa) yang lebih tua dari 180
hari. Kedua angka bisa diubah di **Pengaturan > Retensi** (1 sampai 3650
hari). Peringatan yang masih aktif tidak pernah dihapus, dan log audit
serta log pekerjaan tidak pernah dipangkas.

**Sampel lag.** Untuk grafik lag di dashboard, agent mengirim sampel kecil
(lag, status thread IO dan SQL, MySQL terjangkau atau tidak) pada setiap
pemeriksaan, terlepas dari apakah laporan dikirim. Sampel hanya mengisi
grafik: status, peringatan, dan Telegram tetap dinilai dari laporan
kesehatan. Panel menyimpan paling banyak satu sampel per 5 detik per
server; sampel yang lebih sering dibuang dan ikut dihitung di **Detail
teknis**. Saat panel tidak terjangkau sampel tidak disimpan agent, tetapi
laporan tertunda tetap mengisi celahnya. Agent versi lama tidak mengirim
sampel; grafiknya hanya memakai laporan kesehatan sehingga jarang. Sampel
yang lebih tua dari 7 hari dihapus sekali sehari; angkanya bisa diubah di
**Pengaturan > Retensi** (1 sampai 90 hari).

## Notifikasi Telegram

Panel mengirim peringatan ke grup Telegram lewat bot milik Anda sendiri.

1. Di Telegram, buka chat dengan **@BotFather**, kirim `/newbot`, beri nama
   bot, lalu salin **token bot** yang diberikan (bentuknya
   `123456789:AAH...`). Perlakukan token seperti sandi.
2. Buat grup untuk tim IT (atau pakai yang sudah ada), tambahkan bot itu
   ke grup, lalu kirim pesan `/start` di grup.
3. Cari **chat id** grup: di browser Anda buka
   `https://api.telegram.org/bot<token>/getUpdates` (ganti `<token>`), lalu
   cari `"chat":{"id":-100...`. Id grup diawali tanda minus. Bila hasilnya
   kosong, kirim `/start` lagi di grup lalu muat ulang.
4. Di panel buka **Pengaturan > Telegram**, isi **Token bot** dan **Chat
   id**, lalu klik **Simpan pengaturan**. Token disimpan terenkripsi dengan
   `panel.key`, tidak ditampilkan lagi, dan tidak pernah ditulis ke log.
5. Klik **Kirim tes**. Pesan tes harus muncul di grup; bila gagal, panel
   menyebut alasannya.
6. Isi **Alamat dasar panel** (mis. `https://192.168.1.10:8080`) agar pesan
   memuat tautan ke halaman server. Bila kolom ini kosong, pesan tidak
   memuat tautan; alamat abu-abu di kolom hanya saran (alamat publik
   panel) dan tidak dipakai sebelum diketik. Centang hanya jenis peringatan yang
   ingin dikirim; yang tidak dicentang tetap tampil di halaman
   **Peringatan**.

Host panel harus bisa menjangkau `https://api.telegram.org` (port 443).
Bila jaringan memakai proxy, tambahkan `Environment=HTTPS_PROXY=http://proxy:3128`
ke service lewat `sudo systemctl edit dbpanel-panel`, lalu
`sudo systemctl restart dbpanel-panel`.

**Isi pesan.** Hanya nama server, status, nomor error, penjelasan awam,
saran, waktu, dan tautan panel; pesan error MySQL apa adanya tidak pernah
dikirim (bisa memuat isi baris data). Contoh:

```
⛔ Replika RS-2 berhenti
Data ganda (error 1062): ada baris yang sudah ada di replika.
Saran: Resync ulang.
Master: RS-1 · Sejak: 02:14 · https://192.168.1.10:8080/replicas/3
```

Setiap peringatan dikirim sekali saat muncul, diulang tiap 6 jam selama
masih terbuka (**Ulangi pemberitahuan**), dan ditutup dengan pesan "pulih"
beserta lama gangguannya, setelah peringatan itu tertutup paling sedikit 1
menit. Peringatan yang terbuka lagi kurang dari 5 menit setelah tertutup
dianggap **berkedip**: sebagai ganti pesan barunya dikirim satu pesan
"⚠️ RS-2: replika berhenti berkedip N kali dalam 5 menit terakhir",
paling sering sekali per 5 menit, lalu satu "pulih" setelah tenang.
Peringatan yang ditutup karena replikanya mulai **Resync ulang** atau dilepas
dari panel tidak dianggap pulih; pesannya "ℹ️ RS-2: peringatan replika
berhenti ditutup: Resync ulang dimulai". Pengiriman yang gagal dicoba lagi
tiap menit; statusnya (terkirim, gagal dan alasannya, misalnya "Telegram
tidak terjangkau: …" atau "Telegram menolak: …") tampil di halaman
**Peringatan**.

**Data yang keluar ke Telegram** (untuk penilaian perlindungan data
pribadi, mis. UU PDP; server Telegram berada di luar jaringan Anda): nama
server, jenis peringatan dan status, nomor error MySQL dengan penjelasan
awamnya, waktu kejadian, tautan ke panel (alamat IP atau nama internal
panel dan id server), serta di ringkasan mingguan persentase waktu
sinkron, jumlah dan lama gangguan, pemakaian disk, dan jumlah pekerjaan
yang gagal. Data pasien, isi tabel, dan teks error MySQL tidak pernah
dikirim. Pakai grup privat, bukan kanal publik (`@nama`): isi kanal publik
bisa dibaca siapa pun.

**Jangan ganggu.** Saat perawatan terjadwal, pilih lama **Jangan ganggu**
(1, 2, 8, atau 24 jam) di halaman server, master, replika, atau
**Peringatan**; klik **Batalkan** (tampil selama Jangan ganggu aktif) untuk
mengakhirinya lebih awal. Tombol itu membuka dialog konfirmasi (tanpa kata
sandi) yang menyebut jenis peringatan yang tetap dikirim. Selama
itu peringatan server tersebut tetap dicatat dan tampil di panel, tetapi
tidak dikirim ke Telegram, kecuali jenis yang tidak pernah dibungkam
karena bisa berarti kehilangan data atau dua master: master diduga
tumbang, disk kritis, kunci replika terlepas, promote selesai sebagian,
batas waktu kunci promote, master lama dikarantina, master tidak dipin,
kunci promote master dipertahankan, dan empat peringatan kunci start master
lama. Jenis itu tetap mengirim pesan pertama dan pesan pulihnya; hanya
pengulangannya yang ditunda. Saat Jangan ganggu berakhir, peringatan yang
masih terbuka dikirim satu kali, dan panel mengirim satu ringkasan: rentang
waktunya, jumlah peringatan yang terbuka selama senyap, dan daftarnya
(termasuk yang sudah pulih; paling banyak 30 baris). Awal senyap disimpan di
`panel.db`, jadi ringkasan tetap dikirim bila panel dimulai ulang; ringkasan
yang gagal terkirim dicoba lagi.

**Ringkasan mingguan.** Bawaannya setiap Senin 07:00 (waktu panel), satu
pesan: untuk tiap replika persentase waktu Sinkron atau Ketinggalan selama
7 hari terakhir, jumlah dan lama gangguan, pemakaian disk tiap server, dan
jumlah pekerjaan yang gagal. Atur hari dan jamnya, atau matikan, di
**Pengaturan > Ringkasan mingguan**. Bila Telegram baru diatur setelah
jadwalnya lewat, ringkasan minggu itu tetap dikirim.

## Kontrol replika

Tombol-tombol ini ada di halaman replika (dan master atau server untuk
yang disebut). Setiap tombol membuka dialog yang menjelaskan akibatnya,
hanya tersedia bila server itu tidak sedang menjalankan pekerjaan, dan
dicatat di **Audit**.

Tombol yang merusak atau sulit dibatalkan (Resync ulang, Lepas replika,
Hapus server, Lepas database, Buka kunci master, Siapkan sebagai master
untuk server yang sudah menjadi master, Pasang sertifikat MySQL, dan
Perketat sambungan replikasi; juga unggah sertifikat HTTPS, Kembali ke
sertifikat panel, dan Nama dan alamat tambahan di Pengaturan) meminta kata
sandi panel Anda lagi, dan nama server bila disebut di bawah. Konfirmasi
itu berlaku 2 menit untuk satu tindakan saja; salah kata sandi dicatat di
**Audit** (`confirm_failed`), begitu juga tindakan yang ditolak karena
konfirmasinya tidak berlaku, kedaluwarsa, atau sudah dipakai
(`confirm_rejected`). Hanya sandi salah yang dihitung: setelah 5 sandi
salah dalam semenit panel menolak konfirmasi sampai menit itu lewat, dan
sandi yang benar mengosongkan hitungannya. Sandi salah di sini juga
dihitung ke batas login nama pengguna itu (20 sandi salah per 15 menit),
dan sebaliknya: setelah batas itu habis, login dan konfirmasi sama-sama
ditolak.

- **Stop replika / Start replika:** menghentikan atau menjalankan lagi
  replikasi; statusnya menjadi **Dihentikan** (tanpa peringatan) lalu
  kembali **Sinkron**. Selama berhenti lag membesar; bila lebih lama dari
  masa simpan binlog master (disebut di dialog), replika harus disync
  ulang.
- **Kunci kembali** (saat **Kunci terlepas**): menyalakan lagi
  `super_read_only`. Bila ada yang sempat menulis ke replika, isinya bisa
  berbeda dari master; pertimbangkan **Resync ulang**.
- **Resync ulang:** menyalin ulang data master ke replika ini lalu
  menyambungkan replikasinya lagi, dengan database yang sama seperti
  tercatat. Panel membaca ulang daftar database replika saat dialog dibuka
  dan menyebut persis database yang akan **dibuang**; ketik nama server
  untuk melanjutkan. Database yang hanya ada di replika dicentang satu per
  satu, dan **Ulangi** resync meminta kata sandi lagi (lihat "Pembuangan
  data dengan konfirmasi segar" di "Keamanan"). Pekerjaan berjalan tanpa berhenti, kecuali bila di
  replika ditemukan database di luar daftar itu: pekerjaan lalu menunggu
  persetujuan Anda. CLONE dipakai bila replika mengikuti semua database;
  centang **Paksa dump** untuk memakai mysqldump (juga bila CLONE terus
  gagal). Selama resync MySQL di replika bisa di-restart bila
  pengaturannya perlu diubah. Jalankan di luar jam sibuk.
- **Lepas replika:** ketik nama server, lalu pilih:
  - **Lepas, biarkan replikasi berjalan:** panel berhenti mengelola dan
    memantau server ini; MySQL di sana terus mengikuti master sampai Anda
    menghentikannya sendiri.
  - **Lepas dan hentikan replikasi:** replikasi dihentikan dan
    pengaturannya dihapus, dan agent melepas peran `replica`. MySQL tetap
    hanya-baca; untuk membukanya jalankan di server itu
    `mysql -e "SET PERSIST super_read_only=OFF; SET PERSIST read_only=OFF"`.
    `SET PERSIST` (bukan `SET GLOBAL`) membuat perubahan itu bertahan saat
    MySQL di-restart: dbpanel mengunci dengan `SET PERSIST` dan baris
    `read_only`/`super_read_only` di blok my.cnf-nya, dan nilai yang
    di-PERSIST menang atas my.cnf.

  Akun replikasi di master tidak disentuh (dipakai bersama oleh semua
  replika master itu).
- **Hapus server** (halaman server): ketik nama server. Panel menolak
  menghapus master yang masih punya replika ("lepas atau hapus semua
  replikanya dulu") dan replika yang belum dilepas. Setelah dihapus,
  matikan agent di server itu dengan
  `sudo systemctl disable --now dbpanel-agent`.
- **Buka kunci master** (halaman master, saat **Terkunci**): ketik nama
  server. Pastikan hanya satu server yang menerima tulisan aplikasi.
- **Periksa sekarang** dan **Jangan ganggu**: lihat "Pemantauan" dan
  "Notifikasi Telegram".

## Promote

Promote menjadikan salah satu replika master baru. Buka halaman master dan
tekan **Promote…**: panel membuka layar pertimbangan, dan dari situ juga
memilih jenisnya sendiri. Anda tidak memilih jenis promote; layar
menjelaskan kenapa jenis itu yang tersedia.

**Jangan jalankan panel di server master.** Bila server master mati,
panel ikut mati dan promote darurat tidak bisa dilakukan dari panel. Selama
panel berjalan di server master, halaman master dan layar promote
menampilkan peringatan tetap "Panel berjalan di server ini; bila server ini
mati, promote darurat tidak bisa dilakukan dari panel". Panel boleh berjalan
di salah satu replika, dan replika itu boleh dipilih menjadi master baru.

### Promote terencana dan darurat

- **Terencana (master hidup):** master berstatus Sehat atau Terkunci.
  Panel mengunci master (`super_read_only` dengan `SET PERSIST`, bertahan
  saat MySQL restart), menunggu replika terpilih menerapkan semua
  transaksinya (paling lama 10 menit), menjadikannya master baru, lalu
  mengkarantina master lama. Aplikasi tidak bisa menulis sejak master
  dikunci sampai alamatnya diganti di tiap aplikasi. Bila replika gagal
  menyusul, kunci master dibuka lagi dan tidak ada yang berubah.
- **Darurat (master tidak terjangkau):** master Diduga tumbang, Tidak
  terjangkau, atau agent-nya offline. Replika terpilih dijadikan master
  dengan transaksi yang sudah diterimanya: relay log yang sudah sampai
  diterapkan dulu (paling lama 10 menit), jadi tidak dibuang. Agent
  replika memeriksa sendiri bahwa master memang tidak menjawab (tiga kali
  coba dalam sekitar 30 detik, dan sekali lagi tepat sebelum tulis
  dibuka); bila master ternyata menjawab, terkunci atau tidak, promote
  ditolak dan replika dibiarkan seperti semula: master yang hidup bukan
  keadaan darurat. Konfirmasi meminta kata sandi dan nama master lama.

Bila master menjawab dari replika tetapi agent master tidak bisa dipulihkan,
promote darurat tidak bisa dipakai: pulihkan agent master lalu jalankan
promote terencana. Master yang terkunci (oleh promote terencana yang
gagal, atau dengan `sudo dbpanel agent lock-master`) dibuka dengan
`sudo dbpanel agent unlock-master` di server master (lihat "Peran dan
sumber yang dipin"); perintah yang sama membuka kunci promote terencana
yang tertinggal bila kandidatnya hilang untuk waktu lama (peringatan
**Master terkunci** tetap terbuka selama itu).

Setelah promote terencana gagal sebelum kandidat membuka tulis dan panel
tidak bisa memastikan kunci master sudah dibuka (agent master terputus),
panel menolak promote darurat topologi itu sampai tenggat kunci itu lewat
ditambah tenggang 60 detik penjaga kunci; pesannya menyebut jam mulai
bisanya. Sebelum itu tekan **Ulangi** pada promote terencana itu bila agent
master tersambung lagi, atau buka kuncinya dengan `sudo dbpanel agent
unlock-master` di server master, lalu tekan **Ulangi** pada promote
terencana itu (perintah itu sendiri tidak memberi tahu panel; Ulangi yang
memastikan kunci terbuka). Selama itu **Resync ulang**, **Buat replika**,
**Tambah database**, dan **Lepas database** yang menghapus datanya di
topologi itu juga ditolak, begitu pula **Ulangi** pekerjaan-pekerjaan itu:
memuat atau menghapus data membuat replika bisa ditulis sebentar, dan
penjaga kunci yang melihat replika bisa ditulis pada tenggatnya
mengkarantina master yang sehat.

### Layar pertimbangan

Layar promote menampilkan, untuk tiap replika: status, lag, posisi
terhadap master, antrean terapan ("semua diterima, 2 belum diterapkan;
akan diterapkan sebelum promote"), cakupan database, kesegaran laporan,
tanda **Paling mutakhir**, dan alasan bila replika tidak bisa dipilih
(Dihentikan, Berhenti dengan error, Agent offline, data tidak segar, atau
mempunyai transaksi di luar master). Pilihan bawaan adalah replika lengkap
yang paling mutakhir.

Pada promote darurat posisi dihitung dari laporan terakhir master. Bila
agent master masih tersambung tetapi MySQL-nya mati, panel membaca binlog
terakhir di disk master, jadi angkanya pasti ("ada 3 transaksi yang belum
sampai ke replika mana pun"). Panel juga meminta tiap replika mencoba
menyambung ke master (**Periksa sekarang** mengulangnya). Transaksi yang
belum sampai ke replika terpilih hilang dari master baru; bila master lama
hidup lagi, panel bisa memeriksa dan mengamankannya (lihat di bawah).

**Replika sebagian** (yang hanya mengikuti beberapa database) boleh
dipilih. Layar menampilkan kotak merah "Database berikut tidak akan ada di
master baru: …"; data itu tetap utuh di master lama yang dikarantina dan
bisa diamankan dari sana. Centang persetujuannya, dan konfirmasi meminta
nama replika. Replika yang tidak terjangkau tidak menghalangi promote:
centang persetujuannya, dan replika itu ditandai **Perlu diarahkan ulang**
lalu diarahkan ke master baru saat tersambung lagi.

**Jaringan terbelah.** Promote darurat membuktikan bahwa master tidak
terjangkau dari replika, bukan bahwa master mati. Master yang masih hidup
di sisi jaringan lain tetap bisa ditulis aplikasi di sisi itu, dan
karantina tidak bisa sampai ke sana. Panel tidak mengunci master secara
otomatis saat ia tidak melihat panel dan replika (itu akan memadamkan
master yang sehat pada kebetulan yang salah). Layar mengatakannya: "Pastikan
master benar-benar mati atau tidak lagi dipakai aplikasi; bila ia hidup di
jaringan lain, tulisan di sana akan tertinggal". **Periksa data tertinggal**
menemukan tulisan yang masuk di sana.

### Pengalihan aplikasi

Setelah promote selesai, panel membuka layar pengalihan (juga lewat banner
dashboard "Promote menunggu pengalihan aplikasi" dan halaman master baru)
dan mengirim ringkasan ke Telegram dengan alamat master baru. Layar itu
menampilkan alamat master baru (dengan tombol salin) dan alamat lama,
lalu dua tabel yang diperbarui tiap 15 detik: koneksi yang masih ke master
lama dan koneksi yang sudah ke master baru (per host asal, akun, dan
database). Agent membaca semua thread klien dari `performance_schema`
(atau dari `information_schema.PROCESSLIST` bila `performance_schema`
mati), termasuk sesi yang sedang login. Yang tidak ditampilkan hanya:
akun layanan buatan agent (`dbpanel_repl` dan akun dump
`dbpanel_dump_<8 heksa>`), replika yang membaca binlog, thread MySQL
sendiri (`system user`, `event_scheduler`, thread replikasi), dan sesi
agent sendiri: sesi akun admin MySQL agent dari server itu sendiri (socket,
`localhost`, `127.0.0.1`, `::1`) yang membawa atribut koneksi
`program_name=dbpanel-agent`, yang dipasang agent di setiap koneksinya.
Sesi akun admin agent dari server itu tanpa atribut itu (orang yang
login dengan akun yang sama di server itu) dihitung dengan tanda "sesi
akun admin dari server ini". Tanpa `performance_schema` atribut tidak
terbaca, jadi semua sesi lokal akun itu, termasuk milik agent, dihitung
dengan catatan "tidak bisa dibedakan dari agent". Sesi akun admin agent
dari host lain, dan sesi `dbpanel_admin` dari mana pun, ditandai "akun
admin panel". Koneksi X Protocol ditandai "X Protocol" dan thread yang
jenisnya tidak dikenal agent "jenis koneksi tidak dikenal"; keduanya ikut
dihitung. Bila daftar tidak bisa dibaca, atau tidak lengkap (misalnya
`Performance_schema_thread_instances_lost` lebih dari nol, atau agent
versi lama), tabel menulis "tidak bisa dipastikan" beserta alasannya.
Layar menulis "Tidak ada koneksi aplikasi tersisa" hanya bila daftar
master lama lengkap dan kosong. Atribut koneksi bisa dipalsukan klien,
tetapi hanya sesi yang memakai akun admin agent dari server itu sendiri
yang bisa disembunyikan dengan cara itu. Tabel koneksi hanya dibaca untuk master, master
lama, dan replika topologi yang pernah dipromosikan. Pakai akun admin khusus untuk panel
(lihat "Hubungkan MySQL"), bukan `root` atau akun yang juga dipakai orang
atau aplikasi di server itu: sesi orang lain dengan akun itu ditandai,
bukan dipisahkan.
Arahkan semua aplikasi yang menulis ke database ke alamat baru, restart
aplikasinya, lalu tekan **Sudah saya alihkan**. Selama belum ditekan,
Telegram mengingatkan dengan jarak pengulangan peringatan (bawaan 6 jam),
dan master lama tidak bisa dibangun ulang atau dihapus.

Pada promote darurat dengan master lama yang belum terjangkau, karantina
dikirim begitu agent master lama tersambung lagi. Master lama yang hidup
kembali bisa sempat ditulis aplikasi yang belum dialihkan di antara MySQL
hidup dan agent-nya tersambung (biasanya hitungan detik); layar pengalihan
mengingatkannya ("Master lama belum dikarantina karena belum terjangkau.
Pastikan aplikasi dialihkan sebelum ia dihidupkan kembali."). Transaksi yang
masuk di jendela itu ditemukan oleh **Periksa data tertinggal**.

### Master lama

Master lama dikarantina: agent-nya memasang `super_read_only` dengan
`SET PERSIST`, pin perannya menjadi `quarantined`, dan sumbernya master
baru. Saat agent itu start atau MySQL-nya hidup lagi, agent memasang
`super_read_only` lebih dulu sebelum hal lain.

**Kunci start.** Supaya MySQL master lama juga menyala baca-saja ketika
agent-nya tidak ada (agent mati, atau MySQL-nya mati saat dikarantina dan
dinyalakan orang sebelum agent jalan), karantina menulis kunci start, baik
MySQL-nya hidup maupun mati. Kunci itu dua bagian:

- berkas `dbpanel-start-lock.sql` (milik root, mode 0644) di direktori
  include konfigurasi MySQL bila ada, selain itu di direktori berkas
  konfigurasi utama (setelah symlink diikuti); tidak pernah di dalam
  datadir (CLONE mengganti isinya). Isinya tepat tiga baris:
  `SET GLOBAL super_read_only=ON;`, `SET PERSIST read_only=ON;`,
  `SET PERSIST super_read_only=ON;`;
- satu baris `init_file=<path berkas itu>` di blok konfigurasi yang dikelola
  dbpanel. mysqld menjalankan berkas itu saat menyala, sebelum klien mana
  pun bisa masuk.

Berkas ditulis lebih dulu, baru barisnya; saat dilepas, baris dihapus
lebih dulu, baru berkasnya, jadi agent tidak pernah meninggalkan
konfigurasi yang menyebut berkas yang tidak ada (mysqld menolak menyala
dengan konfigurasi seperti itu). `init_file` milik operator yang dibaca sebelum baris agent
tidak dijalankan selama karantina; halaman master lama menyebutnya. Selama
peran `quarantined`, loop penjaga agent (tiap 5 detik) menulis lagi berkas
atau baris yang hilang atau berubah (bila gagal, dicoba lagi paling sering
semenit sekali), dan semenit sekali (serta segera setelah menulisnya)
memeriksa dengan `mysqld --print-defaults` bahwa `init_file` terakhir yang
dibaca mysqld memang berkas agent dan berkas itu bisa dibaca pengguna MySQL.
mysqld juga membaca `~/.my.cnf` milik akun layanannya (akun dari opsi
`user`; bila tidak ada, `mysql`, karena agent berjalan sebagai root), jadi
pemeriksaan itu dijalankan dua kali: sekali sebagai root dengan
`HOME=/nonexistent` (berkas opsi sistem), lalu sebagai akun layanan itu
sendiri (uid, gid dan grup tambahannya) dengan `HOME` akun tersebut. Root
tidak pernah membuka berkas yang disebut akun itu, dan mysqld yang
berjalan sebagai akun itu melewati tanpa pesan berkas opsi sistem yang
tidak bisa dibacanya; karena itu kunci dianggap terjamin hanya bila kedua
hasil mengatakannya, dan yang pertama tidak terjamin memberi alasannya.
Setiap jalan dibatasi 10 detik dan 256 KiB keluaran. Keluaran yang lebih
besar membuat kunci "belum terjamin" dengan alasan "konfigurasi efektif
MySQL tidak bisa dibaca: keluaran --print-defaults terlalu besar"; id akun
layanan yang tidak bisa dibaca juga, dengan alasan "konfigurasi efektif
MySQL tidak bisa dibaca: id akun layanan MySQL <akun> tidak bisa dibaca:
<sebab>". Laporan ke panel memuat paling banyak
20 `init_file` milik operator dan alasan paling panjang 300 byte. Bila
mysqld hanya membaca satu berkas opsi, agent memeriksanya dengan
`--defaults-file`, sekali sebagai root; di sana `~/.my.cnf` tidak dibaca
dan langkah akun layanan ini dilewati. Nama opsi
dibaca dengan semua awalan yang diterima mysqld (`loose-`, `skip-`,
`disable-`, `enable-`, `maximum-`). `init_file` berawalan yang dibaca
sesudah baris agent membuat kunci "belum terjamin". Awalan `maximum-` pada
`init_file` di mana pun juga membuat kunci "belum terjamin": mysqld menolak
menyala dengan opsi itu. Home akun layanan di atau di bawah `/home`, `/root`
atau `/run/user` yang tidak bisa didaftar agent, atau kosong, membuat kunci
"belum terjamin" juga: `ProtectHome` di unit systemd agent menyembunyikan
direktori itu, jadi `~/.my.cnf` di sana tidak bisa diperiksa. Akun layanan
yang tidak ditemukan, akun tanpa direktori home, atau `~/.my.cnf` yang
menyebut user lain juga membuat kunci "belum terjamin". Yang tetap tidak
terlihat oleh agent: `User=` di unit systemd MySQL yang berbeda dari opsi
`user`, dan `MYSQL_HOME` di lingkungan layanan MySQL.
Perubahan keadaan kunci start dilaporkan ke panel saat itu juga, dan laporan
pertama setelah agent menyala sudah memeriksa kunci itu. Direktori yang
namanya memuat `#`, tanda kutip, garis miring terbalik atau karakter kendali
tidak dipakai untuk kunci start (baris opsi MySQL akan terpotong di sana);
baris **Kunci start** lalu menulis alasannya. Halaman server master lama menampilkan baris
**Kunci start**: "Terkunci", atau "belum terjamin" beserta alasannya; yang
kedua membuka peringatan Kritis "master lama belum terjamin terkunci saat
MySQL menyala; jangan nyalakan MySQL-nya sebelum agent tersambung".

Saat MySQL master lama menyala, putaran penjaga pertama memasang
`read_only` dan `super_read_only` dengan `SET PERSIST` lalu menilai caranya
menyala. Agent menyimpan hasilnya di `/var/lib/dbpanel/start-result`: hasil
terburuk sejak karantina dan start terakhir tiap jenis yang buruk. Urutannya
dari yang terburuk: sempat bisa ditulis (`late`), gagal menyala (`failed`),
menyala tanpa kunci start (`bypassed`), normal (`ok`). Setiap laporan
pemantauan dari server yang dikarantina membawa hasil itu. Jadi hasilnya
tidak hilang bila panel belum tersambung, bila kiriman kejadian dari agent
hilang, atau bila agent dimulai ulang. Bila berkas itu gagal ditulis, agent
mencobanya lagi tiap putaran penjaga. Karantina baru lewat agent menghapus
hasil lama, kecuali pin tidak bisa dibaca. `set-role quarantined` saat agent
mati tidak menghapusnya. Hasil yang tersimpan tetapi tidak terbaca ditimpa
hasil berikutnya, dengan peringatan di log agent.

Panel membuka dan menutup peringatan menyala menurut hasil itu, setiap kali
menerima laporan pemantauan. Hasil menyala yang berubah dihitung sebagai
perubahan: agent mengirim laporan pada pemeriksaan berikutnya (paling lama
satu interval pemeriksaan, bawaan 15 detik; lihat "Kecepatan" di
"Pemantauan"), juga di server yang dikarantina. **Periksa sekarang** di
halaman server meminta laporan saat itu juga. Dari laporan, panel membuka
paling banyak 5 peringatan menyala sejenis per server sekaligus, lalu
satu tiap 10 menit, supaya agent yang laporannya berganti-ganti tidak
membanjiri daftar peringatan; start yang tertahan batas itu dibuka begitu
batasnya mengizinkan bila laporan masih menyebutnya, dan peringatan
pertama sejenis untuk sebuah server selalu terbuka saat itu juga. Bila salah satu bendera sempat mati
(seseorang membukanya, atau kunci tidak berlaku), panel membuka peringatan
Kritis "master lama sempat bisa ditulis saat menyala; jalankan Periksa data
tertinggal"; peringatan itu tertutup setelah **Periksa data tertinggal**
dijalankan sesudahnya, atau server itu tidak lagi master lama. Start
berikutnya yang sempat bisa ditulis membuka peringatan baru. **Bangun ulang
sebagai replika** yang belum selesai menyalin (atau dibatalkan) tidak
menutupnya, dan selama peringatan itu terbuka formulir **Bangun ulang** dan
dialog **Hapus** tidak memercayai hasil **Periksa data tertinggal** yang
lebih lama: keduanya memperingatkan bahwa tulisan sejak itu belum diperiksa.
Laporan menyala yang tidak bisa dibaca panel diperlakukan sebagai yang
terburuk (sempat bisa ditulis, atau gagal menyala). Bila `@@init_file` bukan
berkas agent padahal berkas itu sudah ada sebelum mysqld menyala (misalnya
`--init-file` di baris perintah layanan, yang mengalahkan baris agent dan
tidak terlihat oleh agent selama MySQL mati), peringatannya "kunci start
tidak dipakai MySQL; periksa opsi layanan".

Yang perlu diketahui:

- **Jangan menghapus berkas atau baris itu.** Bila berkas dihapus saat agent
  berjalan, agent menulisnya lagi dalam beberapa detik. Bila berkas
  dihapus (atau tidak bisa dibaca mysqld) saat agent mati, MySQL menolak
  menyala ("Failed to open the bootstrap file") sampai agent hidup dan
  menulisnya lagi; selama MySQL mati, penjaga membaca log error MySQL (juga
  baris yang lebih tua dari berkas yang baru ditulisnya lagi; log dibaca
  hanya bila berkas biasa, bukan symlink atau FIFO) dan panel membuka
  peringatan Kritis "MySQL master lama gagal menyala karena berkas kunci
  tidak terbaca" dengan langkah perbaikannya. Arah gagal ini
  aman bagi data. Bila berkas log error MySQL itu sendiri berupa symlink,
  penjaga tidak membacanya, jadi MySQL yang gagal menyala karena berkas kunci
  tidak dilaporkan ke panel; agent hanya menulis peringatan di journal-nya
  sekali semenit. Bila barisnya dihapus saat agent berjalan, agent
  menulisnya lagi; bila dihapus saat agent mati, MySQL menyala tanpa kunci
  start: ia tetap baca-saja hanya bila `read_only` dan `super_read_only`
  ON sempat tersimpan (`SET PERSIST`) sebelumnya, dan agent melaporkan
  "sempat bisa ditulis" begitu tersambung bila tidak. Untuk melepas kunci
  start saat agent tidak bisa dijalankan, ikuti "Melepas kunci start dengan
  tangan" di bawah.
- **AppArmor dan SELinux.** Kunci start baru diuji pada MySQL 8.0 (8.0.46)
  tanpa AppArmor atau SELinux yang ditegakkan. Bila mysqld dibatasi
  AppArmor, agent hanya menganggap kunci terjamin bila berkasnya di bawah
  `/etc/mysql/` (yang dibaca profil bawaan paket); di bawah SELinux hanya
  bila label berkasnya `mysqld_etc_t`. Selain itu baris **Kunci start**
  menulis "belum terjamin" dengan alasan "mungkin ditolak profil
  AppArmor/SELinux mysqld". Bila profil memang menolak berkasnya, MySQL
  master lama tidak menyala dan panel memperingatkan: data aman, tetapi
  **Periksa data tertinggal** tertunda sampai diperbaiki.
- **Melepas kunci.** Kunci dilepas oleh loop penjaga, bukan sekali jalan:
  begitu peran bukan lagi `quarantined` (misalnya setelah **Bangun ulang
  sebagai replika**) dan MySQL yang hidup terbukti menyimpan kedua bendera
  ON. Sebelum itu kunci tetap, apa pun perannya; jadi `set-role replica`
  saat MySQL mati tidak membuat MySQL menyala bisa ditulis. `set-role
  master` melepasnya saat itu juga (lihat "Peran dan sumber yang dipin").
  Replika yang kunci start-nya masih ada tidak bisa dipilih sebagai
  kandidat promote.
- **Hapus server** tidak melepas karantina dan kunci start master lama,
  baik MySQL-nya hidup maupun mati: selama kunci start terjamin, MySQL-nya
  tetap menyala baca-saja sampai seseorang mengubah perannya di server
  itu. Dialog hapus mengatakannya, dan bila laporan terakhir server itu
  tidak menyebut kunci start "Terkunci", mengatakan bahwa baca-saja tidak
  terjamin (lihat "Memakai lagi server yang dihapus saat masih
  dikarantina" di bawah).

Halaman server master lama memuat kotak **Master lama** dengan:

- **Periksa data tertinggal:** membandingkan GTID master lama dengan master
  baru dan merangkum transaksi yang tidak sampai ke master baru dari binlog
  (jumlah transaksi, tabel dan jumlah baris, rentang waktu; isi baris tidak
  ditampilkan). Hasilnya "Tidak ada data tertinggal" atau, misalnya, "Ada 1
  transaksi yang tidak sampai ke master baru, menyentuh simrs.pasien (1
  baris), antara 09:41:20 dan 09:41:20". Pada promote terencana hasilnya
  selalu kosong.
- **Amankan data tertinggal** (bila ada): menulis transaksi itu sebagai
  berkas SQL di `/var/lib/dbpanel/salvage` di server master lama, yang bisa
  dijalankan dengan `mysql` dan berisi komentar yang bisa dibaca untuk tiap
  baris. Agent hanya menulis transaksi yang ditemukan **Periksa data
  tertinggal** terakhir di server itu (set GTID-nya disimpan agent di
  memori dan di `/var/lib/dbpanel/salvage/leftover-gtid`, 0600), satu
  salvage sekaligus, dan direktori itu paling banyak 8 GiB (salvage
  ditolak bila sudah penuh, dan dihentikan lalu berkasnya dihapus bila
  akan melewatinya; satu berkas paling besar 2 GiB). Berkas disimpan 7
  hari. Daftar berkas di halaman yang sama punya
  tombol **Unduh** (lewat HTTPS, dialirkan dari agent tanpa disimpan di
  panel) dan **Hapus**; keduanya meminta kata sandi panel untuk berkas itu
  saja, karena berkas itu bisa jadi satu-satunya salinan data tertinggal.
  Bila versi agent server itu berbeda dari panel, daftar tidak dibaca dan
  halaman menulis "Agent perlu dimutakhirkan".
- **Bangun ulang sebagai replika:** formulir "Buat replika" dari master
  baru dengan master lama sebagai tujuan. Semua data MySQL di master lama
  diganti salinan dari master baru; konfirmasi menyebut koneksi aplikasi
  yang masih ada dan data tertinggal yang belum diperiksa atau belum
  diamankan, lalu meminta kata sandi dan nama server. Bangun ulang juga
  butuh persetujuan root di master lama (lihat "Persetujuan di server untuk
  bangun ulang" di bawah); tombolnya baru aktif setelah disetujui. Bila
  daftar koneksi master lama tidak lengkap dan kosong (masih ada koneksi, atau daftarnya
  tidak bisa dipastikan), formulir juga meminta centang "Saya paham N
  koneksi masih ada dan tulisannya hilang" (atau pernyataan bahwa
  koneksinya tidak bisa dipastikan); tanpa centang itu bangun ulang
  ditolak dan tidak ada yang dimulai. Panel membaca ulang daftar koneksi
  saat pekerjaan dimulai dan saat **Ulangi**, dan menolak tanpa centang
  bila koneksi muncul lagi. Setelah bangun ulang, agent melepas kunci
  start (di atas) begitu MySQL-nya terbukti menyimpan baca-saja. Berkas yang sudah
  diamankan tidak ikut dihapus: setelah bangun ulang daftarnya tetap
  tampil di halaman server itu, dengan **Unduh** dan **Hapus**, sampai
  berkasnya habis masa simpan 7 hari. Selama seluruh pemuatan salinan
  (bukan hanya sesaat) master lama tidak baca-saja agar pemuatan bisa
  menulis; penjaga karantina menunda pemasangan baca-saja sampai pemuatan
  selesai. Sesi akun MySQL panel atau aplikasi yang belum dialihkan bisa
  menulis ke server itu selama waktu tersebut, dan tulisannya menjadi
  transaksi GTID asing di replika hasil bangun ulang; karena itu alihkan
  semua aplikasi dan jangan memakai akun panel selama bangun ulang.
  Sesudahnya ia kembali baca-saja dan menjadi replika master baru. Pin
  peran agent baru menjadi `replica` bila salinan dari master baru
  berhasil di agent yang sama dan memang mengganti semua database di
  server itu (lihat akhir "Persetujuan di server untuk bangun ulang").
- **Hapus server:** menghapus server dari panel. Panel tidak meminta agent
  melepas pin `quarantined`, jadi server itu tetap dikarantina dan kunci
  start-nya tetap terpasang; MySQL-nya tetap baca-saja selama kunci itu
  terjamin (lihat "Memakai lagi server yang dihapus saat masih
  dikarantina" di bawah). Seperti bangun
  ulang, baru tersedia setelah **Sudah saya alihkan**.

#### Persetujuan di server untuk bangun ulang

Master lama yang dikarantina (quarantined) hanya bisa dibangun ulang
(rebuild) setelah root di server itu menyetujuinya. Bangun ulang membuang semua data MySQL
master lama. Karena itu panel saja tidak bisa memulainya. Root di master
lama harus melihat dari mana data penggantinya diambil, lalu menyetujuinya
di server itu.

1. Di halaman master lama, buka form **Bangun ulang sebagai replika**.
   Kotak **Persetujuan di server** menulis "Belum disetujui. Jalankan
   perintah ini di <server> sebagai root, lalu klik Periksa lagi:", lalu
   perintahnya, misalnya `sudo dbpanel agent approve 042917`, dan "Kode
   berlaku <N> menit lagi.". Tombol **Bangun ulang sebagai replika** belum
   bisa diklik. Form yang dibuka lewat tautan dari situs lain tidak
   bertanya ke agent (pertanyaan itu bisa membuat permintaan baru di
   server); kotaknya hanya menulis "Klik Periksa lagi untuk melihat
   persetujuan di server." dengan tombol **Periksa lagi**.
2. Bila promote tadi promote darurat, jalankan dulu **Periksa data
   tertinggal** (dan **Amankan data tertinggal** bila ada) di halaman
   master lama.
3. Masuk ke master lama dan jalankan perintah dari form itu. Perintah
   selalu mencari alamat nama sumber di DNS saat itu (kecuali sumbernya
   alamat IP). Layar menampilkan nama server ini, perannya, sumber data
   (master baru, `host:port`) dan alamat IP sumber itu, lalu bahwa SEMUA
   data MySQL di server ini dibuang dan diganti, dan baris "Selama
   persetujuan berlaku (24 jam), panel juga boleh mengubah pengaturan
   replikasi, akun replikasi, pengaturan dan sertifikat MySQL di server
   ini, dan memulai ulang MySQL." Pastikan nama dan alamat itu memang
   master baru yang sekarang dipakai aplikasi. Perintah menolak sebelum
   bertanya, tanpa menulis apa pun (kode keluar 1), bila nama sumber
   mengarah ke lebih dari satu alamat ("Nama sumber <host:port> mengarah
   ke lebih dari satu alamat (<alamat>). Persetujuan hanya bisa diberikan
   bila nama itu mengarah ke satu alamat. Tetapkan satu alamat untuk nama
   itu di server ini (misalnya di /etc/hosts), lalu jalankan perintah
   approve ini lagi. Persetujuan tidak ditulis."), atau bila nama itu
   sekarang mengarah ke alamat lain daripada alamat yang dicatat saat
   karantina ("Nama sumber <host:port> sekarang mengarah ke <alamat>, bukan
   ke alamat yang dicatat saat karantina (<alamat tercatat>). Bila alamat
   baru itu memang master baru, jalankan: sudo dbpanel agent set-source
   <host:port>, lalu jalankan perintah approve ini lagi. Persetujuan tidak
   ditulis."). `set-source` dengan `host:port` yang sama menghapus alamat
   tercatat dan kodenya tetap berlaku; dengan `host:port` lain, form
   menampilkan kode baru.
4. Ketik `ya` lalu Enter. Jawaban lain, atau input kosong, membatalkan:
   "Dibatalkan; tidak ada yang disetujui."
5. Perintah menulis "Disetujui sampai <waktu>. Kembali ke panel dan klik
   Periksa lagi. Untuk membatalkan: sudo dbpanel agent approve batal".
   Persetujuan (dan `approve batal`) juga dicatat satu baris di log sistem
   (lihat di bawah).
6. Di form, klik **Periksa lagi**. Kotak itu kini menulis "Sudah disetujui
   di <server> untuk sumber <host:port> (<alamat>); berlaku <H> jam <M>
   menit lagi. Untuk membatalkan, jalankan di server itu: sudo dbpanel
   agent approve batal", dan tombol bangun ulang aktif.

Batas waktunya:

- Kode terdiri dari enam angka dan berlaku 30 menit. Selama berlaku dan
  sumber yang dipin tidak berubah, kodenya tetap sama. Setelah habis, form
  menampilkan kode baru.
- Persetujuan berlaku 24 jam, hanya untuk sumber dan alamat yang
  ditampilkan saat disetujui.
- Persetujuan yang waktunya lebih dari 5 menit di masa depan tidak
  berlaku. Periksa jam server itu.

Berkasnya ada di direktori pin, dalam format JSON, dan dibaca dengan aturan
yang sama dengan berkas pin:

- `/etc/dbpanel/rebuild-request`: permintaan yang ditulis agent, dengan
  `code` (kode), `source` (sumber yang dipin) dan `created` (waktu dibuat).
- `/etc/dbpanel/rebuild-approval`: persetujuan yang ditulis `approve`,
  dengan `source`, `addr` (alamat IP yang ditampilkan), `approved` (waktu
  disetujui) dan `expires` (waktu habis).

Alamat yang disetujui adalah alamat itu sendiri bila sumbernya alamat IP,
atau satu-satunya alamat hasil pencarian DNS nama sumber (yang harus sama
dengan alamat yang diverifikasi saat karantina, bila ada).

Untuk membatalkan persetujuan, jalankan di master lama:

```sh
sudo dbpanel agent approve batal
```

Perintah itu menghapus kedua berkas dan menulis "Persetujuan dan
permintaan bangun ulang di server ini dihapus.". Setiap `set-role` juga
menghapusnya, begitu pula karantina baru dari peran lain dan bangun ulang
yang selesai. Mencabut persetujuan tidak menghentikan pekerjaan bangun
ulang yang sudah berjalan. Untuk menghentikannya, batalkan pekerjaannya di
panel.

`approve` yang menulis persetujuan dan `approve batal` masing-masing
menulis satu baris ke log sistem (syslog, fasilitas `auth`, tag
`dbpanel-agent`; misalnya `journalctl -t dbpanel-agent`): "rebuild approval
written" atau "rebuild approval cancelled", dengan `source`, `addr`,
`expires`, `request_created` (waktu dalam UTC), dan siapa yang
menjalankannya (`user` dan `sudo_uid` dari `sudo`, serta `uid`). Kodenya
tidak pernah dicatat. Bila log sistem tidak bisa ditulis, perintah menulis
"Peringatan: catatan ke log sistem gagal ditulis (<sebab>)." dan hasilnya
tetap. Agent juga mencatat di journal-nya setiap perintah panel yang lolos
karena persetujuan ("protected command allowed by the local rebuild
approval", dengan nama perintah, sumber dan waktu disetujui).

Persetujuan yang ada tidak berlaku lagi bila sumber yang dipin berubah, bila
nama sumber mengarah ke alamat lain (setiap alamat hasil DNS harus sama
dengan alamat yang disetujui) atau DNS-nya gagal, bila waktunya habis, bila
waktunya tidak masuk akal (lebih dari 5 menit di masa depan, atau masa
berlaku lebih dari 24 jam), bila server itu tidak lagi dikarantina, atau
bila berkasnya tidak bisa dipakai. Form lalu menulis "Persetujuan yang ada
di <server> tidak berlaku lagi: <alasan>." dan menampilkan kode baru.
Alasannya ditulis panel sendiri dari kode yang dikirim agent, bukan dari
kalimat agent: "masa berlakunya sudah habis", "sumber yang dipin di server
itu sudah berubah", "nama sumbernya sekarang mengarah ke alamat lain",
"alamat untuk nama sumbernya tidak ditemukan (DNS)", "waktu persetujuannya
di masa depan; periksa jam server itu", "masa berlakunya tidak wajar",
"server itu tidak lagi dikarantina", "berkas persetujuannya tidak bisa
dipakai", atau, untuk kode yang tidak dikenal panel ini (atau agent yang
belum mengirim kode), "alasannya tidak dikenali panel ini".

Jawaban agent yang tidak masuk akal bagi panel (misalnya kode yang bukan
enam angka, kode pada jawaban yang tidak membutuhkannya, sumber yang bukan
alamat IP atau nama host, atau permintaan yang waktunya sudah habis)
diperlakukan sebagai tidak terbaca: form menulis "Persetujuan di server
tidak bisa diperiksa sekarang; klik Periksa lagi." dan isinya dicatat di
log panel. Bila agent menolak pertanyaan itu dengan kalimatnya sendiri
(misalnya karena pin tidak bisa dibaca), form menulis "Persetujuan di
server tidak bisa diperiksa: agent <server> menolak permintaannya." dan
kalimat agent ditampilkan terpisah, diawali "Pesan dari agent <server>
(bukan dari panel):". Penolakan agent yang ditampilkan dengan kalimatnya
sendiri di form **Buat replika** dan di halaman pekerjaan (**Ulangi**,
**Setujui**, **Batalkan**), serta penolakan `mysql.releaseRole` di galat
pekerjaan, juga diawali label itu, dijadikan satu baris dan dipotong.

Panel memeriksa persetujuan saat bangun ulang dimulai, dan saat pekerjaan
bangun ulang yang gagal atau menunggu dijalankan lagi. Tanpa persetujuan
panel menolak dengan "Bangun ulang <server> butuh persetujuan di server
itu. Jalankan di server itu sebagai root: sudo dbpanel agent approve
<kode>, lalu coba lagi." Bila persetujuan habis di tengah pekerjaan,
langkah berikutnya gagal dengan "Server <server> dikarantina (quarantined):
langkah ini butuh persetujuan di server itu. Jalankan di server itu sebagai
root: sudo dbpanel agent approve <kode>, lalu klik Ulangi." (bila kodenya
tidak bisa dibaca dari agent: "… Buka form Bangun ulang sebagai replika,
klik Periksa lagi, jalankan perintah yang ditampilkan di server itu, lalu
klik Ulangi."). Setujui lagi, lalu tekan **Ulangi dari tahap ini**.
Pekerjaan **Buat replika** (juga bangun ulang) yang parameternya tidak bisa
dibaca panel tidak bisa diulang atau disetujui. Catatan audit bangun ulang dan **Ulangi**
atau **Setujui**-nya menyimpan persetujuan yang dipakai keputusan itu
(`local_approval`, `local_approval_age_sec`).

Selama persetujuan berlaku, panel boleh menjalankan langkah bangun ulang
dan perintah lain yang butuh persetujuan (lihat "Mode ketat" di "Peran dan
sumber yang dipin"): itu yang disebut baris "Selama persetujuan berlaku (24
jam), …" di layar `approve`. Memasang MySQL dan menghapus akun selain akun
dump sementara tetap ditolak.

Karantina berakhir (pin menjadi `replica`) hanya bila salinan itu memang
mengganti data server: setelah CLONE dari sumber yang dipin, atau setelah
dump dari sumber itu bila setiap database non-sistem di server (selain
`mysql`, `sys`, `information_schema`, `performance_schema`) dibuat oleh
pemuatan dump itu sendiri. Database yang sudah ada sebelum pemuatan, atau
yang tidak termasuk database yang di-dump, membuat langkah pengaturan
replikasi gagal dengan "server ini masih punya database yang tidak disalin
dari sumber <host:port> (<nama database, paling banyak 10>); karantina tidak
diakhiri. Ulangi Bangun ulang sebagai replika dari panel. Bila panel lalu
menolak karena database itu tidak ada di master baru, lihat README, bagian
"Database yang tidak ada di master baru"." Pin tetap `quarantined` dan persetujuan
tidak dihapus oleh langkah itu. Bangun ulang biasa dari panel tidak
berakhir begini: pekerjaan itu menghapus semua database yang tertinggal di
server (setelah daftarnya Anda setujui) sebelum memuat dump. Bila teks itu
muncul, jalankan lagi **Bangun ulang sebagai replika** dari panel; database
yang disebut ikut dihapus setelah daftarnya Anda setujui, jadi salin dulu
isinya sendiri bila masih diperlukan. Bila daftar database
server itu tidak bisa dibaca saat itu, langkahnya gagal dengan "sumber
replikasi sudah diatur, tetapi daftar database di server ini tidak bisa
dibaca; karantina tidak diakhiri: <sebab>"; tekan **Ulangi dari tahap
ini**.

Daftar database itu hanya dipercaya bila akun MySQL yang dipakai agent
pasti melihat semua database: akun itu memegang `SHOW DATABASES` langsung
di `ON *.*` (tidak dicabut untuk satu database pun), atau `SELECT`
langsung di `ON *.*` selama `partial_revokes` mati (bawaan MySQL). Hak
yang hanya didapat lewat role tidak dihitung. Akun yang dibuat agent saat
memasang MySQL dan contoh `GRANT ALL ON *.*` di "Hubungkan MySQL" sudah
memenuhinya. Bila tidak, dump bangun ulang gagal sebelum memuat apa pun,
atau langkah pengaturan replikasi gagal dengan pin tetap `quarantined` dan
persetujuan tidak dihapus, keduanya dengan "daftar database di server ini
tidak bisa dipastikan lengkap: akun MySQL <akun> yang dipakai agent tidak
punya hak SHOW DATABASES. Jalankan sebagai root MySQL di server ini: GRANT
SHOW DATABASES ON *.* TO <akun>; lalu ulangi bangun ulang dari panel."
Jalankan `GRANT` itu, lalu tekan **Ulangi dari tahap ini**.

Bila agent dimulai ulang di antara salinan data dan pengaturan replikasi,
agent tidak lagi punya catatan salinan itu. Langkah pengaturan replikasi
lalu gagal dengan "sumber replikasi sudah diatur, tetapi server ini tetap
dikarantina: agent tidak punya catatan salinan data dari bangun ulang ini
(misalnya karena agent dimulai ulang di tengah jalan). Ulangi bangun ulang
dari panel." Pin tetap `quarantined` dan persetujuan tidak dihapus.
**Ulangi dari tahap ini** tidak menyalin data lagi, jadi mulai lagi
**Bangun ulang sebagai replika** dari panel.

Agent versi lama belum mengenal persetujuan di server. Panel menolak
membangun ulangnya dengan "Agent perlu dimutakhirkan: agent di <server>
belum mengenal persetujuan di server, jadi bangun ulang ditolak. Klik
Pasang ulang agent di halaman server itu, jalankan perintahnya, lalu coba
lagi." Panel versi lama dengan agent baru menampilkan penolakan agent
sebagai teks; teks itu memuat perintah `sudo dbpanel agent approve <kode>`
lengkap.

#### Memakai lagi server yang dihapus saat masih dikarantina

**Hapus server** pada master lama yang dikarantina tidak mengubah apa pun
di server itu. Bila laporan terakhir server itu menyebut kunci start
"Terkunci", dialog hapus menulis: "Server ini tetap dikarantina
(quarantined) setelah dihapus dari panel: MySQL-nya tetap baca-saja
(read-only) dan kunci start (start lock) tetap terpasang. Untuk memakai
server ini lagi, jalankan di server itu: sudo dbpanel agent set-role
unassigned, atau sudo dbpanel agent set-role master bila server ini akan
menjadi master lagi." Selain itu (belum ada laporan, laporan tanpa keadaan
kunci start, atau kunci "belum terjamin") dialog menulis: "Server ini tetap
dikarantina (quarantined) setelah dihapus dari panel, tetapi kunci start
(start lock)-nya tidak terjamin, jadi MySQL-nya belum tentu baca-saja
(read-only) setelah menyala ulang. Untuk memakai server ini lagi, jalankan
di server itu: sudo dbpanel agent set-role unassigned, atau sudo dbpanel
agent set-role master bila server ini akan menjadi master lagi."

Untuk memakainya lagi, pilih salah satu di server itu:

- **Disiapkan ulang lewat panel:** `sudo dbpanel agent set-role
  unassigned`, lalu ketik `ya`. Peran dan pin sumbernya dihapus. Agent
  yang berjalan melepas kunci start setelah MySQL menyala dengan baca-saja
  tersimpan. Perintah menulis langkah berikutnya: "Langkah berikutnya: bila
  server ini sudah dihapus dari panel, tambahkan lagi lewat Tambah server,
  lalu jalankan Siapkan master atau Buat replika. Perhatian: server ini
  tidak lagi dikarantina; agent tidak lagi menjaga MySQL-nya tetap
  baca-saja (read-only)." (juga bila berkas pin tidak bisa dibaca). Baca-saja
  yang tersimpan tetap sampai seseorang mematikannya.
- **Menjadi master lagi:** `sudo dbpanel agent set-role master`, lalu ketik
  `ya`. Kunci start dilepas saat itu juga dan server dipin sebagai master.
  Perintah menulis: "Langkah berikutnya: bila MySQL di server ini masih
  baca-saja (read-only) dan server ini memang akan dipakai aplikasi sebagai
  master, pastikan master lain tidak lagi dipakai, lalu buka dengan: sudo
  dbpanel agent unlock-master". `unlock-master` tidak menghapus `read_only`
  yang disimpan karantina dengan `SET PERSIST`, jadi setelah MySQL restart
  `read_only` menyala lagi. Matikan dengan `SET PERSIST read_only=OFF;`
  sebagai root MySQL (misalnya lewat `sudo mysql`, atau `mysql -u root -p`
  bila root MySQL memakai kata sandi).

#### Melepas kunci start dengan tangan

Cara ini melepas kunci start (start lock) master lama tanpa agent. Pakai
hanya bila `set-role master` menulis "kunci start tidak bisa diperiksa",
atau agent tidak bisa dijalankan sama sekali. Kerjakan sebagai root
(misalnya setelah `sudo -i`), berurutan:

1. Hentikan agent: `systemctl stop dbpanel-agent`. Selama perannya
   `quarantined`, agent yang berjalan menulis kunci itu lagi dalam beberapa
   detik.
2. Pastikan MySQL menyala dan baca-saja (read-only) tersimpan, sebelum
   kunci dilepas. Tanpa itu, MySQL yang menyala setelah kunci dilepas bisa
   ditulis. Bila MySQL mati, nyalakan dulu (misalnya `systemctl start
   mysql`, atau `systemctl start mysqld`): selama baris kunci masih ada,
   mysqld menjalankan berkas kunci saat menyala, yang memasang dan
   menyimpan baca-saja. Bila MySQL menolak menyala karena berkas kuncinya
   hilang ("Failed to open the bootstrap file"), tulis lagi berkas itu
   (path-nya ada di baris `init_file=`, lihat langkah 3) dengan tiga baris
   isi kunci start (lihat "Master lama"), milik root, mode 0644, lalu
   nyalakan lagi. Lalu masuk sebagai root MySQL (misalnya
   `sudo mysql`, atau `mysql -u root -p` bila root MySQL memakai kata
   sandi) dan jalankan:

   ```sql
   SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only;
   SELECT VARIABLE_NAME, VARIABLE_VALUE FROM performance_schema.persisted_variables
     WHERE VARIABLE_NAME IN ('read_only', 'super_read_only');
   ```

   Lanjutkan hanya bila yang pertama menjawab `1 1` dan yang kedua
   menunjukkan `ON` untuk kedua nama: itu juga syarat agent sebelum ia
   melepas kunci start sendiri. Bila tidak, simpan keduanya, lalu periksa
   lagi dengan dua perintah di atas:

   ```sql
   SET PERSIST read_only=ON;
   SET PERSIST super_read_only=ON;
   ```

3. Cari baris kunci di konfigurasi MySQL. Perintah pertama menampilkan
   berkas opsi yang dibaca mysqld (agent mengelola berkas pertama yang
   ada, atau `zz-dbpanel.cnf` di direktori `!includedir` terakhirnya);
   yang kedua mencari barisnya di tempat yang biasa:

   ```sh
   mysqld --verbose --help 2>/dev/null | grep -A1 '^Default options are read'
   grep -rn 'dbpanel-start-lock.sql' /etc/my.cnf /etc/my.cnf.d /etc/mysql /usr/etc/my.cnf 2>/dev/null
   ```

   Bila `grep` tidak menemukan apa pun, jalankan lagi dengan berkas dari
   perintah pertama dan direktori `!includedir`-nya. Berkas itu
   `<direktori include>/zz-dbpanel.cnf` bila konfigurasi MySQL punya
   direktori include; selain itu blok antara `# BEGIN dbpanel` dan
   `# END dbpanel` di berkas konfigurasi utama.
4. Di berkas itu, hapus baris `init_file=<…>/dbpanel-start-lock.sql`.
5. Baru sesudah itu hapus berkas `dbpanel-start-lock.sql` yang disebut
   baris tadi. Urutan ini wajib: mysqld menolak menyala bila konfigurasinya
   menyebut berkas yang tidak ada.
6. Ubah peran dan hapus pin sumber serta berkas persetujuan (ganti
   `master` dengan `unassigned` bila server ini akan disiapkan ulang lewat
   panel):

   ```sh
   echo master > /etc/dbpanel/role
   rm -f /etc/dbpanel/source /etc/dbpanel/source-addr /etc/dbpanel/rebuild-request /etc/dbpanel/rebuild-approval
   ```

   Berkas peran harus tetap milik root dan tidak bisa ditulis grup atau
   pengguna lain.
7. Karena langkah 2 menyimpan `read_only` dan `super_read_only` ON, MySQL
   tetap baca-saja, juga setelah menyala ulang. Seperti kata `set-role
   master`: "bila MySQL di server ini masih baca-saja (read-only) dan server
   ini memang akan dipakai aplikasi sebagai master, pastikan master lain
   tidak lagi dipakai". Dua master yang sama-sama ditulis aplikasi membuat
   data keduanya berbeda dan sebagian hilang. Baru sesudah itu buka sebagai
   root MySQL:

   ```sql
   SET PERSIST super_read_only=OFF;
   SET PERSIST read_only=OFF;
   ```

Sesudahnya agent boleh dijalankan lagi: `systemctl start dbpanel-agent`.
Dengan peran `master` atau `unassigned`, agent tidak menulis kunci start.

### Replika lain

Replika lain diarahkan ke master baru di akhir promote. Replika yang tidak
terjangkau saat itu berstatus **Perlu diarahkan ulang ke master baru; akan
dilakukan saat tersambung** dan diarahkan otomatis saat agent-nya
tersambung lagi; tombol **Arahkan sekarang** mencobanya saat itu juga.
Replika yang punya transaksi yang tidak ada di master baru berstatus **Perlu
dibangun ulang**: replikasinya tidak diubah, pin sumbernya dipindah ke
master baru, dan **Resync ulang** dari master baru memperbaikinya (tidak
otomatis). Keduanya memunculkan peringatan sampai selesai.

Bila promote berhenti setelah replika terpilih sudah bisa ditulis, panel
tidak membatalkannya (tidak ada jalan mundur yang aman dari dua master).
Pekerjaan berhenti dengan status selesai sebagian; buka pekerjaannya dan
tekan **Ulangi dari tahap ini**.

## Catatan audit

Menu **Audit** mencatat setiap tindakan yang mengubah sesuatu: login
berhasil dan gagal, logout, perubahan pengaturan, tes Telegram, Siapkan
master, Buat replika, tambah dan lepas database, setujui/ulangi/batalkan
pekerjaan, serta semua tombol di "Kontrol replika". Setiap baris berisi
waktu, pengguna, IP, tindakan, target, dan hasilnya (berhasil, atau alasan
ditolak); sandi dan token tidak pernah dicatat. Perubahan chat id dan
alamat dasar panel dicatat dengan nilai barunya, utuh. Isi rincian tiap baris
dibatasi (2 KiB); untuk tindakan yang ditolak atau gagal hanya nama isian
dan panjang nilainya yang dicatat, dan login gagal dengan nama pengguna
yang tidak ada dicatat sebagai `(tidak dikenal)` dengan hash pendek (hash
itu hanya ada di `panel.db`, tidak di salinan journal, karena nama yang
diketik bisa saja sandi). Saring
menurut server dan tanggal. Bila hasil sebuah tindakan berasal dari pesan
error agent, kutipan data di dalamnya disensor (lihat "Sensor kutipan data"
di "Pemantauan"). Catatan tidak bisa diubah atau dihapus lewat
panel dan tidak dipangkas; ia ikut tersimpan di `panel.db`, jadi ikut
dicadangkan bersamanya. Penjagaannya ada di dalam `panel.db` sendiri, jadi
catatan ini tahan ubah terhadap aplikasi, bukan terhadap root di host panel
(yang bisa mengubah berkasnya).

Setiap baris juga membawa hash SHA-256 atas isinya dan hash baris
sebelumnya (rantai hash). Bagian atas halaman **Audit** menunjukkan "Rantai
utuh sampai #4521" atau "Rantai putus di #3100": baris pertama yang tidak
cocok lagi dengan rantai. Itu bisa baris yang diubah atau disisipkan, atau
baris sesudahnya (bila pengubah ikut menghitung ulang hash baris yang ia
ubah, atau bila sebuah baris di tengah dihapus). Status ini diperiksa paling
sering sekali per menit. Pemeriksaan yang sama dari baris perintah (hanya
membaca `panel.db`; keluar 0 bila utuh, 1 bila putus atau tidak bisa
diperiksa, 2 bila argumennya salah; jalankan sebagai pengguna `dbpanel`,
karena root ditolak):

```sh
sudo -u dbpanel dbpanel panel audit-verify
```

Batas rantai ini perlu dipahami. Hash-nya tidak memakai kunci rahasia: siapa
pun yang bisa menulis `panel.db` bisa menghitung ulang semua hash dari baris
yang ia ubah sampai akhir, dan hasilnya tetap "Rantai utuh". Menghapus
baris-baris *terakhir* juga tidak memutus rantai. Kedua hal itu hanya
ketahuan dengan membandingkan isi `panel.db` dengan salinan kedua: setiap
catatan juga ditulis ke log panel (`journalctl -u dbpanel-panel`, pesan
`audit`, dengan `stored=false` bila basis data menolak catatan itu),
termasuk penolakan pairing agent (`agent_pair`). Salinan itu membawa `ts`
persis seperti tersimpan dan `hash` barisnya: baris di `panel.db` yang
hash-nya tidak sama dengan `hash` terakhir di journal, atau yang hilang
dari ujung rantai, menandakan catatan diubah atau dihapus. Log itu
bisa dibaca anggota grup `adm` dan `systemd-journal` di host panel, lebih
luas daripada `panel.db` (0600), jadi batasi keanggotaan grup itu.

Salinan journal itu juga punya batas: ia ada di host yang sama, jadi root
di host panel bisa mengubah atau menghapusnya juga, dan journald membuang
log lama sesuai batas ukuran dan umurnya (bawaan sistem, sering hanya
beberapa minggu). Bila catatan audit perlu bertahan terhadap root di host
panel, teruskan journal ke server log lain (misalnya lewat rsyslog atau
`systemd-journal-remote`) dan bandingkan dari sana.

Saat panel versi ini pertama kali dijalankan, rantai dihitung sekali untuk
catatan lama (migrasi 0009; 10.000 catatan butuh kurang dari satu detik).

## Pembaruan

Perbarui **panel dulu, lalu agent**. Jalankan ulang `install.sh` di host
panel (perintah yang sama dengan "Pasang panel"). Skrip:

1. memasang binary baru;
2. menghentikan `dbpanel-panel`;
3. membuat pengguna `dbpanel` bila belum ada;
4. bila panel sebelumnya berjalan sebagai root dengan berkas langsung di
   `/etc/dbpanel` dan `/var/lib/dbpanel`, memindahkan `panel.db`, `bin/`,
   `ca.*`, `mysql-ca.*`, `web*`, `panel.key` dan `admin-initial-password`
   ke subfolder `panel/` (isi tidak berubah, jadi sertifikat, pin, dan
   sandi tetap), lalu memberikan subfolder itu ke `dbpanel`; berkas agent
   di `/etc/dbpanel` (akun MySQL tersegel, kebijakan lokal, pin `role` dan
   `source`) tidak disentuh;
5. menulis unit systemd baru; bila `/etc/dbpanel/agent.yaml` belum ada,
   melepas agent server host panel di database (`dbpanel panel
   reset-local-agent`, sebagai `dbpanel`) supaya panel menulis token baru;
   lalu menyalakan panel sebagai `dbpanel`;
6. menunggu (paling lama 60 detik) token yang ditulis panel untuk servernya
   sendiri, menyalinnya (dibaca sebagai `dbpanel`) ke
   `/etc/dbpanel/local-pair-token` (0600 root),
   lalu menyalakan `dbpanel-agent`, yang berpasangan dengan token itu lalu
   berjalan. Agent tertanam versi lama digantikan agent ini; server host
   panel tetap baris yang sama (riwayat dan pemantauannya tetap).

Tiap langkah memeriksa keadaannya dulu, jadi skrip aman dijalankan ulang,
juga bila terputus di tengah jalan. Selama itu panel mati beberapa detik;
agent di server lain tersambung kembali sendiri.

Agent di server database yang terlalu lama untuk pemantauan ditandai **perlu dimutakhirkan** di kolom
**Versi agent** daftar **Server**, dan statusnya **Agent perlu
dimutakhirkan**. Untuk memutakhirkannya:

1. Buka halaman server itu, klik **Pasang ulang agent**.
2. Jalankan perintah pemasangan yang ditampilkan di server database itu
   (perintah yang sama juga tampil di daftar server selama tokennya
   berlaku).
3. Agent baru terhubung dan pemantauan mulai dalam beberapa detik.

**Setelah memperbarui panel ke versi dengan HTTPS dan sertifikat MySQL,
pasang ulang agent di setiap server database** dengan langkah di atas
(tidak perlu di host panel: `install.sh` sudah memasang agent-nya). Agent
lama tetap terhubung dan dipantau, tetapi tidak mengenal perintah
sertifikat MySQL. Panel lalu menandainya **perlu dimutakhirkan**.
**Siapkan master**, dan tahap sertifikat replika yang masternya belum punya
sertifikat, melewati tahap sertifikat dengan peringatan di log pekerjaan
(replikasinya belum diverifikasi; jalankan **Perketat sambungan
replikasi** setelah agent diperbarui). **Pasang sertifikat MySQL**,
**Perketat sambungan replikasi**, dan **Buat replika**/**Resync ulang**
dari master yang sudah bersertifikat gagal dengan "Agent di <server> versi
lama dan belum mengenal sertifikat MySQL. Klik Pasang ulang agent di
halaman server itu, jalankan perintahnya, lalu Ulangi." Perintah pemasangan baru memakai HTTPS
berpin; perintah lama berisi `http://` tidak berlaku lagi.

Pemasangan ulang hanya mengganti binary, sertifikat, dan `agent.yaml`.
Akun MySQL tersegel, kebijakan lokal, serta peran dan sumber yang dipin di
`/etc/dbpanel` tetap. Agent lama yang belum punya berkas peran hanya
menulis berkas peran (dengan petunjuk peran dari panel); pin sumber replika
baru ditulis pada konfigurasi replikasi, dump, atau CLONE berikutnya (lihat
"Peran dan sumber yang dipin").

## Keamanan

- **HTTPS.** UI panel, unduhan agent, dan pairing hanya dilayani lewat
  HTTPS di port 8080. Di port yang sama, HTTP biasa hanya melayani halaman
  `/sertifikat` (beserta unduhan `dbpanel-panel.crt` dan gaya halamannya);
  jalur lain dialihkan (301) ke `https://`. Cookie sesi selalu `Secure`,
  dan jawaban HTTPS membawa `Strict-Transport-Security` (1 tahun), jadi
  browser yang pernah membuka panel lewat HTTPS **dengan nama DNS** tidak
  lagi mencoba HTTP. HSTS tidak berlaku untuk alamat IP: `http://` dengan
  IP selalu lewat HTTP biasa dulu sampai dialihkan, dan orang lain di
  jaringan bisa menyela di situ. Simpan bookmark dengan `https://`, dan
  pakai nama DNS bila bisa. Bila browser menolak membuka panel lewat nama
  tanpa pilihan lanjut (HSTS, setelah CA web berganti atau sertifikat
  sendiri kedaluwarsa), buka `http://<IP-panel>:8080/sertifikat` lalu
  pasang ulang sertifikatnya; pemberitahuan di panel juga menyebut alamat
  itu.
  Panel tidak pernah jatuh ke HTTP biasa: bila sertifikat atau kuncinya
  rusak saat start, panel menulis peringatan ke log dan memakai (atau
  membuat ulang) sertifikatnya sendiri.
- **Sertifikat keamanan panel** yang dipasang pengguna (`dbpanel-panel.crt`)
  adalah sertifikat penerbit (CA web, `/etc/dbpanel/panel/web-ca.crt`) yang
  dibatasi (name constraints) pada nama panel **beserta semua subnamanya**,
  IP panel, dan **seluruh subnet** kartu jaringan host panel saat CA dibuat.
  Bila kuncinya bocor (mis. panel disusupi), sertifikat itu bisa dipakai
  menyamar sebagai layanan lain di subnet itu (alamat IP) atau di bawah
  nama-nama itu, di komputer yang memasangnya; tidak untuk situs lain.
  Karena itu **Nama dan alamat tambahan** menolak nama yang terlalu umum:
  nama harus punya minimal tiga bagian (mis. `panel.rsud.go.id`) kecuali
  berakhiran `.local`, `.lan`, `.internal`, `.home` atau `.corp` (cukup dua,
  mis. `rsud.local`), dan tidak boleh langsung di bawah akhiran publik umum
  (mis. `bank.co.id`, `rsud.go.id`). Pakai nama khusus panel, bukan nama
  domain kantor. Sidik jari SHA-256 sertifikat ini tampil di
  **Pengaturan** dan dicetak `install.sh`.
  Sertifikat HTTPS panel (`web.crt`) berlaku 2 tahun dan diterbitkan ulang
  otomatis 30 hari sebelum habis atau saat alamat panel berubah, dengan
  kunci yang sama (pin di perintah pemasangan tetap berlaku), tanpa
  restart. Hanya nama yang dipilih operator (`--public-host`, nama host
  mesin, **Nama dan alamat tambahan**) yang bisa membuat CA web baru: bila
  salah satunya berada di luar batas CA web, panel membuat CA web baru (dua
  CA sebelumnya disimpan sebagai `web-ca.*.old` dan `web-ca.*.old2`) dan
  menampilkan pemberitahuan di setiap halaman sampai ada yang menutupnya:
  komputer yang pernah memasang sertifikat perlu memasang ulang. Kartu
  jaringan yang muncul belakangan (bridge docker, libvirt, VPN) tidak
  pernah membuat CA baru; alamatnya yang di luar batas CA tidak dimuat
  sertifikat (dicatat di log).
- Admin domain Windows bisa menyebarkan `dbpanel-panel.crt` ke semua
  komputer lewat Group Policy (Computer Configuration → Policies → Windows
  Settings → Security Settings → Public Key Policies → Trusted Root
  Certification Authorities).
- **Sertifikat sendiri.** Punya sertifikat dari CA kantor atau publik?
  Unggah sertifikat (PEM, boleh dengan rantainya; sertifikat dan kunci
  bersama paling besar 64 KiB) dan kuncinya di
  **Pengaturan > Sertifikat keamanan panel > Lanjutan** (dengan konfirmasi
  kata sandi). Panel memeriksa
  bahwa kunci cocok, sertifikat belum kedaluwarsa, dan memuat alamat panel,
  lalu langsung memakainya (disimpan 0600 sebagai `web.custom.crt`/`.key`;
  kunci tidak pernah ditampilkan lagi). **Kembali ke sertifikat panel**
  mengembalikan sertifikat buatan panel. Bila sertifikat yang diunggah
  kedaluwarsa atau rusak, panel memakai sertifikatnya sendiri dan
  menampilkan pemberitahuan di setiap halaman sampai admin mengunggah
  sertifikat baru atau memilih Kembali ke sertifikat panel; 14 hari
  sebelumnya peringatan **Sertifikat HTTPS panel hampir kedaluwarsa**
  terbuka. Pin di perintah pemasangan agent
  mengikuti sertifikat yang sedang dipakai.
- Panel hanya menjawab permintaan yang header `Host`-nya alamat panel:
  `--public-host`, `localhost`, alamat loopback, IP salah satu kartu
  jaringan host panel, atau nama yang dimuat sertifikat HTTPS yang sedang
  dipakai (nama host mesin, **Nama dan alamat tambahan**, nama di
  sertifikat sendiri). Nama lain (mis. alias DNS yang belum ditambahkan)
  mendapat HTTP 421; tambahkan di **Pengaturan > Sertifikat keamanan panel
  > Lanjutan > Nama dan alamat tambahan**.
- `--public-host` hanya menerima alamat IPv4/IPv6 atau nama host (huruf,
  angka, titik, strip), karena nilainya masuk ke skrip pemasang agent yang
  berjalan sebagai root.
- Token pemasangan disimpan sebagai hash di `panel.db`. Perintah pemasangan
  hanya bisa ditampilkan ulang selama panel belum dimulai ulang; sesudahnya
  buat token baru. Skrip pemasang memberikan token ke `dbpanel agent pair`
  lewat variabel lingkungan `DBPANEL_TOKEN` (bukan argumen, yang terlihat di
  `ps`). Pairing manual: `sudo DBPANEL_TOKEN=... DBPANEL_WEB_PIN=sha256//...
  dbpanel agent pair --panel https://<ip-panel>:8080 --ca-sha256 <sidik jari
  CA>`, atau `--token-stdin` (lihat "Alamat panel untuk agent").
  Sidik jari CA ada di skrip pemasang (`CA_SHA256=...`), atau hitung di host
  panel dengan `openssl x509 -in /etc/dbpanel/panel/ca.crt -outform DER | sha256sum`.
  `--ca-sha256` wajib; `--insecure-skip-ca-pin` mematikan pemeriksaan itu
  (tidak aman, hanya untuk jaringan yang sepenuhnya tepercaya).
- **Panel bukan root.** Service `dbpanel-panel` berjalan sebagai pengguna
  sistem `dbpanel` (tanpa shell login) dan menolak berjalan sebagai root
  (`dbpanel panel` meminta `--allow-root`, hanya untuk pengujian). Panel
  tidak menjalankan perintah apa pun di host-nya; MySQL host panel diurus
  service `dbpanel-agent` (root) seperti di server lain, berpasangan dengan
  sertifikatnya sendiri.
- **Tata letak berkas di host panel:**
  - `/etc/dbpanel/panel/` (0700 `dbpanel`): `ca.crt`/`ca.key` (CA agent),
    `mysql-ca.*`, `web*` (HTTPS), `panel.key`, `admin-initial-password`,
    dan sementara `local-pair-token` (0600: token server lokal dan pin
    HTTPS, ditulis tiap start selama agent lokal belum berpasangan,
    dihapus begitu berpasangan; panel hanya menerimanya dari alamat
    loopback, dan percobaan dari alamat lain ditolak tanpa memakai token
    itu dan dicatat di log audit sebagai `agent_pair`);
  - `/var/lib/dbpanel/panel/` (0700 `dbpanel`): `panel.db`, `bin/`;
  - `/etc/dbpanel/` (0711 root): berkas agent host panel, sama seperti di
    server lain (`agent.yaml`, `agent.key`, `mysql.cred`, `role`, ...);
    `/var/lib/dbpanel/` 0711 root.
  Panel tidak bisa membaca berkas agent, dan agent tidak memakai berkas
  panel. `install.sh` (root) tidak pernah mengikuti tautan di direktori
  milik `dbpanel`: token dibaca sebagai `dbpanel`, `bin/` dibangun di
  direktori milik root lalu dipasang dengan rename, dan `chown` hanya
  mengenai berkas panel lama milik root yang dipindahkan.
- Hardening systemd: `NoNewPrivileges`, `PrivateTmp`, `ProtectHome`,
  `ProtectKernel*`, dan untuk panel `ProtectSystem=strict` (hanya
  `/var/lib/dbpanel/panel` dan `/etc/dbpanel/panel` yang bisa ditulis,
  tanpa kapabilitas apa pun: port 8080 dan 8443 tidak memerlukannya). Di
  host panel, `dbpanel-agent` dinyalakan setelah panel (drop-in
  `dbpanel-agent.service.d/panel.conf`). Service `dbpanel-agent` berjalan
  sebagai root. Unit agent
  tidak memakai `ProtectSystem`: agent harus menulis `/etc/dbpanel` (akun
  MySQL tersegel), file konfigurasi MySQL di `/etc`, dan memasang paket
  MySQL ke `/usr`. Unit agent memakai `KillMode=process`, supaya mysqld
  yang dinyalakan agent lewat init script atau `mysqld_safe` (misalnya di
  aaPanel) tetap hidup saat agent di-restart atau diperbarui. Catatan:
  karena unit agent memakai `PrivateTmp`, mysqld yang dinyalakan agent
  lewat `mysqld_safe`/init script mewarisi `/tmp` privat agent, sehingga
  socket di `/tmp/mysql.sock` tidak terlihat oleh aplikasi di host. Pada
  server seperti itu, nyalakan MySQL lewat systemd atau aaPanel sendiri
  (agent hanya memberi tahu), atau pindahkan `socket` ke luar `/tmp`
  (misalnya `/run/mysqld/mysqld.sock`).
- Master di host panel: pin peran dan sumbernya ada di `/etc/dbpanel`
  milik root, di luar jangkauan proses panel (pengguna `dbpanel`), sama
  seperti di server lain. Panel yang disusupi tetap bisa meminta apa pun
  yang diizinkan agent, jadi master produksi tetap lebih baik di server
  terpisah.
- Akun dump sementara (`dbpanel_dump_*`) di MySQL 8.0.31 ke atas mendapat
  `SELECT, FLUSH_TABLES, LOCK TABLES, SHOW VIEW, TRIGGER, EVENT`, tanpa
  `RELOAD`. `FLUSH_TABLES` (hak dinamis sejak 8.0.31) cukup untuk `FLUSH
  TABLES` dan `FLUSH TABLES WITH READ LOCK` singkat yang dijalankan
  `mysqldump --single-transaction --set-gtid-purged`; sudah diuji dengan
  dump nyata di MySQL 8.0.46, dan akun itu ditolak (error 1227) saat
  mencoba mereset binary log (`RESET MASTER`, di 8.2 ke atas `RESET BINARY
  LOGS AND GTIDS`), yang dibuka `RELOAD`. MySQL 8.0.26–8.0.30 tidak punya
  `FLUSH_TABLES`, jadi akun dump di sana tetap memakai `RELOAD`. Akun admin
  yang dipakai agent harus memegang `FLUSH_TABLES ... WITH GRANT OPTION`
  untuk memberikannya (root sudah; pemeriksaan "Hubungkan MySQL" dan
  preflight memintanya); bila belum, akun dump tetap mendapat
  `RELOAD` dan log pekerjaan di panel (juga journal agent) mencatat
  peringatan dengan perintah
  `GRANT FLUSH_TABLES ON *.* TO '<admin>'@'<host>' WITH GRANT OPTION`.
- `FLUSH_TABLES` tetap membolehkan `FLUSH TABLES WITH READ LOCK`, yang
  menghentikan semua penulisan selama dipegang. Panel yang disusupi bisa
  membuat akun dump dan memakainya untuk itu. Pagarnya ada di agent:
  akun dump dibatasi 2 sesi (`MAX_USER_CONNECTIONS 2`) dan sandinya
  kedaluwarsa 1 hari setelah dibuat (`PASSWORD EXPIRE INTERVAL 1 DAY`);
  loop penjaga agent, tiap 5 detik, memutus sesi akun
  `dbpanel_dump_<8 hex>` yang memegang kunci global itu 1 menit atau
  lebih (`mysqldump` hanya memegangnya sesaat; dibaca dari
  `performance_schema.metadata_locks`, jadi tanpa `performance_schema`
  pagar ini tidak bekerja). Agent tidak pernah memutus sesi akun lain.
- Agent menyapu sendiri akun dump yang tertinggal (pekerjaan mati di
  tengah jalan): tiap jam, dari pemantau kesehatan agent, akun
  `dbpanel_dump_*` yang sandinya lebih tua dari 6 jam dan tidak punya
  sesi lagi dihapus (`DROP USER` menunggu kunci paling lama 5 detik) dan
  dicatat di journal. Akun yang masih punya sesi tidak diputus: itu dump
  yang masih berjalan (database besar bisa lebih dari 6 jam), jadi
  sapuannya ditunda ke jam berikutnya ("akun dump masih dipakai, sapuan
  ditunda" di journal). Server yang punya sumber replikasi dilewati
  (hapusan di master ikut tereplikasi).
- Repoint replika setelah promosi: agent replika hanya mau pindah ke master
  baru bila sumber yang dipin mati atau read-only, dan master baru memuat
  transaksi sumber itu. Panel yang disusupi bisa membuat sumber read-only
  sendiri (kunci promosi; master hanya terkunci sampai batas waktu kunci)
  lalu menunjuk MySQL milik penyerang yang memalsukan gtid_executed. Yang
  terdampak hanya replika itu (ia bisa diarahkan, lalu dibangun ulang, dari
  server penyerang); master tidak.
- Panel yang disusupi bisa, dengan menyebut replika yang sedang tersambung
  sebagai kandidat, mengunci master dan memicu promote terencana yang
  tidak diinginkan: data utuh, tetapi aplikasi tidak bisa menulis sampai
  dialihkan; semuanya tercatat di audit dan Telegram. Bila kandidat lalu
  dibuat tidak terjangkau, kunci bertahan sampai `sudo dbpanel agent
  unlock-master` dijalankan di server master (membuka otomatis berisiko
  dua master). Begitu pula bila replika yang terbaca saat kunci dipasang
  dibuat tidak terbaca (misalnya MySQL-nya dimatikan): master tetap
  baca-saja sampai replika itu terbaca lagi, `forget-replica`, atau
  `unlock-master`, walau panelnya sudah dicabut. Akibatnya henti tulis,
  bukan kehilangan data; membuka kunci setelah batas waktu akan membuka
  lagi jalan dua master.
- Pin `replica` menerima `createReplUser` ketat, jadi panel yang disusupi
  bisa membuat akun replikasi di replika (juga replika sebagian); replika
  lengkap sudah memilikinya sejak dump. Akun itu bisa membaca binlog
  replika dan menjadi donor CLONE, bukan menulis ke master.
- Panel yang disusupi tidak bisa membuang data master lama yang
  dikarantina tanpa persetujuan root di server itu (mode ketat, lihat
  "Peran dan sumber yang dipin"). Selama persetujuan berlaku (24 jam,
  untuk satu sumber), panel itu bisa membangun ulangnya dari sumber
  tersebut, jadi seluruh data MySQL-nya diganti salinan master baru;
  persetujuan itu tidak membolehkannya memasang MySQL atau menghapus akun
  selain akun dump sementara di sana, dan karantina baru berakhir bila
  setiap database di server itu memang hasil salinan dari sumber.
  Amankan data tertinggal (dan unduh berkasnya) sebelum menyetujui bangun
  ulang; berkas salvage yang sudah ditulis tidak ikut terhapus oleh bangun
  ulang. Tanpa persetujuan pun, panel yang disusupi masih bisa menghapus
  berkas salvage di server itu, dan mengganti berkas CA panel di sana
  (`mysql.tlsTrust`), sehingga pemeriksaan sertifikat dari server itu bisa
  rusak atau melemah. Panel sendiri penerbit CA itu, dan data MySQL tidak
  bisa dihapus lewat perintah itu.
- Salvage di master lama hanya mengambil transaksi yang ditemukan
  pemeriksaan data tertinggal terakhir, satu run sekaligus, dengan batas
  8 GiB untuk direktorinya, jadi panel yang disusupi tidak bisa mengambil
  riwayat binlog lain lewat salvage.
- Agent membaca server lain (kandidat, master lama, replika) dengan TLS
  wajib; tanpa verifikasi sertifikat ("Perketat" belum dijalankan)
  sertifikatnya tidak diperiksa, tetapi koneksi tanpa TLS ditolak.
- Penjaga kunci promosi membaca replika di berkas replika yang pernah
  tersambung, pada port kandidat dan setiap port di daftar port replika.
  Replika yang tidak terbaca saat kunci dipasang (misalnya menolak akun
  replikasi kandidat) dilewati dan tidak menahan kunci; replika yang
  terbaca saat itu menahan kunci selama tidak terbaca lagi (lihat "Peran
  dan sumber yang dipin"). Master dibuka bila semua replika itu terbaca dan
  tidak ada yang bisa ditulis dengan transaksi master itu.
- **Kunci start master lama.** Master lama menyala baca-saja lewat
  `init_file` walau agent-nya tidak ada (lihat "Master lama" di
  "Promote"). Batasnya: baru diuji pada MySQL 8.0 tanpa AppArmor/SELinux
  yang ditegakkan; `--init-file` di baris perintah layanan mengalahkannya
  (dilaporkan begitu MySQL menyala); menghapus barisnya saat agent mati
  membuat MySQL menyala tanpa kunci itu; menghapus berkasnya saat agent mati
  membuat MySQL tidak mau menyala sampai agent menulisnya lagi. Pada promote
  darurat ketika agent master lama tidak terjangkau, karantina dan kunci
  start baru dipasang saat agent itu tersambung lagi.
- **Daftar koneksi.** Hanya akun layanan buatan agent (`dbpanel_repl`,
  `dbpanel_dump_<8 heksa>`) dan sesi agent sendiri dari server itu (akun
  admin agent dengan `program_name=dbpanel-agent`) yang tidak ditampilkan.
  Atribut itu bisa dipalsukan klien, tetapi hanya oleh sesi yang sudah
  memakai akun admin agent dari server itu sendiri. Lihat "Pengalihan
  aplikasi".
- **Pembuangan data dengan konfirmasi segar.** **Ulangi** dan **Setujui**
  pada pekerjaan yang membuang data (Resync ulang; Buat replika yang
  salinannya belum dibuat, dengan CLONE maupun dump, atau dengan database
  yang dibuang; Bangun ulang master lama; Lepas database dengan hapus data;
  Tambah database yang sudah ada di replika) meminta kata sandi panel lagi
  untuk pekerjaan itu saja. **Setujui** dan **Ulangi** Bangun ulang master
  lama membaca lagi koneksinya dan meminta centang `accept_connections`
  bila masih ada koneksi (atau daftarnya tidak bisa dipastikan). Dialog
  **Setujui** untuk database sisa percobaan sebelumnya menyebut setiap
  database yang dihapus. Pekerjaan yang dibuat sebelum promote tidak bisa
  diulang setelah master topologinya berganti. Pekerjaan seperti itu yang konfirmasi terakhirnya lebih dari 24
  jam lalu tidak bisa diulang ("mulai tindakan baru dari halaman
  servernya"), dan **Ulangi** selalu menjalankan pemeriksaan awal lagi
  sebelum membuang apa pun. Dialog **Resync ulang** (dan **Ulangi**-nya)
  mengelompokkan database replika menjadi "ada di master, disalin ulang"
  dan "hanya di replika, isinya hilang permanen"; tiap database kelompok
  kedua harus dicentang sendiri, dan panel mencocokkannya lagi dengan
  daftar database master yang dibaca langsung tepat sebelum pemeriksaan
  awal (pekerjaan ditolak bila daftar itu tidak bisa dibaca). **Lepas
  database** dengan hapus data juga meminta nama replika diketik.
  **Tambah database** yang sudah ada di replika menunggu dengan teks
  "Database X sudah ada di replika ini. Panel tidak bisa memastikan isinya
  sama dengan master. Isinya di replika akan dibuang permanen, lalu diganti
  salinan dari master."; **Setujui** meminta kata sandi dan nama setiap
  database itu diketik, lalu agent membuangnya tanpa binary log
  (`sql_log_bin=0`, riwayat GTID replika tidak berubah) dan memuat salinan
  master. Pertahanan ini melindungi dari salah klik dan konfirmasi basi,
  bukan dari panel yang disusupi: panel seperti itu tetap bisa
  memerintahkan pembuangan.
- Topologi tanpa verifikasi sertifikat ("Perketat" belum dijalankan) tetap
  rentan terhadap pemalsuan DNS nama sumber yang dipin bila panel disusupi:
  pin sumber menyimpan nama, dan pembangunan ulang menyambung ke nama itu.
  Dengan verifikasi, sertifikat master yang menjaga identitasnya.
- **Laporan agent dibatasi.** Agent yang mengirim laporan terlalu sering
  hanya kehilangan laporannya (sambungan tidak diputus), dan jumlahnya
  tampil di **Detail teknis**; satu pesan paling besar 1 MiB. Kejadian
  kunci start dan kunci promote juga dibatasi (10 beruntun, lalu satu tiap
  5 detik per server); kejadian pertama tiap jenis dalam 10 menit tetap
  diterima, jadi banjir satu jenis tidak menyembunyikan jenis lain. Lihat "Batas
  dari agent" di "Pemantauan".
- **Sensor kutipan data.** Nilai berkutip di pesan error MySQL (isi baris,
  mis. nama atau NIK pasien) disensor panel sebelum disimpan di
  `panel.db` (laporan agent, pesan dan log pekerjaan, galat terakhir
  replika, catatan promosi, hasil audit dari jawaban agent) dan sebelum
  dikirim ke Telegram. Sensornya heuristik: ia mengenali kutipan, bukan
  data. Data yang ditulis MySQL tanpa kutip (mis. teks `SIGNAL` trigger)
  tetap terbaca, dan nilai berkutip di pesan yang polanya tidak dikenal
  panel bisa tersisa sebagian bila nilainya sendiri memuat kutip. Teks
  lama di basis data, journal agent, dan log error MySQL di server
  database tidak disentuh. Lihat "Sensor kutipan data" di "Pemantauan".
- **Rantai hash audit** menangkap catatan yang diubah, disisipkan, atau
  dihapus di tengah oleh orang yang tidak menghitung ulang hash-nya. Ia
  tidak menangkap penulisan ulang seluruh `panel.db` atau penghapusan
  baris terakhir; untuk itu bandingkan dengan salinan di journal panel,
  sebaiknya yang diteruskan ke server lain. Lihat "Catatan audit".
- **Pin dilaporkan.** Setiap laporan pemantauan memuat pin peran dan sumber
  server. Master yang tidak dipin `master` (atau berkas pinnya tidak
  terbaca) tiga laporan berturut-turut memunculkan peringatan **Master
  tidak dipin**: di master seperti itu agent tidak menolak perintah yang
  bisa merusak replikasi. Lihat "Peran dan sumber yang dipin".
- **Penolakan selama kunci promosi.** Setelah promote terencana gagal dan
  panel tidak bisa memastikan kunci master sudah dibuka, panel menolak
  promote darurat, **Resync ulang**, **Buat replika**, **Tambah database**,
  **Lepas database** yang menghapus datanya, dan **Ulangi** pekerjaan itu
  di topologi tersebut sampai tenggat kunci +60 detik lewat, supaya penjaga kunci
  tidak mengkarantina master yang sehat (lihat "Promote terencana dan
  darurat").
- **Pairing server lokal hanya dari loopback.** Token `local-pair-token`
  hanya diterima dari alamat loopback (`127.0.0.0/8`, `::1`); percobaan dari alamat lain ditolak
  tanpa memakai token dan dicatat di audit (`agent_pair`). Bila panel tidak
  bisa memeriksa token itu, permintaan ditolak (gagal tertutup).
- **Reverse proxy di depan panel tidak didukung.** Pemeriksaan loopback
  pairing lokal, batas login per IP, dan IP di catatan audit memakai
  alamat asal sambungan yang sebenarnya. Di belakang proxy di host panel
  semua permintaan tampak datang dari `127.0.0.1`: token pairing lokal
  diterima dari mana saja, batas login per IP menjadi satu batas untuk
  semua orang, dan catatan audit kehilangan IP aslinya.
- **Nomor seri sertifikat MySQL diperiksa.** Panel hanya mencatat
  sertifikat MySQL yang dipasang bila nomor seri yang disajikan MySQL sama
  dengan yang diterbitkan panel (lihat "Sertifikat MySQL").
- **Tanda "terverifikasi" mengikuti laporan kesehatan.** Tanda bahwa
  replika memverifikasi sertifikat master dicabut begitu laporan replika
  menunjukkan verifikasi mati, misalnya replikasi diubah di luar panel.
  Tanda itu berasal dari laporan agent replika: agent yang disusupi di
  server replika bisa mengaku terverifikasi. Laporan dari agent yang
  versinya berbeda dari panel tidak mengubah tanda itu.
- Sertifikat agent dan sertifikat listener panel berlaku 2 tahun, CA 10
  tahun. Belum ada perpanjangan otomatis (belum dijadwalkan; direncanakan
  setelah Rencana 2); sampai saat itu, pasang ulang agent sebelum
  sertifikatnya habis. Listener panel membuat sertifikat baru setiap kali
  panel dimulai.

### Keamanan rilis

`SHA256SUMS` diunduh dari rilis yang sama dengan binary-nya, jadi
pemeriksaannya hanya menangkap unduhan rusak atau tertukar, bukan rilis
yang dipalsukan (mis. akun GitHub yang dibobol). Rilis belum
ditandatangani; tanda tangan `SHA256SUMS` (minisign/cosign) dengan kunci
publik yang disematkan di `install.sh` belum dikerjakan (Rencana 2 tidak
memuatnya) dan harus ada sebelum fitur pembaruan agent otomatis. Sampai saat itu, pasang dari tag rilis
tertentu (`DBPANEL_VERSION=...`, dan URL `install.sh` dari tag yang sama,
bukan `main`) dan bandingkan `SHA256SUMS` dengan salinan yang didapat
lewat jalur lain bila memungkinkan.

## Build dari sumber

Butuh Go 1.26.10 atau lebih baru (`go.mod`; Go yang lebih tua mengunduh
toolchain itu sendiri). Binary statis (`CGO_ENABLED=0`):

```sh
make build   # -> dist/dbpanel
make test    # go vet, go test, dan pemeriksaan skrip instalasi
```

Untuk dua arsitektur sekaligus (`linux/amd64` dan `linux/arm64`):

```sh
make release VERSION=0.1.0   # -> dist/dbpanel_linux_{amd64,arm64} dan dist/SHA256SUMS
sudo DBPANEL_BIN=dist/dbpanel_linux_amd64 DBPANEL_BIN_DIR=dist bash scripts/install.sh
```

`DBPANEL_BIN_DIR=dist` menyalin kedua binary ke `/var/lib/dbpanel/panel/bin/`
agar panel dapat melayani agent berarsitektur lain tanpa internet.

## Menjalankan test

```sh
make test       # go vet, semua test (batas waktu 60 menit; paket driver MySQL saja sekitar 35 menit), dan pemeriksaan skrip instalasi
make test-e2e   # hanya test end-to-end
go test -short ./...   # cepat: tanpa test end-to-end
```

Test driver MySQL dan test end-to-end menjalankan `mysqld` sungguhan
(sekali pakai, di `/var/tmp`, port acak di 127.0.0.1), jadi `mysqld` harus
terpasang di mesin ini, mis. `sudo apt install mysql-server-8.0`. Tanpa
`mysqld` test itu dilewati (skip), bukan gagal. Bila dijalankan sebagai
root, `mysqld` berjalan sebagai user `nobody` (ganti dengan
`DBPANEL_TEST_MYSQL_USER`); direktori dasar bisa diganti dengan
`DBPANEL_TEST_MYSQL_DIR`.

Test end-to-end (perintah `make test-e2e` atau
`go test -timeout 30m -run TestE2E ./internal/panel/`) menjalankan panel,
agent dan `mysqld` sungguhan, lalu lewat UI:

- `TestE2EHTTPS`: sertifikat HTTPS yang dibuat panel sendiri, halaman
  `/sertifikat` dan unduhannya lewat HTTP biasa, pengalihan 301 ke
  `https://`, cookie sesi `Secure`, lalu perintah pasang agent persis
  seperti yang tampil di halaman server dijalankan dengan `bash` (curl
  berpin, `sha256sum`, skrip pemasang, binary hasil build pohon ini,
  `dbpanel agent pair` dengan pin) sampai agentnya online. Yang butuh root
  diganti tiruan di `PATH` (`sudo` menjalankan skrip dengan jalur
  `/usr/local/bin`, unit systemd, dan direktori agent dipindah ke direktori
  sementara; `systemctl`, `id`, `sleep`); perintah yang sama dengan pin
  lain harus ditolak `curl`;
- `TestE2ECreateReplica`: hubungkan MySQL, siapkan master (dengan
  sertifikat MySQL), buat replika untuk satu database, tambah database
  kedua, dan buat replika kedua dengan CLONE; replikasi kedua replika
  harus memverifikasi sertifikat master. Master juga sempat diberi
  sertifikat dari CA MySQL panel yang hanya memuat nama host lain:
  replikasi harus gagal (error 2026), dan sertifikat yang memuat
  `127.0.0.1` harus bisa tersambung lagi, jadi yang diperiksa memang nama,
  bukan hanya rantai sertifikat;
- `TestE2EMonitoring`: `STOP REPLICA` langsung di MySQL replika harus
  tampil sebagai Berhenti (di luar panel), membuka peringatan, dan
  mengirim satu pesan ke Telegram palsu (server `httptest`, tidak ada
  panggilan ke internet) dalam 30 detik; lalu Start, Stop, kunci yang
  terlepas dan Kunci kembali, dan Resync ulang;
- `TestE2EPinnedRole`: agent master menolak `mysql.dropDatabases` karena
  perannya dipin, dan panel menolak menghapus master yang masih punya
  replika.
- `TestE2EPromotePlanned`: tiga `mysqld` (master dan dua replika), promote
  terencana lewat layar pertimbangan dengan token konfirmasi; master baru
  bisa ditulis, replika lain mengikutinya, master lama `quarantined` dan
  `super_read_only=ON`, halaman master lama menampilkan "Kunci start:
  Terkunci"; layar pengalihan menampilkan koneksi aplikasi uji ke master
  lama dan baru menulis "Tidak ada koneksi aplikasi tersisa" setelah
  koneksi itu ditutup; **Sudah saya alihkan**; **Periksa data tertinggal**
  kosong; dengan koneksi aplikasi dibuka lagi, **Bangun ulang sebagai
  replika** tanpa centang `accept_connections` ditolak dan tidak memulai
  apa pun, dengan centang berhasil, dan kunci start dilepas; pesan
  Telegram memuat alamat master baru;
- `TestE2EPromoteEmergency`: dua transaksi tertahan di relay log replika A,
  `mysqld` master dimatikan paksa (agent-nya tetap hidup): layar
  pertimbangan membaca binlog di disk master (angka pasti) dan antrean
  terapan A; promote darurat (token dan nama master) menerapkan relay log
  dulu; replika B diarahkan ke A; karantina menulis kunci start (berkas dan
  baris `init_file`). Agent master lama dihentikan dan `mysqld`-nya
  dinyalakan: ia menyala baca-saja karena kunci start; orang membukanya
  dengan `SET GLOBAL super_read_only = OFF` dan menulis satu baris, lalu
  agent dinyalakan dengan laporan menyalanya dibuang sebelum sampai ke
  panel: ia memasang baca-saja lagi dan panel membuka tepat satu peringatan
  `master_start_late` dari hasil menyala di laporan pemantauan. **Periksa
  data tertinggal** menemukan 1 transaksi di `a.t` dan menutup peringatan
  itu, yang tidak terbuka lagi pada dua laporan berikutnya; **Amankan**
  menulis berkas yang diunduh lewat HTTPS dengan token dan memuat
  `INSERT`-nya, master lama dibangun ulang sebagai replika setelah root
  menyetujuinya dengan `approve` (konfigurasinya tidak lagi memuat
  `init_file` dan berkas kuncinya hilang), lalu berkas salvage, yang
  bertahan setelah bangun ulang, dihapus dengan token.

Test ini dilewati dengan `-short`.

Pemeriksaan di browser sungguhan (Chromium lewat Playwright) untuk dialog
konfirmasi kata sandi tidak ikut `go test` biasa:

```sh
go test -tags e2e_browser -timeout 10m -run TestE2EBrowserConfirm ./internal/panel/
```

Test itu menyiapkan master dan replika seperti di atas, lalu
`scripts/e2e_browser_confirm.js` login, klik **Resync ulang**, memberi
kata sandi salah (pesan galat tampil, tidak ada yang dikirim), lalu yang
benar: formulir harus terkirim dengan `confirm_token`. Butuh Node.js
dengan paket `playwright` dan Chromium-nya (test dilewati bila tidak ada).

Tur tangkapan layar (alat pengembang, bukan test) memotret setiap halaman
dalam kedua tema dan dua lebar (1440 dan 390 px), termasuk dialog
konfirmasi dan halaman Replikasi dengan dua topologi:

```sh
make screens                         # ke /tmp/dbpanel-layar
make screens SCREENS_DIR=/tmp/layar  # direktori lain
```

`TestE2EScreenshotTour` (tag `e2e_browser`, jalan hanya bila
`DBPANEL_SCREENSHOT_DIR` diisi) menyiapkan master dan dua replika,
menjalankan promote terencana, lalu `scripts/e2e_screenshot_tour.js`
menulis `<nn>-<halaman>-<tema>-<lebar>.png`, misalnya
`02-dashboard-gelap-390.png` (64 berkas). Halaman yang lebih lebar dari
layar dicetak sebagai `OVERFLOW <berkas>` dan membuat test gagal (setelah
semua tangkapan selesai). Kebutuhannya sama dengan
test browser di atas.

## Mengubah tampilan

Template HTML ada di `web/templates/`, CSS sumber di `web/src/app.css`.
Panel memakai Content-Security-Policy ketat: tidak ada skrip inline,
atribut `on...=` atau `style=` di template, dan Alpine.js yang dipakai
adalah build CSP. Atribut Alpine (`x-data`, `x-text`, `@click`, ...) hanya
boleh menyebut komponen, properti atau method yang didaftarkan di
`web/static/app.js`, bukan ekspresi JavaScript.
Aset hasil build ikut di-embed ke binary dan dikomit, jadi build biasa tidak
butuh Node.js maupun internet. Setelah mengubah template atau CSS:

```sh
scripts/tailwind.sh   # kompilasi Tailwind CSS + daisyUI -> web/static/app.css
scripts/vendor.sh     # unduh ulang Alpine.js (build CSP) -> web/static/vendor/ (bila ganti versi)
make build
```
