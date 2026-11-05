# Rencana Implementasi 4a: Prasyarat Keamanan dan HTTPS Panel

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Setiap perintah destruktif panel butuh kata sandi ulang (token
konfirmasi), panel berjalan sebagai user `dbpanel` tanpa root dengan
agent lokal terpisah, panel melayani HTTPS otomatis sejak pasang di satu
port dengan halaman pasang sertifikat untuk orang awam, sertifikat MySQL
diterbitkan CA panel dan diverifikasi replika, akun dump tanpa `RELOAD`
bila seri mendukung, `releaseRole` mempertahankan pin sumber, dan agent
menyapu akun dump sendiri. Rencana 4b (promote) dibangun di atas pagar
ini; 4c (penguatan audit) menyusul.

**Architecture:** Token konfirmasi adalah tabel `confirmations` di
SQLite dan satu komponen modal Alpine yang dipakai semua tombol
destruktif. Paket `ca` memperoleh dua CA tambahan (web dengan name
constraints, MySQL) dan penerbit sertifikat web/MySQL. Listener UI
membaca byte pertama tiap koneksi: TLS → HTTPS, selain itu HTTP yang
hanya melayani `/sertifikat` dan mengalihkan sisanya. Pasangan agent
tetap di port web, di atas HTTPS dengan pin SPKI di perintah pasang.
Agent tertanam dihapus: server panel menjadi agent biasa yang
berpasangan lewat token lokal sekali pakai yang ditulis panel dan
dipindahkan `install.sh`. TLS MySQL adalah dua perintah agent
(`tlsRequest`, `tlsInstall`) yang dirangkai job orkestrator `mysql_tls`
dan dipanggil "Siapkan master", "Siapkan replika", "Pasang sertifikat
MySQL", dan "Perketat sambungan replikasi".

**Tech Stack:** Go 1.26.10, `CGO_ENABLED=0`, `crypto/x509` (name
constraints, SPKI pin), SQLite (`modernc.org/sqlite`), daisyUI 5 /
Tailwind 4 / Alpine CSP yang ada, harness `testmysql` (mysqld 8.0.46),
`scripts/install_test.sh` (systemd di kontainer).

**Spec:** `docs/superpowers/specs/2026-11-05-rencana-4-promote-design.md`
bagian 2 (tujuan 6–7), 3, 4, 5.7–5.11, 5.13 (bagian yang disebut di
sini), 5.14 (`tls_expiring`), 5.15, 5.16, 8, 9. Spec Rencana 3 5.5 untuk
tabel pin yang ada. Audit:
`docs/superpowers/security/2026-11-02-audit-plan-3.md` (SEC-06, SEC-23,
SEC-28 butir 5, SEC-29 butir 2, SEC-41, SEC-42 butir 2 dan 4, SEC-43
butir 1).

## Global Constraints

- Go 1.26.10, `CGO_ENABLED=0`, pure Go; modul `github.com/husmin-fajrin/db-panel`.
- Antarmuka tidak memakai kata CA, SSL, TLS di teks utama; hanya
  "sertifikat keamanan panel" dan "sertifikat MySQL". Istilah teknis
  hanya di "Detail teknis" dan README.
- Token konfirmasi: 32 byte acak (base64url), disimpan sebagai SHA-256
  hex, umur 2 menit, sekali pakai, terikat `username`, `action`,
  `target_kind`, `target_id`; batas laju `POST /confirm` 5 per menit per
  user; kegagalan sandi → audit `confirm_failed`.
- Wajib token (spec 5.7): promote (4b), bangun ulang master lama (4b),
  unduh dan hapus salvage (4b), resync ulang, lepas replika, hapus
  server, lepas database (`remove_databases`), buka kunci master,
  "Perketat sambungan replikasi", "Pasang sertifikat MySQL",
  `prepare-master` bila master sudah punya topologi. Tanpa token:
  "Sudah saya alihkan", stop/start, kunci kembali, snooze, periksa,
  tes Telegram, simpan pengaturan.
- Tata letak berkas (spec 5.8): `/etc/dbpanel` dan `/var/lib/dbpanel`
  0711 root; `/etc/dbpanel/panel` dan `/var/lib/dbpanel/panel` 0700
  `dbpanel`; berkas agent langsung di `/etc/dbpanel` 0600 root; TLS MySQL
  di `<datadir>/dbpanel-tls/` (folder 0700, berkas 0600, milik user
  `mysql`). Unit panel `User=dbpanel`, `ProtectSystem=strict`,
  `ReadWritePaths=/var/lib/dbpanel/panel /etc/dbpanel/panel`.
- Port web tetap 8080 (HTTPS dan HTTP di satu port, byte pertama 0x16 =
  TLS), port agent mTLS 8443 tidak berubah. HTTP hanya melayani `GET
  /sertifikat` dan `GET /sertifikat/dbpanel-panel.crt`; jalur lain `301`
  ke `https://` host dan jalur yang sama. Cookie sesi `Secure` selalu;
  `Strict-Transport-Security: max-age=31536000` hanya pada HTTPS.
- CA: web CA dan MySQL CA masa 10 tahun, leaf 2 tahun, terbit ulang
  otomatis 30 hari sebelum kedaluwarsa; name constraints web CA = subnet
  tiap antarmuka saat dibuat + `PublicHost` + nama host mesin +
  `https_extra_names`. Nama di luar batasan → CA web baru +
  pemberitahuan tetap. SAN sertifikat MySQL hanya `servers.address` dan
  `source_host` topologi.
