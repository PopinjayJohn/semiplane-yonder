package auth

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// NewSessionStore creates a new session store backed by SQLite.
func NewSessionStore(db *sql.DB) (SessionStore, error) {
	return nil, nil // not implemented
}

// LoadOrGenerateKey loads the session key from disk or generates a new one.
// Key file: <data-dir>/.sessionkey (0600, never in vault).
func LoadOrGenerateKey(dataDir string) ([32]byte, error) {
	return [32]byte{}, nil // not implemented
}

// SessionStoreSQL is the SQLite implementation of SessionStore.
type SessionStoreSQL struct {
	db *sql.DB
}

// Create inserts a new session and returns its ID.
func (s *SessionStoreSQL) Create(ctx context.Context, session *Session) (string, error) {
	return "", nil // not implemented
}

// Get retrieves a session by ID.
func (s *SessionStoreSQL) Get(ctx context.Context, id string) (*Session, error) {
	return nil, nil // not implemented
}

// Update modifies an existing session.
func (s *SessionStoreSQL) Update(ctx context.Context, session *Session) error {
	return nil // not implemented
}

// Delete removes a session by ID.
func (s *SessionStoreSQL) Delete(ctx context.Context, id string) error {
	return nil // not implemented
}

// DeleteByUser removes all sessions for a user.
func (s *SessionStoreSQL) DeleteByUser(ctx context.Context, userID string) error {
	return nil // not implemented
}

// CleanupExpired removes expired sessions.
func (s *SessionStoreSQL) CleanupExpired(ctx context.Context) error {
	return nil // not implemented
}

// User represents a user account.
type User struct {
	ID           string
	Username     string
	PasswordHash string // argon2id PHC
	IsGM         bool
	CreatedAt    int64
	UpdatedAt    int64
	LastLogin    int64
	FailedLogins int
	LockedUntil  int64
}

// UserStore defines the interface for user persistence.
type UserStore interface {
	Create(ctx context.Context, user *User) error
	GetByID(ctx context.Context, id string) (*User, error)
	GetByUsername(ctx context.Context, username string) (*User, error)
	Update(ctx context.Context, user *User) error
	Delete(ctx context.Context, id string) error
	List(ctx context.Context) ([]*User, error)
}

// NewUserStore creates a new user store.
func NewUserStore(db *sql.DB) (UserStore, error) {
	return nil, nil // not implemented
}

// ErrUserNotFound is returned when a user is not found.
var ErrUserNotFound = errors.New("user not found")

// ErrInvalidCredentials is returned for invalid login.
var ErrInvalidCredentials = errors.New("invalid credentials")

// ErrAccountLocked is returned when account is locked.
var ErrAccountLocked = errors.New("account locked")

// SessionConfig holds session configuration.
type SessionConfig struct {
	Key             [32]byte
	AbsoluteTimeout time.Duration // 30 days
	IdleTimeout     time.Duration // 24 hours
	Secure          bool          // only behind trusted-proxy HTTPS
	CSRFKey         [32]byte
	Version         int // session_version for revoke-all
}
