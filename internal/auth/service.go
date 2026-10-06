package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Session lifetimes (spec §7): 30-day absolute + 24h sliding idle expiry.
const (
	SessionAbsoluteTimeout = 30 * 24 * time.Hour
	SessionIdleTimeout     = 24 * time.Hour
	// SessionRefreshThreshold bounds write amplification: the sliding idle
	// deadline is extended only once it is closer than this.
	SessionRefreshThreshold = 12 * time.Hour
)

// MinPasswordLength is the only password-complexity rule in v1 (local-first,
// GM-provisioned accounts; no email/OAuth by scope fence).
const MinPasswordLength = 8

// ValidateUsername enforces the v1 username rule: 1–64 chars of
// [A-Za-z0-9._-], never "." or "..". Usernames are the primary key
// (users.name, Phase 0c: immutable, no rename) and travel inside HMAC cookie
// values, so separators (|), slashes, spaces, and controls are rejected.
func ValidateUsername(name string) error {
	if len(name) == 0 || len(name) > 64 {
		return errors.New("auth: username must be 1-64 characters")
	}
	if name == "." || name == ".." {
		return errors.New("auth: invalid username")
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '_' || c == '-' {
			continue
		}
		return fmt.Errorf("auth: invalid character %q in username", c)
	}
	return nil
}

// ValidatePassword enforces minimum length and the hashing input cap.
func ValidatePassword(password string) error {
	if len(password) < MinPasswordLength {
		return fmt.Errorf("auth: password must be at least %d characters", MinPasswordLength)
	}
	if len(password) > MaxPasswordBytes {
		return fmt.Errorf("auth: password exceeds %d bytes", MaxPasswordBytes)
	}
	return nil
}

// ErrUserExists is returned when creating a duplicate username (exact or
// case-insensitive collision: "Alice" vs "alice" must never coexist, or
// page ownership and grants become ambiguous).
var ErrUserExists = errors.New("auth: user already exists")

