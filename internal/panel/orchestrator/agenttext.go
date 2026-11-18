package orchestrator

import (
	"errors"
	"fmt"
	"strings"

	"github.com/husmin-fajrin/db-panel/internal/panel/hub"
	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
)

// Text from an agent is labelled with where it comes from wherever it
// becomes a step's or a job's error, a replica's last error, a job log
// line or a page's flash (spec 5.6): an agent may be compromised, and its
// words must never read as the panel's instruction.
//
// Its origin is decided by type, never by code: the hub wraps every
// failure an agent reported in *hub.AgentError and nothing it makes
// itself (fromAgent). A text is labelled once: the label replaces the
// hub's wrapper (agentTextError unwraps to the agent's *proto.Error, not
// to the *hub.AgentError), so a later layer that looks for an agent's
// failure in the chain finds none and keeps the labelled text as it is.

// agentLabel is the label before an agent's words (spec 5.6).
const agentLabel = "Pesan dari agent %s (bukan dari panel): %s"

// agentLogPrefix starts a job log line that is the agent's own (spec 5.6).
const agentLogPrefix = "agent: "

// fromAgent reports the words of the failure an agent reported, when err
// holds one (*hub.AgentError); the hub's own failures (timeout, offline)
// are not the agent's, whatever their code.
func fromAgent(err error) (text string, ok bool) {
	ae, ok := errors.AsType[*hub.AgentError](err)
	if !ok {
		return "", false
	}
	return ae.Error(), true
}

// AgentQuoteN is an agent's own sentence as the panel shows it: after a
// label saying whose words they are, made one line (control characters,
// line breaks and bidirectional marks become one space) and cut at limit
// characters by monitor.AgentText, so it never reads as the panel's
// instruction. Everything after the label is the agent's.
func AgentQuoteN(server, text string, limit int) string {
	return fmt.Sprintf(agentLabel, server, monitor.AgentText(text, limit))
}

// AgentQuote is AgentQuoteN cut at maxAgentQuote, for an agent's sentence
// shown in a block of its own (P-2).
func AgentQuote(server, text string) string { return AgentQuoteN(server, text, maxAgentQuote) }

// maxAgentQuote bounds the agent's sentence in an AgentQuote.
const maxAgentQuote = 300

// agentTextError is an agent's text as the error of a step: labelled with
// its server and cut at maxLogLine (not at maxAgentQuote: the agent's
// advice is longer). err, when set, is the agent's *proto.Error, so code
// checks (errors.Is, errors.As) keep working; never the *hub.AgentError,
// so the text is not labelled a second time.
type agentTextError struct {
	server, text string
	err          error
}

func (e agentTextError) Error() string { return AgentQuoteN(e.server, e.text, maxLogLine) }
func (e agentTextError) Unwrap() error { return e.err }

// agentError labels the failure an agent reported in err (fromAgent) with
// the name of its server; any other err is returned as it is.
func agentError(err error, name func(int64) string) error {
	ae, ok := errors.AsType[*hub.AgentError](err)
	if !ok {
		return err
	}
	return agentTextError{server: name(ae.Server), text: ae.Error(), err: ae.Err}
}

// agentErrorText is err's text with the words of an agent's failure in it
// (a *hub.AgentError not labelled yet) labelled: the panel's own words
// that wrap it are kept before the label; when the agent's words are not
// at the end of the text, the whole text is labelled as the agent's.
func agentErrorText(err error, name func(int64) string) string {
	ae, ok := errors.AsType[*hub.AgentError](err)
	if !ok {
		return err.Error()
	}
	full, words := err.Error(), ae.Error()
	server := name(ae.Server)
	if panel, ok := strings.CutSuffix(full, words); ok {
		return panel + AgentQuoteN(server, words, maxLogLine)
	}
	return AgentQuoteN(server, full, maxLogLine)
}

// ShownError is err as a page's flash shows it (spec 5.6): an agent's
// failure in it labelled with its server, named by name (agentErrorText);
// the panel's own text, and a text already labelled, as they are.
func ShownError(err error, name func(serverID int64) string) string {
	return agentErrorText(err, name)
}

// agentLogLine is a log line an agent's job sent, as the job log keeps it:
// one line, after agentLogPrefix.
func agentLogLine(msg string) string {
	return agentLogPrefix + monitor.AgentText(msg, maxLogLine)
}
