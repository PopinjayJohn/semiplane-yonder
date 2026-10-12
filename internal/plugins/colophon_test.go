package plugins

import (
	"strings"
	"testing"
)

func TestRenderColophon(t *testing.T) {
	frag, err := RenderColophon(NewColophonSource("Ember Vale", 42))
	if err != nil {
		t.Fatal(err)
	}
	if frag.Target != "vault-colophon" {
		t.Errorf("target = %q", frag.Target)
	}
	for _, want := range []string{
		`<div data-plugin="vault-colophon"`, `<section aria-label="About this vault"`,
		"Ember Vale", "42 pages",
		"SRD", "CC-BY-4.0",
	} {
		if !strings.Contains(frag.HTML, want) {
			t.Errorf("fragment missing %q:\n%s", want, frag.HTML)
		}
	}
	// The fragment mounts inside the shell's footer.colophon (the page's
	// contentinfo), so it must NOT render its own <footer> — a nested
	// contentinfo fails axe (landmark-no-duplicate-contentinfo,
	// landmark-contentinfo-is-top-level).
	if strings.Contains(frag.HTML, "<footer") {
		t.Errorf("fragment renders a nested <footer>:\n%s", frag.HTML)
	}
}

func TestRenderColophonSingular(t *testing.T) {
	frag, err := RenderColophon(NewColophonSource("Solo", 1))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(frag.HTML, "1 page ") {
		t.Errorf("singular count = %q", frag.HTML)
	}
	if strings.Contains(frag.HTML, "1 pages") {
		t.Errorf("pluralized singular: %q", frag.HTML)
	}
}

func TestRenderColophonEscapes(t *testing.T) {
	frag, err := RenderColophon(NewColophonSource(`<script>alert(1)</script>`, 3))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(frag.HTML, "<script>") {
		t.Errorf("unescaped campaign name:\n%s", frag.HTML)
	}
	if !strings.Contains(frag.HTML, "&lt;script&gt;") {
		t.Errorf("escaped name missing:\n%s", frag.HTML)
	}
}

func TestRenderColophonUnfilteredRefused(t *testing.T) {
	if _, err := RenderColophon(ColophonSource{}); err == nil {
		t.Error("expected UNFILTERED_INPUT rejection")
	}
	if _, err := RenderColophon(ColophonSource{Filtered: false, PageCount: 5, CampaignName: "x"}); err == nil {
		t.Error("expected UNFILTERED_INPUT rejection")
	}
}

func TestColophonZeroSecretContent(t *testing.T) {
	// Counts only: even a hostile campaign name carrying a secret-looking
	// path must arrive escaped, and the fragment must never echo titles.
	frag, err := RenderColophon(NewColophonSource(`quests/ambush.md "Ambush"`, 2))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(frag.HTML, `"Ambush"`) {
		t.Errorf("raw quoted title leaked:\n%s", frag.HTML)
	}
	if !strings.Contains(frag.HTML, "2 pages") {
		t.Errorf("count missing:\n%s", frag.HTML)
	}
}

func TestColophonSlotValidate(t *testing.T) {
	p := NewColophonPlugin()
	if p.ID() != ColophonPluginID {
		t.Fatalf("id = %q", p.ID())
	}
	slots := p.Slots()
	if len(slots) != 1 {
		t.Fatalf("slots = %d, want 1", len(slots))
	}
	s := slots[0]
	if s.SlotName != "footer" || s.ID != ColophonPluginID || s.Component != "vault-colophon" {
		t.Errorf("slot = %+v", s)
	}
	if s.Priority != 5 || !s.SecretFiltered {
		t.Errorf("slot = %+v", s)
	}
	if err := ValidateSlot(ColophonPluginID, s); err != nil {
		t.Fatal(err)
	}
}

func TestColophonCheckPlugin(t *testing.T) {
	p := NewColophonPlugin()
	if rep := CheckPlugin(p); !rep.OK() {
		t.Fatalf("findings: %v", rep.Findings)
	}
	if err := CheckPlugin(p).Error(); err != nil {
		t.Fatal(err)
	}
	if n := len(p.CSS()); n == 0 || n > MaxPluginCSSBytes {
		t.Errorf("css bytes = %d", n)
	}
	a11y := p.A11y()
	if len(a11y.Landmarks) == 0 || a11y.Landmarks[0] != "region" {
		t.Errorf("landmarks = %v, want the labelled <section> region the fragment renders", a11y.Landmarks)
	}
	if len(a11y.Labels) == 0 {
		t.Errorf("labels = %v", a11y.Labels)
	}
	if got := p.JS(); got != "" {
		t.Errorf("js = %q, want empty", got)
	}
	if got := p.Routes(); len(got) != 0 {
		t.Errorf("routes = %v, want none", got)
	}
}
