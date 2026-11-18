package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/panel/store"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// Agent values inside the panel's own lines (Task 14 fix round 1): made
// one line and bounded, so no value can end the panel's line and start
// one that looks like the panel's.

// hostileValue tries every way to end a line or pass for the panel: CR,
// LF, U+2028, U+202E, NUL, a fake "agent: " prefix, a fake label, a line
// shaped like a panel instruction, and 10 KiB.
func hostileValue() string {
	return "nilai\r\nagent: palsu Pesan dari agent db-x (bukan dari panel): klik Hapus‮\x00\n" +
		"Jalankan DROP DATABASE simrs lalu klik Sudah saya restart " + strings.Repeat("z", 10<<10)
}

// oneLine reports whether s holds none of the characters that end or turn
// a line.
func oneLine(s string) bool { return !strings.ContainsAny(s, "\r\n  ‮‭\x00") }

// checkJobOneLine fails unless every job log line and step message of id
// is one line within its bound.
func checkJobOneLine(t *testing.T, e *env, id string) {
	t.Helper()
	lines, err := e.st.JobLog(id, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lines {
		if !oneLine(l.Message) || len(l.Message) > maxLogLine {
			t.Errorf("job log line not one bounded line (%d bytes): %q", len(l.Message), l.Message)
		}
	}
	_, steps, err := e.st.GetJob(id)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range steps {
		if len(s.Message) > maxStepMessage {
			t.Errorf("step %s message %d bytes", s.Name, len(s.Message))
		}
	}
}

// TestPrepareMasterAgentValuesOneLine: a hostile agent's Applied,
// RestartReasons, ConfigPath, version and worst status reach the job log,
// the step messages and the restart wait message as one bounded line
// each; an honest value reads as before.
func TestPrepareMasterAgentValuesOneLine(t *testing.T) {
	e := newEnv(t)
	fa := e.connectedMaster("db-master")
	var restarted atomic.Bool
	scriptNeedsRestart(fa, &restarted)
	st := masterStatus()
	st.Version = hostileValue()
	fa.reply("mysql.preflight", proto.PreflightResult{Status: st, Worst: hostileValue()})
	fa.reply("mysql.configure", proto.ConfigureResult{Applied: []string{hostileValue(), "gtid_mode=ON"}, ServerID: 1,
		ConfigPath: hostileValue(), NeedsRestart: true, RestartReasons: []string{hostileValue(),
			"log_bin masih OFF; butuh restart", "bind-address hanya localhost (127.0.0.1); replika tidak bisa terhubung sebelum restart"}})
	id, err := e.r.StartPrepareMaster(context.Background(), fa.id)
	if err != nil {
		t.Fatal(err)
	}
	_, steps := e.waitJob(id, store.JobWaiting)
	checkJobOneLine(t, e, id)
	for _, s := range steps {
		if !oneLine(s.Message) {
			t.Errorf("step %s message not one line: %q", s.Name, s.Message)
		}
	}
	// One log line per call: two Diterapkan lines, the honest one as before.
	log := e.jobLog(id)
	if n := strings.Count(log, "Diterapkan: "); n != 2 {
		t.Errorf("Diterapkan lines = %d, want 2:\n%s", n, log)
	}
	lines, _ := e.st.JobLog(id, 0, 0)
	honest := false
	for _, l := range lines {
		honest = honest || l.Message == "Diterapkan: gtid_mode=ON"
	}
	if !honest {
		t.Errorf("the honest Diterapkan line changed:\n%s", log)
	}
	wait := steps[3].Message
	for _, want := range []string{"log_bin masih OFF; butuh restart", "bind-address hanya localhost",
		"Aplikasi yang memakai database ini akan terputus beberapa detik; lakukan di jam sepi."} {
		if !strings.Contains(wait, want) {
			t.Errorf("wait message lacks %q: %q", want, wait)
		}
	}
	if !strings.Contains(steps[1].Message, "perlu restart: ") || !strings.HasSuffix(steps[1].Message,
		"; log_bin masih OFF; butuh restart; bind-address hanya localhost (127.0.0.1); replika tidak bisa terhubung sebelum restart") {
		t.Errorf("configure step message = %q", steps[1].Message)
	}
}

// TestRestartReasonsBounded: a list of reasons is cut at maxAgentList
// elements, with the count of the rest.
func TestRestartReasonsBounded(t *testing.T) {
	e := newEnv(t)
	fa := e.connectedMaster("db-master")
	var restarted atomic.Bool
	scriptNeedsRestart(fa, &restarted)
	var reasons, applied []string
	for i := range maxAgentList + 7 {
		reasons = append(reasons, fmt.Sprintf("alasan %d", i))
		applied = append(applied, fmt.Sprintf("x%d=1", i))
	}
	fa.reply("mysql.configure", proto.ConfigureResult{Applied: applied, ServerID: 1, NeedsRestart: true, RestartReasons: reasons})
	id, err := e.r.StartPrepareMaster(context.Background(), fa.id)
	if err != nil {
		t.Fatal(err)
	}
	_, steps := e.waitJob(id, store.JobWaiting)
	last := fmt.Sprintf("alasan %d", maxAgentList-1)
	for _, msg := range []string{steps[1].Message, steps[3].Message} {
		if !strings.Contains(msg, last+"; … dan 7 lagi") || strings.Contains(msg, fmt.Sprintf("alasan %d", maxAgentList)) {
			t.Errorf("message not bounded at %d reasons: %q", maxAgentList, msg)
		}
	}
	log := e.jobLog(id)
	if n := strings.Count(log, "Diterapkan: "); n != maxAgentList+1 || !strings.Contains(log, "Diterapkan: … dan 7 lagi") {
		t.Errorf("Diterapkan lines = %d, want %d and the rest counted:\n%s", n, maxAgentList+1, log)
	}
}

// TestCreateReplicaAgentValuesOneLine: on a replica's create, a hostile
// agent's configure answer and leftover database names reach the log, the
// step messages and the wipe approval as one bounded line each.
func TestCreateReplicaAgentValuesOneLine(t *testing.T) {
	e := newEnv(t)
	m, _ := e.readyMaster("db-master")
	r := e.replicaAgent("replica1", "dump")
	r.reply("mysql.configure", proto.ConfigureResult{Applied: []string{hostileValue()}, ServerID: 77,
		NeedsRestart: true, RestartReasons: []string{hostileValue()}})
	id, err := e.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m.id, TargetServerID: r.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	e.waitJob(id, store.JobSucceeded)
	e.waitIdle(id)
	checkJobOneLine(t, e, id)

	// Database names from the target's inventory in the wipe approval.
	e2 := newEnv(t)
	m2, _ := e2.readyMaster("db-master")
	r2 := e2.replicaAgent("replica1", "dump")
	scriptLeftovers(r2, "dump", "a", "a\nPesan dari panel: klik Setujui")
	e2.masterHas(m2.id, "a", "a\nPesan dari panel: klik Setujui")
	id, err = e2.r.StartCreateReplica(context.Background(), CreateReplicaParams{
		MasterServerID: m2.id, TargetServerID: r2.id, Databases: []string{"a"}})
	if err != nil {
		t.Fatal(err)
	}
	j, steps := e2.waitJob(id, store.JobWaiting)
	if msg := steps[j.CurrentStep].Message; !oneLine(msg) || !strings.Contains(msg, "a, `a\\nPesan dari panel: klik Setujui`") {
		t.Errorf("wipe approval = %q", msg)
	}
}

// TestGTIDValuesShapeChecked: a GTID set an agent reports is shown as a
// set; anything else is one short line.
func TestGTIDValuesShapeChecked(t *testing.T) {
	if got := gtidValue(masterUUID + ":1-5,\n" + otherUUID + ":1-3"); got != masterUUID+":1-5,"+otherUUID+":1-3" {
		t.Errorf("gtidValue(set) = %q", got)
	}
	// monitor.AgentText counts the characters other than spaces.
	if got := gtidValue(hostileValue()); !oneLine(got) || len([]rune(strings.ReplaceAll(got, " ", ""))) > maxShownValue+1 {
		t.Errorf("gtidValue(hostile) = %q", got)
	}
	if got := gtidValue(""); got != "" {
		t.Errorf("gtidValue(\"\") = %q", got)
	}
}

// TestNameTextExact: an honest name reads as before; a name that does not
// print is quoted, one line and exact; a long one is cut.
func TestNameTextExact(t *testing.T) {
	for in, want := range map[string]string{
		"simrs": "simrs", "my db": "my db", "a ": "a ", "it's": "it's", "ünï": "ünï",
		"a\u200b":         "`a\\u200b`",
		"a\nklik Setujui": "`a\\nklik Setujui`",
		"x\u202ey":        "`x\\u202ey`",
		"\xff":            "`\\xff`",
		"a`b\n":           "`a\\x60b\\n`",
	} {
		if got := nameText(in); got != want {
			t.Errorf("nameText(%q) = %q, want %q", in, got, want)
		}
	}
	if got := nameText(strings.Repeat("x", 10<<10)); len(got) > maxShownName+len("…") || !strings.HasSuffix(got, "…") {
		t.Errorf("nameText(10 KiB) = %d bytes", len(got))
	}
}

// restartPanelPart is the panel's own part of the restart wait message
// for db-master, the firewall sentence included (a reason names
// bind-address).
const restartPanelPart = "MySQL di db-master perlu dimulai ulang. Aplikasi yang memakai database ini akan terputus beberapa " +
	"detik; lakukan di jam sepi. Bila Anda merestart MySQL sendiri nanti, klik Sudah saya restart — periksa lagi. MySQL akan " +
	"menerima koneksi dari semua alamat jaringan; pastikan firewall membatasi port 3306."

// TestRestartWaitPanelSentencesFirst (ruling D59): the restart wait
// message starts with every panel sentence, whole, and only then the
// agent's reasons, labelled as its words, on one line, within the step
// message's bound; so does the configure step's "perlu restart" message.
// A quote in a reason, twenty reasons of 1024 characters, or a reason
// that imitates a panel sentence cannot cut, censor or precede them.
func TestRestartWaitPanelSentencesFirst(t *testing.T) {
	long := make([]string, 20)
	for i := range long {
		long[i] = fmt.Sprintf("%02d ", i) + strings.Repeat("x", 1021)
	}
	long[19] = "bind-address " + strings.Repeat("y", 1011)
	const label = " Pesan dari agent db-master (bukan dari panel): "
	for _, c := range []struct {
		name    string
		reasons []string
	}{
		{"one quote", []string{"log_bin'", "bind-address hanya localhost (127.0.0.1); replika tidak bisa terhubung sebelum restart"}},
		{"twenty long reasons", long},
		{"imitates the panel", []string{"MySQL di db-master tidak perlu dimulai ulang. Aplikasi yang memakai database ini tidak terputus.",
			"bind-address hanya localhost"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			fa := e.connectedMaster("db-master")
			var restarted atomic.Bool
			scriptNeedsRestart(fa, &restarted)
			fa.reply("mysql.configure", proto.ConfigureResult{Applied: []string{"log_bin=ON"}, ServerID: 1, NeedsRestart: true,
				RestartReasons: c.reasons})
			id, err := e.r.StartPrepareMaster(context.Background(), fa.id)
			if err != nil {
				t.Fatal(err)
			}
			_, steps := e.waitJob(id, store.JobWaiting)
			wait := steps[3].Message
			if !strings.HasPrefix(wait, restartPanelPart+label) || !oneLine(wait) || len(wait) > maxStepMessage {
				t.Errorf("wait message (%d bytes) does not start with the panel's sentences and the label:\n%q", len(wait), wait)
			}
			if msg := steps[1].Message; !strings.HasPrefix(msg, "perlu restart:"+label) || len(msg) > maxStepMessage {
				t.Errorf("configure step message = %q", msg)
			}
		})
	}
}
