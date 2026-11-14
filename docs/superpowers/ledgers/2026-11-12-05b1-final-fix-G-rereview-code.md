# Final fix wave, part G (panel): security re-review of abb56bc..0c88ce1 (head 82b9df0)

How this was done:
- Inputs: `final-fix-D-rereview-code.md`, the G brief, the GF addendum (G-6 only), the "## Part G" section of `final-fix-D-report.md`, `final-fix-G-code.diff`, and the design note (background only).
- Code was read at head. The graph was queried first (`graphify query`), as the repo rule requires.
- Nothing was run except one throwaway sanitizer test (`internal/panel/redact/zz_rereview_test.go`, run with `-run TestZZRereview`). It has been deleted, and `git status --short` is empty.
- Line numbers refer to head.

## Verdicts

### G-1 (N-B1): the settle flag. PARTIALLY ADDRESSED

What is right:
- The flag is set only after the agent's `mysql.configureReplication` returns OK on a rebuild (`internal/panel/orchestrator/create_replica.go:952-958`):
  ```go
  if jr.st.RebuildOldMaster && !jr.st.QuarantineEnded {
      jr.st.QuarantineEnded = true
      jr.save()
      if err := jr.r.d.Store.ClearOldMaster(jr.target()); err != nil {
          return outcome{}, err
      }
  ```
- I checked the agent side (`internal/agent/drivers/mysql/replication.go:83-93`, `quarantine.go:444-500`, `pins.go:711-740`). An OK answer means the pin is no longer quarantined: either `settleRebuilt` rewrote it, or it was not quarantined under that source. A different source is refused earlier by `claimSource`. Every error return leaves the pin quarantined. So the flag is never set before the settle.
- Cancel after the settle: `runner.go:1176`, `oldMaster := st.RebuildOldMaster && !st.QuarantineEnded`. At that point the agent holds pin `replica`. Its `Release` allows replica → unassigned (`pins.go:600-612`), and `releaseRole` runs after `resetReplica`. So "role unassigned + releaseRole" is safe and truthful. If `releaseRole` fails (agent offline), the panel says unassigned while the agent stays pinned replica. That is the same best-effort behaviour as any cancelled Buat replika.
- `cancelJob` re-reads the stored job (`runner.go:505-512`). So a cancel that arrives after the save sees the flag.

Interleavings walked:

| Interleaving | Stored flag | Marker | Agent pin | Result |
|---|---|---|---|---|
| OK, panel stops before the save | false | kept | replica | AwaitingRebuild state (the "safe side"). Retry re-runs configureReplication. The agent accepts it as a plain replica with `required:false`, which sets the flag and clears the marker. Healed (`TestRetryConnectHealsQuarantineEnded`). But "Batalkan percobaan" is also accepted here, and the panel then records a quarantined old master whose agent is a replica with replication configured. That is conservative but untrue; see residual R-G2. |
| OK, `save()` fails | false (the error is swallowed, `runner.go:1646-1654`) | **cleared** | replica | A later failure gives an ordinary failed replica, which is right. A later **cancel** reads flag=false from the store, so `keepQuarantined` runs: role quarantined, marker gone, no `releaseRole`. That is exactly the "quarantined server with no old-master record" the implementer said G-1 avoids. Finding **N-G2**. |
| OK, saved, `ClearOldMaster` fails | true | **kept** | replica | The job fails at the connect step. AwaitingRebuild is true: box, "Masih dikarantina" lines, D-10 refusals. A Retry never clears the marker again, because the block is guarded by `!QuarantineEnded`; only a successful verify clears it. `ReturnToQuarantine` does not look at the flag, so "Batalkan" is accepted against a settled agent. Finding **N-G2**. |
| Cancel while configureReplication is in flight | false | kept | replica (if the agent finished) | `keepQuarantined`, `resetReplica`, no `releaseRole`. The panel says quarantined while the agent is a replica. This is conservative: a new rebuild needs no approval and heals it. Nothing else can know. Residual R-G2. |
| Cancel between the answer and the save | true (the goroutine saves before it sees ctx) | cleared | replica | Ordinary cancel, unassigned. Correct. |
| `callTimeout` expires while the agent still settles | false | kept | replica | Same as "panel stops before the save". |
| Second promote of T while the rebuild is running | — | overwritten by `SwapMaster` | — | Out of scope (R-3 kept by the brief). Not made worse: G-1 only clears the marker when it still names X. `ClearOldMaster` is keyed by server. |

