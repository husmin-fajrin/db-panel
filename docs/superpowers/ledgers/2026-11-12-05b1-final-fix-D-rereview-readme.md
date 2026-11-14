# Re-review of final fix wave part D: README and plan evidence (0b1c760..552b47b)

Sources: `git show 552b47b:README.md` (the line numbers below are from that file), `git show 552b47b:<path>` for code, `final-fix-D-readme.diff`, and `final-fix-D-code.diff`. Nothing was run. No mysqld, go test or make was used. Where only a measurement can settle a point, it is named as a check for the controller.

### Finding Verdicts

**D-1 (CRITICAL) "Melepas kunci start dengan tangan": ADDRESSED.** It brings in one new Important defect (N-1, below) and two Minor ones.
- Plan evidence: new subsection "Bukti baca-saja saat MySQL dinyalakan tanpa agent" (plan file, docs commit 9d643ee, before README commit d024025). It covers (a), (b), (b2), (b3), (b4) and (c). (b) gives `0 0`, so a persisted OFF beats the option-file line.
- README 1993-2121: the order is now: caution (2000-2008, before step 1) → 1 stop agent → 2 cut applications off, with the reason "Tulisan yang masuk ke server ini tidak pernah sampai ke master baru." → 3 find the option file and lock line → 4 (only if MySQL is down) add `super_read_only=ON`, which is explicitly "bukan jaminan … Perlindungan yang pasti hanya langkah 2", then start → 5 check immediately, with `SET PERSIST` and the meaning of `0` (2079-2083) → 6 remove the lines → 7 remove the file → 8 role files → 9 open only for a master.
- The two-master caution comes before the first step that can open writes. The quoted panel alert is identical to `startUnguardedText` (monitor/alerts.go:384).
- The guard does what the section assumes: `guardQuarantined` (quarantine.go:252-325) acts only on a transition. `if d.guard.up && sameRun { return nil }` means that with the agent stopped (step 1), nothing puts read-only back.

State walk (agent stopped from step 1 on; RO = read_only, SRO = super_read_only; P = persisted):

