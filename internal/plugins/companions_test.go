package plugins

import (
	"strings"
	"testing"
)

func companionsSrc() CompanionsSource {
	src := NewCompanionsSource()
	src.TOC = []TOCItem{{Anchor: "b", Title: "B"}, {Anchor: "a", Title: "A"}}
	src.Backlinks = []BacklinkItem{
		{Path: "zebra", Title: "Zebra"},
		{Path: "apple", Title: "Apple"},
	}
	src.Neighbors = []NeighborEdge{
		{From: "here", To: "zebra"},
		{From: "here", To: "apple"},
	}
	return src
}

func TestAssembleCompanionsUnfilteredRefused(t *testing.T) {
	if _, err := AssembleCompanions(CompanionsSource{}); err == nil {
		t.Fatal("expected UNFILTERED_INPUT rejection for Filtered=false")
	}
}

func TestAssembleCompanionsSortsDeterministically(t *testing.T) {
	d, err := AssembleCompanions(companionsSrc())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Backlinks) != 2 || d.Backlinks[0].Path != "apple" {
		t.Fatalf("backlinks not sorted: %+v", d.Backlinks)
	}
	if len(d.Neighbors) != 2 || d.Neighbors[0].To != "apple" {
		t.Fatalf("neighbors not sorted: %+v", d.Neighbors)
	}
	// TOC keeps document order.
	if len(d.TOC) != 2 || d.TOC[0].Anchor != "b" {
		t.Fatalf("TOC order not preserved: %+v", d.TOC)
	}
}

func TestRenderCompanionsEmptyStates(t *testing.T) {
	src := NewCompanionsSource()
	d, err := AssembleCompanions(src)
	if err != nil {
		t.Fatal(err)
	}
	frag := RenderCompanions(d)
	if frag.Target != "page-companions" {
		t.Errorf("target = %q, want page-companions", frag.Target)
	}
	for _, want := range []string{
		`<section aria-label="Page companions"`,
		`aria-label="On this page"`, `aria-label="Backlinks"`, `aria-label="Linked pages"`,
		`id="companions-toc"`, `id="companions-backlinks"`, `id="companions-links"`,
		`aria-live="polite"`,
		"No headings.", "No backlinks.", "No linked pages.",
	} {
		if !strings.Contains(frag.HTML, want) {
			t.Errorf("fragment missing %q:\n%s", want, frag.HTML)
		}
	}
}

func TestRenderCompanionsNonEmptyAndEscaping(t *testing.T) {
	src := NewCompanionsSource()
	src.TOC = []TOCItem{{Anchor: `a"><script>`, Title: `<b>bold</b>`}}
	src.Backlinks = []BacklinkItem{{Path: `x"><script>`, Title: `<i>evil</i>`}}
	src.Neighbors = []NeighborEdge{{From: "here", To: `y"><script>`}}
	d, err := AssembleCompanions(src)
	if err != nil {
		t.Fatal(err)
	}
	frag := RenderCompanions(d)
	if strings.Contains(frag.HTML, "<script>") || strings.Contains(frag.HTML, "<b>bold</b>") {
		t.Fatalf("unescaped HTML in fragment:\n%s", frag.HTML)
	}
	for _, want := range []string{
		`#a&#34;&gt;&lt;script&gt;`, `&lt;b&gt;bold&lt;/b&gt;`,
		`/p/x&#34;&gt;&lt;script&gt;`, `&lt;i&gt;evil&lt;/i&gt;`,
	} {
		if !strings.Contains(frag.HTML, want) {
			t.Errorf("fragment missing escaped %q:\n%s", want, frag.HTML)
		}
	}
}

func TestCompanionsSlotValid(t *testing.T) {
	p := &CompanionsPlugin{}
	slots := p.Slots()
	if len(slots) != 1 {
		t.Fatalf("slots = %d, want 1", len(slots))
	}
	s := slots[0]
	if s.SlotName != "sidebar-right" || s.ID != CompanionsPluginID ||
		s.Component != CompanionsPluginID || s.Priority != 15 || !s.SecretFiltered {
		t.Fatalf("bad slot contribution: %+v", s)
	}
	if err := ValidateSlot(p.ID(), s); err != nil {
		t.Fatalf("ValidateSlot: %v", err)
	}
}

func TestCompanionsCheckPluginPass(t *testing.T) {
	p := &CompanionsPlugin{}
	if p.ID() != "page-companions" {
		t.Fatalf("id = %q", p.ID())
	}
	if rep := CheckPlugin(p); !rep.OK() {
		t.Fatalf("CheckPlugin findings: %v", rep.Findings)
	} else if err := rep.Error(); err != nil {
		t.Fatalf("CheckPlugin error: %v", err)
	}
	if len(p.CSS()) > MaxPluginCSSBytes {
		t.Fatalf("CSS %d bytes exceeds cap %d", len(p.CSS()), MaxPluginCSSBytes)
	}
}
