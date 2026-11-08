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
	for i := 0; i < len(s); {
		q := s[i]
		if (q != '\'' && q != '"') || apostrophe(s, i) {
			b.WriteByte(q)
			i++
			continue
		}
		if end, ok := template(s, i); ok {
			b.WriteString(Censored)
			i = end + 1
			continue
		}
		if end, ok := closing(s, i); ok {
			lit := s[i+1 : end]
			switch {
			case q == '\'' && panelAccount(lit):
				b.WriteString(s[i : end+1])
				account = end
			case q == '\'' && account >= 0 && i == account+2 && s[account+1] == '@' && host(lit):
				b.WriteString(s[i : end+1])
			default:
				b.WriteString(Censored)
			}
			i = end + 1
			continue
		}
		// Never closed: the rest of the line.
		b.WriteString("'…")
		i++
		for i < len(s) && s[i] != '\n' && s[i] != '\r' {
			i++
		}
	}
	return b.String()
}

// templates are MySQL's messages that print a value without escaping it
// (a quote inside the value is printed as is): the text before the
// value's opening quote, and what follows its closing quote. The value
// ends at the LAST occurrence of that follower on the line, so quotes
// inside it cannot end it early. An empty follower means the line's last
// quote: the statement in Last_SQL_Error's "Query: '…'".
var templates = []struct{ before, after string }{
	{"Duplicate entry ", "' for key '"},
	{" value: ", "' for column"},
	{"Error ", "' on query"}, // Last_SQL_Error: Error '<message>' on query.
	{"Query: ", ""},
}

// template reports where the value opened by the quote at s[open] ends,
// when the quote opens a value of one of MySQL's templates.
func template(s string, open int) (int, bool) {
	if s[open] != '\'' {
		return 0, false
	}
	line := s[open+1:]
	if n := strings.IndexAny(line, "\n\r"); n >= 0 {
		line = line[:n]
	}
	for _, t := range templates {
		if !strings.HasSuffix(s[:open], t.before) {
			continue
		}
		var n int
		if t.after == "" {
			n = strings.LastIndexByte(line, '\'')
		} else {
			n = strings.LastIndex(line, t.after)
		}
		if n >= 0 {
			return open + 1 + n, true
		}
		if t.after == "" {
			return 0, false // never closed: the rest of the line
		}
	}
	return 0, false
}

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