Readers of the marker between the connect step and the end of the job: I agree with the implementer's list (report lines 252-260). The monitor and the promote resend read it only for role quarantined, and the role is replica during the job.

### G-2 (N-B2): "Batalkan percobaan bangun ulang". ADDRESSED, with one gap (inside N-G2)

Store and orchestrator:
- `Store.ReturnToQuarantine` (`internal/panel/store/topologies.go:375-410`) is one transaction. It reads the row, the role (`serverRoleTx`) and `topologies.old_master_server_id` inside the transaction. It refuses with `ErrNotFailedRebuild` (rollback, nothing changed) unless `state == failed && role == replica && n == 1`. Then it deletes the row and sets role quarantined.
- It does **not** check the job's `QuarantineEnded`. The brief listed "a flagged job" as a refusal. The implementer equates "flagged" with "marker cleared", which fails when `ClearOldMaster` fails (N-G2).
- `Runner.CancelRebuildAttempt` (`controls.go:156-172`) takes `startMu`, then `refuseBusy`, then the store call, and makes no agent call.
- Races:
  - Retry takes `startMu` across `ReopenJob` (`runner.go:849-878`). Either the job is already running, so `refuseBusy` refuses; or the cancel ran first, and Retry is refused by `supersededBy` ("tidak lagi terdaftar sebagai replika").
  - A new rebuild or `StartMySQLTLS` runs under `startMu`. TLS is refused in both states anyway.
  - A promote of T is covered by `refuseBusy`'s topology check. A `SwapMaster` that commits first makes the in-transaction marker check fail.
  - Renewal does not take `startMu`, but `quarantinedForTLS` is true both before and after.
  - Late writes from the finished job goroutine hit `ErrNotFound`.
  - I found no window where a Retry can start between the check and the transaction.

Readers afterwards:
- The box shows Periksa (copy not started), Hapus and the rebuild link.
- The failed job is superseded, so the job page hides Retry and the RebuildURL line (`handlers_jobs.go:322-326`).
- The new master is deletable once the old master's row is gone (`TestCancelRebuildAttemptThenDelete`).

Web:
- The route is `POST /servers/{id}/rebuild-attempt/cancel` (`router.go:209`), wrapped in `authedPost`, which is login + `sameOrigin` + `limitForm` (`router.go:183-185`).
  - Unauthenticated requests are redirected to `/login` before the handler runs.
  - The method is POST only.
  - The id is parsed by `serverFromPath`.
- The handler (`handlers_oldmaster.go:606-626`) calls `requireConfirm(…ActCancelRebuildAttempt, auditServer, srv.ID)`. That is the password-confirmation token, bound to user, action, kind and id.
  - Without a token: 403, plus a `confirm_rejected` audit entry (`handlers_confirm.go:243-247`).
- Audit: `a.audit(r, "cancel_rebuild_attempt", auditServer, srv.ID, map[string]any{}, outcomeOf(err))`. It records the operator (from the session), the server and the outcome, on refusal too. The label is in both label maps.
- Escaping:
  - The partial is `_ui.html:73-75`. In `data-confirm-text="Batalkan percobaan bangun ulang di {{.Name}}. {{cancelAttemptText}}"`, html/template escapes for the attribute context.
  - `app.js:380` reads `dataset.confirmText`, and `_confirm.html:14` renders it with `x-text` (textContent). No HTML sink.
  - The new sentences in the box and replica page are template text, auto-escaped (`"` becomes `&#34;`; the test expects that).
- On refusal the flash is `err.Error()`. For a store error that is internal SQL text, which is the existing pattern of the other controls.
- Detach in stop mode stays refused (D-10), and Lepas is hidden in this state (`handlers_mysql.go:937-940`).

### G-3 (N-B3): leftover data after the copy. PARTIALLY ADDRESSED

