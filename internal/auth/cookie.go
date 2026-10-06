package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Cookie names and transport constants.
const (
	// SessionCookieName is the session cookie name.
	SessionCookieName = "yonder_session"
	// CSRFCookieName carries the double-submit CSRF token.
	CSRFCookieName = "yonder_csrf"
	// CSRFFieldName is the form field carrying the submitted CSRF token.
	CSRFFieldName = "csrf_token"
	// CSRFHeaderName is the header fallback for the submitted CSRF token
	// (used by SSE-patched / fetch forms).
	CSRFHeaderName = "X-CSRF-Token"
)

// SessionIDBytes is the 128-bit random session id size (spec §7, P11).
const SessionIDBytes = 16

// NewSessionID generates a random 128-bit session id, hex-encoded (32 chars).
func NewSessionID() (string, error) {
	b := make([]byte, SessionIDBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: session id generation: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// ValidateSessionID reports whether id is a well-formed session id.
// Every entry point that takes a session id from the client must pass
// through here first: ids are hex-only fixed-length, so path traversal and
// SQL/caller-confusion payloads (.., /, quotes) can never reach storage.
func ValidateSessionID(id string) bool {
	if len(id) != SessionIDBytes*2 {
		return false
	}
	for i := 0; i < len(id); i++ {
		if !isHexDigit(id[i]) {
			return false
		}
	}
	return true
}

func isHexDigit(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// SignSessionCookie builds the cookie value
// `session_id|user_id|expiry|hmac_sha256` (spec §7, P11), where expiry is the
// absolute-expiry unix timestamp and the MAC covers the first three fields.
func SignSessionCookie(key [32]byte, sessionID, userID string, expiryUnix int64) string {
	body := sessionID + "|" + userID + "|" + strconv.FormatInt(expiryUnix, 10)
	mac := hmac.New(sha256.New, key[:])
	mac.Write([]byte(body))
	return body + "|" + hex.EncodeToString(mac.Sum(nil))
}

// ParseAndVerifySessionCookie verifies the HMAC (trying each key in order so
// key rotation overlaps cleanly) and parses the cookie fields. It does not
// touch storage; callers must still load the session row and enforce
// expiry, idle timeout, revocation, and session_version.
func ParseAndVerifySessionCookie(keys [][32]byte, value string) (sessionID, userID string, expiry int64, err error) {
	parts := strings.Split(value, "|")
	if len(parts) != 4 || !ValidateSessionID(parts[0]) || parts[1] == "" {
		return "", "", 0, ErrInvalidSession
	}
	expiry, err = strconv.ParseInt(parts[2], 10, 64)
	if err != nil || expiry <= 0 {
		return "", "", 0, ErrInvalidSession
	}
	body := parts[0] + "|" + parts[1] + "|" + parts[2]
	for _, k := range keys {
		mac := hmac.New(sha256.New, k[:])
		mac.Write([]byte(body))
		if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(parts[3])) == 1 {
			return parts[0], parts[1], expiry, nil
		}
	}
	return "", "", 0, ErrInvalidSession
}

// BuildSessionCookie renders the session Set-Cookie value: HttpOnly,
// SameSite=Lax, Path=/, with Secure set only when serveSecure is true.
// serveSecure must be true only behind a configured trusted proxy serving
// HTTPS (or X-Forwarded-Proto: https from an allowlisted proxy); it stays
// off on plain LAN with a logged warning, otherwise table-day login over
// plain HTTP silently dies (pitfalls). The caller owns that decision and
// logging; this helper just encodes it.
func BuildSessionCookie(value string, expires time.Time, serveSecure bool) *http.Cookie {
	maxAge := int(time.Until(expires).Seconds())
	if maxAge < 0 {
		maxAge = 0
	}
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    value,
		Path:     "/",
		Expires:  expires,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   serveSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

// ClearSessionCookie renders an expired session cookie for logout.
func ClearSessionCookie(serveSecure bool) *http.Cookie {
	return &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0).UTC(),
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   serveSecure,
		SameSite: http.SameSiteLaxMode,
	}
}
