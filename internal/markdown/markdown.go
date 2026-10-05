package markdown

import (
	"context"
)

// Parse parses markdown content into a Page structure.
// This is the frozen contract - Lane A owns implementation.
// Uses goldmark with Obsidian extensions.
func Parse(ctx context.Context, content string, path string) (*Page, error) {
	return nil, nil // not implemented
}

// Page represents a parsed markdown page with frontmatter and AST.
type Page struct {
	Path        string
	Title       string
	Content     string // raw markdown
	HTML        string // rendered HTML
	Frontmatter Frontmatter
	AST         any // goldmark AST (opaque to callers)
	Secret      bool
	Owner       string
	EditableBy  []string
	Blocks      []Block
	Links       []Link
	Embeds      []Embed
	TOC         []TOCEntry
}

// Frontmatter represents parsed YAML frontmatter.
// Keys are append-only after Phase 1.
type Frontmatter map[string]any

// Block represents a markdown block (paragraph, heading, code, secret, etc.).
type Block struct {
	Type     string // "paragraph", "heading", "code", "secret", "callout", etc.
	Content  string
	Level    int  // for headings
	Secret   bool // for secret blocks: true = hidden from party, false = owner-visible
	Children []Block
	Position Position
}

// Link represents a [[wikilink]] or [markdown](link).
type Link struct {
	Target   string
	Alias    string
	Secret   bool // target page is secret
	Position Position
}

// Embed represents a ![[embed]] or ![image](path).
type Embed struct {
	Target   string
	Alt      string
	Secret   bool
	Position Position
}

// TOCEntry represents a table of contents entry.
type TOCEntry struct {
	Level  int
	Title  string
	Anchor string
	Secret bool
}

// Position represents a source position in the markdown.
type Position struct {
	Line   int
	Column int
	Offset int
}
