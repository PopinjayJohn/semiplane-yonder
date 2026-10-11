package web

// Backlog B4 (sheet styles + one-handed phone layout): chip-grid style
// switch via ?view=/cookie alongside simple/advanced, responsive phone CSS
// (condensed table, stacked full-width 52px actions, collapsed drawer),
// keyboard-reachable by construction. Uses the shared F2 rig (real vault +
// real index DB) and the alice sheet fixture from handlers_onboard_test.go.

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sheetViewCookieReq(t *testing.T, target, view string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if view != "" {
		req.AddCookie(&http.Cookie{Name: "sheet-view", Value: view})
	}
	return req
}

// ?view= sets the style cookie and redirects (all three styles).
func TestSheetViewSwitchSetsCookie(t *testing.T) {
	sh, _, _ := testSheets(t)
	for _, view := range []string{"simple", "chips", "advanced"} {
		req := httptest.NewRequest(http.MethodGet, "/me?view="+view, nil)
		rec := httptest.NewRecorder()
		sh.MeSheet(rec, withViewer(req, alice()))
		if rec.Code != http.StatusSeeOther {
			t.Fatalf("view=%s: /me = %d, want 303", view, rec.Code)
		}
		if cc := rec.Header().Get("Set-Cookie"); !strings.Contains(cc, "sheet-view="+view) {
			t.Fatalf("view=%s: Set-Cookie = %q, want sheet-view=%s", view, cc, view)
		}
	}
	// Unknown views are ignored (no cookie, no redirect): fail-closed.
	req := httptest.NewRequest(http.MethodGet, "/me?view=bogus", nil)
	rec := httptest.NewRecorder()
	sh.MeSheet(rec, withViewer(req, bob()))
	if rec.Code == http.StatusSeeOther {
		t.Fatal("bogus view must not redirect")
	}
	if cc := rec.Header().Get("Set-Cookie"); strings.Contains(cc, "sheet-view=") {
		t.Fatalf("bogus view set a cookie: %q", cc)
	}
}

