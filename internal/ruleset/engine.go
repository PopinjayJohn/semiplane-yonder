package ruleset

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// NewEngine creates a new ruleset engine.
func NewEngine() *Engine {
	return &Engine{optionals: map[string]bool{}}
}

// Engine is the ruleset evaluation engine: a stateful stack
// (LoadBase/LoadOverlay/LoadHomebrew + optionals) over the hand-rolled
// integer/boolean evaluator. The evaluator performs no I/O; all inputs
// come from the loaded packs and the intent envelope.
//
// Mid-session toggles (LoadOverlay, LoadHomebrew, Enable/DisableFeature)
// affect future Evaluate calls only: history pins its modifier list in the
// dice log (pitfalls: GM toggles affect future rolls/views only).
//
// Engine is not safe for concurrent use; callers serialize access
// (single writer discipline, spec §2).
type Engine struct {
	base      *Ruleset
	overlay   *Ruleset
	homebrew  []*Ruleset
	optionals map[string]bool
	catalog   *Catalog
	// validateNotation optionally validates dice notations on load
	// (provided by the dice package; nil skips the parse check).
	validateNotation func(string) error
}

// SetNotationValidator plugs dice-notation validation into load/lint
// without creating a ruleset↔dice import cycle.
func (e *Engine) SetNotationValidator(fn func(string) error) {
	e.validateNotation = fn
}

// LoadBase loads a base ruleset (dnd, coc, etc.).
// Phase 0c: sources are vault packs under rules/ (installed via template
// import, never bundled in the binary); this method only registers them.
// Lint errors fail the load; warnings do not.
func (e *Engine) LoadBase(rs *Ruleset) error {
	if rs == nil {
		return fmt.Errorf("ruleset: nil base")
	}
	if rs.Type != "base" {
		return fmt.Errorf("ruleset: LoadBase needs type base, got %q", rs.Type)
	}
	if issues := LintErrors(LintRuleset(rs, e.validateNotation)); len(issues) > 0 {
		return fmt.Errorf("ruleset: base %q fails lint: %s", rs.ID, firstIssue(issues))
	}
	if err := e.checkHookRequires(rs); err != nil {
		return err
	}
	e.base = rs
	e.catalog = NewCatalog(rs.ID, rs.IntentDefs)
	for id, of := range rs.Optionals {
		if _, ok := e.optionals[id]; !ok {
			e.optionals[id] = of.Default
		}
	}
	return nil
}

// LoadOverlay loads an overlay ruleset (5e-2014, 5e-2024, etc.).
// The overlay must name the loaded base as its parent. Switching overlays
// needs no rebuild: it only affects future evaluations.
func (e *Engine) LoadOverlay(rs *Ruleset) error {
	if rs == nil {
		return fmt.Errorf("ruleset: nil overlay")
	}
	if e.base == nil {
		return fmt.Errorf("ruleset: load a base before an overlay")
	}
	if rs.Type != "overlay" {
		return fmt.Errorf("ruleset: LoadOverlay needs type overlay, got %q", rs.Type)
	}
	if rs.ParentID != e.base.ID {
		return fmt.Errorf("ruleset: overlay %q targets base %q, loaded base is %q",
			rs.ID, rs.ParentID, e.base.ID)
	}
	if issues := LintErrors(LintRuleset(rs, e.validateNotation)); len(issues) > 0 {
		return fmt.Errorf("ruleset: overlay %q fails lint: %s", rs.ID, firstIssue(issues))
	}
	if err := e.checkHookRequires(rs); err != nil {
		return err
	}
	e.overlay = rs
	e.catalog.Extend(rs.IntentDefs)
	for id, of := range rs.Optionals {
		if _, ok := e.optionals[id]; !ok {
			e.optionals[id] = of.Default
		}
	}
	return nil
}

