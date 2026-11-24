# Re-check of the security-audit fix wave — PANEL-CORE and AGENT items (S2, S4, S5)

Base b950757, head 5fb84df. Read-only review; no test was run by me (a `go test ./...` was running when I
started; the controller reports all four gates ended with exit 0 on this head). Everything below is from reading
the diff (commits 899513c, f2514a2, fcbdd64, texts part of d01422b) and the code around it.

## Item Verdicts

### S2 (PC-3) — ADDRESSED

- `internal/panel/monitor/alerts.go:571-575`: a pin other than `replica`/`quarantined` becomes
  `"tidak dikenal (" + agentText(pin, maxAgentPin) + ")"`, `maxAgentPin = 32` (`:468`). `agentText` (`:478-500`)
  drops invalid UTF-8, turns every space/control/format character (line breaks, bidi marks) into one space and cuts
  at 32 characters with `…`. `""` and `unassigned` keep their own fixed sentences (`:560-567`).
- Every place the pin reaches: `unpinnedMessage` is the only alert builder that words `RolePin`
  (`alerts.go:327-336` is its only caller); the message string is what is stored in `alerts.message`, shown on the
  alerts pages and handed to `notify.Format` for Telegram, so one bound covers all three. The other readers of
  `RolePin` were already bounded or only compare: gate row `orchestrator/gate.go:416` (`agentPart(pin, maxGateValue)`),
  server page `web/handlers_servers.go:685` (`AgentQuoteN(server, …, maxShownPin)`, labelled), old-master box
  `web/handlers_oldmaster.go:452-458` (three known values or nothing), `monitor/start_state.go:295-333` and
  `monitor/state.go:239` (comparisons; `startPin` is kept in memory only).
- No other agent string in `alerts.go` is raw: `startUnguardedMessage` `:506`, `startBlockedMessage` `:516`,
  `keptHosts` `:530`, `quarantinedMessage` (`promote.go:286`), `lockExpiredMessage` (`promote.go:391`) all go through
  `agentText`; `master_locked` (`:199`) and `stoppedMessage` (`:660`) take `Status.Detail` only for statuses whose
  detail is the panel's own text (`status.go:266`, `:181-196`); backup alerts map the agent's reason through
  `backupReasonWords` (`:1230`).
- Tests: `TestMasterUnpinnedAlertBoundsAgentPin` (stored message, 5,000-character pin with `\n`, `\r\n`, U+202E) and
  `TestUnpinnedTelegramTextBounded` (Telegram text through `notify.Format`) assert what the item asked.
- Residual (Rendah, not a failure of the item — it is the form the ruling asked for): the 32 characters are the
  agent's and are not labelled or quoted in the alert, so a pin such as `a). Jalankan: <28 chars>` closes the
  bracket itself and puts up to about 29 characters after it in the panel's voice, on one line. The server page
  labels the same value as the agent's words (`AgentQuoteN`); the alert does not. The other `agentText` values in
  alerts (reason 300, file 300, host 120 characters, in brackets) have the same property and more room. Smallest
  fix if the owner wants it closed: drop `(`, `)` from the pin before it is shown, or show only
  `tidak dikenal` in the alert and leave the value to the server page.
- The web-side point (`Status.Detail` of "Tidak terjangkau"): the report's answer holds; I did not find a place
  where it is shown raw (alerts take it only for Locked/Stopped, see above).

### S5 (AG-1, the length check only) — ADDRESSED (the length check; AG-1's main part stays open by instruction)

- Refused before it is stored: `internal/agent/monitor/monitor.go:285-292` — a `backup_folder` longer than
  `maxBackupFolderLen` (1024, `backup.go:29`) is cut to its first 1025 bytes and copied (`strings.Clone`), and only
  that copy is compared with and stored in `m.backupFolder`. The stored value is therefore at most 1025 bytes and
  holds no reference to the request's string.
- Reported as an invalid folder: `backupState` (`backup.go:424-432`) runs `ValidBackupFolder` on the stored value;
  1025 > 1024 → `BackupUnreadable` / `BackupReasonInvalid`. `scanBackupFolder` (`backup.go:370-373`) never scans it.
