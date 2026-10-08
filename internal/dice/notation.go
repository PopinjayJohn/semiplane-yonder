package dice

import (
	"fmt"
	"strings"
)

// Notation grammar (transport maxima enforced by the roller, not here):
//
//	roll    := count "d" faces keep? explode? reroll? modifier? adv?
//	count   := "" | number            (empty = 1)
//	faces   := number | name          (name resolves via SymbolTable)
//	keep    := ("kh"|"kl"|"dh"|"dl") number
//	explode := "!"
//	reroll  := "r" number             (reroll once while below number)
//	modifier:= ("+"|"-") number       (repeatable, checked sum)
//	adv     := "adv" | "dis"
//
// Examples: "d20", "2d6+3", "4d6kh3", "8d6!", "4d6r1", "2d6kh1+2",
// "1d20adv", "3dF" (symbol die via table {"F": [...]}).
// Whitespace is ignored. "keep" semantics: kh/kl keep the highest/lowest N;
// dh/dl drop the highest/lowest N (converted to keep).
//
// Absolute transport maxima (P12): 100 dice, 1000 faces/symbols per roll.
// Bases may set lower values via TransportConfig; the roller enforces both.
const (
	MaxDice  = 100
	MaxFaces = 1000
)

// SymbolTable maps a symbolic die name (e.g. "F") to its faces in order.
// Declared by the ruleset base (P05 dice_types); the core engine only rolls
// indices and counts/cancels symbols — it never defines what dice mean.
type SymbolTable map[string][]string

// KeepKind selects keep/drop semantics.
type KeepKind int

const (
	KeepNone KeepKind = iota
	KeepHigh
	KeepLow
)

// Spec is a parsed roll: everything the roller needs, no strings.
type Spec struct {
	Count    int
	Faces    int      // numeric faces; 0 when Symbolic
	Symbols  []string // symbolic faces; nil when numeric
	Keep     KeepKind // KeepNone = keep all
	KeepN    int      // dice kept (after dh/dl conversion)
	Explode  bool     // roll an extra die per max-face result
	RerollLT int      // reroll once while below this value (0 = off)
	Modifier int64    // flat bonus (may be negative)
	Adv      int      // +1 advantage, -1 disadvantage, 0 straight
}

// IsSymbolic reports whether the spec rolls symbol faces.
func (s *Spec) IsSymbolic() bool { return s.Symbols != nil }

// ParseNotation parses notation with an optional symbol table (nil table =
// numeric dice only; unknown face names are errors).
func ParseNotation(notation string, symbols SymbolTable) (*Spec, error) {
	s := strings.ReplaceAll(notation, " ", "")
	if s == "" {
		return nil, fmt.Errorf("dice: empty notation")
	}
	p := &nparser{s: s, symbols: symbols}
	spec, err := p.parse()
	if err != nil {
		return nil, err
	}
	if err := spec.checkBounds(); err != nil {
		return nil, err
	}
	return spec, nil
}

// ValidateNotation parses with no symbol table (numeric dice only). It is
// the validator the ruleset lint calls to avoid an import cycle.
func ValidateNotation(notation string) error {
	_, err := ParseNotation(notation, nil)
	return err
}

func (s *Spec) checkBounds() error {
	if s.Count < 1 || s.Count > MaxDice {
		return fmt.Errorf("dice: count %d outside 1..%d", s.Count, MaxDice)
	}
	nfaces := s.Faces
	if s.IsSymbolic() {
		nfaces = len(s.Symbols)
	}
	if nfaces < 1 || nfaces > MaxFaces {
		return fmt.Errorf("dice: faces %d outside 1..%d", nfaces, MaxFaces)
	}
	if s.Keep != KeepNone && (s.KeepN < 1 || s.KeepN > s.Count) {
		return fmt.Errorf("dice: keep %d outside 1..%d", s.KeepN, s.Count)
	}
	if s.RerollLT < 0 || s.RerollLT > nfaces {
		return fmt.Errorf("dice: reroll threshold %d outside range", s.RerollLT)
	}
	return nil
}

// WithinConfig reports whether the spec fits tighter base-declared maxima
// (bases set lower values within the absolute transport maxima).
func (s *Spec) WithinConfig(maxDice, maxFaces int) error {
	if s.Count > maxDice {
		return fmt.Errorf("dice: count %d exceeds base max %d", s.Count, maxDice)
	}
	nfaces := s.Faces
	if s.IsSymbolic() {
		nfaces = len(s.Symbols)
	}
	if nfaces > maxFaces {
		return fmt.Errorf("dice: faces %d exceeds base max %d", nfaces, maxFaces)
	}
	return nil
}

type nparser struct {
	s       string
	pos     int
	symbols SymbolTable
}

