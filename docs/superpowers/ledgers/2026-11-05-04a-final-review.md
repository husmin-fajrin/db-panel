# Final whole-branch review: Plan 4a (0d040d3..d785a4c)

Reviewer: opus, read-only. Graph query first (graphify), then passes over the review diff and the tree at d785a4c.

**What I read:**
- Read closely: `internal/panel/tlsui.go` (all of it), `panel.go`, `servers.go`, `localagent.go`, `cli.go`, `agentapi/pair.go`, `agent/pair.go`, `agent/localpair.go`, `web/handlers_confirm.go`, `store/confirmations.go`, `web/handlers_cert.go`, the `router.go` routes and headers, `auth/middleware.go`, `orchestrator/tls.go` (all), the `runner.go` diff and `Resume`, the `create_replica.go` and `prepare_master.go` diffs, the agent `drivers/mysql/tls.go` install, rollback and check paths, the `users.go`, `pins.go` and `preflight` diffs, the `monitor/alerts.go` diff, `scripts/install.sh` (all), and these templates: `layout`, `cert`, `_confirm`, `settings`, `master_detail`, `replica_detail`, `server_detail`, `agent-install.sh.tmpl`. I also read the `app.js` confirm modal and the fake `sudo` in `e2e_https_test.go`.
- Skimmed: the README hunks (HTTPS, update and security sections), `ca/web.go`, and the remaining tests.
- Not read: the SVG and CSS hunks, `install_test.sh`, `tlsui_test.go`, and the `dump.go`/`clone.go` internals.
- No tests run. The make test and e2e logs on d785a4c are taken as given.

## Strengths

