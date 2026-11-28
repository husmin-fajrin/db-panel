package mysql

import (
	"bufio"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	drv "github.com/go-sql-driver/mysql"

	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// TestPromoteJobBoundParts: the values the panel's agentPromoteJobBound
// adds up (the lines between "bound-parts:" and ":bound-parts" of its
// comment in internal/panel/orchestrator/promote_sent.go) are this
// agent's: a change here that lengthens a step of mysql.promote fails
// until the panel's bound B is worked out again.
func TestPromoteJobBoundParts(t *testing.T) {
	cfg, err := drv.ParseDSN(DSN(proto.ConnectParams{User: "u", Host: "10.0.0.1", Port: 3306}, ""))
	if err != nil {
		t.Fatal(err)
	}
	session, err := os.ReadFile("../../session.go")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`requestTimeout\s*=\s*(\d+)\s*\*\s*time\.Minute`).FindSubmatch(session)
	if m == nil {
		t.Fatal("requestTimeout not found in internal/agent/session.go")
	}
	minutes, _ := strconv.Atoi(string(m[1]))
	agent := map[string]string{
		"adminReadTimeout": cfg.ReadTimeout.String(),
		"adminDialTimeout": cfg.Timeout.String(),
		"requestTimeout":   (time.Duration(minutes) * time.Minute).String(),
		"emergencyProbes":  strconv.Itoa(emergencyProbes),
		"probeSpacing":     probeSpacing.String(),
		"peerTimeout":      peerTimeout.String(),
		"relayWaitSec":     strconv.Itoa(relayWaitSec),
		"relayLogEvery":    relayLogEvery.String(),
		"restoreTimeout":   restoreTimeout.String(),
		"liftLockWait":     liftLockWait.String(),
	}

	f, err := os.Open("../../../panel/orchestrator/promote_sent.go")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	line := regexp.MustCompile(`^//\s+(\w+) = (\S+) \(`)
	panel := map[string]string{}
	in := false
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		s := strings.TrimSpace(sc.Text())
		switch {
		case s == "// bound-parts:":
			in = true
		case s == "// :bound-parts":
			in = false
		case in:
			if m := line.FindStringSubmatch(s); m != nil {
				v := m[2]
				if d, err := time.ParseDuration(v); err == nil {
					v = d.String()
				}
				panel[m[1]] = v
			}
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	for name, want := range agent {
		got, ok := panel[name]
		switch {
		case !ok:
			t.Errorf("%s is not listed in the panel's agentPromoteJobBound comment", name)
		case got != want:
			t.Errorf("%s: the panel adds up %s, the agent has %s", name, got, want)
		}
	}
	for name := range panel {
		if _, ok := agent[name]; !ok {
			t.Errorf("the panel lists %s, which this test does not check", name)
		}
	}
}
