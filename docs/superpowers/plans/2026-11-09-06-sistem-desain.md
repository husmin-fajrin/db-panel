# Rencana Implementasi 6: Sistem Desain dan Rapi-Tampilan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development (recommended) or executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Mengganti kulit panel dengan sistem desain D (dua tema), tata
letak sidebar, dashboard bergaya aaPanel (gauge, grafik lag rapat dari
sampel agent, kartu topologi), dan memperbaiki enam cacat kosmetik, tanpa
mengubah rute, form, nama tombol, atau kalimat keputusan.

**Architecture:** Dua tema daisyUI kustom (`dbpanel-gelap`, `dbpanel-terang`)
dari token di `web/src/app.css`; huruf dibundel; `layout.html` menjadi
sidebar + konten dengan `theme.js` statis yang menyetel `data-theme`
sebelum render. Komponen template bersama (gauge, grafik SVG, kartu,
lencana, banner) dipakai semua halaman. Riwayat lag datang dari event
agent baru `monitor.sample` ke tabel `health_samples` (migrasi 0010),
dikueri per bucket 5 menit untuk grafik. Kelas yang dipancarkan dari Go
dipetakan ke token baru.

**Tech Stack:** Go 1.26 html/template, Tailwind v4 + daisyUI v5
(`scripts/tailwind.sh`, tanpa Node), Alpine CSP build, SVG inline, SQLite;
Playwright/Chromium untuk tur tangkapan layar.

**Spec:** `docs/superpowers/specs/2026-11-09-rencana-6-sistem-desain-design.md`
(§5.1 token, 5.2 huruf, 5.3 spasi, 5.4 tata letak, 5.5 komponen, 5.6
halaman, 5.7 data, 5.8 cacat wajib, 5.9 aksesibilitas, 5.10 verifikasi,
§8 keputusan 8.1 b dan 8.2 b).

## Global Constraints

- CSP tetap `default-src 'self'; script-src 'self'; style-src 'self'`
  (router.go `contentSecurityPolicy`): tanpa inline script/style, tanpa
  aset eksternal; huruf di `web/static/fonts/` (WOFF2 subset Latin, total
  ≤ 400 KB, `LICENSE` OFL).
- Token warna persis §5.1 (gelap/terang): base-100 `#12121F`/`#FFFFFF`,
  base-200 `#0A0A14`/`#F4F6F9`, base-300 `#2A2A3E`/`#DDE2EA`, base-content
  `#EEEEF0`/`#141424`, redup `#9999AA`/`#5E6577`, primary `#00F0FF`
  (konten `#0A0A14`)/`#0A7F93` (konten `#FFFFFF`), success
  `#39FF14`/`#1E9E2E`, warning `#FFD600`/`#C98A00`, error `#FF006E`
  (teks `#FF4D94`)/`#E0005F` (teks `#B8004E`), neutral `#1A1A2E`/`#E9EDF2`.
- Pink (`error`) hanya untuk bahaya/destruktif/gagal/kritis; cyan
  (`primary`) untuk aksi utama, tautan, menu aktif, baris terpilih.
- Huruf: Bungee hanya logo (18 px) dan H1 (28 px); Work Sans untuk teks
  (body 14/1.5, H2 16/600, bantu 12, tombol 14/600); Space Mono untuk
  angka/waktu/lag/GTID/Detail teknis.
- Spasi skala 8; kartu padding 24, jarak 16, margin konten 32 (telepon
  16); radius kartu/tombol/input 8, lencana 4, modal 12; tanpa bayangan
  kartu; glow hanya busur gauge (`drop-shadow(0 0 6px <warna> 35%)`, tema
  gelap); tanpa animasi kecuali transisi ≤ 150 ms yang dipicu pengguna,
  dimatikan oleh `prefers-reduced-motion`.
- Nama tombol, rute, form, dan kalimat keputusan Rencana 4 tidak berubah
  (tombol promote tetap "Promote", dialog tetap "Konfirmasi").
- Semua teks Indonesia. Kontras 4.5:1 (3:1 ≥ 24 px) di kedua tema.
- Lebar 390 px dan 1280 px tanpa gulir horizontal halaman.
- Pengaturan baru: `nama_instansi` (≤ 60 karakter), `tema_bawaan`
  (`sistem`|`gelap`|`terang`, bawaan `sistem`), `samples_retention_days`
  (bawaan 7, rentang 1–90).
