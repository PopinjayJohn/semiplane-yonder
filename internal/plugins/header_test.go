package plugins

import (
	"strings"
	"testing"

	"github.com/semiplane/yonder/internal/web"
)

func TestBreadcrumbEscapingAndActive(t *testing.T) {
	in := NewBreadcrumbInput([]Crumb{
		{Path: "notes/index.md", Title: "Notes & <More>"},
		{Path: "notes/evil.md", Title: `<script>alert("x")</script>`},
	})
	frag, err := RenderBreadcrumb(in)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(frag.HTML, `<nav aria-label="Breadcrumb"`) {
		t.Errorf("missing breadcrumb nav: %s", frag.HTML)
	}
	if !strings.Contains(frag.HTML, "<ol>") || !strings.Contains(frag.HTML, "</ol>") {
		t.Errorf("breadcrumb must be an ordered list: %s", frag.HTML)
	}
	if strings.Contains(frag.HTML, "<script>") {
		t.Errorf("unescaped title in breadcrumb: %s", frag.HTML)
	}
	if !strings.Contains(frag.HTML, "Notes &amp; &lt;More&gt;") {
		t.Errorf("ancestor title not escaped: %s", frag.HTML)
	}
	if !strings.Contains(frag.HTML, "&lt;script&gt;") {
		t.Errorf("last title not escaped: %s", frag.HTML)
	}
	if !strings.Contains(frag.HTML, `aria-current="page"`) {
		t.Errorf("last crumb missing aria-current=page: %s", frag.HTML)
	}
	if strings.Count(frag.HTML, `aria-current="page"`) != 1 {
		t.Errorf("exactly one crumb must be current: %s", frag.HTML)
	}
	// Current page is text, not a link; ancestors are links.
	if !strings.Contains(frag.HTML, `<a href="/p/notes/index.md">`) {
		t.Errorf("ancestor must link: %s", frag.HTML)
	}
	lastLink := strings.LastIndex(frag.HTML, "<a ")
	lastCurrent := strings.LastIndex(frag.HTML, `aria-current="page"`)
	if lastLink > lastCurrent {
		t.Errorf("last crumb must not be a link: %s", frag.HTML)
	}
}

func TestBreadcrumbRefusesUnfiltered(t *testing.T) {
	in := BreadcrumbInput{Filtered: false, Crumbs: []Crumb{{Path: "a.md", Title: "A"}}}
	if _, err := RenderBreadcrumb(in); err == nil {
		t.Fatal("expected UNFILTERED_INPUT error")
	}
}

func TestBreadcrumbEmpty(t *testing.T) {
	frag, err := RenderBreadcrumb(NewBreadcrumbInput(nil))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(frag.HTML, "<ol>") {
		t.Errorf("empty breadcrumb must still render the list: %s", frag.HTML)
	}
}

func TestHeaderSearchFragment(t *testing.T) {
	frag := RenderSearchPalette()
	for _, want := range []string{
		`<form`, `role="search"`, `action="/search"`, `method="get"`,
		`<label for="header-search-input">`, `id="header-search-input"`,
		`name="q"`, `<button type="submit">`,
	} {
		if !strings.Contains(frag.HTML, want) {
			t.Errorf("search fragment missing %q: %s", want, frag.HTML)
		}
	}
	if strings.Contains(strings.ToLower(frag.HTML), "method=\"post\"") {
		t.Error("search must be GET (read-only, no CSRF)")
	}
}

func TestHeaderLiveBadge(t *testing.T) {
	frag := RenderLiveBadge()
	for _, want := range []string{`data-sse-state`, `aria-live="polite"`, "Live updates:"} {
		if !strings.Contains(frag.HTML, want) {
			t.Errorf("live badge missing %q: %s", want, frag.HTML)
		}
	}
	if strings.Contains(strings.ToLower(frag.HTML), "<script") {
		t.Error("live badge must not ship JS in v1")
	}
}

func TestHeaderSlots(t *testing.T) {
	p := HeaderPlugin{}
	if p.ID() != SlotHeaderPluginID {
		t.Fatalf("id = %q", p.ID())
	}
	slots := p.Slots()
	if len(slots) != 3 {
		t.Fatalf("slots = %d, want 3", len(slots))
	}
	want := map[string]struct {
		slot     string
		priority int
	}{
		"breadcrumb": {web.SlotHeaderLeft, 10},
		"search":     {web.SlotHeaderCenter, 10},
		"live-badge": {web.SlotHeaderRight, 5},
	}
	for _, s := range slots {
		w, ok := want[s.ID]
		if !ok {
			t.Errorf("unexpected slot id %q", s.ID)
			continue
		}
		if s.SlotName != w.slot {
			t.Errorf("%s: slot = %q, want %q", s.ID, s.SlotName, w.slot)
		}
		if s.Priority != w.priority {
			t.Errorf("%s: priority = %d, want %d", s.ID, s.Priority, w.priority)
		}
		if !s.SecretFiltered || s.PluginID != SlotHeaderPluginID {
			t.Errorf("%s: %+v (must be secret-filtered, owned)", s.ID, s)
		}
		if err := ValidateSlot(SlotHeaderPluginID, s); err != nil {
			t.Errorf("ValidateSlot(%s): %v", s.ID, err)
		}
	}
}

func TestHeaderCheckPlugin(t *testing.T) {
	rep := CheckPlugin(HeaderPlugin{})
	if !rep.OK() {
		t.Fatalf("CheckPlugin findings: %v", rep.Findings)
	}
	a11y := HeaderPlugin{}.A11y()
	found := map[string]bool{}
	for _, l := range a11y.Landmarks {
		found[l] = true
	}
	if !found["banner"] || !found["search"] {
		t.Errorf("landmarks = %v, want banner+search", a11y.Landmarks)
	}
	if len(a11y.LiveRegions) == 0 {
		t.Error("live region required for the SSE badge")
	}
}
