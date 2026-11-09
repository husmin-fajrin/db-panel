# Final fix wave report: Plan 6 (c245659..0566027)

Commits:
- ea7916f fix(web): final review wave 1: sidebar status, 390-px tables and chart legend, dialog flow, dates
- 0566027 fix(monitor): bound health report age; banners via template; confirm danger on unlock; escape SSE step selector

Each commit builds and passes the web and monitor tests on its own.

## Security

**SEC-81.** monitor.go has a new `reportAge(serverID, takenAgoMS)`, called under `mu` in `report()`.
- A negative age becomes 0.
- The age is compared as an integer against `EventsRetentionDays` in ms (7 days if the setting is unset) before any multiplication.
- Above the cap it is clamped to the cap, and the clamp is logged once per server (`ageClamped` map).
- Test: `TestReportAgeBounded` (monitor_test.go) sends a live report with `math.MaxInt64`. It asserts that ReceivedAt and every status event are not after now, that `reportAge(MaxInt64)` equals the retention, and that the clamp is recorded. With the old line restored, the test fails ("ReceivedAt ... after now").

**SEC-82.** No `template.HTML` remains in handlers_servers.go.
- `Banner` now has typed fields: `Kind` (`BannerCutover` or `BannerPartial`), `Name`, `ServerID`, `Message` and `JobID`.
- layout.html defines `banner-cutover` and `banner-partial`. The texts and hrefs are unchanged.
- Test: `TestBannerKindsEscaped` (views_ui_test.go) uses a name, message and job id containing `<`, `"` and `<script>`. Everything is escaped and the exact texts are present. `TestDashboardBannersKept` still passes.

**SEC-83.** app.js computes `const step = CSS.escape(String(d.step))` once and uses it in all three selectors (`data-step`, `data-step-badge`, `data-step-message`). Covered by TestE2EBrowserConfirm and a job page run in the tour, with no page errors.

**SEC-84.** The `unlock_master` button in master_detail.html has `data-confirm-danger="1"`. It keeps `btn-warning` and the label "Ya, buka kunci". Test: an assertion was added to the unlock test in web_confirm_test.go.

## UX

**UX1 (sidebar status).**
- `Page` now has `ServersTotal` (every registered server), `ServersConnected` and `AllConnected()`.
- router.go counts a server as connected only when it is paired, `Hub.IsOnline`, and, if the monitor monitors it, not `View.AgentAway()`.
- `AgentAway()` is new in monitor: agent offline, presumed down for agent offline, or awaiting the first report.
- The sidebar says "Semua agent tersambung · N server" only when every server is connected. Otherwise it says "K dari N server tersambung", with a warning dot and `text-warning-text`.
- Tests:
  - `TestLayoutAgentStatus` covers the unpaired db-c: 2 servers read "Semua…", and adding an agentless third reads "2 dari 3".
  - `TestLayoutAgentStatusFromMonitor` covers presumed down from agent offline and awaiting report, each reading "1 dari 2".
  - `TestDashboardCounts` was updated.
- Screenshot `16-replikasi-terang-1440` now reads "3 dari 5 server tersambung" in warning tone, with db-gudang presumed down.

**UX2 (lag chart).**
- Inside `lag-chart`, only the SVG sits in the scroll box: `.grafik-lag-geser overflow-x-auto > min-w-[480px]`. The scroll box carries the old 752 px cap.
- The legend sits below, outside the box. It now starts with the peak: a dot plus "puncak N dtk · HH:MM". The in-SVG peak text and its placement helpers (PeakAnchor, PeakLabelX/Y, `.grafik-label-puncak`) are removed. The dot stays in the SVG.
- dashboard.html no longer wraps the chart.
- Test: `TestLagChartTemplate` asserts that the legend entries for the peak, the threshold and the series are outside the scroll box, and that the CSS cap is in place.
- Screenshot `02-dashboard-terang-390`: the legend and "ambang 60 dtk" show whole under the chart. The plot itself still scrolls, as designed.

