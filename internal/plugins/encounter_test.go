package plugins

import (
	"strings"
	"testing"
)

func TestBuildEncounterValidation(t *testing.T) {
	enc, err := BuildEncounter("e1", "Ambush", []MonsterRef{
		{ID: "goblin", Name: "Goblin", HP: 7, Count: 2},
		{ID: "boss", Name: "Boss", HP: 21, Count: 0}, // zero counts skipped
	})
	if err != nil {
		t.Fatal(err)
	}
	if enc.Size != 2 || len(enc.Monsters) != 1 {
		t.Fatalf("encounter = %+v", enc)
	}
	if _, err := BuildEncounter("", "x", []MonsterRef{{ID: "g", Name: "G", HP: 1, Count: 1}}); err == nil {
		t.Error("expected id-required error")
	}
	if _, err := BuildEncounter("e", "x", nil); err == nil {
		t.Error("expected empty error")
	}
	if _, err := BuildEncounter("e", "x", []MonsterRef{{ID: "g", Name: "G", HP: 0, Count: 1}}); err == nil {
		t.Error("expected HP error")
	}
	many := make([]MonsterRef, 0)
	many = append(many, MonsterRef{ID: "g", Name: "G", HP: 1, Count: MaxEncounterCreatures + 1})
	if _, err := BuildEncounter("e", "x", many); err == nil {
		t.Error("expected cap error")
	}
}

func TestEmitSpawnIntent(t *testing.T) {
	enc, _ := BuildEncounter("e1", "Ambush", []MonsterRef{
		{ID: "goblin", Name: "Goblin", HP: 7, Count: 2},
	})
	sp, err := EmitSpawnIntent(enc, "arena")
	if err != nil {
		t.Fatal(err)
	}
	if sp.Intent != SpawnIntentName {
		t.Errorf("intent = %q", sp.Intent)
	}
	if len(sp.Tokens) != 2 || len(sp.Order) != 2 {
		t.Fatalf("spawn = %+v", sp)
	}
	if sp.Tokens[0].TokenID != "e1-1" || sp.Tokens[1].TokenID != "e1-2" {
		t.Errorf("token ids = %v", sp.Tokens)
	}
	for _, tok := range sp.Tokens {
		if tok.HP != 7 || tok.MaxHP != 7 {
			t.Errorf("token hp = %+v", tok)
		}
	}
	if _, err := EmitSpawnIntent(enc, ""); err == nil {
		t.Error("expected map-required error")
	}
}

func TestAdjustHP(t *testing.T) {
	in := []TokenHP{
		{TokenID: "a", HP: 5, MaxHP: 10},
		{TokenID: "b", HP: 2, MaxHP: 10},
		{TokenID: "c", HP: 9, MaxHP: 10},
	}
	got := AdjustHP(in, -4)
	if got[0].HP != 1 || got[1].HP != 0 || got[2].HP != 5 {
		t.Fatalf("harm = %+v", got)
	}
	got = AdjustHP(in, 99)
	for _, x := range got {
		if x.HP != 10 {
			t.Fatalf("overheal not capped: %+v", got)
		}
	}
	if in[0].HP != 5 {
		t.Error("input mutated")
	}
}

func TestLootDropNote(t *testing.T) {
	enc, _ := BuildEncounter("e1", "Ambush", []MonsterRef{{ID: "g", Name: "G", HP: 1, Count: 1}})
	note := LootDropNote(enc, []string{"shortsword", "12 gp"})
	if !strings.Contains(note, "## Loot") || !strings.Contains(note, "- [ ] shortsword") {
		t.Errorf("note = %q", note)
	}
	if empty := LootDropNote(enc, nil); !strings.Contains(empty, "No loot.") {
		t.Errorf("empty note = %q", empty)
	}
}
