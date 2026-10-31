# Desain Rencana 3: Pemantauan, Notifikasi Telegram, Kontrol Dasar

Tanggal: 2026-10-31
Status: menunggu persetujuan pemilik produk
Induk: `docs/superpowers/specs/2026-10-30-db-replica-panel-design.md`
(spec utama). Dokumen ini merinci dan, bila berbeda, menggantikan bagian
7.5, 7.7, 7.8, dan 8 spec utama untuk Rencana 3. Promote (7.7) dan
verifikasi checksum (7.6) ditunda ke Rencana 4.

## 1. Maksud dan alasan

Setelah Rencana 2, panel bisa membuat replika, tetapi tidak tahu apakah
replika itu masih berjalan sejam kemudian. Pengguna harus membuka
terminal untuk tahu. Rencana 3 membuat panel menjawab tiga pertanyaan
"pawang server" tanpa terminal:

1. Pagi ini, semua baik-baik saja? (dashboard tiga detik)
2. Semalam terjadi apa? (garis waktu kejadian, Telegram)
3. Kalau ada yang salah, saya harus menekan apa? (saran awam dan tombol
   Start / Resync ulang / Kunci kembali / Lepas)

Prinsip yang disepakati pemilik produk: setiap keputusan diukur dari
"apakah ini memudahkan user dan memenuhi tujuan utama panel". Pengguna
yang mahir MySQL pun terbantu oleh pemantauan, notifikasi, dan resync
sekali klik; pengguna yang belum mahir terbantu oleh penjelasan awam di
tiap langkah.

## 2. Tujuan, bukan tujuan, anti-tujuan

**Tujuan**

1. Status tiap replika dan master selalu terlihat di panel, diperbarui
   tanpa muat ulang halaman, dengan "terakhir dicek" yang jujur.
2. Masalah diketahui lewat Telegram paling lama **1 menit** setelah
   terjadi (replika berhenti, kunci terlepas, master tidak terjangkau).
3. Beban pemantauan pada master, replika, jaringan, dan panel mendekati
   nol: tidak ada tulisan ke master, tidak ada database heartbeat, tidak
   ada polling dari panel.
4. Garis waktu kejadian per server dan catatan audit siapa menekan apa.
5. Kontrol dasar dengan konfirmasi dan penjelasan awam: Stop/Start,
   Kunci kembali, Buka kunci master, Resync ulang, Lepas replika, Hapus
   server.
6. Prasyarat keamanan yang tidak terasa oleh user: agent master menolak
   perintah yang merusak datanya, agent replika hanya menerima sumber
   yang tercatat, catatan audit tahan ubah.
7. Hutang Rencana 2 yang murah dibayar.

**Bukan tujuan (Rencana 3)**

- Promote terencana/darurat, karantina master lama (Rencana 4).
- Verifikasi isi data lewat checksum (Rencana 4).
- Panel berjalan non-root dan verifikasi sertifikat MySQL oleh CA panel
  (SEC-06, SEC-29 butir 2, SEC-41): Rencana 4 bersama promote. Alasan:
  tidak dirasakan user, memaksa pemasangan ulang, baru genting saat
  promote.
- Grafik lag yang halus; heartbeat ketelitian sub-detik (opsional di
  rencana berikutnya bila dibutuhkan).
- Kanal notifikasi selain Telegram; lebih dari satu chat tujuan.
- Perbaikan otomatis apa pun.

**Anti-tujuan**

- Pemantauan yang membebani server produksi, walau sedikit.
- Panel bertindak sendiri terhadap MySQL tanpa klik manusia. Satu-satunya
  perintah yang dikirim tanpa klik adalah perintah baca status.
- Banjir pesan Telegram: pesan berulang tiap pemeriksaan, atau pesan saat
  perawatan yang disengaja.
- Isi data pasien atau baris tabel ikut keluar lewat Telegram.

## 3. Batasan

- Semua batasan spec utama bagian 4 tetap berlaku.
- Agent memeriksa MySQL-nya sendiri lewat socket/127.0.0.1 dengan akun
  admin yang sudah tersimpan (Rencana 2). Server yang belum "Hubungkan
  MySQL" tidak dipantau, dan panel mengatakannya.
- Pemeriksaan hanya memakai perintah baca memori MySQL: `SHOW GLOBAL
  STATUS/VARIABLES`, `SHOW REPLICA STATUS`, `SHOW BINARY LOGS`,
  `performance_schema.threads`. Tidak ada `SELECT` ke tabel pengguna,
  tidak ada `COUNT(*)`, tidak ada tulisan.
- Panel mungkin tanpa internet (spec 9.15); Telegram boleh gagal dan
  harus terlihat gagal di panel, bukan diam.
