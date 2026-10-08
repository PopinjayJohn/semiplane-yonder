package web

// Ruleset packs (gate G3: vault-backed via H1's resolver).
//
// Base/overlay identity comes from vault truth on every call:
// campaign.Resolve reads campaign.yaml + pack headers live with no cache,
// so a GM overlay switch is a file edit — no rebuild, no restart, no index
// touch. Class/ancestry/background rosters are still the timebox roster
// (Fighter + SRD display names): H1 packs carry no structured class tables
// in v1 (compendium entries are prose), so the roster is code-defined and
// clearly marked below — an H1 follow-up, not vault truth. Handler call
// sites use only Pack / ClassDef, so the roster upgrade needs no handler
// changes when structured tables land.
//
// Fallback (marked): vaults without packs (fresh `init --bare`, unit
// tests) resolve nothing — StubPack keeps every flow working on the
// timebox roster with display-only versions.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/semiplane/yonder/internal/campaign"
	"github.com/semiplane/yonder/internal/ruleset"
)

// ClassDef is the minimal playable-class shape the wizard and level flow
// need: hit die for HP/hit-dice math and a spellcaster flag for slot rows.
type ClassDef struct {
	ID          string
	Name        string
	HitDie      int // faces: 6/8/10/12
	Spellcaster bool
}

// Pack is the minimal H1 pack shape: display-only versions plus the
// timebox roster (Fighter playable; SRD names for ancestry/background
// steps only — no invented mechanics).
type Pack struct {
	Base        string
	Overlay     string
	Classes     map[string]*ClassDef
	Ancestries  []string
	Backgrounds []string
}

// StubPack returns the timebox pack: Fighter (d10, SRD 5.2 CC-BY) with
// ancestry/background name lists sampled from fixtures/srd filenames
// (mechanics-free display strings). FALLBACK ONLY: pack-less vaults and
// unit tests. Packed vaults resolve through LoadPack.
func StubPack() *Pack {
	return &Pack{
		Base:    "dnd",
		Overlay: "srd-5e-2014",
		Classes: map[string]*ClassDef{
			"fighter": {ID: "fighter", Name: "Fighter", HitDie: 10},
		},
		Ancestries:  []string{"human", "elf", "dwarf", "halfling"},
		Backgrounds: []string{"acolyte", "criminal", "sage", "soldier"},
	}
}

// LoadPack resolves the vault's ruleset stack through H1 and returns the
// wizard/sheet Pack shape: base/overlay ids from vault truth, timebox
// roster for classes/ancestries/backgrounds (see file header). Resolution
// failures (no campaign.yaml, no packs) fall back to StubPack — never nil,
// never an error — so pack-less vaults keep serving.
func LoadPack(vaultRoot string) *Pack {
	p := StubPack()
	if vaultRoot == "" {
		return p
	}
	st, err := campaign.Resolve(vaultRoot)
	if err != nil {
		return p
	}
	if st.Base != nil {
		p.Base = st.Base.ID
	}
	if st.Overlay != nil {
		p.Overlay = st.Overlay.ID
	} else {
		p.Overlay = ""
	}
	return p
}

// vaultEngine is the served RecoveryEvaluator: it rebuilds the H2 engine
// from vault truth on every Evaluate (campaign.Resolve reads live disk, no
// cache), so overlay/feature switches apply to future rolls with no
// restart. A stack that fails to load falls back to empty modifiers with a
// loud log — sheets keep working on file truth (marked fallback; same rule
// as LoadVaultStack's contract).
type vaultEngine struct {
	vaultRoot string
}

// Evaluate implements RecoveryEvaluator over a freshly resolved stack.
func (v vaultEngine) Evaluate(ctx context.Context, intent ruleset.Intent) (*ruleset.Modifiers, error) {
	e, err := ruleset.LoadVaultStack(v.vaultRoot)
	if err != nil {
		slog.Warn("ruleset stack unavailable, recovery hooks off", "err", err)
		return StubEvaluator{}.Evaluate(ctx, intent)
	}
	return e.Evaluate(ctx, intent)
}

// Class looks up a class id case-insensitively.
func (p *Pack) Class(id string) (*ClassDef, error) {
	if p == nil {
		return nil, fmt.Errorf("no ruleset pack loaded")
	}
	if c, ok := p.Classes[strings.ToLower(strings.TrimSpace(id))]; ok {
		return c, nil
	}
	return nil, fmt.Errorf("unknown class %q (timebox pack offers: fighter)", id)
}

// HasAncestry / HasBackground validate wizard display-string picks.
func (p *Pack) HasAncestry(a string) bool {
	for _, x := range p.Ancestries {
		if strings.EqualFold(x, strings.TrimSpace(a)) {
			return true
		}
	}
	return false
}

// HasBackground validates wizard display-string picks.
func (p *Pack) HasBackground(b string) bool {
	for _, x := range p.Backgrounds {
		if strings.EqualFold(x, strings.TrimSpace(b)) {
			return true
		}
	}
	return false
}

// StandardArray is the no-help-needed stat method the wizard offers
// (p07: "new player completes wizard with no help").
var StandardArray = []int64{15, 14, 13, 12, 10, 8}

// StatKeys is the fixed ability order used by the wizard stats step.
var StatKeys = []string{"str", "dex", "con", "int", "wis", "cha"}
