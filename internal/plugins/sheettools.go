package plugins

import (
	"context"
	"fmt"
	"html"
	"strings"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/web"
)

// Sheet-header tools plugin (slot plugin, Phase 4).
//
// Sheets live in characters/<pc>/index.md frontmatter (spec §3); the index
// DB holds a read copy only. This plugin performs NO vault writes and owns
// no routes: it renders POST controls (short/long rest) that target the
// existing sheet handlers (POST /characters/<id>/rest), plus a text HP
// status. All input must already be secret-filtered (vault-truth funnel:
// the caller re-reads vault truth + secrets.Filter per request); rendering
// refuses unmarked input fail-closed.

// SheetToolsPluginID is the plugin ID (lowercase alnum + dashes).
const SheetToolsPluginID = "sheet-tools"

// SheetToolsSource is the caller-supplied, ALREADY secret-filtered input.
// Filtered must be true (set only by NewSheetToolsSource after the caller
// ran secrets.Filter / ACL checks); RenderSheetTools refuses unmarked input.
// PreviewAs non-empty means GM preview-as-player: the fragment is read-only
// (no POST controls) so the preview shows exactly what the previewed user
// would see, minus any state-changing affordance.
type SheetToolsSource struct {
	Filtered  bool
	HasSheet  bool   // false renders the empty state (no sheet on this page)
	IsGM      bool   // carried for future GM affordances; no branch today
	PreviewAs string // non-empty = preview mode, read-only (no forms)

	// Already-filtered display values (escaped at render).
	CharID    string // <pc> id; builds the /characters/<id>/rest action
	CharName  string // display name (filtered)
	HPCur     int64
	HPMax     int64
	CSRFToken string // session CSRF token (unescaped; escaped at render)
}

// NewSheetToolsSource marks caller-filtered values as safe input.
func NewSheetToolsSource(charID, charName string, hpCur, hpMax int64, csrfToken string) SheetToolsSource {
	return SheetToolsSource{
		Filtered: true, HasSheet: true,
		CharID: charID, CharName: charName,
		HPCur: hpCur, HPMax: hpMax, CSRFToken: csrfToken,
	}
}

// RenderSheetTools renders the sheet-header tools fragment. Read-only in
// preview mode (PreviewAs != "") and when there is no sheet; otherwise two
// labelled POST forms (short/long rest) against the existing sheet
// handlers. HP status is text ("HP n of m"), never color-only. All strings
// escaped; input must be filtered (refuses Filtered=false).
func RenderSheetTools(src SheetToolsSource) (UIFragment, error) {
	if !src.Filtered {
		return UIFragment{}, &PluginError{Code: "UNFILTERED_INPUT", Message: "sheet-tools input must be secret-filtered"}
	}
	var b strings.Builder
	b.WriteString(`<section aria-label="Sheet tools" data-plugin="sheet-tools" data-fragment="sheet-tools" class="sheet-tools">` + "\n")
	if !src.HasSheet {
		b.WriteString("<p>No sheet on this page.</p>\n</section>")
		return UIFragment{Target: "sheet-tools", HTML: b.String()}, nil
	}
	if strings.TrimSpace(src.CharName) != "" {
		fmt.Fprintf(&b, "<p><span>%s</span></p>\n", html.EscapeString(src.CharName))
	}
	// HP text status (polite live region; text, never color-only).
	fmt.Fprintf(&b, "<p class=\"sheet-tools-status\" role=\"status\" aria-live=\"polite\">HP %d of %d</p>\n", src.HPCur, src.HPMax)
	if strings.TrimSpace(src.PreviewAs) != "" {
		// Preview mode is read-only: status only, no state-changing controls.
		fmt.Fprintf(&b, "<p>Previewing as %s: controls hidden.</p>\n</section>", html.EscapeString(src.PreviewAs))
		return UIFragment{Target: "sheet-tools", HTML: b.String()}, nil
	}
	if strings.TrimSpace(src.CharID) == "" {
		// No target for the existing handlers: status only, no dead forms.
		b.WriteString("</section>")
		return UIFragment{Target: "sheet-tools", HTML: b.String()}, nil
	}
	action := "/characters/" + html.EscapeString(src.CharID) + "/rest"
	b.WriteString("<div class=\"sheet-tools-actions\">\n")
	fmt.Fprintf(&b, "<form method=\"post\" action=\"%s\">\n", action)
	fmt.Fprintf(&b, "<input type=\"hidden\" name=\"%s\" value=\"%s\">\n", auth.CSRFFieldName, html.EscapeString(src.CSRFToken))
	b.WriteString("<input type=\"hidden\" name=\"mode\" value=\"short\">\n")
	b.WriteString("<p><button class=\"sheet-tools-btn\" type=\"submit\" aria-label=\"Take a short rest\">Short rest</button></p>\n</form>\n")
	fmt.Fprintf(&b, "<form method=\"post\" action=\"%s\">\n", action)
	fmt.Fprintf(&b, "<input type=\"hidden\" name=\"%s\" value=\"%s\">\n", auth.CSRFFieldName, html.EscapeString(src.CSRFToken))
	b.WriteString("<input type=\"hidden\" name=\"mode\" value=\"long\">\n")
	b.WriteString("<p><button class=\"sheet-tools-btn\" type=\"submit\" aria-label=\"Take a long rest\">Long rest</button></p>\n</form>\n")
	b.WriteString("</div>\n</section>")
	return UIFragment{Target: "sheet-tools", HTML: b.String()}, nil
}