What is right:
- The sentences match the brief character for character:
  - `CopyStartedText` (`oldmaster.go:123`);
  - `msgCopyStartedForm` and `msgAttemptPending` (`handlers_oldmaster.go:390-393`);
  - `msgCancelAttempt`.
- Box (`server_detail.html:382-391`): copy started → box sentence. Otherwise `RebuildFailed` → attempt-pending sentence. Otherwise the buttons.
- Form (`handlers_oldmaster.go:1002-1020`): the "Jalankan Periksa … dulu" and "Data tertinggal belum diperiksa" texts are not reachable when `noCheck != ""`.
- Earlier results stay visible as history (the table above line 382 is unconditional).
- The orchestrator refuses, not only hides: `StartLeftover` and `StartSalvage` call `refuseAfterCopy` right after `oldMasterOf` (`oldmaster.go:189`, `:210`). In the failed state they are already refused by `oldMasterOf` (role must be quarantined, `msgNotOldMaster` on the web).

`RebuildCopyStarted` (`oldmaster.go:131-170`) against the job shapes:
- **Step names:** read per job from the stored steps by name. "Salin data" has been the literal since before this round, so jobs from before the certificate step and before this round are found. Resync jobs are skipped. Only `RebuildOldMaster` create_replica jobs count.
- **Job cancelled before start:** `CurrentStep` is 0, so not started. Correct.
- **Unreadable params:** count as started. That fails closed, but it refuses a needed action on corruption; acceptable.
- **"Since the promotion":** decided by `j.CreatedAt.Before(prom.StartedAt)` against the latest promotion naming X as old master (`promotions.go:100`, `ORDER BY id DESC`).
  - A second promote that makes X old master again starts a newer row, so earlier rebuilds are excluded. Correct.
  - Wall-clock comparison: see N-G6.
- **Main defect, N-G1 (Important).** "Reached the step" is not "the copy started". The step `Salin data` (`replicaSnapshot`, `create_replica.go:706-761`) does several things before the first destructive agent command:
  - `preflightBeforeDrop` (`:711`), which can **wait** (WaitWipe or WaitForeignSource) at this step;
  - the CLONE wait (`:719`);
  - `replAccount` (`:726`);
  - `verifySnapshot` / `trustCA` (`:732`);
  - an agent offline ("terputus saat Salin data");
  - the agent's strict-mode refusal of `mysql.dropDatabases`, `dumpFrom` or `cloneFrom` (all `strictProtected`, `strict.go:71-73`) when the approval expired.

  All of these leave X's data intact. Afterwards the panel states "data lamanya sudah dihapus atau diganti", which is false, and permanently refuses Periksa and Amankan.

Old sentences still shown, now untrue:
- `monitor/explain.go:71`: the StatusQuarantined advice `Tekan "Periksa data tertinggal" untuk melihat transaksi …`. It renders on the status panel (`_status.html:42`) of every quarantined server, including the copy-started one reached by G-2 or by a cancel.
- `monitor/alerts.go:385` (`startLateText`) and `monitor/promote.go:695` say "jalankan Periksa data tertinggal".

Those are in `internal/panel/monitor`, which the brief kept out. Finding **N-G3**.

### G-4 (N-B4): Buat replika targets. ADDRESSED
- `handlers_mysql.go:606-616`: a failed replica row is listed only when `!awaiting`. A store error returns the error (500), which fails closed.
- The direct POST is still refused by the D-10 check (`create_replica.go:149`, test `TestFailedRebuildRefusesResyncAndDetach`).

### G-5 (R-5): certificate jobs. ADDRESSED
- `quarantinedForTLS` is `AwaitingRebuild`, and true on a store error (`tls.go:807-814`).
- It is used at `StartMySQLTLS` (`:65`), the renewal (`:108`), `StartTightenTLS` (`:529`) and `refuseQuarantinedTLS` (`:797`). The texts are unchanged and stay true in the failed state.
- Dead-end check: the rebuild job carries its own certificate step (`stepInstallCert`, under the rebuild approval). No precheck of the rebuild or of the cancel needs X's own MySQL certificate to be valid first (`create_replica.go` and `tls.go` read only the master's record, `tls.go:426`, `:443`). So refusing `StartMySQLTLS` blocks nothing the operator needs. After the settle (G-1) the server is ordinary again, and renewal resumes.
- One remark: the renewal still proceeds when `GetServer` itself errors (`err == nil && …`). The fail-closed path covers only the store errors inside `AwaitingRebuild`. In tighten, fail-closed means "skip silently". See Out-of-Scope.