- **Single-port HTTPS split.** The design is small and correct. Each connection gets its own peek goroutine with a 5 s deadline. `Close` also closes the connections still peeking. A connection routed to TLS gets its handshake deadline from `ReadHeaderTimeout` (Go's `tlsHandshakeTimeout`). The redirect runs behind `RequireHost`, so it cannot be used as an open redirect. HSTS is sent only when `r.TLS != nil`.
- **Certificate handling is careful.** Writes go through a temporary file and a rename. The key is written before the certificate. I/O errors stop the start instead of silently creating a new CA. Re-issues keep the key, so the pin stays stable. A custom certificate that is broken or expired falls back to the panel's own, never to plain HTTP.
- **Confirmation tokens are done right.** The token is 32 bytes, only its hash is stored, and it is consumed by a single conditional `UPDATE` (atomic, single use, bound to user, action and target). `requireConfirm` runs before any handler work. The modal is one shared component with no inline JS.
- **MySQL certificate install.** `installTLS` does rotation, `GLOBAL`, reload, check, then `PERSIST`. On failure it rolls back files and settings and reloads. The check pins the exact served certificate (`VerifyConnection` with `Equal`). SANs come only from what the panel knows (`certHosts`).
- **Orchestrator upgrade handling.** `matchStoredSteps`, `connectIndex` and `failUnrunnable` keep pre-upgrade jobs runnable or fail them cleanly. Tighten marks the replica paused while it is stopped (no false `replica_stopped`). An operator's own stop is respected.
- **`install.sh`.** It never chowns or reads through panel-owned paths as root (`as_panel`, `panel_read` with `head -c` and `timeout`). The new `bin/` is built in a root-owned stage directory and moved in with `mv -T`. `reset-local-agent` handles a lost local agent. The upgrade steps are each safe to repeat.
- **Pinned pairing.** The pin is checked in `VerifyPeerCertificate`, so the token is never sent to the wrong key. Redirects are refused. The token goes through stdin or the environment, never argv.

## Issues

### Critical

None.

### Important

**I1. The web CA is regenerated whenever a new interface subnet appears (docker, libvirt or VPN bridges).** `internal/panel/tlsui.go:297-303` (renew), `:175-177` (ensureCA), `:561-567` (webNames adds every up, non-loopback interface and its subnet).
- **Failure scenario:** The panel host runs Docker, or a VPN client, or someone runs `docker network create` (each one gets a new 172.x/16 bridge, which is up). The next daily renew sees an IP outside the CA's name constraints. It moves `web-ca.*` to `.old`, overwriting any older `.old`, and makes a new CA. Every computer that installed the certificate now gets warnings and must reinstall it. That happens again with every new bridge. None of these addresses is how anyone reaches the panel.
- **Fix:** Only names the operator chose may force a new CA: `PublicHost`, the machine host name, and the extra names. Leave discovered interface addresses that the current CA does not cover out of the leaf, with a log line. Constrain to interface subnets only when the CA is first created, as the spec says ("saat dibuat"). Add a test where a new interface subnet appears after the CA exists.

**I2. HSTS combined with a CA change or a custom-certificate fallback locks out users who open the panel by DNS name.** `web/router.go:266`, `tlsui.go:144-145,293-296`, the notice in `layout.html`, `README.md:820-823`, `cert.html`.
- **Failure scenario:** IT opens `https://panel.rs.local:8080` after installing the CA (or uploads a corporate certificate), so the browser records HSTS for that host. Later the CA is regenerated (I1, or a real address change), or the custom certificate expires and the panel falls back to its own. Per RFC 6797 §8.4, Chrome, Edge and Firefox now refuse with no "continue" option. `http://panel.rs.local:8080/sertifikat` is upgraded to https by HSTS, so the reinstall page and the "pasang ulang" notice cannot be reached under that name. Only an IP address works, since HSTS ignores IP literals, and nothing tells the user that.
- **Fix:** This is cheap and keeps the spec's HSTS. Put the IP way out into the notice and `/sertifikat`: "Bila browser menolak membuka panel lewat nama tanpa pilihan lanjut, buka http://&lt;IP-panel&gt;:8080/sertifikat". Add the same to the README. Add the I3 alert before a custom certificate runs out. Optionally send HSTS only once the panel has served the same CA/leaf key for a while; that is a judgment call, so keep the 1-year value.

**I3. `tls_expiring` does not cover the web certificate, and the fall-back from an expired custom certificate is silent.** Spec 5.14 says "sertifikat MySQL/web < 14 hari". `monitor/alerts.go` `CheckTLSExpiry` reads only `server_tls`. `tlsui.go:293-296` drops an expired custom certificate with only a log line.
- **Failure scenario:** The hospital's corporate certificate (1 year) expires. There is no alert or Telegram message beforehand. On the day, every browser that trusts only the corporate CA warns, and with I2 it hard-fails.
- **Fix:** Also open `tls_expiring` (server_id 0 or the local server; message "Sertifikat keamanan panel kedaluwarsa dalam N hari") when the active web certificate expires within 14 days. In practice that is the custom certificate; the panel's own one only alerts if renewal fails. Show a persistent notice after a fall-back, as "tidak pernah diam-diam" requires.

**I4. Agents from before this plan make every TLS step fail with a cryptic error. Recommendation: fail clearly, skip only where the spec already tolerates having no certificate.** `orchestrator/tls.go:148-205` (`tlsRequest`/`tlsInstall`), `:244-256` (`trustCA`), `prepare_master.go` step 2, `create_replica.go` `replicaCert`.
- **Failure scenario:** After the panel update, before any agent is reinstalled, "Siapkan master" fails after "Terapkan pengaturan". "Buat replika" and "Resync ulang" (repairing a broken replica) fail after the data copy, with "perintah tidak dikenal: mysql.tlsRequest". Lay IT staff get no hint. The monitor does not mark these agents "perlu dimutakhirkan" (it only reacts to an unknown `monitor.configure`), and the README note is the only guidance.
- **Judgment:**
  - (a) In `tlsRequest`/`tlsInstall`/`trustCA`, map `proto.CodeUnknownCommand` to "Agent di &lt;server&gt; versi lama dan belum mengenal sertifikat MySQL. Klik Pasang ulang agent di halaman server itu, jalankan perintahnya, lalu Ulangi." Also mark the server NeedsUpgrade, as the monitor does.
  - (b) In "Siapkan master" and in the replica's own certificate step (`replicaCert`, when TLSVerify is false), skip with a warning in the job log and continue. The result is a topology without certificates, which the spec already handles ("Sambungan replikasi belum diverifikasi", the Perketat banner).
  - (c) Never skip `trustCA` when the master has a certificate (TLSVerify): silently dropping verification would be wrong, so (a) applies.
  - Test with a fake agent that answers `unknown_command`.

**I5. Perketat can leave a replica stopped, on a configuration it cannot connect with.** `orchestrator/tls.go:440-458`.
- **Failure scenario:** `configureReplication` with `SOURCE_SSL_VERIFY_SERVER_CERT=1` succeeds. Then `startReplica` fails because `awaitStarted` sees `LastIOErrno` (name or chain mismatch, or a pre-existing applier error on an "attention" replica). The step returns the error and the replica stays paused and stopped. The previous, unverified `CHANGE REPLICATION SOURCE` is gone, so "Start replika" fails the same way. The code's own comment "Replication goes on as it was configured before" holds only when `configureReplication` itself fails.
- **Fix:** When start fails after a verified CHANGE, reconfigure with the previous parameters (`SSL: true`, no CA, no verify; the replica row has everything needed), start, unpause, and fail the step with "verifikasi gagal: <IO error>; replikasi dikembalikan tanpa verifikasi". Add a fake-agent test for the start failure.

**I6. A panel restart during a Perketat redirect leaves the replica stopped and marked paused, which suppresses `replica_stopped`.** `orchestrator/runner.go:643-693` (Resume) vs `tls.go:428-435`.
- **Failure scenario:** A crash, an `install.sh` upgrade, or a systemd restart hits between STOP REPLICA and START. Resume fails the job but does not do what `cancelJob` does (`restartReplica`). The replica stays `paused=1`, so it shows "Dihentikan" (stopped by the panel), and no `replica_stopped` alert ever fires. Only the one-time `job_failed` alert remains. Lag grows silently until someone clicks Ulangi.
- **Fix:** In Resume, for `KindTightenTLS` with `st.TightenPaused != 0`, run the same best-effort `restartReplica`. If the agent is offline, at least `SetReplicaPaused(false)` so the normal stopped alert works. This is in the ledger as a deferred minor; promote it.

**I7. Installing a panel MySQL certificate replaces the server-wide `ssl_ca`, which can lock out applications that log in with client certificates.** Agent `drivers/mysql/tls.go` `installTLS` (sets `ssl_ca` to `dbpanel-tls/mysql-ca.crt` with GLOBAL then PERSIST).
- **Failure scenario:** A production master has application accounts `REQUIRE X509` / `ISSUER` / `SUBJECT`, with client certificates from the hospital's own CA. "Siapkan master", "Pasang sertifikat MySQL" or Perketat runs. New application connections fail the client-certificate check, which is an outage. The post-install check uses the admin account, so nothing notices.
- **Fix:** Before swapping, query `mysql.user` for `ssl_type IN ('X509','SPECIFIED')`. If there are any, and the old `ssl_ca` is set and differs, refuse with a clear message: "akun X memakai sertifikat klien; …". Alternatively, write a separate server-side bundle of the old CA plus the panel CA for `ssl_ca`, keeping `mysql-ca.crt` (used for `SOURCE_SSL_CA`) as the panel CA only. Add a README line either way. This is in the ledger as a deferred README minor; promote it with a code guard.

**I8. After the master's address is changed, the next Buat replika or Resync fails mid-job on a TLS name error.** `orchestrator/tls.go:210-229` (`certHosts`, issued once), `web/handlers_mysql.go:357-387` (`setMasterSource` does not reissue), `create_replica.go` `replicaCheckMaster`.
- **Failure scenario:** IT changes "Alamat master" (Plan 3 flow: NAT, VPN, DNS name). The master's certificate does not hold the new name. TLSVerify is true, so the copy runs `--ssl-mode=VERIFY_IDENTITY` against the new host. The job fails at "Salin data", after MySQL was installed on the target, with a certificate-name error.
- **Fix:** Store the SANs in `server_tls`, or recompute them from the stored cert. In `replicaCheckMaster`, when TLSVerify and the master's certificate does not cover `topo.SourceHost`, either run the master's certificate steps inline or refuse at step 0: "Alamat master berubah; klik Perbarui sertifikat MySQL di halaman server master." After `setMasterSource` on a master with `server_tls`, show the same hint.

**I9. Rejected confirmation tokens are not audited, and the limiter semantics differ from the ruling.** `web/handlers_confirm.go:80-96` (`Allow` before the password check counts every attempt), `:139-141` (refusal only logged).
- **Detail:** Both are controller rulings carried into this wave: count failed passwords only and reset on success; write `confirm_rejected` (action, target, outcome) on a refused or expired token. Without the audit row, a replayed or forged destructive POST leaves no trace in the audit log, only in the journal.
- **Fix:** As ruled. Update the README limiter wording to match, and add a per-user limiter test.

**I10. The fake `sudo` in TestE2EHTTPS protects the host by a denylist.** `internal/panel/e2e_https_test.go:196-213`.
- **Failure scenario:** The tests run as uid 0 in CI and in this sandbox. The stub rewrites three exact strings and then greps only for `^BIN=/usr/` and `>/etc/`. A future template line such as `install -m0600 x /etc/dbpanel/…`, `mkdir -p /etc/dbpanel`, `tee /etc/…`, `cp … /usr/local/bin`, `>> /etc/…` or `> "/etc/…"` passes the check. That line then runs for real, as root, on the developer's or CI host.
- **Fix for the fix wave:**
  - (1) In `agent-install.sh.tmpl`, put every absolute path in variables at the top: `BIN=`, `UNIT_FILE=`, `CONFIG_DIR=`. The `pair` call gets `--dir "$CONFIG_DIR"`. The stub then rewrites only those assignment lines.
  - (2) Replace the denylist with an allowlist. After rewriting, strip the quoted unit heredoc body and fail if any `/etc/`, `/usr/`, `/var/` or `/opt/` literal remains outside the variable block.
  - (3) Defence in depth: when the test's euid is 0, have the stub run the script without root's powers, e.g. `exec setpriv --reuid=65534 --regid=65534 --clear-groups bash "$ROOT/install.sh"` (keep the `id` stub so the script's root check passes), or `unshare -r` where available. A missed path then fails with EACCES instead of writing.
  - (4) Skip with a clear message when neither is available and euid is 0.
  - Apply the same to `scripts/install_test.sh`, which already uses `ROOT=`; check that its runs cannot fall back to an empty `ROOT`.

### Minor

- **M1.** `web/handlers_mysql.go:264-266`: the Perketat token is bound to `target_kind=server` (template `master_detail.html` `data-confirm-kind="server"`), but its audit row uses `master`. Unlock uses `master` for both. Pick one convention before 4b adds the promote actions.
- **M2.** `cli.go` `resetLocalAgent` keeps `address` (`127.0.0.1` on a legacy embedded-agent row). `localagent.go:46-52` then no longer resets it to PublicHost, because `cert_serial` is now empty. That is harmless once the agent pairs, but it is wrong if pairing never happens. Set the PublicHost setting there too, or let `ensureLocalServer` also fix `address == 127.0.0.1` on the local row.
- **M3.** `agent/localpair.go:22`: `localPanelURL` is hard-coded to `https://127.0.0.1:8080`. A panel started with `--http :9443` writes a token the local agent can never use. Add the URL as a third line of `local-pair-token`.
- **M4.** `tlsui.go:504-531`: an uploaded certificate is not checked for serverAuth EKU, a CA flag, or key usage, so a cert that browsers reject is accepted. `coversHost` (`:326-335`) turns a wildcard custom certificate into a wildcard Host allowlist, which weakens the DNS-rebinding guard to that domain. (The ledger already lists the first part as deferred.)
- **M5.** `cert.html`/`settings.html`: the CA's SHA-256 fingerprint is not shown. Users install a trusted root downloaded over plain HTTP, and there is nothing to compare against out of band. (Deferred in the ledger.)
- **M6.** `web/handlers_cert.go:89`: `io.LimitReader(…, 64 KiB)` silently truncates a long chain, which then reports as "kunci tidak cocok / bukan PEM". Say "berkas terlalu besar" instead. The imports in this file are not grouped (stdlib and module mixed).
- **M7.** `orchestrator/tls.go:303-318`: `refuseBusy` runs only at start. A resync started on a replica during its Perketat redirect races with it (STOP/CHANGE/START from two jobs). Mark the topology's replicas busy for the job's lifetime, or check at each replica step. (Deferred in the ledger; low probability.)
- **M8.** `listener_impl.go:47` and `servers.go:33`: the comment lines were not reflowed (over 100 columns).
- **M9.** Spec 5.10 says the Buat replika page should show `set-source` when the pinned source differs. Today it only appears in the agent's refusal (job error) and on the master page. This needs `pin_source` (4b, 5.13), so it belongs to 4b.

## Triage of ledger deferred minors and rulings

| Ledger item | Decision |
|---|---|
| T2 ruling: limiter counts failed attempts only, reset on success; `confirm_rejected` audit | **Fix before merge** → I9 |
| T2: auditKeptKeys global map; throttled requests each write an audit row; double replica lookups; replicaError duplicate; per-user limiter test gap | Later (4c audit hardening); test gap goes with I9 |
| T2: README limiter wording | Fix with I9 |
| T2: tokens issuable for actions without consumers | Resolved: `confirmActions` lists only consumed actions |
| T2: modal browser test | Done (T9, `e2e_browser` tag) |
| T1: cryptic test hash; prune only expired; used_at-unset test | Later |
| T3: grantable ignores role-held privileges (spurious RELOAD fallback) | Later (4c); visible in the job log already |
| T3: first sweep wasted; sync sweep delays a check ≤1 min; extra admin connection; English sweep log line | Later |
| T4: excluded-subtree placeholder style; IPv6 constraint tests; subdomain doc; MySQL host validation; file modes; CN > 64; leafNotAfter cap test; Kelvin-sign lowercasing; IP literal as DNS; co.uk suffixes | Later |
| T5: CA fingerprint on /sertifikat | Later (M5), but cheap; worth doing alongside I2's page edit |
| T5: EKU/CA check on upload | Later (M4) |
| T5: routeGoroutines flake risk; no concurrent TLS-while-peeking test; pre-existing agentapi flake; tlsui.go split; imports; CertNotice settings reads | Later |
| T5: ensureCA moves a valid CA to .old before failing on a name error; no dir fsync | Later, but the I1 fix touches this path; avoid the double `.old` overwrite there |
| T6: no name-mismatch e2e | Done (T9) |
| T6: CLONE chain-only in README | Done (README) |
| T6: CloneFrom overwrites operator clone_ssl_ca | Later |
| T6: ssl_ca repoint affects REQUIRE X509 accounts | **Fix before merge** → I7 |
| T6: rollback without deadline under tlsMu | Later (add a 30 s deadline when convenient) |
| T6: failed renewal loses older *.prev; stale key.new message; TCP fallback to loopback; dir fsync; driver noise | Later |
| T7: source-address change leaves cert without new name | **Fix before merge** → I8 |
| T7: tlsInstall serial not compared; StartTightenTLS skips inventory/grant check; Stop-during-redirect race; checkTLSExpiry under m.mu; day-count publish; TLSVerify set without save in addDump; test gaps; re-issue on every re-run | Later (the tighten failure-path test comes with I5) |
| T7 round 2: matchStoredSteps compares count only | Later (only kinds that gained exactly one step exist) |
| T7 round 2: failUnrunnable doesn't set replica state | Later |
| T7 round 2: TightenPaused left by crash/skip | **Fix before merge** → I6 |
| T7 round 2: tls_verified=1 before IO thread verifies (replicaConnect) | Later: the job fails if start fails, and resync rewrites it |
| T8: token-file ignores --ca-sha256; auto-pair skips ca.key guard; reset-password as root; chown -R hardlink note; unit hardening; test realism; real ports in TestPanelRefusesRootWithoutFlag; stale comments | Later |
| T8: stale token after panel restart before pairing | Later (install.sh warns; consider rewriting the token on a timer while unpaired) |
| T8: move_old chown-before-mv residue; runuser not checked; secret copied into bin/ 0644; raw password printed (escape sequences) | Later; `tr -cd '[:print:]'` on the printed password is a one-liner worth taking |
| T9: fake sudo denylist (ruled into final wave) | **Fix before merge** → I10 |
| T9: TestE2EHTTPS under make test; curl (90) text dependency | Later |
| Pre-plan agents "perintah tidak dikenal" (T9 concern) | **Fix before merge** → I4 |

## Declined to judge

- **Promote, rebuild old master, salvage download/delete tokens, promotions table, quarantine and repoint states, pin_role/pin_source columns.** Plan 4b, per the plan's scope.
- **Audit hash chain, retention settings, `master_unpinned`.** Plan 4c / spec 5.12.
- **`AmbientCapabilities=CAP_NET_BIND_SERVICE`.** No port configuration exists yet; the spec makes it conditional.
- **Requiring a confirmation token for uploading a custom HTTPS certificate or changing extra names.** The spec lists "simpan pengaturan" as tokenless and does not list these. Worth raising for 4c, since a session can swap the served key.
- **The HSTS max-age value and HSTS itself.** The spec mandates 1 year; I2 only adds a way out.
- **Signed releases and SHA256SUMS from the same release.** Pre-existing and documented.
- **CA/Browser-Forum style of the excluded-subtree placeholders.** A private CA; the ledger already accepted it.
- **CLONE verifying the donor chain only, not its name.** A MySQL limitation, documented.
- **Behaviour on MySQL 5.7 or 8.4 beyond the tested 8.0.46 harness** (e.g. `RESET BINARY LOGS AND GTIDS` syntax). The harness runs only 8.0.46.
- **Whether the SVG illustrations match each OS's current menus.** I cannot verify this here.
- **Performance of the per-tick `CheckTLSExpiry` DB reads under the monitor lock.** Tens of servers at most.
- **The pre-existing `TestWSLogsReplacedConnection` flake.** Not introduced by this branch.
- **The README statement that the agent listener certificate is re-made on each start** (`README.md:921-925`). It is about port 8443, which is untouched here.

## Verdict

**Not ready to merge as-is; ready after one fix wave.** There are no Critical issues and the core is sound: confirmations, the split listener, pinned pairing, the TLS install with rollback, and the non-root layout. The ten Important items are mostly small, localised fixes:
- **Lock-out and topology safety:** I1, I2, I5, I6, I7.
- **Upgrade and operability:** I3, I4, I8.
- **Already-ruled carry-overs:** I9, I10.

I1, I4 and I5 should land before Plan 4b builds promote on top: promote reuses the verified-repoint path and the agent-command assumptions.
