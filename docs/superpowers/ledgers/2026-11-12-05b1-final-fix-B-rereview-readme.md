### Finding Verdicts

All code read at 472fd35 (`git show 472fd35:<path>`); README = `git show 472fd35:README.md` (line numbers below are of that file).
(README line numbers are those of `git show 472fd35:README.md`, accurate to a line or two.)

**T12-1 — manual release step claimed read-only unconditionally** — ADDRESSED (end state), with a new Critical defect in the added step (see C-1). README 1863-1887: new step 2 checks `SELECT @@GLOBAL.read_only, @@GLOBAL.super_read_only;` = `1 1` and `performance_schema.persisted_variables` ON for both, else `SET PERSIST` both and check again; step 7 (1925-1926) is now tied to step 2. The check is the agent's own release rule or stricter: `readOnlyProvedNow`/`persistedReadOnlyOn` (internal/agent/drivers/mysql/startlock_guard.go:215-253) release when both are PERSISTed ON (README additionally demands live `1 1`: conservative). The lock file content quoted via "Master lama" matches `startLockContent` (startlock.go:41). The required "if MySQL is down, start it first (the start lock makes it read-only and saves that)" was written unconditionally, which is false when the lock is not effective — C-1.

**T12-2 — no two-master caution before opening writes** — ADDRESSED. README 1925-1932 quotes `setRoleNextMaster` (internal/agent/cli.go:333) character for character ("bila MySQL di server ini masih baca-saja (read-only) dan server ini memang akan dipakai aplikasi sebagai master, pastikan master lain tidak lagi dipakai") before the `SET PERSIST … OFF` block. (The caution does not cover step 2, where writes can open earlier: C-1. The condition "only if it becomes the application's master" now lives only inside the quotation: m-2.)

