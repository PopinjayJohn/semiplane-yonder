package markdown

import (
	"bytes"
	"fmt"
	"html"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/renderer"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// obsidianExt assembles goldmark core extensions plus the in-lane
// Obsidian-compat parsers (p02). No new module dependencies: everything is
// built on goldmark core parser/renderer extension points.
type obsidianExt struct{}

var _ goldmark.Extender = (*obsidianExt)(nil)

// newMarkdown builds the goldmark pipeline Lane A owns.
func newMarkdown() goldmark.Markdown {
	return goldmark.New(
		goldmark.WithExtensions(
			extension.Table,
			extension.Strikethrough,
			extension.Linkify,
			extension.TaskList,
			extension.Footnote,
			&obsidianExt{},
		),
	)
}

// Extend implements goldmark.Extender.
func (e *obsidianExt) Extend(m goldmark.Markdown) {
	m.Parser().AddOptions(
		parser.WithInlineParsers(
			util.Prioritized(&wikilinkParser{}, 150),
			util.Prioritized(&embedParser{}, 149),
			util.Prioritized(&tagParser{}, 151),
		),
		parser.WithASTTransformers(
			util.Prioritized(&calloutTransformer{}, 100),
		),
	)
	m.Renderer().AddOptions(
		renderer.WithNodeRenderers(
			util.Prioritized(&obsidianRenderer{}, 1000),
			// Below goldmark's own TaskList renderer (500): overrides
			// ONLY ast.KindTaskCheckBox with the labeled equivalent.
			util.Prioritized(&accessibleTaskRenderer{}, 499),
		),
	)
}

// --- Custom node kinds ---

var (
	kindWikilink    = ast.NewNodeKind("ObsidianWikilink")
	kindEmbed       = ast.NewNodeKind("ObsidianEmbed")
	kindTag         = ast.NewNodeKind("ObsidianTag")
	kindCallout     = ast.NewNodeKind("ObsidianCallout")
	kindUnsupported = ast.NewNodeKind("ObsidianUnsupported")
)

// WikilinkNode is a `[[target|alias]]` reference.
type WikilinkNode struct {
	ast.BaseInline
	Target string
	Alias  string
	// Offset is the byte offset of `[[` in the parsed body source.
	Offset int
}

func (n *WikilinkNode) Kind() ast.NodeKind { return kindWikilink }
func (n *WikilinkNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{
		"Target": n.Target,
		"Alias":  n.Alias,
	}, nil)
}

// EmbedNode is a `![[target|alias]]` embed.
type EmbedNode struct {
	ast.BaseInline
	Target string
	Alias  string
	// Offset is the byte offset of `![[` in the parsed body source.
	Offset int
}

func (n *EmbedNode) Kind() ast.NodeKind { return kindEmbed }
func (n *EmbedNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{
		"Target": n.Target,
		"Alias":  n.Alias,
	}, nil)
}

// TagNode is a `#tag` reference.
type TagNode struct {
	ast.BaseInline
	Tag string
	// Offset is the byte offset of `#` in the parsed body source.
	Offset int
}

func (n *TagNode) Kind() ast.NodeKind { return kindTag }
func (n *TagNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"Tag": n.Tag}, nil)
}

// CalloutNode is a `> [!type]` blockquote promoted to a typed callout.
// Fold is "", "-" (collapsed/hidden, default) or "+" (expanded/visible).
type CalloutNode struct {
	ast.BaseBlock
	CalloutType string
	Fold        string
	Params      map[string]string
	Title       string
	// Offset is the byte offset of the callout in the parsed body source.
	Offset int
}

func (n *CalloutNode) Kind() ast.NodeKind { return kindCallout }
func (n *CalloutNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{
		"Type": n.CalloutType,
		"Fold": n.Fold,
	}, nil)
}

// UnsupportedNode replaces Dataview/DataviewJS fences and Canvas embeds in
// the view (system-rendered placeholder, never authored). Source is
// preserved untouched in Page.Content.
type UnsupportedNode struct {
	ast.BaseBlock
	Info string
	// Offset is the byte offset of the block in the parsed body source.
	Offset int
}

func (n *UnsupportedNode) Kind() ast.NodeKind { return kindUnsupported }
func (n *UnsupportedNode) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"Info": n.Info}, nil)
}

// --- Inline parsers ---

// scanBracketTarget consumes `[[target|alias]]` from the head of line.
// Returns target, alias ("" when absent), bytes consumed, ok.
func scanBracketTarget(line []byte) (target, alias string, consumed int, ok bool) {
	if len(line) < 4 || line[0] != '[' || line[1] != '[' {
		return "", "", 0, false
	}
	end := bytes.Index(line[2:], []byte("]]"))
	if end < 0 {
		return "", "", 0, false
	}
	inner := string(line[2 : 2+end])
	if strings.ContainsRune(inner, '\n') {
		return "", "", 0, false
	}
	consumed = 2 + end + 2
	if i := strings.Index(inner, "|"); i >= 0 {
		target, alias = inner[:i], inner[i+1:]
	} else {
		target, alias = inner, ""
	}
	if strings.TrimSpace(target) == "" {
		return "", "", 0, false
	}
	return target, alias, consumed, true
}

