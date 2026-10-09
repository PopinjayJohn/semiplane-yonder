# `fixtures/srd/` — user-supplied SRD snapshot (dev fixture, never embedded)

Drop your D&D 5e SRD Obsidian markdown snapshot here (this folder, `*.md`).
Nothing here ships in the binary; it exists so tests and the H1 timebox have
real-world markdown to chew on.

## What's needed, in priority order
1. **H1 timebox (Phase 3, next consumer):** Fighter + Goblin + 1 spell, enough
   to build playable sheets (stats, derived values, one roll each).
2. **Lane A leftovers (parser edge cases):** nested `> [!secret]` callouts,
   `[[wikilink]]` in a callout title, CRLF files, invalid YAML/frontmatter
   types (must quarantine, never crash). Synthetic goldens in
   `testdata/markdown/` already cover the basics — real excerpts just widen them.

## Rules
- SRD 5.2 only (CC-BY-4.0). No full Monster Manual / non-SRD spells — those are
  GM-imported homebrew, never fixtures. Keep the attribution note if your
  snapshot has one.
- Obsidian-safe: files stay as authored; the parser never rewrites source.
- Delete this README when done, or keep it — your call.
