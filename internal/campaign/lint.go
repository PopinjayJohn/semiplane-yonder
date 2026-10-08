package campaign

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Issue is one lint finding. Errors fail `rules lint`; warnings never block
// (the GM owns table liability — pitfalls). File is vault-relative posix.
type Issue struct {
	Severity string // "error" | "warn"
	File     string
	Line     int
	Message  string
}

// ValidatePack runs H1's structure checks over one pack directory
// (<vault>/<packDir> holding pack.yaml/pack.yml): required identity keys,
// closed key sets (unknown keys fail), overlay/homebrew parent rules,
// content-file existence, pack-local optional-id uniqueness, and hook
// phase/intent shape. Expression functions inside formulas are NOT checked
// here — the evaluator is H2's, and unknown functions fail at H2's lint.
//
// Warnings (never errors): missing SRD attribution on base/overlay packs
// and likely Product-Identity terms in content markdown.
func ValidatePack(vaultRoot, packDir string) ([]Issue, error) {
	var issues []Issue
	errf := func(line int, msg string) {
		issues = append(issues, Issue{Severity: "error", File: filepath.ToSlash(filepath.Join(packDir, "pack.yaml")), Line: line, Message: msg})
	}
	warnf := func(file string, line int, msg string) {
		issues = append(issues, Issue{Severity: "warn", File: file, Line: line, Message: msg})
	}
	dir := filepath.Join(vaultRoot, packDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("pack %s: %w", packDir, err)
	}
	descName := ""
	for _, cand := range []string{"pack.yaml", "pack.yml"} {
		for _, e := range entries {
			if !e.IsDir() && e.Name() == cand {
				descName = cand
			}
		}
	}
	if descName == "" {
		return nil, fmt.Errorf("pack %s: missing pack.yaml", packDir)
	}
	descFile := filepath.ToSlash(filepath.Join(packDir, descName))
	raw, err := os.ReadFile(filepath.Join(dir, descName))
	if err != nil {
		return nil, fmt.Errorf("pack %s: %w", packDir, err)
	}
	doc, err := parseDoc(string(raw))
	if err != nil {
		errf(1, fmt.Sprintf("bad descriptor: %v", err))
		return issues, nil
	}
	allowedTop := map[string]bool{
		"id": true, "name": true, "type": true, "version": true,
		"parent": true, "attribution": true, "stats": true,
		"derived": true, "rolls": true, "dice": true, "identity": true,
		"intents": true, "hooks": true, "sheet": true, "content": true,
		"optionals": true,
	}
	keys := make([]string, 0, len(doc))
	for k := range doc {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if !allowedTop[k] {
			errf(doc[k].Line, fmt.Sprintf("unknown key %q", k))
		}
	}
	str := func(key string) (string, int, bool) {
		n, ok := doc[key]
		if !ok || n.Value == nil {
			return "", 0, false
		}
		s, ok := n.String()
		if !ok {
			errf(n.Line, fmt.Sprintf("%q wants a string", key))
			return "", n.Line, false
		}
		return s, n.Line, true
	}
	id, idLine, hasID := str("id")
	if !hasID {
		errf(1, `missing required key "id"`)
	} else if base := filepath.Base(filepath.ToSlash(packDir)); id != base {
		errf(idLine, fmt.Sprintf("id %q does not match directory %q", id, base))
	}
	if _, _, ok := str("name"); !ok {
		errf(1, `missing required key "name"`)
	}
	kind, kindLine, hasKind := str("type")
	if !hasKind {
		errf(1, `missing required key "type" (want base|overlay|homebrew)`)
	} else if kind != PackBase && kind != PackOverlay && kind != PackHomebrew {
		errf(kindLine, fmt.Sprintf("type %q wants base|overlay|homebrew", kind))
	}
	if _, _, ok := str("version"); !ok {
		errf(1, `missing required key "version" (display-only, never enforced)`)
	}
	parent, parentLine, hasParent := str("parent")
	if hasKind {
		switch kind {
		case PackBase:
			if hasParent {
				errf(parentLine, "base packs must not declare a parent")
			}
		case PackOverlay, PackHomebrew:
			if !hasParent {
				errf(1, fmt.Sprintf("%s packs require a parent id", kind))
			}
		}
	}
	if _, _, ok := str("attribution"); !ok && (kind == PackBase || kind == PackOverlay) {
		warnf(descFile, 1, "missing attribution (SRD-derived packs carry CC-BY-4.0 credit)")
	}
	if n, ok := doc["stats"]; ok && n.Value != nil {
		checkDefMap(n, map[string]bool{"label": true, "type": true, "min": true, "max": true, "default": true, "secret": true, "editable": true}, descFile, &issues)
	}
	if n, ok := doc["derived"]; ok && n.Value != nil {
		m, ok := n.Map()
		if !ok {
			errf(n.Line, `"derived" wants a map of name -> {formula, ...}`)
		} else {
			for _, name := range sortedKeys(m) {
				en := m[name]
				em, ok := en.Map()
				if !ok {
					errf(en.Line, fmt.Sprintf("derived %q wants a map", name))
					continue
				}
				closedKeys(descFile, em, map[string]bool{"label": true, "formula": true, "depends": true, "secret": true}, "derived "+name, &issues)
				if f, ok := em["formula"]; !ok || f.Value == nil {
					errf(en.Line, fmt.Sprintf("derived %q requires a formula (executed by H2's engine)", name))
				} else if _, ok := f.String(); !ok {
					errf(f.Line, fmt.Sprintf("derived %q formula wants a string", name))
				}
			}
		}
	}
	if n, ok := doc["rolls"]; ok && n.Value != nil {
		checkDefMap(n, map[string]bool{"label": true, "dice": true, "stat": true, "intent": true}, descFile, &issues)
	}
	if n, ok := doc["dice"]; ok && n.Value != nil {
		m, ok := n.Map()
		if !ok {
			errf(n.Line, `"dice" wants a map`)
		} else {
			closedKeys(descFile, m, map[string]bool{"types": true, "faces": true, "symbols": true, "grammar": true, "renderer": true}, "dice", &issues)
		}
	}
	if n, ok := doc["identity"]; ok && n.Value != nil {
		m, ok := n.Map()
		if !ok {
			errf(n.Line, `"identity" wants a map`)
		} else {
			closedKeys(descFile, m, map[string]bool{"portrait": true, "pronouns": true, "voice": true, "notes": true, "inspiration": true, "death-saves": true}, "identity", &issues)
		}
	}
	intentNames := map[string]bool{}
	if n, ok := doc["intents"]; ok && n.Value != nil {
		if l, ok := n.List(); ok {
			for _, e := range l {
				if s, ok := scalarToString(e); ok {
					intentNames[s] = true
				} else {
					errf(n.Line, `"intents" list wants intent names`)
				}
			}
		} else if m, ok := n.Map(); ok {
			for _, name := range sortedKeys(m) {
				intentNames[name] = true
				if en := m[name]; en.Value != nil {
					if _, ok := en.Map(); !ok {
						if _, ok := en.String(); !ok {
							errf(en.Line, fmt.Sprintf("intent %q wants a map or label", name))
						}
					}
				}
			}
		} else {
			errf(n.Line, `"intents" wants a list of names or a map`)
		}
	}
	if n, ok := doc["hooks"]; ok && n.Value != nil {
		l, ok := n.List()
		if !ok {
			errf(n.Line, `"hooks" wants a list of {intent, phase, ...}`)
		} else {
			for _, e := range l {
				em, ok := e.(map[string]Node)
				if !ok {
					errf(n.Line, `"hooks" entries want maps`)
					continue
				}
				closedKeys(descFile, em, map[string]bool{"intent": true, "phase": true, "label": true, "effect": true}, "hook", &issues)
				if in, ok := em["intent"]; ok {
					if s, ok := in.String(); ok && len(intentNames) > 0 && !intentNames[s] {
						errf(in.Line, fmt.Sprintf("hook references unknown intent %q", s))
					}
				} else {
					errf(n.Line, "hook requires an intent")
				}
				if ph, ok := em["phase"]; ok {
					if s, ok := ph.String(); ok && s != "pre-roll" && s != "post-roll" && s != "interpret" {
						errf(ph.Line, fmt.Sprintf("hook phase %q wants pre-roll|post-roll|interpret", s))
					}
				} else {
					errf(n.Line, "hook requires a phase")
				}
			}
		}
	}
	if n, ok := doc["optionals"]; ok && n.Value != nil {
		l, ok := n.List()
		if !ok {
			errf(n.Line, `"optionals" wants a list of {id, ...}`)
		} else {
			seen := map[string]bool{}
			for _, e := range l {
				em, ok := e.(map[string]Node)
				if !ok {
					errf(n.Line, `"optionals" entries want maps`)
					continue
				}
				closedKeys(descFile, em, map[string]bool{"id": true, "title": true, "requires": true, "conflicts": true}, "optional", &issues)
				in, ok := em["id"]
				if !ok || in.Value == nil {
					errf(n.Line, "optional requires an id")
					continue
				}
				s, ok := in.String()
				if !ok || !idRe.MatchString(s) {
					errf(in.Line, fmt.Sprintf("bad optional id %q", in.Value))
					continue
				}
				if seen[s] {
					errf(in.Line, fmt.Sprintf("duplicate optional id %q in pack", s))
					continue
				}
				seen[s] = true
			}
		}
	}
	if n, ok := doc["content"]; ok && n.Value != nil {
		l, ok := n.StringList()
		if !ok {
			errf(n.Line, `"content" wants a list of markdown paths`)
		} else {
			for _, c := range l {
				if strings.Contains(c, "\\") || strings.HasPrefix(c, "/") || strings.Contains(c, "..") {
					errf(n.Line, fmt.Sprintf("content path %q is not vault-safe", c))
					continue
				}
				if !strings.HasSuffix(strings.ToLower(c), ".md") {
					errf(n.Line, fmt.Sprintf("content path %q wants a .md file", c))
					continue
				}
				full := filepath.Join(dir, filepath.FromSlash(c))
				data, err := os.ReadFile(full)
				if err != nil {
					errf(n.Line, fmt.Sprintf("content file %q missing", c))
					continue
				}
				if line, term := firstIdentityTerm(string(data)); term != "" {
					warnf(filepath.ToSlash(filepath.Join(packDir, c)), line,
						fmt.Sprintf("possible non-SRD term %q (warn-only; GM owns table liability)", term))
				}
			}
		}
	}
	_ = parent
	return issues, nil
}