type wikilinkParser struct{}

var _ parser.InlineParser = (*wikilinkParser)(nil)

func (s *wikilinkParser) Trigger() []byte { return []byte{'['} }

func (s *wikilinkParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, seg := block.PeekLine()
	if len(line) < 2 || line[0] != '[' {
		return nil
	}
	// Guard against the `[[` of an embed: the embed parser (higher
	// precedence) owns `![[...]]`; a bare `[` followed by `[` here is a
	// wikilink only when not preceded by `!`... the reader already
	// consumed past `!` in that path, so reaching `[` after `!` means the
	// embed parser declined (not `![[`), and `[[` here is a wikilink.
	target, alias, n, ok := scanBracketTarget(line)
	if !ok {
		return nil
	}
	block.Advance(n)
	node := &WikilinkNode{Target: target, Alias: alias, Offset: seg.Start}
	return node
}

type embedParser struct{}

var _ parser.InlineParser = (*embedParser)(nil)

func (s *embedParser) Trigger() []byte { return []byte{'!'} }

func (s *embedParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, seg := block.PeekLine()
	if len(line) < 5 || line[0] != '!' || line[1] != '[' || line[2] != '[' {
		return nil
	}
	target, alias, n, ok := scanBracketTarget(line[1:])
	if !ok {
		return nil
	}
	block.Advance(n + 1)
	return &EmbedNode{Target: target, Alias: alias, Offset: seg.Start}
}

type tagParser struct{}

var _ parser.InlineParser = (*tagParser)(nil)

func (s *tagParser) Trigger() []byte { return []byte{'#'} }

// isTagChar reports bytes allowed inside a tag after the first character.
func isTagChar(c byte) bool {
	return c == '/' || c == '-' || c == '_' ||
		(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c >= 0x80
}

func (s *tagParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	// Never tag inside a word or URL fragment: require a boundary before
	// `#` (start, whitespace, or opening/breaking punctuation).
	if prev := block.PrecendingCharacter(); prev != 0 && prev != '\n' {
		if prev == '#' || prev == '/' || prev == ':' || prev == '&' ||
			prev == ';' || prev == '=' || prev == '.' || prev == '_' ||
			unicode.IsLetter(prev) || unicode.IsDigit(prev) {
			return nil
		}
	}
	line, seg := block.PeekLine()
	if len(line) < 2 || line[0] != '#' {
		return nil
	}
	// First char must be a letter or underscore (never a bare number:
	// `#123` is not a tag).
	r, _ := utf8.DecodeRune(line[1:])
	if r == utf8.RuneError || (!unicode.IsLetter(r) && r != '_') {
		return nil
	}
	i := 1
	hasLetter := false
	for i < len(line) && isTagChar(line[i]) {
		if line[i] >= 0x80 || line[i] == '_' ||
			(line[i] >= 'a' && line[i] <= 'z') ||
			(line[i] >= 'A' && line[i] <= 'Z') {
			hasLetter = true
		}
		i++
	}
	tag := string(line[1:i])
	tag = strings.TrimRight(tag, "/")
	if tag == "" || !hasLetter {
		return nil
	}
	consumed := 1 + len(tag)
	// Reject digit-only tags (`#2024`); mixed (`#2024/q1`) is fine.
	block.Advance(consumed)
	return &TagNode{Tag: tag, Offset: seg.Start}
}

// --- AST transformer: callouts + unsupported ---

var calloutMarkerRe = regexp.MustCompile(
	`^\[!([A-Za-z]+)((?:\|[^\]\n]+)?)\]([+-]?)[ \t]?([^\n]*)`)

type calloutTransformer struct{}

var _ parser.ASTTransformer = (*calloutTransformer)(nil)

func (t *calloutTransformer) Transform(doc *ast.Document, reader text.Reader, pc parser.Context) {
	source := reader.Source()
	// Collect blockquotes innermost-first (reverse preorder).
	var quotes []*ast.Blockquote
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			if bq, ok := n.(*ast.Blockquote); ok {
				quotes = append(quotes, bq)
			}
		}
		return ast.WalkContinue, nil
	})
	for i := len(quotes) - 1; i >= 0; i-- {
		promoteBlockquote(quotes[i], source)
	}
	// Replace Dataview/DataviewJS fences with placeholders.
	var fences []*ast.FencedCodeBlock
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if entering {
			if fc, ok := n.(*ast.FencedCodeBlock); ok && isUnsupportedFence(fc, source) {
				fences = append(fences, fc)
			}
		}
		return ast.WalkContinue, nil
	})
	for _, fc := range fences {
		info := ""
		if fc.Info != nil {
			info = string(fc.Info.Segment.Value(source))
		}
		u := &UnsupportedNode{Info: strings.TrimSpace(info)}
		if lines := fc.Lines(); lines != nil && lines.Len() > 0 {
			u.Offset = lines.At(0).Start
		}
		if p := fc.Parent(); p != nil {
			p.ReplaceChild(p, fc, u)
		}
	}
}

