package auth

import (
	"context"
)

// Viewer represents the authenticated context for a request.
// All fields are frozen after Phase 0c - changes require explicit amend.
type Viewer struct {
	// UserID is the authenticated user's ID (empty for guests)
	UserID string
	// IsGM indicates if the viewer has GM privileges
	IsGM bool
	// OwnedSlugs are the vault-relative paths this viewer owns (nearest-ancestor inheritance applied)
	OwnedSlugs []string
	// Grants are explicit ACL grants for this viewer (editable-by, etc.)
	Grants []string
	// PreviewAs allows GM to impersonate another viewer (bannered + logged, GM-only)
	PreviewAs *Viewer
}

// SessionStore defines the interface for session persistence.
// Implementations must be safe for concurrent use.
type SessionStore interface {
	// Create inserts a new session and returns its ID.
	Create(ctx context.Context, session *Session) (string, error)
	// Get retrieves a session by ID.
	Get(ctx context.Context, id string) (*Session, error)
	// Update modifies an existing session.
	Update(ctx context.Context, session *Session) error
	// Delete removes a session by ID.
	Delete(ctx context.Context, id string) error
	// DeleteByUser removes all sessions for a user.
	DeleteByUser(ctx context.Context, userID string) error
	// CleanupExpired removes expired sessions (called periodically).
	CleanupExpired(ctx context.Context) error
}

// Session represents an authenticated session.
type Session struct {
	ID        string
	UserID    string
	CreatedAt int64
	ExpiresAt int64
	IdleAt    int64
	Version   int
	CSRFToken string
}

// GenerateKey generates a new session signing key.
// Pure function - no side effects, no I/O.
// Provided by Lane C (auth), wired by Lane E1 (binary/ops).
func GenerateKey() ([32]byte, error) {
	return [32]byte{}, nil // not implemented
}
