# Audit Keamanan dbpanel — Rencana 4a (Prasyarat Keamanan dan HTTPS Panel)

- Tanggal: 2026-11-05
- Commit yang diaudit: `d785a4c` (HEAD, `test(e2e): HTTPS, pinned pairing, verified replication; docs for Plan 4a`), rentang Rencana 4a `0d040d3..d785a4c` (16 commit, 131 berkas, +11.322/−944 baris; `go.mod`/`go.sum` tidak berubah)
- Modul: `github.com/husmin-fajrin/db-panel`
- Pembanding: audit Rencana 3 (`docs/superpowers/security/2026-11-02-audit-plan-3.md`, SEC-42..SEC-53 dan §7), spesifikasi Rencana 4 §5.7–5.11, §8, §9 (`docs/superpowers/specs/2026-11-05-rencana-4-promote-design.md`), rencana `docs/superpowers/plans/2026-11-05-04a-prasyarat-keamanan-https.md`, ledger SDD dengan ruling (`.superpowers/sdd/2026-11-05-04a-prasyarat-keamanan-https/progress.md`)
- Sifat: audit internal atas kode milik sendiri, baca-saja terhadap repo. Semua PoC dijalankan pada salinan sumber di `/tmp/dbpanel-audit4a/src` (berkas test PoC hanya ditambahkan di salinan itu), binary panel `/tmp/dbpanel-audit4a/dbpanel` (go1.26.10, `127.0.0.1:18080` UI HTTPS+HTTP dan `127.0.0.1:18443` agent, `--allow-root`, `--data-dir`/`--config-dir` sementara), dan `mysqld` 8.0.46 sementara dari harness `testmysql` (direktori `/var/tmp/dbpanel-test-*` yang dibuat dan dihapus sendiri). `install.sh` hanya dijalankan lewat `scripts/install_test.sh` (root sementara `ROOT=...`, systemctl tiruan). Proses panel dan helper PoC sudah dimatikan dan direktori sementara dihapus. Working tree, data dir, dan state git repo tidak disentuh, kecuali laporan ini (tidak di-commit).

---

## 1. Ringkasan eksekutif

Tingkat risiko keseluruhan: **Tinggi**, turun ke **Sedang** setelah SEC-54 diperbaiki (perbaikannya kecil: sidik jari CA di kanal tepercaya dan di halaman pasang). Bila SEC-55 dan SEC-56 juga diperbaiki, risiko turun ke **Rendah–Sedang**.

Rencana 4a mengerjakan hampir semua butir yang dijadwalkan audit Rencana 3 dengan rapi. Panel kini berjalan sebagai pengguna `dbpanel` (SEC-06). Agent tertanam diganti agent lokal root yang berpasangan sendiri, sehingga pin di host panel kini milik root (SEC-52 tertutup). UI dilayani HTTPS di satu port dengan cookie `__Host-` `Secure`, HSTS, dan pairing yang dipin ke SPKI (SEC-23). Tombol destruktif meminta kata sandi lewat token sekali pakai yang terikat ke user, aksi, dan target (SEC-28 butir 5). Replikasi, dump, dan CLONE memverifikasi sertifikat MySQL dari CA panel (SEC-29 butir 2 / SEC-41). Akun dump memakai `FLUSH_TABLES`, bukan `RELOAD`, dan `RESET MASTER` ditolak dengan 1227. Agent menyapu akun dump sendiri tiap jam, dan `releaseRole` mempertahankan pin sumber (SEC-42 butir 2 dan 4, SEC-43 butir 1).

Hasil uji yang lulus:
- Token konfirmasi: binding, kedaluwarsa 2 menit, tolak replay, CSRF, dan batas 5 per menit diuji pada binary.
- Listener ganda: request smuggling CL+TE, pipelining lintas server, slowloris (1.800 koneksi diam atau setengah jadi ditutup ≤ 6,5 detik, request sah dijawab 30–70 ms), validasi Host, dan traversal `/static/..`.
- Pin `--pinnedpubkey`: pin salah → curl rc=90.
- Name constraints: Go dan OpenSSL menolak IP dan DNS di luar batas.
- `install.sh`: harness lulus, dengan symlink dan hardlink ditangani.
- Header keamanan: semua kelas respons HTTPS dan HTTP lengkap.
- `go vet`, `go mod verify`, dan govulncheck bersih.

Satu temuan **Tinggi**:

1. **SEC-54**: sertifikat keamanan panel adalah **CA root**. Sertifikat itu diunduh lewat **HTTP polos** (`http://<ip>:8080/sertifikat`, ditunjuk `install.sh` dan pengalihan), dan panel tidak menampilkan sidik jari yang bisa dicocokkan di mana pun. Panduannya meminta pengguna memasang berkas itu di *Trusted Root Certification Authorities* (Local Machine), Keychain System, Firefox, Android, dan iOS, serta menyebarkannya lewat Group Policy ke semua komputer domain. Penyerang on-path di LAN, yaitu penyerang yang menjadi alasan HTTPS dibuat, bisa mengganti berkas itu dengan CA miliknya **tanpa name constraints**. Dampaknya menyadap semua HTTPS di komputer itu (atau di seluruh domain), bukan hanya panel, termasuk perintah pasang agent yang ditampilkan panel (root di server DB baru).

Tiga temuan **Sedang**:

- **SEC-55**: CA web jauh lebih luas dari "alamat panel". CA itu berlaku untuk seluruh subnet tiap antarmuka (termasuk bridge dan VPN) dan untuk subdomain setiap nama tambahan tanpa pemeriksaan akhiran publik. PoC: setelah sesi admin menyimpan nama tambahan `rsud.local co.id`, panel membuat CA baru yang sah untuk `www.bca.co.id` dan `simrs.rsud.local` (Go dan OpenSSL: OK). README mengklaim sebaliknya.
- **SEC-56**: akun dump `FLUSH_TABLES` dari IP mana pun tetap bisa menahan `FLUSH TABLES WITH READ LOCK` di master yang dipin. Sapuan agent tidak memutus sesinya dan malah gagal (1205) selama kunci dipegang. PoC di mysqld 8.0.46: `RESET MASTER` → 1227, FTWRL → OK, `CREATE DATABASE` root → 1205, sapuan → `Error 1205`, sesi tetap hidup.
- **SEC-57**: HSTS tidak berlaku untuk alamat IP (RFC 6797 §8.1), padahal alamat bawaan panel adalah IP. Bookmark `http://` lama dan alamat yang diketik selalu lewat HTTP polos, sehingga SSL stripping dan phishing sandi tetap mungkin. README mengklaim "browser … tidak lagi mencoba HTTP".

Lima temuan Rendah:
- SEC-58: TOCTOU symlink pada `<datadir>/dbpanel-tls` milik `mysql`. PoC: root menulis berkas milik `mysql` dan melakukan `chmod 0700` di direktori lain.
- SEC-59: pengaturan sertifikat HTTPS tanpa konfirmasi sandi dan dengan validasi unggahan yang lemah. PoC: sertifikat RSA-1024 `CA:TRUE` dengan EKU codeSigning diterima.
- SEC-60: celah kecil token konfirmasi, yaitu orakel sandi terpisah dari limiter login dan penolakan token yang tidak diaudit.
- SEC-61: sisa batas root vs `dbpanel` di `install.sh` dan unit (`runuser` tanpa pty, tanpa `SystemCallFilter`, eksposur systemd 7.1).
- SEC-62: agent lama tidak mendapat pagar baru dan panel tidak menuntut pembaruan.

Satu temuan Info (SEC-63) dan dua risiko Diterima (SEC-64, SEC-65).

## 2. Lingkup dan metode

**Berkas yang dibaca seluruhnya:**
- Panel: `internal/panel/tlsui.go`, `servers.go`, `localagent.go`, `cli.go`, perubahan `panel.go` dan `listener_impl.go`; `internal/panel/ca/{web,mysql}.go` dan perubahan `ca.go`; `internal/pki/pki.go`.
- Web: `internal/panel/web/{handlers_confirm,handlers_cert}.go`, `router.go` (header, `RequireHost`, rute), perubahan `handlers_servers.go`, `handlers_mysql.go`, `handlers_controls.go`; `internal/panel/auth/middleware.go`; `internal/panel/store/{confirmations,tls}.go` dan `migrations/0006_confirm_tls.sql`.
- Orkestrator: `internal/panel/orchestrator/tls.go` serta perubahan `create_replica.go`, `snapshot.go`, `runner.go`, `change_databases.go`; `internal/panel/agentapi/{pair,install}.go`.
- Agent: `internal/agent/drivers/mysql/tls.go`, perubahan `users.go`, `pins.go`, `replication.go`, `dump.go`, `clone.go`, `connect.go`, `preflight.go`, `snapshot.go`; `internal/agent/{pair,localpair,cli}.go`; `internal/agent/monitor/monitor.go` (sapuan).
- Skrip dan UI: `scripts/install.sh`, `scripts/systemd/*`, `web/templates/{agent-install.sh.tmpl,cert.html,_confirm.html}`, perubahan `web/static/app.js`.
- Dokumen: README (HTTPS, sertifikat, akun dump, pembaruan), spesifikasi §5.7–5.11, §8, §9, rencana 4a, ledger SDD.

**Alat yang dijalankan:**
- `graphify query` untuk navigasi awal.
- `go vet ./...`: bersih. `go mod verify`: "all modules verified". `go list -m all`.
- govulncheck v1.9.0, dirakit offline dari `GOMODCACHE` dengan go1.26.10, DB `https://storage.googleapis.com/go-vulndb`. Lihat Lampiran 8.2.
- `CGO_ENABLED=0 go build ./cmd/dbpanel` → `/tmp/dbpanel-audit4a/dbpanel`, dijalankan dengan direktori sementara, lalu `curl`, `openssl s_client`, dan skrip Python (`-I`) untuk soket mentah.
- `openssl verify` dan program Go kecil (`x509.Verify`) terhadap `web-ca.crt` yang dibuat panel.
- Test Go PoC di salinan sumber: agent nyata (`mysql.Driver` + `commands.Registry` + `Pins`) di atas mysqld 8.0.46, plus helper `flipper` yang dijalankan sebagai `nobody` (pemilik datadir di harness).
- `scripts/install_test.sh` (lulus), `systemd-analyze security --offline=true` pada unit panel.

**PoC yang dicoba** (hasil di tiap temuan dan Lampiran 8.3):
1. Unduhan CA lewat HTTP polos, isi halaman `/sertifikat`, keluaran `install.sh` (SEC-54).
2. Name constraints CA web: IP lain di subnet, IP luar, DNS luar, email/URI; nama tambahan `rsud.local co.id` → CA baru → `www.bca.co.id` (SEC-55).
3. Akun dump di master yang dipin: host `%`, pola, IP literal, akun yang sudah ada; `RESET MASTER`, FTWRL, sapuan agent saat kunci dipegang (SEC-56).
4. Pengalihan HTTP → HTTPS, HSTS pada jawaban HTTPS, perilaku untuk host IP (SEC-57).
5. Balapan symlink `dbpanel-tls` (pemilik datadir menukar direktori dengan symlink lewat `renameat2(RENAME_EXCHANGE)`) melawan `mysql.tlsTrust` root (SEC-58).
6. Unggah sertifikat sendiri (RSA-1024, `CA:TRUE`, EKU codeSigning, self-signed), kembali ke sertifikat panel, nama tambahan (SEC-59, SEC-55).
7. Siklus token konfirmasi: limiter, salah target, salah aksi, lintas sesi, replay, kedaluwarsa 121 detik, CSRF, target tak ada, audit (SEC-60, §4A).
8. Listener ganda: CL+TE, pipelining, byte TLS setelah HTTP di koneksi yang sama, 1.500 koneksi diam + 300 header setengah jadi, request sah di bawah beban, Host asing/`@`/`/`, traversal (§4A).
9. Pin pairing: `curl -k --pinnedpubkey` benar dan salah (§4A).
10. `install.sh` lewat harness: pasang baru, ulang, pembaruan dari tata letak root, terputus (§4A); analisis symlink, hardlink, `runuser`.
11. Header keamanan pada semua kelas respons HTTPS dan HTTP (§4B).

