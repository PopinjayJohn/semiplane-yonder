package web

// Wizard drafts (Lane I1, Phase 3): claim-token + resume state in SQLite.
//
// Table `wizard_drafts` is created by the app migration (Lane B owns the
// DDL); this file only USES it — same column shape, no second DDL source
// (pitfalls: two DDL sources diverge). Draft rows double as single-use claim
// tokens for the player wizard (p07: GM-issued per-slug link, redemption
// invalidates the token):
//   - GM issues: Save(token, {slug, username, ...}) -> link /c/<token>/create
//   - Player steps: Get (resume) + Save (partial progress)
//   - Finalize: Delete (single-use: second finalize finds no draft)

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// DraftTTL bounds a wizard claim link (matches auth claim TTL scale).
const DraftTTL = 7 * 24 * time.Hour

// WizardDraft is the resume/claim payload stored as JSON in wizard_drafts.
type WizardDraft struct {
	Slug      string            `json:"slug"`
	Username  string            `json:"username"`
	Step      string            `json:"step"`
	Fields    map[string]string `json:"fields"`
	CreatedAt int64             `json:"created_at"`
	ExpiresAt int64             `json:"expires_at"`
}

// DraftStore persists wizard drafts in the app DB (wizard_drafts table).
type DraftStore struct {
	db *sql.DB
}

// NewDraftStore wraps db (must be the app DB; migrations create the table).
// The table is ensured IF NOT EXISTS with the migration-identical shape so
// tests opening a bare DB still work — column order and types match
// migrations/app/0001_app_auth.sql verbatim.
func NewDraftStore(db *sql.DB) (*DraftStore, error) {
	if db == nil {
		return nil, fmt.Errorf("web: nil draft database")
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS wizard_drafts (
		token TEXT PRIMARY KEY,
		payload TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		expires INTEGER NOT NULL
	)`); err != nil {
		return nil, fmt.Errorf("web: ensure wizard_drafts: %w", err)
	}
	return &DraftStore{db: db}, nil
}

// NewDraftToken mints a URL-safe claim token (crypto/rand, 256-bit).
func NewDraftToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("web: draft token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// Save inserts or replaces a draft (upsert: resume across steps).
func (d *DraftStore) Save(ctx context.Context, token string, draft *WizardDraft) error {
	if token == "" || draft == nil {
		return fmt.Errorf("web: draft token and payload required")
	}
	payload, err := json.Marshal(draft)
	if err != nil {
		return fmt.Errorf("web: draft marshal: %w", err)
	}
	_, err = d.db.ExecContext(ctx,
		`INSERT INTO wizard_drafts(token, payload, created_at, expires) VALUES(?,?,?,?)
		 ON CONFLICT(token) DO UPDATE SET payload=excluded.payload, expires=excluded.expires`,
		token, string(payload), draft.CreatedAt, draft.ExpiresAt)
	if err != nil {
		return fmt.Errorf("web: draft save: %w", err)
	}
	return nil
}

// ErrDraftNotFound surfaces when a claim token is unknown, expired, or
// already consumed (single-use). Handlers map it to 404 without distinguishing.
var ErrDraftNotFound = fmt.Errorf("web: wizard draft not found")

// Get loads a draft; expired rows are deleted and report ErrDraftNotFound.
func (d *DraftStore) Get(ctx context.Context, token string, now time.Time) (*WizardDraft, error) {
	if token == "" {
		return nil, ErrDraftNotFound
	}
	var payload string
	var expires int64
	err := d.db.QueryRowContext(ctx,
		`SELECT payload, expires FROM wizard_drafts WHERE token = ?`, token).Scan(&payload, &expires)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, ErrDraftNotFound
		}
		return nil, fmt.Errorf("web: draft lookup: %w", err)
	}
	if now.Unix() > expires {
		_, _ = d.db.ExecContext(ctx, `DELETE FROM wizard_drafts WHERE token = ?`, token)
		return nil, ErrDraftNotFound
	}
	var draft WizardDraft
	if err := json.Unmarshal([]byte(payload), &draft); err != nil {
		return nil, fmt.Errorf("web: draft decode: %w", err)
	}
	if draft.Fields == nil {
		draft.Fields = map[string]string{}
	}
	return &draft, nil
}

// Delete consumes a draft (claim redemption invalidates the token).
func (d *DraftStore) Delete(ctx context.Context, token string) error {
	res, err := d.db.ExecContext(ctx, `DELETE FROM wizard_drafts WHERE token = ?`, token)
	if err != nil {
		return fmt.Errorf("web: draft delete: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("web: draft delete: %w", err)
	}
	if n == 0 {
		return ErrDraftNotFound
	}
	return nil
}
