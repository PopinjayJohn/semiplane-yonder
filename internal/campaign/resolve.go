package campaign

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Pack kinds. Bases declare the system; overlays version a base; homebrew
// diffs either (never a fork — the resolver records the parent link and
// H2's engine composes base -> overlay -> homebrew).
const (
	PackBase     = "base"
	PackOverlay  = "overlay"
	PackHomebrew = "homebrew"
)

// Pack is one resolved ruleset pack: its descriptor plus the vault-relative
// directory it was read from. Pinned versions/hashes are enforced by
// Resolve (mismatch fails closed); empty pins float for legacy vaults.
// Hash is the descriptor-only sha256 (pack.yaml bytes, CRLF-normalized):
// content-prose drift is out of scope, revisit if compendium becomes
// executable. Homebrew hashes are audit-only (warn, never gate).
type Pack struct {
	ID      string // pack id, e.g. "dnd", "5e-2024"
	Name    string
	Kind    string // base | overlay | homebrew
	Version string // descriptor version
	Hash    string // descriptor hash (hex, lowercase)
	Parent  string // overlay/homebrew: id of the pack they version/diff
	Dir     string // vault-relative posix dir, e.g. "rules/overlay/5e-2024"
}

// ErrPinMismatch reports a campaign pin (version or hash) that does not
// match the resolved pack. Callers fail closed; empty pins never raise it.
var ErrPinMismatch = errors.New("campaign pin mismatch")

// Stack is the resolved layer order for a vault: exactly one base, zero or
// one overlay, zero or more homebrew diffs (sorted by dir for determinism).
type Stack struct {
	Base     *Pack
	Overlay  *Pack
	Homebrew []*Pack
}

// Resolve reads campaign.yaml and locates the referenced packs under
// rules/base/, rules/overlay/, plus every homebrew pack under homebrew/*
// and rules/homebrew/* (installed packs first, table-local homebrew/
// wins on duplicate ids). It reads live from disk on every call with no
// cache, so switching overlays is a campaign.yaml edit + re-resolve: no
// rebuild, no restart, no index touch. Unknown pack kinds, missing
// base/overlay packs, and unreadable descriptors are errors. Pinned
// base-version/base-hash and overlay-version/overlay-hash are enforced
// (mismatch wraps ErrPinMismatch, fail closed); empty pins float.
// Homebrew versions/hashes are recorded for audit only, never gated.
func Resolve(vaultRoot string) (*Stack, error) {
	c, err := Load(vaultRoot)
	if err != nil {
		return nil, err
	}
	st := &Stack{}
	if c.Base != "" {
		p, err := loadPack(vaultRoot, filepath.Join("rules", "base", c.Base))
		if err != nil {
			return nil, err
		}
		if p.Kind != PackBase {
			return nil, fmt.Errorf("rules/base/%s: wants kind %q, pack declares %q", c.Base, PackBase, p.Kind)
		}
		if p.ID != "" && p.ID != c.Base {
			return nil, fmt.Errorf("rules/base/%s: pack id %q does not match directory", c.Base, p.ID)
		}
		p.ID = c.Base
		if h, herr := HashPackDescriptor(vaultRoot, p.Dir); herr == nil {
			p.Hash = h
		}
		st.Base = p
	}
	if c.Overlay != "" {
		p, err := loadPack(vaultRoot, filepath.Join("rules", "overlay", c.Overlay))
		if err != nil {
			return nil, err
		}
		if p.Kind != PackOverlay {
			return nil, fmt.Errorf("rules/overlay/%s: wants kind %q, pack declares %q", c.Overlay, PackOverlay, p.Kind)
		}
		if p.ID != "" && p.ID != c.Overlay {
			return nil, fmt.Errorf("rules/overlay/%s: pack id %q does not match directory", c.Overlay, p.ID)
		}
		p.ID = c.Overlay
		if h, herr := HashPackDescriptor(vaultRoot, p.Dir); herr == nil {
			p.Hash = h
		}
		st.Overlay = p
	}
	seen := map[string]bool{}
	for _, hbRoot := range []string{"homebrew", filepath.Join("rules", "homebrew")} {
		entries, err := os.ReadDir(filepath.Join(vaultRoot, hbRoot))
		if err != nil {
			continue // either root may be absent
		}
		for _, e := range entries {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			p, err := loadPack(vaultRoot, filepath.Join(hbRoot, e.Name()))
			if err != nil {
				if os.IsNotExist(err) {
					continue // homebrew dir without a descriptor: not a pack
				}
				return nil, err
			}
			if p.Kind != PackHomebrew {
				return nil, fmt.Errorf("%s: wants kind %q, pack declares %q",
					filepath.ToSlash(filepath.Join(hbRoot, e.Name())), PackHomebrew, p.Kind)
			}
			if p.ID == "" {
				p.ID = e.Name()
			}
			if seen[p.ID] {
				continue // table-local homebrew/ shadows an installed pack
			}
			seen[p.ID] = true
			st.Homebrew = append(st.Homebrew, p)
		}
	}
	sort.Slice(st.Homebrew, func(i, j int) bool { return st.Homebrew[i].Dir < st.Homebrew[j].Dir })
	for _, p := range st.Homebrew {
		if h, err := HashPackDescriptor(vaultRoot, p.Dir); err == nil {
			p.Hash = h
		}
	}
	if err := checkPins(c, st); err != nil {
		return nil, err
	}
	return st, nil
}

