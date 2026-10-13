package main

// W3: /static/core + /static/vendor serving. The embed FS (cmd/app/static)
// holds verbatim copies of the web/static sources — go:embed cannot reach
// outside the package dir, so a file-equality test pins the copies (same
// pattern as the sheetCSS inline copy pinned by TestSheetCSSMatchesFile in
// internal/web). HTTP tests prove the sheet <link> and the CodeMirror hook
// resolve with the runbook §5 cache directives; traversal/listings 404.

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/web"
)

// scopeRe matches one CSS rule's selector (the text before each `{`). Used by
// TestThemeStylesheetsCoverAllSlots to prove no unscoped selector leaks out
// of a theme file (the plugin CSS linter covers plugins; themes are checked
// here).
var (
	scopeRe   = regexp.MustCompile(`([^{}]+)\{`)
	commentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

// maxThemeBytes mirrors the theme-weight budget in tools/budgets
// (web/static/themes must stay inside one rendered page's weight).
const maxThemeBytes = 100 * 1024

// coreThemeTokens are the tokens core CSS defines and consumes (app.css
// :root, statblock.css :root blocks, and its per-callout-type accents). Every
// theme must re-point each one in EVERY scheme: a missing re-point silently
// leaves the light-scheme default inside a dark-scheme page — the failure a
// screenshot would show, pinned here instead.
var coreThemeTokens = []string{
	"--ink", "--paper", "--surface", "--accent", "--on-accent", "--line", "--control-line",
	"--input-line",
	"--focus", "--hint", "--warn", "--danger", "--danger-bg",
	"--topnav-bg", "--topnav-ink", "--topnav-link", "--topnav-line", "--topnav-warn",
	"--font-body", "--font-display",
	"--card-line", "--card-bg", "--card-head-bg", "--card-head-ink", "--card-radius",
	"--sb-paper", "--sb-red", "--sb-rule", "--sb-amber", "--sb-ink",
	"--callout-bg", "--callout-ink",
	"--callout-note", "--callout-abstract", "--callout-info", "--callout-tip",
	"--callout-success", "--callout-question", "--callout-warning",
	"--callout-failure", "--callout-danger", "--callout-bug", "--callout-example",
	"--callout-quote", "--callout-default",
}

var w3StaticPairs = []struct{ src, embedded string }{
	{"web/static/core/sheet.css", "static/core/sheet.css"},
	{"web/static/core/statblock.css", "static/core/statblock.css"},
	{"web/static/core/app.css", "static/core/app.css"},
	{"web/static/vendor/codemirror.js", "static/vendor/codemirror.js"},
	{"web/static/vendor/datastar.js", "static/vendor/datastar.js"},
	{"web/static/vendor/pixi.js", "static/vendor/pixi.js"},
	{"web/static/vtt/pixi-board.js", "static/vtt/pixi-board.js"},
	{"web/static/themes/grimoire.css", "static/themes/grimoire.css"},
	{"web/static/themes/cartographer.css", "static/themes/cartographer.css"},
	{"web/static/themes/preview-grimoire.html", "static/themes/preview-grimoire.html"},
	{"web/static/themes/preview-cartographer.html", "static/themes/preview-cartographer.html"},
}

// Embedded copies must stay byte-identical to the web/static sources.
// Edit the source first, then copy it over.
func TestW3StaticCopiesMatchSources(t *testing.T) {
	for _, p := range w3StaticPairs {
		src, err := os.ReadFile(filepath.Join("..", "..", p.src))
		if err != nil {
			t.Fatalf("read source %s: %v", p.src, err)
		}
		got, err := staticFS.ReadFile(p.embedded)
		if err != nil {
			t.Fatalf("read embedded %s: %v", p.embedded, err)
		}
		if string(got) != string(src) {
			t.Errorf("embedded %s drifted from %s (copy the file verbatim)", p.embedded, p.src)
		}
	}
}

func TestW3StaticServing(t *testing.T) {
	core := staticDirHandler("/static/core/", "static/core", staticCoreCache)
	vendor := staticDirHandler("/static/vendor/", "static/vendor", staticVendorCache)
	themes := staticDirHandler("/static/themes/", "static/themes", staticCoreCache)
	vttLoader := staticDirHandler("/static/vtt/", "static/vtt", staticVTTCache)

	get := func(h http.Handler, method, target string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
		return rec
	}

	// Sheet stylesheet: CSS type, day-long public cache, nosniff.
	rec := get(core, http.MethodGet, "/static/core/sheet.css")
	if rec.Code != http.StatusOK {
		t.Fatalf("core sheet.css = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
		t.Errorf("core content-type = %q, want text/css", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != staticCoreCache {
		t.Errorf("core Cache-Control = %q, want %q", cc, staticCoreCache)
	}
	if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Error("core missing X-Content-Type-Options: nosniff")
	}
	if !strings.Contains(rec.Body.String(), ".sheet") {
		t.Error("core sheet.css body missing sheet rules")
	}

	// Theme stylesheets: CSS type, day-long public cache, nosniff.
	for _, target := range []string{"/static/themes/grimoire.css", "/static/themes/cartographer.css"} {
		rec := get(themes, http.MethodGet, target)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200", target, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/css") {
			t.Errorf("%s content-type = %q, want text/css", target, ct)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != staticCoreCache {
			t.Errorf("%s Cache-Control = %q, want %q", target, cc, staticCoreCache)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s missing X-Content-Type-Options: nosniff", target)
		}
	}

	// First-party VTT loader (A-VTT2): JS type, day-long public cache, and
	// the dynamic import target the page's module script names.
	rec = get(vttLoader, http.MethodGet, "/static/vtt/pixi-board.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("/static/vtt/pixi-board.js = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("vtt content-type = %q, want javascript", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != staticVTTCache {
		t.Errorf("vtt Cache-Control = %q, want %q", cc, staticVTTCache)
	}
	if !strings.Contains(rec.Body.String(), "bootPixi") {
		t.Error("pixi-board.js body missing bootPixi export")
	}

	// Vendor bundles: JS type, immutable year-long cache.
	for _, target := range []string{"/static/vendor/codemirror.js", "/static/vendor/datastar.js"} {
		rec := get(vendor, http.MethodGet, target)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d, want 200", target, rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
			t.Errorf("%s content-type = %q, want javascript", target, ct)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != staticVendorCache {
			t.Errorf("%s Cache-Control = %q, want %q", target, cc, staticVendorCache)
		}
		if !strings.Contains(rec.Header().Get("Cache-Control"), "immutable") {
			t.Errorf("%s cache missing immutable", target)
		}
	}

	// Served vendor bytes must match the VERSIONS pins (`yonder vendor
	// verify` pins the files; this pins what we actually serve).
	pins := map[string]string{}
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "static", "vendor", "VERSIONS"))
	if err != nil {
		t.Fatalf("read VERSIONS: %v", err)
	}
	for _, line := range strings.Split(string(raw), "\n") {
		f := strings.Fields(strings.TrimSpace(line))
		if len(f) < 4 || strings.HasPrefix(f[0], "#") {
			continue
		}
		for _, extra := range f[4:] {
			if file, ok := strings.CutPrefix(extra, "file="); ok {
				pins[file] = f[2]
			}
		}
	}
	for file, want := range pins {
		rec := get(vendor, http.MethodGet, "/static/vendor/"+file)
		if rec.Code != http.StatusOK {
			t.Fatalf("pinned vendor %s = %d, want 200", file, rec.Code)
		}
		sum := sha256.Sum256(rec.Body.Bytes())
		if got := hex.EncodeToString(sum[:]); !strings.EqualFold(got, want) {
			t.Errorf("served %s sha256 = %s, want pinned %s", file, got, want)
		}
	}

	// Directory roots, unknown files, traversal escapes, and non-read
	// methods never serve bytes.
	for _, tc := range []struct {
		h      http.Handler
		method string
		target string
		want   int
	}{
		{core, http.MethodGet, "/static/core/", http.StatusNotFound},
		{vendor, http.MethodGet, "/static/vendor/", http.StatusNotFound},
		{core, http.MethodGet, "/static/core/nope.css", http.StatusNotFound},
		{vendor, http.MethodGet, "/static/vendor/nope.js", http.StatusNotFound},
		{core, http.MethodGet, "/static/core/../print.css", http.StatusNotFound},
		{vendor, http.MethodGet, "/static/vendor/../../go.mod", http.StatusNotFound},
		{themes, http.MethodGet, "/static/themes/", http.StatusNotFound},
		{themes, http.MethodGet, "/static/themes/nope.css", http.StatusNotFound},
		{themes, http.MethodGet, "/static/themes/../app.css", http.StatusNotFound},
		{vttLoader, http.MethodGet, "/static/vtt/", http.StatusNotFound},
		{vttLoader, http.MethodGet, "/static/vtt/nope.js", http.StatusNotFound},
		{vttLoader, http.MethodGet, "/static/vtt/../../go.mod", http.StatusNotFound},
		{core, http.MethodPost, "/static/core/sheet.css", http.StatusMethodNotAllowed},
		{vendor, http.MethodPost, "/static/vendor/codemirror.js", http.StatusMethodNotAllowed},
		{vttLoader, http.MethodPost, "/static/vtt/pixi-board.js", http.StatusMethodNotAllowed},
		{themes, http.MethodPost, "/static/themes/grimoire.css", http.StatusMethodNotAllowed},
	} {
		if rec := get(tc.h, tc.method, tc.target); rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.target, rec.Code, tc.want)
		}
	}
}

// The carve-out covers exactly the embedded public subtrees: registry and
// vault-adjacent paths (and unknown paths, which keep the styled 404) must
// not divert to the ops mux.
func TestW3IsPublicStatic(t *testing.T) {
	for _, p := range []string{
		"/static/print.css",
		"/static/core", "/static/core/",
		"/static/core/sheet.css",
		"/static/vendor", "/static/vendor/",
		"/static/vendor/codemirror.js", "/static/vendor/datastar.js",
		"/static/vendor/pixi.js",
		"/static/vtt", "/static/vtt/",
		"/static/vtt/pixi-board.js",
		"/static/themes", "/static/themes/",
		"/static/themes/grimoire.css", "/static/themes/cartographer.css",
	} {
		if !isPublicStatic(p) {
			t.Errorf("isPublicStatic(%q) = false, want true", p)
		}
	}
	for _, p := range []string{
		"/", "/healthz", "/version", "/p/welcome.md", "/assets/x.png",
		"/bogus", "/static/other.css", "/static/core-evil/sheet.css",
		"/static/themes-evil/grimoire.css", "/static/vtt-evil/pixi-board.js",
	} {
		if isPublicStatic(p) {
			t.Errorf("isPublicStatic(%q) = true, want false", p)
		}
	}
}

// End-to-end over HTTP through the real mux: the sheet <link> and the
// CodeMirror hook resolve (not just the handler in isolation), while the
// print.css mount keeps working.
func TestW3StaticOverHTTP(t *testing.T) {
	vault := t.TempDir()
	if err := os.WriteFile(filepath.Join(vault, "welcome.md"), []byte("---\ntitle: Welcome\n---\n\nHello.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(vault, "campaign.yaml"), []byte("name: W3 Static\ncreated: 2026-10-09T00:00:00Z\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	if err := runReindex(vault, dataDir); err != nil {
		t.Fatal(err)
	}
	db, _, err := openAppDB(dataDir, vault)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	sessionStore, err := auth.NewSessionStore(db)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(wireHandlers(buildInfo{Version: "w3"}, vault, dataDir, sessionStore, "", "").Handler)
	t.Cleanup(srv.Close)

	for target, want := range map[string][2]string{
		"/static/core/sheet.css":          {"text/css", staticCoreCache},
		"/static/core/app.css":            {"text/css", staticCoreCache},
		"/static/themes/grimoire.css":     {"text/css", staticCoreCache},
		"/static/themes/cartographer.css": {"text/css", staticCoreCache},
		"/static/vendor/codemirror.js":    {"javascript", staticVendorCache},
		"/static/vendor/datastar.js":      {"javascript", staticVendorCache},
		"/static/vendor/pixi.js":          {"javascript", staticVendorCache},
		"/static/vtt/pixi-board.js":       {"javascript", staticVTTCache},
		"/static/print.css":               {"text/css", "public, max-age=86400"},
	} {
		resp, err := http.Get(srv.URL + target)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", target, resp.StatusCode)
		}
		if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, want[0]) {
			t.Errorf("GET %s content-type = %q, want %q", target, ct, want[0])
		}
		if cc := resp.Header.Get("Cache-Control"); cc != want[1] {
			t.Errorf("GET %s Cache-Control = %q, want %q", target, cc, want[1])
		}
		_ = resp.Body.Close()
	}
	// Unknown static paths still 404 through the full mux.
	resp, err := http.Get(srv.URL + "/static/vendor/evil.js")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /static/vendor/evil.js = %d, want 404", resp.StatusCode)
	}
}

