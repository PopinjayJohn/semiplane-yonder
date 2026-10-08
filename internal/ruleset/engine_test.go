package ruleset

import (
	"context"
	"testing"
)

// demoStack builds the H2 demo: dnd base with a flanking advantage hook +
// champion-style crit hook, overlay adding a second advantage reason behind
// an optional toggle.
func demoStack(t *testing.T) *Engine {
	t.Helper()
	e := NewEngine()
	base := &Ruleset{
		ID: "dnd", Name: "D&D base", Version: "1", Type: "base",
		IntentDefs: map[string]IntentDef{
			"attack": {Key: "attack", Name: "Attack", Hooks: []HookDef{
				{ID: "flank", Phase: PhasePreRoll, Layer: "base",
					Script: "if(flanking, 1, 0)", Kind: ModAdvantage, Reason: "flanking (base)"},
				{ID: "crit20", Phase: PhasePreRoll, Layer: "base",
					Script: "20", Kind: ModCritRange, Reason: "crit on 20"},
				{ID: "sneak", Phase: PhasePreRoll, Layer: "base",
					Script: "2", Kind: ModBonus, Reason: "sneak dice"},
				{ID: "reveal", Phase: PhasePostRoll, Layer: "base",
					Script: "if(total > 15, 1, 0)", Kind: ModBonus, Reason: "mighty blow"},
			}},
		},
		Optionals: map[string]OptionalFeature{
			"high-ground": {ID: "high-ground", Name: "High ground", Default: true},
		},
	}
	if err := e.LoadBase(base); err != nil {
		t.Fatal(err)
	}
	overlay := &Ruleset{
		ID: "5e-2024", Name: "2024 overlay", Version: "1", Type: "overlay", ParentID: "dnd",
		IntentDefs: map[string]IntentDef{
			"attack": {Key: "attack", Name: "Attack", Hooks: []HookDef{
				{ID: "high", Phase: PhasePreRoll, Layer: "overlay",
					Script: "if(high_ground, 1, 0)", Kind: ModAdvantage,
					Reason: "high ground (overlay)", Requires: "high-ground"},
			}},
		},
	}
	if err := e.LoadOverlay(overlay); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestEvaluateFlankingBothReasons(t *testing.T) {
	e := demoStack(t)
	ctx := context.Background()
	out, err := e.Evaluate(ctx, Intent{
		Intent:  "attack",
		Actor:   "fighter",
		Targets: []string{"goblin"},
		Context: map[string]any{"flanking": true, "high_ground": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.Effective.Advantage != 1 {
		t.Errorf("advantage = %d, want 1", out.Effective.Advantage)
	}
	reasons := map[string]bool{}
	for _, m := range out.NumericModifiers {
		reasons[m.Reason] = true
	}
	if !reasons["flanking (base)"] || !reasons["high ground (overlay)"] {
		t.Errorf("both reasons must pin, got %v", reasons)
	}
	if out.Effective.BonusFor("all") != 2 {
		t.Errorf("bonus = %d, want 2", out.Effective.BonusFor("all"))
	}
	if !out.Effective.HasCrit || out.Effective.CritThreshold != 20 {
		t.Errorf("crit = %+v", out.Effective)
	}
	// Post-roll hook deferred, not evaluated yet.
	if len(out.PostRollHooks) != 1 || out.PostRollHooks[0].Modifiers != nil {
		t.Errorf("post-roll hook must defer, got %+v", out.PostRollHooks)
	}
	if out.Metadata.BaseID != "dnd" || out.Metadata.OverlayID != "5e-2024" {
		t.Errorf("metadata = %+v", out.Metadata)
	}
}

func TestToggleAffectsFutureRollsOnly(t *testing.T) {
	e := demoStack(t)
	ctx := context.Background()
	intent := Intent{Intent: "attack", Actor: "f",
		Context: map[string]any{"flanking": false, "high_ground": true}}
	before, err := e.Evaluate(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	if before.Effective.Advantage != 1 {
		t.Fatalf("overlay hook active: adv = %d", before.Effective.Advantage)
	}
	if err := e.DisableFeature("high-ground"); err != nil {
		t.Fatal(err)
	}
	after, err := e.Evaluate(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	if after.Effective.Advantage != 0 {
		t.Errorf("after toggle off: adv = %d, want 0", after.Effective.Advantage)
	}
	// Pinned history is untouched: the earlier result still carries its list.
	found := false
	for _, m := range before.NumericModifiers {
		if m.Reason == "high ground (overlay)" {
			found = true
		}
	}
	if !found {
		t.Error("earlier pinned list lost the overlay reason")
	}
	// Unknown toggles fail.
	if err := e.DisableFeature("nope"); err == nil {
		t.Error("unknown feature toggle succeeded")
	}
}

func TestEvaluateUnknownIntentFails(t *testing.T) {
	e := demoStack(t)
	_, err := e.Evaluate(context.Background(), Intent{Intent: "skill-push"})
	if err == nil {
		t.Error("CoC intent evaluated against dnd catalog")
	}
}

func TestEvaluateNeedsBase(t *testing.T) {
	_, err := NewEngine().Evaluate(context.Background(), Intent{Intent: "attack"})
	if err == nil {
		t.Error("evaluate without base succeeded")
	}
}

func TestLoadEnforcesParents(t *testing.T) {
	e := NewEngine()
	if err := e.LoadOverlay(&Ruleset{ID: "o", Type: "overlay", ParentID: "dnd"}); err == nil {
		t.Error("overlay before base succeeded")
	}
	if err := e.LoadBase(&Ruleset{ID: "dnd", Name: "b", Type: "base"}); err != nil {
		t.Fatal(err)
	}
	wrong := &Ruleset{ID: "o", Name: "o", Type: "overlay", ParentID: "coc"}
	if err := e.LoadOverlay(wrong); err == nil {
		t.Error("overlay for another base loaded")
	}
	bad := &Ruleset{ID: "h", Name: "h", Type: "homebrew", ParentID: "coc",
		IntentDefs: map[string]IntentDef{}}
	if err := e.LoadHomebrew(bad); err == nil {
		t.Error("homebrew for another base loaded")
	}
	gated := &Ruleset{ID: "h2", Name: "h2", Type: "homebrew", ParentID: "dnd",
		IntentDefs: map[string]IntentDef{
			"attack": {Key: "attack", Hooks: []HookDef{
				{ID: "x", Phase: PhasePreRoll, Script: "1", Requires: "typo-id"},
			}},
		}}
	if err := e.LoadHomebrew(gated); err == nil {
		t.Error("hook with unknown Requires loaded (must fail, never silently skip)")
	}
}

func TestApplyPostRoll(t *testing.T) {
	e := demoStack(t)
	ctx := context.Background()
	intentCtx := map[string]any{"flanking": false, "high_ground": false}
	out, err := e.Evaluate(ctx, Intent{Intent: "attack", Actor: "f", Context: intentCtx})
	if err != nil {
		t.Fatal(err)
	}
	done, extra, err := e.ApplyPostRoll(ctx,
		Intent{Intent: "attack", Actor: "f", Context: intentCtx},
		out.PostRollHooks,
		map[string]Value{"total": IntValue(18)})
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || done[0].Modifiers == nil {
		t.Fatalf("deferred hook not resolved: %+v", done)
	}
	if extra.Effective.BonusFor("all") != 1 {
		t.Errorf("mighty blow bonus = %d, want 1", extra.Effective.BonusFor("all"))
	}
	_, extra2, err := e.ApplyPostRoll(ctx,
		Intent{Intent: "attack", Actor: "f", Context: intentCtx},
		out.PostRollHooks,
		map[string]Value{"total": IntValue(5)})
	if err != nil {
		t.Fatal(err)
	}
	if extra2.Effective.BonusFor("all") != 0 {
		t.Errorf("low total must not trigger, got %d", extra2.Effective.BonusFor("all"))
	}
}
