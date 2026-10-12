package plugins

import (
	"context"
	"fmt"
	"github.com/semiplane/yonder/internal/auth"
	"html"
	"strings"

	"github.com/semiplane/yonder/internal/web"
)

// Header slot plugin (slot-header): pure server-rendered fragments for the
// shell header slots. No vault/DB writes, no new dependencies — all input is
// already secret-filtered upstream (secret titles dropped, never redacted
// here).

// SlotHeaderPluginID is the plugin ID for the header slot fragments.
const SlotHeaderPluginID = "slot-header"

// Crumb is one already-filtered breadcrumb ancestor row.
type Crumb struct {
	Path  string // vault-relative page path (link target)
	Title string // filtered display title (secret titles dropped upstream)
}

// BreadcrumbInput carries ONLY secret-filtered ancestor rows (red line).
// Construct via NewBreadcrumbInput; RenderBreadcrumb refuses Filtered=false.
type BreadcrumbInput struct {
	Filtered bool
	Crumbs   []Crumb
}

// NewBreadcrumbInput marks caller-filtered rows as safe input.
func NewBreadcrumbInput(crumbs []Crumb) BreadcrumbInput {
	return BreadcrumbInput{Filtered: true, Crumbs: append([]Crumb(nil), crumbs...)}
}

// RenderBreadcrumb renders the header-left breadcrumb: a labelled nav with an
// ordered list, all strings escaped, aria-current="page" on the last crumb
// (rendered as text, not a link — it is the current page).
func RenderBreadcrumb(in BreadcrumbInput) (UIFragment, error) {
	if !in.Filtered {
		return UIFragment{}, &PluginError{Code: "UNFILTERED_INPUT", Message: "breadcrumb input must be secret-filtered"}
	}
	var b strings.Builder
	b.WriteString(`<nav aria-label="Breadcrumb" data-fragment="breadcrumb"><ol>` + "\n")
	for i, c := range in.Crumbs {
		if i == len(in.Crumbs)-1 {
			fmt.Fprintf(&b, `<li><span aria-current="page">%s</span></li>`+"\n",
				html.EscapeString(c.Title))
			continue
		}
		fmt.Fprintf(&b, `<li><a href="%s">%s</a></li>`+"\n",
			html.EscapeString("/p/"+c.Path), html.EscapeString(c.Title))
	}
	b.WriteString("</ol></nav>")
	return UIFragment{Target: "header-breadcrumb", HTML: b.String()}, nil
}

// RenderSearchPalette renders the header-center search fragment: a GET
// (read-only, no CSRF needed) labelled form, natively keyboard-reachable,
// visible focus via core tokens (see CSS).
func RenderSearchPalette() UIFragment {
	return UIFragment{Target: "header-search", HTML: `<form role="search" action="/search" method="get" data-fragment="header-search">` +
		`<label for="header-search-input">Search pages</label>` +
		`<input type="search" id="header-search-input" name="q" autocomplete="off">` +
		`<button type="submit">Search</button></form>`}
}

// RenderLiveBadge renders the header-right SSE state fragment: a polite live
// region mirroring the shell contract (server-rendered mount only, no JS in
// v1, no secret content).
func RenderLiveBadge() UIFragment {
	return UIFragment{Target: "header-live", HTML: `<p data-sse-state aria-live="polite">Live updates: connecting…</p>`}
}

// HeaderPlugin is the slot-header Plugin: three secret-filtered header slots.
type HeaderPlugin struct{}

// HeaderSlots returns the three header slot contributions.
func HeaderSlots() []web.SlotComponent {
	return []web.SlotComponent{
		{SlotName: web.SlotHeaderLeft, ID: "breadcrumb", Component: "slot-header-breadcrumb",
			Priority: 10, SecretFiltered: true, PluginID: SlotHeaderPluginID},
		{SlotName: web.SlotHeaderCenter, ID: "search", Component: "slot-header-search",
			Priority: 10, SecretFiltered: true, PluginID: SlotHeaderPluginID},
		{SlotName: web.SlotHeaderRight, ID: "live-badge", Component: "slot-header-live-badge",
			Priority: 5, SecretFiltered: true, PluginID: SlotHeaderPluginID},
	}
}

