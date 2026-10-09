package web

// UI-2 encounter section tests: build→spawn→HP adjust over HTTP against a
// real app DB (migrated vtt_* tables), GM-only gates, server-side HP
// resolution, floor/cap clamps, and the dashboard encounter section.

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/store"
)

func seedCompendium(t *testing.T, s store.Store, goblinHP, fighterHP int64) {
	t.Helper()
	ctx := context.Background()
	for _, tc := range []struct {
		path string
		hp   int64
	}{
		{"rules/base/dnd/compendium/goblin-warrior.md", goblinHP},
		{"rules/base/dnd/compendium/fighter.md", fighterHP},
	} {
		if err := s.PageUpsert(ctx, &store.Page{
			Path: tc.path, Title: tc.path, Content: "# x",
			Frontmatter: map[string]any{"sheet": map[string]any{"hp": tc.hp}},
			UpdatedAt:   time.Now().Unix(),
		}); err != nil {
			t.Fatal(err)
		}
	}
}

func seedMapPage(t *testing.T, s store.Store, mapID string) {
	t.Helper()
	if err := s.PageUpsert(context.Background(), &store.Page{
		Path: "maps/" + mapID + ".md", Title: mapID, Content: "# " + mapID,
		Frontmatter: map[string]any{
			"vtt-map": map[string]any{"grid": map[string]any{"cols": int64(20), "rows": int64(14)}},
		},
		UpdatedAt: time.Now().Unix(),
	}); err != nil {
		t.Fatal(err)
	}
}

func jsonEncounterRequest(t *testing.T, h http.HandlerFunc, v *auth.Viewer, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/encounter", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(auth.CSRFHeaderName, testCSRF)
	req.AddCookie(sessionCookie())
	rec := httptest.NewRecorder()
	h(rec, withViewer(req, v))
	return rec
}

func tokenHPs(t *testing.T, db *sql.DB, mapID string) map[string][2]int {
	t.Helper()
	rows, err := db.Query(`SELECT token, hp, max_hp FROM vtt_tokens WHERE map = ?`, mapID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string][2]int{}
	for rows.Next() {
		var id string
		var hp, max int
		if err := rows.Scan(&id, &hp, &max); err != nil {
			t.Fatal(err)
		}
		out[id] = [2]int{hp, max}
	}
	return out
}