- Perintah pasang agent (spec 5.11): `curl -fsSL -k --pinnedpubkey
  sha256//<SPKI web.crt> -K - <<<'url = "https://…/agent/install?token=…"'`
  (token tetap lewat stdin, bukan argv). Skrip pasang dan `dbpanel agent
  pair` memakai pin yang sama; CA agent tetap dipin lewat `CA_SHA256`.
- Akun dump: hak `FLUSH_TABLES` dinamis bila seri mendukung (builder
  menetapkan versi minimum dari dokumentasi MySQL dan menulisnya di
  README), selain itu `RELOAD`. Agent menyapu `dbpanel_dump_*` > 24 jam
  tiap 1 jam. `mysql.releaseRole` mempertahankan pin sumber.
- Semua teks pengguna berbahasa Indonesia; pesan penolakan menyebut
  perintah CLI bila ada.
- Tidak ada perbaikan otomatis selain pembaruan sertifikat (terbit ulang
  sebelum kedaluwarsa) yang disebut spec.

## Review Focus

1. Browser lama atau alat yang membuka `http://ip:8080/servers/3`
   harus sampai ke halaman yang sama di HTTPS tanpa kehilangan jalur;
   koneksi yang mengirim byte non-TLS lalu diam tidak boleh menggantung
   goroutine (tenggat baca 5 detik). Test di Task 5.
2. Unggahan sertifikat sendiri yang kuncinya tidak cocok, kedaluwarsa,
   atau SAN-nya tidak memuat alamat panel harus ditolak sebelum dipakai,
   dan panel yang start dengan berkas rusak harus kembali ke sertifikat
   buatannya sendiri, tidak pernah HTTP polos. Test di Task 5.
3. Token konfirmasi yang dipakai pada sasaran lain, oleh user lain,
   setelah 2 menit, atau dua kali harus ditolak dengan pesan yang
   meminta ulang, dan handler tidak boleh menjalankan apa pun. Test di
   Task 2.
4. `mysql.tlsInstall` yang gagal memuat (berkas salah, `RELOAD TLS`
   ditolak) harus mengembalikan berkas lama dan MySQL tetap melayani;
   replika yang `CHANGE SOURCE` dengan verifikasi terhadap master tanpa
   sertifikat panel harus ditolak panel sebelum perintah terkirim. Test
   di Task 6 dan 7.
5. Upgrade dari instalasi root yang dijalankan dua kali, atau terputus
   setelah memindahkan berkas tetapi sebelum agent lokal berpasangan,
   harus berakhir pada panel non-root dan agent lokal berpasangan tanpa
   kehilangan panel.db, CA, atau pin. Test di Task 8.

---

### Task 1: Store: migrasi 0006 dan metode token konfirmasi, `server_tls`, pengaturan

**Files:**
- Create: `internal/panel/store/migrations/0006_confirm_tls.sql`,
  `internal/panel/store/confirmations.go`, `internal/panel/store/tls.go`
- Modify: `internal/panel/store/topologies.go` (kolom `replicas.tls_verified`),
  `internal/panel/store/store_test.go`
- Test: `internal/panel/store/confirmations_test.go`, `internal/panel/store/tls_test.go`

**Interfaces:**
- Produces:

```sql
CREATE TABLE confirmations (
    token_hash  TEXT PRIMARY KEY,
    username    TEXT NOT NULL,
    action      TEXT NOT NULL,
    target_kind TEXT NOT NULL DEFAULT '',
    target_id   INTEGER NOT NULL DEFAULT 0,
    expires_at  TEXT NOT NULL,
    used_at     TEXT
);
CREATE TABLE server_tls (
    server_id    INTEGER PRIMARY KEY REFERENCES servers(id) ON DELETE CASCADE,
    cert_serial  TEXT NOT NULL,
    not_after    TEXT NOT NULL,
    installed_at TEXT NOT NULL
);
ALTER TABLE replicas ADD COLUMN tls_verified INTEGER NOT NULL DEFAULT 0;
-- settings keys (baris dibuat saat dibaca pertama, bukan di migrasi):
-- https_mode ('panel'|'custom', bawaan 'panel'), https_extra_names (dipisah koma)
```

```go
type Confirmation struct{ TokenHash, Username, Action, TargetKind string; TargetID int64; ExpiresAt time.Time; UsedAt *time.Time }
func (s *Store) AddConfirmation(c Confirmation) error
// ConsumeConfirmation menandai dipakai dalam satu transaksi; ErrConfirmInvalid bila tidak ada,
// beda user/action/target, kedaluwarsa (now >= ExpiresAt), atau sudah dipakai.
func (s *Store) ConsumeConfirmation(tokenHash, username, action, targetKind string, targetID int64, now time.Time) error
func (s *Store) PruneConfirmations(olderThan time.Time) (int64, error)
var ErrConfirmInvalid = errors.New("konfirmasi tidak berlaku; ulangi konfirmasi")
type ServerTLS struct{ ServerID int64; CertSerial string; NotAfter, InstalledAt time.Time }
func (s *Store) SetServerTLS(t ServerTLS) error
func (s *Store) GetServerTLS(serverID int64) (ServerTLS, error)   // ErrNotFound bila belum ada
func (s *Store) ListServerTLSExpiring(before time.Time) ([]ServerTLS, error)
func (s *Store) SetReplicaTLSVerified(serverID int64, verified bool) error
// Replica memperoleh field TLSVerified bool; scanReplica membacanya.
```

