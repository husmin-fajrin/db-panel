package monitor

import (
	"sort"
	"strconv"
	"strings"
	"unicode"
)

type gtidInterval struct{ lo, hi uint64 } // inclusive

// parseGTIDSet reads "uuid:1-5:7,uuid2:1-3" into merged, sorted intervals
// per (lower-cased) uuid. Whitespace, including the newlines MySQL puts
// between uuids, is ignored. ok is false for anything it cannot read.
func parseGTIDSet(s string) (set map[string][]gtidInterval, ok bool) {
	s = strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, s)
	if s == "" {
		return nil, false
	}
	set = map[string][]gtidInterval{}
	for _, item := range strings.Split(s, ",") {
		parts := strings.Split(item, ":")
		if len(parts) < 2 || parts[0] == "" {
			return nil, false
		}
		uuid := strings.ToLower(parts[0])
		for _, p := range parts[1:] {
			loS, hiS, isRange := strings.Cut(p, "-")
			if !isRange {
				hiS = loS
			}
			lo, err1 := strconv.ParseUint(loS, 10, 64)
			hi, err2 := strconv.ParseUint(hiS, 10, 64)
			if err1 != nil || err2 != nil || lo == 0 || hi < lo {
				return nil, false
			}
			set[uuid] = append(set[uuid], gtidInterval{lo, hi})
		}
	}
	for uuid, iv := range set {
		set[uuid] = mergeIntervals(iv)
	}
	return set, true
}

func mergeIntervals(iv []gtidInterval) []gtidInterval {
	sort.Slice(iv, func(i, j int) bool { return iv[i].lo < iv[j].lo })
	out := iv[:1]
	for _, c := range iv[1:] {
		last := &out[len(out)-1]
		if c.lo <= last.hi+1 {
			if c.hi > last.hi {
				last.hi = c.hi
			}
			continue
		}
		out = append(out, c)
	}
	return out
}

// GTIDGap counts the transactions in masterExecuted that replicaSet does
// not contain. It is 0 when either set is empty or cannot be parsed.
func GTIDGap(masterExecuted, replicaSet string) int {
	m, ok := parseGTIDSet(masterExecuted)
	if !ok {
		return 0
	}
	r, ok := parseGTIDSet(replicaSet)
	if !ok {
		return 0
	}
	var gap uint64
	for uuid, miv := range m {
		riv := r[uuid]
		for _, mi := range miv {
			covered := uint64(0)
			for _, ri := range riv {
				lo, hi := max(mi.lo, ri.lo), min(mi.hi, ri.hi)
				if lo <= hi {
					covered += hi - lo + 1
				}
			}
			gap += mi.hi - mi.lo + 1 - covered
		}
	}
	return int(gap)
}
