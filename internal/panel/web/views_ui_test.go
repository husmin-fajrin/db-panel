package web

import (
	"bytes"
	"os"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/husmin-fajrin/db-panel/internal/panel/monitor"
	"github.com/husmin-fajrin/db-panel/internal/panel/store"
)

func TestGaugeDash(t *testing.T) {
	for _, c := range []struct {
		pct  float64
		want string
	}{
		{0, "0 213.6"},
		{50, "106.8 213.6"},
		{100, "213.6 213.6"},
		{150, "213.6 213.6"},
		{-5, "0 213.6"},
		{33.3, "71.1 213.6"},
	} {
		if got := gaugeDash(c.pct); got != c.want {
			t.Errorf("gaugeDash(%v) = %q, want %q", c.pct, got, c.want)
		}
	}
}

func lagPtr(v int64) *int64 { return &v }

// TestChartPathBreaksOnGaps: a gap of more than two buckets between two
// points, or a nil value, starts a new segment; a gap of two buckets (one
// missing bucket) does not.
func TestChartPathBreaksOnGaps(t *testing.T) {
	t0 := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	at := func(buckets int) time.Time { return t0.Add(time.Duration(buckets) * chartBucket) }
	c := LagChart{From: t0, To: t0.Add(24 * time.Hour), MaxY: 100}

	gap := ChartSeries{Points: []ChartPoint{
		{at(0), lagPtr(10)}, {at(1), lagPtr(20)}, {at(3), lagPtr(30)}, // one missing bucket: same segment
		{at(6), lagPtr(40)}, {at(7), lagPtr(50)}, // three buckets after at(3): new segment
	}}
	segs := chartPath(gap, c, 600, 200)
	if len(segs) != 2 {
		t.Fatalf("gap: %d segments %q, want 2", len(segs), segs)
	}
	if got := strings.Count(segs[0], "L"); got != 2 {
		t.Errorf("first segment %q has %d lines, want 2", segs[0], got)
	}
	for _, s := range segs {
		if !strings.HasPrefix(s, "M") {
			t.Errorf("segment %q does not start with M", s)
		}
	}
	// x of at(0) is 0, y of 10 of 100 on 200 is 180.
	if !strings.HasPrefix(segs[0], "M0 180L") {
		t.Errorf("first segment %q, want it to start at M0 180", segs[0])
	}

	withNil := ChartSeries{Points: []ChartPoint{
		{at(0), lagPtr(10)}, {at(1), lagPtr(20)}, {at(2), nil}, {at(3), lagPtr(5)}, {at(4), lagPtr(5)},
	}}
	if segs := chartPath(withNil, c, 600, 200); len(segs) != 2 {
		t.Errorf("nil: %d segments %q, want 2", len(segs), segs)
	}

	// A lone point still shows (a zero-length line with round caps).
	lone := ChartSeries{Points: []ChartPoint{{at(0), lagPtr(10)}, {at(10), lagPtr(10)}}}
	segs = chartPath(lone, c, 600, 200)
	if len(segs) != 2 || !strings.Contains(segs[0], "L") {
		t.Errorf("lone points: %q, want 2 segments each with an L", segs)
	}
	// Points outside [From, To] are dropped and end the segment there.
	out := ChartSeries{Points: []ChartPoint{
		{t0.Add(-chartBucket), lagPtr(10)}, {at(0), lagPtr(10)}, {at(1), lagPtr(10)},
		{c.To.Add(chartBucket), lagPtr(10)},
	}}
	if segs := chartPath(out, c, 600, 200); len(segs) != 1 || strings.Count(segs[0], "L") != 1 {
		t.Errorf("out of window: %q, want one segment of two points", segs)
	}
	// Values above MaxY stay inside the chart.
	high := ChartSeries{Points: []ChartPoint{{at(0), lagPtr(500)}, {at(1), lagPtr(500)}}}
	if segs := chartPath(high, c, 600, 200); len(segs) != 1 || !strings.HasPrefix(segs[0], "M0 0L") {
		t.Errorf("clamped: %q, want y 0", segs)
	}
}

