package web

import (
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/web/templates"
)

// fakeSlotSource implements BOTH halves of the slot seam: the metadata
// provider (SlotsFor) and the fragment renderer (RenderSlots). Its renderer
// echoes the filtered page list, so these tests can prove secrecy without
// importing internal/plugins — package web cannot import plugins (cycle).
type fakeSlotSource struct {
	slots    map[string][]SlotComponent
	rendered int
}

func (f *fakeSlotSource) SlotsFor(slot string, _ *auth.Viewer, _ string) []SlotComponent {
	return f.slots[slot]
}

func (f *fakeSlotSource) RenderSlots(slot string, _ *auth.Viewer, _ string, in SlotShellData) []SlotFragment {
	f.rendered++
	var b strings.Builder
	for _, p := range in.Pages {
		fmt.Fprintf(&b, "<li>%s</li>", p.Title)
	}
	if slot == SlotPageActions {
		fmt.Fprintf(&b, "<p>maywrite=%s</p>", strconv.FormatBool(in.MayWrite))
	}
	return []SlotFragment{{ID: "fake", HTML: b.String()}}
}

func newFakeSlotSource() *fakeSlotSource {
	return &fakeSlotSource{slots: map[string][]SlotComponent{
		SlotHeaderLeft:   {{SlotName: SlotHeaderLeft, ID: "fake", Component: "fake", PluginID: "fake"}},
		SlotSidebarLeft:  {{SlotName: SlotSidebarLeft, ID: "fake", Component: "fake", PluginID: "fake"}},
		SlotSidebarRight: {{SlotName: SlotSidebarRight, ID: "fake", Component: "fake", PluginID: "fake"}},
		SlotPageActions:  {{SlotName: SlotPageActions, ID: "fake", Component: "fake", PluginID: "fake"}},
		SlotSheetHeader:  {{SlotName: SlotSheetHeader, ID: "fake", Component: "fake", PluginID: "fake"}},
	}}
}

