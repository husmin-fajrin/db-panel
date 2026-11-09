package web

import (
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

// OutcomeOf exposes outcomeOf to web_test.
var OutcomeOf = outcomeOf

// StatusLabelOf is the label newStatusView gives a view.
func StatusLabelOf(id int64, role string, v monitor.View) string {
	return newStatusView(id, role, v, true, time.Time{}).Label
}