// CreateUser provisions an account: validates name/password, stores the
// argon2id PHC hash (never the password), stamps session_version 1.
// Used by `init` for the first GM and by GMs/claim-redeem for players.
func CreateUser(ctx context.Context, users UserStore, username, password string, isGM bool, nowUnix int64) (*User, error) {
	if err := ValidateUsername(username); err != nil {
		return nil, err
	}
	if err := ValidatePassword(password); err != nil {
		return nil, err
	}
	if users == nil {
		return nil, errors.New("auth: nil user store")
	}
	if _, err := users.GetByUsername(ctx, username); err == nil {
		return nil, ErrUserExists
	} else if !errors.Is(err, ErrUserNotFound) {
		return nil, err
	}
	if list, err := users.List(ctx); err == nil {
		for _, u := range list {
			if equalFold(u.Username, username) {
				return nil, ErrUserExists
			}
		}
	} else {
		return nil, err
	}
	hash, err := HashPassword(password)
	if err != nil {
		return nil, err
	}
	u := &User{
		Username:       username,
		PasswordHash:   hash,
		IsGM:           isGM,
		CreatedAt:      nowUnix,
		UpdatedAt:      nowUnix,
		SessionVersion: 1,
	}
	if err := users.Create(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}

// Authenticate verifies a password and maintains the account row: success
// resets failure counters and rehashes on parameter change; failure bumps
// FailedLogins. Unknown users and wrong passwords both surface
// ErrInvalidCredentials (no oracle); locked accounts surface
// ErrAccountLocked.
func Authenticate(ctx context.Context, users UserStore, username, password string, nowUnix int64) (*User, error) {
	u, err := users.GetByUsername(ctx, username)
	if err != nil {
		if errors.Is(err, ErrUserNotFound) {
			return nil, ErrInvalidCredentials
		}
		return nil, err
	}
	if u.LockedUntil > nowUnix {
		return nil, ErrAccountLocked
	}
	needsRehash, err := VerifyPassword(password, u.PasswordHash)
	if err != nil {
		if errors.Is(err, ErrInvalidCredentials) {
			u.FailedLogins++
			u.UpdatedAt = nowUnix
			_ = users.Update(ctx, u)
			return nil, ErrInvalidCredentials
		}
		return nil, err // corrupt stored hash: surfaced, never confused with a wrong password
	}
	u.FailedLogins = 0
	u.LockedUntil = 0
	u.LastLogin = nowUnix
	u.UpdatedAt = nowUnix
	if needsRehash {
		if hash, herr := HashPassword(password); herr == nil {
			u.PasswordHash = hash
		}
	}
	if err := users.Update(ctx, u); err != nil {
		return nil, err
	}
	return u, nil
}

// DefaultSessionConfig returns the v1 session configuration for a signing
// key: 30-day absolute + 24h sliding idle timeouts.
func DefaultSessionConfig(key [32]byte) SessionConfig {
	return SessionConfig{
		Key:             key,
		AbsoluteTimeout: SessionAbsoluteTimeout,
		IdleTimeout:     SessionIdleTimeout,
		Secure:          false,
	}
}

// Login is the full password-login flow for handlers: rate-limit check,
// password authentication, session creation, and cookie signing.
// limiter may be nil (tests); production must pass one.
// Returns the session, the user, and the Set-Cookie value.
func Login(ctx context.Context, users UserStore, sessions SessionStore, limiter *RateLimiter,
	username, password, ip string, cfg SessionConfig, now time.Time) (*Session, *User, string, error) {
	nowUnix := now.Unix()
	if limiter != nil {
		if err := limiter.Check(ctx, users, ip, username, nowUnix); err != nil {
			return nil, nil, "", err
		}
	}
	u, err := Authenticate(ctx, users, username, password, nowUnix)
	if err != nil {
		if limiter != nil && errors.Is(err, ErrInvalidCredentials) {
			_ = limiter.RecordFailure(ctx, users, ip, username, nowUnix)
		}
		return nil, nil, "", err
	}
	if limiter != nil {
		_ = limiter.RecordSuccess(ctx, users, ip, username, nowUnix)
	}
	id, err := NewSessionID()
	if err != nil {
		return nil, nil, "", err
	}
	csrf, err := NewCSRFToken()
	if err != nil {
		return nil, nil, "", err
	}
	absTimeout, idleTimeout := cfg.AbsoluteTimeout, cfg.IdleTimeout
	if absTimeout <= 0 {
		absTimeout = SessionAbsoluteTimeout
	}
	if idleTimeout <= 0 {
		idleTimeout = SessionIdleTimeout
	}
	s := &Session{
		ID:        id,
		UserID:    u.Username,
		CreatedAt: nowUnix,
		ExpiresAt: now.Add(absTimeout).Unix(),
		IdleAt:    now.Add(idleTimeout).Unix(),
		Version:   u.SessionVersion,
		CSRFToken: csrf,
	}
	if _, err := sessions.Create(ctx, s); err != nil {
		return nil, nil, "", err
	}
	return s, u, SignSessionCookie(cfg.Key, s.ID, u.Username, s.ExpiresAt), nil
}

// Logout deletes a session row; unknown ids are success (idempotent logout).
func Logout(ctx context.Context, sessions SessionStore, sessionID string) error {
	if !ValidateSessionID(sessionID) {
		return nil
	}
	if err := sessions.Delete(ctx, sessionID); err != nil && !errors.Is(err, ErrSessionNotFound) {
		return err
	}
	return nil
}

// AuthenticateRequest validates one request cookie end-to-end: HMAC (any of
// the rotation keys), session row, user binding, absolute + idle expiry,
// revocation, and session_version match. On success it opportunistically
// extends the sliding idle deadline (see SessionRefreshThreshold) and
// returns the live session and user.
func AuthenticateRequest(ctx context.Context, sessions SessionStore, users UserStore,
	keys [][32]byte, cookieValue string, now time.Time) (*Session, *User, error) {
	if len(keys) == 0 {
		return nil, nil, errors.New("auth: no session keys configured")
	}
	sessionID, cookieUser, cookieExpiry, err := ParseAndVerifySessionCookie(keys, cookieValue)
	if err != nil {
		return nil, nil, err
	}
	s, err := sessions.Get(ctx, sessionID)
	if err != nil {
		return nil, nil, err
	}
	if s.UserID != cookieUser {
		return nil, nil, ErrInvalidSession
	}
	nowUnix := now.Unix()
	if nowUnix > s.ExpiresAt || nowUnix > cookieExpiry {
		return nil, nil, ErrSessionExpired
	}
	if nowUnix > s.IdleAt {
		return nil, nil, ErrSessionExpired
	}
	u, err := users.GetByUsername(ctx, s.UserID)
	if err != nil {
		return nil, nil, ErrInvalidSession
	}
	if s.Version != u.SessionVersion {
		return nil, nil, ErrSessionRevoked
	}
	_, _ = MaybeRefreshSession(ctx, sessions, s, now, SessionIdleTimeout)
	return s, u, nil
}

// sessionToucher is implemented by stores that track last_seen; refreshes
// use it when available and fall back to Update otherwise.
type sessionToucher interface {
	TouchSession(ctx context.Context, id string, nowUnix, idleAtUnix int64) error
}

// MaybeRefreshSession extends the sliding idle deadline when it is within
// SessionRefreshThreshold of lapsing. Returns true when the session was
// extended.
func MaybeRefreshSession(ctx context.Context, sessions SessionStore, s *Session, now time.Time, idleTimeout time.Duration) (bool, error) {
	if idleTimeout <= 0 {
		idleTimeout = SessionIdleTimeout
	}
	if s.IdleAt-now.Unix() >= int64(SessionRefreshThreshold.Seconds()) {
		return false, nil
	}
	s.IdleAt = now.Add(idleTimeout).Unix()
	if t, ok := sessions.(sessionToucher); ok {
		return true, t.TouchSession(ctx, s.ID, now.Unix(), s.IdleAt)
	}
	return true, sessions.Update(ctx, s)
}

// RevokeAllSessions bumps session_version and deletes every session row for
// the user. Sessions validated afterwards fail on version mismatch even if
// a row survived.
func RevokeAllSessions(ctx context.Context, users UserStore, sessions SessionStore, username string, nowUnix int64) error {
	u, err := users.GetByUsername(ctx, username)
	if err != nil {
		return err
	}
	u.SessionVersion++
	u.UpdatedAt = nowUnix
	if err := users.Update(ctx, u); err != nil {
		return err
	}
	return sessions.DeleteByUser(ctx, username)
}

// ResetPassword sets a new password and revokes every session by bumping
// session_version (spec: `reset-password` bumps session_version). Backs the
// reset-password CLI from day one; there is no email flow in v1.
func ResetPassword(ctx context.Context, users UserStore, sessions SessionStore, username, newPassword string, nowUnix int64) error {
	if err := ValidatePassword(newPassword); err != nil {
		return err
	}
	u, err := users.GetByUsername(ctx, username)
	if err != nil {
		return err
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return err
	}
	u.PasswordHash = hash
	u.SessionVersion++
	u.FailedLogins = 0
	u.LockedUntil = 0
	u.UpdatedAt = nowUnix
	if err := users.Update(ctx, u); err != nil {
		return err
	}
	return sessions.DeleteByUser(ctx, username)
}

// ChangePassword verifies the old password, sets the new one, revokes all
// other sessions, and re-issues the caller's session (same id, fresh CSRF,
// current version, fresh expiries) so the user stays logged in here only.
func ChangePassword(ctx context.Context, users UserStore, sessions SessionStore,
	username, currentSessionID, oldPassword, newPassword string, now time.Time) (*Session, error) {
	nowUnix := now.Unix()
	u, err := Authenticate(ctx, users, username, oldPassword, nowUnix)
	if err != nil {
		return nil, err
	}
	if err := ValidatePassword(newPassword); err != nil {
		return nil, err
	}
	hash, err := HashPassword(newPassword)
	if err != nil {
		return nil, err
	}
	u.PasswordHash = hash
	u.SessionVersion++
	u.UpdatedAt = nowUnix
	if err := users.Update(ctx, u); err != nil {
		return nil, err
	}
	keep, err := sessions.Get(ctx, currentSessionID)
	if err != nil {
		// Current session already gone: revoke everything, nothing to keep.
		if delErr := sessions.DeleteByUser(ctx, username); delErr != nil {
			return nil, delErr
		}
		return nil, nil
	}
	if err := sessions.DeleteByUser(ctx, username); err != nil {
		return nil, err
	}
	csrf, err := NewCSRFToken()
	if err != nil {
		return nil, err
	}
	keep.CSRFToken = csrf
	keep.Version = u.SessionVersion
	keep.CreatedAt = nowUnix
	keep.ExpiresAt = now.Add(SessionAbsoluteTimeout).Unix()
	keep.IdleAt = now.Add(SessionIdleTimeout).Unix()
	if _, err := sessions.Create(ctx, keep); err != nil {
		return nil, err
	}
	return keep, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func equalFold(a, b string) bool {
	return strings.EqualFold(a, b)
}
