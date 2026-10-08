package plugins

import (
	"fmt"
	"regexp"
	"strings"
)

// `plugin check` — P06 CSS + a11y lint for one plugin.
//
// CSS contract: core tokens only, `@layer plugins`, every selector scoped
// under `[data-plugin="<id>"]`, no bare element selectors, no `!important`,
// 20KB cap. Themes are token overrides via `[data-theme]` (not plugin CSS).
// A11y contract (WCAG 2.2 AA target): semantic landmarks, every control
// labelled, live regions declared; `plugin check` fails on missing
// labels/landmarks; redaction badges never color-only (checked as a text
// label requirement on the plugin's advertised labels).

// MaxPluginCSSBytes caps plugin CSS at 20KB (p06).
const MaxPluginCSSBytes = 20 * 1024

// Finding is one lint finding.
type Finding struct {
	Code    string // e.g. "CSS_SIZE", "CSS_SCOPE", "CSS_IMPORTANT", "A11Y_LANDMARK", "A11Y_LABEL"
	Message string
}

func (f Finding) String() string { return f.Code + ": " + f.Message }

// CheckReport is the `plugin check` result for one plugin.
type CheckReport struct {
	PluginID string
	Findings []Finding
}

// OK reports whether the plugin passed (no findings).
func (r CheckReport) OK() bool { return len(r.Findings) == 0 }

// Error returns a non-nil error when the check failed.
func (r CheckReport) Error() error {
	if r.OK() {
		return nil
	}
	msgs := make([]string, 0, len(r.Findings))
	for _, f := range r.Findings {
		msgs = append(msgs, f.String())
	}
	return &PluginError{Code: "PLUGIN_CHECK_FAILED", Message: r.PluginID + ": " + strings.Join(msgs, "; ")}
}

// CheckPlugin lints one plugin's CSS + a11y metadata.
func CheckPlugin(p Plugin) CheckReport {
	rep := CheckReport{PluginID: p.ID()}
	rep.Findings = append(rep.Findings, CheckCSS(p.ID(), p.CSS())...)
	rep.Findings = append(rep.Findings, CheckA11y(p.ID(), p.A11y())...)
	for _, s := range p.Slots() {
		if err := ValidateSlot(p.ID(), s); err != nil {
			rep.Findings = append(rep.Findings, Finding{Code: "SLOT", Message: err.Error()})
		}
	}
	return rep
}

var (
	layerRe     = regexp.MustCompile(`@layer\s+plugins\b`)
	importantRe = regexp.MustCompile(`!important`)
	// Strips comments before selector analysis so commented-out code is not linted.
	commentRe = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

// CheckCSS lints a plugin stylesheet against the P06 CSS contract.
func CheckCSS(pluginID, css string) []Finding {
	var out []Finding
	if len(css) > MaxPluginCSSBytes {
		out = append(out, Finding{
			Code:    "CSS_SIZE",
			Message: fmt.Sprintf("stylesheet %d bytes exceeds %d byte cap", len(css), MaxPluginCSSBytes),
		})
	}
	if strings.TrimSpace(css) == "" {
		return out // no CSS: nothing to scope (themes ship separately)
	}
	if !layerRe.MatchString(css) {
		out = append(out, Finding{Code: "CSS_LAYER", Message: "stylesheet must use @layer plugins"})
	}
	if importantRe.MatchString(css) {
		out = append(out, Finding{Code: "CSS_IMPORTANT", Message: "stylesheet must not use !important"})
	}
	prefix := `[data-plugin="` + pluginID + `"]`
	for i, sel := range splitSelectors(stripComments(css)) {
		sel = strings.TrimSpace(sel)
		if sel == "" || strings.HasPrefix(sel, "@") {
			continue // at-rules handled via layer check above
		}
		if !strings.Contains(sel, prefix) {
			out = append(out, Finding{
				Code:    "CSS_SCOPE",
				Message: fmt.Sprintf("selector %d %q is not scoped under %s", i+1, sel, prefix),
			})
			continue
		}
		if isBareElement(sel, prefix) {
			out = append(out, Finding{
				Code:    "CSS_BARE_ELEMENT",
				Message: fmt.Sprintf("selector %d %q targets bare elements", i+1, sel),
			})
		}
	}
	return out
}

// CheckA11y lints a plugin's accessibility metadata. Fails on missing
// landmarks/labels (p06); live-region presence is advisory-free — dice uses
// polite, reveals assertive, and a plugin with dynamic updates must declare
// at least one.
func CheckA11y(pluginID string, spec A11ySpec) []Finding {
	var out []Finding
	if len(spec.Landmarks) == 0 {
		out = append(out, Finding{Code: "A11Y_LANDMARK", Message: pluginID + ": no ARIA landmarks declared"})
	}
	if len(spec.Labels) == 0 {
		out = append(out, Finding{Code: "A11Y_LABEL", Message: pluginID + ": no control labels declared (redactions/badges need text, never color-only)"})
	}
	return out
}

func stripComments(css string) string { return commentRe.ReplaceAllString(css, "") }

// splitSelectors returns the selector text preceding every rule block,
// including rules nested inside at-rule blocks (`@layer plugins { ... }`).
// Declaration bodies contain no braces, so the text since the last
// `{`, `}`, or (implicitly) start is always a selector or at-rule header.
func splitSelectors(css string) []string {
	var out []string
	boundary := 0
	for i := 0; i < len(css); i++ {
		switch css[i] {
		case '{':
			out = append(out, css[boundary:i])
			boundary = i + 1
		case '}':
			boundary = i + 1
		}
	}
	return out
}

// isBareElement reports a selector that styles a bare element type even
// within the plugin scope (e.g. `[data-plugin="x"] div`). Scoped class,
// id, attribute (other than the scope itself), or pseudo-class targeting is
// fine.
func isBareElement(sel, prefix string) bool {
	rest := strings.ReplaceAll(sel, prefix, "")
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return false
	}
	// Strip combinators; examine each compound selector's first token.
	for _, part := range strings.FieldsFunc(rest, func(r rune) bool {
		return r == ' ' || r == '>' || r == '+' || r == '~' || r == ','
	}) {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		c := part[0]
		if c == '.' || c == '#' || c == '[' || c == ':' || c == '*' {
			if c == '*' {
				return true // universal selector
			}
			continue
		}
		return true // bare element name
	}
	return false
}