### G-6: the sanitizer. PARTIALLY ADDRESSED
Correct:
- **Charset:** `proofUser` (`redact.go:155-166`) is exactly `^[A-Za-z0-9_.$-]+$`, the same as `proofUserRe` (`internal/agent/drivers/mysql/rebuild_load.go:245`). The host is checked by the existing `host` (`redact.go:280-293`). Its character set `[A-Za-z0-9.\-:%_/]` equals `proofHostRe` (`rebuild_load.go:246`). `host` also requires one of a letter, `.`, `:` or `%`, so an agent-accepted host of only digits, `-`, `_` or `/` is censored (fail-safe, cosmetic).
- **What the agent prints:** `quoteString` escapes nothing for these charsets. It prints `akun MySQL %s yang` and `GRANT SHOW DATABASES ON *.* TO %s;` (`rebuild_load.go:208-209`). The anchors match.
- **The phrases:** all five `panelPhrases` (`redact.go:100-106`) are fixed button or README labels with no data meaning. Keeping one reveals only that the literal equalled a constant. My inventory grep agrees with the report: the remaining quoted phrases do not reach `Quotes`.
- **Templates still win:** `template` runs before `advisedAccount` (`redact.go:59-68`). I tested Duplicate entry, `Query: '…'`, `near '…'`, `Error '…' on query … Query: '…'`, `Incorrect string value: '…' for column`, and `Truncated incorrect DOUBLE value:`, each with a lead plus pair or a phrase inside the value. Everything was censored.
- **Stored XSS:** the kept text contains only `'`, `"`, `@`, the phrase letters and the restricted charsets. Job errors are rendered through html/template, so there is no XSS.

Defects found with the throwaway test: **N-G4** (fixed point broken) and **N-G5** (leak through nested quotes), below.

## New breakage in the fix diff

### Important

**N-G1. "Copy started" counts a rebuild that only reached the step; the panel then says the data is gone and refuses Periksa and Amankan forever.**
- Code: `oldmaster.go:159-161` (`j.CurrentStep >= i`); refusals at `oldmaster.go:189`, `:210`; box `server_detail.html:382`; form `handlers_oldmaster.go:1004-1005`.
- Scenario:
  1. The operator starts Bangun ulang. The steps through "Siapkan replika" pass.
  2. At "Salin data" the agent refuses `mysql.dropDatabases` because the root approval expired (or the agent drops off the network, `verifySnapshot`/`trustCA` fails, or the job sits in a WaitWipe after a Retry and the operator cancels it).
  3. Nothing was dropped and no binary log was reset.
  4. The box now says "data lamanya sudah dihapus atau diganti". After "Batalkan percobaan" or the cancel, `StartLeftover` and `StartSalvage` refuse with that sentence.
- Effect: the old master's un-replicated hospital transactions, still on disk, can no longer be checked or salvaged through the panel. The next rebuild destroys them.
- Before this round, a cancelled attempt left Periksa available. This is a regression, and the brief's own definition ("reached the step") causes it.
- Fix:
  - Save a `CopyBegan` flag in `jobState` right before the first destructive command: the `dropDatabases` call, or `copySnapshot` when there is no wipe list (also set it on `reattach`). Save it before sending, so a lost answer counts as begun.
  - Let `RebuildCopyStarted` read that flag.
  - For jobs stored before the flag existed, keep the step rule only when `CurrentStep > i`, or when `CurrentStep == i` and `AgentJobID` or `SnapshotGTID` is set.

### Minor

