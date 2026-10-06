package web

import (
	"context"
	"net/http"

	"github.com/semiplane/yonder/internal/auth"
	"github.com/semiplane/yonder/internal/store"
)

// WriteHandlers contains all write-path HTTP handlers.
// Owned by Lane F2 (write path). Consumes vault.WriteFile + Store API.
// Touches no read handlers.
type WriteHandlers struct {
	Store        store.Store
	Vault        VaultWriter
	SessionStore auth.SessionStore
	SlotRegistry *SlotRegistry
}

// VaultWriter defines the interface for vault write operations.
// Implemented by Lane B (vault package).
type VaultWriter interface {
	WriteFile(ctx context.Context, path, content string) error
	DeleteFile(ctx context.Context, path string) error
	RenameFile(ctx context.Context, oldPath, newPath string) error
}

// RegisterRoutes registers write-path routes with the registry.
func (h *WriteHandlers) RegisterRoutes(reg *RouteRegistry) {
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

// PageEdit renders the page editor.
func (h *WriteHandlers) PageEdit(w http.ResponseWriter, r *http.Request) {
	// not implemented
}

// PageSave handles page save.
func (h *WriteHandlers) PageSave(w http.ResponseWriter, r *http.Request) {
	// not implemented
}

// PageNew renders the new page form.
func (h *WriteHandlers) PageNew(w http.ResponseWriter, r *http.Request) {
	// not implemented
}

// PageCreate handles page creation.
func (h *WriteHandlers) PageCreate(w http.ResponseWriter, r *http.Request) {
	// not implemented
}

// PageRevert handles page revert.
func (h *WriteHandlers) PageRevert(w http.ResponseWriter, r *http.Request) {
	// not implemented
}

// Upload handles file uploads.
func (h *WriteHandlers) Upload(w http.ResponseWriter, r *http.Request) {
	// not implemented
}

// DiceRoll handles dice roll requests.
func (h *WriteHandlers) DiceRoll(w http.ResponseWriter, r *http.Request) {
	// not implemented
}

// WizardStep handles wizard steps.
func (h *WriteHandlers) WizardStep(w http.ResponseWriter, r *http.Request) {
	// not implemented
}

// EncounterAction handles encounter actions.
func (h *WriteHandlers) EncounterAction(w http.ResponseWriter, r *http.Request) {
	// not implemented
}

// VTTStateUpdate handles VTT state updates.
func (h *WriteHandlers) VTTStateUpdate(w http.ResponseWriter, r *http.Request) {
	// not implemented
}
