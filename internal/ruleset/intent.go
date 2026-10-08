package ruleset

import (
	"errors"
	"fmt"
	"sort"
)

// ErrUnknownIntent marks an intent name outside the active base catalog.
// Returned wrapped by Catalog.ValidateIntent (and therefore by
// Engine.Evaluate for undeclared intents).
var ErrUnknownIntent = errors.New("ruleset: unknown intent")

// Hook phases (frozen vocabulary). A hook is an intent + phase: it receives
// the intent plus the current modifier list and returns {source, label,
// value} additions.
const (
	PhasePreRoll   = "pre-roll"
	PhasePostRoll  = "post-roll"
	PhaseInterpret = "interpret"
)

// HookPhases lists the valid phases in execution order.
var HookPhases = []string{PhasePreRoll, PhasePostRoll, PhaseInterpret}

// IsHookPhase reports whether phase is a valid hook phase.
func IsHookPhase(phase string) bool {
	return phase == PhasePreRoll || phase == PhasePostRoll || phase == PhaseInterpret
}

// Modifier kinds (frozen vocabulary for NumericModifier.Type).
// Unknown kinds fail rules lint — bases invent new semantics via intents
// and hook scripts, never via ad-hoc type strings.
const (
	ModBonus        = "bonus"        // flat numeric add (stacking: sum per scope)
	ModPenalty      = "penalty"      // flat numeric subtract (stacking: sum per scope)
	ModAdvantage    = "advantage"    // stacking: cancel vs disadvantage
	ModDisadvantage = "disadvantage" // stacking: cancel vs advantage
	ModCritRange    = "crit-range"   // Value = lowest roll that crits; stacking: narrowest wins
)

// IsModifierKind reports whether kind is a known numeric modifier kind.
func IsModifierKind(kind string) bool {
	switch kind {
	case ModBonus, ModPenalty, ModAdvantage, ModDisadvantage, ModCritRange:
		return true
	}
	return false
}

// Dice modifier kinds (frozen vocabulary for DiceModifier.Type).
const (
	DiceAdd     = "add"
	DiceRemove  = "remove"
	DiceReplace = "replace"
	DiceExplode = "explode"
	DiceReroll  = "reroll"
)

// IsDiceModifierKind reports whether kind is a known dice modifier kind.
func IsDiceModifierKind(kind string) bool {
	switch kind {
	case DiceAdd, DiceRemove, DiceReplace, DiceExplode, DiceReroll:
		return true
	}
	return false
}

// Catalog is the per-base intent catalog. Intent names are validated against
// the active base, never against a global list: a new base brings a new
// catalog. Overlays and homebrew extend the base catalog with new intents.
type Catalog struct {
	baseID  string
	intents map[string]IntentDef
	order   []string // registration order (deterministic iteration)
}

// NewCatalog builds a catalog for baseID from defs.
func NewCatalog(baseID string, defs map[string]IntentDef) *Catalog {
	c := &Catalog{baseID: baseID, intents: map[string]IntentDef{}}
	c.Extend(defs)
	return c
}

// Extend adds overlay/homebrew intents to the catalog (or replaces entries
// with the same key — layers compose base→overlay→homebrew).
func (c *Catalog) Extend(defs map[string]IntentDef) {
	keys := make([]string, 0, len(defs))
	for k := range defs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if _, ok := c.intents[k]; !ok {
			c.order = append(c.order, k)
		}
		c.intents[k] = defs[k]
	}
}

// BaseID returns the owning base id.
func (c *Catalog) BaseID() string { return c.baseID }

// ValidateIntent errors when name is not in the active base catalog.
//
// Gate G3: unknown intents wrap ErrUnknownIntent so recovery flows (which
// probe best-effort hook intents like level-up that a base may never
// declare) can skip cleanly instead of string-matching messages.
func (c *Catalog) ValidateIntent(name string) error {
	if _, ok := c.intents[name]; !ok {
		return fmt.Errorf("ruleset: intent %q not in base %q catalog: %w", name, c.baseID, ErrUnknownIntent)
	}
	return nil
}

