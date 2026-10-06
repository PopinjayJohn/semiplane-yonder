package secrets

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/markdown"
	"github.com/semiplane/yonder/internal/store"
)

// ErrNotFound is returned by Filter when the viewer may not view a page.
// Handlers must map it to a uniform 404 that reveals nothing about whether
// the page exists (no title, path, snippet, or timing oracle beyond the 404).
var ErrNotFound = errors.New("secrets: page not found")

// Redaction placeholders. Titles/paths of secret pages are content: they are
// redacted, never emitted, to unauthorized viewers (pitfalls: secrets leaks).
const (
	// RedactedTitle replaces secret page titles in filtered output.
	RedactedTitle = "[Secret]"
	// RedactedPage is the in-body placeholder for links/embeds pointing at a
	// secret page the viewer cannot see. The alias is always dropped.
	RedactedPage = "▓▓ redacted: secret page"
	// RedactedBlockTag labels withheld secret blocks (mockup B-01/B-03).
	RedactedBlockTag = "[secret − hidden]"
)

// hiddenBlockPlaceholder replaces a `> [!secret]-` (or default-fold) block in
// rendered HTML for viewers who may not see it. The box itself is the notice;
// no secret text leaks. Screen readers hear "redacted", not silence.
const hiddenBlockPlaceholder = `<div class="notice" role="note" aria-label="Redacted secret content"><p style="margin:0;"><span class="block-tag">` + RedactedBlockTag + `</span> Contents withheld.</p></div>`

// CanViewSecret returns true if the viewer can see a secret page.
// GM + owner + editable-by only; non-secret pages are guest-readable.
// Delegates to store.PageVisible (Lane B API) so the read path, search, and
// the index filter share one ACL decision — a single missed join leaks.
func CanViewSecret(viewer *auth.Viewer, page *markdown.Page) bool {
	if page == nil {
		return false
	}
	if !page.Secret {
		return true
	}
	editableByJSON, _ := json.Marshal(page.EditableBy)
	return store.PageVisible(true, page.Path, page.Owner, string(editableByJSON), viewer)
}

// CanViewPage reports page visibility over index-row primitives (store.Page
// shape). secret=false is guest-readable; secret=true needs GM, owner, or
// editable-by (case-insensitive), plus path-scoped OwnedSlugs/Grants
// (nearest-ancestor inheritance applied by the caller).
func CanViewPage(viewer *auth.Viewer, secret bool, pagePath, owner string, editableBy []string) bool {
	if !secret {
		return true
	}
	eb, _ := json.Marshal(editableBy)
	return store.PageVisible(secret, pagePath, owner, string(eb), viewer)
}

// CanViewBlock returns true if the viewer can see a secret block.
// `> [!secret]-` (default, Block.Secret=true) is visible to GM + page owner
// only; `+` (Block.Secret=false) follows the page gate. Editable-by-non-owner
// and other players never see `-` blocks.
func CanViewBlock(viewer *auth.Viewer, block markdown.Block, pageOwner string) bool {
	return store.ChunkVisible(block.Secret, pageOwner, viewer)
}

// Filter applies secret filtering to a page based on the viewer's permissions.
// This is the single server-side secret filter — ALL read paths must use it.
// Client-side hiding is a leak, not a fix.
// Returns a filtered copy of the page (original unchanged), or ErrNotFound
// when the viewer may not view a secret page (uniform 404 upstream).
func Filter(viewer *auth.Viewer, page *markdown.Page) (*markdown.Page, error) {
	if page == nil {
		return nil, ErrNotFound
	}
	if page.Secret && !CanViewSecret(viewer, page) {
		return nil, ErrNotFound
	}
	out := *page
	out.Blocks = filterBlocks(viewer, page.Blocks, page.Owner)
	out.TOC = filterTOC(viewer, page.TOC, page.Owner)
	out.Links = filterLinks(viewer, page.Links, page.Owner)
	out.Embeds = filterEmbeds(viewer, page.Embeds, page.Owner)
	out.HTML = scrubHTML(viewer, page.HTML, page.Owner)
	return &out, nil
}

// FilterBlocks applies secret filtering to a slice of blocks.
// Used for partial renders, embeds, snippets. Hidden `-` blocks are replaced
// with a placeholder (Type "secret-redacted") so renderers emit the labeled
// notice box instead of the contents — or silence a screen reader can't tell
// from missing content.
func FilterBlocks(viewer *auth.Viewer, blocks []markdown.Block, pageOwner string) ([]markdown.Block, error) {
	return filterBlocks(viewer, blocks, pageOwner), nil
}

func filterBlocks(viewer *auth.Viewer, blocks []markdown.Block, pageOwner string) []markdown.Block {
	out := make([]markdown.Block, 0, len(blocks))
	for _, b := range blocks {
		if CanViewBlock(viewer, b, pageOwner) {
			out = append(out, b)
			continue
		}
		out = append(out, markdown.Block{
			Type:     "secret-redacted",
			Position: b.Position,
		})
	}
	return out
}

func filterTOC(viewer *auth.Viewer, toc []markdown.TOCEntry, pageOwner string) []markdown.TOCEntry {
	out := make([]markdown.TOCEntry, 0, len(toc))
	for _, e := range toc {
		if store.ChunkVisible(e.Secret, pageOwner, viewer) {
			out = append(out, e)
		}
	}
	return out
}