- `health_samples`: `server_id, ts, lag_sec INTEGER NULL, io_running TEXT,
  sql_running TEXT, mysql_ok INTEGER`, kunci unik `(server_id, ts)`,
  `INSERT OR IGNORE`; bucket grafik 5 menit `MAX(lag_sec)`, ≤ 288 titik.
- Event agent `monitor.sample` ≤ 200 byte, satu per pemeriksaan; tidak
  memengaruhi status, peringatan, atau Telegram.

## Review Focus

1. Lebar telepon 390 px: sidebar menjadi bar atas dengan menu lipat,
   grid gauge 2×2, tabel lebar bergulir di kotaknya, tidak ada gulir
   horizontal halaman. Test di Task 1 (layout) dan Task 8 (tur 390 px).
2. Agent lama tanpa `monitor.sample`: dashboard tetap tampil, grafik dari
   laporan kesehatan saja dengan catatan "grafik jarang: agent perlu
   dimutakhirkan"; tidak ada error. Test di Task 3 dan Task 4.
3. Beberapa topologi: gauge agregat benar (lag tertinggi menyebut
   replikanya, disk terburuk menyebut masternya), tab tersimpan di
   `localStorage`, tab tersembunyi bila satu topologi. Test di Task 4.
4. Pengalih tema: tidak berkedip (atribut diset sebelum render), nilai
   tidak valid di `localStorage` diabaikan, CSP tidak dilonggarkan,
   `tema_bawaan` dipakai bila pengguna belum memilih. Test di Task 1.
5. Kelas yang dipancarkan dari Go (`severityClass`, `statusBadges`,
   `toneOf`) harus ada di CSS akhir (daftar `@source inline`), kalau tidak
   Tailwind memangkasnya dan lencana tampil polos. Test di Task 2.

---

### Task 1: Fondasi: huruf, dua tema, pengalih tema, layout sidebar

**Files:**
- Create: `web/static/fonts/{bungee-regular,worksans-400,worksans-500,worksans-600,spacemono-400,spacemono-700}.woff2`, `web/static/fonts/LICENSE`, `web/static/theme.js`
- Modify: `web/src/app.css` (hapus `themes: corporate`; dua tema `@plugin "daisyui/theme"` dengan token §5.1; `@font-face` ×6; aturan global huruf, H1 Bungee, `.mono`, `prefers-reduced-motion`), `web/templates/layout.html` (sidebar + bar atas telepon + kotak status bawah + pengalih tema + Keluar; hapus `data-theme="corporate"`; `<script src="/static/theme.js">` sinkron di `<head>`; banner lewat partial `banner`), `web/templates/login.html` dan `cert.html` (tanpa sidebar, kartu tengah), `internal/panel/web/render.go` (`Page` += `Instansi string`, `ThemeDefault string`, `ServersOnline, ServersTotal int`, `Active string` untuk menu), `internal/panel/web/router.go` (sajikan `/static/fonts/*`, `/static/theme.js`; `Active` per rute), `internal/panel/store/settings.go` (`SettingNamaInstansi = "nama_instansi"`, `SettingTemaBawaan = "tema_bawaan"`), `internal/panel/web/handlers_settings.go` + `settings.html` (bagian "Tampilan": nama instansi ≤ 60, tema bawaan enum), `scripts/tailwind.sh` (tidak berubah kecuali komentar), `web/embed.go` (fonts termasuk embed)
- Test: `internal/panel/web/web_layout_test.go`, `internal/panel/web/web_test.go` (`TestStaticAssets` += fonts, theme.js), `internal/panel/web/contrast_test.go`

**Interfaces:**
- Produces: `Page.Instansi`, `Page.ThemeDefault`, `Page.Active`; template `layout` dengan blok `{{define "page-title"}}` (H1) dan `{{define "page-actions"}}`; partial `{{define "banner" .}}` dengan `.Tone` (`info|success|warning|error`) dan `.Text`/`.HTML`; helper Go `func themeTokens() map[string]map[string]string` (sumber kebenaran token untuk tes kontras, nilai sama dengan `app.css`).

