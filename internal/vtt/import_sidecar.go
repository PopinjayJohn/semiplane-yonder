// Sidecar surgical merge (amend A-VTT2 §4): the importer owns the vtt-map
// calibration keys and must never rewrite the rest of the page. Obsidian
// users hand-edit sidecars (comments, key order, prose) — a whole-file
// rewrite would revolt them (pitfalls: vault <-> DB consistency), and
// dropping hand-added sub-keys (token defaults!) on a re-import would lose
// GM setup.
//
// Merge policy, exactly:
//   - Every byte outside the vtt-map block is preserved verbatim (title,
//     owner, comments, blank lines, body).
//   - Inside vtt-map, importer-owned keys (importerOwnedKeys) are replaced
//     (entry = key line + its nested/sequence lines); every other sub-key
//     (tokens, fog-default, future keys) keeps its position and bytes.
//   - The importer's entries are appended at the end of the block.
//   - No vtt-map block yet: one is inserted at the end of the frontmatter.
//   - No frontmatter at all: a fresh fence (title + vtt-map) is prepended
//     and the original content becomes the body.
package vtt

import (
	"strings"
)

// applyVTTMapBlock surgically merges the importer entries into the page's
// vtt-map block. mapID is only used to title a freshly created sidecar.
// freshBody is the prose a brand-new sidecar gets (an existing page keeps
// its body byte-for-byte).
func applyVTTMapBlock(content, mapID string, entries []sidecarEntry, freshBody string) string {
	eol := "\n"
	if strings.Contains(content, "\r\n") {
		eol = "\r\n"
	}
	norm := strings.ReplaceAll(content, "\r\n", "\n")
	hadTrailingNL := strings.HasSuffix(norm, "\n")
	lines := strings.Split(strings.TrimSuffix(norm, "\n"), "\n")

	fmStart, fmEnd := frontmatterBounds(lines)
	if fmStart < 0 {
		// No frontmatter: create one. The original content becomes the body
		// (preserved byte-for-byte after the fence); an empty page gets the
		// generated prose instead.
		body := content
		if strings.TrimSpace(content) == "" {
			body = freshBody
		}
		var b strings.Builder
		b.WriteString("---" + eol)
		b.WriteString("title: " + quoteScalar(mapID) + eol)
		b.WriteString("vtt-map:" + eol)
		for _, ln := range renderBlock(nil, entries) {
			b.WriteString(ln + eol)
		}
		b.WriteString("---" + eol)
		if body != "" && !strings.HasSuffix(body, "\n") {
			body += eol
		}
		b.WriteString(body)
		return b.String()
	}
	// Locate the vtt-map key. A flow-style one-line value (`vtt-map: {…}`)
	// is dropped: it has no nested lines to preserve and calibration is
	// importer-owned, so the block is rebuilt below it.
	keyIdx := -1
	for i := fmStart + 1; i < fmEnd; i++ {
		if name, inline, ok := topLevelKey(lines[i]); ok && name == "vtt-map" {
			if strings.TrimSpace(inline) != "" {
				lines = append(lines[:i:i], lines[i+1:]...)
				fmEnd--
			} else {
				keyIdx = i
			}
			break
		}
	}
	var out []string
	if keyIdx < 0 {
		// No block yet: insert one just before the closing fence.
		out = append(out, lines[:fmEnd]...)
		out = append(out, "vtt-map:")
		out = append(out, renderBlock(nil, entries)...)
		out = append(out, lines[fmEnd:]...)
	} else {
		blockEnd := blockEndLine(lines, keyIdx, fmEnd)
		kept, blockStart := dropOwnedEntries(lines, keyIdx, blockEnd)
		// lines[:blockStart] already ends with the vtt-map key line.
		out = append(out, lines[:blockStart]...)
		out = append(out, renderBlock(kept, entries)...)
		out = append(out, lines[blockEnd:]...)
	}
	res := strings.Join(out, eol)
	if hadTrailingNL {
		res += eol
	}
	return res
}

// renderBlock renders the vtt-map block's CHILD entries: the preserved
// (kept) entries first in their original order, then the importer's. The
// `vtt-map:` key line itself is emitted by the caller (it already exists on
// disk when a block is present).
func renderBlock(kept []string, entries []sidecarEntry) []string {
	out := append([]string{}, kept...)
	for _, e := range entries {
		out = append(out, e.lines...)
	}
	return out
}