// RenderSlot (T1-D) renders the three header fragments, one per slot:
// breadcrumb (header-left), search palette (header-center), live badge
// (header-right). Breadcrumb ancestors are resolved against the filtered
// page list — an ancestor the viewer may not see is simply absent, so the
// crumb trail never names or links a secret page.
func (HeaderPlugin) RenderSlot(slot string, in web.SlotShellData, _ *auth.Viewer) (string, error) {
	switch slot {
	case web.SlotHeaderLeft:
		f, err := RenderBreadcrumb(NewBreadcrumbInput(crumbsFor(in)))
		if err != nil {
			return "", err
		}
		return f.HTML, nil
	case web.SlotHeaderCenter:
		return RenderSearchPalette().HTML, nil
	case web.SlotHeaderRight:
		return RenderLiveBadge().HTML, nil
	}
	return "", nil
}

// crumbsFor builds the filtered ancestor trail for the current page: every
// ancestor path that exists as a visible page, in root-to-leaf order, with
// the page itself last.
func crumbsFor(in web.SlotShellData) []Crumb {
	byPath := make(map[string]string, len(in.Pages))
	for _, p := range in.Pages {
		byPath[p.Path] = p.Title
	}
	leaf := strings.TrimSuffix(strings.TrimSuffix(in.Path, ".md"), ".markdown")
	var out []Crumb
	parts := strings.Split(leaf, "/")
	for i := 1; i < len(parts); i++ {
		anc := strings.Join(parts[:i], "/")
		if title, ok := byPath[anc]; ok {
			out = append(out, Crumb{Path: anc, Title: title})
		}
	}
	out = append(out, Crumb{Path: leaf, Title: in.Title})
	return out
}

// HeaderCSS is the slot-header stylesheet (namespaced, core tokens only).
func HeaderCSS() string {
	return `@layer plugins {
[data-plugin="slot-header"] .sh-breadcrumb { display: flex; }
[data-plugin="slot-header"] .sh-breadcrumb-list { display: flex; gap: var(--space-2, 0.5rem); list-style: none; margin: 0; padding: 0; }
[data-plugin="slot-header"] .sh-breadcrumb-link { color: var(--link, inherit); }
[data-plugin="slot-header"] .sh-breadcrumb-link:focus-visible { outline: 2px solid var(--focus-ring, currentColor); outline-offset: 2px; }
[data-plugin="slot-header"] .sh-breadcrumb-current { font-weight: bold; }
[data-plugin="slot-header"] .sh-search { display: flex; gap: var(--space-1, 0.25rem); align-items: center; }
[data-plugin="slot-header"] .sh-search-input { min-width: 12rem; }
[data-plugin="slot-header"] .sh-search-input:focus-visible { outline: 2px solid var(--focus-ring, currentColor); outline-offset: 2px; }
[data-plugin="slot-header"] .sh-search-button:focus-visible { outline: 2px solid var(--focus-ring, currentColor); outline-offset: 2px; }
[data-plugin="slot-header"] .sh-live { margin: 0; }
@media (prefers-reduced-motion: reduce) {
[data-plugin="slot-header"] .sh-live { animation: none; }
}
}`
}

func (HeaderPlugin) ID() string                            { return SlotHeaderPluginID }
func (HeaderPlugin) Name() string                          { return "Header slots" }
func (HeaderPlugin) Version() string                       { return "0.1" }
func (HeaderPlugin) Init(context.Context, *Registry) error { return nil }
func (HeaderPlugin) Routes() []web.Route                   { return nil }
func (HeaderPlugin) Slots() []web.SlotComponent            { return HeaderSlots() }
func (HeaderPlugin) CSS() string                           { return HeaderCSS() }
func (HeaderPlugin) JS() string                            { return "" }
func (HeaderPlugin) OnEnable(context.Context) error        { return nil }
func (HeaderPlugin) OnDisable(context.Context) error       { return nil }
func (HeaderPlugin) ConfigSchema() string                  { return "{}" }
func (HeaderPlugin) ValidateConfig(map[string]any) error   { return nil }
func (HeaderPlugin) A11y() A11ySpec {
	return A11ySpec{
		Landmarks:   []string{"banner", "search"},
		Labels:      []string{"Breadcrumb", "Search pages", "Search"},
		LiveRegions: []string{"polite"},
	}
}
