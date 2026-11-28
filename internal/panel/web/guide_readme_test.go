package web_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"html"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/husmin-fajrin/db-panel/internal/panel/orchestrator"
)

// guideTable is the guide page's ten guides, in order: their <h2> title
// (### in the README) and their anchor.
var guideTable = []struct{ id, title string }{
	{"promote-darurat", "Promote darurat"},
	{"bangun-ulang-master-lama", "Bangun ulang master lama"},
	{"jadwal-cadangan", "Memindahkan jadwal cadangan sesudah promote"},
	{"perpanjang-sertifikat", "Memperpanjang sertifikat"},
	{"lepas-kunci-dengan-tangan", "Melepas kunci master lama dengan tangan"},
	{"master-terkunci-tanpa-promote", "Master terkunci padahal tidak ada promote"},
	{"pakai-lagi-server-dikarantina", "Memakai lagi server yang dihapus saat masih dikarantina"},
	{"baris-merah", "Memperbaiki baris merah"},
	{"replika-di-luar-panel", "Replika yang tersambung di luar alur panel"},
	{"bekas-replika-mandiri", "Memakai bekas replika sebagai server mandiri"},
}

// guideB4 is B4 (spec 5.11), the condition of the guide
// "Memakai bekas replika sebagai server mandiri", written above its SQL.
const guideB4 = "Buka hanya bila server ini akan dipakai aplikasi sebagai server mandiri dan master lamanya tidak lagi " +
	"dipakai aplikasi mana pun. Bila master sedang tumbang dan replika ini harus menggantikannya, jangan lewat jalan " +
	"ini: pakai Promote darurat, yang juga mengkarantina master lama."

const (
	guideTemplatePath = "../../../web/templates/panduan.html"
	readmePath        = "../../../README.md"
	gateSourcePath    = "../orchestrator/gate.go"
	alertsSourcePath  = "../monitor/alerts.go"
)

var (
	guideH2Re  = regexp.MustCompile(`<h2[^>]*\bid="([^"]*)"[^>]*>([^<]*)</h2>`)
	guideH3Re  = regexp.MustCompile(`<h3[^>]*>([^<]*)</h3>`)
	guidePreRe = regexp.MustCompile(`(?s)<pre class="perintah">(.*?)</pre>`)
	anyTagRe   = regexp.MustCompile(`<[^>]*>`)
	spacesRe   = regexp.MustCompile(`\s+`)
	formatRe   = regexp.MustCompile(`%[sd]`)
)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// guideSection is one guide: its title, anchor (page only) and body.
type guideSection struct{ id, title, body string }

// pageGuides splits the guide page (rendered or its template) at its
// <h2 id> headings.
func pageGuides(page string) []guideSection {
	var out []guideSection
	ms := guideH2Re.FindAllStringSubmatchIndex(page, -1)
	for i, m := range ms {
		end := len(page)
		if i+1 < len(ms) {
			end = ms[i+1][0]
		}
		out = append(out, guideSection{id: page[m[2]:m[3]], title: page[m[4]:m[5]], body: page[m[1]:end]})
	}
	return out
}

// readmeGuides is the README's "## Panduan" section split at its ###
// headings.
func readmeGuides(t *testing.T, readme string) []guideSection {
	t.Helper()
	i := strings.Index(readme, "\n## Panduan\n")
	if i < 0 {
		t.Fatal("README has no \"## Panduan\" section")
	}
	sec := readme[i+1:]
	if j := strings.Index(sec[1:], "\n## "); j >= 0 {
		sec = sec[:j+2]
	}
	if !strings.Contains(readme[i:], "\n## Catatan audit\n") || strings.Index(readme, "\n## Catatan audit\n") < i {
		t.Error("\"## Panduan\" is not before \"## Catatan audit\"")
	}
	var out []guideSection
	var cur *guideSection
	for _, line := range strings.Split(sec, "\n") {
		if title, ok := strings.CutPrefix(line, "### "); ok {
			out = append(out, guideSection{title: strings.TrimSpace(title)})
			cur = &out[len(out)-1]
			continue
		}
		if cur != nil {
			cur.body += line + "\n"
		}
	}
	return out
}

