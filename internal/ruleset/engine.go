package ruleset

import (
	"context"
)

// NewEngine creates a new ruleset engine.
func NewEngine() *Engine {
	return &Engine{}
}

// Engine is the ruleset evaluation engine.
// Core P12 = generic dice engine + transport; evaluator is a dumb engine, bases supply the programs.
// Hand-rolled arithmetic/boolean engine in core executing base data rules.
// Bounds: depth 32, 256 literals, checked 64-bit.
//
//nolint:unused // Phase-0 stub: stack consumed by Lane H2 Evaluate in Phase 3.
type Engine struct {
	base      *Ruleset
	overlay   *Ruleset
	homebrew  []*Ruleset
	optionals map[string]bool
}

// LoadBase loads a base ruleset (dnd, coc, etc.).
// Phase 0c: sources are vault packs under rules/ (installed via template
// import, never bundled in the binary); this method only registers them.
func (e *Engine) LoadBase(rs *Ruleset) error {
	return nil // not implemented
}

// LoadOverlay loads an overlay ruleset (5e-2014, 5e-2024, etc.).
func (e *Engine) LoadOverlay(rs *Ruleset) error {
	return nil // not implemented
}

// LoadHomebrew loads a homebrew ruleset.
func (e *Engine) LoadHomebrew(rs *Ruleset) error {
	return nil // not implemented
}

// EnableFeature enables an optional feature.
func (e *Engine) EnableFeature(id string) error {
	return nil // not implemented
}

// DisableFeature disables an optional feature.
func (e *Engine) DisableFeature(id string) error {
	return nil // not implemented
}

// Evaluate evaluates an intent against the active ruleset stack.
// Intent envelope: {intent, actor, targets, tool, context}
// Hooks: intent + phase (pre-roll/post-roll/interpret)
// Layers compose: base→overlay→homebrew (damage: sum, advantage: cancel, crit: narrowest-wins)
// Full modifier list pinned in log.
func (e *Engine) Evaluate(ctx context.Context, intent Intent) (*Modifiers, error) {
	return nil, nil // not implemented
}

// Lint validates a ruleset (unknown keys fail, warns on likely non-SRD text).
func (e *Engine) Lint(rs *Ruleset) ([]LintIssue, error) {
	return nil, nil // not implemented
}

// LintIssue represents a lint issue.
type LintIssue struct {
	Severity string // "error", "warn"
	Path     string
	Message  string
}