- [ ] **Step 1: Tulis test** `TestConfirmationConsumeOnce` (dua kali → kedua `ErrConfirmInvalid`), `TestConfirmationBoundToUserActionTarget` (tiap kombinasi beda → invalid, aslinya tetap bisa dipakai), `TestConfirmationExpires` (now = ExpiresAt → invalid), `TestPruneConfirmations`, `TestServerTLSRoundTripAndExpiring`, `TestReplicaTLSVerified`, dan tambahkan 0006 ke test migrasi yang ada (`TestMigrateFresh`/`TestMigrateFromExisting` atau setara di `store_test.go`).
- [ ] **Step 2: Jalankan test, pastikan gagal** — `go test ./internal/panel/store/ -run 'Confirmation|ServerTLS|TLSVerified' -v`.
- [ ] **Step 3: Implementasi** migrasi dan metode. `ConsumeConfirmation` memakai `UPDATE … SET used_at=? WHERE token_hash=? AND username=? AND action=? AND target_kind=? AND target_id=? AND used_at IS NULL AND expires_at > ?` dan memeriksa `RowsAffected()==1`.
- [ ] **Step 4: Jalankan test, pastikan lulus** — `go test ./internal/panel/store/`.
- [ ] **Step 5: Commit** — `git commit -m "feat(store): confirmations, server_tls, replica tls_verified (migration 0006)"`

---

### Task 2: Web: token konfirmasi dan modal kata sandi untuk semua tombol destruktif

**Files:**
- Create: `internal/panel/web/handlers_confirm.go`
- Modify: `internal/panel/web/router.go` (rute `POST /confirm`, `Deps.ConfirmLimiter *auth.RateLimiter`),
  `internal/panel/web/handlers_controls.go`, `handlers_mysql.go` (handler destruktif memanggil `a.requireConfirm`),
  `web/static/app.js` (komponen `confirmModal`), `web/templates/_confirm.html` (baru, partial),
  `web/templates/replica_detail.html`, `master_detail.html`, `server_detail.html`, `replica_databases.html`,
  `internal/panel/web/render.go` (`partials` += `_confirm.html`), `internal/panel/panel.go` (membuat limiter),
  `internal/panel/web/audit.go` (aksi `confirm_issued`, `confirm_failed`)
- Test: `internal/panel/web/web_confirm_test.go`, perluasan `web_controls_test.go`, `web_mysql_jobs_test.go`

**Interfaces:**
- Consumes: Task 1 (`AddConfirmation`, `ConsumeConfirmation`, `ErrConfirmInvalid`), `Deps.CheckPassword`, `auth.RateLimiter`.
- Produces:

```go
// POST /confirm  form: action, target_kind, target_id, password  → 200 JSON {"token": "..."}; 401 sandi salah (audit confirm_failed);
// 429 bila limiter (kunci = username) menolak; 400 action tidak dikenal (daftar const confirmActions).
const (
    ActResync = "resync"; ActDetach = "detach"; ActDeleteServer = "delete_server"; ActRemoveDatabases = "remove_databases"
    ActUnlockMaster = "unlock_master"; ActPrepareMasterAgain = "prepare_master_again"; ActMySQLTLS = "mysql_tls"; ActTightenTLS = "tighten_tls"
    // 4b menambah: promote, rebuild_old_master, salvage_download, salvage_delete
)
// requireConfirm membaca confirm_token dari form, memanggil ConsumeConfirmation dengan username sesi, action, target;
// gagal → 403 dengan flash "Konfirmasi tidak berlaku; ulangi konfirmasi." dan return false. Dipanggil sebelum efek apa pun.
func (a *app) requireConfirm(w http.ResponseWriter, r *http.Request, action, targetKind string, targetID int64) bool
```

Komponen Alpine `confirmModal` (CSP build, tanpa JS inline): tombol
destruktif memakai atribut `data-confirm-action`, `data-confirm-kind`,
`data-confirm-id`, `data-confirm-name` (bila perlu ketik nama) dan
`data-confirm-text` (kalimat peringatan). Modal meminta kata sandi
(+ nama server bila diminta), memanggil `POST /confirm` lewat `fetch`
same-origin, lalu mengisi `<input name="confirm_token">` (dan
`confirm_name`) pada form tombol dan mengirimnya. Tombol lama yang hanya
mengetik nama server (resync, hapus server, lepas database dengan drop)
sekarang melewati modal yang sama; `typedName` tetap dipakai handler.

- [ ] **Step 1: Tulis test** `TestConfirmIssuesTokenAndHandlerConsumesIt` (resync lewat `POST /confirm` lalu `POST /replicas/{id}/resync` dengan token → job dibuat), `TestConfirmWrongPasswordAudited` (401 + baris audit `confirm_failed`, tanpa token), `TestConfirmRateLimited` (6 percobaan/menit → 429), `TestHandlerRefusesMissingOrForeignToken` (tanpa token, token sasaran lain, token user lain, token dipakai dua kali, token kedaluwarsa dengan `Now` palsu → 403 dan orkestrator palsu tidak dipanggil), `TestUnlockMasterNeedsToken`, `TestPrepareMasterAgainNeedsTokenOnlyWithTopology`, `TestConfirmUnknownAction400`.
- [ ] **Step 2: Jalankan test, pastikan gagal.**
- [ ] **Step 3: Implementasi** handler, `requireConfirm`, komponen, partial, pemasangan pada tiap tombol di template. Handler destruktif memanggil `requireConfirm` **sebelum** `typedName` dan sebelum orkestrator. Audit `confirm_issued` mencatat action/target.
- [ ] **Step 4: Jalankan test** — `go test ./internal/panel/web/ -race`; `bash scripts/tailwind.sh` bila kelas CSS baru dipakai (periksa README bagian "Mengubah tampilan").
- [ ] **Step 5: Commit** — `git commit -m "feat(web): password confirmation token for destructive actions"`

