# Pengukuran putaran kedua untuk rencana 5b-2a (X2, X4–X7)

Diminta oleh tinjauan rancangan (`2026-11-14-05b2a-design-review.md`,
temuan R-4, tabel X1–X7). X1 sudah diukur pada putaran pertama (rencana,
bagian "Bukti pengukuran", M1c). Berkas ini hanya mencatat; tidak ada kode
produk yang diubah.

## Lingkungan dan cara

Diukur 2026-11-16 pada **MySQL 8.0.46-0ubuntu0.24.04.4** (paket Ubuntu
24.04 `mysql-server-core-8.0`, `mysql-client-core-8.0`; `mysqldump Ver
8.0.46-0ubuntu0.24.04.4`), sama dengan putaran pertama.

**X3 (MySQL 8.4): 8.4 tidak terpasang di lingkungan ini; tidak diukur.**
Satu-satunya biner server di mesin ini adalah `/usr/sbin/mysqld` 8.0.46
(dicari dengan `find / -xdev -name 'mysqld*'`).

Instance coba disusun persis seperti `testmysql.Start` dan putaran pertama:
`mysqld --no-defaults --initialize-insecure --user=nobody`, direktori
`/var/tmp/dbpanel-m2-<n>` milik `nobody:nogroup`, `my.cnf` dengan
`bind-address=127.0.0.1`, port sendiri (34101, 34102), soket sendiri,
`mysqlx=OFF`, `log-bin=binlog`, `gtid-mode=ON`,
`enforce-gtid-consistency=ON`, `user=nobody`, dijalankan `mysqld
--defaults-file=<my.cnf> --daemonize` tanpa supervisor. Server **S**
(sumber/donor, port 34101) dan server **T** (tujuan, "master lama", port
34102). T diinisialisasi ulang untuk setiap percobaan yang mengganti skema
`mysql`. Setelah selesai kedua instance dihentikan (SIGTERM, ditunggu
sampai keluar) dan direktorinya dihapus; `pgrep -a mysqld` sesudahnya
kosong, tidak ada direktori `/var/tmp/dbpanel-*` dan tidak ada mount
tersisa. Bukan MySQL mesin ini, bukan `/etc`, bukan `/var/lib/dbpanel`.

Klien: `mysql --defaults-file=<berkas>` lewat TCP 127.0.0.1 (seperti
putaran pertama), dan untuk X4/X7 sebuah program Go kecil di luar
repositori yang memakai **`github.com/go-sql-driver/mysql` v1.9.3** (versi
di `go.mod` agent) lewat `database/sql` dengan `ExecContext(ctx, …)`, persis
cara `persistReadOnly` dan `liftReadOnly` mengirim pernyataan. Pembatalan
"sisi klien" di bawah ini adalah batas waktu `context` Go itu.

Tingkat akun mengikuti putaran pertama (semua `'…'@'%'`, `WITH GRANT
OPTION`):

| Akun | Hak |
|---|---|
| **A** | `staticGrants` + `dynamicGrants` + `FLUSH_TABLES` |
| **A-r** | A + `replicaDataGrants` (`DROP, CREATE, INSERT, UPDATE, DELETE, ALTER, INDEX, CREATE VIEW, CREATE ROUTINE, ALTER ROUTINE, REFERENCES, CREATE TEMPORARY TABLES`) |
| **A-d** | A-r + `SET_USER_ID, SYSTEM_USER` |

Dump yang dimuat (X2, X7) dibuat sekali dari S dengan akun berbentuk
`mysql.createDumpUser` (`SELECT, FLUSH_TABLES, LOCK TABLES, SHOW VIEW,
TRIGGER, EVENT`, `MAX_USER_CONNECTIONS 2`, `PASSWORD EXPIRE INTERVAL 1 DAY`)
dan argumen `dumpArgs`:
`mysqldump --defaults-file=<f> --single-transaction --set-gtid-purged=ON
--triggers --routines --events --skip-lock-tables --column-statistics=0
--no-tablespaces --all-databases` → exit 0, 1 292 396 byte. Isi S: `app`
(tabel `t`, view `vw`, procedure `p`, trigger `tr` dan event `ev`, semuanya
`DEFINER=root@localhost`) dan `` `with space` ``, seperti putaran pertama.
Baris penting dump: baris 20 `SET @@SESSION.SQL_LOG_BIN= 0;`, baris 26
`SET @@GLOBAL.GTID_PURGED=/*!80000 '+'*/ '2c6aa2c9-…:1-13';`, baris 32
``CREATE DATABASE /*!32312 IF NOT EXISTS*/ `mysql` …``, baris 1084 trigger
`tr` dengan `DEFINER=root@localhost`. Dimuat seperti `loadArgs` +
`flushTrailer`: `mysql --defaults-file=<f> --binary-mode
--init-command="SET sql_log_bin=0"` dengan masukan dump lalu
`\nFLUSH PRIVILEGES;\n`.

## X2. Muat penuh oleh akun tanpa `CONNECTION_ADMIN`

