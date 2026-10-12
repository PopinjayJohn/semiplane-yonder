package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakePluginAssets is the PluginAssets seam double (the plugin registry
// satisfies this interface; web never imports plugins — cycle).
type fakePluginAssets struct{ css, js string }

func (f fakePluginAssets) CSS() string { return f.css }
func (f fakePluginAssets) JS() string  { return f.js }

func TestResolveTheme(t *testing.T) {
	for _, tc := range []struct {
		name, theme, scheme, wantTheme, wantScheme string
	}{
		{"known pair", ThemeGrimoire, SchemeDark, ThemeGrimoire, SchemeDark},
		{"auto passes", ThemeCartographer, SchemeAuto, ThemeCartographer, SchemeAuto},
		{"empty scheme means light", ThemeGrimoire, "", ThemeGrimoire, SchemeLight},
		{"whitespace trimmed", "  grimoire ", " dark ", ThemeGrimoire, SchemeDark},
		{"unknown theme fails closed", "parchment", SchemeDark, "", ""},
		{"empty theme fails closed", "", SchemeDark, "", ""},
		{"scheme without theme is inert", "", SchemeDark, "", ""},
		{"unknown scheme defaults light", ThemeGrimoire, "sepia", ThemeGrimoire, SchemeLight},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gotTheme, gotScheme := ResolveTheme(tc.theme, tc.scheme)
			if gotTheme != tc.wantTheme || gotScheme != tc.wantScheme {
				t.Errorf("ResolveTheme(%q, %q) = (%q, %q), want (%q, %q)",
					tc.theme, tc.scheme, gotTheme, gotScheme, tc.wantTheme, tc.wantScheme)
			}
		})
	}
}

func TestThemeRendering(t *testing.T) {
	if got := HTMLOpenTag("", ""); got != `<html lang="en">` {
		t.Errorf("unthemed open tag = %q", got)
	}
	if got := HTMLOpenTag(ThemeGrimoire, SchemeDark); got != `<html lang="en" data-theme="grimoire" data-scheme="dark">` {
		t.Errorf("themed open tag = %q", got)
	}
	if got := HTMLOpenTag(ThemeGrimoire, ""); got != `<html lang="en" data-theme="grimoire" data-scheme="light">` {
		t.Errorf("defaulted open tag = %q", got)
	}
	if got := ThemeStylesheetLink(""); got != "" {
		t.Errorf("unthemed link = %q, want empty", got)
	}
	if got := ThemeStylesheetLink(ThemeCartographer); got != `<link rel="stylesheet" href="/static/themes/cartographer.css">`+"\n" {
		t.Errorf("themed link = %q", got)
	}
	if !ThemeKnown(ThemeGrimoire) || !ThemeKnown(ThemeCartographer) || ThemeKnown("vellum") {
		t.Error("ThemeKnown disagrees with the shipped set")
	}
	if !SchemeKnown(SchemeLight) || !SchemeKnown(SchemeDark) || !SchemeKnown(SchemeAuto) || SchemeKnown("sepia") {
		t.Error("SchemeKnown disagrees with the shipped set")
	}
}

// The standalone sheet page builds its own head, so it must carry the same
// theme attributes and stylesheet links (a themed campaign that left this
// page unthemed would show themed chrome around a white card).
func TestSheetPageHeadTheme(t *testing.T) {
	render := func(h *SheetHandlers) string {
		rec := httptest.NewRecorder()
		h.writeSheetHTML(rec, http.StatusOK, "Mira", "<p>body</p>")
		return rec.Body.String()
	}

	plain := render(&SheetHandlers{})
	for _, want := range []string{
		`<html lang="en">`,
		`<link rel="stylesheet" href="/static/core/app.css">`,
		`<link rel="stylesheet" href="/static/core/sheet.css">`,
		`<link rel="stylesheet" href="/static/print.css" media="print">`,
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("unthemed sheet head missing %q", want)
		}
	}
	if strings.Contains(plain, "data-theme") || strings.Contains(plain, "/static/themes/") {
		t.Error("unthemed sheet head carries theme output")
	}

	themed := render(&SheetHandlers{Theme: ThemeGrimoire, Scheme: SchemeLight})
	for _, want := range []string{
		`<html lang="en" data-theme="grimoire" data-scheme="light">`,
		`<link rel="stylesheet" href="/static/themes/grimoire.css">`,
	} {
		if !strings.Contains(themed, want) {
			t.Errorf("themed sheet head missing %q", want)
		}
	}
}

func TestRenderShellCarriesThemeAndAssets(t *testing.T) {
	assets := fakePluginAssets{
		css: `@layer plugins { [data-plugin="x"] .a { color: red; } }`,
		js:  `/* plugin client */`,
	}
	h := &ReadHandlers{
		Store: newFakeStore(), SlotRegistry: NewSlotRegistry(),
		Theme: ThemeGrimoire, Scheme: SchemeDark, PluginAssets: assets,
	}
	rec := get(t, h, "/p/welcome.md")
	if rec.Code != 200 {
		t.Fatalf("GET /p/welcome.md = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`<html lang="en" data-theme="grimoire" data-scheme="dark">`,
		`href="/static/themes/grimoire.css"`,
		`href="/static/print.css" media="print"`,
		assets.css,
		assets.js,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("themed page missing %q", want)
		}
	}

	// Unthemed (nil assets, no theme): no theme attributes, no theme link,
	// no asset blocks, and no panic on the nil seam.
	plain := &ReadHandlers{Store: newFakeStore(), SlotRegistry: NewSlotRegistry()}
	rec2 := get(t, plain, "/p/welcome.md")
	if rec2.Code != 200 {
		t.Fatalf("plain GET /p/welcome.md = %d", rec2.Code)
	}
	body2 := rec2.Body.String()
	for _, unwanted := range []string{"data-theme", "/static/themes/", "data-plugin=\"x\""} {
		if strings.Contains(body2, unwanted) {
			t.Errorf("unthemed page carries %q", unwanted)
		}
	}
	if !strings.Contains(body2, `<html lang="en">`) {
		t.Error("unthemed page lost its plain <html> tag")
	}
}
