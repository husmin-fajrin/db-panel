# Rencana Implementasi 5b-1: Mode Ketat dan Perbaikan Agent

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Server yang dikarantina tidak bisa dilepas, dibuang datanya, atau
diganti datanya dari panel tanpa perintah root di server itu; `set-role`
tetap bisa diandalkan saat berkas rusak; kunci start dibaca seperti MySQL
membacanya; hasil start `late` tidak hilang saat agent restart; dan
penjaga agent tidak pernah menunggu panel.

**Architecture:** Agent mendapat gerbang mode ketat di `commands.Registry`
(dipasang driver MySQL) yang, saat pin peran `quarantined`, hanya
meloloskan perintah di daftar izin; perintah terlindung butuh berkas
`rebuild-approval` yang hanya ditulis CLI `dbpanel agent approve <kode>`
dari berkas `rebuild-request` yang dibuat agent. Kode galat baru
`needs_local_approval` membawa penolakan itu ke panel. `set-role` tidak
lagi memuat kredensial, bertanya pada server yang dikarantina, dan
membersihkan pin sumber serta berkas persetujuan. `--print-defaults`
dijalankan dengan `HOME` akun layanan MySQL dan pengurai mengenali awalan
opsi. Hasil start terburuk disimpan di `/var/lib/dbpanel/start-result` dan
dibawa `HealthSnapshot`. Semua kejadian penjaga masuk antrean terbatas yang
dikuras satu goroutine pengirim. Di panel: peringatan start dibuka dari
snapshot, "Hapus server" tidak lagi melepas karantina, dan form bangun
ulang menampilkan serta menegakkan persetujuan di server.

**Tech Stack:** Go 1.26, `CGO_ENABLED=0`, tanpa dependensi baru; SQLite
(modernc); html/template + Tailwind v4/daisyUI v5 (`bash
scripts/tailwind.sh`), Alpine CSP build; harness
`internal/agent/drivers/mysql/testmysql` (mysqld sungguhan, skip bila
tidak terpasang); e2e `internal/panel/e2e_*_test.go`.

**Spec:** `docs/superpowers/specs/2026-11-12-rencana-5b-mode-ketat-gerbang-prasyarat-design.md`
— rencana ini hanya 5b-1: bagian 5.1–5.6, dan bagian 5.11 (aturan kata
untuk teks baru), 5.12 (protokol) dan 5.13 (verifikasi) sejauh menyangkut
5.1–5.6. Bagian 5.7–5.10 dan penggantian nama di 5.11 milik 5b-2. Bagian
9 mengikat. Kode diperiksa pada `main` `a090363`.

## Global Constraints

- Go 1.26, `CGO_ENABLED=0`, tanpa dependensi baru. `crypto/rand` untuk kode.
- Bahasa: teks untuk operator (UI, keluaran CLI, pesan galat yang tampil
  ke operator, README) berbahasa Indonesia; kode, komentar, nama test dan
  pesan commit berbahasa Inggris. Pesan log agent (slog) berbahasa Inggris
  seperti yang ada.
