package web

// themeTokens are the colours of the panel's two daisyUI themes (design
// spec 5.1), by theme and role. web/src/app.css declares the same values as
// --color-<role> in each theme's block (TestThemeTokensMatchCSS keeps them
// equal), and TestContrastTokens checks every text/background pair on them.
//
// muted is the dimmed text colour (text-muted); success-text, warning-text
// and error-text are status colours for text on cards and the page
// (text-success-text, ...; badges), darker than the status colours in the
// light theme so they reach 4.5:1. The other roles are daisyUI's own.
func themeTokens() map[string]map[string]string {
	return map[string]map[string]string{
		"dbpanel-gelap": {
			"base-100":        "#12121F",
			"base-200":        "#0A0A14",
			"base-300":        "#2A2A3E",
			"base-content":    "#EEEEF0",
			"muted":           "#9999AA",
			"primary":         "#00F0FF",
			"primary-content": "#0A0A14",
			"info":            "#00F0FF",
			"info-content":    "#0A0A14",
			"success":         "#39FF14",
			"success-content": "#0A0A14",
			"success-text":    "#39FF14",
			"warning":         "#FFD600",
			"warning-content": "#0A0A14",
			"warning-text":    "#FFD600",
			"error":           "#FF006E",
			"error-content":   "#0A0A14",
			"error-text":      "#FF4D94",
			"neutral":         "#1A1A2E",
			"neutral-content": "#EEEEF0",
		},
		"dbpanel-terang": {
			"base-100":        "#FFFFFF",
			"base-200":        "#F4F6F9",
			"base-300":        "#DDE2EA",
			"base-content":    "#141424",
			"muted":           "#5E6577",
			"primary":         "#076F81",
			"primary-content": "#FFFFFF",
			"info":            "#076F81",
			"info-content":    "#FFFFFF",
			"success":         "#1E9E2E",
			"success-content": "#141424",
			"success-text":    "#15641F",
			"warning":         "#C98A00",
			"warning-content": "#141424",
			"warning-text":    "#7A5200",
			"error":           "#E0005F",
			"error-content":   "#FFFFFF",
			"error-text":      "#B8004E",
			"neutral":         "#E9EDF2",
			"neutral-content": "#141424",
		},
	}
}
