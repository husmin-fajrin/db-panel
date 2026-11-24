# Final review — panel web — Plan 5b-2b (78b1bb9..d12a4df)

Reviewer scope: `internal/panel/web`, `web/templates`, `web/static/app.js`, `web/src`, `README.md`, and the plan's "Teks baru untuk dibaca pemilik".

How it was read: the production diff (4128 lines) in full, in eight parts; the README diff (1308 lines) in full; `web/templates/panduan.html` from the tree (the diff cuts its long lines); the test diff where a rule had to be judged (gate, guide, backup routes, ticks, promote: roughly half of it). Outside the diff, after `graphify query`: `router.go`, `handlers_auth.go` (sameOrigin), `app.js` (confirmModal, promotePicker), and the agent and orchestrator code the guides lean on (`cli.go` set-role/approve/set-source, `pins.go`, `quarantine.go`, `promote_lock.go:504`, `install.go:588-595`, `promote.go:1246-1264,1447-1497`, `promote_view.go:472-492`, `gate.go:345-411,500-527`, `monitor.go:356-374`). No test was run and nothing but this file was written.

## Strengths

- **Order of checks is right in every gated handler.** `prepareMaster`, `resyncReplica`, `createReplica` (plain and rebuild), `addReplicaDatabases`, `removeReplicaDatabases` (only with `drop_data`), `startPromote` (planned only) and `confirmedJobAction` (only when `DropsData`) all call `gateCheck` before `requireConfirm`. `TestGatedHandlersRefuseOnRedRow`, `TestGateRefusesBeforeTokenIsConsumed`, `TestRebuildGatedBeforeToken` and `TestJobRetryGatedBeforeToken` post the same token again after the refusal and see it accepted.
- **A gate that cannot judge refuses** (`gateCheck`, 400 with the error; `TestGateErrorRefuses`), except for the emergency promote, which starts and audits `skipped_red: null` (tested).
- **A refusal carries labels only.** G14 on the page, G0 in the audit and the key list in the log are built from `GateRow.Label`/`Key`; `Detail` and `Fix` reach HTML only inside `gate-list`. `outsideGateLists` in the tests checks that for resync.
- **The promote's mode and the D5 tick are decided before the gate and the token** from `PromoteMode` (no agent call), and again by `checkPromote` and by the orchestrator. D5 is asked three times, never pre-ticked (`wantUnticked`), `required`, and the confirm dialog honours `reportValidity()` (`app.js:389`), so the password is not asked while the box is empty.
- **Escaping and CSP.** No `template.HTML`/`HTMLAttr`/`JS` was added on operator or agent text (the only `HTMLAttr` is the constant `MenuAttr`). New `data-` attributes carry constants or integer ids; `ikutiPrasyarat`/`targetGate` only compare `dataset` values, toggle a class and set `.disabled`. No inline script, style or handler in any changed template (checked by pattern over the range).
- **Agent text on the screens is bounded and labelled** (`agentLine`, `newPinFacts`, `hostPortPin`, `agentPart(…, maxGateValue)` in G1a/G2a).
- **The three new POST routes sit behind login and the same-origin check and are audited**, with a test that tries no cookie, no Origin, a foreign Origin and `null` (`TestBackupRoutesNeedLoginAndSameOrigin`).
- **Guides 5 and 7 are the audited README text.** A sentence-by-sentence diff of README 2697-2868 against guide 5, and 2649-2696 against guide 7, shows only formatting (commands moved into blocks) and, in guide 7, conditions made explicit before `unlock-master` and `SET PERSIST read_only=OFF`.
- **Page and README cannot drift**: `TestGuideMatchesREADME` compares titles, every command block and the prose; `TestGuideRedRowsMatchGate` holds guide 8 to the constants in `gate.go`.
- **Guide 8's SQL is exactly `mysql.ResetAdmin`'s** three statements with the account name changed; the replica rule (never run on a replica, never turn read-only off there) stands directly above the steps.

## Issues

### Critical (Must Fix)

None.

### Important (Should Fix)

