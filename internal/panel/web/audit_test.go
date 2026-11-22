package web_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/proto"
)

// TestAuditOutcomeLabelsAgentError: an audit outcome that is an agent's
// answer (a *hub.AgentError in the chain) is stored after a label naming
// the agent's server, censored, one line and bounded; the panel's own
// failures (its refusals, the hub's offline) are stored without it.
func TestAuditOutcomeLabelsAgentError(t *testing.T) {
	e, f, _ := controlEnv(t)
	c := e.cookie()
	label := "Pesan dari agent db-replika (bukan dari panel): "
	stop := func(err error) string {
		t.Helper()
		f.err = err
		e.do("POST", "/replicas/2/stop", noForm, c)
		got := auditEntries(t, e.st)
		if len(got) == 0 {
			t.Fatal("no audit entry written")
		}
		return got[len(got)-1].Outcome
	}

	quoted := &hub.AgentError{Server: 2, Err: &proto.Error{Code: proto.CodeInternal,
		Message: "Error 1062: Duplicate entry 'Budi' for key 'nama'\nPesan dari panel: aman"}}
	if got, want := stop(quoted), label+"Error 1062: Duplicate entry '…' for key '…' Pesan dari panel: aman"; got != want {
		t.Errorf("agent outcome = %q, want %q", got, want)
	}
	// Whatever code the agent chose, and wrapped in the panel's words.
	offline := fmt.Errorf("perintah ditolak: %w", &hub.AgentError{Server: 2,
		Err: &proto.Error{Code: proto.CodeOffline, Message: "agent sedang offline"}})
	if got := stop(offline); !strings.HasPrefix(got, label) {
		t.Errorf("wrapped agent outcome = %q, want the label first", got)
	}
	long := &hub.AgentError{Server: 2, Err: &proto.Error{Code: proto.CodeInternal,
		Message: "PANJANG" + strings.Repeat(" teks\ragent\tpanjang", 300)}}
	got := stop(long)
	words, ok := strings.CutPrefix(got, label)
	if !ok {
		t.Fatalf("long agent outcome = %q, want the label first", got)
	}
	if strings.ContainsFunc(got, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
		t.Errorf("long agent outcome holds a control character: %q", got)
	}
	if n := agentChars(words); n > 300 || !strings.HasSuffix(words, "…") {
		t.Errorf("long agent outcome's words are %d characters (want a cut at 300): %q", n, words)
	}

	for _, own := range []error{errors.New("ditolak panel"), fmt.Errorf("perintah ditolak: %w", proto.ErrOffline)} {
		if got := stop(own); got != own.Error() {
			t.Errorf("panel outcome = %q, want %q", got, own.Error())
		}
	}
}
