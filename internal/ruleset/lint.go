package ruleset

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// LintIssue represents a lint finding. Severity is "error" (fails the
// gate) or "warn" (never blocks: e.g. likely non-SRD text).
type LintIssue struct {
	Severity string // "error", "warn"
	Path     string
	Message  string
}

// LintRuleset validates a ruleset pack in memory. Errors fail closed
// (unknown keys/functions/names); the likely-non-SRD heuristic only warns
// and never blocks — the GM owns table liability (P05).
//
// validateNotation optionally validates dice notations (the dice package
// provides it); when nil, only notation presence is checked. The parameter
// exists to avoid a ruleset↔dice import cycle — dice already depends on
// ruleset for Modifiers.
//
// Validated:
//   - identity: ID/Name non-empty; Type in {base, overlay, homebrew};
//     overlay/homebrew require ParentID.
//   - stats: keys are idents; Type in {int,float,string,dice,bool};
//     Min <= Max when both set.
//   - derived: formula parses with known functions only, within bounds;
//     every variable is a declared stat/derived key; Depends entries exist.
//   - dice: notation parses; within absolute transport maxima.
//   - intents: keys are idents; Stats/Dice refs exist; hook phases, kinds
//     and scripts valid (scripts see open context: only functions/parse/
//     bounds are checked, not variable names).
//   - optionals: IDs non-empty and unique within the pack.
func LintRuleset(rs *Ruleset, validateNotation func(string) error) []LintIssue {
	if rs == nil {
		return []LintIssue{{Severity: "error", Path: "ruleset", Message: "nil ruleset"}}
	}
	var issues []LintIssue
	errf := func(path, format string, args ...any) {
		issues = append(issues, LintIssue{
			Severity: "error",
			Path:     path,
			Message:  fmt.Sprintf(format, args...),
		})
	}
	if rs.ID == "" {
		errf("ruleset.id", "missing id")
	}
	if rs.Name == "" {
		errf("ruleset.name", "missing name")
	}
	switch rs.Type {
	case "base", "overlay", "homebrew":
	default:
		errf("ruleset.type", "unknown type %q (want base, overlay, or homebrew)", rs.Type)
	}
	if rs.Type != "base" && rs.ParentID == "" {
		errf("ruleset.parent", "type %q requires a parent id", rs.Type)
	}

	statKeys := map[string]bool{}
	for key, sd := range rs.StatDefs {
		if !isValidKey(key) {
			errf("stats."+key, "invalid stat key %q", key)
		}
		if key != sd.Key && sd.Key != "" {
			errf("stats."+key, "map key %q != def key %q", key, sd.Key)
		}
		switch sd.Type {
		case "int", "float", "string", "dice", "bool":
		default:
			errf("stats."+key+".type", "unknown stat type %q", sd.Type)
		}
		if sd.Min != nil && sd.Max != nil && *sd.Min > *sd.Max {
			errf("stats."+key, "min > max")
		}
		statKeys[key] = true
	}
	for key := range rs.DerivedDefs {
		if !isValidKey(key) {
			errf("derived."+key, "invalid derived key %q", key)
		}
		statKeys[key] = true // derived keys are visible to later formulas
	}
	// Second pass so forward references within derived work.
	for _, key := range sortedKeysOf(rs.DerivedDefs) {
		dd := rs.DerivedDefs[key]
		for _, dep := range dd.Depends {
			if !statKeys[dep] {
				errf("derived."+key+".depends", "unknown stat/derived %q", dep)
			}
		}
		if strings.TrimSpace(dd.Formula) == "" {
			errf("derived."+key+".formula", "missing formula")
			continue
		}
		for _, li := range LintExpression(dd.Formula, statKeys) {
			li.Path = "derived." + key + ".formula"
			issues = append(issues, li)
		}
	}
	for _, key := range sortedKeysOf(rs.DiceDefs) {
		dd := rs.DiceDefs[key]
		if !isValidKey(key) {
			errf("dice."+key, "invalid dice key %q", key)
		}
		if strings.TrimSpace(dd.Notation) == "" {
			errf("dice."+key+".notation", "missing notation")
			continue
		}
		if validateNotation != nil {
			if err := validateNotation(dd.Notation); err != nil {
				errf("dice."+key+".notation", "invalid notation: %v", err)
			}
		}
	}
	for _, key := range sortedKeysOf(rs.IntentDefs) {
		id := rs.IntentDefs[key]
		if !isValidKey(key) {
			errf("intents."+key, "invalid intent key %q", key)
		}
		for _, s := range id.Stats {
			if !statKeys[s] {
				errf("intents."+key+".stats", "unknown stat %q", s)
			}
		}
		for _, d := range id.Dice {
			if _, ok := rs.DiceDefs[d]; !ok {
				errf("intents."+key+".dice", "unknown dice %q", d)
			}
		}
		if strings.TrimSpace(id.Validates) != "" {
			for _, li := range LintExpression(id.Validates, nil) {
				li.Path = "intents." + key + ".validates"
				issues = append(issues, li)
			}
		}
		for i, h := range id.Hooks {
			hp := fmt.Sprintf("intents.%s.hooks[%d]", key, i)
			if !IsHookPhase(h.Phase) {
				errf(hp+".phase", "unknown phase %q", h.Phase)
			}
			kind := h.Kind
			if kind == "" {
				kind = ModBonus
			}
			if !IsModifierKind(kind) {
				errf(hp+".kind", "unknown modifier kind %q", h.Kind)
			}
			if strings.TrimSpace(h.Script) == "" {
				errf(hp+".script", "missing script")
				continue
			}
			for _, li := range LintExpression(h.Script, nil) {
				li.Path = hp + ".script"
				issues = append(issues, li)
			}
			if h.Reason == "" {
				issues = append(issues, LintIssue{
					Severity: "warn",
					Path:     hp + ".reason",
					Message:  "missing reason label (log attribution falls back to hook id)",
				})
			}
			if h.Requires != "" && !isValidFeatureID(h.Requires) {
				errf(hp+".requires", "invalid optional id %q", h.Requires)
			}
		}
	}
	seenOpt := map[string]bool{}
	for _, key := range sortedKeysOf(rs.Optionals) {
		of := rs.Optionals[key]
		if of.ID == "" || !isValidFeatureID(of.ID) {
			errf("optionals."+key, "invalid optional id %q", of.ID)
		}
		if seenOpt[of.ID] {
			errf("optionals."+key, "duplicate optional id %q", of.ID)
		}
		seenOpt[of.ID] = true
		for _, r := range of.Requires {
			if r != "" && !hasOptionalID(rs.Optionals, r) {
				errf("optionals."+key+".requires", "unknown optional %q", r)
			}
		}
	}
	issues = append(issues, warnNonSRD(rs)...)
	sort.Slice(issues, func(i, j int) bool {
		if issues[i].Path != issues[j].Path {
			return issues[i].Path < issues[j].Path
		}
		return issues[i].Message < issues[j].Message
	})
	return issues
}

