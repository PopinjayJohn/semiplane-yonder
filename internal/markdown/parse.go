package markdown

import (
	"bytes"
	"context"
	"path"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
)

// normalizePath converts a vault-relative path to posix form (page ID).
// Lookup is case-insensitive elsewhere; display preserves source case.
func normalizePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "./")
	for strings.HasPrefix(p, "/") {
		p = strings.TrimPrefix(p, "/")
	}
	return path.Clean(p)
}

// Parse parses markdown content into a Page structure.
// This is the frozen contract - Lane A owns implementation.
// Uses goldmark with Obsidian extensions.
// Phase 0c: never fails on bad frontmatter — quarantines instead
// (sets Page.Quarantined + QuarantineReason, preserves raw content).
//
// Quarantined pages fail closed: Secret is forced true (GM-only) so a
// broken `secret:` key can never leak. Rendering still proceeds; the read
// path (Lane F1) shows the GM warning banner. Parse returns a non-nil
// error only when ctx is cancelled.
func Parse(ctx context.Context, content string, pagePath string) (*Page, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	page := &Page{
		Path:        normalizePath(pagePath),
		Content:     content, // raw source, byte-identical: never rewritten
		Frontmatter: Frontmatter{},
		Blocks:      []Block{},
		Links:       []Link{},
		Embeds:      []Embed{},
		TOC:         []TOCEntry{},
		Tags:        []string{},
	}

	fmText, body, hasFM := splitFrontmatter(content)
	// Offsets inside the parsed body shift by the frontmatter prefix;
	// positions are reported against the full source.
	prefixLen := len(content) - len(body)
	if hasFM {
		fm, _, bad := parseFrontmatter(fmText)
		page.Frontmatter = fm
		if len(bad) > 0 {
			page.Quarantined = true
			page.QuarantineReason = strings.Join(bad, "; ")
		}
		applyFrontmatter(page, fm)
	} else if fmText == "" && body == content && isUnterminatedFM(content) {
		page.Quarantined = true
		page.QuarantineReason = "unterminated frontmatter"
	}
	if page.Quarantined {
		// Fail closed: a broken frontmatter block may hide `secret: true`.
		page.Secret = true
		page.Owner = ""
		page.EditableBy = nil
	}

	md := newMarkdown()
	bodyBytes := []byte(body)
	root := md.Parser().Parse(text.NewReader(bodyBytes), parser.WithContext(parser.NewContext()))
	doc, ok := root.(*ast.Document)
	if !ok {
		return nil, nil
	}
	page.AST = doc

	var htmlBuf bytes.Buffer
	if err := md.Renderer().Render(&htmlBuf, bodyBytes, doc); err != nil {
		return nil, err
	}
	page.HTML = htmlBuf.String()

	extract(doc, bodyBytes, prefixLen, content, page, ctx)

	// `<!-- optional:id=... -->` structural markers (kept invisible in
	// HTML; they are index metadata for the optionals table).
	for _, loc := range optionalMarkerRe.FindAllStringSubmatchIndex(body, -1) {
		id := body[loc[2]:loc[3]]
		off := loc[0]
		pl, pc := offsetToLineCol(content, prefixLen+off)
		page.Blocks = append(page.Blocks, Block{
			Type:     "optional",
			Content:  id,
			Position: Position{Line: pl, Column: pc, Offset: prefixLen + off},
		})
	}

	if page.Title == "" {
		page.Title = titleFromPath(page.Path)
	}
	return page, nil
}

// isUnterminatedFM reports a leading `---` line with no closing fence.
func isUnterminatedFM(content string) bool {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	normalized = strings.ReplaceAll(normalized, "\r", "\n")
	lines := strings.Split(normalized, "\n")
	if len(lines) == 0 || strings.TrimRight(lines[0], " \t") != "---" {
		return false
	}
	for i := 1; i < len(lines); i++ {
		trimmed := strings.TrimRight(lines[i], " \t")
		if trimmed == "---" || trimmed == "..." {
			return false
		}
	}
	return true
}

