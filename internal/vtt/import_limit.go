// Import admission guard (R1: HTTP cap + DoS review).
//
// POST /vtt/{mapID}/import is GM-only + CSRF + preview-403, so the threat
// is not an unauthenticated attacker (401/403 return before the first body
// byte is read) but a compromised GM session or an accidental import storm
// against the single-writer SQLite path (one import = 2 vault writes + a
// live-state reseed + a full rescan on the request path). The guard is
// therefore admission control, not identity lockout: at most one import in
// flight process-wide (stacked imports would serialize full rescans while
// each holds ~200MB transient) and a small per-GM sliding-window budget
// (past budget → 429, never a lockout).
//
// Shape follows the dice transport's in-memory per-actor window
// (internal/dice/transport.go): no migration, no app-DB writes, resets on
// restart by design. Deliberately NOT the login RateLimiter (DB-backed
// windows + 15-minute account lockout would punish a legit multi-map setup
// session and add precious-DB writes to the import path).
package vtt

import (
	"errors"
	"sync"
	"time"
)

// ErrImportBusy answers a concurrent import while one is already running.
var ErrImportBusy = errors.New("vtt: another import is already running, try again shortly")

// ErrImportRateLimited answers a GM past their import window budget.
var ErrImportRateLimited = errors.New("vtt: too many imports, try again later")

// Import window defaults: imports are rare deliberate acts (map setup, not
// table play) — 3 per 5 minutes per GM leaves a campaign-building session
// alone while stopping a retry loop or a compromised session from burning
// repeated 55MB parses.
const (
	defaultImportMaxInFlight  = 1
	defaultImportMaxPerWindow = 3
	defaultImportWindow       = 5 * time.Minute
)

// ImportLimiter is the import admission guard. The zero value is unusable;
// use NewImportLimiter or DefaultImportLimiter. Safe for concurrent use.
type ImportLimiter struct {
	mu           sync.Mutex
	maxInFlight  int
	maxPerWindow int
	window       time.Duration
	now          func() time.Time // seam for tests; nil = time.Now
	inFlight     int
	windows      map[string][]time.Time
}

// NewImportLimiter builds a limiter admitting maxInFlight concurrent imports
// and maxPerWindow imports per window per user id.
func NewImportLimiter(maxInFlight, maxPerWindow int, window time.Duration) *ImportLimiter {
	if maxInFlight < 1 {
		maxInFlight = 1
	}
	if maxPerWindow < 1 {
		maxPerWindow = 1
	}
	if window <= 0 {
		window = defaultImportWindow
	}
	return &ImportLimiter{
		maxInFlight:  maxInFlight,
		maxPerWindow: maxPerWindow,
		window:       window,
		windows:      map[string][]time.Time{},
	}
}

// DefaultImportLimiter is the production budget (R1 §5).
func DefaultImportLimiter() *ImportLimiter {
	return NewImportLimiter(defaultImportMaxInFlight, defaultImportMaxPerWindow, defaultImportWindow)
}

// Acquire admits one import for user, or reports ErrImportBusy /
// ErrImportRateLimited. Every nil-error Acquire must be paired with
// Release (the handler defers it immediately, so every path past acquire
// releases — the semaphore cannot wedge).
func (l *ImportLimiter) Acquire(user string) error {
	now := time.Now()
	if l.now != nil {
		now = l.now()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight >= l.maxInFlight {
		return ErrImportBusy
	}
	cutoff := now.Add(-l.window)
	kept := l.windows[user][:0]
	for _, ts := range l.windows[user] {
		if ts.After(cutoff) {
			kept = append(kept, ts)
		}
	}
	if len(kept) >= l.maxPerWindow {
		l.windows[user] = kept
		return ErrImportRateLimited
	}
	l.windows[user] = append(kept, now)
	l.inFlight++
	return nil
}

// Release frees one in-flight slot. Extra Releases are no-ops (floor 0).
func (l *ImportLimiter) Release() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.inFlight > 0 {
		l.inFlight--
	}
}