// Theme contract (docs/plan/themes.md): every shipped theme stylesheet must
// carry chrome for ALL EIGHT frozen slot mount points and must ship a light
// and a dark scheme (plus an OS-following "auto" that reuses the dark one),
// and every rule must stay scoped under [data-theme] so no theme leaks onto
// unthemed pages. The design prose and the measured contrast ratios live in
// docs/plan/themes.md; this is the mechanical half.
func TestThemeStylesheetsCoverAllSlots(t *testing.T) {
	themes := []struct{ id, file string }{
		{"grimoire", "static/themes/grimoire.css"},
		{"cartographer", "static/themes/cartographer.css"},
	}
	slots := []string{
		web.SlotHeaderLeft, web.SlotHeaderCenter, web.SlotHeaderRight,
		web.SlotSidebarLeft, web.SlotSidebarRight, web.SlotFooter,
		web.SlotPageActions, web.SlotSheetHeader,
	}
	var total int64
	for _, th := range themes {
		raw, err := staticFS.ReadFile(th.file)
		if err != nil {
			t.Fatalf("read %s: %v", th.file, err)
		}
		css := string(raw)
		total += int64(len(raw))

		// Layer pinning: core < plugins < overrides, and the overrides layer itself.
		for _, want := range []string{"@layer core, plugins, overrides;", "@layer overrides {"} {
			if !strings.Contains(css, want) {
				t.Errorf("%s missing %q", th.file, want)
			}
		}

		// Schemes: light (bare [data-theme]), dark, and auto under the OS pref.
		for _, want := range []string{
			`[data-theme="` + th.id + `"] {`,
			`[data-theme="` + th.id + `"][data-scheme="dark"] {`,
			`[data-theme="` + th.id + `"][data-scheme="auto"] {`,
			"@media (prefers-color-scheme: dark) {",
		} {
			if !strings.Contains(css, want) {
				t.Errorf("%s missing scheme selector %q", th.file, want)
			}
		}

		// A theme must re-point shell + statblock/callout tokens once per
		// scheme, not only restyle chrome — otherwise a themed page keeps
		// unthemed statblocks and callouts.
		for _, tok := range coreThemeTokens {
			if n := strings.Count(css, tok+":"); n < 3 {
				t.Errorf("%s token %s appears %d times, want >= 3 (one per scheme)", th.file, tok, n)
			}
		}

		// All eight slots.
		for _, s := range slots {
			if !strings.Contains(css, `[data-slot="`+s+`"]`) {
				t.Errorf("%s has no chrome for slot %q", th.file, s)
			}
		}

		// No unscoped selector leaves the file: at-rules are fine, anything
		// else must be under [data-theme]. Comments are stripped first so the
		// check looks at selectors, not prose.
		for _, m := range scopeRe.FindAllStringSubmatch(commentRe.ReplaceAllString(css, " "), -1) {
			sel := strings.TrimSpace(m[1])
			if sel == "" || strings.HasPrefix(sel, "@") || strings.Contains(sel, "[data-theme=") {
				continue
			}
			t.Errorf("%s has an unscoped selector %q (themes must stay under [data-theme])", th.file, sel)
		}
	}
	// theme-weight budget gate (tools/budgets), re-pinned where the files live:
	// the whole themes subtree stays inside one rendered page's weight.
	if total > maxThemeBytes {
		t.Errorf("themes total %d bytes over %d", total, maxThemeBytes)
	}
}

