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
lokal (disarankan). Agent menyimpan akun itu tersegel di
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
(`mysql.lockForPromote`, `mysql.promote`, `mysql.repoint`). Ia hanya bisa
dibangun ulang (hapus database, dump atau CLONE, dikonfigurasi sebagai
replika, replikasi) dari sumber yang dipin, yaitu master barunya, atau
dilepas seperti replika. Agent tidak pernah menurunkan pin `quarantined`
(atau pin lain) berdasarkan keadaan MySQL.

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
kunci dihapus lagi). Selama berkas itu ada di master, agent
memasang lagi `super_read_only` pada tiap pemeriksaan kesehatan bila
mati (misalnya sesudah MySQL restart). Pada tenggat agent membaca kandidat
dengan akun replikasi yang dikirim panel (hanya disimpan di memori): bila
kandidat bisa ditulis dan `gtid_executed`-nya memuat UUID server ini,
master mengkarantina dirinya (`quarantined`, sumber = kandidat); bila
kandidat masih read-only, agent menunggu 60 detik dan membaca lagi; bila masih
read-only, agent membaca juga setiap replika di berkas replika yang pernah
tersambung (di port kandidat, dengan akun yang sama): bila salah satunya
bisa ditulis dan `gtid_executed`-nya memuat UUID server ini (replika lain
yang dipromosikan darurat selama kunci berdiri), master mengkarantina
dirinya di bawah replika itu; bila tidak ada, kunci dibuka. Selain itu (kandidat tidak terjangkau, atau
agent dimulai ulang sehingga akunnya hilang) kunci tetap dan dicoba lagi
tiap menit, dilaporkan ke panel paling sering sejam sekali, sampai
`unlock-master`. `mysql.unlockPromote` membatalkan kunci sebelum tenggat
hanya bila kandidat terbaca masih read-only. Kunci `lock-master` tidak
pernah disentuh keduanya. Bila pemeriksaan agent jatuh tepat di antara
langkah `unlock-master` (MySQL dibuka, lalu berkas kunci dihapus), MySQL
bisa terkunci lagi; jalankan `unlock-master` sekali lagi.

Agent juga mencatat replika yang membaca binlog server ini pada tiap
pemeriksaan kesehatan di `/var/lib/dbpanel/replicas-seen`: satu baris per
host, `<host> <waktu RFC3339>`, dengan waktu terbaru; host yang tidak
terlihat lebih dari setahun dibuang. Host diambil dari kolom `HOST` daftar
proses MySQL tanpa port, jadi berupa nama bila `skip_name_resolve` mati.

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
memeriksa MySQL-nya sendiri dan melapor ke panel. **Dashboard** menampilkan
jumlah master, replika, dan peringatan aktif, lalu satu kartu per
topologi: master dengan statusnya dan setiap replika satu baris (status
berwarna, penanda kunci, tertinggal berapa detik, kapan terakhir dicek).
Halaman berganti sendiri saat status berubah; tidak perlu dimuat ulang.
Server yang belum menjadi master atau replika tampil **Belum dipantau**.

Status replika, dalam kata sederhana:

| Status | Artinya |
|---|---|
| Sinkron | replikasi berjalan dan mengikuti master |
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
90 hari; riwayat disimpan 180 hari, lihat Retensi di bawah). Pesan error MySQL apa adanya hanya ada di **Detail teknis** halaman
replika.