**W-1. The job page refuses Ulangi/Setujui with G14, which points at a precondition list the job page does not have; the buttons stay on and the password is asked while a row is red.**
`internal/panel/web/handlers_jobs.go:484-493`; `web/templates/job_detail.html` (no `gate-list`, no `gateFor` in `renderJob`). `TestJobRetryGatedBeforeToken` asserts exactly one gate read, so the missing list is the tested state.
Spec 5.7: for an action that has only the password dialog, the red rows are shown before the password is asked and the dialog does not ask while a row is red. Plan Task 11 lists five screens and leaves the job page out.
Scenario: a resync fails half way and its copy has filled the replica's disk. The operator presses "Ulangi dari tahap ini", types the password, and gets 409 "Tindakan ini ditolak karena ada prasyarat yang belum terpenuhi: Ruang disk. Cara memperbaikinya ada di daftar Prasyarat di halaman ini." There is no list on that page: no server name, no detail, no fix. The same happens to every retry of a data-dropping job after a panel upgrade (Versi agent red).
Smallest fix: in `renderJob`, when the job can be retried or approved, `DropsData` is true and `orchestrator.JobGateInput(job)` is gated, set `Gate = a.gateFor(in, true)`; in `job_detail.html` draw `{{template "gate-list" .Gate}}` beside those buttons, `disabled` on them while `.Gate.Off`, and D2. One test like `TestRedRowDisablesButton` for the job page.

**W-2. After the upgrade the "Akun panel" row cannot turn red (or green) for any server that was already connected, and nothing tells the operator why or what to press.**
Migration 0013 gives every stored inventory `account = ''`. `gate.go:520` reads that as G10b ("Akun panel belum diketahui: agent <server> belum melaporkannya."), and `combine` (`gate.go:363-368`) gives a yellow row no fix. The account is not part of a health report; it is stored only by Hubungkan/Segarkan and inside jobs, and nothing refreshes it on start-up or on connect. README "Pembaruan" (3820-3847) step 2 says "Bila agent di sebuah server masih masuk ke MySQL sebagai `root` …" without a way to find out, step 5 says to read the yellow rows, and the guide (README 3414-3418, `panduan.html:92`) says of G10b "tunggu laporan berikutnya, lalu muat ulang", which never helps this row.
Scenario: a hospital upgrades; its agents log in as root (the installations spec section 8 names). The promote screen shows "Akun panel — Perlu perhatian … belum melaporkannya" for good. The operator waits, reloads, then runs the planned promote; the row the spec wants red never was.
This spans the core side (flag for the panel-core reviewer). Smallest fix I would accept on this side: README "Pembaruan" gets a step before step 2, "Di halaman setiap server, klik **Segarkan** di kartu MySQL; sesudah itu baris **Akun panel** menunjukkan akun yang dipakai agent", and the guide's G10b sentence gets "untuk baris Akun panel: klik Segarkan di kartu MySQL halaman server itu". Better, in `gate.go`: a connected inventory without an account gets that sentence as its fix (one more owner text).
While there: the same README paragraph lists what is refused until the steps are done but leaves out Siapkan sebagai master, Lepas database dengan hapus data, and Ulangi/Setujui of a data-dropping job.

### Minor (Nice to Have)

**W-3. Resync: after a refused post the list G14 points at is inside the closed dialog.** `handlers_controls.go:221-224` renders the replica page with `OpenResync` false; the list is at `replica_detail.html:195` inside `#modal-resync`. Hit only from a page loaded before the row turned red. Fix: render that refusal with the dialog open, or draw the red list outside the dialog.

**W-4. Guide 6 step 3 quotes a word the page never shows.** README 3284 / `panduan.html:69`: "Bila tertulis `quarantined` …", but `newPinFacts` shows "Peran dipin: dikarantina". Fix: "Bila tertulis `dikarantina`" in both places. No test compares quoted screen texts with the templates.

**W-5. Guide 9 (new, never run as a sequence).** Read against the code it holds: `set-role quarantined` from `master` asks nothing and keeps no source; `set-source` writes the pin; on reconnect `onOnline` sends `mysql.quarantine` with `topo.SourceHost:SourcePort`, and `requarantine` accepts when the pin equals it; `mysql.quarantine` and `mysql.tlsTrust` are both allowed on a quarantined server. Three weak spots for an operator following it literally:
  - Step 5 (README 3664) takes the address from "layar pengalihan". After "Sudah saya alihkan" (guide 1 step 10 comes first) nothing links to that screen. The same value is "Alamat untuk replika" on the new master's page; say so. An address spelled differently (IP for name) makes the agent refuse again and the heading stays "belum dikarantina", with no hint in the guide.
  - Steps 2 and 3 do not tell the old master from the new one: both show "Peran dipin: master", and on the new master `replicas-seen` does not hold its own host either. `set-role quarantined` does not ask. An operator logged in to the wrong server locks the new master (recoverable with guide 6, but an outage during an incident). Add "pastikan Anda berada di master lama (`hostname`)" before step 4.
  - It has never been executed end to end; see the verdict under named item 5.