// Lookup returns the intent definition (ok=false when absent).
func (c *Catalog) Lookup(name string) (IntentDef, bool) {
	d, ok := c.intents[name]
	return d, ok
}

// Intents returns intent keys in registration order.
func (c *Catalog) Intents() []string {
	return append([]string(nil), c.order...)
}

// Effective is the post-policy summary of a modifier list. The full ordered
// modifier list (with every source + reason) is pinned in the dice log;
// Effective is the composed view the roller consumes.
type Effective struct {
	// ScopeBonus holds summed bonus-minus-penalty per Applies scope
	// ("attack", "damage", "save", ...). Use BonusFor to include "all".
	ScopeBonus map[string]int64
	// Advantage is the net state after cancel: 1 = advantage,
	// -1 = disadvantage, 0 = straight (none, or cancelled out).
	Advantage int
	// AdvSources / DisSources count contributing sources (for display).
	AdvSources int
	DisSources int
	// CritThreshold is the lowest roll that crits; HasCrit reports whether
	// any crit-range modifier applied. Stacking: narrowest wins, i.e. the
	// maximum threshold.
	CritThreshold int64
	HasCrit       bool
}

// BonusFor returns the summed bonus for scope plus the "all" scope.
func (e Effective) BonusFor(scope string) int64 {
	if e.ScopeBonus == nil {
		return 0
	}
	if scope == "all" {
		return e.ScopeBonus["all"]
	}
	return e.ScopeBonus[scope] + e.ScopeBonus["all"]
}

// ComposeModifiers merges per-layer modifier lists in order
// base→overlay→homebrew and applies the per-key stacking policies:
//
//   - damage (and every other bonus/penalty scope): sum.
//   - advantage vs disadvantage: cancel (net count sign; both present with
//     equal weight rolls straight).
//   - crit-range: narrowest wins (maximum threshold value).
//
// The returned Modifiers preserves the full ordered input list (pinned in
// the log per P05) and carries the composed view in Effective. Nil layers
// are skipped. Input lists are never mutated.
func ComposeModifiers(layers ...*Modifiers) *Modifiers {
	out := &Modifiers{Effective: Effective{ScopeBonus: map[string]int64{}}}
	for _, l := range layers {
		if l == nil {
			continue
		}
		out.DiceModifiers = append(out.DiceModifiers, l.DiceModifiers...)
		out.NumericModifiers = append(out.NumericModifiers, l.NumericModifiers...)
		out.ConditionalModifiers = append(out.ConditionalModifiers, l.ConditionalModifiers...)
		out.PreRollHooks = append(out.PreRollHooks, l.PreRollHooks...)
		out.PostRollHooks = append(out.PostRollHooks, l.PostRollHooks...)
		out.InterpretHooks = append(out.InterpretHooks, l.InterpretHooks...)
	}
	adv, dis := 0, 0
	for _, m := range out.NumericModifiers {
		switch m.Type {
		case ModBonus:
			out.Effective.ScopeBonus[m.Applies] += m.Value
		case ModPenalty:
			out.Effective.ScopeBonus[m.Applies] -= m.Value
		case ModAdvantage:
			adv++
			out.Effective.AdvSources++
		case ModDisadvantage:
			dis++
			out.Effective.DisSources++
		case ModCritRange:
			if !out.Effective.HasCrit || m.Value > out.Effective.CritThreshold {
				out.Effective.CritThreshold = m.Value
				out.Effective.HasCrit = true
			}
		default:
			// Unknown kinds fail lint, but composed data must still fail
			// closed at runtime: ignore the entry rather than guessing.
			// (LintRuleset rejects these before they can load.)
		}
	}
	switch {
	case adv > 0 && dis == 0:
		out.Effective.Advantage = 1
	case dis > 0 && adv == 0:
		out.Effective.Advantage = -1
	default:
		out.Effective.Advantage = 0 // straight: none, or cancelled out
	}
	return out
}