Urutan per percobaan, semuanya dengan akun yang diuji, seperti `dumpFrom`
(server T bukan replika, jadi tanpa `STOP REPLICA`): T mulai dengan
`read_only`/`super_read_only` = ON/ON (di-PERSIST), lalu `SET GLOBAL
super_read_only = OFF` (pembukaan `both=false`), lalu `RESET MASTER`
(`resetBinaryLogs`, 8.0), lalu muat. Sebelum muat, T punya database
`localold` (1 baris) dan `gtid_executed` lokal `…:1-6`.

| Akun | `SET GLOBAL super_read_only = OFF` | `RESET MASTER` | Muat | Keadaan sesudahnya |
|---|---|---|---|---|
| Kontrol: A-d (dengan `CONNECTION_ADMIN`) | berhasil → ON/OFF | berhasil; `gtid_executed` kosong | **berhasil, exit 0** | ON/OFF; `gtid_executed` = `gtid_purged` = `2c6aa2c9-…:1-13`; `app`, `with space`, `mysql` termuat; akun lama tidak bisa login: `ERROR 1045 (28000): Access denied for user 'x2'@'localhost' (using password: YES)` (sama dengan putaran pertama) |
| **(i)** A-d − `CONNECTION_ADMIN` + `SUPER` | berhasil → ON/OFF | berhasil | **berhasil, exit 0** | sama persis dengan kontrol |
| **(ii)** A-d − `CONNECTION_ADMIN`, tanpa `SUPER` | berhasil → ON/OFF | berhasil; `gtid_executed` kosong | **gagal, exit 1**: `ERROR 1290 (HY000) at line 32: The MySQL server is running with the --read-only option so it cannot execute this statement` (``CREATE DATABASE … `mysql` ``) | ON/OFF; **`gtid_executed` = `gtid_purged` = `2c6aa2c9-…:1-13` (milik S)**, padahal tidak satu tabel pun dimuat; riwayat GTID lokal `…:1-6` dan binlog lama hilang; data lokal utuh (`localold` 1 baris, `mysql.user` masih memuat akun itu, akun itu masih bisa login) |
| (i′) A-r − `CONNECTION_ADMIN` + `SUPER` | berhasil | berhasil | gagal, exit 1: `ERROR 1227 (42000) at line 1084: Access denied; you need (at least one of) the SYSTEM_USER privilege(s) for this operation` (trigger `DEFINER=root@localhost`) | ON/OFF; `gtid_executed` = `…:1-13` milik S; tabel skema `mysql` sudah diganti milik S (`mysql.user` tidak lagi memuat akun itu), tetapi karena `FLUSH PRIVILEGES` tidak sampai dijalankan akun itu masih bisa login sampai flush atau restart berikutnya; `app` termuat sebagian |
| (ii′) A-r − `CONNECTION_ADMIN`, tanpa `SUPER` | berhasil | berhasil | gagal, exit 1: `ERROR 1290 (HY000) at line 32: The MySQL server is running with the --read-only option so it cannot execute this statement` | seperti (ii) |

Pernyataan pertama yang gagal untuk akun tanpa `CONNECTION_ADMIN` dan
tanpa `SUPER` adalah baris 32 dump, **sesudah** `RESET MASTER` dan sesudah
baris 26 (`SET @@GLOBAL.GTID_PURGED`) yang lolos; yang sudah rusak saat
gagal: binlog dan riwayat GTID lokal hilang, dan `gtid_executed` kini
mengaku memuat seluruh set GTID sumber tanpa datanya. Pembukaan dan `RESET
MASTER` sendiri tidak menolak akun itu (cukup `SYSTEM_VARIABLES_ADMIN` dan
`RELOAD`), dan uji login `checkClients` (`mysql … -N -e "SELECT 1"` dengan
`--init-command`) juga lolos, jadi tidak ada yang menangkapnya lebih awal.
`SUPER` menggantikan `CONNECTION_ADMIN` sepenuhnya untuk muat ini.

## X4. Pernyataan penjaga terhadap server yang sibuk

Penjaga: `persistReadOnly` mengirim `SET PERSIST read_only = ON` lalu `SET
PERSIST super_read_only = ON` pada koneksi dari pool, dengan konteks
`GuardTick`. Konteks itu `promoteGuardTimeout = 30 * time.Second`
(`internal/agent/monitor/monitor.go:104`, dipasang di `monitor.go:450`
dengan `context.WithTimeout`) untuk satu putaran penjaga. Di sini penjaga
memakai akun A (`dbpanel_adm`) lewat program Go; sesi lain memakai akun
aplikasi B (`appuser`, `ALL ON app.*`, tanpa `CONNECTION_ADMIN`) kecuali
disebut lain. Tabel `app.t` 5 baris, `app.u` kosong. Keadaan awal OFF/OFF
(server bisa ditulisi, penjaga mengunci lagi) kecuali disebut lain.
"Menunggu" = `information_schema.processlist.state` = `Waiting for global
read lock` untuk sesi penjaga, dibaca tiap detik.

