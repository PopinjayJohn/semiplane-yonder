package web

import (
	"context"
	"net/url"
	"strings"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/secrets"
	"github.com/semiplane/yonder/internal/store"
	"github.com/semiplane/yonder/web/templates"
)

// ---------------------------------------------------------------------------
// Slot fragment rendering (T1-D). The shell mounts one
// <div data-plugin-slot …> per enabled slot component; this is the
// server-side half that fills those mounts with the plugin's rendered
// fragment. Before T1-D the mounts were empty: no plugin shipped client JS
// and nothing rendered fragments, so an enabled plugin produced a
// styled-but-blank panel.
//
// Red line (P06, unchanged): SlotShellData carries ONLY already-secret-
// filtered values. web makes the same secrecy decisions it makes for its own
// markup (secrets.CanViewPage), and the plugins refuse unmarked input
// fail-closed. Titles and paths of pages this viewer may not see are ABSENT
// here — never redacted, never placeholders.
//
// Layering: web owns the data + the funnel; plugins own the rendering. The
// two meet at the SlotRenderer interface below, which the plugin registry
// implements and serve injects exactly like SlotProvider — web never imports
// plugins (import cycle), so unwired tests keep rendering empty mounts.
// ---------------------------------------------------------------------------

// SlotShellData is the filtered render input the shell offers slot plugins.
// Every field is post-filter for THIS viewer and THIS page.
type SlotShellData struct {
	Path         string // vault-relative page id (e.g. "characters/mira/index.md")
	Title        string // already-filtered page title
	Secret       bool   // page secret state as this viewer sees it
	ViewerLabel  string
	PreviewAs    string
	IsGM         bool
	MayWrite     bool // authorizeWrite gate for this page (never previewed writes)
	CampaignName string
	LandingPage  string
	CSRFToken    string
	AsParam      string
	// NewSubpageURL is the already-authorized "+ New subpage" target ("" =
	// not offered). Built with the same authorizeWrite gate as the write
	// path, so a plugin taking the page-actions slot keeps the button instead
	// of silently dropping it.
	NewSubpageURL string

	// Pages is every page visible to this viewer (Same decision as the
	// shell's own nav: secrets.CanViewPage, deterministic order). The vault
	// tree and the breadcrumb render from it.
	Pages []SlotPage

	// TOC + Backlinks are this page's filtered rows (companions).
	TOC       []SlotTOCItem
	Backlinks []SlotBacklinkItem
	// Neighbors are this page's filtered forward-link rows (companions).
	Neighbors []SlotNeighborItem

	// Sheet carries derived character-sheet values (nil when the page has no
	// sheet or the viewer may not see it).
	Sheet *SlotSheet
}

// SlotPage is one visible page row (already filtered).
type SlotPage struct {
	Path   string // extensionless nav path
	Title  string
	Secret bool
}

// SlotTOCItem is one filtered table-of-contents row.
type SlotTOCItem struct {
	Anchor string
	Title  string
}

// SlotBacklinkItem is one filtered {path, title} backlink row.
type SlotBacklinkItem struct {
	Path  string
	Title string
}

// SlotNeighborItem is one filtered forward-link row.
type SlotNeighborItem struct {
	From string
	To   string
}

// SlotSheet carries the derived sheet values a plugin may render (never raw
// frontmatter).
type SlotSheet struct {
	CharID   string
	CharName string
	HPCur    int64
	HPMax    int64
}

// SlotFragment is one rendered mount body. HTML is the plugin's escaped,
// filtered fragment; it carries its own stable target id for SSE morphs.
type SlotFragment struct {
	ID   string // component id — matched to the mount's SlotMark.ID
	HTML string
}

// SlotRenderer is the optional rendering half of the slot seam (SlotProvider
// is the metadata half). The plugin registry implements it; the raw registry
// does not, so unwired unit tests render empty mounts exactly as before.
type SlotRenderer interface {
	RenderSlots(slot string, viewer *auth.Viewer, path string, in SlotShellData) []SlotFragment
}

// allSlots is the frozen slot list, in render order.
var allSlots = []string{
	SlotHeaderLeft, SlotHeaderCenter, SlotHeaderRight,
	SlotSidebarLeft, SlotSidebarRight, SlotFooter,
	SlotPageActions, SlotSheetHeader,
}

