# Re-review of final fix wave part H: README and plan evidence (82b9df0..8b0389d)

Scope: README passages changed in 3226818, 5ec0d6c and 1ff5c98 (`final-fix-H-readme.diff`), and the plan's new evidence area "Bukti cadangan dan langkah tangan, tinjauan ulang F (bagian H)" (plan lines ~516-548). README line numbers are from `README.md` at 8b0389d. Code was read at 8b0389d (graphify first, then the located lines). Nothing was run.

## Finding Verdicts

| Item | Verdict | Evidence |
|---|---|---|
| **C-1** truncated backup passes the check | ADDRESSED | README 2079-2113: `; echo "keluar $?"` directly after the redirected mysqldump (no pipeline, so `$?` is mysqldump's status, or 1 if the redirection itself failed), size, `grep -c`, `tail -n 1` must start with `-- Dump completed on`; "Bila `mysqldump` menulis galat apa pun … berhenti". Plan rows: complete dump (exit 0, trailer); missing db (exit 2, grep 0); aborted dump `rusak2` (exit 2, 1630 bytes, **grep 1**, last line `DROP TABLE IF EXISTS `m`;`), which proves the old check let it through and the new one stops it. |
| **I-1** leftover `1 0` treated as the wrong server | ADDRESSED (Minor m-H2 in the `0 0` branch) | README 2054-2063 and 2184-2193: the role decides "wrong server" alone; `1 0` gets its own fix-up; `0 0` / no answer → restart the agent and check again. The restart sentence is true: `guardQuarantined` (quarantine.go:252-325) runs on the guard loop's first iteration (monitor.go:334-343, immediately, then every `GuardEvery` = 5 s, monitor.go:118). At agent start `guard.up` is false, so it reaches `persistReadOnly` whenever `h.Open` succeeds and no rebuild load is deferring it (quarantine.go:253-257, 266-272, 301-311). |
| **I-2** second run destroys the backup | ADDRESSED (Minor m-H3, m-H4) | README 2079-2093: `ls -l` must answer "No such file or directory"; an existing file is never a dump target; database gone → skip steps 4-5; still there → `mv` aside. README 2112-2113: copy off the server before the drop. Plan row "berkas cadangan sudah ada … `mv` … berkas lama utuh". No documented sequence truncates or overwrites the only verified backup (walk below). |
| m-1 missing-grants paragraph | ADDRESSED | README 2244-2254: every statement ends with `;`, the last too, with the reason (ERROR 1064, `super_read_only` stays off). "Heredoc" is replaced by a one-clause explanation (2204-2205). "baris GRANT" is replaced by "perintah `GRANT SHOW DATABASES …;` di tengah baris perintah itu". Plan rows: as the agent prints it, ERROR 1064, exit 1, `1 0`; corrected, exit 0, `1 1`. Mode ketat bullet 632-634 points to it. |
| m-2 check after a full rebuild | PARTIALLY ADDRESSED | README 1835-1841. The comparison reads a stored inventory that the rebuild does not refresh after a plain dump copy, and "hapus database itu" has no procedure on a server that is now a replica (m-H1). |
| m-3 `LIKE` wildcards | ADDRESSED in step 2 | README 2066-2070. The same query in step 4's existence check (2089-2090) carries no note (folded into m-H3). |
| m-4 abandoning the release early | ADDRESSED | README 2344-2345 (stop at step 2), 2366-2368 (stop at step 3: start the agent; step-5 check if MySQL is up). |
| m-5 connection check / events | ADDRESSED (Minor m-H7) | Events query 2333-2340 (plan row); `event_scheduler=OFF` with `super_read_only=ON` in step 4 (2369-2373; plan row: `@@event_scheduler` OFF, no daemon, no writes); repeated connection check at step 5 (2399-2400); both lines removed at step 6 (2425-2426) and on abandonment. The clause "kecuali `event_scheduler` pernah disimpan dengan `SET PERSIST`" is not measured. |
| m-6 "kurang hak" banner | ADDRESSED | README 2253-2254; banner text = server_detail.html:255; its statements already end in `;` (server_detail.html:256-257). |
| m-7 names with quotes/backticks | ADDRESSED | README 2043-2045. |
| m-8 Batalkan purpose | ADDRESSED (Minor m-H8 on the new refusal sentence) | README 1972-1976; refusal text = controls.go:148 `errNotFailedRebuild`, raised by `latestRebuildEnded` (controls.go:165-168, 185-201). |
| m-9 indented `EOF` | ADDRESSED | README 2208-2210. |
| m-10 `-u root -p` with the block | ADDRESSED | README 2210-2211; plan row (measured through `script`, `Enter password:` from the terminal, exit 0, `1 1`). |
| m-11 password in shell history | ADDRESSED (Minor m-H6) | README 1901-1918: interactive `mysql`, one statement per line. Plan row: mysql history omits the `IDENTIFIED BY` line, and the shell history holds nothing typed inside `mysql`. |

## Quoted texts

| README line | Quoted text | Source at 8b0389d | Match |
|---|---|---|---|
| 2001-2004 | "Bangun ulang sebelumnya berhenti di tahap Salin data. Bila penyalinan sempat mulai, catatan transaksi lama di server ini sudah dihapus, dan hasil "tidak ada data tertinggal" dari pemeriksaan ini belum tentu benar." | handlers_oldmaster.go:400-402 `msgCopyUncertain` (box: server_detail.html:387; form: handlers_oldmaster.go:1066-1067) | yes |
| 1974-1975 | "server ini bukan master lama dengan percobaan bangun ulang yang gagal" | controls.go:148 | yes |
| 1991-1995 | CopyStartedText and msgCopyStartedForm (unchanged) | oldmaster.go:123-124; handlers_oldmaster.go:390-391 | yes |
| 1996 | "Saran di kotak status juga menulis kalimat kotak itu" (described, not quoted) | handlers_oldmaster.go:408-419 `copyStatusAdvice` (role quarantined, StatusQuarantined, state STARTED only) | consistent |
| 1980-1983 | msgAttemptPending (unchanged) | handlers_oldmaster.go:392-393 | yes |
| 2085 | "No such file or directory" | `ls` message | yes |
| 2109-2110 | "Unknown database", "Couldn't execute" | plan rows (`Got error: 1049: Unknown database …`, `Couldn't execute 'SHOW FIELDS FROM …'`) | yes (fragments) |
| 2108 | `-- Dump completed on` | plan rows (`baik`, `rusak`) | yes |
| 2250 | "ERROR 1064" | plan row (missing-grants, as printed) | yes |
| 2253-2254 | "Akun admin MySQL ini kurang hak" | server_detail.html:255 | yes (prefix) |
| 1836, 1892 | box **MySQL** | server_detail.html:67 | yes |
| 2001 | **Salin data** | create_replica.go:71 | yes |

## Commands

| README line | Command | Correct (bash / MySQL 8.0) | Names allowed by the section (`$`, space; not `'` `"` backtick) | Does what the sentence says | Evidence row |
|---|---|---|---|---|---|
| 2083 | `ls -l '/root/<nama>-cadangan.sql'` (existence) | yes | yes | yes | none needed |
| 2090 | `mysql -e 'SHOW DATABASES LIKE "<nama>";'` on the old master | yes | `_`/`%` wildcards (m-H3) | mostly | plan 488 (F) |
| 2093 | `mv '/root/<nama>-cadangan.sql' '/root/<nama>-cadangan-lama.sql'` | yes; silently replaces an existing `-cadangan-lama.sql` (m-H3) | yes | yes | plan row "berkas … sudah ada … mv" |
| 2096 | `mysqldump … --databases '<nama>' > '/root/<nama>-cadangan.sql'; echo "keluar $?"` | yes. `$?` is mysqldump's exit status. On an error inside a table/view the dump exits before writing the footer (measured: no trailer, exit 2). | yes | yes | plan rows `baik`, `tidakada`, `rusak2`, `rusak` |
| 2097-2099 | `ls -l`, `grep -c 'CREATE DATABASE'`, `tail -n 1` | yes | yes | yes | same rows |
| 2120 (unchanged) | drop `mysql -e '… DROP DATABASE `<nama>` …'` | unchanged | — | — | F rows |
| 2056-2060, 2186-2190 | `mysql -e 'SET GLOBAL super_read_only=ON;'`; `systemctl restart dbpanel-agent` | yes | — | the restart re-asserts read-only within about 5 s **only if** MySQL is up, the stored account logs in with the rights, and no load is running (m-H2) | restart: code-read (quarantine.go:252-311), not measured; unit name as scripts/install.sh |
| 2200-2202 | the GRANT block, with the `-u root -p` variant | yes | yes | yes | plan 384, new `script` row |
| 1909-1915 | interactive `mysql` with SET/SET/CREATE USER/GRANT/SET | yes; interactive mysql does **not** stop at an error (m-H6) | password must not contain `'` | yes on the happy path | plan row (`MYSQL_HISTFILE`) |
| 2244-2254 | missing-grants block, every statement ending in `;` | yes | yes | yes | two plan rows (as printed / corrected) |
| 2335 | `mysql -e 'SELECT EVENT_SCHEMA, EVENT_NAME FROM information_schema.EVENTS WHERE STATUS="ENABLED";'` | yes (no ANSI_QUOTES) | — | yes | plan row |
| 2369-2373 | option line `event_scheduler=OFF` | yes | — | yes. The "unless persisted" clause is unmeasured (m-H7). | plan row |

C-1 failure modes, checked one by one:

| Failure mode | Passes all four? | Why |
|---|---|---|
| Exit status after a pipeline or redirection | No | No pipeline; a failed redirection makes `$?` = 1. |
| A table/view error part-way (1356, 1449, corrupt table) | No | Exit 2, no trailer (measured). |
| A full disk at the last write | No (cannot verify without a measurement) | mysqldump checks I/O after writing and exits non-zero. The exit-status line still catches it even when the trailer reached the file. |
| A warning-only run (a view with a missing definer on 8.0.46) | Yes, and correctly | The object is dumped (measured `rusak`: exit 0, trailer). |
| `--single-transaction` with MyISAM tables | Yes | Non-transactional tables are not snapshot-consistent. On the quarantined old master (`read_only` 1) only root/SUPER sessions can write, so the risk is negligible. |
| Skipped objects | No case found | `--databases` with routines, triggers and events dumps all the database's objects. Accounts are outside the database, as the section intends. |

## State walks

### "Database yang tidak ada di master baru" (README 2025-2142)

| Case | Walk | End | Next step given? |
|---|---|---|---|
| Happy path (pin quarantined, 1 1) | 1 ok → 2 empty on new master → 3 apps stopped → 4 no file, dump `keluar 0`, size, grep 1, trailer → copy off → 5 drop → 6 `1 1` | DB gone, verified backup in /root and elsewhere | yes |
| New master | 1: pin `master` → stop | unchanged | yes |
| Ordinary replica | 1: pin `replica` → stop | unchanged | yes |
| No agent | 1: `cat` fails → stop | unchanged | yes |
| Return after an interrupted run (`1 0`) | 1: pin ok, `1 0` → SET ON → `1 1` → continue | 1 1 | yes |
| `0 0`, MySQL up, agent and account healthy | 1: restart agent → guard persists ON within ~5 s → `1 1` | 1 1 | yes |
| `0 0` while a new rebuild's load is running | 1: restart agent → the agent's load job dies with the agent, the guard re-asserts | rebuild fails; old master 1 1 | partly (m-H2) |
| MySQL down | 1: "tidak menjawab" → restart agent → still no answer → "periksa lagi" | unchanged; loop | **no** (m-H2) |
| Stored account cannot log in / lacks rights, `0 0` | restart → guard's `h.Open` fails or `persistReadOnly` errors | stays 0 0 | **no** (m-H2) |
| Aborted dump | 4: `keluar 2`, trailer missing → stop | nothing dropped; partial file stays at the path | yes. A re-run then hits the existing-file branch → DB still there → `mv` the partial file aside → new dump: fine. |
| Typo in the name | 2: empty; 4: "Unknown database", `keluar 2` → stop | nothing dropped | yes |
| Second run after a completed run | 4: file exists → `SHOW DATABASES LIKE` empty → skip 4-5 → 6 | backup intact | yes |
| Second run, DB still there (drop had failed) | 4: file exists → DB there → `mv` to `-lama` → new dump | both kept | yes |
| Third run, DB still there | 4: `mv` replaces the earlier `-lama` file | DB still exists, so no data loss | (m-H3) |
| Similar name matches in the step-4 existence query | operator thinks the DB is still there → `mv` aside → dump "Unknown database" → stop | old backup kept under `-lama` | confusing, safe |
| App still connected | 3 stops it; 5 waits/Ctrl-C → 6 fixes `1 0` | 1 1 | yes |

### "Memberi hak SHOW DATABASES …" and variants (README 2168-2254, 1901-1918)

| Case | End | Next step given? |
|---|---|---|
| Happy path | 1 1, grant present, Ulangi | yes |
| Wrong server (master / replica / no agent) | stop at step 1 | yes |
| Return with `1 0` | fixed at step 1 | yes |
| `0 0` / MySQL down | as in the drop walk (m-H2) | partly |
| Typo in the account | ERROR 1410 → step 3 `1 0` → SET ON → retry | yes |
| Run twice | idempotent | yes |
| Indented `EOF` | `>` prompt → Ctrl-C → retype | yes |
| Root with password | `-u root -p` asks on the terminal (measured) | yes |
| Missing-grants variant as printed | ERROR 1064 → `1 0` → fixed. The paragraph now says to add the final `;`. | yes |
| New-account variant, name already exists | interactive mysql prints ERROR 1396 and **continues**: GRANT ALL … WITH GRANT OPTION goes to the existing account, then SRO ON → `1 1` | **no** (m-H6) |

### Failed-rebuild block with the three copy states (README 1964-2010)

| Copy state (oldmaster.go:151-205) | Failed state (role replica, row failed) | Plain quarantined (after Batalkan) | README |
|---|---|---|---|
| NOT STARTED (nothing sent, or a direct refusal) | box/form: attempt-pending; no Periksa | Periksa/Amankan offered and accepted; no warning | 2005-2010. The words "tidak mengubah apa pun" are false for a dump with an approved wipe list (I-H1). |
| UNCERTAIN (`CopySent` with no evidence; old shape at the step with a job id/GTID; unreadable params) | attempt-pending | offered, `msgCopyUncertain` banner above them (server_detail.html:387) and in the form (handlers_oldmaster.go:1066-1067); status advice unchanged | 1998-2004 true |
| STARTED (past the step, `CopyBegan` from progress or result) | `CopyStartedText` in the box; Periksa hidden; form `msgCopyStartedForm` | hidden and refused (`refuseAfterCopy`); status advice replaced | 1984-1997 true. The CLONE "tepat sebelum" wording matches the choice (residual R-H2). |

## False, unverifiable or dangerous statements in the changed text

### Critical

None.

### Important

**I-H1. "tidak mengubah apa pun" is false: on a dump rebuild, the old master's own databases are dropped before the copy command is sent.** README 2005-2010.
- Evidence: in `replicaSnapshot`, `mysql.dropDatabases` runs with the approved `WipeList` (create_replica.go:753-760) before `copySnapshot` sets `CopySent`. The `WipeList` is every database the old master holds that the new master also has (create_replica.go:420-423, 500-522). On an old master that means its main production databases, not "database sisa percobaan sebelumnya". `RebuildCopyState` then says NOT STARTED: correctly, as far as the binary log goes, since the drop is not binlogged (H-1d table).
- What the operator experiences: the README (and Batalkan's own sentence "data di server ini tidak diubah") tells them the old master is untouched. They cancel the attempt believing the old master still holds its databases. That is wrong if they later bring it back with `set-role master` as a fallback, or rely on it as a second copy. The leftover transactions can still be salvaged, but the base data of those databases is gone from this server.
- How bad: a false statement about where data still exists, on a path the operator chooses deliberately (cancel and fall back). Nothing destructive follows from the text alone, so Important, not Critical.
- Corrected (ready to paste):

  > - Bangun ulang yang berhenti di tahap **Salin data** sebelum perintah salinan dikirim, atau yang perintah salinannya ditolak agent, belum mengosongkan binary log server itu: **Periksa** dan **Amankan** tetap seperti biasa. Tetapi pada dump, database server ini yang juga ada di master baru (daftar yang Anda setujui di form) bisa sudah dihapus sebelum perintah salinan. Isi database itu tidak ada lagi di server ini, walau penghapusan itu tidak dicatat di binary log, sehingga transaksi tertinggal tetap bisa diperiksa dan diamankan. Jangan mengandalkan server ini sebagai salinan cadangan database itu.

### Minor

**m-H1. The after-rebuild comparison reads a list that may be stale, and the clean-up has no procedure.** README 1835-1841.
- Evidence: the panel refreshes the target's inventory before the copy (create_replica.go:594, :636) and only after a reconnect (create_replica.go:883). After a plain dump copy the server page still shows the pre-load list. "cadangkan lalu hapus database itu" on a server whose pin is now `replica` is covered by no README procedure: the drop section requires pin `quarantined` at its step 1.
- What the operator experiences: they compare two lists, one of them stale, and may see nothing. If they do find something, they have no next step.
- Corrected: "Sesudah bangun ulang selesai, klik **Segarkan** di kotak **MySQL** di halaman server ini dan di halaman master baru, lalu bandingkan kedua daftar database. Database yang hanya ada di server ini dibuat selama pemuatan; datanya tidak ada di master baru. Jangan promote server ini dan jangan alihkan aplikasi ke sana; minta bantuan admin MySQL untuk memindahkan datanya ke master baru."

**m-H2. The `0 0` / no-answer branch can loop with no next step.** README 2060-2063 and 2190-2193.
- Evidence: the guard re-asserts read-only only when `h.Open` succeeds with the stored account and that account has the rights to persist (quarantine.go:253-257, 307-309). A restart does not start MySQL. During a running rebuild load, `0 0` is the expected state (dump.go:460-466), and a restart kills the agent's load job.
- What the operator experiences: with MySQL down, or a broken stored account, "periksa lagi" repeats forever. A `0 0` seen during a running rebuild leads them to kill it. The second is mild: the rebuild fails and the guard sets read-only.
- Corrected: "Bila `0 0`, lihat dulu di panel apakah bangun ulang server ini sedang berjalan; bila ya, tunggu sampai selesai atau gagal. Bila tidak, jalankan `systemctl restart dbpanel-agent`, tunggu 10 detik, lalu periksa lagi. Bila MySQL tidak menjawab, nyalakan dulu agent lalu MySQL (`systemctl start dbpanel-agent`, lalu `systemctl start mysql` atau `mysqld`), tunggu 10 detik, lalu periksa lagi. Bila sesudah itu masih bukan `1 1`, berhenti dan minta bantuan: jangan lanjut ke langkah berikutnya."

**m-H3. Step 4's existence query has no wildcard note, and `mv` overwrites an earlier `-lama` file.** README 2089-2093.
- What the operator experiences: a similar name can make them think the database is still there. The result is only a confusing extra `mv` and a failed dump, never a loss, because the database itself still holds the data whenever a `-lama` file is replaced.
- Corrected: "… (`mysql -e 'SHOW DATABASES LIKE "<nama>";'`; hanya nama yang persis sama yang dihitung) … pindahkan dulu berkas lama ke nama yang belum dipakai, misalnya `mv -n '/root/<nama>-cadangan.sql' '/root/<nama>-cadangan-'"$(date +%Y%m%d%H%M%S)"'.sql'`." This needs an evidence row.

**m-H4. "Salin berkas itu ke server lain atau media lain" gives no command and no check.** README 2112-2113.
- What the operator experiences: an operator with limited skills may skip this, or copy a partial file. That is no loss on its own, because the `/root` copy stays, but the safeguard is unverified.
- Add: "Misalnya `scp '/root/<nama>-cadangan.sql' <pengguna>@<server lain>:`; lalu bandingkan `sha256sum` berkas itu di kedua server: harus sama."

**m-H5. "dump: sesudah binary log server itu dikosongkan" holds only in GTID ON mode.** README 1984-1985.
- Evidence: `resetBinaryLogs` runs only `if gtidMode == "ON"` (dump.go:142-146). Progress is emitted after the load setup either way.
- What the operator experiences: in other modes, Periksa is refused while the binary log still exists. That matches the panel's rule; only the wording is imprecise.
- Corrected: "(dump: sesudah data lama mulai diganti; pada GTID ON binary log-nya juga dikosongkan; CLONE: tepat sebelum CLONE dimulai)".

**m-H6. The interactive new-account variant does not stop at an error.** README 1901-1918.
- Evidence: an interactive `mysql` session prints an error and continues with the next statement. If `'panel_admin'@'localhost'` already exists (README 288-290 recommends exactly that name for the panel account), CREATE USER fails with ERROR 1396, and `GRANT ALL … WITH GRANT OPTION` then goes to the existing account. Its password is unchanged, so "Hubungkan" with the new password fails.
- What the operator experiences: a confusing login failure, and full rights given to a pre-existing account they may not control.
- Add: "Bila `CREATE USER` menjawab ERROR 1396 (akun sudah ada), jangan ketik perintah `GRANT`; ketik `SET GLOBAL super_read_only=ON;`, `exit`, lalu pakai nama akun lain."

**m-H7. The `event_scheduler` persisted-value clause is unmeasured.** README 2372-2373.
- Cannot verify without a measurement. It is consistent with the measured read_only rule (b), but no row covers it.

**m-H8. The Batalkan refusal sentence overstates.** README 1973-1976.
- Evidence: the refusal fires only when the panel recorded `QuarantineEnded` on the newest rebuild job (controls.go:185-201). When the configure answer was lost after the agent settled, the agent ended the quarantine and Batalkan is still accepted (residual R-4).
- What the operator experiences: rarely, a cancel is accepted on a server that is no longer quarantined.
- Corrected: "… bila panel sudah mencatat bahwa agent mengakhiri karantina pada bangun ulang terakhir."

## Residual risks (code behaviour the README cannot fix)

- **R-H1 (Important, carried as R-1).** The guard does not re-assert `super_read_only` while MySQL stays up. Every by-hand path still depends on the operator reading the `1 1` check.
- **R-H2 (Minor).** A CLONE that emitted "CLONE dimulai" and then failed before changing anything counts as STARTED, so Periksa/Amankan are refused. That was the writer's choice. A copy job that ended early without progress stays UNCERTAIN (offered, with a warning), which is the safe direction.
- **R-H3 (Minor).** In state STARTED, the monitor's "master lama sempat bisa ditulis saat menyala; jalankan Periksa data tertinggal" (monitor/alerts.go:385) and README 1546-1549 ("tertutup setelah **Periksa data tertinggal**") are untrue: the alert cannot close through a check. The monitor was out of scope.
- **R-H4 (Minor, carried as R-4).** A lost configure answer after the settle leaves the panel showing an old master. Batalkan is accepted.
- **R-H5 (Important, carried as R-3).** Databases and rows written during a full dump load are undetected. The README states this and now gives a check, which is weakened by m-H1.
- **R-H6 (Important, carried as R-7).** The proof/listing race from the E re-review.

## Out-of-scope observations

- The drop and GRANT sections still cannot tell a second quarantined old master (another topology) from the intended one: the role check passes on both. This is unchanged and rare.
- `systemctl restart dbpanel-agent` was not run by the writer (no unit on the build host). The unit name matches scripts/install.sh and the other README commands.

## Merge judgment

No finding blocks a merge. C-1 and I-2, the data-loss paths, are closed by measured checks, and I-1 is closed. The one new Important item, I-H1, is a false reassurance about data still on a cancelled old master. It destroys nothing by itself, but it should be corrected in the first follow-up, together with m-H1 and m-H2, which leave operators without a next step.