// LoadHomebrew loads a homebrew diff (never a fork of an overlay).
// Homebrew targets the loaded base or overlay via ParentID.
func (e *Engine) LoadHomebrew(rs *Ruleset) error {
	if rs == nil {
		return fmt.Errorf("ruleset: nil homebrew")
	}
	if e.base == nil {
		return fmt.Errorf("ruleset: load a base before homebrew")
	}
	if rs.Type != "homebrew" {
		return fmt.Errorf("ruleset: LoadHomebrew needs type homebrew, got %q", rs.Type)
	}
	if e.overlay != nil && rs.ParentID != e.overlay.ID && rs.ParentID != e.base.ID {
		return fmt.Errorf("ruleset: homebrew %q targets %q, loaded stack is %q + %q",
			rs.ID, rs.ParentID, e.base.ID, e.overlay.ID)
	}
	if e.overlay == nil && rs.ParentID != "" && rs.ParentID != e.base.ID {
		return fmt.Errorf("ruleset: homebrew %q targets %q, loaded base is %q",
			rs.ID, rs.ParentID, e.base.ID)
	}
	if issues := LintErrors(LintRuleset(rs, e.validateNotation)); len(issues) > 0 {
		return fmt.Errorf("ruleset: homebrew %q fails lint: %s", rs.ID, firstIssue(issues))
	}
	if err := e.checkHookRequires(rs); err != nil {
		return err
	}
	e.homebrew = append(e.homebrew, rs)
	e.catalog.Extend(rs.IntentDefs)
	for id, of := range rs.Optionals {
		if _, ok := e.optionals[id]; !ok {
			e.optionals[id] = of.Default
		}
	}
	return nil
}

// EnableFeature enables an optional feature (unknown ids fail).
func (e *Engine) EnableFeature(id string) error {
	if !e.knowsOptional(id) {
		return fmt.Errorf("ruleset: unknown optional feature %q", id)
	}
	e.optionals[id] = true
	return nil
}

// DisableFeature disables an optional feature (unknown ids fail).
func (e *Engine) DisableFeature(id string) error {
	if !e.knowsOptional(id) {
		return fmt.Errorf("ruleset: unknown optional feature %q", id)
	}
	e.optionals[id] = false
	return nil
}

// FeatureEnabled reports the toggle state (false for unknown ids).
func (e *Engine) FeatureEnabled(id string) bool { return e.optionals[id] }

// checkHookRequires fails a pack whose hooks gate on unknown optional ids
// (checked against already-loaded packs plus the pack itself, so overlays
// may use base-defined optionals). A typo'd gate must never silently
// disable a hook.
func (e *Engine) checkHookRequires(rs *Ruleset) error {
	for _, key := range sortedKeysOf(rs.IntentDefs) {
		for i, h := range rs.IntentDefs[key].Hooks {
			if h.Requires == "" {
				continue
			}
			if _, ok := rs.Optionals[h.Requires]; ok {
				continue
			}
			if e.knowsOptional(h.Requires) {
				continue
			}
			return fmt.Errorf("ruleset: %s intent %q hook[%d] requires unknown optional %q",
				rs.Type, key, i, h.Requires)
		}
	}
	return nil
}

func (e *Engine) knowsOptional(id string) bool {
	for _, rs := range e.stack() {
		if rs == nil {
			continue
		}
		if _, ok := rs.Optionals[id]; ok {
			return true
		}
	}
	return false
}

func (e *Engine) stack() []*Ruleset {
	out := []*Ruleset{e.base, e.overlay}
	return append(out, e.homebrew...)
}

func layerName(i int) string {
	switch i {
	case 0:
		return "base"
	case 1:
		return "overlay"
	default:
		return "homebrew"
	}
}