| Case | After 1 | After 2 | After 3 | After 4 (start) | After 5 | After 6-7 | After 8 | After 9 (master) |
|---|---|---|---|---|---|---|---|---|
| (a) lock effective, down, P ON | agent off, MySQL down | apps cut | file/line known | lock runs: 1 1, P ON ON | check passes | lock line + extra line + file gone; P ON | role master, pins gone | P OFF OFF after the two-master caution; apps back |
| (b) lock unused, down, P ON | same | apps cut | known | 1 1 from P | passes | same | same | same |
| (c) lock unused, nothing P | same | apps cut | known | 1 1 from the added line (measured (a)), P none | P shows nothing → SET PERSIST → 1 1, P ON ON | P ON | same | same |
| (d) lock unused, P OFF | same | apps cut (see N-1) | known | **0 0** (measured (b)), apps cut | `0` → paragraph 2079 → SET PERSIST → 1 1 | P ON | same | same |
| (e) MySQL already up, agent dead | no change | apps cut (but see N-1: open connections) | known | step skipped | `0` possible → SET PERSIST; writes before step 2 named as lost to the new master | P ON | same | same |
| (f) lock file missing, line effective | — | apps cut | known | "Failed to open the bootstrap file" → printf rewrite (content = `startLockContent`, startlock.go:41) → 1 1, P ON | passes | same | same | same |
| (f') still does not start | — | apps cut | known | "berhenti di sini: jangan hapus baris atau berkas kunci" | — | — | — | MySQL down, agent stopped (safe; see m-B) |

(g) Operator walks away:

| Stops after | State | Safe? |
|---|---|---|
| 1 | agent stopped, role quarantined, lock in config, MySQL as before | Yes. A reboot starts the agent (still enabled), and its guard persists read-only on start. A manual MySQL start without the agent is cases (a)-(d) with apps maybe still connected: same risk as before the procedure. |
| 2 | apps cut | yes |
| 3 | apps cut, nothing changed | yes |
| 4 | MySQL up, maybe 0 0 (d), apps cut | Yes while the cut holds. The added `super_read_only=ON` line stays in the option file (m-B). |
| 5 | P ON ON | yes |
| 6/7 | lock gone, role still quarantined, P ON | Yes. Starting the agent rewrites the lock within 5 s. |
| 8 | role master, P ON, apps cut | yes |

**D-2 (Important) "Database yang tidak ada di master baru": ADDRESSED.** It brings in one new Important defect (N-2, wrong server) and several Minor ones.
- The heading is exact (README 1882) and sits at the level of "Persetujuan di server untuk bangun ulang".
- The section says when this happens (1884-1891), and that the data exists nowhere else (1897-1899; see m-D).
- Backup with `mysqldump` (1904-1915), including the size and `CREATE DATABASE` check.
- One `mysql -e` invocation using SET GLOBAL only (1920). Measured in the plan (c) and in report round 2, including `sql_log_bin=OFF`.
- What to run after an error, and how to check (1930-1937).
- It says the persisted values are unchanged (1938-1941) and correctly says the guard does not restore SRO while MySQL runs (code above).
- Both ways forward are given, simple first (1943-1954). This holds against `Runner.Retry` (runner.go:844-902: relaunches `j.CurrentStep`), `settleRebuilt` (quarantine.go:444-499: load record kept on A6; after the drop, `notCopied` is empty) and the D-9 predicate.
- The A6 paragraph (1823-1831) is now true (`leftoverDatabases`, create_replica.go:568-586; preflight.go:527-544).

State walk (old master in the A6 state: pin quarantined, 1 1, P ON ON, agent running, replication configured, apps maybe connected):

| Step | MySQL | RO/SRO | P | Agent | Notes |
|---|---|---|---|---|---|
| start | up | 1 1 | ON ON | running, approval kept | job failed at the configure step (A6) |
| 1 mysqldump (root shell) | up | 1 1 | ON ON | — | measured works at 1 1; file checked |
| 1, run with `sudo` from a non-root shell | — | — | — | — | `> /root/…` opens as the caller: "Permission denied", mysqldump never runs; `ls`/`grep` fail too (m-A) |
| 2 drop ok | up | 1 0 → 1 1 | ON ON | — | ordinary accounts blocked throughout (measured); no binlog event |
| 2 drop fails (wrong name, 1008; FK 3730; any error) | up | **1 0** | ON ON | does NOT restore | text says to run `SET GLOBAL super_read_only=ON` → 1 1 |
| 2 hangs (metadata lock held by a connected app; `lock_wait_timeout` default 1 year) | up | 1 0 until it ends | ON ON | — | not covered (m-C) |
| agent restarted between | up | guard persists ON on agent start → 1 1 | ON ON | new run | safe; then "Ulangi" answers with errNoLoadRecord → new rebuild (text covers it, 1946-1949) |
| approval expired before Ulangi | — | — | — | — | Retry refused by `rebuildApprovalForJob` with the approve command (covered in the "Persetujuan" section, not repeated here) |
| **2 run on the NEW master** | up (writable master) | 0 0 → … | — | role master | see N-2 |

**D-3 (Minor): NOT ADDRESSED for one sub-item. m-1 … m-12 ADDRESSED.**
- m-1 (2034-2037), m-2 (2100-2116), m-3 (2100-2103, 2066-2068), m-4 (2056-2058), m-5 (2049-2054; content equals startlock.go:41), m-6 (663-664; tls.go:512-529 `refuseBusy` on topology rows), m-7 (636), m-8 (2455), m-9 (769), m-10 (1668), m-11 (1650).
- m-12 (301-308): checked. `MySQLError` is used as the "Tidak terjangkau" detail at monitor/status.go:168 and :260, as "Pesan MySQL" at master_detail.html:145 (Detail teknis) and replica_detail.html:105, and not on the quarantined status.
- The "membuat akun MySQL" line (633-636) is rewritten, but the new text is false about dump accounts (m-E). NOT ADDRESSED.

**D-8 README: ADDRESSED.** README 1850-1873:
- The quotation is identical to `errNoLoadRecord` (quarantine.go:523-526).
- "hanya mengulang langkah pengaturan replikasi … gagal lagi" is true (Retry relaunches `CurrentStep`, and `loadedFrom` stays empty after an agent restart).
- Step 1 places are true: box via `oldMasterAwaitingRebuild` + `HasTopology` in server_detail.html; the job page line, which needs `CanRetry`, at job_detail.html; the replica page line at replica_detail.html via handlers_mysql.go:915-921.
- Step 2: the same form (replica_new.html: accept-connections, confirm dialog with typed name).
- Step 3: approval reuse and a new code after expiry ("Sudah disetujui …", replica_new.html:27).

**Part C README passages: NOT ADDRESSED (listing refusal advice).**
- All quotations are identical (table below).
- The upgrade note on NAT/public addresses (316-322) is true: `localMySQL` (connect.go), and the guard's `h.Open` fails, so it returns silently.
- DNS refusal advice: true.
- Missing-copy advice: true (D-8).
- **Listing refusal: "Jalankan `GRANT` itu, lalu tekan Ulangi dari tahap ini" (1848) is not true as written.** The refusal comes at the dump step before `liftReadOnly` (dump.go:122-128) or at the configure step after the load's `restore()` and the guard's after-load recheck. In both, the server is `super_read_only=ON`. A GRANT by MySQL root is then refused (ERROR 1290 --super-read-only). See I-B.
- A6 paragraph example "dibuat aplikasi … selama pemuatan" (1824-1825): mostly not what produces A6 (m-F).

**D-6 README follow-up: ADDRESSED.** README 1801-1804 matches `localApprovalDetail` (handlers_oldmaster.go): no field when `!seen.Read`. `Read` is `err == nil`, so an unusable answer (`errApprovalUnreadable`) also gives no field (approval.go:78-82). There is one Minor imprecision (m-G).

### False, unverifiable or dangerous statements in the changed text

**N-1 (Important): step 2 of the manual release offers a firewall close that does not cut every application.** README 2012-2017: "hentikan aplikasinya, atau tutup port MySQL (biasanya 3306) di firewall server ini untuk semua alamat selain server ini sendiri."
- Applications on the same machine (socket, `localhost`, `127.0.0.1`) are not affected by a port rule. In case (e), MySQL is already running: the usual firewalld/iptables rule sets accept `ESTABLISHED,RELATED` first, so open connections from other hosts keep writing.
- In case (d), MySQL comes up `0 0` (measured (b)). In case (e) it may already be writable. Step 2 is then the only protection ("Perlindungan yang pasti hanya langkah 2"), and with the firewall alternative a local or already-connected application writes into the old master. That is the two-master divergence D-1 exists to prevent. The `0` check catches it only after the fact.
- Corrected: "2. Pastikan tidak ada aplikasi yang bisa menulis ke server ini: hentikan semua aplikasi yang memakai MySQL ini, termasuk aplikasi yang berjalan di server ini sendiri. Menutup port MySQL (biasanya 3306) di firewall hanya menahan sambungan baru dari server lain; aplikasi di server ini sendiri dan sambungan yang sudah terbuka tidak ikut terputus, jadi firewall saja tidak cukup bila MySQL sudah menyala atau ada aplikasi di server ini. Tulisan yang masuk ke server ini tidak pernah sampai ke master baru. …"

**N-2 (Important): the drop procedure does not make sure the operator is on the old master, and its error branch turns a wrong-server run into an outage of the new master.** README 1901-1941.
- The only guard is "Kerjakan di master lama itu". Nothing says how to tell the servers apart.
- On the new master, the database named in the text does not exist (that is the premise):
  - Step 1's `mysqldump` fails ("Unknown database"). Its header is still written, so `ls -l` is not 0 and `grep -c` prints `0`. The text says "Pastikan …" but never says to stop when that fails.
  - Step 2's command then runs `SET GLOBAL super_read_only=OFF` (a no-op there) and fails at `DROP DATABASE` with "Can't drop database".
  - The README then says, for exactly that error, to run `sudo mysql -e 'SET GLOBAL super_read_only=ON;'` and check for `1 1`. That makes the production master read-only (every application write fails), and the check confirms `1 1` as the good state.
- If the name does exist on the new master (A6 race, m-D), the DROP succeeds there without binlog. Replicas keep it, the master loses it (backup exists), and the master is left SRO=ON.
- Corrected (before step 1): "Pastikan dulu Anda berada di master lama: `cat /etc/dbpanel/role` harus menulis `quarantined`, dan `sudo mysql -e 'SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only;'` harus menjawab `1 1`. Bila tidak, berhenti: ini bukan server yang dimaksud." Step 1: "Bila ukuran berkas 0 atau `grep` tidak menulis `1`, berhenti dan jangan lanjut ke langkah 2." Error branch: "Jalankan perintah ini hanya di master lama (peran `quarantined`), jangan di master baru."

**I-B (Important, part C passage under verification): GRANT advice for the listing refusal cannot work on a quarantined server.** README 1844-1848, quoting `errListIncomplete` (rebuild_load.go), then "Jalankan `GRANT` itu, lalu tekan **Ulangi dari tahap ini**."
- The server is `super_read_only=ON` at both places the text appears (above), so root's `GRANT` fails with ERROR 1290.
- An operator who works around it by switching `super_read_only` off without `sql_log_bin=OFF`, at the configure step (after the load's binlog reset), writes a GTID of the old master's own. Promote checks later count that as errant (the reason the implementer put `sql_log_bin=OFF` into the D-2 drop).
- `<akun>` is `'user'@'host'` (`quoteAccount`) and contains single quotes, so it cannot go inside a single-quoted `mysql -e '…'`.
- Corrected: "Server yang dikarantina baca-saja, jadi `GRANT` biasa ditolak. Jalankan di server itu sebagai root:
  ```sh
  sudo mysql <<'SQL'
  SET GLOBAL super_read_only=OFF;
  SET SESSION sql_log_bin=OFF;
  GRANT SHOW DATABASES ON *.* TO <akun>;
  SET GLOBAL super_read_only=ON;
  SQL
  sudo mysql -e 'SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only;'
  ```
  Yang kedua harus menjawab `1 1`; bila tidak, jalankan `sudo mysql -e 'SET GLOBAL super_read_only=ON;'`. Lalu tekan **Ulangi dari tahap ini**."
- Controller check: on a scratch mysqld at `1 1` with persisted ON, confirm that `GRANT SHOW DATABASES ON *.* TO 'x'@'localhost'` as root gives ERROR 1290, and that the heredoc form works and leaves `1 1` with `gtid_executed` unchanged. (The agent text itself, "Jalankan sebagai root MySQL di server ini: GRANT …", has the same gap. That is agent code: see Out-of-Scope.)

**m-A (Minor): `sudo` with a redirection to `/root`.** README 1901 says to work as root ("setelah `sudo -i`"), but every command starts with `sudo`, e.g. 1907 `sudo mysqldump … > /root/<nama>-cadangan.sql`.
- From a non-root shell, the redirection is opened by the caller: "Permission denied", and mysqldump never runs. The `ls`/`grep` checks then fail too, and step 2 (`sudo mysql …`) still works. With no explicit stop (N-2), the only copy can be dropped without a backup.
- Corrected: drop the `sudo` in step 1 and say "Jalankan di shell root (setelah `sudo -i`); `sudo perintah > /root/…` tidak berhasil dari pengguna biasa."

**m-B (Minor): the added `super_read_only=ON` line can outlive an abandoned procedure.** README 2038-2058, 2084-2085.
- Step 4's "berhenti di sini: jangan hapus baris atau berkas kunci" also keeps the added line.
- If the release is later finished by the agent (`set-role`) and the server becomes a master, `unlock-master` does `RESET PERSIST`. Every later MySQL start is then `super_read_only=ON` from the option file, so the master is unwritable after a reboot.
- Add to the stop sentence: "Hapus lagi baris `super_read_only=ON` yang Anda tambahkan, lalu jalankan lagi agent (`systemctl start dbpanel-agent`)."
- Step 4 also gives no guidance when step 3 found no `init_file=` line: "Bila langkah 3 tidak menemukan baris `init_file=`, berhenti dan jangan nyalakan MySQL."

**m-C (Minor): a DROP that hangs is not covered.** README 1930-1937 covers "berhenti dengan galat". A DROP waits for metadata locks of connected applications (this section does not cut them off), for up to `lock_wait_timeout` (default one year), with SRO=OFF meanwhile. Add: "Bila perintah itu tidak selesai (menunggu) dan Anda menghentikannya dengan Ctrl-C, jalankan juga `SET GLOBAL super_read_only=ON` di atas; hentikan dulu aplikasi yang masih tersambung ke database itu."

**m-D (Minor): the section does not say how to know the database is not on the new master, and "tidak di replika lain" is not guaranteed.**
- README 1897-1899 and 1902-1903: "Ganti `<nama>` dengan nama database dari teks tadi."
- A6 can name a database the new master also has: one created after the wipe, in the lift → `listBefore` window (dump.go:136-153). Dropping it removes copied data, and the replica breaks on that database's events.
- Other replicas may still hold a database a partial new master never had.
- Corrected: "Periksa dulu bahwa database itu memang tidak ada di master baru (daftar database di halaman master baru); yang ada di sana dihapus oleh bangun ulang baru, bukan dengan cara ini. Isinya tidak ada di master baru; jangan mengandalkan salinan di tempat lain."
- Also quote the name in the shell (`--databases "<nama>" > "/root/<nama>-cadangan.sql"`) for names with spaces.

**m-E (Minor, D-3 sub-item): "akun lain, juga akun dump, ditolak" is false for dump accounts.** README 633-636.
- On a quarantined server, `mysql.createDumpUser` is `strictProtected` (strict.go), so it is allowed under an approval. `refusedByRole` refuses it only for `roleMaster` (pins.go:899-906). `roleRefuses[quarantined]` does not list it. The handler (users.go) creates it with `keepExisting` false.
- MySQL itself refuses `CREATE USER` while SRO is ON, except during the rebuild load (read-only lifted).
- Corrected: "membuat akun replikasi `dbpanel_repl@'%'` (hanya bila belum ada baris `dbpanel_repl` di host mana pun; akun replikasi lain ditolak) dan akun dump (agent tidak menolaknya, tetapi MySQL yang baca-saja menolak membuatnya kecuali selama pemuatan bangun ulang)".
- Controller check: whether that is the intended rule, or whether the agent should refuse it (agent change, not README).

**m-F (Minor): the A6 example and the section's second cause.**
- README 1823-1825: "misalnya dibuat aplikasi yang belum dialihkan selama pemuatan". README 1893-1895: "dibuat di master lama selama pemuatan salinan".
- For a full rebuild (`Databases` nil, create_replica.go:183 `selectDatabases`), `notCopied` (rebuild_load.go:172-189) flags only databases in `before`, which is listed right before the load (dump.go:151). A database created during the load is not flagged, and the quarantine ends with it. The example is true only for a partial rebuild or the short window before `listBefore`.
- Corrected (1823-1825): "Teks itu muncul bila di server ini ada database yang sudah ada saat pemuatan dimulai, atau (pada bangun ulang sebagian) database yang tidak termasuk yang disalin." See Out-of-Scope for the agent gap.

**m-G (Minor): the audit sentence on `local_approval: false`.** README 1803-1804: "`local_approval: false` berarti agent terbaca dan bangun ulang belum disetujui". An answer "persetujuan tidak diperlukan" (`Required` false, so `Approved` false, and the rebuild proceeds) is recorded the same way. Corrected: "… berarti agent terbaca dan tidak memegang persetujuan (belum disetujui, atau agent menjawab persetujuan tidak diperlukan)."

**m-H (Minor): first-rebuild preflight case.** README 1889: "bangun ulang yang dimulai lagi gagal di pemeriksaan awal". A first rebuild of an old master that holds a database the new master lacks stops at the same check (preflight.go:542-544), and that is the usual way cause 1 shows. Corrected: "bangun ulang (yang pertama atau yang dimulai lagi) gagal di pemeriksaan awal …".

**m-I (Minor, plan evidence):** the plan's (c) table records the drop without `SET SESSION sql_log_bin=OFF`. The README form with it was measured only in the report (round 2). Add the row.

### Quoted texts checked

| Text | README line | Code location | Identical |
|---|---|---|---|
| "MySQL yang disambungkan harus berada di server ini (soket, 127.0.0.1, atau alamat server ini sendiri); <host> bukan alamat server ini." | 299-301 | connect.go `errNotLocal.Error` | yes |
| "alamat MySQL <host> tidak bisa dicari (DNS): <sebab>. MySQL yang disambungkan …" + "tidak ada alamat" | 310-313 | connect.go `errLookupLocal.Error`, `localMySQL` | yes |
| "… bukan alamat server ini." | 319 | same | yes (fragment) |
| A6 "server ini masih punya database yang tidak disalin dari sumber … \"Database yang tidak ada di master baru\"." | 1818-1822, 1887-1888 | rebuild_load.go `errNotReplaced.Error` | yes |
| "sumber replikasi sudah diatur, tetapi daftar database di server ini tidak bisa dibaca; karantina tidak diakhiri: <sebab>" | 1832-1834 | quarantine.go `settleRebuilt` | yes |
| "daftar database di server ini tidak bisa dipastikan lengkap: … lalu ulangi bangun ulang dari panel." | 1844-1847 | rebuild_load.go `errListIncomplete.Error` | yes |
| errNoLoadRecord "sumber replikasi sudah diatur, tetapi server ini tetap dikarantina: … karena salinan data harus diulang." | 1852-1856 | quarantine.go:523-526 `errNoLoadRecord` | yes |
| "server ini masih berisi database lain (<nama database>); versi ini hanya mendukung server replika khusus" | 1889-1891 | preflight.go:542-544 | yes |
| "Ulangi dari tahap ini" | 1848, 1857, 1863, 1944, 1951 | job_detail.html:85, :87 | yes |
| "Sudah disetujui …" | 1871 | replica_new.html:27 | yes (prefix) |
| "Klik Periksa lagi untuk melihat persetujuan di server." | 1670 | replica_new.html:17 | yes |
| "replika <server> masih punya pekerjaan yang belum selesai" | 665-666 | tls.go:524 `refuseBusy` + runner.go:328 `ErrBusy` | yes |
| "master lama belum terjamin terkunci saat MySQL menyala; jangan nyalakan MySQL-nya sebelum agent tersambung" | 2002-2004 | monitor/alerts.go:384 `startUnguardedText` | yes |
| "kunci start tidak bisa diperiksa" | 1997 | cli.go:332 `setRoleNoCheck` | yes (fragment) |
| "bila MySQL di server ini masih baca-saja (read-only) … pastikan master lain tidak lagi dipakai" | 2105-2108 | cli.go:333 `setRoleNextMaster` | yes (fragment) |
| "Failed to open the bootstrap file" | 2047 | MySQL log line (start_kinds_test.go:148, startlock_guard.go) | yes |
| printf content of the lock file | 2051 | startlock.go:41 `startLockContent` | yes (bytes after printf) |
| job page line `Bila "Ulangi dari tahap ini" tidak menolong, …` (described, not quoted) | 1863-1865 | job_detail.html (RebuildURL line) | consistent |

### Out-of-Scope Observations

- Agent text `errListIncomplete` (rebuild_load.go) and the older grant refusal on quarantined ("jalankan perintah GRANT berikut sebagai root lalu periksa ulang", connect.go) both tell root to run `GRANT` on a server that is `super_read_only`. That needs an agent-text decision together with I-B.
- Full rebuild: a database created by an application during the dump load (after `listBefore`) is not flagged by `notCopied` (named empty), so the quarantine ends with a database that is not from the source. This contradicts README 1813-1817 ("bila setiap database non-sistem … dibuat oleh pemuatan dump itu sendiri"). It is an agent gap from part C/A, not this diff.
- After a failed rebuild (panel role `replica`, failed row), the TLS paths skip only `RoleQuarantined` (tls.go:65, 108, 529, 797). Daily renewal and Perketat can therefore reach that server, and its agent installs a certificate under a kept approval. README 660-668 ("panel tidak memasang sertifikat MySQL baru di server yang dikarantina") is then untrue for that state. This falls under D-10 point 2's TLS check.
- m-12 wording "menampilkan teks itu": the panel passes `MySQLError` through `redact.Quotes` (monitor/state.go:223). That is harmless for these texts, which have no quotes.

### Verdict

**Fix round:** Findings remain open:
- **I-B (Important, part C README passage):** "Jalankan `GRANT` itu" for the listing refusal fails on a quarantined, `super_read_only` server. It needs the lift/`sql_log_bin=OFF`/restore form (heredoc, because the account name has single quotes). Measurement named for the controller.
- **D-3, m-E (Minor):** "akun lain, juga akun dump, ditolak" is false for `mysql.createDumpUser` on a quarantined server under an approval.

New breakage brought in by the fix:
- **N-1 (Important):** manual release step 2 accepts a firewall close, which does not cut local applications or open connections. Yet that step is the only protection when MySQL comes up writable.
- **N-2 (Important):** the drop procedure has no check that the operator is on the old master and no explicit stop when the backup check fails. On the new master, its error branch tells the operator to set `super_read_only=ON`, which stops the production master's writes.

Minor: m-A … m-I as listed.
