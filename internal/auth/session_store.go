package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Session and user errors.
var (
	// ErrSessionNotFound covers missing and revoked sessions alike: callers
	// must not learn which of the two a probe hit.
	ErrSessionNotFound = errors.New("auth: session not found")
	// ErrSessionExpired is returned when the absolute or idle deadline passed.
	ErrSessionExpired = errors.New("auth: session expired")
	// ErrSessionRevoked is returned on session_version mismatch
	// (reset-password / revoke-all) or user binding mismatch.
	ErrSessionRevoked = errors.New("auth: session revoked")
	// ErrInvalidSession covers malformed cookies, HMAC failures, and
	// sessions whose user row is gone.
	ErrInvalidSession = errors.New("auth: invalid session")
)

// Session key files live directly under the data dir, never in the vault:
// SessionKeyFile is the active key, SessionKeyPrevFile the rotated-out key
// honored for PrevKeyGrace after rotation.
const (
	SessionKeyFile     = ".sessionkey"
	SessionKeyPrevFile = ".sessionkey.prev"
	// PrevKeyGrace is how long cookies signed by the previous key still
	// verify after rotation (P11: 7 days).
	PrevKeyGrace = 7 * 24 * time.Hour
)

// NewSessionStore creates a session store backed by the app DB,
// ensuring the auth schema exists.
func NewSessionStore(db *sql.DB) (SessionStore, error) {
	if db == nil {
		return nil, errors.New("auth: nil database")
	}
	if err := EnsureAuthSchema(db); err != nil {
		return nil, fmt.Errorf("auth: ensure schema: %w", err)
	}
	return &SessionStoreSQL{db: db}, nil
}

// LoadOrGenerateKey loads the session signing key from
// <data-dir>/.sessionkey (0600, never in the vault) or generates and stores
// one. The key is never logged.
func LoadOrGenerateKey(dataDir string) ([32]byte, error) {
	var zero [32]byte
	if dataDir == "" {
		return zero, errors.New("auth: data dir required")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return zero, fmt.Errorf("auth: create data dir: %w", err)
	}
	path := filepath.Join(dataDir, SessionKeyFile)
	if raw, err := os.ReadFile(path); err == nil {
		return parseKeyFile(raw)
	} else if !errors.Is(err, os.ErrNotExist) {
		return zero, fmt.Errorf("auth: read session key: %w", err)
	}
	key, err := GenerateKey()
	if err != nil {
		return zero, err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			// Lost a race with another process: use its key.
			raw, rerr := os.ReadFile(path)
			if rerr != nil {
				return zero, fmt.Errorf("auth: read session key: %w", rerr)
			}
			return parseKeyFile(raw)
		}
		return zero, fmt.Errorf("auth: write session key: %w", err)
	}
	if _, werr := f.Write(key[:]); werr != nil {
		_ = f.Close()
		return zero, fmt.Errorf("auth: write session key: %w", werr)
	}
	if cerr := f.Close(); cerr != nil {
		return zero, fmt.Errorf("auth: write session key: %w", cerr)
	}
	return key, nil
}

func parseKeyFile(raw []byte) ([32]byte, error) {
	var zero [32]byte
	if len(raw) != 32 {
		return zero, errors.New("auth: corrupt session key file (want 32 raw bytes)")
	}
	var key [32]byte
	copy(key[:], raw)
	return key, nil
}

// RotateSessionKey moves the current key to .sessionkey.prev and generates a
// fresh active key. Cookies signed by the previous key verify for
// PrevKeyGrace (see LoadSessionKeys). Backs the rotate-session-key CLI.
func RotateSessionKey(dataDir string) ([32]byte, error) {
	var zero [32]byte
	if dataDir == "" {
		return zero, errors.New("auth: data dir required")
	}
	cur := filepath.Join(dataDir, SessionKeyFile)
	prev := filepath.Join(dataDir, SessionKeyPrevFile)
	if _, err := os.Stat(cur); err != nil {
		return zero, fmt.Errorf("auth: rotate with no active key: %w", err)
	}
	if err := os.Rename(cur, prev); err != nil {
		return zero, fmt.Errorf("auth: rotate session key: %w", err)
	}
	key, err := GenerateKey()
	if err != nil {
		return zero, err
	}
	if err := os.WriteFile(cur, key[:], 0o600); err != nil {
		return zero, fmt.Errorf("auth: write session key: %w", err)
	}
	return key, nil
}