func initiativeOrder(t *testing.T, db *sql.DB, mapID string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT token FROM vtt_initiative WHERE map = ? ORDER BY ord`, mapID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func TestEncounterSpawnForm(t *testing.T) {
	h, _, s := testSetupApp(t)
	seedCompendium(t, s, 12, 15) // custom HP proves server-side resolution
	seedMapPage(t, s, "arena")
	rec := postForm(t, h.EncounterAction, "/encounter", gm(), map[string]string{
		"action": "spawn", "map": "arena", "goblin-warrior": "2", "fighter": "1",
	}, true)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("spawn = %d, want 303: %s", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard?map=arena" {
		t.Fatalf("spawn Location = %q, want /dashboard?map=arena", loc)
	}
	db := s.AppDB()
	got := tokenHPs(t, db, "arena")
	if len(got) != 3 {
		t.Fatalf("spawned tokens = %d, want 3: %v", len(got), got)
	}
	// HP comes from the compendium pages (12/15), never the client.
	seenGoblin, seenFighter := 0, 0
	for _, hp := range got {
		switch hp {
		case [2]int{12, 12}:
			seenGoblin++
		case [2]int{15, 15}:
			seenFighter++
		default:
			t.Fatalf("unexpected token HP %v (want compendium 12/15)", hp)
		}
	}
	if seenGoblin != 2 || seenFighter != 1 {
		t.Fatalf("counts wrong: goblin=%d fighter=%d: %v", seenGoblin, seenFighter, got)
	}
	// One action wrote tokens + initiative together, spawn order kept.
	if ord := initiativeOrder(t, db, "arena"); len(ord) != 3 {
		t.Fatalf("initiative rows = %d, want 3", len(ord))
	}
}

func TestEncounterSpawnJSON(t *testing.T) {
	h, _, s := testSetupApp(t)
	seedCompendium(t, s, 10, 13)
	rec := jsonEncounterRequest(t, h.EncounterAction, gm(),
		`{"action":"spawn","map":"arena","counts":{"goblin-warrior":1}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("spawn JSON = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	var out struct {
		Map        string   `json:"map"`
		Encounter  string   `json:"encounter"`
		Tokens     []any    `json:"tokens"`
		Initiative []string `json:"initiative"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("spawn JSON not decodable: %v", err)
	}
	if out.Map != "arena" || len(out.Tokens) != 1 || len(out.Initiative) != 1 {
		t.Fatalf("spawn JSON wrong: %+v", out)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "private, no-store" {
		t.Errorf("Cache-Control = %q, want private, no-store", cc)
	}
	_ = s
}

func TestEncounterAuthGates(t *testing.T) {
	h, _, _ := testSetupApp(t)
	spawn := map[string]string{"action": "spawn", "map": "arena", "fighter": "1"}
	if rec := postForm(t, h.EncounterAction, "/encounter", bob(), spawn, true); rec.Code != http.StatusForbidden {
		t.Errorf("player spawn = %d, want 403", rec.Code)
	}
	preview := &auth.Viewer{UserID: "gm", IsGM: true, PreviewAs: "cass"}
	if rec := postForm(t, h.EncounterAction, "/encounter", preview, spawn, true); rec.Code != http.StatusForbidden {
		t.Errorf("GM-preview spawn = %d, want 403 (previews read-only)", rec.Code)
	}
	if rec := postForm(t, h.EncounterAction, "/encounter", gm(), spawn, false); rec.Code != http.StatusForbidden {
		t.Errorf("no-CSRF spawn = %d, want 403", rec.Code)
	}
	req := httptest.NewRequest(http.MethodPost, "/encounter", nil)
	rec := httptest.NewRecorder()
	h.EncounterAction(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("guest spawn = %d, want 401", rec.Code)
	}
}

func TestEncounterValidation(t *testing.T) {
	h, _, _ := testSetupApp(t)
	form := func(fields map[string]string) int {
		return postForm(t, h.EncounterAction, "/encounter", gm(), fields, true).Code
	}
	if code := form(map[string]string{"action": "spawn", "map": "arena"}); code != http.StatusBadRequest {
		t.Errorf("empty spawn = %d, want 400", code)
	}
	if code := form(map[string]string{"action": "spawn", "map": "arena", "goblin-warrior": "15", "fighter": "10"}); code != http.StatusUnprocessableEntity {
		t.Errorf("cap spawn = %d, want 422", code)
	}
	if code := form(map[string]string{"action": "spawn", "map": "../x", "fighter": "1"}); code != http.StatusBadRequest {
		t.Errorf("bad map = %d, want 400", code)
	}
	if code := form(map[string]string{"action": "dance", "map": "arena"}); code != http.StatusBadRequest {
		t.Errorf("unknown action = %d, want 400", code)
	}
	if rec := jsonEncounterRequest(t, h.EncounterAction, gm(),
		`{"action":"spawn","map":"arena","counts":{"dragon":1}}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown creature = %d, want 400", rec.Code)
	}
	if rec := jsonEncounterRequest(t, h.EncounterAction, gm(),
		`{"action":"hp","map":"arena","token":"enc-1","delta":"many"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad delta JSON = %d, want 400", rec.Code)
	}
}

func TestEncounterHPAdjust(t *testing.T) {
	h, _, s := testSetupApp(t)
	seedCompendium(t, s, 10, 13)
	seedMapPage(t, s, "arena")
	if rec := postForm(t, h.EncounterAction, "/encounter", gm(), map[string]string{
		"action": "spawn", "map": "arena", "fighter": "1",
	}, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("spawn = %d, want 303", rec.Code)
	}
	db := s.AppDB()
	var tokenID string
	if err := db.QueryRow(`SELECT token FROM vtt_tokens WHERE map = 'arena'`).Scan(&tokenID); err != nil {
		t.Fatal(err)
	}
	hp := func(fields map[string]string) int {
		return postForm(t, h.EncounterAction, "/encounter", gm(), fields, true).Code
	}
	cur := func() int {
		var hp int
		if err := db.QueryRow(`SELECT hp FROM vtt_tokens WHERE map='arena' AND token=?`, tokenID).Scan(&hp); err != nil {
			t.Fatal(err)
		}
		return hp
	}
	base := map[string]string{"action": "hp", "map": "arena", "token": tokenID}
	if code := hp(mergeFields(base, map[string]string{"delta": "-4"})); code != http.StatusSeeOther {
		t.Fatalf("hp -4 = %d, want 303", code)
	}
	if got := cur(); got != 9 {
		t.Fatalf("hp after -4 = %d, want 9", got)
	}
	// Floor at 0.
	if code := hp(mergeFields(base, map[string]string{"delta": "-999"})); code != http.StatusSeeOther {
		t.Fatalf("hp floor = %d, want 303", code)
	}
	if got := cur(); got != 0 {
		t.Fatalf("hp floor = %d, want 0", got)
	}
	// Cap at max.
	if code := hp(mergeFields(base, map[string]string{"delta": "999"})); code != http.StatusSeeOther {
		t.Fatalf("hp cap = %d, want 303", code)
	}
	if got := cur(); got != 13 {
		t.Fatalf("hp cap = %d, want 13", got)
	}
	// Unknown token 404s (never fabricates).
	if code := hp(mergeFields(base, map[string]string{"token": "nope", "delta": "-1"})); code != http.StatusNotFound {
		t.Errorf("unknown token hp = %d, want 404", code)
	}
	// Players may not adjust.
	if rec := postForm(t, h.EncounterAction, "/encounter", bob(), mergeFields(base, map[string]string{"delta": "-1"}), true); rec.Code != http.StatusForbidden {
		t.Errorf("player hp = %d, want 403", rec.Code)
	}
}

func mergeFields(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func TestEncounterFallbackHP(t *testing.T) {
	h, _, s := testSetupApp(t)
	// No compendium pages: code-defined fallbacks (10/13) keep flows working.
	if rec := postForm(t, h.EncounterAction, "/encounter", gm(), map[string]string{
		"action": "spawn", "map": "arena", "goblin-warrior": "1", "fighter": "1",
	}, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("spawn = %d, want 303", rec.Code)
	}
	got := tokenHPs(t, s.AppDB(), "arena")
	seen := map[[2]int]int{}
	for _, hp := range got {
		seen[hp]++
	}
	if seen[[2]int{10, 10}] != 1 || seen[[2]int{13, 13}] != 1 {
		t.Fatalf("fallback HP wrong: %v", got)
	}
}

func TestAdjustEncounterHPNoMutation(t *testing.T) {
	in := encounterTokenRow{TokenID: "t", HP: 5, MaxHP: 10}
	if got := adjustEncounterHP(in, -9); got.HP != 0 {
		t.Fatalf("floor = %d, want 0", got.HP)
	}
	if got := adjustEncounterHP(in, 99); got.HP != 10 {
		t.Fatalf("cap = %d, want 10", got.HP)
	}
	if in.HP != 5 {
		t.Fatalf("input mutated: HP = %d, want 5", in.HP)
	}
}

func TestDashboardEncounterSection(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(filepath.Join(dir, "t.index.db"), filepath.Join(dir, "t.app.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ctx := context.Background()
	if err := store.NewMigrationRunner(s.IndexDB()).RunIndex(ctx, s.IndexDB()); err != nil {
		t.Fatal(err)
	}
	if err := store.NewMigrationRunner(s.AppDB()).RunApp(ctx); err != nil {
		t.Fatal(err)
	}
	seedCompendium(t, s, 12, 15)
	seedMapPage(t, s, "arena")
	wh := &WriteHandlers{Store: s, SlotRegistry: NewSlotRegistry()}
	if rec := postForm(t, wh.EncounterAction, "/encounter", gm(), map[string]string{
		"action": "spawn", "map": "arena", "goblin-warrior": "1",
	}, true); rec.Code != http.StatusSeeOther {
		t.Fatalf("spawn = %d, want 303", rec.Code)
	}

	h := &ReadHandlers{Store: s, SlotRegistry: NewSlotRegistry()}
	req := httptest.NewRequest(http.MethodGet, "/dashboard?map=arena", nil)
	rec := httptest.NewRecorder()
	h.Dashboard(rec, withViewer(req, gm()))
	if rec.Code != http.StatusOK {
		t.Fatalf("dashboard = %d, want 200", rec.Code)
	}
	body := rec.Body.String()
	for _, want := range []string{
		`aria-labelledby="encounters"`, `action="/encounter"`,
		"Goblin Warrior", "Fighter", "arena",
		`name="goblin-warrior"`, `name="fighter"`,
		`name="delta"`, "Goblin Warrior", "12/12 HP",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard encounter section missing %q", want)
		}
	}
}