- [ ] **Step 1: Tulis test** `TestLayoutSidebarAndActiveMenu` (halaman dashboard memuat `<nav aria-label="Menu utama">`, tujuh tautan menu, menu aktif memakai kelas `menu-active`, logo "dbpanel", nama instansi tampil bila pengaturan diisi dan tidak ada bila kosong), `TestThemeScriptAndAttribute` (`/static/theme.js` 200 dengan `Content-Type` JS; `<html>` tanpa `data-theme` statis dan dengan `data-tema-bawaan="{{tema_bawaan}}"`; CSP header tidak berubah), `TestStaticAssetsFonts` (enam WOFF2 200, total ≤ 400 KB, `LICENSE` 200), `TestContrastTokens` (setiap pasangan teks/latar di `themeTokens()` ≥ 4.5 untuk kedua tema: content/base-100, content/base-200, redup/base-100, primary-content/primary, teks-error/base-100, warning-content/warning), `TestSettingsTampilan` (simpan nama instansi 61 karakter → 400; tema bawaan `ungu` → 400; `gelap` → tersimpan dan `data-tema-bawaan="gelap"`). `TestLayoutMobileMenu` (layout memuat tombol menu `aria-controls="menu-utama"` dengan `aria-expanded`, sidebar berkelas `hidden md:flex`, dan grid gauge dashboard memakai `grid-cols-2 lg:grid-cols-4`).
- [ ] **Step 2: Jalankan, pastikan gagal.** `go test ./internal/panel/web/ -run 'Layout|Theme|StaticAssetsFonts|Contrast|SettingsTampilan' -v` → FAIL.
- [ ] **Step 3: Implementasi.** Unduh subset WOFF2 dari sumber resmi (google/fonts di GitHub, `pyftsubset` dengan `--unicodes=U+0000-00FF,U+2000-206F,U+2190-21FF` bila perlu); `theme.js`: baca `localStorage.tema` ∈ {gelap, terang} → set `data-theme=dbpanel-<x>`; selain itu baca `data-tema-bawaan`: `gelap|terang` → set; `sistem` → `matchMedia('(prefers-color-scheme: dark)')`; tombol di sidebar (`x-data="themeToggle"` di `app.js`) menulis `localStorage.tema` dan atribut. Layout: sidebar `<nav>` 240 px (`md:` ke atas), bar atas dengan tombol menu (Alpine `x-data="sideMenu"`) di bawah `md`; konten `max-w-[1440px]`.
- [ ] **Step 4: `bash scripts/tailwind.sh`; `go test ./internal/panel/web/ -race`; `go vet ./...`** → semua lulus; tes lama yang memeriksa kelas `corporate` disesuaikan.
- [ ] **Step 5: Commit** — `git commit -m "feat(web): design tokens, bundled fonts, theme switch, sidebar layout"`

---

### Task 2: Komponen bersama: gauge, grafik, lencana, kartu, banner, kotak kosong, dialog

**Files:**
- Create: `web/templates/_gauge.html` (`define "gauge"`), `web/templates/_chart.html` (`define "lag-chart"`), `web/templates/_ui.html` (`define "card-head"`, `define "badge"`, `define "empty"`, `define "icon"`), `internal/panel/web/views_ui.go`
- Modify: `web/templates/_status.html`, `_timeline.html`, `_confirm.html` (gaya §5.5: radius 12, kotak peringatan `error` bila destruktif, tombol konfirmasi `error`/`primary` sesuai `data-confirm-danger`), `web/src/app.css` (`@source inline` diperbarui untuk kelas dari Go), `internal/panel/web/views_monitor.go` (`toneOf`, `severityClass` → kelas tema baru), `internal/panel/web/handlers_jobs.go` (`statusBadges`)
- Test: `internal/panel/web/views_ui_test.go`, `web_monitor_test.go` (`TestTemplatesParse` tetap), `web_test.go`

