package web

// Character sheets (Lane I1, Phase 3): schema, validation, surgical edits,
// level/rest flows.
//
// Sheets live in characters/<pc>/index.md frontmatter (AGENTS.md hard rule:
// the index DB holds a read copy only; app DB rows are authoritative for
// auth/VTT/app state). Sheet data sits in the `sheet:` frontmatter MAP —
// that key already exists in Lane A's closed AllowedKeys set, so no
// top-level key changes are needed here (frontmatter keys stay append-only).
//
// Red lines (lane brief):
//   - Simple view never exposes raw frontmatter: it renders derived widgets
//     only (pips/buttons), backed by the allowlisted PATCH below.
//   - Advanced edits validate against this schema (unknown keys fail,
//     ranges enforced, owner immutable).

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"

	"github.com/semiplane/yonder/internal/ruleset"
)

// ---------------------------------------------------------------------------
// Evaluator seam (gate G3: wired to the real engine). Lane H2 owns
// ruleset.Evaluate (intent envelope {intent, actor, targets, tool, context}
// + pre/post/interpret hooks); rest/level flows call through this seam so
// hook-driven recovery bonuses (e.g. Dwarven Toughness, Song of Rest) apply
// with no call-site change. Serve passes a vault-backed engine (vaultEngine,
// below); StubEvaluator remains ONLY as the explicitly-marked fallback for
// pack-less vaults (init --bare) and unit tests.
// ---------------------------------------------------------------------------

// RecoveryEvaluator is the H2 seam for rest/level recovery hooks.
type RecoveryEvaluator interface {
	Evaluate(ctx context.Context, intent ruleset.Intent) (*ruleset.Modifiers, error)
}

// StubEvaluator contributes no hooks and never errors. FALLBACK ONLY: used
// when the vault resolves no ruleset packs (or the stack fails to load —
// serve logs loudly and sheets keep working on file truth). Never the
// default on a packed vault; see vaultEngine.
type StubEvaluator struct{}

// Evaluate implements RecoveryEvaluator (fallback: empty modifiers).
func (StubEvaluator) Evaluate(_ context.Context, _ ruleset.Intent) (*ruleset.Modifiers, error) {
	return &ruleset.Modifiers{}, nil
}

// Hit-die rolls go through dice.Roller in transport flows; rest recovery
// rolls one hit die locally with crypto/rand (spec maxima respected by
// construction: 1 die, <= 12 faces here) to stay offline and
// dependency-free.

// HitDieRoller rolls one hit die; implementations must use crypto/rand.
type HitDieRoller func(faces int64) (int64, error)

// CryptoHitDie rolls 1dFaces with crypto/rand.
func CryptoHitDie(faces int64) (int64, error) {
	if faces < 2 || faces > 1000 {
		return 0, fmt.Errorf("bad die: d%d", faces)
	}
	n, err := rand.Int(rand.Reader, big.NewInt(faces))
	if err != nil {
		return 0, err
	}
	return n.Int64() + 1, nil
}

// ---------------------------------------------------------------------------
// Schema
// ---------------------------------------------------------------------------

// SheetStatKeys lists the six abilities in wizard order.
var SheetStatKeys = []string{"str", "dex", "con", "int", "wis", "cha"}

// Sheet is the validated `sheet:` map: all scalars, flat (nested maps would
// break line-surgical edits, pitfalls: Obsidian users revolt). Slots use
// flat `slots-N` / `slots-N-max` pairs (N = 1..9); absent pairs mean the
// character has no such slots (e.g. Fighter).
type Sheet struct {
	Class       string
	Ancestry    string
	Background  string
	Level       int64
	XP          int64
	Stats       map[string]int64
	HP          int64
	HPMax       int64
	HitDice     int64
	HitDiceMax  int64
	HitDie      int64
	Inspiration int64
	DeathSucc   int64
	DeathFail   int64
	Slots       map[int64]int64 // N -> current
	SlotsMax    map[int64]int64 // N -> max
}

// SimplePatchFields are the only keys PATCH /characters/:id/fields may
// touch: current values, never maxima/identity. Level changes flow through
// LevelUp; maxima change through rest/level flows or Advanced.
var SimplePatchFields = map[string]bool{
	"hp": true, "hit-dice": true, "xp": true, "inspiration": true,
	"death-succ": true, "death-fail": true,
	"slots-1": true, "slots-2": true, "slots-3": true, "slots-4": true,
	"slots-5": true, "slots-6": true, "slots-7": true, "slots-8": true, "slots-9": true,
}

