package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

// Confirmation actions (spec 5.7): each destructive post needs a token
// issued by POST /confirm for its action and target.
const (
	ActResync             = "resync"
	ActDetach             = "detach"
	ActDeleteServer       = "delete_server"
	ActRemoveDatabases    = "remove_databases"
	ActUnlockMaster       = "unlock_master"
	ActPrepareMasterAgain = "prepare_master_again"
	ActMySQLTLS           = "mysql_tls"
	ActTightenTLS         = "tighten_tls"
	// ActPromote starts the promotion of a replica of the master.
	ActPromote = "promote"
	// The quarantined old master after a promotion (spec 5.4): rebuild it
	// as a replica of the new master, download or delete a salvage file
	// (the only copy of its leftover data). Bound to the old master; a
	// salvage token is also issued for one file: its action is
	// salvageAction(ActSalvageDownload or ActSalvageDelete, name).
	ActRebuildOldMaster = "rebuild_old_master"
	ActSalvageDownload  = "salvage_download"
	ActSalvageDelete    = "salvage_delete"
	// The panel's HTTPS certificate (target kind settings, id 0): a session
	// alone must not swap the key the panel serves or widen its web CA.
	ActCertUpload = "cert_upload"
	ActCertRevert = "cert_revert"
	ActCertNames  = "cert_names"
	// Approve and Retry of a job that drops data: issued for one job, as
	// jobConfirmAction(act, jobID), and bound to the job's server.
	ActJobRetry   = "job_retry"
	ActJobApprove = "job_approve"
)

// Light actions (spec 5.1, "Jendela kata sandi"): inside the session's
// password window POST /confirm issues their tokens without a password,
// and their handlers take such a token (requireConfirmLight). Every other
// action is heavy and asks for the password each time.
const (
	ActRepairToken   = "repair_token"      // server (paired)
	ActMasterSource  = "master_source"     // master
	ActPrepareMaster = "prepare_master"    // server: the first "Siapkan sebagai master"
	ActConnectMySQL  = "connect_mysql"     // server with a stored account
	ActStopReplica   = "stop_replica"      // replica
	ActTelegram      = "telegram_settings" // settings, target id 0
)

// lightActions are the actions whose token may come through the window.
var lightActions = []string{ActRepairToken, ActMasterSource, ActPrepareMaster, ActConnectMySQL, ActStopReplica,
	ActTelegram}

// passwordWindow is how long a proved password spares the session's light
// actions the password: from the login or the last right password in
// /confirm; using the window does not extend it.
const passwordWindow = 10 * time.Minute

// jobConfirmAction is the confirmation action of act on job jobID.
func jobConfirmAction(act, jobID string) string { return act + ":" + jobID }

// jobActions are the confirmActions issued only for one job, as
// jobConfirmAction(action, jobID), with target kind auditJob and the job's
// server as target id.
var jobActions = []string{ActJobRetry, ActJobApprove}

// confirmActions are the actions POST /confirm issues tokens for.
var confirmActions = []string{ActResync, ActDetach, ActDeleteServer, ActRemoveDatabases, ActUnlockMaster,
	ActPrepareMasterAgain, ActMySQLTLS, ActTightenTLS, ActPromote, ActCertUpload, ActCertRevert, ActCertNames,
	ActRebuildOldMaster, ActSalvageDownload, ActSalvageDelete, ActRepairToken, ActMasterSource, ActPrepareMaster,
	ActConnectMySQL, ActStopReplica, ActTelegram}

// fileActions are the confirmActions issued only for one salvage file,
// as "<action>:<name>" (spec 5.4: the token is issued for that file).
var fileActions = []string{ActSalvageDownload, ActSalvageDelete}

// salvageAction is the confirmation action of act on salvage file name.
func salvageAction(act, name string) string { return act + ":" + name }