// checkDefMap validates stats/rolls-style maps of name -> scalar-field map.
func checkDefMap(n Node, allowed map[string]bool, file string, issues *[]Issue) {
	m, ok := n.Map()
	if !ok {
		*issues = append(*issues, Issue{Severity: "error", File: file, Line: n.Line, Message: "wants a map of name -> fields"})
		return
	}
	for _, name := range sortedKeys(m) {
		en := m[name]
		em, ok := en.Map()
		if !ok {
			*issues = append(*issues, Issue{Severity: "error", File: file, Line: en.Line, Message: fmt.Sprintf("%q wants a field map", name)})
			continue
		}
		closedKeys(file, em, allowed, name, issues)
	}
}

// closedKeys errors on unknown keys inside one descriptor map.
func closedKeys(file string, m map[string]Node, allowed map[string]bool, what string, issues *[]Issue) {
	for _, k := range sortedKeys(m) {
		if !allowed[k] {
			*issues = append(*issues, Issue{Severity: "error", File: file, Line: m[k].Line,
				Message: fmt.Sprintf("%s: unknown key %q", what, k)})
		} else if sub, ok := m[k].Map(); ok {
			*issues = append(*issues, Issue{Severity: "error", File: file, Line: m[k].Line,
				Message: fmt.Sprintf("%s.%s wants a scalar, not a map", what, k)})
			_ = sub
		}
	}
}

func sortedKeys(m map[string]Node) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// identityTerms are WotC Product-Identity names with no SRD presence.
// A hit warns (never errors): the GM decides what their table runs.
var identityTerms = []string{
	"beholder", "mind flayer", "illithid", "githyanki", "githzerai",
	"umber hulk", "slaad",
}

func firstIdentityTerm(content string) (int, string) {
	norm := strings.ReplaceAll(content, "\r\n", "\n")
	for i, ln := range strings.Split(norm, "\n") {
		low := strings.ToLower(ln)
		for _, t := range identityTerms {
			if strings.Contains(low, t) {
				return i + 1, t
			}
		}
	}
	return 0, ""
}

// HasErrors reports whether any issue is an error.
func HasErrors(issues []Issue) bool {
	for _, i := range issues {
		if i.Severity == "error" {
			return true
		}
	}
	return false
}
