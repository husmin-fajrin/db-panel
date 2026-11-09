package web

import (
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// luminance is the WCAG 2 relative luminance of a #RRGGBB colour.
func luminance(t *testing.T, hex string) float64 {
	t.Helper()
	if len(hex) != 7 || hex[0] != '#' {
		t.Fatalf("colour %q is not #RRGGBB", hex)
	}
	ch := func(s string) float64 {
		v, err := strconv.ParseUint(s, 16, 8)
		if err != nil {
			t.Fatalf("colour %q: %v", hex, err)
		}
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*ch(hex[1:3]) + 0.7152*ch(hex[3:5]) + 0.0722*ch(hex[5:7])
}

// contrast is the WCAG 2 contrast ratio of two colours.
func contrast(t *testing.T, a, b string) float64 {
	t.Helper()
	la, lb := luminance(t, a), luminance(t, b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// TestContrastTokens: every text/background pair of the design tokens
// reaches 4.5:1 (WCAG AA for body text) in both themes.
func TestContrastTokens(t *testing.T) {
	pairs := [][2]string{ // text, background
		{"base-content", "base-100"},
		{"base-content", "base-200"},
		{"muted", "base-100"},
		{"muted", "base-200"},
		{"primary-content", "primary"},
		{"error-text", "base-100"},
		{"warning-content", "warning"},
		{"success-content", "success"},
		{"neutral-content", "neutral"},
		{"primary", "base-100"}, // links on cards and the sidebar
		{"primary", "base-200"}, // links and the logo on the page background
		{"error-content", "error"},
		{"info-content", "info"},
	}
	tokens := themeTokens()
	for _, theme := range []string{"dbpanel-gelap", "dbpanel-terang"} {
		tok, ok := tokens[theme]
		if !ok {
			t.Fatalf("themeTokens has no %s", theme)
		}
		for _, p := range pairs {
			fg, bg := tok[p[0]], tok[p[1]]
			if fg == "" || bg == "" {
				t.Errorf("%s: token %s or %s missing", theme, p[0], p[1])
				continue
			}
			if c := contrast(t, fg, bg); c < 4.5 {
				t.Errorf("%s: %s %s on %s %s = %.2f:1, want >= 4.5", theme, p[0], fg, p[1], bg, c)
			}
		}
	}
}

// TestThemeTokensMatchCSS: web/src/app.css declares every token of
// themeTokens with the same value inside that theme's block, so the
// contrast test checks the colours the browser gets.
func TestThemeTokensMatchCSS(t *testing.T) {
	raw, err := os.ReadFile("../../../web/src/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	for theme, tok := range themeTokens() {
		block := regexp.MustCompile(`(?s)@plugin\s+"[^"]*daisyui-theme\.js"\s*\{[^}]*name:\s*"` +
			regexp.QuoteMeta(theme) + `"[^}]*\}`).FindString(css)
		if block == "" {
			t.Errorf("app.css has no daisyUI theme block %s", theme)
			continue
		}
		for role, hex := range tok {
			re := regexp.MustCompile(`--color-` + regexp.QuoteMeta(role) + `:\s*([^;]+);`)
			m := re.FindStringSubmatch(block)
			if m == nil {
				t.Errorf("%s: app.css does not set --color-%s", theme, role)
				continue
			}
			if got := strings.TrimSpace(m[1]); !strings.EqualFold(got, hex) {
				t.Errorf("%s: --color-%s = %s in app.css, %s in themeTokens", theme, role, got, hex)
			}
		}
	}
}

// TestBuiltCSSMenuAndFocus: the built stylesheet marks the current menu
// entry in primary (cyan) and gives menu entries, buttons and inputs a
// primary focus ring, outside daisyUI's layer so they win.
func TestBuiltCSSMenuAndFocus(t *testing.T) {
	raw, err := os.ReadFile("../../../web/static/app.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(raw)
	// Unlayered rules come after the last @layer block's end; checking the
	// exact minified text keeps the test simple.
	for _, want := range []string{
		".menu{--menu-active-bg:var(--color-primary);--menu-active-fg:var(--color-primary-content)}",
		".menu li>a:focus-visible,",
		// The minifier merges the button and input focus rules.
		".btn:focus-visible,.input:focus,",
		"{outline:2px solid var(--color-primary);outline-offset:2px}",
	} {
		i := strings.LastIndex(css, want)
		if i < 0 {
			t.Errorf("web/static/app.css lacks %q (run scripts/tailwind.sh)", want)
			continue
		}
		if j := strings.LastIndex(css, "@layer utilities{"); j > i {
			t.Errorf("%q comes before the utilities layer: it must be unlayered", want)
		}
	}
}