// confirmAction checks an action posted to /confirm: one of
// confirmActions, a fileActions one only with a valid salvage file name, a
// jobActions one only with a well-formed job id (jobIDPattern; anything
// else never reaches a lookup or a log line).
func confirmAction(action string) bool {
	base, name, perFile := strings.Cut(action, ":")
	if slices.Contains(fileActions, base) {
		return perFile && orchestrator.ValidSalvageName(name)
	}
	if slices.Contains(jobActions, base) {
		return perFile && jobIDPattern.MatchString(name)
	}
	return !perFile && slices.Contains(confirmActions, action)
}

// isJobAction reports whether action is a jobActions one (for one job).
func isJobAction(action string) bool {
	base, _, _ := strings.Cut(action, ":")
	return slices.Contains(jobActions, base)
}

// settingsActions are the actions bound to the settings (target id 0)
// rather than to a server.
var settingsActions = []string{ActCertUpload, ActCertRevert, ActCertNames, ActTelegram}

// confirmKinds are the target kinds a token can be bound to.
var confirmKinds = []string{auditServer, auditMaster, auditReplica, auditSettings, auditJob}

const (
	// confirmTTL is how long a token can be used.
	confirmTTL = 2 * time.Minute
	// confirmTokenBytes is the token's random length.
	confirmTokenBytes = 32
)

const (
	msgConfirmInvalid  = "Konfirmasi tidak berlaku; ulangi konfirmasi."
	msgConfirmBadInput = "Permintaan konfirmasi tidak dikenal."
	msgConfirmPassword = "Kata sandi salah."
	// msgNeedPassword is P8: /confirm without a password, for a heavy
	// action or outside the session's password window.
	msgNeedPassword = "Kata sandi panel diperlukan."
	// msgConfirmTooMany is T1 (spec 5.11): %d is the minutes until the
	// session may try again.
	msgConfirmTooMany = "Terlalu banyak kata sandi salah di sesi ini; coba lagi dalam %d menit, atau keluar lalu login lagi."
	// outcomeRateLimited is the audit outcome of a confirmation refused by
	// the limiter.
	outcomeRateLimited = "rate_limited"
	// outcomeConfirmRejected is the audit outcome of a destructive post
	// whose token was missing, wrong, used or expired.
	outcomeConfirmRejected = "token_invalid"
)

