package templates

import (
	"strings"
	"testing"
)

// T1: theme attributes render only when a theme is set (unthemed stays
// byte-identical to pre-T1), the theme stylesheet + print stylesheet link,
// and delivered plugin CSS/JS arrive unescaped (first-party plugin bytes,
// never vault content).
func TestShellHeadThemeAndAssets(t *testing.T) {
	render := func(d PageData) string {
		var b strings.Builder
		if err := Shell(d, nil, PageBody(d)).Render(t.Context(), &b); err != nil {
			t.Fatalf("render: %v", err)
		}
		return b.String()
	}

	unthemed := render(PageData{Title: "T"})
	if strings.Contains(unthemed, "data-theme") {
		t.Error("unthemed shell carries a data-theme attribute")
	}
	if !strings.Contains(unthemed, `href="/static/core/app.css"`) ||
		!strings.Contains(unthemed, `href="/static/core/statblock.css"`) ||
		!strings.Contains(unthemed, `href="/static/print.css" media="print"`) {
		t.Error("shell head missing core/statblock/print stylesheet links")
	}
	if strings.Contains(unthemed, "/static/themes/") {
		t.Error("unthemed shell links a theme stylesheet")
	}

	themedHTML := render(PageData{Title: "T", Theme: "grimoire", Scheme: "dark"})
	for _, want := range []string{
		`<html lang="en" data-theme="grimoire" data-scheme="dark">`,
		`href="/static/themes/grimoire.css"`,
	} {
		if !strings.Contains(themedHTML, want) {
			t.Errorf("themed shell missing %q:\n%s", want, themedHTML[:400])
		}
	}

	// Delivered plugin assets: CSS/JS land in the head, unescaped, only when
	// non-empty. `>` and `"` must survive (entity-escaping would break CSS
	// selectors and JS).
	css := `@layer plugins { [data-plugin="x"] .a > .b { color: red; } }`
	js := `(function(){var s=document.querySelector("[data-plugin-slot]");})();`
	withAssets := render(PageData{Title: "T", PluginCSS: css, PluginJS: js})
	if !strings.Contains(withAssets, css) {
		t.Errorf("plugin CSS missing or escaped in head:\n%s", withAssets)
	}
	if !strings.Contains(withAssets, js) {
		t.Errorf("plugin JS missing or escaped in head:\n%s", withAssets)
	}

	// Byte-stable renders: the theme attributes are joined in Go because
	// templ.Attributes renders in (random) Go map order, which would make
	// every request's HTML differ. Two renders of the same input must match
	// exactly.
	themedData := PageData{Title: "T", Theme: "grimoire", Scheme: "dark"}
	if a, b := render(themedData), render(themedData); a != b {
		t.Error("themed shell renders are not byte-stable (attribute order varies)")
	}
}
