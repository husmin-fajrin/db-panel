package web

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Shared components (design spec 5.5): the data of the templates in
// _gauge.html, _chart.html and _ui.html.

// tones are the colours a gauge, badge or chart series can take; the
// classes nada-<tone> and lencana-<tone> in app.css set them.
var tones = map[string]bool{"primary": true, "success": true, "warning": true, "error": true, "neutral": true}

// toneName is tone when it is one of tones, else neutral.
func toneName(tone string) string {
	if tones[tone] {
		return tone
	}
	return "neutral"
}

// Badge is the data of the "badge" partial: Tone is one of tones.
type Badge struct{ Tone, Text string }

// CardHead is the data of the "card-head" partial: the card's H2 and, when
// Href is set, a link Action on the right.
type CardHead struct{ Title, Action, Href string }

// Empty is the data of the "empty" partial: a sentence and, when Href is
// set, a button Action.
type Empty struct{ Text, Action, Href string }

// Gauge is the data of the "gauge" partial: an 84 px ring filled to
// Percent in Tone, Value in its centre, Label and Sub beside it, Aria as
// its accessible name.
type Gauge struct {
	Label, Sub, Value, Tone, Aria string
	Percent                       float64
}

// gaugeCircumference is the length of the gauge's ring (r = 34).
const gaugeCircumference = 213.6

// gaugeDash is the stroke-dasharray of a gauge arc filled to percent,
// clamped to 0–100.
func gaugeDash(percent float64) string {
	if math.IsNaN(percent) || percent < 0 {
		percent = 0
	}
	percent = min(percent, 100)
	arc := math.Round(percent/100*gaugeCircumference*10) / 10
	return fmtNum(arc) + " " + fmtNum(gaugeCircumference)
}

// fmtNum writes a coordinate with at most one decimal.
func fmtNum(v float64) string {
	return strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64)
}

// ChartPoint is one bucket of a lag series; Lag is nil when the replica
// reported no lag then (replication not running).
type ChartPoint struct {
	T   time.Time
	Lag *int64
}

// ChartSeries is the lag of one replica; newLagChart sets Tone and Dashed.
type ChartSeries struct {
	Name, Tone string
	Points     []ChartPoint
	Dashed     bool
}

// LagChart is the data of the "lag-chart" partial: the series between
// From and To on a y axis from 0 to MaxY seconds, the threshold line, the
// highest point and its series, and whether the data is sparse (agents
// that send no monitor.sample).
type LagChart struct {
	Series       []ChartSeries
	From, To     time.Time
	MaxY         int64
	ThresholdSec int
	Peak         *ChartPoint
	PeakSeries   string
	Sparse       bool
}

// chartBucket is the width of one point of the lag history (design spec 5.7).
const chartBucket = 5 * time.Minute

// The plot area inside the chart's 640×200 viewBox: y labels on the left,
// hour labels below.
const (
	chartPlotX = 44.0
	chartPlotY = 12.0
	chartPlotW = 584.0
	chartPlotH = 160.0
)

// seriesTones colour the series in turn; from the fourth series on they
// repeat with a dashed line.
var seriesTones = []string{"primary", "warning", "success"}

// newLagChart makes a chart of series: colours, the peak, and a y axis to
// max(threshold × 1.5, peak) rounded up to a multiple of 10 (at least 10).
func newLagChart(series []ChartSeries, from, to time.Time, thresholdSec int, sparse bool) LagChart {
	c := LagChart{From: from, To: to, ThresholdSec: thresholdSec, Sparse: sparse}
	for i, s := range series {
		s.Tone = seriesTones[i%len(seriesTones)]
		s.Dashed = i >= len(seriesTones)
		c.Series = append(c.Series, s)
		for _, p := range s.Points {
			if p.Lag != nil && (c.Peak == nil || *p.Lag > *c.Peak.Lag) {
				c.Peak = &ChartPoint{T: p.T, Lag: p.Lag}
				c.PeakSeries = s.Name
			}
		}
	}
	top := int64(math.Ceil(float64(thresholdSec) * 1.5))
	if c.Peak != nil {
		top = max(top, *c.Peak.Lag)
	}
	c.MaxY = max((top+9)/10*10, 10)
	return c
}

// chartXY places a point in a w×h box: time across, lag up (clamped to
// MaxY).
func chartXY(c LagChart, t time.Time, lag int64, w, h float64) (float64, float64) {
	span := c.To.Sub(c.From)
	x := 0.0
	if span > 0 {
		x = float64(t.Sub(c.From)) / float64(span) * w
	}
	x = min(max(x, 0), w)
	maxY := max(c.MaxY, 1)
	y := h - float64(min(max(lag, 0), maxY))/float64(maxY)*h
	return x, y
}

// chartPath is the line of series s in a w×h box as SVG path segments
// ("M x y L x y …"): a nil value, or a gap of more than two buckets
// between points, ends a segment. A lone point is a zero-length line,
// which round caps draw as a dot.
func chartPath(s ChartSeries, c LagChart, w, h float64) []string {
	var segs []string
	var b strings.Builder
	n := 0
	var last time.Time
	var first string
	flush := func() {
		if n == 1 {
			b.WriteString("L" + first)
		}
		if n > 0 {
			segs = append(segs, b.String())
		}
		b.Reset()
		n = 0
	}
	for _, p := range s.Points {
		if p.Lag == nil {
			flush()
			continue
		}
		if n > 0 && p.T.Sub(last) > 2*chartBucket {
			flush()
		}
		x, y := chartXY(c, p.T, *p.Lag, w, h)
		xy := fmtNum(x) + " " + fmtNum(y)
		if n == 0 {
			first = xy
			b.WriteString("M" + xy)
		} else {
			b.WriteString("L" + xy)
		}
		n++
		last = p.T
	}
	flush()
	return segs
}