// confirmHash is the stored form of a token: its SHA-256 in hex.
func confirmHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// issueConfirm checks the session user's password and answers a one-time
// token for the posted action and target: 200 {"token": ...}, 400 for an
// unknown action or target, 401 for a wrong password, 429 when the
// session's attempts are used up. The password and the token are never
// audited or logged. A right password opens the session's password window
// again (MarkVerified).
//
// Without a password (spec 5.1, "Jendela kata sandi"): a light action or
// a job action, while the session's window is open, gets a token marked
// ViaWindow (audited with "window": true); a heavy consumer refuses it
// when it is used, and whether a job action is heavy is decided from the
// job then. Anything else is 401 {"error": P8, "need_password": true},
// which touches no budget: no password was tried.
//
// The budget is the session's own (ConfirmLimiter and ConfirmLongLimiter,
// keyed by sessionKey; a right password empties both), so wrong logins
// from outside never refuse the logged-in admin (R-181). A wrong password
// here still counts against the login's per-username limit (UserLimiter),
// which holds off logins without a device cookie, but that limit is not
// read here.
func (a *app) issueConfirm(w http.ResponseWriter, r *http.Request) {
	username := auth.UsernameFrom(r.Context())
	action, kind := r.PostFormValue("action"), r.PostFormValue("target_kind")
	id, err := strconv.ParseInt(r.PostFormValue("target_id"), 10, 64)
	settings := slices.Contains(settingsActions, action)
	if err != nil || !confirmAction(action) || !slices.Contains(confirmKinds, kind) ||
		settings != (kind == auditSettings) || isJobAction(action) != (kind == auditJob) ||
		(settings && id != 0) || (!settings && id <= 0) {
		writeJSONError(w, http.StatusBadRequest, msgConfirmBadInput)
		return
	}
	detail := map[string]any{"action": action, "target_kind": kind, "target_id": id}
	sessionToken := auth.TokenFrom(r.Context())
	if r.PostFormValue("password") == "" {
		windowAction := slices.Contains(lightActions, action) || isJobAction(action)
		if !windowAction || !a.Sessions.WindowOpen(sessionToken, passwordWindow) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]any{"error": msgNeedPassword, "need_password": true})
			return
		}
		detail["window"] = true
		a.writeConfirmToken(w, r, username, action, kind, id, true, detail)
		return
	}
	sk := sessionKey(sessionToken)
	// Reserve a slot in both budgets before the slow password check, so
	// concurrent requests cannot all pass a check made before any of them
	// is counted (check-then-act). A wrong password keeps its slots; a
	// right one resets both below; a request that never reaches the check
	// gives its slots back.
	if !a.ConfirmLimiter.Allow(sk) {
		a.confirmRateLimited(w, r, username, sk, kind, id, detail)
		return
	}
	if !a.ConfirmLongLimiter.Allow(sk) {
		a.ConfirmLimiter.Refund(sk)
		a.confirmRateLimited(w, r, username, sk, kind, id, detail)
		return
	}
	user, err := a.Store.GetUser(username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.ConfirmLimiter.Refund(sk)
		a.ConfirmLongLimiter.Refund(sk)
		a.internalError(w, "get user", err)
		return
	}
	if err != nil || !a.CheckPassword(user.PasswordHash, r.PostFormValue("password")) {
		a.UserLimiter.Fail(usernameKey(username))
		a.audit(r, "confirm_failed", kind, id, detail, msgConfirmPassword)
		a.Logger.Info("confirmation refused: wrong password", "username", username, "action", action)
		writeJSONError(w, http.StatusUnauthorized, msgConfirmPassword)
		return
	}
	a.ConfirmLimiter.Reset(sk)
	a.ConfirmLongLimiter.Reset(sk)
	a.Sessions.MarkVerified(sessionToken)
	a.writeConfirmToken(w, r, username, action, kind, id, false, detail)
}

// writeConfirmToken stores a new token for username, action and target
// (viaWindow: issued without a password), audits confirm_issued with
// detail and answers 200 {"token": ...}.
func (a *app) writeConfirmToken(w http.ResponseWriter, r *http.Request, username, action, kind string, id int64,
	viaWindow bool, detail map[string]any) {
	raw := make([]byte, confirmTokenBytes)
	rand.Read(raw) // never fails (crypto/rand)
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := a.Now()
	err := a.Store.AddConfirmation(store.Confirmation{TokenHash: confirmHash(token), Username: username,
		Action: action, TargetKind: kind, TargetID: id, ExpiresAt: now.Add(confirmTTL), ViaWindow: viaWindow})
	if err != nil {
		a.internalError(w, "add confirmation", err)
		return
	}
	a.audit(r, "confirm_issued", kind, id, detail, auditOK)
	// Expired tokens can never be used; drop them.
	if _, err := a.Store.PruneConfirmations(now); err != nil {
		a.Logger.Warn("prune confirmations", "err", err)
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"token": token})
}