// Evaluate evaluates an intent against the active ruleset stack.
// Intent envelope: {intent, actor, targets, tool, context}
// Hooks: intent + phase (pre-roll/post-roll/interpret)
// Layers compose: base→overlay→homebrew (damage: sum, advantage: cancel,
// crit: narrowest-wins). The full ordered modifier list is returned (the
// transport pins it in the log); the composed view is in Effective.
//
// Hook scripts evaluate against the intent scope (numeric/bool context
// values plus targets_count). A script referencing an absent context key
// fails the evaluation: missing context never defaults (fail closed, never
// silently ignore a typo'd key); callers pass explicit values.
//
// Pre-roll hooks evaluate now. Post-roll/interpret hooks are returned as
// deferred entries (Modifiers nil, script in Metadata); the caller runs
// ApplyPostRoll once the roll exists. Intent names validate against the
// active base catalog, never a global list.
func (e *Engine) Evaluate(ctx context.Context, intent Intent) (*Modifiers, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if e.base == nil {
		return nil, fmt.Errorf("ruleset: no base loaded")
	}
	if err := e.catalog.ValidateIntent(intent.Intent); err != nil {
		return nil, err
	}
	start := time.Now()
	scope, err := ScopeFromContext(intent.Context)
	if err != nil {
		return nil, err
	}
	scope["targets_count"] = IntValue(int64(len(intent.Targets)))

	perLayer := make([]*Modifiers, 0, len(e.stack()))
	maxLiterals, maxDepth := 0, 0
	for i, rs := range e.stack() {
		if rs == nil {
			continue
		}
		def, ok := rs.IntentDefs[intent.Intent]
		if !ok {
			continue // layer adds no hooks for this intent
		}
		layer := layerName(i)
		lm := &Modifiers{}
		for hi, h := range def.Hooks {
			if h.Requires != "" && !e.optionals[h.Requires] {
				continue // optional-gated hook, toggled off
			}
			if h.Phase != PhasePreRoll {
				lm.PostRollHooks = append(lm.PostRollHooks, deferHook(h, layer, hi, intent.Intent))
				continue
			}
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			n, err := ParseExpression(h.Script)
			if err != nil {
				return nil, fmt.Errorf("ruleset: %s hook %q: %w", layer, hookID(h, hi), err)
			}
			if n.literals > maxLiterals {
				maxLiterals = n.literals
			}
			if n.depth > maxDepth {
				maxDepth = n.depth
			}
			val, err := n.eval(scope)
			if err != nil {
				return nil, fmt.Errorf("ruleset: %s hook %q: %w", layer, hookID(h, hi), err)
			}
			mod, keep := hookModifier(h, val)
			if !keep {
				continue
			}
			mod.Source = layer + ":" + hookID(h, hi)
			if mod.Reason == "" {
				mod.Reason = hookID(h, hi)
			}
			lm.NumericModifiers = append(lm.NumericModifiers, mod)
		}
		// Conditional power lives in hook scripts (ternary and if()):
		// ConditionalModifiers on the output stay data for the sheet/VTT
		// layer, which resolves them against live sheet state the core
		// evaluator never sees.
		lm.Effective = Effective{ScopeBonus: map[string]int64{}}
		perLayer = append(perLayer, lm)
	}
	out := ComposeModifiers(perLayer...)
	out.Metadata = EvaluationMetadata{
		RulesetVersion: e.base.Version,
		BaseID:         e.base.ID,
		HomebrewIDs:    e.homebrewIDs(),
		Depth:          maxDepth,
		Literals:       maxLiterals,
		DurationMicros: time.Since(start).Microseconds(),
	}
	if e.overlay != nil {
		out.Metadata.OverlayID = e.overlay.ID
	}
	return out, nil
}

// ApplyPostRoll evaluates deferred post-roll/interpret hooks with roll
// variables merged over the intent scope. rollVars carries roll outputs
// (convention: "total", "nat" for the natural d20, "crit" 0/1...). It
// returns the hook results in layer order; the caller appends them to the
// pinned modifier list and recomposes via ComposeModifiers.
func (e *Engine) ApplyPostRoll(ctx context.Context, intent Intent, pending []HookResult, rollVars map[string]Value) ([]HookResult, *Modifiers, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	scope, err := ScopeFromContext(intent.Context)
	if err != nil {
		return nil, nil, err
	}
	scope["targets_count"] = IntValue(int64(len(intent.Targets)))
	for k, v := range rollVars {
		if isValidIdent(k) {
			scope[k] = v
		}
	}
	var done []HookResult
	extra := &Modifiers{}
	for _, p := range pending {
		if p.Metadata["deferred"] != true {
			done = append(done, p)
			continue
		}
		script, _ := p.Metadata["script"].(string)
		kind, _ := p.Metadata["kind"].(string)
		reason, _ := p.Metadata["reason"].(string)
		if script == "" {
			continue
		}
		n, err := ParseExpression(script)
		if err != nil {
			return nil, nil, fmt.Errorf("ruleset: deferred hook %q: %w", p.HookID, err)
		}
		val, err := n.eval(scope)
		if err != nil {
			return nil, nil, fmt.Errorf("ruleset: deferred hook %q: %w", p.HookID, err)
		}
		mod, keep := hookModifier(HookDef{Kind: kind, Reason: reason}, val)
		if !keep {
			continue
		}
		mod.Source = p.Layer + ":" + p.HookID
		if mod.Reason == "" {
			mod.Reason = p.HookID
		}
		extra.NumericModifiers = append(extra.NumericModifiers, mod)
		p.Modifiers = &Modifiers{
			NumericModifiers: []NumericModifier{mod},
			Effective:        Effective{ScopeBonus: map[string]int64{}},
		}
		delete(p.Metadata, "deferred")
		done = append(done, p)
	}
	composed := ComposeModifiers(extra)
	return done, composed, nil
}