// markerOnlyHTML reports whether raw HTML is exactly one optional marker.
func markerOnlyHTML(raw string) bool {
	trimmed := strings.TrimSpace(raw)
	m := optionalMarkerRe.FindString(trimmed)
	return m != "" && m == trimmed
}

// applyFrontmatter copies validated frontmatter onto the page.
func applyFrontmatter(page *Page, fm Frontmatter) {
	if s, ok := fm["secret"].(bool); ok {
		page.Secret = s
	}
	if s, ok := fm["owner"].(string); ok {
		page.Owner = s
	}
	if l, ok := fm["editable-by"].([]string); ok {
		page.EditableBy = l
	}
	if t, ok := fm["title"].(string); ok && t != "" {
		page.Title = t
	}
}

// titleFromPath derives a title from the file name.
func titleFromPath(p string) string {
	base := p
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	lower := strings.ToLower(base)
	for _, ext := range []string{".markdown", ".md"} {
		if strings.HasSuffix(lower, ext) {
			base = base[:len(base)-len(ext)]
			break
		}
	}
	if base == "" || base == "." {
		return "Untitled"
	}
	return base
}

// extractor walks the rendered AST and fills index-facing page fields.
type extractor struct {
	ctx       context.Context
	source    []byte // body bytes
	full      string // full content (for absolute positions)
	prefixLen int
	page      *Page
	anchors   map[string]int
	seenTags  map[string]bool
	callouts  int // depth inside any callout (content covered by its block)
	secrets   int // depth inside secret (`-`/default) callouts
}