// LoadSessionKeys returns the active key plus the previous key when it is
// still inside PrevKeyGrace (rotation overlap). Cookie verification tries
// each in order.
func LoadSessionKeys(dataDir string) ([][32]byte, error) {
	current, err := LoadOrGenerateKey(dataDir)
	if err != nil {
		return nil, err
	}
	keys := [][32]byte{current}
	fi, err := os.Stat(filepath.Join(dataDir, SessionKeyPrevFile))
	if err != nil {
		return keys, nil // no previous key: not an error
	}
	if time.Since(fi.ModTime()) > PrevKeyGrace {
		return keys, nil
	}
	raw, err := os.ReadFile(filepath.Join(dataDir, SessionKeyPrevFile))
	if err != nil {
		return keys, nil // unreadable prev key: active key still works
	}
	if prev, perr := parseKeyFile(raw); perr == nil && prev != current {
		keys = append(keys, prev)
	}
	return keys, nil
}

// SessionStoreSQL is the SQLite implementation of SessionStore.
// Safe for concurrent use; the driver serializes the single writer.
type SessionStoreSQL struct {
	db *sql.DB
}

// Create inserts a new session. An empty ID is generated (128-bit random).
// last_seen starts at creation; revoked rows are never resurrected.
func (s *SessionStoreSQL) Create(ctx context.Context, session *Session) (string, error) {
	if session == nil {
		return "", errors.New("auth: nil session")
	}
	id := session.ID
	if id == "" {
		var err error
		id, err = NewSessionID()
		if err != nil {
			return "", err
		}
	} else if !ValidateSessionID(id) {
		return "", errors.New("auth: malformed session id")
	}
	if session.UserID == "" {
		return "", errors.New("auth: session user required")
	}
	if session.CSRFToken == "" {
		return "", errors.New("auth: session csrf token required")
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO auth_sessions(id, user_id, created_at, expires_at, idle_at, last_seen, csrf_token, version, revoked)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, 0)`,
		id, session.UserID, session.CreatedAt, session.ExpiresAt, session.IdleAt,
		session.CreatedAt, session.CSRFToken, session.Version)
	if err != nil {
		return "", fmt.Errorf("auth: create session: %w", err)
	}
	session.ID = id
	return id, nil
}

// Get retrieves a session by ID. Revoked rows report as not found.
func (s *SessionStoreSQL) Get(ctx context.Context, id string) (*Session, error) {
	if !ValidateSessionID(id) {
		return nil, ErrSessionNotFound
	}
	var sess Session
	var csrf string
	var revoked int
	err := s.db.QueryRowContext(ctx,
		`SELECT id, user_id, created_at, expires_at, idle_at, csrf_token, version, revoked
		 FROM auth_sessions WHERE id = ?`, id).
		Scan(&sess.ID, &sess.UserID, &sess.CreatedAt, &sess.ExpiresAt, &sess.IdleAt, &csrf, &sess.Version, &revoked)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrSessionNotFound
		}
		return nil, fmt.Errorf("auth: get session: %w", err)
	}
	if revoked != 0 {
		return nil, ErrSessionNotFound
	}
	sess.CSRFToken = csrf
	return &sess, nil
}

// Update rewrites a session row's mutable fields. last_seen is preserved
// (use TouchSession to advance it); revoked rows cannot be updated.
func (s *SessionStoreSQL) Update(ctx context.Context, session *Session) error {
	if session == nil || !ValidateSessionID(session.ID) {
		return ErrSessionNotFound
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE auth_sessions SET user_id = ?, created_at = ?, expires_at = ?, idle_at = ?,
		 csrf_token = ?, version = ? WHERE id = ? AND revoked = 0`,
		session.UserID, session.CreatedAt, session.ExpiresAt, session.IdleAt,
		session.CSRFToken, session.Version, session.ID)
	if err != nil {
		return fmt.Errorf("auth: update session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("auth: update session: %w", err)
	}
	if n == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// Delete removes a session row (logout / single revoke).
func (s *SessionStoreSQL) Delete(ctx context.Context, id string) error {
	if !ValidateSessionID(id) {
		return ErrSessionNotFound
	}
	res, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("auth: delete session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("auth: delete session: %w", err)
	}
	if n == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// DeleteByUser removes all sessions for a user (revoke-all companion to the
// session_version bump).
func (s *SessionStoreSQL) DeleteByUser(ctx context.Context, userID string) error {
	if userID == "" {
		return errors.New("auth: session user required")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM auth_sessions WHERE user_id = ?`, userID); err != nil {
		return fmt.Errorf("auth: delete user sessions: %w", err)
	}
	return nil
}

// CleanupExpired removes sessions past either deadline.
func (s *SessionStoreSQL) CleanupExpired(ctx context.Context) error {
	now := time.Now().Unix()
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM auth_sessions WHERE expires_at <= ? OR idle_at <= ?`, now, now)
	if err != nil {
		return fmt.Errorf("auth: cleanup sessions: %w", err)
	}
	return nil
}

// TouchSession advances last_seen and the sliding idle deadline. Used by
// MaybeRefreshSession; Update deliberately leaves last_seen alone.
func (s *SessionStoreSQL) TouchSession(ctx context.Context, id string, nowUnix, idleAtUnix int64) error {
	if !ValidateSessionID(id) {
		return ErrSessionNotFound
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE auth_sessions SET last_seen = ?, idle_at = ? WHERE id = ? AND revoked = 0`,
		nowUnix, idleAtUnix, id)
	if err != nil {
		return fmt.Errorf("auth: touch session: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("auth: touch session: %w", err)
	}
	if n == 0 {
		return ErrSessionNotFound
	}
	return nil
}

// LastSeen returns the stored last_seen timestamp for a session.
func (s *SessionStoreSQL) LastSeen(ctx context.Context, id string) (int64, error) {
	if !ValidateSessionID(id) {
		return 0, ErrSessionNotFound
	}
	var lastSeen int64
	err := s.db.QueryRowContext(ctx,
		`SELECT last_seen FROM auth_sessions WHERE id = ? AND revoked = 0`, id).Scan(&lastSeen)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrSessionNotFound
		}
		return 0, fmt.Errorf("auth: session last_seen: %w", err)
	}
	return lastSeen, nil
}

// ListByUser lists live (non-revoked) sessions for a user, newest first.
// GM session management UI; never emits CSRF tokens — callers needing them
// use Get on a single row.
func (s *SessionStoreSQL) ListByUser(ctx context.Context, userID string) ([]*Session, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, user_id, created_at, expires_at, idle_at, csrf_token, version
		 FROM auth_sessions WHERE user_id = ? AND revoked = 0 ORDER BY created_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("auth: list sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*Session
	for rows.Next() {
		var sess Session
		if err := rows.Scan(&sess.ID, &sess.UserID, &sess.CreatedAt, &sess.ExpiresAt,
			&sess.IdleAt, &sess.CSRFToken, &sess.Version); err != nil {
			return nil, fmt.Errorf("auth: list sessions: %w", err)
		}
		out = append(out, &sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: list sessions: %w", err)
	}
	return out, nil
}

// User represents a user account.
// Username is the primary key (users.name): immutable, no rename in v1
// (Phase 0c; ownership transfer via transfer-ownership CLI). There is no
// separate numeric ID — Session.UserID, Viewer.UserID, and auth_sessions.user_id
// all carry the username.
//
// SessionVersion is the revoke-all counter: every issued session pins the
// value at login, and reset-password / revoke-all bumps it so older
// sessions fail validation even if their rows survive. (Lane-C additive
// field on the Phase-0 stub; amend filed for the migration copy.)
type User struct {
	Username       string
	PasswordHash   string // argon2id PHC
	IsGM           bool
	CreatedAt      int64
	UpdatedAt      int64
	LastLogin      int64
	FailedLogins   int
	LockedUntil    int64
	SessionVersion int
}

// UserStore defines the interface for user persistence (keyed by username).
type UserStore interface {
	Create(ctx context.Context, user *User) error
	GetByUsername(ctx context.Context, username string) (*User, error)
	Update(ctx context.Context, user *User) error
	Delete(ctx context.Context, username string) error
	List(ctx context.Context) ([]*User, error)
}

// NewUserStore creates a user store backed by the app DB,
// ensuring the auth schema exists.
func NewUserStore(db *sql.DB) (UserStore, error) {
	if db == nil {
		return nil, errors.New("auth: nil database")
	}
	if err := EnsureAuthSchema(db); err != nil {
		return nil, fmt.Errorf("auth: ensure schema: %w", err)
	}
	return &UserStoreSQL{db: db}, nil
}

// ErrUserNotFound is returned when a user is not found.
var ErrUserNotFound = errors.New("user not found")

// ErrInvalidCredentials is returned for invalid login.
var ErrInvalidCredentials = errors.New("invalid credentials")

// ErrAccountLocked is returned when account is locked.
var ErrAccountLocked = errors.New("account locked")

// UserStoreSQL is the SQLite implementation of UserStore.
type UserStoreSQL struct {
	db *sql.DB
}

func scanUser(row interface{ Scan(...any) error }) (*User, error) {
	var u User
	var isGM int
	if err := row.Scan(&u.Username, &u.PasswordHash, &isGM, &u.CreatedAt,
		&u.UpdatedAt, &u.LastLogin, &u.FailedLogins, &u.LockedUntil, &u.SessionVersion); err != nil {
		return nil, err
	}
	u.IsGM = isGM != 0
	return &u, nil
}

const userColumns = `name, password_hash, is_gm, created_at, updated_at, last_login, failed_logins, locked_until, session_version`

// Create inserts a user row; usernames must be pre-validated (CreateUser).
func (s *UserStoreSQL) Create(ctx context.Context, user *User) error {
	if user == nil || user.Username == "" || user.PasswordHash == "" {
		return errors.New("auth: username and password hash required")
	}
	if user.SessionVersion <= 0 {
		user.SessionVersion = 1
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users(name, password_hash, is_gm, created_at, updated_at, last_login, failed_logins, locked_until, session_version)
		 VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		user.Username, user.PasswordHash, boolToInt(user.IsGM), user.CreatedAt, user.UpdatedAt,
		user.LastLogin, user.FailedLogins, user.LockedUntil, user.SessionVersion)
	if err != nil {
		return fmt.Errorf("auth: create user: %w", err)
	}
	return nil
}

// GetByUsername loads one user; unknown names report ErrUserNotFound.
func (s *UserStoreSQL) GetByUsername(ctx context.Context, username string) (*User, error) {
	u, err := scanUser(s.db.QueryRowContext(ctx,
		`SELECT `+userColumns+` FROM users WHERE name = ?`, username))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrUserNotFound
		}
		return nil, fmt.Errorf("auth: get user: %w", err)
	}
	return u, nil
}

// Update rewrites a user row.
func (s *UserStoreSQL) Update(ctx context.Context, user *User) error {
	if user == nil || user.Username == "" {
		return ErrUserNotFound
	}
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ?, is_gm = ?, created_at = ?, updated_at = ?,
		 last_login = ?, failed_logins = ?, locked_until = ?, session_version = ? WHERE name = ?`,
		user.PasswordHash, boolToInt(user.IsGM), user.CreatedAt, user.UpdatedAt,
		user.LastLogin, user.FailedLogins, user.LockedUntil, user.SessionVersion, user.Username)
	if err != nil {
		return fmt.Errorf("auth: update user: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("auth: update user: %w", err)
	}
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// Delete removes a user row; their sessions cascade via foreign key where
// enforced (callers additionally bump + DeleteByUser for backends without
// FK enforcement).
func (s *UserStoreSQL) Delete(ctx context.Context, username string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE name = ?`, username)
	if err != nil {
		return fmt.Errorf("auth: delete user: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("auth: delete user: %w", err)
	}
	if n == 0 {
		return ErrUserNotFound
	}
	return nil
}

// List returns all users ordered by name. Usernames are deployment-sized;
// no pagination in v1.
func (s *UserStoreSQL) List(ctx context.Context) ([]*User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userColumns+` FROM users ORDER BY name ASC`)
	if err != nil {
		return nil, fmt.Errorf("auth: list users: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []*User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("auth: list users: %w", err)
		}
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("auth: list users: %w", err)
	}
	return out, nil
}

// SessionConfig holds session configuration.
type SessionConfig struct {
	Key             [32]byte
	AbsoluteTimeout time.Duration // 30 days
	IdleTimeout     time.Duration // 24 hours
	Secure          bool          // only behind trusted-proxy HTTPS
	CSRFKey         [32]byte
	Version         int // session_version for revoke-all
}
