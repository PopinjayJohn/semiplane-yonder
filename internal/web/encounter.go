package web

// Encounter section HTTP surface (UI follow-up; the builder lives in Lane
// I2's internal/plugins encounter.go, live rows in Lane K's vtt_* tables).
//
// Import-cycle note: internal/plugins AND internal/vtt both import
// internal/web, so web can import neither. The small pure-semantics mirrors
// below are deliberate and pinned by tests:
//   - maxEncounterCreatures mirrors plugins.MaxEncounterCreatures (20).
//   - spawnTokenLayout mirrors plugins.EmitSpawnIntent ("<enc>-<n>", grid
//     fill x=(n-1)%8, y=(n-1)/8, spawn-order initiative).
//   - adjustEncounterHP mirrors plugins.AdjustHP (floor 0, cap MaxHP, no
//     input mutation — the input row is copied, never written through).
//   - validEncounterMapID/validEncounterTokenID mirror vtt.ValidMapID.
//   - The spawn SQL mirrors vtt.SpawnTokens (one transaction: upsert tokens,
//     replace initiative); the HP clamp mirrors vtt.SetTokenHP. No VTT board
//     code (render/snapshot/fragment) is touched — K owns the board.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/semiplane/yonder/internal/store"
	"github.com/semiplane/yonder/web/templates"
)

// maxEncounterCreatures caps one spawn (mirrors plugins.MaxEncounterCreatures).
const maxEncounterCreatures = 20

// defaultEncounterGrid mirrors vtt.DefaultGrid (sidecar-absent fallback).
const defaultEncounterGridCols = 20
const defaultEncounterGridRows = 14

// maxEncounterBody caps encounter API bodies (counts + names are tiny).
const maxEncounterBody = 8 << 10

// encounterCreature is one compendium pick offered by the build form. HP is
// resolved server-side from the compendium page at request time (vault
// truth via the index row); FallbackHP covers pack-less vaults. Clients
// send only the creature ID + count — never HP.
type encounterCreature struct {
	ID         string // closed set: "goblin-warrior" | "fighter"
	Name       string
	Page       string // compendium index path carrying sheet.hp
	FallbackHP int
}

// encounterRoster is the closed compendium set (Goblin/Fighter timebox).
func encounterRoster() []encounterCreature {
	return []encounterCreature{
		{ID: "goblin-warrior", Name: "Goblin Warrior",
			Page: "rules/base/dnd/compendium/goblin-warrior.md", FallbackHP: 10},
		{ID: "fighter", Name: "Fighter",
			Page: "rules/base/dnd/compendium/fighter.md", FallbackHP: 13},
	}
}

// resolveCreatureHP reads sheet.hp for one roster creature (case-insensitive
// index lookup; tolerant int coercion). Missing pages/keys fall back to the
// code-defined HP — never an error, never client-supplied.
func resolveCreatureHP(ctx context.Context, st store.Store, c encounterCreature) int {
	if st == nil {
		return c.FallbackHP
	}
	page, err := st.PageGet(ctx, c.Page)
	if err != nil || page == nil {
		return c.FallbackHP
	}
	sheet, ok := page.Frontmatter["sheet"].(map[string]any)
	if !ok {
		return c.FallbackHP
	}
	if n, ok := asEncounterInt(sheet["hp"]); ok && n > 0 && n <= 9999 {
		return n
	}
	return c.FallbackHP
}

func asEncounterInt(v any) (int, bool) {
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case float64:
		if t != float64(int(t)) {
			return 0, false
		}
		return int(t), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return 0, false
		}
		return n, true
	}
	return 0, false
}

// encounterToken is one token write inside a spawn (mirrors
// plugins.SpawnToken; positions filled by spawnTokenLayout).
type encounterToken struct {
	TokenID string
	Name    string
	HP      int
	MaxHP   int
	X       int
	Y       int
}

// resolvedPick is one server-side pick: roster creature + count + the
// server-resolved HP (clients never supply HP).
type resolvedPick struct {
	ID    string
	Name  string
	HP    int
	Count int
}

