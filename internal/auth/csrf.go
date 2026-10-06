package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"net/http"
	"time"
)

// NewCSRFToken generates a random 256-bit double-submit CSRF token.
func NewCSRFToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: csrf token generation: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// ValidateCSRFToken compares the submitted token against the token bound to
// the session (double-submit, P11). Both must be non-empty; comparison is
// constant-time. Applies to every state-changing route, including login
// POST and SSE-patched forms.
func ValidateCSRFToken(sessionToken, submittedToken string) bool {
	if sessionToken == "" || submittedToken == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(sessionToken), []byte(submittedToken)) == 1
}

// BuildCSRFCookie renders the readable (non-HttpOnly, so JS/fetch forms can
// echo it back) CSRF cookie. It carries no authority on its own: the server
// compares the submitted value against the session row, not this cookie.
func BuildCSRFCookie(token string, expires time.Time, serveSecure bool) *http.Cookie {
	maxAge := int(time.Until(expires).Seconds())
	if maxAge < 0 {
		maxAge = 0
	}
	return &http.Cookie{
		Name:     CSRFCookieName,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		MaxAge:   maxAge,
		HttpOnly: false,
		Secure:   serveSecure,
		SameSite: http.SameSiteLaxMode,
	}
}

// CSRFTokenFromRequest extracts the submitted CSRF token from a request:
// form field first, then the fetch/SSE header fallback.
func CSRFTokenFromRequest(r *http.Request) string {
	if t := r.FormValue(CSRFFieldName); t != "" {
		return t
	}
	return r.Header.Get(CSRFHeaderName)
}