// tick is a grid line of the chart in plot coordinates with its label.
type tick struct {
	X, Y  float64
	Label string
}

// chartTicks are the hour labels every 6 hours (00:00, 06:00, 12:00,
// 18:00 local time) between From and To.
func chartTicks(c LagChart) []tick {
	var out []tick
	if !c.To.After(c.From) {
		return out
	}
	from := c.From.Local()
	t := time.Date(from.Year(), from.Month(), from.Day(), from.Hour()/6*6, 0, 0, 0, from.Location())
	for ; !t.After(c.To); t = t.Add(6 * time.Hour) {
		if t.Before(c.From) {
			continue
		}
		x, _ := chartXY(c, t, 0, chartPlotW, chartPlotH)
		out = append(out, tick{X: x, Y: chartPlotH, Label: t.Format("15:04")})
	}
	return out
}

// YTicks are the horizontal grid lines: 0, half and MaxY seconds.
func (c LagChart) YTicks() []tick {
	var out []tick
	for _, v := range []int64{0, c.MaxY / 2, c.MaxY} {
		_, y := chartXY(c, c.From, v, chartPlotW, chartPlotH)
		out = append(out, tick{Y: y, Label: strconv.FormatInt(v, 10)})
	}
	return out
}

// XTicks are chartTicks for the template.
func (c LagChart) XTicks() []tick { return chartTicks(c) }

// Paths are chartPath of s in the plot area.
func (c LagChart) Paths(s ChartSeries) []string { return chartPath(s, c, chartPlotW, chartPlotH) }

// ThresholdY is the y of the threshold line, or -1 when there is none or
// it lies above the axis.
func (c LagChart) ThresholdY() float64 {
	if c.ThresholdSec <= 0 || int64(c.ThresholdSec) > c.MaxY {
		return -1
	}
	_, y := chartXY(c, c.From, int64(c.ThresholdSec), chartPlotW, chartPlotH)
	return y
}

// PeakX and PeakY place the peak's dot; PeakAnchor keeps its label
// inside the chart near the edges.
func (c LagChart) PeakX() float64 {
	if c.Peak == nil {
		return 0
	}
	x, _ := chartXY(c, c.Peak.T, *c.Peak.Lag, chartPlotW, chartPlotH)
	return x
}

func (c LagChart) PeakY() float64 {
	if c.Peak == nil {
		return 0
	}
	_, y := chartXY(c, c.Peak.T, *c.Peak.Lag, chartPlotW, chartPlotH)
	return y
}

func (c LagChart) PeakAnchor() string {
	x := c.PeakX()
	switch {
	case c.peakAtTop() && x > chartPlotW*0.7:
		return "end"
	case c.peakAtTop():
		return "start"
	case x < chartPlotW*0.15:
		return "start"
	case x > chartPlotW*0.85:
		return "end"
	}
	return "middle"
}

// peakAtTop: the peak's dot is too near the top for a label above it, so
// the label goes beside it.
func (c LagChart) peakAtTop() bool { return c.PeakY() < 16 }

// PeakLabelX and PeakLabelY place the peak's label above the dot, or
// beside it when the dot is at the top of the chart.
func (c LagChart) PeakLabelX() float64 {
	switch {
	case !c.peakAtTop():
		return c.PeakX()
	case c.PeakAnchor() == "end":
		return c.PeakX() - 10
	}
	return c.PeakX() + 10
}

func (c LagChart) PeakLabelY() float64 {
	if c.peakAtTop() {
		return c.PeakY() + 4
	}
	return c.PeakY() - 8
}

// PeakTone is the tone of the peak's series, for its dot.
func (c LagChart) PeakTone() string {
	for _, s := range c.Series {
		if s.Name == c.PeakSeries {
			return s.Tone
		}
	}
	return "neutral"
}

// PeakLabel words the peak: "puncak 137 dtk · 14:05".
func (c LagChart) PeakLabel() string {
	if c.Peak == nil {
		return ""
	}
	return fmt.Sprintf("puncak %d dtk · %s", *c.Peak.Lag, c.Peak.T.Local().Format("15:04"))
}

// Aria is the chart's accessible name: the span and the peak.
func (c LagChart) Aria() string {
	span := fmt.Sprintf("Lag replikasi %s sampai %s", c.From.Local().Format("2006-01-02 15:04"), c.To.Local().Format("2006-01-02 15:04"))
	if c.Peak == nil {
		return span + ": belum ada data lag."
	}
	return fmt.Sprintf("%s: puncak %d dtk pada %s pukul %s.", span, *c.Peak.Lag, c.PeakSeries, c.Peak.T.Local().Format("15:04"))
}

// uiFuncs are the template functions of the shared components.
var uiFuncs = map[string]any{
	"gaugeDash": gaugeDash,
	"num":       fmtNum,
	// nada is the tone class of a gauge or chart series (nada-<tone>).
	"nada": func(tone string) string { return "nada-" + toneName(tone) },
	// lencanaClass is the class of a badge in tone (lencana-<tone>).
	"lencanaClass": func(tone string) string { return "lencana-" + toneName(tone) },
	"badgeOf":      func(tone, text string) Badge { return Badge{Tone: tone, Text: text} },
	"cardHead":     func(title, action, href string) CardHead { return CardHead{Title: title, Action: action, Href: href} },
	"emptyOf":      func(text, action, href string) Empty { return Empty{Text: text, Action: action, Href: href} },
}