- The rest of the call's settings are applied: interval, summaries, lag threshold and role hint are set at
  `monitor.go:274-284`, before the folder; `Configure` returns nil, the handler answers with a fresh report
  (`:233-238`). Same path as any refused folder (`TestConfigureKeepsSettingsWithBadFolder` unchanged).
- An old valid folder is not kept scanning: the cut value differs from the old one, so it replaces it and
  `backupGen` rises (`:289-292`); the old result is not reported any more (`backupState` returns invalid first).
  The same over-long folder sent again is no change (no new generation). Two different over-long folders that share
  their first 1025 bytes count as the same folder — harmless, both are refused.
- `ValidBackupFolder` checks the length first: `backup.go:48`, `len(p) > maxBackupFolderLen` before
  `filepath.IsAbs` and `filepath.Clean`.
- Panel-side limit equal: `internal/panel/web/backup_folder.go:33,59` refuses `len(p) > 1024`; a 1024-byte folder is
  accepted by both (`backupFolderCases`, shared by `TestBackupFolderRules` and `TestBackupFolderRulesMatchAgent`,
  gained the 1024-byte, 1 MiB and over-long-not-clean cases).
- Compatibility: the wire format is unchanged (`proto.MonitorConfigureParams` untouched). A panel of the previous
  version never sends more than 1024 bytes (its own rule already refused it), and a call without the field still
  means "no folder". A new panel with an older agent: the panel never sends an over-long folder.
- Nothing forbidden was touched: no semaphore, `internal/agent/session.go` and the unit in `scripts/install.sh` are
  not in the diff (commit fcbdd64 touches `monitor/backup.go`, `monitor/monitor.go` and two test files only).
- Tests match the claims: `TestConfigureRefusesOverlongFolder` (answer invalid, other settings applied, ≤1025 bytes
  kept, same folder again keeps the generation, no scan, 1024 bytes kept whole), `TestValidBackupFolderLengthFirst`
  (zero allocations for an over-long not-clean folder).
- What stays open, as instructed (goes to the owner): AG-1's main vector. The request's 1 MiB parameters are still
  held by the dispatcher and by the handler for as long as the call runs (`monitor.go:222-238` waits on `checkMu`
  and a MySQL read), one goroutine per request without a bound (`session.go`, unchanged). The length check removes
  only the copy the monitor kept and the `filepath.Clean` of the whole string. Worst outcome unchanged from AG-1:
  a compromised panel can grow the root agent until the OOM killer acts on the database server.
- Info: the panel's own rule still runs `filepath.Clean` before its length check (`backup_folder.go:59`); its input
  is a logged-in operator's form field, bounded by the form limit, so nothing follows from it.

### S4 (PC-1, ruling R43) — PARTLY

Addressed: the emergency kind, and the planned kind whenever the master's lock was never asked for or its unlock
was confirmed. Open: (a) the planned kind inside the lock window keeps the old gate-less, password-less Ulangi (the
fallback the wave allowed; truthfully reported), and that window can be re-armed; (b) after the window, when the
master's guard kept the lock, the one-click way the panel had to open the master is gone (new, see "New Breakage"
N1); (c) by the ruling's own boundary, a promote job that is "selesai sebagian" with `mysql.promote` sent and its
outcome unknown keeps the tokenless Ulangi, which is PC-1's emergency scenario from a narrower entry state (point 5).

