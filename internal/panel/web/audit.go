package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

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

// audit records an action of the logged-in user in the audit log (SEC-17).
// detail is stored as JSON without any field whose name mentions a
// password, token or secret; outcome is auditOK or the reason it failed. A
// failed write is logged, never shown to the user.
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
		TargetKind: targetKind, TargetID: targetID, DetailJSON: scrubDetail(detail), Outcome: outcome,
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
// contains "password", "token" or "secret" (any case, at any depth); nil
// (or anything that cannot be encoded) is "{}".
func scrubDetail(detail any) string {
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
	out, err := json.Marshal(scrub(v))
	if err != nil {
		return "{}"
	}
	return string(out)
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
