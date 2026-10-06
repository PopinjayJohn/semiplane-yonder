package web

import (
	"net/http"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/store"
)

// ReadHandlers contains all read-path HTTP handlers.
// Owned by Lane F1 (read path). Consumes markdown.Parse, Store, secrets.Filter.
// Touches no write handlers.
type ReadHandlers struct {
	Store        store.Store
	SlotRegistry *SlotRegistry
	SessionStore auth.SessionStore
}

// RegisterRoutes registers read-path routes with the registry.
func (h *ReadHandlers) RegisterRoutes(reg *RouteRegistry) {
	reg.Register(Route{
		Method:       http.MethodGet,
		Path:         RouteHealthz,
		Handler:      h.Healthz,
		ReadOnly:     true,
		AuthRequired: false,
	})
	reg.Register(Route{
		Method:       http.MethodGet,
		Path:         RouteVersion,
		Handler:      h.Version,
		ReadOnly:     true,
		AuthRequired: false,
	})
	reg.Register(Route{
		Method:         http.MethodGet,
		Path:           RoutePageView,
		Handler:        h.PageView,
		ReadOnly:       true,
		AuthRequired:   false, // guest readable, secrets filtered in handler
		SecretFiltered: true,
	})
	reg.Register(Route{
		Method:         http.MethodGet,
		Path:           RouteSearch,
		Handler:        h.Search,
		ReadOnly:       true,
		AuthRequired:   false,
		SecretFiltered: true,
	})
	reg.Register(Route{
		Method:         http.MethodGet,
		Path:           RouteGraph,
		Handler:        h.Graph,
		ReadOnly:       true,
		AuthRequired:   false,
		SecretFiltered: true,
	})
	reg.Register(Route{
		Method:         http.MethodGet,
		Path:           RouteAutocomplete,
		Handler:        h.Autocomplete,
		ReadOnly:       true,
		AuthRequired:   false,
		SecretFiltered: true,
	})
	reg.Register(Route{
		Method:         http.MethodGet,
		Path:           RouteAssets,
		Handler:        h.Assets,
		ReadOnly:       true,
		AuthRequired:   false,
		SecretFiltered: true,
	})
	reg.Register(Route{
		Method:         http.MethodGet,
		Path:           RouteSSE,
		Handler:        h.SSE,
		ReadOnly:       true,
		AuthRequired:   true,
		SecretFiltered: true,
	})
	reg.Register(Route{
		Method:       http.MethodGet,
		Path:         RouteMe,
		Handler:      h.Me,
		ReadOnly:     true,
		AuthRequired: true,
	})
	reg.Register(Route{
		Method:       http.MethodGet,
		Path:         RouteDashboard,
		Handler:      h.Dashboard,
		ReadOnly:     true,
		AuthRequired: true,
		GMOnly:       true,
	})
	reg.Register(Route{
		Method:       http.MethodGet,
		Path:         RouteVTT,
		Handler:      h.VTT,
		ReadOnly:     true,
		AuthRequired: true,
	})
}

// Healthz returns health check endpoint.
func (h *ReadHandlers) Healthz(w http.ResponseWriter, r *http.Request) {
	// not implemented
}

// Version returns version endpoint.
func (h *ReadHandlers) Version(w http.ResponseWriter, r *http.Request) {
	// not implemented
}

// PageView renders a page view.
func (h *ReadHandlers) PageView(w http.ResponseWriter, r *http.Request) {
	// not implemented
}

// Search handles search requests.
func (h *ReadHandlers) Search(w http.ResponseWriter, r *http.Request) {
	// not implemented
}

// Graph returns the page graph.
func (h *ReadHandlers) Graph(w http.ResponseWriter, r *http.Request) {
	// not implemented
}

// Autocomplete returns autocomplete suggestions.
func (h *ReadHandlers) Autocomplete(w http.ResponseWriter, r *http.Request) {
	// not implemented
}

// Assets serves vault assets through ACL-checked handler.
func (h *ReadHandlers) Assets(w http.ResponseWriter, r *http.Request) {
	// not implemented
}

// SSE handles Server-Sent Events for live updates.
func (h *ReadHandlers) SSE(w http.ResponseWriter, r *http.Request) {
	// not implemented
}

// Me returns the current user's sheet/dashboard.
func (h *ReadHandlers) Me(w http.ResponseWriter, r *http.Request) {
	// not implemented
}

// Dashboard returns the GM dashboard.
func (h *ReadHandlers) Dashboard(w http.ResponseWriter, r *http.Request) {
	// not implemented
}

// VTT returns the VTT view.
func (h *ReadHandlers) VTT(w http.ResponseWriter, r *http.Request) {
	// not implemented
}