**Interfaces:**
- Produces (Go, `views_ui.go`):
  - `type Gauge struct{ Label, Sub, Value, Tone, Aria string; Percent float64 }`; `func gaugeDash(percent float64) string` (dasharray untuk r=34, keliling 213.6; 0 ≤ percent ≤ 100 dijepit).
  - `type ChartSeries struct{ Name, Tone string; Points []ChartPoint; Dashed bool }`, `type ChartPoint struct{ T time.Time; Lag *int64 }`, `type LagChart struct{ Series []ChartSeries; From, To time.Time; MaxY int64; ThresholdSec int; Peak *ChartPoint; PeakSeries string; Sparse bool }`
  - `func chartPath(s ChartSeries, c LagChart, w, h float64) []string` (segmen `M…L…` terpisah di setiap celah data > 2 bucket; nilai nil memutus segmen); `func chartTicks(c LagChart) []tick`.
  - Template `gauge` menerima `Gauge`; `lag-chart` menerima `LagChart`; `badge` menerima `{Tone, Text}`; `empty` menerima `{Text, Action, Href}`.
  - Warna seri: urutan `primary, warning, success`, lalu berulang dengan `Dashed=true`.

- [ ] **Step 1: Tulis test** `TestGaugeDash` (0 → "0 213.6", 50 → "106.8 213.6", 150 dijepit 100), `TestChartPathBreaksOnGaps` (seri dengan celah 3 bucket menghasilkan 2 segmen; nil memutus), `TestChartMaxYFromThreshold` (MaxY = max(ambang×1.5, puncak) dibulatkan ke 10), `TestGoEmittedClassesInCSS` (setiap kelas yang dikembalikan `severityClass`, `statusBadges`, `toneOf` untuk semua nilai muncul di `web/static/app.css` yang dibangun), `TestConfirmDialogDangerStyle` (dialog dengan `data-confirm-danger="1"` memakai tombol `btn-error`, tanpa itu `btn-primary`).
- [ ] **Step 2: Jalankan, pastikan gagal.**
- [ ] **Step 3: Implementasi** partial dan helper; SVG gauge 84×84 sesuai mockup (`role="img"`, `aria-label` dari `Gauge.Aria`); grafik `viewBox 0 0 640 200` dengan grid, label jam tiap 6 jam, legenda, titik + label puncak bila `Peak != nil`; `Sparse` menambah catatan "grafik jarang: agent perlu dimutakhirkan".
- [ ] **Step 4: `bash scripts/tailwind.sh`; `go test ./internal/panel/web/ -race`; `go vet ./...`.**
- [ ] **Step 5: Commit** — `git commit -m "feat(web): shared gauge, lag chart, badge, banner and dialog components"`

---

### Task 3: Riwayat lag: event `monitor.sample`, tabel `health_samples`, retensi

**Files:**
- Create: `internal/panel/store/migrations/0010_health_samples.sql`, `internal/panel/store/samples.go`
- Modify: `internal/proto/health.go` (`EventSample = "monitor.sample"`, `type SampleEvent struct{ TakenAgoMS int64; LagSec *int64; IORunning, SQLRunning string; MySQLOK bool }`), `internal/agent/monitor/monitor.go` (`tick`: setelah `Snapshot`, kirim `monitor.sample` setiap pemeriksaan, terlepas dari apakah laporan kesehatan dikirim; tidak masuk backlog), `internal/panel/monitor/monitor.go` (`HandleEvent`: `monitor.sample` → `Store.AddHealthSample`; `ingest` laporan kesehatan juga menulis sampel dari snapshot agar agent lama tetap memberi data jarang), `internal/panel/panel.go` (retensi harian += `PruneHealthSamples` dengan `samples_retention_days`), `internal/panel/store/settings.go` (`SettingSamplesRetentionDays = "samples_retention_days"`, bawaan 7, validasi 1–90), `internal/panel/web/handlers_settings.go` + `settings.html` (kolom Retensi sampel), `internal/panel/hub/hub.go` (tidak berubah: `monitor.sample` bukan `monitor.snapshot`, jadi tidak dibatasi ember; catat di komentar `allowReport`), `README.md`
- Test: `internal/panel/store/samples_test.go`, `internal/agent/monitor/monitor_test.go`, `internal/panel/monitor/monitor_test.go`, `internal/panel/panel_test.go`