// ?view=chips renders the 6-button big-tap chip grid (mockup B-05 style 2):
// same derived data as Simple, text labels with modifiers, phone-ready
// head (viewport + core stylesheet), thumb-zone actions, collapsed drawer.
func TestSheetChipsGrid(t *testing.T) {
	sh, v, s := testSheets(t)
	seedSheet(t, v, s, "alice", "alice", aliceSheetMD)
	rec := httptest.NewRecorder()
	sh.MeSheet(rec, withViewer(sheetViewCookieReq(t, "/me", "chips"), alice()))
	if rec.Code != http.StatusOK {
		t.Fatalf("/me chips = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	// Phone-ready head: viewport, core stylesheet link + inline fallback.
	for _, want := range []string{
		`<meta name="viewport"`,
		`/static/core/sheet.css`,
		`<a class="skip" href="#main">`,
		`id="main"`,
		`@media (max-width: 480px)`,
		`min-height: 52px`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("chips view missing %q", want)
		}
	}
	// Style switcher with current style marked.
	if !strings.Contains(body, `aria-label="Sheet style"`) {
		t.Error("chips view missing style switcher nav")
	}
	if !strings.Contains(body, `<span aria-current="page">Chips</span>`) {
		t.Error("chips view must mark Chips aria-current")
	}
	if !strings.Contains(body, `/me?view=simple`) {
		t.Error("chips view missing switch back to Simple")
	}
	// Condensed vitals ledger table.
	for _, want := range []string{`<table class="ledger">`, `<caption>Vitals</caption>`, `<th scope="row">HP</th>`, `11 / 11`} {
		if !strings.Contains(body, want) {
			t.Errorf("chips view missing ledger %q", want)
		}
	}
	// Six big-tap chips with text labels + derived modifiers (alice:
	// str 15 +2, dex 14 +2, con 13 +1, int 12 +1, wis 10 +0, cha 8 -1).
	if got := strings.Count(body, `class="chip"`); got != 6 {
		t.Errorf("chips view has %d chips, want 6", got)
	}
	for _, want := range []string{
		`aria-label="Strength 15, modifier +2"`,
		`aria-label="Dexterity 14, modifier +2"`,
		`aria-label="Constitution 13, modifier +1"`,
		`aria-label="Intellect 12, modifier +1"`,
		`aria-label="Wisdom 10, modifier +0"`,
		`aria-label="Charisma 8, modifier -1"`,
		`STR <strong>15</strong>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("chips view missing %q", want)
		}
	}
	// Same surgical flows as Simple (HP adjust, rest, level).
	for _, want := range []string{`value="hp"`, `Short rest`, `Long rest`, `Level up to 2`, `class="thumb-zone"`} {
		if !strings.Contains(body, want) {
			t.Errorf("chips view missing action %q", want)
		}
	}
	// Collapsed drawer below the sheet (native details: keyboard-reachable).
	if !strings.Contains(body, `<details class="drawer">`) {
		t.Error("chips view missing collapsed drawer")
	}
	if !strings.Contains(body, `<summary>Details: inspiration and death saves</summary>`) {
		t.Error("chips view missing drawer summary")
	}
	if strings.Contains(body, `<details class="drawer" open`) || strings.Contains(body, `<details open`) {
		t.Error("drawer must be collapsed by default")
	}
	// Per-viewer output: never shared-cache, never stored.
	if cc := rec.Header().Get("Cache-Control"); cc != "private, no-store" {
		t.Errorf("chips Cache-Control = %q, want private, no-store", cc)
	}
}

// Default stays Simple: ledger-ish layout, no chip grid.
func TestSheetSimpleDefaultNoChips(t *testing.T) {
	sh, v, s := testSheets(t)
	seedSheet(t, v, s, "alice", "alice", aliceSheetMD)
	rec := httptest.NewRecorder()
	sh.MeSheet(rec, withViewer(sheetViewCookieReq(t, "/me", ""), alice()))
	if rec.Code != http.StatusOK {
		t.Fatalf("/me = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if strings.Contains(body, `<div class="chip-grid"`) {
		t.Error("simple view must not render the chip grid")
	}
	if !strings.Contains(body, `<ul class="ledger">`) {
		t.Error("simple view missing ledger list")
	}
	if !strings.Contains(body, `<span aria-current="page">Simple</span>`) {
		t.Error("simple view must mark Simple aria-current")
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, no-store" {
		t.Errorf("simple Cache-Control = %q, want private, no-store", cc)
	}
}

// Chips shows the same derived data with zero frontmatter exposure (same
// red line as Simple: TestMeSimpleHidesFrontmatter).
func TestSheetChipsHidesFrontmatter(t *testing.T) {
	sh, v, s := testSheets(t)
	seedSheet(t, v, s, "alice", "alice", aliceSheetMD)
	rec := httptest.NewRecorder()
	sh.MeSheet(rec, withViewer(sheetViewCookieReq(t, "/me", "chips"), alice()))
	if rec.Code != http.StatusOK {
		t.Fatalf("/me chips = %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"Alice", "HP 11 of 11", "Level 1", "Short rest", "Long rest", "Level up"} {
		if !strings.Contains(body, want) {
			t.Errorf("chips /me missing %q", want)
		}
	}
	for _, leak := range []string{"---", "owner:", "secret:", "hp-max", "hit-dice", "slots-", "str:", "con:", "sheet:"} {
		if strings.Contains(body, leak) {
			t.Errorf("chips /me exposed frontmatter %q", leak)
		}
	}
}

// Unknown cookie values fall back to Simple (fail-closed, never a 500).
func TestSheetViewCookieFallback(t *testing.T) {
	sh, v, s := testSheets(t)
	seedSheet(t, v, s, "alice", "alice", aliceSheetMD)
	rec := httptest.NewRecorder()
	sh.MeSheet(rec, withViewer(sheetViewCookieReq(t, "/me", "bogus"), alice()))
	if rec.Code != http.StatusOK {
		t.Fatalf("/me bogus-cookie = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), `<div class="chip-grid"`) {
		t.Error("bogus cookie must fall back to Simple, not Chips")
	}
}

// The inline copy must match the real stylesheet byte-for-byte (edit the
// .css file first, then copy it into sheetCSS).
func TestSheetCSSMatchesFile(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "web", "static", "core", "sheet.css"))
	if err != nil {
		t.Fatalf("read sheet.css: %v", err)
	}
	if string(raw) != sheetCSS {
		t.Fatal("sheetCSS inline copy drifted from web/static/core/sheet.css (copy the file verbatim)")
	}
}

// Pin the phone/a11y rules the B4 demo depends on.
func TestSheetCSSPhoneRules(t *testing.T) {
	for _, want := range []string{
		"@layer core",
		"chip-grid",
		"min-height: 44px",
		"@media (max-width: 480px)",
		"min-height: 52px",
		"display: block",
		"width: 100%",
		"details.drawer summary",
		"focus-visible",
		"prefers-reduced-motion",
		"thumb-zone",
		"table.ledger",
	} {
		if !strings.Contains(sheetCSS, want) {
			t.Errorf("sheet.css missing %q", want)
		}
	}
}

func TestAbilityMod(t *testing.T) {
	for _, tc := range []struct {
		score, mod int64
	}{
		{15, 2}, {14, 2}, {13, 1}, {12, 1}, {10, 0}, {9, -1}, {8, -1}, {7, -2},
	} {
		if got := abilityMod(tc.score); got != tc.mod {
			t.Errorf("abilityMod(%d) = %d, want %d", tc.score, got, tc.mod)
		}
	}
	if signedMod(2) != "+2" || signedMod(0) != "+0" || signedMod(-1) != "-1" {
		t.Errorf("signedMod wrong: %q %q %q", signedMod(2), signedMod(0), signedMod(-1))
	}
}
