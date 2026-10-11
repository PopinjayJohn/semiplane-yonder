package markdown

import (
	"bytes"
	"fmt"
	"html"
	"regexp"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// Callout / admonition follow-up (S3 plan: callout polish second).
//
// Two syntaxes share one component:
//   - Native callout: `> [!type] title` with `-/+` fold suffix, parsed to
//     CalloutNode (obsidian.go).
//   - Admonition fence: ` ```ad-<type> ` with header params `title:`,
//     `collapse:` (`open`/`closed`/`none`), `icon:`, `color:` (RGB triad),
//     `metadata:` (→ `data-callout-metadata`), then a markdown body.
//     Parsed to AdmonitionNode below.
//
// Both render through writeCalloutOpen/writeCalloutClose: an outer
// `<div class="callout callout-<type>">` (the exact shape the server-side
// secret scrub matches — see internal/secrets scrubHTML — so the outer div
// plus `data-fold="collapse|expand"` semantics are frozen), a title row,
// and `-`/`+` (or `collapse: closed/open`) folding via native
// `<details>/<summary>` (keyboard-reachable, announced, no JS; the print
// stylesheet already expands `details`).
//
// Unknown/custom `ad-` types render generically by slug (no settings lookup
// in v1): same box, title defaults to the capitalized slug, fallback icon.
// Never a crash, never a vault rewrite (quarantine pattern).

var kindAdmonition = ast.NewNodeKind("ObsidianAdmonition")

// AdmonitionNode replaces an ```ad-<type> fence in the AST (in place, so
// fences nested in secret callouts stay nested and the depth-counted secret
// scrub keeps covering them). Page.Content stays byte-identical.
type AdmonitionNode struct {
	ast.BaseBlock
	CalloutType string // canonical type, or sanitized custom slug
	Title       string // explicit title (markdown); "" with TitleSet = omit row
	TitleSet    bool   // title: param present (blank omits the title row)
	Collapse    string // "", "open", "closed", "none"
	Icon        string // icon override name ("" = per-type default)
	Color       string // validated "R, G, B" triad ("" = per-type token)
	Metadata    string // -> data-callout-metadata
	Body        string // markdown body (rendered inside the component)
	// Offset is the byte offset of the fence body in the parsed body source.
	Offset int
}

func (n *AdmonitionNode) Kind() ast.NodeKind { return kindAdmonition }
func (n *AdmonitionNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"Type": n.CalloutType}, nil)
}

// calloutAliases maps admonition alias names to their canonical built-in
// type (same 12 types as Obsidian built-ins). Unknown slugs stay as-is and
// render generically. v1 map; aliases normalize at parse for both syntaxes.
var calloutAliases = map[string]string{
	"summary": "abstract", "tldr": "abstract",
	"todo": "info",
	"hint": "tip", "important": "tip",
	"check": "success", "done": "success",
	"help": "question", "faq": "question",
	"caution": "warning", "attention": "warning",
	"fail": "failure", "missing": "failure",
	"error": "danger",
	"cite":  "quote",
}

// calloutDisplayNames are the default title-row labels (capitalized type).
var calloutDisplayNames = map[string]string{
	"note": "Note", "abstract": "Abstract", "info": "Info", "tip": "Tip",
	"success": "Success", "question": "Question", "warning": "Warning",
	"failure": "Failure", "danger": "Danger", "bug": "Bug",
	"example": "Example", "quote": "Quote",
	"secret": "Secret", "optional": "Optional", "unsupported": "Unsupported content",
}

// canonicalCalloutType lowercases a slug and folds known aliases.
func canonicalCalloutType(slug string) string {
	slug = strings.ToLower(strings.TrimSpace(slug))
	if canon, ok := calloutAliases[slug]; ok {
		return canon
	}
	return slug
}

// cssCalloutSlug sanitizes a type for class/data-callout use: lowercase,
// runs outside [a-z0-9] become one dash (info words are attacker-adjacent
// vault text; never emit raw into attributes).
func cssCalloutSlug(slug string) string {
	slug = strings.ToLower(strings.TrimSpace(slug))
	var b strings.Builder
	dash := false
	for _, r := range slug {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "note"
	}
	return out
}

