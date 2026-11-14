# Re-review of final fix wave part F: README and plan evidence (abb56bc..82b9df0)

Scope: the README passages changed in abb56bc..82b9df0 (`final-fix-F-readme.diff`) and the plan's new evidence tables ("Bukti perintah tangan di server yang dikarantina (tinjauan ulang D, bagian F)", plan lines 447-490). All README line numbers are from `README.md` at 82b9df0. Code was read at 82b9df0; nothing was run (no mysqld, no tests, no make). graphify was queried first (it located rebuild_load.go / connect.go / redact.go); everything else was read at lines the graph or an earlier read pointed to.

## Finding Verdicts

| Item | Verdict | Evidence |
|---|---|---|
| **F-1** (I-B) section "Memberi hak SHOW DATABASES …" | ADDRESSED (new Important I-1 and Minor m-1 in it) | Heading README 2096, once. Agent text quoted 2098-2104 = rebuild_load.go:207-211. Role + `1 1` check 2112-2120. Heredoc 2125-2129 = E report (ii) / plan row (ii) line 384. Error branch and check 2136-2154. Ulangi + missing-copy text 2156-2161. "Persetujuan" passage now quotes the new text and points to the section 1873-1884; unsupported-name text 1886-1900 = rebuild_load.go:201-204. Panel offers the connect form whatever the role (server_detail.html:84 `ShowMySQL`, handlers_servers.go:717; "Hubungkan ulang" 288, "Hubungkan MySQL" 306/327); the handler has no role check (handlers_mysql.go:178-216); the agent allows `mysql.connect` in strict mode (strict.go:46) and refuses a short-granted account on quarantined (connect.go:146-153). |
| **F-2** (N-1) firewall is not a cut-off | ADDRESSED (Minor m-5) | README 2232-2252: stop or reconfigure+restart apps; firewall "hanya tambahan, bukan pengganti" with both reasons; processlist query with expected rows (plan rows 489-490). |
| **F-3** (N-2) right-server check and stop | ADDRESSED as briefed, but the briefed checks are insufficient: new Critical C-1 and Important I-1, I-2 | README 2008-2018 (role file + `1 1`; path = config.go:17 `DefaultDir` + pins.go:44 `RoleFile`, content `quarantined\n` pins.go:541/1000); explicit stop 2045-2048; error branch "hanya di server yang lolos pemeriksaan langkah 1" 2074-2075. |
| **F-4 m-A** one way to be root | ADDRESSED | `sudo -i` and no `sudo` in all three sections: 2001-2006, 2108-2110, 2216-2218; step 5 of release now `mysql` (2302); step 8 `echo master > /etc/dbpanel/role` (2335) was already sudo-less. |
| **F-4 m-B** added line on abandonment | ADDRESSED (Minor m-4 for stops before step 4 / at step 3) | README 2295-2300; stop when no `init_file=` 2272-2273. |
| **F-4 m-C** DROP that waits | ADDRESSED | Step 3 2030-2032; Ctrl-C note 2065-2067; step 6 check. |
| **F-4 m-D** database not on new master | ADDRESSED (Minor m-3: `LIKE` wildcards) | Step 2 2019-2029; "jangan mengandalkan salinan di tempat lain" 1998-1999. |
| **F-4 m-E** dump accounts | ADDRESSED | README 644-646 true: strict.go:85 `strictProtected`; pins.go:903-908 refuses only for `roleMaster`. |
| **F-4 m-F** A6 causes | ADDRESSED | README 1841-1843, 1994-1996; matches rebuild_load.go:172-189 `notCopied`. |
| **F-4 m-G** `local_approval: false` | ADDRESSED | README 1815-1817. |
| **F-4 m-H** first rebuild preflight | ADDRESSED | README 1990-1992; preflight.go:544. |
| **F-4 m-I** evidence row | ADDRESSED | Plan line 458 (drop with `sql_log_bin=OFF`, success) and 459 (failure), plus processlist rows 489-490. |
| **F-5** README follows part G | ADDRESSED (residual R-2, R-4, R-5) | "Setelah bangun ulang gagal" 1925-1945; Batalkan paragraph 1947-1955 (msgCancelAttempt handlers_oldmaster.go:627-629; password confirm via `requireConfirm` + _confirm.html; no `Hub.Call` in controls.go:156-173); data tertinggal 1957-1973 (oldmaster.go:123-124, handlers_oldmaster.go:390-393, `RebuildCopyStarted` oldmaster.go:131-165, `stepCopyName` create_replica.go:71); record ends at G-1 1854-1862 (create_replica.go:952-959; cancel after it runner.go:1176-1194). |
| **F-6** job page shows the text whole | ADDRESSED | README 1879-1880. redact.go:103-105 keeps both phrases; `advisedAccount` (redact.go:122-150) keeps `'user'@'host'` after "akun MySQL " / "GRANT SHOW DATABASES ON *.* TO " when the user is `[A-Za-z0-9_.$-]+` and the host passes `host` (redact.go:280-293: accepts `localhost`, `%`, `10.0.0.%`, IPs). Every account the agent prints there passed `proofAccount` (rebuild_load.go:241-245), so it is kept. The step text is capped at 1 KiB (runner.go:101, 1558); the listing text is ~450 bytes plus two accounts, so it fits. |
| **F-7** `--force` | ADDRESSED | Plan rows 458-463: `--force` fixes the heredoc, not `-e`; both commands stay without it; the last numbered step of both sections is the `1 1` check (2068-2081, 2140-2154). |
| **E "README needs" 1** | ADDRESSED | README 1873-1884. |
| **E "README needs" 2** | ADDRESSED | README 1886-1900; "Hanya hak yang diberikan langsung … `mandatory_roles`" 1867-1869 (rebuild_load.go:256-266 reads `SHOW GRANTS FOR` the account by name). |
| **E "README needs" 3** | ADDRESSED | README 2096-2161. |
| **E "README needs" 4** | ADDRESSED | README 609-615 = pins.go:192-195 `unsourcedQuarantineText`, reached for `mysql.leftover`/`mysql.salvage` at pins.go:806-809 (they pass `strictGate` as `strictAllowed`, strict.go:51-52, 134-136); list/download/delete are role-only (pins.go:875-879). |
| **Later instruction (i)** quoted heredoc and `$` | ADDRESSED | README 2126 `<<'EOF'`, reason 2131-2133; drop command checked: name in single quotes in steps 4-5 and the reason 2041-2043 (commit 82b9df0; plan rows 485-486). |
| **Later instruction (ii)-a** missing-grants paragraph | PARTIALLY ADDRESSED | README 2163-2171 and 631-634 exist and are right in substance, but the agent's list has no `;` after its last statement (connect.go:152 joins with `"; "`), and the README does not say to add one (m-1). |
| **Later instruction (ii)-b** when a rebuild ends the quarantine | ADDRESSED (Minor m-2: no after-check) | README 1825-1834 is exact (see D below); 2697-2700 security bullet consistent. |