---

### Task 3: Agent: akun dump tanpa RELOAD, penyapuan mandiri, `releaseRole` mempertahankan pin sumber

**Files:**
- Modify: `internal/agent/drivers/mysql/users.go` (`dumpGrants` per seri), `internal/agent/drivers/mysql/dump.go`
  (bila `probeArgs`/`dumpArgs` butuh perubahan), `internal/agent/drivers/mysql/pins.go` (`Release` tidak menghapus
  sumber), `internal/agent/drivers/mysql/replication.go` (pesan penolakan menyebut `set-source` dengan host yang dipin),
  `internal/agent/monitor/monitor.go` (penyapuan tiap jam lewat `Source` baru `SweepDumpUsers`), `README.md`
- Test: `internal/agent/drivers/mysql/users_test.go`, `pins_test.go`, `internal/agent/monitor/monitor_test.go`

**Interfaces:**
- Produces:

```go
// users.go
func dumpGrantsFor(version string) string   // "SELECT, FLUSH_TABLES, LOCK TABLES, SHOW VIEW, TRIGGER, EVENT" bila versi >= minimum FLUSH_TABLES (builder menetapkan dari dokumentasi 8.0; konstanta flushTablesMinVersion), selain itu daftar lama dengan RELOAD
// monitor.go: Options.SweepEvery time.Duration (bawaan 1 jam) dan Source memperoleh metode opsional
type DumpSweeper interface{ SweepDumpUsers(ctx context.Context, olderThan time.Duration) (dropped int, err error) }  // Driver mengimplementasikan; monitor memanggil bila Source memenuhinya
// pins.go: Release() hanya menulis role=unassigned; pin sumber tetap. Pesan refusal sumber: "server ini dipin mengikuti sumber <host:port>; … jalankan: sudo dbpanel agent set-source <host:port>"
```

- [ ] **Step 1: Tulis test** `TestDumpGrantsByVersion` (8.0.<min> → RELOAD, >= min → FLUSH_TABLES, 8.4/9.x → FLUSH_TABLES), `TestCreateDumpUserNoReloadOnSupportedSeries` (testmysql 8.0.46: `SHOW GRANTS` tidak memuat RELOAD; dump uji `--no-data` masih berjalan; `RESET BINARY LOGS AND GTIDS` oleh akun dump ditolak 1227), `TestReleaseRoleKeepsSourcePin` (replica→unassigned, `Source()` tetap; `configureReplication` ke host lain ditolak dengan pesan `set-source`), `TestMonitorSweepsDumpUsersHourly` (jam palsu, Source palsu menghitung panggilan).
- [ ] **Step 2: Jalankan test, pastikan gagal.**
- [ ] **Step 3: Implementasi.** Bila dump memakai `--single-transaction` dan masih memerlukan `FLUSH TABLES WITH READ LOCK`, `FLUSH_TABLES` mencukupi; builder memverifikasi dengan test di atas dan mencatat hasil di README "Keamanan".
- [ ] **Step 4: Jalankan test** — `go test ./internal/agent/... -run 'Dump|Release|Sweep'` (test MySQL nyata ~2 menit).
- [ ] **Step 5: Commit** — `git commit -m "feat(agent): dump account without RELOAD, hourly dump-user sweep, releaseRole keeps source pin"`

---

### Task 4: CA: CA web dengan name constraints, CA MySQL, penerbit sertifikat web dan MySQL

**Files:**
- Create: `internal/panel/ca/web.go`, `internal/panel/ca/mysql.go`
- Modify: `internal/panel/ca/ca.go` (faktor umum `createCA` menerima template), `internal/pki/pki.go` (`SPKIPin(certPEM) (string, error)` = base64 SHA-256 SPKI, format `sha256//…`)
- Test: `internal/panel/ca/web_test.go`, `internal/panel/ca/mysql_test.go`, `internal/pki/pki_test.go`

**Interfaces:**
- Produces:

```go
// web.go
type WebNames struct{ DNS []string; IPs []net.IP; Subnets []*net.IPNet }
func LoadOrCreateWebCA(dir string, names WebNames) (*WebCA, error)         // web-ca.crt/key, 10 tahun, IsCA, PermittedDNSDomains=names.DNS, PermittedIPRanges=names.Subnets (+ /32 tiap IP), PermittedDNSDomainsCritical=true
func (w *WebCA) Covers(names WebNames) bool                                 // semua nama/IP dalam batasan
func (w *WebCA) IssueWebCert(names WebNames) (certPEM, keyPEM []byte, err error)   // 2 tahun, serverAuth, SAN = DNS+IPs
func (w *WebCA) IssueWebCertWithKey(names WebNames, keyPEM []byte) (certPEM []byte, err error)  // terbit ulang mempertahankan kunci (pin tetap)
func (w *WebCA) CAPEM() []byte
// mysql.go
func LoadOrCreateMySQLCA(dir string) (*MySQLCA, error)                      // mysql-ca.crt/key, 10 tahun
func (m *MySQLCA) IssueMySQLCert(csrPEM []byte, serverID int64, hosts []string) (certPEM []byte, serial string, notAfter time.Time, err error)  // SAN dari hosts (IP → IPAddresses, lainnya DNSNames), CN "dbpanel-mysql-<id>", serverAuth, 2 tahun; CSR hanya dipakai kunci publiknya
func (m *MySQLCA) CAPEM() []byte
```