**Interfaces:**
- Produces: `func (s *Store) AddHealthSample(h HealthSample) error` (`type HealthSample struct{ ServerID int64; TS time.Time; LagSec *int64; IORunning, SQLRunning string; MySQLOK bool }`, `INSERT OR IGNORE`); `func (s *Store) LagSeries(serverIDs []int64, from, to time.Time, bucket time.Duration) (map[int64][]LagPoint, error)` (`type LagPoint struct{ T time.Time; Lag *int64 }`, `MAX(lag_sec)` per bucket, bucket kosong tidak dikembalikan); `func (s *Store) PruneHealthSamples(olderThan time.Time) (int64, error)`; `func (s *Store) HasSamples(serverID int64, since time.Time) (bool, error)` (untuk `Sparse`).

- [ ] **Step 1: Tulis test** `TestAddHealthSampleIgnoresDuplicate` (dua sampel `(server, ts)` sama → satu baris), `TestLagSeriesBuckets` (sampel 10, 12, 25 detik dalam satu bucket 5 menit → satu titik 25; bucket tanpa data tidak ada; nil tetap nil bila semua nil), `TestPruneHealthSamplesKeepsRecent`, `TestAgentEmitsSampleEveryCheck` (3 pemeriksaan tanpa perubahan status → 3 `monitor.sample`, ≤ 1 `monitor.snapshot`; ukuran JSON ≤ 200 byte), `TestPanelStoresSampleAndSnapshotLag` (event sampel → baris; laporan kesehatan → baris pada `ts` laporan), `TestRetentionPrunesSamples` (loop harian memanggil prune dengan `samples_retention_days`; audit_log dan job_log tidak tersentuh), `TestSamplesRetentionValidation` (0 dan 91 → 400).
- [ ] **Step 2: Jalankan, pastikan gagal.**
- [ ] **Step 3: Implementasi** migrasi 0010 (tabel + indeks unik `(server_id, ts)`), store, proto, agent, panel monitor, retensi, pengaturan.
- [ ] **Step 4: `go test ./internal/panel/... ./internal/agent/monitor/ -race`; `go vet ./...`.**
- [ ] **Step 5: Commit** — `git commit -m "feat(monitor): lag samples from the agent, health_samples table, retention (migration 0010)"`

---

### Task 4: Dashboard baru

**Files:**
- Modify: `web/templates/dashboard.html` (gauge agregat ×4, tab per topologi dengan grafik + kartu topologi, tabel Peringatan 3 dan Pekerjaan 3, banner yang ada, kotak kosong), `internal/panel/web/handlers_servers.go` (`dashboard`: hitung `dashboardPage` baru), `internal/panel/web/views_monitor.go` (`statusView`: label "Mengejar" bila Sinkron tetapi detail menyebut transaksi tertinggal), `web/static/app.js` (`Alpine.data("topoTabs")`: pilihan di `localStorage.topoTab`), `README.md`
- Test: `internal/panel/web/web_dashboard_test.go`, `web_monitor_test.go`

**Interfaces:**
- Produces: `type dashboardPage struct{ Page; Gauges [4]Gauge; Topologies []topologyPanel; Alerts []alertRow; Jobs []jobRow; Empty bool }`, `type topologyPanel struct{ ID int64; Name string; Master masterCard; Replicas []replicaCard; Chart LagChart }`; `func (a *app) dashboardGauges(topos []topologyPanel, openAlerts []store.Alert) [4]Gauge` (sinkron total/semua; lag tertinggi + nama replika; disk master terburuk + nama master; peringatan terbuka + judul terbaru).

- [ ] **Step 1: Tulis test** `TestDashboardGaugesAggregate` (dua topologi: lag 3 dan 12 → gauge lag "12s" menyebut replika pemiliknya; disk 41% dan 77% → "77%" menyebut master-nya; sinkron "3/4"), `TestDashboardTabsHiddenForOneTopology` (satu topologi → tidak ada `role="tablist"`; dua → ada), `TestDashboardChartSparseNote` (server tanpa sampel 24 jam → teks "grafik jarang"), `TestDashboardEmptyState` (tanpa topologi → "Belum ada replika" + tautan tambah), `TestStatusLabelMengejar` (status Sinkron dengan detail "tertinggal 8 transaksi" → label "Mengejar"; tanpa detail → "Sinkron"; kode status tidak berubah), `TestDashboardBannersKept` (promote_partial dan cutover_pending masih tampil).
- [ ] **Step 2: Jalankan, pastikan gagal.**
- [ ] **Step 3: Implementasi.** Grafik memakai `Store.LagSeries` 24 jam, bucket 5 menit, untuk replika topologi; `Sparse` = ada replika tanpa `HasSamples` 1 jam terakhir.
- [ ] **Step 4: `bash scripts/tailwind.sh`; `go test ./internal/panel/web/ -race`; `go vet ./...`.**
- [ ] **Step 5: Commit** — `git commit -m "feat(web): aaPanel-style dashboard with gauges, lag chart and topology tabs"`