func (p *nparser) parse() (*Spec, error) {
	spec := &Spec{Count: 1}
	if p.peekIsDigit() {
		n, err := p.number()
		if err != nil {
			return nil, err
		}
		spec.Count = n
	}
	if !p.eat('d') && !p.eat('D') {
		return nil, fmt.Errorf("dice: expected 'd' in %q", p.s)
	}
	if p.peekIsDigit() {
		n, err := p.number()
		if err != nil {
			return nil, err
		}
		spec.Faces = n
	} else if p.peekIsNameStart() {
		name := p.name()
		faces, ok := p.symbols[name]
		if !ok {
			return nil, fmt.Errorf("dice: unknown die %q", name)
		}
		if len(faces) == 0 {
			return nil, fmt.Errorf("dice: die %q has no faces", name)
		}
		spec.Symbols = append([]string(nil), faces...)
	} else {
		return nil, fmt.Errorf("dice: expected faces after 'd' in %q", p.s)
	}
	// keep/drop, explode, reroll may follow in any relative order, then
	// modifiers, then adv/dis. Loop to accept kh + ! + r in any order.
	for {
		switch {
		case p.eatPrefix("kh"):
			if err := p.keep(spec, KeepHigh); err != nil {
				return nil, err
			}
		case p.eatPrefix("kl"):
			if err := p.keep(spec, KeepLow); err != nil {
				return nil, err
			}
		case p.eatPrefix("dh"):
			if err := p.drop(spec, KeepHigh); err != nil {
				return nil, err
			}
		case p.eatPrefix("dl"):
			if err := p.drop(spec, KeepLow); err != nil {
				return nil, err
			}
		case p.eat('!'):
			if spec.Explode {
				return nil, fmt.Errorf("dice: duplicate '!' in %q", p.s)
			}
			spec.Explode = true
		case p.eat('r'):
			if spec.RerollLT != 0 {
				return nil, fmt.Errorf("dice: duplicate reroll in %q", p.s)
			}
			n, err := p.number()
			if err != nil {
				return nil, err
			}
			if n < 1 {
				return nil, fmt.Errorf("dice: reroll threshold must be >= 1")
			}
			spec.RerollLT = n
		default:
			goto mods
		}
	}
mods:
	for p.pos < len(p.s) && (p.s[p.pos] == '+' || p.s[p.pos] == '-') {
		neg := p.s[p.pos] == '-'
		p.pos++
		if !p.peekIsDigit() {
			return nil, fmt.Errorf("dice: expected number after sign in %q", p.s)
		}
		n, err := p.number()
		if err != nil {
			return nil, err
		}
		if neg {
			n = -n
		}
		// Checked accumulation: notations must not smuggle overflow.
		if (n > 0 && spec.Modifier > maxInt64-int64(n)) ||
			(n < 0 && spec.Modifier < minInt64-int64(n)) {
			return nil, fmt.Errorf("dice: modifier overflows int64")
		}
		spec.Modifier += int64(n)
	}
	if p.eatPrefix("adv") {
		spec.Adv = 1
	} else if p.eatPrefix("dis") {
		spec.Adv = -1
	}
	if p.pos != len(p.s) {
		return nil, fmt.Errorf("dice: unexpected %q in %q", p.s[p.pos:], p.s)
	}
	return spec, nil
}

func (p *nparser) keep(spec *Spec, kind KeepKind) error {
	if spec.Keep != KeepNone {
		return fmt.Errorf("dice: duplicate keep/drop in %q", p.s)
	}
	if !p.peekIsDigit() {
		return fmt.Errorf("dice: expected number after keep/drop in %q", p.s)
	}
	n, err := p.number()
	if err != nil {
		return err
	}
	spec.Keep, spec.KeepN = kind, n
	return nil
}

func (p *nparser) drop(spec *Spec, kind KeepKind) error {
	if spec.Keep != KeepNone {
		return fmt.Errorf("dice: duplicate keep/drop in %q", p.s)
	}
	if !p.peekIsDigit() {
		return fmt.Errorf("dice: expected number after keep/drop in %q", p.s)
	}
	n, err := p.number()
	if err != nil {
		return err
	}
	// dhN drops the highest N = keeps the lowest Count-N.
	keep := spec.Count - n
	if kind == KeepHigh {
		spec.Keep = KeepLow
	} else {
		spec.Keep = KeepHigh
	}
	spec.KeepN = keep
	return nil
}

func (p *nparser) peekIsDigit() bool {
	return p.pos < len(p.s) && p.s[p.pos] >= '0' && p.s[p.pos] <= '9'
}

func (p *nparser) peekIsNameStart() bool {
	if p.pos >= len(p.s) {
		return false
	}
	c := p.s[p.pos]
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func (p *nparser) number() (int, error) {
	start := p.pos
	for p.pos < len(p.s) && p.s[p.pos] >= '0' && p.s[p.pos] <= '9' {
		p.pos++
	}
	var n uint64
	for _, c := range p.s[start:p.pos] {
		n = n*10 + uint64(c-'0')
		if n > MaxFaces {
			return 0, fmt.Errorf("dice: number %q exceeds max %d", p.s[start:p.pos], MaxFaces)
		}
	}
	return int(n), nil
}

func (p *nparser) name() string {
	start := p.pos
	for p.pos < len(p.s) {
		c := p.s[p.pos]
		if c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') {
			p.pos++
			continue
		}
		break
	}
	return p.s[start:p.pos]
}

func (p *nparser) eat(c byte) bool {
	if p.pos < len(p.s) && p.s[p.pos] == c {
		p.pos++
		return true
	}
	return false
}

func (p *nparser) eatPrefix(pre string) bool {
	if strings.HasPrefix(p.s[p.pos:], pre) {
		// "dl"/"dh"/"kh"/"kl" and "adv"/"dis" must be followed by a digit
		// (keep counts) or end/operator (adv/dis) — never a name char, so
		// "dx" is not misread as d + "x"... "dis" vs die "is": "2dis"?
		// Notation "2d6dis" → after "2d6", "dis" prefix matches with end
		// following: fine. A symbol die named "dis" ("2ddis")? The faces
		// parser consumes the name first, so no conflict there.
		p.pos += len(pre)
		return true
	}
	return false
}

const (
	maxInt64 = int64(^uint64(0) >> 1)
	minInt64 = -maxInt64 - 1
)