// hasSlot reports whether the mounts include a component for this slot.
func (h *ReadHandlers) hasSlot(marks []templates.SlotMark, slot string) bool {
	for _, m := range marks {
		if m.SlotName == slot {
			return true
		}
	}
	return false
}

// mayWritePage resolves the write gate for one page through the write path's
// own authorizeWrite (status 0 = allowed). A GM preview (PreviewAs != "")
// never writes — preview-writes-403 everywhere — so previews report false.
func (h *ReadHandlers) mayWritePage(ctx context.Context, viewer *auth.Viewer, pagePath string) bool {
	if pagePath == "" || viewer == nil || viewer.PreviewAs != "" {
		return false
	}
	_, status := (&WriteHandlers{Store: h.Store}).authorizeWrite(ctx, viewer, pagePath)
	return status == 0
}

// newSubpageURL rebuilds the core shell's "+ New subpage" target for a
// character page the viewer may write in ("" otherwise), so the quick-actions
// plugin can offer the same button the core fallback did.
func newSubpageURL(data templates.PageData) string {
	if !data.ShowNewSubpage || data.NewSubpageParent == "" {
		return ""
	}
	return "/p/new?parent=" + url.QueryEscape(data.NewSubpageParent) + data.AsParam
}

// slotShellData builds the filtered shell input from an already-filtered
// PageData. It adds no secrecy decisions of its own beyond the neighbor
// lookup, which re-checks ACL the same way visibleBacklinks does.
func slotShellData(data templates.PageData, viewer *auth.Viewer, csrf string) SlotShellData {
	in := SlotShellData{
		Path:          data.Path,
		Title:         data.Title,
		Secret:        data.Secret,
		ViewerLabel:   data.ViewerLabel,
		PreviewAs:     data.PreviewAs,
		IsGM:          viewer != nil && viewer.IsGM,
		CampaignName:  data.CampaignName,
		LandingPage:   data.LandingPage,
		CSRFToken:     csrf,
		AsParam:       data.AsParam,
		NewSubpageURL: newSubpageURL(data),
	}
	for _, p := range data.Nav {
		in.Pages = append(in.Pages, SlotPage{Path: p.Path, Title: p.Title, Secret: p.Secret})
	}
	for _, e := range data.TOC {
		in.TOC = append(in.TOC, SlotTOCItem{Anchor: e.Anchor, Title: e.Title})
	}
	for _, b := range data.Backlinks {
		in.Backlinks = append(in.Backlinks, SlotBacklinkItem{Path: b.Path, Title: b.Title})
	}
	if data.Sheet != nil {
		// CharID is the <pc> id (the sheet-tools rest action builds
		// /characters/<id>/rest), not the page path.
		clean := strings.TrimSuffix(strings.TrimSuffix(data.Path, ".md"), ".markdown")
		if pc, ok := characterPC(clean); ok {
			in.Sheet = &SlotSheet{
				CharID:   pc,
				CharName: data.Sheet.Title,
				HPCur:    data.Sheet.HP,
				HPMax:    data.Sheet.HPMax,
			}
		}
	}
	return in
}

// visibleForwardLinks resolves this page's forward links to filtered
// {from, to} rows (same ACL check as visibleBacklinks; secret targets the
// viewer may not see are dropped, never redacted).
func visibleForwardLinks(ctx context.Context, st store.Store, viewer *auth.Viewer, pagePath string) []SlotNeighborItem {
	targets, err := st.ForwardLinks(ctx, pagePath)
	if err != nil {
		return nil
	}
	var out []SlotNeighborItem
	for _, t := range targets {
		if len(out) >= 50 {
			break
		}
		tp, err := st.PageGet(ctx, t)
		if err != nil {
			continue
		}
		if tp.Secret && !secrets.CanViewPage(viewer, tp.Secret, tp.Path, tp.Owner, tp.EditableBy) {
			continue
		}
		out = append(out, SlotNeighborItem{
			From: pagePath,
			To:   strings.TrimSuffix(strings.TrimSuffix(tp.Path, ".md"), ".markdown"),
		})
	}
	return out
}
