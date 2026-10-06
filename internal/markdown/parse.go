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

	extract(doc, bodyBytes, prefixLen, content, page)

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
	source    []byte // body bytes
	full      string // full content (for absolute positions)
	prefixLen int
	page      *Page
	anchors   map[string]int
	seenTags  map[string]bool
	callouts  int // depth inside any callout (content covered by its block)
	secrets   int // depth inside secret (`-`/default) callouts
}

func extract(doc *ast.Document, body []byte, prefixLen int, full string, page *Page) {
	ex := &extractor{
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

func (ex *extractor) emitCallout(t *CalloutNode) {
	inner := inlineText(t, ex.source)
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
