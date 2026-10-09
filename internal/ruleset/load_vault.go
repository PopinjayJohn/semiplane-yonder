package ruleset

// Vault pack loader (gate G3 bridge, H1+H2 seam).
//
// H1 owns vault packs as files (rules/base/*, rules/overlay/*,
// rules/homebrew/* + homebrew/*) plus campaign.yaml validation; H2 owns the
// executable Ruleset shape and the engine. No lane built the file→struct
// bridge (H1's parser stops at lint issues; H2's LintRulesetJSON covers JSON
// packs only — its doc anticipates "YAML packs decode through H1's loader").
// This file is that loader: pack.yaml decodes through H1's single parser
// (campaign.ParsePackDoc — never a second YAML reader), then converts the
// data-rules tree into Rulesets the engine executes.
//
// Fidelity rules (pinned by load_vault_test.go on fixtures/demo/rules):
//   - stats / derived (real evaluator expressions) / rolls (dice notations)
//     / intents / optionals convert verbatim.
//   - Hooks convert with label→Reason and effect→Script, but ONLY when the
//     effect parses as an evaluator expression (the same LintExpression
//     check H2's lint applies, so lint-green and load-green never
//     disagree). H1's symbolic effects (add-1d4-once-per-turn) stay display
//     data in compendium prose — H1 shape-lint still passes them — while
//     parseable-but-symbolic effects (half-cover-plus-2) load and fail
//     CLOSED at evaluation (unknown variable) instead of silently rolling
//     wrong numbers. Aligning H1's effect vocabulary with executable
//     scripts is an explicit H1+H2 follow-up (see gate report); the timebox
//     rest intents carry no hooks, so sheet rest/level flows evaluate clean
//     either way.
//   - Hook kind defaults to bonus (mirrors lint); Requires is always empty
//     (H1's closed hook set has no requires key yet).
//
// LoadVaultStack resolves campaign.yaml through H1 on EVERY call and reads
// live from disk with no cache: switching overlays is a campaign.yaml edit,
// no rebuild, no restart, no index touch. Mid-session toggles affect future
// evaluations only (engine rule; history pins its modifier list).

import (
	"fmt"
	"log/slog"
	"sort"

	"github.com/semiplane/yonder/internal/campaign"
)

