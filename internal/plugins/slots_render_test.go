package plugins

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/web"
)

func newTestRegistry(t *testing.T) *Registry {
	t.Helper()
	reg := NewRegistry(web.NewSlotRegistry(), web.NewRouteRegistry())
	for _, p := range []Plugin{
		NewVaultTreePlugin(), &HeaderPlugin{}, &CompanionsPlugin{},
		NewColophonPlugin(), QuickActionsPlugin{}, &SheetToolsPlugin{},
	} {
		if err := reg.Register(p); err != nil {
			t.Fatalf("register %s: %v", p.ID(), err)
		}
	}
	return reg
}

func enableAll(t *testing.T, reg *Registry) {
	t.Helper()
	for _, id := range []string{
		VaultTreePluginID, SlotHeaderPluginID, CompanionsPluginID,
		ColophonPluginID, QuickActionsPluginID, SheetToolsPluginID,
	} {
		if err := reg.Enable(context.Background(), id); err != nil {
			t.Fatalf("enable %s: %v", id, err)
		}
	}
}

func testShellData() web.SlotShellData {
	return web.SlotShellData{
		Path: "characters/mira/index.md", Title: "Mira", Secret: true,
		ViewerLabel: "mira", IsGM: false, MayWrite: true,
		CampaignName: "Ashfall Demo", CSRFToken: "csrf",
		Pages: []web.SlotPage{
			{Path: "characters", Title: "Characters"},
			{Path: "characters/mira/index", Title: "Mira"},
			{Path: "welcome", Title: "Welcome to Ashfall"},
			{Path: "cinder-pact", Title: "The Cinder Pact", Secret: true},
		},
		TOC:       []web.SlotTOCItem{{Anchor: "log", Title: "Session log"}},
		Backlinks: []web.SlotBacklinkItem{{Path: "welcome", Title: "Welcome to Ashfall"}},
		Neighbors: []web.SlotNeighborItem{{From: "characters/mira/index", To: "cinder-pact"}},
		Sheet:     &web.SlotSheet{CharID: "mira", CharName: "Mira", HPCur: 13, HPMax: 13},
	}
}

// Every v1 slot plugin renders its own slot from filtered data and stays
// silent on slots it does not own.
func TestSlotRenderersBySlot(t *testing.T) {
	in := testShellData()
	v := &auth.Viewer{UserID: "mira"}
	for _, tc := range []struct {
		plugin SlotRenderer
		slot   string
		want   string
	}{
		{VaultTreePlugin{}, web.SlotSidebarLeft, "Vault tree"},
		{VaultTreePlugin{}, web.SlotPageActions, ""},
		{HeaderPlugin{}, web.SlotHeaderLeft, "Breadcrumb"},
		{HeaderPlugin{}, web.SlotHeaderCenter, "search"},
		{HeaderPlugin{}, web.SlotHeaderRight, "aria-live"},
		{HeaderPlugin{}, web.SlotFooter, ""},
		{CompanionsPlugin{}, web.SlotSidebarRight, "Backlinks"},
		{CompanionsPlugin{}, web.SlotSidebarLeft, ""},
		{ColophonPlugin{}, web.SlotFooter, "Ashfall Demo"},
		{ColophonPlugin{}, web.SlotHeaderLeft, ""},
		{QuickActionsPlugin{}, web.SlotPageActions, "Roll dice"},
		{QuickActionsPlugin{}, web.SlotFooter, ""},
		{SheetToolsPlugin{}, web.SlotSheetHeader, "HP 13 of 13"},
		{SheetToolsPlugin{}, web.SlotPageActions, ""},
	} {
		got, err := tc.plugin.RenderSlot(tc.slot, in, v)
		if err != nil {
			t.Fatalf("%T %s: %v", tc.plugin, tc.slot, err)
		}
		if tc.want == "" {
			if got != "" {
				t.Errorf("%T rendered slot %s it does not own:\n%s", tc.plugin, tc.slot, got)
			}
			continue
		}
		if !strings.Contains(got, tc.want) {
			t.Errorf("%T %s missing %q:\n%s", tc.plugin, tc.slot, tc.want, got)
		}
	}
}

// The registry renders only enabled plugins, matched by component id.
func TestRegistryRenderSlotsEnabledOnly(t *testing.T) {
	reg := newTestRegistry(t)
	in := testShellData()
	v := &auth.Viewer{UserID: "mira"}

	if got := reg.RenderSlots(web.SlotFooter, v, in.Path, in); len(got) != 0 {
		t.Fatalf("disabled plugin rendered a fragment: %+v", got)
	}
	enableAll(t, reg)
	got := reg.RenderSlots(web.SlotFooter, v, in.Path, in)
	if len(got) != 1 || got[0].ID != ColophonPluginID {
		t.Fatalf("footer fragments = %+v, want one %s", got, ColophonPluginID)
	}
	if !strings.Contains(got[0].HTML, "Ashfall Demo") {
		t.Errorf("footer fragment missing campaign name:\n%s", got[0].HTML)
	}
	// The page count is an aggregate over the FILTERED page list, so a guest
	// never learns how many secret pages exist through it.
	guestIn := web.SlotShellData{CampaignName: "Ashfall Demo", Pages: in.Pages[:3]}
	f := reg.RenderSlots(web.SlotFooter, &auth.Viewer{}, in.Path, guestIn)
	if len(f) != 1 || strings.Contains(f[0].HTML, "4 pages") {
		t.Errorf("colophon count leaked the filtered total:\n%s", f[0].HTML)
	}
}