| Skenario | Sesi lain | Pernyataan penjaga dan hasilnya | Bendera selama / sesudah | Pekerjaan sesi lain |
|---|---|---|---|---|
| (a) | B: `LOCK TABLES app.t WRITE`, ditahan ±11 s, lalu `INSERT INTO app.t VALUES (6,0); UNLOCK TABLES; INSERT INTO app.u …` | `SET PERSIST read_only = ON` **menunggu 10.99 s** (sampai `UNLOCK TABLES`), lalu berhasil; `SET PERSIST super_read_only = ON` berhasil 0.00 s | 0/0 selama menunggu; 1/1 sesudah; `mysqld-auto.cnf` memuat keduanya `ON` | `INSERT` ke `app.t` di bawah kunci **berhasil**; `INSERT INTO app.u` sesudah `UNLOCK` ditolak `ERROR 1290 (HY000) … --read-only option …` |
| (a) + sesi ketiga | seperti (a); 2 s setelah penjaga mulai menunggu, sesi B lain menjalankan `INSERT INTO app.u (note) VALUES ('third')` | `SET PERSIST read_only = ON` menunggu 6.99 s, lalu berhasil; `super_read_only` 0.00 s | 0/0 lalu 1/1 | sesi ketiga **ikut menunggu** (`Waiting for global read lock`) di belakang penjaga, lalu ditolak `ERROR 1290 (HY000) … --read-only option …` begitu penjaga selesai |
| (a) dibatalkan klien | seperti (a), ditahan 11 s | konteks Go 4 s: `SET PERSIST read_only = ON` → `context deadline exceeded` setelah 4.00 s; pernyataan berikutnya di koneksi yang sama → `driver: bad connection` (0.00 s). **Di server pernyataan itu tetap menunggu** (processlist masih menampilkan id sesi penjaga dengan `SET PERSIST read_only = ON`, `Waiting for global read lock`) dan **berlaku** begitu `UNLOCK TABLES` | 0/0 selama; **1/0 sesudah** (`read_only` ON dan di-PERSIST; `super_read_only` tetap OFF, tidak ada entrinya di `mysqld-auto.cnf`) | `INSERT` di bawah kunci berhasil; `INSERT` sesudah `UNLOCK` ditolak 1290 `--read-only` |
| (a) dengan `lock_wait_timeout` | seperti (a) | pada koneksi yang sama lebih dulu `SET SESSION lock_wait_timeout = 3`, tanpa batas klien: `SET PERSIST read_only = ON` → `Error 1205 (HY000): Lock wait timeout exceeded; try restarting transaction` setelah 3.00 s; `SET PERSIST super_read_only = ON` → 1205 yang sama setelah 3.00 s | 0/0 selama dan sesudah; `mysqld-auto.cnf` tidak dibuat | semua berhasil, termasuk `INSERT INTO app.u` sesudah `UNLOCK` (tidak ada yang tertinggal menunggu) |
| (a) dibatalkan lalu `KILL QUERY` | seperti (a) | batas klien 4 s (`context deadline exceeded`), lalu dari koneksi lain `KILL QUERY <id sesi penjaga>`: sesi itu langsung hilang dari daftar pernyataan berjalan | 0/0 selama dan sesudah; tidak di-PERSIST | semua berhasil |
| (b) | B: `UPDATE app.t SET v = v + 1 + 0*SLEEP(2)` (5 baris × 2 s), lalu `INSERT INTO app.u …` | `SET PERSIST read_only = ON` **menunggu 9.04 s** (sampai `UPDATE` selesai), lalu berhasil; `super_read_only` 0.00 s | 0/0 lalu 1/1 | `UPDATE` **selesai dan ter-commit** (`5 rows affected (10.00 sec)`, `SUM(v)` = 5); `INSERT` berikutnya ditolak 1290 `--read-only` |
| (c) | B: `BEGIN; INSERT INTO app.u … ;` lalu diam ±10 s di dalam transaksi, lalu `COMMIT; INSERT …` | **tidak menunggu**: kedua `SET PERSIST` berhasil 0.00 s | 1/1 seketika | `COMMIT` **ditolak** `ERROR 1290 (HY000) at line 2: The MySQL server is running with the --read-only option so it cannot execute this statement`; baris transaksi itu tidak ada (sesi berakhir, transaksi dibatalkan) |
| (d) | B: `BEGIN; SELECT COUNT(*) FROM app.t;` lalu diam ±6 s, lalu `SELECT …; COMMIT; INSERT …` | tidak menunggu: 0.00 s keduanya | 1/1 seketika | `SELECT` dan `COMMIT` berhasil; `INSERT` sesudahnya ditolak 1290 |
| awal ON/OFF, kunci oleh akun biasa | B: `LOCK TABLES app.t WRITE` | keduanya 0.00 s | 1/1 | `LOCK TABLES … WRITE` sendiri ditolak `ERROR 1290 (HY000) … --read-only option …` |
| awal ON/OFF, kunci oleh sesi berhak (root, punya `CONNECTION_ADMIN`, seperti klien `mysql` yang memuat dump) | root: `LOCK TABLES app.t WRITE; INSERT INTO app.t VALUES (6,0);` ditahan 8 s, lalu `INSERT … (7,0); UNLOCK TABLES; INSERT INTO app.u …` | `SET PERSIST read_only = ON` 0.00 s (sudah ON); `SET PERSIST super_read_only = ON` **menunggu 7.00 s** sampai `UNLOCK`, lalu berhasil | 1/0 selama; 1/1 sesudah | kedua `INSERT` di bawah kunci berhasil; `INSERT` sesudah `UNLOCK` ditolak `ERROR 1290 (HY000) … --super-read-only option …` |

