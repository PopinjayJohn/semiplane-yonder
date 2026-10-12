package plugins

import (
	"context"
	"fmt"
	"github.com/semiplane/yonder/internal/auth"
	"html"
	"net/url"
	"strings"

	"github.com/semiplane/yonder/internal/web"
)

// Page-actions quick-actions plugin (slot plugin).
//
// Renders small page-level controls into the frozen `page-actions` slot:
// a dice-roll link (safe GET navigation) and a GM-only secret-visibility
// toggle. No new routes, no direct writes: the toggle is a plain HTML form
// that POSTs through the existing page-edit write path (which enforces the
// double-submit CSRF check server-side), carrying the caller-supplied token.
//
// Red line: input must already be secret-filtered. RenderQuickActions
// refuses QuickActionsSource with Filtered=false (fail closed), and renders
// read-only whenever PreviewAs != "" (preview-writes-403: previews never
// expose state-changing controls).

// QuickActionsPluginID is the plugin/feature ID of the quick-actions slot.
const QuickActionsPluginID = "quick-actions"

// QuickActionsSource is the caller-supplied, ALREADY secret-filtered input.
// Filtered must be true (set only after secrets.Filter / ACL checks);
// RenderQuickActions refuses unmarked input. PagePath/PageSecret/CSRFToken
// are the already-filtered page context needed to target the existing
// write path; CSRFToken is the session's double-submit token.
// NewSubpageURL is the optional already-authorized "+ New subpage" target
// (empty = not offered).
type QuickActionsSource struct {
	Filtered      bool
	CanEdit       bool
	IsGM          bool
	PreviewAs     string
	PagePath      string // vault-relative page id, e.g. "notes/cinder-pact.md"
	PageSecret    bool   // current secret state (filtered; GM sees real state)
	CSRFToken     string // session CSRF token for the toggle form
	NewSubpageURL string // optional "+ New subpage" target ("" = not offered)
}

// NewQuickActionsSource marks caller-filtered page context as safe input.
func NewQuickActionsSource(pagePath string, pageSecret bool, csrfToken string) QuickActionsSource {
	return QuickActionsSource{Filtered: true, PagePath: pagePath, PageSecret: pageSecret, CSRFToken: csrfToken}
}

// RenderSlot (T1-D) maps the filtered page context onto the quick-actions
// source and renders the page-actions fragment. CanEdit comes from the
// write path's own authorizeWrite gate, so the Edit entry can never appear
// where the write path would refuse; previews render read-only.
func (QuickActionsPlugin) RenderSlot(slot string, in web.SlotShellData, _ *auth.Viewer) (string, error) {
	if slot != web.SlotPageActions {
		return "", nil
	}
	src := NewQuickActionsSource(in.Path, in.Secret, in.CSRFToken)
	src.CanEdit = in.MayWrite
	src.IsGM = in.IsGM
	src.PreviewAs = in.PreviewAs
	src.NewSubpageURL = in.NewSubpageURL
	f, err := RenderQuickActions(src)
	if err != nil {
		return "", err
	}
	return f.HTML, nil
}

// RenderQuickActions renders the quick-actions fragment. Unfiltered input is
// refused; preview sessions (PreviewAs != "") get a read-only fragment with
// no state-changing controls; the secret toggle is GM-only.
func RenderQuickActions(src QuickActionsSource) (UIFragment, error) {
	if !src.Filtered {
		return UIFragment{}, &PluginError{Code: "UNFILTERED_INPUT", Message: "quick-actions input must be secret-filtered"}
	}
	preview := src.PreviewAs != ""
	effectiveGM := src.IsGM && !preview

	var b strings.Builder
	b.WriteString(`<section role="region" aria-label="Quick actions" data-plugin="quick-actions" data-fragment="quick-actions">` + "\n")
	b.WriteString(`<div class="qa-actions">` + "\n")

	// Dice-roll entry: safe GET navigation to the dashboard dice tray.
	b.WriteString(`<a class="qa-btn" href="/dashboard#dice-tray" aria-label="Roll dice">Roll dice</a>` + "\n")
	b.WriteString(`<p class="qa-note" aria-live="polite">Dice rolls appear in the dashboard log.</p>` + "\n")

	// Edit entry: GET navigation, only for editors outside previews.
	if src.CanEdit && !preview {
		b.WriteString(`<a class="qa-btn" href="` + html.EscapeString(editPathFor(src.PagePath)) + `" aria-label="Edit this page">Edit this page</a>` + "\n")
	}

	// "+ New subpage": only where the caller authorized it (the same
	// authorizeWrite gate the core shell used before quick-actions took over
	// the slot — the button must not disappear when the plugin is enabled).
	if src.NewSubpageURL != "" && !preview {
		b.WriteString(`<a class="qa-btn" href="` + html.EscapeString(src.NewSubpageURL) + `" aria-label="Create a new subpage">+ New subpage</a>` + "\n")
	}

	// GM-only secret toggle: posts through the existing page-edit write
	// path (CSRF double-submit enforced there). Never rendered for
	// non-GMs or preview sessions.
	if effectiveGM {
		state := "visible to the party (guest-readable)"
		action := "Hide page from party"
		pressed := "false"
		desired := "true"
		if src.PageSecret {
			state = "hidden from the party"
			action = "Reveal page to party"
			pressed = "true"
			desired = "false"
		}
		fmt.Fprintf(&b, `<p class="qa-note" aria-live="assertive">Status: this page is currently %s.</p>`+"\n",
			html.EscapeString(state))
		b.WriteString(`<form class="qa-form" method="post" action="` + html.EscapeString(editPathFor(src.PagePath)) + `">` + "\n")
		fmt.Fprintf(&b, `<input type="hidden" name="csrf_token" value="%s">`+"\n",
			html.EscapeString(src.CSRFToken))
		// Desired end state in the write path's own `secret` vocabulary
		// (secretFlag: 1/true/yes/on); the save handler validates content.
		fmt.Fprintf(&b, `<input type="hidden" name="secret" value="%s">`+"\n", desired)
		fmt.Fprintf(&b, `<button class="qa-btn" type="submit" aria-label="%s" aria-pressed="%s">%s</button>`+"\n",
			html.EscapeString(action), pressed, html.EscapeString(action))
		b.WriteString(`<p class="qa-note">Posts through the page edit form — no direct writes.</p>` + "\n")
		b.WriteString(`</form>` + "\n")
	}

	if preview {
		b.WriteString(`<p class="qa-note">Previewing as another user: write controls hidden.</p>` + "\n")
	}

	b.WriteString(`</div>` + "\n" + `</section>`)
	return UIFragment{Target: "quick-actions", HTML: b.String()}, nil
}

