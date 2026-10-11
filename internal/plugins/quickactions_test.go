package plugins

import (
	"context"
	"strings"
	"testing"

	"github.com/semiplane/yonder/internal/web"
)

func quickActionsGM() QuickActionsSource {
	src := NewQuickActionsSource("notes/cinder-pact.md", true, "test-csrf-token")
	src.CanEdit = true
	src.IsGM = true
	return src
}

func TestRenderQuickActionsRefusesUnfiltered(t *testing.T) {
	src := QuickActionsSource{Filtered: false, IsGM: true}
	if _, err := RenderQuickActions(src); err == nil {
		t.Fatal("expected UNFILTERED_INPUT error")
	} else if pe, ok := err.(*PluginError); !ok || pe.Code != "UNFILTERED_INPUT" {
		t.Fatalf("error = %v, want UNFILTERED_INPUT", err)
	}
}

func TestRenderQuickActionsPreviewHidesWrites(t *testing.T) {
	src := quickActionsGM()
	src.PreviewAs = "mira" // GM previewing as player: read-only (preview-writes-403)
	frag, err := RenderQuickActions(src)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.ToLower(frag.HTML), "<form") ||
		strings.Contains(frag.HTML, "csrf_token") ||
		strings.Contains(frag.HTML, `type="submit"`) {
		t.Fatalf("preview fragment exposes state-changing controls:\n%s", frag.HTML)
	}
	if !strings.Contains(frag.HTML, "/dashboard#dice-tray") {
		t.Errorf("preview fragment missing dice-roll link:\n%s", frag.HTML)
	}
	if !strings.Contains(frag.HTML, "write controls hidden") {
		t.Errorf("preview fragment missing read-only note:\n%s", frag.HTML)
	}
	if frag.Target == "" {
		t.Error("fragment target must be set (SSE-patchable)")
	}
}

func TestRenderQuickActionsGMGating(t *testing.T) {
	// Non-GM: no secret toggle form, no assertive status.
	player := NewQuickActionsSource("notes/cinder-pact.md", true, "tok")
	player.CanEdit = true
	frag, err := RenderQuickActions(player)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(frag.HTML, "<form") || strings.Contains(frag.HTML, "aria-live=\"assertive\"") {
		t.Fatalf("player fragment must not carry the GM toggle:\n%s", frag.HTML)
	}

	// GM: toggle form posting through the existing write path + assertive text.
	gm := quickActionsGM()
	gfrag, err := RenderQuickActions(gm)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`<form`, `method="post"`, `/edit/p/notes/cinder-pact.md`,
		`name="csrf_token"`, `aria-live="assertive"`,
		"hidden from the party", // text status, never color-only
		"Reveal page to party",
	} {
		if !strings.Contains(gfrag.HTML, want) {
			t.Errorf("GM fragment missing %q:\n%s", want, gfrag.HTML)
		}
	}
	// Visible page flips the toggle direction.
	open := quickActionsGM()
	open.PageSecret = false
	ofrag, err := RenderQuickActions(open)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ofrag.HTML, "Hide page from party") ||
		!strings.Contains(ofrag.HTML, "guest-readable") {
		t.Errorf("open-page toggle wrong:\n%s", ofrag.HTML)
	}
}

func TestRenderQuickActionsEscaping(t *testing.T) {
	src := NewQuickActionsSource(`"><script>alert(1)</script>`, false, `"onmouseover="x`)
	src.CanEdit = true
	src.IsGM = true
	frag, err := RenderQuickActions(src)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(frag.HTML, "<script>") || strings.Contains(frag.HTML, `"onmouseover="x`) {
		t.Fatalf("unescaped output:\n%s", frag.HTML)
	}
	if !strings.Contains(frag.HTML, "%3Cscript%3E") || !strings.Contains(frag.HTML, "&#34;onmouseover=") {
		t.Errorf("expected URL-escaped path + HTML-escaped token:\n%s", frag.HTML)
	}
}

func TestQuickActionsSlotContract(t *testing.T) {
	p := QuickActionsPlugin{}
	if p.ID() != QuickActionsPluginID || p.ID() != "quick-actions" {
		t.Fatalf("ID = %q", p.ID())
	}
	slots := p.Slots()
	if len(slots) != 1 {
		t.Fatalf("Slots = %d, want 1", len(slots))
	}
	s := slots[0]
	if s.SlotName != web.SlotPageActions || s.ID != QuickActionsPluginID ||
		s.Component != QuickActionsPluginID || s.Priority != 10 ||
		!s.SecretFiltered || s.PluginID != QuickActionsPluginID {
		t.Fatalf("slot = %+v", s)
	}
	if err := ValidateSlot(p.ID(), s); err != nil {
		t.Fatalf("ValidateSlot: %v", err)
	}
	if len(p.Routes()) != 0 {
		t.Fatalf("no new routes allowed, got %d", len(p.Routes()))
	}
}

func TestQuickActionsCheckPasses(t *testing.T) {
	p := QuickActionsPlugin{}
	if rep := CheckPlugin(p); !rep.OK() {
		t.Fatalf("CheckPlugin findings: %v", rep.Findings)
	}
	spec := p.A11y()
	if len(spec.Landmarks) == 0 || len(spec.Labels) == 0 || len(spec.LiveRegions) == 0 {
		t.Fatalf("A11y under-specified: %+v", spec)
	}
	has := func(list []string, want string) bool {
		for _, v := range list {
			if v == want {
				return true
			}
		}
		return false
	}
	if !has(spec.Landmarks, "region") || !has(spec.LiveRegions, "polite") || !has(spec.LiveRegions, "assertive") {
		t.Errorf("A11y = %+v, want region + polite + assertive", spec)
	}
	if css := p.CSS(); len(css) > MaxPluginCSSBytes {
		t.Errorf("CSS %d bytes exceeds cap", len(css))
	} else if strings.Contains(css, "!important") {
		t.Error("CSS must not use !important")
	}
}

func TestQuickActionsRegisterEnable(t *testing.T) {
	ctx := context.Background()
	r := NewRegistry(web.NewSlotRegistry(), web.NewRouteRegistry())
	p := QuickActionsPlugin{}
	if err := r.Register(p); err != nil {
		t.Fatal(err)
	}
	if err := r.Enable(ctx, QuickActionsPluginID); err != nil {
		t.Fatal(err)
	}
	if got := r.SlotsFor(web.SlotPageActions, nil, ""); len(got) != 1 {
		t.Fatalf("SlotsFor = %d, want 1", len(got))
	}
	if err := r.Disable(ctx, QuickActionsPluginID); err != nil {
		t.Fatal(err)
	}
	if got := r.SlotsFor(web.SlotPageActions, nil, ""); len(got) != 0 {
		t.Fatalf("disabled SlotsFor = %d, want 0", len(got))
	}
}
