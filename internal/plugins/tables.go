package plugins

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"

	"github.com/semiplane/yonder/internal/web"
)

// Random-tables: the first Feature consumer (P06/P07). A d100 sidebar slot
// with one-click insert into the session note. Tables are data (name +
// ranges); rolling uses crypto/rand (seedable source injected for tests).

// RandomTablesFeatureID is the plugin/feature ID of the first consumer.
const RandomTablesFeatureID = "random-tables"

// TableEntry is one ranged outcome (inclusive Min..Max on a Faces-sided die).
type TableEntry struct {
	Min  int
	Max  int
	Text string // already secret-filtered display text
}

// RandTable is a rollable lookup table.
type RandTable struct {
	ID      string
	Name    string
	Faces   int // die size (e.g. 100 for d100)
	Entries []TableEntry
}

// Validate checks full contiguous coverage of 1..Faces (no gaps/overlaps)
// and non-empty texts.
func (t RandTable) Validate() error {
	if strings.TrimSpace(t.ID) == "" || strings.TrimSpace(t.Name) == "" {
		return &PluginError{Code: "BAD_TABLE", Message: "id and name required"}
	}
	if t.Faces < 2 {
		return &PluginError{Code: "BAD_TABLE", Message: t.ID + ": faces must be >= 2"}
	}
	covered := make([]bool, t.Faces+1)
	for i, e := range t.Entries {
		if e.Min < 1 || e.Max > t.Faces || e.Min > e.Max {
			return &PluginError{Code: "BAD_TABLE", Message: fmt.Sprintf("%s: entry %d has bad range %d-%d", t.ID, i, e.Min, e.Max)}
		}
		if strings.TrimSpace(e.Text) == "" {
			return &PluginError{Code: "BAD_TABLE", Message: fmt.Sprintf("%s: entry %d has empty text", t.ID, i)}
		}
		for v := e.Min; v <= e.Max; v++ {
			if covered[v] {
				return &PluginError{Code: "BAD_TABLE", Message: fmt.Sprintf("%s: overlap at %d", t.ID, v)}
			}
			covered[v] = true
		}
	}
	for v := 1; v <= t.Faces; v++ {
		if !covered[v] {
			return &PluginError{Code: "BAD_TABLE", Message: fmt.Sprintf("%s: gap at %d", t.ID, v)}
		}
	}
	return nil
}

// UintN is an injectable [0,n) sampler (crypto/rand in prod, stub in tests).
type UintN func(n int) (int, error)

// CryptoUintN samples [0,n) from crypto/rand.
func CryptoUintN(n int) (int, error) {
	v, err := rand.Int(rand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0, err
	}
	return int(v.Int64()), nil
}

// RollResult is one table roll.
type RollResult struct {
	TableID string
	Value   int    // 1-based face
	Text    string // entry text
}

// Roll rolls on a validated table (validates on every roll: tables are data
// and may hot-reload).
func (t RandTable) Roll(sample UintN) (RollResult, error) {
	if err := t.Validate(); err != nil {
		return RollResult{}, err
	}
	if sample == nil {
		sample = CryptoUintN
	}
	v, err := sample(t.Faces)
	if err != nil {
		return RollResult{}, err
	}
	face := v + 1
	for _, e := range t.Entries {
		if face >= e.Min && face <= e.Max {
			return RollResult{TableID: t.ID, Value: face, Text: e.Text}, nil
		}
	}
	return RollResult{}, &PluginError{Code: "BAD_TABLE", Message: "no entry matched (unreachable after validate)"}
}

// SidebarSlot returns the d100 widget slot contribution (sidebar-right).
func (t RandTable) SidebarSlot() web.SlotComponent {
	return web.SlotComponent{
		SlotName: web.SlotSidebarRight, ID: "random-table-" + t.ID,
		Component: "random-table", Priority: 10,
		SecretFiltered: true, PluginID: RandomTablesFeatureID,
	}
}

// InsertSnippet renders the one-click session-note insert line for a roll
// (the caller appends it via the write path).
func (r RollResult) InsertSnippet(tableName string, faces int) string {
	return fmt.Sprintf("- Rolled %s (d%d %d): %s\n", tableName, faces, r.Value, r.Text)
}
