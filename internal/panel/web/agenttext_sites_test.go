package web_test

import (
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// longAgentText is an agent's text of more than 5,000 characters spread
// over many lines, starting with marker.
func longAgentText(marker string) string {
	return marker + strings.Repeat(" baris\npanjang\r\n\tdari agent", 200)
}

// agentLabelOf is the label before serverName's words on a page.
func agentLabelOf(serverName string) string {
	return "Pesan dari agent " + serverName + " (bukan dari panel): "
}

// labelledValue finds marker's value after serverName's label in body and
// returns the agent's part of it (the text after the label, up to the
// next tag). It fails when the value is not after the label.
func labelledValue(t *testing.T, body, serverName, marker string) string {
	t.Helper()
	start := strings.Index(body, agentLabelOf(serverName)+marker)
	if start < 0 {
		t.Fatalf("no %q after the label %q in the page", marker, agentLabelOf(serverName))
	}
	value := body[start+len(agentLabelOf(serverName)):]
	if end := strings.IndexByte(value, '<'); end >= 0 {
		value = value[:end]
	}
	return value
}

// agentChars counts the agent's characters in a value as monitor.AgentText
// bounds it: the "…" that marks a cut is the panel's, and the single
// spaces it puts for the agent's whitespace are not counted.
func agentChars(value string) int {
	return utf8.RuneCountInString(strings.Join(strings.Fields(strings.TrimSuffix(value, "…")), ""))
}

// wantBoundedLine checks an agent's value as a page shows it: one line (no
// control character), at most limit of the agent's characters
// (agentChars), and marked as cut.
func wantBoundedLine(t *testing.T, what, value string, limit int) {
	t.Helper()
	if strings.ContainsFunc(value, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		t.Errorf("%s holds a control character: %q", what, value)
	}
	if n := agentChars(value); n > limit {
		t.Errorf("%s is %d characters, want at most %d: %q", what, n, limit, value)
	}
	if !strings.HasSuffix(value, "…") {
		t.Errorf("%s is not marked as cut: %q", what, value)
	}
}

// TestHealthFieldsBoundedAndLabelled: the agent's text in a health report
// (MySQL's error messages, the pin error and an unknown role pin) is shown
// on the master, replica and server pages one line, bounded, and after a
// label saying it is the agent's words.
func TestHealthFieldsBoundedAndLabelled(t *testing.T) {
	e, mon, _ := monitorEnv(t)
	c := e.cookie()
	unknownPin := "PINX" + strings.Repeat("peran\nlain", 20) // 204 characters
	if utf8.RuneCountInString(unknownPin) < 200 {
		t.Fatalf("unknown pin is only %d characters", utf8.RuneCountInString(unknownPin))
	}

	// The master page: MySQL's message while MySQL does not answer.
	master := masterSnap()
	master.MySQLOK, master.MySQLError = false, longAgentText("MYSQLERR-M")
	master.RolePin, master.PinError = unknownPin, longAgentText("PINERR")
	mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, ReceivedAt: monNow, Snap: master,
		Status: monitor.Status{Code: monitor.StatusUnreachable, Severity: monitor.SevCrit}})
	rec := e.do("GET", "/masters/1", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBoundedLine(t, "master page MySQLError",
		labelledValue(t, rec.Body.String(), "db-master", "MYSQLERR-M"), 300)

	// The replica page: MySQL's message and the replication errors.
	mon.setView(monitor.View{ServerID: 2, Role: store.RoleReplica, ReceivedAt: monNow,
		Status: monitor.Status{Code: monitor.StatusStopped, Severity: monitor.SevCrit},
		Snap: &proto.HealthSnapshot{MySQLOK: true, MySQLError: longAgentText("MYSQLERR-R"),
			Replica: &proto.ReplicaStatus{IORunning: "No", SQLRunning: "No", SourceHost: "10.0.0.1", SourcePort: 3306,
				LastSQLErrno: 1062, LastSQLError: longAgentText("SQLERR"),
				LastIOErrno: 2003, LastIOError: longAgentText("IOERR")}}})
	rec = e.do("GET", "/replicas/2", nil, c)
	wantCode(t, rec, http.StatusOK)
	for _, marker := range []string{"MYSQLERR-R", "SQLERR", "IOERR"} {
		wantBoundedLine(t, "replica page "+marker, labelledValue(t, rec.Body.String(), "db-replika", marker), 300)
	}

	// The server page: the pin error, and the unknown role pin as
	// "tidak dikenal" with the agent's value cut at 32 characters.
	rec = e.do("GET", "/servers/1", nil, c)
	wantCode(t, rec, http.StatusOK)
	body := rec.Body.String()
	wantBoundedLine(t, "server page PinError", labelledValue(t, body, "db-master", "PINERR"), 300)
	if !strings.Contains(body, "Peran dipin: tidak dikenal ("+agentLabelOf("db-master")+"PINX") {
		t.Errorf("unknown role pin is not shown as tidak dikenal with the agent's labelled value:\n%s",
			between(body, "Detail teknis", "</details>"))
	}
	wantBoundedLine(t, "server page RolePin",
		strings.TrimSuffix(labelledValue(t, body, "db-master", "PINX"), ")"), 32)
	if strings.Contains(body, longAgentText("")[:40]) || strings.Contains(body, unknownPin[:40]) {
		t.Error("the server page shows the agent's text unbounded")
	}
}

// TestSourcePinBoundedWhenMalformed (ruling R9): a source pin that is a
// well-formed host:port is shown as it is; any other value is the agent's
// text, shown as "tidak dikenal" with its labelled value one line and cut
// at 32 characters.
func TestSourcePinBoundedWhenMalformed(t *testing.T) {
	e, mon, _ := monitorEnv(t)
	c := e.cookie()
	show := func(pin string) string {
		t.Helper()
		snap := masterSnap()
		snap.RolePin, snap.SourcePin = "replica", pin
		mon.setView(monitor.View{ServerID: 1, Role: store.RoleMaster, ReceivedAt: monNow, Snap: snap})
		rec := e.do("GET", "/servers/1", nil, c)
		wantCode(t, rec, http.StatusOK)
		return rec.Body.String()
	}
	for _, pin := range []string{"10.0.0.5:3306", "db-master.example:3306"} {
		body := show(pin)
		if !strings.Contains(body, "Sumber dipin: "+pin+"</p>") {
			t.Errorf("well-formed source pin %q is not shown as it is:\n%s", pin, between(body, "Detail teknis", "</details>"))
		}
	}
	unknown := "Sumber dipin: tidak dikenal (" + agentLabelOf("db-master")
	long := longAgentText("SRCPIN")
	body := show(long)
	if !strings.Contains(body, unknown+"SRCPIN") {
		t.Errorf("long source pin is not shown as tidak dikenal:\n%s", between(body, "Detail teknis", "</details>"))
	}
	wantBoundedLine(t, "long source pin", strings.TrimSuffix(labelledValue(t, body, "db-master", "SRCPIN"), ")"), 32)
	for _, pin := range []string{"host:notaport", "db master:3306"} {
		body := show(pin)
		if want := unknown + pin + ")</p>"; !strings.Contains(body, want) {
			t.Errorf("malformed source pin %q is not shown as %q:\n%s", pin, want, between(body, "Detail teknis", "</details>"))
		}
	}
}
