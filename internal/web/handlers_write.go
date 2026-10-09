package web

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"time"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/markdown"
	"github.com/semiplane/yonder/internal/store"
	"github.com/semiplane/yonder/internal/vault"
)

// WriteHandlers contains all write-path HTTP handlers.
// Owned by Lane F2 (write path). Consumes vault.WriteFile + Store API.
// Touches no read handlers.
//
// Write-path rules (P01 editor + P03 conflict UX):
//   - Every state-changing POST requires login (401 for guests) and passes
//     the double-submit CSRF check whenever a SessionStore is wired
//     (pitfalls: CSRF on every state-changing route, including SSE forms).
//     A nil SessionStore skips only the CSRF comparison (unwired dev/test
//     mode); login is still required.
//   - All vault writes go through VaultWriter.WriteFile (atomic temp+rename,
//     clash-checked). Nothing here writes the filesystem any other way,
//     except ResolveConflict which is the explicit human-merge override.
//   - Non-GM writers are confined to their own PC subtree
//     (characters/<pc>/...): prefix check + owner stamp on create, owner /
//     secret / editable-by validation on every save (never-widen wins).
//   - Concurrent saves surface as *.conflict-<ts>.md (single pattern,
//     created by the vault layer, never auto-merged) plus a conflict banner
//     with a manual-merge flow. No error path emits secret content: titles,
//     snippets and bodies only render after the write gate passes, and
//     denials for unreadable pages are uniform 404s (never 403).
type WriteHandlers struct {
	Store         store.Store
	Vault         VaultWriter
	SessionStore  auth.SessionStore
	SlotRegistry  *SlotRegistry
	UserStore     auth.UserStore
	RateLimiter   *auth.RateLimiter
	SessionConfig auth.SessionConfig
	OnAfterWrite  func() error // called after successful write to trigger reindex + store refresh
}

// userStore returns the UserStore, creating it lazily from the app DB if needed.
func (h *WriteHandlers) userStore() auth.UserStore {
	if h.UserStore != nil {
		return h.UserStore
	}
	if h.Store != nil {
		if us, err := auth.NewUserStore(h.Store.AppDB()); err == nil {
			h.UserStore = us
			return us
		}
	}
	return nil
}

// rateLimiter returns the RateLimiter, creating it lazily from the app DB if needed.
func (h *WriteHandlers) rateLimiter() *auth.RateLimiter {
	if h.RateLimiter != nil {
		return h.RateLimiter
	}
	if h.Store != nil {
		if rl, err := auth.NewRateLimiter(h.Store.AppDB()); err == nil {
			h.RateLimiter = rl
			return rl
		}
	}
	return nil
}

// VaultWriter defines the interface for vault write operations.
// Implemented by Lane B (vault package: *vault.Vault satisfies it).
// ReadFile doubles as the read-before-write that arms the vault's
// optimistic clash check; ResolveConflict is the explicit manual-merge
// override (human-supplied text, never auto-merge).
type VaultWriter interface {
	WriteFile(ctx context.Context, path, content string) error
	DeleteFile(ctx context.Context, path string) error
	RenameFile(ctx context.Context, oldPath, newPath string) error
	ReadFile(ctx context.Context, path string) ([]byte, error)
	ResolveConflict(ctx context.Context, conflictPath, resolvedContent string) error
}

// maxWriteBody caps editor POST bodies (content + merged text). Vault files
// are small prose; 5MB stops giant-POST abuse without touching real pages.
const maxWriteBody = 5 << 20

// RegisterRoutes registers write-path routes with the registry.
func (h *WriteHandlers) RegisterRoutes(reg *RouteRegistry) {
	// Auth routes (login/logout) - login POST needs rate limiting + CSRF, logout needs CSRF
	reg.Register(Route{
		Method:       http.MethodGet,
		Path:         RouteLogin,
		Handler:      h.LoginHandler,
		ReadOnly:     true,
		AuthRequired: false, // login page is public
	})
	reg.Register(Route{
		Method:       http.MethodPost,
		Path:         RouteLogin,
		Handler:      h.LoginHandler,
		ReadOnly:     false,
		AuthRequired: false, // login is public
	})
	reg.Register(Route{
		Method:       http.MethodPost,
		Path:         RouteLogout,
		Handler:      h.LogoutHandler,
		ReadOnly:     false,
		AuthRequired: true, // logout requires session
	})

	reg.Register(Route{
		Method:         http.MethodGet,
		Path:           RoutePageEdit,
		Handler:        h.PageEdit,
		ReadOnly:       false,
		AuthRequired:   true,
		SecretFiltered: false, // editor sees all (owner/GM)
	})
	reg.Register(Route{
		Method:       http.MethodPost,
		Path:         RoutePageEdit,
		Handler:      h.PageSave,
		ReadOnly:     false,
		AuthRequired: true,
	})
	reg.Register(Route{
		Method:       http.MethodGet,
		Path:         RoutePageNew,
		Handler:      h.PageNew,
		ReadOnly:     false,
		AuthRequired: true,
	})
	reg.Register(Route{
		Method:       http.MethodPost,
		Path:         RoutePageNew,
		Handler:      h.PageCreate,
		ReadOnly:     false,
		AuthRequired: true,
	})
	reg.Register(Route{
		Method:       http.MethodPost,
		Path:         RoutePageHistory + "/revert",
		Handler:      h.PageRevert,
		ReadOnly:     false,
		AuthRequired: true,
	})
	reg.Register(Route{
		Method:       http.MethodPost,
		Path:         RouteUpload,
		Handler:      h.Upload,
		ReadOnly:     false,
		AuthRequired: true,
	})
	reg.Register(Route{
		Method:       http.MethodPost,
		Path:         RouteDiceRoll,
		Handler:      h.DiceRoll,
		ReadOnly:     false,
		AuthRequired: true,
	})
	reg.Register(Route{
		Method:       http.MethodPost,
		Path:         RouteWizard,
		Handler:      h.WizardStep,
		ReadOnly:     false,
		AuthRequired: true,
	})
	reg.Register(Route{
		Method:       http.MethodPost,
		Path:         RouteEncounter,
		Handler:      h.EncounterAction,
		ReadOnly:     false,
		AuthRequired: true,
		GMOnly:       true,
	})
	reg.Register(Route{
		Method:       http.MethodPost,
		Path:         RouteVTT + "/state",
		Handler:      h.VTTStateUpdate,
		ReadOnly:     false,
		AuthRequired: true,
	})
}

// ---------------------------------------------------------------------------
// Path + ACL helpers (pure policy over the frozen Lane B APIs)
// ---------------------------------------------------------------------------

// pageACL is the effective write ACL for one vault path: nearest-ancestor
// owner/editable-by plus the fail-closed secret OR over self + ancestors
// (P03: secret OR-inherits; editable-by inherits like owner).
type pageACL struct {
	owner      string
	editableBy []string
	secretSelf bool
	above      bool // any ancestor row is secret
	selfExists bool
}