**1. The exact refusal condition; nothing sent on a refusal.**
`promoteRetryRefused` (`orchestrator/promote.go:1710-1731`): the job is a promote job, its params are readable,
`!PromoteSent && !PastPoint`, and — planned kind only — NOT (`lockMayStandUntil` says the lock may stand AND now is
before that time). `lockMayStandUntil` (`:440-456`): "may" is false when `LockUnlocked`, or when the lock was never
asked for (`!LockTried && LockDeadline == nil`); otherwise until = `LockDeadline` (the master agent's answer), else
`LockTriedAt` + 1800 s + 5 min, else the promotion's end + 1800 s, plus `proto.PromoteLockGrace` (60 s). Called in
`Retry` at `runner.go:994-998`, under `startMu`, after `refuseSuperseded` and before the gate, the token and
`ReopenJob`. What runs before it for a promote job sends nothing: `retryConnectionsClear` and
`rebuildApprovalForJob` return at once for any kind but `create_replica` (`runner.go:855`, `approval.go:145`);
`refuseQuarantinedTLS` (`tls.go:792`) and `refuseRetryWhileLockMayStand` (`promote.go:341-365`) do not apply to a
promote kind; the rest are store reads. The web handler sends nothing either (`confirmedJobAction`,
`handlers_jobs.go:491-530`: gate/token only `if drops`; `prepareRetry` `:620-638` refreshes inventories only for a
wipe list). The refusal is audited as `job_retry` with the text (`jobAction`, `:653`), answered 409.
Because a job only ends "gagal" with `PromoteSent` false (`promoteFailed`, `promote.go:1024-1071`, resets it at
`:1061` only when the candidate is read-only and its agent job is over; `resumePromote` `:1157-1162`), the rule in
practice is: every promote job in status "gagal" is refused, except a planned one inside its lock window.

**2. Emergency job failed before the send — states, and the new start.**
Steps that can fail before `PromoteSent` is stored (`promoteNewMaster`, `:716-758`): `mysql.status` on the
candidate (`candidatePort`), opening the stored replication password, `mysql.tlsTrust` on the candidate
(`peerCred`). After the send, a definite refusal by the candidate's agent (the master answered, the IO thread is
still connected, an old agent) ends the same way once the candidate reads read-only and its job is over. Left
behind in every such case: promotion record `failed` (`promoteUndo` `:1106`), job `failed`, alert `job_failed`,
at most the panel's CA file on the candidate; no lock (this kind takes none, `:1107`); the candidate's agent put
replication back itself (`agent/drivers/mysql/promote.go:336-346`). A new emergency start is possible at once: a
failed promotion does not block (`:239`, only running/partial), `refuseWhileLockMayStand` passes because the
latest promotion is not a planned one (`:408`), and the failed job is not "open" (`ActiveJobForTopology` /
`ActiveJobForServer` count running, waiting, queued only: `store/jobs.go:220-253`; `refuseBusy`
`runner.go:367-383`). It is refused only by the start's own checks: mode re-read ("master masih sehat; jalankan
promote terencana", `:199`), the tick (`:205`), candidate eligibility, unreachable replicas. Confirmed by
`TestPromoteEmergencyRetryBeforePointRefused` (refusal, nothing sent, mode refusal, tick refusal, then success).
One state is NOT covered by this and is not "gagal": see point 5.

**3. Planned job failed before the send — what is left, what releases the lock, can the operator be stuck.**
Left behind: promotion `failed` (blocks no planned start; blocks an emergency start and data loads while the lock
may stand, `refuseWhileLockMayStand` `:390-433`); possibly the replication account made on a partial candidate and
the CA file (harmless); the candidate still a replica; on the master, the promote lock file and
`super_read_only` PERSISTed — only if the failure path could not confirm the unlock.
What releases the lock:
- the job's own failure path: `promoteUndo` → `mysql.unlockPromote` (`:1102-1143`); success or "tidak dikunci
  untuk promosi" records `LockUnlocked` → no window → Ulangi refused, new start works
  (`TestPromotePlannedRetryBeforePointRefused`);
- the master agent's guard at the deadline: unlocks after two read-only reads of the candidate 60 s apart and a
  read of every listed replica (`agent/drivers/mysql/promote_lock.go:654-694`); it KEEPS the lock when the agent
  was restarted after the lock (no credential, `:627-630`), the candidate is unreachable (`:640-642`) or writable
  without the master's transactions (`:652-653`), or a listed replica is unreadable (`:674-679`);
- a later `unlockPromote` for the same candidate (Ulangi inside the window; or the failure path of a new planned
  start of the same candidate, whose "Kunci master" step the agent refuses with "sudah dikunci untuk promosi"
  `promote_lock.go:182-184`), which the agent grants only while the candidate is readable and read-only and every
  listed replica is readable (`:438-497`) — the panel hands it the credential, so it also works after an agent
  restart;
- root's `sudo dbpanel agent unlock-master`. Not "Buka kunci master" (refused under a promote lock,
  `pins.go:207-211`), not Batalkan (`runner.go:1185-1186`).