**T12-3 — command to find the line** — ADDRESSED (with m-1). README 1895-1909: `grep -rn 'dbpanel-start-lock.sql' /etc/my.cnf /etc/my.cnf.d /etc/mysql /usr/etc/my.cnf` plus `mysqld --verbose --help … | grep -A1 '^Default options are read'`; matches layout.go:89-108 (ConfigFile = first existing of mysqld's list, IncludeDir = last `!includedir`), realhost.go:135-149 (`managedConfigPath`: IncludeDir/zz-dbpanel.cnf, else ConfigFile through symlinks with the block), startlock.go:161-190 (`StartLockPath`). `parseHelp` keys on the same "Default options are read from the following files" line (layout.go:126).

**T12-4 — how to connect as MySQL root** — ADDRESSED. README 1876-1878, 1852-1853: `sudo mysql`, or `mysql -u root -p`.

**T12-5 — "tidak membuang data"** — ADDRESSED. README 582-583 "tidak membuang data MySQL"; salvage deletion is in the allowed list and is not MySQL data.

**T12-6 — English term at first mention** — ADDRESSED. README 582 "Mode ketat (strict mode)", 569 "dibangun ulang (rebuild: …)", 1637 "dibangun ulang (rebuild)". (In "Peran" the first quoted "Mode ketat" at 572 is a cross-reference before the definition: acceptable.)

**A-1 — rebuild ends quarantine only if every non-system database was created by the load; A6; listing-failure text; what the operator does** — NOT ADDRESSED. Rule, system-database list, A6 text and the listing-failure text match the code (rebuild_load.go:154-161, quarantine.go:443-478, systemDatabases per part A). But the operator action (README 1798-1803) is false in the reachable legitimate case: see I-1.

**A-2 / F-10 — `mysql.connect` local only; grant refusal on quarantined; stored remote account; repair** — ADDRESSED (m-12). README 296-307, 600-606. errNotLocal (connect.go:198-201), `localMySQL` (connect.go:208-237), grant refusal (connect.go:143-147), stored account check in `OpenDB` (conn.go:75-79) and dump (dump.go:297).

**A-3 — two `--print-defaults` runs, bounds, new reasons** — ADDRESSED. README 1450-1465. startlock.go:45 (10 s), 49 (256 KiB), 54-55 (20 / 300), 606-649 (root run HOME=/nonexistent, then service account uid/gid/groups), 635 (id text), 680 (too large), 922 (prefix "konfigurasi efektif MySQL tidak bisa dibaca: "), 926-934 (first run that does not hold gives the reason).

**A-4 / F-5 — approve always resolves; B16; B15 and the way out; removed screen line** — ADDRESSED. README 1660-1681, 1704-1706. rebuild_approval.go:313-345 (`RebuildApprovalAddr`: IP literal → itself; >1 → B16 first; moved → B15), 375-395 (texts), cli.go:487-502 (exit 1 before the screen), pins.go:482-495 (`SetSource` removes source-addr, keeps request; same host:port keeps the code). Old "(nama sumber juga mengarah ke: …)" line gone from README.

**A-5 — install refused, dropUser limited, approve screen line** — ADDRESSED (m-7). README 612-622, 1665-1668, 1780-1784, 2271-2273. pins.go roleRefuses[quarantined]["mysql.install"], pins.go:898 (dropUser on quarantined refused unless `dbpanel_dump_` prefix), cli.go:510 (screen line).

**A-7 — syslog line, facility, tag, fields, warning; agent Info line** — ADDRESSED. README 1724-1735. cli.go:557-563 (`LOG_AUTH|LOG_NOTICE`, tag `dbpanel-agent`), 570-589 (fields; times UTC; `user=%q sudo_uid=… uid=…`; no code; warning text), 452 and 547 (actions), strict.go:185 (Info line, fields cmd/source/approved).

**F-1 / F-2 — start-state change reported at the next check, also on quarantined servers** — ADDRESSED. README 1503-1508. agent monitor/changes.go:26-47 (`startResultChanged`), monitor.go:81 (15 s default), 501-502. P-5 limit sentence (1505-1510) matches start_state.go:222-260 (burst 5, 10 min, first alert of a kind finds a full bucket).

**F-4 — rebuildApproval with unreadable pins** — ADDRESSED. README 636-638; strict.go:209.

**F-6 (C16)** — ADDRESSED. README 733-737; cli.go:653-654 (text, `%v` = removal error), role unchanged, exit 1.

**P-2 — panel-worded reasons, fallback, label** — ADDRESSED. README 1745-1771; orchestrator/approval.go:243-274, template replica_new.html:29, handlers_oldmaster.go:803-805, 832-836, runner.go:1663.

**C12 (addendum §1)** — ADDRESSED. README 1838-1844 quotes cli.go:334 exactly; "(juga bila berkas pin tidak bisa dibaca)" matches cli.go:427-428; the added sentence at 748-750 matches GuardTick/guardStartLock (startlock_guard.go:141-210: unassigned never gets read-only set) and refusedByRole (setReadOnly off allowed outside replicaLike) — m-9 on its wording.

Also checked (brief's list): D1/D1b (handlers_oldmaster.go:489-499; `Terkunci` = status.go:28 / server_detail.html:146) — ADDRESSED, README 1565-1569, 1627-1631, 1820-1834. Perketat busy (tls.go:512-529) — ADDRESSED with m-6. P-6 text (replica_new.html:17) — ADDRESSED (m-10). P-7, P-4 audit fields (approval.go:121; handlers_oldmaster.go:820-829) — ADDRESSED. P12b (approval.go:145-148) — ADDRESSED.

### False, unverifiable or dangerous statements in the changed text

**C-1 (Critical) — step 2 starts MySQL with the agent stopped and promises the lock makes it read-only.** README 1868-1872: "Bila MySQL mati, nyalakan dulu …: selama baris kunci masih ada, mysqld menjalankan berkas kunci saat menyala, yang memasang dan menyimpan baca-saja."
Code: the agent itself only trusts the line when `startLockStatus` says it holds — the agent file must be the LAST `init_file` mysqld reads in every `--print-defaults` run, with no prefix (startlock.go:883-955, `judgeInitFiles`). README 1520-1527 (unchanged) lists the reachable bypasses: an operator `init_file` read after the agent's line, or `--init-file` on the service command line ("kunci start tidak dipakai MySQL"). In those cases mysqld never runs `dbpanel-start-lock.sql`.
Where it goes wrong: (i) the procedure's own entry condition is `set-role master` printing "kunci start tidak bisa diperiksa" (cli.go:332, 636-639) — the lock's effect is unknown by definition; (ii) a server whose panel showed "Kunci start: belum terjamin" or a bypassed start. With the agent stopped (step 1) nothing else sets read-only (guardQuarantined does not run). Following step 2, MySQL comes up writable — a second writable master — and stays so until the operator logs in and runs the check; any client still pointing at it writes, and those writes diverge from the new master. The two-master caution comes only in step 7. The panel's own alert for this state says the opposite of step 2: "jangan nyalakan MySQL-nya sebelum agent tersambung" (panel/monitor/alerts.go:384).
Corrected text (move "find the file" before this step):
"Bila MySQL mati, jangan langsung menyalakannya. Baris kunci hanya membuat MySQL baca-saja bila mysqld memang memakainya (di panel: kunci start "Terkunci"); bila kunci pernah "belum terjamin", ada `--init-file` di opsi layanan, atau Anda tidak tahu (misalnya `set-role master` menulis "kunci start tidak bisa diperiksa"), MySQL bisa menyala dalam keadaan bisa ditulis. Karena itu, sebelum menyalakannya, tambahkan satu baris `super_read_only=ON` tepat di bawah baris `init_file=` di berkas yang ditemukan di langkah 3, dan pastikan tidak ada aplikasi yang mengarah ke server ini. Baris tambahan itu dihapus bersama baris `init_file` di langkah 4. Bila pemeriksaan di bawah menjawab `0`, MySQL sempat bisa ditulis sejak menyala: tulisan yang masuk tidak ada di master baru."

**I-1 (Important) — what to do after A6 is false in the case that actually produces A6.** README 1798-1803: "Bangun ulang biasa dari panel tidak berakhir begini … Bila teks itu muncul, jalankan lagi **Bangun ulang sebagai replika** dari panel; database yang disebut ikut dihapus setelah daftarnya Anda setujui".
Code: the rebuild deletes only the target's databases that the new master also has (`leftoverDatabases`, create_replica.go:570-586); a target database the new master lacks makes the agent preflight fail: "server ini masih berisi database lain (…); versi ini hanya mendukung server replika khusus" (preflight.go:527-544). In the legitimate flow every database the master has is dropped before the load, and one that was present before the load and not on the master already fails the preflight (part A's analysis). So A6 from a legitimate rebuild comes only from a database created on the old master during the load window (read-only is lifted then; README 1616-1623 itself warns apps may write in that window) that the new master does not have — exactly the kind a new rebuild refuses rather than deletes. "tidak berakhir begini" is false for that case too. No panel path drops such a database on a quarantined server (only the rebuild's WipeList and change_databases, which targets ready replicas, send `mysql.dropDatabases`).
Effect: no data loss (refusals), but an operator following the README loops between A6 and "Server replika khusus" with no correct way out.
Corrected text: "Teks itu muncul bila di server ini ada database yang tidak berasal dari salinan, misalnya dibuat aplikasi yang belum dialihkan selama pemuatan. Database yang juga ada di master baru ikut dihapus bila Anda menjalankan lagi **Bangun ulang sebagai replika** (setelah daftarnya Anda setujui). Database yang tidak ada di master baru tidak bisa dihapus lewat panel: bangun ulang berikutnya berhenti di pemeriksaan awal dengan "server ini masih berisi database lain (…)". Salin isinya bila masih diperlukan, lalu hapus database itu di server itu sebagai root MySQL sebelum mengulang." (The exact root-side procedure needs an owner decision: the server is `super_read_only`, so the DROP needs read-only opened briefly while the guard puts it back within 5 s.)

**m-1 (Minor) — step 3's first command prints nothing on common layouts.** README 1901: `mysqld --verbose --help 2>/dev/null | …`. mysqld is not on root's PATH for RHEL distribution packages (`/usr/libexec/mysqld`) or aaPanel (`/www/server/mysql/bin/mysqld`) — the agent looks there itself (layout.go:45-50). With `2>/dev/null` even "command not found" is hidden, and the fallback ("jalankan lagi dengan berkas dari perintah pertama") has nothing to work with. Corrected: "Bila perintah pertama tidak menampilkan apa pun, ganti `mysqld` dengan path lengkapnya: `/usr/sbin/mysqld`, `/usr/libexec/mysqld`, `/www/server/mysql/bin/mysqld` atau `/usr/local/mysql/bin/mysqld`."

**m-2 (Minor) — step 7's "only if it becomes the application's master" condition sits only inside a quotation.** README 1925-1932. A reader who chose `unassigned` in step 6 reads "Baru sesudah itu buka sebagai root MySQL" as the next step. Corrected: "Buka hanya bila server ini akan menjadi master yang dipakai aplikasi (bukan bila Anda memilih `unassigned`), dan seperti kata `set-role master`: "…pastikan master lain tidak lagi dipakai". …"

**m-3 (Minor) — "Karena langkah 2 menyimpan" / "itu juga syarat agent".** README 1885-1887, 1925. Step 2 often only verifies; the agent's rule is persisted ON alone (startlock_guard.go:226-231), the README's is stricter. Corrected: "Karena langkah 2 memastikan `read_only` dan `super_read_only` tersimpan ON …" and "agent memakai syarat yang sama (tersimpan ON) sebelum melepas kunci sendiri".

**m-4 (Minor) — step 2 has no guidance when MySQL does not start for another reason.** README 1869-1874 covers only "berkas kuncinya hilang". The same "Failed to open the bootstrap file" also appears when the file exists but mysqld may not read it (permissions, AppArmor/SELinux; README 1534, startlock.go:980 / macAllows); other start failures are not covered. "Lanjutkan hanya bila …" does stop the operator (safe direction) but without saying so. Add: "Bila MySQL tetap tidak mau menyala, berhenti di sini: jangan hapus baris atau berkas kunci; perbaiki dulu sebab di log error MySQL."

**m-5 (Minor) — rewriting the lock file has no command.** README 1871-1874. Limited-skill operators need it; a file made elsewhere and moved keeps the wrong SELinux label. Suggested: "`printf 'SET GLOBAL super_read_only=ON;\nSET PERSIST read_only=ON;\nSET PERSIST super_read_only=ON;\n' > <path>; chown root:root <path>; chmod 0644 <path>`" (content = startlock.go:41).

**m-6 (Minor) — Perketat busy sentence false for an old master without a replica row.** README 641-644: "selama server itu masih menjalankan pekerjaan (misalnya bangun ulang), **Perketat** ditolak". `StartTightenTLS` checks only the topology's replica rows (tls.go:509-527); a quarantined old master before any rebuild has no row, so a running **Periksa/Amankan data tertinggal** job on it (oldmaster.go:121-160) does not refuse Perketat. Harmless (it is left out anyway). Corrected: "…selama bangun ulangnya masih berjalan, **Perketat** ditolak…".

**m-7 (Minor) — dump account pattern narrower than the code.** README 615-620: "`dbpanel_dump_<8 heksa>`"; on a quarantined server the code allows any name with prefix `dbpanel_dump_` (pins.go:898). Say "akun dump sementara (nama berawalan `dbpanel_dump_`)".

**m-8 (Minor) — security section omits "non-sistem".** README 2274-2275 "setiap database di server itu memang hasil salinan" → "setiap database non-sistem …".

**m-9 (Minor) — "Sesudah itu agent tidak lagi memasang baca-saja di server unassigned".** README 748. The agent never sets read-only once the role is `unassigned`, also before the lock goes (GuardTick runs guardQuarantined only for `quarantined`). Corrected: "Sejak perannya `unassigned`, agent tidak lagi memasang baca-saja: …".

**m-10 (Minor) — P-6 scope.** README 1647: "lewat tautan dari situs lain" — also `same-site` (another host of the same site) is not asked (handlers: askApproval). "…dari situs atau host lain…".

**m-11 (Minor) — "Hapus server" box bullet says the lock "tetap terpasang" unconditionally.** README 1628-1630: true only when a lock was ever placed (StartLockPath can refuse: startlock.go:161-190). "…kunci start-nya (bila terpasang) tetap …".

**m-12 (Minor) — stored-account rule narrowed to "dari versi lama".** README 303-306. `OpenDB` refuses any stored host that is not local now, including a host name of the server itself that no longer resolves (DNS failure → errNotLocal, connect.go:213-220). Say "Akun tersimpan yang host-nya (sekarang) bukan alamat server ini…". Also "laporan pemantauan server itu membawanya" was not verified by the fixer and I did not trace the panel display either: unverifiable as worded.

### Quoted texts that differ from the code

None. 39 compared: errNotLocal; grant refusal; roleRefusal; pin-unreadable for mysql.rebuildApproval; Perketat busy; C16; "keluaran --print-defaults terlalu besar" with prefix; id-account reason with prefix; D1; D1b; C12; setRoleNextMaster fragment; "Klik Periksa lagi untuk melihat persetujuan di server."; A-5 screen line; B16; B15; "rebuild approval written"; "rebuild approval cancelled"; syslog warning; "protected command allowed by the local rebuild approval"; the eight InvalidReason sentences and the fallback; P5 sentence; msgApprovalUnreadable; bad_params line; agent label; P12b tail; A6; listing-failure text; "Failed to open the bootstrap file" (startlock_guard.go:410); "Terkunci"; "belum terjamin".

### Out-of-Scope Observations

- A6 itself (agent text, rebuild_load.go:159-160) tells the operator "Selamatkan atau hapus database itu lewat panel"; for a database the new master lacks there is no panel action that drops it on a quarantined server. Needs a code/text decision together with I-1.
- After a manual or CLI release, an operator `init_file` that was shadowed during the quarantine (read before the agent's line) runs at the next start; and read-only "after restart" relies on mysqld-auto.cnf being loaded (`persisted_globals_load`, default ON). The agent's own release rule has the same assumption; I cannot tell from the code whether either is reachable on the target sites.
- "Peran" text "membuat akun MySQL" (butuh persetujuan) omits that on a quarantined server only `dbpanel_repl@'%'` can be created (strictReplUser, pins.go). Pre-existing behaviour, touched line.

### Verdict

**Fix round:** Findings remain open — C-1 (Critical: manual release step 2 can start the old master writable with the agent stopped, before any two-master caution; T12-1's new text), I-1 (Important: A-1's operator action after A6 is false in the legitimate case). Minor: 12.