- [ ] **Step 1: Tulis test** `TestWebCANameConstraintsAcceptAndReject` (verifikasi rantai Go: SAN di subnet lolos, IP di luar subnet gagal dengan `x509.CertificateInvalid`, DNS `google.com` gagal), `TestWebCACovers`, `TestIssueWebCertKeepsKey` (SPKI pin sama sebelum/sesudah terbit ulang), `TestMySQLCertSANOnlyFromHosts` (CSR berisi SAN asing → tidak masuk sertifikat), `TestSPKIPinFormat`.
- [ ] **Step 2: Jalankan test, pastikan gagal.**
- [ ] **Step 3: Implementasi.**
- [ ] **Step 4: Jalankan test** — `go test ./internal/panel/ca/ ./internal/pki/`.
- [ ] **Step 5: Commit** — `git commit -m "feat(ca): web CA with name constraints, MySQL CA, SPKI pin"`

---

### Task 5: Panel: HTTPS satu port, halaman `/sertifikat`, cookie Secure, HSTS, sertifikat sendiri, pasangan agent berpin

**Files:**
- Create: `internal/panel/tlsui.go` (listener pemilah, pemuat sertifikat, terbit ulang),
  `internal/panel/web/handlers_cert.go`, `web/templates/cert.html`, `web/static/cert/*.png` (tangkapan layar; builder
  boleh memakai gambar skematik sederhana), `internal/panel/web/handlers_settings.go` (bagian "Sertifikat keamanan panel")
- Modify: `internal/panel/servers.go` (`serveUI` memakai pemilah; `PublicURL()` → `https://`), `internal/panel/panel.go`
  (`Options.TLSMode`, `publicNames`), `internal/panel/auth/middleware.go` (`Secure` selalu true),
  `internal/panel/web/router.go` (HSTS middleware pada permintaan TLS; `Deps.CertInfo func() CertInfo`),
  `internal/panel/web/handlers_servers.go` (`pairCommand` dengan `-k --pinnedpubkey sha256//<pin>`),
  `internal/panel/web/handlers_settings.go` (`panel_base_url` bawaan `https://`; nilai `http://` lama ditampilkan dengan
  peringatan dan tautan "ubah ke https"),
  `internal/panel/agentapi/install.go` (`InstallScriptParams.WebPin`), `web/templates/agent-install.sh.tmpl`
  (curl memakai `-k --pinnedpubkey`), `internal/agent/pair.go` (`PairOptions.WebPin string`; `VerifyPeerCertificate`
  membandingkan SPKI; tanpa pin → tolak https tanpa CA dikenal), `internal/agent/cli.go` (`pair --web-pin`,
  env `DBPANEL_WEB_PIN`), `web/templates/settings.html`, `README.md` (bagian "Pasang panel", "Alamat panel untuk agent",
  "Keamanan": HTTPS, cara pasang sertifikat, Group Policy)
- Test: `internal/panel/tlsui_test.go`, `internal/panel/web/web_cert_test.go`, `internal/panel/panel_test.go`
  (pasangan lewat HTTPS berpin), `internal/agent/pair_test.go`

**Interfaces:**
- Consumes: Task 4 (`WebCA`, `SPKIPin`), Task 1 (settings `https_mode`, `https_extra_names`).
- Produces:

```go
// tlsui.go
type CertInfo struct{ Mode string; Names []string; NotAfter time.Time; Pin string; CANeedsReinstall bool }
func (p *Panel) certInfo() CertInfo
// splitListener: Accept() membaca 1 byte dengan tenggat 5 detik (peekTimeout); 0x16 → koneksi TLS (tls.Server dengan GetCertificate
// yang membaca sertifikat aktif di bawah mutex), lainnya → koneksi HTTP polos yang dilayani httpHelperHandler.
// httpHelperHandler: GET /sertifikat dan GET /sertifikat/dbpanel-panel.crt (Content-Type application/x-x509-ca-cert,
// Content-Disposition attachment) dilayani; jalur lain 301 ke "https://"+r.Host+r.URL.RequestURI().
// Terbit ulang: goroutine harian; bila NotAfter-now < 30 hari atau nama berubah → IssueWebCertWithKey; bila nama di luar batasan →
// LoadOrCreateWebCA baru (berkas lama disalin ke web-ca.crt.old), CANeedsReinstall=true sampai pengguna menutup pemberitahuan
// (settings key https_ca_notice_closed_at). Mode custom: web.custom.crt/key di ConfigDir; validasi saat simpan:
// kunci cocok, NotAfter > now, SAN memuat PublicHost; gagal → 400 dengan alasan. Berkas aktif rusak saat start → log Warn + mode panel.
```

Halaman `GET /sertifikat` (di HTTP dan HTTPS, tanpa login): judul
"Pasang sertifikat keamanan panel", kalimat "Browser menampilkan
peringatan? Unduh berkas ini, klik dua kali, pilih Pasang, lalu buka
panel lagi.", tombol unduh, langkah per sistem (Windows, macOS, Firefox,
Android, iOS) dengan gambar, dan satu paragraf "Admin domain Windows
bisa menyebarkan berkas ini ke semua komputer lewat Group Policy
(Computer Configuration → Policies → Windows Settings → Security Settings
→ Public Key Policies → Trusted Root Certification Authorities)". Mode
custom menampilkan "Panel memakai sertifikat yang dipasang admin; tidak
ada yang perlu dipasang."

