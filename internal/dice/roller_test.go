package dice

import (
	"context"
	"testing"
)

func seedOf(b byte, n int) []byte {
	s := make([]byte, n)
	for i := range s {
		s[i] = b + byte(i)
	}
	return s
}

func TestRollSeededDeterministic(t *testing.T) {
	ctx := context.Background()
	r := NewRoller()
	a, err := r.RollWithSeed(ctx, "3d6+2", seedOf(1, 32))
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.RollWithSeed(ctx, "3d6+2", seedOf(1, 32))
	if err != nil {
		t.Fatal(err)
	}
	if a.Total != b.Total || len(a.Dice) != len(b.Dice) {
		t.Fatalf("same seed diverged: %+v vs %+v", a, b)
	}
	for i := range a.Dice {
		if a.Dice[i] != b.Dice[i] {
			t.Fatalf("die %d diverged: %+v vs %+v", i, a.Dice[i], b.Dice[i])
		}
	}
	c, err := r.RollWithSeed(ctx, "3d6+2", seedOf(9, 32))
	if err != nil {
		t.Fatal(err)
	}
	same := c.Total == a.Total
	if same {
		for i := range a.Dice {
			if a.Dice[i] != c.Dice[i] {
				same = false
				break
			}
		}
	}
	if same {
		t.Error("different seeds produced identical rolls (astronomically unlikely; stream suspect)")
	}
}

func TestRollSeededErrors(t *testing.T) {
	ctx := context.Background()
	r := NewRoller()
	if _, err := r.RollWithSeed(ctx, "1d20", nil); err == nil {
		t.Error("nil seed succeeded, want error")
	}
	if _, err := r.RollWithSeed(ctx, "1d20", []byte{}); err == nil {
		t.Error("empty seed succeeded, want error")
	}
	if _, err := r.RollWithSeed(ctx, "bogus", seedOf(1, 8)); err == nil {
		t.Error("bad notation succeeded, want error")
	}
	if _, err := r.Roll(ctx, "bogus"); err == nil {
		t.Error("Roll(bogus) succeeded, want error")
	}
}

func TestRollBounds(t *testing.T) {
	ctx := context.Background()
	r := NewRoller()
	for _, n := range []string{"101d6", "1d1001", "0d6", "4d6kh9"} {
		if _, err := r.RollWithSeed(ctx, n, seedOf(2, 8)); err == nil {
			t.Errorf("Roll(%q) succeeded, want bounds error", n)
		}
	}
}

func TestRollCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewRoller().Roll(ctx, "1d20"); err == nil {
		t.Error("canceled context succeeded, want error")
	}
}

// TestPropertyTotalMatchesKept rolls many seeded notations and checks the
// transport invariant: total == sum(kept dice) + flat modifier.
func TestPropertyTotalMatchesKept(t *testing.T) {
	ctx := context.Background()
	r := NewRoller()
	notations := []string{"1d20", "2d6+3", "4d6kh3", "4d6kl2", "4d6dl1", "8d6!", "4d6r2", "2d8-1+2", "10d10"}
	for _, n := range notations {
		spec, err := ParseNotation(n, nil)
		if err != nil {
			t.Fatal(err)
		}
		nfaces := spec.Faces
		for s := 0; s < 25; s++ {
			res, err := r.RollWithSeed(ctx, n, seedOf(byte(s*7+1), 16))
			if err != nil {
				t.Fatalf("Roll(%q) seed %d: %v", n, s, err)
			}
			var sum int64
			kept := 0
			for _, d := range res.Dice {
				if d.Faces != nfaces {
					t.Errorf("%s: die faces %d, want %d", n, d.Faces, nfaces)
				}
				if d.Value < 1 || d.Value > nfaces {
					t.Errorf("%s: die value %d outside 1..%d", n, d.Value, nfaces)
				}
				if d.Rerolled && d.Original == 0 {
					t.Errorf("%s: rerolled die missing original", n)
				}
				if d.Dropped {
					continue
				}
				kept++
				sum += int64(d.Value)
			}
			want := spec.KeepN
			if spec.Keep == KeepNone {
				want = len(res.Dice)
			}
			// Explode adds dice; keep applies to the rolled set only when no
			// explode dice exist... keep is applied across all dice present,
			// so kept == min(KeepN, total dice).
			if !spec.Explode && spec.Keep != KeepNone && kept != want {
				t.Errorf("%s seed %d: kept %d, want %d", n, s, kept, want)
			}
			if res.Total != sum+spec.Modifier {
				t.Errorf("%s seed %d: total %d != kept sum %d + modifier %d",
					n, s, res.Total, sum, spec.Modifier)
			}
			if len(res.Dice) > MaxDice {
				t.Errorf("%s: %d dice exceed transport max", n, len(res.Dice))
			}
		}
	}
}

