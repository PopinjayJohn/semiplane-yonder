package campaign

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// SetEnabled validates ids against the vault's scanned optionals and writes
// them to campaign.yaml enabled-features. Unknown ids fail (typos must not
// silently enable nothing); an empty list clears the key. Callers enforce
// GM-only access — this function is the file mechanics, not the auth gate.
//
// The edit is surgical via Upsert (the one campaign.yaml writer): comment
// lines, key order, and unrelated values are preserved byte-for-byte
// (unchanged scalar lines keep even their trailing comments); only the
// enabled-features item lines are replaced (or the key appended when
// absent), in the caller's order. The file is replaced atomically
// (temp + rename) and read back through Parse before returning.
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
	cur, err := Parse(raw)
	if err != nil {
		return fmt.Errorf("read campaign.yaml: %w", err)
	}
	cur.EnabledFeatures = want
	updated := Upsert(string(raw), cur)
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