// ruleBody returns the `selector { ... }` body from css (brace-aware, like
// the plugin linter's splitter — a naive split on `}` orphans nested
// at-rules). The selector must START a rule: skipping back over whitespace,
// the previous character must be `}`/`{`/nothing, so ".rail" never matches
// ".rail-left" or ".rail > .drawer".
func ruleBody(t *testing.T, css, selector string) string {
	t.Helper()
	from := 0
	for {
		i := strings.Index(css[from:], selector)
		if i < 0 {
			t.Fatalf("selector %q not found", selector)
		}
		i += from
		j := i - 1
		for j >= 0 && (css[j] == ' ' || css[j] == '\t' || css[j] == '\n') {
			j--
		}
		startsRule := j < 0 || css[j] == '}' || css[j] == '{'
		rest := css[i+len(selector):]
		if !startsRule || !strings.HasPrefix(strings.TrimLeft(rest, " \t\n"), "{") {
			from = i + len(selector)
			continue
		}
		open := i + len(selector) + strings.Index(rest, "{")
		depth := 0
		for k := open; k < len(css); k++ {
			switch css[k] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					return css[open+1 : k]
				}
			}
		}
		t.Fatalf("unbalanced braces after %q", selector)
		return ""
	}
}

// atRules returns the concatenated bodies of every at-rule whose header
// matches (e.g. "@media (min-width: 1200px)"). app.css carries two 900px
// blocks (layout + drawer chrome), so callers get them all.
func atRules(t *testing.T, css, header string) string {
	t.Helper()
	var out strings.Builder
	from := 0
	for {
		i := strings.Index(css[from:], header)
		if i < 0 {
			break
		}
		i += from
		open := strings.Index(css[i:], "{")
		if open < 0 {
			break
		}
		open += i
		depth, end := 0, -1
		for k := open; k < len(css) && end < 0; k++ {
			switch css[k] {
			case '{':
				depth++
			case '}':
				depth--
				if depth == 0 {
					end = k
				}
			}
		}
		if end < 0 {
			t.Fatalf("unbalanced at-rule %q", header)
		}
		out.WriteString(css[open+1 : end])
		from = end + 1
	}
	if out.Len() == 0 {
		t.Fatalf("at-rule %q not found", header)
	}
	return out.String()
}