- Agent lama (Rencana 2) yang belum dimutakhirkan tidak melapor; panel
  menampilkan "Agent perlu dimutakhirkan" untuk versi di bawah Rencana 3,
  memakai versi agent yang sudah dicatat di `servers`.
- Jam server bisa berbeda; semua waktu di panel memakai jam panel saat
  laporan diterima, dan laporan susulan (saat panel sempat mati) membawa
  selisih waktu relatif agent, bukan jam absolut agent.

## 4. Pendekatan yang dipertimbangkan

**A. Agent memeriksa sendiri, lapor hanya bila berubah** (dipilih).
Agent memeriksa tiap 15 detik; mengirim laporan saat ada perubahan yang
berarti, dan ringkasan tiap 2 menit. Panel menerjemahkan laporan menjadi
status awam, kejadian, peringatan, dan Telegram.
Kelebihan: beban hampir nol di semua sisi; agent tetap memeriksa saat
panel mati dan melapor susulan; mudah diatur per server.
Kekurangan: logika baru di agent; tidak ada grafik lag halus.

**B. Panel memanggil `mysql.status` ke semua agent tiap 5 detik** (spec
lama 7.5). Tanpa perubahan agent.
Kekurangan: lalu lintas terus-menerus ke tiap server, skala buruk, tidak
ada pemeriksaan saat panel mati. Pemilik produk menolak beban ini.

**C. Agent lapor tiap pemeriksaan, berubah atau tidak.** Data lengkap
untuk grafik.
Kekurangan: database panel membesar dan lalu lintas naik untuk grafik
yang belum dibutuhkan.

Pemilik produk memilih **A**. Untuk Telegram, audit, dan kontrol hanya ada
satu cara yang masuk akal: di dalam proses panel, disimpan di SQLite
panel, dan Resync ulang memakai orkestrator job Rencana 2.

## 5. Rancangan

### 5.1 Pemantau di agent

Paket baru di agent, `internal/agent/monitor`, berjalan sebagai goroutine
sejak agent start, untuk kedua peran. Ia aktif hanya bila driver MySQL
punya `Host` dan kredensial admin tersimpan; bila belum, ia mengirim satu
laporan `mysql_ok=false, reason="belum terhubung"` dan mencoba lagi tiap
interval.

**Satu pemeriksaan** menghasilkan `proto.HealthSnapshot`:

| Bidang | Sumber | Catatan |
|---|---|---|
| `mysql_ok`, `mysql_error` | koneksi admin lokal, `SELECT 1` | gagal = Tidak terjangkau |
| `uptime_sec`, `threads_connected`, `max_connections`, `threads_running` | `SHOW GLOBAL STATUS/VARIABLES` | restart terdeteksi bila uptime mengecil |
| `read_only`, `super_read_only`, `gtid_executed`, `server_uuid` | variabel | kunci replika; terkunci di master |
| `binlog_dump_threads` | `performance_schema.threads` (sudah ada) | replika tersambung ke server ini |
| `binlog_total_bytes`, `binlog_oldest_age_sec`, `binlog_expire_sec` | `SHOW BINARY LOGS`, mtime berkas binlog tertua di `log_bin_basename`, `binlog_expire_logs_seconds` | hanya bila `log_bin=ON`; umur tertua untuk tampilan saja (pada master yang sudah lama hidup nilainya selalu mendekati batas simpan), bukan untuk peringatan |
| `replica` | `SHOW REPLICA STATUS` (struktur `proto.ReplicaStatus` yang ada) | nil bila bukan replika |
| `disk_total`, `disk_free` (datadir) | `Statfs(datadir)` | |
| `mem_total`, `mem_available`, `load1` | `/proc/meminfo`, `/proc/loadavg` | |
| `agent_version` | build | untuk "perlu dimutakhirkan" |
| `taken_ago_ms` | jam monoton agent | umur laporan saat dikirim (laporan susulan) |

Semua perintah dibatasi timeout 5 detik dan satu koneksi yang dipakai
ulang; bila MySQL tidak menjawab, koneksi dibuang dan dibuka lagi pada
pemeriksaan berikutnya.

**Kapan melapor.** Agent membandingkan dengan laporan terakhir yang
terkirim. Ia langsung melapor bila salah satu berubah:

- `mysql_ok`; `super_read_only`; `read_only`;
- `replica.io_running`, `replica.sql_running`, `last_io_errno`,
  `last_sql_errno`, `source_host`, `source_port`;
- lag menyeberangi ambang (`seconds_behind` < ambang ke >= ambang atau
  sebaliknya; nil dihitung sebagai "tidak diketahui", juga satu keadaan);
