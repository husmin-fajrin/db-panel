# Re-check of the security-audit fix wave — WEB items S1, S3, S6 (Plan 5b-2)

Fix base b950757, head 5fb84df. Read: `audit-fix-wave.md` (S1, S3, S6), `audit-web.md` (WB-1, WB-3, WB-8, WB-9),
`audit-fix-report.md` (S1, S3, S6, texts, noticed), commits 5f9dc80, 344a963, 5fb84df and the texts part of d01422b
(the diff file `review-b950757..5fb84df.diff` has the same changed lines as `git diff b950757..5fb84df`; compared).
Read-only: nothing in the working tree, index or branch was changed; this file is the one write.

Tests: none run. A full `go test -timeout 120m ./...` was running on this checkout (`pgrep -f "go test"`: pid 399,
the controller's gate), so by the rule I ran nothing. The implementer's RED/GREEN output is a claim I checked against
the diff by reading; every verdict below rests on reading, not on a run of mine.

## Item Verdicts

### S1 (WB-1, Tinggi) — ADDRESSED

The box (`web/templates/_status.html:50-61`, shown by `status-full` :46 when `DownGuide`,
`internal/panel/web/views_monitor.go:105`):

1. `Periksa listrik, jaringan dan MySQL di server master. Bila master hidup lagi, panel melihatnya sendiri dan replika menyambung kembali.`
2. `Jangan me-restart replika: posisi replikasinya dibutuhkan untuk langkah berikut.` (unchanged)
3. `Bila master tidak bisa dipulihkan, jalankan promote darurat: di kartu Ganti master di halaman ini, tekan Promote…. Ikuti langkahnya di panduan "Promote darurat".` (link `/panduan#promote-darurat`)

- No SQL, no "panel belum bisa", no "versi berikutnya", no step that tells the operator to point applications
  anywhere. Old steps 3-5 and the note are gone (`_status.html` hunk of 5f9dc80).
- Every sentence is true against the page in that state:
  - The card `<h2>Ganti master</h2>` and the button `Promote…` (`web/templates/master_detail.html:95-112`, heading
    :98, button :102) have no condition around them, so they are on the master page whenever the box is. Same
    spelling, same ellipsis character.
  - In that state the card is in emergency mode: `promoteEmergency` returns true for `monitor.StatusDown`
    (`internal/panel/web/handlers_promote.go:738-747`, used at `handlers_mysql.go:546`), so a freshly loaded page shows
    the badge "mode darurat" and the paragraph "Promote darurat: master tidak terjangkau …" (:99, :103-104).
  - The box can only be rendered with role `master`: besides the master page, `status-full` is used on the replica
    page (role replica) and in the old-master box of the server page, which exists only for role `quarantined`
    (`handlers_oldmaster.go:565-566`, `orchestrator/approval.go:80-82`). So "di halaman ini" is the master page.
  - The link target exists: `web/templates/panduan.html:14` `<h2 id="promote-darurat">`. Guide step 2 says the same
    as the box ("Buka halaman master, lalu tekan Promote…").
- It still checks the master first: step 1 of the box, and step 1 of the linked guide ("Pastikan master lama
  benar-benar mati atau tidak lagi dipakai aplikasi …", `panduan.html:17`), plus the guide's "Lanjutkan hanya bila
  tertulis Mode: Darurat". Nothing in the box invites turning read-only off or a promote outside the panel.
- README (`README.md:1057-1061`) says the same as the box; the old "langkah manual … panel belum bisa memindahkan
  peran master sendiri" is gone.
- Tests: `TestMasterPageDownPointsToEmergencyPromote` (`internal/panel/web/web_monitor_test.go:256`) asserts the new
  sentences, the link, the absence of `RESET REPLICA`, `STOP REPLICA`, `SET PERSIST`, `read_only`, `secara manual`,
  `panel belum bisa`, `versi berikutnya`, `Executed_Gtid_Set` between the status block and "Kontrol", and that the card
  and button are on the page; the healthy-master half is kept. `TestNoTemplatePromotesByHand` (:680) scans every
  template for `RESET REPLICA ALL`, `master secara manual`, `panel belum bisa`.
- Owner's list: both S1 texts are in the plan file, section "Teks baru untuk dibaca pemilik" → "Gelombang perbaikan
  audit keamanan", marked "tidak ada di rencana", old and new wording word for word (commit d01422b).

"No other place" — my own search at head (outside `docs/`, `graphify-out/`, tests):

| Hit | What it is | Judgement |
|---|---|---|
| `RESET REPLICA ALL`: only `internal/agent/drivers/mysql/promote.go:39,491,640,645`, `replication.go:670` | agent code, comments, one job-log line ("konfigurasi replikasi dihapus (RESET REPLICA ALL)") | not advice; fine |
| "secara manual": `README.md:700,3002`, `panduan.html:24` | replaying a salvage SQL file | not promotion |
| "secara manual": `install.go:403`, `realhost.go:160,171` | install MySQL / repair my.cnf block | not promotion |
| "belum bisa": `README.md:1046,1893,2894`, `monitor/explain.go:93`, `monitor/promote.go:398`, `monitor/status.go:220`, `orchestrator/promote_view.go:102`, `snapshot.go`, `cli_admin.go`, `cli_dropdb.go`, `configure.go:434`, `leftover.go:115`, `master_detail.html:59` | states of the panel or agent ("belum bisa dipastikan/dihapus/diukur/mengarantina") | none says the panel cannot promote; fine |
| "dengan tangan": `README.md:775-776, 2711, 2922, 3094, 3327, 3331, 4141`, `cli.go:351`, `panduan.html:47` | releasing the start lock by hand (guide 5 and its sources) | untouched by rule |
| "dengan tangan": `README.md:1814` | says an admin who opens an old master by hand is locked again; points to `set-role` | the opposite of an invitation; fine |
| "dengan tangan": `README.md:2573`, `4289`, `4410` | grant-admin replaces hand SQL; audit forwarding mark; a test description | not promotion |
| `read_only=OFF`: `README.md:2707, 2873-2874, 3286-3287, 3426`, `panduan.html:59` and the block in guide 7 | guides 5 and 7 and their README sources, each under its condition ("server ini memang akan dipakai aplikasi sebagai master, pastikan master lain tidak lagi dipakai") | untouched by rule; confirmed unchanged (below) |
| `read_only=OFF`: `README.md:1387-1394`, `internal/panel/web/handlers_mysql.go:210-212` (flash `dilepas-henti`), `web/templates/replica_detail.html:218` | the detach texts (see below) | left by the implementer; acceptable for this wave, goes to the owner |
| `read_only=OFF` / `= OFF` in `internal/agent/**` | SQL the agent sends, and refusal texts ("server ini bisa ditulis (read_only=OFF) … promote ditolak") | not advice |
| `README.md:4397, 4423` | descriptions of e2e tests | not operator steps |
| Alerts and Telegram: `monitor/alerts.go:214,216` ("Master diduga tumbang: agent tidak terhubung ke panel." / "… MySQL tidak menjawab."), `notify/message.go:64`, `handlers_alerts.go:26` | no advice at all | fine |
| `unlock-master` / `set-role master` advice in panel texts (`orchestrator/promote.go:422`, `monitor/alerts.go:550,576,1085`, `handlers_oldmaster.go:658,662`, `promote.html:80`) | all about a master or an old master, never about opening a replica | fine |
| `web/static/app.js`, `scripts/*.sh` | no hit | — |

Guides 5 and 7: `web/templates/panduan.html` is not in the range diff at all; `README.md` has exactly two hunks
(:1054 for S1, :2891 for S4's "Promote" paragraph). Commands and conditions of both guides and of their README sources
are byte-for-byte as audited.

The detach texts (README :1387-1394, `handlers_mysql.go:210-212`, `replica_detail.html:218`): leaving them in this
wave is acceptable. They do not tell the operator to promote a replica of a down master: they describe a server that
the operator has just removed from the topology through "Lepas replika" (typed name and password confirmation), they
do not say "master" or "arahkan aplikasi", and the wave's ruling limited S1 to texts that tell the operator to promote
by hand. But the owner should know what is left: this is now the only place where the panel itself prints
`SET PERSIST super_read_only=OFF; SET PERSIST read_only=OFF` for a replica with no condition, the detach is offered
whenever the replica is not busy (`handlers_mysql.go:1164`), also while the master is "Diduga tumbang", and the panel's
"stop" variant runs `STOP REPLICA; RESET REPLICA ALL` for the operator. An operator who cannot get the emergency
promote through (WB-2's lock-out, or the agent's refusal because the master still answers from the replica — exactly
the case where a second master is dangerous) can reach the removed box's end state by this route, and the old master
is not quarantined. Guides 5 and 7 carry the condition "pastikan master lain tidak lagi dipakai"; these three texts do
not. Suggested to the owner (new operator text, three places, one sentence): add that condition and a pointer to
promote, e.g. "Buka hanya bila master lama tidak lagi dipakai aplikasi; untuk mengganti master pakai Promote."

### S3 (WB-3, Sedang) — ADDRESSED

- The rule now (`internal/panel/web/handlers_auth.go:169-183`, condition at :176):
  `src == "" || err != nil || u.Scheme != "https" || u.Host == "" || u.Host != r.Host` → 403 `msgForbidden`, where
  `src` is `Origin`, or `Referer` when `Origin` is absent. It is the auditor's fix exactly ("https always"), not
  "same scheme as the request"; nothing else in the check changed, so nothing became weaker.
- Callers: one, `post` at `internal/panel/web/router.go:188`, used by `POST /login`, `POST /logout` (:193-194) and,
  through `authedPost`, every other POST route (:196-262), including `/confirm` and this plan's three routes
  (`/servers/{id}/backup` :211, `/settings/panel-key-backed-up` :254, `/settings/audit-forwarded` :255).
- No legitimate request now fails:
  - Plain-HTTP side of the port: `httpHelperHandler` (`internal/panel/tlsui.go:902-913`) passes only GET/HEAD of
    `/sertifikat`, the download and `/static/` to the UI handler; everything else is a 301. No POST reaches
    `sameOrigin` over plain HTTP, and there is no plain-HTTP mode of the UI (`tlsui.go:21`).
  - Reverse proxy: README :4244 says a reverse proxy in front of the panel is not supported. A TLS-terminating one
    would still deliver `Origin: https://…`.
  - Login, logout, forms, the `/confirm` fetch, pages opened by IP address: all are sent from a page of
    `https://<host:port>`, so `Origin` is `https://<host:port>` and equals `Host` (the panel's `Referrer-Policy:
    same-origin` does not turn `Origin` into `null`, `router.go:297-301`).
  - No script or helper of the repository posts to the UI with an http origin (searched `Origin`/`Referer` in Go,
    shell and JS: only `handlers_auth.go:171,173`, `handlers_cert.go:248` (reads the Referer's path, unaffected) and
    tests; the integration and e2e clients use an `https://` base: `e2e_helpers_test.go:125`, `e2e_replica_test.go:89`,
    `panel_test.go:220…`, `tlsui_test.go:238…`).
- `Origin: null` → scheme "" → 403 as before. `Referer`-only: accepted when it is `https://<Host>/…` (positive case
  kept at `web_test.go:446-450`), refused when it is `http://<Host>/…` (new case).
- Tests (none weakened; each cross-origin list only grew, and the requests are now built over TLS):
  - `TestPostRejectsCrossOrigin` (`web_test.go:405`): `/login`, `/logout`, `/servers`, `/servers/1/token`,
    `/servers/1/delete` × {no header, evil, `null`, `http://example.com`, Referer `http://example.com/servers/1`} →
    403, body, no cookie, nothing created.
  - `TestCSRFOnNewRoutes` (`web_controls_test.go:547`): the control routes incl. snooze × {none, evil,
    `http://example.com`} → 403, no agent call, no audit entry.
  - `TestBackupRoutesNeedLoginAndSameOrigin` (`web_servers_test.go:139`): this plan's three routes × {none, evil,
    `null`, `http://example.com`} → 403, nothing stored.
  - `testOrigin` changed from `http://example.com` to `https://example.com` (`web_test.go:35`): required by the fix
    and only used by positive-path requests; not a weakening.
- Still unconfirmed, as in the audit: whether Firefox/Safari attached the cookie to the attack's POST. It no longer
  matters for this route of attack: the server refuses the request whatever the browser sends.

### S6 (WB-8, parts of WB-9, Rendah) — ADDRESSED

- Static errors: `cacheFor` (`internal/panel/web/router.go:446-452`) hands the file server a `noStoreOnError` writer
  (:456-463) whose `WriteHeader` sets `Cache-Control: no-store` when the status is ≥ 400. Go 1.26's file server
  deletes Cache-Control and then calls `http.Error` on the writer it was given (`net/http/fs.go:189-213`), so the
  wrapper's `WriteHeader` runs after the deletion. The rest of the set comes from `SecurityHeaders` as before.
  - It cannot change a successful answer: 200, 206, 301 and 304 pass with `public, max-age=3600` untouched; an
    implicit 200 (first `Write`) never calls the wrapper's `WriteHeader` with an error code.
  - It strips nothing: it only sets one header. It hides the optional interfaces of the underlying writer
    (`Flusher`, `ReaderFrom`) for `/static/` only; the files come from the embedded FS, so there was no sendfile
    path to lose, and nothing under `/static/` flushes.
- Event stream: `internal/panel/web/sse.go:77` `no-store`; `TestSecurityHeaders` (`web_test.go:816`) and
  `TestSSEStreamsStatus` (:910) expect it.
- Permissions-Policy (`router.go:281-285`): the ten old features plus the fifteen listed, each as `name=()`,
  comma-separated: a valid structured-field dictionary; every name is a registered policy-controlled feature. None is
  used: `web/static/app.js` touches only `navigator.clipboard` (clipboard-write stays allowed), `theme.js` nothing,
  the templates have no `<audio>`, `<video>`, `<iframe>`, `autoplay` or `allowfullscreen`; modals are `<dialog>`, not
  the Fullscreen API; `local-fonts` is the Local Font Access API, not `@font-face`.
- Table test `TestSecurityHeadersOnEveryAnswerClass` (`internal/panel/web/web_headers_test.go:20-95`): 303 to /login,
  303 after a POST, 400, 401 / 200 / 429 of `/confirm`, 403 of `sameOrigin`, 403 of a refused confirmation, 405, 413,
  421 (wrapped as production wraps it, `internal/panel/servers.go:228`), `/alerts/count`, both fragments,
  `/jobs/{id}/log`, the salvage download, static 404 and `/static/`. Each answer is first checked for its status,
  then `wantHeaders` (`web_test.go:751-766`) compares all eight headers of `baseHeaders` with their exact values,
  `Cache-Control` with `no-store`, and that `Server` and (over plain HTTP) HSTS are absent. One header dropped or
  altered on one class fails the test for that class. `TestCertDownloadHeaders` (:99) does the same for the
  certificate download. The plain-HTTP 301 is in `internal/panel/tlsui_test.go:938` (see the note below).
- Not changed, as ruled: CSP (`router.go:273-275`, not in the diff), HSTS (:309, not in the diff), TLS minimum
  (`tlsui.go:417`, file not in the range), no `Clear-Site-Data` anywhere.

## New Breakage in the Fix Diff

No Kritis, Tinggi or Sedang. Two Rendah notes on the fix itself:

1. Rendah — `web/templates/_status.html:57` with `web/templates/master_detail.html:99,103-107` and
   `web/static/app.js:793-826`. Scenario: the master page was opened while the master was healthy, so the card
   "Ganti master" reads "mode terencana" and "Promote terencana: master hidup, jadi panel mengunci master …". The
   master goes down; only the status block is replaced live, so the new box appears and says "jalankan promote
   darurat: di kartu Ganti master di halaman ini, tekan Promote…" beside a card that still says planned mode. Nothing
   wrong can be done (the button opens the promote page, which decides the mode anew and the guide says to continue
   only under "Mode: Darurat"), but an operator who reads literally may hesitate. Before the fix the box did not
   refer to the card. Smallest fix: none needed for safety; if the owner wants it, add to step 3 "(muat ulang halaman
   bila kartu masih menulis mode terencana)", or drop the mode badge's claim by reloading the page on the status
   change. Owner's choice; new operator text either way.
2. Rendah (test strength) — `internal/panel/tlsui_test.go:938-962` `TestPlainHTTPRedirectHeaders` compares the 301's
   eight headers with those of a stub wrapped in the same `web.SecurityHeaders`, not with fixed values, and does not
   assert that `Server` is absent. It does fail if the redirect loses the wrapper or one header, and the exact values
   are pinned by `baseHeaders` in the web package, so the class is covered; it is only less direct than the table
   (`wantHeaders` is not reachable from that package). No change required.

## Out-of-Scope Observations

- The detach texts, for the owner (see S1): `README.md:1387-1394`, `internal/panel/web/handlers_mysql.go:210-212`,
  `web/templates/replica_detail.html:218`. The implementer's line numbers (README "about 1388", handlers "about 208")
  are a few lines off at head; the texts are the ones named.
- A master with no replica that is "Diduga tumbang" gets the same box; "Promote…" then leads to "Master ini belum
  punya replika, jadi tidak ada yang bisa dijadikan master baru." (`web/templates/promote.html:144`). True, a dead
  end, and no worse than the old box, which told that operator to look for a replica.
- A tab left open on a page of a server whose role changed since (a replica page of a server that was promoted and is
  now a down master) would receive the box through the status fragment, where "di halaman ini" is not true. A reload
  redirects it. Present before the fix for the old box as well.
- `X-Accel-Buffering: no` on the event stream (`sse.go:78`) is written for nginx, while the README says a reverse
  proxy is not supported. Harmless.

## Verdict

S1, S3 and S6 are all addressed, with no new Kritis/Tinggi (or Sedang) breakage; open for the owner are only the
unconditioned detach texts (left by ruling) and two Rendah notes. No test was run by me (the controller's full suite
was running).
