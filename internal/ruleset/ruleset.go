package ruleset

import (
	"context"
)

// Evaluate evaluates an intent against the active ruleset stack (base → overlay → homebrew).
// Returns computed modifiers for the transport layer.
// Intent envelope: {intent, actor, targets, tool, context}
// Hooks: intent + phase (pre-roll, post-roll, interpret).
// Layers compose: base → overlay → homebrew (damage: sum, advantage: cancel, crit: narrowest-wins).
// Full modifier list pinned in log.
func Evaluate(ctx context.Context, intent Intent) (*Modifiers, error) {
	return nil, nil // not implemented
}

// Intent represents a ruleset intent envelope.
type Intent struct {
	Intent   string         // e.g., "attack", "save", "skill", "damage"
	Actor    string         // actor ID (character, NPC, etc.)
	Targets  []string       // target IDs
	Tool     string         // weapon, spell, feature, item ID
	Context  map[string]any // situational context (cover, condition, etc.)
	Metadata map[string]any // transport metadata (blind, replay, etc.)
}

// Modifiers represents the computed modifiers from ruleset evaluation.
type Modifiers struct {
	// Dice modifications
	DiceModifiers []DiceModifier
	// Numeric modifiers (advantage, disadvantage, bonuses, penalties)
	NumericModifiers []NumericModifier
	// Conditional modifiers (if/then rules)
	ConditionalModifiers []ConditionalModifier
	// Hook results from pre-roll phase
	PreRollHooks []HookResult
	// Hook results from post-roll phase
	PostRollHooks []HookResult
	// Hook results from interpret phase
	InterpretHooks []HookResult
	// Evaluation metadata
	Metadata EvaluationMetadata
}

// DiceModifier represents a dice modification (add/remove dice, change faces, etc.).
type DiceModifier struct {
	Source   string // rule ID that produced this
	Type     string // "add", "remove", "replace", "explode", "reroll"
	DiceSpec string // e.g., "2d6", "1d20"
	Reason   string
}

// NumericModifier represents a numeric bonus/penalty.
type NumericModifier struct {
	Source  string
	Type    string // "bonus", "penalty", "advantage", "disadvantage", "crit-range"
	Value   int64  // for bonus/penalty; for advantage: 1=adv, -1=disadv; for crit: min roll
	Reason  string
	Applies string // "attack", "damage", "save", "all"
}

// ConditionalModifier represents a conditional rule.
type ConditionalModifier struct {
	Source    string
	Condition string // expression
	Then      *Modifiers
	Else      *Modifiers
}

// HookResult represents the result of a hook execution.
type HookResult struct {
	HookID    string
	Phase     string // "pre-roll", "post-roll", "interpret"
	Layer     string // "base", "overlay", "homebrew"
	Modifiers *Modifiers
	Metadata  map[string]any
}

// EvaluationMetadata contains metadata about the evaluation.
type EvaluationMetadata struct {
	RulesetVersion string
	BaseID         string
	OverlayID      string
	HomebrewIDs    []string
	Depth          int
	Literals       int
	DurationMicros int64
}

// Ruleset represents a loaded ruleset (base, overlay, or homebrew).
type Ruleset struct {
	ID          string
	Name        string
	Version     string
	Type        string // "base", "overlay", "homebrew"
	ParentID    string // for overlay/homebrew
	StatDefs    map[string]StatDef
	DerivedDefs map[string]DerivedDef
	DiceDefs    map[string]DiceDef
	IntentDefs  map[string]IntentDef
	Optionals   map[string]OptionalFeature
}

// StatDef defines a base stat.
type StatDef struct {
	Key      string
	Name     string
	Type     string // "int", "float", "string", "dice"
	Default  any
	Min      *float64
	Max      *float64
	Secret   bool
	Editable bool
}

// DerivedDef defines a derived stat (computed from base stats).
type DerivedDef struct {
	Key     string
	Name    string
	Formula string // expression
	Depends []string
	Secret  bool
}

// DiceDef defines a named dice expression.
type DiceDef struct {
	Key      string
	Name     string
	Notation string // e.g., "2d6+3"
	Min      int
	Max      int
}

// IntentDef defines an intent catalog entry.
type IntentDef struct {
	Key       string
	Name      string
	Stats     []string
	Dice      []string
	Hooks     []HookDef
	Validates string // validation expression
}

// HookDef defines a hook point.
type HookDef struct {
	Phase  string // "pre-roll", "post-roll", "interpret"
	Layer  string // "base", "overlay", "homebrew"
	Script string // expression
}

// OptionalFeature defines an optional ruleset feature.
type OptionalFeature struct {
	ID          string
	Name        string
	Description string
	Requires    []string
	Conflicts   []string
	Default     bool
}