// calloutDisplayName returns the default title-row label for a type:
// the canonical name, or the capitalized slug for custom types.
func calloutDisplayName(calloutType string) string {
	if name, ok := calloutDisplayNames[calloutType]; ok {
		return name
	}
	if calloutType == "" {
		return "Note"
	}
	return strings.ToUpper(calloutType[:1]) + calloutType[1:]
}

// isAdmonitionFence reports fences whose info string names `ad-<type>`
// (first info word, case-insensitive) and returns the raw slug.
func isAdmonitionFence(fc *ast.FencedCodeBlock, source []byte) (string, bool) {
	if fc.Info == nil {
		return "", false
	}
	lang := strings.ToLower(strings.TrimSpace(
		strings.SplitN(string(fc.Info.Segment.Value(source)), " ", 2)[0]))
	slug, ok := strings.CutPrefix(lang, "ad-")
	if !ok || strings.TrimSpace(slug) == "" {
		return "", false
	}
	return strings.TrimSpace(slug), true
}

// adParamRe matches one admonition header line (`key: value`).
var adParamRe = regexp.MustCompile(`^(?i)(title|collapse|icon|color|metadata)\s*:(.*)$`)

// adColorRe validates the `color:` RGB triad.
var adColorRe = regexp.MustCompile(`^\s*(\d{1,3})\s*,\s*(\d{1,3})\s*,\s*(\d{1,3})\s*$`)

// parseAdmonitionBody splits a fence body into header params + markdown
// body. Leading blank lines are skipped; consecutive `key: value` lines are
// params (last wins); the first blank line after params is a separator; the
// first non-param, non-blank line ends the header and everything from there
// is body. Unknown keys are body text, not params (never silently eaten).
func parseAdmonitionBody(body string) (params map[string]string, md string) {
	params = map[string]string{}
	lines := strings.Split(body, "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	for ; i < len(lines); i++ {
		m := adParamRe.FindStringSubmatch(lines[i])
		if m == nil {
			break
		}
		params[strings.ToLower(m[1])] = strings.TrimSpace(m[2])
	}
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i < len(lines) {
		md = strings.Join(lines[i:], "\n")
	}
	return params, md
}

// newAdmonitionNode builds the node from fence body text + offset.
func newAdmonitionNode(slug, body string, offset int) *AdmonitionNode {
	params, md := parseAdmonitionBody(body)
	n := &AdmonitionNode{
		CalloutType: cssCalloutSlug(canonicalCalloutType(slug)),
		Body:        strings.Trim(md, "\n"),
		Offset:      offset,
	}
	if v, ok := params["title"]; ok {
		n.Title, n.TitleSet = v, true
	}
	if v, ok := params["collapse"]; ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "open", "closed", "none":
			n.Collapse = strings.ToLower(strings.TrimSpace(v))
		}
	}
	if v, ok := params["icon"]; ok {
		n.Icon = strings.TrimSpace(v)
	}
	if v, ok := params["color"]; ok {
		if m := adColorRe.FindStringSubmatch(v); m != nil {
			ok := true
			parts := make([]string, 0, 3)
			for _, p := range m[1:] {
				var n64 int
				_, _ = fmt.Sscanf(p, "%d", &n64)
				if n64 < 0 || n64 > 255 {
					ok = false
					break
				}
				parts = append(parts, strings.TrimSpace(p))
			}
			if ok {
				n.Color = strings.Join(parts, ", ")
			}
		}
	}
	if v, ok := params["metadata"]; ok {
		n.Metadata = v
	}
	return n
}

// --- Per-type icons (inline SVG, no new dependency) ---
//
// Decorative only (aria-hidden): the title text always carries the meaning,
// so icons are never the sole signal.

const calloutSVGOpen = `<svg class="callout-icon" viewBox="0 0 16 16" width="16" height="16" aria-hidden="true" focusable="false" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round">`

