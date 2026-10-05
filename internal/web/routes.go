package web

import (
	"net/http"

	"github.com/semiplane/yonder/internal/auth"
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

// Middleware represents a middleware function.
type Middleware func(http.HandlerFunc, *auth.Viewer) http.HandlerFunc

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
func (r *RouteRegistry) BuildHandler(sessionStore auth.SessionStore) http.Handler {
	return nil // not implemented
}

// Core route patterns (frozen - amend only)
const (
	RouteHealthz      = "/healthz"
	RouteVersion      = "/version"
	RouteLogin        = "/login"
	RouteLogout       = "/logout"
	RouteRegister     = "/register"
	RoutePageView     = "/p/{path...}"
	RoutePageEdit     = "/p/{path...}/edit"
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