Yang bisa diandalkan: pernyataan penjaga menunggu selama ada `LOCK TABLES
… WRITE` atau pernyataan tulis yang sedang berjalan (bukan transaksi yang
sekadar terbuka), dan semua sesi yang ingin menulis ikut antre di
belakangnya. Pembatalan konteks Go **tidak** menghentikan pernyataan di
server: ia tetap menunggu dan berlaku belakangan, bisa setengah (hanya
`read_only`); batas yang benar-benar berhenti adalah `SET SESSION
lock_wait_timeout = <n>` di koneksi yang sama (galat 1205, tanpa efek), atau
`KILL QUERY` dari koneksi lain sesudah pembatalan.

## X5. Bentuk keluaran `mysqldump` untuk satu database

Perintah (server S, akun root lewat soket):
`mysqldump --defaults-file=<root.cnf> --single-transaction --routines
--triggers --events --set-gtid-purged=OFF --databases <nama>`.

### Baris terakhir

| Kasus | Exit | Baris terakhir (persis) |
|---|---|---|
| dump lengkap | 0 | `-- Dump completed on 2026-11-16  1:37:09` (jam satu digit didahului **dua** spasi; diakhiri `\n`) |
| dengan `--skip-dump-date` (pembanding) | 0 | `-- Dump completed` |
| dengan `--skip-comments` (pembanding) | 0 | baris kosong — baris penutup **tidak ada** |

### Baris `CREATE DATABASE` menurut nama

| Nama | Exit | Baris `CREATE DATABASE` (persis) | Baris terakhir |
|---|---|---|---|
| `biasa` | 0 | ``CREATE DATABASE /*!32312 IF NOT EXISTS*/ `biasa` /*!40100 DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci */ /*!80016 DEFAULT ENCRYPTION='N' */;`` | `-- Dump completed on …` |
| `dengan spasi` | 0 | ``CREATE DATABASE /*!32312 IF NOT EXISTS*/ `dengan spasi` /*!40100 … */ /*!80016 DEFAULT ENCRYPTION='N' */;`` | `-- Dump completed on …` |
| ``back`tick`` | 0 | ``CREATE DATABASE /*!32312 IF NOT EXISTS*/ `back``tick` /*!40100 … */ /*!80016 DEFAULT ENCRYPTION='N' */;`` (backtick digandakan) | `-- Dump completed on …` |
| `o'brien` | **2** | ``CREATE DATABASE /*!32312 IF NOT EXISTS*/ `o'brien` /*!40100 … */ /*!80016 DEFAULT ENCRYPTION='N' */;`` | `--`; stderr: ``mysqldump: Couldn't execute 'use `o\'brien`': Unknown database 'o\'brien' (1049)`` |
| `dq"uote` | **2** | ``CREATE DATABASE /*!32312 IF NOT EXISTS*/ `dq"uote` /*!40100 … */ /*!80016 DEFAULT ENCRYPTION='N' */;`` | `--`; stderr: ``mysqldump: Couldn't execute 'use `dq\"uote`': Unknown database 'dq\"uote' (1049)`` |
| `rumah_sakit_é` | 0 | ``CREATE DATABASE /*!32312 IF NOT EXISTS*/ `rumah_sakit_é` /*!40100 … */ …;`` (byte UTF-8 apa adanya) | `-- Dump completed on …` |
| `数据库` | 0 | ``CREATE DATABASE /*!32312 IF NOT EXISTS*/ `数据库` /*!40100 … */ …;`` | `-- Dump completed on …` |
| `BESAR` | 0 | ``CREATE DATABASE /*!32312 IF NOT EXISTS*/ `BESAR` /*!40100 … */ …;`` (huruf besar tetap; `lower_case_table_names=0`) | `-- Dump completed on …` |
| `kosong` (tanpa tabel) | 0 | ``CREATE DATABASE /*!32312 IF NOT EXISTS*/ `kosong` …;`` | `-- Dump completed on …`; 0 baris `CREATE TABLE` |

(`/*!40100 … */` di atas selalu `/*!40100 DEFAULT CHARACTER SET utf8mb4
COLLATE utf8mb4_0900_ai_ci */`.) Setelah baris itu selalu ``USE `<nama>`;``
dengan kutip yang sama. Nama dengan `'` atau `"` **tidak bisa** di-dump oleh
mysqldump 8.0.46: tabelnya ikut tertulis, lalu dump berhenti pada tahap
routine dengan exit 2 (juga tanpa `--databases`). Baris `CREATE DATABASE`
selalu nama yang dikutip backtick dengan backtick di dalamnya digandakan;
kutip lain tidak di-escape.

### `CREATE TABLE` dibanding jumlah tabel dasar

Database `x5`: tabel `t1`, `t2` (FK ke `t1`), view `v1`, tabel berpartisi
`pt` (`PARTITION BY RANGE (YEAR(tgl))`), tabel `g` dengan kolom
`VIRTUAL` dan `STORED`, procedure `mk` yang badannya memuat baris
`CREATE TEMPORARY TABLE tmp_kerja (id INT);`, `CREATE TABLE IF NOT EXISTS
kerja (id INT);` dan `CREATE TABLE` (sendirian, nama di baris berikutnya),
function `f`, trigger `trg`, event `ev`. Tabel dasar menurut
`information_schema.tables`: 4 (`g`, `pt`, `t1`, `t2`); `v1` = `VIEW`.
Dump: exit 0, 265 baris.

