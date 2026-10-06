package auth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Login rate limits (spec §7): 5 attempts per 5 minutes per IP and per
// account, then a 15-minute account lockout. State lives in SQLite
// (login_attempts + users.locked_until) so restarts never clear it
// (pitfalls). The limiter sits in front of argon2id: 64MB × login floods is
// a self-DoS without it.
const (
	MaxLoginAttempts   = 5
	LoginAttemptWindow = 5 * time.Minute
	AccountLockout     = 15 * time.Minute
)

// ErrRateLimited is returned when the 5/5min budget is exhausted.
var ErrRateLimited = errors.New("auth: too many login attempts, try again later")

// RateLimiter records login attempts in the app DB.
type RateLimiter struct {
	db *sql.DB
}

// NewRateLimiter creates a RateLimiter, ensuring the auth schema exists.
func NewRateLimiter(db *sql.DB) (*RateLimiter, error) {
	if db == nil {
		return nil, errors.New("auth: nil database")
	}
	if err := EnsureAuthSchema(db); err != nil {
		return nil, fmt.Errorf("auth: ensure schema: %w", err)
	}
	return &RateLimiter{db: db}, nil
}

// Check rejects the attempt when the IP or the account exhausted its 5/5min
// budget, or when the account is inside a 15-minute lockout. nowUnix is
// seconds since epoch (injectable for tests).
func (l *RateLimiter) Check(ctx context.Context, users UserStore, ip, username string, nowUnix int64) error {
	if ip != "" {
		n, err := l.countSince(ctx,
			`SELECT COUNT(*) FROM login_attempts WHERE ip = ? AND success = 0 AND attempted_at > ?`,
			ip, nowUnix-int64(LoginAttemptWindow.Seconds()))
		if err != nil {
			return err
		}
		if n >= MaxLoginAttempts {
			return ErrRateLimited
		}
	}
	if username != "" {
		n, err := l.countSince(ctx,
			`SELECT COUNT(*) FROM login_attempts WHERE username = ? AND success = 0 AND attempted_at > ?`,
			username, nowUnix-int64(LoginAttemptWindow.Seconds()))
		if err != nil {
			return err
		}
		if n >= MaxLoginAttempts {
			return ErrRateLimited
		}
		if users != nil {
			u, err := users.GetByUsername(ctx, username)
			if err == nil && u.LockedUntil > nowUnix {
				return ErrAccountLocked
			}
		}
	}
	return nil
}

// RecordFailure logs a failed attempt and, when the account hits the 5/5min
// budget, starts a 15-minute lockout on the user row (if it exists).
func (l *RateLimiter) RecordFailure(ctx context.Context, users UserStore, ip, username string, nowUnix int64) error {
	if _, err := l.db.ExecContext(ctx,
		`INSERT INTO login_attempts(ip, username, attempted_at, success) VALUES(?, ?, ?, 0)`,
		ip, username, nowUnix); err != nil {
		return fmt.Errorf("auth: record failure: %w", err)
	}
	l.prune(ctx, nowUnix)
	if username == "" || users == nil {
		return nil
	}
	n, err := l.countSince(ctx,
		`SELECT COUNT(*) FROM login_attempts WHERE username = ? AND success = 0 AND attempted_at > ?`,
		username, nowUnix-int64(LoginAttemptWindow.Seconds()))
	if err != nil {
		return err
	}
	if n < MaxLoginAttempts {
		return nil
	}
	u, err := users.GetByUsername(ctx, username)
	if err != nil {
		return nil // unknown user: window counting above is the throttle
	}
	u.LockedUntil = nowUnix + int64(AccountLockout.Seconds())
	u.FailedLogins++
	if err := users.Update(ctx, u); err != nil {
		return fmt.Errorf("auth: lock account: %w", err)
	}
	return nil
}

// RecordSuccess clears the attempt history for this ip+account pair and any
// lockout on the user row: a correct password proves ownership.
func (l *RateLimiter) RecordSuccess(ctx context.Context, users UserStore, ip, username string, nowUnix int64) error {
	if _, err := l.db.ExecContext(ctx,
		`DELETE FROM login_attempts WHERE ip = ? AND username = ?`, ip, username); err != nil {
		return fmt.Errorf("auth: record success: %w", err)
	}
	l.prune(ctx, nowUnix)
	if username == "" || users == nil {
		return nil
	}
	u, err := users.GetByUsername(ctx, username)
	if err != nil {
		return nil
	}
	if u.LockedUntil != 0 || u.FailedLogins != 0 {
		u.LockedUntil = 0
		u.FailedLogins = 0
		if err := users.Update(ctx, u); err != nil {
			return fmt.Errorf("auth: clear lockout: %w", err)
		}
	}
	return nil
}

func (l *RateLimiter) countSince(ctx context.Context, query string, args ...any) (int, error) {
	var n int
	if err := l.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("auth: count attempts: %w", err)
	}
	return n, nil
}

// prune bounds table growth; best-effort, failures are ignored so a full or
// wedged disk never turns login into a 500 for the prune alone.
func (l *RateLimiter) prune(ctx context.Context, nowUnix int64) {
	_, _ = l.db.ExecContext(ctx, `DELETE FROM login_attempts WHERE attempted_at < ?`,
		nowUnix-24*60*60)
}
