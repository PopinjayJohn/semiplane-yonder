package ruleset

import (
	"testing"
)

func numMods(mods ...NumericModifier) *Modifiers {
	return &Modifiers{NumericModifiers: mods}
}

func TestComposeDamageSums(t *testing.T) {
	out := ComposeModifiers(
		numMods(NumericModifier{Source: "base:sneak", Type: "bonus", Value: 5, Reason: "sneak", Applies: "damage"}),
		numMods(NumericModifier{Source: "overlay:duel", Type: "bonus", Value: 2, Reason: "dueling", Applies: "damage"}),
		numMods(NumericModifier{Source: "homebrew:bane", Type: "penalty", Value: 1, Reason: "bane", Applies: "damage"}),
	)
	if got := out.Effective.BonusFor("damage"); got != 6 {
		t.Errorf("damage bonus = %d, want 6 (5+2-1)", got)
	}
	if len(out.NumericModifiers) != 3 {
		t.Errorf("pinned list has %d entries, want 3 (full list preserved)", len(out.NumericModifiers))
	}
	// Order preserved base→overlay→homebrew.
	if out.NumericModifiers[0].Source != "base:sneak" || out.NumericModifiers[2].Source != "homebrew:bane" {
		t.Errorf("layer order not preserved: %+v", out.NumericModifiers)
	}
}

func TestComposeAdvantageCancels(t *testing.T) {
	adv := NumericModifier{Source: "a", Type: "advantage", Value: 1, Reason: "flanking"}
	dis := NumericModifier{Source: "b", Type: "disadvantage", Value: -1, Reason: "prone"}
	if out := ComposeModifiers(numMods(adv)); out.Effective.Advantage != 1 {
		t.Errorf("lone advantage = %d, want 1", out.Effective.Advantage)
	}
	if out := ComposeModifiers(numMods(dis)); out.Effective.Advantage != -1 {
		t.Errorf("lone disadvantage = %d, want -1", out.Effective.Advantage)
	}
	// Cancel: equal weight rolls straight, sources still pinned.
	out := ComposeModifiers(numMods(adv), numMods(dis))
	if out.Effective.Advantage != 0 {
		t.Errorf("adv+dis = %d, want 0 (cancel)", out.Effective.Advantage)
	}
	if out.Effective.AdvSources != 1 || out.Effective.DisSources != 1 {
		t.Errorf("sources not counted: %+v", out.Effective)
	}
	if len(out.NumericModifiers) != 2 {
		t.Errorf("cancelled entries must stay pinned, got %d", len(out.NumericModifiers))
	}
	// 2v1 still cancels (any opposition removes the net state).
	out = ComposeModifiers(numMods(adv, adv), numMods(dis))
	if out.Effective.Advantage != 0 {
		t.Errorf("2adv+1dis = %d, want 0 (cancel)", out.Effective.Advantage)
	}
}

func TestComposeCritNarrowestWins(t *testing.T) {
	out := ComposeModifiers(numMods(
		NumericModifier{Source: "base", Type: "crit-range", Value: 20, Reason: "base 20"},
		NumericModifier{Source: "overlay", Type: "crit-range", Value: 19, Reason: "champion 19-20"},
		NumericModifier{Source: "homebrew", Type: "crit-range", Value: 18, Reason: "keen 18-20"},
	))
	if !out.Effective.HasCrit || out.Effective.CritThreshold != 20 {
		t.Errorf("crit = %+v, want narrowest (20)", out.Effective)
	}
}

func TestComposeNilAndUnknown(t *testing.T) {
	out := ComposeModifiers(nil, numMods(
		NumericModifier{Source: "x", Type: "mystery", Value: 99},
	))
	if len(out.NumericModifiers) != 1 {
		t.Fatalf("unknown entry dropped from pinned list: %+v", out)
	}
	// Unknown kinds fail lint but must fail closed at runtime: no effect.
	if out.Effective.BonusFor("all") != 0 || out.Effective.Advantage != 0 || out.Effective.HasCrit {
		t.Errorf("unknown kind leaked into Effective: %+v", out.Effective)
	}
}

func TestCatalogPerBase(t *testing.T) {
	dnd := NewCatalog("dnd", map[string]IntentDef{
		"attack": {Key: "attack", Name: "Attack"},
	})
	if err := dnd.ValidateIntent("attack"); err != nil {
		t.Fatal(err)
	}
	if err := dnd.ValidateIntent("skill-push"); err == nil {
		t.Error("CoC intent validated against dnd catalog (must be per-base)")
	}
	dnd.Extend(map[string]IntentDef{"attack": {Key: "attack", Name: "Attack v2"}})
	if d, _ := dnd.Lookup("attack"); d.Name != "Attack v2" {
		t.Error("overlay extend did not replace intent")
	}
	if got := dnd.Intents(); len(got) != 1 || got[0] != "attack" {
		t.Errorf("intent order = %v", got)
	}
}