var calloutIcons = map[string]string{
	"note":        `<path d="M3.5 13.5v-10h9v10h-9z"/><path d="M5.8 6h4.4M5.8 8.5h4.4M5.8 11h2.6"/>`,
	"abstract":    `<path d="M2.5 4h11M2.5 8h11M2.5 12h7"/>`,
	"info":        `<circle cx="8" cy="8" r="5.8"/><path d="M8 7.4v3.4"/><path d="M8 4.6v.3"/>`,
	"tip":         `<path d="M8 2.3a3.6 3.6 0 0 0-2.1 6.5c.5.4.9 1 1.1 1.7h2c.2-.7.6-1.3 1.1-1.7A3.6 3.6 0 0 0 8 2.3z"/><path d="M6.9 12.3h2.2M7.2 14h1.6"/>`,
	"success":     `<circle cx="8" cy="8" r="5.8"/><path d="M5.3 8.3 7.2 10.2l3.6-4.2"/>`,
	"question":    `<circle cx="8" cy="8" r="5.8"/><path d="M6.2 6.1A2 2 0 0 1 8 5c1.1 0 2 .7 2 1.7 0 1.2-1.5 1.4-2 2.4"/><path d="M8 11.5v.3"/>`,
	"warning":     `<path d="M8 2.3 14.2 13.2H1.8z"/><path d="M8 6.4v3.1"/><path d="M8 11.5v.3"/>`,
	"failure":     `<circle cx="8" cy="8" r="5.8"/><path d="M5.9 5.9l4.2 4.2M10.1 5.9l-4.2 4.2"/>`,
	"danger":      `<path d="M9.2 1.8 3.4 9h3.7l-1.2 5.2L11.7 7H8z"/>`,
	"bug":         `<ellipse cx="8" cy="9.2" rx="3.2" ry="3.6"/><path d="M8 5.6V2.8M5.6 3.6 6.5 5.8M10.4 3.6 9.5 5.8M4.6 8H2M4.9 11 2.8 12M13.4 8h-2M13.2 12l-2.1-1"/>`,
	"example":     `<path d="M6.3 5.3 3.8 8l2.5 2.7M9.7 5.3 12.2 8l-2.5 2.7"/>`,
	"quote":       `<g fill="currentColor" stroke="none"><path d="M2.8 10.8c0-2.9 1.5-4.9 4-5.9l.6 1c-1.5.8-2.3 1.8-2.5 2.9h1.6v2.9H2.8v-.9zM9.3 10.8c0-2.9 1.5-4.9 4-5.9l.6 1c-1.5.8-2.3 1.8-2.5 2.9h1.6v2.9H9.3v-.9z"/></g>`,
	"secret":      `<rect x="3.2" y="7" width="9.6" height="6.2" rx="1"/><path d="M5.5 7V5.4a2.5 2.5 0 0 1 5 0V7"/>`,
	"optional":    `<rect x="3" y="3" width="10" height="10" rx="1" stroke-dasharray="2.4 1.6"/>`,
	"unsupported": `<rect x="3" y="3" width="10" height="10" rx="1" stroke-dasharray="2.4 1.6"/><path d="M8 5.4v3.2"/><path d="M8 10.8v.3"/>`,
}

// calloutIcon returns the inline SVG for a type (icon override names a
// known type/alias first; unknown overrides fall back to the type icon;
// unknown types get the generic box icon). Never empty.
func calloutIcon(calloutType, override string) string {
	if override != "" {
		if canon := canonicalCalloutType(override); canon != "" {
			if _, ok := calloutIcons[canon]; ok {
				return calloutSVGOpen + calloutIcons[canon] + `</svg>`
			}
		}
	}
	inner, ok := calloutIcons[calloutType]
	if !ok {
		inner = calloutIcons["unsupported"]
	}
	return calloutSVGOpen + inner + `</svg>`
}

// --- Shared callout component writer ---

// calloutConfig drives writeCalloutOpen/writeCalloutClose for both node
// kinds. FoldAttr is "", "collapse" or "expand" (the frozen data-fold
// vocabulary the secret scrub reads); Foldable/Open drive <details>.
type calloutConfig struct {
	Type       string // css slug for class + data-callout
	TitleHTML  string // rendered title-row inner (icon prepended by writer)
	OmitTitle  bool   // blank ad- title: no title row at all
	Foldable   bool
	Open       bool
	FoldAttr   string
	Metadata   string
	Color      string // "R, G, B" accent override
	OptionalID string
}

