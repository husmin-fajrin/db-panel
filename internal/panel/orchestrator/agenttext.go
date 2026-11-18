package orchestrator

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

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

// agentErrText is err's text with the words of an agent's failure in it
// labelled (agentErrorText), the server named from the store; the hub's
// own timeout and offline keep the panel's words, and a text already
// labelled is kept as it is.
func (r *Runner) agentErrText(err error) string { return agentErrorText(err, r.nameOf) }

// agentWords is the text an agent sent in a field of its answer (not an
// error), labelled as serverID's words and cut at limit characters.
func (r *Runner) agentWords(serverID int64, text string, limit int) string {
	return AgentQuoteN(r.nameOf(serverID), text, limit)
}

// agentPart is an agent's value inside a line already marked as the
// agent's ("agent: " lines, or a sentence that names the agent as its
// source): one line, cut at limit characters.
func agentPart(s string, limit int) string { return monitor.AgentText(s, limit) }

// maxShownGTID bounds a GTID set an agent reports, as a page or a job log
// line shows it.
const maxShownGTID = 512

// shownGTID is a GTID set an agent reported, as the panel shows it: only
// when it reads as a GTID set (parseGTIDSet), without its whitespace and
// bounded; "" for anything else, which is then not shown at all.
func shownGTID(s string) string {
	if len(s) > 64<<10 {
		return ""
	}
	if set, ok := parseGTIDSet(s); !ok || len(set) == 0 {
		return ""
	}
	return truncate(strings.Join(strings.Fields(s), ""), maxShownGTID)
}

// maxAgentList bounds how many elements of an agent's list a panel line
// shows; maxShownValue bounds a short agent value (a version, a status).
const (
	maxAgentList  = 20
	maxShownValue = 64
)

// panelText is text the panel composed itself that may span lines on
// purpose (a wait message's paragraphs). logf and done keep it as written;
// every other string argument of theirs is taken for a value that may be
// an agent's and is made one line (agentPart). Its agent values must be
// made one line where it is built.
type panelText string

// flatArgs makes every string argument one line and bounded at limit
// characters (agentPart), keeps a panelText as written, and passes any
// other argument on. It is how logf and done keep an agent's value from
// ending the panel's line: the panel's own words are the format, which is
// never touched, so no line the panel writes is split or joined.
func flatArgs(args []any, limit int) []any {
	out := make([]any, len(args)) // not the caller's slice
	for i, a := range args {
		switch v := a.(type) {
		case string:
			out[i] = agentPart(v, limit)
		case panelText:
			out[i] = string(v)
		default:
			out[i] = a
		}
	}
	return out
}

// agentEach is each element of an agent's list made one line and bounded
// at limit characters; the number of elements is kept (a list the operator
// approves item by item is shown whole).
func agentEach(items []string, limit int) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, agentPart(it, limit))
	}
	return out
}

// agentList is an agent's list as one panel line: each element one line
// and bounded (agentEach, at maxLogLine), at most maxAgentList of them and
// the count of the rest ("… dan N lagi", the page's wording for a cut
// list), joined with sep.
func agentList(items []string, sep string) string {
	shown := items
	if len(items) > maxAgentList {
		shown = items[:maxAgentList]
	}
	out := agentEach(shown, maxLogLine)
	if n := len(items) - len(shown); n > 0 {
		out = append(out, fmt.Sprintf("… dan %d lagi", n))
	}
	return strings.Join(out, sep)
}

// gtidValue is a GTID set an agent reports, as a panel line shows it: the
// set when it reads as one (shownGTID); anything else made one line and
// cut at maxShownValue characters; "" stays "".
func gtidValue(s string) string {
	if s == "" {
		return ""
	}
	if g := shownGTID(s); g != "" {
		return g
	}
	return agentPart(s, maxShownValue)
}

// maxShownName bounds a name nameText shows.
const maxShownName = 256

// nameText is a name an agent reports (a database, a table, a GRANT
// statement to run) as a panel line shows it. Names are data compared
// byte for byte, so flattening (which drops format characters and joins
// spaces) could make two names look the same; instead a name of printable
// characters and plain spaces is shown as it is, and any other (a line
// break, a control, format or bidirectional character, invalid UTF-8) is
// shown between backticks, as MySQL quotes an identifier, with Go escapes
// inside (a backtick as \x60): one line, and still exact. Backticks, not
// double quotes, because redact.Quotes censors a quoted literal and keeps
// an identifier. A longer name is cut, with "…".
func nameText(s string) string {
	cut := ""
	if len(s) > maxShownName {
		n := maxShownName
		for n > 0 && !utf8.RuneStart(s[n]) {
			n--
		}
		s, cut = s[:n], "…"
	}
	if !utf8.ValidString(s) || strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) >= 0 {
		inner := strconv.Quote(s)
		inner = strings.ReplaceAll(inner[1:len(inner)-1], "`", `\x60`)
		return "`" + inner + "`" + cut
	}
	return s + cut
}

// nameList is nameText of each element.
func nameList(items []string) []string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, nameText(it))
	}
	return out
}