| Baris berawalan | Nomor baris | Asal |
|---|---|---|
| ``CREATE TABLE `g` (`` | 33 | tabel `g` |
| ``CREATE TABLE `pt` (`` | 58 | tabel `pt`; partisinya di baris-baris berikutnya dalam `/*!50100 PARTITION BY … */`, tidak menambah baris `CREATE TABLE` |
| ``CREATE TABLE `t1` (`` | 85 | tabel `t1` |
| ``CREATE TABLE `t2` (`` | 126 | tabel `t2` |
| `CREATE TABLE IF NOT EXISTS kerja (id INT);` | 220 | **badan procedure `mk`** |
| `CREATE TABLE` | 221 | **badan procedure `mk`** (baris persis `CREATE TABLE`, tanpa spasi di belakang) |

Jadi `^CREATE TABLE` = 6, `^CREATE TABLE ` (dengan spasi) = 5, tabel dasar
4. View tidak menambah baris `CREATE TABLE`: struktur sementaranya
berbentuk ``/*!50001 CREATE VIEW `v1` AS SELECT `` (baris 152, didahului
``DROP TABLE IF EXISTS `v1`;``) dan bentuk akhirnya
`/*!50001 CREATE ALGORITHM=UNDEFINED */` (baris 249). Kolom
`VIRTUAL`/`STORED` tidak mengubah apa pun. `CREATE TEMPORARY TABLE` di
badan routine tidak berawalan `CREATE TABLE`.

Blok `DELIMITER` di dump ini (baris persis `DELIMITER ;;` membuka, baris
persis `DELIMITER ;` menutup):

| Blok | Buka | Tutup | Isi |
|---|---|---|---|
| trigger `trg` | 109 | 113 | ``/*!50003 CREATE*/ /*!50017 DEFINER=`root`@`localhost`*/ /*!50003 TRIGGER `trg` … BEGIN`` s.d. `END */;;` — letaknya **di bagian tabel `t1`**, sesudah datanya, bukan di akhir |
| event `ev` | 162 | 181 | baris `SET … */ ;;`, ``/*!50106 CREATE*/ /*!50117 DEFINER=…*/ /*!50106 EVENT `ev` … DO BEGIN`` s.d. `END */ ;;` |
| function `f` | 196 | 202 | ``CREATE DEFINER=`root`@`localhost` FUNCTION `f`() RETURNS int`` s.d. `END ;;` |
| procedure `mk` | 216 | 226 | ``CREATE DEFINER=`root`@`localhost` PROCEDURE `mk`()`` s.d. `END ;;` (baris 220–221 di dalamnya) |

Dihitung hanya di luar blok (`DELIMITER ;;` … `DELIMITER ;`), baris
berawalan `CREATE TABLE ` = **4** = jumlah tabel dasar.

Kasus tepi yang diukur: procedure yang badannya memuat string literal
berbaris banyak berisi baris `DELIMITER ;` lalu `CREATE TABLE palsu (id
INT)'`. Dump exit 0; di dalam blok procedure (buka baris 63) ada baris 66
`DELIMITER ;` milik string itu dan baris 67 `CREATE TABLE palsu (id INT)'
;;`, lalu penutup asli baris 68. Penghitung per baris menganggap blok
selesai di baris 66, jadi menghitung **2** untuk 1 tabel dasar. Dump itu
dimuat ulang dengan `mysql` tanpa galat (exit 0; klien mengenali string
literal), procedure dan tabel kembali utuh.

### Dump yang berhenti di tengah

| Penyebab | Exit | Baris terakhir berkas | stderr / catatan |
|---|---|---|---|
| view yang tabelnya sudah di-drop (`CREATE VIEW ab.vbad AS SELECT id FROM ab.b; DROP TABLE ab.b`) | **2** | ``DROP TABLE IF EXISTS `vbad`;`` (52 baris; 1 `CREATE TABLE` dari 2 tabel dasar) | ``mysqldump: Couldn't execute 'SHOW FIELDS FROM `vbad`': View 'ab.vbad' references invalid table(s) or column(s) or function(s) or definer/invoker of view lack rights to use them (1356)`` |
| view `SQL SECURITY DEFINER` yang definer-nya sudah di-drop (`DEFINER='gone'@'%'`) | 0 | `-- Dump completed on …` | **tidak** berhenti bila di-dump sebagai root; pembanding saja |
| nama database dengan `'` atau `"` | 2 | `--` | lihat tabel nama di atas |
| disk penuh: berkas tujuan di tmpfs 8 KiB (`mount -t tmpfs -o size=8k`), dump `x5` 9 932 byte | **5** | `CREATE T` (berkas terpotong tepat 8 192 byte, di tengah badan procedure `mk`, blok `DELIMITER ;;` masih terbuka) | `mysqldump: Got errno 28 on write` |
| stdout ke `/dev/full` (pembanding) | 5 | — | `mysqldump: Got errno 11 on write` |

Pada berkas yang terpotong karena disk penuh, jumlah baris berawalan
`CREATE TABLE ` di luar blok tetap **4 = jumlah tabel dasar**; hanya
baris terakhir, exit status dan blok yang belum ditutup yang
membedakannya dari dump lengkap.

Yang bisa diandalkan: dump selesai hanya bila exit 0 **dan** baris terakhir
diawali `-- Dump completed` (perintah tidak boleh memberi
`--skip-comments`/`--compact` dan harus membaca opsi hanya dari
`--defaults-file`, karena `[mysqldump] skip-comments` di berkas opsi lain
menghapus baris itu). Hitungan `CREATE TABLE` di luar blok `DELIMITER` cocok
untuk dump normal, tetapi bisa lebih besar (string literal) dan tidak
membuktikan kelengkapan (disk penuh lolos hitungan); aturan "paling
sedikit" aman, aturan "sama dengan" tidak.

## X6. Restart sesudah CLONE pada penerima yang `read_only` di-PERSIST

Persiapan: plugin clone terpasang di S dan T; di S akun `dbpanel_repl`
(`REPLICATION SLAVE, BACKUP_ADMIN`); di T akun A (`dbpanel_adm`).
Nilai yang di-PERSIST, dibuat berbeda supaya asalnya terlihat:

| | `mysqld-auto.cnf` sebelum CLONE |
|---|---|
| Donor S | `read_only` `OFF`, `super_read_only` `OFF`, `max_connections` `321` |
| Penerima T | `read_only` `ON`, `super_read_only` `ON`, `max_connections` `222` (berkas 433 byte, `-rw-r----- nobody:nogroup`) |

Langkah di T dengan akun A, seperti `cloneFrom`: `SET GLOBAL
clone_valid_donor_list = '127.0.0.1:34101'; SET GLOBAL clone_ssl_ca = '';
SET GLOBAL super_read_only = OFF` (→ ON/OFF, nilai PERSIST tidak
disentuh), lalu `CLONE INSTANCE FROM 'dbpanel_repl'@'127.0.0.1':34101
IDENTIFIED BY '…'`.

| Langkah | Hasil |
|---|---|
| `CLONE INSTANCE …` | `ERROR 3707 (HY000) at line 1: Restart server failed (mysqld is not managed by supervisor process).` setelah 0.37 s; log galat T: `Clone removing all user data for provisioning: Started` / `Finished`, lalu `[ERROR] [MY-013462] [Server] Clone shutting down server as RESTART failed. Please start server to complete clone operation.` |
| mysqld lama berhenti sendiri | proses lama hilang ±3.2 s setelah galat |
| `mysqld-auto.cnf` saat server mati | **sama byte demi byte dengan milik T sebelum CLONE** (433 byte, `read_only` ON, `super_read_only` ON, `max_connections` 222, metadata dan waktunya pun sama) |
| Restart dengan tangan, seperti `restartAfterClone` untuk galat 3707: tunggu sampai proses lama tidak ada (`waitStopped`; di sini memeriksa pid di `mysqld.pid` tiap 0.2 s), lalu `mysqld --defaults-file=<my.cnf> --daemonize` (sama dengan `Start` di supervisor harness), lalu tunggu sampai bisa login (`WaitReady`) | server naik dalam 1.25 s |
| Sesudah restart | `read_only` = 1, `super_read_only` = 1, `max_connections` = 222; `performance_schema.persisted_variables`: `read_only ON`, `super_read_only ON`, `max_connections 222`; `variables_info.variable_source` ketiganya `PERSISTED`; `clone_status` = `Completed 0`; `server_uuid` milik T tetap; `gtid_executed` = milik S (`…:1-69`); database = milik S; `mysqld-auto.cnf` tetap berkas T |
| Login akun A sesudahnya | `ERROR 1045 (28000): Access denied for user 'dbpanel_adm'@'localhost' (using password: YES)` (akun S menggantikan; jalur `AdminReplaced`) |

Yang bisa diandalkan: CLONE tidak menyalin dan tidak mengubah
`mysqld-auto.cnf` penerima; restart sesudahnya naik dengan nilai PERSIST
**penerima sebelum CLONE** (bukan donor, bukan bawaan), jadi penerima yang
`read_only`/`super_read_only`-nya di-PERSIST ON naik ON/ON walaupun
pembukaan sempat mematikan `super_read_only` dengan `SET GLOBAL`. (Penerima
yang nilainya hanya `SET GLOBAL` tanpa PERSIST tidak diukur; menurut hasil
ini ia naik dengan `my.cnf`/bawaan.)

## X7. Koneksi yang dipegang melewati muat penuh (`AdminReplaced`) dan `wait_timeout`

Persiapan: T baru, akun `x7` = A-d (dengan `CONNECTION_ADMIN`, tanpa
`SUPER`), bendera ON/ON di-PERSIST, `SET GLOBAL wait_timeout = 5` (untuk
meniru diam lebih lama dari `wait_timeout`). Program Go membuka sebelum
muat, sebagai `x7`, tiga koneksi tetap (`db.Conn`) dan satu pool:
**K_A** (`SET SESSION wait_timeout = 28800`, dipakai langsung sesudah
muat), **K_B** (mewarisi global 5 s, dipakai setelah diam 8 s), **K_C**
(`SET SESSION wait_timeout = 28800`, dipakai setelah diam 8 s), **P**
(`sql.DB` dengan satu koneksi menganggur, dipakai setelah diam 8 s). K_A
menjalankan `SET GLOBAL super_read_only = OFF` (→ ON/OFF). Lalu, dari sesi
lain sebagai `x7`: `RESET MASTER` dan muat penuh seperti X2 (exit 0;
sesudah `FLUSH PRIVILEGES` akun `x7` tidak ada lagi, 0 baris di
`mysql.user`).

| Koneksi | Pernyataan sesudah muat | Hasil | Bendera sesudahnya |
|---|---|---|---|
| K_A, dibuka sebelum muat, tidak diam | `SET GLOBAL super_read_only = ON` | **`Error 1227 (42000): Access denied; you need (at least one of) the SUPER or SYSTEM_VARIABLES_ADMIN privilege(s) for this operation`**; `CURRENT_USER()` masih `x7@%` | tetap 1/0 |
| K_A | `SET GLOBAL super_read_only = OFF`, `SET GLOBAL read_only = ON` | keduanya 1227 yang sama | 1/0 |
| K_B, diam 8 s > `wait_timeout` 5 | `SET GLOBAL super_read_only = ON` | `Error 4031 (HY000): The client was disconnected by the server because of inactivity. See wait_timeout and interactive_timeout for configuring this behavior.`; pernyataan berikutnya `driver: bad connection` | tidak berubah |
| K_C, diam 8 s, `wait_timeout` sesi 28800 | `SET GLOBAL super_read_only = ON` | koneksi masih hidup; **1227** yang sama (karena akun, bukan karena diam) | 1/0 |
| P (pool), diam 8 s | `SET GLOBAL super_read_only = ON` | driver membuang koneksi mati (`closing bad idle connection: unexpected read from socket`) dan login baru: `Error 1045 (28000): Access denied for user 'x7'@'localhost' (using password: YES)` | 1/0 |
| koneksi **baru** dengan kredensial lama | ping | `Error 1045 (28000): Access denied for user 'x7'@'localhost' (using password: YES)` | — |

Akhir percobaan: T tertinggal **ON/OFF**, dan tidak satu koneksi agent pun
bisa memasangnya lagi.

Dua pembanding, persiapan sama:

| Variasi | Hasil |
|---|---|
| akun `x7` ditambah `SUPER` (hak statis) | K_A: `SET GLOBAL super_read_only = ON` **berhasil** → 1/1 (juga `OFF` dan `read_only = ON`); K_C setelah diam 8 s: berhasil → 1/1; K_B: 4031; P dan koneksi baru: 1045 |
| tanpa `SUPER`, penutup muat diganti `\nSET GLOBAL super_read_only = ON;\nFLUSH PRIVILEGES;\n` (pemulihan dikirim di aliran muat, sebelum flush) | muat exit 0 (`FLUSH PRIVILEGES` berhasil walau `super_read_only=ON`); bendera **1/1** sesudah muat; K_A sesudahnya tetap 1227 |

Yang bisa diandalkan: hak dinamis (`SYSTEM_VARIABLES_ADMIN`) sesi yang
sudah terbuka dicabut begitu `FLUSH PRIVILEGES` memuat skema `mysql` yang
tidak memuat akunnya; hanya hak statis (`SUPER`) yang ikut tersimpan di
sesi. Koneksi yang diam lebih lama dari `wait_timeout` mati dengan 4031,
dan `SET SESSION wait_timeout = 28800` di awal koneksi mencegahnya; pool
`database/sql` diam-diam login ulang, yang sesudah `AdminReplaced` ditolak
1045.

## Yang tidak diukur

- **X3**: MySQL 8.4 tidak terpasang; semua angka di atas hanya untuk 8.0.46.
- X4 (b), (c), (d) hanya diukur tanpa pembatalan; pembatalan dan
  `lock_wait_timeout` hanya pada skenario (a). Mekanisme tunggunya sama
  (`Waiting for global read lock`), tetapi tidak dibuktikan per skenario.
- X4 dengan batas 30 s penuh tidak dijalankan (diganti 4 s; tidak ada yang
  bergantung pada panjangnya, hanya pada apa yang terjadi saat batas habis).
- X5: view dengan definer yang hilang tidak membuat dump root berhenti;
  akun dump lain (tanpa `SET_USER_ID`/`SYSTEM_USER`) tidak dicoba.
  Disk penuh hanya disimulasikan dengan tmpfs 8 KiB (dan `/dev/full`), bukan
  disk sungguhan. Nama database yang memuat `\` atau baris baru tidak dicoba.
- X6: penerima yang `read_only`-nya hanya `SET GLOBAL` (tanpa PERSIST), dan
  CLONE yang di-restart oleh supervisor (systemd) sungguhan, tidak diukur.
- X7: hanya muat penuh lewat klien `mysql`; jalur CLONE dengan
  `AdminReplaced` (X6 menunjukkan login 1045) tidak diukur untuk koneksi
  yang dipegang, karena CLONE mematikan server dan semua sesinya.

## Yang bisa diandalkan task berikutnya

- **X2 → `dumpFrom`**: dengan `both=false`, akun muat yang tidak punya
  `CONNECTION_ADMIN` maupun `SUPER` gagal di baris 32 dump (`ERROR 1290`
  `--read-only`), **sesudah** `RESET MASTER` dan sesudah `SET
  @@GLOBAL.GTID_PURGED`: riwayat GTID lokal hilang dan `gtid_executed`
  mengaku memuat set sumber tanpa datanya. Pembukaan, `RESET MASTER` dan
  `checkClients` tidak menangkapnya. Jadi `dumpFrom` perlu pemeriksaan
  hak sebelum `STOP REPLICA`/reset: akun muat memegang `CONNECTION_ADMIN`
  atau `SUPER` (yang mana saja cukup; `SUPER` saja terbukti cukup).
- **X4 → penjaga**: `SET PERSIST read_only/super_read_only = ON` menunggu
  `LOCK TABLES … WRITE` dan pernyataan tulis yang berjalan (juga milik sesi
  berhak, seperti muat dump yang memegang `LOCK TABLES` saat
  `read_only` sudah ON), tidak menunggu transaksi yang sekadar terbuka
  (commit-nya kemudian gagal 1290), dan selama menunggu semua penulis lain
  antre di belakangnya. Batas klien (konteks 30 s `promoteGuardTimeout`)
  **tidak cukup**: pernyataan tetap berjalan di server dan berlaku
  belakangan, bisa hanya `read_only` (1/0, ter-PERSIST). Batasi di server:
  `SET SESSION lock_wait_timeout = <n detik, < 30>` pada koneksi yang sama
  sebelum kedua `SET PERSIST`. "Tidak bisa mengunci" terlihat sebagai
  `Error 1205 (HY000): Lock wait timeout exceeded; try restarting
  transaction` setelah n detik dan bendera tidak berubah (`persistReadOnly`
  berhenti pada galat pertama; dalam pengukuran, `SET PERSIST
  super_read_only = ON` yang dikirim sesudahnya juga menunggu n detik lalu
  1205). Penjaga yang mendapat 1205 harus memperlakukannya sebagai "server
  sibuk, belum terkunci", bukan sebagai galat login. Bila tetap
  memakai batas klien, sesudahnya perlu `KILL QUERY <id>` dari koneksi lain.
- **X5 → pemeriksaan cadangan `drop-database`**: (1) exit status mysqldump
  0; (2) baris terakhir berkas diawali `-- Dump completed` (bentuk penuh
  `-- Dump completed on YYYY-MM-DD HH:MM:SS` dengan jam satu digit diberi
  spasi tambahan; jangan cocokkan tanggalnya), dan mysqldump dijalankan
  tanpa `--skip-comments`/`--compact` dengan `--defaults-file` sebagai
  berkas opsi satu-satunya; (3) ada baris ``CREATE DATABASE /*!32312 IF NOT
  EXISTS*/ `<nama dengan ` digandakan>` `` (awal baris persis begitu); (4)
  baris yang diawali `CREATE TABLE ` (dengan spasi) di luar blok yang
  dibuka baris persis `DELIMITER ;;` dan ditutup baris persis `DELIMITER ;`
  berjumlah **paling sedikit** jumlah tabel dasar, dan tidak ada blok yang
  masih terbuka di akhir berkas. Hitungan saja tidak membuktikan apa-apa
  (dump terpotong karena disk penuh lolos hitungan); (1) dan (2) yang
  membuktikan. Database dengan `'` atau `"` di namanya tidak bisa di-dump
  oleh mysqldump 8.0.46 (exit 2) — teks kegagalan cadangan harus menangani
  ini (R-11 butir 2).
- **X6 → gerbang sesudah restart CLONE**: server naik dengan nilai PERSIST
  penerima sebelum CLONE; `mysqld-auto.cnf` tidak disentuh CLONE. Server
  yang dikarantina (keduanya di-PERSIST ON) naik ON/ON tanpa perlu
  dipulihkan, jadi pemegang CLONE boleh dilepas tanpa memulihkan bendera
  (`abandon` di R-1), dan restart itu bukan "start dengan bendera mati"
  (R-3 d). Akun tersimpan bisa ditolak 1045 sesudahnya (akun donor).
- **X7 → koneksi pemulih**: sesudah muat penuh yang mengganti akun, tidak
  ada koneksi agent yang bisa memulihkan bendera: koneksi yang dipegang
  sejak sebelum muat ditolak `1227` (hak dinamis ikut hilang saat `FLUSH
  PRIVILEGES`), koneksi baru dan pool ditolak `1045`. Pemulihan yang
  terbukti berhasil: kirim `SET GLOBAL super_read_only = ON` (dan `read_only`
  bila perlu) **di aliran muat itu sendiri, sebelum `FLUSH PRIVILEGES`**
  pada penutup, atau, pada koneksi yang dipegang, hanya bila akun itu punya
  `SUPER`. Untuk muat yang tidak mengganti akun, koneksi yang dipegang
  dengan `SET SESSION wait_timeout = 28800` di awalnya bertahan melewati
  diam; tanpa itu ia mati dengan `4031` dan pool login ulang.
