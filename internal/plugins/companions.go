package plugins

import (
	"context"
	"fmt"
	"html"
	"sort"
	"strings"

	"github.com/semiplane/yonder/internal/web"
)

// Sidebar-right page-companions plugin (slot plugins lane).
//
// Pure assembly over caller-supplied, already secret-filtered rows
// (TOC from PageData.TOC, backlinks from visibleBacklinks, forward-link
// neighbors from the filtered graph payload). No vault/DB reads or writes
// here: the caller filters server-side (secrets.Filter / CanViewPage) and
// marks the input via NewCompanionsSource; Assemble refuses unmarked input
// fail-closed.

// CompanionsPluginID is the plugin/slot/component namespace.
const CompanionsPluginID = "page-companions"

// TOCItem is one already-filtered table-of-contents row.
type TOCItem struct {
	Anchor string
	Title  string
}

// BacklinkItem is one already-filtered {path, title} backlink row.
type BacklinkItem struct {
	Path  string
	Title string
}

// NeighborEdge is one already-filtered forward-link edge.
type NeighborEdge struct {
	From string
	To   string
}

// CompanionsSource is the caller-supplied, ALREADY secret-filtered input.
// Filtered must be true (set only by NewCompanionsSource after the caller
// ran secrets.Filter / ACL checks); Assemble refuses unmarked input.
type CompanionsSource struct {
	Filtered  bool
	TOC       []TOCItem
	Backlinks []BacklinkItem
	Neighbors []NeighborEdge
}

// NewCompanionsSource marks caller-filtered rows as safe input.
func NewCompanionsSource() CompanionsSource { return CompanionsSource{Filtered: true} }

// CompanionsData is the assembled, deterministically ordered model.
type CompanionsData struct {
	TOC       []TOCItem
	Backlinks []BacklinkItem
	Neighbors []NeighborEdge
}

// AssembleCompanions builds the companions model from filtered input.
// Viewer-agnostic: all secrecy decisions already happened upstream; this
// only copies + deterministically orders rows (backlinks by path/title,
// neighbors by from/to; TOC keeps document order, which is already
// deterministic). Rejects unfiltered input fail-closed.
func AssembleCompanions(src CompanionsSource) (CompanionsData, error) {
	if !src.Filtered {
		return CompanionsData{}, &PluginError{Code: "UNFILTERED_INPUT", Message: "companions input must be secret-filtered"}
	}
	out := CompanionsData{
		TOC:       append([]TOCItem(nil), src.TOC...),
		Backlinks: append([]BacklinkItem(nil), src.Backlinks...),
		Neighbors: append([]NeighborEdge(nil), src.Neighbors...),
	}
	sort.Slice(out.Backlinks, func(i, j int) bool {
		if out.Backlinks[i].Path != out.Backlinks[j].Path {
			return out.Backlinks[i].Path < out.Backlinks[j].Path
		}
		return out.Backlinks[i].Title < out.Backlinks[j].Title
	})
	sort.Slice(out.Neighbors, func(i, j int) bool {
		if out.Neighbors[i].From != out.Neighbors[j].From {
			return out.Neighbors[i].From < out.Neighbors[j].From
		}
		return out.Neighbors[i].To < out.Neighbors[j].To
	})
	return out, nil
}

