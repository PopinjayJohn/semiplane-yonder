package plugins

import (
	"errors"
	"strings"
	"testing"

	"github.com/semiplane/yonder/internal/web"
)

func d6Table() RandTable {
	return RandTable{
		ID: "omens", Name: "Omens", Faces: 6,
		Entries: []TableEntry{
			{Min: 1, Max: 2, Text: "Weal"},
			{Min: 3, Max: 6, Text: "Woe"},
		},
	}
}

func TestTableValidate(t *testing.T) {
	if err := d6Table().Validate(); err != nil {
		t.Fatal(err)
	}
	gap := d6Table()
	gap.Entries = []TableEntry{{Min: 1, Max: 2, Text: "x"}}
	if err := gap.Validate(); err == nil {
		t.Error("expected gap error")
	}
	overlap := d6Table()
	overlap.Entries = []TableEntry{{Min: 1, Max: 3, Text: "x"}, {Min: 3, Max: 6, Text: "y"}}
	if err := overlap.Validate(); err == nil {
		t.Error("expected overlap error")
	}
	empty := d6Table()
	empty.Entries[0].Text = " "
	if err := empty.Validate(); err == nil {
		t.Error("expected empty-text error")
	}
	if err := (RandTable{ID: "x", Name: "x", Faces: 1}).Validate(); err == nil {
		t.Error("expected faces error")
	}
}

func TestTableRollDeterministic(t *testing.T) {
	tab := d6Table()
	stub := UintN(func(n int) (int, error) {
		if n != 6 {
			t.Fatalf("sample n = %d", n)
		}
		return 0, nil // face 1 → Weal
	})
	r, err := tab.Roll(stub)
	if err != nil {
		t.Fatal(err)
	}
	if r.Value != 1 || r.Text != "Weal" || r.TableID != "omens" {
		t.Fatalf("roll = %+v", r)
	}
	stubTop := UintN(func(int) (int, error) { return 5, nil }) // face 6 → Woe
	r2, err := tab.Roll(stubTop)
	if err != nil || r2.Text != "Woe" {
		t.Fatalf("roll = %+v, %v", r2, err)
	}
	if _, err := tab.Roll(UintN(func(int) (int, error) { return 0, errors.New("rng dead") })); err == nil {
		t.Error("expected sampler error")
	}
	if _, err := (RandTable{}).Roll(nil); err == nil {
		t.Error("expected invalid-table error")
	}
}

func TestTableCryptoDefault(t *testing.T) {
	r, err := d6Table().Roll(nil) // nil sampler → crypto/rand
	if err != nil {
		t.Fatal(err)
	}
	if r.Value < 1 || r.Value > 6 {
		t.Errorf("face = %d", r.Value)
	}
}

func TestSidebarSlotSecretFiltered(t *testing.T) {
	s := d6Table().SidebarSlot()
	if s.SlotName != web.SlotSidebarRight {
		t.Errorf("slot = %q", s.SlotName)
	}
	if !s.SecretFiltered || s.PluginID != RandomTablesFeatureID {
		t.Errorf("slot = %+v", s)
	}
	if err := ValidateSlot(RandomTablesFeatureID, s); err != nil {
		t.Fatal(err)
	}
}

func TestInsertSnippet(t *testing.T) {
	r := RollResult{TableID: "omens", Value: 6, Text: "Woe"}
	snip := r.InsertSnippet("Omens", 6)
	if !strings.Contains(snip, "d6 6") || !strings.Contains(snip, "Woe") {
		t.Errorf("snippet = %q", snip)
	}
}
