package vtt

import (
	"context"
	"database/sql"
	"sort"

	"github.com/semiplane/yonder/internal/store"
)

// Token is one live board token (app DB row, Lane K live state).
type Token struct {
	ID        string
	Name      string
	X, Y      int
	HP, MaxHP int
	Hidden    bool
	Character string // linked character page ("" = none)
}

// InitEntry is one initiative row (ordering only; the tracker feature owns
// roll semantics — VTT renders + feeds token order per P08).
type InitEntry struct {
	Token string
	Ord   int
}

// MapState is the full live state of one map (unfiltered; FilterFor projects
// per viewer in snapshot.go).
type MapState struct {
	Tokens []Token
	// FogMask is the stored mask; FogPresent=false means no row (fail closed).
	FogMask    string
	FogPresent bool
	Initiative []InitEntry
}

func appDB(st store.Store) *sql.DB { return st.AppDB() }

// LoadState reads the live rows for a map. Missing tables/rows are not
// errors: an empty state fails closed downstream (fog hidden, no tokens).
func LoadState(ctx context.Context, st store.Store, mapID string) (MapState, error) {
	var out MapState
	db := appDB(st)
	if db == nil {
		return out, nil
	}
	rows, err := db.QueryContext(ctx,
		`SELECT token, name, x, y, hp, max_hp, hidden, character FROM vtt_tokens WHERE map = ? ORDER BY token`, mapID)
	if err != nil {
		return out, nil // pre-0003 DB or missing table: fail closed, not fatal
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var t Token
		var x, y float64
		var hidden int
		if err := rows.Scan(&t.ID, &t.Name, &x, &y, &t.HP, &t.MaxHP, &hidden, &t.Character); err != nil {
			return out, nil
		}
		t.X, t.Y, t.Hidden = int(x), int(y), hidden != 0
		out.Tokens = append(out.Tokens, t)
	}
	_ = rows.Err()
	var mask sql.NullString
	if err := db.QueryRowContext(ctx, `SELECT mask FROM vtt_fog WHERE map = ?`, mapID).Scan(&mask); err == nil && mask.Valid {
		out.FogMask, out.FogPresent = mask.String, true
	}
	irows, err := db.QueryContext(ctx, `SELECT token, ord FROM vtt_initiative WHERE map = ? ORDER BY ord, token`, mapID)
	if err != nil {
		return out, nil
	}
	defer func() { _ = irows.Close() }()
	for irows.Next() {
		var e InitEntry
		if err := irows.Scan(&e.Token, &e.Ord); err != nil {
			return out, nil
		}
		out.Initiative = append(out.Initiative, e)
	}
	return out, nil
}

// ClampToken validates + clamps a token write against the grid. Names are
// truncated, HP clamped into [0,maxHP], positions must sit on the grid.
func ClampToken(g Grid, t Token) (Token, bool) {
	if !ValidTokenID(t.ID) {
		return Token{}, false
	}
	t.Name = truncate(t.Name, 80)
	if t.Name == "" {
		t.Name = t.ID
	}
	if t.MaxHP < 0 || t.MaxHP > 9999 || t.HP < 0 || t.HP > 9999 {
		return Token{}, false
	}
	if t.HP > t.MaxHP {
		t.HP = t.MaxHP
	}
	if t.X < 0 || t.Y < 0 || t.X >= g.Cols || t.Y >= g.Rows {
		return Token{}, false
	}
	if t.Character != "" && !cleanPageRef(t.Character) {
		return Token{}, false
	}
	return t, true
}

// UpsertToken writes one token row (GM or authorized mover — authorization
// lives in handlers.go; this is persistence only).
func UpsertToken(ctx context.Context, st store.Store, mapID string, t Token) error {
	hidden := 0
	if t.Hidden {
		hidden = 1
	}
	_, err := appDB(st).ExecContext(ctx,
		`INSERT INTO vtt_tokens(map, token, x, y, hidden, name, hp, max_hp, character)
		  VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
		  ON CONFLICT(map, token) DO UPDATE SET x=excluded.x, y=excluded.y,
		    hidden=excluded.hidden, name=excluded.name, hp=excluded.hp,
		    max_hp=excluded.max_hp, character=excluded.character`,
		mapID, t.ID, float64(t.X), float64(t.Y), hidden, t.Name, t.HP, t.MaxHP, t.Character)
	return err
}

// MoveToken moves one token, returning the stored row.
func MoveToken(ctx context.Context, st store.Store, mapID, tokenID string, x, y int) (Token, error) {
	db := appDB(st)
	var t Token
	var fx, fy float64
	var hidden int
	err := db.QueryRowContext(ctx,
		`SELECT token, name, x, y, hp, max_hp, hidden, character FROM vtt_tokens WHERE map = ? AND token = ?`,
		mapID, tokenID).Scan(&t.ID, &t.Name, &fx, &fy, &t.HP, &t.MaxHP, &hidden, &t.Character)
	if err != nil {
		return Token{}, err
	}
	t.Hidden = hidden != 0
	t.X, t.Y = x, y
	_, err = db.ExecContext(ctx, `UPDATE vtt_tokens SET x = ?, y = ? WHERE map = ? AND token = ?`,
		float64(x), float64(y), mapID, tokenID)
	return t, err
}

