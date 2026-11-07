package orchestrator

import (
	"slices"
	"strconv"
	"strings"
	"unicode"
)

// gtidSet is a parsed GTID set: per source ("uuid", or "uuid:tag" for a
// tagged GTID), merged inclusive intervals. The consideration screen
// compares the agents' sets with it; nothing is sent to MySQL.
type gtidSet map[string][]gtidRange

type gtidRange struct{ lo, hi uint64 }

// parseGTIDSet reads "uuid:1-5:7,uuid2:tag:1-3"; whitespace (MySQL puts
// newlines between sources) is ignored and "" is the empty set. ok is
// false for anything it cannot read.
func parseGTIDSet(s string) (set gtidSet, ok bool) {
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
	set = gtidSet{}
	if s == "" {
		return set, true
	}
	for _, item := range strings.Split(s, ",") {
		parts := strings.Split(item, ":")
		if len(parts) < 2 || parts[0] == "" {
			return nil, false
		}
		uuid := strings.ToLower(parts[0])
		key := uuid
		for _, p := range parts[1:] {
			if p == "" {
				return nil, false
			}
			if c := p[0]; c < '0' || c > '9' { // a tag: the intervals after it are its own
				key = uuid + ":" + strings.ToLower(p)
				continue
			}
			loS, hiS, isRange := strings.Cut(p, "-")
			if !isRange {
				hiS = loS
			}
			lo, err1 := strconv.ParseUint(loS, 10, 64)
			hi, err2 := strconv.ParseUint(hiS, 10, 64)
			if err1 != nil || err2 != nil || lo == 0 || hi < lo {
				return nil, false
			}
			set[key] = append(set[key], gtidRange{lo, hi})
		}
	}
	for k, iv := range set {
		set[k] = mergeRanges(iv)
	}
	return set, true
}

func mergeRanges(iv []gtidRange) []gtidRange {
	slices.SortFunc(iv, func(a, b gtidRange) int {
		switch {
		case a.lo < b.lo:
			return -1
		case a.lo > b.lo:
			return 1
		}
		return 0
	})
	out := []gtidRange{iv[0]}
	for _, c := range iv[1:] {
		last := &out[len(out)-1]
		if c.lo <= last.hi+1 {
			last.hi = max(last.hi, c.hi)
			continue
		}
		out = append(out, c)
	}
	return out
}

// union is a ∪ b.
func (a gtidSet) union(b gtidSet) gtidSet {
	out := gtidSet{}
	for _, s := range []gtidSet{a, b} {
		for k, iv := range s {
			out[k] = append(out[k], iv...)
		}
	}
	for k, iv := range out {
		out[k] = mergeRanges(slices.Clone(iv))
	}
	return out
}

// minusCount counts the transactions of a that b lacks, leaving out those
// of the source uuid skip (with any tag) when skip is not "".
func (a gtidSet) minusCount(b gtidSet, skip string) int {
	skip = strings.ToLower(skip)
	var n uint64
	for k, aiv := range a {
		if skip != "" && (k == skip || strings.HasPrefix(k, skip+":")) {
			continue
		}
		biv := b[k]
		for _, x := range aiv {
			covered := uint64(0)
			for _, y := range biv {
				lo, hi := max(x.lo, y.lo), min(x.hi, y.hi)
				if lo <= hi {
					covered += hi - lo + 1
				}
			}
			n += x.hi - x.lo + 1 - covered
		}
	}
	return int(n)
}

// gtidMissing counts the transactions of a that b lacks; 0 when either
// cannot be read.
func gtidMissing(a, b string) int {
	as, ok1 := parseGTIDSet(a)
	bs, ok2 := parseGTIDSet(b)
	if !ok1 || !ok2 {
		return 0
	}
	return as.minusCount(bs, "")
}