- `binlog_dump_threads` berubah;
- pemakaian disk datadir menyeberangi 85% atau 95%;
- `threads_connected` menyeberangi 80% dari `max_connections`;
- uptime mengecil (restart).

Di luar itu ia mengirim ringkasan tiap `summary_every`. Nilai bawaan:
`check_every=15s`, `summary_every=120s`, `lag_threshold=60s`. Rantai
waktu terburuk sampai Telegram untuk replika berhenti: 15 dtk periksa +
kirim segera, di bawah 30 dtk. Untuk master tumbang: hub menyadari
koneksi agent mati paling lama 20 dtk (ping 10 + timeout 10), ditambah
`down_after_sec=30`, ditambah kirim segera: sekitar 50 dtk. Keduanya di
bawah batas 1 menit yang ditetapkan pemilik produk, dengan beban satu
perintah baca memori tiap 15 detik. Panel
mengirim nilai aktual lewat perintah `monitor.configure` saat agent
tersambung dan saat pengaturan berubah; agent menyimpannya di memori
saja.

**Saat panel tidak tersambung.** Agent tetap memeriksa. Laporan yang
termasuk "perubahan" disimpan di ring buffer memori maksimal 200 entri
(yang terlama dibuang). Saat tersambung lagi, agent mengirim seluruh
buffer berurutan, lalu laporan terkini. Tiap entri membawa
`taken_ago_ms` sehingga panel bisa menempatkannya di garis waktu
(`waktu panel sekarang - taken_ago`). Ringkasan berkala tidak disimpan.

**Protokol.** Event `monitor.snapshot` (payload `HealthSnapshot`,
`sequence` naik). Perintah `monitor.configure{check_every_sec,
summary_every_sec, lag_threshold_sec}` mengembalikan laporan terkini
sekaligus, sehingga panel langsung punya data saat agent tersambung.
Perintah `monitor.snapshot` (tanpa parameter) mengembalikan laporan
terkini untuk tombol "Periksa sekarang" di UI. Agent tertanam di panel
memakai jalur yang sama (Hub.OnEvent).

### 5.2 Penerjemah status di panel

Paket baru `internal/panel/monitor`. Ia menerima `monitor.snapshot` dari
`Panel.onEvent`, menyimpan laporan terakhir per server di tabel
`server_health` (satu baris per server, ditimpa), menurunkan status, dan
menulis kejadian bila status berubah.

**Status replika** (yang pertama cocok menang):

| Status | Syarat | Warna |
|---|---|---|
| Belum siap | `replicas.state` bukan `ready`/`attention` (sedang dibuat, disinkron ulang, gagal) | abu |
| Agent offline | hub melaporkan offline > 30 dtk, atau laporan terakhir lebih tua dari 3 × `summary_every` | merah |
| Agent perlu dimutakhirkan | versi agent < Rencana 3 | abu |
| Tidak terjangkau | `mysql_ok=false` | merah |
| Berhenti | `sql_running=No`, atau `io_running=No` dengan `last_io_errno != 0` | merah |
| Dihentikan | `sql_running=No` atau `io_running=No` tanpa error, dan `replicas.paused=1` (dihentikan lewat panel) | kuning |
| Berhenti (di luar panel) | sama tanpa `paused` | merah |
| Terputus | `io_running` = `Connecting` atau `No` tanpa error | kuning; merah bila > 3 menit |
| Ketinggalan | `seconds_behind` >= ambang, atau nil saat kedua thread Yes | kuning |
| Sinkron | sisanya | hijau |

Penanda terpisah dari status: **Kunci terlepas** (`super_read_only=OFF`
pada replika) selalu merah dan ditampilkan di sebelah status apa pun.

**Status master:**

| Status | Syarat |
|---|---|
| Agent offline | seperti replika; setelah `down_after_sec` (bawaan 30 dtk) menjadi "Diduga tumbang (agent offline)" |
| Tidak terjangkau | `mysql_ok=false`; setelah `down_after_sec` "Diduga tumbang (MySQL tidak menjawab)" |
| Terkunci | `super_read_only=ON` di master (aplikasi tidak bisa menulis) |
| Sehat | sisanya |

"Diduga tumbang" diperkuat bila replika-replika topologi itu melaporkan
`io_running != Yes` pada waktu yang sama; kalimatnya menyebut keduanya:
"agent tidak terhubung 3 menit dan 2 replika kehilangan koneksi".

**Penjelasan awam** untuk replika Berhenti, dari nomor error
(`last_sql_errno` lalu `last_io_errno`):

