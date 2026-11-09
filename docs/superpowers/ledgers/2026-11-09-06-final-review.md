# Final review and security audit: Plan 6 (f7b0dbc..c245659)

## Summary verdict
The branch is sound and close to the spec. I checked these:
- The CSP string is byte-identical. No template or static JS has `style=`, an inline script, an `on*=` handler or an external URL. SVG uses presentation attributes only. Fonts are self-hosted under an OFL licence.
- Every new value that reaches a template (instansi name, audit summary and JSON, server names in gauges, chart, aria-label and SVG `<text>`) goes through html/template's contextual escaping. Tone classes come from a whitelist (`toneName`).
- `/replikasi` and both fragments are behind `authed()`.
- I diffed every button label, form action and field name between f7b0dbc and c245659. Nothing was removed or renamed; there are only additions.
- `monitor.sample` is safe. The 1 KiB cap applies before decode, the 60 s age check is an integer comparison, the 5 s per-server guard holds, inserts are `INSERT OR IGNORE`, and rows can only land in [now−60 s, now]. Samples never touch status, alerts or Telegram, and the test proves this on a fake clock.
- Migration 0010 only adds a table, with an FK cascade.
- The orchestrator refuses to prepare a quarantined server inside `startMu`, before any inventory read or agent call, and a test covers it.
- Every destructive trigger carries `data-confirm-danger="1"`.
- The tokens pass 4.5:1 in both themes. I recomputed the badge tints myself.

The open items are one pre-existing hostile-agent bound, one misleading health signal, and a handful of 390-px and polish items.

## Security findings
**SEC-81 (Important, pre-existing, fix in this wave).** `internal/panel/monitor/monitor.go:461`, `report()`: `time.Duration(max(he.TakenAgoMS,0))*time.Millisecond` has no upper bound and can overflow.
- Scenario: a compromised or buggy agent sends `taken_ago_ms ≈ 9.3e12`. The product wraps, and `at` lands in the future. That future time becomes `receivedAt`, so the server reads as fresh after its reports stop. `status_events` rows get future timestamps that pruning never deletes, and the weekly tally is skewed (OWASP A04/A09).
- `reportSample` already guards samples, but the status path is open.
- Fix: before any multiplication, do an integer compare against a cap. Suggested cap: the backlog's longest plausible age, for example `EventsRetentionDays` in ms. Above the cap, clamp to the cap or drop the report and count the drop. Add a test with MaxInt64.

**SEC-82 (Minor).** `handlers_servers.go:304,321`: the cutover and promote_partial banners are now assembled as `template.HTML` strings. Every value in them is escaped correctly today (`HTMLEscapeString` on name, message and job id), but this bypasses contextual escaping, a regression in pattern from the old template. Fix: move the markup back into a `{{define}}` that takes typed data.

**SEC-83 (Minor).** `web/static/app.js:204`: `querySelector(`[data-step="${d.step}"]`)` puts SSE data into a CSS selector. This is not XSS, but a quote in a step name throws and stops live updates. Fix: `CSS.escape(d.step)`.

**SEC-84 (Minor).** `master_detail.html:226`: "Buka kunci master" (`unlock_master`) has no `data-confirm-danger`. Unlocking a master that a partial promote left behind can produce two writable masters. That is outside the spec's pink list, but it is as dangerous as Lepas. Fix: add the attribute and keep `btn-warning`.

Checked and found clean:
- `theme.js` accepts only `gelap|terang`, wraps storage access in try/catch, writes only `data-theme`, and runs synchronously before the CSS.
- `topoTab` is checked against the tabs that exist.
- Settings validation: length ≤ 60 runes, control and Cf runes rejected, theme enum, retention 1–90 (web and `Validate`), changes audited by key.
- The instansi name is shown only to logged-in users.
- Sample drops are counted, with memory bounded to 60 minute-buckets per server.

