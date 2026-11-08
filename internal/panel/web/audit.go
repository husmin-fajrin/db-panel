package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/panel/redact"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Audit target kinds: the entity an action concerns. The target id is the
// server's id for all but session (0).
const (
	auditServer  = "server"
	auditMaster  = "master"
	auditReplica = "replica"
	auditJob     = "job"
	auditSession = "session"
)

// auditOK is the outcome of an action that succeeded.
const auditOK = "ok"

// maxAuditOutcome caps an outcome (an error text) in runes.
const maxAuditOutcome = 500

// Bounds of an audit entry's detail (SEC-44): the log is append-only, so
// one entry may never be large.
const (
	maxAuditDetail = 2048 // bytes of detail_json
	maxAuditString = 128  // runes of each string
	maxAuditList   = 64   // items of each list
	auditCutMark   = "…(dipotong)"
)

// auditFullKeys are detail keys whose string value is never cut: where
// alerts and their links go (SEC-47) must be readable whole. Their own
// validation bounds them (a base URL at maxBaseURL, a chat id at 32
// characters), well inside maxAuditDetail.
var auditFullKeys = map[string]bool{store.SettingPanelBaseURL: true, store.SettingTelegramChatID: true}

// auditKeptKeys are detail keys whose string value is kept even when the
// action did not succeed: the handler checked it against a fixed list
// first (the action and target kind of a confirmation).
// salvage_file is a salvage file name the handler checked against the
// agent's strict pattern before auditing it.
var auditKeptKeys = map[string]bool{"action": true, "target_kind": true, auditSalvageFile: true}

// auditSalvageFile is the detail key of a salvage file's name
// (salvage_download, salvage_delete).
const auditSalvageFile = "salvage_file"

// unknownUser stands for a login_failed username that names no account: a
// password typed in the username field must not be kept for ever.
const unknownUser = "(tidak dikenal)"

// audit records an action of the logged-in user in the audit log (SEC-17).
// detail is stored as JSON without any field whose name mentions a
// password, token or secret, each string cut to 128 runes (except the
// values of auditFullKeys), each list to 64
// items, and at most 2 KiB in all; outcome is auditOK or the reason it
// failed. The values of an action that did not succeed (input refused, or
// an action that failed) are never stored: only the field names with the
// length of each string and list (SEC-44). A failed write is logged, never
// shown to the user.
func (a *app) audit(r *http.Request, action, targetKind string, targetID int64, detail any, outcome string) {
	a.auditAs(r, auth.UsernameFrom(r.Context()), action, targetKind, targetID, detail, outcome)
}

// auditAs is audit for a request whose user is not (yet, or any longer)
// in its context: login and logout.
func (a *app) auditAs(r *http.Request, username, action, targetKind string, targetID int64, detail any, outcome string) {
	// The hash joins fields with "\n" (SEC-45): no stored field may hold
	// one. An error text can span lines; usernames should not, but are
	// typed.
	outcome, username = oneLine(outcome), oneLine(username)
	if runes := []rune(outcome); len(runes) > maxAuditOutcome {
		outcome = string(runes[:maxAuditOutcome])
	}
	e := store.AuditEntry{
		TS: a.Now(), Username: username, IP: clientIP(r), Action: action,
		TargetKind: targetKind, TargetID: targetID, DetailJSON: scrubDetail(detail, outcome != auditOK), Outcome: outcome,
	}
	id, err := a.Store.AddAudit(e)
	if err != nil {
		a.Logger.Error("write audit log", "action", action, "target_kind", targetKind, "target_id", targetID, "err", err)
	}
	// The second copy (SEC-45): the panel's log goes to journald, out of
	// reach of whoever can edit panel.db. It carries exactly the stored
	// fields (already scrubbed), also when the database write failed
	// (stored=false, id 0).
	a.Logger.Info("audit", "stored", err == nil, "id", id, "ts", e.TS.UTC().Format(time.RFC3339), "username", e.Username, "ip", e.IP,
		"action", e.Action, "target_kind", e.TargetKind, "target_id", e.TargetID,
		"detail_json", e.DetailJSON, "outcome", e.Outcome)
}

// oneLine replaces line breaks with spaces.
func oneLine(s string) string {
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s)
}

