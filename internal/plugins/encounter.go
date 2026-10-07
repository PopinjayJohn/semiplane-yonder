package plugins

import (
	"fmt"
	"strings"
)

// Encounter builder (P07): pick compendium monsters → builds the encounter →
// emits a spawn INTENT. Lane K executes the intent (writes tokens +
// initiative in one action). Also: mass HP adjust + loot drop into the
// session note. No vault/DB writes here — pure intent construction.

// MaxEncounterCreatures caps one spawn intent (transport sanity bound).
const MaxEncounterCreatures = 20

// MonsterRef is one compendium pick (name/HP already resolved by the caller
// from the ruleset packs; I2 does no ruleset interpretation).
type MonsterRef struct {
	ID    string // compendium id (e.g. "goblin")
	Name  string // display name
	HP    int    // per-creature max HP
	Count int    // how many to spawn
}

// Encounter is a built encounter awaiting a spawn intent.
type Encounter struct {
	ID       string
	Name     string
	Monsters []MonsterRef
	Size     int // total creatures
}

// BuildEncounter validates picks and builds the encounter: non-empty,
// positive counts/HP, within the creature cap.
func BuildEncounter(id, name string, picks []MonsterRef) (Encounter, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(name) == "" {
		return Encounter{}, &PluginError{Code: "BAD_ENCOUNTER", Message: "id and name required"}
	}
	var mons []MonsterRef
	size := 0
	for _, p := range picks {
		if p.Count <= 0 {
			continue
		}
		if p.HP <= 0 {
			return Encounter{}, &PluginError{Code: "BAD_ENCOUNTER", Message: p.Name + ": HP must be positive"}
		}
		mons = append(mons, p)
		size += p.Count
	}
	if len(mons) == 0 {
		return Encounter{}, &PluginError{Code: "BAD_ENCOUNTER", Message: "no creatures picked"}
	}
	if size > MaxEncounterCreatures {
		return Encounter{}, &PluginError{Code: "BAD_ENCOUNTER", Message: fmt.Sprintf("%d creatures exceeds cap %d", size, MaxEncounterCreatures)}
	}
	return Encounter{ID: id, Name: name, Monsters: mons, Size: size}, nil
}

// SpawnToken is one token write inside a spawn intent.
type SpawnToken struct {
	TokenID   string // map-unique token id (caller-namespaced)
	MonsterID string
	Name      string
	HP        int
	MaxHP     int
	X         int
	Y         int
}

// SpawnIntent is the single action Lane K executes: write all tokens +
// initiative ordering at once (P07/P08 run-mode hook).
type SpawnIntent struct {
	Intent      string // always SpawnIntentName
	EncounterID string
	MapID       string
	Tokens      []SpawnToken
	Order       []string // initiative order (token ids)
}

// SpawnIntentName is the intent name Lane K executes.
const SpawnIntentName = "encounter.spawn"

// EmitSpawnIntent lowers an encounter to a spawn intent. Token ids are
// "<encounterID>-<n>" (stable, resumable); initial order follows spawn order
// (the initiative-tracker re-sorts on rolled values later).
func EmitSpawnIntent(enc Encounter, mapID string) (SpawnIntent, error) {
	if strings.TrimSpace(mapID) == "" {
		return SpawnIntent{}, &PluginError{Code: "BAD_SPAWN", Message: "map id required"}
	}
	var tokens []SpawnToken
	var order []string
	n := 0
	for _, m := range enc.Monsters {
		for i := 0; i < m.Count; i++ {
			n++
			id := fmt.Sprintf("%s-%d", enc.ID, n)
			tokens = append(tokens, SpawnToken{
				TokenID: id, MonsterID: m.ID, Name: m.Name,
				HP: m.HP, MaxHP: m.HP, X: (n - 1) % 8, Y: (n - 1) / 8,
			})
			order = append(order, id)
		}
	}
	if len(tokens) == 0 {
		return SpawnIntent{}, &PluginError{Code: "BAD_SPAWN", Message: "empty encounter"}
	}
	return SpawnIntent{Intent: SpawnIntentName, EncounterID: enc.ID, MapID: mapID, Tokens: tokens, Order: order}, nil
}

// AdjustHP applies a mass HP delta to token HP values (heal/harm), flooring
// at 0 and capping at MaxHP. Pure helper; persistence is Lane K's write.
func AdjustHP(tokens []TokenHP, delta int) []TokenHP {
	out := make([]TokenHP, 0, len(tokens))
	for _, t := range tokens {
		t.HP += delta
		if t.HP < 0 {
			t.HP = 0
		}
		if t.HP > t.MaxHP {
			t.HP = t.MaxHP
		}
		out = append(out, t)
	}
	return out
}

// LootDropNote renders the loot-drop markdown appended to the session note
// (the write itself goes through the write path, never here).
func LootDropNote(enc Encounter, loot []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Loot — %s\n\n", enc.Name)
	if len(loot) == 0 {
		b.WriteString("No loot.\n")
		return b.String()
	}
	for _, l := range loot {
		fmt.Fprintf(&b, "- [ ] %s\n", l)
	}
	return b.String()
}