| Errno | Kalimat | Saran utama |
|---|---|---|
| 1062 | Data ganda: ada baris yang sudah ada di replika. Biasanya karena ada yang menulis langsung ke replika. | Resync ulang |
| 1032 | Baris tidak ditemukan: isi replika sudah berbeda dari master. | Resync ulang |
| 1236 | Binlog master yang dibutuhkan sudah tidak ada: replika terlalu lama putus. | Resync ulang |
| 1594 | Relay log di replika rusak. | Start replika; bila gagal lagi, Resync ulang |
| 1021, 1114, 3 (disk penuh / tabel penuh) | Disk replika penuh. | Bereskan disk, lalu Start replika |
| 1045, 1593 (IO) | Akun replikasi ditolak master, atau server_id/UUID bentrok. | Periksa akun replikasi di halaman master |
| 2003, 2013 (IO) | Master tidak bisa dihubungi dari replika. | Periksa jaringan/master |
| lain | Penyebab belum dikenali panel. | tidak ada; tombol tetap tersedia |

Daftar ini satu tabel di kode (`internal/panel/monitor/explain.go`)
supaya mudah ditambah. Pesan MySQL mentah hanya di "Detail teknis".

**Kejadian** (`status_events`): satu baris tiap status replika/master
berubah, kunci terlepas/kembali, restart MySQL terdeteksi, disk atau
koneksi menyeberangi ambang, agent online/offline, dan tiap tindakan dari
catatan audit yang menyangkut server itu. Disimpan 90 hari.

**SSE.** Setiap perubahan `server_health` atau status menerbitkan
`health_update{server_id}`; halaman memuat ulang fragmennya (pola
Rencana 2). Tidak ada polling dari browser.

### 5.3 Peringatan dan Telegram

Tabel `alerts`: `id, kind, server_id, topology_id, opened_at, closed_at,
last_notified_at, notify_count, message, telegram_state
(pending|sent|failed), telegram_error`. Satu peringatan terbuka per
`(kind, server_id)`.

**Jenis** (dengan syarat buka dan tutup):

| kind | Buka | Tutup |
|---|---|---|
| replica_stopped | status Berhenti (bukan Dihentikan) | status lain |
| replica_disconnected | Terputus > 3 menit | io Yes |
| replica_lagging | Ketinggalan > 5 menit terus-menerus | lag < ambang selama 1 menit |
| replica_unlocked | Kunci terlepas | terkunci lagi |
| master_locked | Terkunci | terbuka |
| master_down | Diduga tumbang | Sehat |
| mysql_unreachable | Tidak terjangkau > 30 dtk (replika) | ok |
| agent_offline | Agent offline > 30 dtk | online |
| disk_warn / disk_crit | 85% / 95% | turun di bawah 80% / 90% |
| connections_high | > 80% max_connections selama 5 menit | < 70% |
| binlog_deadline | replika Terputus/Berhenti/Dihentikan terus-menerus (dari `status_since`) lebih lama dari 80% `binlog_expire_sec` master; tanpa `binlog_expire_sec` (0 = tanpa batas) tidak pernah | replika sinkron lagi atau disync ulang |
| job_failed | job gagal (semua jenis) | job diulang dan berhasil, atau dibatalkan |

Semua ambang dan jeda di atas adalah nilai bawaan yang bisa diubah di
Pengaturan, kecuali syarat tutup yang mengikuti ambangnya.

**Pengiriman.** Goroutine `notifier` di panel: tiap peringatan baru
dikirim sekali; selama masih terbuka diulang tiap 6 jam; saat tutup
dikirim pesan "Pulih" dengan lama gangguan. Kegagalan kirim dicoba lagi
tiap 60 detik (backoff tetap), `telegram_state=failed` dan alasannya
tampil di halaman Peringatan. Bot API dipanggil lewat HTTPS ke
`api.telegram.org` dengan timeout 10 detik, menghormati `HTTPS_PROXY`.

**Bentuk pesan** (teks polos, tanpa Markdown agar tak perlu escaping):

```
⛔ Replika RS-2 berhenti
Data ganda (error 1062): ada baris yang sudah ada di replika.
Saran: Resync ulang.
Master: RS-1 · Sejak: 02:14 · http://panel.local/replicas/3
```

Pesan hanya memuat nama server, status, nomor error, kalimat awam,
waktu, dan tautan panel (alamat dasar dari Pengaturan). Pesan error
mentah MySQL tidak pernah dikirim (bisa memuat isi baris).

**Jangan ganggu.** `servers.snooze_until`; tombol di halaman server
dengan pilihan 1, 2, 8, 24 jam, dan "Batalkan". Selama aktif, peringatan
server itu tetap dibuka dan tampil di panel, tetapi Telegram tidak
dikirim; saat berakhir, peringatan yang masih terbuka dikirim satu kali.
Dicatat di audit.