## Correctness / UX findings
1. **(Important) The sidebar status box misreports health.** `router.go:397` skips servers with `!Paired`, and `layout.html:44` then says "Semua agent tersambung · 3 server". In `16-replikasi-terang-1440.png`, the same screen shows db-gudang as "Diduga tumbang (agent offline)". A server can also become unpaired after a token is reissued, so this is not only a test-fixture effect. Fix: count from the monitor's views. Any server in a topology or with a role whose status is agent-offline or awaiting a report counts as offline, and the line shows "N dari M server".
2. **(Minor) The lag chart legend is clipped at 390 px.** The legend sits inside the `min-w-[480px]` scroll box (`dashboard.html:33`, `_chart.html:27`), so "ambang 60 dt" and the 12:00 tick and peak are cut (`02-dashboard-terang-390.png`). Fix: render the legend outside the scroll box.
3. **(Minor) Job-table Status is clipped at 390 px** (`02-dashboard-terang-390.png`, `14-jobs-*-390`). Fix: put the Status column second, or hide "Selesai" below `sm`.
4. **(Minor) The audit table at 390 px shows Waktu, Pengguna and IP; Aksi and Hasil are off-screen** (`12-audit-terang-390.png`). Fix: column order Waktu, Aksi, Hasil, Target, Pengguna, IP, Detail.
5. **(Minor) Light theme `text-warning` is 2.95:1.** `replica_new.html:26` ("Agent … offline"). Fix: `text-warning-text`.
6. **(Minor) The focus ring disappears on the active menu entry.** The ring is primary on a primary background (`app.css:421`, offset −2). Fix: use `outline-color: var(--color-base-content)` for `.menu-active`.
7. **(Minor) Raw values in the audit summary.** It shows "mode: planned" in English (`12-audit-gelap-1440.png`, `audit_summary.go:43`). Fix: map `planned`/`emergency` to terencana/darurat.
8. **(Minor) Confirm dialog opens over the still-open "Lepas replika?" dialog at 390 px** (`15-dialog-konfirmasi-terang-390.png`). The flow is pre-existing and harmless, but close the source `<dialog>` before calling `showModal()` on the confirm.
9. **(Minor) The tour prints OVERFLOW but does not fail** (`scripts/e2e_screenshot_tour.js:96`). Ruled: make it fail.
10. **(Minor) Date formats are mixed.** Cutover, server and job pages show yyyy-mm-dd (`09-cutover-gelap-1440.png`). Audit and alerts show dd-mm-yyyy. The spec binds only audit, so this may stay.

## Deferred-minor triage
**Must fix in the final wave:** SEC-81 (the TakenAgoMS clamp), finding 1 (the sidebar), the four Task 8 "Perlu perbaikan" items (findings 2, 3 and 4 are cheap; finding 8 is recommended), finding 9 (tour fails on overflow), and findings 5–7.

**Fix if cheap:** the sidebar alert counter is still a raw `badge-warning` (contrast is fine, 6.2:1 light) → `lencana-warning`.

**May stay:**
- No third "ikuti sistem" state after a manual choice. Note it in the README.
- No-JS reach of the other topology tabs.
- Gauges are computed at page load while rows update live. `02-dashboard-gelap-1440.png` shows "2 mengejar" beside a row that has already flipped to Sinkron, but "Diperbarui N detik lalu" explains it.
- The login banner pattern, the login double focus ring, and NOT NULL DEFAULT on `health_samples`.
- Native date inputs in the browser's locale.

## Residual risks to present to the owner
- **Ambiguous date filter.** The audit "Sejak" field shows the browser's locale, for example 11/02/2026 for 2 November on an en-US browser. Staff who read dd/mm may filter the wrong range. Set the browser or OS locale to id-ID, or accept the risk.
- **Agent-supplied data is trusted.** Lag charts and status timestamps rest on what the agent reports. A compromised agent can still draw a false chart, within 60 s windows and one row per 5 s. SEC-81 closes the remaining timestamp hole.
- **Sample volume.** At the maximum retention of 90 days, `health_samples` can reach about 1.5 M rows per server. That is fine for SQLite. The default of 7 days stays small.
- **No visual regression gate.** The design relies on a screenshot tour that is reviewed by eye. Only overflow becomes automatic, after finding 9.

## Test honesty
The new monitor tests drive a fake clock (`r.clk`). I found no `time.Sleep` in the new web tests and no weakened fixtures. The alert date tests are rollover-safe. The README "Tampilan" section is accurate, but it should mention the browser-only theme choice with no way back to "ikuti sistem", and the date input locale.

Final verdict: mergeable after fixes