// outcomeOf is auditOK for nil, else the error's text. An agent's answer
// (a *proto.Error in the chain) may quote MySQL's row data: its quoted
// literals are censored (spec 5.12); the panel's own refusals are kept as
// written.
func outcomeOf(err error) string {
	if err == nil {
		return auditOK
	}
	if _, ok := errors.AsType[*proto.Error](err); ok {
		return redact.Quotes(err.Error())
	}
	return err.Error()
}

// logErr is err's text for the panel's log (journald), with the quoted
// literals censored (redact.Quotes): an agent's error may quote MySQL's
// row data. "" for nil.
func logErr(err error) string {
	if err == nil {
		return ""
	}
	return redact.Quotes(err.Error())
}

// outcomeMsg is the audit outcome of a refusal message: auditOK when empty.
func outcomeMsg(msg string) string {
	if msg == "" {
		return auditOK
	}
	return msg
}

// scrubDetail encodes detail as JSON without the fields whose name
// contains "password", "token" or "secret" (any case, at any depth), with
// long strings and lists cut; with lengthsOnly each string and list is
// replaced by its length (<name>_len, <name>_count). Larger than
// maxAuditDetail it is only its size. nil (or anything that cannot be
// encoded) is "{}".
func scrubDetail(detail any, lengthsOnly bool) string {
	if detail == nil {
		return "{}"
	}
	raw, err := json.Marshal(detail)
	if err != nil {
		return "{}"
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber() // keep large ids exact
	var v any
	if err := dec.Decode(&v); err != nil {
		return "{}"
	}
	v = scrub(v)
	if lengthsOnly {
		v = lengths(v)
	} else {
		v = limit(v)
	}
	out, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	if len(out) > maxAuditDetail {
		out, _ = json.Marshal(map[string]any{"dipotong": true, "panjang": len(out)})
	}
	return string(out)
}

// limit cuts every string to maxAuditString runes and every list to
// maxAuditList items, marking each cut; a string under one of
// auditFullKeys stays whole.
func limit(v any) any {
	switch x := v.(type) {
	case string:
		if r := []rune(x); len(r) > maxAuditString {
			return string(r[:maxAuditString]) + auditCutMark
		}
	case map[string]any:
		for k, val := range x {
			if _, ok := val.(string); ok && auditFullKeys[k] {
				continue
			}
			x[k] = limit(val)
		}
	case []any:
		if len(x) > maxAuditList {
			rest := len(x) - maxAuditList
			x = append(x[:maxAuditList:maxAuditList], fmt.Sprintf("…(%d lagi)", rest))
		}
		for i := range x {
			x[i] = limit(x[i])
		}
		return x
	}
	return v
}

// lengths replaces, in a refused or failed action's detail, each string
// by its length in runes (<name>_len) and each list by its count
// (<name>_count); numbers, booleans and the strings of auditKeptKeys stay.
func lengths(v any) any {
	m, ok := v.(map[string]any)
	if !ok {
		return lengths(map[string]any{"detail": v})
	}
	out := make(map[string]any, len(m))
	for k, val := range m {
		switch x := val.(type) {
		case string:
			if auditKeptKeys[k] {
				out[k] = x
				continue
			}
			out[k+"_len"] = utf8.RuneCountInString(x)
		case []any:
			out[k+"_count"] = len(x)
		case map[string]any:
			out[k] = lengths(x)
		default:
			out[k] = x
		}
	}
	return out
}

func scrub(v any) any {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			if secretKey(k) {
				delete(x, k)
				continue
			}
			x[k] = scrub(val)
		}
	case []any:
		for i := range x {
			x[i] = scrub(x[i])
		}
	}
	return v
}

func secretKey(k string) bool {
	k = strings.ToLower(k)
	return strings.Contains(k, "password") || strings.Contains(k, "token") || strings.Contains(k, "secret")
}

// auditUsername is how a login_failed entry names who tried: the username
// when that account exists, else unknownUser and a short SHA-256 of what
// was typed (repeated attempts with one name still group together).
func auditUsername(typed string, exists bool) string {
	if exists {
		return typed
	}
	sum := sha256.Sum256([]byte(typed))
	return unknownUser + " " + hex.EncodeToString(sum[:4])
}
