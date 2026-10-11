package plugins

import (
	"strings"
	"testing"

	"github.com/semiplane/yonder/internal/web"
)

func sheetToolsSrc() SheetToolsSource {
	src := NewSheetToolsSource("mira", "Mira Ashdown", 12, 20, "tok-123")
	if !src.Filtered || !src.HasSheet {
		panic("NewSheetToolsSource must mark filtered input with a sheet")
	}
	return src
}

func TestRenderSheetToolsRefusesUnfiltered(t *testing.T) {
	src := sheetToolsSrc()
	src.Filtered = false
	if _, err := RenderSheetTools(src); err == nil {
		t.Fatal("expected UNFILTERED_INPUT error")
	}
}

func TestRenderSheetToolsRestControls(t *testing.T) {
	frag, err := RenderSheetTools(sheetToolsSrc())
	if err != nil {
		t.Fatal(err)
	}
	if frag.Target != "sheet-tools" {
		t.Fatalf("target = %q, want sheet-tools", frag.Target)
	}
	body := frag.HTML
	if !strings.Contains(body, `aria-label="Sheet tools"`) {
		t.Error("missing section aria-label")
	}
	// Two POST forms against the existing sheet rest handler.
	if got := strings.Count(body, `<form method="post" action="/characters/mira/rest">`); got != 2 {
		t.Errorf("rest forms = %d, want 2", got)
	}
	for _, want := range []string{`name="mode" value="short"`, `name="mode" value="long"`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	// CSRF field name csrf_token on both forms.
	if got := strings.Count(body, `name="csrf_token"`); got != 2 {
		t.Errorf("csrf_token fields = %d, want 2", got)
	}
	// Labelled buttons.
	for _, want := range []string{"Short rest", "Long rest"} {
		if !strings.Contains(body, want) {
			t.Errorf("missing button label %q", want)
		}
	}
	// HP text status (never color-only: numeric text present).
	if !strings.Contains(body, "HP 12 of 20") {
		t.Error("missing HP text status")
	}
	if !strings.Contains(body, `aria-live="polite"`) {
		t.Error("missing polite live region on HP status")
	}
}

func TestRenderSheetToolsPreviewHidesControls(t *testing.T) {
	src := sheetToolsSrc()
	src.PreviewAs = "bob"
	frag, err := RenderSheetTools(src)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(frag.HTML, "<form") || strings.Contains(frag.HTML, "csrf_token") {
		t.Error("preview must hide POST controls (read-only)")
	}
	if !strings.Contains(frag.HTML, "HP 12 of 20") {
		t.Error("preview must keep the HP text status")
	}
}

func TestRenderSheetToolsEmptyState(t *testing.T) {
	src := SheetToolsSource{Filtered: true, HasSheet: false}
	frag, err := RenderSheetTools(src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(frag.HTML, "No sheet on this page.") {
		t.Errorf("missing empty state:\n%s", frag.HTML)
	}
	if strings.Contains(frag.HTML, "<form") {
		t.Error("empty state must render no forms")
	}
}

func TestRenderSheetToolsEscaping(t *testing.T) {
	src := NewSheetToolsSource(`mira"><script>alert(1)</script>`, `<img src=x onerror=alert(1)>`, 1, 2, `"quoted"`)
	frag, err := RenderSheetTools(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{"<script>", "<img src=x", `"quoted"`} {
		if strings.Contains(frag.HTML, raw) {
			t.Errorf("unescaped %q in:\n%s", raw, frag.HTML)
		}
	}
	// PreviewAs is escaped too.
	src = sheetToolsSrc()
	src.PreviewAs = `<b>bob</b>`
	frag, err = RenderSheetTools(src)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(frag.HTML, "<b>bob</b>") {
		t.Errorf("unescaped preview-as in:\n%s", frag.HTML)
	}
}

func TestSheetToolsSlotContract(t *testing.T) {
	p := SheetToolsPlugin{}
	if p.ID() != "sheet-tools" {
		t.Fatalf("ID = %q, want sheet-tools", p.ID())
	}
	slots := p.Slots()
	if len(slots) != 1 {
		t.Fatalf("slots = %d, want 1", len(slots))
	}
	s := slots[0]
	if s.SlotName != web.SlotSheetHeader || s.ID != "sheet-tools" || s.Component != "sheet-tools" {
		t.Errorf("slot identity wrong: %+v", s)
	}
	if s.Priority != 10 {
		t.Errorf("priority = %d, want 10", s.Priority)
	}
	if !s.SecretFiltered {
		t.Error("slot must be SecretFiltered")
	}
	if s.ShowIf.PathPrefix != "characters/" {
		t.Errorf("PathPrefix = %q, want characters/", s.ShowIf.PathPrefix)
	}
	if s.PluginID != p.ID() {
		t.Errorf("PluginID = %q, want %q", s.PluginID, p.ID())
	}
	if err := ValidateSlot(p.ID(), s); err != nil {
		t.Fatalf("ValidateSlot: %v", err)
	}
	if p.Routes() != nil {
		t.Error("sheet-tools must own no routes (existing sheet handlers only)")
	}
}

func TestSheetToolsCheckPluginPasses(t *testing.T) {
	rep := CheckPlugin(SheetToolsPlugin{})
	if !rep.OK() {
		t.Fatalf("CheckPlugin findings: %v", rep.Findings)
	}
	if len(SheetToolsPlugin{}.CSS()) > MaxPluginCSSBytes {
		t.Fatal("CSS exceeds 20KB cap")
	}
	if !strings.Contains(SheetToolsPlugin{}.CSS(), "44px") {
		t.Error("CSS must enforce 44px touch targets")
	}
	spec := SheetToolsPlugin{}.A11y()
	if len(spec.Landmarks) == 0 || spec.Landmarks[0] != "region" {
		t.Errorf("landmarks = %v, want [region]", spec.Landmarks)
	}
	if len(spec.LiveRegions) == 0 || spec.LiveRegions[0] != "polite" {
		t.Errorf("live regions = %v, want [polite]", spec.LiveRegions)
	}
}
