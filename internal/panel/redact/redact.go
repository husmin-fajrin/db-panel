// Package redact censors row data out of MySQL's messages before the
// panel stores or sends them (spec 5.12, "Sensor kutipan data saat
// ingest"): MySQL quotes the offending values ("Duplicate entry 'Budi'
// for key …"), and a hospital's values are patient data.
package redact

import "strings"

// Censored replaces each quoted literal.
const Censored = "'…'"

// Quotes replaces every literal quoted with ' or " in s by '…', its
// escapes (\' and ”, \" and "") included, so no fragment of the value
// is left. Everything outside quotes is kept: words, error numbers, long
// numbers such as binlog positions and GTIDs, `identifiers`. A quote
// that is never closed censors the rest of its line ('… with no closing
// quote). Quotes is a fixed point: Quotes(Quotes(s)) == Quotes(s).
//
// One kind of literal is kept: an account the panel itself makes in
// MySQL ('dbpanel_repl', 'dbpanel_dump_…'), with the host right after it
// ('dbpanel_repl'@'10.0.0.%'). Such names are the panel's, never row
// data, and the panel's advice quotes them as SQL to run (DROP USER,
// GRANT); censoring them would leave the advice useless.
//
// MySQL nests literals without escaping them (Last_SQL_Error: "Error
// 'Duplicate entry 'Budi' for key …” on query"), so a quote closes a
// literal only when no word character follows it, and a contraction's
// apostrophe ("doesn't", "event's") opens none.
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
// IPv6 address, or a pattern of them ('%', '10.0.0.%'), with no space.
func host(lit string) bool {
	if lit == "" {
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
