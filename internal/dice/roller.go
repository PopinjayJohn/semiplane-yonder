package dice

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	mrand "math/rand"
)

// NewRoller creates a new dice roller backed by crypto/rand.
func NewRoller() Roller {
	return &roller{symbols: nil}
}

// NewRollerWithSymbols creates a roller that also resolves symbolic dice
// (faces declared by the ruleset base).
func NewRollerWithSymbols(symbols SymbolTable) Roller {
	return &roller{symbols: symbols}
}

type roller struct {
	symbols SymbolTable
}

func (r *roller) Roll(ctx context.Context, notation string) (*RollResult, error) {
	spec, err := ParseNotation(notation, r.symbols)
	if err != nil {
		return nil, err
	}
	seed, err := GenerateSeed()
	if err != nil {
		return nil, err
	}
	return r.rollSpec(ctx, spec, notation, newCryptoSource(seed), seed)
}

func (r *roller) RollWithSeed(ctx context.Context, notation string, seed []byte) (*RollResult, error) {
	spec, err := ParseNotation(notation, r.symbols)
	if err != nil {
		return nil, err
	}
	if len(seed) == 0 {
		return nil, fmt.Errorf("dice: empty seed")
	}
	return r.rollSpec(ctx, spec, notation, newSeededSource(seed), append([]byte(nil), seed...))
}

// RollSpec rolls an already-parsed spec with fresh entropy (nil seed) or a
// deterministic stream (non-nil seed). It powers transport-level
// modifier-adjusted rolls without re-parsing.
func (r *roller) RollSpec(ctx context.Context, spec *Spec, notation string, seed []byte) (*RollResult, error) {
	if spec == nil {
		return nil, fmt.Errorf("dice: nil spec")
	}
	if seed != nil && len(seed) == 0 {
		return nil, fmt.Errorf("dice: empty seed")
	}
	if seed == nil {
		var err error
		seed, err = GenerateSeed()
		if err != nil {
			return nil, err
		}
		return r.rollSpec(ctx, spec, notation, newCryptoSource(seed), seed)
	}
	return r.rollSpec(ctx, spec, notation, newSeededSource(seed), append([]byte(nil), seed...))
}

// intSource yields uniform values in [0, n).
type intSource interface {
	Intn(n int) (int, error)
}

type cryptoSource struct{}

func newCryptoSource(_ []byte) intSource { return cryptoSource{} }

func (cryptoSource) Intn(n int) (int, error) {
	if n <= 0 {
		return 0, fmt.Errorf("dice: bad range %d", n)
	}
	// Rejection sampling off crypto/rand: unbiased, no modulo bias.
	var b [8]byte
	limit := (^uint64(0) / uint64(n)) * uint64(n)
	for {
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		v := binary.BigEndian.Uint64(b[:])
		if v < limit {
			return int(v % uint64(n)), nil
		}
	}
}

type seededSource struct {
	rng *mrand.Rand
}

func newSeededSource(seed []byte) intSource {
	sum := sha256.Sum256(seed)
	return &seededSource{rng: mrand.New(mrand.NewSource(int64(binary.BigEndian.Uint64(sum[:8]))))}
}

func (s *seededSource) Intn(n int) (int, error) {
	if n <= 0 {
		return 0, fmt.Errorf("dice: bad range %d", n)
	}
	return s.rng.Intn(n), nil
}

// rollSpec rolls an already-parsed spec with the given entropy source.
// It powers transport-level modifier-adjusted rolls without re-parsing.
func (r *roller) rollSpec(ctx context.Context, spec *Spec, notation string, src intSource, seed []byte) (*RollResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	nfaces := spec.Faces
	if spec.IsSymbolic() {
		nfaces = len(spec.Symbols)
	}
	dice := make([]DieResult, 0, spec.Count)
	for i := 0; i < spec.Count; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		v, err := src.Intn(nfaces)
		if err != nil {
			return nil, err
		}
		face := v + 1
		dr := DieResult{Faces: nfaces, Value: face}
		// Reroll once while below threshold (marks the die, keeps newest).
		if spec.RerollLT > 0 && face < spec.RerollLT {
			dr.Original = face
			dr.Rerolled = true
			v2, err := src.Intn(nfaces)
			if err != nil {
				return nil, err
			}
			face = v2 + 1
			dr.Value = face
		}
		dice = append(dice, dr)
		// Explode: extra die per max-face result, capped so a pathological
		// "!"" chain cannot exceed the transport maximum.
		for spec.Explode && face == nfaces && len(dice) < MaxDice {
			vx, err := src.Intn(nfaces)
			if err != nil {
				return nil, err
			}
			face = vx + 1
			dice = append(dice, DieResult{Faces: nfaces, Value: face, Exploded: true})
		}
	}
	applyKeep(dice, spec)
	var total int64
	for _, d := range dice {
		if d.Dropped {
			continue
		}
		var err error
		total, err = checkedAdd64(total, int64(d.Value))
		if err != nil {
			return nil, err
		}
	}
	// Symbolic dice total in the count of kept dice; the flat modifier
	// still applies (checked) so "+1 bless" style hooks compose.
	total, err := checkedAdd64(total, spec.Modifier)
	if err != nil {
		return nil, err
	}
	id, err := newRollID()
	if err != nil {
		return nil, err
	}
	res := &RollResult{
		Notation: notation,
		Total:    total,
		Dice:     dice,
		Seed:     append([]byte(nil), seed...),
		RollID:   id,
	}
	if spec.IsSymbolic() {
		res.Symbols = materializeSymbols(dice, spec.Symbols)
	}
	return res, nil
}