func (e *Engine) homebrewIDs() []string {
	ids := make([]string, 0, len(e.homebrew))
	for _, h := range e.homebrew {
		ids = append(ids, h.ID)
	}
	sort.Strings(ids)
	return ids
}

func hookID(h HookDef, idx int) string {
	if h.ID != "" {
		return h.ID
	}
	return fmt.Sprintf("%s-%s-%d", h.Layer, h.Phase, idx)
}

// hookModifier maps a hook script result to a numeric modifier.
// Scalar ints apply (zero bonuses are noise and skipped); bools apply when
// true (for advantage/disadvantage/crit kinds, nonzero ints also apply).
// Lists and other shapes are errors: hooks produce scalars.
func hookModifier(h HookDef, val Value) (NumericModifier, bool) {
	kind := h.Kind
	if kind == "" {
		kind = ModBonus
	}
	applies := "all"
	mod := NumericModifier{Type: kind, Reason: h.Reason, Applies: applies}
	switch kind {
	case ModAdvantage:
		switch val.Kind() {
		case KindBool:
			b, _ := val.Bool()
			if !b {
				return mod, false
			}
			mod.Value = 1
			return mod, true
		case KindInt:
			i, _ := val.Int()
			if i == 0 {
				return mod, false
			}
			mod.Value = 1
			return mod, true
		default:
			return mod, false
		}
	case ModDisadvantage:
		switch val.Kind() {
		case KindBool:
			b, _ := val.Bool()
			if !b {
				return mod, false
			}
			mod.Value = -1
			return mod, true
		case KindInt:
			i, _ := val.Int()
			if i == 0 {
				return mod, false
			}
			mod.Value = -1
			return mod, true
		default:
			return mod, false
		}
	case ModBonus, ModPenalty, ModCritRange:
		i, ok := val.Int()
		if !ok {
			return mod, false
		}
		if i == 0 {
			return mod, false
		}
		mod.Value = i
		return mod, true
	default:
		return mod, false
	}
}

// deferHook packages a non-pre-roll hook as a pending HookResult. The script
// travels in Metadata so the pinned log records what will run; ApplyPostRoll
// executes it once roll variables exist.
func deferHook(h HookDef, layer string, idx int, intent string) HookResult {
	id := hookID(h, idx)
	return HookResult{
		HookID: id,
		Phase:  h.Phase,
		Layer:  layer,
		Metadata: map[string]any{
			"deferred": true,
			"script":   h.Script,
			"kind":     h.Kind,
			"reason":   h.Reason,
			"intent":   intent,
		},
	}
}

// Lint validates a ruleset (unknown keys fail, warns on likely non-SRD text).
func (e *Engine) Lint(rs *Ruleset) ([]LintIssue, error) {
	if rs == nil {
		return nil, fmt.Errorf("ruleset: nil ruleset")
	}
	return LintRuleset(rs, e.validateNotation), nil
}

func firstIssue(issues []LintIssue) string {
	if len(issues) == 0 {
		return "unknown error"
	}
	return issues[0].Path + ": " + issues[0].Message
}