// RenderCompanions renders the sidebar fragment: one section with three
// labelled regions (On this page / Backlinks / Linked pages). All strings
// are escaped; empty states render as text (never color-only, never absent
// landmarks). Regions carry stable ids for SSE/Datastar morph patching.
func RenderCompanions(d CompanionsData) UIFragment {
	var b strings.Builder
	b.WriteString(`<section aria-label="Page companions" data-fragment="page-companions" data-plugin="page-companions">` + "\n")
	// On this page (TOC).
	b.WriteString(`<div role="region" aria-label="On this page" id="companions-toc" aria-live="polite"><h2>On this page</h2>`)
	if len(d.TOC) == 0 {
		b.WriteString(`<p>No headings.</p>`)
	} else {
		b.WriteString("<ul>\n")
		for _, e := range d.TOC {
			fmt.Fprintf(&b, `<li><a href="#%s">%s</a></li>`+"\n",
				html.EscapeString(e.Anchor), html.EscapeString(e.Title))
		}
		b.WriteString("</ul>")
	}
	b.WriteString("</div>\n")
	// Backlinks.
	b.WriteString(`<div role="region" aria-label="Backlinks" id="companions-backlinks" aria-live="polite"><h2>Backlinks</h2>`)
	if len(d.Backlinks) == 0 {
		b.WriteString(`<p>No backlinks.</p>`)
	} else {
		b.WriteString("<ul>\n")
		for _, bl := range d.Backlinks {
			fmt.Fprintf(&b, `<li><a href="/p/%s">%s</a></li>`+"\n",
				html.EscapeString(bl.Path), html.EscapeString(bl.Title))
		}
		b.WriteString("</ul>")
	}
	b.WriteString("</div>\n")
	// Linked pages (forward neighbors).
	b.WriteString(`<div role="region" aria-label="Linked pages" id="companions-links" aria-live="polite"><h2>Linked pages</h2>`)
	if len(d.Neighbors) == 0 {
		b.WriteString(`<p>No linked pages.</p>`)
	} else {
		b.WriteString("<ul>\n")
		for _, n := range d.Neighbors {
			fmt.Fprintf(&b, `<li><a href="/p/%s">%s</a></li>`+"\n",
				html.EscapeString(n.To), html.EscapeString(n.To))
		}
		b.WriteString("</ul>")
	}
	b.WriteString("</div>\n</section>")
	return UIFragment{Target: "page-companions", HTML: b.String()}
}

// CompanionsPlugin is the sidebar-right page-companions UI plugin.
type CompanionsPlugin struct{}

func (p *CompanionsPlugin) ID() string      { return CompanionsPluginID }
func (p *CompanionsPlugin) Name() string    { return "Page companions" }
func (p *CompanionsPlugin) Version() string { return "0.1.0" }

func (p *CompanionsPlugin) Init(_ context.Context, _ *Registry) error { return nil }

func (p *CompanionsPlugin) Routes() []web.Route { return nil }

func (p *CompanionsPlugin) Slots() []web.SlotComponent {
	return []web.SlotComponent{{
		SlotName: web.SlotSidebarRight, ID: CompanionsPluginID,
		Component: CompanionsPluginID, Priority: 15,
		SecretFiltered: true, PluginID: CompanionsPluginID,
	}}
}

// CSS returns the plugin stylesheet: @layer plugins, every selector scoped
// under [data-plugin="page-companions"], classes only (no bare elements),
// no !important, well under the 20KB cap.
func (p *CompanionsPlugin) CSS() string { return companionsCSS }

const companionsCSS = `@layer plugins {
[data-plugin="page-companions"] .companions-wrap { display: block; margin: 0; padding: 0; }
[data-plugin="page-companions"] .companions-region { border-top: 1px solid var(--line); padding: 0.5rem 0; }
[data-plugin="page-companions"] .companions-heading { font-size: 0.9rem; margin: 0 0 0.25rem 0; }
[data-plugin="page-companions"] .companions-list { list-style: disc; margin: 0 0 0 1.25rem; padding: 0; }
[data-plugin="page-companions"] .companions-empty { font-style: italic; margin: 0.25rem 0; }
[data-plugin="page-companions"] .companions-link:focus-visible { outline: 3px solid var(--accent); outline-offset: 2px; }
@media (prefers-reduced-motion: reduce) {
[data-plugin="page-companions"] .companions-wrap { transition: none; animation: none; }
}
}`

func (p *CompanionsPlugin) JS() string { return "" }

func (p *CompanionsPlugin) OnEnable(_ context.Context) error  { return nil }
func (p *CompanionsPlugin) OnDisable(_ context.Context) error { return nil }

func (p *CompanionsPlugin) ConfigSchema() string { return "{}" }

func (p *CompanionsPlugin) ValidateConfig(_ map[string]any) error { return nil }

// A11y declares the plugin's screen-reader contract: the sidebar mounts as
// a complementary landmark; every region is labelled; dynamic backlink/link
// updates announce politely.
func (p *CompanionsPlugin) A11y() A11ySpec {
	return A11ySpec{
		Landmarks:   []string{"complementary"},
		Labels:      []string{"Page companions", "On this page", "Backlinks", "Linked pages"},
		LiveRegions: []string{"polite"},
	}
}

// Compile-time interface check.
var _ Plugin = (*CompanionsPlugin)(nil)