// ParsePackDoc reads <vault>/<dir>/pack.yaml (accepting pack.yml) and
// returns the parsed descriptor document with source lines.
//
// Gate G3 export: the ruleset loader (internal/ruleset, H2) builds
// executable Rulesets from vault packs through this single parser — no
// second YAML reader (pitfalls: two parsers diverge). Identity accessors
// stay on loadPack/Resolve; this exposes the full data-rules tree.
func ParsePackDoc(vaultRoot, dir string) (map[string]Node, error) {
	var data []byte
	var err error
	for _, name := range []string{"pack.yaml", "pack.yml"} {
		data, err = os.ReadFile(filepath.Join(vaultRoot, dir, name))
		if err == nil {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	doc, err := parseDoc(string(data))
	if err != nil {
		return nil, fmt.Errorf("%s: %v", filepath.ToSlash(dir)+"/pack.yaml", err)
	}
	return doc, nil
}

// loadPack reads <vault>/<dir>/pack.yaml (accepting .yml) and returns the
// descriptor header. Full schema validation is ValidatePack's job (lint.go);
// the resolver reads only identity fields so a lint-failing pack still
// resolves (fail-open for iteration, lint gates the table decision).
func loadPack(vaultRoot, dir string) (*Pack, error) {
	doc, err := ParsePackDoc(vaultRoot, dir)
	if err != nil {
		return nil, err
	}
	p := &Pack{Dir: filepath.ToSlash(dir)}
	get := func(key string) string {
		if n, ok := doc[key]; ok {
			if s, ok := n.String(); ok {
				return s
			}
		}
		return ""
	}
	p.ID = get("id")
	p.Name = get("name")
	p.Kind = get("type")
	p.Version = get("version")
	p.Parent = get("parent")
	return p, nil
}

// HashDescriptor returns the lowercase hex sha256 of raw descriptor bytes,
// CRLF-normalized to LF so Windows and Unix checkouts hash identically.
func HashDescriptor(data []byte) string {
	norm := strings.ReplaceAll(string(data), "\r\n", "\n")
	sum := sha256.Sum256([]byte(norm))
	return hex.EncodeToString(sum[:])
}

// HashPackDescriptor hashes <vault>/<dir>/pack.yaml (accepting pack.yml).
func HashPackDescriptor(vaultRoot, dir string) (string, error) {
	var data []byte
	var err error
	for _, name := range []string{"pack.yaml", "pack.yml"} {
		data, err = os.ReadFile(filepath.Join(vaultRoot, filepath.FromSlash(dir), name))
		if err == nil {
			break
		}
	}
	if err != nil {
		return "", err
	}
	return HashDescriptor(data), nil
}

// checkPins enforces pinned versions/hashes for base and overlay. Empty
// pins float (legacy vaults). Homebrew is audit-only, never gated.
func checkPins(c *Campaign, st *Stack) error {
	if st.Base != nil {
		if c.BaseVersion != "" && st.Base.Version != c.BaseVersion {
			return fmt.Errorf("base version pin %q != pack %q: %w", c.BaseVersion, st.Base.Version, ErrPinMismatch)
		}
		if c.BaseHash != "" && !strings.EqualFold(st.Base.Hash, c.BaseHash) {
			return fmt.Errorf("base hash pin mismatch for %q: %w", st.Base.Dir, ErrPinMismatch)
		}
	}
	if st.Overlay != nil {
		if c.OverlayVersion != "" && st.Overlay.Version != c.OverlayVersion {
			return fmt.Errorf("overlay version pin %q != pack %q: %w", c.OverlayVersion, st.Overlay.Version, ErrPinMismatch)
		}
		if c.OverlayHash != "" && !strings.EqualFold(st.Overlay.Hash, c.OverlayHash) {
			return fmt.Errorf("overlay hash pin mismatch for %q: %w", st.Overlay.Dir, ErrPinMismatch)
		}
	}
	return nil
}