---

### Task 5: Halaman Replikasi, Server, detail server, master, replika

**Files:**
- Create: `web/templates/replication.html`, `internal/panel/web/handlers_replication.go`
- Modify: `internal/panel/web/router.go` (`GET /replikasi`), `web/templates/servers.html` (kolom lencana sendiri, `whitespace-nowrap` nama, server lokal "server panel"), `server_detail.html` (kepala, kartu MySQL dengan baris Layanan gabungan, "Tindakan lanjutan" lipat berisi Siapkan ulang, Detail teknis lipat Space Mono, kotak Master lama sebagai kartu `warning`), `master_detail.html` (kartu topologi + tabel replika + aksi + tombol Promote dengan teks mode), `replica_detail.html` (banner `needs_*`), `internal/panel/web/handlers_mysql.go` (`serverDetail`: `ServiceLine` dari `ServiceActive` + `MySQLOK`), `views_monitor.go`
- Test: `web_replication_test.go`, `web_mysql_test.go`, `web_oldmaster_test.go` (tetap lulus)

**Interfaces:**
- Produces: `func (a *app) replication(w, r)`; `type serviceLine struct{ Text, Tone string }`; `func serviceLineOf(serviceName string, active bool, mysqlOK bool) serviceLine` (MySQL menjawab + unit aktif → "MySQL menjawab · layanan systemd `<nama>` aktif" `success`; menjawab + unit tidak aktif/tidak ada → "MySQL menjawab · layanan systemd tidak terdeteksi (mungkin bukan systemd)" `neutral`; tidak menjawab → "MySQL tidak menjawab" `error`).

- [ ] **Step 1: Tulis test** `TestReplicationListAndRedirect` (dua topologi → daftar; satu → 303 ke `/masters/{id}`), `TestServiceLineOf` (tiga kasus di atas), `TestPrepareAgainUnderAdvanced` (master: tombol ada di `<details>` "Tindakan lanjutan" dengan keterangan; bukan master: tombol utama seperti sebelumnya), `TestServersListLocalLabel` (server lokal berlabel "server panel", bukan "Belum dipasangkan"), `TestOldMasterCardTone` (role quarantined → kartu dengan `border-warning`).
- [ ] **Step 2–4.** Implementasi; `bash scripts/tailwind.sh`; `go test ./internal/panel/web/ -race`; `go vet ./...`.
- [ ] **Step 5: Commit** — `git commit -m "feat(web): replication page, server and topology pages in the new design"`

---

### Task 6: Promote, pengalihan, job dalam desain baru

**Files:**
- Modify: `web/templates/promote.html` (kartu "Apa yang akan terjadi" dengan tiga angka + urutan langkah; tabel kandidat kolom lengkap, lencana di bawah nama, baris terpilih `bg-primary/5`, kotak `error` + tombol "Promote"), `cutover.html` + `_connections.html` (dua kolom, lencana akun admin panel, segar 15 detik tetap), `job_detail.html` + `jobs.html` (garis waktu langkah, log Space Mono), `web/static/app.js` (`promotePicker` menyorot baris terpilih)
- Test: `web_promote_test.go`, `web_cutover_test.go`, `web_jobs_test.go` (yang ada tetap lulus; tambahan di bawah)

- [ ] **Step 1: Tulis test** `TestPromotePageColumnsAndBadges` (kolom "Bisa dipilih?" ada; lencana `Paling mutakhir` berada dalam elemen terpisah di bawah nama, tidak di dalam `<a>` nama; tombol bernama "Promote"), `TestPromotePageStepsList` (daftar `<ol>` enam langkah), `TestJobDetailTimeline` (langkah sebagai `<ol class="timeline">` dengan ikon status).
- [ ] **Step 2–4.** Implementasi; `bash scripts/tailwind.sh`; `go test ./internal/panel/web/ -race`; `go test -tags e2e_browser ./internal/panel/ -run TestE2EBrowserConfirm` (dialog disentuh).
- [ ] **Step 5: Commit** — `git commit -m "feat(web): promote, cutover and job pages in the new design"`