// applyKeep marks dropped dice per the keep spec (stable: ties keep the
// earliest-rolled dice… implemented by sorting indices, not values).
func applyKeep(dice []DieResult, spec *Spec) {
	if spec.Keep == KeepNone {
		return
	}
	idx := make([]int, len(dice))
	for i := range idx {
		idx[i] = i
	}
	less := func(a, b int) bool {
		if dice[a].Value != dice[b].Value {
			if spec.Keep == KeepHigh {
				return dice[a].Value > dice[b].Value
			}
			return dice[a].Value < dice[b].Value
		}
		return a < b
	}
	for i := 1; i < len(idx); i++ {
		for j := i; j > 0 && less(idx[j], idx[j-1]); j-- {
			idx[j], idx[j-1] = idx[j-1], idx[j]
		}
	}
	for k, di := range idx {
		if k >= spec.KeepN {
			dice[di].Dropped = true
		}
	}
}

func checkedAdd64(a, b int64) (int64, error) {
	s := a + b
	if (a^s)&(b^s) < 0 {
		return 0, fmt.Errorf("dice: integer overflow in total")
	}
	return s, nil
}

// newRollID mints a 128-bit hex roll id from crypto/rand.
func newRollID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 32)
	for i, v := range b {
		out[2*i] = hexdigits[v>>4]
		out[2*i+1] = hexdigits[v&0x0f]
	}
	return string(out), nil
}

// GenerateSeed generates a cryptographically secure seed for dice rolls.
func GenerateSeed() ([]byte, error) {
	seed := make([]byte, 32)
	_, err := rand.Read(seed)
	return seed, err
}

// ---- symbol primitives ----

// materializeSymbols maps rolled face values to their symbol names.
func materializeSymbols(dice []DieResult, faces []string) []SymbolFace {
	out := make([]SymbolFace, len(dice))
	for i, d := range dice {
		name := ""
		if d.Value >= 1 && d.Value <= len(faces) {
			name = faces[d.Value-1]
		}
		out[i] = SymbolFace{Symbol: name, Dropped: d.Dropped}
	}
	return out
}

// CountSymbols tallies kept symbols (dropped dice excluded).
func CountSymbols(faces []SymbolFace) map[string]int {
	counts := map[string]int{}
	for _, f := range faces {
		if f.Dropped || f.Symbol == "" {
			continue
		}
		counts[f.Symbol]++
	}
	return counts
}

// CancelOpposites nets opposing symbol counts: for each a↔b pair in
// opposites, the smaller count cancels out of the larger. Pairs are
// processed in sorted key order for determinism; each unordered pair
// cancels once even if listed both directions.
func CancelOpposites(counts map[string]int, opposites map[string]string) map[string]int {
	net := make(map[string]int, len(counts))
	for k, v := range counts {
		if v != 0 {
			net[k] = v
		}
	}
	keys := make([]string, 0, len(opposites))
	for k := range opposites {
		keys = append(keys, k)
	}
	// Deterministic order without importing sort in hot path… clarity wins:
	// insertion-style sort on tiny key sets.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	done := map[string]bool{}
	for _, a := range keys {
		b := opposites[a]
		if b == "" || b == a || done[a] || done[b] {
			continue
		}
		done[a], done[b] = true, true
		ca, cb := net[a], net[b]
		m := ca
		if cb < m {
			m = cb
		}
		if m > 0 {
			net[a] = ca - m
			net[b] = cb - m
			if net[a] == 0 {
				delete(net, a)
			}
			if net[b] == 0 {
				delete(net, b)
			}
		}
	}
	return net
}
