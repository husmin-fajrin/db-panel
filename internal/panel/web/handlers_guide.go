package web

import "net/http"

// guidePage serves GET /panduan: the step-by-step guides (Panduan), the
// same text as the README's "## Panduan" section (guide_readme_test.go).
// It is static: it shows nothing of this installation beyond the layout.
func (a *app) guidePage(w http.ResponseWriter, r *http.Request) {
	a.render.Render(w, "panduan", a.page(r))
}