---

### Task 7: Peringatan, audit, pengaturan, login, sertifikat

**Files:**
- Modify: `web/templates/alerts.html` (chip filter, dropdown dengan panah di luar teks), `audit.html` (peta label aksi lengkap, kolom Detail ringkas + `<details>` JSON, `<input type="date">`), `internal/panel/web/handlers_audit.go` + `views_monitor.go` (`auditActionLabel` lengkap untuk semua aksi yang dicatat `audit()`; `auditDetailSummary(action string, detailJSON string) string`), `settings.html` + `handlers_settings.go` (kartu per bagian, grid 2 kolom, bagian Tampilan dari Task 1, Retensi sampel dari Task 3), `login.html`, `cert.html`
- Test: `web_audit_test.go`, `web_settings_test.go`, `web_alerts_test.go`

- [ ] **Step 1: Tulis test** `TestAuditActionLabelsComplete` (setiap konstanta aksi yang dipanggil `audit(`/`auditAs(` di paket web punya label Indonesia; uji dengan daftar yang dipindai dari kode atau daftar tetap yang diperbarui), `TestAuditDetailSummaryPromote` (`{"candidate_id":3,"accept_partial":true}` → "kandidat: #3; terima sebagian: ya"), `TestAuditDateInputs` (`type="date"`), `TestSettingsNoOverflowStructure` (setiap bagian dalam `<section class="card">`; tidak ada `w-[`/lebar tetap > 100%).
- [ ] **Step 2–4.** Implementasi; `bash scripts/tailwind.sh`; `go test ./internal/panel/web/ -race`.
- [ ] **Step 5: Commit** — `git commit -m "feat(web): alerts, audit, settings, login and certificate pages in the new design"`

---

### Task 8: Verifikasi visual, README, graph

**Files:**
- Modify: `internal/panel/e2e_screens_test.go` + `scripts/e2e_screenshot_tour.js` (dua tema × dua lebar 1440/390; halaman `/replikasi` ditambah sebagai `16-replikasi`; nama berkas `<nn>-<halaman>-<tema>-<lebar>.png`), `README.md` (bagian "Tampilan": tema, huruf dibundel, pengalih, nama instansi, grafik lag dan sampel, retensi sampel), `Makefile` (target `screens`: menjalankan tur dengan `DBPANEL_SCREENSHOT_DIR`)
- Test: tur tangkapan layar; `make test`; `make test-e2e`

- [ ] **Step 1:** Perluas tur; jalankan `DBPANEL_SCREENSHOT_DIR=… go test -tags e2e_browser ./internal/panel/ -run TestE2EScreenshotTour -timeout 20m`; periksa setiap PNG: tidak ada gulir horizontal pada 390 (lebar gambar = 780 px pada skala 2), teks terbaca di kedua tema, tidak ada halaman error.
- [ ] **Step 2:** `make test` lulus; `make test-e2e` lulus (nama tombol dan teks keputusan tidak berubah, jadi e2e Rencana 3–4c lulus tanpa perubahan asersi; bila ada asersi kelas CSS yang gagal, sesuaikan asersi itu dan catat di laporan).
- [ ] **Step 3:** README; `graphify update .`; commit — `git commit -m "test(e2e): screenshot tour in both themes and widths; docs for Plan 6"`

## Catatan untuk pelaksana

- Urutan: 1 → 2 → 3 → 4 → 5 → 6 → 7 → 8. Task 3 bebas setelah 1 (tidak
  menyentuh template) dan boleh paralel dengan 2 bila dikerjakan oleh
  pelaksana berbeda; Task 4 butuh 2 dan 3.
- Setiap task yang mengubah template menjalankan `bash scripts/tailwind.sh`
  dan mengomit `web/static/app.css`.
- Jangan mengubah nama tombol, rute, atau kalimat keputusan; bila sebuah
  tes lama menegaskan kelas CSS lama (`corporate`, `badge-ghost`), ubah
  asersi ke kelas baru dan catat di laporan task.
