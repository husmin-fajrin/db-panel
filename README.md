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

```sh
curl -fsSL https://raw.githubusercontent.com/husmin-fajrin/db-panel/main/scripts/install.sh | sudo bash
```

Skrip mengunduh binary dari GitHub Releases ke `/usr/local/bin/dbpanel`,
membuat `/etc/dbpanel/` (0700) dan `/var/lib/dbpanel/`, memasang service
`dbpanel-panel`, lalu mencetak alamat panel, pengguna dan sandi awal.
Rilis tertentu: tambahkan `DBPANEL_VERSION=v0.1.0` sebelum `bash`
(`... | sudo DBPANEL_VERSION=v0.1.0 bash`).

**Tanpa internet (offline):** salin binary (dari halaman rilis, atau
`dist/dbpanel` hasil `make build`) dan `scripts/install.sh` ke server, lalu:

```sh
sudo DBPANEL_BIN=/path/ke/dbpanel bash install.sh
```

**Binary agent untuk arsitektur lain:** panel selalu bisa membagikan
binary-nya sendiri ke server dengan arsitektur yang sama. Agar server arm64
bisa dipasangkan ke panel amd64 (atau sebaliknya), berikan direktori berisi
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

Lupa sandi? Buat sandi baru (dicetak ke layar):

```sh
sudo dbpanel panel reset-password
```

## Tambah server database

1. Di panel buka **Server > Tambah server**, isi nama, simpan.
2. Salin perintah pemasangan yang ditampilkan, misalnya
   `curl -fsSL http://192.168.1.10:8080/agent/install?token=... | sudo bash`.
3. Jalankan perintah itu di server database. Binary agent diunduh dari
   panel (tidak perlu internet), service `dbpanel-agent` dipasang, dan
   agent dipasangkan dengan token sekali pakai.
4. Server tampil online di panel beserta info sistem dan MySQL.

Server database harus bisa menjangkau panel di port 8080 (unduhan dan
pairing) dan 8443 (koneksi agent). Bila pairing gagal (token kedaluwarsa
atau sudah dipakai), buat token baru dari halaman server lalu ulangi.

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

## Build dari sumber

Butuh Go 1.26. Binary statis (`CGO_ENABLED=0`):

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
Aset hasil build ikut di-embed ke binary dan dikomit, jadi build biasa tidak
butuh Node.js maupun internet. Setelah mengubah template atau CSS:

```sh
scripts/tailwind.sh   # kompilasi Tailwind CSS + daisyUI -> web/static/app.css
scripts/vendor.sh     # unduh ulang Alpine.js -> web/static/vendor/ (bila ganti versi)
make build
```