// TestChartMaxYFromThreshold: the y axis runs to max(threshold x 1.5,
// peak), rounded up to a multiple of 10; the peak and the series colours
// come from newLagChart.
func TestChartMaxYFromThreshold(t *testing.T) {
	t0 := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	t1 := t0.Add(24 * time.Hour)
	pts := func(v ...int64) []ChartPoint {
		var out []ChartPoint
		for i, x := range v {
			out = append(out, ChartPoint{T: t0.Add(time.Duration(i) * chartBucket), Lag: lagPtr(x)})
		}
		return out
	}
	c := newLagChart([]ChartSeries{{Name: "r1", Points: pts(5, 20)}}, t0, t1, 60, false)
	if c.MaxY != 90 {
		t.Errorf("threshold 60, peak 20: MaxY %d, want 90", c.MaxY)
	}
	c = newLagChart([]ChartSeries{{Name: "r1", Points: pts(5, 20)}, {Name: "r2", Points: pts(137, 3)}}, t0, t1, 60, false)
	if c.MaxY != 140 {
		t.Errorf("threshold 60, peak 137: MaxY %d, want 140", c.MaxY)
	}
	if c.Peak == nil || *c.Peak.Lag != 137 || c.PeakSeries != "r2" || !c.Peak.T.Equal(t0) {
		t.Errorf("peak %+v on %q, want 137 on r2 at t0", c.Peak, c.PeakSeries)
	}
	c = newLagChart([]ChartSeries{{Name: "r1", Points: pts(7)}}, t0, t1, 5, false)
	if c.MaxY != 10 {
		t.Errorf("threshold 5, peak 7: MaxY %d, want 10", c.MaxY)
	}
	// Points outside the window do not make the peak; unsorted points are
	// sorted, so the line does not zigzag back.
	c = newLagChart([]ChartSeries{{Name: "r1", Points: []ChartPoint{
		{T: t0.Add(-time.Hour), Lag: lagPtr(900)}, {T: t1.Add(time.Hour), Lag: lagPtr(800)},
		{T: t0.Add(2 * chartBucket), Lag: lagPtr(30)}, {T: t0, Lag: lagPtr(10)}, {T: t0.Add(chartBucket), Lag: lagPtr(20)},
	}}}, t0, t1, 60, false)
	if c.Peak == nil || *c.Peak.Lag != 30 || c.MaxY != 90 {
		t.Errorf("out-of-window peak: peak %+v MaxY %d, want 30 and 90", c.Peak, c.MaxY)
	}
	if !slices.IsSortedFunc(c.Series[0].Points, func(a, b ChartPoint) int { return a.T.Compare(b.T) }) {
		t.Errorf("points not sorted: %+v", c.Series[0].Points)
	}
	if segs := c.Paths(c.Series[0]); len(segs) != 1 || strings.Count(segs[0], "L") != 2 {
		t.Errorf("sorted series paths %q, want one segment of three points", segs)
	}
	c = newLagChart(nil, t0, t1, 0, true)
	if c.MaxY != 10 || c.Peak != nil || !c.Sparse {
		t.Errorf("empty chart: MaxY %d peak %v sparse %v, want 10, nil, true", c.MaxY, c.Peak, c.Sparse)
	}

	var many []ChartSeries
	for i := range 7 {
		many = append(many, ChartSeries{Name: string(rune('a' + i))})
	}
	c = newLagChart(many, t0, t1, 60, false)
	wantTone := []string{"primary", "warning", "success", "primary", "warning", "success", "primary"}
	for i, s := range c.Series {
		if s.Tone != wantTone[i] || s.Dashed != (i >= 3) {
			t.Errorf("series %d: tone %s dashed %v, want %s %v", i, s.Tone, s.Dashed, wantTone[i], i >= 3)
		}
	}
}

