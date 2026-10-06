package secrets

import (
	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/markdown"
)

// Filter applies secret filtering to a page based on the viewer's permissions.
// This is the single server-side secret filter - ALL read paths must use it.
// Client-side hiding is a leak, not a fix.
// Returns a filtered copy of the page (original unchanged).
func Filter(viewer *auth.Viewer, page *markdown.Page) (*markdown.Page, error) {
	return nil, nil // not implemented
}

// FilterBlocks applies secret filtering to a slice of blocks.
// Used for partial renders, embeds, snippets.
func FilterBlocks(viewer *auth.Viewer, blocks []markdown.Block) ([]markdown.Block, error) {
	return nil, nil // not implemented
}

// FilterSearchResult redacts secret titles/paths from search results.
// Secret pages: redacted (title → "[Secret]", path hidden) for unauthorized viewers.
func FilterSearchResult(viewer *auth.Viewer, result *SearchResult) *SearchResult {
	return nil // not implemented
}

// SearchResult represents a search hit (mirrored from store for filtering).
type SearchResult struct {
	Path    string
	Title   string
	Snippet string
	Score   float64
	Secret  bool
}

// CanViewSecret returns true if the viewer can see secret content.
// GM + owner + editable-by (nearest ancestor inheritance).
func CanViewSecret(viewer *auth.Viewer, page *markdown.Page) bool {
	return false // not implemented
}

// CanViewBlock returns true if the viewer can see a secret block.
// Block default '-' = hidden from party, '+' = owner visible.
func CanViewBlock(viewer *auth.Viewer, block markdown.Block, pageOwner string) bool {
	return false // not implemented
}