**N-G2. A store failure inside the settle block leaves the flag and the marker inconsistent, and nothing heals it.**
- Code: `create_replica.go:952-958`, `runner.go:1646-1654` (`save` swallows errors), `topologies.go:375-410` (no flag check).
- (a) `save()` fails and `ClearOldMaster` succeeds. If the operator then cancels the running job (`startReplica` or `waitSynced` slow), `cancelJob` reads flag=false and sets role quarantined with no marker and no `releaseRole`, while the agent is pinned replica.
- (b) `ClearOldMaster` fails after a successful save. The job fails with the flag stored true and the marker kept. Retry skips the block, so the marker stays until a verify succeeds. `ReturnToQuarantine` accepts "Batalkan" against an agent that has ended its quarantine and has replication configured. That is the brief's "flagged job" case.
- Fix:
  - Make `save` return an error and abort the step before `ClearOldMaster` when it fails.
  - In the connect step, run `ClearOldMaster` whenever `RebuildOldMaster && QuarantineEnded`, not only when the flag is first set.
  - In `CancelRebuildAttempt`, refuse when the server's latest rebuild job has `QuarantineEnded`, with the transaction still checking the store state.

**N-G3. Untrue advice remains after the copy started.**
- Code:
  - `monitor/explain.go:71`: StatusQuarantined advice, shown on every quarantined server's status panel;
  - `monitor/alerts.go:385` and `monitor/promote.go:695`: the master_start_late texts "jalankan Periksa data tertinggal".
- Scenario: a quarantined old master whose attempt reached the copy, reached through G-2 or a cancel. The status panel says `Tekan "Periksa data tertinggal" …`, which the page does not offer and the orchestrator refuses.
- If MySQL on X starts writable late, master_start_late opens. It tells the operator to run the check, and it can never close through a check (`monitor/promote.go:713`, the close needs a later check result). It stays open until the rebuild or the delete.
- Fix: the web layer replaces the status advice when `CopyStarted`, or the owner allows a text change in `internal/panel/monitor`.

**N-G4. The new keep rules break the promised fixed point `Quotes(Quotes(s)) == Quotes(s)`.**
- Evidence from the throwaway test:
  - `'Paksa dump'"Budi"` → `'Paksa dump''…'` → `'…'`
  - `akun MySQL 'a'@'b.c'"x"` → `akun MySQL 'a'@'b.c''…'` → `akun MySQL '…'@'…'`
  - Every cut of these inputs at a byte boundary from the second quote on fails the same way.
- Cause: a kept literal directly followed by a literal in the other quote character becomes `''` (a doubled, "escaped" quote) after the first pass.
- The existing `panelAccount` host path has the same pre-existing defect (`'dbpanel_repl'@'h.x'"Budi"`). The round extends it to both new rules.
- No data leaks; the second pass censors more. But stored text that is censored again (`monitor/alerts.go:188`, `:582`) changes, and the property the round's tests claim to check is false. `phrases_test.go`'s fixed-point test has no input with adjacent literals.
- Fix: when writing `Censored` right after a kept literal that ends in the same quote character, separate them, or emit the censored token with the other quote character. Add adjacent-literal inputs to the fixed-point tests.

**N-G5. Rule (b) keeps an account-shaped pair taken from row data in a message MySQL does not template.**
- Evidence from the throwaway test:
  - `Unknown column 'x' akun MySQL 'NIK3201234567890001'@'budi.santoso' y' in 'where clause'` comes out with `'NIK3201234567890001'@'budi.santoso'` intact.
  - The same happens with `GRANT SHOW DATABASES ON *.* TO 'Budi_Santoso'@'1985.03.12'`, inside `Variable … can't be set to the value of '…'`, and after a `'x'` in a deadlock text.
- Before the round the pair was censored. The unquoted parts (" akun MySQL ", " y") already leaked before: that is the pre-existing weakness for nested quotes in non-template messages. Reaching it needs the value itself to contain `' akun MySQL '` or the GRANT prefix, so patient data will practically never trigger it by accident.
- The lead match is a plain `HasSuffix`, so `éakun MySQL '…'` also qualifies.
- Fix: also require a non-word character, or the start of the message, before the lead. Better, anchor on the agent's whole sentence prefix (`tidak bisa dipastikan lengkap: akun MySQL ` and `di server ini: GRANT SHOW DATABASES ON *.* TO `).