func (a *pageACL) secret() bool { return a == nil || a.secretSelf || a.above }

// cleanWritePath validates a vault-relative page path: Windows-safe +
// traversal-checked via vault.CleanRel, .md only, never a conflict file or
// dotfile (both reserved patterns the indexer hides).
func cleanWritePath(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("empty page path")
	}
	rel, err := vault.CleanRel(trimmed)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(strings.ToLower(rel), ".md") {
		return "", fmt.Errorf("page path must end in .md: %q", raw)
	}
	if isConflictRel(rel) {
		return "", fmt.Errorf("reserved conflict-file pattern: %q", raw)
	}
	for _, seg := range strings.Split(rel, "/") {
		if strings.HasPrefix(seg, ".") {
			return "", fmt.Errorf("hidden path segment %q in %q", seg, raw)
		}
	}
	return rel, nil
}

// cleanConflictPath validates a ?conflict=/form conflict file: same Windows-
// safe + traversal rules, but it MUST match the single conflict pattern for
// the given parent (page paths must never be conflict files and vice versa).
func cleanConflictPath(raw, parent string) (string, error) {
	rel, err := vault.CleanRel(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if !conflictOfParent(parent, rel) {
		return "", fmt.Errorf("not a conflict file of %q", parent)
	}
	return rel, nil
}

func isConflictRel(p string) bool {
	base := p
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	i := strings.LastIndex(base, ".conflict-")
	return i >= 0 && strings.HasSuffix(base, ".md")
}

// conflictOfParent reports whether cpath is a conflict file minted for parent
// (single pattern everywhere: <stem>.conflict-<ts>.md).
func conflictOfParent(parent, cpath string) bool {
	if !isConflictRel(cpath) {
		return false
	}
	stem := strings.TrimSuffix(parent, path.Ext(parent))
	if !strings.HasPrefix(cpath, stem+".conflict-") || !strings.HasSuffix(cpath, ".md") {
		return false
	}
	mid := cpath[len(stem)+len(".conflict-") : len(cpath)-len(".md")]
	if mid == "" {
		return false
	}
	for i := 0; i < len(mid); i++ {
		if mid[i] < '0' || mid[i] > '9' {
			return false
		}
	}
	return true
}

// pcSubpath splits characters/<pc>/... paths (P03 path ACL). ok is false for
// anything outside a PC subtree, including the bare characters/ dir.
func pcSubpath(rel string) (pc string, ok bool) {
	parts := strings.Split(rel, "/")
	if len(parts) < 3 || parts[0] != "characters" || parts[1] == "" {
		return "", false
	}
	return parts[1], true
}

func posixDir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return "."
}

// ancestorCandidates lists self + ancestor index rows nearest-first: the path
// itself, then <dir>.md and <dir>/index.md per ancestor dir (PC roots are
// characters/<pc>/index.md, section pages <dir>.md).
func ancestorCandidates(rel string) []string {
	out := []string{rel}
	for dir := posixDir(rel); dir != "." && dir != ""; {
		out = append(out, dir+".md", dir+"/index.md")
		dir = posixDir(dir)
	}
	return out
}

// resolveACL walks self + ancestors via the index (case-insensitive PageGet).
// Missing rows are skipped; a nil Store yields the zero ACL (deny by default
// for non-GM writers outside same-name trees).
func (h *WriteHandlers) resolveACL(ctx context.Context, rel string) (*pageACL, error) {
	acl := &pageACL{}
	if h.Store == nil {
		return acl, nil
	}
	for i, cand := range ancestorCandidates(rel) {
		p, err := h.Store.PageGet(ctx, cand)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return nil, err
		}
		if i == 0 {
			acl.selfExists = true
			acl.secretSelf = p.Secret
		} else if p.Secret {
			acl.above = true
		}
		if acl.owner == "" && p.Owner != "" {
			acl.owner = p.Owner
		}
		if acl.editableBy == nil && len(p.EditableBy) > 0 {
			acl.editableBy = append([]string(nil), p.EditableBy...)
		}
	}
	return acl, nil
}

func editableByHas(list []string, user string) bool {
	for _, u := range list {
		if strings.EqualFold(u, user) {
			return true
		}
	}
	return false
}

// scopeHas reports path-scoped ownership/grants (viewer OwnedSlugs/Grants as
// folded sets from store.SplitViewer): exact or ancestor-dir match.
func scopeHas(owned, grants map[string]bool, rel string) bool {
	for _, c := range ancestorCandidates(rel) {
		if owned[strings.ToLower(c)] || grants[strings.ToLower(c)] {
			return true
		}
		if d := posixDir(c); d != "." {
			if owned[strings.ToLower(d)] || grants[strings.ToLower(d)] {
				return true
			}
		}
	}
	return false
}

func marshalEditableBy(list []string) string {
	b, _ := json.Marshal(list)
	if b == nil {
		return "[]"
	}
	return string(b)
}

// denyForWrite maps a failed write gate to uniform-404-when-unreadable
// (P03: never 403 when the viewer cannot even read the page) else 403.
func denyForWrite(acl *pageACL, v *auth.Viewer, rel string) int {
	if acl.secret() && !store.PageVisible(acl.secret(), rel, acl.owner, marshalEditableBy(acl.editableBy), v) {
		return http.StatusNotFound
	}
	return http.StatusForbidden
}

// authorizeWrite enforces the P03 write gate. Returns status 0 when the
// write may proceed, else the HTTP status to answer with (401 guests always;
// GM bypasses subtree checks; everyone else needs own-PC-subtree ownership
// or an editable-by/scope grant, with unreadable pages hidden as 404).
func (h *WriteHandlers) authorizeWrite(ctx context.Context, v *auth.Viewer, rel string) (*pageACL, int) {
	user, isGM, owned, grants := store.SplitViewer(v)
	if user == "" && !isGM {
		return nil, http.StatusUnauthorized
	}
	acl, err := h.resolveACL(ctx, rel)
	if err != nil {
		return nil, http.StatusInternalServerError
	}
	if isGM {
		return acl, 0
	}
	pc, ok := pcSubpath(rel)
	if !ok {
		return acl, denyForWrite(acl, v, rel)
	}
	if acl.owner != "" {
		if !strings.EqualFold(acl.owner, user) && !editableByHas(acl.editableBy, user) && !scopeHas(owned, grants, rel) {
			return acl, denyForWrite(acl, v, rel)
		}
		return acl, 0
	}
	// Unstamped tree (no owner row yet): only the same-name PC subtree or an
	// explicit scope grant may write here; creates stamp the writer as owner.
	if !strings.EqualFold(pc, user) && !scopeHas(owned, grants, rel) {
		return acl, denyForWrite(acl, v, rel)
	}
	return acl, 0
}

// viewerOf returns the request viewer (nil = guest).
func viewerOf(r *http.Request) *auth.Viewer {
	if v, ok := auth.ViewerFromContext(r.Context()); ok {
		return v
	}
	return nil
}