**Ringkasan mingguan.** Bila diaktifkan (bawaan aktif, Senin 07:00 waktu
panel), satu pesan: untuk tiap replika persentase waktu dalam status
Sinkron/Ketinggalan selama 7 hari terakhir (dari `status_events`), jumlah
dan total durasi gangguan, pemakaian disk tiap server, jumlah job gagal.
Dihitung dari data yang sudah ada; tidak ada pemeriksaan tambahan.

**Pengaturan Telegram:** bot token (disimpan terenkripsi dengan
`panel.key`, tidak pernah ditampilkan lagi setelah disimpan, tidak pernah
di log), chat id, tombol "Kirim tes" (dibatasi 1 per 10 detik), sakelar
per `kind`, alamat dasar panel untuk tautan.

### 5.4 Kontrol

Semua tombol: modal daisyUI dengan satu kalimat "apa yang terjadi" dan
satu kalimat risiko, lalu dicatat di audit. Tombol tersedia hanya bila
tidak ada job aktif untuk server itu (`refuseBusy` yang ada).

| Tombol | Di mana | Yang dilakukan | Konfirmasi |
|---|---|---|---|
| Stop replika | halaman replika | `mysql.stopReplica`; `replicas.paused=1` | modal: "lag akan membesar; master menyimpan binlog N hari (dari `binlog_expire_sec`); lebih dari itu replika harus disync ulang" |
| Start replika | halaman replika | `mysql.startReplica`; `paused=0`; menunggu 10 dtk lalu tampilkan status baru | modal ringan |
| Kunci kembali | replika, saat Kunci terlepas | `mysql.setReadOnly{on:true}` | modal: "bila ada yang sempat menulis ke replika, isi bisa berbeda; pertimbangkan Resync ulang" |
| Buka kunci master | master, saat Terkunci | `mysql.setReadOnly{on:false}` | modal: ketik nama server |
| Periksa sekarang | master dan replika | `monitor.snapshot` | tidak ada |
| Resync ulang | replika | job `resync_replica` | ketik nama server |
| Lepas replika | replika | lihat di bawah | ketik nama server |
| Hapus server | halaman server | lihat di bawah | ketik nama server |
| Jangan ganggu | server | `snooze_until` | tidak ada |

**Resync ulang** = job `resync_replica` dengan tahap "Periksa master",
"Periksa server tujuan", "Siapkan replika", "Salin data", "Sambungkan
replikasi", "Verifikasi" dari `create_replica` (tanpa "Pasang MySQL"),
database yang sama seperti tercatat. Perbedaan dari `create_replica`,
karena targetnya replika terdaftar dari master yang sama:

- **Satu konfirmasi, bukan dua.** Modal menampilkan daftar persis
  database yang akan dibuang (dari inventaris replika yang disegarkan
  saat modal dibuka) dan meminta nama server diketik. Daftar itu
  disimpan di parameter job. Tahap "Periksa server tujuan" membandingkan
  database yang ditemukannya dengan daftar itu: sama → lanjut tanpa
  berhenti; ada yang tidak ada di daftar → job berhenti menunggu
  persetujuan di dalam job seperti Rencana 2 (perlindungan 34e27c1
  tetap untuk kasus yang tidak terduga).
- **CLONE boleh walau tujuan tidak kosong**, bila semua database dipilih
  dan hanya database yang ada di daftar konfirmasi yang ditemukan;
  kalau tidak, dump. User tetap bisa memaksa dump.
- **Pemeriksaan disk** menghitung ruang bebas ditambah ukuran data
  replika saat ini (yang akan dibuang), bukan hanya ruang bebas.

Selama berjalan `replicas.state=resyncing`; sukses → `ready`, gagal →
`failed` dengan Ulangi/Batalkan seperti job lain. Modal menyebut ukuran
data master dan saran menjalankannya di luar jam sibuk. Sebelum tahap
"Siapkan replika", job menyapu akun `dbpanel_dump_%` sisa di master yang
`password_last_changed` lebih dari 24 jam (5.6).

**Lepas replika**: dua pilihan dalam satu modal.
- "Lepas, biarkan replikasi berjalan": panel menghapus baris `replicas`,
  peran server → belum dipakai, akun replikasi di master dibiarkan.
  Kalimat: "MySQL di server ini terus mengikuti master sampai Anda
  menghentikannya sendiri; panel tidak lagi memantaunya."
