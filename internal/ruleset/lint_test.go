package ruleset

import (
	"strings"
	"testing"
)

func validBase() *Ruleset {
	f := 10.0
	return &Ruleset{
		ID: "dnd", Name: "D&D", Version: "1", Type: "base",
		StatDefs: map[string]StatDef{
			"str": {Key: "str", Name: "Strength", Type: "int", Min: floatPtr(1), Max: &f},
		},
		DerivedDefs: map[string]DerivedDef{
			"strmod": {Key: "strmod", Name: "Str mod", Formula: "floor(str-10,2)", Depends: []string{"str"}},
		},
		DiceDefs: map[string]DiceDef{
			"longsword": {Key: "longsword", Name: "Longsword", Notation: "1d8"},
		},
		IntentDefs: map[string]IntentDef{
			"attack": {Key: "attack", Name: "Attack", Stats: []string{"str"}, Dice: []string{"longsword"},
				Hooks: []HookDef{
					{ID: "f", Phase: PhasePreRoll, Script: "strmod", Kind: ModBonus, Reason: "str"},
				}},
		},
		Optionals: map[string]OptionalFeature{
			"flanking": {ID: "flanking", Name: "Flanking"},
		},
	}
}

func floatPtr(f float64) *float64 { return &f }

func TestLintValid(t *testing.T) {
	issues := LintErrors(LintRuleset(validBase(), func(s string) error {
		if s == "1d8" {
			return nil
		}
		return errBadNotation
	}))
	if len(issues) != 0 {
		t.Errorf("valid pack failed lint: %v", issues)
	}
}

func TestLintUnknownsFail(t *testing.T) {
	rs := validBase()
	rs.Type = "weird"
	rs.StatDefs["x"] = StatDef{Key: "x", Type: "vibes"}
	rs.DerivedDefs["bad"] = DerivedDef{Key: "bad", Formula: "fly(str)"}
	rs.DerivedDefs["badvar"] = DerivedDef{Key: "badvar", Formula: "nope+1"}
	rs.IntentDefs["attack"] = IntentDef{Key: "attack", Stats: []string{"cha"},
		Hooks: []HookDef{{Phase: "whenever", Script: "bogusfn(1)", Kind: "mystery"}}}
	rs.Optionals["dup"] = OptionalFeature{ID: "flanking"}
	issues := LintErrors(LintRuleset(rs, nil))
	if len(issues) < 7 {
		t.Errorf("want >=7 errors, got %d: %v", len(issues), issues)
	}
}

func TestLintNotationValidator(t *testing.T) {
	rs := validBase()
	rs.DiceDefs["bad"] = DiceDef{Key: "bad", Notation: "zzz"}
	issues := LintErrors(LintRuleset(rs, func(s string) error {
		if s == "zzz" {
			return errBadNotation
		}
		return nil
	}))
	found := false
	for _, li := range issues {
		if strings.Contains(li.Path, "dice.bad") {
			found = true
		}
	}
	if !found {
		t.Errorf("bad notation not flagged: %v", issues)
	}
	// Nil validator skips the parse check only.
	if issues := LintErrors(LintRuleset(rs, nil)); len(issues) != 0 {
		t.Errorf("nil validator should skip parse: %v", issues)
	}
}

func TestLintSRDWarnsNeverBlocks(t *testing.T) {
	rs := validBase()
	rs.Optionals["v"] = OptionalFeature{ID: "v", Name: "Vecna stat block"}
	issues := LintRuleset(rs, nil)
	warns := 0
	for _, li := range issues {
		if li.Severity == "error" {
			t.Errorf("SRD heuristic blocked: %v", li)
		}
		if li.Severity == "warn" {
			warns++
		}
	}
	if warns == 0 {
		t.Error("expected a non-SRD warning")
	}
}

func TestLintJSONStrict(t *testing.T) {
	good := `{"ID":"dnd","Name":"D&D","Type":"base"}`
	issues, err := LintRulesetJSON([]byte(good), nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = issues
	badKey := `{"ID":"dnd","Name":"D&D","Type":"base","BogusField":1}`
	if _, err := LintRulesetJSON([]byte(badKey), nil); err == nil {
		t.Error("unknown JSON key passed strict lint")
	}
	if _, err := LintRulesetJSON([]byte(`{nope`), nil); err == nil {
		t.Error("invalid JSON passed lint")
	}
	if _, err := LintRulesetJSON([]byte(`   `), nil); err == nil {
		t.Error("empty pack passed lint")
	}
	if issues := LintRuleset(nil, nil); len(LintErrors(issues)) == 0 {
		t.Error("nil ruleset passed lint")
	}
}

type lintErr string

func (e lintErr) Error() string { return string(e) }

const errBadNotation = lintErr("bad notation")