// checkCSRF enforces double-submit CSRF on state-changing routes: the
// submitted token (form field or fetch header) must equal the token bound to
// the session row. Unknown sessions fail closed. A nil SessionStore skips
// only this comparison (unwired mode); login is still enforced separately.
func (h *WriteHandlers) checkCSRF(r *http.Request) bool {
	if h.SessionStore == nil {
		return true
	}
	c, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		return false
	}
	sessID := c.Value
	if i := strings.IndexByte(sessID, '|'); i >= 0 {
		sessID = sessID[:i]
	}
	if !auth.ValidateSessionID(sessID) {
		return false
	}
	sess, err := h.SessionStore.Get(r.Context(), sessID)
	if err != nil || sess == nil {
		return false
	}
	return auth.ValidateCSRFToken(sess.CSRFToken, auth.CSRFTokenFromRequest(r))
}

// csrfTokenForForm returns the session's CSRF token for embedding in forms
// ("" when sessions are unwired or the request carries no session).
func (h *WriteHandlers) csrfTokenForForm(r *http.Request) string {
	if h.SessionStore == nil {
		return ""
	}
	c, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		return ""
	}
	sessID := c.Value
	if i := strings.IndexByte(sessID, '|'); i >= 0 {
		sessID = sessID[:i]
	}
	if !auth.ValidateSessionID(sessID) {
		return ""
	}
	sess, err := h.SessionStore.Get(r.Context(), sessID)
	if err != nil || sess == nil {
		return ""
	}
	return sess.CSRFToken
}

// ---------------------------------------------------------------------------
// Login / Logout handlers (auth surface, P11)
// ---------------------------------------------------------------------------

// LoginHandler handles GET /login (render form) and POST /login (authenticate).
func (h *WriteHandlers) LoginHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.renderLogin(w, r)
	case http.MethodPost:
		h.handleLogin(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// renderLogin renders the login page.
func (h *WriteHandlers) renderLogin(w http.ResponseWriter, r *http.Request) {
	// If already logged in, redirect to dashboard/me
	if v := viewerOf(r); v != nil && v.UserID != "" {
		http.Redirect(w, r, "/me", http.StatusSeeOther)
		return
	}
	csrfToken := ""
	if h.SessionStore != nil {
		if tok, err := auth.NewCSRFToken(); err == nil {
			csrfToken = tok
			// Set CSRF cookie for the login form (double-submit)
			http.SetCookie(w, auth.BuildCSRFCookie(csrfToken, time.Now().Add(1*time.Hour), false))
		}
	}
	var b strings.Builder
	b.WriteString("<h1>Log in</h1>\n")
	b.WriteString("<form method=\"post\" action=\"/login\">\n")
	if csrfToken != "" {
		fmt.Fprintf(&b, "<input type=\"hidden\" name=\"%s\" value=\"%s\">\n",
			auth.CSRFFieldName, html.EscapeString(csrfToken))
	}
	b.WriteString("<p><label for=\"username\">Username</label> <input id=\"username\" name=\"username\" type=\"text\" autocomplete=\"username\" required></p>\n")
	b.WriteString("<p><label for=\"password\">Password</label> <input id=\"password\" name=\"password\" type=\"password\" autocomplete=\"current-password\" required></p>\n")
	b.WriteString("<p><button type=\"submit\">Log in</button></p>\n")
	b.WriteString("</form>\n")
	writeHTML(w, http.StatusOK, "Log in", b.String())
}

// handleLogin processes the login form submission.
func (h *WriteHandlers) handleLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeDenied(w, http.StatusBadRequest, "bad form data")
		return
	}

	// CSRF check (double-submit)
	if h.SessionStore != nil {
		submitted := auth.CSRFTokenFromRequest(r)
		c, _ := r.Cookie(auth.CSRFCookieName)
		var cookieToken string
		if c != nil {
			cookieToken = c.Value
		}
		if !auth.ValidateCSRFToken(cookieToken, submitted) {
			writeDenied(w, http.StatusForbidden, "bad or missing CSRF token")
			return
		}
	}

	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	if username == "" || password == "" {
		writeDenied(w, http.StatusBadRequest, "username and password are required")
		return
	}

	us := h.userStore()
	if us == nil {
		writeDenied(w, http.StatusInternalServerError, "user store not available")
		return
	}

	limiter := h.rateLimiter()

	// Use SessionConfig from handler (wired in serve.go)
	cfg := h.SessionConfig
	if cfg.Key == [32]byte{} {
		writeDenied(w, http.StatusInternalServerError, "session key not configured")
		return
	}

	sess, user, cookieVal, err := auth.Login(r.Context(), us, h.SessionStore, limiter, username, password, clientIP(r), cfg, time.Now())
	if err != nil {
		if errors.Is(err, auth.ErrRateLimited) {
			writeDenied(w, http.StatusTooManyRequests, "too many login attempts, try again later")
			return
		}
		if errors.Is(err, auth.ErrAccountLocked) {
			writeDenied(w, http.StatusForbidden, "account locked, try again later")
			return
		}
		// ErrInvalidCredentials or other: generic message (no oracle)
		writeDenied(w, http.StatusUnauthorized, "invalid username or password")
		return
	}

	_ = sess // session created, cookieVal has the signed cookie
	_ = user // authenticated user

	// Set session cookie
	http.SetCookie(w, auth.BuildSessionCookie(cookieVal, time.Unix(sess.ExpiresAt, 0), cfg.Secure))
	// Set CSRF cookie for subsequent requests
	http.SetCookie(w, auth.BuildCSRFCookie(sess.CSRFToken, time.Unix(sess.ExpiresAt, 0), cfg.Secure))

	// Redirect to /me or dashboard
	http.Redirect(w, r, "/me", http.StatusSeeOther)
}

// clientIP extracts the client IP from the request (handles X-Forwarded-For for trusted proxies).
func clientIP(r *http.Request) string {
	// In production behind a trusted proxy, use X-Forwarded-For
	// For now, use RemoteAddr directly (LAN/dev)
	ip := r.RemoteAddr
	if i := strings.LastIndex(ip, ":"); i >= 0 {
		ip = ip[:i]
	}
	return ip
}