// TestPropertyCryptoInRange smoke-tests the crypto/rand path: every face
// value across many fresh rolls stays in range (unbiased rejection sampling
// must never emit 0 or faces+1).
func TestPropertyCryptoInRange(t *testing.T) {
	ctx := context.Background()
	r := NewRoller()
	for i := 0; i < 100; i++ {
		res, err := r.Roll(ctx, "2d20kh1")
		if err != nil {
			t.Fatal(err)
		}
		if res.Seed == nil {
			t.Fatal("crypto roll has no seed")
		}
		if res.RollID == "" {
			t.Fatal("crypto roll has no id")
		}
		for _, d := range res.Dice {
			if d.Value < 1 || d.Value > 20 {
				t.Fatalf("crypto d20 value %d out of range", d.Value)
			}
		}
	}
}

func TestRollSpecAPI(t *testing.T) {
	ctx := context.Background()
	r := NewRollerWithSymbols(SymbolTable{"F": {"plus", "blank", "minus"}})
	spec, err := ParseNotation("3dF", SymbolTable{"F": {"plus", "blank", "minus"}})
	if err != nil {
		t.Fatal(err)
	}
	res, err := r.(interface {
		RollSpec(ctx context.Context, spec *Spec, notation string, seed []byte) (*RollResult, error)
	}).RollSpec(ctx, spec, "3dF", seedOf(4, 16))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Symbols) != len(res.Dice) {
		t.Fatalf("symbols %d != dice %d", len(res.Symbols), len(res.Dice))
	}
	counts := CountSymbols(res.Symbols)
	total := 0
	for _, f := range res.Symbols {
		if f.Dropped {
			continue
		}
		if f.Symbol == "" {
			t.Error("kept symbol has empty name")
		}
		total++
	}
	sum := 0
	for _, n := range counts {
		sum += n
	}
	if sum != total {
		t.Errorf("CountSymbols sum %d != kept %d", sum, total)
	}
	// Numeric symbol-less roller rejects symbolic dice.
	if _, err := NewRoller().RollWithSeed(ctx, "1dF", seedOf(1, 8)); err == nil {
		t.Error("numeric roller accepted symbolic die, want error")
	}
	if _, err := NewRoller().Roll(ctx, "1dF"); err == nil {
		t.Error("numeric roller accepted symbolic die, want error")
	}
}

func TestCancelOpposites(t *testing.T) {
	opp := map[string]string{"hit": "miss", "miss": "hit", "a": "b"}
	got := CancelOpposites(map[string]int{"hit": 3, "miss": 1}, opp)
	if got["hit"] != 2 || len(got) != 1 {
		t.Errorf("cancel = %v, want map[hit:2]", got)
	}
	// Full cancellation removes both keys; each pair cancels once even if
	// listed both directions.
	got = CancelOpposites(map[string]int{"hit": 1, "miss": 1}, opp)
	if len(got) != 0 {
		t.Errorf("full cancel = %v, want empty", got)
	}
	// Unrelated symbols survive; zero counts are dropped.
	got = CancelOpposites(map[string]int{"hit": 2, "star": 1, "zero": 0}, opp)
	if got["hit"] != 2 || got["star"] != 1 || len(got) != 2 {
		t.Errorf("cancel with bystander = %v", got)
	}
	// Deterministic across map iteration orders (run repeatedly).
	for i := 0; i < 20; i++ {
		got := CancelOpposites(map[string]int{"a": 5, "b": 2, "c": 1}, opp)
		if got["a"] != 3 || got["c"] != 1 || len(got) != 2 {
			t.Fatalf("iter %d: cancel = %v", i, got)
		}
	}
}
