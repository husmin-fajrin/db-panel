# Final fix wave — Plan 5b-2b — implementer report

BASE d12a4df, HEAD b950757 (pushed to origin/claude/install-superpowers-plugin-ev2k9i). 15 commits. Nothing under `internal/agent` or `internal/proto` was changed. Scratch directory (logs, RED/GREEN outputs): `(scratch directory)`.

Commits, oldest first:

| Commit | Item |
|---|---|
| 31addd2 fix(panel): show the red precondition rows on the job page and disable Ulangi and Setujui | F1 |
| 1964120 fix(panel): open the resync dialog when the gate refuses a resync | F8 |
| 6d387bf test(panel): cover /panduan and a promote page in the header and inline-script tests | F12 |
| f9417aa test(panel): a gate that cannot judge refuses every gated start | F7 |
| 8f606e3 fix(panel): judge the agent's backup numbers defensively and say when a scan was cut short | F6 + F4 (code) |
| 0831d36 fix(panel): do not open or close backup_stale from an extrapolated age | F3 |
| cd492c9 fix(panel): send one monitor.configure per server at a time | F5 |
| c291ee4 fix(panel): tell the operator to press Segarkan when the panel account is not known | F2a |
| 1e18190 fix(panel): read the inventory once on connect when the MySQL account is not known | F2c |
| 80592e9 docs(panel): say how to fill an unknown panel account, in Pembaruan and the guide | F2b |
| 6bff687 docs(readme): say that a folder over 10,000 entries is judged from part of it | F4 (docs) |
| f2d6b89 fix(panel): word the note under the disabled drop option as an option | F13 |
| 355da45 docs(panel): correct guide wording found in the final review | F9 |
| c524c4e docs(readme): make Keamanan, Pembaruan, Prasyarat and the backup card exact | F10 |
| b950757 docs(plan): complete the owner's text list with the final fix wave | F11 |

## Per item

### F1 (W-1) — fixed