- [ ] **Step 1: Tulis test** `TestSplitListenerServesTLSAndHTTP` (klien TLS dapat halaman login; klien HTTP `GET /servers` → 301 `https://host/servers`; `GET /sertifikat` → 200 dengan tombol unduh; koneksi yang mengirim "GET" lalu diam ditutup dalam ≤ 6 detik tanpa goroutine tersisa), `TestSessionCookieSecureAndHSTS`, `TestPublicURLIsHTTPS`, `TestCustomCertValidation` (kunci tidak cocok, kedaluwarsa, SAN tanpa PublicHost → 400; sah → dipakai, `certInfo().Mode=="custom"`), `TestBrokenCertFallsBackToPanelCert`, `TestWebCertRenewsKeepingPin` (jam palsu 23 bulan), `TestNewNameOutsideConstraintsRegeneratesCA` (`https_extra_names` ditambah IP subnet lain → CA baru, `CANeedsReinstall`), `TestPairOverHTTPSWithPin` (panel nyata di loopback; `agent.Pair` dengan pin benar → sukses; pin salah → gagal sebelum token terkirim), `TestPairCommandHasPin`.
- [ ] **Step 2: Jalankan test, pastikan gagal.**
- [ ] **Step 3: Implementasi** listener, helper, handler, pengaturan, pin, README.
- [ ] **Step 4: Jalankan test** — `go test ./internal/panel/... ./internal/agent/ -race` dan `make test-e2e` (e2e yang ada harus tetap lulus; `uiClient` dan `startE2EAgent` memakai HTTPS dengan pin).
- [ ] **Step 5: Commit** — `git commit -m "feat(panel): HTTPS on one port with certificate page, pinned agent pairing"`

---

### Task 6: Agent: perintah TLS MySQL, replikasi dan dump/clone terverifikasi, preflight `CONNECTION_ADMIN`

**Files:**
- Create: `internal/agent/drivers/mysql/tls.go`
- Modify: `internal/proto/mysql.go` (`TLSRequestResult`, `TLSInstallParams`, `TLSInstallResult`;
  `ReplicationSourceParams.SSLCA string`, `.VerifyServerCert bool`; `SnapshotParams.SSLCA string`, `.VerifyIdentity bool`),
  `internal/agent/drivers/mysql/driver.go` (`registerTLS`), `replication.go` (`SOURCE_SSL_CA`, `SOURCE_SSL_VERIFY_SERVER_CERT`),
  `dump.go` (`--ssl-mode=VERIFY_IDENTITY --ssl-ca=`), `clone.go` (`clone_ssl_ca` dan verifikasi sesuai dukungan versi),
  `connect.go` (daftar hak wajib += `CONNECTION_ADMIN`), `pins.go` (`mysql.tlsRequest`/`tlsInstall` diterima semua peran),
  `README.md` (hak akun admin)
- Test: `internal/agent/drivers/mysql/tls_test.go`, `replication_test.go`, `connect_test.go`

**Interfaces:**
- Produces:

```go
// proto
type TLSRequestResult struct{ CSRPEM string `json:"csr_pem"`; Hostname string `json:"hostname"` }
type TLSInstallParams struct{ CertPEM string `json:"cert_pem"`; CAPEM string `json:"ca_pem"`; VerifyHost string `json:"verify_host"` }
type TLSInstallResult struct{ Serial string `json:"serial"`; NotAfter time.Time `json:"not_after"`; Reloaded bool `json:"reloaded"` }
// perintah agent
// "mysql.tlsRequest" {} → TLSRequestResult: membuat <datadir>/dbpanel-tls/ (0700, chown mysql), server.key.new (0600 mysql), CSR P-256 tanpa SAN
// "mysql.tlsInstall" TLSInstallParams → TLSInstallResult: tulis server.crt.new, mysql-ca.crt.new; rename atomik ke server.crt/server.key/mysql-ca.crt
//   (lama → *.prev); SET PERSIST ssl_ca/ssl_cert/ssl_key; ALTER INSTANCE RELOAD TLS; verifikasi sambungan baru tls=VERIFY_IDENTITY
//   ke host servers.address (dikirim panel di params.VerifyHost) → gagal: kembalikan *.prev, RELOAD TLS ulang, error.
// "mysql.tlsTrust" {CAPEM} → menulis hanya mysql-ca.crt (dipakai repoint di 4b dan replika tanpa sertifikat sendiri)
func TLSDir(l Layout) string   // filepath.Join(l.DataDir, "dbpanel-tls")
```

`ConfigureReplication` menambahkan `SOURCE_SSL_CA = '<path>'` dan
`SOURCE_SSL_VERIFY_SERVER_CERT = 1` hanya bila `VerifyServerCert`;
`SSLCA` kosong dengan `VerifyServerCert` → ParamError. `DumpFrom` dan
`CloneFrom` memakai `--ssl-mode=VERIFY_IDENTITY --ssl-ca=<path>` /
`SET GLOBAL clone_ssl_ca` bila `VerifyIdentity`.

