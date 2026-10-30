// Package web holds the panel's UI assets: HTML templates and static files
// (compiled Tailwind/daisyUI CSS and vendored Alpine.js), embedded in the
// binary. Rebuild them with scripts/tailwind.sh and scripts/vendor.sh.
package web

import "embed"

// FS contains templates/*.html and static/** (app.css, vendor/alpine.min.js).
//
//go:embed templates static
var FS embed.FS