**W-6. D2 under a checkbox.** `replica_databases.html:91-96`: "Tombol ini mati …" stands under the disabled "Hapus datanya juga di replika" box while the "Lepas database" button below stays on. Plan text D2 is fixed wording; the owner should see this placement and may want a second sentence for it.

**W-7. Guide 2 step 9** (README 2992, `panduan.html:24`): "Layar menampilkan nama server, sumber data dan alamat IP-nya. Pastikan nama dan alamat IP itu memang master baru …". The first name on that screen is "Server ini", the old master. The audited source says "nama server ini … sumber data (master baru, `host:port`)". This is the check before `ya` approves wiping the server. Fix: "Pastikan **Sumber data** dan **Alamat IP** itu memang master baru …".

**W-8. The README now holds two procedures twice** (2649-2868 and guides 5 and 7) and no test holds the old sections to the guides; `set-role`'s message still names the old heading "Melepas kunci start dengan tangan". A later correction to one copy leaves the other stale. Fix: reduce the old sections to their heading and a pointer to the guide, or extend the guide test to them.

**W-9. Owner's text list.** Sampled about 25 entries in each direction; the texts match the code letter for letter. Missing: `permintaan tidak valid` (`handlers_settings.go:566`); the "tidak ada di rencana" mark on `master lama %s masih punya pekerjaan yang belum selesai`; any line naming the four new or extended README sections ("Prasyarat", "Cadangan (backup)", "Pembaruan", "Keamanan"), which are operator text under the Global Constraints. The nine guides are listed by title with a pointer, which is acceptable for their size.

**W-10. README "Keamanan" is narrower than the truth in two places.** Line 4206 lists the rows a compromised agent can make green and leaves out Versi agent and Akun panel (both come from the agent). And "Mengubah folder tidak meminta kata sandi panel" does not say what that reaches: without the password a logged-in session can make a red Cadangan row green or yellow (another folder, a limit of 8760 hours, an empty folder) and so close `backup_stale`. It is audited and no weaker than `POST /settings`, which moves the disk threshold the same way; the sentence should still say it.

**W-11. Small guide wording.** Guide 8 step 6 (README 3556): "kerjakan langkah 5 di halaman replika itu", but the MySQL card is on the server page. Guide 8's last paragraph does not say `reset-admin` asks for `ya`. Guide 1 step 4 writes `systemctl restart dbpanel-agent` without `sudo` (as the promote screen does) while guide 9 writes it with. Guide 3 step 1 (README 3018) assumes the old master can be read, which it cannot after the emergency promote the guide is mostly for. Guide 6 step 5 does not name the outcome "kunci start tidak bisa dilepas (…); peran tidak diubah". README 3830 "Panel menolak agent yang versinya berbeda" reads as if the agent were disconnected.

**W-12. `/panduan` and the pages with a gate list are not in the header and inline-script tests.** `TestSecurityHeaders` and `TestPagesHaveNoInlineScript` (`web_test.go:758-820`) list `/`, `/servers`, `/servers/new`, `/servers/1`. The headers come from one wrapper around the whole mux, so nothing is missing today; adding `/panduan` and one promote page to both lists costs two lines.

## Verdicts on the named items

