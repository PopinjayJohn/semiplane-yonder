package plugins

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/semiplane/yonder/internal/auth"
)

func blindEvent() RollEvent {
	total := int64(18)
	return RollEvent{RollID: "r1", ActorID: "bram", Notation: "1d20+5", Blind: true, Total: &total}
}

func TestRouteBlind(t *testing.T) {
	total := int64(18)
	open := RollEvent{RollID: "r2", Total: &total}
	if got := RouteBlind(open, nil); got.Total == nil {
		t.Error("open roll must pass through")
	}
	// GM keeps values.
	if got := RouteBlind(blindEvent(), &auth.Viewer{UserID: "gm", IsGM: true}); got.Total == nil {
		t.Error("GM must see blind totals")
	}
	// Players, guests, and GM-previews get nil totals.
	for _, v := range []*auth.Viewer{
		nil,
		{UserID: "mira"},
		{UserID: "gm", IsGM: true, PreviewAs: "mira"},
	} {
		got := RouteBlind(blindEvent(), v)
		if got.Total != nil {
			t.Errorf("viewer %+v saw blind total", v)
		}
		if !got.Blind || got.RollID != "r1" {
			t.Errorf("routing dropped identity: %+v", got)
		}
	}
}

func TestClampLimit(t *testing.T) {
	if ClampLimit(0) != 1 || ClampLimit(-5) != 1 {
		t.Error("floor broken")
	}
	if ClampLimit(1000) != MaxRollLogLimit {
		t.Error("ceiling broken")
	}
	if ClampLimit(20) != 20 {
		t.Error("passthrough broken")
	}
}

func TestFrozenContractStrings(t *testing.T) {
	// Exact frozen values Lane K implements against. If any of these change,
	// it is an explicit amend with K notified — never a silent edit.
	cases := map[string]string{
		"fragment path":  MapFragmentPath,
		"fragment wants": "/vtt/{mapID}/fragment",
		"snapshot path":  StateSnapshotPath,
		"snapshot wants": "/api/vtt/{mapID}/state",
		"stream":         StreamPath,
		"stream wants":   "/events",
		"target":         MapFragmentTarget,
		"target wants":   "vtt-map",
	}
	for _, pair := range [][2]string{
		{cases["fragment path"], cases["fragment wants"]},
		{cases["snapshot path"], cases["snapshot wants"]},
		{cases["stream"], cases["stream wants"]},
		{cases["target"], cases["target wants"]},
	} {
		if pair[0] != pair[1] {
			t.Errorf("frozen string drift: %q != %q", pair[0], pair[1])
		}
	}
	events := map[string]string{
		"hello": EventHello, "secret-flip": EventSecretFlip,
		"dice-roll": EventDiceRoll, "dice-blind": EventDiceBlind,
		"initiative-update": EventInitiativeUpdate, "token-move": EventTokenMove,
		"token-hp": EventTokenHP, "fog-update": EventFogUpdate,
		"encounter-spawn": EventEncounterSpawn, "session-recap": EventSessionRecap,
	}
	for want, got := range events {
		if want != got {
			t.Errorf("event name drift: const %q holds %q", want, got)
		}
	}
	seen := map[string]bool{}
	for _, e := range events {
		if seen[e] {
			t.Errorf("duplicate event name %q", e)
		}
		seen[e] = true
	}
	if FragmentVersion != 1 {
		t.Errorf("FragmentVersion = %d", FragmentVersion)
	}
}

func TestSnapshotJSONShape(t *testing.T) {
	snap := StateSnapshot{
		Version: FragmentVersion, MapID: "arena",
		Tokens:     []SnapshotToken{{ID: "e1-1", Name: "Goblin", X: 1, Y: 2, HP: 7, MaxHP: 7}},
		Fog:        SnapshotFog{Hidden: true},
		Initiative: []SnapshotInitEntry{{ID: "e1-1", Name: "Goblin", Order: 1}},
	}
	b, err := json.Marshal(snap)
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{`"version":1`, `"map_id":"arena"`, `"hidden":true`, `"order":1`} {
		if !strings.Contains(s, want) {
			t.Errorf("snapshot missing %s: %s", want, s)
		}
	}
}