**Sensor kutipan data.** Pesan error MySQL sering mengutip isi baris
(`Duplicate entry 'Budi' for key 'nama'`). Sebelum disimpan atau
dikirim, panel mengganti setiap nilai berkutip (`'…'` atau `"…"`,
termasuk escape `\'` dan `''`) dengan `'…'`, jadi yang tersimpan adalah
`Duplicate entry '…' for key '…'`. Berlaku untuk error MySQL di laporan
agent (`mysql_error`, `last_io_error`, `last_sql_error`: snapshot,
garis waktu, peringatan, dan Telegram yang dibangun darinya), log dan
pesan pekerjaan, serta hasil di catatan audit bila berasal dari jawaban
agent. Teks di luar kutipan tetap utuh: nomor error, nama tabel, posisi
binlog, dan angka panjang lain. Kutipan yang tidak ditutup menyensor
sisa barisnya; pernyataan SQL setelah `Query: '` di error replikasi
disensor sampai akhir pesan. Teks yang tersimpan sebelum versi ini tidak diubah di
basis data (snapshot lama disensor saat dibaca ulang).

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
**Pengaturan > Ambang dan interval** (interval pemeriksaan 10 sampai 3600
detik). Tombol **Periksa sekarang** di halaman server meminta laporan baru
saat itu juga (paling sering sekali per 10 detik).

**Batas dari agent.** Panel menerima paling banyak 10 laporan beruntun
dari satu server, lalu satu tiap 2 detik; laporan yang lebih sering dari
itu dibuang tanpa memutus sambungan, dicatat di log panel, dan jumlahnya
tampil di **Detail teknis** halaman server ("12 laporan dibuang dalam 1 jam
terakhir"). Laporan tertunda yang dikirim agent setelah tersambung lagi
(paling banyak 200) mendapat jatah tersendiri, sekali per 10 menit per
server, jadi agent yang sehat praktis tidak pernah mencapai batas ini;
laporan yang dibuang biasanya berarti agent rusak atau diubah. Satu pesan dari agent (dan dari
panel ke agent) paling besar 1 MiB; pesan yang lebih besar memutus
sambungan, dan agent tersambung lagi sendiri.

**Retensi.** Sekali sehari panel menghapus peringatan yang sudah ditutup
lebih dari 90 hari dan riwayat status (linimasa) yang lebih tua dari 180
hari. Kedua angka bisa diubah di **Pengaturan > Retensi** (1 sampai 3650
hari). Peringatan yang masih aktif tidak pernah dihapus, dan log audit
serta log pekerjaan tidak pernah dipangkas.

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
mengakhirinya lebih awal. Selama
itu peringatan server tersebut tetap dicatat dan tampil di panel, tetapi
tidak dikirim ke Telegram. Saat Jangan ganggu berakhir, peringatan yang
masih terbuka dikirim satu kali.

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
  untuk melanjutkan. Pekerjaan berjalan tanpa berhenti, kecuali bila di
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
database). Yang tidak ditampilkan: akun `dbpanel_*`, replika yang membaca
binlog, thread MySQL sendiri, koneksi agent sendiri, dan sesi akun admin
MySQL yang dipakai agent bila tersambung dari server itu sendiri (socket,
`localhost`, `127.0.0.1`, `::1`). Sesi akun admin itu dari host lain
ditampilkan dengan tanda "akun admin panel" dan ikut dihitung di
konfirmasi bangun ulang. Tabel koneksi hanya dibaca untuk master, master
lama, dan replika topologi yang pernah dipromosikan. Karena itu pakai akun admin khusus untuk panel
(lihat "Hubungkan MySQL"), bukan `root` atau akun yang juga dipakai orang
atau aplikasi di server itu.
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
`super_read_only` lebih dulu sebelum hal lain. Halaman server master lama
memuat kotak **Master lama** dengan:

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
  diamankan, lalu meminta kata sandi dan nama server. Berkas yang sudah
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
  berhasil di agent yang sama.
- **Hapus server:** melepas pin `quarantined` (agent tidak lagi menjaga
  baca-saja saat start). Seperti bangun ulang, baru tersedia setelah
  **Sudah saya alihkan**.

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
yang tidak ada dicatat sebagai `(tidak dikenal)` dengan hash pendek. Saring
menurut server dan tanggal. Catatan tidak bisa diubah atau dihapus lewat
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
membaca `panel.db`; keluar 0 bila utuh, 1 bila putus):

```sh
sudo -u dbpanel dbpanel panel audit-verify
```

