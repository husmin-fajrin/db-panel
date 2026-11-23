package web

import (
	"fmt"

	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
)

// msgGateUnread replaces the precondition list when the gate cannot judge
// (an unknown server or topology, a store failure); %s is the error as the
// panel shows it.
const msgGateUnread = "Prasyarat tidak bisa dibaca: %s"

// gateRowView is one precondition row as the "gate-list" template shows
// it: the status in words (D1), the label and, when the row is not green,
// its detail and then its fix ("" shows nothing). Backup: the backup row,
// which carries D4.
type gateRowView struct {
	Key, Label, Detail, Fix string
	Badge                   Badge
	Green, Backup           bool
}

// gateView is the precondition list of one gated action (plan 5b-2b,
// Task 11). Red: a row is red. NeedsAck: the backup row asks for the tick
// (D3). Emergency: an emergency promote, which no row refuses. OnlyRed:
// the page lists red rows only, and only while one is. Err is the line
// shown instead of the rows when the gate could not judge.
type gateView struct {
	Rows                              []gateRowView
	Red, NeedsAck, Emergency, OnlyRed bool
	Err                               string
}

// newGateView is the view of g; with onlyRed only its red rows are kept.
func newGateView(g orchestrator.GateResult, emergency, onlyRed bool) gateView {
	v := gateView{Red: len(g.Red()) > 0, NeedsAck: !emergency && g.NeedsBackupAck(), Emergency: emergency, OnlyRed: onlyRed}
	for _, r := range g.Rows {
		if onlyRed && r.Status != orchestrator.GateRed {
			continue
		}
		row := gateRowView{Key: r.Key, Label: r.Label, Backup: r.Key == "backup"}
		switch r.Status {
		case orchestrator.GateRed:
			row.Badge = Badge{Tone: "error", Text: "Belum terpenuhi"}
		case orchestrator.GateYellow:
			row.Badge = Badge{Tone: "warning", Text: "Perlu perhatian"}
		default:
			row.Badge, row.Green = Badge{Tone: "success", Text: "Terpenuhi"}, true
		}
		if !row.Green {
			row.Detail, row.Fix = r.Detail, r.Fix
		}
		v.Rows = append(v.Rows, row)
	}
	return v
}

// Shown reports whether the list is drawn: always, unless the page lists
// red rows only and none is red (and the gate could be read).
func (v gateView) Shown() bool {
	return !v.OnlyRed || v.Red || v.Err != ""
}

// Off reports whether the gated button is disabled: a red row, or a gate
// that could not judge (the post would be refused either way). An
// emergency promote is never refused by a row. The disabled button is a
// convenience: the handlers and the orchestrator judge the gate again.
func (v gateView) Off() bool {
	return !v.Emergency && (v.Red || v.Err != "")
}

// RedNote reports whether D2 goes below the disabled button: a red row the
// operator can fix (with an unread gate the list's line says why).
func (v gateView) RedNote() bool {
	return !v.Emergency && v.Red && v.Err == ""
}

// gateFor reads the gate of in for a page. A gate that cannot judge does
// not fail the page: its list is msgGateUnread and its button is off.
func (a *app) gateFor(in orchestrator.GateInput, onlyRed bool) *gateView {
	emergency := in.Action == orchestrator.GatePromoteEmergency
	g, err := a.Orchestrator.Gate(in)
	if err != nil {
		a.Logger.Warn("gate not read for the page", "action", in.Action, "master", in.MasterID, "target", in.TargetID,
			"err", logErr(err))
		return &gateView{Emergency: emergency, OnlyRed: onlyRed, Err: fmt.Sprintf(msgGateUnread, a.shownError(err))}
	}
	v := newGateView(g, emergency, onlyRed)
	return &v
}