## Quoted texts

| README line | Quoted text (abridged where long) | Source at 82b9df0 | Match |
|---|---|---|---|
| 610-614 | "server ini dipin sebagai quarantined tanpa sumber yang dipin; perintah mysql.leftover ditolak. Pin master barunya dulu di server ini: sudo dbpanel agent set-source <host:port>" | pins.go:193-194 | yes |
| 629-631, 2164-2167 | "server ini dikarantina (quarantined): akun <akun> belum punya semua hak yang dibutuhkan; jalankan perintah GRANT berikut sebagai root lalu periksa ulang: <perintah GRANT, dipisah titik koma>" | connect.go:150-152 | yes |
| 1836-1840 | A6 "server ini masih punya database yang tidak disalin dari sumber … bagian "Database yang tidak ada di master baru"." | rebuild_load.go:163-165 | yes |
| 1849-1851 | "sumber replikasi sudah diatur, tetapi daftar database di server ini tidak bisa dibaca; karantina tidak diakhiri: <sebab>" | quarantine.go:470-471 | yes |
| 1857 | "Karantina master lama berakhir: agent kini menyimpan peran replika." | create_replica.go:958 | yes |
| 1873-1879, 2098-2104 | listing refusal "daftar database … akun MySQL <akun> … GRANT SHOW DATABASES ON *.* TO <akun>; Server ini baca-saja, … bagian "Memberi hak SHOW DATABASES di server yang dikarantina". Sesudah itu tekan "Ulangi dari tahap ini" pada pekerjaan di panel." | rebuild_load.go:207-211 | yes |
| 1886-1891 | unsupported-name text "… (misalnya dbpanel_admin). Sesudah itu tekan "Ulangi dari tahap ini" pada pekerjaan di panel." | rebuild_load.go:201-204 | yes |
| 1904-1908, 2159-2160 | errNoLoadRecord | quarantine.go:523-526 | yes |
| 1929-1930, 1947 | button "Batalkan percobaan bangun ulang" | _ui.html:74 | yes |
| 1931-1932 | "Server ini master lama yang masih dikarantina. Pakai Bangun ulang sebagai replika." | replica_detail.html:89 (link text) | yes |
| 1934-1935 | "server ini master lama yang masih dikarantina; pakai Bangun ulang sebagai replika" | approval.go:107 `ErrAwaitingRebuild` | yes |
| 1937-1938 | "Bila "Ulangi dari tahap ini" tidak menolong, mulai lagi Bangun ulang sebagai replika." | job_detail.html:89 | yes |
| 1949-1953 | msgCancelAttempt | handlers_oldmaster.go:627-629 | yes |
| 1960-1962 | msgAttemptPending | handlers_oldmaster.go:392-393 | yes |
| 1967-1970 | CopyStartedText | oldmaster.go:123-124 | yes |
| 1970-1973 | msgCopyStartedForm | handlers_oldmaster.go:390-391 | yes |
| 1963 | step **Salin data** | create_replica.go:71 | yes |
| 1991-1992 | "server ini masih berisi database lain (<nama database>); versi ini hanya mendukung server replika khusus" | preflight.go:544 | yes |
| 1892 | box **MySQL**, **Hubungkan ulang**, **Hubungkan MySQL** | server_detail.html:67, 288, 306/327 | yes |
| 2046, 2064, 2137 | "Unknown database", "Can't drop database", "ERROR 1410 … You are not allowed to create a user with GRANT" | MySQL messages, plan rows 459, 462, 483 | yes |
| 2106 | ERROR 1290 | plan row 469 / 383 | yes |
| 2222-2223 | "master lama belum terjamin terkunci saat MySQL menyala; jangan nyalakan MySQL-nya sebelum agent tersambung" | monitor/alerts.go:384 | yes (unchanged) |
| Headings | "#### Database yang tidak ada di master baru" (1983) and "#### Memberi hak SHOW DATABASES di server yang dikarantina" (2096) | named by rebuild_load.go:165 and :209 | each exists exactly once with exactly those words (`grep -c '^#### …$'` = 1) |

