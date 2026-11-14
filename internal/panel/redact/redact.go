// Package redact censors row data out of MySQL's messages before the
// panel stores or sends them (spec 5.12, "Sensor kutipan data saat
// ingest"): MySQL quotes the offending values ("Duplicate entry 'Budi'
// for key …"), and a hospital's values are patient data.
package redact

import "strings"

// Censored replaces each quoted literal.
const Censored = "'…'"

// Quotes replaces every literal quoted with ' or " in s by '…', its
// escapes (a backslash before a quote, or the quote doubled) included,
// so no fragment of the value is left. Everything outside quotes is
// kept: words, error numbers, long numbers such as binlog positions and
// GTIDs, `identifiers`. A quote
// that is never closed censors the rest of its line ('… with no closing
// quote). Quotes is a fixed point: Quotes(Quotes(s)) == Quotes(s).
//
// One kind of literal is kept: an account the panel itself makes in
// MySQL ('dbpanel_repl', 'dbpanel_dump_…'), with the host right after it
// ('dbpanel_repl'@'10.0.0.%'). Such names are the panel's, never row
// data, and the panel's advice quotes them as SQL to run (DROP USER,
// GRANT); censoring them would leave the advice useless.
//
// Two more kinds are kept (G-6), because the agent's advice quotes them
// and censoring them makes the advice unreadable:
//   - a literal whose whole content is exactly one of panelPhrases (a
//     button label or a README section name), case-sensitive;
//   - the account the listing refusal names, 'user'@'host', when the user
//     holds only the characters of the agent's proof (proofUser), the host
//     passes host, and the pair directly follows "akun MySQL " or
//     "GRANT SHOW DATABASES ON *.* TO " (accountLeads).
//
// MySQL's templates (below) still win: such a phrase or account as the
// value of "Duplicate entry '…'" or "Query: '…'" is censored.
//
// MySQL prints some values without escaping them (Duplicate entry,
// Incorrect … value, Last_SQL_Error's message and Query): those values
// are found by MySQL's own message templates first (templates), so a
// quote inside a value cannot end it early. Elsewhere a quote closes a
// literal only when no word character follows it, since MySQL nests
// literals without escaping them, and a contraction's apostrophe
// ("doesn't", "event's") opens none.
func Quotes(s string) string {
	if !strings.ContainsAny(s, `'"`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	account := -1 // where the last panel account literal ended
	// kept is the quote that ends the output when its last bytes are a
	// kept literal (0 otherwise). A censored literal right after a kept
	// one ending in ' is written with " (H-3, N-G4): '…' there would make
	// the two quotes read as an escaped one on a second pass, and Quotes
	// would not be a fixed point.
	var kept byte
	censor := func(open bool) {
		token := Censored
		if !open {
			token = "'…"
		}
		if kept == '\'' {
			token = strings.ReplaceAll(token, "'", `"`)
		}
		b.WriteString(token)
		kept = 0
	}
	keep := func(lit string) {
		b.WriteString(lit)
		kept = lit[len(lit)-1]
	}
	for i := 0; i < len(s); {
		q := s[i]
		if (q != '\'' && q != '"') || apostrophe(s, i) {
			b.WriteByte(q)
			kept = 0
			i++
			continue
		}
		if end, ok := template(s, i); ok {
			censor(true)
			i = end + 1
			continue
		}
		if end, ok := advisedAccount(s, i); ok {
			keep(s[i : end+1])
			i = end + 1
			continue
		}
		if end, ok := closing(s, i); ok {
			lit := s[i+1 : end]
			switch {
			case panelPhrase(lit):
				keep(s[i : end+1])
			case q == '\'' && panelAccount(lit):
				keep(s[i : end+1])
				account = end
			case q == '\'' && account >= 0 && i == account+2 && s[account+1] == '@' && host(lit):
				keep(s[i : end+1])
			default:
				censor(true)
			}
			i = end + 1
			continue
		}
		// Never closed: the rest of the line.
		censor(false)
		i++
		for i < len(s) && s[i] != '\n' && s[i] != '\r' {
			i++
		}
	}
	return b.String()
}

// panelPhrases are the panel's own phrases that operator texts quote and
// that reach Quotes (inventory in the part G report): button labels and
// README section names. The agent's texts in internal/agent quote them;
// a text that quotes a new phrase needs it added here (and a copy of the
// text in phrases_test.go).
var panelPhrases = []string{
	"Ulangi dari tahap ini",                                 // quarantine.go errNoLoadRecord; rebuild_load.go errListIncomplete
	"Database yang tidak ada di master baru",                // rebuild_load.go errNotReplaced (A6)
	"Memberi hak SHOW DATABASES di server yang dikarantina", // rebuild_load.go errListIncomplete
	"Paksa dump",         // clone.go cloneError
	"Sudah saya alihkan", // orchestrator errRebuildCutoverPending, errCutoverStillPending, deletable (panel log)
}

// panelPhrase reports whether lit is exactly one of panelPhrases.
func panelPhrase(lit string) bool {
	for _, p := range panelPhrases {
		if lit == p {
			return true
		}
	}
	return false
}

// accountLeads are the agent's own sentence parts right before the
// account the listing refusal advises (rebuild_load.go errListIncomplete
// prints quoteAccount there), whole so that a value holding a shorter
// lead is not taken for it (H-3, N-G5). A lead counts only at the start of
// the message or after a non-word character.
var accountLeads = []string{"tidak bisa dipastikan lengkap: akun MySQL ", "di server ini: GRANT SHOW DATABASES ON *.* TO "}

// advisedAccount reports where the account 'user'@'host' opened at
// s[open] ends, when it directly follows one of accountLeads, its user
// holds only proofUser characters and its host passes host.
func advisedAccount(s string, open int) (int, bool) {
	if s[open] != '\'' {
		return 0, false
	}
	lead := false
	for _, l := range accountLeads {
		if p := open - len(l); strings.HasSuffix(s[:open], l) && (p == 0 || !word(s[p-1])) {
			lead = true
		}
	}
	if !lead {
		return 0, false
	}
	userEnd := strings.IndexByte(s[open+1:], '\'')
	if userEnd < 0 {
		return 0, false
	}
	userEnd += open + 1
	if !proofUser(s[open+1:userEnd]) || !strings.HasPrefix(s[userEnd+1:], "@'") {
		return 0, false
	}
	hostOpen := userEnd + 2
	hostEnd, ok := closing(s, hostOpen)
	if !ok || !host(s[hostOpen+1:hostEnd]) {
		return 0, false
	}
	return hostEnd, true
}

// proofUser reports whether user holds only the characters the agent's
// listing proof accepts in an account name ([A-Za-z0-9_.$-]+, proofUserRe
// in internal/agent/drivers/mysql; copied, not imported).
func proofUser(user string) bool {
	if user == "" {
		return false
	}
	for i := 0; i < len(user); i++ {
		c := user[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte("_.$-", c) >= 0) {
			return false
		}
	}
	return true
}

// templates are MySQL's messages that print a value without escaping it
// (a quote inside the value is printed as is): the text before the
// value's opening quote (with lead, when set, before it in turn: one
// word between them, such as "Truncated incorrect DOUBLE value: "), and
// what follows its closing quote. The value ends at the LAST occurrence
// of that follower in the rest of the message, whatever lines lie
// between, so neither a quote nor a newline inside it can end it early.
// An empty follower censors to the end of the whole message: the
// statement in Last_SQL_Error's "Query: '…'", a parse error's "near
// '…' at line N", and a "Truncated incorrect … value: '…'", whose value
// may hold anything and whose message may be cut. A template whose
// follower is missing (MySQL cut Last_SQL_Error off at 512 bytes inside
// the value) censors to the end of the message too.
var templates = []struct{ lead, before, after string }{
	{"", "Duplicate entry ", "' for key '"},
	{"Truncated incorrect ", " value: ", ""},
	{"", " value: ", "' for column"},
	{"", "Error ", "' on query"}, // Last_SQL_Error: Error '<message>' on query.
	{"", "Query: ", ""},
	{"", "near ", ""}, // ER_PARSE_ERROR: … near '<statement>' at line 1
}

// template reports where the value opened by the quote at s[open] ends,
// when the quote opens a value of one of MySQL's templates.
func template(s string, open int) (int, bool) {
	q := s[open]
	if q != '\'' && q != '"' {
		return 0, false
	}
	rest := s[open+1:]
	for _, t := range templates {
		head, ok := strings.CutSuffix(s[:open], t.before)
		if !ok || (t.lead != "" && !strings.HasSuffix(strings.TrimRightFunc(head, isWordRune), t.lead)) {
			continue
		}
		if t.after == "" {
			return len(s) - 1, true // the rest of the message
		}
		// MySQL quotes these values with ', but a value quoted with " is
		// matched the same way, so that censoring is a fixed point
		// (Quotes writes every censored literal as '…').
		after := string(q) + t.after[1:]
		if n := strings.LastIndex(rest, after); n >= 0 {
			return open + 1 + n, true
		}
		return len(s) - 1, true // cut inside the value: the rest of the message
	}
	return 0, false
}

func isWordRune(r rune) bool { return r < 0x80 && word(byte(r)) }

// closing finds the quote that closes the literal opened at s[open]:
// the same quote, not escaped (\q or qq), with no word character after
// it. The literal may span lines.
func closing(s string, open int) (int, bool) {
	q := s[open]
	for k := open + 1; k < len(s); k++ {
		switch s[k] {
		case '\\':
			k++ // the escaped byte, whatever it is
		case q:
			if k+1 < len(s) && s[k+1] == q {
				k++ // doubled: an escaped quote
				continue
			}
			if k+1 == len(s) || !word(s[k+1]) {
				return k, true
			}
		}
	}
	return 0, false
}

// apostrophe reports whether the quote at s[i] is a contraction's
// apostrophe ("doesn't", "event's", "you're"): a word character before
// it, one or two lowercase letters after it, then the word ends. Any
// other quote opens a literal, so a value glued to a word ("x'Budi'")
// is censored too.
func apostrophe(s string, i int) bool {
	if i == 0 || !word(s[i-1]) {
		return false
	}
	if p := s[i-1]; strings.IndexByte("xXbB", p) >= 0 && (i == 1 || !word(s[i-2])) {
		return false // a hex or bit literal: x'4275', b'1010'
	}
	n := 0
	for k := i + 1; k < len(s) && s[k] >= 'a' && s[k] <= 'z'; k++ {
		n++
	}
	end := i + 1 + n
	return n >= 1 && n <= 2 && (end == len(s) || !word(s[end]))
}

// panelAccount reports whether lit is the name of an account the panel
// makes: dbpanel_ followed by lowercase letters, digits and _.
func panelAccount(lit string) bool {
	rest, ok := strings.CutPrefix(lit, "dbpanel_")
	if !ok || rest == "" {
		return false
	}
	for i := 0; i < len(rest); i++ {
		if c := rest[i]; !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return false
		}
	}
	return true
}

// host reports whether lit can be an account's host: a name, an IPv4 or
// IPv6 address, or a pattern of them ('%', '10.0.0.%'), with no space,
// never only digits (that is a number, such as an NIK, not a host).
func host(lit string) bool {
	if strings.IndexFunc(lit, func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r == '.' || r == ':' || r == '%'
	}) < 0 {
		return false
	}
	for i := 0; i < len(lit); i++ {
		c := lit[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.IndexByte(".-:%_/", c) >= 0) {
			return false
		}
	}
	return true
}

// word reports whether c belongs to a word: an ASCII letter or digit, or
// any byte of a multi-byte UTF-8 character.
func word(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c >= 0x80
}
