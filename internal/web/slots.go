package web

import (
	"github.com/semiplane/yonder/internal/auth"
)

// SlotRegistry manages UI slots for plugin/components injection.
// Slots: header-*, sidebar-left/right, footer, page-actions, sheet-header
// {component, priority, show-if}, GM-disablable, SSE-patchable, secret-filtered input.
type SlotRegistry struct {
	slots map[string][]SlotComponent
}

// SlotComponent represents a component registered in a slot.
type SlotComponent struct {
	SlotName       string // slot name (e.g., "header-left")
	ID             string
	Component      string // templ component name
	Priority       int    // higher = first
	ShowIf         ShowCondition
	GMOnly         bool
	SecretFiltered bool   // input filtered by secrets.Filter
	PluginID       string // for namespacing CSS
}

// ShowCondition defines when a slot component should be shown.
type ShowCondition struct {
	// Viewer must have all of these grants
	Grants []string
	// Viewer must not have any of these grants
	NotGrants []string
	// Custom expression (evaluated server-side)
	Expression string
	// Path prefix match
	PathPrefix string
}

// NewSlotRegistry creates a new slot registry.
func NewSlotRegistry() *SlotRegistry {
	return &SlotRegistry{
		slots: make(map[string][]SlotComponent),
	}
}

// Register adds a component to a slot.
func (r *SlotRegistry) Register(slot string, component SlotComponent) {
	r.slots[slot] = append(r.slots[slot], component)
}

// Get returns components for a slot, filtered by viewer and path.
func (r *SlotRegistry) Get(slot string, viewer *auth.Viewer, path string) []SlotComponent {
	return nil // not implemented
}

// Core slot names (frozen - amend only)
const (
	SlotHeaderLeft   = "header-left"
	SlotHeaderCenter = "header-center"
	SlotHeaderRight  = "header-right"
	SlotSidebarLeft  = "sidebar-left"
	SlotSidebarRight = "sidebar-right"
	SlotFooter       = "footer"
	SlotPageActions  = "page-actions"
	SlotSheetHeader  = "sheet-header"
)
