package dice

import (
	"context"
	"testing"

	"github.com/semiplane/yonder/internal/ruleset"
)

func newTestTransport(t *testing.T, cfg TransportConfig) (*TransportService, *logStore) {
	t.Helper()
	db := openLogDB(t)
	store := NewLogStore(db).(*logStore)
	return NewTransportService(NewRoller(), store, cfg), store
}

func TestTransportRollLogsAndReplays(t *testing.T) {
	ctx := context.Background()
	tr, _ := newTestTransport(t, DefaultTransportConfig())
	resp, err := tr.Roll(ctx, RollRequest{
		Notation: "1d20+5", ActorID: "fighter", IntentID: "attack",
		Seed: seedOf(7, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.LogID == "" {
		t.Fatal("no log id assigned")
	}
	if resp.Result.RollID != resp.LogID {
		t.Errorf("result id %q != log id %q", resp.Result.RollID, resp.LogID)
	}
	// Replay reads the stored row, never re-rolls: identical values.
	replayed, err := tr.Replay(ctx, resp.LogID, "actor:fighter")
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Total != resp.Result.Total || replayed.Notation != resp.Result.Notation {
		t.Errorf("replay %+v != roll %+v", replayed, resp.Result)
	}
	if len(replayed.Dice) != len(resp.Result.Dice) {
		t.Fatalf("replay dice count %d != %d", len(replayed.Dice), len(resp.Result.Dice))
	}
	for i := range replayed.Dice {
		if replayed.Dice[i] != resp.Result.Dice[i] {
			t.Errorf("replay die %d %+v != %+v", i, replayed.Dice[i], resp.Result.Dice[i])
		}
	}
	// Same seed rolls identically (deterministic stream), so replay also
	// equals a fresh seeded roll — the log pins, not the RNG.
	again, err := NewRoller().RollWithSeed(ctx, "1d20+5", seedOf(7, 32))
	if err != nil {
		t.Fatal(err)
	}
	if again.Total != resp.Result.Total {
		t.Errorf("seeded re-roll total %d != logged %d", again.Total, resp.Result.Total)
	}
}

func TestTransportReplayAuth(t *testing.T) {
	ctx := context.Background()
	tr, _ := newTestTransport(t, DefaultTransportConfig())
	if _, err := tr.Replay(ctx, "", "actor:fighter"); err == nil {
		t.Error("empty roll id succeeded, want error")
	}
	if _, err := tr.Replay(ctx, "9999", "actor:fighter"); err == nil {
		t.Error("unknown roll id succeeded, want error")
	}
	resp, err := tr.Roll(ctx, RollRequest{
		Notation: "1d20", ActorID: "fighter", Seed: seedOf(3, 16),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Open rolls replay for any identified viewer...
	if _, err := tr.Replay(ctx, resp.LogID, "actor:goblin"); err != nil {
		t.Errorf("open replay for other viewer = %v, want nil", err)
	}
	// ...but an empty viewer is rejected.
	if _, err := tr.Replay(ctx, resp.LogID, ""); err == nil {
		t.Error("replay with empty viewer succeeded, want error")
	}
}

func TestTransportBlindRouting(t *testing.T) {
	ctx := context.Background()
	tr, _ := newTestTransport(t, DefaultTransportConfig())
	resp, err := tr.Roll(ctx, RollRequest{
		Notation: "1d20", ActorID: "gm", Blind: true, Broadcast: true,
		Seed: seedOf(5, 16),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Result.Blind {
		t.Error("blind roll lost its flag")
	}
	if len(resp.Broadcast) != 1 || resp.Broadcast[0].ViewerHash != "role:gm" {
		t.Errorf("blind broadcast = %+v, want single role:gm target", resp.Broadcast)
	}
	// Strangers cannot replay a blind roll; the GM role and the original
	// viewer key can (per-viewer re-auth against current data).
	if _, err := tr.Replay(ctx, resp.LogID, "actor:goblin"); err == nil {
		t.Error("blind replay for stranger succeeded, want error")
	}
	if _, err := tr.Replay(ctx, resp.LogID, "role:gm"); err != nil {
		t.Errorf("blind replay for gm = %v, want nil", err)
	}
	if _, err := tr.Replay(ctx, resp.LogID, "actor:gm"); err != nil {
		t.Errorf("blind replay for original viewer = %v, want nil", err)
	}
	// Redacted copies carry shape but no values.
	red := RedactedCopy(resp.Result)
	if red.Total != 0 || red.Seed != nil || red.Symbols != nil || red.Discarded != nil {
		t.Errorf("redacted copy leaks values: %+v", red)
	}
	if len(red.Dice) != len(resp.Result.Dice) {
		t.Errorf("redacted dice count %d != %d", len(red.Dice), len(resp.Result.Dice))
	}
	for _, d := range red.Dice {
		if d.Value != 0 {
			t.Errorf("redacted die leaks value %+v", d)
		}
	}
}

func TestTransportAdvantagePair(t *testing.T) {
	ctx := context.Background()
	tr, _ := newTestTransport(t, DefaultTransportConfig())
	resp, err := tr.Roll(ctx, RollRequest{
		Notation: "1d20adv", ActorID: "fighter", Seed: seedOf(11, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	pair, loser := resp.Result, resp.Result.Discarded
	if loser == nil {
		t.Fatal("advantage roll has no discarded attempt")
	}
	if pair.Total < loser.Total {
		t.Errorf("advantage kept %d over %d (wrong extreme)", pair.Total, loser.Total)
	}
	// Same seed replays the same pair — the loser is pinned, not re-rolled.
	replayed, err := tr.Replay(ctx, resp.LogID, "actor:fighter")
	if err != nil {
		t.Fatal(err)
	}
	if replayed.Discarded == nil || replayed.Discarded.Total != loser.Total {
		t.Errorf("replayed pair lost the loser: %+v", replayed.Discarded)
	}
	dis, err := tr.Roll(ctx, RollRequest{
		Notation: "1d20dis", ActorID: "fighter", Seed: seedOf(11, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	if dis.Result.Total > dis.Result.Discarded.Total {
		t.Errorf("disadvantage kept %d over %d (wrong extreme)",
			dis.Result.Total, dis.Result.Discarded.Total)
	}
}

func TestTransportHookAdvantageAndConflict(t *testing.T) {
	ctx := context.Background()
	tr, _ := newTestTransport(t, DefaultTransportConfig())
	mods := &ruleset.Modifiers{
		NumericModifiers: []ruleset.NumericModifier{
			{Source: "base:flank", Type: ruleset.ModAdvantage, Value: 1, Reason: "flanking", Applies: "all"},
		},
	}
	mods.Effective = ruleset.ComposeModifiers(mods).Effective
	// Hook advantage with a straight notation fans out to a pair.
	resp, err := tr.Roll(ctx, RollRequest{
		Notation: "1d20", ActorID: "fighter", Modifiers: mods, Seed: seedOf(13, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Result.Discarded == nil {
		t.Error("hook advantage did not fan out to a roll-twice pair")
	}
	// Player-typed dis + hook adv conflict fails closed instead of guessing.
	bad := &ruleset.Modifiers{
		NumericModifiers: []ruleset.NumericModifier{
			{Source: "base:flank", Type: ruleset.ModAdvantage, Value: 1, Reason: "flanking", Applies: "all"},
		},
	}
	bad.Effective = ruleset.ComposeModifiers(bad).Effective
	if _, err := tr.Roll(ctx, RollRequest{
		Notation: "1d20dis", ActorID: "fighter", Modifiers: bad, Seed: seedOf(13, 32),
	}); err == nil {
		t.Error("notation dis + hook adv succeeded, want conflict error")
	}
}

func TestTransportScopeBonusPinned(t *testing.T) {
	ctx := context.Background()
	tr, store := newTestTransport(t, DefaultTransportConfig())
	mods := &ruleset.Modifiers{
		NumericModifiers: []ruleset.NumericModifier{
			{Source: "base:sneak", Type: ruleset.ModBonus, Value: 2, Reason: "sneak", Applies: "all"},
		},
	}
	mods.Effective = ruleset.ComposeModifiers(mods).Effective
	plain, err := NewRoller().RollWithSeed(ctx, "1d20", seedOf(17, 32))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := tr.Roll(ctx, RollRequest{
		Notation: "1d20", ActorID: "fighter", Modifiers: mods,
		Metadata: map[string]any{"scope": "all"}, Seed: seedOf(17, 32),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Result.Total != plain.Total+2 {
		t.Errorf("total %d != plain %d + bonus 2", resp.Result.Total, plain.Total)
	}
	if len(resp.Result.Modifiers) != 1 || resp.Result.Modifiers[0].Reason != "sneak" {
		t.Errorf("pinned modifiers = %+v, want the sneak reason", resp.Result.Modifiers)
	}
	entry, err := store.Get(ctx, resp.LogID)
	if err != nil {
		t.Fatal(err)
	}
	if entry.Modifiers == nil || len(entry.Modifiers.NumericModifiers) != 1 {
		t.Errorf("log entry modifiers = %+v, want pinned list", entry.Modifiers)
	}
}

func TestTransportRateLimit(t *testing.T) {
	ctx := context.Background()
	cfg := DefaultTransportConfig()
	cfg.MaxRolls = 2
	tr, _ := newTestTransport(t, cfg)
	req := RollRequest{Notation: "1d6", ActorID: "spammer"}
	if _, err := tr.Roll(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Roll(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Roll(ctx, req); err == nil {
		t.Error("third roll in the same second succeeded, want rate limit")
	}
	// A different actor has its own bucket.
	if _, err := tr.Roll(ctx, RollRequest{Notation: "1d6", ActorID: "other"}); err != nil {
		t.Errorf("other actor blocked by spammer's bucket: %v", err)
	}
}

func TestTransportBaseMaxima(t *testing.T) {
	ctx := context.Background()
	cfg := DefaultTransportConfig()
	cfg.MaxDice = 1
	cfg.MaxFaces = 6
	tr, _ := newTestTransport(t, cfg)
	if _, err := tr.Roll(ctx, RollRequest{Notation: "2d6", ActorID: "f"}); err == nil {
		t.Error("2d6 over base max dice succeeded, want error")
	}
	if _, err := tr.Roll(ctx, RollRequest{Notation: "1d20", ActorID: "f"}); err == nil {
		t.Error("1d20 over base max faces succeeded, want error")
	}
	if _, err := tr.Roll(ctx, RollRequest{Notation: "1d6", ActorID: "f"}); err != nil {
		t.Errorf("1d6 within base maxima = %v, want nil", err)
	}
	if _, err := tr.Roll(ctx, RollRequest{Notation: "1d20", ActorID: ""}); err == nil {
		t.Error("empty actor succeeded, want error")
	}
}

// TestDemoFlankingEndToEnd is the H2 demo: a flanking attack intent
// evaluates through base→overlay hooks, rolls advantage with both reasons
// pinned in the log; disabling the overlay optional mid-session changes
// future rolls only — history keeps its modifier list.
func TestDemoFlankingEndToEnd(t *testing.T) {
	ctx := context.Background()
	e := ruleset.NewEngine()
	base := &ruleset.Ruleset{
		ID: "dnd", Name: "D&D base", Version: "1", Type: "base",
		IntentDefs: map[string]ruleset.IntentDef{
			"attack": {Key: "attack", Name: "Attack", Hooks: []ruleset.HookDef{
				{ID: "flank", Phase: ruleset.PhasePreRoll, Layer: "base",
					Script: "if(flanking, 1, 0)", Kind: ruleset.ModAdvantage, Reason: "flanking (base)"},
			}},
		},
		Optionals: map[string]ruleset.OptionalFeature{
			"high-ground": {ID: "high-ground", Name: "High ground", Default: true},
		},
	}
	if err := e.LoadBase(base); err != nil {
		t.Fatal(err)
	}
	overlay := &ruleset.Ruleset{
		ID: "5e-2024", Name: "2024 overlay", Version: "1", Type: "overlay", ParentID: "dnd",
		IntentDefs: map[string]ruleset.IntentDef{
			"attack": {Key: "attack", Name: "Attack", Hooks: []ruleset.HookDef{
				{ID: "high", Phase: ruleset.PhasePreRoll, Layer: "overlay",
					Script: "if(high_ground, 1, 0)", Kind: ruleset.ModAdvantage,
					Reason: "high ground (overlay)", Requires: "high-ground"},
			}},
		},
	}
	if err := e.LoadOverlay(overlay); err != nil {
		t.Fatal(err)
	}

	intent := ruleset.Intent{
		Intent: "attack", Actor: "fighter", Targets: []string{"goblin"},
		Tool:    "longsword",
		Context: map[string]any{"flanking": true, "high_ground": true},
	}
	mods, err := e.Evaluate(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	if mods.Effective.Advantage != 1 {
		t.Fatalf("effective advantage = %d, want 1", mods.Effective.Advantage)
	}

	tr, store := newTestTransport(t, DefaultTransportConfig())
	resp, err := tr.Roll(ctx, RollRequest{
		Notation: "1d20", ActorID: "fighter", IntentID: "attack",
		Modifiers: mods, Seed: seedOf(23, 32),
		Metadata: map[string]any{
			"targets": []string{"goblin"}, "tool": "longsword",
			"context": map[string]any{"flanking": true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Result.Discarded == nil {
		t.Fatal("flanking attack did not roll advantage")
	}
	// Both reasons pinned in the log.
	entry, err := store.Get(ctx, resp.LogID)
	if err != nil {
		t.Fatal(err)
	}
	reasons := map[string]bool{}
	for _, m := range entry.Result.Modifiers {
		reasons[m.Reason] = true
	}
	if !reasons["flanking (base)"] || !reasons["high ground (overlay)"] {
		t.Errorf("pinned reasons = %v, want both flanking + high ground", reasons)
	}

	// Mid-session toggle: future evaluations lose the overlay reason...
	if err := e.DisableFeature("high-ground"); err != nil {
		t.Fatal(err)
	}
	after, err := e.Evaluate(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	still := 0
	for _, m := range after.NumericModifiers {
		if m.Type == ruleset.ModAdvantage {
			still++
		}
	}
	if still != 1 {
		t.Errorf("after toggle, %d advantage reasons, want 1 (base only)", still)
	}
	// ...but history pins its modifier list.
	again, err := store.Get(ctx, resp.LogID)
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Result.Modifiers) != 2 {
		t.Errorf("history modifiers = %d, want 2 (toggle must not rewrite history)",
			len(again.Result.Modifiers))
	}
}