func extract(doc *ast.Document, body []byte, prefixLen int, full string, page *Page, ctx context.Context) {
	ex := &extractor{
		ctx:    ctx,
		source: body, full: full, prefixLen: prefixLen,
		page: page, anchors: map[string]int{}, seenTags: map[string]bool{},
	}
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		switch t := n.(type) {
		case *CalloutNode:
			if entering {
				ex.emitCallout(t)
				ex.callouts++
				if t.CalloutType == "secret" && t.Fold != "+" {
					ex.secrets++
				}
			} else {
				ex.callouts--
				if t.CalloutType == "secret" && t.Fold != "+" {
					ex.secrets--
				}
			}
			return ast.WalkContinue, nil
		case *UnsupportedNode:
			if entering {
				ex.page.Blocks = append(ex.page.Blocks, Block{
					Type: "unsupported", Content: t.Info,
					Secret:   ex.secrets > 0,
					Position: ex.pos(t.Offset),
				})
			}
			return ast.WalkSkipChildren, nil
		case *StatblockNode:
			// One `statblock` block per fence; the secret flag follows
			// the enclosing secret-callout depth (same as every block).
			// Emitted even inside callouts: inlineText cannot see fence
			// bodies, so without this the text would never reach FTS.
			if entering {
				ex.emitStatblock(t)
			}
			return ast.WalkSkipChildren, nil
		case *AdmonitionNode:
			// One `callout` block per `ad-` fence; the secret flag
			// follows the enclosing secret-callout depth, so FTS +
			// secrets behave like native callouts.
			if entering {
				ex.emitAdmonition(t)
			}
			return ast.WalkSkipChildren, nil
		}
		if !entering {
			return ast.WalkContinue, nil
		}
		switch t := n.(type) {
		case *ast.Heading:
			text := inlineText(t, ex.source)
			anchor := ex.anchor(text)
			ex.page.TOC = append(ex.page.TOC, TOCEntry{
				Level: t.Level, Title: text, Anchor: anchor,
				Secret: ex.secrets > 0,
			})
			if t.Level == 1 && ex.page.Title == "" {
				ex.page.Title = text
			}
			if ex.callouts == 0 {
				ex.page.Blocks = append(ex.page.Blocks, Block{
					Type: "heading", Content: text, Level: t.Level,
					Secret:   ex.secrets > 0,
					Position: ex.blockPos(t),
				})
			}
		case *ast.Paragraph:
			if ex.callouts == 0 {
				ex.page.Blocks = append(ex.page.Blocks, Block{
					Type: "paragraph", Content: inlineText(t, ex.source),
					Secret:   ex.secrets > 0,
					Position: ex.blockPos(t),
				})
			}
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			if ex.callouts == 0 {
				ex.page.Blocks = append(ex.page.Blocks, Block{
					Type: "code", Content: ex.codeText(n),
					Secret:   ex.secrets > 0,
					Position: ex.blockPos(n),
				})
			}
		case *ast.HTMLBlock:
			if ex.callouts == 0 {
				raw := ex.codeText(n)
				// A block that is only an optional marker is covered by
				// the marker scan below; don't double-index it.
				if markerOnlyHTML(raw) {
					break
				}
				ex.page.Blocks = append(ex.page.Blocks, Block{
					Type: "html", Content: raw,
					Secret:   ex.secrets > 0,
					Position: ex.blockPos(n),
				})
			}
		case *WikilinkNode:
			alias := t.Alias
			if alias == "" {
				alias = t.Target
			}
			_ = alias
			ex.page.Links = append(ex.page.Links, Link{
				Target: t.Target, Alias: t.Alias,
				Secret:   ex.secrets > 0,
				Position: ex.pos(t.Offset),
			})
		case *EmbedNode:
			ex.page.Embeds = append(ex.page.Embeds, Embed{
				Target: t.Target, Alt: t.Alias,
				Secret:   ex.secrets > 0,
				Position: ex.pos(t.Offset),
			})
		case *TagNode:
			if !ex.seenTags[t.Tag] {
				ex.seenTags[t.Tag] = true
				ex.page.Tags = append(ex.page.Tags, t.Tag)
			}
		case *ast.Link:
			ex.page.Links = append(ex.page.Links, Link{
				Target: string(t.Destination), Alias: inlineText(t, ex.source),
				Secret:   ex.secrets > 0,
				Position: ex.blockPos(t),
			})
		case *ast.AutoLink:
			u := string(t.URL(ex.source))
			ex.page.Links = append(ex.page.Links, Link{
				Target: u, Alias: u,
				Secret:   ex.secrets > 0,
				Position: ex.blockPos(t),
			})
		case *ast.Image:
			ex.page.Embeds = append(ex.page.Embeds, Embed{
				Target: string(t.Destination), Alt: inlineText(t, ex.source),
				Secret:   ex.secrets > 0,
				Position: ex.blockPos(t),
			})
		}
		return ast.WalkContinue, nil
	})
}

func (ex *extractor) pos(bodyOffset int) Position {
	line, col := offsetToLineCol(ex.full, ex.prefixLen+bodyOffset)
	return Position{Line: line, Column: col, Offset: ex.prefixLen + bodyOffset}
}

func (ex *extractor) blockPos(n ast.Node) Position {
	for x := n; x != nil; x = x.Parent() {
		if x.Type() == ast.TypeBlock {
			if lines := x.Lines(); lines != nil && lines.Len() > 0 {
				return ex.pos(lines.At(0).Start)
			}
		}
	}
	return ex.pos(0)
}