// A renderer that fails leaves its mount empty — never partial, never a
// broken page.
func TestRegistryRenderSlotsFailClosed(t *testing.T) {
	reg := newTestRegistry(t)
	enableAll(t, reg)
	reg.mu.RLock()
	reg.plugins[ColophonPluginID] = &brokenPlugin{&ColophonPlugin{}}
	reg.mu.RUnlock()

	got := reg.RenderSlots(web.SlotFooter, &auth.Viewer{}, "welcome.md", web.SlotShellData{})
	if len(got) != 0 {
		t.Errorf("broken plugin produced a fragment: %+v", got)
	}
	// Other plugins are unaffected.
	if got := reg.RenderSlots(web.SlotSidebarLeft, &auth.Viewer{}, "welcome.md", web.SlotShellData{}); len(got) != 1 {
		t.Errorf("unrelated plugins broke: %+v", got)
	}
}

// brokenPlugin shadows the colophon's render hook with a failure; the
// embedded *ColophonPlugin keeps the rest of the Plugin contract intact.
type brokenPlugin struct{ *ColophonPlugin }

func (brokenPlugin) RenderSlot(string, web.SlotShellData, *auth.Viewer) (string, error) {
	return "", errors.New("boom")
}

// Sheet-tools renders the empty state when there is no sheet, and HP status
// as text ("HP n of m"), never color-only.
func TestSheetToolsEmptyStateAndText(t *testing.T) {
	noSheet := web.SlotShellData{Path: "welcome.md"}
	got, err := SheetToolsPlugin{}.RenderSlot(web.SlotSheetHeader, noSheet, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "No sheet on this page") {
		t.Errorf("empty state missing:\n%s", got)
	}
	withSheet := testShellData()
	got2, err := SheetToolsPlugin{}.RenderSlot(web.SlotSheetHeader, withSheet, &auth.Viewer{UserID: "gm", IsGM: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"HP 13 of 13", "Short rest", "Long rest", "csrf", `action="/characters/mira/rest"`} {
		if !strings.Contains(got2, want) {
			t.Errorf("sheet-tools missing %q:\n%s", want, got2)
		}
	}
}

// Preview sessions render read-only (preview-writes-403 reaches the slot
// fragments too).
func TestSheetToolsPreviewReadOnly(t *testing.T) {
	in := testShellData()
	in.PreviewAs = "mira"
	got, err := SheetToolsPlugin{}.RenderSlot(web.SlotSheetHeader, in, &auth.Viewer{UserID: "gm", IsGM: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "<form") || strings.Contains(got, "Short rest") {
		t.Errorf("preview rendered state-changing controls:\n%s", got)
	}
}

// Quick-actions hides Edit for viewers the write gate refuses and for
// previews; GM-only secret toggle stays GM-only.
func TestQuickActionsGating(t *testing.T) {
	base := testShellData()
	base.MayWrite = false
	got, err := QuickActionsPlugin{}.RenderSlot(web.SlotPageActions, base, &auth.Viewer{UserID: "mira"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "Edit this page") {
		t.Errorf("Edit rendered without the write grant:\n%s", got)
	}
	if strings.Contains(got, "Hide page from party") {
		t.Error("secret toggle rendered for a non-GM")
	}

	base.MayWrite = true
	base.IsGM = true
	base.NewSubpageURL = "/p/new?parent=characters%2Fmira"
	gm := &auth.Viewer{UserID: "gm", IsGM: true}
	// testShellData's page is secret, so the GM toggle offers the reveal.
	got2, _ := QuickActionsPlugin{}.RenderSlot(web.SlotPageActions, base, gm)
	for _, want := range []string{"Edit this page", "Reveal page to party", "csrf", "+ New subpage"} {
		if !strings.Contains(got2, want) {
			t.Errorf("GM fragment missing %q:\n%s", want, got2)
		}
	}
	// A non-secret page offers the hide.
	open := testShellData()
	open.Secret = false
	open.IsGM = true
	got3, _ := QuickActionsPlugin{}.RenderSlot(web.SlotPageActions, open, gm)
	if !strings.Contains(got3, "Hide page from party") {
		t.Errorf("GM fragment missing the hide toggle for an open page:\n%s", got3)
	}

	preview := testShellData()
	preview.PreviewAs = "mira"
	preview.MayWrite = false
	preview.IsGM = true // the previewing GM's own flag; the plugin must still refuse
	got4, _ := QuickActionsPlugin{}.RenderSlot(web.SlotPageActions, preview, gm)
	if strings.Contains(got4, "<form") {
		t.Errorf("preview rendered a POST form:\n%s", got4)
	}
}

// Breadcrumb ancestors are resolved against the filtered page list: a secret
// ancestor the viewer may not see never appears as a crumb.
func TestBreadcrumbFilteredAncestors(t *testing.T) {
	in := testShellData()
	got, err := HeaderPlugin{}.RenderSlot(web.SlotHeaderLeft, in, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, ">Characters<") || !strings.Contains(got, ">Mira<") {
		t.Errorf("visible ancestors missing:\n%s", got)
	}
	// A guest's filtered list simply lacks the secret ancestor, so the crumb
	// trail never names or links it.
	secretAncestor := web.SlotShellData{
		Path:  "cinder-pact/deep/page.md",
		Title: "Deep",
		Pages: []web.SlotPage{{Path: "welcome", Title: "Welcome to Ashfall"}},
	}
	got2, _ := HeaderPlugin{}.RenderSlot(web.SlotHeaderLeft, secretAncestor, nil)
	if strings.Contains(got2, "cinder-pact") || strings.Contains(got2, "Cinder Pact") {
		t.Errorf("secret ancestor leaked into the breadcrumb:\n%s", got2)
	}
}