**UX3 (job tables).**
- The layout `job-table` (used by jobs, master and replica pages) is now Pekerjaan, Status, Server, Dibuat, Selesai, with Selesai `hidden md:table-cell`.
- The dashboard's own "Pekerjaan terakhir" table also has Status second.
- Tests: `TestJobDetailTimeline` (header order, badge before server, hidden Selesai) and `TestDashboardHeaderAndLists` (dashboard header).
- Screenshots at 390: `14-jobs` and `02-dashboard` show Status fully.

**UX4 (audit table).** Column order is now Waktu, Aksi, Hasil, Pengguna, IP, Target, Detail. Test: `TestAuditPageFilters` checks the header and the cell order. Screenshot `12-audit-terang-390` shows Waktu, Aksi and Hasil fully.

**UX5.** replica_new.html now uses `text-warning-text`. No template has bare `text-warning`, `text-error` or `text-success` left.

**UX6 (focus ring).** app.css adds `.menu li > a.menu-active:focus-visible { outline: 2px solid var(--color-base-content); outline-offset: 2px }`, which is unlayered. It is asserted in `TestBuiltCSSMenuAndFocus`.

**UX7.** In audit_summary.go, `mode` is mapped with `sumMode`: planned becomes terencana, emergency becomes darurat, and other values pass through. Test: `TestAuditDetailSummaryPromote` covers terencana, darurat and an unknown value.

**UX8 (dialog flow).**
- confirmModal closes the open source `<dialog>` before `showModal()`.
- On close (Batal or Esc), `kembali()` reopens the source and returns focus to the trigger.
- New helper `fokuskan()` retries `focus()` each frame, up to 20 frames. The daisyUI `.modal` is not yet visible in the frame of `showModal()`, so a plain `focus()` there fails silently. This also affected the confirm's own first-field focus.
- Test: scripts/e2e_browser_confirm.js asserts that the resync dialog is closed under the confirmation, that Batal reopens it with focus on "Ya, resync ulang", and that a second click continues the flow. TestE2EBrowserConfirm passes.
- Screenshot `15-dialog-konfirmasi-terang-390`: only the confirmation shows over the page, with no "Lepas replika?" box behind it.

**UX9.** The tour collects OVERFLOW lines (still printed) and throws after all passes, so it exits 1 and the Go test calls `t.Fatalf`. The README is updated. The run had no overflow.

**UX10.** cutover.html, server_detail.html (including the MySQL cert expiry, previously `Format "2006-01-02"`), servers.html, master_detail.html, replica_detail.html, _connections.html and _timeline.html now use fmtDateTime. Test: the TLS expiry assertion in web_mysql_jobs_test.go now expects dd-mm-yyyy HH:MM.

**Alert counter.** The sidebar badge is now `lencana lencana-warning mono`. Seen in `16-replikasi-terang-1440`.

**README "Tampilan".** It now notes:
- After a choice made with "Ganti tema" there is no way back to "ikuti sistem"; clearing the site data does it.
- Native date inputs follow the browser or OS locale. The 11/02/2026 example is given, with advice to set the locale to Indonesian.

## Commands (tails)
- `bash scripts/tailwind.sh`: `Tailwind v4.1.14 + daisyUI v5.1.29 -> web/static/app.css`. app.css is committed.
- `go test ./internal/panel/... -race -short`: all ok, panel 98s and web 99.6s. In one earlier run, agentapi's `agentapi_test.go:535` ("replacement not logged at Warn") failed while the whole suite ran under load. It passed with `-count=3` and in the final full run. That code is untouched; the test looks timing-sensitive.
- `go test -tags e2e_browser ./internal/panel/ -run TestE2EBrowserConfirm`: `--- PASS (34.7s)`.
- `DBPANEL_SCREENSHOT_DIR=.../scratchpad/screens-final2 go test -tags e2e_browser ./internal/panel/ -run TestE2EScreenshotTour -timeout 30m`: `--- PASS (171.6s)`, 64 PNGs, no OVERFLOW.
- `go vet ./...`: clean. `gofmt -l`: clean. `graphify update .` was run.

## Left as is / notes
- Job pages (job_detail, job tables, job log) keep `fmtTime` (yyyy-mm-dd HH:MM:SS), because the ruling covered cutover and server pages only.
- The chart's aria-label keeps ISO dates.
- At 390 px the lag chart plot still scrolls inside its box. The ruling moved only the labels and the legend out.