func writeCalloutOpen(w util.BufWriter, cfg calloutConfig) {
	attrs := fmt.Sprintf(`class="callout callout-%s" data-callout="%s"`,
		html.EscapeString(cfg.Type), html.EscapeString(cfg.Type))
	if cfg.FoldAttr == "collapse" || cfg.FoldAttr == "expand" {
		attrs += fmt.Sprintf(` data-fold="%s"`, cfg.FoldAttr)
	}
	if cfg.OptionalID != "" {
		attrs += fmt.Sprintf(` data-optional-id="%s"`, html.EscapeString(cfg.OptionalID))
	}
	if cfg.Metadata != "" {
		attrs += fmt.Sprintf(` data-callout-metadata="%s"`, html.EscapeString(cfg.Metadata))
	}
	if cfg.Color != "" {
		attrs += fmt.Sprintf(` style="--callout-accent: %s;"`, html.EscapeString(cfg.Color))
	}
	_, _ = fmt.Fprintf(w, "<div %s>\n", attrs)
	if cfg.Foldable {
		if cfg.Open {
			_, _ = w.WriteString("<details open>\n")
		} else {
			_, _ = w.WriteString("<details>\n")
		}
	}
	if !cfg.OmitTitle {
		if cfg.Foldable {
			_, _ = w.WriteString(`<summary class="callout-title">`)
		} else {
			_, _ = w.WriteString(`<div class="callout-title">`)
		}
		_, _ = w.WriteString(cfg.TitleHTML)
		if cfg.Foldable {
			_, _ = w.WriteString("</summary>\n")
		} else {
			_, _ = w.WriteString("</div>\n")
		}
	} else if cfg.Foldable {
		// Title row omitted but folding needs a summary control: an
		// icon-only summary labelled for assistive tech.
		_, _ = fmt.Fprintf(w, `<summary class="callout-title callout-title-blank" aria-label="%s">%s</summary>`+"\n",
			html.EscapeString(calloutDisplayName(cfg.Type)+" (expand)"), cfg.TitleHTML)
	}
	if cfg.Foldable || !cfg.OmitTitle {
		_, _ = w.WriteString(`<div class="callout-body">` + "\n")
	}
}

func writeCalloutClose(w util.BufWriter, cfg calloutConfig) {
	if cfg.Foldable || !cfg.OmitTitle {
		_, _ = w.WriteString("</div>\n")
	}
	if cfg.Foldable {
		_, _ = w.WriteString("</details>\n")
	}
	_, _ = w.WriteString("</div>\n")
}

// renderCalloutTitle renders a title string (native rest or ad- title:)
// as inline markdown and unwraps a lone paragraph, so `**bold**`,
// `[[wikilinks]]` (redaction-shaped anchors, like every body link) and
// other inline markup work in titles. Empty input renders the default
// display name for the type (plain text, escaped).
func renderCalloutTitle(title, calloutType string) string {
	title = strings.TrimSpace(title)
	if title == "" {
		return html.EscapeString(calloutDisplayName(calloutType))
	}
	md := newMarkdown()
	src := []byte(title)
	doc := md.Parser().Parse(text.NewReader(src), parser.WithContext(parser.NewContext()))
	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, src, doc); err != nil {
		return html.EscapeString(title)
	}
	out := strings.TrimSpace(buf.String())
	if strings.HasPrefix(out, "<p>") && strings.HasSuffix(out, "</p>") &&
		strings.Count(out, "<p>") == 1 {
		out = strings.TrimSuffix(strings.TrimPrefix(out, "<p>"), "</p>")
	}
	if strings.TrimSpace(out) == "" {
		return html.EscapeString(calloutDisplayName(calloutType))
	}
	return out
}

// renderAdBody renders an admonition markdown body to block HTML for
// injection inside the component. Wikilink anchors use the exact renderer
// shape, so the read path rewrites + redacts them like any body link;
// secret divs inside are covered by the global server-side scrub.
func renderAdBody(body string) string {
	if strings.TrimSpace(body) == "" {
		return ""
	}
	md := newMarkdown()
	src := []byte(body)
	doc := md.Parser().Parse(text.NewReader(src), parser.WithContext(parser.NewContext()))
	var buf bytes.Buffer
	if err := md.Renderer().Render(&buf, src, doc); err != nil {
		return "<pre>" + html.EscapeString(body) + "</pre>\n"
	}
	return buf.String()
}