func TestChartTicksEverySixHours(t *testing.T) {
	from := time.Date(2026, 11, 8, 14, 5, 0, 0, time.Local)
	c := LagChart{From: from, To: from.Add(24 * time.Hour), MaxY: 90}
	ticks := chartTicks(c)
	var labels []string
	for _, tk := range ticks {
		labels = append(labels, tk.Label)
		if tk.X < 0 || tk.X > chartPlotW {
			t.Errorf("tick %s at x %v, outside the plot", tk.Label, tk.X)
		}
	}
	if want := []string{"18:00", "00:00", "06:00", "12:00"}; !slices.Equal(labels, want) {
		t.Errorf("ticks %v, want %v", labels, want)
	}
}

// TestLagChartTemplate: the chart is an image with a label naming the peak
// and the span, has a legend entry per series, a dashed line for the
// fourth series, the peak dot and, when sparse, the note.
func TestLagChartTemplate(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Date(2026, 11, 8, 14, 0, 0, 0, time.Local)
	var series []ChartSeries
	for i, name := range []string{"replika-a", "replika-b", "replika-c", "replika-d"} {
		series = append(series, ChartSeries{Name: name, Points: []ChartPoint{
			{T: t0, Lag: lagPtr(int64(10 * (i + 1)))}, {T: t0.Add(chartBucket), Lag: lagPtr(5)},
		}})
	}
	c := newLagChart(series, t0, t0.Add(24*time.Hour), 60, true)
	var buf bytes.Buffer
	if err := r.fragments.ExecuteTemplate(&buf, "lag-chart", c); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		`viewBox="0 0 640 200"`, `role="img"`, `aria-label="Lag replikasi`, "puncak 40 dtk", "replika-d",
		"replika-a", `stroke-dasharray="6 4"`, "grafik jarang: agent perlu dimutakhirkan", "<circle",
		"nada-primary", "nada-warning", "nada-success",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("lag-chart lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "style=") {
		t.Errorf("lag-chart has a style attribute (CSP)")
	}
	// The chart stops growing at 752 px, so its 11 px labels stay near
	// 13 px on wide cards.
	css, err := os.ReadFile("../../../web/static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`\.grafik-lag-geser\{[^}]*max-width:752px`).Match(css) {
		t.Errorf("web/static/app.css does not cap .grafik-lag-geser at 752px (run scripts/tailwind.sh)")
	}
	// Only the SVG scrolls (min 480 px); the legend with the peak and
	// threshold labels is outside the scroll box, so a 390 px screen
	// shows it whole.
	box := regexp.MustCompile(`(?s)<div class="grafik-lag-geser overflow-x-auto" data-grafik-geser><div class="min-w-\[480px\]">\s*<svg class="h-auto w-full" viewBox="0 0 640 200".*?</svg>\s*</div></div>`).FindStringIndex(out)
	if box == nil {
		t.Fatalf("no scroll box around the chart svg:\n%s", out)
	}
	inside, after := out[box[0]:box[1]], out[box[1]:]
	for _, s := range []string{`<span class="mono">puncak 40 dtk ·`, `ambang <span class="mono">60 dtk</span>`, "</svg>replika-a</span>"} {
		if strings.Contains(inside, s) || !strings.Contains(after, s) {
			t.Errorf("%q is not in the legend outside the scroll box", s)
		}
	}
}