func isUnsupportedFence(fc *ast.FencedCodeBlock, source []byte) bool {
	if fc.Info == nil {
		return false
	}
	lang := strings.ToLower(strings.TrimSpace(
		strings.SplitN(string(fc.Info.Segment.Value(source)), " ", 2)[0]))
	return lang == "dataview" || lang == "dataviewjs"
}

// promoteBlockquote converts a `> [!type]` blockquote into a CalloutNode,
// stripping the marker from the title line. Non-marker blockquotes are
// left alone.
//
// Note: by transformer time inline parsing has fragmented the marker
// across Text nodes (goldmark's link parser holds `[` as a potential
// opener), so the marker is matched against the paragraph's full text
// and stripped across leading Text nodes.
func promoteBlockquote(bq *ast.Blockquote, source []byte) {
	first := bq.FirstChild()
	para, ok := first.(*ast.Paragraph)
	if !ok {
		return
	}
	full := inlineText(para, source)
	m := calloutMarkerRe.FindStringSubmatch(full)
	if m == nil {
		return
	}
	calloutType := strings.ToLower(m[1])
	fold := m[3]
	params := map[string]string{}
	if m[2] != "" {
		for _, kv := range strings.Split(strings.TrimPrefix(m[2], "|"), "|") {
			if k, v, ok := strings.Cut(kv, "="); ok {
				params[strings.TrimSpace(k)] = strings.TrimSpace(v)
			} else if strings.TrimSpace(kv) != "" {
				params[strings.TrimSpace(kv)] = ""
			}
		}
	}
	rest := m[4]
	strip := len(m[0]) - len(rest)
	// Strip the marker bytes across leading Text nodes. Any structured
	// inline node inside the marker span aborts promotion (pathological).
	remaining := strip
	aborted := false
	for c := para.FirstChild(); c != nil && remaining > 0; {
		next := c.NextSibling()
		tx, ok := c.(*ast.Text)
		if !ok {
			aborted = true
			break
		}
		seg := tx.Segment
		span := seg.Stop - seg.Start
		if remaining >= span {
			remaining -= span
			para.RemoveChild(para, tx)
		} else {
			seg.Start += remaining
			tx.Segment = seg
			remaining = 0
		}
		c = next
	}
	if aborted || remaining > 0 {
		return
	}
	// Drop the paragraph if the marker was its only content.
	if para.FirstChild() == nil {
		bq.RemoveChild(bq, para)
	}
	callout := &CalloutNode{CalloutType: calloutType, Fold: fold, Params: params, Title: rest}
	if lines := para.Lines(); lines != nil && lines.Len() > 0 {
		callout.Offset = lines.At(0).Start
	}
	for c := bq.FirstChild(); c != nil; {
		next := c.NextSibling()
		bq.RemoveChild(bq, c)
		callout.AppendChild(callout, c)
		c = next
	}
	if p := bq.Parent(); p != nil {
		p.ReplaceChild(p, bq, callout)
	}
}

// --- Renderer ---

type obsidianRenderer struct{}

var _ renderer.NodeRenderer = (*obsidianRenderer)(nil)

func (r *obsidianRenderer) RegisterFuncs(reg renderer.NodeRendererFuncRegisterer) {
	reg.Register(kindWikilink, r.renderWikilink)
	reg.Register(kindEmbed, r.renderEmbed)
	reg.Register(kindTag, r.renderTag)
	reg.Register(kindCallout, r.renderCallout)
	reg.Register(kindUnsupported, r.renderUnsupported)
}

func (r *obsidianRenderer) renderWikilink(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	node := n.(*WikilinkNode)
	if !entering {
		return ast.WalkContinue, nil
	}
	alias := node.Alias
	if alias == "" {
		alias = node.Target
	}
	if _, err := fmt.Fprintf(w, `<a class="wikilink" data-target="%s" href="%s">%s</a>`,
		html.EscapeString(node.Target),
		html.EscapeString(wikilinkHref(node.Target)),
		html.EscapeString(alias)); err != nil {
		return ast.WalkStop, err
	}
	return ast.WalkSkipChildren, nil
}