// SheetToolsPlugin is the slot-only sheet-tools plugin: one sheet-header
// component, no routes, no vault writes. POST controls ride the existing
// sheet handlers; a disabled plugin leaves no UI trace via SlotsFor gating.
type SheetToolsPlugin struct{}

// Compile-time Plugin conformance.
var _ Plugin = SheetToolsPlugin{}

func (SheetToolsPlugin) ID() string      { return SheetToolsPluginID }
func (SheetToolsPlugin) Name() string    { return "Sheet tools" }
func (SheetToolsPlugin) Version() string { return "0.1.0" }

func (SheetToolsPlugin) Init(_ context.Context, _ *Registry) error { return nil }

// Routes returns nil: rest POSTs target the existing sheet handlers
// (POST /characters/<id>/rest); this plugin owns no routes.
func (SheetToolsPlugin) Routes() []web.Route { return nil }

// Slots returns the single sheet-header component (secret-filtered input,
// characters/ pages only).
func (SheetToolsPlugin) Slots() []web.SlotComponent {
	return []web.SlotComponent{{
		SlotName: web.SlotSheetHeader, ID: "sheet-tools",
		Component: "sheet-tools", Priority: 10,
		ShowIf:         web.ShowCondition{PathPrefix: "characters/"},
		SecretFiltered: true, PluginID: SheetToolsPluginID,
	}}
}

// CSS returns the plugin stylesheet: @layer plugins, scoped under
// [data-plugin="sheet-tools"], no bare elements, no !important, <= 20KB.
// Touch targets meet the 44px minimum; reduced motion respected.
func (SheetToolsPlugin) CSS() string {
	return `@layer plugins {
[data-plugin="sheet-tools"] .sheet-tools { display: grid; gap: 0.75rem; }
[data-plugin="sheet-tools"] .sheet-tools-actions { display: flex; flex-wrap: wrap; gap: 0.5rem; }
[data-plugin="sheet-tools"] .sheet-tools-btn { min-height: 44px; min-width: 44px; padding: 0.625rem 1rem; }
[data-plugin="sheet-tools"] .sheet-tools-btn:focus-visible { outline: 2px solid currentColor; outline-offset: 2px; }
[data-plugin="sheet-tools"] .sheet-tools-status { margin: 0; }
@media (prefers-reduced-motion: reduce) {
[data-plugin="sheet-tools"] .sheet-tools { scroll-behavior: auto; }
}
}`
}

func (SheetToolsPlugin) JS() string { return "" }

func (SheetToolsPlugin) OnEnable(_ context.Context) error  { return nil }
func (SheetToolsPlugin) OnDisable(_ context.Context) error { return nil }

func (SheetToolsPlugin) ConfigSchema() string                  { return "{}" }
func (SheetToolsPlugin) ValidateConfig(_ map[string]any) error { return nil }

// A11y declares the screen-reader contract: region landmark, labelled rest
// controls, polite live region for the HP status.
func (SheetToolsPlugin) A11y() A11ySpec {
	return A11ySpec{
		Landmarks:   []string{"region"},
		Labels:      []string{"Sheet tools", "Take a short rest", "Take a long rest", "HP status"},
		LiveRegions: []string{"polite"},
	}
}
