# dbpanel

dbpanel adalah panel web untuk mengelola replikasi MySQL antar-server di
jaringan lokal, ditujukan bagi tim IT rumah sakit yang ingin pemasangan
semudah aaPanel: satu perintah untuk panel, satu perintah untuk tiap server
database. Satu binary `dbpanel` menjalankan dua peran: **panel** (UI web,
port 8080) dan **agent** (berjalan di tiap server database, terhubung ke
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
`/etc/dbpanel/` dan `/var/lib/dbpanel/` (keduanya 0700), memasang service
`dbpanel-panel`, lalu mencetak alamat panel, pengguna dan sandi awal.
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
`/var/lib/dbpanel/bin/` (nama lain diabaikan). Skrip aman
dijalankan ulang untuk memperbarui panel: binary diganti dan service dimulai
ulang.

## Login pertama

Buka `http://<ip-panel>:8080` dan masuk sebagai `admin`. Sandi awal
dicetak di akhir instalasi dan tersimpan di
`/etc/dbpanel/admin-initial-password` (hanya bisa dibaca root):

```sh
sudo cat /etc/dbpanel/admin-initial-password
```

Hapus berkas itu setelah login pertama (`sudo rm
/etc/dbpanel/admin-initial-password`).

Lupa sandi? Buat sandi baru (dicetak ke layar):

```sh
sudo dbpanel panel reset-password
```

`reset-password` juga mengakhiri semua sesi yang sedang login (dalam
beberapa detik, tanpa restart panel). Sesi berakhir setelah 12 jam tanpa
aktivitas dan paling lama 24 jam setelah login.

Login dibatasi 5 percobaan per alamat IP dan 20 sandi salah per nama
pengguna dalam 15 menit, dan hanya beberapa pemeriksaan sandi yang berjalan
bersamaan (selebihnya "Server sibuk, coba lagi sebentar"). Bila akun
terkunci, misalnya karena orang lain sengaja salah sandi berulang kali,
penghitung itu ada di memori panel: `sudo systemctl restart dbpanel-panel`
menghapusnya (semua sesi yang sedang login juga berakhir).

## Tambah server database

1. Di panel buka **Server > Tambah server**, isi nama, simpan.
2. Salin perintah pemasangan yang ditampilkan, misalnya

   ```sh
   f=$(mktemp) && curl -fsSL --max-redirs 0 -K - <<<'url = "http://192.168.1.10:8080/agent/install?token=..."' -o "$f" && echo "<sha256 skrip>  $f" | sha256sum -c - && sudo bash "$f"
   ```

   Perintah ini terdiri dari tiga langkah:
   1. `curl` mengunduh skrip pemasang ke berkas sementara baru dari
      `mktemp` (nama acak, hanya bisa dibaca pemiliknya; nama tetap di
      `/tmp` bisa disiapkan lebih dulu oleh pengguna lain di server itu
      lalu diubah tepat sebelum `sudo`). `curl` tidak mengikuti pengalihan
      (`--max-redirs 0`), agar token tidak ikut terkirim ke host lain;
   2. `sha256sum -c` mencocokkan isi skrip dengan sha256 yang ditampilkan
      panel (dihitung dari skrip yang sama persis yang disajikan untuk token
      itu);
   3. hanya bila cocok, skrip dijalankan dengan `sudo`.

   Pairing berjalan lewat HTTP polos, jadi pihak yang bisa menyadap atau
   mengubah lalu lintas antara server database dan panel (on-path) bisa
   mengganti skrip, binary agent, atau CA panel di jawaban pairing. Hash
   menutup celah itu: skrip yang diubah ditolak oleh `sha256sum -c` sebelum
   apa pun berjalan sebagai root. Skrip yang sudah terverifikasi membawa
   sha256 binary agent (unduhan yang tidak cocok ditolak dengan "Checksum
   binary agent tidak cocok; kemungkinan ada yang menyusup di jaringan") dan
   sidik jari CA panel, yang diberikan ke `dbpanel agent pair --ca-sha256`:
   agent menolak jawaban pairing dengan CA lain ("CA panel tidak cocok dengan
   yang diharapkan") dan menghubungi alamat agent dari skrip
   (`--agent-addr`), bukan dari jawaban pairing. Unduhan binary dan
   `dbpanel agent pair` juga tidak mengikuti pengalihan (redirect). Hash di perintah berubah
   bila token, alamat panel, CA, atau binary agent di panel berubah; bila
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
sertifikat agent yang kuncinya diduga bocor. Server panel sendiri tidak perlu (dan tidak bisa) dipasangi
agent: agent-nya tertanam di panel.

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

## Replikasi MySQL

Alurnya: **Hubungkan MySQL** di setiap server, **Siapkan sebagai master**
di server sumber, lalu **Buat replika** dari halaman master. Setiap alur
adalah pekerjaan bertahap di **Pekerjaan**; tahap yang butuh persetujuan
menunggu tombol Anda dan menjelaskan akibatnya.

### Hubungkan MySQL

Di halaman server, masukkan akun admin MySQL di server itu (biasanya
`root`). Kosongkan host agar agent memakai socket lokal (disarankan; juga
bekerja untuk `root` dengan `auth_socket`). Agent menyimpan akun itu
tersegel di `/etc/dbpanel/mysql.cred` di server itu; panel tidak
menyimpannya dan tidak menampilkannya lagi. Bila akun kurang hak, panel
menampilkan perintah `GRANT` yang perlu dijalankan.

UI panel masih HTTP biasa: kata sandi MySQL yang Anda ketik melintas
jaringan tanpa enkripsi. Buka panel hanya dari jaringan manajemen yang
tepercaya, atau lewat tunnel SSH:

```sh
ssh -L 8080:127.0.0.1:8080 admin@<ip-panel>   # lalu buka http://127.0.0.1:8080
```

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
  replikasi hanya bisa masuk lewat TLS. Sertifikat master belum
  diverifikasi (belum ada CA untuk itu), jadi ini melindungi dari
  penyadapan, belum dari penyamaran master oleh penyerang aktif di jaringan.

### Akun yang dibuat panel di master

- `dbpanel_repl@'%'`: hanya `REPLICATION SLAVE` dan `BACKUP_ADMIN` (untuk
  CLONE), sandi acak 24 karakter. Sandinya tersimpan di setiap replika di
  tabel `mysql.slave_master_info`; **jangan beri akun di replika
  `SELECT ON *.*`** (misalnya akun laporan), pakai grant per database.
- `dbpanel_dump_<8 heksa>@'<alamat replika>'`: akun sementara untuk satu
  dump (baca data, kunci singkat), dibuat sesaat sebelum dump dan dihapus
  sesudahnya, juga bila dump gagal atau dibatalkan. Bila penghapusan gagal,
  log pekerjaan menyebut perintah `DROP USER` yang perlu dijalankan.

Agent hanya mau membuat, mengubah, atau menghapus akun yang namanya
diawali `dbpanel_`, dan menolak perintah yang menghapus data, menimpa
server, atau mengubah replikasi di server yang sedang dibaca replika lain
(master), apa pun yang diminta panel. Di master yang dipin agent tidak
pernah mengubah akun yang sudah ada (sandi dan haknya tetap). Bila "Siapkan
master" dijalankan ulang untuk topologi baru sementara `dbpanel_repl` masih
ada dari persiapan sebelumnya, pekerjaan berhenti dan menyebut perintah
`DROP USER` yang perlu dijalankan di master (hanya bila tidak ada replika
lain yang memakainya).

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

Agent mengingat peran servernya di `/etc/dbpanel/role` (`master` atau
`replica`) dan master yang diikuti replika di `/etc/dbpanel/source`
(`host:port`). Berkas peran ditulis saat "Siapkan master" atau "Buat
replika" pertama berhasil; berkas sumber saat replikasi atau salinan data
(dump, CLONE) pertama kali diarahkan ke master. Sesudah itu agent sendiri
menolak perintah panel yang tidak cocok, walau panelnya disusupi: master
menolak hapus database, salin data dari server lain, dijadikan atau
dikonfigurasi sebagai replika, dikunci read-only, membuat akun selain akun
replikasi `dbpanel_repl` dan akun dump sementara `dbpanel_dump_<8 heksa>`
dari satu alamat IP, mengubah akun yang sudah ada, mengganti `server_id`,
dan restart MySQL kecuali sekali sesudah pengaturan yang memang menunggu
restart; replika menolak buka kunci, dijadikan master, akun replikasi, dan
master selain sumbernya.

Pin **master** tidak bisa diubah dari panel. Pin **replika** (peran dan
sumbernya) bisa dilepas panel sendiri, setelah replikasinya dihentikan
dan direset, lewat **Lepas dan hentikan replikasi**, **Hapus server**, atau
saat pekerjaan **Buat replika** dibatalkan. Artinya server itu kembali tanpa
peran dan tanpa sumber, dan sesudahnya panel bisa lagi mengarahkannya ke
master mana pun atau menimpa datanya: pin replika melindungi dari
kesalahan, bukan dari panel yang disusupi. Selain itu, peran dan sumber
hanya diubah di server itu sendiri:

```sh
sudo dbpanel agent set-role master       # atau replica, atau unassigned (hapus peran dan sumber)
sudo dbpanel agent set-source 10.0.0.5:3306
```

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

### Cadangkan kunci panel

Sandi replikasi disimpan di `panel.db` tersegel dengan
`/etc/dbpanel/panel.key`. **Cadangkan `/etc/dbpanel/panel.key` bersama
`/var/lib/dbpanel/panel.db`** di tempat aman di luar host panel. Tanpa
kunci itu sandi replikasi tidak bisa dibuka lagi: menyiapkan master ulang
membuat sandi baru, dan setiap replika harus dibuat ulang. Simpan kunci
terpisah dari cadangan database biasa; siapa pun yang memegang keduanya
bisa membuka sandi replikasi.

### Memindahkan panel ke server lain

1. Hentikan panel: `sudo systemctl stop dbpanel-panel`.
2. Salin seluruh `/etc/dbpanel` (termasuk `ca.crt`, `ca.key`, `panel.key`)
   dan `/var/lib/dbpanel` ke server baru dengan izin yang sama (direktori
   0700, milik root).
3. Pasang panel di server baru (`install.sh`), jalankan dengan
   `--public-host <alamat baru>` bila perlu.
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
| Agent perlu dimutakhirkan | agent versi lama yang belum bisa memantau (lihat "Pembaruan agent") |
| Belum siap | replika sedang dibuat atau disync ulang, atau pembuatannya gagal |

Penanda merah **Kunci terlepas** tampil di samping status apa pun bila
`super_read_only` di replika mati: aplikasi bisa menulis langsung ke
replika dan isinya bisa berbeda dari master.

Status master: **Sehat**, **Terkunci** (`super_read_only` menyala, aplikasi
tidak bisa menulis), **Tidak terjangkau**, **Agent offline**, dan **Diduga
tumbang** (agent atau MySQL master tidak menjawab lebih dari 30 detik).
Untuk master yang diduga tumbang, halamannya menampilkan langkah manual
"Yang bisa dilakukan sekarang"; panel belum bisa memindahkan peran master
sendiri.

Halaman master dan replika juga menampilkan versi, uptime, koneksi, disk,
RAM, beban, lag, umur binlog, dan garis waktu kejadian 7 hari (disimpan 90
hari). Pesan error MySQL apa adanya hanya ada di **Detail teknis** halaman
replika.

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
6. Isi **Alamat dasar panel** (mis. `http://192.168.1.10:8080`) agar pesan
   memuat tautan ke halaman server. Centang hanya jenis peringatan yang
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
Master: RS-1 · Sejak: 02:14 · http://192.168.1.10:8080/replicas/3
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

## Catatan audit

Menu **Audit** mencatat setiap tindakan yang mengubah sesuatu: login
berhasil dan gagal, logout, perubahan pengaturan, tes Telegram, Siapkan
master, Buat replika, tambah dan lepas database, setujui/ulangi/batalkan
pekerjaan, serta semua tombol di "Kontrol replika". Setiap baris berisi
waktu, pengguna, IP, tindakan, target, dan hasilnya (berhasil, atau alasan
ditolak); sandi dan token tidak pernah dicatat. Perubahan chat id dan
alamat dasar panel dicatat dengan nilai barunya. Isi rincian tiap baris
dibatasi (2 KiB); untuk tindakan yang ditolak atau gagal hanya nama isian
dan panjang nilainya yang dicatat, dan login gagal dengan nama pengguna
yang tidak ada dicatat sebagai `(tidak dikenal)` dengan hash pendek. Saring
menurut server dan tanggal. Catatan tidak bisa diubah atau dihapus lewat
panel dan tidak dipangkas; ia ikut tersimpan di `panel.db`, jadi ikut
dicadangkan bersamanya. Penjagaannya ada di dalam `panel.db` sendiri, jadi
catatan ini tahan ubah terhadap aplikasi, bukan terhadap root di host panel
(yang bisa mengubah berkasnya).

## Pembaruan agent

Perbarui panel dulu (jalankan ulang `install.sh`, lihat "Pasang panel").
Agent tertanam di panel ikut terbarui. Agent di server database yang
terlalu lama untuk pemantauan ditandai **perlu dimutakhirkan** di kolom
**Versi agent** daftar **Server**, dan statusnya **Agent perlu
dimutakhirkan**. Untuk memutakhirkannya:

1. Buka halaman server itu, klik **Pasang ulang agent**.
2. Jalankan perintah pemasangan yang ditampilkan di server database itu
   (perintah yang sama juga tampil di daftar server selama tokennya
   berlaku).
3. Agent baru terhubung dan pemantauan mulai dalam beberapa detik.

Pemasangan ulang hanya mengganti binary, sertifikat, dan `agent.yaml`.
Akun MySQL tersegel, kebijakan lokal, serta peran dan sumber yang dipin di
`/etc/dbpanel` tetap. Agent lama yang belum punya berkas peran hanya
menulis berkas peran (dengan petunjuk peran dari panel); pin sumber replika
baru ditulis pada konfigurasi replikasi, dump, atau CLONE berikutnya (lihat
"Peran dan sumber yang dipin").

## Keamanan

- Panel hanya menjawab permintaan yang header `Host`-nya alamat panel:
  `--public-host`, `localhost`, alamat loopback, atau IP salah satu kartu
  jaringan host panel. Nama lain (mis. alias DNS buatan sendiri) mendapat
  HTTP 421; pakai nama itu sebagai `--public-host` bila memang diperlukan.
- `--public-host` hanya menerima alamat IPv4/IPv6 atau nama host (huruf,
  angka, titik, strip), karena nilainya masuk ke skrip pemasang agent yang
  berjalan sebagai root.
- Token pemasangan disimpan sebagai hash di `panel.db`. Perintah pemasangan
  hanya bisa ditampilkan ulang selama panel belum dimulai ulang; sesudahnya
  buat token baru. Skrip pemasang memberikan token ke `dbpanel agent pair`
  lewat variabel lingkungan `DBPANEL_TOKEN` (bukan argumen, yang terlihat di
  `ps`). Pairing manual: `sudo DBPANEL_TOKEN=... dbpanel agent pair --panel
  http://<ip-panel>:8080 --ca-sha256 <sidik jari CA>`, atau `--token-stdin`.
  Sidik jari CA ada di skrip pemasang (`CA_SHA256=...`), atau hitung di host
  panel dengan `openssl x509 -in /etc/dbpanel/ca.crt -outform DER | sha256sum`.
  `--ca-sha256` wajib; `--insecure-skip-ca-pin` mematikan pemeriksaan itu
  (tidak aman, hanya untuk jaringan yang sepenuhnya tepercaya).
- Service `dbpanel-panel` dan `dbpanel-agent` berjalan sebagai root
  (panel menjalankan agent tertanam untuk host-nya sendiri) dengan
  hardening systemd: `NoNewPrivileges`, `PrivateTmp`, `ProtectHome`,
  `ProtectKernel*`, dan `ProtectSystem=strict` untuk panel (hanya
  `/var/lib/dbpanel` dan `/etc/dbpanel` yang bisa ditulis). Unit agent
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
- Jangan tempatkan master produksi di host panel. Agent tertanam di panel
  memegang pinnya di direktori konfigurasi panel, jadi pin itu bukan batas
  terhadap proses panel sendiri: panel yang disusupi bisa mengubahnya.
  Pin master hanya melindungi master yang agentnya berjalan di server
  lain.
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

`DBPANEL_BIN_DIR=dist` menyalin kedua binary ke `/var/lib/dbpanel/bin/`
agar panel dapat melayani agent berarsitektur lain tanpa internet.

## Menjalankan test

```sh
make test       # go vet, semua test (batas waktu 30 menit), dan pemeriksaan skrip instalasi
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

- `TestE2ECreateReplica`: hubungkan MySQL, siapkan master, buat replika
  untuk satu database, tambah database kedua, dan buat replika kedua
  dengan CLONE;
- `TestE2EMonitoring`: `STOP REPLICA` langsung di MySQL replika harus
  tampil sebagai Berhenti (di luar panel), membuka peringatan, dan
  mengirim satu pesan ke Telegram palsu (server `httptest`, tidak ada
  panggilan ke internet) dalam 30 detik; lalu Start, Stop, kunci yang
  terlepas dan Kunci kembali, dan Resync ulang;
- `TestE2EPinnedRole`: agent master menolak `mysql.dropDatabases` karena
  perannya dipin, dan panel menolak menghapus master yang masih punya
  replika.

Test ini dilewati dengan `-short`.

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