// The page-actions and sheet-header mounts live inside PageBody, so the body
// must be built from the STAMPED PageData. Before T1-D the funnel stamped its
// own copy after the body was already constructed, and those two mounts never
// rendered even with plugins enabled — this pins the fix.
func TestRenderShellFillsPageBodyMounts(t *testing.T) {
	src := newFakeSlotSource()
	h := &ReadHandlers{Store: newFakeStore(), SlotRegistry: NewSlotRegistry(), Slots: src}
	rec := get(t, h, "/p/welcome.md")
	if rec.Code != 200 {
		t.Fatalf("GET /p/welcome.md = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, slot := range []string{SlotPageActions, SlotSidebarLeft, SlotSidebarRight, SlotHeaderLeft} {
		mount := `<div data-plugin-slot="` + slot + `"`
		if !strings.Contains(body, mount) {
			t.Fatalf("no mount div for %s", slot)
		}
		// The fragment must be INSIDE its mount, not elsewhere on the page.
		after := body[strings.Index(body, mount):]
		inner := after[:strings.Index(after, "</div>")]
		if !strings.Contains(inner, "<li>Welcome to Ashfall</li>") {
			t.Errorf("%s mount rendered empty — the body was built from the pre-stamp PageData:\n%s", slot, inner)
		}
	}
	if src.rendered == 0 {
		t.Error("renderer was never called")
	}
}

// Slot fragments are a read path: the filtered page list must never carry a
// secret page's title or path to a viewer who may not see it, and must carry
// it for the owner and the GM.
func TestRenderShellSlotFragmentsFilteredByViewer(t *testing.T) {
	h := &ReadHandlers{Store: newFakeStore(), SlotRegistry: NewSlotRegistry(), Slots: newFakeSlotSource()}

	guest := get(t, h, "/p/welcome.md").Body.String()
	if strings.Contains(guest, "The Cinder Pact") || strings.Contains(guest, "cinder-pact") {
		t.Error("guest slot fragments leak a secret page (title or path)")
	}
	if !strings.Contains(guest, "Welcome to Ashfall") {
		t.Error("guest slot fragments lost the visible page")
	}

	owner := get(t, h, "/p/welcome.md?as=mira").Body.String()
	if !strings.Contains(owner, "The Cinder Pact") {
		t.Error("owner slot fragments dropped their own secret page")
	}
}

// MayWrite comes from the write path's own authorizeWrite gate, so the
// quick-actions Edit affordance can never appear where a write would be
// refused — including GM previews (preview-writes-403).
func TestRenderShellMayWriteMatchesWriteGate(t *testing.T) {
	h := &ReadHandlers{Store: newFakeStore(), SlotRegistry: NewSlotRegistry(), Slots: newFakeSlotSource()}
	// The fake store's pages carry no owner/editable-by grants, so only a GM
	// may write; a previewing GM and a guest may not.
	for _, tc := range []struct {
		target string
		want   bool
	}{
		{"/p/welcome.md?as=gm", true},
		{"/p/welcome.md?as=gm&preview_as=mira", false},
		{"/p/welcome.md", false},
	} {
		body := get(t, h, tc.target).Body.String()
		want := "maywrite=" + strconv.FormatBool(tc.want)
		if !strings.Contains(body, want) {
			t.Errorf("%s: fragments report %s", tc.target, want)
		}
	}
}

// A provider without the rendering half (the raw-registry test fallback)
// leaves mounts empty — exactly the pre-T1-D behavior.
func TestRenderShellWithoutRendererLeavesMountsEmpty(t *testing.T) {
	reg := NewSlotRegistry()
	reg.Register(SlotPageActions, SlotComponent{
		SlotName: SlotPageActions, ID: "core-actions", Component: "core-actions",
		SecretFiltered: true,
	})
	h := &ReadHandlers{Store: newFakeStore(), SlotRegistry: reg, Slots: nil}
	body := get(t, h, "/p/welcome.md").Body.String()
	mount := `<div data-plugin-slot="page-actions"`
	if !strings.Contains(body, mount) {
		t.Fatal("raw-registry mount missing entirely")
	}
	inner := body[strings.Index(body, mount):]
	inner = inner[:strings.Index(inner, "</div>")]
	if strings.Contains(inner, "<li>") {
		t.Errorf("mount rendered content without a renderer:\n%s", inner)
	}
}

// The sheet-header mount only exists where the plugin's ShowIf allows it
// (characters/ subtree): the gate lives in the slot registry, so no
// character page, no mount.
func TestSheetMountShowIfPrefixGating(t *testing.T) {
	reg := NewSlotRegistry()
	reg.Register(SlotSheetHeader, SlotComponent{
		SlotName: SlotSheetHeader, ID: "sheet-tools", Component: "sheet-tools",
		SecretFiltered: true,
		ShowIf:         ShowCondition{PathPrefix: "characters/"},
	})
	v := &auth.Viewer{UserID: "mira"}
	if got := reg.Get(SlotSheetHeader, v, "welcome.md"); len(got) != 0 {
		t.Errorf("mount appeared outside its ShowIf prefix: %d", len(got))
	}
	if got := reg.Get(SlotSheetHeader, v, "characters/mira/index.md"); len(got) != 1 {
		t.Errorf("mount missing inside its ShowIf prefix: %d", len(got))
	}
}

// slotShellData must carry the derived sheet values (never raw frontmatter).
func TestSlotShellDataSheetDerived(t *testing.T) {
	data := templates.PageData{
		Path:  "characters/mira.md",
		Title: "Mira",
		Sheet: &templates.SheetCard{Title: "Mira", Class: "Fighter", Level: 3, HP: 7, HPMax: 10},
	}
	in := slotShellData(data, &auth.Viewer{UserID: "gm", IsGM: true}, "csrf123")
	if in.Sheet == nil {
		t.Fatal("SlotShellData.Sheet is nil for a character page")
	}
	if in.Sheet.CharID != "mira" || in.Sheet.HPCur != 7 || in.Sheet.HPMax != 10 {
		t.Errorf("derived sheet values wrong: %+v", *in.Sheet)
	}
	if in.CSRFToken != "csrf123" {
		t.Errorf("csrf not carried: %q", in.CSRFToken)
	}
	if in.IsGM != true {
		t.Error("IsGM not carried")
	}
}