// SetTokenHP sets one token's HP (clamped), returning the stored row.
func SetTokenHP(ctx context.Context, st store.Store, mapID, tokenID string, hp int) (Token, error) {
	db := appDB(st)
	var t Token
	var fx, fy float64
	var hidden int
	err := db.QueryRowContext(ctx,
		`SELECT token, name, x, y, hp, max_hp, hidden, character FROM vtt_tokens WHERE map = ? AND token = ?`,
		mapID, tokenID).Scan(&t.ID, &t.Name, &fx, &fy, &t.HP, &t.MaxHP, &hidden, &t.Character)
	if err != nil {
		return Token{}, err
	}
	t.Hidden = hidden != 0
	t.X, t.Y = int(fx), int(fy)
	if hp < 0 {
		hp = 0
	}
	if hp > t.MaxHP {
		hp = t.MaxHP
	}
	t.HP = hp
	_, err = db.ExecContext(ctx, `UPDATE vtt_tokens SET hp = ? WHERE map = ? AND token = ?`, hp, mapID, tokenID)
	return t, err
}

// SetFog replaces the hidden-rect mask (GM-only; callers validate rects via
// EncodeMask against the grid first).
func SetFog(ctx context.Context, st store.Store, mapID, mask string) error {
	_, err := appDB(st).ExecContext(ctx,
		`INSERT INTO vtt_fog(map, mask) VALUES(?, ?)
		  ON CONFLICT(map) DO UPDATE SET mask=excluded.mask`, mapID, mask)
	return err
}

// SetInitiative replaces the initiative order (GM-only). Unknown token ids
// are dropped (fail closed); order follows the array position.
func SetInitiative(ctx context.Context, st store.Store, mapID string, order []string) error {
	db := appDB(st)
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM vtt_initiative WHERE map = ?`, mapID); err != nil {
		return err
	}
	seen := map[string]bool{}
	ord := 0
	for _, id := range order {
		if !ValidTokenID(id) || seen[id] {
			continue
		}
		seen[id] = true
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

// SpawnTokens executes an encounter-spawn intent: all tokens + initiative in
// ONE transaction (P07/P08 run-mode hook). Token ids outside the charset,
// off-grid positions, or duplicates-in-batch are rejected whole (fail
// closed); existing live tokens with colliding ids are left untouched unless
// overwrite is set (spawn-then-adjust flows).
func SpawnTokens(ctx context.Context, st store.Store, mapID string, toks []Token, order []string, g Grid) error {
	db := appDB(st)
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
		ct, ok := ClampToken(g, t)
		if !ok {
			return &StateError{Op: "spawn", Msg: "bad token " + t.ID}
		}
		hidden := 0
		if ct.Hidden {
			hidden = 1
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO vtt_tokens(map, token, x, y, hidden, name, hp, max_hp, character)
			  VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)
			  ON CONFLICT(map, token) DO UPDATE SET x=excluded.x, y=excluded.y,
			    hidden=excluded.hidden, name=excluded.name, hp=excluded.hp,
			    max_hp=excluded.max_hp, character=excluded.character`,
			mapID, ct.ID, float64(ct.X), float64(ct.Y), hidden, ct.Name, ct.HP, ct.MaxHP, ct.Character); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM vtt_initiative WHERE map = ?`, mapID); err != nil {
		return err
	}
	ord := 0
	for _, id := range order {
		if !ValidTokenID(id) {
			continue
		}
		var exists int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM vtt_tokens WHERE map = ? AND token = ?`, mapID, id).Scan(&exists); err != nil {
			continue
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

// ResetToSidecar reseeds live tokens + fog from sidecar defaults (GM-only
// "reset to sidecar" after data-dir loss; P08). One transaction.
func ResetToSidecar(ctx context.Context, st store.Store, mapID string, c Calibration) error {
	db := appDB(st)
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
	if _, err := tx.ExecContext(ctx, `DELETE FROM vtt_tokens WHERE map = ?`, mapID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM vtt_initiative WHERE map = ?`, mapID); err != nil {
		return err
	}
	for i, d := range c.Defaults {
		hidden := 0
		if d.Hidden {
			hidden = 1
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO vtt_tokens(map, token, x, y, hidden, name, hp, max_hp, character)
			  VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			mapID, d.ID, float64(d.X), float64(d.Y), hidden, d.Name, d.HP, d.MaxHP, d.Character); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO vtt_initiative(map, token, ord) VALUES(?, ?, ?)`, mapID, d.ID, i); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO vtt_fog(map, mask) VALUES(?, ?)
		  ON CONFLICT(map) DO UPDATE SET mask=excluded.mask`, mapID, sidecarFogMask(c)); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

// SortedTokens returns tokens in stable id order (deterministic renders).
func SortedTokens(toks []Token) []Token {
	out := append([]Token(nil), toks...)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// StateError is a Lane K write-path failure (4xx-class, no secret content).
type StateError struct {
	Op  string
	Msg string
}

func (e *StateError) Error() string { return "vtt " + e.Op + ": " + e.Msg }