// LoadVaultPack builds an executable Ruleset from one H1 vault pack
// directory (vault-relative posix, e.g. "rules/base/dnd"). Structural
// problems (unreadable descriptor, bad shapes) are errors; semantic
// problems surface through LintRuleset (the `rules lint` CLI runs both).
func LoadVaultPack(vaultRoot, packDir string) (*Ruleset, error) {
	doc, err := campaign.ParsePackDoc(vaultRoot, packDir)
	if err != nil {
		return nil, err
	}
	rs := &Ruleset{
		StatDefs:    map[string]StatDef{},
		DerivedDefs: map[string]DerivedDef{},
		DiceDefs:    map[string]DiceDef{},
		IntentDefs:  map[string]IntentDef{},
		Optionals:   map[string]OptionalFeature{},
	}
	str := func(m map[string]campaign.Node, key string) string {
		if n, ok := m[key]; ok {
			if s, ok := n.String(); ok {
				return s
			}
		}
		return ""
	}
	rs.ID = str(doc, "id")
	rs.Name = str(doc, "name")
	rs.Version = str(doc, "version")
	rs.Type = str(doc, "type")
	rs.ParentID = str(doc, "parent")

	if n, ok := doc["stats"]; ok {
		m, ok := n.Map()
		if !ok {
			return nil, fmt.Errorf("%s: stats wants a map", packDir)
		}
		for _, key := range sortedDocKeys(m) {
			em, ok := m[key].Map()
			if !ok {
				return nil, fmt.Errorf("%s: stat %q wants a map", packDir, key)
			}
			sd := StatDef{Key: key, Name: str(em, "label"), Type: str(em, "type")}
			if sd.Type == "" {
				sd.Type = "int"
			}
			if v, ok := em["min"]; ok {
				if f, ok := numVal(v); ok {
					f := f
					sd.Min = &f
				}
			}
			if v, ok := em["max"]; ok {
				if f, ok := numVal(v); ok {
					f := f
					sd.Max = &f
				}
			}
			if v, ok := em["default"]; ok {
				sd.Default = scalarVal(v)
			}
			rs.StatDefs[key] = sd
		}
	}
	if n, ok := doc["derived"]; ok {
		m, ok := n.Map()
		if !ok {
			return nil, fmt.Errorf("%s: derived wants a map", packDir)
		}
		for _, key := range sortedDocKeys(m) {
			em, ok := m[key].Map()
			if !ok {
				return nil, fmt.Errorf("%s: derived %q wants a map", packDir, key)
			}
			dd := DerivedDef{Key: key, Name: str(em, "label"), Formula: str(em, "formula")}
			if d, ok := em["depends"]; ok {
				if l, ok := d.StringList(); ok {
					dd.Depends = l
				}
			}
			rs.DerivedDefs[key] = dd
		}
	}
	if n, ok := doc["rolls"]; ok {
		m, ok := n.Map()
		if !ok {
			return nil, fmt.Errorf("%s: rolls wants a map", packDir)
		}
		for _, key := range sortedDocKeys(m) {
			em, ok := m[key].Map()
			if !ok {
				return nil, fmt.Errorf("%s: roll %q wants a map", packDir, key)
			}
			rs.DiceDefs[key] = DiceDef{Key: key, Name: str(em, "label"), Notation: str(em, "dice")}
		}
	}
	// Intents take two shapes: the base maps names to {label, stats, ...},
	// overlays extend the catalog with a bare name list (p05 layers).
	if n, ok := doc["intents"]; ok {
		if m, ok := n.Map(); ok {
			for _, key := range sortedDocKeys(m) {
				id := IntentDef{Key: key}
				if em, ok := m[key].Map(); ok {
					id.Name = str(em, "label")
					if s, ok := em["stats"]; ok {
						if l, ok := s.StringList(); ok {
							id.Stats = l
						}
					}
					if d, ok := em["dice"]; ok {
						if l, ok := d.StringList(); ok {
							id.Dice = l
						}
					}
					id.Validates = str(em, "validates")
				} else if s, ok := m[key].String(); ok && s != "" {
					id.Name = s
				}
				rs.IntentDefs[key] = id
			}
		} else if l, ok := n.StringList(); ok {
			for _, name := range l {
				if name == "" {
					continue
				}
				rs.IntentDefs[name] = IntentDef{Key: name}
			}
		} else {
			return nil, fmt.Errorf("%s: intents wants a map or a name list", packDir)
		}
	}
	if n, ok := doc["hooks"]; ok {
		l, ok := n.List()
		if !ok {
			return nil, fmt.Errorf("%s: hooks wants a list", packDir)
		}
		byIntent := map[string][]HookDef{}
		for i, item := range l {
			em, ok := seqMap(item)
			if !ok {
				return nil, fmt.Errorf("%s: hook[%d] wants a map", packDir, i)
			}
			intent := str(em, "intent")
			if intent == "" {
				return nil, fmt.Errorf("%s: hook[%d] requires an intent", packDir, i)
			}
			// Executability gate: the engine only runs evaluator
			// expressions, so an effect outside the expression grammar
			// (H1's symbolic vocabulary, e.g. add-1d4-once-per-turn) is
			// left OUT of the executable set — the SAME parse check H2's
			// lint applies, so lint-green and load-green never disagree.
			// The hook stays display data in compendium prose (H1
			// shape-lint still passes it); aligning the effect vocabulary
			// with executable scripts is an H1+H2 follow-up. Parseable
			// but symbolic effects (half-cover-plus-2) load and fail
			// CLOSED at evaluation (unknown variable) rather than rolling
			// wrong numbers.
			script := str(em, "effect")
			if len(LintExpression(script, nil)) > 0 {
				continue
			}
			h := HookDef{
				Phase:  str(em, "phase"),
				Script: script,
				Kind:   ModBonus,
				Reason: str(em, "label"),
			}
			byIntent[intent] = append(byIntent[intent], h)
		}
		for intent, hs := range byIntent {
			id := rs.IntentDefs[intent]
			id.Key = intent
			id.Hooks = append(id.Hooks, hs...)
			rs.IntentDefs[intent] = id
		}
	}
	if n, ok := doc["optionals"]; ok {
		l, ok := n.List()
		if !ok {
			return nil, fmt.Errorf("%s: optionals wants a list", packDir)
		}
		for i, item := range l {
			em, ok := seqMap(item)
			if !ok {
				return nil, fmt.Errorf("%s: optional[%d] wants a map", packDir, i)
			}
			id := str(em, "id")
			if id == "" {
				return nil, fmt.Errorf("%s: optional[%d] requires an id", packDir, i)
			}
			rs.Optionals[id] = OptionalFeature{
				ID:   id,
				Name: str(em, "title"),
			}
		}
	}
	return rs, nil
}

