package web

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/store"
)

// RouteRegistry manages route registration.
// Frozen after Phase 0 - new routes register via this registry, never by editing core routing.
type RouteRegistry struct {
	routes      []Route
	middlewares []Middleware
}

// Route represents a registered route.
type Route struct {
	Method         string
	Path           string
	Handler        http.HandlerFunc
	Middleware     []Middleware
	ReadOnly       bool // for handlers_read.go vs handlers_write.go split
	SecretFiltered bool // applies secrets.Filter to response
	GMOnly         bool
	AuthRequired   bool
}

// Middleware represents a middleware function (standard net/http shape).
// The authenticated Viewer is carried in the request context (see auth package:
// WithViewer / ViewerFromContext); middleware must not take Viewer as a param.
type Middleware func(http.Handler) http.Handler

// NewRouteRegistry creates a new route registry.
func NewRouteRegistry() *RouteRegistry {
	return &RouteRegistry{}
}

// Register adds a route to the registry.
func (r *RouteRegistry) Register(route Route) {
	r.routes = append(r.routes, route)
}

// Use adds global middleware.
func (r *RouteRegistry) Use(middleware Middleware) {
	r.middlewares = append(r.middlewares, middleware)
}

// Routes returns all registered routes.
func (r *RouteRegistry) Routes() []Route {
	return r.routes
}

// BuildHandler builds the complete http.Handler with middleware chain.
// sessionKeys are the rotation keys for HMAC verification (from auth.LoadSessionKeys).
// userStore is used to load user details (IsGM) for the Viewer.
// store is used to load owned slugs and grants for the Viewer.
func (r *RouteRegistry) BuildHandler(sessionStore auth.SessionStore, sessionKeys [][32]byte, userStore auth.UserStore, store store.Store) http.Handler {
	if len(sessionKeys) == 0 {
		panic("web: BuildHandler requires at least one session key")
	}
	if userStore == nil {
		panic("web: BuildHandler requires a user store")
	}
	if store == nil {
		panic("web: BuildHandler requires a store")
	}

	mux := http.NewServeMux()
	seen := make(map[string]bool)

	// Build the global middleware chain
	globalMiddlewares := r.middlewares

	for _, rt := range r.routes {
		h := rt.Handler
		if h == nil {
			continue
		}

		pattern := ToStdlibPattern(rt.Path)
		if rt.Method != "" {
			pattern = rt.Method + " " + pattern
		}
		if seen[pattern] {
			continue // first-wins per method+pattern
		}
		seen[pattern] = true

		// Build middleware chain for this route: global + route-specific
		var chain http.Handler = h
		// Apply route-specific middlewares in reverse order (last wraps first)
		for i := len(rt.Middleware) - 1; i >= 0; i-- {
			chain = rt.Middleware[i](chain)
		}
		// Apply global middlewares in reverse order
		for i := len(globalMiddlewares) - 1; i >= 0; i-- {
			chain = globalMiddlewares[i](chain)
		}

		mux.Handle(pattern, chain)
	}

	// Wrap the entire mux with the session authentication middleware
	return authMiddleware(sessionStore, sessionKeys, userStore, store)(mux)
}

// authMiddleware creates middleware that validates the session cookie,
// loads the session and user, and injects the auth.Viewer into the request context.
func authMiddleware(sessionStore auth.SessionStore, sessionKeys [][32]byte, userStore auth.UserStore, store store.Store) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Try to extract and validate session cookie
			c, err := r.Cookie(auth.SessionCookieName)
			if err != nil {
				// No session cookie, continue as guest
				next.ServeHTTP(w, r)
				return
			}

			cookieVal := c.Value
			_, user, err := auth.AuthenticateRequest(r.Context(), sessionStore, userStore, sessionKeys, cookieVal, time.Now())
			if err != nil {
				// Invalid/expired/revoked session - clear cookie and continue as guest
				http.SetCookie(w, auth.ClearSessionCookie(false))
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
				next.ServeHTTP(w, r)
				return
			}

			// Load owned slugs and grants from index for ACL checks
			ownedSlugs, grants := loadViewerScopes(r.Context(), store, user.Username)

			// Build Viewer
			viewer := &auth.Viewer{
				UserID:     user.Username,
				IsGM:       user.IsGM,
				OwnedSlugs: ownedSlugs,
				Grants:     grants,
			}

			// Inject viewer into context
			ctx := auth.WithViewer(r.Context(), viewer)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// loadViewerScopes queries the index for paths owned by or granted to the user.
