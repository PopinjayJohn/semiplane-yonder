package web

// Ruleset pack stub (Lane I1, Phase 3).
//
// STUB(H1): Lane H1 owns base/overlay/homebrew packs as vault files under
// rules/ plus campaign.yaml validation of names/optionals. H1 has not landed
// yet (fixtures/rules/ is empty on this branch), so the wizard consumes this
// minimal in-code shape derived from fixtures/srd + p05 (Fighter +
// SRD ancestry/background names, display-only versions per p05 "recorded,
// never enforced").
//
// Swap contract: when H1 lands, replace StubPack with a vault-backed loader
// returning this same Pack shape. Wizard + sheet call sites use only Pack /
// ClassDef below, so no handler changes are needed. Dependency noted in the
// lane report.

import (
	"fmt"
	"strings"
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
// (mechanics-free display strings).
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
