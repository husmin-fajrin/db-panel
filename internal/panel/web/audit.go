package web

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/husmin-fajrin/db-panel/internal/panel/auth"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
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
var auditKeptKeys = map[string]bool{"action": true, "target_kind": true}

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
	if runes := []rune(outcome); len(runes) > maxAuditOutcome {
		outcome = string(runes[:maxAuditOutcome])
	}
	_, err := a.Store.AddAudit(store.AuditEntry{
		TS: a.Now(), Username: username, IP: clientIP(r), Action: action,
		TargetKind: targetKind, TargetID: targetID, DetailJSON: scrubDetail(detail, outcome != auditOK), Outcome: outcome,
	})
	if err != nil {
		a.Logger.Error("write audit log", "action", action, "target_kind", targetKind, "target_id", targetID, "err", err)
	}
}

// outcomeOf is auditOK for nil, else the error's text.
func outcomeOf(err error) string {
	if err == nil {
		return auditOK
	}
	return err.Error()
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