- "Lepas dan hentikan replikasi": `mysql.stopReplica`, `mysql.resetReplica`,
  lalu `mysql.releaseRole` (5.5); kunci `super_read_only` **dibiarkan
  ON** dengan kalimat cara membukanya di terminal, karena agent berperan
  replika menolak membuka kunci dan setelah dilepas panel tidak lagi
  mengelola server itu.
Akun replikasi di master **tidak disentuh**: hari ini satu topologi
memakai satu akun `dbpanel_repl@'%'` bersama untuk semua replikanya
(`topologies.repl_user`); menghapusnya memutus replika lain. Akun itu
milik "Siapkan master" dan ikut hilang hanya saat topologi dihapus
(master dilepas setelah semua replikanya pergi). Akun per replika
ditunda ke Rencana 4, saat promote memang membangun ulang akun.

**Hapus server**: ditolak dengan pesan bila server adalah master sebuah
topologi ("lepas atau hapus semua replikanya dulu") atau replika yang
belum dilepas. Hari ini `DeleteServer` menghapus topologi lewat
`ON DELETE CASCADE` tanpa peringatan; itu ditutup. Setelah dihapus, modal
menampilkan perintah untuk mencabut agent di host.

### 5.5 Keamanan

**Peran yang dipin di agent** (SEC-28 butir 1). Berkas
`/etc/dbpanel/role` berisi `master`, `replica`, atau `unassigned`;
ditulis agent (0644, milik root) saat `mysql.configure{role}` pertama
kali berhasil. Perintah panel baru `mysql.releaseRole` mengembalikan
`replica` → `unassigned` (dan menghapus pin sumber), dan agent
menerimanya hanya bila tidak ada konfigurasi replikasi tersisa (setelah
`mysql.resetReplica`) dan `binlog_dump_threads=0`. Panel memanggilnya
pada "Lepas dan hentikan replikasi", saat job `create_replica` dibatalkan
atau gagal lalu replikanya dihapus, dan pada "Hapus server"; dengan itu
server bisa dipakai lagi dari panel tanpa terminal. `master` tidak
pernah bisa dilepas atau diubah oleh perintah panel, juga tidak lewat
`releaseRole`; itu hanya lewat CLI lokal `dbpanel agent set-role <peran>`
(root di host), karena master produksi adalah aset yang dilindungi pin
ini (Rencana 4 merancang alur promote-nya). `mysql.resetReplica` sendiri
tidak mengubah pin: ia juga dipakai di tengah "Siapkan replika". Aturan
registry:

| Peran | Ditolak |
|---|---|
| master | `mysql.dropDatabases`, `mysql.dumpFrom`, `mysql.cloneFrom`, `mysql.configureReplication`, `mysql.setReplicationFilter`, `mysql.resetReplica`, `mysql.startReplica`, `mysql.configure{role:replica}`, `mysql.setReadOnly{on:true}` |
| replica | `mysql.setReadOnly{on:false}`, `mysql.configure{role:master}`, `mysql.createReplUser` |
| unassigned | tidak ada tambahan |

`refuseOnSource` tetap sebagai lapis kedua untuk server `unassigned`.
Untuk `mysql.setReadOnly{on:false}` pada peran `master`, `refuseOnSource`
tidak lagi berlaku (hari ini ia memblokir "Buka kunci master" karena
master selalu punya binlog dump thread). Pesan penolakan menyebut
perannya dan perintah CLI untuk mengubahnya. Agent yang dimutakhirkan di
server yang sudah dikonfigurasi menulis berkas peran dari keadaan MySQL
saat start (replika bila `SHOW REPLICA STATUS` ada baris; master bila
`log_bin=ON` dan `binlog_dump_threads>0` atau tercatat `master_ready`
lewat perintah `monitor.configure{role_hint}` dari panel; selain itu
`unassigned`).

**Sumber yang dipin** (SEC-28 butir 3). Berkas `/etc/dbpanel/source`
berisi `host:port` master; ditulis saat `mysql.configureReplication`
pertama berhasil, dihapus oleh `mysql.releaseRole`. Sesudah itu `mysql.dumpFrom`, `mysql.cloneFrom`, dan
`mysql.configureReplication` menolak host:port lain dengan pesan yang
menyebut `dbpanel agent set-source <host:port>`. Halaman master yang
mengubah alamat sumber (fitur yang ada) menampilkan perintah itu untuk
tiap replika. Pin sumber tidak pernah diubah oleh perintah panel selain dihapus lewat `releaseRole`.