// frontmatterBounds returns the first line index and the closing fence index
// of the frontmatter, or (-1, -1) when the page has none. Input lines carry
// no endings.
func frontmatterBounds(lines []string) (int, int) {
	if len(lines) == 0 || strings.TrimRight(lines[0], " \t") != "---" {
		return -1, -1
	}
	for i := 1; i < len(lines); i++ {
		t := strings.TrimRight(lines[i], " \t")
		if t == "---" || t == "..." {
			return 0, i
		}
	}
	return -1, -1
}

// topLevelKey parses an unindented `key:` or `key: value` line.
func topLevelKey(line string) (name, inline string, ok bool) {
	if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
		return "", "", false
	}
	j := strings.IndexByte(line, ':')
	if j <= 0 {
		return "", "", false
	}
	return strings.TrimSpace(line[:j]), strings.TrimSpace(line[j+1:]), true
}

// blockEndLine returns the exclusive end of the block owned by the key at
// keyIdx: the first following top-level key (or the frontmatter close).
// Nested lines (any indent > 0, or block-sequence items at indent 0) stay in
// the block; a same-indent block sequence directly under the key is valid
// YAML (vtt-map is always a map, so children are indented, but the scanner
// stays tolerant of either shape).
func blockEndLine(lines []string, keyIdx, fmEnd int) int {
	for i := keyIdx + 1; i < fmEnd; i++ {
		ln := lines[i]
		if strings.TrimSpace(ln) == "" {
			continue // blank lines never terminate a block
		}
		indent := lineIndent(ln)
		if indent == 0 && !strings.HasPrefix(strings.TrimSpace(ln), "- ") && strings.TrimSpace(ln) != "-" {
			return i // next top-level key: block ended
		}
	}
	return fmEnd
}

func lineIndent(line string) int {
	n := 0
	for n < len(line) && line[n] == ' ' {
		n++
	}
	return n
}

// dropOwnedEntries removes importer-owned entries from the vtt-map block at
// [keyIdx, blockEnd): each owned key line plus its nested lines, and any
// directly-preceding comment lines at the same indent (so a stale
// "# map walls" comment never orphans). Returns the kept block lines
// (excluding the vtt-map key line itself) and the block start index (just
// after the key line).
func dropOwnedEntries(lines []string, keyIdx, blockEnd int) ([]string, int) {
	block := lines[keyIdx+1 : blockEnd]
	// Child indent: the first non-blank line's indent (default 2).
	childIndent := -1
	for _, ln := range block {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		childIndent = lineIndent(ln)
		break
	}
	if childIndent < 0 {
		return nil, keyIdx + 1
	}
	var kept []string
	for i := 0; i < len(block); i++ {
		ln := block[i]
		if strings.TrimSpace(ln) == "" {
			kept = append(kept, ln)
			continue
		}
		indent := lineIndent(ln)
		trimmed := strings.TrimSpace(ln)
		if indent == childIndent && !strings.HasPrefix(trimmed, "- ") && trimmed != "-" {
			if name, _, ok := splitEntryKey(ln); ok && isImporterOwned(name) {
				// Drop the entry: this line plus following lines that belong
				// to it (deeper indent, or a same-indent sequence item), plus
				// directly-preceding owned comments.
				kept = trimTrailingComments(kept, childIndent)
				i++
				for i < len(block) {
					nx := block[i]
					if strings.TrimSpace(nx) == "" {
						i++
						continue
					}
					nIndent := lineIndent(nx)
					nTrim := strings.TrimSpace(nx)
					if nIndent > childIndent || (nIndent == childIndent && (strings.HasPrefix(nTrim, "- ") || nTrim == "-")) {
						i++
						continue
					}
					i-- // this line starts the next entry
					break
				}
				continue
			}
		}
		kept = append(kept, ln)
	}
	return kept, keyIdx + 1
}

// trimTrailingComments removes trailing comment lines at the given indent
// from kept (a dropped entry's leading comment block).
func trimTrailingComments(kept []string, indent int) []string {
	for len(kept) > 0 {
		last := kept[len(kept)-1]
		if strings.TrimSpace(last) == "" || strings.HasPrefix(strings.TrimSpace(last), "#") &&
			lineIndent(last) == indent {
			kept = kept[:len(kept)-1]
			continue
		}
		break
	}
	return kept
}

// splitEntryKey parses `key: ...` / `key:` at any indent.
func splitEntryKey(line string) (string, string, bool) {
	j := strings.IndexByte(line, ':')
	if j <= 0 {
		return "", "", false
	}
	name := strings.TrimSpace(line[:j])
	if name == "" || strings.ContainsAny(name, " \t") {
		return "", "", false
	}
	return name, strings.TrimSpace(line[j+1:]), true
}

func isImporterOwned(key string) bool {
	for _, k := range importerOwnedKeys {
		if k == key {
			return true
		}
	}
	return false
}