1. **Route table.** New: `GET /panduan` (login; read-only; not audited, like every GET). `POST /servers/{id}/backup`, `POST /settings/panel-key-backed-up`, `POST /settings/audit-forwarded`: login, same-origin, 64 KiB form limit, audited on success and on refusal, no confirmation token (R2). Changed handlers on existing routes (`/masters/{id}/promote`, `/masters/{id}/replicas`, `/replicas/{id}/resync`, `/replicas/{id}/databases/add|remove`, `/servers/{id}/prepare-master`, `/jobs/{id}/approve|retry`, `/servers/{id}/delete`): login, same-origin and their tokens unchanged; the gate was added in front. Not a defect. R2 is acceptable for all three: the marks move only rows that never refuse; the backup form is one input of the Cadangan row and of `backup_stale` (W-10), on a par with `POST /settings` and `POST /masters/{id}/source`, which take no token either.
2. **Order of checks.** Not a defect (see Strengths). `%s` in `Prasyarat tidak bisa dibaca: %s` is `shownError` of a `Gate` error: `prasyarat: …` plus a store error (in English, for example a SQLite lock) and a server name or id. `Gate` makes no agent call, so no agent text. Shown to a logged-in operator only; acceptable.
3. **Escaping and CSP.** Not a defect.
4. **Screens.** List and form cannot disagree with script on: one list per candidate or target, toggled with the choice; the server-rendered state is the checked candidate or posted target. With script off the promote cannot be posted at all (no token), and Buat replika is refused by the server with the posted target's list. D3 shows only on a planned promote and on the rebuild form, D5 only on an emergency, both unticked and directly above the button. Defects: W-1 (Important), W-3, W-6 (Minor).
5. **Guides.** Wrong against the code: W-4. Read two ways before a destructive confirmation: W-7. Guide 9: W-5. No step was found that leaves a server writable if the operator stops after it; every unlock or read-only-off command has its condition directly above it. Gaps: `systemctl`, `timedatectl`, `unlock-master` and `reset-admin` not run with a paired agent are acceptable to merge with as they are: each is an existing, separately tested or standard command, and what the guides claim of them matches the code (`promote_lock.go:504` for `unlock-master`, `cli_admin.go` for `reset-admin`). Guide 9 steps 6-7 are acceptable to merge only with a statement: one sentence in the README's test section and in the owner's report that guide 9 was composed from existing commands and checked by reading the code, not run end to end; a failed run leaves the old master pinned quarantined, which is the safe side. An e2e test belongs in the next plan.
6. **README sections.** "Prasyarat" and "Cadangan (backup)": every claim checked against `gate.go`'s row table, the handlers and the templates is true; the quoted G14 inherits W-1. "Pembaruan": the order is safe at every stop, and it says that until the agents are updated no replica can be chosen even for an emergency promote; defect W-2. "Keamanan": W-10.
7. **Owner's list.** Complete and exact enough to sign off after W-9.
8. **Headers and cookies.** Nothing weakened or missed: one wrapper sets CSP (no `unsafe-inline`, no `unsafe-eval`), `X-Content-Type-Options`, `X-Frame-Options` and `frame-ancestors`, `Referrer-Policy`, `Permissions-Policy`, COOP, CORP, COEP and `Cache-Control: no-store` on every response including `/panduan`; HSTS on TLS; the cookie code was not touched. Each is tested per response class (`web_test.go:725-798`, `tlsui_test.go:242`, `auth_test.go:338`), not on the new page (W-12).

## Triage of deferred findings (one line each: fix before merge / can wait / not a defect — reason)

Only items that touch this side.