**Catatan audit** (SEC-17). Tabel `audit_log(id, ts, username, ip,
action, target_kind, target_id, detail_json, outcome)`, ditulis oleh
setiap handler POST yang mengubah keadaan, termasuk login berhasil/gagal,
logout, ganti pengaturan, tes Telegram, semua tombol 5.4, Siapkan master,
Buat replika, tambah/lepas database, approve/retry/cancel job. Tahan
ubah: trigger SQLite `BEFORE UPDATE` dan `BEFORE DELETE` yang
`RAISE(ABORT)`; tidak ada kode yang menghapusnya; tidak dipangkas.
Halaman `/audit` dengan filter server dan rentang waktu, hanya admin
(satu-satunya peran). `detail_json` tidak pernah memuat sandi atau token.

**Lain-lain.** Token Telegram terenkripsi (AES-256-GCM, `panel.key`),
tidak pernah ke log atau SSE. `monitor.configure` divalidasi (10–3600
dtk). Payload laporan dibatasi 64 KiB; panel menolak lebih besar.
Rate limit 1/10 dtk untuk "Kirim tes" dan "Periksa sekarang" per server.
Semua halaman baru di balik login dan CSRF yang ada.

### 5.6 Hutang Rencana 2 yang dibayar

1. `Pdeathsig=SIGTERM` pada semua proses anak agent (`realhost.go`,
   `install.go`) sehingga pipeline dump/klien mati bersama agent
   (residual 4).
2. Penyapuan akun `dbpanel_dump_%` sisa di master, lebih tua dari 24 jam,
   sebelum tahap "Siapkan replika" pada `create_replica`, `resync_replica`,
   dan `add_databases` (celah "DROP mendahului CREATE"). Akun
   `dbpanel_repl` tidak pernah disapu.
3. Batas 1 KiB untuk semua jalur `job.log` (residual 5).
4. `Cancel` di antara reopen dan launch menghasilkan `canceled`, bukan
   `failed` (M2).
5. `masterVerify` memakai alamat sumber yang tersimpan, bukan yang
   diedit sebelum disimpan (residual 6); kalimat log master offline (M3).
6. `/jobs/{id}/log?after=` untuk melanjutkan log setelah SSE putus.

Ditunda ke rencana berikutnya: alamat dump di balik NAT (agent replika
melaporkan IP keluar), penandatanganan binary, perpanjangan sertifikat
otomatis, token konfirmasi per perintah (Rencana 4 bersama promote).

### 5.7 Model data (migrasi 0004)

- `server_health(server_id PK, received_at, snapshot_json, status,
  status_since, lock_ok, agent_version)`.
- `status_events(id, server_id, topology_id, ts, kind, from_status,
  to_status, detail)`; indeks `(server_id, ts)`; pangkas 90 hari.
- `alerts` (5.3); indeks `(server_id, kind, closed_at)`.
- `audit_log` (5.5) dengan trigger tahan ubah.
- `replicas.paused INTEGER DEFAULT 0`; nilai `state` baru `resyncing`.
- `servers.snooze_until TEXT`.
- `settings` kunci baru: `telegram_token_enc`, `telegram_chat_id`,
  `telegram_kinds_json`, `panel_base_url`, `monitor_check_every`,
  `monitor_summary_every`, `lag_threshold_sec`, `disconnect_alert_sec`,
  `down_after_sec`, `disk_warn_pct`, `disk_crit_pct`, `weekly_summary`
  (`on|off`), `weekly_summary_at` (`Mon 07:00`), `alert_repeat_hours`.

### 5.8 Antarmuka

- **Dashboard**: kartu angka (master, replika, peringatan aktif); satu
  kartu per topologi: master dengan status dan replikanya masing-masing
  satu baris (nama, status berwarna, penanda kunci, tertinggal N dtk,
  terakhir dicek N dtk lalu). Server yang belum terhubung MySQL tampil
  "Belum dipantau". Diperbarui lewat SSE.
- **Halaman master**: status besar, versi, uptime, replika tersambung
  vs terdaftar, umur binlog tertua dan batas simpannya, koneksi/max,
  disk/RAM/load, tombol Buka kunci (bila Terkunci), Periksa sekarang,
  Jangan ganggu, garis waktu 7 hari (bisa diperluas 90), daftar replika.
- **Halaman replika**: status besar + kalimat awam + saran, penanda
  kunci, tombol Start/Stop/Kunci kembali/Resync ulang/Lepas/Periksa
  sekarang/Jangan ganggu, database yang diikuti, lag, GTID diterima vs
  dieksekusi, "Detail teknis" (error mentah, errno, source host/port,
  filter), garis waktu, riwayat job.
- **Halaman Peringatan** (`/alerts`): aktif (dengan status Telegram dan
  alasannya bila gagal) dan riwayat 90 hari.