Batas rantai ini perlu dipahami. Hash-nya tidak memakai kunci rahasia: siapa
pun yang bisa menulis `panel.db` bisa menghitung ulang semua hash dari baris
yang ia ubah sampai akhir, dan hasilnya tetap "Rantai utuh". Menghapus
baris-baris *terakhir* juga tidak memutus rantai. Kedua hal itu hanya
ketahuan dengan membandingkan isi `panel.db` dengan salinan kedua: setiap
catatan juga ditulis ke log panel (`journalctl -u dbpanel-panel`, pesan
`audit`, dengan `stored=false` bila basis data menolak catatan itu). Log itu
bisa dibaca anggota grup `adm` dan `systemd-journal` di host panel, lebih
luas daripada `panel.db` (0600), jadi batasi keanggotaan grup itu. Saat
panel versi ini pertama kali dijalankan, rantai dihitung sekali untuk
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
  pemantau agent, setiap pemeriksaan (15 detik), memutus sesi akun
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
  dua master).
- Pin `replica` menerima `createReplUser` ketat, jadi panel yang disusupi
  bisa membuat akun replikasi di replika (juga replika sebagian); replika
  lengkap sudah memilikinya sejak dump. Akun itu bisa membaca binlog
  replika dan menjadi donor CLONE, bukan menulis ke master.
- Panel yang disusupi bisa menghapus data tertinggal di master lama yang
  dikarantina: lewat bangun ulang sebagai replika (seluruh data MySQL
  diganti salinan master baru) atau `dropDatabases`. Amankan data
  tertinggal (dan unduh berkasnya) sebelum menyetujui bangun ulang;
  berkas salvage yang sudah ditulis tidak ikut terhapus.
- Salvage di master lama hanya mengambil transaksi yang ditemukan
  pemeriksaan data tertinggal terakhir, satu run sekaligus, dengan batas
  8 GiB untuk direktorinya, jadi panel yang disusupi tidak bisa mengambil
  riwayat binlog lain lewat salvage.
- Agent membaca server lain (kandidat, master lama, replika) dengan TLS
  wajib; tanpa verifikasi sertifikat ("Perketat" belum dijalankan)
  sertifikatnya tidak diperiksa, tetapi koneksi tanpa TLS ditolak.
- Penjaga kunci promosi membaca replika di berkas replika yang pernah
  tersambung pada port kandidat. Replika di port lain, atau yang menolak
  akun replikasi kandidat, dilewati; master tetap dibuka bila tidak ada
  replika terbaca yang bisa ditulis dan memuat transaksi master itu.
- Topologi tanpa verifikasi sertifikat ("Perketat" belum dijalankan) tetap
  rentan terhadap pemalsuan DNS nama sumber yang dipin bila panel disusupi:
  pin sumber menyimpan nama, dan pembangunan ulang menyambung ke nama itu.
  Dengan verifikasi, sertifikat master yang menjaga identitasnya.
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
  `super_read_only=ON`; layar pengalihan menampilkan koneksi aplikasi uji
  ke master lama; **Sudah saya alihkan**; **Periksa data tertinggal**
  kosong; **Bangun ulang sebagai replika**; pesan Telegram memuat alamat
  master baru;
- `TestE2EPromoteEmergency`: dua transaksi tertahan di relay log replika A,
  `mysqld` master dimatikan paksa (agent-nya tetap hidup): layar
  pertimbangan membaca binlog di disk master (angka pasti) dan antrean
  terapan A; promote darurat (token dan nama master) menerapkan relay log
  dulu; replika B diarahkan ke A. Master lama dihidupkan dengan satu baris
  yang masuk sebelum agent-nya mengunci; agent mengkarantinanya saat
  start, **Periksa data tertinggal** menemukan 1 transaksi di `a.t`,
  **Amankan** menulis berkas yang diunduh lewat HTTPS dengan token dan
  memuat `INSERT`-nya, berkas dihapus dengan token, lalu master lama
  dibangun ulang sebagai replika.

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