func TestGaugeTemplate(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	g := Gauge{Label: "Disk", Sub: "/var/lib/mysql", Value: "71%", Tone: "warning", Aria: "Disk terpakai 71 persen", Percent: 71}
	if err := r.fragments.ExecuteTemplate(&buf, "gauge", g); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{`viewBox="0 0 84 84"`, `role="img"`, `aria-label="Disk terpakai 71 persen"`,
		`r="34"`, `stroke-dasharray="151.7 213.6"`, "nada-warning", ">71%<", ">Disk<", "/var/lib/mysql"} {
		if !strings.Contains(out, want) {
			t.Errorf("gauge lacks %q:\n%s", want, out)
		}
	}
	buf.Reset()
	if err := r.fragments.ExecuteTemplate(&buf, "gauge", Gauge{Label: "Lag", Value: "3 dtk"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), `aria-label="Lag 3 dtk"`) {
		t.Errorf("gauge without Aria has no fallback name:\n%s", buf.String())
	}
	buf.Reset()
	if err := r.fragments.ExecuteTemplate(&buf, "gauge", Gauge{Tone: "ungu"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "nada-neutral") {
		t.Errorf("unknown tone is not neutral:\n%s", buf.String())
	}
}

func TestBadgeCardEmptyTemplates(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	run := func(name string, data any) string {
		t.Helper()
		var buf bytes.Buffer
		if err := r.fragments.ExecuteTemplate(&buf, name, data); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	if got := run("badge", Badge{Tone: "error", Text: "Gagal"}); got != `<span class="lencana lencana-error">Gagal</span>` {
		t.Errorf("badge = %s", got)
	}
	if got := run("badge", Badge{Tone: "<x>", Text: "a"}); !strings.Contains(got, "lencana-neutral") {
		t.Errorf("badge with unknown tone = %s", got)
	}
	head := run("card-head", CardHead{Title: "Replika", Action: "Lihat semua", Href: "/replikasi"})
	for _, want := range []string{"<h2", "Replika", `href="/replikasi"`, "Lihat semua"} {
		if !strings.Contains(head, want) {
			t.Errorf("card-head lacks %q: %s", want, head)
		}
	}
	empty := run("empty", Empty{Text: "Belum ada replika.", Action: "Tambah replika", Href: "/replicas/new"})
	for _, want := range []string{"Belum ada replika.", `href="/replicas/new"`, "btn btn-primary", "Tambah replika", `aria-hidden="true"`} {
		if !strings.Contains(empty, want) {
			t.Errorf("empty lacks %q: %s", want, empty)
		}
	}
	if got := run("empty", Empty{Text: "Kosong."}); strings.Contains(got, "<a ") {
		t.Errorf("empty without Href has a link: %s", got)
	}
}

// goEmittedClasses are the CSS classes Go code puts into pages: badge
// classes of severities, job/step/replica states and Telegram states, and
// the timeline's icon colours.
func goEmittedClasses() []string {
	set := map[string]bool{}
	for _, s := range []monitor.Severity{monitor.SevOK, monitor.SevWarn, monitor.SevCrit, monitor.SevNeutral, monitor.Severity(99)} {
		set[severityClass(s)] = true
		set[toneOf(s)] = true
	}
	for _, b := range statusBadges {
		set[b.Class] = true
	}
	set[statusClass("tidak-dikenal")] = true
	for _, st := range []string{store.TelegramPending, store.TelegramSent, store.TelegramFailed, store.TelegramRecovered, ""} {
		set[alertRow{Alert: store.Alert{TelegramState: st}}.TelegramClass()] = true
	}
	set[auditEntry(store.AuditEntry{Outcome: auditOK}).Tone] = true
	set[auditEntry(store.AuditEntry{Outcome: "gagal"}).Tone] = true
	var out []string
	for c := range set {
		for f := range strings.FieldsSeq(c) {
			out = append(out, f)
		}
	}
	sort.Strings(out)
	return slices.Compact(out)
}

// TestGoEmittedClassesInCSS: every class Go emits is in the built
// stylesheet (Tailwind drops utilities it does not see in the templates),
// and web/src/app.css lists exactly those classes in @source inline.
func TestGoEmittedClassesInCSS(t *testing.T) {
	built, err := os.ReadFile("../../../web/static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	classes := goEmittedClasses()
	for _, c := range classes {
		sel := regexp.MustCompile(`\.` + regexp.QuoteMeta(cssEscape(c)) + `[^A-Za-z0-9_\\-]`)
		if !sel.Match(built) {
			t.Errorf("class %q is not in web/static/app.css (run scripts/tailwind.sh, list it in @source inline)", c)
		}
	}
	src, err := os.ReadFile("../../../web/src/app.css")
	if err != nil {
		t.Fatal(err)
	}
	var listed []string
	for _, m := range regexp.MustCompile(`@source inline\("([^"]*)"\)`).FindAllStringSubmatch(string(src), -1) {
		listed = append(listed, strings.Fields(m[1])...)
	}
	sort.Strings(listed)
	listed = slices.Compact(listed)
	if !slices.Equal(listed, classes) {
		t.Errorf("@source inline lists %v, Go emits %v", listed, classes)
	}
}

// cssEscape escapes the characters of a class name that CSS selectors
// escape (Tailwind's opacity slash, for one).
func cssEscape(c string) string {
	return strings.NewReplacer("/", `\/`, ":", `\:`, ".", `\.`).Replace(c)
}

// TestConfirmDialogDangerStyle: the dialog's confirm button is btn-primary
// unless the button that opened it carries data-confirm-danger="1"; then
// app.js swaps in btn-error and frames the warning in the error colour.
func TestConfirmDialogDangerStyle(t *testing.T) {
	r, err := NewRenderer()
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := r.fragments.ExecuteTemplate(&buf, "confirm-modal", nil); err != nil {
		t.Fatal(err)
	}
	dialog := buf.String()
	btn := regexp.MustCompile(`<button type="submit"[^>]*>Konfirmasi</button>`).FindString(dialog)
	if btn == "" {
		t.Fatalf("dialog has no Konfirmasi submit button:\n%s", dialog)
	}
	if !strings.Contains(btn, `class="btn btn-primary"`) || !strings.Contains(btn, `:class="tombolClass"`) {
		t.Errorf("confirm button %s: want static btn-primary bound to tombolClass", btn)
	}
	if !regexp.MustCompile(`x-text="text"[^>]*:class="teksClass"|:class="teksClass"[^>]*x-text="text"`).MatchString(dialog) {
		t.Errorf("warning text is not bound to teksClass:\n%s", dialog)
	}
	if !strings.Contains(dialog, `modal-box`) {
		t.Errorf("dialog lost modal-box (radius 12)")
	}
	js, err := os.ReadFile("../../../web/static/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`this.danger = btn.dataset.confirmDanger === "1";`,
		`return { "btn-error": this.danger, "btn-primary": !this.danger };`,
		`return { "kotak-bahaya": this.danger };`,
	} {
		if !strings.Contains(string(js), want) {
			t.Errorf("app.js lacks %q", want)
		}
	}
	css, err := os.ReadFile("../../../web/static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	for _, sel := range []string{".btn-error", ".btn-primary", ".kotak-bahaya", ".modal-box"} {
		if !strings.Contains(string(css), sel) {
			t.Errorf("web/static/app.css lacks %s", sel)
		}
	}
}

// TestChartFromWindow: the dashboard's chart window holds exactly 288
// buckets of 5 minutes (design spec 5.7), now's bucket included, so
// LagSeries never returns a 289th point.
func TestChartFromWindow(t *testing.T) {
	base := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	for _, off := range []time.Duration{0, time.Second, 2*time.Minute + 30*time.Second, 4*time.Minute + 59*time.Second, 7 * time.Hour} {
		now := base.Add(off)
		from := chartFrom(now)
		secs := int64(chartBucket / time.Second)
		if from.Unix()%secs != 0 {
			t.Errorf("now %v: from %v is not on a bucket boundary", now, from)
		}
		if n := (now.Unix()-from.Unix())/secs + 1; n != 288 {
			t.Errorf("now %v: %d buckets, want 288", now, n)
		}
		if chartBuckets != 288 || chartBuckets >= store.MaxLagBuckets {
			t.Errorf("chartBuckets = %d", chartBuckets)
		}
	}
}