// pageCommands is the text of each <pre class="perintah"> of body.
func pageCommands(body string) []string {
	var out []string
	for _, m := range guidePreRe.FindAllStringSubmatch(body, -1) {
		out = append(out, strings.Trim(html.UnescapeString(m[1]), "\n"))
	}
	return out
}

// readmeCommands is the text of each fenced block of body.
func readmeCommands(body string) []string {
	var out []string
	var block []string
	in := false
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case !in && strings.HasPrefix(trimmed, "```"):
			in, block = true, nil
		case in && trimmed == "```":
			in = false
			out = append(out, strings.Join(block, "\n"))
		case in:
			// Blocks inside a list item are indented by the item's width.
			block = append(block, line)
		}
	}
	return dedent(out)
}

// dedent removes from each block the indentation its first line has.
func dedent(blocks []string) []string {
	for i, b := range blocks {
		lines := strings.Split(b, "\n")
		ind := len(lines[0]) - len(strings.TrimLeft(lines[0], " "))
		for j, l := range lines {
			if len(l) >= ind && strings.TrimSpace(l[:ind]) == "" {
				lines[j] = l[ind:]
			}
		}
		blocks[i] = strings.Join(lines, "\n")
	}
	return blocks
}

// plainText is s without tags, unescaped, its white space folded.
func plainText(s string) string {
	return spacesRe.ReplaceAllString(html.UnescapeString(anyTagRe.ReplaceAllString(s, " ")), " ")
}

func TestGuidePageNeedsLogin(t *testing.T) {
	e := newEnv(t)
	rec := e.do("GET", "/panduan", nil, nil)
	wantCode(t, rec, http.StatusSeeOther)
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "/login") {
		t.Errorf("Location = %q, want the login page", loc)
	}
	if strings.Contains(rec.Body.String(), "Promote darurat") {
		t.Error("the guide shows without login")
	}
}

func TestGuidePageHasTenGuides(t *testing.T) {
	e := newEnv(t)
	if _, err := e.st.CreateServer("db-rahasia-9"); err != nil {
		t.Fatal(err)
	}
	rec := e.do("GET", "/panduan", nil, e.cookie())
	wantCode(t, rec, http.StatusOK)
	body := rec.Body.String()
	main := between(body, "<main", "</main>")
	if n := strings.Count(main, "<h2"); n != len(guideTable) {
		t.Errorf("page has %d <h2>, want %d", n, len(guideTable))
	}
	got := pageGuides(main)
	if len(got) != len(guideTable) {
		t.Fatalf("guides = %d, want %d:\n%s", len(got), len(guideTable), main)
	}
	for i, g := range guideTable {
		if got[i].id != g.id || got[i].title != g.title {
			t.Errorf("guide %d = %q #%s, want %q #%s", i+1, got[i].title, got[i].id, g.title, g.id)
		}
	}
	// The table of contents above the first guide links every guide.
	toc := main[:strings.Index(main, "<h2")]
	for _, g := range guideTable {
		if !strings.Contains(toc, `href="#`+g.id+`"`) || !strings.Contains(toc, ">"+g.title+"<") {
			t.Errorf("table of contents lacks %q (#%s)", g.title, g.id)
		}
	}
	// Static text: nothing of the installation, no inline script.
	for _, no := range []string{"db-rahasia-9", "<script>", " onclick="} {
		if strings.Contains(main, no) {
			t.Errorf("guide page shows %q", no)
		}
	}
}