Is the kept exception exactly the window in which a new start cannot work? No, on both sides:
- wider: the window is "the panel has no confirmation", not "the lock stands". When the unlock happened and only
  its answer was lost, or the lock call never reached the agent, a new planned start works at once, yet the
  gate-less Ulangi is still offered. And even with the lock standing the panel never refuses a new planned start:
  it is accepted (gate, tick, password) and fails at "Kunci master", and with the same candidate that failure opens
  the lock. What the exception really keeps is the one-click way and the agreement with the emergency refusal and
  README 1450-1462, which send the operator to Ulangi in exactly this window (same function, so text and button
  agree to the second).
- narrower: a lock the guard keeps outlives the window, and there Ulangi is gone (N1).
When the window ends: Ulangi disappears at the next page load (a page left open still shows the button; its post
is refused, 409, with the sentence) and the POST is refused (`TestPromotePlannedRetryWhileLockMayStand`: offered
at deadline + 59 s, refused at + 61 s, nothing sent). A new start then works when the guard opened the lock, which
is the normal case; at the boundary itself (guard round, clock difference between panel and master) it can still
meet the lock for some seconds, fail at "Kunci master" and open it through its own failure path.
Can the window be held open? Yes, three ways, none new in this diff but all now decide how long the gate-less
Ulangi lives:
- every Ulangi that runs "Kunci master" again sets `LockTriedAt` to now and `LockUnlocked` to false
  (`promoteLock`, `:634-636`). With no `LockDeadline` stored (the first lock's answer was lost, or the lock was
  refused and the unlock was not confirmed) each such Ulangi that fails again with the unlock unconfirmed (master's
  agent unreachable) moves the window to now + 36 min. One click every half hour keeps it open without limit; the
  same click also pushes back the time from which an emergency promote is allowed (`:415-428`), and the emergency
  refusal's own text tells the operator to press it;
- a relock that succeeds stores a new deadline; a later failure with the unlock unconfirmed starts a new window;
- `LockDeadline` is the master agent's answer, stored unbounded (`:641-642`): a compromised master agent holds the
  window open for as long as it likes with one answer (see Out-of-Scope).
A slow or briefly unreachable master agent alone (no click) cannot extend it: with a stored `LockDeadline` the
window never moves (`:446-447`).

**4. Inside the kept window: what Ulangi still skips, what still guards.**
Skipped: the gate (`gateActionForJob` has no promote kind, `runner.go:709-724`), the backup tick, the typed
confirmation and password (`dropsData` false → no token, `:653-668`), the mode re-read and `PromoteView`
(candidate eligibility, branched, writable or unreachable replicas as they are now). What it does: continues the
same promotion from the failed step — before the deadline without a new lock (`locked()`, `:659-661`), after it
with a relock (`relock` `:665-672`) — and can complete it. Still guarding: the master's agent (a second lock is
refused; a new lock needs the candidate connected as a replica and readable, `promote_lock.go:177-215`), and the
candidate's agent for a planned promote: the old master must answer, be `super_read_only`, the candidate must hold
all its transactions, twice, and the lock must not be about to end
(`agent/drivers/mysql/promote.go:423-434, 465-483`). So this path cannot make two masters by itself; what is
bypassed is the Prasyarat list (clock, TLS, pin, account, backup …), the backup tick and the password, for a
re-run of a promote that passed all of them at most about 31 minutes earlier (36 for a lost answer), unless
re-armed as above. That is strictly smaller than PC-1's planned half (which had no time limit) but it is not
zero, and it is the common case after a panel restart during a planned promote (`resumePromote` attempts no
unlock, so every such job is in the window).
Described truthfully? Report: yes ("Decision", "Noticed"), except that "the window is bounded by … at most
~30 min + 60 s … or LockTriedAt + 35 min" does not say that Ulangi itself moves `LockTriedAt`. README: the new
paragraph (2894-2903) names the exception and its end correctly, but does not say that this Ulangi runs without
the Prasyarat list and without the password; and the three sentences "Tidak ada tombol untuk melewati gerbang"
(1527), "Tidak ada tombol untuk melewati baris merah" (3443; mirrored in `web/templates/panduan.html:91`) and
"Tidak ada tombol untuk melewatinya" (4287-4288) are still not exactly true inside the window. R43 expected the
fix to make that sentence true; it does so only outside the window.