func (ex *extractor) anchor(title string) string {
	base := slugify(title)
	if n := ex.anchors[base]; n > 0 {
		ex.anchors[base] = n + 1
		return base + "-" + itoa(n)
	}
	ex.anchors[base] = 1
	return base
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func (ex *extractor) codeText(n ast.Node) string {
	var b strings.Builder
	if lines := n.Lines(); lines != nil {
		for i := 0; i < lines.Len(); i++ {
			sg := lines.At(i)
			b.Write(sg.Value(ex.source))
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

func (ex *extractor) emitStatblock(t *StatblockNode) {
	ex.page.Blocks = append(ex.page.Blocks, Block{
		Type: "statblock", Content: t.IndexText(),
		Secret:   ex.secrets > 0,
		Position: ex.pos(t.Offset),
	})
	// Desc wikilinks reuse the existing link path (graph + read-path
	// secret-target redaction); the image rides the embed path.
	for _, lt := range statblockBracketLinks(t.IndexText()) {
		ex.page.Links = append(ex.page.Links, Link{
			Target: lt[0], Alias: lt[1],
			Secret:   ex.secrets > 0,
			Position: ex.pos(t.Offset),
		})
	}
	if img := strings.TrimSpace(t.Data.Image); img != "" {
		ex.page.Embeds = append(ex.page.Embeds, Embed{
			Target: img, Alt: strings.TrimSpace(t.Data.ImageAlt),
			Secret:   ex.secrets > 0,
			Position: ex.pos(t.Offset),
		})
	}
}

func (ex *extractor) emitCallout(t *CalloutNode) {
	inner := inlineText(t, ex.source)
	// Stripped plain titles left the body for the title row; keep them in
	// the index text so title search recall is unchanged. Structured titles
	// stayed in the body (TitleInBody) and need no prefix.
	if strings.TrimSpace(t.Title) != "" && !t.TitleInBody {
		if inner == "" {
			inner = strings.TrimSpace(t.Title)
		} else {
			inner = strings.TrimSpace(t.Title) + "\n" + inner
		}
	}
	block := Block{
		Content:  inner,
		Secret:   ex.secrets > 0,
		Position: ex.pos(t.Offset),
	}
	switch t.CalloutType {
	case "secret":
		block.Type = "secret"
		// `-`/default = hidden from party, `+` = owner-visible expanded.
		block.Secret = t.Fold != "+"
	case "optional":
		block.Type = "optional"
		if id, ok := t.Params["id"]; ok && id != "" {
			block.Content = id
		}
	default:
		block.Type = "callout"
	}
	ex.page.Blocks = append(ex.page.Blocks, block)
}

// emitAdmonition indexes one `ad-` fence as a `callout` block (the same
// type native non-secret/non-optional callouts emit, so FTS, preview, and
// the index treat them alike). The secret flag follows the enclosing
// secret-callout depth. Body + title are sub-parsed so their text feeds the
// block and their links/embeds/tags/TOC join the page under the same flag
// (positions approximate to the fence: same convention as statblock).
// Sub-parse failures fall back to the raw strings — never a crash, and the
// fence body is always searchable.
//
// Indexing contract (fail closed): every render-visible string is indexed
// exactly once under the enclosing flag; inner-hidden (`-`/default secret)
// text is excluded from an open chunk and instead appended as its own
// secret block — the native shape for inner callout blocks, so GM search,
// FilterBlocks withholding, and preview all behave alike.
func (ex *extractor) emitAdmonition(t *AdmonitionNode) {
	secret := ex.secrets > 0
	pos := ex.pos(t.Offset)
	ctx := ex.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	var parts []string
	if title := strings.TrimSpace(t.Title); title != "" {
		if sub, err := Parse(ctx, title, ex.page.Path); err == nil && sub != nil {
			added := false
			for _, b := range sub.Blocks {
				if strings.TrimSpace(b.Content) != "" {
					parts = append(parts, b.Content)
					added = true
				}
			}
			if !added {
				parts = append(parts, title)
			}
			ex.mergeSubPage(sub, secret, pos)
		} else {
			parts = append(parts, title)
		}
	}
	if strings.TrimSpace(t.Body) != "" {
		if sub, err := Parse(ctx, t.Body, ex.page.Path); err == nil && sub != nil {
			for _, b := range sub.Blocks {
				if b.Secret && !secret {
					nb := b
					nb.Secret = true
					nb.Position = pos
					ex.page.Blocks = append(ex.page.Blocks, nb)
					continue
				}
				if strings.TrimSpace(b.Content) != "" {
					parts = append(parts, b.Content)
				}
			}
			// Tight lists carry text in TextBlocks, which emit no blocks
			// (same gap as top-level native); collect them from safe
			// regions so list-heavy ad- bodies stay searchable.
			if doc, ok := sub.AST.(*ast.Document); ok && doc != nil {
				parts = append(parts, tightListTexts(doc, []byte(t.Body), secret)...)
			}
			ex.mergeSubPage(sub, secret, pos)
		} else {
			parts = append(parts, t.Body)
		}
	}
	ex.page.Blocks = append(ex.page.Blocks, Block{
		Type: "callout", Content: strings.Join(parts, "\n"),
		Secret:   secret,
		Position: pos,
	})
}

// tightListTexts returns the plain text of top-level tight lists in a
// sub-parsed admonition body, skipping lists inside inner-hidden secret
// callouts unless the enclosing chunk is itself secret (same audience).
func tightListTexts(doc *ast.Document, body []byte, enclosingSecret bool) []string {
	var out []string
	depth := 0
	_ = ast.Walk(doc, func(n ast.Node, entering bool) (ast.WalkStatus, error) {
		if cn, ok := n.(*CalloutNode); ok {
			if cn.CalloutType == "secret" && cn.Fold != "+" {
				if entering {
					depth++
				} else {
					depth--
				}
			}
			return ast.WalkContinue, nil
		}
		if !entering {
			return ast.WalkContinue, nil
		}
		if lst, ok := n.(*ast.List); ok && lst.IsTight {
			if enclosingSecret || depth == 0 {
				if s := strings.TrimSpace(inlineText(lst, body)); s != "" {
					out = append(out, s)
				}
			}
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	return out
}

// mergeSubPage folds a sub-parsed admonition body/title into the enclosing
// page: links/embeds/tags/TOC join with the enclosing secret flag OR-ed in
// (a secret link inside an open ad- block stays secret-flagged for the
// graph + read-path redaction) and positions collapse to the fence.
func (ex *extractor) mergeSubPage(sub *Page, secret bool, pos Position) {
	for _, l := range sub.Links {
		l.Secret = l.Secret || secret
		l.Position = pos
		ex.page.Links = append(ex.page.Links, l)
	}
	for _, e := range sub.Embeds {
		e.Secret = e.Secret || secret
		e.Position = pos
		ex.page.Embeds = append(ex.page.Embeds, e)
	}
	for _, te := range sub.TOC {
		te.Secret = te.Secret || secret
		ex.page.TOC = append(ex.page.TOC, te)
	}
	for _, tag := range sub.Tags {
		if !ex.seenTags[tag] {
			ex.seenTags[tag] = true
			ex.page.Tags = append(ex.page.Tags, tag)
		}
	}
}

// inlineText renders a subtree to plain display text (custom inline nodes
// contribute their alias/target/tag text).
func inlineText(n ast.Node, source []byte) string {
	var b strings.Builder
	var walk func(x ast.Node)
	walk = func(x ast.Node) {
		switch t := x.(type) {
		case *ast.Text:
			b.Write(t.Segment.Value(source))
			if t.SoftLineBreak() {
				b.WriteByte('\n')
			}
		case *ast.String:
			b.Write(t.Value)
		case *WikilinkNode:
			if t.Alias != "" {
				b.WriteString(t.Alias)
			} else {
				b.WriteString(t.Target)
			}
		case *EmbedNode:
			if t.Alias != "" {
				b.WriteString(t.Alias)
			} else {
				b.WriteString(t.Target)
			}
		case *TagNode:
			b.WriteByte('#')
			b.WriteString(t.Tag)
		case *ast.AutoLink:
			b.Write(t.URL(source))
		default:
			for c := x.FirstChild(); c != nil; c = c.NextSibling() {
				walk(c)
			}
		}
	}
	// For container nodes render children; for leaf custom nodes above,
	// render directly.
	switch n.(type) {
	case *WikilinkNode, *EmbedNode, *TagNode, *ast.Text, *ast.String, *ast.AutoLink:
		walk(n)
	default:
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			walk(c)
		}
	}
	return b.String()
}