- Aturan kata teks baru (spek 5.11): pakai nama yang sudah dipakai panel
  ("baca-saja", "karantina", "kunci start", "daftar replika", "daftar port
  replika", "Bangun ulang sebagai replika"); istilah Inggris dalam kurung
  pada penyebutan pertama di satu layar atau panduan, misalnya "baca-saja
  (read-only)", "dikarantina (quarantined)", "kunci start (start lock)".
  Teks lama tidak diubah kecuali yang disebut task. Semua teks baru ada di
  task-nya dan sekali lagi di "Teks baru untuk dibaca pemilik"; dipakai
  persis.
- Berkas di `/etc/dbpanel` (direktori pin): `rebuild-request` dan
  `rebuild-approval`. Kunci JSON berbahasa Inggris: permintaan `code`,
  `source`, `created`; persetujuan `source`, `addr`, `approved`,
  `expires`. Aturan kepercayaan sama dengan berkas pin (`readTrustedFile`:
  berkas biasa, milik root atau pengguna agent, tidak bisa ditulis
  grup/lain) dan ditulis atomik (`writePin`: temp + rename + fsync
  direktori, mode 0644).
- Nilai tetap: permintaan berlaku **30 menit**, kode **enam angka** dari
  `crypto/rand` (nol di depan dipertahankan); persetujuan berlaku **24
  jam**; persetujuan dengan `approved` lebih dari **5 menit** di masa
  depan tidak berlaku.
- Perintah agent baru `mysql.rebuildApproval` (tanpa parameter); kode galat
  baru `needs_local_approval`.
- CLI: `dbpanel agent approve <kode>`, `dbpanel agent approve batal`;
  kata konfirmasi `ya`; `set-role … --ya` melewati pertanyaan yang baru
  ada pada server yang dikarantina.
- Hasil start terburuk di `/var/lib/dbpanel/start-result` (nama berkas
  `start-result`); urutan terburuk→terbaik: `late`, start gagal
  (`failed`), `bypassed`, `ok`.
- Protokol (spek 5.12): field baru `omitempty`, dan pointer bila "tidak ada"
  harus dibeda dari nol. Panel lama dengan agent baru: penolakan mode
  ketat tampil sebagai teks yang memuat perintah lengkapnya. Panel baru
  dengan agent lama: `unknown_command` untuk `mysql.rebuildApproval` →
  bangun ulang ditolak dengan "Agent perlu dimutakhirkan".
- Gerbang setiap task: test paket yang disentuh selama bekerja; sebelum
  task dinyatakan selesai `make test` (±40 menit, termasuk shellcheck;
  `pip install shellcheck-py` bila shellcheck tidak ada) dan, untuk task
  yang menyentuh panel atau e2e, `make test-e2e` (±9 menit). Jangan pernah
  dua run test bersamaan. Test mysqld sungguhan butuh `-timeout` panjang
  (`-timeout 30m`). Test yang dikenal flaky: `TestWSLogsReplacedConnection`
  (jalankan ulang sekali; bila gagal lagi, selidiki).
- Setelah template berubah: `bash scripts/tailwind.sh`. CSP ketat (tanpa
  script/style inline).
- Commit: bahasa Inggris, awalan konvensional, diakhiri baris trailer yang
  diberikan instruksi sesi pelaksana; bila tidak ada, salin dari commit
  terakhir: `TRAILERS="$(git log -1 --format=%B | git interpret-trailers --parse)"`
  lalu `git commit -m "<subjek>" -m "$TRAILERS"`. Jangan menulis nama
  model, ID agent, atau path direktori kerja lokal yang diabaikan git di
  rencana, kode, atau berkas repo lain.
- Setelah mengubah kode: `graphify update .` (`graphify-out/` ada di
  `.gitignore`, tidak ikut commit).

## Review Focus

1. **Akun layanan MySQL dengan `~/.my.cnf` atau home yang disembunyikan
   `ProtectHome`.** Operator menaruh `init-file` di `~/.my.cnf` akun
   `mysql`, atau akun layanan punya home di `/home/...` yang tidak terlihat
   agent: kunci start harus dilaporkan **tidak terpasang** dengan alasan
   yang menyebut berkas/sebabnya, bukan "Terkunci". Test
   `TestServiceAccountMyCnfBreaksStartLock` dan
   `TestPrintDefaultsRunsWithServiceHome` di Task 6.
2. **Agent restart di antara keputusan `late` dan pengirimannya.** Panel
   tetap membuka `master_start_late` tepat sekali dari snapshot, tidak
   membukanya lagi setelah "Periksa data tertinggal" menutupnya, dan
   membukanya lagi untuk start `late` yang lebih baru. Test
   `TestStartResultSurvivesRestart` (Task 7),
   `TestStartLateOpensFromSnapshotOnce` dan
   `TestStartLateFromSnapshotNotReopenedAfterLeftoverCheck` (Task 9),
   `TestE2EPromoteEmergency` dengan kejadian start yang dibuang (Task 12).
3. **Versi campuran.** Panel lama + agent baru: penolakan memuat `sudo
   dbpanel agent approve <kode>` utuh; "Hapus server" panel lama
   (`releaseRole`) ditolak dan server tetap terkunci. Panel baru + agent
   lama: bangun ulang ditolak dengan "Agent perlu dimutakhirkan" dan
   "Hapus server" tidak memanggil `releaseRole` sama sekali. Test
   `TestStrictRefusalCarriesFullCommand` (Task 3),
   `TestRebuildRefusedOnOldAgent` (Task 11),
   `TestDeleteQuarantinedNeverReleasesRole` (Task 10).
4. **Persetujuan yang tidak lagi cocok:** sumber diganti `set-source`, nama
   sumber kini teresolusi ke alamat lain, kedaluwarsa, jam server mundur
   sehingga `approved` jauh di masa depan, atau `set-role` sesudahnya:
   semua menolak lagi dengan kode baru. Test
   `TestRebuildApprovalValidity` (Task 2),
   `TestStrictApprovalVoidedBySourceDNSExpiryAndSetRole` (Task 3),
   `TestStrictModeWithMySQL` (Task 3).
5. **Soket panel yang tidak mengalir dan `set-role` dari skrip tanpa
   terminal.** Penjaga tetap berputar tiap `GuardEvery` dan `promoteMu`
   bebas selama pengiriman; `set-role master` pada server dikarantina
   dengan stdin kosong membatalkan (keluar 1) alih-alih menggantung. Test
   `TestGuardRoundsNotDelayedByNonDrainingPanel` dan
   `TestGuardNeverSendsUnderPromoteMu` (Task 8),
   `TestSetRoleOnQuarantinedAsks` (Task 5).

## Bukti kunci start di lingkungan layanan (spek 5.4)

**Dari perencana (mysqld 8.0.46-0ubuntu0.24.04.4, tanpa server berjalan):**

- `mysqld --defaults-file=F --print-defaults` dengan F berisi
  `loose-init-file=/x/a.sql`, `skip-init-file`, `init_file=/x/b.sql`,
  `loose_user=nobody` mencetak persis
  `--loose-init-file=/x/a.sql --skip-init-file --init_file=/x/b.sql --loose_user=nobody`:
  awalan dicetak apa adanya, dan pemisah sesudah awalan bisa `-` atau `_`.
  Pengurai hari ini (`readEffectiveOptions`) tidak mengenali satu pun dari
  tiga argumen pertama sebagai `init_file`, dan tidak mengenali
  `--loose_user`.
- `HOME=<dir> mysqld --print-defaults` (tanpa `--defaults-file`) membaca
  `<dir>/.my.cnf`: berkas berisi `[mysqld]` / `init-file=/home/x.sql`
  muncul sebagai `--init-file=/home/x.sql`. Daftar bawaan:
  `/etc/my.cnf /etc/mysql/my.cnf ~/.my.cnf`. Jadi `HOME` proses agent
  (root, `/root`, disembunyikan `ProtectHome=yes` di
  `scripts/systemd/dbpanel-agent.service:19` dan
  `web/templates/agent-install.sh.tmpl:120`) menentukan `~/.my.cnf` mana
  yang terbaca; `HOME` akun layanan tidak pernah dipakai.

**Diisi pelaksana di Task 6 Step 1 (mysqld sungguhan yang dijalankan):**

| Bentuk di berkas opsi | `--print-defaults` mencetak | `@@global.init_file` setelah start | Isi init file dijalankan? | Diperlakukan agent |
|---|---|---|---|---|
| `init_file=<agent>` terakhir | | | | terkunci |
| `init_file=<agent>` lalu `loose-init-file=/op/x.sql` | | | | tidak terkunci |
| `init_file=<agent>` lalu `skip-init-file` | | | | tidak terkunci |
| `skip-init-file` lalu `init_file=<agent>` | | | | (catat) |
| `init_file=<agent>` lalu `disable-init-file` / `maximum-init-file=…` | | | | tidak terkunci |
| `~/.my.cnf` akun layanan berisi `init-file=/op/y.sql` | | | | tidak terkunci |

Catat juga: versi mysqld, perintah persis yang dijalankan, dan untuk tiap
bentuk yang terbukti tidak berpengaruh, satu kalimat "tetap dianggap tidak
terkunci karena …" (spek 5.4: gagal-tertutup).

---

## File Structure

| Berkas | Tanggung jawab |
|---|---|
| `internal/proto/messages.go` | `CodeNeedsLocalApproval` |
| `internal/proto/mysql.go` | `RebuildApprovalResult`; `QuarantineStartFailed` |
| `internal/proto/health.go` | `StartResultState`, `HealthSnapshot.StartResult` |
| `internal/agent/commands/registry.go` | `LocalActionError`, `Gate`, `Registry.SetGate`, pemetaan ke `needs_local_approval` |
| `internal/agent/drivers/mysql/pins.go` | `releaseRole`/`Release` ditolak pada `quarantined`; `unlockPromote` di tabel peran; `SetRole` membersihkan berkas persetujuan dan, untuk `master`, pin sumber |
| `internal/agent/drivers/mysql/rebuild_approval.go` (baru) | jenis, baca/tulis, keabsahan permintaan dan persetujuan; alamat untuk CLI |
| `internal/agent/drivers/mysql/strict.go` (baru) | tabel kelas perintah, gerbang mode ketat, `mysql.rebuildApproval` |
| `internal/agent/drivers/mysql/driver.go` | pasang gerbang dan perintah baru; field baru Driver |
| `internal/agent/drivers/mysql/quarantine.go` | `settleRebuilt` menghapus berkas persetujuan dan hasil start |
| `internal/agent/drivers/mysql/startlock_guard.go` | `ReleaseStartLock` melapor ditemukan/tidak; hasil start dicatat sebelum dikirim; antrean menggantikan `pendingStart` |
| `internal/agent/drivers/mysql/startlock.go` | `HOME` akun layanan, awalan opsi, aturan kunci |
| `internal/agent/drivers/mysql/host.go`, `realhost.go` | `Host.ExecEnv` |
| `internal/agent/drivers/mysql/start_result.go` (baru) | berkas hasil start terburuk |
| `internal/agent/drivers/mysql/event_queue.go` (baru) | antrean kirim terbatas dan goroutine pengirim |
| `internal/agent/drivers/mysql/promote_lock.go` | `emit` → antrean; `keepLock` |
| `internal/agent/drivers/mysql/health.go` | `Snapshot` membawa `StartResult` |
| `internal/agent/monitor/monitor.go` | `EventSender`; `Run` menjalankan pengirim |
| `internal/agent/cli.go` | `approve`, `approve batal`, `set-role` baru, pelepasan kunci start tanpa kredensial, `RunCLI` |
| `internal/panel/monitor/promote.go` | peringatan start dari snapshot; tanpa peringatan ganda |
| `internal/panel/orchestrator/controls.go` | "Hapus server" tanpa `releaseRole` pada server dikarantina |
| `internal/panel/orchestrator/approval.go` (baru) | `RebuildApproval`, penegakan, pemetaan galat |
| `internal/panel/orchestrator/create_replica.go`, `runner.go`, `tls.go` | penegakan di `StartCreateReplica`, `Retry`, `Approve`, `callWithin`; TLS melewati server dikarantina |
| `internal/panel/web/handlers_oldmaster.go`, `handlers_mysql.go`, `handlers_jobs.go`, `web/templates/replica_new.html` | kotak "Persetujuan di server", tombol, audit, teks dialog hapus |
| `README.md` | `approve`, `set-role` pada server dikarantina, server dihapus saat dikarantina, melepas kunci start dengan tangan, laporan start yang tidak hilang |

---

### Task 1: Kode galat `needs_local_approval`; `releaseRole` ditolak pada `quarantined` (5.1)

**Files:**
- Modify: `internal/proto/messages.go:16-20`
- Modify: `internal/agent/commands/registry.go` (`ParamError` ~L27-30, `Dispatch` ~L91-128)
- Modify: `internal/agent/drivers/mysql/pins.go` (`roleRefuses` L97-118, `Release` L523-535, `Allow` L693-705, komentar L22-35)
- Test: `internal/agent/commands/registry_test.go`, `internal/agent/drivers/mysql/pins_test.go`

**Interfaces:**
- Produces:

```go
// internal/proto
const CodeNeedsLocalApproval = "needs_local_approval"

// internal/agent/commands
// LocalActionError refuses a command until root acts on the server itself;
// Dispatch answers it with proto.CodeNeedsLocalApproval and Msg. Checked
// before ParamError.
type LocalActionError struct{ Msg string }
func (e *LocalActionError) Error() string

// internal/agent/drivers/mysql (pins.go)
// localPinError is a refusal by the pins that needs root on the server:
// it matches *commands.LocalActionError and kind (ErrRolePinned or ErrSourcePinned).
type localPinError struct{ lae *commands.LocalActionError; kind error }
func (e *localPinError) Error() string
func (e *localPinError) Unwrap() []error
func quarantineReleaseRefusal() error // teks A1, kind ErrRolePinned
// roleRefuses[roleQuarantined] += "mysql.releaseRole", "mysql.unlockPromote"
// Allow: role quarantined && cmd == "mysql.releaseRole" → quarantineReleaseRefusal() (sebelum roleRefusal)
// Release(): role master → roleRefusal (seperti sekarang); role quarantined → quarantineReleaseRefusal()
```

- Kompatibilitas: panel lama memanggil `releaseRole` saat "Hapus server" dan
  mencatat penolakannya sebagai best effort (`controls.go:178-181`); baris
  server tetap dihapus, server tetap dikarantina. Panel lama tidak mengenal
  kode baru; `proto.Error` tetap membawa teksnya.

- [ ] **Step 1: Tulis test.**

```go
// registry_test.go
func TestLocalActionErrorCode(t *testing.T) {
	reg := NewRegistry()
	reg.Register("x", func(context.Context, json.RawMessage) (any, error) {
		return nil, fmt.Errorf("wrapped: %w", &LocalActionError{Msg: "jalankan di server ini: sudo dbpanel agent approve 123456"})
	})
	resp := reg.Dispatch(context.Background(), proto.Request{ID: "1", Cmd: "x"})
	// resp.OK == false, resp.Error.Code == proto.CodeNeedsLocalApproval
	// resp.Error.Message == "jalankan di server ini: sudo dbpanel agent approve 123456"
}

// pins_test.go
// wantLocalRefusal is wantRefused for code needs_local_approval.
func wantLocalRefusal(t *testing.T, what string, r proto.Response, parts ...string)

func TestReleaseRoleRefusedOnQuarantined(t *testing.T) {
	dir := pinDir(t, "quarantined", "10.0.0.1:3306")
	r := newPinRig(t, nil, dir)
	wantLocalRefusal(t, "releaseRole", r.dispatch("mysql.releaseRole", nil),
		"server ini dikarantina (quarantined)", "perintah mysql.releaseRole ditolak",
		"sudo dbpanel agent set-role unassigned", "sudo dbpanel agent set-role master")
	// mysql.LoadPins(dir).Release(): errors.Is(err, mysql.ErrRolePinned) and errors.As(err, **commands.LocalActionError)
	// pinFile(role) == "quarantined", pinFile(source) == "10.0.0.1:3306"
	// mysql.unlockPromote on quarantined: wantRefused(..., "server ini dipin sebagai quarantined")
}

// R-109 (spek 5.13): no panel path from quarantined to writable; mysqld sungguhan.
func TestNoPanelPathFromQuarantinedToWritable(t *testing.T) {
	inst := testmysql.Start(t, testmysql.Options{})
	// SET PERSIST read_only=ON, super_read_only=ON lewat rootDB
	dir := pinDir(t, "quarantined", "10.0.0.1:3306")
	r := newPinRig(t, inst.Host(), dir)
	// releaseRole → needs_local_approval; then
	// setReadOnly{On:false} → !OK, message contains "quarantined";
	// configure{Role:"master"} → !OK, message contains "quarantined"
	//   (bad_params dari tabel peran sekarang; setelah Task 3 needs_local_approval dari gerbang — test hanya menegaskan !OK + "quarantined")
	// @@global.super_read_only == 1 dan pinFile(role) == "quarantined" setelah ketiganya
}
```

  Ubah `TestQuarantinedRefusalTable` (pins_test.go:751): blok terakhir
  "Released like a replica" menjadi `Release()` → `ErrRolePinned` dan pin
  tidak berubah. Tambahkan `"mysql.unlockPromote"` ke daftar yang ditolak
  pada `quarantined`.

- [ ] **Step 2: Jalankan, pastikan gagal.**
  `go test ./internal/agent/commands/ ./internal/agent/drivers/mysql/ -run 'LocalActionError|ReleaseRole|QuarantinedRefusalTable|NoPanelPath' -timeout 30m`
  Expected: FAIL (`LocalActionError` tidak ada; `Release` pada quarantined berhasil).

- [ ] **Step 3: Implementasi.** `CodeNeedsLocalApproval` di `messages.go`;
  `LocalActionError` dan cabang `errors.As` di `Dispatch` sebelum
  `ParamError`; `localPinError`, `quarantineReleaseRefusal` dan perubahan
  `roleRefuses`/`Allow`/`Release` di `pins.go`. Teks A1 persis:

```go
"server ini dikarantina (quarantined); perintah mysql.releaseRole ditolak. Karantina hanya lepas lewat bangun ulang " +
	"yang selesai, atau dengan perintah di server ini: sudo dbpanel agent set-role unassigned (atau sudo dbpanel agent " +
	"set-role master bila server ini akan menjadi master lagi)"
```

  Perbarui komentar paket di `pins.go:22-35`: celah SA-1 (releaseRole)
  tertutup; SA-2 (data dibuang lewat sumber yang dipin) ditutup Task 3.

- [ ] **Step 4: Jalankan Step 2 dan `go test ./internal/agent/... -race -short`.** Expected: PASS.
- [ ] **Step 5: Commit** — `git commit -m "feat(agent): refuse releaseRole on a quarantined server with needs_local_approval" -m "$TRAILERS"`

---

### Task 2: Berkas permintaan dan persetujuan bangun ulang (5.2, logika berkas)

**Files:**
- Create: `internal/agent/drivers/mysql/rebuild_approval.go`, `internal/agent/drivers/mysql/rebuild_approval_test.go`
- Modify: `internal/agent/drivers/mysql/export_test.go` (ekspor `CheckApproval`)

**Interfaces:**
- Consumes: `readJSON`/`writeJSON` (`promote_files.go:343-364`), `removeFile`,
  `ParseSource`, `Pins.Role`, `Pins.Source`, `Pins.SourceDial`, `lookupIP`
  (`quarantine.go:334`), `peerTimeout` (`peer.go:26`).
- Produces:

```go
// internal/agent/drivers/mysql/rebuild_approval.go
const (
	RebuildRequestFile  = "rebuild-request"
	RebuildApprovalFile = "rebuild-approval"
	RebuildRequestTTL   = 30 * time.Minute
	RebuildApprovalTTL  = 24 * time.Hour
	approvalFutureSlack = 5 * time.Minute
)

// RebuildRequest is what the agent asks root to approve: the code the
// panel shows and the source pin when it was made. Written by the agent only.
type RebuildRequest struct {
	Code    string    `json:"code"`    // six digits
	Source  string    `json:"source"`  // host:port
	Created time.Time `json:"created"`
}

// RebuildApproval is root's approval, written by "dbpanel agent approve" only.
type RebuildApproval struct {
	Source   string    `json:"source"`   // host:port of the source pin approved
	Addr     string    `json:"addr"`     // the IP address shown to the operator
	Approved time.Time `json:"approved"`
	Expires  time.Time `json:"expires"`  // Approved + RebuildApprovalTTL
}

func NewRebuildApproval(source, addr string, now time.Time) RebuildApproval
func newRebuildCode() (string, error) // crypto/rand, fmt.Sprintf("%06d", n), n in [0, 1e6)
func (r RebuildRequest) validFor(source string, now time.Time) bool
	// sameSource(r.Source, source) && now.Before(r.Created.Add(RebuildRequestTTL)) && !r.Created.After(now.Add(approvalFutureSlack))
// checkApproval is the rule of spec 5.2; ips are what the source's host
// resolves to now (the host itself when it is an IP). nil means valid; an
// error's text says why in Indonesian (teks A5).
func checkApproval(a RebuildApproval, role, source string, ips []net.IP, now time.Time) error
func sameSource(a, b string) bool // ParseSource both; host EqualFold, port equal

func (p *Pins) ReadRebuildRequest() (RebuildRequest, bool, error)
func (p *Pins) ValidRebuildRequest(now time.Time) (RebuildRequest, bool, error) // valid for the current source pin
// EnsureRebuildRequest returns the valid request, or writes a new one (new
// code) for the current source pin. Refused unless the role is quarantined
// with a source pin.
func (p *Pins) EnsureRebuildRequest(now time.Time) (RebuildRequest, error)
func (p *Pins) RemoveRebuildRequest() error
func (p *Pins) ReadRebuildApproval() (RebuildApproval, bool, error)
func (p *Pins) WriteRebuildApproval(a RebuildApproval) error
func (p *Pins) ClearRebuild() error // removes both files; none is not an error
// CheckRebuildApproval reads the approval and checks it against the pins
// and the source host's addresses now (lookupIP, peerTimeout).
func (p *Pins) CheckRebuildApproval(ctx context.Context, now time.Time) (RebuildApproval, error)
// RebuildApprovalAddr is the address "approve" records and shows: the
// verified address (SourceAddrFile) when there is one, the host when it is
// an IP, else the first address the host resolves to (IPv4 before IPv6,
// each sorted); others are the remaining resolved addresses.
func (p *Pins) RebuildApprovalAddr(ctx context.Context) (addr string, others []string, err error)
```

- Alasan tidak berlaku (teks A5, persis; `%s` waktu dalam format
  `2006-01-02 15:04 MST` waktu lokal):
  - peran: `"server ini tidak dikarantina (peran: %s)"`
  - sumber: `"persetujuan untuk sumber %s, bukan sumber yang dipin sekarang (%s)"`
  - alamat: `"nama sumber %s sekarang mengarah ke alamat lain, bukan alamat yang disetujui (%s)"`
  - resolusi gagal: `"alamat untuk nama sumber %s tidak ditemukan (DNS): %v"`
  - kedaluwarsa: `"persetujuan sudah habis pada %s"`
  - masa depan: `"waktu persetujuan %s lebih dari 5 menit di masa depan; periksa jam server ini"`
  - masa berlaku: `"masa berlaku persetujuan lebih dari 24 jam"`
  - berkas: `"berkas persetujuan tidak bisa dipakai: %v"`

- [ ] **Step 1: Tulis test** (`rebuild_approval_test.go`, unit, tanpa mysqld).

```go
func TestRebuildCodeSixDigits(t *testing.T) // 200 kode: semua ^[0-9]{6}$, tidak semuanya sama

func TestRebuildRequestKeptWhileValid(t *testing.T) {
	dir := pinDir(t, "quarantined", "db-b:3306")
	p := mustPins(t, dir)
	t0 := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	r1, _ := p.EnsureRebuildRequest(t0)               // Source "db-b:3306", Created t0
	r2, _ := p.EnsureRebuildRequest(t0.Add(29 * time.Minute)) // r2.Code == r1.Code
	r3, _ := p.EnsureRebuildRequest(t0.Add(30 * time.Minute)) // r3.Created == t0+30m (kode baru boleh sama secara kebetulan: bandingkan Created)
	// SetSource("db-c", 3306) → EnsureRebuildRequest: Source "db-c:3306"
	// berkas: mode 0644, kunci JSON tepat {"code","source","created"}
}

func TestRebuildRequestNeedsQuarantinedSource(t *testing.T)
	// replica → error; quarantined tanpa source → error; tidak ada berkas tertulis

func TestRebuildApprovalValidity(t *testing.T) {
	now := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	base := mysql.RebuildApproval{Source: "db-b:3306", Addr: "10.0.0.5", Approved: now.Add(-time.Hour), Expires: now.Add(23 * time.Hour)}
	ips := []net.IP{net.ParseIP("10.0.0.5")}
	// table, via mysql.CheckApproval(a, role, source, ips, now):
	//   base, "quarantined", "db-b:3306", ips                → nil
	//   base, "quarantined", "DB-B:3306", ips                → nil
	//   base, "replica", ...                                 → error containing "tidak dikarantina"
	//   base, ..., "db-c:3306", ...                          → error containing "db-c:3306"
	//   base, ..., ips = [10.0.0.6]                          → error containing "10.0.0.5"
	//   now = base.Expires                                   → error containing "sudah habis"
	//   Approved = now+5m, Expires = Approved+24h            → nil
	//   Approved = now+5m+1s, Expires = Approved+24h         → error containing "di masa depan"
	//   Expires = Approved+24h+1s                            → error containing "lebih dari 24 jam"
}

func TestRebuildFilesTrusted(t *testing.T)
	// WriteRebuildApproval → mode 0644, kunci JSON tepat {"source","addr","approved","expires"};
	// chmod 0666 → ReadRebuildApproval error memuat "chmod 0644"; symlink → error "harus berkas biasa";
	// ClearRebuild menghapus keduanya; ClearRebuild tanpa berkas → nil

func TestRebuildApprovalAddr(t *testing.T)
	// source "10.0.0.5:3306" → "10.0.0.5"; source-addr "10.0.0.9" ditulis → "10.0.0.9";
	// source "db-b:3306" dengan SetResolver palsu → [fd00::5, 10.0.0.7, 10.0.0.6] → addr "10.0.0.6", others ["10.0.0.7","fd00::5"]
```

- [ ] **Step 2: Jalankan, pastikan gagal.** `go test ./internal/agent/drivers/mysql/ -run 'Rebuild' -short`
  Expected: FAIL (tipe dan fungsi tidak ada).
- [ ] **Step 3: Implementasi** di `rebuild_approval.go`. Baca/tulis lewat
  `readJSON`/`writeJSON` pada `p.path(name)`; `CheckRebuildApproval`
  memanggil `p.Role()`, `p.Source()`, resolusi tanpa memegang `p.mu`.
  `export_test.go`: `func CheckApproval(a RebuildApproval, role, source string, ips []net.IP, now time.Time) error { return checkApproval(a, role, source, ips, now) }`.
- [ ] **Step 4: Jalankan** Step 2 dan `go test ./internal/agent/drivers/mysql/ -race -short`. Expected: PASS.
- [ ] **Step 5: Commit** — `git commit -m "feat(agent): rebuild request and approval files with trust checks and validity rules" -m "$TRAILERS"`

---

### Task 3: Mode ketat: daftar izin, `mysql.rebuildApproval`, permintaan dari penolakan (5.2)

**Files:**
- Create: `internal/agent/drivers/mysql/strict.go`, `internal/agent/drivers/mysql/strict_test.go`, `internal/agent/strict_commands_test.go`
- Modify: `internal/agent/commands/registry.go` (`Registry`, `Dispatch`), `internal/agent/drivers/mysql/driver.go` (field L24-159, `Register` L163-191), `pins.go` (`SetRole` L403-415), `quarantine.go` (`settleRebuilt` L439-459), `internal/proto/mysql.go`, `export_test.go`
- Test: `strict_test.go`, `pins_test.go` (helper `approveRebuild`), `quarantine_test.go`, `internal/agent/strict_commands_test.go`

**Interfaces:**
- Consumes: Task 1 `LocalActionError`, `localPinError`; Task 2 seluruh `Pins.*Rebuild*`.
- Produces:

```go
// internal/agent/commands
// Gate is asked before the handler of every registered command (after the
// local policy); a non-nil error is the command's answer.
type Gate func(ctx context.Context, cmd string) error
func (r *Registry) SetGate(g Gate)

// internal/proto
type RebuildApprovalResult struct {
	Required     bool   `json:"required"`                // the role pin is quarantined: protected commands need an approval
	Approved     bool   `json:"approved"`
	RemainingSec int64  `json:"remaining_sec,omitempty"` // approved: until Expires; else until the request's code expires
	AgeSec       int64  `json:"age_sec,omitempty"`       // approved: since Approved
	Code         string `json:"code,omitempty"`          // not approved: the request's code
	Source       string `json:"source,omitempty"`        // host:port the approval is or would be for
	Addr         string `json:"addr,omitempty"`          // approved: the approved address
	Invalid      string `json:"invalid,omitempty"`       // why an approval on file does not hold (teks A5)
}

// internal/agent/drivers/mysql/strict.go
type strictClass int
const (
	strictAllowed     strictClass = iota + 1 // runs without an approval
	strictProtected                          // needs a valid approval; refused with needs_local_approval and a request is made
	strictRoleRefused                        // refused on a quarantined server whatever the approval (roleRefuses)
)
var strictCommands = map[string]strictClass{ /* tabel di bawah, 47 baris */ }
func StrictClassNames() map[string]string // "allowed" | "protected" | "role-refused"; untuk test paket agent
func (d *Driver) strictGate(ctx context.Context, cmd string) error
func (d *Driver) rebuildApprovalState(ctx context.Context, now time.Time) (proto.RebuildApprovalResult, error) // handler mysql.rebuildApproval
// Driver += approvalMu sync.Mutex (EnsureRebuildRequest dalam satu proses);
//           approvalLogged string (kunci persetujuan yang pemakaian pertamanya sudah dicatat: Source+"|"+Approved.Format(RFC3339Nano))
// Register: if d.pins != nil { reg.SetGate(d.strictGate) }; d.registerRebuildApproval(reg)
// Pins.SetRole(role): ClearRebuild() lebih dulu untuk setiap peran; gagal → error, peran tidak ditulis
// settleRebuilt (Driver): setelah pins.settleRebuilt berhasil → d.pins.ClearRebuild() (gagal: warn, karantina tetap berakhir)
// export_test.go: func (d *Driver) SettleRebuilt(host string, port int) error { return d.settleRebuilt(host, port) }
```

- Aturan `strictGate` (urut):
  1. `d.pins.Role() != roleQuarantined` → `nil` (pin yang tidak terbaca:
     perilaku hari ini, `Allow` menolak perintah yang dijaga).
  2. kelas `strictAllowed` atau `strictRoleRefused` → `nil` (handler dan
     tabel peran memutuskan).
  3. kelas lain (termasuk perintah yang tidak ada di tabel) dianggap
     terlindung.
  4. tanpa pin sumber → `localPinError{kind: ErrSourcePinned}` dengan teks
     `unsourcedQuarantineRefusal` yang ada (A4).
  5. `CheckRebuildApproval(ctx, d.now())` sah → catat pemakaian pertama
     (`d.Logger.Info("local rebuild approval used", "cmd", cmd, "source", a.Source, "addr", a.Addr, "approved", a.Approved, "expires", a.Expires)`, sekali per `approvalLogged`) → `nil`.
  6. selain itu → `EnsureRebuildRequest` di bawah `approvalMu` → teks A2;
     bila gagal → teks A3. Keduanya `localPinError{kind: ErrRolePinned}`.

- Teks A2/A3 persis:

```go
fmt.Sprintf("server ini dikarantina (quarantined) mengikuti sumber %s; perintah %s butuh persetujuan root di server ini. "+
	"Jalankan di server ini: sudo dbpanel agent approve %s (kode berlaku %d menit lagi)", req.Source, cmd, req.Code, minutesLeft)
// minutesLeft = ceil(remaining/1m), minimal 1
fmt.Sprintf("server ini dikarantina (quarantined); perintah %s butuh persetujuan root di server ini, "+
	"tetapi permintaan persetujuan tidak bisa dibuat: %v", cmd, err)
```

- `mysql.rebuildApproval`: peran bukan `quarantined` → `{Required:false}`;
  tanpa pin sumber → `ParamError` teks A4 untuk `mysql.rebuildApproval`;
  persetujuan sah → `{Required, Approved, RemainingSec, AgeSec, Source,
  Addr}`; selain itu → `EnsureRebuildRequest` → `{Required, Code, Source,
  RemainingSec, Invalid}` (`Invalid` = teks alasan bila berkas persetujuan
  ada tetapi tidak berlaku, "" bila tidak ada berkas).

- **Daftar perintah yang terdaftar hari ini dan kelasnya** (transkripsikan
  ke `strictCommands`; kolom alasan hanya untuk yang tidak jelas):

| Perintah | Kelas | Alasan |
|---|---|---|
| `probe` | allowed | membaca fakta host |
| `job.status`, `job.log` | allowed | membaca |
| `job.cancel` | allowed | hanya menghentikan pekerjaan agent; tidak mengubah data sendiri |
| `monitor.snapshot`, `monitor.configure` | allowed | `monitor.*` (spek); `Bootstrap` tidak pernah mengubah pin yang ada |
| `mysql.status`, `mysql.inventory`, `mysql.connections`, `mysql.preflight`, `mysql.binlogTail` | allowed | membaca |
| `mysql.connect` | allowed | tidak disebut spek ("koneksi" di spek = `mysql.connections`); hanya menyimpan akun yang terbukti bisa masuk ke MySQL lokal, tidak menyentuh data, dan diperlukan untuk penyelamatan bila akun admin berubah (Keputusan 5) |
| `mysql.probeSource` | allowed | hanya membaca sumber yang dipin |
| `mysql.waitSynced` | allowed | hanya menunggu dan membaca GTID |
| `mysql.leftover`, `mysql.salvage`, `mysql.salvageList`, `mysql.salvageRead` | allowed | penyelamatan |
| `mysql.salvageDelete` | allowed | spek menyebut "hapus berkas salvage"; berkas di luar MySQL, risiko dicatat di Keputusan |
| `mysql.quarantine` | allowed | `requarantine` menolak sumber lain (`quarantine.go:177-188`) |
| `mysql.recordReplicas`, `mysql.rebuildApproval` | allowed | spek |
| `mysql.setReadOnly` | allowed | `{on:true}` hanya mengencangkan; `{on:false}` ditolak tabel peran apa pun persetujuannya |
| `mysql.stopReplica` | allowed | menghentikan applier: justru yang melindungi data dari `DROP` lewat replikasi |
| `mysql.dropDatabases`, `mysql.dumpFrom`, `mysql.cloneFrom` | protected | spek |
| `mysql.configureReplication`, `mysql.setReplicationFilter`, `mysql.startReplica`, `mysql.startUntil`, `mysql.resetReplica` | protected | spek (replikasi) |
| `mysql.configure`, `mysql.restart`, `mysql.install` | protected | spek (konfigurasi, restart, pemasangan) |
| `mysql.createReplUser`, `mysql.createDumpUser`, `mysql.dropUser`, `mysql.sweepDumpUsers` | protected | mengubah akun; bawaan tolak (sweep otomatis agent memanggil driver langsung, bukan lewat registry) |
| `mysql.tlsRequest`, `mysql.tlsInstall`, `mysql.tlsTrust` | protected | mengubah kunci, sertifikat atau CA MySQL; panel melewati server dikarantina untuk perpanjangan (Task 11) |
| `mysql.lockForPromote`, `mysql.promote`, `mysql.repoint` | role-refused | sudah di `roleRefuses[roleQuarantined]` |
| `mysql.unlockPromote` | role-refused | hanya untuk master (`requireMaster`); Task 1 menambahkannya ke tabel peran |
| `mysql.releaseRole` | role-refused | Task 1: `needs_local_approval` dengan perintah `set-role` |

  Jumlah: 47 (6 non-mysql + 41 mysql termasuk `mysql.rebuildApproval`).

- Kompatibilitas: panel lama menerima `needs_local_approval` sebagai galat
  dengan pesan A2 yang memuat perintah lengkap dan kode yang berlaku.

- [ ] **Step 1: Tulis test.**

```go
// pins_test.go
// approveRebuild writes a valid approval for source (an IP host) as "approve" would.
func approveRebuild(t *testing.T, dir, source string)

// strict_test.go (rig: newPinRig(t, nil, pinDir(t, "quarantined", "10.0.0.1:3306")))
func TestQuarantinedStrictModeRefusesProtected(t *testing.T) {
	// untuk setiap perintah protected (tabel pinCase dengan parameter sah,
	// termasuk mysql.install, mysql.restart, mysql.tlsRequest):
	//   wantLocalRefusal(..., "mysql.<cmd> butuh persetujuan root di server ini",
	//     "sudo dbpanel agent approve ", "kode berlaku ")
	// semua pesan memuat kode yang sama == ReadRebuildRequest().Code; Source "10.0.0.1:3306"
}
func TestStrictRefusalCarriesFullCommand(t *testing.T)
	// regexp `sudo dbpanel agent approve [0-9]{6} \(kode berlaku [0-9]+ menit lagi\)$` cocok dengan pesan dropDatabases
func TestQuarantinedStrictModeAllowsListed(t *testing.T)
	// setiap allowed mysql.* tanpa host → wantPastPins atau OK; setReadOnly{On:false} → wantRefused "dipin sebagai quarantined"
func TestStrictApprovalOpensRebuildPath(t *testing.T)
	// approveRebuild(dir, "10.0.0.1:3306"); kasus "rebuild path" dari TestQuarantinedRefusalTable → wantPastPins;
	// dumpFrom ke 10.0.0.2 → wantRefused "server ini dipin mengikuti sumber 10.0.0.1:3306" (bad_params)
func TestStrictApprovalVoidedBySourceDNSExpiryAndSetRole(t *testing.T)
	// (a) SetSource("10.0.0.2",3306) → dropDatabases needs_local_approval;
	// (b) sumber nama "db-b:3306" + SetResolver ke 10.0.0.5, approval Addr 10.0.0.5 → lolos; resolver → 10.0.0.6 → ditolak;
	// (c) d.Now = Expires → ditolak; (d) p.SetRole("quarantined") → kedua berkas hilang → ditolak
func TestUnsourcedQuarantinedStrictRefusal(t *testing.T)
	// pinDir quarantined tanpa source: dropDatabases → needs_local_approval, "sudo dbpanel agent set-source"; tidak ada berkas permintaan
func TestApprovalFirstUseLogged(t *testing.T)
	// logger ke buffer; dua perintah terlindung → tepat satu baris `msg="local rebuild approval used"` memuat source=10.0.0.1:3306 addr=10.0.0.1
func TestRebuildApprovalCommand(t *testing.T) {
	// replica → {Required:false}
	// quarantined → Required, !Approved, Code ^[0-9]{6}$, Source "10.0.0.1:3306", 1790 < RemainingSec <= 1800; kedua kali kode sama
	// approveRebuild → Approved, Addr "10.0.0.1", 0 <= AgeSec < 5, RemainingSec > 86000, Code ""
	// approval untuk "10.0.0.9:3306" → !Approved, Invalid memuat "10.0.0.9:3306", Code ada
}
func TestSettleRebuiltClearsApproval(t *testing.T)
	// approveRebuild; d.MarkRebuildLoaded("10.0.0.1", 3306); d.SettleRebuilt("10.0.0.1", 3306) → role replica, kedua berkas hilang

// spek 5.13, mysqld sungguhan (quarantineRig: server dikarantina di bawah master baru)
func TestStrictModeWithMySQL(t *testing.T)
	// dropDatabases{toko} ditolak (needs_local_approval), database toko masih ada;
	// approveRebuild → dropDatabases berhasil, toko hilang;
	// buat lagi toko; SetSource ke host lain → ditolak; kembalikan sumber + approve; d.Now lewat Expires → ditolak;
	// approve; CLI-level p.SetRole("quarantined") → ditolak

// internal/agent/strict_commands_test.go
func TestEveryRegisteredCommandClassified(t *testing.T) {
	reg := commands.NewRegistry()
	probe.Register(reg, noMySQLSystem{}, "test")
	runner := jobs.NewRunner(nil, nil)
	runner.Register(reg)
	drv := RegisterMySQL(reg, config.Config{}, t.TempDir(), noMySQLSystem{}, runner, slog.New(slog.DiscardHandler))
	monitor.New(drv, nil, slog.New(slog.DiscardHandler), monitor.Options{Pins: drv.Pins()}).Register(reg)
	// set(reg.Names()) == set(keys(mysql.StrictClassNames())): nama terdaftar tanpa kelas → t.Errorf("%s registered but not classified"),
	// kelas tanpa perintah → t.Errorf("%s classified but not registered")
	// setiap "role-refused": Pins quarantined .Allow(cmd, nil) != nil
}
```

  Ubah test yang kini menjalankan perintah terlindung pada pin
  `quarantined` tanpa persetujuan: panggil `approveRebuild` lebih dulu di
  `TestQuarantinedRefusalTable` (blok rebuild path),
  `TestQuarantinedRebuiltPinsReplica`, `TestConfigureReplicationEndsQuarantine`,
  `TestQuarantineGuardLeavesRebuildAlone`, `TestQuarantineGuardDefersToRebuildLoad`,
  `TestRebuildConfigureAndCloneKeepReadOnly`; cari yang lain dengan
  `go test ./internal/agent/... -run . -short 2>&1 | grep needs_local_approval`.

- [ ] **Step 2: Jalankan, pastikan gagal.**
  `go test ./internal/agent/ ./internal/agent/drivers/mysql/ -run 'Strict|RebuildApproval|ApprovalFirstUse|SettleRebuiltClears|EveryRegistered|Unsourced' -timeout 30m`
  Expected: FAIL (`SetGate`, `StrictClassNames`, `mysql.rebuildApproval` tidak ada).
- [ ] **Step 3: Implementasi.** `Registry.gate` dibaca di bawah `r.mu`
  bersama handler; dipanggil setelah `denied`, sebelum handler, di dalam
  recover yang sama; galatnya dipetakan seperti galat handler. Gerbang
  memakai `d.now()`.
- [ ] **Step 4: Jalankan** Step 2, lalu `go test ./internal/agent/... -race -short` dan `go test ./internal/agent/drivers/mysql/ -run 'Quarantine|Rebuild|Pins|Settle' -timeout 30m`. Expected: PASS.
- [ ] **Step 5: Commit** — `git commit -m "feat(agent): strict mode on quarantined servers: allowlist, local rebuild approval, mysql.rebuildApproval" -m "$TRAILERS"`

---

### Task 4: CLI `approve <kode>` dan `approve batal` (5.2)

**Files:**
- Modify: `internal/agent/cli.go` (usage L32-57, `Main` L88-117, var `stdin` L80-83)
- Test: `internal/agent/cli_test.go`

**Interfaces:**
- Consumes: Task 2 `ValidRebuildRequest`, `RemoveRebuildRequest`,
  `WriteRebuildApproval`, `ClearRebuild`, `NewRebuildApproval`,
  `RebuildApprovalAddr`, `RebuildApprovalTTL`.
- Produces:

```go
// RunCLI is Main with the input the prompts read (approve, set-role);
// Main passes the package's stdin. e2e tests answer prompts through it.
func RunCLI(args []string, in io.Reader, stdout, stderr io.Writer) int
func mainApprove(args []string, in io.Reader, stdout, stderr io.Writer) int // "approve <kode>|batal [--dir DIR]"
var cliHostname = os.Hostname // tests replace
var cliNow = time.Now          // tests replace
```

- Alur `approve <kode>`: argumen (`parsePinArgs`, kata `batal` → hapus
  keduanya) → `LoadPins(dir)` (galat → B9) → peran bukan `quarantined` →
  B3 → tanpa sumber → B6 → `ValidRebuildRequest(now)` tidak ada → B4 →
  kode beda (`subtle.ConstantTimeCompare`) → B5 → `RebuildApprovalAddr` →
  galat B7 → cetak B1 (stdout) → baca satu baris dari `in`; selain `ya`
  (setelah `strings.TrimSpace`) → B2 stdout, keluar 1 → baca ulang
  permintaan; kode berubah atau hilang → B8, keluar 1 →
  `WriteRebuildApproval(NewRebuildApproval(req.Source, addr, now))` →
  `RemoveRebuildRequest` → B10, keluar 0.
- Teks (persis; `%s` waktu `2006-01-02 15:04 MST` lokal):

```text
B1 (stdout, lalu prompt tanpa baris baru):
Persetujuan bangun ulang (rebuild) server ini

Server ini  : <hostname>
Peran       : dikarantina (quarantined)
Sumber data : <host:port>
Alamat IP   : <addr>
[bila others: "              (nama sumber juga mengarah ke: <others dipisah ", ">)"]

Bila disetujui, SEMUA data MySQL di server ini dibuang dan diganti salinan dari sumber di atas. Data yang dibuang tidak bisa dikembalikan.
Pastikan nama dan alamat IP di atas memang master baru Anda, yaitu server yang sekarang dipakai aplikasi.
Bila promote tadi promote darurat, jalankan dulu "Periksa data tertinggal" (dan "Amankan data tertinggal" bila ada) di halaman server ini di panel.
Persetujuan berlaku 24 jam, hanya untuk sumber di atas.

Ketik ya untuk menyetujui, atau apa saja untuk membatalkan: 
B2  Dibatalkan; tidak ada yang disetujui.
B3  Server ini tidak dikarantina (peran: %s); persetujuan bangun ulang tidak diperlukan.
B4  Tidak ada permintaan persetujuan yang berlaku di server ini. Buka form "Bangun ulang sebagai replika" di panel, klik Periksa lagi, lalu jalankan perintah yang ditampilkan.
B5  Kode salah. Salin perintah dari form "Bangun ulang sebagai replika" di panel.
B6  Server ini dikarantina tanpa sumber yang dipin; pin master barunya dulu: sudo dbpanel agent set-source <host:port>
B7  Alamat untuk nama sumber %s tidak ditemukan (DNS: %v); persetujuan tidak ditulis.
B8  Permintaan berubah selama Anda membaca; jalankan perintah ini lagi.
B9  Pin server ini tidak bisa dibaca (%v); persetujuan tidak ditulis.
B10 Disetujui sampai %s. Kembali ke panel dan klik Periksa lagi. Untuk membatalkan: sudo dbpanel agent approve batal
B11 Persetujuan dan permintaan bangun ulang di server ini dihapus.
B12 Gagal menulis persetujuan: %v          (juga: "Gagal menghapus: %v" untuk batal)
```

  Baris usage baru (menggantikan baris `set-role` juga untuk Task 5):

```text
  dbpanel agent set-role {master|replica|quarantined|unassigned} [--ya] [--dir DIR]
                                                         ubah peran yang dipin; --ya: tanpa pertanyaan
                                                         pada server yang dikarantina (quarantined)
  dbpanel agent approve <kode> [--dir DIR]               setujui bangun ulang server yang dikarantina;
                                                         kode dari form "Bangun ulang sebagai replika"
  dbpanel agent approve batal [--dir DIR]                hapus persetujuan dan permintaan bangun ulang
```

- [ ] **Step 1: Tulis test** `TestCLIApprove` (sub-test per kasus; `dir` dengan pin `quarantined` + `127.0.0.1:3306`, permintaan dari `mysql.LoadPins(dir).EnsureRebuildRequest(time.Now())`; `cliHostname` → `"db-lama"`):
  - `RunCLI({"approve", code, "--dir", dir}, strings.NewReader("ya\n"), …)` → 0; stdout memuat `"Server ini  : db-lama"`, `"Sumber data : 127.0.0.1:3306"`, `"Alamat IP   : 127.0.0.1"`, `"SEMUA data MySQL di server ini dibuang"`, `"Periksa data tertinggal"`, dan `"Ketik ya"` muncul setelah semuanya; berkas persetujuan `Source "127.0.0.1:3306"`, `Addr "127.0.0.1"`, `Expires.Sub(Approved) == 24*time.Hour`; berkas permintaan hilang; stdout memuat `"Disetujui sampai "`.
  - jawaban `"y\n"` dan input kosong → 1, `"Dibatalkan; tidak ada yang disetujui."`, tanpa persetujuan, permintaan tetap.
  - kode salah → 1, stderr `"Kode salah."`; tanpa permintaan → 1 `"Tidak ada permintaan persetujuan yang berlaku"`; permintaan ditulis tangan dengan `created` 31 menit lalu → sama; peran `replica` → 1 `"tidak dikarantina (peran: replica)"`.
  - `approve batal` → 0, `"dihapus"`, kedua berkas hilang; `approve` tanpa argumen → 2.
- [ ] **Step 2: Jalankan, pastikan gagal.** `go test ./internal/agent/ -run TestCLIApprove -short` Expected: FAIL (`RunCLI` tidak ada).
- [ ] **Step 3: Implementasi.** `Main` memanggil `RunCLI(args, stdin, stdout, stderr)`; `mainSetRole` juga menerima `in` (dipakai Task 5).
- [ ] **Step 4: Jalankan** Step 2 dan `go test ./internal/agent/ -race -short`. Expected: PASS.
- [ ] **Step 5: Commit** — `git commit -m "feat(agent): approve CLI shows the source and address and writes the local rebuild approval" -m "$TRAILERS"`

---

### Task 5: `set-role` yang bertanya, membersihkan, dan melepas kunci start tanpa kredensial (5.1, 5.3)

**Files:**
- Modify: `internal/agent/cli.go` (`mainSetRole` L259-282, `cliReleaseStartLock` L305-318, `cliMySQLHost` L323-334 tetap untuk `lock-master`/`unlock-master`), `internal/agent/drivers/mysql/pins.go` (`SetRole` L403-415), `internal/agent/drivers/mysql/startlock_guard.go` (`releaseStartLock` L56-104, pemanggil di `guardStartLock` dan `releaseForPromote`)
- Test: `internal/agent/cli_test.go` (`TestCLISetRoleAndSetSource` L231, `TestSetRoleReplicaWhileDownKeepsStartLock` L711, `TestSetRoleMasterReleasesFirst` L731, `TestSetRoleMasterRefusedWhileConfigLocked` L768), `pins_test.go`

**Interfaces:**
- Consumes: Task 3 `Pins.SetRole` membersihkan berkas persetujuan; Task 4 `RunCLI`.
- Produces:

```go
// internal/agent/drivers/mysql
// releaseStartLock / ReleaseStartLock: found is false when neither an agent
// init_file line nor a lock file existed (nothing to release).
func releaseStartLock(ctx context.Context, h Host) (found bool, err error)
func ReleaseStartLock(ctx context.Context, h Host) (found bool, err error)
// Pins.SetRole("master") also removes SourceFile and SourceAddrFile (after ClearRebuild, before the role).

// internal/agent
func parseSetRoleArgs(args []string, stderr io.Writer) (dir, role string, yes, ok bool) // "[--dir DIR] [--ya] ROLE [--dir DIR] [--ya]"
func mainSetRole(args []string, in io.Reader, stdout, stderr io.Writer) int
// cliLayoutHost is the MySQL of this server as far as the start lock needs
// it: the discovered layout, no admin account. Tests replace it.
var cliLayoutHost = func(cfg config.Config) (mysql.Host, error) {
	return mysql.NewRealHost(cfg, nil, probe.RealSystem(), slog.New(slog.DiscardHandler))
}
// cliReleaseStartLock: config.Load(dir) best effort (any error → config.Config{}),
// cliLayoutHost, ReleaseStartLock (2 min); prints the outcome; false = keep the role.
func cliReleaseStartLock(dir string, stdout, stderr io.Writer) bool
```

- Alur `set-role`: argumen → `p := loadPinsForCLI` → `was := p.Role()`;
  bila `was == "quarantined"` dan peran baru bukan `quarantined` dan bukan
  `--ya`: cetak C1 + C2/C3/C4 + C5 ke stdout, baca satu baris dari `in`;
  selain `ya` → C6, keluar 1 → bila peran `master`: `cliReleaseStartLock`
  (false → keluar 1, peran tetap) → `p.SetRole(role)` → `"peran " +
  pinsUpdated` (teks lama) → untuk `master` C11; untuk `unassigned` dari
  `quarantined` C12.
- Hasil pelepasan: `errors.Is(err, mysql.ErrNoMysqld)` dari
  `cliLayoutHost` → C9 (peran diubah); galat lain dari `cliLayoutHost` →
  C10 (stderr, peran tetap); `ReleaseStartLock` galat → teks lama
  `"kunci start tidak bisa dilepas (%v); peran tidak diubah"`; `found` →
  C7; tidak `found` → C8.
- Teks (persis):

```text
C1  Server ini dikarantina (quarantined) mengikuti sumber <host:port>.
    (tanpa sumber: "Server ini dikarantina (quarantined) tanpa sumber yang dipin.")
C2  (master) Setelah perintah ini:
    - kunci start (start lock) dilepas sekarang, jadi MySQL tidak lagi dipaksa baca-saja (read-only) saat menyala;
    - server ini dipin sebagai master dan bisa dibuka untuk ditulis. Bila master baru masih dipakai aplikasi, Anda akan punya dua master yang bisa ditulis.
C3  (unassigned) Setelah perintah ini:
    - agent melepas kunci start (start lock) setelah MySQL menyala dengan baca-saja (read-only) tersimpan;
    - server ini tanpa peran: panel boleh menyiapkannya ulang, termasuk membuang semua datanya, tanpa persetujuan di server ini.
C4  (replica) Setelah perintah ini:
    - agent melepas kunci start (start lock) setelah MySQL menyala dengan baca-saja (read-only) tersimpan;
    - server ini dipin sebagai replika: panel boleh membuang datanya dan menyalin ulang dari sumber yang dipin tanpa persetujuan di server ini.
C5  Ketik ya untuk melanjutkan, atau apa saja untuk membatalkan: 
C6  Dibatalkan; peran tidak diubah.
C7  Kunci start dilepas: baris init_file dan berkas kuncinya dihapus dari konfigurasi MySQL.
C8  Kunci start tidak ditemukan; tidak ada yang dilepas.
C9  MySQL tidak ditemukan di server ini; tidak ada kunci start yang perlu dilepas.
C10 kunci start tidak bisa diperiksa (%v); peran tidak diubah. Bila agent tidak bisa dijalankan, lepas dengan tangan: lihat README, bagian "Melepas kunci start dengan tangan".
C11 Langkah berikutnya: bila MySQL di server ini masih baca-saja (read-only) dan server ini memang akan dipakai aplikasi sebagai master, pastikan master lain tidak lagi dipakai, lalu buka dengan: sudo dbpanel agent unlock-master
C12 Langkah berikutnya: bila server ini sudah dihapus dari panel, tambahkan lagi lewat Tambah server, lalu jalankan Siapkan master atau Buat replika. Sampai itu MySQL-nya tetap baca-saja (read-only).
```

- [ ] **Step 1: Tulis test.** Stub `cliLayoutHost` di setiap test `set-role` (lihat Catatan: tanpa stub test menyentuh konfigurasi MySQL sistem). Stub tanpa mysqld: `mysql.NewHost(mysql.Layout{ConfigFile: filepath.Join(tmp, "my.cnf"), ConfigFiles: []string{filepath.Join(tmp, "my.cnf")}}, nil, nil, probe.RealSystem(), nil)`.
  - `TestSetRoleOnQuarantinedAsks`: pin `quarantined` + `10.0.0.1:3306`. `master` dengan input `"tidak\n"` → 1, stdout memuat C1 (dengan sumber), `"dilepas sekarang"`, `"dua master yang bisa ditulis"`, `"Ketik ya untuk melanjutkan"`, C6; peran tetap. Input kosong → 1, C6 (tidak menggantung). `unassigned` dengan `"ya\n"` → 0, stdout memuat `"membuang semua datanya"` dan C12; berkas peran hilang. `master --ya` tanpa input → 0, stdout tanpa `"Ketik ya"`. Pin `replica` → `set-role master` tanpa input → 0 (tidak bertanya).
  - `TestSetRoleClearsSourceAndApprovalFiles`: `quarantined` + sumber + `source-addr` + permintaan + persetujuan → `set-role master --ya` → peran `master`; `source`, `source-addr`, `rebuild-request`, `rebuild-approval` hilang. Pin `replica` + sumber + persetujuan → `set-role replica` → sumber tetap, berkas persetujuan hilang.
  - `TestSetRoleMasterNeverTouchesSecrets`: dir dengan `agent.yaml` berisi `panel_addr: [` dan tanpa `secrets.key`; stub mencatat `cfg` → `cfg == config.Config{}`; setelah `set-role master` keluar 0, `secrets.key` dan `mysql.cred` tidak ada, stdout C8. `agent.yaml` sah dengan `mysql_socket: /run/x.sock` → stub menerima `cfg.MySQLSocket == "/run/x.sock"`.
  - `TestSetRoleMasterOutcomes`: stub mengembalikan `mysql.ErrNoMysqld` → 0, C9, peran master; galat lain → 1, stderr memuat `"kunci start tidak bisa diperiksa ("` dan `"Melepas kunci start dengan tangan"`, peran tetap. Setiap keluaran sukses `master` memuat C11.
  - Ubah: `TestCLISetRoleAndSetSource` (stub → `ErrNoMysqld`; sesudah `set-role master` berkas `source` tidak ada — urutan test sekarang `master` lalu `replica` lalu `set-source`, tetap lulus); `TestSetRoleReplicaWhileDownKeepsStartLock` (`--ya`, stub `cliLayoutHost` yang gagal test bila dipanggil); `TestSetRoleMasterReleasesFirst` dan `TestSetRoleMasterRefusedWhileConfigLocked` (`--ya`, stub `cliLayoutHost` alih-alih `cliMySQLHost`; tambah: keluaran sukses memuat C7).
  - `pins_test.go`: `TestSetRoleMasterDropsSourcePins` (Pins-level: `SetRole("master")` menghapus `source` dan `source-addr`; `SetRole("replica")` tidak).
- [ ] **Step 2: Jalankan, pastikan gagal.** `go test ./internal/agent/ ./internal/agent/drivers/mysql/ -run 'SetRole|SetSource' -timeout 30m` Expected: FAIL.
- [ ] **Step 3: Implementasi** sesuai alur di atas; `guardStartLock` dan `releaseForPromote` mengabaikan `found`.
- [ ] **Step 4: Jalankan** Step 2 dan `go test ./internal/agent/... -race -short`. Expected: PASS.
- [ ] **Step 5: Commit** — `git commit -m "feat(agent): set-role asks on a quarantined server, releases the start lock without credentials and clears source and approval pins" -m "$TRAILERS"`

---

### Task 6: Kunci start dibaca di lingkungan layanan MySQL (5.4)

**Files:**
- Modify: `internal/agent/drivers/mysql/host.go:28` (interface `Host`), `realhost.go:239-248` (`Exec`), `startlock.go` (`effectiveOpts` L455-460, `printDefaultsArgs` L466, `readEffectiveOptions` L480-525, `startLockStatus` L659-723), `export_test.go`
- Modify (test hosts): `startlock_test.go` (`printDefaultsHost` L151-186)
- Modify (rencana ini): bagian "Bukti kunci start di lingkungan layanan"

**Interfaces:**
- Produces:

```go
// Host
// ExecEnv is Exec with env (KEY=VALUE) replacing those variables of the agent's environment.
ExecEnv(ctx context.Context, env []string, stdin io.Reader, stdout io.Writer, name string, args ...string) error

// startlock.go
type initFileArg struct {
	Prefix string // "" or the stripped prefixes joined by "_", e.g. "loose", "skip", "loose_skip"
	Value  string
	Raw    string // the argument as --print-defaults printed it
}
type effectiveOpts struct {
	InitFiles    []string      // plain init_file values, in order (as today)
	InitFileArgs []initFileArg // every argument named init_file after its prefixes, in order
	User         string
	LogError     string
	Home         string // HOME the final --print-defaults ran with
}
// optionName strips "--", turns "-" into "_", then strips the prefixes
// mysqld accepts (loose_, skip_, disable_, enable_, maximum_) repeatedly.
func optionName(arg string) (name, prefix string)
var protectedHomePrefixes = []string{"/home", "/root", "/run/user"}
var lookupUser = user.Lookup // tests replace (SetLookupUser)
var statHome = os.Stat       // tests replace (SetStatHome)
// serviceHome is the home of the MySQL service account; hidden is true
// when it lies under a protectedHomePrefixes entry and the agent cannot
// stat it (ProtectHome=yes makes those directories empty and unreadable).
func serviceHome(account string) (home string, hidden bool, err error)
// errHomeHidden / errNoServiceAccount: readEffectiveOptions' refusals.
```

- `readEffectiveOptions`: putaran 1 `ExecEnv(env {"HOME=/nonexistent"})`
  untuk mengetahui `user` (aturan bawaan sama: `--user` terakhir setelah
  awalan dibuang, else `mysql` bila root, else pengguna proses);
  `serviceHome(user)`: tidak ditemukan → galat
  `"akun layanan MySQL %s tidak ditemukan: %v"`; `hidden` → galat
  `"direktori home akun layanan MySQL %s (%s) tidak terlihat oleh agent (ProtectHome); ~/.my.cnf di sana tidak bisa diperiksa"`;
  putaran 2 dengan `HOME=<home>`; hasil putaran 2 yang dipakai; bila
  `user` putaran 2 berbeda dari putaran 1 → galat
  `"opsi user di ~/.my.cnf akun layanan MySQL berbeda (%s, bukan %s); kunci start tidak bisa dipastikan"`.
  `printDefaultsArgs` tidak berubah.
- `startLockStatus` (aturan spek 5.4): argumen `InitFileArgs` terakhir
  harus `Prefix == ""` dan `Value == path`, selain itu `Locked=false`:
  - terakhir berawalan: `"opsi %s dibaca sesudah baris agent; kunci start tidak terjamin"` (`%s` = `Raw`);
  - terakhir tanpa awalan, nilai lain: teks lama `"init_file lain dibaca sesudah baris agent: <v>"`;
  - galat `readEffectiveOptions`: teks lama `"konfigurasi efektif MySQL tidak bisa dibaca: %v"` (memuat dua galat di atas).
  `OperatorInitFiles`: nilai argumen sebelum baris agent dengan awalan
  `""` atau `"loose"` (yang akan dijalankan), seperti sekarang. Bila Step
  1 membuktikan bentuk lain berpengaruh walau terbaca sebelum baris agent
  (misalnya `skip-init-file`), tambahkan aturan "di mana pun → tidak
  terkunci" dan catat di Bukti.

- [ ] **Step 1: Bukti dengan mysqld sungguhan; isi tabel di bagian "Bukti".**
  Satu datadir sementara (`mysqld --initialize-insecure --datadir=D`,
  dengan `--user=nobody` bila root, seperti `testmysql`); untuk tiap baris
  tabel, `my.cnf` dengan `[mysqld]` `datadir=D` `socket=D/s.sock`
  `port=0` `mysqlx=OFF` dan bentuk yang diuji; `<agent>` berisi isi kunci
  start (tiga baris `SET …`); jalankan `mysqld --defaults-file=my.cnf
  --print-defaults`, lalu `mysqld --defaults-file=my.cnf &`, tunggu soket,
  `mysql -S D/s.sock -uroot -e "SELECT @@global.init_file,
  @@global.super_read_only"`, matikan. Untuk `~/.my.cnf`: jalankan
  `mysqld` tanpa `--defaults-file` dengan `HOME=<dir>` yang berisi
  `.my.cnf` (datadir, soket dan `init-file` di sana), dan bandingkan
  `@@init_file` dengan dan tanpa `HOME` itu. Commit bukti terpisah:
  `git commit -m "docs: Plan 5b-1 evidence of how mysqld reads init_file forms" -m "$TRAILERS"`.
- [ ] **Step 2: Tulis test.**

```go
func TestOptionNamePrefixes(t *testing.T)
	// "--init-file"→("init_file",""), "--init_file", "--loose-init-file"→("init_file","loose"), "--loose_init_file",
	// "--skip-init-file"→("init_file","skip"), "--disable-init-file", "--enable-init-file", "--maximum-init-file",
	// "--loose-skip-init-file"→("init_file","loose_skip"), "--loose_user"→("user","loose"), "--init-file-x"→("init_file_x","")
func TestStartLockPrefixedInitFileNotLocked(t *testing.T) // fakeLockHost; keluaran --print-defaults palsu:
	//   "--init_file=<agent>"                                   → Locked
	//   "--init_file=<agent> --skip-init-file"                  → !Locked, Reason memuat "opsi --skip-init-file dibaca sesudah baris agent"
	//   "--init_file=<agent> --loose-init-file=/op/x.sql"       → !Locked, Reason memuat "--loose-init-file=/op/x.sql"
	//   "--loose-init-file=<agent>"                             → !Locked
	//   "--init_file=<agent> --maximum-init-file=/op/y.sql"     → !Locked
	//   "--loose-init-file=/op/a.sql --init_file=<agent>"       → Locked, OperatorInitFiles == ["/op/a.sql"]
func TestPrintDefaultsRunsWithServiceHome(t *testing.T)
	// printDefaultsHost mencatat env tiap panggilan; SetLookupUser: "mysql"→/var/lib/mysql, "app"→/home/app
	// keluaran dengan --user=mysql → panggilan terakhir memuat "HOME=/var/lib/mysql", Locked
	// --user=app, SetStatHome → ENOENT untuk /home/app → !Locked, Reason memuat "/home/app" dan "ProtectHome"
	// --user=app, stat berhasil → "HOME=/home/app", Locked
	// akun tidak ada → !Locked, Reason memuat "akun layanan MySQL"
func TestServiceAccountMyCnfBreaksStartLock(t *testing.T) // mysqld sungguhan
	// testmysql instance + WriteStartLock; SetLookupUser: akun mysqld instance → tmp home berisi .my.cnf
	// "[mysqld]\ninit-file=<tmp>/op.sql\n" → startLockStatus !Locked, Reason "init_file lain dibaca sesudah baris agent: <tmp>/op.sql"
	// tanpa .my.cnf → Locked
```

  `printDefaultsHost` di `startlock_test.go` mengimplementasikan `ExecEnv`
  (mencatat `env`, menjawab sama seperti `Exec`). `recordingHost` dan
  `slowHost` mewarisi `ExecEnv` dari `mysql.Host` tertanam; tidak perlu
  diubah.
- [ ] **Step 3: Jalankan, pastikan gagal.** `go test ./internal/agent/drivers/mysql/ -run 'OptionName|Prefixed|ServiceHome|ServiceAccount|StartLock|PrintDefaults|EffectiveInit' -timeout 30m` Expected: FAIL.
- [ ] **Step 4: Implementasi.** `realhost.go`: `Exec` = `ExecEnv(ctx, nil, …)`; `cmd.Env` = `os.Environ()` dengan variabel dari `env` diganti.
- [ ] **Step 5: Jalankan** Step 3 dan `go test ./internal/agent/... -race -short`. Expected: PASS.
- [ ] **Step 6: Commit** — `git commit -m "fix(agent): read the start lock with the MySQL service account's HOME and every init_file prefix" -m "$TRAILERS"`

---

### Task 7: Hasil start terburuk yang disimpan dan dibawa snapshot (5.5, agent dan protokol)

**Files:**
- Create: `internal/agent/drivers/mysql/start_result.go`, `start_result_test.go`
- Modify: `internal/proto/health.go` (`HealthSnapshot` L7-52), `internal/proto/mysql.go` (konstanta hasil L576-585), `internal/agent/drivers/mysql/driver.go` (field `StartResultPath`), `startlock_guard.go` (`guardStartLock` L120-180, `watchBootFailure` L289-335, `reportQuarantineStart` L468-489), `quarantine.go` (`settleRebuilt`), `health.go` (`Snapshot` L42-72)

**Interfaces:**
- Produces:

```go
// internal/proto
const QuarantineStartFailed = "failed" // StartResultState.Result: MySQL failed to start on the lock file
// StartResultState is the worst start of MySQL since the quarantine (spec 5.5).
type StartResultState struct {
	Result   string `json:"result"`              // late | failed | bypassed | ok
	AgeSec   int64  `json:"age_sec"`             // since the agent decided it, by the agent's clock
	InitFile string `json:"init_file,omitempty"` // @@init_file at that start; the lock file for failed
}
// HealthSnapshot +=
	// StartResult is set while the role pin is quarantined and a start was judged; nil otherwise and from an older agent.
	StartResult *StartResultState `json:"start_result,omitempty"`

// internal/agent/drivers/mysql
const StartResultFile = "/var/lib/dbpanel/start-result"
// Driver += StartResultPath string // empty: "start-result" beside SeenPath, or StartResultFile when SeenPath is empty too
type startResult struct {
	Result   string    `json:"result"`
	Decided  time.Time `json:"decided"` // wall clock: the only time that survives a restart
	InitFile string    `json:"init_file,omitempty"`
}
func startResultRank(result string) int                 // late 4, failed 3, bypassed 2, ok 1, other 0
func (d *Driver) startResultPath() string
func (d *Driver) recordStartResult(r startResult) error // writes when rank(r) >= rank(stored), or none/unreadable stored; MkdirAll(dir, 0o755); writeJSON
func (d *Driver) StartResult(now time.Time) *proto.StartResultState // nil unless the role pin is quarantined and the file reads; AgeSec = max(0, now-Decided)
func (d *Driver) clearStartResult()                    // removeFile; a failure is warned
```

- Urutan: `reportQuarantineStart` dan `watchBootFailure` memanggil
  `recordStartResult` **sebelum** `emitStart`; kegagalan tulis → `warn`,
  kejadian tetap dikirim. `guardStartLock`: peran bukan `quarantined` (dan
  bukan `""`) → `clearStartResult()` sebelum cabang lain. `settleRebuilt`
  yang berhasil → `clearStartResult()`. `Snapshot` mengisi
  `s.StartResult = d.StartResult(d.now())`.
- Kompatibilitas: panel lama mengabaikan `start_result`; agent lama tidak
  mengirimnya (panel baru tetap membuka dari kejadian).

- [ ] **Step 1: Tulis test.**

```go
func TestStartResultWorstKept(t *testing.T) // unit, Driver{SeenPath: tmp/replicas-seen}
	// berurutan ok, bypassed, ok, failed, bypassed, late, failed → isi berkas: ok, bypassed, bypassed, failed, failed, late, late
	// late lagi dengan Decided lebih baru → Decided berkas == yang baru
	// berkas di tmp/start-result, mode 0644
func TestStartResultSurvivesRestart(t *testing.T) // mysqld sungguhan
	// guard rig pin quarantined, ev offline, MySQL bisa ditulis → tickAt → berkas late
	// Driver baru (dir, SeenPath sama; "restart") → Snapshot(ctx).StartResult == &{Result:"late", AgeSec in [0,5], InitFile:<@@init_file>}
	// putaran pertama Driver baru (flag kini ON) → kejadian ok; StartResult tetap late
func TestStartResultWrittenBeforeSend(t *testing.T) // Emitter yang membaca berkas saat Emit: berkas sudah late
func TestStartResultClearedOutsideQuarantine(t *testing.T)
	// berkas ada; SetRole("replica") → GuardTick → berkas hilang; Snapshot.StartResult == nil
func TestStartFailedRecorded(t *testing.T) // pola TestBootstrapFailureEventFromErrorLog → berkas failed, InitFile == path kunci
```

- [ ] **Step 2: Jalankan, pastikan gagal.** `go test ./internal/agent/drivers/mysql/ -run 'StartResult|StartFailedRecorded' -timeout 30m` Expected: FAIL.
- [ ] **Step 3: Implementasi** sesuai Interfaces.
- [ ] **Step 4: Jalankan** Step 2 dan `go test ./internal/agent/... ./internal/proto/... -race -short`. Expected: PASS.
- [ ] **Step 5: Commit** — `git commit -m "feat(agent): keep the worst start since the quarantine on disk and report it in every snapshot" -m "$TRAILERS"`

---

### Task 8: Antrean kirim terbatas; penjaga tidak pernah mengirim (5.6)

**Files:**
- Create: `internal/agent/drivers/mysql/event_queue.go`, `event_queue_test.go`, `internal/agent/guard_send_test.go`
- Modify: `internal/agent/drivers/mysql/driver.go` (hapus `pendingStart`, `startSendWarned`; tambah `outbox`), `startlock_guard.go` (`emitStart`, `sendRoundEvents`, `flushStartEvents`, `maxPendingStart` L337-405), `promote_lock.go` (`GuardTick` L547-569, `guardExpired` L700, `keepLock` L720-733, `selfQuarantine` L776, `emit` L784-796), `internal/agent/monitor/monitor.go` (`Run` L295-313), `export_test.go`, `promote_lock_test.go` (`eventLog` L33-60)

**Interfaces:**
- Produces:

```go
// event_queue.go
const (
	eventQueueCap = 64
	eventRetry    = 5 * time.Second // after a failed send
)
type eventQueue struct {
	mu    sync.Mutex
	items []proto.Event
	wake  chan struct{} // cap 1, made on first push
}
// push appends ev; when full it first drops the oldest start report
// (EventQuarantineStart, EventQuarantineStartFailed: their value is in
// the snapshot), else the oldest event, and returns what it dropped.
func (q *eventQueue) push(ev proto.Event) (dropped *proto.Event)
func (q *eventQueue) front() (proto.Event, bool)
func (q *eventQueue) popFront()
// enqueue replaces emit and emitStart: it marshals payload and pushes it;
// it never blocks and never sends. Without Events it drops the event.
func (d *Driver) enqueue(typ string, payload any)
// sendQueued sends from the front until the queue is empty (true) or a
// send fails (false, warned once until a send succeeds). Never under promoteMu.
func (d *Driver) sendQueued() bool
// RunEvents is the one sender: sendQueued, then wait for a push (wake),
// eventRetry after a failure, or ctx.
func (d *Driver) RunEvents(ctx context.Context)
// Driver += outbox eventQueue; sendWarned bool (owned by the sender)

// internal/agent/monitor
// EventSender is a Source that sends its guard's events itself
// (mysql.Driver.RunEvents); Run runs it beside its loops and waits for it.
type EventSender interface{ RunEvents(ctx context.Context) }

// export_test.go
func (d *Driver) FlushEvents() bool { return d.sendQueued() }
```

- Perubahan perilaku: `emit` → `enqueue` (tanpa nilai kembali);
  `keepLock` mencatat `keptAt` setelah `enqueue` (kejadian ditahan antrean
  sampai terkirim); `sendRoundEvents` hanya `enqueue` `qNotice`; hapus
  `flushStartEvents`, `pendingStart`, `maxPendingStart`,
  `startSendWarned`. `GuardTick`: `defer d.sendRoundEvents()` boleh tetap
  (ia tidak mengirim lagi). Perbarui komentar `GuardTick` L530-546.
- Kompatibilitas: tidak ada perubahan protokol; urutan kejadian sama
  dengan urutan diputuskan.

- [ ] **Step 1: Tulis test.**

```go
// event_queue_test.go (package mysql_test via export, atau internal)
func TestEventQueueOrderAndDrop(t *testing.T)
	// 64 kejadian: lockExpired e1, quarantineStart s1, lockExpired e2..; push e65 → s1 terbuang, urutan sisanya tetap;
	// antrean penuh tanpa kejadian start → yang tertua terbuang
func TestGuardNeverSendsUnderPromoteMu(t *testing.T) // mysqld sungguhan
	// guard rig quarantined + MySQL bisa ditulis; Events = stuckEmitter (Emit memblok sampai release; di dalam Emit
	// goroutine memanggil d.HoldPromoteMu() dan harus berhasil dalam 1 s); go d.RunEvents(ctx)
	// 5 × tickAt: tiap GuardTick < 2 s; Emit dipanggil sekali (macet); release → semua kejadian terkirim, late lebih dulu
func TestQueuedEventsDeliveredInOrderAfterOffline(t *testing.T)
	// pengganti TestQuarantineStartReportRetriedUntilDelivered: offline → 2 putaran → FlushEvents()==false, 0 terkirim;
	// online → FlushEvents()==true → tepat satu late

// internal/agent/guard_send_test.go (package agent; mysqld sungguhan)
func TestGuardRoundsNotDelayedByNonDrainingPanel(t *testing.T) {
	// httptest + websocket.Accept yang tidak pernah membaca (pola TestAgentReadLimitOneMiB);
	// Agent New(...) tersambung; goroutine mengirim a.Emit dengan payload 1 MiB sampai satu Emit memblok (writeMu dipegang);
	// Driver quarantined di testmysql (MySQL bisa ditulis → kejadian late) dengan Events = a;
	// monitor.New(timedSource{d}, a, logger, monitor.Options{Pins: d.Pins()}).Run(ctx) selama 4 putaran penjaga
	// timedSource menanam *mysql.Driver dan mencatat awal/akhir GuardTick
	// untuk setiap dua putaran berurutan: start[i+1]-end[i] <= GuardEvery+1s
}
```

  `eventLog` (`promote_lock_test.go`): tambah `flush func() bool`; `all()`
  dan `sends()` memanggil `flush` (bila tidak nil) sebelum mengunci `mu`;
  setiap rig yang memasang `d.Events = ev` juga memasang `ev.flush =
  d.FlushEvents` (`newGuardRig`, `newQuarantineRig`, rig di
  `promote_lock_test.go`, `stateAtSend` mewarisi). Ubah test yang
  menegaskan pengiriman di dalam putaran: `TestGuardSendsReportsAfterReadOnlyAndHousekeeping`,
  `TestGuardFlushesStartReportsOnEveryPath` (kini: kejadian sampai lewat
  pengirim apa pun jalur putarannya),
  `TestQuarantineStartReportRetriedUntilDelivered` (diganti test di atas).
- [ ] **Step 2: Jalankan, pastikan gagal.** `go test ./internal/agent/ ./internal/agent/drivers/mysql/ -run 'EventQueue|NeverSends|NonDraining|QueuedEvents|StartReport|Flushes|SendsReports' -timeout 30m` Expected: FAIL.
- [ ] **Step 3: Implementasi** sesuai Interfaces; `Monitor.Run`: `if s, ok := m.src.(EventSender); ok { wg.Add(1); go func() { defer wg.Done(); s.RunEvents(ctx) }() }`.
- [ ] **Step 4: Jalankan** Step 2, `go test ./internal/agent/... -race -short`, dan `go test ./internal/agent/drivers/mysql/ -timeout 40m`. Expected: PASS.
- [ ] **Step 5: Commit** — `git commit -m "fix(agent): guard events go through a bounded queue drained by one sender, never under promoteMu" -m "$TRAILERS"`

---

### Task 9: Panel membuka peringatan start dari snapshot, tanpa ganda (5.5, panel)

**Files:**
- Modify: `internal/panel/monitor/promote.go` (`handleStartEvent` L583-645, dekat `leftoverCheckedAfter` L713-728), `internal/panel/monitor/monitor.go` (`report` L471-486)
- Test: `internal/panel/monitor/alerts_test.go`

**Interfaces:**
- Consumes: Task 7 `proto.StartResultState`, `QuarantineStartFailed`.
- Produces:

```go
const (
	startResultSlack       = time.Minute      // decided time read back from a relative age
	startEventRepeatWindow = 10 * time.Minute // an event after a snapshot already opened the same alert
)
// startResultFromSnapshot opens, from a report's StartResult, the alert of
// a start the panel may not have heard of (its event lost): late →
// KindMasterStartLate (startLateText), failed → KindMasterStartBlocked
// (startBlockedMessage(InitFile)), bypassed → KindMasterStartBypassed
// (startBypassedText); ok → nothing. decided = at - AgeSec. It opens only
// when the server resolves as quarantined, no alert of that kind is open,
// none was opened at or after decided-startResultSlack
// (Store.CountAlertsOpenedSince), and, for late, no "Periksa data
// tertinggal" result is later than decided (leftoverCheckedAfter on
// store.Alert{ServerID: id, OpenedAt: decided}). The caller holds mu.
func (m *Monitor) startResultFromSnapshot(id int64, snap *proto.HealthSnapshot, at time.Time) []alertUpdate
```

- `report`: setelah `ingest`, di bawah `mu`, untuk setiap laporan (juga
  backlog): `alerts = append(alerts, m.startResultFromSnapshot(serverID, &he.Snapshot, at)...)`.
- `handleStartEvent`: untuk `late`/`bypassed`/`failed` (juga bentuk
  "terburuk" untuk kejadian tak terbaca) tidak membuka bila tidak ada
  alert jenis itu yang terbuka **dan** ada yang dibuka sejak `now -
  startEventRepeatWindow` (sudah dibuka dari snapshot dan sudah
  ditangani). Penutupan oleh kejadian tidak berubah.

- [ ] **Step 1: Tulis test** (rig yang ada: `quarSnap`, `r.quarantined`, `ma.setSnap`, `ma.send`, `startEvent`, `closedWithReason`; leftover seperti `TestStartLateClosesAfterLeftoverCheck`).

```go
func TestStartLateOpensFromSnapshotOnce(t *testing.T)
	// snap := quarSnap(locked); snap.StartResult = &proto.StartResultState{Result: "late", AgeSec: 30}
	// r.quarantined(snap) → isOpen(late); ma.send() dua kali → len(r.alerts(mID, late)) == 1;
	// r.m.HandleEvent(mID, startEvent(late)) → masih 1
func TestStartLateFromSnapshotNotReopenedAfterLeftoverCheck(t *testing.T)
	// kejadian late membuka; clk +1m; setCheck(now) + tick → tertutup; laporan dengan AgeSec = 120 (decided sebelum alert) → tidak ada alert baru
func TestStartLateNewerSnapshotReopens(t *testing.T)
	// setelah tertutup oleh pemeriksaan, clk +5m, laporan dengan AgeSec 0 → alert baru terbuka (2 alert)
func TestStartEventAfterSnapshotNoDouble(t *testing.T)
	// snapshot membuka; pemeriksaan menutup; dalam 10 menit HandleEvent(late) → tetap 1 alert; setelah 11 menit → kejadian late baru membuka
func TestSnapshotFailedAndBypassedOpen(t *testing.T)
	// failed (InitFile "/etc/mysql/x.sql") → KindMasterStartBlocked, pesan memuat "/etc/mysql/x.sql"; bypassed → KindMasterStartBypassed
func TestStartResultIgnoredWhenNotQuarantined(t *testing.T) // master biasa dengan StartResult late → tidak ada alert
```

- [ ] **Step 2: Jalankan, pastikan gagal.** `go test ./internal/panel/monitor/ -run 'StartLate|StartEventAfter|SnapshotFailed|StartResult' -race` Expected: FAIL.
- [ ] **Step 3: Implementasi** sesuai Interfaces.
- [ ] **Step 4: Jalankan** Step 2 dan `go test ./internal/panel/... -race -short`. Expected: PASS.
- [ ] **Step 5: Commit** — `git commit -m "feat(monitor): open start alerts from the snapshot's start result without opening them twice" -m "$TRAILERS"`

---

### Task 10: "Hapus server" pada server dikarantina tidak melepasnya (5.1, panel)

**Files:**
- Modify: `internal/panel/orchestrator/controls.go` (`deleteServer` L174-193, komentar `DeleteServer` L153-163), `internal/panel/web/handlers_oldmaster.go` (`deleteWarnings` L463-500)
- Test: `internal/panel/orchestrator/controls_test.go` (L241-308), `internal/panel/web/web_oldmaster_test.go` (`TestDeleteQuarantinedDialogSaysLockStays` L805), `internal/panel/e2e_replica_test.go`

**Interfaces:**
- Produces: `deleteServer` membaca server; `srv.Role == store.RoleQuarantined`
  → tidak ada panggilan agent sama sekali; peran lain → `releaseRole`
  best effort seperti sekarang. `deleteWarnings(srv, box, sf)`: kalimat
  lama "Bila MySQL server ini sedang mati, …" diganti D1, ditambahkan bila
  `srv.Role == store.RoleQuarantined` (juga saat `box == nil`).
- Teks D1 (persis):

```text
Server ini tetap dikarantina (quarantined) setelah dihapus dari panel: MySQL-nya tetap baca-saja (read-only) dan kunci start (start lock) tetap terpasang. Untuk memakai server ini lagi, jalankan di server itu: sudo dbpanel agent set-role unassigned, atau sudo dbpanel agent set-role master bila server ini akan menjadi master lagi.
```

- Kompatibilitas: berlaku apa pun versi agent (keputusan dari peran di
  panel, bukan jawaban agent).

- [ ] **Step 1: Tulis/ubah test.**
  - `TestDeleteQuarantinedNeverReleasesRole` (orchestrator): old master setelah `SwapMaster` + `ConfirmCutover`, fake menjawab `releaseRole` sukses (agent lama) → `DeleteServer` sukses, `wantCmds(t, "old master", m.cmds(0), nil)`.
  - Ubah `TestDeleteQuarantinedRefusedWhileCutoverPending` (harapkan `nil`, bukan `[]string{"mysql.releaseRole"}`) dan periksa `TestDeleteOldMasterWhileQuarantinePending`.
  - Ubah `TestDeleteQuarantinedDialogSaysLockStays`: `sentence` = D1; halaman server yang bukan master lama tidak memuat `"tetap dikarantina (quarantined)"`.
  - `TestE2EDeleteQuarantinedOldMasterStaysLocked` (e2e baru; `startE2EPromoteTopology`, promote terencana, `confirmCutover`, hapus master lama lewat UI dengan token `web.ActDeleteServer`): pin `role` di dir agent master lama tetap `quarantined`, `startLockOnDisk` → baris dan berkas ada, `super_read_only` ON; lalu `agent.RunCLI({"set-role", "unassigned", "--ya", "--dir", dir}, …)` → 0 dan berkas `role` hilang.
- [ ] **Step 2: Jalankan, pastikan gagal.** `go test ./internal/panel/orchestrator/ ./internal/panel/web/ -run 'DeleteQuarantined|DeleteOldMaster' -race` Expected: FAIL.
- [ ] **Step 3: Implementasi.**
- [ ] **Step 4: Jalankan** Step 2, `go test ./internal/panel/... -race -short`, `go test -timeout 30m -run TestE2EDeleteQuarantinedOldMasterStaysLocked ./internal/panel/`. Expected: PASS.
- [ ] **Step 5: Commit** — `git commit -m "fix(panel): deleting a quarantined server leaves it quarantined and says how to reuse it" -m "$TRAILERS"`

---

### Task 11: Panel: persetujuan di server untuk bangun ulang (5.2, panel)

**Files:**
- Create: `internal/panel/orchestrator/approval.go`, `approval_test.go`
- Modify: `internal/panel/orchestrator/create_replica.go` (`StartCreateReplica` L81-100), `runner.go` (`Approve` L783-804, `Retry` L837-890, `callWithin` L1627-1635), `tls.go` (`StartMySQLTLS` L58-70, `RenewMySQLTLS` L89-117), `fake_test.go` (`agentCommands` L44-53, `scriptReplicaTarget` L595-635, helper `failCode`), `internal/panel/web/handlers_mysql.go` (interface `Orchestrator` L22-75, `createReplica` L626-738), `handlers_oldmaster.go` (`rebuildInfo` L674-688, `rebuildTarget` L716-788), `handlers_jobs.go` (retry/approve L460-480: audit), `web/templates/replica_new.html` (L3-14, L83-86), `internal/panel/web/web_mysql_jobs_test.go` (`fakeOrch`), `internal/panel/e2e_helpers_test.go` (`createReplica` L726-753)
- Test: `approval_test.go`, `internal/panel/web/web_oldmaster_test.go`, e2e yang membangun ulang master lama

**Interfaces:**
- Consumes: Task 3 `proto.RebuildApprovalResult`, `mysql.rebuildApproval`; Task 1 `proto.CodeNeedsLocalApproval`; Task 4 `agent.RunCLI`.
- Produces:

```go
// internal/panel/orchestrator/approval.go
var ErrRebuildNeedsApproval = errors.New("bangun ulang butuh persetujuan di server")
type rebuildApprovalError struct{ msg string } // Error() = msg; Is(ErrRebuildNeedsApproval)
// RebuildApproval asks serverID's agent (mysql.rebuildApproval). An agent
// without the command: AgentNeedsUpgrade and an outdatedAgentError with
// teks P11 (errors.Is ErrAgentOutdated).
func (r *Runner) RebuildApproval(ctx context.Context, serverID int64) (proto.RebuildApprovalResult, error)
// requireRebuildApproval: nil error only when !Required or Approved; not
// approved → rebuildApprovalError (P10) with the agent's code.
func (r *Runner) requireRebuildApproval(ctx context.Context, serverID int64) (proto.RebuildApprovalResult, error)
// rebuildApprovalForJob: for a create_replica job with RebuildOldMaster in
// failed, partial or waiting, requireRebuildApproval on its server (callTimeout, r.base); else nil.
func (r *Runner) rebuildApprovalForJob(jobID string) error
// localApprovalErr: a needs_local_approval answer from serverID becomes a
// stepError (P12, code from RebuildApproval; P12b when that fails); else err.
func (r *Runner) localApprovalErr(ctx context.Context, serverID int64, err error) error

// internal/panel/web
// Orchestrator += RebuildApproval(ctx context.Context, serverID int64) (proto.RebuildApprovalResult, error)
// rebuildInfo +=
	Approval    *proto.RebuildApprovalResult // nil when it could not be read
	ApprovalErr string                       // P6/P7, the panel's own words
	ApprovalOK  bool                         // Approved || !Required: the button is enabled
```

- Penegakan: `StartCreateReplica` → bila `p.RebuildOldMaster`, setelah
  pemeriksaan koneksi dan sebelum `startMu`: `requireRebuildApproval`
  (galat dikembalikan apa adanya). `Approve` dan `Retry` →
  `rebuildApprovalForJob(jobID)` sebelum `startMu` (seperti
  `retryConnectionsClear`). `callWithin` → `errors.Is(err,
  &proto.Error{Code: proto.CodeNeedsLocalApproval})` → `localApprovalErr`.
  `RenewMySQLTLS` melewati server dengan peran `store.RoleQuarantined`
  (`r.log.Info("mysql certificate renewal skipped: quarantined", …)`);
  `StartMySQLTLS` menolak server itu dengan P15.
- Web: `rebuildTarget` mengisi `Approval`/`ApprovalErr`/`ApprovalOK` bila
  agent online (offline → P7); tombol "Bangun ulang sebagai replika"
  `disabled` juga bila `not .ApprovalOK`. Kotak dirender di atas `<form>`
  (setelah alert peringatan). "Periksa lagi" adalah tautan `class="btn
  btn-sm"` ke `/masters/{{$.Master.ID}}/replicas/new?bangun-ulang={{.Server.ID}}`
  (tanpa JS). Audit `create_replica` (rebuild) dan audit retry/approve
  job bangun ulang memuat `"local_approval": <Approved>` dan
  `"local_approval_age_sec": <AgeSec>` (dibaca lagi lewat
  `RebuildApproval` di handler; galat → `"local_approval": false`).
- Teks (persis; `{{…}}` diganti nilai):

```text
P1  Persetujuan di server
P2  Belum disetujui. Jalankan perintah ini di {{server}} sebagai root, lalu klik Periksa lagi:
    <code>sudo dbpanel agent approve {{kode}}</code>
    Kode berlaku {{N}} menit lagi.
P3  Bangun ulang membuang semua data MySQL di server ini. Persetujuan di server memastikan orang yang memegang server itu melihat dari mana data penggantinya diambil, sehingga panel saja tidak bisa melakukannya.
P4  Sudah disetujui di {{server}} untuk sumber {{source}} ({{addr}}); berlaku {{H}} jam {{M}} menit lagi. Untuk membatalkan, jalankan di server itu: sudo dbpanel agent approve batal
P5  Persetujuan yang ada di {{server}} tidak berlaku lagi: {{invalid}}.
P6  Agent perlu dimutakhirkan: agent di {{server}} belum mengenal persetujuan di server. Klik Pasang ulang agent di halaman server itu, jalankan perintahnya, lalu klik Periksa lagi.
P7  Persetujuan di server tidak bisa diperiksa: agent {{server}} offline.   (galat lain: "Persetujuan di server tidak bisa diperiksa sekarang; klik Periksa lagi.")
P8  Agent {{server}} tidak melaporkan server ini dikarantina, jadi persetujuan di server tidak diperlukan.
P9  Periksa lagi
P10 Bangun ulang {{server}} butuh persetujuan di server itu. Jalankan di server itu sebagai root: sudo dbpanel agent approve {{kode}}, lalu coba lagi.
P11 Agent perlu dimutakhirkan: agent di {{server}} belum mengenal persetujuan di server, jadi bangun ulang ditolak. Klik Pasang ulang agent di halaman server itu, jalankan perintahnya, lalu coba lagi.
P12 Server {{server}} dikarantina (quarantined): langkah ini butuh persetujuan di server itu. Jalankan di server itu sebagai root: sudo dbpanel agent approve {{kode}}, lalu klik Ulangi.
P12b Server {{server}} dikarantina (quarantined): langkah ini butuh persetujuan di server itu. Buka form Bangun ulang sebagai replika, klik Periksa lagi, jalankan perintah yang ditampilkan di server itu, lalu klik Ulangi.
P15 Server ini dikarantina (quarantined); sertifikat MySQL-nya dipasang lagi saat dibangun ulang sebagai replika.
```

- Kompatibilitas: agent lama (`unknown_command`) → P11 di `Start*`,
  `Retry`, `Approve`, dan P6 di form; panel lama + agent baru: lihat Task
  3. `invalid` agent dipotong `maxAgentWords` sebelum tampil.

- [ ] **Step 1: Tulis test.**
  - `fake_test.go`: `"mysql.rebuildApproval"` di `agentCommands`; `scriptReplicaTarget` menjawab `proto.RebuildApprovalResult{Required: true, Approved: true, RemainingSec: 86000, AgeSec: 60, Source: "10.0.0.1:3306", Addr: "10.0.0.1"}`; `func (fa *fakeAgent) failCode(cmd, msg string)` (handler mengembalikan `&commands.LocalActionError{Msg: msg}`).
  - `TestRebuildRefusedWithoutApproval`: jawaban `{Required: true, Code: "123456", Source: "10.0.0.1:3306", RemainingSec: 1700}` → `StartCreateReplica(RebuildOldMaster)` → `errors.Is(err, ErrRebuildNeedsApproval)`, teks memuat `"sudo dbpanel agent approve 123456"`; tidak ada job, tidak ada baris replika, perintah ke target hanya `mysql.connections` dan `mysql.rebuildApproval`.
  - `TestRebuildRefusedOnOldAgent`: `fa.oldAgent("mysql.rebuildApproval")` → `errors.Is(err, ErrAgentOutdated)`, teks memuat `"Agent perlu dimutakhirkan"`.
  - `TestRebuildNotRequiredProceeds`: `{Required: false}` → job dimulai.
  - `TestRebuildRetryAndApproveRecheckApproval`: job bangun ulang gagal; jawaban kini belum disetujui → `Retry` → `ErrRebuildNeedsApproval`, job tetap `failed`; job menunggu (WaitWipe) → `Approve` → sama.
  - `TestNeedsLocalApprovalMidJobNamesCommand`: `failCode("mysql.configure", "…")` dan `rebuildApproval` menjawab kode `"654321"` → job `failed`, `j.Error` memuat `"sudo dbpanel agent approve 654321"` dan `"klik Ulangi"`.
  - `TestTLSSkipsQuarantined`: `RenewMySQLTLS` tidak memulai job untuk server dikarantina; `StartMySQLTLS` → galat P15.
  - Web (`fakeOrch` mendapat field `approval proto.RebuildApprovalResult`, `approvalErr error`): `TestRebuildFormShowsApprovalBox` — belum disetujui: `"Persetujuan di server"`, `"sudo dbpanel agent approve 123456"`, `"Kode berlaku "`, P3, tautan `"?bangun-ulang=1"` berlabel `"Periksa lagi"`, tombol `disabled`; disetujui: `"Sudah disetujui di "`, tombol tanpa `disabled`; galat agent lama: `"Agent perlu dimutakhirkan"`. `TestRebuildAuditHasLocalApproval`: entri audit `create_replica` memuat `"local_approval":true` dan `"local_approval_age_sec":60`.
  - E2E: helper `func (e *e2ePromoteTopology) approveRebuild(t *testing.T, master, old *e2eNode)` — GET form, regex `sudo dbpanel agent approve ([0-9]{6})`, `agent.RunCLI([]string{"approve", code, "--dir", old.agent.dir}, strings.NewReader("ya\n"), &out, &errb)` harus 0, GET lagi memuat `"Sudah disetujui di "`. `createReplica(…, rebuild=true, …)` memanggilnya sebelum `postJob`. Di `TestE2EPromotePlanned` (sebelum `rebuildOldMaster`): POST bangun ulang tanpa persetujuan → bukan 303, badan memuat `"sudo dbpanel agent approve "`, tidak ada job (spek 5.13: "berhenti di persetujuan, lanjut setelah approve").
- [ ] **Step 2: Jalankan, pastikan gagal.** `go test ./internal/panel/orchestrator/ ./internal/panel/web/ -run 'Rebuild|NeedsLocalApproval|TLSSkips' -race` Expected: FAIL.
- [ ] **Step 3: Implementasi;** `bash scripts/tailwind.sh` setelah template.
- [ ] **Step 4: Jalankan** Step 2, `go test ./internal/panel/... -race -short`, lalu `make test-e2e`. Expected: PASS.
- [ ] **Step 5: Commit** — `git commit -m "feat(panel): rebuild of a quarantined old master needs and shows the local approval" -m "$TRAILERS"`

---

### Task 12: E2E sisa spek 5.13 dan README

**Files:**
- Modify: `internal/panel/e2e_helpers_test.go` (`e2eAgent`, `start` L463-522), `internal/panel/e2e_replica_test.go` (`TestE2EPromoteEmergency`), `README.md` ("Peran dan sumber yang dipin" L539-640, "Master lama" L1263-1420, bagian CLI)

**Interfaces:**
- Consumes: semua task sebelumnya.
- Produces: `e2eAgent.dropStartEvents bool` — `start` membungkus
  `Events` dengan pembungkus yang membuang `proto.EventQuarantineStart`
  dan `EventQuarantineStartFailed` (mengembalikan `nil`), meneruskan yang
  lain ke `a`.

- [ ] **Step 1: Ubah `TestE2EPromoteEmergency`** (spek 5.13, uji 5.5): sebelum `m.agent.start` yang menghasilkan `late`, set `m.agent.dropStartEvents = true`; harapkan `master_start_late` tetap terbuka (dari snapshot) dan tepat satu alert jenis itu; setelah "Periksa data tertinggal", alert tertutup dan tidak terbuka lagi selama dua laporan berikutnya; sisa alur (Amankan, bangun ulang dengan `approveRebuild`) tidak berubah.
- [ ] **Step 2: Jalankan** `go test -timeout 30m -run 'TestE2EPromoteEmergency|TestE2EPromotePlanned|TestE2EDeleteQuarantined' ./internal/panel/`. Expected: PASS.
- [ ] **Step 3: README** (teks Indonesia, aturan 5.11):
  - "Peran dan sumber yang dipin": `releaseRole` ditolak pada `quarantined`; mode ketat (daftar perintah yang tetap jalan tanpa persetujuan, perintah yang butuh persetujuan); `set-role` pada server yang dikarantina bertanya dan `--ya`; setiap `set-role` menghapus `rebuild-request`/`rebuild-approval`; `set-role master` menghapus `source`/`source-addr`, tidak memuat `secrets.key`/`mysql.cred`, tiga hasil pelepasan kunci start; ubah kalimat "atau dilepas seperti replika" untuk server `quarantined`.
  - Subbagian baru "Persetujuan di server untuk bangun ulang": alur form → `sudo dbpanel agent approve <kode>` (apa yang ditampilkan, `ya`), 30 menit / 24 jam / 5 menit, berkas dan isinya, `approve batal`, apa yang membuatnya tidak berlaku, pesan `needs_local_approval` di panel lama.
  - Subbagian baru "Memakai lagi server yang dihapus saat masih dikarantina": D1 dan langkah `set-role unassigned`/`master`.
  - Subbagian baru "Melepas kunci start dengan tangan": (1) hentikan agent (`systemctl stop dbpanel-agent`); (2) di berkas konfigurasi terkelola (`<direktori include>/zz-dbpanel.cnf`, atau blok dbpanel di berkas konfigurasi utama) hapus baris `init_file=<…>/dbpanel-start-lock.sql`; (3) baru hapus berkas `dbpanel-start-lock.sql`; (4) `echo master > /etc/dbpanel/role` atau `unassigned`, dan hapus `/etc/dbpanel/source`, `/etc/dbpanel/source-addr`, `/etc/dbpanel/rebuild-request`, `/etc/dbpanel/rebuild-approval`; (5) MySQL tetap baca-saja (read-only) sampai dibuka (`SET PERSIST super_read_only=OFF; SET PERSIST read_only=OFF;` sebagai root MySQL) — urutan baris sebelum berkas wajib, alasannya disebut.
  - "Master lama": ganti kalimat "(agent yang dimulai ulang sebelum laporan sampai kehilangannya)" dengan hasil start terburuk di `/var/lib/dbpanel/start-result` yang dibawa setiap laporan kesehatan; kunci start diperiksa dengan `HOME` akun layanan MySQL dan semua awalan opsi (`loose-`, `skip-`, `disable-`, `enable-`, `maximum-`); home di `/home`, `/root`, `/run/user` yang tidak terlihat agent membuat kunci "belum terjamin".
  - Daftar perintah CLI: baris usage dari Task 4.
- [ ] **Step 4: Gerbang akhir.** `make test` lalu `make test-e2e` (berurutan); catat durasi.
- [ ] **Step 5:** `graphify update .`; commit — `git commit -m "test(e2e): start alert from the snapshot when the event is lost; docs for Plan 5b-1" -m "$TRAILERS"`

---

## Keputusan perencana

1. **Gerbang mode ketat di `commands.Registry`, bukan di `Pins.Allow`.**
   Sepertiga perintah (`install`, `restart`, `connect`, `stopReplica`,
   `job.*`, `probe`, …) tidak pernah memanggil `d.allow`; daftar izin
   "semua yang lain ditolak" hanya bisa ditegakkan di titik yang dilewati
   semua perintah. Klasifikasi per nama; aturan parameter tetap di
   `Pins.Allow`.
2. **Tiga kelas, termasuk `role-refused`.** `lockForPromote`, `promote`,
   `repoint`, `unlockPromote`, `releaseRole` lolos gerbang dan ditolak
   tabel peran, supaya operator tidak pernah diberi kode persetujuan untuk
   perintah yang tetap akan ditolak.
3. **`releaseRole` pada `quarantined` memakai `needs_local_approval`**
   dengan teks `set-role`, karena jalan keluarnya perintah root di server;
   `unlockPromote` masuk tabel peran `quarantined` (bad_params seperti
   promosi lain).
4. **Gerbang hanya bertindak saat pin peran terbaca `quarantined`.** Pin
   yang tidak terbaca tetap seperti sekarang (perintah yang dijaga
   ditolak `Allow`, yang lain jalan). Alasan: tidak mengubah perilaku
   server non-karantina; berkas pin milik root.
5. **`setReadOnly`, `stopReplica`, `job.cancel`, `connect`,
   `salvageDelete` tanpa persetujuan.** Dua yang pertama hanya
   mengencangkan atau menghentikan applier; `salvageDelete` disebut spek
   (risiko: panel yang dibobol bisa menghapus berkas salvage, salinan data
   tertinggal). `connect` tidak disebut spek; dibolehkan karena hanya
   menyimpan akun yang bisa masuk ke MySQL lokal dan "Periksa data
   tertinggal" butuh akun yang berfungsi. Bila pemilik ingin lebih ketat,
   pindahkan ke `protected` (satu baris tabel).
6. **`tls*`, `createReplUser`, `createDumpUser`, `dropUser`,
   `sweepDumpUsers` butuh persetujuan** (bawaan tolak). Akibatnya panel
   melewati server dikarantina pada perpanjangan sertifikat harian dan
   menolak "Pasang sertifikat MySQL" di sana (P15); tanpa itu muncul job
   gagal harian dan kode persetujuan yang tidak diminta.
7. **Persetujuan dengan `expires - approved > 24 jam` tidak berlaku.**
   Murah, gagal-tertutup terhadap berkas tulisan tangan.
8. **Alamat yang disetujui:** `source-addr` bila ada, IP bila sumbernya
   IP, selain itu alamat pertama hasil resolusi (IPv4 dulu, terurut); CLI
   menampilkan alamat lainnya. Keabsahan: nama masih teresolusi ke alamat
   itu (di antara yang lain).
9. **Kode tetap selama permintaan berlaku dan sumber tidak berubah;**
   perubahan sumber membuat kode baru. `approvalMu` mencegah dua kode
   dari dua perintah serentak di satu proses.
10. **Log pemakaian pertama per persetujuan per proses agent** (di memori);
    agent yang restart mencatat lagi pada pemakaian berikutnya.
11. **Bukti "sudah dimuat" tetap di memori** (spek 9.1 mengizinkan berkas,
    tidak dikerjakan): agent restart di antara muat dan sambung membuat
    job memuat ulang selama persetujuan berlaku.
12. **`set-role` juga bertanya untuk `replica` dari `quarantined`.** Ia
    juga mengakhiri mode ketat; spek hanya menyebut `master`/`unassigned`.
    `set-role quarantined` tidak bertanya.
13. **Input kosong/EOF pada pertanyaan = batal** (keluar 1), supaya skrip
    tanpa `--ya` tidak menggantung dan tidak mengubah apa pun.
14. **Hasil pelepasan kunci start:** `mysqld` tidak ditemukan →
    "tidak ditemukan" dan peran diubah; tata letak tidak bisa dibaca
    karena sebab lain → "tidak bisa diperiksa", peran tidak diubah
    (gagal-tertutup), dengan rujukan ke README.
15. **CLI pelepasan kunci start memakai `NewRealHost` tanpa kredensial**;
    `agent.yaml` hanya untuk `mysql_socket`, apa pun galatnya diabaikan.
    `cliMySQLHost` tetap untuk `lock-master`/`unlock-master`.
16. **`Host.ExecEnv` ditambahkan ke interface;** `--print-defaults`
    dijalankan dua kali (tahu `user`, lalu dengan `HOME` akun itu).
17. **ProtectHome dikenali dari letak:** home di bawah `/home`, `/root`,
    `/run/user` yang tidak bisa di-`stat` dianggap tersembunyi → tidak
    terkunci. Agent tidak bisa membedakan "tersembunyi" dari "tidak ada"
    di sana; akun dengan home `/home/x` yang memang tidak ada ikut
    dilaporkan tidak terkunci (arah aman).
18. **Aturan awalan hanya untuk argumen `init_file` terakhir** (kalimat
    spek 5.4), kecuali bukti Task 6 menunjukkan bentuk yang berpengaruh
    dari posisi lain.
19. **Berkas hasil start di samping `SeenPath`** (bawaan
    `/var/lib/dbpanel/start-result`), mengikuti pola `SeenPortsPath`,
    sehingga rig test dan agent e2e tidak berbagi berkas sistem.
20. **Hasil yang sama buruknya ditulis ulang** (waktu baru), supaya start
    `late` kedua setelah "Periksa data tertinggal" membuka peringatan lagi
    dari snapshot.
21. **Hasil `failed` baru (`QuarantineStartFailed`) dan snapshot juga
    membuka `master_start_blocked`/`master_start_bypassed`,** bukan hanya
    `late`: urutan spek menyebut keempatnya.
22. **Anti-ganda:** snapshot membuka hanya bila tidak ada alert jenis itu
    dibuka sejak `decided − 1 menit` dan tidak ada "Periksa data
    tertinggal" setelah `decided`; kejadian tidak membuka lagi alert yang
    dibuka dalam 10 menit terakhir dan sudah ditutup.
23. **Antrean 64, coba lagi 5 detik, `enqueue` tidak pernah memblok;**
    `keepLock` menganggap kejadian terkirim saat masuk antrean (antrean
    menahannya sampai terkirim).
24. **Pengirim dijalankan `monitor.Run` lewat `EventSender`,** karena
    `dbpanel agent` dan agent e2e sama-sama menjalankan monitor.
25. **`RunCLI` diekspor** supaya e2e menjawab pertanyaan `approve`/`set-role`
    lewat jalur kode CLI yang sama.
26. **"Hapus server" memutuskan dari peran di panel** (`RoleQuarantined`),
    tanpa bertanya ke agent, supaya berlaku untuk agent lama.
27. **Tombol bangun ulang dinonaktifkan sampai disetujui**, selain
    penolakan di `StartCreateReplica`; "Periksa lagi" adalah tautan muat
    ulang (tanpa JS).
28. **Audit persetujuan juga pada Ulangi/Setujui job bangun ulang**, bukan
    hanya saat mulai: keduanya membuang data.
29. **Pemetaan `needs_local_approval` di tengah job bersifat umum**
    (`callWithin`) dan mengambil kode lewat `mysql.rebuildApproval`.
30. **Uji 5.5 di e2e memakai pembungkus yang membuang kejadian start**,
    karena waktu restart di antara keputusan dan pengiriman tidak bisa
    dipegang di e2e tanpa kait.
31. **Pembagian task mengikuti saran;** perubahan `Pins.SetRole`
    (bersihkan berkas persetujuan) ada di Task 3 bersama aturan yang
    memakainya, pembersihan pin sumber untuk `master` di Task 5.

## Catatan untuk pelaksana

- Urutan: 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8 → 9 → 10 → 11 → 12. Task 6 hanya
  bergantung pada Task 1; Task 9 pada Task 7; Task 10 bebas setelah Task 4
  (memakai `RunCLI` di e2e).
- **`TestCLISetRoleAndSetSource` dan setiap test `set-role master` harus
  men-stub `cliLayoutHost`.** Tanpa stub, `set-role master` di direktori
  sementara kini menemukan `mysqld` sistem (`DiscoverLayout`) dan mencoba
  melepas kunci start di konfigurasi MySQL mesin test.
- Test `set-role` pada pin `quarantined` (`TestSetRoleReplicaWhileDownKeepsStartLock`,
  `TestSetRoleMasterReleasesFirst`, `TestSetRoleMasterRefusedWhileConfigLocked`)
  kini bertanya: tambahkan `--ya`, atau mereka gagal dengan "Dibatalkan".
- **LIFO `defer` di `GuardTick`:** `defer d.promoteMu.Unlock()` didaftar
  sebelum `defer d.sendRoundEvents()`, jadi `sendRoundEvents` berjalan
  sambil `promoteMu` dipegang. Setelah Task 8 ia hanya `enqueue`; jangan
  kembalikan pengiriman ke sana. `selfQuarantine` dan handler
  `mysql.quarantine` juga memegang `promoteMu` saat "mengirim".
- `keepLock` memakai nilai kembali `emit` untuk mencoba lagi; setelah Task
  8 tidak ada nilai kembali (lihat Keputusan 23).
- ±75 pembacaan kejadian di test driver (`ev.all()`, `startEvents`,
  `sends()`, `wantEvents`) mengandalkan pengiriman sinkron. Pasang
  `ev.flush = d.FlushEvents` di rig, jangan ubah satu per satu.
- Keadaan di memori yang tidak dijangkau CLI: cache `Pins` dibaca ulang
  lewat `stat` hanya untuk `role` dan `source` — berkas permintaan dan
  persetujuan dibaca langsung setiap kali (jangan dicache); `loadedFrom`
  (tanda muat bangun ulang) bertahan melewati `set-role`; `approvalLogged`;
  `startLockState`. `set-role` tidak menyentuh agent yang berjalan.
- Flock: `releaseStartLock` memegang flock konfigurasi terkelola
  (`lockManagedConfig`, menunggu sampai 30 detik); agent dan CLI saling
  menunggu. CLI `approve` tidak memakai flock (Keputusan 9, B8 menangani
  perubahan).
- `wantRefused` di `pins_test.go` hanya menerima `bad_params`; pakai
  `wantLocalRefusal` (Task 1) untuk penolakan baru.
- Test yang menjalankan perintah terlindung pada pin `quarantined` butuh
  `approveRebuild` lebih dulu (daftar di Task 3). Sumber test berbentuk IP
  sehingga tidak ada resolusi DNS.
- `newPinRig` sudah memberi `SeenPath` sementara, jadi `start-result`
  ikut sementara; `&mysql.Driver{…}` tanpa `SeenPath` dengan `PinDir` akan
  menulis ke `/var/lib/dbpanel` — beri `SeenPath` di test baru.
- Agent e2e (`e2e_helpers_test.go:463`) membangun `mysql.Driver` sendiri,
  bukan `RegisterMySQL`: `SeenPath` sudah di `ag.dir`; `RunEvents` jalan
  lewat `mon.Run`.
- Fake orchestrator (`fake_test.go`): perintah yang tidak dijawab
  menggagalkan test; tambahkan `mysql.rebuildApproval` ke
  `agentCommands` dan jawaban bawaan di `scriptReplicaTarget`, atau semua
  test bangun ulang yang ada gagal. `fakeOrch` web juga butuh
  `RebuildApproval`.
- Test yang kini menegaskan perilaku lama dan harus diubah:
  `TestQuarantinedRefusalTable` (Release), `TestDeleteQuarantinedRefusedWhileCutoverPending`,
  `TestDeleteQuarantinedDialogSaysLockStays`, `TestGuardSendsReportsAfterReadOnlyAndHousekeeping`,
  `TestGuardFlushesStartReportsOnEveryPath`, `TestQuarantineStartReportRetriedUntilDelivered`,
  `TestCLISetRoleAndSetSource`, tiga test `set-role` di atas, dan e2e yang
  membangun ulang master lama (lewat helper `createReplica`).
- Di luar lingkup tetapi terlihat: `unlock-master` hanya `RESET PERSIST`
  `super_read_only`; `read_only` yang dipersist karantina tetap ON setelah
  restart. Jangan diubah di rencana ini; README langkah manual menyebut
  `SET PERSIST read_only=OFF`.
- `MYSQL_HOME` di lingkungan layanan juga memengaruhi berkas opsi yang
  dibaca mysqld; tidak diperiksa (sama kelasnya dengan R-097).
- Test mysqld memakan menit; `-run` dan `-timeout 30m` saat iterasi;
  paket penuh sebelum commit. Setelah setiap task: review independen;
  setelah Task 12: review akhir cabang dan audit keamanan. Inventaris
  risiko dan dokumen audit diperbarui setelah review akhir, tidak di
  rencana ini.

## Teks baru untuk dibaca pemilik

Setiap teks baru sekali; `<…>` diisi saat dipakai. Kode, nama berkas dan
perintah tetap apa adanya.

**Agent: penolakan yang tampil di panel (juga panel lama)**

- A1: server ini dikarantina (quarantined); perintah mysql.releaseRole ditolak. Karantina hanya lepas lewat bangun ulang yang selesai, atau dengan perintah di server ini: sudo dbpanel agent set-role unassigned (atau sudo dbpanel agent set-role master bila server ini akan menjadi master lagi)
- A2: server ini dikarantina (quarantined) mengikuti sumber `<host:port>`; perintah `<perintah>` butuh persetujuan root di server ini. Jalankan di server ini: sudo dbpanel agent approve `<kode>` (kode berlaku `<N>` menit lagi)
- A3: server ini dikarantina (quarantined); perintah `<perintah>` butuh persetujuan root di server ini, tetapi permintaan persetujuan tidak bisa dibuat: `<sebab>`
- A4: teks lama "server ini dipin sebagai quarantined tanpa sumber yang dipin; perintah `<perintah>` ditolak. Pin master barunya dulu di server ini: sudo dbpanel agent set-source <host:port>" (tidak berubah; kini dengan kode `needs_local_approval`)

**Agent: alasan persetujuan tidak berlaku (A5, tampil di form sebagai P5)**

- server ini tidak dikarantina (peran: `<peran>`)
- persetujuan untuk sumber `<a>`, bukan sumber yang dipin sekarang (`<b>`)
- nama sumber `<host>` sekarang mengarah ke alamat lain, bukan alamat yang disetujui (`<ip>`)
- alamat untuk nama sumber `<host>` tidak ditemukan (DNS): `<sebab>`
- persetujuan sudah habis pada `<waktu>`
- waktu persetujuan `<waktu>` lebih dari 5 menit di masa depan; periksa jam server ini
- masa berlaku persetujuan lebih dari 24 jam
- berkas persetujuan tidak bisa dipakai: `<sebab>`

**Agent: alasan kunci start "belum terjamin" (halaman master lama)**

- opsi `<argumen>` dibaca sesudah baris agent; kunci start tidak terjamin
- akun layanan MySQL `<akun>` tidak ditemukan: `<sebab>`
- direktori home akun layanan MySQL `<akun>` (`<home>`) tidak terlihat oleh agent (ProtectHome); ~/.my.cnf di sana tidak bisa diperiksa
- opsi user di ~/.my.cnf akun layanan MySQL berbeda (`<a>`, bukan `<b>`); kunci start tidak bisa dipastikan

**CLI `dbpanel agent approve`**

- B1 (ditampilkan sebelum bertanya):

```text
Persetujuan bangun ulang (rebuild) server ini

Server ini  : <hostname>
Peran       : dikarantina (quarantined)
Sumber data : <host:port>
Alamat IP   : <alamat>
              (nama sumber juga mengarah ke: <alamat lain>)

Bila disetujui, SEMUA data MySQL di server ini dibuang dan diganti salinan dari sumber di atas. Data yang dibuang tidak bisa dikembalikan.
Pastikan nama dan alamat IP di atas memang master baru Anda, yaitu server yang sekarang dipakai aplikasi.
Bila promote tadi promote darurat, jalankan dulu "Periksa data tertinggal" (dan "Amankan data tertinggal" bila ada) di halaman server ini di panel.
Persetujuan berlaku 24 jam, hanya untuk sumber di atas.

Ketik ya untuk menyetujui, atau apa saja untuk membatalkan: 
```

- B2: Dibatalkan; tidak ada yang disetujui.
- B3: Server ini tidak dikarantina (peran: `<peran>`); persetujuan bangun ulang tidak diperlukan.
- B4: Tidak ada permintaan persetujuan yang berlaku di server ini. Buka form "Bangun ulang sebagai replika" di panel, klik Periksa lagi, lalu jalankan perintah yang ditampilkan.
- B5: Kode salah. Salin perintah dari form "Bangun ulang sebagai replika" di panel.
- B6: Server ini dikarantina tanpa sumber yang dipin; pin master barunya dulu: sudo dbpanel agent set-source <host:port>
- B7: Alamat untuk nama sumber `<host>` tidak ditemukan (DNS: `<sebab>`); persetujuan tidak ditulis.
- B8: Permintaan berubah selama Anda membaca; jalankan perintah ini lagi.
- B9: Pin server ini tidak bisa dibaca (`<sebab>`); persetujuan tidak ditulis.
- B10: Disetujui sampai `<waktu>`. Kembali ke panel dan klik Periksa lagi. Untuk membatalkan: sudo dbpanel agent approve batal
- B11: Persetujuan dan permintaan bangun ulang di server ini dihapus.
- B12: Gagal menulis persetujuan: `<sebab>` · Gagal menghapus: `<sebab>`
- Usage:

```text
  dbpanel agent set-role {master|replica|quarantined|unassigned} [--ya] [--dir DIR]
                                                         ubah peran yang dipin; --ya: tanpa pertanyaan
                                                         pada server yang dikarantina (quarantined)
  dbpanel agent approve <kode> [--dir DIR]               setujui bangun ulang server yang dikarantina;
                                                         kode dari form "Bangun ulang sebagai replika"
  dbpanel agent approve batal [--dir DIR]                hapus persetujuan dan permintaan bangun ulang
```

**CLI `dbpanel agent set-role`**

- C1: Server ini dikarantina (quarantined) mengikuti sumber `<host:port>`. · Server ini dikarantina (quarantined) tanpa sumber yang dipin.
- C2 (`master`): Setelah perintah ini: / - kunci start (start lock) dilepas sekarang, jadi MySQL tidak lagi dipaksa baca-saja (read-only) saat menyala; / - server ini dipin sebagai master dan bisa dibuka untuk ditulis. Bila master baru masih dipakai aplikasi, Anda akan punya dua master yang bisa ditulis.
- C3 (`unassigned`): Setelah perintah ini: / - agent melepas kunci start (start lock) setelah MySQL menyala dengan baca-saja (read-only) tersimpan; / - server ini tanpa peran: panel boleh menyiapkannya ulang, termasuk membuang semua datanya, tanpa persetujuan di server ini.
- C4 (`replica`): Setelah perintah ini: / - agent melepas kunci start (start lock) setelah MySQL menyala dengan baca-saja (read-only) tersimpan; / - server ini dipin sebagai replika: panel boleh membuang datanya dan menyalin ulang dari sumber yang dipin tanpa persetujuan di server ini.
- C5: Ketik ya untuk melanjutkan, atau apa saja untuk membatalkan:
- C6: Dibatalkan; peran tidak diubah.
- C7: Kunci start dilepas: baris init_file dan berkas kuncinya dihapus dari konfigurasi MySQL.
- C8: Kunci start tidak ditemukan; tidak ada yang dilepas.
- C9: MySQL tidak ditemukan di server ini; tidak ada kunci start yang perlu dilepas.
- C10: kunci start tidak bisa diperiksa (`<sebab>`); peran tidak diubah. Bila agent tidak bisa dijalankan, lepas dengan tangan: lihat README, bagian "Melepas kunci start dengan tangan".
- C11: Langkah berikutnya: bila MySQL di server ini masih baca-saja (read-only) dan server ini memang akan dipakai aplikasi sebagai master, pastikan master lain tidak lagi dipakai, lalu buka dengan: sudo dbpanel agent unlock-master
- C12: Langkah berikutnya: bila server ini sudah dihapus dari panel, tambahkan lagi lewat Tambah server, lalu jalankan Siapkan master atau Buat replika. Sampai itu MySQL-nya tetap baca-saja (read-only).

**Panel: form "Bangun ulang sebagai replika", job, sertifikat**

- P1: Persetujuan di server
- P2: Belum disetujui. Jalankan perintah ini di `<server>` sebagai root, lalu klik Periksa lagi: `sudo dbpanel agent approve <kode>` — Kode berlaku `<N>` menit lagi.
- P3: Bangun ulang membuang semua data MySQL di server ini. Persetujuan di server memastikan orang yang memegang server itu melihat dari mana data penggantinya diambil, sehingga panel saja tidak bisa melakukannya.
- P4: Sudah disetujui di `<server>` untuk sumber `<host:port>` (`<alamat>`); berlaku `<H>` jam `<M>` menit lagi. Untuk membatalkan, jalankan di server itu: sudo dbpanel agent approve batal
- P5: Persetujuan yang ada di `<server>` tidak berlaku lagi: `<alasan A5>`.
- P6: Agent perlu dimutakhirkan: agent di `<server>` belum mengenal persetujuan di server. Klik Pasang ulang agent di halaman server itu, jalankan perintahnya, lalu klik Periksa lagi.
- P7: Persetujuan di server tidak bisa diperiksa: agent `<server>` offline. · Persetujuan di server tidak bisa diperiksa sekarang; klik Periksa lagi.
- P8: Agent `<server>` tidak melaporkan server ini dikarantina, jadi persetujuan di server tidak diperlukan.
- P9: Periksa lagi
- P10: Bangun ulang `<server>` butuh persetujuan di server itu. Jalankan di server itu sebagai root: sudo dbpanel agent approve `<kode>`, lalu coba lagi.
- P11: Agent perlu dimutakhirkan: agent di `<server>` belum mengenal persetujuan di server, jadi bangun ulang ditolak. Klik Pasang ulang agent di halaman server itu, jalankan perintahnya, lalu coba lagi.
- P12: Server `<server>` dikarantina (quarantined): langkah ini butuh persetujuan di server itu. Jalankan di server itu sebagai root: sudo dbpanel agent approve `<kode>`, lalu klik Ulangi.
- P12b: Server `<server>` dikarantina (quarantined): langkah ini butuh persetujuan di server itu. Buka form Bangun ulang sebagai replika, klik Periksa lagi, jalankan perintah yang ditampilkan di server itu, lalu klik Ulangi.
- P15: Server ini dikarantina (quarantined); sertifikat MySQL-nya dipasang lagi saat dibangun ulang sebagai replika.

**Panel: dialog "Hapus server"**

- D1: Server ini tetap dikarantina (quarantined) setelah dihapus dari panel: MySQL-nya tetap baca-saja (read-only) dan kunci start (start lock) tetap terpasang. Untuk memakai server ini lagi, jalankan di server itu: sudo dbpanel agent set-role unassigned, atau sudo dbpanel agent set-role master bila server ini akan menjadi master lagi.

**README** (ditulis pelaksana di Task 12 dengan aturan 5.11; dibaca pemilik
pada review akhir): subbagian baru "Persetujuan di server untuk bangun
ulang", "Memakai lagi server yang dihapus saat masih dikarantina",
"Melepas kunci start dengan tangan", dan perubahan di "Peran dan sumber
yang dipin" serta "Master lama".