func toInt(v any) (int64, bool) {
	switch t := v.(type) {
	case int64:
		return t, true
	case int:
		return int64(t), true
	case float64:
		if t != float64(int64(t)) {
			return 0, false
		}
		return int64(t), true
	case string:
		var n int64
		s := strings.TrimSpace(t)
		if s == "" {
			return 0, false
		}
		neg := false
		if strings.HasPrefix(s, "-") {
			neg = true
			s = s[1:]
		} else if strings.HasPrefix(s, "+") {
			s = s[1:]
		}
		for i := 0; i < len(s); i++ {
			if s[i] < '0' || s[i] > '9' {
				return 0, false
			}
			n = n*10 + int64(s[i]-'0')
			if n > 1<<40 {
				return 0, false
			}
		}
		if neg {
			n = -n
		}
		return n, true
	}
	return 0, false
}

func toStr(v any) (string, bool) {
	s, ok := v.(string)
	if !ok {
		return "", false
	}
	return s, true
}

// ParseSheet validates a raw `sheet:` map into a Sheet. Unknown inner keys
// fail (red line: Advanced edits validate against schema); ranges enforce
// the timebox SRD bounds. Cross-field caps (hp <= hp-max etc.) fail.
func ParseSheet(m map[string]any) (*Sheet, error) {
	known := map[string]bool{
		"class": true, "ancestry": true, "background": true,
		"level": true, "xp": true,
		"str": true, "dex": true, "con": true, "int": true, "wis": true, "cha": true,
		"hp": true, "hp-max": true, "hit-dice": true, "hit-dice-max": true, "hit-die": true,
		"inspiration": true, "death-succ": true, "death-fail": true,
	}
	for n := int64(1); n <= 9; n++ {
		known[fmt.Sprintf("slots-%d", n)] = true
		known[fmt.Sprintf("slots-%d-max", n)] = true
	}
	var unknown []string
	for k := range m {
		if !known[k] {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("unknown sheet keys: %s", strings.Join(unknown, ", "))
	}
	sh := &Sheet{Stats: map[string]int64{}, Slots: map[int64]int64{}, SlotsMax: map[int64]int64{}}
	getStr := func(key string) (string, error) {
		v, ok := m[key]
		if !ok {
			return "", nil
		}
		s, ok := toStr(v)
		if !ok {
			return "", fmt.Errorf("bad type for sheet key %q: want string", key)
		}
		return s, nil
	}
	var err error
	if sh.Class, err = getStr("class"); err != nil {
		return nil, err
	}
	if sh.Ancestry, err = getStr("ancestry"); err != nil {
		return nil, err
	}
	if sh.Background, err = getStr("background"); err != nil {
		return nil, err
	}
	getInt := func(key string, def int64) (int64, error) {
		v, ok := m[key]
		if !ok {
			return def, nil
		}
		n, ok := toInt(v)
		if !ok {
			return 0, fmt.Errorf("bad type for sheet key %q: want integer", key)
		}
		return n, nil
	}
	intKeys := []string{"level", "xp", "str", "dex", "con", "int", "wis", "cha",
		"hp", "hp-max", "hit-dice", "hit-dice-max", "hit-die",
		"inspiration", "death-succ", "death-fail"}
	vals := map[string]int64{}
	for _, k := range intKeys {
		n, err := getInt(k, 0)
		if err != nil {
			return nil, err
		}
		vals[k] = n
	}
	sh.Level, sh.XP = vals["level"], vals["xp"]
	for _, k := range SheetStatKeys {
		sh.Stats[k] = vals[k]
	}
	sh.HP, sh.HPMax = vals["hp"], vals["hp-max"]
	sh.HitDice, sh.HitDiceMax, sh.HitDie = vals["hit-dice"], vals["hit-dice-max"], vals["hit-die"]
	sh.Inspiration, sh.DeathSucc, sh.DeathFail = vals["inspiration"], vals["death-succ"], vals["death-fail"]
	for n := int64(1); n <= 9; n++ {
		cur, err := getInt(fmt.Sprintf("slots-%d", n), -1)
		if err != nil {
			return nil, err
		}
		mx, err := getInt(fmt.Sprintf("slots-%d-max", n), -1)
		if err != nil {
			return nil, err
		}
		if cur >= 0 || mx >= 0 {
			if cur < 0 {
				cur = 0
			}
			if mx < 0 {
				mx = 0
			}
			sh.Slots[n], sh.SlotsMax[n] = cur, mx
		}
	}
	if err := sh.Validate(); err != nil {
		return nil, err
	}
	return sh, nil
}

// Validate enforces ranges + cross-field caps.
func (sh *Sheet) Validate() error {
	if sh.Level < 1 || sh.Level > 20 {
		return fmt.Errorf("level %d out of range 1-20", sh.Level)
	}
	if sh.XP < 0 {
		return fmt.Errorf("xp must be >= 0")
	}
	for _, k := range SheetStatKeys {
		if sh.Stats[k] < 1 || sh.Stats[k] > 30 {
			return fmt.Errorf("%s %d out of range 1-30", k, sh.Stats[k])
		}
	}
	if sh.HPMax < 1 || sh.HPMax > 999 {
		return fmt.Errorf("hp-max %d out of range 1-999", sh.HPMax)
	}
	if sh.HP < 0 || sh.HP > sh.HPMax {
		return fmt.Errorf("hp %d out of range 0-%d", sh.HP, sh.HPMax)
	}
	if sh.HitDiceMax < 1 || sh.HitDiceMax > 20 {
		return fmt.Errorf("hit-dice-max %d out of range 1-20", sh.HitDiceMax)
	}
	if sh.HitDice < 0 || sh.HitDice > sh.HitDiceMax {
		return fmt.Errorf("hit-dice %d out of range 0-%d", sh.HitDice, sh.HitDiceMax)
	}
	switch sh.HitDie {
	case 6, 8, 10, 12:
	default:
		return fmt.Errorf("hit-die d%d: want one of d6 d8 d10 d12", sh.HitDie)
	}
	if sh.Inspiration < 0 || sh.Inspiration > 1 {
		return fmt.Errorf("inspiration %d: want 0 or 1", sh.Inspiration)
	}
	if sh.DeathSucc < 0 || sh.DeathSucc > 3 || sh.DeathFail < 0 || sh.DeathFail > 3 {
		return fmt.Errorf("death saves out of range 0-3")
	}
	for n := int64(1); n <= 9; n++ {
		mx, hasMax := sh.SlotsMax[n]
		cur, hasCur := sh.Slots[n]
		if !hasMax && !hasCur {
			continue
		}
		if mx < 0 || mx > 9 {
			return fmt.Errorf("slots-%d-max %d out of range 0-9", n, mx)
		}
		if cur < 0 || cur > mx {
			return fmt.Errorf("slots-%d %d out of range 0-%d", n, cur, mx)
		}
	}
	return nil
}

// ToMap renders the sheet back to a frontmatter-ready map (ints as int64,
// strings plain; the writer quotes as needed).
func (sh *Sheet) ToMap() map[string]any {
	m := map[string]any{
		"class": sh.Class, "ancestry": sh.Ancestry, "background": sh.Background,
		"level": sh.Level, "xp": sh.XP,
		"hp": sh.HP, "hp-max": sh.HPMax,
		"hit-dice": sh.HitDice, "hit-dice-max": sh.HitDiceMax, "hit-die": sh.HitDie,
		"inspiration": sh.Inspiration, "death-succ": sh.DeathSucc, "death-fail": sh.DeathFail,
	}
	for _, k := range SheetStatKeys {
		m[k] = sh.Stats[k]
	}
	for n := int64(1); n <= 9; n++ {
		if mx, ok := sh.SlotsMax[n]; ok {
			m[fmt.Sprintf("slots-%d-max", n)] = mx
			m[fmt.Sprintf("slots-%d", n)] = sh.Slots[n]
		}
	}
	return m
}

// ConMod returns the Constitution modifier (floored, per SRD).
func (sh *Sheet) ConMod() int64 { return floorDiv(sh.Stats["con"]-10, 2) }

func floorDiv(a, b int64) int64 {
	q := a / b
	if (a%b != 0) && ((a < 0) != (b < 0)) {
		q--
	}
	return q
}

// ---------------------------------------------------------------------------
// Flows (pure: read sheet -> mutate -> caller writes back surgically)
// ---------------------------------------------------------------------------

// ShortRest spends spend hit dice, rolling each (+ Con mod, min 0 per die)
// up to missing HP. Returns HP actually restored.
func (sh *Sheet) ShortRest(spend int64, roll HitDieRoller) (int64, error) {
	if spend < 1 {
		return 0, fmt.Errorf("spend at least 1 hit die")
	}
	if spend > sh.HitDice {
		return 0, fmt.Errorf("only %d hit dice remaining", sh.HitDice)
	}
	if roll == nil {
		roll = CryptoHitDie
	}
	missing := sh.HPMax - sh.HP
	var total int64
	for i := int64(0); i < spend; i++ {
		r, err := roll(sh.HitDie)
		if err != nil {
			return 0, err
		}
		got := r + sh.ConMod()
		if got < 0 {
			got = 0
		}
		total += got
	}
	if total > missing {
		total = missing
	}
	sh.HitDice -= spend
	sh.HP += total
	return total, nil
}

// LongRest restores full HP, half hit dice (min 1), all slots, and clears
// death saves (SRD long-rest recovery).
func (sh *Sheet) LongRest() {
	sh.HP = sh.HPMax
	back := sh.HitDiceMax / 2
	if back < 1 {
		back = 1
	}
	sh.HitDice += back
	if sh.HitDice > sh.HitDiceMax {
		sh.HitDice = sh.HitDiceMax
	}
	for n, mx := range sh.SlotsMax {
		sh.Slots[n] = mx
	}
	sh.DeathSucc, sh.DeathFail = 0, 0
}

// LevelUp advances one level: +1 hit die, HP max + average (die/2+1+Con,
// min 1), current HP rises by the same (new vitality, never overheal past
// the new max by construction). Caster slot tables are pack data (H1
// follow-up: structured class tables); the timebox roster records none.
func (sh *Sheet) LevelUp(class *ClassDef) error {
	if sh.Level >= 20 {
		return fmt.Errorf("already level 20")
	}
	gain := sh.HitDie/2 + 1 + sh.ConMod()
	if gain < 1 {
		gain = 1
	}
	sh.Level++
	sh.HitDiceMax++
	sh.HitDice++
	sh.HPMax += gain
	if sh.HPMax > 999 {
		sh.HPMax = 999
	}
	sh.HP += gain
	if sh.HP > sh.HPMax {
		sh.HP = sh.HPMax
	}
	_ = class // caster slot tables are pack data (structured class tables are an H1 follow-up)
	return nil
}

// TouchRecovery runs the H2 seam for a rest intent so hook modifiers compose
// here. Recovery hooks are best-effort bonuses: an intent the active base
// never declared (e.g. level-up on the timebox dnd base) is skipped, never
// a 500 — the sheet flow itself is the source of truth for HP/hit-dice math.
// Genuine evaluation failures still propagate.
func TouchRecovery(ctx context.Context, ev RecoveryEvaluator, intent, actor string) error {
	if ev == nil {
		return nil
	}
	_, err := ev.Evaluate(ctx, ruleset.Intent{Intent: intent, Actor: actor})
	if err != nil && errors.Is(err, ruleset.ErrUnknownIntent) {
		return nil
	}
	return err
}

// ---------------------------------------------------------------------------
// Surgical nested edits (preserve comments/order — pitfalls)
// ---------------------------------------------------------------------------

// setSheetValue sets one scalar inside the `sheet:` frontmatter map,
// preserving every other byte (comments, order, quoting of untouched lines).
// Missing keys are inserted directly under `sheet:`; a missing `sheet:`
// block (or missing fence) is created. Flow-style `sheet: {...}` blocks are
// refused (ok=false: fail closed, never corrupt) — the Advanced editor
// rewrites those wholesale instead.
func setSheetValue(content, key, value string) (string, bool) {
	lines := splitKeepEnds(content)
	fmStart, fmEnd := -1, -1
	if len(lines) > 0 && trimEOL(strings.TrimRight(lines[0], " \t\r\n")) == "---" {
		for i := 1; i < len(lines); i++ {
			t := strings.TrimRight(trimEOL(lines[i]), " \t")
			if t == "---" || t == "..." {
				fmStart, fmEnd = 0, i
				break
			}
		}
	}
	if fmStart < 0 {
		return "---\nsheet:\n  " + key + ": " + value + "\n---\n" + content, true
	}
	sheetLine := -1
	sheetIndent := ""
	for i := fmStart + 1; i < fmEnd; i++ {
		body := trimEOL(lines[i])
		trimmed := strings.TrimLeft(body, " \t")
		if strings.HasPrefix(body, " ") || strings.HasPrefix(body, "\t") {
			continue
		}
		if trimmed == "sheet:" || strings.HasPrefix(trimmed, "sheet:") {
			rest := strings.TrimSpace(strings.TrimPrefix(trimmed, "sheet:"))
			if rest == "" || strings.HasPrefix(rest, "#") {
				sheetLine = i
				sheetIndent = body[:len(body)-len(trimmed)]
				break
			}
			return content, false // flow-style sheet: refuse surgical edit
		}
	}
	eol := "\n"
	if fmEnd > fmStart+1 {
		eol = fmLineEnd(lines[fmStart+1])
	}
	if sheetLine < 0 {
		head := append([]string{}, lines[:fmStart+1]...)
		head = append(head, "sheet:"+eol, "  "+key+": "+value+eol)
		return strings.Join(append(head, lines[fmStart+1:]...), ""), true
	}
	// Scan the sheet block: deeper-indented scalar lines only.
	for i := sheetLine + 1; i < fmEnd; i++ {
		body := trimEOL(lines[i])
		if strings.TrimSpace(body) == "" || strings.HasPrefix(strings.TrimSpace(body), "#") {
			continue
		}
		indent := body[:len(body)-len(strings.TrimLeft(body, " \t"))]
		if len(indent) <= len(sheetIndent) {
			// Left the sheet block: insert before this line.
			ins := sheetIndent + "  " + key + ": " + value + eol
			out := append([]string{}, lines[:i]...)
			out = append(out, ins)
			return strings.Join(append(out, lines[i:]...), ""), true
		}
		trimmed := strings.TrimSpace(body)
		if strings.HasPrefix(trimmed, "- ") || trimmed == "-" {
			continue // sequences never hold sheet scalars: skip
		}
		j := strings.IndexByte(trimmed, ':')
		if j < 0 {
			continue
		}
		if strings.TrimSpace(trimmed[:j]) == key {
			// Replace value in place, preserving a trailing comment.
			after := trimmed[j+1:]
			comment := ""
			if idx := strings.Index(after, " #"); idx >= 0 {
				comment = after[idx:]
			}
			lines[i] = indent + key + ": " + value + comment + fmLineEnd(lines[i])
			return strings.Join(lines, ""), true
		}
	}
	// Insert after the last consecutive sheet-block line (never after a
	// trailing top-level key — that would reparent the value).
	end := sheetLine + 1
	for end < fmEnd {
		body := trimEOL(lines[end])
		if strings.TrimSpace(body) == "" || strings.HasPrefix(strings.TrimSpace(body), "#") {
			end++
			continue
		}
		indent := body[:len(body)-len(strings.TrimLeft(body, " \t"))]
		if len(indent) <= len(sheetIndent) {
			break
		}
		end++
	}
	ins := sheetIndent + "  " + key + ": " + value + eol
	out := append([]string{}, lines[:end]...)
	out = append(out, ins)
	return strings.Join(append(out, lines[end:]...), ""), true
}

// formatSheetScalar renders a sheet scalar for frontmatter (ints bare,
// strings quoted when needed).
func formatSheetScalar(v any) string {
	switch t := v.(type) {
	case int64:
		return fmt.Sprintf("%d", t)
	case int:
		return fmt.Sprintf("%d", t)
	case string:
		return quoteYAML(t)
	default:
		return quoteYAML(fmt.Sprintf("%v", v))
	}
}

// applySheetMap writes every entry of m into content via setSheetValue
// (used by rest/level flows: only changed keys are passed by callers).
// Flow-style sheets fail closed with ok=false.
func applySheetMap(content string, m map[string]any) (string, bool) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		var ok bool
		content, ok = setSheetValue(content, k, formatSheetScalar(m[k]))
		if !ok {
			return content, false
		}
	}
	return content, true
}
