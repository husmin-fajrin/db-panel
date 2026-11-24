Task 1: minor (deferred): commit 548c1fd does not build its tests until 9127ee9 renames the test alias (bisect breaks for one commit; history is not rewritten).
Task 1: minor (deferred): F2 carries a raw driver/dial cause that can hold single quotes, which the panel's quote censor may cut; `errLiftLockUnopenable` has the same exposure (see K9: quoted names in censored lines).
Task 1: minor (deferred): of F2's three failure branches only the `openAdmin` one is tested (`d.host()` and the query failure are not).
Task 1: minor (deferred): every testmysql teardown now opens a root connection and runs two queries before SIGTERM (bounded at 2 s).
Task 2: minor (deferred): doc comments of `lateMessage` and `lateCloseReason` (monitor/start_state.go) still describe the rules before F7 and P-M6.
Task 2: minor (deferred): `TestLateRuleDecisions` has no `topologyGone` dimension; the new fact is covered only by the integration test.
Task 2: minor (deferred): `deletableLocked` reads the replica list twice and shadows `reps`/`err` (controls.go).
Task 2: minor (deferred): `topologyGone` re-reads `PromotionByOldMaster`; `closeStartAlerts` does two store reads per tick per open late alert of a quarantined server without a topology.
Task 2: minor (deferred): the "master lama" busy label shows only when the old master's unfinished job has no topology id (a job with the topology id is refused earlier with "topologi ini …").
Task 3: minor (deferred): a wrapped agent error is labelled as a whole in the audit (panel words included) while the flash keeps the panel's words before the label (audit.go vs orchestrator/agenttext.go).
Task 3: minor (deferred): a long labelled audit outcome can exceed `maxAuditOutcome` (500) and is then cut by `auditAs` without an ellipsis.
Task 3: minor (deferred): an unknown RolePin made only of whitespace/control characters renders as `tidak dikenal (Pesan dari agent X (bukan dari panel): )`.
Task 3: minor (deferred): the panel keeps its own copy of the agent's source-host rule (handlers_servers.go `hostPortPin`); a change to the agent's rule must be made in both.
Task 4: minor (deferred): audit-plan-5a.md:217 "Topologi yang sudah menjalankan …" makes the topology the actor; the other rewrites say "… sudah dijalankan di topologi".
Task 4: minor (deferred): `"Wajibkan pemeriksaan sertifikat TLS" wajib` reads redundantly (inventaris :282, :442; audit-5a :731).
Task 4: minor (deferred): README lines 453, 687, 688, 925, 927, 2817, 3064, 3117 run past 80 columns after the rename.
Task 4: minor (deferred): three test comments still say "Perketat" (failed_rebuild_test.go:916, tls_test.go:697, alerts_test.go:809).
Task 5: minor (deferred): TestChangedOnRolePinAndPerfSchema lives in monitor_test.go while the other changed() tests are in changes_test.go (the brief's Files list put it there).
Task 5: minor (deferred): the agent mysql test package now takes about 60 minutes; splitting it or trimming its slowest tests is work for a later plan.
Task 6: minor (deferred): backup.go logs the folder clipped in one line and unclipped in another.
Task 6: minor (deferred): a folder deleted between open and readlink reads "<path> (deleted)" and is reported `none` instead of `missing`.
Task 6: minor (deferred): `hidden`/`invalid` are tested through ValidBackupFolder and one end-to-end case (/home/x); /proc/1 and a relative path through monitor.configure are not.
Task 6: minor (deferred): gate 1 ran while one comment in backup.go was edited (no code); gates 2 and 3 ran on the committed tree. (The reviewer's "-timeout 60m" point is Ruling R3; note added to global-constraints.md.)
Task 6: minor (deferred): as root the R14 test creates `dbpanel-backup-test-*` under /var/lib (or /srv, /opt, /mnt, /run); a killed test run leaves it behind outside the test temp directory.
Task 6: minor (deferred): backupFS doc comment says the walk path is "the real place, and its depth the real depth" — true against symlink swaps only, not against a concurrent rename of an already-opened ancestor (gains an attacker nothing).
Task 6: minor (deferred): the `to-proc1` and `proc/1` test cases assume /proc/1 can be opened (not so under hidepid).
Task 7: note for Task 9 (batch 2): JudgeBackup extrapolates the age from the last report (brief's formula), so an offline master can read `stale`; the Task 9 dispatch must have the gate row checked against the spec's rule that unknown data is yellow, not red.
Task 7: minor (deferred): JudgeBackup adds the agent's NewestAgeSec to the elapsed time unchecked; a huge or negative value from a hostile agent wraps to a negative age (judged ok, and Task 8's card could show it); a negative NewestSize passes the 0-byte check. A hostile agent can report "fresh" anyway.
Task 7: minor (deferred): backup_stale closing because the server stopped being a master is reported as a recovery (same as the existing master rules).
Task 7: minor (deferred): ruleCtx reads the server row once per master on every evaluation, under m.mu.
Task 7: minor (deferred): a stored account cut at 255 bytes can lose the "…" that marks it as cut.
Task 7: minor (deferred): over-long comment line in TestMigration0013Columns.
Task 7: minor (deferred): BackupMessage with a limit of 0 hours ("lebih dari 0 jam") is no longer tested; JudgeBackup falls back to the default for a row without a limit.
Task 8: minor (deferred): R22's refill window — any report the agent produced before it received the new folder (late configure answer, live summary, backlog batch, CheckNow answer) puts the old folder's backup state back until the answer to the new configure (`pending`) arrives; a full fix needs the report to name its folder or carry a generation number.
Task 8: minor (deferred): saving unchanged values still forgets the report (card reads "Belum ada laporan dari agent." until the next report); comparing with the stored row first would avoid it.
Task 8: minor (deferred): web `backupMaxAge` repeats monitor's `backupMaxAgeHours`.
Task 8: minor (deferred): renderServerWith reads the monitor view twice; `backupForm *backupCard` carries only two strings.
Task 8: minor (deferred): the marks card renders its heading with no rows when the marks cannot be read.
Task 8: minor (deferred): an unparsable stored mark time shows "Ditandai oleh … pada -."
Task 8: minor (deferred): the last block of TestConfigureServerForgetsBackup asserts nothing.
Task 9: minor (deferred): gate.go is 612 lines (a third is text constants); not split.
Task 9: minor (deferred): testmysql.freePort (instance.go:279) releases the port before mysqld binds it, so `make test` can fail once when packages run side by side (seen once, 2026-11-22).
Task 9: minor (deferred): a Buat replika target with no health report reads G10b for disk (yellow, does not block); the agent's own preflight still checks disk inside the job.
Task 9: minor (carried to Task 10): the all-offline loop of TestGateUnknownIsYellow does not assert yellow on clock, disk, perf_schema, backup.
Task 9: minor (deferred): a red multi-server row's Detail also carries the yellow "unknown" sentences of its other servers (Task 11's page should read well with that).
Task 9: minor (deferred): gate.go is 632 lines; could be split into gate_texts.go and gate_rows.go.
Task 10: minor (carried to Task 12): a start refused by the orchestrator after the handler's own gate check passed is audited with the G14 page text in promote, create_replica and databases handlers, with G0 elsewhere; audit a.outcomeOf(err) for a *GateError.
Task 10: minor (carried to Task 12): the promote handler's gate pre-check is keyed on the posted mode, not view.Mode (G14 instead of the "mode moved" message; an `emergency` post against a planned view consumes the token before checkPromote refuses). Not a bypass: StartPromote gates on the view's mode.
Task 10: minor (deferred): job page checks the gate before the superseded check, so a superseded data-dropping job reads G14 with a fix pointing at the old master (hand-made POST only).
Task 10: minor (deferred): in StartCreateReplica the role check could move before the gate and before RemoveReplica.
Task 10: minor (deferred): JobGateInput returns "not gated" for unreadable params; safe only because rebuildApprovalForJob refuses such create_replica params (reasoning lives in a comment).
Task 10: minor (deferred): plain create_replica with an unknown target shows the gate's error text instead of the earlier target message; no web test covers the plain create_replica gate.
Task 10: minor (deferred): e2e helper connectRoot now connects as dbpanel_admin (misleading name); five tests call greenGate themselves after a promote.
Task 10: note: until Task 11 adds the tick, a planned promote or rebuild with no backup folder is refused in the UI with G15 (Task 11 must land before any release; it is the next task).
Task 11: minor (carried to Task 12): no test asserts which GateInput the five red-only screens read on GET (the fake gate answers the same for any input).
Task 11: minor (carried to Task 12): the D3 test does not assert the box is not pre-ticked.
Task 11: minor (carried to Task 12): the "Siapkan ulang" red list is inside the folded "Tindakan lanjutan" box; open the box while a row is red.
Task 11: minor (deferred): on Bangun ulang without approval the old approval hint and D2 both show (they agree; old text not changed).
Task 11: minor (deferred): outsideGateLists in web_gate_test.go depends on the section's exact class string.
Task 11: note: web/static/app.js changed outside the brief's list (Alpine component targetGate toggles server-rendered lists per picked server; no inline script; JS-off state is the server-rendered first choice with the Task 10 refusal as backstop) — accepted by the reviewer.
Task 12: minor (carried to Task 14): no test sets a gate error on an emergency post and asserts the promote still starts with "skipped_red":null (the "never refused by a gate error" rule is unguarded).
Task 12: minor (carried to Task 14): TestPromoteModeMovedBeforeGateAndToken checks only the first gate input.
Task 12: minor (deferred): skipped_red is read before StartPromote's own fresh view, outside startMu; the recorded set can differ from the rows at the moment the job is created (record only).
Task 12: minor (deferred): on an emergency promote the orchestrator refuses, SEC-44 turns the three audit fields into lengths (nothing was skipped then).
Task 12: minor (deferred): TestUngatedHandlersSkipGate no longer asserts the emergency gate read happens after the token; the K2 web check cannot tell where the blocked line came from.
Task 12: minor (deferred; for the security audit): a logged-in post without a confirmation token (stale mode or missing tick) now re-renders through PromoteView, which makes agent CheckNow/probe calls; the promote GET already does the same.
Task 12: minor (deferred): StartPromote checks the tick after PromoteView, so a tickless call still sends mysql.probeSource to candidates (the handler's pre-check prevents it in practice).
Task 12: note: Runner.PromoteMode added to the web Orchestrator interface (no agent call; same function PromoteView uses); the D5 box sits in the form footer beside D3 because the mode card is outside the form; skipped_red is null when the gate cannot judge and the emergency promote still starts.
Task 13: note for the final review and audit (pre-existing, documented in README 2515-2519): unlock-master resets only super_read_only's saved value; the read_only saved by quarantine stays and comes back after a MySQL restart; README line 803's comment ("RESET PERSIST, read_only OFF") is looser than the code.
Task 13: minor (deferred): guide 9 step 3 compares a host that replicas-seen may hold as a name with an address that may be an IP (wrong reading continues in the safe direction).
Task 13: minor (deferred): guide 6 has two composed read-only check steps and a second pointer sentence beyond R33's one sentence; its step 5 does not mention the "kunci start tidak bisa dilepas" outcome.
Task 13: minor (deferred): guide 7 moves the "Setiap set-role juga menghapus id karantina" note to the end and the data-loss warning ahead of set-role master (safer, but not the source order).
Task 13: minor (deferred): guide 3 step 8 tells the operator to open Promote… on the new master only to look; step 3's "Jangan hapus berkas cadangan lama" reads as a non-sequitur; step 1 assumes the old master is reachable.
Task 13: minor (deferred): guide 2 step 9 "Pastikan nama dan alamat IP itu memang master baru" is ambiguous (inherited from the README).
Task 13: minor (deferred): guide 5's grep exits 2 on a missing path (source kept); _ui.html icon and .perintah CSS outside the brief's list (accepted).
Task 13: minor (carried to Task 14): guide 8's lead-in bullets overlap — a quarantined old master is not on the Replikasi page, so bullet 2 matches it before bullet 3 (operator gets ERROR 1290; reset-admin note recovers).
Task 13: minor (carried to Task 14): guide 9 step 8 "Lalu periksa …" can be read as part of the "MySQL mati" branch, so the read-only check may be skipped when MySQL is already running.
Task 13: minor (carried to Task 14): guide 8 step 6's condition (row red or replica already uses panel_admin) is hard for an operator to see; running step 6 on every full replica is harmless.
Task 14: minor (deferred): owner's list line for `master lama %s masih punya pekerjaan yang belum selesai` (promote.go:287-291) lacks the "tidak ada di rencana" mark.
Task 14: minor (deferred): README "Keamanan" lists the agent-reported rows as (jam, pin master, ruang disk, performance_schema, cadangan) but leaves out Akun panel and Versi agent, which a compromised agent can also forge.
Task 14: minor (deferred): README "Pembaruan" step 1 "Panel menolak agent yang versinya berbeda" reads as if the agent is disconnected; it is planned actions that are refused.
Task 14: minor (deferred): README "Prasyarat" table omits when the Port MySQL master row is skipped (panel host is the master, the target or a replica).
Task 14: minor (deferred): README:803 comment "kedua flag OFF sampai restart" can suggest both flags come back on restart.
Task 14: minor (deferred): e2e test 3 not mutation-checked; its /var/lib/dbpanel-e2e-backup-* folder is left behind when go test's timeout panics; the 5-minute rescan of an unchanged folder is shown only by agent unit tests.
Task 14: minor (deferred; NAMED ITEM for the final review and audit): two quick saves of the backup form start two background monitor.configure calls (monitor.go:368-373, 403-440); out of order, the agent scans the older folder while the store names the newer one, so the card, backup_stale and the gate's backup row judge the wrong folder — a false green is possible until the agent reconnects.
Task 14: minor (deferred): commit 54fce1b carries the brief's message ("… docs for Plan 5b-2b") but holds only the tests; the docs are in f2ff98a and d12a4df.
