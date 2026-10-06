package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/semiplane/yonder/internal/auth"
)

// ParsedBlock is one indexable chunk of a page. Ordinals are assigned at
// insert time in slice order (ordinal 0 is reserved for the synthetic
// "title + tags" chunk built by the indexer, so Chunks start at 1).
type ParsedBlock struct {
	Secret bool
	Text   string
}

// ParsedOptional is one ruleset optional offered by the page's file.
type ParsedOptional struct {
	ID    string
	Title string
	Line  int
}

// ParsedPage is the indexer's view of a parsed markdown file. It is an
// intentional projection (not markdown.Page): Lane B consumes markdown.Parse
// via fakes in Phase 1 and never imports Lane A's package. Lane F1 owns the
// real parse→index conversion at write time (amend request, not this diff).
type ParsedPage struct {
	Title           string
	FrontmatterJSON string // raw JSON object; non-indexed keys stay here only
	Secret          bool
	Owner           string
	EditableBy      []string
	Tags            []string
	Chunks          []ParsedBlock // body chunks in source order
	Links           []string      // wikilink targets (raw)
	Optionals       []ParsedOptional
}

// Parser converts file bytes to a ParsedPage. Production wiring passes a
// wrapper over markdown.Parse; tests pass fakes.
type Parser func(ctx context.Context, content []byte, path string) (*ParsedPage, error)
type Optional = ParsedOptional

// HashBytes returns the sha256 hex used for vault_files/hash change compare.
func HashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// TitleTagsChunk builds the synthetic ordinal-0 chunk text: title plus tag
// values, indexed under the page's secret flag (P04: frontmatter title + tags
// in FTS; all other frontmatter excluded).
func TitleTagsChunk(title string, tags []string) string {
	if len(tags) == 0 {
		return title
	}
	return title + "\n" + strings.Join(tags, " ")
}

// SplitViewer resolves the effective identity for index filtering. A GM
// preview (PreviewAs, GM-only impersonation) filters as the previewed user:
// history and search must show exactly what that user would see.
func SplitViewer(v *auth.Viewer) (userID string, isGM bool, owned, grants map[string]bool) {
	owned = map[string]bool{}
	grants = map[string]bool{}
	if v == nil {
		return "", false, owned, grants
	}
	userID, isGM = v.UserID, v.IsGM
	if v.PreviewAs != "" {
		userID, isGM = v.PreviewAs, false
	}
	for _, s := range v.OwnedSlugs {
		owned[strings.ToLower(s)] = true
	}
	for _, g := range v.Grants {
		grants[strings.ToLower(g)] = true
	}
	return userID, isGM, owned, grants
}

// PageVisible reports whether a page row may be shown to viewer v (nil = guest).
// secret:true pages are readable by GM + owner/editable-by only (AGENTS.md);
// non-secret pages are guest-readable. ownedSlugs/grants carry the viewer's
// path-scoped rights (nearest-ancestor inheritance applied by the caller).
func PageVisible(secret bool, pagePath, owner string, editableByJSON string, v *auth.Viewer) bool {
	if !secret {
		return true
	}
	userID, isGM, owned, grants := SplitViewer(v)
	if isGM {
		return true
	}
	if userID == "" {
		return false
	}
	if owner != "" && strings.EqualFold(owner, userID) {
		return true
	}
	var list []string
	_ = json.Unmarshal([]byte(editableByJSON), &list)
	for _, u := range list {
		if strings.EqualFold(u, userID) {
			return true
		}
	}
	// Path-scoped ownership/grants, matched case-insensitively.
	fold := strings.ToLower(pagePath)
	return owned[fold] || grants[fold]
}

// ChunkVisible reports whether a single block chunk may be shown. Secret
// blocks (`> [!secret]-`, default hidden) are visible to GM, the page owner,
// and `editable-by` holders (p03: `-` is "visible to GM + page owner /
// editable-by when hidden from party"; p03 wins over spec §4's "owner-visible"
// shorthand per the spec's own conflict rule). `+` (owner-visible default...
// i.e. non-secret) chunks follow the page.
// Identity-scoped only: unlike PageVisible there is deliberately NO
// owned-slugs/grants fallback — path-scoped rights confer page read, never
// hidden-block content. (Gate amend, Phase 2: the write-path editor serves raw
// source to editable-by holders, so denying `-` on reads was bypassable;
// widening is the only coherent model.)
func ChunkVisible(chunkSecret bool, owner, editableByJSON string, v *auth.Viewer) bool {
	if !chunkSecret {
		return true
	}
	userID, isGM, _, _ := SplitViewer(v)
	if isGM {
		return true
	}
	if userID == "" {
		return false
	}
	if owner != "" && strings.EqualFold(owner, userID) {
		return true
	}
	var list []string
	_ = json.Unmarshal([]byte(editableByJSON), &list)
	for _, u := range list {
		if strings.EqualFold(u, userID) {
			return true
		}
	}
	return false
}