func filterLinks(viewer *auth.Viewer, links []markdown.Link, pageOwner string) []markdown.Link {
	out := make([]markdown.Link, 0, len(links))
	for _, l := range links {
		if store.ChunkVisible(l.Secret, pageOwner, viewer) {
			out = append(out, l)
		}
	}
	return out
}

func filterEmbeds(viewer *auth.Viewer, embeds []markdown.Embed, pageOwner string) []markdown.Embed {
	out := make([]markdown.Embed, 0, len(embeds))
	for _, e := range embeds {
		if store.ChunkVisible(e.Secret, pageOwner, viewer) {
			out = append(out, e)
		}
	}
	return out
}

// FilterSearchResult redacts secret titles/paths from search results.
// Secret pages: redacted (title → "[Secret]", path hidden) for unauthorized
// viewers. Non-secret results pass through. GM sees everything.
// NOTE: the store already drops invisible pages before SearchResult
// construction; this is defense-in-depth for listing paths (tree,
// autocomplete) that must render a row without leaking title/path.
func FilterSearchResult(viewer *auth.Viewer, result *SearchResult) *SearchResult {
	if result == nil {
		return nil
	}
	if !result.Secret {
		return result
	}
	if isEffectiveGM(viewer) {
		return result
	}
	return &SearchResult{Path: "", Title: RedactedTitle}
}

func isEffectiveGM(viewer *auth.Viewer) bool {
	if viewer == nil {
		return false
	}
	if viewer.PreviewAs != "" {
		return false // GM preview filters as the previewed user, never as GM
	}
	return viewer.IsGM
}

// scrubHTML removes hidden `-` secret callout divs from pre-rendered HTML and
// replaces each with the labeled notice box. `+` (data-fold="expand") divs
// stay: they are owner-visible content for anyone past the page gate.
// The renderer emits `<div class="callout callout-secret" ...>`; only divs
// WITHOUT data-fold="expand" are hidden-from-party.
func scrubHTML(viewer *auth.Viewer, html, pageOwner string) string {
	userID, isGM := effectiveUser(viewer)
	if isGM || (userID != "" && pageOwner != "" && strings.EqualFold(pageOwner, userID)) {
		return html // GM + page owner see all blocks; nothing to scrub
	}
	const marker = `class="callout callout-secret"`
	var b strings.Builder
	b.Grow(len(html))
	rest := html
	for {
		i := strings.Index(rest, "<div")
		if i < 0 {
			b.WriteString(rest)
			break
		}
		tagEnd := strings.Index(rest[i:], ">")
		if tagEnd < 0 {
			b.WriteString(rest)
			break
		}
		tagEnd += i
		openTag := rest[i : tagEnd+1]
		if !strings.Contains(openTag, marker) || strings.Contains(openTag, `data-fold="expand"`) {
			b.WriteString(rest[:tagEnd+1])
			rest = rest[tagEnd+1:]
			continue
		}
		// Hidden secret div: skip to its matching </div> with depth counting.
		end := matchClose(rest, tagEnd+1)
		b.WriteString(rest[:i])
		if end < 0 {
			// Unbalanced markup: fail closed — drop the remainder rather
			// than risk emitting hidden content.
			b.WriteString(hiddenBlockPlaceholder)
			break
		}
		b.WriteString(hiddenBlockPlaceholder)
		rest = rest[end:]
	}
	return b.String()
}

// matchClose returns the offset just past the `</div>` matching the div whose
// content starts at from (right after its opening tag), or -1 if unbalanced.
func matchClose(s string, from int) int {
	depth := 1
	i := from
	for i < len(s) {
		lt := strings.Index(s[i:], "<")
		if lt < 0 {
			return -1
		}
		i += lt
		switch {
		case strings.HasPrefix(s[i:], "</div"):
			gt := strings.Index(s[i:], ">")
			if gt < 0 {
				return -1
			}
			depth--
			i += gt + 1
			if depth == 0 {
				return i
			}
		case strings.HasPrefix(s[i:], "<div"):
			gt := strings.Index(s[i:], ">")
			if gt < 0 {
				return -1
			}
			// Self-closing divs don't open a scope.
			if strings.HasSuffix(strings.TrimSpace(s[i:i+gt]), "/") {
				i += gt + 1
				continue
			}
			depth++
			i += gt + 1
		default:
			i++
		}
	}
	return -1
}

func effectiveUser(viewer *auth.Viewer) (string, bool) {
	if viewer == nil {
		return "", false
	}
	if viewer.PreviewAs != "" {
		return viewer.PreviewAs, false
	}
	return viewer.UserID, viewer.IsGM
}

// SearchResult represents a search hit (mirrored from store for filtering).
type SearchResult struct {
	Path    string
	Title   string
	Snippet string
	Score   float64
	Secret  bool
}

// FromStoreResult converts a store.SearchResult to a secrets.SearchResult.
func FromStoreResult(r *store.SearchResult) *SearchResult {
	if r == nil {
		return nil
	}
	return &SearchResult{
		Path:    r.Path,
		Title:   r.Title,
		Snippet: r.Snippet,
		Score:   r.Score,
		Secret:  r.Secret,
	}
}
