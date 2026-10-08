package plugins

import (
	"strings"
	"testing"
)

const goodCSS = `@layer plugins {
[data-plugin="random-tables"] .roll-btn { color: var(--accent); }
[data-plugin="random-tables"] .roll-list { margin: 0; }
}`

func TestCheckCSSPass(t *testing.T) {
	if f := CheckCSS("random-tables", goodCSS); len(f) != 0 {
		t.Fatalf("findings: %v", f)
	}
}

func TestCheckCSSSize(t *testing.T) {
	big := `@layer plugins { [data-plugin="x"] .a { color: red; } }` + strings.Repeat(" ", MaxPluginCSSBytes)
	if f := CheckCSS("x", big); !hasCode(f, "CSS_SIZE") {
		t.Fatalf("expected CSS_SIZE, got %v", f)
	}
}

func TestCheckCSSLayerAndImportant(t *testing.T) {
	css := `[data-plugin="x"] .a { color: red !important; }`
	f := CheckCSS("x", css)
	if !hasCode(f, "CSS_LAYER") {
		t.Errorf("expected CSS_LAYER, got %v", f)
	}
	if !hasCode(f, "CSS_IMPORTANT") {
		t.Errorf("expected CSS_IMPORTANT, got %v", f)
	}
}

func TestCheckCSSScope(t *testing.T) {
	css := `@layer plugins { .unscoped { color: red; } [data-plugin="x"] .ok { color: blue; } }`
	f := CheckCSS("x", css)
	if !hasCode(f, "CSS_SCOPE") {
		t.Fatalf("expected CSS_SCOPE, got %v", f)
	}
}

func TestCheckCSSBareElement(t *testing.T) {
	css := `@layer plugins { [data-plugin="x"] div { margin: 0; } }`
	if f := CheckCSS("x", css); !hasCode(f, "CSS_BARE_ELEMENT") {
		t.Fatalf("expected CSS_BARE_ELEMENT, got %v", f)
	}
}

func TestCheckCSSPseudoClassOK(t *testing.T) {
	css := `@layer plugins { [data-plugin="x"] .btn:focus-visible { outline: 2px solid; } }`
	if f := CheckCSS("x", css); len(f) != 0 {
		t.Fatalf("findings: %v", f)
	}
}

func TestCheckA11yFails(t *testing.T) {
	if f := CheckA11y("x", A11ySpec{}); !hasCode(f, "A11Y_LANDMARK") || !hasCode(f, "A11Y_LABEL") {
		t.Fatalf("expected landmark+label findings, got %v", f)
	}
	ok := CheckA11y("x", A11ySpec{Landmarks: []string{"complementary"}, Labels: []string{"roll d100"}})
	if len(ok) != 0 {
		t.Fatalf("findings: %v", ok)
	}
}

func TestCheckPluginEndToEnd(t *testing.T) {
	p := testPlugin("random-tables")
	p.css = goodCSS
	if rep := CheckPlugin(p); !rep.OK() {
		t.Fatalf("expected pass, got %v", rep.Findings)
	}
	if err := CheckPlugin(p).Error(); err != nil {
		t.Fatal(err)
	}
	bad := testPlugin("bad")
	bad.css = `.naked { color: red !important; }`
	bad.a11y = A11ySpec{}
	rep := CheckPlugin(bad)
	if rep.OK() || rep.Error() == nil {
		t.Fatal("expected failing report")
	}
	joined := rep.Error().Error()
	for _, want := range []string{"CSS_LAYER", "CSS_IMPORTANT", "CSS_SCOPE", "A11Y_LANDMARK", "A11Y_LABEL"} {
		if !strings.Contains(joined, want) {
			t.Errorf("report missing %s: %s", want, joined)
		}
	}
}

func hasCode(f []Finding, code string) bool {
	for _, x := range f {
		if x.Code == code {
			return true
		}
	}
	return false
}
