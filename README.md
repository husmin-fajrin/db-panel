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
   `curl -fsSL -K - <<<'url = "http://192.168.1.10:8080/agent/install?token=..."' | sudo bash`.
   Perintah ini butuh shell bash (atau zsh): URL bertoken diberikan ke
   `curl` lewat here-string (`<<<`), bukan sebagai argumen, sehingga token
   tidak terlihat di `ps`. Perintah tetap tercatat di riwayat shell; awali
   dengan spasi bila `HISTCONTROL` berisi `ignorespace`/`ignoreboth`
   (bawaan Ubuntu), atau jalankan `export HISTCONTROL=ignorespace` dulu.
3. Jalankan perintah itu di server database. Binary agent diunduh dari
   panel (tidak perlu internet), service `dbpanel-agent` dipasang, dan
   agent dipasangkan dengan token sekali pakai.
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
  http://<ip-panel>:8080`, atau `--token-stdin`.
- Service `dbpanel-panel` dan `dbpanel-agent` berjalan sebagai root
  (panel menjalankan agent tertanam untuk host-nya sendiri) dengan
  hardening systemd: `NoNewPrivileges`, `PrivateTmp`, `ProtectHome`,
  `ProtectKernel*`, dan `ProtectSystem=strict` untuk panel (hanya
  `/var/lib/dbpanel` dan `/etc/dbpanel` yang bisa ditulis) serta
  `ProtectSystem=full` untuk agent.
- Sertifikat agent dan sertifikat listener panel berlaku 2 tahun, CA 10
  tahun. Belum ada perpanjangan otomatis (dijadwalkan di Rencana 2);
  sampai saat itu, pasang ulang agent sebelum sertifikatnya habis.
  Listener panel membuat sertifikat baru setiap kali panel dimulai.

### Keamanan rilis

`SHA256SUMS` diunduh dari rilis yang sama dengan binary-nya, jadi
pemeriksaannya hanya menangkap unduhan rusak atau tertukar, bukan rilis
yang dipalsukan (mis. akun GitHub yang dibobol). Rilis belum
ditandatangani; tanda tangan `SHA256SUMS` (minisign/cosign) dengan kunci
publik yang disematkan di `install.sh` ditunda ke Rencana 2, sebelum ada
fitur pembaruan agent otomatis. Sampai saat itu, pasang dari tag rilis
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
