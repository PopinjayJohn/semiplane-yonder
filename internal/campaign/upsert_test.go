package campaign

import (
	"strings"
	"testing"
)

// Relocated from internal/web/campaign_test.go (B4 unification): the web
// duplicate is deleted; Parse + Validate + Upsert live here alone.

// --- parser (the one parser) ---

func TestUpsertParseFull(t *testing.T) {
	in := "# comment\nname: Ashfall\ncreated: 2026-01-01\nbase: dnd\noverlay: srd-5e-2014\nlanding-page: welcome\nenabled-features:\n- grit\n- honor\nenabled-plugins:\n- random-tables\n"
	c, err := Parse([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "Ashfall" || c.Base != "dnd" || c.Overlay != "srd-5e-2014" || c.LandingPage != "welcome" {
		t.Fatalf("parsed wrong: %+v", c)
	}
	if len(c.EnabledFeatures) != 2 || c.EnabledFeatures[0] != "grit" {
		t.Fatalf("features wrong: %v", c.EnabledFeatures)
	}
	if len(c.EnabledPlugins) != 1 || c.EnabledPlugins[0] != "random-tables" {
		t.Fatalf("plugins wrong (namespace leak?): %v", c.EnabledPlugins)
	}
	if err := Validate(c); err != nil {
		t.Fatalf("valid campaign rejected: %v", err)
	}
}

func TestUpsertParseFlowList(t *testing.T) {
	c, err := Parse([]byte("name: X\nenabled-features: [a, b]\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(c.EnabledFeatures) != 2 {
		t.Fatalf("flow list wrong: %v", c.EnabledFeatures)
	}
}

func TestUpsertParseUnknownKey(t *testing.T) {
	if _, err := Parse([]byte("name: X\nfrobnicate: 1\n")); err == nil {
		t.Fatal("unknown key must fail (append-only via amend)")
	}
}

// --- Validate (the one shape check) ---

func TestUpsertValidateRules(t *testing.T) {
	if err := Validate(&Campaign{}); err == nil {
		t.Fatal("empty name must fail")
	}
	if err := Validate(&Campaign{Name: "X", Overlay: "o"}); err == nil {
		t.Fatal("overlay without base must fail")
	}
	if err := Validate(&Campaign{Name: "X", EnabledFeatures: []string{"Bad_ID"}}); err == nil {
		t.Fatal("bad feature id must fail")
	}
	if err := Validate(&Campaign{Name: "X", EnabledFeatures: []string{"a", "a"}}); err == nil {
		t.Fatal("duplicate feature must fail")
	}
	if err := Validate(&Campaign{Name: "X", EnabledPlugins: []string{"a", "a"}}); err == nil {
		t.Fatal("duplicate plugin must fail")
	}
	if err := Validate(nil); err == nil {
		t.Fatal("nil must fail")
	}
}

// --- Upsert (the one surgical writer) ---

func TestUpsertPreservesComments(t *testing.T) {
	existing := "# keep me\nname: Old\nbase: dnd\n"
	got := Upsert(existing, &Campaign{Name: "New", Base: "dnd", Overlay: "srd-5e-2014", EnabledFeatures: []string{"grit"}})
	for _, want := range []string{"# keep me", "name: New", "overlay: srd-5e-2014", "- grit"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "name: Old") {
		t.Fatalf("stale value kept:\n%s", got)
	}
	// Round-trips through the parser.
	c, err := Parse([]byte(got))
	if err != nil {
		t.Fatalf("upsert broke parsing: %v\n%s", err, got)
	}
	if c.Name != "New" || len(c.EnabledFeatures) != 1 {
		t.Fatalf("round-trip wrong: %+v", c)
	}
}

func TestUpsertClearsAndRebuildFeatures(t *testing.T) {
	existing := "name: X\nbase: dnd\nenabled-features:\n- old1\n- old2\n"
	got := Upsert(existing, &Campaign{Name: "X", EnabledFeatures: []string{"new"}})
	if strings.Contains(got, "old1") || strings.Contains(got, "base:") {
		t.Fatalf("stale keys kept:\n%s", got)
	}
	if !strings.Contains(got, "- new") {
		t.Fatalf("new features missing:\n%s", got)
	}
}

func TestUpsertEmpty(t *testing.T) {
	got := Upsert("", &Campaign{Name: "X", Base: "dnd"})
	c, err := Parse([]byte(got))
	if err != nil || c.Name != "X" || c.Base != "dnd" {
		t.Fatalf("empty upsert wrong: %v %+v\n%s", err, c, got)
	}
}

// --- merge-specific: the two writers' promises, now one function ---

func TestUpsertKeepsUnchangedScalarBytes(t *testing.T) {
	existing := "name: Pact\nbase: dnd # system base (display-only version below)\noverlay: 5e-2024\n"
	got := Upsert(existing, &Campaign{Name: "Pact", Base: "dnd", Overlay: "5e-2024"})
	if got != existing {
		t.Fatalf("no-op upsert rewrote bytes:\n%q\nvs\n%q", got, existing)
	}
}

func TestUpsertPreservesOrderAndIndents(t *testing.T) {
	existing := "name: Pact\nenabled-features:\n  - b-id\n  - a-id\nenabled-plugins:\n  - random-tables\n"
	got := Upsert(existing, &Campaign{Name: "Pact", EnabledFeatures: []string{"b-id", "a-id"}, EnabledPlugins: []string{"random-tables"}})
	// Untouched plugins block byte-identical; features rebuilt in given
	// (unsorted) order with the file's two-space item style.
	if !strings.Contains(got, "enabled-plugins:\n  - random-tables") {
		t.Fatalf("untouched block not preserved:\n%s", got)
	}
	fb := strings.Index(got, "- b-id")
	fa := strings.Index(got, "- a-id")
	if fb < 0 || fa < 0 || fb > fa {
		t.Fatalf("given order not preserved:\n%s", got)
	}
	if !strings.Contains(got, "  - b-id") {
		t.Fatalf("item indent not preserved:\n%s", got)
	}
	c, err := Parse([]byte(got))
	if err != nil {
		t.Fatalf("re-parse: %v\n%s", err, got)
	}
	if len(c.EnabledFeatures) != 2 || c.EnabledFeatures[0] != "b-id" {
		t.Fatalf("round-trip order wrong: %v", c.EnabledFeatures)
	}
}

func TestUpsertReplacesFlowList(t *testing.T) {
	existing := "name: X\nenabled-features: [old]\n"
	got := Upsert(existing, &Campaign{Name: "X", EnabledFeatures: []string{"new"}})
	if strings.Contains(got, "[old]") {
		t.Fatalf("flow list survived:\n%s", got)
	}
	c, err := Parse([]byte(got))
	if err != nil || len(c.EnabledFeatures) != 1 || c.EnabledFeatures[0] != "new" {
		t.Fatalf("flow replace wrong: %v %+v\n%s", err, c, got)
	}
}

func TestUpsertUnknownKeysPreserved(t *testing.T) {
	// Forward-compat: keys the frozen set doesn't know are never dropped
	// by the writer (the parser still rejects them on read — append-only
	// extension happens via amend, and such files fail closed elsewhere).
	existing := "name: X\nfuture-key: keep\n"
	got := Upsert(existing, &Campaign{Name: "Y"})
	if !strings.Contains(got, "future-key: keep") {
		t.Fatalf("unknown key dropped:\n%s", got)
	}
}

func TestUpsertCRLFStable(t *testing.T) {
	existing := "name: X\r\nbase: dnd\r\nenabled-features:\r\n- a\r\n"
	got := Upsert(existing, &Campaign{Name: "X", Base: "dnd", EnabledFeatures: []string{"b"}})
	if strings.Contains(got, "\n- b\n") && !strings.Contains(got, "\r\n- b\r\n") {
		t.Fatalf("new block used bare LF in a CRLF file:\n%q", got)
	}
	c, err := Parse([]byte(got))
	if err != nil || len(c.EnabledFeatures) != 1 || c.EnabledFeatures[0] != "b" {
		t.Fatalf("crlf round-trip wrong: %v %+v\n%q", err, c, got)
	}
}
