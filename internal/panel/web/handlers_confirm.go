package web

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
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

// jobConfirmAction is the confirmation action of act on job jobID.
func jobConfirmAction(act, jobID string) string { return act + ":" + jobID }

// jobActions are the confirmActions issued only for one job, as
// jobConfirmAction(action, jobID), with target kind auditJob and the job's
// server as target id.
var jobActions = []string{ActJobRetry, ActJobApprove}

// confirmActions are the actions POST /confirm issues tokens for.
var confirmActions = []string{ActResync, ActDetach, ActDeleteServer, ActRemoveDatabases, ActUnlockMaster,
	ActPrepareMasterAgain, ActMySQLTLS, ActTightenTLS, ActPromote, ActCertUpload, ActCertRevert, ActCertNames,
	ActRebuildOldMaster, ActSalvageDownload, ActSalvageDelete}

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
var settingsActions = []string{ActCertUpload, ActCertRevert, ActCertNames}

// confirmKinds are the target kinds a token can be bound to.
var confirmKinds = []string{auditServer, auditMaster, auditReplica, auditSettings, auditJob}

const (
	// confirmTTL is how long a token can be used.
	confirmTTL = 2 * time.Minute
	// confirmTokenBytes is the token's random length.
	confirmTokenBytes = 32
	// Default POST /confirm limit per username when Deps.ConfirmLimiter
	// is nil.
	defaultConfirmMax    = 5
	defaultConfirmWindow = time.Minute
)

const (
	msgConfirmInvalid  = "Konfirmasi tidak berlaku; ulangi konfirmasi."
	msgConfirmBadInput = "Permintaan konfirmasi tidak dikenal."
	msgConfirmPassword = "Kata sandi salah."
	msgConfirmBlocked  = "Terlalu banyak percobaan konfirmasi; coba lagi dalam 1 menit."
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
// user's attempts are used up. The password and the token are never
// audited or logged.
//
// Only wrong passwords count against the user (ConfirmLimiter, reset by a
// right one), and they share the login's per-username budget
// (UserLimiter): a stolen session cannot guess the password here faster
// than at the login page, and failures here also hold off logins.
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
	userKey := usernameKey(username)
	// Reserve a slot in both budgets before the slow password check, so
	// concurrent requests cannot all pass a check made before any of them
	// is counted (check-then-act). A wrong password keeps its slots; a
	// right one resets both below; a request that never reaches the check
	// gives its slots back.
	if !a.ConfirmLimiter.Allow(username) {
		a.confirmRateLimited(w, r, username, kind, id, detail)
		return
	}
	if !a.UserLimiter.Allow(userKey) {
		a.ConfirmLimiter.Refund(username)
		a.confirmRateLimited(w, r, username, kind, id, detail)
		return
	}
	user, err := a.Store.GetUser(username)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		a.ConfirmLimiter.Refund(username)
		a.UserLimiter.Refund(userKey)
		a.internalError(w, "get user", err)
		return
	}
	if err != nil || !a.CheckPassword(user.PasswordHash, r.PostFormValue("password")) {
		a.audit(r, "confirm_failed", kind, id, detail, msgConfirmPassword)
		a.Logger.Info("confirmation refused: wrong password", "username", username, "action", action)
		writeJSONError(w, http.StatusUnauthorized, msgConfirmPassword)
		return
	}
	a.ConfirmLimiter.Reset(username)
	a.UserLimiter.Reset(userKey)
	raw := make([]byte, confirmTokenBytes)
	rand.Read(raw) // never fails (crypto/rand)
	token := base64.RawURLEncoding.EncodeToString(raw)
	now := a.Now()
	err = a.Store.AddConfirmation(store.Confirmation{TokenHash: confirmHash(token), Username: username,
		Action: action, TargetKind: kind, TargetID: id, ExpiresAt: now.Add(confirmTTL)})
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

// confirmRateLimited answers 429 for a confirmation refused by a limiter.
func (a *app) confirmRateLimited(w http.ResponseWriter, r *http.Request, username, kind string, id int64,
	detail map[string]any) {
	a.audit(r, "confirm_failed", kind, id, detail, outcomeRateLimited)
	a.Logger.Warn("confirmation rate limited", "username", username)
	writeJSONError(w, http.StatusTooManyRequests, msgConfirmBlocked)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// requireConfirm consumes the form's confirm_token for the session user,
// action and target. Without a valid one it answers 403 with
// msgConfirmInvalid on the target's page and returns false; the handler
// must then do nothing else. Handlers call it before any other check.
func (a *app) requireConfirm(w http.ResponseWriter, r *http.Request, action, targetKind string, targetID int64) bool {
	return a.requireConfirmToken(w, r, r.PostFormValue("confirm_token"), action, targetKind, targetID)
}

// requireConfirmToken is requireConfirm for a token taken from elsewhere
// than the posted form: the salvage download is a GET whose token is in
// its query.
func (a *app) requireConfirmToken(w http.ResponseWriter, r *http.Request, token, action, targetKind string, targetID int64) bool {
	err := store.ErrConfirmInvalid
	if token != "" {
		err = a.Store.ConsumeConfirmation(confirmHash(token), auth.UsernameFrom(r.Context()), action, targetKind,
			targetID, a.Now())
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
		a.render.renderStatus(w, http.StatusForbidden, "replica_databases", p)
	case action == ActTightenTLS:
		// The button is on the master page.
		a.showError(w, r, srv, http.StatusForbidden, msgConfirmInvalid)
	case targetKind == auditServer:
		a.renderServer(w, r, srv, serverView{Status: http.StatusForbidden, Flash: msgConfirmInvalid})
	default:
		a.showError(w, r, srv, http.StatusForbidden, msgConfirmInvalid)
	}
}