// confirmRateLimited answers 429 (T1) for a confirmation refused by a
// session budget, naming the whole minutes until both budgets accept the
// session again (at least 1).
func (a *app) confirmRateLimited(w http.ResponseWriter, r *http.Request, username, sk, kind string, id int64,
	detail map[string]any) {
	wait := max(a.ConfirmLimiter.RetryAfter(sk), a.ConfirmLongLimiter.RetryAfter(sk))
	minutes := max(1, int((wait+time.Minute-1)/time.Minute))
	a.audit(r, "confirm_failed", kind, id, detail, outcomeRateLimited)
	a.Logger.Warn("confirmation rate limited", "username", username)
	writeJSONError(w, http.StatusTooManyRequests, fmt.Sprintf(msgConfirmTooMany, minutes))
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// requireConfirm consumes the form's confirm_token for the session user,
// action and target, for a heavy action: a token issued through the
// password window is refused (and stays unused). Without a valid one it
// answers 403 with msgConfirmInvalid on the target's page and returns
// false; the handler must then do nothing else. Handlers call it before
// any other check.
func (a *app) requireConfirm(w http.ResponseWriter, r *http.Request, action, targetKind string, targetID int64) bool {
	return a.consumeConfirm(w, r, r.PostFormValue("confirm_token"), action, targetKind, targetID, false)
}

// requireConfirmLight is requireConfirm for a light action (lightActions):
// it takes a token issued with the password or through the window.
func (a *app) requireConfirmLight(w http.ResponseWriter, r *http.Request, action, targetKind string, targetID int64) bool {
	return a.consumeConfirm(w, r, r.PostFormValue("confirm_token"), action, targetKind, targetID, true)
}

// requireConfirmToken is requireConfirm for a token taken from elsewhere
// than the posted form: the salvage download is a GET whose token is in
// its query.
func (a *app) requireConfirmToken(w http.ResponseWriter, r *http.Request, token, action, targetKind string, targetID int64) bool {
	return a.consumeConfirm(w, r, token, action, targetKind, targetID, false)
}

// consumeConfirm consumes token for the session user, action and target;
// allowWindow says whether a token issued through the window will do.
func (a *app) consumeConfirm(w http.ResponseWriter, r *http.Request, token, action, targetKind string, targetID int64,
	allowWindow bool) bool {
	err := store.ErrConfirmInvalid
	if token != "" {
		err = a.Store.ConsumeConfirmation(confirmHash(token), auth.UsernameFrom(r.Context()), action, targetKind,
			targetID, a.Now(), allowWindow)
	}
	switch {
	case err == nil:
		return true
	case !errors.Is(err, store.ErrConfirmInvalid):
		a.internalError(w, "consume confirmation", err)
		return false
	}
	a.Logger.Warn("confirmation refused", "action", action, "target_kind", targetKind, "target_id", targetID,
		"by", auth.UsernameFrom(r.Context()))
	a.audit(r, "confirm_rejected", targetKind, targetID,
		map[string]any{"action": action, "target_kind": targetKind, "target_id": targetID}, outcomeConfirmRejected)
	a.confirmRefused(w, r, action, targetKind, targetID)
	return false
}

// confirmRefused shows msgConfirmInvalid with 403 on the page the
// action's button is on.
func (a *app) confirmRefused(w http.ResponseWriter, r *http.Request, action, targetKind string, targetID int64) {
	switch targetKind {
	case auditSettings:
		a.renderStoredSettings(w, r, http.StatusForbidden, msgConfirmInvalid)
		return
	case auditJob:
		// The buttons are on the job page of {id}.
		a.renderJob(w, r, http.StatusForbidden, msgConfirmInvalid)
		return
	}
	srv, err := a.Store.GetServer(targetID)
	if err != nil {
		http.Error(w, msgConfirmInvalid, http.StatusForbidden)
		return
	}
	switch {
	case action == ActRemoveDatabases:
		rep, err := a.Store.GetReplicaByServer(srv.ID)
		if err != nil {
			http.Error(w, msgConfirmInvalid, http.StatusForbidden)
			return
		}
		p, err := a.replicaDatabasesForm(r, srv, rep)
		if err != nil {
			a.internalError(w, "replica databases", err)
			return
		}
		p.Flash = msgConfirmInvalid
		a.renderDatabases(w, http.StatusForbidden, p)
	case action == ActTightenTLS:
		// The button is on the master page.
		a.showError(w, r, srv, http.StatusForbidden, msgConfirmInvalid)
	case targetKind == auditServer:
		a.renderServer(w, r, srv, serverView{Status: http.StatusForbidden, Flash: msgConfirmInvalid})
	default:
		a.showError(w, r, srv, http.StatusForbidden, msgConfirmInvalid)
	}
}