// TestGuideMatchesREADME: the README's "## Panduan" has the page's ten
// guides, in the same order, and each guide the same commands in the same
// order.
func TestGuideMatchesREADME(t *testing.T) {
	page := pageGuides(readFile(t, guideTemplatePath))
	readme := readmeGuides(t, readFile(t, readmePath))
	if len(page) != len(guideTable) || len(readme) != len(guideTable) {
		t.Fatalf("guides: page %d, README %d, want %d", len(page), len(readme), len(guideTable))
	}
	for i, g := range guideTable {
		if page[i].title != g.title || page[i].id != g.id {
			t.Errorf("page guide %d = %q #%s, want %q #%s", i+1, page[i].title, page[i].id, g.title, g.id)
		}
		if readme[i].title != g.title {
			t.Errorf("README guide %d = %q, want %q", i+1, readme[i].title, g.title)
		}
		pc, rc := pageCommands(page[i].body), readmeCommands(readme[i].body)
		if len(pc) != len(rc) {
			t.Errorf("%s: page has %d commands, README %d:\npage %q\nREADME %q", g.title, len(pc), len(rc), pc, rc)
			continue
		}
		for j := range pc {
			if pc[j] != rc[j] {
				t.Errorf("%s: command %d differs:\npage   %q\nREADME %q", g.title, j+1, pc[j], rc[j])
			}
		}
	}
	// The text around the commands too: each guide reads the same on both
	// sides once tags and Markdown are gone, so a condition written above a
	// command cannot drift.
	for i, g := range guideTable {
		if pp, rp := pageProse(page[i].body), readmeProse(readme[i].body); pp != rp {
			a, b := firstDiff(pp, rp)
			t.Errorf("%s: text differs:\npage   …%s…\nREADME …%s…", g.title, a, b)
		}
	}
	// Every command block of the page is a perintah block (none is missed
	// by the comparison).
	all := readFile(t, guideTemplatePath)
	if n, m := strings.Count(all, "<pre"), strings.Count(all, `<pre class="perintah">`); n != m {
		t.Errorf("page has %d <pre>, of them %d perintah blocks", n, m)
	}
}

var (
	inlineTagRe  = regexp.MustCompile(`</?(a|strong|code)\b[^>]*>`)
	listMarkRe   = regexp.MustCompile(`(?m)^[ \t]*(\d+\.|-)[ \t]`)
	fenceRe      = regexp.MustCompile("(?m)^[ \t]*```[a-z]*[ \t]*$")
	subHeadingRe = regexp.MustCompile(`(?m)^#### `)
	actionRe     = regexp.MustCompile(`\{\{.*?\}\}`)
)

// pageProse is the text of a guide on the page: inline tags dropped, other
// tags a space, entities unescaped, white space folded.
func pageProse(body string) string {
	s := inlineTagRe.ReplaceAllString(actionRe.ReplaceAllString(body, ""), "")
	s = anyTagRe.ReplaceAllString(s, " ")
	return strings.TrimSpace(spacesRe.ReplaceAllString(html.UnescapeString(s), " "))
}

// readmeProse is the text of a guide in the README: list markers, fences,
// sub-heading marks, bold and code marks dropped, white space folded.
func readmeProse(body string) string {
	s := fenceRe.ReplaceAllString(body, " ")
	s = listMarkRe.ReplaceAllString(s, " ")
	s = subHeadingRe.ReplaceAllString(s, " ")
	s = strings.NewReplacer("**", "", "`", "").Replace(s)
	return strings.TrimSpace(spacesRe.ReplaceAllString(s, " "))
}

// firstDiff is a and b around the first byte where they differ.
func firstDiff(a, b string) (string, string) {
	i := 0
	for i < len(a) && i < len(b) && a[i] == b[i] {
		i++
	}
	from := max(0, i-40)
	return a[from:min(len(a), i+40)], b[from:min(len(b), i+40)]
}