- **Halaman Pengaturan** (`/settings`): Telegram, ambang, interval,
  ringkasan mingguan, alamat dasar panel.
- **Halaman Audit** (`/audit`).
- **Daftar server**: kolom status pemantauan dan versi agent, tanda
  "perlu dimutakhirkan" dengan perintah pembaruan.
- Sampai promote ada (Rencana 4), halaman master Diduga tumbang
  menampilkan kotak "Yang bisa dilakukan sekarang" berisi langkah manual
  singkat untuk menjadikan replika paling mutakhir sebagai master, dengan
  peringatan bahwa panel belum bisa melakukannya.

### 5.9 Pengujian

- Unit: penurunan status dan mesin peringatan dengan jam palsu (buka,
  ulang 6 jam, tutup, snooze, backoff Telegram); tabel penjelasan errno;
  pembandingan "berubah" di agent; ring buffer dan laporan susulan.
- testmysql: `HealthSnapshot` dari mysqld nyata sebagai master dan
  sebagai replika (termasuk umur binlog dan dump threads).
- Telegram: server `httptest` palsu; tidak ada panggilan keluar di tes.
- E2E (`TestE2ECreateReplica` diperluas): setelah replika jadi, `STOP
  REPLICA` langsung di mysqld → status Berhenti (di luar panel) dan
  peringatan dalam 30 detik; Start dari panel → Sinkron dan "Pulih";
  Resync ulang menyelesaikan job; peran dipin menolak `dropDatabases`
  pada master.
- `-race` untuk paket monitor dan notifier.

## 6. Diserahkan ke builder

- Struktur paket dan nama tipe di dalam batasan di atas; pembagian tugas.
- Bentuk persis tabel/indeks dan kueri pangkas.
- Tata letak kartu dan warna di dalam daisyUI yang ada; ikon.
- Cara menghitung persentase mingguan dari `status_events`.
- Cara agent membaca umur binlog bila berkas tidak terbaca (laporkan nil,
  jangan gagal).
- Perintah CLI `dbpanel agent set-role` / `set-source` (bentuk output,
  validasi).

## 7. Keputusan pemilik produk (brainstorming 2026-10-31)

1. Cakupan Rencana 3 = pemantauan + Telegram + kontrol dasar + prasyarat
   keamanan yang tak terasa + hutang Rencana 2. Promote dan checksum di
   Rencana 4.
2. Pemantauan tidak boleh membebani server; tanpa database heartbeat.
3. Batas waktu tahu ada masalah: 1 menit.
4. Tidak ada perbaikan otomatis; semua tindakan menunggu klik user.
5. Saran awam ditampilkan di depan, error mentah di "Detail teknis".
6. Telegram: satu bot, satu grup tim IT.
7. Pendekatan A.
8. Enam fitur "pawang": dashboard tiga detik, garis waktu, Telegram
   dengan tindakan, Jangan ganggu, ringkasan mingguan, peringatan
   sebelum kejadian (batas binlog; tren disk bila mudah).
9. Prinsip: setiap keputusan diukur dari kemudahan user dan tujuan utama
   panel; pemilik produk menyerahkan keputusan teknis kepada builder
   dengan prinsip itu.

Keputusan setelah builder check (semua keputusan saya, dengan prinsip
butir 9):

1. Akun replikasi bersama per master tidak disentuh oleh Lepas replika;
   akun per replika ditunda ke Rencana 4 (5.4).
2. Peringatan batas binlog mengukur lama replika terputus/berhenti
   terhadap `binlog_expire_sec`, bukan umur berkas binlog tertua (5.3).
3. Resync ulang satu konfirmasi (ketik nama dengan daftar database yang
   dibuang); job berhenti hanya bila menemukan yang tidak ada di daftar;
   CLONE boleh untuk resync semua database; cek disk memperhitungkan data
   yang dibuang (5.4).
4. Batas 1 menit dipenuhi dengan periksa tiap 15 dtk dan ambang tumbang
   30 dtk; beban tetap satu perintah baca memori (5.1).
5. Panel boleh melepas pin `replica` → `unassigned` lewat `releaseRole`
   pada alur yang aman; pin `master` hanya lewat CLI lokal (5.5).

Keputusan lain yang saya ambil sendiri (dengan alasan di badannya): panel
non-root dan CA MySQL ditunda (bagian 2); ambang bawaan 60 dtk lag, 3
menit terputus, 30 dtk tumbang, 6 jam ulang (5.3); pin peran dan sumber
dengan CLI lokal untuk mengubahnya (5.5); dua pilihan Lepas replika dan
kunci dibiarkan (5.4); tren disk tidak masuk (hanya batas binlog).