## Commands

| README line | Command | Bash / MySQL 8.0 | Special names (`$` ` ' " space) | Does what the text says | Evidence row | Guard before destructive part |
|---|---|---|---|---|---|---|
| 2011, 2115 | `cat /etc/dbpanel/role` | ok | — | yes (pins.go:44, :1000 writes `<role>\n`) | none (code-verified) | it is the guard |
| 2012, 2071, 2116, 2143 | `mysql -e 'SELECT @@read_only, @@super_read_only;'` | ok | — | yes | plan 487 | — |
| 2024 | `mysql -e 'SHOW DATABASES LIKE "<nama>";'` (new master) | ok without ANSI_QUOTES | `$` safe; `"` / `'` / `\` break it; `_` and `%` are wildcards (m-3) | mostly (false matches possible) | plan 488 | read-only |
| 2036 | `mysqldump … --databases '<nama>' > '/root/<nama>-cadangan.sql'` | ok | `$`, space safe; `'` merges words so `>` becomes part of the database argument (m-7) | creates a backup, but **truncates an existing backup before mysqldump runs** (I-2), and the check that follows passes on a truncated dump (C-1) | plan 476, 483-485 | overwriting redirection is NOT guarded |
| 2037-2038 | `ls -l …`, `grep -c 'CREATE DATABASE' …` | ok | as above | insufficient as a go/no-go (C-1) | plan 483-485 | — |
| 2052 | `mysql -e 'SET GLOBAL super_read_only=OFF; SET SESSION sql_log_bin=OFF; DROP DATABASE `<nama>`; SET GLOBAL super_read_only=ON;'` | ok (backticks literal inside single quotes) | `$` safe (plan 486); backtick breaks SQL → error → `1 0` → step 6; `'` → unbalanced quote, shell waits for more input (not "rejected") | yes | plan 458-459, 484, 486 | guarded by steps 1, 2, 4 — but step 4's criteria are too weak (C-1) |
| 2078, 2150 | `mysql -e 'SET GLOBAL super_read_only=ON;'` | ok | — | yes | plan 380s/459 narrative | role check of step 1 named |
| 2126-2128 | `mysql <<'EOF' … GRANT SHOW DATABASES ON *.* TO '<user>'@'<host>'; … EOF` | ok; `EOF` must start in column 1 (m-9) | quoted heredoc: no expansion, `$` safe (plan 465-466) | yes | plan 384 (success), 462 (failure), 463-465 (`--force`) | step 1 |
| 2104-2110 | `-u root -p` added to the heredoc form | `-p` reads the password from the terminal, so it should work with stdin redirected | — | — | **cannot verify without a measurement** (no row) | — |
| 1898-1899 | heredoc with `CREATE USER 'panel_admin'@'localhost' IDENTIFIED BY '<sandi>'; GRANT ALL …` | ok | the password lands in `/root/.bash_history` (m-11) | yes | plan 467 | step 1 |
| 2163-2171 | heredoc with the GRANTs of the missing-grants text | **fails if pasted as shown by the agent**: the last statement has no `;` (connect.go:152), so it runs into `SET GLOBAL super_read_only=ON` → ERROR 1064 → `1 0` (m-1) | — | only when the operator adds the `;` | plan 466 (measured *with* the `;`) | step 1 |
| 2243 | `mysql -e 'SELECT ID, USER, HOST, DB, COMMAND, TIME FROM information_schema.PROCESSLIST WHERE ID <> CONNECTION_ID();'` | ok (I_S.PROCESSLIST deprecated in 8.0, still works) | — | yes, at one point in time (m-5) | plan 489-490 | — |
| 2288-2290, 2316-2317, 2335-2336, 2352-2353 | unchanged release commands | ok | — | yes | plan "Bukti baca-saja saat MySQL dinyalakan tanpa agent" | unchanged |

## State walks

Notation: RO/SRO = `@@read_only`/`@@super_read_only`; P = persisted values; pin = /etc/dbpanel/role.

### "Memberi hak SHOW DATABASES di server yang dikarantina" (README 2096-2161)

| Case | After step 1 | After step 2 | After step 3 | End state | README tells next step? |
|---|---|---|---|---|---|
| Right server, job failed with the listing text (pin quarantined, 1 1, P ON ON) | passes | heredoc ok: 1 1, grant added, gtid unchanged (plan 384) | `1 1` | Ulangi → new connection sees the grant (E report) | yes |
| Wrong server: new master (pin master, 0 0) | stop | — | — | unchanged | yes ("bukan server yang dimaksud") |
| Wrong server: ordinary replica (pin replica, 1 1) | stop | — | — | unchanged | yes |
| Wrong server: no agent (no pin file) | `cat` error → stop | — | — | unchanged | yes |
| Typo in account | passes | ERROR 1410, client stops: **1 0**, P ON ON, gtid unchanged | shows `1 0` → `SET GLOBAL super_read_only=ON` → 1 1; fix name, step 2 again | 1 1 | yes |
| Typo that names another existing account | passes | GRANT goes to that account (SHOW DATABASES only) | 1 1 | wrong account holds SHOW DATABASES; Ulangi fails again with the same text | partly: "persis seperti yang tertulis" |
| Stops after a failed step 2 | — | 1 0 | not run | **SRO OFF until a MySQL restart or someone sets it; root-connected apps and any SUPER/CONNECTION_ADMIN session can write to the old master** | when the operator comes back and starts at step 1, step 1 reports `1 0` as "bukan server yang dimaksud" → stop, still 1 0 (I-1) |
| Running it twice (success both times) | passes | GRANT is idempotent | 1 1 | unchanged | yes |
| MySQL down | `mysql` cannot connect → "salah satunya lain" → stop | — | — | unchanged | stop only; no next step (MySQL down on a quarantined server: agent's job, acceptable) |
| Agent down | passes | ok | 1 1 | Ulangi: agent offline, job fails again; once the agent starts, the load record is gone if it restarted → missing-copy text → new rebuild (2158-2161) | yes |
| MySQL root needs a password, `-p` forgotten | access denied → stop | — | — | unchanged | partly (2109-2110 says add `-u root -p`) |
| Raw-README copy with indented `EOF` | — | shell waits at `>` (heredoc never ends); Ctrl-C → nothing ran | — | unchanged | no (m-9) |
| Create-new-account variant (1896-1900) | passes | CREATE USER + GRANT ALL: 1 1 (plan 467); if the name already exists: ERROR 1396 → 1 0 | 1 0 → fix | Hubungkan ulang with it → saved (has every grant) → Ulangi | yes; password stays in shell history (m-11) |
| Missing-grants variant (2163-2171), statements pasted as the agent prints them | passes | all but the last GRANT run, last fails ERROR 1064 (no `;`) → **1 0**, partial grants | `1 0` → fixed | operator must find the missing `;` alone | no (m-1) |

### "Database yang tidak ada di master baru" (README 1983-2094)

| Case | Step 1 | 2 | 3 | 4 | 5 | 6 | End |
|---|---|---|---|---|---|---|---|
| Right server, A6 state (pin quarantined, 1 1, P ON ON) | pass | empty on new master | apps stopped | dump ok, size>0, grep 1 | drop ok, 1 1, no binlog event | 1 1 | Ulangi → `notCopied` empty → quarantine ends (quarantine.go:459-481) |
| New master | stop | — | — | — | — | — | unchanged |
| Ordinary replica / no agent | stop | — | — | — | — | — | unchanged |
| Name present on new master (A6 window) | pass | name shown → stop | — | — | — | — | unchanged; new rebuild drops it |
| Typo in name | pass | empty | — | "Unknown database", header-only file, grep 0 → stop | — | — | unchanged (a stray small file in /root) |
| **mysqldump aborts mid-way** (e.g. view with a missing definer, error 1449; disk full) | pass | pass | — | file > 0 bytes, `CREATE DATABASE` already written → grep 1 → "Lanjut" | **drop ok** | 1 1 | **database gone, backup incomplete, data exists nowhere else** (C-1) |
| Drop fails (FK, name with backtick) | pass | pass | pass | ok | error → **1 0** | `1 0` → SET ON → 1 1 | ok |
| Drop waits on a metadata lock, Ctrl-C | pass | pass | (apps not all stopped) | ok | interrupted → 1 0 | fixed | ok (whether the server-side DROP still completes after the client exits: cannot verify without a measurement) |
| Stops after step 5 failed / Ctrl-C, comes back later | — | — | — | — | 1 0 | not run | step 1 on return says `1 0` = "bukan server yang dimaksud" → stop, **SRO stays OFF** (I-1) |
| **Running it twice** (first run dropped the database; operator repeats because Ulangi failed or unsure) | pass | empty | — | `>` **truncates the existing good backup**, mysqldump "Unknown database", grep 0 → stop | — | — | **only backup destroyed** (I-2) |
| MySQL down | stop | — | — | — | — | — | unchanged |
| Agent down | pass … drop works | | | | | | Ulangi: agent offline; later missing-copy text → new rebuild (covered 2085-2091) |
| App still connected (writing to other DBs as root) | | | | | SRO OFF for milliseconds | | negligible |

### "Melepas kunci start dengan tangan", changed steps (README 2212-2360)

| Case | State | README next step? |
|---|---|---|
| Happy path (lock in config, MySQL down) | as in the D re-review walk (a)-(c); step 2 now includes the processlist check when MySQL is up | yes |
| MySQL already up (case e), processlist shows only event_scheduler | continue; if the server is `0 0`, **events defined on the old master run and write** until step 5 (m-5) | no |
| MySQL down at step 2, started at step 4 | the processlist check was skipped ("Bila MySQL mati … aplikasinya sendiri") and is not repeated after the start (m-5) | no |
| Stops at step 3 because no `init_file=` line | agent stopped (step 1), MySQL as before, no instruction to start the agent again (m-4) | "berhenti" only |
| Stops after step 1 or 2 | agent stopped, guard off; the abandonment sentence (2295-2300) only covers "langkah ini atau sesudahnya" from step 4 | no (m-4) |
| Stops at/after step 4, before 6 | remove the added line, start the agent → guard re-asserts on agent start | yes |
| MySQL refuses to start after printf | "berhenti… jangan hapus baris `init_file=`" plus the abandonment sentence | yes |
| Wrong server | the section is entered only after `set-role master` on this server; no role check of its own (unchanged, out of scope) | — |

### Failed-rebuild block ("Setelah bangun ulang gagal", "Batalkan percobaan bangun ulang", data tertinggal) (README 1925-1973)

| Case | Panel state (code) | README |
|---|---|---|
| Rebuild fails before the configure answer (pin quarantined, role replica, failed row, old-master marker kept) | `AwaitingRebuild` true (approval.go:81-102): box with Bangun ulang + Batalkan (server_detail.html:407-410); replica page sentence + Batalkan (replica_detail.html:89-90); Resync/Lepas refused (`ErrAwaitingRebuild`); not a Buat replika target; no TLS jobs; start alerts closed (alerts.go:355-361) | all stated 1925-1945 |
| Batalkan | one store transaction, no agent call, refuses when busy (controls.go:156-173); job superseded (runner.go:935-968: row gone / role changed) | stated 1947-1955 |
| After Batalkan, copy not started | plain quarantined old master: Periksa/Amankan offered, Hapus offered | stated |
| After Batalkan or cancel, copy started (any rebuild since the promotion reached step index of "Salin data") | Periksa/Amankan hidden and refused with CopyStartedText (oldmaster.go:184-186) | stated; but see R-2: "copy started" also covers attempts where nothing local changed |
| Fails after the configure answer | `QuarantineEnded` saved, marker cleared → ordinary failed replica | stated 1854-1860 |
| Configure answer lost (timeout) after the agent settled | flag false, panel still shows old master; Batalkan records `quarantined` while the agent pin is `replica` (R-4) | not stated |
| Cancel of a running rebuild after the settle | treated as Buat replika: row removed, role reset, `releaseRole` (runner.go:1176-1194) | stated 1860-1862 |

## False, unverifiable or dangerous statements in the changed text

### Critical

**C-1. The backup check lets a truncated dump through, then the only copy is dropped.** README 2036-2038 and 2045-2048: "Lanjut hanya bila berkas itu ada, ukurannya di `ls -l` bukan 0, dan `grep` menulis `1`."
- Evidence: mysqldump writes the header and `CREATE DATABASE` before any table. When it aborts part-way (a classic: a view or trigger whose definer no longer exists, "Couldn't execute 'SHOW FIELDS FROM …' … (1449)"; a corrupt table; a full disk), it exits non-zero but the file is > 0 bytes and holds exactly one `CREATE DATABASE`. All three checks pass. The plan's rows only measured a complete dump (plan 476, 483-485) and a missing database (484).
- Harm: step 5 drops a database that, by this section's own premise, exists nowhere else (1998-1999). Irreversible data loss on the path the README explicitly approves.
- Corrected step 4 (ready to paste):

  > 4. Cadangkan database itu ke berkas di `/root`. Pastikan dulu berkas itu belum ada: `ls -l '/root/<nama>-cadangan.sql'` harus menjawab "No such file or directory" (lihat juga langkah sebelumnya bila berkas itu sudah ada). Lalu:
  >
  > ```sh
  > mysqldump --single-transaction --routines --triggers --events --set-gtid-purged=OFF --databases '<nama>' > '/root/<nama>-cadangan.sql'; echo "keluar $?"
  > ls -l '/root/<nama>-cadangan.sql'
  > grep -c 'CREATE DATABASE' '/root/<nama>-cadangan.sql'
  > tail -n 1 '/root/<nama>-cadangan.sql'
  > ```
  >
  > Lanjut hanya bila keempatnya benar: baris pertama menulis `keluar 0`, ukuran berkas bukan 0, `grep` menulis `1`, dan baris terakhir berkas diawali `-- Dump completed on`. Bila `mysqldump` menulis galat apa pun (misalnya "Unknown database" atau "Couldn't execute"), cadangan itu tidak lengkap: berhenti dan jangan lanjut ke langkah 5.

- The `keluar $?` and `-- Dump completed on` trailer need an evidence row (a complete dump, a missing database, and one aborted dump, e.g. a view with a dropped definer).

### Important

**I-1. A leftover `1 0` is reported as "the wrong server", so the operator stops and leaves `super_read_only` off.** README 2015-2016 and 2119-2120: "Yang pertama harus menulis `quarantined`, yang kedua `1 1`. Bila salah satunya lain, berhenti: ini bukan server yang dimaksud."
- Evidence: a failed or interrupted step 5 / step 2 leaves `1 0` (plan 459, 462). The guard does not set it again while MySQL runs (quarantine.go:302-304, given facts). An operator who walked away and comes back, or who starts the section again for the next database, hits step 1 with `1 0` and is told to stop.
- Harm: the quarantined old master keeps `super_read_only=OFF` with no next step. `read_only` alone does not stop root or SUPER/CONNECTION_ADMIN sessions (README 2058-2060), and applications connected as MySQL root are common in this audience. Their writes never reach the new master and become errant GTIDs on the old master.
- Corrected sentence (both sections):

  > Yang pertama harus menulis `quarantined`. Bila tidak, berhenti: ini bukan server yang dimaksud (di master baru perintah-perintah di bawah akan menghapus data yang sedang dipakai atau mengunci aplikasi dari menulis). Yang kedua harus `1 1`. Bila `1 0`, itu sisa percobaan sebelumnya yang berhenti di tengah: jalankan `mysql -e 'SET GLOBAL super_read_only=ON;'` lalu periksa lagi sampai `1 1`. Bila `0 0` atau MySQL tidak menjawab, berhenti, jalankan `systemctl restart dbpanel-agent` (penjaga karantina memasang baca-saja lagi saat agent mulai), tunggu beberapa detik, lalu periksa lagi.

**I-2. Running the drop section a second time destroys the backup.** README 2036: `… > '/root/<nama>-cadangan.sql'`.
- Evidence: the shell truncates the target before mysqldump starts. On a second run after the database was already dropped, mysqldump fails with "Unknown database" and leaves the 798-byte header file (plan 484) in place of the good backup. The stop at 2045-2048 comes after the truncation. "Simpan berkas itu di tempat yang aman" (2048) does not say to move or copy it, so on many servers the only copy stays at that path.
- Harm: the only copy of a database that exists nowhere else is lost. Typical triggers: Ulangi fails (missing-copy text, button gone) and the operator repeats the whole section, or the operator is unsure whether the drop ran.
- Corrected sentence (before the command, see C-1): "Bila `ls -l '/root/<nama>-cadangan.sql'` menunjukkan berkas yang sudah ada, jangan jalankan `mysqldump` ke nama itu: berkas lama terhapus begitu perintah dimulai, juga bila `mysqldump` gagal. Periksa dulu apakah database itu masih ada di server ini (`mysql -e 'SHOW DATABASES LIKE "<nama>";'`). Bila sudah tidak ada, database itu sudah dihapus pada percobaan sebelumnya: lewati langkah 4 dan 5. Bila masih ada, pindahkan dulu berkas lama ke tempat lain (misalnya `mv '/root/<nama>-cadangan.sql' '/root/<nama>-cadangan-lama.sql'`)." And at 2048: "Salin berkas itu ke server lain atau media lain sebelum lanjut; jangan hanya menyimpannya di server ini."

### Minor

**m-1. The missing-grants paragraph does not work as written for the agent's own text.** README 2163-2171, 631-634, 1896-1899.
- Evidence: connect.go:152 joins the statements with `"; "` and puts no `;` after the last one (often `GRANT USAGE ON *.* TO '…'@'…' WITH GRANT OPTION`, connect.go:479-481). Pasted "di antara `SET SESSION sql_log_bin=OFF;` dan `SET GLOBAL super_read_only=ON;`", the last GRANT runs into the SET: ERROR 1064, the earlier GRANTs already ran, the client stops, and the result is `1 0`. The step-3 check catches it, but the operator has no hint why. The evidence row (plan 466) was measured with the `;` added. Also "baris `GRANT`" (633, 1897) is ambiguous: the heredoc is one line holding four statements. "heredoc" (2169) is jargon the README never explains.
- Harm: a confusing failure; partial grants; repeated `1 0` windows.
- Corrected: "… jalankan perintah `GRANT` itu dengan cara yang sama: ikuti langkah 1-3 di atas, tetapi di langkah 2 ganti perintah `GRANT SHOW DATABASES …;` di tengah baris itu dengan semua perintah `GRANT` dari teks itu, dan akhiri setiap perintah dengan titik koma (`;`), juga yang terakhir (teks agent tidak menulis titik koma di akhir). Baris itu tetap diawali `SET GLOBAL super_read_only=OFF; SET SESSION sql_log_bin=OFF;` dan diakhiri `SET GLOBAL super_read_only=ON;`. Lalu klik **Hubungkan MySQL** lagi."

**m-2. The README says what is not detected after a full rebuild, but not how to check for it.** README 1831-1834.
- Evidence: the sentence is exact. dump.go:136-153 lifts read-only (`liftReadOnly`, dump.go:460-466: `read_only` and `super_read_only` OFF), resets the binlogs, then lists `before`. `notCopied` (rebuild_load.go:172-189) flags only `before` names or names outside `named`. CLONE (`whole`) flags nothing and replaces the data. So on a full dump a database created between `listBefore` and the end of the load passes, and so do writes into copied databases (README 1640-1647 already says those become errant GTIDs). The panel requires "Sudah saya alihkan" before a rebuild (create_replica.go:54-55).
- Harm: the operator is told to cut over before, but has no way to notice a miss afterwards.
- Add after 1834: "Sesudah bangun ulang selesai, bandingkan daftar database di halaman server ini dengan halaman master baru. Database yang hanya ada di server ini dibuat selama pemuatan: datanya tidak ada di master baru. Jangan pakai server ini sebagai replika sebelum hal itu diselesaikan."

**m-3. `SHOW DATABASES LIKE "<nama>"` treats `_` and `%` as wildcards.** README 2024-2029.
- Evidence: MySQL LIKE semantics. Hospital database names often hold `_` (for example `simrs_lama`).
- Harm: a different name can match (`simrsxlama`), and the operator stops with no next step. This is the safe direction. A name holding `"` or `\` breaks the query.
- Corrected: "Jawabannya harus kosong. Bila tampil nama yang persis sama, berhenti … Bila tampil nama lain yang mirip (tanda `_` di perintah itu cocok dengan huruf apa pun), abaikan nama itu."

**m-4. Abandoning the release before step 4 leaves the agent stopped.** README 2272-2273 ("berhenti di sini dan jangan nyalakan MySQL") and 2295-2300 (covers only "langkah ini atau sesudahnya").
- Harm: the agent and its guard stay off with role `quarantined`. When MySQL is already up (case e), the operator stops without the step-5 read-only check.
- Add at 2273: "Jalankan lagi agent (`systemctl start dbpanel-agent`) bila bisa. Bila MySQL sudah menyala, jalankan juga pemeriksaan langkah 5 sebelum meninggalkan server ini." The same "jalankan lagi agent" applies to a stop after step 1 or 2.

**m-5. The connection check is one point in time, and treats `event_scheduler` as harmless.** README 2240-2252.
- Evidence: the check is skipped when MySQL is down at step 2, and step 4 then starts MySQL with no new check. When MySQL comes up `0 0` (measured case (b)), scheduled events on the old master write like an application does. The `event_scheduler` row only shows that such writes can run.
- Corrected: "… Yang boleh tampil hanya `event_scheduler` … Bila server ini punya event terjadwal (`mysql -e 'SELECT EVENT_SCHEMA, EVENT_NAME FROM information_schema.EVENTS WHERE STATUS=\"ENABLED\";'` tidak kosong), event itu bisa menulis seperti aplikasi: sebelum langkah 4 tambahkan juga baris `event_scheduler=OFF` di bawah baris `super_read_only=ON` (dan hapus lagi di langkah 6)." Also in step 5: "Jalankan lagi pemeriksaan sambungan dari langkah 2 segera setelah MySQL menyala." These need evidence rows.

**m-6. Bare GRANT advice on a quarantined server's page is not covered.** README 324-327 and the new paragraph 2163-2171.
- Evidence: the server page banner "Akun admin MySQL ini kurang hak. Jalankan perintah berikut di MySQL server ini (sebagai root), lalu klik Segarkan" (server_detail.html:252-258) is shown whatever the role, from the stored inventory (runner.go:264-268). On a quarantined server those GRANTs fail with ERROR 1290. The new paragraph covers only the refusal text of **Hubungkan MySQL**.
- Add to 2163-2171: "Perintah di kotak "Akun admin MySQL ini kurang hak" di halaman server yang dikarantina dijalankan dengan cara yang sama."

**m-7. Names with a quote, backtick or double quote are not handled.** README 2041-2043.
- Evidence: the commands hold `$` and spaces (plan 483-486). A `'` in the name breaks the single quotes: in step 4, `'a'b' > '/root/a'b-cadangan.sql'` parses into one argument, so the "redirection" becomes part of the database name. In step 5 the quote is unbalanced and the shell waits for more input. The D report's "the shell rejects the command" is not what happens. A backtick breaks the DROP (step 6 catches it). Nothing is destroyed, but the operator is lost.
- Add: "Bila nama database memuat tanda kutip (`'` atau `"`) atau backtick (`` ` ``), jangan pakai perintah di bawah; minta bantuan admin MySQL."

**m-8. "Pakai bila … atau untuk memeriksa data tertinggal" holds only before the copy started.** README 1953-1955.
- Evidence: the next list (1963-1967) says that after a copy started, Batalkan does not bring Periksa back. The two passages do not contradict each other, but the first one alone leads the operator to cancel for nothing.
- Corrected: "… atau, bila bangun ulang belum sampai di tahap **Salin data**, untuk memeriksa data tertinggal."

**m-9. A heredoc copied from the raw README file hangs.** README 2126-2128: the code block sits in a list item, so the raw file has `   EOF` with leading spaces.
- Evidence: with `<<'EOF'`, bash ends the heredoc only on a line that is exactly `EOF`. Copied from the rendered page, the indentation is gone.
- Add: "Baris `EOF` terakhir harus berada di awal baris, tanpa spasi di depannya; bila shell terus menampilkan `>`, tekan Ctrl-C (tidak ada yang dijalankan) dan ketik ulang."

**m-10. `-u root -p` with the heredoc is not measured.** README 2109-2110.
- `mysql -p` reads the password from the terminal, so this should work, but there is no evidence row. Cannot verify without a measurement. Add the row.

**m-11. The new-account variant leaves the password in root's shell history.** README 1898-1899.
- Evidence: bash stores a heredoc's lines in `~/.bash_history`. The mysql client's own history filter does not apply, because the SQL never passes through it interactively.
- Add: "Kata sandi itu tersimpan di riwayat shell root; sesudahnya jalankan `history -c` dan hapus baris itu dari `/root/.bash_history`, atau buat akun itu dari dalam `mysql` (ketik `mysql`, lalu jalankan keempat perintah satu per satu)."

### Statements verified true (no change needed)

- 1825-1834: the full-rebuild sentence. It is exact for kind (a full dump; partial dumps flag the database, CLONE replaces everything), window (between `listBefore` after the read-only lift and the end of the load) and read-only state (`read_only` and `super_read_only` OFF during the load, dump.go:460-466).
- 1854-1862: the G-1 moment, the log line, and cancel after the settle.
- 1867-1869: only direct grants count (`SHOW GRANTS FOR` the account by name, rebuild_load.go:250-256; plan line 349 for `mandatory_roles`).
- 1879-1880: the job page shows the refusal whole (F-6).
- 1883-1884 and 2156-2158: works at both steps without an agent restart (fresh pool per step, E report).
- 1941-1943: Ulangi disappears after another job or after the cancel (`NewerJobExists`, row/role checks at runner.go:935-968; handlers_jobs.go:322-326 hides the button).
- 1944-1945: the monitor's start alerts open only for role `quarantined` (alerts.go:355-361), and the status rules skip only `quarantined` (alerts.go:529).
- 1963-1967: Periksa/Amankan are refused after a copy started, also after a cancel. `RebuildCopyStarted` reads every rebuild since the promotion (oldmaster.go:131-165). The orchestrator's `StartLeftover`/`StartSalvage` refuse with `refuseAfterCopy`.
- 609-615: leftover and salvage need the source pin.

## Residual risks (the README cannot fix these; the code behaves this way)

- **R-1 (Important).** The quarantine guard does not re-assert `super_read_only` while MySQL stays up (quarantine.go:302-304). Every by-hand path that ends at `1 0` depends on a human reading step 3 or step 6 correctly. I-1 shows how that fails. A per-tick read of the two flags would close it.
- **R-2 (Important).** "Copy started" is decided by the step index alone (oldmaster.go:156-159). A rebuild that stopped inside **Salin data** before anything local changed counts as started, and Periksa/Amankan are then refused for good, with a text saying the data "sudah dihapus atau diganti". Such stops include the preflight re-run at the start of the step (create_replica.go:806-816), the CLONE confirmation wait (716-723) followed by a cancel, `verifySnapshot`, and a listing refusal in `dumpFrom` before the binlog reset (dump.go:120-128). Leftover transactions the binlog still holds can then not be salvaged, and the README gives no by-hand way.
- **R-3 (Important, pre-existing; README now states it).** On a full dump rebuild, a database or rows written by a still-connected application during the load are not detected. They stay as foreign data and errant GTIDs on the new replica, and the quarantine ends. Mitigated only by the "Sudah saya alihkan" confirmation.
- **R-4 (Minor).** If the `configureReplication` answer is lost after the agent settled, `QuarantineEnded` stays false. **Batalkan percobaan bangun ulang** then records `quarantined` in the panel while the agent's pin is `replica` and its guard is off. A delete dialog then wrongly says the server stays quarantined. A Retry heals it (D report), but nothing tells the operator to prefer Retry.
- **R-5 (Minor).** In the failed state the panel judges no start-lock alerts (alerts.go:355-361). A MySQL started writable without the agent goes unannounced until Batalkan or a new rebuild.
- **R-6 (Low).** The agent's missing-grants text ends without `;` and says "jalankan … sebagai root" on a server where that fails (connect.go:150-152). The server-page banner gives the same bare advice (server_detail.html:255). The agent's unsupported-name text omits `$`, which the proof accepts (rebuild_load.go:203 vs :230).
- **R-7 (Important, carried from the E re-review R-1).** The proof/listing race through GRANT/REVOKE toggling is unchanged. The README cannot mitigate it.

## Out-of-scope observations

- README 2209 ("Memakai lagi …", unchanged) still says `sudo mysql`. It is outside the by-hand sections, so this is consistent with m-A as briefed.
- `information_schema.PROCESSLIST` is deprecated in 8.0 (`performance_schema.processlist` is the successor). It works on 8.0.46 (plan 489).
- The D report says a name holding `'` "makes the shell reject the command". In fact the shell either merges words (step 4) or waits for more input (step 5); see m-7.
- The "Melepas kunci start" section has no role check of its own, so a run on the wrong server (for example the new master) is guarded only by its preconditions. This is unchanged from earlier rounds.