// editPathFor targets the existing page-edit write path for a vault-relative
// page id, escaping each segment. Empty path falls back to the new-page
// route so the control never posts to a bare prefix.
func editPathFor(pagePath string) string {
	pagePath = strings.Trim(strings.TrimSpace(pagePath), "/")
	if pagePath == "" {
		return "/p/new"
	}
	segs := strings.Split(pagePath, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return "/edit/p/" + strings.Join(segs, "/")
}

// QuickActionsPlugin is the page-actions quick-actions slot plugin.
type QuickActionsPlugin struct{}

// ID returns the plugin identifier.
func (QuickActionsPlugin) ID() string { return QuickActionsPluginID }

// Name returns the human-readable plugin name.
func (QuickActionsPlugin) Name() string { return "Page Quick Actions" }

// Version returns the plugin version.
func (QuickActionsPlugin) Version() string { return "1.0.0" }

// Init initializes the plugin (no-op: slot-only, no state).
func (QuickActionsPlugin) Init(_ context.Context, _ *Registry) error { return nil }

// Routes returns no routes: controls POST through existing write paths only.
func (QuickActionsPlugin) Routes() []web.Route { return nil }

// Slots returns the single page-actions slot contribution.
func (QuickActionsPlugin) Slots() []web.SlotComponent {
	return []web.SlotComponent{{
		SlotName: web.SlotPageActions, ID: QuickActionsPluginID,
		Component: QuickActionsPluginID, Priority: 10,
		SecretFiltered: true, PluginID: QuickActionsPluginID,
	}}
}

// CSS returns the plugin stylesheet: @layer plugins, scoped under
// [data-plugin="quick-actions"], class selectors only, 44px targets,
// visible focus, reduced-motion respected.
func (QuickActionsPlugin) CSS() string { return quickActionsCSS }

const quickActionsCSS = `@layer plugins {
  [data-plugin="quick-actions"] .qa-actions {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem;
    align-items: center;
  }
  [data-plugin="quick-actions"] .qa-btn {
    display: inline-flex;
    align-items: center;
    justify-content: center;
    min-height: 44px;
    min-width: 44px;
    padding: 0.5rem 1rem;
    border: 1px solid var(--border, currentColor);
    border-radius: 0.375rem;
    text-decoration: none;
  }
  [data-plugin="quick-actions"] .qa-btn:focus-visible {
    outline: 3px solid var(--focus, currentColor);
    outline-offset: 2px;
  }
  [data-plugin="quick-actions"] .qa-form {
    display: contents;
  }
  [data-plugin="quick-actions"] .qa-note {
    flex-basis: 100%;
    margin: 0;
    font-size: 0.875rem;
  }
  @media (prefers-reduced-motion: reduce) {
    [data-plugin="quick-actions"] .qa-btn {
      transition: none;
    }
  }
}
`

// JS returns no JavaScript (static controls + SSE state only).
func (QuickActionsPlugin) JS() string { return "" }

// OnEnable is a no-op.
func (QuickActionsPlugin) OnEnable(_ context.Context) error { return nil }

// OnDisable is a no-op.
func (QuickActionsPlugin) OnDisable(_ context.Context) error { return nil }

// ConfigSchema returns the empty JSON schema (no configuration).
func (QuickActionsPlugin) ConfigSchema() string { return "{}" }

// ValidateConfig accepts any config (nothing to validate).
func (QuickActionsPlugin) ValidateConfig(_ map[string]any) error { return nil }

// A11y declares the plugin's keyboard/screen-reader contract.
func (QuickActionsPlugin) A11y() A11ySpec {
	return A11ySpec{
		Landmarks:   []string{"region"},
		Labels:      []string{"Roll dice", "Edit this page", "Reveal page to party", "Hide page from party"},
		LiveRegions: []string{"polite", "assertive"},
	}
}
