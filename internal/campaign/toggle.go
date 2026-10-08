package campaign

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// SetEnabled validates ids against the vault's scanned optionals and writes
// them to campaign.yaml enabled-features. Unknown ids fail (typos must not
// silently enable nothing); an empty list clears the key. Callers enforce
// GM-only access — this function is the file mechanics, not the auth gate.
//
// The edit is surgical: comment lines, key order, and unrelated values are
// preserved byte-for-byte; only the enabled-features item lines are
// replaced (or the key appended when absent). The file is replaced
// atomically (temp + rename) and read back through Parse before returning.
func SetEnabled(vaultRoot string, ids []string) error {
	known, err := Scan(vaultRoot)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, o := range known {
		have[o.ID] = true
	}
	seen := map[string]bool{}
	want := make([]string, 0, len(ids))
	for _, id := range ids {
		if seen[id] {
			continue
		}
		seen[id] = true
		if !have[id] {
			return fmt.Errorf("unknown optional id %q (not offered by any vault file)", id)
		}
		want = append(want, id)
	}
	path := filepath.Join(vaultRoot, "campaign.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read campaign.yaml: %w", err)
	}
	updated := rewriteListKey(string(raw), "enabled-features", want)
	tmp, err := os.CreateTemp(vaultRoot, ".campaign.yaml.*.tmp")
	if err != nil {
		return fmt.Errorf("write campaign.yaml: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(updated); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("write campaign.yaml: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("write campaign.yaml: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("write campaign.yaml: %w", err)
	}
	back, err := Load(vaultRoot)
	if err != nil {
		return fmt.Errorf("re-read campaign.yaml: %w", err)
	}
	if len(back.EnabledFeatures) != len(want) {
		return fmt.Errorf("re-read campaign.yaml: enabled-features mismatch")
	}
	for i := range want {
		if back.EnabledFeatures[i] != want[i] {
			return fmt.Errorf("re-read campaign.yaml: enabled-features mismatch")
		}
	}
	return nil
}

// rewriteListKey replaces the block-sequence value of key (or appends the
// key when absent). Item lines belonging to the old list — at deeper indent
// than the key, or `- ` items at the same indent (the Obsidian shape) — are
// dropped; everything else, including comments and blank lines, is kept.
// New items render as `key:` + two-space `- id` lines appended in place.
func rewriteListKey(content, key string, ids []string) string {
	eol := "\n"
	if strings.Contains(content, "\r\n") {
		eol = "\r\n"
	}
	norm := strings.ReplaceAll(content, "\r\n", "\n")
	lines := strings.Split(norm, "\n")
	keyIndent := -1
	keyIdx := -1
	for i, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		indent := 0
		for indent < len(ln) && ln[indent] == ' ' {
			indent++
		}
		k, _, err := splitKey(strings.TrimSpace(ln[indent:]))
		if err != nil {
			continue
		}
		if k == key && keyIdx < 0 {
			keyIdx = i
			keyIndent = indent
			break
		}
	}
	items := make([]string, 0, len(ids))
	pad := strings.Repeat(" ", 2)
	if keyIdx >= 0 {
		pad = strings.Repeat(" ", keyIndent+2)
	}
	for _, id := range ids {
		items = append(items, pad+"- "+id)
	}
	var out []string
	if keyIdx < 0 {
		out = append([]string{}, lines...)
		// Trim trailing blank lines, then append the new key block.
		for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
			out = out[:len(out)-1]
		}
		out = append(out, key+":")
		out = append(out, items...)
		return strings.Join(out, eol) + eol
	}
	out = append([]string{}, lines[:keyIdx+1]...)
	// Preserve an inline/flow value if the key already has one (`key: []`
	// or `key: [a]`): an empty flow list is replaced wholesale below.
	if k, v, err := splitKey(strings.TrimSpace(lines[keyIdx][keyIndent:])); err == nil && k == key && v != "" {
		if strings.TrimSpace(v) != "[]" {
			// Non-empty inline value: leave the line alone and append
			// nothing — Parse will reject the mixed shape on re-read
			// rather than silently dropping the author's value.
			return strings.Join(append(out, lines[keyIdx+1:]...), eol)
		}
		out[len(out)-1] = strings.Repeat(" ", keyIndent) + key + ":"
	}
	i := keyIdx + 1
	for i < len(lines) {
		ln := lines[i]
		trimmed := strings.TrimSpace(ln)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			break
		}
		indent := 0
		for indent < len(ln) && ln[indent] == ' ' {
			indent++
		}
		isItem := strings.HasPrefix(trimmed, "- ") || trimmed == "-"
		if isItem && indent >= keyIndent {
			i++
			continue
		}
		break
	}
	out = append(out, items...)
	out = append(out, lines[i:]...)
	joined := strings.Join(out, eol)
	if !strings.HasSuffix(joined, eol) {
		joined += eol
	}
	return joined
}

// SortedIDs returns the vault's optional ids in deterministic order (GM
// picker input; I1 consumes this).
func SortedIDs(vaultRoot string) ([]string, error) {
	opts, err := Scan(vaultRoot)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(opts))
	for _, o := range opts {
		ids = append(ids, o.ID)
	}
	sort.Strings(ids)
	return ids, nil
}