- Task 1, F2 may be cut by the quote censor: can wait — wording of one job-log line.
- Task 3, wrapped agent error labelled as a whole in the audit: can wait — attributes more to the agent, never less.
- Task 3, labelled outcome over 500 cut without ellipsis: can wait — cosmetic.
- Task 3, unknown RolePin of only white space shows an empty label: can wait — cosmetic.
- Task 3, panel's own copy of the source-host rule: can wait — a drift shows an honest pin as "tidak dikenal", the safe side.
- Task 4, `"Wajibkan …" wajib`, lines over 80 columns, "Perketat" in three test comments: can wait — polish.
- Task 7, negative age or size from a hostile agent: not a defect on this side — the card clamps both to 0 (`backup_folder.go`); the judging is core's.
- Task 8, R22 refill window: can wait — seconds long; belongs with the named item below.
- Task 8, saving unchanged values forgets the report: can wait — the notice already says the state follows the next report.
- Task 8, `backupMaxAge` repeated; view read twice; `backupForm` type: can wait — tidiness.
- Task 8, marks card heading without rows on a store failure; unparsable mark time shows "pada -.": can wait — only after a store fault.
- Task 9, a red row's detail also carries its yellow sentences: can wait — reads acceptably in `gate-list`.
- Task 10, job page gates before the superseded check: can wait — hand-made post only; fix together with W-1.
- Task 10, plain create_replica with an unknown target shows the gate's error; no web test for the plain create gate: can wait — no token to protect there, and the orchestrator's gate is tested.
- Task 10 carried to 12 (G0 audit for a late `*GateError`; mode keyed on the view): built (`startRefused`, `checkPromotePosted`) and tested.
- Task 11 carried to 12 (gate inputs of the red-only screens; unticked D3; open the folded box): built and tested.
- Task 11, approval hint and D2 both show: can wait — they agree.
- Task 11, `outsideGateLists` depends on a class string: can wait — a changed class makes the test fail, not pass.
- Task 12 carried to 14 (gate error on an emergency post; every gate input checked): built (`4f45283`, `78928fc`).
- Task 12, `skipped_red` read outside `startMu`: can wait — a record only.
- Task 12, SEC-44 turns the three fields into lengths on a refused emergency: can wait — nothing was skipped then.
- Task 12, `TestUngatedHandlersSkipGate` lost one assertion: can wait.
- Task 12, a tokenless post re-renders through `PromoteView`: can wait — the GET does the same; for the audit.
- Task 13, README 803 comment: can wait — the paragraph under it is exact since `453a749`.
- Task 13, guide 9 step 3 name against address: can wait — a wrong reading goes on in the safe direction; see W-5.
- Task 13, guide 6 extra steps and missing outcome: can wait — see W-11.
- Task 13, guide 7 order differs from its source: not a defect — the warning now precedes the command.
- Task 13, guide 3 steps 1, 3, 8: can wait — see W-11.
- Task 13, guide 2 step 9: fix before merge — it is the check before approving a wipe, and the fix is one clause (W-7).
- Task 13, guide 5's `grep` exit code; icon and CSS outside the file list: not a defect.
- Task 13 carried to 14 (guide 8 bullets, guide 9 step 8, guide 8 step 6): built (`e1fd004`, `a7d6ec5`, `38a7275`).
- Task 14, missing "tidak ada di rencana" mark: fix before merge — the list is what the owner signs (W-9).
- Task 14, "Keamanan" leaves out Akun panel and Versi agent: fix before merge — a residual-risk sentence must not be narrower than the truth (W-10).
- Task 14, "Pembaruan" step 1 wording: can wait — fix with W-2 if that paragraph is edited.
- Task 14, "Prasyarat" table omits when the port row is skipped: can wait.
- Task 14, e2e test 3 not mutation-checked: can wait — not this side.
- Task 14, NAMED ITEM, two quick saves of the backup form: can wait from this side — `saveServerBackup` cannot close it (`ConfigureServer` reads the row and sends in its own goroutine); two different folders must be saved within the few milliseconds between one goroutine's read and its send. The wrong state then lasts until the agent reconnects or the settings are saved, so the panel-core reviewer should decide between a per-server lock around read-and-send and a README sentence.
- Task 14, commit `54fce1b` message: not a defect — history is not rewritten.

## Findings against rulings

- **R2** stands, but its stated cost ("reveals only a file's age and size") is incomplete: the unconfirmed form is also an input of the Cadangan row and of `backup_stale` (W-10). No change to the ruling asked; the README should say it.
- **R28** (G10b keeps no fix) has one case where it leaves the operator without a way forward: the account row of a connected inventory stored before this version (W-2). The right step for that row is always the same (Segarkan), so the ruling's reason, "the right step differs per row", does not hold for it.
- R19-R23, R31-R36: no finding. R32 was checked by diff, R35 and R36 against `install.go` and the guide text, R34 by tracing guide 9 through the agent and the orchestrator (W-5 lists what reading cannot show).

## Declined to judge

- The row rules themselves, `JudgeBackup`, the port probe (SSRF), migration 0013 and the monitor's configure race: panel-core side.
- The backup scanner and the agent CLI beyond the sentences the guides quote: agent side.
- The full OWASP pass, rate limits, session and token lifetimes: the audit that follows.
- Browser behaviour of `promotePicker`/`targetGate` (form restore on reload, back navigation): read, not run; no Playwright run.
- Visual layout, contrast and Tailwind output of the new lists and cards.
- Decisions K1, K2, K6: the owner's; the README states them.
- The e2e tests: not run, read only where the README describes them.
- The two security documents and the ledgers named in the README diff's stat: their text is not in the diff file given to this side.
- English/Indonesian UI (spec 7.7): outside Plan 5.
- README text older than this range, beyond the two sections compared with guides 5 and 7.

## Assessment (Ready to merge? Yes / With fixes / No — reasoning in 2–3 sentences)

**With fixes.** The enforcement on this side is sound: every gated handler checks before its token, a refusal carries labels only, the ticks are asked where they belong, and the routes, headers and escaping lost nothing. Two things an operator will meet should be fixed in the fix wave: the job page refuses with a pointer to a list it does not show (W-1), and after an upgrade the Akun panel row stays "not known" with no way forward on the screen, in the guide or in the README (W-2). Of the minors, W-4, W-7, W-9 and W-10 are one-line text fixes worth taking in the same wave, and guide 9 needs the statement that it was not run end to end.