**N-G6. "Since the promotion" compares wall-clock times.**
- Code: `oldmaster.go:144` (`j.CreatedAt.Before(prom.StartedAt)`).
- Scenario: if the panel's clock steps back (NTP correction, VM restore) by more than the time between the promote start and the rebuild's creation, a rebuild that copied is ignored. Periksa is then offered on dumped data, and the check finds X's GTIDs contained in the new master's. "Tidak ada data tertinggal" is false reassurance.
- Fix: store the promotion id in the rebuild's `jobState` (it is known at `StartCreateReplica`) and match on it, or compare by job and promotion order rather than by time.

## State table (old master X, topology T, new master N)

How to read the table:
- **Pin:** X's agent role pin.
- **Marker:** `topologies.old_master_server_id = X`.
- **Offers / Accepts:** the page / the orchestrator.
- **✗** marks a cell where the panel offers something refused, refuses something needed, says something false, or leaves no way out.
- **Batalkan:** "Batalkan percobaan bangun ulang".

| State | Role / row / marker / pin | Periksa, Amankan | Rebuild | Batalkan | Hapus | Resync / Lepas / BuatR | TLS | Retry of rebuild job |
|---|---|---|---|---|---|---|---|---|
| Plain quarantined (never rebuilt) | quarantined / none / X / quarantined | offer / accept | offer / accept (approval) | — | offer / accept (guards) | not a replica | refused, not offered | n/a |
| Rebuild running, before copy | replica / creating / X / quarantined | not offered (no box) / refused | — | not offered / busy | refused (replica) | refused (busy) | refused | busy |
| Running, after copy, before settle | same | same | — | same | same | same | same | busy |
| Running, after settle | replica / creating / **cleared** / replica | not offered | — | refused | refused | busy | busy | busy |
| Failed before the copy step | replica / failed / X / quarantined | attempt-pending sentence; refused (not quarantined). True | offer / accept | offer / accept | hidden (Batalkan first) | hidden / D-10 refused; BuatR target hidden (G-4) | refused | offer / accept |
| **Failed AT the copy step, before any drop** | same | **✗ "data lamanya sudah dihapus" (false)**; after Batalkan **✗ Periksa refused, data still there** (N-G1) | ok | ok | ok | ok | ok | ok |
| Failed after copy, before settle | replica / failed / X / quarantined | copy-started sentence (true) | ok | ok | hidden | refused | refused | ok |
| Failed after settle | replica / failed / cleared / replica | no box | not offered (400) | not offered / refused | needs Lepas (works) | offered / accepted (ordinary) | allowed | ok |
| Failed after settle, `ClearOldMaster` failed | replica / failed / **X** / replica | box says quarantined **✗** | ok (`required:false`) | **✗ accepted against a settled agent** (N-G2) | hidden | **✗ refused "masih dikarantina"** | **✗ refused "dikarantina"** | ok, but never re-clears **✗** |
| Panel stopped between the OK and the save | replica / failed / X / replica (agent) | as the failed-after-copy row; Batalkan accepted, conservative mismatch | ok | accepted (R-G2) | hidden | refused, conservative | refused | ok and heals |
| Attempt cancelled (Batalkan), copy not started | quarantined / none / X / quarantined | offer / accept | offer / accept | — | offer / accept | not a replica | refused | superseded |
| Attempt cancelled, copy started | quarantined / none / X / quarantined | hidden + true sentence; refused. **✗ status advice still "Tekan Periksa data tertinggal"; master_start_late cannot close** (N-G3) | ok | — | ok | — | refused | superseded |
| Job cancelled while running, before settle | quarantined (`keepQuarantined`) / none / X / quarantined | as the two rows above, by copy | ok | — | ok | — | refused | cancelled |
| Job cancelled during an in-flight configureReplication | quarantined / none / X / **replica** | copy started (true) | ok, heals | — | ok (agent not released) | — | refused (conservative) | cancelled |
| Job cancelled after settle | unassigned / none / cleared / unassigned (`releaseRole` sent) | no box | — | — | ok | ordinary unassigned | allowed | cancelled |

No state is left without a way forward (a rebuild) and a way out (Batalkan then Hapus, or Lepas then Hapus).

## Tests: would they fail without the fix?

