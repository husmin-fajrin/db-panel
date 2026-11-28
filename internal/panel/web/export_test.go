package web

import (
	"strconv"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
)

// SubscriberCount exposes the broker's subscriber count to web_test.
func SubscriberCount(b *Broker) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.subs)
}

// PairCommand exposes pairCommand to web_test.
var PairCommand = pairCommand

// StatusLabel exposes statusLabel to web_test.
var StatusLabel = statusLabel

// ScrubDetail exposes scrubDetail to web_test.
var ScrubDetail = scrubDetail

// HasFragment reports whether the renderer's fragment set defines name.
func HasFragment(r *Renderer, name string) bool {
	return r.fragments.Lookup(name) != nil
}

// HasPageTemplate reports whether page's template set defines name.
func HasPageTemplate(r *Renderer, page, name string) bool {
	t, ok := r.pages[page]
	return ok && t.Lookup(name) != nil
}

// Monitoring formatters, exposed to web_test.
var (
	FmtDuration = fmtDuration
	FmtPct      = fmtPct
	Ago         = ago
)

// StatusExplain exposes the explanation newStatusView gives a view.
func StatusExplain(id int64, role string, v monitor.View) *monitor.Explanation {
	return newStatusView(id, role, v, true, time.Time{}).Explain
}

// OutcomeOf exposes outcomeText to web_test, a server named by its id.
func OutcomeOf(err error) string {
	return outcomeText(err, func(id int64) string { return "#" + strconv.FormatInt(id, 10) })
}

// StatusLabelOf is the label newStatusView gives a view.
func StatusLabelOf(id int64, role string, v monitor.View) string {
	return newStatusView(id, role, v, true, time.Time{}).Label
}

// ServiceLineOf exposes serviceLineOf to web_test.
var ServiceLineOf = serviceLineOf

// PromoteScreenSteps are the step names of the promote screen's
// "Urutan langkah" card in mode.
func PromoteScreenSteps(mode string) []string {
	var names []string
	for _, s := range promoteScreenSteps(mode) {
		names = append(names, s.Name)
	}
	return names
}

// Audit page wording, exposed to web_test.
var (
	AuditActionLabel   = auditActionLabel
	AuditDetailSummary = auditDetailSummary
)

// LightActions exposes lightActions to web_test.
func LightActions() []string { return lightActions }

// SetBeforeCreatePairToken makes f run right before a handler creates a
// pairing token and returns a func restoring the default. Not for parallel
// tests.
func SetBeforeCreatePairToken(f func(serverID int64)) (restore func()) {
	old := beforeCreatePairToken
	beforeCreatePairToken = f
	return func() { beforeCreatePairToken = old }
}
