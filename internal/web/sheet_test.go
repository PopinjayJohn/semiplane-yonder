package web

import (
	"strings"
	"testing"
)

func validSheetMap() map[string]any {
	return map[string]any{
		"class": "fighter", "ancestry": "human", "background": "soldier",
		"level": int64(1), "xp": int64(0),
		"str": int64(15), "dex": int64(14), "con": int64(13),
		"int": int64(12), "wis": int64(10), "cha": int64(8),
		"hp": int64(11), "hp-max": int64(11),
		"hit-dice": int64(1), "hit-dice-max": int64(1), "hit-die": int64(10),
		"inspiration": int64(0), "death-succ": int64(0), "death-fail": int64(0),
	}
}

func TestParseSheetValid(t *testing.T) {
	sh, err := ParseSheet(validSheetMap())
	if err != nil {
		t.Fatal(err)
	}
	if sh.Class != "fighter" || sh.HPMax != 11 || sh.ConMod() != 1 {
		t.Fatalf("wrong sheet: %+v", sh)
	}
}

func TestParseSheetRejectsUnknown(t *testing.T) {
	m := validSheetMap()
	m["frobnicate"] = int64(1)
	if _, err := ParseSheet(m); err == nil {
		t.Fatal("unknown sheet key must fail (red line)")
	}
}

func TestParseSheetRanges(t *testing.T) {
	for _, tc := range []struct {
		name, key string
		val       int64
	}{
		{"level-low", "level", 0}, {"level-high", "level", 21},
		{"stat-low", "str", 0}, {"stat-high", "cha", 31},
		{"hp-over-max", "hp", 12}, {"hpmax-low", "hp-max", 0},
		{"dice-over-max", "hit-dice", 2}, {"die-bad", "hit-die", 20},
		{"insp", "inspiration", 2}, {"death", "death-fail", 4},
	} {
		m := validSheetMap()
		m[tc.key] = tc.val
		if _, err := ParseSheet(m); err == nil {
			t.Fatalf("%s: out-of-range accepted", tc.name)
		}
	}
}

func TestParseSheetSlotPairs(t *testing.T) {
	m := validSheetMap()
	m["slots-1"] = int64(2)
	m["slots-1-max"] = int64(2)
	sh, err := ParseSheet(m)
	if err != nil {
		t.Fatal(err)
	}
	if sh.Slots[1] != 2 || sh.SlotsMax[1] != 2 {
		t.Fatalf("slots wrong: %+v", sh.Slots)
	}
	m["slots-1"] = int64(3)
	if _, err := ParseSheet(m); err == nil {
		t.Fatal("slots over max accepted")
	}
}

func TestSetSheetValueReplace(t *testing.T) {
	in := "---\ntitle: Mira\nowner: mira\nsheet:\n  class: fighter\n  hp: 5 # current\n  hp-max: 11\n---\nbody\n"
	got, ok := setSheetValue(in, "hp", "8")
	if !ok {
		t.Fatal("surgical edit refused")
	}
	if !strings.Contains(got, "  hp: 8 # current") {
		t.Fatalf("value/comment wrong:\n%s", got)
	}
	for _, want := range []string{"title: Mira", "owner: mira", "class: fighter", "hp-max: 11", "body"} {
		if !strings.Contains(got, want) {
			t.Fatalf("lost %q:\n%s", want, got)
		}
	}
}

func TestSetSheetValueInsert(t *testing.T) {
	in := "---\ntitle: Mira\nsheet:\n  hp: 5\nsecret: true\n---\n"
	got, ok := setSheetValue(in, "xp", "100")
	if !ok {
		t.Fatal("surgical edit refused")
	}
	// Inserted inside the sheet block, before the trailing top-level key.
	si := strings.Index(got, "  xp: 100")
	sec := strings.Index(got, "secret: true")
	hp := strings.Index(got, "  hp: 5")
	if si < 0 || hp > si || si > sec {
		t.Fatalf("bad insert position:\n%s", got)
	}
}

func TestSetSheetValueCreatesBlock(t *testing.T) {
	got, ok := setSheetValue("---\ntitle: T\n---\nbody\n", "hp", "3")
	if !ok {
		t.Fatal("refused")
	}
	if !strings.Contains(got, "sheet:\n  hp: 3") {
		t.Fatalf("block not created:\n%s", got)
	}
	got, ok = setSheetValue("just body\n", "hp", "3")
	if !ok || !strings.HasPrefix(got, "---\nsheet:") {
		t.Fatalf("fence not created:\n%s", got)
	}
}

func TestSetSheetValueRefusesFlow(t *testing.T) {
	if _, ok := setSheetValue("---\nsheet: {hp: 5}\n---\n", "hp", "8"); ok {
		t.Fatal("flow-style sheet must fail closed, never corrupt")
	}
}

func TestShortRestHealsAndSpends(t *testing.T) {
	sh, _ := ParseSheet(validSheetMap())
	sh.HP = 3                                                   // missing 8
	fixed := func(faces int64) (int64, error) { return 6, nil } // d10->6 +1 con = 7
	healed, err := sh.ShortRest(1, fixed)
	if err != nil {
		t.Fatal(err)
	}
	if healed != 7 || sh.HP != 10 || sh.HitDice != 0 {
		t.Fatalf("rest wrong: healed=%d hp=%d dice=%d", healed, sh.HP, sh.HitDice)
	}
	// Overspend rejected; over-heal capped.
	if _, err := sh.ShortRest(1, fixed); err == nil {
		t.Fatal("overspend accepted")
	}
	sh.HitDice = 1
	sh.HP = 10
	healed, err = sh.ShortRest(1, fixed)
	if err != nil || healed != 1 || sh.HP != 11 {
		t.Fatalf("overheal not capped: %d %v", healed, err)
	}
}

func TestLongRestRestores(t *testing.T) {
	sh, _ := ParseSheet(validSheetMap())
	sh.HP, sh.HitDice, sh.DeathFail = 2, 0, 2
	sh.Slots, sh.SlotsMax = map[int64]int64{1: 0}, map[int64]int64{1: 2}
	sh.HitDiceMax = 4
	sh.LongRest()
	if sh.HP != sh.HPMax || sh.HitDice != 2 || sh.DeathFail != 0 || sh.Slots[1] != 2 {
		t.Fatalf("long rest wrong: %+v", sh)
	}
}

func TestLevelUpMath(t *testing.T) {
	sh, _ := ParseSheet(validSheetMap()) // con 13 -> +1; d10 avg 6; gain 7
	if err := sh.LevelUp(&ClassDef{ID: "fighter", HitDie: 10}); err != nil {
		t.Fatal(err)
	}
	if sh.Level != 2 || sh.HPMax != 18 || sh.HP != 18 || sh.HitDiceMax != 2 || sh.HitDice != 2 {
		t.Fatalf("level wrong: %+v", sh)
	}
	sh.Level = 20
	if err := sh.LevelUp(&ClassDef{HitDie: 10}); err == nil {
		t.Fatal("level 21 accepted")
	}
}

func TestTouchRecoveryStub(t *testing.T) {
	if err := TouchRecovery(t.Context(), StubEvaluator{}, "rest-long", "x"); err != nil {
		t.Fatalf("stub evaluator must never fail: %v", err)
	}
}