// LoadVaultStack resolves the vault's campaign through H1 and loads the
// base→overlay→homebrew stack into a ready Engine, then enables the
// campaign's enabled-features. Homebrew diffing an inactive overlay is
// skipped (loud log); any other structural failure (missing campaign,
// unresolvable packs, lint errors, unknown enabled ids) is an error —
// callers fall back to empty modifiers with a loud log so sheets keep
// working (marked fallback; availability over rules precision).
func LoadVaultStack(vaultRoot string) (*Engine, error) {
	st, err := campaign.Resolve(vaultRoot)
	if err != nil {
		return nil, fmt.Errorf("ruleset: resolve: %w", err)
	}
	if st.Base == nil {
		return nil, fmt.Errorf("ruleset: no base pack resolved (campaign.yaml base unset?)")
	}
	e := NewEngine()
	load := func(p *campaign.Pack) (*Ruleset, error) {
		rs, err := LoadVaultPack(vaultRoot, p.Dir)
		if err != nil {
			return nil, err
		}
		// Resolver identity wins over descriptor drift (resolve.go already
		// enforces id==dir; the executable struct carries the same).
		rs.ID = p.ID
		return rs, nil
	}
	base, err := load(st.Base)
	if err != nil {
		return nil, err
	}
	if err := e.LoadBase(base); err != nil {
		return nil, err
	}
	if st.Overlay != nil {
		ov, err := load(st.Overlay)
		if err != nil {
			return nil, err
		}
		if err := e.LoadOverlay(ov); err != nil {
			return nil, err
		}
	}
	for _, hp := range st.Homebrew {
		// Parentage pre-check (mirrors Engine.LoadHomebrew): a homebrew
		// diffing an inactive overlay (grit targets 5e-2024 while the table
		// runs 5e-2014) is SKIPPED with a loud log, never applied to the
		// wrong base — and it never sinks the valid base→overlay prefix
		// (resolver is fail-open by lane design; enabling the skipped
		// pack's optionals still fails loudly below).
		if st.Overlay != nil {
			if hp.Parent != "" && hp.Parent != st.Overlay.ID && hp.Parent != st.Base.ID {
				slog.Warn("ruleset: skipping homebrew for another overlay", "pack", hp.ID, "parent", hp.Parent, "overlay", st.Overlay.ID)
				continue
			}
		} else if hp.Parent != "" && hp.Parent != st.Base.ID {
			slog.Warn("ruleset: skipping homebrew for another base", "pack", hp.ID, "parent", hp.Parent, "base", st.Base.ID)
			continue
		}
		hb, err := load(hp)
		if err != nil {
			return nil, err
		}
		if err := e.LoadHomebrew(hb); err != nil {
			return nil, err
		}
	}
	c, err := campaign.Load(vaultRoot)
	if err != nil {
		return nil, fmt.Errorf("ruleset: campaign: %w", err)
	}
	for _, id := range c.EnabledFeatures {
		if err := e.EnableFeature(id); err != nil {
			return nil, fmt.Errorf("ruleset: enable %q: %w", id, err)
		}
	}
	return e, nil
}

func sortedDocKeys(m map[string]campaign.Node) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// seqMap coerces one sequence item to a Node map. H1's parser yields
// map[string]Node for `- key: value` items and bare scalars otherwise.
func seqMap(v any) (map[string]campaign.Node, bool) {
	switch t := v.(type) {
	case map[string]campaign.Node:
		return t, true
	case campaign.Node:
		return t.Map()
	}
	return nil, false
}

// numVal coerces a descriptor scalar to float64 (stat min/max bounds).
func numVal(n campaign.Node) (float64, bool) {
	switch v := n.Value.(type) {
	case int64:
		return float64(v), true
	case float64:
		return v, true
	case int:
		return float64(v), true
	}
	return 0, false
}

// scalarVal carries a descriptor scalar into StatDef.Default untouched.
func scalarVal(n campaign.Node) any {
	switch v := n.Value.(type) {
	case string, int64, float64, bool:
		return v
	case int:
		return int64(v)
	}
	return nil
}
