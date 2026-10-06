package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Claim-token errors.
var (
	ErrClaimNotFound = errors.New("auth: claim token not found")
	ErrClaimExpired  = errors.New("auth: claim token expired")
	ErrClaimRedeemed = errors.New("auth: claim token already redeemed")
)

// DefaultClaimTTL bounds a shareable wizard/invite link (P07/P11).
const DefaultClaimTTL = 7 * 24 * time.Hour

// ClaimToken is a single- (or few-)use invite issued by the GM. Only the
// SHA-256 hash is stored; the raw token is shown once at issue time and
// travels in the wizard link. Redeeming creates the player account.
type ClaimToken struct {
	TokenHash   string
	CreatedBy   string
	NewUsername string // pre-assigned username; empty = redeemer chooses
	IsGM        bool
	Note        string
	CreatedAt   int64
	ExpiresAt   int64
	RedeemedAt  int64
	MaxUses     int
	Uses        int
}

// ClaimStore persists claim tokens in the app DB.
type ClaimStore struct {
	db *sql.DB
}

// NewClaimStore creates a ClaimStore, ensuring the auth schema exists.
func NewClaimStore(db *sql.DB) (*ClaimStore, error) {
	if db == nil {
		return nil, errors.New("auth: nil database")
	}
	if err := EnsureAuthSchema(db); err != nil {
		return nil, fmt.Errorf("auth: ensure schema: %w", err)
	}
	return &ClaimStore{db: db}, nil
}

// IssueClaim creates a claim token and returns the raw token (shown once).
// newUsername may be empty to let the redeemer pick; otherwise redeem must
// use the same name. ttl <= 0 selects DefaultClaimTTL.
func (s *ClaimStore) IssueClaim(ctx context.Context, createdBy, newUsername string, isGM bool, note string, ttl time.Duration, now time.Time) (string, error) {
	if createdBy == "" {
		return "", errors.New("auth: claim issuer required")
	}
	if newUsername != "" {
		if err := ValidateUsername(newUsername); err != nil {
			return "", err
		}
	}
	if ttl <= 0 {
		ttl = DefaultClaimTTL
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("auth: claim token generation: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(token))
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO claim_tokens(token_hash, created_by, new_username, is_gm, note, created_at, expires_at, max_uses, uses)
		 VALUES(?, ?, ?, ?, ?, ?, ?, 1, 0)`,
		hex.EncodeToString(sum[:]), createdBy, newUsername, boolToInt(isGM), note,
		now.Unix(), now.Add(ttl).Unix())
	if err != nil {
		return "", fmt.Errorf("auth: issue claim: %w", err)
	}
	return token, nil
}

// RedeemClaim validates a raw claim token and creates the account with the
// given password. If the claim pre-assigns a username, username must match
// (case-insensitively). Single-use by default: a second redeem fails.
func (s *ClaimStore) RedeemClaim(ctx context.Context, users UserStore, token, username, password string, now time.Time) (*User, error) {
	if token == "" {
		return nil, ErrClaimNotFound
	}
	sum := sha256.Sum256([]byte(token))
	row := s.db.QueryRowContext(ctx,
		`SELECT token_hash, created_by, new_username, is_gm, note, created_at, expires_at, redeemed_at, max_uses, uses
		 FROM claim_tokens WHERE token_hash = ?`, hex.EncodeToString(sum[:]))
	var c ClaimToken
	var isGM int
	var note, createdBy string
	if err := row.Scan(&c.TokenHash, &createdBy, &c.NewUsername, &isGM, &note, &c.CreatedAt, &c.ExpiresAt, &c.RedeemedAt, &c.MaxUses, &c.Uses); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrClaimNotFound
		}
		return nil, fmt.Errorf("auth: redeem claim lookup: %w", err)
	}
	c.CreatedBy = createdBy
	c.IsGM = isGM != 0
	c.Note = note
	if now.Unix() > c.ExpiresAt {
		return nil, ErrClaimExpired
	}
	if c.Uses >= c.MaxUses {
		return nil, ErrClaimRedeemed
	}
	if c.NewUsername != "" && !equalFold(c.NewUsername, username) {
		return nil, fmt.Errorf("auth: claim token is for a different username: %w", ErrClaimNotFound)
	}
	user, err := CreateUser(ctx, users, username, password, c.IsGM, now.Unix())
	if err != nil {
		return nil, err
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE claim_tokens SET uses = uses + 1, redeemed_at = ? WHERE token_hash = ? AND uses < max_uses`,
		now.Unix(), c.TokenHash)
	if err != nil {
		return nil, fmt.Errorf("auth: redeem claim update: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return nil, fmt.Errorf("auth: redeem claim update: %w", err)
	}
	if n == 0 {
		// Lost a race with another redeem; the account was already created
		// above, surface the conflict instead of silently double-spending.
		return nil, ErrClaimRedeemed
	}
	return user, nil
}

// RevokeClaim deletes a claim token so the link stops working.
func (s *ClaimStore) RevokeClaim(ctx context.Context, token string) error {
	sum := sha256.Sum256([]byte(token))
	res, err := s.db.ExecContext(ctx, `DELETE FROM claim_tokens WHERE token_hash = ?`, hex.EncodeToString(sum[:]))
	if err != nil {
		return fmt.Errorf("auth: revoke claim: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("auth: revoke claim: %w", err)
	}
	if n == 0 {
		return ErrClaimNotFound
	}
	return nil
}

// CleanupExpiredClaims deletes spent or expired claim tokens.
func (s *ClaimStore) CleanupExpiredClaims(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM claim_tokens WHERE expires_at <= ? OR uses >= max_uses`, now.Unix())
	if err != nil {
		return fmt.Errorf("auth: cleanup claims: %w", err)
	}
	return nil
}
