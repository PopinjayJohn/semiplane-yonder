package web

import (
	"strings"
	"testing"
)

func TestParseCampaignFull(t *testing.T) {
	in := "# comment\nname: Ashfall\ncreated: 2026-01-01\nbase: dnd\noverlay: srd-5e-2014\nenabled-features:\n- grit\n- honor\n"
	c, err := ParseCampaign(in)
	if err != nil {
		t.Fatal(err)
	}
	if c.Name != "Ashfall" || c.Base != "dnd" || c.Overlay != "srd-5e-2014" {
		t.Fatalf("parsed wrong: %+v", c)
	}
	if len(c.EnabledFeatures) != 2 || c.EnabledFeatures[0] != "grit" {
		t.Fatalf("features wrong: %v", c.EnabledFeatures)
	}
	if err := ValidateCampaign(c); err != nil {
		t.Fatalf("valid campaign rejected: %v", err)
	}
}

func TestParseCampaignFlowList(t *testing.T) {
	c, err := ParseCampaign("name: X\nenabled-features: [a, b]\n")
	if err != nil {
		t.Fatal(err)
	}
	if len(c.EnabledFeatures) != 2 {
		t.Fatalf("flow list wrong: %v", c.EnabledFeatures)
	}
}

func TestParseCampaignUnknownKey(t *testing.T) {
	if _, err := ParseCampaign("name: X\nfrobnicate: 1\n"); err == nil {
		t.Fatal("unknown key must fail (append-only via amend)")
	}
}

func TestValidateCampaignRules(t *testing.T) {
	if err := ValidateCampaign(&Campaign{}); err == nil {
		t.Fatal("empty name must fail")
	}
	if err := ValidateCampaign(&Campaign{Name: "X", Overlay: "o"}); err == nil {
		t.Fatal("overlay without base must fail")
	}
	if err := ValidateCampaign(&Campaign{Name: "X", EnabledFeatures: []string{"Bad_ID"}}); err == nil {
		t.Fatal("bad feature id must fail")
	}
	if err := ValidateCampaign(&Campaign{Name: "X", EnabledFeatures: []string{"a", "a"}}); err == nil {
		t.Fatal("duplicate feature must fail")
	}
}

func TestUpsertCampaignPreservesComments(t *testing.T) {
	existing := "# keep me\nname: Old\nbase: dnd\n"
	got := UpsertCampaignYAML(existing, &Campaign{Name: "New", Base: "dnd", Overlay: "srd-5e-2014", EnabledFeatures: []string{"grit"}})
	for _, want := range []string{"# keep me", "name: New", "overlay: srd-5e-2014", "- grit"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "name: Old") {
		t.Fatalf("stale value kept:\n%s", got)
	}
	// Round-trips through the parser.
	c, err := ParseCampaign(got)
	if err != nil {
		t.Fatalf("upsert broke parsing: %v\n%s", err, got)
	}
	if c.Name != "New" || len(c.EnabledFeatures) != 1 {
		t.Fatalf("round-trip wrong: %+v", c)
	}
}

func TestUpsertCampaignClearsAndRebuildFeatures(t *testing.T) {
	existing := "name: X\nbase: dnd\nenabled-features:\n- old1\n- old2\n"
	got := UpsertCampaignYAML(existing, &Campaign{Name: "X", EnabledFeatures: []string{"new"}})
	if strings.Contains(got, "old1") || strings.Contains(got, "base:") {
		t.Fatalf("stale keys kept:\n%s", got)
	}
	if !strings.Contains(got, "- new") {
		t.Fatalf("new features missing:\n%s", got)
	}
}

func TestUpsertCampaignEmpty(t *testing.T) {
	got := UpsertCampaignYAML("", &Campaign{Name: "X", Base: "dnd"})
	c, err := ParseCampaign(got)
	if err != nil || c.Name != "X" || c.Base != "dnd" {
		t.Fatalf("empty upsert wrong: %v %+v\n%s", err, c, got)
	}
}
