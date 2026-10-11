package plugins

import (
	"context"
	"fmt"
	"html"
	"strings"

	"github.com/semiplane/yonder/internal/web"
)

// Vault-colophon footer plugin: campaign name + visible page count + SRD
// attribution. Counts only — never titles or paths (zero secret content).
// No vault/DB writes, no new dependencies.

// ColophonPluginID is the plugin ID of the footer colophon.
const ColophonPluginID = "vault-colophon"

// ColophonSource is the caller-supplied, ALREADY secret-filtered input.
// Filtered must be true (set only by NewColophonSource after the caller ran
// secrets.Filter / ACL checks); RenderColophon refuses unmarked input
// (fail closed). PageCount is an aggregate count only — individual
// titles/paths must never be passed in.
type ColophonSource struct {
	Filtered     bool
	PageCount    int
	CampaignName string
}

// NewColophonSource marks caller-filtered aggregates as safe input.
// Negative counts clamp to zero.
func NewColophonSource(campaignName string, pageCount int) ColophonSource {
	if pageCount < 0 {
		pageCount = 0
	}
	return ColophonSource{Filtered: true, PageCount: pageCount, CampaignName: campaignName}
}

// RenderColophon renders the footer fragment: campaign name (escaped),
// aggregate page-count text, and the SRD CC-BY-4.0 attribution line.
// Plain semantic markup, no scripts — print-safe.
func RenderColophon(src ColophonSource) (UIFragment, error) {
	if !src.Filtered {
		return UIFragment{}, &PluginError{Code: "UNFILTERED_INPUT", Message: "colophon input must be secret-filtered"}
	}
	count := src.PageCount
	if count < 0 {
		count = 0
	}
	pages := "pages"
	if count == 1 {
		pages = "page"
	}
	var b strings.Builder
	b.WriteString(`<footer data-plugin="vault-colophon" class="colophon">` + "\n")
	b.WriteString(`<section aria-label="About this vault" class="colophon-about">` + "\n")
	fmt.Fprintf(&b, "<p class=\"colophon-campaign\">%s</p>\n", html.EscapeString(src.CampaignName))
	fmt.Fprintf(&b, "<p class=\"colophon-count\">%d %s in this vault.</p>\n", count, pages)
	b.WriteString(`<p class="colophon-attrib">Contains material from the Systems Reference Document 5.2 (SRD 5.2) by Wizards of the Coast, licensed under CC-BY-4.0.</p>` + "\n")
	b.WriteString("</section>\n</footer>")
	return UIFragment{Target: "vault-colophon", HTML: b.String()}, nil
}

// ColophonPlugin is the Plugin implementation for the footer colophon.
type ColophonPlugin struct{}

// NewColophonPlugin builds the footer colophon plugin.
func NewColophonPlugin() *ColophonPlugin { return &ColophonPlugin{} }

func (p *ColophonPlugin) ID() string      { return ColophonPluginID }
func (p *ColophonPlugin) Name() string    { return "Vault Colophon" }
func (p *ColophonPlugin) Version() string { return "0.1.0" }

func (p *ColophonPlugin) Init(_ context.Context, _ *Registry) error { return nil }

func (p *ColophonPlugin) Routes() []web.Route { return nil }

// Slots contributes one secret-filtered footer component.
func (p *ColophonPlugin) Slots() []web.SlotComponent {
	return []web.SlotComponent{{
		SlotName: web.SlotFooter, ID: ColophonPluginID,
		Component: "vault-colophon", Priority: 5,
		SecretFiltered: true, PluginID: ColophonPluginID,
	}}
}

// CSS returns the namespaced, print-safe colophon stylesheet.
func (p *ColophonPlugin) CSS() string { return colophonCSS }

const colophonCSS = `@layer plugins {
[data-plugin="vault-colophon"] .colophon { margin-block-start: 2rem; }
[data-plugin="vault-colophon"] .colophon-about { max-width: 60ch; }
[data-plugin="vault-colophon"] .colophon-campaign { font-weight: 600; }
[data-plugin="vault-colophon"] .colophon-count { margin-block: 0.25rem; }
[data-plugin="vault-colophon"] .colophon-attrib { font-size: 0.875rem; }
@media print {
[data-plugin="vault-colophon"] .colophon { margin-block-start: 1rem; }
[data-plugin="vault-colophon"] .colophon-attrib { font-size: 0.75rem; }
}
}`

func (p *ColophonPlugin) JS() string { return "" }

func (p *ColophonPlugin) OnEnable(_ context.Context) error  { return nil }
func (p *ColophonPlugin) OnDisable(_ context.Context) error { return nil }

func (p *ColophonPlugin) ConfigSchema() string                  { return "{}" }
func (p *ColophonPlugin) ValidateConfig(_ map[string]any) error { return nil }

// A11y declares the footer landmark + label `plugin check` lints.
func (p *ColophonPlugin) A11y() A11ySpec {
	return A11ySpec{
		Landmarks: []string{"contentinfo"},
		Labels:    []string{"About this vault"},
	}
}