- **G-1:** `TestRebuildFailureAfterSettleIsOrdinaryReplica`, `TestRetryConnectHealsQuarantineEnded` and `TestCancelAfterSettleIsOrdinaryCancel` assert the flag, the marker, the role and `releaseRole`. RED is recorded and plausible. The before-settle test passes on the old code by design.
  - Missing: `save` failure and `ClearOldMaster` failure (N-G2); a cancel during an in-flight `configureReplication`.
  - The Retry-heal test uses a configure failure *before* the agent's OK as a stand-in. It does not model an agent already pinned replica, which the fake cannot represent.
- **G-2:** store and orchestrator tests check the conditions, that no agent command is sent, superseded and delete. Before the fix they are compile failures, which is acceptable.
  - The web tests have no recorded RED (implementer's remark). Reading them, they would fail without the route (404, not 403/303), so they are meaningful.
  - Missing: an unauthenticated POST and a cross-origin POST to the new route (middleware, covered generically elsewhere); `ReturnToQuarantine` with flag true and marker kept.
- **G-3:** the web table covers the four states at steps 1 and 6.
  - Missing: the boundary `CurrentStep == index("Salin data")` with nothing dropped (it would document N-G1); unreadable params; a job without "Salin data" (older or odd shape); two rebuilds where only the earlier one reached the copy; a rebuild created before the promotion; the status-panel advice (N-G3).
- **G-4, G-5:** RED by stash and by assertion.
  - Missing for G-5: the fail-closed store-error path; tighten leaving an AwaitingRebuild replica out.
- **G-6:** the RED is real (`akun MySQL '…'@'…'`).
  - The fixed-point test misses adjacent literals (N-G4).
  - The no-leak check looks only for the substrings "Budi" and the NIK. It never puts a lead plus a pair inside a non-template value (N-G5).

## Residual risks (by design)

- **R-G1 (Minor).** A failed rebuild's state stays monitor-silent (old R-1, kept by the brief) until Batalkan or the next rebuild.
- **R-G2 (Minor).** When the agent settled but the panel never got the OK (panel stop, `callTimeout`, cancel during the call), the panel keeps calling X a quarantined old master. That is conservative: TLS is refused and the agent stays pinned replica after a Hapus. A rebuild or a Retry heals it, but "Batalkan" freezes the mismatch until the next rebuild.
- **R-G3 (Minor).** A second promote of T while X is in the failed state (old R-3) drops X out of AwaitingRebuild. Batalkan is then refused, and Resync, Lepas and TLS are offered against a quarantined agent. The agent's approval gate is the only barrier. Owner's decision.
- **R-G4 (Minor).** The phrase list and the agent's texts must change together (`redact.go:100-106` and `phrases_test.go`). A new quoted phrase in an agent text is silently censored until someone adds it.
- **R-G5 (Minor).** Unreadable job params count as "copy started", so on corruption Periksa is refused and nothing can override it.

## Out-of-scope observations

- `users.go:545` ("Jalankan di master: GRANT FLUSH_TABLES ON *.* TO %s …") is the same kind of account advice. Unless the account is a `dbpanel_…` one, it is still censored wherever it passes `Quotes`.
- Nested quotes in non-template MySQL messages already leaked the unquoted fragments of a value before this round (for example `bar` in `Unknown column 'foo 'x'@'y' bar'`).
- `RenewMySQLTLS` renews when `GetServer` itself errors (`tls.go:108`, `err == nil && …`). In `StartTightenTLS`, "fail closed" silently leaves a replica out on a store error instead of failing the job.
- `render.go:583` puts the orchestrator import in the standard-library group (gofmt does not complain; no effect).

## Verdict

- G-2, G-4 and G-5: ADDRESSED.
- G-1, G-3 and G-6: PARTIALLY ADDRESSED.
- New findings:
  - **Important (1):** N-G1, a "copy started" that over-approximates and refuses a needed salvage with a false sentence.
  - **Minor (5):** N-G2 (store failures inside the settle block), N-G3 (untrue monitor advice after the copy), N-G4 (fixed point broken), N-G5 (rule (b) leak through nested quotes), N-G6 (wall-clock "since the promotion").
