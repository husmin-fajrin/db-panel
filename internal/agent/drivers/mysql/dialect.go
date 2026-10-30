package mysql

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// ErrUnsupported is returned by ParseVersion for anything but Oracle MySQL
// 8.0.26 or later.
var ErrUnsupported = errors.New("versi MySQL tidak didukung (butuh Oracle MySQL 8.0.26 ke atas)")

// Dialect is a parsed server version and the SQL that differs between series.
type Dialect struct {
	Version             string
	Major, Minor, Patch int
	Series              string // "8.0", "8.4" (8.x innovation: "8.<minor>"), "9" for 9.x and later
}

// ParseVersion parses @@version, e.g. "8.0.46-0ubuntu0.24.04.4".
func ParseVersion(v string) (Dialect, error) {
	if strings.Contains(strings.ToLower(v), "mariadb") {
		return Dialect{}, ErrUnsupported
	}
	num, _, _ := strings.Cut(v, "-")
	parts := strings.Split(num, ".")
	if len(parts) != 3 {
		return Dialect{}, ErrUnsupported
	}
	var n [3]int
	for i, p := range parts {
		x, err := strconv.Atoi(p)
		if err != nil || x < 0 {
			return Dialect{}, ErrUnsupported
		}
		n[i] = x
	}
	d := Dialect{Version: v, Major: n[0], Minor: n[1], Patch: n[2]}
	switch {
	case d.Major < 8, d.Major == 8 && d.Minor == 0 && d.Patch < 26:
		return Dialect{}, ErrUnsupported
	case d.Major == 8:
		d.Series = fmt.Sprintf("8.%d", d.Minor)
	default:
		d.Series = strconv.Itoa(d.Major)
	}
	return d, nil
}

// ResetBinaryLogs is the statement that clears binary logs and GTID history:
// RESET MASTER was replaced in 8.2 and removed in 8.4.
func (d Dialect) ResetBinaryLogs() string {
	if d.Major == 8 && d.Minor < 2 {
		return "RESET MASTER"
	}
	return "RESET BINARY LOGS AND GTIDS"
}

// CloneNeedsExactPatch reports whether CLONE requires donor and recipient to
// run the same patch release: true before 8.0.37, which only requires the
// same series.
func (d Dialect) CloneNeedsExactPatch() bool {
	return d.Major == 8 && d.Minor == 0 && d.Patch < 37
}

// SameSeries reports whether d and o can replicate and clone between each
// other (8.0/8.0, 8.4/8.4, 9/9).
func (d Dialect) SameSeries(o Dialect) bool { return d.Series != "" && d.Series == o.Series }