## 3. Model ancaman delta

Model ancaman Rencana 1–3 tetap berlaku (Rencana 3 §3 butir 12–17). Rencana 4a mengubah batas dan menambah aset berikut:

- **Aset baru:**
  - CA web (`web-ca.key`) yang **dipasang pengguna sebagai CA root** di browser dan sistem operasi, dan bisa disebar lewat GPO;
  - CA MySQL (`mysql-ca.key`) beserta sertifikat dan kunci server MySQL di `<datadir>/dbpanel-tls`;
  - token konfirmasi (`confirmations`);
  - token pasangan lokal (`local-pair-token`);
  - pin SPKI di perintah pasang.
- **Batas yang berubah:**
  - Proses web bukan lagi root. Batas panel→host kini adalah (a) permukaan perintah agent lokal (root, dijaga pin di `/etc/dbpanel` milik root), (b) `install.sh` yang membaca berkas di direktori milik `dbpanel`, dan (c) unit systemd.
  - Kepercayaan browser→panel kini bergantung pada CA web yang dipasang pengguna.

18. **Panel yang dibobol (pengguna `dbpanel`) vs root:** symlink dan hardlink di direktori panel, berkas token dan sandi yang dibaca root, `bin/` yang disajikan, `reset-local-agent`, ptrace dan TTY pada proses `runuser`, unit hardening; plus semua CA yang dipegang panel (web, MySQL, agent).
19. **Panel yang dibobol vs master/replika yang dipin** (butir 12, lanjutan): akun dump `FLUSH_TABLES`, pilihan verifikasi TLS per perintah, `tlsInstall` mengganti `ssl_ca` server, `releaseRole`.
20. **Penyerang on-path di LAN:**
  - halaman HTTP pembantu dan unduhan CA;
  - pengalihan 301;
  - HSTS;
  - listener terbelah (smuggling, slowloris, Host);
  - pairing pertama dengan `-k --pinnedpubkey`;
  - replikasi, dump, dan CLONE yang diverifikasi.
21. **Agent jahat:** CSR dan hasil `tlsInstall` palsu, nama di sertifikat MySQL, token lokal.
22. **Operator yang keliru:** nama tambahan yang terlalu luas, unggahan sertifikat yang salah, "Pasang sertifikat MySQL" di server yang punya TLS sendiri, token konfirmasi.
23. **Pemilik OS user `mysql`** (mysqld dibobol): direktori `dbpanel-tls` di dalam datadir miliknya.
24. **Campuran versi:** panel baru dengan agent sebelum Rencana 4a.

## 4. Temuan

### 4.0 Tabel kerentanan

| ID | Tingkat | Jenis | Judul | Dapat dieksploitasi oleh | PoC |
|---|---|---|---|---|---|
| SEC-54 | **Tinggi** | Kerentanan (desain, A02/A08) | CA root panel diunduh lewat HTTP polos tanpa sidik jari yang bisa dicocokkan; panduan memasangnya di Trusted Root dan GPO → CA penyerang tanpa batasan | On-path LAN (+ pengguna memasang sertifikat sesuai panduan) | Ya (unduhan HTTP) + analisis |
| SEC-55 | **Sedang** | Kerentanan (desain PKI) | CA web berlaku untuk seluruh subnet antarmuka dan subdomain nama tambahan apa pun (`co.id`); bisa diperluas sesi admin; README keliru | Panel dibobol; sesi admin yang keliru atau dicuri | Ya |
| SEC-56 | **Sedang** | Kerentanan (DoS, sisa SEC-42) | Akun dump `FLUSH_TABLES` dari IP mana pun menahan FTWRL di master yang dipin; sapuan agent tidak memutus sesi dan gagal 1205 | Panel dibobol | Ya |
| SEC-57 | **Sedang** | Kelemahan (A02/A05) | HSTS tidak berlaku untuk host IP (alamat bawaan); bookmark/ketik `http://` selalu lewat HTTP polos → SSL stripping; README keliru | On-path LAN | Analisis + curl |
| SEC-58 | Rendah | Kerentanan (TOCTOU, A01) | `<datadir>/dbpanel-tls` di direktori milik `mysql`: root agent mengikuti symlink (berkas di direktori lain, `chmod 0700`, rename `server.*`) | OS user `mysql` | Ya |
| SEC-59 | Rendah | Kelemahan (A04/A05) | Unggah/ganti sertifikat HTTPS, nama tambahan, tutup pemberitahuan tanpa token konfirmasi; unggahan RSA-1024 `CA:TRUE` codeSigning diterima | Sesi admin (dicuri atau keliru) | Ya |
| SEC-60 | Rendah | Kelemahan (A07/A09) | Token konfirmasi: `/confirm` orakel sandi 5/menit terpisah dari limiter login; token yang ditolak tidak diaudit; terikat user bukan sesi; token untuk target yang tidak ada | Sesi curian; forensik | Ya |
| SEC-61 | Rendah | Kelemahan (batas root) | `install.sh`: `runuser` tanpa pty saat panel berjalan (kelas TIOCSTI), sandi dicetak mentah, `[ -s ]` mengikuti link; unit tanpa `SystemCallFilter`/`ProtectProc` (eksposur 7.1) | Panel dibobol | Analisis + `systemd-analyze` |
| SEC-62 | Rendah | Kelemahan (campuran versi) | Agent sebelum 4a tetap membuat akun dump `RELOAD`, tanpa sapuan sendiri, `releaseRole` menghapus pin sumber; panel tidak menuntut pembaruan | Panel dibobol; operator | Telusur |
| SEC-63 | Info | Catatan | Robustness/PKI: CLONE hanya rantai, `ssl_ca` server diganti, serial `tlsInstall` tak dicocokkan, `tls_verified` sebelum IO thread, OpenSSL dan pengecualian kosong, `web-ca.key.old`, dll. | Beragam | Sebagian |
| SEC-64 | Diterima | Risiko residual | Panel dibobol tetap memegang semua CA dan menyajikan skrip/binary agent → root di server yang dipasang/diperbarui lewat perintah panel | Panel dibobol | — |
| SEC-65 | Diterima | Risiko residual | Pengguna yang tidak memasang sertifikat tetap melihat peringatan; penyamaran tidak terdeteksi (spesifikasi §8) | On-path LAN | — |

---

### SEC-54 — CA root panel dipasang lewat HTTP polos tanpa sidik jari yang bisa dicocokkan
- **Tingkat:** Tinggi. CVSS 3.1 `AV:A/AC:H/PR:N/UI:R/S:C/C:H/I:H/A:N` ≈ 7.5. Penyerangnya persis penyerang yang menjadi alasan SEC-23 ditutup. Dampaknya melampaui panel (lingkup berubah). Interaksi pengguna adalah langkah yang **diminta produk sendiri**.
- **Dieksploitasi oleh:** siapa pun yang on-path antara komputer operator dan panel (ARP/DHCP spoofing di LAN rumah sakit, Wi-Fi bersama, switch yang dibobol) pada saat sertifikat dipasang.
- **Lokasi:**
  - `internal/panel/tlsui.go:773-791` (`certPath`, `httpHelperHandler`: `/sertifikat`, `/sertifikat/dbpanel-panel.crt`, dan `/static/*` dilayani lewat HTTP polos);
  - `internal/panel/web/handlers_cert.go:62-79` (unduhan `web-ca.crt` apa adanya);
  - `web/templates/cert.html:10-27,35-37,51,64,77,87` (panduan: Local Machine → Trusted Root, Keychain System → Always Trust, Firefox "Trust this CA to identify websites", Android/iOS, GPO; tanpa sidik jari);
  - `scripts/install.sh:461-462` (mencetak `Buka http://<ip>:8080/sertifikat`);
  - README "Sertifikat keamanan panel".
  - Sidik jari CA web tidak ditampilkan di halaman HTTPS mana pun (`CertInfo` hanya memuat pin SPKI leaf). Ledger Task 5 menunda "CA fingerprint on /sertifikat".
- **Serangan dan dampak:**
  1. Operator membuka `http://10.x.x.x:8080` (bookmark lama, alamat yang diketik, atau saran `install.sh`), mendapat peringatan browser, lalu mengikuti halaman `/sertifikat`.
  2. Penyerang on-path menjawab request HTTP itu (halaman dan berkas) dengan CA miliknya tanpa name constraints, lengkap dengan nama `dbpanel-web-ca`.
  3. Pengguna memasangnya ke Trusted Root (Local Machine). Admin domain bahkan bisa menyebarkannya ke semua komputer lewat GPO, sesuai panduan.

  Akibatnya:
  - **(a)** penyerang bisa menerbitkan sertifikat untuk situs apa pun (surel, perbankan, SIMRS web, pembaruan perangkat lunak yang memakai penyimpanan sistem) dan menyadap komputer itu tanpa peringatan, untuk waktu yang lama;
  - **(b)** penyerang menyamar sebagai panel tanpa peringatan dan menangkap sandi admin, yang juga lolos token konfirmasi;
  - **(c)** perintah pasang agent yang ditampilkan halaman server bisa diganti (pin `--pinnedpubkey` dan hash skrip milik penyerang) → root di server DB baru.

  Tanpa langkah 3 pun, (b) dan (c) tetap mungkin selama operator mengklik peringatan (SEC-65). Namun langkah 3 seharusnya menutupnya, dan justru menjadi pintu yang lebih besar. Name constraints CA web (SEC-55) tidak menolong, karena CA yang dipasang bukan CA panel.
- **Bukti (binary yang berjalan):**
  ```
  $ curl -s -D - http://127.0.0.1:18080/sertifikat/dbpanel-panel.crt -o dl.crt
  HTTP/1.1 200 OK
  Content-Type: application/x-x509-ca-cert
  Content-Disposition: attachment; filename="dbpanel-panel.crt"
  (tanpa TLS, tanpa HSTS)
  $ openssl x509 -in dl.crt -noout -subject -fingerprint -sha256
  subject=CN = dbpanel-web-ca
  sha256 Fingerprint=80:D9:DB:0C:…:C0:7C      ← tidak tercantum di halaman, di Pengaturan, atau di keluaran install.sh
  $ grep -ci 'sidik\|fingerprint\|SHA-256' web/templates/cert.html
  0
  ```