// goConsts is the string constants of the Go file at path, by name, and
// their names in the order they are declared.
func goConsts(t *testing.T, path string) (map[string]string, []string) {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	vals := map[string]string{}
	var order []string
	var eval func(ast.Expr) (string, bool)
	eval = func(x ast.Expr) (string, bool) {
		switch x := x.(type) {
		case *ast.BasicLit:
			if x.Kind != token.STRING {
				return "", false
			}
			s, err := strconv.Unquote(x.Value)
			return s, err == nil
		case *ast.BinaryExpr:
			a, ok1 := eval(x.X)
			b, ok2 := eval(x.Y)
			return a + b, ok1 && ok2 && x.Op == token.ADD
		case *ast.ParenExpr:
			return eval(x.X)
		}
		return "", false
	}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if i < len(vs.Values) {
					if s, ok := eval(vs.Values[i]); ok {
						vals[name.Name] = s
						order = append(order, name.Name)
					}
				}
			}
		}
	}
	return vals, order
}

// textFragments is the literal parts of a format text: what a guide quoting
// it with placeholders for its values must hold.
func textFragments(format string) []string {
	var out []string
	for _, p := range formatRe.Split(strings.ReplaceAll(format, "%%", "%"), -1) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// TestGuideRedRowsMatchGate: "Memperbaiki baris merah" has one
// sub-section per precondition row, titled with the row's label, and
// quotes every detail and fix text of the rows as the code has them.
func TestGuideRedRowsMatchGate(t *testing.T) {
	gate, order := goConsts(t, gateSourcePath)
	var labels []string
	texts := map[string]string{}
	textRe := regexp.MustCompile(`^textG[0-9]+[a-z]*$`)
	for _, name := range order {
		switch {
		case strings.HasPrefix(name, "label"):
			labels = append(labels, gate[name])
		case textRe.MatchString(name) && name != "textG0":
			texts[name] = gate[name]
		}
	}
	if len(labels) != 12 || len(texts) < 30 {
		t.Fatalf("gate.go: %d labels, %d texts; the parse missed some", len(labels), len(texts))
	}
	alerts, _ := goConsts(t, alertsSourcePath)
	for _, name := range []string{"backupStaleText", "backupEmptyFileText", "backupNoneText", "backupUnreadableText",
		"BackupTruncatedText"} {
		if alerts[name] == "" {
			t.Fatalf("alerts.go has no %s", name)
		}
		texts[name] = alerts[name]
	}

	var page, readme guideSection
	for _, g := range pageGuides(readFile(t, guideTemplatePath)) {
		if g.id == "baris-merah" {
			page = g
		}
	}
	for _, g := range readmeGuides(t, readFile(t, readmePath)) {
		if g.title == "Memperbaiki baris merah" {
			readme = g
		}
	}
	var pageH3, readmeH4 []string
	for _, m := range guideH3Re.FindAllStringSubmatch(page.body, -1) {
		pageH3 = append(pageH3, html.UnescapeString(m[1]))
	}
	for _, line := range strings.Split(readme.body, "\n") {
		if h, ok := strings.CutPrefix(line, "#### "); ok {
			readmeH4 = append(readmeH4, strings.TrimSpace(h))
		}
	}
	if strings.Join(pageH3, "|") != strings.Join(labels, "|") {
		t.Errorf("page sub-sections = %q, want the rows' labels %q", pageH3, labels)
	}
	if strings.Join(readmeH4, "|") != strings.Join(labels, "|") {
		t.Errorf("README sub-sections = %q, want the rows' labels %q", readmeH4, labels)
	}
	pageText, readmeText := plainText(page.body), spacesRe.ReplaceAllString(readme.body, " ")
	for name, text := range texts {
		for _, frag := range textFragments(text) {
			if !strings.Contains(pageText, frag) {
				t.Errorf("page does not quote %s: lacks %q", name, frag)
			}
			if !strings.Contains(readmeText, frag) {
				t.Errorf("README does not quote %s: lacks %q", name, frag)
			}
		}
	}
}

// redAccount is a gate whose account row is red (G6a, G6f).
func redAccount(orchestrator.GateInput) (orchestrator.GateResult, error) {
	return orchestrator.GateResult{Rows: []orchestrator.GateRow{
		{Key: "agent_version", Label: "Versi agent"},
		{Key: "account", Label: "Akun panel", Status: orchestrator.GateRed, Detail: "Agent di db-calon masuk ke MySQL sebagai root.",
			Fix: "Buat akun MySQL khusus panel lalu hubungkan lagi (Panduan, \"Memperbaiki baris merah\"). Bila akunnya " +
				"sudah diganti di server, klik Segarkan di kartu MySQL halaman server itu."},
	}}, nil
}

const (
	linkPromote = `<a class="link" href="/panduan#promote-darurat">Panduan: Promote darurat</a>`
	linkRebuild = `<a class="link" href="/panduan#bangun-ulang-master-lama">Panduan: Bangun ulang master lama</a>`
	linkAccount = `<a class="link" href="/panduan#baris-merah">Panduan: Memperbaiki baris merah</a>`
	linkBackup  = `<a class="link" href="/panduan#jadwal-cadangan">Panduan: Memindahkan jadwal cadangan sesudah promote</a>`
	linkFormer  = `Panduan: "<a class="link" href="/panduan#bekas-replika-mandiri">Memakai bekas replika sebagai server mandiri</a>"`
	wantD8      = "Pindahkan jadwal cadangan (backup) ke master baru, lalu isi folder cadangan di halaman servernya."
)

func TestScreensLinkToGuides(t *testing.T) {
	// The emergency promote screen; not the planned one.
	e, f, _ := promoteEnv(t)
	c := e.cookie()
	f.setView(emergencyView())
	rec := e.do("GET", "/masters/1/promote", nil, c)
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, linkPromote)
	f.setView(plannedView())
	rec = e.do("GET", "/masters/1/promote", nil, c)
	wantNoBody(t, rec, "/panduan#promote-darurat")

	// G6f: the red account row, inside its row; not on another row.
	f.setGate(redAccount)
	rec = e.do("GET", "/masters/1/promote", nil, c)
	wantCode(t, rec, http.StatusOK)
	row := between(rec.Body.String(), `data-gate-row="account"`, "</li>")
	if !strings.Contains(row, linkAccount) {
		t.Errorf("red account row has no link to the guide:\n%s", row)
	}
	f.setGate(yellowOnly)
	rec = e.do("GET", "/masters/1/promote", nil, c)
	wantNoBody(t, rec, "/panduan#baris-merah")

	// The rebuild form.
	e2, _ := rebuildEnvF(t)
	rec = e2.do("GET", "/masters/2/replicas/new?bangun-ulang=1", nil, e2.cookie())
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, linkRebuild)

	// The cutover screen: D8 and its link.
	e3, _, _ := promotedEnv(t)
	rec = e3.do("GET", "/masters/2/cutover", nil, e3.cookie())
	wantCode(t, rec, http.StatusOK)
	wantBody(t, rec, wantD8, linkBackup)

	// The Lepas replika dialog, at its "Lepas dan hentikan replikasi"
	// choice (P7).
	e4, _, _ := controlEnv(t)
	rec = e4.do("GET", "/replicas/2", nil, e4.cookie())
	wantCode(t, rec, http.StatusOK)
	choice := between(rec.Body.String(), `name="mode" value="stop"`, "</label>")
	if !strings.Contains(choice, linkFormer) {
		t.Errorf("the detach choice has no link to the guide:\n%s", choice)
	}

	// Every anchor is on the guide page.
	rec = e3.do("GET", "/panduan", nil, e3.cookie())
	wantCode(t, rec, http.StatusOK)
	for _, id := range []string{"promote-darurat", "bangun-ulang-master-lama", "baris-merah", "jadwal-cadangan",
		"bekas-replika-mandiri"} {
		wantBody(t, rec, `id="`+id+`"`)
	}
}
