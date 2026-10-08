package dice

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/semiplane/yonder/internal/ruleset"
)

// NewLogStore creates a dice log store over the app DB. It reads/writes the
// dice_logs table created by the store migrations (schema owned by Lane B;
// this file only issues DML against its columns). Tests must create the
// same shape in a file-backed temp DB.
func NewLogStore(db *sql.DB) LogStore {
	return &logStore{db: db}
}

type logStore struct {
	db *sql.DB
}

// Save persists entry and assigns its RollID from the row id (decimal).
// Replay addresses that id; the roller's pre-save crypto id is replaced so
// no schema change (no extra roll_id column) is needed.
func (s *logStore) Save(ctx context.Context, entry *LogEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if entry == nil || entry.Result == nil {
		return fmt.Errorf("dice: nil log entry")
	}
	env := entry.Envelope
	if env == nil {
		env = envelopeJSON(LogRequestOf(entry), "")
	}
	envJSON, err := json.Marshal(env)
	if err != nil {
		return err
	}
	modJSON, err := json.Marshal(entry.Modifiers)
	if err != nil {
		return err
	}
	resJSON, err := json.Marshal(entry.Result)
	if err != nil {
		return err
	}
	blind := 0
	if entry.Blind {
		blind = 1
	}
	if entry.Timestamp == 0 {
		entry.Timestamp = time.Now().Unix()
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	r, err := tx.ExecContext(ctx,
		`INSERT INTO dice_logs(session_id, actor, envelope_json, modifiers_json, result_json, blind, created_at)
		 VALUES('', ?, ?, ?, ?, ?, ?)`,
		entry.ActorID, string(envJSON), string(modJSON), string(resJSON), blind, entry.Timestamp)
	if err != nil {
		return err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return err
	}
	entry.RollID = strconv.FormatInt(id, 10)
	entry.Result.RollID = entry.RollID
	resJSON, err = json.Marshal(entry.Result)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE dice_logs SET result_json = ? WHERE id = ?`,
		string(resJSON), id); err != nil {
		return err
	}
	return tx.Commit()
}

// Get loads an entry by the decimal RollID assigned at Save.
func (s *logStore) Get(ctx context.Context, rollID string) (*LogEntry, error) {
	id, err := strconv.ParseInt(rollID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("dice: unknown roll %q", rollID)
	}
	var e LogEntry
	var envJSON, modJSON, resJSON string
	var blind int
	err = s.db.QueryRowContext(ctx,
		`SELECT id, actor, envelope_json, modifiers_json, result_json, blind, created_at
		 FROM dice_logs WHERE id = ?`, id).
		Scan(&id, &e.ActorID, &envJSON, &modJSON, &resJSON, &blind, &e.Timestamp)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("dice: roll %q not found", rollID)
	}
	if err != nil {
		return nil, err
	}
	e.RollID = strconv.FormatInt(id, 10)
	e.Blind = blind != 0
	var env struct {
		Intent string `json:"intent"`
		Viewer string `json:"viewer"`
	}
	if jerr := json.Unmarshal([]byte(envJSON), &env); jerr == nil {
		e.IntentID = env.Intent
	}
	e.ViewerHash = viewerHash(viewerKeyOf(env.Viewer, e.ActorID))
	if modJSON != "" && modJSON != "null" {
		var mods ruleset.Modifiers
		if jerr := json.Unmarshal([]byte(modJSON), &mods); jerr != nil {
			return nil, fmt.Errorf("dice: corrupt modifiers for roll %q", rollID)
		}
		e.Modifiers = &mods
	}
	var res RollResult
	if jerr := json.Unmarshal([]byte(resJSON), &res); jerr != nil {
		return nil, fmt.Errorf("dice: corrupt result for roll %q", rollID)
	}
	e.Result = &res
	e.Seed = append([]byte(nil), res.Seed...)
	e.Notation = res.Notation
	e.Broadcast = false
	return &e, nil
}

// List returns recent entries for an actor, newest first.
func (s *logStore) List(ctx context.Context, actorID string, limit int) ([]*LogEntry, error) {
	if limit <= 0 || limit > 100 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, actor, envelope_json, modifiers_json, result_json, blind, created_at
		 FROM dice_logs WHERE actor = ? ORDER BY id DESC LIMIT ?`, actorID, limit)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []*LogEntry
	for rows.Next() {
		var id int64
		var e LogEntry
		var envJSON, modJSON, resJSON string
		var blind int
		if err := rows.Scan(&id, &e.ActorID, &envJSON, &modJSON, &resJSON, &blind, &e.Timestamp); err != nil {
			return nil, err
		}
		e.RollID = strconv.FormatInt(id, 10)
		e.Blind = blind != 0
		var env struct {
			Intent string `json:"intent"`
			Viewer string `json:"viewer"`
		}
		if jerr := json.Unmarshal([]byte(envJSON), &env); jerr == nil {
			e.IntentID = env.Intent
		}
		e.ViewerHash = viewerHash(viewerKeyOf(env.Viewer, e.ActorID))
		if modJSON != "" && modJSON != "null" {
			var mods ruleset.Modifiers
			if jerr := json.Unmarshal([]byte(modJSON), &mods); jerr != nil {
				return nil, fmt.Errorf("dice: corrupt modifiers for roll %q", e.RollID)
			}
			e.Modifiers = &mods
		}
		var res RollResult
		if jerr := json.Unmarshal([]byte(resJSON), &res); jerr != nil {
			return nil, fmt.Errorf("dice: corrupt result for roll %q", e.RollID)
		}
		e.Result = &res
		e.Seed = append([]byte(nil), res.Seed...)
		e.Notation = res.Notation
		out = append(out, &e)
	}
	return out, rows.Err()
}

// PurgeOld removes entries older than retention days (0 = disabled).
func (s *logStore) PurgeOld(ctx context.Context, days int) error {
	if days <= 0 {
		return nil
	}
	cutoff := time.Now().Unix() - int64(days)*86400
	_, err := s.db.ExecContext(ctx, `DELETE FROM dice_logs WHERE created_at < ?`, cutoff)
	return err
}

// LogRequestOf rebuilds a roll request view from a stored entry for
// envelope serialization. Only envelope fields are populated.
func LogRequestOf(entry *LogEntry) RollRequest {
	return RollRequest{
		ActorID:  entry.ActorID,
		IntentID: entry.IntentID,
		Notation: entry.Notation,
	}
}

// viewerKeyOf resolves the stored viewer routing key, falling back to the
// transport default ("actor:"+actor) for rows written before the viewer key
// was persisted.
func viewerKeyOf(stored, actorID string) string {
	if stored != "" {
		return stored
	}
	return "actor:" + actorID
}