- `internal/panel/web/handlers_jobs.go`: `jobPage.Gate *gateView` (struct, ~line 164); in `renderJob`, after `DropsData` is set: when `(CanApprove || CanRetry) && DropsData` and `orchestrator.JobGateInput(job)` is gated, `p.Gate = a.gateFor(in, true)` (red rows only; the gate reads the panel's data only, no agent call).
- `web/templates/job_detail.html`: `{{template "gate-list" .Gate}}` above the action buttons; `disabled` while `.Gate.Off` on the Setujui opener (`data-gate-button`), both Setujui submit buttons, the Recheck button (posts /retry), "Ulangi dari tahap ini" (`/retry/prepare`, `data-gate-button`) and "Ya, ulangi" in the retry dialog; D2 below the buttons when `.Gate.RedNote`.
- Server-side refusal unchanged (`confirmedJobAction` still calls `gateCheck` before the token, and the orchestrator judges again).
- Tests: new `TestJobPageRedGate` (retry and approve cases: buttons off, D1, D2, red row detail+fix, red rows only, agent text only inside the list, no agent call, one gate read; green gate: buttons on, no list) in `internal/panel/web/web_gate_test.go`. `TestJobRetryGatedBeforeToken` now asserts the first gate input is the handler's (the refused page reads the gate again for its list, as the resync and rebuild tests already allow) and additionally asserts the refused page shows D2 and the red row's fix. Not a weakening: the assertion that the handler judges first with the exact input stays.
- RED (`go test ./internal/panel/web/ -run TestJobPageRedGate -count=1`):
  ```
  --- FAIL: TestJobPageRedGate/retry: web_gate_test.go:957: element not disabled: <button type="submit" class="btn btn-error" data-confirm-action="job_retry:1111…a" …>
  --- FAIL: TestJobPageRedGate/approve: web_gate_test.go:957: element not disabled: <button type="submit" class="btn btn-error" data-confirm-action="job_approve:3333…d" …>
  ```
  GREEN: `--- PASS: TestJobRetryGatedBeforeToken`, `--- PASS: TestJobPageRedGate`, `ok internal/panel/web`.
- New operator text: none (D1, D2, G-rows are existing texts, now also on the job page; listed in F11).

### F2 (C-1 = W-2; R40) — fixed (a, b and c)

a. `internal/panel/orchestrator/gate.go`: new constant `textG10bf = "Klik %s di kartu MySQL halaman server itu, lalu muat ulang halaman ini."` (rendered with `refreshButton` = "Segarkan", so the operator sees exactly the ruling's text). `accountRow` now separates `!inv.Connected` from `inv.Account == ""`; a yellow row that has at least one connected inventory with an empty account gets that fix. Other rows' G10b keep no fix; the account row's G10b for a server whose MySQL is not connected or has no inventory keeps no fix either, because the Segarkan button exists on the MySQL card only when MySQL is connected (`server_detail.html:236-272`) — telling the operator to click a button that is not there would be false. (Committed first as `textG10bAccount` in c291ee4, renamed `textG10bf` in 80592e9 so that `TestGuideRedRowsMatchGate`'s `^textG[0-9]+[a-z]*$` picks it up and holds the guide to it.)
  - Test: `TestGateAccountSplitsAtLastAt` expects `wantG10bAccountF` on the resync and prepare-master rows (gate_test.go). RED: `gate_test.go:508: row account = {1 "Akun panel belum diketahui: agent db-master belum melaporkannya." ""}`. GREEN: `ok internal/panel/orchestrator`.
b. README "Pembaruan": new step 2 (old 2–5 become 3–6). Guide 8 (page and README): the intro's G10b sentence points the Akun panel row to its sub-section; the Akun panel sub-section quotes the yellow text and the new fix and says waiting does not help. `TestGuideMatchesREADME` and `TestGuideRedRowsMatchGate` green (the latter now requires `textG10bf` in the guide).
c. `internal/panel/orchestrator/promote.go`: `OnOnline` starts a second goroutine (same `wg`) running `refreshUnknownAccount(r.base, id)`: reads the stored inventory; only if `Connected && Account == ""` it calls `RefreshInventory` (the path behind Segarkan: `inventoryCallCut` → `Hub.Call("mysql.inventory")`, `updateInventory` under `invMu`, Publish). No `startMu`, no `repointMu`, no Monitor lock; it does not hold up the connect (OnOnline returns at once). A failure is logged (Warn) and the row stays as it was. The agent's account is bounded and cleaned by the existing `storedAccount`. Not audited (the web Segarkan is audited as `mysql_refresh`; this automatic read-only refresh is only logged).
  - Test: `TestOnOnlineRefreshesUnknownAccount` (inventory_test.go): with `startMu` held by the test and the agent's answer held, `OnOnline` returns within 2 s, the refresh reaches the agent, the account is stored, the gate's account row turns red (root); a second connect asks nothing; a server whose MySQL is not connected is asked nothing. Run with `-race`.
  - RED: `inventory_test.go:112: timed out waiting for the inventory read`. GREEN: `--- PASS: TestOnOnlineRefreshesUnknownAccount (0.33s)`.
- New operator texts: `Klik Segarkan di kartu MySQL halaman server itu, lalu muat ulang halaman ini.` (ruling text); README/guide sentences listed below.

### F3 (C-2; R41) — fixed

- `internal/panel/monitor/alerts.go`: `ruleCtx.online` (set from `serverState.online` in `Monitor.ruleCtx`); `backupReportCurrent(v, ctx) = ctx.online && now−ReceivedAt ≤ StaleAfter`. The `backup_stale` rule: `open` returns false when the report is not current; `close` returns true for `unset` (a cleared folder is not an agent verdict) and otherwise only `ok && current`. So while the agent is away or silent, the alert neither opens nor closes.
- Tests: new `TestBackupStaleNotFromExtrapolation` (agent away with an old ok report → no alert; agent back with a fresh stale report → opens with G8a; open alert kept unchanged while away and while the agent is connected but silent > StaleAfter; fresh fine report closes; panel restart after 62 h → first tick of a new Monitor opens nothing). `TestBackupStaleAlertOpensAndCloses`: the "stale only by time passing" case now steps the clock 65 s (within StaleAfter = 360 s in the rig) from 47 h 59 min, and the "an hour past" case sends a report at 48 h 59 min then steps 65 s — the reviewer's accepted adjustment; extrapolation is still tested (both messages are still produced by the tick, not by the report).
- RED: `agent_away: backup_stale opened from an old report while the agent is away`; `open_alert_kept: … Message:… berumur 3 hari 8 jam …, want … 50 jam … kept as it was`; `panel_restart_after_a_long_stop: backup_stale opened on the first tick after a restart, from extrapolation alone`. GREEN: all three subtests PASS; `ok internal/panel/monitor`.
- New operator text: none.

### F4 (A-1 = C-5; R39) — fixed (panel only)

- Agent limit checked: `internal/agent/monitor/backup.go:26 maxBackupEntries = 10000`.
- `internal/panel/monitor/alerts.go`: `BackupVerdict.Truncated` (from `b.Truncated` on the ok path, so for ok, stale and empty_file); exported `BackupTruncatedText` with the ruling's sentence letter for letter; `BackupMessageAs` appends it (one space) to G8a and G8b when `Truncated`. The judgement is unchanged.
- Card: `internal/panel/web/backup_folder.go` `newBackupCard` appends it to B7 "Berkas terbaru: …" for an ok+truncated report (stale/empty already get it through `BackupMessageAs`). Alert text and gate row Detail get it through `BackupMessage` (`gate.go backupRow` unchanged). A green gate row shows no detail, so an ok+truncated report adds nothing there (judgement kept: green).
- Telegram bound: the longest text is G8a + sentence ≈ 300 bytes plus a server name; `TestBackupStaleAlertTruncated` asserts ≤ 1024 bytes, far inside Telegram's 4096.
- README "Cadangan (backup)" gains the fact in one sentence; guide 8's backup sub-section quotes the sentence, and `TestGuideRedRowsMatchGate` now also reads `BackupTruncatedText` from alerts.go.
- Tests: `TestJudgeBackupTable` (ok+truncated, stale+truncated, unreadable/limit with truncated → no flag), `TestBackupMessageTexts` (stale and empty_file with the sentence, ok none), `TestBackupStaleAlertTruncated` (new), `TestServerPageShowsBackupState` (ok+truncated and stale+truncated card), `TestGateBackup` (red row detail with the sentence; ok+truncated green), `TestGuideRedRowsMatchGate`.
- RED: see F6 below (same run) plus card: `web_servers_test.go:262: server 1, … card lacks "Berkas terbaru: 2 jam lalu, 5.0 MB. Folder ini berisi …"`; guide: `guide_readme_test.go:401: page does not quote BackupTruncatedText: lacks "Folder ini berisi lebih dari 10.000 berkas, …"` and `:404 README does not quote …`. GREEN: all PASS.
- New operator text: the ruling's sentence (given); README and guide sentences below.

### F5 (C-3 = A-2) — fixed

- `internal/panel/monitor/monitor.go`: `Monitor.configMu map[int64]*sync.Mutex` (guarded by `m.mu`, created in `New`), `configLock(id)`; `configure` holds the server's lock from `GetServer` through `hub.Call` and `m.report`. Lock order: configMu lock, then `m.mu`; `m.mu` is never held while waiting for a configMu lock (configLock releases it before `Lock`). Another server's configure is not held. `ConfigureServer` still forgets the backup state at once and sends in the background; Close cancels `m.ctx`, which ends a held call (configureTimeout also bounds it), and the waiter then runs with a cancelled context.
- Tests: `TestConfigureInOrderPerServer` (first configure held at the agent; a second save + ConfigureServer does not reach the agent while the first is unanswered; another server's configure goes through meanwhile; after release the agent got `/srv/a` then `/srv/b`), `TestConfigureLockDoesNotHoldClose` (two configures queued on one server, first unanswered: Close returns). The test agent's `monitor.configure` handler now honours its context when held (`select` on the gate and `ctx.Done()`), as a real connection's call does; without that, the local test connection ignores cancellation for any held call, with or without this change. Both run with `-race`.
- RED: `monitor_test.go:1604: 2 configures reached the agent while the first was unanswered, want 1`. GREEN: both PASS (3 runs with -race for the first).
- New operator text: none.

### F6 (C-4) — fixed

- `JudgeBackup`: `age := max(NewestAgeSec, 0)`; the addition of the time since the report saturates at `math.MaxInt64`; `Size = max(NewestSize, 0)` and `Size == 0` (so any size ≤ 0) is `empty_file`.
- Tests in `TestJudgeBackupTable`: negative age (→ 0 + 1 h, ok), `math.MaxInt64` age 1 h old (→ stale, MaxInt64), negative size (→ empty_file, Size 0).
- RED (`go test ./internal/panel/monitor/ -run 'TestJudgeBackupTable|TestBackupMessageTexts'`): `negative age: … AgeSec:-356400 … want … AgeSec:3600`; `huge age: … Status:ok AgeSec:-9223372036854772209 … want Status:stale AgeSec:9223372036854775807`; `negative size: … Status:ok … Size:-5 … want Status:empty_file … Size:0`. GREEN: PASS.

### F7 (C-6) — fixed

- Test-only hook (as the Runner's existing `afterReopen`, `settleStore`, `paramsStore`): `Runner.gateFault func(GateInput) error` (runner.go), checked at the top of `Gate` (gate.go) — nil in production.
- `TestStartRefusesWhenGateCannotJudge` (gate_enforce_test.go): prepare_master, create_replica, resync, add_databases, remove_databases_drop, promote_planned, rebuild_old_master and a Retry of a failed resync each refused with the gate's own error (not ErrGateRed), no job, no replica row or promotion, only the pre-lock reads sent; promote_emergency still starts and succeeds.
- RED: compile (`e.r.gateFault undefined`), then, to show the test bites, `refuseRedGate` temporarily changed to swallow the gate error: every gated subtest failed with `err = <nil>, want the gate's failure prasyarat: pengaturan: database terkunci` (reverted before commit). GREEN: all 9 subtests PASS.

### F8 (W-3) — fixed

- `internal/panel/web/handlers_controls.go`: `resyncRefused` renders the replica page with `OpenResync: true` and the flash; used for the handler's `gateCheck` refusal and for a `*GateError` from `StartResync`. Other refusals keep the dialog closed.
- `TestResyncRefusalOpensDialog`: handler refusal → 409, G14, dialog `data-open="true"`, red row fix, D2; start's GateError → 409, open; another start error → closed; plain GET → closed.
- RED: `web_gate_test.go:996: body does not contain "id=\"modal-resync\" class=\"modal\" x-data=\"openDialog\" data-open=\"true\""`. GREEN: PASS.

### F9 (guides; page and README together) — fixed

All in `web/templates/panduan.html` and README `## Panduan`; `TestGuideMatchesREADME` green.
- W-4: guide 6 step 3 "Bila tertulis `dikarantina`" (checked: `newPinFacts` maps quarantined → "dikarantina", handlers_servers.go:680).
- W-5 (guide 9): (i) step 5 adds that the same address is "Alamat untuk replika" on the new master's page (label checked: master_detail.html:160, same `topo.SourceHost:SourcePort` as the cutover screen's NewAddress) and that another spelling makes the agent refuse again; (ii) step 3 now starts with `hostname`, compared with **Hostname** on the old master's server page (label checked: server_detail.html:97), stop if different; step 4 says "Di server yang sama (master lama)"; step numbers unchanged, so cross-references stay right; (iii) a closing paragraph says steps 6 and 7 were checked against the code but not run end to end with a panel and two agents, and to stop and call a MySQL admin if what is seen differs.
- W-7: guide 2 step 9 names **Sumber data** and **Alamat IP** (labels checked: internal/agent/cli.go:526).
- W-11: guide 8 step 6 "di halaman server replika itu"; guide 8's last paragraph says `reset-admin` shows what it will do and asks for `ya` (checked: cli_admin.go:497-501, `confirmed`); guide 1 step 4 command now `sudo systemctl restart dbpanel-agent` (promote screen text not touched; guides 5 and 7 not touched); guide 3 step 1 no longer assumes the old master can be read; guide 6 step 5 names "kunci start tidak bisa dilepas (…); peran tidak diubah" (cli.go:687) and what to do: fix the cause and run `set-role master` again, or follow guide 5 — no new command.
- Commands written: only `hostname` is new; it was run here (answered `vm`). `sudo systemctl restart dbpanel-agent` was not run (systemctl unavailable on this machine; same command as guide 9 step 6).
- No step turns read-only off, opens a server for writes, removes a lock or deletes data; the conditions before `set-role quarantined` and `set-role master` are unchanged and still directly before them.

### F10 (README) — fixed

- Keamanan: rows an agent can turn green now "versi agent, pin master, jam, ruang disk, akun panel, performance_schema, cadangan"; the backup bullet says what a pointed path discloses (exists, folder, regular file within three levels, more than 10,000 entries, newest file's age and size) and what the password-less form reaches (folder change, limit change, clearing → red Cadangan row to yellow or green, backup_stale closed; audited).
- Pembaruan step 1: planned actions involving a server whose agent has another version are refused; the agent stays connected and monitored.
- Prasyarat: when the panel host is the master, the action's target or a replica in that topology, the port is not probed and the row is always "Terpenuhi" (from `masterPortRow`, gate.go — the row is shown green, not hidden; I wrote what the code does).
- `unlock-master` comment: "kedua flag OFF sekarang" instead of "sampai restart".
- Cadangan (backup): an agent of the previous version does not scan; the card keeps "Belum ada laporan dari agent." until the agent is updated.
- Also in Prasyarat (part of F13): the D2 sentence notes the option form under the drop checkbox.

### F11 (owner's list) — fixed

Plan file, section "Teks baru untuk dibaca pemilik" only: `permintaan tidak valid` (Pengaturan), the "tidak ada di rencana" mark on `master lama %s masih punya pekerjaan yang belum selesai`, a paragraph naming "Prasyarat", "Cadangan (backup)", "Pembaruan" and "Keamanan" as operator text to read whole, and every text this wave added or changed (in their groups, plus a new sub-section "README dan Panduan: gelombang perbaikan akhir"), each marked "tidak ada di rencana".

### F12 (W-12) — fixed

`TestSecurityHeaders` and `TestPagesHaveNoInlineScript` (web_test.go) now include `/panduan` and a promote page with a red gate list (`promoteEnvF` + `redDisk`, asserting `data-gate-list` is present). The tests passed at once (nothing was missing); to show they bite, an inline `style=` was put into panduan.html temporarily: `web_test.go:824: GET /panduan has inline script/handler/style " style="` (reverted).

### F13 (W-6) — fixed

- `web/templates/_gate.html`: new `gate-off-option-note`; `replica_databases.html` uses it under the disabled "Hapus datanya juga di replika" box. Every other placement keeps D2.
- Text: `Pilihan ini mati selama ada prasyarat yang belum terpenuhi. Perbaiki baris bertanda "Belum terpenuhi", lalu muat ulang halaman ini.`
- Tests: new `TestDropOptionOffNote` (option sentence in Lepas database and no "Tombol ini mati" there; D2 still under Tambah database); `TestRedRowDisablesButton` takes a per-case note (the drop case expects the option sentence; others D2).
- RED: `web_gate_test.go:1039: Lepas database's note: …` (D2 found). GREEN: PASS.

## Gates (on b950757, one at a time)

- `go vet ./...`: no output, exit 0. `gofmt -l internal cmd`: no output. `graphify update .` run.
- `bash scripts/tailwind.sh`: not run — no class was added or changed in any template (new markup uses existing classes `text-sm text-warning-text`, and attributes only).
- Gate 1: not needed (no agent/proto change).
- Gate 2 `go test ./internal/panel/... -race -short -timeout 120m` → ffw-gate2.log:
  ```
  ok  	…/internal/panel	116.518s
  ok  	…/internal/panel/monitor	109.901s
  ok  	…/internal/panel/orchestrator	253.583s
  ok  	…/internal/panel/web	213.819s
  (agentapi, notify ok; auth, ca, hub, redact, store cached ok)
  EXIT=0
  ```
- Gate 3 `make test` → ffw-gate3.log (agent MySQL package from the cache):
  ```
  ok  	…/internal/agent/drivers/mysql	(cached)
  ok  	…/internal/panel	1169.786s
  ok  	…/internal/panel/orchestrator	43.158s
  ok  	…/internal/panel/web	30.239s
  ok  scripts/install_test.sh
  EXIT=0
  ```
- Gate 4 `make test-e2e` → ffw-gate4.log:
  ```
  go test -timeout 30m -run TestE2E ./internal/panel/
  ok  	github.com/husmin-fajrin/db-panel/internal/panel	1140.116s
  EXIT=0
  ```
No `--- FAIL`, no rerun needed, no freePort failure.

## Texts not in the plan

Operator texts this wave added or changed (all listed in F11, marked "tidak ada di rencana"):

1. Gate, Akun panel row fix: `Klik %s di kartu MySQL halaman server itu, lalu muat ulang halaman ini.` (shown as `Klik Segarkan di kartu MySQL halaman server itu, lalu muat ulang halaman ini.`; ruling R40 text).
2. Card, alert, Telegram, gate row: `Folder ini berisi lebih dari 10.000 berkas, jadi agent hanya memeriksa sebagian dan berkas yang lebih baru bisa terlewat. Arahkan folder cadangan ke subfolder yang berisi lebih sedikit berkas.` (ruling R39 text).
3. Databases page: `Pilihan ini mati selama ada prasyarat yang belum terpenuhi. Perbaiki baris bertanda "Belum terpenuhi", lalu muat ulang halaman ini.`
4. Job page: existing D1/D2/G texts now drawn there (no new words).
5. Guides (page + README): guide 1 `sudo systemctl restart dbpanel-agent`; guide 2 step 9 `Layar menampilkan nama server ini, sumber data dan alamat IP-nya. Pastikan Sumber data dan Alamat IP itu memang master baru yang sekarang dipakai aplikasi.`; guide 3 step 1 `Cari jadwal cadangan database yang dulu berjalan di master lama, …` + `Bila master lama mati atau tidak bisa dimasuki (misalnya sesudah promote darurat), ambil keterangan itu dari catatan Anda atau dari orang yang memasang jadwalnya.`; guide 6 `Bila tertulis dikarantina` and `Bila perintah menulis "kunci start tidak bisa dilepas (…); peran tidak diubah", server itu tetap dikarantina dan kuncinya tetap terpasang: perbaiki sebab di dalam kurung bila Anda bisa lalu jalankan set-role master sekali lagi, atau ikuti panduan "Melepas kunci master lama dengan tangan".`; guide 8 `(tunggu laporan berikutnya, lalu muat ulang; untuk baris Akun panel, lihat sub-bagiannya)`, `Atau tertulis (kuning): "Akun panel belum diketahui: agent <server> belum melaporkannya."`, `Perbaikan: "Klik Segarkan di kartu MySQL halaman server itu, lalu muat ulang halaman ini." Laporan berkala agent tidak membawa akun itu, jadi menunggu tidak menolong.`, `kerjakan langkah 5 di halaman server replika itu`, `Di sana jalankan perintah ini. Perintah itu menampilkan apa yang akan dikerjakannya lalu meminta Anda mengetik ya. Sesudah selesai, klik Segarkan di halaman server itu:`, `Di belakang teks pertama atau kedua bisa tertulis: "<text 2>" Bila begitu, kerjakan saran itu dulu: berkas terbaru mungkin ada tetapi tidak terlihat oleh agent.`; guide 9 step 3 `Masuk ke master lama. Pastikan dulu Anda memang di master lama, karena perintah langkah 4 tidak bertanya. Jalankan perintah ini; jawabannya harus sama dengan Hostname di halaman server master lama. Bila berbeda, berhenti: Anda berada di server lain.` + `hostname` + `Di server yang sama, lihat daftar replika yang pernah tersambung ke server itu:`, step 4 `Di server yang sama (master lama), pin server itu sebagai dikarantina.`, step 5 `Bila layar itu sudah tidak ada, alamat yang sama tertulis sebagai Alamat untuk replika di halaman master baru. Alamat yang ditulis lain (misalnya alamat IP untuk nama host) membuat agent menolak karantina lagi.`, closing `Langkah 6 dan 7 sudah dicocokkan dengan kode panel dan agent, tetapi belum pernah dijalankan dari awal sampai akhir dengan panel dan dua agent. Bila yang Anda lihat berbeda dari yang tertulis di sini, berhenti dan panggil admin MySQL.`
6. README: Pembaruan new step 2 `Di halaman setiap server, klik Segarkan di kartu MySQL. Versi panel sebelumnya tidak menyimpan akun MySQL yang dipakai agent; Segarkan membacanya, sehingga baris Akun panel bisa dinilai.`; Pembaruan step 1 `Selama agent sebuah server berbeda versi dari panel, tindakan terencana yang melibatkan server itu ditolak (baris Versi agent); agent itu tetap tersambung dan dipantau. Agent lama juga …`; Cadangan `Agent versi sebelumnya tidak memindai folder, jadi kartunya tetap menulis kalimat itu sampai agent tersebut diperbarui.` and `Bila folder berisi lebih dari 10.000 entri, agent hanya memeriksa sebagian, sehingga berkas yang lebih baru bisa terlewat; kartu (dan teks masalahnya di peringatan dan di baris Cadangan (backup)) lalu ditambah kalimat "<text 2>"`; Prasyarat `(di bawah centang itu: "Pilihan ini mati …"; tombol Lepas database tetap hidup untuk melepas tanpa hapus data)` and `Bila host panel adalah master itu sendiri, server tujuan tindakan, atau replika di topologi master itu, panel tidak memeriksa port MySQL master dan baris Port MySQL master selalu bertanda "Terpenuhi".`; Keamanan backup bullet and gate bullet (quoted in F11); unlock-master comment `kedua flag OFF sekarang`.

Agent log lines added (English, not operator text): `inventory not read on connect (MySQL account unknown)`, `inventory read on connect (MySQL account was unknown)`.

## Noticed outside the wave (not fixed)

- W-2's "while there": README "Sesudah memperbarui ke versi dengan daftar Prasyarat" lists what is refused until the steps are done but still leaves out Siapkan sebagai master, Lepas database with drop, and Ulangi/Setujui of a data-dropping job. Not in F10's list, so not changed.
- `prepareRetry` (`POST /jobs/{id}/retry/prepare`) refreshes two inventories (agent calls) before the dialog even when the job's gate is red; the button is now disabled and the final post is refused, so this only matters for a stale page. Not changed.
- F2c refreshes the inventory of a quarantined server too; an agent that refuses `mysql.inventory` there would log one Warn per connect. Harmless; noted.
- The guide-test regex for gate texts (`^textG[0-9]+[a-z]*$`) silently skips any gate text constant not named that way; worth a note for whoever adds texts next.
- Guide 3 step 3 ("Hentikan jadwal itu di master lama") also assumes the old master is reachable; the ruling named step 1 only, so step 3 was left.

## Self-review

- Each code item has a test that failed before the change (outputs above) and passes after; F7 and F12 add tests for behaviour that already held, and both were shown to bite by a temporary mutation.
- No existing check, refusal or test was weakened. Two existing tests changed: `TestJobRetryGatedBeforeToken` (exactly one gate read → the first read is the handler's, because the refused page now reads the gate for its list — the intended F1 behaviour; it also gains assertions) and `TestBackupStaleAlertOpensAndCloses` (clock steps kept within StaleAfter, as the reviewer's fix requires; it still tests extrapolation by the tick). `TestRedRowDisablesButton` now checks the correct note per placement. The fake monitor agent now honours its call context when held.
- F2a scope decision: the fix text is attached only when Segarkan actually exists for that server (connected inventory without account). If the controller wanted it on every yellow account row, that is a one-line change, but the text would then point at a button that is not on the page.
- F3 decision: a cleared folder still closes backup_stale while the agent is away (it is the stored row, not an agent verdict).
- F10 Prasyarat: the ruling says "when the row is not shown"; the code shows it green without probing, and the README says that.
- Untrusted text: the job page shows gate rows through the same escaped template as other pages; the F2c account goes through the existing `storedAccount` bound; no `template.HTML`, no inline script (F12 test covers /panduan and a gate page).
- Lock reasoning for F5 and F2c is in the items; both tested with `-race`, and gate 2 ran the whole panel tree with `-race`.