// The shell layout contract, pinned on the SERVED bytes (the mirror is
// byte-pinned against web/static/core/app.css by
// TestW3StaticCopiesMatchSources, so this covers what a browser gets).
//
// The shipped shell nested both sidebars in one <details> below main with
// `.layout { display: block }`, which stacked them under the page like a
// second footer — a layout regression a unit test on HTML structure alone
// cannot catch, because the DOM was "fine" and the CSS did the stacking.
// The contract: main first in the DOM (phones), a CSS grid with named
// areas placing the rails beside the page when there is room (tablets:
// page + companions; desktop: tree · page · companions), and rails pinned
// with independent scroll so a deep vault tree never pushes the page down.
func TestCoreAppCSSLayoutGrid(t *testing.T) {
	raw, err := staticFS.ReadFile("static/core/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	css := commentRe.ReplaceAllString(string(raw), " ")

	// Base: a grid, not the old block flow. One column, three named areas,
	// main first — DOM order and visual order agree on phones and swap on
	// wide screens by area name alone.
	base := ruleBody(t, css, ".layout")
	if !strings.Contains(base, "display: grid") {
		t.Error(".layout must be display: grid (the old display: block stacked the rails under main)")
	}
	if !strings.Contains(base, `grid-template-areas: "main" "left" "right"`) {
		t.Errorf(`.layout base areas must be "main" "left" "right" (main first), got: %s`, base)
	}

	// Each region claims its named area — the pieces that let main stay
	// first in the DOM while the rails move beside it.
	for sel, area := range map[string]string{
		".sheet-col":  "main",
		".rail-left":  "left",
		".rail-right": "right",
	} {
		body := ruleBody(t, css, sel)
		if !strings.Contains(body, "grid-area: "+area) {
			t.Errorf("%s must claim grid-area: %s, got: %s", sel, area, body)
		}
	}

	// Tablet: page + companions rail side by side, tree below full width.
	tablet := atRules(t, css, "@media (min-width: 900px)")
	if !strings.Contains(tablet, `grid-template-areas: "main right" "left left"`) {
		t.Errorf(`900px layout must be "main right" / "left left", got: %s`, tablet)
	}
	// Rails stop looking like stacked drawers once they are real columns.
	if !strings.Contains(tablet, ".rail > .drawer { border: 0") {
		t.Errorf("900px must strip the stacked-drawer border on .rail > .drawer, got: %s", tablet)
	}

	// Desktop: tree · page · companions, three columns, rails pinned and
	// independently scrolling.
	desktop := atRules(t, css, "@media (min-width: 1200px)")
	for _, want := range []string{
		`grid-template-areas: "left main right"`,
		"position: sticky",
		"align-self: start",
		"max-height: calc(100vh - 2rem)",
		"overflow-y: auto",
	} {
		if !strings.Contains(desktop, want) {
			t.Errorf("1200px layout missing %q, got: %s", want, desktop)
		}
	}
}
