package mysql

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// gtidSet is a GTID set computed here, without a server: per source (a
// server UUID, or "uuid:tag" for tagged GTIDs), sorted disjoint intervals
// of transaction numbers. mysql.binlogTail builds one from a binary log
// read with mysqlbinlog while MySQL is down; the promotion counts relay
// log transactions with it.
type gtidSet map[string][]gtidInterval

// gtidInterval is the transactions lo..hi, both included.
type gtidInterval struct{ lo, hi int64 }

var (
	gtidUUIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	gtidTagRe  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]{0,31}$`)
)

// parseGTIDSet parses a GTID set as MySQL prints it
// ("uuid:1-5:7,uuid2:3", with "uuid:1-5:tag:1-2" for tags; whitespace and
// newlines are ignored). The empty string is the empty set.
func parseGTIDSet(s string) (gtidSet, error) {
	g := gtidSet{}
	for _, entry := range strings.Split(s, ",") {
		entry = strings.Join(strings.Fields(entry), "")
		if entry == "" {
			continue
		}
		parts := strings.Split(entry, ":")
		uuid := strings.ToLower(parts[0])
		if !gtidUUIDRe.MatchString(uuid) {
			return nil, fmt.Errorf("set GTID tidak valid: %q", entry)
		}
		key := uuid
		for _, part := range parts[1:] {
			if gtidTagRe.MatchString(part) {
				key = uuid + ":" + strings.ToLower(part)
				continue
			}
			lo, hi, ok := parseGTIDInterval(part)
			if !ok {
				return nil, fmt.Errorf("set GTID tidak valid: %q", entry)
			}
			g.add(key, lo, hi)
		}
	}
	return g, nil
}

// parseGTIDInterval parses "n" or "n-m" (1 <= n <= m).
func parseGTIDInterval(s string) (lo, hi int64, ok bool) {
	a, b, isRange := strings.Cut(s, "-")
	lo, err := strconv.ParseInt(a, 10, 64)
	if err != nil || lo < 1 {
		return 0, 0, false
	}
	hi = lo
	if isRange {
		if hi, err = strconv.ParseInt(b, 10, 64); err != nil || hi < lo {
			return 0, 0, false
		}
	}
	return lo, hi, true
}

// parseGTID parses one GTID, "uuid:n" or "uuid:tag:n", into its source key
// and number.
func parseGTID(s string) (key string, n int64, ok bool) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", 0, false
	}
	n, err := strconv.ParseInt(s[i+1:], 10, 64)
	if err != nil || n < 1 {
		return "", 0, false
	}
	uuid, tag, tagged := strings.Cut(s[:i], ":")
	if !gtidUUIDRe.MatchString(uuid) || (tagged && !gtidTagRe.MatchString(tag)) {
		return "", 0, false
	}
	key = strings.ToLower(uuid)
	if tagged {
		key += ":" + strings.ToLower(tag)
	}
	return key, n, true
}

// add puts lo..hi into the set under key, merging overlapping and
// adjacent intervals.
func (g gtidSet) add(key string, lo, hi int64) {
	ivs := append(g[key], gtidInterval{lo, hi})
	slices.SortFunc(ivs, func(a, b gtidInterval) int {
		switch {
		case a.lo < b.lo:
			return -1
		case a.lo > b.lo:
			return 1
		}
		return 0
	})
	merged := ivs[:1]
	for _, iv := range ivs[1:] {
		last := &merged[len(merged)-1]
		if iv.lo <= last.hi+1 {
			last.hi = max(last.hi, iv.hi)
			continue
		}
		merged = append(merged, iv)
	}
	g[key] = merged
}

// addSet puts every transaction of o into g.
func (g gtidSet) addSet(o gtidSet) {
	for key, ivs := range o {
		for _, iv := range ivs {
			g.add(key, iv.lo, iv.hi)
		}
	}
}

// count is the number of transactions in the set.
func (g gtidSet) count() int64 {
	var n int64
	for _, ivs := range g {
		for _, iv := range ivs {
			n += iv.hi - iv.lo + 1
		}
	}
	return n
}

// String prints the set as MySQL does, without newlines: sources sorted
// by UUID, a UUID's untagged intervals first, then each tag's.
func (g gtidSet) String() string {
	keys := make([]string, 0, len(g))
	for k, ivs := range g {
		if len(ivs) > 0 {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys) // "uuid" sorts before "uuid:tag"
	var entries []string
	var cur strings.Builder
	curUUID := ""
	for _, k := range keys {
		uuid, tag, _ := strings.Cut(k, ":")
		if uuid != curUUID {
			if cur.Len() > 0 {
				entries = append(entries, cur.String())
				cur.Reset()
			}
			curUUID = uuid
			cur.WriteString(uuid)
		}
		if tag != "" {
			cur.WriteString(":" + tag)
		}
		for _, iv := range g[k] {
			cur.WriteString(":" + strconv.FormatInt(iv.lo, 10))
			if iv.hi != iv.lo {
				cur.WriteString("-" + strconv.FormatInt(iv.hi, 10))
			}
		}
	}
	if cur.Len() > 0 {
		entries = append(entries, cur.String())
	}
	return strings.Join(entries, ",")
}

// gtidCount is the number of transactions in the GTID set s.
func gtidCount(s string) (int64, error) {
	g, err := parseGTIDSet(s)
	if err != nil {
		return 0, err
	}
	return g.count(), nil
}

// has reports whether the transaction n of source key is in the set.
func (g gtidSet) has(key string, n int64) bool {
	for _, iv := range g[key] {
		if n >= iv.lo && n <= iv.hi {
			return true
		}
	}
	return false
}