// spawnTokenLayout lowers validated picks to token writes + initiative order
// (mirrors plugins.EmitSpawnIntent: ids "<encounterID>-<n>", x=(n-1)%8,
// y=(n-1)/8, order follows spawn order).
func spawnTokenLayout(encounterID string, picks []resolvedPick) ([]encounterToken, []string) {
	var toks []encounterToken
	var order []string
	n := 0
	for _, p := range picks {
		for i := 0; i < p.Count; i++ {
			n++
			id := fmt.Sprintf("%s-%d", encounterID, n)
			toks = append(toks, encounterToken{
				TokenID: id, Name: p.Name, HP: p.HP,
				MaxHP: p.HP, X: (n - 1) % 8, Y: (n - 1) / 8,
			})
			order = append(order, id)
		}
	}
	return toks, order
}

// encounterGrid resolves the spawn grid from the map sidecar's vtt-map
// block (defaults when absent/broken — mirrors vtt.CalibrationFor). Tokens
// outside it are dropped by the caller (fail closed, never clamped into
// existence).
func encounterGrid(ctx context.Context, st store.Store, mapID string) (cols, rows int) {
	cols, rows = defaultEncounterGridCols, defaultEncounterGridRows
	if st == nil {
		return cols, rows
	}
	page, err := st.PageGet(ctx, "maps/"+mapID+".md")
	if err != nil || page == nil {
		return cols, rows
	}
	block, ok := page.Frontmatter["vtt-map"].(map[string]any)
	if !ok {
		return cols, rows
	}
	grid, ok := block["grid"].(map[string]any)
	if !ok {
		return cols, rows
	}
	if n, ok := asEncounterInt(grid["cols"]); ok && n >= 1 && n <= 64 {
		cols = n
	}
	if n, ok := asEncounterInt(grid["rows"]); ok && n >= 1 && n <= 64 {
		rows = n
	}
	return cols, rows
}

