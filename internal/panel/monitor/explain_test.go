package monitor

import "testing"

func TestExplainTable(t *testing.T) {
	cases := []struct {
		name        string
		sql, io     int
		wantText    string
		wantAdvice  string
		wantPrimary string
	}{
		{"1062 duplicate", 1062, 0, "Data ganda", "Resync ulang", "resync"},
		{"1032 missing row", 1032, 0, "Baris tidak ditemukan", "Resync ulang", "resync"},
		{"1236 binlog gone", 0, 1236, "Binlog master", "Resync ulang", "resync"},
		{"1594 relay log", 1594, 0, "Relay log", "Start replika", "start"},
		{"1021 disk full", 1021, 0, "Disk replika penuh", "Bereskan disk", "start"},
		{"1114 table full", 1114, 0, "Disk replika penuh", "Bereskan disk", "start"},
		{"3 disk write", 3, 0, "Disk replika penuh", "Bereskan disk", "start"},
		{"1045 denied", 0, 1045, "Akun replikasi ditolak", "Periksa akun replikasi", "master"},
		{"1593 server id", 0, 1593, "Akun replikasi ditolak", "Periksa akun replikasi", "master"},
		{"2003 unreachable", 0, 2003, "Master tidak bisa dihubungi", "Periksa jaringan", "master"},
		{"2013 lost", 0, 2013, "Master tidak bisa dihubungi", "Periksa jaringan", "master"},
		{"unknown", 9999, 0, "Penyebab belum dikenali panel.", "", ""},
		{"none", 0, 0, "Penyebab belum dikenali panel.", "", ""},
		{"sql errno wins over io", 1062, 2003, "Data ganda", "Resync ulang", "resync"},
		{"unknown sql falls to io", 9999, 2003, "Master tidak bisa dihubungi", "Periksa jaringan", "master"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Explain(c.sql, c.io)
			if !hasPrefix(got.Text, c.wantText) {
				t.Errorf("Text = %q, want prefix %q", got.Text, c.wantText)
			}
			if !hasPrefix(got.Advice, c.wantAdvice) {
				t.Errorf("Advice = %q, want prefix %q", got.Advice, c.wantAdvice)
			}
			if c.wantAdvice == "" && got.Advice != "" {
				t.Errorf("Advice = %q, want empty", got.Advice)
			}
			if got.Primary != c.wantPrimary {
				t.Errorf("Primary = %q, want %q", got.Primary, c.wantPrimary)
			}
		})
	}
}

func TestExplainUnknownExact(t *testing.T) {
	got := Explain(9999, 0)
	want := Explanation{Text: "Penyebab belum dikenali panel."}
	if got != want {
		t.Errorf("Explain(9999,0) = %+v, want %+v", got, want)
	}
}

func hasPrefix(s, p string) bool { return len(s) >= len(p) && s[:len(p)] == p }