- [ ] **Step 1: Tulis test** (testmysql): `TestTLSRequestInstallReload` (sertifikat dari `MySQLCA` uji dipasang; `SHOW STATUS LIKE 'Ssl_server_not_after'` berubah; sambungan `tls=VERIFY_IDENTITY` ke 127.0.0.1 berhasil bila SAN memuatnya), `TestTLSInstallRollbackOnBadCert` (cert yang tidak cocok dengan kunci → error, berkas lama kembali, MySQL masih melayani TLS lama), `TestReplicationVerifiesSourceCert` (dua mysqld: sumber dengan sertifikat CA uji → IO thread Yes; sumber dengan sertifikat otomatis + verify → `Last_IO_Error` menyebut sertifikat), `TestDumpVerifyIdentityArgs`, `TestPreflightRequiresConnectionAdmin` (akun tanpa hak itu → `missing_grants` memuat `CONNECTION_ADMIN`).
- [ ] **Step 2: Jalankan test, pastikan gagal.**
- [ ] **Step 3: Implementasi.**
- [ ] **Step 4: Jalankan test** — `go test ./internal/agent/drivers/mysql/ -run 'TLS|Replication|Preflight' -timeout 20m`.
- [ ] **Step 5: Commit** — `git commit -m "feat(agent): MySQL server certificates from the panel CA, verified replication and dumps"`

---

### Task 7: Orkestrator dan web: job `mysql_tls`, langkah TLS di Siapkan master/replika, "Pasang sertifikat MySQL", "Perketat sambungan replikasi", `tls_expiring`