// LogoutHandler handles POST /logout.
func (h *WriteHandlers) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// CSRF check
	if h.SessionStore != nil && !h.checkCSRF(r) {
		writeDenied(w, http.StatusForbidden, "bad or missing CSRF token")
		return
	}

	// Get session ID from cookie
	c, err := r.Cookie(auth.SessionCookieName)
	if err == nil {
		sessID := c.Value
		if i := strings.IndexByte(sessID, '|'); i >= 0 {
			sessID = sessID[:i]
		}
		if auth.ValidateSessionID(sessID) {
			_ = h.SessionStore.Delete(r.Context(), sessID)
		}
	}

	// Clear session cookie
	http.SetCookie(w, auth.ClearSessionCookie(false))
	// Clear CSRF cookie
	http.SetCookie(w, &http.Cookie{
		Name:     auth.CSRFCookieName,
		Value:    "",
		Path:     "/",
		Expires:  time.Unix(0, 0).UTC(),
		MaxAge:   -1,
		HttpOnly: false,
		Secure:   false,
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ---------------------------------------------------------------------------
// Frontmatter stamping (surgical: preserve comments/order, never rewrite)
// ---------------------------------------------------------------------------

// quoteYAML renders a scalar safe for a frontmatter line: bare when simple,
// double-quoted otherwise.
func quoteYAML(s string) string {
	simple := s != "" && !strings.ContainsAny(s, ":#{}[],&*!|>'\"%@`") &&
		!strings.HasPrefix(s, " ") && !strings.HasSuffix(s, " ") &&
		!strings.ContainsAny(s, "\r\n\t")
	if simple {
		return s
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\n':
			b.WriteString("\\n")
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// fmLineEnd returns the line ending used by line ("" for the last line).
func fmLineEnd(line string) string {
	if strings.HasSuffix(line, "\r\n") {
		return "\r\n"
	}
	if strings.HasSuffix(line, "\n") {
		return "\n"
	}
	return "\n"
}

func splitKeepEnds(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i+1])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func trimEOL(s string) string { return strings.TrimRight(s, "\r\n") }

// ensureFMKeys sets top-level frontmatter keys, preserving every other byte
// (comments, order, quoting of untouched lines). Keys present are replaced
// only when force is true (owner pin, secret escalation); other keys are
// insert-only (e.g. title). Missing keys are inserted directly after the
// opening fence, or a fresh fence is prepended when the page has none.
func ensureFMKeys(content string, keys []fmKey) string {
	lines := splitKeepEnds(content)
	fmStart, fmEnd := -1, -1
	if len(lines) > 0 && trimEOL(strings.TrimRight(lines[0], " \t\r\n")) == "---" {
		for i := 1; i < len(lines); i++ {
			t := strings.TrimRight(trimEOL(lines[i]), " \t")
			if t == "---" || t == "..." {
				fmStart, fmEnd = 0, i
				break
			}
		}
	}
	done := make([]bool, len(keys))
	if fmStart >= 0 {
		for i := fmStart + 1; i < fmEnd; i++ {
			body := trimEOL(lines[i])
			if strings.HasPrefix(body, " ") || strings.HasPrefix(body, "\t") {
				continue // nested: not a top-level key
			}
			j := strings.IndexByte(body, ':')
			if j < 0 {
				continue
			}
			name := strings.TrimSpace(body[:j])
			for k, p := range keys {
				if done[k] || p.name != name {
					continue
				}
				rest := strings.TrimSpace(body[j+1:])
				if p.force || name == "owner" {
					if p.force || rest != p.value {
						lines[i] = p.name + ": " + p.value + fmLineEnd(lines[i])
					}
				}
				done[k] = true
			}
		}
		var insert []string
		for k, p := range keys {
			if !done[k] {
				eol := "\n"
				if fmEnd > fmStart+1 {
					eol = fmLineEnd(lines[fmStart+1])
				} else if len(lines) > 0 {
					eol = fmLineEnd(lines[0])
				}
				insert = append(insert, p.name+": "+p.value+eol)
			}
		}
		if len(insert) > 0 {
			head := append([]string{}, lines[:fmStart+1]...)
			head = append(head, insert...)
			lines = append(head, lines[fmStart+1:]...)
		}
		return strings.Join(lines, "")
	}
	// No frontmatter: prepend a fresh fence (GM quarantine-override writes
	// never reach here — stamping applies to non-GM writes only).
	var b strings.Builder
	b.WriteString("---\n")
	for _, p := range keys {
		b.WriteString(p.name + ": " + p.value + "\n")
	}
	b.WriteString("---\n")
	b.WriteString(content)
	return b.String()
}

type fmKey struct {
	name  string
	value string
	force bool // replace even when a value already exists
}

// stampCreate stamps a fresh page: owner = creator (always), secret forced
// true under a secret ancestor, optional title when supplied and missing.
func stampCreate(content, owner string, forceSecret bool, title string) string {
	keys := []fmKey{{name: "owner", value: quoteYAML(owner), force: true}}
	if forceSecret {
		keys = append(keys, fmKey{name: "secret", value: "true", force: true})
	}
	if title != "" {
		keys = append(keys, fmKey{name: "title", value: quoteYAML(title)})
	}
	return ensureFMKeys(content, keys)
}

// stampSave re-stamps on save: owner pinned to the effective owner (or the
// writer when the tree is unstamped), secret forced under secret ancestors.
func stampSave(content, effOwner string, forceSecret bool) string {
	keys := []fmKey{{name: "owner", value: quoteYAML(effOwner), force: true}}
	if forceSecret {
		keys = append(keys, fmKey{name: "secret", value: "true", force: true})
	}
	return ensureFMKeys(content, keys)
}

// ---------------------------------------------------------------------------
// Write validation (owner/secret/editable-by on every write, never-widen)
// ---------------------------------------------------------------------------

type validatedWrite struct {
	content string // stamped content ready for WriteFile
	secret  bool   // effective secret flag
}

// validateCreate enforces create policy: owner is always stamped to the
// writer (client-supplied owner ignored), secret inherited fail-closed.
func validateCreate(ctx context.Context, content, rel, user string, acl *pageACL, isGM bool, title string) (*validatedWrite, int, string) {
	parsed, err := markdown.Parse(ctx, content, rel)
	if err != nil {
		return nil, http.StatusInternalServerError, "could not parse page"
	}
	if parsed.Quarantined && !isGM {
		return nil, http.StatusUnprocessableEntity, "bad frontmatter: " + parsed.QuarantineReason
	}
	if isGM {
		return &validatedWrite{content: content, secret: parsed.Secret}, 0, ""
	}
	stamped := stampCreate(content, user, acl.secret(), title)
	secret := parsed.Secret || acl.secret()
	return &validatedWrite{content: stamped, secret: secret}, 0, ""
}

// validateSave enforces save policy: owner immutable (GM transfer only),
// editable-by frozen for players, secret never widens (true->false rejected
// unless inherited-then-forced or GM).
func validateSave(ctx context.Context, content, rel, user string, acl *pageACL, isGM bool) (*validatedWrite, int, string) {
	parsed, err := markdown.Parse(ctx, content, rel)
	if err != nil {
		return nil, http.StatusInternalServerError, "could not parse page"
	}
	if parsed.Quarantined && !isGM {
		return nil, http.StatusUnprocessableEntity, "bad frontmatter: " + parsed.QuarantineReason
	}
	if isGM {
		return &validatedWrite{content: content, secret: parsed.Secret}, 0, ""
	}
	effOwner := acl.owner
	if effOwner == "" {
		effOwner = user
	}
	if parsed.Owner != "" && !strings.EqualFold(parsed.Owner, effOwner) {
		return nil, http.StatusForbidden, "owner is immutable (ask your GM to transfer it)"
	}
	if !sameStringSet(parsed.EditableBy, acl.editableBy) {
		return nil, http.StatusForbidden, "editable-by can only be changed by your GM"
	}
	// Secret: inherited-secret forces true (stored FM stays truthful);
	// self-only secret may narrow but never widen.
	selfSecret := acl.secretSelf
	if acl.above && !parsed.Secret {
		stamped := stampSave(content, effOwner, true)
		return &validatedWrite{content: stamped, secret: true}, 0, ""
	}
	if selfSecret && !parsed.Secret && !acl.above {
		return nil, http.StatusForbidden, "a secret page cannot be made non-secret by a player (ask your GM)"
	}
	stamped := stampSave(content, effOwner, false)
	return &validatedWrite{content: stamped, secret: parsed.Secret || acl.secret()}, 0, ""
}

func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		// Compare case-insensitively as sets (order-free).
		if len(a) == 0 && len(b) == 0 {
			return true
		}
	}
	seen := map[string]int{}
	for _, s := range a {
		seen[strings.ToLower(s)]++
	}
	for _, s := range b {
		seen[strings.ToLower(s)]--
	}
	for _, n := range seen {
		if n != 0 {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Tiny HTML responses (no templates: web/templates is Lane F1's file)
// ---------------------------------------------------------------------------

func writeHTML(w http.ResponseWriter, status int, title, body string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = fmt.Fprintf(w, "<!DOCTYPE html>\n<html lang=\"en\">\n<head><meta charset=\"utf-8\">\n<title>%s</title></head>\n<body>\n<main>\n%s\n</main>\n</body>\n</html>\n",
		html.EscapeString(title), body)
}

func writeDenied(w http.ResponseWriter, status int, msg string) {
	writeHTML(w, status, "Not allowed",
		"<h1>Not allowed</h1>\n<p>"+html.EscapeString(msg)+"</p>")
}

// conflictBanner renders the conflict notice: text-first (never color-only),
// assertive live region, with a link into the manual-merge flow.
func conflictBanner(parent, cpath string) string {
	var b strings.Builder
	b.WriteString("<section aria-live=\"assertive\" role=\"alert\" class=\"conflict-banner\">\n")
	b.WriteString("<h2>Conflicting edit saved separately</h2>\n")
	b.WriteString("<p>Someone else saved <strong>" + html.EscapeString(parent) + "</strong> " +
		"while you were editing. Your text was preserved — nothing was auto-merged — in " +
		"<code>" + html.EscapeString(cpath) + "</code>.</p>\n")
	mergeURL := "/p/" + parent + "/edit?conflict=" + url.QueryEscape(cpath)
	b.WriteString("<p><a href=\"" + html.EscapeString(mergeURL) + "\">Merge by hand</a>: " +
		"compare both versions and post the merged text. Only the owner or GM can resolve.</p>\n")
	b.WriteString("</section>\n")
	return b.String()
}

// visibleConflicts lists conflict rows for parent that viewer v may see
// (parent ACL inherited, orphans writer+GM only — store.ConflictVisible).
// Empty without a Store (unwired mode); the fresh-conflict 409 path carries
// its own banner regardless.
func (h *WriteHandlers) visibleConflicts(ctx context.Context, v *auth.Viewer, parent string) []*store.ConflictRow {
	if h.Store == nil {
		return nil
	}
	rows, err := store.ListConflicts(ctx, h.Store.IndexDB(), parent)
	if err != nil {
		return nil
	}
	var out []*store.ConflictRow
	for _, c := range rows {
		if store.ConflictVisible(c, v) {
			out = append(out, c)
		}
	}
	return out
}

func conflictListHTML(parent string, rows []*store.ConflictRow) string {
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("<section aria-live=\"polite\" class=\"conflict-list\">\n")
	b.WriteString("<h2>Unresolved conflicts</h2>\n<ul>\n")
	for _, c := range rows {
		mergeURL := "/p/" + parent + "/edit?conflict=" + url.QueryEscape(c.Path)
		fmt.Fprintf(&b, "<li><code>%s</code> <a href=\"%s\">Merge by hand</a></li>\n",
			html.EscapeString(c.Path), html.EscapeString(mergeURL))
	}
	b.WriteString("</ul>\n</section>\n")
	return b.String()
}

// ---------------------------------------------------------------------------
// URL helpers (routes.go registry is frozen; paths parsed from the request)
// ---------------------------------------------------------------------------

func editPathOf(r *http.Request) string {
	p := r.URL.Path
	// Support both old format (/p/{path...}/edit) and new format (/edit/p/{path...})
	if strings.HasPrefix(p, "/p/") && strings.HasSuffix(p, "/edit") {
		inner := strings.TrimSuffix(strings.TrimPrefix(p, "/p/"), "/edit")
		inner = strings.TrimSuffix(inner, "/")
		if un, err := url.PathUnescape(inner); err == nil {
			inner = un
		}
		if inner == "" {
			return r.FormValue("path")
		}
		return inner
	}
	if strings.HasPrefix(p, "/edit/p/") {
		inner := strings.TrimPrefix(p, "/edit/p/")
		inner = strings.TrimSuffix(inner, "/")
		if un, err := url.PathUnescape(inner); err == nil {
			inner = un
		}
		if inner == "" {
			return r.FormValue("path")
		}
		return inner
	}
	if v := r.FormValue("path"); v != "" {
		return v
	}
	return ""
}

func revertPathOf(r *http.Request) string {
	p := r.URL.Path
	if !strings.HasPrefix(p, "/p/") || !strings.HasSuffix(p, "/history/revert") {
		return r.FormValue("path")
	}
	inner := strings.TrimSuffix(strings.TrimPrefix(p, "/p/"), "/history/revert")
	inner = strings.TrimSuffix(inner, "/")
	if un, err := url.PathUnescape(inner); err == nil {
		inner = un
	}
	if inner == "" {
		return r.FormValue("path")
	}
	return inner
}

// ---------------------------------------------------------------------------
// Handlers: editor + save + preview
// ---------------------------------------------------------------------------

// PageEdit renders the page editor (textarea + conflict banner + merge view).
func (h *WriteHandlers) PageEdit(w http.ResponseWriter, r *http.Request) {
	v := viewerOf(r)
	user, isGM, _, _ := store.SplitViewer(v)
	if user == "" && !isGM {
		writeDenied(w, http.StatusUnauthorized, "log in to edit pages")
		return
	}
	if h.Vault == nil {
		writeDenied(w, http.StatusInternalServerError, "vault is not configured")
		return
	}
	raw := editPathOf(r)
	// Ensure .md extension for cleanWritePath
	if !strings.HasSuffix(strings.ToLower(raw), ".md") {
		raw += ".md"
	}
	rel, err := cleanWritePath(raw)
	if err != nil {
		writeDenied(w, http.StatusBadRequest, "bad page path: "+err.Error())
		return
	}
	_, status := h.authorizeWrite(r.Context(), v, rel)
	if status != 0 {
		writeDenied(w, status, "you cannot edit this page")
		return
	}
	// Manual-merge view wins when ?conflict= names one of this page's files.
	if cpath := r.URL.Query().Get("conflict"); cpath != "" {
		h.renderMergeView(w, r, v, rel, cpath)
		return
	}
	content, err := h.Vault.ReadFile(r.Context(), rel)
	if err != nil {
		if os.IsNotExist(err) {
			writeDenied(w, http.StatusNotFound, "no such page (create it under New first)")
		} else {
			writeDenied(w, http.StatusInternalServerError, "could not read page")
		}
		return
	}
	conflicts := h.visibleConflicts(r.Context(), v, rel)
	var b strings.Builder
	fmt.Fprintf(&b, "<h1>Editing %s</h1>\n", html.EscapeString(rel))
	b.WriteString(conflictListHTML(rel, conflicts))
	b.WriteString(conflictBannerFor(rel, conflicts))
	editURL := "/edit/p/" + strings.TrimSuffix(strings.TrimSuffix(rel, ".md"), ".markdown")
	b.WriteString("<form method=\"post\" action=\"" + html.EscapeString(editURL) + "\">\n")
	fmt.Fprintf(&b, "<input type=\"hidden\" name=\"%s\" value=\"%s\">\n",
		auth.CSRFFieldName, html.EscapeString(h.csrfTokenForForm(r)))
	fmt.Fprintf(&b, "<p><label for=\"edit-content\">Content for %s</label></p>\n", html.EscapeString(rel))
	fmt.Fprintf(&b, "<textarea id=\"edit-content\" name=\"content\" rows=\"24\" cols=\"80\">%s</textarea>\n",
		html.EscapeString(string(content)))
	b.WriteString("<p><button type=\"submit\">Save</button> " +
		"<button type=\"submit\" name=\"preview\" value=\"1\" formaction=\"?preview=1\">Preview</button></p>\n")
	b.WriteString("</form>\n")
	writeHTML(w, http.StatusOK, "Editing "+rel, b.String())
}

// conflictBannerFor reuses stored rows to banner the newest visible conflict
// on the editor (the fresh-save 409 path banners inline instead).
func conflictBannerFor(parent string, rows []*store.ConflictRow) string {
	if len(rows) == 0 {
		return ""
	}
	return conflictBanner(parent, rows[0].Path)
}

// renderMergeView shows parent + conflict side by side with a merged-textarea
// posting to the revert (resolve) endpoint. Both sides render only after the
// write gate passed (caller), so no secret content leaks here.
func (h *WriteHandlers) renderMergeView(w http.ResponseWriter, r *http.Request, v *auth.Viewer, parent, cpath string) {
	crel, err := cleanConflictPath(cpath, parent)
	if err != nil {
		writeDenied(w, http.StatusBadRequest, "not a conflict file of this page")
		return
	}
	current, err := h.Vault.ReadFile(r.Context(), parent)
	if err != nil {
		writeDenied(w, http.StatusNotFound, "the parent page is gone; nothing to merge into")
		return
	}
	other, err := h.Vault.ReadFile(r.Context(), crel)
	if err != nil {
		writeDenied(w, http.StatusNotFound, "that conflict file no longer exists")
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<h1>Merging %s</h1>\n", html.EscapeString(parent))
	b.WriteString("<p>Nothing is auto-merged. Compare both versions, write the merged text, then resolve. " +
		"Resolving writes the parent atomically and deletes the conflict file.</p>\n")
	fmt.Fprintf(&b, "<h2>Current: %s</h2>\n<pre>%s</pre>\n",
		html.EscapeString(parent), html.EscapeString(string(current)))
	fmt.Fprintf(&b, "<h2>Conflict: %s</h2>\n<pre>%s</pre>\n",
		html.EscapeString(crel), html.EscapeString(string(other)))
	fmt.Fprintf(&b, "<form method=\"post\" action=\"%s\">\n", html.EscapeString("/p/"+parent+"/history/revert"))
	fmt.Fprintf(&b, "<input type=\"hidden\" name=\"%s\" value=\"%s\">\n",
		auth.CSRFFieldName, html.EscapeString(h.csrfTokenForForm(r)))
	fmt.Fprintf(&b, "<input type=\"hidden\" name=\"conflict\" value=\"%s\">\n", html.EscapeString(crel))
	b.WriteString("<p><label for=\"merge-content\">Merged content</label></p>\n")
	fmt.Fprintf(&b, "<textarea id=\"merge-content\" name=\"content\" rows=\"24\" cols=\"80\">%s</textarea>\n",
		html.EscapeString(string(current)))
	b.WriteString("<p><button type=\"submit\">Resolve with this text</button></p>\n</form>\n")
	writeHTML(w, http.StatusOK, "Merging "+parent, b.String())
}

// PageSave handles page save (and server preview with preview=1, which
// parses + renders without writing anything).
func (h *WriteHandlers) PageSave(w http.ResponseWriter, r *http.Request) {
	v := viewerOf(r)
	user, isGM, _, _ := store.SplitViewer(v)
	if user == "" && !isGM {
		writeDenied(w, http.StatusUnauthorized, "log in to save pages")
		return
	}
	if h.Vault == nil {
		writeDenied(w, http.StatusInternalServerError, "vault is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWriteBody)
	raw := editPathOf(r)
	if raw == "" {
		raw = r.FormValue("path")
	}
	// Ensure .md extension for cleanWritePath
	if !strings.HasSuffix(strings.ToLower(raw), ".md") {
		raw += ".md"
	}
	rel, err := cleanWritePath(raw)
	if err != nil {
		writeDenied(w, http.StatusBadRequest, "bad page path: "+err.Error())
		return
	}
	if !h.checkCSRF(r) {
		writeDenied(w, http.StatusForbidden, "bad or missing CSRF token")
		return
	}
	acl, status := h.authorizeWrite(r.Context(), v, rel)
	if status != 0 {
		writeDenied(w, status, "you cannot save this page")
		return
	}
	content := r.FormValue("content")
	// Server preview: validate + render, never write.
	if r.FormValue("preview") != "" || r.URL.Query().Get("preview") != "" {
		h.renderPreview(w, r, rel, content, acl)
		return
	}
	// Existence is checked via the index, deliberately NOT via a vault read:
	// a vault ReadFile here would re-arm this process's clash baseline to
	// the current disk state and every concurrent save would silently win.
	// The editor GET already armed the baseline; WriteFile below compares
	// disk against it and parks diverged saves as *.conflict-<ts>.md.
	if h.Store != nil {
		if _, err := h.Store.PageGet(r.Context(), rel); err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				writeDenied(w, http.StatusInternalServerError, "could not look up page")
				return
			}
			// Not indexed: missing file → 404; present-but-unindexed →
			// arm the baseline to current (no false conflict possible).
			if _, rerr := h.Vault.ReadFile(r.Context(), rel); rerr != nil {
				if os.IsNotExist(rerr) {
					writeDenied(w, http.StatusNotFound, "no such page (create it under New first)")
				} else {
					writeDenied(w, http.StatusInternalServerError, "could not read page")
				}
				return
			}
		}
	} else if _, err := h.Vault.ReadFile(r.Context(), rel); err != nil {
		if os.IsNotExist(err) {
			writeDenied(w, http.StatusNotFound, "no such page (create it under New first)")
		} else {
			writeDenied(w, http.StatusInternalServerError, "could not read page")
		}
		return
	}
	vw, vstatus, vmsg := validateSave(r.Context(), content, rel, user, acl, isGM)
	if vstatus != 0 {
		writeDenied(w, vstatus, vmsg)
		return
	}
	if err := h.Vault.WriteFile(r.Context(), rel, vw.content); err != nil {
		var ce *vault.ErrConflict
		if errors.As(err, &ce) {
			writeHTML(w, http.StatusConflict, "Edit conflict",
				conflictBanner(rel, ce.ConflictPath)+
					"<p><a href=\""+html.EscapeString("/p/"+rel+"/edit")+"\">Back to the editor</a></p>")
			return
		}
		writeDenied(w, http.StatusInternalServerError, "could not save page")
		return
	}
	http.Redirect(w, r, "/p/"+rel, http.StatusSeeOther)
	if h.OnAfterWrite != nil {
		_ = h.OnAfterWrite()
	}
}

// renderPreview parses submitted content and renders the server preview:
// secret `-` blocks carry a red "hidden" badge, `+` a green "visible" badge
// (text labels, never color-only), unsupported blocks a placeholder. The
// editor already passed the write gate, so full content may show.
func (h *WriteHandlers) renderPreview(w http.ResponseWriter, r *http.Request, rel, content string, _ *pageACL) {
	page, err := markdown.Parse(r.Context(), content, rel)
	if err != nil {
		writeDenied(w, http.StatusInternalServerError, "could not parse page")
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "<h1>Preview: %s</h1>\n", html.EscapeString(rel))
	if page.Title != "" {
		fmt.Fprintf(&b, "<p>Title: %s</p>\n", html.EscapeString(page.Title))
	}
	fmt.Fprintf(&b, "<p>Secret page: %t. Owner: %s.</p>\n", page.Secret, html.EscapeString(page.Owner))
	if page.Quarantined {
		fmt.Fprintf(&b, "<div role=\"alert\"><p>Warning: frontmatter problem (%s). "+
			"The page is treated as secret until fixed.</p></div>\n", html.EscapeString(page.QuarantineReason))
	}
	b.WriteString("<hr>\n")
	b.WriteString(renderPreviewBlocks(page.Blocks))
	b.WriteString("<hr>\n<p><a href=\"" + html.EscapeString("/p/"+rel+"/edit") + "\">Back to the editor</a> " +
		"(nothing was saved).</p>\n")
	writeHTML(w, http.StatusOK, "Preview "+rel, b.String())
}

// renderPreviewBlocks renders parsed blocks to preview HTML (all text
// escaped; unsupported blocks become explicit placeholders).
func renderPreviewBlocks(blocks []markdown.Block) string {
	var b strings.Builder
	for _, bl := range blocks {
		switch bl.Type {
		case "heading":
			lvl := bl.Level
			if lvl < 2 {
				lvl = 2
			}
			if lvl > 6 {
				lvl = 6
			}
			fmt.Fprintf(&b, "<h%d>%s</h%d>\n", lvl, html.EscapeString(bl.Content), lvl)
		case "code":
			b.WriteString("<pre><code>" + html.EscapeString(bl.Content) + "</code></pre>\n")
		case "secret":
			cls, badge := "prev-secret-shown", "secret + shown to party"
			if bl.Secret {
				cls, badge = "prev-secret-hidden", "secret \u2212 hidden from party"
			}
			b.WriteString("<section class=\"" + cls + "\">\n")
			b.WriteString("<p class=\"prev-badge\">" + html.EscapeString(badge) + "</p>\n")
			b.WriteString(renderParas(bl.Content))
			b.WriteString("</section>\n")
		case "optional":
			b.WriteString("<aside class=\"prev-optional\"><p>Optional content: " +
				html.EscapeString(bl.Content) + "</p></aside>\n")
		case "unsupported":
			b.WriteString("<div class=\"prev-unsupported\" role=\"note\"><p>Unsupported content \u2014 placeholder. " +
				html.EscapeString(bl.Content) + " blocks are not rendered in v1. The source is preserved.</p></div>\n")
		case "callout":
			b.WriteString("<blockquote>" + renderParas(bl.Content) + "</blockquote>\n")
		case "html":
			b.WriteString("<pre class=\"prev-html\">" + html.EscapeString(bl.Content) + "</pre>\n")
		default: // paragraph and anything future: plain escaped paragraphs
			if bl.Secret {
				b.WriteString("<p class=\"prev-badge\">secret \u2212 hidden from party</p>\n")
			}
			b.WriteString(renderParas(bl.Content))
		}
	}
	return b.String()
}

func renderParas(s string) string {
	var b strings.Builder
	for _, p := range strings.Split(s, "\n\n") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		b.WriteString("<p>" + html.EscapeString(p) + "</p>\n")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Handlers: create
// ---------------------------------------------------------------------------

// PageNew renders the new page form (PC subtree only; enforced on create).
func (h *WriteHandlers) PageNew(w http.ResponseWriter, r *http.Request) {
	v := viewerOf(r)
	user, isGM, _, _ := store.SplitViewer(v)
	if user == "" && !isGM {
		writeDenied(w, http.StatusUnauthorized, "log in to create pages")
		return
	}
	parent := r.URL.Query().Get("parent")
	if parent == "" && user != "" {
		parent = "characters/" + user
	}
	var b strings.Builder
	b.WriteString("<h1>New page</h1>\n")
	b.WriteString("<p>Pages can only be created inside your own character tree " +
		"(<code>characters/&lt;you&gt;/...</code>). You are stamped as owner automatically.</p>\n")
	b.WriteString("<form method=\"post\" action=\"" + html.EscapeString(RoutePageNew) + "\">\n")
	fmt.Fprintf(&b, "<input type=\"hidden\" name=\"%s\" value=\"%s\">\n",
		auth.CSRFFieldName, html.EscapeString(h.csrfTokenForForm(r)))
	fmt.Fprintf(&b, "<p><label for=\"new-parent\">Folder</label> <input id=\"new-parent\" name=\"parent\" value=\"%s\" size=\"40\"></p>\n",
		html.EscapeString(parent))
	b.WriteString("<p><label for=\"new-name\">Name</label> <input id=\"new-name\" name=\"name\" size=\"40\"> (.md is added when missing)</p>\n")
	b.WriteString("<p><label for=\"new-secret\"><input id=\"new-secret\" type=\"checkbox\" name=\"secret\" value=\"true\"> Secret page</label> (forced on under a secret folder)</p>\n")
	b.WriteString("<p><label for=\"new-content\">Content</label></p>\n")
	b.WriteString("<textarea id=\"new-content\" name=\"content\" rows=\"24\" cols=\"80\"></textarea>\n")
	b.WriteString("<p><button type=\"submit\">Create</button></p>\n</form>\n")
	writeHTML(w, http.StatusOK, "New page", b.String())
}

// PageCreate handles page creation: prefix check, owner stamp, existence
// check, then a single VaultWriter.WriteFile.
func (h *WriteHandlers) PageCreate(w http.ResponseWriter, r *http.Request) {
	v := viewerOf(r)
	user, isGM, _, _ := store.SplitViewer(v)
	if user == "" && !isGM {
		writeDenied(w, http.StatusUnauthorized, "log in to create pages")
		return
	}
	if h.Vault == nil {
		writeDenied(w, http.StatusInternalServerError, "vault is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWriteBody)
	if !h.checkCSRF(r) {
		writeDenied(w, http.StatusForbidden, "bad or missing CSRF token")
		return
	}
	parent := strings.TrimSpace(r.FormValue("parent"))
	name := strings.TrimSpace(r.FormValue("name"))
	if parent == "" || name == "" {
		writeDenied(w, http.StatusBadRequest, "folder and name are both required")
		return
	}
	if !strings.Contains(name, ".") {
		name += ".md"
	}
	rel, err := cleanWritePath(parent + "/" + name)
	if err != nil {
		writeDenied(w, http.StatusBadRequest, "bad page path: "+err.Error())
		return
	}
	acl, status := h.authorizeWrite(r.Context(), v, rel)
	if status != 0 {
		writeDenied(w, status, "you cannot create pages here (own character tree only)")
		return
	}
	if _, err := h.Vault.ReadFile(r.Context(), rel); err == nil {
		writeDenied(w, http.StatusConflict, "a page already exists at "+rel)
		return
	} else if !os.IsNotExist(err) {
		writeDenied(w, http.StatusInternalServerError, "could not check for an existing page")
		return
	}
	content := r.FormValue("content")
	if secretFlag(r.FormValue("secret")) {
		content = ensureFMKeys(content, []fmKey{{name: "secret", value: "true"}})
	}
	title := strings.TrimSpace(r.FormValue("title"))
	vw, vstatus, vmsg := validateCreate(r.Context(), content, rel, user, acl, isGM, title)
	if vstatus != 0 {
		writeDenied(w, vstatus, vmsg)
		return
	}
	if err := h.Vault.WriteFile(r.Context(), rel, vw.content); err != nil {
		var ce *vault.ErrConflict
		if errors.As(err, &ce) {
			writeHTML(w, http.StatusConflict, "Edit conflict", conflictBanner(rel, ce.ConflictPath))
			return
		}
		writeDenied(w, http.StatusInternalServerError, "could not create page")
		return
	}
	http.Redirect(w, r, "/p/"+rel, http.StatusSeeOther)
	if h.OnAfterWrite != nil {
		_ = h.OnAfterWrite()
	}
}

func secretFlag(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Handlers: conflict resolve (manual merge over the frozen watcher API)
// ---------------------------------------------------------------------------

// PageRevert resolves a conflict with caller-supplied merged text (manual
// merge, never auto-merge): re-validated like a save, written via
// VaultWriter.ResolveConflict, conflict file deleted. Full history revert is
// a later lane; this route serves conflict resolution in v1.
func (h *WriteHandlers) PageRevert(w http.ResponseWriter, r *http.Request) {
	v := viewerOf(r)
	user, isGM, _, _ := store.SplitViewer(v)
	if user == "" && !isGM {
		writeDenied(w, http.StatusUnauthorized, "log in to resolve conflicts")
		return
	}
	if h.Vault == nil {
		writeDenied(w, http.StatusInternalServerError, "vault is not configured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxWriteBody)
	if !h.checkCSRF(r) {
		writeDenied(w, http.StatusForbidden, "bad or missing CSRF token")
		return
	}
	raw := revertPathOf(r)
	rel, err := cleanWritePath(raw)
	if err != nil {
		writeDenied(w, http.StatusBadRequest, "bad page path: "+err.Error())
		return
	}
	acl, status := h.authorizeWrite(r.Context(), v, rel)
	if status != 0 {
		writeDenied(w, status, "you cannot resolve conflicts on this page")
		return
	}
	cpath, err := cleanConflictPath(strings.TrimSpace(r.FormValue("conflict")), rel)
	if err != nil {
		writeDenied(w, http.StatusBadRequest, "not a conflict file of this page")
		return
	}
	if _, err := h.Vault.ReadFile(r.Context(), cpath); err != nil {
		writeDenied(w, http.StatusNotFound, "that conflict file no longer exists")
		return
	}
	merged := r.FormValue("content")
	if strings.TrimSpace(merged) == "" {
		writeDenied(w, http.StatusBadRequest, "merged text is required (nothing is auto-merged)")
		return
	}
	vw, vstatus, vmsg := validateSave(r.Context(), merged, rel, user, acl, isGM)
	if vstatus != 0 {
		writeDenied(w, vstatus, vmsg)
		return
	}
	if err := h.Vault.ResolveConflict(r.Context(), cpath, vw.content); err != nil {
		writeDenied(w, http.StatusInternalServerError, "could not resolve conflict")
		return
	}
	http.Redirect(w, r, "/p/"+rel, http.StatusSeeOther)
	if h.OnAfterWrite != nil {
		_ = h.OnAfterWrite()
	}
}

// ---------------------------------------------------------------------------
// Stubs owned by other lanes (routes stay registered; logic lands later)
// ---------------------------------------------------------------------------

// Upload handles file uploads (Lane E1/P13 owns the pipeline).
func (h *WriteHandlers) Upload(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "uploads are not implemented in this milestone", http.StatusNotImplemented)
}

// DiceRoll handles dice roll requests (Lane H2/P12 owns the engine).
func (h *WriteHandlers) DiceRoll(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "dice rolls are not implemented in this milestone", http.StatusNotImplemented)
}

// WizardStep handles wizard steps (Lane I1/P07 owns onboarding).
func (h *WriteHandlers) WizardStep(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "the character wizard is not implemented in this milestone", http.StatusNotImplemented)
}

// EncounterAction handles encounter actions (Lane I2 owns run-mode).
func (h *WriteHandlers) EncounterAction(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "encounters are not implemented in this milestone", http.StatusNotImplemented)
}

// VTTStateUpdate handles VTT state updates (Lane K/P08 owns the table).
func (h *WriteHandlers) VTTStateUpdate(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "VTT state is not implemented in this milestone", http.StatusNotImplemented)
}