// spawnEncounterTokens executes the spawn: all tokens + initiative in ONE
// transaction (mirrors vtt.SpawnTokens: upsert-each, replace initiative,
// unknown order ids dropped). Off-grid tokens fail the whole spawn (422 at
// the caller, StateError shape here).
func spawnEncounterTokens(ctx context.Context, db *sql.DB, mapID string, toks []encounterToken, order []string, cols, rows int) error {
	for _, t := range toks {
		if !validEncounterTokenID(t.TokenID) || t.X < 0 || t.Y < 0 || t.X >= cols || t.Y >= rows {
			return fmt.Errorf("bad token %s", t.TokenID)
		}
		if t.HP < 0 || t.HP > 9999 || t.MaxHP < 0 || t.MaxHP > 9999 || t.HP > t.MaxHP {
			return fmt.Errorf("bad HP for %s", t.TokenID)
		}
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	for _, t := range toks {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO vtt_tokens(map, token, x, y, hidden, name, hp, max_hp, character)
			  VALUES(?, ?, ?, ?, 0, ?, ?, ?, '')
			  ON CONFLICT(map, token) DO UPDATE SET x=excluded.x, y=excluded.y,
			    hidden=excluded.hidden, name=excluded.name, hp=excluded.hp,
			    max_hp=excluded.max_hp, character=excluded.character`,
			mapID, t.TokenID, float64(t.X), float64(t.Y), t.Name, t.HP, t.MaxHP); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM vtt_initiative WHERE map = ?`, mapID); err != nil {
		return err
	}
	ord := 0
	for _, id := range order {
		if !validEncounterTokenID(id) {
			continue
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM vtt_tokens WHERE map = ? AND token = ?`, mapID, id).Scan(&exists); err != nil {
			continue // unknown token: dropped, never fabricated
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO vtt_initiative(map, token, ord) VALUES(?, ?, ?)`, mapID, id, ord); err != nil {
			return err
		}
		ord++
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// encounterTokenRow is one live token read back for HP adjust + display.
type encounterTokenRow struct {
	TokenID string
	Name    string
	X, Y    int
	HP      int
	MaxHP   int
	Hidden  bool
}

// adjustEncounterHP applies a delta to one token row (floor 0, cap MaxHP),
// copying the input — never mutating it (mirrors plugins.AdjustHP).
func adjustEncounterHP(in encounterTokenRow, delta int) encounterTokenRow {
	out := in
	out.HP += delta
	if out.HP < 0 {
		out.HP = 0
	}
	if out.HP > out.MaxHP {
		out.HP = out.MaxHP
	}
	return out
}

// loadEncounterToken reads one live token row (sql.ErrNoRows when absent).
func loadEncounterToken(ctx context.Context, db *sql.DB, mapID, tokenID string) (encounterTokenRow, error) {
	var t encounterTokenRow
	var fx, fy float64
	var hidden int
	err := db.QueryRowContext(ctx,
		`SELECT token, name, x, y, hp, max_hp, hidden FROM vtt_tokens WHERE map = ? AND token = ?`,
		mapID, tokenID).Scan(&t.TokenID, &t.Name, &fx, &fy, &t.HP, &t.MaxHP, &hidden)
	if err != nil {
		return t, err
	}
	t.X, t.Y, t.Hidden = int(fx), int(fy), hidden != 0
	return t, nil
}

// storeEncounterTokenHP persists one clamped HP value (mirrors
// vtt.SetTokenHP bounds).
func storeEncounterTokenHP(ctx context.Context, db *sql.DB, mapID, tokenID string, hp int) error {
	_, err := db.ExecContext(ctx, `UPDATE vtt_tokens SET hp = ? WHERE map = ? AND token = ?`, hp, mapID, tokenID)
	return err
}

// listEncounterTokens reads live tokens for the dashboard token list in
// initiative order (unordered extras appended by token id). GM-only callers:
// positions + hidden flags are included (K leak rules: unauthorized viewers
// never reach this — the section and the action are both GM-only).
func listEncounterTokens(ctx context.Context, db *sql.DB, mapID string) []encounterTokenRow {
	if db == nil {
		return nil
	}
	rows, err := db.QueryContext(ctx,
		`SELECT token, name, x, y, hp, max_hp, hidden FROM vtt_tokens WHERE map = ? ORDER BY token`, mapID)
	if err != nil {
		return nil
	}
	defer func() { _ = rows.Close() }()
	var toks []encounterTokenRow
	for rows.Next() {
		var t encounterTokenRow
		var fx, fy float64
		var hidden int
		if err := rows.Scan(&t.TokenID, &t.Name, &fx, &fy, &t.HP, &t.MaxHP, &hidden); err != nil {
			continue
		}
		t.X, t.Y, t.Hidden = int(fx), int(fy), hidden != 0
		toks = append(toks, t)
	}
	irows, err := db.QueryContext(ctx, `SELECT token FROM vtt_initiative WHERE map = ? ORDER BY ord, token`, mapID)
	if err != nil || toks == nil {
		return toks
	}
	defer func() { _ = irows.Close() }()
	byID := map[string]encounterTokenRow{}
	for _, t := range toks {
		byID[t.TokenID] = t
	}
	var ordered []encounterTokenRow
	seen := map[string]bool{}
	for irows.Next() {
		var id string
		if err := irows.Scan(&id); err != nil {
			continue
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		if t, ok := byID[id]; ok {
			ordered = append(ordered, t)
		}
	}
	for _, t := range toks {
		if !seen[t.TokenID] {
			ordered = append(ordered, t)
		}
	}
	return ordered
}

// validEncounterMapID mirrors vtt.ValidMapID (charset + reserved names).
func validEncounterMapID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, r := range id {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			continue
		}
		return false
	}
	switch strings.ToUpper(id) {
	case "CON", "PRN", "AUX", "NUL",
		"COM1", "COM2", "COM3", "COM4", "COM5", "COM6", "COM7", "COM8", "COM9",
		"LPT1", "LPT2", "LPT3", "LPT4", "LPT5", "LPT6", "LPT7", "LPT8", "LPT9":
		return false
	}
	return true
}

func validEncounterTokenID(id string) bool { return validEncounterMapID(id) }

// encounterInput is the closed action set for POST /encounter.
type encounterInput struct {
	Action string
	Map    string
	Counts map[string]int // creature ID -> count (spawn only)
	Token  string         // hp only
	Delta  int            // hp only
	// isForm selects the response shape: dashboard forms get a 303 back,
	// JSON fetch clients get JSON.
	isForm bool
}

// parseEncounterInput reads the closed key set from a JSON body or a
// dashboard form post. Unknown creature IDs are rejected (closed set);
// unknown scalar fields are ignored.
func parseEncounterInput(r *http.Request) (encounterInput, error) {
	var in encounterInput
	in.Counts = map[string]int{}
	if strings.Contains(r.Header.Get("Content-Type"), "application/json") {
		var body struct {
			Action string         `json:"action"`
			Map    string         `json:"map"`
			Counts map[string]int `json:"counts"`
			Token  string         `json:"token"`
			Delta  int            `json:"delta"`
		}
		dec := json.NewDecoder(io.LimitReader(r.Body, maxEncounterBody))
		if err := dec.Decode(&body); err != nil {
			return in, fmt.Errorf("bad request")
		}
		in.Action, in.Map = body.Action, body.Map
		in.Token, in.Delta = body.Token, body.Delta
		for id, n := range body.Counts {
			if !encounterCreatureID(id) {
				return in, fmt.Errorf("unknown creature %q", id)
			}
			in.Counts[id] = n
		}
		return in, nil
	}
	in.isForm = true
	body, err := io.ReadAll(io.LimitReader(r.Body, maxEncounterBody+1))
	if err != nil || int64(len(body)) > maxEncounterBody {
		return in, fmt.Errorf("bad request")
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	if err := r.ParseForm(); err != nil {
		return in, fmt.Errorf("bad request")
	}
	in.Action = r.FormValue("action")
	in.Map = r.FormValue("map")
	in.Token = r.FormValue("token")
	if d := strings.TrimSpace(r.FormValue("delta")); d != "" {
		n, err := strconv.Atoi(d)
		if err != nil {
			return in, fmt.Errorf("bad delta")
		}
		in.Delta = n
	}
	for _, c := range encounterRoster() {
		raw := strings.TrimSpace(r.FormValue(c.ID))
		if raw == "" {
			continue
		}
		n, err := strconv.Atoi(raw)
		if err != nil {
			return in, fmt.Errorf("bad count for %s", c.ID)
		}
		in.Counts[c.ID] = n
	}
	return in, nil
}

func encounterCreatureID(id string) bool {
	for _, c := range encounterRoster() {
		if c.ID == id {
			return true
		}
	}
	return false
}

// encounterAction handles POST /encounter (replaces the F2 501 stub on the
// same route): GM-only build→spawn and HP adjust. Previews are read-only.
func (h *WriteHandlers) encounterAction(w http.ResponseWriter, r *http.Request) {
	v := viewerOf(r)
	if v == nil || v.UserID == "" {
		http.Error(w, "login required", http.StatusUnauthorized)
		return
	}
	if !effGM(v) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if !h.checkCSRF(r) {
		http.Error(w, "bad or missing CSRF token", http.StatusForbidden)
		return
	}
	if h.Store == nil || h.Store.AppDB() == nil {
		http.Error(w, "encounters unavailable", http.StatusInternalServerError)
		return
	}
	in, err := parseEncounterInput(r)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	switch in.Action {
	case "spawn":
		h.encounterSpawn(w, r, in)
	case "hp":
		h.encounterHP(w, r, in)
	default:
		http.Error(w, "unknown action", http.StatusBadRequest)
	}
}

// encounterSpawn validates the build (non-empty, positive counts, creature
// cap), resolves HP server-side, and writes tokens + initiative in one
// action for the chosen map.
func (h *WriteHandlers) encounterSpawn(w http.ResponseWriter, r *http.Request, in encounterInput) {
	if !validEncounterMapID(in.Map) {
		http.Error(w, "bad map", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	roster := encounterRoster()
	counts := map[string]int{}
	total := 0
	for _, c := range roster {
		n := in.Counts[c.ID]
		if n < 0 || n > maxEncounterCreatures {
			http.Error(w, "bad count", http.StatusBadRequest)
			return
		}
		counts[c.ID] = n
		total += n
	}
	if total == 0 {
		http.Error(w, "no creatures picked", http.StatusBadRequest)
		return
	}
	if total > maxEncounterCreatures {
		http.Error(w, fmt.Sprintf("%d creatures exceeds cap %d", total, maxEncounterCreatures), http.StatusUnprocessableEntity)
		return
	}
	// Server-side HP resolution (clients never supply HP).
	var picks []resolvedPick
	for _, c := range roster {
		picks = append(picks, resolvedPick{
			ID: c.ID, Name: c.Name,
			HP:    resolveCreatureHP(ctx, h.Store, c),
			Count: counts[c.ID],
		})
	}
	encounterID := fmt.Sprintf("enc-%d", time.Now().UnixNano())
	toks, order := spawnTokenLayout(encounterID, picks)
	cols, rows := encounterGrid(ctx, h.Store, in.Map)
	db := h.Store.AppDB()
	if err := spawnEncounterTokens(ctx, db, in.Map, toks, order, cols, rows); err != nil {
		http.Error(w, "cannot spawn: "+err.Error(), http.StatusUnprocessableEntity)
		return
	}
	if in.isForm {
		redirectWithIdentity(w, r, "/dashboard?map="+in.Map)
		return
	}
	type tokenJSON struct {
		ID string `json:"id"`
		HP int    `json:"hp"`
	}
	out := struct {
		Map        string      `json:"map"`
		Encounter  string      `json:"encounter"`
		Tokens     []tokenJSON `json:"tokens"`
		Initiative []string    `json:"initiative"`
	}{Map: in.Map, Encounter: encounterID, Initiative: order}
	for _, t := range toks {
		out.Tokens = append(out.Tokens, tokenJSON{ID: t.TokenID, HP: t.HP})
	}
	b, _ := json.Marshal(out)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(b)
}

// encounterHP applies one HP delta to one token (floor 0 / cap max via
// adjustEncounterHP).
func (h *WriteHandlers) encounterHP(w http.ResponseWriter, r *http.Request, in encounterInput) {
	if !validEncounterMapID(in.Map) || !validEncounterTokenID(in.Token) {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if in.Delta < -999 || in.Delta > 999 {
		http.Error(w, "bad delta", http.StatusBadRequest)
		return
	}
	ctx := r.Context()
	db := h.Store.AppDB()
	cur, err := loadEncounterToken(ctx, db, in.Map, in.Token)
	if err != nil {
		http.Error(w, "no such token", http.StatusNotFound)
		return
	}
	next := adjustEncounterHP(cur, in.Delta)
	if err := storeEncounterTokenHP(ctx, db, in.Map, in.Token, next.HP); err != nil {
		http.Error(w, "cannot set HP", http.StatusInternalServerError)
		return
	}
	if in.isForm {
		redirectWithIdentity(w, r, "/dashboard?map="+in.Map)
		return
	}
	b, _ := json.Marshal(map[string]any{
		"map_id": in.Map, "id": next.TokenID, "hp": next.HP, "max_hp": next.MaxHP,
	})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	_, _ = w.Write(b)
}

// buildEncounterData assembles the dashboard encounter section. The selected
// map is the ?map= query, else the first indexed map, else "arena" (empty
// board text, never an error).
func buildEncounterData(ctx context.Context, st store.Store, selected string) templates.EncounterData {
	var out templates.EncounterData
	for _, c := range encounterRoster() {
		out.Roster = append(out.Roster, templates.EncounterCreature{
			ID: c.ID, Name: c.Name, HP: resolveCreatureHP(ctx, st, c),
		})
	}
	if st != nil {
		if pages, err := st.PageList(ctx, store.PageListOptions{Prefix: "maps/", Limit: 100}); err == nil {
			for _, p := range pages {
				id := strings.TrimPrefix(p.Path, "maps/")
				id = strings.TrimSuffix(id, ".md")
				if id != "" && validEncounterMapID(id) {
					out.Maps = append(out.Maps, id)
				}
			}
		}
	}
	out.Map = selected
	if out.Map == "" && len(out.Maps) > 0 {
		out.Map = out.Maps[0]
	}
	if out.Map == "" {
		out.Map = "arena"
	}
	var db *sql.DB
	if st != nil {
		db = st.AppDB()
	}
	for _, t := range listEncounterTokens(ctx, db, out.Map) {
		out.Tokens = append(out.Tokens, templates.EncounterToken{
			ID: t.TokenID, Name: t.Name, X: t.X, Y: t.Y,
			HP: t.HP, MaxHP: t.MaxHP, Hidden: t.Hidden,
		})
	}
	return out
}

// adjust control (text status, never color-only; labels per control).