func loadViewerScopes(ctx context.Context, st store.Store, username string) (ownedSlugs, grants []string) {
	if st == nil || username == "" {
		return nil, nil
	}
	// Query all pages to find ownership and editable-by grants
	// This is a potentially expensive operation; in production we might cache this.
	pages, err := st.PageList(ctx, store.PageListOptions{IncludeSecret: true, Limit: 10000})
	if err != nil {
		return nil, nil
	}
	ownedSet := make(map[string]bool)
	grantsSet := make(map[string]bool)
	for _, p := range pages {
		if p.Owner != "" && strings.EqualFold(p.Owner, username) {
			ownedSet[strings.ToLower(p.Path)] = true
		}
		for _, u := range p.EditableBy {
			if strings.EqualFold(u, username) {
				grantsSet[strings.ToLower(p.Path)] = true
			}
		}
	}
	for k := range ownedSet {
		ownedSlugs = append(ownedSlugs, k)
	}
	for k := range grantsSet {
		grants = append(grants, k)
	}
	return ownedSlugs, grants
}

// Core route patterns (frozen - amend only)
const (
	RouteHealthz      = "/healthz"
	RouteVersion      = "/version"
	RouteLogin        = "/login"
	RouteLogout       = "/logout"
	RouteRegister     = "/register"
	RouteIndex        = "/"
	RoutePageView     = "/p/{path...}"
	RoutePageEdit     = "/edit/p/{path...}"
	RoutePageNew      = "/p/new"
	RoutePageHistory  = "/p/{path...}/history"
	RouteSearch       = "/search"
	RouteGraph        = "/graph"
	RouteAutocomplete = "/autocomplete"
	RouteAssets       = "/assets/{path...}"
	RouteUpload       = "/upload"
	RouteSSE          = "/events"
	RouteDiceRoll     = "/api/dice/roll"
	RouteDiceReplay   = "/api/dice/replay"
	RouteWizard       = "/wizard/{step}"
	RouteMe           = "/me"
	RouteDashboard    = "/dashboard"
	RouteEncounter    = "/encounter"
	RouteVTT          = "/vtt/{mapID}"
	RouteSettings     = "/settings"
)

// ToStdlibPattern converts frozen route patterns (using chi-style {path...}
// mid-pattern) to stdlib ServeMux-compatible patterns (Go 1.22+).
// Handlers extract the full path from r.URL.Path themselves.
func ToStdlibPattern(pattern string) string {
	// Patterns with {path...} not at end → register the static prefix.
	// Handlers do exact matching on r.URL.Path.
	switch pattern {
	case RoutePageView: // "/p/{path...}" → "/p/"
		return "/p/"
	case RoutePageEdit: // "/edit/p/{path...}" → "/edit/p/"
		return "/edit/p/"
	case RoutePageHistory: // "/p/{path...}/history" → handled by PageView via /p/
		return "/p/"
	case RoutePageHistory + "/revert": // "/p/{path...}/history/revert" → "/p/"
		return "/p/"
	case RouteAssets: // "/assets/{path...}" → "/assets/"
		return "/assets/"
	case RouteVTT: // "/vtt/{mapID}" → "/vtt/"
		return "/vtt/"
	case RouteWizard: // "/wizard/{step}" → "/wizard/"
		return "/wizard/"
	case "/api/vtt/{mapID}/state": // plugins.StateSnapshotPath
		return "/api/vtt/"
	default:
		return pattern // exact patterns like /healthz, /search, /p/new, /upload, etc.
	}
}
