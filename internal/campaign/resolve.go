package campaign

import (
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
// directory it was read from. Descriptor versions are recorded, never
// enforced — a campaign pinning overlay-version "1.0" still resolves a pack
// stamped "1.1" (lint warns, resolution proceeds).
type Pack struct {
	ID      string // pack id, e.g. "dnd", "5e-2024"
	Name    string
	Kind    string // base | overlay | homebrew
	Version string // display-only
	Parent  string // overlay/homebrew: id of the pack they version/diff
	Dir     string // vault-relative posix dir, e.g. "rules/overlay/5e-2024"
}

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
// base/overlay packs, and unreadable descriptors are errors; version skew
// is not (recorded, never enforced).
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