func hasOptionalID(m map[string]OptionalFeature, id string) bool {
	for _, of := range m {
		if of.ID == id {
			return true
		}
	}
	return false
}

// isValidKey validates data keys (stat, derived, dice, intent names).
// Keys are map-lookup data, never expression variables, so they allow the
// hyphenated P05 vocabulary (rest-short, con-mod, second-wind) that the
// expression grammar cannot lex: a formula or script can never *reference* a
// hyphenated key (it lexes as subtraction), but packs may declare and carry
// such keys. Expression variables keep the stricter isValidIdent.
func isValidKey(s string) bool {
	return isValidFeatureID(s)
}

// isValidFeatureID validates optional-feature ids: letters, digits,
// underscore, hyphen (path-slug style per P05 `optional:id=...`).
// Expression variables use the stricter isValidIdent.
func isValidFeatureID(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_' || c == '-' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z'):
		case c >= '0' && c <= '9' && i > 0:
		default:
			return false
		}
	}
	return true
}

func sortedKeysOf[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// LintErrors returns only error-severity issues.
func LintErrors(issues []LintIssue) []LintIssue {
	var out []LintIssue
	for _, li := range issues {
		if li.Severity == "error" {
			out = append(out, li)
		}
	}
	return out
}

// nonSRDMarkers is a small heuristic list of Wizards product-identity names
// that cannot appear in SRD-derived text. Matching warns; it never blocks.
var nonSRDMarkers = []string{
	"vecna", "mordenkainen", "tasha", "bigby", "otiluke", "tensor",
	"melf", "nystul", "agalath", "faerun", "eberron", "mind flayer",
	"beholder", "githyanki", "githzerai", "slaad", "yuan-ti",
}

// warnNonSRD scans display-text fields for likely non-SRD names.
func warnNonSRD(rs *Ruleset) []LintIssue {
	var texts []string
	texts = append(texts, rs.Name)
	for _, id := range rs.IntentDefs {
		texts = append(texts, id.Name)
	}
	for _, of := range rs.Optionals {
		texts = append(texts, of.Name, of.Description)
	}
	joined := strings.ToLower(strings.Join(texts, "\n"))
	var issues []LintIssue
	for _, m := range nonSRDMarkers {
		if strings.Contains(joined, m) {
			issues = append(issues, LintIssue{
				Severity: "warn",
				Path:     "ruleset.text",
				Message:  fmt.Sprintf("possible non-SRD text %q (GM owns table liability; not blocking)", m),
			})
		}
	}
	return issues
}

// LintRulesetJSON strictly decodes one JSON pack object (unknown struct
// fields fail) and lints it. It gives the `rules lint` CLI a semantic gate
// for JSON packs; YAML packs decode through H1's loader into Ruleset and
// call LintRuleset. Map keys (stat names, intent names) are data, not
// struct fields, so they are validated by LintRuleset, not here.
func LintRulesetJSON(data []byte, validateNotation func(string) error) ([]LintIssue, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, fmt.Errorf("rules lint: empty pack")
	}
	var rs Ruleset
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rs); err != nil {
		return nil, fmt.Errorf("rules lint: %w", err)
	}
	return LintRuleset(&rs, validateNotation), nil
}