- **Perbaikan minimal:**
  1. Tampilkan sidik jari SHA-256 `web-ca.crt` (format yang ditampilkan Windows, macOS, dan Firefox: heksadesimal dengan titik dua) di:
     - **keluaran `install.sh`** (terminal root adalah kanal tepercaya), dibaca dengan `openssl x509 -fingerprint -sha256` dari berkas yang dibaca sebagai panel;
     - halaman Pengaturan (HTTPS, setelah login);
     - halaman `/sertifikat`, dengan langkah "cocokkan angka ini dengan yang ditampilkan di layar pemasangan/admin sebelum klik Pasang" untuk tiap sistem.
  2. `install.sh` dan pemberitahuan "CA baru" menunjuk `https://…/sertifikat` (pengguna menerima peringatan sekali, lalu mencocokkan sidik jari). HTTP pembantu tetap ada untuk browser yang menolak, tetapi halamannya menyebut bahwa unduhan lewat HTTP harus dicocokkan dulu.
  3. Untuk GPO: sarankan menyalin `web-ca.crt` dari host panel (SSH/USB) dan mencocokkan sidik jari, bukan mengunduh lewat browser.
  4. Pertimbangkan memasang CA hanya untuk penggunaan TLS server (Windows: *Intermediate*/*Enterprise* tidak cukup untuk root; tetap root). Pada macOS, "Always Trust" hanya untuk SSL, bukan semua penggunaan. Panduan Firefox sudah membatasi ke "identify websites".
- **Kapan:** sebelum merge (butir 1–2 murah); butir 3–4 sebelum rilis.

### SEC-55 — CA web berlaku untuk seluruh subnet dan semua subdomain nama tambahan
- **Tingkat:** Sedang. CVSS 3.1 `AV:A/AC:H/PR:H/UI:R/S:C/C:H/I:H/A:N` ≈ 6.6. Prasyaratnya kunci CA web di tangan penyerang (panel dibobol; kunci dibaca proses `dbpanel`) **atau** nama tambahan yang terlalu luas yang disimpan admin. Dampaknya mengenai layanan lain yang dipercaya browser yang memasang CA panel.
- **Lokasi:**
  - `internal/panel/tlsui.go:538-575` (`webNames`: subnet setiap antarmuka yang aktif, kecuali loopback/link-local; termasuk bridge kontainer, VPN, dan IPv6 global /64);
  - `tlsui.go:441-456` (`SetExtraNames`: hanya `ValidWebHost`, yaitu ≥ 2 label);
  - `internal/panel/ca/web.go:57-90,97-135` (`PermittedDNSDomains` = nama apa adanya, yang menurut RFC 5280 mencakup semua subdomain; `PermittedIPRanges` = subnet);
  - `tlsui.go:290-306` (`renew`: nama di luar batas → CA baru otomatis);
  - README baris 812-816 ("dibatasi hanya untuk alamat panel … Sekalipun kuncinya bocor, sertifikat itu tidak bisa dipakai untuk menyamar sebagai situs lain").
- **Serangan dan dampak:**
  1. *Subnet:* host panel `192.168.1.10/24` → CA sah untuk **semua** `192.168.1.0/24`. Di jaringan rumah sakit yang datar (`/16` atau `10.0.0.0/8`), CA sah untuk setiap layanan HTTPS yang dibuka lewat IP: SIMRS web, PACS, antarmuka switch/printer/UPS, iDRAC/iLO. Panel yang dibobol (pengguna `dbpanel`, yang membaca `web-ca.key`) bisa menyadap semua itu di browser yang memasang CA panel, tanpa peringatan. Antarmuka `docker0` (`172.17.0.0/16`) dan VPN ikut masuk.
  2. *Nama tambahan:* admin yang ingin "semua nama kantor" menulis `rsud.local` atau, karena salah paham, `co.id`/`go.id`. `validHost` menerimanya, `renew` membuat **CA baru** dengan batasan itu, dan panel menampilkan pemberitahuan agar semua komputer memasang ulang. Setelah itu CA panel sah untuk `*.co.id`. Sesi yang dicuri juga bisa melakukannya tanpa sandi (SEC-59), lalu menutup pemberitahuannya.
  3. Batasan email/URI: OpenSSL tidak memperlakukan pengecualian kosong (`email:`, `URI:`) sebagai "kecualikan semua" (Go memperlakukannya demikian). Yang menahan S/MIME/code signing di klien OpenSSL adalah EKU `serverAuth` di CA (PoC: `-purpose smimesign` → error 26). Jadi pertahanan lapis kedua itu bergantung pada penegakan EKU CA oleh klien.
- **Bukti** (`web-ca.crt` buatan panel; leaf ditandatangani `web-ca.key` seperti yang bisa dilakukan panel yang dibobol):
  ```
  Name Constraints (awal): Permitted IP:192.0.2.0/24, IP:127.0.0.1/32, IP:192.0.2.2/32; Excluded DNS:"", email:"", URI:""
  other-host-in-subnet  IP:192.0.2.77     openssl: OK            go: err=<nil>
  ip-outside            IP:10.9.9.9       openssl: verification failed  go: not permitted
  dns-any               DNS:simrs.rsud.local  openssl: failed      go: excluded by constraint ""
  email                 email:a@b.c,IP:192.0.2.5  openssl: OK      go: excluded by constraint ""
  POST /settings/cert-names extra_names="rsud.local co.id 10.0.0.1" → 303 (tanpa sandi)
  panel.log: WARN "panel names outside the web CA; creating a new one"
  Name Constraints (baru): Permitted DNS:rsud.local, DNS:co.id, IP:192.0.2.0/24, …
  DNS:www.bca.co.id     openssl: OK | go err=<nil>
  DNS:simrs.rsud.local  openssl: OK | go err=<nil>
  DNS:www.google.com    openssl: failed | go: not permitted
  ```
- **Perbaikan minimal:**
  1. Batasan IP = /32 (/128) untuk alamat yang benar-benar dipakai (PublicHost, alamat antarmuka yang dipilih). Pergantian DHCP sudah ditangani alur "CA baru + pemberitahuan". Sarankan reservasi DHCP di README. Bila subnet dipertahankan, batasi ke prefiks ≥ /24 (IPv4) dan ≥ /64 (IPv6), dan kecualikan antarmuka virtual (`docker*`, `br-*`, `veth*`, `tun*`, `wg*`, `virbr*`).
  2. Nama tambahan: tolak nama yang merupakan akhiran publik atau domain organisasi. Minimal: ≥ 3 label, atau cocokkan dengan daftar akhiran publik tertanam untuk `.id`/`.com`/`.local` yang umum. Tampilkan peringatan bahwa nama itu mencakup semua subdomainnya. Minta token konfirmasi (SEC-59).
  3. Perbaiki README: CA panel berlaku untuk semua host di subnet dan semua subdomain nama yang didaftarkan.
  4. (Opsional) Simpan CA web di luar proses web: penerbitan ulang leaf lewat helper root kecil atau CLI saat `install.sh`, sehingga panel yang dibobol tidak memegang `web-ca.key`. Sebagai kompromi, terbitkan leaf 2 tahun dan jauhkan kunci CA dari proses yang berjalan (perpanjangan lewat `install.sh`/CLI).
- **Kapan:** butir 2 dan 3 sebelum merge; butir 1 sebelum rilis; butir 4 Rencana 5.

### SEC-56 — Akun dump `FLUSH_TABLES` tetap bisa menahan FTWRL; sapuan agent tidak memutus sesi
- **Tingkat:** Sedang. CVSS 3.1 `AV:N/AC:L/PR:H/UI:N/S:U/C:N/I:N/A:H` ≈ 4.9, dinaikkan karena targetnya master produksi SIMRS dan kuncinya bertahan tanpa batas waktu.
- **Dieksploitasi oleh:** panel yang dibobol (prasyarat sama dengan SEC-42).
- **Lokasi:**
  - `internal/agent/drivers/mysql/users.go:36-49` (`dumpGrants` dengan `FLUSH_TABLES`, `LOCK TABLES`);
  - `pins.go:546-553` (master yang dipin menerima `dbpanel_dump_<8 hex>` dari **IP literal mana pun**);
  - `users.go:345-399` (`dropAccount`/`SweepDumpUsers`: `DROP USER` saja, tanpa `KILL`);
  - `users.go:456-486` dan `internal/agent/monitor/monitor.go:216-250` (sapuan tiap jam, umur 24 jam, batas 1 menit).
- **Serangan dan dampak:** `RESET MASTER` kini ditolak (tujuan spesifikasi §5.10 tercapai). Namun `FLUSH_TABLES` tetap mengizinkan `FLUSH TABLES WITH READ LOCK`, dan `LOCK TABLES` tetap ada. Panel yang dibobol:
  1. membuat `dbpanel_dump_deadbeef@'<IP penyerang>'` di master yang dipin (diterima);
  2. login dan menahan FTWRL: semua penulisan SIMRS berhenti;
  3. sapuan agent setelah 24 jam tidak menolong: `DROP USER` tidak memutus sesi yang ada, dan `DROP USER` sendiri menulis ke `mysql.user` sehingga **terhalang kunci global** (1205 setelah `lock_wait_timeout`, yang bawaannya 1 tahun; sapuan berhenti di batas 1 menit). Kunci bertahan sampai sesi diputus manusia.

  Akun dump juga membaca semua data (`SELECT ON *.*`), tetapi panel yang dibobol memang bisa membaca data lewat dump ke replika miliknya. Yang baru adalah ketersediaan. Penolakan `%` dan pola host, serta penolakan mengubah akun yang ada, bekerja (PoC).
- **Bukti** (`TestAudit4aPocFlushTablesDumpUser`, agent nyata, pin `master`, mysqld 8.0.46):
  ```
  createDumpUser{dbpanel_dump_deadbee0@%}: ok=false (dipin sebagai master … ditolak)
  createDumpUser{dbpanel_dump_deadbee1@10.66.66.%}: ok=false
  createDumpUser{dbpanel_dump_deadbee2@10.66.66.66}: ok=true
  createDumpUser{dbpanel_dump_deadbeef@127.0.0.1}: ok=true
  grant: GRANT SELECT, LOCK TABLES, SHOW VIEW, EVENT, TRIGGER ON *.* …; GRANT FLUSH_TABLES ON *.* …
  RESET MASTER as dump user: Error 1227 (42000): Access denied; you need … RELOAD
  FLUSH TABLES WITH READ LOCK as dump user: err=<nil>
  while dump user holds FTWRL: root CREATE DATABASE: Error 1205 … after 2s
  agent sweep: dropped=[] err=hapus user 'dbpanel_dump_deadbee2'@'10.66.66.66': Error 1205 (HY000): Lock wait timeout exceeded
  sessions of the dropped account still open: 1
  after the sweep: root CREATE DATABASE: Error 1205 … after 2s
  ```
- **Perbaikan minimal (agent):**
  1. Sapuan: sebelum `DROP USER`, `KILL` setiap sesi `dbpanel_dump_%` yang lebih tua dari umur maksimum job (dari `performance_schema.processlist`/`threads`). Jalankan sapuan dengan `SET SESSION lock_wait_timeout = 5`.
  2. Pagar langsung: monitor agent master membunuh sesi `dbpanel_dump_%` yang memegang `FLUSH TABLES WITH READ LOCK` lebih dari N detik (mysqldump `--single-transaction` hanya memegangnya sesaat). Sumbernya `performance_schema.metadata_locks` dengan `OBJECT_TYPE='GLOBAL'`, atau `processlist` dengan state `Waiting for global read lock` pada sesi lain.
  3. `CREATE USER … WITH MAX_USER_CONNECTIONS 2` dan `PASSWORD EXPIRE INTERVAL 1 DAY` untuk akun dump. Umur sapuan diturunkan ke ≤ 6 jam.
  4. (Rencana 5, SEC-42 butir 4 asli) dump lewat kanal agent tanpa akun jaringan.
  5. README: `FLUSH_TABLES` tetap mengizinkan FTWRL. Sebutkan bahwa pagar terhadap panel yang dibobol hanya butir 1–2.
- **Kapan:** butir 1–3 sebelum merge (murah, lokal di agent); butir 4 Rencana 5.

### SEC-57 — HSTS tidak berlaku untuk alamat IP; akses lewat `http://` tetap bisa di-strip
- **Tingkat:** Sedang. CVSS 3.1 `AV:A/AC:H/PR:N/UI:R/S:U/C:H/I:H/A:N` ≈ 6.4.
- **Dieksploitasi oleh:** penyerang on-path di LAN.
- **Lokasi:** `internal/panel/web/router.go:266-268` (HSTS hanya di TLS, benar), `internal/panel/tlsui.go:780-791` (301 ke `https://` lewat HTTP polos), `internal/panel/panel.go` (`PublicHost` bawaan = IP non-loopback pertama), `scripts/install.sh:461-462`, README baris 806-808 ("browser yang pernah membuka panel lewat HTTPS tidak lagi mencoba HTTP"), spesifikasi §5.11 ("Bookmark lama tetap sampai ke panel").
- **Serangan dan dampak:** RFC 6797 §8.1 melarang browser mencatat HSTS untuk host berupa IP. Alamat bawaan panel adalah IP, jadi HSTS tidak pernah aktif di instalasi standar. Setiap kali operator membuka bookmark `http://10.x.x.x:8080` (semua bookmark sebelum pembaruan) atau mengetik `10.x.x.x:8080`, request pertama lewat HTTP polos. Penyerang on-path menjawab request itu dengan halaman login palsu lewat HTTP (atau mem-proxy panel lewat HTTP). Browser hanya menampilkan "Not secure", yang sama dengan pengalaman pengguna sebelum Rencana 4a. Sandi admin bocor, dan sandi itu juga lolos token konfirmasi. Cookie `__Host-…; Secure` tidak bocor, jadi kerugiannya "hanya" sandi. Ini berbeda dari SEC-65: pengguna yang **sudah** memasang CA tetap terkena selama masuk lewat `http://`.
- **Bukti:**
  ```
  $ curl -s -D - http://127.0.0.1:18080/servers?x=1
  HTTP/1.1 301 Moved Permanently
  Location: https://127.0.0.1:18080/servers?x=1      (tanpa Strict-Transport-Security; HTTPS memasangnya: max-age=31536000)
  ```
  Perilaku browser untuk HSTS di host IP tidak diuji di browser nyata (§8.4). Dasarnya norma RFC 6797 §8.1.
- **Perbaikan minimal:**
  1. README dan pesan `install.sh`: perbarui bookmark ke `https://`; HSTS hanya bekerja dengan nama DNS. Sarankan nama DNS (`--public-host panel.rsud.local`) agar HSTS dan sertifikat berlaku.
  2. Halaman login dan dasbor: bila `Referer` atau navigasi berasal dari `http://`, tampilkan sekali "simpan alamat https:// ini sebagai bookmark".
  3. Pertimbangkan mode "HTTP mati" di Pengaturan (helper hanya menjawab `/sertifikat`, jalur lain mendapat teks statis "buka https://…" tanpa tautan yang bisa disalahgunakan), untuk organisasi yang sudah memasang CA lewat GPO.
- **Kapan:** README dan pesan sebelum merge; butir 2–3 sebelum rilis.

### SEC-58 — TOCTOU symlink pada `<datadir>/dbpanel-tls` (direktori milik `mysql`)
- **Tingkat:** Rendah. CVSS 3.1 `AV:L/AC:H/PR:L/UI:N/S:C/C:N/I:L/A:L` ≈ 3.9. Prasyaratnya eksekusi kode sebagai OS user `mysql` (mysqld dibobol, plugin atau UDF). Dampaknya penulisan nama tetap dan `chmod` sebagai root di luar datadir.
- **Lokasi:**
  - `internal/agent/drivers/mysql/tls.go:260-287` (`tlsDir`: `Mkdir` → `Lstat` → `os.Chmod(dir)` mengikuti symlink → `Lchown`);
  - `tls.go:289-318` (`writeTLSFile`: `CreateTemp(dir, …)` lewat path, dipanggil **setelah** `tlsDir` kembali);
  - `tls.go:413-448` (`rotation.swap/undo`: `Rename` `server.crt`/`server.key`/`mysql-ca.crt` ↔ `.prev`/`.new` lewat path);
  - `tls.go:354-364,466-505` (`trustTLS`, `installTLS`).
  - Direktori induk (datadir) milik `mysql`, jadi pemiliknya bisa mengganti entri `dbpanel-tls` kapan saja (dicatat ledger Task 6 sebagai risiko, tanpa ruling).
- **Serangan dan dampak:** pemilik datadir menukar `dbpanel-tls` dengan symlink secara atomik (`renameat2(RENAME_EXCHANGE)`) saat agent root menjalankan `mysql.tlsRequest`/`tlsTrust`/`tlsInstall`, yang semuanya dipicu panel (job harian perpanjangan juga). Akibatnya:
  - root `chmod 0700` target symlink (direktori atau berkas mana pun: `/usr/bin/sudo` kehilangan setuid, `/etc` rusak);
  - root menulis `server.key.new`, `server.crt`, `mysql-ca.crt` (milik `mysql`, 0600) ke direktori mana pun;
  - pada `tlsInstall`, berkas `server.crt`/`server.key` yang sudah ada di direktori itu (mis. milik layanan web lain) di-rename ke `.prev` dan diganti berkas milik `mysql`.

  Tidak ada jalur eksekusi kode langsung yang ditemukan (nama berkas tetap, isi dari panel/agent), tetapi integritas berkas root dan ketersediaan layanan lain terdampak.
- **Bukti** (`TestAudit4aPocTLSDirSymlinkRace`, `flipper` sebagai `nobody`, pemilik datadir harness):
  ```
  run 1: iteration 0: root chmod followed the link: /tmp/dbpanel-audit4a/victim mode now -rwx------ (was 0755)
  run 2: iteration 0: root wrote mysql-ca.crt into /tmp/dbpanel-audit4a/victim (mode -rw-------, owner uid 65534)
         tlsTrust calls: ok=1 refused=0; chmod through link=0, files written through link=1
  ```
  (Versi awal dengan dua `rename` biasa: 0 dari 3.000 percobaan, 3 ditolak `Lstat`; dengan pertukaran atomik, berhasil pada percobaan pertama.)
- **Perbaikan minimal:** buka `datadir` sekali dengan `os.OpenRoot` (Go ≥ 1.24) dan lakukan `Mkdir`/`OpenFile`/`Rename`/`Chmod` lewat `*os.Root` (tidak bisa keluar lewat symlink). Alternatif: buka `dbpanel-tls` dengan `O_DIRECTORY|O_NOFOLLOW`, `fstat` + periksa pemilik, lalu `fchmod`/`fchown` dan `*at` relatif ke fd itu. Jangan pernah `os.Chmod(path)` pada path di direktori milik user lain.
- **Kapan:** sebelum merge (perubahan lokal di satu berkas).

### SEC-59 — Pengaturan sertifikat HTTPS tanpa konfirmasi sandi; validasi unggahan lemah
- **Tingkat:** Rendah. CVSS 3.1 `AV:A/AC:H/PR:H/UI:R/S:U/C:L/I:H/A:N` ≈ 4.4.
- **Dieksploitasi oleh:** sesi admin yang dicuri (lebih sulit sejak HTTPS, lihat SEC-57) atau admin yang keliru.
- **Lokasi:** `internal/panel/web/router.go:224-228` (`/settings/cert`, `/settings/cert-panel`, `/settings/cert-names`, `/settings/cert-notice-close` hanya `authedPost`), `handlers_cert.go:94-174`, `internal/panel/tlsui.go:504-530` (`validateCustomCert`: kunci cocok, belum kedaluwarsa, `VerifyHostname(publicHost)`; tanpa cek `IsCA`, EKU, ukuran kunci, rantai, atau `NotBefore`).
- **Serangan dan dampak:**
  - (a) Sesi curian mengunggah sertifikat milik penyerang (self-signed sudah cukup). Panel langsung menyajikannya. Pin di perintah pasang agent dan di `local-pair-token` ikut berganti ke kunci penyerang, sehingga penyerang on-path bisa menjawab pairing berikutnya, mengambil token, dan mendaftarkan agent palsu untuk server baru itu. Browser yang sudah memasang CA panel kini memperingatkan, dan pengguna terbiasa mengklik lanjut.
  - (b) Nama tambahan memicu CA baru (SEC-55) dan pemberitahuan bisa ditutup oleh sesi yang sama.
  - (c) Admin yang keliru mengunggah sertifikat CA kantor beserta kuncinya. Bila SAN-nya kebetulan memuat alamat panel, panel menyimpan kunci CA kantor di proses web.
- **Bukti:**
  ```
  rsa1024.crt: Public-Key: (1024 bit), CA:TRUE, Extended Key Usage: Code Signing, self-signed, SAN IP:127.0.0.1
  POST /settings/cert (tanpa sandi) → 303 /settings?pesan=sertifikat-sendiri
  openssl s_client → subject=CN = attacker, CA:TRUE, Code Signing   (disajikan sebagai sertifikat HTTPS panel)
  POST /settings/cert-panel → 303; subject=CN = 127.0.0.1
  ```
- **Perbaikan minimal:** token konfirmasi (`requireConfirm`) untuk unggah, kembali ke sertifikat panel, nama tambahan, dan tutup pemberitahuan CA. `validateCustomCert` menolak `IsCA`, EKU tanpa `serverAuth` (bila ada EKU), RSA < 2048, kurva < P-256, dan `NotBefore` di masa depan. Peringatan bila sertifikat tidak bisa diverifikasi dengan penyimpanan sistem (self-signed). Kirim peringatan Telegram "sertifikat HTTPS panel diganti" (Rencana 4c).
- **Kapan:** token sebelum merge; validasi sebelum rilis.

### SEC-60 — Token konfirmasi: celah kecil di sekitar mekanisme yang sehat
- **Tingkat:** Rendah. CVSS 3.1 `AV:N/AC:H/PR:H/UI:N/S:U/C:L/I:L/A:N` ≈ 3.1.
- **Lokasi:** `internal/panel/web/handlers_confirm.go:71-115` (`issueConfirm`), `:126-147` (`requireConfirm`), `internal/panel/panel.go` (`confirmMaxAttempts=5`, `confirmWindow=1m`, limiter terpisah dari `Limiter`/`UserLimiter` login).
- **Masalah:**
  1. **Orakel sandi.** Sesi curian bisa menebak sandi lewat `/confirm` dengan laju 5/menit (300/jam, tanpa batas harian). Limiter login per username adalah 20/15 menit (80/jam). Kegagalan `/confirm` tidak dihitung ke limiter login dan tidak memicu penguncian. Sandi yang ditebak lewat sini membuka semua aksi destruktif.
  2. **Penolakan token tidak diaudit.** Token yang salah target, kedaluwarsa, atau dipakai ulang hanya menulis `Warn` di journal. Ruling ledger Task 2 ("rejected token should write audit `confirm_rejected`") belum diterapkan. Ruling "limiter hanya menghitung sandi salah" juga belum diterapkan: konfirmasi yang sah ikut dihitung.
  3. **Terikat ke username, bukan sesi.** Di rumah sakit, satu akun `admin` sering dipakai bersama. Token dari sesi A bisa dipakai sesi B (PoC). Bocornya token sulit (respons JSON same-origin, tidak dicatat), jadi ini catatan.
  4. Token diterbitkan untuk target yang tidak ada (`target_id=999` → 200) dan untuk jenis target yang tidak sesuai aksinya. Tidak berbahaya, tetapi menambah baris audit `confirm_issued` yang menyesatkan.
- **Bukti (binary):**
  ```
  /confirm sandi salah ×5 → 401; ke-6 → 429 "Terlalu banyak percobaan konfirmasi"
  token server 2 dipakai di server 3 → 403; token yang sama di server 2 → 303 (tidak terbakar oleh salah target)
  token sesi 1 dipakai sesi 2 (user sama) → 303 dihapus
  token dipakai (server lokal, 409), dipakai ulang → 403
  token setelah 121 dtk → 403;  Origin asing → 403;  aksi tak dikenal → 400;  target 999 → 200 + token
  audit_log: confirm_failed ×5, confirm_failed rate_limited, confirm_issued …; tidak ada baris untuk 403 replay/salah target/kedaluwarsa
  ```
- **Perbaikan minimal:** kegagalan `/confirm` juga dihitung ke `UserLimiter` login (atau satu anggaran bersama), dan limiter hanya menghitung sandi salah. Audit `confirm_rejected` di `requireConfirm`. Tolak penerbitan untuk target yang tidak ada dan jenis yang tidak cocok dengan aksi. Pertimbangkan mengikat token ke hash token sesi.
- **Kapan:** butir audit dan limiter sebelum merge (sudah ruling); sisanya Rencana 4c.

### SEC-61 — Sisa batas root vs `dbpanel` di `install.sh` dan unit systemd
- **Tingkat:** Rendah. Semua jalur di bawah ini butuh panel yang dibobol dan kondisi tambahan.
- **Lokasi:** `scripts/install.sh:110-119` (`as_panel`/`panel_read`: `runuser -u dbpanel` mewarisi TTY root), `:350-372,453-471` (dipanggil **saat panel berjalan**: token lokal dan sandi awal), `:356-357,430-431` (`[ -s "$PANEL_TOKEN_FILE" ]`/`[ -s "$PASSWORD_FILE" ]` sebagai root mengikuti symlink), `:465` (sandi dicetak mentah ke terminal), `scripts/systemd/dbpanel-panel.service`.
- **Masalah:**
  1. **Kelas TIOCSTI (CVE-2016-2779 untuk `runuser`).** Proses `timeout`/`sh`/`head` yang dijalankan `runuser` sebagai `dbpanel` memegang TTY root dan berada di sesi yang sama. Setelah `execve` biner non-setuid, prosesnya *dumpable* dan bisa di-ptrace oleh proses `dbpanel` lain. Panel yang dibobol, yang berjalan saat `hand_local_token` dan `print_summary`, bisa mencoba menempel dan menyuntikkan `ioctl(TIOCSTI)` ke TTY root. Syaratnya `dev.tty.legacy_tiocsti=1` (RHEL 8/9, kernel < 6.2; lingkungan audit: `1`), `kernel.yama.ptrace_scope=0` (Debian/RHEL bawaan), sudo tanpa `use_pty`, dan operator di shell root. Tidak ada PoC end-to-end (balapan dengan proses berumur milidetik).
  2. Unit panel tidak memasang `SystemCallFilter=@system-service` (yang menutup `ptrace`), `ProtectProc=invisible`, `ProcSubset=pid`, `PrivateDevices`, `ProtectKernelLogs`, `RestrictAddressFamilies`, `RestrictNamespaces`, `MemoryDenyWriteExecute`, atau `CapabilityBoundingSet=`. Rekomendasi SEC-06 Rencana 1 hanya diterapkan sebagian. `systemd-analyze security --offline`: **7.1 MEDIUM**.
  3. Sandi awal (≤ 4.096 byte, isi dari panel) dicetak tanpa penyaringan: urutan escape terminal (ledger Task 8, ditunda).
  4. `[ -s FILE ]` sebagai root mengikuti symlink milik panel: hanya orakel keberadaan/ukuran, tidak ada isi yang terbaca.
- **Perbaikan minimal:** `as_panel` menjalankan `runuser … </dev/null 2>/dev/null` (atau `setsid -w`), sehingga anak proses tidak memegang TTY. `print_summary` menyaring dengan `tr -cd '[:print:]'`. Unit menambah `SystemCallFilter=@system-service`, `SystemCallErrorNumber=EPERM`, `ProtectProc=invisible`, `ProcSubset=pid`, `PrivateDevices=yes`, `ProtectKernelLogs=yes`, `RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX`, `RestrictNamespaces=yes`, `MemoryDenyWriteExecute=yes`, `CapabilityBoundingSet=`, `ProtectClock=yes`, `ProtectHostname=yes`. Perbarui `install_test.sh` (perbandingan unit).
- **Kapan:** sebelum rilis (murah).

### SEC-62 — Agent sebelum Rencana 4a tidak mendapat pagar baru; panel tidak menuntut pembaruan
- **Tingkat:** Rendah, dengan catatan: naik ke **Sedang** saat Rencana 4b (promote) bergantung pada pagar agent.
- **Lokasi:** `internal/agent/drivers/mysql/{users,pins,replication}.go` (pagar baru hanya di binary baru); `internal/panel/web/handlers_servers.go:189-198` (`NeedsUpgrade` hanya dari kemampuan pemantauan); README "Pembaruan" (perbarui panel dulu, lalu agent); ledger Task 9 (agent lama gagal di langkah sertifikat dengan "perintah tidak dikenal: mysql.tlsRequest").
- **Masalah:** agent master lama tetap membuat akun dump ber-`RELOAD` (`RESET MASTER` kembali mungkin, SEC-42 utuh), tidak menyapu akun dump sendiri, dan `releaseRole` di replika lama tetap menghapus pin sumber (SEC-43 utuh). Panel tidak tahu versi agent selain dari kemampuan pemantauan, dan tidak menolak job di agent lama. Verifikasi TLS gagal tertutup dengan benar (`mysql.tlsTrust` dipanggil sebelum `configureReplication`/dump/CLONE dengan verifikasi, sehingga agent lama gagal, bukan diam-diam tidak memverifikasi), tetapi "Siapkan master"/"Buat replika" gagal dengan pesan teknis.
- **Perbaikan minimal:** agent melaporkan versi protokol saat terhubung (sudah ada `AgentVersion` saat pairing; laporkan juga di koneksi mTLS). Panel menandai "agent perlu diperbarui untuk pagar keamanan" di daftar server dan **menolak** job yang mengandalkan pagar baru (Rencana 4b: semua promote) sampai agent diperbarui. Pesan yang jelas untuk `perintah tidak dikenal`.
- **Kapan:** penanda sebelum rilis; penolakan sebelum Rencana 4b.

### SEC-63 — Catatan robustness dan PKI (Info)
- a. **CLONE hanya memverifikasi rantai** (`clone.go:174-185`, keterbatasan MySQL, tercatat di README). Root di server terkelola **mana pun** memegang kunci sertifikat dari CA MySQL yang sama, jadi bila ia on-path, ia bisa menyamar sebagai donor CLONE untuk server lain. Replikasi dan dump memverifikasi nama.
- b. **`tlsInstall` mengganti `ssl_ca`/`ssl_cert`/`ssl_key` server (PERSIST).** TLS yang dikonfigurasi operator sendiri (CA rumah sakit untuk klien SIMRS dengan `VERIFY_CA`, akun `REQUIRE X509`) diganti tanpa peringatan. Panel yang dibobol bisa memasang CA-nya sendiri sebagai `ssl_ca` master. Saran: periksa `ssl_ca`/`ssl_cert` yang bukan bawaan MySQL dan minta persetujuan eksplisit di dialog konfirmasi.
- c. Serial hasil `tlsInstall` tidak dicocokkan dengan yang diterbitkan, dan `replicas.tls_verified=1` diset sebelum IO thread benar-benar tersambung (ledger Task 7). Agent jahat bisa membuat `server_tls` berbohong, tetapi akibatnya gagal tertutup (replika menolak).
- d. OpenSSL mengabaikan pengecualian kosong `email:`/`URI:` (SEC-55 butir 3). EKU `serverAuth` di CA menahannya.
- e. `web-ca.key.old` disimpan tanpa batas setelah CA diganti. Itu kunci CA yang mungkin masih dipercaya browser lama. Hapus setelah N hari, atau saat pemberitahuan ditutup.
- f. `hostAllowed` menerima nama apa pun yang dicakup sertifikat unggahan, termasuk wildcard (`*.rsud.go.id`). Ini sedikit melonggarkan pertahanan DNS rebinding untuk subdomain yang dikuasai penyerang.
- g. Penjaga lama "token untuk server panel tidak boleh dipakai host lain" dihapus (`agentapi/pair.go`). Token lokal kini bisa dipakai dari IP mana pun, dan alamat dipaksa `PublicHost`. Paparannya kecil (berkas 0600 milik `dbpanel`/root, 15 menit), tetapi pairing lokal bisa dibatasi ke `RemoteAddr` loopback.
- h. Host tanpa port pada HTTP pembantu → `Location: https://localhost/x` (port 443). Hanya fungsional. Pengalihan dari perintah pasang lama `http://…/agent/install?token=…` memantulkan token di `Location` (token sudah lewat HTTP polos, dan `curl --max-redirs 0` berhenti).
- i. Sapuan dump sinkron di loop monitor bisa menunda laporan kesehatan ≤ 1 menit per jam saat terhalang (SEC-56).
- j. `TestE2EHTTPS` memakai `sudo` tiruan dengan daftar tolak (ledger Task 9, Important yang ditunda): perubahan template di masa depan bisa menulis path sistem nyata saat test berjalan sebagai root. Ini masalah test, bukan produk.

### SEC-64 — Panel yang dibobol tetap pusat kepercayaan (diterima, inheren)
- **Ruling:** spesifikasi §5.8/§5.11 (panel menerbitkan sertifikat agent, MySQL, dan web, serta menyajikan skrip dan binary agent); SEC-52 tertutup oleh pemisahan agent lokal.
- **Risiko residual:** proses `dbpanel` membaca `ca.key`, `mysql-ca.key`, `web-ca.key`, dan `panel.key`, mengendalikan `/agent/install` dan `/agent/bin/*` (hash di skrip dan perintah pasang juga buatannya), serta mengganti `bin/` (direktori milik root di dalam direktori miliknya bisa di-rename). Setiap server yang dipasang atau diperbarui dengan perintah dari panel yang dibobol menjadi milik penyerang (root). Pagar agent (pin) membatasi apa yang bisa dilakukan ke server yang **sudah** berpasangan.
- **Mitigasi:** rilis bertanda tangan dan verifikasi tanda tangan di skrip agent (SEC dari Rencana 1, belum dijadwalkan); SEC-55 butir 4; audit dan peringatan Telegram untuk pembuatan token pasangan (Rencana 4c).

### SEC-65 — Pengguna yang tidak memasang sertifikat (diterima)
- **Ruling:** spesifikasi §8 butir terakhir.
- **Risiko residual:** peringatan browser terus muncul. Penyamaran di LAN tidak terdeteksi oleh pengguna itu: sandi dan sesi bisa dicuri penyerang on-path yang menyajikan sertifikatnya sendiri.
- **Mitigasi:** SEC-54 (pemasangan yang aman) dan SEC-57 (DNS + HSTS); dorong penyebaran lewat GPO dengan sidik jari yang dicocokkan.

---

### 4A. Yang diperiksa dan dinilai aman

- **Token konfirmasi** (`store/confirmations.go`, `web/handlers_confirm.go`, `_confirm.html`, `app.js`):
  - Token acak 32 byte (`crypto/rand`), hanya SHA-256-nya yang disimpan.
  - Konsumsi atomik dengan satu `UPDATE … WHERE token_hash, username, action, target_kind, target_id, used_at IS NULL, expires_at > now`. Format waktu RFC3339 UTC tanpa pecahan, jadi perbandingan string aman.
  - Salah target tidak membakar token. Replay → 403, kedaluwarsa 2 menit → 403 (PoC 121 detik), Origin asing → 403.
  - Token dan sandi tidak dicatat atau diaudit. Modal memakai `x-text` (tanpa `innerHTML`).
  - `requireConfirm` dipanggil sebelum pemeriksaan lain di 8 handler: resync, detach, hapus server, lepas database, buka kunci master, siapkan master ulang (bila topologi ada), pasang sertifikat MySQL, perketat.
- **Listener terbelah** (`tlsui.go:633-770`):
  - Byte pertama dibaca per goroutine dengan batas 5 detik. Koneksi terikat ke satu server sejak byte pertama, jadi tidak ada smuggling lintas server (byte TLS setelah request HTTP di koneksi yang sama tetap dilayani helper). CL+TE ditangani `net/http`.
  - Helper: `ReadHeaderTimeout`/`ReadTimeout`/`IdleTimeout` 5 detik. Dari 1.500 koneksi diam dan 300 header setengah jadi, semuanya ditutup ≤ 6,5 detik, dan request sah dijawab 30 ms (HTTP) dan 70 ms (HTTPS) di bawah beban.
  - Helper hanya melayani GET/HEAD `/sertifikat`, unduhannya, dan `/static/*`. `/static/../servers` → 307 dari `ServeMux` (tidak dilayani), `/sertifikat/../servers` → 301 ke HTTPS.
  - `RequireHost` pada kedua server: Host asing → 421, Host dengan `@` atau `/` → 400. Pengalihan memakai Host yang sudah divalidasi, jadi tidak ada open redirect.
  - Close menutup koneksi yang masih diintip.
- **TLS UI:**
  - TLS ≥ 1.2 (`openssl s_client`: TLS 1.3 `TLS_AES_128_GCM_SHA256`, TLS 1.2 `ECDHE-ECDSA-AES128-GCM-SHA256`), leaf ECDSA P-256.
  - `GetCertificate` per handshake, sehingga pembaruan tanpa restart.
  - Tidak ada fallback ke HTTP polos. Sertifikat unggahan yang rusak atau kedaluwarsa → kembali ke sertifikat panel (dengan log).
  - Kunci leaf dipertahankan saat diterbitkan ulang (pin tetap), dan ditulis atomik 0600.
- **Cookie dan sesi:** `__Host-dbpanel_session; Path=/; HttpOnly; Secure; SameSite=Lax` (dicek). Cookie HTTP lama tidak dibaca lagi dan dihapus saat logout. HSTS hanya pada TLS.
- **Pin pairing:**
  - `pairCommand`, `RenderInstallScript`, dan skrip pasang menolak pin kosong. `curl -k --pinnedpubkey` dengan pin salah → rc=90 sebelum request terkirim (dicek).
  - Agent `pinnedTransport` memeriksa SPKI di `VerifyPeerCertificate` sebelum token terkirim, mewajibkan `https://` bila pin dipakai, dan tetap menolak pengalihan.
  - `--ca-sha256` (CA agent) tetap dipin. Format pin divalidasi (`sha256//` + 32 byte).
  - Pin dibuat dari kunci yang aktif saat ditampilkan.
- **CA web:**
  - Name constraints kritis, `pathlen:0`, EKU `serverAuth`, DNS dikecualikan bila tidak ada nama.
  - `validHost` menolak label tunggal, nama kosong, wildcard, dan non-ASCII.
  - IP atau DNS di luar batasan ditolak Go dan OpenSSL (PoC; luasnya batasan adalah SEC-55).
  - CA diganti hanya bila tidak terbaca atau tidak mencakup nama. Galat I/O menghentikan start.
- **CA MySQL dan `mysql.tls*`:**
  - CA terpisah dari CA agent. SAN hanya dari `servers.address` dan `source_host` (bukan dari agent), CN tetap, dan tanda tangan CSR diverifikasi (`ParseAgentCSR`).
  - Agent: satu blok PEM ≤ 64 KiB, CA harus `IsCA`, leaf bukan CA, ECDSA, ditandatangani CA itu, kunci cocok dengan `server.key.new` yang dibuat agent (kunci tidak pernah keluar), dan `VerifyHost` lolos `sourceHostRe`.
  - `GLOBAL` → `RELOAD TLS` → cek koneksi baru `VERIFY_IDENTITY` → `PERSIST`. Bila gagal, berkas dan variabel dikembalikan.
  - `CONNECTION_ADMIN` dan `FLUSH_TABLES … WITH GRANT OPTION` ada di preflight.
- **Verifikasi replikasi, dump, dan CLONE:**
  - `validateVerify`: path absolut bersih, tanpa kutip, backslash, atau karakter kontrol, ≤ 511.
  - `SOURCE_SSL_CA`/`VERIFY_SERVER_CERT` selalu diset (dikosongkan bila tanpa verifikasi). Dump memakai `--ssl-mode=VERIFY_IDENTITY --ssl-ca` di argumen dan berkas opsi, dan CLONE memakai `REQUIRE SSL` + `clone_ssl_ca`.
  - Orkestrator memanggil `mysql.tlsTrust` sebelum perintah yang memverifikasi, sehingga agent lama gagal tertutup.
  - Topologi lama mendapat peringatan sampai "Perketat".
- **Pin peran/sumber:** `Release` hanya menulis `role=unassigned`, dan pin sumber tetap (test + kode). `SetRole unassigned` (penghapus pin sumber) hanya dari CLI lokal. `configureReplication`/`dumpFrom`/`cloneFrom` ke host lain setelah lepas → ditolak dengan perintah `set-source`.
- **Akun dump di master yang dipin:** host `%`/pola ditolak, akun yang sudah ada tidak diubah (PoC). `RESET MASTER` → 1227 (PoC). Fallback `RELOAD` hanya untuk < 8.0.31 atau admin tanpa hak memberi, dengan peringatan di log job.
- **`install.sh`** (harness `install_test.sh` lulus):
  - Root tidak pernah membaca, `chown`, atau `chmod` lewat path di direktori panel. Pembacaan lewat `runuser` dengan `[ -f ]`, `head -c` (4 KiB untuk token/sandi, 64 MiB + 1 untuk binary), dan `timeout 60`.
  - `bin/` dibangun di direktori root lalu `mv -T`, dan `bin/` yang berupa link atau berkas → berhenti.
  - Token disalin ke `mktemp` 0600 di `/etc/dbpanel`. `prepare_dirs` memeriksa symlink dan memakai `chown -h`.
  - `move_old` hanya `chown -R -h` sumber di direktori root (entrinya tidak bisa dibuat `dbpanel`). `fs.protected_hardlinks=1` mencegah hardlink ke berkas root.
  - Panel dihentikan sebelum pemindahan, dan cgroup mengakhiri semua proses `dbpanel`.
- **Panel non-root:** `dbpanel panel` sebagai root tanpa `--allow-root` → keluar 1 (dicek). `reset-local-agent` menolak root. Unit `User=dbpanel`, `ProtectSystem=strict`, `ReadWritePaths` dua direktori panel, `UMask=0077`, `NoNewPrivileges`.
- **Token pasangan lokal:** 0600, 15 menit, dihapus setelah pairing. Format token+pin divalidasi agent. Pairing ke `127.0.0.1` dengan pin, jadi port lokal yang diambil proses lain tidak bisa menyamar.
- **Halaman `/sertifikat`:** tanpa login dan tanpa data sesi (`Page{Version}` saja), header lengkap juga lewat HTTP, unduhan `application/x-x509-ca-cert` dengan `attachment`.
- **Rahasia:** kunci unggahan tidak pernah ditampilkan atau dicatat. Audit `https_cert_upload` mencatat nama saja.

### 4B. Security headers (dicek dengan `curl -D -` ke binary go1.26.10 yang berjalan, setelah login)

Baseline sama dengan Rencana 3: CSP `default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; connect-src 'self'; object-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'`, `nosniff`, `X-Frame-Options: DENY`, `Referrer-Policy: same-origin`, `Permissions-Policy`, COOP/CORP `same-origin`, `Cache-Control: no-store`, tanpa header `Server`. Baru: `Strict-Transport-Security: max-age=31536000` pada setiap jawaban HTTPS (tidak pada HTTP).

| Kelas respons | Status | Content-Type | CSP | nosniff | XFO | Referrer | Permissions | COOP/CORP | Cache-Control | HSTS |
|---|---|---|---|---|---|---|---|---|---|---|
| HTTPS `GET /login` | 200 | text/html | baseline | ada | DENY | same-origin | ada | ada/ada | no-store | max-age=31536000 |
| HTTPS `GET /` (sesi) | 200 | text/html | baseline | ada | DENY | same-origin | ada | ada/ada | no-store | max-age=31536000 |
| HTTPS `GET /settings` | 200 | text/html | baseline | ada | DENY | same-origin | ada | ada/ada | no-store | max-age=31536000 |
| HTTPS `GET /servers/1` | 200 | text/html | baseline | ada | DENY | same-origin | ada | ada/ada | no-store | max-age=31536000 |
| HTTPS `GET /sertifikat` | 200 | text/html | baseline | ada | DENY | same-origin | ada | ada/ada | no-store | max-age=31536000 |
| HTTPS `GET /sertifikat/dbpanel-panel.crt` | 200 | application/x-x509-ca-cert | baseline | ada | DENY | same-origin | ada | ada/ada | no-store | max-age=31536000 |
| HTTPS `GET /static/app.js` | 200 | text/javascript | baseline | ada | DENY | same-origin | ada | ada/ada | public, max-age=3600 | max-age=31536000 |
| HTTPS `POST /confirm` sandi salah | 401 | application/json | baseline | ada | DENY | same-origin | ada | ada/ada | no-store | max-age=31536000 |
| HTTPS `POST /confirm` Origin asing | 403 | text/plain | baseline | ada | DENY | same-origin | ada | ada/ada | no-store | max-age=31536000 |
| HTTPS `GET /confirm` | 405 | text/plain | baseline | ada | DENY | same-origin | ada | ada/ada | no-store | max-age=31536000 |
| HTTPS Host asing | 421 | text/plain | baseline | ada | DENY | same-origin | ada | ada/ada | no-store | max-age=31536000 |
| HTTPS `GET /agent/install?token=…` (token salah) | 403 | text/plain | baseline | ada | DENY | same-origin | ada | ada/ada | no-store | max-age=31536000 |
| HTTPS `GET /servers` tanpa sesi | 303 → `/login` | text/html | baseline | ada | DENY | same-origin | ada | ada/ada | no-store | max-age=31536000 |
| HTTP `GET /sertifikat` | 200 | text/html | baseline | ada | DENY | same-origin | ada | ada/ada | no-store | — |
| HTTP `GET /sertifikat/dbpanel-panel.crt` | 200 | application/x-x509-ca-cert | baseline | ada | DENY | same-origin | ada | ada/ada | no-store | — |
| HTTP `GET /static/app.js` | 200 | text/javascript | baseline | ada | DENY | same-origin | ada | ada/ada | public, max-age=3600 | — |
| HTTP `GET /login`, `POST /login` | 301 → https | text/html / — | baseline | ada | DENY | same-origin | ada | ada/ada | no-store | — |
| HTTP Host asing | 421 | text/plain | baseline | ada | DENY | same-origin | ada | ada/ada | no-store | — |

**Putusan 4B: lulus.** Cookie: `__Host-dbpanel_session=…; Path=/; HttpOnly; Secure; SameSite=Lax`. Template baru (`cert.html`, `_confirm.html`) tanpa `<script>` inline, atribut `on*`, `style=`, atau `javascript:`. Catatan:
- HSTS tanpa `includeSubDomains` (wajar untuk IP) dan tidak berlaku untuk host IP (SEC-57).
- `/sertifikat` lewat HTTP membawa header lengkap, tetapi isi dan berkasnya tidak terlindungi (SEC-54).
- TLS 1.0/1.1 tidak bisa diuji dengan klien OpenSSL di lingkungan audit ("no protocols available" di sisi klien). `MinVersion: tls.VersionTLS12` di kode.

## 5. Status temuan terjadwal (yang seharusnya ditutup Rencana 4a)

| ID | Status di `d785a4c` | Bukti / sisa |
|---|---|---|
| **SEC-06** (panel non-root) | **Ditutup, dengan sisa Rendah** | Unit `User=dbpanel`, `ProtectSystem=strict`, `ReadWritePaths` dua direktori; CLI menolak root (dicek); agent tertanam dihapus, agent lokal root terpisah; `/etc/dbpanel` dan `/var/lib/dbpanel` 0711 root; `install.sh` tidak mengikuti link milik panel (harness lulus, perbaikan Critical/Important Task 8 terverifikasi). **Sisa:** hardening unit sebagian, kelas TIOCSTI pada `runuser` (SEC-61); panel tetap memegang semua CA dan distribusi agent (SEC-64, inheren). |
| **SEC-52** (pin agent tertanam di direktori panel) | **Ditutup** | Pin host panel kini `/etc/dbpanel/{role,source}` milik root, dijaga agent lokal yang terpisah. |
| **SEC-23** (UI dan pairing lewat HTTP polos) | **Diterapkan, dengan celah bootstrap** | HTTPS satu port, cookie `__Host-` `Secure`, HSTS, `/agent/*` lewat HTTPS dengan pin SPKI (rc=90 pada pin salah). **Celah:** CA root dipasang lewat HTTP tanpa sidik jari (SEC-54, Tinggi); HSTS tidak berlaku untuk IP (SEC-57); pengguna tanpa sertifikat (SEC-65, diterima). |
| **SEC-28 butir 5** (token konfirmasi per perintah) | **Diterapkan** | 8 aksi wajib token sesuai §5.7 untuk cakupan 4a (promote/salvage di 4b). PoC: binding, replay, kedaluwarsa, CSRF, limiter. **Sisa:** orakel sandi, audit penolakan, ruling limiter (SEC-60); pengaturan sertifikat HTTPS di luar daftar (SEC-59). Semafor/satu job per server tetap lewat `startMu` + `refuseBusy`. |
| **SEC-29 butir 2 / SEC-41** (verifikasi CA MySQL) | **Diterapkan** | CA MySQL terpisah; replikasi `SOURCE_SSL_VERIFY_SERVER_CERT=1` (nama), dump `VERIFY_IDENTITY`, CLONE `clone_ssl_ca` + `REQUIRE SSL`; `tls_verified` per replika; "Perketat sambungan replikasi" untuk topologi lama; agent lama gagal tertutup. **Sisa:** CLONE hanya rantai (SEC-63a), `ssl_ca` server diganti (SEC-63b), TOCTOU berkas (SEC-58); panel yang dibobol bisa memilih tanpa verifikasi (inheren, SEC-64). |
| **SEC-42 butir 2** (sapuan akun dump oleh agent) | **Diterapkan, tidak efektif terhadap kunci yang dipegang** | Monitor agent tiap jam, > 24 jam, melewati server dengan sumber replikasi. **Celah:** tidak `KILL` sesi, `DROP USER` gagal 1205 saat FTWRL dipegang (SEC-56); agent lama tidak menyapu (SEC-62). |
| **SEC-42 butir 4** (dump tanpa RELOAD) | **Sebagian** | `FLUSH_TABLES` ≥ 8.0.31: `RESET MASTER` → 1227 (PoC). **Sisa:** FTWRL/`LOCK TABLES` tetap (SEC-56); fallback `RELOAD` untuk < 8.0.31 atau admin tanpa hak memberi (peringatan di log job); dump lewat kanal agent tetap rencana (Rencana 5). |
| **SEC-43 butir 1** (`releaseRole` mempertahankan pin sumber) | **Ditutup untuk agent baru** | `Pins.Release` hanya menulis `unassigned`; test `TestReleaseRoleKeepsSourcePin`. **Sisa:** agent lama (SEC-62). |
| SEC-44, SEC-45, SEC-46, SEC-47, SEC-48, SEC-49, SEC-50 | Di luar lingkup 4a | Dijadwalkan Rencana 4c (audit) dan 5. SEC-47a (token bot lewat HTTP polos) kini tertutup oleh HTTPS, dengan catatan SEC-54/57. |
| SEC-53 (akun `dbpanel_repl@'%'` bersama) | Tidak berubah | Ditunda (akun per replika). |

## 6. Peta OWASP Top 10 dan ASVS

### 6.1 OWASP Top 10 (2021), delta Rencana 4a

| Kategori | Yang berlaku di Rencana 4a | Temuan | Putusan | Bukti |
|---|---|---|---|---|
| A01 Broken Access Control | Token konfirmasi; pagar agent untuk akun dump; batas root vs `dbpanel`; berkas TLS di datadir | SEC-56, SEC-58, SEC-59, SEC-62 | **sebagian** | Token: PoC lulus; FTWRL dan TOCTOU: PoC berhasil |
| A02 Cryptographic Failures | HTTPS UI, pin SPKI, CA web/MySQL, verifikasi TLS MySQL | SEC-54, SEC-55, SEC-57, SEC-63 | **rentan** | Unduhan CA lewat HTTP; batasan CA luas (PoC); TLS 1.2+/1.3 lulus |
| A03 Injection | Path CA di SQL dan berkas opsi; nama host di SAN; nama tambahan | — | **aman** | `validateVerify`, `sourceHostRe`, `validHost`; `x-text` di modal |
| A04 Insecure Design | Trust bootstrap CA; cakupan subnet; pengaturan sertifikat tanpa re-auth | SEC-54, SEC-55, SEC-59 | **rentan** | PoC |
| A05 Security Misconfiguration | Header, HSTS, unit systemd | SEC-57, SEC-61 | **sebagian** | Header lulus (§4B); `systemd-analyze` 7.1 |
| A06 Vulnerable and Outdated Components | go1.26.10, modul tidak berubah | — | **aman** | govulncheck: 0 simbol, 0 paket; GO-2026-5932 tingkat modul |
| A07 Identification and Authentication Failures | Re-auth kata sandi untuk aksi destruktif; cookie `__Host-` | SEC-60, SEC-57 | **sebagian** | Limiter 5/mnt terpisah dari login |
| A08 Software and Data Integrity Failures | Distribusi CA dan binary agent; pin pairing | SEC-54, SEC-64 | **rentan** | CA lewat HTTP tanpa sidik jari |
| A09 Security Logging and Monitoring Failures | Audit `confirm_*`, `https_cert_*` | SEC-60, SEC-59 | **sebagian** | Penolakan token tidak diaudit |
| A10 SSRF | Tidak ada fetch baru dari input pengguna | — | **aman** | — |

Kategori dengan minimal satu temuan: A01, A02, A04, A05, A07, A08, A09.

### 6.2 OWASP ASVS 4.0.3 Level 2 (bab yang relevan dengan delta)

| Bab | Gagal / tak terverifikasi | Temuan | Lulus (contoh) |
|---|---|---|---|
| V1 Architecture | 1.9.1 (komunikasi terenkripsi antar komponen: CLONE hanya rantai); 1.6.x kunci CA di proses web | SEC-63a, SEC-55, SEC-64 | 1.4.1 pagar di agent, 1.14.x pemisahan proses panel/agent |
| V2 Authentication | 2.2.1 anti-otomasi pada pemeriksaan sandi ulang (5/mnt tanpa penguncian) | SEC-60 | 2.2.x limiter login; 2.4.1 bcrypt |
| V3 Session Management | 3.7.1 re-auth sebelum transaksi sensitif: tidak untuk pengaturan sertifikat HTTPS | SEC-59 | 3.4.1–3.4.5 cookie `__Host-`, `Secure`, `HttpOnly`, `SameSite=Lax`; 3.4.3 path `/` |
| V4 Access Control | 4.1.3 hak minimum (akun dump bisa FTWRL); 4.3.1 aksi administratif dengan konfirmasi | SEC-56, SEC-59 | 4.2.2 CSRF (Origin asing → 403 pada `/confirm`); token terikat target |
| V7 Error Handling and Logging | 7.2.2 kegagalan kontrol akses diaudit | SEC-60 | 7.1.1 token dan sandi tidak dicatat |
| V9 Communications | 9.1.1 TLS untuk semua koneksi klien: unduhan CA dan pengalihan lewat HTTP; 9.1.2 HSTS efektif | SEC-54, SEC-57 | 9.1.2/9.1.3 TLS 1.2+ dengan cipher AEAD; 9.2.1 verifikasi sertifikat MySQL (nama) dan pin SPKI pairing; 9.2.2 TLS ke MySQL wajib |
| V10 Malicious Code | 10.3.2 integritas pembaruan (binary agent dari panel) | SEC-64 | hash binary di skrip pasang |
| V12 Files and Resources | 12.3.1/12.3.3 operasi berkas yang tidak mengikuti symlink di direktori pengguna lain | SEC-58 | `install.sh` membaca lewat `runuser`, `mv -T`, `chown -h`; `writeFileAtomic` |
| V14 Configuration | 14.1.x hardening unit (sebagian) | SEC-61 | 14.4.1–14.4.7 header lengkap termasuk HSTS (§4B); 14.2.1 govulncheck bersih; 14.2.x `go mod verify` |

## 7. Rekomendasi berurutan dengan prioritas

1. **Sebelum merge (wajib):**
   - SEC-54 butir 1–2: sidik jari SHA-256 CA web di keluaran `install.sh`, Pengaturan, dan `/sertifikat`, dengan langkah mencocokkan; tautan `https://…/sertifikat`.
   - SEC-56 butir 1–3: sapuan `KILL` sesi dump tua dengan `lock_wait_timeout` pendek; pembunuh FTWRL yang lama; batas koneksi dan umur akun dump. README.
   - SEC-58: operasi `dbpanel-tls` lewat `os.OpenRoot`/fd, tanpa `Chmod` lewat path.
   - SEC-55 butir 2–3: tolak nama tambahan yang terlalu umum dan beri peringatan cakupan subdomain; perbaiki klaim README.
   - SEC-59: `requireConfirm` untuk unggah/kembali/nama tambahan/tutup pemberitahuan.
   - SEC-60: audit `confirm_rejected`, limiter hanya menghitung sandi salah dan berbagi anggaran dengan login (keduanya sudah ruling ledger).
   - README/pesan untuk SEC-57 (bookmark `https://`, HSTS hanya dengan nama DNS).
2. **Sebelum rilis:**
   - SEC-55 butir 1 (batasan IP /32 atau subnet sempit tanpa antarmuka virtual).
   - SEC-54 butir 3–4 (panduan GPO dari berkas di host).
   - SEC-59 validasi unggahan (`IsCA`, EKU, ukuran kunci).
   - SEC-61 (`runuser </dev/null`, penyaringan sandi, `SystemCallFilter` dan kawan-kawan).
   - SEC-62 penanda "agent perlu diperbarui".
   - SEC-57 butir 2–3.
3. **Sebelum Rencana 4b (promote):**
   - SEC-62 penolakan job di agent lama: promote bergantung pada pagar agent (§5.1).
   - SEC-63b (persetujuan sebelum mengganti `ssl_ca` operator).
   - SEC-63c (cocokkan serial `tlsInstall`; `tls_verified` setelah IO thread tersambung).
   - SEC-63g (pairing lokal dari loopback saja).
4. **Rencana 4c (audit):**
   - SEC-60 butir 3–4 (token terikat sesi, tolak target yang tidak ada).
   - Audit dan peringatan Telegram untuk penggantian sertifikat HTTPS, CA baru, dan token pasangan.
   - SEC-63e (hapus `web-ca.key.old` terjadwal).
5. **Rencana 5:**
   - SEC-42 butir 4 asli (dump lewat kanal agent, tanpa akun jaringan).
   - SEC-55 butir 4 (kunci CA web di luar proses web).
   - Rilis bertanda tangan untuk SEC-64; mode ketat.
6. **Test regresi keamanan yang perlu ditambahkan:**
   - Sapuan agent pada akun dump yang memegang FTWRL → sesi dibunuh dan akun dihapus (PoC §8.3 bisa dipakai).
   - `tlsTrust`/`tlsInstall` saat `dbpanel-tls` ditukar symlink → tidak ada berkas atau `chmod` di luar datadir (PoC §8.3).
   - Nama tambahan `co.id`/`rsud.local` ditolak, atau butuh konfirmasi dan memperingatkan.
   - Unggahan `CA:TRUE`/RSA-1024/EKU codeSigning ditolak; unggahan tanpa `confirm_token` → 403.
   - `/confirm` gagal 5× → login untuk user itu ikut terbatas; token yang ditolak → baris audit `confirm_rejected`.
   - Halaman `/sertifikat` dan Pengaturan memuat sidik jari yang sama dengan `openssl x509 -fingerprint -sha256 web-ca.crt`.
   - `TestE2EHTTPS`: `sudo` tiruan berbasis daftar izin, bukan daftar tolak (SEC-63j).

## 8. Lampiran

### 8.1 Versi dependensi (`go list -m all`)

Toolchain build: **go1.26.10** (`go version /tmp/dbpanel-audit4a/dbpanel` → `go1.26.10`). Tidak ada perubahan `go.mod`/`go.sum` di rentang `0d040d3..d785a4c`.
- **Langsung:** `github.com/coder/websocket v1.8.15`, `github.com/go-sql-driver/mysql v1.9.3`, `github.com/google/uuid v1.6.0`, `golang.org/x/crypto v0.58.0`, `gopkg.in/yaml.v3 v3.0.1`, `modernc.org/sqlite v1.61.0`.
- **Tidak langsung (antara lain):** `filippo.io/edwards25519 v1.1.1`, `github.com/dustin/go-humanize v1.1.0`, `github.com/remyoudompheng/bigfft`, `golang.org/x/{net v0.60.0, sys v0.49.0, sync v0.24.0, text v0.43.0}`, `modernc.org/{libc, mathutil, memory}`.

`go mod verify`: "all modules verified". `go vet ./...`: bersih (rc=0). Aset front-end: Alpine CSP tidak berubah; `app.css` dikompilasi ulang (1 baris), tidak diverifikasi ulang. Ilustrasi `web/static/cert/*.svg` diperiksa: tanpa `<script>`.

### 8.2 Hasil govulncheck

Perintah: `govulncheck -db https://storage.googleapis.com/go-vulndb [-show verbose] ./...` (govulncheck v1.9.0 dirakit offline dari `GOMODCACHE`, `golang.org/x/vuln@v1.9.0`, `GOPROXY=off`). 24 paket akar, stdlib go1.26.10.
- **Tingkat simbol (terjangkau): 0 kerentanan.**
- **Tingkat paket: 0.**
- Tingkat modul (tidak terpanggil): **GO-2026-5932** `golang.org/x/crypto/openpgp` (tidak dipelihara, paket tidak dipakai; sama dengan Rencana 1–3).

### 8.3 Transkrip PoC

**PoC-A: SEC-56** (`TestAudit4aPocFlushTablesDumpUser`, agent nyata, pin `master`, mysqld 8.0.46-0ubuntu0.24.04.4, 12,6 detik)
```
role pin = "master"
createDumpUser{dbpanel_dump_deadbee0@%} on pinned master: ok=false err=server ini dipin sebagai master; perintah mysql.createDumpUser ditolak. …
createDumpUser{dbpanel_dump_deadbee1@10.66.66.%} on pinned master: ok=false err=… ditolak
createDumpUser{dbpanel_dump_deadbee2@10.66.66.66} on pinned master: ok=true result={}
createDumpUser{dbpanel_dump_deadbeef@127.0.0.1} on pinned master: ok=true result={}
(run sebelumnya, nama sama host lain: ok=false err=akun 'dbpanel_dump_deadbeef'@'10.66.66.66' sudah ada di master yang dipin; agent tidak mengubah akun yang sudah ada)
grant: GRANT SELECT, LOCK TABLES, SHOW VIEW, EVENT, TRIGGER ON *.* TO `dbpanel_dump_deadbeef`@`127.0.0.1`
grant: GRANT FLUSH_TABLES ON *.* TO `dbpanel_dump_deadbeef`@`127.0.0.1`
RESET MASTER as dump user: err=Error 1227 (42000): Access denied; you need (at least one of) the RELOAD privilege(s) for this operation
FLUSH TABLES WITH READ LOCK as dump user: err=<nil>
while dump user holds FTWRL: root CREATE DATABASE: err=Error 1205 (HY000): Lock wait timeout exceeded; try restarting transaction after 2s
agent sweep: dropped=[] err=hapus user 'dbpanel_dump_deadbee2'@'10.66.66.66': Error 1205 (HY000): Lock wait timeout exceeded; try restarting transaction
sessions of the dropped account still open: 1
after the sweep dropped the account: root CREATE DATABASE: err=Error 1205 (HY000): Lock wait timeout exceeded; … after 2s
```
(`lock_wait_timeout` global diturunkan ke 2 detik untuk test; bawaannya 31.536.000 detik.)

**PoC-B: SEC-58** (`TestAudit4aPocTLSDirSymlinkRace`, datadir milik `nobody`, `flipper` sebagai `nobody` menukar `dbpanel-tls` ↔ symlink ke `/tmp/dbpanel-audit4a/victim` milik root dengan `renameat2(RENAME_EXCHANGE)`)
```
run 1: iteration 0: /tmp/dbpanel-audit4a/victim mode now -rwx------ (was 0755)
run 2: iteration 0: root wrote mysql-ca.crt into /tmp/dbpanel-audit4a/victim (mode -rw-------, owner uid 65534)
       tlsTrust calls: ok=1 refused=0; chmod through link=0, files written through link=1
(flipper dua-rename biasa: 3000 percobaan, 0 kena, 3 ditolak Lstat)
```

**PoC-C: SEC-55, name constraints** (`web-ca.crt` dari panel; leaf ditandatangani `web-ca.key` dengan `openssl x509 -req`)
```
X509v3 Name Constraints: critical
    Permitted: IP:192.0.2.0/255.255.255.0, IP:127.0.0.1/255.255.255.255, IP:192.0.2.2/255.255.255.255
    Excluded:  DNS:, email:, URI:
other-host-in-subnet   IP:192.0.2.77             openssl: OK       go: err=<nil>
ip-outside             IP:10.9.9.9               openssl: verification failed   go: IP address "10.9.9.9" is not permitted by any constraint
dns-any                DNS:simrs.rsud.local      openssl: verification failed   go: DNS name … is excluded by constraint ""
email                  email:a@b.c,IP:192.0.2.5  openssl: OK       go: email address "a@b.c" is excluded by constraint ""
uri (serverAuth)       URI:https://evil.example/ openssl: OK       go: URI … is excluded by constraint ""
smime (emailProtection) -purpose smimesign: error 26 at 1 depth lookup: unsuitable certificate purpose
--- POST /settings/cert-names extra_names="rsud.local co.id 10.0.0.1" → 303
Permitted: DNS:rsud.local, DNS:co.id, IP:192.0.2.0/24, IP:127.0.0.1/32, IP:192.0.2.2/32, IP:10.0.0.1/32
DNS:www.bca.co.id      openssl: OK | go err=<nil>
DNS:simrs.rsud.local   openssl: OK | go err=<nil>
DNS:www.google.com     openssl: verification failed | go: not permitted by any constraint
```

**PoC-D: SEC-59, unggah sertifikat**
```
rsa1024.crt: Public-Key: (1024 bit); CA:TRUE; Extended Key Usage: Code Signing; self-signed; SAN IP:127.0.0.1
POST /settings/cert (sesi, tanpa sandi) → 303 https://127.0.0.1:18080/settings?pesan=sertifikat-sendiri
openssl s_client → subject=CN = attacker; Basic Constraints: critical CA:TRUE; EKU: Code Signing
POST /settings/cert-panel → 303; subject=CN = 127.0.0.1
```

**PoC-E: SEC-60 dan §4A, siklus token** (binary, sesi `admin`)
```
/confirm wrong pw ×5 → 401 {"error":"Kata sandi salah."}; ke-6 → 429 {"error":"Terlalu banyak percobaan konfirmasi; coba lagi dalam 1 menit."}
delete server 2 tanpa token → 403
token len 43
wrong target (3): 403
same token, right target, after misuse: 303 /servers?pesan=dihapus
token from session 1 used in session 2 (same user): 303 /servers?pesan=dihapus
use on local server (consumed, then refused 409); replay same token: 403
CSRF /confirm foreign origin: 403;  unknown action: 400;  token for nonexistent target 999: 200 {"token":"…"}
expired token (121 s): 403
audit_log: confirm_failed ×5 (Kata sandi salah.), confirm_failed (rate_limited), confirm_issued …, server_delete ok/409; tidak ada baris untuk 403 penolakan token
panel.log: 9 × "confirmation refused" (Warn)
```

**PoC-F: §4A, listener terbelah** (Python `-I`, soket mentah ke 127.0.0.1:18080)
```
CL+TE plain: ['HTTP/1.1 301 … Location: https://127.0.0.1:18080/sertifikat', 'HTTP/1.1 301 … Location: https://127.0.0.1:18080/servers']
pipelined plain: ['HTTP/1.1 200 OK', 'HTTP/1.1 301 … Location: https://127.0.0.1:18080/servers']
plain then TLS bytes on same conn: ['HTTP/1.1 200 OK']
TLS CL+TE: ['HTTP/1.1 403 Forbidden']
HTTP/1.0 no host: ['HTTP/1.0 421 Misdirected Request']
legit under load (1500 silent + 300 partial): 200 in 0.03 s;  TLS: 200 in 0.07 s
conns still open after 6.5s: 0 of 1800
Host evil.example → 421/421; 127.0.0.1:18080@evil.example → 400/400; 127.0.0.1:18080/evil → 400/400; 127.0.0.1. → 421; 192.0.2.77 → 421
/static/../servers → 307 http://127.0.0.1:18080/servers; /sertifikat/../servers → 301 https://…/servers; /static/..%2fservers → 404
POST /agent/pair over HTTP → 301 https://…/agent/pair; GET /agent/install?token=ABC over HTTP → 301 https://…/agent/install?token=ABC
pin salah: curl -sk --pinnedpubkey sha256//AAAA… → 000, rc=90
dbpanel panel (root, tanpa --allow-root) → "panel tidak dijalankan sebagai root …", rc=1
```

**PoC-G: unit dan install.sh**
```
systemd-analyze security --offline=true scripts/systemd/dbpanel-panel.service → Overall exposure level: 7.1 MEDIUM
  ✗ SystemCallFilter (semua grup), ✗ ProtectProc, ✗ ProcSubset, ✗ PrivateDevices/DeviceAllow, ✗ RestrictAddressFamilies, ✗ CapabilityBoundingSet …
  ✓ ProtectSystem=strict, PrivateTmp, ProtectHome, ProtectKernelModules/Tunables, ProtectControlGroups, RestrictSUIDSGID, LockPersonality, UMask
bash scripts/install_test.sh → "ok  scripts/install_test.sh" (shellcheck tidak terpasang; dilewati)
sysctl: dev.tty.legacy_tiocsti = 1; fs.protected_hardlinks = 1; kernel.yama tidak ada
```

### 8.4 Yang tidak bisa dijalankan atau dibuktikan

- **MITM LAN nyata** (ARP/DHCP spoofing) terhadap unduhan `/sertifikat` dan pengalihan 301 tidak dijalankan. SEC-54 dan SEC-57 dibuktikan dari respons HTTP polos dan isi halaman.
- **Perilaku browser:** HSTS untuk host IP (dasar: RFC 6797 §8.1), penegakan name constraints dan EKU CA oleh Windows CryptoAPI, macOS, Android, dan iOS tidak diuji di browser atau OS nyata. Hanya Go dan OpenSSL.
- **Rantai TIOCSTI** (SEC-61) tidak dicoba end-to-end: butuh ptrace ke proses `runuser` berumur milidetik dan sesi root interaktif tanpa `use_pty`.
- **Unit systemd** tidak dijalankan di bawah systemd PID 1. Analisis memakai `systemd-analyze --offline`. `install.sh` hanya dijalankan lewat harness dengan `ROOT=` dan systemctl tiruan, tidak terhadap path sistem nyata.
- **MySQL 8.0.26–8.0.30** (fallback `RELOAD`) dan **8.4** (`RESET BINARY LOGS AND GTIDS`) tidak diuji; hanya 8.0.46.
- **Replikasi/dump/CLONE dengan sertifikat palsu dari on-path** tidak diuji di jaringan. Penolakan rantai dan nama mengandalkan test repo (`TestReplicationVerifiesSourceCert` dan kasus nama tidak cocok di e2e) yang tidak dijalankan ulang di audit ini. Ledger mencatat `make test` 15m25s dan `make test-e2e` 5m02s lulus.
- **Modal konfirmasi di browser** (Playwright, `-tags e2e_browser`) tidak dijalankan.
- **TLS 1.0/1.1** tidak bisa diuji dengan klien OpenSSL lokal (protokol dimatikan di klien). Batas bawah dibaca dari kode (`MinVersion: tls.VersionTLS12`).
- PoC TOCTOU (SEC-58) memakai `nobody` sebagai pemilik datadir (harness `testmysql`), bukan user `mysql` dari paket. Perilaku agent identik karena pemilik dibaca dari `stat` datadir.