**Files:**
- Create: `internal/panel/orchestrator/tls.go`
- Modify: `internal/panel/orchestrator/runner.go` (`KindMySQLTLS = "mysql_tls"`, `KindTightenTLS = "tighten_tls"`,
  `jobState.TLSVerify bool`), `prepare_master.go` (langkah "Pasang sertifikat MySQL" setelah konfigurasi),
  `create_replica.go` dan `resync.go` (langkah TLS di replika; `configureReplication`/`dumpFrom`/`cloneFrom` memakai
  verifikasi bila master punya `server_tls`; `SetReplicaTLSVerified`), `change_databases.go` (parameter dump yang sama),
  `internal/panel/panel.go` (CA MySQL dimuat; `Deps.MySQLCA`; pemeriksaan harian kedaluwarsa → job `mysql_tls` otomatis),
  `internal/panel/monitor/alerts.go` (`KindTLSExpiring = "tls_expiring"`, Peringatan, buka < 14 hari, tutup saat diperpanjang),
  `internal/panel/notify/message.go` (nama jenis), `internal/panel/web/handlers_mysql.go` (`POST /servers/{id}/mysql-tls`,
  `POST /masters/{id}/tighten-tls`, keduanya `requireConfirm`), `web/templates/master_detail.html` (kotak "Perketat sambungan
  replikasi" bila master tanpa `server_tls` atau ada replika `tls_verified=0`; peringatan "Sambungan replikasi belum
  diverifikasi"), `server_detail.html` (status sertifikat MySQL + tombol), `README.md`
- Test: `internal/panel/orchestrator/tls_test.go`, `internal/panel/web/web_mysql_jobs_test.go`, `internal/panel/monitor/alerts_test.go`

**Interfaces:**
- Consumes: Task 6 perintah agent dan field proto; Task 1 `server_tls`, `tls_verified`; Task 4 `MySQLCA`; Task 2 `requireConfirm`.
- Produces:

```go
func (r *Runner) StartMySQLTLS(ctx context.Context, serverID int64) (string, error)       // job mysql_tls: tlsRequest → IssueMySQLCert(hosts: address, source_host bila master) → tlsInstall{VerifyHost: address} → SetServerTLS
func (r *Runner) StartTightenTLS(ctx context.Context, masterServerID int64) (string, error) // job tighten_tls: mysql_tls di master, lalu per replika: mysql_tls (sertifikat sendiri + CA), stopReplica, configureReplication{VerifyServerCert, SSLCA}, startReplica, SetReplicaTLSVerified(true); replika offline → langkah "dilewati, ulangi nanti" (tidak menggagalkan job)
func (jr *jobRun) mysqlTLSSteps() []step                                                     // dipakai ulang oleh prepare_master dan create_replica
```

- [ ] **Step 1: Tulis test** `TestMySQLTLSJobInstallsAndRecords` (hub palsu: urutan perintah `tlsRequest`→`tlsInstall`, `server_tls` terisi), `TestPrepareMasterIncludesTLSStep`, `TestCreateReplicaVerifiesWhenMasterHasCert` (tanpa `server_tls` master → `VerifyServerCert=false`, `tls_verified=0`, log menyebut "belum diverifikasi"), `TestTightenTLSSkipsOfflineReplica`, `TestTLSExpiringAlert` (jam palsu 13 hari sebelum NotAfter → buka; setelah `SetServerTLS` baru → tutup), `TestDailyRenewalStartsJob` (29 hari sebelum → job dibuat sekali), handler web: tombol butuh token, kotak tampil sesuai kondisi.
- [ ] **Step 2: Jalankan test, pastikan gagal.**
- [ ] **Step 3: Implementasi.**
- [ ] **Step 4: Jalankan test** — `go test ./internal/panel/... -race`.
- [ ] **Step 5: Commit** — `git commit -m "feat(orchestrator): MySQL TLS jobs, verified replication setup, tls_expiring alert"`

---

### Task 8: Panel non-root, agent lokal terpisah, `install.sh` dan upgrade idempoten

**Files:**
- Create: `scripts/systemd/dbpanel-panel.service` (diperbarui), `internal/agent/localpair.go` (membaca token lokal)
- Modify: `scripts/install.sh` (user `dbpanel`, tata letak 5.8, upgrade, pasangan agent lokal), `scripts/install_test.sh`,
  `internal/panel/panel.go` (`Options` bawaan `DataDir=/var/lib/dbpanel/panel`, `ConfigDir=/etc/dbpanel/panel`; tidak
  memaksa 0700 pada induk; menulis `local-pair-token` saat baris server lokal belum punya agent berpasangan:
  `cert_serial=='local'` atau `paired=0`), `internal/panel/localagent.go` (hapus `attachLocal`; `ensureLocalServer`
  tetap; `MarkPaired` lokal memakai `PublicHost` sebagai alamat), `internal/panel/servers.go`,
  `internal/panel/agentapi/pair.go` (server lokal: alamat = `PublicHost`, bukan IP jarak jauh), `internal/panel/cli.go`
  (`panel` menolak berjalan sebagai root kecuali `--allow-root`, untuk test), `internal/agent/cli.go`
  (`pair --token-file PATH` membaca `token\npin\n`, menghapus berkas setelah sukses), `cmd/dbpanel/main_test.go`,
  `README.md` ("Pasang panel", "Pembaruan": panel dulu lalu agent; "Keamanan": panel non-root)
- Test: `scripts/install_test.sh` (kasus pasang baru, upgrade dari tata letak root, dijalankan dua kali, terputus setelah
  pindah berkas), `internal/panel/panel_test.go` (`TestLocalServerPairsThroughToken`, `TestEmbeddedAgent*` dihapus),
  `internal/agent/cli_test.go`

**Interfaces:**
- Consumes: Task 5 (pasangan berpin; `PublicURL` https).
- Produces: berkas `/etc/dbpanel/panel/local-pair-token` (0600 `dbpanel`) berisi `<token>\n<pin>\n`, umur token 15 menit, dibuat ulang tiap start selama server lokal belum berpasangan. `install.sh`: `useradd --system --home /var/lib/dbpanel/panel --shell /usr/sbin/nologin dbpanel`; urutan upgrade: stop panel → buat user → pindahkan `panel.db`, `bin/`, `ca.*`, `panel.key`, `admin-initial-password` ke subfolder dan `chown -R dbpanel` → tulis unit → start panel → tunggu token (≤ 60 s) → `mv` ke `/etc/dbpanel/local-pair-token` (0600 root) → tulis unit agent → `systemctl enable --now dbpanel-agent` (agent menjalankan `pair --token-file` sendiri saat start bila berkas ada dan `agent.yaml` belum ada). Dijalankan ulang: tiap langkah memeriksa keadaan dulu.

- [ ] **Step 1: Tulis test** `install_test.sh`: kasus `fresh_nonroot` (unit `User=dbpanel`, proses panel bukan root, agent lokal berpasangan: `curl -k https://127.0.0.1:8080/login` 200 dan log panel "agent online" untuk server lokal), `upgrade_from_root` (siapkan tata letak lama dengan panel.db dan ca.key; sesudah upgrade: berkas di subfolder, pemilik `dbpanel`, `panel.db` sama hash-nya, pin `/etc/dbpanel/role` tidak berubah), `rerun_idempotent`, `interrupted_after_move` (simulasi: jalankan sampai sebelum start agent lalu jalankan lagi). Go: `TestLocalServerPairsThroughToken` (panel di dir sementara menulis token; `agent.Pair` dengan `--token-file` memasangkan; baris server lokal `address==PublicHost`, `cert_serial` bukan "local"), `TestPanelRefusesRootWithoutFlag`.
- [ ] **Step 2: Jalankan test, pastikan gagal.**
- [ ] **Step 3: Implementasi.** `ErrLocalServer` tetap. Dokumentasi README.
- [ ] **Step 4: Jalankan test** — `bash scripts/install_test.sh` dan `go test ./internal/panel/ ./internal/agent/ ./cmd/...`.
- [ ] **Step 5: Commit** — `git commit -m "feat(install): panel runs as user dbpanel with a separate local agent"`

---

### Task 9: Integrasi end-to-end, README, graph

**Files:**
- Modify: `internal/panel/e2e_helpers_test.go` (HTTPS berpin, agent lokal nyata bila perlu), `internal/panel/e2e_replica_test.go`
  (`TestE2ECreateReplica` memverifikasi `tls_verified=1` dan `SHOW REPLICA STATUS` `Source_SSL_Verify_Server_Cert: Yes`;
  resync dengan token konfirmasi), tambah `TestE2EHTTPS` (sertifikat otomatis, `/sertifikat` lewat HTTP, 301, cookie Secure,
  pasang agent lewat perintah pasang berpin), `README.md`, `graphify-out/` (`graphify update .`)
- Test: `make test`, `make test-e2e`

- [ ] **Step 1: Perluas e2e** sesuai daftar; jalankan `make test-e2e`, pastikan lulus.
- [ ] **Step 2: `make test`** (≈20 menit) lulus; `go vet ./...` bersih.
- [ ] **Step 3: README** memuat: HTTPS dan halaman sertifikat (dengan kalimat untuk awam), perintah pasang agent baru, panel non-root dan urutan pembaruan, hak `CONNECTION_ADMIN`, `FLUSH_TABLES` dan versi minimum, pin sumber yang dipertahankan.
- [ ] **Step 4: `graphify update .`**, commit — `git commit -m "test(e2e): HTTPS, pinned pairing, verified replication; docs for Plan 4a"`

## Catatan untuk pelaksana

- Jalankan Task 1–4 sebelum Task 5; Task 5 sebelum 8; Task 6 sebelum 7.
  Task 3 bebas.
- `make test-e2e` membutuhkan mysqld lokal (lihat README "Menjalankan
  test"); test MySQL nyata memakan menit, jalankan dengan `-run` saat
  iterasi.
- Jangan ubah `audit_log` di rencana ini (hash chain di 4c).