**5. Jobs past the point of no return: unchanged.**
`promoteRetryRefused` returns false for `PromoteSent || PastPoint` (`:1718-1720`); nothing else in `Retry`
changed. `TestPromoteRetryPastPointStillRuns` (partial after a failed quarantine: no refusal, no token, not gated,
a red row on the new master, succeeds) and the rewritten `TestPromoteRetryStillTokenless` (both packages) hold it.
To be exact about the boundary: "past the point" here includes a job with `PromoteSent` true and `PastPoint`
false — `mysql.promote` was sent and the panel could not learn the outcome (the start call timed out or the
candidate's agent was unreachable when asked, `:767`, `:1031-1038`, `:1048-1057`; a panel restart after the send,
`:1157-1159`). Such a job is "selesai sebagian", blocks every new promote (`:239-240`), and its Ulangi sends
`mysql.promote` again at once (`:740` skips the relock, `:757-761`) with no mode re-read, tick, name or password,
at any later time. The candidate may in fact never have opened writes. For the emergency kind this is PC-1's
scenario entered through "the candidate's agent dropped while it probed" instead of "the candidate refused": days
later one click, and the candidate's three probes are the only check. The ruling leaves this state its Ulangi
(it is the only way to settle the job), so it is not a failure of the fix, but it is the part of PC-1 that
remains for the emergency kind. Smallest way to close it: at Retry, for `PromoteSent && !PastPoint`, first ask
the candidate what `promoteFailed` asks (`mysql.status`, `job.status`); when it is read-only and its job is over,
fail the job (`PromoteSent=false`, `promoteUndo`) and answer with the new refusal instead of sending again.

**6. The job page.**
`renderJob` (`web/handlers_jobs.go:340-344`): for a failed or partial job that is not superseded,
`RetryRefused(job.ID)` non-empty clears `CanRetry`; the template renders the button only `{{if .CanRetry}}`
(`job_detail.html:84-92`) and shows the sentence as an info banner (`:19`). `RetryRefused` (`promote.go:1736-1750`)
uses the same function as `Retry`, so hidden ⇔ refused, apart from a store error (button shown, the post refused
with that error) and a page rendered before the window ended. A superseded job shows only the superseded banner.
`TestPromoteRetryRefusedHidesButton` covers both kinds through the fake orchestrator.
Is the sentence true? "mulai promote baru dari halaman master": the master page always shows the card
**Ganti master** with the link **Promote…** (`master_detail.html:95-102`, no condition), and the promote screen is
blocked only by a pending cutover, a running or partial promotion, a writable replica, no selectable candidate or
an open job (`handlers_promote.go:610-634`); a failed promotion blocks none of them. So yes for the emergency kind
and for the planned kind without a standing lock. Not quite when the lock still stands after the window (N1): the
new promote will fail at "Kunci master". Small points: the banner is plain text; the way to the master page from
the job page is the server link in the title and then "Buka halaman master" (two clicks; a link to
`/masters/{id}/promote` in the banner would be kinder in an outage). After a refused POST the sentence appears
twice (error flash and info banner).

