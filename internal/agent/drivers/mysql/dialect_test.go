package mysql

import (
	"errors"
	"testing"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in                  string
		major, minor, patch int
		series              string
		err                 error
	}{
		{in: "8.0.46-0ubuntu0.24.04.4", major: 8, minor: 0, patch: 46, series: "8.0"},
		{in: "8.0.26", major: 8, minor: 0, patch: 26, series: "8.0"},
		{in: "8.0.36-log", major: 8, minor: 0, patch: 36, series: "8.0"},
		{in: "8.4.3", major: 8, minor: 4, patch: 3, series: "8.4"},
		{in: "9.1.0", major: 9, minor: 1, patch: 0, series: "9"},
		{in: "8.0.25", err: ErrUnsupported},
		{in: "5.7.44-log", err: ErrUnsupported},
		{in: "10.11.6-MariaDB-0ubuntu0.24.04.1", err: ErrUnsupported},
		{in: "5.5.5-10.6.12-MariaDB", err: ErrUnsupported},
		{in: "", err: ErrUnsupported},
		{in: "garbage", err: ErrUnsupported},
	}
	for _, tt := range tests {
		d, err := ParseVersion(tt.in)
		if tt.err != nil {
			if !errors.Is(err, tt.err) {
				t.Errorf("ParseVersion(%q) err = %v, want %v", tt.in, err, tt.err)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseVersion(%q): %v", tt.in, err)
			continue
		}
		if d.Major != tt.major || d.Minor != tt.minor || d.Patch != tt.patch || d.Series != tt.series || d.Version != tt.in {
			t.Errorf("ParseVersion(%q) = %+v", tt.in, d)
		}
	}
}

func TestResetBinaryLogsByVersion(t *testing.T) {
	tests := []struct {
		v          string
		reset      string
		exactPatch bool
	}{
		{"8.0.26", "RESET MASTER", true},
		{"8.0.36", "RESET MASTER", true},
		{"8.0.37", "RESET MASTER", false},
		{"8.0.46-0ubuntu0.24.04.4", "RESET MASTER", false},
		{"8.4.3", "RESET BINARY LOGS AND GTIDS", false},
		{"9.1.0", "RESET BINARY LOGS AND GTIDS", false},
	}
	for _, tt := range tests {
		d, err := ParseVersion(tt.v)
		if err != nil {
			t.Fatalf("ParseVersion(%q): %v", tt.v, err)
		}
		if got := d.ResetBinaryLogs(); got != tt.reset {
			t.Errorf("%s ResetBinaryLogs = %q, want %q", tt.v, got, tt.reset)
		}
		if got := d.CloneNeedsExactPatch(); got != tt.exactPatch {
			t.Errorf("%s CloneNeedsExactPatch = %v, want %v", tt.v, got, tt.exactPatch)
		}
	}
	a, _ := ParseVersion("8.0.40")
	b, _ := ParseVersion("8.0.46-0ubuntu0.24.04.4")
	c, _ := ParseVersion("8.4.3")
	if !a.SameSeries(b) || a.SameSeries(c) {
		t.Error("SameSeries: 8.0/8.0 must match, 8.0/8.4 must not")
	}
}