// wikilinkHref builds the href for an unresolved wikilink target. Targets
// are vault-relative posix paths; route resolution happens at the read
// path (Lane F1), which rewrites these hrefs.
func wikilinkHref(target string) string {
	base := target
	if i := strings.LastIndex(base, "#"); i >= 0 {
		base = base[:i]
	}
	return base
}

var imageExts = map[string]bool{
	".png": true, ".jpg": true, ".jpeg": true, ".gif": true,
	".webp": true, ".svg": true, ".bmp": true, ".ico": true,
}

func (r *obsidianRenderer) renderEmbed(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	node := n.(*EmbedNode)
	if !entering {
		return ast.WalkContinue, nil
	}
	target := node.Target
	base := target
	if i := strings.LastIndex(base, "#"); i >= 0 {
		base = base[:i]
	}
	alt := node.Alias
	if alt == "" {
		alt = target
	}
	lower := strings.ToLower(base)
	switch {
	case strings.HasSuffix(lower, ".canvas"):
		if _, err := fmt.Fprintf(w, `<div class="callout callout-unsupported" data-callout="unsupported"><div class="callout-title">Unsupported content</div><p>Canvas embeds are not rendered in v1.</p></div>`); err != nil {
			return ast.WalkStop, err
		}
	case hasImageExt(lower):
		if _, err := fmt.Fprintf(w, `<img class="embed" src="%s" alt="%s" />`,
			html.EscapeString(base), html.EscapeString(alt)); err != nil {
			return ast.WalkStop, err
		}
	default:
		// Page embed: transclusion needs secret filtering + store, so Lane A
		// marks it and Lane F1 expands it at the read path. A span keeps
		// inline context valid HTML.
		if _, err := fmt.Fprintf(w, `<span class="embed embed-page" data-target="%s"><a class="wikilink" data-target="%s" href="%s">%s</a></span>`,
			html.EscapeString(target), html.EscapeString(target),
			html.EscapeString(wikilinkHref(target)), html.EscapeString(alt)); err != nil {
			return ast.WalkStop, err
		}
	}
	return ast.WalkSkipChildren, nil
}

func hasImageExt(s string) bool {
	i := strings.LastIndex(s, ".")
	if i < 0 {
		return false
	}
	return imageExts[s[i:]]
}

func (r *obsidianRenderer) renderTag(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	node := n.(*TagNode)
	if !entering {
		return ast.WalkContinue, nil
	}
	if _, err := fmt.Fprintf(w, `<a class="tag" data-tag="%s" href="#tag/%s">#%s</a>`,
		html.EscapeString(node.Tag), html.EscapeString(node.Tag),
		html.EscapeString(node.Tag)); err != nil {
		return ast.WalkStop, err
	}
	return ast.WalkSkipChildren, nil
}

func (r *obsidianRenderer) renderCallout(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	node := n.(*CalloutNode)
	if entering {
		attrs := fmt.Sprintf(`class="callout callout-%s" data-callout="%s"`,
			html.EscapeString(node.CalloutType), html.EscapeString(node.CalloutType))
		if node.Fold == "+" || node.Fold == "-" {
			fold := "collapse"
			if node.Fold == "+" {
				fold = "expand"
			}
			attrs += fmt.Sprintf(` data-fold="%s"`, fold)
		}
		if id, ok := node.Params["id"]; ok && id != "" {
			attrs += fmt.Sprintf(` data-optional-id="%s"`, html.EscapeString(id))
		}
		if _, err := fmt.Fprintf(w, "<div %s>\n", attrs); err != nil {
			return ast.WalkStop, err
		}
		return ast.WalkContinue, nil
	}
	_, _ = w.WriteString("</div>\n")
	return ast.WalkContinue, nil
}

func (r *obsidianRenderer) renderUnsupported(w util.BufWriter, source []byte, n ast.Node, entering bool) (ast.WalkStatus, error) {
	node := n.(*UnsupportedNode)
	if !entering {
		return ast.WalkContinue, nil
	}
	lang := node.Info
	if i := strings.IndexByte(lang, ' '); i >= 0 {
		lang = lang[:i]
	}
	if _, err := fmt.Fprintf(w, `<div class="callout callout-unsupported" data-callout="unsupported"><div class="callout-title">Unsupported content</div><p>%s blocks are not rendered in v1. The source is preserved.</p></div>`+"\n",
		html.EscapeString(lang)); err != nil {
		return ast.WalkStop, err
	}
	return ast.WalkSkipChildren, nil
}

// optionalMarkerRe finds `<!-- optional:id=... -->` structural markers.
var optionalMarkerRe = regexp.MustCompile(`(?i)<!--[ \t]*optional:id=([A-Za-z0-9_./-]+)[ \t]*-->`)