## Re-review of the fix wave (c245659..0566027)

All 15 items are addressed and the diff adds no Critical or Important breakage.

**SEC-81: ADDRESSED.**
- `reportAge` makes a negative age zero and compares integers against `EventsRetentionDays` in ms before any multiplication.
- The largest possible cap is 3650 days, about 3.2e11 ms, which converts to a Duration without overflow. So `at` is always ≤ now.
- An age over the cap is clamped rather than dropped: the report lands at now − retention and the next prune removes its rows. One Warn per server is logged.
- Backlog reports inside the retention are placed exactly as before. `reportSample` still has its own window.
- The MaxInt64 test fails on the old line.

**SEC-82: ADDRESSED.** `Banner` has typed `Kind`/`Name`/`ServerID`/`Message`/`JobID` fields. The `banner-cutover` and `banner-partial` defines are autoescaped, the texts and hrefs are unchanged, and no `template.HTML` is left in `handlers_servers.go`. There is an escape test using `<script>`.

**SEC-83: ADDRESSED.** `CSS.escape(String(d.step))` is applied to all three selectors.

**SEC-84: ADDRESSED.** `unlock_master` now carries `data-confirm-danger="1"` and there is a test.

**UX1: ADDRESSED.**
- `ServersTotal` now counts every server. A server is connected only when it is paired, `Hub.IsOnline`, and not `AgentAway()` (agent offline, presumed down for it, or awaiting a report).
- "Semua agent tersambung" appears only when connected == total and there is at least one server. Otherwise the line reads "K dari N server tersambung" in warning tone.
- The only added per-request cost is one in-memory `Monitor.Views()` copy, which is cheap.
- `16-replikasi-terang-1440` reads "3 dari 5". `03-servers` reads "3 dari 4" because the e2e local "vm" (server panel) is unpaired. In production `install.sh` pairs the local agent, so an unpaired panel host is a real fault there, not a false alarm.

**UX2: ADDRESSED.** Only the SVG sits in the scroll box. The legend, the peak (dot plus label) and the threshold are below it, outside the box. The dot stays in the plot. `02-dashboard-terang-390` shows them whole.

**UX3: ADDRESSED.** Status is the second column and Selesai is `hidden md:table-cell`, on both the shared job table and the dashboard's own table. The badges are fully visible in `14-jobs` and `02-dashboard` at 390 px.

**UX4: ADDRESSED.** The audit column order is Waktu, Aksi, Hasil, …. Those three are visible in `12-audit-terang-390`.

**UX5, UX6, UX7: ADDRESSED.**
- `replica_new` uses `text-warning-text`, and no bare `text-warning`/`text-error`/`text-success` remains.
- The active menu entry's focus ring is `base-content` at offset 2.
- planned/emergency show as terencana/darurat.

**UX8: ADDRESSED.**
- **Submission is intact.** The source `<dialog>` is closed, not reset, so its form fields remain. `submit()` adds the token and names to `this.tombol.form` and calls `form.submit()`, which works on a form inside a closed dialog.
- **No stray reopen.** On a successful POST the confirm dialog stays open while the page navigates, so `kembali()` does not reopen the source. On a GET download it closes and reopens the source, which is harmless.
- **Cancel restores state.** Batal and Esc fire `close`, then `kembali()` reopens the source and focuses the trigger.
- **No focus trap.** `fokuskan()` retries for at most 20 frames and stops when the element is disconnected.
- The browser e2e covers closing under the confirm, reopening on Batal with focus, and a second run. `15-dialog-konfirmasi-terang-390` shows the confirm dialog alone.

**UX9: ADDRESSED.** The tour collects OVERFLOW lines and throws after all passes, so the exit status is 1 and `t.Fatalf` fires.

**UX10: ADDRESSED** on the server, master, replica, cutover, connections and timeline pages. Job pages are still yyyy-mm-dd, as ruled.

**Alert counter and README: ADDRESSED.** The counter is now `lencana-warning`. The README notes that a manual theme choice cannot return to "ikuti sistem" and that the date input follows the browser locale.

**New observation (Minor, may stay):** the agentapi `TestWSLogsReplacedConnection`-style timing flake recurred under load, as the fixer noted. That code is untouched and the test is already on the Plan 5 list.

Final verdict: mergeable
