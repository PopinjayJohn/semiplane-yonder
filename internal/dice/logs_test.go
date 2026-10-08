package dice

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// openLogDB creates a file-backed temp DB with the dice_logs shape owned by
// the store migrations (Lane B). :memory: does not survive database/sql
// pooling, so tests use a real file like the store package does.
func openLogDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("close test db: %v", err)
		}
	})
	_, err = db.Exec(`CREATE TABLE dice_logs (
	    id INTEGER PRIMARY KEY AUTOINCREMENT,
	    session_id TEXT,
	    actor TEXT NOT NULL,
	    envelope_json TEXT NOT NULL,
	    modifiers_json TEXT NOT NULL DEFAULT '[]',
	    result_json TEXT NOT NULL DEFAULT '{}',
	    blind INTEGER NOT NULL DEFAULT 0,
	    created_at INTEGER NOT NULL
	)`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func testEntry() *LogEntry {
	return &LogEntry{
		ActorID:   "fighter",
		IntentID:  "attack",
		Notation:  "1d20+5",
		Timestamp: 1700000000,
		Blind:     false,
		Envelope:  map[string]any{"intent": "attack", "actor": "fighter"},
		Result: &RollResult{
			Notation: "1d20+5", Total: 17,
			Dice:   []DieResult{{Faces: 20, Value: 12}},
			RollID: "pre-save-id",
		},
	}
}

func TestLogSaveGetRoundTrip(t *testing.T) {
	ctx := context.Background()
	store := NewLogStore(openLogDB(t))
	e := testEntry()
	if err := store.Save(ctx, e); err != nil {
		t.Fatal(err)
	}
	if e.RollID == "" {
		t.Fatal("Save did not assign a RollID")
	}
	if e.RollID == "pre-save-id" {
		t.Error("Save kept the roller crypto id instead of the row id")
	}
	got, err := store.Get(ctx, e.RollID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ActorID != "fighter" || got.IntentID != "attack" || got.Notation != "1d20+5" {
		t.Errorf("reloaded entry = %+v", got)
	}
	if got.Result == nil || got.Result.Total != 17 {
		t.Errorf("reloaded result = %+v", got.Result)
	}
	// Replay reads stored values: the stored total survives even if it no
	// longer matches any fresh roll.
	if got.Result.Total != e.Result.Total {
		t.Errorf("stored total %d != saved %d", got.Result.Total, e.Result.Total)
	}
}

func TestLogBlindAndEnvelope(t *testing.T) {
	ctx := context.Background()
	store := NewLogStore(openLogDB(t))
	e := testEntry()
	e.Blind = true
	e.Envelope = map[string]any{
		"intent": "attack", "actor": "fighter",
		"targets": []string{"goblin"}, "tool": "longsword",
	}
	if err := store.Save(ctx, e); err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(ctx, e.RollID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Blind {
		t.Error("blind flag lost in round trip")
	}
}

func TestLogGetUnknown(t *testing.T) {
	ctx := context.Background()
	store := NewLogStore(openLogDB(t))
	if _, err := store.Get(ctx, "4242"); err == nil {
		t.Error("Get(4242) succeeded, want not-found")
	}
	if _, err := store.Get(ctx, "not-a-row"); err == nil {
		t.Error("Get(not-a-row) succeeded, want error")
	}
	if err := store.Save(ctx, nil); err == nil {
		t.Error("Save(nil) succeeded, want error")
	}
}

func TestLogListAndPurge(t *testing.T) {
	ctx := context.Background()
	store := NewLogStore(openLogDB(t))
	for i := 0; i < 3; i++ {
		e := testEntry()
		if err := store.Save(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	other := testEntry()
	other.ActorID = "goblin"
	if err := store.Save(ctx, other); err != nil {
		t.Fatal(err)
	}
	entries, err := store.List(ctx, "fighter", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Errorf("List(fighter) = %d entries, want 3", len(entries))
	}
	// Newest first.
	for i := 1; i < len(entries); i++ {
		if entries[i-1].RollID < entries[i].RollID {
			t.Errorf("list not newest-first: %v", entries)
			break
		}
	}
	limited, err := store.List(ctx, "fighter", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(limited) != 2 {
		t.Errorf("List(fighter, 2) = %d entries, want 2", len(limited))
	}
	if err := store.PurgeOld(ctx, 0); err != nil {
		t.Errorf("PurgeOld(0) = %v, want nil (disabled)", err)
	}
	// Everything is from 2026 wall-clock (Timestamp 0 → now at Save... only
	// when Timestamp == 0; testEntry pins a 2023 stamp, so a 30-day purge
	// must remove all four rows).
	if err := store.PurgeOld(ctx, 30); err != nil {
		t.Fatal(err)
	}
	rest, err := store.List(ctx, "fighter", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rest) != 0 {
		t.Errorf("after purge, %d entries remain, want 0", len(rest))
	}
}