**7. Tests.**
`TestPromoteRetryStillTokenless` (orchestrator) now fails the quarantine, gets a partial job and retries it with
`JobAck{}`; the web one stores past-point params and still asserts the plain `/retry` form: both keep guarding
"no token for a promote past the point". New: emergency (refused, nothing sent, new start with mode/tick),
planned (refused, new start gated then green), the window exception (offered inside, refused after), past the
point, the exact text, the page. Neighbours changed only where they asserted the old rule
(`TestRetryGatedOnlyWhenDataIsDropped/promote`, `TestPromotePlannedCatchUpTimeoutUnlocks`,
`TestPromoteFailureAtNewMasterUnlocksOrGoesPartial`), each now asserting the refusal and moving the rest of its
story to a new start; nothing was loosened. Not covered: a job partial with `PromoteSent && !PastPoint` staying
retryable (only implicitly, by older tests); the re-arming of the window; a new start after the window while the
lock still stands (N1); an emergency job that fails before the send on `mysql.status`/`mysql.tlsTrust` (only the
"candidate refused" entry is tested; same code path afterwards); the relock on Ulangi, which lost its only direct
assertion (the old `lockForPromote == 2` check) and is now reachable only inside the window.
I ran no test (a full `go test ./...` was running while I read this part; afterwards nothing was left that a
fake-agent test would settle better than the code: N1 depends on the real agent's lock handling).

**8. Other paths that continue a promote job.**
None re-runs the promotion step. Approve: no promote step ever waits (no `wait(...)` in `promote.go`), so a
promote job is never "menunggu" and `Approve` answers `errNotWaiting` (`runner.go:918-919`). Resume after a panel
restart only fails the job or marks it partial (`runner.go:1226-1287`, `promote.go:1149-1163`); it launches
nothing. `OnOnline` sends only a pending quarantine or a repoint of a promotion already made
(`promote.go:1479-1554`). `Retry` and `Approve` have one caller each, the two web routes (`router.go:242-243`).
Cancel of a running job before the send calls `promoteUndo` and ends "dibatalkan" (never retryable).

## New Breakage in the Fix Diff

**N1 — Sedang. After the lock window, a master whose promote lock the guard kept has lost its one-click way out
in the panel; the page's sentence sends the operator to a promote that fails at "Kunci master".**
`internal/panel/orchestrator/promote.go:1721-1729` (the exception ends at deadline + 60 s whatever the master
says), `web/templates/job_detail.html:19`.
- Scenario: a planned promote fails before the send (catch-up timeout, a refusal, or a panel restart) and the
  unlock is not confirmed because the master's agent is unreachable at that moment. The agent comes back after a
  restart (or the candidate / a listed replica was unreadable at the deadline): the guard keeps the lock
  (`agent/drivers/mysql/promote_lock.go:627-630, 640-642, 674-679`) and the master stays `super_read_only` — the
  applications cannot write. More than about 31 minutes after the lock, the operator opens the failed job.
- Before the fix: "Ulangi dari tahap ini" → the relock is refused by the master's agent → the failure path sends
  `mysql.unlockPromote` with the candidate's credential → the agent, which can now read the candidate, opens the
  master (`promote_lock.go:456-497`). One click, no gate. README 1450-1457 describes this use of Ulangi.
- After the fix: no Ulangi; the banner says "mulai promote baru dari halaman master". That works only as a side
  effect and only if everything a planned start needs holds: planned mode, the same candidate still eligible, no
  red Prasyarat row, backup tick, password. The new job then FAILS at "Kunci master" ("sudah dikunci untuk
  promosi … kunci baru ditolak") and its failure path opens the master; a third start is the one that promotes.
  With another candidate the agent refuses the unlock ("kunci promosi ini untuk kandidat X, bukan Y") and a new
  36-minute window starts on the new job. With one red row (for example a stale backup, or an agent version row
  after the very agent reinstall that caused the restart) the panel has no way out at all: "Buka kunci master" is
  refused by the agent (`pins.go:207-211`), and what remains is `sudo dbpanel agent unlock-master` as root on the
  master (named by the `master_locked` alert and by that refusal, so the operator is not left without any text).
- This is the case the wave's rule ("never strand a master read-only with no way out in the panel") and R43's
  stop condition were written for. It is not total — a green gate with the same candidate still opens it — but
  the report's sentence "no way out the panel had is removed" is not correct: an unconditional one became one
  that depends on the gate and on choosing the same candidate, and nothing on screen says so.
- Worst outcome: a longer write outage on a healthy master, and an operator who follows the sentence literally
  gets a second failed promote. No two-master risk.
- Confirmed: by reading only (panel and agent code; the pre-fix path was pinned by the assertion removed from
  `TestPromotePlannedCatchUpTimeoutUnlocks`, "the retry took the lock again … and lifted it again").
- Smallest fix: for a planned job with `LockTried && !LockUnlocked` whose window has passed, let Ulangi (or a
  button of its own, e.g. "Pastikan kunci master terbuka") run only `promoteUndo` — the `mysql.unlockPromote`
  with the candidate's credential and the agent's own checks — and no step: the job stays "gagal", a confirmed
  unlock or "tidak dikunci untuk promosi" records `LockUnlocked`. The same unlock-only action could replace the
  in-window exception as well, which would close PC-1's planned half completely and make the README sentence
  true. If no code change is wanted now: a second sentence on the job page for exactly this state — that the
  master may still be locked, that a new promote of the same replica opens it by failing once, and the
  `unlock-master` command with its caution — plus the same in README 2894-2903.

**N2 — Rendah. A failed promote can now show two banners that contradict each other.**
`internal/panel/orchestrator/promote.go:545-546` (unchanged line, made untrue by the fix): the step error for an
agent that does not know a promote command ends "… jalankan perintahnya, lalu Ulangi." It is produced before the
send (`mysql.lockForPromote` `:637`, `mysql.tlsTrust` `:1234`, and `mysql.promote` itself `:768`, which ends
"gagal" because an unknown command is a definite refusal). The job page then shows that text as the error banner
and "Ulangi … tidak tersedia …: mulai promote baru" as the info banner, with no Ulangi button. Smallest fix: end
that text with "lalu jalankan promote lagi" (it is also used after the point, where Ulangi exists; "promote lagi"
is true in both).

**N3 — Rendah (texts). README and the guide still say there is no button past the gate.**
README 1527, 3443 (mirrored at `web/templates/panduan.html:91`) and 4287-4288; the new paragraph at 2894-2903
gives the exception but not that this Ulangi skips the Prasyarat list and the password. Not made worse by the
fix (it was less true before); listed because R43 named this sentence as what the fix would make true. Smallest
fix: one clause in the new paragraph ("Ulangi itu melanjutkan promote yang sama tanpa menilai lagi daftar
Prasyarat dan tanpa kata sandi"), and the owner decides whether the three sentences get "kecuali …".

Nothing else found in the S2, S4 and S5 parts of the diff: no check weakened, no test-only switch in production
code (`monitor/export_test.go` is a test file), the moved deadline arithmetic (`lockMayStandUntil`) is the old
code unchanged, the new interface method is implemented by the runner and the web fake.

## Out-of-Scope Observations

- The lock window can be moved by pressing Ulangi (`promote.go:634-636` with `:448-449`), and each move also
  pushes back the hour from which an emergency promote is allowed (`:415-428`) while that refusal's own text tells
  the operator to press Ulangi "bila agent master tersambung lagi". An operator who presses it while the master's
  agent is still away postpones the emergency promote by 36 minutes each time. Older than this diff.
- `LockDeadline` is taken from the master agent's answer without a bound (`promote.go:641-642`). A compromised
  master agent can keep "the lock may stand" true for any time: emergency promote and data loads refused, the
  gate-less Ulangi kept. Older than this diff; I found no earlier note of it in this workspace's audit files
  (searched, not exhaustive).
- A planned promote that was cancelled (Batalkan) with its unlock unconfirmed: the emergency refusal tells the
  operator to press Ulangi on that job, which a cancelled job never offers (`runner.go:1029-1030`); the window then
  simply has to run out. Older than this diff.
- `lockExpiredMessage` (`monitor/promote.go:398-399`) advises `tekan "Buka kunci master" di halaman master` for a
  lock the guard kept; the agent refuses that command under a promote lock and names the CLI instead
  (`pins.go:207-211`). The job log line "agent master membukanya sendiri saat tenggat kunci" (`promote.go:1137`)
  is likewise not true when the guard keeps the lock. Older texts.
- S2: `monitor/state.go` keeps the agent's last role pin (`startPin`) in memory unbounded (up to the 64 KiB
  report limit per server); never shown or stored. Info.
- S5: the agent logs up to 1,024 bytes of a refused folder on each check round (`backup.go:371`, `%q`-escaped).
  Info.
- The `job_failed` alert of a promote that failed before the send now stays open until a later job on that master
  ends, since the job itself can no longer be re-run to success; as before for any failed job nobody retries.

## Verdict

S2 and S5 are addressed as asked (S5 closes only the stored-length part of AG-1; the request flood stays with the
owner). S4 is partly addressed: Ulangi of every "gagal" promote is refused with nothing sent and a new start is
really possible for the emergency kind and for a planned promote whose lock is confirmed open; still open are the
gate-less, password-less Ulangi of a planned promote inside the lock window (about 31-36 minutes, re-armable by
pressing it; worst outcome a planned promote re-run without the Prasyarat list — the agents' own checks still
prevent two masters), the same tokenless Ulangi for a promote "selesai sebagian" whose `mysql.promote` outcome is
unknown (worst outcome PC-1's emergency case: two masters if the candidate's probe is fooled by a partition), and
one new regression (N1): after the window a master left locked by its guard can no longer be opened by one click
in the panel, only by a new gated promote of the same replica that fails once, or by root's `unlock-master`
(worst outcome a longer write outage, no data divergence).
