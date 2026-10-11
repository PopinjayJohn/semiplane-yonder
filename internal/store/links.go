package store

import (
	"strings"
)

// NormalizeLinkTarget canonicalizes a raw wikilink/embed target to the
// page-path form stored in links.target (W1).
//
// Steps (in order):
//   - trim surrounding whitespace;
//   - strip repeated leading "./" prefixes (re-trimming after each);
//   - cut any #fragment suffix;
//   - drop the edge entirely ("") when nothing addressable remains
//     (blank or fragment-only targets such as `[[#frag]]`);
//   - keep targets that already carry an extension as-is
//     (`[[lore.markdown]]`, `![[pic.png]]`);
//   - otherwise default extensionless wikilink bases to `.md`
//     (`[[cinder-pact]]` → `cinder-pact.md`).
//
// The base-trimming half intentionally mirrors web.normalizeLinkBase (the
// read-path helper shared by resolveTarget/resolveGraphTarget): store cannot
// import web (web imports store), so the two copies must stay in sync — a
// target normalized here must resolve through the same base there. Case is
// preserved (page-ID lookup is case-insensitive elsewhere); subdirectory
// separators are left untouched (no path.Clean: interior `.`/`..` segments
// resolve — or fail closed — identically on both sides).
//
// Fail-closed: unresolvable targets (dangling pages, assets, dropped empties)
// never match a page row, so the graph/backlink readers that consume this
// column simply emit no edge. Secret filtering stays entirely read-side:
// normalization never consults page ACL.
func NormalizeLinkTarget(target string) string {
	base := strings.TrimSpace(target)
	for strings.HasPrefix(base, "./") {
		base = strings.TrimSpace(strings.TrimPrefix(base, "./"))
	}
	if i := strings.LastIndex(base, "#"); i >= 0 {
		base = base[:i]
	}
	base = strings.TrimSpace(base)
	if base == "" {
		return ""
	}
	if hasLinkExtension(base) {
		return base
	}
	return base + ".md"
}

// hasLinkExtension reports whether the final path segment carries any `.`
// suffix. Extensionless wikilink bases gain the default `.md`; anything
// already suffixed (page extensions, asset names) keeps its form so the
// stored shape stays honest — the handler's extension-candidate fallback
// still resolves dotted page names (`[[my.page]]` → `my.page.md`).
func hasLinkExtension(base string) bool {
	seg := base
	if i := strings.LastIndex(seg, "/"); i >= 0 {
		seg = seg[i+1:]
	}
	return strings.Contains(seg, ".")
}
